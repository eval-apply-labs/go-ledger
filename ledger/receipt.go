package ledger

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// Receipt is a validator node's signed acknowledgement that it validated and queued a payment for
// the block the payment names. A payer can show it to a merchant as provisional proof of
// acceptance: the merchant verifies the node's signature and that the node is a known validator,
// and knows a validator has taken the payment on.
//
// It is *provisional*: it attests that one node accepted the payment, not that the network
// committed it. Final assurance is the block's Commit certificate once the block clears — a
// merchant wanting certainty waits for that (or collects receipts from a quorum of validators).
type Receipt struct {
	Node      string // the validator node's public key (base64)
	PaymentID string // the payment's CONTENT id (PaymentID), identifying which payment this acknowledges
	BlockID   int64  // the block the payment is queued for
	Signature string // the node's signature over the fields (domain "receipt", network-bound)
}

func (r Receipt) signFields() [][]byte {
	return [][]byte{
		[]byte(r.Node),
		[]byte(r.PaymentID),
		[]byte(strconv.FormatInt(r.BlockID, 10)),
	}
}

// SignReceipt builds a receipt for a payment, signed by the node in the "receipt" domain. The
// payment is identified by its content id (PaymentID), not its signature bytes — a malleated
// signature is a different byte string for the same payment, so signature bytes are not a stable id.
func SignReceipt(node *signing.Signer, p Payment) (Receipt, error) {
	r := Receipt{Node: node.PublicKeyBase64(), PaymentID: PaymentID(p), BlockID: p.BlockID}
	sig, err := node.SignBase64(signing.DomainReceipt, r.signFields()...)
	if err != nil {
		return Receipt{}, err
	}
	r.Signature = sig
	return r, nil
}

// Verify reports whether the receipt carries a valid signature by r.Node for the network. The
// caller should additionally check that r.Node is a validator it recognises.
func (r Receipt) Verify(scheme signing.Scheme, networkID string) bool {
	return signing.VerifyBase64(scheme, networkID, r.Node, signing.DomainReceipt, r.Signature, r.signFields()...)
}
