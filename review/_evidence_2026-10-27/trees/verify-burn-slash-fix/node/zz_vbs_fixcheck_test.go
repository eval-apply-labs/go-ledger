package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// Against the burn-set prototype: all three modes must exclude the key AND keep committing.
func TestVBSFixBurnAllModes(t *testing.T) {
	check := func(name string, c *c01net, slashedAt int64, extra func()) {
		c.byz.mode.Store(byzHonest)
		if extra != nil {
			extra()
		}
		waitFor(t, 15*time.Second, func() bool { return c.nodes[0].Snapshot().Height >= slashedAt+12 }, name+": chain stopped after the burn")
		s := c.nodes[0].Snapshot()
		for i, nd := range c.nodes {
			if containsStr(nd.GovernanceView().Validators, c.byz.key) {
				t.Errorf("%s: node %d still lists the burned key", name, i)
			}
		}
		signed := false
		for h := slashedAt + 2; h < s.Height; h++ {
		}
		for _, v := range s.LastCommit.Votes {
			if v.Validator == c.byz.key {
				signed = true
			}
		}
		carried, total := c.evidenceCarriedAfter(c.byz.key, slashedAt+2)
		t.Logf("%s: burned=%v height %d->%d, burned key in last commit=%v, evidence carried after burn %d/%d", name, ledger.IsBurned(s.Burned, c.byz.key), slashedAt, s.Height, signed, carried, total)
	}
	{ // static
		honest, keys := gen(t, 4)
		bzS, bzK := gen(t, 1)
		c := &c01net{t: t, bus: newVBus(), honest: honest, keys: keys}
		var vals []consensus.Validator
		for _, k := range append(append([]string{}, keys...), bzK[0]) {
			vals = append(vals, consensus.Validator{PublicKey: k, Power: 1})
			c.genesis = append(c.genesis, ledger.Account{PublicKey: k, Amount: rat("100"), ValidatorNode: k})
		}
		c.byz = newByz(c.bus, bzS[0])
		c.startNodes(consensus.NewValidatorSet(vals), nil)
		at := c.waitSlashed(nil)
		check("static", c, at, func() {
			c.nodes[3].Stop() // one honest laptop asleep: 3 of the remaining 4 = quorum 3 → must keep going
			c.nodes = c.nodes[:3]
		})
		gv := c.nodes[0].GovernanceView()
		t.Logf("static: after burn total=%d quorum=%d", gv.TotalPower, gv.Quorum)
		c.stopAll()
	}
	{ // dynamic delegated
		honest, keys := gen(t, 3)
		bzS, bzK := gen(t, 1)
		_, dk := gen(t, 2)
		c := &c01net{t: t, bus: newVBus(), honest: honest, keys: keys}
		for _, k := range keys {
			c.genesis = append(c.genesis, ledger.Account{PublicKey: k, Amount: rat("200"), ValidatorNode: k})
		}
		c.genesis = append(c.genesis,
			ledger.Account{PublicKey: bzK[0], Amount: rat("1"), ValidatorNode: bzK[0]},
			ledger.Account{PublicKey: dk[0], Amount: rat("100"), ValidatorNode: bzK[0]},
			ledger.Account{PublicKey: dk[1], Amount: rat("100"), ValidatorNode: bzK[0]})
		c.byz = newByz(c.bus, bzS[0])
		c.startNodes(nil, rat("50"))
		at := c.waitSlashed(rat("50"))
		check("dynamic-delegated", c, at, nil)
		c.stopAll()
	}
	{ // dynamic self-staked + re-funding payment
		honest, keys := gen(t, 3)
		bzS, bzK := gen(t, 1)
		sock, sk := gen(t, 1)
		c := &c01net{t: t, bus: newVBus(), honest: honest, keys: keys}
		for _, k := range keys {
			c.genesis = append(c.genesis, ledger.Account{PublicKey: k, Amount: rat("200"), ValidatorNode: k})
		}
		c.genesis = append(c.genesis,
			ledger.Account{PublicKey: bzK[0], Amount: rat("100"), ValidatorNode: bzK[0]},
			ledger.Account{PublicKey: sk[0], Amount: rat("300"), ValidatorNode: keys[0]})
		c.byz = newByz(c.bus, bzS[0])
		c.startNodes(nil, rat("50"))
		at := c.waitSlashed(rat("50"))
		check("dynamic-selfstaked", c, at, func() {
			h := c.nodes[1].Snapshot().Height
			pay, _ := ledger.SignPayment(sock[0], ledger.Payment{PublicKeyFrom: sk[0], PublicKeyTo: c.byz.key, Amount: rat("60"), BlockID: h + 3})
			if err := c.nodes[1].AdmitPayment(pay); err != nil {
				t.Fatalf("payment refused: %v", err)
			}
		})
		t.Logf("dynamic-selfstaked: re-funded balance=%s (account exists, seat refused)", balance(c.nodes[0].Snapshot().Accounts, c.byz.key).RatString())
		c.stopAll()
	}
}
