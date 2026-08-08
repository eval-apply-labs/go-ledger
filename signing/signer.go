package signing

import "encoding/base64"

// Signer holds a key pair bound to a network and produces domain-separated signatures. It
// deliberately exposes no "sign these raw bytes" method: every signature is scoped to a
// Domain, which is exactly what makes cross-protocol reuse impossible.
type Signer struct {
	scheme    Scheme
	networkID string
	pub       []byte
	priv      []byte
}

// NewSigner builds a Signer for an existing key pair.
func NewSigner(scheme Scheme, networkID string, pub, priv []byte) *Signer {
	return &Signer{scheme: scheme, networkID: networkID, pub: pub, priv: priv}
}

// Generate creates a fresh key pair and returns a Signer for it. Keys are meant to be created
// and held by their owner (the audit flagged generating keys server-side and
// returning the private key over HTTP -- do not do that; this is for client/local use).
func Generate(scheme Scheme, networkID string) (*Signer, error) {
	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, err
	}
	return &Signer{scheme: scheme, networkID: networkID, pub: pub, priv: priv}, nil
}

// PublicKey returns the signer's public key bytes.
func (s *Signer) PublicKey() []byte { return s.pub }

// PublicKeyBase64 returns the public key in base64 wire form.
func (s *Signer) PublicKeyBase64() string { return base64.StdEncoding.EncodeToString(s.pub) }

// Sign produces a signature over the canonical (networkID, domain, fields) preimage.
func (s *Signer) Sign(domain Domain, fields ...[]byte) ([]byte, error) {
	return s.scheme.Sign(s.priv, Preimage(s.networkID, domain, fields...))
}

// SignBase64 is Sign with the signature returned in base64 wire form.
func (s *Signer) SignBase64(domain Domain, fields ...[]byte) (string, error) {
	sig, err := s.Sign(domain, fields...)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// Verify reports whether sig is a valid signature over (networkID, domain, fields) by pub. It
// returns false for every kind of mismatch -- wrong domain, wrong network, tampered or
// re-split field, or a bad signature -- with no way to tell them apart (uniform failure).
func Verify(scheme Scheme, networkID string, pub []byte, domain Domain, sig []byte, fields ...[]byte) bool {
	return scheme.Verify(pub, Preimage(networkID, domain, fields...), sig)
}

// VerifyBase64 is Verify with base64-encoded public key and signature (wire form). It returns
// false on malformed base64.
func VerifyBase64(scheme Scheme, networkID, pubB64 string, domain Domain, sigB64 string, fields ...[]byte) bool {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return Verify(scheme, networkID, pub, domain, sig, fields...)
}
