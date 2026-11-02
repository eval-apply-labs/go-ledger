package signing

import (
	"crypto/mldsa"
	"errors"
	"fmt"
)

// mldsa65 is ML-DSA-65 (FIPS 204, NIST security level 3), the network's only signature scheme,
// from the standard library's crypto/mldsa. A private key is stored as its 32-byte seed. A public key
// is 1952 bytes and a signature 3309 bytes. Every node must verify exactly as every other does, so
// the scheme is fixed at genesis. The type is unexported, so no package outside signing can name it:
// the files named mldsa65*.go are all that change with the scheme.
type mldsa65 struct{} // implements Scheme

// Go has no implements keyword: a type implements an interface by having its methods. This line is
// the nearest thing to one. It does not build unless mldsa65 has every method of Scheme, with the
// same signature, so the comment above can never go stale. The underscore discards the value.
var _ Scheme = mldsa65{}

// NetworkScheme is the network's signature scheme, and the one place that chooses it. To change the
// scheme, replace the files named mldsa65*.go with files that implement Scheme and define
// NetworkScheme, then start a new network (README, Cryptographic agility).
func NetworkScheme() Scheme { return mldsa65{} }

func (mldsa65) Name() string { return "ML-DSA-65" }

func (mldsa65) SignatureSize() int { return mldsa.MLDSA65SignatureSize }

// ValidatePublicKey refuses a key of any size but 1952 bytes, and the most degenerate of the weak keys
// anyone can sign under: those whose t1, the key's last 1920 bytes, has every coefficient 0 or 1023.
// For such a key each coefficient of t1·2^13 is 0 or -1 mod q, so for every challenge c the
// coefficients of c·t1·2^13 are at most τ = 49 in size. A signature with z = 0 and no hint, whose
// challenge is the hash of the message and of w1 = 0, then verifies for every message, with no private
// key (TestMLDSA65RefusesKeysAnyoneCanSignUnder forges one).
//
// It is a sanity check, not a guarantee that only the key's holder can sign under a key it takes. The
// same forgery works whenever every coefficient of c·t1·2^13, taken mod q and centred, stays below
// γ2 = 261888, and so under other keys with small t1 coefficients, for many messages or for all: with
// one coefficient of 1 and the rest 0, or with every coefficient 1, it verified for each of 100
// messages tried; with every coefficient 2, for about 80 of them. ValidatePublicKey takes those keys,
// for three reasons. No short check covers the family: whether the forgery works depends on the
// challenge, so on the message, and any bound on the coefficients leaves keys just above it under
// which anyone can sign some messages. A key's holder can always let anyone sign under it anyway, by
// publishing its seed, so such a key harms no one its holder could not. And honest key generation
// does not make one: t1 is the high bits of A·s1 + s2, spread over 0 to 1023, so 1536 small
// coefficients come with negligible chance. The check catches the keys a bug would most likely make,
// such as all zeros.
//
// FIPS 204 defines no such check, and crypto/mldsa takes all these keys. ML-DSA-65 has no second
// encoding of a key: every other 1952 bytes are the encoding of exactly one key, which
// crypto/mldsa.NewPublicKey accepts. It is still asked, so that a later crypto/mldsa that refuses more
// refuses it here too.
func (mldsa65) ValidatePublicKey(publicKey []byte) error {
	if len(publicKey) != mldsa.MLDSA65PublicKeySize {
		return fmt.Errorf("a public key is %d bytes, not %d", mldsa.MLDSA65PublicKeySize, len(publicKey))
	}
	if anyoneCanSignUnder(publicKey) {
		return errors.New("a weak key: anyone can sign under it (every coefficient of its t1 is 0 or 1023)")
	}
	_, err := mldsa.NewPublicKey(mldsa.MLDSA65(), publicKey)
	return err
}

// anyoneCanSignUnder reports whether every 10-bit coefficient of the key's t1, packed little-endian
// after its 32-byte seed rho, is 0 or 1023: the weak keys ValidatePublicKey refuses, not all of them.
func anyoneCanSignUnder(publicKey []byte) bool {
	t1 := publicKey[32:]
	for start := 0; start < len(t1)*8; start += 10 {
		coefficient := 0
		for bit := 0; bit < 10; bit++ {
			coefficient |= int(t1[(start+bit)/8]>>((start+bit)%8)&1) << bit
		}
		if coefficient != 0 && coefficient != 1023 {
			return false
		}
	}
	return true
}

// GenerateKey draws a fresh seed from crypto/rand and returns it with its public key.
func (mldsa65) GenerateKey() (publicKey, privateKey []byte, err error) {
	key, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		return nil, nil, err
	}
	return key.PublicKey().Bytes(), key.Bytes(), nil
}

// ParsePrivateKey derives the key from a 32-byte seed, and refuses any other length.
func (mldsa65) ParsePrivateKey(privateKey []byte) (PrivateKey, error) {
	key, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), privateKey)
	if err != nil {
		return nil, err
	}
	return mldsa65Key{privateKey: key, publicKey: key.PublicKey().Bytes()}, nil
}

// Verify reports whether signature verifies under publicKey. It refuses a key or a signature of any
// size but the exact one, 1952 and 3309 bytes, and never panics. It refuses the weak keys
// ValidatePublicKey refuses, and reads the others with crypto/mldsa.NewPublicKey, as ValidatePublicKey
// does, which refuses a key of the wrong size at once, so Verify refuses every key ValidatePublicKey
// refuses. crypto/mldsa refuses a signature of the wrong size only after expanding the key, which
// costs over half a genuine check. Anyone can send a node a signature to check, so the signature's
// size is checked here first.
func (mldsa65) Verify(publicKey, message, signature []byte) bool {
	if len(signature) != mldsa.MLDSA65SignatureSize {
		return false
	}
	if len(publicKey) == mldsa.MLDSA65PublicKeySize && anyoneCanSignUnder(publicKey) {
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

// Sign signs deterministically, as PrivateKey requires, with no context string.
func (key mldsa65Key) Sign(message []byte) ([]byte, error) {
	return key.privateKey.SignDeterministic(message, nil)
}
