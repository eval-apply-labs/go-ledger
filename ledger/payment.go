package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// PaymentID is a malleability-proof identity for a payment: a hash of its canonical signed CONTENT
// (the same fields the payer signs) and NOT its signature. Because Ed25519 signing is deterministic,
// two signatures over identical content can only arise from malleation (a third party mangling a
// valid signature into another valid-looking one); keying dedup and replay on the content instead of
// the signature bytes makes that irrelevant — no dependence on the crypto library rejecting
// non-canonical signatures. Fields are length-prefixed so their boundaries are unambiguous.
func PaymentID(p Payment) string { return hashFields("payment-id", p.signFields()) }

// hashFields is a stable, unambiguous hash of a set of signed fields (length-prefixed, so field
// boundaries can't shift). The `tag` is a type discriminator hashed first, so a Payment and an
// AccountUpdate whose field bytes happen to coincide cannot produce the same id (cross-type collision).
// Shared by PaymentID and UpdateID for content-based (malleability-proof) identity.
func hashFields(tag string, fields [][]byte) string {
	h := sha256.New()
	var n [8]byte
	write := func(b []byte) {
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	write([]byte(tag))
	for _, f := range fields {
		write(f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// signFields is the canonical, ordered list of fields a payer authorises. Amount uses its
// exact RatString form so the signed value is unambiguous. Domain separation, network binding
// and length-prefixing are added by the signing layer (signing.Preimage), so a payment
// signature can never be replayed as another kind of statement or on another network.
func (p Payment) signFields() [][]byte {
	var amount string
	if p.Amount != nil {
		amount = p.Amount.RatString()
	}
	return [][]byte{
		[]byte(p.PublicKeyFrom),
		[]byte(p.PublicKeyTo),
		[]byte(p.ToName), // the payer signs the NAME when paying by name; nodes resolve it on-chain
		[]byte(amount),
		[]byte(p.Comment),
		[]byte(strconv.FormatInt(p.BlockID, 10)),
		[]byte(p.PrevStateDigest),
	}
}

// SignPayment returns a copy of p with its Signature set to the payer's signature over the
// canonical payment fields, in the "payment" domain and bound to the signer's network.
func SignPayment(signer *signing.Signer, p Payment) (Payment, error) {
	sig, err := signer.SignBase64(signing.DomainPayment, p.signFields()...)
	if err != nil {
		return p, err
	}
	p.Signature = sig
	return p, nil
}

// VerifyPayment reports whether p carries a valid signature by its own payer. Because
// PublicKeyFrom is both the account identity and the verification key, a payment can only be
// authorised by the holder of that account's key -- you cannot forge a payment "from" someone
// else. This is the check the consensus/ingestion layer must apply before a payment is
// processed, so that block-time validation is never weaker than ingestion.
func VerifyPayment(scheme signing.Scheme, networkID string, p Payment) bool {
	if p.Amount == nil {
		return false
	}
	return signing.VerifyBase64(scheme, networkID, p.PublicKeyFrom, signing.DomainPayment, p.Signature, p.signFields()...)
}

// ValidPayments returns the subset of payments carrying a valid payer signature.
func ValidPayments(scheme signing.Scheme, networkID string, payments []Payment) []Payment {
	out := make([]Payment, 0, len(payments))
	for _, p := range payments {
		if VerifyPayment(scheme, networkID, p) {
			out = append(out, p)
		}
	}
	return out
}
