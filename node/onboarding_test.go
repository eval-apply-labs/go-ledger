package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// buildCommit assembles a real ⅔ precommit certificate signed by the given set for a digest.
func buildCommit(t *testing.T, height int64, digest [32]byte, set []consensus.Validator, signers map[string]*signing.Signer) consensus.Commit {
	t.Helper()
	var votes []consensus.Vote
	for _, v := range set {
		vote := consensus.Vote{Height: height, Round: 0, Type: consensus.Precommit, BlockDigest: digest, Validator: v.PublicKey}
		signed, err := consensus.SignVote(signers[v.PublicKey], vote)
		if err != nil {
			t.Fatal(err)
		}
		votes = append(votes, signed)
	}
	return consensus.Commit{Height: height, BlockDigest: digest, Votes: votes}
}

// The dynamic-set onboarding refinement: a joiner that only trusts the genesis set must adopt a
// checkpoint whose commit was signed by a LARGER set that grew while it was away (because a
// validator it trusts is among the signers), yet reject one signed only by strangers.
func TestVerifyCheckpointSkippingTrust(t *testing.T) {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	c, _ := signing.Generate(signing.Ed25519{}, "testnet")
	A, B, C := a.PublicKeyBase64(), b.PublicKeyBase64(), c.PublicKeyBase64()

	// Genesis trusts only A (funded over the threshold); dynamic set.
	genesis := []ledger.Account{{PublicKey: A, Name: "founder", Amount: rat("1000"), ValidatorNode: A}}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))

	// A committed state where A, B and C are all validators, with a commit signed by all three.
	grown := []ledger.Account{
		{PublicKey: A, Amount: rat("800"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("500"), ValidatorNode: B},
		{PublicKey: C, Amount: rat("400"), ValidatorNode: C},
	}
	grownSet := []consensus.Validator{{PublicKey: A, Power: 800}, {PublicKey: B, Power: 500}, {PublicKey: C, Power: 400}}
	cp := Checkpoint{
		Accounts:   grown,
		Commit:     buildCommit(t, 5, (ledger.LedgerState{Accounts: grown}).Digest(), grownSet, map[string]*signing.Signer{A: a, B: b, C: c}),
		Validators: grownSet,
	}
	if !nd.verifyCheckpoint(cp, genesis) {
		t.Fatal("joiner should adopt a checkpoint whose grown-set commit its trusted validator A co-signed")
	}

	// A checkpoint signed only by strangers A never vouches for must be rejected.
	x1, _ := signing.Generate(signing.Ed25519{}, "testnet")
	x2, _ := signing.Generate(signing.Ed25519{}, "testnet")
	x3, _ := signing.Generate(signing.Ed25519{}, "testnet")
	X1, X2, X3 := x1.PublicKeyBase64(), x2.PublicKeyBase64(), x3.PublicKeyBase64()
	strangers := []ledger.Account{
		{PublicKey: X1, Amount: rat("1000"), ValidatorNode: X1},
		{PublicKey: X2, Amount: rat("1000"), ValidatorNode: X2},
		{PublicKey: X3, Amount: rat("1000"), ValidatorNode: X3},
	}
	strangerSet := []consensus.Validator{{PublicKey: X1, Power: 1000}, {PublicKey: X2, Power: 1000}, {PublicKey: X3, Power: 1000}}
	scp := Checkpoint{
		Accounts:   strangers,
		Commit:     buildCommit(t, 5, (ledger.LedgerState{Accounts: strangers}).Digest(), strangerSet, map[string]*signing.Signer{X1: x1, X2: x2, X3: x3}),
		Validators: strangerSet,
	}
	if nd.verifyCheckpoint(scp, genesis) {
		t.Fatal("a checkpoint signed only by strangers must be rejected — no trusted validator vouches")
	}
}
