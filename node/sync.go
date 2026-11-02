package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// syncThenStart runs in a goroutine at Start when Bootstrap peers are configured. It fetches the
// best verified checkpoint from those peers, hands it to the loop to adopt, then starts consensus.
// If nothing valid is found (peers down, or the network is still at genesis) consensus simply
// starts at the configured height. Ordering is guaranteed by the single event channel: the adopt
// event is enqueued before the start event.
func (node *Node) syncThenStart() {
	if best, found, _ := node.bestCheckpoint(catchUpClient(), eachItsOwnPeer(node.config.Bootstrap)); found {
		node.enqueue(event{kind: eventAdopt, checkpoint: &best})
	}
	node.enqueue(event{kind: eventStart})
}

// catchUpCheckpoint is the best verified checkpoint a running node can catch up to if it falls
// behind: its bootstrap peers'; when none of them yields one (none is configured, all are down, or
// none verifies), its current GOSSIP peers'; and when none of those does either, the public faces' of
// the other validators of the committed view, at their public addresses, where the fetcher serves
// hdls/status and hdls/checkpoint (spec §6.1). So any networked node can self-recover from a peer that
// is ahead, even while gossip is blocked, as on the second path (spec §7.7), or while the other
// validators' gossip ports refuse it, as they refuse a validator dropped while it was away; and while a
// source yields a checkpoint, the ones after it are not asked. Every public face is asked only when no
// bootstrap or gossip peer answered at all, or while the node is stuck on its height (see
// pullPublicBundles): on a healthy network the gossip peers answer, at the node's own height, and every
// node asking every other bank's public face at each tick would cost them for nothing. Otherwise one
// public face is asked a tick, each in turn (publicFaceTurn): one small request a tick, which finds
// within about as many ticks as there are validators that the node lags behind banks its gossip peers
// are not among, as when the network splits. An idle node runs no driver, so it is never stuck, and
// without that it would not find out until its heartbeat starts a driver, minutes later (review R2-2).
// Any peer is a safe source: bestCheckpoint verifies a checkpoint against the validators of the node's
// own last committed state.
// The gossip peers come from the transport's Peers method, which HTTPTransport has (and, in tests,
// peeredRecorder).
func (node *Node) catchUpCheckpoint() (Checkpoint, bool) {
	client := catchUpClient()
	checkpoint, found, bootstrapPeerAnswered := node.bestCheckpoint(client, eachItsOwnPeer(node.config.Bootstrap))
	if found {
		return checkpoint, true
	}
	gossipPeerAnswered := false
	if peerProvider, isPeerProvider := node.transport.(interface{ Peers() [][]string }); isPeerProvider {
		if checkpoint, found, gossipPeerAnswered = node.bestCheckpoint(client, peerProvider.Peers()); found {
			return checkpoint, true
		}
	}
	publicAddresses := node.otherValidatorsPublicAddresses()
	if (bootstrapPeerAnswered || gossipPeerAnswered) && !node.stuckOnItsHeight.Load() && len(publicAddresses) > 0 {
		turn := node.publicFaceTurn.Add(1) % uint64(len(publicAddresses))
		publicAddresses = publicAddresses[turn : turn+1]
	}
	checkpoint, found, _ = node.bestCheckpoint(node.publicAddressClient(), eachItsOwnPeer(publicAddresses))
	return checkpoint, found
}

// catchUpClient is the client of the fetches from the bootstrap and gossip peers, at the start
// (syncThenStart) and at each catch-up tick (catchUpCheckpoint): as NewHTTPClient's, it gives up after 5
// seconds and follows no redirect, and its connections give up after peerDialTimeout, so a URL that never
// answers costs a tick 2 seconds, not 5 (review N8). Every client shares its transport, catchUpTransport,
// so that the fetches reuse their connections.
func catchUpClient() *http.Client {
	client := NewHTTPClient(5 * time.Second)
	client.Transport = catchUpTransport
	return client
}

// catchUpTransport is catchUpClient's transport.
var catchUpTransport = peerRoundTripper()

// eachItsOwnPeer is a peer list (see bestCheckpoint) in which each of urls is the one URL of a peer of
// its own: a bootstrap peer, or a validator's public address.
func eachItsOwnPeer(urls []string) [][]string {
	peers := make([][]string, len(urls))
	for i, url := range urls {
		peers[i] = []string{url}
	}
	return peers
}

// otherValidatorsPublicAddresses is the public address of each validator of the committed view, but the
// node itself, that publishes one: where the node pulls public bundles (pullPublicBundles), and the last
// source of a catch-up (catchUpCheckpoint), each through publicAddressClient. It reads only the committed
// view, so it is safe to call from a goroutine.
func (node *Node) otherValidatorsPublicAddresses() []string {
	view := node.view.Load()
	var publicAddresses []string
	for _, validator := range view.validators.Validators() {
		account, _ := ledger.FindAccount(view.state.Accounts, validator.Name)
		if validator.Name != node.self && account.PublicAddress != "" {
			publicAddresses = append(publicAddresses, account.PublicAddress)
		}
	}
	return publicAddresses
}

// publicAddressClient is the client of the fetches from the other validators' public addresses: as
// catchUpClient's, it gives up after 5 seconds, a connection after peerDialTimeout, and follows no
// redirect, and its transport, publicAddressTransport, refuses to connect to the node's own machine. A
// public address is on chain, so any validator chooses its own: one that published
// http://127.0.0.1:8080 would make every bank stuck at a height fetch its own operator port's routes, up
// to a 64 MiB export that bank's loop builds, and so would one that published an address every bank
// has, such as rootless Podman's 10.0.2.100. Another private address is reached as any other: the
// quickstart's banks have them. A test's config may let loopback through (allowLoopbackPublicAddresses).
func (node *Node) publicAddressClient() *http.Client {
	client := NewHTTPClient(5 * time.Second)
	if !node.config.allowLoopbackPublicAddresses {
		client.Transport = publicAddressTransport
	}
	return client
}

// publicAddressTransport is publicAddressClient's transport. Its dialer, dialPublicAddress, refuses the
// node's own machine as a destination. It uses no proxy, so the address it connects to is the
// destination's. Every client shares it, so that the fetches reuse their connections.
var publicAddressTransport = &http.Transport{
	DialContext:     dialPublicAddress,
	IdleConnTimeout: 90 * time.Second,
}

// publicAddressDialer is dialPublicAddress's. A connection gives up after peerDialTimeout. Its Control
// refuses a loopback destination (refuseLoopback): it runs on the address the connection goes to, once a
// name is resolved, so a name that resolves to 127.0.0.1 is refused too, before a packet is sent.
var publicAddressDialer = &net.Dialer{Timeout: peerDialTimeout, Control: refuseLoopback}

// dialPublicAddress connects as publicAddressDialer does, then drops a connection whose remote address is
// its local one (bothEndsOneAddress): the node reaching its own machine through one of its own addresses
// that is not loopback (review N7). Such an address is on every interface of a machine whose operator
// port listens on :8080, as in a container, and some are every bank's: rootless Podman's 10.0.2.100, or
// the first container's on a Docker bridge. No other machine's address is the node's own.
func dialPublicAddress(dialContext context.Context, network, address string) (net.Conn, error) {
	connection, err := publicAddressDialer.DialContext(dialContext, network, address)
	if err == nil && bothEndsOneAddress(connection) {
		connection.Close()
		return nil, fmt.Errorf("refused: %s is this host's own address, and a public address never is", address)
	}
	return connection, err
}

// refuseLoopback is publicAddressDialer's Control: it refuses a connection to a loopback address, or to
// the unspecified one (0.0.0.0, ::), which reaches the node's own host too.
func refuseLoopback(network, address string, _ syscall.RawConn) error {
	addressAndPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	if ip := addressAndPort.Addr().Unmap(); ip.IsLoopback() || ip.IsUnspecified() {
		return fmt.Errorf("refused: %s is this host's own address, and a public address never is", address)
	}
	return nil
}

// catchUpLoop runs alongside the started node and periodically fetches catchUpCheckpoint, every
// catchUpInterval. If it is ahead of us — we missed a commit window — it is handed to the loop to
// fast-forward. bestCheckpoint is anchored on the committed view the loop last published (race-free in
// this goroutine); the loop re-verifies against the view it holds before adopting.
func (node *Node) catchUpLoop() {
	node.everyInterval(node.catchUpInterval, func() {
		if checkpoint, found := node.catchUpCheckpoint(); found {
			node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})
		}
	})
}

// catchUpInterval is the cadence of catchUpLoop: five block intervals, as the committed view governs
// them, and at least 3 s.
func (node *Node) catchUpInterval() time.Duration {
	fiveBlockIntervals := 5 * time.Duration(node.view.Load().state.Parameters.BlockIntervalMs) * time.Millisecond
	return max(fiveBlockIntervals, 3*time.Second)
}

// everyInterval calls action every interval(), until the node stops. It wakes at least once a second
// and asks interval() again, so once a ⅔ vote changes the timings, the cadence follows within a second:
// a node that ran at a day per step does not wait out the day after a vote lowers it.
func (node *Node) everyInterval(interval func() time.Duration, action func()) {
	last := time.Now()
	for {
		select {
		case <-node.quit:
			return
		case <-time.After(min(interval()-time.Since(last), time.Second)):
		}
		if time.Since(last) >= interval() {
			action()
			last = time.Now()
		}
	}
}

// reGossipLoop periodically asks the loop to re-broadcast the current height's votes and the current
// round's proposal. Both are otherwise sent once, so a partition can leave the two sides holding
// disjoint vote sets; on merge, without re-exchange, neither side reaches quorum-any and the round
// never advances — a liveness stall (never a safety break) the adversarial harness surfaced. And a
// proposal whose one broadcast is LOST used to burn its whole round timing out to nil; re-sending it
// lets the same round still decide. The cadence is reGossipInterval, the governed step timeout:
// frequent enough to unstick a merged network within a few rounds. A tick is not cheap. While a driver
// runs (without one nothing is re-gossiped), each tick sends every peer every vote the node holds for
// the height, about 4.7 KB of JSON each with ML-DSA-65; the last commit's precommits and its
// decided proposal, block included (lastCommitMessages); and the current round's proposal, block
// included. A receiver reads and decodes each proposal, up to 2 MiB, before it can drop a copy it
// holds or one of a height it has left. The loop's eventRegossip handler does the send (reGossip),
// writes the public bundle (publishBundle), and pulls the other validators' when the height is stuck
// (pullPublicBundles).
func (node *Node) reGossipLoop() {
	node.everyInterval(node.reGossipInterval, func() { node.enqueue(event{kind: eventRegossip}) })
}

// reGossipInterval is the cadence of reGossipLoop, and, unless the slow-down switch is longer, half the
// wait of a node back without its sign log (see ownSignaturesWait): the step timeout of the committed
// view, so it follows a ⅔ vote.
func (node *Node) reGossipInterval() time.Duration {
	milliseconds := node.view.Load().state.Parameters.StepTimeoutMs
	if milliseconds <= 0 {
		return time.Second
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// pullPublicBundles is the second path (spec §7.7), run on the loop at each re-gossip tick. Once the
// driver of the height in progress has run for 4 step timeouts without deciding it, the node fetches
// the public bundle (see publishBundle) of every other current validator, of the committed view's, that
// publishes a public address, and imports each, which takes its votes and proposals as it takes gossip.
// So where direct gossip is blocked but the banks' public faces still get through, the banks exchange
// votes and proposals all the same. Every message is signed, so the path needs no trust, and the allow
// list is not involved: the pull goes out, it doesn't come in. The import checks each vote and proposal
// off the loop first, and stops at the first line that fails that check, is unreadable, or is refused
// (see pulledCheck and importBundle), so a hostile public face spends the loop's time on no forged vote
// or proposal, and on one line of anything else.
//
// The node pulls again at each tick until a commit or a catch-up ends the height; then no driver runs
// until the next height starts, and that height's driver counts its own 4 step timeouts. An idle node
// runs no driver, so it never pulls, and its count starts when work or the heartbeat starts the driver
// (startHeight sets heightStartedAt). The 4 step timeouts are the governed step timeout, the re-gossip
// interval, and not the slow-down switch's: with the switch on, the round timers wait for a carrier, and
// the pull starts as soon as on a network that runs at full speed.
//
// The decision is the loop's, and the fetches run off it, one goroutine for each validator, so a slow
// public face holds up no other, and the loop not at all. One round of fetches runs at a time: a tick
// that finds the last round still running skips its own, so a public face that hangs holds the pulls up
// for the client's 5 seconds at most, and never piles up goroutines tick after tick. The round is
// untracked, as the backup mirror is (see saveSnapshot): it may outlive Stop, and the stopped loop
// handles nothing it hands over.
func (node *Node) pullPublicBundles() {
	stuck := node.driver != nil && time.Since(node.heightStartedAt) >= 4*node.reGossipInterval()
	if stuck != node.pullingPublicBundles {
		node.pullingPublicBundles = stuck
		node.stuckOnItsHeight.Store(stuck)
		if stuck {
			log.Printf("public bundles: height %d has run for 4 step timeouts without a commit: the node pulls the other validators' public bundles until it commits", node.height())
		} else {
			log.Printf("public bundles: the node stops pulling them, having committed height %d", node.state.Height)
		}
	}
	if !stuck || !node.pullRoundRunning.CompareAndSwap(false, true) {
		return // not stuck, or the last round still runs
	}
	publicAddresses, client := node.otherValidatorsPublicAddresses(), node.publicAddressClient()
	go func() {
		defer node.pullRoundRunning.Store(false)
		var fetches sync.WaitGroup
		for _, publicAddress := range publicAddresses {
			fetches.Go(func() {
				if bundle := fetchPublicBundle(client, publicAddress); bundle != nil {
					node.importBundle(bundle, node.pulledCheck()) // a bundle it refuses whole, of another network, is nothing too
				}
			})
		}
		fetches.Wait()
	}()
}

// pulledCheck is the check each vote and each proposal of a pulled public bundle passes in the pull's
// goroutine, before the import hands it to the loop (see importBundle). It is of the height in progress,
// whose set is the committed view's validators, or of the last committed height, whose set signed the
// last commit. A vote is by a validator of that set, and its signature verifies under the key the set
// holds for the name, a check made once a height (see verifiedVotes). A proposal names the proposer the
// set schedules for its height and round, and its signature verifies under that proposer's key
// (fromItsProposer), as onProposal checks it. The sets are the committed view's as the import of the
// bundle starts. Otherwise a hostile public face, the bank's CDN or anyone on a plain-HTTP path to it,
// could fill 8 MiB with about 1,800 forged votes or 1,600 forged proposals, each of which costs the loop
// a signature check, or with 300,000 small ones, at every pull while the node is stuck. Any other
// message passes: its intake checks it.
//
// A public bundle holds what decided the publisher's last height, then its votes and proposal of the
// height after. Every vote and proposal of a bank at the node's height passes. Of a bank one height
// ahead, the last commit passes, which decides the node's height, and the import stops at the first
// message of the height after, which the node takes at its pulls there. Of a bank further ahead or behind,
// none passes, and none would count: catch-up brings the node up. No honest bundle holds another vote or
// proposal that fails, so the import stops at the first that fails, and a forged bundle costs one
// signature check. It reads only immutable config and the committed view, so it is safe to call from a
// goroutine.
func (node *Node) pulledCheck() func(Message) bool {
	view := node.view.Load()
	setOf := func(height int64) *consensus.ValidatorSet {
		switch height {
		case view.state.Height + 1:
			return view.validators
		case view.state.Height:
			return view.lastCommitValidators
		}
		return nil
	}
	return func(message Message) bool {
		switch message.Type {
		case MessageVote:
			vote := *message.Vote
			signers := setOf(vote.Height)
			return signers != nil && signers.Contains(vote.Validator) && node.pulledVotes.verifiedOnce(view.state.Height, vote, func() bool {
				return vote.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, signers.PublicKey(vote.Validator))
			})
		case MessageProposal:
			proposers := setOf(message.Proposal.Proposal.Height)
			return proposers != nil && node.fromItsProposer(message.Proposal.Proposal, proposers)
		}
		return true
	}
}

// verifiedVotes are the votes of pulled public bundles whose signatures verified at one committed
// height, so that a pull does not verify one again (review R2-4): a public bundle holds the same votes
// pull after pull, as every bank's does, about 1,200 with 21 banks, and each check costs half a
// millisecond. A vote is remembered whole, signature and signed fields, since a copy of its signature
// on another field verifies no longer. The pulls of a round run on a goroutine each, so a mutex guards
// them, held while a vote is checked, so that a vote two banks' bundles carry is checked once, not once
// by each. They start again when the committed height changes, and when they number maxVerifiedVotes,
// about twice what a public bundle holds, so that a validator that signs vote after vote cannot make them
// grow without bound.
type verifiedVotes struct {
	mutex  sync.Mutex
	height int64 // the committed height the votes verified at
	votes  map[consensus.Vote]bool
}

const maxVerifiedVotes = 4096

// verifiedOnce reports whether vote verifies: true if it verified before at committedHeight, and
// otherwise what verify reports, which it remembers.
func (memory *verifiedVotes) verifiedOnce(committedHeight int64, vote consensus.Vote, verify func() bool) bool {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	if memory.votes == nil || memory.height != committedHeight || len(memory.votes) >= maxVerifiedVotes {
		memory.height, memory.votes = committedHeight, make(map[consensus.Vote]bool)
	}
	if !memory.votes[vote] && verify() {
		memory.votes[vote] = true
	}
	return memory.votes[vote]
}

// bestCheckpoint fetches a checkpoint from each peer that is ahead of the node, keeps only those that
// verifyCheckpoint accepts with the validators of the node's last committed state as the trust anchor,
// and returns the one at the highest committed height. It asks each peer's height first, and fetches a
// checkpoint, about 2.9 KB per account with ML-DSA-65, only from a peer whose height is ahead (spec
// 6.6): on an idle chain every peer is at the node's height, tick after tick. A peer is the list of base
// URLs one node is reached at, as for HTTPTransport.Broadcast: the operator's, one made of a validator's
// IP address, which the ledger takes only in its one plain form, and the gossip port, or a validator's
// public address. The node asks a peer at the first of its URLs that answers its status, and at no
// other: they reach one node, so a peer ahead sends its checkpoint once. The client is the caller's,
// and follows no redirect (NewHTTPClient).
// It also reports whether any peer answered its status at all, ahead or not. It reads only immutable
// config and the committed view the loop publishes (see publishView), so it is safe to call from a
// goroutine.
func (node *Node) bestCheckpoint(client *http.Client, peers [][]string) (best Checkpoint, found bool, anyPeerAnswered bool) {
	view := node.view.Load()
	anchor := view.validators
	for _, peerURLs := range peers {
		for _, peerURL := range peerURLs {
			peerHeight, err := fetchHeight(client, peerURL)
			if err != nil {
				continue // no answer: the peer's next URL
			}
			anyPeerAnswered = true
			if peerHeight > view.state.Height+1 { // ahead of the height in progress
				checkpoint, err := fetchCheckpoint(client, peerURL, MaxCheckpointBytes)
				// The anchor filter must stay even though adopt/catchUp re-verify against the live state in
				// the loop: without it a forged checkpoint at a higher height would win the pick. It refuses an
				// invalid quorum, a block hash mismatch, or a set no trusted validator vouches for.
				if err == nil && node.verifyCheckpoint(checkpoint, anchor) && (!found || checkpoint.Commit.Height > best.Commit.Height) {
					best, found = checkpoint, true
				}
			}
			break // the peer answered: its other URLs reach the same node
		}
	}
	return best, found, anyPeerAnswered
}

// MaxCheckpointBytes bounds a fetched checkpoint. It is generous, because an honest checkpoint holds
// every account plus a commit certificate and soon outgrows the 1 MiB intake bound, but finite: the
// body is read before anything in it is verified, so one catch-up source must not be able to make
// this node read and decode without limit.
const MaxCheckpointBytes = 64 << 20

// fetchHeight GETs the height in progress a peer's /hdls/status reports: one above its committed height.
func fetchHeight(client *http.Client, peer string) (int64, error) {
	response, err := client.Get(peer + "/hdls/status")
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, errors.New("status fetch: " + response.Status)
	}
	var status struct {
		HeightInProgress int64 `json:"heightInProgress"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(&status)
	return status.HeightInProgress, err
}

// fetchCheckpoint GETs a peer's committed checkpoint. A non-200 answer is refused, and only maxBytes of
// the body are read, so a larger one fails to decode; either way the source is skipped.
func fetchCheckpoint(client *http.Client, peer string, maxBytes int64) (Checkpoint, error) {
	response, err := client.Get(peer + "/hdls/checkpoint")
	if err != nil {
		return Checkpoint{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Checkpoint{}, errors.New("checkpoint fetch: " + response.Status)
	}
	var checkpoint Checkpoint
	if err := json.NewDecoder(io.LimitReader(response.Body, maxBytes)).Decode(&checkpoint); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// fetchPublicBundle GETs the public bundle a validator's public face serves (spec §7.7), or nil when
// there is none to read: the request fails, the answer is not 200, or the body can't be read. The ledger
// takes a public address only in its one plain form, with no path (ledger.CanonicalAddress), so the
// route is the whole path. Only MaxPublicBundleBytes of the body are read: of a larger one, the import
// takes the whole lines, and counts the line cut short as unreadable.
func fetchPublicBundle(client *http.Client, publicAddress string) []byte {
	response, err := client.Get(publicAddress + "/hdls/bundle")
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	bundle, err := io.ReadAll(io.LimitReader(response.Body, MaxPublicBundleBytes))
	if err != nil {
		return nil
	}
	return bundle
}

// adopt fast-forwards to a verified checkpoint if it is ahead of us and consensus has not started
// yet (a fresh or rebooting node). The certificate is verified against the set it carries, which the
// node's own validators must vouch for, and must be for the block hash the header and the digest of the
// supplied state give, so we trust the ⅔ quorum, not the node that served it.
// The checkpoint is saved as the node's snapshot before the node votes on top of it, as after catchUp:
// otherwise its sign log would run ahead of a snapshot of an older height, and Open would refuse to
// restart it (see loadSignLog). Once it is on disk, the sign log of the height the node opened at is
// emptied in place, at that height: a log for the height the node now starts at would end the wait of
// a node back without one (startFirstHeight), and a log restored with an older snapshot cannot vouch
// for the heights above it.
func (node *Node) adopt(checkpoint Checkpoint) {
	if node.started {
		return
	}
	if checkpoint.Commit.Height+1 <= node.height() {
		return // not ahead of our configured/current height
	}
	if !node.verifyCheckpoint(checkpoint, node.view.Load().validators) {
		return
	}
	node.fastForward(checkpoint)
	if node.saveSnapshot() == nil {
		node.emptySignLog(node.signLog.Height)
	}
}

// catchUp lets a RUNNING node that fell behind (missed a commit window) fast-forward to a peer's
// later verified checkpoint and resume consensus at the new height, instead of staying stuck. Stale
// timers/messages from the abandoned height are dropped by their height/round guards. From a height it
// may have signed at before it lost its sign log, the node may have signed at the network's height too,
// so it waits there before it starts it (see waitForOwnSignatures), and empties its sign log at the log's
// own height, as adopt does, so that a restart during the wait waits too. It returns nil once it has
// fast-forwarded, or the reason it did not, which ImportCheckpoint answers. A checkpoint of another
// network does not verify: every signature binds the network id, and the checkpoint carries no other
// mark of its network, so the reason names both.
func (node *Node) catchUp(checkpoint Checkpoint) error {
	if !node.started {
		return errors.New("the node is still starting: try again once it runs") // pre-start is adopt()'s job
	}
	if checkpoint.Commit.Height+1 <= node.height() {
		return fmt.Errorf("not ahead: the checkpoint is of height %d, and this node has committed height %d", checkpoint.Commit.Height, node.state.Height)
	}
	if !node.verifyCheckpoint(checkpoint, node.view.Load().validators) {
		return errors.New("does not verify: it is not a state this network's validators signed, so it is another network's checkpoint, or damaged")
	}
	waits := node.height() <= node.mayHaveSignedUpTo
	node.fastForward(checkpoint)
	logHeight := node.height()
	if waits {
		logHeight = node.signLog.Height
	}
	if node.saveSnapshot() == nil { // before the node votes at the new height (see adopt)
		node.emptySignLog(logHeight)
	}
	if waits {
		node.waitForOwnSignatures()
		return nil
	}
	node.startHeight()
	return nil
}

// ImportCheckpoint takes a checkpoint a carrier brought (spec §7.5) through the event catch-up uses: the
// loop verifies it as catch-up does, with the validators of the node's last committed state as the
// anchor, and adopts it if it is ahead. It returns nil once the node has adopted it, or the reason it did
// not (see catchUp).
func (node *Node) ImportCheckpoint(checkpoint Checkpoint) error {
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint, admit: verdict})
	_, err := node.awaitAdmission(verdict)
	return err
}

// verifyCheckpoint reports whether a peer's checkpoint is trustworthy from THIS node's vantage, the
// trust anchor being the validator set (name, key, power) of the node's own committed state. It runs
// the four steps of spec 4.14 in order, each only if the one before passed: the shape and the hashes,
// which need no signature (Checkpoint.Consistent: the block hash recomputed from the header and the
// state's digest is the one the commit decided, and the set the checkpoint carries hashes to the
// header's); then more than 1/3 of the anchor's power vouches for it (anchorVouches), so at least one
// validator we already trust signed it; and last, the commit is a valid > 2/3 quorum of the set the
// checkpoint carries, the set that signed it. So a set padded with made-up validators fails the hashes,
// and a forged header the anchor, before any member of the set is verified. This is the BFT "skipping"
// rule: it lets a node safely adopt a checkpoint whose validator set changed while it was away, without
// trusting the serving peer. (Full rotation past the anchor would need a chain of checkpoints; for
// incremental change the anchor overlaps the signing set — the real case.)
func (node *Node) verifyCheckpoint(checkpoint Checkpoint, anchor *consensus.ValidatorSet) bool {
	signers := consensus.NewValidatorSet(checkpoint.Validators)
	return checkpoint.Consistent() && node.anchorVouches(checkpoint, signers, anchor) &&
		checkpoint.Commit.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, signers)
}

// anchorVouches reports whether names of the anchor holding more than 1/3 of its power signed the
// checkpoint's commit, each under the key the anchor holds for it. A vote counts only when it is one of
// the commit's, a precommit for its height, round and block hash, and when the set the checkpoint
// carries gives its name the anchor's key, so neither a vote for another block nor a forged set that
// gives real names keys of the forger's can borrow their power. Only a name's first such vote is
// checked, whether or not it verifies, so copies of one name's vote cost one signature check, not one
// each (spec 4.14 step 3); a commit that names a name twice fails Commit.Verify anyway.
func (node *Node) anchorVouches(checkpoint Checkpoint, signers, anchor *consensus.ValidatorSet) bool {
	commit := checkpoint.Commit
	checked := make(map[string]bool)
	var vouched int64
	for _, vote := range commit.Votes {
		anchorKey := anchor.PublicKey(vote.Validator)
		ofTheCommit := vote.Type == consensus.Precommit && vote.Height == commit.Height && vote.Round == commit.Round && vote.BlockHash == commit.BlockHash
		if anchorKey == "" || !ofTheCommit || checked[vote.Validator] || signers.PublicKey(vote.Validator) != anchorKey {
			continue
		}
		checked[vote.Validator] = true
		if vote.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, anchorKey) {
			vouched += anchor.Power(vote.Validator)
		}
	}
	return vouched*3 > anchor.TotalPower()
}
