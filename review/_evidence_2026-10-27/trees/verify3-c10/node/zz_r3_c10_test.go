package node

// R3 verification (group R3-dos) for NF-C10: does the round-1 C10 fix (pool only after >=1 verified
// current-validator vote) still let ONE current validator fill the 64-slot parameter pool with
// distinct single-vote proposals that can never reach quorum, blocking a genuine quorum-bearing one?

import (
	"fmt"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestR3C10SingleValidatorFillsParamPool(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	// 4 validators; one (byz) is Byzantine with <1/3 by holding the smallest stake. Power is derived
	// from stake here (dynamic set) so the attacker's power is genuinely a minority.
	signers := make([]*signing.Signer, 4)
	for i := range signers {
		signers[i], _ = signing.Generate(scheme, network)
	}
	byz, honest := signers[0], signers[1]
	vals := []consensus.Validator{
		{PublicKey: signers[0].PublicKeyBase64(), Power: 1},
		{PublicKey: signers[1].PublicKeyBase64(), Power: 10},
		{PublicKey: signers[2].PublicKeyBase64(), Power: 10},
		{PublicKey: signers[3].PublicKeyBase64(), Power: 10},
	}
	vs := consensus.NewValidatorSet(vals)
	nd := New(Config{NetworkID: network, Scheme: scheme, Signer: honest, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour, MinBlockInterval: time.Hour,
		Genesis: []ledger.Account{{PublicKey: honest.PublicKeyBase64(), Amount: rat("1"), ValidatorNode: honest.PublicKeyBase64()}}}, NewHTTPTransport(nil))
	nd.started = true
	nd.startHeight()

	base := nd.parameters
	prevDigest := base.Digest()

	// The Byzantine validator self-signs 64 DISTINCT acceptable parameter profiles (varying StepTimeoutMs),
	// each carrying only its own one valid vote. Each is submitted as the attacker would POST it.
	accepted := 0
	for i := 0; i < 200 && accepted < 64; i++ {
		np := base
		np.StepTimeoutMs = base.StepTimeoutMs + int64(1000+i) // still Acceptable (timing stays consistent)
		if !np.Acceptable(len(vals)) {
			continue
		}
		vote, _ := ledger.SignParameterVote(byz, np, prevDigest, nd.governanceSequence)
		ch := ledger.ParameterChange{NewParameters: np, PreviousParametersDigest: prevDigest, Sequence: nd.governanceSequence, Votes: []ledger.ParameterVote{vote}}
		if nd.addParameterVotes(ch) {
			accepted++
		}
	}
	t.Logf("single Byzantine validator (power %d of %d) got %d self-signed single-vote proposals pooled; pool size now %d/64",
		vs.Power(byz.PublicKeyBase64()), vs.TotalPower(), accepted, len(nd.paramProposals))

	// Now a genuine change that the three honest validators (power 30 of 31, > 2/3) would ratify: build
	// it with all three honest votes so it is quorum-bearing on arrival.
	gp := base
	gp.TransactionCost = rat("0.2")
	var honestVotes []ledger.ParameterVote
	for _, s := range []*signing.Signer{signers[1], signers[2], signers[3]} {
		v, _ := ledger.SignParameterVote(s, gp, prevDigest, nd.governanceSequence)
		honestVotes = append(honestVotes, v)
	}
	genuine := ledger.ParameterChange{NewParameters: gp, PreviousParametersDigest: prevDigest, Sequence: nd.governanceSequence, Votes: honestVotes}
	pooledGenuine := nd.addParameterVotes(genuine)
	pending := nd.pendingParameterChange()
	t.Logf("genuine quorum-bearing fee change pooled: %v; pendingParameterChange returns non-nil (would enact): %v", pooledGenuine, pending != nil)
	if len(nd.paramProposals) >= 64 && !pooledGenuine && pending == nil {
		t.Errorf("CONFIRMED NF-C10: one minority validator's 64 self-signed single-vote proposals fill the pool AFTER the C10 fix; a genuine 30/31-power fee change is refused at the cap and cannot enact")
	}
}

// Sibling: the reassignment pool (no Acceptable gate) under the same C10 >=1-verified-vote fix.
func TestR3C10SingleValidatorFillsReassignPool(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 4)
	for i := range signers {
		signers[i], _ = signing.Generate(scheme, network)
	}
	byz := signers[0]
	vals := []consensus.Validator{
		{PublicKey: signers[0].PublicKeyBase64(), Power: 1}, {PublicKey: signers[1].PublicKeyBase64(), Power: 10},
		{PublicKey: signers[2].PublicKeyBase64(), Power: 10}, {PublicKey: signers[3].PublicKeyBase64(), Power: 10},
	}
	vs := consensus.NewValidatorSet(vals)
	nd := New(Config{NetworkID: network, Scheme: scheme, Signer: signers[1], Validators: vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour, MinBlockInterval: time.Hour,
		Genesis: []ledger.Account{{PublicKey: signers[1].PublicKeyBase64(), Amount: rat("1"), ValidatorNode: signers[1].PublicKeyBase64()}}}, NewHTTPTransport(nil))
	nd.started = true
	nd.startHeight()
	accepted := 0
	for i := 0; i < 64; i++ {
		acct := fmt.Sprintf("ghost-account-%d", i)
		prev, next := rat("0"), rat(fmt.Sprintf("%d", 1+i))
		vote, _ := ledger.SignReassignmentVote(byz, acct, prev, next, nd.governanceSequence)
		r := ledger.Reassignment{Account: acct, PreviousAmount: prev, NewAmount: next, Sequence: nd.governanceSequence, Votes: []ledger.ReassignmentVote{vote}}
		if nd.addReassignmentVotes(r) {
			accepted++
		}
	}
	t.Logf("single minority validator pooled %d self-signed single-vote reassignments; pool now %d/64", accepted, len(nd.reassignmentProposals))
	if accepted < 64 {
		t.Errorf("expected the reassignment pool to also fill")
	}
}
