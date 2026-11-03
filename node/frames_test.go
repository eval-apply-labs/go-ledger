package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// framesOf reads the frames of a public folder's hdls/frames, one per line.
func framesOf(t *testing.T, node *Node) []Frame {
	t.Helper()
	var frames []Frame
	for _, line := range bytes.SplitAfter(publicFile(t, node, framesPath), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var frame Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("a line of hdls/frames does not read: %v: %q", err, line)
		}
		frames = append(frames, frame)
	}
	return frames
}

// The engine writes hdls/frames into its public folder (Phase 4 spec §4.4): a line for each frame it saw
// start at a height it committed, with the height the set signs from, the set's hash in hex and the set.
// A frame starts where the names or the keys of the set change, not where the powers alone do: in dynamic
// mode a payment that moves stake changes them, and the file was written again at almost every busy
// block (review). Here the founder backs itself with 1000, and the shop is seated from 10 of stake. The
// founder pays the shop 5 at height 1, which changes only the founder's power, and 6 more at height 2,
// which seats the shop (it gets each amount less the payment's cost): a frame starts at height 3.
// Restarted from its snapshot, the node reads the file back, but for a line of height 9 added to it: a
// line above the height in progress, as the node leaves when it is opened from an older snapshot than its
// last, or seeded onto the other side of a fork, is dropped (review). The founder pays the shop 10 more at
// height 3, which changes only the powers, and at height 4 a ⅔ vote gives the shop a new key: a frame of
// the same names starts at height 5.
func TestFramesFileListsEachFrameTheNodeSawStart(t *testing.T) {
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), selfDelegated("shop", "0")}, time.Hour, time.Hour)
	config := testConfig(dynamicChain("10"), "founder", genesis)
	folder := t.TempDir()
	config.SnapshotPath, config.PublicPath = filepath.Join(folder, "snapshot.json"), filepath.Join(folder, "public")
	// commitHeight commits the height in progress: the founder, with more than ⅔ of the power, decides
	// within startHeight.
	commitHeight := func(node *Node) {
		t.Helper()
		height := node.height()
		if node.startHeight(); node.height() != height+1 {
			t.Fatalf("premise: the node commits height %d", height)
		}
	}
	payAndCommit := func(node *Node, amount string) {
		t.Helper()
		if err := admitPaymentNow(node, signedPayment("founder", "shop", amount, node.height())); err != nil {
			t.Fatalf("premise: the payment is pooled: %v", err)
		}
		commitHeight(node)
	}
	wantFrames := func(node *Node, want ...Frame) {
		t.Helper()
		node.publicFolder.waitUntilIdle()
		frames := framesOf(t, node)
		if len(frames) != len(want) {
			t.Fatalf("hdls/frames lists %d frames, want %d: %v", len(frames), len(want), frames)
		}
		for i, frame := range frames {
			hash := ledger.ValidatorsHash(want[i].Validators)
			if frame.Height != want[i].Height || frame.ValidatorsHash != hex.EncodeToString(hash[:]) || ledger.ValidatorsHash(frame.Validators) != hash {
				t.Errorf("hdls/frames lists %v from height %d, under the hash %s; want %v from %d", frame.Validators, frame.Height, frame.ValidatorsHash, want[i].Validators, want[i].Height)
			}
		}
	}

	node := New(config, &recordingTransport{}) // driven by hand
	payAndCommit(node, "5")
	payAndCommit(node, "6")
	seatsTheShop := Frame{Height: 3, Validators: validatorsOf([]string{"founder", "shop"}, 989, 10)}
	wantFrames(node, seatsTheShop)
	node.Stop()
	staleLine, _ := jsonAnswer(Frame{Height: 9, Validators: validatorsOf([]string{"founder"}, 1)})
	framesFile := filepath.Join(config.PublicPath, framesPath)
	if file, err := os.ReadFile(framesFile); err != nil || os.WriteFile(framesFile, append(file, staleLine...), 0o644) != nil {
		t.Fatalf("premise: the frames file takes a line: %v", err)
	}

	restarted := New(config, &recordingTransport{})
	defer restarted.Stop()
	wantFrames(restarted, seatsTheShop)
	payAndCommit(restarted, "10")
	rekey := ledger.Reassignment{Name: "shop", NewKey: testSigner("shop-new-key").PublicKeyBase64(), Sequence: restarted.state.GovernanceSequence}
	rekey, _ = ledger.SignReassignmentNewKey(testSigner("shop-new-key"), rekey)
	vote, _ := ledger.SignReassignmentVote(testSigner("founder"), "founder", rekey)
	rekey.Votes = []ledger.ReassignmentVote{vote}
	if _, err := restarted.addReassignmentVotes(rekey); err != nil {
		t.Fatalf("premise: the vote to give the shop a new key is pooled: %v", err)
	}
	commitHeight(restarted)
	rekeyedShop := validatorsOf([]string{"founder", "shop"}, 979, 20)
	rekeyedShop[1].PublicKey = rekey.NewKey
	wantFrames(restarted, seatsTheShop, Frame{Height: 5, Validators: rekeyedShop})
}

// framesNetwork is a dynamic-mode network on a bus whose validators each publish a public face, as a
// fetcher serves it: hdls/status, hdls/checkpoint and hdls/frames from the node's public folder, and
// hdls/record/<unit> from its record. With tamperFrames set, a face serves its frames with each member's
// power raised by one, and the hash of that set, which no block names.
type framesNetwork struct {
	nodes        []*Node
	tamperFrames atomic.Bool
}

// newFramesNetwork starts a node for each of names, from genesis, each with a snapshot and a public folder
// in a folder of its own. The accounts of faced get a public face each, whose URL is their public address.
func newFramesNetwork(t *testing.T, accounts []ledger.Account, faced map[string]bool, names []string) *framesNetwork {
	t.Helper()
	network := &framesNetwork{nodes: make([]*Node, len(names))}
	folders := map[string]string{}
	for i, account := range accounts {
		folder := t.TempDir()
		folders[account.Name] = folder
		if !faced[account.Name] {
			continue
		}
		owner := slices.Index(names, account.Name)
		publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			node := network.nodes[owner]
			switch path := request.URL.Path; {
			case path == "/"+framesPath && network.tamperFrames.Load():
				for _, frame := range framesOf(t, node) {
					for j := range frame.Validators {
						frame.Validators[j].Power++
					}
					hash := ledger.ValidatorsHash(frame.Validators)
					frame.ValidatorsHash = hex.EncodeToString(hash[:])
					line, _ := jsonAnswer(frame)
					responseWriter.Write(line)
				}
			case path == "/hdls/status" || path == "/hdls/checkpoint" || path == "/"+framesPath:
				http.ServeFile(responseWriter, request, filepath.Join(node.config.PublicPath, path))
			case strings.HasPrefix(path, "/hdls/record/"):
				http.ServeFile(responseWriter, request, filepath.Join(node.recordDirectory(), strings.TrimPrefix(path, "/hdls/record/")))
			default:
				http.NotFound(responseWriter, request)
			}
		}))
		t.Cleanup(publicFace.Close) // after the test's deferred calls, which stop the nodes
		accounts[i].PublicAddress = publicFace.URL
	}
	genesis := testGenesis(accounts, 200*time.Millisecond, 0)
	messageBus := newBus()
	for i, name := range names {
		config := testConfig(dynamicChain("50"), name, genesis)
		config.SnapshotPath, config.PublicPath = filepath.Join(folders[name], "snapshot.json"), filepath.Join(folders[name], "public")
		network.nodes[i] = New(config, messageBus)
		messageBus.register(name, network.nodes[i])
	}
	for _, node := range network.nodes {
		node.Start()
		t.Cleanup(node.Stop)
	}
	return network
}

// commitEverywhere hands every node the payment of amount from `from` to `to`, or, with no amount, an
// update of the shop's description, for the block two past the height in progress, so that no block in
// the making can miss it, and waits until every node has committed that block. Pooled, it is work until
// then (spec 6.6), so the network commits the blocks before it too.
func (network *framesNetwork) commitEverywhere(t *testing.T, from, to, amount string) {
	t.Helper()
	blockID := network.nodes[0].Snapshot().Height + 2
	for _, node := range network.nodes {
		if amount == "" {
			node.SubmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "shop", Description: "after the payments", BlockID: blockID}))
		} else {
			node.SubmitPayment(signedPayment(from, to, amount, blockID))
		}
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, node := range network.nodes {
			if node.Snapshot().Height <= blockID {
				return false
			}
		}
		return true
	}, "the network did not commit the block")
}

// A node away while ⅔ of the set changed in three steps walks the frames and catches up (Phase 4 spec
// §4.3, §6). In dynamic mode four validators of genesis each back themselves with 100. First one pays
// 10 out of its stake, which changes only its power, and lists no frame; then in three steps three of
// them pay their whole stake to a newcomer each, who takes their seat, and one more block commits. The
// newest checkpoint is then signed by one validator of genesis and three newcomers: a quarter of the
// genesis frame, which a fresh node, trusting the genesis frame, refuses on its own. It reads hdls/frames
// from the public faces of the genesis validators, takes each frame that the one before signed off,
// checked against their record entries, until the frame it holds signed the checkpoint; it then adopts
// it. The first frame it takes was signed off by the genesis names and keys at another power: more than
// ⅔ of the genesis frame, by its own powers, signed it all the same.
//
// Before that, the faces serve their frames with each power raised by one: every frame's set then hashes
// to another than the one the block before it names as the next (NextValidatorsHash), and the node takes
// no frame, and no checkpoint.
func TestNodeAwayWhileTheSetChangedWalksTheFrames(t *testing.T) {
	validators, newcomers := validatorNames(4), []string{"newcomer-0", "newcomer-1", "newcomer-2"}
	var accounts []ledger.Account
	faced := map[string]bool{}
	for _, name := range validators {
		accounts = append(accounts, selfDelegated(name, "100"))
		faced[name] = true
	}
	for _, name := range newcomers {
		accounts = append(accounts, selfDelegated(name, "0"))
	}
	accounts = append(accounts, testAccount("shop", "0"))
	network := newFramesNetwork(t, accounts, faced, append(slices.Clone(validators), newcomers...))
	network.commitEverywhere(t, validators[3], "shop", "10")
	for i, newcomer := range newcomers {
		network.commitEverywhere(t, validators[i], newcomer, "100")
	}
	network.commitEverywhere(t, "", "", "") // the newcomers' set signs a block
	newest := network.nodes[0].Snapshot().checkpoint()
	if frames := framesOf(t, network.nodes[0]); len(frames) != 3 {
		t.Fatalf("the genesis validator's hdls/frames lists %d frames, want 3, one for each step that seated a newcomer", len(frames))
	}

	config := testConfig(dynamicChain("50"), "joiner", network.nodes[0].config.Genesis)
	config.allowLoopbackPublicAddresses = true // the public faces listen on 127.0.0.1
	joiner := New(config, &recordingTransport{})
	if joiner.verifyCheckpoint(newest, joiner.view.Load().validators) {
		t.Fatal("premise: more than ⅔ of the genesis frame signed the newest checkpoint, so no walk is needed")
	}

	network.tamperFrames.Store(true)
	if _, _, found := joiner.catchUpCheckpoint(); found {
		t.Error("the node took a checkpoint by frames whose sets no block named as the next")
	}
	network.tamperFrames.Store(false)
	joiner.Start()
	defer joiner.Stop()
	waitFor(t, 15*time.Second, func() bool { return joiner.Snapshot().Height > newest.Commit.Height },
		"the node did not catch up by walking the frames")
	if snapshot := joiner.Snapshot(); ledger.AccountsDigest(snapshot.Accounts) != ledger.AccountsDigest(network.nodes[0].Snapshot().Accounts) {
		t.Error("the node caught up to another state than the network's")
	}
}

// validatorsUnderKeys is the validators of names, of power 1 each, each under the key testSigner holds
// for keyPrefix and its name, with those signers by name: the set a key change, or a forger, seats.
func validatorsUnderKeys(keyPrefix string, names []string) ([]consensus.Validator, map[string]signing.Signer) {
	validators, signers := validatorsOf(names), map[string]signing.Signer{}
	for i, name := range names {
		signers[name] = testSigner(keyPrefix + name)
		validators[i].PublicKey = signers[name].PublicKeyBase64()
	}
	return validators, signers
}

// testFace is a public face that serves the files a test puts in it, by path, as a fetcher serves a
// public folder, and counts the reads of hdls/frames. A test changes its files only between fetches.
type testFace struct {
	files      map[string][]byte
	framesRead atomic.Int32
	server     *httptest.Server
}

func newTestFace(t *testing.T) *testFace {
	face := &testFace{files: map[string][]byte{}}
	face.server = httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/"+framesPath {
			face.framesRead.Add(1)
		}
		if file, found := face.files[request.URL.Path]; found {
			responseWriter.Write(file)
		} else {
			http.NotFound(responseWriter, request)
		}
	}))
	t.Cleanup(face.server.Close)
	return face
}

// put sets the face's file at path to values, one JSON line each.
func (face *testFace) put(path string, values ...any) {
	var file []byte
	for _, value := range values {
		line, _ := jsonAnswer(value)
		file = append(file, line...)
	}
	face.files["/"+path] = file
}

// faceJoiner is a fresh node, named joiner, that trusts the genesis of the static validators names, the
// first of which publishes face as its public address, and whose gossip peers are at the URLs given. It
// returns the node and the genesis.
func faceJoiner(t *testing.T, face *testFace, names []string, peers ...string) (*Node, ledger.LedgerState) {
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	accounts[0].PublicAddress = face.server.URL
	genesis := testGenesis(append(accounts, testAccount("joiner", "0")), time.Second, 0)
	config := testConfig(staticChain(names...), "joiner", genesis)
	config.allowLoopbackPublicAddresses = true // the face listens on 127.0.0.1
	var peerURLs [][]string
	for _, peer := range peers {
		peerURLs = append(peerURLs, []string{peer})
	}
	return New(config, &peeredRecorder{peers: peerURLs}), genesis
}

// A walk takes a frame only once more than ⅔ of the frame it holds, under that frame's keys, signed the
// block before it, and that block, as its hash recomputed from the record entry shows, names the frame's
// set as the next one (signsOff). A face that lies in its record is caught by one of the two; no test
// caught either lie before (review). Here a fresh node trusts the genesis frame of four validators, and a
// checkpoint of height 5 is signed under other keys of all four, so it walks the frames of the one public
// face: a frame from height 3, and the record entry of height 2. The face does not hold the entry of
// height 3, as a face that has just committed height 2 does not, and no signature the walk checks covers
// it. The control: the entry holds the commit, by the genesis keys, of a block whose next set is that of
// new keys, and the walk takes it. Lie 1: the commit is by keys of a forger's, of a block that names the
// forger's set as the one that signs it and the next one; only anchorVouches stops it. Lie 2: the commit
// is by the genesis keys, of a block whose next set is the genesis set, and the entry claims the
// forger's; only the recomputed block hash stops it.
func TestWalkRefusesARecordThatLies(t *testing.T) {
	names := validatorNames(4)
	face := newTestFace(t)
	joiner, genesis := faceJoiner(t, face, names)
	face.put("hdls/status", map[string]any{"heightInProgress": 6}) // ahead of the node
	genesisSet := validatorsOf(names)
	newSet, newSigners := validatorsUnderKeys("new-key-", names)
	forgedSet, forgerSigners := validatorsUnderKeys("forger-", names)
	// listFrameAt3 has the face list the frame of claimedNext from height 3, and hold the record entry of
	// height 2: the commit, by signers under the keys signingSet gives them, of the block whose next set is
	// signedNext, while the entry claims claimedNext as the next set.
	listFrameAt3 := func(signingSet []consensus.Validator, signers map[string]signing.Signer, signedNext, claimedNext []consensus.Validator) {
		previousBlockHash, sealedDigest := [32]byte{7}, [32]byte{8}
		blockHash := ledger.BlockHashOf(2, previousBlockHash, ledger.ValidatorsHash(signingSet), ledger.ValidatorsHash(signedNext), sealedDigest)
		face.put(unitPath("hdls/record", 2), RecordEntry{Height: 2, PreviousBlockHash: previousBlockHash, NextValidatorsHash: ledger.ValidatorsHash(claimedNext),
			SealedDigest: sealedDigest, Commit: buildCommit(t, 2, blockHash, signingSet, signers), Validators: signingSet})
		claimedHash := ledger.ValidatorsHash(claimedNext)
		face.put(framesPath, Frame{Height: 3, ValidatorsHash: hex.EncodeToString(claimedHash[:]), Validators: claimedNext})
	}

	listFrameAt3(genesisSet, nil, newSet, newSet)
	if joiner.trustedFrameOf(checkpointOf(t, 5, genesis, newSet, newSigners), joiner.view.Load()) == nil {
		t.Error("the walk did not take a frame of new keys that the genesis frame signed off")
	}
	forged := checkpointOf(t, 5, genesis, forgedSet, forgerSigners)
	listFrameAt3(forgedSet, forgerSigners, forgedSet, forgedSet)
	if joiner.trustedFrameOf(forged, joiner.view.Load()) != nil {
		t.Error("the walk took a frame that keys of a forger's signed off, not the genesis frame's")
	}
	listFrameAt3(genesisSet, nil, genesisSet, forgedSet)
	if joiner.trustedFrameOf(forged, joiner.view.Load()) != nil {
		t.Error("the walk took a frame that the record entry claims as the next set, and its block does not name")
	}
}

// A walk reads the frames of a public face only when the face's status is ahead of the node: a face at
// the node's height lists no frame after the node's trusted one. Before, a checkpoint that is sound on
// its own but that no walk reaches made the node read every face's frames file again for each peer that
// served it, at every tick (review). Here an up-to-date node's gossip peer serves a checkpoint of height
// 1000 under keys of a forger's, which costs the forger nothing, and the one public face is at the node's
// height.
func TestWalkReadsNoFramesOfAFaceNotAheadOfTheNode(t *testing.T) {
	names := validatorNames(4)
	forgedSet, forgerSigners := validatorsUnderKeys("forger-", names)
	var forged Checkpoint
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		servePeer(responseWriter, request, forged)
	}))
	defer peer.Close()
	face := newTestFace(t)
	face.put("hdls/status", map[string]any{"heightInProgress": 1})
	joiner, genesis := faceJoiner(t, face, names, peer.URL)
	forged = checkpointOf(t, 1000, genesis, forgedSet, forgerSigners)
	if _, _, found := joiner.catchUpCheckpoint(); found || !joiner.selfConsistent(forged) {
		t.Fatal("premise: the forged checkpoint is sound on its own, and refused")
	}
	if framesRead := face.framesRead.Load(); framesRead != 0 {
		t.Errorf("the walk read the frames file of a face at the node's height %d times, want never", framesRead)
	}
}

// A walk takes at most maxFrames frames from a face at one attempt (walkFrames). Here a fresh node trusts
// the genesis of one validator, and its public face lists maxFrames+1 frames, from height 2 on, each of
// the validator under a key of its own, with the record entry before each, whose commit the key before
// signed. A checkpoint that the key of frame maxFrames signed passes; one that the key after it signed
// does not.
func TestWalkTakesAtMostMaxFramesFromAFace(t *testing.T) {
	names := []string{"validator"}
	face := newTestFace(t)
	joiner, genesis := faceJoiner(t, face, names)
	face.put("hdls/status", map[string]any{"heightInProgress": maxFrames + 10}) // ahead of the node
	// The set and the signers of the genesis frame, then those of each frame listed.
	sets, signers := [][]consensus.Validator{validatorsOf(names)}, []map[string]signing.Signer{nil}
	var frames []any
	units := map[int64][]any{} // the record entries, by the first height of their unit
	for height := int64(1); height <= maxFrames+1; height++ {
		nextSet, nextSigners := validatorsUnderKeys(fmt.Sprintf("from-%d-", height+1), names)
		signingSet := sets[height-1]
		blockHash := ledger.BlockHashOf(height, [32]byte{}, ledger.ValidatorsHash(signingSet), ledger.ValidatorsHash(nextSet), [32]byte{})
		firstHeight := height - height%recordUnitHeights
		units[firstHeight] = append(units[firstHeight], RecordEntry{Height: height, NextValidatorsHash: ledger.ValidatorsHash(nextSet),
			Commit: buildCommit(t, height, blockHash, signingSet, signers[height-1]), Validators: signingSet})
		frames = append(frames, Frame{Height: height + 1, Validators: nextSet})
		sets, signers = append(sets, nextSet), append(signers, nextSigners)
	}
	face.put(framesPath, frames...)
	for firstHeight, unit := range units {
		face.put(unitPath("hdls/record", firstHeight), unit...)
	}

	for frame, passes := range map[int]bool{maxFrames: true, maxFrames + 1: false} {
		checkpoint := checkpointOf(t, maxFrames+5, genesis, sets[frame], signers[frame])
		if passed := joiner.trustedFrameOf(checkpoint, joiner.view.Load()) != nil; passed != passes {
			t.Errorf("a checkpoint that frame %d after the trusted one signed: passed %v, want %v", frame, passed, passes)
		}
	}
}
