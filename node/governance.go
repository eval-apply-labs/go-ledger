package node

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// proposalKey identifies a parameter-change proposal by its new parameters and the parameter state it
// amends, so votes for the same proposal accumulate together in the pool. The FULL proposed profile
// is keyed (its digest), not just the cost — two proposals sharing a cost but differing in timing or
// bounds must pool separately, or the first to arrive would squat the slot and block the other's votes.
func proposalKey(change ledger.ParameterChange) string {
	hasher := sha256.New()
	newParametersDigest := change.NewParameters.Digest()
	hasher.Write(newParametersDigest[:])
	hasher.Write(change.PreviousParametersDigest[:])
	hasher.Write([]byte(strconv.FormatInt(change.Sequence, 10)))
	return hex.EncodeToString(hasher.Sum(nil))
}

// changeMessageKey is the relay-dedup identity of a gossiped proposal: its proposal plus the set of
// voters it carries, so a message that adds new votes re-floods while an exact duplicate does not.
func changeMessageKey(change ledger.ParameterChange) string {
	voters := make([]string, 0, len(change.Votes))
	for _, vote := range change.Votes {
		voters = append(voters, vote.Validator)
	}
	sort.Strings(voters)
	return "param:" + proposalKey(change) + ":" + strings.Join(voters, ",")
}

// addParameterVotes merges the votes carried by a submitted/gossiped proposal into the pool (dedup by
// validator, verifying each signature against the current validators). Returns true if any new vote
// was added — the caller gossips it onward, so votes converge across the network.
func (n *Node) addParameterVotes(change ledger.ParameterChange) bool {
	currentValidators := n.validatorsFor(n.accounts)
	if !change.NewParameters.Acceptable(len(currentValidators.Validators())) {
		return false // never pool or gossip a profile that could never enact (e.g. window too short)
	}
	key := proposalKey(change)
	pooled, exists := n.paramProposals[key]
	if !exists {
		if len(n.paramProposals) >= 64 {
			return false // bound the pool
		}
		pooled = &ledger.ParameterChange{NewParameters: change.NewParameters, PreviousParametersDigest: change.PreviousParametersDigest, Sequence: change.Sequence}
		n.paramProposals[key] = pooled
	}
	alreadyVoted := make(map[string]bool, len(pooled.Votes))
	for _, vote := range pooled.Votes {
		alreadyVoted[vote.Validator] = true
	}
	added := false
	for _, vote := range change.Votes {
		if alreadyVoted[vote.Validator] || !currentValidators.Contains(vote.Validator) {
			continue
		}
		if !ledger.VerifyParameterVote(n.cfg.Scheme, n.cfg.NetworkID, change.NewParameters, change.PreviousParametersDigest, change.Sequence, vote) {
			continue
		}
		pooled.Votes = append(pooled.Votes, vote)
		alreadyVoted[vote.Validator] = true
		added = true
	}
	return added
}

// pendingParameterChange returns a pooled proposal that has reached the > ⅔ quorum and amends the
// CURRENT committed parameters, ready to carry in a proposed block — or nil.
func (n *Node) pendingParameterChange() *ledger.ParameterChange {
	currentDigest := n.parameters.Digest()
	currentValidators := n.validatorsFor(n.accounts)
	// Scan in sorted-key order: map order is randomized per process. A divergent pick is not a
	// safety issue — only this node's own proposed block carries it, and every node validates the
	// block's content deterministically in ApplyBlock — but a deterministic pick keeps proposals
	// reproducible across nodes and replays when two proposals reach quorum at once.
	keys := make([]string, 0, len(n.paramProposals))
	for key := range n.paramProposals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		change := n.paramProposals[key]
		if change.Sequence == n.governanceSequence && change.PreviousParametersDigest == currentDigest &&
			change.Valid(n.cfg.Scheme, n.cfg.NetworkID, currentValidators, n.parameters, n.governanceSequence) {
			return change
		}
	}
	return nil
}

// GovernanceView is a read-only snapshot of governance for an admin UI: the current parameters, the
// validator set / quorum, and the pending proposals with their vote tallies.
type GovernanceView struct {
	TransactionCost    string                  `json:"transactionCost"`
	BlockIntervalMs    int64                   `json:"blockIntervalMs"` // the governed timing profile
	StepTimeoutMs      int64                   `json:"stepTimeoutMs"`
	MaxPeers           int                     `json:"maxPeers"`
	MaxMessageBytes    int                     `json:"maxMessageBytes"`    // governed per-message serialized-size cap
	GovernanceSequence int64                   `json:"governanceSequence"` // votes must bind to this
	TotalPower         int64                   `json:"totalPower"`
	Quorum             int64                   `json:"quorum"`
	Validators         []string                `json:"validators"`
	ParameterProposals []ParameterProposalView `json:"parameterProposals"`
	Reassignments      []ReassignmentView      `json:"reassignments"`
}

// ParameterProposalView is one pending parameter-change proposal (the full proposed profile) and how
// close it is to ratifying.
type ParameterProposalView struct {
	NewTransactionCost string   `json:"newTransactionCost"`
	NewBlockIntervalMs int64    `json:"newBlockIntervalMs"`
	NewStepTimeoutMs   int64    `json:"newStepTimeoutMs"`
	NewMaxPeers        int      `json:"newMaxPeers"`
	NewMaxMessageBytes int      `json:"newMaxMessageBytes"`
	VotedPower         int64    `json:"votedPower"`
	Reached            bool     `json:"reached"`       // votes already meet the > ⅔ quorum
	AmendsCurrent      bool     `json:"amendsCurrent"` // still amends the parameters in force
	Voters             []string `json:"voters"`
}

// ReassignmentView is one pending balance-reassignment proposal and how close it is to ratifying.
type ReassignmentView struct {
	Account        string   `json:"account"`
	PreviousAmount string   `json:"previousAmount"`
	NewAmount      string   `json:"newAmount"`
	VotedPower     int64    `json:"votedPower"`
	Reached        bool     `json:"reached"`
	AmendsCurrent  bool     `json:"amendsCurrent"` // still amends the balance in force
	Voters         []string `json:"voters"`
}

// governanceView builds the read-only governance snapshot. It reads owned state, so it runs on the
// event loop (via the evGovernanceQuery handler).
func (n *Node) governanceView() GovernanceView {
	validators := n.validatorsFor(n.accounts)
	view := GovernanceView{
		BlockIntervalMs:    n.parameters.BlockIntervalMs,
		StepTimeoutMs:      n.parameters.StepTimeoutMs,
		MaxPeers:           n.parameters.MaxPeers,
		MaxMessageBytes:    n.parameters.MaxMessageBytes,
		GovernanceSequence: n.governanceSequence,
		TotalPower:         validators.TotalPower(),
		Quorum:             validators.Quorum(),
	}
	if n.parameters.TransactionCost != nil {
		view.TransactionCost = n.parameters.TransactionCost.RatString()
	}
	for _, validator := range validators.Validators() {
		view.Validators = append(view.Validators, validator.PublicKey)
	}
	currentParametersDigest := n.parameters.Digest()
	for _, change := range n.paramProposals {
		votedPower, voters := tallyVoters(validators, voterKeysOfParameterChange(*change))
		newCost := ""
		if change.NewParameters.TransactionCost != nil {
			newCost = change.NewParameters.TransactionCost.RatString()
		}
		view.ParameterProposals = append(view.ParameterProposals, ParameterProposalView{
			NewTransactionCost: newCost,
			NewBlockIntervalMs: change.NewParameters.BlockIntervalMs,
			NewStepTimeoutMs:   change.NewParameters.StepTimeoutMs,
			NewMaxPeers:        change.NewParameters.MaxPeers,
			NewMaxMessageBytes: change.NewParameters.MaxMessageBytes,
			VotedPower:         votedPower,
			Reached:            votedPower >= validators.Quorum(),
			AmendsCurrent:      change.PreviousParametersDigest == currentParametersDigest,
			Voters:             voters,
		})
	}
	for _, reassignment := range n.reassignmentProposals {
		votedPower, voters := tallyVoters(validators, voterKeysOfReassignment(*reassignment))
		previous, next := "", ""
		if reassignment.PreviousAmount != nil {
			previous = reassignment.PreviousAmount.RatString()
		}
		if reassignment.NewAmount != nil {
			next = reassignment.NewAmount.RatString()
		}
		amendsCurrent := reassignment.PreviousAmount != nil &&
			ledger.AccountAmount(n.accounts, reassignment.Account).Cmp(reassignment.PreviousAmount) == 0
		view.Reassignments = append(view.Reassignments, ReassignmentView{
			Account:        reassignment.Account,
			PreviousAmount: previous,
			NewAmount:      next,
			VotedPower:     votedPower,
			Reached:        votedPower >= validators.Quorum(),
			AmendsCurrent:  amendsCurrent,
			Voters:         voters,
		})
	}
	return view
}

func voterKeysOfParameterChange(change ledger.ParameterChange) []string {
	keys := make([]string, 0, len(change.Votes))
	for _, vote := range change.Votes {
		keys = append(keys, vote.Validator)
	}
	return keys
}

func voterKeysOfReassignment(reassignment ledger.Reassignment) []string {
	keys := make([]string, 0, len(reassignment.Votes))
	for _, vote := range reassignment.Votes {
		keys = append(keys, vote.Validator)
	}
	return keys
}

// tallyVoters sums the voting power of a proposal's voters (counting only current validators) and
// returns that power with the voter list.
func tallyVoters(validators *consensus.ValidatorSet, voters []string) (int64, []string) {
	var power int64
	for _, voter := range voters {
		power += validators.Power(voter)
	}
	return power, voters
}

// pruneParameterProposals drops proposals that can no longer enact: those amending a superseded
// governance sequence (a governance action has since applied) or a different parameter state.
func (n *Node) pruneParameterProposals() {
	currentDigest := n.parameters.Digest()
	for key, change := range n.paramProposals {
		if change.Sequence != n.governanceSequence || change.PreviousParametersDigest != currentDigest {
			delete(n.paramProposals, key)
		}
	}
}
