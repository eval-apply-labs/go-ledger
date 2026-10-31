package node

// Proof-of-concept tests for the consensus-core review (area: consensus/*.go + how node.go drives it).
// White-box (package node) so they can read n.heightVotes / n.pendingSlashes / n.driver directly.
//
// Run (from this scratch copy):
//   GOLEDGER_CHAOS_SLACK=4 go test -count=1 -run 'TestPoC' ./node/

import (
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type pocCountingTransport struct {
	mu       sync.Mutex
	voteMsgs int
}

func (c *pocCountingTransport) Broadcast(from string, msg Message) {
	c.mu.Lock()
	if msg.Type == MsgVote {
		c.voteMsgs++
	}
	c.mu.Unlock()
}
func (c *pocCountingTransport) votes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.voteMsgs
}

func pocDigest(b byte) [32]byte {
	var d [32]byte
	for i := range d {
		d[i] = b
	}
	return d
}

func pocSignVote(t *testing.T, s *signing.Signer, h int64, r int32, typ consensus.VoteType, d [32]byte) consensus.Vote {
	t.Helper()
	v, err := consensus.SignVote(s, consensus.Vote{Height: h, Round: r, Type: typ, BlockDigest: d, Validator: s.PublicKeyBase64()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func pocValidatorNode(t *testing.T, self int) ([]*signing.Signer, *consensus.ValidatorSet, []ledger.Account, Config, *pocCountingTransport, *Node) {
	t.Helper()
	signers := make([]*signing.Signer, 4)
	vals := make([]consensus.Validator, 4)
	for i := range signers {
		s, err := signing.Generate(signing.Ed25519{}, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	var genesis []ledger.Account
	for i := range signers {
		genesis = append(genesis, ledger.Account{PublicKey: signers[i].PublicKeyBase64(), Amount: rat("100"), ValidatorNode: signers[i].PublicKeyBase64()})
	}
	ct := &pocCountingTransport{}
	cfg := Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[self],
		Validators: vs, TransactionCost: rat("0.1"),
		Timeout: 5 * time.Second, Genesis: genesis,
		MaxMempool: 100, // the mempool IS capped; contrast heightVotes below
	}
	n := New(cfg, ct)
	return signers, vs, genesis, cfg, ct, n
}

// FINDING (DoS, medium): node.rememberVote stores ANY vote whose Height == current height into the
// UNBOUNDED heightVotes map with no signature check, no validator-set check, and no round bound; and
// reGossipVotes re-broadcasts the ENTIRE map to every peer on each tick. So anyone who can POST
// /hdls/vote (handleVote does not verify before SubmitVote) can (a) grow a node's memory without
// bound and (b) make every node flood the network with arbitrary junk every re-gossip tick. This is
// an amplifying, unauthenticated DoS; the heightVotes map and the re-gossip loop are both post-review
// additions (the vote-re-gossip liveness fix), so they were not in scope when #10 was deferred.
func TestPoCUnverifiedVotesStoredUnboundedAndReGossiped(t *testing.T) {
	_, _, _, cfg, ct, n := pocValidatorNode(t, 0)
	n.started = true
	n.startHeight() // create the height-1 driver (so reGossipVotes runs; driver != nil)

	beforeStore := len(n.heightVotes)

	// An OUTSIDER (not a validator, no valid signature) forges K distinct "votes" for the current
	// height. Round is absurd (the driver drops it without even allocating a VoteSet), Validator is a
	// non-member, Signature is junk — none of this is checked before the vote is remembered/relayed.
	const K = 5000
	for i := 0; i < K; i++ {
		junk := consensus.Vote{
			Height:    n.height,       // the ONLY field rememberVote inspects
			Round:     int32(1) << 30, // far-future: dropped by the driver, still stored by the node
			Type:      consensus.Prevote,
			Validator: "outsider-not-in-the-validator-set",
			Signature: "junk-signature-" + strconv.Itoa(i), // distinct => distinct heightVotes key
		}
		n.handle(event{kind: evVote, vote: &junk})
	}

	if got := len(n.heightVotes) - beforeStore; got != K {
		t.Fatalf("heightVotes grew by %d, want %d: unverified junk votes ARE stored, with NO cap "+
			"(cfg.MaxMempool=%d bounds the mempool; heightVotes has no equivalent)", got, K, cfg.MaxMempool)
	}

	// One re-gossip tick re-broadcasts every stored junk vote to peers. K posts => K*peers broadcasts
	// EVERY tick, for as long as the height has not committed (the flood itself helps prevent that).
	before := ct.votes()
	n.reGossipVotes()
	emitted := ct.votes() - before
	if emitted < K {
		t.Fatalf("one re-gossip tick emitted %d vote broadcasts, want >= %d (the whole junk set)", emitted, K)
	}
	t.Logf("CONFIRMED: %d unverified junk votes stored in heightVotes (no cap), re-broadcast %d times "+
		"in a SINGLE reGossipVotes tick — unauthenticated memory + bandwidth amplification", K, emitted)
}

// FINDING (accountability, low; touches the stated "two different signings = burned key" principle):
// equivocation evidence the driver gathers is harvested into pendingSlashes ONLY inside commit() (the
// local-decide path). A node that instead advances the height via catchUp() (verified-checkpoint
// fast-forward, the normal recovery for a node that fell behind) discards its driver — and the
// equivocation it witnessed — WITHOUT harvesting. So a double-signer can escape the slash on every
// node that resolved that height by catch-up rather than by deciding locally.
func TestPoCEquivocationEvidenceLostOnCatchUp(t *testing.T) {
	signers, vs, genesis, cfg, _, n := pocValidatorNode(t, 0)
	n.started = true
	n.startHeight() // height-1 driver

	// signers[1] equivocates at height 1, round 0: two conflicting prevotes, both delivered to us.
	cheat := signers[1]
	vX := pocSignVote(t, cheat, 1, 0, consensus.Prevote, pocDigest(0x11))
	vY := pocSignVote(t, cheat, 1, 0, consensus.Prevote, pocDigest(0x22))
	n.handle(event{kind: evVote, vote: &vX})
	n.handle(event{kind: evVote, vote: &vY})

	// Precondition: the driver DID detect it — the equivocation is actionable here and now.
	foundInDriver := false
	for _, e := range n.driver.Evidence() {
		if e.Validator == cheat.PublicKeyBase64() && e.Valid(cfg.Scheme, cfg.NetworkID) {
			foundInDriver = true
		}
	}
	if !foundInDriver {
		t.Fatal("precondition failed: the driver should hold the equivocation evidence")
	}

	// A peer that decided height 1 serves a verified checkpoint (3 honest validators precommit the
	// genesis-state digest — NOT the equivocator). We fell behind, so we catch up to it.
	st := ledger.LedgerState{Accounts: genesis, Parameters: n.parameters, GovernanceSequence: 0}
	d := st.Digest()
	var cvotes []consensus.Vote
	for _, idx := range []int{0, 2, 3} { // 3 of 4 == quorum
		cvotes = append(cvotes, pocSignVote(t, signers[idx], 1, 0, consensus.Precommit, d))
	}
	cp := Checkpoint{
		Accounts: genesis, Parameters: n.parameters, GovernanceSequence: 0,
		Commit:     consensus.Commit{Height: 1, Round: 0, BlockDigest: d, Votes: cvotes},
		Validators: vs.Validators(),
	}
	n.catchUp(cp)

	if n.height != 2 {
		t.Fatalf("precondition failed: catch-up should advance to height 2, got %d", n.height)
	}
	if _, queued := n.pendingSlashes[cheat.PublicKeyBase64()]; queued {
		t.Fatal("unexpected: the evidence was harvested after all")
	}
	t.Logf("CONFIRMED: node advanced past height 1 via catch-up; the double-signer is NOT in " +
		"pendingSlashes — this node will never carry the equivocation it witnessed into a block")
}

// FINDING (low; completeness of the "two different signings = burned key" principle): the principle
// is enforced for VOTES (prevote/precommit equivocation -> Evidence -> slash) but NOT for PROPOSALS.
// The round's scheduled proposer can sign TWO different, validly-signed proposals for the same
// (height, round) — two signings over conflicting statements by one key — and (a) there is no
// evidence type or slash for it, and (b) onProposal relays BOTH (the second is not deduped, since the
// held-proposal fast path only matches an identical digest), flooding two full blocks and splitting
// the round's prevote. It costs that round's liveness and cannot fork, but the proposer key is never
// "burned" the way a double-voting key is.
func TestPoCProposerEquivocationUnslashedAndBothRelayed(t *testing.T) {
	signers, vs, _, _, _, n := pocValidatorNode(t, 0)
	n.started = true
	n.startHeight()

	// The round-0 proposer (deterministic) signs two DIFFERENT blocks for (height 1, round 0).
	proposerKey := vs.ProposerAt(1, 0)
	var proposer *signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() == proposerKey {
			proposer = s
		}
	}
	if proposer == nil {
		t.Fatal("could not find the round-0 proposer among the signers")
	}

	mk := func(dig byte) ProposalMsg {
		p := ProposalMsg{Height: 1, Round: 0, ValidRound: -1, Block: ledger.Block{Height: 1, StateDigest: pocDigest(dig)}}
		if err := SignProposal(proposer, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := mk(0xAA)
	second := mk(0xBB) // same proposer, same (height, round), DIFFERENT block — an equivocating proposal

	if !n.onProposal(first) {
		t.Fatal("the first authentic proposal should be accepted (and relayed)")
	}
	if !n.onProposal(second) {
		t.Fatal("BUG shown: the SECOND conflicting proposal from the same proposer is ALSO accepted and relayed")
	}
	// Nothing anywhere records this as a slashable double-sign.
	if _, queued := n.pendingSlashes[proposerKey]; queued {
		t.Fatal("unexpected: proposer equivocation was recorded for slashing (it is not supposed to be, per current code)")
	}
	t.Logf("CONFIRMED: both conflicting proposals from one proposer were accepted/relayed; no slash — " +
		"proposal equivocation is outside the burned-key guarantee that covers vote equivocation")
}

// FINDING (info; new angle on the accepted C2 saturation decision): power saturation at 2^40 does not
// merely "cap the whale" — it equalizes ALL above-cap validators, discarding their true-stake
// ordering. A by-count majority of above-cap validators then forms a quorum regardless of how small
// their true-stake share is relative to a larger whale, which breaks the stake==power premise behind
// the "BFT up to <1/3 of stake" framing (docs/security.md). Reachable only at >2^40 whole-unit stakes,
// which the code comments assume away — so info, not a live break — but it contradicts the doc claim.
func TestPoCSaturationEqualizesAboveCapValidators(t *testing.T) {
	const capP = int64(1) << 40
	vs := consensus.NewValidatorSet([]consensus.Validator{
		{PublicKey: "whale", Power: math.MaxInt64}, // true stake ~2^63 — caps to 2^40
		{PublicKey: "byz-1", Power: capP + 1},      // true stake just over 2^40 — caps to 2^40
		{PublicKey: "byz-2", Power: capP + 1},
		{PublicKey: "byz-3", Power: capP + 1},
	})
	if vs.Power("whale") != vs.Power("byz-1") {
		t.Fatalf("expected the whale and a just-over-cap validator to have EQUAL capped power (%d vs %d)",
			vs.Power("whale"), vs.Power("byz-1"))
	}
	byz := vs.Power("byz-1") + vs.Power("byz-2") + vs.Power("byz-3")
	if byz < vs.Quorum() {
		t.Fatalf("the three capped above-cap validators should alone form a quorum; byz=%d quorum=%d", byz, vs.Quorum())
	}
	t.Logf("CONFIRMED: saturation gives the whale (true stake ~2^63) the SAME power as a validator with "+
		"~2^40 true stake; the three above-cap 'byz' validators hold %d of capped power vs quorum %d — "+
		"a quorum — despite a vanishing share of the whale's true stake. Only reachable at >2^40 stakes.", byz, vs.Quorum())
}
