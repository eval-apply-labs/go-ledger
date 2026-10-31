package consensus

import "testing"

func TestProposerIsStakeWeightedAndDeterministic(t *testing.T) {
	// Four validators; D holds half the stake (power 3 of total 6).
	validatorSet := NewValidatorSet([]Validator{
		{Name: "A", Power: 1},
		{Name: "B", Power: 1},
		{Name: "C", Power: 1},
		{Name: "D", Power: 3},
	})

	// Deterministic: same (height, round) → same proposer.
	for i := 0; i < 100; i++ {
		if validatorSet.ProposerAt(10, 2) != validatorSet.ProposerAt(10, 2) {
			t.Fatal("proposer selection is not deterministic")
		}
	}

	// Stake-weighted: D should propose ~half the time; nobody starves.
	counts := map[string]int{}
	for height := int64(0); height < 400; height++ {
		for round := int32(0); round < 30; round++ {
			counts[validatorSet.ProposerAt(height, round)]++
		}
	}
	total := 0
	for _, count := range counts {
		total += count
	}
	if share := float64(counts["D"]) / float64(total); share < 0.42 || share > 0.58 {
		t.Fatalf("D holds half the stake but proposed %.1f%% (want ~50%%)", share*100)
	}
	for _, name := range []string{"A", "B", "C", "D"} {
		if counts[name] == 0 {
			t.Fatalf("validator %s never proposed (starvation)", name)
		}
	}
}
