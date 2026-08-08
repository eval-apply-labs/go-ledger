package consensus

import (
	"encoding/binary"
	"hash/fnv"
	"sort"
)

// Validator is a consensus participant with a voting power equal to its delegated stake.
type Validator struct {
	PublicKey string // base64
	Power     int64  // voting power (delegated stake, in whole units)
}

// MaxValidatorPower caps a single validator's voting power: power SATURATES here instead of
// overflowing. Stake beyond 2^40 whole units still gives the largest possible voice, and every
// power-arithmetic path stays far inside int64 — vs.total, the 2*total in Quorum, the uint64(total)
// modulus in ProposerAt, and the int64 tallies in voteset/commit/governance — for any realistic set
// (overflowing the total would need > 2^22 validators AT the cap). The alternatives are worse:
// truncating a big stake with Int64() can wrap NEGATIVE and silently drop the validator (a liveness
// stall if it held real power), or wrap small-positive and corrupt the quorum denominators that
// safety depends on; rejecting the validator outright is the same silent drop. Enforced in
// NewValidatorSet, the single choke point both the fixed-config and stake-derived paths go through.
const MaxValidatorPower int64 = 1 << 40

// ValidatorSet is an immutable, deduplicated set of validators with positive power.
type ValidatorSet struct {
	validators []Validator
	byKey      map[string]int64
	total      int64
}

// NewValidatorSet builds a ValidatorSet, ignoring non-positive-power and duplicate entries.
func NewValidatorSet(vals []Validator) *ValidatorSet {
	vs := &ValidatorSet{byKey: make(map[string]int64, len(vals))}
	for _, v := range vals {
		if v.Power <= 0 {
			continue
		}
		if v.Power > MaxValidatorPower {
			v.Power = MaxValidatorPower // saturate, never overflow — see MaxValidatorPower
		}
		if _, dup := vs.byKey[v.PublicKey]; dup {
			continue
		}
		vs.byKey[v.PublicKey] = v.Power
		vs.validators = append(vs.validators, v)
		vs.total += v.Power
	}
	sort.Slice(vs.validators, func(i, j int) bool {
		return vs.validators[i].PublicKey < vs.validators[j].PublicKey
	})
	return vs
}

// TotalPower is the sum of all validators' power.
func (vs *ValidatorSet) TotalPower() int64 { return vs.total }

// Quorum is the least power constituting a strict > 2/3 supermajority: floor(2*total/3) + 1.
// Requiring more than two-thirds is what makes any two quorums intersect in an honest
// validator, so two conflicting blocks cannot both reach quorum while <= 1/3 of power is
// Byzantine. (A total/2 rule has no such intersection guarantee.)
func (vs *ValidatorSet) Quorum() int64 { return (2*vs.total)/3 + 1 }

// SkipThreshold is the least power that guarantees at least one honest validator: floor(total/3)+1
// (strictly more than a third). Seeing this much voting power from a round ahead of ours means an
// honest node has already advanced there, so it is safe to skip forward to catch up.
func (vs *ValidatorSet) SkipThreshold() int64 { return vs.total/3 + 1 }

// Power returns a validator's power, or 0 if it is not in the set.
func (vs *ValidatorSet) Power(pubKey string) int64 { return vs.byKey[pubKey] }

// ProposerAt returns the validator scheduled to propose at (height, round), chosen deterministically
// and weighted by voting power — a validator with twice the stake proposes roughly twice as often.
// It is a pure function of (height, round, set), so every node agrees. Selection walks the
// power-ordered cumulative weights at a well-mixed seed in [0, totalPower).
func (vs *ValidatorSet) ProposerAt(height int64, round int32) string {
	if vs.total <= 0 || len(vs.validators) == 0 {
		return ""
	}
	// Hash (height, round) to a seed uniform in [0, total) — FNV-1a distributes well for any count.
	var buf [12]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(height))
	binary.BigEndian.PutUint32(buf[8:12], uint32(round))
	h := fnv.New64a()
	_, _ = h.Write(buf[:])
	seed := int64(h.Sum64() % uint64(vs.total))
	var cum int64
	for _, v := range vs.validators { // validators is sorted → deterministic across nodes
		cum += v.Power
		if seed < cum {
			return v.PublicKey
		}
	}
	return vs.validators[len(vs.validators)-1].PublicKey
}

// Contains reports whether pubKey is a validator in the set.
func (vs *ValidatorSet) Contains(pubKey string) bool {
	_, ok := vs.byKey[pubKey]
	return ok
}

// Validators returns a copy of the sorted validator list.
func (vs *ValidatorSet) Validators() []Validator {
	return append([]Validator(nil), vs.validators...)
}
