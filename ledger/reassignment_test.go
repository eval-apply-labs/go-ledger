package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A balance reassignment is ratified only by a > ⅔ stake quorum, applies to committed state (it may
// mint or reduce), rejects a negative target, and cannot be replayed once the balance has moved on.
func TestReassignmentRequiresTwoThirds(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 3)
	vals := make([]consensus.Validator, 3)
	for i := range signers {
		s, _ := signing.Generate(scheme, networkID)
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	validators := consensus.NewValidatorSet(vals) // total 3 → quorum 3

	target := "grantee-public-key"
	accounts := []Account{
		{PublicKey: signers[0].PublicKeyBase64(), Amount: rat("100"), ValidatorNode: signers[0].PublicKeyBase64()},
		{PublicKey: target, Amount: rat("10"), Name: "coop"},
	}
	previousAmount := rat("10")
	newAmount := rat("250") // a mint: raise the co-op's balance to 250
	voteBy := func(s *signing.Signer) ReassignmentVote {
		v, _ := SignReassignmentVote(s, target, previousAmount, newAmount, 0) // genesis governance sequence 0
		return v
	}

	// All three approve: valid, and applied in a block the balance is overridden.
	ratified := &Reassignment{Account: target, PreviousAmount: previousAmount, NewAmount: newAmount,
		Votes: []ReassignmentVote{voteBy(signers[0]), voteBy(signers[1]), voteBy(signers[2])}}
	if !ratified.Valid(scheme, networkID, validators, accounts, 0) {
		t.Fatal("a reassignment approved by all validators should be ratified")
	}
	previousState := LedgerState{Accounts: accounts, Parameters: LedgerParameters{TransactionCost: rat("0.1")}}
	blk := ProposeBlock(scheme, networkID, 1, previousState, BlockContent{Reassignment: ratified}, validators)
	nextState, ok := ApplyBlock(scheme, networkID, previousState, blk, validators)
	if !ok {
		t.Fatal("a block with a ratified reassignment should apply")
	}
	if AccountAmount(nextState.Accounts, target).Cmp(rat("250")) != 0 {
		t.Fatalf("balance not overridden: got %s", AccountAmount(nextState.Accounts, target).RatString())
	}
	// The previous state was not mutated (copy-on-write).
	if AccountAmount(previousState.Accounts, target).Cmp(rat("10")) != 0 {
		t.Fatal("applying a reassignment must not mutate the previous state")
	}

	// Only one of three approves (< quorum): not ratified, and a block carrying it is rejected.
	subQuorum := &Reassignment{Account: target, PreviousAmount: previousAmount, NewAmount: newAmount,
		Votes: []ReassignmentVote{voteBy(signers[0])}}
	if subQuorum.Valid(scheme, networkID, validators, accounts, 0) {
		t.Fatal("a sub-⅔ reassignment must not be ratified")
	}
	rejected := blk
	rejected.Reassignment = subQuorum
	if _, ok := ApplyBlock(scheme, networkID, previousState, rejected, validators); ok {
		t.Fatal("ApplyBlock must reject a block whose reassignment lacks a ⅔ quorum")
	}

	// Non-negative guard: a negative target amount is never valid.
	negative := &Reassignment{Account: target, PreviousAmount: previousAmount, NewAmount: rat("-5"),
		Votes: []ReassignmentVote{voteBy(signers[0]), voteBy(signers[1]), voteBy(signers[2])}}
	if negative.Valid(scheme, networkID, validators, accounts, 0) {
		t.Fatal("a reassignment to a negative balance must be rejected")
	}

	// Replay guard: once the balance has moved on, the certificate no longer amends it.
	movedOn := []Account{{PublicKey: target, Amount: rat("11")}}
	if ratified.Valid(scheme, networkID, validators, movedOn, 0) {
		t.Fatal("a certificate must not re-apply against a balance it did not amend (replay guard)")
	}
}

// The specific replay the review found: a reassignment guarded only by the previous BALANCE can be
// replayed whenever the balance cycles back — a newcomer grant (prev=0 → X) re-mints X forever once
// the grantee spends to zero. The governance SEQUENCE closes it: applying bumps the sequence, so the
// certificate (bound to the old sequence) can never re-apply, even when the balance returns to prev.
func TestReassignmentSequenceGuardBlocksCycleReplay(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 3)
	vals := make([]consensus.Validator, 3)
	for i := range signers {
		s, _ := signing.Generate(scheme, networkID)
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	validators := consensus.NewValidatorSet(vals)

	newcomer := "newcomer-public-key"
	grant := &Reassignment{Account: newcomer, PreviousAmount: rat("0"), NewAmount: rat("500"), Sequence: 0}
	for _, s := range signers {
		v, _ := SignReassignmentVote(s, newcomer, rat("0"), rat("500"), 0)
		grant.Votes = append(grant.Votes, v)
	}

	// At sequence 0 with the newcomer absent (balance 0), the grant is valid.
	funders := []Account{{PublicKey: signers[0].PublicKeyBase64(), Amount: rat("100"), ValidatorNode: signers[0].PublicKeyBase64()}}
	if !grant.Valid(scheme, networkID, validators, funders, 0) {
		t.Fatal("the grant should be valid against sequence 0 when the newcomer holds 0")
	}

	// The grantee later spends everything back to zero (absent again). The balance-only guard would now
	// re-accept the SAME certificate — but the sequence has advanced, so it must be rejected.
	afterSpend := funders // newcomer absent again → AccountAmount == 0 == PreviousAmount
	if grant.Valid(scheme, networkID, validators, afterSpend, 1) {
		t.Fatal("a spent-back-to-zero grant must NOT re-mint: the governance sequence advanced (replay guard)")
	}
}
