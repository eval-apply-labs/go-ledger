package main

// PoCs for key-file handling (ui/keyfile.go). Each asserts the SECURE behaviour, so it FAILS on the
// current code.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The creation-time warning says "Set GOLEDGER_WALLET_PASSPHRASE to seal it", and the README says to
// set it "to keep the key sealed". But loadOrCreateKey skips the plaintext->sealed upgrade whenever
// that variable is set (keyfile.go:90), so following the advice leaves the key in plaintext, with no
// further warning on any later run.
func TestPoCEnvPassphraseDoesNotSealExistingPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.json")
	t.Setenv(passphraseEnv, "")
	if _, _, err := loadOrCreateKey(path); err != nil { // service first run: plaintext + warning
		t.Fatal(err)
	}
	t.Setenv(passphraseEnv, "operator-followed-the-warning")
	if _, _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	var k keyFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &k); err != nil {
		t.Fatal(err)
	}
	if k.PrivateKey != "" || k.Encrypted == nil {
		t.Errorf("after setting %s as instructed, the wallet file still holds the private key in PLAINTEXT", passphraseEnv)
	}
}

// The publicKey field is neither GCM associated data nor checked against the decrypted private key.
// A file whose publicKey was altered (e.g. a tampered backup — the manual calls a sealed backup "safe
// to store anywhere") still unlocks with the right passphrase, and the wallet then shows and uses
// the substituted key as "your account".
func TestPoCPublicKeyNotBoundToPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.json")
	t.Setenv(passphraseEnv, "correct horse")
	if _, _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	var k keyFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &k); err != nil {
		t.Fatal(err)
	}
	other := testNodeSigner(t).PublicKeyBase64()
	k.PublicKey = other
	data, _ = json.Marshal(k)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := LoadOrCreateWallet(path, "testnet", "http://127.0.0.1:0", 3)
	if err == nil {
		t.Errorf("a key file whose publicKey does not match its private key loaded without error; wallet identity is now %s…", first(w.PublicKey(), 12))
	}
}
