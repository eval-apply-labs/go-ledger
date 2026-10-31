package main

// PoCs for the admin UI. Each asserts the SECURE behaviour, so it FAILS on the current code.

import (
	"net/http/httptest"
	"testing"
)

// resolveAccountKey (admin.go:156-167) falls back to the raw input when no committed account holds
// the name, and VoteReassignment signs whatever comes back without checking it is a public key or
// showing it to the voter. A name that is not (or no longer) registered — e.g. an account dropped
// when its balance reached zero, which also frees its name — is signed as the literal string.
func TestPoCReassignmentVoteSignsUnresolvedNameLiterally(t *testing.T) {
	var wallet *Wallet
	nodeSrv, rec := newRecordingNode(t, func() []string { return []string{wallet.PublicKey()} })
	wallet = newTestWallet(t, nodeSrv.URL)
	if err := wallet.VoteReassignment("coop", "500"); err != nil {
		t.Logf("vote refused: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ra := range rec.reassignments {
		if !looksLikePublicKey(ra.Account) {
			t.Errorf("signed a reassignment vote for account %q, which is not a public key", ra.Account)
		}
	}
}

// No anti-framing header on the pages that carry one-click signing buttons (admin.html:71,101).
func TestPoCAdminPageHasNoFramingProtection(t *testing.T) {
	var wallet *Wallet
	nodeSrv, _ := newRecordingNode(t, func() []string { return []string{wallet.PublicKey()} })
	wallet = newTestWallet(t, nodeSrv.URL)
	rec := httptest.NewRecorder()
	walletMux(wallet).ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/admin", nil))
	if rec.Header().Get("X-Frame-Options") == "" && rec.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("GET /admin sets neither X-Frame-Options nor a CSP frame-ancestors directive")
	}
}

// Sharper variant: a MALICIOUS reference node remaps the typed name to an attacker-chosen key.
// VoteReassignment calls resolveAccountKey, which trusts whatever /hdls/accounts returns (admin.go
// :156-167), and balanceOf, which trusts the same source — so the node fully controls both the
// target key and the bound PreviousAmount. The validator typed "coop" and clicks vote; the signed
// certificate targets the attacker's key. The payment path was hardened to sign the NAME on-chain
// (findings.md, third pass) precisely to stop this; the reassignment/mint path still resolves
// client-side. If >=2/3 of validators share this node (or it is the only one), the quorum forms on
// the attacker's key and mints to it. Asserts the vote targets what the user typed → FAILS today.
func TestPoCReassignmentVoteTargetsNodeChosenKey(t *testing.T) {
	attacker := testNodeSigner(t).PublicKeyBase64()
	var wallet *Wallet
	rec := &recordingNode{}
	node := testNodeSigner(t)
	mux := httpNewServeMuxReassign(t, rec, node, func() []ledgerAccount {
		return []ledgerAccount{{Name: "coop", PublicKey: attacker, Amount: "7"}} // fabricated mapping
	}, func() []string { return []string{wallet.PublicKey()} })
	srv := httptestNewServer(t, mux)
	wallet = newTestWallet(t, srv.URL)

	if err := wallet.VoteReassignment("coop", "1000000"); err != nil {
		t.Fatalf("vote: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reassignments) == 0 {
		t.Fatal("no reassignment vote submitted")
	}
	for _, ra := range rec.reassignments {
		if ra.Account == attacker {
			t.Errorf("user typed \"coop\" but signed a reassignment vote targeting the node-supplied attacker key %s… (prev bound to node-supplied %s)",
				first(ra.Account, 12), ra.PreviousAmount.RatString())
		}
	}
}
