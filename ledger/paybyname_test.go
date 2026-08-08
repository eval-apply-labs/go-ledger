package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// Paying by name resolves against the committed registry inside the ledger rules — on every node
// independently — and never through the wallet's node. Unregistered names and ambiguous recipients
// are deterministic no-ops.
func TestPayByNameResolvesAgainstCommittedRegistry(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Name: "bob", Amount: rat("5"), ValidatorNode: "B"},
	}
	cost := rat("0.1")

	after := ProcessPayments(accounts, []Payment{{PublicKeyFrom: "A", ToName: "bob", Amount: rat("3")}}, cost)
	if balance(after, "A").Cmp(rat("7")) != 0 || balance(after, "B").Cmp(rat("7.9")) != 0 {
		t.Fatalf("pay-by-name should debit A and credit bob's account: A=%s B=%s",
			balance(after, "A").FloatString(2), balance(after, "B").FloatString(2))
	}

	// An unregistered name is a no-op: nothing debited, nothing created.
	after = ProcessPayments(accounts, []Payment{{PublicKeyFrom: "A", ToName: "nobody", Amount: rat("3")}}, cost)
	if balance(after, "A").Cmp(rat("10")) != 0 || len(after) != len(accounts) {
		t.Fatal("a payment to an unregistered name must be a deterministic no-op")
	}

	// An ambiguous recipient (both forms set) is excluded outright.
	after = ProcessPayments(accounts, []Payment{{PublicKeyFrom: "A", PublicKeyTo: "B", ToName: "bob", Amount: rat("3")}}, cost)
	if balance(after, "A").Cmp(rat("10")) != 0 {
		t.Fatal("a payment naming both a key and a name must be excluded")
	}

	// No recipient at all is excluded too (previously a pay-to-"" would synthesise an empty-key account).
	after = ProcessPayments(accounts, []Payment{{PublicKeyFrom: "A", Amount: rat("3")}}, cost)
	if balance(after, "A").Cmp(rat("10")) != 0 || has(after, "") {
		t.Fatal("a payment with no recipient must be excluded")
	}
}

// A signature-valid payment to an unregistered name must never be carried into a committed block:
// ProcessPayments would drop it before the fee, so if the block layer let it through it would be a
// permanent, zero-fee no-op occupying block space and a per-node signature verification. ProposeBlock
// must exclude it, and ApplyBlock must reject a block a dishonest proposer padded with one.
func TestUnresolvablePaymentNeverEntersABlock(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payer, _ := signing.Generate(scheme, network)
	A := payer.PublicKeyBase64()
	prev := []Account{{PublicKey: A, Amount: big.NewRat(10, 1), ValidatorNode: A}}
	cost := big.NewRat(1, 10)
	const height = 4

	spam, err := SignPayment(payer, Payment{PublicKeyFrom: A, ToName: "nobody", Amount: big.NewRat(3, 1), BlockID: height})
	if err != nil {
		t.Fatal(err)
	}

	// ProposeBlock excludes it: an honest proposer builds an empty block.
	blk := ProposeBlock(scheme, network, height, st(prev, cost), BlockContent{Payments: []Payment{spam}})
	if len(blk.Payments) != 0 {
		t.Fatalf("ProposeBlock must exclude an unresolvable-name payment, carried %d", len(blk.Payments))
	}

	// A dishonest proposer force-includes it (recomputing the digest so the block is otherwise
	// self-consistent). ApplyBlock must still reject the whole block.
	forged := ProposeBlock(scheme, network, height, st(prev, cost), BlockContent{})
	forged.Payments = []Payment{spam}
	if recomputed, ok := ApplyBlock(scheme, network, st(prev, cost), forged); ok {
		t.Fatalf("ApplyBlock must reject a block padded with an unresolvable-name payment; accepted with %d accounts", len(recomputed.Accounts))
	}
}

// The recipient name is part of the signed content: retargeting a signed payment to a different
// name (or swapping name for key form) must invalidate the signature.
func TestPaymentSignatureCoversToName(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payer, _ := signing.Generate(scheme, network)
	p, err := SignPayment(payer, Payment{PublicKeyFrom: payer.PublicKeyBase64(), ToName: "bob", Amount: rat("1"), BlockID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPayment(scheme, network, p) {
		t.Fatal("the signed pay-by-name payment should verify")
	}
	retargeted := p
	retargeted.ToName = "mallory"
	if VerifyPayment(scheme, network, retargeted) {
		t.Fatal("retargeting the name must break the signature")
	}
	swapped := p
	swapped.ToName = ""
	swapped.PublicKeyTo = payer.PublicKeyBase64()
	if VerifyPayment(scheme, network, swapped) {
		t.Fatal("swapping the name for a key recipient must break the signature")
	}
}

// A name moving in the very block that pays it: resolution uses the PRE-block registry (updates
// apply after payments), so every node deterministically pays the owner the payer could see.
func TestPayByNameUsesPreBlockOwnerWhenNameMovesInSameBlock(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payer, _ := signing.Generate(scheme, network)
	bob, _ := signing.Generate(scheme, network)
	A, B := payer.PublicKeyBase64(), bob.PublicKeyBase64()
	prev := []Account{
		{PublicKey: A, Amount: big.NewRat(10, 1), ValidatorNode: A},
		{PublicKey: B, Name: "bob", Amount: big.NewRat(5, 1), ValidatorNode: B},
	}
	cost := big.NewRat(1, 10)
	const height = 7

	pay, err := SignPayment(payer, Payment{PublicKeyFrom: A, ToName: "bob", Amount: big.NewRat(3, 1), BlockID: height})
	if err != nil {
		t.Fatal(err)
	}
	rename, err := SignAccountUpdate(bob, AccountUpdate{From: B, Name: "robert", BlockID: height})
	if err != nil {
		t.Fatal(err)
	}

	blk := ProposeBlock(scheme, network, height, st(prev, cost), BlockContent{
		Payments: []Payment{pay},
		Updates:  []AccountUpdate{rename},
	})
	next, ok := ApplyBlock(scheme, network, st(prev, cost), blk)
	if !ok {
		t.Fatal("the block should apply")
	}
	if balance(next.Accounts, B).Cmp(big.NewRat(79, 10)) != 0 {
		t.Fatalf("the payment must reach the pre-block owner of 'bob': got %s", balance(next.Accounts, B).FloatString(2))
	}
	for _, a := range next.Accounts {
		if a.PublicKey == B && a.Name != "robert" {
			t.Fatalf("the rename should still land: got name %q", a.Name)
		}
	}
}
