package node

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// recordedRun is what a sole validator recorded on a dynamic chain: it ran with a snapshot path, and a
// payment to it in each of blocks 1 to 3 raised its power, so the signing set changed at heights 2 to 4.
type recordedRun struct {
	chain           ledger.Chain
	node            *Node                  // stopped
	blocks          map[int64]ledger.Block // the blocks it proposed, by height
	payments        []ledger.Payment
	committedHeight int64
}

func runRecordedNode(t *testing.T) recordedRun {
	t.Helper()
	chain := dynamicChain("1")
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), testAccount("alice", "100")}, 5*time.Second, 10*time.Millisecond)
	config := testConfig(chain, "founder", genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	recorder := &recordingTransport{}
	run := recordedRun{chain: chain, node: New(config, recorder), blocks: map[int64]ledger.Block{}}
	for blockID := int64(1); blockID <= 3; blockID++ {
		payment := signedPayment("alice", "founder", "10", blockID)
		run.node.SubmitPayment(payment)
		run.payments = append(run.payments, payment)
	}
	run.node.Start()
	keepBusy(t, "founder", run.node)
	waitFor(t, 5*time.Second, func() bool { return run.node.Snapshot().Height > 6 }, "the validator did not commit six blocks")
	run.node.Stop()
	for _, proposal := range recorder.proposals() {
		run.blocks[proposal.Proposal.Height] = proposal.Block
	}
	run.committedHeight = run.node.state.Height
	for height := int64(1); height <= 3; height++ {
		if len(run.blocks[height].Content.Payments) != 1 {
			t.Fatalf("premise: block %d carries one payment", height)
		}
	}
	return run
}

// recordFileEntries decodes every line of the record file at path, which must end in a complete line.
func recordFileEntries[Entry any](t *testing.T, path string) []Entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(data, []byte("\n"))
	if partial := lines[len(lines)-1]; len(partial) != 0 {
		t.Fatalf("%s ends in a partial line: %.60q", path, partial)
	}
	entries := make([]Entry, len(lines)-1)
	for i, line := range lines[:len(lines)-1] {
		if err := json.Unmarshal(line, &entries[i]); err != nil {
			t.Fatalf("%s, line %d: %v", path, i+1, err)
		}
	}
	return entries
}

// unitNames lists the rolling unit files of the node's record.
func unitNames(t *testing.T, node *Node) []string {
	t.Helper()
	files, err := os.ReadDir(node.recordDirectory())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name())
	}
	return names
}

// rollingEntries is every rolling entry of the node's record, oldest first.
func rollingEntries(t *testing.T, node *Node) []RecordEntry {
	t.Helper()
	var entries []RecordEntry
	for _, name := range unitNames(t, node) {
		entries = append(entries, recordFileEntries[RecordEntry](t, filepath.Join(node.recordDirectory(), name))...)
	}
	return entries
}

// recordBytes is everything the node's record files hold, rolling and lifelong.
func recordBytes(t *testing.T, node *Node) []byte {
	t.Helper()
	var all []byte
	for _, name := range unitNames(t, node) {
		data, err := os.ReadFile(filepath.Join(node.recordDirectory(), name))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, data...)
	}
	data, err := os.ReadFile(node.lifelongRecordPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return append(all, data...)
}

// Every committed block has a rolling entry, with the proposal that proposed it and the commit that
// decided it. The payments the blocks carried, and the payer's name, are nowhere in the record, and a
// block that carries only payments has no lifelong entry.
func TestRecordKeepsCertificatesAndNeverPayments(t *testing.T) {
	run := runRecordedNode(t)
	entries := rollingEntries(t, run.node)
	if int64(len(entries)) != run.committedHeight {
		t.Fatalf("%d rolling entries for %d committed blocks", len(entries), run.committedHeight)
	}
	for i, entry := range entries {
		blockHash := ledger.BlockHash(run.blocks[entry.Height])
		switch {
		case entry.Height != int64(i+1):
			t.Fatalf("entry %d is for height %d", i, entry.Height)
		case entry.Proposal.Height != entry.Height || entry.Proposal.Proposer != "founder" || entry.Proposal.BlockHash != blockHash || entry.Proposal.Signature == "":
			t.Fatalf("entry %d does not hold the signed proposal of its block: %+v", entry.Height, entry.Proposal)
		case entry.Commit.Height != entry.Height || entry.Commit.BlockHash != blockHash || len(entry.Commit.Votes) != 1:
			t.Fatalf("entry %d does not hold the commit of its block", entry.Height)
		}
	}
	record := recordBytes(t, run.node)
	for _, payment := range run.payments {
		if bytes.Contains(record, []byte(payment.Signature)) {
			t.Fatalf("the record holds the payment for block %d", payment.BlockID)
		}
	}
	// As JSON strings, with their quotes, which base64 never holds: a key or signature may spell out
	// alice by chance.
	if bytes.Contains(record, []byte(`"Payments"`)) || bytes.Contains(record, []byte(`"alice"`)) {
		t.Fatal("the record holds a block's payments")
	}
	if _, err := os.Stat(run.node.lifelongRecordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocks with payments only made a lifelong record: %v", err)
	}
}

// The record never holds a block's state digest or nonce, in any spelling: only the sealed digest that
// mixes them.
func TestRecordNeverHoldsTheBareStateDigest(t *testing.T) {
	run := runRecordedNode(t)
	record := recordBytes(t, run.node)
	for height := int64(1); height <= run.committedHeight; height++ {
		block := run.blocks[height]
		for _, secret := range [][32]byte{block.StateDigest, block.Nonce} {
			jsonForm, _ := json.Marshal(secret)
			if serializedBlock, _ := json.Marshal(block); !bytes.Contains(serializedBlock, jsonForm) {
				t.Fatal("premise: the search looks for a digest as JSON writes it")
			}
			for _, form := range [][]byte{jsonForm, []byte(hex.EncodeToString(secret[:])), []byte(base64.StdEncoding.EncodeToString(secret[:])), secret[:]} {
				if bytes.Contains(record, form) {
					t.Fatalf("the record holds the state digest or the nonce of block %d", height)
				}
			}
		}
	}
}

// Anyone can check the record from its own fields: each entry's block hash, recomputed from its
// height, previous block hash, next validators hash and sealed digest with the latest set the record
// holds at or before it, is the hash its proposal and commit sign; both verify under that set; each
// entry names the block hash of the one before; and each entry's set is the one the entry before named
// as the next (Phase 4 spec §4.3), here across the three blocks that changed it. The set is written in
// the first entry and wherever it changes, and only there.
func TestRecordChainChecksFromItsOwnFields(t *testing.T) {
	run := runRecordedNode(t)
	entries := rollingEntries(t, run.node)
	var validators []consensus.Validator
	setsWritten := 0
	for i, entry := range entries {
		if len(entry.Validators) > 0 {
			validators = entry.Validators
			setsWritten++
		}
		set := consensus.NewValidatorSet(validators)
		blockHash := ledger.BlockHashOf(entry.Height, entry.PreviousBlockHash, ledger.ValidatorsHash(validators), entry.NextValidatorsHash, entry.SealedDigest)
		switch {
		case blockHash != entry.Commit.BlockHash || blockHash != entry.Proposal.BlockHash:
			t.Fatalf("the block hash of entry %d does not recompute from its fields", entry.Height)
		case !entry.Commit.Verify(run.chain.Scheme, run.chain.NetworkID, set):
			t.Fatalf("the commit of entry %d does not verify under the record's set", entry.Height)
		case !entry.Proposal.Verify(run.chain.Scheme, run.chain.NetworkID, set.PublicKey(entry.Proposal.Proposer)):
			t.Fatalf("the proposal of entry %d does not verify under the record's set", entry.Height)
		case i == 0 && entry.PreviousBlockHash != run.chain.GenesisHash:
			t.Fatal("the first entry does not build on the genesis hash")
		case i > 0 && entry.PreviousBlockHash != entries[i-1].Commit.BlockHash:
			t.Fatalf("entry %d does not name the block hash of the entry before", entry.Height)
		case i > 0 && ledger.ValidatorsHash(validators) != entries[i-1].NextValidatorsHash:
			t.Fatalf("entry %d is signed by another set than the one the entry before named as the next", entry.Height)
		}
		setChanged := i == 0 || run.blocks[entry.Height].ValidatorsHash != run.blocks[entry.Height-1].ValidatorsHash
		if (len(entry.Validators) > 0) != setChanged {
			t.Fatalf("entry %d: the set changed: %v, the set written: %v", entry.Height, setChanged, entry.Validators)
		}
	}
	if setsWritten != 4 {
		t.Fatalf("premise: the set is written at heights 1 to 4, got %d sets", setsWritten)
	}
	if entries[len(entries)-1].Commit.BlockHash != run.node.lastBlockHash {
		t.Fatal("the last entry is not the node's last block, whose hash vouches for every entry before")
	}
}

// syntheticRecord drives a node's record by hand with made-up blocks and unsigned certificates: the
// record checks no signature, so only the heights, the hashes and the retention matter. The node is
// one of four static validators, beside a name that is no validator.
type syntheticRecord struct {
	node              *Node // never started
	signingSet        *consensus.ValidatorSet
	lastBlockHash     [32]byte
	proposalSignature string // what the next proposals carry as their signature
}

func newSyntheticRecord(t *testing.T, retainBlocks int64) *syntheticRecord {
	names := validatorNames(4)
	accounts := []ledger.Account{testAccount("outsider", "5")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	chain := staticChain(names...)
	config := testConfig(chain, names[0], testGenesis(accounts, time.Hour, time.Hour))
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	config.Genesis.Parameters.RetainBlocks = retainBlocks
	node := New(config, &recordingTransport{})
	return &syntheticRecord{node: node, signingSet: chain.ValidatorRule.SetFor(node.state)}
}

// onNetwork moves the synthetic record, before its first block, to the network of genesisHash.
func (record *syntheticRecord) onNetwork(genesisHash [32]byte) *syntheticRecord {
	record.node.config.Chain.GenesisHash, record.lastBlockHash = genesisHash, genesisHash
	return record
}

// commitBlock records the block of content at height, after which the retention is retainAfter, as
// commit does, and returns its hash.
func (record *syntheticRecord) commitBlock(height int64, content ledger.BlockContent, retainAfter int64) [32]byte {
	previousState := record.node.state
	block := ledger.Block{Height: height, PreviousBlockHash: record.lastBlockHash, ValidatorsHash: ledger.ValidatorsHash(record.signingSet.Validators()),
		Nonce: [32]byte{byte(height), byte(height >> 8)}, Content: content, StateDigest: [32]byte{1, byte(height), byte(height >> 8)}}
	blockHash := ledger.BlockHash(block)
	record.node.state.Height = height
	record.node.state.Parameters.RetainBlocks = retainAfter
	record.node.recordCommittedBlock(previousState, block, consensus.SignedProposal{Height: height, BlockHash: blockHash, Signature: record.proposalSignature},
		consensus.Commit{Height: height, BlockHash: blockHash}, record.signingSet)
	record.lastBlockHash = blockHash
	return blockHash
}

// commit records an empty block at each height, keeping the retention.
func (record *syntheticRecord) commit(heights ...int64) {
	for _, height := range heights {
		record.commitBlock(height, ledger.BlockContent{}, record.node.state.Parameters.RetainBlocks)
	}
}

// keptUntil is the KeepUntilHeight of each entry of the unit file that holds height.
func (record *syntheticRecord) keptUntil(t *testing.T, height int64) []int64 {
	t.Helper()
	var heights []int64
	for _, entry := range recordFileEntries[RecordEntry](t, unitPath(record.node.recordDirectory(), height)) {
		heights = append(heights, entry.KeepUntilHeight)
	}
	return heights
}

const (
	firstUnit  = "00000000000000000000.jsonl"
	secondUnit = "00000000000000000100.jsonl"
)

// A unit file goes as a whole: its oldest entries stay long past their own KeepUntilHeight, until the
// height passes the unit's last one. The next unit's first entry carries the set, though it did not
// change, so the record still checks once the unit before is gone.
func TestRecordPrunesWholeUnitsAfterKeepUntilHeight(t *testing.T) {
	record := newSyntheticRecord(t, 10)
	record.commit(1, 2, 3, 99, 100, 101, 102, 109)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{firstUnit, secondUnit}) {
		t.Fatalf("units %v, want the first two", names)
	}
	if kept := record.keptUntil(t, 1); !slices.Equal(kept, []int64{11, 12, 13, 109}) {
		t.Fatalf("the first unit keeps its entries until %v", kept)
	}
	record.commit(110)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{secondUnit}) {
		t.Fatalf("units %v after height 110, want only the second", names)
	}
	entries := rollingEntries(t, record.node)
	if len(entries) != 5 || entries[0].Height != 100 || len(entries[0].Validators) != 4 {
		t.Fatalf("the second unit does not start with its set at height 100: %+v", entries[0])
	}
	for _, entry := range entries[1:] {
		if len(entry.Validators) != 0 {
			t.Fatalf("entry %d repeats the unchanged set", entry.Height)
		}
	}
}

// A cut in RetainBlocks applies only to the entries written after it. The block that carries the cut
// keeps the retention before it, a later entry in the same unit never keeps less than the one before
// it, so the unit is kept as long as its oldest entries were promised; the next unit follows the cut.
func TestRetentionCutAppliesOnlyToNewRecords(t *testing.T) {
	record := newSyntheticRecord(t, 50)
	record.commit(1, 90)
	record.commitBlock(91, ledger.BlockContent{}, 10) // the cut
	record.commit(92, 99, 100, 110)
	if kept := record.keptUntil(t, 1); !slices.Equal(kept, []int64{51, 140, 141, 141, 141}) {
		t.Fatalf("the first unit keeps its entries until %v", kept)
	}
	if kept := record.keptUntil(t, 100); !slices.Equal(kept, []int64{110, 120}) {
		t.Fatalf("the second unit keeps its entries until %v", kept)
	}
	record.commit(141)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{firstUnit, secondUnit}) {
		t.Fatalf("units %v at height 141, want both", names)
	}
	record.commit(142)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{secondUnit}) {
		t.Fatalf("units %v at height 142, want only the second", names)
	}
}

// A longer RetainBlocks applies at once, to old units too, from the block that carries it: a unit is
// kept until the height passes its last height plus the current retention, though its own
// KeepUntilHeight has passed.
func TestLongerRetentionKeepsOldUnits(t *testing.T) {
	record := newSyntheticRecord(t, 10)
	record.commit(1, 99, 100)
	record.commitBlock(110, ledger.BlockContent{}, 50) // the longer retention, at the height the old one deletes the first unit
	record.commit(149)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{firstUnit, secondUnit}) {
		t.Fatalf("units %v at height 149, want both", names)
	}
	record.commit(150)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{secondUnit}) {
		t.Fatalf("units %v at height 150, want only the second", names)
	}
}

// Pruning lists the record directory, a file per 100 heights retained, only once the oldest unit file is
// due, not at every commit. The test makes the directory unlistable, though its files can still be read
// and written by name: until the second unit, as pruning last read it, is due, pruning succeeds without
// listing; at that height it lists again, and fails.
func TestPruningListsTheRecordOnlyWhenTheOldestUnitIsDue(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists a directory without read permission")
	}
	record := newSyntheticRecord(t, 10)
	record.commit(1, 99, 100, 110)
	if names := unitNames(t, record.node); !slices.Equal(names, []string{secondUnit}) {
		t.Fatalf("premise: units %v after height 110, want only the second, read as ending at 110", names)
	}
	directory := record.node.recordDirectory()
	if err := os.Chmod(directory, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(directory, 0o700) })
	for height := int64(111); height <= 120; height++ {
		record.commit(height)
		if err := record.node.pruneRecord(height); err != nil {
			t.Fatalf("pruning at height %d listed the record: %v", height, err)
		}
	}
	record.commit(121)
	if err := record.node.pruneRecord(121); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("pruning at height 121, past 110 + 10, did not list the record: %v", err)
	}
}

// The record files carry the first 16 hex digits of the genesis hash in their names. A node of another
// genesis on the same snapshot path neither reads the old files nor extends them: it serves none of
// their entries, and records its own blocks, at the same heights and above, in files of its own.
func TestRecordFilesAreNamedByNetwork(t *testing.T) {
	parameterChange := ledger.BlockContent{ParameterChange: &ledger.ParameterChange{NewParameters: ledger.DefaultParameters()}}
	old := newSyntheticRecord(t, 1000).onNetwork([32]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xff})
	old.commit(1)
	old.commitBlock(2, parameterChange, 1000)
	snapshotPath := old.node.config.SnapshotPath
	if old.node.recordDirectory() != snapshotPath+".0123456789abcdef.record" || old.node.lifelongRecordPath() != snapshotPath+".0123456789abcdef.lifelong.jsonl" {
		t.Fatalf("the record is at %s and %s", old.node.recordDirectory(), old.node.lifelongRecordPath())
	}
	oldRecord := recordBytes(t, old.node)

	// The same genesis hash but for its sixteenth hex digit.
	newGenesisHash := [32]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xee, 0xff}
	record := newSyntheticRecord(t, 1000).onNetwork(newGenesisHash)
	record.node.config.SnapshotPath = snapshotPath
	for _, target := range []string{"/hdls/record", "/hdls/record?from=0", "/hdls/lifelong-record", "/hdls/lifelong-record?from=0"} {
		if heights := servedHeights(t, record.node, target); len(heights) != 0 {
			t.Fatalf("%s served heights %v of the old network", target, heights)
		}
	}
	record.commit(1)
	record.commitBlock(2, parameterChange, 1000)
	record.commitBlock(3, parameterChange, 1000)
	if !bytes.Equal(recordBytes(t, old.node), oldRecord) {
		t.Fatal("the node of the new network changed the old network's record")
	}
	files, err := os.ReadDir(filepath.Dir(snapshotPath))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name())
	}
	if want := []string{"snapshot.json.0123456789abcdee.lifelong.jsonl", "snapshot.json.0123456789abcdee.record",
		"snapshot.json.0123456789abcdef.lifelong.jsonl", "snapshot.json.0123456789abcdef.record"}; !slices.Equal(names, want) {
		t.Fatalf("files %v beside the snapshot, want %v", names, want)
	}
	if entries := rollingEntries(t, record.node); len(entries) != 3 || entries[0].PreviousBlockHash != newGenesisHash {
		t.Fatalf("the new network's record does not start at its own genesis: %+v", entries)
	}
	for target, want := range map[string][]int64{"/hdls/record": heightsFrom(1, 3), "/hdls/lifelong-record": {2, 3}} {
		if heights := servedHeights(t, record.node, target); !slices.Equal(heights, want) {
			t.Fatalf("%s served heights %v, want %v", target, heights, want)
		}
	}
}

// Rolling entries go in aligned units of 100 heights: the unit of height h starts at h - (h mod 100),
// and its file is named by that height. A node that catches up skips heights, so a unit file may hold
// fewer than 100 entries and start above its first height. Each unit file's first entry, and only it,
// carries the signing set, though the set did not change, so each unit checks on its own: every entry's
// block hash recomputes from its fields with the set its unit file holds.
func TestRecordUnitsHoldAHundredAlignedHeights(t *testing.T) {
	record := newSyntheticRecord(t, 100000)
	record.commit(heightsFrom(1, 250)...)
	record.commit(357, 358) // a catch-up skipped heights 251 to 356
	want := map[string][]int64{"00000000000000000000.jsonl": heightsFrom(1, 99), "00000000000000000100.jsonl": heightsFrom(100, 199),
		"00000000000000000200.jsonl": heightsFrom(200, 250), "00000000000000000300.jsonl": {357, 358}}
	if names := unitNames(t, record.node); !slices.Equal(names, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("units %v", names)
	}
	for name, wantHeights := range want {
		entries := recordFileEntries[RecordEntry](t, filepath.Join(record.node.recordDirectory(), name))
		var heights []int64
		for i, entry := range entries {
			heights = append(heights, entry.Height)
			if (i == 0) != (len(entry.Validators) > 0) {
				t.Fatalf("%s: entry %d carries the set: %v", name, entry.Height, len(entry.Validators) > 0)
			}
			if ledger.BlockHashOf(entry.Height, entry.PreviousBlockHash, ledger.ValidatorsHash(entries[0].Validators), entry.NextValidatorsHash, entry.SealedDigest) !=
				entry.Commit.BlockHash {
				t.Fatalf("%s: the block hash of entry %d does not recompute with the unit's set", name, entry.Height)
			}
		}
		if !slices.Equal(heights, wantHeights) {
			t.Fatalf("%s holds heights %v, want %v", name, heights, wantHeights)
		}
	}
}

// signersOf is the name, key and power of each name, as the synthetic record's state and set hold them.
func (record *syntheticRecord) signersOf(names ...string) []consensus.Validator {
	var signers []consensus.Validator
	for _, name := range names {
		signers = append(signers, consensus.Validator{Name: name, PublicKey: testSigner(name).PublicKeyBase64(), Power: record.signingSet.Power(name)})
	}
	return signers
}

// The lifelong record keeps, for every block that carries a governance action (a parameter change or
// the ⅔ name action) or evidence, those items and the name, key and power of each name that signed
// them, each once and in name order, power 0 for a name that is not a validator. Blocks with payments,
// account updates or holder requests, a freeze included, and empty blocks have no lifelong entry.
func TestLifelongRecordKeepsGovernanceAndEvidence(t *testing.T) {
	record := newSyntheticRecord(t, 1000)
	names := validatorNames(4)
	parameterChange := &ledger.ParameterChange{NewParameters: ledger.DefaultParameters(), Sequence: 3,
		Votes: []ledger.ParameterVote{{Validator: names[2], Signature: "two"}, {Validator: names[0], Signature: "zero"}, {Validator: names[1], Signature: "one"}}}
	reassignment := &ledger.Reassignment{Name: "outsider", Freeze: true, Sequence: 4,
		Votes: []ledger.ReassignmentVote{{Validator: names[0], Signature: "zero"}, {Validator: names[1], Signature: "one"}, {Validator: names[3], Signature: "three"}}}
	voteEvidence := []consensus.VoteEvidence{{Name: names[3],
		First:  consensus.Vote{Height: 2, Type: consensus.Prevote, BlockHash: [32]byte{1}, Validator: names[3], Signature: "first"},
		Second: consensus.Vote{Height: 2, Type: consensus.Prevote, BlockHash: [32]byte{2}, Validator: names[3], Signature: "second"}}}
	proposalEvidence := []consensus.ProposalEvidence{{Name: "outsider",
		First:  consensus.SignedProposal{Height: 3, BlockHash: [32]byte{1}, Proposer: "outsider", Signature: "first"},
		Second: consensus.SignedProposal{Height: 3, BlockHash: [32]byte{2}, Proposer: "outsider", Signature: "second"}}}

	record.commitBlock(1, ledger.BlockContent{
		Payments:       []ledger.Payment{{From: "outsider", To: names[0], Amount: rational("1"), BlockID: 1}},
		AccountUpdates: []ledger.AccountUpdate{{Name: names[1], Description: "a bank", BlockID: 1}},
		NameRequests: []ledger.NameRequest{{Kind: ledger.FreezeKind, Name: names[2], Requestor: names[2], BlockID: 1},
			{Kind: ledger.KeyChangeKind, Name: "outsider", Requestor: "outsider", NewKey: "new key", Bank: names[0], Fee: rational("1"), BlockID: 1}},
	}, 1000)
	parameterBlockHash := record.commitBlock(2, ledger.BlockContent{ParameterChange: parameterChange}, 1000)
	actionBlockHash := record.commitBlock(3, ledger.BlockContent{Reassignment: reassignment}, 1000)
	voteEvidenceBlockHash := record.commitBlock(4, ledger.BlockContent{VoteEvidence: voteEvidence}, 1000)
	proposalEvidenceBlockHash := record.commitBlock(5, ledger.BlockContent{ProposalEvidence: proposalEvidence}, 1000)
	bothBlockHash := record.commitBlock(6, ledger.BlockContent{VoteEvidence: voteEvidence, Reassignment: reassignment}, 1000)
	record.commit(7)

	want := []LifelongRecordEntry{
		{Height: 2, BlockHash: parameterBlockHash, ParameterChange: parameterChange, Signers: record.signersOf(names[0], names[1], names[2])},
		{Height: 3, BlockHash: actionBlockHash, Reassignment: reassignment, Signers: record.signersOf(names[0], names[1], names[3])},
		{Height: 4, BlockHash: voteEvidenceBlockHash, VoteEvidence: voteEvidence, Signers: record.signersOf(names[3])},
		{Height: 5, BlockHash: proposalEvidenceBlockHash, ProposalEvidence: proposalEvidence, Signers: record.signersOf("outsider")},
		// validator-3 signed a vote of the action and is convicted in the same block: one signer
		{Height: 6, BlockHash: bothBlockHash, Reassignment: reassignment, VoteEvidence: voteEvidence, Signers: record.signersOf(names[0], names[1], names[3])},
	}
	if want[3].Signers[0].Power != 0 || want[0].Signers[0].Power != 1 {
		t.Fatal("premise: the outsider has no power and the validators have power 1")
	}
	got := recordFileEntries[LifelongRecordEntry](t, record.node.lifelongRecordPath())
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("lifelong record:\n%s\nwant:\n%s", gotJSON, wantJSON)
	}
	if heights := servedHeights(t, record.node, "/hdls/lifelong-record?from=3"); !slices.Equal(heights, heightsFrom(3, 6)) {
		t.Fatalf("the lifelong route served heights %v from height 3, want 3 to 6", heights)
	}
}

// servedHeights asks the node's route at target and returns the heights of the entries served.
func servedHeights(t *testing.T, node *Node, target string) []int64 {
	t.Helper()
	response := httptest.NewRecorder()
	NewServer(node).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("%s answered %d: %s", target, response.Code, response.Body.String())
	}
	var entries []struct{ Height int64 }
	if err := json.Unmarshal(response.Body.Bytes(), &entries); err != nil || entries == nil {
		t.Fatalf("%s did not answer a JSON array: %q", target, response.Body.String())
	}
	heights := []int64{}
	for _, entry := range entries {
		heights = append(heights, entry.Height)
	}
	return heights
}

// heightsFrom is the heights first to last, in order.
func heightsFrom(first, last int64) []int64 {
	var heights []int64
	for height := first; height <= last; height++ {
		heights = append(heights, height)
	}
	return heights
}

// GET /hdls/record?from=H serves the rolling entries for heights H to H + 99 only, from the unit file of
// H and the one after it, and opens no other unit file: a line planted in the unit files before and
// after them, with a height in the range, is never served. A malformed or negative from is a 400.
func TestRecordRouteServesAHundredHeights(t *testing.T) {
	record := newSyntheticRecord(t, 100000) // long enough that no unit is pruned
	record.commit(heightsFrom(1, 250)...)
	record.commit(357, 358)
	for _, check := range []struct {
		target string
		want   []int64
	}{
		{"/hdls/record?from=0", heightsFrom(1, 99)},
		{"/hdls/record?from=1", heightsFrom(1, 100)},
		{"/hdls/record?from=150", heightsFrom(150, 249)},
		{"/hdls/record?from=200", heightsFrom(200, 250)},
		{"/hdls/record?from=260", []int64{357, 358}},
		{"/hdls/record?from=359", nil},
		{"/hdls/record?from=5000", nil},
	} {
		if heights := servedHeights(t, record.node, check.target); !slices.Equal(heights, check.want) {
			t.Fatalf("%s served heights %v, want %v", check.target, heights, check.want)
		}
	}
	// Units 200 and 300 hold 51 entries for heights 200 to 299, short of the 100 a route serves: a line
	// of height 260 planted in the units before and after them is served by a route that opens either.
	for _, unit := range []int64{100, 400} {
		file, err := os.OpenFile(unitPath(record.node.recordDirectory(), unit), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"Height":260}` + "\n"); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if heights := servedHeights(t, record.node, "/hdls/record?from=200"); !slices.Equal(heights, heightsFrom(200, 250)) {
		t.Fatalf("/hdls/record?from=200 served heights %v from other unit files", heights)
	}
	for _, target := range []string{"/hdls/record?from=", "/hdls/record?from=one", "/hdls/record?from=1.5", "/hdls/record?from=-1", "/hdls/lifelong-record?from=x"} {
		response := httptest.NewRecorder()
		NewServer(record.node).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "?from=<height>") {
			t.Fatalf("%s answered %d %q, want 400 with the usage", target, response.Code, response.Body.String())
		}
	}
}

// GET /hdls/record with no from serves the newest unit file's entries, all of them and only them: the
// latest signatures, though the unit holds fewer than 100 heights and starts above its first one. With
// no record yet it serves none.
func TestRecordRouteWithoutFromServesTheNewestUnit(t *testing.T) {
	record := newSyntheticRecord(t, 100000)
	for _, step := range []struct {
		heights []int64
		want    []int64
	}{
		{nil, []int64{}},
		{heightsFrom(1, 99), heightsFrom(1, 99)},
		{heightsFrom(100, 150), heightsFrom(100, 150)},
		{[]int64{357, 358}, []int64{357, 358}}, // after a catch-up
		{heightsFrom(359, 401), []int64{400, 401}},
	} {
		record.commit(step.heights...)
		if heights := servedHeights(t, record.node, "/hdls/record"); !slices.Equal(heights, step.want) {
			t.Fatalf("/hdls/record served heights %v, want %v", heights, step.want)
		}
	}
}

// GET /hdls/lifelong-record with no from serves the newest 100 lifelong entries, or all of them while
// there are fewer; with from=H, the first 100 of height H and above.
func TestLifelongRouteWithoutFromServesTheNewest(t *testing.T) {
	record := newSyntheticRecord(t, 100000)
	evenHeights := func(first, last int64) []int64 {
		var heights []int64
		for height := first; height <= last; height += 2 {
			heights = append(heights, height)
		}
		return heights
	}
	commitUpTo := func(last int64) { // a lifelong entry at each even height
		for height := record.node.state.Height + 1; height <= last; height++ {
			content := ledger.BlockContent{}
			if height%2 == 0 {
				content.ParameterChange = &ledger.ParameterChange{NewParameters: ledger.DefaultParameters(), Sequence: height}
			}
			record.commitBlock(height, content, 100000)
		}
	}
	commitUpTo(20)
	if heights := servedHeights(t, record.node, "/hdls/lifelong-record"); !slices.Equal(heights, evenHeights(2, 20)) {
		t.Fatalf("the lifelong route served heights %v, want every even one up to 20", heights)
	}
	commitUpTo(300)
	for _, check := range []struct {
		target string
		want   []int64
	}{
		{"/hdls/lifelong-record", evenHeights(102, 300)},
		{"/hdls/lifelong-record?from=0", evenHeights(2, 200)},
		{"/hdls/lifelong-record?from=3", evenHeights(4, 202)},
		{"/hdls/lifelong-record?from=250", evenHeights(250, 300)},
		{"/hdls/lifelong-record?from=301", nil},
	} {
		if heights := servedHeights(t, record.node, check.target); !slices.Equal(heights, check.want) {
			t.Fatalf("%s served heights %v, want %v", check.target, heights, check.want)
		}
	}
}

// A line cut short, by a write that failed or one still under way, is skipped when read, whether or not
// it happens to be valid JSON, and the next entry replaces it, so each file holds whole lines again. The
// same holds when the cut line is all the file holds (the file's first write failed), and when the
// lines, and the part cut short, are longer than the node first reads back.
func TestPartialLastLineIsSkipped(t *testing.T) {
	record := newSyntheticRecord(t, 1000)
	governance := func(height int64, signature string) ledger.BlockContent {
		return ledger.BlockContent{ParameterChange: &ledger.ParameterChange{NewParameters: ledger.DefaultParameters(), Sequence: height,
			Votes: []ledger.ParameterVote{{Validator: validatorNames(1)[0], Signature: signature}}}}
	}
	cutShort := func(partials map[string]string) {
		for path, partial := range partials {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString(partial); err != nil {
				t.Fatal(err)
			}
			file.Close()
		}
	}
	serves := func(last int64) {
		t.Helper()
		for _, target := range []string{"/hdls/record", "/hdls/record?from=0", "/hdls/lifelong-record", "/hdls/lifelong-record?from=0"} {
			if heights := servedHeights(t, record.node, target); !slices.Equal(heights, heightsFrom(1, last)) {
				t.Fatalf("%s served heights %v, want 1 to %d", target, heights, last)
			}
		}
	}
	follows := func(height int64) { // the entry at height follows the entry before it, and each file holds whole lines
		t.Helper()
		entries := rollingEntries(t, record.node)
		if last := entries[len(entries)-1]; last.Height != height || last.KeepUntilHeight != height+1000 || len(last.Validators) != 0 {
			t.Fatalf("the entry after the partial line does not follow the entry before it: %+v", last)
		}
		if entries := recordFileEntries[LifelongRecordEntry](t, record.node.lifelongRecordPath()); int64(len(entries)) != height {
			t.Fatalf("the lifelong record holds %d entries, want %d", len(entries), height)
		}
	}
	unit, lifelong := unitPath(record.node.recordDirectory(), 1), record.node.lifelongRecordPath()

	if err := os.MkdirAll(record.node.recordDirectory(), 0o700); err != nil {
		t.Fatal(err)
	}
	cutShort(map[string]string{unit: `{"Height":1,"Previous`, lifelong: `{"Height":1}`}) // the first write of each file
	serves(0)
	for height := int64(1); height <= 3; height++ {
		record.commitBlock(height, governance(height, "signature"), 1000)
	}
	serves(3)
	follows(3)
	cutShort(map[string]string{unit: `{"Height":4,"Previous`, lifelong: `{"Height":4}`})
	serves(3)
	record.commitBlock(4, governance(4, "signature"), 1000)
	serves(4)
	follows(4)

	long := string(bytes.Repeat([]byte("s"), 100<<10)) // longer than the 64 KiB read back first
	record.proposalSignature = long
	record.commitBlock(5, governance(5, long), 1000) // a long entry in both files
	record.proposalSignature = ""
	cutShort(map[string]string{unit: `{"Height":6,"Proposal":"` + long, lifelong: `{"Height":6,"Signers":"` + long})
	serves(5)
	record.commitBlock(6, governance(6, "signature"), 1000)
	serves(6)
	follows(6)
}

// A node that restarts from an older snapshot, because its last snapshot write failed, commits again
// heights it has recorded: each file keeps the entries it has for them and adds none, so every height
// is recorded once and each entry still follows the one before it, in the unit the restart starts in
// and in the next.
func TestRecordHoldsEachHeightOnce(t *testing.T) {
	record := newSyntheticRecord(t, 1000)
	parameterChange := ledger.BlockContent{ParameterChange: &ledger.ParameterChange{NewParameters: ledger.DefaultParameters()}}
	record.commit(1)
	secondBlockHash := record.commitBlock(2, ledger.BlockContent{}, 1000)
	record.commitBlock(3, parameterChange, 1000)
	record.commitBlock(4, parameterChange, 1000)
	record.commit(99, 100, 101)            // a catch-up skipped heights 5 to 98
	record.lastBlockHash = secondBlockHash // the restart, from the snapshot of height 2
	record.commitBlock(3, parameterChange, 1000)
	record.commitBlock(4, parameterChange, 1000)
	record.commit(99, 100, 101, 102)
	entries := rollingEntries(t, record.node)
	var heights []int64
	for i, entry := range entries {
		heights = append(heights, entry.Height)
		if i > 0 && entry.PreviousBlockHash != entries[i-1].Commit.BlockHash {
			t.Fatalf("entry %d does not follow the entry before it", entry.Height)
		}
	}
	if want := []int64{1, 2, 3, 4, 99, 100, 101, 102}; !slices.Equal(heights, want) {
		t.Fatalf("the rolling record holds heights %v, want %v", heights, want)
	}
	if heights := servedHeights(t, record.node, "/hdls/lifelong-record?from=0"); !slices.Equal(heights, []int64{3, 4}) {
		t.Fatalf("the lifelong record holds heights %v, want 3 and 4", heights)
	}
}

// A node without a snapshot path writes no record, here or anywhere, and its routes serve none.
func TestNodeWithoutASnapshotPathRecordsNothing(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	record := newSyntheticRecord(t, 1000)
	record.node.config.SnapshotPath = ""
	record.commitBlock(1, ledger.BlockContent{ParameterChange: &ledger.ParameterChange{NewParameters: ledger.DefaultParameters()}}, 1000)
	if files, err := os.ReadDir(workingDirectory); err != nil || len(files) != 0 {
		t.Fatalf("a node without a snapshot path wrote %v (%v)", files, err)
	}
	// Its routes read no file, not even one planted at the paths its record files would have.
	if err := os.MkdirAll(record.node.recordDirectory(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{unitPath(record.node.recordDirectory(), 1), record.node.lifelongRecordPath()} {
		if err := os.WriteFile(path, []byte(`{"Height":1}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"/hdls/record", "/hdls/record?from=0", "/hdls/lifelong-record", "/hdls/lifelong-record?from=0"} {
		if heights := servedHeights(t, record.node, target); len(heights) != 0 {
			t.Fatalf("%s served heights %v without a record", target, heights)
		}
	}
}
