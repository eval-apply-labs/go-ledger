package signing

// The key file, which the wallet (ui/keyfile.go) and the signer (cmd/signer) read, and keygen -encrypt
// writes. It is JSON: the public key, for display, and the private key in the form the network's scheme
// stores it, in plaintext or sealed like an SSH key: a passphrase is stretched with PBKDF2-HMAC-SHA256
// and the key sealed with AES-256-GCM, so a copied, backed-up or stolen key file is useless without the
// passphrase. An empty passphrase keeps the plaintext form, as ssh-keygen does. Where the passphrase
// comes from is the caller's choice: the wallet asks for it or reads an environment variable, the
// signer and keygen read it from a file (ReadPassphraseFile). A signer client's secret is read from a
// file too (ReadSecretFile). Zero external dependencies: PBKDF2 comes from the standard library
// (crypto/pbkdf2).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// keyFileIterations follows the OWASP recommendation for PBKDF2-HMAC-SHA256 (~2025). Stored in the
// file, so it can be raised later without breaking existing key files.
const keyFileIterations = 600_000

// KeyFile is a key file's JSON form. Its publicKey is outside the seal, so a reader checks it against
// the key the private key derives before it shows it.
type KeyFile struct {
	PublicKey  string        `json:"publicKey"`                     // its public key, for display
	PrivateKey string        `json:"privateKey,omitempty"`          // the private key, plaintext (empty passphrase)
	Encrypted  *EncryptedKey `json:"encryptedPrivateKey,omitempty"` // the private key, sealed
}

// EncryptedKey is a private key sealed under a passphrase.
type EncryptedKey struct {
	KDF        string `json:"kdf"` // "pbkdf2-hmac-sha256"
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`       // base64
	Nonce      string `json:"nonce"`      // base64
	Ciphertext string `json:"ciphertext"` // base64: AES-256-GCM over the private key
}

// ReadKeyFile reads the key file at path. The error of a missing file is os.ErrNotExist (errors.Is).
func ReadKeyFile(path string) (KeyFile, error) {
	var keyFile KeyFile
	data, err := os.ReadFile(path)
	if err != nil {
		return keyFile, err
	}
	if err := json.Unmarshal(data, &keyFile); err != nil {
		return keyFile, fmt.Errorf("unreadable key file %s: %w", path, err)
	}
	return keyFile, nil
}

// Unlock returns the stored private key: unsealed under passphrase, or the plaintext one, for which the
// passphrase is not used. A wrong passphrase, or a sealed key changed in any way, is refused by
// AES-GCM, never returning garbage key bytes. A key file that carries both forms is refused: a
// plaintext key beside the sealed one would make the seal pointless.
func (keyFile KeyFile) Unlock(passphrase string) ([]byte, error) {
	switch {
	case keyFile.Encrypted != nil && keyFile.PrivateKey != "":
		return nil, errors.New("the key file carries its private key both sealed and in plaintext: a key file holds one or the other")
	case keyFile.Encrypted != nil:
		return keyFile.Encrypted.open(passphrase)
	case keyFile.PrivateKey != "":
		return base64.StdEncoding.DecodeString(keyFile.PrivateKey)
	default:
		return nil, errors.New("the key file carries no private key")
	}
}

// WriteKeyFile writes the key pair to path: sealed under the passphrase, or plaintext if it is empty.
// It may replace the only copy of a key, so the write is crash-atomic: a synced temp file (mode 0600)
// renamed over the old one, then the folder synced, so the rename itself survives a crash (Windows,
// which cannot sync a folder, skips that). A path that is a symlink is followed, so the file it points
// to is the one replaced: renaming over the link itself would leave a plaintext key there.
func WriteKeyFile(path string, publicKey, privateKey []byte, passphrase string) error {
	keyFile := KeyFile{PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	if passphrase == "" {
		keyFile.PrivateKey = base64.StdEncoding.EncodeToString(privateKey)
	} else {
		sealed, err := sealPrivateKey(privateKey, passphrase)
		if err != nil {
			return err
		}
		keyFile.Encrypted = sealed
	}
	data, err := json.MarshalIndent(keyFile, "", "  ")
	if err != nil {
		return err
	}
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	temporaryFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(temporaryFile.Name()) // nothing left to remove once renamed
	_, err = temporaryFile.Write(data)
	if err == nil {
		err = temporaryFile.Sync()
	}
	if closeErr := temporaryFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryFile.Name(), path); err != nil || runtime.GOOS == "windows" {
		return err
	}
	folder, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer folder.Close()
	return folder.Sync()
}

// ReadPassphraseFile reads a passphrase from the file at path, such as a container secret, without one
// trailing newline. An empty passphrase is refused: it would leave a key in plaintext.
func ReadPassphraseFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	passphrase := strings.TrimSuffix(string(data), "\n")
	if passphrase == "" {
		return "", fmt.Errorf("the passphrase file %s is empty", path)
	}
	return passphrase, nil
}

// ReadSecretFile reads a signer client's secret from the file at path: 64 lowercase hex digits, with or
// without one trailing newline, as keygen -secret prints them. The secret is the 32 bytes they encode.
// The signer reads its clients' secrets with it, and the engine its own (spec §2.2, §2.7).
func ReadSecretFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(string(data), "\n")
	secret, err := hex.DecodeString(text)
	if err != nil || len(secret) != 32 || hex.EncodeToString(secret) != text {
		return nil, fmt.Errorf("the secret file %s does not hold 64 lowercase hex digits, as keygen -secret prints them", path)
	}
	return secret, nil
}

func sealPrivateKey(privateKey []byte, passphrase string) (*EncryptedKey, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aesGCM, err := gcmFor(passphrase, salt, keyFileIterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &EncryptedKey{
		KDF:        "pbkdf2-hmac-sha256",
		Iterations: keyFileIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(aesGCM.Seal(nil, nonce, privateKey, nil)),
	}, nil
}

func (sealedKey *EncryptedKey) open(passphrase string) ([]byte, error) {
	if sealedKey.KDF != "pbkdf2-hmac-sha256" || sealedKey.Iterations < 1 {
		return nil, fmt.Errorf("unsupported key encryption %q", sealedKey.KDF)
	}
	salt, err := base64.StdEncoding.DecodeString(sealedKey.Salt)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(sealedKey.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealedKey.Ciphertext)
	if err != nil {
		return nil, err
	}
	aesGCM, err := gcmFor(passphrase, salt, sealedKey.Iterations)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aesGCM.NonceSize() { // Open would panic
		return nil, fmt.Errorf("a key file's nonce is %d bytes, not %d", len(nonce), aesGCM.NonceSize())
	}
	privateKey, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("wrong passphrase (or a corrupted key file)")
	}
	return privateKey, nil
}

func gcmFor(passphrase string, salt []byte, iterations int) (cipher.AEAD, error) {
	derivedKey, err := pbkdf2.Key(sha256.New, passphrase, salt, iterations, 32)
	if err != nil {
		return nil, err
	}
	blockCipher, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blockCipher)
}
