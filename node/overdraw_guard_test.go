package node

import (
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The intake guardrail: a payment that would overdraw the payer's balance — counting outgoing
// already promised in the mempool — is refused with no receipt, so an honest user can't stumble
// into the ledger's overdraw ban (which destroys the payer's whole balance) by double-sending.
func TestIntakeRefusesOverdraw(t *testing.T) {
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), testAccount("stranger", "0")}, 5*time.Second, 50*time.Millisecond)
	node := New(testConfig(dynamicChain("100"), "founder", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()

	// A block well ahead, within the intake window, so the payments stay pending for the whole test.
	pay := func(from, to, amount, comment string) ledger.Payment {
		payment, _ := ledger.SignPayment(testSigner(from), ledger.Payment{From: from, To: to, Amount: rational(amount), Comment: comment, BlockID: MaxBlockIDLookahead})
		return payment
	}

	first := pay("founder", "stranger", "600", "first")
	if newlyPooled, err := node.AdmitPayment(first); err != nil || !newlyPooled {
		t.Fatalf("a 600-of-1000 payment should be pooled, got %v, %v", newlyPooled, err)
	}
	// A second individually-valid payment that would take the block total to 1200 > 1000: the exact
	// shape that used to send an honest double-sender to the shadow realm.
	if _, err := node.AdmitPayment(pay("founder", "stranger", "600", "second")); err == nil || !strings.Contains(err.Error(), "overdraw") {
		t.Fatalf("a payment overdrawing the promised total must be refused, got: %v", err)
	}
	// Resubmitting the queued payment is accepted again, as an idempotent no-op that earns no receipt.
	if newlyPooled, err := node.AdmitPayment(first); err != nil || newlyPooled {
		t.Fatalf("resubmitting an already-queued payment should be accepted, not pooled again: %v, %v", newlyPooled, err)
	}
	// A payer at zero has nothing to promise, and a name with no account has nothing at all.
	if _, err := node.AdmitPayment(pay("stranger", "founder", "1", "unfunded")); err == nil {
		t.Fatal("a payment from an unfunded account must be refused")
	}
	if _, err := node.AdmitPayment(pay("ghost", "founder", "1", "accountless")); err == nil {
		t.Fatal("a payment from a name with no account must be refused")
	}
	// A payment naming an already-committed block can never clear.
	stale, _ := ledger.SignPayment(testSigner("founder"), ledger.Payment{From: "founder", To: "stranger", Amount: rational("1"), Comment: "stale", BlockID: 0})
	if _, err := node.AdmitPayment(stale); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a payment naming a past block must be refused as stale, got: %v", err)
	}
}

// Intake refuses a payment that could never apply instead of admitting it, relaying it, and handing
// back a receipt for what no block will carry (which the sender would mistake for a completed
// payment): one to a name no account holds, and one from or to a frozen name (spec 4.1). It checks the
// payer's signature under the key the committed state holds for the payer's name.
func TestIntakeRefusesAPaymentToAnUnknownOrFrozenName(t *testing.T) {
	frozen := testAccount("frozen", "100")
	frozen.Frozen = true
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), frozen}, 5*time.Second, 50*time.Millisecond)
	node := New(testConfig(dynamicChain("100"), "founder", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()

	misattributed, _ := ledger.SignPayment(testSigner("frozen"), ledger.Payment{From: "founder", To: "founder", Amount: rational("1"), BlockID: MaxBlockIDLookahead}) // another account's key signs a payment from the founder
	for _, testCase := range []struct {
		name    string
		payment ledger.Payment
		reason  string
	}{
		{"to a name with no account", signedPayment("founder", "nobody", "1", MaxBlockIDLookahead), `no account is named "nobody"`},
		{"to a frozen name", signedPayment("founder", "frozen", "1", MaxBlockIDLookahead), `the payee "frozen" is frozen`},
		{"from a frozen name", signedPayment("frozen", "founder", "1", MaxBlockIDLookahead), `the payer "frozen" is frozen`},
		{"signed under another name's key", misattributed, "invalid payment signature"},
	} {
		if _, err := node.AdmitPayment(testCase.payment); err == nil || !strings.Contains(err.Error(), testCase.reason) {
			t.Errorf("%s: got %v, want a refusal mentioning %q", testCase.name, err, testCase.reason)
		}
	}
	if _, err := node.AdmitPayment(signedPayment("founder", "founder", "1", MaxBlockIDLookahead)); err != nil {
		t.Fatalf("a payment between names that exist and are not frozen must be admitted, got: %v", err)
	}
}

// After a commit changes balances under the pool (payments admitted by other nodes, a reassignment),
// pending payments that now overdraw are dropped, in arrival order, keeping the invariant that
// this node can never propose a block the ledger would answer with an overdraw ban (see
// reconcilePools). A dropped payment is forgotten, so it can come back once the balance allows.
func TestMempoolDropsOverdrawnOnBalanceDrop(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("alice", "1000"), testAccount("bob", "0"), testAccount("carol", "0")}, time.Hour, time.Hour)
	node := New(testConfig(staticChain("bob"), "bob", genesis), NewHTTPTransport(nil))
	first, second := signedPayment("alice", "bob", "300", 10), signedPayment("alice", "carol", "300", 11)
	if !node.mempool.add(first) || !node.mempool.add(second) {
		t.Fatal("both payments should enter the pool")
	}
	setAliceBalance := func(amount string) {
		previousAccounts := node.state.Accounts
		node.state.Accounts = append([]ledger.Account(nil), previousAccounts...)
		node.state.Accounts[0].Amount = rational(amount) // alice sorts first
		node.publishView()
		node.reconcilePools(previousAccounts)
	}

	setAliceBalance("500") // the first 300 still fits; both together do not
	if pending := node.mempool.pendingCopy(); len(pending) != 1 || pending[0].To != "bob" {
		t.Fatalf("only the payment that arrived first and still fits should remain, got %v", pending)
	}
	if node.mempool.contains(second) {
		t.Fatal("a dropped payment must be forgotten (resubmittable after a refill)")
	}
	setAliceBalance("900")
	if !node.mempool.add(second) {
		t.Fatal("the dropped payment should be re-addable")
	}
}
