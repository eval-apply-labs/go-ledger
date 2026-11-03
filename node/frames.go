package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The frames (Phase 4 spec §4.3, §4.4). A frame is a validator set and the height from which it signs:
// the set the chain had between two changes of its names or keys. Its powers may change in between, and
// in dynamic mode a payment that moves a validator's stake changes them, at almost every busy block: a
// frame is the set as it was at its first height. Every block names the set that signs the next one
// (ledger.Block.NextValidatorsHash), so the commit of the last height of a frame is more than ⅔ of the
// set there, the frame's names under its keys, signing off the next one. A node whose trusted frame no
// longer signs the newest checkpoint walks from its frame to the newest, one frame at a time (see
// walkFrames). It holds a frame past changes of its powers, which is the trust of a node that checks a
// checkpoint against its own older view: more than ⅔ of the frame it holds, by that frame's powers, must
// sign (anchorVouches).
//
// The public folder's hdls/frames is the index of that walk: one JSON line per frame the node saw start
// at a height it committed, the oldest first, each line a Frame. A frame is listed from the commit of the
// height before it, whose record entry the walk fetches, so the file lists only the frames whose start
// the node's own record holds: none of the heights a catch-up skips. It keeps the frames that start
// within RetainBlocks of the committed height, as the record keeps its entries, and at most maxFrames of
// them, the newest. It is an index and nothing more: a walker checks every line against the record
// entries, so a wrong line costs it a fetch.

// Frame is one line of hdls/frames.
type Frame struct {
	Height         int64                 // the first height the set signs
	ValidatorsHash string                // ledger.ValidatorsHash of the set, in lowercase hex
	Validators     []consensus.Validator // the set: each member's name, key and power, in name order
}

// framesPath is the path of the frames file in the public folder.
const framesPath = "hdls/frames"

// maxFrames is the most frames hdls/frames lists, and the most a walk takes at one attempt (see
// walkFrames). A frame starts only with a freeze, a key change, or, in dynamic mode, a name that stake
// seats or unseats, so the file seldom gets near it: at most it keeps the newest 100, about 5.6 MB with 21
// banks, which it writes again at each frame.
const maxFrames = 100

// frameLine is a line of hdls/frames as the node holds it: the frame's height and the line's bytes, its
// newline included.
type frameLine struct {
	height int64
	line   []byte
}

// openFrames reads the frames of hdls/frames, which the node wrote before it stopped, at Open: the file
// is their only copy, since the record it indexes is too large to read at each start. A line that does
// not read is dropped, and so is a line above the height in progress: the node was opened from an older
// snapshot than the one it listed that line at, or seeded onto the other side of a fork, and a walker
// would read that line before those the node lists next, and stop there. Then the file is written again
// without them. Without the file the node lists the frames from its next one.
func (node *Node) openFrames() {
	data, _ := os.ReadFile(filepath.Join(node.config.PublicPath, framesPath))
	var kept []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		var frame Frame
		if bytes.HasSuffix(line, []byte("\n")) && json.Unmarshal(line, &frame) == nil && frame.Height <= node.height() {
			node.frames = append(node.frames, frameLine{height: frame.Height, line: line})
			kept = append(kept, line...)
		}
	}
	if !bytes.Equal(kept, data) {
		node.publicFolder.write(framesPath, kept)
	}
}

// noteFrame lists the frame block starts, if it starts one, and writes hdls/frames again: block has just
// committed, its record entry is on disk, node.lastCommitValidators is the set that signed it, and
// node.heightValidators the set that signs the next height. The next set starts a frame when it seats
// other names, or one under another key, than the set that signed block; the line then lists it as it
// is, powers included, as block's NextValidatorsHash names it. The frames that started more than
// RetainBlocks heights ago are dropped, and so are those past the newest maxFrames. Without a public
// folder it does nothing.
func (node *Node) noteFrame(block ledger.Block) {
	sameNameAndKey := func(signer, next consensus.Validator) bool {
		return signer.Name == next.Name && signer.PublicKey == next.PublicKey
	}
	if node.publicFolder == nil || slices.EqualFunc(node.lastCommitValidators.Validators(), node.heightValidators.Validators(), sameNameAndKey) {
		return
	}
	line, _ := jsonAnswer(Frame{Height: block.Height + 1, ValidatorsHash: hex.EncodeToString(block.NextValidatorsHash[:]),
		Validators: node.heightValidators.Validators()}) // names, keys and numbers: it always marshals
	node.frames = append(node.frames, frameLine{height: block.Height + 1, line: line})
	oldest := max(len(node.frames)-maxFrames, 0)
	for oldest < len(node.frames)-1 && node.frames[oldest].height <= block.Height-node.state.Parameters.RetainBlocks {
		oldest++
	}
	node.frames = node.frames[oldest:]
	var content []byte
	for _, frame := range node.frames {
		content = append(content, frame.line...)
	}
	node.publicFolder.write(framesPath, content)
}

// walkFrames is the walk from frame to frame (Phase 4 spec §4.3), for a checkpoint that more than ⅔ of the
// node's trusted frame, the committed view's set, did not sign, but that is sound on its own: its commit
// is a quorum of the set it carries, which hashes to its header. It reads hdls/frames from the public face
// of each other validator of the trusted frame, in turn, if the face's status is ahead of the node: one
// that is not lists no frame after the trusted one, so a checkpoint no walk reaches, which costs a forger
// nothing, does not make the node read every face's frames. From the first frame after the trusted one, it
// takes each frame, up to the checkpoint's height, that the frame it holds signs off (signsOff), until more
// than ⅔ of the frame it holds signed the checkpoint; it returns that frame. A frame that is not signed
// off, or a face's last frame, ends the walk of that face, and the next face is read. Nothing a face
// serves is taken unchecked: a frame is taken only once the commit of the height before it, by more than
// ⅔ of the frame held, names its set as the next one. A walk takes at most maxFrames frames from a face,
// and each file it reads is bounded as a checkpoint is (fetchLines); it keeps the record units of the
// frame it checks, and drops older ones. It reads only immutable config and the committed view, so it is
// safe to call from a goroutine.
func (node *Node) walkFrames(checkpoint Checkpoint, view *committedView) *consensus.ValidatorSet {
	client, checkpointSigners := node.publicAddressClient(), consensus.NewValidatorSet(checkpoint.Validators)
	for _, publicAddress := range node.otherValidatorsPublicAddresses() {
		if heightInProgress, _, err := fetchStatus(client, publicAddress); err != nil || heightInProgress <= view.state.Height+1 {
			continue // a face not ahead of the node lists no frame after its trusted one
		}
		units := map[int64][]RecordEntry{} // the record units this face served, by their first height
		entryAt := func(height int64) (RecordEntry, []consensus.Validator, bool) {
			firstHeight := height - height%recordUnitHeights
			if _, fetched := units[firstHeight]; !fetched {
				units[firstHeight] = fetchLines[RecordEntry](client, fmt.Sprintf("%s/hdls/record/%020d.jsonl", publicAddress, firstHeight))
			}
			return entryOf(units[firstHeight], height)
		}
		trusted, trustedHeight, taken := view.validators, view.state.Height+1, 0
		for _, frame := range fetchLines[Frame](client, publicAddress+"/"+framesPath) {
			if frame.Height <= trustedHeight {
				continue // the frame held, or one before it
			}
			maps.DeleteFunc(units, func(firstHeight int64, _ []RecordEntry) bool { return firstHeight+recordUnitHeights < frame.Height })
			if frame.Height > checkpoint.Commit.Height || taken == maxFrames || !node.signsOff(trusted, frame, entryAt) {
				break
			}
			trusted, trustedHeight, taken = consensus.NewValidatorSet(frame.Validators), frame.Height, taken+1
			if node.anchorVouches(checkpoint.Commit, checkpointSigners, trusted) {
				return trusted
			}
		}
	}
	return nil
}

// signsOff reports whether trusted, the frame the node holds, signs off frame, a later one a public face
// lists (Phase 4 spec §4.3), by the record entry entryAt finds of the height before frame's: it holds the
// commit of its height, which is a quorum of the set that signed it and which names more than ⅔ of
// trusted's power signed under trusted's keys (anchorVouches); its block hash recomputes from the entry
// and that set, so the block is of the entry's height; and the block names frame's set as the one that
// signs next (NextValidatorsHash). The set that signed the entry is the one the last entry at or before it
// in its unit file lists (entryOf), and the block hash covers it. So the frame before frame need not be
// trusted's: a frame more than ⅔ of the trusted one signed off is taken, as a checkpoint is
// (verifyCheckpoint). The entry of frame's own height is not read: no signature the walk checks covers
// it, so a face could make it say anything, and an honest face may not hold it yet.
func (node *Node) signsOff(trusted *consensus.ValidatorSet, frame Frame, entryAt func(height int64) (RecordEntry, []consensus.Validator, bool)) bool {
	before, beforeSigners, beforeFound := entryAt(frame.Height - 1)
	commit, signers := before.Commit, consensus.NewValidatorSet(beforeSigners)
	return beforeFound &&
		ledger.BlockHashOf(before.Height, before.PreviousBlockHash, ledger.ValidatorsHash(beforeSigners), before.NextValidatorsHash, before.SealedDigest) == commit.BlockHash &&
		before.NextValidatorsHash == ledger.ValidatorsHash(frame.Validators) &&
		node.anchorVouches(commit, signers, trusted) && commit.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, signers)
}

// entryOf is the entry of height among the entries of a record unit, oldest first as its file holds them,
// with the set that signed it: the one the last entry at or before it lists (see writeRecordEntry).
func entryOf(unit []RecordEntry, height int64) (RecordEntry, []consensus.Validator, bool) {
	var signers []consensus.Validator
	for _, entry := range unit {
		if len(entry.Validators) > 0 {
			signers = entry.Validators
		}
		if entry.Height == height {
			return entry, signers, true
		}
	}
	return RecordEntry{}, nil, false
}

// fetchLines GETs url and reads its answer as JSON values, one a line, as hdls/frames and a record unit
// hold them: the values before the first one that does not read, within the first MaxCheckpointBytes. A
// request that fails, or an answer other than 200, reads as none.
func fetchLines[Value any](client *http.Client, url string) []Value {
	response, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var values []Value
	decoder := json.NewDecoder(io.LimitReader(response.Body, MaxCheckpointBytes))
	for {
		var value Value
		if decoder.Decode(&value) != nil {
			return values
		}
		values = append(values, value)
	}
}
