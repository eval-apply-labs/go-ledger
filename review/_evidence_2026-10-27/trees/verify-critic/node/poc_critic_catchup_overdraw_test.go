package node

import (
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type captureTransport struct {
	mu   sync.Mutex
	msgs []Message
}

func (c *captureTransport) Broadcast(from string, msg Message) {
	c.mu.Lock()
	c.msgs = append(c.msgs, msg)
	c.mu.Unlock()
}

// PoC (critic, round 2): commit() re-establishes the intake invariant after every block
// (mem.dropOverdrawn), because "a pending overdraw left in the pool would make this node propose a block
// that bans — and destroys — the payer". catchUp()/fastForward() changes balances the same way but
// never re-runs it. An honest node that catches up and is then proposer proposes an overdrawing set:
// the honest payer is banned and its remaining balance destroyed by an all-honest network.
func TestPoCCriticCatchUpSkipsDropOverdrawn(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 4)
	vals := make([]consensus.Validator, 4)
	for i := range signers {
		signers[i], _ = signing.Generate(scheme, network)
		vals[i] = consensus.Validator{PublicKey: signers[i].PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	// The node under test is whoever is the round-0 proposer at height 3.
	self := -1
	for i, s := range signers {
		if vs.ProposerAt(3, 0) == s.PublicKeyBase64() {
			self = i
		}
	}
	payer, _ := signing.Generate(scheme, network)
	P := payer.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: P, Name: "payer", Amount: rat("100"), ValidatorNode: P}}
	cfg := Config{NetworkID: network, Scheme: scheme, Signer: signers[self], Validators: vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour, Genesis: genesis}
	tr := &captureTransport{}
	nd := New(cfg, tr)
	nd.started = true // hand-driven (not Start()ed) so the sequence is deterministic
	nd.startHeight()

	// Payment X1 (60, for block 3) admitted by this node while the payer holds 100.
	x1, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: "shop", Amount: rat("60"), BlockID: 3})
	if err := nd.admitPayment(x1); err != nil {
		t.Fatalf("premise: X1 admitted, got %v", err)
	}

	// Meanwhile the rest of the network commits heights 1 and 2; height 2 carries the payer's OTHER
	// payment X2 (60) that it handed to a different reference node. Payer now holds 40.
	params := nd.parameters
	s0 := ledger.LedgerState{Accounts: genesis, Parameters: params}
	s1, _ := ledger.ApplyBlock(scheme, network, s0, ledger.ProposeBlock(scheme, network, 1, s0, ledger.BlockContent{}, vs), vs)
	x2, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: "cafe", Amount: rat("60"), BlockID: 2})
	s2, ok := ledger.ApplyBlock(scheme, network, s1, ledger.ProposeBlock(scheme, network, 2, s1, ledger.BlockContent{Payments: []ledger.Payment{x2}}, vs), vs)
	if !ok || ledger.AccountAmount(s2.Accounts, P).Cmp(rat("40")) != 0 {
		t.Fatal("premise: payer holds 40 after height 2")
	}
	d2 := s2.Digest()
	var votes []consensus.Vote
	for i, s := range signers {
		if i == self {
			continue
		}
		v, _ := consensus.SignVote(s, consensus.Vote{Height: 2, Round: 0, Type: consensus.Precommit, BlockDigest: d2, Validator: s.PublicKeyBase64()})
		votes = append(votes, v)
	}
	cp := Checkpoint{Accounts: s2.Accounts, Parameters: s2.Parameters, GovernanceSequence: s2.GovernanceSequence,
		Commit: consensus.Commit{Height: 2, Round: 0, BlockDigest: d2, Votes: votes}}

	nd.catchUp(cp) // the ordinary self-recovery path (catchUpLoop → evCatchUp)
	if nd.height != 3 {
		t.Fatalf("premise: caught up to height 3, at %d", nd.height)
	}
	t.Logf("after catch-up: payer balance 40, X1 (60) still pending in this node's mempool: %v", nd.mem.contains(x1))

	var proposal *ProposalMsg
	tr.mu.Lock()
	for _, m := range tr.msgs {
		if m.Type == MsgProposal && m.Proposal.Height == 3 {
			proposal = m.Proposal
		}
	}
	tr.mu.Unlock()
	if proposal == nil {
		t.Fatal("node did not propose at height 3")
	}
	next, valid := ledger.ApplyBlock(scheme, network, s2, proposal.Block, vs)
	t.Logf("height-3 proposal carries %d payment(s); valid for every honest peer: %v; payer after: %s (present=%v)",
		len(proposal.Block.Payments), valid, ledger.AccountAmount(next.Accounts, P).RatString(), func() bool {
			for _, a := range next.Accounts {
				if a.PublicKey == P {
					return true
				}
			}
			return false
		}())
	if valid && len(proposal.Block.Payments) == 1 && ledger.AccountAmount(next.Accounts, P).Sign() == 0 {
		t.Errorf("BAN BY HONEST NODE: after catch-up the node proposed the now-overdrawing X1; the payer is banned and its remaining 40 destroyed (commit() would have dropped X1)")
	}
}
