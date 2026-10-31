package node

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// S4: total outage right after EVERY validator precommitted D1 at H but before anyone received a
// quorum of precommits (nobody decided). After the reboot: does the network still make progress?
// (Unfixed: it re-decides an arbitrary block at H — safe, as nobody decided D1. A fix that restores
// only the lock digest, without the block, would leave everyone locked on a body nobody holds.)
func TestV3S4TotalOutageNobodyDecided(t *testing.T) {
	n := newV3Net(t, []int64{1, 1, 1, 1}, true)
	defer n.shutdown()
	H := int64(4)
	var phase atomic.Int32
	phase.Store(1)
	sw := map[string]*crashSwitch{}
	for _, k := range n.keys {
		sw[k] = newCrashSwitch()
	}
	n.setAllow(func(from, to string, m Message) bool {
		if phase.Load() != 1 {
			return true
		}
		if !sw[from].passesFrom(m) || sw[to].down.Load() {
			return false
		}
		if m.Type == MsgVote && m.Vote.Height == H && m.Vote.Type == consensus.Precommit {
			return false // every precommit at H is still in flight when the power dies
		}
		return true
	})
	var nodes []*Node
	for _, k := range n.keys {
		k := k
		nodes = append(nodes, n.boot(k, sw[k].hook(func(m Message) bool { return isOwnVote(m, k, H, consensus.Precommit, true) })))
	}
	n.pollCommits()
	for _, nd := range nodes {
		nd.Start()
	}
	waitFor(t, 20*time.Second, func() bool { return nodes[0].Snapshot().Height >= H-1 }, "no progress to H-1")
	nodes[0].SubmitPayment(n.pay(H, "5"))
	for _, k := range n.keys {
		select {
		case <-sw[k].fired:
		case <-time.After(30 * time.Second * testTimeSlack):
			t.Fatal("a validator never precommitted non-nil at H")
		}
	}
	for _, nd := range nodes {
		nd.Stop()
	}
	n.mu.Lock()
	n.nodes = map[string]*Node{}
	n.mu.Unlock()
	phase.Store(2)
	var re []*Node
	for _, k := range n.keys {
		re = append(re, n.boot(k, nil))
	}
	t.Logf("all rebooted at height %d", re[0].height)
	for _, nd := range re {
		nd.Start()
	}
	dl := time.Now().Add(15 * time.Second)
	for time.Now().Before(dl) && re[0].Snapshot().Height <= H+1 {
		time.Sleep(50 * time.Millisecond)
	}
	at := n.commitsAt(H)
	for d, r := range at {
		t.Logf("H=%d committed %x.. round %d", H, d[:4], r.commit.Round)
	}
	t.Logf("15s after reboot: heights %d %d %d %d", re[0].Snapshot().Height, re[1].Snapshot().Height, re[2].Snapshot().Height, re[3].Snapshot().Height)
	if re[0].Snapshot().Height <= H {
		t.Errorf("HALT at height %d after a total outage", H)
	}
	if len(at) > 1 {
		t.Errorf("FORK at %d", H)
	}
}
