package main

import (
	"fmt"
	"math"
)

// verbatim copy of ledger.GroupParameters
func GroupParameters(participants, maxPeers int) (groupSize, dim int) {
	if participants <= 0 {
		return 0, 0
	}
	if maxPeers <= 0 || participants < maxPeers {
		return participants, 1
	}
	if participants > 1_000_000_000 {
		participants = 1_000_000_000
	}
	exp := 1
	connections := (participants - 1) * 3
	for maxPeers < connections {
		exp++
		newConnections := exp * int(math.Ceil(math.Pow(float64(participants), 1.0/float64(exp))-1))
		if newConnections >= connections {
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

func main() {
	for n := 1; n <= 20000; n++ {
		for mp := 0; mp <= 64; mp++ {
			k, d := GroupParameters(n, mp)
			fmt.Println(n, mp, k, d)
		}
	}
}
