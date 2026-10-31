package node

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): ProposeBlock includes EVERY valid payment/update naming the height
// (block.go:105-127) and ApplyBlock accepts any number — there is no per-block count or byte cap —
// while every receiver decodes a proposal under a 1 MiB body limit (httptransport.go decode). Account
// updates carry no fee and the update pool admits any BlockID. So ~600 individually-in-bound (≤2000 B)
// updates from ONE account with a 1-unit balance, all naming one future height, make every honest
// proposer's candidate for that height undeliverable; nothing evicts them (pools prune only on
// commit), so the height never commits. The test asserts the network keeps committing; it FAILS today
// (the chain is live through the preceding heights, then stops at the loaded height).
func TestPoCOversizeBlockStallsHeight(t *testing.T) { runOversizeBlock(t, 600) }

// Control: the same load at 400 updates (~770 KB, under the receivers' limit) commits normally, so
// the stall above is the proposal size, not the update volume. Expected to PASS.
func TestPoCOversizeBlockControlUnderLimit(t *testing.T) { runOversizeBlock(t, 400) }

func runOversizeBlock(t *testing.T, count int) {
	const n = 4
	const stuckHeight = 3
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	sender, _ := signing.Generate(signing.Ed25519{}, "testnet")
	S := sender.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: S, Amount: rat("1"), ValidatorNode: S}}

	// ~600 distinct fee-free updates naming one height, each within the default 2000-byte cap.
	var updates []ledger.AccountUpdate
	for i := 0; i < count; i++ {
		u, _ := ledger.SignAccountUpdate(sender, ledger.AccountUpdate{From: S, BlockID: stuckHeight,
			Description: fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 1700))})
		if !ledger.WithinMessageBound(2000, u) {
			t.Fatal("setup: each update must fit the default per-message cap")
		}
		updates = append(updates, u)
	}
	params := ledger.LedgerParameters{TransactionCost: rat("0.1"), StepTimeoutMs: 1000, MaxMessageBytes: 2000}
	blk := ledger.ProposeBlock(signing.Ed25519{}, "testnet", stuckHeight, ledger.LedgerState{Accounts: genesis, Parameters: params}, ledger.BlockContent{Updates: updates}, vs)
	raw, _ := json.Marshal(ProposalMsg{Height: stuckHeight, Block: blk})
	t.Logf("candidate proposal for height %d carries %d updates and serializes to %d bytes (receiver body limit %d)", stuckHeight, len(blk.Updates), len(raw), 1<<20)

	transports := make([]*HTTPTransport, n)
	nodes := make([]*Node, n)
	servers := make([]*httptest.Server, n)
	for i := 0; i < n; i++ {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis, MaxMempool: 10000, MaxMessageBytes: 2000}, transports[i])
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
		for _, u := range updates {
			nd.SubmitUpdate(u) // what gossip delivers to every node
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

	waitFor(t, 10*time.Second, func() bool { return nodes[0].Snapshot().Height >= stuckHeight }, "setup: the network should first commit the heights before the loaded one")
	deadline := time.Now().Add(25 * time.Second * testTimeSlack)
	for time.Now().Before(deadline) {
		if nodes[0].Snapshot().Height > stuckHeight {
			return // committed past the loaded height: not stuck
		}
		time.Sleep(100 * time.Millisecond)
	}
	var hs []int64
	for _, nd := range nodes {
		hs = append(hs, nd.Snapshot().Height)
	}
	t.Errorf("chain stalled at height %d for %v with all 4 validators up (heights=%v): every candidate block exceeds the receivers' 1 MiB proposal limit", stuckHeight, 25*time.Second*testTimeSlack, hs)
}
