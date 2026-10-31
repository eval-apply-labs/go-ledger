package node

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

func namesFor(count int) []string {
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("n%03d", i) // already sorted
	}
	return names
}

func TestFullMeshNeighbors(t *testing.T) {
	names := namesFor(5)
	if neighborCount := len(Neighbors(names, names[2], 0)); neighborCount != 4 { // maxPeers 0 → all-to-all
		t.Fatalf("full mesh should have n-1=4 neighbours, got %d", neighborCount)
	}
}

func TestGroupParametersScalesDimension(t *testing.T) {
	// A tiny network or no cap stays 1-dimensional (all-to-all).
	if _, dimensions := ledger.GroupParameters(5, 0); dimensions != 1 {
		t.Fatalf("no cap should give dim 1, got %d", dimensions)
	}
	// A real cap on a larger network raises the dimension above 1.
	if _, dimensions := ledger.GroupParameters(100, 8); dimensions < 2 {
		t.Fatalf("100 nodes capped at 8 peers should need dim >= 2, got %d", dimensions)
	}
}

// The Hamming-graph topology must stay connected (so relayed gossip reaches everyone) while cutting
// fan-out below all-to-all. Checked on counts that fill their grid cleanly, plus the 8-node target.
func TestHypercubeConnectedAndReducesFanout(t *testing.T) {
	for _, nodeCount := range []int{8, 9, 16, 27} {
		names := namesFor(nodeCount)
		const maxPeers = 6
		neighborsByName := make(map[string][]string, nodeCount)
		maxFanout := 0
		for _, name := range names {
			neighbors := Neighbors(names, name, maxPeers)
			neighborsByName[name] = neighbors
			if len(neighbors) > maxFanout {
				maxFanout = len(neighbors)
			}
		}
		if maxFanout >= nodeCount-1 {
			t.Fatalf("n=%d: fan-out not reduced (%d >= %d)", nodeCount, maxFanout, nodeCount-1)
		}
		// BFS from names[0] must reach all nodeCount nodes (edges are symmetric).
		seen := map[string]bool{names[0]: true}
		queue := []string{names[0]}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for _, neighbor := range neighborsByName[current] {
				if !seen[neighbor] {
					seen[neighbor] = true
					queue = append(queue, neighbor)
				}
			}
		}
		if len(seen) != nodeCount {
			t.Fatalf("n=%d: topology not connected, reached %d of %d", nodeCount, len(seen), nodeCount)
		}
	}
}

// hyperBus is an in-memory transport that delivers each node's broadcasts ONLY to its topology
// neighbours — so consensus works only if messages are relayed across the multi-hop graph.
type hyperBus struct { // implements Transport
	mutex     sync.Mutex
	nodes     map[string]*Node
	neighbors map[string][]string
}

func (hypercubeBus *hyperBus) Broadcast(from string, message Message) {
	hypercubeBus.mutex.Lock()
	var targets []*Node
	for _, neighborName := range hypercubeBus.neighbors[from] {
		if neighbor := hypercubeBus.nodes[neighborName]; neighbor != nil {
			targets = append(targets, neighbor)
		}
	}
	hypercubeBus.mutex.Unlock()
	for _, target := range targets {
		node := target
		go func() {
			switch message.Type {
			case MessageProposal:
				node.SubmitProposal(*message.Proposal)
			case MessageVote:
				node.SubmitVote(*message.Vote)
			case MessagePayment:
				node.SubmitPayment(*message.Payment)
			}
		}()
	}
}

// Consensus must work when each node can only talk to its hypercube neighbours and relies on relay
// to flood proposals/votes across the graph.
func TestConsensusOverHypercubeTopology(t *testing.T) {
	const nodeCount = 9                // 3x3 grid (dim 2)
	names := validatorNames(nodeCount) // in name order
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(accounts, 5*time.Second, 100*time.Millisecond)

	hypercubeBus := &hyperBus{nodes: map[string]*Node{}, neighbors: map[string][]string{}}
	nodes := make([]*Node, nodeCount)
	for i, name := range names {
		nodes[i] = New(testConfig(staticChain(names...), name, genesis), hypercubeBus)
		hypercubeBus.nodes[name] = nodes[i]
		hypercubeBus.neighbors[name] = Neighbors(names, name, 6) // dim 2, ~4 peers each (not 8)
	}
	// sanity: no node has all-to-all
	if len(hypercubeBus.neighbors[names[0]]) >= nodeCount-1 {
		t.Fatalf("expected reduced fan-out, node 0 has %d peers", len(hypercubeBus.neighbors[names[0]]))
	}
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()

	// All nodes must reach height >= 2 with an identical committed state — i.e. consensus flooded
	// across the multi-hop topology via relay.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var snapshots []Snapshot
		allAdvanced := true
		for _, node := range nodes {
			snapshot := node.Snapshot()
			snapshots = append(snapshots, snapshot)
			if snapshot.Height < 2 {
				allAdvanced = false
			}
		}
		if allAdvanced {
			want := ledger.AccountsDigest(snapshots[0].Accounts)
			for i, snapshot := range snapshots {
				if ledger.AccountsDigest(snapshot.Accounts) != want {
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
