package consensus

import (
	"sort"

	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// Evidence records that a validator equivocated: it signed two different votes for the same
// (height, round, type). Two conflicting signed votes by one key are a transferable, durable
// proof that any node can present later to slash the validator -- unlike a view-local check,
// which is non-durable and non-transferable (audit A1).
type Evidence struct {
	Validator string
	A, B      Vote
}

// VoteSet accumulates verified votes for a single (height, round, type) and tallies power per
// block digest. A validator's second, conflicting vote is recorded as Evidence and the
// validator's power is removed entirely -- an equivocator gets no vote.
type VoteSet struct {
	height       int64
	round        int32
	typ          VoteType
	vals         *ValidatorSet
	votes        map[string]Vote
	power        map[[32]byte]int64
	equivocators map[string]bool
	evidence     []Evidence
}

// NewVoteSet creates an empty vote set scoped to one (height, round, type).
func NewVoteSet(vals *ValidatorSet, height int64, round int32, typ VoteType) *VoteSet {
	return &VoteSet{
		height: height, round: round, typ: typ, vals: vals,
		votes:        map[string]Vote{},
		power:        map[[32]byte]int64{},
		equivocators: map[string]bool{},
	}
}

// Add verifies and records a vote. It returns (counted, error): counted is true only if the
// vote was newly tallied. A vote is rejected (and NOT counted) when it is for the wrong
// height/round/type, is not from a set validator, has a bad signature, or is a conflicting
// second vote from a validator (which is recorded as Evidence and un-counts that validator).
func (s *VoteSet) Add(scheme signing.Scheme, networkID string, v Vote) (bool, error) {
	if v.Height != s.height || v.Round != s.round || v.Type != s.typ {
		return false, ErrWrongVote
	}
	if !s.vals.Contains(v.Validator) {
		return false, ErrNotValidator
	}
	if !v.Verify(scheme, networkID) {
		return false, ErrBadSignature
	}
	if s.equivocators[v.Validator] {
		return false, ErrEquivocation
	}
	if prev, ok := s.votes[v.Validator]; ok {
		if prev.BlockDigest == v.BlockDigest {
			return false, nil // identical duplicate: ignore
		}
		// Conflicting second vote: un-count the first, ban from this set, keep the proof.
		s.power[prev.BlockDigest] -= s.vals.Power(v.Validator)
		s.equivocators[v.Validator] = true
		s.evidence = append(s.evidence, Evidence{Validator: v.Validator, A: prev, B: v})
		return false, ErrEquivocation
	}
	s.votes[v.Validator] = v
	s.power[v.BlockDigest] += s.vals.Power(v.Validator)
	return true, nil
}

// TwoThirdsMajority returns the digest that has reached quorum power, if any. The map iteration is
// order-independent ONLY because quorum is a strict >2/3 majority, so at most one digest can ever
// reach it within one set — weaken the quorum rule and this must pick deterministically instead.
func (s *VoteSet) TwoThirdsMajority() ([32]byte, bool) {
	q := s.vals.Quorum()
	for d, p := range s.power {
		if p >= q {
			return d, true
		}
	}
	return [32]byte{}, false
}

// TotalVoted is the total counted power across all digests (equivocators excluded).
func (s *VoteSet) TotalVoted() int64 {
	var sum int64
	for _, p := range s.power {
		sum += p
	}
	return sum
}

// PowerFor is the counted power for a specific digest.
func (s *VoteSet) PowerFor(d [32]byte) int64 { return s.power[d] }

// HasQuorumAny reports whether the total counted power has reached quorum (>2/3 for any mix
// of values) -- the "+2/3 prevotes(*)" condition that starts a timeout.
func (s *VoteSet) HasQuorumAny() bool { return s.TotalVoted() >= s.vals.Quorum() }

// HasQuorumFor reports whether one specific digest has reached quorum power.
func (s *VoteSet) HasQuorumFor(d [32]byte) bool { return s.power[d] >= s.vals.Quorum() }

// Evidence returns a copy of the equivocation evidence gathered so far.
func (s *VoteSet) Evidence() []Evidence { return append([]Evidence(nil), s.evidence...) }

// Commit assembles a commit certificate from a precommit vote set if a digest has reached
// quorum. It returns (Commit, false) for a prevote set or when no digest has quorum. The
// committed digest is exactly the state digest validators precommitted -- what got agreed is
// what gets applied (audit A2).
func (s *VoteSet) Commit() (Commit, bool) {
	if s.typ != Precommit {
		return Commit{}, false
	}
	d, ok := s.TwoThirdsMajority()
	if !ok {
		return Commit{}, false
	}
	var votes []Vote
	for _, v := range s.votes {
		if v.BlockDigest == d && !s.equivocators[v.Validator] {
			votes = append(votes, v)
		}
	}
	sort.Slice(votes, func(i, j int) bool { return votes[i].Validator < votes[j].Validator })
	return Commit{Height: s.height, Round: s.round, BlockDigest: d, Votes: votes}, true
}
