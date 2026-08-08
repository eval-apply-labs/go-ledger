package node

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// reassignmentKey identifies a reassignment proposal by the account and the balances it moves
// between, so votes for the same proposal accumulate together in the pool.
func reassignmentKey(reassignment ledger.Reassignment) string {
	hasher := sha256.New()
	hasher.Write([]byte(reassignment.Account))
	var previous, next string
	if reassignment.PreviousAmount != nil {
		previous = reassignment.PreviousAmount.RatString()
	}
	if reassignment.NewAmount != nil {
		next = reassignment.NewAmount.RatString()
	}
	hasher.Write([]byte("\x00" + previous + "\x00" + next + "\x00" + strconv.FormatInt(reassignment.Sequence, 10)))
	return hex.EncodeToString(hasher.Sum(nil))
}

// reassignmentMessageKey is the relay-dedup identity of a gossiped reassignment: its proposal plus
// the voters it carries, so a message that adds new votes re-floods while an exact duplicate doesn't.
func reassignmentMessageKey(reassignment ledger.Reassignment) string {
	voters := make([]string, 0, len(reassignment.Votes))
	for _, vote := range reassignment.Votes {
		voters = append(voters, vote.Validator)
	}
	sort.Strings(voters)
	return "reassign:" + reassignmentKey(reassignment) + ":" + strings.Join(voters, ",")
}

// addReassignmentVotes merges the votes carried by a submitted/gossiped reassignment into the pool
// (dedup by validator, verifying each signature against the current validators). Returns true if any
// new vote was added — the caller gossips it onward, so votes converge across the network.
func (n *Node) addReassignmentVotes(reassignment ledger.Reassignment) bool {
	if reassignment.PreviousAmount == nil || reassignment.NewAmount == nil {
		return false
	}
	currentValidators := n.validatorsFor(n.accounts)
	key := reassignmentKey(reassignment)
	pooled, exists := n.reassignmentProposals[key]
	if !exists {
		if len(n.reassignmentProposals) >= 64 {
			return false // bound the pool
		}
		pooled = &ledger.Reassignment{Account: reassignment.Account, PreviousAmount: reassignment.PreviousAmount, NewAmount: reassignment.NewAmount, Sequence: reassignment.Sequence}
		n.reassignmentProposals[key] = pooled
	}
	alreadyVoted := make(map[string]bool, len(pooled.Votes))
	for _, vote := range pooled.Votes {
		alreadyVoted[vote.Validator] = true
	}
	added := false
	for _, vote := range reassignment.Votes {
		if alreadyVoted[vote.Validator] || !currentValidators.Contains(vote.Validator) {
			continue
		}
		if !ledger.VerifyReassignmentVote(n.cfg.Scheme, n.cfg.NetworkID, reassignment.Account, reassignment.PreviousAmount, reassignment.NewAmount, reassignment.Sequence, vote) {
			continue
		}
		pooled.Votes = append(pooled.Votes, vote)
		alreadyVoted[vote.Validator] = true
		added = true
	}
	return added
}

// pendingReassignment returns a pooled reassignment that has reached the > ⅔ quorum and still amends
// the current balance, ready to carry in a proposed block — or nil.
func (n *Node) pendingReassignment() *ledger.Reassignment {
	currentValidators := n.validatorsFor(n.accounts)
	// Sorted-key scan for a deterministic pick — see pendingParameterChange for why.
	keys := make([]string, 0, len(n.reassignmentProposals))
	for key := range n.reassignmentProposals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reassignment := n.reassignmentProposals[key]
		if reassignment.Sequence == n.governanceSequence &&
			reassignment.Valid(n.cfg.Scheme, n.cfg.NetworkID, currentValidators, n.accounts, n.governanceSequence) {
			return reassignment
		}
	}
	return nil
}

// pruneReassignments drops proposals that can no longer enact: those amending a superseded governance
// sequence (a governance action has since applied) or a balance that has since moved.
func (n *Node) pruneReassignments() {
	for key, reassignment := range n.reassignmentProposals {
		if reassignment.Sequence != n.governanceSequence || reassignment.PreviousAmount == nil ||
			ledger.AccountAmount(n.accounts, reassignment.Account).Cmp(reassignment.PreviousAmount) != 0 {
			delete(n.reassignmentProposals, key)
		}
	}
}
