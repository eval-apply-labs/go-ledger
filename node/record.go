package node

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The signature record (spec 6.8) keeps, beside the snapshot, who signed each committed block. A rolling
// entry per block holds its proposal, its commit and the set that signed them, from which anyone
// recomputes the block hash and checks the chain; it is kept at least RetainBlocks heights, and deleted
// a unit file at a time. A lifelong entry, never deleted, holds each governance action (a parameter
// change or the ⅔ name action) and each piece of evidence a block carries, with the name, key and power
// of every name that signed it. The record never holds a block's payments, account updates, holder
// requests (freezes included), nonce or state digest. A node records only the blocks it commits, each
// height once, so the heights a catch-up skips are missing, and a failed write is logged, never fatal.
//
// Files, beside the snapshot, named by network (the first 16 hex digits of the genesis hash), so a node
// of another genesis on the same snapshot path neither reads nor extends them:
// <snapshot path>.<network>.record/<first height, 20 digits>.jsonl, one rolling entry per line, a unit
// file for each aligned run of 100 heights; and <snapshot path>.<network>.lifelong.jsonl, one lifelong
// entry per line.

const (
	recordUnitHeights = 100 // the heights a rolling unit file covers: the unit of height h starts at h - (h mod 100)
	maxRecordEntries  = 100 // the most entries a record route serves at once
)

// RecordEntry is the rolling entry of one committed block.
type RecordEntry struct {
	Height             int64
	PreviousBlockHash  [32]byte
	NextValidatorsHash [32]byte                 // the hash of the set that signs the next height, as the block names it
	SealedDigest       [32]byte                 // mixes the nonce; never the bare state digest
	Proposal           consensus.SignedProposal // the proposal of the round that decided it
	Commit             consensus.Commit
	// Validators is the signing set, written when its ValidatorsHash differs from the entry before's
	// and in the first entry of each unit file; otherwise empty.
	Validators      []consensus.Validator
	KeepUntilHeight int64
}

// LifelongRecordEntry is the lifelong entry of a block that carries a governance action or evidence.
type LifelongRecordEntry struct {
	Height           int64
	BlockHash        [32]byte
	ParameterChange  *ledger.ParameterChange
	Reassignment     *ledger.Reassignment
	VoteEvidence     []consensus.VoteEvidence
	ProposalEvidence []consensus.ProposalEvidence
	// Signers holds the name, key and power, at that height, of every name whose signature the
	// entry holds (power 0 for a non-validator). They can be checked against the rolling record
	// while it covers that height; after that, only by comparing nodes' copies.
	Signers []consensus.Validator
}

// recordPrefix is the snapshot path and the network: the first 16 hex digits of the genesis hash.
func (node *Node) recordPrefix() string {
	return fmt.Sprintf("%s.%x", node.config.SnapshotPath, node.config.Chain.GenesisHash[:8])
}
func (node *Node) recordDirectory() string    { return node.recordPrefix() + ".record" }
func (node *Node) lifelongRecordPath() string { return node.recordPrefix() + ".lifelong.jsonl" }

// unitPath is the path of the rolling unit file that holds height, named by the unit's first height.
func unitPath(directory string, height int64) string {
	return filepath.Join(directory, fmt.Sprintf("%020d.jsonl", height-height%recordUnitHeights))
}

// recordCommittedBlock records block, which commit has just committed on previousState (whose
// RetainBlocks and keys hold for it), with the proposal of the round that decided it, its commit and
// signingSet, the set that signed both. Then it deletes the rolling unit files past their retention. A
// node without a snapshot path records nothing.
func (node *Node) recordCommittedBlock(previousState ledger.LedgerState, block ledger.Block, proposal consensus.SignedProposal,
	commit consensus.Commit, signingSet *consensus.ValidatorSet) {
	if node.config.SnapshotPath == "" {
		return
	}
	if err := node.writeRecordEntry(previousState, block, proposal, commit, signingSet); err != nil {
		log.Printf("record of height %d not written: %v", block.Height, err)
	}
	if entry := lifelongEntryOf(previousState, block, commit, signingSet); entry != nil {
		_, end, err := placeOfNextLine(node.lifelongRecordPath(), block.Height)
		if err == nil {
			err = writeLine(node.lifelongRecordPath(), end, entry)
		}
		if err != nil {
			log.Printf("lifelong record of height %d not written: %v", block.Height, err)
		}
	}
	if err := node.pruneRecord(block.Height); err != nil {
		log.Printf("record %s not pruned: %v", node.recordDirectory(), err)
	}
}

// writeRecordEntry adds block's rolling entry to the unit file of the block's height.
func (node *Node) writeRecordEntry(previousState ledger.LedgerState, block ledger.Block, proposal consensus.SignedProposal,
	commit consensus.Commit, signingSet *consensus.ValidatorSet) error {
	if err := os.MkdirAll(node.recordDirectory(), 0o700); err != nil {
		return err
	}
	path := unitPath(node.recordDirectory(), block.Height)
	lastLine, end, err := placeOfNextLine(path, block.Height)
	if err != nil {
		return err
	}
	validators := signingSet.Validators()
	entry := RecordEntry{
		Height:             block.Height,
		PreviousBlockHash:  block.PreviousBlockHash,
		NextValidatorsHash: block.NextValidatorsHash,
		SealedDigest:       ledger.SealedDigestOf(block.Nonce, block.StateDigest, ledger.ContentHash(block.Content)),
		Proposal:           proposal,
		Commit:             commit,
		Validators:         validators,
		KeepUntilHeight:    block.Height + previousState.Parameters.RetainBlocks,
	}
	var before RecordEntry
	if lastLine != nil && json.Unmarshal(lastLine, &before) == nil { // not the first entry of the unit
		entry.KeepUntilHeight = max(entry.KeepUntilHeight, before.KeepUntilHeight)
		// The block hash of the entry before covers the set that signed it: recomputed with this set,
		// it comes out the same only if the set is the same.
		if ledger.BlockHashOf(before.Height, before.PreviousBlockHash, ledger.ValidatorsHash(validators), before.NextValidatorsHash, before.SealedDigest) == before.Commit.BlockHash {
			entry.Validators = nil
		}
	}
	return writeLine(path, end, entry)
}

// lifelongEntryOf is block's lifelong entry, or nil if the block carries no governance action (a
// parameter change or the ⅔ name action) and no evidence. Holder requests, freezes included, are never
// recorded. Each signer's key is its key before the block, the one its signature was checked under.
func lifelongEntryOf(previousState ledger.LedgerState, block ledger.Block, commit consensus.Commit, signingSet *consensus.ValidatorSet) *LifelongRecordEntry {
	content := block.Content
	if content.ParameterChange == nil && content.Reassignment == nil && len(content.VoteEvidence) == 0 && len(content.ProposalEvidence) == 0 {
		return nil
	}
	signerNames := map[string]bool{}
	if content.ParameterChange != nil {
		for _, vote := range content.ParameterChange.Votes {
			signerNames[vote.Validator] = true
		}
	}
	if content.Reassignment != nil {
		for _, vote := range content.Reassignment.Votes {
			signerNames[vote.Validator] = true
		}
	}
	for _, evidence := range content.VoteEvidence {
		signerNames[evidence.Name] = true
	}
	for _, evidence := range content.ProposalEvidence {
		signerNames[evidence.Name] = true
	}
	entry := &LifelongRecordEntry{Height: block.Height, BlockHash: commit.BlockHash, ParameterChange: content.ParameterChange,
		Reassignment: content.Reassignment, VoteEvidence: content.VoteEvidence, ProposalEvidence: content.ProposalEvidence}
	for _, name := range slices.Sorted(maps.Keys(signerNames)) {
		account, _ := ledger.FindAccount(previousState.Accounts, name)
		entry.Signers = append(entry.Signers, consensus.Validator{Name: name, PublicKey: account.PublicKey, Power: signingSet.Power(name)})
	}
	return entry
}

// unitEnd is what pruning reads of a rolling unit file: its last entry's height and KeepUntilHeight.
type unitEnd struct{ Height, KeepUntilHeight int64 }

// dueAt is whether the unit file that ends so is deleted at height, under the current retainBlocks.
func (end unitEnd) dueAt(height, retainBlocks int64) bool {
	return height > end.KeepUntilHeight && height > end.Height+retainBlocks
}

// pruneRecord deletes the oldest rolling unit files once height has passed both their last entry's
// KeepUntilHeight and their last height plus the current RetainBlocks. So a longer retention applies at
// once, to old units too, and a cut only to the entries written after it. The directory holds a file
// per 100 heights retained, so it is listed only once the oldest unit file, as it ended when last read,
// is due: the entries added to it since only end it later. A new node starts from the zero end, which
// is due no later than any file's.
func (node *Node) pruneRecord(height int64) error {
	retainBlocks := node.state.Parameters.RetainBlocks
	if !node.oldestUnitEnd.dueAt(height, retainBlocks) {
		return nil
	}
	directory := node.recordDirectory()
	firstHeights, err := unitFirstHeights(directory)
	if err != nil {
		return err
	}
	for _, firstHeight := range firstHeights {
		path := unitPath(directory, firstHeight)
		lastLine, _, err := lastCompleteLine(path)
		if err != nil {
			return err
		}
		var end unitEnd // a unit file with no complete line keeps nothing
		if lastLine != nil {
			if err := json.Unmarshal(lastLine, &end); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
		if !end.dueAt(height, retainBlocks) {
			node.oldestUnitEnd = end
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// unitFirstHeights lists the first heights of the rolling unit files, oldest first: their names, 20
// digits each, sort as the heights do.
func unitFirstHeights(directory string) ([]int64, error) {
	files, err := os.ReadDir(directory)
	var firstHeights []int64
	for _, file := range files {
		if firstHeight, parseErr := strconv.ParseInt(strings.TrimSuffix(file.Name(), ".jsonl"), 10, 64); parseErr == nil {
			firstHeights = append(firstHeights, firstHeight)
		}
	}
	return firstHeights, err
}

// lastCompleteLine reads the record file at path from its end. It returns the file's last complete line,
// nil if there is none or no file, and the offset just after it, where the next line goes: whatever
// follows is a partial line, left by a write that failed, and the next line replaces it.
func lastCompleteLine(path string) ([]byte, int64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	information, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := information.Size()
	for tailSize := int64(64 << 10); ; tailSize *= 2 {
		start := max(size-tailSize, 0)
		tail := make([]byte, size-start)
		if _, err := file.ReadAt(tail, start); err != nil {
			return nil, 0, err
		}
		lineEnd := bytes.LastIndexByte(tail, '\n')
		if lineEnd < 0 && start == 0 {
			return nil, 0, nil
		}
		if lineEnd >= 0 {
			lineStart := bytes.LastIndexByte(tail[:lineEnd], '\n') + 1
			if lineStart > 0 || start == 0 {
				return tail[lineStart:lineEnd], start + int64(lineEnd) + 1, nil
			}
		}
		// The last line begins before the part read: read twice as much.
	}
}

// placeOfNextLine is lastCompleteLine for the line of height, which must be above every height the record
// file at path holds. A node that restarts from an older snapshot, because its last snapshot write
// failed, commits heights it has already recorded: the file keeps the entries it has for them. A height
// is only ever written to its own unit file, so each height is recorded once.
func placeOfNextLine(path string, height int64) ([]byte, int64, error) {
	lastLine, end, err := lastCompleteLine(path)
	var last struct{ Height int64 }
	if err == nil && lastLine != nil && json.Unmarshal(lastLine, &last) == nil && last.Height >= height {
		err = fmt.Errorf("%s already reaches height %d", path, last.Height)
	}
	return lastLine, end, err
}

// writeLine writes value's JSON and a newline at offset in the record file at path, creating the file if
// need be, and cuts off whatever followed.
func writeLine(path string, offset int64, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	err = file.Truncate(offset)
	if err == nil {
		_, err = file.WriteAt(append(data, '\n'), offset)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// readRecord reads the record files at paths, in order, and returns the first 100 of their entries of
// heights from to last, as written, or with newest the last 100. A node without a snapshot path has no
// record. A file that is gone (pruned meanwhile) gives none, and a partial last line, still being
// written or left by a write that failed, is skipped.
func (node *Node) readRecord(paths []string, from, last int64, newest bool) []json.RawMessage {
	entries := []json.RawMessage{}
	if node.config.SnapshotPath == "" {
		return entries
	}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		reader := bufio.NewReader(file)
		for newest || len(entries) < maxRecordEntries {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				break // the end of the file, or a partial last line
			}
			var entry struct{ Height int64 }
			if json.Unmarshal(line, &entry) == nil && from <= entry.Height && entry.Height <= last {
				entries = append(entries, line[:len(line)-1])
			}
			if len(entries) > maxRecordEntries { // only with newest: drop the oldest
				entries = entries[1:]
			}
		}
		file.Close()
	}
	return entries
}
