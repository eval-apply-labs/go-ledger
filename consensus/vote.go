package consensus

import (
	"strconv"

	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// VoteType distinguishes the two voting phases. Two phases (prevote then precommit) are what
// let honest validators lock onto a value, which is what prevents conflicting commits across
// rounds; a single-phase snapshot cannot.
type VoteType uint8

const (
	Prevote   VoteType = 1
	Precommit VoteType = 2
)

func (t VoteType) String() string {
	switch t {
	case Prevote:
		return "prevote"
	case Precommit:
		return "precommit"
	default:
		return "unknown"
	}
}

// Vote is a validator's signed statement that, at (Height, Round), it votes (Prevote or
// Precommit) for the block with BlockDigest. An all-zero BlockDigest is a "nil" vote.
type Vote struct {
	Height      int64
	Round       int32
	Type        VoteType
	BlockDigest [32]byte
	Validator   string // base64 public key; also the verification key
	Signature   string // base64, domain "block-revision", over the canonical fields
}

func (v Vote) signFields() [][]byte {
	return [][]byte{
		[]byte(strconv.FormatInt(v.Height, 10)),
		[]byte(strconv.FormatInt(int64(v.Round), 10)),
		{byte(v.Type)},
		v.BlockDigest[:],
		[]byte(v.Validator),
	}
}

// SignVote returns a copy of v signed by signer in the block-revision domain, bound to the
// signer's network. (Height, round and type are all part of the signed bytes, so a vote for
// one round can never be replayed as a vote for another.)
func SignVote(signer *signing.Signer, v Vote) (Vote, error) {
	sig, err := signer.SignBase64(signing.DomainBlockRevision, v.signFields()...)
	if err != nil {
		return v, err
	}
	v.Signature = sig
	return v, nil
}

// Verify reports whether v carries a valid signature by its own validator for the network.
func (v Vote) Verify(scheme signing.Scheme, networkID string) bool {
	return signing.VerifyBase64(scheme, networkID, v.Validator, signing.DomainBlockRevision, v.Signature, v.signFields()...)
}
