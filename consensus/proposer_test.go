package consensus

import "testing"

func TestProposerIsStakeWeightedAndDeterministic(t *testing.T) {
	// Four validators; D holds half the stake (power 3 of total 6).
	vs := NewValidatorSet([]Validator{
		{PublicKey: "A", Power: 1},
		{PublicKey: "B", Power: 1},
		{PublicKey: "C", Power: 1},
		{PublicKey: "D", Power: 3},
	})

	// Deterministic: same (height, round) → same proposer.
	for i := 0; i < 100; i++ {
		if vs.ProposerAt(10, 2) != vs.ProposerAt(10, 2) {
			t.Fatal("proposer selection is not deterministic")
		}
	}

	// Stake-weighted: D should propose ~half the time; nobody starves.
	counts := map[string]int{}
	for h := int64(0); h < 400; h++ {
		for r := int32(0); r < 30; r++ {
			counts[vs.ProposerAt(h, r)]++
		}
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	if share := float64(counts["D"]) / float64(total); share < 0.42 || share > 0.58 {
		t.Fatalf("D holds half the stake but proposed %.1f%% (want ~50%%)", share*100)
	}
	for _, k := range []string{"A", "B", "C", "D"} {
		if counts[k] == 0 {
			t.Fatalf("validator %s never proposed (starvation)", k)
		}
	}
}
