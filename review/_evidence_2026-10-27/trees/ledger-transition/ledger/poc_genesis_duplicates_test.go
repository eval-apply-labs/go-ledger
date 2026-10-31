package ledger

import (
	"math/big"
	"testing"
)

// PoC (ledger-transition review, low): cmd/server/config.go load() accepts duplicate genesis keys,
// negative/zero amounts and duplicate/over-long names. ProcessPayments keys accounts by a map
// (last duplicate wins) and drops all duplicates from the pass-through loop once the key is touched,
// so a duplicated genesis entry's balance silently vanishes on first use; and StateDigest's stable
// sort makes the digest depend on the input order of duplicates, contrary to its comment
// ("the digest must not depend on input order"). Logged; FAILS on the conservation check.
func TestPoCGenesisDuplicateKeyBreaksConservation(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "A", Amount: rat("5"), ValidatorNode: "A"}, // config typo: same key twice
		{PublicKey: "B", Amount: rat("1"), ValidatorNode: "B"},
	}
	before := total(accounts)
	after := ProcessPayments(accounts, []Payment{{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("1")}}, rat("0.1"))
	lost := new(big.Rat).Sub(new(big.Rat).Sub(before, total(after)), rat("0.1"))
	swapped := []Account{accounts[1], accounts[0], accounts[2]}
	t.Logf("supply before=%s after=%s (expected only the 0.1 fee to burn; extra lost=%s); digest order-dependent=%v",
		before.FloatString(2), total(after).FloatString(2), lost.FloatString(2), StateDigest(accounts) != StateDigest(swapped))
	if lost.Sign() != 0 {
		t.Errorf("a duplicated genesis key loses %s units on first use", lost.FloatString(2))
	}
}
