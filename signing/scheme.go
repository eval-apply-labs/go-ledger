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

// Scheme is a signature algorithm: key generation, key parsing and verification over raw bytes.
// A Scheme does not sign; the PrivateKey it parses does. Domain separation and canonical encoding
// are one layer up: Signer.Sign gives its key only a domain preimage, and Verify rebuilds one for
// the Scheme. In tests it is also implemented by wrappers of MLDSA65 that replace one method:
// countingScheme (consensus and node tests), brokenScheme (consensus) and verificationCounter
// (ledger).
type Scheme interface { // implemented by MLDSA65 (mldsa65.go), the network's one scheme
	// GenerateKey returns a fresh (public, private) key pair as raw bytes, the private key in the
	// stored form ParsePrivateKey reads.
	GenerateKey() (publicKey, privateKey []byte, err error)
	// ParsePrivateKey reads a stored private key once, so each signature does not derive it again.
	ParsePrivateKey(privateKey []byte) (PrivateKey, error)
	// Verify reports whether signature is a valid signature over message by the public key.
	// It must return false (never panic) on malformed keys or signatures.
	Verify(publicKey, message, signature []byte) bool
}

// PrivateKey is a private key that ParsePrivateKey read. In tests it is also implemented by
// brokenKey (consensus), a key that cannot sign.
type PrivateKey interface { // implemented by mldsa65Key (mldsa65.go)
	// PublicKey returns the public key that verifies this key's signatures.
	PublicKey() []byte
	// Sign returns a signature over message.
	Sign(message []byte) ([]byte, error)
}
