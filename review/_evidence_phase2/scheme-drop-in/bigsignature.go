package signing

import (
	"bytes"
	"crypto/mldsa"
	"fmt"
)

// bigSignature is ML-DSA-65 with each signature padded with zeros to 20000 bytes, standing in for a
// scheme with SLH-DSA-sized signatures (17 to 50 KB). It is for the drop-in proof only: the padding
// adds no security.
type bigSignature struct{} // implements Scheme

var _ Scheme = bigSignature{}

const bigSignatureSize = 20000

func NetworkScheme() Scheme { return bigSignature{} }

func (bigSignature) Name() string { return "big" }

func (bigSignature) SignatureSize() int { return bigSignatureSize }

// ValidatePublicKey is ML-DSA-65's: the size, then crypto/mldsa.NewPublicKey.
func (bigSignature) ValidatePublicKey(publicKey []byte) error {
	if len(publicKey) != mldsa.MLDSA65PublicKeySize {
		return fmt.Errorf("a public key is %d bytes, not %d", mldsa.MLDSA65PublicKeySize, len(publicKey))
	}
	_, err := mldsa.NewPublicKey(mldsa.MLDSA65(), publicKey)
	return err
}

func (bigSignature) GenerateKey() (publicKey, privateKey []byte, err error) {
	key, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		return nil, nil, err
	}
	return key.PublicKey().Bytes(), key.Bytes(), nil
}

func (bigSignature) ParsePrivateKey(privateKey []byte) (PrivateKey, error) {
	key, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), privateKey)
	if err != nil {
		return nil, err
	}
	return bigKey{privateKey: key, publicKey: key.PublicKey().Bytes()}, nil
}

func (bigSignature) Verify(publicKey, message, signature []byte) bool {
	if len(signature) != bigSignatureSize || len(publicKey) != mldsa.MLDSA65PublicKeySize {
		return false
	}
	if !bytes.Equal(signature[mldsa.MLDSA65SignatureSize:], make([]byte, bigSignatureSize-mldsa.MLDSA65SignatureSize)) {
		return false
	}
	key, err := mldsa.NewPublicKey(mldsa.MLDSA65(), publicKey)
	return err == nil && mldsa.Verify(key, message, signature[:mldsa.MLDSA65SignatureSize], nil) == nil
}

type bigKey struct { // implements PrivateKey
	privateKey *mldsa.PrivateKey
	publicKey  []byte
}

var _ PrivateKey = bigKey{}

func (key bigKey) PublicKey() []byte { return key.publicKey }

func (key bigKey) Sign(message []byte) ([]byte, error) {
	signature, err := key.privateKey.SignDeterministic(message, nil)
	if err != nil {
		return nil, err
	}
	return append(signature, make([]byte, bigSignatureSize-len(signature))...), nil
}
