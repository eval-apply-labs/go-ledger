package node

// Network-level impact of the post-commit-window proposal loss + stale timeouts (scratch copy only):
// an all-honest, loss-free localhost network over the real HTTP transport. Every height should
// commit in round 0 within ~MinBlockInterval + a few ms. The PoC records the round each height
// committed in and the wall time, and fails when the network spends most of its time stalled.
//
// Run: go test -count=1 -run 'TestPoCThroughput' -v ./node/

import (
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestPoCThroughput3Validators(t *testing.T) { runThroughputPoC(t, 3) }
func TestPoCThroughput4Validators(t *testing.T) { runThroughputPoC(t, 4) }

func runThroughputPoC(t *testing.T, n int) {
	const (
		interval = 100 * time.Millisecond // MinBlockInterval (governed BlockIntervalMs)
		timeout  = 1 * time.Second        // base step timeout (and re-gossip cadence)
		runFor   = 12 * time.Second
	)
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("1000"), ValidatorNode: "v"}}

	transports := make([]*HTTPTransport, n)
	nodes := make([]*Node, n)
	servers := make([]*httptest.Server, n)
	for i := 0; i < n; i++ {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: timeout, MinBlockInterval: interval, Genesis: genesis,
		}, transports[i])
		servers[i] = httptest.NewServer(NewServer(nodes[i]).Handler())
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	for i := 0; i < n; i++ {
		var peers []string
		for j := 0; j < n; j++ {
			if j != i {
				peers = append(peers, servers[j].URL)
			}
		}
		transports[i].SetPeers(peers)
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()

	roundOf := map[int64]int32{}
	firstSeen := map[int64]time.Duration{}
	began := time.Now()
	for time.Since(began) < runFor {
		for _, nd := range nodes {
			s := nd.Snapshot()
			if s.LastCommit.Height > 0 {
				if _, ok := roundOf[s.LastCommit.Height]; !ok {
					roundOf[s.LastCommit.Height] = s.LastCommit.Round
					firstSeen[s.LastCommit.Height] = time.Since(began)
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	var heights []int64
	for h := range roundOf {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	laterRound := 0
	var stalled time.Duration
	prev := time.Duration(0)
	for _, h := range heights {
		if roundOf[h] > 0 {
			laterRound++
		}
		if gap := firstSeen[h] - prev; gap > 3*interval {
			stalled += gap
		}
		prev = firstSeen[h]
	}
	ideal := int(runFor / (interval + 15*time.Millisecond))
	t.Logf("%d validators, %v: committed %d heights (ideal ≈ %d); %d needed round ≥ 1; %v of %v spent in gaps > %v",
		n, runFor, len(heights), ideal, laterRound, stalled.Round(time.Millisecond), runFor, 3*interval)
	if len(heights) < ideal/2 {
		t.Errorf("BUG: an all-honest, loss-free %d-validator localhost network committed only %d of ~%d heights "+
			"(%d heights fell back to round ≥ 1)", n, len(heights), ideal, laterRound)
	}
}
