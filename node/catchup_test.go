package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A running node that cannot make progress alone (a straggler that missed a commit window) must
// fast-forward when handed a peer's later verified checkpoint, instead of staying stuck. Because it
// is 1 of a 2-quorum set, height 6 is unreachable except via catch-up — so reaching it proves it.
func TestStragglerCatchUp(t *testing.T) {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	A, B := a.PublicKeyBase64(), b.PublicKeyBase64()
	set := []consensus.Validator{{PublicKey: A, Power: 1}, {PublicKey: B, Power: 1}} // quorum 2
	vs := consensus.NewValidatorSet(set)
	genesis := []ledger.Account{{PublicKey: A, Name: "founder", Amount: rat("1000"), ValidatorNode: A}}

	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: b, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 30 * time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()

	// A peer's checkpoint at height 5 — a real A+B commit over the resulting state.
	state5 := []ledger.Account{
		{PublicKey: A, Amount: rat("900"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("50"), ValidatorNode: B},
	}
	cp := Checkpoint{
		Accounts:   state5,
		Commit:     buildCommit(t, 5, (ledger.LedgerState{Accounts: state5}).Digest(), set, map[string]*signing.Signer{A: a, B: b}),
		Validators: set,
	}
	nd.enqueue(event{kind: evCatchUp, checkpoint: &cp})

	waitFor(t, 3*time.Second, func() bool { return nd.Snapshot().Height == 6 },
		"straggler did not catch up to height 6 after a later checkpoint")
}
