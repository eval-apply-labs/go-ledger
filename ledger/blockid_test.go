package ledger

import (
	"testing"

	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// Deterministic tests for the per-block binding that provides replay protection: a payment is
// included/applied only in the block whose height equals its BlockID.

func TestProposeBlockIncludesOnlyMatchingBlockID(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	prev := []Account{
		{PublicKey: alice.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}
	pay, _ := SignPayment(alice, Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 7,
	})

	// Proposing a different block (5) must not include a payment naming block 7.
	if blk := ProposeBlock(signing.Ed25519{}, "mainnet", 5, st(prev, rat("0.1")), BlockContent{Payments: []Payment{pay}}); len(blk.Payments) != 0 {
		t.Fatalf("payment for block 7 must not be included in block 5, got %d payments", len(blk.Payments))
	}
	// Proposing block 7 includes it.
	if blk := ProposeBlock(signing.Ed25519{}, "mainnet", 7, st(prev, rat("0.1")), BlockContent{Payments: []Payment{pay}}); len(blk.Payments) != 1 {
		t.Fatalf("payment for block 7 must be included in block 7, got %d payments", len(blk.Payments))
	}
}

func TestApplyBlockRejectsMismatchedBlockID(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	prev := []Account{
		{PublicKey: alice.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}
	pay, _ := SignPayment(alice, Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 5,
	})
	next := ProcessPayments(prev, []Payment{pay}, rat("0.1"))
	// A block claiming height 6 but carrying a payment that names block 5 must be rejected.
	blk := Block{
		Height: 6, PrevStateDigest: st(prev, rat("0.1")).Digest(), Payments: []Payment{pay},
		StateDigest: st(next, rat("0.1")).Digest(),
	}
	if _, ok := ApplyBlock(signing.Ed25519{}, "mainnet", st(prev, rat("0.1")), blk); ok {
		t.Fatal("a block at height 6 containing a payment for block 5 must be rejected")
	}
}
