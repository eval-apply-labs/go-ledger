// Package consensus is the pure, testable safety core of the ledger's block agreement.
//
// It fixes the central consensus finding of the audit (A1/A4):
// a block finalised on a single ">1/2 of all stake" snapshot of one node's local gossip view,
// with no quorum intersection and no durable equivocation accountability, so a sub-1/2
// Byzantine coalition could fork the chain under a partition. Here, finalisation requires a
// >2/3 stake quorum of precommit votes for one block hash in one round. Any two such quorums
// overlap in more than 1/3 of the power, so in an honest validator while <= 1/3 of power is
// Byzantine, and in one round an honest validator never precommits two blocks. Across rounds the
// lock carries it: once a block has > 2/3 precommits in a round, the honest ones among them, more
// than 1/3 of the power, are locked on it, so no other block gets > 2/3 prevotes, nor an honest
// precommit, in a later round. So two conflicting blocks cannot both commit. Votes are signed
// through the domain-separated signing layer, and a validator that double-signs is caught as
// transferable Evidence.
//
// It holds the validator set, votes, tallying, commit certificates and equivocation evidence, and the
// round driver (driver.go): proposer selection, timeouts and prevote/precommit locking. The driver is
// pure too: the node feeds it proposals, votes and fired timeouts, and carries out the Actions it
// returns. Gossip, the real timers and the check of each proposed block (ledger.ApplyBlock) are in
// package node.
package consensus

import "errors"

var (
	// ErrWrongVote is returned when a vote does not match the vote set's height/round/type.
	ErrWrongVote = errors.New("consensus: vote is for a different height/round/type")
	// ErrNotValidator is returned when a vote is not from a member of the validator set.
	ErrNotValidator = errors.New("consensus: vote is not from a set validator")
	// ErrBadSignature is returned when a vote's signature does not verify.
	ErrBadSignature = errors.New("consensus: vote signature invalid")
	// ErrEquivocation is returned when a validator casts a second, conflicting vote.
	ErrEquivocation = errors.New("consensus: validator equivocated (double vote)")
)
