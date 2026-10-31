package node

import (
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// addrNet routes by ADDRESS (like HTTPTransport) so the on-chain peer DNS (SetPeers) takes effect.
type addrNet struct {
	mu     sync.Mutex
	byAddr map[string]*Node
}

type addrTransport struct {
	net   *addrNet
	mu    sync.Mutex
	peers []string
}

func (t *addrTransport) SetPeers(p []string) {
	t.mu.Lock()
	t.peers = append([]string(nil), p...)
	t.mu.Unlock()
}
func (t *addrTransport) Peers() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.peers...)
}
func (t *addrTransport) Broadcast(from string, msg Message) {
	for _, a := range t.Peers() {
		t.net.mu.Lock()
		nd := t.net.byAddr[a]
		t.net.mu.Unlock()
		if nd != nil {
			go deliverToNode(nd, msg)
		}
	}
}

// PoC (critic, round 2): following the documented rollout ("once validators publish addresses (in
// genesis or via an update), nodes resolve their peers from committed state"), two of four equal
// validators publish their address. From the next height the publishers gossip ONLY to each other
// (resolvedPeerAddresses covers validators with an address), while the two non-publishers keep their
// static all-to-all peers but never hear from the publishers. Neither side holds a quorum's view, so
// consensus halts — and since the remaining validators' address updates need a block, it never resumes.
func TestPoCCriticPartialPeerDNSPublicationHalts(t *testing.T) {
	for _, publishers := range [][]int{{}, {0, 2}} {
		scheme, network := signing.Ed25519{}, "testnet"
		addrs := []string{"a", "b", "c", "d"}
		signers := make([]*signing.Signer, 4)
		vals := make([]consensus.Validator, 4)
		var genesis []ledger.Account
		for i := range signers {
			signers[i], _ = signing.Generate(scheme, network)
			k := signers[i].PublicKeyBase64()
			vals[i] = consensus.Validator{PublicKey: k, Power: 1}
			genesis = append(genesis, ledger.Account{PublicKey: k, Amount: rat("100"), ValidatorNode: k})
		}
		vs := consensus.NewValidatorSet(vals)
		an := &addrNet{byAddr: map[string]*Node{}}
		nodes := make([]*Node, 4)
		for i := range nodes {
			var others []string
			for j, a := range addrs {
				if j != i {
					others = append(others, a)
				}
			}
			nodes[i] = New(Config{
				NetworkID: network, Scheme: scheme, Signer: signers[i], Validators: vs,
				TransactionCost: rat("0.1"), Timeout: 200 * time.Millisecond, MinBlockInterval: 20 * time.Millisecond,
				Genesis: genesis,
			}, &addrTransport{net: an, peers: others})
			an.byAddr[addrs[i]] = nodes[i]
		}
		// The publishers' address updates, bound to block 2 (gossiped like any update).
		for _, i := range publishers {
			u, _ := ledger.SignAccountUpdate(signers[i], ledger.AccountUpdate{From: signers[i].PublicKeyBase64(), Address: addrs[i], BlockID: 2})
			for _, nd := range nodes {
				nd.SubmitUpdate(u)
			}
		}
		for _, nd := range nodes {
			nd.Start()
		}
		time.Sleep(6 * time.Second)
		var hs []int64
		published := 0
		for _, nd := range nodes {
			s := nd.Snapshot()
			hs = append(hs, s.Height)
			for _, a := range s.Accounts {
				if a.Address != "" {
					published++
				}
			}
		}
		for _, nd := range nodes {
			nd.Stop()
		}
		max := int64(0)
		for _, h := range hs {
			if h > max {
				max = h
			}
		}
		t.Logf("publishers=%v: heights after 6s = %v (addresses visible in node states: %d)", publishers, hs, published)
		if len(publishers) == 2 && max <= 4 {
			t.Errorf("HALT: after two of four validators published their on-chain address the network stopped at height %d (control run with no publication advances normally)", max)
		}
	}
}
