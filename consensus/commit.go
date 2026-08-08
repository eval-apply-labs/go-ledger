package consensus

import "github.com/eval-apply-labs/go-ledger/signing"

// Commit is a set of precommit votes for a single block digest at a given height AND ROUND whose
// combined power meets the validator set's quorum. It is the durable, verifiable proof that the
// block was finalised -- and, because it is a set of precommits for a specific digest, it binds the
// exact applied state. Any node (or a light client) can check a Commit against the validator set
// without having watched the round.
//
// The Round is load-bearing for safety: equivocation (and therefore the quorum-intersection
// argument) is per-round, so a set mixing precommits from different rounds is NOT a proof that any
// single round decided the block. Verify enforces that every precommit shares c.Round, so a forged
// mixed-round certificate cannot pass — which matters because Commit.Verify is the trust anchor for
// onboarding/checkpoint sync.
type Commit struct {
	Height      int64
	Round       int32
	BlockDigest [32]byte
	Votes       []Vote
}

// Verify reports whether c is a valid quorum of precommits for BlockDigest at (Height, Round): every
// vote must be a precommit for this height, round and digest, be signed by a distinct validator that
// is in the set, and their combined power must meet quorum. It returns false on any violation.
func (c Commit) Verify(scheme signing.Scheme, networkID string, vals *ValidatorSet) bool {
	seen := make(map[string]bool, len(c.Votes))
	var power int64
	for _, v := range c.Votes {
		if v.Type != Precommit || v.Height != c.Height || v.Round != c.Round || v.BlockDigest != c.BlockDigest {
			return false
		}
		if !vals.Contains(v.Validator) || seen[v.Validator] {
			return false // unknown validator, or the same one counted twice
		}
		if !v.Verify(scheme, networkID) {
			return false
		}
		seen[v.Validator] = true
		power += vals.Power(v.Validator)
	}
	return power >= vals.Quorum()
}

// Voters returns the set of validators whose precommits are in the commit.
func (c Commit) Voters() []string {
	out := make([]string, 0, len(c.Votes))
	for _, v := range c.Votes {
		out = append(out, v.Validator)
	}
	return out
}
