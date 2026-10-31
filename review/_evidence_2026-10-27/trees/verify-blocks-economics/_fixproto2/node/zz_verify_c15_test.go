package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type verifyNet struct {
	nodes   []*Node
	servers []*httptest.Server
	signers []*signing.Signer
	vs      *consensus.ValidatorSet
}

func startVerifyNet(t *testing.T, n int, genesis []ledger.Account, maxPool int, interval time.Duration) *verifyNet {
	t.Helper()
	net := &verifyNet{}
	vals := make([]consensus.Validator, n)
	for i := 0; i < n; i++ {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		net.signers = append(net.signers, s)
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	net.vs = consensus.NewValidatorSet(vals)
	transports := make([]*HTTPTransport, n)
	for i := 0; i < n; i++ {
		transports[i] = NewHTTPTransport(nil)
		nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: net.signers[i], Validators: net.vs,
			TransactionCost: rat("0.1"), Timeout: time.Second, MinBlockInterval: interval, Genesis: genesis,
			MaxMempool: maxPool, MaxCommentLength: 280, MaxMessageBytes: 2000}, transports[i])
		net.nodes = append(net.nodes, nd)
		net.servers = append(net.servers, httptest.NewServer(NewServer(nd).Handler()))
	}
	for i := 0; i < n; i++ {
		var peers []string
		for j := 0; j < n; j++ {
			if j != i {
				peers = append(peers, net.servers[j].URL)
			}
		}
		transports[i].SetPeers(peers)
	}
	for _, nd := range net.nodes {
		nd.Start()
	}
	t.Cleanup(func() {
		for _, nd := range net.nodes {
			nd.Stop()
		}
		for _, s := range net.servers {
			s.Close()
		}
	})
	return net
}

func (net *verifyNet) heights() []int64 {
	var hs []int64
	for _, nd := range net.nodes {
		hs = append(hs, nd.Snapshot().Height)
	}
	return hs
}

func postJSON(t *testing.T, url string, v any) int {
	t.Helper()
	b, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// C15 re-run through production intake only: a key with NO account POSTs N fee-free updates to ONE
// node's /hdls/update (gossip spreads them), all naming a FUTURE height chosen while the chain is live.
func runC15HTTP(t *testing.T, count int) {
	ghost, _ := signing.Generate(signing.Ed25519{}, "testnet") // no account anywhere
	G := ghost.PublicKeyBase64()
	holder, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{{PublicKey: holder.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: holder.PublicKeyBase64()},
		{PublicKey: G, Amount: rat("1"), ValidatorNode: G}} // FIXED COPY: the spammer is funded (1 unit)
	net := startVerifyNet(t, 4, genesis, 10000, 200*time.Millisecond)

	waitFor(t, 10*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= 3 }, "chain should be live first")
	target := net.nodes[0].Snapshot().Height + 25
	var updates []ledger.AccountUpdate
	for i := 0; i < count; i++ {
		u, _ := ledger.SignAccountUpdate(ghost, ledger.AccountUpdate{From: G, BlockID: target,
			Description: fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 1700))})
		if !ledger.WithinMessageBound(2000, u) {
			t.Fatal("each update must fit the default per-message cap")
		}
		updates = append(updates, u)
	}
	start := time.Now()
	for _, u := range updates {
		if code := postJSON(t, net.servers[0].URL+"/hdls/update", u); code != http.StatusAccepted {
			t.Fatalf("update intake refused: %d", code)
		}
	}
	h := net.nodes[0].Snapshot().Height
	t.Logf("posted %d ghost updates (BlockID=%d) to node0 in %v; chain at height %d", count, target, time.Since(start).Round(time.Millisecond), h)
	if h >= target {
		t.Fatalf("setup: chain reached target before the updates were posted")
	}
	// What size is the honest candidate for that height (ghost updates included by ProposeBlock)?
	blk := ledger.ProposeBlock(signing.Ed25519{}, "testnet", target, ledger.LedgerState{Accounts: net.nodes[0].Snapshot().Accounts,
		Parameters: ledger.LedgerParameters{TransactionCost: rat("0.1"), StepTimeoutMs: 1000, MaxMessageBytes: 2000}},
		ledger.BlockContent{Updates: updates}, net.vs)
	_ = genesis
	raw, _ := json.Marshal(ProposalMsg{Height: target, Block: blk})
	t.Logf("ProposeBlock carries %d/%d ghost-key updates; proposal JSON = %d bytes (decode limit %d)", len(blk.Updates), count, len(raw), 1<<20)

	waitFor(t, 30*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= target }, "chain should reach the loaded height")
	deadline := time.Now().Add(20 * time.Second * testTimeSlack)
	for time.Now().Before(deadline) {
		if net.nodes[0].Snapshot().Height > target {
			t.Logf("committed past the loaded height %d after %v (heights=%v)", target, time.Since(start).Round(time.Millisecond), net.heights())
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("STALLED at height %d for %v, all validators up (heights=%v)", target, 20*time.Second*testTimeSlack, net.heights())
}

func TestVerifyC15GhostUpdatesViaHTTPStall(t *testing.T)   { runC15HTTP(t, 600) }
func TestVerifyC15FundedUpdates2000ViaHTTP(t *testing.T)   { runC15HTTP(t, 2000) }
func TestVerifyC15GhostUpdatesViaHTTPControl(t *testing.T) { runC15HTTP(t, 400) }
