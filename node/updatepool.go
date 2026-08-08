package node

import "github.com/eval-apply-co-uk/go-ledger/ledger"

// updatePool holds pending account-attribute updates (the registry / re-delegation messages) waiting
// for the block they name, mirroring the payment mempool: content-deduplicated, size-bounded, and
// pruned once their target block has passed. Owned by the node's event loop, so it needs no locking.
type updatePool struct {
	pendingUpdates []ledger.AccountUpdate
	seenIdentities map[string]bool
	maximumSize    int
}

func newUpdatePool(maximumSize int) *updatePool {
	return &updatePool{seenIdentities: map[string]bool{}, maximumSize: maximumSize}
}

// add records an update, returning true only if it is newly accepted (identified by CONTENT, not
// signature, so a malleated copy dedups); the caller gossips onward only what add reports new.
func (pool *updatePool) add(update ledger.AccountUpdate) bool {
	updateIdentity := ledger.UpdateID(update)
	if updateIdentity == "" || pool.seenIdentities[updateIdentity] {
		return false
	}
	if pool.maximumSize > 0 && len(pool.pendingUpdates) >= pool.maximumSize {
		return false // full: apply backpressure
	}
	pool.seenIdentities[updateIdentity] = true
	pool.pendingUpdates = append(pool.pendingUpdates, update)
	return true
}

func (pool *updatePool) snapshot() []ledger.AccountUpdate {
	return append([]ledger.AccountUpdate(nil), pool.pendingUpdates...)
}

// dropBelow removes updates whose target block has passed, so they can never be re-included.
func (pool *updatePool) dropBelow(minimumBlockID int64) {
	keptUpdates := pool.pendingUpdates[:0]
	for _, update := range pool.pendingUpdates {
		if update.BlockID >= minimumBlockID {
			keptUpdates = append(keptUpdates, update)
		} else {
			delete(pool.seenIdentities, ledger.UpdateID(update))
		}
	}
	pool.pendingUpdates = append([]ledger.AccountUpdate(nil), keptUpdates...)
}
