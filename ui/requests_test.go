package main

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"html"
	"math/big"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The double signature setup (spec 7.3): the customer's wallet drafts a registration of its own key,
// paid for by its bank, and signs as the new key; the bank's desk signs as requestor and bank; the
// complete request, three signatures, is submitted once. Draft and Sign send nothing, and an incomplete
// request is not submitted.
func TestRegistrationCollectsThreeSignatures(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank, customer := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	bankName := nameOf(walletSigner(t, bank))
	testNode.setState([]signing.Signer{walletSigner(t, bank)}, accountOf(walletSigner(t, bank), "10"))

	draftPage := html.UnescapeString(submitForm(customer, "/requests", url.Values{"kind": {"registration"}, "name": {" Alice "}, "requestor": {bankName}}).Body.String())
	for _, want := range []string{"Draft: nothing is signed yet", "name: alice", "requestor, who pays the fee: " + bankName, "co-signing bank: " + bankName,
		"new key: " + fingerprint(customer.PublicKey()), "fee: 1", "block: 130", "signed by the requestor: false, the bank: false, the new key: false"} {
		if !strings.Contains(draftPage, want) {
			t.Errorf("the draft does not show %q", want)
		}
	}
	draft, err := customer.DraftNameRequest(verified(t, customer), requestForm{Kind: ledger.RegistrationKind, Name: "alice", Requestor: bankName})
	if err != nil {
		t.Fatal(err)
	}
	signedByCustomer, err := customer.SignNameRequest(verified(t, customer), draft.Text)
	if err != nil || !slices.Equal(signedByCustomer.Signed, []string{"new key"}) {
		t.Fatalf("the customer signed as %v (err %v), want as the new key", signedByCustomer.Signed, err)
	}
	if err := customer.SubmitNameRequest(signedByCustomer.Text); err == nil {
		t.Error("a request without the bank's signatures was submitted")
	}
	signedByBank, err := bank.SignNameRequest(verified(t, bank), signedByCustomer.Text)
	if err != nil || !slices.Equal(signedByBank.Signed, []string{"requestor", "bank"}) {
		t.Fatalf("the bank signed as %v (err %v), want as requestor and bank", signedByBank.Signed, err)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("drafting and signing sent %d request(s)", len(posts))
	}
	if refusal := redirectError(submitForm(customer, "/requests", url.Values{"step": {"submit"}, "request": {signedByBank.Text}})); refusal != "" {
		t.Fatal(refusal)
	}
	var registration ledger.NameRequest
	if posts := testNode.posted(); len(posts) != 1 || posts[0].path != "/hdls/name-request" || json.Unmarshal(posts[0].body, &registration) != nil {
		t.Fatalf("want one request sent to /hdls/name-request, got %d", len(posts))
	}
	checkpoint := verified(t, customer)
	if registration.Name != "alice" || registration.NewKey != customer.PublicKey() || ledger.NameRequestShapeError(registration) != nil ||
		ledger.NameRequestFitsState(checkpoint.State, consensus.NewValidatorSet(checkpoint.Validators), registration) != nil ||
		!ledger.VerifyNameRequest(signing.NetworkScheme(), testNetworkID, bank.PublicKey(), bank.PublicKey(), registration) {
		t.Errorf("the submitted registration %+v is not alice's for the customer's key, with three valid signatures", registration)
	}
}

// The Sign step signs every role the wallet holds, and only those (spec 7.3): requestor when its name is
// the Requestor, bank when its name is the Bank, new key when its key is the NewKey. Here a third party
// pays for the registration, and its bank of record co-signs; a wallet that holds no role signs nothing.
func TestSignStepSignsOnlyTheRolesThisWalletHolds(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank, payer, customer, stranger := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	bankName := nameOf(walletSigner(t, bank))
	testNode.setState([]signing.Signer{walletSigner(t, bank)},
		ledger.Account{Name: "payer", PublicKey: payer.PublicKey(), Amount: big.NewRat(10, 1), Bank: bankName},
		ledger.Account{Name: "stranger", PublicKey: stranger.PublicKey(), Amount: big.NewRat(10, 1), Bank: bankName})
	draft, err := customer.DraftNameRequest(verified(t, customer), requestForm{Kind: ledger.RegistrationKind, Name: "alice", Requestor: "payer"})
	if err != nil {
		t.Fatal(err)
	}
	signatures := func(text string) [3]bool {
		var request ledger.NameRequest
		_ = json.Unmarshal([]byte(text), &request)
		return [3]bool{request.RequestorSignature != "", request.BankSignature != "", request.NewKeySignature != ""}
	}
	text := draft.Text
	for _, signer := range []struct {
		label  string
		wallet *Wallet
		roles  []string
		after  [3]bool // requestor, bank, new key
	}{
		{"the customer", customer, []string{"new key"}, [3]bool{false, false, true}},
		{"the payer", payer, []string{"requestor"}, [3]bool{true, false, true}},
		{"the payer's bank", bank, []string{"bank"}, [3]bool{true, true, true}},
	} {
		signed, err := signer.wallet.SignNameRequest(verified(t, signer.wallet), text)
		if err != nil || !slices.Equal(signed.Signed, signer.roles) || signatures(signed.Text) != signer.after {
			t.Fatalf("%s signed as %v (err %v), with signatures %v; want as %v, with %v", signer.label, signed.Signed, err, signatures(signed.Text), signer.roles, signer.after)
		}
		text = signed.Text
	}
	if _, err := stranger.SignNameRequest(verified(t, stranger), draft.Text); err == nil || !strings.Contains(err.Error(), "no role") {
		t.Errorf("a wallet holding no role signed: %v", err)
	}
	// Only a registration's requestor may be another name: the payer does not sign a key change of the
	// stranger's name as its requestor.
	keyChange, _ := json.Marshal(ledger.NameRequest{Kind: ledger.KeyChangeKind, Name: "stranger", Requestor: "payer", NewKey: customer.PublicKey(), Bank: bankName,
		Fee: governanceParameters.NewKeyFee, BlockID: 130})
	if _, err := payer.SignNameRequest(verified(t, payer), string(keyChange)); err == nil {
		t.Error("the payer signed a key change of another name as its requestor")
	}
}

// The Sign step signs only a request whose block lies in the node's intake window (spec 4.3): from the
// height in progress, here 10, to node.MaxRequestLookahead past it. A signature for a block further
// ahead would outlive every request a node takes; no block of a refused request is kept.
func TestSignStepRefusesABlockOutsideTheIntakeWindow(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank, customer := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	bankName := nameOf(walletSigner(t, bank))
	testNode.setState([]signing.Signer{walletSigner(t, bank)}, accountOf(walletSigner(t, bank), "10"))
	registration := func(blockID int64) string {
		text, _ := json.Marshal(ledger.NameRequest{Kind: ledger.RegistrationKind, Name: "alice", Requestor: bankName, NewKey: customer.PublicKey(), Bank: bankName,
			Fee: governanceParameters.NewKeyFee, BlockID: blockID})
		return string(text)
	}
	for _, blockID := range []int64{9, 10 + node.MaxRequestLookahead + 1, 1_000_010} {
		if _, err := bank.SignNameRequest(verified(t, bank), registration(blockID)); err == nil {
			t.Errorf("the bank signed a request for block %d, outside the intake window 10 to %d", blockID, 10+node.MaxRequestLookahead)
		}
	}
	if len(bank.saved.RequestBlockIDs) != 0 {
		t.Errorf("the bank kept the blocks %v of requests it refused", bank.saved.RequestBlockIDs)
	}
	if _, err := bank.SignNameRequest(verified(t, bank), registration(10+node.MaxRequestLookahead)); err != nil {
		t.Errorf("the bank refused a request for block %d, the last of the intake window: %v", 10+node.MaxRequestLookahead, err)
	}
}

// A request takes no value of the payment counter (spec 7.3): drafting and signing one leave it alone.
// The wallet that signs as the fee payer keeps the request's block, across a restart, and its payments
// skip that block, where they would lapse. A freeze takes no counter value either.
func TestRequestsLeaveThePaymentCounterAlone(t *testing.T) {
	testNode := newGovernanceNode(t)
	keyfile := filepath.Join(t.TempDir(), "bank.json")
	bank, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	customer := newTestWallet(t, testNode.baseURL)
	bankName := nameOf(walletSigner(t, bank))
	testNode.setState([]signing.Signer{walletSigner(t, bank)}, accountOf(walletSigner(t, bank), "10"))
	draft, err := customer.DraftNameRequest(verified(t, customer), requestForm{Kind: ledger.RegistrationKind, Name: "alice", Requestor: bankName, Lead: "4"})
	if err != nil {
		t.Fatal(err)
	}
	signedByCustomer, err := customer.SignNameRequest(verified(t, customer), draft.Text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bank.SignNameRequest(verified(t, bank), signedByCustomer.Text); err != nil {
		t.Fatal(err)
	}
	restarted, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3) // before the freeze, which also writes the state file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bank.Freeze(); err != nil {
		t.Fatal(err)
	}
	for _, wallet := range []*Wallet{customer, bank} {
		if wallet.saved.LastPaymentBlockID != 0 {
			t.Errorf("a request or a freeze took the payment counter to block %d", wallet.saved.LastPaymentBlockID)
		}
	}
	if first, second := nextBlockID(t, restarted, 10), nextBlockID(t, restarted, 10); first != 13 || second != 15 {
		t.Errorf("the payments after a request for block 14 name blocks %d and %d, want 13 and 15", first, second)
	}
	if blockID := nextBlockID(t, customer, 10); blockID != 13 {
		t.Errorf("the customer's first payment names block %d, want 13: signing as the new key keeps no block", blockID)
	}
}

// Skipping the blocks of the requests a wallet pays for never takes a payment past height + lookahead +
// blockSlack, inside the node's intake window: a desk that paid for a run of requests waits for the
// chain instead, and the counter does not latch. At height 10 the window ends at block 45.
func TestRequestBlocksKeepPaymentsInsideTheWindow(t *testing.T) {
	wallet, err := LoadOrCreateWallet(filepath.Join(t.TempDir(), "bank.json"), "testnet", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	for blockID := int64(13); blockID <= 50; blockID++ {
		wallet.keepRequestBlock(blockID)
	}
	if blockID, err := wallet.nextPaymentBlockID(10); err == nil || !strings.Contains(err.Error(), "wait for the chain to advance") {
		t.Errorf("with requests in blocks 13 to 50 a payment names block %d (err %v), want a refusal to wait", blockID, err)
	}
	if blockID := nextBlockID(t, wallet, 20); blockID != 51 {
		t.Errorf("once the chain advanced to height 20, whose window ends at block 55, the payment names block %d, want 51", blockID)
	}
}

// The hash name of a key (spec 7.7) is "k-" and the first 15 bytes of the SHA-256 of the raw key, in
// lowercase base32 without padding: 26 characters, a name. The Requests page offers it, and a
// registration drafted without a name registers it.
func TestHashNameConvention(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if name := hashName(base64.StdEncoding.EncodeToString(key)); name != "k-mmg42klgyqzwneiskrelxms3" {
		t.Errorf("the hash name of the key 0..31 is %q, want k-mmg42klgyqzwneiskrelxms3", name)
	}

	testNode := newGovernanceNode(t)
	bank, customer := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, bank)}, accountOf(walletSigner(t, bank), "10"))
	rawKey, _ := base64.StdEncoding.DecodeString(customer.PublicKey())
	keyHash := sha256.Sum256(rawKey)
	want := "k-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(keyHash[:15]))
	if name := hashName(customer.PublicKey()); name != want || len(name) != 26 || !ledger.CanonicalName(name) {
		t.Errorf("the hash name is %q, want %q, a name of 26 characters", name, want)
	}
	if !strings.Contains(page(t, customer, "/requests"), "your hash name, for a wallet that wants no name of its own: <strong>"+want+"</strong>") {
		t.Error("the Requests page does not offer the hash name")
	}
	draft, err := customer.DraftNameRequest(verified(t, customer), requestForm{Kind: ledger.RegistrationKind, Requestor: nameOf(walletSigner(t, bank))})
	if err != nil || !strings.Contains(draft.Text, `"Name":"`+want+`"`) {
		t.Errorf("a registration drafted without a name does not register the hash name (err %v)", err)
	}
}

// The bank's desk lists the pending changes on its customers, so the bank can tell them (spec 7.4): a
// key change or a bank change away on a name whose bank of record it is, and a bank change to it.
func TestDeskListsThePendingChangesOnItsCustomers(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank := newTestWallet(t, testNode.baseURL)
	bankName := nameOf(walletSigner(t, bank))
	newKey := testNodeSigner(t).PublicKeyBase64()
	account := func(name, bankOfRecord, pendingKey, pendingBank string, pendingHeight int64) ledger.Account {
		return ledger.Account{Name: name, PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat), Bank: bankOfRecord,
			PendingKey: pendingKey, PendingBank: pendingBank, PendingHeight: pendingHeight}
	}
	testNode.setState([]signing.Signer{walletSigner(t, bank)},
		account("carol", bankName, newKey, "", 50), account("dave", "other", "", bankName, 60), account("erin", bankName, "", "other", 70),
		account("frank", bankName, "", "", 0), account("gina", "other", newKey, "", 80), account("other", "", "", "", 0))
	body := page(t, bank, "/requests")
	for _, want := range []string{"Pending changes on your customers", "<td>carol</td><td>key change (to key " + fingerprint(newKey) + "…)</td><td>50</td>",
		"<td>dave</td><td>bank change (to " + bankName + ")</td><td>60</td>", "<td>erin</td><td>bank change (to other)</td><td>70</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the desk does not show %q", want)
		}
	}
	for _, other := range []string{"<td>frank</td>", "<td>gina</td>"} {
		if strings.Contains(body, other) {
			t.Errorf("the desk lists %s, who has no pending change of this bank's", other)
		}
	}
	if !strings.Contains(body, "co-sign only after checking the holder in person or by a second") {
		t.Error("the desk does not remind the operator to check the holder")
	}
}
