package consensus

import (
	"encoding/binary"
	"hash/fnv"
	"sort"
)

// Validator is a seated name: the name its votes and proposals carry, the name's current key, which
// verifies them, and its voting power.
type Validator struct {
	Name      string // the account name
	PublicKey string // the name's current key (base64)
	Power     int64  // voting power: the genesis power in static mode, the floor of delegated stake in dynamic mode
}

// MaxValidatorPower caps a single validator's voting power: power SATURATES here instead of
// overflowing. Stake beyond 2^40 whole units still gives the largest possible voice, and every
// power-arithmetic path stays far inside int64 — validatorSet.total, the 2*total in Quorum, the
// uint64(total) modulus in ProposerAt, and the int64 tallies in voteset/commit/governance — for any
// realistic set (overflowing the total would need > 2^22 validators AT the cap). The alternatives are
// worse: truncating a big stake with Int64() can wrap NEGATIVE and silently drop the validator (a
// liveness stall if it held real power), or wrap small-positive and corrupt the quorum denominators
// that safety depends on; rejecting the validator outright is the same silent drop. Enforced in
// NewValidatorSet, which both the static (genesis) path and the stake path go through; the stake path
// (ledger's ValidatorRule.SetFor) also caps a stake too large for int64 before it gets there.
const MaxValidatorPower int64 = 1 << 40

// ValidatorSet is an immutable, deduplicated set of validators with positive power, keyed and sorted
// by name.
type ValidatorSet struct {
	validators []Validator
	byName     map[string]Validator
	total      int64
}

// NewValidatorSet builds a ValidatorSet, ignoring non-positive-power entries and every entry after the
// first for a name.
func NewValidatorSet(validators []Validator) *ValidatorSet {
	validatorSet := &ValidatorSet{byName: make(map[string]Validator, len(validators))}
	for _, validator := range validators {
		if validator.Power <= 0 {
			continue
		}
		if validator.Power > MaxValidatorPower {
			validator.Power = MaxValidatorPower // saturate, never overflow — see MaxValidatorPower
		}
		if _, duplicate := validatorSet.byName[validator.Name]; duplicate {
			continue
		}
		validatorSet.byName[validator.Name] = validator
		validatorSet.validators = append(validatorSet.validators, validator)
		validatorSet.total += validator.Power
	}
	sort.Slice(validatorSet.validators, func(i, j int) bool {
		return validatorSet.validators[i].Name < validatorSet.validators[j].Name
	})
	return validatorSet
}

// TotalPower is the sum of all validators' power.
func (validatorSet *ValidatorSet) TotalPower() int64 { return validatorSet.total }

// Quorum is the least power constituting a strict > 2/3 supermajority: floor(2*total/3) + 1.
// Requiring more than two-thirds is what makes any two quorums intersect in an honest
// validator, so two conflicting blocks cannot both reach quorum while <= 1/3 of power is
// Byzantine. (A total/2 rule has no such intersection guarantee.)
func (validatorSet *ValidatorSet) Quorum() int64 { return (2*validatorSet.total)/3 + 1 }

// SkipThreshold is the least power that guarantees at least one honest validator: floor(total/3)+1
// (strictly more than a third). Seeing this much voting power from a round ahead of ours means an
// honest node has already advanced there, so it is safe to skip forward to catch up.
func (validatorSet *ValidatorSet) SkipThreshold() int64 { return validatorSet.total/3 + 1 }

// Power returns a validator's power, or 0 if the name is not in the set.
func (validatorSet *ValidatorSet) Power(name string) int64 { return validatorSet.byName[name].Power }

// PublicKey returns the key the set holds for a name, the one that verifies its votes, or "" if the
// name is not in the set.
func (validatorSet *ValidatorSet) PublicKey(name string) string {
	return validatorSet.byName[name].PublicKey
}

// ProposerAt returns the name scheduled to propose at (height, round), chosen deterministically
// and weighted by voting power — a validator with twice the stake proposes roughly twice as often.
// It is a pure function of (height, round, set), so every node agrees. Selection walks the validators
// in name order, adding up their power, until the sum passes a well-mixed seed in [0, total power).
func (validatorSet *ValidatorSet) ProposerAt(height int64, round int32) string {
	if validatorSet.total <= 0 || len(validatorSet.validators) == 0 {
		return ""
	}
	// Hash (height, round) to a seed uniform in [0, total) — FNV-1a distributes well for any count.
	var buffer [12]byte
	binary.BigEndian.PutUint64(buffer[0:8], uint64(height))
	binary.BigEndian.PutUint32(buffer[8:12], uint32(round))
	hasher := fnv.New64a()
	_, _ = hasher.Write(buffer[:])
	seed := int64(hasher.Sum64() % uint64(validatorSet.total))
	var cumulativePower int64
	for _, validator := range validatorSet.validators { // validators is sorted → deterministic across nodes
		cumulativePower += validator.Power
		if seed < cumulativePower {
			return validator.Name
		}
	}
	return validatorSet.validators[len(validatorSet.validators)-1].Name
}

// Contains reports whether name is a validator in the set.
func (validatorSet *ValidatorSet) Contains(name string) bool {
	_, found := validatorSet.byName[name]
	return found
}

// Validators returns a copy of the sorted validator list.
func (validatorSet *ValidatorSet) Validators() []Validator {
	return append([]Validator(nil), validatorSet.validators...)
}
