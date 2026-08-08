package signing

import "testing"

func mustSigner(t *testing.T, network string) *Signer {
	t.Helper()
	s, err := Generate(Ed25519{}, network)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return s
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s := mustSigner(t, "mainnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	sig, err := s.Sign(DomainPayment, fields...)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(Ed25519{}, "mainnet", s.PublicKey(), DomainPayment, sig, fields...) {
		t.Fatal("a valid signature failed to verify")
	}
}

// The headline invariant (audit C1): a signature made in one domain must NOT verify in
// another. This is what stops a coerced server-auth signature from being replayed as a payment.
func TestCrossDomainSignatureRejected(t *testing.T) {
	s := mustSigner(t, "mainnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	sig, _ := s.Sign(DomainServerAuth, fields...) // signed as a server-auth challenge
	if Verify(Ed25519{}, "mainnet", s.PublicKey(), DomainPayment, sig, fields...) {
		t.Fatal("a server-auth signature was accepted as a payment (cross-protocol reuse!)")
	}
}

// Audit C6: a signature made on one network must NOT verify on another (testnet<->mainnet /
// fork replay), even with the same key, domain and fields.
func TestCrossNetworkSignatureRejected(t *testing.T) {
	s := mustSigner(t, "testnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	sig, _ := s.Sign(DomainPayment, fields...)
	if Verify(Ed25519{}, "mainnet", s.PublicKey(), DomainPayment, sig, fields...) {
		t.Fatal("a testnet signature was accepted on mainnet (cross-network replay!)")
	}
}

// Audit C7: field boundaries must be unambiguous, so content cannot be shifted between fields
// (the ':'-join injection). A signature over ("alice","100") must not verify for a different
// split of the same bytes, nor for the joined form.
func TestFieldInjectionResisted(t *testing.T) {
	s := mustSigner(t, "mainnet")
	sig, _ := s.Sign(DomainPayment, []byte("alice"), []byte("100"))
	if Verify(Ed25519{}, "mainnet", s.PublicKey(), DomainPayment, sig, []byte("alic"), []byte("e100")) {
		t.Fatal("re-split fields verified -- field boundaries are ambiguous")
	}
	if Verify(Ed25519{}, "mainnet", s.PublicKey(), DomainPayment, sig, []byte("alice100")) {
		t.Fatal("joined fields verified against split signature -- injection possible")
	}
}

func TestTamperedFieldRejected(t *testing.T) {
	s := mustSigner(t, "mainnet")
	sig, _ := s.Sign(DomainPayment, []byte("alice"), []byte("100"), []byte("bob"))
	if Verify(Ed25519{}, "mainnet", s.PublicKey(), DomainPayment, sig, []byte("alice"), []byte("1000"), []byte("bob")) {
		t.Fatal("a tampered amount verified")
	}
}

// Malformed keys/signatures must fail closed, never panic.
func TestMalformedInputsFailClosed(t *testing.T) {
	s := mustSigner(t, "mainnet")
	sig, _ := s.Sign(DomainPayment, []byte("x"))
	if Verify(Ed25519{}, "mainnet", []byte("short-key"), DomainPayment, sig, []byte("x")) {
		t.Fatal("verification succeeded with a malformed public key")
	}
	if VerifyBase64(Ed25519{}, "mainnet", "!!!not-base64!!!", DomainPayment, "!!!", []byte("x")) {
		t.Fatal("verification succeeded with malformed base64")
	}
}

// The base64 wire helpers must round-trip.
func TestBase64WireRoundTrip(t *testing.T) {
	s := mustSigner(t, "mainnet")
	sigB64, err := s.SignBase64(DomainReceipt, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyBase64(Ed25519{}, "mainnet", s.PublicKeyBase64(), DomainReceipt, sigB64, []byte("hello")) {
		t.Fatal("base64 round-trip failed to verify")
	}
}
