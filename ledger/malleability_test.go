package ledger

import (
	"math/big"
	"testing"
)

// PaymentID must depend only on content, not the signature, so a malleated-signature copy shares an
// identity with the original.
func TestPaymentIDIgnoresSignature(t *testing.T) {
	base := Payment{From: "a", To: "b", Amount: big.NewRat(10, 1), BlockID: 5}
	firstCopy, secondCopy := base, base
	firstCopy.Signature, secondCopy.Signature = "sigAAA", "sigBBB" // two valid encodings of the same payment
	if PaymentID(firstCopy) != PaymentID(secondCopy) {
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
	previousAccounts := []Account{{Name: "a", Amount: big.NewRat(100, 1)}, {Name: "b", Amount: new(big.Rat)}}
	cost := big.NewRat(1, 10)
	base := Payment{From: "a", To: "b", Amount: big.NewRat(10, 1), BlockID: 5}
	firstCopy, secondCopy := base, base
	firstCopy.Signature, secondCopy.Signature = "sigAAA", "sigBBB"

	after := ProcessPayments(previousAccounts, []Payment{firstCopy, secondCopy}, cost)

	if payerBalance := balance(after, "a"); payerBalance.Cmp(big.NewRat(90, 1)) != 0 {
		t.Fatalf("payer should be debited once (100-10=90), got %s", payerBalance.FloatString(2))
	}
}
