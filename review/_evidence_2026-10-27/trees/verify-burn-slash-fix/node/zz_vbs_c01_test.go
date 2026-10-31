package node

// C01 verification through the REAL pipeline: a byzantine agent holding a real validator key
// double-signs, honest nodes collect the evidence in their drivers, carry it into a proposed block,
// and the slash commits via ApplyBlock on every node. Then we check what the "burned" key can still do.

import (
	"crypto/rand"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

const (
	byzEquivocate int32 = iota
	byzHonest
	byzAbstain
)

type byzAgent struct {
	key    string
	signer *signing.Signer
	bus    *vbus
	mode   atomic.Int32
	mu     sync.Mutex
	seenEq map[int64]bool
	seenOK map[[2]int64]bool
}

func newByz(b *vbus, s *signing.Signer) *byzAgent {
	a := &byzAgent{key: s.PublicKeyBase64(), signer: s, bus: b, seenEq: map[int64]bool{}, seenOK: map[[2]int64]bool{}}
	b.setHandler(a.key, a.handle)
	return a
}

func (a *byzAgent) send(v consensus.Vote) {
	signed, err := consensus.SignVote(a.signer, v)
	if err == nil {
		a.bus.Broadcast(a.key, Message{Type: MsgVote, From: a.key, Vote: &signed})
	}
}

func (a *byzAgent) handle(m Message) {
	if m.Type != MsgProposal || m.Proposal == nil {
		return
	}
	h, r, real := m.Proposal.Height, m.Proposal.Round, m.Proposal.Block.StateDigest
	switch a.mode.Load() {
	case byzEquivocate:
		a.mu.Lock()
		if a.seenEq[h] {
			a.mu.Unlock()
			return
		}
		a.seenEq[h] = true
		a.mu.Unlock()
		var fake [32]byte
		_, _ = rand.Read(fake[:])
		for rr := int32(0); rr < 2; rr++ {
			for _, typ := range []consensus.VoteType{consensus.Prevote, consensus.Precommit} {
				for _, d := range [][32]byte{real, fake} {
					a.send(consensus.Vote{Height: h, Round: rr, Type: typ, BlockDigest: d, Validator: a.key})
				}
			}
		}
	case byzHonest:
		a.mu.Lock()
		k := [2]int64{h, int64(r)}
		if a.seenOK[k] {
			a.mu.Unlock()
			return
		}
		a.seenOK[k] = true
		a.mu.Unlock()
		a.send(consensus.Vote{Height: h, Round: r, Type: consensus.Prevote, BlockDigest: real, Validator: a.key})
		a.send(consensus.Vote{Height: h, Round: r, Type: consensus.Precommit, BlockDigest: real, Validator: a.key})
	}
}

type c01net struct {
	t       *testing.T
	bus     *vbus
	honest  []*signing.Signer
	keys    []string
	nodes   []*Node
	byz     *byzAgent
	genesis []ledger.Account
}

func (c *c01net) stopAll() {
	for _, nd := range c.nodes {
		nd.Stop()
	}
}

func (c *c01net) startNodes(vs *consensus.ValidatorSet, threshold *big.Rat) {
	for i, s := range c.honest {
		nd := New(Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: s, Validators: vs, ValidatorThreshold: threshold,
			TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond,
			Genesis: c.genesis,
		}, c.bus)
		c.nodes = append(c.nodes, nd)
		c.bus.setNode(c.keys[i], nd)
	}
	for _, nd := range c.nodes {
		nd.Start()
	}
}

func gen(t *testing.T, n int) ([]*signing.Signer, []string) {
	var ss []*signing.Signer
	var ks []string
	for i := 0; i < n; i++ {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		ss = append(ss, s)
		ks = append(ks, s.PublicKeyBase64())
	}
	return ss, ks
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (c *c01net) waitSlashed(threshold *big.Rat) int64 {
	c.t.Helper()
	waitFor(c.t, 20*time.Second, func() bool {
		for _, nd := range c.nodes {
			if balance(nd.Snapshot().Accounts, c.byz.key).Sign() != 0 {
				return false
			}
		}
		return true
	}, "the equivocator was not slashed on every honest node")
	return c.nodes[0].Snapshot().Height
}

// evidenceCarriedAfter counts honest proposals at heights >= h whose block carries evidence against key.
func (c *c01net) evidenceCarriedAfter(key string, h int64) (carried, total int) {
	for _, k := range c.keys {
		for _, p := range c.bus.proposalsFrom(k) {
			if p.Height < h {
				continue
			}
			total++
			for _, e := range p.Block.Evidence {
				if e.Validator == key {
					carried++
					break
				}
			}
		}
	}
	return
}

// lastCommitHas waits until some honest node's LastCommit (at a height > after) carries key's precommit.
func (c *c01net) waitCommitSignedBy(key string, after int64) int64 {
	c.t.Helper()
	var at int64
	waitFor(c.t, 20*time.Second, func() bool {
		for _, nd := range c.nodes {
			s := nd.Snapshot()
			if s.LastCommit.Height <= after {
				continue
			}
			for _, v := range s.LastCommit.Votes {
				if v.Validator == key {
					at = s.LastCommit.Height
					return true
				}
			}
		}
		return false
	}, "the slashed key's precommit never appeared in a commit certificate")
	return at
}

// Mode 1 — STATIC set (the documented default deployment).
func TestVBSC01StaticSlashKeepsKeyVotingAndGoverning(t *testing.T) {
	honest, keys := gen(t, 4)
	bzS, bzK := gen(t, 1)
	c := &c01net{t: t, bus: newVBus(), honest: honest, keys: keys}
	var vals []consensus.Validator
	for _, k := range append(append([]string{}, keys...), bzK[0]) {
		vals = append(vals, consensus.Validator{PublicKey: k, Power: 1})
		c.genesis = append(c.genesis, ledger.Account{PublicKey: k, Amount: rat("100"), ValidatorNode: k})
	}
	vs := consensus.NewValidatorSet(vals) // 5 x power 1: total 5, quorum 4
	c.byz = newByz(c.bus, bzS[0])
	c.startNodes(vs, nil)
	defer c.stopAll()

	slashedAt := c.waitSlashed(nil)
	c.byz.mode.Store(byzHonest)
	t.Logf("static: equivocator's account slashed on all honest nodes by height %d", slashedAt)
	for i, nd := range c.nodes {
		gv := nd.GovernanceView()
		t.Logf("  node %d: validator set still lists the slashed key=%v (total power %d, quorum %d)", i, containsStr(gv.Validators, c.byz.key), gv.TotalPower, gv.Quorum)
		if !containsStr(gv.Validators, c.byz.key) {
			t.Fatalf("unexpected: slashed key left the static set")
		}
	}
	// Its consensus votes still count: it appears in a later ⅔ commit certificate.
	at := c.waitCommitSignedBy(c.byz.key, slashedAt+1)
	t.Logf("  slashed key's precommit is part of the commit certificate at height %d (counted toward quorum)", at)
	carried, total := c.evidenceCarriedAfter(c.byz.key, slashedAt+1)
	t.Logf("  honest proposals after the slash still carrying the same evidence: %d of %d (static: slashEvidence keeps it until the 1000-block window ends)", carried, total)

	// Governance: the slashed key's vote is still decisive. Honest H1..H3 (3) + slashed key (1) = 4 = quorum.
	s := c.nodes[0].Snapshot()
	seq := s.GovernanceSequence
	target := "treasury-grant-target"
	mk := func(sg *signing.Signer) ledger.ReassignmentVote {
		v, _ := ledger.SignReassignmentVote(sg, target, new(big.Rat), rat("1000"), seq)
		return v
	}
	withBurned := ledger.Reassignment{Account: target, PreviousAmount: new(big.Rat), NewAmount: rat("1000"), Sequence: seq,
		Votes: []ledger.ReassignmentVote{mk(honest[0]), mk(honest[1]), mk(honest[2]), mk(bzS[0])}}
	without := withBurned
	without.Votes = withBurned.Votes[:3]
	t.Logf("  reassignment certificate valid WITHOUT the slashed key's vote: %v; WITH it: %v",
		without.Valid(signing.Ed25519{}, "testnet", vs, s.Accounts, seq), withBurned.Valid(signing.Ed25519{}, "testnet", vs, s.Accounts, seq))
	for _, nd := range c.nodes {
		nd.SubmitReassignmentVote(withBurned)
	}
	waitFor(t, 10*time.Second, func() bool { return balance(c.nodes[0].Snapshot().Accounts, target).Cmp(rat("1000")) == 0 },
		"reassignment carried by the slashed key's vote did not enact")
	t.Logf("  ENACTED through the node pipeline: a ⅔ reassignment whose deciding vote is the slashed key's")

	// Liveness: one honest laptop sleeps and the slashed key withholds its vote → 3 of 5 < quorum 4.
	// Were the key excluded, the set would be 4 with quorum 3 and the chain would keep going.
	c.byz.mode.Store(byzAbstain)
	c.nodes[3].Stop()
	c.nodes = c.nodes[:3]
	time.Sleep(time.Second)
	h0 := c.nodes[0].Snapshot().Height
	time.Sleep(6 * time.Second)
	h1 := c.nodes[0].Snapshot().Height
	t.Logf("  with 1 honest laptop asleep and the slashed key abstaining: height %d -> %d over 6s", h0, h1)
	if h1 == h0 {
		t.Errorf("STATIC: a slashed key keeps full consensus and governance power and still counts in the quorum denominator; 1 sleeping honest laptop now stalls the chain")
	}
}

// Mode 2 — DYNAMIC set, stake delegated by others (the "bank" model).
func TestVBSC01DynamicDelegatedSlashKeepsSeat(t *testing.T) {
	honest, keys := gen(t, 3)
	bzS, bzK := gen(t, 1)
	_, dk := gen(t, 2)
	c := &c01net{t: t, bus: newVBus(), honest: honest, keys: keys}
	for _, k := range keys {
		c.genesis = append(c.genesis, ledger.Account{PublicKey: k, Amount: rat("200"), ValidatorNode: k})
	}
	c.genesis = append(c.genesis,
		ledger.Account{PublicKey: bzK[0], Amount: rat("1"), ValidatorNode: bzK[0]},
		ledger.Account{PublicKey: dk[0], Amount: rat("100"), ValidatorNode: bzK[0]}, // customers delegate to the bank
		ledger.Account{PublicKey: dk[1], Amount: rat("100"), ValidatorNode: bzK[0]})
	threshold := rat("50")
	c.byz = newByz(c.bus, bzS[0])
	c.startNodes(nil, threshold)
	defer c.stopAll()

	slashedAt := c.waitSlashed(threshold)
	c.byz.mode.Store(byzHonest)
	for i, nd := range c.nodes {
		s := nd.Snapshot()
		set := deriveValidators(s.Accounts, threshold)
		t.Logf("  node %d after slash: key in set=%v power=%d of %d (quorum %d)", i, set.Contains(c.byz.key), set.Power(c.byz.key), set.TotalPower(), set.Quorum())
	}
	at := c.waitCommitSignedBy(c.byz.key, slashedAt+1)
	carried, total := c.evidenceCarriedAfter(c.byz.key, slashedAt+1)
	t.Logf("  slashed key co-signs the commit at height %d; honest proposals still carrying its evidence: %d of %d", at, carried, total)
	if deriveValidators(c.nodes[0].Snapshot().Accounts, threshold).Contains(c.byz.key) {
		t.Errorf("DYNAMIC/DELEGATED: slash removed a 1-unit account; the key keeps 200 delegated power and keeps voting")
	}
}

// Mode 3 — DYNAMIC set, self-staked: ejected by the slash, then re-admitted by one incoming payment.
func TestVBSC01DynamicSelfStakedReadmittedByPayment(t *testing.T) {
	honest, keys := gen(t, 3)
	bzS, bzK := gen(t, 1)
	sock, sk := gen(t, 1)
	c := &c01net{t: t, bus: newVBus(), honest: honest, keys: keys}
	for _, k := range keys {
		c.genesis = append(c.genesis, ledger.Account{PublicKey: k, Amount: rat("200"), ValidatorNode: k})
	}
	c.genesis = append(c.genesis,
		ledger.Account{PublicKey: bzK[0], Amount: rat("100"), ValidatorNode: bzK[0]},
		ledger.Account{PublicKey: sk[0], Amount: rat("300"), ValidatorNode: keys[0]}) // attacker's sock puppet, delegated to an honest node
	threshold := rat("50")
	c.byz = newByz(c.bus, bzS[0])
	c.startNodes(nil, threshold)
	defer c.stopAll()

	slashedAt := c.waitSlashed(threshold)
	c.byz.mode.Store(byzHonest)
	waitFor(t, 5*time.Second, func() bool {
		for _, nd := range c.nodes {
			if containsStr(nd.GovernanceView().Validators, c.byz.key) {
				return false
			}
		}
		return true
	}, "slashed self-staked key was not ejected")
	t.Logf("self-staked: slashed and ejected by height %d", slashedAt)

	// The sock puppet pays the burned key 60 through the normal client intake.
	h := c.nodes[1].Snapshot().Height
	pay, _ := ledger.SignPayment(sock[0], ledger.Payment{PublicKeyFrom: sk[0], PublicKeyTo: c.byz.key, Amount: rat("60"), BlockID: h + 3})
	if err := c.nodes[1].AdmitPayment(pay); err != nil {
		t.Fatalf("payment refused: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, nd := range c.nodes {
			if !containsStr(nd.GovernanceView().Validators, c.byz.key) {
				return false
			}
		}
		return true
	}, "the burned key was not re-admitted")
	readmit := c.nodes[0].Snapshot().Height
	at := c.waitCommitSignedBy(c.byz.key, readmit)
	time.Sleep(time.Second)
	carried, total := c.evidenceCarriedAfter(c.byz.key, readmit)
	t.Logf("  re-admitted on all nodes by height %d (power %d); its precommit counts in the commit at %d; honest proposals carrying evidence against it since: %d of %d",
		readmit, deriveValidators(c.nodes[0].Snapshot().Accounts, threshold).Power(c.byz.key), at, carried, total)
	t.Errorf("DYNAMIC/SELF-STAKED: a key slashed for equivocation is back in the validator set after one 60-unit payment, and nobody re-slashes it")
}
