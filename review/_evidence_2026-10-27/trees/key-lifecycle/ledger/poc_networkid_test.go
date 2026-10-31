package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (KEY-2): a signature binds only the network-id STRING, never a genesis/chain digest. The
// network id defaults to "village" in cmd/demo, the operator example config, the wallet, and the
// user manual, so two independent deployments routinely share it. Here one validator's
// reassignment-vote, signed on network "village", is a VALID ratifying vote on a COMPLETELY
// DIFFERENT deployment (different genesis accounts) — because the only things the vote binds are
// (account, prevAmount, newAmount, sequence, networkID), all of which two fresh "village" networks
// share. Governance votes, parameter votes and payments are all replayable across any same-id
// deployment where the validator exists on both sides. Binding the id to the genesis digest (or
// warning against reusing the default) is the fix.
func TestPoCGovernanceVoteReplaysAcrossSameNamedNetworks(t *testing.T) {
	const networkID = "village" // the universal default
	scheme := signing.Ed25519{}

	// A validator key that (as happens with a co-located operator) is a validator on two separate
	// "village" deployments stood up from the same template.
	val, _ := signing.Generate(scheme, networkID)
	valKey := val.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: valKey, Power: 1}}) // quorum 1

	// Deployment A: the validator legitimately votes to grant "coop" 500 on network A.
	coop := "coop-account-key"
	accountsA := []Account{
		{PublicKey: valKey, Amount: big.NewRat(1000, 1), ValidatorNode: valKey},
		{PublicKey: coop, Amount: big.NewRat(10, 1)},
	}
	prev, next := big.NewRat(10, 1), big.NewRat(500, 1)
	vote, _ := SignReassignmentVote(val, coop, prev, next, 0)
	reassign := Reassignment{Account: coop, PreviousAmount: prev, NewAmount: next, Sequence: 0,
		Votes: []ReassignmentVote{vote}}

	if !reassign.Valid(scheme, networkID, vs, accountsA, 0) {
		t.Fatal("sanity: the vote should ratify on its own network A")
	}

	// Deployment B: a DIFFERENT network — different genesis, different balances, its own history —
	// that merely reused the default id "village" and happens to have a "coop" account also at 10.
	accountsB := []Account{
		{PublicKey: valKey, Amount: big.NewRat(7, 1), ValidatorNode: valKey},
		{PublicKey: coop, Amount: big.NewRat(10, 1)},
		{PublicKey: "unrelated-B-only-account", Amount: big.NewRat(99, 1)},
	}
	// The SAME certificate, byte for byte, replayed by anyone onto network B:
	if reassign.Valid(scheme, networkID, vs, accountsB, 0) {
		t.Logf("CONFIRMED: a governance certificate signed on one deployment is Valid on a DIFFERENT deployment sharing the default network id — no genesis binding")
	} else {
		t.Fatal("expected the cross-deployment replay to succeed, demonstrating the missing genesis binding")
	}

	// Belt and braces: an ordinary payment signed on A verifies on B too (same id, same key).
	p := Payment{PublicKeyFrom: valKey, PublicKeyTo: coop, Amount: big.NewRat(5, 1), BlockID: 1}
	p, _ = SignPayment(val, p)
	if !VerifyPayment(scheme, networkID, p) {
		t.Fatal("payment should verify on its own network")
	}
	// VerifyPayment takes only (scheme, networkID, p): nothing ties it to a specific genesis, so the
	// identical call on network B (same id) is the same successful verification.
	t.Logf("CONFIRMED: VerifyPayment binds only the id string; the same signed payment verifies on any 'village' network the payer has an account on")
}
