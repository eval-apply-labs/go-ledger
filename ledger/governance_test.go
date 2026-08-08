package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// A parameter change is ratified only by a > ⅔ stake quorum, applies to committed state, and cannot
// be replayed once the parameters it amends have moved on.
func TestParameterChangeRequiresTwoThirds(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 3)
	vals := make([]consensus.Validator, 3)
	for i := range signers {
		s, _ := signing.Generate(scheme, networkID)
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	validators := consensus.NewValidatorSet(vals) // total 3 → quorum floor(6/3)+1 = 3

	// A well-formed timing profile (step timeout comfortably covers the fan-out); the change keeps it.
	current := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: 3000, MaxPeers: 0}
	proposed := LedgerParameters{TransactionCost: big.NewRat(1, 2), BlockIntervalMs: 1000, StepTimeoutMs: 3000, MaxPeers: 0} // raise the fee to 0.5
	previousDigest := current.Digest()
	voteBy := func(s *signing.Signer) ParameterVote {
		v, _ := SignParameterVote(s, proposed, previousDigest, 0) // genesis governance sequence 0
		return v
	}

	// All three approve (>= quorum): the change is valid and, applied in a block, amends the params.
	ratified := &ParameterChange{NewParameters: proposed, PreviousParametersDigest: previousDigest,
		Votes: []ParameterVote{voteBy(signers[0]), voteBy(signers[1]), voteBy(signers[2])}}
	if !ratified.Valid(scheme, networkID, validators, current, 0) {
		t.Fatal("a change approved by all validators should be ratified")
	}
	previousState := LedgerState{Parameters: current}
	blk := ProposeBlock(scheme, networkID, 1, previousState, BlockContent{ParameterChange: ratified}, validators)
	nextState, ok := ApplyBlock(scheme, networkID, previousState, blk, validators)
	if !ok {
		t.Fatal("a block with a ratified parameter change should apply")
	}
	if nextState.Parameters.TransactionCost.Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("parameters not amended: cost = %s", nextState.Parameters.TransactionCost.RatString())
	}

	// Only one of three approves (< quorum): not ratified, and a block carrying it is rejected.
	subQuorum := &ParameterChange{NewParameters: proposed, PreviousParametersDigest: previousDigest,
		Votes: []ParameterVote{voteBy(signers[0])}}
	if subQuorum.Valid(scheme, networkID, validators, current, 0) {
		t.Fatal("a sub-⅔ change must not be ratified")
	}
	rejected := blk
	rejected.ParameterChange = subQuorum
	if _, ok := ApplyBlock(scheme, networkID, previousState, rejected, validators); ok {
		t.Fatal("ApplyBlock must reject a block whose parameter change lacks a ⅔ quorum")
	}

	// Replay guard: applying the change bumped the governance sequence, so the ratified certificate
	// (bound to sequence 0) can never re-apply — even against the exact parameters it amended.
	if nextState.GovernanceSequence != 1 {
		t.Fatalf("applying a governance action should bump the sequence to 1, got %d", nextState.GovernanceSequence)
	}
	if ratified.Valid(scheme, networkID, validators, current, nextState.GovernanceSequence) {
		t.Fatal("a certificate must not re-apply once the governance sequence has advanced (replay guard)")
	}
}

// The window and the fan-out are coupled: capping peers raises the gossip dimension, so the step
// timeout must grow to cover it. A profile that breaks the coupling is rejected even with a full
// quorum; the same fan-out with a long-enough timeout is accepted.
func TestParameterChangeEnforcesTimingCoupling(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 3)
	vals := make([]consensus.Validator, 3)
	for i := range signers {
		s, _ := signing.Generate(scheme, networkID)
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	validators := consensus.NewValidatorSet(vals)

	current := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: 3000, MaxPeers: 0}
	previousDigest := current.Digest()
	// Capping fan-out at 2 among 3 nodes forces dimension 2, so the timeout must cover 2 hops.
	makeChange := func(profile LedgerParameters) *ParameterChange {
		var votes []ParameterVote
		for _, signer := range signers {
			vote, _ := SignParameterVote(signer, profile, previousDigest, 0)
			votes = append(votes, vote)
		}
		return &ParameterChange{NewParameters: profile, PreviousParametersDigest: previousDigest, Votes: votes}
	}
	tooShort := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: 300, MaxPeers: 2}
	longEnough := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: 600, MaxPeers: 2}
	if makeChange(tooShort).Valid(scheme, networkID, validators, current, 0) {
		t.Fatal("a profile whose step timeout can't cover the fan-out must be rejected even with a full quorum")
	}
	if !makeChange(longEnough).Valid(scheme, networkID, validators, current, 0) {
		t.Fatal("the same fan-out with a long-enough step timeout should be accepted")
	}
}
