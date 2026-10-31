package ledger

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// The block hash and its parts are SHA-256 over lp() fields (spec 5.1), built here by hand: the content
// hash over "go-ledger block content v2" and the content's JSON; the sealed digest over "go-ledger
// sealed digest v2", the nonce, the state digest and the content hash; the validators hash over
// "go-ledger validators v2" and each member's name, key and power; and the block hash over "go-ledger
// block hash v2", the height in decimal, the previous block hash, the validators hash and the sealed
// digest. Digests and nonces are 32 raw bytes. A header holds what recomputes the hash from the state
// digest.
func TestBlockHashLayout(t *testing.T) {
	validators := []consensus.Validator{{Name: "bank-a", PublicKey: "key-a", Power: 1}, {Name: "bank-b", PublicKey: "key-b", Power: 12}}
	block := Block{Height: 42, PreviousBlockHash: [32]byte{1}, ValidatorsHash: ValidatorsHash(validators), Nonce: [32]byte{2},
		Content:     BlockContent{Payments: []Payment{{From: "alice", To: "bakery", Amount: rational("12.5"), BlockID: 42}}},
		StateDigest: [32]byte{3}}
	contentJSON, err := json.Marshal(block.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentHash := sha256.Sum256(lengthPrefixedPreimage("go-ledger block content v2", string(contentJSON)))
	sealedDigest := sha256.Sum256(lengthPrefixedPreimage("go-ledger sealed digest v2", string(block.Nonce[:]), string(block.StateDigest[:]), string(contentHash[:])))
	validatorsHash := sha256.Sum256(lengthPrefixedPreimage("go-ledger validators v2", "bank-a", "key-a", "1", "bank-b", "key-b", "12"))
	blockHash := sha256.Sum256(lengthPrefixedPreimage("go-ledger block hash v2", "42", string(block.PreviousBlockHash[:]), string(validatorsHash[:]), string(sealedDigest[:])))

	switch {
	case ContentHash(block.Content) != contentHash:
		t.Error("the content hash does not follow the spec 5.1 layout")
	case SealedDigestOf(block.Nonce, block.StateDigest, contentHash) != sealedDigest:
		t.Error("the sealed digest does not follow the spec 5.1 layout")
	case ValidatorsHash(validators) != validatorsHash:
		t.Error("the validators hash does not follow the spec 5.1 layout")
	case BlockHashOf(42, block.PreviousBlockHash, validatorsHash, sealedDigest) != blockHash || BlockHash(block) != blockHash:
		t.Error("the block hash does not follow the spec 5.1 layout")
	case block.Header() != BlockHeader{Height: 42, PreviousBlockHash: block.PreviousBlockHash, ValidatorsHash: validatorsHash, Nonce: block.Nonce, ContentHash: contentHash}:
		t.Errorf("the header is %+v", block.Header())
	case block.Header().BlockHash(block.StateDigest) != blockHash:
		t.Error("the header and the state digest do not recompute the block hash")
	}
}

// The block hash covers every part of a block (spec 5.1): its height, the previous block hash, the
// signing set (each member's name, key and power), the nonce, the state digest and each field of the
// content. So a vote, commit or proposal over it covers them all: a block that differs in any one is
// another block, and two blocks that differ only in their nonce are two blocks.
func TestBlockHashCoversEveryPart(t *testing.T) {
	validators := []consensus.Validator{{Name: "bank-a", PublicKey: "key-a", Power: 1}}
	block := Block{Height: 5, PreviousBlockHash: [32]byte{1}, ValidatorsHash: ValidatorsHash(validators), Nonce: [32]byte{2},
		Content: BlockContent{Payments: []Payment{{From: "alice", To: "bob", Amount: rational("1"), BlockID: 5}}}, StateDigest: [32]byte{3}}
	otherSet := func(change func(*consensus.Validator)) func(*Block) {
		return func(changed *Block) {
			validator := validators[0]
			change(&validator)
			changed.ValidatorsHash = ValidatorsHash([]consensus.Validator{validator})
		}
	}
	for part, change := range map[string]func(*Block){
		"the height":              func(changed *Block) { changed.Height++ },
		"the previous block hash": func(changed *Block) { changed.PreviousBlockHash[0]++ },
		"a signer's name":         otherSet(func(validator *consensus.Validator) { validator.Name = "bank-b" }),
		"a signer's key":          otherSet(func(validator *consensus.Validator) { validator.PublicKey = "key-b" }),
		"a signer's power":        otherSet(func(validator *consensus.Validator) { validator.Power = 2 }),
		"another signer":          func(changed *Block) { changed.ValidatorsHash = ValidatorsHash(append(validators, validators[0])) },
		"the nonce":               func(changed *Block) { changed.Nonce[0]++ },
		"the state digest":        func(changed *Block) { changed.StateDigest[0]++ },
		"a payment": func(changed *Block) {
			changed.Content.Payments = []Payment{{From: "alice", To: "bob", Amount: rational("2"), BlockID: 5}}
		},
		"the account updates": func(changed *Block) {
			changed.Content.AccountUpdates = []AccountUpdate{{Name: "alice", Description: "x", BlockID: 5}}
		},
		"the vote evidence":     func(changed *Block) { changed.Content.VoteEvidence = []consensus.VoteEvidence{{Name: "cheat"}} },
		"the proposal evidence": func(changed *Block) { changed.Content.ProposalEvidence = []consensus.ProposalEvidence{{Name: "cheat"}} },
		"the parameter change": func(changed *Block) {
			changed.Content.ParameterChange = &ParameterChange{NewParameters: DefaultParameters()}
		},
		"the reassignment":          func(changed *Block) { changed.Content.Reassignment = &Reassignment{Name: "alice"} },
		"the payments, all of them": func(changed *Block) { changed.Content.Payments = nil },
	} {
		changed := block
		change(&changed)
		if BlockHash(changed) == BlockHash(block) {
			t.Errorf("a block that differs in %s has the same hash", part)
		}
	}
}
