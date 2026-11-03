package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// -secret prints a client secret for the signer: 32 random bytes in lowercase hex, one line, and nothing
// else, a new one each run.
func TestKeygenSecretPrintsThirtyTwoRandomBytesInHex(t *testing.T) {
	var first, second bytes.Buffer
	if err := run([]string{"-secret"}, &first); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-secret"}, &second); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}\n$`).MatchString(first.String()) {
		t.Fatalf("keygen -secret printed %q, want 64 lowercase hex digits and a newline", first.String())
	}
	if first.String() == second.String() {
		t.Fatal("two runs of keygen -secret printed the same secret")
	}
}

// -encrypt writes a new key file, mode 0600, with the private key sealed under the passphrase file's
// passphrase (less its trailing newline) and none in plaintext, and prints the public key the private key
// derives.
func TestKeygenEncryptWritesASealedKeyFile(t *testing.T) {
	directory := t.TempDir()
	passphraseFile := filepath.Join(directory, "passphrase")
	if err := os.WriteFile(passphraseFile, []byte("throwaway test passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFilePath := filepath.Join(directory, "signer-key.json")
	var output bytes.Buffer
	if err := run([]string{"-encrypt", keyFilePath, "-passphrase-file", passphraseFile}, &output); err != nil {
		t.Fatal(err)
	}
	fileInformation, err := os.Stat(keyFilePath)
	if err != nil || fileInformation.Mode().Perm() != 0o600 {
		t.Fatalf("the key file is not mode 0600 (%v)", err)
	}
	keyFile, err := signing.ReadKeyFile(keyFilePath)
	if err != nil || keyFile.Encrypted == nil || keyFile.PrivateKey != "" {
		t.Fatalf("the key file is not sealed: %+v (%v)", keyFile, err)
	}
	privateKey, err := keyFile.Unlock("throwaway test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	parsedKey, err := signing.NetworkScheme().ParsePrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.StdEncoding.EncodeToString(parsedKey.PublicKey())
	if output.String() != publicKey+"\n" || keyFile.PublicKey != publicKey {
		t.Fatalf("keygen -encrypt printed %q, want the key file's public key and nothing else", output.String())
	}
}

// -encrypt never replaces a file, a symlink included: it may be another key's only copy. It writes
// nothing without a passphrase, from an empty passphrase file (which would leave the key in plaintext),
// or with flags that do not go together.
func TestKeygenEncryptRefusesToOverwriteOrToWritePlaintext(t *testing.T) {
	directory := t.TempDir()
	passphraseFile := filepath.Join(directory, "passphrase")
	if err := os.WriteFile(passphraseFile, []byte("throwaway test passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(directory, "existing.json")
	if err := os.WriteFile(existing, []byte("another key's only copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, link} {
		if err := run([]string{"-encrypt", path, "-passphrase-file", passphraseFile}, new(bytes.Buffer)); err == nil {
			t.Errorf("keygen -encrypt %s replaced a file that exists", filepath.Base(path))
		}
	}
	if data, _ := os.ReadFile(existing); string(data) != "another key's only copy" {
		t.Fatalf("keygen -encrypt changed a file that exists: %q", data)
	}

	emptyPassphraseFile := filepath.Join(directory, "empty-passphrase")
	if err := os.WriteFile(emptyPassphraseFile, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFilePath := filepath.Join(directory, "key.json")
	for _, arguments := range [][]string{
		{"-encrypt", keyFilePath, "-passphrase-file", emptyPassphraseFile},
		{"-encrypt", keyFilePath},
		{"-encrypt", keyFilePath, "-passphrase-file", passphraseFile, "-secret"},
		{"-encrypt", keyFilePath, "-passphrase-file", passphraseFile, "extra"},
	} {
		if err := run(arguments, new(bytes.Buffer)); err == nil {
			t.Errorf("keygen %q was taken", arguments)
		}
		if _, err := os.Lstat(keyFilePath); err == nil {
			t.Fatalf("keygen %q wrote a key file", arguments)
		}
	}
}
