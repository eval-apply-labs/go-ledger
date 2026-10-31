package node

import (
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// MaxBlockIDLookahead is how far past the current height a pooled payment or account update may name
// its block: a wallet names one a few blocks ahead (its lookahead), and the rest is slack for a node
// that is behind. The pools evict an entry only once its block has passed, so without this bound an
// entry naming a far-future block would hold its slot for good. Name requests other than a freeze
// get longer (MaxRequestLookahead).
const MaxBlockIDLookahead = 64

// mempool is a simple set of pending payments awaiting inclusion in the block they name. It is
// owned by the node's event loop, so it needs no locking. maxSize bounds its growth so a flood of valid
// payments can't exhaust memory; 0 means unlimited. Intake bounds each payment's comment before a
// payment gets here (checkPayment).
type mempool struct {
	pending []ledger.Payment
	seen    map[string]bool
	maxSize int
}

func newMempool(maxSize int) *mempool {
	return &mempool{seen: map[string]bool{}, maxSize: maxSize}
}

// add records a payment, returning true if it was newly accepted. It is rejected (false) — and so
// not gossiped onward — if it is a duplicate or the mempool is full. A payment is identified by its
// signed CONTENT, not its signature, so a malleated-signature copy dedups to the same entry (see
// ledger.PaymentID). Bounding here is what stops a valid-but-spammy flood from growing the mempool
// without limit.
func (pool *mempool) add(payment ledger.Payment) bool {
	key := ledger.PaymentID(payment)
	if pool.seen[key] {
		return false
	}
	if pool.maxSize > 0 && len(pool.pending) >= pool.maxSize {
		return false // full: apply backpressure
	}
	pool.seen[key] = true
	pool.pending = append(pool.pending, payment)
	return true
}

func (pool *mempool) pendingCopy() []ledger.Payment {
	return append([]ledger.Payment(nil), pool.pending...)
}

// contains reports whether a payment with this content is already pending.
func (pool *mempool) contains(payment ledger.Payment) bool {
	return pool.seen[ledger.PaymentID(payment)]
}

// outgoingFor is the sum of the payer's pending payments' amounts: the payments' part of its pending
// outgoing (see Node.pendingOutgoing).
func (pool *mempool) outgoingFor(payerName string) *big.Rat {
	total := new(big.Rat)
	for _, payment := range pool.pending {
		if payment.From == payerName && payment.Amount != nil {
			total.Add(total, payment.Amount)
		}
	}
	return total
}

// paysIn reports whether the payer has a payment pooled for the block: then a fee it would pay in
// that block is refused, since a block prefers a payer's requests and its payments would lapse (spec
// 5.5).
func (pool *mempool) paysIn(payerName string, blockID int64) bool {
	for _, payment := range pool.pending {
		if payment.From == payerName && payment.BlockID == blockID {
			return true
		}
	}
	return false
}

// keep keeps the pooled payments for which wanted reports true, in arrival order, and forgets the rest,
// so they can be submitted again (see Node.reconcilePools).
func (pool *mempool) keep(wanted func(ledger.Payment) bool) {
	var kept []ledger.Payment
	for _, payment := range pool.pending {
		if wanted(payment) {
			kept = append(kept, payment)
		} else {
			delete(pool.seen, ledger.PaymentID(payment))
		}
	}
	pool.pending = kept
}

// AdmitPayment runs the payment intake (spec 4.1) and returns its verdict: whether the payment was newly
// pooled, and relayed, or the reason it was refused. A payment already pooled is neither: an idempotent
// resubmission. The checks against the committed view and the payer's signature run here, off the loop
// (checkPayment); the loop runs the cheap checks again against its live state, confirms that the payer's
// key is still the one checked, and pools the payment (admitPayment). This is the synchronous intake the
// HTTP layer uses, so a receipt is only ever signed for a payment the node newly holds.
func (node *Node) AdmitPayment(payment ledger.Payment) (newlyPooled bool, err error) {
	verifiedKey, err := node.checkPayment(payment)
	if err != nil {
		return false, err
	}
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventPayment, payment: &payment, verifiedKey: verifiedKey, admit: verdict})
	return node.awaitAdmission(verdict)
}

// checkPayment is the payment intake's work off the loop (spec 4.1 steps 2 to 4), against the committed
// view: the payment's shape (a positive amount, a comment within MaxCommentLength), its checks against
// the committed state (paymentIntakeError), then the payer's signature, last, under the payer's key in
// that state. It returns the key the signature verified under.
func (node *Node) checkPayment(payment ledger.Payment) (string, error) {
	switch {
	case payment.Amount == nil || payment.Amount.Sign() <= 0:
		return "", errors.New("non-positive amount")
	case node.config.MaxCommentLength > 0 && len(payment.Comment) > node.config.MaxCommentLength:
		return "", errors.New("comment too long")
	}
	view := node.view.Load()
	if err := paymentIntakeError(view, payment); err != nil {
		return "", err
	}
	payer, _ := ledger.FindAccount(view.state.Accounts, payment.From)
	if !ledger.VerifyPayment(node.config.Chain.Scheme, node.config.Chain.NetworkID, payer.PublicKey, payment) {
		return "", errors.New("invalid payment signature: it does not verify under the payer's key")
	}
	return payer.PublicKey, nil
}

// paymentIntakeError is the payment intake's check against the committed view (spec 4.1 step 3), made
// off the loop and again on the loop, where the view is the live committed state. The payment must
// name a block from the height in progress to MaxBlockIDLookahead past it: the pool evicts a payment
// only once its block has passed, so a far-future one would hold its slot for good. A pinned payment
// must name the height in progress and pin the committed state's digest, the only state a block of
// that height builds on: a pin for a later block can never be checked here, so it is refused (E38). It
// must fit the state (ledger.PaymentFitsState: the cost and the largest payment, the message bound, a
// payer and a payee that exist and are not frozen, the payer's balance), and its credit must keep its
// payee within the largest balance. No block could carry a payment that fails any of these.
func paymentIntakeError(view *committedView, payment ledger.Payment) error {
	state := view.state
	height := state.Height + 1
	switch {
	case payment.BlockID < height:
		return errors.New("stale block id: the payment names an already-committed block")
	case payment.BlockID > height+MaxBlockIDLookahead:
		return errors.New("block id too far ahead: the payment names a block beyond the intake window")
	case payment.PreviousStateDigest != "" && payment.BlockID != height:
		return errors.New("a pinned payment must name the next block, the one built on the state it pins")
	case payment.PreviousStateDigest != "" && payment.PreviousStateDigest != hex.EncodeToString(view.stateDigest[:]):
		return errors.New("pinned to another state: the pin is not the digest of the committed state")
	}
	if err := ledger.PaymentFitsState(state, payment); err != nil {
		return err
	}
	if !ledger.CreditFitsBalanceCap(state, payment) {
		return errors.New("would lift the payee above the largest balance the network allows (MaxAccountBalance)")
	}
	return nil
}

// admitPayment is the payment intake on the loop (spec 4.1 step 5), after checkPayment: it pools (and
// relays) a payment only if it could actually clear. It runs paymentIntakeError again against the live
// state, and refuses a payment whose payer's key is no longer verifiedKey, the key its signature was
// checked under. A payment already pooled is accepted again, as an idempotent resubmission: it reports
// false and no error, and the HTTP layer signs no new receipt for it. It refuses a payment whose payer
// pays the fee of a pooled name request for the same block: a block prefers the payer's requests, so
// the payment would lapse after a receipt (spec 5.5).
//
// The load-bearing check is the overdraw guardrail — a payment is refused, with no receipt, if the
// payer's pending outgoing (pendingOutgoing: its pooled payments and fees) plus this payment exceeds its
// committed balance. The ledger's response to an overdraw inside a committed block is deliberately
// harsh (the payer is banned and its balance destroyed — see ledger.ProcessPayments); with every honest
// intake refusing up front, an honest user cannot stumble into that, and a banned overdraw in a
// committed block can only mean a misused/cloned key or a proposer that bypassed intake. Refused payments are also not relayed, so junk
// doesn't propagate. The guard is against this node's local view (committed balance + its own pending
// pool); pending incoming credits don't count, so it is conservative.
func (node *Node) admitPayment(payment ledger.Payment, verifiedKey string) (bool, error) {
	if err := paymentIntakeError(node.view.Load(), payment); err != nil {
		return false, err
	}
	if payer, _ := ledger.FindAccount(node.state.Accounts, payment.From); payer.PublicKey != verifiedKey {
		return false, errors.New("the payer's key changed after its signature was checked")
	}
	if node.mempool.contains(payment) {
		return false, nil // already pooled: an idempotent resubmission
	}
	if node.requests.paysFeeIn(payment.From, payment.BlockID) {
		return false, errors.New("the payer pays the fee of a pooled name request for the same block, which the block would prefer")
	}
	withThisPayment := new(big.Rat).Add(node.pendingOutgoing(payment.From), payment.Amount)
	if withThisPayment.Cmp(ledger.AccountAmount(node.state.Accounts, payment.From)) > 0 {
		return false, errors.New("would overdraw: the payer's pending outgoing plus this payment exceeds its balance")
	}
	if !node.mempool.add(payment) {
		return false, errors.New("not admitted: the mempool is full")
	}
	node.relay(Message{Type: MessagePayment, Payment: &payment})
	node.wakeIfIdle()
	return true, nil
}

// pendingOutgoing is the payer's pending outgoing (spec 6.5): the amounts of its pooled payments plus
// the fees of its pooled name requests.
func (node *Node) pendingOutgoing(payerName string) *big.Rat {
	return new(big.Rat).Add(node.mempool.outgoingFor(payerName), node.requests.feesOf(payerName))
}
