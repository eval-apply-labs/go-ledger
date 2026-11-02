package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The pigeon test's timings. A courier rests a beat between two carries. The governed step timeout is
// far shorter than a beat, as on a network that runs at full speed until the link is cut.
const (
	pigeonBeat        = 100 * time.Millisecond
	pigeonStepTimeout = 20 * time.Millisecond
)

// pigeonRun is what a courier saw of a pigeon network's commits.
type pigeonRun struct {
	carries           int                // the carries made
	heightsAfterCarry []int64            // after each carry, how many heights, from 1 on, the courier had seen committed
	blockHashes       map[int64][32]byte // the block each height committed, as the first node seen past it decided it
	highestRounds     map[int64]int32    // the highest round a node decided each height at
	forks             []string           // the heights two nodes decided with different blocks
}

// newPigeonRun is a run that has seen nothing yet.
func newPigeonRun() pigeonRun {
	return pigeonRun{blockHashes: map[int64][32]byte{}, highestRounds: map[int64]int32{}}
}

// noteLastCommit notes a node's last commit, as the courier sees it: the block it decided its height
// with, a fork if that is not the block the first node seen past the height decided, and its round.
func (run *pigeonRun) noteLastCommit(nodeName string, lastCommit consensus.Commit) {
	if lastCommit.Height == 0 {
		return
	}
	if blockHash, seen := run.blockHashes[lastCommit.Height]; !seen {
		run.blockHashes[lastCommit.Height] = lastCommit.BlockHash
	} else if blockHash != lastCommit.BlockHash {
		run.forks = append(run.forks, fmt.Sprintf("height %d, at %s", lastCommit.Height, nodeName))
	}
	run.highestRounds[lastCommit.Height] = max(run.highestRounds[lastCommit.Height], lastCommit.Round)
}

// committedHeights is how many heights, from 1 on, the courier saw committed.
func (run pigeonRun) committedHeights() int64 {
	height := int64(0)
	for _, found := run.blockHashes[height+1]; found; _, found = run.blockHashes[height+1] {
		height++
	}
	return height
}

// runPigeonNetwork runs four validators that share no network link: each one's transport delivers
// nothing. A courier carries their messages (spec §7.9): it rests a beat, then exports every node's
// bundle and imports it into every other node, through ExportBundle and ImportBundle, and so on. So a
// message made during a carry reaches every bank at the next one, a beat later at the least. After each
// carry the courier notes each node's last commit: its block and its round. It stops after maxCarries
// carries, or once every node has committed upTo. The governed step timeout is pigeonStepTimeout, and
// the slow-down switch is minimumStepTimeout. An idle heartbeat of a millisecond (Config.idleHeartbeat)
// keeps no node idle, as work would: each starts its next height, and its round timers, about two
// milliseconds after a commit. (A pooled payment would keep them busy too, but every bundle would carry
// it, and every import check its signature again.)
func runPigeonNetwork(t *testing.T, minimumStepTimeout time.Duration, maxCarries int, upTo int64) pigeonRun {
	t.Helper()
	names := validatorNames(4)
	accounts := []ledger.Account{testAccount("alice", "100")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, pigeonStepTimeout, 0)
	var nodes []*Node
	for _, name := range names {
		config := testConfig(staticChain(names...), name, genesis)
		config.MinimumStepTimeout = minimumStepTimeout
		config.idleHeartbeat = time.Millisecond
		nodes = append(nodes, New(config, &recordingTransport{})) // delivers nothing
	}
	for _, node := range nodes {
		node.Start()
		defer node.Stop()
	}

	run := newPigeonRun()
	for allCommitted := false; run.carries < maxCarries && !allCommitted; run.carries++ {
		time.Sleep(pigeonBeat)
		carryBundles(t, nodes)
		allCommitted = true
		for i, node := range nodes {
			lastCommit := node.Snapshot().LastCommit // through the loop: after what the imports queued
			allCommitted = allCommitted && lastCommit.Height >= upTo
			run.noteLastCommit(names[i], lastCommit)
		}
		run.heightsAfterCarry = append(run.heightsAfterCarry, run.committedHeights())
	}
	return run
}

// carryBundles is one carry of a courier (spec §7.9): it exports every node's bundle, then imports each
// into every other node, through ExportBundle and ImportBundle.
func carryBundles(t *testing.T, nodes []*Node) {
	t.Helper()
	bundles := make([][]byte, len(nodes))
	for i, node := range nodes {
		bundles[i] = node.ExportBundle()
	}
	var importsDone sync.WaitGroup
	for i, node := range nodes {
		importsDone.Go(func() {
			for j, bundle := range bundles {
				if j == i {
					continue // its own
				}
				if _, err := node.ImportBundle(bundle); err != nil {
					t.Errorf("a bundle of the network was refused: %v", err)
				}
			}
		})
	}
	importsDone.Wait()
}

// The pigeon test (spec §7, §10): four validators share no network link, and a courier carries bundles
// between them, resting a beat of 100 ms between two carries. The governed step timeout is 20 ms, far
// shorter than a beat.
//
// With the slow-down switch on, at a second, the chain commits four heights with no fork, most of them
// in round 0: a round takes three carries, the proposal, then the prevotes, then the precommits. A
// second is above twice the time a bundle takes to reach every bank, as §7.6 asks: the rest of the carry
// it was made in, a beat, and the next carry, under 0.5 s even under the race detector, where a carry
// can take longer than a beat.
//
// With the switch off, a node's propose timer of round 0 fires 20 ms after its height starts, during or
// just after the carry that committed the height before, and the courier rests a beat before the next
// carry brings the proposal. So every node prevotes "no block", and round 0 fails, as long as a timer
// fires within 80 ms of its time. Round 1 fails the same way, as long as a timer fires within 40 ms: it
// starts once round 0's precommits have come, two carries later, and its timer of 40 ms fires before the
// next carry. Round 2 starts two carries later still, so no height can commit before the seventh carry,
// where the switch commits two heights in six. That is the assertion: a comparison over a fixed number
// of carries, which those failed rounds make deterministic. Over more carries, or of the rounds the
// switched-off network commits in, it would depend on how long a carry takes: under the race detector
// the carries of its growing bundles outlast its round timers for dozens of rounds, and it commits
// nothing.
func TestPigeonCourierCarriesTheChain(t *testing.T) {
	const heights = 4
	switchedOn := runPigeonNetwork(t, 10*pigeonBeat, 4*3*heights*int(testTimeSlack), heights)
	t.Logf("switch on: %d heights in %d carries, %v after each, at the highest rounds %v", switchedOn.committedHeights(), switchedOn.carries,
		switchedOn.heightsAfterCarry, switchedOn.highestRounds)
	if len(switchedOn.forks) > 0 {
		t.Fatalf("with the switch on, nodes decided different blocks: %v", switchedOn.forks)
	}
	if switchedOn.committedHeights() < heights {
		t.Fatalf("with the switch on, the chain committed %d heights in %d carries, want %d", switchedOn.committedHeights(), switchedOn.carries, heights)
	}
	inRoundZero := 0
	for height := int64(1); height <= heights; height++ {
		if switchedOn.highestRounds[height] == 0 {
			inRoundZero++
		}
	}
	if inRoundZero*2 <= heights {
		t.Errorf("with the switch on, %d of %d heights committed in round 0, want most", inRoundZero, heights)
	}

	const comparedCarries = 6
	switchedOff := runPigeonNetwork(t, 0, comparedCarries, heights)
	t.Logf("switch off: %d heights in %d carries, at the highest rounds %v", switchedOff.committedHeights(), switchedOff.carries, switchedOff.highestRounds)
	if len(switchedOff.forks) > 0 {
		t.Fatalf("with the switch off, nodes decided different blocks: %v", switchedOff.forks)
	}
	if committedWithTheSwitch := switchedOn.heightsAfterCarry[comparedCarries-1]; switchedOff.committedHeights() >= committedWithTheSwitch {
		t.Errorf("in %d carries the chain committed %d heights with the switch off, and %d with it on: it kept the pace without the switch",
			comparedCarries, switchedOff.committedHeights(), committedWithTheSwitch)
	}
}

// A validator back without its sign log in pigeon mode (spec §7.6, §7.9) waits twice the slow-down switch
// before it signs, so that the next carry brings its earlier votes and proposal back first (review B2).
// Four validators share no link, with the switch at an hour, and a courier carries their bundles until
// each has committed height 2. The proposer of height 3 proposes round 0, a carry takes the proposal to
// the others, and the proposer restarts without its sign log. Well past two governed re-gossip
// intervals, 40 ms, and with no carry, it has signed nothing: it used to sign another block for round 0
// after 40 ms, a proposal equivocation, for which its name is frozen. The next carry brings its proposal
// back, and when the wait ends (the test fires its timer, which is two hours away), the node sends that
// proposal again, and prevotes it.
func TestPigeonValidatorWithoutItsSignLogWaitsForTheNextCarry(t *testing.T) {
	const height = 3
	names := validatorNames(4)
	accounts := []ledger.Account{testAccount("alice", "100")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, pigeonStepTimeout, 0)
	folder := t.TempDir()
	configs := make([]Config, len(names))
	nodes := make([]*Node, len(names))
	for i, name := range names {
		configs[i] = testConfig(staticChain(names...), name, genesis)
		configs[i].MinimumStepTimeout = time.Hour
		configs[i].idleHeartbeat = time.Millisecond
		configs[i].SnapshotPath = filepath.Join(folder, name+".json")
		nodes[i] = New(configs[i], &recordingTransport{})
		nodes[i].Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()
	for carries := 0; ; carries++ {
		if carries == 60*int(testTimeSlack) {
			t.Fatalf("premise: in %d carries the validators did not all commit height %d", carries, height-1)
		}
		time.Sleep(pigeonBeat / 2)
		carryBundles(t, nodes)
		if !slices.ContainsFunc(nodes, func(node *Node) bool { return node.Snapshot().LastCommit.Height < height-1 }) {
			break
		}
	}
	proposer := slices.Index(names, consensus.NewValidatorSet(validatorsOf(names)).ProposerAt(height, 0))
	// heldProposal is the proposal of height 3 round 0 that node's bundle carries, if any.
	heldProposal := func(node *Node) (ProposalMessage, bool) {
		_, lines := bundleLines(t, node.ExportBundle())
		for _, line := range lines {
			var proposal ProposalMessage
			if line.Type == "proposal" && json.Unmarshal(line.Body, &proposal) == nil && proposal.Proposal.Height == height && proposal.Proposal.Round == 0 {
				return proposal, true
			}
		}
		return ProposalMessage{}, false
	}
	var first ProposalMessage
	waitFor(t, 5*time.Second, func() bool {
		var found bool
		first, found = heldProposal(nodes[proposer])
		return found
	}, "premise: the proposer of height 3 proposed round 0")
	carryBundles(t, nodes) // the others take the proposal

	nodes[proposer].Stop()
	if err := os.Remove(configs[proposer].SnapshotPath + ".votes"); err != nil {
		t.Fatal(err)
	}
	restarted := &recordingTransport{}
	nodes[proposer] = New(configs[proposer], restarted)
	nodes[proposer].Start()
	time.Sleep(300 * time.Millisecond)
	if signed := restarted.proposals(); len(signed) > 0 {
		t.Fatalf("back without its sign log, the proposer proposed block %x for round 0 before the next carry, where it had proposed %x",
			signed[0].Proposal.BlockHash[:4], first.Proposal.BlockHash[:4])
	}
	if len(restarted.votes(names[proposer], height, 0, consensus.Prevote)) > 0 {
		t.Fatal("back without its sign log, the proposer prevoted before the next carry")
	}

	carryBundles(t, nodes) // brings the proposal back
	nodes[proposer].enqueue(event{kind: eventNextHeight, height: height})
	waitFor(t, 5*time.Second, func() bool { return len(restarted.votes(names[proposer], height, 0, consensus.Prevote)) > 0 },
		"once the wait was over, the proposer did not prevote")
	for _, prevote := range restarted.votes(names[proposer], height, 0, consensus.Prevote) {
		if prevote.BlockHash != first.Proposal.BlockHash {
			t.Errorf("the proposer prevoted %x, not its earlier proposal %x", prevote.BlockHash[:4], first.Proposal.BlockHash[:4])
		}
	}
	oneBlockPerRound(t, append([]ProposalMessage{first}, restarted.proposals()...))
}
