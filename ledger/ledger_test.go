package ledger

import (
	"math/big"
	"slices"
	"testing"
)

func rational(literal string) *big.Rat {
	value, valid := new(big.Rat).SetString(literal)
	if !valid {
		panic("bad rational literal: " + literal)
	}
	return value
}

func total(accounts []Account) *big.Rat {
	sum := new(big.Rat)
	for _, account := range accounts {
		sum.Add(sum, account.Amount)
	}
	return sum
}

// balance is the balance of the account named `name`, or zero when there is none.
func balance(accounts []Account, name string) *big.Rat {
	for _, account := range accounts {
		if account.Name == name {
			return account.Amount
		}
	}
	return new(big.Rat)
}

// B1: a negative-amount payment must not mint money to the payer.
func TestNegativeAmountPaymentDoesNotMint(t *testing.T) {
	accounts := []Account{{Name: "a", Amount: rational("10")}, {Name: "b", Amount: rational("5")}}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{{From: "a", To: "a", Amount: rational("-1000000")}}, rational("0.1"))
	if total(after).Cmp(before) > 0 {
		t.Fatalf("supply was minted: before=%s after=%s", before.FloatString(2), total(after).FloatString(2))
	}
}

// B2: a payment from a non-existent account must not mint money to the payee.
func TestPhantomPayerPaymentDoesNotMint(t *testing.T) {
	accounts := []Account{{Name: "b", Amount: rational("5")}}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{{From: "ghost", To: "b", Amount: rational("1000000")}}, rational("0.1"))
	if total(after).Cmp(before) > 0 {
		t.Fatalf("supply was minted: before=%s after=%s", before.FloatString(2), total(after).FloatString(2))
	}
}

// A payment to a name with no account moves nothing and creates no account: names come into being
// only through the genesis (and, later, registration), never by being paid.
func TestPaymentToANameWithNoAccountMovesNothing(t *testing.T) {
	accounts := []Account{{Name: "a", Amount: rational("10")}}
	after := ProcessPayments(accounts, []Payment{{From: "a", To: "nobody", Amount: rational("3")}}, rational("0.1"))
	if len(after) != 1 || balance(after, "a").Cmp(rational("10")) != 0 {
		t.Fatalf("a payment to a name with no account changed the accounts: %v", after)
	}
}

// C3 regression (external review): a NEGATIVE transaction cost must not mint. Unclamped, a negative
// cost credits each payee Amount-cost > Amount (minting |cost| per payment) and lets negative
// amounts past the below-cost filter, re-opening the B1 mint. ProcessPayments clamps it to zero, so
// the behaviour matches the nil-cost case exactly.
func TestNegativeTransactionCostDoesNotMint(t *testing.T) {
	accounts := []Account{{Name: "a", Amount: rational("10")}, {Name: "b", Amount: rational("5")}}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{
		{From: "a", To: "b", Amount: rational("3")},
		{From: "b", To: "a", Amount: rational("-1")}, // must stay excluded
	}, rational("-1"))
	if total(after).Cmp(before) > 0 {
		t.Fatalf("a negative cost minted supply: before=%s after=%s", before.FloatString(2), total(after).FloatString(2))
	}
	if balance(after, "b").Cmp(rational("8")) != 0 {
		t.Errorf("b = %s, want 8 (the full amount, no negative-fee bonus)", balance(after, "b").FloatString(2))
	}
	if balance(after, "a").Cmp(rational("7")) != 0 {
		t.Errorf("a = %s, want 7 (the negative-amount payment must not credit the payer)", balance(after, "a").FloatString(2))
	}
	if total(after).Cmp(before) != 0 {
		t.Errorf("supply changed under a negative cost: %s -> %s", before.FloatString(2), total(after).FloatString(2))
	}
}

// Pins the overdraw ban's full consequence (design decision, findings.md): a payer whose payments
// in ONE block together overdraw is banned, and the ban destroys the payer's ENTIRE balance — the
// account stays at zero (a name is never deleted), the payee receives nothing, supply drops by exactly
// the destroyed balance. This is the most counter-intuitive deliberate behaviour in the ledger; a
// future "fix" relaxing it must be a conscious choice, not an accident. (The two payments differ in
// Comment: identical content would collapse to one and not overdraw.)
func TestOverdrawBanDestroysPayerBalance(t *testing.T) {
	accounts := []Account{{Name: "a", Amount: rational("10")}, {Name: "b", Amount: rational("5")}}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{
		{From: "a", To: "b", Amount: rational("6")},
		{From: "a", To: "b", Amount: rational("6"), Comment: "second"},
	}, rational("0.1"))
	if len(after) != 2 || after[0].Name != "a" || after[0].Amount.Sign() != 0 {
		t.Fatalf("the banned payer must stay, at zero: %v", after)
	}
	if balance(after, "b").Cmp(rational("5")) != 0 {
		t.Errorf("b = %s, want 5 (a banned payer's payments must not pay out)", balance(after, "b").FloatString(2))
	}
	if destroyed := new(big.Rat).Sub(before, total(after)); destroyed.Cmp(rational("10")) != 0 {
		t.Errorf("supply dropped by %s, want exactly the banned payer's balance (10)", destroyed.FloatString(2))
	}
}

// A nil TransactionCost (an unset parameter) must not panic — it is treated as a zero cost, so a
// payment transfers its full amount and negatives are still excluded.
func TestNilTransactionCostTreatedAsZero(t *testing.T) {
	accounts := []Account{{Name: "a", Amount: rational("10")}, {Name: "b", Amount: rational("5")}}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{
		{From: "a", To: "b", Amount: rational("3")},
		{From: "b", To: "a", Amount: rational("-1")}, // still excluded under a zero cost
	}, nil)
	if balance(after, "a").Cmp(rational("7")) != 0 {
		t.Errorf("a = %s, want 7 (no fee burned)", balance(after, "a").FloatString(2))
	}
	if balance(after, "b").Cmp(rational("8")) != 0 {
		t.Errorf("b = %s, want 8 (full amount received)", balance(after, "b").FloatString(2))
	}
	if total(after).Cmp(before) != 0 {
		t.Errorf("supply changed under a nil cost: %s -> %s", before.FloatString(2), total(after).FloatString(2))
	}
}

// A legitimate payment still transfers value (minus the burned transaction cost).
func TestNormalPaymentStillWorks(t *testing.T) {
	accounts := []Account{{Name: "a", Amount: rational("10")}, {Name: "b", Amount: rational("5")}}
	after := ProcessPayments(accounts, []Payment{{From: "a", To: "b", Amount: rational("3")}}, rational("0.1"))
	if balance(after, "a").Cmp(rational("7")) != 0 {
		t.Errorf("a = %s, want 7", balance(after, "a").FloatString(2))
	}
	if balance(after, "b").Cmp(rational("7.9")) != 0 {
		t.Errorf("b = %s, want 7.9", balance(after, "b").FloatString(2))
	}
}

// The ban destroys exactly the payer's pre-block balance and cancels its payments. Credits the payer
// receives in the same block land as usual (they used to restore its whole balance), and the payer
// keeps its account — name, key, delegation, address — at whatever those credits bring.
func TestOverdrawBanSemantics(t *testing.T) {
	overdraw := []Payment{
		{From: "a", To: "b", Amount: rational("6")},
		{From: "a", To: "b", Amount: rational("6"), Comment: "second"},
	}
	for _, testCase := range []struct {
		name    string
		credits []Payment
		want    string // a's balance afterwards
	}{
		{"no credit", nil, "0"},
		{"credited", []Payment{{From: "c", To: "a", Amount: rational("0.3")}}, "0.2"},
		{"credit of exactly the cost", []Payment{{From: "c", To: "a", Amount: rational("0.1")}}, "0"},
		{"credit from another banned payer", []Payment{
			{From: "d", To: "a", Amount: rational("0.9")},
			{From: "d", To: "a", Amount: rational("0.9"), Comment: "second"},
		}, "0"},
	} {
		payer := Account{Name: "a", PublicKey: "key-a", Amount: rational("10"), Delegate: "v1", PublicAddress: "http://a", IPAddresses: []string{"10.0.0.1"}}
		accounts := []Account{payer, {Name: "b", Amount: rational("5")}, {Name: "c", Amount: rational("5")}, {Name: "d", Amount: rational("1")}}
		after := ProcessPayments(accounts, append(append([]Payment(nil), overdraw...), testCase.credits...), rational("0.1"))
		if balance(after, "b").Cmp(rational("5")) != 0 {
			t.Errorf("%s: b = %s, want 5 (a banned payer's payments must not pay out)", testCase.name, balance(after, "b").FloatString(2))
		}
		payerAfter := after[0]
		switch {
		case payerAfter.Amount.Cmp(rational(testCase.want)) != 0:
			t.Errorf("%s: a = %s, want %s", testCase.name, payerAfter.Amount.FloatString(2), testCase.want)
		case payerAfter.Name != "a" || payerAfter.PublicKey != "key-a" || payerAfter.Delegate != "v1" || payerAfter.PublicAddress != "http://a" ||
			!slices.Equal(payerAfter.IPAddresses, []string{"10.0.0.1"}):
			t.Errorf("%s: a lost its attributes: %+v", testCase.name, payerAfter)
		}
	}
}
