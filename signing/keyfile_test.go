package signing

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A key file the wallet wrote before the format moved from ui/keyfile.go to this package must open as
// before. The two files in testdata were written by that code, for one throwaway key pair: sealed under
// the passphrase "throwaway test passphrase", and in plaintext. Both unlock to a private key that
// derives their publicKey, and the code here writes the plaintext one byte for byte as that code did.
func TestKeyFileWrittenByTheWalletStillOpens(t *testing.T) {
	sealed, err := ReadKeyFile(filepath.Join("testdata", "sealed-key-file.json"))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Encrypted == nil || sealed.Encrypted.Iterations != 600_000 || sealed.PrivateKey != "" {
		t.Fatalf("the sealed fixture reads as %+v", sealed)
	}
	privateKey, err := sealed.Unlock("throwaway test passphrase")
	if err != nil {
		t.Fatalf("the wallet's sealed key file no longer opens: %v", err)
	}
	parsedKey, err := NetworkScheme().ParsePrivateKey(privateKey)
	if err != nil || base64.StdEncoding.EncodeToString(parsedKey.PublicKey()) != sealed.PublicKey {
		t.Fatalf("the unsealed private key does not derive the file's publicKey (%v)", err)
	}
	sealedData, _ := os.ReadFile(filepath.Join("testdata", "sealed-key-file.json"))
	if again, _ := json.MarshalIndent(sealed, "", "  "); !bytes.Equal(again, sealedData) {
		t.Errorf("KeyFile does not write the sealed fixture back as it was:\n%s", again)
	}

	plainPath := filepath.Join("testdata", "plain-key-file.json")
	plain, err := ReadKeyFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	plainPrivateKey, err := plain.Unlock("")
	if err != nil || !bytes.Equal(plainPrivateKey, privateKey) || plain.PublicKey != sealed.PublicKey {
		t.Fatalf("the plaintext fixture holds another key (%v)", err)
	}
	rewritten := filepath.Join(t.TempDir(), "plain-key-file.json")
	if err := WriteKeyFile(rewritten, parsedKey.PublicKey(), privateKey, ""); err != nil {
		t.Fatal(err)
	}
	plainData, _ := os.ReadFile(plainPath)
	if rewrittenData, _ := os.ReadFile(rewritten); !bytes.Equal(rewrittenData, plainData) {
		t.Errorf("WriteKeyFile writes the plaintext key file differently:\n%s", rewrittenData)
	}
}

// A key file that carries its private key both sealed and in plaintext is refused, under the right
// passphrase too: a plaintext key beside the sealed one would make the seal pointless.
func TestAKeyFileWithBothASealedAndAPlaintextKeyIsRefused(t *testing.T) {
	sealed, err := ReadKeyFile(filepath.Join("testdata", "sealed-key-file.json"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ReadKeyFile(filepath.Join("testdata", "plain-key-file.json"))
	if err != nil {
		t.Fatal(err)
	}
	both := sealed
	both.PrivateKey = plain.PrivateKey
	if _, err := both.Unlock("throwaway test passphrase"); err == nil || !strings.Contains(err.Error(), "both sealed and in plaintext") {
		t.Fatalf("a key file with both forms of its key: got %v, want an error", err)
	}
}

// A wallet sealed before the KDF moved to the standard library's crypto/pbkdf2 must still open. This
// fixture was sealed by the hand-written PBKDF2-HMAC-SHA256 the wallet used until then, at 1000
// iterations so the test is fast (a key file records its own count).
func TestWalletSealedBeforeStdlibKDFStillOpens(t *testing.T) {
	var sealed EncryptedKey
	fixture := `{"kdf":"pbkdf2-hmac-sha256","iterations":1000,"salt":"Z28tbGVkZ2VyLXNhbHQxNg==","nonce":"bm9uY2UtMTJieXRl","ciphertext":"tSh8iOY3KoYXJc0XxL4ER2v/aldVki/JXwU4pOcFYxuRIILptdD/84JEesSlRlP9"}`
	if err := json.Unmarshal([]byte(fixture), &sealed); err != nil {
		t.Fatal(err)
	}
	if opened, err := (KeyFile{Encrypted: &sealed}).Unlock("correct horse"); err != nil || string(opened) != "this-stands-in-for-a-private-key" {
		t.Fatalf("a wallet sealed by the earlier KDF no longer opens: %q, %v", opened, err)
	}
}

// Seal/open round-trip: the right passphrase recovers the key, a wrong one (or a tampered
// ciphertext) is rejected by the AEAD, never returning garbage key bytes. A nonce of the wrong size is
// refused too: AES-GCM would panic on it.
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
	shortNonce := *sealed
	shortNonce.Nonce = base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := shortNonce.open("correct horse"); err == nil {
		t.Fatal("a nonce of the wrong size must not unlock")
	}
}

// A passphrase file loses one trailing newline, as an editor or echo leaves it, and nothing else. An
// empty passphrase is refused, since it would leave a key in plaintext.
func TestReadPassphraseFile(t *testing.T) {
	directory := t.TempDir()
	for content, want := range map[string]string{"correct horse\n": "correct horse", "correct horse": "correct horse",
		"correct horse\n\n": "correct horse\n", " correct horse \n": " correct horse "} {
		path := filepath.Join(directory, "passphrase")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if passphrase, err := ReadPassphraseFile(path); err != nil || passphrase != want {
			t.Errorf("a passphrase file holding %q reads as %q (%v), want %q", content, passphrase, err, want)
		}
	}
	for _, content := range []string{"", "\n"} {
		path := filepath.Join(directory, "empty")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPassphraseFile(path); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("a passphrase file holding %q was taken (%v)", content, err)
		}
	}
}

// A secret file holds 64 lowercase hex digits, as keygen -secret prints them, with or without one
// trailing newline; the secret is the 32 bytes they encode. Any other content is refused, so a secret
// mistyped on one side never passes for another.
func TestReadSecretFile(t *testing.T) {
	directory := t.TempDir()
	secretHex := strings.Repeat("0a", 32)
	for content, wantTaken := range map[string]bool{
		secretHex + "\n": true, secretHex: true,
		secretHex + "\n\n": false, strings.ToUpper(secretHex): false, secretHex[2:]: false, secretHex + "0a": false, "": false,
	} {
		path := filepath.Join(directory, "secret")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		secret, err := ReadSecretFile(path)
		if taken := err == nil && bytes.Equal(secret, bytes.Repeat([]byte{0x0a}, 32)); taken != wantTaken {
			t.Errorf("a secret file holding %q: taken %v (%v), want %v", content, taken, err, wantTaken)
		}
	}
}
