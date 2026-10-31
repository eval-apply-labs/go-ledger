package consensus

import "github.com/eval-apply-labs/go-ledger/signing"

// Commit is a set of precommit votes for a single block hash at a given height AND ROUND whose
// combined power meets the validator set's quorum. It is the durable, verifiable proof that the
// block was finalised -- and, because it is a set of precommits for a specific block hash, it binds the
// exact block, and through it the state it results in. Any node (or a light client) can check a Commit against the validator set
// without having watched the round.
//
// The Round is load-bearing for safety: equivocation (and therefore the quorum-intersection
// argument) is per-round, so a set mixing precommits from different rounds is NOT a proof that any
// single round decided the block. Verify enforces that every precommit shares commit.Round, so a forged
// mixed-round certificate cannot pass — which matters because the checkpoint checks rely on it (the
// last step of node's verifyCheckpoint, after the anchor's).
type Commit struct {
	Height    int64
	Round     int32
	BlockHash [32]byte
	Votes     []Vote
}

// Verify reports whether commit is a valid quorum of precommits for BlockHash at (Height, Round):
// every vote must be a precommit for this height, round and block hash, and the votes must make a > ⅔
// certificate of the set (CertificateTally): each from a distinct name in the set, signed under the key
// the set holds for that name, with combined power meeting quorum. It returns false on any violation.
func (commit Commit) Verify(scheme signing.Scheme, networkID string, validatorSet *ValidatorSet) bool {
	tally := NewCertificateTally(validatorSet)
	for _, vote := range commit.Votes {
		if vote.Type != Precommit || vote.Height != commit.Height || vote.Round != commit.Round || vote.BlockHash != commit.BlockHash {
			return false
		}
		if !tally.Add(vote.Validator, func(publicKey string) bool { return vote.Verify(scheme, networkID, publicKey) }) {
			return false // a name outside the set, one counted twice, or a signature that does not verify
		}
	}
	return tally.Reached()
}

// CertificateTally adds up the signers of a > ⅔ certificate: a block's Commit, a parameter change or a
// ⅔ name action. It holds the certificate rule, written once: a signer counts when its name is in the
// set and has not counted already, and its signature verifies under the key the set holds for that
// name; the certificate holds when the power of the signers that counted reaches the set's Quorum.
//
// A certificate's own check (Commit.Verify, and ledger's ParameterChange.Valid and Reassignment.Valid)
// stops at the first signer that does not count, so one bad vote makes the whole certificate invalid.
// The wallet, which assembles a certificate from the votes pasted into it, keeps the signers that count
// and leaves the others out.
type CertificateTally struct {
	validatorSet *ValidatorSet
	counted      map[string]bool // the names that counted
	power        int64           // their summed power
}

// NewCertificateTally starts an empty tally of a certificate's signers against validatorSet.
func NewCertificateTally(validatorSet *ValidatorSet) *CertificateTally {
	return &CertificateTally{validatorSet: validatorSet, counted: map[string]bool{}}
}

// Add counts the signer named name, and reports whether it counted: the name is in the set and has not
// counted already, and verifies reports that the signer's signature verifies under publicKey, the key
// the set holds for that name. verifies is called only for a name that can still count, so a name
// outside the set or one repeated costs no signature check.
func (certificateTally *CertificateTally) Add(name string, verifies func(publicKey string) bool) bool {
	validatorSet := certificateTally.validatorSet
	if !validatorSet.Contains(name) || certificateTally.counted[name] || !verifies(validatorSet.PublicKey(name)) {
		return false
	}
	certificateTally.counted[name] = true
	certificateTally.power += validatorSet.Power(name)
	return true
}

// Power is the summed power of the signers that counted.
func (certificateTally *CertificateTally) Power() int64 { return certificateTally.power }

// Reached reports whether the signers that counted hold the set's quorum, more than ⅔ of its power.
func (certificateTally *CertificateTally) Reached() bool {
	return certificateTally.power >= certificateTally.validatorSet.Quorum()
}
