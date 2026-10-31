//go:build go1.24

package main

// PoC (checked-OK evidence, not a bug): cross-check the hand-rolled pbkdf2SHA256 against the
// toolchain's crypto/pbkdf2 (stdlib since Go 1.24) on random passwords/salts, including passwords
// longer than the HMAC block size (64 B -> key hashed first), empty password/salt, multi-block
// output (keyLen > 32), and odd lengths. The shipped vectors only cover keyLen <= 32.

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	mrand "math/rand"
	"testing"
)

func TestPoCPBKDF2MatchesStdlib(t *testing.T) {
	rng := mrand.New(mrand.NewSource(1))
	for i := 0; i < 300; i++ {
		pw := make([]byte, rng.Intn(150))
		salt := make([]byte, rng.Intn(70))
		_, _ = rand.Read(pw)
		_, _ = rand.Read(salt)
		iters := 1 + rng.Intn(50)
		keyLen := 1 + rng.Intn(100)
		got := pbkdf2SHA256(pw, salt, iters, keyLen)
		want, err := pbkdf2.Key(sha256.New, string(pw), salt, iters, keyLen)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("mismatch pw=%x salt=%x iters=%d keyLen=%d", pw, salt, iters, keyLen)
		}
	}
	// the production parameters once (600k iterations, 32-byte key)
	got := pbkdf2SHA256([]byte("correct horse"), []byte("0123456789abcdef"), kdfIterations, 32)
	want, _ := pbkdf2.Key(sha256.New, "correct horse", []byte("0123456789abcdef"), kdfIterations, 32)
	if !bytes.Equal(got, want) {
		t.Fatal("mismatch at production parameters")
	}
}
