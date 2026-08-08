// Package consensus is the pure, testable safety core of the ledger's block agreement.
//
// It fixes the central consensus finding of the audit (A1/A4):
// a block finalised on a single ">1/2 of all stake" snapshot of one node's local gossip view,
// with no quorum intersection and no durable equivocation accountability, so a sub-1/2
// Byzantine coalition could fork the chain under a partition. Here, finalisation requires a
// >2/3 stake quorum of precommit votes for one block digest, which guarantees any two quorums
// overlap in an honest validator -- so two conflicting blocks cannot both commit while <= 1/3
// of power is Byzantine. Votes are signed through the domain-separated signing layer, and a
// validator that double-signs is caught as transferable Evidence.
//
// This package is the safety core only: validator set, votes, tallying, commit certificates,
// and equivocation evidence -- all pure and unit-tested. The round driver (proposer selection,
// timeouts, prevote/precommit locking) and gossip live in the networked layer above it.
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
