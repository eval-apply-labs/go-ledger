package node

import (
	"fmt"

	"github.com/eval-apply-co-uk/go-ledger/ledger"
)

// relay forwards a message received from a peer to this node's own gossip peers, but only the first
// time this node sees it. This is what lets a multi-hop topology (see topology.go) flood a message
// to every node: each node passes it on to its neighbours until everyone has it. With an all-to-all
// transport, relaying is a harmless deduplicated no-op after the first hop.
func (n *Node) relay(msg Message) {
	k := msgKey(msg)
	if k == "" || n.relaySeen[k] {
		return
	}
	n.relaySeen[k] = true
	n.transport.Broadcast(n.self, msg)
}

// msgKey is a stable identity for relay-dedup.
func msgKey(msg Message) string {
	switch msg.Type {
	case MsgProposal:
		if msg.Proposal == nil {
			return ""
		}
		return fmt.Sprintf("p:%d:%d:%x", msg.Proposal.Height, msg.Proposal.Round, msg.Proposal.Block.StateDigest)
	case MsgVote:
		if msg.Vote == nil {
			return ""
		}
		return "v:" + msg.Vote.Validator + ":" + msg.Vote.Signature
	case MsgPayment:
		if msg.Payment == nil {
			return ""
		}
		return "pay:" + ledger.PaymentID(*msg.Payment) // content, not signature (malleation-proof)
	case MsgUpdate:
		if msg.Update == nil {
			return ""
		}
		return "upd:" + ledger.UpdateID(*msg.Update)
	case MsgParameterVote:
		if msg.ParameterChange == nil {
			return ""
		}
		return changeMessageKey(*msg.ParameterChange) // proposal + its voters, so new votes re-flood
	case MsgReassignmentVote:
		if msg.Reassignment == nil {
			return ""
		}
		return reassignmentMessageKey(*msg.Reassignment)
	}
	return ""
}
