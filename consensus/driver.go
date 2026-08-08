package consensus

import (
	"sort"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// maxRoundLookahead bounds how far ahead of the current round a vote is accepted, so a rogue
// validator cannot force allocation of a VoteSet per round for arbitrarily many future rounds
// (memory exhaustion). It is far above any realistic round count within a single height.
const maxRoundLookahead int32 = 256

// This file is the round driver: the Tendermint-style state machine that turns the safety core
// (validator set, votes, commit) into a live protocol. It fixes the liveness half of the audit
// finding -- a single-phase >1/2 snapshot cannot lock a value, so it can neither guarantee
// safety across rounds nor make progress after a failed round. The driver runs two phases
// (prevote -> precommit) with a lock, and advances rounds on timeout.
//
// It is a PURE state machine: timeouts arrive as events (Timeout), not real timers, so it is
// fully unit-testable. The caller (a networked node) feeds it events -- Start, ReceiveProposal,
// ReceiveVote, Timeout -- and executes the Actions it returns (broadcast a proposal/vote, arm a
// timer, or record a decision). The driver signs its own votes (Ed25519 is deterministic, so it
// stays deterministic) and counts them itself.
//
// Scope: a single height with full locking (lockedValue/lockedRound, validValue/validRound), round
// advancement, and the "f+1 messages from a higher round -> skip to it" catch-up for round
// stragglers (see step1); the skip is a liveness aid and does not affect safety.

// Step is the phase within a round.
type Step uint8

const (
	StepPropose Step = iota
	StepPrevote
	StepPrecommit
)

// nilDigest (all-zero) is the "vote for no block" value. A real state digest is a SHA-256 hash,
// never all-zero, so this is a safe sentinel.
var nilDigest [32]byte

// Proposal is a block proposal for a round. Digest is the proposed block's state digest;
// ValidRound is the POL round it was locked at (-1 for a fresh proposal); Valid is whether the
// caller validated the underlying block (via ledger.ApplyBlock). The driver works on digests;
// block validation is the caller's job.
type Proposal struct {
	Round      int32
	Digest     [32]byte
	ValidRound int32
	Valid      bool
}

// Action is something the driver asks the caller to do. Callers type-switch on the concrete type.
type Action interface{ isAction() }

// ActProposal: this node is proposer -- broadcast this proposal.
type ActProposal struct {
	Round      int32
	Digest     [32]byte
	ValidRound int32
}

// ActBroadcastVote: broadcast this already-signed vote to peers.
type ActBroadcastVote struct{ Vote Vote }

// ActSchedule: arm a timer for (Step, Round); when it fires, call Timeout(Step, Round).
type ActSchedule struct {
	Step  Step
	Round int32
}

// ActDecided: the height decided this block digest. Commit is the ⅔ precommit certificate that
// finalised it -- a durable, independently-verifiable proof of the decision (used by onboarding
// sync so a joining node can verify the state it is handed).
type ActDecided struct {
	Height int64
	Round  int32
	Digest [32]byte
	Commit Commit
}

func (ActProposal) isAction()      {}
func (ActBroadcastVote) isAction() {}
func (ActSchedule) isAction()      {}
func (ActDecided) isAction()       {}

// Driver is the per-height consensus state machine for one validator node.
type Driver struct {
	height    int64
	signer    *signing.Signer
	self      string
	vals      *ValidatorSet
	scheme    signing.Scheme
	networkID string

	round int32
	step  Step

	lockedValue *[32]byte
	lockedRound int32
	validValue  *[32]byte
	validRound  int32

	// proposeValue is the digest this node proposes when it is proposer with no locked value.
	proposeValue [32]byte

	prevotes   map[int32]*VoteSet
	precommits map[int32]*VoteSet
	proposals  map[int32]Proposal

	decided  bool
	decision [32]byte

	sentPrevote      map[int32]bool
	sentPrecommit    map[int32]bool
	schedPrevoteTO   map[int32]bool
	schedPrecommitTO map[int32]bool
	gotPrevoteQuorum map[int32]bool // "+2/3 prevotes for the proposed value" one-shot per round
}

// NewDriver builds a driver for a validator node. signer is this node's identity; proposeValue
// is the block digest it will propose when it is the proposer and holds no lock.
func NewDriver(signer *signing.Signer, vals *ValidatorSet, scheme signing.Scheme, networkID string, height int64, proposeValue [32]byte) *Driver {
	return &Driver{
		height: height, signer: signer, self: signer.PublicKeyBase64(),
		vals: vals, scheme: scheme, networkID: networkID,
		lockedRound: -1, validRound: -1, proposeValue: proposeValue,
		prevotes:   map[int32]*VoteSet{},
		precommits: map[int32]*VoteSet{},
		proposals:  map[int32]Proposal{},

		sentPrevote:      map[int32]bool{},
		sentPrecommit:    map[int32]bool{},
		schedPrevoteTO:   map[int32]bool{},
		schedPrecommitTO: map[int32]bool{},
		gotPrevoteQuorum: map[int32]bool{},
	}
}

// Round, Step and Decision expose state for the caller and tests.
func (d *Driver) Round() int32 { return d.round }
func (d *Driver) Step() Step   { return d.step }
func (d *Driver) Decision() ([32]byte, bool) {
	return d.decision, d.decided
}

// Proposer returns the validator scheduled to propose at the given round — stake-weighted and
// deterministic (see ValidatorSet.ProposerAt), so a validator's chance to propose is proportional
// to its voting power.
func (d *Driver) Proposer(round int32) string {
	return d.vals.ProposerAt(d.height, round)
}

// Start begins the height at round 0.
func (d *Driver) Start() []Action { return d.startRound(0) }

func (d *Driver) ensureRound(r int32) {
	if d.prevotes[r] == nil {
		d.prevotes[r] = NewVoteSet(d.vals, d.height, r, Prevote)
		d.precommits[r] = NewVoteSet(d.vals, d.height, r, Precommit)
	}
}

// sortedPrecommitRounds returns the rounds with a precommit set, ascending — so the decision scan is
// deterministic across nodes (all pick the lowest committing round).
func (d *Driver) sortedPrecommitRounds() []int32 {
	rounds := make([]int32, 0, len(d.precommits))
	for rr := range d.precommits {
		rounds = append(rounds, rr)
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i] < rounds[j] })
	return rounds
}

func (d *Driver) startRound(r int32) []Action {
	d.round = r
	d.step = StepPropose
	d.ensureRound(r)

	var acts []Action
	if d.self == d.Proposer(r) {
		digest := d.proposeValue
		vr := int32(-1)
		if d.validValue != nil {
			digest = *d.validValue
			vr = d.validRound
		}
		if _, ok := d.proposals[r]; !ok {
			d.proposals[r] = Proposal{Round: r, Digest: digest, ValidRound: vr, Valid: true}
		}
		acts = append(acts, ActProposal{Round: r, Digest: digest, ValidRound: vr})
	} else {
		acts = append(acts, ActSchedule{Step: StepPropose, Round: r})
	}
	return append(acts, d.evaluate()...)
}

// ReceiveProposal records a proposal (assumed already checked to be from the round's proposer
// and validated by the caller, who sets Valid) and re-evaluates.
func (d *Driver) ReceiveProposal(p Proposal) []Action {
	d.ensureRound(p.Round)
	if _, ok := d.proposals[p.Round]; !ok {
		d.proposals[p.Round] = p
	}
	return d.evaluate()
}

// ReceiveVote verifies (via the vote set) and records a peer vote, then re-evaluates. Invalid
// votes (wrong height, bad signature, equivocation) are dropped; equivocation is retained as
// Evidence inside the vote set.
func (d *Driver) ReceiveVote(v Vote) []Action {
	if v.Height != d.height {
		return nil
	}
	if v.Round < 0 || v.Round > d.round+maxRoundLookahead {
		return nil // reject far-future (or negative) rounds — bounds per-round allocation
	}
	d.ensureRound(v.Round)
	switch v.Type {
	case Prevote:
		d.prevotes[v.Round].Add(d.scheme, d.networkID, v)
	case Precommit:
		d.precommits[v.Round].Add(d.scheme, d.networkID, v)
	default:
		return nil
	}
	return d.evaluate()
}

// Timeout delivers a fired timer for (step, round).
func (d *Driver) Timeout(step Step, round int32) []Action {
	if round != d.round || d.decided {
		return nil
	}
	switch step {
	case StepPropose:
		if d.step == StepPropose && !d.sentPrevote[round] {
			return append(d.castVote(Prevote, round, nilDigest), d.evaluate()...)
		}
	case StepPrevote:
		if d.step == StepPrevote && !d.sentPrecommit[round] {
			return append(d.castVote(Precommit, round, nilDigest), d.evaluate()...)
		}
	case StepPrecommit:
		return d.startRound(round + 1)
	}
	return nil
}

// castVote signs a vote by this node, counts it in the local tally, sets the phase, and returns
// the broadcast action. It sets the sent* one-shot so the same vote is never re-emitted.
func (d *Driver) castVote(t VoteType, round int32, digest [32]byte) []Action {
	v := Vote{Height: d.height, Round: round, Type: t, BlockDigest: digest, Validator: d.self}
	signed, err := SignVote(d.signer, v)
	if err != nil {
		// Unreachable in practice: Ed25519 signing is deterministic over an in-memory key with no
		// I/O, so this only fails on a corrupt key / programming error. Returning nil here silently
		// drops the transition — a known limitation of the pure (error-free) state-machine interface;
		// a signer this broken is a fatal misconfiguration the node should have caught at startup.
		return nil
	}
	d.ensureRound(round)
	if t == Prevote {
		d.prevotes[round].Add(d.scheme, d.networkID, signed)
		d.sentPrevote[round] = true
		if d.round == round {
			d.step = StepPrevote
		}
	} else {
		d.precommits[round].Add(d.scheme, d.networkID, signed)
		d.sentPrecommit[round] = true
		if d.round == round {
			d.step = StepPrecommit
		}
	}
	return []Action{ActBroadcastVote{Vote: signed}}
}

// evaluate applies all "upon" rules to a fixpoint (one transition per pass, repeated until none
// fire), so a vote that unlocks a cascade of transitions is fully processed.
func (d *Driver) evaluate() []Action {
	var all []Action
	for {
		acts, progressed := d.step1()
		all = append(all, acts...)
		if !progressed || d.decided {
			break
		}
	}
	return all
}

func (d *Driver) step1() ([]Action, bool) {
	if d.decided {
		return nil, false // height decided; ignore further votes/proposals (Tendermint halts on decide)
	}
	r := d.round

	// upon proposal + 2/3 precommits for its digest (any round): decide. Rounds are scanned in
	// ascending order so every node picks the SAME (lowest) round at which the block committed;
	// otherwise two nodes could emit commits carrying different rounds for the same decision.
	for _, rr := range d.sortedPrecommitRounds() {
		pc := d.precommits[rr]
		p, ok := d.proposals[rr]
		if ok && p.Valid && p.Digest != nilDigest && pc.HasQuorumFor(p.Digest) {
			d.decided = true
			d.decision = p.Digest
			commit, _ := pc.Commit()
			return []Action{ActDecided{Height: d.height, Round: rr, Digest: p.Digest, Commit: commit}}, false
		}
	}

	// upon f+1 total voting power from a round ahead of ours: skip forward to it (round straggler
	// catch-up). Safe because >1/3 of power from round rr means an honest validator is already there.
	// It only fires when this node is behind in rounds; in normal single-round progress there are no
	// higher-round votes, so it never triggers.
	if !d.decided {
		skip := d.round
		thresh := d.vals.SkipThreshold()
		for rr := range d.prevotes {
			if rr > skip && (d.prevotes[rr].TotalVoted() >= thresh || d.precommits[rr].TotalVoted() >= thresh) {
				skip = rr
			}
		}
		if skip > d.round {
			return d.startRound(skip), true
		}
	}

	// upon proposal for the current round while in propose step: cast our prevote.
	if d.step == StepPropose && !d.sentPrevote[r] {
		if p, ok := d.proposals[r]; ok {
			if p.ValidRound == -1 {
				return d.prevoteFor(r, p, -1), true
			}
			if p.ValidRound >= 0 && p.ValidRound < r {
				if pv := d.prevotes[p.ValidRound]; pv != nil && pv.HasQuorumFor(p.Digest) {
					return d.prevoteFor(r, p, p.ValidRound), true
				}
			}
		}
	}

	// upon 2/3 prevotes for the proposed value, while step >= prevote (first time): set validValue,
	// and if still in prevote step, lock it and precommit it. The step >= prevote guard is
	// Tendermint line 36 — without it the one-shot can be consumed at propose step (before we have
	// prevoted), permanently skipping the lock/precommit branch and stalling the round to nil.
	if p, ok := d.proposals[r]; ok && p.Valid && p.Digest != nilDigest && d.step >= StepPrevote &&
		d.prevotes[r].HasQuorumFor(p.Digest) && !d.gotPrevoteQuorum[r] {
		d.gotPrevoteQuorum[r] = true
		d.validValue = digestPtr(p.Digest)
		d.validRound = r
		if d.step == StepPrevote && !d.sentPrecommit[r] {
			d.lockedValue = digestPtr(p.Digest)
			d.lockedRound = r
			return d.castVote(Precommit, r, p.Digest), true
		}
		return nil, true
	}

	// upon 2/3 prevotes for nil while in prevote step: precommit nil.
	if d.step == StepPrevote && d.prevotes[r].HasQuorumFor(nilDigest) && !d.sentPrecommit[r] {
		return d.castVote(Precommit, r, nilDigest), true
	}

	// upon 2/3 prevotes(*) while in prevote step (first time): arm the prevote timeout.
	if d.step == StepPrevote && d.prevotes[r].HasQuorumAny() && !d.schedPrevoteTO[r] {
		d.schedPrevoteTO[r] = true
		return []Action{ActSchedule{Step: StepPrevote, Round: r}}, true
	}

	// upon 2/3 precommits(*) (first time): arm the precommit timeout.
	if d.precommits[r].HasQuorumAny() && !d.schedPrecommitTO[r] {
		d.schedPrecommitTO[r] = true
		return []Action{ActSchedule{Step: StepPrecommit, Round: r}}, true
	}

	return nil, false
}

// prevoteFor applies the locking rule to decide whether to prevote the proposed value or nil.
// It prevotes the value if it is valid and (we hold no lock, or we are locked on it, or there is
// a proof-of-lock at round vr >= our lockedRound). Otherwise it prevotes nil.
func (d *Driver) prevoteFor(r int32, p Proposal, vr int32) []Action {
	digest := nilDigest
	if p.Valid {
		switch {
		case d.lockedValue == nil:
			digest = p.Digest
		case *d.lockedValue == p.Digest:
			digest = p.Digest
		case vr >= 0 && d.lockedRound <= vr:
			digest = p.Digest
		}
	}
	return d.castVote(Prevote, r, digest)
}

func digestPtr(d [32]byte) *[32]byte { c := d; return &c }
