package ledger

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): StateDigest encodes a nil Amount as "0" (digest.go:29-31), the
// same bytes as a real zero. A zero-balance account is reachable in committed state (a ratified
// reassignment to 0 keeps the account — applyReassignment never deletes). So two DIFFERENT account
// sets — Amount=0 vs Amount=nil — share one committed digest, and node checkpoint verification
// (digest + commit only) cannot tell them apart. The nil variant then panics the state transition
// (ledger.go:202 Set(nil) on a credit; ledger.go:166 Cmp on a debit). Asserts digest injectivity
// and no-panic; FAILS on the current code.
func TestPoCDigestConflatesNilAndZeroAmount(t *testing.T) {
	scheme, networkID := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 3)
	vals := make([]consensus.Validator, 3)
	for i := range signers {
		s, _ := signing.Generate(scheme, networkID)
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	validators := consensus.NewValidatorSet(vals)
	payer := signers[0]
	P := payer.PublicKeyBase64()
	target := "coop-public-key"
	accounts := []Account{
		{PublicKey: P, Amount: rat("100"), ValidatorNode: P},
		{PublicKey: target, Amount: rat("10"), Name: "coop"},
	}
	// A ⅔-ratified reassignment of the co-op to zero (a documented "reduce a balance" use).
	zero := new(big.Rat)
	var votes []ReassignmentVote
	for _, s := range signers {
		v, _ := SignReassignmentVote(s, target, rat("10"), zero, 0)
		votes = append(votes, v)
	}
	prev := LedgerState{Accounts: accounts, Parameters: LedgerParameters{TransactionCost: rat("0.1")}}
	blk := ProposeBlock(scheme, networkID, 1, prev, BlockContent{Reassignment: &Reassignment{Account: target, PreviousAmount: rat("10"), NewAmount: zero, Votes: votes}}, validators)
	committed, ok := ApplyBlock(scheme, networkID, prev, blk, validators)
	if !ok || !has(committed.Accounts, target) {
		t.Fatal("setup: the reassignment-to-zero block should apply and keep the (zero-balance) account")
	}

	// What a checkpoint server can hand a syncing node: the same accounts, JSON-encoded, with the
	// zero amount replaced by null. It decodes to a nil *big.Rat.
	raw, _ := json.Marshal(committed.Accounts)
	var tampered []Account
	_ = json.Unmarshal(raw, &tampered)
	for i := range tampered {
		if tampered[i].PublicKey == target {
			tampered[i].Amount = nil // == decoding {"Amount": null}
		}
	}
	tamperedState := LedgerState{Accounts: tampered, Parameters: committed.Parameters, GovernanceSequence: committed.GovernanceSequence}
	sameDigest := tamperedState.Digest() == committed.Digest()
	t.Logf("committed digest == digest with nil amount: %v", sameDigest)

	// The next block pays the co-op (by name — the account kept its name). Honest state: fine.
	pay, _ := SignPayment(payer, Payment{PublicKeyFrom: P, ToName: "coop", Amount: rat("5"), BlockID: 2})
	honestBlk := ProposeBlock(scheme, networkID, 2, committed, BlockContent{Payments: []Payment{pay}}, validators)
	if _, ok := ApplyBlock(scheme, networkID, committed, honestBlk, validators); !ok {
		t.Fatal("setup: the honest next block should apply on the honest state")
	}
	// The same block applied on the digest-identical nil variant.
	panicked := func() (p any) {
		defer func() { p = recover() }()
		ApplyBlock(scheme, networkID, tamperedState, honestBlk, validators)
		return nil
	}()
	t.Logf("ApplyBlock on the digest-identical nil-amount state panicked: %v", panicked)
	if sameDigest {
		t.Errorf("digest is not injective: Amount=0 and Amount=nil states share the committed digest")
	}
	if panicked != nil {
		t.Errorf("state transition panics on a state that verifies against the committed digest: %v", panicked)
	}
}
