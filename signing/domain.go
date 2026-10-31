package signing

import "encoding/binary"

// Domain is the purpose tag bound into every signature so a signature made for one kind of
// statement cannot be replayed as another (audit C1: cross-protocol signature reuse). Add a
// new constant for each new signable payload type; never sign anything untagged.
type Domain string

// The domains. A name request's requestor signs in the domain named by the request's kind (registration,
// key-change, bank-change or freeze), so a signer that sees only the domain can tell a bank paying for
// a customer's registration from a holder changing its own key or bank.
const (
	DomainPayment            Domain = "payment"
	DomainAccountUpdate      Domain = "account-update"
	DomainRegistration       Domain = "registration"
	DomainKeyChange          Domain = "key-change"
	DomainBankChange         Domain = "bank-change"
	DomainFreeze             Domain = "freeze"
	DomainNameRequestBank    Domain = "name-request-bank"    // the co-signing bank, over a name request
	DomainNameRequestNewKey  Domain = "name-request-new-key" // the new key, as proof of possession, over a registration or key change
	DomainBlockRevision      Domain = "block-revision"
	DomainReceipt            Domain = "receipt"
	DomainParameterVote      Domain = "parameter-vote"
	DomainReassignmentVote   Domain = "reassignment-vote"
	DomainReassignmentNewKey Domain = "reassignment-new-key" // the new key, as proof of possession, over a ⅔ name action
	DomainProposal           Domain = "proposal"
)

// protocolTag is a fixed prefix so a hypernode signature can never collide with a signature
// produced by some other protocol that happens to reuse the same key material.
const protocolTag = "hypernode/v1"

// Preimage builds the canonical, unambiguous byte string that is actually signed and verified
// for (networkID, domain, fields). Every component -- the protocol tag, the network id, the
// domain, and each field -- is length-prefixed with a 4-byte big-endian length, so:
//
//   - a value containing a delimiter can never shift a field boundary (audit C7: field
//     injection via ':'-joined strings);
//   - the network id is part of the signed bytes, blocking testnet<->mainnet and cross-fork
//     replay (audit C6);
//   - the domain is part of the signed bytes, blocking cross-protocol reuse (audit C1).
//
// The verifier must reconstruct the exact same field sequence; any difference yields a
// different preimage and therefore a failed verification.
func Preimage(networkID string, domain Domain, fields ...[]byte) []byte {
	// Pre-size: 4-byte length header per component (3 fixed + fields) plus the bytes.
	size := 4 * (3 + len(fields))
	size += len(protocolTag) + len(networkID) + len(domain)
	for _, field := range fields {
		size += len(field)
	}
	preimage := make([]byte, 0, size)

	write := func(component []byte) {
		var lengthPrefix [4]byte
		binary.BigEndian.PutUint32(lengthPrefix[:], uint32(len(component)))
		preimage = append(preimage, lengthPrefix[:]...)
		preimage = append(preimage, component...)
	}
	write([]byte(protocolTag))
	write([]byte(networkID))
	write([]byte(domain))
	for _, field := range fields {
		write(field)
	}
	return preimage
}
