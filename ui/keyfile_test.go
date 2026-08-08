package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Published PBKDF2-HMAC-SHA256 test vectors (the RFC 6070 inputs re-run under SHA-256, as used by
// RFC 7914 §11 and NIST CAVP) — the hand-rolled KDF must match them exactly.
func TestPBKDF2SHA256Vectors(t *testing.T) {
	for _, vector := range []struct {
		password, salt string
		iterations     int
		want           string
	}{
		{"password", "salt", 1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"password", "salt", 4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
		{"passwordPASSWORDpassword", "saltSALTsaltSALTsaltSALTsaltSALTsalt", 4096,
			"348c89dbcbd32b2f32d814b8116e84cf2b17347ebc1800181c4e2a1fb8dd53e1"},
	} {
		got := pbkdf2SHA256([]byte(vector.password), []byte(vector.salt), vector.iterations, len(vector.want)/2)
		if hex.EncodeToString(got) != vector.want {
			t.Errorf("pbkdf2(%q,%q,%d) = %x, want %s", vector.password, vector.salt, vector.iterations, got, vector.want)
		}
	}
}

// Seal/open round-trip: the right passphrase recovers the key, a wrong one (or a tampered
// ciphertext) is rejected by the AEAD, never returning garbage key bytes.
func TestSealedKeyRoundTrip(t *testing.T) {
	priv := []byte("this-stands-in-for-a-private-key")
	sealed, err := sealPrivateKey(priv, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealed.open("correct horse")
	if err != nil || !bytes.Equal(got, priv) {
		t.Fatalf("round-trip failed: %v", err)
	}
	if _, err := sealed.open("wrong horse"); err == nil {
		t.Fatal("a wrong passphrase must not unlock the key")
	}
	tampered := *sealed
	raw := []byte(tampered.Ciphertext)
	raw[0] ^= 1
	tampered.Ciphertext = string(raw)
	if _, err := tampered.open("correct horse"); err == nil {
		t.Fatal("a tampered ciphertext must not unlock")
	}
}

// The file round-trip in both forms, driven through the env passphrase (no terminal in tests): a
// fresh wallet is created sealed, reloads with the passphrase, and never touches plaintext; the
// empty passphrase keeps the ssh-keygen-style plaintext form, and legacy files load unchanged.
func TestKeyFileRoundTrip(t *testing.T) {
	dir := t.TempDir()

	t.Setenv(passphraseEnv, "hunter2")
	path := filepath.Join(dir, "wallet.json")
	pub, priv, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var k keyFile
	if err := json.Unmarshal(data, &k); err != nil {
		t.Fatal(err)
	}
	if k.Encrypted == nil || k.PrivateKey != "" {
		t.Fatal("with a passphrase set, the key must be stored sealed only")
	}
	if bytes.Contains(data, priv) {
		t.Fatal("the raw private key must not appear in the sealed file")
	}
	pub2, priv2, err := loadOrCreateKey(path)
	if err != nil || !bytes.Equal(pub, pub2) || !bytes.Equal(priv, priv2) {
		t.Fatalf("reloading the sealed wallet failed: %v", err)
	}
	t.Setenv(passphraseEnv, "wrong")
	if _, _, err := loadOrCreateKey(path); err == nil {
		t.Fatal("a wrong passphrase must fail, not silently regenerate the wallet")
	}

	// Legacy/empty-passphrase plaintext form still works (created non-interactively with no env).
	t.Setenv(passphraseEnv, "")
	plainPath := filepath.Join(dir, "plain.json")
	pub, priv, err = loadOrCreateKey(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2, err = loadOrCreateKey(plainPath)
	if err != nil || !bytes.Equal(pub, pub2) || !bytes.Equal(priv, priv2) {
		t.Fatalf("reloading the plaintext wallet failed: %v", err)
	}
}
