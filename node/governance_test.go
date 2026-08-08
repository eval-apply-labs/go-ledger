package node

import (
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// End to end: a validator's parameter-change vote, once it reaches the quorum, is bundled into a
// block and amends the committed parameters. Here a lone validator (quorum 1) raises the fee.
func TestNodeEnactsParameterChangeByVote(t *testing.T) {
	aliceSigner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := aliceSigner.PublicKeyBase64()
	validatorSet := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}}) // quorum 1
	genesis := []ledger.Account{{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey}}

	validatorNode := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: aliceSigner, Validators: validatorSet,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 30 * time.Millisecond,
		Genesis: genesis,
	}, NewHTTPTransport(nil))
	validatorNode.Start()
	defer validatorNode.Stop()

	waitFor(t, 2*time.Second, func() bool { return validatorNode.Snapshot().Height >= 2 }, "network did not start")

	// Vote to raise the transaction fee to 0.5, keeping the (valid) timing profile the node booted with.
	snap := validatorNode.Snapshot()
	currentParameters := snap.Parameters
	sequence := snap.GovernanceSequence
	newParameters := currentParameters
	newParameters.TransactionCost = rat("0.5")
	vote, _ := ledger.SignParameterVote(aliceSigner, newParameters, currentParameters.Digest(), sequence)
	validatorNode.SubmitParameterVote(ledger.ParameterChange{
		NewParameters:            newParameters,
		PreviousParametersDigest: currentParameters.Digest(),
		Sequence:                 sequence,
		Votes:                    []ledger.ParameterVote{vote},
	})

	waitFor(t, 4*time.Second, func() bool {
		cost := validatorNode.Snapshot().Parameters.TransactionCost
		return cost != nil && cost.Cmp(rat("0.5")) == 0
	}, "the quorum-approved parameter change was not enacted in committed state")
}
