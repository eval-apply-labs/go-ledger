package node

import (
	"testing"
	"time"
)

// Throughput probe: 4 equal validators with snapshots on, no faults, 5 s of wall time.
func TestV3BlockRateProbe(t *testing.T) {
	n := newV3Net(t, []int64{3, 3, 3, 1}, true)
	defer n.shutdown()
	var nodes []*Node
	for _, k := range n.keys {
		nodes = append(nodes, n.boot(k, nil))
	}
	for _, nd := range nodes {
		nd.Start()
	}
	time.Sleep(5 * time.Second)
	t.Logf("heights after 5s: %d %d %d %d (tmpdir=%s)", nodes[0].Snapshot().Height, nodes[1].Snapshot().Height, nodes[2].Snapshot().Height, nodes[3].Snapshot().Height, t.TempDir())
}
