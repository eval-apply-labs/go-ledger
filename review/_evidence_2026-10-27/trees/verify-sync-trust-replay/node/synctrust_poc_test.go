package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// ---------------------------------------------------------------------------
// PoC 1 (SYNC-1, HIGH): long-range / nothing-at-stake checkpoint forgery.
//
// A fresh or (re)starting node anchors trust on the GENESIS validator set
// forever (bestCheckpoint passes n.cfg.Genesis; adopt/loadSnapshot pass the
// pre-start n.accounts, which is genesis). verifyCheckpoint accepts ANY
// checkpoint as long as:
//
//	(1) its Commit is a >2/3 quorum of the signing set the PEER supplied, and
//	(2) >1/3 of the genesis anchor's power is among those signers.
//
// So an attacker who holds the private keys of >1/3 (by genesis power) of the
// GENESIS validators can fabricate a completely attacker-controlled state and
// a self-signed commit over it, and a joining node adopts it. This is the
// eMudhra lesson applied to consensus: those genesis keys may have LEFT the
// active set (zero current stake, nothing to slash, invisible to live
// consensus) or have been weakly generated — yet they remain a permanent root
// of trust for every future joiner, with no weak-subjectivity checkpoint or
// pinning to retire them.
//
// The test shows the checkpoint being ACCEPTED and then adopted (the node's
// accounts become the attacker's), while an up-to-date node (whose anchor is
// the live, rotated set) would reject the very same checkpoint.
// ---------------------------------------------------------------------------
func TestPoCLongRangeCheckpointForgeryAcceptedByJoiner(t *testing.T) {
	net := "village"
	// Genesis validators A,B,C — equal stake, all above threshold. These are the
	// anchor a fresh joiner trusts. Imagine them as the founding laptops.
	a, _ := signing.Generate(signing.Ed25519{}, net)
	b, _ := signing.Generate(signing.Ed25519{}, net)
	c, _ := signing.Generate(signing.Ed25519{}, net)
	A, B, C := a.PublicKeyBase64(), b.PublicKeyBase64(), c.PublicKeyBase64()

	genesis := []ledger.Account{
		{PublicKey: A, Name: "founderA", Amount: rat("1000"), ValidatorNode: A},
		{PublicKey: B, Name: "founderB", Amount: rat("1000"), ValidatorNode: B},
		{PublicKey: C, Name: "founderC", Amount: rat("1000"), ValidatorNode: C},
	}

	// A fresh joiner in DYNAMIC-validator mode (the set rotates with stake).
	joiner := New(Config{
		NetworkID: net, Scheme: signing.Ed25519{}, Signer: a, // signer identity irrelevant for verify
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))

	// The attacker holds the founders' keys (they left; keys abandoned / weak).
	attacker, _ := signing.Generate(signing.Ed25519{}, net)
	M := attacker.PublicKeyBase64()

	// Fabricated state at a far-future height: the founders hold ZERO (they have
	// left the network), and the attacker holds a minted fortune. Note the state
	// itself says the signers are no longer validators.
	malicious := []ledger.Account{
		{PublicKey: A, Amount: rat("0"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("0"), ValidatorNode: B},
		{PublicKey: C, Amount: rat("0"), ValidatorNode: C},
		{PublicKey: M, Name: "attacker", Amount: rat("9000000000"), ValidatorNode: M},
	}
	maliciousDigest := ledger.LedgerState{Accounts: malicious}.Digest()

	// The attacker chooses the signing set the checkpoint carries: the genesis
	// validators (whose keys it holds). It signs a >2/3 commit over the forged
	// digest at height 100.
	signingSet := []consensus.Validator{{PublicKey: A, Power: 1000}, {PublicKey: B, Power: 1000}, {PublicKey: C, Power: 1000}}
	forged := Checkpoint{
		Accounts:   malicious,
		Commit:     buildCommit(t, 100, maliciousDigest, signingSet, map[string]*signing.Signer{A: a, B: b, C: c}),
		Validators: signingSet,
	}

	// THE BUG: the joiner accepts the forged checkpoint.
	if !joiner.verifyCheckpoint(forged, genesis) {
		t.Fatal("expected the long-range forged checkpoint to be (wrongly) ACCEPTED by a genesis-anchored joiner")
	}
	t.Logf("SYNC-1: genesis-anchored joiner ACCEPTED a checkpoint signed only by former/abandoned validator keys")

	// And it would actually adopt it — the node's whole view becomes attacker-controlled.
	joiner.fastForward(forged)
	if got := ledger.AccountAmount(joiner.accounts, M); got.Cmp(rat("9000000000")) != 0 {
		t.Fatalf("node did not adopt the attacker balance; got %s", got.RatString())
	}
	if joiner.height != 101 {
		t.Fatalf("node did not fast-forward to the forged height; height=%d", joiner.height)
	}
	t.Logf("SYNC-1: after adopt, node believes attacker holds %s at height %d",
		ledger.AccountAmount(joiner.accounts, M).RatString(), joiner.height)

	// Contrast: an UP-TO-DATE node, whose anchor is the live rotated state (the
	// attacker's former-validator keys are NOT current validators there),
	// correctly rejects the same forged checkpoint. Only joiners are fooled.
	liveRotatedState := []ledger.Account{
		{PublicKey: M, Amount: rat("1"), ValidatorNode: M}, // whoever the live validators actually are
	}
	freshJoinerForContrast := New(Config{
		NetworkID: net, Scheme: signing.Ed25519{}, Signer: a,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))
	if freshJoinerForContrast.verifyCheckpoint(forged, liveRotatedState) {
		t.Fatal("an up-to-date node (live-set anchor) must reject the forged checkpoint — if it accepts, the finding is even broader")
	}
	t.Logf("SYNC-1: the SAME checkpoint is rejected when anchored to the live (rotated) set — confirms joiners are the exposed population")
}

// Sub-variant: >1/3 of the genesis keys is enough (not all). With 3 equal
// genesis validators, TWO compromised founder keys already clear the anchor
// overlap AND can self-sign a 2-of-2 quorum over an attacker-chosen signing
// set — so the precondition is ">1/3 of the former set", not "all of it".
func TestPoCLongRangeNeedsOnlyOverOneThirdOfGenesisKeys(t *testing.T) {
	net := "village"
	a, _ := signing.Generate(signing.Ed25519{}, net)
	b, _ := signing.Generate(signing.Ed25519{}, net)
	c, _ := signing.Generate(signing.Ed25519{}, net)
	A, B, C := a.PublicKeyBase64(), b.PublicKeyBase64(), c.PublicKeyBase64()
	genesis := []ledger.Account{
		{PublicKey: A, Amount: rat("1000"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("1000"), ValidatorNode: B},
		{PublicKey: C, Amount: rat("1000"), ValidatorNode: C},
	}
	joiner := New(Config{
		NetworkID: net, Scheme: signing.Ed25519{}, Signer: a,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))

	attacker, _ := signing.Generate(signing.Ed25519{}, net)
	M := attacker.PublicKeyBase64()
	malicious := []ledger.Account{{PublicKey: M, Name: "attacker", Amount: rat("9000000000"), ValidatorNode: M}}
	maliciousDigest := ledger.LedgerState{Accounts: malicious}.Digest()

	// Attacker compromised only A and B (2 of 3 genesis keys). It advertises a
	// signing set of just {A,B} with equal power; quorum of that set is 2, which
	// A+B meet. Anchor overlap: A+B hold 2000 of 3000 genesis power → >1/3.
	signingSet := []consensus.Validator{{PublicKey: A, Power: 1000}, {PublicKey: B, Power: 1000}}
	forged := Checkpoint{
		Accounts:   malicious,
		Commit:     buildCommit(t, 50, maliciousDigest, signingSet, map[string]*signing.Signer{A: a, B: b}),
		Validators: signingSet,
	}
	if !joiner.verifyCheckpoint(forged, genesis) {
		t.Fatal("expected 2-of-3 compromised genesis keys to be enough to forge an accepted checkpoint")
	}
	t.Logf("SYNC-1: 2 of 3 former-validator keys sufficed to forge a checkpoint a joiner accepts")
}

// ---------------------------------------------------------------------------
// PoC 3 (SYNC-3, MEDIUM, liveness — the flip side of SYNC-1): once the dynamic
// validator set rotates FULLY past the genesis validators, a genuine, honest,
// current checkpoint is REJECTED by any joiner/restarter, because the anchor
// (genesis) has zero overlap with the live signing set. consensus.md says
// full rotation "would need a chain of intermediate checkpoints to walk", but
// no such walk/bisection exists (bestCheckpoint fetches ONE checkpoint per
// peer) and there is no weak-subjectivity pin to move the anchor forward. So
// onboarding and crash-recovery silently break forever after the founders
// leave — the node falls back to genesis and can never advance.
// ---------------------------------------------------------------------------
func TestPoCFullRotationBricksOnboarding(t *testing.T) {
	net := "village"
	a, _ := signing.Generate(signing.Ed25519{}, net)
	A := a.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: A, Name: "founder", Amount: rat("1000"), ValidatorNode: A}}

	joiner := New(Config{
		NetworkID: net, Scheme: signing.Ed25519{}, Signer: a,
		Validators: nil, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: time.Second, Genesis: genesis,
	}, NewHTTPTransport(nil))

	// The live network after full rotation: entirely new validators X,Y,Z; the
	// founder A has left (not a validator, below threshold). This is an HONEST
	// current checkpoint.
	x, _ := signing.Generate(signing.Ed25519{}, net)
	y, _ := signing.Generate(signing.Ed25519{}, net)
	z, _ := signing.Generate(signing.Ed25519{}, net)
	X, Y, Z := x.PublicKeyBase64(), y.PublicKeyBase64(), z.PublicKeyBase64()
	current := []ledger.Account{
		{PublicKey: A, Amount: rat("5"), ValidatorNode: A}, // founder dropped below threshold
		{PublicKey: X, Amount: rat("500"), ValidatorNode: X},
		{PublicKey: Y, Amount: rat("500"), ValidatorNode: Y},
		{PublicKey: Z, Amount: rat("500"), ValidatorNode: Z},
	}
	currentSet := []consensus.Validator{{PublicKey: X, Power: 500}, {PublicKey: Y, Power: 500}, {PublicKey: Z, Power: 500}}
	honestCP := Checkpoint{
		Accounts:   current,
		Commit:     buildCommit(t, 42, ledger.LedgerState{Accounts: current}.Digest(), currentSet, map[string]*signing.Signer{X: x, Y: y, Z: z}),
		Validators: currentSet,
	}

	// A genuine, >2/3-signed, digest-bound current checkpoint — yet rejected,
	// because no genesis-anchor validator co-signed it. The joiner can never
	// catch up to the real chain.
	if joiner.verifyCheckpoint(honestCP, genesis) {
		t.Fatal("expected a post-full-rotation honest checkpoint to be REJECTED by a genesis-anchored joiner (if accepted, SYNC-3 is not reproduced)")
	}
	t.Logf("SYNC-3: an honest, >2/3-signed current checkpoint is REJECTED after full rotation — onboarding/recovery is bricked with no implemented bisection or pin")
}

// ---------------------------------------------------------------------------
// PoC 2 (SYNC-2, MEDIUM): fetchCheckpoint has no response-size bound.
//
// The server's own intake bounds every request body at 1 MiB
// (httptransport.go: http.MaxBytesReader(w, r.Body, 1<<20)). But the catch-up
// client reads a peer's /hdls/checkpoint reply with a bare
// json.NewDecoder(resp.Body).Decode(&cp) and no limit (sync.go:115). A
// catch-up SOURCE (a bootstrap peer, or — for a node with no bootstrap — any
// current validator that published its on-chain address, i.e. ONE dishonest
// validator) can return an arbitrarily large body and exhaust the memory of
// every node that polls it each catch-up tick.
//
// The test serves a ~4 MiB checkpoint (well over the 1 MiB intake bound) and
// shows fetchCheckpoint decodes it happily — proving no cap is applied.
// ---------------------------------------------------------------------------
func TestPoCUnboundedCheckpointFetch(t *testing.T) {
	const padBytes = 4 << 20 // 4 MiB — 4x the server's 1 MiB intake bound
	huge := strings.Repeat("A", padBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A valid-shaped checkpoint whose Description field alone is 4 MiB.
		cp := Checkpoint{Accounts: []ledger.Account{{PublicKey: "x", Description: huge}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cp)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	cp, err := fetchCheckpoint(client, srv.URL)
	if err != nil {
		t.Fatalf("fetchCheckpoint errored on a large body (if this is a size-limit error, the bug is fixed): %v", err)
	}
	got := 0
	if len(cp.Accounts) == 1 {
		got = len(cp.Accounts[0].Description)
	}
	if got < padBytes {
		t.Fatalf("expected the full %d-byte oversized field to be decoded unbounded, got %d", padBytes, got)
	}
	t.Logf("SYNC-2: fetchCheckpoint decoded a %d-byte checkpoint with no size cap (server intake caps at %d)", got, 1<<20)
}
