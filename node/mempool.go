package node

import (
	"math/big"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// mempool is a simple set of pending payments awaiting inclusion in the block they name. It is
// owned by the node's event loop, so it needs no locking. maxSize/maxComment bound its growth so a
// flood of valid payments can't exhaust memory (the DoS bound restored here);
// 0 means unlimited.
type mempool struct {
	pending    []ledger.Payment
	seen       map[string]bool
	maxSize    int
	maxComment int
}

func newMempool(maxSize, maxComment int) *mempool {
	return &mempool{seen: map[string]bool{}, maxSize: maxSize, maxComment: maxComment}
}

// add records a payment, returning true if it was newly accepted. It is rejected (false) — and so
// not gossiped onward — if its comment exceeds maxComment, it is a duplicate, or the mempool is
// full. Bounding here is what stops a valid-but-spammy flood from growing the mempool without limit.
func (m *mempool) add(p ledger.Payment) bool {
	if m.maxComment > 0 && len(p.Comment) > m.maxComment {
		return false // over-long comment: refuse to hold or relay it
	}
	k := paymentKey(p)
	if k == "" || m.seen[k] {
		return false
	}
	if m.maxSize > 0 && len(m.pending) >= m.maxSize {
		return false // full: apply backpressure
	}
	m.seen[k] = true
	m.pending = append(m.pending, p)
	return true
}

func (m *mempool) snapshot() []ledger.Payment {
	return append([]ledger.Payment(nil), m.pending...)
}

// contains reports whether a payment with this content is already pending.
func (m *mempool) contains(p ledger.Payment) bool { return m.seen[paymentKey(p)] }

// outgoingFor is the payer's already-promised outgoing: the sum of its pending payments' amounts.
func (m *mempool) outgoingFor(payer string) *big.Rat {
	total := new(big.Rat)
	for _, p := range m.pending {
		if p.PublicKeyFrom == payer && p.Amount != nil {
			total.Add(total, p.Amount)
		}
	}
	return total
}

// dropOverdrawn re-establishes the intake invariant (a payer's pending outgoing never exceeds its
// committed balance) after the balance changed under the pool — a commit carrying payments admitted
// by OTHER nodes, a reassignment, or a slash. Payments are kept in arrival order while their running
// total still fits the payer's balance; the rest are dropped (and forgotten, so they can be
// resubmitted once the balance allows). Together with the intake guard this means a block this node
// proposes can never trip the ledger's overdraw ban.
func (m *mempool) dropOverdrawn(balanceOf func(payer string) *big.Rat) {
	promised := map[string]*big.Rat{}
	kept := m.pending[:0]
	for _, p := range m.pending {
		amount := p.Amount
		if amount == nil {
			amount = new(big.Rat)
		}
		total, ok := promised[p.PublicKeyFrom]
		if !ok {
			total = new(big.Rat)
			promised[p.PublicKeyFrom] = total
		}
		next := new(big.Rat).Add(total, amount)
		if next.Cmp(balanceOf(p.PublicKeyFrom)) > 0 {
			delete(m.seen, paymentKey(p))
			continue
		}
		total.Set(next)
		kept = append(kept, p)
	}
	m.pending = append([]ledger.Payment(nil), kept...)
}

// dropBelow removes payments whose target block is below minBlockID — their block has passed, so
// they are stale (applied already, or missed) and can never be included again. Together with the
// per-block inclusion rule (a payment is only included in the block it names) this both bounds the
// mempool and prevents replay: a resubmitted, already-cleared payment names a past block and is
// never re-includable.
func (m *mempool) dropBelow(minBlockID int64) {
	kept := m.pending[:0]
	for _, p := range m.pending {
		if p.BlockID >= minBlockID {
			kept = append(kept, p)
		} else {
			delete(m.seen, paymentKey(p))
		}
	}
	m.pending = append([]ledger.Payment(nil), kept...)
}

// paymentKey identifies a payment by its signed CONTENT, not its signature, so a malleated-signature
// copy dedups to the same entry (see ledger.PaymentID).
func paymentKey(p ledger.Payment) string { return ledger.PaymentID(p) }
