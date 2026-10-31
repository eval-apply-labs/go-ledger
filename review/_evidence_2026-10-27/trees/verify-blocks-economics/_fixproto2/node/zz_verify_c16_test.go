package node

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// C16 through production intake on a live 4-node HTTP network: a funded account POSTs far-future
// (BlockID 2^40) payments to ONE node's /hdls/pay; gossip fills every node's mempool; commits keep
// happening but nothing evicts them; an honest payment at ANOTHER node is refused. amount selects the
// below-cost variant (1e-9) or an at/above-cost variant (0.2) to separate the two root causes.
func runC16FarFuture(t *testing.T, amount string, fillerBalance string) {
	runC16FarFutureN(t, amount, fillerBalance, 60)
}
func TestVerifyC16ControlNoFill(t *testing.T) { runC16FarFutureN(t, "0.2", "12", 0) }

func runC16FarFutureN(t *testing.T, amount string, fillerBalance string, fill int) {
	const pool = 60 // stand-in for the default 10000
	filler, _ := signing.Generate(signing.Ed25519{}, "testnet")
	honest, _ := signing.Generate(signing.Ed25519{}, "testnet")
	F, H := filler.PublicKeyBase64(), honest.PublicKeyBase64()
	genesis := []ledger.Account{
		{PublicKey: F, Amount: rat(fillerBalance), ValidatorNode: F},
		{PublicKey: H, Amount: rat("100"), ValidatorNode: H},
	}
	net := startVerifyNet(t, 4, genesis, pool, 200*time.Millisecond)
	waitFor(t, 10*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= 2 }, "live")

	receipts := 0
	for i := 0; i < fill; i++ {
		p, _ := ledger.SignPayment(filler, ledger.Payment{PublicKeyFrom: F, PublicKeyTo: H, Amount: rat(amount),
			BlockID: 1 << 40, Comment: fmt.Sprintf("f%03d", i)})
		if postJSON(t, net.servers[0].URL+"/hdls/pay", p) == http.StatusOK {
			receipts++
		}
	}
	h0 := net.nodes[0].Snapshot().Height
	waitFor(t, 20*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= h0+10 }, "chain keeps committing")
	refused := 0
	for i := 0; i < 4; i++ {
		hNow := net.nodes[i].Snapshot().Height
		p, _ := ledger.SignPayment(honest, ledger.Payment{PublicKeyFrom: H, PublicKeyTo: F, Amount: rat("5"), BlockID: hNow + 3, Comment: fmt.Sprintf("h%d", i)})
		if code := postJSON(t, net.servers[i].URL+"/hdls/pay", p); code == http.StatusConflict {
			refused++
		}
	}
	t.Logf("amount=%s: %d/%d far-future payments receipted at node0; after %d+ commits (heights=%v) an honest 5-unit payment was refused (409) at %d/4 nodes; filler balance still %s",
		amount, receipts, pool, 10, net.heights(), refused, ledger.AccountAmount(net.nodes[3].Snapshot().Accounts, F).FloatString(9))
	if refused > 0 {
		t.Errorf("honest payments refused network-wide by a far-future fill that commits never evict")
	}
}

func TestVerifyC16FarFutureBelowCostHTTP(t *testing.T) { runC16FarFuture(t, "1/1000000000", "1") }
func TestVerifyC16FarFutureAtCostHTTP(t *testing.T)    { runC16FarFuture(t, "0.2", "12") }

// NEW: the account-update pool (cap = MaxMempool) admits any BlockID from any key, with NO account and
// no fee. A key with no account fills it with BlockID 2^40 updates via ONE node; gossip fills all
// pools; an honest name registration then gets 202 Accepted everywhere but never lands.
func TestVerifyUpdatePoolPermanentFillByGhostKey(t *testing.T) { runUpdatePoolFill(t, 60) }
func TestVerifyUpdatePoolControlNoFill(t *testing.T)           { runUpdatePoolFill(t, 0) }

func runUpdatePoolFill(t *testing.T, fill int) {
	const pool = 60
	ghost, _ := signing.Generate(signing.Ed25519{}, "testnet")
	owner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	G, O := ghost.PublicKeyBase64(), owner.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: O, Amount: rat("100"), ValidatorNode: O}}
	net := startVerifyNet(t, 4, genesis, pool, 200*time.Millisecond)
	waitFor(t, 10*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= 2 }, "live")
	for i := 0; i < fill; i++ {
		u, _ := ledger.SignAccountUpdate(ghost, ledger.AccountUpdate{From: G, Description: fmt.Sprintf("g%03d", i), BlockID: 1 << 40})
		if code := postJSON(t, net.servers[0].URL+"/hdls/update", u); code != http.StatusAccepted {
			t.Fatalf("ghost update refused: %d", code)
		}
	}
	h0 := net.nodes[0].Snapshot().Height
	waitFor(t, 20*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= h0+5 }, "commits")
	target := net.nodes[2].Snapshot().Height + 3
	reg, _ := ledger.SignAccountUpdate(owner, ledger.AccountUpdate{From: O, Name: "alice", BlockID: target})
	code := postJSON(t, net.servers[2].URL+"/hdls/update", reg)
	waitFor(t, 20*time.Second, func() bool { return net.nodes[2].Snapshot().Height > target+1 }, "past the registration block")
	named := false
	for _, a := range net.nodes[2].Snapshot().Accounts {
		if a.PublicKey == O && a.Name == "alice" {
			named = true
		}
	}
	t.Logf("honest registration HTTP status=%d; name registered after block %d: %v (heights=%v)", code, target, named, net.heights())
	if !named {
		t.Errorf("an honest name registration was accepted (HTTP %d) but silently dropped: the update pool is held by a no-account key's far-future updates", code)
	}
}
