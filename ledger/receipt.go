package ledger

import (
	"strconv"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// Receipt is a node's signed acknowledgement that it validated and queued a payment for the block the
// payment names; every node signs one, an unnamed observer with Node "". A payer can show it to a
// merchant as provisional proof of acceptance: the merchant verifies the node's signature under the
// key it trusts for that node, and knows that node has taken the payment on.
//
// It is *provisional*: it attests that one node accepted the payment, not that the network
// committed it. Final assurance is the block's Commit certificate once the block clears — a
// merchant wanting certainty waits for that (or collects receipts from a quorum of validators).
type Receipt struct {
	Node      string // the node's name ("" for an unnamed observer); the reader holds the key that verifies it
	PaymentID string // the payment's CONTENT id (PaymentID), identifying which payment this acknowledges
	BlockID   int64  // the block the payment is queued for
	Signature string // the node's signature over the fields (domain "receipt", network-bound)
}

func (receipt Receipt) signFields() [][]byte {
	return [][]byte{
		[]byte(receipt.Node),
		[]byte(receipt.PaymentID),
		[]byte(strconv.FormatInt(receipt.BlockID, 10)),
	}
}

// SignReceipt builds a receipt for a payment, signed by the node named nodeName in the "receipt"
// domain. The payment is identified by its content id (PaymentID), not its signature bytes — a
// malleated signature is a different byte string for the same payment, so signature bytes are not a
// stable id.
func SignReceipt(nodeSigner *signing.Signer, nodeName string, payment Payment) (Receipt, error) {
	receipt := Receipt{Node: nodeName, PaymentID: PaymentID(payment), BlockID: payment.BlockID}
	signature, err := nodeSigner.SignBase64(signing.DomainReceipt, receipt.signFields()...)
	if err != nil {
		return Receipt{}, err
	}
	receipt.Signature = signature
	return receipt, nil
}

// Verify reports whether the receipt carries a valid signature by publicKey for the network. The caller
// passes the key it trusts for receipt.Node, a validator it recognises.
func (receipt Receipt) Verify(scheme signing.Scheme, networkID, publicKey string) bool {
	return signing.VerifyBase64(scheme, networkID, publicKey, signing.DomainReceipt, receipt.Signature, receipt.signFields()...)
}
