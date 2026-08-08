package ledger

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// The optional fork-binding tier: a payment carrying a PrevStateDigest is included only on a block
// whose prior state matches, so it cannot be replayed onto a fork with a different history.
func TestPrevStateBindingPinsToHistory(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payerKey, _ := signing.Generate(scheme, network)
	payee, _ := signing.Generate(scheme, network)
	payer := payerKey.PublicKeyBase64()

	prev := []Account{{PublicKey: payer, Name: "p", Amount: big.NewRat(1000, 1), ValidatorNode: payer}}
	prevHex := hex.EncodeToString(func() []byte { d := StateDigest(prev); return d[:] }())
	cost := big.NewRat(1, 10)

	mk := func(prevBind string) Payment {
		p := Payment{PublicKeyFrom: payer, PublicKeyTo: payee.PublicKeyBase64(), Amount: big.NewRat(100, 1), BlockID: 5, PrevStateDigest: prevBind}
		p, _ = SignPayment(payerKey, p)
		return p
	}

	// Correctly pinned → included and applies.
	good := mk(prevHex)
	blk := ProposeBlock(scheme, network, 5, st(prev, cost), BlockContent{Payments: []Payment{good}})
	if len(blk.Payments) != 1 {
		t.Fatal("payment pinned to the correct history should be included")
	}
	if _, ok := ApplyBlock(scheme, network, st(prev, cost), blk); !ok {
		t.Fatal("a block with a correctly-pinned payment should apply")
	}

	// Pinned to a different history → excluded by the proposer.
	wrong := mk("00000000000000000000000000000000000000000000000000000000deadbeef")
	if got := len(ProposeBlock(scheme, network, 5, st(prev, cost), BlockContent{Payments: []Payment{wrong}}).Payments); got != 0 {
		t.Fatalf("payment pinned to a wrong history must be excluded, got %d included", got)
	}

	// And ApplyBlock rejects a block that smuggles a wrongly-pinned (but validly signed) payment.
	tampered := blk
	tampered.Payments = []Payment{wrong}
	if _, ok := ApplyBlock(scheme, network, st(prev, cost), tampered); ok {
		t.Fatal("ApplyBlock must reject a fork-pinned payment placed on the wrong history")
	}

	// An unpinned payment (empty PrevStateDigest) is unaffected — the default path still works.
	if got := len(ProposeBlock(scheme, network, 5, st(prev, cost), BlockContent{Payments: []Payment{mk("")}}).Payments); got != 1 {
		t.Fatalf("an unpinned payment should still be included, got %d", got)
	}
}
