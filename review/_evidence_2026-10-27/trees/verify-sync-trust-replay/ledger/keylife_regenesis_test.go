package ledger

// Key-lifecycle review PoC (network-id binding, re-genesis angle). Nothing in any signed preimage
// binds a genesis/chain identity — only the free-form network id string (default "village" in the
// demo, the operator example, the wallet and the user manual). The documented migration/upgrade path
// (docs/operating-a-network.md "Migrating the ledger", and the findings.md 2026-07-31 "coordinated
// re-genesis, not a rolling upgrade" rule) captures the agreed state and restarts it as a FRESH
// genesis: heights restart at 1 and the governance sequence at 0. When that re-genesis keeps the same
// network id and the same keys (true for every re-genesis that is NOT a scheme migration — a plain
// chain restart, or a software upgrade whose field change does not touch signFields), every payment
// and governance certificate signed on the old chain verifies and applies again on the new one.
//
// These tests assert the SAFE behaviour, so they FAIL while the weakness exists, printing the bad
// outcome.

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestKeyLifecycleReGenesisReplaysOldPayment(t *testing.T) {
	scheme, network := signing.Ed25519{}, "village" // the docs' / demo's / wallet's default id
	alice, _ := signing.Generate(scheme, network)
	bob, _ := signing.Generate(scheme, network)
	A, B := alice.PublicKeyBase64(), bob.PublicKeyBase64()
	cost := big.NewRat(1, 10)

	// Old chain: alice pays bob 40 at block 1.
	oldGenesis := []Account{{PublicKey: A, Name: "alice", Amount: big.NewRat(100, 1), ValidatorNode: A}}
	pay, err := SignPayment(alice, Payment{PublicKeyFrom: A, PublicKeyTo: B, Amount: big.NewRat(40, 1), BlockID: 1})
	if err != nil {
		t.Fatal(err)
	}
	oldBlock := ProposeBlock(scheme, network, 1, st(oldGenesis, cost), BlockContent{Payments: []Payment{pay}})
	oldState, ok := ApplyBlock(scheme, network, st(oldGenesis, cost), oldBlock)
	if !ok || balance(oldState.Accounts, A).Cmp(big.NewRat(60, 1)) != 0 {
		t.Fatalf("setup: old chain did not apply the payment")
	}

	// Documented migration: reseed a fresh genesis from the captured accounts, same id, same keys
	// (only a SCHEME migration re-keys). Heights restart at 1.
	newGenesis := oldState.Accounts
	// Anyone who kept the old payment (any gossip peer / validator / archive) resubmits it.
	replayBlock := ProposeBlock(scheme, network, 1, st(newGenesis, cost), BlockContent{Payments: []Payment{pay}})
	newState, ok := ApplyBlock(scheme, network, st(newGenesis, cost), replayBlock)
	if !ok {
		t.Fatal("setup: replay block did not apply")
	}
	if len(replayBlock.Payments) != 0 || balance(newState.Accounts, A).Cmp(big.NewRat(60, 1)) != 0 {
		t.Errorf("REPLAY: a payment signed on the old chain was re-applied after re-genesis under the same "+
			"network id: alice %s -> %s, bob %s -> %s (replay block carried %d old payment(s))",
			balance(newGenesis, A).FloatString(2), balance(newState.Accounts, A).FloatString(2),
			balance(newGenesis, B).FloatString(2), balance(newState.Accounts, B).FloatString(2),
			len(replayBlock.Payments))
	}
}

func TestKeyLifecycleReGenesisReplaysOldReassignment(t *testing.T) {
	scheme, network := signing.Ed25519{}, "village"
	validator, _ := signing.Generate(scheme, network)
	V := validator.PublicKeyBase64()
	newcomer, _ := signing.Generate(scheme, network)
	N := newcomer.PublicKeyBase64()
	set := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: V, Power: 1}}) // quorum 1

	// Old chain, governance sequence 0: the community grants a newcomer 100 (prev 0 -> 100).
	grantVote, _ := SignReassignmentVote(validator, N, new(big.Rat), big.NewRat(100, 1), 0)
	grant := Reassignment{Account: N, PreviousAmount: new(big.Rat), NewAmount: big.NewRat(100, 1),
		Sequence: 0, Votes: []ReassignmentVote{grantVote}}
	oldGenesis := []Account{{PublicKey: V, Amount: big.NewRat(1000, 1), ValidatorNode: V}}
	if !grant.Valid(scheme, network, set, oldGenesis, 0) {
		t.Fatal("setup: the grant should ratify on the old chain at sequence 0")
	}

	// Re-genesis (same id, same keys): the reseed genesis does NOT carry the newcomer's granted
	// balance (say the migration captured an earlier height, or the newcomer is re-created fresh), so
	// the fresh chain is again at sequence 0 with N at 0 — exactly the certificate's precondition.
	newGenesis := []Account{{PublicKey: V, Amount: big.NewRat(1000, 1), ValidatorNode: V}}
	if grant.Valid(scheme, network, set, newGenesis, 0) {
		t.Errorf("REPLAY: a ⅔ grant certificate from the old chain re-ratifies on the re-genesis chain " +
			"(same network id, sequence reset to 0) — the newcomer can be re-minted 100 with no new vote")
	}
}

// A second, deployment-coexistence framing of the same root cause: two independent "village"
// networks (the universal default) — a reassignment certificate signed on one is Valid on the other.
func TestKeyLifecycleSameIdCrossDeploymentReplay(t *testing.T) {
	const network = "village"
	scheme := signing.Ed25519{}
	val, _ := signing.Generate(scheme, network)
	valKey := val.PublicKeyBase64()
	set := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: valKey, Power: 1}})

	coop := "coop-account-key"
	prev, next := big.NewRat(10, 1), big.NewRat(500, 1)
	vote, _ := SignReassignmentVote(val, coop, prev, next, 0)
	cert := Reassignment{Account: coop, PreviousAmount: prev, NewAmount: next, Sequence: 0,
		Votes: []ReassignmentVote{vote}}

	accountsA := []Account{{PublicKey: valKey, Amount: big.NewRat(1000, 1), ValidatorNode: valKey}, {PublicKey: coop, Amount: big.NewRat(10, 1)}}
	accountsB := []Account{{PublicKey: valKey, Amount: big.NewRat(7, 1), ValidatorNode: valKey}, {PublicKey: coop, Amount: big.NewRat(10, 1)}, {PublicKey: "B-only", Amount: big.NewRat(99, 1)}}
	if !cert.Valid(scheme, network, set, accountsA, 0) {
		t.Fatal("setup: should ratify on network A")
	}
	if cert.Valid(scheme, network, set, accountsB, 0) {
		t.Errorf("REPLAY: a governance certificate signed on deployment A is Valid on a DIFFERENT " +
			"deployment B sharing the default network id — signatures bind no genesis identity")
	}
}
