package consensus

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// keyed returns the signers indexed by their base64 public key, for picking the proposer /
// specific validators deterministically.
func keyed(signers []*signing.Signer) map[string]*signing.Signer {
	m := make(map[string]*signing.Signer, len(signers))
	for _, s := range signers {
		m[s.PublicKeyBase64()] = s
	}
	return m
}

func findVote(acts []Action, typ VoteType, round int32) *Vote {
	for _, a := range acts {
		if bv, ok := a.(ActBroadcastVote); ok && bv.Vote.Type == typ && bv.Vote.Round == round {
			v := bv.Vote
			return &v
		}
	}
	return nil
}

func hasProposal(acts []Action) bool {
	for _, a := range acts {
		if _, ok := a.(ActProposal); ok {
			return true
		}
	}
	return false
}

func hasSchedule(acts []Action, step Step, round int32) bool {
	for _, a := range acts {
		if s, ok := a.(ActSchedule); ok && s.Step == step && s.Round == round {
			return true
		}
	}
	return false
}

// A node behind in rounds must skip forward when it sees f+1 (>1/3) voting power from a higher
// round — an honest node is already there, so it is safe to catch up.
func TestDriverSkipsToHigherRoundOnFPlusOne(t *testing.T) {
	signers, vs := makeValidators(t, 4) // total 4 → SkipThreshold = 4/3+1 = 2
	by := keyed(signers)
	const height = int64(1)
	me := nonProposer(vs, by, height, 0) // observing at round 0
	d := NewDriver(me, vs, scheme, network, height, digest(0x01))
	d.Start()
	if d.Round() != 0 {
		t.Fatalf("should start at round 0, got %d", d.Round())
	}

	var others []*signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() != me.PublicKeyBase64() {
			others = append(others, s)
		}
	}
	X := digest(0x11)
	d.ReceiveVote(mustVote(t, others[0], height, 2, Prevote, X)) // 1 of 4 — below threshold, no skip
	if d.Round() != 0 {
		t.Fatalf("one higher-round vote should not skip, at round %d", d.Round())
	}
	d.ReceiveVote(mustVote(t, others[1], height, 2, Prevote, X)) // 2 of 4 — f+1, skip
	if d.Round() != 2 {
		t.Fatalf("expected skip to round 2 on f+1 higher-round votes, at round %d", d.Round())
	}
}

// nonProposer returns a signer that is NOT the proposer for any of the given rounds at height.
func nonProposer(vs *ValidatorSet, by map[string]*signing.Signer, height int64, rounds ...int32) *signing.Signer {
	excluded := map[string]bool{}
	for _, r := range rounds {
		excluded[vs.ProposerAt(height, r)] = true
	}
	for _, v := range vs.Validators() {
		if !excluded[v.PublicKey] {
			return by[v.PublicKey]
		}
	}
	return nil
}

// Happy path: proposer proposes, a quorum prevotes then precommits, the height decides.
func TestDriverHappyPathDecides(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)

	proposerKey := vs.ProposerAt(height, 0)
	me := by[proposerKey]
	var peers []*signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() != proposerKey {
			peers = append(peers, s)
		}
	}

	dg := digest(0x42)
	d := NewDriver(me, vs, scheme, network, height, dg)

	acts := d.Start()
	if !hasProposal(acts) {
		t.Fatal("proposer should emit a proposal on Start")
	}
	if pv := findVote(acts, Prevote, 0); pv == nil || pv.BlockDigest != dg {
		t.Fatal("proposer should prevote its own proposal")
	}

	// Two peer prevotes for dg -> with the proposer's own, that is quorum (3 of 4) -> precommit.
	d.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, dg))
	acts = d.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, dg))
	if pc := findVote(acts, Precommit, 0); pc == nil || pc.BlockDigest != dg {
		t.Fatal("reaching 2/3 prevotes should make the node lock and precommit dg")
	}

	// Two peer precommits for dg -> quorum of precommits -> decide.
	d.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, dg))
	d.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, dg))
	got, ok := d.Decision()
	if !ok || got != dg {
		t.Fatalf("expected decision on dg, got ok=%v", ok)
	}
}

// A non-proposer with no proposal times out to prevote-nil, precommit-nil, then advances round.
func TestDriverTimeoutAdvancesRound(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)

	me := nonProposer(vs, by, height, 0) // must not be the round-0 proposer, so it arms the timeout
	var peers []*signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, s)
		}
	}

	d := NewDriver(me, vs, scheme, network, height, digest(0x01))
	acts := d.Start()
	if !hasSchedule(acts, StepPropose, 0) {
		t.Fatal("a non-proposer should arm the propose timeout")
	}

	d.Timeout(StepPropose, 0)
	if d.Step() != StepPrevote {
		t.Fatal("propose timeout should move to prevote (nil)")
	}

	d.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, nilDigest))
	d.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, nilDigest))
	if d.Step() != StepPrecommit {
		t.Fatal("2/3 prevote-nil should move to precommit (nil)")
	}

	d.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, nilDigest))
	acts = d.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, nilDigest))
	if !hasSchedule(acts, StepPrecommit, 0) {
		t.Fatal("2/3 precommits should arm the precommit timeout")
	}

	d.Timeout(StepPrecommit, 0)
	if d.Round() != 1 {
		t.Fatalf("precommit timeout should advance to round 1, got %d", d.Round())
	}
}

// G8 (external review): a vote from a far-future round must be dropped WITHOUT allocating per-round
// state — maxRoundLookahead exists so a rogue validator cannot force a VoteSet allocation per
// arbitrary future round (memory exhaustion). A negative round is rejected outright, and the
// boundary round (exactly current + lookahead) is accepted.
func TestDriverRejectsFarFutureAndNegativeRounds(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)
	me := nonProposer(vs, by, height, 0)
	var peer *signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() != me.PublicKeyBase64() {
			peer = s
			break
		}
	}
	d := NewDriver(me, vs, scheme, network, height, digest(0x01))
	d.Start()

	far := d.Round() + maxRoundLookahead + 1
	if acts := d.ReceiveVote(mustVote(t, peer, height, far, Prevote, digest(0x11))); len(acts) != 0 {
		t.Fatal("a far-future vote must produce no actions")
	}
	if d.prevotes[far] != nil {
		t.Fatal("a far-future vote must not allocate a VoteSet")
	}
	d.ReceiveVote(mustVote(t, peer, height, -1, Prevote, digest(0x11)))
	if d.prevotes[-1] != nil {
		t.Fatal("a negative round must not allocate a VoteSet")
	}
	edge := d.Round() + maxRoundLookahead
	d.ReceiveVote(mustVote(t, peer, height, edge, Prevote, digest(0x11)))
	if d.prevotes[edge] == nil {
		t.Fatal("a vote at exactly the lookahead bound must be accepted")
	}
}

// G7 (external review): the castVote signing-error branch, documented "unreachable in practice".
// A driver whose signer cannot sign must drop the transition quietly — no panic, no broadcast, and
// the one-shot state untouched (the vote is not marked sent, the step does not advance). No
// injection seam is needed: signing.NewSigner accepts arbitrary key bytes and Ed25519.Sign
// length-checks at sign time — the same misconfiguration cmd/server refuses at config load.
func TestDriverBrokenSignerDropsVoteQuietly(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)
	me := nonProposer(vs, by, height, 0)
	broken := signing.NewSigner(scheme, network, me.PublicKey(), []byte("truncated"))
	d := NewDriver(broken, vs, scheme, network, height, digest(0x01))
	d.Start() // non-proposer: arms the propose timeout, nothing signed yet

	acts := d.Timeout(StepPropose, 0) // would prevote nil — the signature fails instead
	if findVote(acts, Prevote, 0) != nil {
		t.Fatal("a broken signer must not broadcast a vote")
	}
	if d.sentPrevote[0] {
		t.Fatal("the failed vote must not be marked sent")
	}
	if d.Step() != StepPropose {
		t.Fatal("the failed transition must not advance the step")
	}
}

// G6 (external review): the POL/unlock release path — the complement of
// TestDriverLockingPreventsConflictingPrevote below. A node locked on X MUST prevote a different
// value Y when the round's proposal carries a proof-of-lock from a round vr >= its lockedRound
// (Tendermint lines 28–29: a ⅔ prevote quorum at vr proves the network moved past the lock).
// Without this release, two locked camps could stall a height forever.
func TestDriverPOLUnlockReleasesLock(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)
	me := nonProposer(vs, by, height, 0, 1, 2) // receives injected proposals in rounds 0..2
	var peers []*signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, s)
		}
	}
	X, Y := digest(0x11), digest(0x22)
	d := NewDriver(me, vs, scheme, network, height, digest(0x00))
	d.Start()

	// Round 0: me locks X (proposal X + 2/3 prevotes) and precommits it.
	d.ReceiveProposal(Proposal{Round: 0, Digest: X, ValidRound: -1, Valid: true})
	d.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, X))
	d.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, X))
	if d.lockedValue == nil || *d.lockedValue != X {
		t.Fatal("me should be locked on X after 2/3 prevotes for X")
	}
	d.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, nilDigest))
	d.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, nilDigest))
	d.Timeout(StepPrecommit, 0)

	// Round 1: all three peers prevote Y — a full ⅔ proof-of-lock for Y at round 1, which me
	// witnesses but does not join (locked on X with no acceptable proposal, it times out to nil).
	for _, p := range peers {
		d.ReceiveVote(mustVote(t, p, height, 1, Prevote, Y))
	}
	d.Timeout(StepPropose, 1) // me prevotes nil (locked on X)
	d.Timeout(StepPrevote, 1) // me precommits nil
	d.ReceiveVote(mustVote(t, peers[0], height, 1, Precommit, nilDigest))
	d.ReceiveVote(mustVote(t, peers[1], height, 1, Precommit, nilDigest))
	d.Timeout(StepPrecommit, 1)
	if d.Round() != 2 {
		t.Fatalf("expected round 2, got %d", d.Round())
	}

	// Round 2: proposal Y arrives carrying the round-1 POL. vr=1 >= lockedRound=0, and me holds the
	// round-1 prevote quorum proving it — the lock must release and me must prevote Y, not nil.
	acts := d.ReceiveProposal(Proposal{Round: 2, Digest: Y, ValidRound: 1, Valid: true})
	pv := findVote(acts, Prevote, 2)
	if pv == nil {
		t.Fatal("expected a round-2 prevote")
	}
	if pv.BlockDigest != Y {
		t.Fatalf("a POL at vr=1 >= lockedRound=0 must release the lock: prevoted %x, want Y", pv.BlockDigest[:4])
	}
}

// Safety: once a node has locked value X, it must NOT prevote a conflicting value Y in a later
// round without a proof-of-lock -- it prevotes nil instead. This is the locking rule a
// single-phase snapshot cannot provide.
func TestDriverLockingPreventsConflictingPrevote(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	by := keyed(signers)
	const height = int64(1)

	me := nonProposer(vs, by, height, 0, 1) // must not propose in round 0 or 1 (it receives injected proposals)
	var peers []*signing.Signer
	for _, s := range signers {
		if s.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, s)
		}
	}
	X, Y := digest(0x11), digest(0x22)

	d := NewDriver(me, vs, scheme, network, height, digest(0x00))
	d.Start()

	// Round 0: proposal X arrives; me prevotes X; two peers prevote X -> me locks X, precommits X.
	d.ReceiveProposal(Proposal{Round: 0, Digest: X, ValidRound: -1, Valid: true})
	d.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, X))
	d.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, X))
	if d.lockedValue == nil || *d.lockedValue != X {
		t.Fatal("me should be locked on X after 2/3 prevotes for X")
	}

	// Drive to round 1: gather 2/3 precommits (me=X, two peers=nil) then fire the precommit timeout.
	d.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, nilDigest))
	d.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, nilDigest))
	d.Timeout(StepPrecommit, 0)
	if d.Round() != 1 {
		t.Fatalf("expected round 1, got %d", d.Round())
	}

	// Round 1: a fresh proposal for a DIFFERENT value Y (no POL). me is locked on X.
	acts := d.ReceiveProposal(Proposal{Round: 1, Digest: Y, ValidRound: -1, Valid: true})
	pv := findVote(acts, Prevote, 1)
	if pv == nil {
		t.Fatal("expected a round-1 prevote")
	}
	if pv.BlockDigest != nilDigest {
		t.Fatal("a node locked on X must prevote nil for conflicting Y, not Y")
	}
}
