package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): the documented trigger for the overdraw ban is "two individually-
// valid, genuinely-signed payments from the same payer in one block" (findings.md design decision),
// and the second-pass closure is the wallet never signing two payments naming the same block
// (ui/wallet.go nextPaymentBlockID). This test shows the ban's actual semantics:
//
//  1. ONE payment whose amount exceeds the payer's balance at its block bans the payer and destroys
//     the whole balance — no same-block pair is needed. An honest wallet signs any positive amount
//     without a balance check (ui/wallet.go Send) and the node refuses it with 409 only AFTER
//     receiving the signature; honest proposers then never include it, but a block that does carry
//     it applies cleanly (ApplyBlock has no overdraw check).
//  2. A banned payer that also RECEIVES a credit in the same block is kept with its full balance plus
//     the credit (it is re-added via the `affected` loop), contrary to "the account is dropped from
//     the result entirely" — the penalty is avoidable by whoever controls the block's contents.
//
// Both are logged; the test FAILS on (1) to flag the doc/closure mismatch.
func TestPoCOverdrawBanSemantics(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payerSigner, _ := signing.Generate(scheme, network)
	A := payerSigner.PublicKeyBase64()
	cost := rat("0.1")

	// (1) Single over-balance payment, applied through the real block path.
	prev := st([]Account{
		{PublicKey: A, Name: "alice", Amount: rat("10"), ValidatorNode: A},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
	}, cost)
	single, _ := SignPayment(payerSigner, Payment{PublicKeyFrom: A, PublicKeyTo: "B", Amount: rat("100"), BlockID: 4})
	blk := ProposeBlock(scheme, network, 4, prev, BlockContent{Payments: []Payment{single}})
	next, ok := ApplyBlock(scheme, network, prev, blk)
	t.Logf("(1) block with ONE over-balance payment: applies=%v, carried=%d, payer present after=%v, payer balance before=10",
		ok, len(blk.Payments), has(next.Accounts, A))
	singleTriggersBan := ok && len(blk.Payments) == 1 && !has(next.Accounts, A)

	// (2) Banned payer that also receives a credit in the same block.
	accounts := []Account{
		{PublicKey: "A", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
		{PublicKey: "C", Amount: rat("5"), ValidatorNode: "C"},
	}
	after := ProcessPayments(accounts, []Payment{
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6")},
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6"), Comment: "second"}, // overdraw → A banned
		{PublicKeyFrom: "C", PublicKeyTo: "A", Amount: rat("1")},                    // a credit to A in the same block
	}, cost)
	t.Logf("(2) banned payer with a same-block credit: present=%v balance=%s (documented: dropped entirely)",
		has(after, "A"), balance(after, "A").FloatString(2))

	if singleTriggersBan {
		t.Errorf("a single over-balance payment destroys the payer's whole balance; the documented trigger (and the wallet's same-block guard) assumes two same-block payments are required")
	}
	if has(after, "A") {
		t.Errorf("a banned payer receiving a same-block credit keeps its balance (%s): the ban's destroy semantics depend on unrelated payments in the block", balance(after, "A").FloatString(2))
	}
	_ = big.NewRat
}
