// Command keygen prints a fresh Ed25519 key pair (base64) for use in a node config or as an
// account identity. Keys are generated locally and never transmitted -- the private key is
// yours to keep (the audit flagged generating keys server-side and returning
// the private key over HTTP; don't do that).
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
	pub, priv, err := signing.Ed25519{}.GenerateKey()
	if err != nil {
		panic(err)
	}
	fmt.Printf("publicKey:  %s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("privateKey: %s\n", base64.StdEncoding.EncodeToString(priv))
}
