package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// loadSnapshot reads a persisted checkpoint from disk (if configured and present), verifies its
// certificate against the validator set, and adopts it as the starting state. It is called from
// New before the run loop exists, so it sets state directly. A missing, unreadable, or invalid
// file is ignored — the node simply starts at genesis (and can still catch up via bootstrap sync).
func (n *Node) loadSnapshot() {
	if n.cfg.SnapshotPath == "" {
		return
	}
	data, err := os.ReadFile(n.cfg.SnapshotPath)
	if err != nil {
		return
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return
	}
	if cp.Commit.Height+1 <= n.height {
		return
	}
	if !n.verifyCheckpoint(cp, n.accounts) { // anchor = genesis at this point (set in New)
		return
	}
	n.fastForward(cp)
}

// saveSnapshot atomically writes the current committed checkpoint to disk. It is called from the
// run loop after each commit, so accounts and lastCommit are consistent. The write goes to a temp
// file and is renamed over the target (atomic on the same filesystem); errors are ignored
// (best-effort durability). For a large ledger this should move off the loop / become incremental.
func (n *Node) saveSnapshot() {
	if n.cfg.SnapshotPath == "" && n.cfg.BackupPath == "" {
		return
	}
	var vals []consensus.Validator
	if n.lastCommitVals != nil {
		vals = n.lastCommitVals.Validators()
	}
	data, err := json.Marshal(Checkpoint{Accounts: n.accounts, Parameters: n.parameters, GovernanceSequence: n.governanceSequence, Commit: n.lastCommit, Validators: vals})
	if err != nil {
		return
	}
	if n.cfg.SnapshotPath != "" {
		writeAtomic(n.cfg.SnapshotPath, n.cfg.SnapshotPath+".tmp", data) // local durability, on the loop
	}
	if n.cfg.BackupPath != "" {
		// Off-machine copy (point it at a mounted share/NAS). Written asynchronously so a slow or
		// hung mount can't stall consensus, with a height-tagged temp file so overlapping commits
		// don't clash on the same temp path. The goroutine is deliberately untracked: it may outlive
		// Stop(), and the worst it can then do is land one stale-but-valid checkpoint, which
		// loadSnapshot certificate-verifies like any other. Note the rename's atomicity is a
		// local-filesystem guarantee — on NFS/CIFS a mirror can fail (silently, per best-effort);
		// the operator guide tells operators to prefer a local disk for this reason.
		tmp := n.cfg.BackupPath + "." + strconv.FormatInt(n.lastCommit.Height, 10) + ".tmp"
		go writeAtomic(n.cfg.BackupPath, tmp, data)
	}
}

// writeAtomic writes data to path via a temp file and a rename (atomic on the same filesystem). It
// fsyncs the temp file before the rename and the directory after, so on power loss the rename can't
// become durable while the data blocks are not — which would silently drop a committed checkpoint
// (and then fail to parse on reload). Best-effort: any error abandons the write.
func writeAtomic(path, tmp string, data []byte) {
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return
	}
	if err := f.Sync(); err != nil { // flush data to disk before the rename exposes it
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		return
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil { // make the rename itself durable
		_ = dir.Sync()
		_ = dir.Close()
	}
}
