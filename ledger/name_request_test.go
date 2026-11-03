package ledger

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// requestBlock is the block that follows requestFixture's state.
const requestBlock = 5

// requestFixture is a state at height 4 on a chain that seats bank-a and bank-b (power 1 each, so the
// ⅔ need both): the two banks with 100 each, alice and bob with 10 each and bank-a as their bank of
// record, and carol with 10 and no bank. A key or bank change waits two blocks.
func requestFixture() (Chain, LedgerState) {
	alice, bob := namedAccount("alice", "10"), namedAccount("bob", "10")
	alice.Bank, bob.Bank = "bank-a", "bank-a"
	state := buildLedgerState(requestBlock-1, []Account{namedAccount("bank-a", "100"), namedAccount("bank-b", "100"), alice, bob, namedAccount("carol", "10")}, rational("0.1"))
	state.Parameters.ChangeDelayBlocks = 2
	return testChain("bank-a", "bank-b"), state
}

// signedAs is request signed by the test name signerName in one role (SignAsRequestor, SignAsBank or
// SignAsNewKey).
func signedAs(sign func(signing.Signer, NameRequest) (NameRequest, error), signerName string, request NameRequest) NameRequest {
	signed, err := sign(testSigner(signerName), request)
	if err != nil {
		panic(err)
	}
	return signed
}

// requestOf is a request of kind about name for the block blockID, signed in each role it has: by the
// requestor, by the bank ("" for none), and by the test name keyHolder, whose key is the new key (""
// for none). Its fee is the default new-key fee, or none for a freeze.
func requestOf(kind, name, requestor, bank, keyHolder string, blockID int64) NameRequest {
	request := NameRequest{Kind: kind, Name: name, Requestor: requestor, Bank: bank, BlockID: blockID}
	if kind != FreezeKind {
		request.Fee = DefaultParameters().NewKeyFee
	}
	if keyHolder != "" {
		request.NewKey = testSigner(keyHolder).PublicKeyBase64()
	}
	request = signedAs(SignAsRequestor, requestor, request)
	if bank != "" {
		request = signedAs(SignAsBank, bank, request)
	}
	if keyHolder != "" {
		request = signedAs(SignAsNewKey, keyHolder, request)
	}
	return request
}

func registrationOf(name, requestor, bank, keyHolder string) NameRequest {
	return requestOf(RegistrationKind, name, requestor, bank, keyHolder, requestBlock)
}

func keyChangeOf(name, bank, keyHolder string, blockID int64) NameRequest {
	return requestOf(KeyChangeKind, name, name, bank, keyHolder, blockID)
}

func bankChangeOf(name, newBank string, blockID int64) NameRequest {
	return requestOf(BankChangeKind, name, name, newBank, "", blockID)
}

func freezeOf(name string, blockID int64) NameRequest {
	return requestOf(FreezeKind, name, name, "", "", blockID)
}

// requests is a block content of name requests.
func requests(nameRequests ...NameRequest) BlockContent {
	return BlockContent{NameRequests: nameRequests}
}

// nextBlock proposes the block on state from content, applies it and returns both, failing the test
// unless the proposed block applies.
func nextBlock(t *testing.T, chain Chain, state LedgerState, content BlockContent) (Block, LedgerState) {
	t.Helper()
	block := ProposeBlock(chain, state, zeroHash, zeroHash, content)
	next, err := ApplyBlock(chain, state, zeroHash, block)
	if err != nil {
		t.Fatalf("the proposed block does not apply: %v", err)
	}
	return block, next
}

// forgedBlock is the block on state that carries content just as it is, with the digest of the state
// that content results in and the hash of the set it seats: a block only a rule about its content can
// refuse.
func forgedBlock(chain Chain, state LedgerState, content BlockContent) Block {
	validators := chain.ValidatorRule.SetFor(state)
	next := applyContent(state, validators, content)
	return Block{Height: state.Height + 1, ValidatorsHash: ValidatorsHash(validators.Validators()), NextValidatorsHash: signingSetHash(chain, next),
		Content: content, StateDigest: next.Digest()}
}

// refuses checks that ApplyBlock refuses the forged block carrying content on state, with an error
// that mentions want.
func refuses(t *testing.T, chain Chain, state LedgerState, content BlockContent, want string) {
	t.Helper()
	if _, err := ApplyBlock(chain, state, zeroHash, forgedBlock(chain, state, content)); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ApplyBlock: got %v, want a refusal mentioning %q", err, want)
	}
}

// accountOf is the account named name in state; it fails the test when there is none.
func accountOf(t *testing.T, state LedgerState, name string) Account {
	t.Helper()
	account, found := FindAccount(state.Accounts, name)
	if !found {
		t.Fatalf("no account is named %q", name)
	}
	return account
}

// A registration carries three signatures: the requestor's, the bank's and the new key's proof of
// possession. A block carries it only when all three verify, each under its own key; one made by
// any other key, or missing, keeps it out.
func TestRegistrationNeedsAllThreeSignatures(t *testing.T) {
	chain, state := requestFixture()
	genuine := registrationOf("dave", "alice", "bank-a", "dave-key")
	if block, _ := nextBlock(t, chain, state, requests(genuine)); len(block.Content.NameRequests) != 1 {
		t.Fatal("premise: the registration with its three signatures is carried")
	}
	for role, forged := range map[string]NameRequest{
		"the requestor's": signedAs(SignAsRequestor, "mallory", genuine),
		"the bank's":      signedAs(SignAsBank, "mallory", genuine),
		"the new key's":   signedAs(SignAsNewKey, "mallory", genuine),
	} {
		if block, _ := nextBlock(t, chain, state, requests(forged)); len(block.Content.NameRequests) != 0 {
			t.Errorf("a registration with %s signature made by another key was carried", role)
		}
		refuses(t, chain, state, requests(forged), "signature")
	}
	missing := genuine
	missing.BankSignature = ""
	refuses(t, chain, state, requests(missing), "do not fit")
}

// The requestor signs in the domain named by the request's kind, over the request's fields in the
// order of spec 4.3 (the kind, the name, the requestor, the new key, the bank, the fee, "" for none,
// and the block); the bank signs them in "name-request-bank" and the new key in
// "name-request-new-key". So a signer that sees only the domain knows what it signs, and a requestor's
// signature for one kind does not verify for another.
func TestRequestorSignsInTheDomainOfItsKind(t *testing.T) {
	scheme := signing.NetworkScheme()
	verifies := func(signature, signerName string, domain signing.Domain, request NameRequest) bool {
		feeText := FormatAmount(request.Fee)
		return signing.VerifyBase64(scheme, testNetwork, testSigner(signerName).PublicKeyBase64(), domain, signature,
			[]byte(request.Kind), []byte(request.Name), []byte(request.Requestor), []byte(request.NewKey), []byte(request.Bank), []byte(feeText), []byte("5"))
	}
	for kind, request := range map[string]NameRequest{
		RegistrationKind: registrationOf("dave", "alice", "bank-a", "dave-key"),
		KeyChangeKind:    keyChangeOf("alice", "bank-a", "alice-new", requestBlock),
		BankChangeKind:   bankChangeOf("alice", "bank-b", requestBlock),
		FreezeKind:       freezeOf("alice", requestBlock),
	} {
		if !verifies(request.RequestorSignature, "alice", signing.Domain(kind), request) {
			t.Errorf("the requestor of a %s does not sign in the %q domain over the spec 4.3 fields", kind, kind)
		}
		for otherKind := range requestorDomains {
			if otherKind != kind && verifies(request.RequestorSignature, "alice", signing.Domain(otherKind), request) {
				t.Errorf("the requestor's signature on a %s verifies in the %q domain", kind, otherKind)
			}
		}
		if request.Bank != "" && !verifies(request.BankSignature, request.Bank, signing.DomainNameRequestBank, request) {
			t.Errorf("the bank of a %s does not sign in the name-request-bank domain", kind)
		}
	}
	registration := registrationOf("dave", "alice", "bank-a", "dave-key")
	if !verifies(registration.NewKeySignature, "dave-key", signing.DomainNameRequestNewKey, registration) {
		t.Error("the new key does not sign in the name-request-new-key domain")
	}
	asKeyChange := registration
	asKeyChange.RequestorSignature, _ = testSigner("alice").SignBase64(signing.DomainKeyChange, registration.signFields()...)
	if VerifyNameRequest(scheme, testNetwork, testSigner("alice").PublicKeyBase64(), testSigner("bank-a").PublicKeyBase64(), asKeyChange) {
		t.Error("a registration whose requestor signed in the key-change domain verified")
	}
}

// The shape check runs first and refuses a request whose fields do not fit its kind: an unknown kind,
// a name that is not canonical, another requestor outside a registration, a field the kind leaves
// empty that is set or the reverse, a new key of the wrong length or in another spelling (one as long
// as the key used to pass), a signature of the wrong length, a block id of 0.
func TestNameRequestShapeIsCheckedFirst(t *testing.T) {
	registration := registrationOf("dave", "alice", "bank-a", "dave-key")
	freeze := freezeOf("alice", requestBlock)
	if NameRequestShapeError(registration) != nil || NameRequestShapeError(freeze) != nil {
		t.Fatal("premise: a well-formed registration and freeze pass the shape check")
	}
	for name, change := range map[string]func(*NameRequest){
		"an unknown kind":               func(request *NameRequest) { request.Kind = "rename" },
		"a name in capitals":            func(request *NameRequest) { request.Name = "Dave" },
		"a bank that is not a name":     func(request *NameRequest) { request.Bank = "-" },
		"a new key of the wrong length": func(request *NameRequest) { request.NewKey = request.NewKey[:len(request.NewKey)-4] },
		"a new key in another spelling": func(request *NameRequest) { request.NewKey = otherSpellingOf(t, request.NewKey) },
		"a short bank signature":        func(request *NameRequest) { request.BankSignature = request.BankSignature[4:] },
		"no new key proof":              func(request *NameRequest) { request.NewKeySignature = "" },
		"block 0":                       func(request *NameRequest) { request.BlockID = 0 },
		"a key change for another name": func(request *NameRequest) { request.Kind = KeyChangeKind },
		"no requestor signature":        func(request *NameRequest) { request.RequestorSignature = "" },
		"a registration without a bank": func(request *NameRequest) { request.Bank, request.BankSignature = "", "" },
		"a bank change with a new key": func(request *NameRequest) {
			request.Kind, request.Requestor, request.Name = BankChangeKind, "alice", "alice"
		},
		"a registration with a null fee": func(request *NameRequest) { request.Fee = nil },
	} {
		request := registration
		change(&request)
		if NameRequestShapeError(request) == nil {
			t.Errorf("%s passed the shape check", name)
		}
	}
	for name, change := range map[string]func(*NameRequest){
		"a freeze with a fee":  func(request *NameRequest) { request.Fee = rational("1") },
		"a freeze with a bank": func(request *NameRequest) { request.Bank = "bank-a" },
	} {
		request := freeze
		change(&request)
		if NameRequestShapeError(request) == nil {
			t.Errorf("%s passed the shape check", name)
		}
	}
}

// A registration, key change or bank change with a null fee is refused against the state, as a fee
// that is not the new-key fee, and does not panic: the wallet's Sign step checks a request against its
// state before the shape check would refuse it.
func TestNameRequestWithANullFeeIsRefusedAgainstTheState(t *testing.T) {
	chain, state := requestFixture()
	validators := chain.ValidatorRule.SetFor(state)
	for _, request := range []NameRequest{registrationOf("dave", "alice", "bank-a", "dave-key"),
		keyChangeOf("alice", "bank-a", "alice-new-key", requestBlock), bankChangeOf("alice", "bank-b", requestBlock)} {
		request.Fee = nil
		if err := NameRequestFitsState(state, validators, request); err == nil || !strings.Contains(err.Error(), "new-key fee") {
			t.Errorf("a %s with a null fee: %v; want it refused as not the new-key fee", request.Kind, err)
		}
	}
}

// A name is registered once: a registration of a name some account has, from the genesis or from an
// earlier registration, is never carried.
func TestRegisteredNameCannotBeRegisteredAgain(t *testing.T) {
	chain, state := requestFixture()
	_, registered := nextBlock(t, chain, state, requests(registrationOf("dave", "alice", "bank-a", "dave-key")))
	again := requestOf(RegistrationKind, "dave", "bob", "bank-a", "other-key", requestBlock+1)
	if block, _ := nextBlock(t, chain, registered, requests(again)); len(block.Content.NameRequests) != 0 {
		t.Error("a second registration of a registered name was carried")
	}
	refuses(t, chain, registered, requests(again), "taken")
	refuses(t, chain, state, requests(registrationOf("carol", "alice", "bank-a", "other-key")), "taken")
}

// A registration takes effect in its block: the new account holds the new key, a zero balance, no
// delegate and the co-signing bank as its bank of record, and the requestor pays the new-key fee,
// which is burned. The bank must be the requestor's bank of record, seated and not frozen, and the fee
// must be the network's.
func TestRegistrationChargesTheRequestorAndSetsTheBankOfRecord(t *testing.T) {
	chain, state := requestFixture()
	_, next := nextBlock(t, chain, state, requests(registrationOf("dave", "alice", "bank-a", "dave-key")))
	dave := accountOf(t, next, "dave")
	if dave.PublicKey != testSigner("dave-key").PublicKeyBase64() || dave.Amount.Sign() != 0 || dave.Bank != "bank-a" || dave.Delegate != "" {
		t.Errorf("the registered account is %+v, want the new key, a zero balance and bank-a as its bank", dave)
	}
	if paid := accountOf(t, next, "alice").Amount; paid.Cmp(rational("9")) != 0 {
		t.Errorf("the requestor holds %s after the registration, want 10 less the fee of 1", FormatAmount(paid))
	}

	wrongFee := registrationOf("dave", "alice", "bank-a", "dave-key")
	wrongFee.Fee = rational("0.5")
	wrongFee = signedAs(SignAsNewKey, "dave-key", signedAs(SignAsBank, "bank-a", signedAs(SignAsRequestor, "alice", wrongFee)))
	frozenBank := state
	frozenBank.Accounts = freezeNames(state.Accounts, map[string]bool{"bank-a": true})
	for _, testCase := range []struct {
		name    string
		state   LedgerState
		request NameRequest
		want    string
	}{
		{"another bank than the requestor's", state, registrationOf("dave", "alice", "bank-b", "dave-key"), "bank of record"},
		{"a bank that is not seated", state, registrationOf("dave", "carol", "carol", "dave-key"), "seated"},
		{"a frozen bank", frozenBank, registrationOf("dave", "alice", "bank-a", "dave-key"), "seated bank that is not frozen"},
		{"another fee", state, wrongFee, "fee"},
		{"a new key that is a name's key", state, registrationOf("dave", "alice", "bank-a", "carol"), "already a name's key"},
	} {
		if block, _ := nextBlock(t, chain, testCase.state, requests(testCase.request)); len(block.Content.NameRequests) != 0 {
			t.Errorf("%s: the registration was carried", testCase.name)
		}
		refuses(t, chain, testCase.state, requests(testCase.request), testCase.want)
	}
}

// A seated requestor may co-sign as its own bank (R3): a genesis bank has no bank of record, yet it can
// pay for a customer's registration. The new name's bank of record is that bank.
func TestSeatedRequestorMayBeItsOwnBank(t *testing.T) {
	chain, state := requestFixture()
	_, next := nextBlock(t, chain, state, requests(registrationOf("erin", "bank-a", "bank-a", "erin-key")))
	if erin := accountOf(t, next, "erin"); erin.Bank != "bank-a" || accountOf(t, next, "bank-a").Amount.Cmp(rational("99")) != 0 {
		t.Errorf("a seated bank's own registration of erin gave %+v, and left the bank with %s", erin, FormatAmount(accountOf(t, next, "bank-a").Amount))
	}
}

// A key change waits ChangeDelayBlocks: filed in block h, it takes effect at the end of block h + the
// delay. Until then the old key signs, through that last block; after it, only the new key does.
func TestKeyChangeWaitsChangeDelayBlocks(t *testing.T) {
	chain, state := requestFixture() // the delay is 2
	oldKey, newKey := testSigner("alice").PublicKeyBase64(), testSigner("alice-new").PublicKeyBase64()
	_, filed := nextBlock(t, chain, state, requests(keyChangeOf("alice", "bank-a", "alice-new", requestBlock)))
	if alice := accountOf(t, filed, "alice"); alice.PublicKey != oldKey || alice.PendingKey != newKey || alice.PendingHeight != requestBlock+2 ||
		alice.Amount.Cmp(rational("9")) != 0 {
		t.Fatalf("after filing, alice is %+v, want the old key, the new one pending until block 7, and the fee paid", alice)
	}
	state = filed
	for _, height := range []int64{6, 7} {
		block, next := nextBlock(t, chain, state, BlockContent{Payments: []Payment{signedPayment("alice", "bob", "1", height)}})
		if len(block.Content.Payments) != 1 {
			t.Fatalf("the old key's payment in block %d was not carried", height)
		}
		state = next
	}
	if alice := accountOf(t, state, "alice"); alice.PublicKey != newKey || alice.PendingKey != "" || alice.PendingHeight != 0 {
		t.Fatalf("after block 7, alice is %+v, want the new key and nothing pending", alice)
	}
	newKeyPayment, _ := SignPayment(testSigner("alice-new"), Payment{From: "alice", To: "bob", Amount: rational("1"), BlockID: 8})
	block, _ := nextBlock(t, chain, state, BlockContent{Payments: []Payment{signedPayment("alice", "bob", "1", 8), newKeyPayment}})
	if len(block.Content.Payments) != 1 || block.Content.Payments[0].Signature != newKeyPayment.Signature {
		t.Error("after the change, the block must carry the new key's payment and not the old key's")
	}
}

// A key change is co-signed by the name's bank of record, which needs no seat but must not be frozen.
// Another bank's co-signature does not do, and a name with no bank of record changes its key only by
// the ⅔.
func TestKeyChangeNeedsTheBankOfRecord(t *testing.T) {
	chain, state := requestFixture()
	frozenBank := state
	frozenBank.Accounts = freezeNames(state.Accounts, map[string]bool{"bank-a": true})
	for _, testCase := range []struct {
		name    string
		state   LedgerState
		request NameRequest
	}{
		{"co-signed by another bank", state, keyChangeOf("alice", "bank-b", "alice-new", requestBlock)},
		{"of a name with no bank of record", state, keyChangeOf("carol", "bank-a", "carol-new", requestBlock)},
		{"co-signed by a frozen bank of record", frozenBank, keyChangeOf("alice", "bank-a", "alice-new", requestBlock)},
	} {
		if block, _ := nextBlock(t, chain, testCase.state, requests(testCase.request)); len(block.Content.NameRequests) != 0 {
			t.Errorf("a key change %s was carried", testCase.name)
		}
		refuses(t, chain, testCase.state, requests(testCase.request), "bank")
	}

	// The bank of record needs no seat: bank-c is not seated, and still co-signs for dave.
	dave := namedAccount("dave", "10")
	dave.Bank = "bank-c"
	withUnseatedBank := buildLedgerState(requestBlock-1, append(append([]Account(nil), state.Accounts...), namedAccount("bank-c", "0"), dave), rational("0.1"))
	if block, _ := nextBlock(t, chain, withUnseatedBank, requests(keyChangeOf("dave", "bank-c", "dave-new", requestBlock))); len(block.Content.NameRequests) != 1 {
		t.Error("a key change co-signed by a bank of record that is not seated was not carried")
	}
}

// A bank change needs only the holder and the new bank, which must be another name, seated and not
// frozen; the old bank is not asked. It waits ChangeDelayBlocks like a key change.
func TestBankChangeNeedsOnlyTheNewSeatedBank(t *testing.T) {
	chain, state := requestFixture()
	_, filed := nextBlock(t, chain, state, requests(bankChangeOf("alice", "bank-b", requestBlock)))
	if alice := accountOf(t, filed, "alice"); alice.Bank != "bank-a" || alice.PendingBank != "bank-b" || alice.PendingHeight != 7 {
		t.Fatalf("after filing, alice is %+v, want bank-a still, and bank-b pending until block 7", alice)
	}
	_, waited := nextBlock(t, chain, filed, BlockContent{})
	if _, moved := nextBlock(t, chain, waited, BlockContent{}); accountOf(t, moved, "alice").Bank != "bank-b" {
		t.Error("the bank change did not take effect at the end of block 7")
	}
	for name, request := range map[string]NameRequest{
		"to a bank that is not seated": bankChangeOf("alice", "carol", requestBlock),
		"to the name itself":           bankChangeOf("bob", "bob", requestBlock),
	} {
		if block, _ := nextBlock(t, chain, state, requests(request)); len(block.Content.NameRequests) != 0 {
			t.Errorf("a bank change %s was carried", name)
		}
		refuses(t, chain, state, requests(request), "new bank")
	}
}

// A seated name changes its key or bank only by the ⅔ (one key per bank): it cannot file either
// change. Nor can a name that has a change waiting file another.
func TestSeatedNameCannotFileAChange(t *testing.T) {
	chain, state := requestFixture()
	state.Accounts = append([]Account(nil), state.Accounts...)
	bankAIndex, _ := findAccountIndex(state.Accounts, "bank-a")
	state.Accounts[bankAIndex].Bank = "bank-b" // so only the seat stands in the way of its key change
	waiting := state
	waiting.Accounts = applyNameRequests(state.Accounts, []NameRequest{keyChangeOf("alice", "bank-a", "alice-new", requestBlock)}, 9)
	for _, testCase := range []struct {
		name    string
		state   LedgerState
		request NameRequest
		want    string
	}{
		{"a seated name's key change", state, keyChangeOf("bank-a", "bank-b", "bank-a-new", requestBlock), "seated"},
		{"a seated name's bank change", state, bankChangeOf("bank-a", "bank-b", requestBlock), "seated"},
		{"a second change while one waits", waiting, bankChangeOf("alice", "bank-b", requestBlock), "waiting"},
	} {
		if block, _ := nextBlock(t, chain, testCase.state, requests(testCase.request)); len(block.Content.NameRequests) != 0 {
			t.Errorf("%s was carried", testCase.name)
		}
		refuses(t, chain, testCase.state, requests(testCase.request), testCase.want)
	}
}

// A pending change lapses at maturity if its name is seated in the set of the block it falls due in:
// a seated name's key and bank change only by the ⅔. Here alice files a key change, and stake
// delegated to her in the meantime seats her before it falls due. The fee stays burned.
func TestPendingChangeLapsesWhenItsNameIsSeatedAtMaturity(t *testing.T) {
	chain := Chain{Scheme: signing.NetworkScheme(), NetworkID: testNetwork, ValidatorRule: ValidatorRule{Threshold: rational("50")}}
	bankA, bankB, alice := namedAccount("bank-a", "100"), namedAccount("bank-b", "100"), namedAccount("alice", "10")
	bankA.Delegate, bankB.Delegate, alice.Bank = "bank-a", "bank-b", "bank-a"
	state := buildLedgerState(requestBlock-1, []Account{bankA, bankB, alice, namedAccount("bob", "10")}, rational("0.1"))
	state.Parameters.ChangeDelayBlocks = 2

	_, filed := nextBlock(t, chain, state, requests(keyChangeOf("alice", "bank-a", "alice-new", requestBlock)))
	_, delegated := nextBlock(t, chain, filed, BlockContent{AccountUpdates: []AccountUpdate{signedUpdate(AccountUpdate{Name: "bank-b", Delegate: "alice", BlockID: 6})}})
	if !chain.ValidatorRule.SetFor(delegated).Contains("alice") {
		t.Fatal("premise: bank-b's stake seats alice before her change falls due")
	}
	_, matured := nextBlock(t, chain, delegated, BlockContent{})
	if alice := accountOf(t, matured, "alice"); alice.PublicKey != testSigner("alice").PublicKeyBase64() || alice.PendingHeight != 0 || alice.Amount.Cmp(rational("9")) != 0 {
		t.Errorf("alice is %+v: want her old key, nothing pending, and the fee still burned", alice)
	}
}

// A pending key change lapses if, by the time it falls due, its new key has become some name's key: a
// key in state is the key of exactly one name.
func TestPendingKeyChangeLapsesWhenTheKeyIsTakenMeanwhile(t *testing.T) {
	chain, state := requestFixture()
	_, filed := nextBlock(t, chain, state, requests(keyChangeOf("alice", "bank-a", "shared-key", requestBlock)))
	taken := requestOf(RegistrationKind, "dave", "bob", "bank-a", "shared-key", 6)
	_, registered := nextBlock(t, chain, filed, requests(taken))
	_, matured := nextBlock(t, chain, registered, BlockContent{})
	if alice := accountOf(t, matured, "alice"); alice.PublicKey != testSigner("alice").PublicKeyBase64() || alice.PendingHeight != 0 {
		t.Errorf("alice is %+v: her change to a key dave took meanwhile must lapse", alice)
	}
	if accountOf(t, matured, "dave").PublicKey != testSigner("shared-key").PublicKeyBase64() {
		t.Error("dave lost the key he registered")
	}
}

// Two changes to one key that fall due in one block: they are judged in name order, each against the
// state the one before left, so the first applies and the second lapses.
func TestTwoDueChangesToOneKeyApplyOnlyTheFirst(t *testing.T) {
	chain, state := requestFixture()
	sharedKey := testSigner("shared-key").PublicKeyBase64()
	state.Accounts = append([]Account(nil), state.Accounts...)
	for _, name := range []string{"alice", "bob"} {
		index, _ := findAccountIndex(state.Accounts, name)
		state.Accounts[index].PendingKey, state.Accounts[index].PendingHeight = sharedKey, requestBlock
	}
	_, next := nextBlock(t, chain, state, BlockContent{})
	alice, bob := accountOf(t, next, "alice"), accountOf(t, next, "bob")
	if alice.PublicKey != sharedKey || bob.PublicKey != testSigner("bob").PublicKeyBase64() || alice.PendingHeight != 0 || bob.PendingHeight != 0 {
		t.Errorf("alice has key %.8s and bob %.8s: want the shared key for alice, first in name order, and bob's own for bob", alice.PublicKey, bob.PublicKey)
	}
}

// A holder's freeze is free and takes effect in its block: the name is frozen, its balance stays, and
// its pending change is gone. A freeze that carries a fee is refused by its shape.
func TestFreezeIsFreeImmediateAndCancelsThePendingChange(t *testing.T) {
	chain, state := requestFixture()
	_, filed := nextBlock(t, chain, state, requests(keyChangeOf("alice", "bank-a", "thief-key", requestBlock)))
	_, frozen := nextBlock(t, chain, filed, requests(freezeOf("alice", 6)))
	if alice := accountOf(t, frozen, "alice"); !alice.Frozen || alice.PendingKey != "" || alice.PendingHeight != 0 || alice.Amount.Cmp(rational("9")) != 0 {
		t.Errorf("after her freeze alice is %+v: want frozen, nothing pending, and her balance as it was", alice)
	}
	withFee := freezeOf("alice", 6)
	withFee.Fee = rational("1")
	refuses(t, chain, filed, requests(withFee), "do not fit")
}

// A freeze also clears every pending change the frozen name co-signed (R11): the key change of a name
// whose bank of record it is, and a bank change to it. So a thief's key change co-signed with a
// leaked bank key never lands. A change another bank co-signed stays.
func TestFreezingABankClearsTheChangesItCoSigned(t *testing.T) {
	chain, state := requestFixture()
	carol := namedAccount("carol", "10")
	carol.Bank = "bank-b"
	state = buildLedgerState(requestBlock-1, append(append([]Account(nil), state.Accounts[:4]...), carol), rational("0.1")) // alice, bank-a, bank-b, bob
	state.Parameters.ChangeDelayBlocks = 2
	_, filed := nextBlock(t, chain, state, requests(
		keyChangeOf("alice", "bank-a", "alice-new", requestBlock), // co-signed by alice's bank, bank-a
		bankChangeOf("bob", "bank-b", requestBlock),               // to bank-b: bank-b co-signs
		keyChangeOf("carol", "bank-b", "carol-new", requestBlock), // co-signed by carol's bank, bank-b
	))
	_, frozen := nextBlock(t, chain, filed, requests(freezeOf("bank-b", 6)))
	if accountOf(t, frozen, "bob").PendingHeight != 0 || accountOf(t, frozen, "carol").PendingHeight != 0 {
		t.Error("bank-b's freeze left a change it co-signed pending")
	}
	if accountOf(t, frozen, "alice").PendingHeight == 0 {
		t.Error("bank-b's freeze cleared a change bank-a co-signed")
	}
}

// Once a freeze has landed, the frozen name neither pays nor is paid, sends no update or request, and
// takes no delegation.
func TestFrozenNameNeitherPaysNorIsPaidAfterItsFreezeLands(t *testing.T) {
	chain, state := requestFixture()
	_, frozen := nextBlock(t, chain, state, requests(freezeOf("alice", requestBlock)))
	for name, content := range map[string]BlockContent{
		"a payment from it":        {Payments: []Payment{signedPayment("alice", "bob", "1", 6)}},
		"a payment to it":          {Payments: []Payment{signedPayment("bob", "alice", "1", 6)}},
		"its update":               {AccountUpdates: []AccountUpdate{signedUpdate(AccountUpdate{Name: "alice", Description: "x", BlockID: 6})}},
		"a delegation to it":       {AccountUpdates: []AccountUpdate{signedUpdate(AccountUpdate{Name: "bob", Delegate: "alice", BlockID: 6})}},
		"its registration of dave": requests(requestOf(RegistrationKind, "dave", "alice", "bank-a", "dave-key", 6)),
	} {
		block, _ := nextBlock(t, chain, frozen, content)
		if len(block.Content.Payments)+len(block.Content.AccountUpdates)+len(block.Content.NameRequests) != 0 {
			t.Errorf("%s was carried after the freeze", name)
		}
		refuses(t, chain, frozen, content, "frozen")
	}
}

// Every freeze wins its block (R6): a name the block freezes sends, receives and requests nothing else
// in it, and nothing names it as delegate or bank. The proposer leaves those items out, and a block
// that carries one is refused. So a victim's freeze beats a thief's payment in the same block.
func TestFreezeWinsOverASameBlockPayment(t *testing.T) {
	chain, state := requestFixture()
	freeze := freezeOf("alice", requestBlock)
	theft := signedPayment("alice", "carol", "5", requestBlock)
	block, _ := nextBlock(t, chain, state, BlockContent{NameRequests: []NameRequest{freeze}, Payments: []Payment{theft, signedPayment("bob", "carol", "1", requestBlock)}})
	if len(block.Content.NameRequests) != 1 || len(block.Content.Payments) != 1 || block.Content.Payments[0].From != "bob" {
		t.Fatalf("want the freeze and bob's payment carried, and not the frozen name's: %d requests, %d payments", len(block.Content.NameRequests), len(block.Content.Payments))
	}
	for name, content := range map[string]BlockContent{
		"a payment from it":  {Payments: []Payment{theft}},
		"a payment to it":    {Payments: []Payment{signedPayment("bob", "alice", "1", requestBlock)}},
		"its update":         {AccountUpdates: []AccountUpdate{signedUpdate(AccountUpdate{Name: "alice", Description: "x", BlockID: requestBlock})}},
		"a delegation to it": {AccountUpdates: []AccountUpdate{signedUpdate(AccountUpdate{Name: "bob", Delegate: "alice", BlockID: requestBlock})}},
		"its request":        requests(registrationOf("dave", "alice", "bank-a", "dave-key")),
	} {
		content.NameRequests = append([]NameRequest{freeze}, content.NameRequests...)
		t.Run(name, func(t *testing.T) { refuses(t, chain, state, content, "rule") })
	}
}

// A block carries at most one request about a name (rule 13), and binds each new key once among its
// registrations, key changes and ⅔ action (rule 14). The proposer keeps the first and leaves the
// rest; a block that carries two is refused.
func TestOneRequestPerNameAndPerNewKeyPerBlock(t *testing.T) {
	chain, state := requestFixture()
	for name, pair := range map[string][]NameRequest{
		"two registrations of one name":              {registrationOf("dave", "alice", "bank-a", "dave-key"), registrationOf("dave", "bob", "bank-a", "other-key")},
		"two registrations of one key":               {registrationOf("dave", "alice", "bank-a", "dave-key"), registrationOf("erin", "bob", "bank-a", "dave-key")},
		"a key change and a registration of its key": {keyChangeOf("alice", "bank-a", "shared-key", requestBlock), registrationOf("dave", "bob", "bank-a", "shared-key")},
		"two freezes of one name":                    {freezeOf("alice", requestBlock), freezeOf("alice", requestBlock)},
		"a freeze and a key change of one name":      {freezeOf("alice", requestBlock), keyChangeOf("alice", "bank-a", "alice-new", requestBlock)},
	} {
		if block, _ := nextBlock(t, chain, state, requests(pair...)); len(block.Content.NameRequests) != 1 {
			t.Errorf("%s: carried %d requests, want the first only", name, len(block.Content.NameRequests))
		}
		refuses(t, chain, state, requests(pair...), "rule")
	}
}

// The ⅔ action's new key counts as used in its block (rule 14), so the proposer leaves out a request
// that binds the same key, rather than build a block that would be refused.
func TestProposeBlockSkipsARequestSharingTheActionsNewKey(t *testing.T) {
	chain, state := requestFixture()
	action := votedAction(rekeyedTo("shared-key", Reassignment{Name: "carol"}), "bank-a", "bank-b")
	registration := registrationOf("dave", "alice", "bank-a", "shared-key")
	block, _ := nextBlock(t, chain, state, BlockContent{Reassignment: action, NameRequests: []NameRequest{registration}})
	if block.Content.Reassignment == nil || len(block.Content.NameRequests) != 0 {
		t.Fatalf("want the action carried and the registration of its key left out, got %d requests", len(block.Content.NameRequests))
	}
	refuses(t, chain, state, BlockContent{Reassignment: action, NameRequests: []NameRequest{registration}}, "rule 14")
}

// A block prefers a payer's requests (spec 5.5): a requestor that pays a fee in a block sends no
// payment in it. The proposer leaves its payments out, and a block that carries both is refused. It
// may still be paid.
func TestFeePayerSendsNoPaymentInTheSameBlock(t *testing.T) {
	chain, state := requestFixture()
	registration := registrationOf("dave", "alice", "bank-a", "dave-key")
	fromIt, toIt := signedPayment("alice", "carol", "1", requestBlock), signedPayment("bob", "alice", "1", requestBlock)
	block, _ := nextBlock(t, chain, state, BlockContent{NameRequests: []NameRequest{registration}, Payments: []Payment{fromIt, toIt}})
	if len(block.Content.NameRequests) != 1 || len(block.Content.Payments) != 1 || block.Content.Payments[0].From != "bob" {
		t.Fatalf("want the registration and the payment to its requestor, got %d requests and %d payments", len(block.Content.NameRequests), len(block.Content.Payments))
	}
	refuses(t, chain, state, BlockContent{NameRequests: []NameRequest{registration}, Payments: []Payment{fromIt}}, "rules 16, 19")
}

// A requestor's fees in one block add up to at most its pre-block balance (rule 16): each request fits
// alone, both together do not.
func TestFeesAddUpToAtMostTheBalance(t *testing.T) {
	chain, state := requestFixture()
	state.Accounts = append([]Account(nil), state.Accounts...)
	aliceIndex, _ := findAccountIndex(state.Accounts, "alice")
	state.Accounts[aliceIndex].Amount = rational("1.5")
	pair := []NameRequest{registrationOf("dave", "alice", "bank-a", "dave-key"), registrationOf("erin", "alice", "bank-a", "erin-key")}
	if block, _ := nextBlock(t, chain, state, requests(pair...)); len(block.Content.NameRequests) != 1 {
		t.Errorf("carried %d of two registrations whose fees together pass the requestor's balance, want 1", len(block.Content.NameRequests))
	}
	refuses(t, chain, state, requests(pair...), "rules 16, 19")
}

// verificationCounter is the network's scheme, counting its signature checks.
type verificationCounter struct { // implements signing.Scheme: the network scheme's methods, with Verify replaced
	signing.Scheme
	checks *atomic.Int64
}

func (scheme verificationCounter) Verify(publicKey, message, signature []byte) bool {
	scheme.checks.Add(1)
	return scheme.Scheme.Verify(publicKey, message, signature)
}

// ProposeBlock checks an entry's signatures only when the entry is about to go into the block, after
// its cheap rules. Every node builds a candidate at every height, so a height costs the signature
// checks of what one block carries, however full the pools are for later blocks.
func TestProposeBlockVerifiesOnlyWhatItCarries(t *testing.T) {
	chain, state := requestFixture()
	checks := &atomic.Int64{}
	chain.Scheme = verificationCounter{Scheme: signing.NetworkScheme(), checks: checks}
	var content BlockContent
	for later := int64(requestBlock + 1); later < requestBlock+40; later++ { // a full pool for later blocks
		content.Payments = append(content.Payments, signedPayment("alice", "bob", "1", later))
		content.AccountUpdates = append(content.AccountUpdates, signedUpdate(AccountUpdate{Name: "carol", Description: "later", BlockID: later}))
		content.NameRequests = append(content.NameRequests, requestOf(RegistrationKind, "dave", "alice", "bank-a", "dave-key", later))
	}
	content.Payments = append(content.Payments, signedPayment("bob", "carol", "1", requestBlock))
	content.AccountUpdates = append(content.AccountUpdates, signedUpdate(AccountUpdate{Name: "carol", Description: "now", BlockID: requestBlock}))
	content.NameRequests = append(content.NameRequests, registrationOf("dave", "alice", "bank-a", "dave-key"), freezeOf("bank-b", requestBlock))

	checks.Store(0)
	block := ProposeBlock(chain, state, zeroHash, zeroHash, content)
	if carried := len(block.Content.Payments) + len(block.Content.AccountUpdates) + len(block.Content.NameRequests); carried != 4 {
		t.Fatalf("carried %d entries, want this block's payment, update, registration and freeze", carried)
	}
	if got := checks.Load(); got != 6 {
		t.Errorf("ProposeBlock made %d signature checks, want 6: one each for the payment, the update and the freeze, three for the registration", got)
	}
}

// A name request travels with its fee as a plain decimal, and a freeze's absent fee as null, which
// reads back as no fee.
func TestNameRequestFeeTravelsAsPlainDecimal(t *testing.T) {
	registration := registrationOf("dave", "alice", "bank-a", "dave-key")
	registration.Fee = rational("1.5")
	for _, testCase := range []struct {
		request NameRequest
		want    string
	}{{registration, `"Fee":"1.5"`}, {freezeOf("alice", requestBlock), `"Fee":null`}} {
		encoded, _ := json.Marshal(testCase.request)
		var decoded NameRequest
		if !strings.Contains(string(encoded), testCase.want) || json.Unmarshal(encoded, &decoded) != nil || FormatAmount(decoded.Fee) != FormatAmount(testCase.request.Fee) {
			t.Errorf("%s: want %s, and the same fee back", encoded[:40], testCase.want)
		}
	}
	if json.Unmarshal([]byte(`{"Kind":"registration","Fee":"1e3"}`), new(NameRequest)) == nil {
		t.Error("a fee in exponent form decoded")
	}
}

// No block leaves the validator set empty (rule 24, R9): then no block could ever commit again. The
// proposer takes out the item that would empty it, the last added first, never the whole block, and a
// block that empties it is refused. So the last seated bank cannot freeze itself, by its own request or
// by the ⅔, and in a dynamic chain a payment cannot move away the stake that seats the last name.
func TestNoBlockLeavesTheSetEmpty(t *testing.T) {
	_, state := requestFixture()
	chain := testChain("bank-a") // bank-a is the one seated name
	parameters := state.Parameters
	parameters.TransactionCost = rational("0.2")
	parameterChange := &ParameterChange{NewParameters: parameters, PreviousParametersDigest: state.Parameters.Digest()}
	vote, _ := SignParameterVote(testSigner("bank-a"), "bank-a", parameters, state.Parameters.Digest(), 0)
	parameterChange.Votes = []ParameterVote{vote}
	selfFreeze := freezeOf("bank-a", requestBlock)
	block, _ := nextBlock(t, chain, state, BlockContent{ParameterChange: parameterChange, NameRequests: []NameRequest{selfFreeze}})
	if len(block.Content.NameRequests) != 0 || block.Content.ParameterChange == nil {
		t.Error("want the last bank's freeze taken out, and the parameter change, added before it, kept")
	}
	refuses(t, chain, state, requests(selfFreeze), "rule 24")
	actionFreeze := votedAction(Reassignment{Name: "bank-a", Freeze: true}, "bank-a")
	if block, _ := nextBlock(t, chain, state, BlockContent{Reassignment: actionFreeze}); block.Content.Reassignment != nil {
		t.Error("the ⅔ freeze of the last seated bank was carried")
	}
	refuses(t, chain, state, BlockContent{Reassignment: actionFreeze}, "rule 24")

	dynamic := Chain{Scheme: signing.NetworkScheme(), NetworkID: testNetwork, ValidatorRule: ValidatorRule{Threshold: rational("50")}}
	founder := namedAccount("founder", "60")
	founder.Delegate = "founder"
	dynamicState := buildLedgerState(requestBlock-1, []Account{founder, namedAccount("alice", "10"), namedAccount("bob", "0")}, rational("0.1"))
	innocent, emptying := signedPayment("alice", "bob", "1", requestBlock), signedPayment("founder", "bob", "20", requestBlock)
	if block, _ := nextBlock(t, dynamic, dynamicState, BlockContent{Payments: []Payment{innocent, emptying}}); len(block.Content.Payments) != 1 || block.Content.Payments[0].From != "alice" {
		t.Errorf("want only the payment that would unseat the founder taken out, got %d payments", len(block.Content.Payments))
	}
	refuses(t, dynamic, dynamicState, BlockContent{Payments: []Payment{emptying}}, "rule 24")
}
