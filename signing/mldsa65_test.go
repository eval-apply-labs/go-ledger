package signing

import (
	"bytes"
	"crypto/mldsa"
	"crypto/sha256"
	"crypto/sha3"
	"fmt"
	"testing"
)

// The facts of ML-DSA-65 the rest of the code takes from the Scheme: its name, and its sizes in bytes
// and in base64. Keys and signatures of an earlier network's Ed25519 sizes (a 32-byte key, a 64-byte
// signature) are refused.
func TestMLDSA65Facts(t *testing.T) {
	scheme := NetworkScheme()
	if scheme != Scheme(mldsa65{}) || scheme.Name() != "ML-DSA-65" {
		t.Fatalf("the network's scheme is %T named %q, want ML-DSA-65", scheme, scheme.Name())
	}
	if scheme.SignatureSize() != 3309 || SignatureBase64Length(scheme) != 4412 {
		t.Fatalf("a %d-byte signature (%d characters), want 3309 (4412)", scheme.SignatureSize(), SignatureBase64Length(scheme))
	}
	signer, err := Generate(scheme, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signer.Sign(DomainPayment, []byte("x"))
	if err != nil || len(signer.PublicKey()) != 1952 || len(signer.PublicKeyBase64()) != 2604 || len(signature) != 3309 {
		t.Fatalf("signed with a %d-byte key (%d characters), giving a %d-byte signature (%v), want 1952 (2604) and 3309",
			len(signer.PublicKey()), len(signer.PublicKeyBase64()), len(signature), err)
	}
	if Verify(scheme, "mainnet", signer.PublicKey()[:32], DomainPayment, signature, []byte("x")) ||
		Verify(scheme, "mainnet", signer.PublicKey(), DomainPayment, signature[:64], []byte("x")) {
		t.Fatal("a key or a signature of Ed25519's size verified")
	}
}

// ValidatePublicKey takes exactly the 1952-byte keys and says why it refuses another size: the sizes of
// ML-DSA-44 and ML-DSA-87 keys, and of an earlier network's Ed25519 key, among them. ML-DSA-65 has no
// second encoding of a key; the weak keys it refuses (all zeros and all ones below among them) are the
// subject of TestMLDSA65RefusesKeysAnyoneCanSignUnder.
func TestMLDSA65ValidatePublicKey(t *testing.T) {
	scheme := NetworkScheme()
	signer, err := Generate(scheme, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	signature, _ := signer.Sign(DomainPayment, []byte("x"))
	for _, size := range []int{0, 32, 1312, 1951, 1953, 2592} {
		err := scheme.ValidatePublicKey(make([]byte, size))
		if want := fmt.Sprintf("a public key is 1952 bytes, not %d", size); err == nil || err.Error() != want {
			t.Errorf("a %d-byte key: %v, want %q", size, err, want)
		}
		if Verify(scheme, "mainnet", make([]byte, size), DomainPayment, signature, []byte("x")) {
			t.Errorf("a %d-byte key verified", size)
		}
	}
	if err := scheme.ValidatePublicKey(signer.PublicKey()); err != nil || !Verify(scheme, "mainnet", signer.PublicKey(), DomainPayment, signature, []byte("x")) {
		t.Errorf("a genuine key was refused (%v)", err)
	}
	for what, publicKey := range map[string][]byte{"all zeros": make([]byte, 1952), "all ones": bytes.Repeat([]byte{0xff}, 1952)} {
		if err := scheme.ValidatePublicKey(publicKey); err == nil || Verify(scheme, "mainnet", publicKey, DomainPayment, signature, []byte("x")) {
			t.Errorf("%s, a weak key, was accepted (%v)", what, err)
		}
	}
}

// A known answer: the seed of the bytes 0 to 31 derives the public key whose SHA-256 is below. It pins
// crypto/mldsa's key derivation, which every node and every wallet of a network must share.
func TestMLDSA65KnownKey(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	key, err := NetworkScheme().ParsePrivateKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	const want = "d666806e11cee19a7c989f7445f90dd419cf4d2d51db8c0fdb4c0f0a542238c9"
	if digest := fmt.Sprintf("%x", sha256.Sum256(key.PublicKey())); digest != want {
		t.Errorf("the seed 0..31 derives a key whose SHA-256 is %s, want %s", digest, want)
	}
}

// A key whose t1 has every coefficient 0 or 1023 is weak: crypto/mldsa verifies, under it, a signature
// made with no private key, for any message (z = 0 and no hint, so w' = A·z - c·t1·2^13 has no high
// bits and the challenge is only a hash of the message). ValidatePublicKey refuses such keys, and Verify
// refuses the forgery. Under a key with one coefficient of 512 and the rest 0, the scheme takes the key
// and this forgery fails. That holds for 512, not for every key with one other coefficient. The test
// also records the check's limit (see ValidatePublicKey): the scheme takes a key with one coefficient
// of 1 and the rest 0, and one with every coefficient 1, and crypto/mldsa verifies the forgery under
// each.
func TestMLDSA65RefusesKeysAnyoneCanSignUnder(t *testing.T) {
	read := func(length int, parts ...[]byte) []byte {
		hash := sha3.NewSHAKE256()
		for _, part := range parts {
			hash.Write(part)
		}
		out := make([]byte, length)
		hash.Read(out)
		return out
	}
	keyWithT1 := func(coefficient func(i int) int) []byte {
		publicKey := make([]byte, 1952)
		copy(publicKey, "a seed rho of thirty-two bytes..")
		for i := 0; i < 1536; i++ {
			for bit := 0; bit < 10; bit++ {
				if coefficient(i)>>bit&1 == 1 {
					position := 32*8 + i*10 + bit
					publicKey[position/8] |= 1 << (position % 8)
				}
			}
		}
		return publicKey
	}
	forge := func(publicKey, message []byte) []byte {
		challenge := read(48, read(64, read(64, publicKey), []byte{0, 0}, message), make([]byte, 6*128))
		zero := bytes.Repeat([]byte{0x00, 0x00, 0x08, 0x00, 0x80}, 5*128) // z = 0: each coefficient packed as 2^19 in 20 bits
		return append(append(challenge, zero...), make([]byte, 55+6)...)  // no hint
	}
	message := []byte("pay 1000 to mallory")
	for name, coefficient := range map[string]func(int) int{
		"every coefficient 0":        func(int) int { return 0 },
		"every coefficient 1023":     func(int) int { return 1023 },
		"coefficients of 0 and 1023": func(i int) int { return i % 3 / 2 * 1023 },
	} {
		publicKey := keyWithT1(coefficient)
		key, err := mldsa.NewPublicKey(mldsa.MLDSA65(), publicKey)
		if err != nil || mldsa.Verify(key, message, forge(publicKey, message), nil) != nil {
			t.Fatalf("%s: premise: crypto/mldsa accepts the key and the forgery (%v)", name, err)
		}
		if NetworkScheme().ValidatePublicKey(publicKey) == nil || NetworkScheme().Verify(publicKey, message, forge(publicKey, message)) {
			t.Errorf("%s: a key anyone can sign under was accepted", name)
		}
	}
	oneOf512 := keyWithT1(func(i int) int { return map[bool]int{true: 512}[i == 700] })
	key, _ := mldsa.NewPublicKey(mldsa.MLDSA65(), oneOf512)
	if NetworkScheme().ValidatePublicKey(oneOf512) != nil || mldsa.Verify(key, message, forge(oneOf512, message), nil) == nil {
		t.Error("a key with one coefficient of 512 is taken, and this forgery must fail under it")
	}
	// The check's limit: other keys with small coefficients let anyone sign too, and the scheme takes
	// them (see ValidatePublicKey). With one coefficient of 1, c·t1·2^13 has coefficients of 0 or
	// ±2^13; with every coefficient 1, of at most 49·2^13 = 401408 for a challenge c, and in practice
	// far below γ2 = 261888. So this forgery verifies under both, for this message as for most.
	for name, coefficient := range map[string]func(int) int{
		"one coefficient 1":   func(i int) int { return map[bool]int{true: 1}[i == 700] },
		"every coefficient 1": func(int) int { return 1 },
	} {
		publicKey := keyWithT1(coefficient)
		key, _ := mldsa.NewPublicKey(mldsa.MLDSA65(), publicKey)
		if NetworkScheme().ValidatePublicKey(publicKey) != nil || mldsa.Verify(key, message, forge(publicKey, message), nil) != nil {
			t.Errorf("%s: the limit has moved: the key was refused, or the forgery failed under it; update the comments of ValidatePublicKey and the docs", name)
		}
	}
}

// A private key is stored as its 32-byte seed, the form keygen prints and the wallet keeps. Every
// other length is refused, a 64-byte Ed25519 private key of an earlier network among them.
func TestPrivateKeyIsA32ByteSeed(t *testing.T) {
	scheme := NetworkScheme()
	publicKey, privateKey, err := scheme.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(privateKey) != 32 {
		t.Fatalf("GenerateKey returned a %d-byte private key, want the 32-byte seed", len(privateKey))
	}
	parsedKey, err := scheme.ParsePrivateKey(privateKey)
	if err != nil || !bytes.Equal(parsedKey.PublicKey(), publicKey) {
		t.Fatalf("the seed does not parse back to its public key (%v)", err)
	}
	for _, size := range []int{0, 31, 33, 64} {
		if _, err := scheme.ParsePrivateKey(make([]byte, size)); err == nil {
			t.Errorf("a %d-byte private key was parsed", size)
		}
		if _, err := NewSigner(scheme, "mainnet", make([]byte, size)); err == nil {
			t.Errorf("a signer was built from a %d-byte private key", size)
		}
	}
}
