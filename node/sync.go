package node

import (
	"encoding/json"
	"net/http"
	"time"
)

// syncThenStart runs in a goroutine at Start when Bootstrap peers are configured. It fetches the
// best verified checkpoint from those peers, hands it to the loop to adopt, then starts consensus.
// If nothing valid is found (peers down, or the network is still at genesis) consensus simply
// starts at the configured height. Ordering is guaranteed by the single event channel: the adopt
// event is enqueued before the start event.
func (n *Node) syncThenStart() {
	if best, ok := n.bestCheckpoint(n.cfg.Bootstrap); ok {
		n.enqueue(event{kind: evAdopt, checkpoint: &best})
	}
	n.enqueue(event{kind: evStart})
}

// catchUpSources are the peers a running node polls to catch up if it falls behind: its configured
// bootstrap peers if any, otherwise its current GOSSIP peers. So any networked node can self-recover
// from a peer that is ahead — not only a node that happened to be given bootstrap peers.
func (n *Node) catchUpSources() []string {
	if len(n.cfg.Bootstrap) > 0 {
		return n.cfg.Bootstrap
	}
	if peerProvider, ok := n.transport.(interface{ Peers() []string }); ok {
		return peerProvider.Peers()
	}
	return nil
}

// catchUpLoop runs alongside the started node and periodically asks its catch-up sources for their
// latest checkpoint. If one is ahead of us — we missed a commit window — it is handed to the loop to
// fast-forward. bestCheckpoint is genesis-anchored (race-free in this goroutine); the loop re-verifies
// against live state before adopting.
func (n *Node) catchUpLoop() {
	interval := 3 * time.Second
	if d := 5 * n.cfg.MinBlockInterval; d > interval {
		interval = d
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
			if cp, ok := n.bestCheckpoint(n.catchUpSources()); ok {
				n.enqueue(event{kind: evCatchUp, checkpoint: &cp})
			}
		}
	}
}

// reGossipLoop periodically asks the loop to re-broadcast the current height's votes and the current
// round's proposal. Both are otherwise sent once, so a partition can leave the two sides holding
// disjoint vote sets; on merge, without re-exchange, neither side reaches quorum-any and the round
// never advances — a liveness stall (never a safety break) the adversarial harness surfaced. And a
// proposal whose one broadcast is LOST used to burn its whole round timing out to nil; re-sending it
// lets the same round still decide. The cadence is the configured base step timeout (immutable, so
// this goroutine needs no loop state), which is the right order of magnitude: frequent enough to
// unstick a merged network within a few rounds, not so frequent it floods a healthy one (votes are
// tiny; the proposal is deduped at every receiver). The loop's evRegossip handler does the send.
func (n *Node) reGossipLoop() {
	interval := n.cfg.Timeout
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
			n.enqueue(event{kind: evRegossip})
		}
	}
}

// bestCheckpoint fetches a checkpoint from each source peer, keeps only those whose Commit
// certificate verifies against the validator set and binds the digest of the supplied accounts,
// and returns the one at the highest committed height. It reads only immutable config, so it is
// safe to call from a goroutine.
func (n *Node) bestCheckpoint(sources []string) (Checkpoint, bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	var best Checkpoint
	found := false
	for _, peer := range sources {
		cp, err := fetchCheckpoint(client, peer)
		if err != nil {
			continue
		}
		// Anchor trust on genesis here (immutable → race-free in this goroutine); adopt re-verifies
		// against the live state in the loop before it takes effect.
		if !n.verifyCheckpoint(cp, n.cfg.Genesis) {
			continue // invalid quorum, digest mismatch, or no trusted validator vouches for the set
		}
		if !found || cp.Commit.Height > best.Commit.Height {
			best, found = cp, true
		}
	}
	return best, found
}

func fetchCheckpoint(client *http.Client, peer string) (Checkpoint, error) {
	resp, err := client.Get(peer + "/hdls/checkpoint")
	if err != nil {
		return Checkpoint{}, err
	}
	defer resp.Body.Close()
	var cp Checkpoint
	if err := json.NewDecoder(resp.Body).Decode(&cp); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}
