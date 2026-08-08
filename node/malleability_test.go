package node

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The mempool must dedup by content, so a malleated-signature copy of a queued payment is dropped
// (and, since add reports it not-new, never re-gossiped).
func TestMempoolDedupsMalleatedDuplicate(t *testing.T) {
	m := newMempool(0, 0)
	base := ledger.Payment{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("10"), BlockID: 5}
	p1, p2 := base, base
	p1.Signature, p2.Signature = "sigAAA", "sigBBB"

	if !m.add(p1) {
		t.Fatal("first payment should be recorded as new")
	}
	if m.add(p2) {
		t.Fatal("a malleated duplicate (same content, different signature) must be deduped")
	}
	if got := len(m.snapshot()); got != 1 {
		t.Fatalf("mempool should hold exactly 1 payment, got %d", got)
	}
}
