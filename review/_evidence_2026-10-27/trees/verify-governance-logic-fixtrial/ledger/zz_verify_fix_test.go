package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestVerifyFixC12ExclusionRule(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	var signers []*signing.Signer
	var vals []consensus.Validator
	for i := 0; i < 3; i++ {
		s, _ := signing.Generate(scheme, network)
		signers = append(signers, s)
		vals = append(vals, consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1})
	}
	set := consensus.NewValidatorSet(vals)
	thief, _ := signing.Generate(scheme, network)
	stash, _ := signing.Generate(scheme, network)
	other, _ := signing.Generate(scheme, network)
	T, X, O := thief.PublicKeyBase64(), stash.PublicKeyBase64(), other.PublicKeyBase64()
	state := LedgerState{Accounts: []Account{
		{PublicKey: T, Name: "thief", Amount: big.NewRat(1000, 1), ValidatorNode: T},
		{PublicKey: O, Amount: big.NewRat(50, 1), ValidatorNode: O},
	}, Parameters: LedgerParameters{TransactionCost: big.NewRat(1, 10), StepTimeoutMs: 1000}}
	cert := &Reassignment{Account: T, PreviousAmount: big.NewRat(1000, 1), NewAmount: new(big.Rat)}
	for _, s := range signers {
		v, _ := SignReassignmentVote(s, T, cert.PreviousAmount, cert.NewAmount, 0)
		cert.Votes = append(cert.Votes, v)
	}
	out, _ := SignPayment(thief, Payment{PublicKeyFrom: T, PublicKeyTo: X, Amount: big.NewRat(1000, 1), BlockID: 1})
	inByName, _ := SignPayment(other, Payment{PublicKeyFrom: O, ToName: "thief", Amount: big.NewRat(10, 1), BlockID: 1})
	unrelated, _ := SignPayment(other, Payment{PublicKeyFrom: O, PublicKeyTo: X, Amount: big.NewRat(5, 1), BlockID: 1})
	blk := ProposeBlock(scheme, network, 1, state, BlockContent{Payments: []Payment{out, inByName, unrelated}, Reassignment: cert}, set)
	next, ok := ApplyBlock(scheme, network, state, blk, set)
	t.Logf("proposed: reassignment=%v payments=%d ; applies=%v ; T=%s X=%s O=%s", blk.Reassignment != nil, len(blk.Payments), ok,
		AccountAmount(next.Accounts, T).RatString(), AccountAmount(next.Accounts, X).RatString(), AccountAmount(next.Accounts, O).RatString())
	if !ok || blk.Reassignment == nil || len(blk.Payments) != 1 || AccountAmount(next.Accounts, X).Cmp(big.NewRat(49, 10)) != 0 {
		t.Fatalf("exclusion rule: want reassignment carried, only the unrelated payment kept")
	}
	// A hand-crafted block carrying both must be rejected by ApplyBlock.
	crafted := blk
	crafted.Payments = []Payment{out}
	if _, ok := ApplyBlock(scheme, network, state, crafted, set); ok {
		t.Fatalf("ApplyBlock accepted a reassignment + same-block outflow")
	}
}

func TestVerifyFixC13Ceiling(t *testing.T) {
	ok := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 86_400_000, StepTimeoutMs: 86_400_000}
	bad := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 86_400_000_000, StepTimeoutMs: 3000}
	if !ok.Acceptable(4) || bad.Acceptable(4) {
		t.Fatalf("ceiling: pigeon-mode day must pass (%v), 2.7y must fail (%v)", ok.Acceptable(4), bad.Acceptable(4))
	}
}
