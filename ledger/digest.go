package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// AccountsDigest is a deterministic SHA-256 digest of the account set (spec 3.5): the tag "go-ledger
// accounts v2", then every field of every account, length-prefixed, in slice order, the IP addresses
// and then the TLS keys last, each list after its count (listFields). It feeds the state digest (LedgerState.Digest), which the
// block hash the consensus layer votes on and commits covers, so two honest nodes that computed the
// same state produce the same digest and a commit binds the exact applied state (audit A2: a naive vote
// can be over a value derived from a *different* input than what got applied).
// It does not sort: the ledger keeps accounts in strictly ascending order of name, and a decoded state
// out of that order is refused before its digest is taken (LedgerState.NamesAscending). (SHA-256, not
// MD5 -- audit C5.)
func AccountsDigest(accounts []Account) [32]byte {
	fields := [][]byte{[]byte("go-ledger accounts v2")}
	for _, account := range accounts {
		frozen := "0"
		if account.Frozen {
			frozen = "1"
		}
		fields = append(fields,
			[]byte(account.Name),
			[]byte(account.PublicKey),
			[]byte(FormatAmount(account.Amount)), // a nil amount, never in honest state, is "": not a zero balance
			[]byte(account.Bank),
			[]byte(account.Delegate),
			[]byte(account.Description),
			[]byte(account.PublicAddress), // mutable on-chain state, as are the IP addresses and the TLS keys: bound, or a synced checkpoint could rewrite where a name is reached
			[]byte(frozen),
			[]byte(account.PendingKey),
			[]byte(account.PendingBank),
			[]byte(strconv.FormatInt(account.PendingHeight, 10)))
		fields = append(fields, listFields(account.IPAddresses)...)
		fields = append(fields, listFields(account.TLSKeys)...)
	}
	return lengthPrefixedDigest(fields...)
}

// listFields writes a list as signed or hashed fields: its length in decimal, then each item. The
// length comes first so that the fields after the list, the next account's in a digest, are never read
// as more of its items.
func listFields(items []string) [][]byte {
	fields := [][]byte{[]byte(strconv.Itoa(len(items)))}
	for _, item := range items {
		fields = append(fields, []byte(item))
	}
	return fields
}

// lengthPrefixedDigest is SHA-256 over the fields, each written as its 4-byte big-endian length and
// then its bytes (lp() in spec 0.2). A digest's first field is its tag, such as "go-ledger state v2".
func lengthPrefixedDigest(fields ...[]byte) [32]byte {
	hasher := sha256.New()
	var lengthPrefix [4]byte
	for _, field := range fields {
		binary.BigEndian.PutUint32(lengthPrefix[:], uint32(len(field)))
		hasher.Write(lengthPrefix[:])
		hasher.Write(field)
	}
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}
