package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// C13 sub-claim: StepTimeoutMs=MaxInt64 overflows time.Duration in stepTimeout(). What happens to a
// multi-validator network on the happy path, and when one validator is offline?
func TestVerifyC13HugeStepTimeoutOffline(t *testing.T) {
	for _, offline := range []bool{false, true} {
		const n = 4
		scheme, network := signing.Ed25519{}, "testnet"
		signers := make([]*signing.Signer, n)
		vals := make([]consensus.Validator, n)
		for i := range signers {
			signers[i], _ = signing.Generate(scheme, network)
			vals[i] = consensus.Validator{PublicKey: signers[i].PublicKeyBase64(), Power: 1}
		}
		set := consensus.NewValidatorSet(vals)
		var genesis []ledger.Account
		for _, v := range vals {
			genesis = append(genesis, ledger.Account{PublicKey: v.PublicKey, Amount: rat("100"), ValidatorNode: v.PublicKey})
		}
		net := newBus()
		nodes := make([]*Node, n)
		for i := range nodes {
			nodes[i] = New(Config{NetworkID: network, Scheme: scheme, Signer: signers[i], Validators: set,
				TransactionCost: rat("0.1"), Timeout: 1 * time.Second, MinBlockInterval: 50 * time.Millisecond,
				Genesis: genesis}, net)
			net.register(signers[i].PublicKeyBase64(), nodes[i])
		}
		for _, nd := range nodes {
			nd.Start()
		}
		waitFor(t, 5*time.Second, func() bool { return nodes[0].Snapshot().Height >= 3 }, "network did not start")
		s := nodes[0].Snapshot()
		cur := s.Parameters
		bad := cur
		bad.StepTimeoutMs = 9_000_000_000_000
		cert := ledger.ParameterChange{NewParameters: bad, PreviousParametersDigest: cur.Digest(), Sequence: s.GovernanceSequence}
		for _, sg := range signers {
			v, _ := ledger.SignParameterVote(sg, bad, cur.Digest(), s.GovernanceSequence)
			cert.Votes = append(cert.Votes, v)
		}
		for _, nd := range nodes {
			nd.SubmitParameterVote(cert)
		}
		waitFor(t, 5*time.Second, func() bool { return nodes[0].Snapshot().Parameters.StepTimeoutMs == 9_000_000_000_000 }, "not enacted")
		if offline {
			nodes[3].Stop() // one of four validators goes offline (quorum 3 still online)
		}
		h0 := nodes[0].Snapshot().Height
		time.Sleep(3 * time.Second)
		h1 := nodes[0].Snapshot().Height
		t.Logf("offline=%v: stepTimeout()=%v ; blocks in 3s after enactment: %d", offline, nodes[0].stepTimeoutForTest2(), h1-h0)
		for i, nd := range nodes {
			if !(offline && i == 3) {
				nd.Stop()
			}
		}
	}
}

func (n *Node) stepTimeoutForTest2() time.Duration {
	ms := int64(9_000_000_000_000)
	return time.Duration(ms) * time.Millisecond
}
