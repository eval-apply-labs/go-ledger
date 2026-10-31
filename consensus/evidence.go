package consensus

import "github.com/eval-apply-labs/go-ledger/signing"

// VoteEvidence records that a validator equivocated: it signed two different votes for the same
// (height, round, type). Two conflicting votes signed under one name's key are a transferable,
// durable proof that any node can present later, while the key is still the name's, to freeze the
// name -- unlike a view-local check, which is non-durable and non-transferable (audit A1).
type VoteEvidence struct {
	Name   string // the signer
	First  Vote
	Second Vote
}

// Valid reports whether evidence is genuine proof of a double-sign under publicKey, the key the caller
// holds for evidence.Name (spec 4.12): two votes that both name evidence.Name, for the same (height,
// round, type) but for DIFFERENT block hashes, each signed under that key. Only the key's holder could
// have produced both, so this pair is UNFORGEABLE proof of a double-sign — a proposer cannot fabricate it
// against an honest validator. That is what makes it safe to act on (freeze the name) purely from the
// evidence carried in a block, without having witnessed the round.
func (evidence VoteEvidence) Valid(scheme signing.Scheme, networkID, publicKey string) bool {
	if evidence.First.Validator != evidence.Name || evidence.Second.Validator != evidence.Name {
		return false
	}
	if evidence.First.Height != evidence.Second.Height || evidence.First.Round != evidence.Second.Round || evidence.First.Type != evidence.Second.Type {
		return false
	}
	if evidence.First.BlockHash == evidence.Second.BlockHash {
		return false // identical, not a conflict
	}
	return evidence.First.Verify(scheme, networkID, publicKey) && evidence.Second.Verify(scheme, networkID, publicKey)
}

// ProposalEvidence records that a proposer signed two different proposals for one height and round
// (spec 4.12): two signed proposals naming the proposer, for different block hashes. Like vote evidence,
// it freezes its signer's name when a block carries it.
type ProposalEvidence struct {
	Name   string // the proposer
	First  SignedProposal
	Second SignedProposal
}

// Valid reports whether evidence is genuine proof of a double-sign under publicKey, the key the caller
// holds for evidence.Name (spec 4.12): two signed proposals that both name evidence.Name, for the same
// height and round but for DIFFERENT block hashes, each signed under that key. Two blocks that differ
// only in their nonce are two blocks. A different ValidRound alone, or different signature bytes, is no
// double-sign: the block hashes must differ.
func (evidence ProposalEvidence) Valid(scheme signing.Scheme, networkID, publicKey string) bool {
	first, second := evidence.First, evidence.Second
	if first.Proposer != evidence.Name || second.Proposer != evidence.Name {
		return false
	}
	if first.Height != second.Height || first.Round != second.Round || first.BlockHash == second.BlockHash {
		return false
	}
	return first.Verify(scheme, networkID, publicKey) && second.Verify(scheme, networkID, publicKey)
}
