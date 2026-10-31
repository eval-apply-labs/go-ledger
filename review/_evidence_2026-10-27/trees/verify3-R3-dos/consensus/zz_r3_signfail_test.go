package consensus

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// R3 (VBS-N1): a signer whose Sign returns an error (stand-in: wrong-length private key; the only error
// path today). Each case runs the driver call in a goroutine and reports whether it returns.
func returnsWithin(f func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestR3SignFailPaths(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)
	broken := func(s *signing.Signer) *signing.Signer {
		return signing.NewSigner(scheme, network, s.PublicKey(), []byte("short"))
	}

	// (a) proposer at round 0: Start() itself.
	prop := by[vs.ProposerAt(height, 0)]
	dA := NewDriver(broken(prop), vs, scheme, network, height, digest(0x01))
	okA := returnsWithin(func() { dA.Start() }, 2*time.Second)

	// (b) non-proposer: Start() then the round-0 proposal arrives.
	me := nonProposer(vs, by, height, 0)
	dB := NewDriver(broken(me), vs, scheme, network, height, digest(0x02))
	okStartB := returnsWithin(func() { dB.Start() }, 2*time.Second)
	okB := returnsWithin(func() { dB.ReceiveProposal(Proposal{Round: 0, Digest: digest(0x03), ValidRound: -1, Valid: true}) }, 2*time.Second)

	// (c) non-proposer, no proposal yet: the propose timeout (the path the existing test pins).
	dC := NewDriver(broken(me), vs, scheme, network, height, digest(0x04))
	dC.Start()
	okC := returnsWithin(func() { dC.Timeout(StepPropose, 0) }, 2*time.Second)

	t.Logf("(a) proposer Start returns: %v | (b) non-proposer Start returns: %v, ReceiveProposal returns: %v | (c) propose timeout with no proposal returns: %v",
		okA, okStartB, okB, okC)
}
