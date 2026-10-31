package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review, companion to ledger/poc_digest_nil_amount_test.go): a checkpoint
// whose committed state holds a zero-balance account verifies just as well when the serving peer
// rewrites that account's Amount to null, because the ledger digest encodes nil and 0 identically.
// verifyCheckpoint (commit + digest + anchor) accepts it and fastForward adopts the accounts as-is.
// Asserts the tampered checkpoint is rejected; FAILS on the current code.
func TestPoCCheckpointWithNilAmountVerifies(t *testing.T) {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	A := a.PublicKeyBase64()
	set := []consensus.Validator{{PublicKey: A, Power: 1}}
	genesis := []ledger.Account{{PublicKey: A, Amount: rat("1000"), ValidatorNode: A}}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a,
		Validators: consensus.NewValidatorSet(set), TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))

	// Committed state with a zero-balance (e.g. reassigned-to-zero) named account.
	committed := []ledger.Account{
		{PublicKey: A, Amount: rat("1000"), ValidatorNode: A},
		{PublicKey: "coop-key", Name: "coop", Amount: rat("0")},
	}
	digest := (ledger.LedgerState{Accounts: committed}).Digest()
	commit := buildCommit(t, 7, digest, set, map[string]*signing.Signer{A: a})

	served := append([]ledger.Account(nil), committed...)
	served[1].Amount = nil // the peer serves {"Amount": null}
	cp := Checkpoint{Accounts: served, Commit: commit}
	if nd.verifyCheckpoint(cp, genesis) {
		t.Fatal("a checkpoint whose account set differs from the committed one (Amount null vs 0) verified and would be adopted by fastForward")
	}
}
