package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A running node that cannot make progress alone (a straggler that missed a commit window) must
// fast-forward when handed a peer's later verified checkpoint, instead of staying stuck. Because it
// is 1 of a 2-quorum set, height 6 is unreachable except via catch-up — so reaching it proves it.
func TestStragglerCatchUp(t *testing.T) {
	names := []string{"founder", "straggler"} // quorum 2
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	straggler := New(testConfig(staticChain(names...), "straggler", genesis), NewHTTPTransport(nil))
	straggler.Start()
	defer straggler.Stop()

	// A peer's checkpoint at height 5 — a real founder+straggler commit over the resulting state.
	state5 := testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0)
	checkpoint := checkpointOf(t, 5, state5, validatorsOf(names), nil)
	straggler.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})

	waitFor(t, 3*time.Second, func() bool { return straggler.Snapshot().Height == 6 },
		"straggler did not catch up to height 6 after a later checkpoint")
}

// A node whose bootstrap peers are all down must still catch up, from its gossip peers: a checkpoint
// is verified against the node's own last committed accounts, so any peer is a safe source. While a
// bootstrap peer serves the checkpoint, a gossip peer that reports its block is asked only its status,
// not for the checkpoint again.
func TestCatchUpFallsBackToGossipPeers(t *testing.T) {
	names := []string{"founder", "straggler"} // quorum 2: the straggler cannot decide alone
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	state5 := testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0)
	checkpoint := checkpointOf(t, 5, state5, validatorsOf(names), nil)
	var bootstrapUp atomic.Bool
	bootstrap := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if !bootstrapUp.Load() {
			http.Error(responseWriter, "down", http.StatusServiceUnavailable)
			return
		}
		servePeer(responseWriter, request, checkpoint)
	}))
	defer bootstrap.Close()
	var gossipAsked atomic.Int32
	gossip := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/hdls/checkpoint" {
			gossipAsked.Add(1)
		}
		servePeer(responseWriter, request, checkpoint)
	}))
	defer gossip.Close()

	config := testConfig(staticChain(names...), "straggler", genesis)
	config.Bootstrap = []string{bootstrap.URL}
	straggler := New(config, NewHTTPTransport([]string{gossip.URL}))
	bootstrapUp.Store(true)
	if _, _, found := straggler.catchUpCheckpoint(); !found || gossipAsked.Load() != 0 {
		t.Fatalf("with its bootstrap peer answering, the node must take its checkpoint and ask no gossip peer for it again (found %v, gossip peer asked %d times)", found, gossipAsked.Load())
	}

	bootstrapUp.Store(false)
	straggler.Start()
	defer straggler.Stop()
	waitFor(t, 10*time.Second, func() bool { return straggler.Snapshot().Height == 6 },
		"with its bootstrap peer down, the node did not catch up from its gossip peer")
}

// A checkpoint a carrier brought (spec §7.5) is taken on the operator port's POST /hdls/checkpoint, and
// verified as catch-up verifies one. One that is ahead and verifies is adopted, and the answer is 202.
// Each other is refused with 409 and its reason: one that is not ahead, at the node's committed height
// or below it; one whose state is not the one its commit signs; and another network's, signed by the
// same banks under the same keys, whose signatures bind the other network's id.
func TestCarriedCheckpointIsAdoptedOnlyWhenAheadAndVerified(t *testing.T) {
	names := []string{"founder", "straggler"} // quorum 2: the straggler cannot decide alone
	scheme := signing.NetworkScheme()
	signersOn := map[string]map[string]signing.Signer{testNetworkID: {}, "elsewhere": {}} // by network id, then name
	var validators []consensus.Validator
	var accounts []ledger.Account
	for _, name := range names {
		_, privateKey, err := scheme.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		for networkID, signers := range signersOn {
			if signers[name], err = signing.NewSigner(scheme, networkID, privateKey); err != nil {
				t.Fatal(err)
			}
		}
		publicKey := signersOn[testNetworkID][name].PublicKeyBase64()
		validators = append(validators, consensus.Validator{Name: name, PublicKey: publicKey, Power: 1})
		accounts = append(accounts, ledger.Account{Name: name, PublicKey: publicKey, Amount: rational("100")})
	}
	genesis := testGenesis(accounts, 30*time.Second, 0)
	straggler := New(Config{Chain: staticChain(names...), Signer: signersOn[testNetworkID]["straggler"], Name: "straggler", Genesis: genesis},
		&recordingTransport{})
	straggler.Start()
	defer straggler.Stop()
	handler := NewServer(straggler).Handler()
	post := func(checkpoint Checkpoint) *httptest.ResponseRecorder {
		body, _ := json.Marshal(checkpoint)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hdls/checkpoint", bytes.NewReader(body)))
		return recorder
	}

	later := genesis
	later.Accounts = append([]ledger.Account(nil), genesis.Accounts...)
	later.Accounts[0].Amount = rational("90")
	later.Accounts[1].Amount = rational("110")
	damaged := checkpointOf(t, 9, later, validators, signersOn[testNetworkID])
	damaged.State.Accounts = append([]ledger.Account(nil), later.Accounts...)
	damaged.State.Accounts[1].Amount = rational("1000") // another state than the one its commit signs
	for _, refused := range []struct {
		checkpoint Checkpoint
		reason     string
	}{
		{checkpointOf(t, 9, later, validators, signersOn["elsewhere"]), "another network"},
		{damaged, "does not verify"},
	} {
		if answer := post(refused.checkpoint); answer.Code != http.StatusConflict || !strings.Contains(answer.Body.String(), refused.reason) {
			t.Errorf("a checkpoint refused for %q was answered %d %q, want 409 and the reason", refused.reason, answer.Code, answer.Body)
		}
	}
	if height := straggler.Snapshot().Height; height != 1 {
		t.Fatalf("after the refused checkpoints the node is at height %d, want 1", height)
	}

	if answer := post(checkpointOf(t, 5, later, validators, signersOn[testNetworkID])); answer.Code != http.StatusAccepted {
		t.Fatalf("an ahead, verifying checkpoint was answered %d %q, want 202", answer.Code, answer.Body)
	}
	if snapshot := straggler.Snapshot(); snapshot.Height != 6 || balance(snapshot.Accounts, "straggler").Cmp(rational("110")) != 0 {
		t.Fatalf("after adopting the checkpoint of height 5 the node is at height %d, want 6, with its state", snapshot.Height)
	}
	for _, height := range []int64{5, 3} {
		if answer := post(checkpointOf(t, height, later, validators, signersOn[testNetworkID])); answer.Code != http.StatusConflict ||
			!strings.Contains(answer.Body.String(), "not ahead") {
			t.Errorf("a checkpoint of height %d, at the node's committed height 5 or below, was answered %d %q, want 409, not ahead", height, answer.Code, answer.Body)
		}
	}
}

// recordingTransport delivers nothing; it records what the node broadcasts.
type recordingTransport struct { // implements Transport
	mutex    sync.Mutex
	messages []Message
}

func (recorder *recordingTransport) Broadcast(from string, message Message) {
	recorder.mutex.Lock()
	recorder.messages = append(recorder.messages, message)
	recorder.mutex.Unlock()
}

func (recorder *recordingTransport) sent(messageType MessageType) int {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	count := 0
	for _, message := range recorder.messages {
		if message.Type == messageType {
			count++
		}
	}
	return count
}

func (recorder *recordingTransport) proposals() []ProposalMessage {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	var sentProposals []ProposalMessage
	for _, message := range recorder.messages {
		if message.Type == MessageProposal {
			sentProposals = append(sentProposals, *message.Proposal)
		}
	}
	return sentProposals
}

func (recorder *recordingTransport) proposalAt(height int64) *ProposalMessage {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	for _, message := range recorder.messages {
		if message.Type == MessageProposal && message.Proposal.Proposal.Height == height {
			proposal := *message.Proposal
			return &proposal
		}
	}
	return nil
}

// votes returns the votes signed by validatorKey that were sent for (height, round, voteType).
func (recorder *recordingTransport) votes(validatorName string, height int64, round int32, voteType consensus.VoteType) []consensus.Vote {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	var sentVotes []consensus.Vote
	for _, message := range recorder.messages {
		if vote := message.Vote; vote != nil && vote.Validator == validatorName && vote.Height == height && vote.Round == round && vote.Type == voteType {
			sentVotes = append(sentVotes, *vote)
		}
	}
	return sentVotes
}

// A catch-up drops what the node held for the height it leaves, as a commit does: the blocks taken for
// it, with their payments and nonces, and the last proposal decided. Nothing reads them after the
// fast-forward, and they used to stay in memory until the next commit.
func TestCatchUpDropsTheBlocksOfTheHeightItLeaves(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100")}, time.Hour, time.Hour)
	chain := staticChain("validator")
	node := New(testConfig(chain, "validator", genesis), &recordingTransport{}) // driven by hand
	if node.startHeight(); node.lastProposal.Proposal.Height != 1 {
		t.Fatal("premise: the sole validator commits height 1 inside startHeight, and keeps its proposal")
	}
	block := ledger.ProposeBlock(chain, node.state, node.lastBlockHash, [32]byte{}, ledger.BlockContent{})
	if !node.onProposal(proposalOf(testSigner("validator"), "validator", 2, 0, block)) || len(node.blocks) != 1 {
		t.Fatal("premise: the node holds a block for height 2, taken in the post-commit window")
	}

	node.fastForward(checkpointOf(t, 5, node.state, validatorsOf([]string{"validator"}), nil))
	if len(node.blocks) != 0 || node.lastProposal.Proposal.Height != 0 {
		t.Errorf("after the fast-forward the node holds %d blocks and the proposal it decided at height %d, want none",
			len(node.blocks), node.lastProposal.Proposal.Height)
	}
}

// A catch-up can lower a payer's balance under its pending payments (here, the payer's other payment
// committed while this node was away). The pool must then be reconciled exactly as after a commit,
// or the node proposes the now-overdrawing payments and the overdraw ban destroys the payer's
// balance. Each pending payment fits the new balance on its own, so the ledger's lone over-balance
// rule (E24) does not drop either: only the pool reconciliation keeps the pair out of one block.
func TestCatchUpDropsPendingOverdraw(t *testing.T) {
	names := []string{"first", "second"} // quorum 2
	chain := staticChain(names...)
	accounts := []ledger.Account{testAccount("first", "0"), testAccount("second", "0"), testAccount("payer", "100"),
		testAccount("cafe", "0"), testAccount("payee-0", "0"), testAccount("payee-1", "0")}
	genesis := testGenesis(accounts, 30*time.Second, 0)

	recorder := &recordingTransport{}
	node := New(testConfig(chain, chain.ValidatorRule.SetFor(genesis).ProposerAt(2, 0), genesis), recorder)
	node.Start()
	defer node.Stop()
	for i := 0; i < 2; i++ {
		if _, err := node.AdmitPayment(signedPayment("payer", fmt.Sprintf("payee-%d", i), "30", 2)); err != nil {
			t.Fatalf("premise: two payments of 30 out of 100 are admitted: %v", err)
		}
	}

	// Height 1 committed elsewhere with the payer's other 60: it holds 40, under the pending 30 + 30.
	accounts[2], accounts[3] = testAccount("payer", "40"), testAccount("cafe", "59.9")
	checkpoint := checkpointOf(t, 1, testGenesis(accounts, 30*time.Second, 0), validatorsOf(names), nil)
	node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})

	waitFor(t, 3*time.Second, func() bool { return recorder.proposalAt(2) != nil }, "the node did not propose at the caught-up height")
	if proposal := recorder.proposalAt(2); len(proposal.Block.Content.Payments) != 1 {
		t.Fatalf("after catch-up the node proposed %d of the payer's payments, want 1: together they overdraw its 40, and the ban would destroy its balance", len(proposal.Block.Content.Payments))
	}
}

// In dynamic mode, once the founder (the only genesis validator) pays out and leaves the set, no
// checkpoint carries a genesis signature any more. A node that misses commits must still catch up:
// catch-up trusts what the validators of its own last committed state vouch for, not only genesis.
func TestDynamicStragglerCatchesUpAfterFounderPayout(t *testing.T) {
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), selfDelegated("successor", "0")}, 300*time.Millisecond, 20*time.Millisecond)
	messageBus := newBus()
	newNodeOnBus := func(name string) *Node {
		node := New(testConfig(dynamicChain("100"), name, genesis), messageBus)
		messageBus.register(name, node)
		return node
	}
	founderNode, successorNode := newNodeOnBus("founder"), newNodeOnBus("successor")
	// The founder pays the successor 990 at height 1; from height 2 the successor is the only validator
	// (the founder keeps 10 < 100).
	founderNode.SubmitPayment(signedPayment("founder", "successor", "990", 1))
	successorNode.Start() // first, so it hears the founder's height-1 proposal
	founderNode.Start()
	defer successorNode.Stop()
	defer founderNode.Stop()
	keepBusy(t, "successor", successorNode, founderNode)
	waitFor(t, 10*time.Second, func() bool { return founderNode.Snapshot().Height >= 3 }, "the founder's node did not follow the payout")

	// The founder's node drops off gossip and falls behind.
	messageBus.mutex.Lock()
	delete(messageBus.nodes, "founder")
	messageBus.mutex.Unlock()
	behind := founderNode.Snapshot().Height
	waitFor(t, 10*time.Second, func() bool { return successorNode.Snapshot().Height > behind+2 }, "X did not advance alone")

	server := httptest.NewServer(NewServer(successorNode).Handler())
	defer server.Close()
	checkpoint, trustedFrame, found := founderNode.bestCheckpoint([]catchUpPeer{{urls: []string{server.URL}, client: NewHTTPClient(5 * time.Second)}}) // what its catch-up loop does on every tick
	if !found {
		t.Fatal("the straggler rejected the network's checkpoint: it trusts only genesis, and the founder no longer signs")
	}
	founderNode.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint, trustedFrame: trustedFrame})
	waitFor(t, 5*time.Second, func() bool { return founderNode.Snapshot().Height > checkpoint.Commit.Height }, "the straggler did not catch up")
}
