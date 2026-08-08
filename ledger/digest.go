package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// StateDigest is a deterministic SHA-256 digest of the account set. It is what the consensus
// layer votes on and commits, so two honest nodes that computed the same state produce the
// same digest and a commit binds the exact applied state (audit A2: a naive vote can be over a
// value derived from a *different* input than what got applied). Accounts are sorted by public
// key and every field is length-prefixed, so the digest is canonical and order-independent.
// (SHA-256, not MD5 -- audit C5.)
func StateDigest(accounts []Account) [32]byte {
	sorted := append([]Account(nil), accounts...)
	// Stable sort so the digest is deterministic even if two accounts share a public key (which the
	// map-keyed processing shouldn't produce, but the digest must not depend on input order if it did).
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].PublicKey < sorted[j].PublicKey })

	h := sha256.New()
	var n [4]byte
	field := func(b []byte) {
		binary.BigEndian.PutUint32(n[:], uint32(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	for _, a := range sorted {
		amt := "0"
		if a.Amount != nil {
			amt = a.Amount.RatString()
		}
		field([]byte(a.PublicKey))
		field([]byte(a.Name))
		field([]byte(amt))
		field([]byte(a.ValidatorNode))
		field([]byte(a.Description))
		field([]byte(a.Address)) // Address is mutable on-chain state (peer DNS) — must be bound, or a
		//                          synced checkpoint could carry rewritten gossip addresses (eclipse).
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
