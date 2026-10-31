package node

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// syncThenStart runs in a goroutine at Start when Bootstrap peers are configured. It fetches the
// best verified checkpoint from those peers, hands it to the loop to adopt, then starts consensus.
// If nothing valid is found (peers down, or the network is still at genesis) consensus simply
// starts at the configured height. Ordering is guaranteed by the single event channel: the adopt
// event is enqueued before the start event.
func (node *Node) syncThenStart() {
	if best, found := node.bestCheckpoint(node.config.Bootstrap); found {
		node.enqueue(event{kind: eventAdopt, checkpoint: &best})
	}
	node.enqueue(event{kind: eventStart})
}

// catchUpCheckpoint is the best verified checkpoint a running node can catch up to if it falls
// behind: its bootstrap peers', or, when none of them yields one (none is configured, all are down,
// or none verifies), its current GOSSIP peers'. So any networked node can self-recover from a peer
// that is ahead, and while a bootstrap peer yields a checkpoint the gossip peers are not asked. Any
// peer is a safe source: bestCheckpoint verifies a checkpoint against the validators of the node's own
// last committed state. The gossip peers come from the transport's Peers method, which HTTPTransport
// has (and, in tests, peeredRecorder).
func (node *Node) catchUpCheckpoint() (Checkpoint, bool) {
	if checkpoint, found := node.bestCheckpoint(node.config.Bootstrap); found {
		return checkpoint, true
	}
	if peerProvider, isPeerProvider := node.transport.(interface{ Peers() []string }); isPeerProvider {
		return node.bestCheckpoint(peerProvider.Peers())
	}
	return Checkpoint{}, false
}

// catchUpLoop runs alongside the started node and periodically fetches catchUpCheckpoint, every
// catchUpInterval. If it is ahead of us — we missed a commit window — it is handed to the loop to
// fast-forward. bestCheckpoint is anchored on the committed view the loop last published (race-free in
// this goroutine); the loop re-verifies against the view it holds before adopting.
func (node *Node) catchUpLoop() {
	node.everyInterval(node.catchUpInterval, func() {
		if checkpoint, found := node.catchUpCheckpoint(); found {
			node.enqueue(event{kind: eventCatchUp, checkpoint: &checkpoint})
		}
	})
}

// catchUpInterval is the cadence of catchUpLoop: five block intervals, as the committed view governs
// them, and at least 3 s.
func (node *Node) catchUpInterval() time.Duration {
	fiveBlockIntervals := 5 * time.Duration(node.view.Load().state.Parameters.BlockIntervalMs) * time.Millisecond
	return max(fiveBlockIntervals, 3*time.Second)
}

// everyInterval calls action every interval(), until the node stops. It wakes at least once a second
// and asks interval() again, so once a ⅔ vote changes the timings, the cadence follows within a second:
// a node that ran at a day per step does not wait out the day after a vote lowers it.
func (node *Node) everyInterval(interval func() time.Duration, action func()) {
	last := time.Now()
	for {
		select {
		case <-node.quit:
			return
		case <-time.After(min(interval()-time.Since(last), time.Second)):
		}
		if time.Since(last) >= interval() {
			action()
			last = time.Now()
		}
	}
}

// reGossipLoop periodically asks the loop to re-broadcast the current height's votes and the current
// round's proposal. Both are otherwise sent once, so a partition can leave the two sides holding
// disjoint vote sets; on merge, without re-exchange, neither side reaches quorum-any and the round
// never advances — a liveness stall (never a safety break) the adversarial harness surfaced. And a
// proposal whose one broadcast is LOST used to burn its whole round timing out to nil; re-sending it
// lets the same round still decide. The cadence is reGossipInterval, the governed step timeout:
// frequent enough to unstick a merged network within a few rounds. A tick is not cheap. While a driver
// runs (without one nothing is re-gossiped), each tick sends every peer every vote the node holds for
// the height, about 4.7 KB of JSON each; the last commit's precommits and its decided proposal, block
// included (sendLastCommit); and the current round's proposal, block included. A receiver reads and
// decodes each proposal, up to 2 MiB, before it can drop a copy it holds or one of a height it has
// left. The loop's eventRegossip handler does the send (reGossipVotes, reGossipProposal).
func (node *Node) reGossipLoop() {
	node.everyInterval(node.reGossipInterval, func() { node.enqueue(event{kind: eventRegossip}) })
}

// reGossipInterval is the cadence of reGossipLoop, and half the wait of a node back without its sign
// log (see waitForOwnSignatures): the step timeout of the committed view, so it follows a ⅔ vote.
func (node *Node) reGossipInterval() time.Duration {
	milliseconds := node.view.Load().state.Parameters.StepTimeoutMs
	if milliseconds <= 0 {
		return time.Second
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// bestCheckpoint fetches a checkpoint from each source peer that is ahead of the node, keeps only those
// that verifyCheckpoint accepts with the validators of the node's last committed state as the trust
// anchor, and returns the one at the highest committed height. It asks each peer's height first, and
// fetches a checkpoint, about 2.9 KB per account, only from a peer whose height is ahead (spec 6.6): on
// an idle chain every peer is at the node's height, tick after tick. It reads only immutable config and
// the committed view the loop publishes (see publishView), so it is safe to call from a goroutine.
func (node *Node) bestCheckpoint(sources []string) (Checkpoint, bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	view := node.view.Load()
	anchor := view.validators
	var best Checkpoint
	found := false
	for _, peer := range sources {
		if peerHeight, err := fetchHeight(client, peer); err != nil || peerHeight <= view.state.Height+1 {
			continue // unreachable, or not ahead of the height in progress
		}
		checkpoint, err := fetchCheckpoint(client, peer, MaxCheckpointBytes)
		if err != nil {
			continue
		}
		// The anchor filter must stay even though adopt/catchUp re-verify against the live state in
		// the loop: without it a forged checkpoint at a higher height would win the pick below.
		if !node.verifyCheckpoint(checkpoint, anchor) {
			continue // invalid quorum, block hash mismatch, or no trusted validator vouches for the set
		}
		if !found || checkpoint.Commit.Height > best.Commit.Height {
			best, found = checkpoint, true
		}
	}
	return best, found
}

// MaxCheckpointBytes bounds a fetched checkpoint. It is generous, because an honest checkpoint holds
// every account plus a commit certificate and soon outgrows the 1 MiB intake bound, but finite: the
// body is read before anything in it is verified, so one catch-up source must not be able to make
// this node read and decode without limit.
const MaxCheckpointBytes = 64 << 20

// fetchHeight GETs the height in progress a peer's /hdls/status reports: one above its committed height.
func fetchHeight(client *http.Client, peer string) (int64, error) {
	response, err := client.Get(peer + "/hdls/status")
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, errors.New("status fetch: " + response.Status)
	}
	var status struct {
		HeightInProgress int64 `json:"heightInProgress"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(&status)
	return status.HeightInProgress, err
}

// fetchCheckpoint GETs a peer's committed checkpoint. A non-200 answer is refused, and only maxBytes of
// the body are read, so a larger one fails to decode; either way the source is skipped.
func fetchCheckpoint(client *http.Client, peer string, maxBytes int64) (Checkpoint, error) {
	response, err := client.Get(peer + "/hdls/checkpoint")
	if err != nil {
		return Checkpoint{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Checkpoint{}, errors.New("checkpoint fetch: " + response.Status)
	}
	var checkpoint Checkpoint
	if err := json.NewDecoder(io.LimitReader(response.Body, maxBytes)).Decode(&checkpoint); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// adopt fast-forwards to a verified checkpoint if it is ahead of us and consensus has not started
// yet (a fresh or rebooting node). The certificate is verified against the set it carries, which the
// node's own validators must vouch for, and must be for the block hash the header and the digest of the
// supplied state give, so we trust the ⅔ quorum, not the node that served it.
// The checkpoint is saved as the node's snapshot before the node votes on top of it, as after catchUp:
// otherwise its sign log would run ahead of a snapshot of an older height, and Open would refuse to
// restart it (see loadSignLog). Once it is on disk, the sign log of the height the node opened at is
// emptied in place, at that height: a log for the height the node now starts at would end the wait of
// a node back without one (startFirstHeight), and a log restored with an older snapshot cannot vouch
// for the heights above it.
func (node *Node) adopt(checkpoint Checkpoint) {
	if node.started {
		return
	}
	if checkpoint.Commit.Height+1 <= node.height() {
		return // not ahead of our configured/current height
	}
	if !node.verifyCheckpoint(checkpoint, node.view.Load().validators) {
		return
	}
	node.fastForward(checkpoint)
	if node.saveSnapshot() == nil {
		node.emptySignLog(node.signLog.Height)
	}
}

// catchUp lets a RUNNING node that fell behind (missed a commit window) fast-forward to a peer's
// later verified checkpoint and resume consensus at the new height, instead of staying stuck. Stale
// timers/messages from the abandoned height are dropped by their height/round guards. From a height it
// may have signed at before it lost its sign log, the node may have signed at the network's height too,
// so it waits there before it starts it (see waitForOwnSignatures), and empties its sign log at the log's
// own height, as adopt does, so that a restart during the wait waits too.
func (node *Node) catchUp(checkpoint Checkpoint) {
	if !node.started {
		return // pre-start is adopt()'s job
	}
	if checkpoint.Commit.Height+1 <= node.height() {
		return // not actually ahead of us
	}
	if !node.verifyCheckpoint(checkpoint, node.view.Load().validators) {
		return
	}
	waits := node.height() <= node.mayHaveSignedUpTo
	node.fastForward(checkpoint)
	logHeight := node.height()
	if waits {
		logHeight = node.signLog.Height
	}
	if node.saveSnapshot() == nil { // before the node votes at the new height (see adopt)
		node.emptySignLog(logHeight)
	}
	if waits {
		node.waitForOwnSignatures()
		return
	}
	node.startHeight()
}

// verifyCheckpoint reports whether a peer's checkpoint is trustworthy from THIS node's vantage, the
// trust anchor being the validator set (name, key, power) of the node's own committed state. It runs
// the four steps of spec 4.14 in order, each only if the one before passed: the shape and the hashes,
// which need no signature (Checkpoint.Consistent: the block hash recomputed from the header and the
// state's digest is the one the commit decided, and the set the checkpoint carries hashes to the
// header's); then more than 1/3 of the anchor's power vouches for it (anchorVouches), so at least one
// validator we already trust signed it; and last, the commit is a valid > 2/3 quorum of the set the
// checkpoint carries, the set that signed it. So a set padded with made-up validators fails the hashes,
// and a forged header the anchor, before any member of the set is verified. This is the BFT "skipping"
// rule: it lets a node safely adopt a checkpoint whose validator set changed while it was away, without
// trusting the serving peer. (Full rotation past the anchor would need a chain of checkpoints; for
// incremental change the anchor overlaps the signing set — the real case.)
func (node *Node) verifyCheckpoint(checkpoint Checkpoint, anchor *consensus.ValidatorSet) bool {
	signers := consensus.NewValidatorSet(checkpoint.Validators)
	return checkpoint.Consistent() && node.anchorVouches(checkpoint, signers, anchor) &&
		checkpoint.Commit.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, signers)
}

// anchorVouches reports whether names of the anchor holding more than 1/3 of its power signed the
// checkpoint's commit, each under the key the anchor holds for it. A vote counts only when it is one of
// the commit's, a precommit for its height, round and block hash, and when the set the checkpoint
// carries gives its name the anchor's key, so neither a vote for another block nor a forged set that
// gives real names keys of the forger's can borrow their power. Only a name's first such vote is
// checked, whether or not it verifies, so copies of one name's vote cost one signature check, not one
// each (spec 4.14 step 3); a commit that names a name twice fails Commit.Verify anyway.
func (node *Node) anchorVouches(checkpoint Checkpoint, signers, anchor *consensus.ValidatorSet) bool {
	commit := checkpoint.Commit
	checked := make(map[string]bool)
	var vouched int64
	for _, vote := range commit.Votes {
		anchorKey := anchor.PublicKey(vote.Validator)
		ofTheCommit := vote.Type == consensus.Precommit && vote.Height == commit.Height && vote.Round == commit.Round && vote.BlockHash == commit.BlockHash
		if anchorKey == "" || !ofTheCommit || checked[vote.Validator] || signers.PublicKey(vote.Validator) != anchorKey {
			continue
		}
		checked[vote.Validator] = true
		if vote.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, anchorKey) {
			vouched += anchor.Power(vote.Validator)
		}
	}
	return vouched*3 > anchor.TotalPower()
}
