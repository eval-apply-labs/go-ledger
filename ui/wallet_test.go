package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The wallet's name is the account in its verified state whose current key is the wallet's key (spec
// 7.2): never an account that only shares a name or a balance. With no such account the wallet holds no
// name, and it refuses to pay or update, since every message names its signer and nodes check it under
// that name's key; nothing is sent.
func TestWalletFindsItsNameByKey(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)},
		ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)})
	if name := nameOfKey(verified(t, wallet).State.Accounts, wallet.PublicKey()); name != "" {
		t.Fatalf("with no account under its key the wallet holds the name %q, want none", name)
	}
	if _, _, err := wallet.Send("shop", "1"); err == nil || !strings.Contains(err.Error(), "not the key of any name") {
		t.Errorf("a wallet holding no name paid: %v", err)
	}
	if _, err := wallet.UpdateAccount("hello", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "not the key of any name") {
		t.Errorf("a wallet holding no name sent an update: %v", err)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("%d request(s) sent by a wallet holding no name", len(posts))
	}

	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)},
		ledger.Account{Name: "bob", PublicKey: wallet.PublicKey(), Amount: big.NewRat(7, 1)})
	if name := nameOfKey(verified(t, wallet).State.Accounts, wallet.PublicKey()); name != "bob" {
		t.Fatalf("the wallet's name is %q, want bob, the account under its key", name)
	}
	if balance, err := balanceOf(wallet); err != nil || balance.Cmp(big.NewRat(7, 1)) != 0 {
		t.Fatalf("the wallet's balance is %v (err %v), want bob's 7", balance, err)
	}
}

// The wallet pays by name only (spec 7.3): it signs a payment from its own name to the name typed,
// which every node looks up in its own state, and refuses a recipient that is not a name, a public key
// included, or that has no account, before anything is signed.
func TestWalletPaysByName(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	testNode.setState(nil, ledger.Account{Name: "bob", PublicKey: wallet.PublicKey(), Amount: big.NewRat(7, 1)}, accountOf(shop, "0"))
	for _, recipient := range []string{shop.PublicKeyBase64(), "shop", "", "sh op"} {
		if _, _, err := wallet.Send(recipient, "1"); err == nil {
			t.Errorf("a payment to %q was signed", recipient)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("%d request(s) sent for recipients that are not names", len(posts))
	}
	if _, _, err := wallet.Send("  "+nameOf(shop)+"\n", "1"); err != nil {
		t.Fatal(err)
	}
	var payment ledger.Payment
	if posts := testNode.posted(); len(posts) != 1 || posts[0].path != "/hdls/pay" || json.Unmarshal(posts[0].body, &payment) != nil {
		t.Fatalf("want one payment sent to /hdls/pay, got %d request(s)", len(posts))
	}
	if payment.From != "bob" || payment.To != nameOf(shop) || !ledger.VerifyPayment(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), payment) {
		t.Errorf("the payment goes from %q to %q, want from bob to %s, signed under the wallet's key", payment.From, payment.To, nameOf(shop))
	}
}

// The same-block payment guard (lastPaymentBlockID) is persisted, so a wallet restarted within the
// lookahead window resumes its counter instead of resetting to zero and reissuing a block a still-
// pending pre-restart payment already named (which the ledger would answer with a balance-destroying
// overdraw ban). The height stays put here, so blockID is driven purely by the counter.
func TestPaymentBlockIDGuardSurvivesReload(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	originalWallet, err := LoadOrCreateWallet(keyfile, "testnet", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	for i := 0; i < 5; i++ {
		blockID := nextBlockID(t, originalWallet, 10)
		if blockID <= last {
			t.Fatalf("block ids must strictly increase within a session: %d then %d", last, blockID)
		}
		last = blockID
	}

	// Reload the same wallet file (a restart): the guard must not hand out a block id <= the last one.
	reloadedWallet, err := LoadOrCreateWallet(keyfile, "testnet", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if resumed := nextBlockID(t, reloadedWallet, 10); resumed <= last {
		t.Fatalf("a reloaded wallet reissued block %d (<= last used %d) — the guard did not survive restart", resumed, last)
	}
}

// The wallet never puts two signed payments for one block into the world, whatever its node reports
// (UI-1). The counter used to take its height from the node's /hdls/status, which a node can set at
// will, and it dropped a counter an earlier run had left far past that height. Run 1, at height 1000,
// named block 1003. After a restart the node reported 900, and the wallet named block 903; then it
// reported 1000, and the wallet named block 1003 again. A node that kept run 1's payment then held two
// payments for block 1003, enough to burn the payer's balance. The counter now takes the height of the
// verified state, which never goes back.
func TestBlockIDCounterNeverRepeatsWhateverTheNodeReports(t *testing.T) {
	testNode := newGovernanceNode(t)
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	start := func() *Wallet {
		wallet, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
		if err != nil {
			t.Fatal(err)
		}
		return wallet
	}
	report := func(heightInProgress int64) {
		testNode.mutex.Lock()
		defer testNode.mutex.Unlock()
		testNode.reportedHeight = heightInProgress
	}
	wallet := start()
	testNode.mutex.Lock()
	testNode.height = 999 // the state of block 999: block 1000 is in progress
	testNode.mutex.Unlock()
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(100, 1)},
		ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)})
	pay := func() {
		t.Helper()
		if _, _, err := wallet.Send("shop", "1"); err != nil {
			t.Fatal(err)
		}
	}
	pay() // run 1

	wallet = start() // a restart
	report(900)
	pay()
	time.Sleep(150 * time.Millisecond) // as in the review: a climb of one block per millisecond looked real
	report(1000)
	pay()

	var blocks []int64
	for _, post := range testNode.posted() {
		var payment ledger.Payment
		if json.Unmarshal(post.body, &payment) != nil {
			t.Fatalf("unreadable payment posted to %s", post.path)
		}
		blocks = append(blocks, payment.BlockID)
	}
	if len(blocks) != 3 || blocks[0] != 1003 {
		t.Fatalf("premise: three payments, the first for block 1003; they name the blocks %v", blocks)
	}
	if blocks[1] <= blocks[0] || blocks[2] <= blocks[1] {
		t.Errorf("the payments name the blocks %v: after the restart the wallet named a block again, or one before", blocks)
	}
}

// The payment counter never goes back, whatever height it is given: at a lower height the wallet
// refuses to sign instead, in the same run and after a restart. So does a restarted wallet whose
// counter a burst of payments left at the edge of the window, even a block lower: pending payments
// name those blocks. Once the chain advances it signs again. The counter belongs to one network:
// another network's counter is not carried over, nor is an earlier network's, whose file names no
// network name.
func TestBlockIDCounterNeverGoesBack(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	open := func(network string) *Wallet {
		wallet, err := LoadOrCreateWallet(keyfile, network, "", 3)
		if err != nil {
			t.Fatal(err)
		}
		return wallet
	}

	wallet := open("testnet")
	nextBlockID(t, wallet, 1000) // block 1003
	if blockID, err := wallet.nextPaymentBlockID(900); err == nil {
		t.Errorf("at height 900 the wallet named block %d after block 1003", blockID)
	}
	if blockID, err := open("testnet").nextPaymentBlockID(900); err == nil {
		t.Errorf("restarted at height 900 the wallet named block %d after block 1003", blockID)
	}

	if blockID := nextBlockID(t, open("othernet"), 10); blockID != 13 {
		t.Errorf("on another network the next payment names block %d, want 13", blockID)
	}
	if err := os.WriteFile(keyfile+".state", []byte(`{"networkId": "othernet", "lastPaymentBlockID": 20}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if blockID := nextBlockID(t, open("othernet"), 10); blockID != 13 {
		t.Errorf("with an earlier network's counter file, which names no network name, the next payment names block %d, want 13", blockID)
	}

	wallet = open("othernet") // carries 13: blocks 14..45 fit the window at height 10, the next does not
	for want := int64(14); want <= 10+3+blockSlack; want++ {
		if blockID := nextBlockID(t, wallet, 10); blockID != want {
			t.Fatalf("payment names block %d, want %d", blockID, want)
		}
	}
	if blockID, err := wallet.nextPaymentBlockID(10); err == nil {
		t.Errorf("the wallet named block %d, past the window at height 10", blockID)
	}
	for _, heightInProgress := range []int64{10, 9} {
		if blockID, err := open("othernet").nextPaymentBlockID(heightInProgress); err == nil {
			t.Errorf("restarted after the burst, at height %d, the wallet named block %d again", heightInProgress, blockID)
		}
	}
	if blockID := nextBlockID(t, open("othernet"), 11); blockID != 10+3+blockSlack+1 {
		t.Errorf("once the chain advanced the next payment names block %d, want %d", blockID, 10+3+blockSlack+1)
	}
}

// A node takes a payment, an update or a freeze only up to node.MaxBlockIDLookahead (64) blocks past its
// height, and a burst of payments runs up to blockSlack (32) blocks past the wallet's lookahead. So the
// wallet refuses a lookahead above 32, with an error that names the node's window, and at 32 a whole
// burst stays inside that window. A lookahead below 1 is the default, 3.
func TestBlockIDCounterStaysInsideTheNodesWindow(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	ceiling := int64(node.MaxBlockIDLookahead - blockSlack)
	window := fmt.Sprintf("the node's %d-block window", node.MaxBlockIDLookahead)
	if _, err := LoadOrCreateWallet(keyfile, "testnet", "", ceiling+1); err == nil || !strings.Contains(err.Error(), window) {
		t.Errorf("a lookahead of %d: %v; want a refusal naming %s", ceiling+1, err, window)
	}
	if wallet, err := LoadOrCreateWallet(keyfile, "testnet", "", 0); err != nil || wallet.lookahead != 3 {
		t.Errorf("a lookahead of 0 gave a wallet (err %v) whose lookahead is not the default 3", err)
	}

	wallet, err := LoadOrCreateWallet(keyfile, "testnet", "", ceiling)
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	for i := 0; i < 2*node.MaxBlockIDLookahead; i++ {
		blockID, err := wallet.nextPaymentBlockID(10)
		if err != nil {
			break
		}
		last = blockID
	}
	if last != 10+node.MaxBlockIDLookahead {
		t.Errorf("with a lookahead of %d the burst at height 10 ends at block %d, want %d, the edge of the node's window", ceiling, last, 10+node.MaxBlockIDLookahead)
	}
}

// A block carries one update per account, so of two updates naming one block only one lands: a
// description and then a delegation, sent in one block interval, left the account without its
// delegation. Each update therefore names a block of its own, like a payment, and a payment after them
// a later one.
func TestAccountUpdatesNameBlocksOfTheirOwn(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "kay", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if _, err := wallet.UpdateAccount("the corner shop", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.UpdateAccount("", "kay", "", "", ""); err != nil {
		t.Fatal(err)
	}
	var description, delegation ledger.AccountUpdate
	if posts := testNode.posted(); len(posts) != 2 || json.Unmarshal(posts[0].body, &description) != nil || json.Unmarshal(posts[1].body, &delegation) != nil {
		t.Fatalf("want two account updates sent, got %d request(s)", len(posts))
	}
	if delegation.BlockID <= description.BlockID {
		t.Fatalf("the delegation names block %d, the description before it block %d", delegation.BlockID, description.BlockID)
	}
	if paymentBlockID := nextBlockID(t, wallet, 10); paymentBlockID <= delegation.BlockID {
		t.Fatalf("a payment after the updates names block %d, not past theirs (%d)", paymentBlockID, delegation.BlockID)
	}
}

// A payment saved as text takes a block of the payment counter, as a sent payment does (spec §7.8):
// two saves, and a save and a send, never name one block, so the wallet never signs two payments for one
// block. The text is a bundle of the one signed payment, for the pinned network, and the saves send
// nothing.
func TestSavedPaymentsNameBlocksOfTheirOwn(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)}, accountOf(shop, "0"))
	checkpoint := verified(t, wallet)
	first, err := wallet.SavePayment(checkpoint, nameOf(shop), "1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := wallet.SavePayment(checkpoint, nameOf(shop), "1")
	if err != nil {
		t.Fatal(err)
	}
	if first.BlockID != checkpoint.State.Height+1+wallet.lookahead || second.BlockID != first.BlockID+1 {
		t.Errorf("two saves name blocks %d and %d, want %d and the next", first.BlockID, second.BlockID, checkpoint.State.Height+1+wallet.lookahead)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("saving payments sent %d request(s)", len(posts))
	}
	if _, _, err := wallet.Send(nameOf(shop), "1"); err != nil {
		t.Fatal(err)
	}
	var sent ledger.Payment
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &sent) != nil || sent.BlockID != second.BlockID+1 {
		t.Errorf("a payment sent after the saves names block %d, want %d", sent.BlockID, second.BlockID+1)
	}

	lines := strings.Split(strings.TrimSuffix(first.Text, "\n"), "\n")
	var header node.BundleHeader
	var line node.BundleLine
	var saved ledger.Payment
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &header) != nil || json.Unmarshal([]byte(lines[1]), &line) != nil || json.Unmarshal(line.Body, &saved) != nil {
		t.Fatalf("the saved text is not a bundle of one message: %q", first.Text)
	}
	if header != (node.BundleHeader{Bundle: node.BundleVersion, NetworkID: testNetworkID, From: "alice", HeightInProgress: checkpoint.State.Height + 1}) || line.Type != "payment" {
		t.Errorf("the bundle's header is %+v and its message a %q", header, line.Type)
	}
	if saved.From != "alice" || saved.To != nameOf(shop) || saved.BlockID != first.BlockID || !ledger.VerifyPayment(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), saved) {
		t.Errorf("the saved payment goes from %q to %q for block %d; want alice's to %s for block %d, signed under the wallet's key", saved.From, saved.To, saved.BlockID, nameOf(shop), first.BlockID)
	}
}

// "Sign and save as text" follows post, redirect, get: the POST signs the payment, keeps it, and
// redirects to GET /send, which shows the payment saved last. So a reload, or Back and Forward, repeats
// the GET and signs nothing: the payment counter does not move, and the page shows the same text. With
// nothing saved, GET /send goes to the main page.
func TestSaveStepSignsOnceWhateverTheReloads(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)}, accountOf(shop, "0"))
	nothingSaved := httptest.NewRecorder()
	routes(wallet, "127.0.0.1:8090", "").ServeHTTP(nothingSaved, httptest.NewRequest("GET", "http://127.0.0.1:8090/send", nil))
	if nothingSaved.Code != http.StatusSeeOther || nothingSaved.Header().Get("Location") != "/" {
		t.Errorf("GET /send with nothing saved: status %d to %q, want a redirect to /", nothingSaved.Code, nothingSaved.Header().Get("Location"))
	}

	saved := submitForm(wallet, "/send", url.Values{"step": {"save"}, "to": {nameOf(shop)}, "amount": {"10"}})
	if saved.Code != http.StatusSeeOther || saved.Header().Get("Location") != "/send" {
		t.Fatalf("the save step: status %d to %q (%q), want a redirect to /send", saved.Code, saved.Header().Get("Location"), redirectError(saved))
	}
	wallet.mutex.Lock()
	counter := wallet.saved.LastPaymentBlockID
	wallet.mutex.Unlock()
	first, reload := page(t, wallet, "/send"), page(t, wallet, "/send")
	if want := fmt.Sprintf("pay <strong>10</strong> to <strong>%s</strong>, for block %d", nameOf(shop), counter); !strings.Contains(first, want) || first != reload {
		t.Errorf("GET /send does not show the payment saved, for block %d, the same on a reload", counter)
	}
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	if wallet.saved.LastPaymentBlockID != counter || len(testNode.posted()) != 0 {
		t.Errorf("showing the saved payment again moved the payment counter from %d to %d, or sent something", counter, wallet.saved.LastPaymentBlockID)
	}
}

// An account backs no validator until its holder delegates, so the account form takes a delegate by
// name: the signed update names that name (here the wallet's own, a bank backing itself), "-" clears it,
// and a delegate that is not a name is refused before anything is sent.
func TestAccountFormDelegates(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "kay", PublicKey: wallet.PublicKey(), Amount: big.NewRat(500, 1)})
	walletUI := routes(wallet, "127.0.0.1:8090", "")
	post := func(delegate string) string {
		request := httptest.NewRequest("POST", "http://127.0.0.1:8090/update", strings.NewReader(url.Values{"delegate": {delegate}}.Encode()))
		request.Header.Set("Origin", "http://127.0.0.1:8090")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		walletUI.ServeHTTP(recorder, request)
		return recorder.Header().Get("Location")
	}

	for _, refused := range []string{"Not A Name", wallet.PublicKey()} {
		if location := post(refused); !strings.Contains(location, "err=") || len(testNode.posted()) != 0 {
			t.Fatalf("a delegate that is not a name was not refused (redirect %q, %d request(s) sent)", location, len(testNode.posted()))
		}
	}
	for _, delegate := range []string{"kay", ledger.ClearField} {
		if location := post(delegate); strings.Contains(location, "err=") {
			t.Fatalf("delegating to %q refused: %q", delegate, location)
		}
	}
	posts := testNode.posted()
	var update, cleared ledger.AccountUpdate
	if len(posts) != 2 || json.Unmarshal(posts[0].body, &update) != nil || json.Unmarshal(posts[1].body, &cleared) != nil {
		t.Fatalf("want two account updates sent, got %d request(s)", len(posts))
	}
	if update.Name != "kay" || update.Delegate != "kay" || !ledger.VerifyAccountUpdate(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), update) {
		t.Errorf("the update of %q delegates to %q, want kay's to kay, signed under the wallet's key", update.Name, update.Delegate)
	}
	if cleared.Delegate != ledger.ClearField {
		t.Errorf("the second update delegates to %q, want %q (none)", cleared.Delegate, ledger.ClearField)
	}
}

// The network reads an amount only in plain form and on the 1e-8 grid (ledger.ParseAmount). The wallet
// used to sign whatever big.Rat parsed: a payment every node then refused as "bad json", and a vote
// whose review showed the amount but that could not be signed ("unreadable draft"). Such an amount is
// now refused before anything is signed or sent, with the ledger's reason; one on the grid still goes.
func TestAmountsOffTheGridAreRefusedBeforeSigning(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	target := "target"
	testNode.setState([]signing.Signer{walletSigner(t, wallet)}, accountOf(walletSigner(t, wallet), "5"),
		ledger.Account{Name: target, PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(7, 1)})
	for _, amount := range []string{"0.123456789", "1/3", "1e-9", "1e2"} {
		if _, _, err := wallet.Send(target, amount); err == nil {
			t.Errorf("a payment of %q was signed", amount)
		}
		if _, err := wallet.DraftParameters(verified(t, wallet), parameterForm{TransactionCost: amount}); err == nil {
			t.Errorf("a transaction cost of %q passed review", amount)
		}
		if _, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: target, NewAmount: amount}); err == nil {
			t.Errorf("a balance of %q passed review", amount)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("%d request(s) sent for amounts the network refuses", len(posts))
	}
	if _, _, err := wallet.Send(target, "0.12345678"); err != nil || len(testNode.posted()) != 1 {
		t.Fatalf("a payment on the grid was not sent: %v", err)
	}
	if _, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: target, NewAmount: "0.12345678"}); err != nil {
		t.Fatalf("a balance on the grid did not pass review: %v", err)
	}
	// People type ".5": the wallet reads it as 0.5.
	var payment ledger.Payment
	if _, _, err := wallet.Send(target, ".5"); err != nil || len(testNode.posted()) != 2 || json.Unmarshal(testNode.posted()[1].body, &payment) != nil {
		t.Fatalf("a payment of .5 was not sent: %v", err)
	}
	if payment.Amount.Cmp(big.NewRat(1, 2)) != 0 {
		t.Errorf("a payment of .5 was signed for %s, want 1/2", payment.Amount.RatString())
	}
	if _, err := wallet.DraftReassignment(verified(t, wallet), nameActionForm{Name: target, NewAmount: ".5"}); err != nil {
		t.Errorf("a balance of .5 did not pass review: %v", err)
	}
}

// statusNode is a node that only reports a height, which the test may change.
func statusNode(t *testing.T, height *atomic.Int64) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(map[string]int64{"heightInProgress": height.Load()})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// The wallet reads its node's /hdls/status as it reads any answer of a node it does not trust (C12,
// C67): it refuses a status of 300 or more, and reads at most maxStatusBytes. It used to decode either
// answer below, and take the height and the network id in it. After a sane height it ignores one that
// outruns it faster than blocks commit, so one wild answer cannot send a freeze or a draft far ahead.
func TestWalletBoundsTheStatusItReads(t *testing.T) {
	var reported atomic.Int64
	reported.Store(10)
	wallet := newTestWallet(t, statusNode(t, &reported))
	if height, err := wallet.height(); err != nil || height != 10 {
		t.Fatalf("the wallet read the height %d (err %v), want 10", height, err)
	}
	reported.Store(1 << 40)
	if height, err := wallet.height(); err != nil || height != 10 {
		t.Errorf("after the height 10 the wallet took the height %d (err %v), want 10 again", height, err)
	}

	for name, answer := range map[string]func(responseWriter http.ResponseWriter){
		"an error status": func(responseWriter http.ResponseWriter) {
			responseWriter.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": 10, "networkId": testNetworkID})
		},
		"an answer past the bound": func(responseWriter http.ResponseWriter) {
			_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": 10, "networkId": testNetworkID, "padding": strings.Repeat("x", maxStatusBytes)})
		},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) { answer(responseWriter) }))
		wallet := newTestWallet(t, server.URL)
		if height, err := wallet.height(); err == nil {
			t.Errorf("%s: the wallet took the height %d", name, height)
		}
		if networkID, err := wallet.servedNetworkID(); err == nil {
			t.Errorf("%s: the wallet took the network id %q", name, networkID)
		}
		server.Close()
	}
}

// verified is the wallet's verified state, read as a page reads it; the test fails if none verifies.
func verified(t *testing.T, wallet *Wallet) node.Checkpoint {
	t.Helper()
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

// balanceOf is the balance of the wallet's name in its verified state, as its main page shows it, or
// zero when its key holds no name.
func balanceOf(wallet *Wallet) (*big.Rat, error) {
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		return nil, err
	}
	return ledger.AccountAmount(checkpoint.State.Accounts, nameOfKey(checkpoint.State.Accounts, wallet.PublicKey())), nil
}

// nextBlockID is the block the wallet's counter names for a payment at the height in progress; the
// test fails if it refuses.
func nextBlockID(t *testing.T, wallet *Wallet, heightInProgress int64) int64 {
	t.Helper()
	blockID, err := wallet.nextPaymentBlockID(heightInProgress)
	if err != nil {
		t.Fatal(err)
	}
	return blockID
}

// page is the wallet's page at path, as the operator's browser shows it: its HTML, unescaped.
func page(t *testing.T, wallet *Wallet, path string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	routes(wallet, "127.0.0.1:8090", "").ServeHTTP(recorder, httptest.NewRequest("GET", "http://127.0.0.1:8090"+path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, recorder.Code)
	}
	return html.UnescapeString(recorder.Body.String())
}

// submitForm posts form to the wallet's path as the operator's browser does.
func submitForm(wallet *Wallet, path string, form url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "http://127.0.0.1:8090"+path, strings.NewReader(form.Encode()))
	request.Header.Set("Origin", "http://127.0.0.1:8090")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	routes(wallet, "127.0.0.1:8090", "").ServeHTTP(recorder, request)
	return recorder
}

// redirectError is the error a redirect carries to the page it names, or "" when it carries none.
func redirectError(recorder *httptest.ResponseRecorder) string {
	location, _ := url.Parse(recorder.Header().Get("Location"))
	if location == nil {
		return ""
	}
	return location.Query().Get("err")
}

// A wallet whose key holds no name says so (spec 7.2): "not registered", its key's fingerprint, the first
// 16 hex digits of the key's SHA-256, and a button that copies the full key. The accounts table shows each
// name's bank, frozen mark and balance, and its key's fingerprint, never a key in full.
func TestUnregisteredWalletShowsItsKeyFingerprint(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	frozen := testNodeSigner(t)
	testNode.setState(nil, ledger.Account{Name: "bakery", PublicKey: shop.PublicKeyBase64(), Amount: big.NewRat(5, 1), Bank: nameOf(testNode.validators[0])},
		ledger.Account{Name: "thief", PublicKey: frozen.PublicKeyBase64(), Amount: big.NewRat(3, 1), Frozen: true})
	rawKey, _ := base64.StdEncoding.DecodeString(wallet.PublicKey())
	keyHash := sha256.Sum256(rawKey)
	body := page(t, wallet, "/")
	for _, want := range []string{"not registered", hex.EncodeToString(keyHash[:])[:16], `data-key="` + wallet.PublicKey() + `"`, "copy my full key",
		fingerprint(shop.PublicKeyBase64()), "<td>bakery</td><td>" + nameOf(testNode.validators[0]) + "</td>", "thief <strong>(frozen)</strong>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	for _, key := range []string{shop.PublicKeyBase64(), frozen.PublicKeyBase64(), testNode.validators[0].PublicKeyBase64()} {
		if strings.Contains(body, key) {
			t.Errorf("the page prints an account's key in full")
		}
	}
}

// "not registered" is a fact of a verified state (spec 7.2): no account there has the wallet's key.
// When no state verified, the pages said it anyway, and told the holder to register at their bank
// (UI-2): for a wallet whose pin no longer matches its node's validators, and for one whose node is
// down. They now say that no state verifies. The admin page no longer says such a wallet is not a
// validator, nor shows the parameters it could not read as zeros. A wallet whose verified state gives
// its key no name still reads "not registered".
func TestUnregisteredWalletShowsOnlyAfterAStateVerifies(t *testing.T) {
	testNode := newGovernanceNode(t)
	stale := newTestWallet(t, testNode.baseURL)
	alice := ledger.Account{Name: "alice", PublicKey: stale.PublicKey(), Amount: big.NewRat(5, 1)}
	testNode.setState(nil, alice)
	verified(t, stale)                                            // it pins the node's validators
	testNode.setState([]signing.Signer{testNodeSigner(t)}, alice) // the banks re-key: no state verifies for it
	unregistered := newTestWallet(t, testNode.baseURL)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	offline := newTestWallet(t, closed.URL)

	for name, wallet := range map[string]*Wallet{"a wallet whose pin no longer matches": stale, "a wallet whose node is down": offline} {
		for _, path := range []string{"/", "/requests"} {
			if body := page(t, wallet, path); strings.Contains(body, "not registered") || !strings.Contains(body, "No verified state") {
				t.Errorf("%s: %s says the wallet is not registered, or does not say that no state verifies", name, path)
			}
		}
		if body := page(t, wallet, "/admin"); strings.Contains(body, "not a validator") || strings.Contains(body, "quorum (> ⅔) 0") {
			t.Errorf("%s: the admin page says the wallet is not a validator, or shows a quorum of 0", name)
		}
	}
	for _, path := range []string{"/", "/requests"} {
		if body := page(t, unregistered, path); !strings.Contains(body, "not registered") {
			t.Errorf("%s does not say that a wallet whose key has no name is not registered", path)
		}
	}
	if body := page(t, unregistered, "/admin"); !strings.Contains(body, "This wallet is not a validator") {
		t.Error("the admin page does not say that a wallet with no name is not a validator")
	}
}

// A wallet whose key is no longer its name's key, after a sale or a theft, shows "not registered", names
// the last name it held, from <keyfile>.state, and says that name now has another key (spec 7.5).
func TestWalletWhoseNameHasAnotherKeySaysSo(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if body := page(t, wallet, "/"); strings.Contains(body, "now has another key") {
		t.Fatal("the page says alice has another key while she has the wallet's")
	}
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)})
	body := page(t, wallet, "/")
	for _, want := range []string{"not registered", "This wallet held the name <strong>alice</strong>. That name now has another key."} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
}

// A key change to the wallet's key shows the name it will hold: "becomes yours at block N" (spec 7.2).
func TestPendingKeyShowsBecomesYours(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1),
		PendingKey: wallet.PublicKey(), PendingHeight: 50})
	if body := page(t, wallet, "/"); !strings.Contains(body, "<strong>alice</strong> becomes yours at block 50") {
		t.Errorf("the page does not say alice becomes the wallet's at block 50")
	}
}

// A pending change on the wallet's own name is announced next to the freeze button (spec 7.5): a key
// change, with the new key's fingerprint, or a bank change, with the new bank, and the block it takes
// effect at.
func TestPendingChangeOnOwnNameShowsAWarning(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	thief := testNodeSigner(t)
	for _, testCase := range []struct {
		account ledger.Account
		want    string
	}{
		{ledger.Account{PendingKey: thief.PublicKeyBase64(), PendingHeight: 50}, "A key change (to key " + fingerprint(thief.PublicKeyBase64()) + "…) on your name takes effect at block 50."},
		{ledger.Account{PendingBank: "bank-b", PendingHeight: 60}, "A bank change (to bank-b) on your name takes effect at block 60."},
	} {
		account := testCase.account
		account.Name, account.PublicKey, account.Amount = "alice", wallet.PublicKey(), big.NewRat(5, 1)
		testNode.setState(nil, account)
		body := page(t, wallet, "/")
		for _, want := range []string{testCase.want, "If you did not ask for it, freeze your name now.", `<form method="post" action="/freeze">`} {
			if !strings.Contains(body, want) {
				t.Errorf("the page does not show %q", want)
			}
		}
	}
}

// The wallet's own name is frozen: the page says so and what to do, and offers no freeze (spec 7.5).
func TestFrozenNameShowsAWarning(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1), Frozen: true})
	body := page(t, wallet, "/")
	if !strings.Contains(body, "Your name is frozen. Only a ⅔ vote of the banks can unfreeze it, with a new key. Contact your bank.") {
		t.Error("the page does not say the name is frozen")
	}
	if strings.Contains(body, `action="/freeze"`) {
		t.Error("the page offers to freeze a frozen name")
	}
}

// The freeze button asks to confirm, and signs and sends nothing until then. Confirmed, it signs a freeze
// of the wallet's own name under its key, sends it to the node, and the page shows it as sent (spec 7.3).
func TestFreezeButtonSubmitsAFreeze(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if body := page(t, wallet, "/"); !strings.Contains(body, "Freeze alice") {
		t.Fatal("the page has no freeze button for alice")
	}
	confirm := submitForm(wallet, "/freeze", nil)
	if body := html.UnescapeString(confirm.Body.String()); !strings.Contains(body, "Freeze alice?") || !strings.Contains(body, `name="step" value="confirm"`) {
		t.Fatalf("the freeze button did not ask to confirm (status %d)", confirm.Code)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("the freeze button sent %d request(s) before it was confirmed", len(posts))
	}
	if refusal := redirectError(submitForm(wallet, "/freeze", url.Values{"step": {"confirm"}})); refusal != "" {
		t.Fatal(refusal)
	}
	var freeze ledger.NameRequest
	if posts := testNode.posted(); len(posts) != 1 || posts[0].path != "/hdls/name-request" || json.Unmarshal(posts[0].body, &freeze) != nil {
		t.Fatalf("want one freeze sent to /hdls/name-request, got %d request(s)", len(posts))
	}
	if freeze.Kind != ledger.FreezeKind || freeze.Name != "alice" || freeze.Requestor != "alice" || freeze.BlockID != 13 ||
		ledger.NameRequestShapeError(freeze) != nil || !ledger.VerifyNameRequest(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), "", freeze) {
		t.Errorf("the freeze sent is %+v, want alice's own freeze for block 13 (height 10 + lookahead 3), signed under the wallet's key", freeze)
	}
	if body := page(t, wallet, "/"); !strings.Contains(body, "Your freeze of alice was sent for block 13.") {
		t.Error("the page does not show the freeze as sent")
	}
}

// A freeze names height + lookahead, or the block of a pending change on the name if that is sooner: a
// freeze applies before the changes due in its block, so it still cancels that change (spec 7.3).
func TestFreezeNamesTheBlockBeforeThePendingChange(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	thief := testNodeSigner(t)
	for pendingHeight, want := range map[int64]int64{11: 11, 13: 13, 20: 13} {
		testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1), PendingKey: thief.PublicKeyBase64(), PendingHeight: pendingHeight})
		freeze, err := wallet.Freeze()
		if err != nil {
			t.Fatal(err)
		}
		if freeze.BlockID != want {
			t.Errorf("with a change pending for block %d the freeze names block %d, want %d", pendingHeight, freeze.BlockID, want)
		}
	}
}

// Once the block a freeze names has committed and a verified state does not show the name frozen, the
// page says the freeze did not land and whom to turn to (spec 7.3). Once one shows it frozen, the page
// says the name is frozen instead.
func TestFreezeThatDidNotLandIsReported(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if _, err := wallet.Freeze(); err != nil {
		t.Fatal(err)
	}
	testNode.mutex.Lock()
	testNode.height = 13 // the freeze's block committed, without it
	testNode.mutex.Unlock()
	body := page(t, wallet, "/")
	if !strings.Contains(body, "Your freeze of alice for block 13 did not land") || !strings.Contains(body, "Contact your bank, or another bank.") {
		t.Error("the page does not report the freeze that did not land")
	}
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1), Frozen: true})
	body = page(t, wallet, "/")
	if strings.Contains(body, "did not land") || strings.Contains(body, "was sent") || !strings.Contains(body, "Your name is frozen.") {
		t.Error("once alice is frozen the page still reports the freeze, or does not say alice is frozen")
	}
}

// A freeze names its block from the verified state, as a payment does. A dishonest node that reports a
// far height (a colluding bank's, docs/security.md) cannot make the freeze name a block no verified
// state reaches, which would hide that it did not land.
func TestFreezeNamesItsBlockFromTheVerifiedState(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	verified(t, wallet) // the state of block 9: block 10 is in progress
	testNode.mutex.Lock()
	testNode.reportedHeight = 1_000_000_000 // what the dishonest node's /hdls/status says
	testNode.mutex.Unlock()
	freeze, err := wallet.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if want := 10 + wallet.lookahead; freeze.BlockID != want {
		t.Fatalf("the freeze names block %d, want %d: the verified height in progress plus the lookahead", freeze.BlockID, want)
	}
	testNode.mutex.Lock()
	testNode.reportedHeight = 0
	testNode.height = 500 // long past the freeze's block, and alice is not frozen
	testNode.mutex.Unlock()
	if body := page(t, wallet, "/"); !strings.Contains(body, "did not land") {
		t.Fatal("once a verified state passes the freeze's block without it, the page must say the freeze did not land")
	}
}

// When no state verifies (here: the banks the wallet pinned are gone), the freeze button still freezes
// the last name a verified state gave the wallet's key, signed for the pinned network id (spec 7.2). A
// wallet that never held a name has nothing to freeze.
func TestFreezeWorksWhenNoStateVerifies(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	stranger := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	verified(t, wallet)
	verified(t, stranger)
	testNode.setState([]signing.Signer{testNodeSigner(t)}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if _, err := wallet.verifiedState(); err == nil {
		t.Fatal("premise: a state signed by validators the wallet did not pin verifies")
	}
	if body := page(t, wallet, "/"); !strings.Contains(body, "cannot read a verified state") || !strings.Contains(body, "Freeze alice") {
		t.Fatal("with no verified state the page does not offer to freeze alice")
	}
	if refusal := redirectError(submitForm(wallet, "/freeze", url.Values{"step": {"confirm"}})); refusal != "" {
		t.Fatal(refusal)
	}
	var freeze ledger.NameRequest
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &freeze) != nil {
		t.Fatalf("want one freeze sent, got %d request(s)", len(posts))
	}
	if freeze.Name != "alice" || !ledger.VerifyNameRequest(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), "", freeze) {
		t.Errorf("the freeze sent is of %q, want alice's, signed under the wallet's key for the pinned network", freeze.Name)
	}
	if _, err := stranger.Freeze(); err == nil || len(testNode.posted()) != 1 {
		t.Error("a wallet that never held a name sent a freeze")
	}
}

// Before it signs a payment, the wallet shows the payee's name, as it read what was typed, and the
// payee's bank of record from its verified state, and sends nothing (spec 7.3). Confirmed, it pays that
// name. A name with no account, or a frozen one, is refused at review.
func TestPaymentShowsThePayeesBank(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)},
		ledger.Account{Name: "bakery", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat), Bank: "bank-a"},
		ledger.Account{Name: "bank-a", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)},
		ledger.Account{Name: "closed", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat), Frozen: true})
	review := submitForm(wallet, "/send", url.Values{"to": {" Bakery "}, "amount": {"2"}})
	body := html.UnescapeString(review.Body.String())
	for _, want := range []string{"pay <strong>2</strong> to <strong>bakery</strong>", "bakery's bank of record: <strong>bank-a</strong>", `name="step" value="confirm"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the review does not show %q", want)
		}
	}
	for _, refused := range []string{"nobody", "closed"} {
		if refusal := redirectError(submitForm(wallet, "/send", url.Values{"to": {refused}, "amount": {"2"}})); refusal == "" {
			t.Errorf("a payment to %q passed review", refused)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("reviewing payments sent %d request(s)", len(posts))
	}
	if refusal := redirectError(submitForm(wallet, "/send", url.Values{"step": {"confirm"}, "to": {"bakery"}, "amount": {"2"}})); refusal != "" {
		t.Fatal(refusal)
	}
	var payment ledger.Payment
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &payment) != nil || payment.To != "bakery" {
		t.Fatalf("the confirmed payment did not go to bakery (%d request(s))", len(posts))
	}
}

// Before it signs a payment, the wallet makes the checks a node's intake makes against its verified
// state (ledger.PaymentFitsState, ledger.CreditFitsBalanceCap): the cost floor, the largest payment,
// the payee's cap, the payer's balance and a frozen payer. The review page shows the ledger's reason,
// and a refused payment sends nothing and uses up no block of the payment counter: the payment that
// passes after them names height + lookahead, block 13.
func TestPaymentReviewRunsTheLedgersRules(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	alice := ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(200_000, 1)}
	full := ledger.Account{Name: "full", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat).Set(governanceParameters.MaxAccountBalance)}
	shop := ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)}
	refused := func(to, amount, reason string) {
		t.Helper()
		if refusal := redirectError(submitForm(wallet, "/send", url.Values{"to": {to}, "amount": {amount}})); !strings.Contains(refusal, reason) {
			t.Errorf("the review of %s to %s says %q, want the ledger's reason %q", amount, to, refusal, reason)
		}
		if _, _, err := wallet.Send(to, amount); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("a payment of %s to %s: %v, want a refusal saying %q", amount, to, err, reason)
		}
	}
	testNode.setState(nil, alice, full, shop)
	refused("shop", "0.05", "below the transaction cost")
	refused("shop", "100000.00000001", "above the largest single payment")
	refused("full", "1", "above the largest balance")
	alice.Amount = big.NewRat(5, 1)
	testNode.setState(nil, alice, full, shop)
	refused("shop", "6", "above the payer's balance")
	alice.Frozen = true
	testNode.setState(nil, alice, full, shop)
	refused("shop", "1", `the payer "alice" is frozen`)
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("%d request(s) sent for payments the ledger refuses", len(posts))
	}

	alice.Frozen = false
	testNode.setState(nil, alice, full, shop)
	var payment ledger.Payment
	if _, _, err := wallet.Send("shop", "1"); err != nil {
		t.Fatal(err)
	}
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &payment) != nil || payment.BlockID != 13 {
		t.Errorf("the payment after the refusals names block %d, want 13: a refusal used up a block", payment.BlockID)
	}
}

// Before it signs an account update, the wallet makes the check a node's intake makes against its
// verified state (ledger.UpdateFitsState): a delegate with no account, a frozen delegate, a frozen
// account, and a description that fits the message bound only until the signature is added, are refused
// with the ledger's reason. A refused update sends nothing and uses up no block of the payment counter:
// the update that passes after them names height + lookahead, block 13.
func TestAccountUpdateRunsTheLedgersRules(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	kay := ledger.Account{Name: "kay", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)}
	closed := ledger.Account{Name: "closed", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat), Frozen: true}
	refused := func(description, delegate, reason string) {
		t.Helper()
		if _, err := wallet.UpdateAccount(description, delegate, "", "", ""); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("an update (a %d-byte description, delegate %q): %v, want a refusal saying %q", len(description), delegate, err, reason)
		}
	}
	testNode.setState(nil, kay, closed)
	refused("", "nobody", `no account is named "nobody" (the delegate)`)
	refused("", "closed", `the delegate "closed" is frozen`)
	refused(strings.Repeat("d", 3000), "", "update too large") // 3,000 of the 6,400 bytes, and a 4,412-byte signature
	kay.Frozen = true
	testNode.setState(nil, kay, closed)
	refused("hello", "", `the account "kay" is frozen`)
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("%d request(s) sent for updates the ledger refuses", len(posts))
	}

	kay.Frozen = false
	testNode.setState(nil, kay, closed)
	if update, err := wallet.UpdateAccount("hello", "", "", "", ""); err != nil || update.BlockID != 13 {
		t.Errorf("the update after the refusals names block %d (err %v), want 13: a refusal used up a block", update.BlockID, err)
	}
}

// The wallet lowercases and trims a name a person types, shows the result, and refuses anything else
// (spec 3.2): a payee, a delegate and the name of a ⅔ name action alike, before anything is signed.
func TestWalletRefusesNonCanonicalNames(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet)}, accountOf(walletSigner(t, wallet), "5"),
		ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)})
	checkpoint := verified(t, wallet)
	for _, refused := range []string{"sh op", "sh_op", "-", "-shop", "", wallet.PublicKey(), strings.Repeat("a", 33)} {
		if _, err := wallet.ReviewPayment(checkpoint, refused, "1"); err == nil {
			t.Errorf("a payment to %q passed review", refused)
		}
		if refused != "" && refused != ledger.ClearField {
			if _, err := wallet.UpdateAccount("", refused, "", "", ""); err == nil {
				t.Errorf("a delegation to %q was signed", refused)
			}
		}
		if _, err := wallet.DraftReassignment(checkpoint, nameActionForm{Name: refused, NewAmount: "1"}); err == nil {
			t.Errorf("a name action on %q passed review", refused)
		}
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Fatalf("%d request(s) sent for names that are not names", len(posts))
	}
	if review, err := wallet.ReviewPayment(checkpoint, "  SHOP\n", "1"); err != nil || review.To != "shop" {
		t.Errorf("a payment to \"  SHOP\" reviews as to %q (err %v), want shop", review.To, err)
	}
	if body := html.UnescapeString(submitForm(wallet, "/send", url.Values{"to": {"Shop"}, "amount": {"1"}}).Body.String()); !strings.Contains(body, "to <strong>shop</strong>") {
		t.Error("the review does not show the name as the wallet read it")
	}
	delegated := submitForm(wallet, "/update", url.Values{"delegate": {" Shop "}})
	if refusal := redirectError(delegated); refusal != "" {
		t.Fatal(refusal)
	}
	var update ledger.AccountUpdate
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &update) != nil || update.Delegate != "shop" {
		t.Errorf("the delegation to \" Shop \" names %q, want shop", update.Delegate)
	}
	if location, _ := url.Parse(delegated.Header().Get("Location")); !strings.Contains(location.Query().Get("msg"), "delegating to shop") {
		t.Errorf("the account form does not show the delegate as the wallet read it: %q", location.Query().Get("msg"))
	}
}

// A key a person pastes is taken trimmed, and refused when the network's scheme refuses it, with the
// scheme's reason: a key cut short says how long a key is.
func TestTypedKeyGivesTheSchemesReason(t *testing.T) {
	signer, err := signing.Generate(signing.NetworkScheme(), testNetworkID)
	if err != nil {
		t.Fatal(err)
	}
	key := signer.PublicKeyBase64()
	if typed, err := typedKey(" " + key + "\n"); err != nil || typed != key {
		t.Fatalf("the pasted key was read as %.20q… (%v), want the key", typed, err)
	}
	cut, _ := base64.StdEncoding.DecodeString(key[:len(key)-4])
	reason := signing.NetworkScheme().ValidatePublicKey(cut)
	if _, err := typedKey(key[:len(key)-4]); err == nil || reason == nil || !strings.Contains(err.Error(), reason.Error()) {
		t.Errorf("a key cut short was refused with %v, want the scheme's reason, %v", err, reason)
	}
}

// The account form takes a public address, IP addresses and TLS keys, as well as a description and a
// delegate (spec 7.3, §4; Phase 4 §3.3): a bank publishes its public face, the IP addresses the other
// validators send it their messages at, and the hashes of its web server's keys, each list separated by
// spaces, which replaces the whole list; "-" withdraws any of them. An address or key in another form
// than the ledger's (ledger.CanonicalAddress, ledger.CanonicalIPAddress, ledger.CanonicalTLSKey), here
// with a path, a leading zero or capitals, is refused with the reason, and nothing is sent.
func TestAccountFormTakesAnAddress(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "kay", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	tlsKey, nextTLSKey := strings.Repeat("0123456789abcdef", 4), strings.Repeat("fedcba9876543210", 4)
	for _, refused := range []struct{ field, value, want string }{
		{"publicAddress", "https://bank-a.example/", `the public address "https://bank-a.example/" is not an address`},
		{"ipAddresses", "203.0.113.7 010.0.0.2", `the IP address "010.0.0.2" is not an IP address`},
		{"tlsKeys", strings.ToUpper(tlsKey), `is not a TLS key`},
	} {
		if refusal := redirectError(submitForm(wallet, "/update", url.Values{refused.field: {refused.value}})); !strings.Contains(refusal, refused.want) {
			t.Errorf("the %s %q: %q, want its refusal", refused.field, refused.value, refusal)
		}
	}
	for _, form := range []url.Values{
		{"publicAddress": {"https://bank-a.example"}, "ipAddresses": {" 203.0.113.7  2001:db8::7 "}, "tlsKeys": {tlsKey + " " + nextTLSKey}},
		{"publicAddress": {ledger.ClearField}, "ipAddresses": {ledger.ClearField}, "tlsKeys": {ledger.ClearField}},
	} {
		if refusal := redirectError(submitForm(wallet, "/update", form)); refusal != "" {
			t.Fatal(refusal)
		}
	}
	var published, withdrawn ledger.AccountUpdate
	posts := testNode.posted()
	if len(posts) != 2 || json.Unmarshal(posts[0].body, &published) != nil || json.Unmarshal(posts[1].body, &withdrawn) != nil {
		t.Fatalf("want two account updates sent, got %d request(s)", len(posts))
	}
	if published.PublicAddress != "https://bank-a.example" || !slices.Equal(published.IPAddresses, []string{"203.0.113.7", "2001:db8::7"}) ||
		!slices.Equal(published.TLSKeys, []string{tlsKey, nextTLSKey}) || !ledger.VerifyAccountUpdate(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), published) {
		t.Errorf("the first update publishes %q, %v and %v, want https://bank-a.example, the two IP addresses and the two TLS keys, signed under the wallet's key",
			published.PublicAddress, published.IPAddresses, published.TLSKeys)
	}
	if withdrawn.PublicAddress != ledger.ClearField || !slices.Equal(withdrawn.IPAddresses, []string{ledger.ClearField}) || !slices.Equal(withdrawn.TLSKeys, []string{ledger.ClearField}) {
		t.Errorf("the second update publishes %q, %v and %v, want %q for each", withdrawn.PublicAddress, withdrawn.IPAddresses, withdrawn.TLSKeys, ledger.ClearField)
	}
}

// Only "/" is the main page: any other path the routes do not name, /favicon.ico included, is not
// found, and reads no checkpoint.
func TestUnknownPathIsNotFound(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	for _, path := range []string{"/favicon.ico", "/nowhere"} {
		recorder := httptest.NewRecorder()
		routes(wallet, "127.0.0.1:8090", "").ServeHTTP(recorder, httptest.NewRequest("GET", "http://127.0.0.1:8090"+path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
	testNode.mutex.Lock()
	defer testNode.mutex.Unlock()
	if testNode.checkpointReads != 0 {
		t.Errorf("unknown paths read the checkpoint %d times, want none", testNode.checkpointReads)
	}
}

// A page reads the node's checkpoint once, and shows and acts on that one state (spec 7.2): every page,
// every review and draft step that renders one, and every action that redirects to one.
func TestPagesReadTheCheckpointOnce(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState([]signing.Signer{walletSigner(t, wallet), testNodeSigner(t), testNodeSigner(t)}, accountOf(walletSigner(t, wallet), "5"),
		ledger.Account{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)})
	checkpoint := verified(t, wallet) // the first use pins the network
	registration, _ := json.Marshal(ledger.NameRequest{Kind: ledger.RegistrationKind, Name: "newcomer", Requestor: nameOf(walletSigner(t, wallet)),
		NewKey: testNodeSigner(t).PublicKeyBase64(), Bank: nameOf(walletSigner(t, wallet)), Fee: governanceParameters.NewKeyFee, BlockID: 130})
	nameAction, err := wallet.DraftReassignment(checkpoint, nameActionForm{Name: "shop", NewAmount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	parameterChange, err := wallet.DraftParameters(checkpoint, parameterForm{TransactionCost: "0.2"})
	if err != nil {
		t.Fatal(err)
	}
	signs := func(path string, form url.Values) {
		if recorder := submitForm(wallet, path, form); recorder.Code != http.StatusOK {
			t.Errorf("the sign step of %s did not show what it signed: status %d, %q", path, recorder.Code, redirectError(recorder))
		}
	}
	reads := func(action func()) int {
		testNode.mutex.Lock()
		before := testNode.checkpointReads
		testNode.mutex.Unlock()
		action()
		testNode.mutex.Lock()
		defer testNode.mutex.Unlock()
		return testNode.checkpointReads - before
	}
	for name, action := range map[string]func(){
		"the main page":         func() { page(t, wallet, "/") },
		"the admin page":        func() { page(t, wallet, "/admin") },
		"the requests page":     func() { page(t, wallet, "/requests") },
		"a payment review":      func() { submitForm(wallet, "/send", url.Values{"to": {"shop"}, "amount": {"1"}}) },
		"a payment":             func() { submitForm(wallet, "/send", url.Values{"step": {"confirm"}, "to": {"shop"}, "amount": {"1"}}) },
		"a payment saved":       func() { submitForm(wallet, "/send", url.Values{"step": {"save"}, "to": {"shop"}, "amount": {"1"}}) },
		"an account update":     func() { submitForm(wallet, "/update", url.Values{"description": {"hello"}}) },
		"a freeze confirmation": func() { submitForm(wallet, "/freeze", nil) },
		"a name action draft":   func() { submitForm(wallet, "/admin/reassignment", url.Values{"name": {"shop"}, "amount": {"1"}}) },
		"a parameter draft":     func() { submitForm(wallet, "/admin/parameter", url.Values{"cost": {"0.2"}}) },
		"a request draft": func() {
			submitForm(wallet, "/requests", url.Values{"kind": {"bank-change"}, "bank": {nameOf(testNode.validators[1])}})
		},
		"a request's sign step": func() { signs("/requests", url.Values{"step": {"sign"}, "request": {string(registration)}}) },
		"a name action's sign step": func() {
			signs("/admin/reassignment", url.Values{"step": {"sign"}, "draft": {nameAction.Certificate}})
		},
		"a parameter vote's sign step": func() {
			signs("/admin/parameter", url.Values{"step": {"sign"}, "draft": {parameterChange.Certificate}})
		},
	} {
		if count := reads(action); count != 1 {
			t.Errorf("%s read the checkpoint %d times, want once", name, count)
		}
	}
}
