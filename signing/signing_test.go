package signing

import (
	"bytes"
	"encoding/base64"
	"math"
	"strings"
	"testing"
	"time"
)

func mustSigner(t *testing.T, network string) *Signer {
	t.Helper()
	signer, err := Generate(MLDSA65{}, network)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return signer
}

func TestSignVerifyRoundTrip(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	signature, err := signer.Sign(DomainPayment, fields...)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(MLDSA65{}, "mainnet", signer.PublicKey(), DomainPayment, signature, fields...) {
		t.Fatal("a valid signature failed to verify")
	}
}

// The headline invariant (audit C1): a signature made in one domain must NOT verify in
// another. This is what stops a node's receipt signature from being replayed as a payment.
func TestCrossDomainSignatureRejected(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	signature, _ := signer.Sign(DomainReceipt, fields...) // signed as a receipt
	if Verify(MLDSA65{}, "mainnet", signer.PublicKey(), DomainPayment, signature, fields...) {
		t.Fatal("a receipt signature was accepted as a payment (cross-protocol reuse!)")
	}
}

// Audit C6: a signature made on one network must NOT verify on another (testnet<->mainnet /
// fork replay), even with the same key, domain and fields.
func TestCrossNetworkSignatureRejected(t *testing.T) {
	signer := mustSigner(t, "testnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	signature, _ := signer.Sign(DomainPayment, fields...)
	if Verify(MLDSA65{}, "mainnet", signer.PublicKey(), DomainPayment, signature, fields...) {
		t.Fatal("a testnet signature was accepted on mainnet (cross-network replay!)")
	}
}

// Audit C7: field boundaries must be unambiguous, so content cannot be shifted between fields
// (the ':'-join injection). A signature over ("alice","100") must not verify for a different
// split of the same bytes, nor for the joined form.
func TestFieldInjectionResisted(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, _ := signer.Sign(DomainPayment, []byte("alice"), []byte("100"))
	if Verify(MLDSA65{}, "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("alic"), []byte("e100")) {
		t.Fatal("re-split fields verified -- field boundaries are ambiguous")
	}
	if Verify(MLDSA65{}, "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("alice100")) {
		t.Fatal("joined fields verified against split signature -- injection possible")
	}
}

func TestTamperedFieldRejected(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, _ := signer.Sign(DomainPayment, []byte("alice"), []byte("100"), []byte("bob"))
	if Verify(MLDSA65{}, "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("alice"), []byte("1000"), []byte("bob")) {
		t.Fatal("a tampered amount verified")
	}
}

// Malformed keys/signatures must fail closed, never panic.
func TestMalformedInputsFailClosed(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, _ := signer.Sign(DomainPayment, []byte("x"))
	if Verify(MLDSA65{}, "mainnet", []byte("short-key"), DomainPayment, signature, []byte("x")) {
		t.Fatal("verification succeeded with a malformed public key")
	}
	if VerifyBase64(MLDSA65{}, "mainnet", "!!!not-base64!!!", DomainPayment, "!!!", []byte("x")) {
		t.Fatal("verification succeeded with malformed base64")
	}
}

// The base64 wire helpers must round-trip.
func TestBase64WireRoundTrip(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signatureBase64, err := signer.SignBase64(DomainReceipt, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyBase64(MLDSA65{}, "mainnet", signer.PublicKeyBase64(), DomainReceipt, signatureBase64, []byte("hello")) {
		t.Fatal("base64 round-trip failed to verify")
	}
}

// A key has one accepted spelling. StdEncoding also decodes three other spellings of a 1952-byte key
// (non-zero bits in the last character before the padding) and any with an embedded \r or \n, which
// Strict still accepts. Each verified as a separate identity of one key pair: a second validator seat
// whose votes counted apart from the first, so voting twice left no evidence.
func TestVerifyBase64AcceptsOnlyTheCanonicalKeySpelling(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signatureBase64, _ := signer.SignBase64(DomainBlockRevision, []byte("x"))
	key := signer.PublicKeyBase64()
	if !CanonicalBase64(key) || !VerifyBase64(MLDSA65{}, "mainnet", key, DomainBlockRevision, signatureBase64, []byte("x")) {
		t.Fatal("the canonical spelling must verify")
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	lastIndex := len(key) - 2 // the character carrying the key's final bits, before the one "="
	last := strings.IndexByte(alphabet, key[lastIndex])
	aliases := []string{key[:20] + "\n" + key[20:], key + "\r\n"}
	for bits := 1; bits < 4; bits++ {
		aliases = append(aliases, key[:lastIndex]+string(alphabet[last|bits])+key[lastIndex+1:])
	}
	for _, alias := range aliases {
		if decoded, err := base64.StdEncoding.DecodeString(alias); err != nil || !bytes.Equal(decoded, signer.PublicKey()) {
			t.Fatalf("setup: %q must decode to the same key", alias)
		}
		if CanonicalBase64(alias) || VerifyBase64(MLDSA65{}, "mainnet", alias, DomainBlockRevision, signatureBase64, []byte("x")) {
			t.Errorf("the alias spelling %q was accepted", alias)
		}
	}
}

// ValidPublicKeyBase64 is the shape check the ledger, the server config and the wallet share: a key of
// exactly 1952 bytes in its one spelling, PublicKeyBase64Length characters; a signature is
// SignatureBase64Length. The length alone is not enough: 2604 canonical characters also spell 1951 and
// 1953 bytes.
func TestValidPublicKeyBase64TakesOnlyAKeyInItsOneSpelling(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	key := signer.PublicKeyBase64()
	signatureBase64, _ := signer.SignBase64(DomainPayment, []byte("x"))
	if !ValidPublicKeyBase64(key) || len(key) != PublicKeyBase64Length || len(signatureBase64) != SignatureBase64Length {
		t.Fatalf("a %d-character key (valid: %v) and a %d-character signature, want %d and %d",
			len(key), ValidPublicKeyBase64(key), len(signatureBase64), PublicKeyBase64Length, SignatureBase64Length)
	}
	encode := base64.StdEncoding.EncodeToString
	for what, refused := range map[string]string{
		"an empty key":                "",
		"a key that is not base64":    strings.Repeat("!", PublicKeyBase64Length),
		"a 32-byte key":               encode(make([]byte, 32)),
		"a 1951-byte key":             encode(signer.PublicKey()[:1951]),
		"a 1953-byte key":             encode(append(bytes.Clone(signer.PublicKey()), 0)),
		"the key in another spelling": key[:20] + "\n" + key[20:],
	} {
		if ValidPublicKeyBase64(refused) {
			t.Errorf("%s (%d characters) passed for a public key", what, len(refused))
		}
	}
}

// A signature has one accepted spelling too. A 3309-byte signature fills its last base64 group, so
// its only other spellings carry an embedded \r or \n, which StdEncoding skips. A copy of an honest
// vote so spelled used to verify, at the cost of a full signature check, before the node found it
// was a copy; now it fails before any check.
func TestVerifyBase64AcceptsOnlyTheCanonicalSignatureSpelling(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signatureBase64, _ := signer.SignBase64(DomainBlockRevision, []byte("x"))
	key := signer.PublicKeyBase64()
	for _, alias := range []string{signatureBase64[:20] + "\n" + signatureBase64[20:], signatureBase64 + "\r\n"} {
		if decoded, _ := base64.StdEncoding.DecodeString(alias); base64.StdEncoding.EncodeToString(decoded) != signatureBase64 {
			t.Fatalf("setup: %q must decode to the same signature", alias)
		}
		if VerifyBase64(MLDSA65{}, "mainnet", key, DomainBlockRevision, alias, []byte("x")) {
			t.Errorf("the alias spelling %q of a signature was accepted", alias)
		}
	}
}

// Signing is deterministic: the round driver needs a signature to be a function of what is signed,
// so a relayed copy of a vote is the same bytes as the vote. Two signers from one seed agree too.
func TestSignaturesAreDeterministic(t *testing.T) {
	_, privateKey, err := MLDSA65{}.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewSigner(MLDSA65{}, "mainnet", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSigner(MLDSA65{}, "mainnet", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	fields := [][]byte{[]byte("1"), []byte("0"), []byte("block")}
	signature, _ := first.Sign(DomainBlockRevision, fields...)
	again, _ := first.Sign(DomainBlockRevision, fields...)
	fromSecond, _ := second.Sign(DomainBlockRevision, fields...)
	if !bytes.Equal(signature, again) || !bytes.Equal(signature, fromSecond) {
		t.Fatal("one key signed the same fields twice and got different signatures")
	}
	if other, _ := first.Sign(DomainBlockRevision, []byte("2"), []byte("0"), []byte("block")); bytes.Equal(signature, other) {
		t.Fatal("different fields got the same signature")
	}
}

// Verification takes a key and a signature of exactly ML-DSA-65's sizes, 1952 and 3309 bytes. Any
// other size fails and never panics: a copy of a genuine key or signature with a byte added or cut
// included, in raw form and on the wire.
func TestVerifyRefusesWrongKeyAndSignatureSizes(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, err := signer.Sign(DomainPayment, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	publicKey := signer.PublicKey()
	if len(publicKey) != 1952 || len(signature) != 3309 {
		t.Fatalf("a %d-byte key and a %d-byte signature, want 1952 and 3309", len(publicKey), len(signature))
	}
	if !Verify(MLDSA65{}, "mainnet", publicKey, DomainPayment, signature, []byte("x")) {
		t.Fatal("the genuine key and signature must verify")
	}
	resized := func(raw []byte, size int) []byte {
		copied := make([]byte, size)
		copy(copied, raw)
		return copied
	}
	encode := base64.StdEncoding.EncodeToString
	for _, size := range []int{0, 32, 1951, 1953, 2 * 1952} {
		key := resized(publicKey, size)
		if Verify(MLDSA65{}, "mainnet", key, DomainPayment, signature, []byte("x")) ||
			VerifyBase64(MLDSA65{}, "mainnet", encode(key), DomainPayment, encode(signature), []byte("x")) {
			t.Errorf("a %d-byte key verified", size)
		}
	}
	for _, size := range []int{0, 64, 3308, 3310, 2 * 3309} {
		resizedSignature := resized(signature, size)
		if Verify(MLDSA65{}, "mainnet", publicKey, DomainPayment, resizedSignature, []byte("x")) ||
			VerifyBase64(MLDSA65{}, "mainnet", encode(publicKey), DomainPayment, encode(resizedSignature), []byte("x")) {
			t.Errorf("a %d-byte signature verified", size)
		}
	}
}

// A key or a signature of the wrong size is refused before any work: a node verifies whatever anyone
// sends it. crypto/mldsa checks a key's size at once, but a signature's only after expanding the key,
// which costs over half a genuine check, so Verify checks the signature's size first. Each case keeps
// the fastest of five runs, so a busy machine cannot fail the test: the margin is thousands of times.
func TestVerifyRefusesWrongSizesBeforeAnyWork(t *testing.T) {
	_, seed, err := MLDSA65{}.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := MLDSA65{}.ParsePrivateKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("x")
	signature, err := privateKey.Sign(message)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.PublicKey()
	if !(MLDSA65{}).Verify(publicKey, message, signature) {
		t.Fatal("the genuine key and signature must verify")
	}
	fastest := func(publicKey, signature []byte) time.Duration {
		best := time.Duration(math.MaxInt64)
		for i := 0; i < 5; i++ {
			start := time.Now()
			for j := 0; j < 20; j++ {
				MLDSA65{}.Verify(publicKey, message, signature)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	genuine := fastest(publicKey, signature)
	for _, size := range []int{0, 3308, 3310} {
		if refused := fastest(publicKey, make([]byte, size)); refused > genuine/10 {
			t.Errorf("refusing a %d-byte signature took %v, a genuine check %v", size, refused, genuine)
		}
	}
	for _, size := range []int{0, 1951, 1953} {
		if refused := fastest(make([]byte, size), signature); refused > genuine/10 {
			t.Errorf("refusing a %d-byte key took %v, a genuine check %v", size, refused, genuine)
		}
	}
}

// A private key is stored as its 32-byte seed, the form keygen prints and the wallet keeps. Every
// other length is refused, a 64-byte Ed25519 private key of an earlier network among them.
func TestPrivateKeyIsA32ByteSeed(t *testing.T) {
	publicKey, privateKey, err := MLDSA65{}.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(privateKey) != 32 {
		t.Fatalf("GenerateKey returned a %d-byte private key, want the 32-byte seed", len(privateKey))
	}
	parsedKey, err := MLDSA65{}.ParsePrivateKey(privateKey)
	if err != nil || !bytes.Equal(parsedKey.PublicKey(), publicKey) {
		t.Fatalf("the seed does not parse back to its public key (%v)", err)
	}
	for _, size := range []int{0, 31, 33, 64} {
		if _, err := (MLDSA65{}).ParsePrivateKey(make([]byte, size)); err == nil {
			t.Errorf("a %d-byte private key was parsed", size)
		}
		if _, err := NewSigner(MLDSA65{}, "mainnet", make([]byte, size)); err == nil {
			t.Errorf("a signer was built from a %d-byte private key", size)
		}
	}
}

// A Signer takes only the private key and derives the public key from it, so no signer can name one
// key and sign with another (E26).
func TestSignerDerivesItsPublicKey(t *testing.T) {
	publicKey, privateKey, err := MLDSA65{}.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(MLDSA65{}, "mainnet", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signer.PublicKey(), publicKey) || signer.PublicKeyBase64() != base64.StdEncoding.EncodeToString(publicKey) {
		t.Fatal("the signer's public key is not the one its seed derives")
	}
	signature, err := signer.SignBase64(DomainReceipt, []byte("x"))
	if err != nil || !VerifyBase64(MLDSA65{}, "mainnet", signer.PublicKeyBase64(), DomainReceipt, signature, []byte("x")) {
		t.Fatalf("the signer's signature does not verify under its own public key (%v)", err)
	}
}
