package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): the release-candidate fix made ApplyBlock reject a block padded
// with zero-fee no-op payments — but only for ONE no-op kind (unresolvable recipient). Payments that
// ProcessPayments drops just as silently (payer has no account; amount below the cost; a duplicate-
// content copy) and no-op updates (no such account) still pass ApplyBlock, occupying block space and
// a per-node signature verification for free. Asserts the documented property; FAILS today.
func TestPoCApplyBlockAcceptsOtherZeroFeeNoOps(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	funded, _ := signing.Generate(scheme, network)
	ghost, _ := signing.Generate(scheme, network) // no account
	F, G := funded.PublicKeyBase64(), ghost.PublicKeyBase64()
	prev := st([]Account{{PublicKey: F, Amount: rat("10"), ValidatorNode: F}}, rat("0.1"))
	const h = 4
	fromGhost, _ := SignPayment(ghost, Payment{PublicKeyFrom: G, PublicKeyTo: F, Amount: rat("5"), BlockID: h})
	belowCost, _ := SignPayment(funded, Payment{PublicKeyFrom: F, PublicKeyTo: G, Amount: rat("0.01"), BlockID: h})
	ghostUpdate, _ := SignAccountUpdate(ghost, AccountUpdate{From: G, Description: "no account", BlockID: h})
	blk := ProposeBlock(scheme, network, h, prev, BlockContent{Payments: []Payment{fromGhost, belowCost}, Updates: []AccountUpdate{ghostUpdate}})
	next, ok := ApplyBlock(scheme, network, prev, blk)
	t.Logf("block carrying %d no-op payments + %d no-op update: applies=%v, accounts unchanged=%v",
		len(blk.Payments), len(blk.Updates), ok, StateDigest(next.Accounts) == StateDigest(prev.Accounts))
	if ok && len(blk.Payments)+len(blk.Updates) > 0 && StateDigest(next.Accounts) == StateDigest(prev.Accounts) {
		t.Errorf("ApplyBlock accepted %d fee-free no-op messages (the class the RC fix closed only for unresolvable recipients)", len(blk.Payments)+len(blk.Updates))
	}
}

// PoC (ledger-transition review, docs-drift): Payment.PrevStateDigest is documented as "included only
// in a block whose PrevStateDigest matches (hex)" (ledger.go:40-41; operating-a-network.md "prior
// state"), and the only digest the node API exposes is the full committed digest (/hdls/resolve
// "stateDigest" = Commit.BlockDigest). The ledger instead compares the ACCOUNT-only digest
// (block.go:100,183), so a payment pinned per the docs can never be included — yet admitPayment does
// not check the field and issues a receipt. FAILS today.
func TestPoCPrevStateDigestPinPerDocsNeverIncludable(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payer, _ := signing.Generate(scheme, network)
	P := payer.PublicKeyBase64()
	prev := st([]Account{{PublicKey: P, Amount: rat("10"), ValidatorNode: P}}, rat("0.1"))
	const h = 4
	pinned, _ := SignPayment(payer, Payment{PublicKeyFrom: P, PublicKeyTo: "B", Amount: rat("1"), BlockID: h, PrevStateDigest: digestHex(prev.Digest())})
	blk := ProposeBlock(scheme, network, h, prev, BlockContent{Payments: []Payment{pinned}})
	t.Logf("block.PrevStateDigest == payment pin: %v; payment included by ProposeBlock: %v", digestHex(blk.PrevStateDigest) == pinned.PrevStateDigest, len(blk.Payments) == 1)
	if len(blk.Payments) == 0 {
		t.Errorf("a payment pinned to the block's PrevStateDigest (as documented) is excluded: the ledger compares the account-only digest")
	}
}
