package signing

import (
	"crypto/mldsa"
	"errors"
	"fmt"
)

// mldsa87 is ML-DSA-87 (FIPS 204, NIST security level 5), the network's only signature scheme,
// from the standard library's crypto/mldsa. A private key is stored as its 32-byte seed. A public key
// is 2592 bytes and a signature 4627 bytes. Every node must verify exactly as every other does, so
// the scheme is fixed at genesis. The type is unexported, so no package outside signing can name it:
// the files named mldsa87*.go are all that change with the scheme.
type mldsa87 struct{} // implements Scheme

// Go has no implements keyword: a type implements an interface by having its methods. This line is
// the nearest thing to one. It does not build unless mldsa87 has every method of Scheme, with the
// same signature, so the comment above can never go stale. The underscore discards the value.
var _ Scheme = mldsa87{}

// NetworkScheme is the network's signature scheme, and the one place that chooses it. To change the
// scheme, replace the files named mldsa87*.go with files that implement Scheme and define
// NetworkScheme, then start a new network (README, Cryptographic agility).
func NetworkScheme() Scheme { return mldsa87{} }

func (mldsa87) Name() string { return "ML-DSA-87" }

func (mldsa87) SignatureSize() int { return mldsa.MLDSA87SignatureSize }

// ValidatePublicKey refuses a key of any size but 2592 bytes, and the most degenerate of the weak keys
// anyone can sign under: those whose t1 has every coefficient 0 or 1023, as for ML-DSA-65 (see
// mldsa65.go there). For them, c·t1·2^13 has coefficients of at most τ = 60 for every challenge c, far
// below γ2 = 261888, so a signature with z = 0 and no hint verifies for every message. It is a sanity
// check, not a guarantee: other keys with small t1 coefficients let anyone sign many or all messages,
// and pass, for the reasons mldsa65.go gives. Every other 2592 bytes are the encoding of exactly one
// key, which crypto/mldsa.NewPublicKey accepts.
func (mldsa87) ValidatePublicKey(publicKey []byte) error {
	if len(publicKey) != mldsa.MLDSA87PublicKeySize {
		return fmt.Errorf("a public key is %d bytes, not %d", mldsa.MLDSA87PublicKeySize, len(publicKey))
	}
	if anyoneCanSignUnder(publicKey) {
		return errors.New("a weak key: anyone can sign under it (every coefficient of its t1 is 0 or 1023)")
	}
	_, err := mldsa.NewPublicKey(mldsa.MLDSA87(), publicKey)
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
func (mldsa87) GenerateKey() (publicKey, privateKey []byte, err error) {
	key, err := mldsa.GenerateKey(mldsa.MLDSA87())
	if err != nil {
		return nil, nil, err
	}
	return key.PublicKey().Bytes(), key.Bytes(), nil
}

// ParsePrivateKey derives the key from a 32-byte seed, and refuses any other length.
func (mldsa87) ParsePrivateKey(privateKey []byte) (PrivateKey, error) {
	key, err := mldsa.NewPrivateKey(mldsa.MLDSA87(), privateKey)
	if err != nil {
		return nil, err
	}
	return mldsa87Key{privateKey: key, publicKey: key.PublicKey().Bytes()}, nil
}

// Verify reports whether signature verifies under publicKey. It refuses a key or a signature of any
// size but the exact one, 2592 and 4627 bytes, and never panics. It refuses the weak keys
// ValidatePublicKey refuses, and reads the others with crypto/mldsa.NewPublicKey, as ValidatePublicKey
// does, which refuses a key of the wrong size at once, so Verify refuses every key ValidatePublicKey
// refuses. crypto/mldsa refuses a signature of the wrong
// size only after expanding the key, which costs over half a genuine check. Anyone can send a node a
// signature to check, so the signature's size is checked here first.
func (mldsa87) Verify(publicKey, message, signature []byte) bool {
	if len(signature) != mldsa.MLDSA87SignatureSize {
		return false
	}
	if len(publicKey) == mldsa.MLDSA87PublicKeySize && anyoneCanSignUnder(publicKey) {
		return false
	}
	key, err := mldsa.NewPublicKey(mldsa.MLDSA87(), publicKey)
	return err == nil && mldsa.Verify(key, message, signature, nil) == nil
}

// mldsa87Key is a private key derived from its seed once: the derivation costs about 40% of a
// signature.
type mldsa87Key struct { // implements PrivateKey
	privateKey *mldsa.PrivateKey
	publicKey  []byte
}

var _ PrivateKey = mldsa87Key{} // the compiler checks that mldsa87Key implements PrivateKey

func (key mldsa87Key) PublicKey() []byte { return key.publicKey }

// Sign signs deterministically, as PrivateKey requires, with no context string.
func (key mldsa87Key) Sign(message []byte) ([]byte, error) {
	return key.privateKey.SignDeterministic(message, nil)
}
