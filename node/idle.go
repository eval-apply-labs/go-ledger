package node

import (
	"slices"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// An idle chain (spec 6.6) starts a height only when there is work, so a chain nobody uses signs,
// re-gossips and records nothing. startHeight asks hasWork right after it builds its candidate block;
// with no work it sets the node's idle field and starts no driver. wakeIfIdle starts the height once
// work arrives: it is called when a payment, update, name request or certificate is pooled, when
// onProposal takes a proposal, when a countable vote is kept, and when a late double-sign is kept as a
// proof.

// hasWork reports whether the height in progress has work. A pending proof is work only once the
// candidate block carries it: evidence the one-third cap holds back keeps nothing moving.
func (node *Node) hasWork(candidateBlock ledger.Block) bool {
	content, height := candidateBlock.Content, node.height()
	switch {
	case node.lastCommit.Height == 0: // no block committed yet: block 1 always commits, so a new wallet has a commit to pin
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
// re-gossip wait of a node back without its sign log (see waitForOwnSignatures), which keeps it from
// signing against the votes it lost before they come back.
func (node *Node) wakeIfIdle() {
	if node.idle {
		node.startHeight()
	}
}
