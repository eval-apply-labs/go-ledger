package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
)

// Ed25519 is the default signature scheme: standard-library, small fixed-size keys and
// signatures, fast, and free of the padding / hash-then-sign ambiguities of RSA. The audit
// noted the signing scheme itself was never the problem -- what mattered was what got signed
// -- so we choose the simplest safe primitive and put all the care into domain separation.
type Ed25519 struct{}

func (Ed25519) Name() string { return "ed25519" }

func (Ed25519) GenerateKey() (pub, priv []byte, err error) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return pk, sk, nil
}

func (Ed25519) Sign(priv, message []byte) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("signing: invalid ed25519 private key size")
	}
	return ed25519.Sign(ed25519.PrivateKey(priv), message), nil
}

func (Ed25519) Verify(pub, message, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), message, sig)
}
