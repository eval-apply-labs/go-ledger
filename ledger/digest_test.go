package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"strconv"
	"testing"
)

// lengthPrefixedPreimage builds an lp() preimage by hand (spec 0.2): each field as its 4-byte
// big-endian length, then its bytes.
func lengthPrefixedPreimage(fields ...string) []byte {
	var preimage []byte
	for _, field := range fields {
		preimage = binary.BigEndian.AppendUint32(preimage, uint32(len(field)))
		preimage = append(preimage, field...)
	}
	return preimage
}

func TestAccountsDigestChangesWithBalance(t *testing.T) {
	originalAccounts := []Account{{Name: "a", Amount: rational("10")}, {Name: "b", Amount: rational("5")}}
	changedAccounts := []Account{{Name: "a", Amount: rational("11")}, {Name: "b", Amount: rational("5")}} // one balance changed
	if AccountsDigest(originalAccounts) == AccountsDigest(changedAccounts) {
		t.Fatal("digest must change when a balance changes")
	}
}

// A nil amount hashes differently from a zero balance. Zero balances are routine (an account stays at
// zero once it pays out), and a peer that nulled one out of a checkpoint kept the digest, so a node
// adopted a state whose next payment to that account panicked. Now such a state has another digest, so
// the block hash its checkpoint's commit signed does not recompute from it (and the decoder refuses a
// null amount anyway).
func TestAccountsDigestTellsNilAmountFromZero(t *testing.T) {
	honest := buildLedgerState(0, []Account{namedAccount("alice", "0"), namedAccount("payer", "10")}, rational("0.1"))
	tampered := buildLedgerState(0, []Account{{Name: "alice", PublicKey: honest.Accounts[0].PublicKey}, honest.Accounts[1]}, rational("0.1"))
	if AccountsDigest(honest.Accounts) == AccountsDigest(tampered.Accounts) || honest.Digest() == tampered.Digest() {
		t.Fatal("a nil amount must not hash like a zero balance")
	}
}

// The state digest covers the height: two states that differ only in it are different states.
func TestStateDigestCoversTheHeight(t *testing.T) {
	state := buildLedgerState(4, []Account{{Name: "a", Amount: rational("10")}}, rational("0.1"))
	later := state
	later.Height = 5
	if state.Digest() == later.Digest() {
		t.Fatal("two states that differ only in their height have the same digest")
	}
}

// The accounts digest is SHA-256 over lp() fields (spec 0.2 and 3.5), built here by hand: the tag
// "go-ledger accounts v2", then, for each account in slice order, its name, key, amount (the canonical
// decimal of spec 3.3: 10.5, never 21/2), bank, delegate, description, public address, frozen mark (1
// or 0), pending key, pending bank and pending height (in decimal; 0 for none), and last the number of
// its IP addresses, in decimal, and each of them. It hashes the slice order and never sorts: the ledger
// keeps the accounts in name order, and a decoded state out of that order is refused first
// (NamesAscending).
func TestAccountsDigestLayout(t *testing.T) {
	accounts := []Account{
		{Name: "alice", PublicKey: "key-a", Amount: rational("10.5"), Bank: "bank-a", Delegate: "bank-a", Description: "the bakery", Frozen: true},
		{Name: "bank-a", PublicKey: "key-b", Amount: rational("1000"), Delegate: "bank-a", PublicAddress: "https://bank-a.example",
			IPAddresses: []string{"10.0.0.2", "2001:db8::2"}, PendingKey: "key-c", PendingHeight: 86412},
		{Name: "carol", PublicKey: "key-d", Amount: new(big.Rat), Bank: "bank-a", PendingBank: "bank-b", PendingHeight: 7},
	}
	preimage := lengthPrefixedPreimage("go-ledger accounts v2",
		"alice", "key-a", "10.5", "bank-a", "bank-a", "the bakery", "", "1", "", "", "0", "0",
		"bank-a", "key-b", "1000", "", "bank-a", "", "https://bank-a.example", "0", "key-c", "", "86412", "2", "10.0.0.2", "2001:db8::2",
		"carol", "key-d", "0", "bank-a", "", "", "", "0", "", "bank-b", "7", "0")
	if sha256.Sum256(preimage) != AccountsDigest(accounts) {
		t.Fatal("the accounts digest does not follow the spec 3.5 layout")
	}
	if AccountsDigest([]Account{accounts[1], accounts[0], accounts[2]}) == AccountsDigest(accounts) {
		t.Fatal("the accounts digest must hash the accounts in slice order, not sort them")
	}
}

// The state and parameter digests are SHA-256 over lp() fields (spec 0.2 and 3.5), built here by hand.
// The parameter digest: the tag "go-ledger parameters v2", then the ten parameters in order, amounts
// canonical and integers in decimal. The state digest: the tag "go-ledger state v2", the accounts digest
// and the parameter digest (32 raw bytes each), then the governance sequence and the height in decimal.
func TestStateDigestLayout(t *testing.T) {
	parameters := DefaultParameters()
	parameters.TransactionCost = rational("0.25")
	parametersPreimage := lengthPrefixedPreimage("go-ledger parameters v2",
		"0.25", "1000", "3000", "0", strconv.FormatInt(parameters.MaxMessageBytes, 10), "1", "86400", "604800", "100000", "10000000")
	if sha256.Sum256(parametersPreimage) != parameters.Digest() {
		t.Fatal("the parameter digest does not follow the spec 3.5 layout")
	}

	accounts := []Account{{Name: "a", PublicKey: "A", Amount: rational("10.5")}}
	state := LedgerState{Accounts: accounts, Parameters: parameters, GovernanceSequence: 7, Height: 12}
	accountsDigest, parametersDigest := AccountsDigest(accounts), sha256.Sum256(parametersPreimage)
	if sha256.Sum256(lengthPrefixedPreimage("go-ledger state v2", string(accountsDigest[:]), string(parametersDigest[:]), "7", "12")) != state.Digest() {
		t.Fatal("the state digest does not follow the spec 3.5 layout")
	}
}

// The accounts of a state are in strictly ascending byte order of name, which also proves the names
// unique; NamesAscending is the check a decoded state must pass before any other.
func TestNamesAscendingRefusesDisorderAndDuplicates(t *testing.T) {
	for _, testCase := range []struct {
		names []string
		want  bool
	}{
		{nil, true},
		{[]string{"a"}, true},
		{[]string{"a", "a-b", "b"}, true},
		{[]string{"b", "a"}, false},
		{[]string{"a", "a"}, false},
		{[]string{"a-b", "a"}, false},
	} {
		var accounts []Account
		for _, name := range testCase.names {
			accounts = append(accounts, Account{Name: name})
		}
		if got := (LedgerState{Accounts: accounts}).NamesAscending(); got != testCase.want {
			t.Errorf("NamesAscending(%v) = %v, want %v", testCase.names, got, testCase.want)
		}
	}
}
