package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestVerifyPaymentRoundTrip(t *testing.T) {
	payment := signedPayment("alice", "bob", "3", 2)
	if !VerifyPayment(signing.NetworkScheme(), testNetwork, testSigner("alice").PublicKeyBase64(), payment) {
		t.Fatal("a validly-signed payment did not verify")
	}
}

func TestVerifyPaymentTamperedAmountRejected(t *testing.T) {
	payment := signedPayment("alice", "bob", "3", 2)
	payment.Amount = rational("300") // tamper after signing
	if VerifyPayment(signing.NetworkScheme(), testNetwork, testSigner("alice").PublicKeyBase64(), payment) {
		t.Fatal("a tampered amount verified")
	}
}

func TestVerifyPaymentWrongNetworkRejected(t *testing.T) {
	payment := signedPayment("alice", "bob", "3", 2)
	if VerifyPayment(signing.NetworkScheme(), "mainnet", testSigner("alice").PublicKeyBase64(), payment) {
		t.Fatal("a testnet payment verified on mainnet")
	}
}

// The payer and the payee are part of the signed content: retargeting a signed payment to another name,
// or claiming it came from another name, breaks the signature.
func TestPaymentSignatureCoversBothNames(t *testing.T) {
	payment := signedPayment("alice", "bob", "1", 5)
	retargeted := payment
	retargeted.To = "mallory"
	relabelled := payment
	relabelled.From = "carol"
	for _, changed := range []Payment{retargeted, relabelled} {
		if VerifyPayment(signing.NetworkScheme(), testNetwork, testSigner("alice").PublicKeyBase64(), changed) {
			t.Fatalf("a payment changed to %s -> %s still verified", changed.From, changed.To)
		}
	}
}

// A payment names its payer and carries no key: every node checks it under the key the pre-block state
// holds for that name. A payment signed under any other key — even one that is some account's key — is
// refused, so you cannot forge a payment "from" a name whose key you do not hold. And when the state
// gives the name a new key, only the new key signs for it.
func TestPaymentIsSignedByThePayersCurrentKey(t *testing.T) {
	accounts := []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), namedAccount("mallory", "1"), seatedAccount()}
	previousState := buildLedgerState(3, accounts, rational("0.1"))
	honest := signedPayment("alice", "bob", "10", 4)
	forged, _ := SignPayment(testSigner("mallory"), Payment{From: "alice", To: "bob", Amount: rational("10"), BlockID: 4})
	if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{forged}}); len(block.Content.Payments) != 0 {
		t.Fatal("ProposeBlock carried a payment from alice signed under mallory's key")
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, forged)); err == nil {
		t.Fatal("ApplyBlock accepted a payment from alice signed under mallory's key")
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, honest)); err != nil {
		t.Fatalf("alice's own payment was refused: %v", err)
	}

	// Give alice mallory's key in the state, as a re-key would: now the key that signed `forged` is
	// alice's current key, and her old one no longer signs for her.
	rekeyed := buildLedgerState(3, []Account{{Name: "alice", PublicKey: testSigner("mallory").PublicKeyBase64(), Amount: rational("100")}, accounts[1], seatedAccount()}, rational("0.1"))
	if _, err := ApplyBlock(testChain(), rekeyed, zeroHash, forgeBlock(rekeyed, 4, honest)); err == nil {
		t.Fatal("a payment signed under alice's former key applied after the state gave her another")
	}
	if _, err := ApplyBlock(testChain(), rekeyed, zeroHash, forgeBlock(rekeyed, 4, forged)); err != nil {
		t.Fatalf("a payment signed under alice's current key was refused: %v", err)
	}
}

// A payment to a name no account holds would move nothing (ProcessPayments drops it before the fee),
// so it never enters a block: it would be a permanent, zero-fee no-op occupying block space and a
// signature check on every node. ProposeBlock leaves it out, and ApplyBlock refuses a block a dishonest
// proposer padded with one. A payment never creates an account.
func TestPaymentToAnUnknownNameIsRefused(t *testing.T) {
	previousState := buildLedgerState(3, []Account{namedAccount("alice", "10"), seatedAccount()}, rational("0.1"))
	spam := signedPayment("alice", "nobody", "3", 4)
	if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{spam}}); len(block.Content.Payments) != 0 {
		t.Fatalf("ProposeBlock carried a payment to a name no account holds")
	}
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, spam)); err == nil {
		t.Fatal("ApplyBlock accepted a block padded with a payment to a name no account holds")
	}
}

// A frozen name neither pays nor is paid (spec 4.7): no block carries a payment from it or to it.
// Nothing freezes a name yet; the field is in the state, and the rule holds for any state carrying it.
func TestPaymentFromOrToAFrozenNameIsRefused(t *testing.T) {
	frozen := namedAccount("frozen", "100")
	frozen.Frozen = true
	previousState := buildLedgerState(3, []Account{frozen, namedAccount("alice", "100"), seatedAccount()}, rational("0.1"))
	for name, payment := range map[string]Payment{
		"from a frozen name": signedPayment("frozen", "alice", "10", 4),
		"to a frozen name":   signedPayment("alice", "frozen", "10", 4),
	} {
		if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{payment}}); len(block.Content.Payments) != 0 {
			t.Errorf("%s: ProposeBlock carried it", name)
		}
		if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, payment)); err == nil {
			t.Errorf("%s: ApplyBlock accepted a block carrying it", name)
		}
	}
	unfrozen := buildLedgerState(3, []Account{namedAccount("frozen", "100"), namedAccount("alice", "100"), seatedAccount()}, rational("0.1"))
	if _, err := ApplyBlock(testChain(), unfrozen, zeroHash, forgeBlock(unfrozen, 4, signedPayment("frozen", "alice", "10", 4))); err != nil {
		t.Fatalf("the same payment, once the name is not frozen, was refused: %v", err)
	}
}
