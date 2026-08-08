package consensus

import (
	"math"
	"testing"
)

// C2 (external review): a validator's power saturates at MaxValidatorPower instead of overflowing
// the int64 quorum arithmetic. With raw MaxInt64 powers the total would wrap negative, Quorum's
// 2*total would overflow, and ProposerAt's uint64(total) modulus would explode — the saturated set
// must instead stay well-formed end to end.
func TestValidatorSetSaturatesPower(t *testing.T) {
	vs := NewValidatorSet([]Validator{
		{PublicKey: "A", Power: math.MaxInt64},
		{PublicKey: "B", Power: math.MaxInt64},
		{PublicKey: "C", Power: 7},
	})
	if got := vs.Power("A"); got != MaxValidatorPower {
		t.Fatalf("power A = %d, want the MaxValidatorPower cap %d", got, MaxValidatorPower)
	}
	wantTotal := 2*MaxValidatorPower + 7
	if vs.TotalPower() != wantTotal {
		t.Fatalf("total = %d, want %d (no overflow)", vs.TotalPower(), wantTotal)
	}
	if q := vs.Quorum(); q != (2*wantTotal)/3+1 {
		t.Fatalf("quorum = %d, want %d", q, (2*wantTotal)/3+1)
	}
	if p := vs.ProposerAt(1, 0); p == "" {
		t.Fatal("proposer selection must work on a saturated set")
	}
}
