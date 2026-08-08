package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestProposeAndApplyBlockRoundTrip(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	prev := []Account{
		{PublicKey: alice.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}
	pay, _ := SignPayment(alice, Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 1,
	})

	blk := ProposeBlock(signing.Ed25519{}, "mainnet", 1, st(prev, rat("0.1")), BlockContent{Payments: []Payment{pay}})
	next, ok := ApplyBlock(signing.Ed25519{}, "mainnet", st(prev, rat("0.1")), blk)
	if !ok {
		t.Fatal("a well-formed block failed to apply")
	}
	if next.Digest() != blk.StateDigest {
		t.Fatal("applied state digest mismatch")
	}
	if balance(next.Accounts, alice.PublicKeyBase64()).Cmp(rat("90")) != 0 {
		t.Errorf("alice = %s, want 90", balance(next.Accounts, alice.PublicKeyBase64()).FloatString(2))
	}
	if balance(next.Accounts, "bob").Cmp(rat("9.9")) != 0 {
		t.Errorf("bob = %s, want 9.9", balance(next.Accounts, "bob").FloatString(2))
	}
}

func TestApplyBlockRejectsTamperedDigest(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	prev := []Account{
		{PublicKey: alice.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}
	pay, _ := SignPayment(alice, Payment{PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 1})
	blk := ProposeBlock(signing.Ed25519{}, "mainnet", 1, st(prev, rat("0.1")), BlockContent{Payments: []Payment{pay}})
	blk.StateDigest[0] ^= 0xFF // tamper with the claimed result
	if _, ok := ApplyBlock(signing.Ed25519{}, "mainnet", st(prev, rat("0.1")), blk); ok {
		t.Fatal("a block with a tampered state digest must not apply")
	}
}

func TestApplyBlockRejectsUnsignedPayment(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	prev := []Account{
		{PublicKey: alice.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}
	unsigned := Payment{PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 1}
	// hand-build a block whose digest is self-consistent but whose payment is unsigned
	blk := Block{
		Height:          1,
		PrevStateDigest: st(prev, rat("0.1")).Digest(),
		Payments:        []Payment{unsigned},
		StateDigest:     st(ProcessPayments(prev, []Payment{unsigned}, rat("0.1")), rat("0.1")).Digest(),
	}
	if _, ok := ApplyBlock(signing.Ed25519{}, "mainnet", st(prev, rat("0.1")), blk); ok {
		t.Fatal("a block containing an unsigned payment must not apply")
	}
}

func TestApplyBlockRejectsWrongPrev(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	prev := []Account{{PublicKey: alice.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: "v"}}
	pay, _ := SignPayment(alice, Payment{PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 1})
	blk := ProposeBlock(signing.Ed25519{}, "mainnet", 1, st(prev, rat("0.1")), BlockContent{Payments: []Payment{pay}})
	other := []Account{{PublicKey: alice.PublicKeyBase64(), Amount: rat("999"), ValidatorNode: "v"}}
	if _, ok := ApplyBlock(signing.Ed25519{}, "mainnet", st(other, rat("0.1")), blk); ok {
		t.Fatal("a block must not apply onto a different prior state")
	}
}
