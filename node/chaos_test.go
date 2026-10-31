package node

// The node-level tier of the adversarial harness (TODO.md): the REAL stack — event loop, mempool,
// governance pools, freezing double-signers, snapshot/catch-up — run under fault injection: message loss,
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

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

func chaosSeed(t *testing.T) int64 {
	t.Helper()
	if environmentValue := os.Getenv("GOLEDGER_CHAOS_SEED"); environmentValue != "" {
		if environmentSeed, err := strconv.ParseInt(environmentValue, 10, 64); err == nil {
			t.Logf("chaos seed %d (from GOLEDGER_CHAOS_SEED)", environmentSeed)
			return environmentSeed
		}
	}
	seed := time.Now().UnixNano()
	t.Logf("chaos seed %d (set GOLEDGER_CHAOS_SEED=%d to replay this fault schedule)", seed, seed)
	return seed
}

// ---- the chaos transport ----

// chaosNetwork is an in-memory Transport that injects faults between registered participants: loss,
// duplication, random delay (which is reorder, since deliveries race), and partitions. Participants
// are handler funcs, so byzantine agents (a name with custom behavior, no Node) plug in beside real
// nodes. All randomness comes from one seeded RNG (mutex-guarded — Broadcast is called from many
// goroutines), so the fault schedule is reproducible even though goroutine interleaving is not.
type chaosNetwork struct { // implements Transport
	mutex                sync.Mutex
	random               *rand.Rand
	handlers             map[string]func(Message)
	group                map[string]int // partition group per name; different group = no delivery. Empty = healed.
	dropProbability      float64
	duplicateProbability float64
	maxDelay             time.Duration
}

func newChaosNetwork(seed int64) *chaosNetwork {
	return &chaosNetwork{
		random:   rand.New(rand.NewSource(seed)),
		handlers: map[string]func(Message){},
		group:    map[string]int{},
	}
}

func (network *chaosNetwork) register(name string, handler func(Message)) {
	network.mutex.Lock()
	network.handlers[name] = handler
	network.mutex.Unlock()
}

func (network *chaosNetwork) registerNode(name string, node *Node) {
	network.register(name, func(message Message) { deliverToNode(node, message) })
}

func (network *chaosNetwork) setFaults(drop, duplicate float64, maxDelay time.Duration) {
	network.mutex.Lock()
	network.dropProbability, network.duplicateProbability, network.maxDelay = drop, duplicate, maxDelay
	network.mutex.Unlock()
}

func (network *chaosNetwork) partition(groups map[string]int) {
	network.mutex.Lock()
	network.group = groups
	network.mutex.Unlock()
}

func (network *chaosNetwork) heal() { network.partition(map[string]int{}) }

func (network *chaosNetwork) Broadcast(from string, message Message) {
	type delivery struct {
		handler func(Message)
		delay   time.Duration
	}
	var deliveries []delivery
	network.mutex.Lock()
	fromGroup := network.group[from]
	for name, handler := range network.handlers {
		if name == from || network.group[name] != fromGroup {
			continue
		}
		if network.random.Float64() < network.dropProbability {
			continue
		}
		copies := 1
		if network.random.Float64() < network.duplicateProbability {
			copies = 2
		}
		for i := 0; i < copies; i++ {
			var delay time.Duration
			if network.maxDelay > 0 {
				delay = time.Duration(network.random.Int63n(int64(network.maxDelay)))
			}
			deliveries = append(deliveries, delivery{handler: handler, delay: delay})
		}
	}
	network.mutex.Unlock()
	for _, pending := range deliveries {
		pending := pending
		time.AfterFunc(pending.delay, func() { pending.handler(message) })
	}
}

func deliverToNode(node *Node, message Message) {
	switch message.Type {
	case MessageProposal:
		node.SubmitProposal(*message.Proposal)
	case MessageVote:
		node.SubmitVote(*message.Vote)
	case MessagePayment:
		node.SubmitPayment(*message.Payment)
	case MessageUpdate:
		node.SubmitUpdate(*message.Update)
	case MessageParameterCertificate:
		node.AdmitParameterCertificate(*message.ParameterChange)
	case MessageReassignmentCertificate:
		node.AdmitReassignmentCertificate(*message.Reassignment)
	}
}

// ---- the checkers ----

// chaosChecker polls every node's snapshot and holds the two Jepsen-style invariants across the
// whole run: (1) agreement — a committed height has exactly one digest network-wide; (2) supply —
// the total never exceeds the cap (genesis supply, plus any explicit ⅔ mint the scenario performs).
type chaosChecker struct {
	mutex      sync.Mutex
	commits    map[int64][32]byte
	violations []string
	supplyCap  *big.Rat
	stop       chan struct{}
	done       chan struct{}
}

func watchInvariants(nodes []*Node, supplyCap *big.Rat) *chaosChecker {
	checker := &chaosChecker{
		commits:   map[int64][32]byte{},
		supplyCap: new(big.Rat).Set(supplyCap),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go func() {
		defer close(checker.done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-checker.stop:
				return
			case <-ticker.C:
				for i, node := range nodes {
					snapshot := node.Snapshot()
					if snapshot.LastCommit.Height <= 0 {
						continue
					}
					checker.mutex.Lock()
					if seen, recorded := checker.commits[snapshot.LastCommit.Height]; recorded {
						if seen != snapshot.LastCommit.BlockHash {
							checker.violations = append(checker.violations, "FORK at height "+strconv.FormatInt(snapshot.LastCommit.Height, 10)+" observed via node "+strconv.Itoa(i))
						}
					} else {
						checker.commits[snapshot.LastCommit.Height] = snapshot.LastCommit.BlockHash
					}
					supply := new(big.Rat)
					for _, account := range snapshot.Accounts {
						if account.Amount != nil {
							supply.Add(supply, account.Amount)
						}
					}
					if supply.Cmp(checker.supplyCap) > 0 {
						checker.violations = append(checker.violations, "SUPPLY "+supply.RatString()+" exceeds cap "+checker.supplyCap.RatString()+" on node "+strconv.Itoa(i))
					}
					checker.mutex.Unlock()
				}
			}
		}
	}()
	return checker
}

// raiseCap widens the supply cap when a scenario performs an explicit, legitimate ⅔ mint.
func (checker *chaosChecker) raiseCap(by *big.Rat) {
	checker.mutex.Lock()
	checker.supplyCap.Add(checker.supplyCap, by)
	checker.mutex.Unlock()
}

func (checker *chaosChecker) assertClean(t *testing.T) {
	t.Helper()
	close(checker.stop)
	<-checker.done
	checker.mutex.Lock()
	defer checker.mutex.Unlock()
	for _, violation := range checker.violations {
		t.Error(violation)
	}
}

// ---- cluster scaffolding ----

type chaosCluster struct {
	t         *testing.T
	network   *chaosNetwork
	names     []string // the validators' names
	chain     ledger.Chain
	nodes     []*Node
	checker   *chaosChecker
	supplyCap *big.Rat
}

// newChaosCluster builds validatorCount validator nodes on a chaosNetwork. With an empty threshold the
// validator set is FIXED (power 1 each); with a threshold it is DYNAMIC, derived from the staked
// genesis accounts (100 each, self-delegated). extraGenesis appends non-validator accounts; tweak
// adjusts a node's config before construction.
func newChaosCluster(t *testing.T, validatorCount int, seed int64, threshold string, extraGenesis []ledger.Account, tweak func(i int, config *Config)) *chaosCluster {
	t.Helper()
	cluster := &chaosCluster{t: t, network: newChaosNetwork(seed), names: validatorNames(validatorCount)}
	cluster.chain = staticChain(cluster.names...)
	if threshold != "" {
		cluster.chain = dynamicChain(threshold)
	}
	var genesis []ledger.Account
	for _, name := range cluster.names {
		genesis = append(genesis, selfDelegated(name, "100"))
	}
	genesis = append(genesis, extraGenesis...)
	for i, name := range cluster.names {
		config := testConfig(cluster.chain, name, testGenesis(genesis, 400*time.Millisecond, 40*time.Millisecond))
		if tweak != nil {
			tweak(i, &config)
		}
		node := New(config, cluster.network)
		cluster.nodes = append(cluster.nodes, node)
		cluster.network.registerNode(name, node)
	}
	supply := new(big.Rat)
	for _, account := range genesis {
		supply.Add(supply, account.Amount)
	}
	cluster.supplyCap = supply
	return cluster
}

// start launches the invariant checker over the FINAL node set (so a test may swap a node between
// construction and start without racing the checker) and then starts every node.
func (cluster *chaosCluster) start() {
	cluster.checker = watchInvariants(cluster.nodes, cluster.supplyCap)
	for _, node := range cluster.nodes {
		node.Start()
	}
}

// finish stops the checker (reporting any invariant violation) and then the nodes.
func (cluster *chaosCluster) finish() {
	cluster.checker.assertClean(cluster.t)
	for _, node := range cluster.nodes {
		node.Stop()
	}
}

func (cluster *chaosCluster) heights() []int64 {
	nodeHeights := make([]int64, len(cluster.nodes))
	for i, node := range cluster.nodes {
		nodeHeights[i] = node.Snapshot().Height
	}
	return nodeHeights
}

func (cluster *chaosCluster) maxHeight() int64 {
	highest := int64(0)
	for _, height := range cluster.heights() {
		if height > highest {
			highest = height
		}
	}
	return highest
}

// ---- scenarios ----

// Sustained loss + duplication + reorder on every link: the network must keep committing, stay
// fork-free, and conserve supply while real payments flow.
func TestChaosLossDupReorderKeepsAgreementAndProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	cluster := newChaosCluster(t, 4, chaosSeed(t), "", []ledger.Account{testAccount("alice", "1000"), testAccount("merchant", "0")}, nil)
	cluster.network.setFaults(0.12, 0.10, 60*time.Millisecond)
	cluster.start()
	defer cluster.finish()

	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= 2 }, "network never started under chaos")

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
				height := cluster.nodes[0].Snapshot().Height
				_, _ = cluster.nodes[0].AdmitPayment(signedPayment("alice", "merchant", "1", height+3))
			}
		}
	}()

	start := cluster.maxHeight()
	time.Sleep(4 * time.Second)
	close(stopTraffic)

	// A generous floor: the point is that sustained loss/dup/reorder SLOWS but does not stop
	// consensus (growing-timeout round retries add real variance), not a precise throughput number.
	if got := cluster.maxHeight(); got < start+6 {
		t.Fatalf("under 12%% loss+dup+reorder the network should still commit steadily: went %d -> %d", start, got)
	}
	// The merchant must have received at least one payment (traffic really flowed through chaos).
	waitFor(t, 4*time.Second, func() bool {
		return balance(cluster.nodes[0].Snapshot().Accounts, "merchant").Sign() > 0
	}, "no payment landed despite 3s of traffic under chaos")
}

// dropFirstProposalSend is a Transport wrapper that swallows the FIRST transmission of every
// distinct proposal, network-wide (shared by all nodes). Votes and everything else pass through.
// It models the worst case of proposal loss: no round's proposal ever arrives on first send, so
// without proposal re-gossip the network could never commit anything.
type dropFirstProposalSend struct { // implements Transport
	inner Transport
	mutex sync.Mutex
	seen  map[string]bool
}

func (transport *dropFirstProposalSend) Broadcast(from string, message Message) {
	if message.Type == MessageProposal {
		key := messageKey(message)
		transport.mutex.Lock()
		first := !transport.seen[key]
		transport.seen[key] = true
		transport.mutex.Unlock()
		if first {
			return
		}
	}
	transport.inner.Broadcast(from, message)
}

// A proposal whose broadcast is lost used to burn its whole round (honest nodes time out to nil);
// the proposer now re-sends the held proposal on the re-gossip cadence, so the SAME round still
// decides. With every proposal's first transmission dropped, any commit at all proves the recovery
// path — remove reGossipProposal and this test stalls at height 1 forever.
func TestChaosLostProposalRecoversViaReGossip(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	network := newChaosNetwork(chaosSeed(t))
	lossy := &dropFirstProposalSend{inner: network, seen: map[string]bool{}}

	names := validatorNames(4)
	var accounts []ledger.Account
	supply := new(big.Rat)
	for _, name := range names {
		accounts = append(accounts, selfDelegated(name, "100"))
		supply.Add(supply, rational("100"))
	}
	genesis := testGenesis(accounts, 400*time.Millisecond, 40*time.Millisecond)
	var nodes []*Node
	for _, name := range names {
		node := New(testConfig(staticChain(names...), name, genesis), lossy)
		nodes = append(nodes, node)
		network.registerNode(name, node)
	}
	checker := watchInvariants(nodes, supply)
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		checker.assertClean(t)
		for _, node := range nodes {
			node.Stop()
		}
	}()
	keepBusy(t, names[0], nodes...)

	waitFor(t, 20*time.Second, func() bool {
		for _, node := range nodes {
			if node.Snapshot().Height >= 4 {
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
	cluster := newChaosCluster(t, 4, chaosSeed(t), "", nil, nil)
	// Delay/reorder only: removing one node leaves the quorum at 3-of-4 with zero slack (all three
	// survivors must agree every block). Vote re-gossip lets the survivors tolerate lost VOTES, but a
	// lost PROPOSAL still fails that round (only votes are re-gossiped, not blocks), so a reduced
	// quorum under loss is merely slower, not reliably brisk — the loss/dup/reorder robustness with
	// margin is asserted with all four nodes present, above. Here we isolate the partition variable.
	cluster.network.setFaults(0, 0, 30*time.Millisecond)
	cluster.start()
	defer cluster.finish()
	keepBusy(t, cluster.names[0], cluster.nodes...)

	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= 3 }, "network never started")

	// Isolate node 3; nodes 0/1/2 stay together (group 0, the default).
	cluster.network.partition(map[string]int{cluster.names[3]: 1})
	atSplit := cluster.nodes[3].Snapshot().Height
	majorityAtSplit := cluster.nodes[0].Snapshot().Height

	// The majority of three keeps committing.
	waitFor(t, 12*time.Second, func() bool {
		lowest := cluster.nodes[0].Snapshot().Height
		for _, i := range []int{1, 2} {
			if height := cluster.nodes[i].Snapshot().Height; height < lowest {
				lowest = height
			}
		}
		return lowest >= majorityAtSplit+4
	}, "the majority-of-three did not keep committing while a minority was partitioned off")

	// The isolated node, below quorum, cannot commit anything new on its own.
	if got := cluster.nodes[3].Snapshot().Height; got > atSplit+1 {
		t.Fatalf("an isolated below-quorum node must not make independent progress: height went %d -> %d", atSplit, got)
	}

	// Heal and confirm the majority is still live and consistent (recovery of the straggler itself is
	// the checkpoint test's job). The fork/supply checkers cover the whole run.
	cluster.network.heal()
	resumeFrom := cluster.nodes[0].Snapshot().Height
	waitFor(t, 12*time.Second, func() bool { return cluster.nodes[0].Snapshot().Height >= resumeFrom+3 }, "the majority did not keep committing after heal")
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
	cluster := newChaosCluster(t, 4, chaosSeed(t), "", nil, nil)
	cluster.network.setFaults(0, 0, 20*time.Millisecond) // isolate the partition variable; re-gossip does the healing
	cluster.start()
	defer cluster.finish()
	keepBusy(t, cluster.names[0], cluster.nodes...) // busy nodes hold votes to re-gossip; idle ones would hold none

	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= 3 }, "network never started")
	cluster.network.partition(map[string]int{cluster.names[0]: 0, cluster.names[1]: 0, cluster.names[2]: 1, cluster.names[3]: 1})
	atSplit := cluster.maxHeight()

	// Both halves must freeze: with 2-of-4 power on each side, no side reaches quorum-any.
	time.Sleep(1500 * time.Millisecond)
	if got := cluster.maxHeight(); got > atSplit+1 {
		t.Fatalf("a 2|2 split must freeze (no side has quorum): height went %d -> %d", atSplit, got)
	}

	// Heal: vote re-gossip must re-exchange the frozen votes and unwedge every node.
	cluster.network.heal()
	waitFor(t, 20*time.Second, func() bool {
		for _, height := range cluster.heights() {
			if height < atSplit+2 {
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
	var bootstrapServer *httptest.Server // node 0's HTTP API, the straggler's bootstrap source
	cluster := newChaosCluster(t, 4, chaosSeed(t), "", nil, nil)
	bootstrapServer = httptest.NewServer(NewServer(cluster.nodes[0]).Handler())
	defer bootstrapServer.Close()

	// Rebuild node 3 with the bootstrap source configured (config is set at construction).
	config := testConfig(cluster.chain, cluster.names[3], cluster.nodes[3].config.Genesis)
	config.Bootstrap = []string{bootstrapServer.URL}
	straggler := New(config, cluster.network)
	cluster.nodes[3] = straggler
	cluster.network.registerNode(cluster.names[3], straggler)

	// Delay/dup/reorder but no loss: with the straggler gossip-isolated, the surviving quorum is
	// 3-of-4 with no slack, so it needs clean delivery to keep producing the checkpoints the
	// straggler catches up to (see the minority-partition note on why loss + reduced quorum stalls).
	cluster.network.setFaults(0, 0.05, 40*time.Millisecond)
	// Gossip-isolate the straggler from the start; its HTTP catch-up path stays clear.
	cluster.network.partition(map[string]int{cluster.names[3]: 1})
	cluster.start()
	defer cluster.finish()
	keepBusy(t, cluster.names[0], cluster.nodes...)

	// The other three (quorum 3 of 4) progress; the straggler must track via checkpoint sync alone.
	waitFor(t, 8*time.Second, func() bool { return cluster.nodes[0].Snapshot().Height >= 6 }, "majority never progressed")
	waitFor(t, 10*time.Second, func() bool {
		return cluster.nodes[3].Snapshot().LastCommit.Height >= 3
	}, "gossip-isolated node never adopted a verified checkpoint (catch-up under chaos)")

	keepBusy(t, cluster.names[0], cluster.nodes[:3]...) // more blocks: the catch-up took some; the straggler may be far behind
	cluster.network.heal()
	rejoinFrom := cluster.nodes[0].Snapshot().Height
	waitFor(t, 12*time.Second, func() bool {
		snapshot := cluster.nodes[3].Snapshot()
		return snapshot.Height >= rejoinFrom+2
	}, "straggler did not rejoin live consensus after the partition healed")
}

// A byzantine validator — real stake, real key — equivocates (conflicting prevotes AND precommits
// to everyone) and front-runs rounds with garbage proposals, all under message chaos. The honest
// majority must: never fork, keep committing, collect the equivocation as evidence, and freeze the
// byzantine name (dynamic set: out of the set, its balance left for the ⅔ to decide on).
func TestChaosEquivocatorIsFrozenWithoutFork(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	const byzantine = "byzantine"
	byzantineSigner := testSigner(byzantine)
	seed := chaosSeed(t)
	cluster := newChaosCluster(t, 4, seed, "50", []ledger.Account{selfDelegated(byzantine, "100")}, nil)

	// The byzantine agent: a chaosNetwork participant with the key but its own rules. On each height's
	// first observed proposal it (a) double-signs prevotes and precommits for the real digest AND a
	// fabricated one, and (b) broadcasts a garbage signed proposal (exercising the proposer
	// authentication and the invalid-block nil-prevote path at rounds where it IS the proposer).
	var byzantineMutex sync.Mutex
	attacked := map[int64]bool{}
	byzantineRandom := rand.New(rand.NewSource(seed + 1))
	cluster.network.register(byzantine, func(message Message) {
		if message.Type != MessageProposal {
			return
		}
		height := message.Proposal.Proposal.Height
		byzantineMutex.Lock()
		if attacked[height] {
			byzantineMutex.Unlock()
			return
		}
		attacked[height] = true
		var fake [32]byte
		byzantineRandom.Read(fake[:])
		byzantineMutex.Unlock()

		realDigest := message.Proposal.Proposal.BlockHash
		for round := int32(0); round < 2; round++ {
			for _, voteType := range []consensus.VoteType{consensus.Prevote, consensus.Precommit} {
				for _, digest := range [][32]byte{realDigest, fake} {
					vote, err := consensus.SignVote(byzantineSigner, consensus.Vote{Height: height, Round: round, Type: voteType, BlockHash: digest, Validator: byzantine})
					if err == nil {
						cluster.network.Broadcast(byzantine, Message{Type: MessageVote, Vote: &vote})
					}
				}
			}
			garbage := proposalOf(byzantineSigner, byzantine, height, round, ledger.Block{Height: height, StateDigest: fake})
			cluster.network.Broadcast(byzantine, Message{Type: MessageProposal, Proposal: &garbage})
		}
	})

	cluster.network.setFaults(0.10, 0.05, 40*time.Millisecond)
	cluster.start()
	defer cluster.finish()
	keepBusy(t, cluster.names[0], cluster.nodes...)

	// The equivocator's name must be frozen on every honest node, out of the set, with its balance.
	waitFor(t, 15*time.Second, func() bool {
		for _, node := range cluster.nodes {
			snapshot := node.Snapshot()
			if account, _ := ledger.FindAccount(snapshot.Accounts, byzantine); !account.Frozen || account.Amount.Cmp(rational("100")) != 0 {
				return false
			}
			if cluster.chain.ValidatorRule.SetFor(ledger.LedgerState{Accounts: snapshot.Accounts}).Contains(byzantine) {
				return false
			}
		}
		return true
	}, "the equivocating validator was not frozen, with its balance, on all honest nodes")

	// And the network must still be committing afterwards.
	after := cluster.maxHeight()
	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= after+3 }, "network stopped committing after the freeze")
}

// The governance-certificate flow under chaos, including the exact value-cycle replay the review
// found: enact A→B, then B→A (parameters identical to genesis again), then REPLAY the ratified
// A→B certificate. The governance sequence must make the replay inert on every node.
func TestChaosGovernanceCycleReplayStaysInert(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos scenarios skipped in -short")
	}
	cluster := newChaosCluster(t, 4, chaosSeed(t), "", nil, nil)
	cluster.network.setFaults(0.10, 0.05, 40*time.Millisecond)
	cluster.start()
	defer cluster.finish()
	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= 2 }, "network never started")

	base := cluster.nodes[0].Snapshot().Parameters
	raised := base
	raised.TransactionCost = rational("0.25")

	// certificate: every validator signs (loss means votes may need the gossip pool to converge).
	makeCertificate := func(newParameters ledger.LedgerParameters, previousParametersDigest [32]byte, sequence int64) ledger.ParameterChange {
		change := ledger.ParameterChange{NewParameters: newParameters, PreviousParametersDigest: previousParametersDigest, Sequence: sequence}
		for _, name := range cluster.names {
			vote, err := ledger.SignParameterVote(testSigner(name), name, newParameters, previousParametersDigest, sequence)
			if err != nil {
				t.Fatal(err)
			}
			change.Votes = append(change.Votes, vote)
		}
		return change
	}
	submitEverywhere := func(change ledger.ParameterChange) {
		for _, node := range cluster.nodes {
			node.AdmitParameterCertificate(change)
		}
	}
	allAt := func(cost string, sequence int64) func() bool {
		return func() bool {
			for _, node := range cluster.nodes {
				snapshot := node.Snapshot()
				if snapshot.GovernanceSequence != sequence || snapshot.Parameters.TransactionCost.Cmp(rational(cost)) != 0 {
					return false
				}
			}
			return true
		}
	}

	certificateAToB := makeCertificate(raised, base.Digest(), 0)
	submitEverywhere(certificateAToB)
	waitFor(t, 10*time.Second, allAt("0.25", 1), "A->B parameter change did not enact under chaos")

	certificateBToA := makeCertificate(base, raised.Digest(), 1)
	submitEverywhere(certificateBToA)
	waitFor(t, 10*time.Second, allAt("0.1", 2), "B->A revert did not enact under chaos")

	// The replay: parameters have cycled back to A, so the old A->B certificate's value-bound guard
	// (previous-parameters digest) MATCHES again. Only the sequence stands between this certificate
	// and a unilateral re-flip. Hammer it at every node, repeatedly.
	for i := 0; i < 3; i++ {
		submitEverywhere(certificateAToB)
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
	// Threshold 20, base validators staked 100: an existing validator node funds the newcomer OVER
	// the threshold while itself staying comfortably above it (100 → ~79), so there is no phantom
	// validator (an in-set key with no running node stalls rounds when it is the scheduled proposer).
	// The assertion is that every node independently derives the newcomer into the set — the same
	// stake-derivation converging across chaos, fork-free (the checker covers the last part).
	cluster := newChaosCluster(t, 4, chaosSeed(t), "20", []ledger.Account{selfDelegated("newcomer", "0")}, nil)
	cluster.network.setFaults(0.10, 0.05, 40*time.Millisecond) // all four present → quorum 3-of-4 has slack
	cluster.start()
	defer cluster.finish()
	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= 2 }, "network never started")

	// Fund the newcomer over the threshold from validator 0; retry with a fresh target block if chaos
	// makes one miss (a missed payment is pruned at its block, freeing the promised outgoing again).
	deadline := time.Now().Add(15 * time.Second * testTimeSlack)
	for time.Now().Before(deadline) {
		admitted := true
		for _, node := range cluster.nodes {
			if !cluster.chain.ValidatorRule.SetFor(ledger.LedgerState{Accounts: node.Snapshot().Accounts}).Contains("newcomer") {
				admitted = false
				break
			}
		}
		if admitted {
			return // every node independently derived the grown set — done
		}
		height := cluster.nodes[0].Snapshot().Height
		_, _ = cluster.nodes[0].AdmitPayment(signedPayment(cluster.names[0], "newcomer", "21", height+3)) // refused while the previous attempt is still pending — fine
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
	if environmentValue := os.Getenv("GOLEDGER_CHAOS_SOAK_SECONDS"); environmentValue != "" {
		if environmentSeconds, err := strconv.Atoi(environmentValue); err == nil && environmentSeconds > 0 {
			seconds = environmentSeconds
		}
	}
	seed := chaosSeed(t)
	cluster := newChaosCluster(t, 4, seed, "", []ledger.Account{testAccount("alice", "10000"), testAccount("merchant", "0")}, nil)
	cluster.start()
	defer cluster.finish()
	waitFor(t, 8*time.Second, func() bool { return cluster.maxHeight() >= 2 }, "network never started")

	stop := make(chan struct{})
	var waitGroup sync.WaitGroup

	waitGroup.Add(1)
	go func() { // rolling fault schedule + occasional partition
		defer waitGroup.Done()
		random := rand.New(rand.NewSource(seed + 2)) // own RNG — a *rand.Rand is not concurrency-safe
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		partitioned := false
		for {
			select {
			case <-stop:
				cluster.network.heal()
				return
			case <-ticker.C:
				cluster.network.setFaults(random.Float64()*0.15, random.Float64()*0.15, time.Duration(random.Int63n(80))*time.Millisecond)
				if partitioned {
					cluster.network.heal()
					partitioned = false
				} else if random.Float64() < 0.3 {
					cut := random.Intn(4) // isolate one random node for one tick
					cluster.network.partition(map[string]int{cluster.names[cut]: 1})
					partitioned = true
				}
			}
		}
	}()

	waitGroup.Add(1)
	go func() { // payment traffic
		defer waitGroup.Done()
		random := rand.New(rand.NewSource(seed + 3)) // own RNG (see above)
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				target := cluster.nodes[random.Intn(4)]
				height := target.Snapshot().Height
				_, _ = target.AdmitPayment(signedPayment("alice", "merchant", "1", height+3))
			}
		}
	}()

	start := cluster.maxHeight()
	time.Sleep(time.Duration(seconds) * time.Second)
	close(stop)
	waitGroup.Wait()

	// Progress is only a SOFT floor — random partitions and heavy loss stall consensus on purpose,
	// and a reduced quorum losing a proposal fails that round — so we assert merely that the network
	// is not completely wedged (some blocks committed). The HARD assertions are the fork and supply
	// checkers running the whole time (in finish()); on a long soak they are the real payload.
	floor := int64(seconds) / 5
	if floor < 1 {
		floor = 1
	}
	if got := cluster.maxHeight(); got < start+floor {
		t.Fatalf("soak made almost no progress: height %d -> %d over %ds (floor +%d)", start, got, seconds, floor)
	}
}
