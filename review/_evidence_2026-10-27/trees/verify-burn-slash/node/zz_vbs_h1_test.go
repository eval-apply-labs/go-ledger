package node

// H1 verification: an HONEST validator that restarts (process crash / reboot) while its height is
// still open re-enters that height at round 0 with no memory of what it signed, and can sign a
// conflicting vote at the same (height, round, type) — a valid, unforgeable equivocation proof
// against itself. Everything below runs the real stack: real Nodes, real event loops, real snapshot
// file; the "crash" is: drop the node's I/O right after one exact outgoing message, Stop() it, and
// restart it with New(sameConfig) exactly as cmd/server does after a reboot.

import (
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type h1net struct {
	t       *testing.T
	bus     *vbus
	signers map[string]*signing.Signer
	vs      *consensus.ValidatorSet
	genesis []ledger.Account
	payer   *signing.Signer
	cfgs    map[string]Config
	nodes   map[string]*Node
	down    map[string]*atomic.Bool
}

func newH1Net(t *testing.T, powers []int64) (*h1net, []string) {
	t.Helper()
	n := &h1net{t: t, bus: newVBus(), signers: map[string]*signing.Signer{}, cfgs: map[string]Config{}, nodes: map[string]*Node{}, down: map[string]*atomic.Bool{}}
	var keys []string
	var vals []consensus.Validator
	for _, p := range powers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		k := s.PublicKeyBase64()
		n.signers[k] = s
		keys = append(keys, k)
		vals = append(vals, consensus.Validator{PublicKey: k, Power: p})
		n.genesis = append(n.genesis, ledger.Account{PublicKey: k, Amount: rat("100"), ValidatorNode: k})
		n.down[k] = &atomic.Bool{}
	}
	n.payer, _ = signing.Generate(signing.Ed25519{}, "testnet")
	n.genesis = append(n.genesis, ledger.Account{PublicKey: n.payer.PublicKeyBase64(), Amount: rat("1000"), ValidatorNode: keys[0]})
	n.vs = consensus.NewValidatorSet(vals)
	dir := t.TempDir()
	for i, k := range keys {
		n.cfgs[k] = Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: n.signers[k], Validators: n.vs,
			TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond,
			Genesis: n.genesis, SnapshotPath: filepath.Join(dir, "snap"+string(rune('a'+i))+".json"),
		}
	}
	return n, keys
}

// firstProposerHeight is the first height >= from at which `key` proposes round 0.
func (n *h1net) firstProposerHeight(key string, from int64) int64 {
	for h := from; ; h++ {
		if n.vs.ProposerAt(h, 0) == key {
			return h
		}
	}
}

func (n *h1net) firstNonProposerHeight(key string, from int64) int64 {
	for h := from; ; h++ {
		if n.vs.ProposerAt(h, 0) != key {
			return h
		}
	}
}

func (n *h1net) payment(blockID int64, comment string) ledger.Payment {
	p, err := ledger.SignPayment(n.payer, ledger.Payment{PublicKeyFrom: n.payer.PublicKeyBase64(), PublicKeyTo: "merchant-" + comment, Amount: rat("5"), Comment: comment, BlockID: blockID})
	if err != nil {
		n.t.Fatal(err)
	}
	return p
}

// prevoteAt reports whether the vote is key's own vote of the given type at (h, r).
func isVote(m Message, key string, h int64, r int32, typ consensus.VoteType) bool {
	return m.Type == MsgVote && m.Vote != nil && m.Vote.Validator == key && m.Vote.Height == h && m.Vote.Round == r && m.Vote.Type == typ
}

func conflictingPair(votes []consensus.Vote, r int32, typ consensus.VoteType) (consensus.Evidence, bool) {
	for i := range votes {
		for j := i + 1; j < len(votes); j++ {
			a, b := votes[i], votes[j]
			if a.Round == r && b.Round == r && a.Type == typ && b.Type == typ && a.BlockDigest != b.BlockDigest {
				return consensus.Evidence{Validator: a.Validator, A: a, B: b}, true
			}
		}
	}
	return consensus.Evidence{}, false
}

// H1-A: the round-0 PROPOSER restarts mid-height. Deterministic, no timing race: on restart it
// rebuilds its candidate from an (empty) mempool, re-proposes a different block at (H,0) and
// prevotes it immediately. X holds 2 of 4 power (the docs' "concentrate stake on the machines you
// can keep awake" shape), so A+B alone (2) lack quorum (3) and height H stays open until X returns.
func TestVBSH1ProposerRestartSelfEquivocatesAndHalts(t *testing.T) {
	n, keys := newH1Net(t, []int64{2, 1, 1})
	X, A, B := keys[0], keys[1], keys[2]
	H := n.firstProposerHeight(X, 6)
	t.Logf("validator X power 2/4 (quorum %d); X proposes round 0 at height H=%d", n.vs.Quorum(), H)

	xT := newCrashTransport(n.bus, func(m Message) bool { return isVote(m, X, H, 0, consensus.Prevote) })
	n.bus.setFilter(func(from, to string, m Message) bool { return !n.down[to].Load() && !n.down[from].Load() })
	for _, k := range keys {
		var tr Transport = n.bus
		if k == X {
			tr = xT
		}
		nd := New(n.cfgs[k], tr)
		n.nodes[k] = nd
		n.bus.setNode(k, nd)
	}
	// Ordinary traffic: one payment naming block H, so X's block at H is not the empty block.
	n.nodes[X].SubmitPayment(n.payment(H, "h1a"))
	for _, k := range keys {
		n.nodes[k].Start()
	}
	stopped := map[*Node]bool{}
	stop := func(nd *Node) {
		if !stopped[nd] {
			stopped[nd] = true
			nd.Stop()
		}
	}
	defer func() {
		for _, k := range keys {
			stop(n.nodes[k])
		}
	}()

	select {
	case <-xT.fired:
	case <-time.After(20 * time.Second * testTimeSlack):
		t.Fatal("X never prevoted at (H,0)")
	}
	n.down[X].Store(true) // power cut: nothing in or out
	stop(n.nodes[X])
	t.Logf("X crashed right after its (H=%d, r=0) prevote; A at height %d, B at height %d", H, n.nodes[A].Snapshot().Height, n.nodes[B].Snapshot().Height)
	time.Sleep(300 * time.Millisecond)

	// Ordinary reboot: same config, same snapshot file.
	x2 := New(n.cfgs[X], n.bus)
	if x2.height != H {
		t.Fatalf("restart loaded height %d, want %d (snapshot of H-1)", x2.height, H)
	}
	n.nodes[X] = x2
	n.bus.setNode(X, x2)
	n.down[X].Store(false)
	x2.Start()

	waitFor(t, 5*time.Second, func() bool {
		_, ok := conflictingPair(n.bus.votesBy(X, H), 0, consensus.Prevote)
		return ok
	}, "no conflicting (H,0) prevote by X observed")
	ev, _ := conflictingPair(n.bus.votesBy(X, H), 0, consensus.Prevote)
	t.Logf("X signed TWO different prevotes at (H=%d, r=0): %x... vs %x...; Evidence.Valid=%v",
		H, ev.A.BlockDigest[:4], ev.B.BlockDigest[:4], ev.Valid(signing.Ed25519{}, "testnet"))
	if !ev.Valid(signing.Ed25519{}, "testnet") {
		t.Fatal("expected a valid equivocation proof")
	}

	// Liveness: X's power is un-counted at round 0 everywhere, A and B are locked on X's ORIGINAL
	// block from round 0, and the round-0 proof-of-lock no longer reaches quorum for anyone (X's
	// prevote is un-counted), so X can never re-accept it: a permanent livelock at height H.
	time.Sleep(20 * time.Second)
	hA, hB, hX := n.nodes[A].Snapshot().Height, n.nodes[B].Snapshot().Height, x2.Snapshot().Height
	t.Logf("after 20s (~50 base timeouts): heights A=%d B=%d X=%d (H=%d)", hA, hB, hX, H)
	for _, k := range keys {
		stop(n.nodes[k])
	}
	time.Sleep(200 * time.Millisecond)
	for _, k := range []string{A, B} {
		nd := n.nodes[k]
		found := false
		for _, e := range nd.driver.Evidence() {
			if e.Validator == X && e.Valid(signing.Ed25519{}, "testnet") {
				found = true
			}
		}
		t.Logf("peer %s.. driver holds valid evidence against X: %v; round=%d step=%d", k[:6], found, nd.driver.Round(), nd.driver.Step())
	}
	t.Logf("restarted X: round=%d step=%d", x2.driver.Round(), x2.driver.Step())
	if hA == H && hB == H && hX == H {
		t.Errorf("HALT: an honest reboot of X at an open height produced self-equivocation and the network is stuck at height %d", H)
	}
}

// H1-B: four EQUAL validators, one laptop (C) asleep — the docs' own threat model. X (the round-0
// proposer at H) reboots mid-height. A+B alone (2) lack quorum (3) until C wakes. When C wakes
// (syncs via bootstrap like a real rejoin), either the height decides and the honest X is SLASHED by
// its own restart (account destroyed today; key burned under the owner's planned rule), or C picks
// X's re-proposal and the network deadlocks on A/B's lock.
func TestVBSH1FourEqualOneAsleepRestart(t *testing.T) {
	n, keys := newH1Net(t, []int64{1, 1, 1, 1})
	X, A, B, C := keys[0], keys[1], keys[2], keys[3]
	H := n.firstProposerHeight(X, 6)
	t.Logf("4 equal validators (quorum %d), C asleep; X proposes round 0 at H=%d", n.vs.Quorum(), H)

	xT := newCrashTransport(n.bus, func(m Message) bool { return isVote(m, X, H, 0, consensus.Prevote) })
	n.bus.setFilter(func(from, to string, m Message) bool { return !n.down[to].Load() && !n.down[from].Load() })
	for _, k := range []string{X, A, B} {
		var tr Transport = n.bus
		if k == X {
			tr = xT
		}
		nd := New(n.cfgs[k], tr)
		n.nodes[k] = nd
		n.bus.setNode(k, nd)
	}
	srvA := httptest.NewServer(NewServer(n.nodes[A]).Handler())
	defer srvA.Close()
	n.nodes[X].SubmitPayment(n.payment(H, "h1b"))
	for _, k := range []string{X, A, B} {
		n.nodes[k].Start()
	}
	stopped := map[*Node]bool{}
	stop := func(nd *Node) {
		if nd != nil && !stopped[nd] {
			stopped[nd] = true
			nd.Stop()
		}
	}
	defer func() {
		for _, k := range keys {
			stop(n.nodes[k])
		}
	}()

	select {
	case <-xT.fired:
	case <-time.After(20 * time.Second * testTimeSlack):
		t.Fatal("X never prevoted at (H,0)")
	}
	n.down[X].Store(true)
	stop(n.nodes[X])
	time.Sleep(300 * time.Millisecond)
	x2 := New(n.cfgs[X], n.bus)
	n.nodes[X] = x2
	n.bus.setNode(X, x2)
	n.down[X].Store(false)
	x2.Start()
	waitFor(t, 5*time.Second, func() bool {
		_, ok := conflictingPair(n.bus.votesBy(X, H), 0, consensus.Prevote)
		return ok
	}, "no conflicting (H,0) prevote by X observed")
	time.Sleep(2 * time.Second)
	maxRoundBeforeWake := int32(0)
	for _, k := range []string{A, B, X} {
		for _, v := range n.bus.votesBy(k, H) {
			if v.Round > maxRoundBeforeWake {
				maxRoundBeforeWake = v.Round
			}
		}
	}
	t.Logf("before C wakes: heights A=%d B=%d X=%d (H=%d); highest round voted at H so far = %d", n.nodes[A].Snapshot().Height, n.nodes[B].Snapshot().Height, x2.Snapshot().Height, H, maxRoundBeforeWake)

	// C wakes: a fresh process that rejoins via bootstrap (fetches A's verified checkpoint).
	cfgC := n.cfgs[C]
	cfgC.SnapshotPath = ""
	cfgC.Bootstrap = []string{srvA.URL}
	c := New(cfgC, n.bus)
	n.nodes[C] = c
	n.bus.setNode(C, c)
	c.Start()

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		s := n.nodes[A].Snapshot()
		if s.Height > H+2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	sA := n.nodes[A].Snapshot()
	xBal := balance(sA.Accounts, X)
	t.Logf("after C woke: heights A=%d B=%d X=%d C=%d; X's balance on A = %s (was 100)",
		sA.Height, n.nodes[B].Snapshot().Height, x2.Snapshot().Height, c.Snapshot().Height, xBal.RatString())
	switch {
	case sA.Height <= H:
		t.Errorf("DEADLOCK: all four validators online and honest, network stuck at height %d after X's reboot", H)
	case xBal.Sign() == 0:
		t.Errorf("SLASHED: honest X lost its whole balance to evidence produced by its own reboot (and stays in the static set)")
	default:
		t.Logf("height advanced and X not slashed (evidence not carried?)")
	}
}

// H1-C: the NON-proposer case. X prevoted A's block at (H,0) and crashed; A+B are stuck at round 0.
// After the reboot X waits at round 0 for the proposal: if its round-0 propose timeout fires before a
// peer's periodic re-gossip of the proposal arrives, it prevotes nil and equivocates. Race between
// the governed StepTimeoutMs and the re-gossip period (cfg.Timeout); measured over several trials.
func TestVBSH1NonProposerRestartRace(t *testing.T) {
	trials, conflicts := 6, 0
	for trial := 0; trial < trials; trial++ {
		func() {
			n, keys := newH1Net(t, []int64{1, 1, 1, 1})
			X, A, B := keys[0], keys[1], keys[2]
			H := n.firstNonProposerHeight(X, 6)
			for n.vs.ProposerAt(H, 0) == keys[3] { // proposer must be awake (A or B)
				H = n.firstNonProposerHeight(X, H+1)
			}
			xT := newCrashTransport(n.bus, func(m Message) bool { return isVote(m, X, H, 0, consensus.Prevote) })
			n.bus.setFilter(func(from, to string, m Message) bool { return !n.down[to].Load() && !n.down[from].Load() })
			for _, k := range []string{X, A, B} {
				var tr Transport = n.bus
				if k == X {
					tr = xT
				}
				nd := New(n.cfgs[k], tr)
				n.nodes[k] = nd
				n.bus.setNode(k, nd)
			}
			n.nodes[n.vs.ProposerAt(H, 0)].SubmitPayment(n.payment(H, "h1c"))
			for _, k := range []string{X, A, B} {
				n.nodes[k].Start()
			}
			select {
			case <-xT.fired:
			case <-time.After(20 * time.Second * testTimeSlack):
				t.Fatal("X never prevoted at (H,0)")
			}
			n.down[X].Store(true)
			n.nodes[X].Stop()
			time.Sleep(300 * time.Millisecond)
			x2 := New(n.cfgs[X], n.bus)
			n.nodes[X] = x2
			n.bus.setNode(X, x2)
			n.down[X].Store(false)
			x2.Start()
			time.Sleep(3 * time.Second)
			_, conflict := conflictingPair(n.bus.votesBy(X, H), 0, consensus.Prevote)
			if conflict {
				conflicts++
			}
			t.Logf("trial %d: H=%d, X's (H,0) prevotes seen=%d conflicting=%v; heights A=%d B=%d", trial, H, len(n.bus.votesBy(X, H)), conflict, n.nodes[A].Snapshot().Height, n.nodes[B].Snapshot().Height)
			for _, k := range []string{X, A, B} {
				n.nodes[k].Stop()
			}
		}()
	}
	t.Logf("non-proposer restart produced a conflicting (H,0) prevote in %d/%d trials", conflicts, trials)
}

func msgHeight(m Message) int64 {
	switch {
	case m.Type == MsgProposal && m.Proposal != nil:
		return m.Proposal.Height
	case m.Type == MsgVote && m.Vote != nil:
		return m.Vote.Height
	}
	return -1
}

// H1-D (lock amnesia → fork, NO Byzantine validator). X holds 2 of 4 power, so {X,A} and {X,B} are
// each a quorum. Height H: B is cut off (Wi-Fi drop); X proposes D1 (it holds a payment), X and A
// prevote and precommit D1, A decides D1. X reboots before A's precommit reaches it — so X never
// committed H and its lock on D1 lived only in memory. After the reboot the partition flips (A drops
// off, B returns). X restarts H at round 0 with no lock and an empty mempool; X+B decide the empty
// block D2 at height H. Two valid ⅔ certificates for different blocks at one height = a fork.
func TestVBSH1LockAmnesiaForkNoByzantine(t *testing.T) {
	n, keys := newH1Net(t, []int64{2, 1, 1})
	X, A, B := keys[0], keys[1], keys[2]
	H := n.firstProposerHeight(X, 6)
	var phase atomic.Int32
	phase.Store(1)
	xT := newCrashTransport(n.bus, func(m Message) bool {
		return isVote(m, X, H, 0, consensus.Precommit) && m.Vote.BlockDigest != ([32]byte{})
	})
	n.bus.setFilter(func(from, to string, m Message) bool {
		if n.down[to].Load() || n.down[from].Load() {
			return false
		}
		if m.Type == MsgPayment && to == B {
			return false // B never hears of the payment (it was submitted while B was offline)
		}
		h := msgHeight(m)
		switch phase.Load() {
		case 1:
			if (to == B || from == B) && h >= H {
				return false // B is offline for height H
			}
			if to == X && m.Type == MsgVote && m.Vote.Height == H && m.Vote.Type == consensus.Precommit && m.Vote.Validator == A {
				return false // X reboots before A's precommit arrives
			}
		case 2:
			if to == A || from == A {
				return false // A drops off; B is back
			}
		}
		return true
	})
	for _, k := range keys {
		var tr Transport = n.bus
		if k == X {
			tr = xT
		}
		nd := New(n.cfgs[k], tr)
		n.nodes[k] = nd
		n.bus.setNode(k, nd)
	}
	n.nodes[X].SubmitPayment(n.payment(H, "h1d"))

	type rec struct {
		node   string
		commit consensus.Commit
	}
	var recMu sync.Mutex
	commits := map[int64]map[[32]byte]rec{}
	stopPoll := make(chan struct{})
	var cur sync.Map // key -> *Node
	for _, k := range keys {
		cur.Store(k, n.nodes[k])
	}
	go func() {
		for {
			select {
			case <-stopPoll:
				return
			default:
			}
			for _, k := range keys {
				v, _ := cur.Load(k)
				s := v.(*Node).Snapshot()
				if s.LastCommit.Height > 0 {
					recMu.Lock()
					if commits[s.LastCommit.Height] == nil {
						commits[s.LastCommit.Height] = map[[32]byte]rec{}
					}
					if _, ok := commits[s.LastCommit.Height][s.LastCommit.BlockDigest]; !ok {
						commits[s.LastCommit.Height][s.LastCommit.BlockDigest] = rec{node: k, commit: s.LastCommit}
					}
					recMu.Unlock()
				}
			}
			time.Sleep(3 * time.Millisecond)
		}
	}()
	for _, k := range keys {
		n.nodes[k].Start()
	}
	stopped := map[*Node]bool{}
	stop := func(nd *Node) {
		if nd != nil && !stopped[nd] {
			stopped[nd] = true
			nd.Stop()
		}
	}
	defer func() {
		close(stopPoll)
		time.Sleep(20 * time.Millisecond)
		for _, k := range keys {
			v, _ := cur.Load(k)
			stop(v.(*Node))
		}
	}()

	select {
	case <-xT.fired:
	case <-time.After(20 * time.Second * testTimeSlack):
		t.Fatal("X never precommitted at (H,0)")
	}
	n.down[X].Store(true)
	stop(n.nodes[X])
	waitFor(t, 5*time.Second, func() bool { return n.nodes[A].Snapshot().Height > H }, "A did not decide H")
	aCommit := n.nodes[A].Snapshot().LastCommit
	t.Logf("A decided height %d with X's precommit: digest %x...", H, aCommit.BlockDigest[:4])

	phase.Store(2)
	x2 := New(n.cfgs[X], n.bus)
	if x2.height != H {
		t.Fatalf("restart loaded height %d, want %d", x2.height, H)
	}
	n.nodes[X] = x2
	cur.Store(X, x2)
	n.bus.setNode(X, x2)
	n.down[X].Store(false)
	x2.Start()
	dl := time.Now().Add(20 * time.Second)
	for time.Now().Before(dl) && !(n.nodes[B].Snapshot().Height > H+1 && x2.Snapshot().Height > H+1) {
		time.Sleep(20 * time.Millisecond)
	}
	if !(n.nodes[B].Snapshot().Height > H+1 && x2.Snapshot().Height > H+1) {
		bv, xv := n.bus.votesBy(B, H), n.bus.votesBy(X, H)
		t.Logf("NO DECISION: B height=%d X2 height=%d; B votes at H: %d, X votes at H: %d", n.nodes[B].Snapshot().Height, x2.Snapshot().Height, len(bv), len(xv))
		for _, v := range append(bv, xv...) {
			t.Logf("  vote %s.. r=%d type=%d digest=%x", v.Validator[:6], v.Round, v.Type, v.BlockDigest[:4])
		}
		t.FailNow()
	}
	time.Sleep(100 * time.Millisecond)

	recMu.Lock()
	atH := commits[H]
	recMu.Unlock()
	scheme := signing.Ed25519{}
	for d, r := range atH {
		t.Logf("height %d committed digest %x... on node %s.. (round %d, %d precommits) — certificate verifies against the static set: %v",
			H, d[:4], r.node[:6], r.commit.Round, len(r.commit.Votes), r.commit.Verify(scheme, "testnet", n.vs))
	}
	if len(atH) > 1 {
		t.Errorf("FORK at height %d with zero Byzantine validators: one honest ≥⅓-power validator rebooted mid-height and forgot its lock", H)
	}
}
