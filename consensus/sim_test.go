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

	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// simSeeds scales a scenario's seed count: divided in -short mode, multiplied by
// GOLEDGER_SIM_SEED_FACTOR for long soak runs (e.g. =100 overnight).
func simSeeds(t *testing.T, base int64) int64 {
	if testing.Short() {
		return base/4 + 1
	}
	if v := os.Getenv("GOLEDGER_SIM_SEED_FACTOR"); v != "" {
		if f, err := strconv.ParseInt(v, 10, 64); err == nil && f > 0 {
			return base * f
		}
	}
	return base
}

// ---- virtual-time event queue ----

type simEvent struct {
	at  int64 // virtual time
	seq int64 // insertion order; the tie-breaker that keeps equal-time delivery deterministic
	run func()
}

type simQueue []simEvent

func (q simQueue) Len() int { return len(q) }
func (q simQueue) Less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	return q[i].seq < q[j].seq
}
func (q simQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *simQueue) Push(x interface{}) { *q = append(*q, x.(simEvent)) }
func (q *simQueue) Pop() interface{} {
	old := *q
	n := len(old)
	e := old[n-1]
	*q = old[:n-1]
	return e
}

// ---- the simulator ----

type sim struct {
	t   *testing.T
	rng *rand.Rand

	now   int64
	seq   int64
	queue simQueue

	drivers []*Driver
	vals    *ValidatorSet

	// fault model
	dropProb    float64 // per-recipient message loss
	maxDelay    int64   // per-recipient delivery delay in [1, maxDelay] (reorder falls out of this)
	timeoutBase []int64 // per-driver step-timeout budget — DIFFERENT per driver = clock skew

	decisions map[int][32]byte // driver index -> decided digest
}

func newSim(t *testing.T, seed int64, n int, dropProb float64, maxDelay int64) (*sim, []*signing.Signer) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	signers, vals := makeValidators(t, n)
	s := &sim{
		t: t, rng: rng, vals: vals,
		dropProb: dropProb, maxDelay: maxDelay,
		decisions: map[int][32]byte{},
	}
	for i := 0; i < n; i++ {
		// Every driver proposes a DIFFERENT candidate digest, so agreement is meaningful (the
		// network must converge on one proposer's value, not trivially share a single input).
		s.drivers = append(s.drivers, NewDriver(signers[i], vals, scheme, network, 1, digest(byte(i+1))))
		// Clock skew: each node's timers run on its own budget.
		s.timeoutBase = append(s.timeoutBase, 80+rng.Int63n(60))
	}
	return s, signers
}

func (s *sim) schedule(delay int64, run func()) {
	s.seq++
	heap.Push(&s.queue, simEvent{at: s.now + delay, seq: s.seq, run: run})
}

func (s *sim) linkDelay() int64 { return 1 + s.rng.Int63n(s.maxDelay) }
func (s *sim) dropped() bool    { return s.dropProb > 0 && s.rng.Float64() < s.dropProb }

// dispatch executes a driver's actions, scheduling the network effects on the virtual clock.
func (s *sim) dispatch(i int, acts []Action) {
	for _, a := range acts {
		switch act := a.(type) {
		case ActProposal:
			// Only the round's scheduled proposer ever emits this (the node layer's authentication
			// is exercised in the node-level harness); deliver to each peer with loss and delay.
			p := Proposal{Round: act.Round, Digest: act.Digest, ValidRound: act.ValidRound, Valid: true}
			for j := range s.drivers {
				if j == i || s.dropped() {
					continue
				}
				j, p := j, p
				s.schedule(s.linkDelay(), func() { s.dispatch(j, s.drivers[j].ReceiveProposal(p)) })
			}
		case ActBroadcastVote:
			v := act.Vote
			for j := range s.drivers {
				if j == i || s.dropped() {
					continue
				}
				j, v := j, v
				s.schedule(s.linkDelay(), func() { s.dispatch(j, s.drivers[j].ReceiveVote(v)) })
			}
		case ActSchedule:
			step, round := act.Step, act.Round
			i := i
			s.schedule(s.timeoutBase[i]*int64(round+1), func() { s.dispatch(i, s.drivers[i].Timeout(step, round)) })
		case ActDecided:
			// THE invariant, checked on the spot: the certificate must verify (round-scoped, ⅔),
			// and no two drivers may ever decide different digests.
			if !act.Commit.Verify(scheme, network, s.vals) {
				s.t.Fatalf("driver %d emitted a commit certificate that does not verify", i)
			}
			s.decisions[i] = act.Digest
			for j, d := range s.decisions {
				if d != act.Digest {
					s.t.Fatalf("FORK: driver %d decided %x, driver %d decided %x", i, act.Digest[:4], j, d[:4])
				}
			}
		}
	}
}

// run pumps the queue until everyone decided, the queue drained, or the event budget ran out.
// Returns how many drivers decided.
func (s *sim) run(maxEvents int) int {
	for events := 0; len(s.queue) > 0 && events < maxEvents; events++ {
		if len(s.decisions) == len(s.drivers) {
			break
		}
		e := heap.Pop(&s.queue).(simEvent)
		s.now = e.at
		e.run()
	}
	return len(s.decisions)
}

func (s *sim) startAll() {
	for i := range s.drivers {
		i := i
		s.schedule(1, func() { s.dispatch(i, s.drivers[i].Start()) })
	}
}

// ---- scenarios ----

// Randomized delay, reorder and clock skew, no loss: every schedule must terminate with ALL
// drivers deciding the SAME digest. Each seed is a fully deterministic replay.
func TestSimAgreementAndLivenessUnderDelayAndSkew(t *testing.T) {
	for seed := int64(0); seed < simSeeds(t, 60); seed++ {
		s, _ := newSim(t, seed, 4, 0, 40)
		s.startAll()
		decided := s.run(200_000)
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
	for seed := int64(0); seed < simSeeds(t, 40); seed++ {
		s, _ := newSim(t, seed, 4, 0.20, 40)
		s.startAll()
		decidedTotal += s.run(150_000)
	}
	if decidedTotal == 0 {
		t.Fatal("no driver decided across any seed — the loss model is too hostile to exercise agreement at all")
	}
}

// An equivocating validator (a full member of the set) sprays conflicting prevotes AND precommits
// for the two leading candidate digests, at several rounds, to every node. With 4 honest + 1
// byzantine (quorum 4 of 5) two conflicting quorums would need an honest double-signer, so
// agreement must survive; the equivocation must also surface as slashable evidence.
func TestSimAgreementWithEquivocatingValidator(t *testing.T) {
	for seed := int64(0); seed < simSeeds(t, 25); seed++ {
		rng := rand.New(rand.NewSource(seed))
		signers, vals := makeValidators(t, 5) // signers[4] is the byzantine one
		s := &sim{
			t: t, rng: rng, vals: vals,
			dropProb: 0, maxDelay: 40,
			decisions: map[int][32]byte{},
		}
		for i := 0; i < 4; i++ { // only the 4 honest validators run drivers
			s.drivers = append(s.drivers, NewDriver(signers[i], vals, scheme, network, 1, digest(byte(i+1))))
			s.timeoutBase = append(s.timeoutBase, 80+rng.Int63n(60))
		}
		byz := signers[4]
		byzKey := byz.PublicKeyBase64()

		inject := func(round int32, typ VoteType, d [32]byte, when int64) {
			v := Vote{Height: 1, Round: round, Type: typ, BlockDigest: d, Validator: byzKey}
			signed, err := SignVote(byz, v)
			if err != nil {
				t.Fatal(err)
			}
			for j := range s.drivers {
				j := j
				s.schedule(when+s.rng.Int63n(30), func() { s.dispatch(j, s.drivers[j].ReceiveVote(signed)) })
			}
		}
		for wave := int64(0); wave < 8; wave++ {
			at := 2 + wave*25
			for r := int32(0); r < 3; r++ {
				for _, typ := range []VoteType{Prevote, Precommit} {
					inject(r, typ, digest(1), at) // one story to the network...
					inject(r, typ, digest(2), at) // ...and its contradiction
				}
			}
		}

		s.startAll()
		if decided := s.run(200_000); decided != 4 {
			t.Fatalf("seed %d: only %d/4 honest drivers decided with a byzantine validator present", seed, decided)
		}
		evidenceSeen := false
		for _, d := range s.drivers {
			for _, ev := range d.Evidence() {
				if ev.Validator == byzKey && ev.Valid(scheme, network) {
					evidenceSeen = true
				}
			}
		}
		if !evidenceSeen {
			t.Fatalf("seed %d: the equivocator produced no valid slashable evidence at any honest driver", seed)
		}
	}
}
