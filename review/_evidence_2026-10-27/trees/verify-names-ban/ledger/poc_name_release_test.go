package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): ProcessPayments drops any account whose balance reaches zero
// (ledger.go:209), and with it the account's Name, delegation, Address and Description. The name is
// thereby released with no signed update from its owner, and applyAccountUpdates (which runs AFTER
// payments, against the post-payment accounts) lets any other account claim it in the SAME block.
// Payments signed by name for a later block then resolve to the new holder at apply time.
//
// The user manual suggests moving to a new machine by "creat[ing] a fresh wallet there and pay[ing]
// yourself to it" — i.e. emptying the old account — and documents pay-by-name as paying "whoever holds
// it on the ledger", but nowhere says that emptying (or being banned/slashed) releases the name.
// Asserts a registered name never changes owner without that owner's signature; FAILS today.
func TestPoCZeroBalanceReleasesNameToAnotherKey(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	alice, _ := signing.Generate(scheme, network)
	bob, _ := signing.Generate(scheme, network)
	other, _ := signing.Generate(scheme, network)
	A, B, O := alice.PublicKeyBase64(), bob.PublicKeyBase64(), other.PublicKeyBase64()
	cost := rat("0.1")
	state := st([]Account{
		{PublicKey: A, Name: "alice", Amount: rat("10"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("50"), ValidatorNode: B},
		{PublicKey: O, Amount: rat("1"), ValidatorNode: O},
	}, cost)

	// Bob signs a pay-by-name payment to "alice" for block 6 (normal wallet lookahead).
	toAlice, _ := SignPayment(bob, Payment{PublicKeyFrom: B, ToName: "alice", Amount: rat("20"), BlockID: 6})

	// Block 5: Alice moves her whole balance to her new wallet (the manual's migration advice); the
	// same block carries another account's (fee-free) update claiming "alice".
	newWallet, _ := signing.Generate(scheme, network)
	move, _ := SignPayment(alice, Payment{PublicKeyFrom: A, PublicKeyTo: newWallet.PublicKeyBase64(), Amount: rat("10"), BlockID: 5})
	claim, _ := SignAccountUpdate(other, AccountUpdate{From: O, Name: "alice", BlockID: 5})
	blk5 := ProposeBlock(scheme, network, 5, state, BlockContent{Payments: []Payment{move}, Updates: []AccountUpdate{claim}})
	state5, ok := ApplyBlock(scheme, network, state, blk5)
	if !ok {
		t.Fatal("setup: block 5 should apply")
	}
	owner := nameRegistry(state5.Accounts)["alice"]
	t.Logf("after block 5: \"alice\" owned by other=%v (alice's key=%v)", owner == O, owner == A)

	blk6 := ProposeBlock(scheme, network, 6, state5, BlockContent{Payments: []Payment{toAlice}})
	state6, ok := ApplyBlock(scheme, network, state5, blk6)
	if !ok {
		t.Fatal("setup: block 6 should apply")
	}
	t.Logf("after block 6: other's balance=%s (was 1), Bob's pay-by-name payment signed for \"alice\" credited it",
		balance(state6.Accounts, O).FloatString(2))
	if owner != A && owner != "" {
		t.Errorf("a registered name moved to a different key without its owner's signature (released by zero-balance deletion)")
	}
}
