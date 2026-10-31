package node

// Third-pass verifier PoC for VSTR-NEW-3 (from scratch). Static 4-validator set over real HTTP, with
// snapshotPath set as docs/operating-a-network.md recommends. Phase 1 runs the old chain. Phase 2 is a
// same-id, same-keys re-genesis with one deliberate edit (a balance correction).

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type v3Key struct{ pub, priv []byte }

func v3RunHTTP(t *testing.T, network string, signers []v3Key, set *consensus.ValidatorSet, genesis []ledger.Account, interval time.Duration, snaps []string) *v3Cluster {
	t.Helper()
	n := len(signers)
	c := &v3Cluster{nodes: make([]*Node, n), servers: make([]*httptest.Server, n), urls: make([]string, n)}
	transports := make([]*HTTPTransport, n)
	for i := range signers {
		transports[i] = NewHTTPTransport(nil)
		c.nodes[i] = New(Config{NetworkID: network, Scheme: signing.Ed25519{}, Signer: signing.NewSigner(signing.Ed25519{}, network, signers[i].pub, signers[i].priv),
			Validators: set, TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: interval, Genesis: genesis, SnapshotPath: snaps[i]}, transports[i])
		c.servers[i] = httptest.NewServer(NewServer(c.nodes[i]).Handler())
		c.urls[i] = c.servers[i].URL
	}
	for i := range signers {
		var peers []string
		for j := range signers {
			if j != i {
				peers = append(peers, c.urls[j])
			}
		}
		transports[i].SetPeers(peers)
	}
	return c
}

func TestV3VSTRNEW3LeftoverSnapshotUndoesReGenesis(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newNetwork string
		staleNodes []int // nodes whose snapshotPath still holds the old chain's file
	}{
		{"all-nodes-kept-snapshot", "village", []int{0, 1, 2, 3}},
		{"one-node-kept-snapshot", "village", []int{3}},
		{"one-node-kept-snapshot-fresh-id", "village-2", []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signers := make([]v3Key, 4)
			vals := make([]consensus.Validator, 4)
			for i := range signers {
				signers[i].pub, signers[i].priv, _ = signing.Ed25519{}.GenerateKey()
				vals[i] = consensus.Validator{PublicKey: signing.NewSigner(signing.Ed25519{}, "village", signers[i].pub, signers[i].priv).PublicKeyBase64(), Power: 1}
			}
			set := consensus.NewValidatorSet(vals)
			dir := t.TempDir()
			snaps := make([]string, 4)
			for i := range snaps {
				snaps[i] = filepath.Join(dir, "snap"+string(rune('0'+i))+".json")
			}
			oldGenesis := []ledger.Account{{PublicKey: "alice", Name: "alice", Amount: rat("100"), ValidatorNode: vals[0].PublicKey}}
			for _, v := range vals {
				oldGenesis = append(oldGenesis, ledger.Account{PublicKey: v.PublicKey, Amount: rat("1000"), ValidatorNode: v.PublicKey})
			}
			// Phase 1: the old chain runs to height >= 25.
			old := v3RunHTTP(t, "village", signers, set, oldGenesis, 20*time.Millisecond, snaps)
			for _, nd := range old.nodes {
				nd.Start()
			}
			waitFor(t, 20*time.Second, func() bool {
				for _, u := range old.urls {
					if v3Height(u) < 25 {
						return false
					}
				}
				return true
			}, "old chain stalled")
			captured := v3Accounts(old.urls[0])
			oldH := v3Height(old.urls[0])
			old.stop()
			time.Sleep(200 * time.Millisecond)

			// Phase 2: re-genesis from the captured state with one edit (alice corrected 100 -> 150).
			newGenesis := append([]ledger.Account(nil), captured...)
			for i := range newGenesis {
				if newGenesis[i].PublicKey == "alice" {
					newGenesis[i].Amount = rat("150")
				}
			}
			stale := map[int]bool{}
			for _, i := range tc.staleNodes {
				stale[i] = true
			}
			for i := range snaps {
				if !stale[i] {
					os.Remove(snaps[i]) // this operator followed a (hypothetical) 'clear snapshotPath' step
				}
			}
			c := v3RunHTTP(t, tc.newNetwork, signers, set, newGenesis, 500*time.Millisecond, snaps)
			defer c.stop()
			startHeights := make([]int64, 4)
			for i, nd := range c.nodes {
				startHeights[i] = nd.height // set by New()/loadSnapshot; the loop is not running yet
			}
			for _, nd := range c.nodes {
				nd.Start()
			}
			time.Sleep(7 * time.Second * testTimeSlack / 4) // two-plus catch-up ticks (3 s each)
			hs := make([]int64, 4)
			alice := make([]string, 4)
			for i, u := range c.urls {
				hs[i] = v3Height(u)
				alice[i] = ledger.AccountAmount(v3Accounts(u), "alice").RatString()
			}
			t.Logf("old chain stopped at height %d; new start heights %v; after ~7s heights %v; alice per node %v (edit = 150)", oldH, startHeights, hs, alice)
		})
	}
}
