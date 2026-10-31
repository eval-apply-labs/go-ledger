package node

// Verifier E2E (round 2, group names-ban): drive a real node through its HTTP intake (/hdls/update,
// /hdls/pay) and its event loop / ProposeBlock / ApplyBlock commit path. No internals are called.

import (
	"bytes"
	"encoding/json"
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

func nameOwner(accounts []ledger.Account, name string) string {
	for _, a := range accounts {
		if a.Name == name {
			return a.PublicKey
		}
	}
	return ""
}

func startSingleValidator(t *testing.T, genesis []ledger.Account, val *signing.Signer) (*Node, *httptest.Server) {
	t.Helper()
	key := val.PublicKeyBase64()
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: val,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: key, Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 150 * time.Millisecond,
		Genesis: genesis, MaxMessageBytes: 2000,
	}, NewHTTPTransport(nil))
	srv := httptest.NewServer(NewServer(nd).Handler())
	nd.Start()
	t.Cleanup(func() { nd.Stop(); srv.Close() })
	return nd, srv
}

// H2 end to end: stolen-key rename + thief claim, both via /hdls/update; an honest payer's
// pay-by-name payment admitted (with a signed receipt) BEFORE the hijack is credited to the thief.
func TestVerifyE2EH2RenameReleaseHijack(t *testing.T) {
	sch := signing.Ed25519{}
	val, _ := signing.Generate(sch, "testnet")
	victim, _ := signing.Generate(sch, "testnet")
	thief, _ := signing.Generate(sch, "testnet")
	payer, _ := signing.Generate(sch, "testnet")
	VAL, V, T, P := val.PublicKeyBase64(), victim.PublicKeyBase64(), thief.PublicKeyBase64(), payer.PublicKeyBase64()
	nd, srv := startSingleValidator(t, []ledger.Account{
		{PublicKey: VAL, Amount: rat("1000"), ValidatorNode: VAL},
		{PublicKey: V, Name: "alice", Amount: rat("10"), ValidatorNode: V},
		{PublicKey: T, Amount: rat("1"), ValidatorNode: T},
		{PublicKey: P, Amount: rat("50"), ValidatorNode: P},
	}, val)

	h := nd.Snapshot().Height
	// Honest payer, before the attack: pay "alice" 20 at h+12 (admitted now: "alice" resolves to V).
	pay, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, ToName: "alice", Amount: rat("20"), BlockID: h + 12})
	if code := postJSON(t, srv.URL+"/hdls/pay", pay); code != http.StatusOK {
		t.Fatalf("/hdls/pay refused the honest payment: %d", code)
	}
	// Thief: rename the victim's account with the stolen key, then claim from own account a block later.
	rename, _ := ledger.SignAccountUpdate(victim, ledger.AccountUpdate{From: V, Name: "alice-old", BlockID: h + 6})
	claim, _ := ledger.SignAccountUpdate(thief, ledger.AccountUpdate{From: T, Name: "alice", BlockID: h + 7})
	c1 := postJSON(t, srv.URL+"/hdls/update", rename)
	c2 := postJSON(t, srv.URL+"/hdls/update", claim)
	t.Logf("intake: /hdls/pay=200, /hdls/update rename=%d claim=%d", c1, c2)

	waitFor(t, 20*time.Second, func() bool { return nd.Snapshot().Height > h+12 }, "chain did not pass block h+12")
	accts := nd.Snapshot().Accounts
	t.Logf("committed: owner(alice)=%s (victim=%s thief=%s); victim=%s thief=%s payer=%s",
		nameOwner(accts, "alice")[:8], V[:8], T[:8], balance(accts, V).FloatString(2),
		balance(accts, T).FloatString(2), balance(accts, P).FloatString(2))
	if nameOwner(accts, "alice") == T && balance(accts, T).Cmp(rat("20.9")) == 0 && balance(accts, V).Cmp(rat("10")) == 0 {
		t.Errorf("H2 CONFIRMED end-to-end: name moved victim->thief through intake; receipt-backed payment to 'alice' credited the thief; victim balance untouched")
	}
}

// C02 end to end: alice empties her account to her new wallet (exact balance, admitted by the
// overdraw guard since promised == balance); a watcher's pre-staged claim for the same block lands
// after payments; an earlier receipt-backed payment to "alice" credits the watcher.
func TestVerifyE2EC02SpendToZeroRelease(t *testing.T) {
	sch := signing.Ed25519{}
	val, _ := signing.Generate(sch, "testnet")
	alice, _ := signing.Generate(sch, "testnet")
	fresh, _ := signing.Generate(sch, "testnet")
	watcher, _ := signing.Generate(sch, "testnet")
	payer, _ := signing.Generate(sch, "testnet")
	VAL, A, F, W, P := val.PublicKeyBase64(), alice.PublicKeyBase64(), fresh.PublicKeyBase64(), watcher.PublicKeyBase64(), payer.PublicKeyBase64()
	nd, srv := startSingleValidator(t, []ledger.Account{
		{PublicKey: VAL, Amount: rat("1000"), ValidatorNode: VAL},
		{PublicKey: A, Name: "alice", Amount: rat("10"), ValidatorNode: A},
		{PublicKey: W, Amount: rat("1"), ValidatorNode: W},
		{PublicKey: P, Amount: rat("50"), ValidatorNode: P},
	}, val)
	h := nd.Snapshot().Height
	pay, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, ToName: "alice", Amount: rat("20"), BlockID: h + 10})
	move, _ := ledger.SignPayment(alice, ledger.Payment{PublicKeyFrom: A, PublicKeyTo: F, Amount: rat("10"), BlockID: h + 6})
	claim, _ := ledger.SignAccountUpdate(watcher, ledger.AccountUpdate{From: W, Name: "alice", BlockID: h + 6})
	c0 := postJSON(t, srv.URL+"/hdls/pay", pay)
	c1 := postJSON(t, srv.URL+"/hdls/update", claim) // pre-staged while 'alice' is still taken
	c2 := postJSON(t, srv.URL+"/hdls/pay", move)
	t.Logf("intake: pay-by-name=%d, claim(taken name)=%d, move-exact-balance=%d", c0, c1, c2)
	waitFor(t, 20*time.Second, func() bool { return nd.Snapshot().Height > h+10 }, "chain did not pass h+10")
	accts := nd.Snapshot().Accounts
	t.Logf("committed: owner(alice)=%q watcher=%s fresh=%s", nameOwner(accts, "alice")[:8], balance(accts, W).FloatString(2), balance(accts, F).FloatString(2))
	if nameOwner(accts, "alice") == W && balance(accts, W).Cmp(rat("20.9")) == 0 {
		t.Errorf("C02 CONFIRMED end-to-end: emptying the account released 'alice' to a pre-staged claim; receipt-backed payment credited the watcher")
	}
}
