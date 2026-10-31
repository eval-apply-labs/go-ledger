package node

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func coprimeAmount(cost *big.Rat, i int) *big.Rat {
	base := new(big.Int).Lsh(big.NewInt(1), 2600)
	base.Add(base, big.NewInt(int64(1+i)))
	return new(big.Rat).Add(cost, new(big.Rat).SetFrac(big.NewInt(1), base))
}

// (a) Intake cost: admitPayment's overdraw guard re-sums the payer's pending amounts on EVERY admission
// (mempool.outgoingFor), so pending coprime-denominator payments make each admission slower on the
// single event loop, before any block. Measured through AdmitPayment (what /hdls/pay calls).
func TestVerifyC08IntakeCostOfPendingCoprimeAmounts(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payee, _ := signing.Generate(signing.Ed25519{}, "testnet")
	P, Q := payer.PublicKeyBase64(), payee.PublicKeyBase64()
	cost := rat("0.1")
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: cost, Timeout: time.Second, MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000,
		Genesis: []ledger.Account{{PublicKey: P, Amount: rat("100000"), ValidatorNode: P}}}, NewHTTPTransport(nil))
	go nd.run() // the event loop only (no consensus), so the payments stay pending
	defer nd.Stop()
	const n = 160
	start := time.Now()
	var last time.Duration
	for i := 0; i < n; i++ {
		p, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: Q, Amount: coprimeAmount(cost, i), BlockID: 5})
		if !ledger.WithinMessageBound(2000, p) {
			t.Fatal("must fit the cap")
		}
		t0 := time.Now()
		if err := nd.AdmitPayment(p); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		last = time.Since(t0)
		if (i+1)%20 == 0 {
			t.Logf("after %d pending: last admission took %v, total %v", i+1, last.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
		}
	}
}

// (b) Governance cannot reassign a bloated balance: Reassignment binds the EXACT PreviousAmount, so
// once a balance's encoding passes the 1 MiB body bound, no vote for it can be POSTed or gossiped
// (and no proposal could carry it). Balance built the way C08 builds it (LCM of ~700 coprime d_i).
func TestVerifyC08BloatedBalanceCannotBeReassigned(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	thief, _ := signing.Generate(signing.Ed25519{}, "testnet")
	V, T := v.PublicKeyBase64(), thief.PublicKeyBase64()
	den := big.NewInt(1)
	base := new(big.Int).Lsh(big.NewInt(1), 2600)
	for i := 0; i < 700; i++ {
		d := new(big.Int).Add(base, big.NewInt(int64(1+i)))
		g := new(big.Int).GCD(nil, nil, den, d)
		den.Mul(den, new(big.Int).Quo(d, g)) // lcm
	}
	bal := new(big.Rat).Add(rat("5000"), new(big.Rat).SetFrac(big.NewInt(1), den))
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: V, Power: 1}})
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: time.Second, MaxMempool: 10000, MaxMessageBytes: 2000,
		Genesis: []ledger.Account{{PublicKey: T, Amount: bal, ValidatorNode: T}}}, NewHTTPTransport(nil))
	srv := httptest.NewServer(NewServer(nd).Handler())
	defer srv.Close()
	go nd.run()
	defer nd.Stop()
	vote, _ := ledger.SignReassignmentVote(v, T, bal, rat("0"), 0)
	r := ledger.Reassignment{Account: T, PreviousAmount: bal, NewAmount: rat("0"), Sequence: 0, Votes: []ledger.ReassignmentVote{vote}}
	body, _ := json.Marshal(r)
	resp, err := http.Post(srv.URL+"/hdls/reassignment-vote", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Logf("thief balance encodes to %d bytes; a quorum-1 claw-back vote is %d bytes; POST /hdls/reassignment-vote -> HTTP %d",
		len(bal.RatString()), len(body), resp.StatusCode)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("a fully ratified claw-back of the bloated account cannot even be submitted (HTTP %d)", resp.StatusCode)
	}
}
