package ledger

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// AccountUpdate is a signed change to an account's own attributes: a re-delegation (point your stake at
// another name), a Description, and where the name is reached: its PublicAddress and its IPAddresses,
// which are a validator's gossip addresses (the on-chain peer DNS). It is carried in a block like a
// payment and applied deterministically. An empty field leaves that attribute unchanged, and ClearField
// sets it to none. An update never names or renames an account: a name is the account's identity.
type AccountUpdate struct {
	Name          string   // the account; its current key signs
	Delegate      string   // the name this balance backs as stake ("" = unchanged, ClearField = none)
	Description   string   // free text ("" = unchanged, ClearField = none)
	PublicAddress string   // the name's public face, CanonicalAddress ("" = unchanged, ClearField = none)
	IPAddresses   []string // the whole new list, each CanonicalIPAddress (empty = unchanged, []string{ClearField} = none)
	BlockID       int64    // the block this update is bound to (replay guard, as for a payment)
	Signature     string   // the account's signature over the canonical fields (domain "account-update")
}

// ClearField, in an update's Delegate, Description or PublicAddress, or as the one item of its
// IPAddresses, sets that field to none; "" (an empty list) leaves it unchanged. It is never a name
// (CanonicalName) or an IP address (CanonicalIPAddress), so it can always be told from one. Without it
// a delegation, or a published address, could never be withdrawn.
const ClearField = "-"

// signFields are the fields the account signs, the IP addresses last, after their count (listFields).
func (accountUpdate AccountUpdate) signFields() [][]byte {
	fields := [][]byte{
		[]byte(accountUpdate.Name),
		[]byte(accountUpdate.Delegate),
		[]byte(accountUpdate.Description),
		[]byte(accountUpdate.PublicAddress),
		[]byte(strconv.FormatInt(accountUpdate.BlockID, 10)),
	}
	return append(fields, listFields(accountUpdate.IPAddresses)...)
}

// SignAccountUpdate returns a copy of update signed by its account's key in the "account-update" domain.
func SignAccountUpdate(signer *signing.Signer, update AccountUpdate) (AccountUpdate, error) {
	signature, err := signer.SignBase64(signing.DomainAccountUpdate, update.signFields()...)
	if err != nil {
		return update, err
	}
	update.Signature = signature
	return update, nil
}

// VerifyAccountUpdate reports whether update carries a valid signature by publicKey, which the caller
// takes from the state: the current key of the account named update.Name.
func VerifyAccountUpdate(scheme signing.Scheme, networkID, publicKey string, update AccountUpdate) bool {
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainAccountUpdate, update.Signature, update.signFields()...)
}

// UpdateID is a malleability-proof identity for an update (content, not signature) — used for
// mempool/gossip dedup, mirroring PaymentID.
func UpdateID(update AccountUpdate) string {
	return hashFields("account-update-id", update.signFields())
}

// applyAccountUpdates applies signed attribute updates to accounts, in order, deterministically: the
// delegate, the description, the public address and the IP addresses, each left alone when the update
// leaves it empty, and set to none by ClearField. Callers pass only updates that ApplyBlock's checks
// accept (updateError), so each names an existing account; the accounts keep their order.
func applyAccountUpdates(accounts []Account, updates []AccountUpdate) []Account {
	if len(updates) == 0 {
		return accounts
	}
	updatedAccounts := append([]Account(nil), accounts...)
	for _, update := range updates {
		index, found := findAccountIndex(updatedAccounts, update.Name)
		if !found {
			continue
		}
		applyField(&updatedAccounts[index].Delegate, update.Delegate)
		applyField(&updatedAccounts[index].Description, update.Description)
		applyField(&updatedAccounts[index].PublicAddress, update.PublicAddress)
		applyListField(&updatedAccounts[index].IPAddresses, update.IPAddresses)
	}
	return updatedAccounts
}

// applyField sets *field from an update's value: "" leaves it, ClearField empties it, anything else
// replaces it.
func applyField(field *string, value string) {
	switch value {
	case "":
	case ClearField:
		*field = ""
	default:
		*field = value
	}
}

// applyListField sets *field from an update's list: an empty list leaves it, ClearField alone empties
// it, and any other list replaces the whole of it.
func applyListField(field *[]string, value []string) {
	switch {
	case len(value) == 0:
	case clearsList(value):
		*field = nil
	default:
		*field = value
	}
}

// clearsList reports whether an update's list is ClearField alone, which empties the field.
func clearsList(value []string) bool {
	return len(value) == 1 && value[0] == ClearField
}
