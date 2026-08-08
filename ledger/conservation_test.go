package ledger

import (
	"math/big"
	"testing"
)

func totalSupply(accounts []Account) *big.Rat {
	sum := new(big.Rat)
	for _, account := range accounts {
		if account.Amount != nil {
			sum.Add(sum, account.Amount)
		}
	}
	return sum
}

// The money invariant (the Bitcoin value-overflow lesson, and audit B1/B2): processing payments must
// NEVER increase total supply — value can only move or be burned as fees, never minted. Checked
// across the mint attempts and overspend that a buggy ledger would let through.
func TestSupplyIsNeverMinted(t *testing.T) {
	transactionCost := big.NewRat(1, 10)
	startingAccounts := []Account{
		{PublicKey: "A", Amount: big.NewRat(100, 1), ValidatorNode: "A"},
		{PublicKey: "B", Amount: big.NewRat(50, 1), ValidatorNode: "B"},
	}
	supplyBefore := totalSupply(startingAccounts)

	scenarios := map[string][]Payment{
		"normal":             {{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: big.NewRat(10, 1)}},
		"negative-amount-B1": {{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: big.NewRat(-10, 1)}},
		"phantom-payer-B2":   {{PublicKeyFrom: "ghost", PublicKeyTo: "B", Amount: big.NewRat(10, 1)}},
		"overspend":          {{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: big.NewRat(1000, 1)}},
		"round-trip":         {{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: big.NewRat(10, 1)}, {PublicKeyFrom: "B", PublicKeyTo: "A", Amount: big.NewRat(5, 1)}},
	}
	for scenarioName, payments := range scenarios {
		supplyAfter := totalSupply(ProcessPayments(startingAccounts, payments, transactionCost))
		if supplyAfter.Cmp(supplyBefore) > 0 {
			t.Fatalf("scenario %q MINTED supply: before=%s after=%s", scenarioName, supplyBefore.FloatString(2), supplyAfter.FloatString(2))
		}
	}

	// And exactly: a single applied payment burns exactly one fee (no leak either direction).
	supplyAfterNormal := totalSupply(ProcessPayments(startingAccounts, scenarios["normal"], transactionCost))
	expectedSupply := new(big.Rat).Sub(supplyBefore, transactionCost)
	if supplyAfterNormal.Cmp(expectedSupply) != 0 {
		t.Fatalf("a normal payment should burn exactly the fee: after=%s want=%s",
			supplyAfterNormal.FloatString(2), expectedSupply.FloatString(2))
	}
}
