package node

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

func keysFor(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%03d", i) // already sorted
	}
	return keys
}

func TestFullMeshNeighbors(t *testing.T) {
	keys := keysFor(5)
	if got := len(Neighbors(keys, keys[2], 0)); got != 4 { // maxPeers 0 → all-to-all
		t.Fatalf("full mesh should have n-1=4 neighbours, got %d", got)
	}
}

func TestGroupParametersScalesDimension(t *testing.T) {
	// A tiny network or no cap stays 1-dimensional (all-to-all).
	if _, dim := ledger.GroupParameters(5, 0); dim != 1 {
		t.Fatalf("no cap should give dim 1, got %d", dim)
	}
	// A real cap on a larger network raises the dimension above 1.
	if _, dim := ledger.GroupParameters(100, 8); dim < 2 {
		t.Fatalf("100 nodes capped at 8 peers should need dim >= 2, got %d", dim)
	}
}

// The Hamming-graph topology must stay connected (so relayed gossip reaches everyone) while cutting
// fan-out below all-to-all. Checked on counts that fill their grid cleanly, plus the 8-node target.
func TestHypercubeConnectedAndReducesFanout(t *testing.T) {
	for _, n := range []int{8, 9, 16, 27} {
		keys := keysFor(n)
		const maxPeers = 6
		adj := make(map[string][]string, n)
		maxFanout := 0
		for _, k := range keys {
			nb := Neighbors(keys, k, maxPeers)
			adj[k] = nb
			if len(nb) > maxFanout {
				maxFanout = len(nb)
			}
		}
		if maxFanout >= n-1 {
			t.Fatalf("n=%d: fan-out not reduced (%d >= %d)", n, maxFanout, n-1)
		}
		// BFS from keys[0] must reach all n (edges are symmetric).
		seen := map[string]bool{keys[0]: true}
		queue := []string{keys[0]}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, nb := range adj[cur] {
				if !seen[nb] {
					seen[nb] = true
					queue = append(queue, nb)
				}
			}
		}
		if len(seen) != n {
			t.Fatalf("n=%d: topology not connected, reached %d of %d", n, len(seen), n)
		}
	}
}

// hyperBus is an in-memory transport that delivers each node's broadcasts ONLY to its topology
// neighbours — so consensus works only if messages are relayed across the multi-hop graph.
type hyperBus struct {
	mu        sync.Mutex
	nodes     map[string]*Node
	neighbors map[string][]string
}

func (b *hyperBus) Broadcast(from string, msg Message) {
	b.mu.Lock()
	var targets []*Node
	for _, k := range b.neighbors[from] {
		if nd := b.nodes[k]; nd != nil {
			targets = append(targets, nd)
		}
	}
	b.mu.Unlock()
	for _, nd := range targets {
		nd := nd
		go func() {
			switch msg.Type {
			case MsgProposal:
				nd.SubmitProposal(*msg.Proposal)
			case MsgVote:
				nd.SubmitVote(*msg.Vote)
			case MsgPayment:
				nd.SubmitPayment(*msg.Payment)
			}
		}()
	}
}

// Consensus must work when each node can only talk to its hypercube neighbours and relies on relay
// to flood proposals/votes across the graph.
func TestConsensusOverHypercubeTopology(t *testing.T) {
	const n = 9 // 3x3 grid (dim 2)
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	keys := make([]string, 0, n)
	for _, v := range vs.Validators() {
		keys = append(keys, v.PublicKey)
	}
	genesis := []ledger.Account{{PublicKey: keys[0], Name: "founder", Amount: rat("1000"), ValidatorNode: keys[0]}}

	bus := &hyperBus{nodes: map[string]*Node{}, neighbors: map[string][]string{}}
	nodes := make([]*Node, n)
	for i, s := range signers {
		nodes[i] = New(Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: s, Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 100 * time.Millisecond,
			Genesis: genesis,
		}, bus)
		bus.nodes[s.PublicKeyBase64()] = nodes[i]
		bus.neighbors[s.PublicKeyBase64()] = Neighbors(keys, s.PublicKeyBase64(), 6) // dim 2, ~4 peers each (not 8)
	}
	// sanity: no node has all-to-all
	if len(bus.neighbors[keys[0]]) >= n-1 {
		t.Fatalf("expected reduced fan-out, node 0 has %d peers", len(bus.neighbors[keys[0]]))
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()

	// All nodes must reach height >= 2 with an identical committed state — i.e. consensus flooded
	// across the multi-hop topology via relay.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var snaps []Snapshot
		ok := true
		for _, nd := range nodes {
			s := nd.Snapshot()
			snaps = append(snaps, s)
			if s.Height < 2 {
				ok = false
			}
		}
		if ok {
			want := ledger.StateDigest(snaps[0].Accounts)
			for i, s := range snaps {
				if ledger.StateDigest(s.Accounts) != want {
					t.Fatalf("node %d diverged over the hypercube topology", i)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("nodes did not reach consensus over the hypercube topology in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
