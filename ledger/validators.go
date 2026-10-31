package ledger

import (
	"math/big"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Chain is the fixed facts of one chain: the signature scheme, the effective network id every signature
// binds (NetworkID), the genesis hash block 1 builds on (GenesisHash), and how the validator set follows
// from the state.
type Chain struct {
	Scheme        signing.Scheme
	NetworkID     string
	GenesisHash   [32]byte // block 1's PreviousBlockHash; tests may leave it zero
	ValidatorRule ValidatorRule
}

// ValidatorRule says how the validator set follows from the state: a static list of names and powers,
// or, when that list is empty, stake (dynamic admission).
type ValidatorRule struct {
	StaticValidators []StaticValidator // static mode: names and powers; empty in dynamic mode
	Threshold        *big.Rat          // dynamic mode: the stake a name needs to be seated (nil: any)
}

// StaticValidator is one seat of a static validator set.
type StaticValidator struct {
	Name  string
	Power int64
}

// SetFor returns the validator set that governs the block built on state, with each seated name's
// current key from the state. It is a pure function of the committed state, so every node computes the
// same set, and a frozen name is never seated.
//
// Static mode: each static validator whose account exists and is not frozen, with the power the
// genesis file gives it. Dynamic mode: each account that is not frozen backs its Delegate with its
// balance (a frozen balance backs no one), and a name whose total reaches the threshold, that exists
// and is not frozen, is seated with power equal to the floor of that total. A newly funded name joins
// at the next block.
//
// Immutability note: the stake accumulators are FRESH big.Rats (the first delegation is copied with
// Set, later ones Add into the copy) — an account's Amount is never mutated in place. The snapshot
// fast path (node.Snapshot) shares Amount pointers with committed state on the strength of this
// package-wide treat-amounts-as-immutable invariant; an "optimization" reusing account Amounts as
// accumulators here would silently corrupt served snapshots.
func (validatorRule ValidatorRule) SetFor(state LedgerState) *consensus.ValidatorSet {
	var validators []consensus.Validator
	if len(validatorRule.StaticValidators) > 0 {
		for _, staticValidator := range validatorRule.StaticValidators {
			if account, found := FindAccount(state.Accounts, staticValidator.Name); found && !account.Frozen {
				validators = append(validators, consensus.Validator{Name: account.Name, PublicKey: account.PublicKey, Power: staticValidator.Power})
			}
		}
		return consensus.NewValidatorSet(validators)
	}
	stake := make(map[string]*big.Rat)
	for _, account := range state.Accounts {
		if account.Frozen || account.Delegate == "" || account.Amount == nil {
			continue
		}
		if total, found := stake[account.Delegate]; found {
			total.Add(total, account.Amount)
		} else {
			stake[account.Delegate] = new(big.Rat).Set(account.Amount)
		}
	}
	for name, nameStake := range stake {
		if validatorRule.Threshold != nil && nameStake.Cmp(validatorRule.Threshold) < 0 {
			continue
		}
		account, found := FindAccount(state.Accounts, name)
		if !found || account.Frozen {
			continue
		}
		// floor(stake), saturating: raw Int64() on a stake past 2^63 wraps (negative → validator
		// silently dropped; small-positive → corrupted quorum math). Power beyond the cap counts as
		// the cap — NewValidatorSet enforces the same bound for the static path.
		power := consensus.MaxValidatorPower
		if floor := new(big.Int).Quo(nameStake.Num(), nameStake.Denom()); floor.IsInt64() {
			power = floor.Int64()
		}
		validators = append(validators, consensus.Validator{Name: name, PublicKey: account.PublicKey, Power: power})
	}
	return consensus.NewValidatorSet(validators) // drops a power of 0, and sorts by name
}

// powerIgnoringFreezes is the set validatorRule derives from state as if no account were frozen (spec
// 5.2): a frozen name keeps the power of its seat, and a frozen balance still backs its delegate. The
// one-third cap counts power by it (withinOneThirdCap), so neither a freeze nor R10 changes the cap.
func powerIgnoringFreezes(validatorRule ValidatorRule, state LedgerState) *consensus.ValidatorSet {
	unfrozen := state
	unfrozen.Accounts = make([]Account, len(state.Accounts))
	for index, account := range state.Accounts {
		account.Frozen = false
		unfrozen.Accounts[index] = account
	}
	return validatorRule.SetFor(unfrozen)
}

// withinOneThirdCap is rule 22 (spec 5.8): with N the names frozen before the block (in
// previousAccounts) and the names in frozenByTheBlock, 3 × P(N) < P(V ∪ N), where V is validators, the
// block's set, and P sums the power powerIgnoringFreezes gives the names. So evidence never brings the
// frozen names to a third or more of the power of the seated and frozen names together.
func withinOneThirdCap(powerIgnoringFreezes, validators *consensus.ValidatorSet, previousAccounts []Account, frozenByTheBlock map[string]bool) bool {
	var frozenPower, totalPower int64
	for _, validator := range powerIgnoringFreezes.Validators() {
		account, _ := FindAccount(previousAccounts, validator.Name)
		if account.Frozen || frozenByTheBlock[validator.Name] {
			frozenPower += validator.Power
			totalPower += validator.Power
		} else if validators.Contains(validator.Name) {
			totalPower += validator.Power
		}
	}
	return 3*frozenPower < totalPower
}
