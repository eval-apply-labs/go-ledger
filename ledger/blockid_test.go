package ledger

import (
	"testing"
)

// Deterministic tests for the per-block binding that provides replay protection: a payment is
// included/applied only in the block whose height equals its BlockID.

func TestProposeBlockIncludesOnlyMatchingBlockID(t *testing.T) {
	accounts := []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}
	payment := signedPayment("alice", "bob", "10", 7)

	// Proposing a different block (5) must not include a payment naming block 7.
	if block := ProposeBlock(testChain(), buildLedgerState(4, accounts, rational("0.1")), zeroHash, zeroHash, BlockContent{Payments: []Payment{payment}}); len(block.Content.Payments) != 0 {
		t.Fatalf("payment for block 7 must not be included in block 5, got %d payments", len(block.Content.Payments))
	}
	// Proposing block 7 includes it.
	if block := ProposeBlock(testChain(), buildLedgerState(6, accounts, rational("0.1")), zeroHash, zeroHash, BlockContent{Payments: []Payment{payment}}); len(block.Content.Payments) != 1 {
		t.Fatalf("payment for block 7 must be included in block 7, got %d payments", len(block.Content.Payments))
	}
}

func TestApplyBlockRejectsMismatchedBlockID(t *testing.T) {
	previousState := buildLedgerState(5, []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}, rational("0.1"))
	// A block at height 6 carrying a payment that names block 5 must be rejected.
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 6, signedPayment("alice", "bob", "10", 5))); err == nil {
		t.Fatal("a block at height 6 containing a payment for block 5 must be rejected")
	}
}

// E1: only the block at the state's height plus one applies. A proposer used to be able to label its
// block with an old height and fill it with payments signed for that height, which every node applied
// again. The state carries its height, so ApplyBlock refuses such a block for every caller.
func TestApplyBlockRequiresTheNextHeight(t *testing.T) {
	state := buildLedgerState(4, []Account{namedAccount("payer", "100"), namedAccount("payee", "0"), seatedAccount()}, rational("0.1"))
	blockAt := func(height int64) Block {
		labelled := state
		labelled.Height = height - 1 // a block built as if on the state below height
		return ProposeBlock(testChain(), labelled, zeroHash, zeroHash, BlockContent{Payments: []Payment{signedPayment("payer", "payee", "10", height)}})
	}
	for _, height := range []int64{1, 3, 4, 6, 7} { // old heights, the state's own, and later ones
		if _, err := ApplyBlock(testChain(), state, zeroHash, blockAt(height)); err == nil {
			t.Errorf("a block labelled %d applied on the state at height 4", height)
		}
	}
	next, err := ApplyBlock(testChain(), state, zeroHash, blockAt(5))
	if err != nil || next.Height != 5 || balance(next.Accounts, "payee").Cmp(rational("9.9")) != 0 {
		t.Fatalf("the block at height 5 must apply and give a state at height 5 (error %v, height %d)", err, next.Height)
	}
}
