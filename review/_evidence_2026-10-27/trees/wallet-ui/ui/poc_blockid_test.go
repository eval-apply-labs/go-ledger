package main

// PoC: the persisted same-block guard (lastPaymentBlockID, wallet.go:168-178) trusts the node's
// reported height without any bound and is not scoped to a network/genesis. One out-of-range height
// (a dishonest or buggy reference node, or simply the previous chain before a documented re-genesis,
// which restarts at height 1) is persisted and then dominates max(height+lookahead, last+1) forever:
// every later payment names a far-future block, even against an honest node and across restarts.
// Asserts the SECURE behaviour (payments name a block near the honest tip), so it FAILS today.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestPoCPersistedBlockIDCounterUnbounded(t *testing.T) {
	var reported atomic.Int64
	reported.Store(1 << 40) // one bad /hdls/status answer (or the old chain's height before a re-genesis)
	mux := http.NewServeMux()
	mux.HandleFunc("/hdls/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"height": reported.Load()})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	w1, err := LoadOrCreateWallet(keyfile, "testnet", srv.URL, 3)
	if err != nil {
		t.Fatal(err)
	}
	_ = w1.nextPaymentBlockID() // the payment signed while the bad height was reported

	reported.Store(10)                                            // honest tip from now on
	w2, err := LoadOrCreateWallet(keyfile, "testnet", srv.URL, 3) // restart (or a different -node / -network)
	if err != nil {
		t.Fatal(err)
	}
	if id := w2.nextPaymentBlockID(); id > 10+1000 {
		t.Errorf("with the honest tip at height 10 the next payment names block %d: the bad height was persisted and is never forgotten", id)
	}
}
