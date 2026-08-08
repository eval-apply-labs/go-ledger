package ledger

import (
	"math/big"
	"testing"
)

// PaymentID must depend only on content, not the signature, so a malleated-signature copy shares an
// identity with the original.
func TestPaymentIDIgnoresSignature(t *testing.T) {
	base := Payment{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: big.NewRat(10, 1), BlockID: 5}
	a, b := base, base
	a.Signature, b.Signature = "sigAAA", "sigBBB" // two valid encodings of the same payment
	if PaymentID(a) != PaymentID(b) {
		t.Fatal("same content must share a PaymentID regardless of signature")
	}
	other := base
	other.Amount = big.NewRat(11, 1)
	if PaymentID(base) == PaymentID(other) {
		t.Fatal("different content must have different PaymentIDs")
	}
}

// The ledger core must debit a payer once even if a block carries the same payment twice under two
// different (malleated) signatures — the defence that no longer depends on the crypto library
// rejecting non-canonical signatures.
func TestMalleatedDuplicateDebitsOnce(t *testing.T) {
	prev := []Account{{PublicKey: "A", Name: "a", Amount: big.NewRat(100, 1), ValidatorNode: "A"}}
	cost := big.NewRat(1, 10)
	base := Payment{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: big.NewRat(10, 1), BlockID: 5}
	p1, p2 := base, base
	p1.Signature, p2.Signature = "sigAAA", "sigBBB"

	out := ProcessPayments(prev, []Payment{p1, p2}, cost)

	var a *big.Rat
	for _, acc := range out {
		if acc.PublicKey == "A" {
			a = acc.Amount
		}
	}
	if a == nil || a.Cmp(big.NewRat(90, 1)) != 0 {
		t.Fatalf("payer should be debited once (100-10=90), got %v", a)
	}
}
