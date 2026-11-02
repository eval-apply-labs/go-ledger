// Package node ties the layers together into a running node, a validator or an observer that signs
// nothing: it owns the committed ledger state, three pools (payments, account updates, name requests),
// the complete ⅔ certificates, the pending double-sign proofs and the per-height consensus Driver, and
// drives them from a single-goroutine event loop. That actor model -- all state mutated by one loop,
// inputs delivered as events on a channel -- makes the node race-free by construction.
//
// The node is transport-agnostic: it takes a Transport to send messages to peers. Messages come in
// through its methods: SubmitProposal and SubmitVote post an event; AdmitPayment, AdmitUpdate,
// AdmitNameRequest and the two Admit*Certificate methods wait for the loop's verdict, the first three
// after their checks have run off the loop against the committed view. ImportBundle hands each message
// of a bundle to one of these (bundle.go). Snapshot, GovernanceView and ExportBundle read through the
// loop. The HTTP layer (httptransport.go) is a thin adapter over these; /hdls/status, /hdls/resolve and
// the record routes read the committed view or the record files without the loop.
package node

import (
	"slices"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// MessageType tags a wire message.
type MessageType uint8

const (
	MessageProposal MessageType = iota
	MessageVote
	MessagePayment
	MessageUpdate
	MessageParameterCertificate
	MessageReassignmentCertificate
	MessageNameRequest
)

// ProposalMessage carries a signed proposal and the block it proposes (spec 4.11). The signature covers
// the block's hash, and so the whole block: a node takes the message only if the proposal names the
// round's proposer, verifies under the key the validator set holds for that name, and its block hashes
// to the signed block hash (see Node.onProposal), so an unauthenticated machine can neither front-run
// the proposer to stall the round nor attach another body to a genuine proposal (E15).
type ProposalMessage struct {
	Proposal consensus.SignedProposal
	Block    ledger.Block
}

// Message is what nodes exchange: a proposal, a (self-contained, signed) vote, or a gossiped payment,
// update, name request or certificate (so it reaches whichever node proposes the block it names).
type Message struct {
	Type            MessageType
	Proposal        *ProposalMessage
	Vote            *consensus.Vote
	Payment         *ledger.Payment
	Update          *ledger.AccountUpdate
	NameRequest     *ledger.NameRequest
	ParameterChange *ledger.ParameterChange
	Reassignment    *ledger.Reassignment
}

// gossipRoute is the last segment of the gossip route that takes message, under /hdls/, and the body
// that route takes: what HTTPTransport posts, and what a bundle's line holds (EncodeBundle). It is ""
// for a message of no known type.
func gossipRoute(message Message) (routeName string, body any) {
	switch message.Type {
	case MessageProposal:
		return "proposal", message.Proposal
	case MessageVote:
		return "vote", message.Vote
	case MessagePayment:
		return "payment", message.Payment
	case MessageUpdate:
		return "update", message.Update
	case MessageNameRequest:
		return "name-request", message.NameRequest
	case MessageParameterCertificate:
		return "parameter-vote", message.ParameterChange
	case MessageReassignmentCertificate:
		return "reassignment-vote", message.Reassignment
	}
	return "", nil
}

// Transport sends a message to a node's peers. Implementations MUST deliver asynchronously (not
// block the caller), because Broadcast is called from the node's event loop. In tests it is also
// implemented by the in-memory networks: bus (node_test.go), chaosNetwork and dropFirstProposalSend
// (chaos_test.go), restartNetwork, powerCut and peeredRecorder (restart_test.go), recordingTransport
// (catchup_test.go) and hyperBus (topology_test.go).
type Transport interface { // implemented by HTTPTransport (httptransport.go)
	Broadcast(from string, message Message)
}

// Checkpoint is the committed state a node hands to a joining or rebooting peer (spec 4.14): the state
// after the last committed block, that block's header, the commit certificate that finalised it, and
// the set that signed the commit (name, key and power). The set is carried in both modes, since keys
// live in the state and the set that signed a block cannot be told from the state the block produced.
// It is not signed, but the block hash the commit signs covers its hash, so a checker that trusts the
// commit trusts the set (see Consistent and Node.verifyCheckpoint): it trusts the ⅔ quorum's
// signatures rather than the node that served the checkpoint.
type Checkpoint struct {
	State      ledger.LedgerState
	LastBlock  ledger.BlockHeader
	Commit     consensus.Commit
	Validators []consensus.Validator
}

// Consistent reports whether the checkpoint passes the first two steps of its check (spec 4.14), which
// need no signature. Its shape: the state, the header and the commit are of one height, the state's
// names are strictly ascending (the digest hashes them in that order, and lookups by name rely on it),
// and the validators are sorted by name, distinct, each with power. Its hashes: the validators hash to
// the header's ValidatorsHash, and the block hash recomputed from the header and the state's digest is
// the one the commit decided. The commit's signatures are the caller's to check.
func (checkpoint Checkpoint) Consistent() bool {
	state, header := checkpoint.State, checkpoint.LastBlock
	return state.Height == header.Height && header.Height == checkpoint.Commit.Height && state.NamesAscending() &&
		slices.Equal(consensus.NewValidatorSet(checkpoint.Validators).Validators(), checkpoint.Validators) &&
		ledger.ValidatorsHash(checkpoint.Validators) == header.ValidatorsHash &&
		header.BlockHash(state.Digest()) == checkpoint.Commit.BlockHash
}
