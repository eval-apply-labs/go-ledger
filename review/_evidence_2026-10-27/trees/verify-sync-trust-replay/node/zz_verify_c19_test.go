package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// serveCheckpoint is a static /hdls/checkpoint endpoint (an honest archive or an attacker).
func serveCheckpoint(cp Checkpoint) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hdls/checkpoint" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, cp)
	}))
}

// C19 / SYNC-1 through the REAL join path: New → Start → syncThenStart → bestCheckpoint →
// fetchCheckpoint → evAdopt → adopt → fastForward. Genesis has four founders (equal power). Two have
// left (A, B: zero balance); C, D are still validators next to newcomers X, Y. The joiner's bootstrap
// list holds an HONEST source (a genuine checkpoint co-signed by C, D, X, Y) and one attacker source
// (holding only the retired A, B keys). The attacker's forged checkpoint claims a higher height, so it
// wins bestCheckpoint's max-height pick, and the joiner never recovers afterwards.
func TestVerifyC19ForgedCheckpointWinsThroughBootstrap(t *testing.T) {
	const net = "village"
	gen := func() *signing.Signer { s, _ := signing.Generate(signing.Ed25519{}, net); return s }
	a, b, c, d, x, y, m := gen(), gen(), gen(), gen(), gen(), gen(), gen()
	A, B, C, D, X, Y, M := a.PublicKeyBase64(), b.PublicKeyBase64(), c.PublicKeyBase64(), d.PublicKeyBase64(), x.PublicKeyBase64(), y.PublicKeyBase64(), m.PublicKeyBase64()
	params := ledger.LedgerParameters{TransactionCost: rat("0.1"), BlockIntervalMs: 20, StepTimeoutMs: 2000}

	genesis := []ledger.Account{
		{PublicKey: A, Amount: rat("1000"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("1000"), ValidatorNode: B},
		{PublicKey: C, Amount: rat("1000"), ValidatorNode: C},
		{PublicKey: D, Amount: rat("1000"), ValidatorNode: D},
	}

	// Honest current state at height 200 (A, B gone); a genuine commit by the live set C, D, X, Y.
	live := []ledger.Account{
		{PublicKey: C, Amount: rat("1000"), ValidatorNode: C},
		{PublicKey: D, Amount: rat("1000"), ValidatorNode: D},
		{PublicKey: X, Amount: rat("1000"), ValidatorNode: X},
		{PublicKey: Y, Amount: rat("1000"), ValidatorNode: Y},
	}
	liveSet := []consensus.Validator{{PublicKey: C, Power: 1000}, {PublicKey: D, Power: 1000}, {PublicKey: X, Power: 1000}, {PublicKey: Y, Power: 1000}}
	liveDigest := ledger.LedgerState{Accounts: live, Parameters: params}.Digest()
	honest := Checkpoint{Accounts: live, Parameters: params,
		Commit:     buildCommit(t, 200, liveDigest, liveSet, map[string]*signing.Signer{C: c, D: d, X: x, Y: y}),
		Validators: liveSet}

	// Forged state: the attacker's own key M is the sole validator and holds a fortune; signing set
	// = the retired A, B with token power, self-signed.
	forgedAccounts := []ledger.Account{{PublicKey: M, Name: "attacker", Amount: rat("9000000000"), ValidatorNode: M}}
	forgedSet := []consensus.Validator{{PublicKey: A, Power: 1}, {PublicKey: B, Power: 1}}
	forgedDigest := ledger.LedgerState{Accounts: forgedAccounts, Parameters: params}.Digest()
	forged := Checkpoint{Accounts: forgedAccounts, Parameters: params,
		Commit:     buildCommit(t, 1_000_000, forgedDigest, forgedSet, map[string]*signing.Signer{A: a, B: b}),
		Validators: forgedSet}

	honestSrv, attackSrv := serveCheckpoint(honest), serveCheckpoint(forged)
	defer honestSrv.Close()
	defer attackSrv.Close()

	joinerKey := gen()
	joiner := New(Config{
		NetworkID: net, Scheme: signing.Ed25519{}, Signer: joinerKey,
		ValidatorThreshold: rat("100"), TransactionCost: rat("0.1"), Timeout: 2 * time.Second,
		MinBlockInterval: 20 * time.Millisecond, Genesis: genesis,
		Bootstrap: []string{honestSrv.URL, attackSrv.URL},
	}, NewHTTPTransport(nil))
	joiner.Start()
	defer joiner.Stop()

	// Sanity: the honest checkpoint alone IS acceptable to this joiner (C+D = 2000 > 4000/3).
	if !joiner.verifyCheckpoint(honest, genesis) {
		t.Fatal("setup: the honest checkpoint should pass the genesis-anchored check")
	}

	waitFor(t, 3*time.Second, func() bool { return joiner.Snapshot().Height > 1 }, "joiner did not sync at all")
	s := joiner.Snapshot()
	t.Logf("joiner after bootstrap: height=%d attackerBalance=%s honestXBalance=%s",
		s.Height, balance(s.Accounts, M).RatString(), balance(s.Accounts, X).RatString())
	if s.Height != 1_000_001 || balance(s.Accounts, M).Cmp(rat("9000000000")) != 0 {
		t.Fatalf("forged checkpoint not adopted (height %d)", s.Height)
	}
	// Permanent capture: the catch-up loop keeps polling the honest source, but its checkpoint is
	// "behind" (200 < 1e6) and its signers have no power in the adopted (forged) live set.
	time.Sleep(7 * time.Second)
	s = joiner.Snapshot()
	if balance(s.Accounts, X).Sign() != 0 || s.Height < 1_000_001 {
		t.Fatalf("joiner recovered to the honest chain (unexpected): height %d", s.Height)
	}
	t.Logf("after 2+ catch-up ticks the joiner is still on the forged chain at height %d", s.Height)
}

// C19 / SYNC-3: a node cannot reload its OWN snapshot after the dynamic set rotated past genesis.
// Real network on the in-memory bus: the founder A (sole genesis validator) pays X at height 1, X
// becomes the sole validator and commits alone. X persists snapshots (production saveSnapshot).
// Restarting X from the same config discards its own snapshot (loadSnapshot anchors on genesis).
// Restarting BOTH nodes (the documented "simultaneous outage" recovery) then rolls the ledger back:
// A is again the genesis validator and re-commits a NEW height 1 from genesis.
func TestVerifyC19OwnSnapshotRejectedAndRollbackAfterRotation(t *testing.T) {
	const net = "testnet"
	a, _ := signing.Generate(signing.Ed25519{}, net)
	x, _ := signing.Generate(signing.Ed25519{}, net)
	A, X := a.PublicKeyBase64(), x.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: A, Name: "founder", Amount: rat("1000"), ValidatorNode: A}}
	dir := t.TempDir()
	snapA, snapX := filepath.Join(dir, "a.json"), filepath.Join(dir, "x.json")
	mk := func(s *signing.Signer, bs *bus, path string) *Node {
		nd := New(Config{
			NetworkID: net, Scheme: signing.Ed25519{}, Signer: s, ValidatorThreshold: rat("100"),
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 20 * time.Millisecond,
			Genesis: genesis, SnapshotPath: path,
		}, bs)
		bs.register(s.PublicKeyBase64(), nd)
		return nd
	}

	b1 := newBus()
	nA, nX := mk(a, b1, snapA), mk(x, b1, snapX)
	pay, _ := ledger.SignPayment(a, ledger.Payment{PublicKeyFrom: A, PublicKeyTo: X, Amount: rat("990"), BlockID: 1})
	nA.SubmitPayment(pay)
	nX.SubmitPayment(pay)
	nA.Start()
	nX.Start()
	deadline := time.Now().Add(5 * time.Second * testTimeSlack)
	for {
		sa, sx := nA.Snapshot(), nX.Snapshot()
		if sx.Height >= 8 && sa.Height >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rotated network did not advance: A=%d X=%d Avals=%v Xvals=%v A.bal=%s X.bal=%s", sa.Height, sx.Height, sa.Validators, sx.Validators, balance(sa.Accounts, A).RatString(), balance(sx.Accounts, X).RatString())
		}
		time.Sleep(20 * time.Millisecond)
	}
	sx := nX.Snapshot()
	t.Logf("before restart: height=%d X=%s A=%s lastCommit signers=%d validators=%v",
		sx.Height, balance(sx.Accounts, X).RatString(), balance(sx.Accounts, A).RatString(), len(sx.LastCommit.Votes), sx.Validators)
	nA.Stop()
	nX.Stop()
	time.Sleep(100 * time.Millisecond)

	// The persisted file is a genuine checkpoint: it verifies against the live set, not genesis.
	raw, err := os.ReadFile(snapX)
	if err != nil {
		t.Fatal(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	probe := New(Config{NetworkID: net, Scheme: signing.Ed25519{}, Signer: x, ValidatorThreshold: rat("100"), TransactionCost: rat("0.1"), Genesis: genesis}, newBus())
	t.Logf("own snapshot at height %d: verifies vs live set=%v, vs genesis=%v",
		cp.Commit.Height, probe.verifyCheckpoint(cp, cp.Accounts), probe.verifyCheckpoint(cp, genesis))

	// Restart only X: its own snapshot is discarded.
	b2 := newBus()
	rX := mk(x, b2, snapX)
	if rX.height != 1 {
		t.Fatalf("X reloaded its own snapshot (height %d) — SYNC-3 not reproduced", rX.height)
	}
	t.Logf("restarted X came up at height %d with X balance %s (its own snapshot was discarded)", rX.height, balance(rX.accounts, X).RatString())

	// Restart A too (simultaneous outage). Both snapshots are rejected; A (genesis validator) rebuilds.
	rA := mk(a, b2, snapA)
	t.Logf("restarted A came up at height %d", rA.height)
	rA.Start()
	rX.Start()
	defer rA.Stop()
	defer rX.Stop()
	waitFor(t, 5*time.Second, func() bool { return rX.Snapshot().Height >= 3 }, "restarted network did not move")
	s := rX.Snapshot()
	t.Logf("after simultaneous restart: height=%d X=%s A=%s — committed history was rolled back to genesis",
		s.Height, balance(s.Accounts, X).RatString(), balance(s.Accounts, A).RatString())
	if balance(s.Accounts, X).Sign() == 0 && balance(s.Accounts, A).Cmp(rat("1000")) == 0 {
		t.Logf("ROLLBACK: a new height-%d chain from genesis now conflicts with the old committed heights", s.Height-1)
	}
}

// gate drops a node's INBOUND gossip while closed (its own outbound polling still works).
type gate struct {
	mu     sync.Mutex
	closed bool
	h      http.Handler
}

func (g *gate) set(closed bool) { g.mu.Lock(); g.closed = closed; g.mu.Unlock() }
func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed && r.URL.Path != "/hdls/checkpoint" && r.URL.Path != "/hdls/status" {
		http.Error(w, "partitioned", http.StatusServiceUnavailable)
		return
	}
	g.h.ServeHTTP(w, r)
}

// C19 / SYNC-3 for a RUNNING straggler: after rotation, a node that misses a few blocks can never
// catch up through catchUpLoop, because bestCheckpoint filters every candidate against GENESIS
// before the loop's live-anchored catchUp ever sees it. Real HTTP network: A (genesis validator)
// rotates its stake to X at height 1; S is an observer that follows, is partitioned briefly, then
// healed. The control at the end hands the same peer checkpoint straight to the loop (as the
// existing TestStragglerCatchUp does) and it is accepted — proving the genesis filter is the blocker.
func TestVerifyC19StragglerStuckAfterRotation(t *testing.T) {
	const net = "testnet"
	gen := func() *signing.Signer { s, _ := signing.Generate(signing.Ed25519{}, net); return s }
	a, x, s := gen(), gen(), gen()
	A, X := a.PublicKeyBase64(), x.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: A, Name: "founder", Amount: rat("1000"), ValidatorNode: A}}

	signers := []*signing.Signer{a, x, s}
	transports := make([]*HTTPTransport, 3)
	nodes := make([]*Node, 3)
	servers := make([]*httptest.Server, 3)
	gates := make([]*gate, 3)
	for i, sg := range signers {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(Config{
			NetworkID: net, Scheme: signing.Ed25519{}, Signer: sg, ValidatorThreshold: rat("100"),
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 50 * time.Millisecond, Genesis: genesis,
		}, transports[i])
		gates[i] = &gate{h: NewServer(nodes[i]).Handler()}
		servers[i] = httptest.NewServer(gates[i])
	}
	defer func() {
		for _, sv := range servers {
			sv.Close()
		}
	}()
	for i := range nodes {
		var peers []string
		for j := range nodes {
			if j != i {
				peers = append(peers, servers[j].URL)
			}
		}
		transports[i].SetPeers(peers)
	}
	pay, _ := ledger.SignPayment(a, ledger.Payment{PublicKeyFrom: A, PublicKeyTo: X, Amount: rat("990"), BlockID: 1})
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
	nX, nS := nodes[1], nodes[2]
	waitFor(t, 5*time.Second, func() bool { return nS.Snapshot().Height >= 4 }, "observer did not follow the rotated chain")
	if sx := nX.Snapshot(); len(sx.Validators) != 1 || sx.Validators[0].PublicKey != X {
		t.Fatalf("setup: expected X to be the sole live validator, got %v", sx.Validators)
	}

	gates[2].set(true) // partition S (inbound gossip dropped)
	time.Sleep(1 * time.Second)
	gates[2].set(false) // heal
	stuck := nS.Snapshot().Height
	t.Logf("S healed at height %d; X at %d", stuck, nX.Snapshot().Height)
	time.Sleep(8 * time.Second) // > 2 catch-up ticks (3 s each)
	after, xh := nS.Snapshot().Height, nX.Snapshot().Height
	t.Logf("after 8 s: S height=%d, X height=%d", after, xh)
	if after > stuck {
		t.Fatalf("S caught up (%d → %d) — straggler variant not reproduced", stuck, after)
	}

	// bestCheckpoint (genesis filter) vs the loop's live-anchored catchUp on the very same checkpoint.
	_, found := nS.bestCheckpoint([]string{servers[1].URL})
	t.Logf("bestCheckpoint over X's live checkpoint: found=%v", found)
	sx := nX.Snapshot()
	cp := Checkpoint{Accounts: sx.Accounts, Parameters: sx.Parameters, GovernanceSequence: sx.GovernanceSequence, Commit: sx.LastCommit, Validators: sx.Validators}
	nS.enqueue(event{kind: evCatchUp, checkpoint: &cp})
	waitFor(t, 3*time.Second, func() bool { return nS.Snapshot().Height > stuck }, "control: live-anchored catchUp also refused")
	t.Logf("control: handing the SAME checkpoint straight to catchUp moves S to %d — only the genesis filter blocks it", nS.Snapshot().Height)
}
