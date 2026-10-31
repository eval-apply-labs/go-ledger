// Command keygen prints a fresh ML-DSA-65 key pair (base64) for a node's config, or as a name's key in
// a genesis file, a registration or a key change: the public key (2604 characters) and the private
// key, which is the 32-byte seed the key derives from (44 characters). Keys are generated locally and
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
	publicKey, privateKey, err := signing.MLDSA65{}.GenerateKey()
	if err != nil {
		panic(err)
	}
	fmt.Printf("publicKey:  %s\n", base64.StdEncoding.EncodeToString(publicKey))
	fmt.Printf("privateKey: %s\n", base64.StdEncoding.EncodeToString(privateKey))
}
