package node

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// syncThenStart runs in a goroutine at Start when Bootstrap peers are configured. It finds the best
// checkpoint as catch-up does (catchUpCheckpoint), hands it to the loop to adopt, then starts consensus.
// If nothing valid is found (peers down, or the network is still at genesis) consensus simply starts at
// the configured height. Ordering is guaranteed by the single event channel: the adopt event is enqueued
// before the start event.
func (node *Node) syncThenStart() {
	if best, trustedFrame, found := node.catchUpCheckpoint(); found {
		node.enqueue(event{kind: eventAdopt, checkpoint: &best, trustedFrame: trustedFrame})
	}
	node.enqueue(event{kind: eventStart})
}

// catchUpCheckpoint is the best checkpoint a node can catch up to, at its start (syncThenStart) or once it
// falls behind (catchUpLoop), with the frame it passed against (see bestCheckpoint). It asks every node it
// knows of (Phase 4 spec §4.4): its bootstrap peers; its current GOSSIP peers; and every other validator of
// its trusted frame, the committed view's set, at its public address, where the fetcher serves hdls/status
// and hdls/checkpoint (spec §6.1). So a peer that withholds the newest checkpoint is outvoted by the
// others, and any networked node can self-recover from a peer that is ahead, even while gossip is blocked,
// as on the second path (spec §7.7), or while the other validators' gossip ports refuse it, as they refuse
// a validator dropped while it was away. Every one is asked at every tick: a status is a small file the
// fetcher serves from the public folder, behind a CDN, and a checkpoint is fetched only from a node ahead
// or on another block (review R2-2 asked one public face a tick; the Phase 4 spec asks them all, Q14).
// The gossip peers come from the transport's Peers method, which HTTPTransport has (and, in tests,
// peeredRecorder).
func (node *Node) catchUpCheckpoint() (Checkpoint, *consensus.ValidatorSet, bool) {
	var peers []catchUpPeer
	client, publicAddressClient := catchUpClient(), node.publicAddressClient()
	for _, bootstrapURL := range node.config.Bootstrap {
		peers = append(peers, catchUpPeer{urls: []string{bootstrapURL}, client: client})
	}
	if peerProvider, isPeerProvider := node.transport.(interface{ Peers() [][]string }); isPeerProvider {
		for _, gossipURLs := range peerProvider.Peers() {
			peers = append(peers, catchUpPeer{urls: gossipURLs, client: client})
		}
	}
	for _, publicAddress := range node.otherValidatorsPublicAddresses() {
		peers = append(peers, catchUpPeer{urls: []string{publicAddress}, client: publicAddressClient})
	}
	return node.bestCheckpoint(peers)
}

// catchUpPeer is one node catch-up asks: the base URLs it is reached at, in the order they are tried, and
// the client it is asked through. A bootstrap peer has one URL, the operator's, and a gossip peer one made
// of each IP address the validator publishes, which the ledger takes only in its one plain form, and the
// gossip port, as for HTTPTransport.Broadcast; both are asked through catchUpClient's client. A public
// face has its public address, asked through publicAddressClient's.
type catchUpPeer struct {
	urls   []string
	client *http.Client
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

// otherValidatorsPublicAddresses is the public address of each validator of the committed view, but the
// node itself, that publishes one: where the node pulls public bundles (pullPublicBundles), asks for a
// checkpoint to catch up to (catchUpCheckpoint) and reads the frames it walks (walkFrames), each through
// publicAddressClient. It reads only the committed
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
// fast-forward, with the frame it passed against. bestCheckpoint starts from the committed view the loop
// last published (race-free in this goroutine); the loop re-verifies against that frame before adopting.
// A halted node asks nothing: it adopts nothing (see halt).
func (node *Node) catchUpLoop() {
	node.everyInterval(node.catchUpInterval, func() {
		if node.haltedBecause.Load() != nil {
			return
		}
		if checkpoint, trustedFrame, found := node.catchUpCheckpoint(); found {
			node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint, trustedFrame: trustedFrame})
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

// bestCheckpoint asks each peer its status, all at once (askStatuses), and fetches a checkpoint, about 2.9
// KB per account with ML-DSA-65, only from a peer at the node's height or ahead of it, the highest first,
// that reports a block it does not know (spec 6.6). It keeps a checkpoint only when it passes against the
// node's trusted frame, directly or by a walk of the frames, or, at the node's own height, against the set
// that signed the node's block there (trustedFrameOf). Of those, it returns the highest one ahead of the
// node, with the frame it passed against. It knows the node's own last block from the start: on an idle
// chain every peer is at the node's height, on that block, tick after tick, and none is fetched. Once a
// checkpoint has passed, a peer that reports its block is not fetched, nor is a peer below it, but for
// one at the node's height, so the honest peers at the network's height cost one fetch. A peer that
// reports another block at a height whose block it knows is fetched and checked: if that one passes too,
// the two are the fork alarm (raiseForkAlarm), or, at the node's own height, the divergence alarm
// (raiseDivergenceAlarmAt), and none is returned. A peer that serves another height than it reports, or a
// checkpoint that does not pass, costs its fetch, and the next one is asked, so a peer that lies about
// its height holds up no other. The client is each peer's, and follows no redirect (NewHTTPClient). It
// reads only immutable config and the committed view the loop publishes (see publishView), so it is safe
// to call from a goroutine.
func (node *Node) bestCheckpoint(peers []catchUpPeer) (best Checkpoint, trustedFrame *consensus.ValidatorSet, found bool) {
	view := node.view.Load()
	// The node's own block, and the commit of each height a checkpoint that passed is of.
	passed := map[int64]consensus.Commit{view.state.Height: {Height: view.state.Height, BlockHash: view.lastBlockHash}}
	for _, status := range askStatuses(peers) {
		if status.heightInProgress <= view.state.Height {
			break // behind the node: nor is any peer after it
		}
		if found && status.heightInProgress <= best.Commit.Height && status.heightInProgress != view.state.Height+1 {
			continue // below the best, and not at the node's height
		}
		if commit, seen := passed[status.heightInProgress-1]; seen && hex.EncodeToString(commit.BlockHash[:]) == status.lastBlockHash {
			continue // the node's own block, or the block of a checkpoint that passed
		}
		checkpoint, err := fetchCheckpoint(status.client, status.url, MaxCheckpointBytes)
		if err != nil {
			continue
		}
		// The frame must be checked here even though adopt/catchUp check it again on the loop: without it a
		// forged checkpoint at a higher height would win the pick.
		frame := node.trustedFrameOf(checkpoint, view)
		if frame == nil {
			continue
		}
		if other, seen := passed[checkpoint.Commit.Height]; seen && other.BlockHash != checkpoint.Commit.BlockHash {
			if checkpoint.Commit.Height == view.state.Height {
				node.raiseDivergenceAlarmAt(checkpoint.Commit, view.lastBlockHash)
			} else {
				node.raiseForkAlarm(other, checkpoint.Commit)
			}
			return Checkpoint{}, nil, false
		}
		passed[checkpoint.Commit.Height] = checkpoint.Commit
		if checkpoint.Commit.Height > view.state.Height && (!found || checkpoint.Commit.Height > best.Commit.Height) {
			best, trustedFrame, found = checkpoint, frame, true
		}
	}
	return best, trustedFrame, found
}

// peerStatus is a peer's answer to its status: the URL it answered at, its height in progress, and the
// hash of its last block, in hex.
type peerStatus struct {
	catchUpPeer
	url              string
	heightInProgress int64
	lastBlockHash    string
}

// askStatuses asks each peer its status, each in a goroutine of its own, so a peer that does not answer
// holds up no other, and returns the answers, the highest height first, and in the order of peers among
// equal heights. A peer is asked at the first of its URLs that answers its status, and at no other: they
// reach one node, so a peer ahead sends its checkpoint once.
func askStatuses(peers []catchUpPeer) []peerStatus {
	answers := make([]*peerStatus, len(peers))
	var asks sync.WaitGroup
	for i, peer := range peers {
		asks.Go(func() {
			for _, peerURL := range peer.urls {
				if heightInProgress, lastBlockHash, err := fetchStatus(peer.client, peerURL); err == nil {
					answers[i] = &peerStatus{catchUpPeer: peer, url: peerURL, heightInProgress: heightInProgress, lastBlockHash: lastBlockHash}
					return // the peer answered: its other URLs reach the same node
				}
			}
		})
	}
	asks.Wait()
	var statuses []peerStatus
	for _, answer := range answers {
		if answer != nil {
			statuses = append(statuses, *answer)
		}
	}
	slices.SortStableFunc(statuses, func(first, second peerStatus) int {
		return cmp.Compare(second.heightInProgress, first.heightInProgress)
	})
	return statuses
}

// trustedFrameOf is the frame checkpoint passes against (Phase 4 spec §4.2, §4.3), or nil: the committed
// view's set, the frame the node trusts, when more than ⅔ of it signed the checkpoint; otherwise, when the
// checkpoint is sound on its own (selfConsistent), the frame a walk from it reaches (walkFrames). A
// checkpoint of the committed height passes against the set that signed the node's own block there, and
// no other: the committed view's set signs the next height, and is another set when the node's block
// changed it, so the network's commit of another block there would fail against it, and raise no
// divergence alarm (see bestCheckpoint). It reads only immutable config and the committed view, so it is
// safe to call from a goroutine.
func (node *Node) trustedFrameOf(checkpoint Checkpoint, view *committedView) *consensus.ValidatorSet {
	if checkpoint.Commit.Height == view.state.Height {
		if view.lastCommitValidators == nil || !node.verifyCheckpoint(checkpoint, view.lastCommitValidators) {
			return nil // no set signed the genesis
		}
		return view.lastCommitValidators
	}
	if node.verifyCheckpoint(checkpoint, view.validators) {
		return view.validators
	}
	if !node.selfConsistent(checkpoint) {
		return nil
	}
	return node.walkFrames(checkpoint, view)
}

// MaxCheckpointBytes bounds a fetched checkpoint. It is generous, because an honest checkpoint holds
// every account plus a commit certificate and soon outgrows the 1 MiB intake bound, but finite: the
// body is read before anything in it is verified, so one catch-up source must not be able to make
// this node read and decode without limit.
const MaxCheckpointBytes = 64 << 20

// fetchStatus GETs what a peer's /hdls/status reports: its height in progress, one above its committed
// height, and the hash of its last block, in hex.
func fetchStatus(client *http.Client, peer string) (int64, string, error) {
	response, err := client.Get(peer + "/hdls/status")
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, "", errors.New("status fetch: " + response.Status)
	}
	var status struct {
		HeightInProgress int64  `json:"heightInProgress"`
		LastBlockHash    string `json:"lastBlockHash"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(&status)
	return status.HeightInProgress, status.LastBlockHash, err
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
// yet (a fresh or rebooting node). The certificate is verified against the set it carries, more than ⅔
// of the frame the node trusts must have signed it, and it must be for the block hash the header and
// the digest of the supplied state give, so we trust the ⅔ quorum, not the node that served it. The
// frame is trustedFrame, the one catch-up found the checkpoint passes against, its own validators' or one
// a walk of the frames reached from them (see trustedFrameOf), or, when it is nil, its own validators'.
// A halted node adopts none (see halt), and a seated one whose height in progress the checkpoint commits a
// block of that does not build on its last block raises the divergence alarm (divergesFrom).
// The checkpoint is saved as the node's snapshot before the node votes on top of it, as after catchUp:
// otherwise its sign log would run ahead of a snapshot of an older height, and Open would refuse to
// restart it (see loadSignLog). Once it is on disk, the sign log of the height the node opened at is
// emptied in place, at that height: a log for the height the node now starts at would end the wait of
// a node back without one (startFirstHeight), and a log restored with an older snapshot cannot vouch
// for the heights above it.
func (node *Node) adopt(checkpoint Checkpoint, trustedFrame *consensus.ValidatorSet) {
	if node.started || node.haltedBecause.Load() != nil {
		return
	}
	if checkpoint.Commit.Height+1 <= node.height() {
		return // not ahead of our configured/current height
	}
	if trustedFrame == nil {
		trustedFrame = node.view.Load().validators
	}
	if !node.verifyCheckpoint(checkpoint, trustedFrame) || node.divergesFrom(checkpoint) {
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
// fast-forwarded, or the reason it did not, which ImportCheckpoint answers. The checkpoint is verified as
// adopt verifies it, against trustedFrame or, when it is nil, the node's own validators. A halted node
// takes none (see halt), and one of its height in progress that does not build on its last block raises
// the divergence alarm, as in adopt (divergesFrom). A checkpoint of
// another network does not verify: every signature binds the network id, and the checkpoint carries no
// other mark of its network, so the reason names both.
func (node *Node) catchUp(checkpoint Checkpoint, trustedFrame *consensus.ValidatorSet) error {
	if !node.started {
		return errors.New("the node is still starting: try again once it runs") // pre-start is adopt()'s job
	}
	if alarm := node.haltedBecause.Load(); alarm != nil {
		return fmt.Errorf("the node is halted until its operator restarts it, by the %s", *alarm)
	}
	if checkpoint.Commit.Height+1 <= node.height() {
		return fmt.Errorf("not ahead: the checkpoint is of height %d, and this node has committed height %d", checkpoint.Commit.Height, node.state.Height)
	}
	if trustedFrame == nil {
		trustedFrame = node.view.Load().validators
	}
	if !node.verifyCheckpoint(checkpoint, trustedFrame) {
		return errors.New("does not verify: it is not a state this network's validators signed, so it is another network's checkpoint, or damaged")
	}
	if node.divergesFrom(checkpoint) {
		return errors.New("diverges: more than ⅔ of the set signed a block of this node's height in progress that does not build on its last block, and the node halts")
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
// anchor, and adopts it if it is ahead. It walks no frames: a carrier brings no record, so more than ⅔ of
// those validators must have signed it. It returns nil once the node has adopted it, or the reason it did
// not (see catchUp).
func (node *Node) ImportCheckpoint(checkpoint Checkpoint) error {
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint, admit: verdict})
	_, err := node.awaitAdmission(verdict)
	return err
}

// verifyCheckpoint reports whether a peer's checkpoint is trustworthy from THIS node's vantage, the
// trust anchor being a frame the node trusts (Phase 4 spec §4.1): the validator set (name, key, power)
// of the node's own committed state, or one a walk of the frames reached from it (walkFrames). It runs
// the four steps of spec 4.14 in order, each only if the one before passed: the shape and the hashes,
// which need no signature (Checkpoint.Consistent: the block hash recomputed from the header and the
// state's digest is the one the commit decided, and the set the checkpoint carries hashes to the
// header's); then more than ⅔ of the anchor's power vouches for it (anchorVouches); and last, the commit
// is a valid > ⅔ quorum of the set the checkpoint carries, the set that signed it. So a set padded with
// made-up validators fails the hashes, and a forged header the anchor, before any member of the set is
// verified. It lets a node adopt a checkpoint whose validator set changed while it was away, as long as
// more than ⅔ of the frame it trusts still signs, without trusting the serving peer: forging a state
// takes the keys of more than ⅔ of the power of a set the node already trusts (Phase 4 spec §4.2). When
// the set changed more than that, the node walks from frame to frame.
func (node *Node) verifyCheckpoint(checkpoint Checkpoint, anchor *consensus.ValidatorSet) bool {
	signers := consensus.NewValidatorSet(checkpoint.Validators)
	return checkpoint.Consistent() && node.anchorVouches(checkpoint.Commit, signers, anchor) &&
		checkpoint.Commit.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, signers)
}

// anchorVouches reports whether names of the anchor holding more than ⅔ of its power signed the commit,
// each under the key the anchor holds for it: the anchor's own quorum (Phase 4 spec §4.2; it was ⅓
// before). A vote counts only when it is one of the commit's, a precommit for its height, round and block
// hash, and when signers, the set that signed the commit, gives its name the anchor's key, so neither a
// vote for another block nor a forged set that gives real names keys of the forger's can borrow their
// power. Only a name's first such vote is checked, whether or not it verifies, so copies of one name's
// vote cost one signature check, not one each (spec 4.14 step 3); a commit that names a name twice fails
// Commit.Verify anyway.
func (node *Node) anchorVouches(commit consensus.Commit, signers, anchor *consensus.ValidatorSet) bool {
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
	return vouched >= anchor.Quorum()
}
