package node

// Standalone network-level impact of non-height-scoped timeouts (scratch copy only): a 4-validator
// in-memory network whose messages take up to 150ms (a multi-hop / slow-LAN proposal fan-out well
// inside the 1s governed step timeout). Records how many heights needed round >= 1. MEASUREMENT ONLY
// (always passes): compare the logged numbers across the pristine copy (flake-suite), the window-only
// fix (flake-suite-fix-windowonly) and the full fix (flake-suite-fix).

import (
	"sort"
	"testing"
	"time"
)

func TestPoCStaleTimersUnderSlowFanout(t *testing.T) {
	c := newChaosCluster(t, 4, 42, nil, nil, func(i int, cfg *Config) {
		cfg.Timeout = 1 * time.Second
		cfg.MinBlockInterval = 100 * time.Millisecond
	})
	c.net.setFaults(0, 0, 150*time.Millisecond)
	c.start()
	defer c.finish()
	roundOf := map[int64]int32{}
	began := time.Now()
	for time.Since(began) < 15*time.Second {
		for _, nd := range c.nodes {
			s := nd.Snapshot()
			if s.LastCommit.Height > 0 {
				if _, ok := roundOf[s.LastCommit.Height]; !ok {
					roundOf[s.LastCommit.Height] = s.LastCommit.Round
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	var hs []int64
	later := 0
	for h, r := range roundOf {
		hs = append(hs, h)
		if r > 0 {
			later++
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] < hs[j] })
	t.Logf("15s, 4 validators, delay<=150ms, timeout 1s: committed %d heights, %d needed round >= 1", len(hs), later)
}
