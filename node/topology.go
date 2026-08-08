package node

import "github.com/eval-apply-co-uk/go-ledger/ledger"

// Gossip topology — the original "Hypernode" scheme.
//
// A node's peers are the nodes that share all but one coordinate of its address in a d-dimensional
// space (a Hamming graph). The dimension is not fixed: it auto-scales from the participant count
// and the agreed max-peers-per-node, choosing the smallest dimension whose fan-out fits (that's the
// diameter/degree trade-off). Because the graph is connected, relayed gossip (see Node.relay) still
// reaches every node in ~d hops, at a fan-out of ~d·(k−1) instead of N−1. With maxPeers <= 0 (or a
// tiny network) it degenerates to all-to-all. The set is recomputed whenever the validator set
// changes. The dimension math lives in ledger.GroupParameters, shared with the governance timing
// invariant so the window and the fan-out can't be tuned inconsistently.

// Neighbors returns the gossip peers for `self` among the sorted validator keys, auto-scaled to the
// agreed maxPeers. maxPeers <= 0 means all-to-all. Every node must relay received messages for a
// multi-hop topology to reach everyone.
func Neighbors(keys []string, self string, maxPeers int) []string {
	n := len(keys)
	idx := -1
	for i, k := range keys {
		if k == self {
			idx = i
			break
		}
	}
	if idx < 0 || n <= 1 {
		return nil
	}
	k, dim := ledger.GroupParameters(n, maxPeers)
	if dim <= 1 { // one dimension == all-to-all
		out := make([]string, 0, n-1)
		for i, key := range keys {
			if i != idx {
				out = append(out, key)
			}
		}
		return out
	}
	coord := toCoord(idx, k, dim)
	seen := make(map[int]bool)
	var out []string
	for d := 0; d < dim; d++ {
		for v := 0; v < k; v++ {
			if v == coord[d] {
				continue
			}
			c := append([]int(nil), coord...)
			c[d] = v
			j := fromCoord(c, k)
			if j >= 0 && j < n && j != idx && !seen[j] {
				seen[j] = true
				out = append(out, keys[j])
			}
		}
	}
	return out
}

// toCoord / fromCoord are the little-endian mixed-radix address <-> index mapping (the original
// calculateAddress: repeated floorMod/floorDiv by groupSize).
func toCoord(idx, k, dim int) []int {
	c := make([]int, dim)
	for d := 0; d < dim; d++ {
		c[d] = idx % k
		idx /= k
	}
	return c
}

func fromCoord(c []int, k int) int {
	idx, mul := 0, 1
	for d := 0; d < len(c); d++ {
		idx += c[d] * mul
		mul *= k
	}
	return idx
}
