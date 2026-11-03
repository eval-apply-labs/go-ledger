package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// engine is a bank's engine, a real node: the one validator, bank-a, of a network named testnet, whose
// genesis gives the wallet's key the name alice, with a balance of 100 and bank-a as its bank of record,
// and the name shop an account at zero. Its handler serves every route, as the operator port does, and it
// writes its public folder (spec §6.1) for a fetcher to serve.
type engine struct {
	handler      http.Handler
	bank         signing.Signer // bank-a's key, for this network
	publicFolder string
}

// startEngine starts the engine for the wallet's key. Its blocks follow each other 10 ms apart, so a
// payment's block, a few ahead, commits at once.
func startEngine(t *testing.T, wallet *Wallet) engine {
	t.Helper()
	scheme := signing.NetworkScheme()
	bankPublicKey, bankPrivateKey, err := scheme.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	parameters := ledger.DefaultParameters()
	parameters.BlockIntervalMs = 10
	chain, genesisState, err := ledger.ValidateGenesis(ledger.Genesis{
		NetworkName: "testnet", GossipPort: 8070, Parameters: parameters,
		Validators: []ledger.GenesisValidator{{Name: "bank-a", Power: 1}},
		Accounts: []ledger.GenesisAccount{
			{Name: "alice", PublicKey: wallet.PublicKey(), Amount: "100", Bank: "bank-a"},
			{Name: "bank-a", PublicKey: base64.StdEncoding.EncodeToString(bankPublicKey), Amount: "0"},
			{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: "0", Bank: "bank-a"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bank, err := signing.NewSigner(scheme, chain.NetworkID, bankPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicFolder := t.TempDir()
	engineNode, err := node.Open(node.Config{Chain: chain, Signer: bank, Name: "bank-a", Genesis: genesisState, PublicPath: publicFolder}, node.NewHTTPTransport(nil))
	if err != nil {
		t.Fatal(err)
	}
	engineNode.Start()
	t.Cleanup(engineNode.Stop)
	return engine{handler: node.NewServer(engineNode).Handler(), bank: bank, publicFolder: publicFolder}
}

// publicFace is a bank's public face as cmd/fetcher is one (spec §6.2), in front of an engine's operator
// port: it answers GET /hdls/status, /hdls/checkpoint and /hdls/governance, the files the engine writes
// byte for byte as its routes answer, and passes on POST /hdls/pay, /hdls/update and /hdls/name-request.
// It answers 404 to anything else, and keeps every request it was asked, as "METHOD path".
type publicFace struct {
	baseURL string
	mutex   sync.Mutex
	asked   []string
}

// publicFaceRoutes are the routes a public face serves.
var publicFaceRoutes = []string{"GET /hdls/status", "GET /hdls/checkpoint", "GET /hdls/governance", "POST /hdls/pay", "POST /hdls/update", "POST /hdls/name-request"}

func newPublicFace(t *testing.T, engineHandler http.Handler) *publicFace {
	t.Helper()
	face := &publicFace{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		route := request.Method + " " + request.URL.Path
		face.mutex.Lock()
		face.asked = append(face.asked, route)
		face.mutex.Unlock()
		if !slices.Contains(publicFaceRoutes, route) {
			http.NotFound(responseWriter, request)
			return
		}
		engineHandler.ServeHTTP(responseWriter, request)
	}))
	t.Cleanup(server.Close)
	face.baseURL = server.URL
	return face
}

// askedFor is every request the face was asked, in order.
func (face *publicFace) askedFor() []string {
	face.mutex.Lock()
	defer face.mutex.Unlock()
	return slices.Clone(face.asked)
}

// eventually waits until condition holds, polling, and fails the test after 30 seconds.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !condition(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s after 30 seconds", what)
		}
	}
}

// accountIn is the account of name in the wallet's verified state, or none when no state verifies.
func accountIn(wallet *Wallet, name string) ledger.Account {
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		return ledger.Account{}
	}
	account, _ := ledger.FindAccount(checkpoint.State.Accounts, name)
	return account
}

// hasAmount reports whether account holds amount, written as the pages write it.
func hasAmount(account ledger.Account, amount string) bool {
	return account.Amount != nil && ledger.FormatAmount(account.Amount) == amount
}

// redirectMessage is the message a redirect carries to the page it names, or "" when it carries none.
func redirectMessage(recorder *httptest.ResponseRecorder) string {
	location, _ := url.Parse(recorder.Header().Get("Location"))
	if location == nil {
		return ""
	}
	return location.Query().Get("msg")
}

// A customer's wallet runs with -node set to its bank's public address (spec §8), which serves static
// files and passes on three submissions. Every customer page works there: the first use pins the
// network, a payment is reviewed, sent and receipted, an account update and a freeze are taken, a request
// is drafted, and the main, requests and admin pages show the state. The wallet asks the public face for
// nothing it does not serve.
func TestCustomersWalletWorksAgainstAPublicFace(t *testing.T) {
	wallet := newTestWallet(t, "")
	face := newPublicFace(t, startEngine(t, wallet).handler)
	wallet.nodeURL = face.baseURL
	eventually(t, "verified state", func() bool { _, err := wallet.verifiedState(); return err == nil })

	if body := html.UnescapeString(submitForm(wallet, "/send", url.Values{"to": {"shop"}, "amount": {"10"}}).Body.String()); !strings.Contains(body, "pay <strong>10</strong> to <strong>shop</strong>") {
		t.Error("the payment review does not show the payment")
	}
	if message := redirectMessage(submitForm(wallet, "/send", url.Values{"step": {"confirm"}, "to": {"shop"}, "amount": {"10"}})); !strings.Contains(message, "receipt from validator bank-a verified") {
		t.Errorf("the payment: %q; want a receipt from bank-a that verifies", message)
	}
	if recorder := submitForm(wallet, "/update", url.Values{"description": {"a customer"}}); redirectMessage(recorder) == "" {
		t.Errorf("the account update was refused: %q", redirectError(recorder))
	}
	draft := submitForm(wallet, "/requests", url.Values{"kind": {ledger.KeyChangeKind}, "newKey": {testNodeSigner(t).PublicKeyBase64()}})
	if body := html.UnescapeString(draft.Body.String()); draft.Code != http.StatusOK || !strings.Contains(body, "Draft: nothing is signed yet") {
		t.Errorf("the request draft: status %d, %q", draft.Code, redirectError(draft))
	}
	eventually(t, "commit of the payment and the update", func() bool {
		alice := accountIn(wallet, "alice")
		return hasAmount(alice, "90") && alice.Description == "a customer"
	})
	if body := page(t, wallet, "/"); !strings.Contains(body, `<div class="balance">90</div>`) || !strings.Contains(body, "your name: <strong>alice</strong>") {
		t.Error("the main page does not show alice's balance of 90")
	}
	if body := page(t, wallet, "/requests"); !strings.Contains(body, "your name: <strong>alice</strong>") {
		t.Error("the requests page does not show alice's name")
	}
	if body := page(t, wallet, "/admin"); !strings.Contains(body, "cost 0.1") {
		t.Error("the admin page does not show the governance the public face serves")
	}
	if message := redirectMessage(submitForm(wallet, "/freeze", url.Values{"step": {"confirm"}})); !strings.Contains(message, "freeze of alice sent") {
		t.Errorf("the freeze: %q", message)
	}

	for _, route := range face.askedFor() {
		if !slices.Contains(publicFaceRoutes, route) {
			t.Errorf("the wallet asked the public face for %s, which it does not serve", route)
		}
	}
}

// The bank operator's admin page reads governance from a public face too, but a certificate is submitted
// on the bank's operator node (spec §8). Against a public face, which answers 404, the page says so,
// not the raw answer. The same certificate is taken on the operator node.
func TestAdminPageAgainstAPublicFaceSaysWhereVotesGo(t *testing.T) {
	wallet := newTestWallet(t, "")
	bankEngine := startEngine(t, wallet)
	wallet.nodeURL = newPublicFace(t, bankEngine.handler).baseURL
	eventually(t, "verified state", func() bool { _, err := wallet.verifiedState(); return err == nil })
	draft, err := wallet.DraftParameters(verified(t, wallet), parameterForm{TransactionCost: "0.2"})
	if err != nil {
		t.Fatal(err)
	}
	var change ledger.ParameterChange
	if err := json.Unmarshal([]byte(draft.Certificate), &change); err != nil {
		t.Fatal(err)
	}
	vote, err := ledger.SignParameterVote(bankEngine.bank, "bank-a", change.NewParameters, change.PreviousParametersDigest, change.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	change.Votes = []ledger.ParameterVote{vote}
	certificate, _ := json.Marshal(change)

	refusal := redirectError(submitForm(wallet, "/admin/parameter", url.Values{"step": {"submit"}, "votes": {string(certificate)}}))
	if !strings.Contains(refusal, "votes are submitted on the bank's operator node") || strings.Contains(refusal, "404") {
		t.Errorf("a certificate submitted at a public face: %q; want where votes are submitted, not the raw answer", refusal)
	}
	if body := page(t, wallet, "/admin?err="+url.QueryEscape(refusal)); !strings.Contains(body, "cost 0.1") || !strings.Contains(body, "votes are submitted on the bank's operator node") {
		t.Error("the admin page does not show the governance and where votes are submitted")
	}

	operatorPort := httptest.NewServer(bankEngine.handler)
	defer operatorPort.Close()
	wallet.nodeURL = operatorPort.URL
	if err := wallet.SubmitParameters(string(certificate)); err != nil {
		t.Errorf("the operator node refused the certificate: %v", err)
	}
}

// A node that answers 404 to a submission does not take it. For the two certificates the wallet says
// where votes are submitted, the bank's operator node; for any other path it says plainly that the node
// does not take that path, and nothing about votes.
func TestNotFoundSaysWhereVotesGoOnlyForVotes(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	wallet := newTestWallet(t, notFound.URL)
	for _, path := range []string{"/hdls/parameter-vote", "/hdls/reassignment-vote", "/hdls/update", "/hdls/name-request", "/hdls/pay"} {
		err := wallet.postJSON(path, struct{}{})
		saysWhereVotesGo := err != nil && strings.Contains(err.Error(), "votes are submitted on the bank's operator node")
		if isVote := strings.HasSuffix(path, "-vote"); err == nil || !strings.Contains(err.Error(), path) || saysWhereVotesGo != isVote {
			t.Errorf("a 404 to %s: %v; want it named, and where votes go only for a vote", path, err)
		}
	}
}

// A payment saved as text (spec §7.8) sends nothing. Its text is a bundle of the one payment, which a
// bank imports on its operator node (POST /hdls/bundle), and the payment commits.
func TestPaymentSavedAsTextCommitsAtABank(t *testing.T) {
	wallet := newTestWallet(t, "")
	bankEngine := startEngine(t, wallet)
	face := newPublicFace(t, bankEngine.handler)
	wallet.nodeURL = face.baseURL
	eventually(t, "verified state", func() bool { _, err := wallet.verifiedState(); return err == nil })

	if saved := submitForm(wallet, "/send", url.Values{"step": {"save"}, "to": {"shop"}, "amount": {"10"}}); saved.Code != http.StatusSeeOther {
		t.Fatalf("the save step: status %d, %q; want a redirect to the payment as text", saved.Code, redirectError(saved))
	}
	body := page(t, wallet, "/send")
	_, text, _ := strings.Cut(body, `<textarea readonly rows="6">`)
	text, _, _ = strings.Cut(text, "</textarea>")
	if !strings.Contains(body, "Your payment, saved as text") || !strings.HasPrefix(text, `{"bundle":1,`) {
		t.Fatal("the page the save step redirects to does not show the payment as text")
	}
	for _, route := range face.askedFor() {
		if strings.HasPrefix(route, "POST") {
			t.Errorf("saving the payment sent %s", route)
		}
	}

	recorder := httptest.NewRecorder()
	bankEngine.handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/hdls/bundle", strings.NewReader(text)))
	var counts node.BundleCounts
	if err := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&counts); err != nil || counts.Taken != 1 {
		t.Fatalf("the bank's import: status %d, %s; want the payment taken", recorder.Code, recorder.Body.String())
	}
	eventually(t, "commit of the saved payment", func() bool {
		return hasAmount(accountIn(wallet, "alice"), "90") && hasAmount(accountIn(wallet, "shop"), "9.9")
	})
}
