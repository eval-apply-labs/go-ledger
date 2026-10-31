package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (critic, round 2): the account-update pool admits ANY validly self-signed update — no account,
// no balance, no fee, no BlockID upper bound — and evicts only BlockID < height. A fresh keypair with
// no account fills it with far-future updates once; they survive every commit and every honest
// update (name registration, re-delegation, peer-DNS address) is refused from then on.
func TestPoCCriticUpdatePoolControl(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	v, _ := signing.Generate(scheme, network)
	alice, _ := signing.Generate(scheme, network)
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}})
	const poolCap = 50 // stands in for maxMempool (default 10000: same mechanism, 10000 tiny messages)
	nd := New(Config{
		NetworkID: network, Scheme: scheme, Signer: v, Validators: vs, TransactionCost: rat("0.1"),
		Timeout: 200 * time.Millisecond, MinBlockInterval: 10 * time.Millisecond, MaxMempool: poolCap,
		Genesis: []ledger.Account{{PublicKey: alice.PublicKeyBase64(), Amount: rat("10"), ValidatorNode: v.PublicKeyBase64()}},
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()

	stranger, _ := signing.Generate(scheme, network) // NOT an account on the ledger
	for i := 0; i < 0; i++ {
		u, _ := ledger.SignAccountUpdate(stranger, ledger.AccountUpdate{From: stranger.PublicKeyBase64(), Description: "x", BlockID: 1<<40 + int64(i)})
		nd.SubmitUpdate(u)
	}
	// Let a few blocks commit (each commit runs updates.dropBelow).
	deadline := time.Now().Add(5 * time.Second)
	for nd.Snapshot().Height < 6 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h := nd.Snapshot().Height
	reg, _ := ledger.SignAccountUpdate(alice, ledger.AccountUpdate{From: alice.PublicKeyBase64(), Name: "alice", BlockID: h + 2})
	nd.SubmitUpdate(reg)
	deadline = time.Now().Add(3 * time.Second)
	for nd.Snapshot().Height < h+4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snap := nd.Snapshot()
	named := false
	for _, a := range snap.Accounts {
		if a.PublicKey == alice.PublicKeyBase64() && a.Name == "alice" {
			named = true
		}
	}
	t.Logf("height now %d; alice's registration for block %d committed: %v", snap.Height, h+2, named)
	if !named {
		t.Errorf("SQUAT: %d far-future updates from a key with no account filled the update pool across %d commits; alice's valid name registration was refused", poolCap, snap.Height-1)
	}
}
