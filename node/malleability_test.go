package node

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The mempool must dedup by content, so a malleated-signature copy of a queued payment is dropped
// (and, since add reports it not-new, never re-gossiped).
func TestMempoolDedupsMalleatedDuplicate(t *testing.T) {
	mempool := newMempool(0)
	base := ledger.Payment{From: "alice", To: "bob", Amount: rational("10"), BlockID: 5}
	original, malleated := base, base
	original.Signature, malleated.Signature = "sigAAA", "sigBBB"

	if !mempool.add(original) {
		t.Fatal("first payment should be recorded as new")
	}
	if mempool.add(malleated) {
		t.Fatal("a malleated duplicate (same content, different signature) must be deduped")
	}
	if got := len(mempool.pendingCopy()); got != 1 {
		t.Fatalf("mempool should hold exactly 1 payment, got %d", got)
	}
}
