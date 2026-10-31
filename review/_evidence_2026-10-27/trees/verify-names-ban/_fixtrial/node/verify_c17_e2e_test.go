package node

// Verifier E2E for C17 (nil-vs-zero digest) through the real node: validator X reboots and
// bootstraps from a peer that relays the GENUINE latest checkpoint with one zero Amount rewritten to
// JSON null. X adopts it (Start -> syncThenStart -> bestCheckpoint -> verifyCheckpoint -> adopt),
// rejoins consensus, persists the null to its snapshot on the next commit, and the process dies on
// the first block that pays the zero-balance account. X runs in a CHILD process so the crash is real.
//
// Precondition switch VERIFY_C17_ZERO_VIA:
//   "reassign" (default): the zero comes from a ⅔ reassignment to 0 (the owner's claw-back op).
//   "spend": the zero comes from the named account spending its exact balance — only stays in state
//            if named accounts survive at zero (the decided naming change); on pristine code the
//            account is deleted and the test reports the precondition as unmet.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type c17keys struct{ aPub, aPriv, xPub, xPriv, cPub, cPriv, pPub, pPriv []byte }

func c17signer(pub, priv []byte) *signing.Signer {
	return signing.NewSigner(signing.Ed25519{}, "testnet", pub, priv)
}

func c17genesis(k c17keys) []ledger.Account {
	b := func(p []byte) string { return c17signer(p, nil).PublicKeyBase64() }
	return []ledger.Account{
		{PublicKey: b(k.aPub), Amount: rat("1000"), ValidatorNode: b(k.aPub)},
		{PublicKey: b(k.xPub), Amount: rat("1000"), ValidatorNode: b(k.xPub)},
		{PublicKey: b(k.cPub), Name: "coop", Amount: rat("10"), ValidatorNode: b(k.cPub)},
		{PublicKey: b(k.pPub), Amount: rat("100"), ValidatorNode: b(k.pPub)},
	}
}

func c17config(k c17keys, self *signing.Signer) Config {
	b := func(p []byte) string { return c17signer(p, nil).PublicKeyBase64() }
	return Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: self,
		Validators: consensus.NewValidatorSet([]consensus.Validator{
			{PublicKey: b(k.aPub), Power: 1}, {PublicKey: b(k.xPub), Power: 1}}), // quorum 2: both needed
		TransactionCost: rat("0.1"), Timeout: 600 * time.Millisecond, MinBlockInterval: 100 * time.Millisecond,
		Genesis: c17genesis(k), MaxMessageBytes: 2000,
	}
}

func hexEnv(name string) []byte { b, _ := hex.DecodeString(os.Getenv(name)); return b }

// Child: the rebooted victim validator X.
func TestVerifyC17ChildVictim(t *testing.T) {
	if os.Getenv("VERIFY_C17_CHILD") != "1" {
		t.Skip("child process only")
	}
	k := c17keys{aPub: hexEnv("C17_APUB"), xPub: hexEnv("C17_XPUB"), xPriv: hexEnv("C17_XPRIV"), cPub: hexEnv("C17_CPUB"), pPub: hexEnv("C17_PPUB")}
	cfg := c17config(k, c17signer(k.xPub, k.xPriv))
	if bs := os.Getenv("C17_BOOTSTRAP"); bs != "" {
		cfg.Bootstrap = []string{bs}
	}
	cfg.SnapshotPath = os.Getenv("C17_SNAPSHOT")
	nd := New(cfg, NewHTTPTransport([]string{os.Getenv("C17_PEER")}))
	ln, err := net.Listen("tcp", os.Getenv("C17_LISTEN"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, NewServer(nd).Handler())
	nd.Start()
	if os.Getenv("C17_REPORT_ONLY") == "1" { // restart probe: report the loaded state and exit
		time.Sleep(100 * time.Millisecond)
		for _, a := range nd.Snapshot().Accounts {
			if a.Name == "coop" {
				fmt.Printf("C17-RESTART-STATE coop amount nil=%v height=%d\n", a.Amount == nil, nd.Snapshot().Height)
			}
		}
		time.Sleep(5 * time.Second) // stay up long enough to receive A's re-gossiped proposal
		fmt.Printf("C17-RESTART-SURVIVED height=%d\n", nd.Snapshot().Height)
		return
	}
	time.Sleep(90 * time.Second) // parent kills us, or a panic ends us first
}

func runChild(t *testing.T, env []string) (*exec.Cmd, *strings.Builder) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestVerifyC17ChildVictim$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), env...)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, out
}

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestVerifyC17E2ENilCheckpointCrashesNode(t *testing.T) {
	if os.Getenv("VERIFY_C17_CHILD") == "1" {
		t.Skip()
	}
	gen := func() ([]byte, []byte) { p, s, _ := ed25519.GenerateKey(nil); return p, s }
	var k c17keys
	k.aPub, k.aPriv = gen()
	k.xPub, k.xPriv = gen()
	k.cPub, k.cPriv = gen()
	k.pPub, k.pPriv = gen()
	A, X, C, P := c17signer(k.aPub, k.aPriv), c17signer(k.xPub, k.xPriv), c17signer(k.cPub, k.cPriv), c17signer(k.pPub, k.pPriv)

	xAddr := freePort(t)
	xURL := "http://" + xAddr
	va := New(c17config(k, A), NewHTTPTransport([]string{xURL}))
	sa := httptest.NewServer(NewServer(va).Handler())
	defer sa.Close()
	x1 := New(c17config(k, X), NewHTTPTransport([]string{sa.URL}))
	ln, err := net.Listen("tcp", xAddr)
	if err != nil {
		t.Fatal(err)
	}
	x1srv := &http.Server{Handler: NewServer(x1).Handler()}
	go x1srv.Serve(ln)
	va.Start()
	x1.Start()
	defer va.Stop()

	// Phase 1: put a zero-amount account into committed state.
	via := os.Getenv("VERIFY_C17_ZERO_VIA")
	if via == "spend" {
		h := va.Snapshot().Height
		spend, _ := ledger.SignPayment(C, ledger.Payment{PublicKeyFrom: C.PublicKeyBase64(), PublicKeyTo: P.PublicKeyBase64(), Amount: rat("10"), BlockID: h + 3})
		if err := va.AdmitPayment(spend); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 15*time.Second, func() bool { return va.Snapshot().Height > h+3 }, "spend block not committed")
	} else {
		va1, _ := ledger.SignReassignmentVote(A, C.PublicKeyBase64(), rat("10"), rat("0"), 0)
		vx1, _ := ledger.SignReassignmentVote(X, C.PublicKeyBase64(), rat("10"), rat("0"), 0)
		va.SubmitReassignmentVote(ledger.Reassignment{Account: C.PublicKeyBase64(), PreviousAmount: rat("10"), NewAmount: rat("0"), Sequence: 0,
			Votes: []ledger.ReassignmentVote{va1, vx1}})
		waitFor(t, 15*time.Second, func() bool { return va.Snapshot().GovernanceSequence == 1 }, "reassignment not committed")
	}
	present := false
	for _, a := range va.Snapshot().Accounts {
		if a.Name == "coop" && a.Amount != nil && a.Amount.Sign() == 0 {
			present = true
		}
	}
	t.Logf("precondition via=%q: zero-amount named account in committed state = %v", via, present)
	if !present {
		t.Skip("precondition unmet: no zero-amount account survives in committed state on this code")
	}

	// X reboots (laptop sleep/restart). Network stalls: quorum needs both.
	x1.Stop()
	x1srv.Close()
	time.Sleep(500 * time.Millisecond)
	stalledAt := va.Snapshot().Height

	// Malicious bootstrap peer: relays A's GENUINE checkpoint with coop's Amount rewritten to null.
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(sa.URL + "/hdls/checkpoint")
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		var raw map[string]json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		var accts []map[string]any
		_ = json.Unmarshal(raw["Accounts"], &accts)
		for _, a := range accts {
			if a["Name"] == "coop" {
				a["Amount"] = nil
			}
		}
		raw["Accounts"], _ = json.Marshal(accts)
		_ = json.NewEncoder(w).Encode(raw)
	}))
	defer evil.Close()

	snap := filepath.Join(t.TempDir(), "x.snapshot.json")
	env := []string{"VERIFY_C17_CHILD=1",
		"C17_APUB=" + hex.EncodeToString(k.aPub), "C17_XPUB=" + hex.EncodeToString(k.xPub), "C17_XPRIV=" + hex.EncodeToString(k.xPriv),
		"C17_CPUB=" + hex.EncodeToString(k.cPub), "C17_PPUB=" + hex.EncodeToString(k.pPub),
		"C17_LISTEN=" + xAddr, "C17_PEER=" + sa.URL, "C17_SNAPSHOT=" + snap}
	child, out := runChild(t, append(env, "C17_BOOTSTRAP="+evil.URL))
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	defer child.Process.Kill()

	waitFor(t, 20*time.Second, func() bool { return va.Snapshot().Height > stalledAt+2 }, "X did not rejoin consensus after adopting the checkpoint")
	data, _ := os.ReadFile(snap)
	t.Logf("X rejoined (network resumed from %d to %d); X's own snapshot persisted a null amount: %v",
		stalledAt, va.Snapshot().Height, strings.Contains(string(data), `"Amount":null`))

	// Honest traffic: someone pays the co-op by name.
	h := va.Snapshot().Height
	pay, _ := ledger.SignPayment(P, ledger.Payment{PublicKeyFrom: P.PublicKeyBase64(), ToName: "coop", Amount: rat("5"), BlockID: h + 3})
	if err := va.AdmitPayment(pay); err != nil {
		t.Fatalf("honest payment refused: %v", err)
	}
	select {
	case err := <-done:
		o := out.String()
		crashed := err != nil && strings.Contains(o, "nil pointer dereference") && strings.Contains(o, "ledger.ProcessPayments")
		idx := strings.Index(o, "panic:")
		excerpt := o
		if idx >= 0 {
			excerpt = o[idx:]
			if len(excerpt) > 900 {
				excerpt = excerpt[:900]
			}
		}
		t.Logf("victim process exited: err=%v\n%s", err, excerpt)
		if crashed {
			t.Errorf("C17 CONFIRMED end-to-end: X adopted a digest-identical nil-amount checkpoint from its bootstrap peer and its process died on the next block paying the account")
		}
	case <-time.After(30 * time.Second * testTimeSlack):
		t.Fatal("victim did not crash")
	}
	time.Sleep(time.Second)
	t.Logf("network after the crash: height %d (block %d carried the payment)", va.Snapshot().Height, h+3)

	// Restart X with an HONEST bootstrap peer: its own snapshot (null) loads; A is not ahead (stalled),
	// so nothing overwrites it.
	probe, pout := runChild(t, append(env, "C17_BOOTSTRAP="+sa.URL, "C17_REPORT_ONLY=1"))
	_ = probe.Wait()
	for _, line := range strings.Split(pout.String(), "\n") {
		if strings.Contains(line, "C17-RESTART") || strings.HasPrefix(line, "panic:") ||
			strings.Contains(line, "node.(*Node).") || strings.Contains(line, "ledger.ApplyBlock") || strings.Contains(line, "ledger.ProposeBlock") {
			t.Logf("restart with honest bootstrap: %s", strings.TrimSpace(line))
		}
	}
}
