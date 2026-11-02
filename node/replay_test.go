package node

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A payment submitted to ONE node (the payer's "reference node") must (1) return a valid signed
// receipt, (2) gossip so that whichever node proposes the payment's target block includes it, and
// (3) not be applicable twice — a replay of the already-cleared payment (a past block) is ignored.
func TestPaymentGossipReceiptAndReplay(t *testing.T) {
	const nodeCount = 4
	names := validatorNames(nodeCount)
	accounts := []ledger.Account{testAccount("alice", "1000"), testAccount("recipient", "0")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, 5*time.Second, 300*time.Millisecond)

	transports := make([]*HTTPTransport, nodeCount)
	nodes := make([]*Node, nodeCount)
	servers := make([]*httptest.Server, nodeCount)
	for i := 0; i < nodeCount; i++ {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(testConfig(staticChain(names...), names[i], genesis), transports[i])
		servers[i] = httptest.NewServer(NewServer(nodes[i]).Handler())
	}
	defer func() {
		for _, server := range servers {
			server.Close()
		}
	}()
	for i := 0; i < nodeCount; i++ {
		var peers [][]string
		for j := 0; j < nodeCount; j++ {
			if j != i {
				peers = append(peers, []string{servers[j].URL})
			}
		}
		transports[i].SetPeers(peers)
	}
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()

	waitFor(t, 8*time.Second, func() bool { return nodes[0].Snapshot().Height >= 2 }, "network did not start")

	// A payment naming a near-future block (height+3), submitted to ONE node via /hdls/pay. Blocks are
	// 300ms and gossip is localhost, so the payment reaches the proposer of its target block in time.
	height := nodes[0].Snapshot().Height
	payment := signedPayment("alice", "recipient", "10", height+3)
	body, _ := json.Marshal(payment)
	response, err := http.Post(servers[0].URL+"/hdls/pay", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var receipt ledger.Receipt
	_ = json.NewDecoder(response.Body).Decode(&receipt)
	response.Body.Close()
	if receipt.Node != names[0] {
		t.Fatal("receipt does not name the node we submitted to")
	}
	if !receipt.Verify(signing.NetworkScheme(), testNetworkID, testSigner(names[0]).PublicKeyBase64()) {
		t.Fatal("the node's payment receipt did not verify under its key")
	}

	// It must gossip to the eventual proposer, be included, and COMMIT — i.e. be applied on at least a
	// quorum of nodes. (A node that falls behind in consensus stays behind without a catch-up path;
	// with quorum 3 of 4 the network still commits, and cross-height catch-up is exercised separately
	// in TestStragglerCatchUp — here we assert the payment is genuinely committed, not that every node
	// happened to keep up.)
	waitFor(t, 20*time.Second, func() bool {
		funded := 0
		for _, node := range nodes {
			if balance(node.Snapshot().Accounts, "recipient").Cmp(rational("9.9")) == 0 {
				funded++
			}
		}
		return funded >= 3 // a quorum of the 4 validators has committed the payment
	}, "payment submitted to one node did not gossip and commit across a quorum")

	// Replay: resubmit the same (now past-block) payment to a different node. It must not apply again.
	replayResponse, _ := http.Post(servers[1].URL+"/hdls/pay", "application/json", bytes.NewReader(body))
	if replayResponse != nil {
		replayResponse.Body.Close()
	}
	time.Sleep(600 * time.Millisecond) // several blocks
	// No node may show a doubled credit, and a committed node must still show the single 9.9.
	sawSingleCredit := false
	for _, node := range nodes {
		got := balance(node.Snapshot().Accounts, "recipient")
		switch got.Cmp(rational("9.9")) {
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

// A Byzantine round-0 proposer (1 of 4) builds a block on the current state but labels it with an
// OLD height and fills it with a payment signed for that height. ApplyBlock checks each payment's
// BlockID against the block's own Height, so only the node's height check stops the replay: the
// honest nodes must reject the block, keep alice's balance, and still commit the height.
func TestProposalLabelledWithOldHeightCannotReplayPayments(t *testing.T) {
	names := validatorNames(4)
	chain := staticChain(names...)
	attacker := names[3] // runs no node: the test holds its key
	accounts := []ledger.Account{testAccount("alice", "100"), testAccount("mallory", "0")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, 300*time.Millisecond, 0)
	validatorSet := chain.ValidatorRule.SetFor(genesis)
	bus := newBus()
	nodes := make([]*Node, 3)
	for i := range nodes {
		nodes[i] = New(testConfig(chain, names[i], genesis), bus)
		bus.register(names[i], nodes[i])
	}
	payment := signedPayment("alice", "mallory", "30", 1)
	for _, node := range nodes {
		node.SubmitPayment(payment)
	}
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()
	allPast := func(height int64) func() bool {
		return func() bool {
			for _, node := range nodes {
				if node.Snapshot().Height <= height {
					return false
				}
			}
			return true
		}
	}
	waitFor(t, 10*time.Second, allPast(1), "the honest payment did not commit")
	keepBusy(t, names[0], nodes...) // for a block past attackHeight

	// Later blocks are empty, so the state stays the post-payment one until attackHeight: the
	// first height ahead whose round-0 proposer is the attacker (and whose round 1 and predecessor are
	// honest, so the round-1 retry decides promptly).
	var highestHeight int64
	for _, node := range nodes {
		if height := node.Snapshot().Height; height > highestHeight {
			highestHeight = height
		}
	}
	var attackHeight int64
	for height := highestHeight + 1; attackHeight == 0; height++ {
		if validatorSet.ProposerAt(height, 0) == attacker && validatorSet.ProposerAt(height, 1) != attacker && validatorSet.ProposerAt(height-1, 0) != attacker {
			attackHeight = height
		}
	}
	snapshot := nodes[0].Snapshot()
	// The current accounts, labelled as the genesis state, so the block built on them is labelled 1.
	currentState := ledger.LedgerState{Accounts: snapshot.Accounts, Parameters: snapshot.Parameters, GovernanceSequence: snapshot.GovernanceSequence}
	block := ledger.ProposeBlock(chain, currentState, [32]byte{}, [32]byte{}, ledger.BlockContent{Payments: []ledger.Payment{payment}})
	if block.Height != 1 || len(block.Content.Payments) != 1 {
		t.Fatal("premise: a block labelled with height 1 carries the height-1 payment")
	}
	byzantineProposal := proposalOf(testSigner(attacker), attacker, attackHeight, 0, block)
	// Re-sent until attackHeight commits, as a proposer re-gossips, so each node holds it as soon as
	// it enters attackHeight.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				for _, node := range nodes {
					node.SubmitProposal(byzantineProposal)
				}
			}
		}
	}()

	waitFor(t, 20*time.Second, allPast(attackHeight), "the honest nodes did not commit the attack height")
	for i, node := range nodes {
		if got := balance(node.Snapshot().Accounts, "alice"); got.Cmp(rational("70")) != 0 {
			t.Fatalf("node %d: alice's height-1 payment was applied again at height %d: alice = %s, want 70", i, attackHeight, got.RatString())
		}
	}
}

// A payment the node already holds gets 202 and no new receipt (spec 4.1): only the submission that
// pools it is signed for, so replaying a gossiped payment at /hdls/pay costs the node no signature. The
// receipt names the node by its name and verifies under the node's key.
func TestRepeatPaymentGetsNoNewReceipt(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("bank-a", "0"), testAccount("alice", "100"), testAccount("bob", "0")}, time.Hour, time.Hour)
	node := New(testConfig(staticChain("bank-a"), "bank-a", genesis), NewHTTPTransport(nil))
	node.Start() // the sole validator commits block 1 at once, then waits an hour: the payment stays pooled
	defer node.Stop()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	payment := signedPayment("alice", "bob", "10", 2)
	pay := func(route string) (int, []byte) {
		body, _ := json.Marshal(payment)
		response, err := http.Post(server.URL+route, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		answer, _ := io.ReadAll(response.Body)
		return response.StatusCode, answer
	}

	status, answer := pay("/hdls/pay")
	var receipt ledger.Receipt
	if err := json.Unmarshal(answer, &receipt); err != nil || status != http.StatusOK || receipt.Node != "bank-a" || receipt.PaymentID != ledger.PaymentID(payment) ||
		!receipt.Verify(signing.NetworkScheme(), testNetworkID, testSigner("bank-a").PublicKeyBase64()) {
		t.Fatalf("the first submission: %d %s, want 200 and a receipt from bank-a for this payment", status, answer)
	}
	for _, route := range []string{"/hdls/pay", "/hdls/payment"} {
		if status, answer := pay(route); status != http.StatusAccepted || len(bytes.TrimSpace(answer)) != 0 {
			t.Errorf("a repeat at %s: %d %q, want 202 and no receipt", route, status, answer)
		}
	}
}
