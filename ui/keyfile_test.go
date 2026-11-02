package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// A wallet sealed before the KDF moved to the standard library's crypto/pbkdf2 must still open. This
// fixture was sealed by the hand-written PBKDF2-HMAC-SHA256 the wallet used until then, at 1000
// iterations so the test is fast (a key file records its own count).
func TestWalletSealedBeforeStdlibKDFStillOpens(t *testing.T) {
	var sealed encryptedKey
	fixture := `{"kdf":"pbkdf2-hmac-sha256","iterations":1000,"salt":"Z28tbGVkZ2VyLXNhbHQxNg==","nonce":"bm9uY2UtMTJieXRl","ciphertext":"tSh8iOY3KoYXJc0XxL4ER2v/aldVki/JXwU4pOcFYxuRIILptdD/84JEesSlRlP9"}`
	if err := json.Unmarshal([]byte(fixture), &sealed); err != nil {
		t.Fatal(err)
	}
	if opened, err := sealed.open("correct horse"); err != nil || string(opened) != "this-stands-in-for-a-private-key" {
		t.Fatalf("a wallet sealed by the earlier KDF no longer opens: %q, %v", opened, err)
	}
}

// Seal/open round-trip: the right passphrase recovers the key, a wrong one (or a tampered
// ciphertext) is rejected by the AEAD, never returning garbage key bytes.
func TestSealedKeyRoundTrip(t *testing.T) {
	privateKey := []byte("this-stands-in-for-a-private-key")
	sealed, err := sealPrivateKey(privateKey, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := sealed.open("correct horse")
	if err != nil || !bytes.Equal(opened, privateKey) {
		t.Fatalf("round-trip failed: %v", err)
	}
	if _, err := sealed.open("wrong horse"); err == nil {
		t.Fatal("a wrong passphrase must not unlock the key")
	}
	tampered := *sealed
	encodedCiphertext := []byte(tampered.Ciphertext)
	encodedCiphertext[0] ^= 1
	tampered.Ciphertext = string(encodedCiphertext)
	if _, err := tampered.open("correct horse"); err == nil {
		t.Fatal("a tampered ciphertext must not unlock")
	}
}

// The publicKey field is outside the seal, so the wallet's account comes from the private key: a
// sealed backup whose publicKey was swapped (to misdirect payments meant for "your account") still
// unlocks with the right passphrase, but must not load.
func TestKeyFileRefusesForeignPublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.json")
	t.Setenv(passphraseEnvironmentVariable, "correct horse")
	if _, _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	var storedKey keyFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &storedKey); err != nil {
		t.Fatal(err)
	}
	storedKey.PublicKey = testNodeSigner(t).PublicKeyBase64()
	data, _ = json.Marshal(storedKey)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if wallet, err := LoadOrCreateWallet(path, "testnet", "http://127.0.0.1:0", 3); err == nil {
		t.Fatalf("a key file whose publicKey is not its private key's loaded, as account %s", wallet.PublicKey())
	}
}

// A plaintext wallet is sealed as soon as a passphrase is available. Setting the passphrase variable,
// as the plaintext warning advises, used to leave an existing file in plaintext and silence the
// warning. The re-seal replaces the file whole (mode 0600, no temp file left) and leaves the .state
// sidecar alone; with no passphrase the warning repeats on every start.
func TestPlaintextKeyIsSealedOncePassphraseIsSet(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "wallet.json")
	t.Setenv(passphraseEnvironmentVariable, "")
	publicKey, privateKey, err := loadOrCreateKey(path) // a service's first run: plaintext, with a warning
	if err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"lastPaymentBlockID":7}`)
	if err := os.WriteFile(path+".state", state, 0o600); err != nil {
		t.Fatal(err)
	}
	if warning := printedToStandardError(t, func() { _, _, _ = loadOrCreateKey(path) }); !strings.Contains(warning, "UNENCRYPTED") {
		t.Errorf("reloading a plaintext key with no passphrase available printed no warning")
	}

	t.Setenv(passphraseEnvironmentVariable, "operator-followed-the-warning")
	if _, _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	var storedKey keyFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &storedKey); err != nil {
		t.Fatal(err)
	}
	if storedKey.PrivateKey != "" || storedKey.Encrypted == nil {
		t.Fatalf("with %s set, the existing wallet file still holds the private key in plaintext", passphraseEnvironmentVariable)
	}
	fileInformation, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInformation.Mode().Perm() != 0o600 {
		t.Errorf("sealed wallet file mode %v, want 0600", fileInformation.Mode().Perm())
	}
	if stateAfterSeal, _ := os.ReadFile(path + ".state"); !bytes.Equal(stateAfterSeal, state) {
		t.Errorf("the re-seal changed the .state sidecar: %q", stateAfterSeal)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 2 {
		t.Errorf("the re-seal left %d files behind, want the wallet and its .state", len(entries))
	}
	reloadedPublicKey, reloadedPrivateKey, err := loadOrCreateKey(path)
	if err != nil || !bytes.Equal(publicKey, reloadedPublicKey) || !bytes.Equal(privateKey, reloadedPrivateKey) {
		t.Fatalf("the sealed wallet does not reopen to the same key: %v", err)
	}
}

// A wallet.json may be a symlink, into an encrypted or backed-up directory say. Sealing it must seal
// the file the link points to: the re-seal used to rename its temp file over the link, which became
// a sealed copy while the file it pointed to kept the key in plaintext.
func TestPlaintextKeyIsSealedThroughASymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "vault", "wallet.json")
	if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(passphraseEnvironmentVariable, "")
	if _, _, err := loadOrCreateKey(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "wallet.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	t.Setenv(passphraseEnvironmentVariable, "operator-followed-the-warning")
	if _, _, err := loadOrCreateKey(link); err != nil {
		t.Fatal(err)
	}
	var storedKey keyFile
	data, _ := os.ReadFile(target)
	if err := json.Unmarshal(data, &storedKey); err != nil || storedKey.PrivateKey != "" || storedKey.Encrypted == nil {
		t.Fatalf("the file the link points to still holds the private key in plaintext (%v)", err)
	}
	if linkInformation, err := os.Lstat(link); err != nil || linkInformation.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the re-seal replaced the symlink (%v)", err)
	}
}

// printedToStandardError runs action and returns what it printed to os.Stderr.
func printedToStandardError(t *testing.T, action func()) string {
	t.Helper()
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = pipeWriter
	action()
	os.Stderr = saved
	pipeWriter.Close()
	printed, _ := io.ReadAll(pipeReader)
	return string(printed)
}

// The file round-trip in both forms, driven through the env passphrase (no terminal in tests): a
// fresh wallet is created sealed, reloads with the passphrase, and never touches plaintext; the
// empty passphrase keeps the ssh-keygen-style plaintext form, and legacy files load unchanged.
func TestKeyFileRoundTrip(t *testing.T) {
	directory := t.TempDir()

	t.Setenv(passphraseEnvironmentVariable, "hunter2")
	path := filepath.Join(directory, "wallet.json")
	publicKey, privateKey, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var storedKey keyFile
	if err := json.Unmarshal(data, &storedKey); err != nil {
		t.Fatal(err)
	}
	if storedKey.Encrypted == nil || storedKey.PrivateKey != "" {
		t.Fatal("with a passphrase set, the key must be stored sealed only")
	}
	if bytes.Contains(data, privateKey) {
		t.Fatal("the raw private key must not appear in the sealed file")
	}
	reloadedPublicKey, reloadedPrivateKey, err := loadOrCreateKey(path)
	if err != nil || !bytes.Equal(publicKey, reloadedPublicKey) || !bytes.Equal(privateKey, reloadedPrivateKey) {
		t.Fatalf("reloading the sealed wallet failed: %v", err)
	}
	t.Setenv(passphraseEnvironmentVariable, "wrong")
	if _, _, err := loadOrCreateKey(path); err == nil {
		t.Fatal("a wrong passphrase must fail, not silently regenerate the wallet")
	}

	// Legacy/empty-passphrase plaintext form still works (created non-interactively with no env).
	t.Setenv(passphraseEnvironmentVariable, "")
	plainPath := filepath.Join(directory, "plain.json")
	publicKey, privateKey, err = loadOrCreateKey(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	reloadedPublicKey, reloadedPrivateKey, err = loadOrCreateKey(plainPath)
	if err != nil || !bytes.Equal(publicKey, reloadedPublicKey) || !bytes.Equal(privateKey, reloadedPrivateKey) {
		t.Fatalf("reloading the plaintext wallet failed: %v", err)
	}
}

// The key file holds the private key in the form the network's scheme stores it, in plaintext or
// sealed, and the publicKey it derives, for display. A file whose private key the scheme cannot parse,
// such as an earlier network's 64-byte Ed25519 key, is refused with an error that says so.
func TestKeyFileHoldsTheSeed(t *testing.T) {
	directory := t.TempDir()
	for name, passphrase := range map[string]string{"plain.json": "", "sealed.json": "correct horse"} {
		t.Setenv(passphraseEnvironmentVariable, passphrase)
		path := filepath.Join(directory, name)
		publicKey, privateKey, err := loadOrCreateKey(path)
		if err != nil {
			t.Fatal(err)
		}
		var storedKey keyFile
		data, _ := os.ReadFile(path)
		if err := json.Unmarshal(data, &storedKey); err != nil {
			t.Fatal(err)
		}
		stored, err := base64.StdEncoding.DecodeString(storedKey.PrivateKey)
		if passphrase != "" {
			stored, err = storedKey.Encrypted.open(passphrase)
		}
		if err != nil || !bytes.Equal(stored, privateKey) {
			t.Fatalf("%s holds a %d-byte private key (%v), not the wallet's", name, len(stored), err)
		}
		scheme := signing.NetworkScheme()
		derived, err := scheme.ParsePrivateKey(stored)
		if err != nil || scheme.ValidatePublicKey(publicKey) != nil || !bytes.Equal(derived.PublicKey(), publicKey) ||
			storedKey.PublicKey != base64.StdEncoding.EncodeToString(publicKey) {
			t.Fatalf("%s: its publicKey is not the key its private key derives (%v)", name, err)
		}
	}

	t.Setenv(passphraseEnvironmentVariable, "")
	earlier := filepath.Join(directory, "earlier.json")
	data, _ := json.Marshal(keyFile{PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 64))})
	if err := os.WriteFile(earlier, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrCreateKey(earlier); err == nil || !strings.Contains(err.Error(), "earlier network") {
		t.Fatalf("a 64-byte private key must be refused as an earlier network's, got: %v", err)
	}
}
