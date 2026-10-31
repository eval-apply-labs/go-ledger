package node

// Verifier PoC (round 2) for C11 on LIVE dynamic-mode nodes: three validator nodes over the bus,
// votes and the re-delegation enter through the node intakes (SubmitParameterVote / SubmitUpdate),
// blocks are built by the honest proposers and committed by consensus.

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestVerifyC11DepartedVoterPoisonsLiveNetwork(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	mk := func() *signing.Signer { s, _ := signing.Generate(scheme, network); return s }
	signers := []*signing.Signer{mk(), mk(), mk()}
	k1, k2, k3 := signers[0].PublicKeyBase64(), signers[1].PublicKeyBase64(), signers[2].PublicKeyBase64()
	genesis := []ledger.Account{
		{PublicKey: k1, Amount: rat("60"), ValidatorNode: k1},
		{PublicKey: k2, Amount: rat("30"), ValidatorNode: k2},
		{PublicKey: k3, Amount: rat("15"), ValidatorNode: k3},
	}
	net := newBus()
	nodes := make([]*Node, 3)
	for i := range nodes {
		nodes[i] = New(Config{NetworkID: network, Scheme: scheme, Signer: signers[i], ValidatorThreshold: rat("10"),
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 100 * time.Millisecond,
			Genesis: genesis}, net)
		net.register(signers[i].PublicKeyBase64(), nodes[i])
	}
	for _, nd := range nodes {
		nd.Start()
		defer nd.Stop()
	}
	waitFor(t, 5*time.Second, func() bool { return nodes[0].Snapshot().Height >= 3 }, "network did not start")

	s := nodes[0].Snapshot()
	cur := s.Parameters
	proposed := cur
	proposed.TransactionCost = rat("0.5")
	vote := func(sg *signing.Signer, p ledger.LedgerParameters) ledger.ParameterChange {
		pv, _ := ledger.SignParameterVote(sg, p, cur.Digest(), s.GovernanceSequence)
		return ledger.ParameterChange{NewParameters: p, PreviousParametersDigest: cur.Digest(), Sequence: s.GovernanceSequence, Votes: []ledger.ParameterVote{pv}}
	}
	submitAll := func(c ledger.ParameterChange) {
		for _, nd := range nodes {
			nd.SubmitParameterVote(c)
		}
	}
	// v3 (15 of 105) votes first, then re-delegates its stake to v1 (leaving the set).
	submitAll(vote(signers[2], proposed))
	h := nodes[0].Snapshot().Height
	upd, _ := ledger.SignAccountUpdate(signers[2], ledger.AccountUpdate{From: k3, Delegated: k1, BlockID: h + 2})
	for _, nd := range nodes {
		nd.SubmitUpdate(upd)
	}
	waitFor(t, 5*time.Second, func() bool {
		return !deriveValidators(nodes[0].Snapshot().Accounts, rat("10")).Contains(k3)
	}, "v3 did not leave the set")
	// The two remaining validators (105 of 105) vote the same proposal.
	submitAll(vote(signers[0], proposed))
	submitAll(vote(signers[1], proposed))
	start := nodes[0].Snapshot().Height
	waitFor(t, 10*time.Second, func() bool { return nodes[0].Snapshot().Height >= start+8 }, "chain stalled")
	after := nodes[0].Snapshot()
	view := nodes[0].GovernanceView()
	var pv ParameterProposalView
	for _, p := range view.ParameterProposals {
		if p.NewTransactionCost == "1/2" {
			pv = p
		}
	}
	t.Logf("8 blocks after v1+v2 voted: cost=%s ; view votedPower=%d/%d reached=%v amendsCurrent=%v voters=%d",
		after.Parameters.TransactionCost.RatString(), pv.VotedPower, view.Quorum, pv.Reached, pv.AmendsCurrent, len(pv.Voters))
	if after.Parameters.TransactionCost.Cmp(rat("0.5")) != 0 {
		t.Errorf("POISONED on live nodes: ratified-by-current-validators proposal never enacts")
	}
	// Workaround probe: an otherwise-identical profile differing in one field pools under a NEW key.
	alt := proposed
	alt.MaxMessageBytes = cur.MaxMessageBytes + 1
	if alt.MaxMessageBytes < ledger.MinMessageBytes {
		alt.MaxMessageBytes = ledger.MinMessageBytes
	}
	submitAll(vote(signers[0], alt))
	submitAll(vote(signers[1], alt))
	waitFor(t, 10*time.Second, func() bool { return nodes[0].Snapshot().Parameters.TransactionCost.Cmp(rat("0.5")) == 0 }, "workaround did not enact either")
	t.Logf("workaround: a tweaked profile voted by v1+v2 enacted (cost now %s)", nodes[0].Snapshot().Parameters.TransactionCost.RatString())
}
