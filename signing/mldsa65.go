package signing

import (
	"crypto/mldsa"
	"encoding/base64"
)

// MLDSA65 is ML-DSA-65 (FIPS 204, NIST security level 3), the network's only signature scheme,
// from the standard library's crypto/mldsa. A private key is stored as its 32-byte seed. A public key
// is 1952 bytes and a signature 3309 bytes. Every node must verify exactly as every other does, so
// the scheme is fixed at genesis.
type MLDSA65 struct{} // implements Scheme

// Go has no implements keyword: a type implements an interface by having its methods. This line is
// the nearest thing to one. It does not build unless MLDSA65 has every method of Scheme, with the
// same signature, so the comment above can never go stale. The underscore discards the value.
var _ Scheme = MLDSA65{}

// The lengths, in standard base64, of an ML-DSA-65 public key (1952 bytes) and signature (3309 bytes).
// Shape checks refuse any other length before a signature is checked.
const (
	PublicKeyBase64Length = 2604
	SignatureBase64Length = 4412
)

// ValidPublicKeyBase64 reports whether key is an ML-DSA-65 public key in its one spelling: the
// canonical base64 (CanonicalBase64) of exactly 1952 bytes, so PublicKeyBase64Length characters.
func ValidPublicKeyBase64(key string) bool {
	raw, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(raw) == mldsa.MLDSA65PublicKeySize && CanonicalBase64(key)
}

// GenerateKey draws a fresh seed from crypto/rand and returns it with its public key.
func (MLDSA65) GenerateKey() (publicKey, privateKey []byte, err error) {
	key, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		return nil, nil, err
	}
	return key.PublicKey().Bytes(), key.Bytes(), nil
}

// ParsePrivateKey derives the key from a 32-byte seed, and refuses any other length.
func (MLDSA65) ParsePrivateKey(privateKey []byte) (PrivateKey, error) {
	key, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), privateKey)
	if err != nil {
		return nil, err
	}
	return mldsa65Key{privateKey: key, publicKey: key.PublicKey().Bytes()}, nil
}

// Verify reports whether signature verifies under publicKey. It refuses a key or a signature of any
// size but the exact one, 1952 and 3309 bytes, and never panics. crypto/mldsa refuses a key of the
// wrong size at once, but a signature only after expanding the key, which costs over half a genuine
// check. Anyone can send a node a signature to check, so the signature's size is checked here first.
func (MLDSA65) Verify(publicKey, message, signature []byte) bool {
	if len(signature) != mldsa.MLDSA65SignatureSize {
		return false
	}
	key, err := mldsa.NewPublicKey(mldsa.MLDSA65(), publicKey)
	return err == nil && mldsa.Verify(key, message, signature, nil) == nil
}

// mldsa65Key is a private key derived from its seed once: the derivation costs about 40% of a
// signature.
type mldsa65Key struct { // implements PrivateKey
	privateKey *mldsa.PrivateKey
	publicKey  []byte
}

var _ PrivateKey = mldsa65Key{} // the compiler checks that mldsa65Key implements PrivateKey

func (key mldsa65Key) PublicKey() []byte { return key.publicKey }

// Sign signs deterministically, with no context string: the round driver needs a signature to be a
// function of what is signed, so relayed copies of one vote are the same bytes.
func (key mldsa65Key) Sign(message []byte) ([]byte, error) {
	return key.privateKey.SignDeterministic(message, nil)
}
