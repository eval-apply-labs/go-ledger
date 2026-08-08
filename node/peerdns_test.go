package node

import (
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// The network is self-describing: a node resolves its gossip peers from the on-chain addresses of
// the current validators (the peer DNS), not from static config — and never resolves itself.
func TestResolvePeersFromOnChainAddresses(t *testing.T) {
	selfSigner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	peerOneSigner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	peerTwoSigner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	selfKey := selfSigner.PublicKeyBase64()
	peerOneKey := peerOneSigner.PublicKeyBase64()
	peerTwoKey := peerTwoSigner.PublicKeyBase64()

	validatorSet := consensus.NewValidatorSet([]consensus.Validator{
		{PublicKey: selfKey, Power: 1}, {PublicKey: peerOneKey, Power: 1}, {PublicKey: peerTwoKey, Power: 1},
	})
	genesisAccounts := []ledger.Account{
		{PublicKey: selfKey, Address: "http://self:8080", Amount: rat("1"), ValidatorNode: selfKey},
		{PublicKey: peerOneKey, Address: "http://one:8080", Amount: rat("1"), ValidatorNode: peerOneKey},
		{PublicKey: peerTwoKey, Address: "http://two:8080", Amount: rat("1"), ValidatorNode: peerTwoKey},
	}
	validatorNode := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: selfSigner, Validators: validatorSet,
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesisAccounts,
	}, NewHTTPTransport(nil))

	resolvedAddresses := validatorNode.resolvedPeerAddresses()
	resolvedSet := map[string]bool{}
	for _, address := range resolvedAddresses {
		resolvedSet[address] = true
	}
	if len(resolvedAddresses) != 2 || !resolvedSet["http://one:8080"] || !resolvedSet["http://two:8080"] {
		t.Fatalf("should resolve the two other validators' addresses, got %v", resolvedAddresses)
	}
	if resolvedSet["http://self:8080"] {
		t.Fatal("a node must not resolve itself as a gossip peer")
	}
}
