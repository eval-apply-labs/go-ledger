package node

import (
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// MaxRequestLookahead is how far past the height in progress a name request may name its block: an
// hour at one block per second. Every signature covers the block id, and the requestor, the bank and
// the new key may sign on three machines, so they need more than the minute payments get. Pending fees
// and one request per name and block still bound the pool. A freeze pays no fee, so it gets the
// payments' MaxBlockIDLookahead instead: otherwise one name could hold 3,601 pool slots for nothing.
// An honest freeze names a block a few ahead anyway, since it should land at once (owner, 2026-10-30).
const MaxRequestLookahead = 3600

// requestPool holds pending name requests waiting for the block they name, like the payment mempool:
// identified by content, bounded, and pruned once they can no longer apply. It holds one request per
// name and block, as a block carries at most one per name. Owned by the node's event loop, so it needs
// no locking.
type requestPool struct {
	pending []ledger.NameRequest
	seen    map[string]bool // the ids of the pending requests, and their slots (requestSlot)
	maxSize int             // 0 means unlimited
}

func newRequestPool(maxSize int) *requestPool {
	return &requestPool{seen: map[string]bool{}, maxSize: maxSize}
}

// requestSlot names the name and block a request takes up in the pool.
func requestSlot(request ledger.NameRequest) string {
	return "slot:" + request.Name + ":" + strconv.FormatInt(request.BlockID, 10)
}

// add pools a request, and reports whether it did: not when it is pooled already, when another request
// holds its name and block, or when the pool is full. It holds one request per name and block, as a
// block carries at most one per name, and as updatePool.add does for updates.
func (pool *requestPool) add(request ledger.NameRequest) bool {
	requestIdentity, slot := ledger.NameRequestID(request), requestSlot(request)
	if pool.seen[requestIdentity] || pool.seen[slot] {
		return false
	}
	if pool.maxSize > 0 && len(pool.pending) >= pool.maxSize {
		return false // full: apply backpressure
	}
	pool.seen[requestIdentity], pool.seen[slot] = true, true
	pool.pending = append(pool.pending, request)
	return true
}

func (pool *requestPool) contains(request ledger.NameRequest) bool {
	return pool.seen[ledger.NameRequestID(request)]
}

func (pool *requestPool) pendingCopy() []ledger.NameRequest {
	return append([]ledger.NameRequest(nil), pool.pending...)
}

// feesOf is the sum of the fees the payer's pooled requests carry: part of its pending outgoing.
func (pool *requestPool) feesOf(payerName string) *big.Rat {
	total := new(big.Rat)
	for _, request := range pool.pending {
		if request.Requestor == payerName && request.Fee != nil {
			total.Add(total, request.Fee)
		}
	}
	return total
}

// paysFeeIn reports whether the payer pays the fee of a pooled request for the block: a block prefers
// a payer's requests, so its payments for that block would lapse (spec 5.5).
func (pool *requestPool) paysFeeIn(payerName string, blockID int64) bool {
	for _, request := range pool.pending {
		if request.Requestor == payerName && request.Fee != nil && request.BlockID == blockID {
			return true
		}
	}
	return false
}

// keep keeps the pooled requests for which wanted reports true, in arrival order, and forgets the rest,
// so they can be submitted again.
func (pool *requestPool) keep(wanted func(ledger.NameRequest) bool) {
	var kept []ledger.NameRequest
	for _, request := range pool.pending {
		if wanted(request) {
			kept = append(kept, request)
		} else {
			delete(pool.seen, ledger.NameRequestID(request))
			delete(pool.seen, requestSlot(request))
		}
	}
	pool.pending = kept
}

// AdmitNameRequest runs the name-request intake (spec 4.3) and returns nil once the request is pooled,
// now or before, or the reason it was refused. Its shape, its checks against the committed view and,
// last, its signatures run here, off the loop (checkNameRequest); the loop runs the checks against the
// state again, confirms the signers' keys, and pools it (admitNameRequest).
func (node *Node) AdmitNameRequest(request ledger.NameRequest) error {
	requestorKey, bankKey, err := node.checkNameRequest(request)
	if err != nil {
		return err
	}
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventNameRequest, nameRequest: &request, verifiedKey: requestorKey, verifiedBankKey: bankKey, admit: verdict})
	_, err = node.awaitAdmission(verdict)
	return err
}

// checkNameRequest is the name-request intake's work off the loop (spec 4.3 steps 1 to 3): the shape,
// the checks against the committed view (nameRequestIntakeError), then the signatures, last, under the
// requestor's and the bank's keys in that view. It returns those two keys.
func (node *Node) checkNameRequest(request ledger.NameRequest) (requestorKey, bankKey string, err error) {
	if err := ledger.NameRequestShapeError(request); err != nil {
		return "", "", err
	}
	view := node.view.Load()
	if err := node.nameRequestIntakeError(view, request); err != nil {
		return "", "", err
	}
	requestor, _ := ledger.FindAccount(view.state.Accounts, request.Requestor)
	bank, _ := ledger.FindAccount(view.state.Accounts, request.Bank)
	if !ledger.VerifyNameRequest(node.config.Chain.Scheme, node.config.Chain.NetworkID, requestor.PublicKey, bank.PublicKey, request) {
		return "", "", errors.New("a signature does not verify under its signer's key")
	}
	return requestor.PublicKey, bank.PublicKey, nil
}

// nameRequestIntakeError is the name-request intake's check against the committed view (spec 4.3 step
// 2), made off the loop and again on the loop, and by the pool after each commit. The request must name
// a block from the height in progress to MaxRequestLookahead past it (MaxBlockIDLookahead for a
// freeze), fit the state (the rules of its kind, and a fee its payer can pay), and, for a freeze, leave
// a name seated once it applies (R9): the last seated name has no emergency brake, since a block that
// seats no name could never be followed.
func (node *Node) nameRequestIntakeError(view *committedView, request ledger.NameRequest) error {
	height := view.state.Height + 1
	lookahead := int64(MaxRequestLookahead)
	if request.Kind == ledger.FreezeKind {
		lookahead = MaxBlockIDLookahead
	}
	if request.BlockID < height || request.BlockID > height+lookahead {
		return fmt.Errorf("the request names block %d, outside the intake window %d to %d", request.BlockID, height, height+lookahead)
	}
	if err := ledger.NameRequestFitsState(view.state, view.validators, request); err != nil {
		return err
	}
	if request.Kind == ledger.FreezeKind && !ledger.LeavesANameSeated(node.config.Chain, view.state, ledger.BlockContent{NameRequests: []ledger.NameRequest{request}}) {
		return errors.New("freezing the name would leave no name seated, and then no block could ever commit again (R9)")
	}
	return nil
}

// admitNameRequest is the name-request intake on the loop (spec 4.3 step 4), after checkNameRequest. It
// runs nameRequestIntakeError again against the live state, and refuses a request whose requestor's or
// bank's key is no longer the one its signature was checked under. It accepts again a request already
// pooled, reporting false and no error. It refuses a fee whose payer has a payment pooled for the same
// block (spec 5.5) or whose pending outgoing, payments and fees, it would take above its balance; and a
// request the pool does not take (requestPool.add): another request for the same name and block, or a
// full pool. It logs that last refusal, for operators: wallets keep no sign log, so a second request
// for one name and block freezes nothing. Otherwise it pools the request, relays it, and reports true.
func (node *Node) admitNameRequest(request ledger.NameRequest, requestorKey, bankKey string) (bool, error) {
	if err := node.nameRequestIntakeError(node.view.Load(), request); err != nil {
		return false, err
	}
	requestor, _ := ledger.FindAccount(node.state.Accounts, request.Requestor)
	bank, _ := ledger.FindAccount(node.state.Accounts, request.Bank)
	if requestor.PublicKey != requestorKey || bank.PublicKey != bankKey {
		return false, errors.New("a signer's key changed after its signature was checked")
	}
	if node.requests.contains(request) {
		return false, nil // already pooled: an idempotent resubmission
	}
	if request.Fee != nil {
		withThisFee := new(big.Rat).Add(node.pendingOutgoing(request.Requestor), request.Fee)
		if !ledger.FeesFit(ledger.AccountAmount(node.state.Accounts, request.Requestor), withThisFee, node.mempool.paysIn(request.Requestor, request.BlockID)) {
			return false, errors.New("the fee payer has a payment pooled for the same block, or its pending payments and fees with this fee are above its balance")
		}
	}
	if !node.requests.add(request) {
		log.Printf("name request refused: another request for %q names block %d, or the request pool is full", request.Name, request.BlockID)
		return false, errors.New("not admitted: another request for this name names the same block, or the request pool is full")
	}
	node.relay(Message{Type: MessageNameRequest, NameRequest: &request})
	node.wakeIfIdle()
	return true, nil
}
