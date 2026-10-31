package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// PoC (KEY-1): cmd/server load() validates key LENGTHS (the C4 fix) but never checks that the
// configured privateKey actually signs for the configured publicKey. A config that pastes a
// publicKey from one keygen run and a privateKey from another (both correct length) loads cleanly,
// but the node's identity is the configured pub while ed25519.Sign binds priv[32:] — so every vote,
// proposal and receipt it emits verifies under priv's real pub, NOT the configured pub it is known
// by. The node is a registered validator that can never contribute a counted vote: a silent
// consensus ghost (exactly the C4 failure mode, reachable past the length-only C4 fix). There is no
// sign+verify self-test at startup to catch it.
func TestPoCConfigAcceptsMismatchedKeypair(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)  // identity the config claims (and the validator set lists)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader) // a DIFFERENT private key
	pub1B64 := base64.StdEncoding.EncodeToString(pub1)

	c := baseConfig(t)
	c["publicKey"] = pub1B64
	c["privateKey"] = base64.StdEncoding.EncodeToString(priv2)
	c["validators"] = []map[string]any{{"publicKey": pub1B64, "power": 1}}
	c["genesis"] = []map[string]any{{"publicKey": pub1B64, "name": "founder", "amount": "1000", "validatorNode": pub1B64}}

	cfg, _, _, err := load(writeConfig(t, c))
	if err != nil {
		t.Fatalf("load rejected the mismatched pair (good, bug absent): %v", err)
	}
	t.Logf("BUG: load() accepted a config whose privateKey does not sign for its publicKey")

	// The node's identity is the configured pub...
	if cfg.Signer.PublicKeyBase64() != pub1B64 {
		t.Fatalf("signer identity = %s, want configured pub", cfg.Signer.PublicKeyBase64())
	}
	// ...but a vote it signs cannot be verified under that identity.
	v := consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, Validator: pub1B64}
	signed, err := consensus.SignVote(cfg.Signer, v)
	if err != nil {
		t.Fatalf("SignVote: %v", err)
	}
	if signed.Verify(cfg.Scheme, cfg.NetworkID) {
		t.Fatal("expected the vote to FAIL verification under the configured pub (ghost), but it verified")
	}
	t.Logf("CONFIRMED: this validator's own signed vote does not verify under its configured identity -> it can never be counted toward quorum (silent validator death)")
}
