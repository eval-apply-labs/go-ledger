package node

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// C2 (external review): a delegated stake past 2^63 must saturate at MaxValidatorPower — raw
// Int64() truncation would wrap negative (the whale silently dropped: a liveness stall if it held
// >⅓ of power) or small-positive (corrupted quorum math). Reachable in principle via genesis or a
// ⅔-vote reassignment minting an enormous balance.
func TestDeriveValidatorsSaturatesHugeStake(t *testing.T) {
	huge := new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 100)) // 2^100 whole units
	accounts := []ledger.Account{
		{PublicKey: "W", Amount: huge, ValidatorNode: "W"},
		{PublicKey: "S", Amount: rat("5"), ValidatorNode: "S"},
	}
	vs := deriveValidators(accounts, nil)
	if got := vs.Power("W"); got != consensus.MaxValidatorPower {
		t.Fatalf("whale power = %d, want the saturation cap %d", got, consensus.MaxValidatorPower)
	}
	if got := vs.Power("S"); got != 5 {
		t.Fatalf("small validator power = %d, want 5 (unaffected by the whale)", got)
	}
	if vs.TotalPower() != consensus.MaxValidatorPower+5 {
		t.Fatalf("total = %d, want cap+5", vs.TotalPower())
	}
	if vs.Quorum() <= 0 || vs.ProposerAt(1, 0) == "" {
		t.Fatal("quorum and proposer selection must stay well-formed on a saturated set")
	}
}
