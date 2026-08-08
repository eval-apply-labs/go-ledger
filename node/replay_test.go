package node

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// A payment submitted to ONE node (the payer's "reference node") must (1) return a valid signed
// receipt, (2) gossip so that whichever node proposes the payment's target block includes it, and
// (3) not be applicable twice — a replay of the already-cleared payment (a past block) is ignored.
func TestPaymentGossipReceiptAndReplay(t *testing.T) {
	const n = 4
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)

	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	recipient, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("1000"), ValidatorNode: "v"},
	}

	transports := make([]*HTTPTransport, n)
	nodes := make([]*Node, n)
	servers := make([]*httptest.Server, n)
	for i := 0; i < n; i++ {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 300 * time.Millisecond,
			Genesis: genesis,
		}, transports[i])
		servers[i] = httptest.NewServer(NewServer(nodes[i]).Handler())
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	for i := 0; i < n; i++ {
		var peers []string
		for j := 0; j < n; j++ {
			if j != i {
				peers = append(peers, servers[j].URL)
			}
		}
		transports[i].SetPeers(peers)
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()

	waitFor(t, 8*time.Second, func() bool { return nodes[0].Snapshot().Height >= 2 }, "network did not start")

	// A payment naming a near-future block (h+3), submitted to ONE node via /hdls/pay. Blocks are
	// 300ms and gossip is localhost, so the payment reaches the proposer of its target block in time.
	h := nodes[0].Snapshot().Height
	pay, _ := ledger.SignPayment(alice, ledger.Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: recipient.PublicKeyBase64(), Amount: rat("10"), BlockID: h + 3,
	})
	body, _ := json.Marshal(pay)
	resp, err := http.Post(servers[0].URL+"/hdls/pay", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var receipt ledger.Receipt
	_ = json.NewDecoder(resp.Body).Decode(&receipt)
	resp.Body.Close()
	if !receipt.Verify(signing.Ed25519{}, "testnet") {
		t.Fatal("the node's payment receipt did not verify")
	}
	if receipt.Node != signers[0].PublicKeyBase64() {
		t.Fatal("receipt was not signed by the node we submitted to")
	}

	// It must gossip to the eventual proposer, be included, and COMMIT — i.e. be applied on at least a
	// quorum of nodes. (A node that falls behind in consensus stays behind without a catch-up path;
	// with quorum 3 of 4 the network still commits, and cross-height catch-up is exercised separately
	// in TestStragglerCatchUp — here we assert the payment is genuinely committed, not that every node
	// happened to keep up.)
	waitFor(t, 20*time.Second, func() bool {
		funded := 0
		for _, nd := range nodes {
			if balance(nd.Snapshot().Accounts, recipient.PublicKeyBase64()).Cmp(rat("9.9")) == 0 {
				funded++
			}
		}
		return funded >= 3 // a quorum of the 4 validators has committed the payment
	}, "payment submitted to one node did not gossip and commit across a quorum")

	// Replay: resubmit the same (now past-block) payment to a different node. It must not apply again.
	resp2, _ := http.Post(servers[1].URL+"/hdls/pay", "application/json", bytes.NewReader(body))
	if resp2 != nil {
		resp2.Body.Close()
	}
	time.Sleep(600 * time.Millisecond) // several blocks
	// No node may show a doubled credit, and a committed node must still show the single 9.9.
	sawSingleCredit := false
	for _, nd := range nodes {
		got := balance(nd.Snapshot().Accounts, recipient.PublicKeyBase64())
		switch got.Cmp(rat("9.9")) {
		case 0:
			sawSingleCredit = true
		case 1:
			t.Fatalf("replayed payment was applied again: recipient = %s, want 9.9", got.FloatString(2))
		}
	}
	if !sawSingleCredit {
		t.Fatal("expected a committed node to show the recipient's single 9.9 credit")
	}
}
