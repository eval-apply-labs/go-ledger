package ledger

import "testing"

// C05 semantics table on the CURRENT code (no assertions — logs exactly what happens).
func TestVerifyC05BanSemanticsTable(t *testing.T) {
	cost := rat("0.1")
	base := func() []Account {
		return []Account{
			{PublicKey: "A", Name: "alice", Amount: rat("10"), ValidatorNode: "V1", Address: "http://a"},
			{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
			{PublicKey: "C", Amount: rat("5"), ValidatorNode: "C"},
			{PublicKey: "D", Amount: rat("1"), ValidatorNode: "D"},
		}
	}
	overdraw := []Payment{
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6")},
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6"), Comment: "2"},
	}
	cases := []struct {
		name  string
		extra []Payment
	}{
		{"no credit", nil},
		{"credit 0.2 by key from C", []Payment{{PublicKeyFrom: "C", PublicKeyTo: "A", Amount: rat("0.2")}}},
		{"credit == cost (0.1) from C", []Payment{{PublicKeyFrom: "C", PublicKeyTo: "A", Amount: rat("0.1")}}},
		{"credit 0.2 BY NAME from C", []Payment{{PublicKeyFrom: "C", ToName: "alice", Amount: rat("0.2")}}},
		{"credit from another BANNED payer D", []Payment{
			{PublicKeyFrom: "D", PublicKeyTo: "A", Amount: rat("0.9")}, {PublicKeyFrom: "D", PublicKeyTo: "A", Amount: rat("0.9"), Comment: "x"}}},
		{"single over-balance payment + credit", nil},
	}
	for i, c := range cases {
		pays := append(append([]Payment(nil), overdraw...), c.extra...)
		if i == len(cases)-1 {
			pays = []Payment{{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("11")}, {PublicKeyFrom: "C", PublicKeyTo: "A", Amount: rat("1")}}
		}
		before := total(base())
		after := ProcessPayments(base(), pays, cost)
		a, present := Account{}, false
		for _, x := range after {
			if x.PublicKey == "A" {
				a, present = x, true
			}
		}
		amt := "-"
		if present {
			amt = a.Amount.FloatString(2)
		}
		t.Logf("%-38s A present=%-5v balance=%-6s name=%q deleg=%q addr=%q | B=%s | supply %s -> %s",
			c.name, present, amt, a.Name, a.ValidatorNode, a.Address, balance(after, "B").FloatString(2),
			before.FloatString(2), total(after).FloatString(2))
	}
}
