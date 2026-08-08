package signing

import "encoding/binary"

// Domain is the purpose tag bound into every signature so a signature made for one kind of
// statement cannot be replayed as another (audit C1: cross-protocol signature reuse). Add a
// new constant for each new signable payload type; never sign anything untagged.
type Domain string

const (
	DomainPayment          Domain = "payment"
	DomainValidatorMsg     Domain = "validator-message"
	DomainAccountUpdate    Domain = "account-update"
	DomainServerAuth       Domain = "server-auth"
	DomainBlockRevision    Domain = "block-revision"
	DomainReceipt          Domain = "receipt"
	DomainParameterVote    Domain = "parameter-vote"
	DomainReassignmentVote Domain = "reassignment-vote"
	DomainProposal         Domain = "proposal"
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
	for _, f := range fields {
		size += len(f)
	}
	buf := make([]byte, 0, size)

	write := func(b []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(b)))
		buf = append(buf, n[:]...)
		buf = append(buf, b...)
	}
	write([]byte(protocolTag))
	write([]byte(networkID))
	write([]byte(domain))
	for _, f := range fields {
		write(f)
	}
	return buf
}
