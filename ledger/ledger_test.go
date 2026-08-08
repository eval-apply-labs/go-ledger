package ledger

import (
	"math/big"
	"testing"
)

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad rational literal: " + s)
	}
	return r
}

func total(accts []Account) *big.Rat {
	sum := new(big.Rat)
	for _, a := range accts {
		sum.Add(sum, a.Amount)
	}
	return sum
}

func balance(accts []Account, pk string) *big.Rat {
	for _, a := range accts {
		if a.PublicKey == pk {
			return a.Amount
		}
	}
	return new(big.Rat)
}

// B1: a negative-amount payment must not mint money to the payer.
func TestNegativeAmountPaymentDoesNotMint(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	before := total(accounts)
	after := ProcessPayments(accounts,
		[]Payment{{PublicKeyFrom: "A", PublicKeyTo: "A", Amount: rat("-1000000")}}, rat("0.1"))
	if total(after).Cmp(before) > 0 {
		t.Fatalf("supply was minted: before=%s after=%s", before.FloatString(2), total(after).FloatString(2))
	}
}

// B2: a payment from a non-existent account must not mint money to the payee.
func TestPhantomPayerPaymentDoesNotMint(t *testing.T) {
	accounts := []Account{{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"}}
	before := total(accounts)
	after := ProcessPayments(accounts,
		[]Payment{{PublicKeyFrom: "GHOST", PublicKeyTo: "B", Amount: rat("1000000")}}, rat("0.1"))
	if total(after).Cmp(before) > 0 {
		t.Fatalf("supply was minted: before=%s after=%s", before.FloatString(2), total(after).FloatString(2))
	}
}

// C3 regression (external review): a NEGATIVE transaction cost must not mint. Unclamped, a negative
// cost credits each payee Amount-cost > Amount (minting |cost| per payment) and lets negative
// amounts past the below-cost filter, re-opening the B1 mint. ProcessPayments clamps it to zero, so
// the behaviour matches the nil-cost case exactly.
func TestNegativeTransactionCostDoesNotMint(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("3")},
		{PublicKeyFrom: "B", PublicKeyTo: "A", Amount: rat("-1")}, // must stay excluded
	}, rat("-1"))
	if total(after).Cmp(before) > 0 {
		t.Fatalf("a negative cost minted supply: before=%s after=%s", before.FloatString(2), total(after).FloatString(2))
	}
	if balance(after, "B").Cmp(rat("8")) != 0 {
		t.Errorf("B = %s, want 8 (the full amount, no negative-fee bonus)", balance(after, "B").FloatString(2))
	}
	if balance(after, "A").Cmp(rat("7")) != 0 {
		t.Errorf("A = %s, want 7 (the negative-amount payment must not credit the payer)", balance(after, "A").FloatString(2))
	}
	if total(after).Cmp(before) != 0 {
		t.Errorf("supply changed under a negative cost: %s -> %s", before.FloatString(2), total(after).FloatString(2))
	}
}

// Pins the overdraw ban's full consequence (design decision, findings.md): a payer whose payments
// in ONE block together overdraw is banned, and the ban destroys the payer's ENTIRE balance — the
// account vanishes, the payee receives nothing, supply drops by exactly the destroyed balance. This
// is the most counter-intuitive deliberate behaviour in the ledger; a future "fix" relaxing it must
// be a conscious choice, not an accident. (The two payments differ in Comment: identical content
// would collapse to one and not overdraw.)
func TestOverdrawBanDestroysPayerBalance(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6")},
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6"), Comment: "second"},
	}, rat("0.1"))
	for _, a := range after {
		if a.PublicKey == "A" {
			t.Fatalf("banned payer still present with balance %s — the ban must drop the account entirely", a.Amount.FloatString(2))
		}
	}
	if balance(after, "B").Cmp(rat("5")) != 0 {
		t.Errorf("B = %s, want 5 (a banned payer's payments must not pay out)", balance(after, "B").FloatString(2))
	}
	if destroyed := new(big.Rat).Sub(before, total(after)); destroyed.Cmp(rat("10")) != 0 {
		t.Errorf("supply dropped by %s, want exactly the banned payer's balance (10)", destroyed.FloatString(2))
	}
}

// A nil TransactionCost (an unset parameter) must not panic — it is treated as a zero cost, so a
// payment transfers its full amount and negatives are still excluded.
func TestNilTransactionCostTreatedAsZero(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("3")},
		{PublicKeyFrom: "B", PublicKeyTo: "A", Amount: rat("-1")}, // still excluded under a zero cost
	}, nil)
	if balance(after, "A").Cmp(rat("7")) != 0 {
		t.Errorf("A = %s, want 7 (no fee burned)", balance(after, "A").FloatString(2))
	}
	if balance(after, "B").Cmp(rat("8")) != 0 {
		t.Errorf("B = %s, want 8 (full amount received)", balance(after, "B").FloatString(2))
	}
	if total(after).Cmp(before) != 0 {
		t.Errorf("supply changed under a nil cost: %s -> %s", before.FloatString(2), total(after).FloatString(2))
	}
}

// A legitimate payment still transfers value (minus the burned transaction cost).
func TestNormalPaymentStillWorks(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}
	after := ProcessPayments(accounts,
		[]Payment{{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("3")}}, rat("0.1"))
	if balance(after, "A").Cmp(rat("7")) != 0 {
		t.Errorf("A = %s, want 7", balance(after, "A").FloatString(2))
	}
	if balance(after, "B").Cmp(rat("7.9")) != 0 {
		t.Errorf("B = %s, want 7.9", balance(after, "B").FloatString(2))
	}
}
