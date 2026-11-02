package node

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// restartNetwork runs static-set validators on an in-memory full mesh where they can lose power and
// restart. Each delivery passes filter, checked when it is delivered; a node without power neither
// sends nor receives; every vote and proposal sent is recorded. A restart is what cmd/server does after a reboot:
// a new node from the same config, which reloads its own snapshot and sign log.
type restartNetwork struct { // implements Transport
	t            *testing.T
	names        []string
	configs      map[string]Config
	validatorSet *consensus.ValidatorSet

	mutex     sync.Mutex
	nodes     map[string]*Node
	down      map[string]bool
	stopped   map[*Node]bool
	filter    func(from, to string, message Message) bool
	votes     []consensus.Vote
	proposals []ProposalMessage
}

func newRestartNetwork(t *testing.T, powers ...int64) *restartNetwork {
	network := &restartNetwork{t: t, configs: map[string]Config{}, nodes: map[string]*Node{}, down: map[string]bool{}, stopped: map[*Node]bool{},
		names: validatorNames(len(powers))}
	accounts := []ledger.Account{testAccount("payer", "1000"), testAccount("merchant", "0")}
	chain := staticChain()
	for i, name := range network.names {
		accounts = append(accounts, testAccount(name, "0"))
		chain.ValidatorRule.StaticValidators = append(chain.ValidatorRule.StaticValidators, ledger.StaticValidator{Name: name, Power: powers[i]})
	}
	genesis := testGenesis(accounts, 400*time.Millisecond, 20*time.Millisecond)
	network.validatorSet = chain.ValidatorRule.SetFor(genesis)
	snapshotDirectory := t.TempDir()
	for i, name := range network.names {
		config := testConfig(chain, name, genesis)
		config.SnapshotPath = filepath.Join(snapshotDirectory, fmt.Sprintf("node%d.json", i))
		network.configs[name] = config
	}
	t.Cleanup(func() {
		var running []*Node
		network.mutex.Lock()
		for node, stopped := range network.stopped {
			if !stopped {
				running = append(running, node)
			}
		}
		network.mutex.Unlock()
		for _, node := range running {
			node.Stop()
		}
	})
	return network
}

// serveCheckpoints gives every node the others' checkpoints as catch-up sources, as on a real
// network, so a node left behind fetches a verified checkpoint from one that decided. A node without
// power does not answer. It applies to nodes started after the call.
func (network *restartNetwork) serveCheckpoints() {
	urls := map[string]string{}
	for _, name := range network.names {
		checkpointServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			network.mutex.Lock()
			node, down := network.nodes[name], network.down[name]
			network.mutex.Unlock()
			if node == nil || down {
				http.Error(responseWriter, "no power", http.StatusServiceUnavailable)
				return
			}
			NewServer(node).Handler().ServeHTTP(responseWriter, request)
		}))
		network.t.Cleanup(checkpointServer.Close)
		urls[name] = checkpointServer.URL
	}
	for name, config := range network.configs {
		for _, peer := range network.names {
			if peer != name {
				config.Bootstrap = append(config.Bootstrap, urls[peer])
			}
		}
		network.configs[name] = config
	}
}

func (network *restartNetwork) Broadcast(from string, message Message) {
	network.mutex.Lock()
	if message.Vote != nil {
		network.votes = append(network.votes, *message.Vote)
	}
	if message.Proposal != nil {
		network.proposals = append(network.proposals, *message.Proposal)
	}
	targets := make(map[string]*Node, len(network.nodes))
	for name, node := range network.nodes {
		targets[name] = node
	}
	network.mutex.Unlock()
	for to, node := range targets {
		if to == from {
			continue
		}
		go func() {
			network.mutex.Lock()
			deliver := !network.down[to] && (network.filter == nil || network.filter(from, to, message))
			network.mutex.Unlock()
			if deliver {
				deliverToNode(node, message)
			}
		}()
	}
}

// start boots the node of name on transport (again, after it lost power).
func (network *restartNetwork) start(name string, transport Transport) *Node {
	node := New(network.configs[name], transport)
	network.mutex.Lock()
	network.nodes[name], network.down[name], network.stopped[node] = node, false, false
	network.mutex.Unlock()
	node.Start()
	return node
}

// powerOff cuts the power of name's node: it stops, as dead as a machine without power.
func (network *restartNetwork) powerOff(name string, node *Node) {
	network.mutex.Lock()
	network.down[name], network.stopped[node] = true, true
	network.mutex.Unlock()
	node.Stop()
}

// powerCut is a node's link that loses power right after the first message that cut matches has
// left: nothing the node sends after it goes out, and nothing reaches the node.
type powerCut struct { // implements Transport
	network *restartNetwork
	name    string
	cut     func(Message) bool
	dark    chan struct{}
}

func (network *restartNetwork) powerCut(name string, cut func(Message) bool) *powerCut {
	return &powerCut{network: network, name: name, cut: cut, dark: make(chan struct{})}
}

func (outage *powerCut) Broadcast(from string, message Message) {
	select {
	case <-outage.dark:
		return
	default:
	}
	outage.network.Broadcast(from, message)
	if outage.cut(message) {
		outage.network.mutex.Lock()
		outage.network.down[outage.name] = true
		outage.network.mutex.Unlock()
		close(outage.dark)
	}
}

// wait blocks until the power is cut, then powers the node off.
func (outage *powerCut) wait(node *Node) {
	select {
	case <-outage.dark:
	case <-time.After(30 * time.Second * testTimeSlack):
		outage.network.t.Fatal("the message that cuts the power was never sent")
	}
	outage.network.powerOff(outage.name, node)
}

func (network *restartNetwork) pay(blockID int64) ledger.Payment {
	return signedPayment("payer", "merchant", "5", blockID)
}

// votesBy returns the votes the validator named name has sent at the given height.
func (network *restartNetwork) votesBy(name string, height int64) []consensus.Vote {
	network.mutex.Lock()
	defer network.mutex.Unlock()
	var matching []consensus.Vote
	for _, vote := range network.votes {
		if vote.Validator == name && vote.Height == height {
			matching = append(matching, vote)
		}
	}
	return matching
}

// doubleSigned reports whether the votes hold two different ones for one round and type:
// equivocation evidence against their signer.
func doubleSigned(votes []consensus.Vote) bool {
	signed := map[[2]int32][32]byte{}
	for _, vote := range votes {
		slot := [2]int32{vote.Round, int32(vote.Type)}
		if earlierDigest, found := signed[slot]; found && earlierDigest != vote.BlockHash {
			return true
		}
		signed[slot] = vote.BlockHash
	}
	return false
}

func heightOf(message Message) int64 {
	switch {
	case message.Proposal != nil:
		return message.Proposal.Proposal.Height
	case message.Vote != nil:
		return message.Vote.Height
	case message.Payment != nil:
		return message.Payment.BlockID
	}
	return 0
}

func ownVote(message Message, name string, height int64, voteType consensus.VoteType) bool {
	return message.Vote != nil && message.Vote.Validator == name && message.Vote.Height == height && message.Vote.Type == voteType && message.Vote.BlockHash != [32]byte{}
}

// The amnesia fork. Static X:2 A:1 B:1, quorum 3. At height H, B is asleep; X proposes D1 (it carries
// a payment), X and A lock and precommit it, and A decides D1. X loses power after its precommit
// left and before A's arrived, so it never decided. Then A drops off, B wakes, and X restarts from
// its H-1 snapshot. Restarted unlocked, X would decide an empty block with B: two certificates for
// height H. X must come back locked on D1, voting for no other block, so nothing else can be decided
// at H; once A is back, everyone continues on D1.
func TestRestartedValidatorKeepsItsLock(t *testing.T) {
	network := newRestartNetwork(t, 2, 1, 1)
	validatorX, validatorA, validatorB := network.names[0], network.names[1], network.names[2]
	heightH := int64(3)
	for network.validatorSet.ProposerAt(heightH, 0) != validatorX {
		heightH++
	}
	var away atomic.Value // the validator cut off from the others
	away.Store(validatorB)
	network.filter = func(from, to string, message Message) bool {
		if away.Load() == validatorB {
			if from == validatorA && to == validatorX && message.Vote != nil && message.Vote.Height == heightH && message.Vote.Type == consensus.Precommit {
				return false // still in flight when X loses power
			}
			return from != validatorB && to != validatorB || heightOf(message) < heightH // B sleeps through height H, payment included
		}
		return from != away.Load() && to != away.Load()
	}
	cut := network.powerCut(validatorX, func(message Message) bool { return ownVote(message, validatorX, heightH, consensus.Precommit) })
	nodeX := network.start(validatorX, cut)
	nodeA := network.start(validatorA, network)
	nodeB := network.start(validatorB, network)
	if _, err := nodeX.AdmitPayment(network.pay(heightH)); err != nil {
		t.Fatal(err)
	}
	cut.wait(nodeX)
	waitFor(t, 10*time.Second, func() bool { return nodeA.Snapshot().Height > heightH }, "A did not decide height H")
	digestD1 := nodeA.Snapshot().LastCommit.BlockHash

	away.Store(validatorA)
	nodeX = network.start(validatorX, network)
	waitFor(t, 20*time.Second, func() bool {
		for _, vote := range network.votesBy(validatorX, heightH) {
			if vote.Round > 0 {
				return true
			}
		}
		return nodeX.Snapshot().Height > heightH || nodeB.Snapshot().Height > heightH
	}, "the restarted X neither decided nor went on to the next round")
	for _, vote := range network.votesBy(validatorX, heightH) {
		if vote.BlockHash != digestD1 && vote.BlockHash != [32]byte{} {
			t.Fatalf("the restarted X voted for a block other than D1 at height %d: it forgot its lock", heightH)
		}
	}
	for name, node := range map[string]*Node{"X": nodeX, "B": nodeB} {
		if snapshot := node.Snapshot(); snapshot.Height > heightH && balance(snapshot.Accounts, "merchant").Sign() == 0 {
			t.Fatalf("FORK: %s committed a block at height %d other than A's decision", name, heightH)
		}
	}

	away.Store("") // A is back; X and B take A's checkpoint, as their catch-up loops would
	checkpoint := nodeA.Snapshot().checkpoint()
	nodeX.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})
	nodeB.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})
	keepBusy(t, "payer", nodeX, nodeA, nodeB) // the blocks after height H carry nothing
	waitFor(t, 10*time.Second, func() bool {
		return nodeX.Snapshot().Height > heightH+1 && nodeA.Snapshot().Height > heightH+1 && nodeB.Snapshot().Height > heightH+1
	}, "the network did not continue on A's decision")
	for _, node := range []*Node{nodeX, nodeA, nodeB} {
		if got := balance(node.Snapshot().Accounts, "merchant"); got.Cmp(rational("4.9")) != 0 {
			t.Fatalf("every node must hold A's decision at height %d: merchant = %s", heightH, got.RatString())
		}
	}
}

// A total outage right after every validator precommitted D1 at height H, before any precommit
// arrived anywhere, so nobody decided. After the restart each node is locked on D1 and has its block
// from the sign log: the height must still decide, on D1, and no node may vote against its own
// logged votes. (A node that decides stops re-gossiping that height's votes; one left short of a
// quorum catches up from a checkpoint, as on a real network.)
func TestTotalOutageAfterUnanimousPrecommitStillDecides(t *testing.T) {
	network := newRestartNetwork(t, 1, 1, 1, 1)
	const heightH = int64(3)
	var powered atomic.Bool
	network.filter = func(from, to string, message Message) bool {
		return powered.Load() || message.Vote == nil || message.Vote.Height != heightH || message.Vote.Type != consensus.Precommit
	}
	nodes := map[string]*Node{}
	for _, name := range network.names {
		nodes[name] = network.start(name, network)
		if _, err := nodes[name].AdmitPayment(network.pay(heightH)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 20*time.Second, func() bool {
		for _, name := range network.names {
			precommitted := false
			for _, vote := range network.votesBy(name, heightH) {
				precommitted = precommitted || vote.Type == consensus.Precommit && vote.BlockHash != [32]byte{}
			}
			if !precommitted {
				return false
			}
		}
		return true
	}, "not every validator precommitted a block at height H")
	for _, name := range network.names {
		network.powerOff(name, nodes[name])
	}

	powered.Store(true)
	network.serveCheckpoints()
	for _, name := range network.names {
		nodes[name] = network.start(name, network)
	}
	waitFor(t, 20*time.Second, func() bool {
		for _, node := range nodes {
			if node.Snapshot().Height <= heightH {
				return false
			}
		}
		return true
	}, "the height did not decide after the outage")
	for i, name := range network.names {
		if doubleSigned(network.votesBy(name, heightH)) {
			t.Fatalf("validator %d signed against its own vote at height %d", i, heightH)
		}
		if got := balance(nodes[name].Snapshot().Accounts, "merchant"); got.Cmp(rational("4.9")) != 0 {
			t.Fatalf("validator %d did not decide the block everyone precommitted: merchant = %s", i, got.RatString())
		}
	}
}

// The round-0 proposer X (2 of 4 power, so A and B cannot decide without it) loses power right after
// its prevote left. Restarted from scratch it would propose a different block from its now empty
// mempool, with a fresh nonce, and prevote it: two proposals and two prevotes for one round, evidence
// that freezes its name, and a height that never decides. It must not sign anything new for that round,
// and the height must decide: with its sign log, and without it (a dead disk, the snapshot restored
// alone), once the others' re-gossip has brought its proposal and its prevote back.
func TestRestartedProposerDoesNotSignAgainstItself(t *testing.T) {
	for _, keepLog := range []bool{true, false} {
		t.Run(fmt.Sprintf("sign log kept %v", keepLog), func(t *testing.T) {
			network := newRestartNetwork(t, 2, 1, 1)
			validatorX := network.names[0]
			heightH := int64(3)
			for network.validatorSet.ProposerAt(heightH, 0) != validatorX {
				heightH++
			}
			cut := network.powerCut(validatorX, func(message Message) bool { return ownVote(message, validatorX, heightH, consensus.Prevote) })
			nodeX := network.start(validatorX, cut)
			nodeA := network.start(network.names[1], network)
			nodeB := network.start(network.names[2], network)
			if _, err := nodeX.AdmitPayment(network.pay(heightH)); err != nil {
				t.Fatal(err)
			}
			cut.wait(nodeX)
			if !keepLog {
				if err := os.Remove(network.configs[validatorX].SnapshotPath + ".votes"); err != nil {
					t.Fatal(err)
				}
			}

			nodeX = network.start(validatorX, network)
			waitFor(t, 20*time.Second, func() bool {
				return doubleSigned(network.votesBy(validatorX, heightH)) || nodeX.Snapshot().Height > heightH && nodeA.Snapshot().Height > heightH && nodeB.Snapshot().Height > heightH
			}, "the height did not decide after the proposer restarted")
			if doubleSigned(network.votesBy(validatorX, heightH)) {
				t.Fatalf("the restarted proposer signed against its own vote at height %d", heightH)
			}
			network.mutex.Lock()
			proposals := network.proposals
			network.mutex.Unlock()
			oneBlockPerRound(t, proposals)
		})
	}
}

// A vote leaves only once it is in the sign log on disk. While the log cannot be written (here a folder
// stands where its temporary file goes, as a failed disk refuses the write), the node releases no vote
// and commits nothing; once it can, the vote is retried and the height commits.
func TestVoteIsReleasedOnlyOnceLogged(t *testing.T) {
	recorder := &recordingTransport{}
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000")}, 50*time.Millisecond, 0))
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	blocker := config.SnapshotPath + ".votes.tmp" // where the sign log is written first: every write fails
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	node := New(config, recorder)
	node.Start()
	defer node.Stop()
	// Work for height 2, whose proposal shows that height 1 committed.
	keepBusy(t, "validator", node)
	time.Sleep(300 * time.Millisecond) // several timeouts, each retrying the vote
	if recorder.sent(MessageVote) != 0 {
		t.Fatal("a vote was released before it was in the sign log")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return recorder.proposalAt(2) != nil }, "the height did not commit once the sign log could be written")
}

// oneBlockPerRound fails the test if the proposals hold two different blocks, by block hash, for one
// height and round: two signed proposals for one round, which Phase 2 makes evidence against their
// signer.
func oneBlockPerRound(t *testing.T, proposals []ProposalMessage) {
	t.Helper()
	signed := map[[2]int64][32]byte{}
	for _, message := range proposals {
		proposal := message.Proposal
		slot := [2]int64{proposal.Height, int64(proposal.Round)}
		if earlierHash, found := signed[slot]; found && earlierHash != proposal.BlockHash {
			t.Fatalf("the proposer signed two blocks for height %d round %d", proposal.Height, proposal.Round)
		}
		signed[slot] = proposal.BlockHash
	}
}

// A proposal leaves only once it is in the sign log, like a vote. While the log cannot be written (here a
// folder stands where its temporary file goes, as a failed disk refuses the write), the proposer sends
// none. A proposal sent without being on disk would be forgotten: restarted with the disk fixed and
// another mempool, the proposer would sign a different block for the same height and round.
func TestProposalIsSentOnlyOnceLogged(t *testing.T) {
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000"), testAccount("payer", "1000")}, 50*time.Millisecond, 0))
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	blocker := config.SnapshotPath + ".votes.tmp" // where the sign log is written first: every write fails
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(amount string, recorder *recordingTransport) *Node {
		node := New(config, recorder)
		if err := admit(node, signedPayment("payer", "validator", amount, 1)); err != nil {
			t.Fatal(err)
		}
		node.Start()
		return node
	}

	first := &recordingTransport{}
	node := run("10", first)
	time.Sleep(300 * time.Millisecond) // several timeouts, each retrying
	node.Stop()
	if first.sent(MessageProposal) != 0 {
		t.Fatal("a proposal was sent before it was in the sign log")
	}

	if err := os.Remove(blocker); err != nil { // the operator fixes the disk and restarts the node
		t.Fatal(err)
	}
	second := &recordingTransport{}
	node = run("20", second)
	waitFor(t, 5*time.Second, func() bool { return second.proposalAt(1) != nil }, "no proposal once the sign log could be written")
	node.Stop()
	oneBlockPerRound(t, append(first.proposals(), second.proposals()...))

	// Two empty blocks that differ only in their nonce share their state digest, but they are two blocks:
	// once one of them is in the sign log for a round, the other is not signed for it (spec 6.9).
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	logging := New(config, &recordingTransport{})
	emptyBlock := func(nonce byte) ProposalMessage {
		block := ledger.ProposeBlock(config.Chain, logging.state, logging.lastBlockHash, [32]byte{nonce}, ledger.BlockContent{})
		return proposalOf(testSigner("validator"), "validator", 1, 0, block)
	}
	if err := logging.logProposal(emptyBlock(1)); err != nil {
		t.Fatal(err)
	}
	if err := logging.logProposal(emptyBlock(2)); err == nil {
		t.Fatal("the sign log took a second block for round 0 that differs from the logged one only in its nonce")
	}
	if err := logging.logProposal(emptyBlock(1)); err != nil {
		t.Fatalf("the sign log refused the proposal it holds: %v", err)
	}
}

// A proposer restarted at a height whose proposal is in its sign log sends that proposal again, never
// another block for its round, whatever its mempool holds now. Here the other validator is down, so
// the height cannot decide and the proposer stays in the round it proposed in.
func TestRestartedProposerSendsItsLoggedProposalAgain(t *testing.T) {
	chain := staticChain("first", "second")
	genesis := testGenesis([]ledger.Account{testAccount("first", "0"), testAccount("second", "0"), testAccount("payer", "1000")}, 50*time.Millisecond, 0)
	proposer := chain.ValidatorRule.SetFor(genesis).ProposerAt(1, 0)
	config := testConfig(chain, proposer, genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	run := func(amount string, recorder *recordingTransport, failureMessage string) {
		node := New(config, recorder)
		if err := admit(node, signedPayment("payer", proposer, amount, 1)); err != nil {
			t.Fatal(err)
		}
		node.Start()
		waitFor(t, 5*time.Second, func() bool { return recorder.proposalAt(1) != nil }, failureMessage)
		node.Stop()
	}
	first, second := &recordingTransport{}, &recordingTransport{}
	run("10", first, "the proposer sent no proposal for height 1")
	run("20", second, "restarted, the proposer did not send its logged proposal again")
	oneBlockPerRound(t, append(first.proposals(), second.proposals()...))
}

// A sole validator proposes, prevotes and precommits in one step, so its precommit reaches the sign
// log before its proposal is even signed. The log must still hold the block it locked on: if the
// snapshot of that height does not reach the disk, the node keeps the log, and restarted it commits the
// same block again, with the same precommit.
func TestSoleValidatorRestartRecommitsItsLockedBlock(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000"), testAccount("shop", "0")}, time.Second, 0))
	config.SnapshotPath = snapshotPath
	if err := os.Mkdir(snapshotPath+".tmp", 0o700); err != nil { // where the snapshot is written first: every write fails
		t.Fatal(err)
	}
	// Driven directly, without the run loop: startHeight decides and commits height 1 in one call.
	first := New(config, NewHTTPTransport(nil))
	if err := admit(first, signedPayment("validator", "shop", "10", 1)); err != nil {
		t.Fatal(err)
	}
	first.startHeight()
	if first.lastCommit.Height != 1 {
		t.Fatal("premise: the sole validator commits height 1 at once")
	}
	if _, err := os.Stat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("premise: the snapshot of height 1 never reached the disk (%v)", err)
	}

	again := New(config, NewHTTPTransport(nil))
	again.startHeight()
	if again.lastCommit.Height != 1 || again.lastCommit.BlockHash != first.lastCommit.BlockHash {
		t.Fatal("the restarted validator must commit the block it had precommitted, not another one")
	}
	if balance(again.state.Accounts, "shop").Cmp(rational("9.9")) != 0 {
		t.Fatal("the recommitted block must carry the payment")
	}
}

// A validator back from a snapshot without its sign log (a dead disk, or a log an operator deleted)
// may have voted at the height it starts at. It signs nothing for two re-gossip intervals, so that
// peers' re-gossip brings its earlier votes back first, and then signs nothing against them: here it
// is the round's proposer, and would otherwise propose its block and prevote it at once. A new network
// has no snapshot and starts at once.
func TestValidatorBackWithoutItsSignLogWaitsForItsVotes(t *testing.T) {
	network := newHandNet(t, 4)
	proposerIndex := network.proposer(2, 0)
	genesis := testGenesis(network.genesis, 100*time.Millisecond, 0)
	checkpoint := checkpointOf(t, 1, genesis, network.validatorSet.Validators(), nil)
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(snapshotPath, snapshotFile(checkpoint), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testConfig(network.chain, network.name(proposerIndex), genesis)
	config.SnapshotPath = snapshotPath
	recorder := &recordingTransport{}
	node, err := Open(config, recorder)
	if err != nil || node.height() != 2 {
		t.Fatalf("premise: the node opens at height 2 from its snapshot (err %v)", err)
	}
	node.Start()
	earlier := network.vote(proposerIndex, 2, 0, consensus.Prevote, [32]byte{1}) // signed before its log was lost; a peer re-gossips it
	node.SubmitVote(earlier)
	waitFor(t, 5*time.Second, func() bool { return len(recorder.votes(network.name(proposerIndex), 2, 0, consensus.Prevote)) > 1 },
		"the node did not start and re-gossip its votes") // the first copy is the relay of the one it got
	node.Stop()
	for _, vote := range recorder.votes(network.name(proposerIndex), 2, 0, consensus.Prevote) {
		if vote.BlockHash != earlier.BlockHash {
			t.Fatal("the node signed a second prevote for a round it had prevoted in before it lost its sign log")
		}
	}
	if recorder.proposalAt(2) != nil {
		t.Fatal("the node proposed in a round it had prevoted in before it lost its sign log")
	}

	config.Signer, config.Name = network.signers[network.proposer(1, 0)], network.name(network.proposer(1, 0))
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	config.Genesis.Parameters.StepTimeoutMs = time.Hour.Milliseconds()
	fresh := &recordingTransport{}
	node = New(config, fresh)
	node.Start()
	defer node.Stop()
	waitFor(t, 5*time.Second, func() bool { return fresh.proposalAt(1) != nil }, "a node of a new network waited before proposing")
}

// olderSnapshotNode opens validator self of network from a snapshot of height 1 on genesis, its sign log
// gone (an older backup restored on a new disk, say), on transport: it opens at height 2.
func olderSnapshotNode(t *testing.T, network *handNet, self int, genesis ledger.LedgerState, transport Transport) (*Node, Config) {
	t.Helper()
	config := testConfig(network.chain, network.name(self), genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(config.SnapshotPath, snapshotFile(checkpointOf(t, 1, genesis, network.validatorSet.Validators(), nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := Open(config, transport)
	if err != nil || node.height() != 2 {
		t.Fatalf("premise: the node opens at height 2 from its snapshot (err %v)", err)
	}
	return node, config
}

// waitsAfterARestart reports whether a node restarted from config's files now would wait before it signs
// at the height it opens at, as after a lost sign log: its sign log is not for that height.
func waitsAfterARestart(t *testing.T, config Config) bool {
	t.Helper()
	restarted, err := Open(config, &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	restarted.startFirstHeight()
	return restarted.heightStarted == 0
}

// A validator back without its sign log from a snapshot below the network may have signed at the
// network's height too, and its catch-up takes it there before its peers' re-gossip has brought those
// signatures back. So it waits there too, a full wait, whether the catch-up comes during
// its first wait, whose timer then starts nothing, or once it runs, or idles at, the height it came back
// at. A peer's vote does not start the height meanwhile, and the node's sign log stays below it, so that
// a restart during the wait waits too. Here the node proposed at that height before it lost its log: once
// the re-gossip has brought its proposal back, it sends that proposal again, never another block for the
// round.
func TestValidatorBackWithoutItsSignLogWaitsAtTheHeightItCatchesUpTo(t *testing.T) {
	network := newHandNet(t, 4)
	const networkHeight = 4 // the network's height in progress, where the node proposed before it lost its log
	self := network.proposer(networkHeight, 0)
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	checkpoint := checkpointOf(t, networkHeight-1, genesis, network.validatorSet.Validators(), nil) // the network's last commit
	earlier := network.sign(self, networkHeight, 0, ledger.ProposeBlock(network.chain, checkpoint.State, checkpoint.Commit.BlockHash, [32]byte{9}, ledger.BlockContent{}))
	peerVote := network.vote((self+1)%4, networkHeight, 0, consensus.Prevote, earlier.Proposal.BlockHash)
	for _, atFirstHeight := range []string{"waiting", "running", "idle"} {
		t.Run("caught up while "+atFirstHeight+" at the height it came back at", func(t *testing.T) {
			recorder := &recordingTransport{}
			node, config := olderSnapshotNode(t, network, self, genesis, recorder)
			if atFirstHeight != "idle" {
				if err := admit(node, signedPayment(network.name(0), "shop", "1", networkHeight)); err != nil {
					t.Fatal(err) // work for the network's height, which keeps the height the node came back at running
				}
			}
			node.started = true
			node.startFirstHeight() // the wait at height 2: its timer would fire in an hour
			if atFirstHeight != "waiting" {
				node.handle(event{kind: eventNextHeight, height: 2})                                     // the wait's timer
				node.handle(event{kind: eventTimeout, step: consensus.StepPropose, round: 0, height: 2}) // no proposal comes: it prevotes nil
				if node.idle != (atFirstHeight == "idle") || (node.signLog.Height == 2) != (atFirstHeight == "running") {
					t.Fatal("premise: with work the node runs height 2 and signs there, in a new sign log; without, it idles")
				}
			}

			node.handle(event{kind: eventCatchUp, checkpoint: &checkpoint})
			node.handle(event{kind: eventVote, vote: &peerVote}) // a peer's re-gossip of the height's votes
			if atFirstHeight == "waiting" {
				node.handle(event{kind: eventNextHeight, height: 2}) // the timer of its first wait
			}
			if node.height() != networkHeight || node.driver != nil || recorder.proposalAt(networkHeight) != nil {
				t.Fatal("the node started the height it caught up to before its wait there was over, before its earlier votes and proposals could come back")
			}
			if !waitsAfterARestart(t, config) {
				t.Error("restarted during the wait, the node would start the height at once: its sign log is for that height")
			}
			node.handle(event{kind: eventProposal, proposal: &earlier})      // a peer's re-gossip of the round's proposal
			node.handle(event{kind: eventNextHeight, height: networkHeight}) // the wait's timer
			if prevotes := recorder.votes(network.name(self), networkHeight, 0, consensus.Prevote); len(prevotes) != 1 || prevotes[0].BlockHash != earlier.Proposal.BlockHash {
				t.Fatalf("once the wait was over, the node did not resume from its earlier proposal: it prevoted %v", prevotes)
			}
			oneBlockPerRound(t, append([]ProposalMessage{earlier}, recorder.proposals()...))
		})
	}
}

// peeredRecorder is a recordingTransport with gossip peers, which a running catch-up asks (see
// catchUpCheckpoint).
type peeredRecorder struct { // implements Transport, with recordingTransport's Broadcast
	recordingTransport
	peers [][]string
}

func (recorder *peeredRecorder) Peers() [][]string { return recorder.peers }

// The same, end to end. A validator back without its sign log from a snapshot below the network, with
// gossip peers and no bootstrap peer, starts the height it came back at after its first wait; its running
// catch-up then takes it to the network's height, where it proposed before it lost its log. A peer
// re-gossips that proposal all along, as peers do with the current round's proposal: the node waits,
// takes it back, and sends it again.
func TestValidatorBackWithoutItsSignLogResumesItsProposalAfterACatchUp(t *testing.T) {
	network := newHandNet(t, 4)
	const networkHeight = 4
	self := network.proposer(networkHeight, 0)
	reGossipInterval := 300 * time.Millisecond * testTimeSlack
	genesis := testGenesis(network.genesis, reGossipInterval, 100*time.Millisecond) // the first catch-up comes after 3 s, after the first wait
	checkpoint := checkpointOf(t, networkHeight-1, genesis, network.validatorSet.Validators(), nil)
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		servePeer(responseWriter, request, checkpoint)
	}))
	defer peer.Close()
	earlier := network.sign(self, networkHeight, 0, ledger.ProposeBlock(network.chain, checkpoint.State, checkpoint.Commit.BlockHash, [32]byte{9}, ledger.BlockContent{}))

	recorder := &peeredRecorder{peers: [][]string{{peer.URL}}}
	node, _ := olderSnapshotNode(t, network, self, genesis, recorder)
	if err := admit(node, signedPayment(network.name(0), "shop", "1", networkHeight)); err != nil {
		t.Fatal(err) // work the network carries for its height
	}
	node.Start()
	defer node.Stop()
	stopReGossip := make(chan struct{})
	defer close(stopReGossip)
	go func() {
		for {
			select {
			case <-stopReGossip:
				return
			case <-time.After(reGossipInterval / 4):
				node.SubmitProposal(earlier)
			}
		}
	}()

	waitFor(t, 10*time.Second, func() bool { return len(recorder.votes(network.name(self), networkHeight, 0, consensus.Prevote)) > 0 },
		"the node did not resume the height it caught up to")
	if prevote := recorder.votes(network.name(self), networkHeight, 0, consensus.Prevote)[0]; prevote.BlockHash != earlier.Proposal.BlockHash {
		t.Errorf("the node prevoted %x at height %d, not its earlier proposal %x", prevote.BlockHash[:4], networkHeight, earlier.Proposal.BlockHash[:4])
	}
	oneBlockPerRound(t, append([]ProposalMessage{earlier}, recorder.proposals()...))
}

// A validator back without its sign log one height behind the network (from a backupPath mirror, which
// skips commits while one is in flight, say) decides that height at the end of its first wait, from the
// last commit its peers re-send, and so commits into the network's height, where it signed too: its own
// earlier prevote there, or its proposal, came back during the wait. So that height does not start after
// the block interval: the node waits there too, two re-gossip intervals, and its sign log stays below
// the height meanwhile, so that a restart during the wait waits too. When the re-gossip brings its
// signature again, it resumes from it, and signs no other block or prevote for that round, which it
// proposes in. Past that height it waits no more, whatever a vote in its name that it never signed
// claims.
func TestValidatorBackWithoutItsSignLogWaitsAtTheHeightItCommitsInto(t *testing.T) {
	network := newHandNet(t, 4)
	const networkHeight = 3
	self := network.proposer(networkHeight, 0)
	genesis := testGenesis(network.genesis, network.timeout, time.Millisecond) // a wait of an hour, a block interval of 1 ms
	for _, cameBack := range []string{"prevote", "proposal"} {
		t.Run("its "+cameBack+" came back", func(t *testing.T) {
			recorder := &recordingTransport{}
			node, config := olderSnapshotNode(t, network, self, genesis, recorder)
			node.started = true
			node.startFirstHeight() // the wait at height 2: its timer would fire in an hour

			// What the node signed at height 3 before it lost its log: a block on the state after height 2,
			// and its prevote for it. Its peers re-gossip one of them, with the last commit of height 2.
			lastProposal := network.proposal(node, 2, 0, ledger.BlockContent{})
			stateAfterIt, err := ledger.ApplyBlock(network.chain, node.state, node.lastBlockHash, lastProposal.Block)
			if err != nil {
				t.Fatal(err)
			}
			earlier := network.sign(self, networkHeight, 0, ledger.ProposeBlock(network.chain, stateAfterIt, lastProposal.Proposal.BlockHash, [32]byte{9}, ledger.BlockContent{}))
			earlierPrevote := network.vote(self, networkHeight, 0, consensus.Prevote, earlier.Proposal.BlockHash)
			reGossip := func() {
				if cameBack == "prevote" {
					node.handle(event{kind: eventVote, vote: &earlierPrevote})
				} else {
					node.handle(event{kind: eventProposal, proposal: &earlier})
				}
			}
			reGossip()
			node.handle(event{kind: eventProposal, proposal: &lastProposal})
			network.voteAll(node, self, 2, 0, consensus.Precommit, lastProposal.Proposal.BlockHash)
			forged := network.vote((self+1)%4, 1000, 0, consensus.Prevote, [32]byte{1})
			forged.Validator = network.name(self) // a vote in its name, far ahead, that it never signed
			node.handle(event{kind: eventVote, vote: &forged})
			node.handle(event{kind: eventNextHeight, height: 2}) // the wait's timer: it decides height 2 at once
			if node.height() != networkHeight {
				t.Fatal("premise: the node decides height 2 from the last commit its peers re-sent")
			}
			if err := admit(node, signedPayment(network.name(0), "shop", "1", networkHeight)); err != nil {
				t.Fatal(err) // work the network carries for its height
			}
			if !waitsAfterARestart(t, config) {
				t.Error("restarted during the wait, the node would start the height at once: its sign log is for that height")
			}
			select {
			case started := <-node.events: // the block interval's timer, if the height starts after it
				node.handle(started)
			case <-time.After(200 * time.Millisecond * testTimeSlack):
			}

			reGossip()                                                       // the next re-gossip
			node.handle(event{kind: eventNextHeight, height: networkHeight}) // the wait's timer
			if node.driver == nil {
				t.Fatal("the height did not start once the wait was over")
			}
			oneBlockPerRound(t, append([]ProposalMessage{earlier}, recorder.proposals()...))
			for _, vote := range recorder.votes(network.name(self), networkHeight, 0, consensus.Prevote) {
				if vote.BlockHash != earlier.Proposal.BlockHash {
					t.Fatal("the node signed a second prevote for a round it had signed in before it lost its sign log")
				}
			}

			node.handle(event{kind: eventProposal, proposal: &earlier}) // the block, if the node lacks it
			network.voteAll(node, self, networkHeight, 0, consensus.Prevote, earlier.Proposal.BlockHash)
			network.voteAll(node, self, networkHeight, 0, consensus.Precommit, earlier.Proposal.BlockHash)
			if node.height() != networkHeight+1 {
				t.Fatal("premise: height 3 decides the block the node proposed before it lost its sign log")
			}
			select {
			case started := <-node.events:
				if started.kind != eventNextHeight || started.height != networkHeight+1 {
					t.Fatalf("premise: the first timer to fire is the one that starts height 4 (event kind %d)", started.kind)
				}
			case <-time.After(200 * time.Millisecond * testTimeSlack):
				t.Error("past the height it signed at, the node did not start the next height after the block interval: its waits did not end")
			}
		})
	}
}

// A validator back without its sign log notes that it proposed above the height it came back at, its
// proposal there re-gossiped to it, and it signs at the height it came back at, so it has a sign log for
// that height again. Restarted once more before that height commits, it starts the height at once, from
// that log, and must still wait at the height above, whether a commit or a catch-up takes it there. So
// the sign log keeps the height it may have signed up to: each log written says it, and a log of the
// height in progress is written again when the node notes a later signature of its own. Here it notes
// that signature during its first wait, or only once it has signed at the height it came back at.
func TestValidatorBackWithoutItsSignLogStillWaitsAfterAnotherRestart(t *testing.T) {
	network := newHandNet(t, 4)
	const networkHeight = 3
	self := network.proposer(networkHeight, 0)
	genesis := testGenesis(network.genesis, network.timeout, time.Millisecond) // a wait of an hour, a block interval of 1 ms
	checkpoint := checkpointOf(t, networkHeight-1, genesis, network.validatorSet.Validators(), nil)
	earlier := network.sign(self, networkHeight, 0, ledger.ProposeBlock(network.chain, checkpoint.State, checkpoint.Commit.BlockHash, [32]byte{9}, ledger.BlockContent{}))
	for _, noted := range []string{"during its wait", "once it signed"} {
		for _, movedOn := range []string{"commit", "catch-up"} {
			t.Run("noted "+noted+", then a "+movedOn, func(t *testing.T) {
				node, config := olderSnapshotNode(t, network, self, genesis, &recordingTransport{})
				keepBusyByHand(t, "shop", node)
				node.started = true
				node.startFirstHeight() // the wait at height 2
				if noted == "during its wait" {
					node.handle(event{kind: eventProposal, proposal: &earlier}) // a peer at height 3 re-gossips it
				}
				node.handle(event{kind: eventNextHeight, height: 2})                                     // the wait's timer
				node.handle(event{kind: eventTimeout, step: consensus.StepPropose, round: 0, height: 2}) // no proposal comes: it prevotes nil
				if noted == "once it signed" {
					node.handle(event{kind: eventProposal, proposal: &earlier})
				}
				if node.signLog.Height != 2 || node.mayHaveSignedUpTo != networkHeight {
					t.Fatal("premise: the node signed at height 2, in a new sign log, and noted it may have signed up to height 3")
				}

				recorder := &recordingTransport{}
				restarted, err := Open(config, recorder)
				if err != nil {
					t.Fatal(err)
				}
				keepBusyByHand(t, "shop", restarted)
				restarted.started = true
				if restarted.startFirstHeight(); restarted.driver == nil {
					t.Fatal("premise: restarted with the sign log of its height, the node starts that height at once")
				}
				if movedOn == "commit" {
					lastProposal := network.proposal(restarted, 2, 0, ledger.BlockContent{})
					restarted.handle(event{kind: eventProposal, proposal: &lastProposal})
					network.voteAll(restarted, self, 2, 0, consensus.Precommit, lastProposal.Proposal.BlockHash)
					select {
					case started := <-restarted.events: // the block interval's timer, if the height starts after it
						restarted.handle(started)
					case <-time.After(200 * time.Millisecond * testTimeSlack):
					}
				} else {
					restarted.handle(event{kind: eventCatchUp, checkpoint: &checkpoint})
				}
				if restarted.height() != networkHeight || restarted.driver != nil || recorder.proposalAt(networkHeight) != nil {
					t.Fatal("restarted once more, the node started height 3 at once: it forgot that it may have signed there")
				}
			})
		}
	}
}

// The height a node back without its sign log may have signed up to never falls: a vote of its own for a
// height below the highest it has noted leaves that one.
func TestValidatorBackWithoutItsSignLogNeverLowersItsBound(t *testing.T) {
	network := newHandNet(t, 4)
	node, _ := olderSnapshotNode(t, network, 0, testGenesis(network.genesis, network.timeout, time.Hour), &recordingTransport{})
	node.started = true
	node.startFirstHeight() // the wait at height 2
	higher, lower := network.vote(0, 5, 0, consensus.Prevote, [32]byte{1}), network.vote(0, 3, 0, consensus.Prevote, [32]byte{1})
	node.handle(event{kind: eventVote, vote: &higher})
	if node.handle(event{kind: eventVote, vote: &lower}); node.mayHaveSignedUpTo != 5 {
		t.Fatalf("a vote of its own for height 3 moved the height it may have signed up to from 5 to %d", node.mayHaveSignedUpTo)
	}
}

// While a validator back without its sign log waits, its peers may be in any round of the height, and
// re-gossip their round's proposal: its own, if it proposed there before it lost its log. It takes that
// proposal back whatever the round, though while no driver runs it takes no other proposal beyond round
// 1; once the wait is over, the peers' votes take it to that round, and it sends the same proposal again,
// never another block. Only its key signs its own proposals, so nobody can flood it with them. Here the
// round is 2, during its first wait or during the wait after a catch-up.
func TestValidatorBackWithoutItsSignLogTakesBackItsProposalOfAnyRound(t *testing.T) {
	network := newHandNet(t, 4)
	const round = 2
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	for _, afterACatchUp := range []bool{false, true} {
		t.Run(fmt.Sprintf("after a catch-up %v", afterACatchUp), func(t *testing.T) {
			height := int64(2) // the height it came back at, or the network's after the catch-up
			if afterACatchUp {
				height = 4
			}
			self := network.proposer(height, round)
			if network.proposer(height, 0) == self || network.proposer(height, 1) == self {
				t.Fatal("premise: the node proposes in no earlier round of the height")
			}
			recorder := &recordingTransport{}
			node, _ := olderSnapshotNode(t, network, self, genesis, recorder)
			if err := admit(node, signedPayment(network.name(0), "shop", "1", height)); err != nil {
				t.Fatal(err) // work for the height
			}
			node.started = true
			node.startFirstHeight() // the wait at height 2
			if afterACatchUp {
				checkpoint := checkpointOf(t, height-1, genesis, network.validatorSet.Validators(), nil)
				node.handle(event{kind: eventCatchUp, checkpoint: &checkpoint})
			}

			// Its peers, in round 2, re-gossip its proposal and their prevotes for it.
			earlier := network.sign(self, height, round, ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{9}, ledger.BlockContent{}))
			node.handle(event{kind: eventProposal, proposal: &earlier})
			for _, peer := range []int{(self + 1) % 4, (self + 2) % 4} {
				prevote := network.vote(peer, height, round, consensus.Prevote, earlier.Proposal.BlockHash)
				node.handle(event{kind: eventVote, vote: &prevote})
			}
			node.handle(event{kind: eventNextHeight, height: height}) // the wait's timer
			oneBlockPerRound(t, append([]ProposalMessage{earlier}, recorder.proposals()...))
			if prevotes := recorder.votes(network.name(self), height, round, consensus.Prevote); len(prevotes) != 1 || prevotes[0].BlockHash != earlier.Proposal.BlockHash {
				t.Fatalf("once the wait was over, the node did not resume round %d from its earlier proposal: it prevoted %v", round, prevotes)
			}
		})
	}
}

// The same, end to end. Static X:2 A:1 B:1, so the quorum needs X. At height H the proposals of rounds 0
// and 1 are lost, X proposes in round 2 and loses power as soon as its proposal has left, before its
// prevote. A and B take the proposal and stay in round 2, short of a quorum, re-gossiping it. X comes back
// from its latest snapshot without its sign log: it takes its proposal back during its wait and sends
// it again, and the height decides on it.
func TestValidatorBackWithoutItsSignLogResendsItsProposalOfRoundTwo(t *testing.T) {
	network := newRestartNetwork(t, 2, 1, 1)
	validatorX := network.names[0]
	heightH := int64(3)
	for network.validatorSet.ProposerAt(heightH, 2) != validatorX {
		heightH++
	}
	var lossOver atomic.Bool
	network.filter = func(from, to string, message Message) bool {
		return lossOver.Load() || message.Proposal == nil || message.Proposal.Proposal.Height != heightH || message.Proposal.Proposal.Round >= 2
	}
	cut := network.powerCut(validatorX, func(message Message) bool {
		return message.Proposal != nil && message.Proposal.Proposal.Height == heightH && message.Proposal.Proposal.Round == 2 && message.Proposal.Proposal.Proposer == validatorX
	})
	nodeX := network.start(validatorX, cut)
	nodeA := network.start(network.names[1], network)
	nodeB := network.start(network.names[2], network)
	keepBusy(t, "payer", nodeX, nodeA, nodeB)
	cut.wait(nodeX)
	lossOver.Store(true)
	if err := os.Remove(network.configs[validatorX].SnapshotPath + ".votes"); err != nil {
		t.Fatal(err)
	}

	nodeX = network.start(validatorX, network)
	waitFor(t, 20*time.Second, func() bool {
		return nodeX.Snapshot().Height > heightH && nodeA.Snapshot().Height > heightH && nodeB.Snapshot().Height > heightH
	}, "the height did not decide once X was back")
	network.mutex.Lock()
	proposals := network.proposals
	network.mutex.Unlock()
	oneBlockPerRound(t, proposals)
	if doubleSigned(network.votesBy(validatorX, heightH)) {
		t.Fatalf("X signed two different votes for one round and type at height %d", heightH)
	}
}

// A validator back without its sign log takes back, during its wait, its own proposal of round 0 and its
// prevote for it, and resumes from them; then it signs a nil precommit, its first signature since, which
// starts a new sign log for the height. Restarted once more before the height ends, it finds that log
// and starts the height at once, from it: so the log must hold what it took back too, or the node,
// round 0's proposer, would propose another block for that round. Here it takes them back during its
// first wait, or during the wait after a catch-up.
func TestValidatorBackWithoutItsSignLogLogsWhatItTookBack(t *testing.T) {
	network := newHandNet(t, 4)
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	for _, afterACatchUp := range []bool{false, true} {
		t.Run(fmt.Sprintf("after a catch-up %v", afterACatchUp), func(t *testing.T) {
			height := int64(2)
			if afterACatchUp {
				height = 4
			}
			self := network.proposer(height, 0)
			node, config := olderSnapshotNode(t, network, self, genesis, &recordingTransport{})
			keepBusyByHand(t, "shop", node)
			node.started = true
			node.startFirstHeight() // the wait at height 2
			if afterACatchUp {
				checkpoint := checkpointOf(t, height-1, genesis, network.validatorSet.Validators(), nil)
				node.handle(event{kind: eventCatchUp, checkpoint: &checkpoint})
			}
			earlier := network.sign(self, height, 0, ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{9}, ledger.BlockContent{}))
			earlierPrevote := network.vote(self, height, 0, consensus.Prevote, earlier.Proposal.BlockHash)
			node.handle(event{kind: eventProposal, proposal: &earlier}) // its peers' re-gossip
			node.handle(event{kind: eventVote, vote: &earlierPrevote})
			node.handle(event{kind: eventNextHeight, height: height}) // the wait's timer
			for _, peer := range []int{(self + 1) % 4, (self + 2) % 4} {
				nilPrevote := network.vote(peer, height, 0, consensus.Prevote, [32]byte{})
				node.handle(event{kind: eventVote, vote: &nilPrevote})
			}
			node.handle(event{kind: eventTimeout, step: consensus.StepPrevote, round: 0, height: height}) // it precommits nil
			if node.signLog.Height != height || len(node.signLog.Votes) == 0 || node.signLog.Votes[len(node.signLog.Votes)-1].Type != consensus.Precommit {
				t.Fatalf("premise: the node's new sign log for height %d ends with its nil precommit", height)
			}

			recorder := &recordingTransport{}
			restarted, err := Open(config, recorder)
			if err != nil {
				t.Fatal(err)
			}
			keepBusyByHand(t, "shop", restarted)
			restarted.started = true
			if restarted.startFirstHeight(); restarted.driver == nil {
				t.Fatal("premise: restarted with the sign log of its height, the node starts that height at once")
			}
			oneBlockPerRound(t, append([]ProposalMessage{earlier}, recorder.proposals()...))
			for _, vote := range recorder.votes(network.name(self), height, 0, consensus.Prevote) {
				if vote.BlockHash != earlierPrevote.BlockHash {
					t.Fatal("restarted once more, the node signed a second prevote for round 0")
				}
			}
		})
	}
}

// A validator back without its sign log that commits into a height it waits at keeps its sign log below
// that height meanwhile. A signature of its own for a later height noted during that wait raises the
// bound, which must reach that log too: a restart during the wait reads the bound from it.
func TestValidatorBackWithoutItsSignLogKeepsABoundNotedDuringALaterWait(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(3, 0)
	genesis := testGenesis(network.genesis, network.timeout, time.Millisecond)
	checkpoint := checkpointOf(t, 2, genesis, network.validatorSet.Validators(), nil)
	atThree := network.sign(self, 3, 0, ledger.ProposeBlock(network.chain, checkpoint.State, checkpoint.Commit.BlockHash, [32]byte{9}, ledger.BlockContent{}))
	node, config := olderSnapshotNode(t, network, self, genesis, &recordingTransport{})
	keepBusyByHand(t, "shop", node)
	node.started = true
	node.startFirstHeight()                                     // the wait at height 2
	node.handle(event{kind: eventProposal, proposal: &atThree}) // noted: it may have signed up to height 3
	node.handle(event{kind: eventNextHeight, height: 2})
	node.handle(event{kind: eventTimeout, step: consensus.StepPropose, round: 0, height: 2}) // a nil prevote, in a sign log for height 2
	lastProposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &lastProposal})
	network.voteAll(node, self, 2, 0, consensus.Precommit, lastProposal.Proposal.BlockHash)
	if node.height() != 3 || node.driver != nil || node.signLog.Height != 2 {
		t.Fatal("premise: the node committed into height 3 and waits there, its sign log at height 2")
	}
	atFour := network.vote(self, 4, 0, consensus.Prevote, [32]byte{4})
	if node.handle(event{kind: eventVote, vote: &atFour}); node.mayHaveSignedUpTo != 4 {
		t.Fatal("premise: noted during the wait at height 3, it may have signed up to height 4")
	}
	restarted, err := Open(config, &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.mayHaveSignedUpTo != 4 {
		t.Errorf("restarted during the wait at height 3, the node forgot that it may have signed at height 4 (it takes %d)", restarted.mayHaveSignedUpTo)
	}
}

// Restarted, twice, during the wait after a commit into a height it may have signed at, its sign log
// below that height and its bound above it, a validator back without its sign log keeps the bound,
// and still waits at the height above once it commits into it: loadSignLog takes the bound back before
// it empties the lower log, which so keeps it, and the wait at the height the node opens at does not
// lower it.
func TestValidatorBackWithoutItsSignLogKeepsItsBoundThroughRestartsDuringAWait(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(4, 0)
	genesis := testGenesis(network.genesis, network.timeout, time.Millisecond)
	atFour := network.vote(self, 4, 0, consensus.Prevote, [32]byte{4})
	node, config := olderSnapshotNode(t, network, self, genesis, &recordingTransport{})
	keepBusyByHand(t, "shop", node)
	node.started = true
	node.startFirstHeight()                            // the wait at height 2
	node.handle(event{kind: eventVote, vote: &atFour}) // noted: it may have signed up to height 4
	node.handle(event{kind: eventNextHeight, height: 2})
	node.handle(event{kind: eventTimeout, step: consensus.StepPropose, round: 0, height: 2}) // a nil prevote, in a sign log for height 2
	lastProposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &lastProposal})
	network.voteAll(node, self, 2, 0, consensus.Precommit, lastProposal.Proposal.BlockHash)
	if node.height() != 3 || node.driver != nil || node.signLog.Height != 2 || node.mayHaveSignedUpTo != 4 {
		t.Fatal("premise: the node waits at height 3 after the commit, its sign log at height 2, its bound 4")
	}
	for restart := 1; restart <= 2; restart++ {
		restarted, err := Open(config, &recordingTransport{})
		if err != nil {
			t.Fatal(err)
		}
		restarted.started = true
		if restarted.startFirstHeight(); restarted.mayHaveSignedUpTo != 4 || restarted.driver != nil {
			t.Fatalf("restart %d during the wait at height 3: the bound is %d, want 4, and the node waits: %v", restart, restarted.mayHaveSignedUpTo, restarted.driver == nil)
		}
		node = restarted
	}
	keepBusyByHand(t, "shop", node)
	node.handle(event{kind: eventNextHeight, height: 3}) // the wait's timer
	proposalAtThree := network.proposal(node, 3, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposalAtThree})
	network.voteAll(node, self, 3, 0, consensus.Precommit, proposalAtThree.Proposal.BlockHash)
	if node.height() != 4 {
		t.Fatal("premise: height 3 decides")
	}
	select {
	case started := <-node.events: // the block interval's timer, if the height starts after it
		node.handle(started)
	case <-time.After(200 * time.Millisecond * testTimeSlack):
	}
	if node.driver != nil {
		t.Fatal("after the restarts, height 4 started after the block interval: the node forgot that it may have signed there")
	}
}

// The normal path. A validator that never lost its sign log keeps its bound at 0, in memory and in its
// log, never waits after a commit, and an ordinary restart starts at once. An observer, with no sign log,
// waits at the height it opens at and once after a catch-up from there, then never again.
func TestSignLogOfANodeThatNeverLostItHoldsNoBound(t *testing.T) {
	network := newHandNet(t, 4)
	genesis := testGenesis(network.genesis, network.timeout, time.Millisecond)
	self := network.nonProposer(1, 2)
	config := testConfig(network.chain, network.name(self), genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	node := New(config, &recordingTransport{})
	keepBusyByHand(t, "shop", node)
	node.started = true
	if node.startFirstHeight(); node.driver == nil {
		t.Fatal("a node of a new network waited")
	}
	for height := int64(1); height <= 2; height++ {
		network.commit(node, self, height)
		select {
		case started := <-node.events: // the block interval's timer
			node.handle(started)
		case <-time.After(200 * time.Millisecond * testTimeSlack):
			t.Fatalf("height %d did not start after the block interval", height+1)
		}
		if node.driver == nil || node.mayHaveSignedUpTo != 0 {
			t.Fatalf("after committing height %d the node runs no driver, or takes a bound (%d)", height, node.mayHaveSignedUpTo)
		}
	}
	data, err := os.ReadFile(config.SnapshotPath + ".votes")
	if err != nil || !strings.Contains(string(data), `"MayHaveSignedUpTo":0`) {
		t.Fatalf("the sign log does not hold a bound of 0 (%v)", err)
	}
	restarted, err := Open(config, &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	keepBusyByHand(t, "shop", restarted)
	restarted.started = true
	if restarted.startFirstHeight(); restarted.driver == nil || restarted.mayHaveSignedUpTo != 0 {
		t.Fatalf("an ordinary restart waited, or took a bound (%d)", restarted.mayHaveSignedUpTo)
	}

	observerConfig := testConfig(network.chain, "observer", genesis)
	observerConfig.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(observerConfig.SnapshotPath, snapshotFile(checkpointOf(t, 1, genesis, network.validatorSet.Validators(), nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	observer := New(observerConfig, &recordingTransport{})
	keepBusyByHand(t, "shop", observer)
	observer.started = true
	if observer.startFirstHeight(); observer.driver != nil {
		t.Fatal("the observer did not wait at the height it opened at")
	}
	checkpoint := checkpointOf(t, 3, genesis, network.validatorSet.Validators(), nil)
	if observer.handle(event{kind: eventCatchUp, checkpoint: &checkpoint}); observer.height() != 4 || observer.driver != nil {
		t.Fatal("the observer did not wait once after its catch-up from the height it opened at")
	}
	observer.handle(event{kind: eventNextHeight, height: 4}) // the wait's timer
	network.commit(observer, -1, 4)
	select {
	case started := <-observer.events: // the block interval's timer
		observer.handle(started)
	case <-time.After(200 * time.Millisecond * testTimeSlack):
		t.Fatal("the observer waited after a commit past the height it opened at")
	}
	if observer.driver == nil {
		t.Fatal("height 5 did not start after the block interval")
	}
}

// A validator back without its sign log waits two re-gossip intervals, not one, at each height it may
// have signed at: each peer re-gossips once an interval, but its ticks drift later, so a wait of one
// interval can end just before a peer's tick and miss what it would have brought back. Here the wait at
// the height it comes back at, and the window after its commit into the next one, where it signed too.
func TestValidatorBackWithoutItsSignLogWaitsTwoReGossipIntervals(t *testing.T) {
	network := newHandNet(t, 4)
	const reGossipInterval = 100 * time.Millisecond
	self := network.proposer(3, 0)
	genesis := testGenesis(network.genesis, reGossipInterval, time.Millisecond)
	node, _ := olderSnapshotNode(t, network, self, genesis, &recordingTransport{})
	keepBusyByHand(t, "shop", node)
	// startedAfter hands the node the event that starts height, among those it posts, and returns how
	// long after since it came.
	startedAfter := func(height int64, since time.Time) time.Duration {
		t.Helper()
		deadline := time.After(10 * time.Second * testTimeSlack)
		for {
			select {
			case posted := <-node.events:
				if posted.kind == eventNextHeight && posted.height == height {
					waited := time.Since(since)
					node.handle(posted)
					return waited
				}
			case <-deadline:
				t.Fatalf("height %d never started", height)
			}
		}
	}
	node.started = true
	since := time.Now()
	node.startFirstHeight() // the wait at height 2
	atThree := network.vote(self, 3, 0, consensus.Prevote, [32]byte{3})
	node.handle(event{kind: eventVote, vote: &atThree}) // noted: it may have signed up to height 3
	if waited := startedAfter(2, since); waited < 2*reGossipInterval {
		t.Errorf("at the height it came back at, the node waited %v, less than two re-gossip intervals", waited)
	}
	lastProposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &lastProposal})
	since = time.Now()
	if network.voteAll(node, self, 2, 0, consensus.Precommit, lastProposal.Proposal.BlockHash); node.height() != 3 {
		t.Fatal("premise: height 2 decides")
	}
	if waited := startedAfter(3, since); waited < 2*reGossipInterval {
		t.Errorf("after its commit into height 3, the node waited %v, less than two re-gossip intervals", waited)
	}
}

// While a validator back without its sign log waits, it takes back a proposal in its own name for a
// round past round 1, but within limits: not another validator's, not one in its name under another key,
// not one for a round it does not propose, not one past MaxRoundLookahead, and none past the round after
// the driver's once the height runs. It takes one block per round, relays each once, and keeps a second
// block of its own for a round it holds one for as a proof.
func TestValidatorBackWithoutItsSignLogTakesBackOnlyItsOwnProposalsOfLaterRounds(t *testing.T) {
	network := newHandNet(t, 4)
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	const height = 2
	self := network.proposer(height, 3)
	var ownRounds []int32 // the rounds from 2 to 39 the node proposes in
	for round := int32(2); round < 40; round++ {
		if network.proposer(height, round) == self {
			ownRounds = append(ownRounds, round)
		}
	}
	notItsRound := int32(2) // a round past 1 that another validator proposes in
	for network.proposer(height, notItsRound) == self {
		notItsRound++
	}
	other := network.proposer(height, notItsRound)
	beyond := consensus.MaxRoundLookahead + 1 // a round past MaxRoundLookahead that the node proposes in
	for network.proposer(height, beyond) != self {
		beyond++
	}
	recorder := &recordingTransport{}
	node, _ := olderSnapshotNode(t, network, self, genesis, recorder)
	keepBusyByHand(t, "shop", node)
	node.started = true
	node.startFirstHeight() // the wait at height 2
	block := func(nonce byte) ledger.Block {
		return ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{nonce}, ledger.BlockContent{})
	}
	if node.onProposal(network.sign(other, height, notItsRound, block(1))) {
		t.Errorf("during the wait the node took another validator's proposal of round %d", notItsRound)
	}
	if node.onProposal(proposalOf(network.signers[other], network.name(self), height, ownRounds[0], block(2))) {
		t.Error("during the wait the node took a proposal in its name signed under another key")
	}
	if node.onProposal(network.sign(self, height, notItsRound, block(3))) {
		t.Errorf("during the wait the node took its own proposal of round %d, which it does not propose", notItsRound)
	}
	if node.onProposal(network.sign(self, height, beyond, block(4))) {
		t.Errorf("during the wait the node took its own proposal of round %d, past MaxRoundLookahead", beyond)
	}
	for _, round := range ownRounds {
		own := network.sign(self, height, round, block(byte(10+round)))
		node.handle(event{kind: eventProposal, proposal: &own})
		node.handle(event{kind: eventProposal, proposal: &own}) // a copy
	}
	if len(node.heightProposals) != len(ownRounds) || recorder.sent(MessageProposal) != len(ownRounds) {
		t.Errorf("of its own proposals for rounds %v the node held %d and relayed %d", ownRounds, len(node.heightProposals), recorder.sent(MessageProposal))
	}
	if node.onProposal(network.sign(self, height, ownRounds[0], block(99))) {
		t.Error("the node took a second block of its own for a round it holds one for")
	}
	if proof, found := node.pendingProofs[network.name(self)]; !found || proof.ProposalEvidence == nil {
		t.Error("the node kept no proof of its own second block for a round")
	}
	if node.handle(event{kind: eventNextHeight, height: height}); node.driver == nil {
		t.Fatal("premise: the height starts once the wait is over")
	}
	later := node.round() + 2
	for network.proposer(height, later) != self {
		later++
	}
	if node.onProposal(network.sign(self, height, later, block(77))) {
		t.Errorf("with its driver in round %d the node took its own proposal of round %d", node.round(), later)
	}
}
