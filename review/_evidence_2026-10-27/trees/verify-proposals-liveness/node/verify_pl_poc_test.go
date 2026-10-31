package node

// Round-2 verification PoCs (group proposals-liveness). They drive the node through handle(), the
// exact function the run loop calls for every event, so every message goes through the production
// intake: onProposal's height/held/proposer/signature gates, ApplyBlock, rememberProposal, relay.
//
// Run: GOLEDGER_CHAOS_SLACK=4 go test -count=1 -run 'TestVPL' -v ./node/

import (
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// vplNode builds a validator node for pocNet signer `self` and starts height 1 synchronously, with no
// run loop: the test is the loop (it calls handle()), so it can read node state race-free.
func vplNode(p *pocNet, self int) (*Node, *captureTransport) {
	tr := &captureTransport{}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: p.signers[self], Validators: p.vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour, MinBlockInterval: time.Hour, Genesis: p.genesis,
	}, tr)
	nd.started = true
	nd.startHeight()
	return nd, tr
}

func (p *pocNet) sign(signerIdx int, pm ProposalMsg) ProposalMsg {
	p.t.Helper()
	if err := SignProposal(p.signers[signerIdx], &pm); err != nil {
		p.t.Fatal(err)
	}
	return pm
}

func relayedProposalDigests(tr *captureTransport) map[[32]byte]int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := map[[32]byte]int{}
	for _, m := range tr.msgs {
		if m.Type == MsgProposal && m.Proposal != nil {
			out[m.Proposal.Block.StateDigest]++
		}
	}
	return out
}

// C03: the AUTHENTIC round proposer signs two different, fully VALID blocks for (h=1, r=0). Both go
// through the real intake: both are ApplyBlock-validated, stored, and RELAYED; the node keeps the
// first for its prevote; nothing records the double-signing.
func TestVPLProposerEquivocationTwoValidBlocks(t *testing.T) {
	p := newPocNet(t, 4)
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	bob, _ := signing.Generate(signing.Ed25519{}, "testnet")
	p.genesis = []ledger.Account{{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("1000"), ValidatorNode: "v"}}
	byz := p.index(p.vs.ProposerAt(1, 0))
	self := (byz + 1) % 4
	nd, tr := vplNode(p, self)

	state := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	pay, err := ledger.SignPayment(alice, ledger.Payment{PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: bob.PublicKeyBase64(), Amount: rat("5"), BlockID: 1})
	if err != nil {
		t.Fatal(err)
	}
	blkX := ledger.ProposeBlock(signing.Ed25519{}, "testnet", 1, state, ledger.BlockContent{}, p.vs)
	blkY := ledger.ProposeBlock(signing.Ed25519{}, "testnet", 1, state, ledger.BlockContent{Payments: []ledger.Payment{pay}}, p.vs)
	if blkX.StateDigest == blkY.StateDigest {
		t.Fatal("premise: X and Y must have different digests")
	}
	for _, b := range []ledger.Block{blkX, blkY} {
		if _, ok := ledger.ApplyBlock(signing.Ed25519{}, "testnet", state, b, p.vs); !ok {
			t.Fatal("premise: both blocks are valid")
		}
	}
	px := p.sign(byz, ProposalMsg{Height: 1, Round: 0, ValidRound: -1, Block: blkX})
	py := p.sign(byz, ProposalMsg{Height: 1, Round: 0, ValidRound: -1, Block: blkY})

	nd.handle(event{kind: evProposal, proposal: &px})
	nd.handle(event{kind: evProposal, proposal: &py})

	relayed := relayedProposalDigests(tr)
	own := tr.votes(p.signers[self].PublicKeyBase64(), 1, 0, consensus.Prevote)
	_, storedY := nd.blocks[blkY.StateDigest]
	t.Logf("relayed X=%d Y=%d; storedY=%v; own prevotes=%d (for X: %v); driver evidence=%d; pendingSlashes=%d",
		relayed[blkX.StateDigest], relayed[blkY.StateDigest], storedY, len(own),
		len(own) == 1 && own[0].BlockDigest == blkX.StateDigest, len(nd.driver.Evidence()), len(nd.pendingSlashes))
	if relayed[blkY.StateDigest] == 0 || len(nd.driver.Evidence()) != 0 {
		t.Fatal("not reproduced")
	}
	t.Errorf("CONFIRMED C03: second, conflicting VALID proposal from the authentic proposer for (1,0) was validated, stored and relayed; no evidence")
}

// junkBody returns a block for height 1 that passes ApplyBlock's cheap PrevStateDigest gate (so the
// expensive per-payment signature check runs) and carries n junk-signature payments; tag makes the
// claimed digest distinct.
func junkBody(state ledger.LedgerState, n int, tag uint32) ledger.Block {
	k1, _ := signing.Generate(signing.Ed25519{}, "testnet")
	k2, _ := signing.Generate(signing.Ed25519{}, "testnet")
	sig := "AAAA" + string(make([]byte, 0))
	pays := make([]ledger.Payment, n)
	for i := range pays {
		pays[i] = ledger.Payment{PublicKeyFrom: k1.PublicKeyBase64(), PublicKeyTo: k2.PublicKeyBase64(), Amount: rat("1"), BlockID: 1,
			Signature: "x7Yp3tA0b9oG7nQkzv0eMa0d1Yw0pQ3H2b0x8u5U7mCj2y3Q9H3h1wq3Yj8pS1qzQm0eWw6m3o3n5r8k9mZ0Bw==" + sig[:0]}
	}
	var d [32]byte
	d[0], d[1], d[2], d[3] = byte(tag>>24), byte(tag>>16), byte(tag>>8), byte(tag)
	d[31] = 0x5a
	return ledger.Block{Height: 1, PrevStateDigest: state.Digest(), Payments: pays, StateDigest: d}
}

// NEW: one validator — any validator with power, at ANY time during a height — can make every node
// run ApplyBlock on, STORE, and RELAY an unbounded number of distinct ~1 MiB proposals: (a) many
// distinct digests for one round it is scheduled for, and (b) proposals for far-future rounds it is
// scheduled for (no round bound on proposals, unlike votes' maxRoundLookahead).
func TestVPLProposalFloodUnboundedCPUAndMemory(t *testing.T) {
	p := newPocNet(t, 4)
	byz := 0
	byzKey := p.signers[byz].PublicKeyBase64()
	self := 1
	nd, tr := vplNode(p, self)
	state := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}

	// Rounds of height 1 where byz is the scheduled proposer, including ones far beyond the votes'
	// lookahead window (driver round is 0, maxRoundLookahead is 256).
	var rounds []int32
	far := 0
	for r := int32(0); len(rounds) < 20 && r < 256; r++ { // near rounds
		if p.vs.ProposerAt(1, r) == byzKey {
			rounds = append(rounds, r)
		}
	}
	for r := int32(100000); far < 20; r++ { // far rounds, way past the votes' 256-round lookahead
		if p.vs.ProposerAt(1, r) == byzKey {
			rounds = append(rounds, r)
			far++
		}
	}
	const perBody = 3400
	sample := junkBody(state, perBody, 0)
	pm := p.sign(byz, ProposalMsg{Height: 1, Round: rounds[0], ValidRound: -1, Block: sample})
	raw, _ := json.Marshal(pm)
	t.Logf("one junk proposal serializes to %d bytes (HTTP intake limit 1048576)", len(raw))

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	start := time.Now()
	sent := 0
	// (a) 20 distinct digests for one scheduled round + (b) one per further scheduled round.
	for i := 0; i < 20; i++ {
		pm := p.sign(byz, ProposalMsg{Height: 1, Round: rounds[0], ValidRound: -1, Block: junkBody(state, perBody, uint32(1000+i))})
		nd.handle(event{kind: evProposal, proposal: &pm})
		sent++
	}
	for i, r := range rounds[1:] {
		pm := p.sign(byz, ProposalMsg{Height: 1, Round: r, ValidRound: -1, Block: junkBody(state, perBody, uint32(2000+i))})
		nd.handle(event{kind: evProposal, proposal: &pm})
		sent++
	}
	elapsed := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&m1)

	relayed := 0
	for _, c := range relayedProposalDigests(tr) {
		relayed += c
	}
	maxHeld := int32(0)
	for r := range nd.heightProposals {
		if r > maxHeld {
			maxHeld = r
		}
	}
	t.Logf("sent %d signed proposals from ONE validator (%d for far rounds > 256, max round held %d): node spent %v "+
		"in handle() (single event loop), relayed %d of them to its peers, retains %d blocks in n.blocks and %d rounds "+
		"in heightProposals; retained heap grew %.1f MiB",
		sent, far, maxHeld, elapsed.Round(time.Millisecond), relayed, len(nd.blocks), len(nd.heightProposals),
		float64(int64(m1.HeapAlloc)-int64(m0.HeapAlloc))/(1<<20))
	if relayed < sent-1 || len(nd.blocks) < sent {
		t.Fatal("not reproduced")
	}
	t.Errorf("CONFIRMED: no per-round dedup and no round bound on proposals")
}

// C04 + C21 end to end on the pristine tree, no race needed: the node drops the genuine H+1 proposal
// in its post-commit window; a forged copy (genuine signature, swapped body) delivered any time before
// the proposer's next re-gossip tick then occupies the round; the re-gossiped genuine copy is deduped;
// three validators precommit the genuine block and the node still cannot decide.
func TestVPLWindowDropThenForgedCopyPoisons(t *testing.T) {
	p := newPocNet(t, 4)
	if p.vs.ProposerAt(1, 0) == p.vs.ProposerAt(2, 0) {
		t.Skip("needs distinct proposers at heights 1 and 2")
	}
	self := pickNonProposer(p, 1, 2)
	selfKey := p.signers[self].PublicKeyBase64()
	const interval = 300 * time.Millisecond
	nd, tr := p.node(self, 60*time.Second, interval)
	nd.Start()
	defer nd.Stop()

	p.commitHeight(nd, self, 1)
	p2 := p.proposal(nd.Snapshot(), 2, 0)
	nd.SubmitProposal(p2) // lands in the post-commit window
	_ = nd.Snapshot()
	time.Sleep(interval + 200*time.Millisecond) // height 2 started

	forged := p2
	forged.Block = ledger.Block{Height: 2, StateDigest: p2.Block.StateDigest}
	nd.SubmitProposal(forged) // the forger's copy: any time before the proposer's re-gossip tick
	for i := 0; i < 3; i++ {
		nd.SubmitProposal(p2) // genuine re-gossip / relays
	}
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, 2, 0, consensus.Prevote, p2.Block.StateDigest))
		}
	}
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, 2, 0, consensus.Precommit, p2.Block.StateDigest))
		}
	}
	time.Sleep(300 * time.Millisecond)
	h := nd.Snapshot().Height
	own := tr.votes(selfKey, 2, 0, consensus.Prevote)
	var nilD [32]byte
	t.Logf("height after 3/4 precommitted the genuine block: %d; own prevote nil=%v", h, len(own) == 1 && own[0].BlockDigest == nilD)
	if h != 2 {
		t.Fatal("not reproduced: node committed height 2")
	}
	t.Errorf("CONFIRMED: window drop + later forged copy poisons the round deterministically (no race)")
}

// NEW, network level: a 4-validator in-memory network (all-to-all, no faults). After a warm-up, ONE
// validator's key signs junk ~1 MiB proposals for rounds it is scheduled for at the current height and
// POSTs them to ONE node (~4/s). Honest relay spreads them; every node ApplyBlocks each one on its
// single event loop. Measures commits before / during / after.
func TestVPLNetworkFreezeByOneValidator(t *testing.T) {
	c := newChaosCluster(t, 4, 7, nil, nil, nil)
	vals := make([]consensus.Validator, 4)
	for i := range vals {
		vals[i] = consensus.Validator{PublicKey: c.keys[i], Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	var mu sync.Mutex
	seenHeight := int64(1)
	var prevDigest [32]byte
	c.net.register("tap", func(m Message) { // the byzantine validator's own view of the gossip
		if m.Type == MsgProposal && m.Proposal != nil {
			mu.Lock()
			if m.Proposal.Height >= seenHeight {
				seenHeight = m.Proposal.Height
				prevDigest = m.Proposal.Block.PrevStateDigest
			}
			mu.Unlock()
		}
	})
	c.start()
	defer c.finish()

	time.Sleep(2 * time.Second)
	h0 := c.maxHeight()
	t.Logf("warm-up: height %d after 2s", h0)

	stop := make(chan struct{})
	done := make(chan int)
	go func() {
		sent := 0
		tick := time.NewTicker(vplTick)
		defer tick.Stop()
		for i := uint32(0); ; i++ {
			select {
			case <-stop:
				done <- sent
				return
			case <-tick.C:
			}
			mu.Lock()
			h, prev := seenHeight, prevDigest
			mu.Unlock()
			r := int32(1000 + i*7)
			for vs.ProposerAt(h, r) != c.keys[0] {
				r++
			}
			blk := junkBody(ledger.LedgerState{}, 3400, 7000+i)
			blk.Height = h
			blk.PrevStateDigest = prev // the committed state's digest: passes ApplyBlock's cheap gate
			pm := ProposalMsg{Height: h, Round: r, ValidRound: -1, Block: blk}
			_ = SignProposal(c.signers[0], &pm)
			for _, target := range c.nodes[1:] { // POST it to every honest peer, as any validator peer can
				target := target
				go target.SubmitProposal(pm)
			}
			sent++
		}
	}()
	time.Sleep(6 * time.Second)
	close(stop)
	sent := <-done
	h1 := c.maxHeight()
	t.Logf("during 6s attack (%d junk proposals, each POSTed to the 3 honest nodes): height %d -> %d (heights %v)", sent, h0, h1, c.heights())
	time.Sleep(3 * time.Second)
	t.Logf("3s after the attack stopped: height %d", c.maxHeight())
}

var vplTick = func() time.Duration {
	if v := os.Getenv("VPL_TICK_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 250 * time.Millisecond
}()

// C21/LIVE-2, sharper sub-case: Driver.Timeout(StepPrecommit, r) has NO step guard (it always starts
// round r+1). A precommit timer is armed whenever 2/3 precommits-of-anything arrive before a decision
// (e.g. one nil precommit). Armed at height 1, it fires into height 2's round-0 driver mid-prevote and
// knocks this node to round 1 — so it never precommits the healthy round-0 block of height 2.
func TestVPLStalePrecommitTimerAbortsHealthyRound(t *testing.T) {
	p := newPocNet(t, 4)
	self := pickNonProposer(p, 1, 2)
	selfKey := p.signers[self].PublicKeyBase64()
	var others []int
	for i := range p.signers {
		if i != self {
			others = append(others, i)
		}
	}
	const timeout = 1 * time.Second
	nd, tr := p.node(self, timeout, 10*time.Millisecond)
	nd.Start()
	defer nd.Stop()
	time.Sleep(20 * time.Millisecond)

	// Height 1: proposal, 3 prevotes for it, then precommits nil(A), D(B) → with self's own D that is
	// 2/3 precommits-any → precommit timer armed; then D(C) → decide.
	p1 := p.proposal(nd.Snapshot(), 1, 0)
	nd.SubmitProposal(p1)
	for _, i := range others {
		nd.SubmitVote(p.vote(i, 1, 0, consensus.Prevote, p1.Block.StateDigest))
	}
	var nilD [32]byte
	nd.SubmitVote(p.vote(others[0], 1, 0, consensus.Precommit, nilD))
	nd.SubmitVote(p.vote(others[1], 1, 0, consensus.Precommit, p1.Block.StateDigest))
	armedAt := time.Now()
	nd.SubmitVote(p.vote(others[2], 1, 0, consensus.Precommit, p1.Block.StateDigest))
	for nd.Snapshot().Height != 2 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // height 2 started
	p2 := p.proposal(nd.Snapshot(), 2, 0)
	nd.SubmitProposal(p2) // self prevotes D2 at round 0 and waits for the others' prevotes
	_ = nd.Snapshot()
	time.Sleep(time.Until(armedAt.Add(timeout + 150*time.Millisecond))) // the height-1 precommit timer has fired
	for _, i := range others {
		nd.SubmitVote(p.vote(i, 2, 0, consensus.Prevote, p2.Block.StateDigest))
	}
	_ = nd.Snapshot()
	pc := tr.votes(selfKey, 2, 0, consensus.Precommit)
	t.Logf("height-2 round-0 precommits cast by the node after 2/3 prevotes for the valid block: %d", len(pc))
	if len(pc) == 1 && pc[0].BlockDigest == p2.Block.StateDigest {
		t.Fatal("not reproduced: node precommitted round 0 normally")
	}
	t.Errorf("CONFIRMED: a stale height-1 precommit timer moved the node to round 1 of height 2; it never precommitted the healthy round-0 block")
}

// C04 sub-variant: n.blocks is keyed by digest and overwritten by ANY authentic-signature copy. On an
// idle chain every proposer builds the identical empty block, so rounds share one digest D. A node
// that holds round 1's genuine block D then receives a forged-body copy of round 0 (genuine round-0
// signature over D): the forged body overwrites n.blocks[D]; round 1 still DECIDES D, but commit()
// re-applies the forged body, fails, and returns — the driver is decided, the height never commits.
func TestVPLForgedEarlierRoundOverwritesDecidedBlock(t *testing.T) {
	p := newPocNet(t, 4)
	p0 := p.index(p.vs.ProposerAt(1, 0))
	p1 := p.index(p.vs.ProposerAt(1, 1))
	self := -1
	for i := range p.signers {
		if i != p0 && i != p1 {
			self = i
			break
		}
	}
	nd, _ := vplNode(p, self)
	snap := Snapshot{Accounts: nd.accounts, Parameters: nd.parameters}
	genuine0 := p.proposal(snap, 1, 0) // round 0's genuine proposal (never reached this node)
	genuine1 := p.proposal(snap, 1, 1) // round 1's: same empty block, same digest
	if genuine0.Block.StateDigest != genuine1.Block.StateDigest {
		t.Fatal("premise: idle-chain proposals share one digest")
	}
	// Round 0 timed out here (proposal never arrived): advance this node to round 1 via timeouts.
	nd.handle(event{kind: evTimeout, step: consensus.StepPropose, round: 0})
	var nilD [32]byte
	for _, i := range []int{0, 1, 2, 3} {
		if i != self {
			v := p.vote(i, 1, 0, consensus.Precommit, nilD)
			nd.handle(event{kind: evVote, vote: &v})
		}
	}
	nd.handle(event{kind: evTimeout, step: consensus.StepPrecommit, round: 0})
	if nd.driver.Round() != 1 {
		t.Fatalf("premise: node should be at round 1, at %d", nd.driver.Round())
	}
	nd.handle(event{kind: evProposal, proposal: &genuine1}) // the real round-1 block: valid, prevoted
	forged := genuine0
	forged.Block = ledger.Block{Height: 1, StateDigest: genuine0.Block.StateDigest}
	nd.handle(event{kind: evProposal, proposal: &forged}) // forged copy of round 0 arrives late
	for i := range p.signers {
		if i != self {
			v := p.vote(i, 1, 1, consensus.Precommit, genuine1.Block.StateDigest)
			nd.handle(event{kind: evVote, vote: &v})
		}
	}
	d, decided := nd.driver.Decision()
	t.Logf("driver decided=%v (digest matches=%v); node height=%d", decided, d == genuine1.Block.StateDigest, nd.height)
	if !decided || nd.height != 1 {
		t.Fatal("not reproduced")
	}
	t.Errorf("CONFIRMED: decided the valid block but the forged body overwrote n.blocks[D]; commit failed, height stays 1 with a decided driver")
}
