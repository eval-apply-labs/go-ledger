package node

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// crashSwitch models a power cut right after one specific outgoing message: that message still
// leaves (it was on the wire), everything after it is dropped, in and out.
type crashSwitch struct {
	down      atomic.Bool
	lastWords atomic.Value // string: signature of the last vote that made it out
	fired     chan struct{}
	once      atomic.Bool
	sent      sync.Map // msgKey of every message handed to the NIC before the crash
}

func newCrashSwitch() *crashSwitch { return &crashSwitch{fired: make(chan struct{})} }

func (c *crashSwitch) hook(trigger func(Message) bool) func(Message) {
	return func(m Message) {
		if c.down.Load() {
			return
		}
		if k := msgKey(m); k != "" {
			c.sent.Store(k, true) // already on the wire: it is delivered even if the crash follows
		}
		if trigger(m) {
			c.lastWords.Store(m.Vote.Signature)
			c.down.Store(true)
			if c.once.CompareAndSwap(false, true) {
				close(c.fired)
			}
		}
	}
}

// passes reports whether a message on a link touching this node is still delivered.
func (c *crashSwitch) passesFrom(m Message) bool {
	if !c.down.Load() {
		return true
	}
	if k := msgKey(m); k != "" {
		if _, ok := c.sent.Load(k); ok {
			return true
		}
	}
	lw, _ := c.lastWords.Load().(string)
	return m.Type == MsgVote && m.Vote != nil && lw != "" && m.Vote.Signature == lw
}

// S1 — the finding's scenario, rebuilt independently, WITH bootstrap + catch-up live over HTTP.
// Static X:2 A:1 B:1 (quorum 3). B asleep at H. X proposes D1 (holds a payment); X,A lock+precommit
// D1; A decides. X loses power after its precommit left, before A's precommit reached it. A then
// drops off, B wakes, X reboots (New(sameConfig) reloads its H-1 snapshot) and X+B decide at H.
func TestV3S1SingleHeavyValidatorRestartForks(t *testing.T) { v3S1(t, 0, 0) }

// Variants: D1 proposed by A (X is not the proposer), and/or the restart happens after B's own
// round-0 timeouts fired (so X's post-restart commit lands in a LATER round: pure lock amnesia, no
// same-round double-sign, hence no Evidence can exist at all).
func TestV3S1ProposerA(t *testing.T)           { v3S1(t, 1, 0) }
func TestV3S1CrossRound(t *testing.T)          { v3S1(t, 0, 2*time.Second) }
func TestV3S1ProposerACrossRound(t *testing.T) { v3S1(t, 1, 2*time.Second) }

func v3S1(t *testing.T, proposerIdx int, rebootDelay time.Duration) {
	n := newV3Net(t, []int64{2, 1, 1}, true)
	defer n.shutdown()
	X, A, B := n.keys[0], n.keys[1], n.keys[2]
	H := int64(4)
	for n.vs.ProposerAt(H, 0) != n.keys[proposerIdx] {
		H++
	}
	t.Logf("static set X:2 A:1 B:1, quorum %d; (H=%d, r=0) proposer=%s", n.vs.Quorum(), H, []string{"X", "A", "B"}[proposerIdx])

	var phase atomic.Int32
	phase.Store(1)
	xc := newCrashSwitch()
	n.setAllow(func(from, to string, m Message) bool {
		switch phase.Load() {
		case 1:
			if from == X && !xc.passesFrom(m) || to == X && xc.down.Load() {
				return false
			}
			if (from == B || to == B) && msgH(m) >= H {
				return false // B's laptop is asleep for height H (and missed the payment's gossip)
			}
			if from == A && to == X && m.Type == MsgVote && m.Vote.Height == H && m.Vote.Type == consensus.Precommit {
				return false // A's precommit is still in flight when X loses power
			}
		case 2:
			if from == A || to == A {
				return false // A drops off; B is back
			}
		}
		return true
	})
	n.setHTTP(func(c, s string) bool {
		switch phase.Load() {
		case 1:
			return c != B && s != B && !(xc.down.Load() && (c == X || s == X))
		case 2:
			return c != A && s != A
		}
		return true
	})

	x := n.boot(X, xc.hook(func(m Message) bool { return isOwnVote(m, X, H, consensus.Precommit, true) }))
	a := n.boot(A, nil)
	b := n.boot(B, nil)
	n.nodes[n.keys[proposerIdx]].SubmitPayment(n.pay(H, "5"))
	n.pollCommits()
	x.Start()
	a.Start()
	b.Start()

	select {
	case <-xc.fired:
	case <-time.After(30 * time.Second * testTimeSlack):
		t.Fatal("X never precommitted non-nil at H")
	}
	x.Stop()
	n.mu.Lock()
	delete(n.nodes, X)
	n.mu.Unlock()
	{
		dl := time.Now().Add(10 * time.Second * testTimeSlack)
		for a.Snapshot().Height <= H && time.Now().Before(dl) {
			time.Sleep(10 * time.Millisecond)
		}
		if a.Snapshot().Height <= H {
			for _, k := range []string{X, A, B} {
				var vs []string
				for _, v := range n.votesBy(k, H) {
					vs = append(vs, v.Type.String()+"@r"+itoa32(v.Round)+":"+hex4(v.BlockDigest))
				}
				t.Logf("votes at H by %s..: %v", k[:6], vs)
			}
			t.Logf("A height %d", a.Snapshot().Height)
			t.Fatal("A did not decide H")
		}
	}
	sA := a.Snapshot()
	t.Logf("A decided H=%d digest %x.. round %d with %d precommits; merchant balance on A = %s",
		H, sA.LastCommit.BlockDigest[:4], sA.LastCommit.Round, len(sA.LastCommit.Votes), balance(sA.Accounts, "merchant").RatString())

	time.Sleep(rebootDelay) // the reboot itself takes a while; B (awake, but cut off) times out round 0 meanwhile
	phase.Store(2)
	x2 := n.boot(X, nil)
	if x2.height != H {
		t.Fatalf("restart loaded height %d, want %d", x2.height, H)
	}
	x2.Start()
	{
		dl := time.Now().Add(8 * time.Second)
		for time.Now().Before(dl) && !(x2.Snapshot().Height > H+1 && b.Snapshot().Height > H+1) {
			time.Sleep(20 * time.Millisecond)
		}
		t.Logf("with A away, after reboot: X height %d, B height %d (H=%d)", x2.Snapshot().Height, b.Snapshot().Height, H)
	}

	atH := n.commitsAt(H)
	for d, r := range atH {
		t.Logf("H=%d committed %x.. on %s.. round %d, %d precommits, Verify(static set)=%v",
			H, d[:4], r.node[:6], r.commit.Round, len(r.commit.Votes), r.commit.Verify(signing.Ed25519{}, "testnet", n.vs))
	}
	var xVotes []string
	for _, v := range n.votesBy(X, H) {
		xVotes = append(xVotes, v.Type.String()+"@r"+itoa32(v.Round)+":"+hex4(v.BlockDigest))
	}
	t.Logf("X's signed votes at H: %v", xVotes)

	// Phase 3: A comes back; catch-up decides which branch survives.
	phase.Store(3)
	waitFor(t, 30*time.Second, func() bool {
		return a.Snapshot().Height > H+2 && b.Snapshot().Height > H+2 && x2.Snapshot().Height > H+2
	}, "network did not resume after A rejoined")
	atH = n.commitsAt(H)
	for d, r := range atH {
		t.Logf("final: H=%d committed %x.. (first seen on %s.., round %d)", H, d[:4], r.node[:6], r.commit.Round)
	}
	sA2, sB := a.Snapshot(), b.Snapshot()
	t.Logf("after A rejoins: A height %d merchant=%s | B height %d merchant=%s | X balance on B=%s",
		sA2.Height, balance(sA2.Accounts, "merchant").RatString(), sB.Height, balance(sB.Accounts, "merchant").RatString(),
		balance(sB.Accounts, X).RatString())
	if len(atH) > 1 {
		t.Errorf("FORK at height %d with zero Byzantine validators (one honest restart)", H)
	}
}

// S2 — shared power outage in the deployment the docs recommend: two always-on desktops X1:2 X2:2
// "alone hold quorum" (4 of 5), one laptop L:1 on battery. The outage lands after the desktops'
// precommits left but before each other's precommit reached them (or before their snapshot fsync);
// L (on battery) received both and decided D1. Power returns; the desktops reboot before L's Wi-Fi
// is back. No partition beyond the outage itself, no Byzantine node.
func TestV3S2SharedPowerOutageForks(t *testing.T) { v3S2(t, []int64{2, 2, 1}, false) }

// Control: identical, but L is reachable when the desktops reboot -> bootstrap sync adopts D1.
func TestV3S2ControlDeciderReachable(t *testing.T) { v3S2(t, []int64{2, 2, 1}, true) }

// S3: three desktops at 3/10 each (NO validator holds >= 1/3) that together hold quorum, laptop 1/10.
func TestV3S3NoValidatorAboveThirdForks(t *testing.T) { v3S2(t, []int64{3, 3, 3, 1}, false) }

func v3S2(t *testing.T, powers []int64, lReachableAtReboot bool) {
	n := newV3Net(t, powers, true)
	defer n.shutdown()
	desk := n.keys[:len(n.keys)-1]
	L := n.keys[len(n.keys)-1]
	_ = desk[0]
	isDesk := func(k string) bool {
		for _, d := range desk {
			if d == k {
				return true
			}
		}
		return false
	}
	H := int64(4)
	for !isDesk(n.vs.ProposerAt(H, 0)) {
		H++
	}
	t.Logf("static powers %v (last = laptop L), quorum %d; (H=%d,r=0) proposer is a desktop", powers, n.vs.Quorum(), H)

	var phase atomic.Int32
	phase.Store(1)
	sw := map[string]*crashSwitch{}
	for _, d := range desk {
		sw[d] = newCrashSwitch()
	}
	var lIsolated atomic.Bool
	n.setAllow(func(from, to string, m Message) bool {
		if c := sw[from]; c != nil && phase.Load() == 1 && !c.passesFrom(m) {
			return false
		}
		if c := sw[to]; c != nil && phase.Load() == 1 && c.down.Load() {
			return false
		}
		if lIsolated.Load() && (from == L || to == L) {
			return false
		}
		if isDesk(from) && isDesk(to) && m.Type == MsgVote && m.Vote.Height == H && m.Vote.Type == consensus.Precommit && phase.Load() == 1 {
			return false // desktop<->desktop precommits still in flight when the power dies
		}
		return true
	})
	n.setHTTP(func(c, s string) bool {
		if lIsolated.Load() && (c == L || s == L) {
			return false
		}
		if phase.Load() == 1 && (sw[c] != nil && sw[c].down.Load() || sw[s] != nil && sw[s].down.Load()) {
			return false
		}
		return true
	})
	var dn []*Node
	for _, d := range desk {
		d := d
		dn = append(dn, n.boot(d, sw[d].hook(func(m Message) bool { return isOwnVote(m, d, H, consensus.Precommit, true) })))
	}
	l := n.boot(L, nil)
	n.pollCommits()
	for _, nd := range dn {
		nd.Start()
	}
	l.Start()
	waitFor(t, 20*time.Second, func() bool { return l.Snapshot().Height >= H-1 }, "network did not reach H-1")
	l.SubmitPayment(n.pay(H, "5")) // a payment made at the laptop, gossiped to the desktops

	for _, d := range desk {
		select {
		case <-sw[d].fired:
		case <-time.After(30 * time.Second * testTimeSlack):
			t.Fatal("a desktop never precommitted non-nil at H")
		}
	}
	for _, nd := range dn {
		nd.Stop()
	}
	n.mu.Lock()
	for _, d := range desk {
		delete(n.nodes, d)
	}
	n.mu.Unlock()
	{
		dl := time.Now().Add(10 * time.Second * testTimeSlack)
		for l.Snapshot().Height <= H && time.Now().Before(dl) {
			time.Sleep(10 * time.Millisecond)
		}
		if l.Snapshot().Height <= H {
			for _, k := range n.keys {
				var vs []string
				for _, v := range n.votesBy(k, H) {
					vs = append(vs, v.Type.String()+"@r"+itoa32(v.Round)+":"+hex4(v.BlockDigest))
				}
				t.Logf("votes at H by %s..: %v", k[:6], vs)
			}
			t.Fatalf("L did not decide H (L at %d)", l.Snapshot().Height)
		}
	}
	lIsolated.Store(!lReachableAtReboot) // router is down too
	sL := l.Snapshot()
	t.Logf("L decided H=%d digest %x.. (%d precommits); merchant on L = %s",
		H, sL.LastCommit.BlockDigest[:4], len(sL.LastCommit.Votes), balance(sL.Accounts, "merchant").RatString())

	phase.Store(2)
	time.Sleep(200 * time.Millisecond)
	var rn []*Node
	for _, d := range desk {
		rn = append(rn, n.boot(d, nil))
	}
	r1 := rn[0]
	t.Logf("desktops rebooted from their snapshots at height %d", r1.height)
	for _, nd := range rn {
		nd.Start()
	}
	waitFor(t, 20*time.Second, func() bool {
		for _, nd := range rn {
			if nd.Snapshot().Height <= H+1 {
				return false
			}
		}
		return true
	}, "desktops did not progress past H after reboot")
	atH := n.commitsAt(H)
	for d, r := range atH {
		t.Logf("H=%d committed %x.. on %s.. round %d, %d precommits, Verify(static set)=%v",
			H, d[:4], r.node[:6], r.commit.Round, len(r.commit.Votes), r.commit.Verify(signing.Ed25519{}, "testnet", n.vs))
	}
	lIsolated.Store(false) // Wi-Fi back
	waitFor(t, 15*time.Second, func() bool { return l.Snapshot().Height > r1.Snapshot().Height-2 && l.Snapshot().Height > H+2 }, "L did not rejoin")
	sL2 := l.Snapshot()
	t.Logf("after L rejoins: L height %d merchant=%s (was %s)", sL2.Height, balance(sL2.Accounts, "merchant").RatString(),
		balance(sL.Accounts, "merchant").RatString())
	if len(atH) > 1 {
		t.Errorf("FORK at height %d after a shared power outage, zero Byzantine validators", H)
	}
}

func itoa32(r int32) string {
	if r == 0 {
		return "0"
	}
	s := ""
	for r > 0 {
		s = string(rune('0'+r%10)) + s
		r /= 10
	}
	return s
}

func hex4(d [32]byte) string {
	const hx = "0123456789abcdef"
	if d == ([32]byte{}) {
		return "nil"
	}
	out := make([]byte, 0, 8)
	for _, b := range d[:4] {
		out = append(out, hx[b>>4], hx[b&15])
	}
	return string(out)
}
