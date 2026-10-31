package ledger

import (
	"fmt"
	"testing"
)

// The optional fork-binding tier: a payment carrying a PreviousStateDigest is included only in a block
// built on the state whose full digest (LedgerState.Digest, the one /hdls/resolve serves) it pins, so it
// cannot be replayed onto a fork with a different history. Phase 1 compared the accounts digest, so a pin
// made as documented never landed (E38): a pin to the accounts digest is now one to another state.
func TestPinUsesTheFullStateDigest(t *testing.T) {
	previousState := buildLedgerState(4, []Account{namedAccount("payer", "1000"), namedAccount("payee", "0"), seatedAccount()}, rational("0.1"))
	paymentPinnedTo := func(pinnedDigest string) Payment {
		payment, _ := SignPayment(testSigner("payer"), Payment{From: "payer", To: "payee", Amount: rational("100"), BlockID: 5, PreviousStateDigest: pinnedDigest})
		return payment
	}

	// Pinned to the state the block builds on → included and applies.
	good := paymentPinnedTo(digestHex(previousState.Digest()))
	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{good}})
	if len(block.Content.Payments) != 1 {
		t.Fatal("payment pinned to the state digest of the state the block builds on should be included")
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, block); err != nil {
		t.Fatalf("a block with a correctly-pinned payment should apply: %v", err)
	}

	for name, wrongPin := range map[string]string{
		"the accounts digest (E38)": digestHex(AccountsDigest(previousState.Accounts)),
		"another history":           "00000000000000000000000000000000000000000000000000000000deadbeef",
	} {
		// Excluded by the proposer...
		wrong := paymentPinnedTo(wrongPin)
		if included := len(ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{wrong}}).Content.Payments); included != 0 {
			t.Fatalf("a payment pinned to %s must be excluded, got %d included", name, included)
		}
		// ...and refused in a block that smuggles it in, validly signed.
		tampered := block
		tampered.Content.Payments = []Payment{wrong}
		if _, err := ApplyBlock(testChain(), previousState, zeroHash, tampered); err == nil {
			t.Fatalf("ApplyBlock must refuse a payment pinned to %s", name)
		}
	}

	// An unpinned payment (empty PreviousStateDigest) is unaffected — the default path still works.
	if included := len(ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{paymentPinnedTo("")}}).Content.Payments); included != 1 {
		t.Fatalf("an unpinned payment should still be included, got %d", included)
	}
}

// The pre-block state's digest hashes every account, and only a pinned payment reads it. So a block
// whose payments are unpinned is proposed and applied without it: the same block with its payment
// pinned costs one digest more. The digest is measured by its allocations, which grow with the number
// of accounts.
func TestOnlyAPinnedPaymentHashesThePreBlockState(t *testing.T) {
	accounts := []Account{namedAccount("payer", "1000"), namedAccount("payee", "0"), seatedAccount()}
	for i := range 2000 {
		accounts = append(accounts, Account{Name: fmt.Sprintf("holder-%04d", i), Amount: rational("1")})
	}
	previousState := buildLedgerState(4, accounts, rational("0.1"))
	oneDigest := testing.AllocsPerRun(3, func() { previousState.Digest() })
	cost := func(pin string) (proposing, applying float64) {
		payment, _ := SignPayment(testSigner("payer"), Payment{From: "payer", To: "payee", Amount: rational("100"), BlockID: 5, PreviousStateDigest: pin})
		content := BlockContent{Payments: []Payment{payment}}
		block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, content)
		if _, err := ApplyBlock(testChain(), previousState, zeroHash, block); err != nil || len(block.Content.Payments) != 1 {
			t.Fatalf("the payment pinned to %q was not carried, or its block does not apply: %v", pin, err)
		}
		proposing = testing.AllocsPerRun(3, func() { ProposeBlock(testChain(), previousState, zeroHash, zeroHash, content) })
		applying = testing.AllocsPerRun(3, func() { _, _ = ApplyBlock(testChain(), previousState, zeroHash, block) })
		return proposing, applying
	}
	unpinnedProposing, unpinnedApplying := cost("")
	pinnedProposing, pinnedApplying := cost(digestHex(previousState.Digest()))
	if pinnedProposing-unpinnedProposing < oneDigest/2 || pinnedApplying-unpinnedApplying < oneDigest/2 {
		t.Errorf("a pin adds %.0f allocations to proposing and %.0f to applying, want about one digest (%.0f): the unpinned block hashes the pre-block state too",
			pinnedProposing-unpinnedProposing, pinnedApplying-unpinnedApplying, oneDigest)
	}
}
