package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// statusOfNode is the node's /hdls/status, as its operator port answers it.
func statusOfNode(t *testing.T, node *Node) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	NewServer(node).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hdls/status", nil))
	var status map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("the status does not read: %v: %q", err, recorder.Body)
	}
	return status
}

// haltedBy reports whether the node's status says it is halted by an alarm whose text holds alarm.
func haltedBy(t *testing.T, node *Node, alarm string) func() bool {
	return func() bool {
		halted, _ := statusOfNode(t, node)["halted"].(string)
		return strings.Contains(halted, alarm)
	}
}

// Two checkpoints that both pass, with commits for different blocks at one height, are the fork alarm
// (Phase 4 spec §4.4, Q14): more than ⅓ of a set signed twice. The node adopts neither, logs both commits
// in full, and halts: its status says so, and it takes no checkpoint after, not even a carried one, until
// a restart. Here two gossip peers each serve a checkpoint of height 5, signed by the whole set, of two
// different states.
func TestTwoValidCommitsAtOneHeightHaltTheNodeWithTheForkAlarm(t *testing.T) {
	logged := captureLog(t)
	names := []string{"founder", "straggler"}
	stateWith := func(founderAmount string) ledger.LedgerState {
		return testGenesis([]ledger.Account{testAccount("founder", founderAmount), testAccount("straggler", "0")}, 30*time.Second, 0)
	}
	first, second := checkpointOf(t, 5, stateWith("900"), validatorsOf(names), nil), checkpointOf(t, 5, stateWith("800"), validatorsOf(names), nil)
	var peers [][]string
	for _, checkpoint := range []Checkpoint{first, second} {
		peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			servePeer(responseWriter, request, checkpoint)
		}))
		defer peer.Close()
		peers = append(peers, []string{peer.URL})
	}
	straggler := New(testConfig(staticChain(names...), "straggler", stateWith("1000")), &peeredRecorder{peers: peers})
	straggler.Start()
	defer straggler.Stop()
	if status := statusOfNode(t, straggler); status["halted"] != nil {
		t.Fatalf("premise: a node that runs says it is not halted, not %v", status["halted"])
	}

	if _, _, found := straggler.catchUpCheckpoint(); found {
		t.Error("catch-up took one of two checkpoints that commit different blocks at one height")
	}
	waitFor(t, 5*time.Second, haltedBy(t, straggler, "fork alarm"), "the node's status does not say the fork alarm halted it")
	for _, checkpoint := range []Checkpoint{first, second} {
		if commit, _ := json.Marshal(checkpoint.Commit); !strings.Contains(logged.String(), string(commit)) {
			t.Errorf("the log does not hold the commit of block %x in full", checkpoint.Commit.BlockHash[:4])
		}
	}
	if err := straggler.ImportCheckpoint(checkpointOf(t, 7, stateWith("700"), validatorsOf(names), nil)); err == nil || !strings.Contains(err.Error(), "halted") {
		t.Errorf("a halted node took a carried checkpoint, or refused it for another reason: %v", err)
	}
	if height := straggler.Snapshot().Height; height != 1 {
		t.Errorf("the halted node is at height %d, want 1: it adopts nothing", height)
	}
}

// seededJoinerNetwork starts three of four static validators on a bus, each with a snapshot of its own,
// which commit height 1 and then idle. It returns them, the bus, and the config of the fourth,
// validator-3, a joiner its bank seeds: its snapshot is validator-0's checkpoint as seed returns it,
// which may alter it. The step timeout is 200 ms, so the joiner, back without a sign log, waits 400 ms
// at its first height before it signs (see ownSignaturesWait).
func seededJoinerNetwork(t *testing.T, seed func(Checkpoint) Checkpoint) ([]*Node, *bus, Config) {
	t.Helper()
	names := validatorNames(4)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(append(accounts, testAccount("shop", "0")), 200*time.Millisecond, 0)
	configOf := func(name string) Config {
		config := testConfig(staticChain(names...), name, genesis)
		config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
		return config
	}
	messageBus := newBus()
	var others []*Node
	for _, name := range names[:3] {
		node := New(configOf(name), messageBus)
		messageBus.register(name, node)
		node.Start()
		t.Cleanup(node.Stop)
		others = append(others, node)
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, node := range others {
			if node.Snapshot().Height != 2 {
				return false
			}
		}
		return true
	}, "the three validators did not commit height 1")

	data, err := os.ReadFile(others[0].config.SnapshotPath)
	var saved savedSnapshot
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil {
		t.Fatalf("premise: validator-0's snapshot reads: %v", err)
	}
	saved.Checkpoint = seed(saved.Checkpoint)
	config := configOf(names[3])
	if data, err = json.Marshal(saved); err == nil {
		err = os.WriteFile(config.SnapshotPath, data, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	return others, messageBus, config
}

// payEverywhere hands each node a payment of validator-0 to the shop for the block two past the first
// node's height in progress, so that no block in the making can miss it, and returns that block's height.
// Pooled, the payment is work until then.
func payEverywhere(nodes ...*Node) int64 {
	blockID := nodes[0].Snapshot().Height + 2
	payment := signedPayment("validator-0", "shop", "1", blockID)
	for _, node := range nodes {
		node.SubmitPayment(payment)
	}
	return blockID
}

// A seated joiner is confirmed at its first commit with its own precommit (Phase 4 spec §4.5): it
// precommits only a block it applied to its own state, so that commit is more than ⅔ of the set agreeing
// with the state its bank seeded it with. Before, its status says "confirmed": null. Here validator-3
// joins from validator-0's snapshot of height 1, and commits height 2 with the others.
func TestSeatedJoinerIsConfirmedAtItsFirstCommit(t *testing.T) {
	others, messageBus, config := seededJoinerNetwork(t, func(checkpoint Checkpoint) Checkpoint { return checkpoint })
	joiner := New(config, messageBus)
	messageBus.register(config.Name, joiner)
	joiner.Start()
	defer joiner.Stop()
	if status := statusOfNode(t, joiner); status["confirmed"] != nil || joiner.Snapshot().Height != 2 {
		t.Fatalf("premise: the joiner starts at height 2, from its seed, and is not confirmed (%v)", status["confirmed"])
	}

	payEverywhere(append(others, joiner)...)
	waitFor(t, 10*time.Second, func() bool { return statusOfNode(t, joiner)["confirmed"] != nil }, "the joiner was never confirmed")
	if confirmed := statusOfNode(t, joiner)["confirmed"]; confirmed != float64(2) {
		t.Errorf("the joiner was confirmed at height %v, want 2, its first commit", confirmed)
	}
}

// A seeded joiner whose seed was altered never becomes confirmed, and halts with the divergence alarm
// (Phase 4 spec §4.5, Q15). Here validator-0's snapshot of height 1 is altered: the joiner's balance is
// raised, and the checkpoint signed again by keys of the seeding bank's own, which the set it carries
// gives the validators' names, so the joiner loads it as its own snapshot (selfConsistent). Signed with
// the validators' real keys, the seed's commit would be a double-sign of each at height 1, which the
// others would freeze. The others' block 2 builds on the real block 1, which the joiner's state refuses;
// once more than ⅔ of the set precommit it, the joiner halts, and its status says so. Its bootstrap peer
// is validator-0's operator port, so without the alarm its catch-up would take the network's state and the
// joiner would be confirmed at its next commit: halted, it adopts nothing, not even the network's
// checkpoint handed to it, and signs nothing, while the others commit on.
func TestJoinerWithAnAlteredSeedHaltsWithTheDivergenceAlarm(t *testing.T) {
	names := validatorNames(4)
	others, messageBus, config := seededJoinerNetwork(t, func(checkpoint Checkpoint) Checkpoint {
		altered := checkpoint.State
		altered.Accounts = append([]ledger.Account(nil), altered.Accounts...)
		for i := range altered.Accounts {
			if altered.Accounts[i].Name == "validator-3" {
				altered.Accounts[i].Amount = rational("1000")
			}
		}
		forgedSet, forgerSigners := validatorsOf(names), map[string]signing.Signer{}
		for i, name := range names {
			forgerSigners[name] = testSigner("seeder-key-" + name)
			forgedSet[i].PublicKey = forgerSigners[name].PublicKeyBase64()
		}
		return checkpointOf(t, checkpoint.Commit.Height, altered, forgedSet, forgerSigners)
	})
	operatorPort := httptest.NewServer(NewServer(others[0]).Handler())
	defer operatorPort.Close()
	config.Bootstrap = []string{operatorPort.URL}
	joiner := New(config, messageBus)
	messageBus.register(config.Name, joiner)
	joiner.Start()
	defer joiner.Stop()

	payEverywhere(append(others, joiner)...)
	waitFor(t, 10*time.Second, haltedBy(t, joiner, "divergence alarm"), "the joiner with an altered seed did not halt with the divergence alarm")
	blockID := payEverywhere(others...)
	waitFor(t, 10*time.Second, func() bool { return others[0].Snapshot().Height > blockID }, "the others did not commit on")
	if err := joiner.ImportCheckpoint(others[0].Snapshot().checkpoint()); err == nil || !strings.Contains(err.Error(), "halted") {
		t.Errorf("the halted joiner took the network's checkpoint, or refused it for another reason: %v", err)
	}
	if status := statusOfNode(t, joiner); status["confirmed"] != nil || joiner.Snapshot().Height != 2 {
		t.Errorf("the halted joiner is at height %d, confirmed %v; want height 2, never confirmed", joiner.Snapshot().Height, status["confirmed"])
	}
}

// A checkpoint of a seated node's height in progress whose block does not build on the node's last
// block raises the divergence alarm too (divergesFrom): more than ⅔ of the set committed a block there
// that the node's state refuses, and a catch-up must not take the node past it. Here a validator of two
// at genesis is handed, as a carrier would, a checkpoint of height 1 that both validators signed, whose
// block builds on another block than the genesis hash. It refuses it and halts.
func TestCheckpointOfTheNodesHeightOnAnotherBlockRaisesTheDivergenceAlarm(t *testing.T) {
	names := []string{"founder", "straggler"}
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	straggler := New(testConfig(staticChain(names...), "straggler", genesis), &recordingTransport{})
	straggler.Start()
	defer straggler.Stop()

	elsewhere := checkpointAfter(t, [32]byte{1}, 1, genesis, validatorsOf(names), nil)
	if err := straggler.ImportCheckpoint(elsewhere); err == nil || !strings.Contains(err.Error(), "diverges") {
		t.Errorf("the node took a checkpoint of its height on another block, or refused it for another reason: %v", err)
	}
	if !haltedBy(t, straggler, "divergence alarm")() || straggler.Snapshot().Height != 1 {
		t.Error("the node did not halt with the divergence alarm at height 1")
	}
}

// A checkpoint that passes, of the node's committed height, for another block than the node's own, raises
// the divergence alarm: more than ⅔ of a frame the node trusts committed a block there that the node did
// not, so the node is on one side of a fork, or its state is false. Catch-up used to fetch only from
// peers ahead of the node and to compare their checkpoints with each other, never with the node's own
// block, so a node on one side of a fork took the other side silently (review). Here a validator of two
// committed block X at height 1. A peer at height 1 serves a commit, by both, of another block there. In
// the second case a peer at height 3, on that peer's side, serves a checkpoint too, which the node would
// otherwise adopt. In the third the node's block X gave the founder a new key, so the set that signs
// height 2 is not the one that signed height 1: the network's commit passes against the latter only
// (trustedFrameOf). Each time the node adopts nothing, halts, and logs the network's commit in full.
func TestValidCommitOfAnotherBlockAtTheNodesHeightRaisesTheDivergenceAlarm(t *testing.T) {
	names := []string{"founder", "straggler"}
	stateWith := func(founderAmount string) ledger.LedgerState {
		return testGenesis([]ledger.Account{testAccount("founder", founderAmount), testAccount("straggler", "0")}, 30*time.Second, 0)
	}
	ownBlock, otherBlock := checkpointOf(t, 1, stateWith("900"), validatorsOf(names), nil), checkpointOf(t, 1, stateWith("800"), validatorsOf(names), nil)
	otherSideLater := checkpointAfter(t, [32]byte{0xee}, 3, stateWith("700"), validatorsOf(names), nil)
	founderWithANewKey := stateWith("900")
	founderWithANewKey.Accounts[0].PublicKey = testSigner("founder-new-key").PublicKeyBase64() // the founder's, first in name order
	ownBlockWithANewKey := checkpointOf(t, 1, founderWithANewKey, validatorsOf(names), nil)
	serve := func(checkpoint Checkpoint) string {
		server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			servePeer(responseWriter, request, checkpoint)
		}))
		t.Cleanup(server.Close)
		return server.URL
	}

	for _, situation := range []struct {
		name     string
		ownBlock Checkpoint
		peers    [][]string
	}{
		{"a peer at the node's height on another block", ownBlock, [][]string{{serve(otherBlock)}}},
		{"that peer, and one further ahead on its side", ownBlock, [][]string{{serve(otherBlock)}, {serve(otherSideLater)}}},
		{"a peer at the node's height on another block, the node's block having changed the set", ownBlockWithANewKey, [][]string{{serve(otherBlock)}}},
	} {
		t.Run(situation.name, func(t *testing.T) {
			logged := captureLog(t)
			straggler := New(testConfig(staticChain(names...), "straggler", stateWith("1000")), &peeredRecorder{peers: situation.peers})
			straggler.adopt(situation.ownBlock, nil)
			if straggler.height() != 2 {
				t.Fatalf("premise: the node committed its block at height 1, so it works on 2, not %d", straggler.height())
			}
			straggler.Start()
			defer straggler.Stop()

			if _, _, found := straggler.catchUpCheckpoint(); found {
				t.Error("catch-up took a checkpoint on the other side of a fork with the node's own block")
			}
			waitFor(t, 5*time.Second, haltedBy(t, straggler, "divergence alarm"), "the node's status does not say the divergence alarm halted it")
			if commit, _ := json.Marshal(otherBlock.Commit); !strings.Contains(logged.String(), string(commit)) {
				t.Error("the log does not hold the network's commit in full")
			}
			if height := straggler.Snapshot().Height; height != 2 {
				t.Errorf("the halted node works on height %d, want 2: it adopts nothing", height)
			}
		})
	}
}

// A node is confirmed only by a commit that holds its own precommit, while the set that signed the commit
// seats the node's name under the node's own key (noteConfirmation, Phase 4 spec §4.5): a precommit in its
// name under another key is another node's, so a node not seated under its own key, an observer, is
// never confirmed. Here, of four validators, a commit of all four confirms validator-3; one of the other
// three does not; and neither confirms a node that runs under validator-3's name with another key, as
// one does once a ⅔ vote gave the name a new key.
func TestOnlyTheNodesOwnPrecommitConfirmsIt(t *testing.T) {
	names := validatorNames(4)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(accounts, 30*time.Second, 0)
	ofAllFour, ofTheOtherThree := buildCommit(t, 1, [32]byte{1}, validatorsOf(names), nil), buildCommit(t, 1, [32]byte{1}, validatorsOf(names[:3]), nil)
	for _, situation := range []struct {
		name      string
		key       string // the test name whose key the node runs under
		commit    consensus.Commit
		confirmed bool
	}{
		{"its own key, its own precommit", "validator-3", ofAllFour, true},
		{"its own key, no precommit of its own", "validator-3", ofTheOtherThree, false},
		{"another key", "observer", ofAllFour, false},
	} {
		config := testConfig(staticChain(names...), "validator-3", genesis)
		config.Signer = testSigner(situation.key)
		node := New(config, &recordingTransport{})
		node.heightValidators = node.view.Load().validators // the set of height 1, as startHeight takes it
		node.noteConfirmation(situation.commit)
		if confirmed := node.confirmedHeight.Load() == 1; confirmed != situation.confirmed {
			t.Errorf("%s: confirmed %v, want %v", situation.name, confirmed, situation.confirmed)
		}
	}
}

// The divergence alarm of a refused block is a seated node's (noteDivergence): an observer refuses a block
// as a validator does, but more than ⅔ of the set precommitting it does not halt the observer, which
// relies on the checkpoints (Phase 4 spec §4.5). Here three of four validators precommit a block at height
// 1 that two nodes refused: validator-3, which halts, and an observer, which does not.
func TestOnlyASeatedNodeHaltsOnABlockItRefused(t *testing.T) {
	names := validatorNames(4)
	var accounts []ledger.Account
	for _, name := range append(slices.Clone(names), "observer") {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(accounts, 30*time.Second, 0)
	refusedBlockHash := [32]byte{9}
	for _, situation := range []struct {
		name  string
		halts bool
	}{{"validator-3", true}, {"observer", false}} {
		node := New(testConfig(staticChain(names...), situation.name, genesis), &recordingTransport{})
		node.heightValidators = node.view.Load().validators // the set of height 1, as startHeight takes it
		node.refusedBlocks[refusedBlockHash] = refusedBlock{Reason: "a test's"}
		for _, name := range names[:3] {
			node.heightVotes[name] = consensus.Vote{Height: 1, Type: consensus.Precommit, BlockHash: refusedBlockHash, Validator: name}
		}
		node.noteDivergence(refusedBlockHash)
		if halted := node.haltedBecause.Load() != nil; halted != situation.halts {
			t.Errorf("%s: halted %v, want %v", situation.name, halted, situation.halts)
		}
	}
}

// A halted node asks no peer for a checkpoint (catchUpLoop): it adopts none (see halt). Here a halted
// validator's gossip peer counts what it is asked for a catch-up interval and a second.
func TestHaltedNodeAsksNoPeerForACheckpoint(t *testing.T) {
	var asked atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		asked.Add(1)
		http.NotFound(responseWriter, request)
	}))
	defer peer.Close()
	names := []string{"founder", "straggler"}
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	straggler := New(testConfig(staticChain(names...), "straggler", genesis), &peeredRecorder{peers: [][]string{{peer.URL}}})
	straggler.halt("a test's alarm")
	straggler.Start()
	defer straggler.Stop()
	time.Sleep(straggler.catchUpInterval() + time.Second)
	if count := asked.Load(); count != 0 {
		t.Errorf("the halted node asked its peer %d times", count)
	}
}
