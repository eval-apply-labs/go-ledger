package node

// Verifier PoC (round 2) for C12: drive the same-block outflow through PRODUCTION paths only —
// four static validators over the in-memory bus, the reassignment certificate entering via
// SubmitReassignmentVote (the /hdls/reassignment-vote intake), the target's payment entering via
// AdmitPayment (the /hdls/pay intake, with the overdraw guard), and the honest proposer's own
// startHeight/ProposeBlock + every validator's ApplyBlock.

import (
	"math/big"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func supplyOfAccounts(accounts []ledger.Account) *big.Rat {
	sum := new(big.Rat)
	for _, a := range accounts {
		if a.Amount != nil {
			sum.Add(sum, a.Amount)
		}
	}
	return sum
}

func runSameBlockOutflow(t *testing.T, newAmount string) {
	const n = 4
	scheme, network := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		signers[i], _ = signing.Generate(scheme, network)
		vals[i] = consensus.Validator{PublicKey: signers[i].PublicKeyBase64(), Power: 1}
	}
	set := consensus.NewValidatorSet(vals) // quorum 3
	target, _ := signing.Generate(scheme, network)
	stash, _ := signing.Generate(scheme, network)
	T, X := target.PublicKeyBase64(), stash.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: T, Name: "thief", Amount: rat("1000"), ValidatorNode: T}}
	for _, v := range vals {
		genesis = append(genesis, ledger.Account{PublicKey: v.PublicKey, Amount: rat("100"), ValidatorNode: v.PublicKey})
	}
	net := newBus()
	nodes := make([]*Node, n)
	for i := range nodes {
		nodes[i] = New(Config{NetworkID: network, Scheme: scheme, Signer: signers[i], Validators: set,
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 400 * time.Millisecond,
			Genesis: genesis}, net)
		net.register(signers[i].PublicKeyBase64(), nodes[i])
	}
	for _, nd := range nodes {
		nd.Start()
		defer nd.Stop()
	}
	waitFor(t, 5*time.Second, func() bool { return nodes[0].Snapshot().Height >= 3 }, "network did not start")
	supplyBefore := supplyOfAccounts(nodes[0].Snapshot().Accounts)

	// Wait for a fresh commit, so we are inside the inter-block window (startHeight(H) not yet run).
	h0 := nodes[0].Snapshot().Height
	waitFor(t, 5*time.Second, func() bool { return nodes[0].Snapshot().Height > h0 }, "no commit")
	H := nodes[0].Snapshot().Height
	seq := nodes[0].Snapshot().GovernanceSequence

	// The banks assemble the certificate off-pool and submit it complete (3 of 4 = quorum).
	cert := ledger.Reassignment{Account: T, PreviousAmount: rat("1000"), NewAmount: rat(newAmount), Sequence: seq}
	for _, s := range signers[:3] {
		v, _ := ledger.SignReassignmentVote(s, T, rat("1000"), rat(newAmount), seq)
		cert.Votes = append(cert.Votes, v)
	}
	for _, nd := range nodes {
		nd.SubmitReassignmentVote(cert)
	}
	// The target watches the PUBLIC governance view; on seeing its key reach quorum it pays out
	// its whole balance, naming the block that will carry the certificate (the current height).
	waitFor(t, time.Second, func() bool {
		for _, r := range nodes[0].GovernanceView().Reassignments {
			if r.Account == T && r.Reached {
				return true
			}
		}
		return false
	}, "certificate not pooled")
	pay, _ := ledger.SignPayment(target, ledger.Payment{PublicKeyFrom: T, PublicKeyTo: X, Amount: rat("1000"), BlockID: nodes[0].Snapshot().Height})
	if err := nodes[0].AdmitPayment(pay); err != nil {
		t.Fatalf("honest intake refused the outflow: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		done := true
		for _, nd := range nodes {
			if nd.Snapshot().Height <= H+1 {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			for i, nd := range nodes {
				sn := nd.Snapshot()
				round := int32(-1)
				if nd.driver != nil {
					round = nd.driver.Round()
				}
				t.Logf("STALL node%d: height=%d heightStarted=%d round=%d seq=%d T=%s mem=%d reass=%d heightProposals=%d",
					i, sn.Height, nd.heightStarted, round, sn.GovernanceSequence, ledger.AccountAmount(sn.Accounts, T).RatString(),
					len(nd.mem.pending), len(nd.reassignmentProposals), len(nd.heightProposals))
				for r, p := range nd.heightProposals {
					t.Logf("   node%d holds proposal r=%d proposer=%s… payments=%d reass=%v digest=%x", i, r, p.Proposer[:8], len(p.Block.Payments), p.Block.Reassignment != nil, p.Block.StateDigest[:4])
				}
			}
			t.Fatalf("chain did not advance")
		}
		time.Sleep(10 * time.Millisecond)
	}
	snaps := make([]Snapshot, n)
	for i, nd := range nodes {
		snaps[i] = nd.Snapshot()
	}
	for i := 1; i < n; i++ {
		if ledger.StateDigest(snaps[i].Accounts) != ledger.StateDigest(snaps[0].Accounts) && snaps[i].Height == snaps[0].Height {
			t.Fatalf("nodes diverged")
		}
	}
	s := snaps[0]
	supplyAfter := supplyOfAccounts(s.Accounts)
	ratified := new(big.Rat).Sub(rat(newAmount), rat("1000"))
	bound := new(big.Rat).Add(supplyBefore, ratified)
	var tName string
	for _, a := range s.Accounts {
		if a.PublicKey == T {
			tName = a.Name
		}
	}
	t.Logf("H=%d seq %d→%d ; T=%s (name %q) X=%s ; supply %s→%s ; ratified delta %s ; payment BlockID=%d",
		H, seq, s.GovernanceSequence, ledger.AccountAmount(s.Accounts, T).RatString(), tName,
		ledger.AccountAmount(s.Accounts, X).RatString(), supplyBefore.FloatString(1), supplyAfter.FloatString(1),
		ratified.RatString(), pay.BlockID)
	if s.GovernanceSequence != seq+1 {
		t.Fatalf("setup: certificate did not enact")
	}
	if ledger.AccountAmount(s.Accounts, X).Sign() > 0 && supplyAfter.Cmp(bound) > 0 {
		t.Errorf("SAME-BLOCK OUTFLOW ESCAPED/MINTED: supply %s exceeds ratified bound %s by %s (X kept %s)",
			supplyAfter.FloatString(1), bound.FloatString(1), new(big.Rat).Sub(supplyAfter, bound).FloatString(1),
			ledger.AccountAmount(s.Accounts, X).FloatString(1))
	}
}

func TestVerifyC12SeizureDefeatedThroughNode(t *testing.T)     { runSameBlockOutflow(t, "0") }
func TestVerifyC12PartialSeizureMintsThroughNode(t *testing.T) { runSameBlockOutflow(t, "500") }
func TestVerifyC12TopUpMintsThroughNode(t *testing.T)          { runSameBlockOutflow(t, "5000") }
