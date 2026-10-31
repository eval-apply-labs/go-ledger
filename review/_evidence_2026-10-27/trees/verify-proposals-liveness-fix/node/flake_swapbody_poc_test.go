package node

// PoC (scratch copy only): the proposal signature covers (height, round, validRound, StateDigest) but
// not the block BODY, and onProposal records/relays the FIRST authentic-signature copy of a round even
// when its body fails ApplyBlock. So anyone who has seen a genuine proposal (any gossip peer / relay /
// validator) can, with NO key, re-send it with a swapped body under the genuine signature. If that
// copy reaches a node first (a relay hop, a smaller message, or — deterministically — a node that
// dropped the genuine copy in its post-commit window, see flake_poc_test.go), the node:
//   - records the round's proposal as INVALID and prevotes nil against an honest, valid block;
//   - stores the forged body under the digest (n.blocks), relays the forged copy onward, and
//     re-gossips it every cfg.Timeout (reGossipProposal);
//   - dedups every later genuine copy (held fast path keys on the digest only), so it never prevotes
//     the real block and cannot commit the height even after the rest of the network precommits it.
//
// Run: go test -count=1 -run 'TestPoCSwappedBodyFirstSightPoisonsRound' -v ./node/

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestPoCSwappedBodyFirstSightPoisonsRound(t *testing.T) {
	p := newPocNet(t, 4)
	self := pickNonProposer(p, 1)
	selfKey := p.signers[self].PublicKeyBase64()
	nd, tr := p.node(self, 60*time.Second, 50*time.Millisecond) // long timeout: nothing fires on its own
	nd.Start()
	defer nd.Stop()
	time.Sleep(50 * time.Millisecond) // height 1 started

	genuine := p.proposal(nd.Snapshot(), 1, 0)
	if len(genuine.Block.Payments) != 0 || genuine.Block.PrevStateDigest == ([32]byte{}) {
		t.Fatal("premise: genuine block is a well-formed (empty) block on genesis")
	}
	// The forgery: genuine Height/Round/ValidRound/Proposer/Signature/StateDigest, swapped body.
	forged := genuine
	forged.Block = ledger.Block{Height: 1, StateDigest: genuine.Block.StateDigest} // wrong PrevStateDigest → invalid
	if !VerifyProposal(signing.Ed25519{}, "testnet", forged) {
		t.Fatal("premise: the forged copy carries a valid proposer signature (body not covered)")
	}

	nd.SubmitProposal(forged)  // arrives first
	nd.SubmitProposal(genuine) // the real one, a moment later
	for i := 0; i < 3; i++ {
		nd.SubmitProposal(genuine) // relays / re-gossip of the real one
	}
	_ = nd.Snapshot()

	got := tr.votes(selfKey, 1, 0, consensus.Prevote)
	var nilDigest [32]byte
	if len(got) != 1 {
		t.Fatalf("expected exactly one own prevote, got %+v", got)
	}
	relayedForged := false
	tr.mu.Lock()
	for _, m := range tr.msgs {
		if m.Type == MsgProposal && m.Proposal != nil && m.Proposal.Block.PrevStateDigest == ([32]byte{}) {
			relayedForged = true
		}
	}
	tr.mu.Unlock()

	// The other three validators prevote and precommit the genuine block → it is decided network-wide.
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, 1, 0, consensus.Prevote, genuine.Block.StateDigest))
		}
	}
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, 1, 0, consensus.Precommit, genuine.Block.StateDigest))
		}
	}
	time.Sleep(300 * time.Millisecond)
	h := nd.Snapshot().Height

	if got[0].BlockDigest == genuine.Block.StateDigest && h == 2 {
		t.Fatal("not reproduced: the node prevoted and committed the genuine block")
	}
	t.Errorf("BUG: one keyless forged copy (genuine signature, swapped body) delivered first made the honest node "+
		"prevote nil=%v against the valid proposal, relay the forgery onward=%v, ignore 4 genuine copies, and stay "+
		"at height %d although 3/4 of the validators precommitted the genuine block (only catch-up can move it on)",
		got[0].BlockDigest == nilDigest, relayedForged, h)
}
