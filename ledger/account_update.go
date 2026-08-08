package ledger

import (
	"strconv"

	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// AccountUpdate is a signed change to an account's own attributes: a re-delegation (point your stake
// at a different validator), a display Name, and a Description. It is carried in a block like a
// payment and applied deterministically. The Name is a first-come-unique claim across the whole
// ledger — that uniqueness rule is what makes this a naming service: a "domain" is just a Name bound
// to a public key, and because you must already have an account to update it, registering a name is
// paid for by the same money layer that secures payments. An empty field leaves that attribute
// unchanged.
type AccountUpdate struct {
	From        string // the account's public key (owner and verification key)
	Name        string // requested unique name (empty = unchanged)
	Delegated   string // validator public key to delegate stake to (empty = unchanged)
	Description string // free text (empty = unchanged)
	Address     string // this validator's gossip address, for the on-chain peer DNS (empty = unchanged)
	BlockID     int64  // the block this update is bound to (replay guard, as for a payment)
	Signature   string // owner signature over the canonical fields (domain "account-update")
}

func (u AccountUpdate) signFields() [][]byte {
	return [][]byte{
		[]byte(u.From),
		[]byte(u.Name),
		[]byte(u.Delegated),
		[]byte(u.Description),
		[]byte(u.Address),
		[]byte(strconv.FormatInt(u.BlockID, 10)),
	}
}

// SignAccountUpdate returns a copy of u signed by its owner in the "account-update" domain.
func SignAccountUpdate(signer *signing.Signer, u AccountUpdate) (AccountUpdate, error) {
	sig, err := signer.SignBase64(signing.DomainAccountUpdate, u.signFields()...)
	if err != nil {
		return u, err
	}
	u.Signature = sig
	return u, nil
}

// VerifyAccountUpdate reports whether u carries a valid signature by its own account (From).
func VerifyAccountUpdate(scheme signing.Scheme, networkID string, u AccountUpdate) bool {
	return signing.VerifyBase64(scheme, networkID, u.From, signing.DomainAccountUpdate, u.Signature, u.signFields()...)
}

// UpdateID is a malleability-proof identity for an update (content, not signature) — used for
// mempool/gossip dedup, mirroring PaymentID.
func UpdateID(u AccountUpdate) string { return hashFields("account-update-id", u.signFields()) }

// MaxAccountNameLength caps a registered name's byte length. Deliberately well below 44 — the
// base64 length of an Ed25519 public key — so a registered name can NEVER be a valid key encoding:
// a payment recipient is either a key or a name with no overlap (a 44-char "name" could otherwise
// be crafted to mask someone's actual key and hijack pay-by-key inputs treated as names, or vice
// versa). Also keeps the registry human-scale.
const MaxAccountNameLength = 32

// applyAccountUpdates applies signed attribute updates to accounts, in order, deterministically:
// re-delegation, description, and Name — the Name only if it is free or already this account's
// (first-come uniqueness = the registry) and within MaxAccountNameLength. An update for a
// non-existent account is dropped (you must hold an account to name/delegate it). Callers pass only
// updates that verify and name this block.
func applyAccountUpdates(scheme signing.Scheme, networkID string, accounts []Account, updates []AccountUpdate, height int64) []Account {
	if len(updates) == 0 {
		return accounts
	}
	updatedAccounts := make([]Account, len(accounts))
	copy(updatedAccounts, accounts)
	accountIndexByKey := make(map[string]int, len(updatedAccounts))
	ownerByName := make(map[string]string, len(updatedAccounts))
	for accountIndex, account := range updatedAccounts {
		accountIndexByKey[account.PublicKey] = accountIndex
		if account.Name != "" {
			ownerByName[account.Name] = account.PublicKey
		}
	}

	for _, update := range updates {
		if update.BlockID != height || !VerifyAccountUpdate(scheme, networkID, update) {
			continue
		}
		accountIndex, accountExists := accountIndexByKey[update.From]
		if !accountExists {
			continue // you must already hold an account to name or delegate it
		}
		account := updatedAccounts[accountIndex]
		if update.Name != "" && len(update.Name) <= MaxAccountNameLength {
			currentOwner, nameAlreadyTaken := ownerByName[update.Name]
			if !nameAlreadyTaken || currentOwner == update.From {
				if account.Name != "" {
					delete(ownerByName, account.Name) // release the previous name
				}
				ownerByName[update.Name] = update.From
				account.Name = update.Name
			}
			// else: the name belongs to someone else — ignore the rename, apply the rest
		}
		// An over-long name (it could mask a public key) is ignored like a taken one; the rest applies.
		if update.Delegated != "" {
			account.ValidatorNode = update.Delegated
		}
		if update.Description != "" {
			account.Description = update.Description
		}
		if update.Address != "" {
			account.Address = update.Address
		}
		updatedAccounts[accountIndex] = account
	}
	return updatedAccounts
}
