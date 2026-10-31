package consensus

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// A signer that cannot sign (today: "unreachable"; with the planned remote signer: network error,
// signer down, or the signer's own double-sign refusal — exactly the H1 guard). castVote returns nil
// without setting sentPrevote, but step1 reports progress=true, so evaluate() spins forever on the
// node's single event-loop goroutine.
func TestVBSDriverSpinsWhenSigningFails(t *testing.T) {
	good, _ := signing.Generate(signing.Ed25519{}, "testnet")
	broken := signing.NewSigner(signing.Ed25519{}, "testnet", good.PublicKey(), []byte("not-a-64-byte-private-key"))
	vals := NewValidatorSet([]Validator{{PublicKey: good.PublicKeyBase64(), Power: 1}, {PublicKey: "other", Power: 1}})
	var h int64 = 1
	for vals.ProposerAt(h, 0) != good.PublicKeyBase64() {
		h++
	}
	d := NewDriver(broken, vals, signing.Ed25519{}, "testnet", h, [32]byte{1})
	done := make(chan struct{})
	go func() { d.Start(); close(done) }()
	select {
	case <-done:
		t.Log("Start returned")
	case <-time.After(3 * time.Second):
		t.Errorf("Driver.Start() (proposer, signing fails) did not return within 3s: evaluate() spins on (nil, progressed=true)")
	}
}
