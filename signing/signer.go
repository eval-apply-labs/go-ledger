package signing

import "encoding/base64"

// Signer holds a key pair bound to a network and produces domain-separated signatures. It
// deliberately exposes no "sign these raw bytes" method: every signature is scoped to a
// Domain, which is exactly what makes cross-protocol reuse impossible.
type Signer struct {
	networkID  string
	privateKey PrivateKey
}

// NewSigner builds a Signer for a stored private key. It derives the public key from the private
// key, so the two cannot disagree.
func NewSigner(scheme Scheme, networkID string, privateKey []byte) (*Signer, error) {
	parsedKey, err := scheme.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	return &Signer{networkID: networkID, privateKey: parsedKey}, nil
}

// Generate creates a fresh key pair and returns a Signer for it. Tests use it; keygen, the demo and
// the wallet call Scheme.GenerateKey instead. Keys are meant to be created and held by their owner
// (the audit flagged generating keys server-side and returning the private key over HTTP).
func Generate(scheme Scheme, networkID string) (*Signer, error) {
	_, privateKey, err := scheme.GenerateKey()
	if err != nil {
		return nil, err
	}
	return NewSigner(scheme, networkID, privateKey)
}

// PublicKey returns the signer's public key bytes.
func (signer *Signer) PublicKey() []byte { return signer.privateKey.PublicKey() }

// PublicKeyBase64 returns the public key in base64 wire form.
func (signer *Signer) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(signer.PublicKey())
}

// Sign produces a signature over the canonical (networkID, domain, fields) preimage.
func (signer *Signer) Sign(domain Domain, fields ...[]byte) ([]byte, error) {
	return signer.privateKey.Sign(Preimage(signer.networkID, domain, fields...))
}

// SignBase64 is Sign with the signature returned in base64 wire form.
func (signer *Signer) SignBase64(domain Domain, fields ...[]byte) (string, error) {
	signature, err := signer.Sign(domain, fields...)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// Verify reports whether signature is a valid signature over (networkID, domain, fields) by publicKey.
// It returns false for every kind of mismatch -- wrong domain, wrong network, tampered or
// re-split field, or a bad signature -- with no way to tell them apart (uniform failure).
func Verify(scheme Scheme, networkID string, publicKey []byte, domain Domain, signature []byte, fields ...[]byte) bool {
	return scheme.Verify(publicKey, Preimage(networkID, domain, fields...), signature)
}

// VerifyBase64 is Verify with base64-encoded public key and signature (wire form). It returns
// false on malformed base64, and for a key or a signature in any spelling but its canonical one
// (CanonicalBase64), so every signing identity, and every signature, has exactly one spelling.
func VerifyBase64(scheme Scheme, networkID, publicKeyBase64 string, domain Domain, signatureBase64 string, fields ...[]byte) bool {
	if !CanonicalBase64(publicKeyBase64) || !CanonicalBase64(signatureBase64) {
		return false
	}
	publicKey, _ := base64.StdEncoding.DecodeString(publicKeyBase64) // canonical, so both decode
	signature, _ := base64.StdEncoding.DecodeString(signatureBase64)
	return Verify(scheme, networkID, publicKey, domain, signature, fields...)
}

// CanonicalBase64 reports whether encoded is the standard base64 encoding of the bytes it decodes to.
// base64.StdEncoding also decodes other spellings of the same bytes (non-zero trailing bits, and
// embedded \r or \n, which even its Strict mode accepts), and the ledger compares keys as strings, so
// a key in another spelling would pass for a second key of one key pair.
func CanonicalBase64(encoded string) bool {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil && base64.StdEncoding.EncodeToString(raw) == encoded
}
