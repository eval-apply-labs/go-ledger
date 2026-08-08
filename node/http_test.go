package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// End-to-end over real HTTP: four nodes, each behind an httptest server, exchange proposals and
// votes as JSON and must reach consensus and apply a payment. This also exercises JSON
// round-tripping of blocks (big.Rat amounts) and votes ([32]byte digests).
func TestNodesReachConsensusOverHTTP(t *testing.T) {
	const n = 4
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)

	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Name: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}

	transports := make([]*HTTPTransport, n)
	nodes := make([]*Node, n)
	servers := make([]*httptest.Server, n)
	for i := 0; i < n; i++ {
		transports[i] = NewHTTPTransport(nil)
		cfg := Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i],
			Validators: vs, TransactionCost: rat("0.1"), Timeout: 5 * time.Second, Genesis: genesis,
		}
		nodes[i] = New(cfg, transports[i])
		servers[i] = httptest.NewServer(NewServer(nodes[i]).Handler())
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	for i := 0; i < n; i++ {
		var peers []string
		for j := 0; j < n; j++ {
			if j != i {
				peers = append(peers, servers[j].URL)
			}
		}
		transports[i].SetPeers(peers)
	}

	pay, _ := ledger.SignPayment(alice, ledger.Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 1,
	})
	for _, nd := range nodes {
		nd.SubmitPayment(pay)
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()

	deadline := time.Now().Add(6 * time.Second)
	var snaps []Snapshot
	for {
		snaps = snaps[:0]
		allAdvanced := true
		for _, nd := range nodes {
			s := nd.Snapshot()
			snaps = append(snaps, s)
			if s.Height < 2 {
				allAdvanced = false
			}
		}
		if allAdvanced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes did not reach height 2 over HTTP in time; heights=%v", heights(snaps))
		}
		time.Sleep(20 * time.Millisecond)
	}

	want := ledger.StateDigest(snaps[0].Accounts)
	for i, s := range snaps {
		if ledger.StateDigest(s.Accounts) != want {
			t.Fatalf("node %d diverged over HTTP", i)
		}
	}
	if got := balance(snaps[0].Accounts, "bob"); got.Cmp(rat("9.9")) != 0 {
		t.Fatalf("bob = %s, want 9.9 over HTTP", got.FloatString(2))
	}
}

// The decentralized name server: GET /hdls/resolve turns a registered name into the account's
// public key AND its published network address (or the reverse, by key), each answer anchored to
// the committed height/digest so clients can cross-check independent nodes byte-for-byte.
func TestResolveEndpointServesNameAndAddress(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()},
		{PublicKey: alice.PublicKeyBase64(), Name: "alice", Description: "the bakery",
			Amount: rat("50"), ValidatorNode: v.PublicKeyBase64(), Address: "http://10.0.0.7:8080"},
	}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()
	server := httptest.NewServer(NewServer(nd).Handler())
	defer server.Close()

	get := func(query string) (int, ResolveRecord) {
		resp, err := http.Get(server.URL + "/hdls/resolve" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var rec ResolveRecord
		_ = json.NewDecoder(resp.Body).Decode(&rec)
		return resp.StatusCode, rec
	}

	status, rec := get("?name=alice")
	if status != http.StatusOK || rec.PublicKey != alice.PublicKeyBase64() || rec.Address != "http://10.0.0.7:8080" {
		t.Fatalf("resolve by name: status=%d record=%+v", status, rec)
	}
	if rec.StateDigest == "" {
		t.Fatal("the answer must be anchored to a committed state digest")
	}
	if status, rec = get("?key=" + url.QueryEscape(alice.PublicKeyBase64())); status != http.StatusOK || rec.Name != "alice" {
		t.Fatalf("reverse lookup by key: status=%d record=%+v", status, rec)
	}
	if status, _ = get("?name=nobody"); status != http.StatusNotFound {
		t.Fatalf("an unregistered name must 404, got %d", status)
	}
	if status, _ = get(""); status != http.StatusBadRequest {
		t.Fatalf("no selector must 400, got %d", status)
	}
	if status, _ = get("?name=alice&key=x"); status != http.StatusBadRequest {
		t.Fatalf("both selectors must 400, got %d", status)
	}
}

// Regression: a standard base64 public key contains '+' in most cases, and query parsing decodes
// '+' to a space — so a raw (unescaped) key in ?key= used to match nothing and 404. handleResolve
// restores the '+', so the reverse lookup works with a key pasted verbatim.
func TestResolveToleratesRawPlusInKey(t *testing.T) {
	scheme := signing.Ed25519{}
	v, _ := signing.Generate(scheme, "testnet")
	var alice *signing.Signer
	for i := 0; i < 4000 && alice == nil; i++ {
		if cand, _ := signing.Generate(scheme, "testnet"); strings.Contains(cand.PublicKeyBase64(), "+") {
			alice = cand
		}
	}
	if alice == nil {
		t.Skip("no key containing '+' generated")
	}
	nd := New(Config{
		NetworkID: "testnet", Scheme: scheme, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second,
		Genesis: []ledger.Account{
			{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()},
			{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("50"), ValidatorNode: v.PublicKeyBase64()},
		},
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()
	server := httptest.NewServer(NewServer(nd).Handler())
	defer server.Close()

	// The key is placed raw in the query (its '+' NOT percent-encoded) — exactly what a client pasting
	// a key does. Without the fix its '+' arrives as a space and matches no account.
	resp, err := http.Get(server.URL + "/hdls/resolve?key=" + alice.PublicKeyBase64())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rec ResolveRecord
	_ = json.NewDecoder(resp.Body).Decode(&rec)
	if resp.StatusCode != http.StatusOK || rec.Name != "alice" {
		t.Fatalf("a raw key with '+' must resolve: status=%d record=%+v", resp.StatusCode, rec)
	}
}

// Regression: a zero block interval (unset in config, or governed to 0) with a single-validator
// network used to recurse commit→startHeight→decide→commit in one call stack until it overflowed.
// The next height must always be scheduled through the loop, never re-entered synchronously.
func TestZeroBlockIntervalDoesNotRecurse(t *testing.T) {
	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, // MinBlockInterval deliberately unset
		Genesis: []ledger.Account{{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()}},
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()
	waitFor(t, 4*time.Second, func() bool { return nd.Snapshot().Height >= 20 },
		"a solo validator with a zero interval should commit freely (and not crash)")
}
