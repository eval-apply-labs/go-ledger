package node

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad rat: " + s)
	}
	return r
}

// bus is an in-memory Transport that delivers each broadcast to every other registered node,
// asynchronously (a goroutine per delivery), mimicking a full-mesh network.
type bus struct {
	mu    sync.Mutex
	nodes map[string]*Node
}

func newBus() *bus { return &bus{nodes: map[string]*Node{}} }

func (b *bus) register(key string, n *Node) {
	b.mu.Lock()
	b.nodes[key] = n
	b.mu.Unlock()
}

func (b *bus) Broadcast(from string, msg Message) {
	b.mu.Lock()
	targets := make(map[string]*Node, len(b.nodes))
	for k, v := range b.nodes {
		targets[k] = v
	}
	b.mu.Unlock()
	for k, nd := range targets {
		if k == from {
			continue
		}
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

func balance(accts []ledger.Account, pk string) *big.Rat {
	for _, a := range accts {
		if a.PublicKey == pk {
			return a.Amount
		}
	}
	return new(big.Rat)
}

// Four validator nodes, wired full-mesh in-memory, must all decide the same block and apply the
// submitted payment.
func TestNodesReachConsensusAndApplyPayment(t *testing.T) {
	const n = 4
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)

	// Genesis: alice (an account we control) funded, bob empty.
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	genesis := []ledger.Account{
		{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("100"), ValidatorNode: "v"},
		{PublicKey: "bob", Name: "bob", Amount: rat("0"), ValidatorNode: "v"},
	}

	b := newBus()
	nodes := make([]*Node, n)
	for i := 0; i < n; i++ {
		cfg := Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i],
			Validators: vs, TransactionCost: rat("0.1"),
			Timeout: 5 * time.Second, Genesis: genesis,
		}
		nodes[i] = New(cfg, b)
		b.register(signers[i].PublicKeyBase64(), nodes[i])
	}

	// A signed payment alice -> bob 10, submitted to every node BEFORE start, so whichever node
	// proposes height 1 includes it.
	pay, err := ledger.SignPayment(alice, ledger.Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "bob", Amount: rat("10"), BlockID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
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

	// Wait until every node has committed at least height 1 (i.e. reached height >= 2).
	deadline := time.Now().Add(4 * time.Second)
	var snaps []Snapshot
	for {
		snaps = snaps[:0]
		allAdvanced := true
		for _, nd := range nodes {
			s := nd.Snapshot()
			snaps = append(snaps, s)
			if s.Height < 2 {
				allAdvanced = false
			}
		}
		if allAdvanced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes did not reach height 2 in time; heights=%v", heights(snaps))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// All nodes must agree on the committed state, and bob must have received 10 - fee.
	want := ledger.StateDigest(snaps[0].Accounts)
	for i, s := range snaps {
		if ledger.StateDigest(s.Accounts) != want {
			t.Fatalf("node %d diverged from node 0's state", i)
		}
	}
	if got := balance(snaps[0].Accounts, "bob"); got.Cmp(rat("9.9")) != 0 {
		t.Fatalf("bob = %s, want 9.9 (payment not applied through consensus)", got.FloatString(2))
	}
}

func heights(snaps []Snapshot) []int64 {
	out := make([]int64, len(snaps))
	for i, s := range snaps {
		out[i] = s.Height
	}
	return out
}
