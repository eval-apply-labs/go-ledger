package node

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// The intake guardrail: a payment that would overdraw the payer's balance — counting outgoing
// already promised in the mempool — is refused with no receipt, so an honest user can't stumble
// into the ledger's overdraw ban (which destroys the payer's whole balance) by double-sending.
func TestIntakeRefusesOverdraw(t *testing.T) {
	founder, _ := signing.Generate(signing.Ed25519{}, "testnet")
	stranger, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: founder.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: founder.PublicKeyBase64()},
	}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: founder,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 50 * time.Millisecond,
		Genesis: genesis,
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()

	// Far-future block, so the payments stay pending for the whole test.
	pay := func(signer *signing.Signer, amount, comment string) ledger.Payment {
		p, _ := ledger.SignPayment(signer, ledger.Payment{
			PublicKeyFrom: signer.PublicKeyBase64(), PublicKeyTo: stranger.PublicKeyBase64(),
			Amount: rat(amount), Comment: comment, BlockID: 100000,
		})
		return p
	}

	first := pay(founder, "600", "first")
	if err := nd.AdmitPayment(first); err != nil {
		t.Fatalf("a 600-of-1000 payment should be admitted, got: %v", err)
	}
	// A second individually-valid payment that would take the block total to 1200 > 1000: the exact
	// shape that used to send an honest double-sender to the shadow realm.
	if err := nd.AdmitPayment(pay(founder, "600", "second")); err == nil || !strings.Contains(err.Error(), "overdraw") {
		t.Fatalf("a payment overdrawing the promised total must be refused, got: %v", err)
	}
	// Resubmitting the queued payment is an idempotent no-op (a wallet retry re-earns its receipt).
	if err := nd.AdmitPayment(first); err != nil {
		t.Fatalf("resubmitting an already-queued payment should be accepted, got: %v", err)
	}
	// A payer with no account has balance 0 — nothing to promise.
	if err := nd.AdmitPayment(pay(stranger, "1", "unfunded")); err == nil {
		t.Fatal("a payment from an unfunded account must be refused")
	}
	// A payment naming an already-committed block can never clear.
	stale, _ := ledger.SignPayment(founder, ledger.Payment{
		PublicKeyFrom: founder.PublicKeyBase64(), PublicKeyTo: stranger.PublicKeyBase64(),
		Amount: rat("1"), Comment: "stale", BlockID: 0,
	})
	if err := nd.AdmitPayment(stale); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a payment naming a past block must be refused as stale, got: %v", err)
	}
}

// Intake refuses a payment that resolves to nobody — an unregistered name, or a payment naming
// both/neither a key and a name — instead of admitting it, relaying it, and handing back a receipt
// for what the ledger will silently drop (which the sender would mistake for a completed payment).
func TestIntakeRefusesUnresolvableRecipient(t *testing.T) {
	founder, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: founder.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: founder.PublicKeyBase64()},
	}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: founder,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 50 * time.Millisecond,
		Genesis: genesis,
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()

	toName := func(name string) ledger.Payment {
		p, _ := ledger.SignPayment(founder, ledger.Payment{
			PublicKeyFrom: founder.PublicKeyBase64(), ToName: name, Amount: rat("1"), BlockID: 100000,
		})
		return p
	}
	if err := nd.AdmitPayment(toName("nobody")); err == nil || !strings.Contains(err.Error(), "unresolvable") {
		t.Fatalf("a payment to an unregistered name must be refused, got: %v", err)
	}
	// A registered name is admitted (founder names itself in genesis).
	if err := nd.AdmitPayment(toName("founder")); err != nil {
		t.Fatalf("a payment to a registered name must be admitted, got: %v", err)
	}
}

// After a commit changes balances under the pool (payments admitted by other nodes, a
// reassignment, a slash), pending payments that now overdraw are dropped, keeping the invariant
// that this node can never propose a block the ledger would answer with an overdraw ban.
func TestMempoolDropsOverdrawnOnBalanceDrop(t *testing.T) {
	m := newMempool(0, 0)
	bigPay := ledger.Payment{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("600"), Comment: "big", BlockID: 10, Signature: "s1"}
	smallPay := ledger.Payment{PublicKeyFrom: "A", PublicKeyTo: "C", Amount: rat("300"), Comment: "small", BlockID: 11, Signature: "s2"}
	if !m.add(bigPay) || !m.add(smallPay) {
		t.Fatal("both payments should enter the pool")
	}

	// A's balance drops from 1000 to 500: the 600 no longer fits; the 300 still does.
	m.dropOverdrawn(func(payer string) *big.Rat { return rat("500") })

	pending := m.snapshot()
	if len(pending) != 1 || pending[0].Comment != "small" {
		t.Fatalf("only the still-affordable payment should remain, got %v", pending)
	}
	// The dropped payment is forgotten, so it can come back once the balance allows again.
	if m.contains(bigPay) {
		t.Fatal("a dropped payment must be forgotten (resubmittable after a refill)")
	}
	m.dropOverdrawn(func(payer string) *big.Rat { return rat("900") })
	if !m.add(bigPay) {
		t.Fatal("the dropped payment should be re-addable")
	}
}
