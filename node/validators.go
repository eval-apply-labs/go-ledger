package node

import (
	"math/big"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// validatorsFor returns the validator set that governs a block built on the given accounts. With a
// fixed Config.Validators it is that set (static). Otherwise it is DERIVED from the accounts by
// stake (dynamic admission): the set is a pure function of the committed state, so every node
// computes the same one, and a newly-funded account joins automatically at the next block.
func (n *Node) validatorsFor(accounts []ledger.Account) *consensus.ValidatorSet {
	if n.cfg.Validators != nil {
		return n.cfg.Validators
	}
	return deriveValidators(accounts, n.cfg.ValidatorThreshold)
}

// deriveValidators builds a validator set from account stake: each account's balance counts toward
// its ValidatorNode, and a node whose total stake meets the threshold becomes a validator with
// voting power equal to the floor of that stake. voting power = delegated
// amount — as a pure function of the committed accounts, so admission and removal follow the state.
//
// Immutability note: the stake accumulators are FRESH big.Rats (the first delegation is copied with
// Set, later ones Add into the copy) — an account's Amount is never mutated in place. The snapshot
// fast path (Node.Snapshot) shares Amount pointers with committed state on the strength of this
// package-wide treat-amounts-as-immutable invariant; an "optimization" reusing account Amounts as
// accumulators here would silently corrupt served snapshots.
func deriveValidators(accounts []ledger.Account, threshold *big.Rat) *consensus.ValidatorSet {
	stake := make(map[string]*big.Rat)
	for _, a := range accounts {
		if a.Amount == nil || a.ValidatorNode == "" {
			continue
		}
		if cur, ok := stake[a.ValidatorNode]; ok {
			cur.Add(cur, a.Amount)
		} else {
			stake[a.ValidatorNode] = new(big.Rat).Set(a.Amount)
		}
	}
	var vals []consensus.Validator
	for node, s := range stake {
		if threshold != nil && s.Cmp(threshold) < 0 {
			continue
		}
		// floor(stake), saturating: raw Int64() on a stake past 2^63 wraps (negative → validator
		// silently dropped; small-positive → corrupted quorum math). Power beyond the cap counts as
		// the cap — NewValidatorSet enforces the same bound on the fixed-config path.
		power := consensus.MaxValidatorPower
		if floor := new(big.Int).Quo(s.Num(), s.Denom()); floor.IsInt64() {
			power = floor.Int64()
		}
		if power <= 0 {
			continue
		}
		vals = append(vals, consensus.Validator{PublicKey: node, Power: power})
	}
	return consensus.NewValidatorSet(vals)
}
