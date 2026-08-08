package ledger

import "testing"

func TestStateDigestOrderIndependent(t *testing.T) {
	a := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	b := []Account{ // same accounts, reversed order
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
	}
	if StateDigest(a) != StateDigest(b) {
		t.Fatal("digest must be independent of account order")
	}
}

func TestStateDigestChangesWithBalance(t *testing.T) {
	a := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	c := []Account{
		{PublicKey: "A", Amount: rat("11"), ValidatorNode: "A"}, // one balance changed
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	if StateDigest(a) == StateDigest(c) {
		t.Fatal("digest must change when a balance changes")
	}
}
