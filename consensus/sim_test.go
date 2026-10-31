package consensus

// The deterministic consensus simulator: the driver-level tier of the adversarial harness
// (TODO.md). The Driver is a pure state machine — timeouts are events, no goroutines, no clocks —
// so a whole validator network can run single-threaded on a virtual clock with every source of
// nondeterminism owned by one seeded RNG. A failing seed therefore REPLAYS EXACTLY, which is what
// makes this tier the place to hammer the safety argument: thousands of randomized schedules
// (message delay, reorder, loss, per-node timeout skew, an equivocating validator) with the
// agreement invariant checked on every decision.
//
// What this tier deliberately does not cover — the mempool, governance flow, catch-up and
// onboarding — lives in the node-level chaos harness (node/chaos_test.go), which runs the real
// stack under fault injection at the cost of wall-clock nondeterminism.

import (
	"container/heap"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// scaledSeedCount scales a scenario's seed count: divided in -short mode, multiplied by
// GOLEDGER_SIM_SEED_FACTOR for long soak runs (e.g. =100 overnight).
func scaledSeedCount(t *testing.T, base int64) int64 {
	if testing.Short() {
		return base/4 + 1
	}
	if factorSetting := os.Getenv("GOLEDGER_SIM_SEED_FACTOR"); factorSetting != "" {
		if factor, err := strconv.ParseInt(factorSetting, 10, 64); err == nil && factor > 0 {
			return base * factor
		}
	}
	return base
}

// ---- virtual-time event queue ----

type simulatorEvent struct {
	dueTime  int64 // virtual time
	sequence int64 // insertion order; the tie-breaker that keeps equal-time delivery deterministic
	run      func()
}

type eventQueue []simulatorEvent

func (queue eventQueue) Len() int { return len(queue) }
func (queue eventQueue) Less(i, j int) bool {
	if queue[i].dueTime != queue[j].dueTime {
		return queue[i].dueTime < queue[j].dueTime
	}
	return queue[i].sequence < queue[j].sequence
}
func (queue eventQueue) Swap(i, j int)           { queue[i], queue[j] = queue[j], queue[i] }
func (queue *eventQueue) Push(event interface{}) { *queue = append(*queue, event.(simulatorEvent)) }
func (queue *eventQueue) Pop() interface{} {
	old := *queue
	length := len(old)
	last := old[length-1]
	*queue = old[:length-1]
	return last
}

// ---- the simulator ----

type consensusSimulator struct {
	t      *testing.T
	random *rand.Rand

	now      int64
	sequence int64
	queue    eventQueue

	drivers      []*Driver
	validatorSet *ValidatorSet

	// fault model
	dropProbability float64 // per-recipient message loss
	maxDelay        int64   // per-recipient delivery delay in [1, maxDelay] (reorder falls out of this)
	timeoutBase     []int64 // per-driver step-timeout budget — DIFFERENT per driver = clock skew

	decisions map[int][32]byte // driver index -> decided digest
}

func newSimulator(t *testing.T, seed int64, validatorCount int, dropProbability float64, maxDelay int64) (*consensusSimulator, []*signing.Signer) {
	t.Helper()
	random := rand.New(rand.NewSource(seed))
	signers, validatorSet := makeValidators(t, validatorCount)
	simulator := &consensusSimulator{
		t: t, random: random, validatorSet: validatorSet,
		dropProbability: dropProbability, maxDelay: maxDelay,
		decisions: map[int][32]byte{},
	}
	for i := 0; i < validatorCount; i++ {
		// Every driver proposes a DIFFERENT candidate digest, so agreement is meaningful (the
		// network must converge on one proposer's value, not trivially share a single input).
		simulator.drivers = append(simulator.drivers, NewDriver(signers[i], nameOf(signers[i]), validatorSet, scheme, network, 1, digest(byte(i+1))))
		// Clock skew: each node's timers run on its own budget.
		simulator.timeoutBase = append(simulator.timeoutBase, 80+random.Int63n(60))
	}
	return simulator, signers
}

func (simulator *consensusSimulator) schedule(delay int64, run func()) {
	simulator.sequence++
	heap.Push(&simulator.queue, simulatorEvent{dueTime: simulator.now + delay, sequence: simulator.sequence, run: run})
}

func (simulator *consensusSimulator) linkDelay() int64 {
	return 1 + simulator.random.Int63n(simulator.maxDelay)
}
func (simulator *consensusSimulator) dropped() bool {
	return simulator.dropProbability > 0 && simulator.random.Float64() < simulator.dropProbability
}

// dispatch executes a driver's actions, scheduling the network effects on the virtual clock.
func (simulator *consensusSimulator) dispatch(i int, actions []Action) {
	for _, item := range actions {
		switch action := item.(type) {
		case ActionProposal:
			// Only the round's scheduled proposer ever emits this (the node layer's authentication
			// is exercised in the node-level harness); deliver to each peer with loss and delay.
			proposal := Proposal{Round: action.Round, BlockHash: action.BlockHash, ValidRound: action.ValidRound}
			for j := range simulator.drivers {
				if j == i || simulator.dropped() {
					continue
				}
				j, proposal := j, proposal
				simulator.schedule(simulator.linkDelay(), func() { simulator.dispatch(j, simulator.drivers[j].ReceiveProposal(proposal)) })
			}
		case ActionBroadcastVote:
			vote := action.Vote
			for j := range simulator.drivers {
				if j == i || simulator.dropped() {
					continue
				}
				j, vote := j, vote
				simulator.schedule(simulator.linkDelay(), func() { simulator.dispatch(j, simulator.drivers[j].ReceiveVote(vote)) })
			}
		case ActionSchedule:
			step, round := action.Step, action.Round
			i := i
			simulator.schedule(simulator.timeoutBase[i]*int64(round+1), func() { simulator.dispatch(i, simulator.drivers[i].Timeout(step, round)) })
		case ActionDecided:
			// THE invariant, checked on the spot: the certificate must verify (round-scoped, ⅔),
			// and no two drivers may ever decide different digests.
			if !action.Commit.Verify(scheme, network, simulator.validatorSet) {
				simulator.t.Fatalf("driver %d emitted a commit certificate that does not verify", i)
			}
			simulator.decisions[i] = action.Commit.BlockHash
			for j, decidedDigest := range simulator.decisions {
				if decidedDigest != action.Commit.BlockHash {
					simulator.t.Fatalf("FORK: driver %d decided %x, driver %d decided %x", i, action.Commit.BlockHash[:4], j, decidedDigest[:4])
				}
			}
		}
	}
}

// runUntilDecided pumps the queue until everyone decided, the queue drained, or the event budget
// ran out. Returns how many drivers decided.
func (simulator *consensusSimulator) runUntilDecided(maxEvents int) int {
	for events := 0; len(simulator.queue) > 0 && events < maxEvents; events++ {
		if len(simulator.decisions) == len(simulator.drivers) {
			break
		}
		event := heap.Pop(&simulator.queue).(simulatorEvent)
		simulator.now = event.dueTime
		event.run()
	}
	return len(simulator.decisions)
}

func (simulator *consensusSimulator) startAll() {
	for i := range simulator.drivers {
		i := i
		simulator.schedule(1, func() { simulator.dispatch(i, simulator.drivers[i].Start()) })
	}
}

// ---- scenarios ----

// Randomized delay, reorder and clock skew, no loss: every schedule must terminate with ALL
// drivers deciding the SAME digest. Each seed is a fully deterministic replay.
func TestSimAgreementAndLivenessUnderDelayAndSkew(t *testing.T) {
	for seed := int64(0); seed < scaledSeedCount(t, 60); seed++ {
		simulator, _ := newSimulator(t, seed, 4, 0, 40)
		simulator.startAll()
		decided := simulator.runUntilDecided(200_000)
		if decided != 4 {
			t.Fatalf("seed %d: only %d/4 drivers decided (liveness failure under delay/reorder/skew)", seed, decided)
		}
	}
}

// 20%% per-link message loss on top: liveness is not guaranteed (a vote is never retransmitted
// within a round — round advancement is the retry), but agreement must hold among WHOEVER decides,
// on every seed. The fork check runs inside dispatch on each decision.
func TestSimAgreementUnderMessageLoss(t *testing.T) {
	decidedTotal := 0
	for seed := int64(0); seed < scaledSeedCount(t, 40); seed++ {
		simulator, _ := newSimulator(t, seed, 4, 0.20, 40)
		simulator.startAll()
		decidedTotal += simulator.runUntilDecided(150_000)
	}
	if decidedTotal == 0 {
		t.Fatal("no driver decided across any seed — the loss model is too hostile to exercise agreement at all")
	}
}

// An equivocating validator (a full member of the set) sprays conflicting prevotes AND precommits
// for the two leading candidate digests, at several rounds, to every node. With 4 honest + 1
// byzantine (quorum 4 of 5) two conflicting quorums would need an honest double-signer, so
// agreement must survive; the equivocation must also surface as evidence that freezes its name.
func TestSimAgreementWithEquivocatingValidator(t *testing.T) {
	for seed := int64(0); seed < scaledSeedCount(t, 25); seed++ {
		random := rand.New(rand.NewSource(seed))
		signers, validatorSet := makeValidators(t, 5) // signers[4] is the byzantine one
		simulator := &consensusSimulator{
			t: t, random: random, validatorSet: validatorSet,
			dropProbability: 0, maxDelay: 40,
			decisions: map[int][32]byte{},
		}
		for i := 0; i < 4; i++ { // only the 4 honest validators run drivers
			simulator.drivers = append(simulator.drivers, NewDriver(signers[i], nameOf(signers[i]), validatorSet, scheme, network, 1, digest(byte(i+1))))
			simulator.timeoutBase = append(simulator.timeoutBase, 80+random.Int63n(60))
		}
		byzantine := signers[4]
		byzantineName := nameOf(byzantine)

		inject := func(round int32, voteType VoteType, blockDigest [32]byte, when int64) {
			vote := Vote{Height: 1, Round: round, Type: voteType, BlockHash: blockDigest, Validator: byzantineName}
			signed, err := SignVote(byzantine, vote)
			if err != nil {
				t.Fatal(err)
			}
			for j := range simulator.drivers {
				j := j
				simulator.schedule(when+simulator.random.Int63n(30), func() { simulator.dispatch(j, simulator.drivers[j].ReceiveVote(signed)) })
			}
		}
		for wave := int64(0); wave < 8; wave++ {
			waveTime := 2 + wave*25
			for round := int32(0); round < 3; round++ {
				for _, voteType := range []VoteType{Prevote, Precommit} {
					inject(round, voteType, digest(1), waveTime) // one story to the network...
					inject(round, voteType, digest(2), waveTime) // ...and its contradiction
				}
			}
		}

		simulator.startAll()
		if decided := simulator.runUntilDecided(200_000); decided != 4 {
			t.Fatalf("seed %d: only %d/4 honest drivers decided with a byzantine validator present", seed, decided)
		}
		evidenceSeen := false
		for _, driver := range simulator.drivers {
			for _, evidence := range driver.Evidence() {
				if evidence.Name == byzantineName && evidence.Valid(scheme, network, byzantine.PublicKeyBase64()) {
					evidenceSeen = true
				}
			}
		}
		if !evidenceSeen {
			t.Fatalf("seed %d: the equivocator produced no valid evidence at any honest driver", seed)
		}
	}
}
