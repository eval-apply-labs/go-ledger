package node

// Third-pass verifier harness (R3-history-safety). Written from scratch.
//
// Network model: an in-memory full-mesh gossip bus whose every link is gated by a partition
// predicate evaluated at DELIVERY time, plus real HTTP checkpoint servers (NewServer(n).Handler())
// so the production bootstrap (syncThenStart) and catch-up (catchUpLoop -> bestCheckpoint ->
// evCatchUp -> catchUp) paths run for real, gated by the same predicate. Each client node j sees
// server i through its own URL srv[j][i], so "j can reach i" is enforced per direction.
//
// Crash model: a node "crashes" when its isolation flag is set (nothing in/out on gossip or HTTP)
// and Stop() is called; it "reboots" via New(sameConfig) — exactly what cmd/server does — which
// reloads the node's own snapshot file.

import (
	"net/http"
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

type v3net struct {
	t       *testing.T
	keys    []string
	signers map[string]*signing.Signer
	vs      *consensus.ValidatorSet
	genesis []ledger.Account
	payer   *signing.Signer
	cfgs    map[string]Config

	mu    sync.Mutex
	nodes map[string]*Node

	// gossip predicate (from, to, msg) -> deliver?
	allow atomic.Pointer[func(from, to string, m Message) bool]
	// http predicate (client, server) -> reachable?
	httpAllow atomic.Pointer[func(client, server string) bool]

	srvs []*httptest.Server

	tapMu sync.Mutex
	votes []consensus.Vote // every vote ever broadcast (own or relayed)

	commitMu sync.Mutex
	commits  map[int64]map[[32]byte]commitRec
	stopPoll chan struct{}
}

type commitRec struct {
	node   string
	commit consensus.Commit
}

func newV3Net(t *testing.T, powers []int64, withSnapshots bool) *v3net {
	t.Helper()
	n := &v3net{t: t, signers: map[string]*signing.Signer{}, cfgs: map[string]Config{}, nodes: map[string]*Node{},
		commits: map[int64]map[[32]byte]commitRec{}, stopPoll: make(chan struct{})}
	var vals []consensus.Validator
	for _, p := range powers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		k := s.PublicKeyBase64()
		n.signers[k] = s
		n.keys = append(n.keys, k)
		vals = append(vals, consensus.Validator{PublicKey: k, Power: p})
		n.genesis = append(n.genesis, ledger.Account{PublicKey: k, Amount: rat("100"), ValidatorNode: k})
	}
	n.payer, _ = signing.Generate(signing.Ed25519{}, "testnet")
	n.genesis = append(n.genesis,
		ledger.Account{PublicKey: n.payer.PublicKeyBase64(), Name: "payer", Amount: rat("1000"), ValidatorNode: n.keys[0]},
		ledger.Account{PublicKey: "merchant", Name: "merchant", Amount: rat("0"), ValidatorNode: n.keys[0]})
	n.vs = consensus.NewValidatorSet(vals)
	all := func(string, string, Message) bool { return true }
	n.allow.Store(&all)
	allH := func(string, string) bool { return true }
	n.httpAllow.Store(&allH)

	// One HTTP endpoint per (client, server) pair.
	urls := map[string]map[string]string{}
	for _, c := range n.keys {
		urls[c] = map[string]string{}
		for _, s := range n.keys {
			if c == s {
				continue
			}
			c, s := c, s
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !(*n.httpAllow.Load())(c, s) {
					http.Error(w, "unreachable", http.StatusServiceUnavailable)
					return
				}
				n.mu.Lock()
				nd := n.nodes[s]
				n.mu.Unlock()
				if nd == nil {
					http.Error(w, "down", http.StatusServiceUnavailable)
					return
				}
				NewServer(nd).Handler().ServeHTTP(w, r)
			}))
			n.srvs = append(n.srvs, srv)
			urls[c][s] = srv.URL
		}
	}
	dir := t.TempDir()
	for i, k := range n.keys {
		var boot []string
		for _, s := range n.keys {
			if s != k {
				boot = append(boot, urls[k][s])
			}
		}
		cfg := Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: n.signers[k], Validators: n.vs,
			TransactionCost: rat("0.1"), Timeout: 300 * time.Millisecond, MinBlockInterval: 30 * time.Millisecond,
			Genesis: n.genesis, Bootstrap: boot, // the docs: "Use both together"
		}
		if withSnapshots {
			cfg.SnapshotPath = filepath.Join(dir, "snap"+string(rune('a'+i))+".json")
		}
		n.cfgs[k] = cfg
	}
	return n
}

func (n *v3net) setAllow(f func(from, to string, m Message) bool) { n.allow.Store(&f) }
func (n *v3net) setHTTP(f func(client, server string) bool)       { n.httpAllow.Store(&f) }

func (n *v3net) node(k string) *Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.nodes[k]
}

// transport for node k (optionally wrapped by an outgoing hook that may veto/observe).
type v3transport struct {
	n    *v3net
	self string
	hook func(m Message) // called BEFORE fan-out (on the sender's loop)
}

func (tr *v3transport) Broadcast(from string, m Message) {
	if tr.hook != nil {
		tr.hook(m)
	}
	m = copyMsgV3(m)
	if m.Type == MsgVote && m.Vote != nil {
		tr.n.tapMu.Lock()
		tr.n.votes = append(tr.n.votes, *m.Vote)
		tr.n.tapMu.Unlock()
	}
	for _, to := range tr.n.keys {
		if to == from {
			continue
		}
		to := to
		mm := copyMsgV3(m)
		go func() {
			if !(*tr.n.allow.Load())(from, to, mm) { // evaluated at delivery time
				return
			}
			if nd := tr.n.node(to); nd != nil {
				deliverToNode(nd, mm)
			}
		}()
	}
}

func copyMsgV3(m Message) Message {
	c := m
	if m.Vote != nil {
		v := *m.Vote
		c.Vote = &v
	}
	if m.Proposal != nil {
		p := *m.Proposal
		c.Proposal = &p
	}
	if m.Payment != nil {
		p := *m.Payment
		c.Payment = &p
	}
	return c
}

// boot creates (or re-creates after a crash) node k from its config — the real restart path.
func (n *v3net) boot(k string, hook func(Message)) *Node {
	nd := New(n.cfgs[k], &v3transport{n: n, self: k, hook: hook})
	n.mu.Lock()
	n.nodes[k] = nd
	n.mu.Unlock()
	return nd
}

// pollCommits records every (height, digest) commit any live node ever reports.
func (n *v3net) pollCommits() {
	go func() {
		for {
			select {
			case <-n.stopPoll:
				return
			default:
			}
			n.mu.Lock()
			live := map[string]*Node{}
			for k, v := range n.nodes {
				live[k] = v
			}
			n.mu.Unlock()
			for k, nd := range live {
				s := nd.Snapshot()
				if s.LastCommit.Height <= 0 {
					continue
				}
				n.commitMu.Lock()
				if n.commits[s.LastCommit.Height] == nil {
					n.commits[s.LastCommit.Height] = map[[32]byte]commitRec{}
				}
				if _, ok := n.commits[s.LastCommit.Height][s.LastCommit.BlockDigest]; !ok {
					n.commits[s.LastCommit.Height][s.LastCommit.BlockDigest] = commitRec{node: k, commit: s.LastCommit}
				}
				n.commitMu.Unlock()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
}

func (n *v3net) commitsAt(h int64) map[[32]byte]commitRec {
	n.commitMu.Lock()
	defer n.commitMu.Unlock()
	out := map[[32]byte]commitRec{}
	for d, r := range n.commits[h] {
		out[d] = r
	}
	return out
}

func (n *v3net) votesBy(k string, h int64) []consensus.Vote {
	n.tapMu.Lock()
	defer n.tapMu.Unlock()
	var out []consensus.Vote
	seen := map[string]bool{}
	for _, v := range n.votes {
		if v.Validator == k && v.Height == h && !seen[v.Signature] {
			seen[v.Signature] = true
			out = append(out, v)
		}
	}
	return out
}

func (n *v3net) shutdown() {
	close(n.stopPoll)
	time.Sleep(10 * time.Millisecond)
	n.mu.Lock()
	for _, nd := range n.nodes {
		func() {
			defer func() { _ = recover() }() // double Stop panics on close(quit)
			nd.Stop()
		}()
	}
	n.mu.Unlock()
	for _, s := range n.srvs {
		s.Close()
	}
}

func (n *v3net) pay(blockID int64, amt string) ledger.Payment {
	p, err := ledger.SignPayment(n.payer, ledger.Payment{PublicKeyFrom: n.payer.PublicKeyBase64(), PublicKeyTo: "merchant", Amount: rat(amt), BlockID: blockID})
	if err != nil {
		n.t.Fatal(err)
	}
	return p
}

func isOwnVote(m Message, key string, h int64, typ consensus.VoteType, nonNil bool) bool {
	if m.Type != MsgVote || m.Vote == nil || m.Vote.Validator != key || m.Vote.Height != h || m.Vote.Type != typ {
		return false
	}
	return !nonNil || m.Vote.BlockDigest != ([32]byte{})
}

func msgH(m Message) int64 {
	switch {
	case m.Type == MsgProposal && m.Proposal != nil:
		return m.Proposal.Height
	case m.Type == MsgVote && m.Vote != nil:
		return m.Vote.Height
	case m.Type == MsgPayment && m.Payment != nil:
		return m.Payment.BlockID
	}
	return -1
}
