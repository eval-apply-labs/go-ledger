package node

import (
	"slices"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// An idle chain (spec 6.6) starts a height at once when there is work, and otherwise only after the
// heartbeat, so a chain nobody uses signs, re-gossips and records one empty block per IdleHeartbeat.
// startHeight asks hasWork right after it builds its candidate block; with no work it sets the node's
// idle field, starts no driver and arms the heartbeat. wakeIfIdle starts the height once work arrives:
// it is called when a payment, update, name request or certificate is pooled, when onProposal takes a
// proposal, when a countable vote is kept, and when a late double-sign is kept as a proof. When the
// heartbeat fires first, onIdleHeartbeat starts the height with no work, and its block is empty.

// IdleHeartbeat is how long a node stays idle before it starts the height anyway (spec 6.6). The node
// is idle only once the post-commit window, BlockIntervalMs, has passed, so an idle chain commits a
// block about every IdleHeartbeat + BlockIntervalMs: about every 5 minutes at the default of a second,
// longer if the ⅔ raise the window. Such a block proves the validators are alive and agree, and lets a
// wallet tell an idle chain from a stalled one. It is a constant, not a parameter the ⅔ can change, so
// a wallet can rely on it.
const IdleHeartbeat = 5 * time.Minute

// hasWork reports whether the height in progress has work. A pending proof is work only once the
// candidate block carries it: evidence the one-third cap holds back keeps nothing moving. The idle
// heartbeat counts as work at the height it fired for.
func (node *Node) hasWork(candidateBlock ledger.Block) bool {
	content, height := candidateBlock.Content, node.height()
	switch {
	case node.lastCommit.Height == 0: // no block committed yet: block 1 always commits, so a new wallet has a commit to pin
		return true
	case node.heartbeatHeight == height: // the idle heartbeat fired at this height (see onIdleHeartbeat)
		return true
	case len(content.Payments) > 0 || len(content.AccountUpdates) > 0 || len(content.NameRequests) > 0 ||
		len(content.VoteEvidence) > 0 || len(content.ProposalEvidence) > 0 || content.ParameterChange != nil || content.Reassignment != nil:
		return true // the candidate block carries something
	case node.signLog.Height == height && (len(node.signLog.Votes) > 0 || len(node.signLog.Proposals) > 0):
		return true // the node signed at this height before a restart; the empty log a commit leaves does not count
	case len(node.heightProposals) > 0 || len(node.heightVotes) > 0:
		return true // a peer's proposal or countable vote for the height has arrived
	}
	// A pending change falls due only if the chain reaches its height, and a pooled entry for a later block
	// lands only if the chain reaches that block.
	return slices.ContainsFunc(node.state.Accounts, func(account ledger.Account) bool { return account.PendingHeight != 0 }) ||
		slices.ContainsFunc(node.mempool.pending, func(payment ledger.Payment) bool { return payment.BlockID > height }) ||
		slices.ContainsFunc(node.updates.pending, func(update ledger.AccountUpdate) bool { return update.BlockID > height }) ||
		slices.ContainsFunc(node.requests.pending, func(request ledger.NameRequest) bool { return request.BlockID > height })
}

// wakeIfIdle starts the height in progress if the node is idle, now that work may have arrived. It acts
// only on the idle field, which only startHeight sets (and the wait below clears), so it never cuts short
// the two waits that keep their own timers: the post-commit window (BlockIntervalMs, see commit), and the
// wait of a node back without its sign log (see waitForOwnSignatures, ownSignaturesWait), which keeps it
// from signing against the votes it lost before they come back.
func (node *Node) wakeIfIdle() {
	if node.idle {
		node.startHeight()
	}
}

// armIdleHeartbeat arms the heartbeat of the height in progress, which startHeight has just left idle:
// IdleHeartbeat later, its timer hands the loop an eventIdleHeartbeat for this height. A node woken at
// the height and left idle again arms another; the first to fire starts the height, and the later
// ones find the node no longer idle there.
func (node *Node) armIdleHeartbeat() {
	wait := IdleHeartbeat
	if node.config.idleHeartbeat > 0 {
		wait = node.config.idleHeartbeat // a test's shorter heartbeat
	}
	height := node.height()
	time.AfterFunc(wait, func() { node.enqueue(event{kind: eventIdleHeartbeat, height: height}) })
}

// onIdleHeartbeat starts the height the heartbeat was armed for, if the node is still idle there: with
// no work its candidate is an empty block, which commits and is recorded like any other. Like
// wakeIfIdle it acts only on the idle field, so it never cuts short the post-commit window or the wait
// of a node back without its sign log, and never starts a second driver: once work has started the
// height, or a commit or a catch-up has moved the node on, it does nothing.
func (node *Node) onIdleHeartbeat(height int64) {
	if node.idle && height == node.height() {
		node.heartbeatHeight = height
		node.startHeight()
	}
}
