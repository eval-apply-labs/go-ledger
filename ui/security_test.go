package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// The CSRF guard: the wallet listens on localhost and auto-signs with the local key, so a page the
// user merely visits must not be able to POST a payment or governance vote. Cross-site browser
// requests are blocked; the local operator (no browser headers, e.g. curl) is deliberately allowed.
func TestCSRFGuard(t *testing.T) {
	cases := []struct {
		name        string
		method      string
		headers     map[string]string
		wantBlocked bool
	}{
		{"cross-site fetch", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"same-site (another local port)", "POST", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"foreign Origin, no Sec-Fetch-Site", "POST", map[string]string{"Origin": "http://evil.example"}, true},
		{"same-origin form post", "POST", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8090"}, false},
		{"no browser headers (curl / local operator)", "POST", nil, false},
		{"cross-site GET (read-only, harmless)", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := guard(func(w http.ResponseWriter, r *http.Request) { called = true })
			req := httptest.NewRequest(tc.method, "http://127.0.0.1:8090/send", strings.NewReader("to=x&amount=1"))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h(rec, req)
			if tc.wantBlocked {
				if called {
					t.Fatal("handler ran on a request the guard must block")
				}
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
				}
			} else if !called {
				t.Fatalf("handler did not run on an allowed request (status %d)", rec.Code)
			}
		})
	}
}

// Every mutating handler — payments AND the admin governance votes — must reject a cross-site POST
// before touching the wallet. The handler carries no wallet here: if the guard ever let the request
// through, the nil dereference would fail the test loudly.
func TestMutatingHandlersAreGuarded(t *testing.T) {
	h := &handler{}
	routes := map[string]http.HandlerFunc{
		"/send":               h.send,
		"/register":           h.register,
		"/admin/parameter":    h.proposeParameter,
		"/admin/reassignment": h.voteReassignment,
	}
	for path, route := range routes {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8090"+path, strings.NewReader("a=b"))
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		guard(route)(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d for a cross-site POST", path, rec.Code, http.StatusForbidden)
		}
	}
}

// fakeNode is a minimal node for the wallet to talk to: it reports a height, a validator set, and
// answers /hdls/pay with whatever receipt makeReceipt builds from the submitted payment.
func fakeNode(t *testing.T, validators []string, makeReceipt func(p ledger.Payment) ledger.Receipt) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/hdls/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"height": 10})
	})
	mux.HandleFunc("/hdls/governance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"validators": validators})
	})
	mux.HandleFunc("/hdls/pay", func(w http.ResponseWriter, r *http.Request) {
		var p ledger.Payment
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("pay: %v", err)
		}
		_ = json.NewEncoder(w).Encode(makeReceipt(p))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestWallet(t *testing.T, nodeURL string) *Wallet {
	t.Helper()
	w, err := LoadOrCreateWallet(filepath.Join(t.TempDir(), "wallet.json"), "testnet", nodeURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func testNodeSigner(t *testing.T) *signing.Signer {
	t.Helper()
	s, err := signing.Generate(signing.Ed25519{}, "testnet")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The happy path the manual describes: a current validator signs a receipt over THIS payment, and
// the wallet marks it verified.
func TestReceiptFromValidatorVerifies(t *testing.T) {
	node := testNodeSigner(t)
	srv := fakeNode(t, []string{node.PublicKeyBase64()}, func(p ledger.Payment) ledger.Receipt {
		r, err := ledger.SignReceipt(node, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	})
	w := newTestWallet(t, srv.URL)
	_, verified, err := w.Send(testNodeSigner(t).PublicKeyBase64(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("a current validator's receipt over this payment must verify")
	}
}

// A receipt from a node that is NOT in the current validator set must not be marked verified: a
// dishonest non-validator can self-sign a receipt, and "verified ✓" would lend it the network's
// authority.
func TestReceiptFromNonValidatorNotVerified(t *testing.T) {
	node := testNodeSigner(t)
	someoneElse := testNodeSigner(t)
	srv := fakeNode(t, []string{someoneElse.PublicKeyBase64()}, func(p ledger.Payment) ledger.Receipt {
		r, err := ledger.SignReceipt(node, p) // validly signed — but the signer is no validator
		if err != nil {
			t.Fatal(err)
		}
		return r
	})
	w := newTestWallet(t, srv.URL)
	_, verified, err := w.Send(someoneElse.PublicKeyBase64(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		t.Fatal("a non-validator's receipt must not be marked verified")
	}
}

// A receipt acknowledging a DIFFERENT payment must not verify, even from a real validator with a
// valid signature: the receipt must be the node's promise about THIS payment.
func TestReceiptOverDifferentPaymentNotVerified(t *testing.T) {
	node := testNodeSigner(t)
	srv := fakeNode(t, []string{node.PublicKeyBase64()}, func(p ledger.Payment) ledger.Receipt {
		other := p
		other.Comment = "an unrelated payment" // different content → different PaymentID
		r, err := ledger.SignReceipt(node, other)
		if err != nil {
			t.Fatal(err)
		}
		return r
	})
	w := newTestWallet(t, srv.URL)
	_, verified, err := w.Send(testNodeSigner(t).PublicKeyBase64(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		t.Fatal("a receipt whose PaymentID names another payment must not be marked verified")
	}
}

// A receipt with a bad signature must not verify, whoever it claims to be from.
func TestReceiptWithBadSignatureNotVerified(t *testing.T) {
	node := testNodeSigner(t)
	srv := fakeNode(t, []string{node.PublicKeyBase64()}, func(p ledger.Payment) ledger.Receipt {
		r, err := ledger.SignReceipt(node, p)
		if err != nil {
			t.Fatal(err)
		}
		r.BlockID++ // signed fields no longer match the signature
		return r
	})
	w := newTestWallet(t, srv.URL)
	_, verified, err := w.Send(testNodeSigner(t).PublicKeyBase64(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		t.Fatal("a receipt whose signature does not cover its fields must not be marked verified")
	}
}
