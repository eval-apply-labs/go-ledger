package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// snapshotFile is the snapshot file of checkpoint on the test network, as saveSnapshot writes it.
func snapshotFile(checkpoint Checkpoint) []byte {
	data, _ := json.Marshal(savedSnapshot{NetworkID: testNetworkID, Checkpoint: checkpoint})
	return data
}

// A node persists its committed state to disk; a restart (same key, same snapshot path, no
// network at all) reloads it and resumes from the committed height — proving recovery works even
// when there is no peer to sync from (the simultaneous-total-outage case).
func TestNodePersistsAndReloads(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")

	config := testConfig(staticChain("founder"), "founder",
		testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("recipient", "0")}, 5*time.Second, 20*time.Millisecond))
	config.SnapshotPath = snapshotPath

	// First run: commit a payment, so the persisted state is non-genesis.
	first := New(config, NewHTTPTransport(nil))
	first.SubmitPayment(signedPayment("founder", "recipient", "10", 1))
	first.Start()
	keepBusy(t, "founder", first)
	waitFor(t, 4*time.Second, func() bool {
		snapshot := first.Snapshot()
		return snapshot.Height >= 3 && balance(snapshot.Accounts, "recipient").Sign() > 0
	}, "first run did not commit the payment")
	// snapshot file must exist
	waitFor(t, 2*time.Second, func() bool { _, err := os.Stat(snapshotPath); return err == nil }, "snapshot not written")
	firstHeight := first.Snapshot().Height
	first.Stop()
	time.Sleep(50 * time.Millisecond) // let the loop settle

	// Restart: brand-new Node from the same config (no bootstrap, no peers). It must load the disk
	// snapshot and come up at (at least) the height the first run reached, holding the payment.
	restart := New(config, NewHTTPTransport(nil))
	restart.Start()
	defer restart.Stop()

	snapshot := restart.Snapshot()
	if snapshot.Height < firstHeight {
		t.Fatalf("restart came up at height %d, below persisted %d (snapshot not loaded)", snapshot.Height, firstHeight)
	}
	if balance(snapshot.Accounts, "recipient").Cmp(rational("9.9")) != 0 {
		t.Fatalf("restart did not recover the committed state (recipient = %s, want 9.9)", balance(snapshot.Accounts, "recipient").FloatString(2))
	}
}

// In dynamic mode the set that signs a node's last commit can drift away from genesis entirely: here
// the founder, the only genesis validator, pays out and leaves the set. The node's own snapshot must
// still reload, not be discarded for a silent restart at genesis (a roll-back of committed history).
func TestNodeReloadsOwnSnapshotAfterValidatorRotation(t *testing.T) {
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), selfDelegated("successor", "0")}, 300*time.Millisecond, 20*time.Millisecond)
	snapshotDirectory := t.TempDir()
	config := func(name, file string) Config {
		config := testConfig(dynamicChain("100"), name, genesis)
		config.SnapshotPath = filepath.Join(snapshotDirectory, file)
		return config
	}
	messageBus := newBus()
	founderNode, successorNode := New(config("founder", "a.json"), messageBus), New(config("successor", "x.json"), messageBus)
	messageBus.register("founder", founderNode)
	messageBus.register("successor", successorNode)
	// The founder pays the successor 990 at height 1; from height 2 the successor is the only validator
	// (the founder keeps 10 < 100).
	founderNode.SubmitPayment(signedPayment("founder", "successor", "990", 1))
	successorNode.Start() // first, so it hears the founder's height-1 proposal
	founderNode.Start()
	keepBusy(t, "successor", successorNode, founderNode)
	waitFor(t, 10*time.Second, func() bool { return successorNode.Snapshot().Height >= 4 }, "the rotated chain did not advance")
	reached := successorNode.Snapshot().Height // its snapshot holds height reached-1, signed by the successor alone
	founderNode.Stop()
	successorNode.Stop()

	restarted := New(config("successor", "x.json"), newBus())
	if restarted.height() < reached {
		t.Fatalf("the node's own snapshot was discarded: restarted at height %d, had committed up to %d", restarted.height(), reached-1)
	}
	if successorBalance := balance(restarted.state.Accounts, "successor"); successorBalance.Cmp(rational("989.9")) != 0 {
		t.Fatalf("restarted without the committed payout: X = %s, want 989.9", successorBalance.RatString())
	}
}

// A snapshot file that exists but cannot be parsed or does not verify must stop the node from
// starting, never fall back to genesis silently (re-running committed heights). A missing file is a
// first start.
func TestOpenRefusesCorruptSnapshot(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000"), testAccount("shop", "0")}, time.Second, 0))
	config.SnapshotPath = snapshotPath
	if _, err := Open(config, NewHTTPTransport(nil)); err != nil {
		t.Fatalf("a missing snapshot is a first start, got: %v", err)
	}

	state := testGenesis([]ledger.Account{testAccount("shop", "99.9"), testAccount("validator", "900")}, time.Second, 0)
	checkpoint := checkpointOf(t, 3, state, validatorsOf([]string{"validator"}), nil)
	genuine := snapshotFile(checkpoint)
	checkpoint.State.Accounts = []ledger.Account{testAccount("shop", "0"), testAccount("validator", "999.9")} // edited after signing
	edited := snapshotFile(checkpoint)

	if err := os.WriteFile(snapshotPath, genuine, 0o600); err != nil {
		t.Fatal(err)
	}
	if node, err := Open(config, NewHTTPTransport(nil)); err != nil || node.height() != 4 {
		t.Fatalf("premise: the genuine snapshot loads (err %v)", err)
	}
	for name, data := range map[string][]byte{"truncated": genuine[:len(genuine)/2], "edited": edited} {
		if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(config, NewHTTPTransport(nil)); err == nil {
			t.Fatalf("%s snapshot: Open must refuse to start, not fall back to genesis", name)
		}
	}
}

// Open refuses a snapshot path whose folder does not exist, and names the folder (C9). The node used to
// open as on a first start, and then every write of its snapshot and of its sign log failed: a validator
// sent no vote or proposal, and while no node committed, nothing was logged.
func TestOpenRefusesAMissingSnapshotFolder(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "data")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000")}, time.Second, 0))
	config.SnapshotPath = filepath.Join(folder, "snapshot.json")
	if _, err := Open(config, NewHTTPTransport(nil)); err == nil || !strings.Contains(err.Error(), folder) {
		t.Fatalf("Open with a missing snapshot folder: error %v, want one that names %s", err, folder)
	}
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(config, NewHTTPTransport(nil)); err != nil {
		t.Fatalf("once the folder exists, a missing snapshot is a first start, got: %v", err)
	}
}

// A sign log for a height above the one the snapshot restarts at holds votes a restart there would
// forget, so Open must refuse to start. A log for the restart height is reloaded.
func TestOpenRefusesSignLogAheadOfSnapshot(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000")}, time.Second, 0))
	config.SnapshotPath = snapshotPath
	logAt := func(height int64) {
		vote, _ := consensus.SignVote(testSigner("validator"), consensus.Vote{Height: height, Type: consensus.Prevote, Validator: "validator"})
		data, _ := json.Marshal(signLog{NetworkID: testNetworkID, Height: height, Votes: []consensus.Vote{vote}})
		if err := os.WriteFile(snapshotPath+".votes", data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logAt(1) // no snapshot yet: the node restarts at height 1
	if node, err := Open(config, NewHTTPTransport(nil)); err != nil || len(node.signLog.Votes) != 1 {
		t.Fatalf("a sign log for the restart height must be reloaded (err %v)", err)
	}
	logAt(2)
	if _, err := Open(config, NewHTTPTransport(nil)); err == nil {
		t.Fatal("a sign log above the restart height must stop the node from starting")
	}
}

// A sign log for the restart height that does not verify on the node's network, left from a network
// re-created under a new id or corrupted, must not be resumed: its votes would lock the node on another
// chain's block and keep it silent in the rounds they cover. Open refuses it, as it refuses a snapshot
// that does not verify.
func TestOpenRefusesSignLogThatDoesNotVerify(t *testing.T) {
	_, privateKey, err := signing.MLDSA65{}.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signerOn := func(network string) *signing.Signer { // one key, signing for the given network
		signer, err := signing.NewSigner(signing.MLDSA65{}, network, privateKey)
		if err != nil {
			t.Fatal(err)
		}
		return signer
	}
	validatorSigner := signerOn("village-2")
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	chain := staticChain("validator")
	chain.NetworkID = "village-2"
	config := Config{Chain: chain, Signer: validatorSigner, Name: "validator", SnapshotPath: snapshotPath,
		Genesis: testGenesis([]ledger.Account{{Name: "validator", PublicKey: validatorSigner.PublicKeyBase64(), Amount: rational("1000")}}, time.Second, 0)}
	for network, refused := range map[string]bool{"village-1": true, "village-2": false} {
		vote, _ := consensus.SignVote(signerOn(network),
			consensus.Vote{Height: 1, Type: consensus.Precommit, BlockHash: [32]byte{1}, Validator: "validator"})
		data, _ := json.Marshal(signLog{NetworkID: "village-2", Height: 1, Votes: []consensus.Vote{vote}})
		if err := os.WriteFile(snapshotPath+".votes", data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(config, NewHTTPTransport(nil)); (err != nil) != refused {
			t.Errorf("a sign log signed for %s on %s: Open error %v, want refused %v", network, config.Chain.NetworkID, err, refused)
		}
	}
	// So must a proposal of its own in the log, or the one it is locked on, which must also be for the
	// log's height, and hold the block it signed: the node would send it again, or lock on it, and decide
	// that block by its hash.
	anotherBody := proposalOf(signerOn("village-2"), "validator", 1, 0, ledger.Block{Height: 1})
	anotherBody.Block.Nonce[0]++
	for name, testCase := range map[string]struct {
		proposal ProposalMessage
		refused  bool
	}{
		"signed for village-1":                   {proposalOf(signerOn("village-1"), "validator", 1, 0, ledger.Block{Height: 1}), true},
		"for height 2":                           {proposalOf(signerOn("village-2"), "validator", 2, 0, ledger.Block{Height: 2}), true},
		"with another block than the one signed": {anotherBody, true},
		"signed for village-2, for height 1":     {proposalOf(signerOn("village-2"), "validator", 1, 0, ledger.Block{Height: 1}), false},
	} {
		for role, logged := range map[string]signLog{
			"own":    {NetworkID: "village-2", Height: 1, Proposals: []ProposalMessage{testCase.proposal}},
			"locked": {NetworkID: "village-2", Height: 1, Locked: &testCase.proposal},
		} {
			data, _ := json.Marshal(logged)
			if err := os.WriteFile(snapshotPath+".votes", data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(config, NewHTTPTransport(nil)); (err != nil) != testCase.refused {
				t.Errorf("a logged %s proposal %s: Open error %v, want refused %v", role, name, err, testCase.refused)
			}
		}
	}
}

// A node that fast-forwards to a peer's checkpoint, by catch-up while it runs or by bootstrap at start,
// votes at the height after it. It must save that checkpoint as its snapshot first: a restart before
// its next own commit would otherwise find its sign log above the snapshot, and Open would refuse it.
func TestValidatorRestartsAfterFastForwardBeforeItsNextCommit(t *testing.T) {
	names := []string{"founder", "self"} // self cannot decide alone
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("self", "0")}, 50*time.Millisecond, 0)
	checkpoint := checkpointOf(t, 5, testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("self", "50")}, 50*time.Millisecond, 0), validatorsOf(names), nil)
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		servePeer(responseWriter, request, checkpoint)
	}))
	defer peer.Close()

	for _, bootstrap := range []bool{false, true} {
		config := testConfig(staticChain(names...), "self", genesis)
		config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
		if bootstrap {
			config.Bootstrap = []string{peer.URL}
		}
		recorder := &recordingTransport{}
		node, err := Open(config, recorder)
		if err != nil {
			t.Fatal(err)
		}
		node.Start()
		if !bootstrap {
			node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})
		}
		keepBusy(t, "self", node) // work at height 6
		waitFor(t, 5*time.Second, func() bool { return len(recorder.votes("self", 6, 0, consensus.Prevote)) > 0 }, "the node did not vote at height 6")
		node.Stop() // power loss before height 6 commits: the founder is not reachable
		if again, err := Open(config, recorder); err != nil || again.height() != 6 {
			t.Fatalf("bootstrap %v: the node could not restart at height 6 after fast-forwarding (err %v)", bootstrap, err)
		}
	}
}

// A state's accounts are in strictly ascending order of name: the digest hashes them in that order and
// every lookup by name relies on it. So a decoded state out of that order is refused before any other
// check, here with a commit that is otherwise genuine, signed over the digest of the disordered accounts:
// the node's own snapshot (Open refuses to start), a peer's checkpoint (catch-up does not adopt it), and
// a genesis.
func TestStateOutOfNameOrderIsRefused(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	genesis := testGenesis([]ledger.Account{testAccount("shop", "0"), testAccount("validator", "1000")}, time.Second, 0)
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.SnapshotPath = snapshotPath

	ordered := testGenesis([]ledger.Account{testAccount("shop", "99.9"), testAccount("validator", "900")}, time.Second, 0)
	disordered := ordered
	disordered.Accounts = []ledger.Account{ordered.Accounts[1], ordered.Accounts[0]}
	for name, state := range map[string]ledger.LedgerState{"in name order": ordered, "out of name order": disordered} {
		checkpoint := checkpointOf(t, 3, state, validatorsOf([]string{"validator"}), nil)
		if err := os.WriteFile(snapshotPath, snapshotFile(checkpoint), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(config, NewHTTPTransport(nil))
		if (err != nil) != (name == "out of name order") {
			t.Errorf("a snapshot %s: Open error %v", name, err)
		}

		os.Remove(snapshotPath)
		node := New(config, NewHTTPTransport(nil))
		if trusted := node.verifyCheckpoint(checkpoint, node.view.Load().validators); trusted != (name == "in name order") {
			t.Errorf("a peer's checkpoint %s: trusted %v", name, trusted)
		}
	}

	config.Genesis = disordered
	config.Genesis.Height = 0
	config.SnapshotPath = ""
	if _, err := Open(config, NewHTTPTransport(nil)); err == nil {
		t.Error("a genesis out of name order was accepted")
	}
}

// The snapshot holds the last block's header, never a block (spec 3.6 and 4.14): after three commits it
// holds the nonce of the last block, with which the block's hash recomputes, and none of the earlier
// blocks' nonces, nor any payment the blocks carried.
func TestSnapshotKeepsOnlyTheLastNonce(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000"), testAccount("shop", "0")}, time.Second, 0))
	config.SnapshotPath = snapshotPath
	recorder := &recordingTransport{}
	node := New(config, recorder) // driven by hand: a sole validator decides and commits a height within startHeight
	payment, _ := ledger.SignPayment(testSigner("validator"), ledger.Payment{From: "validator", To: "shop", Amount: rational("10"),
		Comment: "a comment the snapshot does not keep", BlockID: 1})
	if err := admit(node, payment); err != nil {
		t.Fatal(err)
	}
	keepBusyByHand(t, "shop", node)
	for height := int64(1); height <= 3; height++ {
		if node.startHeight(); node.lastCommit.Height != height {
			t.Fatalf("premise: the sole validator commits height %d at once", height)
		}
	}
	snapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, height := range []int64{1, 2, 3} {
		nonce, _ := json.Marshal(recorder.proposalAt(height).Block.Nonce)
		if kept, last := bytes.Contains(snapshot, nonce), height == 3; kept != last {
			t.Errorf("the snapshot holds the nonce of block %d: %v, want %v", height, kept, last)
		}
	}
	if bytes.Contains(snapshot, []byte(payment.Comment)) || bytes.Contains(snapshot, []byte(payment.Signature)) {
		t.Error("the snapshot holds a payment of a committed block")
	}
	if again, err := Open(config, NewHTTPTransport(nil)); err != nil || again.height() != 4 || again.lastBlockHash != node.lastBlockHash {
		t.Fatalf("the snapshot does not reload at height 4 with the last block's hash (err %v)", err)
	}
}

// Once the snapshot of a height is on disk, a validator's sign log holds nothing but the next height
// (spec 6.9): it kept the node's votes and its proposal, the block with its payments and nonce included,
// and an idle chain must not keep the last block (spec 3.6). A restart at the next height finds that
// log complete, and the node starts at once. (After a failed snapshot write the log of the height stays:
// see TestSoleValidatorRestartRecommitsItsLockedBlock.)
func TestSignLogIsEmptiedAfterTheSnapshot(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000"), testAccount("shop", "0")}, time.Second, 0))
	config.SnapshotPath = snapshotPath
	node := New(config, NewHTTPTransport(nil)) // driven by hand: a sole validator decides and commits a height within startHeight
	payment := signedPayment("validator", "shop", "10", 1)
	if err := admit(node, payment); err != nil {
		t.Fatal(err)
	}
	if node.startHeight(); node.lastCommit.Height != 1 {
		t.Fatal("premise: the sole validator commits height 1 at once")
	}
	data, err := os.ReadFile(snapshotPath + ".votes")
	if err != nil {
		t.Fatal(err)
	}
	var logged signLog
	if err := json.Unmarshal(data, &logged); err != nil || logged.Height != 2 || len(logged.Votes) != 0 || len(logged.Proposals) != 0 ||
		logged.Locked != nil || bytes.Contains(data, []byte(payment.Signature)) {
		t.Fatalf("after the snapshot of height 1 the sign log is %s, want an empty log for height 2", data)
	}
	if again, err := Open(config, NewHTTPTransport(nil)); err != nil || again.height() != 2 || again.signLog.Height != 2 {
		t.Fatalf("restarted, the node does not open at height 2 with the log of height 2 (err %v)", err)
	}
}

// A node that leaves a height it signed in by a catch-up, or by adopting a checkpoint, empties its sign
// log once the new snapshot is on disk, as a commit does (spec 3.6, 6.9): the log held its vote and its
// proposal's block, which an idle chain must not keep on disk (C10). A catch-up writes the empty log for
// the height it starts, as a commit does. Adopt empties the log at its own height, so the node still
// waits two re-gossip intervals before it signs at the adopted height (startFirstHeight).
func TestSignLogLeftBehindIsEmptied(t *testing.T) {
	names := []string{"founder", "straggler"} // quorum 2: neither decides alone
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, time.Hour, 0)
	checkpoint := checkpointOf(t, 5, genesis, validatorsOf(names), nil)
	config := testConfig(staticChain(names...), staticChain(names...).ValidatorRule.SetFor(genesis).ProposerAt(1, 0), genesis)
	// signedAtHeightOne is a node that proposed block 1 and prevoted it, both in its sign log.
	signedAtHeightOne := func() *Node {
		config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
		node := New(config, &recordingTransport{})
		node.startHeight()
		if len(node.signLog.Proposals) != 1 || len(node.signLog.Votes) != 1 {
			t.Fatalf("premise: the node logged its proposal and its prevote at height 1, got %d proposals and %d votes", len(node.signLog.Proposals), len(node.signLog.Votes))
		}
		return node
	}
	loggedOnDisk := func(node *Node) signLog {
		data, err := os.ReadFile(node.signLogPath())
		if err != nil {
			t.Fatal(err)
		}
		var logged signLog
		if err := json.Unmarshal(data, &logged); err != nil {
			t.Fatal(err)
		}
		return logged
	}

	caughtUp := signedAtHeightOne()
	caughtUp.started = true
	caughtUp.catchUp(checkpoint)
	if logged := loggedOnDisk(caughtUp); logged.Height != 6 || len(logged.Votes) != 0 || len(logged.Proposals) != 0 || logged.Locked != nil {
		t.Errorf("after a catch-up to height 5 the sign log holds height %d, %d votes and %d proposals, want an empty log for height 6", logged.Height, len(logged.Votes), len(logged.Proposals))
	}

	signedAtHeightOne()                                  // writes the log of height 1, then stops
	adopting, err := Open(config, &recordingTransport{}) // back with that log, as at height 1
	if err != nil || adopting.signLog.Height != 1 {
		t.Fatalf("premise: the node opens with its log of height 1 (err %v)", err)
	}
	adopting.adopt(checkpoint)
	if logged := loggedOnDisk(adopting); logged.Height != 1 || len(logged.Votes) != 0 || len(logged.Proposals) != 0 || logged.Locked != nil {
		t.Errorf("after adopting a checkpoint of height 5 the sign log holds height %d, %d votes and %d proposals, want an empty log for height 1", logged.Height, len(logged.Votes), len(logged.Proposals))
	}
	adopting.started = true
	if adopting.startFirstHeight(); adopting.heightStarted != 0 {
		t.Error("a node that adopted a checkpoint started the height at once, without the re-gossip wait")
	}
}

// A node that stops between commit's two writes, the snapshot of a height and then the empty sign log
// for the next, comes back with the log of the height it committed: its votes and its proposal, the
// block with its payment and nonce included, below the height it restarts at. Open empties that log at
// its own height, as adopt does (spec 3.6, 6.9), so the node still waits two re-gossip intervals
// before it signs (startFirstHeight), as after a lost log.
func TestSignLogBelowTheRestartHeightIsEmptied(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000"), testAccount("shop", "0")}, time.Hour, 0))
	config.SnapshotPath = snapshotPath
	recorder := &recordingTransport{}
	node := New(config, recorder) // driven by hand: a sole validator decides and commits a height within startHeight
	payment := signedPayment("validator", "shop", "10", 1)
	if err := admit(node, payment); err != nil {
		t.Fatal(err)
	}
	if node.startHeight(); node.lastCommit.Height != 1 {
		t.Fatal("premise: the sole validator commits height 1 at once")
	}
	// The stop: the snapshot of height 1 is on disk, and the log of height 1 was never emptied. It holds
	// what the node sent: its proposal and its two votes.
	proposal := recorder.proposalAt(1)
	prevotes, precommits := recorder.votes("validator", 1, 0, consensus.Prevote), recorder.votes("validator", 1, 0, consensus.Precommit)
	if proposal == nil || len(prevotes) == 0 || len(precommits) == 0 {
		t.Fatal("premise: the node sent its proposal, its prevote and its precommit for height 1")
	}
	leftBehind, _ := json.Marshal(signLog{NetworkID: testNetworkID, Height: 1, Votes: []consensus.Vote{prevotes[0], precommits[0]},
		Proposals: []ProposalMessage{*proposal}, Locked: proposal})
	if err := os.WriteFile(snapshotPath+".votes", leftBehind, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(config, &recordingTransport{})
	if err != nil || restarted.height() != 2 {
		t.Fatalf("premise: the node restarts at height 2 (err %v)", err)
	}
	data, err := os.ReadFile(snapshotPath + ".votes")
	if err != nil {
		t.Fatal(err)
	}
	var logged signLog
	if err := json.Unmarshal(data, &logged); err != nil {
		t.Fatal(err)
	}
	if logged.Height != 1 || len(logged.Votes) != 0 || len(logged.Proposals) != 0 || logged.Locked != nil {
		t.Fatalf("restarted at height 2, the sign log holds height %d, %d votes, %d proposals and a locked proposal: %v; want an empty log for height 1",
			logged.Height, len(logged.Votes), len(logged.Proposals), logged.Locked != nil)
	}
	restarted.started = true
	if restarted.startFirstHeight(); restarted.heightStarted != 0 {
		t.Error("a node back with a sign log below its restart height started at once, without the re-gossip wait")
	}
}

// A snapshot records the network id it was committed on, and a node refuses one that records none, as
// a Phase 1 snapshot does, or another network's, with an error that says it belongs to an earlier
// network (spec 0.2): resuming another chain's state would fork this one.
func TestSnapshotOfAnEarlierNetworkIsRefused(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	genesis := testGenesis([]ledger.Account{testAccount("validator", "1000")}, time.Second, 0)
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.SnapshotPath = snapshotPath
	checkpoint := checkpointOf(t, 3, genesis, validatorsOf([]string{"validator"}), nil)
	phase1, _ := json.Marshal(map[string]any{"Accounts": checkpoint.State.Accounts, "Parameters": checkpoint.State.Parameters,
		"GovernanceSequence": 0, "Commit": checkpoint.Commit, "Validators": checkpoint.Validators})
	anotherNetwork, _ := json.Marshal(savedSnapshot{NetworkID: "othernet", Checkpoint: checkpoint})
	for name, data := range map[string][]byte{"a Phase 1 snapshot": phase1, "another network's snapshot": anotherNetwork, "this network's": snapshotFile(checkpoint)} {
		if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(config, NewHTTPTransport(nil))
		if refused := err != nil && strings.Contains(err.Error(), "belongs to an earlier network"); refused != (name != "this network's") {
			t.Errorf("%s: Open error %v", name, err)
		}
	}
}

// A sign log records the network id it was signed on, and a node refuses one that records none, as a
// Phase 1 log does, or another network's, whatever its height, with an error that says it belongs to an
// earlier network (spec 0.2).
func TestSignLogOfAnEarlierNetworkIsRefused(t *testing.T) {
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "1000")}, time.Second, 0))
	config.SnapshotPath = snapshotPath
	vote, _ := consensus.SignVote(testSigner("validator"), consensus.Vote{Height: 1, Type: consensus.Prevote, Validator: "validator"})
	phase1, _ := json.Marshal(map[string]any{"Height": 1, "Votes": []consensus.Vote{vote}})
	phase1Ahead, _ := json.Marshal(map[string]any{"Height": 9})
	anotherNetwork, _ := json.Marshal(signLog{NetworkID: "othernet", Height: 1, Votes: []consensus.Vote{vote}})
	thisNetwork, _ := json.Marshal(signLog{NetworkID: testNetworkID, Height: 1, Votes: []consensus.Vote{vote}})
	for name, data := range map[string][]byte{"a Phase 1 log": phase1, "a Phase 1 log of a later height": phase1Ahead,
		"another network's log": anotherNetwork, "this network's": thisNetwork} {
		if err := os.WriteFile(snapshotPath+".votes", data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(config, NewHTTPTransport(nil))
		if refused := err != nil && strings.Contains(err.Error(), "belongs to an earlier network"); refused != (name != "this network's") {
			t.Errorf("%s: Open error %v", name, err)
		}
	}
}

// A write that fails after writeAtomic created its temporary file removes that file. The backup mirror
// names its temporary file by height, so a share that refuses the rename would otherwise keep one stale
// file per commit. Here the rename fails because a folder stands where the file should go.
func TestFailedWriteLeavesNoTemporaryFile(t *testing.T) {
	backupPath := filepath.Join(t.TempDir(), "backup.json")
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatal(err)
	}
	temporaryPath := backupPath + ".7.tmp"
	if err := writeAtomic(backupPath, temporaryPath, []byte("{}")); err == nil {
		t.Fatal("writeAtomic renamed a file over a folder")
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the failed write left its temporary file %s behind (%v)", temporaryPath, err)
	}
}
