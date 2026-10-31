package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Governance fetches the node's read-only governance view. The wallet does not trust its node, so it
// reads at most node.MaxCheckpointBytes of it, as verifiedState reads a checkpoint, and refuses an
// answer that is not a success.
func (wallet *Wallet) Governance() (node.GovernanceView, error) {
	var view node.GovernanceView
	response, err := wallet.client.Get(wallet.nodeURL + "/hdls/governance")
	if err != nil {
		return view, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return view, fmt.Errorf("the node answered %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, node.MaxCheckpointBytes)).Decode(&view); err != nil {
		return view, err
	}
	return view, nil
}

// A governance change is voted off the network. Each validator drafts the vote in its own wallet,
// checks exactly what it would sign, signs it, and hands the signed vote (text) to whoever collects
// them; nothing reaches a node until votes holding > ⅔ of the power are assembled into one
// certificate, which is submitted once. Nodes pool only complete certificates, so a pending seizure
// is not announced to its target before it can enact.
//
// A vote binds to a verified checkpoint (trust.go), which its page reads once: the committed state (the
// accounts, the parameters in force and the governance sequence), and the validator set with its
// powers. That set signed the last block; it is the current one unless that block moved stake (dynamic
// validators), and the node checks each certificate again when it pools and applies it.

// voteDraft is a vote as a step of the admin page shows it: what it binds to, one fact per line, and the
// certificate as text. A draft's confirmation form carries it back unchanged to be signed; a signed
// vote is handed to the other validators.
type voteDraft struct {
	Action      string // the form that signs it: /admin/parameter or /admin/reassignment
	Facts       []string
	Certificate string
	NewKeyProof bool // the new key's wallet signed its proof of possession, and no one voted yet: the validators sign it next
}

// parameterForm is a parameter draft as typed in the admin form, one text per parameter. A blank one
// keeps the value in force.
type parameterForm struct {
	TransactionCost, BlockIntervalMs, StepTimeoutMs, MaxPeers, MaxMessageBytes,
	NewKeyFee, ChangeDelayBlocks, RetainBlocks, MaxPaymentAmount, MaxAccountBalance string
}

// DraftParameters builds a vote for new parameters: the transaction cost, the timing (block interval,
// step timeout, gossip fan-out), the per-message size cap, the new-key fee, the change delay, the
// record's retention and the two caps. A blank field keeps the current value, so you can change one
// field or all ten. It amends the parameters currently in force and enacts once > ⅔ of validator power
// has signed it. The node rejects parameters it does not accept (the step timeout must cover the gossip
// fan-out, and every parameter has its bounds), so such a draft is refused here first.
func (wallet *Wallet) DraftParameters(checkpoint node.Checkpoint, form parameterForm) (voteDraft, error) {
	state := checkpoint.State
	next := state.Parameters // start from the parameters in force; override only the fields provided
	var err error
	for _, amount := range []struct {
		label, typed string
		target       **big.Rat
	}{
		{"transaction cost", form.TransactionCost, &next.TransactionCost},
		{"new key fee", form.NewKeyFee, &next.NewKeyFee},
		{"largest payment", form.MaxPaymentAmount, &next.MaxPaymentAmount},
		{"largest balance", form.MaxAccountBalance, &next.MaxAccountBalance},
	} {
		if amount.typed != "" {
			if *amount.target, err = parseAmount(amount.typed); err != nil {
				return voteDraft{}, fmt.Errorf("invalid %s %q: %w", amount.label, amount.typed, err)
			}
		}
	}
	for _, number := range []struct {
		label, typed string
		target       *int64
	}{
		{"block interval (ms)", form.BlockIntervalMs, &next.BlockIntervalMs},
		{"step timeout (ms)", form.StepTimeoutMs, &next.StepTimeoutMs},
		{"max peers", form.MaxPeers, &next.MaxPeers},
		{"max message bytes", form.MaxMessageBytes, &next.MaxMessageBytes},
		{"change delay (blocks)", form.ChangeDelayBlocks, &next.ChangeDelayBlocks},
		{"retention (blocks)", form.RetainBlocks, &next.RetainBlocks},
	} {
		if number.typed != "" {
			if *number.target, err = strconv.ParseInt(strings.TrimSpace(number.typed), 10, 64); err != nil {
				return voteDraft{}, fmt.Errorf("invalid %s %q", number.label, number.typed)
			}
		}
	}
	// Reject up front parameters the node would refuse: the step timeout must reach every node at this
	// fan-out (the window and the fan-out are coupled), the timings have a ceiling, and every other
	// parameter has its bounds.
	participants := len(checkpoint.Validators)
	if !next.TimingConsistent(participants) {
		return voteDraft{}, fmt.Errorf("the network does not accept block interval %d ms and step timeout %d ms for %d nodes at max peers %d: the step timeout must cover the gossip fan-out, max peers is at most %d, and timings have an upper bound",
			next.BlockIntervalMs, next.StepTimeoutMs, participants, next.MaxPeers, ledger.MaxPeersLimit)
	}
	if !next.Acceptable(participants) {
		return voteDraft{}, fmt.Errorf("the network does not accept these parameters: max message bytes must be 0 (unbounded) or %d to %d, the new key fee and the two caps above 0, the change delay 1 to %d blocks, the retention %d to %d blocks",
			ledger.MinMessageBytes, ledger.MaxMessageBytesLimit, ledger.MaxChangeDelayBlocks, ledger.MinRetainBlocks, ledger.MaxRetainBlocks)
	}
	change := ledger.ParameterChange{NewParameters: next, PreviousParametersDigest: state.Parameters.Digest(), Sequence: state.GovernanceSequence}
	certificate, _ := json.Marshal(change)
	return voteDraft{Action: "/admin/parameter", Facts: parameterFacts(state.Parameters, change), Certificate: string(certificate)}, nil
}

// parameterFacts are the facts a parameter vote binds: each parameter in force and its new value, and
// the governance sequence.
func parameterFacts(current ledger.LedgerParameters, change ledger.ParameterChange) []string {
	next := change.NewParameters
	return []string{
		fmt.Sprintf("transaction cost: %s → %s", ledger.FormatAmount(current.TransactionCost), ledger.FormatAmount(next.TransactionCost)),
		fmt.Sprintf("block interval: %d ms → %d ms", current.BlockIntervalMs, next.BlockIntervalMs),
		fmt.Sprintf("step timeout: %d ms → %d ms", current.StepTimeoutMs, next.StepTimeoutMs),
		fmt.Sprintf("max peers: %d → %d", current.MaxPeers, next.MaxPeers),
		fmt.Sprintf("max message bytes: %d → %d", current.MaxMessageBytes, next.MaxMessageBytes),
		fmt.Sprintf("new key fee: %s → %s", ledger.FormatAmount(current.NewKeyFee), ledger.FormatAmount(next.NewKeyFee)),
		fmt.Sprintf("change delay: %d blocks → %d blocks", current.ChangeDelayBlocks, next.ChangeDelayBlocks),
		fmt.Sprintf("retention: %d blocks → %d blocks", current.RetainBlocks, next.RetainBlocks),
		fmt.Sprintf("largest payment: %s → %s", ledger.FormatAmount(current.MaxPaymentAmount), ledger.FormatAmount(next.MaxPaymentAmount)),
		fmt.Sprintf("largest balance: %s → %s", ledger.FormatAmount(current.MaxAccountBalance), ledger.FormatAmount(next.MaxAccountBalance)),
		fmt.Sprintf("governance sequence: %d", change.Sequence),
	}
}

// nameActionForm is a ⅔ name action as typed in the admin form (spec 7.6). Every field but the name is
// optional, and a blank one leaves that part of the name as it is. Bank takes ledger.ClearField ("-")
// for none; Freeze is "", "freeze" or "unfreeze".
type nameActionForm struct {
	Name, NewAmount, NewKey, Bank, Freeze string
}

// DraftReassignment builds a vote for a ⅔ name action (spec 4.8): it may set the name's balance, give it
// a new key, set or clear its bank of record, and freeze or unfreeze it, or, for a name no account has,
// create it with a key and a balance. The vote binds the name, its current balance when it sets a new
// one, every change, and the governance sequence; it enacts once > ⅔ of validator power has signed the
// same values. An action the network would refuse is refused here first. An action with a new key needs
// that key's proof of possession, which the new key's wallet signs first (SignReassignment).
func (wallet *Wallet) DraftReassignment(checkpoint node.Checkpoint, form nameActionForm) (voteDraft, error) {
	state := checkpoint.State
	name, err := typedName(form.Name)
	if err != nil {
		return voteDraft{}, err
	}
	action := ledger.Reassignment{Name: name, Freeze: form.Freeze == "freeze", Unfreeze: form.Freeze == "unfreeze", Sequence: state.GovernanceSequence}
	if form.NewAmount != "" {
		if action.NewAmount, err = parseAmount(form.NewAmount); err != nil {
			return voteDraft{}, fmt.Errorf("invalid amount %q: %w", form.NewAmount, err)
		}
		action.PreviousAmount = ledger.AccountAmount(state.Accounts, name)
	}
	if strings.TrimSpace(form.NewKey) != "" {
		if action.NewKey, err = typedKey(form.NewKey); err != nil {
			return voteDraft{}, err
		}
	}
	if action.Bank = strings.TrimSpace(form.Bank); action.Bank != "" && action.Bank != ledger.ClearField {
		if action.Bank, err = typedName(action.Bank); err != nil {
			return voteDraft{}, fmt.Errorf("the bank: %w", err)
		}
	}
	if err := fitsStateBeforeTheProof(action, state); err != nil {
		return voteDraft{}, fmt.Errorf("the network does not accept this action: %w", err)
	}
	certificate, _ := json.Marshal(action)
	return voteDraft{Action: "/admin/reassignment", Facts: nameActionFacts(action, state), Certificate: string(certificate)}, nil
}

// fitsStateBeforeTheProof is the action's check against the state (ledger.Reassignment.FitsState), all
// but the new key's proof of possession, which the new key's wallet adds after the draft.
func fitsStateBeforeTheProof(action ledger.Reassignment, state ledger.LedgerState) error {
	if action.NewKey != "" && action.NewKeySignature == "" {
		action.NewKeySignature = "signed by the new key's wallet"
	}
	return action.FitsState(state)
}

// nameActionFacts are the facts a name action binds and acts on (spec 7.6): the name's current key
// fingerprint, balance, bank of record, frozen mark and pending change, then each change.
func nameActionFacts(action ledger.Reassignment, state ledger.LedgerState) []string {
	facts := []string{"name: " + action.Name}
	if account, exists := ledger.FindAccount(state.Accounts, action.Name); exists {
		bank := account.Bank
		if bank == "" {
			bank = "none"
		}
		facts = append(facts, "current key: "+fingerprint(account.PublicKey), "current balance: "+ledger.FormatAmount(account.Amount),
			"current bank of record: "+bank, fmt.Sprintf("frozen: %v", account.Frozen))
		if change := pendingChange(account); change != "" {
			facts = append(facts, fmt.Sprintf("pending change: %s at block %d", change, account.PendingHeight))
		} else {
			facts = append(facts, "pending change: none")
		}
	} else {
		facts = append(facts, "a new name: no account has it yet")
	}
	if action.NewAmount != nil {
		facts = append(facts, "new balance: "+ledger.FormatAmount(action.NewAmount))
	}
	if action.NewKey != "" {
		facts = append(facts, fmt.Sprintf("new key: %s (its proof of possession: %v)", fingerprint(action.NewKey), action.NewKeySignature != ""))
	}
	switch action.Bank {
	case "":
	case ledger.ClearField:
		facts = append(facts, "new bank of record: none")
	default:
		facts = append(facts, "new bank of record: "+action.Bank)
	}
	if action.Freeze {
		facts = append(facts, "freeze the name")
	}
	if action.Unfreeze {
		facts = append(facts, "unfreeze the name")
	}
	return append(facts, "any pending change is cleared", fmt.Sprintf("governance sequence: %d", action.Sequence))
}

// SignParameters signs a reviewed parameter draft exactly as drafted — see handOver. It first checks the
// draft as the node checks the certificate (ledger.ParameterChange.Valid): it must amend the parameters
// in force, at the governance sequence, with values the network accepts. Its facts compare the
// parameters in force with the new ones, so a draft that amends others would read wrongly.
func (wallet *Wallet) SignParameters(checkpoint node.Checkpoint, draft string) (voteDraft, bool, error) {
	var change ledger.ParameterChange
	if json.Unmarshal([]byte(draft), &change) != nil || change.NewParameters.TransactionCost == nil {
		return voteDraft{}, false, errors.New("unreadable parameter draft")
	}
	state := checkpoint.State
	if change.Sequence != state.GovernanceSequence || change.PreviousParametersDigest != state.Parameters.Digest() ||
		!change.NewParameters.Acceptable(len(checkpoint.Validators)) {
		return voteDraft{}, false, errors.New("this draft does not amend the parameters in force at the current governance sequence, or the network does not accept its values: draft it again")
	}
	name, err := wallet.registeredName(checkpoint)
	if err != nil {
		return voteDraft{}, false, err
	}
	signer, err := wallet.signer()
	if err != nil {
		return voteDraft{}, false, err
	}
	signed, err := ledger.SignParameterVote(signer, name, change.NewParameters, change.PreviousParametersDigest, change.Sequence)
	if err != nil {
		return voteDraft{}, false, err
	}
	change.Votes = []ledger.ParameterVote{signed}
	return wallet.handOver(checkpoint, change, "/hdls/parameter-vote", parameterFacts(checkpoint.State.Parameters, change))
}

// SignReassignment signs a name action draft, this wallet's own or one handed to it as text, in the
// role this wallet holds (spec 7.6). The new key's wallet signs the new key's proof of possession; it
// holds no name, since the new key is no name's key. Any other wallet signs its vote, under its name,
// once the action fits the verified state and its proof verifies under the new key: the new key's
// wallet signs first. See handOver for what happens to the signed text.
func (wallet *Wallet) SignReassignment(checkpoint node.Checkpoint, draft string) (voteDraft, bool, error) {
	var action ledger.Reassignment
	if json.Unmarshal([]byte(draft), &action) != nil || action.Name == "" {
		return voteDraft{}, false, errors.New("unreadable name action draft")
	}
	signer, err := wallet.signer()
	if err != nil {
		return voteDraft{}, false, err
	}
	state := checkpoint.State
	if action.NewKey == wallet.PublicKey() {
		if err := fitsStateBeforeTheProof(action, state); err != nil {
			return voteDraft{}, false, fmt.Errorf("the network does not accept this action: %w", err)
		}
		if action, err = ledger.SignReassignmentNewKey(signer, action); err != nil {
			return voteDraft{}, false, err
		}
		proof, submitted, err := wallet.handOver(checkpoint, action, "/hdls/reassignment-vote", nameActionFacts(action, state))
		proof.NewKeyProof = true
		return proof, submitted, err
	}
	if action.NewKey != "" && action.NewKeySignature == "" {
		return voteDraft{}, false, errors.New("this action gives the name a new key: hand the draft to the new key's wallet first, which signs its proof of possession, and sign the text it hands back")
	}
	if err := action.FitsState(state); err != nil {
		return voteDraft{}, false, fmt.Errorf("the network does not accept this action: %w", err)
	}
	if action.NewKey != "" && !ledger.VerifyReassignmentNewKey(signing.MLDSA65{}, wallet.pinnedNetworkID(), action) {
		return voteDraft{}, false, errors.New("the new key's proof of possession does not verify: hand the draft to the new key's wallet again, and sign the text it hands back")
	}
	name, err := wallet.registeredName(checkpoint)
	if err != nil {
		return voteDraft{}, false, err
	}
	vote, err := ledger.SignReassignmentVote(signer, name, action)
	if err != nil {
		return voteDraft{}, false, err
	}
	action.Votes = []ledger.ReassignmentVote{vote}
	return wallet.handOver(checkpoint, action, "/hdls/reassignment-vote", nameActionFacts(action, state))
}

// handOver returns a signed certificate as text, for the operator to hand to the other validators;
// nothing is sent. Only when this wallet's name alone is a quorum (the single-validator demo) is its vote
// a complete certificate already, and then it is submitted at once, to path.
func (wallet *Wallet) handOver(checkpoint node.Checkpoint, certificate any, path string, facts []string) (voteDraft, bool, error) {
	text, err := json.Marshal(certificate)
	if err != nil {
		return voteDraft{}, false, err
	}
	signed := voteDraft{Facts: facts, Certificate: string(text)}
	validators := consensus.NewValidatorSet(checkpoint.Validators)
	if validators.Power(nameOfKey(checkpoint.State.Accounts, wallet.PublicKey())) >= validators.Quorum() {
		return signed, true, wallet.postJSON(path, certificate)
	}
	return signed, false, nil
}

// SubmitParameters assembles the signed parameter votes the operator pasted into one certificate and
// submits it once — see SubmitReassignment.
func (wallet *Wallet) SubmitParameters(pasted string) error {
	votes, err := decodeVotes[ledger.ParameterChange](pasted)
	if err != nil {
		return err
	}
	change := votes[0]
	if change.NewParameters.TransactionCost == nil {
		return errors.New("not a parameter vote")
	}
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		return err
	}
	validators := consensus.NewValidatorSet(checkpoint.Validators)
	tally := consensus.NewCertificateTally(validators)
	change.Votes = nil
	for _, signed := range votes {
		for _, vote := range signed.Votes {
			if tally.Add(vote.Validator, func(publicKey string) bool {
				return ledger.VerifyParameterVote(signing.MLDSA65{}, wallet.pinnedNetworkID(), publicKey, change.NewParameters, change.PreviousParametersDigest, change.Sequence, vote)
			}) {
				change.Votes = append(change.Votes, vote)
			}
		}
	}
	if !change.Valid(signing.MLDSA65{}, wallet.pinnedNetworkID(), validators, checkpoint.State.Parameters, checkpoint.State.GovernanceSequence) {
		return incomplete(tally.Power(), validators.Quorum(), "the parameters or the governance sequence these votes amend have moved since they were signed, or the new values are out of bounds for the current validators")
	}
	return wallet.postJSON("/hdls/parameter-vote", change)
}

// SubmitReassignment assembles the signed name action votes the operator pasted (the texts the
// validators' wallets printed, one after another) into one certificate and submits it once. It keeps
// the votes of distinct current validators for the same values as the first text, and the first new
// key's proof, in the order pasted, that makes them a valid certificate. It sends nothing unless they
// make one: a quorum, still amending the name and the governance sequence, and a proof that verifies.
// Each vote is checked under the key the validator set holds for its name.
func (wallet *Wallet) SubmitReassignment(pasted string) error {
	votes, err := decodeVotes[ledger.Reassignment](pasted)
	if err != nil {
		return err
	}
	action := votes[0]
	if action.Name == "" {
		return errors.New("not a name action vote")
	}
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		return err
	}
	validators := consensus.NewValidatorSet(checkpoint.Validators)
	tally := consensus.NewCertificateTally(validators)
	action.Votes = nil
	for _, signed := range votes {
		for _, vote := range signed.Votes {
			if tally.Add(vote.Validator, func(publicKey string) bool {
				return ledger.VerifyReassignmentVote(signing.MLDSA65{}, wallet.pinnedNetworkID(), publicKey, action, vote)
			}) {
				action.Votes = append(action.Votes, vote)
			}
		}
	}
	const cause = "the state these votes bind to has changed since they were signed, or the new key's proof is missing or does not verify"
	if !tally.Reached() {
		return incomplete(tally.Power(), validators.Quorum(), cause)
	}
	// The votes do not cover the new key's proof, so each text's proof is tried in turn: a bad proof in
	// one text cannot block a good one in another.
	for _, signed := range votes {
		action.NewKeySignature = signed.NewKeySignature
		if action.Valid(signing.MLDSA65{}, wallet.pinnedNetworkID(), validators, checkpoint.State) {
			return wallet.postJSON("/hdls/reassignment-vote", action)
		}
	}
	return incomplete(tally.Power(), validators.Quorum(), cause)
}

// decodeVotes reads signed votes pasted one after another, as the wallets printed them.
func decodeVotes[CertificateType any](pasted string) ([]CertificateType, error) {
	var votes []CertificateType
	decoder := json.NewDecoder(strings.NewReader(pasted))
	for {
		var vote CertificateType
		if err := decoder.Decode(&vote); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("unreadable vote: %v", err)
		}
		votes = append(votes, vote)
	}
	if len(votes) == 0 {
		return nil, errors.New("paste the signed votes first")
	}
	return votes, nil
}

// incomplete says why the pasted votes do not make a certificate: too little power, or else cause, what
// can still fail in the caller's kind of certificate.
func incomplete(power, quorum int64, cause string) error {
	if power < quorum {
		return fmt.Errorf("not enough votes: current validators holding %d of the %d power needed signed these values", power, quorum)
	}
	return errors.New(cause + ": draft and sign the vote again")
}
