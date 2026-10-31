package main

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func signingScheme() signing.Scheme { return signing.Ed25519{} }

type ledgerAccount struct {
	Name, PublicKey, Amount string
}

func httpNewServeMuxReassign(t *testing.T, rec *recordingNode, node interface {
	PublicKeyBase64() string
}, accounts func() []ledgerAccount, validators func() []string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/hdls/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"height": 10})
	})
	mux.HandleFunc("/hdls/governance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"validators": validators(), "transactionCost": "1/10", "stepTimeoutMs": 3000, "governanceSequence": 0,
		})
	})
	mux.HandleFunc("/hdls/accounts", func(w http.ResponseWriter, r *http.Request) {
		out := []ledger.Account{}
		for _, a := range accounts() {
			amt, _ := new(big.Rat).SetString(a.Amount)
			out = append(out, ledger.Account{Name: a.Name, PublicKey: a.PublicKey, Amount: amt})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/hdls/reassignment-vote", func(w http.ResponseWriter, r *http.Request) {
		var ra ledger.Reassignment
		_ = json.NewDecoder(r.Body).Decode(&ra)
		rec.mu.Lock()
		rec.reassignments = append(rec.reassignments, ra)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}

func httptestNewServer(t *testing.T, mux *http.ServeMux) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
