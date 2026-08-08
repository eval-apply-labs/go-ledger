package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// End to end: a signed account update submitted to a running node flows through the update pool into
// a block and registers the name in committed state — the registry, running on the payment engine.
func TestNodeRegistersNameViaUpdate(t *testing.T) {
	aliceSigner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := aliceSigner.PublicKeyBase64()
	validatorSet := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}}) // quorum 1
	genesisAccounts := []ledger.Account{{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey}}

	validatorNode := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: aliceSigner, Validators: validatorSet,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 40 * time.Millisecond,
		Genesis: genesisAccounts,
	}, NewHTTPTransport(nil))
	validatorNode.Start()
	defer validatorNode.Stop()

	currentHeight := validatorNode.Snapshot().Height
	nameRegistration, _ := ledger.SignAccountUpdate(aliceSigner, ledger.AccountUpdate{
		From: aliceKey, Name: "alice", Description: "the founder", BlockID: currentHeight + 4,
	})
	validatorNode.SubmitUpdate(nameRegistration)

	waitFor(t, 5*time.Second, func() bool {
		for _, account := range validatorNode.Snapshot().Accounts {
			if account.PublicKey == aliceKey && account.Name == "alice" {
				return true
			}
		}
		return false
	}, "the name was not registered in committed state")
}
