package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// onProposal returns whether a proposal is worth RELAYING. It must relay a proposal only on its
// first sight and never re-relay a duplicate: the dedup key is Block.StateDigest, which the proposal
// signature covers but the block BODY does not, so a duplicate can carry a swapped, unverified body
// under a copied digest — and the proposer's own broadcast bypasses relay() dedup, so it would be
// the one node to re-flood that body. This is the pay-by-name/size-cap-range regression guard.
func TestOnProposalRelaysFirstSightOnlyNeverDuplicates(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	proposer, _ := signing.Generate(scheme, network) // the sole validator → round-0 proposer
	observer, _ := signing.Generate(scheme, network) // the node under test: NOT a validator
	attacker, _ := signing.Generate(scheme, network) // holds no validator slot

	nd := New(Config{
		NetworkID: network, Scheme: scheme, Signer: observer,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: proposer.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second,
		Genesis: []ledger.Account{{PublicKey: proposer.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: proposer.PublicKeyBase64()}},
	}, NewHTTPTransport(nil))
	// Deliberately NOT Start()ed: driving the state machine directly makes onProposal's relay verdict
	// observable without racing the run loop.
	nd.startHeight()
	if got := nd.driver.Proposer(0); got != proposer.PublicKeyBase64() {
		t.Fatalf("test premise: round-0 proposer should be the sole validator, got %q", got)
	}

	// A genuine proposal by the round proposer: authentic and first-seen → relayed.
	var digest [32]byte
	copy(digest[:], "genuine-round-0-committed-digest!") // 32 bytes; the body is irrelevant to relay
	genuine := ProposalMsg{Height: 1, Round: 0, ValidRound: -1, Block: ledger.Block{Height: 1, StateDigest: digest}}
	if err := SignProposal(proposer, &genuine); err != nil {
		t.Fatal(err)
	}
	if !nd.onProposal(genuine) {
		t.Fatal("a genuine proposal on its first sight must be relayed")
	}

	// The same proposal again (a peer's re-gossip): already held → NOT re-relayed.
	if nd.onProposal(genuine) {
		t.Fatal("a duplicate of an already-held proposal must not be re-relayed")
	}

	// Forgery 1 — wrong proposer: an attacker signs a proposal copying the genuine digest. Dropped.
	forgedWrongSigner := ProposalMsg{Height: 1, Round: 0, ValidRound: -1,
		Block: ledger.Block{Height: 1, StateDigest: digest, Payments: make([]ledger.Payment, 512)}}
	if err := SignProposal(attacker, &forgedWrongSigner); err != nil {
		t.Fatal(err)
	}
	if nd.onProposal(forgedWrongSigner) {
		t.Fatal("a proposal not signed by the round proposer must not be relayed")
	}

	// Forgery 2 — the sharp one: replay the genuine proposer/signature (both public, seen on the
	// wire) with a SWAPPED block body under the same digest. The signature verifies (it only covers
	// the digest), so the verify gate alone would pass it; the held-duplicate guard is what stops it.
	forgedSwappedBody := genuine
	forgedSwappedBody.Block.Payments = make([]ledger.Payment, 512) // body the proposer never signed
	if nd.onProposal(forgedSwappedBody) {
		t.Fatal("a duplicate carrying a swapped body under a copied digest+signature must not be relayed")
	}
}
