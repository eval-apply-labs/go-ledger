package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Critic PoCs: ApplyBlock takes Block.Height from the (unsigned) block body and never compares it with
// the height being decided, so every height-scoped guard (payment/update BlockID, evidence age) is
// keyed on a value the proposer picks.

func amountOf(accounts []Account, key string) *big.Rat { return AccountAmount(accounts, key) }

// Variant: replaying an old payment whose amount now exceeds the payer's balance trips the overdraw
// ban: the victim's whole remaining balance is destroyed and the account (and its name) removed.
func TestPoCCriticReplayTripsOverdrawBan(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	alice, _ := signing.Generate(scheme, network)
	A := alice.PublicKeyBase64()
	cost := big.NewRat(1, 10)
	genesis := st([]Account{{PublicKey: A, Name: "alice", Amount: big.NewRat(100, 1), ValidatorNode: A}}, cost)

	pay, _ := SignPayment(alice, Payment{PublicKeyFrom: A, PublicKeyTo: "mallory", Amount: big.NewRat(60, 1), BlockID: 1})
	b1 := ProposeBlock(scheme, network, 1, genesis, BlockContent{Payments: []Payment{pay}})
	s1, ok := ApplyBlock(scheme, network, genesis, b1)
	if !ok || amountOf(s1.Accounts, A).Cmp(big.NewRat(40, 1)) != 0 {
		t.Fatal("premise: honest block 1 applies, alice holds 40")
	}
	// Many heights later (say 500) a Byzantine proposer re-includes the height-1 payment.
	replay := ProposeBlock(scheme, network, 1, s1, BlockContent{Payments: []Payment{pay}})
	s2, ok := ApplyBlock(scheme, network, s1, replay)
	if !ok {
		t.Fatal("ApplyBlock rejected the replay (no bug)")
	}
	if has(s2.Accounts, A) {
		t.Fatalf("alice still present: %s", amountOf(s2.Accounts, A).RatString())
	}
	t.Errorf("REPLAY+BAN: re-including alice's already-applied 60-unit payment (BlockID 1) on a later state banned her: her remaining 40 was destroyed and her account+name removed")
}

// Variant: a future-dated payment pulled forward into an earlier block applies there AND again in the
// block it actually names (the honest proposer still holds it in its mempool).
func TestPoCCriticFuturePaymentAppliesTwice(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	alice, _ := signing.Generate(scheme, network)
	A := alice.PublicKeyBase64()
	cost := big.NewRat(1, 10)
	s0 := st([]Account{{PublicKey: A, Amount: big.NewRat(100, 1), ValidatorNode: A}}, cost)
	pay, _ := SignPayment(alice, Payment{PublicKeyFrom: A, PublicKeyTo: "bob", Amount: big.NewRat(10, 1), BlockID: 9})

	early := ProposeBlock(scheme, network, 9, s0, BlockContent{Payments: []Payment{pay}}) // decided at height 5
	s1, ok := ApplyBlock(scheme, network, s0, early)
	if !ok {
		t.Fatal("early block rejected")
	}
	honest := ProposeBlock(scheme, network, 9, s1, BlockContent{Payments: []Payment{pay}}) // the real height 9
	s2, ok := ApplyBlock(scheme, network, s1, honest)
	if !ok || len(honest.Payments) != 1 {
		t.Fatal("honest block 9 did not re-include")
	}
	if amountOf(s2.Accounts, A).Cmp(big.NewRat(80, 1)) == 0 {
		t.Errorf("DOUBLE APPLY: a payment signed once for block 9 debited alice twice (100 -> %s)", amountOf(s2.Accounts, A).RatString())
	}
}

// Variant: the evidence-age window (findings #5 fix) is bypassed by labelling the block with a height
// inside the window: 5000 blocks after the equivocation, an account recreated under the offender's key
// is confiscated again.
func TestPoCCriticStaleEvidenceWindowBypass(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	cheat, _ := signing.Generate(scheme, network)
	B := cheat.PublicKeyBase64()
	cost := big.NewRat(1, 10)
	var dX, dY [32]byte
	dX[0], dY[0] = 0x11, 0x22
	vX, _ := consensus.SignVote(cheat, consensus.Vote{Height: 3, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: B})
	vY, _ := consensus.SignVote(cheat, consensus.Vote{Height: 3, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: B})
	ev := consensus.Evidence{Validator: B, A: vX, B: vY}
	// State at (real) height ~5000: the key B was slashed long ago and has since been paid 250 again.
	recreated := st([]Account{{PublicKey: B, Amount: big.NewRat(250, 1), ValidatorNode: B}}, cost)
	if len(ProposeBlock(scheme, network, 5000, recreated, BlockContent{Evidence: []consensus.Evidence{ev}}).Evidence) != 0 {
		t.Fatal("premise: an honestly-labelled block at 5000 drops the stale evidence")
	}
	blk := ProposeBlock(scheme, network, 3, recreated, BlockContent{Evidence: []consensus.Evidence{ev}}) // labelled height 3
	next, ok := ApplyBlock(scheme, network, recreated, blk)
	if ok && !has(next.Accounts, B) {
		t.Errorf("WINDOW BYPASS: 4997-block-old evidence re-included by labelling the block height 3; the recreated account's 250 was confiscated")
	}
}

// Variant: replay of an old signed AccountUpdate (here a re-delegation the owner later reversed).
func TestPoCCriticOldAccountUpdateReplay(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	alice, _ := signing.Generate(scheme, network)
	A := alice.PublicKeyBase64()
	cost := big.NewRat(1, 10)
	s0 := st([]Account{{PublicKey: A, Amount: big.NewRat(100, 1), ValidatorNode: "goodValidator"}}, cost)
	old, _ := SignAccountUpdate(alice, AccountUpdate{From: A, Delegated: "evilValidator", BlockID: 2})
	undo, _ := SignAccountUpdate(alice, AccountUpdate{From: A, Delegated: "goodValidator", BlockID: 7})
	s1, _ := ApplyBlock(scheme, network, s0, ProposeBlock(scheme, network, 2, s0, BlockContent{Updates: []AccountUpdate{old}}))
	s2, _ := ApplyBlock(scheme, network, s1, ProposeBlock(scheme, network, 7, s1, BlockContent{Updates: []AccountUpdate{undo}}))
	if s2.Accounts[0].ValidatorNode != "goodValidator" {
		t.Fatal("premise")
	}
	s3, ok := ApplyBlock(scheme, network, s2, ProposeBlock(scheme, network, 2, s2, BlockContent{Updates: []AccountUpdate{old}}))
	if ok && s3.Accounts[0].ValidatorNode == "evilValidator" {
		t.Errorf("UPDATE REPLAY: alice's reversed delegation to evilValidator was re-applied by a block labelled height 2")
	}
}
