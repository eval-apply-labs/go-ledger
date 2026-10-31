package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// PoC (critic, round 2): with maxPeers > 0 and URLs on every validator, load() REPLACES the operator's
// `peers` list with Neighbors(validators, self). For a node whose own key is not in `validators` (a
// non-validator reference/"light" node, or a key typo) Neighbors returns nil, so the node starts with
// ZERO gossip peers: it still admits payments and signs receipts, but relays them to nobody.
func TestPoCCriticNonValidatorWithMaxPeersGetsNoPeers(t *testing.T) {
	c := baseConfig(t) // this node's key is the sole entry of validators
	others := []map[string]any{}
	for i := 0; i < 4; i++ {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		others = append(others, map[string]any{"publicKey": base64.StdEncoding.EncodeToString(pub), "power": 1, "url": "http://10.0.0." + string(rune('2'+i)) + ":8080"})
	}
	c["validators"] = others // this node is NOT among them (a non-validator node)
	c["maxPeers"] = 2
	c["timeoutMs"] = 3000
	c["peers"] = []string{"http://10.0.0.2:8080", "http://10.0.0.3:8080"}
	_, _, peers, err := load(writeConfig(t, c))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("configured peers=%v, effective peers=%v", c["peers"], peers)
	if len(peers) == 0 {
		t.Errorf("ISOLATED: the configured peers were discarded; a non-validator node with maxPeers>0 starts with no gossip peers")
	}
}
