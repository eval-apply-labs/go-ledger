//go:build unix

package node

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// A backup mount that hangs instead of failing (a hard NFS mount whose server is gone) costs one
// mirror goroutine, not one per block: each held an OS thread in its blocked syscall, until the
// runtime's thread limit aborted the node. A FIFO with no reader at every height's temp path stands
// in for the hung mount, since opening it for writing blocks the same way.
func TestHungBackupMountHoldsOneMirror(t *testing.T) {
	const commits = 60
	backup := filepath.Join(t.TempDir(), "backup.json")
	var fifos []string
	for height := 1; height <= 2*commits; height++ {
		fifo := backup + "." + strconv.Itoa(height) + ".tmp"
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		fifos = append(fifos, fifo)
	}
	t.Cleanup(func() { // open each FIFO's read end, which releases a mirror blocked on it
		for _, fifo := range fifos {
			if reader, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
				reader.Close()
			}
		}
	})
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, time.Second, time.Millisecond))
	config.BackupPath = backup
	node := New(config, NewHTTPTransport(nil))

	before := runtime.NumGoroutine()
	node.Start()
	defer node.Stop()
	keepBusy(t, "validator", node) // MaxBlockIDLookahead blocks, more than the commits
	waitFor(t, 10*time.Second, func() bool { return node.Snapshot().Height > commits }, "the node stopped committing")
	if grown := runtime.NumGoroutine() - before; grown > 20 {
		t.Fatalf("%d more goroutines after %d commits on a hung backup mount: mirrors pile up instead of one at a time", grown, commits)
	}
}
