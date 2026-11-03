package ledger

import (
	"encoding/json"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// The block hash (spec 5.1) is what votes, commits and proposals sign. It covers the whole block: its
// height, the hash of the block before, the hash of the set that signs it, the hash of the set that
// signs the next block, and the sealed digest, which mixes the proposer's nonce, the digest of the
// resulting state and the hash of the content. So a signature over it covers the block's body (E15),
// its signing set and the set that comes next: a commit is its set signing off its successor (Phase 4
// spec §4.3). The random nonce keeps the state digest out of the signature record, which keeps the
// sealed digest instead (spec 6.8).

// BlockHeader is what a checkpoint carries of the last block (spec 4.14): enough to recompute the
// block's hash from the state it resulted in, without the block's content.
type BlockHeader struct {
	Height             int64
	PreviousBlockHash  [32]byte
	ValidatorsHash     [32]byte
	NextValidatorsHash [32]byte
	Nonce              [32]byte
	ContentHash        [32]byte
}

// Header is the block's header.
func (block Block) Header() BlockHeader {
	return BlockHeader{Height: block.Height, PreviousBlockHash: block.PreviousBlockHash, ValidatorsHash: block.ValidatorsHash,
		NextValidatorsHash: block.NextValidatorsHash, Nonce: block.Nonce, ContentHash: ContentHash(block.Content)}
}

// BlockHash is the block's hash: what the validators vote for.
func BlockHash(block Block) [32]byte {
	return block.Header().BlockHash(block.StateDigest)
}

// BlockHash is the hash of the block this is the header of, given the digest of the state that block
// resulted in: what a checkpoint check recomputes.
func (blockHeader BlockHeader) BlockHash(stateDigest [32]byte) [32]byte {
	return BlockHashOf(blockHeader.Height, blockHeader.PreviousBlockHash, blockHeader.ValidatorsHash, blockHeader.NextValidatorsHash,
		SealedDigestOf(blockHeader.Nonce, stateDigest, blockHeader.ContentHash))
}

// ContentHash is the hash of a block's content, its JSON as one field. json.Marshal writes a given value
// the same way every time, and every node runs the same release; a node hashes the content it decoded,
// never the bytes it received.
func ContentHash(content BlockContent) [32]byte {
	serialized, _ := json.Marshal(content) // the content always marshals: an amount writes as text, or null
	return lengthPrefixedDigest([]byte("go-ledger block content v2"), serialized)
}

// SealedDigestOf is the sealed digest: the nonce, the state digest and the content hash, hashed together.
func SealedDigestOf(nonce, stateDigest, contentHash [32]byte) [32]byte {
	return lengthPrefixedDigest([]byte("go-ledger sealed digest v2"), nonce[:], stateDigest[:], contentHash[:])
}

// ValidatorsHash is the hash of a validator set: each member's name, key and power, in the order given.
// A ValidatorSet lists its members in name order, and so does the set a checkpoint carries.
func ValidatorsHash(validators []consensus.Validator) [32]byte {
	fields := [][]byte{[]byte("go-ledger validators v2")}
	for _, validator := range validators {
		fields = append(fields, []byte(validator.Name), []byte(validator.PublicKey), []byte(strconv.FormatInt(validator.Power, 10)))
	}
	return lengthPrefixedDigest(fields...)
}

// BlockHashOf is the block hash of its five parts: the height, the previous block hash, the validators
// hash, the next validators hash and the sealed digest.
func BlockHashOf(height int64, previousBlockHash, validatorsHash, nextValidatorsHash, sealedDigest [32]byte) [32]byte {
	return lengthPrefixedDigest([]byte("go-ledger block hash v2"), []byte(strconv.FormatInt(height, 10)),
		previousBlockHash[:], validatorsHash[:], nextValidatorsHash[:], sealedDigest[:])
}
