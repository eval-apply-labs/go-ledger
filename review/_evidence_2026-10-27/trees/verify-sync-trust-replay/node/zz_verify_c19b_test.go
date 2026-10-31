package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// C19 partial rotation: ALL FOUR founders keep validating, but each moved 600 of its 1000 to a new
// "bank" N (live: N≈2400, founders 400 each). A minimal quorum is N + one founder, whose genesis
// power is 1000/4000 ≤ ⅓. Sample the checkpoints the five nodes actually serve and count how many a
// genesis-anchored joiner/restarter would accept.
func TestVerifyC19PartialRotationCheckpointAcceptance(t *testing.T) {
	const net = "testnet"
	gen := func() *signing.Signer { s, _ := signing.Generate(signing.Ed25519{}, net); return s }
	signers := []*signing.Signer{gen(), gen(), gen(), gen(), gen()} // F1..F4, N
	N := signers[4].PublicKeyBase64()
	var genesis []ledger.Account
	for _, s := range signers[:4] {
		genesis = append(genesis, ledger.Account{PublicKey: s.PublicKeyBase64(), Amount: rat("1000"), ValidatorNode: s.PublicKeyBase64()})
	}
	b := newBus()
	var nodes []*Node
	for _, s := range signers {
		nd := New(Config{NetworkID: net, Scheme: signing.Ed25519{}, Signer: s, ValidatorThreshold: rat("100"),
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 30 * time.Millisecond, Genesis: genesis}, b)
		b.register(s.PublicKeyBase64(), nd)
		nodes = append(nodes, nd)
	}
	for _, s := range signers[:4] {
		p, _ := ledger.SignPayment(s, ledger.Payment{PublicKeyFrom: s.PublicKeyBase64(), PublicKeyTo: N, Amount: rat("600"), BlockID: 1})
		for _, nd := range nodes {
			nd.SubmitPayment(p)
		}
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()
	probe := New(Config{NetworkID: net, Scheme: signing.Ed25519{}, Signer: gen(), ValidatorThreshold: rat("100"), TransactionCost: rat("0.1"), Genesis: genesis}, newBus())
	type key struct {
		node int
		h    int64
	}
	seen := map[key]bool{}
	accepted, rejected := 0, 0
	byNode := make([][2]int, len(nodes))
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && accepted+rejected < 150 {
		for i, nd := range nodes {
			s := nd.Snapshot()
			if s.LastCommit.Height < 3 || seen[key{i, s.LastCommit.Height}] {
				continue
			}
			seen[key{i, s.LastCommit.Height}] = true
			cp := Checkpoint{Accounts: s.Accounts, Parameters: s.Parameters, GovernanceSequence: s.GovernanceSequence, Commit: s.LastCommit, Validators: s.Validators}
			if probe.verifyCheckpoint(cp, genesis) {
				accepted++
				byNode[i][0]++
			} else {
				rejected++
				byNode[i][1]++
			}
		}
		time.Sleep(3 * time.Millisecond)
	}
	s := nodes[0].Snapshot()
	t.Logf("live set %v; sampled %d served checkpoints: %d accepted, %d REJECTED by a genesis-anchored joiner; per node [acc rej] F1..F4,N = %v",
		s.Validators, accepted+rejected, accepted, rejected, byNode)
}
