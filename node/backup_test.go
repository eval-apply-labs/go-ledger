package node

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// A node with both snapshotPath and backupPath must mirror every committed checkpoint to both, so a
// second (off-machine) copy exists — the backup is written asynchronously, so we wait for it.
func TestBackupMirrorsSnapshot(t *testing.T) {
	dir := t.TempDir()
	snap, backup := dir+"/snap.json", dir+"/backup.json"

	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	A := a.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: A, Power: 1}}) // quorum 1 → commits alone
	genesis := []ledger.Account{{PublicKey: A, Name: "founder", Amount: rat("1000"), ValidatorNode: A}}

	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis, SnapshotPath: snap, BackupPath: backup,
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()

	committed := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var cp Checkpoint
		return json.Unmarshal(data, &cp) == nil && cp.Commit.Height >= 1
	}
	waitFor(t, 3*time.Second, func() bool { return committed(snap) && committed(backup) },
		"snapshot and off-machine backup were not both written")
}
