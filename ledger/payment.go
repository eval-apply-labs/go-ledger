package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// Payment moves Amount from the account named From to the account named To. From's current key signs
// it (see SignPayment), and every node looks that key up in the state, so a payment can come only from
// the holder of From's key. Comment, BlockID and PreviousStateDigest are all part of the signed fields.
type Payment struct { // implements json.Marshaler and json.Unmarshaler, in amount.go
	From    string // the payer's name; its current key signs
	To      string // the payee's name
	Amount  *big.Rat
	Comment string
	BlockID int64 // the height of the one block that may carry it (the replay guard)
	// PreviousStateDigest optionally pins the payment to a specific chain history: if non-empty, the
	// payment is included only in a block built on a state whose state digest (LedgerState.Digest, in
	// hex) it is (E38). Intake takes a pin only for the next block, the one built on the committed state,
	// whose digest /hdls/resolve serves. Empty = BlockID-only (the default, and all an ordinary user
	// needs). This is the fork-resistant tier: a
	// payment pinned to one history cannot be replayed on a fork whose block at the same height had a
	// different predecessor. It is meaningful only when the payer TRUSTS the node reporting the live
	// tip — in practice their OWN node: pinning to a tip an untrusted node told you buys nothing. So
	// this tier goes with self-hosting — run a node if you hold significant value; everyone else
	// relies on BlockID, which already prevents ordinary replay.
	PreviousStateDigest string
	Signature           string // base64 payer signature over the canonical fields (domain "payment")
}

// PaymentID is a malleability-proof identity for a payment: a hash of its canonical signed CONTENT
// (the same fields the payer signs) and NOT its signature. One payment can carry many valid
// signatures: a signer outside this code need not sign deterministically (the Scheme requires it only
// of this code's own keys), and a third party might mangle a valid signature into another valid one.
// Keying dedup and replay on the content instead of the signature bytes makes both irrelevant — no
// dependence on how the payer signed, or on the crypto library rejecting non-canonical signatures.
// Fields are length-prefixed so their boundaries are unambiguous.
func PaymentID(payment Payment) string { return hashFields("payment-id", payment.signFields()) }

// hashFields is a stable, unambiguous hash of a set of signed fields (length-prefixed, so field
// boundaries can't shift). The `tag` is a type discriminator hashed first, so a Payment and an
// AccountUpdate whose field bytes happen to coincide cannot produce the same id (cross-type collision).
// Shared by PaymentID, UpdateID, NameRequestID and ReassignmentID for content-based (malleability-proof)
// identity.
func hashFields(tag string, fields [][]byte) string {
	hasher := sha256.New()
	var lengthPrefix [8]byte
	write := func(data []byte) {
		binary.BigEndian.PutUint64(lengthPrefix[:], uint64(len(data)))
		hasher.Write(lengthPrefix[:])
		hasher.Write(data)
	}
	write([]byte(tag))
	for _, field := range fields {
		write(field)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// signFields is the canonical, ordered list of fields a payer authorises. Amount is in its one
// canonical decimal form (FormatAmount), so the signed value is unambiguous. Domain separation, network
// binding and length-prefixing are added by the signing layer (signing.Preimage), so a payment
// signature can never be replayed as another kind of statement or on another network.
func (payment Payment) signFields() [][]byte {
	return [][]byte{
		[]byte(payment.From),
		[]byte(payment.To),
		[]byte(FormatAmount(payment.Amount)),
		[]byte(payment.Comment),
		[]byte(strconv.FormatInt(payment.BlockID, 10)),
		[]byte(payment.PreviousStateDigest),
	}
}

// SignPayment returns a copy of payment with its Signature set to the payer's signature over the
// canonical payment fields, in the "payment" domain and bound to the signer's network.
func SignPayment(signer *signing.Signer, payment Payment) (Payment, error) {
	signature, err := signer.SignBase64(signing.DomainPayment, payment.signFields()...)
	if err != nil {
		return payment, err
	}
	payment.Signature = signature
	return payment, nil
}

// VerifyPayment reports whether payment carries a valid signature by publicKey. The caller passes the
// key the state holds for payment.From, the payer's current key, so a payment can only be authorised by
// the holder of that name's key -- you cannot forge a payment "from" someone else. Intake and every
// block check apply it before a payment is processed, so block-time validation is never weaker than
// ingestion.
func VerifyPayment(scheme signing.Scheme, networkID, publicKey string, payment Payment) bool {
	if payment.Amount == nil {
		return false
	}
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainPayment, payment.Signature, payment.signFields()...)
}

// ProcessPayments applies a batch of payments to a set of accounts and returns the new account set,
// in the same order: a payment moves money between existing names and never creates or deletes an
// account. It is a pure function of (accounts, payments, transactionCost); the input slices are not
// mutated.
//
// Integrity invariant (audit B1 / B2): a payment may move value only if its amount is at least the
// transaction cost (so never negative; a zero amount passes only at a zero cost, and moves nothing) AND
// its payer and payee are existing accounts. Checking neither allows two ways to MINT supply:
//
//	B1  a negative amount is never overdraft-banned and the "credit - debit" step subtracts
//	    the negative, ballooning the payer's balance;
//	B2  a non-existent payer is synthesised at zero, driven negative and dropped, so the
//	    payee's credit is minted from nothing.
//
// Rule 17 (paymentError, then PaymentFitsState) already refuses such a payment before a block applies,
// in contentError and in ProposeBlock, and the cost has passed Acceptable. The filters below and the
// cost clamp are a second line of defence on the money path, so neither mint is representable even
// for a caller that skips those checks.
func ProcessPayments(accounts []Account, payments []Payment, transactionCost *big.Rat) []Account {
	if transactionCost == nil || transactionCost.Sign() < 0 {
		// Treat an unset cost as zero rather than panicking on Cmp. A NEGATIVE cost is clamped for the
		// same conservation reason: it would credit each payee Amount-cost > Amount (minting |cost| per
		// payment) and let negative amounts through the below-cost filter. The genesis file and every
		// parameter change pass LedgerParameters.Acceptable, which refuses a negative cost; this keeps
		// supply conserved even for a caller that bypasses it.
		transactionCost = new(big.Rat)
	}
	indexByName := make(map[string]int, len(accounts))
	for index, account := range accounts {
		indexByName[account.Name] = index
	}

	// B1/B2 guard: keep only strictly-valid payments; and collapse duplicate CONTENT so a malleated
	// signature (a second valid encoding of the same payment) can never debit the payer twice, even
	// in a hand-crafted block. Deterministic (first occurrence wins over the block's fixed order), so
	// every node computes the same result. Note: two payments the payer genuinely means as distinct
	// must differ in a field (e.g. the Comment) — identical content collapses to one.
	seen := make(map[string]bool)
	var acceptedPayments []Payment
	for _, payment := range payments {
		if payment.Amount == nil || payment.Amount.Cmp(transactionCost) < 0 {
			continue // nil, or below the cost (never negative): so every negative is excluded, and zero unless the cost is 0
		}
		_, payerExists := indexByName[payment.From]
		_, payeeExists := indexByName[payment.To]
		if !payerExists || !payeeExists {
			continue // both names must exist: a payment never creates an account
		}
		paymentID := PaymentID(payment)
		if seen[paymentID] {
			continue // duplicate content (e.g. a malleated-signature copy) -- apply once
		}
		seen[paymentID] = true
		acceptedPayments = append(acceptedPayments, payment)
	}

	// Ban any payer whose total outgoing would overdraw their balance. Deliberate (see findings.md,
	// design decision): honest intake refuses such payments up front (the node's payment intake), and a
	// block carries no payment larger than its payer's balance on its own (PaymentFitsState), so a
	// committed overdraw means two or more signed payments that together overdraw: a misused/cloned
	// key. The ban destroys the payer's pre-block balance and cancels its payments; credits it receives
	// in the same block still land.
	outgoing := map[string]*big.Rat{}
	for _, payment := range acceptedPayments {
		addToTotal(outgoing, payment.From, payment.Amount)
	}
	banned := map[string]bool{}
	for payerName, totalOutgoing := range outgoing {
		if accounts[indexByName[payerName]].Amount.Cmp(totalOutgoing) < 0 {
			banned[payerName] = true
		}
	}

	debit := map[string]*big.Rat{}  // payer -> total sent
	credit := map[string]*big.Rat{} // payee -> total received (net of the transaction cost)
	for _, payment := range acceptedPayments {
		if banned[payment.From] {
			continue
		}
		addToTotal(debit, payment.From, payment.Amount)
		if payment.Amount.Cmp(transactionCost) > 0 {
			addToTotal(credit, payment.To, new(big.Rat).Sub(payment.Amount, transactionCost))
		}
	}

	result := append([]Account(nil), accounts...)
	for index, account := range result {
		totalReceived, credited := credit[account.Name]
		totalSent, debited := debit[account.Name]
		if !credited && !debited && !banned[account.Name] {
			continue
		}
		newAmount := new(big.Rat)
		if !banned[account.Name] {
			newAmount.Set(account.Amount)
		}
		if credited {
			newAmount.Add(newAmount, totalReceived)
		}
		if debited {
			newAmount.Sub(newAmount, totalSent)
		}
		result[index].Amount = newAmount
	}
	return result
}

func addToTotal(totals map[string]*big.Rat, name string, amount *big.Rat) {
	if currentTotal, found := totals[name]; found {
		currentTotal.Add(currentTotal, amount)
	} else {
		totals[name] = new(big.Rat).Set(amount)
	}
}
