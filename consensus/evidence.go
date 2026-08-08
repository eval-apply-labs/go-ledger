package consensus

import "github.com/eval-apply-labs/go-ledger/signing"

// Valid reports whether e is genuine equivocation proof: two validly-signed votes by the same
// validator, for the same (height, round, type), but for DIFFERENT block digests. Only the validator
// itself could have produced both (it signed each), so this pair is UNFORGEABLE proof of a
// double-sign — a proposer cannot fabricate it against an honest validator. That is what makes it
// safe to act on (slash) purely from the evidence carried in a block, without having witnessed the
// round.
func (e Evidence) Valid(scheme signing.Scheme, networkID string) bool {
	if e.A.Validator != e.Validator || e.B.Validator != e.Validator {
		return false
	}
	if e.A.Height != e.B.Height || e.A.Round != e.B.Round || e.A.Type != e.B.Type {
		return false
	}
	if e.A.BlockDigest == e.B.BlockDigest {
		return false // identical, not a conflict
	}
	return e.A.Verify(scheme, networkID) && e.B.Verify(scheme, networkID)
}

// Evidence aggregates the equivocation proofs this driver has gathered across its vote sets, so a
// node can carry them into a block to slash the offenders.
func (d *Driver) Evidence() []Evidence {
	var out []Evidence
	for _, vs := range d.prevotes {
		out = append(out, vs.Evidence()...)
	}
	for _, vs := range d.precommits {
		out = append(out, vs.Evidence()...)
	}
	return out
}
