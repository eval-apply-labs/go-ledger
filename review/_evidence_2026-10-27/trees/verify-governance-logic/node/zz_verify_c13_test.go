package node

// Verifier PoC (round 2) for C13: after a ratified huge BlockIntervalMs, does a node RESTART (which
// runs startHeight on evStart, bypassing the interval) let the revert enact? The vote pools are
// in-memory, and n.Start() runs before the HTTP server listens (cmd/server/main.go), so the restarted
// node builds its candidate with an empty pool.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestVerifyC13RestartDoesNotRecover(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := alice.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}})
	snap := filepath.Join(t.TempDir(), "snap.json")
	cfg := Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: alice, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 30 * time.Millisecond,
		Genesis:      []ledger.Account{{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey}},
		SnapshotPath: snap,
	}
	nd := New(cfg, NewHTTPTransport(nil))
	nd.Start()
	waitFor(t, 2*time.Second, func() bool { return nd.Snapshot().Height >= 2 }, "network did not start")
	s := nd.Snapshot()
	cur := s.Parameters
	halt := cur
	halt.BlockIntervalMs = 86_400_000_000
	pv, _ := ledger.SignParameterVote(alice, halt, cur.Digest(), s.GovernanceSequence)
	nd.SubmitParameterVote(ledger.ParameterChange{NewParameters: halt, PreviousParametersDigest: cur.Digest(), Sequence: s.GovernanceSequence, Votes: []ledger.ParameterVote{pv}})
	waitFor(t, 4*time.Second, func() bool { return nd.Snapshot().Parameters.BlockIntervalMs == halt.BlockIntervalMs }, "setup: change not enacted")
	s2 := nd.Snapshot()
	revert, _ := ledger.SignParameterVote(alice, cur, halt.Digest(), s2.GovernanceSequence)
	revertCert := ledger.ParameterChange{NewParameters: cur, PreviousParametersDigest: halt.Digest(), Sequence: s2.GovernanceSequence, Votes: []ledger.ParameterVote{revert}}
	nd.SubmitParameterVote(revertCert)
	time.Sleep(500 * time.Millisecond)
	s3 := nd.Snapshot()
	nd.Stop()
	t.Logf("enacted: height %d -> %d after 0.5s (halted=%v)", s2.Height, s3.Height, s3.Height == s2.Height)

	// Restart from the snapshot, then re-submit the revert as quickly as an operator could.
	nd2 := New(cfg, NewHTTPTransport(nil))
	nd2.Start()
	defer nd2.Stop()
	nd2.SubmitParameterVote(revertCert)
	time.Sleep(1 * time.Second)
	s4 := nd2.Snapshot()
	t.Logf("after restart: height %d (one block produced on evStart=%v), BlockIntervalMs=%d (reverted=%v), revert pooled=%d",
		s4.Height, s4.Height == s3.Height+1, s4.Parameters.BlockIntervalMs, s4.Parameters.BlockIntervalMs == cur.BlockIntervalMs, len(nd2.GovernanceView().ParameterProposals))
	if s4.Parameters.BlockIntervalMs != cur.BlockIntervalMs {
		t.Errorf("restart does not recover: the restart block is built before the revert can be pooled")
	}
}
