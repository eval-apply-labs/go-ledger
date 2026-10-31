package node

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Review of the C21 prototype fix: what the post-commit window admits into heightProposals.
func TestVPLFixWindowAdmission(t *testing.T) {
	p := newPocNet(t, 4)
	self := pickNonProposer(p, 1, 2)
	nd, tr := vplNode(p, self)
	// Commit height 1 synchronously through handle().
	p1 := p.proposal(Snapshot{Accounts: nd.accounts, Parameters: nd.parameters}, 1, 0)
	nd.handle(event{kind: evProposal, proposal: &p1})
	for i := range p.signers {
		if i != self {
			v := p.vote(i, 1, 0, consensus.Prevote, p1.Block.StateDigest)
			nd.handle(event{kind: evVote, vote: &v})
		}
	}
	for i := range p.signers {
		if i != self {
			v := p.vote(i, 1, 0, consensus.Precommit, p1.Block.StateDigest)
			nd.handle(event{kind: evVote, vote: &v})
		}
	}
	if nd.height != 2 || nd.driver != nil {
		t.Fatalf("expected to be in the post-commit window of height 2 (height=%d driverNil=%v)", nd.height, nd.driver == nil)
	}
	snap := Snapshot{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	good := p.proposal(snap, 2, 0)

	wrongProposer := good
	other := (p.index(good.Proposer) + 1) % 4
	wrongProposer = p.sign(other, ProposalMsg{Height: 2, Round: 0, ValidRound: -1, Block: good.Block})
	badSig := good
	badSig.Signature = p1.Signature
	stale := p1 // height 1
	future := p.proposal(snap, 3, 0)
	cases := []struct {
		name string
		pm   ProposalMsg
	}{{"wrong proposer", wrongProposer}, {"bad signature", badSig}, {"stale height", stale}, {"future height", future}}
	for _, c := range cases {
		pm := c.pm
		nd.handle(event{kind: evProposal, proposal: &pm})
		if len(nd.heightProposals) != 0 {
			t.Fatalf("window admitted %s", c.name)
		}
	}
	// An authentic but INVALID block (wrong prev digest) is buffered and relayed (same policy as the
	// main path) and judged at replay: the node must prevote nil, not the bogus digest.
	var bogus [32]byte
	bogus[0] = 9
	invalid := p.sign(p.index(good.Proposer), ProposalMsg{Height: 2, Round: 0, ValidRound: -1, Block: ledger.Block{Height: 2, StateDigest: bogus}})
	nd.handle(event{kind: evProposal, proposal: &invalid})
	nd.handle(event{kind: evProposal, proposal: &good}) // same round: first wins in the window
	if len(nd.heightProposals) != 1 {
		t.Fatalf("expected exactly one buffered proposal, got %d", len(nd.heightProposals))
	}
	nd.handle(event{kind: evNextHeight})
	pv := tr.votes(p.signers[self].PublicKeyBase64(), 2, 0, consensus.Prevote)
	var nilD [32]byte
	if len(pv) != 1 || pv[0].BlockDigest != nilD {
		t.Fatalf("expected a nil prevote for the buffered invalid block, got %+v", pv)
	}
	_ = signing.Ed25519{}
	t.Logf("window rejects wrong proposer / bad signature / stale / future height; buffers first authentic copy only; validity judged at replay (nil prevote for an invalid block)")
}
