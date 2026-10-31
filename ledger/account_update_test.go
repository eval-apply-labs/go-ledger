package ledger

import (
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// find is the account named `name`, or the zero Account when there is none.
func find(accounts []Account, name string) Account {
	account, _ := FindAccount(accounts, name)
	return account
}

// signedUpdate is update signed under the key of its test account, update.Name.
func signedUpdate(update AccountUpdate) AccountUpdate {
	signed, err := SignAccountUpdate(testSigner(update.Name), update)
	if err != nil {
		panic(err)
	}
	return signed
}

// forgeUpdateBlock is the block at height that carries updates on previousState, with the signing set's
// hash and the digest of the state they result in: a block whose only fault can be a rule about the
// updates themselves.
func forgeUpdateBlock(previousState LedgerState, height int64, updates ...AccountUpdate) Block {
	next := previousState
	next.Accounts, next.Height = applyAccountUpdates(previousState.Accounts, updates), height
	return Block{Height: height, ValidatorsHash: signingSetHash(testChain(), previousState), Content: BlockContent{AccountUpdates: updates}, StateDigest: next.Digest()}
}

// An update delegates an account's stake to a NAME, and sets its description: in a dynamic chain the
// delegate is then seated with that stake. An update signed under any key but its account's, another
// account's key included, is left out of a block, and a block that carries one is invalid.
func TestUpdateDelegatesToAName(t *testing.T) {
	previousState := buildLedgerState(4, []Account{namedAccount("alice", "100"), namedAccount("bob", "100"), seatedAccount()}, rational("0.1"))
	update := signedUpdate(AccountUpdate{Name: "alice", Delegate: "bob", Description: "hi", BlockID: 5})
	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}})
	next, err := ApplyBlock(testChain(), previousState, zeroHash, block)
	if err != nil {
		t.Fatalf("a block carrying a valid account update should apply: %v", err)
	}
	if alice := find(next.Accounts, "alice"); alice.Delegate != "bob" || alice.Description != "hi" {
		t.Fatalf("update not applied: delegate=%q description=%q", alice.Delegate, alice.Description)
	}
	if validators := testChain().ValidatorRule.SetFor(next); validators.Power("bob") != 100 || validators.Contains("alice") {
		t.Fatalf("bob should be seated with alice's 100, and alice not at all: %+v", validators.Validators())
	}

	forged, _ := SignAccountUpdate(testSigner("bob"), AccountUpdate{Name: "alice", Delegate: "bob", Description: "hi", BlockID: 5}) // alice's update, bob's key
	if carried := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{forged}}); len(carried.Content.AccountUpdates) != 0 {
		t.Fatal("ProposeBlock carried an update of alice's signed under bob's key")
	}
	tamperedBlock := block // the same fields, so the same state digest: only the signature check can refuse it
	tamperedBlock.Content.AccountUpdates = []AccountUpdate{forged}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, tamperedBlock); err == nil {
		t.Fatal("ApplyBlock accepted an update of alice's signed under bob's key")
	}
}

// "-" (ClearField) sets a delegate, a description or an address to none, and "" leaves it as it is.
// Without it a delegation, or an address a validator published, could never be withdrawn.
func TestUpdateClearsAFieldWithADash(t *testing.T) {
	alice := namedAccount("alice", "100")
	alice.Delegate, alice.Description, alice.Address = "bob", "hi", "http://10.0.0.2:8080"
	previousState := buildLedgerState(4, []Account{alice, namedAccount("bob", "100"), seatedAccount()}, rational("0.1"))

	keep := signedUpdate(AccountUpdate{Name: "alice", Description: "hello", BlockID: 5})
	next, err := ApplyBlock(testChain(), previousState, zeroHash, ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{keep}}))
	if after := find(next.Accounts, "alice"); err != nil || after.Delegate != "bob" || after.Description != "hello" || after.Address != "http://10.0.0.2:8080" {
		t.Fatalf("an update of the description alone changed other fields (error %v): %+v", err, after)
	}

	clear := signedUpdate(AccountUpdate{Name: "alice", Delegate: ClearField, Description: ClearField, Address: ClearField, BlockID: 6})
	block := ProposeBlock(testChain(), next, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{clear}})
	cleared, err := ApplyBlock(testChain(), next, zeroHash, block)
	if after := find(cleared.Accounts, "alice"); err != nil || len(block.Content.AccountUpdates) != 1 || after.Delegate != "" || after.Description != "" || after.Address != "" {
		t.Fatalf("\"-\" did not clear the fields (error %v): %+v", err, after)
	}
	if testChain().ValidatorRule.SetFor(cleared).Contains("bob") {
		t.Fatal("once alice withdrew her delegation, nothing backs bob, yet he is seated")
	}
}

// A delegate must be a name with an account that is not frozen, and an update must come from an
// account that is not frozen: no block carries any other update. Delegating to a name nobody holds
// would back no one, and a frozen name changes nothing.
func TestDelegationToAnUnknownOrFrozenNameIsRefused(t *testing.T) {
	frozen := namedAccount("frozen", "100")
	frozen.Frozen = true
	previousState := buildLedgerState(4, []Account{namedAccount("alice", "100"), frozen, seatedAccount()}, rational("0.1"))
	for name, update := range map[string]AccountUpdate{
		"to a name with no account": signedUpdate(AccountUpdate{Name: "alice", Delegate: "nobody", BlockID: 5}),
		"to a frozen name":          signedUpdate(AccountUpdate{Name: "alice", Delegate: "frozen", BlockID: 5}),
		"from a frozen name":        signedUpdate(AccountUpdate{Name: "frozen", Description: "still here", BlockID: 5}),
		"changing nothing":          signedUpdate(AccountUpdate{Name: "alice", BlockID: 5}),
	} {
		if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}}); len(block.Content.AccountUpdates) != 0 {
			t.Errorf("%s: ProposeBlock carried the update", name)
		}
		if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, update)); err == nil {
			t.Errorf("%s: ApplyBlock accepted a block carrying the update", name)
		}
	}
	accepted := signedUpdate(AccountUpdate{Name: "alice", Delegate: "alice", BlockID: 5})
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, accepted)); err != nil {
		t.Fatalf("a delegation to an existing name that is not frozen was refused: %v", err)
	}
}

// An update's signature covers each of its fields, in the spec's order: without Delegate a relay could
// add a delegation to a signed description change, and without BlockID replay it in a later block. A
// receipt's covers the node's name, the payment id and the block.
func TestUpdateAndReceiptSignaturesCoverEveryField(t *testing.T) {
	scheme, network := signing.MLDSA65{}, testNetwork
	alice := testSigner("alice")
	update := signedUpdate(AccountUpdate{Name: "alice", Delegate: "bank-a", Description: "hi", Address: ClearField, BlockID: 5})
	updateFields := [][]byte{[]byte("alice"), []byte("bank-a"), []byte("hi"), []byte("-"), []byte("5")}
	if !signing.VerifyBase64(scheme, network, alice.PublicKeyBase64(), signing.DomainAccountUpdate, update.Signature, updateFields...) {
		t.Error("an update is not signed over its name, delegate, description, address and block")
	}

	receipt, err := SignReceipt(alice, "alice", signedPayment("alice", "bob", "12.5", 3))
	if err != nil {
		t.Fatal(err)
	}
	receiptFields := [][]byte{[]byte("alice"), []byte(receipt.PaymentID), []byte("3")}
	if !signing.VerifyBase64(scheme, network, alice.PublicKeyBase64(), signing.DomainReceipt, receipt.Signature, receiptFields...) {
		t.Error("a receipt is not signed over the node's name, the payment id and the block")
	}
}

// An update names the one block that may carry it, like a payment (rule 20): no other block carries it,
// so a signed update cannot be replayed later.
func TestUpdateNamesItsBlock(t *testing.T) {
	previousState := buildLedgerState(4, []Account{namedAccount("alice", "100"), seatedAccount()}, rational("0.1"))
	for _, blockID := range []int64{4, 6} {
		update := signedUpdate(AccountUpdate{Name: "alice", Description: "hi", BlockID: blockID})
		if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}}); len(block.Content.AccountUpdates) != 0 {
			t.Errorf("block 5 carried an update for block %d", blockID)
		}
		if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, update)); err == nil || !strings.Contains(err.Error(), "replay guard") {
			t.Errorf("block 5 carrying an update for block %d: got %v, want the replay guard's refusal", blockID, err)
		}
	}
}

// A block carries at most one update per name, and none of a name the block locks or to it (rule 21):
// two updates of one name would make the order of a block's updates matter, and a name the block
// freezes changes nothing.
func TestOneUpdatePerNameAndNoneForALockedName(t *testing.T) {
	chain, state := requestFixture()
	first := signedUpdate(AccountUpdate{Name: "alice", Description: "first", BlockID: requestBlock})
	second := signedUpdate(AccountUpdate{Name: "alice", Description: "second", BlockID: requestBlock})
	if block, _ := nextBlock(t, chain, state, BlockContent{AccountUpdates: []AccountUpdate{first, second}}); len(block.Content.AccountUpdates) != 1 {
		t.Errorf("carried %d updates of one name, want 1", len(block.Content.AccountUpdates))
	}
	refuses(t, chain, state, BlockContent{AccountUpdates: []AccountUpdate{first, second}}, "rule 21")

	action := votedAction(Reassignment{Name: "carol"}, "bank-a", "bank-b")
	for name, update := range map[string]AccountUpdate{
		"of the action's name": signedUpdate(AccountUpdate{Name: "carol", Description: "x", BlockID: requestBlock}),
		"to the action's name": signedUpdate(AccountUpdate{Name: "bob", Delegate: "carol", BlockID: requestBlock}),
	} {
		content := BlockContent{Reassignment: action, AccountUpdates: []AccountUpdate{update}}
		if block, _ := nextBlock(t, chain, state, content); len(block.Content.AccountUpdates) != 0 {
			t.Errorf("an update %s was carried", name)
		}
		t.Run(name, func(t *testing.T) { refuses(t, chain, state, content, "rule 21") })
	}
}
