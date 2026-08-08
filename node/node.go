package node

import (
	"errors"
	"math/big"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// Config is a node's static configuration.
type Config struct {
	NetworkID        string
	Scheme           signing.Scheme
	Signer           *signing.Signer
	Validators       *consensus.ValidatorSet
	TransactionCost  *big.Rat
	Timeout          time.Duration // base per-step timeout; grows with round for eventual synchrony
	MinBlockInterval time.Duration // minimum wall time between committing a block and starting the next
	Genesis          []ledger.Account
	Bootstrap        []string // peer base URLs to sync the committed state from on start (join/rejoin)
	SnapshotPath     string   // if set, persist the committed checkpoint here and reload it on start
	BackupPath       string   // if set, also mirror each committed checkpoint here (e.g. a mounted share), async
	MaxMempool       int      // cap on pending payments (0 = unlimited) — bounds a valid-payment flood
	MaxCommentLength int      // cap on a payment's Comment length in bytes (0 = unlimited)
	MaxMessageBytes  int      // GENESIS value of the governed per-message serialized-size cap (0 = unbounded)
	MaxPeers         int      // >0 applies the Hypernode topology when resolving on-chain peer addresses

	// ValidatorThreshold, set together with a nil Validators, enables DYNAMIC validator admission:
	// the validator set is derived from the committed accounts each block — an account whose
	// stake (balance summed by its ValidatorNode) reaches the threshold becomes a validator, with
	// voting power equal to that stake. Leave Validators non-nil for a fixed set.
	ValidatorThreshold *big.Rat
}

type eventKind uint8

const (
	evStart eventKind = iota
	evNextHeight
	evProposal
	evVote
	evTimeout
	evPayment
	evUpdate
	evParameterVote
	evReassignmentVote
	evQuery
	evGovernanceQuery
	evRegossip
	evAdopt
	evCatchUp
)

type event struct {
	kind         eventKind
	proposal     *ProposalMsg
	vote         *consensus.Vote
	payment      *ledger.Payment
	update       *ledger.AccountUpdate
	paramChange  *ledger.ParameterChange
	reassignment *ledger.Reassignment
	step         consensus.Step
	round        int32
	query        chan Snapshot
	govQuery     chan GovernanceView
	checkpoint   *Checkpoint
	admit        chan error // if set on an evPayment, receives the admission verdict
}

// Snapshot is a race-free view of the node's state, obtained via the event loop.
type Snapshot struct {
	Height             int64
	Accounts           []ledger.Account
	Parameters         ledger.LedgerParameters // committed operating parameters (for checkpoints)
	GovernanceSequence int64                   // committed governance sequence (for checkpoints)
	Decided            [32]byte
	LastCommit         consensus.Commit
	Validators         []consensus.Validator // the set that signed LastCommit (for checkpoints)
}

// Node is a single validator. All mutable state is owned by the run loop; external callers only
// touch it through Submit*/Snapshot, which post events onto the loop's channel.
type Node struct {
	cfg       Config
	self      string
	transport Transport

	events chan event
	quit   chan struct{}

	// state owned by run()
	height             int64
	accounts           []ledger.Account
	parameters         ledger.LedgerParameters // committed operating parameters (governed; see governance)
	governanceSequence int64                   // committed governance sequence (single-use guard for certs)
	driver             *consensus.Driver
	mem                *mempool
	updates            *updatePool
	blocks             map[[32]byte]ledger.Block
	lastDecision       [32]byte
	lastCommit         consensus.Commit
	started            bool
	heightStarted      int64                     // the height startHeight last began, so a stale re-trigger is a no-op
	relaySeen          map[string]bool           // relay dedup, so a message floods a multi-hop topology once
	heightVotes        map[string]consensus.Vote // votes seen at the current height, re-gossiped periodically (see reGossipLoop)
	heightProposals    map[int32]ProposalMsg     // authentic proposals seen at the current height, by round (first wins, like the driver)

	heightVals     *consensus.ValidatorSet // the set governing the height in progress
	lastCommitVals *consensus.ValidatorSet // the set that signed lastCommit (served in checkpoints)

	pendingSlashes        map[string]consensus.Evidence      // equivocation proofs to carry into the next block (by offender)
	paramProposals        map[string]*ledger.ParameterChange // accumulating parameter-change votes (by proposal)
	reassignmentProposals map[string]*ledger.Reassignment    // accumulating balance-reassignment votes (by proposal)
}

// New builds a node. If a snapshot is configured and present on disk, it is loaded as the starting
// state. It does not start the loop; call Start.
func New(cfg Config, transport Transport) *Node {
	n := &Node{
		cfg:       cfg,
		self:      cfg.Signer.PublicKeyBase64(),
		transport: transport,
		events:    make(chan event, 1024),
		quit:      make(chan struct{}),
		height:    1,
		accounts:  cfg.Genesis,
		parameters: ledger.LedgerParameters{
			TransactionCost: cfg.TransactionCost,
			BlockIntervalMs: cfg.MinBlockInterval.Milliseconds(),
			StepTimeoutMs:   cfg.Timeout.Milliseconds(),
			MaxPeers:        cfg.MaxPeers,
			MaxMessageBytes: cfg.MaxMessageBytes,
		},
		mem:                   newMempool(cfg.MaxMempool, cfg.MaxCommentLength),
		updates:               newUpdatePool(cfg.MaxMempool),
		blocks:                map[[32]byte]ledger.Block{},
		relaySeen:             map[string]bool{},
		heightVotes:           map[string]consensus.Vote{},
		heightProposals:       map[int32]ProposalMsg{},
		pendingSlashes:        map[string]consensus.Evidence{},
		paramProposals:        map[string]*ledger.ParameterChange{},
		reassignmentProposals: map[string]*ledger.Reassignment{},
	}
	n.loadSnapshot()
	return n
}

// Start launches the run loop. If Bootstrap peers are configured, it first syncs the committed
// state from them (join/rejoin) and then begins consensus at the synced height; otherwise it
// begins at the genesis height.
func (n *Node) Start() {
	go n.run()
	if len(n.cfg.Bootstrap) > 0 {
		go n.syncThenStart()
	} else {
		n.enqueue(event{kind: evStart})
	}
	// Ongoing catch-up runs regardless, from bootstrap OR gossip peers, so a node that falls a full
	// height behind self-recovers instead of getting stuck.
	go n.catchUpLoop()
	// Periodic re-gossip of the current height's votes and current round's proposal: a merged
	// network re-exchanges held votes so a partition-formed stall can advance once the partition
	// lifts, and a lost proposal is re-sent instead of burning its round (see reGossipLoop).
	go n.reGossipLoop()
}

// Stop halts the run loop.
func (n *Node) Stop() { close(n.quit) }

// SubmitProposal / SubmitVote / SubmitPayment are the thread-safe inputs.
func (n *Node) SubmitProposal(p ProposalMsg)   { n.enqueue(event{kind: evProposal, proposal: &p}) }
func (n *Node) SubmitVote(v consensus.Vote)    { n.enqueue(event{kind: evVote, vote: &v}) }
func (n *Node) SubmitPayment(p ledger.Payment) { n.enqueue(event{kind: evPayment, payment: &p}) }

// AdmitPayment submits a payment and waits for the loop's admission verdict: nil if the payment is
// queued (or was already queued — an idempotent resubmission), an error naming the refusal
// otherwise. This is the synchronous intake used by the HTTP layer so a receipt is only ever signed
// for a payment the node actually holds — see admitPayment for the overdraw guardrail.
func (n *Node) AdmitPayment(p ledger.Payment) error {
	verdict := make(chan error, 1)
	n.enqueue(event{kind: evPayment, payment: &p, admit: verdict})
	select {
	case err := <-verdict:
		return err
	case <-n.quit:
		return errors.New("node stopped")
	}
}

// SubmitUpdate is the thread-safe input for a signed account-attribute update (name/delegation).
func (n *Node) SubmitUpdate(u ledger.AccountUpdate) { n.enqueue(event{kind: evUpdate, update: &u}) }

// SubmitParameterVote is the thread-safe input for a validator's parameter-change vote(s).
func (n *Node) SubmitParameterVote(c ledger.ParameterChange) {
	n.enqueue(event{kind: evParameterVote, paramChange: &c})
}

// SubmitReassignmentVote is the thread-safe input for a validator's balance-reassignment vote(s).
func (n *Node) SubmitReassignmentVote(r ledger.Reassignment) {
	n.enqueue(event{kind: evReassignmentVote, reassignment: &r})
}

// Snapshot returns a race-free view of node state.
func (n *Node) Snapshot() Snapshot {
	resp := make(chan Snapshot, 1)
	n.enqueue(event{kind: evQuery, query: resp})
	select {
	case s := <-resp:
		return s
	case <-n.quit:
		return Snapshot{}
	}
}

// GovernanceView returns a race-free read-only view of governance (current parameters, validators,
// and pending proposals with tallies) for an admin UI.
func (n *Node) GovernanceView() GovernanceView {
	resp := make(chan GovernanceView, 1)
	n.enqueue(event{kind: evGovernanceQuery, govQuery: resp})
	select {
	case view := <-resp:
		return view
	case <-n.quit:
		return GovernanceView{}
	}
}

func (n *Node) enqueue(e event) {
	select {
	case n.events <- e:
	case <-n.quit:
	}
}

func (n *Node) run() {
	for {
		select {
		case <-n.quit:
			return
		case e := <-n.events:
			n.handle(e)
		}
	}
}

func (n *Node) handle(e event) {
	switch e.kind {
	case evStart:
		if !n.started {
			n.started = true
			n.startHeight()
		}
	case evNextHeight:
		n.startHeight()
	case evAdopt:
		n.adopt(*e.checkpoint)
	case evCatchUp:
		n.catchUp(*e.checkpoint)
	case evProposal:
		// Relay only an AUTHENTIC proposal (from the round's proposer, correctly signed); an
		// unauthenticated one is dropped, not gossiped onward.
		if n.onProposal(*e.proposal) {
			n.relay(Message{Type: MsgProposal, From: n.self, Proposal: e.proposal})
		}
	case evVote:
		if n.driver != nil {
			n.process(n.driver.ReceiveVote(*e.vote))
		}
		n.rememberVote(*e.vote)
		n.relay(Message{Type: MsgVote, From: n.self, Vote: e.vote})
	case evTimeout:
		if n.driver != nil {
			n.process(n.driver.Timeout(e.step, e.round))
		}
	case evPayment:
		err := n.admitPayment(*e.payment)
		if e.admit != nil {
			e.admit <- err
		}
	case evUpdate:
		// Same governed size bound as payments — an oversized update would otherwise put an
		// arbitrarily large name/description into permanent, every-block-rehashed state for free.
		if ledger.WithinMessageBound(n.parameters.MaxMessageBytes, *e.update) && n.updates.add(*e.update) {
			n.relay(Message{Type: MsgUpdate, From: n.self, Update: e.update})
		}
	case evParameterVote:
		if n.addParameterVotes(*e.paramChange) {
			n.relay(Message{Type: MsgParameterVote, From: n.self, ParameterChange: e.paramChange})
		}
	case evReassignmentVote:
		if n.addReassignmentVotes(*e.reassignment) {
			n.relay(Message{Type: MsgReassignmentVote, From: n.self, Reassignment: e.reassignment})
		}
	case evQuery:
		var lastVals []consensus.Validator
		if n.lastCommitVals != nil {
			lastVals = n.lastCommitVals.Validators()
		}
		// The account slice is copied, but each Account.Amount *big.Rat pointer is SHARED with the
		// loop's live state. This is race-free only because ledger.ProcessPayments (and the other
		// appliers) treat amounts as immutable — they allocate a fresh big.Rat rather than mutating
		// in place — so the loop never writes through a pointer a reader holds. Any future in-place
		// amount mutation in ledger would break that invariant and must instead deep-copy here.
		e.query <- Snapshot{
			Height:             n.height,
			Accounts:           append([]ledger.Account(nil), n.accounts...),
			Parameters:         n.parameters,
			GovernanceSequence: n.governanceSequence,
			Decided:            n.lastDecision,
			LastCommit:         n.lastCommit,
			Validators:         lastVals,
		}
	case evGovernanceQuery:
		e.govQuery <- n.governanceView()
	case evRegossip:
		n.reGossipVotes()
		n.reGossipProposal()
	}
}

// adopt fast-forwards to a verified checkpoint if it is ahead of us and consensus has not started
// yet (a fresh or rebooting node). The certificate is verified against the validator set and must
// bind the digest of the supplied accounts, so we trust the ⅔ quorum, not the node that served it.
func (n *Node) adopt(cp Checkpoint) {
	if n.started {
		return
	}
	if cp.Commit.Height+1 <= n.height {
		return // not ahead of our configured/current height
	}
	if !n.verifyCheckpoint(cp, n.accounts) {
		return
	}
	n.fastForward(cp)
}

// fastForward sets the committed state to a checkpoint. The caller MUST have verified it.
func (n *Node) fastForward(cp Checkpoint) {
	n.accounts = cp.Accounts
	n.parameters = cp.Parameters
	n.governanceSequence = cp.GovernanceSequence
	n.height = cp.Commit.Height + 1
	n.lastCommit = cp.Commit
	n.lastDecision = cp.Commit.BlockDigest
	n.lastCommitVals = n.checkpointSigners(cp)
	n.heightVotes = map[string]consensus.Vote{} // votes from the abandoned height no longer apply
	n.heightProposals = map[int32]ProposalMsg{} // likewise its proposals
	n.resolvePeersFromState()                   // adopt the peer addresses published in the synced state
}

// catchUp lets a RUNNING node that fell behind (missed a commit window) fast-forward to a peer's
// later verified checkpoint and resume consensus at the new height, instead of staying stuck. Stale
// timers/messages from the abandoned height are dropped by their height/round guards.
func (n *Node) catchUp(cp Checkpoint) {
	if !n.started {
		return // pre-start is adopt()'s job
	}
	if cp.Commit.Height+1 <= n.height {
		return // not actually ahead of us
	}
	if !n.verifyCheckpoint(cp, n.accounts) {
		return
	}
	n.fastForward(cp)
	n.startHeight()
}

// checkpointSigners is the validator set that signed a checkpoint's commit: the carried set for a
// dynamic network (whose set may differ from what today's accounts yield), else the configured/
// derived set.
func (n *Node) checkpointSigners(cp Checkpoint) *consensus.ValidatorSet {
	if n.cfg.Validators == nil && len(cp.Validators) > 0 {
		return consensus.NewValidatorSet(cp.Validators)
	}
	return n.validatorsFor(cp.Accounts)
}

// verifyCheckpoint reports whether cp is trustworthy from THIS node's vantage: its Commit is a valid
// >2/3 quorum of the set that signed it, which binds the accounts' digest, AND — the trust anchor —
// more than 1/3 of the power of the validator set derived from `anchor` also signed the commit, so
// at least one validator we already trust vouches for the (possibly changed) signing set. This is
// the BFT "skipping" rule: it lets a node safely adopt a checkpoint whose validator set grew while
// it was away, without trusting the serving peer. (Full rotation past the anchor would need a chain
// of checkpoints; for incremental growth the anchor overlaps the signing set — the real case here.)
func (n *Node) verifyCheckpoint(cp Checkpoint, anchor []ledger.Account) bool {
	if !cp.Commit.Verify(n.cfg.Scheme, n.cfg.NetworkID, n.checkpointSigners(cp)) {
		return false
	}
	if (ledger.LedgerState{Accounts: cp.Accounts, Parameters: cp.Parameters, GovernanceSequence: cp.GovernanceSequence}).Digest() != cp.Commit.BlockDigest {
		return false
	}
	trusted := n.validatorsFor(anchor)
	var vouched int64
	for _, voter := range cp.Commit.Voters() {
		vouched += trusted.Power(voter)
	}
	return vouched*3 > trusted.TotalPower()
}

// slashEvidence returns the pending equivocation proofs for offenders that are still validators, to
// carry into a proposed block. Once slashed (removed from state) they stop being validators and are
// pruned in commit, so a proof is not carried forever.
func (n *Node) slashEvidence() []consensus.Evidence {
	if len(n.pendingSlashes) == 0 {
		return nil
	}
	vals := n.validatorsFor(n.accounts)
	var out []consensus.Evidence
	for offender, ev := range n.pendingSlashes {
		if vals.Contains(offender) {
			out = append(out, ev)
		}
	}
	return out
}

// admitPayment is the payment intake: it queues (and relays) a payment only if it could actually
// clear. The load-bearing check is the overdraw guardrail — a payment is refused, with no receipt,
// if the payer's already-promised outgoing plus this payment exceeds its committed balance. The
// ledger's response to an overdraw inside a committed block is deliberately harsh (the payer is
// banned and its balance destroyed — see ledger.ProcessPayments); with every honest intake refusing
// up front, an honest user cannot stumble into that, and a banned overdraw in a committed block can
// only mean a misused/cloned key or a proposer that bypassed intake. Refused payments are also not
// relayed, so junk doesn't propagate.
//
// The guard is against this node's local view (committed balance + its own pending pool); pending
// incoming credits don't count, so it is conservative. A duplicate of an already-queued payment is
// admitted as a no-op (idempotent resubmission — the receipt can be re-issued).
func (n *Node) admitPayment(p ledger.Payment) error {
	if p.Amount == nil || p.Amount.Sign() <= 0 {
		return errors.New("non-positive amount")
	}
	if p.BlockID < n.height {
		return errors.New("stale block id: the payment names an already-committed block")
	}
	if !ledger.WithinMessageBound(n.parameters.MaxMessageBytes, p) {
		return errors.New("payment too large: its serialized form exceeds the governed message bound")
	}
	if !ledger.RecipientResolves(n.accounts, p) {
		// Refuse a payment that resolves to nobody rather than admit it, relay it, and hand back a
		// receipt for what the ledger will silently drop. This is against the current committed
		// registry, so a name must already be registered before you pay it (register, wait for the
		// block, then pay — the wallet's normal flow); a typo'd or not-yet-registered name is a clear
		// error here instead of a no-op the sender mistakes for a completed payment.
		return errors.New("unresolvable recipient: no account holds that name, or the payment names both/neither a key and a name")
	}
	if n.mem.contains(p) {
		return nil // already queued: an idempotent resubmission
	}
	promised := new(big.Rat).Add(n.mem.outgoingFor(p.PublicKeyFrom), p.Amount)
	if promised.Cmp(ledger.AccountAmount(n.accounts, p.PublicKeyFrom)) > 0 {
		return errors.New("would overdraw: the payer's pending outgoing plus this payment exceeds its balance")
	}
	if !n.mem.add(p) {
		return errors.New("not admitted: mempool full or comment too long")
	}
	n.relay(Message{Type: MsgPayment, From: n.self, Payment: &p})
	return nil
}

// startHeight builds a candidate block from the mempool (used if this node is proposer) and
// starts a fresh Driver for the current height.
func (n *Node) startHeight() {
	if n.heightStarted == n.height && n.driver != nil {
		return // already started this height — ignore a stale evNextHeight/evCatchUp that would
		//        otherwise replace the live driver mid-height and discard its in-progress votes.
	}
	n.heightStarted = n.height
	n.heightVals = n.validatorsFor(n.accounts) // the set governing this height (and signing its commit)
	candidateBlock := ledger.ProposeBlock(n.cfg.Scheme, n.cfg.NetworkID, n.height,
		ledger.LedgerState{Accounts: n.accounts, Parameters: n.parameters, GovernanceSequence: n.governanceSequence},
		ledger.BlockContent{
			Payments:        n.mem.snapshot(),
			Updates:         n.updates.snapshot(),
			Evidence:        n.slashEvidence(),
			ParameterChange: n.pendingParameterChange(),
			Reassignment:    n.pendingReassignment(),
		},
		n.heightVals)
	n.blocks[candidateBlock.StateDigest] = candidateBlock
	n.driver = consensus.NewDriver(n.cfg.Signer, n.heightVals, n.cfg.Scheme, n.cfg.NetworkID, n.height, candidateBlock.StateDigest)
	n.process(n.driver.Start())
}

// onProposal ingests a proposal and returns whether it was AUTHENTIC (so the caller relays it). A
// proposal is authentic only if it is for the current height and is signed by the round's scheduled
// proposer — without that check any machine could front-run the proposer with an invalid block and
// make honest nodes prevote nil forever, an unauthenticated liveness kill. Block validity (via
// ApplyBlock) sets the driver's Valid flag but does not gate relay: an authentic proposal of an
// invalid block is still relayed so peers can see it and prevote nil.
func (n *Node) onProposal(p ProposalMsg) bool {
	if p.Height != n.height || n.driver == nil {
		return false
	}
	if held, ok := n.heightProposals[p.Round]; ok && held.Block.StateDigest == p.Block.StateDigest {
		// We already hold this round's proposal, so do NOT relay this duplicate. The proposal was
		// flooded on its first sight, and reGossipProposal periodically re-broadcasts the AUTHENTIC
		// held copy, so propagation is covered. Relaying the duplicate would be unsafe: only its
		// StateDigest is matched here (an attacker-settable field the proposal signature covers but
		// the block BODY is not), so a copied digest can carry a swapped, unverified body — and the
		// proposer's own broadcast bypasses relay() dedup, so it, uniquely, would re-flood that body.
		return false
	}
	if p.Proposer != n.driver.Proposer(p.Round) || !VerifyProposal(n.cfg.Scheme, n.cfg.NetworkID, p) {
		return false // wrong proposer or bad signature — drop, don't relay
	}
	_, valid := ledger.ApplyBlock(n.cfg.Scheme, n.cfg.NetworkID, ledger.LedgerState{Accounts: n.accounts, Parameters: n.parameters, GovernanceSequence: n.governanceSequence}, p.Block, n.validatorsFor(n.accounts))
	n.blocks[p.Block.StateDigest] = p.Block
	n.rememberProposal(p)
	n.process(n.driver.ReceiveProposal(consensus.Proposal{
		Round: p.Round, Digest: p.Block.StateDigest, ValidRound: p.ValidRound, Valid: valid,
	}))
	return true
}

func (n *Node) process(acts []consensus.Action) {
	for _, a := range acts {
		switch act := a.(type) {
		case consensus.ActProposal:
			blk, ok := n.blocks[act.Digest]
			if !ok {
				continue
			}
			proposal := &ProposalMsg{Height: n.height, Round: act.Round, ValidRound: act.ValidRound, Block: blk}
			if err := SignProposal(n.cfg.Signer, proposal); err != nil {
				continue // can't sign our own proposal (unreachable with a valid signer)
			}
			n.rememberProposal(*proposal)
			n.transport.Broadcast(n.self, Message{Type: MsgProposal, From: n.self, Proposal: proposal})
		case consensus.ActBroadcastVote:
			v := act.Vote
			n.rememberVote(v)
			n.transport.Broadcast(n.self, Message{Type: MsgVote, From: n.self, Vote: &v})
		case consensus.ActSchedule:
			n.arm(act.Step, act.Round)
		case consensus.ActDecided:
			n.commit(act)
		}
	}
}

// rememberVote records a vote seen at the current height (own or a peer's), keyed by its signer and
// signature so equivocating votes are both retained. The set is re-broadcast by reGossipVotes and
// reset on every height change (commit / fast-forward).
func (n *Node) rememberVote(v consensus.Vote) {
	if v.Height != n.height {
		return // only the current height is worth re-gossiping; other heights are stale or ahead
	}
	n.heightVotes[v.Validator+":"+v.Signature] = v
}

// reGossipVotes re-broadcasts every vote this node holds for the current height. Votes are otherwise
// broadcast only once (when first cast or first relayed), so a partition that splits the network can
// leave each side holding votes the other never received; without re-exchange the merged network can
// stay frozen below quorum-any, unable to arm the timeout that advances the round (a liveness stall
// the adversarial harness surfaced — safety was never at risk). Periodic re-gossip re-exchanges those
// held votes so the merged network reaches quorum-any and advances. The driver dedups on receipt
// (idempotent Add, equivocation still recorded once), so re-gossip cannot double-count or fork.
func (n *Node) reGossipVotes() {
	if n.driver == nil || len(n.heightVotes) == 0 {
		return
	}
	for _, v := range n.heightVotes {
		v := v
		n.transport.Broadcast(n.self, Message{Type: MsgVote, From: n.self, Vote: &v})
	}
}

// rememberProposal keeps an authentic proposal for the current height so reGossipProposal can
// re-send it. First per round wins, mirroring the driver, so an equivocating proposer cannot make
// nodes re-gossip conflicting blocks for one round.
func (n *Node) rememberProposal(p ProposalMsg) {
	if _, ok := n.heightProposals[p.Round]; !ok {
		n.heightProposals[p.Round] = p
	}
}

// reGossipProposal re-broadcasts the proposal for the driver's CURRENT round, if this node holds it.
// The vote counterpart above unfreezes a merged partition, but a round whose proposal broadcast was
// simply LOST used to burn the whole round timing out to nil — only the current round's proposal is
// worth re-flooding (it is the large message; older rounds are already being skipped past). Receivers
// dedup cheaply (onProposal's held-proposal fast path, relaySeen), so the steady-state cost per tick
// is one map lookup per peer.
func (n *Node) reGossipProposal() {
	if n.driver == nil {
		return
	}
	if p, ok := n.heightProposals[n.driver.Round()]; ok {
		n.transport.Broadcast(n.self, Message{Type: MsgProposal, From: n.self, Proposal: &p})
	}
}

// arm schedules a timeout that grows with the round, so that if the network is slow the rounds
// eventually stretch past message latency and consensus makes progress (eventual synchrony). The base
// per-step timeout is the GOVERNED value from committed parameters (see stepTimeout).
func (n *Node) arm(step consensus.Step, round int32) {
	d := n.stepTimeout() * time.Duration(round+1)
	time.AfterFunc(d, func() {
		n.enqueue(event{kind: evTimeout, step: step, round: round})
	})
}

// stepTimeout is the current governed per-step consensus timeout, taken from committed parameters
// (with a floor so a mis-seeded genesis can't wedge the loop).
func (n *Node) stepTimeout() time.Duration {
	milliseconds := n.parameters.StepTimeoutMs
	if milliseconds <= 0 {
		milliseconds = n.cfg.Timeout.Milliseconds()
	}
	if milliseconds <= 0 {
		milliseconds = 1000
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// blockInterval is the current governed exchange-window cadence, taken from committed parameters.
func (n *Node) blockInterval() time.Duration {
	if n.parameters.BlockIntervalMs <= 0 {
		return 0
	}
	return time.Duration(n.parameters.BlockIntervalMs) * time.Millisecond
}

func (n *Node) commit(act consensus.ActDecided) {
	blk, ok := n.blocks[act.Digest]
	if !ok {
		return
	}
	nextState, ok := ledger.ApplyBlock(n.cfg.Scheme, n.cfg.NetworkID, ledger.LedgerState{Accounts: n.accounts, Parameters: n.parameters, GovernanceSequence: n.governanceSequence}, blk, n.heightVals)
	if !ok {
		return
	}
	n.accounts = nextState.Accounts
	n.parameters = nextState.Parameters
	n.governanceSequence = nextState.GovernanceSequence
	n.lastDecision = act.Digest
	n.lastCommit = act.Commit
	n.lastCommitVals = n.heightVals // the set that signed this commit, for checkpoints we serve
	// Record any equivocation seen this height (to slash in a future block), and drop proofs for
	// offenders already slashed out of the validator set.
	if n.driver != nil {
		for _, ev := range n.driver.Evidence() {
			n.pendingSlashes[ev.Validator] = ev
		}
	}
	slashed := n.validatorsFor(n.accounts)
	for offender := range n.pendingSlashes {
		if !slashed.Contains(offender) {
			delete(n.pendingSlashes, offender)
		}
	}
	n.saveSnapshot()
	n.height++
	n.mem.dropBelow(n.height) // stale payments (their block has passed) can never replay
	// Re-establish the intake invariant against the NEW balances: a commit can shrink a payer's
	// balance under its pending payments (payments admitted by other nodes, a reassignment, a
	// slash), and a pending overdraw left in the pool would make this node propose a block that
	// bans — and destroys — the payer (see admitPayment / ledger.ProcessPayments).
	n.mem.dropOverdrawn(func(payer string) *big.Rat { return ledger.AccountAmount(n.accounts, payer) })
	n.updates.dropBelow(n.height)               // likewise for stale account-attribute updates
	n.pruneParameterProposals()                 // drop parameter votes that no longer amend the current parameters
	n.pruneReassignments()                      // drop reassignment votes that no longer amend the current balance
	n.relaySeen = map[string]bool{}             // bound the relay-dedup set; old messages won't re-flood usefully
	n.heightVotes = map[string]consensus.Vote{} // previous height's votes are done; start the new height clean
	n.heightProposals = map[int32]ProposalMsg{}
	n.blocks = map[[32]byte]ledger.Block{}
	n.resolvePeersFromState() // re-resolve gossip peers from any address changes this block (peer DNS)
	interval := n.blockInterval()
	if interval <= 0 {
		// Never re-enter startHeight synchronously: a single-validator network decides the next
		// height INSIDE that call, so commit→startHeight→decide→commit recurses to a stack
		// overflow when the governed interval is zero. A 1 ms timer floor makes it iterative.
		interval = time.Millisecond
	}
	time.AfterFunc(interval, func() { n.enqueue(event{kind: evNextHeight}) })
}
