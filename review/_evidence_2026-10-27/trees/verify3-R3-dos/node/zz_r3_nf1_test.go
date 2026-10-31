package node

// R3 verification (group R3-dos) for NF1 / NEW-4. Every proposal goes through handle(), the function
// the run loop calls per event, after a JSON round-trip, so the bodies are what the HTTP intake hands
// the loop. Run: GOLEDGER_CHAOS_SLACK=4 go test -count=1 -run TestR3NF1 -v ./node/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// countTransport counts what the node broadcasts without retaining it (so it does not skew heap numbers).
type countTransport struct{ proposals, votes atomic.Int64 }

func (c *countTransport) Broadcast(from string, msg Message) {
	switch msg.Type {
	case MsgProposal:
		c.proposals.Add(1)
	case MsgVote:
		c.votes.Add(1)
	}
}

type r3net struct {
	signers []*signing.Signer
	vs      *consensus.ValidatorSet
	genesis []ledger.Account
}

func newR3Net(t *testing.T, n int) *r3net {
	t.Helper()
	r := &r3net{}
	vals := make([]consensus.Validator, n)
	for i := 0; i < n; i++ {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		r.signers = append(r.signers, s)
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	r.vs = consensus.NewValidatorSet(vals)
	r.genesis = []ledger.Account{{PublicKey: r.signers[0].PublicKeyBase64(), Amount: rat("100"), ValidatorNode: r.signers[0].PublicKeyBase64()}}
	return r
}

// handNode builds node `self` and starts height 1 with no run loop: the test calls handle() itself.
func (r *r3net) handNode(self int) (*Node, *countTransport) {
	tr := &countTransport{}
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: r.signers[self], Validators: r.vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour, MinBlockInterval: time.Hour, Genesis: r.genesis,
		MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000}, tr)
	nd.started = true
	nd.startHeight()
	return nd, tr
}

// bodyFor returns a height-1 block with n payments carrying well-formed keys and 64-byte (invalid)
// signatures, bound to the current state's digest so ApplyBlock reaches the per-payment check.
func bodyFor(state ledger.LedgerState, n int, tag int) ledger.Block {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	sig, _ := a.SignBase64(signing.DomainPayment, []byte("unrelated"))
	pays := make([]ledger.Payment, n)
	for i := range pays {
		pays[i] = ledger.Payment{PublicKeyFrom: a.PublicKeyBase64(), PublicKeyTo: b.PublicKeyBase64(), Amount: rat("1"), BlockID: 1, Signature: sig}
	}
	var d [32]byte
	copy(d[:], fmt.Sprintf("r3-junk-%08d", tag))
	return ledger.Block{Height: 1, PrevStateDigest: state.Digest(), Payments: pays, StateDigest: d}
}

// wire signs pm with s and JSON round-trips it, as the HTTP intake would decode it.
func wire(t *testing.T, s *signing.Signer, pm ProposalMsg) (ProposalMsg, int) {
	t.Helper()
	if err := SignProposal(s, &pm); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var out ProposalMsg
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, len(raw)
}

func roundsOf(vs *consensus.ValidatorSet, key string, from int32, count int) []int32 {
	var out []int32
	for r := from; len(out) < count; r++ {
		if vs.ProposerAt(1, r) == key {
			out = append(out, r)
		}
	}
	return out
}

func heapMiB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// (1) Who can trigger the expensive path: a non-validator key, or a validator signing a round it is not
// scheduled for, is dropped before ApplyBlock; only the scheduled proposer's key gets through.
func TestR3NF1KeyRequirement(t *testing.T) {
	r := newR3Net(t, 4)
	nd, tr := r.handNode(1)
	state := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	outsider, _ := signing.Generate(signing.Ed25519{}, "testnet")
	const n = 3400
	time1 := func(s *signing.Signer, round int32, tag int) (time.Duration, bool) {
		pm, size := wire(t, s, ProposalMsg{Height: 1, Round: round, ValidRound: -1, Block: bodyFor(state, n, tag)})
		if size > 1<<20 {
			t.Fatalf("proposal is %d bytes, over the 1 MiB intake bound", size)
		}
		before := tr.proposals.Load()
		start := time.Now()
		nd.handle(event{kind: evProposal, proposal: &pm})
		return time.Since(start), tr.proposals.Load() > before
	}
	attacker := r.signers[0]
	scheduled := roundsOf(r.vs, attacker.PublicKeyBase64(), 1000, 1)[0]
	var notScheduled int32 = 1000
	for r.vs.ProposerAt(1, notScheduled) == attacker.PublicKeyBase64() {
		notScheduled++
	}
	dOut, relOut := time1(outsider, scheduled, 1)
	dWrong, relWrong := time1(attacker, notScheduled, 2)
	dOK, relOK := time1(attacker, scheduled, 3)
	t.Logf("outsider key: %v relayed=%v | validator, unscheduled round: %v relayed=%v | validator, scheduled round %d: %v relayed=%v",
		dOut.Round(time.Microsecond), relOut, dWrong.Round(time.Microsecond), relWrong, scheduled, dOK.Round(time.Millisecond), relOK)
	if relOut || relWrong || !relOK {
		t.Fatalf("unexpected gate behaviour")
	}
}

// (2) Per-proposal event-loop time and retained memory for ~1 MiB bodies: N distinct digests for one
// scheduled round plus one per further scheduled round far beyond the votes' 256-round window.
func TestR3NF1FloodCostPerProposal(t *testing.T) {
	r := newR3Net(t, 4)
	nd, tr := r.handNode(1)
	state := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	attacker := r.signers[0]
	const n = 3400
	const sameRound, farRounds = 10, 30
	rounds := roundsOf(r.vs, attacker.PublicKeyBase64(), 100000, farRounds)
	type job struct {
		round int32
		tag   int
	}
	var jobs []job
	for i := 0; i < sameRound; i++ {
		jobs = append(jobs, job{rounds[0], 100 + i})
	}
	for i, rr := range rounds[1:] {
		jobs = append(jobs, job{rr, 200 + i})
	}
	size := 0
	h0 := heapMiB()
	blocks0 := len(nd.blocks)
	var total, maxOne time.Duration
	for _, j := range jobs {
		pm, sz := wire(t, attacker, ProposalMsg{Height: 1, Round: j.round, ValidRound: -1, Block: bodyFor(state, n, j.tag)})
		size = sz
		start := time.Now()
		nd.handle(event{kind: evProposal, proposal: &pm})
		d := time.Since(start)
		total += d
		if d > maxOne {
			maxOne = d
		}
	}
	h1 := heapMiB()
	retained := len(nd.blocks) - blocks0
	t.Logf("proposal size %d bytes; %d proposals (%d same-round digests + %d far rounds, max round %d, driver round %d)",
		size, sameRound+farRounds-1, sameRound, farRounds-1, rounds[len(rounds)-1], nd.driver.Round())
	t.Logf("event-loop time: total %v, mean %v/proposal, max %v; relayed %d; retained blocks +%d; heap +%.1f MiB (%.2f MiB/proposal)",
		total.Round(time.Millisecond), (total / time.Duration(sameRound+farRounds-1)).Round(time.Millisecond), maxOne.Round(time.Millisecond),
		tr.proposals.Load(), retained, h1-h0, (h1-h0)/float64(sameRound+farRounds-1))
}

// (3) NEW-4 facet: tiny proposals (no body, cheap ApplyBlock reject) for many far rounds. Measures how the
// cost of each further proposal and of one ordinary peer vote grows with the number of rounds held.
func TestR3NF1RoundCountAmplification(t *testing.T) {
	r := newR3Net(t, 4)
	nd, tr := r.handNode(1)
	attacker, voter := r.signers[0], r.signers[2]
	vote := func(round int32) time.Duration {
		v, _ := consensus.SignVote(voter, consensus.Vote{Height: 1, Round: round, Type: consensus.Prevote, Validator: voter.PublicKeyBase64()})
		start := time.Now()
		nd.handle(event{kind: evVote, vote: &v})
		return time.Since(start)
	}
	t.Logf("rounds held %6d: one peer vote %v", len(nd.heightProposals), vote(0).Round(time.Microsecond))
	h0 := heapMiB()
	next := int32(1000)
	sent := 0
	for _, target := range []int{1000, 5000, 20000} {
		var batch time.Duration
		k := 0
		for sent < target {
			rr := roundsOf(r.vs, attacker.PublicKeyBase64(), next, 1)[0]
			next = rr + 1
			var d [32]byte
			copy(d[:], fmt.Sprintf("tiny-%d", rr))
			pm, _ := wire(t, attacker, ProposalMsg{Height: 1, Round: rr, ValidRound: -1, Block: ledger.Block{Height: 1, StateDigest: d}})
			start := time.Now()
			nd.handle(event{kind: evProposal, proposal: &pm})
			batch += time.Since(start)
			sent++
			k++
		}
		t.Logf("rounds held %6d: last %d tiny proposals cost %v of loop time (%v each); one peer vote now %v; relayed so far %d; heap +%.1f MiB",
			len(nd.heightProposals), k, batch.Round(time.Millisecond), (batch / time.Duration(k)).Round(time.Microsecond),
			vote(0).Round(time.Microsecond), tr.proposals.Load(), heapMiB()-h0)
	}
}

// (4) The same ~1 MiB proposal through the real HTTP handler of a running node: accepted (202) and
// processed by the loop (relayed), with wall time from POST to the loop being free again.
func TestR3NF1ThroughHTTP(t *testing.T) {
	r := newR3Net(t, 4)
	tr := &countTransport{}
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: r.signers[1], Validators: r.vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour, MinBlockInterval: time.Hour, Genesis: r.genesis,
		MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000}, tr)
	srv := httptest.NewServer(NewServer(nd).Handler())
	defer srv.Close()
	nd.Start()
	defer nd.Stop()
	snap := nd.Snapshot()
	state := ledger.LedgerState{Accounts: snap.Accounts, Parameters: snap.Parameters, GovernanceSequence: snap.GovernanceSequence}
	attacker := r.signers[0]
	rounds := roundsOf(r.vs, attacker.PublicKeyBase64(), 50000, 5)
	start := time.Now()
	for i, rr := range rounds {
		pm := ProposalMsg{Height: 1, Round: rr, ValidRound: -1, Block: bodyFor(state, 3400, 900+i)}
		if err := SignProposal(attacker, &pm); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(pm)
		resp, err := http.Post(srv.URL+"/hdls/proposal", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST %d bytes: HTTP %d", len(raw), resp.StatusCode)
		}
	}
	_ = nd.Snapshot() // queued behind the proposals: returns once the loop has processed them
	t.Logf("5 proposals POSTed and processed in %v; relayed by the node: %d", time.Since(start).Round(time.Millisecond), tr.proposals.Load())
}

// (5) Same-round equivocation inside the window (round 1 while the node is at round 0): every distinct
// digest the scheduled proposer signs is re-applied, retained and relayed.
func TestR3NF1SameRoundInWindow(t *testing.T) {
	r := newR3Net(t, 4)
	attackerKey := r.vs.ProposerAt(1, 1)
	attacker, self := -1, -1
	for i, s := range r.signers {
		if s.PublicKeyBase64() == attackerKey {
			attacker = i
		} else if self < 0 {
			self = i
		}
	}
	nd, tr := r.handNode(self)
	state := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	before, blocks0, h0 := tr.proposals.Load(), len(nd.blocks), heapMiB()
	var total time.Duration
	for i := 0; i < 10; i++ {
		pm, _ := wire(t, r.signers[attacker], ProposalMsg{Height: 1, Round: 1, ValidRound: -1, Block: bodyFor(state, 3400, 500+i)})
		start := time.Now()
		nd.handle(event{kind: evProposal, proposal: &pm})
		total += time.Since(start)
	}
	t.Logf("10 distinct ~1 MiB proposals for round 1: loop time %v, relayed %d, retained blocks +%d, heap +%.1f MiB",
		total.Round(time.Millisecond), tr.proposals.Load()-before, len(nd.blocks)-blocks0, heapMiB()-h0)
}

// (6) What a bound of d.round+maxRoundLookahead (the votes' 256) plus first-wins would still allow per
// height: one ~1 MiB proposal for each round in [2, 256] the attacker is scheduled for.
func TestR3NF1ResidualUnderVoteLookahead(t *testing.T) {
	r := newR3Net(t, 4)
	nd, tr := r.handNode(1)
	state := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	attacker := r.signers[0]
	var total time.Duration
	k := 0
	for rr := int32(2); rr <= 256; rr++ {
		if r.vs.ProposerAt(1, rr) != attacker.PublicKeyBase64() {
			continue
		}
		pm, _ := wire(t, attacker, ProposalMsg{Height: 1, Round: rr, ValidRound: -1, Block: bodyFor(state, 3400, 3000+int(rr))})
		start := time.Now()
		nd.handle(event{kind: evProposal, proposal: &pm})
		total += time.Since(start)
		k++
	}
	t.Logf("1 of 4 equal-power validators: %d scheduled rounds in [2,256] at height 1 -> %v of loop time, relayed %d, retained %d blocks",
		k, total.Round(time.Millisecond), tr.proposals.Load(), len(nd.blocks))
}
