package node

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

func payTo(signature, comment string, blockID int64) ledger.Payment {
	return ledger.Payment{From: "alice", To: "bob", Amount: rational("1"), Comment: comment, BlockID: blockID, Signature: signature}
}

// The mempool must bound its size — the DoS bound a valid-payment flood would otherwise blow past.
// (Intake bounds the comment: see TestIntakeRefusesAnOverlongComment.)
func TestMempoolBounds(t *testing.T) {
	mempool := newMempool(2) // hold at most 2 payments

	if !mempool.add(payTo("s1", "ok", 5)) {
		t.Fatal("first payment should be accepted")
	}
	if !mempool.add(payTo("s2", "ok", 6)) {
		t.Fatal("second payment should be accepted")
	}
	if mempool.add(payTo("s3", "ok", 7)) {
		t.Fatal("third payment must be rejected — mempool is full")
	}
	if got := len(mempool.pendingCopy()); got != 2 {
		t.Fatalf("mempool should hold exactly 2, got %d", got)
	}
}
