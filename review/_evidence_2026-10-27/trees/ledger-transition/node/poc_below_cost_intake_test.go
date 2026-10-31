package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): admitPayment refuses only non-positive amounts, not amounts below
// the transaction cost. ProcessPayments drops a below-cost payment before debiting anything (no fee),
// but ProposeBlock does not filter it, so an HONEST node admits it (receipt), gossips it, and carries
// it into its own blocks as a fee-free no-op. Asserts intake refuses it; FAILS today.
func TestPoCBelowCostPaymentAdmittedAndCarriedFree(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	V, P := v.PublicKeyBase64(), payer.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: P, Amount: rat("1"), ValidatorNode: P}}
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: V, Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis, MaxMempool: 10000, MaxMessageBytes: 2000}, NewHTTPTransport(nil))

	admitted := 0
	for i := 0; i < 50; i++ {
		p, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: V, Amount: rat("0.001"), BlockID: 1, Comment: string(rune('A' + i))})
		if nd.admitPayment(p) == nil {
			admitted++
		}
	}
	blk := ledger.ProposeBlock(nd.cfg.Scheme, nd.cfg.NetworkID, 1, ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters}, ledger.BlockContent{Payments: nd.mem.snapshot()}, nd.cfg.Validators)
	next, ok := ledger.ApplyBlock(nd.cfg.Scheme, nd.cfg.NetworkID, ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters}, blk, nd.cfg.Validators)
	t.Logf("below-cost (0.001 < fee 0.1) payments admitted by honest intake: %d/50; carried in the honest proposal: %d; applies=%v; payer balance after=%s (fees charged: none)",
		admitted, len(blk.Payments), ok, ledger.AccountAmount(next.Accounts, P).FloatString(3))
	if admitted > 0 {
		t.Errorf("honest intake admitted %d below-cost payments that the ledger will drop without charging a fee", admitted)
	}
}

// Companion: with no upper bound on BlockID at intake and dropBelow evicting only past blocks, the
// same fee-free below-cost payments naming a far-future block occupy the bounded mempool indefinitely
// at ~zero balance (the overdraw guard sums amounts, so tiny amounts lock almost nothing), and an
// ordinary honest payment is then refused. Asserts the honest payment is admitted; FAILS today.
func TestPoCBelowCostFarFutureFillsMempool(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	filler, _ := signing.Generate(signing.Ed25519{}, "testnet")
	honest, _ := signing.Generate(signing.Ed25519{}, "testnet")
	V, F, H := v.PublicKeyBase64(), filler.PublicKeyBase64(), honest.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: F, Amount: rat("1"), ValidatorNode: F}, {PublicKey: H, Amount: rat("100"), ValidatorNode: H}}
	const poolCap = 50 // stands in for the default 10000
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: V, Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis, MaxMempool: poolCap, MaxMessageBytes: 2000}, NewHTTPTransport(nil))
	for i := 0; i < poolCap; i++ {
		p, _ := ledger.SignPayment(filler, ledger.Payment{PublicKeyFrom: F, PublicKeyTo: V, Amount: rat("1/1000000000"), BlockID: 1 << 40, Comment: string(rune('A' + i))})
		_ = nd.admitPayment(p)
	}
	nd.mem.dropBelow(1000) // what commit does as heights advance: far-future entries survive
	normal, _ := ledger.SignPayment(honest, ledger.Payment{PublicKeyFrom: H, PublicKeyTo: V, Amount: rat("5"), BlockID: 3})
	err := nd.admitPayment(normal)
	t.Logf("pool holds %d far-future fee-free entries locking %s units of the filler's balance; honest 5-unit payment: err=%v",
		len(nd.mem.pending), nd.mem.outgoingFor(F).FloatString(9), err)
	if err != nil {
		t.Errorf("an ordinary honest payment was refused because fee-free far-future entries hold the bounded mempool: %v", err)
	}
}
