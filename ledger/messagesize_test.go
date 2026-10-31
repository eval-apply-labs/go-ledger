package ledger

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
)

// The governed per-message size cap: an oversized payment or update is never proposed, and a block
// carrying one (a proposer that bypassed intake) is rejected outright — one transaction fee cannot
// buy an arbitrarily large permanent ledger entry.
func TestMessageSizeBoundEnforcedInProposeAndApply(t *testing.T) {
	previousState := buildLedgerState(2, []Account{namedAccount("payer", "100"), namedAccount("payee", "0"), seatedAccount()}, rational("0.1"))
	previousState.Parameters.MaxMessageBytes = MinMessageBytes
	const height = 3

	small := signedPayment("payer", "payee", "1", height)
	huge, err := SignPayment(testSigner("payer"), Payment{From: "payer", To: "payee", Amount: big.NewRat(1, 1),
		Comment: strings.Repeat("x", 4*MinMessageBytes), BlockID: height})
	if err != nil {
		t.Fatal(err)
	}

	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{small, huge}})
	if len(block.Content.Payments) != 1 || block.Content.Payments[0].Comment != "" {
		t.Fatalf("ProposeBlock must carry the small payment and drop the oversized one: got %d", len(block.Content.Payments))
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, block); err != nil {
		t.Fatalf("the bounded block should apply: %v", err)
	}

	// A proposer that bypassed intake: the block carries the oversized payment → rejected.
	smuggled := block
	smuggled.Content.Payments = []Payment{small, huge}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, smuggled); err == nil {
		t.Fatal("ApplyBlock must reject a block carrying an oversized payment")
	}

	// Same bound for account updates (an unbounded description is a free permanent state write).
	hugeUpdate := signedUpdate(AccountUpdate{Name: "payer", Description: strings.Repeat("x", 4*MinMessageBytes), BlockID: height})
	block = ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{hugeUpdate}})
	if len(block.Content.AccountUpdates) != 0 {
		t.Fatal("ProposeBlock must drop an oversized account update")
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, height, hugeUpdate)); err == nil {
		t.Fatal("ApplyBlock must reject a block carrying an oversized account update")
	}
}

// Governance cannot set the cap below what a plain payment needs (that would brick the network),
// and 0 stays available as the explicit unbounded setting.
func TestMaxMessageBytesGovernanceFloor(t *testing.T) {
	base := DefaultParameters()
	for _, testCase := range []struct {
		maxMessageBytes int64
		acceptable      bool
	}{
		{0, true}, {MinMessageBytes, true}, {12000, true},
		{MinMessageBytes - 1, false}, {2000, false}, {1, false}, {-1, false},
	} {
		parameters := base
		parameters.MaxMessageBytes = testCase.maxMessageBytes
		if accepted := parameters.Acceptable(4); accepted != testCase.acceptable {
			t.Errorf("Acceptable with MaxMessageBytes=%d: got %v, want %v", testCase.maxMessageBytes, accepted, testCase.acceptable)
		}
	}
}

// Every peer decodes a proposal under 2 MiB (1 MiB before Phase 2), and updates are free: about 545
// updates of 1.9 KB pushed every honest proposal for a height past the 1 MiB, and the chain halted. The
// proposer now stops at maxBlockContentBytes (payments first), carries one update per account, and none
// for a name with no account.
func TestProposalStaysUnderTheContentBudget(t *testing.T) {
	const height, queued = 5, 600
	accounts := []Account{namedAccount("payer", "100"), namedAccount("first", "1"), seatedAccount()}
	update := func(name string, i int) AccountUpdate {
		return signedUpdate(AccountUpdate{Name: name, BlockID: height, Description: fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 1700))})
	}
	var updates []AccountUpdate
	for i := 0; i < queued; i++ {
		name := fmt.Sprintf("member-%04d", i)
		accounts = append(accounts, namedAccount(name, "1"))
		updates = append(updates, update(name, i))
	}
	updates = append([]AccountUpdate{update("first", 0), update("first", 1), update("ghost", 2)}, updates...)
	payment := signedPayment("payer", "first", "1", height)
	previousState := buildLedgerState(height-1, accounts, rational("0.1")) // MaxMessageBytes is the default

	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{payment}, AccountUpdates: updates})
	contentBytes := serializedSize(block.Content.Payments[0])
	updatedNames := map[string]bool{}
	for _, carriedUpdate := range block.Content.AccountUpdates {
		if updatedNames[carriedUpdate.Name] || carriedUpdate.Name == "ghost" {
			t.Fatalf("carried a second update for one account, or one for a name with no account")
		}
		updatedNames[carriedUpdate.Name] = true
		contentBytes += serializedSize(carriedUpdate)
	}
	if contentBytes > maxBlockContentBytes || len(block.Content.AccountUpdates) >= queued {
		t.Fatalf("carried %d of %d updates, %d content bytes: want the budget (%d) to cut the block", len(block.Content.AccountUpdates), queued, contentBytes, maxBlockContentBytes)
	}
	if proposal, _ := json.Marshal(block); len(proposal) >= 2<<20 {
		t.Fatalf("the proposal is %d bytes, over the 2 MiB every peer decodes", len(proposal))
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, block); err != nil {
		t.Fatalf("the budgeted block should apply: %v", err)
	}
}

// The message bound fits a payment by name, and little more (spec 4.1 and 2.4). Its floor,
// MinMessageBytes, fits the largest payment whose comment needs no escaping: a 280-byte comment (the
// default comment limit), two 32-byte names, a pin, a 27-character amount and a 19-digit block id. Its
// default fits that payment with every comment byte escaped, as JSON writes <, > and & in six bytes.
// Payments no longer carry two 1952-byte keys, so a vote may now bound messages at the floor, and the
// default refuses a payment padded with a 2000-byte comment, seven times the default comment limit.
func TestMinimumMessageBoundFitsAPayment(t *testing.T) {
	largest := func(comment string) Payment {
		payment, _ := SignPayment(testSigner("payer"), Payment{From: strings.Repeat("n", 32), To: strings.Repeat("m", 32),
			Amount: rational("999999999999999999.99999999"), Comment: comment, BlockID: math.MaxInt64, PreviousStateDigest: strings.Repeat("ab", 32)})
		return payment
	}
	plain, escaped, padded := largest(strings.Repeat("x", 280)), largest(strings.Repeat("<", 280)), largest(strings.Repeat("x", 2000))
	defaultBound := DefaultParameters().MaxMessageBytes
	t.Logf("the largest payment: %d bytes, %d with its comment escaped", serializedSize(plain), serializedSize(escaped))
	if !WithinMessageBound(MinMessageBytes, plain) {
		t.Errorf("the floor, %d bytes, does not fit the largest payment (%d bytes)", MinMessageBytes, serializedSize(plain))
	}
	if !WithinMessageBound(defaultBound, escaped) {
		t.Errorf("the default bound, %d bytes, does not fit the largest payment with its comment escaped (%d bytes)", defaultBound, serializedSize(escaped))
	}
	if WithinMessageBound(defaultBound, padded) {
		t.Errorf("the default bound, %d bytes, fits a payment padded with a 2000-byte comment (%d bytes)", defaultBound, serializedSize(padded))
	}
	atTheFloor := DefaultParameters()
	atTheFloor.MaxMessageBytes = 5000
	if !atTheFloor.Acceptable(4) {
		t.Error("a vote may not bound messages at 5000 bytes, though every payment by name fits")
	}
}
