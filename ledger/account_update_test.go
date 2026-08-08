package ledger

import (
	"math/big"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func find(accounts []Account, publicKey string) Account {
	for _, account := range accounts {
		if account.PublicKey == publicKey {
			return account
		}
	}
	return Account{}
}

// The keystone: a signed account update registers a unique name (a "domain"), re-delegates stake,
// and sets a description — and a name already owned by another account can't be stolen (first-come
// uniqueness = the registry), while an unsigned update is rejected outright.
func TestAccountUpdateRegistersNameAndReDelegates(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	aliceSigner, _ := signing.Generate(scheme, networkID)
	bobSigner, _ := signing.Generate(scheme, networkID)
	aliceKey, bobKey := aliceSigner.PublicKeyBase64(), bobSigner.PublicKeyBase64()
	startingAccounts := []Account{
		{PublicKey: aliceKey, Amount: big.NewRat(100, 1), ValidatorNode: aliceKey},
		{PublicKey: bobKey, Amount: big.NewRat(100, 1), ValidatorNode: bobKey},
	}
	transactionCost := big.NewRat(1, 10)

	aliceUpdate, _ := SignAccountUpdate(aliceSigner, AccountUpdate{From: aliceKey, Name: "alice", Delegated: bobKey, Description: "hi", BlockID: 5})
	blockWithUpdate := ProposeBlock(scheme, networkID, 5, st(startingAccounts, transactionCost), BlockContent{Updates: []AccountUpdate{aliceUpdate}})
	accountsAfterUpdate, applied := ApplyBlock(scheme, networkID, st(startingAccounts, transactionCost), blockWithUpdate)
	if !applied {
		t.Fatal("a block carrying a valid account update should apply")
	}
	aliceAccount := find(accountsAfterUpdate.Accounts, aliceKey)
	if aliceAccount.Name != "alice" || aliceAccount.ValidatorNode != bobKey || aliceAccount.Description != "hi" {
		t.Fatalf("update not applied: name=%q delegated=%q description=%q", aliceAccount.Name, aliceAccount.ValidatorNode, aliceAccount.Description)
	}

	// Bob cannot steal the name Alice already owns.
	bobNameGrab, _ := SignAccountUpdate(bobSigner, AccountUpdate{From: bobKey, Name: "alice", BlockID: 6})
	nextBlock := ProposeBlock(scheme, networkID, 6, accountsAfterUpdate, BlockContent{Updates: []AccountUpdate{bobNameGrab}})
	accountsAfterGrab, applied := ApplyBlock(scheme, networkID, accountsAfterUpdate, nextBlock)
	if !applied {
		t.Fatal("the block should still apply (the rename is simply ignored)")
	}
	if find(accountsAfterGrab.Accounts, bobKey).Name == "alice" {
		t.Fatal("Bob must not be able to claim a name already owned by Alice")
	}
	if find(accountsAfterGrab.Accounts, aliceKey).Name != "alice" {
		t.Fatal("Alice should keep her registered name")
	}

	// An unsigned/forged update makes the whole block invalid.
	forgedUpdate := aliceUpdate
	forgedUpdate.Signature = "garbage"
	tamperedBlock := blockWithUpdate
	tamperedBlock.Updates = []AccountUpdate{forgedUpdate}
	if _, applied := ApplyBlock(scheme, networkID, st(startingAccounts, transactionCost), tamperedBlock); applied {
		t.Fatal("ApplyBlock must reject a block carrying an unsigned account update")
	}
}

// A name longer than MaxAccountNameLength is refused — critically, that keeps every registrable
// name strictly shorter than a base64 public key (44 chars), so no name can ever mask a real
// account key and hijack a recipient input; a payment target is unambiguously a key or a name.
func TestAccountNameLengthCapBlocksKeyMasking(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	aliceSigner, _ := signing.Generate(scheme, networkID)
	victimSigner, _ := signing.Generate(scheme, networkID)
	aliceKey, victimKey := aliceSigner.PublicKeyBase64(), victimSigner.PublicKeyBase64()
	if len(victimKey) <= MaxAccountNameLength {
		t.Fatalf("premise broken: a base64 key (%d chars) must exceed the name cap (%d)", len(victimKey), MaxAccountNameLength)
	}
	startingAccounts := []Account{
		{PublicKey: aliceKey, Amount: big.NewRat(100, 1), ValidatorNode: aliceKey},
	}

	// The attack: register the VICTIM'S PUBLIC KEY as alice's "name", then anyone paying that raw
	// key through a name-first resolver would be redirected to alice. Must be ignored.
	masking, err := SignAccountUpdate(aliceSigner, AccountUpdate{From: aliceKey, Name: victimKey, BlockID: 4})
	if err != nil {
		t.Fatal(err)
	}
	after := applyAccountUpdates(scheme, networkID, startingAccounts, []AccountUpdate{masking}, 4)
	if find(after, aliceKey).Name != "" {
		t.Fatal("a name the length of a public key must be refused")
	}

	// A name at the cap still registers, and the rest of an update with an over-long name applies.
	okName, _ := SignAccountUpdate(aliceSigner, AccountUpdate{From: aliceKey, Name: strings.Repeat("a", MaxAccountNameLength), BlockID: 4})
	tooLong, _ := SignAccountUpdate(aliceSigner, AccountUpdate{From: aliceKey,
		Name: strings.Repeat("b", MaxAccountNameLength+1), Description: "still applies", BlockID: 5})
	after = applyAccountUpdates(scheme, networkID, startingAccounts, []AccountUpdate{okName}, 4)
	if find(after, aliceKey).Name != strings.Repeat("a", MaxAccountNameLength) {
		t.Fatal("a name at the cap should register")
	}
	after = applyAccountUpdates(scheme, networkID, after, []AccountUpdate{tooLong}, 5)
	if got := find(after, aliceKey); got.Name != strings.Repeat("a", MaxAccountNameLength) || got.Description != "still applies" {
		t.Fatalf("over-long name ignored but the rest applied: name=%q description=%q", got.Name, got.Description)
	}
}
