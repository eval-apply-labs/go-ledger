package node

import (
	"errors"
	"maps"
	"slices"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// reassignmentKey identifies a ⅔ name action by every field its votes sign (ledger.ReassignmentID), so
// two actions that differ in any of them, the new key, the bank or the freeze marks included, never
// share a slot; the pool holds one certificate per action, and relay dedups on it.
func reassignmentKey(reassignment ledger.Reassignment) string {
	return ledger.ReassignmentID(reassignment)
}

// reassignmentFits returns nil when a ⅔ name action still fits the state, every rule but its signatures
// (ledger.Reassignment.FitsState), and leaves a name seated once it applies (R9), and otherwise the rule
// it breaks: what intake checks before any signature, and what the pool keeps after each commit.
func (node *Node) reassignmentFits(reassignment ledger.Reassignment) error {
	if err := reassignment.FitsState(node.state); err != nil {
		return err
	}
	if !ledger.LeavesANameSeated(node.config.Chain, node.state, ledger.BlockContent{Reassignment: &reassignment}) {
		return errors.New("once it applies, no name would be seated (R9)")
	}
	return nil
}

// addReassignmentVotes pools a submitted or gossiped ⅔ name action, but only a complete certificate:
// one Valid against the current state, governance sequence and validators, its votes already meeting
// the > ⅔ quorum. Anything less is not pooled, relayed or shown: a seizure's target could otherwise read
// the first vote on GET /hdls/governance and move its money before the quorum forms, and anyone could
// fill the bounded pool with entries that would never enact. A copy of a pooled certificate with the
// same votes, which relays bring, is dropped before any signature check; so is an action that no
// longer fits the state or that alone would leave no name seated (R9). A later certificate for a
// pooled action replaces it, and departed validators' votes are left out, as for parameter changes
// (see addParameterVotes). It returns what addParameterVotes returns: true when it pools the
// certificate, new or replacing one, and the reason when it refuses one.
func (node *Node) addReassignmentVotes(reassignment ledger.Reassignment) (bool, error) {
	validators := node.view.Load().validators
	reassignment = currentReassignmentVoters(reassignment, validators)
	key := reassignmentKey(reassignment)
	pooled, found := node.reassignmentCertificatePool[key]
	if found && slices.Equal(pooled.Votes, reassignment.Votes) {
		return false, nil // a relayed copy: it costs no signature check
	}
	if err := node.reassignmentFits(reassignment); err != nil {
		return false, err
	}
	if !reassignment.Valid(node.config.Chain.Scheme, node.config.Chain.NetworkID, validators, node.state) {
		return false, errors.New("its new key's proof does not verify, or its votes do not verify or do not reach the ⅔ quorum of the current validators")
	}
	if !found && len(node.reassignmentCertificatePool) >= maxPooledCertificates {
		return false, errors.New("the pool of name-action certificates is full")
	}
	node.reassignmentCertificatePool[key] = &reassignment
	return true, nil
}

// pendingReassignment returns a pooled ⅔ name action that is still valid against the current state and
// validators, ready to carry in a proposed block — or nil. Votes of validators
// that have since left the set are dropped first, as in pendingParameterChange.
func (node *Node) pendingReassignment() *ledger.Reassignment {
	currentValidators := node.view.Load().validators
	// Sorted-key scan for a deterministic pick — see pendingParameterChange for why.
	for _, key := range slices.Sorted(maps.Keys(node.reassignmentCertificatePool)) {
		reassignment := currentReassignmentVoters(*node.reassignmentCertificatePool[key], currentValidators)
		if reassignment.Valid(node.config.Chain.Scheme, node.config.Chain.NetworkID, currentValidators, node.state) {
			return &reassignment
		}
	}
	return nil
}

// currentReassignmentVoters returns reassignment with only the votes of current validators.
func currentReassignmentVoters(reassignment ledger.Reassignment, validators *consensus.ValidatorSet) ledger.Reassignment {
	votes := reassignment.Votes
	reassignment.Votes = nil
	for _, vote := range votes {
		if validators.Contains(vote.Validator) {
			reassignment.Votes = append(reassignment.Votes, vote)
		}
	}
	return reassignment
}

// pruneReassignments drops the actions that can no longer enact: those that no longer fit the state,
// a governance action having moved the sequence on, the balance having moved, the new key or the bank
// having been taken or frozen, or the name's frozen mark having changed, and those that would now
// leave no name seated (R9).
func (node *Node) pruneReassignments() {
	for key, reassignment := range node.reassignmentCertificatePool {
		if node.reassignmentFits(*reassignment) != nil {
			delete(node.reassignmentCertificatePool, key)
		}
	}
}
