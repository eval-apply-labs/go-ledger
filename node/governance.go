package node

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// parameterChangeKey identifies a parameter change by its new parameters, the parameter state it amends
// and its governance sequence; the pool holds one certificate per change, and relay dedups on it. The
// FULL new profile is keyed (its digest), not just the cost — two changes sharing a cost but differing
// in timing or bounds are different changes and must not share a slot.
func parameterChangeKey(change ledger.ParameterChange) string {
	hasher := sha256.New()
	newParametersDigest := change.NewParameters.Digest()
	hasher.Write(newParametersDigest[:])
	hasher.Write(change.PreviousParametersDigest[:])
	hasher.Write([]byte(strconv.FormatInt(change.Sequence, 10)))
	return hex.EncodeToString(hasher.Sum(nil))
}

// maxPooledCertificates is how many complete certificates each certificate pool holds, the pool of
// parameter changes and the pool of ⅔ name actions, so nobody can fill the node's memory with them.
const maxPooledCertificates = 64

// addParameterVotes pools a submitted or gossiped parameter change, but only a complete certificate:
// one Valid against the current parameters, governance sequence and validators, its votes already
// meeting the > ⅔ quorum. Anything less is not pooled, relayed or shown: validators collect their
// votes among themselves and submit the certificate once, so nobody can fill the bounded pool with
// entries that would never enact, and a change is not public before it can apply. A later certificate
// for a pooled change replaces it, so fresh votes are not shut out by a certificate that has since
// lost voters (see pendingParameterChange). Votes of validators no longer in the set are left out
// first, as when a certificate is picked for a block, and a copy of a pooled certificate with the same
// votes, which relays bring, is dropped before any signature check. A certificate has only bounded
// fields, one vote per signer, so MaxMessageBytes does not apply to it. Returns true when it pools the
// certificate, new or replacing one — the caller passes it to relay and wakes an idle node (spec 6.6):
// a replacement may be the first certificate for the change that a block can carry. relay sends a
// change's certificate once per height (see relay), so a replacement taken at the same height is not
// sent again. Returns false and no error for a copy of a pooled certificate, and false and the reason
// when it refuses one.
func (node *Node) addParameterVotes(change ledger.ParameterChange) (bool, error) {
	validators := node.view.Load().validators
	change = currentParameterVoters(change, validators)
	key := parameterChangeKey(change)
	pooled, found := node.parameterCertificatePool[key]
	if found && slices.Equal(pooled.Votes, change.Votes) {
		return false, nil // a relayed copy: it costs no signature check
	}
	if !change.Valid(node.config.Chain.Scheme, node.config.Chain.NetworkID, validators, node.state.Parameters, node.state.GovernanceSequence) {
		return false, errors.New("it amends other parameters or another governance sequence than those in force, its new parameters break a bound, or its votes do not verify or do not reach the ⅔ quorum of the current validators")
	}
	if !found && len(node.parameterCertificatePool) >= maxPooledCertificates {
		return false, errors.New("the pool of parameter-change certificates is full")
	}
	node.parameterCertificatePool[key] = &change
	return true, nil
}

// pendingParameterChange returns a pooled certificate that is still valid against the CURRENT
// committed parameters and validators, ready to carry in a proposed block — or nil. The votes of
// validators that have left the set since the certificate was pooled are dropped first: Valid rejects
// a whole certificate for a single non-member vote, so a departed voter would otherwise block a change
// that the remaining voters still carry by quorum.
func (node *Node) pendingParameterChange() *ledger.ParameterChange {
	currentValidators := node.view.Load().validators
	// Scan in sorted-key order: map order is randomized per process. A divergent pick is not a
	// safety issue — only this node's own proposed block carries it, and every node validates the
	// block's content deterministically in ApplyBlock — but a deterministic pick keeps proposals
	// reproducible across nodes and replays when two changes reach quorum at once.
	for _, key := range slices.Sorted(maps.Keys(node.parameterCertificatePool)) {
		change := currentParameterVoters(*node.parameterCertificatePool[key], currentValidators)
		if change.Valid(node.config.Chain.Scheme, node.config.Chain.NetworkID, currentValidators, node.state.Parameters, node.state.GovernanceSequence) {
			return &change
		}
	}
	return nil
}

// currentParameterVoters returns change with only the votes of current validators.
func currentParameterVoters(change ledger.ParameterChange, validators *consensus.ValidatorSet) ledger.ParameterChange {
	votes := change.Votes
	change.Votes = nil
	for _, vote := range votes {
		if validators.Contains(vote.Validator) {
			change.Votes = append(change.Votes, vote)
		}
	}
	return change
}

// GovernanceView is a read-only snapshot of governance for an admin UI: the current parameters, the
// validator set / quorum, and the pooled certificates awaiting a block, with their vote tallies.
type GovernanceView struct {
	Parameters         ParametersView          `json:"parameters"`         // the parameters in force
	GovernanceSequence int64                   `json:"governanceSequence"` // votes must bind to this
	TotalPower         int64                   `json:"totalPower"`
	Quorum             int64                   `json:"quorum"`
	Validators         []string                `json:"validators"` // the names of the current validators
	ParameterProposals []ParameterProposalView `json:"parameterProposals"`
	Reassignments      []ReassignmentView      `json:"reassignments"`
}

// ParametersView is the ten ledger parameters as an admin page shows them, amounts in canonical form.
type ParametersView struct {
	TransactionCost   string `json:"transactionCost"`
	BlockIntervalMs   int64  `json:"blockIntervalMs"` // the governed timing profile
	StepTimeoutMs     int64  `json:"stepTimeoutMs"`
	MaxPeers          int64  `json:"maxPeers"`
	MaxMessageBytes   int64  `json:"maxMessageBytes"` // governed per-message serialized-size cap
	NewKeyFee         string `json:"newKeyFee"`
	ChangeDelayBlocks int64  `json:"changeDelayBlocks"`
	RetainBlocks      int64  `json:"retainBlocks"`
	MaxPaymentAmount  string `json:"maxPaymentAmount"`
	MaxAccountBalance string `json:"maxAccountBalance"`
}

// parametersView is the view of a set of parameters.
func parametersView(parameters ledger.LedgerParameters) ParametersView {
	return ParametersView{
		TransactionCost:   ledger.FormatAmount(parameters.TransactionCost),
		BlockIntervalMs:   parameters.BlockIntervalMs,
		StepTimeoutMs:     parameters.StepTimeoutMs,
		MaxPeers:          parameters.MaxPeers,
		MaxMessageBytes:   parameters.MaxMessageBytes,
		NewKeyFee:         ledger.FormatAmount(parameters.NewKeyFee),
		ChangeDelayBlocks: parameters.ChangeDelayBlocks,
		RetainBlocks:      parameters.RetainBlocks,
		MaxPaymentAmount:  ledger.FormatAmount(parameters.MaxPaymentAmount),
		MaxAccountBalance: ledger.FormatAmount(parameters.MaxAccountBalance),
	}
}

// ParameterProposalView is one pooled parameter-change certificate (the full proposed profile) and the
// power of its voters among the current validators.
type ParameterProposalView struct {
	NewParameters ParametersView `json:"newParameters"`
	VotedPower    int64          `json:"votedPower"`
	Reached       bool           `json:"reached"`       // votes already meet the > ⅔ quorum
	AmendsCurrent bool           `json:"amendsCurrent"` // still amends the parameters in force
	Voters        []string       `json:"voters"`        // names
}

// ReassignmentView is one pooled ⅔ name action and the power of its voters among the current
// validators. Its amounts are "" when it leaves the balance alone.
type ReassignmentView struct {
	Name           string   `json:"name"` // the name it is about
	PreviousAmount string   `json:"previousAmount"`
	NewAmount      string   `json:"newAmount"`
	NewKey         string   `json:"newKey"` // "" leaves the key
	Bank           string   `json:"bank"`   // "" leaves the bank of record; "-" sets none
	Freeze         bool     `json:"freeze"`
	Unfreeze       bool     `json:"unfreeze"`
	VotedPower     int64    `json:"votedPower"`
	Reached        bool     `json:"reached"`
	AmendsCurrent  bool     `json:"amendsCurrent"` // still fits the state in force
	Voters         []string `json:"voters"`        // names
}

// governanceView builds the read-only governance snapshot. It reads owned state, so it runs on the
// event loop (via the eventGovernanceQuery handler). It lists the pooled certificates in key order, the
// order pendingParameterChange and pendingReassignment pick from, so the list does not reshuffle.
func (node *Node) governanceView() GovernanceView {
	validators := node.view.Load().validators
	view := GovernanceView{
		Parameters:         parametersView(node.state.Parameters),
		GovernanceSequence: node.state.GovernanceSequence,
		TotalPower:         validators.TotalPower(),
		Quorum:             validators.Quorum(),
	}
	for _, validator := range validators.Validators() {
		view.Validators = append(view.Validators, validator.Name)
	}
	currentParametersDigest := node.state.Parameters.Digest()
	for _, key := range slices.Sorted(maps.Keys(node.parameterCertificatePool)) {
		change := node.parameterCertificatePool[key]
		voters := votersOfParameterChange(*change)
		votedPower := tallyVoters(validators, voters)
		view.ParameterProposals = append(view.ParameterProposals, ParameterProposalView{
			NewParameters: parametersView(change.NewParameters),
			VotedPower:    votedPower,
			Reached:       votedPower >= validators.Quorum(),
			AmendsCurrent: change.PreviousParametersDigest == currentParametersDigest,
			Voters:        voters,
		})
	}
	for _, key := range slices.Sorted(maps.Keys(node.reassignmentCertificatePool)) {
		reassignment := node.reassignmentCertificatePool[key]
		voters := votersOfReassignment(*reassignment)
		votedPower := tallyVoters(validators, voters)
		view.Reassignments = append(view.Reassignments, ReassignmentView{
			Name:           reassignment.Name,
			PreviousAmount: ledger.FormatAmount(reassignment.PreviousAmount),
			NewAmount:      ledger.FormatAmount(reassignment.NewAmount),
			NewKey:         reassignment.NewKey,
			Bank:           reassignment.Bank,
			Freeze:         reassignment.Freeze,
			Unfreeze:       reassignment.Unfreeze,
			VotedPower:     votedPower,
			Reached:        votedPower >= validators.Quorum(),
			AmendsCurrent:  reassignment.FitsState(node.state) == nil,
			Voters:         voters,
		})
	}
	return view
}

func votersOfParameterChange(change ledger.ParameterChange) []string {
	voters := make([]string, 0, len(change.Votes))
	for _, vote := range change.Votes {
		voters = append(voters, vote.Validator)
	}
	return voters
}

func votersOfReassignment(reassignment ledger.Reassignment) []string {
	voters := make([]string, 0, len(reassignment.Votes))
	for _, vote := range reassignment.Votes {
		voters = append(voters, vote.Validator)
	}
	return voters
}

// tallyVoters sums the voting power of a certificate's voters, counting only current validators.
func tallyVoters(validators *consensus.ValidatorSet, voters []string) int64 {
	var power int64
	for _, voter := range voters {
		power += validators.Power(voter)
	}
	return power
}

// pruneParameterCertificates drops the certificates that can no longer enact: those amending a
// superseded governance sequence (a governance action has since applied) or a different parameter
// state.
func (node *Node) pruneParameterCertificates() {
	currentDigest := node.state.Parameters.Digest()
	for key, change := range node.parameterCertificatePool {
		if change.Sequence != node.state.GovernanceSequence || change.PreviousParametersDigest != currentDigest {
			delete(node.parameterCertificatePool, key)
		}
	}
}
