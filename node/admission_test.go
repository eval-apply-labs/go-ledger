package node

import (
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

func TestDeriveValidatorsByStake(t *testing.T) {
	accounts := []ledger.Account{
		{PublicKey: "A", Amount: rat("1000"), ValidatorNode: "A"}, // stakes to itself, over threshold
		{PublicKey: "B", Amount: rat("50"), ValidatorNode: "B"},   // under threshold
		{PublicKey: "C", Amount: rat("120"), ValidatorNode: "V"},  // delegates to V
		{PublicKey: "D", Amount: rat("30"), ValidatorNode: "V"},   // delegates to V (V total 150)
	}
	vs := deriveValidators(accounts, rat("100"))
	if !vs.Contains("A") {
		t.Fatal("A (stake 1000) should be a validator")
	}
	if vs.Contains("B") {
		t.Fatal("B (stake 50 < 100) should NOT be a validator")
	}
	if !vs.Contains("V") {
		t.Fatal("V (delegated 120+30=150) should be a validator")
	}
	if vs.Power("A") != 1000 || vs.Power("V") != 150 {
		t.Fatalf("powers: A=%d (want 1000), V=%d (want 150)", vs.Power("A"), vs.Power("V"))
	}
}

// Dynamic admission: a funded payment lifts a new account's stake over the threshold, and it joins
// the validator set the committed state yields — no static config, no explicit admission message.
func TestDynamicValidatorAdmission(t *testing.T) {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: a.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: a.PublicKeyBase64()},
	}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a,
		Validators: nil, ValidatorThreshold: rat("100"), // DYNAMIC — set derived from accounts
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis,
	}, NewHTTPTransport(nil))

	if deriveValidators(genesis, rat("100")).Contains(b.PublicKeyBase64()) {
		t.Fatal("B should not be a validator at genesis")
	}

	// Pay B over the threshold (B receives 200 − fee > 100).
	pay, _ := ledger.SignPayment(a, ledger.Payment{
		PublicKeyFrom: a.PublicKeyBase64(), PublicKeyTo: b.PublicKeyBase64(), Amount: rat("200"), BlockID: 1,
	})
	nd.SubmitPayment(pay)
	nd.Start()
	defer nd.Stop()

	waitFor(t, 4*time.Second, func() bool {
		return deriveValidators(nd.Snapshot().Accounts, rat("100")).Contains(b.PublicKeyBase64())
	}, "B was not admitted as a validator after being funded over the threshold")
}

// The governed per-message size cap at intake: an oversized payment is refused with no receipt and
// not gossiped, so one fee can't buy an arbitrarily large permanent ledger entry (see also the
// ledger-level enforcement in ProposeBlock/ApplyBlock, which catches a proposer that bypassed this).
func TestIntakeRefusesOversizedPayment(t *testing.T) {
	founder, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: founder.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: founder.PublicKeyBase64()},
	}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: founder,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 50 * time.Millisecond,
		MaxMessageBytes: ledger.MinMessageBytes,
		Genesis:         genesis,
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()

	small, _ := ledger.SignPayment(founder, ledger.Payment{
		PublicKeyFrom: founder.PublicKeyBase64(), PublicKeyTo: "B", Amount: rat("1"), BlockID: 100000,
	})
	if err := nd.AdmitPayment(small); err != nil {
		t.Fatalf("a plain payment should fit the bound, got: %v", err)
	}
	huge, _ := ledger.SignPayment(founder, ledger.Payment{
		PublicKeyFrom: founder.PublicKeyBase64(), PublicKeyTo: "B", Amount: rat("1"),
		Comment: strings.Repeat("x", 4*ledger.MinMessageBytes), BlockID: 100000,
	})
	if err := nd.AdmitPayment(huge); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("an oversized payment must be refused at intake, got: %v", err)
	}
}
