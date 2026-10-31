package node

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): README/consensus.md claim a slash "removes the double-signer's
// account (confiscates stake, ejects from a dynamic set)". applySlashes deletes only the offender's
// OWN account; accounts that DELEGATE to the offender (ValidatorNode == offender) are untouched, and
// deriveValidators sums stake by ValidatorNode, so in dynamic mode the equivocator stays a validator
// with its delegated power. This test asserts the documented property and FAILS on the current code.
func TestPoCSlashDoesNotEjectDelegatedValidator(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	offenderSigner, _ := signing.Generate(scheme, network)
	honestSigner, _ := signing.Generate(scheme, network)
	V, H := offenderSigner.PublicKeyBase64(), honestSigner.PublicKeyBase64()

	accounts := []ledger.Account{
		{PublicKey: V, Amount: big.NewRat(10, 1), ValidatorNode: V},     // the validator's own small balance
		{PublicKey: "D1", Amount: big.NewRat(400, 1), ValidatorNode: V}, // delegators ("bank customers")
		{PublicKey: "D2", Amount: big.NewRat(400, 1), ValidatorNode: V},
		{PublicKey: H, Amount: big.NewRat(2000, 1), ValidatorNode: H},
	}
	threshold := big.NewRat(100, 1)
	before := deriveValidators(accounts, threshold)
	if !before.Contains(V) {
		t.Fatal("setup: V should be a validator before the slash")
	}

	var dX, dY [32]byte
	dX[0], dY[0] = 0x11, 0x22
	vX, _ := consensus.SignVote(offenderSigner, consensus.Vote{Height: 3, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: V})
	vY, _ := consensus.SignVote(offenderSigner, consensus.Vote{Height: 3, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: V})
	ev := consensus.Evidence{Validator: V, A: vX, B: vY}

	prev := ledger.LedgerState{Accounts: accounts, Parameters: ledger.LedgerParameters{TransactionCost: big.NewRat(1, 10)}}
	blk := ledger.ProposeBlock(scheme, network, 5, prev, ledger.BlockContent{Evidence: []consensus.Evidence{ev}}, before)
	next, ok := ledger.ApplyBlock(scheme, network, prev, blk, before)
	if !ok || len(blk.Evidence) != 1 {
		t.Fatal("setup: the slash block should apply")
	}
	after := deriveValidators(next.Accounts, threshold)
	t.Logf("V power before slash = %d, after slash = %d (total after = %d)", before.Power(V), after.Power(V), after.TotalPower())
	if after.Contains(V) {
		t.Fatalf("documented: a slash ejects the equivocator from a dynamic set; observed: V is still a validator with power %d (only its own 10-unit account was removed)", after.Power(V))
	}
}
