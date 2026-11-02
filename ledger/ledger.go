// Package ledger is the pure, side-effect-free core of the hypernode ledger: account state and the
// rules for applying a batch of payments, account updates, name requests, double-sign evidence and
// governance to move committed state from one to the next. It has no I/O and no networking (so it can
// be audited and unit-tested in complete isolation — the point of the Go rewrite), and it verifies
// signatures where the rules require it (block/payment/update/vote validity) via the `signing`
// package, but it holds no keys and does no key management of its own.
package ledger

import (
	"math/big"
	"slices"
	"strconv"
	"strings"
)

// Account is one name and what the ledger holds for it (spec 3.1). Amount is an exact rational, a
// whole number of 1e-8 units (see ParseAmount): no floating-point rounding in the money path. At most
// one of PendingKey and PendingBank is set, and PendingHeight is above 0 exactly when one is.
type Account struct { // implements json.Marshaler and json.Unmarshaler, in amount.go
	Name          string   // the identity, a canonical name (CanonicalName)
	PublicKey     string   // the name's current key (base64); it verifies every message the name signs
	Amount        *big.Rat // the balance; never nil in state
	Bank          string   // the bank of record, a name; "" for none
	Delegate      string   // the name this balance backs as stake; "" for none
	Description   string
	PublicAddress string   // the name's public face, "" or CanonicalAddress (e.g. "https://bank-a.example"): a bank's, which wallets read from, or a website
	IPAddresses   []string // at most MaxIPAddresses, each CanonicalIPAddress: what DNS A and AAAA records hold, and a validator's gossip addresses (the on-chain peer DNS)
	Frozen        bool     // a frozen name neither pays nor is paid, and is never seated
	PendingKey    string   // a key change waiting for PendingHeight; "" for none
	PendingBank   string   // a bank change waiting for PendingHeight; "" for none
	PendingHeight int64    // the height at whose end the pending change takes effect; 0 for none
}

// FindAccount returns the account with the given name, and whether there is one.
func FindAccount(accounts []Account, name string) (Account, bool) {
	index, found := findAccountIndex(accounts, name)
	if !found {
		return Account{}, false
	}
	return accounts[index], true
}

// findAccountIndex returns the index of the account with the given name, and whether there is one.
// Accounts are kept in strictly ascending order of name (see LedgerState.NamesAscending), so it
// searches by halves.
func findAccountIndex(accounts []Account, name string) (int, bool) {
	return slices.BinarySearchFunc(accounts, name, func(account Account, name string) int {
		return strings.Compare(account.Name, name)
	})
}

// AccountAmount returns the balance of the account named name in `accounts`, or zero if there is none.
func AccountAmount(accounts []Account, name string) *big.Rat {
	if account, found := FindAccount(accounts, name); found && account.Amount != nil {
		return account.Amount
	}
	return new(big.Rat)
}

// LedgerState is the full committed state: the accounts, the operating parameters, the governance
// sequence and the height. Its Digest is a block's StateDigest, which the block hash that consensus
// commits covers, so agreement on that hash is agreement on the accounts, the parameters in force, the
// governance sequence AND the height of the last block applied.
//
// GovernanceSequence is a monotonic counter incremented by each applied governance action (a parameter
// change or a ⅔ name action). Every such certificate binds to the sequence it amends and is
// applied only while that sequence is current; applying it bumps the sequence, so the certificate is
// single-use and CANNOT be replayed after state cycles back (the value-bound guards alone could not
// guarantee that). Votes still accumulate freely because the sequence is stable between governance
// actions.
//
// Height is the height of the last block applied, 0 for the genesis. ApplyBlock takes only the block at
// Height + 1, so a block labelled with another height never applies, whoever calls it (E1).
type LedgerState struct {
	Accounts           []Account // strictly ascending byte order of Name (NamesAscending); the digest hashes this order
	Parameters         LedgerParameters
	GovernanceSequence int64
	Height             int64
}

// Digest is the state digest over the accounts, the parameters, the governance sequence and the height
// (spec 3.5).
func (ledgerState LedgerState) Digest() [32]byte {
	accountsDigest := AccountsDigest(ledgerState.Accounts)
	parametersDigest := ledgerState.Parameters.Digest()
	return lengthPrefixedDigest([]byte("go-ledger state v2"), accountsDigest[:], parametersDigest[:],
		[]byte(strconv.FormatInt(ledgerState.GovernanceSequence, 10)), []byte(strconv.FormatInt(ledgerState.Height, 10)))
}

// NamesAscending reports whether the state's accounts are in strictly ascending byte order of name,
// which also proves the names unique. The ledger keeps them so, the digest hashes them in that order,
// and every lookup by name relies on it, so a decoded state (a snapshot, a peer's checkpoint, the state
// a wallet reads) is refused before any other check unless they are.
func (ledgerState LedgerState) NamesAscending() bool {
	for i := 1; i < len(ledgerState.Accounts); i++ {
		if ledgerState.Accounts[i-1].Name >= ledgerState.Accounts[i].Name {
			return false
		}
	}
	return true
}
