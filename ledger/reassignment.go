package ledger

import (
	"errors"
	"math/big"
	"slices"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// ReassignmentVote is one validator's signed approval of a ⅔ name action (its signature is over the
// action's fields, all but the votes and the new key's proof).
type ReassignmentVote struct {
	Validator string // the validator's name; the validator set holds the key that verifies it
	Signature string // over the action's vote fields, domain "reassignment-vote"
}

// Reassignment is the ⅔ name action (spec 4.8): a > ⅔-ratified change to one name, the community's
// deliberate exception to the rules a holder lives by. It can set the name's balance (minting or
// reducing it: the one exception to payment conservation), give it a new key, set or clear its bank of
// record, and freeze or unfreeze it; for a name no account has, it creates the account. It binds the
// name, the governance sequence and its own fields, never the name's key, so a key change that lands
// first cannot make it stale; applying it bumps the sequence, so the certificate is single-use even
// when the state cycles back. It also clears any pending change of the name (R7), and the name sends,
// receives and requests nothing else in the block that carries it (R6). The bundle of validator
// signatures is the proof, verified the same way by every node.
type Reassignment struct { // implements json.Marshaler and json.Unmarshaler, in amount.go
	Name            string
	PreviousAmount  *big.Rat // nil, or the balance the name holds now (0 for a new name); set exactly when NewAmount is
	NewAmount       *big.Rat // nil leaves the balance; else the balance to set
	NewKey          string   // "" leaves the key
	NewKeySignature string   // the new key's proof of possession, domain "reassignment-new-key"; "" when NewKey is ""
	Bank            string   // "" leaves the bank of record; ClearField sets none
	Freeze          bool
	Unfreeze        bool  // only with a new key: every unfreeze re-keys
	Sequence        int64 // the governance sequence this action amends (single-use guard)
	Votes           []ReassignmentVote
}

// voteFields are the fields every vote and the new key's proof sign: everything but the votes and the
// proof, amounts in canonical form ("" for none), booleans as 1 or 0.
func (reassignment Reassignment) voteFields() [][]byte {
	return [][]byte{
		[]byte(reassignment.Name),
		[]byte(FormatAmount(reassignment.PreviousAmount)),
		[]byte(FormatAmount(reassignment.NewAmount)),
		[]byte(reassignment.NewKey),
		[]byte(reassignment.Bank),
		flag(reassignment.Freeze),
		flag(reassignment.Unfreeze),
		[]byte(strconv.FormatInt(reassignment.Sequence, 10)),
	}
}

// flag writes a boolean as a signed field: 1 or 0 (spec 0.2).
func flag(value bool) []byte {
	if value {
		return []byte("1")
	}
	return []byte("0")
}

// ReassignmentID identifies a name action by every field its votes sign, so two actions that differ in
// any of them never share a pool slot or a relay key.
func ReassignmentID(reassignment Reassignment) string {
	return hashFields("reassignment-id", reassignment.voteFields())
}

// SignReassignmentVote is the approval of reassignment by the validator named validatorName, whose key
// signer holds.
func SignReassignmentVote(signer *signing.Signer, validatorName string, reassignment Reassignment) (ReassignmentVote, error) {
	signature, err := signer.SignBase64(signing.DomainReassignmentVote, reassignment.voteFields()...)
	if err != nil {
		return ReassignmentVote{}, err
	}
	return ReassignmentVote{Validator: validatorName, Signature: signature}, nil
}

// SignReassignmentNewKey returns a copy of reassignment with its new key's proof of possession, made by
// newKeySigner, which holds that key.
func SignReassignmentNewKey(newKeySigner *signing.Signer, reassignment Reassignment) (Reassignment, error) {
	signature, err := newKeySigner.SignBase64(signing.DomainReassignmentNewKey, reassignment.voteFields()...)
	reassignment.NewKeySignature = signature
	return reassignment, err
}

// VerifyReassignmentNewKey reports whether reassignment's NewKeySignature is a valid proof of possession
// by its NewKey — used by a validator's wallet before it votes, since the votes do not cover the proof.
func VerifyReassignmentNewKey(scheme signing.Scheme, networkID string, reassignment Reassignment) bool {
	return signing.VerifyBase64(scheme, networkID, reassignment.NewKey, signing.DomainReassignmentNewKey, reassignment.NewKeySignature, reassignment.voteFields()...)
}

// VerifyReassignmentVote reports whether vote is a valid signature by publicKey, the key the caller
// holds for vote.Validator, approving reassignment — used to check votes as they are gathered into a
// certificate.
func VerifyReassignmentVote(scheme signing.Scheme, networkID, publicKey string, reassignment Reassignment, vote ReassignmentVote) bool {
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainReassignmentVote, vote.Signature, reassignment.voteFields()...)
}

// FitsState checks the action against a state with every rule of spec 4.8 but its signatures: the
// rules intake and the pool apply before any signature is checked. It amends the state's governance
// sequence; its name is canonical; it does not freeze and also unfreeze or re-key; its two amounts are
// both set or both nil, and if set the first is the name's balance (0 for a new name) and the second
// is not negative and raises no balance above the largest balance; a new key has the exact length, is
// no name's key, and comes with a proof, which is otherwise empty; a bank is "", ClearField, or another
// name that exists and is not frozen. A new name gets a key and a balance and is neither frozen nor
// unfrozen; a frozen name is unfrozen exactly when it gets a new key; a name that is not frozen is not
// unfrozen. It returns an error naming the first rule the action breaks.
func (reassignment Reassignment) FitsState(state LedgerState) error {
	account, exists := FindAccount(state.Accounts, reassignment.Name)
	bank, bankExists := FindAccount(state.Accounts, reassignment.Bank)
	setsAmount := reassignment.PreviousAmount != nil
	switch {
	case reassignment.Sequence != state.GovernanceSequence:
		return errors.New("it amends another governance sequence")
	case !CanonicalName(reassignment.Name):
		return errors.New("its name is not a canonical name")
	case reassignment.Freeze && (reassignment.Unfreeze || reassignment.NewKey != ""):
		return errors.New("it freezes the name and also unfreezes or re-keys it")
	case setsAmount != (reassignment.NewAmount != nil):
		return errors.New("it sets one amount without the other")
	case setsAmount && reassignment.PreviousAmount.Cmp(AccountAmount(state.Accounts, reassignment.Name)) != 0:
		return errors.New("its previous amount is not the balance the name holds")
	case setsAmount && reassignment.NewAmount.Sign() < 0:
		return errors.New("its new amount is negative")
	case setsAmount && reassignment.NewAmount.Cmp(state.Parameters.MaxAccountBalance) > 0 && reassignment.NewAmount.Cmp(reassignment.PreviousAmount) > 0:
		return errors.New("it raises the balance above the largest balance")
	case reassignment.NewKey != "" && (len(reassignment.NewKey) != signing.PublicKeyBase64Length || keyInUse(state.Accounts, reassignment.NewKey)):
		return errors.New("its new key is not of the exact length, or is already a name's key")
	case (reassignment.NewKey == "") != (reassignment.NewKeySignature == ""):
		return errors.New("it carries a new key without its proof, or a proof without a new key")
	case reassignment.Bank != "" && reassignment.Bank != ClearField && (!bankExists || bank.Frozen || reassignment.Bank == reassignment.Name):
		return errors.New("its bank is not another name that exists and is not frozen")
	case !exists && (reassignment.NewKey == "" || !setsAmount || reassignment.Freeze || reassignment.Unfreeze):
		return errors.New("it creates a name without a key and a balance, or freezes or unfreezes it")
	case exists && account.Frozen && reassignment.Unfreeze != (reassignment.NewKey != ""):
		return errors.New("a frozen name is unfrozen exactly when it is re-keyed")
	case exists && !account.Frozen && reassignment.Unfreeze:
		return errors.New("it unfreezes a name that is not frozen")
	}
	return nil
}

// Valid reports whether the action is ratified against the pre-block state (spec 4.8): it fits the
// state (FitsState), its new key's proof verifies under that key, and its votes make a > ⅔ certificate
// of validators (consensus.CertificateTally): each from a distinct name in the set, verifying under the
// key the set holds for that name, with combined power meeting the quorum. One bad vote makes the whole
// action invalid. A nil validator set is never valid.
func (reassignment Reassignment) Valid(scheme signing.Scheme, networkID string, validators *consensus.ValidatorSet, previousState LedgerState) bool {
	if validators == nil || reassignment.FitsState(previousState) != nil {
		return false
	}
	if reassignment.NewKey != "" && !VerifyReassignmentNewKey(scheme, networkID, reassignment) {
		return false
	}
	tally := consensus.NewCertificateTally(validators)
	for _, vote := range reassignment.Votes {
		if !tally.Add(vote.Validator, func(publicKey string) bool {
			return VerifyReassignmentVote(scheme, networkID, publicKey, reassignment, vote)
		}) {
			return false // a name outside the set, one counted twice, or a signature that does not verify
		}
	}
	return tally.Reached()
}

// applyReassignment applies the action's parts other than a freeze, which the freeze step applies
// (spec 5.3 step I.5): it creates a new name as {Name, NewKey, NewAmount, Bank}; otherwise NewAmount,
// Bank and NewKey replace what they name, and Unfreeze clears the frozen mark. Either way the name's
// pending change is cleared (R7). FitsState has checked it.
func applyReassignment(accounts []Account, reassignment Reassignment) []Account {
	result := append([]Account(nil), accounts...) // copy-on-write: never mutate the caller's (previous-state) accounts
	index, exists := findAccountIndex(result, reassignment.Name)
	if !exists {
		result = slices.Insert(result, index, Account{Name: reassignment.Name, PublicKey: reassignment.NewKey, Amount: new(big.Rat)})
	}
	account := &result[index]
	if reassignment.NewAmount != nil {
		account.Amount = new(big.Rat).Set(reassignment.NewAmount)
	}
	if reassignment.NewKey != "" {
		account.PublicKey = reassignment.NewKey
	}
	applyField(&account.Bank, reassignment.Bank)
	if reassignment.Unfreeze {
		account.Frozen = false
	}
	clearPendingChange(account)
	return result
}
