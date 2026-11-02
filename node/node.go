package node

import (
	"cmp"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"maps"
	"math/big"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Config is a node's static configuration.
type Config struct {
	Chain  ledger.Chain // the signature scheme, the network id and how the validator set follows from the state
	Signer *signing.Signer
	Name   string // this node's account name, which its votes, proposals and receipts carry; "" for an unnamed observer
	// Genesis is the state the network starts from, at height 0 for a new network. Its parameters (the
	// transaction cost, the step timeout, the block interval, the gossip fan-out, the message bound and
	// the rest) hold until a vote changes them. The node reads the timings from its committed state, so
	// every timing follows a vote, the re-gossip and catch-up intervals included.
	Genesis   ledger.LedgerState
	Bootstrap []string // peer base URLs to sync the committed state from on start (join/rejoin), and the first ones the running catch-up asks (see catchUpCheckpoint)
	// SnapshotPath, if set, is where the node keeps its committed checkpoint, with the sign log and the
	// signature record's files beside it (see signLogPath, recordPrefix); it reloads the first two on
	// start.
	SnapshotPath     string
	BackupPath       string // if set, also mirror the committed checkpoint here (e.g. a mounted share), off the loop and best-effort (see saveSnapshot)
	MaxMempool       int    // cap on each pool of pending payments, updates and name requests (0 = unlimited) — bounds a flood of valid ones
	MaxCommentLength int    // cap on a payment's Comment length in bytes (0 = unlimited)
	// GossipAllow is what the gossip port always lets in besides the current validators' IP addresses
	// (spec §5.1): IP prefixes, a single address as a /32 or a /128 (see gossipAllowList).
	GossipAllow []netip.Prefix
	// PublicPath, if set, is the public folder: the files the fetcher serves to the public, written at
	// Open and after each commit and catch-up (spec §6.1, see publicfolder.go). "" writes none.
	PublicPath string
	// MinimumStepTimeout is the slow-down switch (spec §7.6), 0 for off: the round timers wait at least
	// this long per step, when the governed step timeout is shorter (see stepTimeout). So the node waits
	// longer for a proposal before it votes "no block", as a carrier needs; no rule reads the clock, so
	// a longer timer can't make it sign twice or take an invalid block. The wait of a node back without
	// its sign log follows it too, so that a carrier brings its earlier votes back before it signs again
	// (see ownSignaturesWait). Nothing else does: re-gossip, the pull of public bundles, catch-up and the
	// idle heartbeat keep the governed timings.
	MinimumStepTimeout time.Duration
	// idleHeartbeat, if set, replaces IdleHeartbeat (see idle.go). Only tests set it, to a shorter one;
	// it is unexported, so a deployed node keeps the constant that wallets rely on.
	idleHeartbeat time.Duration
	// allowLoopbackPublicAddresses, if set, lets the fetches from the validators' public addresses reach
	// a loopback address (see publicAddressClient). Only tests set it, whose public faces listen on
	// 127.0.0.1; it is unexported, so a deployed node always refuses one.
	allowLoopbackPublicAddresses bool
}

type eventKind uint8

const (
	eventStart eventKind = iota
	eventNextHeight
	eventProposal
	eventVote
	eventTimeout
	eventPayment
	eventUpdate
	eventNameRequest
	eventParameterCertificate
	eventReassignmentCertificate
	eventQuery
	eventGovernanceQuery
	eventBundleQuery
	eventRegossip
	eventAdopt
	eventCatchUp
	eventIdleHeartbeat
)

type event struct {
	kind            eventKind
	proposal        *ProposalMessage
	vote            *consensus.Vote
	payment         *ledger.Payment
	update          *ledger.AccountUpdate
	nameRequest     *ledger.NameRequest
	verifiedKey     string // for an eventPayment, eventUpdate or eventNameRequest: the key its (requestor's) signature was checked under, off the loop
	verifiedBankKey string // for an eventNameRequest: the key its bank's signature was checked under ("" for a freeze)
	parameterChange *ledger.ParameterChange
	reassignment    *ledger.Reassignment
	step            consensus.Step
	round           int32
	height          int64 // for eventTimeout, eventNextHeight and eventIdleHeartbeat: the height that armed the timer
	query           chan Snapshot
	governanceQuery chan GovernanceView
	bundleQuery     chan []byte // receives the node's bundle (see ExportBundle)
	checkpoint      *Checkpoint
	admit           chan admission // receives the loop's verdict on a payment, an update, a name request, a certificate or a carried checkpoint
}

// admission is the loop's verdict on a payment, an update, a name request or a certificate: whether it
// was newly pooled, or the reason it was refused. One already pooled is neither. On a checkpoint a
// carrier brought (ImportCheckpoint) it is only the reason the node did not adopt it, nil once it did.
type admission struct {
	newlyPooled bool
	err         error
}

// Snapshot is a race-free view of the node's state, obtained via the event loop.
type Snapshot struct {
	Height             int64 // the height in progress, one above the committed state's
	Accounts           []ledger.Account
	Parameters         ledger.LedgerParameters // committed operating parameters (for checkpoints)
	GovernanceSequence int64                   // committed governance sequence (for checkpoints)
	LastBlock          ledger.BlockHeader      // the header of the last committed block (for checkpoints)
	LastCommit         consensus.Commit
	Validators         []consensus.Validator // the set that signed LastCommit (for checkpoints)
}

// checkpoint is the committed checkpoint the snapshot holds (spec 4.14): the state at the committed
// height, the header of the block that produced it, its commit and the set that signed it.
func (snapshot Snapshot) checkpoint() Checkpoint {
	state := ledger.LedgerState{Accounts: snapshot.Accounts, Parameters: snapshot.Parameters,
		GovernanceSequence: snapshot.GovernanceSequence, Height: snapshot.Height - 1}
	return Checkpoint{State: state, LastBlock: snapshot.LastBlock, Commit: snapshot.LastCommit, Validators: snapshot.Validators}
}

// Node is one node of the network: a validator, or an observer that follows the chain and signs
// nothing. All mutable state is owned by the run loop, but backupBusy (see saveSnapshot),
// pullRoundRunning and stuckOnItsHeight (see pullPublicBundles), publicFaceTurn (see
// catchUpCheckpoint), the votes the pulls verified, which their mutex guards (see verifiedVotes), and
// the public folder's queue, which its writer guards (see publicFolderWriter). Other goroutines post
// events onto the loop's channel (the Submit* and Admit* inputs, Snapshot, GovernanceView,
// ExportBundle), or read the committed view the loop publishes (see committedView).
type Node struct {
	config    Config
	self      string
	transport Transport

	events chan event
	quit   chan struct{}
	done   chan struct{}                 // closed when the run loop returns (see Stop)
	view   atomic.Pointer[committedView] // what the node last committed, published for the work done off the loop (see publishView)

	// state owned by run()
	state            ledger.LedgerState // the committed state: accounts, governed parameters, governance sequence and height
	driver           *consensus.Driver
	mempool          *mempool
	updates          *updatePool
	requests         *requestPool
	blocks           map[[32]byte]ledger.Block // the blocks the node may decide at the height in progress, by block hash
	lastBlockHash    [32]byte                  // the hash of the last committed block; the genesis hash until the first commit
	lastBlockHeader  ledger.BlockHeader        // that block's header, for checkpoints (none at genesis)
	lastCommit       consensus.Commit
	lastProposal     ProposalMessage // the proposal lastCommit decided (see sendLastCommit)
	started          bool
	idle             bool                      // no driver because the height in progress has no work (see idle.go); only startHeight sets it
	heartbeatHeight  int64                     // the height whose idle heartbeat has fired: it starts with no work (see onIdleHeartbeat)
	heightStarted    int64                     // the height startHeight last began, so a stale re-trigger is a no-op
	relaySeen        map[string]bool           // the messages relayed at this height, by messageKey; only a certificate's mark decides anything (see relay); also the rounds whose proposer equivocation was reported (see reportEquivocation)
	heightVotes      map[string]consensus.Vote // votes seen at the current height, by validator, round and type (see rememberVote), re-gossiped periodically (see reGossipLoop)
	heightProposals  map[int32]ProposalMessage // the proposals taken at the current height (see onProposal), by round (first wins, like the driver)
	invalidProposals map[int32]int             // authentic proposals at the current height whose block did not apply, by round (see onProposal)

	heightStartedAt      time.Time // when startHeight last began the height in progress, by the wall clock: when its driver started, if one runs (see pullPublicBundles)
	pullingPublicBundles bool      // whether the node pulls public bundles, so it logs when it starts and when it stops (see pullPublicBundles)

	heightValidators     *consensus.ValidatorSet // the set governing the height in progress (nil until the first starts)
	lastCommitValidators *consensus.ValidatorSet // the set that signed lastCommit (served in checkpoints)

	pendingProofs               map[string]pendingProof            // double-sign proofs to carry into a block, by the signer's name (see pendingProof)
	parameterCertificatePool    map[string]*ledger.ParameterChange // complete parameter-change certificates awaiting a block (by parameterChangeKey)
	reassignmentCertificatePool map[string]*ledger.Reassignment    // complete ⅔ name-action certificates awaiting a block (by reassignmentKey)
	signLog                     signLog                            // own votes at the height in progress, on disk before release (see logVote)
	mayHaveSignedUpTo           int64                              // the highest height the node may have signed at before it lost its sign log, 0 for none (see waitForOwnSignatures)
	loggedKeyReplaced           bool                               // whether the node has logged that its name is seated under another key (see seatedSigner)
	oldestUnitEnd               unitEnd                            // the oldest rolling record unit file as it ended when pruneRecord last read it

	configuredPeers [][]string // the transport's peers before on-chain resolution replaced them (see resolvePeersFromState)
	peersResolved   bool       // whether the transport is using resolved peers

	backupBusy       atomic.Bool   // a BackupPath mirror is in flight (see saveSnapshot); set on the loop, cleared by the mirror
	pullRoundRunning atomic.Bool   // a round of public bundle pulls is in flight (see pullPublicBundles); set on the loop, cleared by the round
	stuckOnItsHeight atomic.Bool   // pullingPublicBundles, for catch-up's goroutine (see catchUpCheckpoint); set on the loop
	pulledVotes      verifiedVotes // the votes of pulled public bundles that verified at the committed height (see pulledCheck)
	publicFaceTurn   atomic.Uint64 // which public face catch-up asks when it asks one (see catchUpCheckpoint); catch-up's goroutine alone counts it

	publicFolder    *publicFolderWriter // writes the public folder off the loop; nil without a PublicPath (see openPublicFolder)
	publishedBundle []byte              // the public bundle publishBundle last queued, so that one unchanged is not written again
}

// Open builds a node. If a snapshot is configured and present on disk, it is loaded as the starting
// state, together with the sign log of the height it restarts at (a log of an earlier height is
// emptied, see loadSignLog); Open fails if the snapshot's folder does not exist, if either file cannot
// be read or the snapshot does not verify (see loadSnapshot), or if the sign log is ahead of the
// snapshot (see loadSignLog). Then, if its name is one of two or more validators, it takes its gossip
// peers from the validators' IP addresses in the state it opened at, once every validator has one (see
// resolvePeersFromState). Last, with a PublicPath, it writes the public folder from that state, and fails
// if it can't create its folders (see openPublicFolder). It does not start the loop; call Start.
func Open(config Config, transport Transport) (*Node, error) {
	if !config.Genesis.NamesAscending() {
		return nil, errors.New("genesis: the accounts are not in strictly ascending order of name")
	}
	node := &Node{
		config:                      config,
		self:                        config.Name,
		transport:                   transport,
		events:                      make(chan event, 1024),
		quit:                        make(chan struct{}),
		state:                       config.Genesis,
		lastBlockHash:               config.Chain.GenesisHash,
		mempool:                     newMempool(config.MaxMempool),
		updates:                     newUpdatePool(config.MaxMempool),
		requests:                    newRequestPool(config.MaxMempool),
		blocks:                      map[[32]byte]ledger.Block{},
		relaySeen:                   map[string]bool{},
		heightVotes:                 map[string]consensus.Vote{},
		heightProposals:             map[int32]ProposalMessage{},
		invalidProposals:            map[int32]int{},
		pendingProofs:               map[string]pendingProof{},
		parameterCertificatePool:    map[string]*ledger.ParameterChange{},
		reassignmentCertificatePool: map[string]*ledger.Reassignment{},
	}
	if err := node.loadSnapshot(); err != nil {
		return nil, err
	}
	if err := node.loadSignLog(); err != nil {
		return nil, err
	}
	node.publishView()
	node.resolvePeersFromState()
	if err := node.openPublicFolder(); err != nil {
		return nil, err
	}
	return node, nil
}

// height is the height in progress: the one after the committed state's. The node keeps no other
// count, so its height always follows the state it holds.
func (node *Node) height() int64 { return node.state.Height + 1 }

// Start launches the run loop. If Bootstrap peers are configured, it first syncs the committed
// state from them (join/rejoin) and then begins consensus at the synced height; otherwise it
// begins at the height it opened at. Without a sign log for a height past genesis, it begins after the
// wait of a node back without its sign log (see startFirstHeight, ownSignaturesWait).
func (node *Node) Start() {
	node.done = make(chan struct{})
	go node.run()
	if len(node.config.Bootstrap) > 0 {
		go node.syncThenStart()
	} else {
		node.enqueue(event{kind: eventStart})
	}
	// Ongoing catch-up runs regardless, from bootstrap peers, gossip peers or the other validators'
	// public addresses (see catchUpCheckpoint), so a node that falls a full height behind self-recovers
	// instead of getting stuck.
	go node.catchUpLoop()
	// Periodic re-gossip of the current height's votes and current round's proposal: a merged
	// network re-exchanges held votes so a partition-formed stall can advance once the partition
	// lifts, and a lost proposal is re-sent instead of burning its round (see reGossipLoop).
	go node.reGossipLoop()
}

// Stop halts the run loop and waits until it has finished the event in hand, then until the public
// folder's files are written, so nothing the node writes (its snapshot, its sign log, its public folder)
// lands after Stop returns.
func (node *Node) Stop() {
	close(node.quit)
	if node.done != nil {
		<-node.done
	}
	if node.publicFolder != nil {
		node.publicFolder.waitUntilIdle()
	}
}

// SubmitProposal and SubmitVote are the thread-safe inputs for consensus messages.
func (node *Node) SubmitProposal(proposal ProposalMessage) {
	node.enqueue(event{kind: eventProposal, proposal: &proposal})
}
func (node *Node) SubmitVote(vote consensus.Vote) { node.enqueue(event{kind: eventVote, vote: &vote}) }

// awaitAdmission waits for the loop's verdict on a payment, an update, a name request or a certificate.
func (node *Node) awaitAdmission(verdict chan admission) (bool, error) {
	select {
	case result := <-verdict:
		return result.newlyPooled, result.err
	case <-node.quit:
		return false, errors.New("node stopped")
	}
}

// AdmitParameterCertificate hands a parameter-change certificate to the loop, which pools it only once
// complete (see addParameterVotes), and returns the loop's verdict: whether it newly pooled the
// certificate, or one replacing it, and relayed it, or the reason it refused it. A copy of a pooled
// certificate is neither.
func (node *Node) AdmitParameterCertificate(parameterChange ledger.ParameterChange) (newlyPooled bool, err error) {
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventParameterCertificate, parameterChange: &parameterChange, admit: verdict})
	return node.awaitAdmission(verdict)
}

// AdmitReassignmentCertificate hands a ⅔ name-action certificate to the loop, which pools it only once
// complete (see addReassignmentVotes), and returns the loop's verdict, as AdmitParameterCertificate
// does.
func (node *Node) AdmitReassignmentCertificate(reassignment ledger.Reassignment) (newlyPooled bool, err error) {
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventReassignmentCertificate, reassignment: &reassignment, admit: verdict})
	return node.awaitAdmission(verdict)
}

// Snapshot returns a race-free view of node state.
func (node *Node) Snapshot() Snapshot {
	response := make(chan Snapshot, 1)
	node.enqueue(event{kind: eventQuery, query: response})
	select {
	case snapshot := <-response:
		return snapshot
	case <-node.quit:
		return Snapshot{}
	}
}

// GovernanceView returns a race-free read-only view of governance (current parameters, validators,
// and pending proposals with tallies) for an admin UI.
func (node *Node) GovernanceView() GovernanceView {
	response := make(chan GovernanceView, 1)
	node.enqueue(event{kind: eventGovernanceQuery, governanceQuery: response})
	select {
	case view := <-response:
		return view
	case <-node.quit:
		return GovernanceView{}
	}
}

func (node *Node) enqueue(incoming event) {
	select {
	case node.events <- incoming:
	case <-node.quit:
	}
}

func (node *Node) run() {
	defer close(node.done)
	for {
		select {
		case <-node.quit:
			return
		case event := <-node.events:
			node.handle(event)
		}
	}
}

// handle runs one event on the loop: it hands each kind to the function that deals with it.
func (node *Node) handle(incoming event) {
	switch incoming.kind {
	case eventStart:
		if !node.started {
			node.started = true
			node.startFirstHeight()
		}
	case eventNextHeight:
		// Like a consensus timer, it belongs to the height that armed it: one the node has left by a
		// catch-up starts nothing, and cannot cut a later wait short (see waitForOwnSignatures).
		if incoming.height == node.height() {
			node.startHeight()
		}
	case eventIdleHeartbeat:
		node.onIdleHeartbeat(incoming.height)
	case eventAdopt:
		node.adopt(*incoming.checkpoint)
	case eventCatchUp:
		err := node.catchUp(*incoming.checkpoint)
		if incoming.admit != nil { // a carried checkpoint, whose import waits for the verdict (ImportCheckpoint)
			incoming.admit <- admission{err: err}
		}
	case eventProposal:
		// Relay only a proposal the node took: AUTHENTIC (from the round's proposer, correctly
		// signed) and valid. Any other is dropped, not gossiped onward.
		if node.onProposal(*incoming.proposal) {
			node.relay(Message{Type: MessageProposal, Proposal: incoming.proposal})
			node.wakeIfIdle()
		}
	case eventVote:
		// Votes reach the node unchecked. A copy of a vote it holds is dropped before any check (spec
		// 4.10): signing is deterministic, so every relayed copy of an honest vote is byte-identical. Only
		// a vote the height in progress counts, and that the node does not hold yet, is kept, relayed and
		// counted. Another may still prove a late double-sign, or, one of the node's own for a later
		// height, that it signed there before it lost its sign log (see noteOwnSignature).
		if node.holdsVote(*incoming.vote) {
			break
		}
		if !node.countable(*incoming.vote) {
			node.noteOwnSignature(incoming.vote.Validator, incoming.vote.Height, incoming.vote.Verify)
			node.recordLateDoubleSign(*incoming.vote)
		} else if node.rememberVote(*incoming.vote) {
			node.relay(Message{Type: MessageVote, Vote: incoming.vote})
			if node.driver != nil {
				node.process(node.driver.ReceiveVote(*incoming.vote))
			}
			node.wakeIfIdle()
		}
	case eventTimeout:
		// A timer belongs to the height that armed it: round numbers restart every height, so a stale
		// one would otherwise fire into a later height's driver.
		if node.driver != nil && incoming.height == node.height() {
			node.process(node.driver.Timeout(incoming.step, incoming.round))
		}
	case eventPayment:
		newlyPooled, err := node.admitPayment(*incoming.payment, incoming.verifiedKey)
		incoming.admit <- admission{newlyPooled: newlyPooled, err: err}
	case eventUpdate:
		newlyPooled, err := node.admitUpdate(*incoming.update, incoming.verifiedKey)
		incoming.admit <- admission{newlyPooled: newlyPooled, err: err}
	case eventNameRequest:
		newlyPooled, err := node.admitNameRequest(*incoming.nameRequest, incoming.verifiedKey, incoming.verifiedBankKey)
		incoming.admit <- admission{newlyPooled: newlyPooled, err: err}
	case eventParameterCertificate:
		newlyPooled, err := node.addParameterVotes(*incoming.parameterChange)
		if newlyPooled {
			node.publishGovernance()
			node.relay(Message{Type: MessageParameterCertificate, ParameterChange: incoming.parameterChange})
			node.wakeIfIdle()
		}
		incoming.admit <- admission{newlyPooled: newlyPooled, err: err}
	case eventReassignmentCertificate:
		newlyPooled, err := node.addReassignmentVotes(*incoming.reassignment)
		if newlyPooled {
			node.publishGovernance()
			node.relay(Message{Type: MessageReassignmentCertificate, Reassignment: incoming.reassignment})
			node.wakeIfIdle()
		}
		incoming.admit <- admission{newlyPooled: newlyPooled, err: err}
	case eventQuery:
		incoming.query <- node.snapshot()
	case eventGovernanceQuery:
		incoming.governanceQuery <- node.governanceView()
	case eventBundleQuery:
		incoming.bundleQuery <- node.bundle(true, MaxBundleBytes)
	case eventRegossip:
		node.reGossip()
		node.publishBundle()
		node.pullPublicBundles()
	}
}

// snapshot is a copy of the node's committed state and checkpoint, for Snapshot and saveSnapshot. The
// account slice is copied, but each Account.Amount *big.Rat pointer is SHARED with the loop's live
// state. This is race-free only because ledger.ProcessPayments (and the other appliers) treat amounts as
// immutable — they allocate a fresh big.Rat rather than mutating in place — so the loop never writes
// through a pointer a reader holds. Any future in-place amount mutation in ledger would break that
// invariant and must instead deep-copy here.
func (node *Node) snapshot() Snapshot {
	var lastValidators []consensus.Validator
	if node.lastCommitValidators != nil {
		lastValidators = node.lastCommitValidators.Validators()
	}
	return Snapshot{
		Height:             node.height(),
		Accounts:           append([]ledger.Account(nil), node.state.Accounts...),
		Parameters:         node.state.Parameters,
		GovernanceSequence: node.state.GovernanceSequence,
		LastBlock:          node.lastBlockHeader,
		LastCommit:         node.lastCommit,
		Validators:         lastValidators,
	}
}

// committedView is what the node last committed, as the work done off the loop sees it (spec 6.5):
// the committed state, its digest, the validator set of the height in progress, derived from it, the
// set that signed the last commit, and the gossip port's allow list, derived from the state and the
// set. The HTTP handlers check payments and updates against it, signatures included, /hdls/resolve
// answers from it, bestCheckpoint takes the set as its trust anchor, the pull of public bundles checks
// their votes and proposals against both sets (see pulledCheck), and the gossip port's listener lets in
// what the allow list holds. It is immutable once published.
type committedView struct {
	state                ledger.LedgerState
	stateDigest          [32]byte // state.Digest(): what a payment's pin names (E38)
	validators           *consensus.ValidatorSet
	lastCommitValidators *consensus.ValidatorSet // the set that signed the last commit; nil before the first
	gossipAllowList      []netip.Prefix          // see gossipAllowList
}

// publishView publishes the committed state as the committed view, after Open, each commit and each
// catch-up, before anything else runs on the new state: on the loop, too, the view is the live
// committed state. The ledger never mutates an account slice or an amount in place, so sharing them
// with the loop's state is race-free. Publishing it is also the one atomic swap that puts the allow
// list the new state makes in front of the gossip port (spec §5.2).
func (node *Node) publishView() {
	validators := node.config.Chain.ValidatorRule.SetFor(node.state)
	node.view.Store(&committedView{state: node.state, stateDigest: node.state.Digest(), validators: validators,
		lastCommitValidators: node.lastCommitValidators, gossipAllowList: gossipAllowList(node.state.Accounts, validators, node.self, node.config.GossipAllow)})
}

// reconcilePools brings the pending pools in line with a newly committed state (a commit or a
// fast-forward; the height in progress is already the next one, and the committed view is published),
// given the accounts before it (spec 6.5). An entry goes when its block has passed, so it can never
// replay; when its signer, requestor or bank now has another key than in previousAccounts, the key its
// signature was checked under; and when it fails its intake check against the new state, which a
// payment or an update from a newly frozen name, a request that no longer fits, or a freeze that would
// now leave no name seated (R9) does. Then, payments first and requests after, each in arrival order, a
// payment or fee goes when the payer's pending outgoing would pass its balance: the new state can
// shrink a balance under what the pools promise, and a pending overdraw left in the pool would make
// this node propose a block that bans, and destroys, the payer (see admitPayment and
// ledger.ProcessPayments). Certificates that no longer apply go too, and so do pending proofs that no
// block can carry any more (prunePendingProofs).
func (node *Node) reconcilePools(previousAccounts []ledger.Account) {
	view := node.view.Load()
	keyKept := func(name string) bool {
		before, _ := ledger.FindAccount(previousAccounts, name)
		now, _ := ledger.FindAccount(node.state.Accounts, name)
		return before.PublicKey == now.PublicKey
	}
	pendingOutgoingSoFar := map[string]*big.Rat{} // each payer's pending outgoing among the entries kept so far
	// affordable reports whether the payer's balance covers amount on top of what it keeps so far, and
	// if so keeps amount too. It is called last, for an entry that passes every other check.
	affordable := func(payerName string, amount *big.Rat) bool {
		total := new(big.Rat).Set(amount)
		if kept, found := pendingOutgoingSoFar[payerName]; found {
			total.Add(total, kept)
		}
		if total.Cmp(ledger.AccountAmount(node.state.Accounts, payerName)) > 0 {
			return false
		}
		pendingOutgoingSoFar[payerName] = total
		return true
	}
	node.mempool.keep(func(payment ledger.Payment) bool {
		return paymentIntakeError(view, payment) == nil && keyKept(payment.From) && affordable(payment.From, payment.Amount)
	})
	node.requests.keep(func(request ledger.NameRequest) bool {
		return node.nameRequestIntakeError(view, request) == nil && keyKept(request.Requestor) && keyKept(request.Bank) &&
			(request.Fee == nil || affordable(request.Requestor, request.Fee))
	})
	node.updates.keep(func(update ledger.AccountUpdate) bool {
		return updateIntakeError(node.state, update) == nil && keyKept(update.Name)
	})
	node.pruneParameterCertificates() // drop parameter certificates that no longer amend the current parameters
	node.pruneReassignments()         // drop name actions that no longer fit the state
	node.prunePendingProofs()         // drop proofs no block can carry any more
}

// startHeight builds a candidate block from the pools (used if this node is proposer) and starts the
// Driver for the current height. With no work for the height (hasWork), it sets idle, starts no driver
// and arms the idle heartbeat; wakeIfIdle calls it again once work arrives, and onIdleHeartbeat once the
// heartbeat fires, when the height starts with no work. The driver resumes from what the node already
// holds for the height: its own votes and proposals (from its sign log after a restart, or re-gossiped
// back to it), and the proposals onProposal took before the driver existed, in the post-commit window.
// It is then fed the votes that arrived meanwhile.
func (node *Node) startHeight() {
	if node.heightStarted == node.height() && node.driver != nil {
		return // already started this height — ignore a stale eventNextHeight/eventCatchUp that would
		//        otherwise replace the live driver mid-height and discard its in-progress votes.
	}
	node.retireDriver() // after a catch-up, the driver of the height left behind
	node.heightStarted = node.height()
	// The node's own clock, which no rule reads: it only times the pull of public bundles.
	node.heightStartedAt = time.Now()
	node.heightValidators = node.view.Load().validators // the set governing this height (and signing its commit)
	var nonce [32]byte
	rand.Read(nonce[:]) // a fresh nonce for each candidate (spec 5.1); it never fails
	voteEvidence, proposalEvidence := node.pendingEvidence()
	candidateBlock := ledger.ProposeBlock(node.config.Chain, node.state, node.lastBlockHash, nonce,
		ledger.BlockContent{
			Payments:         node.mempool.pendingCopy(),
			AccountUpdates:   node.updates.pendingCopy(),
			NameRequests:     node.requests.pendingCopy(),
			VoteEvidence:     voteEvidence,
			ProposalEvidence: proposalEvidence,
			ParameterChange:  node.pendingParameterChange(),
			Reassignment:     node.pendingReassignment(),
		})
	if !node.hasWork(candidateBlock) {
		node.idle = true // no driver until there is work (see wakeIfIdle) or the heartbeat fires
		node.armIdleHeartbeat()
		return
	}
	node.idle = false
	candidateHash := ledger.BlockHash(candidateBlock)
	node.blocks[candidateHash] = candidateBlock
	node.driver = consensus.NewDriver(node.seatedSigner(), node.self, node.heightValidators, node.config.Chain.Scheme, node.config.Chain.NetworkID, node.height(), candidateHash)
	if node.config.SnapshotPath != "" {
		node.driver.SetSignLog(node.logVote)
	}
	// Re-entering a height it already signed in before a restart: re-gossip its logged votes, hold its
	// logged proposals, which it sends again rather than sign another block for their rounds, and
	// re-feed the proposal it is locked on, so that round can still decide.
	resumed := node.signLog.Height == node.height()
	if resumed {
		for _, vote := range node.signLog.Votes {
			node.rememberVote(vote)
		}
		for _, proposal := range node.signLog.Proposals {
			node.blocks[proposal.Proposal.BlockHash] = proposal.Block // loadSignLog checked that the block has this hash
			node.rememberProposal(proposal)
		}
	}
	// The driver resumes from its own votes at the height, logged or re-gossiped back to it after it
	// came back without its sign log (see waitForOwnSignatures), so it signs nothing against them.
	var ownVotes []consensus.Vote
	for _, vote := range node.heightVotes {
		if vote.Validator == node.self {
			ownVotes = append(ownVotes, vote)
		}
	}
	var heldProposals []consensus.Proposal
	for _, proposal := range node.heightProposals {
		heldProposals = append(heldProposals, consensus.Proposal{Round: proposal.Proposal.Round, BlockHash: proposal.Proposal.BlockHash, ValidRound: proposal.Proposal.ValidRound})
	}
	node.process(node.driver.Resume(ownVotes, heldProposals))
	if resumed && node.signLog.Locked != nil {
		node.onProposal(*node.signLog.Locked)
	}
	for _, vote := range node.heightVotes {
		if node.driver == nil {
			return // decided
		}
		node.process(node.driver.ReceiveVote(vote))
	}
}

// seatedSigner is the signer the driver of the height in progress signs with (spec 6.3): the node's own,
// when its name is seated in the height's set with exactly the node's key; otherwise none, and the
// driver follows the rounds and decides without signing a vote or a proposal. So a node that is not
// seated, an observer among them, never signs, and one whose name was re-keyed never signs under its
// old key. When the name is seated under another key, the node logs that once.
func (node *Node) seatedSigner() *signing.Signer {
	seatedKey := node.heightValidators.PublicKey(node.self)
	switch {
	case seatedKey == node.config.Signer.PublicKeyBase64():
		return node.config.Signer
	case seatedKey != "" && !node.loggedKeyReplaced:
		node.loggedKeyReplaced = true
		log.Printf("the name %q is seated under another key than this node's: the node signs no vote or proposal", node.self)
	}
	return nil
}

// startFirstHeight starts the height the node opens at. Back from a snapshot (its own, or a checkpoint
// it adopted) without a sign log for that height, it may have voted there before it lost the log, with
// its disk or by an operator's hand, so it waits there first (see waitForOwnSignatures). A node still at
// its genesis state has no snapshot and starts at once.
func (node *Node) startFirstHeight() {
	if node.state.Height == node.config.Genesis.Height || node.signLog.Height == node.height() {
		node.startHeight()
		return
	}
	node.mayHaveSignedUpTo = max(node.mayHaveSignedUpTo, node.height())
	node.waitForOwnSignatures()
}

// waitForOwnSignatures starts the wait of a node back without its sign log at the height in progress,
// which it may have signed at before it lost the log: for ownSignaturesWait it signs nothing, and holds
// what arrives as in the post-commit window, so that peers' re-gossip of the height's votes and current
// proposal, whatever round they are in (see onProposal), brings its own earlier ones back first; then
// the height starts, and the driver resumes from them (see startHeight). Each peer re-gossips once an
// interval, but its ticks drift later by the time each one takes, so a wait of one interval can end just
// before a tick; two hold one, unless a peer stalls for longer. In pigeon mode a carrier brings them
// back instead, and the wait is longer (see ownSignaturesWait). The wait is best effort, by timing.
//
// It waits so at every height the node may have signed at, up to mayHaveSignedUpTo: the height it comes
// back at (startFirstHeight); each later one a commit takes it to, where the post-commit window becomes
// the wait (commit); and the height a catch-up from one of those takes it to, the network's height,
// where it may have signed too, and which it may reach before its votes there have come back (catchUp).
// Its own votes and proposals for a later height are not kept, only noted (see noteOwnSignature): they
// come back with the re-gossip at that height, where they pass that height's checks, a proposal's block
// included, which no state the node holds earlier could check. Once the node has a sign log, the log
// keeps mayHaveSignedUpTo, so that a restart keeps it (see loadSignLog), and its first new log at a
// height holds what it took back there (see heightSignLog).
//
// The waits end. Each is one timer, which starts only the height it was armed for: a catch-up during a
// wait at a height up to mayHaveSignedUpTo moves the wait, in full, to the new height, and one during the
// wait above it, after a first catch-up, starts the new height at once. mayHaveSignedUpTo rises only with
// the node's own signatures, which reach no higher than it got before, and a catch-up waits only from a
// height up to it, so once a catch-up has taken the node past it, the node never chases a network that
// moves on while it waits. A validator the quorum needs delays the network by the wait at each such
// height. A validator that never lost its sign log nor its latest snapshot never waits, nor does a new
// network.
func (node *Node) waitForOwnSignatures() {
	node.retireDriver() // after a catch-up, the driver of the height left behind
	node.idle = false
	node.heightValidators = node.view.Load().validators
	height := node.height()
	time.AfterFunc(node.ownSignaturesWait(), func() { node.enqueue(event{kind: eventNextHeight, height: height}) })
}

// ownSignaturesWait is how long a node back without its sign log waits at a height it may have signed at
// (see waitForOwnSignatures): two re-gossip intervals, or, with the slow-down switch on and longer, two
// of its step timeouts. In pigeon mode the node's earlier votes and proposal are at the other banks, and
// come back with the next carry, not at a re-gossip tick; the banks set the switch to at least twice the
// time a carrier takes (spec §7.6), so a wait of twice the switch outlasts the next carry. A restart that
// keeps its sign log never waits, so a long switch delays only a node that lost its log.
func (node *Node) ownSignaturesWait() time.Duration {
	return 2 * max(node.reGossipInterval(), node.config.MinimumStepTimeout)
}

// noteOwnSignature raises mayHaveSignedUpTo to height when the node meets a vote or a proposal in its own
// name for a height above both the one in progress and mayHaveSignedUpTo, whose signature verifies under
// its own key (verify is the vote's or the proposal's Verify): it signed there before it lost its sign
// log, or before it came back from an older copy of its data, so it waits there (see
// waitForOwnSignatures). Such a message costs one signature check, as a countable vote does, and
// mayHaveSignedUpTo never falls. The node's sign log, if it has one, is written again with the new
// bound: a restart reads the bound from it, whether it finds it for the height in progress, and starts
// at once, or below it, during a wait (see loadSignLog). With no log yet, the bound is written with the
// first one, and a restart before then waits at the height it opens at and learns it again.
func (node *Node) noteOwnSignature(signerName string, height int64, verify func(signing.Scheme, string, string) bool) {
	if signerName == node.self && height > max(node.height(), node.mayHaveSignedUpTo) &&
		verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, node.config.Signer.PublicKeyBase64()) {
		node.mayHaveSignedUpTo = height
		if node.signLog.Height != 0 && node.writeSignLog(node.signLog) != nil {
			log.Printf("sign log %s not written: a restart may forget that the node may have signed up to height %d", node.signLogPath(), height)
		}
	}
}

// maxInvalidProposals is how many authentic proposals of one round whose block does not apply a node
// checks (see onProposal) before it takes no proposal for that round.
const maxInvalidProposals = 2

// onProposal ingests a proposal and returns whether the node took it (so the caller relays it). It runs
// the steps of spec 4.11 in order, the cheap ones first, then one signature check, then the block hash,
// then ApplyBlock:
//
//  1. the proposal is for the height in progress, and a set exists for it (one of the node's own for a
//     later height is only noted, see noteOwnSignature);
//  2. no proposal is held for its round yet: only the first of a round is taken, as in the driver, so a
//     proposer cannot make every node apply, store and relay block after block for one round, and a
//     second one its proposer signed is reported (see reportEquivocation);
//  3. its round is from 0 to the one after the driver's, so not far ahead, and fewer than
//     maxInvalidProposals authentic copies of the round failed to apply. While no driver runs, a
//     proposal in the node's own name may be for any round up to MaxRoundLookahead, past which no
//     vote counts either: a node back without its sign log takes back the one its peers re-gossip,
//     whatever round they are in (see waitForOwnSignatures). Only the key its name is seated under
//     signs those (step 5), one per round: the node's own key, or the new one of a re-keyed name;
//  4. it names the round's scheduled proposer, and
//  5. its signature verifies under the key the height's set holds for that name: without these any
//     machine could front-run the proposer with an invalid block and make honest nodes prevote nil
//     forever, an unauthenticated liveness kill;
//  6. its block hashes to the signed block hash, and is labelled with the height: the signature covers
//     the whole block, so a copy with another body is not authentic (E15);
//  7. its block applies (ApplyBlock).
//
// A copy that fails step 5 or 6 is dropped and never held, so it cannot take the round from the genuine
// proposal, which is taken when it comes. One that fails step 7 is authentic: its proposer signed a block
// that does not apply. Each such copy costs an ApplyBlock, and the proposer can send it again and again
// without signing anything new, so once maxInvalidProposals of them have failed for a round, the node
// takes no more proposals for it. If no valid proposal comes, the driver prevotes nil when its propose
// step times out.
//
// Between a commit and the start of the next height (the block interval window) there is no
// driver, and the next one starts at round 0. A proposal for the new height is then taken all the
// same, and startHeight shows it to the driver once it exists.
func (node *Node) onProposal(proposalMessage ProposalMessage) bool {
	signedProposal, block := proposalMessage.Proposal, proposalMessage.Block
	if signedProposal.Height != node.height() || node.heightValidators == nil {
		node.noteOwnSignature(signedProposal.Proposer, signedProposal.Height, signedProposal.Verify)
		return false // another height, or no height started yet
	}
	if held, found := node.heightProposals[signedProposal.Round]; found {
		// Not even a duplicate is relayed: the proposal was flooded on its first sight, and
		// re-gossip periodically re-broadcasts the held copy, so propagation is covered.
		node.reportEquivocation(held.Proposal, signedProposal)
		return false
	}
	takesItsOwnBack := node.driver == nil && signedProposal.Proposer == node.self && signedProposal.Round <= node.round()+consensus.MaxRoundLookahead
	if signedProposal.Round < 0 || signedProposal.Round > node.round()+1 && !takesItsOwnBack ||
		node.invalidProposals[signedProposal.Round] >= maxInvalidProposals {
		return false
	}
	if !node.fromItsProposer(signedProposal, node.heightValidators) {
		return false // wrong proposer or bad signature — drop, don't relay
	}
	if ledger.BlockHash(block) != signedProposal.BlockHash || block.Height != signedProposal.Height {
		return false // another body under the signature: not authentic
	}
	if _, err := ledger.ApplyBlock(node.config.Chain, node.state, node.lastBlockHash, block); err != nil {
		node.invalidProposals[signedProposal.Round]++
		return false
	}
	node.blocks[signedProposal.BlockHash] = block
	node.rememberProposal(proposalMessage)
	if node.driver != nil {
		node.process(node.driver.ReceiveProposal(consensus.Proposal{
			Round: signedProposal.Round, BlockHash: signedProposal.BlockHash, ValidRound: signedProposal.ValidRound,
		}))
	}
	return true
}

// fromItsProposer reports whether proposal names the proposer that validators, the set of its height,
// schedules for its height and round, and its signature verifies under the key the set holds for that
// name: onProposal's steps 4 and 5, which the pull of a public bundle takes too (see pulledCheck).
func (node *Node) fromItsProposer(proposal consensus.SignedProposal, validators *consensus.ValidatorSet) bool {
	return proposal.Proposer == validators.ProposerAt(proposal.Height, proposal.Round) &&
		proposal.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, validators.PublicKey(proposal.Proposer))
}

// round is the driver's round, or 0, the round the next height's driver starts at, in the
// post-commit window.
func (node *Node) round() int32 {
	if node.driver == nil {
		return 0
	}
	return node.driver.Round()
}

// process carries out the actions the driver returns: sign, log and send a proposal, send a vote, arm
// a timer, or commit.
func (node *Node) process(actions []consensus.Action) {
	for _, rawAction := range actions {
		switch action := rawAction.(type) {
		case consensus.ActionProposal:
			if _, found := node.blocks[action.BlockHash]; !found {
				continue
			}
			proposal, err := node.signProposal(action.Round, action.ValidRound, action.BlockHash)
			if err != nil {
				continue // can't sign our own proposal (unreachable with a valid signer)
			}
			if node.config.SnapshotPath != "" && node.logProposal(proposal) != nil {
				continue // like a vote, a proposal that is not in the sign log is not sent
			}
			node.rememberProposal(proposal)
			node.transport.Broadcast(node.self, Message{Type: MessageProposal, Proposal: &proposal})
		case consensus.ActionBroadcastVote:
			vote := action.Vote
			node.rememberVote(vote)
			node.transport.Broadcast(node.self, Message{Type: MessageVote, Vote: &vote})
		case consensus.ActionSchedule:
			node.armTimeout(action.Step, action.Round)
		case consensus.ActionDecided:
			node.commit(action)
		}
	}
}

// signProposal signs this node's proposal of the block it holds under blockHash, for round, at the
// height in progress.
func (node *Node) signProposal(round, validRound int32, blockHash [32]byte) (ProposalMessage, error) {
	signedProposal, err := consensus.SignProposal(node.config.Signer, consensus.SignedProposal{
		Height: node.height(), Round: round, ValidRound: validRound, BlockHash: blockHash, Proposer: node.self})
	return ProposalMessage{Proposal: signedProposal, Block: node.blocks[blockHash]}, err
}

// countable reports whether vote is one the driver of the height in progress counts: a prevote or
// precommit for this height naming one of its validators, signed for this network under the key the set
// holds for that name, for a round from 0 to consensus.MaxRoundLookahead past the driver's. In the
// post-commit window that is the next height's set and round 0.
func (node *Node) countable(vote consensus.Vote) bool {
	return vote.Height == node.height() && node.heightValidators != nil && node.heightValidators.Contains(vote.Validator) &&
		(vote.Type == consensus.Prevote || vote.Type == consensus.Precommit) &&
		vote.Round >= 0 && vote.Round <= node.round()+consensus.MaxRoundLookahead &&
		vote.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, node.heightValidators.PublicKey(vote.Validator))
}

// rememberVote records a vote seen at the current height (own or a peer's) and reports whether it is
// new. It keeps a validator's first vote per round and type and one conflicting vote, so an
// equivocation is re-gossiped with both halves; a copy of either, in any spelling, or a third vote
// adds nothing. Peers' votes reach it only for rounds the driver accepts (see countable), so the set
// holds at most validators x 2 types x 2 votes per round. The set is re-broadcast by reGossip and reset
// on every height change (commit / fast-forward).
func (node *Node) rememberVote(vote consensus.Vote) bool {
	if vote.Height != node.height() {
		return false // only the current height is worth re-gossiping; other heights are stale or ahead
	}
	slot := voteSlot(vote)
	if first, found := node.heightVotes[slot]; found {
		if _, conflictFound := node.heightVotes[slot+":conflict"]; conflictFound || first.BlockHash == vote.BlockHash {
			return false
		}
		slot += ":conflict"
	}
	node.heightVotes[slot] = vote
	return true
}

// voteSlot names a validator's vote of one round and type at the height in progress (see rememberVote).
func voteSlot(vote consensus.Vote) string {
	return fmt.Sprintf("%s:%d:%d", vote.Validator, vote.Round, vote.Type)
}

// holdsVote reports whether the node holds this very vote, signature bytes included, as the first vote
// of its slot or the conflicting one (see rememberVote).
func (node *Node) holdsVote(vote consensus.Vote) bool {
	first, firstFound := node.heightVotes[voteSlot(vote)]
	conflict, conflictFound := node.heightVotes[voteSlot(vote)+":conflict"]
	return firstFound && first == vote || conflictFound && conflict == vote
}

// reGossip re-broadcasts what the node holds for the height in progress, and what decided the last
// one (consensusMessages). Nothing is re-sent while no driver runs.
//
// Votes are otherwise broadcast only once (when first cast or first relayed), so a partition that
// splits the network can leave each side holding votes the other never received; without re-exchange
// the merged network can stay frozen below quorum-any, unable to arm the timeout that advances the
// round (a liveness stall the adversarial harness surfaced — safety was never at risk). Periodic
// re-gossip re-exchanges those held votes so the merged network reaches quorum-any and advances. The
// driver dedups on receipt (idempotent Add, equivocation still recorded once), so re-gossip cannot
// double-count or fork.
//
// The current round's proposal goes too: a round whose proposal broadcast was simply LOST used to burn
// the whole round timing out to nil. Only the current round's is worth re-flooding (it is the large
// message; older rounds are already being skipped past). It is a costly part of a tick, as is the last
// decided proposal: the whole block goes to every peer, and each receiver reads and decodes it
// (Server.handleProposal, up to 2 MiB) before onProposal finds the round's proposal held and drops the
// copy.
func (node *Node) reGossip() {
	if node.driver == nil {
		return
	}
	for _, message := range node.consensusMessages() {
		node.transport.Broadcast(node.self, message)
	}
}

// consensusMessages is what re-gossip sends, and what a bundle carries first (spec §7.3), in this order:
//
//  1. what decided the last height (lastCommitMessages);
//  2. the votes the node holds for the current round;
//  3. the current round's proposal, if the node holds one;
//  4. the other votes it holds for the height in progress: the past rounds' first, newest first, then
//     the future rounds', nearest first.
//
// Within a round the votes go in the order of their slots (voteSlot), so a node that holds the same
// messages gives the same list. A node one height behind that takes them in this order, as a bundle's
// import does, decides the last height first, and then takes the votes and the proposal of the next in
// its post-commit window, where it would otherwise have dropped them as another height's. The public
// bundle ends at MaxPublicBundleBytes, and a bank stuck for dozens of rounds holds more votes than fit:
// the cut then drops the future rounds, farthest first, then the oldest rounds, which no longer decide
// anything, and never the current round's votes or proposal. A rogue validator can send votes for up to
// consensus.MaxRoundLookahead rounds ahead, about 2.4 MB of them: coming before the past rounds, they
// would push past the cut the recent ones, which hold the prevotes a proposal's ValidRound names (review
// R2-3).
func (node *Node) consensusMessages() []Message {
	messages := node.lastCommitMessages()
	currentRound := node.round()
	// rank is where a round's votes go: the current round's first, at 0, then the past rounds', newest
	// first, then the future rounds', nearest first, after every past round, whose rank is below 2³¹.
	rank := func(round int32) int64 {
		if round > currentRound {
			return 1<<32 + int64(round-currentRound)
		}
		return int64(currentRound - round)
	}
	slots := slices.SortedFunc(maps.Keys(node.heightVotes), func(first, second string) int {
		return cmp.Or(cmp.Compare(rank(node.heightVotes[first].Round), rank(node.heightVotes[second].Round)), strings.Compare(first, second))
	}) // by rank, and within a round by slot
	appendVotes := func(ofTheCurrentRound bool) {
		for _, slot := range slots {
			if vote := node.heightVotes[slot]; (vote.Round == currentRound) == ofTheCurrentRound {
				messages = append(messages, Message{Type: MessageVote, Vote: &vote})
			}
		}
	}
	appendVotes(true)
	if proposal, found := node.heightProposals[currentRound]; found {
		messages = append(messages, Message{Type: MessageProposal, Proposal: &proposal})
	}
	appendVotes(false)
	return messages
}

// lastCommitMessages is what decided the previous height: its precommits and the proposal they decided.
// The nodes that decided a height re-send nothing else of it, yet a node still there may lack one: a
// precommit (a validator that decided on its own precommit, logged before a restart, never sent it), or
// the proposal, every copy of which can be lost while the others decide without it. Lacking either,
// that node can only wait for a checkpoint, and with its catch-up sources down it waits for good. So a
// node sends them as it commits (sendLastCommit), while that node is at most one height behind (the
// next commit replaces them), and again on every re-gossip. A node past that height drops them (see
// recordLateDoubleSign and onProposal).
func (node *Node) lastCommitMessages() []Message {
	var messages []Message
	for _, committedVote := range node.lastCommit.Votes {
		vote := committedVote
		messages = append(messages, Message{Type: MessageVote, Vote: &vote})
	}
	if proposal := node.lastProposal; proposal.Proposal.Height > 0 && proposal.Proposal.Height == node.lastCommit.Height {
		messages = append(messages, Message{Type: MessageProposal, Proposal: &proposal})
	}
	return messages
}

// sendLastCommit broadcasts what decided the previous height (lastCommitMessages), as the node commits.
func (node *Node) sendLastCommit() {
	for _, message := range node.lastCommitMessages() {
		node.transport.Broadcast(node.self, message)
	}
}

// rememberProposal keeps an authentic proposal for the current height so re-gossip can re-send it
// (see reGossip). First per round wins, mirroring the driver, so an equivocating proposer cannot make
// nodes re-gossip conflicting blocks for one round.
func (node *Node) rememberProposal(proposal ProposalMessage) {
	if _, found := node.heightProposals[proposal.Proposal.Round]; !found {
		node.heightProposals[proposal.Proposal.Round] = proposal
	}
}

// armTimeout schedules a timeout that grows with the round, so that if the network is slow the rounds
// eventually stretch past message latency and consensus makes progress (eventual synchrony). The base
// per-step timeout is the GOVERNED value from committed parameters, or the slow-down switch when that
// is longer (see stepTimeout). The timer carries the current height, and fires into no other.
func (node *Node) armTimeout(step consensus.Step, round int32) {
	delay := node.stepTimeout() * time.Duration(round+1)
	height := node.height()
	time.AfterFunc(delay, func() {
		node.enqueue(event{kind: eventTimeout, step: step, round: round, height: height})
	})
}

// stepTimeout is the base of the round timers (armTimeout): the current governed per-step consensus
// timeout, taken from committed parameters (with a floor so a mis-seeded genesis can't wedge the loop),
// or the node's MinimumStepTimeout, the slow-down switch, when that is longer.
func (node *Node) stepTimeout() time.Duration {
	milliseconds := node.state.Parameters.StepTimeoutMs
	if milliseconds <= 0 {
		milliseconds = 1000
	}
	return max(time.Duration(milliseconds)*time.Millisecond, node.config.MinimumStepTimeout)
}

// blockInterval is the current governed exchange-window cadence, taken from committed parameters.
// commit floors it at 1 ms.
func (node *Node) blockInterval() time.Duration {
	return time.Duration(node.state.Parameters.BlockIntervalMs) * time.Millisecond
}

// commit applies the block the driver decided and makes the result the committed state; then it saves
// the snapshot, empties the sign log, writes the record, publishes the view, reconciles the pools,
// re-resolves the peers, sends the last commit, writes the public folder, and starts the next height
// after the block interval; at a height it waits at (see waitForOwnSignatures), after the wait
// (ownSignaturesWait) if that is longer. The public folder comes after the record: its status must not pass a
// height before the record holds it (see publishCommittedState).
func (node *Node) commit(decision consensus.ActionDecided) {
	block, found := node.blocks[decision.Commit.BlockHash]
	if !found || block.Height != node.height() {
		return
	}
	nextState, err := ledger.ApplyBlock(node.config.Chain, node.state, node.lastBlockHash, block)
	if err != nil {
		return
	}
	previousState := node.state // for the record and the pools
	node.state = nextState      // the height in progress moves to the next one with it
	node.lastBlockHash = decision.Commit.BlockHash
	node.lastBlockHeader = block.Header()
	node.lastCommit = decision.Commit
	node.lastProposal = node.heightProposals[decision.Commit.Round]
	node.lastCommitValidators = node.heightValidators // the set that signed this commit, for checkpoints we serve
	// The decided driver is done, and until startHeight builds the next one no driver runs (see
	// onProposal). Keep the double-signs it saw, under the keys of the set it counted votes under, to
	// freeze their names in a later block.
	node.retireDriver()
	node.heightValidators = node.config.Chain.ValidatorRule.SetFor(node.state) // the next height's set, which the post-commit window authenticates against
	// Whether the node waits at the next height, for its own earlier votes (see waitForOwnSignatures).
	waits := node.height() <= node.mayHaveSignedUpTo
	if node.saveSnapshot() == nil {
		// The snapshot of the height is on disk, so a restart starts above it, and the log of what the
		// node signed there is emptied, for the next height: the sign log on disk keeps no block while the
		// chain idles. The node still keeps the last decided proposal, its block included, in memory
		// until the next commit or catch-up (lastProposal, which sendLastCommit sends again). After a
		// failed write the log stays, for a restart at that height. Before a height the node waits at, the
		// log is emptied at its own height instead, so that a restart during the wait waits too.
		logHeight := node.height()
		if waits {
			logHeight = node.signLog.Height
		}
		node.emptySignLog(logHeight)
	}
	node.recordCommittedBlock(previousState, block, node.lastProposal.Proposal, node.lastCommit, node.lastCommitValidators)
	node.publishView()
	node.reconcilePools(previousState.Accounts)
	node.relaySeen = map[string]bool{}             // bound the relay-dedup set; old messages won't re-flood usefully
	node.heightVotes = map[string]consensus.Vote{} // previous height's votes are done; start the new height clean
	node.heightProposals = map[int32]ProposalMessage{}
	node.invalidProposals = map[int32]int{}
	node.blocks = map[[32]byte]ledger.Block{}
	node.resolvePeersFromState() // re-resolve gossip peers from any address changes this block (peer DNS)
	node.sendLastCommit()
	node.writePublicFolder(previousState.Accounts)
	interval := node.blockInterval()
	if waits {
		interval = max(interval, node.ownSignaturesWait())
	}
	if interval <= 0 {
		// Never re-enter startHeight synchronously: a single-validator network decides the next
		// height INSIDE that call, so commit→startHeight→decide→commit recurses to a stack
		// overflow when the governed interval is zero. A 1 ms timer floor makes it iterative.
		interval = time.Millisecond
	}
	height := node.height()
	time.AfterFunc(interval, func() { node.enqueue(event{kind: eventNextHeight, height: height}) })
}
