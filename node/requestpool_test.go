package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// nameRequestOf is a request of kind about name for the block blockID, signed in each role it has: by
// the requestor, by the bank ("" for none), and by the test name keyHolder, whose key is the new key
// ("" for none). Its fee is the default new-key fee, or none for a freeze.
func nameRequestOf(kind, name, requestor, bank, keyHolder string, blockID int64) ledger.NameRequest {
	request := ledger.NameRequest{Kind: kind, Name: name, Requestor: requestor, Bank: bank, BlockID: blockID}
	if kind != ledger.FreezeKind {
		request.Fee = ledger.DefaultParameters().NewKeyFee
	}
	if keyHolder != "" {
		request.NewKey = testSigner(keyHolder).PublicKeyBase64()
	}
	var err error
	if request, err = ledger.SignAsRequestor(testSigner(requestor), request); err != nil {
		panic(err)
	}
	if bank != "" {
		if request, err = ledger.SignAsBank(testSigner(bank), request); err != nil {
			panic(err)
		}
	}
	if keyHolder != "" {
		if request, err = ledger.SignAsNewKey(testSigner(keyHolder), request); err != nil {
			panic(err)
		}
	}
	return request
}

// banked is the test account of name, with the balance and bank as its bank of record.
func banked(name, amount, bank string) ledger.Account {
	account := testAccount(name, amount)
	account.Bank = bank
	return account
}

// requestNode is a node that is not started, on a chain whose one seat is bank-a, at its genesis: it
// works on block 1. bank-a holds 100, alice and bob 10 each with bank-a as their bank of record, and
// carol 0.5. A change waits one block.
func requestNode(scheme signing.Scheme) *Node {
	genesis := testGenesis([]ledger.Account{testAccount("bank-a", "100"), banked("alice", "10", "bank-a"), banked("bob", "10", "bank-a"), testAccount("carol", "0.5")},
		time.Hour, time.Hour)
	genesis.Parameters.ChangeDelayBlocks = 1
	chain := staticChain("bank-a")
	chain.Scheme = scheme
	return New(testConfig(chain, "bank-a", genesis), &recordingTransport{})
}

// admitRequest runs the name-request intake on a node that is not started, as AdmitNameRequest does:
// off the loop, then on it.
func admitRequest(node *Node, request ledger.NameRequest) error {
	requestorKey, bankKey, err := node.checkNameRequest(request)
	if err != nil {
		return err
	}
	_, err = node.admitNameRequest(request, requestorKey, bankKey)
	return err
}

// admitPaymentNow runs the payment intake on a node that is not started, as AdmitPayment does.
func admitPaymentNow(node *Node, payment ledger.Payment) error {
	verifiedKey, err := node.checkPayment(payment)
	if err != nil {
		return err
	}
	_, err = node.admitPayment(payment, verifiedKey)
	return err
}

// A block prefers a payer's requests, so its payments for that block would lapse (spec 5.5). Intake
// refuses, with a reason, a payment whose payer pays the fee of a pooled request for its block, and a
// request whose fee payer has a payment pooled for its block, rather than give a receipt for a payment
// that would lapse. Other blocks are not affected, and a freeze, which pays no fee, is never held back.
func TestIntakeRefusesAPaymentAndARequestOfOnePayerForOneBlock(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3)); err != nil {
		t.Fatalf("premise: alice's registration is pooled: %v", err)
	}
	if err := admitPaymentNow(node, signedPayment("alice", "bob", "1", 3)); err == nil || !strings.Contains(err.Error(), "same block") {
		t.Errorf("alice's payment for the block of her pooled request: got %v, want a refusal", err)
	}
	if err := admitPaymentNow(node, signedPayment("alice", "bob", "1", 4)); err != nil {
		t.Errorf("alice's payment for another block was refused: %v", err)
	}

	if err := admitPaymentNow(node, signedPayment("bob", "alice", "1", 5)); err != nil {
		t.Fatalf("premise: bob's payment is pooled: %v", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "erin", "bob", "bank-a", "erin-key", 5)); err == nil || !strings.Contains(err.Error(), "same block") {
		t.Errorf("bob's request for the block of his pooled payment: got %v, want a refusal", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "erin", "bob", "bank-a", "erin-key", 6)); err != nil {
		t.Errorf("bob's request for another block was refused: %v", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.FreezeKind, "bob", "bob", "", "", 5)); err != nil {
		t.Errorf("bob's freeze for the block of his pooled payment was refused: %v", err)
	}
}

// The pool holds one request per name and block, as a block carries at most one per name (spec 4.3
// step 4). The same request again is accepted, as a resubmission; another request for the same name
// and block is refused; the same name for another block is taken. The pool's add enforces it itself,
// as the update pool's does.
func TestRequestPoolHoldsOneRequestPerNameAndBlock(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	byAlice := nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3)
	pool := newRequestPool(0)
	if !pool.add(byAlice) || pool.add(nameRequestOf(ledger.RegistrationKind, "dave", "bob", "bank-a", "dave-key", 3)) || len(pool.pending) != 1 {
		t.Error("the pool's add took a second request for the same name and block")
	}
	if err := admitRequest(node, byAlice); err != nil {
		t.Fatalf("premise: alice's registration of dave is pooled: %v", err)
	}
	if err := admitRequest(node, byAlice); err != nil {
		t.Errorf("alice's registration of dave again: got %v, want it accepted", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "dave", "bob", "bank-a", "dave-key", 3)); err == nil || !strings.Contains(err.Error(), "another request") {
		t.Errorf("bob's registration of dave for the same block: got %v, want a refusal", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "dave", "bob", "bank-a", "dave-key", 4)); err != nil {
		t.Errorf("bob's registration of dave for another block was refused: %v", err)
	}
	if pooled := len(node.requests.pendingCopy()); pooled != 2 {
		t.Errorf("the pool holds %d requests, want 2", pooled)
	}
}

// A payer's pending outgoing is its pooled payments plus its pooled fees, and intake holds it within
// the payer's balance whichever comes first. After a commit the pools keep, payments first and then
// fees, only what the new balance still covers.
func TestPoolCountsPendingFees(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	setBalance := func(name, amount string) {
		previousAccounts := node.state.Accounts
		node.state.Accounts = append([]ledger.Account(nil), previousAccounts...)
		index := 0
		for node.state.Accounts[index].Name != name {
			index++
		}
		node.state.Accounts[index].Amount = rational(amount)
		node.publishView()
		node.reconcilePools(previousAccounts)
	}
	setBalance("alice", "2.5")
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3)); err != nil {
		t.Fatalf("premise: alice's registration, a fee of 1, is pooled: %v", err)
	}
	if err := admitPaymentNow(node, signedPayment("alice", "bob", "2", 4)); err == nil || !strings.Contains(err.Error(), "overdraw") {
		t.Errorf("a payment of 2 on top of a pooled fee of 1, from 2.5: got %v, want a refusal", err)
	}
	if err := admitPaymentNow(node, signedPayment("alice", "bob", "1.5", 4)); err != nil {
		t.Fatalf("a payment of 1.5 on top of a pooled fee of 1, from 2.5, was refused: %v", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "erin", "alice", "bank-a", "erin-key", 5)); err == nil || !strings.Contains(err.Error(), "above its balance") {
		t.Errorf("a second fee on top of 2.5 pending, from 2.5: got %v, want a refusal", err)
	}

	setBalance("alice", "2") // the payment of 1.5 still fits; with the fee of 1 it does not
	if len(node.mempool.pendingCopy()) != 1 || len(node.requests.pendingCopy()) != 0 {
		t.Errorf("after alice's balance fell to 2, the pools hold %d payments and %d requests: want the payment kept and the fee dropped",
			len(node.mempool.pendingCopy()), len(node.requests.pendingCopy()))
	}
}

// Intake checks a name request against the committed state, and its payer's funds, before any of its
// signatures (spec 4.3 step 2): a request refused for a cheap reason costs no signature check, so a
// flood of them, from an unfunded requestor included, costs the node nothing but the cheap checks.
func TestRequestIntakeRunsCheapChecksBeforeSignatures(t *testing.T) {
	checks := &atomic.Int64{}
	node := requestNode(countingScheme{Scheme: signing.NetworkScheme(), checks: checks})
	wrongFee := nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3)
	wrongFee.Fee = rational("2")
	unknownKind := nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3)
	unknownKind.Kind = "rename"
	for name, request := range map[string]ledger.NameRequest{
		"an unknown kind":               unknownKind,
		"a name that is taken":          nameRequestOf(ledger.RegistrationKind, "bob", "alice", "bank-a", "dave-key", 3),
		"an unfunded requestor":         nameRequestOf(ledger.RegistrationKind, "dave", "carol", "bank-a", "dave-key", 3),
		"a bank that is not seated":     nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bob", "dave-key", 3),
		"another fee":                   wrongFee,
		"a block past the window":       nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 2+MaxRequestLookahead),
		"the last seated bank's freeze": nameRequestOf(ledger.FreezeKind, "bank-a", "bank-a", "", "", 3),
	} {
		checks.Store(0)
		if err := admitRequest(node, request); err == nil || checks.Load() != 0 {
			t.Errorf("%s: refused %v after %d signature checks, want a refusal before any", name, err, checks.Load())
		}
	}
	checks.Store(0)
	if err := admitRequest(node, nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3)); err != nil || checks.Load() != 3 {
		t.Errorf("a valid registration: %v after %d signature checks, want it pooled after three", err, checks.Load())
	}
}

// Intake takes a request that names a block from the height in progress to 3,600 blocks past it, an
// hour at one block per second (spec 4.3, Q24): three people may sign one request on three machines.
// A freeze, which pays no fee, gets the payments' 64 blocks, so one name cannot fill the pool with
// free freezes for many blocks. Through POST /hdls/name-request it answers 202, or 409 with the reason.
func TestRequestWindowIsMaxRequestLookahead(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	waitFor(t, 2*time.Second, func() bool { return node.Snapshot().Height == 2 }, "the sole validator did not commit block 1")

	post := func(request ledger.NameRequest) int {
		body, _ := json.Marshal(request)
		response, err := http.Post(server.URL+"/hdls/name-request", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	for blockID, want := range map[int64]int{
		1:        http.StatusConflict, // committed already
		2 + 3600: http.StatusAccepted, // the last block in the window, written out so the test pins 3,600
		3 + 3600: http.StatusConflict,
	} {
		if status := post(nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", blockID)); status != want {
			t.Errorf("a registration for block %d at height 2: status %d, want %d", blockID, status, want)
		}
	}
	for blockID, want := range map[int64]int{
		2 + 64: http.StatusAccepted, // the last block in a freeze's window, written out so the test pins 64
		3 + 64: http.StatusConflict,
	} {
		if status := post(nameRequestOf(ledger.FreezeKind, "bob", "bob", "", "", blockID)); status != want {
			t.Errorf("bob's freeze for block %d at height 2: status %d, want %d", blockID, status, want)
		}
	}
}

// A request reaches every node by gossip, and a registration takes effect in its block: four
// validators register dave at alice's request, then alice pays the new name, and dave pays on with the
// key he registered.
func TestRegistrationEndToEnd(t *testing.T) {
	names := validatorNames(4)
	accounts := []ledger.Account{banked("alice", "100", names[0]), testAccount("bob", "0")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, 2*time.Second, 100*time.Millisecond)
	bus := newBus()
	nodes := make([]*Node, len(names))
	for i, name := range names {
		nodes[i] = New(testConfig(staticChain(names...), name, genesis), bus)
		bus.register(name, nodes[i])
	}
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()
	everyNode := func(condition func(Snapshot) bool) func() bool {
		return func() bool {
			for _, node := range nodes {
				if !condition(node.Snapshot()) {
					return false
				}
			}
			return true
		}
	}

	registration := nameRequestOf(ledger.RegistrationKind, "dave", "alice", names[0], "dave-key", nodes[0].Snapshot().Height+3)
	if _, err := nodes[0].AdmitNameRequest(registration); err != nil {
		t.Fatalf("the registration was refused: %v", err)
	}
	waitFor(t, 10*time.Second, everyNode(func(snapshot Snapshot) bool {
		dave, found := ledger.FindAccount(snapshot.Accounts, "dave")
		return found && dave.PublicKey == testSigner("dave-key").PublicKeyBase64() && dave.Bank == names[0]
	}), "dave was not registered on every node")

	if _, err := nodes[1].AdmitPayment(signedPayment("alice", "dave", "10", nodes[1].Snapshot().Height+3)); err != nil {
		t.Fatalf("the payment to the new name was refused: %v", err)
	}
	waitFor(t, 10*time.Second, everyNode(func(snapshot Snapshot) bool { return balance(snapshot.Accounts, "dave").Cmp(rational("9.9")) == 0 }),
		"the payment to dave did not land on every node")

	fromDave, _ := ledger.SignPayment(testSigner("dave-key"), ledger.Payment{From: "dave", To: "bob", Amount: rational("1"), BlockID: nodes[2].Snapshot().Height + 3})
	if _, err := nodes[2].AdmitPayment(fromDave); err != nil {
		t.Fatalf("dave's payment under his registered key was refused: %v", err)
	}
	waitFor(t, 10*time.Second, everyNode(func(snapshot Snapshot) bool { return balance(snapshot.Accounts, "bob").Cmp(rational("0.9")) == 0 }),
		"dave's payment did not land on every node")
}

// No block leaves the validator set empty (R9), and intake refuses what alone would: the last seated
// bank's own freeze, and a ⅔ action that freezes it. Another name's freeze is taken.
func TestNoBlockLeavesTheSetEmpty(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	if err := admitRequest(node, nameRequestOf(ledger.FreezeKind, "bank-a", "bank-a", "", "", 1)); err == nil || !strings.Contains(err.Error(), "R9") {
		t.Errorf("the last seated bank's freeze: got %v, want a refusal under R9", err)
	}
	if err := admitRequest(node, nameRequestOf(ledger.FreezeKind, "alice", "alice", "", "", 1)); err != nil {
		t.Errorf("alice's freeze was refused: %v", err)
	}
	freezeBy := func(name string) ledger.Reassignment {
		action := ledger.Reassignment{Name: name, Freeze: true}
		vote, _ := ledger.SignReassignmentVote(testSigner("bank-a"), "bank-a", action)
		action.Votes = []ledger.ReassignmentVote{vote}
		return action
	}
	if newlyPooled(node.addReassignmentVotes(freezeBy("bank-a"))) {
		t.Error("the ⅔ freeze of the last seated bank was pooled")
	}
	if !newlyPooled(node.addReassignmentVotes(freezeBy("bob"))) {
		t.Error("the ⅔ freeze of bob was not pooled")
	}
}

// The loop confirms that the key a payment's, an update's or a name request's signature was checked
// under, off the loop, is still its signer's key (spec 4.1 step 5, 4.2 step 4, 4.3 step 4): a key
// change can land in between. For a name request that is the requestor's key and the bank's. After the
// commit that changes a key, the pools drop what was signed under the old one. /hdls/resolve shows the
// change while it waits.
func TestLoopRefusesWhatWasCheckedUnderAReplacedKey(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	resolve := func() ResolveRecord {
		response, err := http.Get(server.URL + "/hdls/resolve?name=alice")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var record ResolveRecord
		json.NewDecoder(response.Body).Decode(&record)
		return record
	}
	newKey := testSigner("alice-new").PublicKeyBase64()
	if err := admitRequest(node, nameRequestOf(ledger.KeyChangeKind, "alice", "alice", "bank-a", "alice-new", 1)); err != nil {
		t.Fatalf("premise: alice's key change is pooled: %v", err)
	}
	alicesBankForBob := ledger.Reassignment{Name: "bob", Bank: "alice"} // so alice co-signs bob's key change below
	vote, _ := ledger.SignReassignmentVote(testSigner("bank-a"), "bank-a", alicesBankForBob)
	alicesBankForBob.Votes = []ledger.ReassignmentVote{vote}
	if !newlyPooled(node.addReassignmentVotes(alicesBankForBob)) {
		t.Fatal("premise: the ⅔ action making alice bob's bank of record is pooled")
	}
	node.startHeight() // the sole validator commits block 1, which files the change; it falls due at the end of block 2
	if record := resolve(); record.PendingKey != newKey || record.PendingHeight != 2 || record.PendingBank != "" {
		t.Errorf("/hdls/resolve shows the pending change as key %.8s at height %d, want alice-new's due at 2", record.PendingKey, record.PendingHeight)
	}

	payment, update := signedPayment("alice", "bob", "1", 3), signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "x", BlockID: 3})
	paymentKey, paymentErr := node.checkPayment(payment)
	updateKey, updateErr := node.checkUpdate(update)
	if paymentErr != nil || updateErr != nil {
		t.Fatalf("premise: both pass the checks off the loop under alice's key: %v, %v", paymentErr, updateErr)
	}
	byAlice := nameRequestOf(ledger.RegistrationKind, "dave", "alice", "bank-a", "dave-key", 3) // alice is the requestor
	coSignedByAlice := nameRequestOf(ledger.KeyChangeKind, "bob", "bob", "alice", "bob-new", 3) // alice is the bank
	byAliceRequestorKey, byAliceBankKey, byAliceErr := node.checkNameRequest(byAlice)
	coSignedRequestorKey, coSignedBankKey, coSignedErr := node.checkNameRequest(coSignedByAlice)
	if byAliceErr != nil || coSignedErr != nil {
		t.Fatalf("premise: both name requests pass the checks off the loop under alice's key: %v, %v", byAliceErr, coSignedErr)
	}
	pooled := signedPayment("alice", "bob", "2", 3)
	if err := admitPaymentNow(node, pooled); err != nil {
		t.Fatalf("premise: a payment under alice's key is pooled: %v", err)
	}
	node.startHeight() // block 2: alice's key changes
	if resolve().PublicKey != newKey {
		t.Fatal("premise: alice's key changed at the end of block 2")
	}

	if _, err := node.admitPayment(payment, paymentKey); err == nil || !strings.Contains(err.Error(), "key changed") {
		t.Errorf("a payment checked under alice's replaced key: got %v, want a refusal on the loop", err)
	}
	if _, err := node.admitUpdate(update, updateKey); err == nil || !strings.Contains(err.Error(), "key changed") {
		t.Errorf("an update checked under alice's replaced key: got %v, want a refusal on the loop", err)
	}
	if _, err := node.admitNameRequest(byAlice, byAliceRequestorKey, byAliceBankKey); err == nil || !strings.Contains(err.Error(), "key changed") {
		t.Errorf("a name request whose requestor's key was replaced: got %v, want a refusal on the loop", err)
	}
	if _, err := node.admitNameRequest(coSignedByAlice, coSignedRequestorKey, coSignedBankKey); err == nil || !strings.Contains(err.Error(), "key changed") {
		t.Errorf("a name request whose bank's key was replaced: got %v, want a refusal on the loop", err)
	}
	if node.mempool.contains(pooled) {
		t.Error("the pool kept a payment signed under alice's replaced key")
	}
}

// Two name actions that differ only in a field Phase 2 added (the new key, the bank, the freeze mark)
// are two actions: the pool keys every field the votes sign, so they never share a slot, and the
// governance view shows those fields.
func TestNameActionPoolKeysEveryField(t *testing.T) {
	node := requestNode(signing.NetworkScheme())
	voted := func(action ledger.Reassignment) ledger.Reassignment {
		vote, _ := ledger.SignReassignmentVote(testSigner("bank-a"), "bank-a", action)
		action.Votes = []ledger.ReassignmentVote{vote}
		return action
	}
	rekey := ledger.Reassignment{Name: "alice", NewKey: testSigner("alice-new").PublicKeyBase64()}
	rekey, _ = ledger.SignReassignmentNewKey(testSigner("alice-new"), rekey)
	for _, action := range []ledger.Reassignment{{Name: "alice"}, {Name: "alice", Bank: "-"}, {Name: "alice", Freeze: true}, rekey} {
		if !newlyPooled(node.addReassignmentVotes(voted(action))) {
			t.Errorf("an action on alice with bank %q, freeze %v and a new key %v did not take a slot of its own", action.Bank, action.Freeze, action.NewKey != "")
		}
	}
	shown := map[string]bool{}
	for _, view := range node.governanceView().Reassignments {
		shown[view.Bank+"/"+strconv.FormatBool(view.Freeze)+"/"+strconv.FormatBool(view.NewKey != "")] = true
	}
	if len(shown) != 4 || !shown["-/false/false"] || !shown["/true/false"] || !shown["/false/true"] {
		t.Errorf("the governance view shows the actions as %v", shown)
	}
}

// A relayed copy of a pooled certificate, with the same votes, is dropped before any signature check:
// every validator relays it, so each copy would otherwise cost the whole certificate's checks. The
// certificate is larger than MaxMessageBytes, which does not apply to certificates: with a scheme of
// small keys and signatures, the test lowers the node's bound below it.
func TestRelayedCertificateCopyCostsNoSignatureCheck(t *testing.T) {
	checks := &atomic.Int64{}
	node := requestNode(countingScheme{Scheme: signing.NetworkScheme(), checks: checks})
	action := ledger.Reassignment{Name: "alice", NewKey: testSigner("alice-new").PublicKeyBase64()}
	action, _ = ledger.SignReassignmentNewKey(testSigner("alice-new"), action)
	vote, _ := ledger.SignReassignmentVote(testSigner("bank-a"), "bank-a", action)
	action.Votes = []ledger.ReassignmentVote{vote}
	encoded, _ := json.Marshal(action)
	node.state.Parameters.MaxMessageBytes = min(node.state.Parameters.MaxMessageBytes, int64(len(encoded))-1)
	if int64(len(encoded)) <= node.state.Parameters.MaxMessageBytes {
		t.Fatalf("premise: the certificate (%d bytes) is larger than MaxMessageBytes", len(encoded))
	}
	parameters := node.state.Parameters
	parameters.TransactionCost = rational("0.2")
	parameters.MaxMessageBytes = max(parameters.MaxMessageBytes, ledger.MinMessageBytes()) // one a vote may set
	change := ledger.ParameterChange{NewParameters: parameters, PreviousParametersDigest: node.state.Parameters.Digest()}
	parameterVote, _ := ledger.SignParameterVote(testSigner("bank-a"), "bank-a", parameters, change.PreviousParametersDigest, 0)
	change.Votes = []ledger.ParameterVote{parameterVote}

	if !newlyPooled(node.addReassignmentVotes(action)) || !newlyPooled(node.addParameterVotes(change)) {
		t.Fatal("premise: both certificates are pooled")
	}
	checks.Store(0)
	if newlyPooled(node.addReassignmentVotes(action)) || newlyPooled(node.addParameterVotes(change)) || checks.Load() != 0 {
		t.Errorf("relayed copies cost %d signature checks, want none", checks.Load())
	}
}
