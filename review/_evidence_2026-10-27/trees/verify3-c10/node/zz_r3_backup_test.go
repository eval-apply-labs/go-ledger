package node

// R3 verification (group R3-dos) for NEW-6. A FIFO with no reader at each per-height backup temp path
// stands in for a hung (hard-mounted, server gone) NFS share: open(2) blocks in the kernel, as it does
// on such a mount. Run: GOLEDGER_CHAOS_SLACK=4 go test -count=1 -run TestR3Backup -v ./node/

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func r3Threads() int {
	data, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Threads:") {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Threads:")))
			return n
		}
	}
	return -1
}

func r3HungBackupNode(t *testing.T, dir string, fifos int) *Node {
	backup := filepath.Join(dir, "backup.json")
	for h := 1; h <= fifos; h++ {
		if err := syscall.Mkfifo(backup+"."+strconv.Itoa(h)+".tmp", 0o600); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	return New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second, MinBlockInterval: time.Millisecond, BackupPath: backup,
		SnapshotPath: filepath.Join(dir, "snap.json"),
		Genesis:      []ledger.Account{{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()}}}, NewHTTPTransport(nil))
}

func TestR3BackupThreadsPerCommit(t *testing.T) {
	const commits = 300
	nd := r3HungBackupNode(t, t.TempDir(), 100000)
	g0, th0 := runtime.NumGoroutine(), r3Threads()
	nd.Start()
	deadline := time.Now().Add(60 * time.Second)
	for nd.Snapshot().Height <= commits && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	nd.Stop()
	time.Sleep(300 * time.Millisecond)
	h := nd.height // loop stopped: no race
	g1, th1 := runtime.NumGoroutine(), r3Threads()
	t.Logf("%d commits (local snapshot kept being written; consensus not stalled): goroutines %d -> %d, OS threads %d -> %d", h-1, g0, g1, th0, th1)
}

// The process-level consequence, measured with the runtime's thread cap lowered from its default
// (10000) to 250 so it is reached quickly: the node process dies with a fatal runtime error.
func TestR3BackupThreadCapKillsProcess(t *testing.T) {
	if os.Getenv("R3_BACKUP_CHILD") == "1" {
		debug.SetMaxThreads(250)
		nd := r3HungBackupNode(t, os.Getenv("R3_BACKUP_DIR"), 2000)
		nd.Start()
		time.Sleep(120 * time.Second)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestR3BackupThreadCapKillsProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "R3_BACKUP_CHILD=1", "R3_BACKUP_DIR="+dir)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	lines := strings.Split(string(out), "\n")
	first := ""
	for _, l := range lines {
		if strings.Contains(l, "fatal") || strings.Contains(l, "thread") {
			first = l
			break
		}
	}
	t.Logf("child exited after %v: err=%v; first fatal line: %q", time.Since(start).Round(time.Millisecond), err, first)
}
