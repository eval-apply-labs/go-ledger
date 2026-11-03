package main

// The wallet key file. The private key, in the form the network's scheme stores it, is stored
// encrypted like an SSH key: a passphrase (asked at
// startup, terminal echo off) is stretched with PBKDF2-HMAC-SHA256 and the key sealed with
// AES-256-GCM, so a copied/backed-up/stolen wallet.json is useless without the passphrase — the
// dominant loss vector for a file that otherwise holds the key in plain base64. An EMPTY passphrase
// keeps the plaintext form (exactly like ssh-keygen); a plaintext file keeps working and is sealed in
// place as soon as a passphrase is available. The format is signing.KeyFile (signing/keyfile.go),
// which the signer shares: this file finds the passphrase and makes the key on first use.

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// passphraseEnvironmentVariable lets scripts and tests supply the passphrase non-interactively.
const passphraseEnvironmentVariable = "GOLEDGER_WALLET_PASSPHRASE"

// loadOrCreateKey returns the wallet key pair, creating and saving a fresh one if no file exists
// yet (asking whether to protect it with a passphrase). Only a MISSING file creates a new key — any
// other failure (unreadable JSON, wrong passphrase) is surfaced instead of silently overwriting a
// wallet that may hold funds.
func loadOrCreateKey(path string) (publicKey, privateKey []byte, err error) {
	storedKey, err := signing.ReadKeyFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if publicKey, privateKey, err = signing.NetworkScheme().GenerateKey(); err != nil {
			return nil, nil, err
		}
		passphrase, err := newPassphrase("set a wallet passphrase (empty = store unencrypted): ")
		if err != nil {
			return nil, nil, err
		}
		return publicKey, privateKey, signing.WriteKeyFile(path, publicKey, privateKey, passphrase)
	}
	if err != nil {
		return nil, nil, err
	}
	if storedKey.PrivateKey == "" && storedKey.Encrypted == nil {
		return nil, nil, fmt.Errorf("wallet file %s holds a public key only, as a bank's wallet whose key is in its signer does: "+
			"name the signer with -signer-url, -signer-client and -signer-secret-file", path)
	}
	passphrase := "" // a plaintext key needs none
	if storedKey.Encrypted != nil {
		if passphrase, err = getPassphrase("wallet passphrase: "); err != nil {
			return nil, nil, err
		}
	}
	if privateKey, err = storedKey.Unlock(passphrase); err != nil {
		return nil, nil, fmt.Errorf("cannot unlock the wallet: %w", err)
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
			err = signing.WriteKeyFile(path, publicKey, privateKey, passphrase)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return publicKey, privateKey, nil
}

// readSignerClient reads what a bank's wallet whose key is in the signer program needs (spec §2.7): the
// bank's public key, from the wallet file at path, and the secret of the client it asks the signer as.
// The wallet file holds the public key only, {"publicKey": "<base64>"}, the key the engine's config
// gives: the wallet checks every answer of the signer under it, so it comes from the bank, never from
// the signer. A file that holds a private key is refused: a wallet signs with its own key or through the
// signer, not both. A missing file is an error, never a new key.
func readSignerClient(path string, settings signerSettings) (publicKey, secret []byte, err error) {
	if settings.url == "" || settings.client == "" || settings.secretFile == "" {
		return nil, nil, errors.New("-signer-url, -signer-client and -signer-secret-file go together: where the signer listens, " +
			"the client name its config gives this wallet, and that client's secret file")
	}
	storedKey, err := signing.ReadKeyFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("with -signer-url, the wallet file must hold the bank's public key, as {\"publicKey\": \"<base64>\"}: %w", err)
	}
	if storedKey.PrivateKey != "" || storedKey.Encrypted != nil {
		return nil, nil, fmt.Errorf("wallet file %s holds a private key, and -signer-url names a signer: a wallet signs with its own key or through the signer, not both. "+
			"A bank's wallet file for its signer holds the bank's public key only", path)
	}
	if err := signing.ValidatePublicKeyBase64(signing.NetworkScheme(), storedKey.PublicKey); err != nil {
		return nil, nil, fmt.Errorf("wallet file %s: its publicKey is not a public key of the network's scheme: %w", path, err)
	}
	if secret, err = signing.ReadSecretFile(settings.secretFile); err != nil {
		return nil, nil, fmt.Errorf("-signer-secret-file: %w", err)
	}
	publicKey, _ = base64.StdEncoding.DecodeString(storedKey.PublicKey) // valid, so it decodes
	return publicKey, secret, nil
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
