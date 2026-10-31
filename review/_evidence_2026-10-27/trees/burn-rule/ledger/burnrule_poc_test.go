package ledger

// PoC tests for the "two different signings means the key is burned" review (label: burn-rule).
// Each test ASSERTS the behaviour the design/docs promise and FAILS on the current code, printing
// the observed outcome. They live only in the reviewer's scratch copy.

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func findAccount(accounts []Account, key string) (Account, bool) {
	for _, a := range accounts {
		if a.PublicKey == key {
			return a, true
		}
	}
	return Account{}, false
}

// BURN-1: the overdraw ban is documented (findings.md design decision, docs/security.md,
// TestOverdrawBanDestroysPayerBalance) as "the account is dropped from the result entirely, so its
// existing balance vanishes". ProcessPayments only skips the banned payer in the pass-through loop;
// the `affected` loop re-creates any key that received a credit from its ORIGINAL balance. So a
// banned payer that is also paid (any amount above the fee) in the same block keeps its whole balance.
func TestPoCOverdrawBanEscapedByIncomingCredit(t *testing.T) {
	accounts := []Account{
		{PublicKey: "A", Name: "alice", Amount: rat("10"), ValidatorNode: "A"},
		{PublicKey: "B", Amount: rat("5"), ValidatorNode: "B"},
		{PublicKey: "C", Amount: rat("5"), ValidatorNode: "C"},
	}
	after := ProcessPayments(accounts, []Payment{
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6")},                    // two same-block
		{PublicKeyFrom: "A", PublicKeyTo: "B", Amount: rat("6"), Comment: "second"}, // payments: 12 > 10
		{PublicKeyFrom: "C", PublicKeyTo: "A", Amount: rat("0.2")},                  // any credit to A
	}, rat("0.1"))
	a, present := findAccount(after, "A")
	if present {
		t.Fatalf("overdraw ban escaped: banned payer A is still present with balance %s and name %q "+
			"(its original 10 plus the 0.1 credit) — the ban should have dropped it entirely",
			a.Amount.FloatString(2), a.Name)
	}
}

// BURN-2a: spending to exactly zero deletes the account (ProcessPayments drops non-positive balances)
// and with it the registered Name. Because account updates apply AFTER payments in the same block,
// another account's update naming this very block claims the freed name immediately — so every later
// pay-by-name payment to "alice" credits the new owner. No signature by alice's key is involved in
// the transfer of the name.
func TestPoCSpendToZeroFreesNameInSameBlock(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	alice, _ := signing.Generate(scheme, network)
	other, _ := signing.Generate(scheme, network)
	A, O := alice.PublicKeyBase64(), other.PublicKeyBase64()
	prev := []Account{
		{PublicKey: A, Name: "alice", Amount: rat("10"), ValidatorNode: A},
		{PublicKey: O, Amount: rat("1"), ValidatorNode: O},
	}
	const h = 7
	// alice pays out her whole balance (e.g. "move everything to my new key").
	pay, _ := SignPayment(alice, Payment{PublicKeyFrom: A, PublicKeyTo: O, Amount: rat("10"), BlockID: h})
	// A standing claim by another account, signed for this block (a no-op while the name is taken).
	claim, _ := SignAccountUpdate(other, AccountUpdate{From: O, Name: "alice", BlockID: h})

	blk := ProposeBlock(scheme, network, h, st(prev, rat("0.1")), BlockContent{Payments: []Payment{pay}, Updates: []AccountUpdate{claim}})
	next, ok := ApplyBlock(scheme, network, st(prev, rat("0.1")), blk)
	if !ok {
		t.Fatal("block should apply")
	}
	if owner := nameRegistry(next.Accounts)["alice"]; owner != A {
		t.Fatalf("spending to zero released the registered name: \"alice\" now resolves to %s (another account), "+
			"not to alice's key; later pay-by-name payments to \"alice\" credit that account", owner[:8])
	}
}

// BURN-2b: a slash (applySlashes) deletes the offender's account, which frees its name for anyone in
// the NEXT block. The burn rule therefore hands the burned identity's name to the first claimant
// instead of tombstoning it.
func TestPoCSlashFreesValidatorNameForAnyone(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	val, _ := signing.Generate(scheme, network)
	other, _ := signing.Generate(scheme, network)
	V, O := val.PublicKeyBase64(), other.PublicKeyBase64()
	prev := []Account{
		{PublicKey: V, Name: "bakery", Amount: rat("100"), ValidatorNode: V},
		{PublicKey: O, Amount: rat("1"), ValidatorNode: O},
	}
	var dX, dY [32]byte
	dX[0], dY[0] = 1, 2
	vX, _ := consensus.SignVote(val, consensus.Vote{Height: 4, Round: 0, Type: consensus.Prevote, BlockDigest: dX, Validator: V})
	vY, _ := consensus.SignVote(val, consensus.Vote{Height: 4, Round: 0, Type: consensus.Prevote, BlockDigest: dY, Validator: V})
	ev := consensus.Evidence{Validator: V, A: vX, B: vY}

	s0 := st(prev, rat("0.1"))
	b5 := ProposeBlock(scheme, network, 5, s0, BlockContent{Evidence: []consensus.Evidence{ev}})
	s1, ok := ApplyBlock(scheme, network, s0, b5)
	if !ok {
		t.Fatal("slash block should apply")
	}
	claim, _ := SignAccountUpdate(other, AccountUpdate{From: O, Name: "bakery", BlockID: 6})
	b6 := ProposeBlock(scheme, network, 6, s1, BlockContent{Updates: []AccountUpdate{claim}})
	s2, ok := ApplyBlock(scheme, network, s1, b6)
	if !ok {
		t.Fatal("claim block should apply")
	}
	if owner, taken := nameRegistry(s2.Accounts)["bakery"]; taken && owner != V {
		t.Fatalf("the burned validator's name \"bakery\" was re-registered by another account (%s...) one block "+
			"after the slash; pay-by-name traffic for \"bakery\" now credits that account", owner[:8])
	}
}

// BURN-3: after a slash, the burned key is not excluded at the ledger layer: a payment to it simply
// re-creates the account (ProcessPayments synthesises any paid-to key), self-delegated, and once the
// evidence is no longer carried the key is an ordinary funded account again — and, in dynamic mode,
// a validator again as soon as its stake reaches the threshold. There is no tombstone in state.
func TestPoCSlashedKeyIsRecreatedByAnyPayment(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	val, _ := signing.Generate(scheme, network)
	payer, _ := signing.Generate(scheme, network)
	V, P := val.PublicKeyBase64(), payer.PublicKeyBase64()
	prev := []Account{
		{PublicKey: V, Amount: rat("100"), ValidatorNode: V},
		{PublicKey: P, Amount: rat("500"), ValidatorNode: P},
	}
	var dX, dY [32]byte
	dX[0], dY[0] = 1, 2
	vX, _ := consensus.SignVote(val, consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: V})
	vY, _ := consensus.SignVote(val, consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: V})
	ev := consensus.Evidence{Validator: V, A: vX, B: vY}
	s0 := st(prev, rat("0.1"))
	s1, ok := ApplyBlock(scheme, network, s0, ProposeBlock(scheme, network, 2, s0, BlockContent{Evidence: []consensus.Evidence{ev}}))
	if !ok {
		t.Fatal("slash block should apply")
	}
	if _, present := findAccount(s1.Accounts, V); present {
		t.Fatal("precondition: V should be slashed out")
	}
	// Any later payment to V (evidence not re-carried in this block) restores V as a funded account.
	pay, _ := SignPayment(payer, Payment{PublicKeyFrom: P, PublicKeyTo: V, Amount: rat("200"), BlockID: 3})
	s2, ok := ApplyBlock(scheme, network, s1, ProposeBlock(scheme, network, 3, s1, BlockContent{Payments: []Payment{pay}}))
	if !ok {
		t.Fatal("payment block should apply")
	}
	if a, present := findAccount(s2.Accounts, V); present {
		t.Fatalf("burned key %s... is back: re-created by a payment with balance %s, ValidatorNode=self "+
			"(no tombstone — the ledger does not remember the key was burned)", V[:8], a.Amount.FloatString(1))
	}
}

// BURN-6: two DIFFERENT account updates signed by one key for the SAME block (the natural uniqueness
// slot for account-update) are both applied in order; nothing records or penalises the pair.
func TestPoCConflictingAccountUpdatesBothApply(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	owner, _ := signing.Generate(scheme, network)
	v1, _ := signing.Generate(scheme, network)
	v2, _ := signing.Generate(scheme, network)
	K := owner.PublicKeyBase64()
	prev := []Account{{PublicKey: K, Name: "carol", Amount: rat("50"), ValidatorNode: K}}
	u1, _ := SignAccountUpdate(owner, AccountUpdate{From: K, Delegated: v1.PublicKeyBase64(), BlockID: 9})
	u2, _ := SignAccountUpdate(owner, AccountUpdate{From: K, Delegated: v2.PublicKeyBase64(), Name: "carol2", BlockID: 9})
	s0 := st(prev, rat("0.1"))
	blk := ProposeBlock(scheme, network, 9, s0, BlockContent{Updates: []AccountUpdate{u1, u2}})
	if len(blk.Updates) != 2 {
		t.Fatalf("expected both updates carried, got %d", len(blk.Updates))
	}
	next, ok := ApplyBlock(scheme, network, s0, blk)
	if !ok {
		t.Fatal("block should apply")
	}
	a, _ := findAccount(next.Accounts, K)
	if a.PublicKey != "" {
		t.Fatalf("two conflicting same-block updates from one key were both applied (last wins: delegated=%s..., name=%q); "+
			"no conflict is detected and the key is not burned", a.ValidatorNode[:8], a.Name)
	}
}

// helper so big is used even if a test above is edited away
var _ = big.NewRat
