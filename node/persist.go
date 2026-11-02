package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// savedSnapshot is what the snapshot file holds: the node's committed checkpoint, the network id it was
// committed on, and the node's pending proofs (spec 6.4), which the one-third cap can hold back for days
// and a release upgrade restarts every node. The checkpoint the node serves never carries them.
type savedSnapshot struct {
	NetworkID     string
	Checkpoint    Checkpoint
	PendingProofs map[string]pendingProof
}

// ofAnotherNetwork is why a snapshot or sign log whose network id is missing or not this node's is
// refused (spec 0.2): it belongs to an earlier network, whose files record none, or to another one.
func ofAnotherNetwork(file, path string) error {
	return fmt.Errorf("%s %s belongs to an earlier network, or another one: it does not record this network's id; a new network starts without the old snapshot and sign log", file, path)
}

// loadSnapshot reads the node's own persisted checkpoint (if configured and present) and adopts it as
// the starting state, with the pending proofs saved beside it, less those the state makes moot (see
// prunePendingProofs). It is called from Open before the run loop exists, so it sets state directly.
// The file must record this node's network id. It is then trusted on self-consistency: this node
// verified every block before writing it, and the genesis-overlap rule a peer's checkpoint must pass
// (verifyCheckpoint) would discard a valid snapshot once the signing set has drifted away from
// genesis. A missing file is a first start. A file that exists but cannot be read, parsed or verified
// is an error, never a silent fallback to genesis: re-running committed heights can roll committed
// history back, so an operator must look (restore the file, or delete it and sync from a trusted peer).
// The snapshot's folder must exist: in one that does not, neither the snapshot nor the sign log beside
// it could be written, so a validator would send no vote or proposal, and log nothing while no node
// commits.
func (node *Node) loadSnapshot() error {
	if node.config.SnapshotPath == "" {
		return nil
	}
	folder := filepath.Dir(node.config.SnapshotPath)
	if folderInformation, err := os.Stat(folder); err != nil || !folderInformation.IsDir() {
		return fmt.Errorf("the snapshot folder %s does not exist: create it before the node starts", folder)
	}
	data, err := os.ReadFile(node.config.SnapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	var saved savedSnapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("snapshot %s: %w", node.config.SnapshotPath, err)
	}
	if saved.NetworkID != node.config.Chain.NetworkID {
		return ofAnotherNetwork("snapshot", node.config.SnapshotPath)
	}
	checkpoint := saved.Checkpoint
	if !node.selfConsistent(checkpoint) {
		return fmt.Errorf("snapshot %s: its accounts are not in name order, its block hash does not recompute from its state and header, or its commit does not verify", node.config.SnapshotPath)
	}
	if checkpoint.Commit.Height+1 > node.height() {
		maps.Copy(node.pendingProofs, saved.PendingProofs)
		node.fastForward(checkpoint) // which prunes the proofs against the state (see reconcilePools)
	}
	return nil
}

// selfConsistent reports whether a checkpoint passes the steps of verifyCheckpoint but the anchor's: it
// is Consistent, and its commit is a valid > 2/3 quorum of the set it carries. It trusts that set, so
// on its own it is enough only for the node's own snapshot (see loadSnapshot); a peer's checkpoint must
// pass verifyCheckpoint.
func (node *Node) selfConsistent(checkpoint Checkpoint) bool {
	return checkpoint.Consistent() &&
		checkpoint.Commit.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, consensus.NewValidatorSet(checkpoint.Validators))
}

// fastForward sets the committed state to a checkpoint, with the hash of its last block (the one its
// commit decided, which the check recomputed from the header) and that block's header. It drops, as a
// commit does, what the node held for the height it leaves: its votes, proposals and blocks, and the
// last proposal it decided. Then, after a catch-up, it writes the public folder; at Open, from
// loadSnapshot, the folder is not open yet, and Open writes it whole. The caller MUST have verified the
// checkpoint.
func (node *Node) fastForward(checkpoint Checkpoint) {
	previousAccounts := node.state.Accounts
	node.state = checkpoint.State
	node.lastBlockHash = checkpoint.Commit.BlockHash
	node.lastBlockHeader = checkpoint.LastBlock
	node.lastCommit = checkpoint.Commit
	node.lastCommitValidators = consensus.NewValidatorSet(checkpoint.Validators)
	node.heightVotes = map[string]consensus.Vote{}     // votes from the abandoned height no longer apply
	node.heightProposals = map[int32]ProposalMessage{} // likewise its proposals
	node.invalidProposals = map[int32]int{}
	node.blocks = map[[32]byte]ledger.Block{}
	node.lastProposal = ProposalMessage{}
	node.publishView()
	node.reconcilePools(previousAccounts)
	node.resolvePeersFromState() // adopt the peer addresses published in the synced state
	node.writePublicFolder(previousAccounts)
}

// saveSnapshot atomically writes the current committed checkpoint to disk. It is called from the
// run loop after each commit and catch-up, so accounts and lastCommit are consistent. The write goes
// to a temp file and is renamed over the target (atomic on the same filesystem). An error is logged
// and returned, never fatal (best-effort durability): the caller keeps the sign log of the height then
// (see commit), since a snapshot left behind with a sign log ahead of it stops the next restart (see
// loadSignLog).
func (node *Node) saveSnapshot() error {
	if node.config.SnapshotPath == "" && node.config.BackupPath == "" {
		return nil
	}
	data, err := json.Marshal(savedSnapshot{NetworkID: node.config.Chain.NetworkID, Checkpoint: node.snapshot().checkpoint(), // the last block's header, never the block (spec 3.6)
		PendingProofs: node.pendingProofs})
	if err != nil {
		return err
	}
	if node.config.SnapshotPath != "" { // local durability, on the loop
		if err = writeAtomic(node.config.SnapshotPath, node.config.SnapshotPath+".tmp", data); err != nil {
			log.Printf("snapshot %s not saved: %v", node.config.SnapshotPath, err)
		}
	}
	if node.config.BackupPath != "" && node.backupBusy.CompareAndSwap(false, true) {
		// Off-machine copy (point it at a mounted share/NAS). Written asynchronously so a slow or
		// hung mount can't stall consensus, and one at a time: while a mirror is in flight, later
		// commits skip theirs, so a mount that hangs instead of failing ties up one goroutine (and the
		// OS thread its blocked syscall holds), not one per block until the runtime's thread limit
		// aborts the node. The goroutine is deliberately untracked: it may outlive Stop(), and the
		// worst it can then do is land one stale-but-valid checkpoint, which loadSnapshot
		// certificate-verifies like any other. Note the rename's atomicity is a local-filesystem
		// guarantee — on NFS/CIFS a mirror can fail (silently, per best-effort); the operator guide
		// says so (backupPath).
		temporaryBackupPath := node.config.BackupPath + "." + strconv.FormatInt(node.lastCommit.Height, 10) + ".tmp"
		go func() {
			defer node.backupBusy.Store(false)
			writeAtomic(node.config.BackupPath, temporaryBackupPath, data)
		}()
	}
	return err
}

// writeAtomic writes data to path via a temp file and a rename (atomic on the same filesystem). It
// fsyncs the temp file before the rename and the directory after, so on power loss the rename can't
// become durable while the data blocks are not — which would silently drop a committed checkpoint
// (and then fail to parse on reload). Any error abandons the write and is returned: a failed snapshot is
// logged and keeps the sign log of its height (see commit), a failed sign log keeps the vote or proposal
// from being released, and a failed backup mirror is ignored (best-effort). A write that fails at or
// before its rename removes its temp file: the backup mirror names a new one at each height, so a full
// or refusing share would otherwise keep one stale file per commit.
func writeAtomic(path, temporaryPath string, data []byte) error {
	temporaryFile, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := temporaryFile.Write(data); err != nil {
		temporaryFile.Close()
		os.Remove(temporaryPath)
		return err
	}
	if err := temporaryFile.Sync(); err != nil { // flush data to disk before the rename exposes it
		temporaryFile.Close()
		os.Remove(temporaryPath)
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	directory, err := os.Open(filepath.Dir(path)) // make the rename itself durable
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// signLog is this validator's write-ahead record of what it signed at the height in progress: its
// own votes and proposals, plus the proposal of the latest round it precommitted a value in, so that
// after a restart it can still decide, or re-propose, the block it is locked on. Each vote is on disk
// before the driver counts it or the node releases it (logVote), each proposal before the node sends
// it (logProposal), and a restart at the same height resumes from the log (Driver.Resume), so a
// reboot mid-height can neither sign a second, conflicting vote or proposal nor forget its lock. It
// lives next to the snapshot, and once the snapshot of its height is on disk, commit replaces it with
// an empty log for the next height. It records the network id it was signed on, and the height up to
// which the node may have signed before it lost an earlier log, so that a restart keeps it (see
// waitForOwnSignatures).
type signLog struct {
	NetworkID         string
	Height            int64
	Votes             []consensus.Vote
	Proposals         []ProposalMessage // its own, one per round
	Locked            *ProposalMessage
	MayHaveSignedUpTo int64 // the node's mayHaveSignedUpTo when it wrote the log
}

func (node *Node) signLogPath() string { return node.config.SnapshotPath + ".votes" }

// heightSignLog is the sign log of the height in progress: the one kept so far, or a new one. A new one
// starts with the votes and proposals of the node's own that it holds for the height: those its peers'
// re-gossip brought back after it came back without its sign log (see waitForOwnSignatures), which a
// later restart, starting at once from this log, must not forget. The node signs only under the key its
// name is seated under (see seatedSigner), which they verified under, so loadSignLog takes them back. A
// node that never lost its log holds none of its own before it logs the first.
func (node *Node) heightSignLog() signLog {
	if node.signLog.Height == node.height() {
		return node.signLog
	}
	next := signLog{Height: node.height()}
	for _, vote := range node.heightVotes {
		if vote.Validator == node.self {
			next.Votes = append(next.Votes, vote)
		}
	}
	for _, proposal := range node.heightProposals {
		if proposal.Proposal.Proposer == node.self {
			next.Proposals = append(next.Proposals, proposal)
		}
	}
	return next
}

// writeSignLog writes next durably, with this node's network id and mayHaveSignedUpTo, and makes it the
// node's sign log. On an error the log stays as it was, on disk and in memory.
func (node *Node) writeSignLog(next signLog) error {
	next.NetworkID, next.MayHaveSignedUpTo = node.config.Chain.NetworkID, node.mayHaveSignedUpTo
	data, err := json.Marshal(next)
	if err == nil {
		err = writeAtomic(node.signLogPath(), node.signLogPath()+".tmp", data)
	}
	if err == nil {
		node.signLog = next
	}
	return err
}

// emptySignLog replaces a sign log the node has left behind, of a height below the one in progress, with
// an empty log for height. Its callers call it once the snapshot of the state the node holds is on disk:
// commit, catchUp and adopt once they have saved it, loadSignLog once Open has read it. So the log of a
// height the node signed in, its proposal's block, payments and nonce included, is not kept on disk while
// the chain idles (spec 3.6, 6.9); in memory the node keeps the last decided proposal until the next
// commit or catch-up (see Node.commit). Without a log, or with one for the height in progress, it does
// nothing.
func (node *Node) emptySignLog(height int64) {
	if node.signLog.Height == 0 || node.signLog.Height >= node.height() {
		return
	}
	if err := node.writeSignLog(signLog{Height: height}); err != nil {
		log.Printf("sign log %s not emptied: %v", node.signLogPath(), err)
	}
}

// logProposal adds a proposal of this node's own to the current height's sign log and writes the log
// durably; the node sends the proposal only once that has succeeded (see process). A proposal already
// logged for its round is not written again, and another block for that round, by block hash, is
// refused, even one that differs only in its nonce: after a restart the driver sends the logged
// proposal again (Driver.Resume), never a different one.
func (node *Node) logProposal(proposal ProposalMessage) error {
	next := node.heightSignLog()
	for _, logged := range next.Proposals {
		if logged.Proposal.Round == proposal.Proposal.Round {
			if logged.Proposal.BlockHash != proposal.Proposal.BlockHash {
				return fmt.Errorf("sign log %s holds another proposal for round %d", node.signLogPath(), proposal.Proposal.Round)
			}
			return nil
		}
	}
	next.Proposals = append(next.Proposals, proposal)
	return node.writeSignLog(next)
}

// logVote is the driver's sign log (see consensus.Driver.SetSignLog): it adds an own vote to the
// current height's log, with the round's proposal when the vote precommits a value, and writes the
// log durably. On an error the driver drops the vote, so a vote that is not on disk is never counted
// or released.
func (node *Node) logVote(vote consensus.Vote, proposal consensus.Proposal) error {
	next := node.heightSignLog()
	next.Votes = append(next.Votes, vote)
	if vote.Type == consensus.Precommit && vote.BlockHash != ([32]byte{}) {
		locked, held := node.heightProposals[vote.Round]
		if !held {
			// Its own proposal, which process signs and sends once the driver returns: sign the same
			// message now, so the lock is never on disk without its block.
			var err error
			locked, err = node.signProposal(vote.Round, proposal.ValidRound, vote.BlockHash)
			if err != nil {
				return err
			}
		}
		next.Locked = &locked
	}
	return node.writeSignLog(next)
}

// loadSignLog reloads the sign log if it is for the height this node restarts at. A log that does not
// record this node's network id is an error, as for the snapshot, whatever its height. From a log of its
// network, whatever its height, the node takes back mayHaveSignedUpTo: a node back without its sign log
// that has since signed at the height it came back at restarts with a log for its height, and must still
// wait at the heights above it that it may have signed at before (see waitForOwnSignatures). A log for a
// later height means the snapshot is behind votes this node already signed: restarting below them would
// forget them, so that is an error, as is a log that cannot be read. A log for an earlier height, which
// a stop between commit's snapshot write and its emptying of the log leaves behind, is emptied at its
// own height, as adopt does: its block, payments and nonce go, and the node still waits before it signs
// (startFirstHeight). A log for the restart height is an error if it does not verify on this network
// (left from a re-created network, or corrupted), or holds a proposal of its own for another height, or
// a proposal whose block is not the one it signed, as for the snapshot: resuming from it would lock the
// node on another chain's block, or have it re-send a block that is not for this height. The node's own
// votes and proposals verify under its own key; the proposal it is locked on, which the round's proposer
// signed, under the key the height's validator set holds for that proposer.
func (node *Node) loadSignLog() error {
	if node.config.SnapshotPath == "" {
		return nil
	}
	data, err := os.ReadFile(node.signLogPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sign log: %w", err)
	}
	var loadedSignLog signLog
	if err := json.Unmarshal(data, &loadedSignLog); err != nil {
		return fmt.Errorf("sign log %s: %w", node.signLogPath(), err)
	}
	if loadedSignLog.NetworkID != node.config.Chain.NetworkID {
		return ofAnotherNetwork("sign log", node.signLogPath())
	}
	node.mayHaveSignedUpTo = loadedSignLog.MayHaveSignedUpTo
	if loadedSignLog.Height > node.height() {
		return fmt.Errorf("sign log %s holds votes for height %d, above the height %d the snapshot restarts at", node.signLogPath(), loadedSignLog.Height, node.height())
	}
	if loadedSignLog.Height < node.height() {
		node.signLog = loadedSignLog
		node.emptySignLog(loadedSignLog.Height)
		return nil
	}
	scheme, networkID, ownKey := node.config.Chain.Scheme, node.config.Chain.NetworkID, node.config.Signer.PublicKeyBase64()
	for _, vote := range loadedSignLog.Votes {
		if !vote.Verify(scheme, networkID, ownKey) {
			return fmt.Errorf("sign log %s holds a vote that does not verify under this node's key on network %q (a re-created network starts without the old snapshot and its sign log)", node.signLogPath(), networkID)
		}
	}
	validators := node.config.Chain.ValidatorRule.SetFor(node.state) // the set of the height the log is for
	signedFor := func(proposal ProposalMessage, publicKey string) bool {
		return proposal.Proposal.Height == loadedSignLog.Height && proposal.Proposal.Verify(scheme, networkID, publicKey) &&
			ledger.BlockHash(proposal.Block) == proposal.Proposal.BlockHash
	}
	verified := loadedSignLog.Locked == nil || signedFor(*loadedSignLog.Locked, validators.PublicKey(loadedSignLog.Locked.Proposal.Proposer))
	for _, proposal := range loadedSignLog.Proposals {
		verified = verified && signedFor(proposal, ownKey)
	}
	if !verified {
		return fmt.Errorf("sign log %s holds a proposal for another height, or one that does not verify on network %q (its own proposals under this node's key, the one it is locked on under its proposer's), or whose block is not the one signed", node.signLogPath(), networkID)
	}
	node.signLog = loadedSignLog
	return nil
}
