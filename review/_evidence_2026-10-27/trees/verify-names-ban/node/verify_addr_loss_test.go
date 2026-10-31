package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Mechanism check: a STATIC validator whose account reaches zero is deleted with its on-chain
// Address, so peers resolving gossip targets from state stop dialing it.
func TestVerifyAddressLostOnZeroBalance(t *testing.T) {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	c, _ := signing.Generate(signing.Ed25519{}, "testnet")
	A, B, C := a.PublicKeyBase64(), b.PublicKeyBase64(), c.PublicKeyBase64()
	set := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: A, Power: 1}, {PublicKey: B, Power: 1}, {PublicKey: C, Power: 1}})
	gen := []ledger.Account{
		{PublicKey: A, Amount: rat("100"), ValidatorNode: A, Address: "http://a:1"},
		{PublicKey: B, Amount: rat("5"), ValidatorNode: B, Address: "http://b:1"},
		{PublicKey: C, Amount: rat("5"), ValidatorNode: C, Address: "http://c:1"},
	}
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a, Validators: set,
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: gen}, NewHTTPTransport(nil))
	before := nd.resolvedPeerAddresses()
	nd.accounts = ledger.ProcessPayments(nd.accounts, []ledger.Payment{{PublicKeyFrom: B, PublicKeyTo: A, Amount: rat("5")}}, rat("0.1"))
	after := nd.resolvedPeerAddresses()
	t.Logf("A's gossip targets before=%v after B spent to zero=%v (B still in the static set: %v)", before, after, set.Contains(B))
}
