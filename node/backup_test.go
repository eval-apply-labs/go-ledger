package node

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// A node with both snapshotPath and backupPath must mirror every committed checkpoint to both, so a
// second (off-machine) copy exists — the backup is written asynchronously, so we wait for it.
func TestBackupMirrorsSnapshot(t *testing.T) {
	directory := t.TempDir()
	snapshotPath, backup := directory+"/snap.json", directory+"/backup.json"

	config := testConfig(staticChain("founder"), "founder", testGenesis([]ledger.Account{testAccount("founder", "1000")}, 5*time.Second, 20*time.Millisecond)) // quorum 1 → commits alone
	config.SnapshotPath, config.BackupPath = snapshotPath, backup
	node := New(config, NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()

	committed := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var saved savedSnapshot
		return json.Unmarshal(data, &saved) == nil && saved.Checkpoint.Commit.Height >= 1
	}
	waitFor(t, 3*time.Second, func() bool { return committed(snapshotPath) && committed(backup) },
		"snapshot and off-machine backup were not both written")
}
