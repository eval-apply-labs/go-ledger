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
	startingAccounts := []Account{{Name: "a", Amount: big.NewRat(100, 1)}, {Name: "b", Amount: big.NewRat(50, 1)}}
	supplyBefore := totalSupply(startingAccounts)

	scenarios := map[string][]Payment{
		"normal":             {{From: "a", To: "b", Amount: big.NewRat(10, 1)}},
		"negative-amount-B1": {{From: "a", To: "b", Amount: big.NewRat(-10, 1)}},
		"phantom-payer-B2":   {{From: "ghost", To: "b", Amount: big.NewRat(10, 1)}},
		"phantom-payee":      {{From: "a", To: "ghost", Amount: big.NewRat(10, 1)}},
		"overspend":          {{From: "a", To: "b", Amount: big.NewRat(1000, 1)}},
		"round-trip":         {{From: "a", To: "b", Amount: big.NewRat(10, 1)}, {From: "b", To: "a", Amount: big.NewRat(5, 1)}},
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
