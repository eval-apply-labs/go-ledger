package main

import (
	"path/filepath"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// looksLikePublicKey classifies a recipient string. It must tolerate surrounding whitespace (a
// pasted key often carries a trailing newline) and reject anything that is not exactly the base64 of
// a 32-byte key, so a real key is never misfiled as a name (which would sign a no-op payment).
func TestLooksLikePublicKeyTrimsAndValidates(t *testing.T) {
	s, _ := signing.Generate(signing.Ed25519{}, "testnet")
	key := s.PublicKeyBase64()
	if !looksLikePublicKey(key) {
		t.Fatal("a real public key must be recognised as a key")
	}
	if !looksLikePublicKey("  " + key + "\n") {
		t.Fatal("surrounding whitespace on a pasted key must be tolerated")
	}
	if looksLikePublicKey("alice") {
		t.Fatal("a short registered name must not be mistaken for a key")
	}
}

// The same-block payment guard (lastPaymentBlockID) is persisted, so a wallet restarted within the
// lookahead window resumes its counter instead of resetting to zero and reissuing a block a still-
// pending pre-restart payment already named (which the ledger would answer with a balance-destroying
// overdraw ban). The node is unreachable here, so height() returns 0 and blockID is driven purely by
// the persisted counter.
func TestPaymentBlockIDGuardSurvivesReload(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	nodeURL := "http://127.0.0.1:0" // unreachable → height() == 0

	w1, err := LoadOrCreateWallet(keyfile, "testnet", nodeURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	for i := 0; i < 5; i++ {
		id := w1.nextPaymentBlockID()
		if id <= last {
			t.Fatalf("block ids must strictly increase within a session: %d then %d", last, id)
		}
		last = id
	}

	// Reload the same wallet file (a restart): the guard must not hand out a block id <= the last one.
	w2, err := LoadOrCreateWallet(keyfile, "testnet", nodeURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	if resumed := w2.nextPaymentBlockID(); resumed <= last {
		t.Fatalf("a reloaded wallet reissued block %d (<= last used %d) — the guard did not survive restart", resumed, last)
	}
}
