package main

// The wallet key file. The private key, in the form the network's scheme stores it, is stored
// encrypted like an SSH key: a passphrase (asked at
// startup, terminal echo off) is stretched with PBKDF2-HMAC-SHA256 and the key sealed with
// AES-256-GCM, so a copied/backed-up/stolen wallet.json is useless without the passphrase — the
// dominant loss vector for a file that otherwise holds the key in plain base64. An EMPTY passphrase
// keeps the plaintext form (exactly like ssh-keygen); a plaintext file keeps working and is sealed in
// place as soon as a passphrase is available. Zero external dependencies: PBKDF2 comes from the
// standard library (crypto/pbkdf2).

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// kdfIterations follows the OWASP recommendation for PBKDF2-HMAC-SHA256 (~2025). Stored in the
// file, so it can be raised later without breaking existing wallets.
const kdfIterations = 600_000

// passphraseEnvironmentVariable lets scripts and tests supply the passphrase non-interactively.
const passphraseEnvironmentVariable = "GOLEDGER_WALLET_PASSPHRASE"

type keyFile struct {
	PublicKey  string        `json:"publicKey"`                     // its public key, for display; checked on load
	PrivateKey string        `json:"privateKey,omitempty"`          // the private key, plaintext (empty passphrase)
	Encrypted  *encryptedKey `json:"encryptedPrivateKey,omitempty"` // the private key, sealed
}

type encryptedKey struct {
	KDF        string `json:"kdf"` // "pbkdf2-hmac-sha256"
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`       // base64
	Nonce      string `json:"nonce"`      // base64
	Ciphertext string `json:"ciphertext"` // base64: AES-256-GCM over the private key
}

// loadOrCreateKey returns the wallet key pair, creating and saving a fresh one if no file exists
// yet (asking whether to protect it with a passphrase). Only a MISSING file creates a new key — any
// other failure (unreadable JSON, wrong passphrase) is surfaced instead of silently overwriting a
// wallet that may hold funds.
func loadOrCreateKey(path string) (publicKey, privateKey []byte, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if publicKey, privateKey, err = signing.NetworkScheme().GenerateKey(); err != nil {
			return nil, nil, err
		}
		passphrase, err := newPassphrase("set a wallet passphrase (empty = store unencrypted): ")
		if err != nil {
			return nil, nil, err
		}
		return publicKey, privateKey, saveKey(path, publicKey, privateKey, passphrase)
	}
	if err != nil {
		return nil, nil, err
	}
	var storedKey keyFile
	if err := json.Unmarshal(data, &storedKey); err != nil {
		return nil, nil, fmt.Errorf("unreadable wallet file %s: %w", path, err)
	}
	switch {
	case storedKey.Encrypted != nil:
		passphrase, err := getPassphrase("wallet passphrase: ")
		if err != nil {
			return nil, nil, err
		}
		if privateKey, err = storedKey.Encrypted.open(passphrase); err != nil {
			return nil, nil, err
		}
	case storedKey.PrivateKey != "":
		if privateKey, err = base64.StdEncoding.DecodeString(storedKey.PrivateKey); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("wallet file %s carries no private key", path)
	}
	// The account is the key the private key derives. The publicKey field is outside the seal, so one that
	// disagrees (a tampered or corrupted file) is refused rather than shown as this wallet's account.
	// A private key the network's scheme cannot parse comes from an earlier network, on another scheme.
	scheme := signing.NetworkScheme()
	parsedKey, err := scheme.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("wallet file %s holds a %d-byte private key, not a private key of the network's scheme, %s: it belongs to an earlier network; make a new wallet",
			path, len(privateKey), scheme.Name())
	}
	publicKey = parsedKey.PublicKey()
	if storedKey.PublicKey != base64.StdEncoding.EncodeToString(publicKey) {
		return nil, nil, fmt.Errorf("wallet file %s: publicKey does not match the private key (tampered or corrupted file)", path)
	}
	// A plaintext key is sealed as soon as a passphrase is available: the environment override, or one
	// typed now. With neither, newPassphrase warns that the key stays unencrypted, on every start.
	if storedKey.Encrypted == nil {
		passphrase, err := newPassphrase("wallet key is stored UNENCRYPTED — set a passphrase now (empty to keep): ")
		if err == nil && passphrase != "" {
			err = saveKey(path, publicKey, privateKey, passphrase)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return publicKey, privateKey, nil
}

// saveKey writes the key pair: sealed under the passphrase, or plaintext if it is empty. It may
// replace a funded wallet's only copy, so the write is crash-atomic: a synced temp file (mode 0600)
// renamed over the old one. A path that is a symlink is followed, so the file it points to is the one
// replaced: renaming over the link itself would leave a plaintext key there.
func saveKey(path string, publicKey, privateKey []byte, passphrase string) error {
	storedKey := keyFile{PublicKey: base64.StdEncoding.EncodeToString(publicKey)}
	if passphrase == "" {
		storedKey.PrivateKey = base64.StdEncoding.EncodeToString(privateKey)
	} else {
		sealed, err := sealPrivateKey(privateKey, passphrase)
		if err != nil {
			return err
		}
		storedKey.Encrypted = sealed
	}
	data, err := json.MarshalIndent(storedKey, "", "  ")
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
	return os.Rename(temporaryFile.Name(), path)
}

func sealPrivateKey(privateKey []byte, passphrase string) (*encryptedKey, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aesGCM, err := gcmFor(passphrase, salt, kdfIterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &encryptedKey{
		KDF:        "pbkdf2-hmac-sha256",
		Iterations: kdfIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(aesGCM.Seal(nil, nonce, privateKey, nil)),
	}, nil
}

func (sealedKey *encryptedKey) open(passphrase string) ([]byte, error) {
	if sealedKey.KDF != "pbkdf2-hmac-sha256" || sealedKey.Iterations < 1 {
		return nil, fmt.Errorf("unsupported wallet key encryption %q", sealedKey.KDF)
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
	privateKey, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("cannot unlock the wallet: wrong passphrase (or a corrupted key file)")
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

// ---- passphrase acquisition ----

// standardInputReader is shared so a prompt-and-confirm pair cannot lose a buffered line between two readers.
var standardInputReader = bufio.NewReader(os.Stdin)

// getPassphrase returns the passphrase for an EXISTING sealed key: the environment override if set,
// otherwise a terminal prompt. A sealed wallet in a non-interactive run with no override is an error.
func getPassphrase(prompt string) (string, error) {
	if passphrase := os.Getenv(passphraseEnvironmentVariable); passphrase != "" {
		return passphrase, nil
	}
	if !interactive() {
		return "", fmt.Errorf("the wallet key is passphrase-protected; set %s or run interactively", passphraseEnvironmentVariable)
	}
	return readSecret(prompt)
}

// newPassphrase asks for a NEW passphrase (with confirmation). Non-interactively it returns the
// environment override, or empty (plaintext) so scripted runs keep working.
func newPassphrase(prompt string) (string, error) {
	if passphrase := os.Getenv(passphraseEnvironmentVariable); passphrase != "" {
		return passphrase, nil
	}
	if !interactive() {
		// No terminal to prompt on and no env override: we cannot obtain a passphrase, so the key is
		// stored UNENCRYPTED. Say so loudly rather than silently — an operator running the wallet as a
		// service is the one who most needs to know, and can seal it by setting the env var.
		fmt.Fprintf(os.Stderr, "warning: no terminal to set a passphrase — the wallet key is stored UNENCRYPTED. Set %s to seal it.\n", passphraseEnvironmentVariable)
		return "", nil
	}
	passphrase, err := readSecret(prompt)
	if err != nil {
		return "", nil // no usable terminal after all (e.g. stdin is /dev/null): keep plaintext
	}
	if passphrase == "" {
		return "", nil
	}
	again, err := readSecret("repeat the passphrase: ")
	if err != nil {
		return "", err
	}
	if passphrase != again {
		return "", errors.New("passphrases do not match")
	}
	return passphrase, nil
}

// interactive reports whether stdin is a real terminal we can prompt on. A character-device check
// alone is NOT enough — /dev/null is a character device too, so a wallet run as a service (systemd,
// nohup) with stdin /dev/null would be misread as interactive: a sealed wallet would then fail with
// a raw EOF instead of the "set the passphrase env var" hint, and a fresh wallet would silently fall
// through the passphrase prompt to plaintext. We confirm a real tty with `stty` (the same tool
// readSecret already uses), and fall back to the char-device heuristic only if stty is absent, so a
// genuine terminal on a minimal system still gets a prompt.
func interactive() bool {
	fileInformation, err := os.Stdin.Stat()
	if err != nil || fileInformation.Mode()&os.ModeCharDevice == 0 {
		return false // a pipe, file or redirect is never a terminal
	}
	probe := exec.Command("stty")
	probe.Stdin = os.Stdin // Stdout/Stderr left nil (discarded); stty only queries the tty on stdin
	if probe.Run() == nil {
		return true // stty read terminal settings — a real tty
	}
	if _, err := exec.LookPath("stty"); err != nil {
		return true // no stty to confirm with — trust the char-device heuristic we already passed
	}
	return false // stty is present and said "not a tty" (e.g. /dev/null)
}

// readSecret reads one line from the terminal with echo turned off (best-effort, via stty — the
// portable stdlib has no raw-terminal support and the module stays dependency-free; if stty is
// unavailable the passphrase is read with echo on, still functional).
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	echoOff := exec.Command("stty", "-echo")
	echoOff.Stdin = os.Stdin
	echoSuppressed := echoOff.Run() == nil
	line, err := standardInputReader.ReadString('\n')
	if echoSuppressed {
		echoOn := exec.Command("stty", "echo")
		echoOn.Stdin = os.Stdin
		_ = echoOn.Run()
		fmt.Fprintln(os.Stderr)
	}
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
