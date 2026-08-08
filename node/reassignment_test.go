package node

import (
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// End to end: a validator's balance-reassignment vote, once it reaches the quorum, is bundled into a
// block and overrides the account's committed balance. A lone validator (quorum 1) grants the co-op.
func TestNodeEnactsReassignmentByVote(t *testing.T) {
	aliceSigner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := aliceSigner.PublicKeyBase64()
	validatorSet := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}}) // quorum 1
	coop := "community-coop-account"
	genesis := []ledger.Account{
		{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey},
		{PublicKey: coop, Name: "coop", Amount: rat("10")},
	}

	validatorNode := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: aliceSigner, Validators: validatorSet,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 30 * time.Millisecond,
		Genesis: genesis,
	}, NewHTTPTransport(nil))
	validatorNode.Start()
	defer validatorNode.Stop()

	waitFor(t, 2*time.Second, func() bool { return validatorNode.Snapshot().Height >= 2 }, "network did not start")

	// Vote to grant the co-op — raise its balance from 10 to 500 (a ⅔-ratified mint).
	previousAmount := rat("10")
	newAmount := rat("500")
	sequence := validatorNode.Snapshot().GovernanceSequence
	vote, _ := ledger.SignReassignmentVote(aliceSigner, coop, previousAmount, newAmount, sequence)
	validatorNode.SubmitReassignmentVote(ledger.Reassignment{
		Account: coop, PreviousAmount: previousAmount, NewAmount: newAmount, Sequence: sequence,
		Votes: []ledger.ReassignmentVote{vote},
	})

	waitFor(t, 4*time.Second, func() bool {
		for _, account := range validatorNode.Snapshot().Accounts {
			if account.PublicKey == coop && account.Amount != nil && account.Amount.Cmp(rat("500")) == 0 {
				return true
			}
		}
		return false
	}, "the quorum-approved reassignment was not enacted in committed state")

	// And the read-only governance view is reachable (used by the admin UI).
	if view := validatorNode.GovernanceView(); view.Quorum != 1 {
		t.Fatalf("governance view quorum = %d, want 1", view.Quorum)
	}
}
