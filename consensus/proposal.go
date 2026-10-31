package consensus

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// SignedProposal is what a proposer signs: one block, by its hash, for one height and round (spec
// 4.11). The block hash covers the whole block, so the signature covers the block's body too, and the
// proposer's name: nobody can attach another body to it, or sign it under another name (E15).
type SignedProposal struct {
	Height     int64
	Round      int32
	ValidRound int32    // the round the block was locked in, or -1 for a fresh proposal
	BlockHash  [32]byte // the hash of the proposed block (ledger.BlockHash)
	Proposer   string   // the proposer's name; the validator set holds the key that verifies it
	Signature  string   // base64, domain "proposal", over the fields above
}

func (signedProposal SignedProposal) signFields() [][]byte {
	return [][]byte{
		[]byte(strconv.FormatInt(signedProposal.Height, 10)),
		[]byte(strconv.FormatInt(int64(signedProposal.Round), 10)),
		[]byte(strconv.FormatInt(int64(signedProposal.ValidRound), 10)),
		signedProposal.BlockHash[:],
		[]byte(signedProposal.Proposer),
	}
}

// SignProposal returns a copy of proposal signed by signer in the proposal domain, bound to the
// signer's network.
func SignProposal(signer *signing.Signer, proposal SignedProposal) (SignedProposal, error) {
	signature, err := signer.SignBase64(signing.DomainProposal, proposal.signFields()...)
	if err != nil {
		return proposal, err
	}
	proposal.Signature = signature
	return proposal, nil
}

// Verify reports whether signedProposal carries a valid signature by publicKey for the network. The
// caller passes the key its validator set holds for signedProposal.Proposer, and checks that the name is
// the round's proposer.
func (signedProposal SignedProposal) Verify(scheme signing.Scheme, networkID, publicKey string) bool {
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainProposal, signedProposal.Signature, signedProposal.signFields()...)
}
