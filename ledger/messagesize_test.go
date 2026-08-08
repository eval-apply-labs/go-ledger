package ledger

import (
	"math/big"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// The governed per-message size cap: an oversized payment or update is never proposed, and a block
// carrying one (a proposer that bypassed intake) is rejected outright — one transaction fee cannot
// buy an arbitrarily large permanent ledger entry.
func TestMessageSizeBoundEnforcedInProposeAndApply(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	payer, _ := signing.Generate(scheme, network)
	A := payer.PublicKeyBase64()
	prev := LedgerState{
		Accounts:   []Account{{PublicKey: A, Amount: big.NewRat(100, 1), ValidatorNode: A}},
		Parameters: LedgerParameters{TransactionCost: big.NewRat(1, 10), MaxMessageBytes: MinMessageBytes},
	}
	const height = 3

	small, err := SignPayment(payer, Payment{PublicKeyFrom: A, PublicKeyTo: "B", Amount: big.NewRat(1, 1), BlockID: height})
	if err != nil {
		t.Fatal(err)
	}
	huge, err := SignPayment(payer, Payment{PublicKeyFrom: A, PublicKeyTo: "B", Amount: big.NewRat(1, 1),
		Comment: strings.Repeat("x", 4*MinMessageBytes), BlockID: height})
	if err != nil {
		t.Fatal(err)
	}

	blk := ProposeBlock(scheme, network, height, prev, BlockContent{Payments: []Payment{small, huge}})
	if len(blk.Payments) != 1 || blk.Payments[0].Comment != "" {
		t.Fatalf("ProposeBlock must carry the small payment and drop the oversized one: got %d", len(blk.Payments))
	}
	if _, ok := ApplyBlock(scheme, network, prev, blk); !ok {
		t.Fatal("the bounded block should apply")
	}

	// A proposer that bypassed intake: the block carries the oversized payment → rejected.
	smuggled := blk
	smuggled.Payments = []Payment{small, huge}
	if _, ok := ApplyBlock(scheme, network, prev, smuggled); ok {
		t.Fatal("ApplyBlock must reject a block carrying an oversized payment")
	}

	// Same bound for account updates (an unbounded description is a free permanent state write).
	hugeUpdate, err := SignAccountUpdate(payer, AccountUpdate{From: A,
		Description: strings.Repeat("x", 4*MinMessageBytes), BlockID: height})
	if err != nil {
		t.Fatal(err)
	}
	blk = ProposeBlock(scheme, network, height, prev, BlockContent{Updates: []AccountUpdate{hugeUpdate}})
	if len(blk.Updates) != 0 {
		t.Fatal("ProposeBlock must drop an oversized account update")
	}
	smuggled = blk
	smuggled.Updates = []AccountUpdate{hugeUpdate}
	if _, ok := ApplyBlock(scheme, network, prev, smuggled); ok {
		t.Fatal("ApplyBlock must reject a block carrying an oversized account update")
	}
}

// Governance cannot set the cap below what a plain payment needs (that would brick the network),
// and 0 stays available as the explicit unbounded setting.
func TestMaxMessageBytesGovernanceFloor(t *testing.T) {
	base := LedgerParameters{TransactionCost: big.NewRat(1, 10), StepTimeoutMs: 1000}
	for _, tc := range []struct {
		maxMessageBytes int
		acceptable      bool
	}{
		{0, true}, {MinMessageBytes, true}, {2000, true},
		{MinMessageBytes - 1, false}, {1, false}, {-1, false},
	} {
		p := base
		p.MaxMessageBytes = tc.maxMessageBytes
		if got := p.Acceptable(4); got != tc.acceptable {
			t.Errorf("Acceptable with MaxMessageBytes=%d: got %v, want %v", tc.maxMessageBytes, got, tc.acceptable)
		}
	}
}
