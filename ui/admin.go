package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// governanceView mirrors the node's GET /hdls/governance response: the current parameters (including
// the timing profile), the validator set / quorum, and the pending proposals with their tallies.
type governanceView struct {
	TransactionCost    string                  `json:"transactionCost"`
	BlockIntervalMs    int64                   `json:"blockIntervalMs"`
	StepTimeoutMs      int64                   `json:"stepTimeoutMs"`
	MaxPeers           int                     `json:"maxPeers"`
	MaxMessageBytes    int                     `json:"maxMessageBytes"`
	GovernanceSequence int64                   `json:"governanceSequence"`
	TotalPower         int64                   `json:"totalPower"`
	Quorum             int64                   `json:"quorum"`
	Validators         []string                `json:"validators"`
	ParameterProposals []parameterProposalView `json:"parameterProposals"`
	Reassignments      []reassignmentView      `json:"reassignments"`
}

type parameterProposalView struct {
	NewTransactionCost string `json:"newTransactionCost"`
	NewBlockIntervalMs int64  `json:"newBlockIntervalMs"`
	NewStepTimeoutMs   int64  `json:"newStepTimeoutMs"`
	NewMaxPeers        int    `json:"newMaxPeers"`
	NewMaxMessageBytes int    `json:"newMaxMessageBytes"`
	VotedPower         int64  `json:"votedPower"`
	Reached            bool   `json:"reached"`
	AmendsCurrent      bool   `json:"amendsCurrent"`
}

type reassignmentView struct {
	Account        string `json:"account"`
	PreviousAmount string `json:"previousAmount"`
	NewAmount      string `json:"newAmount"`
	VotedPower     int64  `json:"votedPower"`
	Reached        bool   `json:"reached"`
	AmendsCurrent  bool   `json:"amendsCurrent"`
}

// Governance fetches the node's read-only governance view.
func (w *Wallet) Governance() (governanceView, error) {
	var view governanceView
	resp, err := w.client.Get(w.nodeURL + "/hdls/governance")
	if err != nil {
		return view, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return view, err
	}
	return view, nil
}

// IsValidator reports whether this wallet's key is in the current validator set — only a validator's
// vote counts toward the ⅔ quorum (a non-validator may still submit, but it will not tally).
func (w *Wallet) IsValidator(view governanceView) bool {
	for _, validator := range view.Validators {
		if validator == w.PublicKey() {
			return true
		}
	}
	return false
}

// ProposeParameters casts this wallet's vote for a new operating profile — the transaction cost,
// the timing (block interval, step timeout, gossip fan-out) and the per-message size cap. Each
// argument is a string; a blank one keeps the current value, so you can change one field or the
// whole profile. It amends the parameters currently in force and enacts once > ⅔ of validator power
// has voted for the same profile. The node rejects a profile whose step timeout is too short for
// the fan-out (the timing coupling) or whose message cap is too small to fit any payment.
func (w *Wallet) ProposeParameters(cost, blockIntervalMs, stepTimeoutMs, maxPeers, maxMessageBytes string) error {
	view, err := w.Governance()
	if err != nil {
		return err
	}
	currentCost, ok := new(big.Rat).SetString(view.TransactionCost)
	if !ok {
		return fmt.Errorf("node reported an unreadable current transaction cost %q", view.TransactionCost)
	}
	current := ledger.LedgerParameters{
		TransactionCost: currentCost,
		BlockIntervalMs: view.BlockIntervalMs,
		StepTimeoutMs:   view.StepTimeoutMs,
		MaxPeers:        view.MaxPeers,
		MaxMessageBytes: view.MaxMessageBytes,
	}
	next := current // start from the profile in force; override only the fields provided
	if cost != "" {
		amount, ok := new(big.Rat).SetString(cost)
		if !ok || amount.Sign() < 0 {
			return fmt.Errorf("invalid transaction cost %q", cost)
		}
		next.TransactionCost = amount
	}
	if blockIntervalMs != "" {
		value, err := strconv.ParseInt(blockIntervalMs, 10, 64)
		if err != nil || value < 0 {
			return fmt.Errorf("invalid block interval (ms) %q", blockIntervalMs)
		}
		next.BlockIntervalMs = value
	}
	if stepTimeoutMs != "" {
		value, err := strconv.ParseInt(stepTimeoutMs, 10, 64)
		if err != nil || value <= 0 {
			return fmt.Errorf("invalid step timeout (ms) %q", stepTimeoutMs)
		}
		next.StepTimeoutMs = value
	}
	if maxPeers != "" {
		value, err := strconv.Atoi(maxPeers)
		if err != nil || value < 0 {
			return fmt.Errorf("invalid max peers %q", maxPeers)
		}
		next.MaxPeers = value
	}
	if maxMessageBytes != "" {
		value, err := strconv.Atoi(maxMessageBytes)
		if err != nil || (value != 0 && value < ledger.MinMessageBytes) {
			return fmt.Errorf("invalid max message bytes %q — must be 0 (unbounded) or at least %d", maxMessageBytes, ledger.MinMessageBytes)
		}
		next.MaxMessageBytes = value
	}
	// The window and the fan-out are coupled — reject up front (with a clear reason) a profile the node
	// would refuse: a step timeout too short to reach every node at this fan-out.
	if !next.TimingConsistent(len(view.Validators)) {
		return fmt.Errorf("step timeout %d ms is too short for %d nodes at max-peers %d — raise the timeout or the fan-out",
			next.StepTimeoutMs, len(view.Validators), next.MaxPeers)
	}
	vote, err := ledger.SignParameterVote(w.signer, next, current.Digest(), view.GovernanceSequence)
	if err != nil {
		return err
	}
	return w.postJSON("/hdls/parameter-vote", ledger.ParameterChange{
		NewParameters:            next,
		PreviousParametersDigest: current.Digest(),
		Sequence:                 view.GovernanceSequence,
		Votes:                    []ledger.ParameterVote{vote},
	})
}

// resolveAccountKey maps a registered name to its account key via this wallet's node, falling back
// to treating the input as a raw key. Unlike payments (where the NAME is signed and the network
// resolves it), a reassignment certificate binds a raw key at vote-signing time — so this lookup is
// a per-voter convenience: each validator resolves against its own node, and the ⅔ quorum only forms
// if they all voted for the same key and starting balance, so one lying node cannot retarget it.
func (w *Wallet) resolveAccountKey(nameOrKey string) string {
	accounts, err := w.accounts()
	if err != nil {
		return nameOrKey
	}
	for _, account := range accounts {
		if account.Name != "" && account.Name == nameOrKey {
			return account.PublicKey
		}
	}
	return nameOrKey
}

// VoteReassignment casts this wallet's vote to set an account's balance to newAmount (the account
// named by a registered name or a raw public key). It binds to the account's current balance, so a
// vote agrees on the exact starting point, and enacts once > ⅔ of validator power has voted for it.
func (w *Wallet) VoteReassignment(nameOrKey, newAmount string) error {
	view, err := w.Governance() // for the current governance sequence the vote must bind to
	if err != nil {
		return err
	}
	account := w.resolveAccountKey(nameOrKey)
	if account == "" {
		return fmt.Errorf("an account (name or public key) is required")
	}
	next, ok := new(big.Rat).SetString(newAmount)
	if !ok {
		return fmt.Errorf("invalid amount %q", newAmount)
	}
	if next.Sign() < 0 {
		return fmt.Errorf("a balance cannot be negative")
	}
	previousAmount := w.balanceOf(account)
	vote, err := ledger.SignReassignmentVote(w.signer, account, previousAmount, next, view.GovernanceSequence)
	if err != nil {
		return err
	}
	return w.postJSON("/hdls/reassignment-vote", ledger.Reassignment{
		Account:        account,
		PreviousAmount: previousAmount,
		NewAmount:      next,
		Sequence:       view.GovernanceSequence,
		Votes:          []ledger.ReassignmentVote{vote},
	})
}

// balanceOf returns an account's current balance from the node, or zero if it has no entry yet.
func (w *Wallet) balanceOf(account string) *big.Rat {
	accounts, err := w.accounts()
	if err != nil {
		return new(big.Rat)
	}
	return ledger.AccountAmount(accounts, account)
}

func (w *Wallet) postJSON(path string, body any) error {
	data, _ := json.Marshal(body)
	resp, err := w.client.Post(w.nodeURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("node rejected the submission: %s", string(message))
	}
	return nil
}
