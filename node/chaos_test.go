package node

// The node-level tier of the adversarial harness (TODO.md): the REAL stack — event loop, mempool,
// governance pools, slashing, snapshot/catch-up — run under fault injection: message loss,
// duplication, random delay (reorder), partitions, and byzantine agents holding real validator
// keys. Complements the deterministic driver simulator (consensus/sim_test.go): that tier replays
// exactly but models only the driver; this tier is wall-clock (goroutine scheduling is real), so
// the SEED reproduces the fault schedule, not the exact interleaving. Every scenario runs the same
// two checkers throughout: no fork (each committed height has ONE digest network-wide) and no
// silent minting (supply never exceeds genesis outside an explicit ⅔ mint).
//
// Scale knobs: -short skips the chaos scenarios entirely; GOLEDGER_CHAOS_SEED replays a fault
// schedule; GOLEDGER_CHAOS_SOAK_SECONDS lengthens the randomized soak.

import (
	"math/big"
	"math/rand"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

func chaosSeed(t *testing.T) int64 {
	t.Helper()
	if v := os.Getenv("GOLEDGER_CHAOS_SEED"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			t.Logf("chaos seed %d (from GOLEDGER_CHAOS_SEED)", n)
			return n
		}
	}
	seed := time.Now().UnixNano()
	t.Logf("chaos seed %d (set GOLEDGER_CHAOS_SEED=%d to replay this fault schedule)", seed, seed)
	return seed
}

// ---- the chaos transport ----

// chaosNet is an in-memory Transport that injects faults between registered participants: loss,
// duplication, random delay (which is reorder, since deliveries race), and partitions. Participants
// are handler funcs, so byzantine agents (a key with custom behavior, no Node) plug in beside real
// nodes. All randomness comes from one seeded RNG (mutex-guarded — Broadcast is called from many
// goroutines), so the fault schedule is reproducible even though goroutine interleaving is not.
type chaosNet struct {
	mu       sync.Mutex
	rng      *rand.Rand
	handlers map[string]func(Message)
	group    map[string]int // partition group per key; different group = no delivery. Empty = healed.
	dropProb float64
	dupProb  float64
	maxDelay time.Duration
}

func newChaosNet(seed int64) *chaosNet {
	return &chaosNet{
		rng:      rand.New(rand.NewSource(seed)),
		handlers: map[string]func(Message){},
		group:    map[string]int{},
	}
}

func (c *chaosNet) register(key string, h func(Message)) {
	c.mu.Lock()
	c.handlers[key] = h
	c.mu.Unlock()
}

func (c *chaosNet) registerNode(key string, nd *Node) {
	c.register(key, func(m Message) { deliverToNode(nd, m) })
}

func (c *chaosNet) setFaults(drop, dup float64, maxDelay time.Duration) {
	c.mu.Lock()
	c.dropProb, c.dupProb, c.maxDelay = drop, dup, maxDelay
	c.mu.Unlock()
}

func (c *chaosNet) partition(groups map[string]int) {
	c.mu.Lock()
	c.group = groups
	c.mu.Unlock()
}

func (c *chaosNet) heal() { c.partition(map[string]int{}) }

func (c *chaosNet) Broadcast(from string, msg Message) {
	type delivery struct {
		h     func(Message)
		delay time.Duration
	}
	var out []delivery
	c.mu.Lock()
	fromGroup := c.group[from]
	for key, h := range c.handlers {
		if key == from || c.group[key] != fromGroup {
			continue
		}
		if c.rng.Float64() < c.dropProb {
			continue
		}
		copies := 1
		if c.rng.Float64() < c.dupProb {
			copies = 2
		}
		for i := 0; i < copies; i++ {
			var d time.Duration
			if c.maxDelay > 0 {
				d = time.Duration(c.rng.Int63n(int64(c.maxDelay)))
			}
			out = append(out, delivery{h: h, delay: d})
		}
	}
	c.mu.Unlock()
	for _, dv := range out {
		dv := dv
		time.AfterFunc(dv.delay, func() { dv.h(msg) })
	}
}

func deliverToNode(nd *Node, m Message) {
	switch m.Type {
	case MsgProposal:
		nd.SubmitProposal(*m.Proposal)
	case MsgVote:
		nd.SubmitVote(*m.Vote)
	case MsgPayment:
		nd.SubmitPayment(*m.Payment)
	case MsgUpdate:
		nd.SubmitUpdate(*m.Update)
	case MsgParameterVote:
		nd.SubmitParameterVote(*m.ParameterChange)
	case MsgReassignmentVote:
		nd.SubmitReassignmentVote(*m.Reassignment)
	}
}

// ---- the checkers ----

// chaosChecker polls every node's snapshot and holds the two Jepsen-style invariants across the
// whole run: (1) agreement — a committed height has exactly one digest network-wide; (2) supply —
// the total never exceeds the cap (genesis supply, plus any explicit ⅔ mint the scenario performs).
type chaosChecker struct {
	mu         sync.Mutex
	commits    map[int64][32]byte
	violations []string
	supplyCap  *big.Rat
	stop       chan struct{}
	done       chan struct{}
}

func watchInvariants(nodes []*Node, supplyCap *big.Rat) *chaosChecker {
	c := &chaosChecker{
		commits:   map[int64][32]byte{},
		supplyCap: new(big.Rat).Set(supplyCap),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-ticker.C:
				for i, nd := range nodes {
					s := nd.Snapshot()
					if s.LastCommit.Height <= 0 {
						continue
					}
					c.mu.Lock()
					if seen, ok := c.commits[s.LastCommit.Height]; ok {
						if seen != s.LastCommit.BlockDigest {
							c.violations = append(c.violations, "FORK at height "+strconv.FormatInt(s.LastCommit.Height, 10)+" observed via node "+strconv.Itoa(i))
						}
					} else {
						c.commits[s.LastCommit.Height] = s.LastCommit.BlockDigest
					}
					supply := new(big.Rat)
					for _, a := range s.Accounts {
						if a.Amount != nil {
							supply.Add(supply, a.Amount)
						}
					}
					if supply.Cmp(c.supplyCap) > 0 {
						c.violations = append(c.violations, "SUPPLY "+supply.RatString()+" exceeds cap "+c.supplyCap.RatString()+" on node "+strconv.Itoa(i))
					}
					c.mu.Unlock()
				}
			}
		}
	}()
	return c
}

// raiseCap widens the supply cap when a scenario performs an explicit, legitimate ⅔ mint.
func (c *chaosChecker) raiseCap(by *big.Rat) {
	c.mu.Lock()
	c.supplyCap.Add(c.supplyCap, by)
	c.mu.Unlock()
}

func (c *chaosChecker) assertClean(t *testing.T) {
	t.Helper()
	close(c.stop)
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.violations {
		t.Error(v)
	}
}

// ---- cluster scaffolding ----

type chaosCluster struct {
	t         *testing.T
	net       *chaosNet
	signers   []*signing.Signer
	keys      []string
	nodes     []*Node
	checker   *chaosChecker
	supplyCap *big.Rat
}

// newChaosCluster builds n validator nodes on a chaosNet. With a nil threshold the validator set is
// FIXED (power 1 each); with a threshold it is DYNAMIC, derived from the staked genesis accounts
// (100 each, self-delegated). extraGenesis appends non-validator accounts; tweak adjusts a node's
// config before construction.
func newChaosCluster(t *testing.T, n int, seed int64, threshold *big.Rat, extraGenesis []ledger.Account, tweak func(i int, cfg *Config)) *chaosCluster {
	t.Helper()
	c := &chaosCluster{t: t, net: newChaosNet(seed)}
	vals := make([]consensus.Validator, n)
	for i := 0; i < n; i++ {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		c.signers = append(c.signers, s)
		c.keys = append(c.keys, s.PublicKeyBase64())
		vals[i] = consensus.Validator{PublicKey: c.keys[i], Power: 1}
	}
	var vs *consensus.ValidatorSet
	if threshold == nil {
		vs = consensus.NewValidatorSet(vals)
	}
	var genesis []ledger.Account
	for i := 0; i < n; i++ {
		genesis = append(genesis, ledger.Account{PublicKey: c.keys[i], Amount: rat("100"), ValidatorNode: c.keys[i]})
	}
	genesis = append(genesis, extraGenesis...)
	for i := 0; i < n; i++ {
		cfg := Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: c.signers[i],
			Validators: vs, ValidatorThreshold: threshold,
			TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond,
			Genesis: genesis,
		}
		if tweak != nil {
			tweak(i, &cfg)
		}
		nd := New(cfg, c.net)
		c.nodes = append(c.nodes, nd)
		c.net.registerNode(c.keys[i], nd)
	}
	supply := new(big.Rat)
	for _, a := range genesis {
		supply.Add(supply, a.Amount)
	}
	c.supplyCap = supply
	return c
}

// start launches the invariant checker over the FINAL node set (so a test may swap a node between
// construction and start without racing the checker) and then starts every node.
func (c *chaosCluster) start() {
	c.checker = watchInvariants(c.nodes, c.supplyCap)
	for _, nd := range c.nodes {
		nd.Start()
	}
}

// finish stops the checker (reporting any invariant violation) and then the nodes.
func (c *chaosCluster) finish() {
	c.checker.assertClean(c.t)
	for _, nd := range c.nodes {
		nd.Stop()
	}
}

func (c *chaosCluster) heights() []int64 {
	out := make([]int64, len(c.nodes))
	for i, nd := range c.nodes {
		out[i] = nd.Snapshot().Height
	}
	return out
}

func (c *chaosCluster) maxHeight() int64 {
	max := int64(0)
	for _, h := range c.heights() {
		if h > max {
			max = h
		}
	}
	return max
}

// ---- scenarios ----

// Sustained loss + duplication + reorder on every link: the network must keep committing, stay
// fork-free, and conserve supply while real payments flow.
func TestChaosLossDupReorderKeepsAgreementAndProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	c := newChaosCluster(t, 4, chaosSeed(t), nil,
		[]ledger.Account{{PublicKey: alice.PublicKeyBase64(), Amount: rat("1000"), ValidatorNode: alice.PublicKeyBase64()}}, nil)
	c.net.setFaults(0.12, 0.10, 60*time.Millisecond)
	c.start()
	defer c.finish()

	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= 2 }, "network never started under chaos")

	// Payment traffic under fire: a fresh payment every 150ms, each naming a near-future block.
	// Refusals (overdraw guard while earlier ones are pending) are fine — traffic, not bookkeeping.
	stopTraffic := make(chan struct{})
	go func() {
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopTraffic:
				return
			case <-ticker.C:
				h := c.nodes[0].Snapshot().Height
				pay, err := ledger.SignPayment(alice, ledger.Payment{
					PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "merchant",
					Amount: rat("1"), BlockID: h + 3,
				})
				if err == nil {
					_ = c.nodes[0].AdmitPayment(pay)
				}
			}
		}
	}()

	start := c.maxHeight()
	time.Sleep(4 * time.Second)
	close(stopTraffic)

	// A generous floor: the point is that sustained loss/dup/reorder SLOWS but does not stop
	// consensus (growing-timeout round retries add real variance), not a precise throughput number.
	if got := c.maxHeight(); got < start+6 {
		t.Fatalf("under 12%% loss+dup+reorder the network should still commit steadily: went %d -> %d", start, got)
	}
	// The merchant must have received at least one payment (traffic really flowed through chaos).
	waitFor(t, 4*time.Second, func() bool {
		return balance(c.nodes[0].Snapshot().Accounts, "merchant").Sign() > 0
	}, "no payment landed despite 3s of traffic under chaos")
}

// dropFirstProposalSend is a Transport wrapper that swallows the FIRST transmission of every
// distinct proposal, network-wide (shared by all nodes). Votes and everything else pass through.
// It models the worst case of proposal loss: no round's proposal ever arrives on first send, so
// without proposal re-gossip the network could never commit anything.
type dropFirstProposalSend struct {
	inner Transport
	mu    sync.Mutex
	seen  map[string]bool
}

func (d *dropFirstProposalSend) Broadcast(from string, msg Message) {
	if msg.Type == MsgProposal {
		k := msgKey(msg)
		d.mu.Lock()
		first := !d.seen[k]
		d.seen[k] = true
		d.mu.Unlock()
		if first {
			return
		}
	}
	d.inner.Broadcast(from, msg)
}

// A proposal whose broadcast is lost used to burn its whole round (honest nodes time out to nil);
// the proposer now re-sends the held proposal on the re-gossip cadence, so the SAME round still
// decides. With every proposal's first transmission dropped, any commit at all proves the recovery
// path — remove reGossipProposal and this test stalls at height 1 forever.
func TestChaosLostProposalRecoversViaReGossip(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	net := newChaosNet(chaosSeed(t))
	lossy := &dropFirstProposalSend{inner: net, seen: map[string]bool{}}

	var signers []*signing.Signer
	var keys []string
	vals := make([]consensus.Validator, 4)
	for i := 0; i < 4; i++ {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		signers = append(signers, s)
		keys = append(keys, s.PublicKeyBase64())
		vals[i] = consensus.Validator{PublicKey: keys[i], Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	var genesis []ledger.Account
	supply := new(big.Rat)
	for i := 0; i < 4; i++ {
		genesis = append(genesis, ledger.Account{PublicKey: keys[i], Amount: rat("100"), ValidatorNode: keys[i]})
		supply.Add(supply, rat("100"))
	}
	var nodes []*Node
	for i := 0; i < 4; i++ {
		nd := New(Config{
			NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond,
			Genesis: genesis,
		}, lossy)
		nodes = append(nodes, nd)
		net.registerNode(keys[i], nd)
	}
	checker := watchInvariants(nodes, supply)
	for _, nd := range nodes {
		nd.Start()
	}
	defer func() {
		checker.assertClean(t)
		for _, nd := range nodes {
			nd.Stop()
		}
	}()

	waitFor(t, 20*time.Second, func() bool {
		for _, nd := range nodes {
			if nd.Snapshot().Height >= 4 {
				return true
			}
		}
		return false
	}, "with every proposal's first send lost, only proposal re-gossip can advance the network — it did not")
}

// A 3|1 partition — the realistic "a laptop drops off the network" case. The majority of three
// still holds quorum (3 of 4), so it must keep committing and must never fork; the isolated node,
// below quorum, must make no independent progress (no minority fork). The isolated node's RECOVERY
// path — verified-checkpoint catch-up + rejoin — is exercised separately in the gossip-isolated
// test, because a below-quorum node cannot self-heal from gossip votes alone (they are for a height
// it hasn't reached).
// (The even 2|2 split — where both halves freeze below quorum-any — is exercised separately in
// TestChaos2x2PartitionFreezesThenRecovers, which asserts the vote-re-gossip recovery.)
func TestChaosMinorityPartitionCannotProgressOrFork(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	c := newChaosCluster(t, 4, chaosSeed(t), nil, nil, nil)
	// Delay/reorder only: removing one node leaves the quorum at 3-of-4 with zero slack (all three
	// survivors must agree every block). Vote re-gossip lets the survivors tolerate lost VOTES, but a
	// lost PROPOSAL still fails that round (only votes are re-gossiped, not blocks), so a reduced
	// quorum under loss is merely slower, not reliably brisk — the loss/dup/reorder robustness with
	// margin is asserted with all four nodes present, above. Here we isolate the partition variable.
	c.net.setFaults(0, 0, 30*time.Millisecond)
	c.start()
	defer c.finish()

	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= 3 }, "network never started")

	// Isolate node 3; nodes 0/1/2 stay together (group 0, the default).
	c.net.partition(map[string]int{c.keys[3]: 1})
	atSplit := c.nodes[3].Snapshot().Height
	majAtSplit := c.nodes[0].Snapshot().Height

	// The majority of three keeps committing.
	waitFor(t, 12*time.Second, func() bool {
		lo := c.nodes[0].Snapshot().Height
		for _, i := range []int{1, 2} {
			if h := c.nodes[i].Snapshot().Height; h < lo {
				lo = h
			}
		}
		return lo >= majAtSplit+4
	}, "the majority-of-three did not keep committing while a minority was partitioned off")

	// The isolated node, below quorum, cannot commit anything new on its own.
	if got := c.nodes[3].Snapshot().Height; got > atSplit+1 {
		t.Fatalf("an isolated below-quorum node must not make independent progress: height went %d -> %d", atSplit, got)
	}

	// Heal and confirm the majority is still live and consistent (recovery of the straggler itself is
	// the checkpoint test's job). The fork/supply checkers cover the whole run.
	c.net.heal()
	resumeFrom := c.nodes[0].Snapshot().Height
	waitFor(t, 12*time.Second, func() bool { return c.nodes[0].Snapshot().Height >= resumeFrom+3 }, "the majority did not keep committing after heal")
}

// The even 2|2 split: both halves hold exactly half the power, below quorum-any, so neither can
// commit AND neither arms the round-advance timeout — both freeze at the height in progress. Because
// each side broadcast its round's votes only within its own half, the frozen votes are disjoint. This
// scenario asserts the vote-re-gossip fix (reGossipLoop): once the partition heals, the two sides
// re-exchange their held votes, reach quorum-any, advance the round, and commit — the network
// self-heals instead of staying wedged. Safety (no fork) holds throughout, split and merged.
func TestChaos2x2PartitionFreezesThenRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	c := newChaosCluster(t, 4, chaosSeed(t), nil, nil, nil)
	c.net.setFaults(0, 0, 20*time.Millisecond) // isolate the partition variable; re-gossip does the healing
	c.start()
	defer c.finish()

	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= 3 }, "network never started")
	c.net.partition(map[string]int{c.keys[0]: 0, c.keys[1]: 0, c.keys[2]: 1, c.keys[3]: 1})
	atSplit := c.maxHeight()

	// Both halves must freeze: with 2-of-4 power on each side, no side reaches quorum-any.
	time.Sleep(1500 * time.Millisecond)
	if got := c.maxHeight(); got > atSplit+1 {
		t.Fatalf("a 2|2 split must freeze (no side has quorum): height went %d -> %d", atSplit, got)
	}

	// Heal: vote re-gossip must re-exchange the frozen votes and unwedge every node.
	c.net.heal()
	waitFor(t, 20*time.Second, func() bool {
		for _, h := range c.heights() {
			if h < atSplit+2 {
				return false
			}
		}
		return true
	}, "the 2|2 split did not self-heal after merge — vote re-gossip should have unwedged it")
}

// A node cut off from GOSSIP (but able to reach one peer's HTTP checkpoint endpoint) must keep
// tracking the network through verified checkpoints — catch-up under chaos — and rejoin fully
// once the partition heals. The fork checker also covers every adopted checkpoint.
func TestChaosGossipIsolatedNodeTracksViaCheckpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	var srv *httptest.Server // node 0's HTTP API, the straggler's bootstrap source
	c := newChaosCluster(t, 4, chaosSeed(t), nil, nil, nil)
	srv = httptest.NewServer(NewServer(c.nodes[0]).Handler())
	defer srv.Close()

	// Rebuild node 3 with the bootstrap source configured (config is set at construction).
	straggler := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: c.signers[3],
		Validators: consensus.NewValidatorSet([]consensus.Validator{
			{PublicKey: c.keys[0], Power: 1}, {PublicKey: c.keys[1], Power: 1},
			{PublicKey: c.keys[2], Power: 1}, {PublicKey: c.keys[3], Power: 1},
		}),
		TransactionCost: rat("0.1"), Timeout: 400 * time.Millisecond, MinBlockInterval: 40 * time.Millisecond,
		Genesis:   c.nodes[3].cfg.Genesis,
		Bootstrap: []string{srv.URL},
	}, c.net)
	c.nodes[3] = straggler
	c.net.registerNode(c.keys[3], straggler)

	// Delay/dup/reorder but no loss: with the straggler gossip-isolated, the surviving quorum is
	// 3-of-4 with no slack, so it needs clean delivery to keep producing the checkpoints the
	// straggler catches up to (see the minority-partition note on why loss + reduced quorum stalls).
	c.net.setFaults(0, 0.05, 40*time.Millisecond)
	// Gossip-isolate the straggler from the start; its HTTP catch-up path stays clear.
	c.net.partition(map[string]int{c.keys[3]: 1})
	c.start()
	defer c.finish()

	// The other three (quorum 3 of 4) progress; the straggler must track via checkpoint sync alone.
	waitFor(t, 8*time.Second, func() bool { return c.nodes[0].Snapshot().Height >= 6 }, "majority never progressed")
	waitFor(t, 10*time.Second, func() bool {
		return c.nodes[3].Snapshot().LastCommit.Height >= 3
	}, "gossip-isolated node never adopted a verified checkpoint (catch-up under chaos)")

	c.net.heal()
	rejoinFrom := c.nodes[0].Snapshot().Height
	waitFor(t, 12*time.Second, func() bool {
		s := c.nodes[3].Snapshot()
		return s.Height >= rejoinFrom+2
	}, "straggler did not rejoin live consensus after the partition healed")
}

// A byzantine validator — real stake, real key — equivocates (conflicting prevotes AND precommits
// to everyone) and front-runs rounds with garbage proposals, all under message chaos. The honest
// majority must: never fork, keep committing, collect the equivocation as evidence, and slash the
// byzantine account out of existence (dynamic set: stake gone, membership gone).
func TestChaosEquivocatorIsSlashedWithoutFork(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	byz, _ := signing.Generate(signing.Ed25519{}, "testnet")
	byzKey := byz.PublicKeyBase64()
	seed := chaosSeed(t)
	c := newChaosCluster(t, 4, seed, rat("50"),
		[]ledger.Account{{PublicKey: byzKey, Amount: rat("100"), ValidatorNode: byzKey}}, nil)

	// The byzantine agent: a chaosNet participant with the key but its own rules. On each height's
	// first observed proposal it (a) double-signs prevotes and precommits for the real digest AND a
	// fabricated one, and (b) broadcasts a garbage signed proposal (exercising the proposer
	// authentication and the invalid-block nil-prevote path at rounds where it IS the proposer).
	var byzMu sync.Mutex
	attacked := map[int64]bool{}
	byzRng := rand.New(rand.NewSource(seed + 1))
	c.net.register(byzKey, func(m Message) {
		if m.Type != MsgProposal {
			return
		}
		h := m.Proposal.Height
		byzMu.Lock()
		if attacked[h] {
			byzMu.Unlock()
			return
		}
		attacked[h] = true
		var fake [32]byte
		byzRng.Read(fake[:])
		byzMu.Unlock()

		real := m.Proposal.Block.StateDigest
		for r := int32(0); r < 2; r++ {
			for _, typ := range []consensus.VoteType{consensus.Prevote, consensus.Precommit} {
				for _, d := range [][32]byte{real, fake} {
					v, err := consensus.SignVote(byz, consensus.Vote{Height: h, Round: r, Type: typ, BlockDigest: d, Validator: byzKey})
					if err == nil {
						c.net.Broadcast(byzKey, Message{Type: MsgVote, From: byzKey, Vote: &v})
					}
				}
			}
			garbage := ProposalMsg{Height: h, Round: r, ValidRound: -1, Block: ledger.Block{Height: h, StateDigest: fake}}
			if err := SignProposal(byz, &garbage); err == nil {
				c.net.Broadcast(byzKey, Message{Type: MsgProposal, From: byzKey, Proposal: &garbage})
			}
		}
	})

	c.net.setFaults(0.10, 0.05, 40*time.Millisecond)
	c.start()
	defer c.finish()

	// The equivocator must be slashed on every honest node: account destroyed, out of the set.
	waitFor(t, 15*time.Second, func() bool {
		for _, nd := range c.nodes {
			s := nd.Snapshot()
			if balance(s.Accounts, byzKey).Sign() != 0 {
				return false
			}
			if deriveValidators(s.Accounts, rat("50")).Contains(byzKey) {
				return false
			}
		}
		return true
	}, "the equivocating validator was not slashed out on all honest nodes")

	// And the network must still be committing afterwards.
	after := c.maxHeight()
	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= after+3 }, "network stopped committing after the slash")
}

// The governance-certificate flow under chaos, including the exact value-cycle replay the review
// found: enact A→B, then B→A (parameters identical to genesis again), then REPLAY the ratified
// A→B certificate. The governance sequence must make the replay inert on every node.
func TestChaosGovernanceCycleReplayStaysInert(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	c := newChaosCluster(t, 4, chaosSeed(t), nil, nil, nil)
	c.net.setFaults(0.10, 0.05, 40*time.Millisecond)
	c.start()
	defer c.finish()
	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= 2 }, "network never started")

	base := c.nodes[0].Snapshot().Parameters
	raised := base
	raised.TransactionCost = rat("0.25")

	// certificate: every validator signs (loss means votes may need the gossip pool to converge).
	makeCert := func(newParams ledger.LedgerParameters, prev [32]byte, seq int64) ledger.ParameterChange {
		change := ledger.ParameterChange{NewParameters: newParams, PreviousParametersDigest: prev, Sequence: seq}
		for _, s := range c.signers {
			v, err := ledger.SignParameterVote(s, newParams, prev, seq)
			if err != nil {
				t.Fatal(err)
			}
			change.Votes = append(change.Votes, v)
		}
		return change
	}
	submitEverywhere := func(change ledger.ParameterChange) {
		for _, nd := range c.nodes {
			nd.SubmitParameterVote(change)
		}
	}
	allAt := func(cost string, seq int64) func() bool {
		return func() bool {
			for _, nd := range c.nodes {
				s := nd.Snapshot()
				if s.GovernanceSequence != seq || s.Parameters.TransactionCost.Cmp(rat(cost)) != 0 {
					return false
				}
			}
			return true
		}
	}

	certAB := makeCert(raised, base.Digest(), 0)
	submitEverywhere(certAB)
	waitFor(t, 10*time.Second, allAt("0.25", 1), "A->B parameter change did not enact under chaos")

	certBA := makeCert(base, raised.Digest(), 1)
	submitEverywhere(certBA)
	waitFor(t, 10*time.Second, allAt("0.1", 2), "B->A revert did not enact under chaos")

	// The replay: parameters have cycled back to A, so the old A->B certificate's value-bound guard
	// (previous-parameters digest) MATCHES again. Only the sequence stands between this certificate
	// and a unilateral re-flip. Hammer it at every node, repeatedly.
	for i := 0; i < 3; i++ {
		submitEverywhere(certAB)
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(1 * time.Second) // several block windows for any replay to (wrongly) land
	if !allAt("0.1", 2)() {
		t.Fatal("REPLAY LANDED: the ratified A->B certificate re-applied after the parameters cycled back to A")
	}
}

// Dynamic validator admission under chaos: a payment lifts a newcomer over the stake threshold
// mid-flight; every node must converge on the same grown validator set, fork-free.
func TestChaosDynamicAdmissionConverges(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	newcomer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	// Threshold 20, base validators staked 100: an existing validator node funds the newcomer OVER
	// the threshold while itself staying comfortably above it (100 → ~79), so there is no phantom
	// validator (an in-set key with no running node stalls rounds when it is the scheduled proposer).
	// The assertion is that every node independently derives the newcomer into the set — the same
	// stake-derivation converging across chaos, fork-free (the checker covers the last part).
	const threshold = "20"
	c := newChaosCluster(t, 4, chaosSeed(t), rat(threshold), nil, nil)
	c.net.setFaults(0.10, 0.05, 40*time.Millisecond) // all four present → quorum 3-of-4 has slack
	c.start()
	defer c.finish()
	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= 2 }, "network never started")

	// Fund the newcomer over the threshold from validator 0; retry with a fresh target block if chaos
	// makes one miss (a missed payment is pruned at its block, freeing the promised outgoing again).
	deadline := time.Now().Add(15 * time.Second * testTimeSlack)
	for time.Now().Before(deadline) {
		admitted := true
		for _, nd := range c.nodes {
			if !deriveValidators(nd.Snapshot().Accounts, rat(threshold)).Contains(newcomer.PublicKeyBase64()) {
				admitted = false
				break
			}
		}
		if admitted {
			return // every node independently derived the grown set — done
		}
		h := c.nodes[0].Snapshot().Height
		pay, err := ledger.SignPayment(c.signers[0], ledger.Payment{
			PublicKeyFrom: c.keys[0], PublicKeyTo: newcomer.PublicKeyBase64(),
			Amount: rat("21"), BlockID: h + 3,
		})
		if err == nil {
			_ = c.nodes[0].AdmitPayment(pay) // refused while the previous attempt is still pending — fine
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("newcomer was not admitted as a validator on all nodes under chaos")
}

// The randomized soak: rolling fault levels, random partitions and heals, continuous payment
// traffic — with the fork and supply checkers on the whole time. Runs ~2s by default;
// GOLEDGER_CHAOS_SOAK_SECONDS=600 makes it a real soak.
func TestChaosSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	seconds := 2
	if v := os.Getenv("GOLEDGER_CHAOS_SOAK_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			seconds = n
		}
	}
	seed := chaosSeed(t)
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	c := newChaosCluster(t, 4, seed, nil,
		[]ledger.Account{{PublicKey: alice.PublicKeyBase64(), Amount: rat("10000"), ValidatorNode: alice.PublicKeyBase64()}}, nil)
	c.start()
	defer c.finish()
	waitFor(t, 8*time.Second, func() bool { return c.maxHeight() >= 2 }, "network never started")

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // rolling fault schedule + occasional partition
		defer wg.Done()
		rng := rand.New(rand.NewSource(seed + 2)) // own RNG — a *rand.Rand is not concurrency-safe
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		partitioned := false
		for {
			select {
			case <-stop:
				c.net.heal()
				return
			case <-ticker.C:
				c.net.setFaults(rng.Float64()*0.15, rng.Float64()*0.15, time.Duration(rng.Int63n(80))*time.Millisecond)
				if partitioned {
					c.net.heal()
					partitioned = false
				} else if rng.Float64() < 0.3 {
					cut := rng.Intn(4) // isolate one random node for one tick
					c.net.partition(map[string]int{c.keys[cut]: 1})
					partitioned = true
				}
			}
		}
	}()

	wg.Add(1)
	go func() { // payment traffic
		defer wg.Done()
		rng := rand.New(rand.NewSource(seed + 3)) // own RNG (see above)
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				target := c.nodes[rng.Intn(4)]
				h := target.Snapshot().Height
				pay, err := ledger.SignPayment(alice, ledger.Payment{
					PublicKeyFrom: alice.PublicKeyBase64(), PublicKeyTo: "merchant",
					Amount: rat("1"), BlockID: h + 3,
				})
				if err == nil {
					_ = target.AdmitPayment(pay)
				}
			}
		}
	}()

	start := c.maxHeight()
	time.Sleep(time.Duration(seconds) * time.Second)
	close(stop)
	wg.Wait()

	// Progress is only a SOFT floor — random partitions and heavy loss stall consensus on purpose,
	// and a reduced quorum losing a proposal fails that round — so we assert merely that the network
	// is not completely wedged (some blocks committed). The HARD assertions are the fork and supply
	// checkers running the whole time (in finish()); on a long soak they are the real payload.
	floor := int64(seconds) / 5
	if floor < 1 {
		floor = 1
	}
	if got := c.maxHeight(); got < start+floor {
		t.Fatalf("soak made almost no progress: height %d -> %d over %ds (floor +%d)", start, got, seconds, floor)
	}
}
