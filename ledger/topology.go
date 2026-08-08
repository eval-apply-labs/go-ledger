package ledger

import "math"

// GroupParameters derives (groupSize k, dimension d) for the Hypernode gossip topology from the
// participant count and the agreed max-peers-per-node, choosing the smallest dimension whose fan-out
// fits maxPeers. The dimension d is how many relay hops a message takes to reach every node — the
// load-bearing link between the exchange window and the number of nodes reachable within it, so it
// lives here (in the committed-state layer) where governance can check a timing profile against it.
// Faithful to the original Hypernode setGroupParameters (throws replaced by clamps); maxPeers <= 0
// (or a tiny network) degenerates to one dimension = all-to-all.
func GroupParameters(participants, maxPeers int) (groupSize, dim int) {
	if participants <= 0 {
		return 0, 0
	}
	if maxPeers <= 0 || participants < maxPeers {
		return participants, 1 // no cap / tiny network → one dimension = all-to-all
	}
	if participants > 1_000_000_000 {
		participants = 1_000_000_000
	}
	exp := 1
	connections := (participants - 1) * 3
	for maxPeers < connections {
		exp++
		newConnections := exp * int(math.Ceil(math.Pow(float64(participants), 1.0/float64(exp))-1))
		if newConnections >= connections { // past the asymptote — more dimensions stop helping
			exp--
			break
		}
		connections = newConnections
		if exp > 60 {
			break
		}
	}
	groupSize = int(math.Ceil(math.Pow(float64(participants), 1.0/float64(exp))))
	if groupSize < 2 {
		groupSize = 2
	}
	return groupSize, exp
}
