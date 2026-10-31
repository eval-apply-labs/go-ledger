package node

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func postJSON(t *testing.T, url string, v any) int {
	t.Helper()
	b, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func supply(accts []ledger.Account) *big.Rat {
	s := new(big.Rat)
	for _, a := range accts {
		if a.Amount != nil {
			s.Add(s, a.Amount)
		}
	}
	return s
}

// C14 through the production intake. Old chain: a ⅔ (quorum-1) grant mints a newcomer 100 at
// governance sequence 0, the newcomer spends it all, and alice pays bob 40 at block 5. The
// documented software-upgrade re-genesis then restarts from the captured accounts with the SAME
// network id and keys (heights restart at 1, sequence at 0). Anyone holding the old messages POSTs
// them to /hdls/pay and /hdls/reassignment-vote of the new chain. Control: same flow with a fresh
// network id (no code change) refuses both.
func TestVerifyC14ReGenesisReplayViaHTTP(t *testing.T) {
	scheme := signing.Ed25519{}
	keypair := func() (pub, priv []byte) { p, s, _ := scheme.GenerateKey(); return p, s }
	vPub, vPriv := keypair()
	aPub, aPriv := keypair()
	nPub, nPriv := keypair()
	signer := func(net string, pub, priv []byte) *signing.Signer { return signing.NewSigner(scheme, net, pub, priv) }
	V, Alice, N := signer("village", vPub, vPriv).PublicKeyBase64(), signer("village", aPub, aPriv).PublicKeyBase64(), signer("village", nPub, nPriv).PublicKeyBase64()
	bob, _ := signing.Generate(scheme, "village")
	Bob := bob.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: V, Power: 1}})

	startChain := func(net string, genesis []ledger.Account, interval time.Duration) (*Node, *httptest.Server) {
		nd := New(Config{
			NetworkID: net, Scheme: scheme, Signer: signer(net, vPub, vPriv), Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: interval, Genesis: genesis,
		}, NewHTTPTransport(nil))
		srv := httptest.NewServer(NewServer(nd).Handler())
		nd.Start()
		return nd, srv
	}

	// ---- old chain ("village") ----
	oldGenesis := []ledger.Account{
		{PublicKey: V, Amount: rat("1000"), ValidatorNode: V},
		{PublicKey: Alice, Name: "alice", Amount: rat("100"), ValidatorNode: V},
	}
	old, oldSrv := startChain("village", oldGenesis, 200*time.Millisecond)
	vote, _ := ledger.SignReassignmentVote(signer("village", vPub, vPriv), N, new(big.Rat), rat("100"), 0)
	grant := ledger.Reassignment{Account: N, PreviousAmount: new(big.Rat), NewAmount: rat("100"), Sequence: 0, Votes: []ledger.ReassignmentVote{vote}}
	if c := postJSON(t, oldSrv.URL+"/hdls/reassignment-vote", grant); c != http.StatusAccepted {
		t.Fatalf("old grant post: %d", c)
	}
	waitFor(t, 5*time.Second, func() bool { return balance(old.Snapshot().Accounts, N).Cmp(rat("100")) == 0 }, "old grant not enacted")
	h := old.Snapshot().Height
	spend, _ := ledger.SignPayment(signer("village", nPub, nPriv), ledger.Payment{PublicKeyFrom: N, PublicKeyTo: Alice, Amount: rat("100"), BlockID: h + 1})
	if c := postJSON(t, oldSrv.URL+"/hdls/pay", spend); c != http.StatusOK {
		t.Fatalf("old spend post: %d", c)
	}
	payBlock := h + 4
	pay, _ := ledger.SignPayment(signer("village", aPub, aPriv), ledger.Payment{PublicKeyFrom: Alice, PublicKeyTo: Bob, Amount: rat("40"), BlockID: payBlock})
	if c := postJSON(t, oldSrv.URL+"/hdls/pay", pay); c != http.StatusOK {
		t.Fatalf("old pay post: %d", c)
	}
	waitFor(t, 8*time.Second, func() bool { return old.Snapshot().Height > payBlock }, "old chain did not pass the payment block")
	captured := old.Snapshot()
	old.Stop()
	oldSrv.Close()
	t.Logf("old chain captured at height %d: alice=%s bob=%s newcomer=%s sequence=%d supply=%s",
		captured.Height, balance(captured.Accounts, Alice).RatString(), balance(captured.Accounts, Bob).RatString(),
		balance(captured.Accounts, N).RatString(), captured.GovernanceSequence, supply(captured.Accounts).RatString())

	replay := func(net string) (codePay, codeGrant int, s Snapshot) {
		nd, srv := startChain(net, captured.Accounts, 300*time.Millisecond)
		defer srv.Close()
		defer nd.Stop()
		codePay = postJSON(t, srv.URL+"/hdls/pay", pay)                   // verbatim old payment
		codeGrant = postJSON(t, srv.URL+"/hdls/reassignment-vote", grant) // verbatim old certificate
		waitFor(t, 10*time.Second, func() bool { return nd.Snapshot().Height > payBlock+1 }, "new chain stalled")
		return codePay, codeGrant, nd.Snapshot()
	}

	// ---- re-genesis, SAME network id (the documented software-upgrade path) ----
	cp, cg, s := replay("village")
	t.Logf("same id: /pay=%d /reassignment-vote=%d → height %d alice=%s bob=%s newcomer=%s sequence=%d supply %s → %s",
		cp, cg, s.Height, balance(s.Accounts, Alice).RatString(), balance(s.Accounts, Bob).RatString(),
		balance(s.Accounts, N).RatString(), s.GovernanceSequence, supply(captured.Accounts).RatString(), supply(s.Accounts).RatString())
	if balance(s.Accounts, N).Cmp(rat("100")) == 0 && balance(s.Accounts, Bob).Cmp(balance(captured.Accounts, Bob)) > 0 {
		t.Logf("REPLAY CONFIRMED: old grant re-minted 100 and old payment moved alice's money again")
	}

	// ---- control: re-genesis with a FRESH network id, no code change ----
	cp, cg, s = replay("village-2")
	t.Logf("fresh id: /pay=%d /reassignment-vote=%d → newcomer=%s bob=%s supply %s",
		cp, cg, balance(s.Accounts, N).RatString(), balance(s.Accounts, Bob).RatString(), supply(s.Accounts).RatString())
	if balance(s.Accounts, N).Sign() != 0 || balance(s.Accounts, Bob).Cmp(balance(captured.Accounts, Bob)) != 0 {
		t.Fatalf("fresh network id did NOT stop the replay")
	}
}
