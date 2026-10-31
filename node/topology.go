package node

import "github.com/eval-apply-labs/go-ledger/ledger"

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

// Neighbors returns the gossip peers for `self` among the sorted validator names, auto-scaled to the
// agreed maxPeers. maxPeers <= 0 means all-to-all. Every node must relay received messages for a
// multi-hop topology to reach everyone.
func Neighbors(names []string, self string, maxPeers int) []string {
	participantCount := len(names)
	selfIndex := -1
	for i, name := range names {
		if name == self {
			selfIndex = i
			break
		}
	}
	if selfIndex < 0 || participantCount <= 1 {
		return nil
	}
	groupSize, dimensions := ledger.GroupParameters(participantCount, maxPeers)
	if dimensions <= 1 { // one dimension == all-to-all
		neighbors := make([]string, 0, participantCount-1)
		for i, name := range names {
			if i != selfIndex {
				neighbors = append(neighbors, name)
			}
		}
		return neighbors
	}
	// Each neighbour differs from the node in exactly one digit, so no two are the same and none is the
	// node itself; groupSize^dimensions >= participantCount, so every node has an address. Only the
	// addresses past the last participant are left out.
	selfCoordinates := indexToCoordinates(selfIndex, groupSize, dimensions)
	var neighbors []string
	for dimension := 0; dimension < dimensions; dimension++ {
		for coordinateValue := 0; coordinateValue < groupSize; coordinateValue++ {
			if coordinateValue == selfCoordinates[dimension] {
				continue
			}
			neighborCoordinates := append([]int(nil), selfCoordinates...)
			neighborCoordinates[dimension] = coordinateValue
			if j := coordinatesToIndex(neighborCoordinates, groupSize); j < participantCount {
				neighbors = append(neighbors, names[j])
			}
		}
	}
	return neighbors
}

// indexToCoordinates is a node's address in the topology: the digits of index in base groupSize,
// lowest first, dimensions of them. coordinatesToIndex turns the digits back into the index.
func indexToCoordinates(index, groupSize, dimensions int) []int {
	coordinates := make([]int, dimensions)
	for dimension := 0; dimension < dimensions; dimension++ {
		coordinates[dimension] = index % groupSize
		index /= groupSize
	}
	return coordinates
}

func coordinatesToIndex(coordinates []int, groupSize int) int {
	index, multiplier := 0, 1
	for dimension := 0; dimension < len(coordinates); dimension++ {
		index += coordinates[dimension] * multiplier
		multiplier *= groupSize
	}
	return index
}
