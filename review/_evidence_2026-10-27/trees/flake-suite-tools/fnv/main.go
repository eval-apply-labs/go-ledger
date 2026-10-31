package main

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
)

func seed(height int64, round int32) uint64 {
	var buf [12]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(height))
	binary.BigEndian.PutUint32(buf[8:12], uint32(round))
	h := fnv.New64a()
	_, _ = h.Write(buf[:])
	return h.Sum64()
}

func main() {
	for _, c := range [][2]uint64{{3, 1}, {3, 100}, {3, 1000}, {4, 1}, {4, 100}, {5, 100}, {7, 100}, {10, 1}, {10, 100}} {
		n, p := c[0], c[1]
		same := 0
		maxRun, run := 1, 1
		const H = 100000
		for h := int64(1); h < H; h++ {
			a := (seed(h, 0) % (n * p)) / p
			b := (seed(h+1, 0) % (n * p)) / p
			if a == b {
				same++
				run++
				if run > maxRun {
					maxRun = run
				}
			} else {
				run = 1
			}
		}
		fmt.Printf("n=%d power=%d: P(same round-0 proposer at H,H+1)=%.3f (fair=%.3f) longest run=%d\n", n, p, float64(same)/H, 1/float64(n), maxRun)
	}
}
