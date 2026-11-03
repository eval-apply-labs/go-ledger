package signing

import (
	"bytes"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustSigner(t *testing.T, network string) Signer {
	t.Helper()
	signer, err := Generate(NetworkScheme(), network)
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
	if !Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, fields...) {
		t.Fatal("a valid signature failed to verify")
	}
}

// The headline invariant (audit C1): a signature made in one domain must NOT verify in
// another. This is what stops a node's receipt signature from being replayed as a payment.
func TestCrossDomainSignatureRejected(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	signature, _ := signer.Sign(DomainReceipt, fields...) // signed as a receipt
	if Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, fields...) {
		t.Fatal("a receipt signature was accepted as a payment (cross-protocol reuse!)")
	}
}

// Audit C6: a signature made on one network must NOT verify on another (testnet<->mainnet /
// fork replay), even with the same key, domain and fields.
func TestCrossNetworkSignatureRejected(t *testing.T) {
	signer := mustSigner(t, "testnet")
	fields := [][]byte{[]byte("alice"), []byte("100"), []byte("bob")}
	signature, _ := signer.Sign(DomainPayment, fields...)
	if Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, fields...) {
		t.Fatal("a testnet signature was accepted on mainnet (cross-network replay!)")
	}
}

// Audit C7: field boundaries must be unambiguous, so content cannot be shifted between fields
// (the ':'-join injection). A signature over ("alice","100") must not verify for a different
// split of the same bytes, nor for the joined form.
func TestFieldInjectionResisted(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, _ := signer.Sign(DomainPayment, []byte("alice"), []byte("100"))
	if Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("alic"), []byte("e100")) {
		t.Fatal("re-split fields verified -- field boundaries are ambiguous")
	}
	if Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("alice100")) {
		t.Fatal("joined fields verified against split signature -- injection possible")
	}
}

func TestTamperedFieldRejected(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, _ := signer.Sign(DomainPayment, []byte("alice"), []byte("100"), []byte("bob"))
	if Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("alice"), []byte("1000"), []byte("bob")) {
		t.Fatal("a tampered amount verified")
	}
}

// Malformed keys/signatures must fail closed, never panic.
func TestMalformedInputsFailClosed(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signature, _ := signer.Sign(DomainPayment, []byte("x"))
	if Verify(NetworkScheme(), "mainnet", []byte("short-key"), DomainPayment, signature, []byte("x")) {
		t.Fatal("verification succeeded with a malformed public key")
	}
	if VerifyBase64(NetworkScheme(), "mainnet", "!!!not-base64!!!", DomainPayment, "!!!", []byte("x")) {
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
	if !VerifyBase64(NetworkScheme(), "mainnet", signer.PublicKeyBase64(), DomainReceipt, signatureBase64, []byte("hello")) {
		t.Fatal("base64 round-trip failed to verify")
	}
}

// A key has one accepted spelling. StdEncoding also decodes any spelling with an embedded \r or \n,
// which Strict still accepts, and, for a key whose size is not a multiple of three, those with non-zero
// unused bits in the last character before the padding. Each verified as a separate identity of one key
// pair: a second validator seat whose votes counted apart from the first, so voting twice left no
// evidence.
func TestVerifyBase64AcceptsOnlyTheCanonicalKeySpelling(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signatureBase64, _ := signer.SignBase64(DomainBlockRevision, []byte("x"))
	key := signer.PublicKeyBase64()
	if !CanonicalBase64(key) || !VerifyBase64(NetworkScheme(), "mainnet", key, DomainBlockRevision, signatureBase64, []byte("x")) {
		t.Fatal("the canonical spelling must verify")
	}
	for _, alias := range otherSpellings(key) {
		if decoded, err := base64.StdEncoding.DecodeString(alias); err != nil || !bytes.Equal(decoded, signer.PublicKey()) {
			t.Fatalf("setup: %q must decode to the same key", alias)
		}
		if CanonicalBase64(alias) || VerifyBase64(NetworkScheme(), "mainnet", alias, DomainBlockRevision, signatureBase64, []byte("x")) {
			t.Errorf("the alias spelling %q was accepted", alias)
		}
	}
}

// ValidatePublicKeyBase64 is the check every key that enters the ledger passes: the genesis, the server
// config, the wallet and the new key of a name request or a ⅔ name action. It takes the scheme's own
// keys, in their one spelling, and refuses the rest, with the scheme's reason when the bytes are the
// scheme's to refuse: a scheme that refuses a key (keyRefusingScheme) has it refused here too, and a key
// a byte short is refused for the reason the scheme gives. A signature's base64 is
// SignatureBase64Length characters.
func TestValidatePublicKeyBase64TakesOnlyAKeyTheSchemeAcceptsInItsOneSpelling(t *testing.T) {
	scheme := NetworkScheme()
	signer := mustSigner(t, "mainnet")
	key, publicKey := signer.PublicKeyBase64(), signer.PublicKey()
	signatureBase64, _ := signer.SignBase64(DomainPayment, []byte("x"))
	if len(signatureBase64) != SignatureBase64Length(scheme) {
		t.Fatalf("a %d-character signature, want %d", len(signatureBase64), SignatureBase64Length(scheme))
	}
	for range 20 {
		if key := mustSigner(t, "mainnet").PublicKeyBase64(); ValidatePublicKeyBase64(scheme, key) != nil {
			t.Fatalf("a genuine key was refused: %v", ValidatePublicKeyBase64(scheme, key))
		}
	}
	encode := base64.StdEncoding.EncodeToString
	for what, refused := range map[string]string{
		"an empty key":                "",
		"a key that is not base64":    strings.Repeat("!", len(key)),
		"half a key":                  encode(publicKey[:len(publicKey)/2]),
		"a key a byte short":          encode(publicKey[:len(publicKey)-1]),
		"a key a byte long":           encode(append(bytes.Clone(publicKey), 0)),
		"the key in another spelling": key[:20] + "\n" + key[20:],
	} {
		if ValidatePublicKeyBase64(scheme, refused) == nil {
			t.Errorf("%s (%d characters) passed for a public key", what, len(refused))
		}
	}
	reason := scheme.ValidatePublicKey(publicKey[:len(publicKey)-1])
	if err := ValidatePublicKeyBase64(scheme, encode(publicKey[:len(publicKey)-1])); reason == nil || err == nil || err.Error() != reason.Error() {
		t.Errorf("a key a byte short was refused with %v, want the scheme's reason, %v", err, reason)
	}
	if err := ValidatePublicKeyBase64(keyRefusingScheme{scheme}, key); err == nil || err.Error() != "refused by the scheme" {
		t.Errorf("a key its scheme refuses was refused with %v, want the scheme's reason", err)
	}
}

// keyRefusingScheme is the network's scheme with a ValidatePublicKey that refuses every key.
type keyRefusingScheme struct{ Scheme } // implements Scheme: the network scheme's methods, with ValidatePublicKey replaced

func (keyRefusingScheme) ValidatePublicKey([]byte) error { return errors.New("refused by the scheme") }

// otherSpellings returns the other strings standard base64 decodes to the same bytes as the canonical
// encoded: with an embedded \r or \n, which StdEncoding skips even in its Strict mode, and, when encoded
// ends in padding, with non-zero unused bits in the character before it, two per "=".
func otherSpellings(encoded string) []string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	spellings := []string{encoded[:20] + "\n" + encoded[20:], encoded + "\r\n"}
	if padding := len(encoded) - len(strings.TrimRight(encoded, "=")); padding > 0 {
		lastIndex := len(encoded) - padding - 1
		last := strings.IndexByte(alphabet, encoded[lastIndex])
		for bits := 1; bits < 1<<(2*padding); bits++ {
			spellings = append(spellings, encoded[:lastIndex]+string(alphabet[last|bits])+encoded[lastIndex+1:])
		}
	}
	return spellings
}

// A signature has one accepted spelling too. A copy of an honest vote in another spelling used to
// verify, at the cost of a full signature check, before the node found it was a copy; now it fails
// before any check.
func TestVerifyBase64AcceptsOnlyTheCanonicalSignatureSpelling(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	signatureBase64, _ := signer.SignBase64(DomainBlockRevision, []byte("x"))
	key := signer.PublicKeyBase64()
	for _, alias := range otherSpellings(signatureBase64) {
		if decoded, _ := base64.StdEncoding.DecodeString(alias); base64.StdEncoding.EncodeToString(decoded) != signatureBase64 {
			t.Fatalf("setup: %q must decode to the same signature", alias)
		}
		if VerifyBase64(NetworkScheme(), "mainnet", key, DomainBlockRevision, alias, []byte("x")) {
			t.Errorf("the alias spelling %q of a signature was accepted", alias)
		}
	}
}

// Signing is deterministic: the round driver needs a signature to be a function of what is signed,
// so a relayed copy of a vote is the same bytes as the vote. Two signers from one seed agree too.
func TestSignaturesAreDeterministic(t *testing.T) {
	_, privateKey, err := NetworkScheme().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewSigner(NetworkScheme(), "mainnet", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSigner(NetworkScheme(), "mainnet", privateKey)
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

// Verification takes a key and a signature of exactly the scheme's sizes. Any other size fails and
// never panics: a copy of a genuine key or signature with a byte added or cut included, in raw form and
// on the wire. ValidatePublicKey refuses each of those keys too, with a reason, and takes the genuine one.
func TestVerifyRefusesWrongKeyAndSignatureSizes(t *testing.T) {
	scheme := NetworkScheme()
	signer := mustSigner(t, "mainnet")
	signature, err := signer.Sign(DomainPayment, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	publicKey := signer.PublicKey()
	keySize, signatureSize := len(publicKey), scheme.SignatureSize()
	if len(signature) != signatureSize {
		t.Fatalf("a %d-byte signature, want %d", len(signature), signatureSize)
	}
	if !Verify(NetworkScheme(), "mainnet", publicKey, DomainPayment, signature, []byte("x")) || scheme.ValidatePublicKey(publicKey) != nil {
		t.Fatalf("the genuine key and signature must verify, and the key pass ValidatePublicKey (%v)", scheme.ValidatePublicKey(publicKey))
	}
	resized := func(raw []byte, size int) []byte {
		copied := make([]byte, size)
		copy(copied, raw)
		return copied
	}
	encode := base64.StdEncoding.EncodeToString
	for _, size := range []int{0, keySize / 2, keySize - 1, keySize + 1, 2 * keySize} {
		key := resized(publicKey, size)
		if Verify(NetworkScheme(), "mainnet", key, DomainPayment, signature, []byte("x")) ||
			VerifyBase64(NetworkScheme(), "mainnet", encode(key), DomainPayment, encode(signature), []byte("x")) {
			t.Errorf("a %d-byte key verified", size)
		}
		if err := scheme.ValidatePublicKey(key); err == nil || err.Error() == "" {
			t.Errorf("a %d-byte key passed ValidatePublicKey, or was refused with no reason (%v)", size, err)
		}
	}
	if err := scheme.ValidatePublicKey(nil); err == nil {
		t.Error("no key at all passed ValidatePublicKey")
	}
	for _, size := range []int{0, signatureSize / 2, signatureSize - 1, signatureSize + 1, 2 * signatureSize} {
		resizedSignature := resized(signature, size)
		if Verify(NetworkScheme(), "mainnet", publicKey, DomainPayment, resizedSignature, []byte("x")) ||
			VerifyBase64(NetworkScheme(), "mainnet", encode(publicKey), DomainPayment, encode(resizedSignature), []byte("x")) {
			t.Errorf("a %d-byte signature verified", size)
		}
	}
}

// A key or a signature of the wrong size is refused before any work, as Scheme.Verify requires: a node
// verifies whatever anyone sends it. Each case keeps the fastest of five runs, so a busy machine cannot
// fail the test: the margin is thousands of times.
func TestVerifyRefusesWrongSizesBeforeAnyWork(t *testing.T) {
	scheme := NetworkScheme()
	publicKey, seed, err := scheme.GenerateKey()
	keySize, signatureSize := len(publicKey), scheme.SignatureSize()
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := scheme.ParsePrivateKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("x")
	signature, err := privateKey.Sign(message)
	if err != nil {
		t.Fatal(err)
	}
	if !scheme.Verify(publicKey, message, signature) {
		t.Fatal("the genuine key and signature must verify")
	}
	fastest := func(publicKey, signature []byte) time.Duration {
		best := time.Duration(math.MaxInt64)
		for i := 0; i < 5; i++ {
			start := time.Now()
			for j := 0; j < 20; j++ {
				scheme.Verify(publicKey, message, signature)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	genuine := fastest(publicKey, signature)
	for _, size := range []int{0, signatureSize - 1, signatureSize + 1} {
		if refused := fastest(publicKey, make([]byte, size)); refused > genuine/10 {
			t.Errorf("refusing a %d-byte signature took %v, a genuine check %v", size, refused, genuine)
		}
	}
	for _, size := range []int{0, keySize - 1, keySize + 1} {
		if refused := fastest(make([]byte, size), signature); refused > genuine/10 {
			t.Errorf("refusing a %d-byte key took %v, a genuine check %v", size, refused, genuine)
		}
	}
}

// ParsePrivateKey refuses a stored private key of another length, empty, a byte short or a byte long,
// with an error and without panicking, as Scheme requires.
func TestParsePrivateKeyRefusesOtherLengths(t *testing.T) {
	scheme := NetworkScheme()
	_, privateKey, err := scheme.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range [][]byte{nil, privateKey[:len(privateKey)-1], append(bytes.Clone(privateKey), 0)} {
		if _, err := scheme.ParsePrivateKey(stored); err == nil {
			t.Errorf("a %d-byte private key was parsed; the scheme stores %d bytes", len(stored), len(privateKey))
		}
	}
}

// A scheme is used from many goroutines at once, as Scheme requires: the node signs receipts on its HTTP
// handlers while its loop signs votes, and checks signatures off the loop. Under -race, this finds a
// scheme or a parsed key that is not safe for that.
func TestSchemeIsSafeForConcurrentUse(t *testing.T) {
	signer := mustSigner(t, "mainnet")
	var group sync.WaitGroup
	for i := range 8 {
		group.Go(func() {
			message := []byte{byte(i)}
			signature, err := signer.Sign(DomainPayment, message)
			if err != nil || !Verify(NetworkScheme(), "mainnet", signer.PublicKey(), DomainPayment, signature, message) {
				t.Errorf("a signature made and checked alongside others failed (%v)", err)
			}
		})
	}
	group.Wait()
}

// A Signer takes only the private key and derives the public key from it, so no signer can name one
// key and sign with another (E26).
func TestSignerDerivesItsPublicKey(t *testing.T) {
	publicKey, privateKey, err := NetworkScheme().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(NetworkScheme(), "mainnet", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signer.PublicKey(), publicKey) || signer.PublicKeyBase64() != base64.StdEncoding.EncodeToString(publicKey) {
		t.Fatal("the signer's public key is not the one its seed derives")
	}
	signature, err := signer.SignBase64(DomainReceipt, []byte("x"))
	if err != nil || !VerifyBase64(NetworkScheme(), "mainnet", signer.PublicKeyBase64(), DomainReceipt, signature, []byte("x")) {
		t.Fatalf("the signer's signature does not verify under its own public key (%v)", err)
	}
}
