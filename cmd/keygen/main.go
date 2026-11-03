// Command keygen prints a fresh key pair of the network's signature scheme (signing.NetworkScheme), in
// base64, for a node's config, or as a name's key in a genesis file, a registration or a key change:
// the public key, and the private key in the form the scheme stores it. Keys are generated locally and
// never transmitted -- the private key is yours to keep (the audit flagged generating keys
// server-side and returning the private key over HTTP; don't do that).
//
// With -encrypt, it writes the fresh key pair to a new key file instead, sealed under the passphrase in
// the -passphrase-file (signing.WriteKeyFile: the format the wallet and the signer read), and prints
// only the public key. It never replaces a file that exists. With -secret, it prints a new client
// secret for the signer, 32 random bytes in lowercase hex, and nothing else.
//
// Usage:
//
//	keygen
//	keygen -encrypt <key file> -passphrase-file <file>
//	keygen -secret
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
}

// run is keygen with its arguments and its output passed in, so the tests can call it.
func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	secret := flags.Bool("secret", false, "print a new 32-byte client secret for the signer, in hex, and nothing else")
	keyFilePath := flags.String("encrypt", "", "write a new key pair to this key file, sealed, and print its public key")
	passphraseFilePath := flags.String("passphrase-file", "", "the file holding the passphrase that seals the -encrypt key file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	switch {
	case flags.NArg() > 0 || (*secret && *keyFilePath != "") || (*keyFilePath == "") != (*passphraseFilePath == ""):
		return errors.New("usage: keygen | keygen -encrypt <key file> -passphrase-file <file> | keygen -secret")
	case *secret:
		clientSecret := make([]byte, 32)
		if _, err := rand.Read(clientSecret); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, hex.EncodeToString(clientSecret))
		return err
	case *keyFilePath != "":
		return writeEncryptedKeyFile(*keyFilePath, *passphraseFilePath, output)
	}
	publicKey, privateKey, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "publicKey:  %s\n", base64.StdEncoding.EncodeToString(publicKey))
	_, err = fmt.Fprintf(output, "privateKey: %s\n", base64.StdEncoding.EncodeToString(privateKey))
	return err
}

// writeEncryptedKeyFile writes a fresh key pair to a new key file, sealed under the passphrase in the
// passphrase file, and prints its public key. A path that exists, a symlink included, is refused: it may
// be another key's only copy.
func writeEncryptedKeyFile(keyFilePath, passphraseFilePath string, output io.Writer) error {
	passphrase, err := signing.ReadPassphraseFile(passphraseFilePath)
	if err != nil {
		return err
	}
	publicKey, privateKey, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		return err
	}
	// O_EXCL takes the path only if nothing is there, so no file is replaced; WriteKeyFile then renames
	// the key file over the empty one.
	claimedFile, err := os.OpenFile(keyFilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	claimedFile.Close()
	if err := signing.WriteKeyFile(keyFilePath, publicKey, privateKey, passphrase); err != nil {
		os.Remove(keyFilePath)
		return err
	}
	_, err = fmt.Fprintln(output, base64.StdEncoding.EncodeToString(publicKey))
	return err
}
