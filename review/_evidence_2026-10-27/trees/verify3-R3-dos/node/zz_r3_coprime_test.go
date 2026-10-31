package node

// R3 verification (group R3-dos) for BE-NEW-1: far-future coprime-denominator pending payments make
// every admission (outgoingFor) and every post-commit re-check (dropOverdrawn) super-linear, for a
// 1-unit payer who is never charged. Measured through AdmitPayment (what /hdls/pay calls) and through a
// live single-validator node's commit loop.

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func coprimeAmt(i int) *big.Rat {
	d := new(big.Int).Lsh(big.NewInt(1), 2600)
	d.Add(d, big.NewInt(int64(1+i)))
	return new(big.Rat).SetFrac(big.NewInt(1), d) // 1/d_i : pairwise-coprime denominators, each < 2000 B
}

// (a) Intake cost grows with the number of pending coprime payments (outgoingFor's big.Rat.Add).
func TestR3CoprimeIntakeCost(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	P := payer.PublicKeyBase64()
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second, MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000,
		Genesis: []ledger.Account{{PublicKey: P, Amount: rat("1"), ValidatorNode: P}}}, NewHTTPTransport(nil))
	go nd.run() // event loop only (no consensus): payments stay pending
	defer nd.Stop()
	for i := 0; i < 160; i++ {
		p, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: v.PublicKeyBase64(), Amount: coprimeAmt(i), BlockID: 1 << 40, Comment: fmt.Sprintf("%d", i)})
		if !ledger.WithinMessageBound(2000, p) {
			t.Fatalf("payment %d exceeds the 2000-byte cap", i)
		}
		t0 := time.Now()
		if err := nd.AdmitPayment(p); err != nil {
			t.Fatalf("admit %d refused (so NOT free/resident): %v", i, err)
		}
		d := time.Since(t0)
		if (i+1)%40 == 0 {
			t.Logf("%d pending: last admission %v", i+1, d.Round(time.Millisecond))
		}
	}
}

// (b) A live single validator: after loading N far-future coprime payments the commit loop re-sums them
// every block (dropOverdrawn), so throughput collapses though the payer's balance never moves.
func TestR3CoprimeCommitCost(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	P := payer.PublicKeyBase64()
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second, MinBlockInterval: time.Millisecond, MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000,
		Genesis: []ledger.Account{{PublicKey: P, Amount: rat("1"), ValidatorNode: P}}}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()
	rate := func(d time.Duration) float64 {
		h0 := nd.Snapshot().Height
		time.Sleep(d)
		return float64(nd.Snapshot().Height-h0) / d.Seconds()
	}
	for nd.Snapshot().Height < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	base := rate(2 * time.Second)
	const n = 140
	for i := 0; i < n; i++ {
		p, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: v.PublicKeyBase64(), Amount: coprimeAmt(i), BlockID: 1 << 40, Comment: fmt.Sprintf("%d", i)})
		if err := nd.AdmitPayment(p); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
	}
	after := rate(10 * time.Second)
	t.Logf("baseline %.1f blocks/s; after %d far-future coprime payments %.2f blocks/s; payer balance still %s",
		base, n, after, ledger.AccountAmount(nd.Snapshot().Accounts, P).FloatString(2))
}
