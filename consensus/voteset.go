package consensus

import (
	"sort"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// VoteSet accumulates verified votes for a single (height, round, type) and tallies power per
// block hash. A validator's second, conflicting vote is recorded as Evidence and the
// validator's power is removed entirely -- an equivocator gets no vote.
type VoteSet struct {
	height       int64
	round        int32
	voteType     VoteType
	validatorSet *ValidatorSet
	votes        map[string]Vote
	power        map[[32]byte]int64
	equivocators map[string]bool
	evidence     []VoteEvidence
}

// NewVoteSet creates an empty vote set scoped to one (height, round, type).
func NewVoteSet(validatorSet *ValidatorSet, height int64, round int32, voteType VoteType) *VoteSet {
	return &VoteSet{
		height: height, round: round, voteType: voteType, validatorSet: validatorSet,
		votes:        map[string]Vote{},
		power:        map[[32]byte]int64{},
		equivocators: map[string]bool{},
	}
}

// Add verifies and records a vote. It returns (counted, error): counted is true only if the
// vote was newly tallied. A vote is rejected (and NOT counted) when it is for the wrong
// height/round/type, does not name a set validator, does not verify under the key the set holds for
// that name, or is a conflicting second vote from a validator (which is recorded as evidence and
// un-counts that validator).
func (voteSet *VoteSet) Add(scheme signing.Scheme, networkID string, vote Vote) (bool, error) {
	if vote.Height != voteSet.height || vote.Round != voteSet.round || vote.Type != voteSet.voteType {
		return false, ErrWrongVote
	}
	if !voteSet.validatorSet.Contains(vote.Validator) {
		return false, ErrNotValidator
	}
	if !vote.Verify(scheme, networkID, voteSet.validatorSet.PublicKey(vote.Validator)) {
		return false, ErrBadSignature
	}
	if voteSet.equivocators[vote.Validator] {
		return false, ErrEquivocation
	}
	if previous, found := voteSet.votes[vote.Validator]; found {
		if previous.BlockHash == vote.BlockHash {
			return false, nil // identical duplicate: ignore
		}
		// Conflicting second vote: un-count the first, ban from this set, keep the proof.
		voteSet.power[previous.BlockHash] -= voteSet.validatorSet.Power(vote.Validator)
		voteSet.equivocators[vote.Validator] = true
		voteSet.evidence = append(voteSet.evidence, VoteEvidence{Name: vote.Validator, First: previous, Second: vote})
		return false, ErrEquivocation
	}
	voteSet.votes[vote.Validator] = vote
	voteSet.power[vote.BlockHash] += voteSet.validatorSet.Power(vote.Validator)
	return true, nil
}

// TwoThirdsMajority returns the block hash that has reached quorum power, if any. The map iteration is
// order-independent ONLY because quorum is a strict >2/3 majority, so at most one block hash can ever
// reach it within one set — weaken the quorum rule and this must pick deterministically instead.
func (voteSet *VoteSet) TwoThirdsMajority() ([32]byte, bool) {
	quorum := voteSet.validatorSet.Quorum()
	for blockHash, power := range voteSet.power {
		if power >= quorum {
			return blockHash, true
		}
	}
	return [32]byte{}, false
}

// TotalVoted is the total counted power across all block hashes (equivocators excluded).
func (voteSet *VoteSet) TotalVoted() int64 {
	var sum int64
	for _, power := range voteSet.power {
		sum += power
	}
	return sum
}

// HasQuorumAny reports whether the total counted power has reached quorum (>2/3 for any mix
// of values) -- the "+2/3 prevotes(*)" condition that starts a timeout.
func (voteSet *VoteSet) HasQuorumAny() bool {
	return voteSet.TotalVoted() >= voteSet.validatorSet.Quorum()
}

// HasQuorumFor reports whether one specific block hash has reached quorum power.
func (voteSet *VoteSet) HasQuorumFor(blockHash [32]byte) bool {
	return voteSet.power[blockHash] >= voteSet.validatorSet.Quorum()
}

// Evidence returns a copy of the equivocation evidence gathered so far.
func (voteSet *VoteSet) Evidence() []VoteEvidence {
	return append([]VoteEvidence(nil), voteSet.evidence...)
}

// Commit assembles a commit certificate from a precommit vote set if a block hash has reached
// quorum. It returns (Commit, false) for a prevote set or when no block hash has quorum. The
// committed hash is exactly the block hash validators precommitted -- what got agreed is
// what gets applied (audit A2).
func (voteSet *VoteSet) Commit() (Commit, bool) {
	if voteSet.voteType != Precommit {
		return Commit{}, false
	}
	blockHash, hasMajority := voteSet.TwoThirdsMajority()
	if !hasMajority {
		return Commit{}, false
	}
	var votes []Vote
	for _, vote := range voteSet.votes {
		if vote.BlockHash == blockHash && !voteSet.equivocators[vote.Validator] {
			votes = append(votes, vote)
		}
	}
	sort.Slice(votes, func(i, j int) bool { return votes[i].Validator < votes[j].Validator })
	return Commit{Height: voteSet.height, Round: voteSet.round, BlockHash: blockHash, Votes: votes}, true
}
