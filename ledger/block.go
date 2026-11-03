package ledger

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// WithinMessageBound reports whether a single ledger message (a payment or an account update) fits
// the governed serialized-size cap (LedgerParameters.MaxMessageBytes; 0 = unbounded). Measured on
// the message's canonical JSON, the form it travels in, so a bounded network cannot be made to commit
// an entry (a megabyte description; a name is at most 32 bytes) that every validator would keep and
// re-hash into every subsequent digest; a payment, whose comment the state never keeps, is bounded the
// same way. Enforced at intake (node), in ProposeBlock, and in ApplyBlock, so a proposer that bypassed
// intake cannot smuggle one in.
func WithinMessageBound(maxBytes int64, message any) bool {
	if maxBytes <= 0 {
		return true
	}
	serialized, err := json.Marshal(message)
	return err == nil && int64(len(serialized)) <= maxBytes
}

// maxBlockContentBytes bounds the serialized freezes, name requests, payments and updates an honest
// proposer puts in one block: well under the 2 MiB every peer decodes a proposal under, leaving room
// for the envelope, the evidence and a governance certificate. It is proposer policy, not a validity
// rule (ApplyBlock accepts any block that applies), and a constant: a governed bound would change the
// parameter digest.
const maxBlockContentBytes = 768 << 10

// serializedSize is the length of a message's JSON form, the bytes it takes up in a proposal.
func serializedSize(message any) int {
	serialized, _ := json.Marshal(message) // name requests, payments and updates always marshal
	return len(serialized)
}

// digestHex is the hex form a payment's PreviousStateDigest is compared against.
func digestHex(digest [32]byte) string { return hex.EncodeToString(digest[:]) }

// creditedBalance is a payee's balance once a payment credits it with its amount less the transaction
// cost.
func creditedBalance(payeeBalance *big.Rat, payment Payment, transactionCost *big.Rat) *big.Rat {
	credited := new(big.Rat).Sub(payment.Amount, transactionCost)
	return credited.Add(credited, payeeBalance)
}

// CreditFitsBalanceCap reports whether payment's credit keeps its payee's balance in state within
// MaxAccountBalance: the check intake makes against the committed state.
func CreditFitsBalanceCap(state LedgerState, payment Payment) bool {
	payeeBalance := AccountAmount(state.Accounts, payment.To)
	return creditedBalance(payeeBalance, payment, state.Parameters.TransactionCost).Cmp(state.Parameters.MaxAccountBalance) <= 0
}

// withinBalanceCap is rule 23: no account ends the block above the pre-block MaxAccountBalance unless
// its balance did not rise in the block (an account the block creates rises from zero). So a cap that
// the block lowers applies from the next block, and a balance already above a lowered cap may stay
// there and shrink.
func withinBalanceCap(previousAccounts []Account, maxAccountBalance *big.Rat, nextAccounts []Account) bool {
	for _, account := range nextAccounts {
		previousBalance := AccountAmount(previousAccounts, account.Name)
		if account.Amount.Cmp(maxAccountBalance) > 0 && account.Amount.Cmp(previousBalance) > 0 {
			return false
		}
	}
	return true
}

// PaymentFitsState checks a payment against a state, the pre-block state in a block or the committed
// state at intake (spec 5.3 rule 17, 4.1 step 3): its amount is at least the transaction cost and at
// most the largest single payment, its JSON fits the message bound, its payer and payee exist and are
// not frozen, and its amount is at most the payer's balance. It returns an error naming the first rule
// the payment breaks. The block the payment names, a pin and the signature are the caller's to check.
func PaymentFitsState(state LedgerState, payment Payment) error {
	parameters := state.Parameters
	payer, payerFound := FindAccount(state.Accounts, payment.From)
	payee, payeeFound := FindAccount(state.Accounts, payment.To)
	switch {
	case payment.Amount == nil || payment.Amount.Cmp(parameters.TransactionCost) < 0:
		return errors.New("amount below the transaction cost: the ledger would drop it without moving value")
	case payment.Amount.Cmp(parameters.MaxPaymentAmount) > 0:
		return errors.New("amount above the largest single payment the network allows (MaxPaymentAmount)")
	case !WithinMessageBound(parameters.MaxMessageBytes, payment):
		return errors.New("payment too large: its serialized form exceeds the governed message bound")
	case !payerFound:
		return fmt.Errorf("no account is named %q (the payer)", payment.From)
	case payer.Frozen:
		return fmt.Errorf("the payer %q is frozen", payment.From)
	case !payeeFound:
		return fmt.Errorf("no account is named %q (the payee)", payment.To)
	case payee.Frozen:
		return fmt.Errorf("the payee %q is frozen", payment.To)
	case payment.Amount.Cmp(payer.Amount) > 0:
		return errors.New("amount above the payer's balance")
	}
	return nil
}

// paymentError is rule 17 of spec 5.3 for a payment in the block at height, built on previousState
// whose state digest, in hex, is stateDigestHex, all but the signature (paymentSigned): the payment
// names this block, is unpinned or pinned to that digest (E38), and fits previousState. Only a pinned
// payment reads stateDigestHex, so the caller computes it only once a payment carries a pin: the
// digest hashes every account.
func paymentError(previousState LedgerState, stateDigestHex string, height int64, payment Payment) error {
	if payment.BlockID != height {
		return fmt.Errorf("it names block %d, not this one (the replay guard)", payment.BlockID)
	}
	if payment.PreviousStateDigest != "" && payment.PreviousStateDigest != stateDigestHex {
		return errors.New("it is pinned to another history")
	}
	return PaymentFitsState(previousState, payment)
}

// paymentSigned reports whether a payment verifies under its payer's key in previousState.
func paymentSigned(chain Chain, previousState LedgerState, payment Payment) bool {
	payer, _ := FindAccount(previousState.Accounts, payment.From)
	return VerifyPayment(chain.Scheme, chain.NetworkID, payer.PublicKey, payment)
}

// UpdateFitsState checks an account update against a state, the pre-block state in a block or the
// committed state at intake (spec 5.3 rule 20, 4.2): it changes a field, its JSON fits the message
// bound, a public address it sets is in the one plain form (CanonicalAddress), IP addresses it sets
// are at most MaxIPAddresses, each in its one plain form (CanonicalIPAddress) and none twice, TLS keys
// it sets are at most MaxTLSKeys, each in its one form (CanonicalTLSKey) and none twice, its account
// exists and is not frozen, and a delegate it names exists and is not frozen. It returns an error
// naming the first rule the update breaks. The block it names and its signature are the caller's to
// check. Intake, blocks (updateError) and the wallet all check an update here, so no address or key
// of another form enters the state by an update.
func UpdateFitsState(state LedgerState, update AccountUpdate) error {
	account, accountFound := FindAccount(state.Accounts, update.Name)
	delegate, delegateFound := FindAccount(state.Accounts, update.Delegate)
	setsDelegate := update.Delegate != "" && update.Delegate != ClearField
	setsPublicAddress := update.PublicAddress != "" && update.PublicAddress != ClearField
	var ipAddressesRefusal, tlsKeysRefusal error
	if !clearsList(update.IPAddresses) {
		ipAddressesRefusal = ipAddressesError(update.IPAddresses) // nil for an empty list, which leaves them
	}
	if !clearsList(update.TLSKeys) {
		tlsKeysRefusal = tlsKeysError(update.TLSKeys) // nil for an empty list, which leaves them
	}
	switch {
	case update.Delegate == "" && update.Description == "" && update.PublicAddress == "" && len(update.IPAddresses) == 0 && len(update.TLSKeys) == 0:
		return errors.New("the update changes nothing: it sets no delegate, description, public address, IP addresses or TLS keys")
	case !WithinMessageBound(state.Parameters.MaxMessageBytes, update):
		return errors.New("update too large: its serialized form exceeds the governed message bound")
	case setsPublicAddress && !CanonicalAddress(update.PublicAddress):
		return fmt.Errorf("the public address %q is not an address: %s", update.PublicAddress, addressRule)
	case ipAddressesRefusal != nil:
		return ipAddressesRefusal
	case tlsKeysRefusal != nil:
		return tlsKeysRefusal
	case !accountFound:
		return fmt.Errorf("no account is named %q", update.Name)
	case account.Frozen:
		return fmt.Errorf("the account %q is frozen", update.Name)
	case setsDelegate && !delegateFound:
		return fmt.Errorf("no account is named %q (the delegate)", update.Delegate)
	case setsDelegate && delegate.Frozen:
		return fmt.Errorf("the delegate %q is frozen", update.Delegate)
	}
	return nil
}

// updateError is rule 20 of spec 5.3 for an update in the block at height, built on previousState, all
// but the signature (updateSigned): the update names this block and fits previousState.
func updateError(previousState LedgerState, height int64, update AccountUpdate) error {
	if update.BlockID != height {
		return fmt.Errorf("it names block %d, not this one (the replay guard)", update.BlockID)
	}
	return UpdateFitsState(previousState, update)
}

// updateSigned reports whether an update verifies under its account's key in previousState.
func updateSigned(chain Chain, previousState LedgerState, update AccountUpdate) bool {
	account, _ := FindAccount(previousState.Accounts, update.Name)
	return VerifyAccountUpdate(chain.Scheme, chain.NetworkID, account.PublicKey, update)
}

// nameRequestError is rules 9 and 12 of spec 5.3 for a name request in the block at height, built on
// previousState, which seats validators, all but the signatures (nameRequestSigned): the request has
// its shape, names this block, and fits previousState.
func nameRequestError(previousState LedgerState, validators *consensus.ValidatorSet, height int64, request NameRequest) error {
	if err := NameRequestShapeError(request); err != nil {
		return err
	}
	if request.BlockID != height {
		return fmt.Errorf("it names block %d, not this one (the replay guard)", request.BlockID)
	}
	return NameRequestFitsState(previousState, validators, request)
}

// nameRequestSigned reports whether a name request's signatures verify under its requestor's and its
// bank's keys in previousState, and under its new key.
func nameRequestSigned(chain Chain, previousState LedgerState, request NameRequest) bool {
	requestor, _ := FindAccount(previousState.Accounts, request.Requestor)
	bank, _ := FindAccount(previousState.Accounts, request.Bank)
	return VerifyNameRequest(chain.Scheme, chain.NetworkID, requestor.PublicKey, bank.PublicKey, request)
}

// Block is a batch of changes applied atomically to move the committed state from one to the next
// (spec 5.1). Its hash (BlockHash, hash.go) covers every field, and is what the consensus layer
// commits: agreement on it is agreement on the exact block, the set that signs it, the set that signs
// the next block, and the state it results in (A2).
type Block struct {
	Height             int64
	PreviousBlockHash  [32]byte // the hash of the block before; the genesis hash for block 1
	ValidatorsHash     [32]byte // ValidatorsHash of the set that signs this block: the one the pre-block state seats
	NextValidatorsHash [32]byte // ValidatorsHash of the set that signs the next block: the one the resulting state seats
	Nonce              [32]byte // random, drawn by the proposer; it keeps the state digest out of the record
	Content            BlockContent
	StateDigest        [32]byte // the digest of the resulting LedgerState
}

// BlockContent is what a block carries: payments to settle, account updates (re-delegation,
// description, addresses), name requests (registrations, key and bank changes, freezes), proofs of
// double-signing, each of which freezes its signer's name, and at most one governance action to
// ratify. A proposer offers ProposeBlock the content it holds, and ProposeBlock keeps only what is
// valid for the block.
type BlockContent struct {
	Payments         []Payment
	AccountUpdates   []AccountUpdate
	NameRequests     []NameRequest // in a proposed block, the freezes come first
	VoteEvidence     []consensus.VoteEvidence
	ProposalEvidence []consensus.ProposalEvidence
	ParameterChange  *ParameterChange // a ⅔-ratified amendment to the parameters (nil if none)
	Reassignment     *Reassignment    // the ⅔ name action (nil if none)
}

// maxEvidencePerBlock is how many evidence items, of both kinds together, an honest proposer puts in one
// block (spec 5.4). Like maxBlockContentBytes it is proposer policy, not a validity rule; the rest wait.
const maxEvidencePerBlock = 8

// evidenceFitsState is the part of rule 7 (spec 4.12) that needs no signature, for a double-sign by name
// at signedHeight carried in the block at height, built on previousState: the name exists and is not
// frozen, and the double-sign is below the block's height. It returns the name's current key, the one
// both signatures must verify under. There is no age limit and no check against a past validator set:
// once the name's key changes, older evidence against it no longer verifies, and so it is moot.
func evidenceFitsState(previousState LedgerState, height int64, name string, signedHeight int64) (string, error) {
	account, found := FindAccount(previousState.Accounts, name)
	switch {
	case !found:
		return "", fmt.Errorf("no account is named %q", name)
	case account.Frozen:
		return "", fmt.Errorf("the account %q is frozen already", name)
	case signedHeight >= height:
		return "", fmt.Errorf("the double-sign, at height %d, is not below this block's height", signedHeight)
	}
	return account.PublicKey, nil
}

// voteEvidenceError is rule 7 for vote evidence in the block at height, built on previousState: it fits
// the state (evidenceFitsState), and its two votes are a double-sign under the name's current key.
func voteEvidenceError(chain Chain, previousState LedgerState, height int64, evidence consensus.VoteEvidence) error {
	publicKey, err := evidenceFitsState(previousState, height, evidence.Name, evidence.First.Height)
	if err == nil && !evidence.Valid(chain.Scheme, chain.NetworkID, publicKey) {
		err = fmt.Errorf("it is not a double-sign by %q under its current key", evidence.Name)
	}
	return err
}

// proposalEvidenceError is rule 7 for proposal evidence, as voteEvidenceError is for vote evidence.
func proposalEvidenceError(chain Chain, previousState LedgerState, height int64, evidence consensus.ProposalEvidence) error {
	publicKey, err := evidenceFitsState(previousState, height, evidence.Name, evidence.First.Height)
	if err == nil && !evidence.Valid(chain.Scheme, chain.NetworkID, publicKey) {
		err = fmt.Errorf("it is not a double-sign by %q under its current key", evidence.Name)
	}
	return err
}

// frozenNames is F, the names a block freezes (spec 5.3 rule 11): those its evidence and its freeze
// requests are about, and the ⅔ action's name when the action freezes it.
func frozenNames(content BlockContent) map[string]bool {
	frozen := map[string]bool{}
	for _, evidence := range content.VoteEvidence {
		frozen[evidence.Name] = true
	}
	for _, evidence := range content.ProposalEvidence {
		frozen[evidence.Name] = true
	}
	for _, request := range content.NameRequests {
		if request.Kind == FreezeKind {
			frozen[request.Name] = true
		}
	}
	if content.Reassignment != nil && content.Reassignment.Freeze {
		frozen[content.Reassignment.Name] = true
	}
	return frozen
}

// applyContent applies a block's content, which its caller has checked, to previousState, whose set is
// validators, in the order of spec 5.3 step I: payments, updates, name requests other than freezes, the
// freezes from every source (evidence, freeze requests, the ⅔ action), which take no money (R5), the ⅔
// action's other parts, then the changes that fall due; the result is at the next height, with the new
// parameters if a parameter change applied, and the governance sequence moved on by the governance
// action.
func applyContent(previousState LedgerState, validators *consensus.ValidatorSet, content BlockContent) LedgerState {
	height := previousState.Height + 1
	parameters, sequence := previousState.Parameters, previousState.GovernanceSequence
	accounts := ProcessPayments(previousState.Accounts, content.Payments, parameters.TransactionCost)
	accounts = applyAccountUpdates(accounts, content.AccountUpdates)
	accounts = applyNameRequests(accounts, content.NameRequests, height+parameters.ChangeDelayBlocks)
	accounts = freezeNames(accounts, frozenNames(content))
	if content.ParameterChange != nil {
		parameters = content.ParameterChange.NewParameters // ⅔ of validators approved this amendment
		sequence++
	}
	if content.Reassignment != nil {
		accounts = applyReassignment(accounts, *content.Reassignment) // ⅔ of validators approved this action
		sequence++
	}
	accounts = applyDueChanges(accounts, validators, height)
	return LedgerState{Accounts: accounts, Parameters: parameters, GovernanceSequence: sequence, Height: height}
}

// seatsAName is rule 24 (R9): the validator rule seats at least one name on state. A state that seats
// none could never sign another block.
func seatsAName(validatorRule ValidatorRule, state LedgerState) bool {
	return validatorRule.SetFor(state).TotalPower() > 0
}

// LeavesANameSeated reports whether applying content alone to state still leaves a name seated (R9).
// Intake and the pools refuse a freeze or a ⅔ action that alone would leave the validator set empty;
// content holds that one item, valid against state.
func LeavesANameSeated(chain Chain, state LedgerState, content BlockContent) bool {
	return seatsAName(chain.ValidatorRule, applyContent(state, chain.ValidatorRule.SetFor(state), content))
}

// withoutLastItem takes the item ProposeBlock added last out of content, and reports whether there was
// one (spec 5.4, R9): an update, a payment, a name request other than a freeze, a proposal evidence item,
// a vote evidence item, a freeze, then the governance action. Taking an item out never makes the rest
// invalid.
func withoutLastItem(content *BlockContent) bool {
	requests := content.NameRequests
	lastRequestIsFreeze := len(requests) > 0 && requests[len(requests)-1].Kind == FreezeKind
	switch {
	case len(content.AccountUpdates) > 0:
		content.AccountUpdates = content.AccountUpdates[:len(content.AccountUpdates)-1]
	case len(content.Payments) > 0:
		content.Payments = content.Payments[:len(content.Payments)-1]
	case len(requests) > 0 && !lastRequestIsFreeze:
		content.NameRequests = requests[:len(requests)-1]
	case len(content.ProposalEvidence) > 0:
		content.ProposalEvidence = content.ProposalEvidence[:len(content.ProposalEvidence)-1]
	case len(content.VoteEvidence) > 0:
		content.VoteEvidence = content.VoteEvidence[:len(content.VoteEvidence)-1]
	case len(requests) > 0:
		content.NameRequests = requests[:len(requests)-1]
	case content.ParameterChange != nil:
		content.ParameterChange = nil
	case content.Reassignment != nil:
		content.Reassignment = nil
	default:
		return false
	}
	return true
}

// ProposeBlock builds the block that follows previousState from the valid part of `content` (spec 5.4),
// on the block whose hash is previousBlockHash, with the nonce the caller drew and the hash of the set
// chain.ValidatorRule derives from previousState, the set that signs it. It chooses, in this order:
//
//  1. at most one governance action, a valid parameter change before a valid ⅔ name action: both bind
//     the governance sequence, and applying one bumps it, so a second would be stale;
//  2. valid freeze requests, one per name, none for the action's name;
//  3. valid evidence, vote evidence and then proposal evidence, each in name order: one item per name,
//     none for a name already locked, at most maxEvidencePerBlock, and each only while the one-third cap
//     still holds with its name frozen too (rule 22); the rest wait;
//  4. valid name requests whose name, requestor and bank are not locked (the action's name and the
//     names the block freezes, R6), one per name and one per new key (the action's new key counts as
//     used), with each requestor's fees within its balance;
//  5. valid payments from and to names that are not locked, from a payer that pays no fee in the block,
//     keeping each payee within MaxAccountBalance (rule 23);
//  6. valid updates of names that are not locked, to delegates that are not locked, one per name.
//
// Each entry's cheap rules come first; its signatures are checked only when it is about to be added,
// so a block costs the signature checks of what it carries, whatever the pools hold. Freezes, requests,
// payments and updates stop at maxBlockContentBytes, so every peer can decode the proposal. If the
// resulting state would seat no name (rule 24, R9), items come out, the last added first, until it
// seats one; they lapse or wait. The transaction cost, the fees and the caps are the committed
// parameters', not chosen per block. StateDigest is the digest of the state the block results in, and
// NextValidatorsHash the hash of the set that state seats, the one that signs the next block.
func ProposeBlock(chain Chain, previousState LedgerState, previousBlockHash, nonce [32]byte, content BlockContent) Block {
	validators := chain.ValidatorRule.SetFor(previousState)
	height := previousState.Height + 1
	block := Block{Height: height, PreviousBlockHash: previousBlockHash, ValidatorsHash: ValidatorsHash(validators.Validators()), Nonce: nonce}
	carried := &block.Content
	switch {
	case content.ParameterChange != nil && content.ParameterChange.Valid(chain.Scheme, chain.NetworkID, validators, previousState.Parameters, previousState.GovernanceSequence):
		carried.ParameterChange = content.ParameterChange
	case content.Reassignment != nil && content.Reassignment.Valid(chain.Scheme, chain.NetworkID, validators, previousState):
		carried.Reassignment = content.Reassignment
	}
	locked := map[string]bool{}  // L: the action's name and the names the block freezes
	newKeys := map[string]bool{} // the new keys the block binds
	if action := carried.Reassignment; action != nil {
		locked[action.Name] = true
		if action.NewKey != "" {
			newKeys[action.NewKey] = true
		}
	}
	budget := maxBlockContentBytes // the bytes of freezes, requests, payments and updates still free

	for _, request := range content.NameRequests {
		if request.Kind != FreezeKind || locked[request.Name] || nameRequestError(previousState, validators, height, request) != nil {
			continue
		}
		size := serializedSize(request)
		if size > budget || !nameRequestSigned(chain, previousState, request) {
			continue
		}
		budget -= size
		locked[request.Name] = true
		carried.NameRequests = append(carried.NameRequests, request)
	}

	powerIgnoringFreezes := powerIgnoringFreezes(chain.ValidatorRule, previousState) // what the cap counts
	frozenByTheBlock := frozenNames(*carried)                                        // F so far: the freezes carried
	// canFreeze is the check of an evidence item against name, at signedHeight, all but its signatures:
	// the block has room for it, name is not locked, the item fits the state, and the one-third cap holds
	// with name frozen too. It returns name's current key, which the signatures must verify under.
	canFreeze := func(name string, signedHeight int64) (string, bool) {
		if len(carried.VoteEvidence)+len(carried.ProposalEvidence) >= maxEvidencePerBlock || locked[name] {
			return "", false
		}
		publicKey, err := evidenceFitsState(previousState, height, name, signedHeight)
		withName := maps.Clone(frozenByTheBlock)
		withName[name] = true
		return publicKey, err == nil && withinOneThirdCap(powerIgnoringFreezes, validators, previousState.Accounts, withName)
	}
	voteEvidenceByName := func(first, second consensus.VoteEvidence) int { return strings.Compare(first.Name, second.Name) }
	for _, evidence := range slices.SortedFunc(slices.Values(content.VoteEvidence), voteEvidenceByName) {
		if publicKey, fits := canFreeze(evidence.Name, evidence.First.Height); fits && evidence.Valid(chain.Scheme, chain.NetworkID, publicKey) {
			carried.VoteEvidence = append(carried.VoteEvidence, evidence)
			frozenByTheBlock[evidence.Name], locked[evidence.Name] = true, true
		}
	}
	proposalEvidenceByName := func(first, second consensus.ProposalEvidence) int { return strings.Compare(first.Name, second.Name) }
	for _, evidence := range slices.SortedFunc(slices.Values(content.ProposalEvidence), proposalEvidenceByName) {
		if publicKey, fits := canFreeze(evidence.Name, evidence.First.Height); fits && evidence.Valid(chain.Scheme, chain.NetworkID, publicKey) {
			carried.ProposalEvidence = append(carried.ProposalEvidence, evidence)
			frozenByTheBlock[evidence.Name], locked[evidence.Name] = true, true
		}
	}

	requested := map[string]bool{} // the names a request other than a freeze is about
	fees := map[string]*big.Rat{}  // each requestor's fees in the block
	for _, request := range content.NameRequests {
		if request.Kind == FreezeKind || requested[request.Name] || locked[request.Name] || locked[request.Requestor] || locked[request.Bank] ||
			newKeys[request.NewKey] || nameRequestError(previousState, validators, height, request) != nil {
			continue
		}
		feesAfter := new(big.Rat).Set(request.Fee)
		if paid, found := fees[request.Requestor]; found {
			feesAfter.Add(feesAfter, paid)
		}
		if !FeesFit(AccountAmount(previousState.Accounts, request.Requestor), feesAfter, false) {
			continue
		}
		size := serializedSize(request)
		if size > budget || !nameRequestSigned(chain, previousState, request) {
			continue
		}
		budget -= size
		requested[request.Name], fees[request.Requestor] = true, feesAfter
		if request.NewKey != "" {
			newKeys[request.NewKey] = true
		}
		carried.NameRequests = append(carried.NameRequests, request)
	}

	stateDigestHex := ""                   // computed for the first pinned payment (see paymentError)
	payeeBalances := map[string]*big.Rat{} // each payee's balance with the credits carried so far
	for _, payment := range content.Payments {
		if payment.PreviousStateDigest != "" && stateDigestHex == "" {
			stateDigestHex = digestHex(previousState.Digest())
		}
		_, paysFee := fees[payment.From]
		if paysFee || locked[payment.From] || locked[payment.To] || paymentError(previousState, stateDigestHex, height, payment) != nil {
			continue // a payer's requests win its block (spec 5.5), and a locked name neither pays nor is paid (R6)
		}
		payeeBalance, tracked := payeeBalances[payment.To]
		if !tracked {
			payeeBalance = AccountAmount(previousState.Accounts, payment.To)
		}
		payeeBalanceAfter := creditedBalance(payeeBalance, payment, previousState.Parameters.TransactionCost)
		if payeeBalanceAfter.Cmp(previousState.Parameters.MaxAccountBalance) > 0 {
			continue // it would lift its payee above the largest balance
		}
		size := serializedSize(payment)
		if size > budget || !paymentSigned(chain, previousState, payment) {
			continue
		}
		budget -= size
		payeeBalances[payment.To] = payeeBalanceAfter
		carried.Payments = append(carried.Payments, payment)
	}

	updated := make(map[string]bool) // accounts with an update already in this block
	for _, update := range content.AccountUpdates {
		if updated[update.Name] || locked[update.Name] || locked[update.Delegate] || updateError(previousState, height, update) != nil {
			continue
		}
		size := serializedSize(update)
		if size > budget || !updateSigned(chain, previousState, update) {
			continue
		}
		budget -= size
		updated[update.Name] = true
		carried.AccountUpdates = append(carried.AccountUpdates, update)
	}

	nextState := applyContent(previousState, validators, block.Content)
	for !seatsAName(chain.ValidatorRule, nextState) && withoutLastItem(carried) {
		nextState = applyContent(previousState, validators, block.Content)
	}
	block.StateDigest = nextState.Digest()
	block.NextValidatorsHash = ValidatorsHash(chain.ValidatorRule.SetFor(nextState).Validators())
	return block
}

// contentError checks a block's content against previousState and validators, the set it seats (spec
// 5.3 steps C to H), and returns an error naming the first rule the content breaks. Everything is
// judged against previousState: nothing a block does counts for its own checks.
func contentError(chain Chain, previousState LedgerState, validators *consensus.ValidatorSet, content BlockContent) error {
	height := previousState.Height + 1
	switch {
	case content.ParameterChange != nil && content.Reassignment != nil:
		return errors.New("the block carries two governance actions, which would bind the same sequence")
	case content.ParameterChange != nil && !content.ParameterChange.Valid(chain.Scheme, chain.NetworkID, validators, previousState.Parameters, previousState.GovernanceSequence):
		return errors.New("the parameter change is not ratified against this state by a > ⅔ quorum of the set")
	case content.Reassignment != nil && !content.Reassignment.Valid(chain.Scheme, chain.NetworkID, validators, previousState):
		return errors.New("the ⅔ name action is not valid against this state, or not ratified by a > ⅔ quorum of the set")
	}

	// D. Evidence, freezes, and the locked names L: the action's name and the names the block freezes (R6).
	evidenceNames := map[string]bool{} // the names the block's evidence is about
	// evidenceNameError is rules 8 and 10 for an evidence item about name: no other evidence item of the
	// block is about it, and the ⅔ action is not.
	evidenceNameError := func(name string) error {
		switch {
		case evidenceNames[name]:
			return fmt.Errorf("another evidence item of the block is about %q (rule 8)", name)
		case content.Reassignment != nil && content.Reassignment.Name == name:
			return fmt.Errorf("the block's ⅔ action is about %q (rule 10)", name)
		}
		evidenceNames[name] = true
		return nil
	}
	for index, evidence := range content.VoteEvidence {
		err := evidenceNameError(evidence.Name)
		if err == nil {
			err = voteEvidenceError(chain, previousState, height, evidence)
		}
		if err != nil {
			return fmt.Errorf("vote evidence %d: %w", index, err)
		}
	}
	for index, evidence := range content.ProposalEvidence {
		err := evidenceNameError(evidence.Name)
		if err == nil {
			err = proposalEvidenceError(chain, previousState, height, evidence)
		}
		if err != nil {
			return fmt.Errorf("proposal evidence %d: %w", index, err)
		}
	}
	locked := map[string]bool{}
	newKeys := map[string]bool{}
	if action := content.Reassignment; action != nil {
		locked[action.Name] = true
		if action.NewKey != "" {
			newKeys[action.NewKey] = true
		}
	}
	for index, request := range content.NameRequests {
		if request.Kind != FreezeKind {
			continue
		}
		if err := nameRequestError(previousState, validators, height, request); err != nil {
			return fmt.Errorf("freeze %d: %w", index, err)
		}
		if locked[request.Name] {
			return fmt.Errorf("freeze %d: the block's ⅔ action or another freeze is about %q (rules 10, 13)", index, request.Name)
		}
		if !nameRequestSigned(chain, previousState, request) {
			return fmt.Errorf("freeze %d: its signature does not verify under the name's current key", index)
		}
		locked[request.Name] = true
	}
	for name := range evidenceNames {
		locked[name] = true
	}

	// E. The other name requests.
	requested := map[string]bool{}
	fees := map[string]*big.Rat{}
	for index, request := range content.NameRequests {
		if request.Kind == FreezeKind {
			continue
		}
		if err := nameRequestError(previousState, validators, height, request); err != nil {
			return fmt.Errorf("request %d: %w", index, err)
		}
		switch {
		case requested[request.Name]:
			return fmt.Errorf("request %d: another request of the block is about %q (rule 13)", index, request.Name)
		case newKeys[request.NewKey]:
			return fmt.Errorf("request %d: another item of the block binds its new key (rule 14)", index)
		case locked[request.Name] || locked[request.Requestor] || locked[request.Bank]:
			return fmt.Errorf("request %d: its name, requestor or bank is frozen by the block or is the ⅔ action's name (rule 15)", index)
		case !nameRequestSigned(chain, previousState, request):
			return fmt.Errorf("request %d: a signature does not verify under its signer's current key", index)
		}
		requested[request.Name] = true
		if request.NewKey != "" {
			newKeys[request.NewKey] = true
		}
		addToTotal(fees, request.Requestor, request.Fee)
	}

	// F. Payments.
	stateDigestHex := "" // computed for the first pinned payment (see paymentError)
	paying := map[string]bool{}
	for index, payment := range content.Payments {
		if payment.PreviousStateDigest != "" && stateDigestHex == "" {
			stateDigestHex = digestHex(previousState.Digest())
		}
		if err := paymentError(previousState, stateDigestHex, height, payment); err != nil {
			return fmt.Errorf("payment %d: %w", index, err)
		}
		if locked[payment.From] || locked[payment.To] {
			return fmt.Errorf("payment %d: it pays from or to a name the block freezes or the ⅔ action is about (rule 18)", index)
		}
		if !paymentSigned(chain, previousState, payment) {
			return fmt.Errorf("payment %d: its signature does not verify under the payer's current key", index)
		}
		paying[payment.From] = true
	}
	for payer, totalFees := range fees {
		if !FeesFit(AccountAmount(previousState.Accounts, payer), totalFees, paying[payer]) {
			return fmt.Errorf("the fees of %q are above its balance, or it also pays in the block (rules 16, 19)", payer)
		}
	}

	// G. Account updates.
	updated := map[string]bool{}
	for index, update := range content.AccountUpdates {
		if err := updateError(previousState, height, update); err != nil {
			return fmt.Errorf("update %d: %w", index, err)
		}
		if updated[update.Name] || locked[update.Name] || locked[update.Delegate] {
			return fmt.Errorf("update %d: another update of the block is about %q, or it names a locked name (rule 21)", index, update.Name)
		}
		if !updateSigned(chain, previousState, update) {
			return fmt.Errorf("update %d: its signature does not verify under the account's current key", index)
		}
		updated[update.Name] = true
	}

	// H. The one-third cap, only when the block carries evidence.
	if len(evidenceNames) > 0 && !withinOneThirdCap(powerIgnoringFreezes(chain.ValidatorRule, previousState), validators, previousState.Accounts, frozenNames(content)) {
		return errors.New("the block's freezes would bring the frozen names to a third or more of the power (rule 22)")
	}
	return nil
}

// ApplyBlock re-executes block against previousState and returns the resulting LedgerState, or an error
// naming the first rule the block breaks (spec 5.3). The block must be the next block (its height is
// previousState's plus one), build on the block whose hash is previousBlockHash, and name, by its hash,
// the validator set chain.ValidatorRule derives from previousState, the set that signs it. Its content
// must pass contentError. The state it results in must keep every balance that rose within the largest
// balance (rule 23), seat at least one name (rule 24, R9), have the digest the block claims, and seat
// the set the block names, by its hash, as the next one (Phase 4 spec §4.3), so the block's commit is
// its set signing off the set that signs the next block. This is the check every node runs, so a node
// only accepts a block whose applied state is exactly what it recomputes (audit A2).
func ApplyBlock(chain Chain, previousState LedgerState, previousBlockHash [32]byte, block Block) (LedgerState, error) {
	height := previousState.Height + 1
	if block.Height != height {
		return LedgerState{}, fmt.Errorf("block %d is not the next block, %d: an old height would replay its payments (E1)", block.Height, height)
	}
	if block.PreviousBlockHash != previousBlockHash {
		return LedgerState{}, errors.New("the block does not build on the last block: its previous block hash is another")
	}
	validators := chain.ValidatorRule.SetFor(previousState)
	if block.ValidatorsHash != ValidatorsHash(validators.Validators()) {
		return LedgerState{}, errors.New("the block names another signing set: its validators hash is not the hash of the set this state seats")
	}
	if err := contentError(chain, previousState, validators, block.Content); err != nil {
		return LedgerState{}, err
	}
	nextState := applyContent(previousState, validators, block.Content)
	switch {
	case !withinBalanceCap(previousState.Accounts, previousState.Parameters.MaxAccountBalance, nextState.Accounts):
		return LedgerState{}, errors.New("an account ends above the largest balance though its balance rose (rule 23)")
	case !seatsAName(chain.ValidatorRule, nextState):
		return LedgerState{}, errors.New("the block leaves no name seated, and then no block could ever commit again (rule 24)")
	case nextState.Digest() != block.StateDigest:
		return LedgerState{}, errors.New("the block's state digest is not the digest of the state it results in")
	case block.NextValidatorsHash != ValidatorsHash(chain.ValidatorRule.SetFor(nextState).Validators()):
		return LedgerState{}, errors.New("the block names another next signing set: its next validators hash is not the hash of the set the state it results in seats")
	}
	return nextState, nil
}
