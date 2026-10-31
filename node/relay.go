package node

import (
	"fmt"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// relay forwards a message received from a peer to this node's own gossip peers, but only the first
// time this node sees it. This is what lets a multi-hop topology (see topology.go) flood a message
// to every node: each node passes it on to its neighbours until everyone has it. Every caller but the
// two certificate handlers already relays only what is new to the node: onProposal takes one proposal
// per round, rememberVote keeps a vote once, and the three admit functions relay only what their pool
// newly took. So the mark in relaySeen decides only for certificates: addParameterVotes and
// addReassignmentVotes also report a replacement as newly pooled, and the mark keeps it from being
// relayed again at the height. relaySeen also holds reportEquivocation's marks. With all-to-all peers
// (maxPeers 0) it is not free: every node that takes a new message sends it once to all its peers, so
// with N nodes a message travels N × (N − 1) times instead of N − 1, and each receiver decodes a copy
// before it drops it. With 21 validators, a proposal of up to 768 KiB goes out 420 times, not 20.
func (node *Node) relay(message Message) {
	key := messageKey(message)
	if key == "" || node.relaySeen[key] {
		return
	}
	node.relaySeen[key] = true
	node.transport.Broadcast(node.self, message)
}

// messageKey is a stable identity for relay-dedup.
func messageKey(message Message) string {
	switch message.Type {
	case MessageProposal:
		if message.Proposal == nil {
			return ""
		}
		// By block hash: the node relays only a proposal it took, whose block hashes to the one signed.
		signedProposal := message.Proposal.Proposal
		return fmt.Sprintf("p:%d:%d:%x", signedProposal.Height, signedProposal.Round, signedProposal.BlockHash)
	case MessageVote:
		if message.Vote == nil {
			return ""
		}
		return "v:" + message.Vote.Validator + ":" + message.Vote.Signature
	case MessagePayment:
		if message.Payment == nil {
			return ""
		}
		return "pay:" + ledger.PaymentID(*message.Payment) // content, not signature (malleation-proof)
	case MessageUpdate:
		if message.Update == nil {
			return ""
		}
		return "upd:" + ledger.UpdateID(*message.Update)
	case MessageNameRequest:
		if message.NameRequest == nil {
			return ""
		}
		return "request:" + ledger.NameRequestID(*message.NameRequest)
	case MessageParameterCertificate:
		if message.ParameterChange == nil {
			return ""
		}
		return "param:" + parameterChangeKey(*message.ParameterChange)
	case MessageReassignmentCertificate:
		if message.Reassignment == nil {
			return ""
		}
		return "reassign:" + reassignmentKey(*message.Reassignment)
	}
	return ""
}
