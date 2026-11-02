// Package signing provides domain-separated digital signatures for the ledger.
//
// It is the fix for the central finding of the security audit (C1: cross-protocol signature reuse),
// where one unscoped key signed every payload type over bare, ':'-joined strings with no type tag or
// network id, so a signature made in one context could be replayed in another. Signer, the type all
// production code signs with, has no method that signs raw bytes: it signs only a canonical,
// length-prefixed preimage that binds a Domain and the network id (see domain.go), and every check
// (Verify, VerifyBase64) rebuilds that preimage. PrivateKey.Sign does take raw bytes; outside tests,
// only Signer.Sign calls it.
package signing

import (
	"encoding/base64"
	"errors"
)

// Scheme is a signature algorithm: its signature size, the public keys it accepts, key generation, key
// parsing and verification over raw bytes. A Scheme does not sign; the PrivateKey it parses does.
// Domain separation and canonical encoding are one layer up: Signer.Sign gives its key only a domain
// preimage, and Verify rebuilds one for the Scheme. The rest of the code knows the network's scheme
// only through this interface and NetworkScheme, so changing the scheme is replacing the files that
// implement it (README, Cryptographic agility). In tests it is also implemented by wrappers of the
// network's scheme that replace one method: countingScheme (consensus and node tests), brokenScheme
// (consensus), verificationCounter (ledger) and keyRefusingScheme (signing).
//
// A scheme's methods, and those of the keys it parses, must be safe to call from many goroutines at
// once: the node signs receipts on its HTTP handlers while its loop signs votes. A public key must have
// one byte encoding: the ledger tells keys apart by their bytes, so ValidatePublicKey, and so Verify,
// must refuse any other encoding of a key. The byte bounds set elsewhere assume keys and signatures of
// a few kilobytes: ledger.MaxMessageBytesLimit (1 MiB, which a ⅔ certificate, one signature per
// validator, must fit), the node's 2 MiB proposal bound, the ledger's 768 KiB of block content per
// block and node.MaxCheckpointBytes (64 MiB). A scheme with much larger ones needs them rechecked.
type Scheme interface { // implemented by mldsa65 (mldsa65.go), the network's one scheme
	// Name names the scheme in messages, such as "ML-DSA-65".
	Name() string
	// SignatureSize is the size in bytes of every signature. The shape checks refuse a signature of
	// any other size before it is checked.
	SignatureSize() int
	// ValidatePublicKey returns nil for a public key the scheme accepts: of the right size, decodable,
	// and not degenerate, such as a weak key or another encoding of a key, as the scheme defines them.
	// Otherwise its error says why. Verify refuses every key ValidatePublicKey refuses. It must not
	// panic, whatever the bytes. Every key that enters the ledger passes it, through
	// ValidatePublicKeyBase64.
	ValidatePublicKey(publicKey []byte) error
	// GenerateKey returns a fresh (public, private) key pair as raw bytes, the private key in the
	// stored form ParsePrivateKey reads.
	GenerateKey() (publicKey, privateKey []byte, err error)
	// ParsePrivateKey reads a stored private key once, so each signature does not derive it again. It
	// returns an error, never panics, for a stored key of any other form.
	ParsePrivateKey(privateKey []byte) (PrivateKey, error)
	// Verify reports whether signature is a valid signature over message by the public key. It must
	// return false, never panic, on a malformed key or signature, refuse every key ValidatePublicKey
	// refuses, and refuse a key or a signature of the wrong size before any costly work: anyone can
	// send a node a signature to check.
	Verify(publicKey, message, signature []byte) bool
}

// PrivateKey is a private key that ParsePrivateKey read. In tests it is also implemented by
// brokenKey (consensus), a key that cannot sign.
type PrivateKey interface { // implemented by mldsa65Key (mldsa65.go)
	// PublicKey returns the public key that verifies this key's signatures.
	PublicKey() []byte
	// Sign returns a signature over message. It must be deterministic, one key and one message giving
	// one signature: the round driver relies on it, so relayed copies of one vote are the same bytes.
	Sign(message []byte) ([]byte, error)
}

// SignatureBase64Length is the length, in standard base64, of a signature of scheme. Shape checks
// refuse any other length before a signature is checked.
func SignatureBase64Length(scheme Scheme) int {
	return base64.StdEncoding.EncodedLen(scheme.SignatureSize())
}

// ValidatePublicKeyBase64 returns nil for a public key of scheme in its one spelling: the canonical
// base64 (CanonicalBase64) of bytes that scheme.ValidatePublicKey accepts. Otherwise its error says
// why, with the scheme's reason when the bytes are the scheme's to refuse. Every key that enters the
// ledger passes it: a genesis account's, the server config's, the key a person types in the wallet, and
// the new key of a name request or of a ⅔ name action.
func ValidatePublicKeyBase64(scheme Scheme, key string) error {
	if !CanonicalBase64(key) {
		return errors.New("it is not standard base64 in its one spelling")
	}
	raw, _ := base64.StdEncoding.DecodeString(key) // canonical, so it decodes
	return scheme.ValidatePublicKey(raw)
}
