package consensus

import (
	"sort"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// MaxRoundLookahead bounds how far ahead of the current round a vote is accepted, so a rogue
// validator cannot force allocation of a VoteSet per round for arbitrarily many future rounds
// (memory exhaustion). It is far above any realistic round count within a single height. A node
// keeps and relays only the votes a driver accepts, so it bounds those too.
const MaxRoundLookahead int32 = 256

// This file is the round driver: the Tendermint-style state machine that turns the safety core
// (validator set, votes, commit) into a live protocol. It fixes the liveness half of the audit
// finding -- a single-phase >1/2 snapshot cannot lock a value, so it can neither guarantee
// safety across rounds nor make progress after a failed round. The driver runs two phases
// (prevote -> precommit) with a lock, and advances rounds on timeout.
//
// It is a PURE state machine: timeouts arrive as events (Timeout), not real timers, so it is
// fully unit-testable. The caller (a networked node) feeds it events -- Resume at the start of every
// height, ReceiveProposal, ReceiveVote, Timeout -- and executes the Actions it returns
// (broadcast a proposal/vote, arm a timer, or record a decision). The driver signs its own votes
// (a PrivateKey signs deterministically, so the driver stays deterministic) and counts them itself.
//
// Scope: a single height with full locking (lockedValue/lockedRound, validValue/validRound), round
// advancement, and the "f+1 messages from a higher round -> skip to it" catch-up for round
// stragglers (see applyOneRule); the skip is a liveness aid and does not affect safety.

// Step is the phase within a round.
type Step uint8

const (
	StepPropose Step = iota
	StepPrevote
	StepPrecommit
)

// nilBlockHash (all-zero) is the "vote for no block" value. A real block hash is a SHA-256 hash,
// never all-zero, so this is a safe sentinel.
var nilBlockHash [32]byte

// Proposal is a block proposal for a round. BlockHash is the proposed block's hash (ledger.BlockHash);
// ValidRound is the POL round it was locked at (-1 for a fresh proposal). The driver works on block
// hashes; block validation is the caller's job: it passes only proposals whose block applied
// (ledger.ApplyBlock).
type Proposal struct {
	Round      int32
	BlockHash  [32]byte
	ValidRound int32
}

// Action is something the driver asks the caller to do. Callers type-switch on the concrete type.
type Action interface{ isAction() } // implemented by the four Action types below, and only by them

// ActionProposal: this node is proposer -- broadcast this proposal.
type ActionProposal struct { // implements Action
	Round      int32
	BlockHash  [32]byte
	ValidRound int32
}

// ActionBroadcastVote: broadcast this already-signed vote to peers.
type ActionBroadcastVote struct{ Vote Vote } // implements Action

// ActionSchedule: arm a timer for (Step, Round); when it fires, call Timeout(Step, Round).
type ActionSchedule struct { // implements Action
	Step  Step
	Round int32
}

// ActionDecided: the height decided a block. Commit is the ⅔ precommit certificate that finalised it,
// and holds the height, the round and the block hash -- a durable, independently-verifiable proof of
// the decision (used by onboarding sync so a joining node can verify the state it is handed).
type ActionDecided struct { // implements Action
	Commit Commit
}

// These empty methods are what make the four types Actions: Action's one method is unexported, so
// no type outside this package can be one.
func (ActionProposal) isAction()      {}
func (ActionBroadcastVote) isAction() {}
func (ActionSchedule) isAction()      {}
func (ActionDecided) isAction()       {}

// Driver is the per-height consensus state machine for one validator node.
type Driver struct {
	height       int64
	signer       signing.Signer
	self         string // this node's name, which its votes carry
	validatorSet *ValidatorSet
	scheme       signing.Scheme
	networkID    string

	round int32
	step  Step

	lockedValue *[32]byte
	lockedRound int32
	validValue  *[32]byte
	validRound  int32

	// proposeValue is the block hash this node proposes when it is proposer with no locked value.
	proposeValue [32]byte

	prevotes   map[int32]*VoteSet
	precommits map[int32]*VoteSet
	proposals  map[int32]Proposal

	decided bool

	sentPrevote               map[int32]bool
	sentPrecommit             map[int32]bool
	scheduledPrevoteTimeout   map[int32]bool
	scheduledPrecommitTimeout map[int32]bool
	gotPrevoteQuorum          map[int32]bool // "+2/3 prevotes for the proposed value" one-shot per round

	signLog func(Vote, Proposal) error // makes an own vote durable before it is counted (see SetSignLog)
}

// NewDriver builds a driver for a validator node. signer holds this node's key and self is its name,
// which its votes carry; proposeValue is the hash of the block it will propose when it is the proposer and
// holds no lock. With a nil signer the driver follows the rounds and decides, but never signs a vote or a
// proposal: the node's name is not seated with the node's key (spec 6.3).
func NewDriver(signer signing.Signer, self string, validatorSet *ValidatorSet, scheme signing.Scheme, networkID string, height int64, proposeValue [32]byte) *Driver {
	return &Driver{
		height: height, signer: signer, self: self,
		validatorSet: validatorSet, scheme: scheme, networkID: networkID,
		lockedRound: -1, validRound: -1, proposeValue: proposeValue,
		prevotes:   map[int32]*VoteSet{},
		precommits: map[int32]*VoteSet{},
		proposals:  map[int32]Proposal{},

		sentPrevote:               map[int32]bool{},
		sentPrecommit:             map[int32]bool{},
		scheduledPrevoteTimeout:   map[int32]bool{},
		scheduledPrecommitTimeout: map[int32]bool{},
		gotPrevoteQuorum:          map[int32]bool{},
	}
}

// Round returns the round the driver is in.
func (driver *Driver) Round() int32 { return driver.round }

// Proposer returns the validator scheduled to propose at the given round — stake-weighted and
// deterministic (see ValidatorSet.ProposerAt), so a validator's chance to propose is proportional
// to its voting power.
func (driver *Driver) Proposer(round int32) string {
	return driver.validatorSet.ProposerAt(driver.height, round)
}

// SetSignLog has the driver hand each of its own votes, once signed and before it is counted or
// released, to signLog, together with the driver's proposal for the vote's round (the zero Proposal
// if it holds none). The caller makes the vote durable there (its write-ahead sign log). If signLog
// fails, the vote is dropped like a failed signature and retried later (see castVote).
func (driver *Driver) SetSignLog(signLog func(Vote, Proposal) error) { driver.signLog = signLog }

// Resume begins the height, holding the proposals the caller already has for it,
// and, after a restart, from this validator's own votes at the height, as its sign log kept them or
// as peers re-gossiped them back to it. Each vote that verifies under the key the set holds for this
// validator is re-counted and marked sent, so the driver never signs a second, different vote for the
// same round and type; a proposal of its own among those held is sent again, never replaced by another
// block (see startRound); the lock and valid value are restored from the highest-round precommit of a
// value; and the driver resumes at the latest round it voted in, never earlier. Starting afresh at
// round 0 instead would be amnesia: an unlocked restart can complete a quorum that conflicts with a
// value already decided elsewhere. With no vote of its own, the driver begins at round 0. The node
// begins every height with Resume.
func (driver *Driver) Resume(ownVotes []Vote, proposals []Proposal) []Action {
	for _, proposal := range proposals {
		if _, held := driver.proposals[proposal.Round]; !held {
			driver.proposals[proposal.Round] = proposal
		}
	}
	latestRound := int32(0)
	for _, vote := range ownVotes {
		if vote.Height == driver.height && vote.Validator == driver.self && vote.Round >= 0 && driver.count(vote) {
			driver.voted(vote)
			latestRound = max(latestRound, vote.Round)
		}
	}
	return driver.startRound(latestRound)
}

// count tallies a vote in the set of its round and type, and reports whether the vote set verified its
// signature: the vote counted, was counted already, or proved a double-sign (VoteSet.Add checks the
// signature before it looks for one). A vote of no known type is not counted.
func (driver *Driver) count(vote Vote) bool {
	driver.ensureRound(vote.Round)
	var err error
	switch vote.Type {
	case Prevote:
		_, err = driver.prevotes[vote.Round].Add(driver.scheme, driver.networkID, vote)
	case Precommit:
		_, err = driver.precommits[vote.Round].Add(driver.scheme, driver.networkID, vote)
	default:
		return false
	}
	return err == nil || err == ErrEquivocation
}

// voted marks a counted vote of this validator's own as cast, whether it signed it just now, logged
// it before a restart, or got it back from peers' re-gossip after it came back without its sign log:
// the driver never signs a second, different vote for its round and type. The step of the current
// round moves on to it, and a precommit of a value restores the lock it took, and the valid value,
// unless the driver holds a later one.
func (driver *Driver) voted(vote Vote) {
	if vote.Type == Prevote {
		driver.sentPrevote[vote.Round] = true
		if vote.Round == driver.round && driver.step == StepPropose {
			driver.step = StepPrevote
		}
		return
	}
	driver.sentPrecommit[vote.Round] = true
	if vote.Round == driver.round {
		driver.step = StepPrecommit
	}
	if vote.BlockHash != nilBlockHash && vote.Round > driver.lockedRound {
		driver.lockedValue, driver.lockedRound = blockHashPointer(vote.BlockHash), vote.Round
	}
	if vote.BlockHash != nilBlockHash && vote.Round > driver.validRound {
		driver.validValue, driver.validRound = blockHashPointer(vote.BlockHash), vote.Round
	}
}

func (driver *Driver) ensureRound(round int32) {
	if driver.prevotes[round] == nil {
		driver.prevotes[round] = NewVoteSet(driver.validatorSet, driver.height, round, Prevote)
		driver.precommits[round] = NewVoteSet(driver.validatorSet, driver.height, round, Precommit)
	}
}

// sortedPrecommitRounds returns the rounds with a precommit set, ascending, so the decision scan picks
// the lowest round this node holds a quorum for.
func (driver *Driver) sortedPrecommitRounds() []int32 {
	rounds := make([]int32, 0, len(driver.precommits))
	for round := range driver.precommits {
		rounds = append(rounds, round)
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i] < rounds[j] })
	return rounds
}

func (driver *Driver) startRound(round int32) []Action {
	driver.round = round
	driver.step = StepPropose
	if driver.sentPrecommit[round] { // only after Resume: re-entering a round this node already voted in
		driver.step = StepPrecommit
	} else if driver.sentPrevote[round] {
		driver.step = StepPrevote
	}
	driver.ensureRound(round)

	var actions []Action
	if driver.signer != nil && driver.self == driver.Proposer(round) && !driver.sentPrevote[round] { // never re-propose a round it already voted in
		// A proposal it holds for its own round is its own, signed before a restart: it is sent again,
		// never replaced by another block.
		proposal, held := driver.proposals[round]
		if !held {
			proposal = Proposal{Round: round, BlockHash: driver.proposeValue, ValidRound: -1}
			if driver.validValue != nil {
				proposal.BlockHash, proposal.ValidRound = *driver.validValue, driver.validRound
			}
			driver.proposals[round] = proposal
		}
		actions = append(actions, ActionProposal{Round: round, BlockHash: proposal.BlockHash, ValidRound: proposal.ValidRound})
	}
	// The proposer arms it too: if its own prevote cannot be signed, this timeout is the retry.
	actions = append(actions, ActionSchedule{Step: StepPropose, Round: round})
	return append(actions, driver.evaluate()...)
}

// ReceiveProposal records a proposal (assumed already checked by the caller to be from the round's
// proposer, with a block that applied) and re-evaluates.
func (driver *Driver) ReceiveProposal(proposal Proposal) []Action {
	driver.ensureRound(proposal.Round)
	if _, held := driver.proposals[proposal.Round]; !held {
		driver.proposals[proposal.Round] = proposal
	}
	return driver.evaluate()
}

// ReceiveVote verifies (via the vote set) and records a peer vote, then re-evaluates. A vote that the
// vote set does not verify (wrong height or round, no known type, a name outside the set, a bad
// signature) is dropped, and nothing is re-evaluated; a double-sign is counted out and kept as Evidence
// inside the vote set. A verified vote of this validator's own, re-gossiped back to it, counts as cast
// (see voted).
func (driver *Driver) ReceiveVote(vote Vote) []Action {
	if vote.Height != driver.height {
		return nil
	}
	if vote.Round < 0 || vote.Round > driver.round+MaxRoundLookahead {
		return nil // reject far-future (or negative) rounds — bounds per-round allocation
	}
	if !driver.count(vote) {
		return nil
	}
	if vote.Validator == driver.self {
		driver.voted(vote) // the vote set verified it under the key it holds for this validator
	}
	return driver.evaluate()
}

// Timeout delivers a fired timer for (step, round).
func (driver *Driver) Timeout(step Step, round int32) []Action {
	if round != driver.round || driver.decided {
		return nil
	}
	switch step {
	case StepPropose:
		if driver.step == StepPropose && !driver.sentPrevote[round] {
			return driver.timeoutVote(Prevote, round)
		}
	case StepPrevote:
		if driver.step == StepPrevote && !driver.sentPrecommit[round] {
			return driver.timeoutVote(Precommit, round)
		}
	case StepPrecommit:
		return driver.startRound(round + 1)
	}
	return nil
}

// timeoutVote casts the nil vote a timed-out step calls for. If the vote cannot be signed, the same
// timeout is armed again, so the vote is retried instead of the round stalling.
func (driver *Driver) timeoutVote(voteType VoteType, round int32) []Action {
	step := driver.step
	if actions := driver.castVote(voteType, round, nilBlockHash); actions != nil {
		return append(actions, driver.evaluate()...)
	}
	return []Action{ActionSchedule{Step: step, Round: round}}
}

// castVote signs a vote by this node, has the sign log (if set) make it durable, counts it in the
// local tally, sets the phase, and returns the broadcast action. It sets the sent* one-shot so the
// same vote is never re-emitted. If signing or the sign log fails (a signer that is down, a full
// disk), nothing changes and it returns no action: the rule that cast the vote reports no progress
// (applyOneRule) and fires again on the next event, and a timeout re-arms (timeoutVote). A driver with
// no signer marks the vote as cast, unsigned and uncounted, and returns an empty action list: the rules
// move on, no timeout re-arms for it, and nothing is sent.
func (driver *Driver) castVote(voteType VoteType, round int32, blockHash [32]byte) []Action {
	vote := Vote{Height: driver.height, Round: round, Type: voteType, BlockHash: blockHash, Validator: driver.self}
	if driver.signer == nil {
		driver.voted(vote)
		return []Action{}
	}
	signed, err := SignVote(driver.signer, vote)
	if err == nil && driver.signLog != nil {
		err = driver.signLog(signed, driver.proposals[round])
	}
	if err != nil {
		return nil
	}
	driver.count(signed)
	driver.voted(signed)
	return []Action{ActionBroadcastVote{Vote: signed}}
}

// evaluate applies all "upon" rules to a fixpoint (one transition per pass, repeated until none
// fire), so a vote that unlocks a cascade of transitions is fully processed.
func (driver *Driver) evaluate() []Action {
	var allActions []Action
	for {
		actions, progressed := driver.applyOneRule()
		allActions = append(allActions, actions...)
		if !progressed {
			break
		}
	}
	return allActions
}

func (driver *Driver) applyOneRule() ([]Action, bool) {
	if driver.decided {
		return nil, false // height decided; ignore further votes/proposals (Tendermint halts on decide)
	}
	currentRound := driver.round

	// upon proposal + 2/3 precommits for its block hash (any round): decide. Rounds are scanned in
	// ascending order, so the node decides at the lowest round it holds a quorum for. Two nodes can
	// decide one block at different rounds, each commit holding the precommits its own node collected.
	for _, round := range driver.sortedPrecommitRounds() {
		precommitSet := driver.precommits[round]
		proposal, held := driver.proposals[round]
		if held && proposal.BlockHash != nilBlockHash && precommitSet.HasQuorumFor(proposal.BlockHash) {
			// The quorum can come before this node precommits, and count a double-signer whose other
			// precommit it has not seen yet. A node that has seen it counts the double-signer out
			// (VoteSet.Add) and needs this node's precommit in its place, so the node precommits the
			// block it decides, and its commit carries that vote. The block is decided: the vote can
			// complete no other quorum.
			var actions []Action
			if !driver.sentPrecommit[round] {
				actions = driver.castVote(Precommit, round, proposal.BlockHash)
			}
			driver.decided = true
			commit, _ := precommitSet.Commit()
			return append(actions, ActionDecided{Commit: commit}), false
		}
	}

	// upon f+1 total voting power from a round ahead of ours: skip forward to it (round straggler
	// catch-up). Safe because >1/3 of power from that round means an honest validator is already there.
	// It only fires when this node is behind in rounds; in normal single-round progress there are no
	// higher-round votes, so it never triggers.
	skipToRound := driver.round
	skipThreshold := driver.validatorSet.SkipThreshold()
	for round := range driver.prevotes {
		if round > skipToRound && (driver.prevotes[round].TotalVoted() >= skipThreshold || driver.precommits[round].TotalVoted() >= skipThreshold) {
			skipToRound = round
		}
	}
	if skipToRound > driver.round {
		return driver.startRound(skipToRound), true
	}

	// upon proposal for the current round while in propose step: cast our prevote.
	if driver.step == StepPropose && !driver.sentPrevote[currentRound] {
		if proposal, held := driver.proposals[currentRound]; held {
			if proposal.ValidRound == -1 {
				return driver.prevoteFor(currentRound, proposal, -1), driver.sentPrevote[currentRound] // no progress if it could not be signed
			}
			if proposal.ValidRound >= 0 && proposal.ValidRound < currentRound {
				if validRoundPrevotes := driver.prevotes[proposal.ValidRound]; validRoundPrevotes != nil && validRoundPrevotes.HasQuorumFor(proposal.BlockHash) {
					return driver.prevoteFor(currentRound, proposal, proposal.ValidRound), driver.sentPrevote[currentRound]
				}
			}
		}
	}

	// upon 2/3 prevotes for the proposed value, while step >= prevote (first time): set validValue,
	// and if still in prevote step, lock it and precommit it. The step >= prevote guard is
	// Tendermint line 36 — without it the one-shot can be consumed at propose step (before we have
	// prevoted), permanently skipping the lock/precommit branch and stalling the round to nil.
	if proposal, held := driver.proposals[currentRound]; held && proposal.BlockHash != nilBlockHash && driver.step >= StepPrevote &&
		driver.prevotes[currentRound].HasQuorumFor(proposal.BlockHash) && !driver.gotPrevoteQuorum[currentRound] {
		driver.validValue = blockHashPointer(proposal.BlockHash)
		driver.validRound = currentRound
		if driver.step != StepPrevote || driver.sentPrecommit[currentRound] {
			driver.gotPrevoteQuorum[currentRound] = true
			return nil, true
		}
		if actions := driver.castVote(Precommit, currentRound, proposal.BlockHash); actions != nil {
			driver.gotPrevoteQuorum[currentRound] = true
			driver.lockedValue = blockHashPointer(proposal.BlockHash)
			driver.lockedRound = currentRound
			return actions, true
		}
		// The precommit was refused (a signer that is down, a full disk): nothing is consumed and
		// nothing locked, so this rule fires again on the next event, and the rules below arm the
		// prevote timeout as usual (see castVote).
	}

	// upon 2/3 prevotes for nil while in prevote step: precommit nil. If the precommit cannot be cast,
	// go on to the rule below: the prevote timeout it arms retries it (see timeoutVote). The usual
	// quorum of nil prevotes is also the quorum that arms that timeout, and no new vote may come.
	if driver.step == StepPrevote && driver.prevotes[currentRound].HasQuorumFor(nilBlockHash) && !driver.sentPrecommit[currentRound] {
		if actions := driver.castVote(Precommit, currentRound, nilBlockHash); actions != nil {
			return actions, true
		}
	}

	// upon 2/3 prevotes(*) while in prevote step (first time): arm the prevote timeout.
	if driver.step == StepPrevote && driver.prevotes[currentRound].HasQuorumAny() && !driver.scheduledPrevoteTimeout[currentRound] {
		driver.scheduledPrevoteTimeout[currentRound] = true
		return []Action{ActionSchedule{Step: StepPrevote, Round: currentRound}}, true
	}

	// upon 2/3 precommits(*) (first time): arm the precommit timeout.
	if driver.precommits[currentRound].HasQuorumAny() && !driver.scheduledPrecommitTimeout[currentRound] {
		driver.scheduledPrecommitTimeout[currentRound] = true
		return []Action{ActionSchedule{Step: StepPrecommit, Round: currentRound}}, true
	}

	return nil, false
}

// prevoteFor applies the locking rule to decide whether to prevote the proposed value or nil.
// It prevotes the value if we hold no lock, or we are locked on it, or there is a proof-of-lock at
// proofOfLockRound >= our lockedRound. Otherwise it prevotes nil. The value is valid: the caller
// passes only proposals whose block applied.
func (driver *Driver) prevoteFor(round int32, proposal Proposal, proofOfLockRound int32) []Action {
	blockHash := nilBlockHash
	switch {
	case driver.lockedValue == nil:
		blockHash = proposal.BlockHash
	case *driver.lockedValue == proposal.BlockHash:
		blockHash = proposal.BlockHash
	case proofOfLockRound >= 0 && driver.lockedRound <= proofOfLockRound:
		blockHash = proposal.BlockHash
	}
	return driver.castVote(Prevote, round, blockHash)
}

func blockHashPointer(blockHash [32]byte) *[32]byte {
	blockHashCopy := blockHash
	return &blockHashCopy
}

// Evidence aggregates the equivocation proofs this driver has gathered across its vote sets, so a
// node can carry them into a block to freeze the offenders' names.
func (driver *Driver) Evidence() []VoteEvidence {
	var evidence []VoteEvidence
	for _, prevoteSet := range driver.prevotes {
		evidence = append(evidence, prevoteSet.Evidence()...)
	}
	for _, precommitSet := range driver.precommits {
		evidence = append(evidence, precommitSet.Evidence()...)
	}
	return evidence
}
