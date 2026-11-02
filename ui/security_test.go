package main

import (
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The CSRF guard: the wallet listens on localhost and auto-signs with the local key, so a page the
// user merely visits must not be able to POST a payment or governance vote. Cross-site browser
// requests are blocked, and so is every request addressed to a host name that is not the wallet's
// own (a DNS-rebinding page) and every POST that does not say where it comes from.
func TestCSRFGuard(t *testing.T) {
	const listen = "127.0.0.1:8090"
	cases := []struct {
		name        string
		method      string
		host        string
		headers     map[string]string
		wantBlocked bool
	}{
		{"cross-site fetch", "POST", listen, map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"same-site (another local port)", "POST", listen, map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"foreign Origin, no Sec-Fetch-Site", "POST", listen, map[string]string{"Origin": "http://evil.example"}, true},
		{"foreign Referer, no Origin", "POST", listen, map[string]string{"Referer": "http://evil.example/page"}, true},
		{"no Origin or Referer", "POST", listen, nil, true},
		{"rebound host name with matching Origin", "POST", "rebind.example:8090", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://rebind.example:8090"}, true},
		{"rebound host name, read", "GET", "rebind.example:8090", nil, true},
		{"same-origin form post", "POST", listen, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8090"}, false},
		{"same-origin form post via localhost", "POST", "localhost:8090", map[string]string{"Origin": "http://localhost:8090"}, false},
		{"same-origin Referer, no Origin", "POST", listen, map[string]string{"Referer": "http://127.0.0.1:8090/admin"}, false},
		{"cross-site GET (read-only, harmless)", "GET", listen, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			called := false
			guardedHandler := guard(listen, http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) { called = true }))
			request := httptest.NewRequest(testCase.method, "http://"+testCase.host+"/send", strings.NewReader("to=x&amount=1"))
			for headerName, headerValue := range testCase.headers {
				request.Header.Set(headerName, headerValue)
			}
			recorder := httptest.NewRecorder()
			guardedHandler.ServeHTTP(recorder, request)
			if testCase.wantBlocked {
				if called {
					t.Fatal("handler ran on a request the guard must block")
				}
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
				}
			} else if !called {
				t.Fatalf("handler did not run on an allowed request (status %d)", recorder.Code)
			}
		})
	}
}

// The Host allowlist on a wildcard listen: loopback names and any IP address on the listen port (how
// the operator reaches it over the LAN), but no other host name, since a page can never rebind an
// address. A specific listen address accepts itself and loopback only.
func TestOwnHost(t *testing.T) {
	for _, listen := range []string{":8090", "0.0.0.0:8090", "[::]:8090"} {
		for host, want := range map[string]bool{
			"localhost:8090": true, "127.0.0.1:8090": true, "[::1]:8090": true, "192.168.1.5:8090": true,
			"rebind.example:8090": false, "192.168.1.5:8091": false, "localhost": false,
		} {
			if accepted := ownHost(host, listen); accepted != want {
				t.Errorf("listen %s: ownHost(%q) = %v, want %v", listen, host, accepted, want)
			}
		}
	}
	for host, want := range map[string]bool{
		"192.168.1.5:8090": true, "localhost:8090": true, "127.0.0.1:8090": true,
		"192.168.1.6:8090": false, "rebind.example:8090": false,
	} {
		if accepted := ownHost(host, "192.168.1.5:8090"); accepted != want {
			t.Errorf("listen 192.168.1.5:8090: ownHost(%q) = %v, want %v", host, accepted, want)
		}
	}
}

// Every mutating route — payments AND the admin governance votes — must reject a cross-site POST
// before touching the wallet. The routes carry no wallet here: if the guard ever let the request
// through, the nil dereference would fail the test loudly.
func TestMutatingHandlersAreGuarded(t *testing.T) {
	walletUI := routes(nil, "127.0.0.1:8090")
	for _, path := range []string{"/send", "/update", "/freeze", "/checkpoint", "/requests", "/admin/parameter", "/admin/reassignment"} {
		request := httptest.NewRequest("POST", "http://127.0.0.1:8090"+path, strings.NewReader("a=b"))
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		recorder := httptest.NewRecorder()
		walletUI.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d for a cross-site POST", path, recorder.Code, http.StatusForbidden)
		}
	}
}

// DNS rebinding: a page the user visits re-points its own host name at 127.0.0.1. The browser then
// treats the wallet as that page's origin, so its requests carry the attacker's name as Host, a
// matching Origin and Sec-Fetch-Site: same-origin. The wallet must refuse all of them, the reads
// (key, balance) as well as the signed payment and vote, and nothing may reach the node.
func TestDNSRebindingIsRefused(t *testing.T) {
	node := testNodeSigner(t)
	var paid atomic.Int32
	nodeServer := fakeNode(t, []*signing.Signer{node}, nil, func(payment ledger.Payment) any {
		paid.Add(1)
		receipt, _ := ledger.SignReceipt(node, nameOf(node), payment)
		return receipt
	})
	walletServer := httptest.NewUnstartedServer(nil)
	listen := walletServer.Listener.Addr().String() // what -listen 127.0.0.1:<port> serves
	walletServer.Config.Handler = routes(newTestWallet(t, nodeServer.URL), listen)
	walletServer.Start()
	defer walletServer.Close()
	_, port, _ := net.SplitHostPort(listen)
	rebound := "rebind.attacker.example:" + port // the attacker's name, now resolving to 127.0.0.1
	attacker := "attacker"

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, testCase := range []struct {
		method, path string
		form         url.Values
	}{
		{"GET", "/", nil},
		{"GET", "/admin", nil},
		{"GET", "/requests", nil},
		{"POST", "/send", url.Values{"step": {"confirm"}, "to": {attacker}, "amount": {"999"}}},
		{"POST", "/freeze", url.Values{"step": {"confirm"}}},
		{"POST", "/requests", url.Values{"step": {"sign"}, "request": {"{}"}}},
		{"POST", "/admin/reassignment", url.Values{"name": {attacker}, "amount": {"1000000"}}},
	} {
		request, _ := http.NewRequest(testCase.method, walletServer.URL+testCase.path, strings.NewReader(testCase.form.Encode()))
		request.Host = rebound
		request.Header.Set("Origin", "http://"+rebound)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("rebound %s %s: status %d, want %d", testCase.method, testCase.path, response.StatusCode, http.StatusForbidden)
		}
	}
	if paymentCount := paid.Load(); paymentCount != 0 {
		t.Errorf("the node received %d payment(s) signed for the rebound page", paymentCount)
	}

	response, err := client.Get(walletServer.URL + "/") // the operator's own address still works
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET / on the wallet's own address: status %d, want %d", response.StatusCode, http.StatusOK)
	}
}

// No other site may frame the wallet's pages and overlay their one-click signing buttons.
func TestPagesDenyFraming(t *testing.T) {
	walletUI := routes(newTestWallet(t, "http://127.0.0.1:0"), "127.0.0.1:8090")
	for _, path := range []string{"/", "/requests", "/admin"} {
		recorder := httptest.NewRecorder()
		walletUI.ServeHTTP(recorder, httptest.NewRequest("GET", "http://127.0.0.1:8090"+path, nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("X-Frame-Options") != "DENY" ||
			recorder.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" {
			t.Errorf("GET %s: status %d, X-Frame-Options %q, Content-Security-Policy %q; want 200 and framing denied",
				path, recorder.Code, recorder.Header().Get("X-Frame-Options"), recorder.Header().Get("Content-Security-Policy"))
		}
	}
}

// fakeNode is a minimal node for the wallet to talk to: it reports a height and a state its
// validators signed, holding the accounts, and answers /hdls/pay with whatever receipt makeReceipt
// builds from the submitted payment, usually a ledger.Receipt.
func fakeNode(t *testing.T, validators []*signing.Signer, accounts []ledger.Account, makeReceipt func(payment ledger.Payment) any) *httptest.Server {
	t.Helper()
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hdls/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": 10, "networkId": testNetworkID})
	})
	serveMux.HandleFunc("/hdls/checkpoint", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(signedCheckpoint(validators, accounts))
	})
	serveMux.HandleFunc("/hdls/pay", func(responseWriter http.ResponseWriter, request *http.Request) {
		var payment ledger.Payment
		if err := json.NewDecoder(request.Body).Decode(&payment); err != nil {
			t.Errorf("pay: %v", err)
		}
		_ = json.NewEncoder(responseWriter).Encode(makeReceipt(payment))
	})
	server := httptest.NewServer(serveMux)
	t.Cleanup(server.Close)
	return server
}

func newTestWallet(t *testing.T, nodeURL string) *Wallet {
	t.Helper()
	wallet, err := LoadOrCreateWallet(filepath.Join(t.TempDir(), "wallet.json"), "testnet", nodeURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	return wallet
}

// testNetworkID is the network id the fake nodes serve and their validators sign for: a network named
// "testnet", the name the test wallets are started with.
var testNetworkID = ledger.NetworkID("testnet", [32]byte{1})

// walletSigner signs with the wallet's key for the test network, as a validator of a fake node.
func walletSigner(t *testing.T, wallet *Wallet) *signing.Signer {
	t.Helper()
	signer, err := signing.NewSigner(signing.NetworkScheme(), testNetworkID, wallet.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func testNodeSigner(t *testing.T) *signing.Signer {
	t.Helper()
	signer, err := signing.Generate(signing.NetworkScheme(), testNetworkID)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// payThroughFakeNode has a test wallet, named "alice" on a fake node whose validators are given, pay
// the shop 1, the node answering with makeReceipt's receipt, and reports whether the wallet verified
// that receipt.
func payThroughFakeNode(t *testing.T, validators []*signing.Signer, makeReceipt func(payment ledger.Payment) any) bool {
	t.Helper()
	wallet := newTestWallet(t, "")
	accounts := []ledger.Account{
		{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(100, 1)},
		{Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)},
	}
	wallet.nodeURL = fakeNode(t, validators, accounts, makeReceipt).URL
	_, verified, err := wallet.Send("shop", "1")
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

// The happy path the manual describes: a current validator signs a receipt over THIS payment, under
// its name, and the wallet marks it verified.
func TestReceiptFromValidatorVerifies(t *testing.T) {
	node := testNodeSigner(t)
	verified := payThroughFakeNode(t, []*signing.Signer{node}, func(payment ledger.Payment) any {
		receipt, err := ledger.SignReceipt(node, nameOf(node), payment)
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	})
	if !verified {
		t.Fatal("a current validator's receipt over this payment must verify")
	}
}

// A receipt from a node that is NOT a pinned validator must not be marked verified: a dishonest
// non-validator can self-sign a receipt, and "verified ✓" would lend it the network's authority. Nor
// may it borrow a validator's name: a receipt is checked under the key pinned for the name it gives.
func TestReceiptFromNonValidatorNotVerified(t *testing.T) {
	node := testNodeSigner(t)
	someoneElse := testNodeSigner(t)
	for _, claimedName := range []string{nameOf(node), nameOf(someoneElse)} {
		verified := payThroughFakeNode(t, []*signing.Signer{someoneElse}, func(payment ledger.Payment) any {
			receipt, err := ledger.SignReceipt(node, claimedName, payment) // validly signed — but the signer is no validator
			if err != nil {
				t.Fatal(err)
			}
			return receipt
		})
		if verified {
			t.Fatalf("a non-validator's receipt, naming %s, must not be marked verified", claimedName)
		}
	}
}

// A receipt acknowledging a DIFFERENT payment must not verify, even from a real validator with a
// valid signature: the receipt must be the node's promise about THIS payment.
func TestReceiptOverDifferentPaymentNotVerified(t *testing.T) {
	node := testNodeSigner(t)
	verified := payThroughFakeNode(t, []*signing.Signer{node}, func(payment ledger.Payment) any {
		other := payment
		other.Comment = "an unrelated payment" // different content → different PaymentID
		receipt, err := ledger.SignReceipt(node, nameOf(node), other)
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	})
	if verified {
		t.Fatal("a receipt whose PaymentID names another payment must not be marked verified")
	}
}

// A receipt with a bad signature must not verify, whoever it claims to be from.
func TestReceiptWithBadSignatureNotVerified(t *testing.T) {
	node := testNodeSigner(t)
	verified := payThroughFakeNode(t, []*signing.Signer{node}, func(payment ledger.Payment) any {
		receipt, err := ledger.SignReceipt(node, nameOf(node), payment)
		if err != nil {
			t.Fatal(err)
		}
		receipt.BlockID++ // signed fields no longer match the signature
		return receipt
	})
	if verified {
		t.Fatal("a receipt whose signature does not cover its fields must not be marked verified")
	}
}

// The wallet reads a receipt as it reads any answer of its node, which it does not trust (UI-4): at
// most maxReceiptBytes. It used to read a receipt whole, bounded only by the client's 5-second timeout,
// so a node could make it hold as much as it could send in that time. A receipt past the bound, here a
// validator's real one padded with an unknown field, gets no ✓; the same receipt unpadded does.
func TestReceiptIsReadUpToItsBound(t *testing.T) {
	node := testNodeSigner(t)
	for padding, wantVerified := range map[int]bool{0: true, int(maxReceiptBytes): false} {
		verified := payThroughFakeNode(t, []*signing.Signer{node}, func(payment ledger.Payment) any {
			receipt, err := ledger.SignReceipt(node, nameOf(node), payment)
			if err != nil {
				t.Error(err)
			}
			return struct {
				ledger.Receipt
				Padding string
			}{receipt, strings.Repeat("x", padding)}
		})
		if verified != wantVerified {
			t.Errorf("a receipt padded with %d bytes: verified %v, want %v", padding, verified, wantVerified)
		}
	}
}
