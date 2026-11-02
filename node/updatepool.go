package node

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// updatePool holds pending account updates (re-delegations, descriptions, addresses) waiting for the
// block they name, mirroring the payment mempool: content-deduplicated, size-bounded, and pruned once
// they can no longer apply. Owned by the node's event loop, so it needs no locking.
type updatePool struct {
	pending []ledger.AccountUpdate
	seen    map[string]bool // the ids of the pending updates, and their slots (updateSlot)
	maxSize int             // 0 means unlimited
}

func newUpdatePool(maxSize int) *updatePool {
	return &updatePool{seen: map[string]bool{}, maxSize: maxSize}
}

// add records an update, returning true only if it is newly accepted (identified by CONTENT, not
// signature, so a malleated copy dedups); the caller gossips onward only what add reports new. It
// holds one update per account and block: a block carries one per account (ledger.ProposeBlock), so
// a second would only hold a slot, and one account signing many could fill the pool.
func (pool *updatePool) add(update ledger.AccountUpdate) bool {
	updateIdentity, slot := ledger.UpdateID(update), updateSlot(update)
	if pool.seen[updateIdentity] || pool.seen[slot] {
		return false
	}
	if pool.maxSize > 0 && len(pool.pending) >= pool.maxSize {
		return false // full: apply backpressure
	}
	pool.seen[updateIdentity], pool.seen[slot] = true, true
	pool.pending = append(pool.pending, update)
	return true
}

// contains reports whether an update with this content is already pending.
func (pool *updatePool) contains(update ledger.AccountUpdate) bool {
	return pool.seen[ledger.UpdateID(update)]
}

// updateSlot names the account and block an update takes up in the pool (see add).
func updateSlot(update ledger.AccountUpdate) string {
	return "slot:" + update.Name + ":" + strconv.FormatInt(update.BlockID, 10)
}

func (pool *updatePool) pendingCopy() []ledger.AccountUpdate {
	return append([]ledger.AccountUpdate(nil), pool.pending...)
}

// keep keeps the pooled updates for which wanted reports true, in arrival order, and forgets the rest,
// so they no longer hold a slot (see Node.reconcilePools).
func (pool *updatePool) keep(wanted func(ledger.AccountUpdate) bool) {
	var kept []ledger.AccountUpdate
	for _, update := range pool.pending {
		if wanted(update) {
			kept = append(kept, update)
		} else {
			delete(pool.seen, ledger.UpdateID(update))
			delete(pool.seen, updateSlot(update))
		}
	}
	pool.pending = kept
}

// AdmitUpdate runs the account-update intake (spec 4.2) and returns its verdict, as AdmitPayment does:
// whether the update was newly pooled, and relayed, or the reason it was refused. One already pooled is
// neither. As for a payment, the checks against the committed view and the signature run here
// (checkUpdate), and the loop runs the cheap checks again and pools it (admitUpdate).
func (node *Node) AdmitUpdate(update ledger.AccountUpdate) (newlyPooled bool, err error) {
	verifiedKey, err := node.checkUpdate(update)
	if err != nil {
		return false, err
	}
	verdict := make(chan admission, 1)
	node.enqueue(event{kind: eventUpdate, update: &update, verifiedKey: verifiedKey, admit: verdict})
	return node.awaitAdmission(verdict)
}

// checkUpdate is the account-update intake's work off the loop (spec 4.2 steps 1 to 3), against the
// committed view: the update's checks against the committed state (updateIntakeError), then its
// signature, last, under its account's key in that state. It returns the key the signature verified
// under.
func (node *Node) checkUpdate(update ledger.AccountUpdate) (string, error) {
	state := node.view.Load().state
	if err := updateIntakeError(state, update); err != nil {
		return "", err
	}
	account, _ := ledger.FindAccount(state.Accounts, update.Name)
	if !ledger.VerifyAccountUpdate(node.config.Chain.Scheme, node.config.Chain.NetworkID, account.PublicKey, update) {
		return "", errors.New("invalid update signature: it does not verify under the account's key")
	}
	return account.PublicKey, nil
}

// updateIntakeError is the account-update intake's check against a committed state (spec 4.2 step 2),
// made off the loop against the committed view and again on the loop against the live state. The update
// must name a block from the height in progress to MaxBlockIDLookahead past it, and fit the state
// (ledger.UpdateFitsState: it changes a field, fits the governed size bound as payments do, and names
// an account, and any delegate, that exists and is not frozen). Anything else could never apply: it
// would only hold a slot in the bounded pool, and enough of it would silently crowd out every honest
// update.
func updateIntakeError(state ledger.LedgerState, update ledger.AccountUpdate) error {
	height := state.Height + 1
	if update.BlockID < height || update.BlockID > height+MaxBlockIDLookahead {
		return fmt.Errorf("the update names block %d, outside the intake window %d to %d", update.BlockID, height, height+MaxBlockIDLookahead)
	}
	return ledger.UpdateFitsState(state, update)
}

// admitUpdate is the account-update intake on the loop (spec 4.2 step 4), after checkUpdate: it runs
// updateIntakeError again against the live state, refuses an update whose account's key is no longer
// verifiedKey, accepts again one already pooled (reporting false and no error), and refuses another
// update of the same account for the same block, or one that finds the pool full. Otherwise it pools
// the update, relays it, and reports true.
func (node *Node) admitUpdate(update ledger.AccountUpdate, verifiedKey string) (bool, error) {
	if err := updateIntakeError(node.state, update); err != nil {
		return false, err
	}
	if account, _ := ledger.FindAccount(node.state.Accounts, update.Name); account.PublicKey != verifiedKey {
		return false, errors.New("the account's key changed after its signature was checked")
	}
	if node.updates.contains(update) {
		return false, nil // already pooled: an idempotent resubmission
	}
	if !node.updates.add(update) {
		return false, errors.New("not admitted: another update of this account names the same block, or the pool is full")
	}
	node.relay(Message{Type: MessageUpdate, Update: &update})
	node.wakeIfIdle()
	return true, nil
}
