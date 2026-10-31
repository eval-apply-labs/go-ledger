package node

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func osThreads() int {
	data, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Threads:") {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Threads:")))
			return n
		}
	}
	return -1
}

// PoC (critic, round 2): saveSnapshot spawns one untracked goroutine per commit to mirror the checkpoint
// to BackupPath. If the backup target hangs (a stuck NFS/CIFS mount: open/write never return), each
// commit leaves one more goroutine — and one more OS thread, since it is parked in a blocking syscall —
// holding a full copy of the checkpoint. A FIFO with no reader stands in for the hung mount here.
func TestPoCCriticBackupGoroutinesAccumulateOnHungMount(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "backup.json")
	const commits = 150
	for h := 1; h <= commits+50; h++ {
		if err := syscall.Mkfifo(backup+"."+strconv.Itoa(h)+".tmp", 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scheme, network := signing.Ed25519{}, "testnet"
	v, _ := signing.Generate(scheme, network)
	nd := New(Config{
		NetworkID: network, Scheme: scheme, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second, MinBlockInterval: time.Millisecond, BackupPath: backup,
		Genesis: []ledger.Account{{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()}},
	}, NewHTTPTransport(nil))
	g0, th0 := runtime.NumGoroutine(), osThreads()
	nd.Start()
	deadline := time.Now().Add(20 * time.Second)
	for nd.Snapshot().Height <= commits && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	h := nd.Snapshot().Height
	time.Sleep(200 * time.Millisecond)
	g1, th1 := runtime.NumGoroutine(), osThreads()
	nd.Stop()
	t.Logf("commits=%d goroutines %d -> %d, OS threads %d -> %d (consensus kept committing: %v)", h-1, g0, g1, th0, th1, h > commits)
	if g1-g0 >= commits && th1-th0 >= commits/2 {
		t.Errorf("LEAK: one blocked backup goroutine (and OS thread) per commit; Go aborts the process at 10000 threads")
	}
	// Unblock the writers so the test process can exit cleanly.
	for i := 1; i <= commits+50; i++ {
		if f, err := os.OpenFile(backup+"."+strconv.Itoa(i)+".tmp", os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			defer f.Close()
		}
	}
}
