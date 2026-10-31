package ledger

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// ParameterVote is one validator's signed approval of a proposed parameter change (its signature is
// over the change's shared (NewParameters, PreviousParametersDigest, Sequence)).
type ParameterVote struct {
	Validator string // the validator's name; the validator set holds the key that verifies it
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

// VerifyParameterVote reports whether `vote` is a valid signature by publicKey, the key the caller holds
// for vote.Validator, approving (newParameters, previousParametersDigest, sequence) — used to check votes
// as they are gathered into a certificate.
func VerifyParameterVote(scheme signing.Scheme, networkID, publicKey string, newParameters LedgerParameters, previousParametersDigest [32]byte, sequence int64, vote ParameterVote) bool {
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainParameterVote, vote.Signature, parameterVoteFields(newParameters, previousParametersDigest, sequence)...)
}

// SignParameterVote is the approval, by the validator named validatorName whose key signer holds, of
// `newParameters` replacing the parameters that hash to `previousParametersDigest`, at governance
// `sequence`.
func SignParameterVote(signer *signing.Signer, validatorName string, newParameters LedgerParameters, previousParametersDigest [32]byte, sequence int64) (ParameterVote, error) {
	signature, err := signer.SignBase64(signing.DomainParameterVote, parameterVoteFields(newParameters, previousParametersDigest, sequence)...)
	if err != nil {
		return ParameterVote{}, err
	}
	return ParameterVote{Validator: validatorName, Signature: signature}, nil
}

// Valid reports whether the change is ratified against `currentParameters` at `currentSequence`: it
// amends exactly those parameters (digest match) at exactly this sequence (single-use replay guard),
// the new parameters pass Acceptable for the current validator count (every bound LedgerParameters
// lists, the timing coupling among them), and the votes make a > ⅔ certificate of the current set
// (consensus.CertificateTally): each from a distinct current validator, signing under the key the set
// holds for its name exactly these (NewParameters, PreviousParametersDigest, Sequence), with combined
// power meeting the quorum. One bad vote makes the whole change invalid.
func (parameterChange ParameterChange) Valid(scheme signing.Scheme, networkID string, validators *consensus.ValidatorSet, currentParameters LedgerParameters, currentSequence int64) bool {
	if validators == nil || parameterChange.Sequence != currentSequence || parameterChange.PreviousParametersDigest != currentParameters.Digest() {
		return false
	}
	if !parameterChange.NewParameters.Acceptable(len(validators.Validators())) {
		return false // a parameter outside its bounds, or a step timeout too short for the fan-out
	}
	tally := consensus.NewCertificateTally(validators)
	for _, vote := range parameterChange.Votes {
		if !tally.Add(vote.Validator, func(publicKey string) bool {
			return VerifyParameterVote(scheme, networkID, publicKey, parameterChange.NewParameters, parameterChange.PreviousParametersDigest, parameterChange.Sequence, vote)
		}) {
			return false // a name outside the set, one counted twice, or a signature that does not verify
		}
	}
	return tally.Reached()
}
