// Package node ties the layers together into a running validator: it owns the ledger state, a
// payment mempool, and the per-height consensus Driver, and drives them from a single-goroutine
// event loop. That actor model -- all state mutated by one loop, inputs delivered as events on a
// channel -- makes the node race-free by construction, which is the deliberate fix for the
// lock-free shared mutable state.
//
// The node is transport-agnostic: it takes a Transport to send messages to peers and exposes
// Submit* methods for peers/clients to deliver messages in. The HTTP layer (httptransport.go) is
// a thin adapter over these.
package node

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// MsgType tags a wire message.
type MsgType uint8

const (
	MsgProposal MsgType = iota
	MsgVote
	MsgPayment
	MsgUpdate
	MsgParameterVote
	MsgReassignmentVote
)

// ProposalMsg carries a proposed block (the full block, so peers can validate it and derive its
// digest) plus its round and proof-of-lock round. Proposer/Signature authenticate it: a proposal is
// accepted only if it is signed by the round's scheduled proposer (see Node.onProposal), so an
// unauthenticated machine cannot front-run the proposer to stall the round.
type ProposalMsg struct {
	Height     int64
	Round      int32
	ValidRound int32
	Block      ledger.Block
	Proposer   string // the proposer's public key (base64)
	Signature  string // proposer's signature over the fields, domain "proposal", network-bound
}

func proposalSignFields(height int64, round, validRound int32, digest [32]byte) [][]byte {
	return [][]byte{
		[]byte(strconv.FormatInt(height, 10)),
		[]byte(strconv.FormatInt(int64(round), 10)),
		[]byte(strconv.FormatInt(int64(validRound), 10)),
		digest[:],
	}
}

// SignProposal fills in p.Proposer/p.Signature: the proposer signs (height, round, validRound, block
// digest). Because the block's digest is signed, the block cannot be swapped without invalidating it.
func SignProposal(signer *signing.Signer, p *ProposalMsg) error {
	sig, err := signer.SignBase64(signing.DomainProposal, proposalSignFields(p.Height, p.Round, p.ValidRound, p.Block.StateDigest)...)
	if err != nil {
		return err
	}
	p.Proposer = signer.PublicKeyBase64()
	p.Signature = sig
	return nil
}

// VerifyProposal reports whether p carries a valid signature by p.Proposer over its fields (the
// caller must additionally check p.Proposer is the round's scheduled proposer).
func VerifyProposal(scheme signing.Scheme, networkID string, p ProposalMsg) bool {
	return signing.VerifyBase64(scheme, networkID, p.Proposer, signing.DomainProposal, p.Signature, proposalSignFields(p.Height, p.Round, p.ValidRound, p.Block.StateDigest)...)
}

// Message is what nodes exchange: a proposal, a (self-contained, signed) vote, or a gossiped
// payment (so a payment reaches whichever node proposes the block it names).
type Message struct {
	Type            MsgType
	From            string
	Proposal        *ProposalMsg
	Vote            *consensus.Vote
	Payment         *ledger.Payment
	Update          *ledger.AccountUpdate
	ParameterChange *ledger.ParameterChange
	Reassignment    *ledger.Reassignment
}

// Transport sends a message to a node's peers. Implementations MUST deliver asynchronously (not
// block the caller), because Broadcast is called from the node's event loop.
type Transport interface {
	Broadcast(from string, msg Message)
}

// Checkpoint is the committed state a node hands to a joining/rebooting peer: the accounts after
// the last committed block, plus the Commit certificate that finalised it. The receiver verifies
// the certificate against the validator set and checks it binds the digest of these accounts, so
// it trusts the ⅔ quorum's signatures rather than the node that served the checkpoint.
//
// Validators is the set that SIGNED the Commit (the validator set at the commit's height). With a
// fixed validator set it is redundant (the receiver knows it from config); with dynamic validators
// it lets the receiver verify a commit signed by a set that grew since the receiver's last-known
// set — checked under the "skipping" trust rule (see Node.verifyCheckpoint), not trusted blindly.
type Checkpoint struct {
	Accounts           []ledger.Account
	Parameters         ledger.LedgerParameters // committed parameters, so the digest the Commit binds recomputes
	GovernanceSequence int64                   // committed governance sequence (also under the digest)
	Commit             consensus.Commit
	Validators         []consensus.Validator
}
