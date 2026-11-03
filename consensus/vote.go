package consensus

import (
	"fmt"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// VoteType distinguishes the two voting phases. Two phases (prevote then precommit) are what
// let honest validators lock onto a value, which is what prevents conflicting commits across
// rounds; a single-phase snapshot cannot.
type VoteType uint8 // implements fmt.Stringer

const (
	Prevote   VoteType = 1
	Precommit VoteType = 2
)

var _ fmt.Stringer = VoteType(0) // the compiler checks that VoteType implements fmt.Stringer

// String names the vote type. fmt calls it to print a VoteType with %s or %v.
func (voteType VoteType) String() string {
	switch voteType {
	case Prevote:
		return "prevote"
	case Precommit:
		return "precommit"
	default:
		return "unknown"
	}
}

// Vote is a validator's signed statement that, at (Height, Round), it votes (Prevote or
// Precommit) for the block with BlockHash. An all-zero BlockHash is a "nil" vote.
type Vote struct {
	Height    int64
	Round     int32
	Type      VoteType
	BlockHash [32]byte
	Validator string // the signer's name; the validator set holds the key that verifies it
	Signature string // base64, domain "block-revision", over the canonical fields
}

func (vote Vote) signFields() [][]byte {
	return [][]byte{
		[]byte(strconv.FormatInt(vote.Height, 10)),
		[]byte(strconv.FormatInt(int64(vote.Round), 10)),
		{byte(vote.Type)},
		vote.BlockHash[:],
		[]byte(vote.Validator),
	}
}

// SignVote returns a copy of vote signed by signer in the block-revision domain, bound to the
// signer's network. (Height, round and type are all part of the signed bytes, so a vote for
// one round can never be replayed as a vote for another.)
func SignVote(signer signing.Signer, vote Vote) (Vote, error) {
	signature, err := signer.SignBase64(signing.DomainBlockRevision, vote.signFields()...)
	if err != nil {
		return vote, err
	}
	vote.Signature = signature
	return vote, nil
}

// Verify reports whether vote carries a valid signature by publicKey for the network. The caller passes
// the key its validator set holds for vote.Validator: a vote names its signer and carries no key.
func (vote Vote) Verify(scheme signing.Scheme, networkID, publicKey string) bool {
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainBlockRevision, vote.Signature, vote.signFields()...)
}
