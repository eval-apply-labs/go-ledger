package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// busyUpdate is an update of the test account name's description for the block blockID. Pooled, it is
// work until that block (spec 6.6), so it keeps a chain committing blocks without moving money. Each
// block gets its own description, since an update must change a field.
func busyUpdate(name string, blockID int64) ledger.AccountUpdate {
	return signedUpdate(ledger.AccountUpdate{Name: name, Description: fmt.Sprintf("busy until block %d", blockID), BlockID: blockID})
}

// keepBusy keeps running test nodes committing blocks, empty ones included, for the next
// MaxBlockIDLookahead heights: it hands each node the busyUpdate of the test account name for the block
// that far past the lowest node's height. Without it a test network has no work after block 1, and an
// idle chain commits nothing more.
func keepBusy(t *testing.T, name string, nodes ...*Node) {
	t.Helper()
	lowest := nodes[0].Snapshot().Height
	for _, node := range nodes[1:] {
		lowest = min(lowest, node.Snapshot().Height)
	}
	update := busyUpdate(name, lowest+MaxBlockIDLookahead)
	for _, node := range nodes {
		if err := node.AdmitUpdate(update); err != nil {
			t.Fatalf("a node refused the update that keeps the network busy: %v", err)
		}
	}
}

// keepBusyByHand is keepBusy for a node the test drives by hand, without its run loop: it pools the
// busyUpdate of the test account name for the block MaxBlockIDLookahead past the node's height.
func keepBusyByHand(t *testing.T, name string, node *Node) {
	t.Helper()
	update := busyUpdate(name, node.height()+MaxBlockIDLookahead)
	verifiedKey, err := node.checkUpdate(update)
	if err == nil {
		_, err = node.admitUpdate(update, verifiedKey)
	}
	if err != nil {
		t.Fatalf("the node refused the update that keeps it busy: %v", err)
	}
}

// frozen reports whether the committed state of node holds the account named name frozen.
func frozen(node *Node, name string) func() bool {
	return func() bool { account, _ := ledger.FindAccount(node.Snapshot().Accounts, name); return account.Frozen }
}

// total is the number of messages the node has broadcast.
func (recorder *recordingTransport) total() int {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return len(recorder.messages)
}

// idleBank runs bank-a, the sole validator of a chain where alice and bob, each holding 10, have bank-a
// as their bank of record, until it has committed block 1 and gone idle at height 2. Its step timeout is
// the least a parameter change accepts.
func idleBank(t *testing.T, tweak func(genesis *ledger.LedgerState)) *Node {
	t.Helper()
	genesis := testGenesis([]ledger.Account{testAccount("bank-a", "100"), banked("alice", "10", "bank-a"), banked("bob", "10", "bank-a")},
		ledger.AssumedGossipHopMs*time.Millisecond, 20*time.Millisecond)
	if tweak != nil {
		tweak(&genesis)
	}
	node := New(testConfig(staticChain("bank-a"), "bank-a", genesis), NewHTTPTransport(nil))
	node.Start()
	t.Cleanup(node.Stop)
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "the bank did not commit block 1")
	time.Sleep(100 * time.Millisecond) // past the post-commit window: idle
	if node.Snapshot().Height != 2 {
		t.Fatal("premise: with nothing to do, the bank commits nothing after block 1")
	}
	return node
}

// A fresh network commits block 1 with nothing pooled, since no block has been committed yet (spec
// 6.6): a new wallet then has a commit to pin, which verifies against the genesis set. After it the
// network has no work, and commits nothing more.
func TestFreshNetworkCommitsBlockOneThenIdles(t *testing.T) {
	names := validatorNames(4)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	chain, genesis := staticChain(names...), testGenesis(accounts, 400*time.Millisecond, 20*time.Millisecond)
	bus := newBus()
	nodes := make([]*Node, len(names))
	for i, name := range names {
		nodes[i] = New(testConfig(chain, name, genesis), bus)
		bus.register(name, nodes[i])
	}
	for _, node := range nodes {
		node.Start()
		defer node.Stop()
	}
	waitFor(t, 5*time.Second, allReach(nodes, 2), "the fresh network did not commit block 1")
	time.Sleep(500 * time.Millisecond) // 25 block intervals
	for i, node := range nodes {
		if height := node.Snapshot().Height; height != 2 {
			t.Fatalf("node %d went on to height %d with nothing to do", i, height)
		}
	}
	checkpoint := nodes[0].Snapshot().checkpoint()
	if checkpoint.Commit.Height != 1 || !nodes[0].verifyCheckpoint(checkpoint, chain.ValidatorRule.SetFor(genesis)) {
		t.Fatal("the checkpoint does not carry a commit of block 1 that verifies against the genesis set")
	}
}

// An idle chain signs, sends, re-gossips and records nothing (spec 6.6): after block 1 the sole
// validator sends no message through ten re-gossip intervals, its record holds block 1 alone, and its
// sign log is the empty one for height 2.
func TestIdleChainCommitsNothing(t *testing.T) {
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, 50*time.Millisecond, 20*time.Millisecond))
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	recorder := &recordingTransport{}
	node := New(config, recorder)
	node.Start()
	defer node.Stop()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")
	time.Sleep(100 * time.Millisecond) // past the post-commit window
	sent := recorder.total()
	time.Sleep(500 * time.Millisecond)
	if height := node.Snapshot().Height; height != 2 || recorder.total() != sent {
		t.Fatalf("idle, the node went on to height %d and sent %d more messages", height, recorder.total()-sent)
	}
	if entries := rollingEntries(t, node); len(entries) != 1 || entries[0].Height != 1 {
		t.Fatalf("the record holds %d entries, want block 1's alone", len(entries))
	}
	var logged signLog
	if data, err := os.ReadFile(node.signLogPath()); err != nil || json.Unmarshal(data, &logged) != nil || logged.Height != 2 || len(logged.Votes)+len(logged.Proposals) != 0 {
		t.Fatalf("the sign log is not the empty one for height 2: %+v (%v)", logged, err)
	}
}

// Work wakes an idle chain (spec 6.6): a payment, an update, a name request (here a freeze) or a
// certificate (a parameter change, or a ⅔ name action), once pooled, starts the height, and the block
// carries it.
func TestPaymentWakesAnIdleChain(t *testing.T) {
	for kind, submit := range map[string]func(t *testing.T, node *Node) (landed func() bool){
		"a payment": func(t *testing.T, node *Node) func() bool {
			if _, err := node.AdmitPayment(signedPayment("alice", "bob", "1", 2)); err != nil {
				t.Fatal(err)
			}
			return func() bool { return balance(node.Snapshot().Accounts, "bob").Cmp(rational("10.9")) == 0 }
		},
		"an update": func(t *testing.T, node *Node) func() bool {
			if err := node.AdmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "awake", BlockID: 2})); err != nil {
				t.Fatal(err)
			}
			return describedAs(node, "alice", "awake")
		},
		"a name request": func(t *testing.T, node *Node) func() bool {
			if err := node.AdmitNameRequest(nameRequestOf(ledger.FreezeKind, "alice", "alice", "", "", 2)); err != nil {
				t.Fatal(err)
			}
			return frozen(node, "alice")
		},
		"a parameter change": func(t *testing.T, node *Node) func() bool {
			current := node.Snapshot().Parameters
			raised := current
			raised.TransactionCost = rational("0.2")
			vote, _ := ledger.SignParameterVote(testSigner("bank-a"), "bank-a", raised, current.Digest(), 0)
			node.AdmitParameterCertificate(ledger.ParameterChange{NewParameters: raised, PreviousParametersDigest: current.Digest(), Votes: []ledger.ParameterVote{vote}})
			return func() bool { return node.Snapshot().Parameters.TransactionCost.Cmp(rational("0.2")) == 0 }
		},
		"a name action": func(t *testing.T, node *Node) func() bool {
			action := ledger.Reassignment{Name: "alice", Freeze: true}
			vote, _ := ledger.SignReassignmentVote(testSigner("bank-a"), "bank-a", action)
			action.Votes = []ledger.ReassignmentVote{vote}
			node.AdmitReassignmentCertificate(action)
			return frozen(node, "alice")
		},
	} {
		t.Run(kind, func(t *testing.T) {
			node := idleBank(t, nil)
			waitFor(t, 5*time.Second, submit(t, node), kind+" did not wake the idle chain")
		})
	}
}

// A certificate that replaces a pooled one for the same change is pooled too, and wakes an idle node
// (spec 6.6). The pooled one may have lost voters that left the set and no longer reach the quorum, so
// no block carries it and the chain idles; here it holds no vote at all. The validators' replacement,
// with fresh votes, must start the height, or the change would wait for other work.
func TestReplacingCertificateWakesAnIdleNode(t *testing.T) {
	for kind, replace := range map[string]func(node *Node){
		"a parameter change": func(node *Node) {
			raised := node.state.Parameters
			raised.TransactionCost = rational("0.2")
			change := ledger.ParameterChange{NewParameters: raised, PreviousParametersDigest: node.state.Parameters.Digest()}
			voteless := change
			node.parameterCertificatePool[parameterChangeKey(change)] = &voteless
			vote, _ := ledger.SignParameterVote(testSigner("bank-a"), "bank-a", raised, change.PreviousParametersDigest, 0)
			change.Votes = []ledger.ParameterVote{vote}
			node.handle(event{kind: eventParameterCertificate, parameterChange: &change, admit: make(chan admission, 1)})
		},
		"a name action": func(node *Node) {
			action := ledger.Reassignment{Name: "alice", Freeze: true}
			voteless := action
			node.reassignmentCertificatePool[reassignmentKey(action)] = &voteless
			vote, _ := ledger.SignReassignmentVote(testSigner("bank-a"), "bank-a", action)
			action.Votes = []ledger.ReassignmentVote{vote}
			node.handle(event{kind: eventReassignmentCertificate, reassignment: &action, admit: make(chan admission, 1)})
		},
	} {
		node := requestNode(signing.MLDSA65{})
		node.startHeight() // the sole validator commits block 1 within the call
		node.startHeight()
		replace(node)
		if node.lastCommit.Height != 2 {
			t.Errorf("%s replacing a pooled one did not wake the idle node", kind)
		}
	}
}

// A peer's countable vote, or a proposal the node takes, wakes an idle node (spec 6.6): it starts the
// height, so it counts the vote, or prevotes the proposal.
func TestPeerVoteWakesAnIdleNode(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1, 2)
	peer := (self + 1) % 4
	idleAtHeightTwo := func() (*Node, *recordingTransport) {
		node, recorder := network.node(self)
		network.commit(node, self, 1)
		node.handle(event{kind: eventNextHeight, height: node.height()})
		if !node.idle || node.driver != nil {
			t.Fatal("premise: with nothing to do, the node is idle at height 2")
		}
		return node, recorder
	}

	node, _ := idleAtHeightTwo()
	vote := network.vote(peer, 2, 0, consensus.Prevote, [32]byte{1})
	node.handle(event{kind: eventVote, vote: &vote})
	if node.idle || node.driver == nil {
		t.Fatal("a peer's countable vote did not wake the node")
	}

	node, recorder := idleAtHeightTwo()
	proposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	if prevotes := recorder.votes(network.name(self), 2, 0, consensus.Prevote); len(prevotes) != 1 || prevotes[0].BlockHash != proposal.Proposal.BlockHash {
		t.Fatalf("the proposal did not wake the node to prevote it: %v", prevotes)
	}
}

// A pending change is work until it falls due (spec 6.6), so ChangeDelayBlocks keeps its meaning: here a
// key change waits five blocks, and the chain commits them, empty, until the new key is the name's. Then
// it has no work.
func TestPendingChangeKeepsTheChainMoving(t *testing.T) {
	node := idleBank(t, func(genesis *ledger.LedgerState) { genesis.Parameters.ChangeDelayBlocks = 5 })
	if err := node.AdmitNameRequest(nameRequestOf(ledger.KeyChangeKind, "alice", "alice", "bank-a", "alice-new", 2)); err != nil {
		t.Fatal(err)
	}
	rekeyed := func() bool {
		alice, _ := ledger.FindAccount(node.Snapshot().Accounts, "alice")
		return alice.PublicKey == testSigner("alice-new").PublicKeyBase64()
	}
	waitFor(t, 5*time.Second, rekeyed, "the chain stopped before the key change filed in block 2 fell due in block 7")
	time.Sleep(200 * time.Millisecond)
	if height := node.Snapshot().Height; height != 8 {
		t.Fatalf("the chain is at height %d, want 8: idle once the change fell due in block 7", height)
	}
}

// An entry pooled for a later block is work until that block, or it would never land (spec 6.6): a
// payment for block 6, pooled at height 2, keeps the chain moving through block 6, and then it idles.
// The same holds for an update, a name request up to MaxRequestLookahead ahead, and a freeze up to
// MaxBlockIDLookahead ahead: pooled while the node is idle, each starts the height in progress.
func TestFutureBlockIdKeepsTheChainMoving(t *testing.T) {
	node := idleBank(t, nil)
	if _, err := node.AdmitPayment(signedPayment("alice", "bob", "1", 6)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return balance(node.Snapshot().Accounts, "bob").Cmp(rational("10.9")) == 0 },
		"the payment for block 6 did not land")
	time.Sleep(200 * time.Millisecond)
	if height := node.Snapshot().Height; height != 7 {
		t.Fatalf("the chain is at height %d, want 7: idle once block 6 carried the payment", height)
	}

	for kind, admit := range map[string]func(node *Node) error{
		"an update": func(node *Node) error {
			keepBusyByHand(t, "alice", node) // for block 2 + MaxBlockIDLookahead
			return nil
		},
		"a registration": func(node *Node) error {
			return admitRequest(node, nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave", 2+MaxRequestLookahead))
		},
		"a freeze": func(node *Node) error {
			return admitRequest(node, nameRequestOf(ledger.FreezeKind, "alice", "alice", "", "", 2+MaxBlockIDLookahead))
		},
	} {
		node := requestNode(signing.MLDSA65{})
		node.startHeight() // the sole validator commits block 1 within the call
		node.startHeight()
		if !node.idle {
			t.Fatal("premise: with nothing to do, the node is idle at height 2")
		}
		if err := admit(node); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if node.lastCommit.Height != 2 {
			t.Errorf("%s for a later block did not start the height in progress", kind)
		}
	}
}

// A node restarted mid-height whose sign log holds a vote for the height resumes it though it has no
// other work (spec 6.6): it re-gossips its logged vote. The empty log a commit leaves for the next
// height is no such work: restarted with it, the node stays idle and sends nothing.
func TestRestartMidHeightResumesWhileIdle(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(2)
	logged := network.vote(self, 2, 0, consensus.Prevote, [32]byte{1})
	for _, votes := range [][]consensus.Vote{{logged}, nil} {
		genesis := testGenesis(network.genesis, network.timeout, time.Hour)
		config := testConfig(network.chain, network.name(self), genesis)
		config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
		data, _ := json.Marshal(signLog{NetworkID: testNetworkID, Height: 2, Votes: votes})
		if os.WriteFile(config.SnapshotPath, snapshotFile(checkpointOf(t, 1, genesis, network.validatorSet.Validators(), nil)), 0o600) != nil ||
			os.WriteFile(config.SnapshotPath+".votes", data, 0o600) != nil {
			t.Fatal("premise: the snapshot and the sign log are written")
		}
		recorder := &recordingTransport{}
		node, err := Open(config, recorder)
		if err != nil {
			t.Fatal(err)
		}
		node.started = true
		node.startFirstHeight()
		node.handle(event{kind: eventRegossip})
		resent := recorder.votes(network.name(self), 2, 0, consensus.Prevote)
		if len(votes) > 0 && (node.idle || len(resent) != 1 || resent[0] != logged) {
			t.Fatalf("restarted with a vote in its sign log, the node did not resume the height: idle %v, re-gossiped %v", node.idle, resent)
		}
		if len(votes) == 0 && (!node.idle || recorder.total() != 0) {
			t.Fatalf("restarted with the empty sign log, the node did not stay idle: idle %v, sent %d messages", node.idle, recorder.total())
		}
	}
}

// Spec review record #2: a validator back without its sign log may have voted at its height, and signs
// nothing for two re-gossip intervals, so that peers bring its earlier votes back first (see
// startFirstHeight). A peer's countable vote arriving first must not wake it early: wakeIfIdle acts
// only on a node that startHeight left idle. Here the node is the round's proposer, and woken early it
// would propose a block and prevote it at once, against its earlier prevote.
func TestNodeBackWithoutItsSignLogSignsNothingBeforeTheRegossip(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(2, 0)
	peer := (self + 1) % 4
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	config := testConfig(network.chain, network.name(self), genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(config.SnapshotPath, snapshotFile(checkpointOf(t, 1, genesis, network.validatorSet.Validators(), nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingTransport{}
	node, err := Open(config, recorder)
	if err != nil || node.height() != 2 {
		t.Fatalf("premise: the node opens at height 2 from its snapshot (err %v)", err)
	}
	node.started = true
	node.startFirstHeight() // the re-gossip wait: its timer would fire in an hour

	peerVote := network.vote(peer, 2, 0, consensus.Prevote, [32]byte{1})
	node.handle(event{kind: eventVote, vote: &peerVote})
	if node.driver != nil || recorder.proposalAt(2) != nil || len(recorder.votes(network.name(self), 2, 0, consensus.Prevote)) != 0 {
		t.Fatal("a peer's vote started the height during the re-gossip wait")
	}
	earlier := network.vote(self, 2, 0, consensus.Prevote, [32]byte{1}) // signed before the log was lost; a peer re-gossips it
	node.handle(event{kind: eventVote, vote: &earlier})
	node.handle(event{kind: eventNextHeight, height: node.height()}) // the wait's timer
	if node.driver == nil {
		t.Fatal("the height did not start once the wait was over")
	}
	for _, vote := range recorder.votes(network.name(self), 2, 0, consensus.Prevote) {
		if vote != earlier {
			t.Fatal("the node signed a second prevote for a round it had prevoted in before it lost its sign log")
		}
	}
	if recorder.proposalAt(2) != nil {
		t.Fatal("the node proposed in a round it had prevoted in before it lost its sign log")
	}
}

// Spec review record #2: a payment pooled in the post-commit window does not start the next height
// before BlockIntervalMs is over, since wakeIfIdle acts only on a node that startHeight left idle. The
// height starts when the window's timer fires, and the payment is in the block.
func TestPaymentInThePostCommitWindowDoesNotStartTheHeightEarly(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(2, 0)
	if network.proposer(1, 0) == self {
		t.Fatal("premise: with four equal validators heights 1 and 2 have different proposers")
	}
	node, recorder := network.node(self)
	network.commit(node, self, 1)
	if err := admit(node, signedPayment(network.name(self), "shop", "5", 2)); err != nil {
		t.Fatal(err)
	}
	if node.driver != nil || recorder.proposalAt(2) != nil {
		t.Fatal("a payment pooled in the post-commit window started the next height early")
	}
	node.handle(event{kind: eventNextHeight, height: node.height()}) // the window's timer
	if proposal := recorder.proposalAt(2); proposal == nil || len(proposal.Block.Content.Payments) != 1 {
		t.Fatal("once the window was over, the node did not propose the payment")
	}
}

// A pending proof is work only once the candidate block carries it (spec 6.6). With four equal seats a
// late double-sign's evidence fits the one-third cap, the candidate carries it, and the height starts;
// with three, freezing one seat is a third of the power, the cap holds the evidence back, and the node
// stays idle with the proof pending.
func TestPendingProofIsWorkOnlyOnceTheCandidateCarriesIt(t *testing.T) {
	for _, validatorCount := range []int{4, 3} {
		network := newHandNet(t, validatorCount)
		self := network.nonProposer(1)
		cheat := 0 // the first validator whose precommit network.commit hands the node: it is in the certificate
		if cheat == self {
			cheat = 1
		}
		node, _ := network.node(self)
		network.commit(node, self, 1)
		late := network.vote(cheat, 1, 0, consensus.Precommit, [32]byte{1})
		node.handle(event{kind: eventVote, vote: &late})
		if _, pending := node.pendingProofs[network.name(cheat)]; !pending {
			t.Fatalf("%d seats: premise: the late double-sign is a pending proof", validatorCount)
		}
		node.handle(event{kind: eventNextHeight, height: node.height()})
		if carried := validatorCount == 4; node.idle == carried {
			t.Errorf("%d seats: idle %v with the proof pending, want %v", validatorCount, node.idle, !carried)
		}
	}
}

// A late double-sign that reaches an idle node wakes it (spec 6.6: the candidate carries the proof). Left
// idle, the node would keep the proof in memory only until other work came, and a restart of every node
// meanwhile, as for a release upgrade, would lose it (spec 6.4). Here the node proposes height 2, and its
// proposal carries the proof.
func TestLateDoubleSignWakesAnIdleNode(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(2, 0)
	if network.proposer(1, 0) == self {
		t.Fatal("premise: with four equal validators heights 1 and 2 have different proposers")
	}
	cheat := 0 // the first validator whose precommit network.commit hands the node: it is in the certificate
	if cheat == self {
		cheat = 1
	}
	node, recorder := network.node(self)
	network.commit(node, self, 1)
	node.handle(event{kind: eventNextHeight, height: node.height()})
	if !node.idle {
		t.Fatal("premise: with nothing to do, the node is idle at height 2")
	}
	late := network.vote(cheat, 1, 0, consensus.Precommit, [32]byte{1})
	node.handle(event{kind: eventVote, vote: &late})
	if !carriesEvidenceAgainst(recorder, 2, network.name(cheat)) {
		t.Fatal("the late double-sign did not wake the node to propose a block carrying it")
	}
}
