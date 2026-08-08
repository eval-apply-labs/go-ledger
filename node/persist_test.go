package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A node persists its committed state to disk; a restart (same key, same snapshot path, no
// network at all) reloads it and resumes from the committed height — proving recovery works even
// when there is no peer to sync from (the simultaneous-total-outage case).
func TestNodePersistsAndReloads(t *testing.T) {
	snap := filepath.Join(t.TempDir(), "snapshot.json")

	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	recipient, _ := signing.Generate(signing.Ed25519{}, "testnet")
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: a.PublicKeyBase64(), Power: 1}})
	genesis := []ledger.Account{
		{PublicKey: a.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: a.PublicKeyBase64()},
	}
	cfg := Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis, SnapshotPath: snap,
	}

	// First run: commit a payment, so the persisted state is non-genesis.
	first := New(cfg, NewHTTPTransport(nil))
	pay, _ := ledger.SignPayment(a, ledger.Payment{
		PublicKeyFrom: a.PublicKeyBase64(), PublicKeyTo: recipient.PublicKeyBase64(), Amount: rat("10"), BlockID: 1,
	})
	first.SubmitPayment(pay)
	first.Start()
	waitFor(t, 4*time.Second, func() bool {
		s := first.Snapshot()
		return s.Height >= 3 && balance(s.Accounts, recipient.PublicKeyBase64()).Sign() > 0
	}, "first run did not commit the payment")
	// snapshot file must exist
	waitFor(t, 2*time.Second, func() bool { _, err := os.Stat(snap); return err == nil }, "snapshot not written")
	firstHeight := first.Snapshot().Height
	first.Stop()
	time.Sleep(50 * time.Millisecond) // let the loop settle

	// Restart: brand-new Node from the same config (no bootstrap, no peers). It must load the disk
	// snapshot and come up at (at least) the height the first run reached, holding the payment.
	restart := New(cfg, NewHTTPTransport(nil))
	restart.Start()
	defer restart.Stop()

	s := restart.Snapshot()
	if s.Height < firstHeight {
		t.Fatalf("restart came up at height %d, below persisted %d (snapshot not loaded)", s.Height, firstHeight)
	}
	if balance(s.Accounts, recipient.PublicKeyBase64()).Cmp(rat("9.9")) != 0 {
		t.Fatalf("restart did not recover the committed state (recipient = %s, want 9.9)",
			balance(s.Accounts, recipient.PublicKeyBase64()).FloatString(2))
	}
}
