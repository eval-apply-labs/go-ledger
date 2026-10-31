package node

// PoC tests for the root cause of the TestPaymentGossipReceiptAndReplay flake (scratch copy only).
//
// They drive ONE real Node by hand — the test holds every validator key, delivers proposals and
// votes itself, and captures what the node broadcasts — so they are deterministic (no reliance on
// goroutine scheduling or a quiet machine): every timing margin is hundreds of milliseconds.
//
// Run: go test -count=1 -run 'TestPoC' -v ./node/
//
//   TestPoCDeadWindowProposalDropped          — the H+1 proposal arriving between commit(H) and
//                                               startHeight(H+1) is checked against the OLD driver's
//                                               proposer schedule, dropped, and not relayed; the node
//                                               starts H+1 without it and cannot prevote until the
//                                               proposer's next re-gossip tick (cfg.Timeout later).
//   TestPoCDeadWindowProposalHeldNeverProcessed — when the old and new round-0 proposers coincide
//                                               (86% of heights with 3 equal validators), the H+1
//                                               proposal is fed to the dead driver and HELD; the held
//                                               copy then dedups every re-gossiped copy, so the live
//                                               driver never sees round 0's proposal at all.
//   TestPoCStaleTimeoutFiresIntoLaterHeight   — evTimeout carries no height: a propose timer armed at
//                                               height H fires into height H+1's driver (same round
//                                               number) and makes the node prevote nil long before
//                                               its own H+1 propose timeout, rejecting a timely,
//                                               valid proposal.

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// captureTransport records everything the node under test broadcasts and delivers nothing.
type captureTransport struct {
	mu   sync.Mutex
	msgs []Message
}

func (c *captureTransport) Broadcast(from string, m Message) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
}

// votes returns the distinct votes the node broadcast AS ITSELF (its own signed votes, not relays of
// peers' votes) at (height, round, type); re-gossip re-sends are collapsed by signature.
func (c *captureTransport) votes(self string, height int64, round int32, typ consensus.VoteType) []consensus.Vote {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []consensus.Vote
	seen := map[string]bool{}
	for _, m := range c.msgs {
		if m.Type == MsgVote && m.Vote != nil && m.Vote.Validator == self && m.Vote.Height == height &&
			m.Vote.Round == round && m.Vote.Type == typ && !seen[m.Vote.Signature] {
			seen[m.Vote.Signature] = true
			out = append(out, *m.Vote)
		}
	}
	return out
}

func (c *captureTransport) relayedProposals(height int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.msgs {
		if m.Type == MsgProposal && m.Proposal != nil && m.Proposal.Height == height {
			n++
		}
	}
	return n
}

// pocNet is a hand-driven validator set: the test holds all keys (sorted = validator-set order).
type pocNet struct {
	t       *testing.T
	signers []*signing.Signer
	vs      *consensus.ValidatorSet
	genesis []ledger.Account
}

func newPocNet(t *testing.T, n int) *pocNet {
	t.Helper()
	p := &pocNet{t: t}
	for i := 0; i < n; i++ {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		p.signers = append(p.signers, s)
	}
	sort.Slice(p.signers, func(i, j int) bool { return p.signers[i].PublicKeyBase64() < p.signers[j].PublicKeyBase64() })
	var vals []consensus.Validator
	for _, s := range p.signers {
		vals = append(vals, consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1})
	}
	p.vs = consensus.NewValidatorSet(vals)
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	p.genesis = []ledger.Account{{PublicKey: alice.PublicKeyBase64(), Name: "alice", Amount: rat("1000"), ValidatorNode: "v"}}
	return p
}

func (p *pocNet) index(key string) int {
	for i, s := range p.signers {
		if s.PublicKeyBase64() == key {
			return i
		}
	}
	p.t.Fatalf("unknown key %s", key)
	return -1
}

func (p *pocNet) node(self int, timeout, interval time.Duration) (*Node, *captureTransport) {
	tr := &captureTransport{}
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: p.signers[self], Validators: p.vs,
		TransactionCost: rat("0.1"), Timeout: timeout, MinBlockInterval: interval, Genesis: p.genesis,
	}, tr)
	return nd, tr
}

// proposal builds the (empty) block for height on the node's committed state, signed by the
// scheduled round proposer — exactly what an honest proposer with an empty mempool broadcasts.
func (p *pocNet) proposal(snap Snapshot, height int64, round int32) ProposalMsg {
	p.t.Helper()
	state := ledger.LedgerState{Accounts: snap.Accounts, Parameters: snap.Parameters, GovernanceSequence: snap.GovernanceSequence}
	blk := ledger.ProposeBlock(signing.Ed25519{}, "testnet", height, state, ledger.BlockContent{}, p.vs)
	pm := ProposalMsg{Height: height, Round: round, ValidRound: -1, Block: blk}
	if err := SignProposal(p.signers[p.index(p.vs.ProposerAt(height, round))], &pm); err != nil {
		p.t.Fatal(err)
	}
	return pm
}

func (p *pocNet) vote(i int, height int64, round int32, typ consensus.VoteType, digest [32]byte) consensus.Vote {
	p.t.Helper()
	v, err := consensus.SignVote(p.signers[i], consensus.Vote{Height: height, Round: round, Type: typ, BlockDigest: digest, Validator: p.signers[i].PublicKeyBase64()})
	if err != nil {
		p.t.Fatal(err)
	}
	return v
}

// commitHeight drives the node through `height` round 0: the proposer's block, then every other
// validator's prevote and precommit for it. Returns once the node has committed (Height advanced).
func (p *pocNet) commitHeight(nd *Node, self int, height int64) {
	p.t.Helper()
	pm := p.proposal(nd.Snapshot(), height, 0)
	if p.index(pm.Proposer) != self {
		nd.SubmitProposal(pm)
	}
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, height, 0, consensus.Prevote, pm.Block.StateDigest))
		}
	}
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, height, 0, consensus.Precommit, pm.Block.StateDigest))
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for nd.Snapshot().Height != height+1 {
		if time.Now().After(deadline) {
			p.t.Fatalf("node did not commit height %d", height)
		}
		time.Sleep(time.Millisecond)
	}
}

func pickNonProposer(p *pocNet, heights ...int64) int {
	for i := range p.signers {
		ok := true
		for _, h := range heights {
			if p.index(p.vs.ProposerAt(h, 0)) == i {
				ok = false
			}
		}
		if ok {
			return i
		}
	}
	p.t.Fatal("no validator is a non-proposer at all the given heights")
	return -1
}

// Four equal validators (round-robin proposers: H and H+1 always differ). The H+1 proposal lands in
// the post-commit window: it is checked against the OLD driver's schedule, dropped, not relayed.
func TestPoCDeadWindowProposalDropped(t *testing.T) {
	p := newPocNet(t, 4)
	if p.vs.ProposerAt(1, 0) == p.vs.ProposerAt(2, 0) {
		t.Skip("needs distinct proposers at heights 1 and 2 (always true for 4 equal validators)")
	}
	self := pickNonProposer(p, 1, 2)
	selfKey := p.signers[self].PublicKeyBase64()
	const interval = 1 * time.Second                 // the post-commit window (MinBlockInterval)
	nd, tr := p.node(self, 60*time.Second, interval) // long timeout: no timer or re-gossip tick interferes
	nd.Start()
	defer nd.Stop()

	p.commitHeight(nd, self, 1)
	committedAt := time.Now()

	// The height-2 proposer (it committed height 1 first and its own interval elapsed first) sends
	// its proposal; here it arrives while this node is still inside its post-commit window.
	p2 := p.proposal(nd.Snapshot(), 2, 0)
	nd.SubmitProposal(p2)
	_ = nd.Snapshot() // the proposal event has been handled once the snapshot returns (FIFO loop)
	arrivedAfter := time.Since(committedAt)
	if arrivedAfter > interval/2 {
		t.Skip("machine too slow to land the proposal inside the window; rerun")
	}
	relayedInWindow := tr.relayedProposals(2)

	time.Sleep(interval + 500*time.Millisecond) // the node has now started height 2
	if got := tr.votes(selfKey, 2, 0, consensus.Prevote); len(got) != 0 {
		t.Fatalf("unexpected: node prevoted at height 2 without re-delivery: %+v", got)
	}
	t.Logf("BUG: a valid, authentic, timely height-2 proposal was received %v after commit(1) and DROPPED "+
		"(relayed onward: %d copies); the node started height 2 with no proposal and no prevote. "+
		"Recovery requires the proposer's next re-gossip tick (every cfg.Timeout).", arrivedAfter.Round(time.Microsecond), relayedInWindow)

	// Re-delivery (what reGossipProposal eventually does) is processed normally → proves the only
	// reason the node did not prevote is the window drop.
	nd.SubmitProposal(p2)
	_ = nd.Snapshot()
	got := tr.votes(selfKey, 2, 0, consensus.Prevote)
	if len(got) != 1 || got[0].BlockDigest != p2.Block.StateDigest {
		t.Fatalf("re-delivered proposal should be prevoted, got %+v", got)
	}
	t.Errorf("height-2 proposal delivered in the post-commit window was dropped (prevote only after re-delivery)")
}

// Three equal validators: FNV-of-height gives the same round-0 proposer at consecutive heights 86% of
// the time (see notes). The H+1 proposal arriving in the window then PASSES the stale-driver proposer
// check, is fed to the decided driver (ignored), and is HELD — and the held copy makes every later
// re-gossiped copy a "duplicate" that is never fed to the live driver. Round 0 of H+1 is lost for good
// on this node; with 3 validators (quorum = all 3) round 0 fails network-wide.
func TestPoCDeadWindowProposalHeldNeverProcessed(t *testing.T) {
	p := newPocNet(t, 3)
	if p.vs.ProposerAt(1, 0) != p.vs.ProposerAt(2, 0) {
		t.Skip("needs the same proposer at heights 1 and 2 (true for 3 equal validators)")
	}
	self := pickNonProposer(p, 1, 2)
	selfKey := p.signers[self].PublicKeyBase64()
	const interval = 1 * time.Second
	nd, tr := p.node(self, 60*time.Second, interval)
	nd.Start()
	defer nd.Stop()

	p.commitHeight(nd, self, 1)
	committedAt := time.Now()
	p2 := p.proposal(nd.Snapshot(), 2, 0)
	nd.SubmitProposal(p2)
	_ = nd.Snapshot()
	if time.Since(committedAt) > interval/2 {
		t.Skip("machine too slow to land the proposal inside the window; rerun")
	}
	if tr.relayedProposals(2) == 0 {
		t.Fatal("expected the in-window proposal to be accepted (and relayed) — precondition failed")
	}

	time.Sleep(interval + 500*time.Millisecond) // height 2 started
	// Every honest peer keeps re-sending it (reGossipProposal / relays). None of it reaches the driver.
	for i := 0; i < 5; i++ {
		nd.SubmitProposal(p2)
	}
	// Even the other validators' prevotes for it cannot help: the live driver has no proposal.
	for i := range p.signers {
		if i != self {
			nd.SubmitVote(p.vote(i, 2, 0, consensus.Prevote, p2.Block.StateDigest))
		}
	}
	_ = nd.Snapshot()
	time.Sleep(200 * time.Millisecond)
	if got := tr.votes(selfKey, 2, 0, consensus.Prevote); len(got) != 0 {
		t.Fatalf("unexpected: node prevoted height 2: %+v", got)
	}
	t.Errorf("BUG: height-2 round-0 proposal received in the post-commit window is held but never fed to " +
		"the live driver; 5 re-deliveries + 2 peer prevotes later the node still has not prevoted (round 0 lost)")
}

// Timeouts are not height-scoped. A propose timer armed at height 1 fires into height 2's driver
// (round 0 again) and the node prevotes nil only ~0.3s into height 2, though its own height-2 propose
// timeout is a full cfg.Timeout (1s). A valid proposal arriving 0.45s into height 2 is then refused.
func TestPoCStaleTimeoutFiresIntoLaterHeight(t *testing.T) {
	p := newPocNet(t, 4)
	self := pickNonProposer(p, 1, 2)
	selfKey := p.signers[self].PublicKeyBase64()
	const timeout = 1 * time.Second
	nd, tr := p.node(self, timeout, 10*time.Millisecond)
	start := time.Now()
	nd.Start() // height 1 starts now: as a non-proposer the node arms (propose, round 0) for +1s

	time.Sleep(650 * time.Millisecond)
	p.commitHeight(nd, self, 1)
	defer nd.Stop()
	time.Sleep(30 * time.Millisecond) // > MinBlockInterval: height 2 has started
	h2Start := time.Since(start)
	if h2Start > 850*time.Millisecond {
		t.Skip("machine too slow for the margins; rerun")
	}
	// Its OWN height-2 propose timeout fires at ≈ h2Start + 1s. Wait until just past the height-1 timer.
	time.Sleep(time.Until(start.Add(timeout + 100*time.Millisecond)))
	early := tr.votes(selfKey, 2, 0, consensus.Prevote)

	// A valid, authentic proposal for height 2 now arrives — ~0.45s into a 1s propose timeout.
	p2 := p.proposal(nd.Snapshot(), 2, 0)
	nd.SubmitProposal(p2)
	_ = nd.Snapshot()
	got := tr.votes(selfKey, 2, 0, consensus.Prevote)
	if len(got) == 1 && got[0].BlockDigest == p2.Block.StateDigest {
		t.Fatal("node prevoted the proposal — stale timer did not fire (bug not reproduced)")
	}
	var nilDigest [32]byte
	if len(early) != 1 || early[0].BlockDigest != nilDigest {
		t.Fatalf("expected an early nil prevote from the stale timer, got %+v", early)
	}
	t.Errorf("BUG: height 2 started at %v; the node prevoted NIL at height 2 before %v (driven by the height-1 propose "+
		"timer — its own height-2 timeout is due at ~%v) and so rejected a valid proposal delivered %v into the height",
		h2Start.Round(time.Millisecond), (timeout + 100*time.Millisecond), (h2Start + timeout).Round(time.Millisecond),
		(timeout + 100*time.Millisecond - h2Start).Round(time.Millisecond))
}
