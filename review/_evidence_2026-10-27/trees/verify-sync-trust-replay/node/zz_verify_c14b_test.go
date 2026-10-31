package node

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Re-genesis (same id, same keys) on a node whose snapshotPath still holds the OLD chain's file:
// New() verifies it against the NEW genesis (whose validators are the same keys) and resumes the
// old chain instead of starting the new one. With a fresh network id the stale file is refused.
func TestVerifyC14StaleSnapshotResumesOldChain(t *testing.T) {
	scheme := signing.Ed25519{}
	pub, priv, _ := scheme.GenerateKey()
	mk := func(net string) *signing.Signer { return signing.NewSigner(scheme, net, pub, priv) }
	V := mk("village").PublicKeyBase64()
	snap := filepath.Join(t.TempDir(), "snap.json")
	cfg := func(net string, genesis []ledger.Account) Config {
		return Config{NetworkID: net, Scheme: scheme, Signer: mk(net), ValidatorThreshold: rat("100"),
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 20 * time.Millisecond, Genesis: genesis, SnapshotPath: snap}
	}
	oldGenesis := []ledger.Account{{PublicKey: V, Amount: rat("1000"), ValidatorNode: V}, {PublicKey: "carol", Amount: rat("5"), ValidatorNode: V}}
	old := New(cfg("village", oldGenesis), newBus())
	old.Start()
	waitFor(t, 5*time.Second, func() bool { return old.Snapshot().Height >= 6 }, "old chain stalled")
	captured := old.Snapshot()
	old.Stop()
	time.Sleep(50 * time.Millisecond)
	// New genesis = captured accounts with one deliberate migration edit (e.g. a field fix for carol).
	newGenesis := append([]ledger.Account(nil), captured.Accounts...)
	for i := range newGenesis {
		if newGenesis[i].PublicKey == "carol" {
			newGenesis[i].Description = "migrated"
		}
	}
	same := New(cfg("village", newGenesis), newBus())
	fresh := New(cfg("village-2", newGenesis), newBus())
	t.Logf("same id, stale snapshot left in place: start height %d (want 1), carol description %q", same.height, func() string {
		for _, a := range same.accounts {
			if a.PublicKey == "carol" {
				return a.Description
			}
		}
		return ""
	}())
	t.Logf("fresh id: start height %d", fresh.height)
	_ = consensus.Validator{}
}
