package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// governanceNode is a fake node holding a committed state. It serves that state as a node does (a
// checkpoint its validators signed, the governance view) and records every POST instead of acting on it.
type governanceNode struct {
	baseURL         string
	mutex           sync.Mutex
	networkID       string // the network id it serves, testNetworkID unless a test changes it
	height          int64  // the height of the state it serves, 9 unless a test changes it; its height in progress is one more
	reportedHeight  int64  // when a test sets it, the height in progress its /hdls/status reports instead: a lie
	checkpointFails int    // when a test sets it, the error status /hdls/checkpoint answers with, and no checkpoint
	accounts        []ledger.Account
	validators      []signing.Signer
	pending         []node.ReassignmentView
	posts           []recordedPost
	checkpointReads int // how many times a wallet read its checkpoint

	// forge, when a test sets it, changes the checkpoint the node serves after it is signed.
	forge func(checkpoint *node.Checkpoint)
}

type recordedPost struct {
	path string
	body []byte
}

// governanceParameters are the parameters in force on a governanceNode, at governance sequence
// governanceSequence.
var governanceParameters = ledger.DefaultParameters()

const governanceSequence = 4

func newGovernanceNode(t *testing.T) *governanceNode {
	t.Helper()
	testNode := &governanceNode{networkID: testNetworkID, height: 9, validators: []signing.Signer{testNodeSigner(t)}}
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hdls/checkpoint", func(responseWriter http.ResponseWriter, request *http.Request) {
		testNode.mutex.Lock()
		defer testNode.mutex.Unlock()
		testNode.checkpointReads++
		if testNode.checkpointFails != 0 {
			responseWriter.WriteHeader(testNode.checkpointFails)
			return
		}
		checkpoint := signedCheckpointAt(testNode.height, testNode.validators, testNode.accounts)
		if testNode.forge != nil {
			testNode.forge(&checkpoint)
		}
		_ = json.NewEncoder(responseWriter).Encode(checkpoint)
	})
	serveMux.HandleFunc("/hdls/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		testNode.mutex.Lock()
		defer testNode.mutex.Unlock()
		heightInProgress := testNode.height + 1
		if testNode.reportedHeight != 0 {
			heightInProgress = testNode.reportedHeight
		}
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": heightInProgress, "networkId": testNode.networkID})
	})
	serveMux.HandleFunc("/hdls/governance", func(responseWriter http.ResponseWriter, request *http.Request) {
		testNode.mutex.Lock()
		defer testNode.mutex.Unlock()
		validators := consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators)
		view := node.GovernanceView{Parameters: node.ParametersView{TransactionCost: ledger.FormatAmount(governanceParameters.TransactionCost), StepTimeoutMs: governanceParameters.StepTimeoutMs},
			GovernanceSequence: governanceSequence, TotalPower: validators.TotalPower(), Quorum: validators.Quorum(), Reassignments: testNode.pending}
		for _, validator := range validators.Validators() {
			view.Validators = append(view.Validators, validator.Name)
		}
		_ = json.NewEncoder(responseWriter).Encode(view)
	})
	serveMux.HandleFunc("/", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		testNode.mutex.Lock()
		testNode.posts = append(testNode.posts, recordedPost{request.URL.Path, body})
		testNode.mutex.Unlock()
		responseWriter.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(serveMux)
	t.Cleanup(server.Close)
	testNode.baseURL = server.URL
	return testNode
}

// setState gives the node its validators (power 1 each; nil keeps the one it started with) and accounts,
// with an account at zero for each validator they leave out.
func (testNode *governanceNode) setState(validators []signing.Signer, accounts ...ledger.Account) {
	testNode.mutex.Lock()
	defer testNode.mutex.Unlock()
	if validators != nil {
		testNode.validators = validators
	}
	testNode.accounts = withValidatorAccounts(testNode.validators, accounts)
}

// nameOf is a test signer's name, derived from its key: "v-" and the first 12 hex digits of the key's
// SHA-256.
func nameOf(signer signing.Signer) string {
	keyHash := sha256.Sum256(signer.PublicKey())
	return "v-" + hex.EncodeToString(keyHash[:6])
}

// accountOf is the account of a test signer, named by nameOf, with the balance.
func accountOf(signer signing.Signer, amount string) ledger.Account {
	parsed, _ := ledger.ParseAmount(amount)
	return ledger.Account{Name: nameOf(signer), PublicKey: signer.PublicKeyBase64(), Amount: parsed}
}

// withValidatorAccounts is the accounts, plus an account at zero for each validator they leave out (a
// validator's seat takes its key from its account), in name order as the ledger keeps them.
func withValidatorAccounts(validators []signing.Signer, accounts []ledger.Account) []ledger.Account {
	all := append([]ledger.Account(nil), accounts...)
	for _, validator := range validators {
		if !slices.ContainsFunc(all, func(account ledger.Account) bool { return account.Name == nameOf(validator) }) {
			all = append(all, accountOf(validator, "0"))
		}
	}
	slices.SortFunc(all, func(first, second ledger.Account) int { return strings.Compare(first.Name, second.Name) })
	return all
}

// signedCheckpoint is the checkpoint a network of these validators (power 1 each, named by nameOf)
// serves for the accounts, with an account for each validator, at governanceNode's parameters: every
// validator precommitted the block at height 9 that resulted in that state.
func signedCheckpoint(validators []signing.Signer, accounts []ledger.Account) node.Checkpoint {
	return signedCheckpointAt(9, validators, accounts)
}

// signedCheckpointAt is signedCheckpoint for the block at height.
func signedCheckpointAt(height int64, validators []signing.Signer, accounts []ledger.Account) node.Checkpoint {
	checkpoint := node.Checkpoint{State: ledger.LedgerState{Accounts: withValidatorAccounts(validators, accounts),
		Parameters: governanceParameters, GovernanceSequence: governanceSequence, Height: height}}
	for _, validator := range validators {
		checkpoint.Validators = append(checkpoint.Validators, consensus.Validator{Name: nameOf(validator), PublicKey: validator.PublicKeyBase64(), Power: 1})
	}
	slices.SortFunc(checkpoint.Validators, func(first, second consensus.Validator) int { return strings.Compare(first.Name, second.Name) })
	return signedBy(checkpoint, validators)
}

// signedBy is checkpoint with the header of a block at its state's height signed by the validators it
// carries, and a commit of every one of the signers given (named by nameOf) over that block's hash.
func signedBy(checkpoint node.Checkpoint, signers []signing.Signer) node.Checkpoint {
	height := checkpoint.State.Height
	checkpoint.LastBlock = ledger.BlockHeader{Height: height, ValidatorsHash: ledger.ValidatorsHash(checkpoint.Validators)}
	checkpoint.Commit = consensus.Commit{Height: height, BlockHash: checkpoint.LastBlock.BlockHash(checkpoint.State.Digest())}
	for _, signer := range signers {
		vote, _ := consensus.SignVote(signer, consensus.Vote{Height: height, Type: consensus.Precommit, BlockHash: checkpoint.Commit.BlockHash, Validator: nameOf(signer)})
		checkpoint.Commit.Votes = append(checkpoint.Commit.Votes, vote)
	}
	return checkpoint
}

// state is the committed state the node serves (its accounts at governanceParameters and governanceSequence).
func (testNode *governanceNode) state() ledger.LedgerState {
	return ledger.LedgerState{Accounts: testNode.accounts, Parameters: governanceParameters, GovernanceSequence: governanceSequence}
}

// change runs change under the node's mutex, for a test to change what the node serves.
func (testNode *governanceNode) change(change func()) {
	testNode.mutex.Lock()
	defer testNode.mutex.Unlock()
	change()
}

func (testNode *governanceNode) posted() []recordedPost {
	testNode.mutex.Lock()
	defer testNode.mutex.Unlock()
	return append([]recordedPost(nil), testNode.posts...)
}

// A reassignment vote binds the account's name, its balance and the governance sequence. Reviewing the
// vote shows them and signs and sends nothing; a name with no account is refused, never signed as a
// literal. The pending table names each account.
func TestReassignmentReviewShowsTargetAndSendsNothing(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.pending = []node.ReassignmentView{{Name: "coop", PreviousAmount: "7", NewAmount: "8"}}
	testNode.setState([]signing.Signer{walletSigner(t, wallet), testNodeSigner(t), testNodeSigner(t)},
		ledger.Account{Name: "coop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(7, 1)})
	walletUI := routes(wallet, "127.0.0.1:8090", "")
	post := func(form url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "http://127.0.0.1:8090/admin/reassignment", strings.NewReader(form.Encode()))
		request.Header.Set("Origin", "http://127.0.0.1:8090")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		walletUI.ServeHTTP(recorder, request)
		return recorder
	}

	page := html.UnescapeString(post(url.Values{"name": {"coop"}, "amount": {"1000000"}}).Body.String())
	for _, want := range []string{"name: coop", "current balance: 7", "new balance: 1000000", "governance sequence: 4",
		"Sign vote", `<td><strong>coop</strong></td>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the review page does not show %q", want)
		}
	}
	refused := post(url.Values{"name": {"nobody"}, "amount": {"500"}})
	if location := refused.Header().Get("Location"); refused.Code != http.StatusSeeOther || !strings.Contains(location, "err=") {
		t.Errorf("a name with no account was not refused: status %d, location %q", refused.Code, location)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Errorf("reviewing votes sent %d request(s) to the node, first to %s", len(posts), posts[0].path)
	}
}

// Votes are collected off the network. Signing hands the signed vote back as text and sends nothing.
// Submitting assembles the pasted votes into one certificate and POSTs it once, only when distinct
// current validators holding a quorum signed the same values: a non-validator's vote, a duplicate and
// a vote for other values are left out, and short of a quorum nothing is sent.
func TestReassignmentVotesAreCollectedOffNode(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	secondValidator, thirdValidator, outsider := testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)
	testNode.setState([]signing.Signer{walletSigner(t, wallet), secondValidator, thirdValidator},
		ledger.Account{Name: "target", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(100, 1)})

	draft, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: "target", NewAmount: "0"})
	if err != nil {
		t.Fatal(err)
	}
	signedDraft, submitted, err := wallet.SignReassignment(verified(t, wallet), draft.Certificate)
	if err != nil || submitted {
		t.Fatalf("sign: submitted %v, err %v", submitted, err)
	}
	ownVote := signedDraft.Certificate
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("signing a vote sent %d request(s) to the node", len(posts))
	}
	var signed ledger.Reassignment
	if err := json.Unmarshal([]byte(ownVote), &signed); err != nil || len(signed.Votes) != 1 || signed.Votes[0].Validator != nameOf(walletSigner(t, wallet)) ||
		!ledger.VerifyReassignmentVote(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(),
			ledger.Reassignment{Name: "target", PreviousAmount: big.NewRat(100, 1), NewAmount: new(big.Rat), Sequence: governanceSequence}, signed.Votes[0]) {
		t.Fatalf("the signed vote %s is not this wallet's vote, under its name, for the drafted values (%v)", ownVote, err)
	}
	voteBy := func(signer signing.Signer, newAmount *big.Rat) string {
		action := ledger.Reassignment{Name: signed.Name, PreviousAmount: signed.PreviousAmount, NewAmount: newAmount, Sequence: signed.Sequence}
		vote, _ := ledger.SignReassignmentVote(signer, nameOf(signer), action)
		action.Votes = []ledger.ReassignmentVote{vote}
		text, _ := json.Marshal(action)
		return string(text)
	}
	zero := new(big.Rat)

	if err := wallet.SubmitReassignment(ownVote + voteBy(secondValidator, zero) + voteBy(outsider, zero) + voteBy(thirdValidator, big.NewRat(1, 1))); err == nil {
		t.Error("votes short of a quorum were submitted")
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("an incomplete certificate reached the node (%d request(s))", len(posts))
	}
	if err := wallet.SubmitReassignment(ownVote + "\n" + voteBy(secondValidator, zero) + voteBy(secondValidator, zero) + voteBy(outsider, zero) + voteBy(thirdValidator, zero)); err != nil {
		t.Fatal(err)
	}
	posts := testNode.posted()
	if len(posts) != 1 || posts[0].path != "/hdls/reassignment-vote" {
		t.Fatalf("want one POST to /hdls/reassignment-vote, got %d", len(posts))
	}
	var certificate ledger.Reassignment
	_ = json.Unmarshal(posts[0].body, &certificate)
	if len(certificate.Votes) != 3 || !certificate.Valid(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators), testNode.state()) {
		t.Errorf("the submitted certificate (%d votes) is not valid", len(certificate.Votes))
	}
}

// A parameter vote goes the same way: the review spells out the profile change, signing sends
// nothing, and a quorum's votes are submitted once, as one certificate.
func TestParameterVotesAreCollectedOffNode(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	secondValidator, thirdValidator := testNodeSigner(t), testNodeSigner(t)
	testNode.setState([]signing.Signer{walletSigner(t, wallet), secondValidator, thirdValidator})

	draft, err := wallet.DraftParameters(verified(t, wallet), parameterForm{TransactionCost: "0.2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(draft.Facts, "\n"), "transaction cost: 0.1 → 0.2") {
		t.Errorf("the review does not spell out the change: %q", draft.Facts)
	}
	signedDraft, submitted, err := wallet.SignParameters(verified(t, wallet), draft.Certificate)
	if err != nil || submitted || len(testNode.posted()) != 0 {
		t.Fatalf("sign: submitted %v, err %v, %d request(s) sent", submitted, err, len(testNode.posted()))
	}
	ownVote := signedDraft.Certificate
	var signed ledger.ParameterChange
	if err := json.Unmarshal([]byte(ownVote), &signed); err != nil {
		t.Fatal(err)
	}
	voteBy := func(signer signing.Signer) string {
		vote, _ := ledger.SignParameterVote(signer, nameOf(signer), signed.NewParameters, signed.PreviousParametersDigest, signed.Sequence)
		change := signed
		change.Votes = []ledger.ParameterVote{vote}
		text, _ := json.Marshal(change)
		return string(text)
	}
	if err := wallet.SubmitParameters(ownVote + voteBy(secondValidator) + voteBy(thirdValidator)); err != nil {
		t.Fatal(err)
	}
	posts := testNode.posted()
	if len(posts) != 1 || posts[0].path != "/hdls/parameter-vote" {
		t.Fatalf("want one POST to /hdls/parameter-vote, got %d", len(posts))
	}
	var certificate ledger.ParameterChange
	_ = json.Unmarshal(posts[0].body, &certificate)
	if certificate.NewParameters.TransactionCost.Cmp(big.NewRat(1, 5)) != 0 ||
		!certificate.Valid(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators), governanceParameters, governanceSequence) {
		t.Errorf("the submitted certificate is not a valid vote for cost 0.2")
	}
}

// A parameter certificate has no new key. Votes with the power needed that amend a governance sequence
// that has moved are refused for what can move under a parameter vote, never for a new key's proof.
func TestParameterVotesForAMovedSequenceNameTheirCause(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	signer := walletSigner(t, wallet)
	testNode.setState([]signing.Signer{signer})

	newParameters := governanceParameters
	newParameters.TransactionCost = big.NewRat(1, 5)
	stale := ledger.ParameterChange{NewParameters: newParameters, PreviousParametersDigest: governanceParameters.Digest(), Sequence: governanceSequence - 1}
	vote, err := ledger.SignParameterVote(signer, nameOf(signer), stale.NewParameters, stale.PreviousParametersDigest, stale.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	stale.Votes = []ledger.ParameterVote{vote}
	text, _ := json.Marshal(stale)
	err = wallet.SubmitParameters(string(text))
	if err == nil || !strings.Contains(err.Error(), "governance sequence") || strings.Contains(err.Error(), "new key") {
		t.Errorf("submitting votes for a moved governance sequence: %v; want a refusal that names the sequence, and no new key", err)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Errorf("%d request(s) reached the node", len(posts))
	}
}

// The ledger refuses a timing whose step timeout is too short for the fan-out and, with its ceiling,
// one whose timings are too long. The wallet does not know which bound a profile broke, so its
// refusal states the profile and the rule instead of calling the timeout too short.
func TestParameterTimingRefusalIsNeutral(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet)})
	_, err := wallet.DraftParameters(verified(t, wallet), parameterForm{StepTimeoutMs: "1"})
	if err == nil || strings.Contains(err.Error(), "too short") || !strings.Contains(err.Error(), "step timeout 1 ms") {
		t.Errorf("refusing a 1 ms step timeout: %v; want a neutral message naming the profile", err)
	}
}

// In the single-validator demo the wallet's name alone is a quorum, so signing submits the complete
// certificate at once.
func TestSoleValidatorVoteIsSubmittedAtOnce(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet)}, ledger.Account{Name: "target", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)})

	draft, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: "target", NewAmount: "6"})
	if err != nil {
		t.Fatal(err)
	}
	if _, submitted, err := wallet.SignReassignment(verified(t, wallet), draft.Certificate); err != nil || !submitted {
		t.Fatalf("sign: submitted %v, err %v", submitted, err)
	}
	posts := testNode.posted()
	if len(posts) != 1 {
		t.Fatalf("want one POST, got %d", len(posts))
	}
	var certificate ledger.Reassignment
	_ = json.Unmarshal(posts[0].body, &certificate)
	if !certificate.Valid(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators), testNode.state()) {
		t.Error("the submitted certificate is not valid")
	}
}

// The parameter draft takes all ten parameters: the vote carries each value typed, its review shows
// each change, and a value the network refuses is refused before anything is signed.
func TestParameterDraftCarriesTheTenParameters(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet)})

	draft, err := wallet.DraftParameters(verified(t, wallet), parameterForm{
		TransactionCost: "0.2", BlockIntervalMs: "600", StepTimeoutMs: "2500", MaxPeers: "7", MaxMessageBytes: "0",
		NewKeyFee: "3", ChangeDelayBlocks: "21", RetainBlocks: "1001", MaxPaymentAmount: "600", MaxAccountBalance: "9001",
	})
	if err != nil {
		t.Fatal(err)
	}
	var change ledger.ParameterChange
	if err := json.Unmarshal([]byte(draft.Certificate), &change); err != nil {
		t.Fatal(err)
	}
	want := ledger.LedgerParameters{TransactionCost: big.NewRat(1, 5), BlockIntervalMs: 600, StepTimeoutMs: 2500, MaxPeers: 7, MaxMessageBytes: 0,
		NewKeyFee: big.NewRat(3, 1), ChangeDelayBlocks: 21, RetainBlocks: 1001, MaxPaymentAmount: big.NewRat(600, 1), MaxAccountBalance: big.NewRat(9001, 1)}
	if change.NewParameters.Digest() != want.Digest() {
		t.Errorf("the drafted vote carries %+v, want %+v", change.NewParameters, want)
	}
	facts := strings.Join(draft.Facts, "\n")
	for _, fact := range []string{"new key fee: 1 → 3", "change delay: 86400 blocks → 21 blocks", "retention: 604800 blocks → 1001 blocks",
		"largest payment: 100000 → 600", "largest balance: 10000000 → 9001", "max peers: 0 → 7", fmt.Sprintf("max message bytes: %d → 0", ledger.DefaultParameters().MaxMessageBytes)} {
		if !strings.Contains(facts, fact) {
			t.Errorf("the review does not show %q:\n%s", fact, facts)
		}
	}
	for _, refused := range []parameterForm{{RetainBlocks: "999"}, {ChangeDelayBlocks: "0"}, {NewKeyFee: "0"}, {MaxAccountBalance: "0"}, {MaxPeers: "1001"}} {
		if _, err := wallet.DraftParameters(verified(t, wallet), refused); err == nil {
			t.Errorf("a draft of %+v passed review", refused)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("drafting sent %d request(s) to the node", len(posts))
	}
}

// A parameter draft is signed only while it fits the verified state, as the node checks the
// certificate: it amends the parameters in force, at the governance sequence, with values the network
// accepts. Otherwise the wallet says to draft it again, and signs and sends nothing, even where its vote
// alone is a quorum.
func TestParameterDraftIsSignedOnlyWhileItFitsTheState(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet)})
	draft, err := wallet.DraftParameters(verified(t, wallet), parameterForm{TransactionCost: "0.2"})
	if err != nil {
		t.Fatal(err)
	}
	var change ledger.ParameterChange
	if err := json.Unmarshal([]byte(draft.Certificate), &change); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*ledger.ParameterChange){
		"another governance sequence": func(change *ledger.ParameterChange) { change.Sequence++ },
		"other parameters amended":    func(change *ledger.ParameterChange) { change.PreviousParametersDigest[0] ^= 1 },
		"a retention below its floor": func(change *ledger.ParameterChange) { change.NewParameters.RetainBlocks = ledger.MinRetainBlocks - 1 },
	} {
		altered := change
		alter(&altered)
		text, _ := json.Marshal(altered)
		if _, submitted, err := wallet.SignParameters(verified(t, wallet), string(text)); err == nil || submitted || !strings.Contains(err.Error(), "draft it again") {
			t.Errorf("a draft with %s: submitted %v, err %v; want it refused, to be drafted again", name, submitted, err)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("a draft that does not fit the state sent %d request(s) to the node", len(posts))
	}
	if _, submitted, err := wallet.SignParameters(verified(t, wallet), draft.Certificate); err != nil || !submitted {
		t.Errorf("the draft as drafted: submitted %v, err %v; want it signed and submitted", submitted, err)
	}
}

// The wallet does not trust its node's governance view: an answer that is not a success is refused,
// whatever its body.
func TestGovernanceViewRefusesAFailedAnswer(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"totalPower": 3, "quorum": 3})
	}))
	t.Cleanup(failing.Close)
	if view, err := newTestWallet(t, failing.URL).Governance(); err == nil {
		t.Errorf("a governance view answered with status 500 was read: %+v", view)
	}
}

// The ⅔ may not raise a balance above the largest balance, so a draft that would is refused before
// anything is signed; one up to the cap, or one lowering a balance already above it, is drafted.
func TestReassignmentDraftRefusesABalanceAboveTheCap(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet)}, ledger.Account{Name: "target", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)},
		ledger.Account{Name: "rich", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(20_000_000, 1)}) // above the default cap of 10,000,000
	if _, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: "target", NewAmount: "10000000.00000001"}); err == nil || !strings.Contains(err.Error(), "largest balance") {
		t.Errorf("a draft raising a balance above the cap was not refused: %v", err)
	}
	for _, accepted := range [][2]string{{"target", "10000000"}, {"rich", "19000000"}} {
		if _, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: accepted[0], NewAmount: accepted[1]}); err != nil {
			t.Errorf("a draft setting a balance to %s was refused: %v", accepted[1], err)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("drafting sent %d request(s) to the node", len(posts))
	}
}

// A name action with a new key (spec 7.6), here recovery: the ⅔ unfreeze a thief's frozen name with the
// victim's new key. The draft shows the name's current key fingerprint, balance, bank, frozen mark and
// pending change, and its text goes to the new key's wallet first, whose Sign step signs the proof of
// possession and sends nothing. A validator's vote waits for that proof; then the sole validator's vote
// is a quorum, and the certificate it submits is valid.
func TestNameActionDraftCollectsTheNewKeyProof(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank, newWallet := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	thief := testNodeSigner(t)
	testNode.pending = []node.ReassignmentView{{Name: "bob", NewKey: thief.PublicKeyBase64(), Freeze: false, Unfreeze: true}}
	testNode.setState([]signing.Signer{walletSigner(t, bank)}, ledger.Account{Name: "alice", PublicKey: thief.PublicKeyBase64(), Amount: big.NewRat(5, 1),
		Bank: nameOf(walletSigner(t, bank)), Frozen: true})

	draftPage := html.UnescapeString(submitForm(bank, "/admin/reassignment", url.Values{"name": {"alice"}, "newKey": {newWallet.PublicKey()}, "freeze": {"unfreeze"}}).Body.String())
	for _, want := range []string{"name: alice", "current key: " + fingerprint(thief.PublicKeyBase64()), "current balance: 5", "current bank of record: " + nameOf(walletSigner(t, bank)),
		"frozen: true", "pending change: none", "new key: " + fingerprint(newWallet.PublicKey()) + " (its proof of possession: false)", "unfreeze the name",
		"new key " + fingerprint(thief.PublicKeyBase64()) + " unfreeze"} {
		if !strings.Contains(draftPage, want) {
			t.Errorf("the admin page does not show %q", want)
		}
	}
	draft, err := bank.DraftReassignment(verified(t, bank), nameActionForm{Name: "alice", NewKey: newWallet.PublicKey(), Freeze: "unfreeze"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draftPage, draft.Certificate) {
		t.Error("the admin page does not show the draft as text, to hand to the new key's wallet")
	}
	if _, _, err := bank.SignReassignment(verified(t, bank), draft.Certificate); err == nil || !strings.Contains(err.Error(), "new key's wallet first") {
		t.Errorf("a validator voted before the new key's proof: %v", err)
	}
	proof, submitted, err := newWallet.SignReassignment(verified(t, newWallet), draft.Certificate)
	var proved ledger.Reassignment
	if err != nil || submitted || json.Unmarshal([]byte(proof.Certificate), &proved) != nil || proved.NewKeySignature == "" || len(proved.Votes) != 0 {
		t.Fatalf("the new key's wallet did not sign the proof alone (submitted %v, err %v)", submitted, err)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("drafting and signing the proof sent %d request(s)", len(posts))
	}
	if _, submitted, err := bank.SignReassignment(verified(t, bank), proof.Certificate); err != nil || !submitted {
		t.Fatalf("the sole validator's vote: submitted %v, err %v", submitted, err)
	}
	var certificate ledger.Reassignment
	if posts := testNode.posted(); len(posts) != 1 || posts[0].path != "/hdls/reassignment-vote" || json.Unmarshal(posts[0].body, &certificate) != nil {
		t.Fatalf("want one certificate sent to /hdls/reassignment-vote, got %d request(s)", len(testNode.posted()))
	}
	if certificate.NewKey != newWallet.PublicKey() || !certificate.Unfreeze ||
		!certificate.Valid(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators), testNode.state()) {
		t.Error("the submitted certificate is not a valid unfreeze of alice with the new key")
	}
}

// Each step of the admin page says who takes its text next (UI-8). Only a name action's draft says that
// an action with a new key goes to the new key's wallet first; a parameter draft said it too. Once the
// new key's wallet has signed its proof, the page says the validators sign it next; it used to say that
// the collector pastes it into "Submit certificate", which takes votes, and the proof carries none.
func TestNameActionPagesSayWhoSignsNext(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank, newWallet := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, bank), testNodeSigner(t)},
		ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)})
	pageOf := func(wallet *Wallet, path string, form url.Values) string {
		return html.UnescapeString(submitForm(wallet, path, form).Body.String())
	}
	const newKeyFirst = "action with a new key goes to the new key's wallet"
	if strings.Contains(pageOf(bank, "/admin/parameter", url.Values{"cost": {"0.2"}}), newKeyFirst) {
		t.Error("a parameter draft says it goes to the new key's wallet first")
	}
	if !strings.Contains(pageOf(bank, "/admin/reassignment", url.Values{"name": {"shop"}, "newKey": {newWallet.PublicKey()}}), newKeyFirst) {
		t.Error("a name action's draft does not say it goes to the new key's wallet first")
	}
	draft, err := bank.DraftReassignment(verified(t, bank), nameActionForm{Name: "shop", NewKey: newWallet.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	proofPage := pageOf(newWallet, "/admin/reassignment", url.Values{"step": {"sign"}, "draft": {draft.Certificate}})
	if !strings.Contains(proofPage, "Hand this text to the validators: each signs it") || strings.Contains(proofPage, "pastes them all into") {
		t.Error("the page of the new key's proof does not say the validators sign it next, or says to submit it")
	}
}

// A validator votes on a name action with a new key only once its proof of possession verifies under that
// key, not merely when one is there; and Submit takes the first proof that verifies, so a bad proof in
// the first text pasted does not block the good one in the texts after it.
func TestNameActionVotesOnlyOnAProofThatVerifies(t *testing.T) {
	testNode := newGovernanceNode(t)
	bank, newWallet := newTestWallet(t, testNode.baseURL), newTestWallet(t, testNode.baseURL)
	secondValidator, thirdValidator := testNodeSigner(t), testNodeSigner(t)
	testNode.setState([]signing.Signer{walletSigner(t, bank), secondValidator, thirdValidator},
		ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)})
	draft, err := bank.DraftReassignment(verified(t, bank), nameActionForm{Name: "shop", NewKey: newWallet.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	proved, _, err := newWallet.SignReassignment(verified(t, newWallet), draft.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	var action ledger.Reassignment
	_ = json.Unmarshal([]byte(draft.Certificate), &action)
	forged, _ := ledger.SignReassignmentNewKey(testNodeSigner(t), action) // a proof of the right size, by another key
	if _, _, err := bank.SignReassignment(verified(t, bank), marshal(forged)); err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("a validator voted on a proof that does not verify: %v", err)
	}
	ownVote, _, err := bank.SignReassignment(verified(t, bank), proved.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	voteOf := func(validator signing.Signer, proof ledger.Reassignment) string {
		vote, _ := ledger.SignReassignmentVote(validator, nameOf(validator), action)
		proof.Votes = []ledger.ReassignmentVote{vote}
		return marshal(proof)
	}
	if err := bank.SubmitReassignment(voteOf(secondValidator, forged) + ownVote.Certificate + voteOf(thirdValidator, forged)); err != nil {
		t.Fatal(err)
	}
	var certificate ledger.Reassignment
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &certificate) != nil {
		t.Fatalf("want one certificate sent, got %d request(s)", len(posts))
	}
	if len(certificate.Votes) != 3 || !certificate.Valid(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators), testNode.state()) {
		t.Error("the submitted certificate does not carry the proof that verifies and the three votes")
	}
}

// marshal is a certificate as text, as a wallet prints it.
func marshal(certificate ledger.Reassignment) string {
	text, _ := json.Marshal(certificate)
	return string(text)
}

// A name action need not set a balance: a ⅔ freeze sets no amount (spec 4.8). It is drafted, signed by
// each validator and submitted as one certificate like any other.
func TestNameActionWithoutAmountsIsSignedAndSubmitted(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	secondValidator, thirdValidator := testNodeSigner(t), testNodeSigner(t)
	testNode.setState([]signing.Signer{walletSigner(t, wallet), secondValidator, thirdValidator},
		ledger.Account{Name: "thief", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(100, 1)})
	draft, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: "thief", Bank: ledger.ClearField, Freeze: "freeze"})
	if err != nil {
		t.Fatal(err)
	}
	ownVote, submitted, err := wallet.SignReassignment(verified(t, wallet), draft.Certificate)
	if err != nil || submitted {
		t.Fatalf("sign: submitted %v, err %v", submitted, err)
	}
	action := ledger.Reassignment{Name: "thief", Bank: ledger.ClearField, Freeze: true, Sequence: governanceSequence}
	texts := ownVote.Certificate
	for _, validator := range []signing.Signer{secondValidator, thirdValidator} {
		vote, _ := ledger.SignReassignmentVote(validator, nameOf(validator), action)
		signed := action
		signed.Votes = []ledger.ReassignmentVote{vote}
		text, _ := json.Marshal(signed)
		texts += string(text)
	}
	if err := wallet.SubmitReassignment(texts); err != nil {
		t.Fatal(err)
	}
	var certificate ledger.Reassignment
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &certificate) != nil {
		t.Fatalf("want one certificate sent, got %d request(s)", len(posts))
	}
	if certificate.PreviousAmount != nil || certificate.NewAmount != nil || !certificate.Freeze || len(certificate.Votes) != 3 ||
		!certificate.Valid(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(signedCheckpoint(testNode.validators, nil).Validators), testNode.state()) {
		t.Errorf("the submitted certificate is not a valid freeze of thief without amounts")
	}
}
