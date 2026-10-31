package node

import (
	"encoding/json"
	"math/big"
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

// dynNet: all-to-all in-memory gossip for dynamic-validator nodes, restartable by key.
type dynNet struct {
	mu    sync.Mutex
	nodes map[string]*Node
}

func (d *dynNet) Broadcast(from string, m Message) {
	d.mu.Lock()
	var targets []*Node
	for k, nd := range d.nodes {
		if k != from && nd != nil {
			targets = append(targets, nd)
		}
	}
	d.mu.Unlock()
	for _, nd := range targets {
		nd, mm := nd, copyMsgV3(m)
		go deliverToNode(nd, mm)
	}
}

func (d *dynNet) set(k string, nd *Node) { d.mu.Lock(); d.nodes[k] = nd; d.mu.Unlock() }

func readSnap(t *testing.T, path string) Checkpoint {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	return cp
}

// Founder A is the only genesis validator. At h1 it pays X 990 -> X becomes the only live validator
// (A keeps 10 < threshold 100). X commits on, both nodes persist snapshots.
func rotatedPair(t *testing.T) (dn *dynNet, mk func(*signing.Signer, string) *Node, a, x *signing.Signer, snapA, snapX string, genesis []ledger.Account) {
	a, _ = signing.Generate(signing.Ed25519{}, "testnet")
	x, _ = signing.Generate(signing.Ed25519{}, "testnet")
	genesis = []ledger.Account{{PublicKey: a.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: a.PublicKeyBase64()}}
	dir := t.TempDir()
	snapA, snapX = filepath.Join(dir, "a.json"), filepath.Join(dir, "x.json")
	dn = &dynNet{nodes: map[string]*Node{}}
	mk = func(s *signing.Signer, path string) *Node {
		nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: s, ValidatorThreshold: rat("100"),
			TransactionCost: rat("0.1"), Timeout: 300 * time.Millisecond, MinBlockInterval: 30 * time.Millisecond,
			Genesis: genesis, SnapshotPath: path}, dn)
		dn.set(s.PublicKeyBase64(), nd)
		return nd
	}
	return
}

func TestV3RotationOwnSnapshotAndRollback(t *testing.T) {
	_, mk, a, x, snapA, snapX, genesis := rotatedPair(t)
	A, X := a.PublicKeyBase64(), x.PublicKeyBase64()
	nA, nX := mk(a, snapA), mk(x, snapX)
	pay, _ := ledger.SignPayment(a, ledger.Payment{PublicKeyFrom: A, PublicKeyTo: X, Amount: rat("990"), BlockID: 1})
	nA.SubmitPayment(pay)
	nA.Start()
	nX.Start()
	waitFor(t, 10*time.Second, func() bool { return nX.Snapshot().Height >= 8 && nA.Snapshot().Height >= 3 }, "rotated chain did not advance")
	nA.Stop()
	nX.Stop()
	time.Sleep(100 * time.Millisecond)

	cpX, cpA := readSnap(t, snapX), readSnap(t, snapA)
	probe := &Node{cfg: Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, ValidatorThreshold: rat("100")}}
	t.Logf("X's own snapshot h=%d: signers=%d carried set=%v | verify vs live=%v vs genesis=%v",
		cpX.Commit.Height, len(cpX.Commit.Votes), len(cpX.Validators), probe.verifyCheckpoint(cpX, cpX.Accounts), probe.verifyCheckpoint(cpX, genesis))
	t.Logf("before: X=%s A=%s (both nodes at h=%d/%d)", balance(cpX.Accounts, X).RatString(), balance(cpX.Accounts, A).RatString(), cpX.Commit.Height, cpA.Commit.Height)
	oldCommit2 := cpX.Commit

	// Simultaneous outage: both reboot from their own (valid) snapshots.
	rA, rX := mk(a, snapA), mk(x, snapX)
	t.Logf("after reboot New(): A.height=%d X.height=%d (snapshots silently discarded: %v)", rA.height, rX.height, rA.height == 1 && rX.height == 1)
	rA.Start()
	rX.Start()
	defer rA.Stop()
	defer rX.Stop()
	time.Sleep(2 * time.Second)
	s := rA.Snapshot()
	sx := rX.Snapshot()
	t.Logf("X's own node after reboot: h=%d X=%s A=%s", sx.Height, balance(sx.Accounts, X).RatString(), balance(sx.Accounts, A).RatString())
	t.Logf("founder node after reboot: height=%d X=%s A=%s", s.Height, balance(s.Accounts, X).RatString(), balance(s.Accounts, A).RatString())
	// Conflicting certificates at the same height on the two histories.
	time.Sleep(50 * time.Millisecond)
	_ = oldCommit2
	if balance(sx.Accounts, X).Sign() == 0 || balance(s.Accounts, X).Sign() == 0 && balance(s.Accounts, A).Cmp(rat("1000")) == 0 {
		t.Errorf("ROLLBACK: committed history up to h=%d silently replaced by a new chain from genesis", cpX.Commit.Height)
	}
	// The old signed payment is valid again on the new chain at its BlockID if it is still ahead.
	_ = new(big.Rat)
}

// Only the founder's node reboots (X keeps running). The founder holds all genesis power, so after
// the silent genesis fallback it alone re-commits a parallel chain from genesis: two live chains.
// A fresh genesis-anchored joiner then adopts the founder's NEW chain, not the live one.
func TestV3RotationFounderRebootAloneForks(t *testing.T) {
	_, mk, a, x, snapA, snapX, genesis := rotatedPair(t)
	A, X := a.PublicKeyBase64(), x.PublicKeyBase64()
	nA, nX := mk(a, snapA), mk(x, snapX)
	pay, _ := ledger.SignPayment(a, ledger.Payment{PublicKeyFrom: A, PublicKeyTo: X, Amount: rat("990"), BlockID: 1})
	nA.SubmitPayment(pay)
	nA.Start()
	nX.Start()
	defer func() { nX.Stop() }()
	waitFor(t, 10*time.Second, func() bool { return nX.Snapshot().Height >= 8 && nA.Snapshot().Height >= 3 }, "rotated chain did not advance")
	nA.Stop()
	time.Sleep(100 * time.Millisecond)
	rA := mk(a, snapA)
	t.Logf("founder rebooted at height %d (X is at %d)", rA.height, nX.Snapshot().Height)
	rA.Start()
	defer rA.Stop()
	time.Sleep(3 * time.Second)
	sA, sX := rA.Snapshot(), nX.Snapshot()
	t.Logf("3s later: founder node h=%d X=%s A=%s | X node h=%d X=%s A=%s",
		sA.Height, balance(sA.Accounts, X).RatString(), balance(sA.Accounts, A).RatString(),
		sX.Height, balance(sX.Accounts, X).RatString(), balance(sX.Accounts, A).RatString())

	// A new member joins with the published genesis config and both peers as bootstrap.
	srvA, srvX := httptest.NewServer(NewServer(rA).Handler()), httptest.NewServer(NewServer(nX).Handler())
	defer srvA.Close()
	defer srvX.Close()
	j, _ := signing.Generate(signing.Ed25519{}, "testnet")
	joiner := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: j, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: 300 * time.Millisecond, MinBlockInterval: 30 * time.Millisecond,
		Genesis: genesis, Bootstrap: []string{srvX.URL, srvA.URL}}, &dynNet{nodes: map[string]*Node{}})
	joiner.Start()
	defer joiner.Stop()
	time.Sleep(2 * time.Second)
	sj := joiner.Snapshot()
	t.Logf("genesis-anchored joiner adopted h=%d with X=%s A=%s", sj.Height-1, balance(sj.Accounts, X).RatString(), balance(sj.Accounts, A).RatString())
	if sA.Height > 2 && balance(sA.Accounts, X).Sign() == 0 && balance(sX.Accounts, X).Sign() > 0 {
		t.Errorf("FORK: a single honest reboot of the founder started a parallel chain from genesis")
	}
}

// Breadth: founders all still active, but a newcomer N holds most of a quorum. How often does a
// node's OWN latest snapshot fail the genesis anchor (=> silent fallback to genesis on reboot)?
func TestV3RotationPartialBreadth(t *testing.T) {
	gen := func() *signing.Signer { s, _ := signing.Generate(signing.Ed25519{}, "testnet"); return s }
	f1, f2, f3, nn := gen(), gen(), gen(), gen()
	F1, F2, F3, N := f1.PublicKeyBase64(), f2.PublicKeyBase64(), f3.PublicKeyBase64(), nn.PublicKeyBase64()
	genesis := []ledger.Account{
		{PublicKey: F1, Amount: rat("3000"), ValidatorNode: F1},
		{PublicKey: F2, Amount: rat("1000"), ValidatorNode: F2},
		{PublicKey: F3, Amount: rat("1000"), ValidatorNode: F3},
	}
	dir := t.TempDir()
	dn := &dynNet{nodes: map[string]*Node{}}
	var nodes []*Node
	var paths []string
	for i, s := range []*signing.Signer{f1, f2, f3, nn} {
		p := filepath.Join(dir, string(rune('a'+i))+".json")
		paths = append(paths, p)
		nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: s, ValidatorThreshold: rat("100"),
			TransactionCost: rat("0.1"), Timeout: 300 * time.Millisecond, MinBlockInterval: 30 * time.Millisecond,
			Genesis: genesis, SnapshotPath: p}, dn)
		dn.set(s.PublicKeyBase64(), nd)
		nodes = append(nodes, nd)
	}
	pay, _ := ledger.SignPayment(f1, ledger.Payment{PublicKeyFrom: F1, PublicKeyTo: N, Amount: rat("2899"), BlockID: 1})
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
	waitFor(t, 10*time.Second, func() bool { return nodes[3].Snapshot().Height >= 3 }, "no progress")
	probe := &Node{cfg: Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, ValidatorThreshold: rat("100")}}
	total, failed := 0, 0
	for i := 0; i < 40; i++ {
		time.Sleep(97 * time.Millisecond)
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var cp Checkpoint
			if json.Unmarshal(raw, &cp) != nil || cp.Commit.Height < 2 {
				continue
			}
			total++
			if !probe.verifyCheckpoint(cp, genesis) {
				failed++
			}
		}
	}
	s := nodes[3].Snapshot()
	var vs []string
	for _, v := range s.Validators {
		vs = append(vs, v.PublicKey[:4]+":"+itoa64(v.Power))
	}
	t.Logf("live set %v (quorum %d); own snapshots failing the genesis anchor: %d of %d samples",
		vs, consensus.NewValidatorSet(s.Validators).Quorum(), failed, total)
}

func itoa64(v int64) string { return big.NewInt(v).String() }
