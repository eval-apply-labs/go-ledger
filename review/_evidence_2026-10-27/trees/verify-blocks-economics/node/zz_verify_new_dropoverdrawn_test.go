package node

import (
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// NEW (C08 x C16): pending payments are never charged while their BlockID is in the future, and the
// node re-sums each payer's pending amounts after EVERY commit (mempool.dropOverdrawn) and on every
// admission (outgoingFor). Amounts 1/d_i with pairwise-coprime ~783-digit d_i (each payment < 2000 B,
// below the cost, so no fee, and the whole set totals ~0 balance) make that sum's denominator their
// LCM, so every commit on every node pays super-linear GCD cost — forever, for a payer holding 1 unit.
func TestVerifyNewFarFutureCoprimePendingSlowsEveryCommit(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	P := payer.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}})
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: time.Second, MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000,
		Genesis: []ledger.Account{{PublicKey: P, Amount: rat("1"), ValidatorNode: P}}}, NewHTTPTransport(nil))
	srv := httptest.NewServer(NewServer(nd).Handler())
	defer srv.Close()
	nd.Start()
	defer nd.Stop()
	waitFor(t, 10*time.Second, func() bool { return nd.Snapshot().Height >= 3 }, "live")
	rate := func(d time.Duration) float64 {
		h0 := nd.Snapshot().Height
		time.Sleep(d)
		return float64(nd.Snapshot().Height-h0) / d.Seconds()
	}
	t.Logf("baseline: %.1f blocks/s", rate(2*time.Second))

	base := new(big.Int).Lsh(big.NewInt(1), 2600)
	const n = 140
	start := time.Now()
	for i := 0; i < n; i++ {
		d := new(big.Int).Add(base, big.NewInt(int64(1+i)))
		p, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: v.PublicKeyBase64(),
			Amount: new(big.Rat).SetFrac(big.NewInt(1), d), BlockID: 1 << 40, Comment: fmt.Sprintf("%d", i)})
		if code := postJSON(t, srv.URL+"/hdls/pay", p); code != http.StatusOK {
			t.Fatalf("payment %d refused: %d", i, code)
		}
	}
	t.Logf("%d far-future coprime payments receipted in %v", n, time.Since(start).Round(time.Millisecond))
	h1 := nd.Snapshot().Height
	r := rate(30 * time.Second)
	t.Logf("after loading: %.2f blocks/s (heights %d -> %d); payer balance still %s",
		r, h1, nd.Snapshot().Height, ledger.AccountAmount(nd.Snapshot().Accounts, P).FloatString(2))
	if r < 1 {
		t.Errorf("every commit now pays the pending-sum cost: throughput fell to %.2f blocks/s for a 1-unit payer that is never charged", r)
	}
}
