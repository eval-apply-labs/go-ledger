// Package signing provides domain-separated digital signatures for the ledger.
//
// This is the deliberate fix for the central finding of the security audit
// (C1: cross-protocol signature reuse). There, a single unscoped key signed every payload
// type over bare, ':'-joined strings with no type tag or network id, so a signature made in
// one context could be replayed in another. Here, signing is ALWAYS scoped to a Domain and
// bound to a network id via a canonical, length-prefixed preimage (see domain.go), and there
// is deliberately no "sign these raw bytes" API -- the only way to produce a signature is
// through a Domain.
package signing

// Scheme is a signature algorithm: key generation, signing and verification over raw bytes.
// Domain separation and canonical encoding are handled one layer up (see Signer / Verify),
// so a Scheme only ever sees an already-framed preimage -- it never signs caller free-text.
// Keeping the algorithm behind this interface lets a post-quantum scheme (e.g. ML-DSA)
// be dropped in later without touching the domain-separation layer -- see mldsa.go for the
// commented, non-default ML-DSA slot and why it is not yet active (no stdlib primitive).
type Scheme interface {
	// Name identifies the scheme (e.g. "ed25519").
	Name() string
	// GenerateKey returns a fresh (public, private) key pair as raw bytes.
	GenerateKey() (pub, priv []byte, err error)
	// Sign returns a signature over message using the private key.
	Sign(priv, message []byte) ([]byte, error)
	// Verify reports whether sig is a valid signature over message by the public key.
	// It must return false (never panic) on malformed keys or signatures.
	Verify(pub, message, sig []byte) bool
}
