package node

import (
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// C28 via the live event loop: a validator votes normally, then — once the height has decided
// (it sees the next height's proposal) — broadcasts a conflicting precommit for the decided
// (height, round). Every honest node receives both signed precommits, yet the conflict lands either
// in a decided driver that is discarded un-harvested, or in a driver for the next height that
// drops it (height mismatch). The double-signer is never slashed.
func TestVBSC28LateDoubleSignNeverSlashed(t *testing.T) {
	honest, keys := gen(t, 3)
	bzS, bzK := gen(t, 1)
	bus := newVBus()
	var vals []consensus.Validator
	var genesis []ledger.Account
	for _, k := range append(append([]string{}, keys...), bzK[0]) {
		vals = append(vals, consensus.Validator{PublicKey: k, Power: 1})
		genesis = append(genesis, ledger.Account{PublicKey: k, Amount: rat("100"), ValidatorNode: k})
	}
	vs := consensus.NewValidatorSet(vals)
	var mu sync.Mutex
	mine := map[int64]consensus.Vote{} // the byz key's real precommit per height
	late := 0
	bus.setHandler(bzK[0], func(m Message) {
		if m.Type != MsgProposal || m.Proposal == nil {
			return
		}
		h, r, d := m.Proposal.Height, m.Proposal.Round, m.Proposal.Block.StateDigest
		mu.Lock()
		defer mu.Unlock()
		if _, ok := mine[h]; !ok {
			pv, _ := consensus.SignVote(bzS[0], consensus.Vote{Height: h, Round: r, Type: consensus.Prevote, BlockDigest: d, Validator: bzK[0]})
			pc, _ := consensus.SignVote(bzS[0], consensus.Vote{Height: h, Round: r, Type: consensus.Precommit, BlockDigest: d, Validator: bzK[0]})
			mine[h] = pc
			bus.Broadcast(bzK[0], Message{Type: MsgVote, From: bzK[0], Vote: &pv})
			bus.Broadcast(bzK[0], Message{Type: MsgVote, From: bzK[0], Vote: &pc})
			if prev, ok := mine[h-1]; ok { // height h-1 decided: double-sign it now
				conflict := prev
				conflict.BlockDigest[0] ^= 0xff
				signed, _ := consensus.SignVote(bzS[0], conflict)
				late++
				bus.Broadcast(bzK[0], Message{Type: MsgVote, From: bzK[0], Vote: &signed})
			}
		}
	})
	var nodes []*Node
	for i, s := range honest {
		nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: s, Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond, Genesis: genesis}, bus)
		nodes = append(nodes, nd)
		bus.setNode(keys[i], nd)
	}
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		for _, nd := range nodes {
			nd.Stop()
		}
	}()
	waitFor(t, 20*time.Second, func() bool { return nodes[0].Snapshot().Height >= 40 }, "no progress")
	mu.Lock()
	n := late
	mu.Unlock()
	ev, ok := conflictingPair(bus.votesBy(bzK[0], 10), 0, consensus.Precommit)
	t.Logf("late conflicting precommits broadcast: %d; e.g. height 10 proof valid=%v; byz balance on honest nodes: %s %s %s",
		n, ok && ev.Valid(signing.Ed25519{}, "testnet"), balance(nodes[0].Snapshot().Accounts, bzK[0]).RatString(),
		balance(nodes[1].Snapshot().Accounts, bzK[0]).RatString(), balance(nodes[2].Snapshot().Accounts, bzK[0]).RatString())
	if balance(nodes[0].Snapshot().Accounts, bzK[0]).Sign() != 0 {
		t.Errorf("C28: ~%d valid double-signs delivered to every honest node after their heights decided; none was ever harvested or slashed", n)
	}
}
