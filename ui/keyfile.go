package main

// The wallet key file. The private key is stored encrypted like an SSH key: a passphrase (asked at
// startup, terminal echo off) is stretched with PBKDF2-HMAC-SHA256 and the key sealed with
// AES-256-GCM, so a copied/backed-up/stolen wallet.json is useless without the passphrase — the
// dominant loss vector for a file that otherwise holds the key in plain base64. An EMPTY passphrase
// keeps the plaintext form (exactly like ssh-keygen), and a legacy plaintext file keeps working and
// is offered a one-time upgrade. Zero external dependencies: PBKDF2 is implemented here (RFC 8018)
// from stdlib HMAC-SHA256.

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// kdfIterations follows the OWASP recommendation for PBKDF2-HMAC-SHA256 (~2025). Stored in the
// file, so it can be raised later without breaking existing wallets.
const kdfIterations = 600_000

// passphraseEnv lets scripts and tests supply the passphrase non-interactively.
const passphraseEnv = "GOLEDGER_WALLET_PASSPHRASE"

type keyFile struct {
	PublicKey  string        `json:"publicKey"`
	PrivateKey string        `json:"privateKey,omitempty"`          // plaintext form (empty passphrase / legacy)
	Encrypted  *encryptedKey `json:"encryptedPrivateKey,omitempty"` // passphrase-sealed form
}

type encryptedKey struct {
	KDF        string `json:"kdf"` // "pbkdf2-hmac-sha256"
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`       // base64
	Nonce      string `json:"nonce"`      // base64
	Ciphertext string `json:"ciphertext"` // base64: AES-256-GCM over the raw private key
}

// loadOrCreateKey returns the wallet key pair, creating and saving a fresh one if no file exists
// yet (asking whether to protect it with a passphrase). Only a MISSING file creates a new key — any
// other failure (unreadable JSON, wrong passphrase) is surfaced instead of silently overwriting a
// wallet that may hold funds.
func loadOrCreateKey(path string) (pub, priv []byte, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if pub, priv, err = (signing.Ed25519{}).GenerateKey(); err != nil {
			return nil, nil, err
		}
		pass, err := newPassphrase("set a wallet passphrase (empty = store unencrypted): ")
		if err != nil {
			return nil, nil, err
		}
		return pub, priv, saveKey(path, pub, priv, pass)
	}
	if err != nil {
		return nil, nil, err
	}
	var k keyFile
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, nil, fmt.Errorf("unreadable wallet file %s: %w", path, err)
	}
	if pub, err = base64.StdEncoding.DecodeString(k.PublicKey); err != nil {
		return nil, nil, err
	}
	switch {
	case k.Encrypted != nil:
		pass, err := getPassphrase("wallet passphrase: ")
		if err != nil {
			return nil, nil, err
		}
		priv, err = k.Encrypted.open(pass)
		return pub, priv, err
	case k.PrivateKey != "":
		if priv, err = base64.StdEncoding.DecodeString(k.PrivateKey); err != nil {
			return nil, nil, err
		}
		// Legacy plaintext file: offer the one-time upgrade (skipped when non-interactive).
		if interactive() && os.Getenv(passphraseEnv) == "" {
			pass, err := newPassphrase("wallet key is stored UNENCRYPTED — set a passphrase now (empty to keep): ")
			if err == nil && pass != "" {
				err = saveKey(path, pub, priv, pass)
			}
			if err != nil {
				return nil, nil, err
			}
		}
		return pub, priv, nil
	}
	return nil, nil, fmt.Errorf("wallet file %s carries no private key", path)
}

// saveKey writes the key pair: sealed under the passphrase, or plaintext if it is empty.
func saveKey(path string, pub, priv []byte, passphrase string) error {
	k := keyFile{PublicKey: base64.StdEncoding.EncodeToString(pub)}
	if passphrase == "" {
		k.PrivateKey = base64.StdEncoding.EncodeToString(priv)
	} else {
		sealed, err := sealPrivateKey(priv, passphrase)
		if err != nil {
			return err
		}
		k.Encrypted = sealed
	}
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func sealPrivateKey(priv []byte, passphrase string) (*encryptedKey, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := gcmFor(passphrase, salt, kdfIterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &encryptedKey{
		KDF:        "pbkdf2-hmac-sha256",
		Iterations: kdfIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, priv, nil)),
	}, nil
}

func (e *encryptedKey) open(passphrase string) ([]byte, error) {
	if e.KDF != "pbkdf2-hmac-sha256" || e.Iterations < 1 {
		return nil, fmt.Errorf("unsupported wallet key encryption %q", e.KDF)
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(e.Ciphertext)
	if err != nil {
		return nil, err
	}
	gcm, err := gcmFor(passphrase, salt, e.Iterations)
	if err != nil {
		return nil, err
	}
	priv, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("cannot unlock the wallet: wrong passphrase (or a corrupted key file)")
	}
	return priv, nil
}

func gcmFor(passphrase string, salt []byte, iterations int) (cipher.AEAD, error) {
	block, err := aes.NewCipher(pbkdf2SHA256([]byte(passphrase), salt, iterations, 32))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// pbkdf2SHA256 is PBKDF2 (RFC 8018 §5.2) with HMAC-SHA256 — implemented here because the stdlib of
// this toolchain ships the primitives but not the KDF, and the ui module stays dependency-free.
// Verified against published test vectors in keyfile_test.go.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	derived := make([]byte, 0, blocks*hashLen)
	var counter [4]byte
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Write(counter[:])
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		derived = append(derived, t...)
	}
	return derived[:keyLen]
}

// ---- passphrase acquisition ----

// stdin is shared so a prompt-and-confirm pair cannot lose a buffered line between two readers.
var stdin = bufio.NewReader(os.Stdin)

// getPassphrase returns the passphrase for an EXISTING sealed key: the environment override if set,
// otherwise a terminal prompt. A sealed wallet in a non-interactive run with no override is an error.
func getPassphrase(prompt string) (string, error) {
	if pass := os.Getenv(passphraseEnv); pass != "" {
		return pass, nil
	}
	if !interactive() {
		return "", fmt.Errorf("the wallet key is passphrase-protected; set %s or run interactively", passphraseEnv)
	}
	return readSecret(prompt)
}

// newPassphrase asks for a NEW passphrase (with confirmation). Non-interactively it returns the
// environment override, or empty (plaintext) so scripted runs keep working.
func newPassphrase(prompt string) (string, error) {
	if pass := os.Getenv(passphraseEnv); pass != "" {
		return pass, nil
	}
	if !interactive() {
		// No terminal to prompt on and no env override: we cannot obtain a passphrase, so the key is
		// stored UNENCRYPTED. Say so loudly rather than silently — an operator running the wallet as a
		// service is the one who most needs to know, and can seal it by setting the env var.
		fmt.Fprintf(os.Stderr, "warning: no terminal to set a passphrase — the wallet key is stored UNENCRYPTED. Set %s to seal it.\n", passphraseEnv)
		return "", nil
	}
	pass, err := readSecret(prompt)
	if err != nil {
		return "", nil // no usable terminal after all (e.g. stdin is /dev/null): keep plaintext
	}
	if pass == "" {
		return "", nil
	}
	again, err := readSecret("repeat the passphrase: ")
	if err != nil {
		return "", err
	}
	if pass != again {
		return "", errors.New("passphrases do not match")
	}
	return pass, nil
}

// interactive reports whether stdin is a real terminal we can prompt on. A character-device check
// alone is NOT enough — /dev/null is a character device too, so a wallet run as a service (systemd,
// nohup) with stdin /dev/null would be misread as interactive: a sealed wallet would then fail with
// a raw EOF instead of the "set the passphrase env var" hint, and a fresh wallet would silently fall
// through the passphrase prompt to plaintext. We confirm a real tty with `stty` (the same tool
// readSecret already uses), and fall back to the char-device heuristic only if stty is absent, so a
// genuine terminal on a minimal system still gets a prompt.
func interactive() bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
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
	line, err := stdin.ReadString('\n')
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
