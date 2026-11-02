// Command keygen prints a fresh key pair of the network's signature scheme (signing.NetworkScheme), in
// base64, for a node's config, or as a name's key in a genesis file, a registration or a key change:
// the public key, and the private key in the form the scheme stores it. Keys are generated locally and
// never transmitted -- the private key is yours to keep (the audit flagged generating keys
// server-side and returning the private key over HTTP; don't do that).
//
// Usage:
//
//	keygen
package main

import (
	"encoding/base64"
	"fmt"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func main() {
	publicKey, privateKey, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		panic(err)
	}
	fmt.Printf("publicKey:  %s\n", base64.StdEncoding.EncodeToString(publicKey))
	fmt.Printf("privateKey: %s\n", base64.StdEncoding.EncodeToString(privateKey))
}
