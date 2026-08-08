package ledger

import (
	"encoding/hex"
	"encoding/json"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// WithinMessageBound reports whether a single ledger message (a payment or an account update) fits
// the governed serialized-size cap (LedgerParameters.MaxMessageBytes; 0 = unbounded). Measured on
// the message's canonical JSON — the form it travels and is stored in — so a bounded network cannot
// be made to commit an entry (huge-denominator amount, megabyte name/description/comment) that every
// validator would carry and re-hash into every subsequent digest. Enforced at intake (node), in
// ProposeBlock, and in ApplyBlock, so a proposer that bypassed intake cannot smuggle one in.
func WithinMessageBound(maxBytes int, message any) bool {
	if maxBytes <= 0 {
		return true
	}
	serialized, err := json.Marshal(message)
	return err == nil && len(serialized) <= maxBytes
}

// digestHex is the hex form a payment's PrevStateDigest is compared against.
func digestHex(d [32]byte) string { return hex.EncodeToString(d[:]) }

// Block is a batch of changes applied atomically to move the committed state from one to the next.
// StateDigest is the digest of the resulting LedgerState (accounts AND parameters) -- the single
// value the consensus layer commits, so agreement on it is agreement on the exact applied state
// (A2). PrevStateDigest is the digest of the state this block builds on. Evidence carries unforgeable
// proofs of validator equivocation to slash in this block.
type Block struct {
	Height          int64
	PrevStateDigest [32]byte
	Payments        []Payment
	Updates         []AccountUpdate
	Evidence        []consensus.Evidence
	ParameterChange *ParameterChange // a ⅔-ratified amendment to the parameters (nil if none)
	Reassignment    *Reassignment    // a ⅔-ratified override of one account's balance (nil if none)
	StateDigest     [32]byte
}

// BlockContent is the raw, unfiltered content a proposer offers for a block: payments to settle,
// account-attribute updates (the registry / re-delegation), equivocation evidence to slash, and an
// optional parameter change to ratify. ProposeBlock keeps only what is valid and names this block.
type BlockContent struct {
	Payments        []Payment
	Updates         []AccountUpdate
	Evidence        []consensus.Evidence
	ParameterChange *ParameterChange
	Reassignment    *Reassignment
}

// maxEvidenceAge bounds how many blocks after an equivocation its evidence may still be carried, so
// old evidence cannot be re-included later to confiscate a same-key account that was recreated in the
// meantime. Generous enough for legitimate prompt slashing (the node carries a proof only until the
// offender is removed), but not open-ended.
const maxEvidenceAge = 1000

// evidenceInWindow reports whether an equivocation at its (shared) vote height is recent enough to be
// carried in a block at `height`.
func evidenceInWindow(e consensus.Evidence, height int64) bool {
	return e.A.Height <= height && height-e.A.Height <= maxEvidenceAge
}

// applySlashes removes the accounts of validators proven to have equivocated (double-signed). It
// confiscates their stake and, in the dynamic-validator model where power IS stake, ejects them from
// the set. Only evidence that verifies is acted on; it is deterministic, so every node computes the
// same slashed state. This is the durable penalty behind the vote sets' in-round un-counting — it
// zeroes out anyone who double-signs for a block.
func applySlashes(scheme signing.Scheme, networkID string, accounts []Account, evidence []consensus.Evidence) []Account {
	offenders := make(map[string]bool)
	for _, equivocation := range evidence {
		if equivocation.Valid(scheme, networkID) {
			offenders[equivocation.Validator] = true
		}
	}
	if len(offenders) == 0 {
		return accounts
	}
	remainingAccounts := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !offenders[account.PublicKey] {
			remainingAccounts = append(remainingAccounts, account)
		}
	}
	return remainingAccounts
}

// ProposeBlock builds the block that results from applying `content` to `previousState`. The
// transaction cost is taken from the committed parameters (not chosen per block), only
// signature-valid payments/updates that name THIS block are included, and the resulting StateDigest
// is computed over the actual applied LedgerState (accounts and parameters).
func ProposeBlock(scheme signing.Scheme, networkID string, height int64, previousState LedgerState, content BlockContent, validators ...*consensus.ValidatorSet) Block {
	transactionCost := previousState.Parameters.TransactionCost
	validatorSet := firstValidatorSet(validators)
	// The fork-binding tier pins a payment to the ACCOUNT history the payer saw (a wallet can compute
	// it), distinct from the block's PrevStateDigest (the full committed state).
	accountDigestHex := digestHex(StateDigest(previousState.Accounts))

	// Binding a payment to an exact block (its BlockID) is the replay guard: a payment applies at
	// most once, and a resubmitted (past-block) payment is stale here; a future-block payment waits.
	registry := nameRegistry(previousState.Accounts)
	var paymentsForThisBlock []Payment
	for _, payment := range ValidPayments(scheme, networkID, content.Payments) {
		if payment.BlockID != height {
			continue
		}
		if payment.PrevStateDigest != "" && payment.PrevStateDigest != accountDigestHex {
			continue // fork-binding tier: the payment is pinned to a different history
		}
		if !WithinMessageBound(previousState.Parameters.MaxMessageBytes, payment) {
			continue // oversized message — never carried into a block
		}
		if _, resolves := resolveRecipient(registry, payment); !resolves {
			continue // unresolvable recipient — would be a zero-fee no-op; never carried into a block
		}
		paymentsForThisBlock = append(paymentsForThisBlock, payment)
	}
	var verifiedUpdates []AccountUpdate
	for _, update := range content.Updates {
		if update.BlockID == height && VerifyAccountUpdate(scheme, networkID, update) &&
			WithinMessageBound(previousState.Parameters.MaxMessageBytes, update) {
			verifiedUpdates = append(verifiedUpdates, update)
		}
	}
	var verifiedEvidence []consensus.Evidence
	for _, equivocation := range content.Evidence {
		if equivocation.Valid(scheme, networkID) && evidenceInWindow(equivocation, height) {
			verifiedEvidence = append(verifiedEvidence, equivocation) // only carry provable, recent equivocation
		}
	}

	// At most ONE governance action per block: both bind the current governance sequence, and applying
	// one bumps it, so a second action in the same block would be stale. Prefer the parameter change.
	nextParameters := previousState.Parameters
	nextSequence := previousState.GovernanceSequence
	var ratifiedChange *ParameterChange
	var ratifiedReassignment *Reassignment
	switch {
	case content.ParameterChange != nil && content.ParameterChange.Valid(scheme, networkID, validatorSet, previousState.Parameters, previousState.GovernanceSequence):
		nextParameters = content.ParameterChange.NewParameters // ⅔ of validators approved this amendment
		ratifiedChange = content.ParameterChange
		nextSequence++
	case content.Reassignment != nil && content.Reassignment.Valid(scheme, networkID, validatorSet, previousState.Accounts, previousState.GovernanceSequence):
		ratifiedReassignment = content.Reassignment // ⅔ of validators approved this balance override
		nextSequence++
	}

	resultingAccounts := ProcessPayments(previousState.Accounts, paymentsForThisBlock, transactionCost)
	resultingAccounts = applyAccountUpdates(scheme, networkID, resultingAccounts, verifiedUpdates, height)
	resultingAccounts = applySlashes(scheme, networkID, resultingAccounts, verifiedEvidence)
	if ratifiedReassignment != nil {
		resultingAccounts = applyReassignment(resultingAccounts, ratifiedReassignment.Account, ratifiedReassignment.NewAmount)
	}
	nextState := LedgerState{Accounts: resultingAccounts, Parameters: nextParameters, GovernanceSequence: nextSequence}
	return Block{
		Height:          height,
		PrevStateDigest: previousState.Digest(),
		Payments:        paymentsForThisBlock,
		Updates:         verifiedUpdates,
		Evidence:        verifiedEvidence,
		ParameterChange: ratifiedChange,
		Reassignment:    ratifiedReassignment,
		StateDigest:     nextState.Digest(),
	}
}

// ApplyBlock re-executes `block` against `previousState` and returns the resulting LedgerState. It
// returns (state, false) unless the block applies cleanly: it must build on previousState (its
// PrevStateDigest matches), every payment/update it carries must be signature-valid and name this
// block, every slash must be backed by unforgeable evidence, and the independently recomputed result
// digest must equal the block's claimed StateDigest. This is the check every node runs, so a node
// only accepts a block whose applied state is exactly what it recomputes (audit A2).
func ApplyBlock(scheme signing.Scheme, networkID string, previousState LedgerState, block Block, validators ...*consensus.ValidatorSet) (LedgerState, bool) {
	if previousState.Digest() != block.PrevStateDigest {
		return LedgerState{}, false
	}
	if len(ValidPayments(scheme, networkID, block.Payments)) != len(block.Payments) {
		return LedgerState{}, false // at least one payment is unsigned or forged
	}
	accountDigestHex := digestHex(StateDigest(previousState.Accounts))
	registry := nameRegistry(previousState.Accounts)
	for _, payment := range block.Payments {
		if payment.BlockID != block.Height {
			return LedgerState{}, false // every payment must name exactly this block (replay guard)
		}
		if payment.PrevStateDigest != "" && payment.PrevStateDigest != accountDigestHex {
			return LedgerState{}, false // a fork-pinned payment placed on the wrong history
		}
		if !WithinMessageBound(previousState.Parameters.MaxMessageBytes, payment) {
			return LedgerState{}, false // an oversized message a proposer smuggled past intake
		}
		if _, resolves := resolveRecipient(registry, payment); !resolves {
			return LedgerState{}, false // a zero-fee no-op (unresolvable recipient) a proposer padded the block with
		}
	}
	for _, update := range block.Updates {
		if update.BlockID != block.Height || !VerifyAccountUpdate(scheme, networkID, update) ||
			!WithinMessageBound(previousState.Parameters.MaxMessageBytes, update) {
			return LedgerState{}, false // an unsigned, misplaced or oversized account update
		}
	}
	for _, equivocation := range block.Evidence {
		if !equivocation.Valid(scheme, networkID) || !evidenceInWindow(equivocation, block.Height) {
			return LedgerState{}, false // slash without unforgeable proof, or stale re-included evidence
		}
	}

	if block.ParameterChange != nil && block.Reassignment != nil {
		return LedgerState{}, false // at most one governance action per block (they'd bind the same sequence)
	}
	nextParameters := previousState.Parameters
	nextSequence := previousState.GovernanceSequence
	if block.ParameterChange != nil {
		if !block.ParameterChange.Valid(scheme, networkID, firstValidatorSet(validators), previousState.Parameters, previousState.GovernanceSequence) {
			return LedgerState{}, false // an unratified parameter change (not a > ⅔ quorum of the set)
		}
		nextParameters = block.ParameterChange.NewParameters
		nextSequence++
	}
	if block.Reassignment != nil {
		if !block.Reassignment.Valid(scheme, networkID, firstValidatorSet(validators), previousState.Accounts, previousState.GovernanceSequence) {
			return LedgerState{}, false // an unratified balance reassignment (not a > ⅔ quorum of the set)
		}
		nextSequence++
	}

	resultingAccounts := ProcessPayments(previousState.Accounts, block.Payments, previousState.Parameters.TransactionCost)
	resultingAccounts = applyAccountUpdates(scheme, networkID, resultingAccounts, block.Updates, block.Height)
	resultingAccounts = applySlashes(scheme, networkID, resultingAccounts, block.Evidence)
	if block.Reassignment != nil {
		resultingAccounts = applyReassignment(resultingAccounts, block.Reassignment.Account, block.Reassignment.NewAmount)
	}
	nextState := LedgerState{Accounts: resultingAccounts, Parameters: nextParameters, GovernanceSequence: nextSequence}
	if nextState.Digest() != block.StateDigest {
		return LedgerState{}, false // claimed result does not match the recomputed result
	}
	return nextState, true
}
