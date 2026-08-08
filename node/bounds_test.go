package node

import (
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

func payTo(sig, comment string, blockID int64) ledger.Payment {
	return ledger.Payment{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("1"), Comment: comment, BlockID: blockID, Signature: sig}
}

// The mempool must bound its size and reject over-long comments — the DoS bounds a valid-payment
// flood would otherwise blow past.
func TestMempoolBounds(t *testing.T) {
	m := newMempool(2, 8) // hold at most 2 payments; comments up to 8 bytes

	if !m.add(payTo("s1", "ok", 5)) {
		t.Fatal("first payment should be accepted")
	}
	if !m.add(payTo("s2", "ok", 6)) {
		t.Fatal("second payment should be accepted")
	}
	if m.add(payTo("s3", "ok", 7)) {
		t.Fatal("third payment must be rejected — mempool is full")
	}
	if got := len(m.snapshot()); got != 2 {
		t.Fatalf("mempool should hold exactly 2, got %d", got)
	}
	if m.add(payTo("s4", strings.Repeat("x", 9), 8)) {
		t.Fatal("a payment with an over-long comment must be rejected")
	}
}
