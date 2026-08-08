package ledger

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// ParameterVote is one validator's signed approval of a proposed parameter change (its signature is
// over the change's shared (NewParameters, PreviousParametersDigest, Sequence)).
type ParameterVote struct {
	Validator string // the validator's public key
	Signature string // over the (NewParameters, PreviousParametersDigest, Sequence) preimage, domain "parameter-vote"
}

// ParameterChange is a proposed amendment to the ledger parameters, ratified by a > ⅔ stake quorum
// of the CURRENT validators — the same threshold as block finality, so a minority (≤ ⅓) can neither
// force a change nor ratify two conflicting ones. It is bound to the parameters it amends
// (PreviousParametersDigest) AND to the committed governance Sequence: it applies only while both
// still match, and applying it bumps the sequence, so a ratified certificate is single-use and cannot
// be replayed after the parameters cycle back to a previous value. The bundle of signatures IS the
// proof (like a Commit certificate): any node verifies and applies it the same way.
type ParameterChange struct {
	NewParameters            LedgerParameters
	PreviousParametersDigest [32]byte
	Sequence                 int64 // the governance sequence this change amends (single-use guard)
	Votes                    []ParameterVote
}

func parameterVoteFields(newParameters LedgerParameters, previousParametersDigest [32]byte, sequence int64) [][]byte {
	return append(newParameters.fields(),
		previousParametersDigest[:],
		[]byte(strconv.FormatInt(sequence, 10)))
}

// firstValidatorSet returns the first set in a variadic argument, or nil — so ProposeBlock/ApplyBlock
// can take the validator set optionally (only a block carrying a ParameterChange needs it).
func firstValidatorSet(sets []*consensus.ValidatorSet) *consensus.ValidatorSet {
	if len(sets) > 0 {
		return sets[0]
	}
	return nil
}

// VerifyParameterVote reports whether `vote` is a valid signature by its validator approving
// (newParameters, previousParametersDigest, sequence) — used to filter votes as they are gossiped.
func VerifyParameterVote(scheme signing.Scheme, networkID string, newParameters LedgerParameters, previousParametersDigest [32]byte, sequence int64, vote ParameterVote) bool {
	return signing.VerifyBase64(scheme, networkID, vote.Validator, signing.DomainParameterVote, vote.Signature, parameterVoteFields(newParameters, previousParametersDigest, sequence)...)
}

// SignParameterVote is a validator's approval of `newParameters` replacing the parameters that hash
// to `previousParametersDigest`, at governance `sequence`.
func SignParameterVote(signer *signing.Signer, newParameters LedgerParameters, previousParametersDigest [32]byte, sequence int64) (ParameterVote, error) {
	signature, err := signer.SignBase64(signing.DomainParameterVote, parameterVoteFields(newParameters, previousParametersDigest, sequence)...)
	if err != nil {
		return ParameterVote{}, err
	}
	return ParameterVote{Validator: signer.PublicKeyBase64(), Signature: signature}, nil
}

// Valid reports whether the change is ratified against `currentParameters` at `currentSequence`: it
// amends exactly those parameters (digest match) at exactly this sequence (single-use replay guard),
// the new parameters are a well-formed operating profile (non-negative cost, and a step timeout long
// enough for the gossip fan-out at the current validator count — the timing coupling), and every vote
// is a distinct current validator signing exactly these (NewParameters, PreviousParametersDigest,
// Sequence), with combined power meeting the > ⅔ quorum.
func (change ParameterChange) Valid(scheme signing.Scheme, networkID string, validators *consensus.ValidatorSet, currentParameters LedgerParameters, currentSequence int64) bool {
	if validators == nil || change.Sequence != currentSequence || change.PreviousParametersDigest != currentParameters.Digest() {
		return false
	}
	if !change.NewParameters.Acceptable(len(validators.Validators())) {
		return false // an ill-formed profile: negative cost, or a window too short for the fan-out
	}
	fields := parameterVoteFields(change.NewParameters, change.PreviousParametersDigest, change.Sequence)
	seen := make(map[string]bool)
	var approvingPower int64
	for _, vote := range change.Votes {
		if !validators.Contains(vote.Validator) || seen[vote.Validator] {
			return false // unknown validator, or one counted twice
		}
		if !signing.VerifyBase64(scheme, networkID, vote.Validator, signing.DomainParameterVote, vote.Signature, fields...) {
			return false
		}
		seen[vote.Validator] = true
		approvingPower += validators.Power(vote.Validator)
	}
	return approvingPower >= validators.Quorum()
}
