package node

import (
	"bytes"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// onProposal returns whether a proposal is worth RELAYING: only an authentic one, on its first sight.
// A duplicate is never relayed again (the proposer's own broadcast bypasses relay() dedup, so it would
// be the one node to re-flood it), and neither is a forgery: a proposal signed under another key than
// the proposer's, or a genuine signature with a body it does not sign.
func TestOnProposalRelaysFirstSightOnlyNeverDuplicates(t *testing.T) {
	// "proposer" is the sole validator → round-0 proposer; the node under test, "observer", is NOT a
	// validator; "attacker" holds no validator slot.
	chain := staticChain("proposer")
	node := New(testConfig(chain, "observer", testGenesis([]ledger.Account{testAccount("proposer", "100")}, 5*time.Second, 0)), NewHTTPTransport(nil))
	// Deliberately NOT Start()ed: driving the state machine directly makes onProposal's relay verdict
	// observable without racing the run loop.
	node.startHeight()
	if got := node.driver.Proposer(0); got != "proposer" {
		t.Fatalf("test premise: round-0 proposer should be the sole validator, got %q", got)
	}
	block := ledger.ProposeBlock(chain, node.state, node.lastBlockHash, [32]byte{}, ledger.BlockContent{})
	otherBody := block
	otherBody.Content.Payments = make([]ledger.Payment, 512) // a body the proposer never signed

	// Forgery 1 — wrong signer: an attacker signs, under its own key and the proposer's name, the
	// genuine block or another. Dropped: it does not verify under the key the set holds for the name.
	for _, forgedBlock := range []ledger.Block{block, otherBody} {
		forgedWrongSigner := proposalOf(testSigner("attacker"), "proposer", 1, 0, forgedBlock)
		if node.onProposal(forgedWrongSigner) {
			t.Fatal("a proposal not signed by the round proposer must not be relayed")
		}
	}

	// Forgery 2 — the genuine proposer's signature (public, seen on the wire) with another body. The
	// signature covers the block's hash, so the body is not the one signed: dropped, not held.
	genuine := proposalOf(testSigner("proposer"), "proposer", 1, 0, block)
	forgedSwappedBody := genuine
	forgedSwappedBody.Block = otherBody
	if node.onProposal(forgedSwappedBody) {
		t.Fatal("a copy carrying a body its signature does not cover must not be relayed")
	}

	// The genuine proposal by the round proposer: authentic, valid and first-seen → relayed.
	if !node.onProposal(genuine) {
		t.Fatal("a genuine proposal on its first sight must be relayed")
	}

	// The same proposal again (a peer's re-gossip): already held → NOT re-relayed.
	if node.onProposal(genuine) {
		t.Fatal("a duplicate of an already-held proposal must not be re-relayed")
	}
}

// payment is a block content with one payment of amount from validator i's genesis account at
// height, to the shop: each amount gives a different, valid block.
func (network *handNet) payment(i int, height int64, amount string) ledger.BlockContent {
	return ledger.BlockContent{Payments: []ledger.Payment{signedPayment(network.name(i), "shop", amount, height)}}
}

// A validator can sign any number of different proposals for a round it proposes, and proposals for
// rounds far ahead. A node takes only the first proposal of a round, and only for rounds up to the one
// after its own: the others are neither applied, nor stored, nor relayed.
func TestProposalFloodTakesOnePerRoundUpToTheNext(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	node, recorder := network.node(self)
	stored := len(node.blocks)
	for i := 1; i <= 10; i++ { // ten blocks for round 1, the node being at round 0
		proposal := network.proposal(node, 1, 1, network.payment(self, 1, strconv.Itoa(i)))
		node.handle(event{kind: eventProposal, proposal: &proposal})
	}
	for i, round := range []int32{2, 3, 257, 100000} {
		proposal := network.proposal(node, 1, round, network.payment(self, 1, strconv.Itoa(20+i)))
		node.handle(event{kind: eventProposal, proposal: &proposal})
	}
	if got := recorder.sent(MessageProposal); got != 1 {
		t.Fatalf("the node relayed %d of 14 proposals, want only the first of round 1", got)
	}
	if got := len(node.blocks) - stored; got != 1 {
		t.Fatalf("the node stored %d of 14 proposed blocks, want only the first of round 1", got)
	}
}

// A proposal whose block does not apply is not held, so taking the first proposal of a round does not
// bound it: the round's proposer could send one again and again, and each copy would cost every node an
// ApplyBlock. Once maxInvalidProposals authentic copies of a round have failed, a node checks no further
// proposal for that round, not even a valid one; the next round is not affected.
func TestInvalidProposalsEndTheirRound(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	node, recorder := network.node(self)
	genuine := network.proposal(node, 1, 0, network.payment(self, 1, "5"))
	invalidBlock := genuine.Block
	invalidBlock.StateDigest[0]++ // its state digest is not the digest of its result: it does not apply
	invalid := network.sign(network.proposer(1, 0), 1, 0, invalidBlock)
	for i := 0; i < 5; i++ {
		node.handle(event{kind: eventProposal, proposal: &invalid})
	}
	node.handle(event{kind: eventProposal, proposal: &genuine})
	if recorder.sent(MessageProposal) != 0 || len(node.heightProposals) != 0 {
		t.Fatal("the node still checked a proposal for a round after its proposer's blocks had failed again and again")
	}
	next := network.proposal(node, 1, 1, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &next})
	if recorder.sent(MessageProposal) != 1 {
		t.Fatal("the node did not take the next round's proposal")
	}
}

// A proposer that signs two different blocks for one round equivocates. A later proposal is dropped
// like any other after the first, neither applied, stored nor relayed, and the node logs the first
// conflicting pair for its operator once, however often and with however many blocks the proposer
// follows up.
func TestProposerEquivocationIsDroppedAndLoggedOnce(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	node, recorder := network.node(self)
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)

	first := network.proposal(node, 1, 0, ledger.BlockContent{})
	second := network.proposal(node, 1, 0, network.payment(self, 1, "5"))
	third := network.proposal(node, 1, 0, network.payment(self, 1, "6"))
	for _, proposal := range []ProposalMessage{first, second, second, third} {
		node.handle(event{kind: eventProposal, proposal: &proposal})
	}
	if got := recorder.sent(MessageProposal); got != 1 {
		t.Fatalf("the node relayed %d proposals for round 0, want only the first", got)
	}
	if _, stored := node.blocks[second.Proposal.BlockHash]; stored {
		t.Fatal("the node applied and stored the proposer's second block for the round")
	}
	logOutput := logged.String()
	if strings.Count(logOutput, "\n") != 1 || !strings.Contains(logOutput, first.Proposal.Proposer) ||
		!strings.Contains(logOutput, fmt.Sprintf("%x", first.Proposal.BlockHash)) || !strings.Contains(logOutput, fmt.Sprintf("%x", second.Proposal.BlockHash)) {
		t.Fatalf("want one log line naming the proposer and both blocks, got %q", logOutput)
	}
}

// Two blocks that differ only in their nonce are two blocks (spec 5.1), though they share the state
// digest: a proposer that signs both for one round has signed two proposals for it, and the node
// reports the pair as it reports any other two blocks.
func TestNonceOnlyDifferenceIsReportedAsEquivocation(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	node, _ := network.node(self)
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)

	first := network.proposal(node, 1, 0, ledger.BlockContent{})
	second := network.sign(network.proposer(1, 0), 1, 0, ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{1}, ledger.BlockContent{}))
	if first.Block.StateDigest != second.Block.StateDigest || first.Proposal.BlockHash == second.Proposal.BlockHash {
		t.Fatal("premise: the two blocks differ only in their nonce")
	}
	for _, proposal := range []ProposalMessage{first, second} {
		node.handle(event{kind: eventProposal, proposal: &proposal})
	}
	if logOutput := logged.String(); !strings.Contains(logOutput, "proposal equivocation") ||
		!strings.Contains(logOutput, fmt.Sprintf("%x", first.Proposal.BlockHash)) || !strings.Contains(logOutput, fmt.Sprintf("%x", second.Proposal.BlockHash)) {
		t.Fatalf("the two blocks that differ only in their nonce were not reported: %q", logOutput)
	}
}

// The proposal signature covers the block hash, and so the whole block (E15): a copy of a genuine
// proposal with another body is not authentic, whether that body would apply or not. However many such
// copies come first, none is taken, relayed or counted against the round, so when the genuine copy
// comes the node prevotes it and, with the others' votes, decides the height.
func TestSwappedBodyIsNotAuthenticAndTheGenuineCopyStillCounts(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	node, recorder := network.node(self)
	genuine := network.proposal(node, 1, 0, network.payment(self, 1, "5"))
	validBody := ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{}, ledger.BlockContent{}) // a block that applies
	invalidBody := genuine.Block
	invalidBody.Content.Payments = nil // another content under the genuine state digest: it does not apply
	for _, body := range []ledger.Block{validBody, invalidBody} {
		forged := genuine
		forged.Block = body
		for i := 0; i <= maxInvalidProposals; i++ {
			node.handle(event{kind: eventProposal, proposal: &forged})
		}
	}
	if recorder.sent(MessageProposal) != 0 || len(node.heightProposals) != 0 {
		t.Fatal("the node took or relayed a copy whose body its signature does not cover")
	}
	node.handle(event{kind: eventProposal, proposal: &genuine})
	if prevotes := recorder.votes(network.name(self), 1, 0, consensus.Prevote); len(prevotes) != 1 || prevotes[0].BlockHash != genuine.Proposal.BlockHash {
		t.Fatalf("the node did not prevote the genuine proposal: %v", prevotes)
	}
	network.voteAll(node, self, 1, 0, consensus.Prevote, genuine.Proposal.BlockHash)
	network.voteAll(node, self, 1, 0, consensus.Precommit, genuine.Proposal.BlockHash)
	if node.height() != 2 || balance(node.state.Accounts, network.name(self)).Cmp(rational("95")) != 0 {
		t.Fatal("the node did not commit the genuine block")
	}
}

// A copy of a genuine proposal with another body that comes after the node took a block, a re-gossiped
// copy of that very proposal or one of an earlier round, never replaces the block the node holds: the
// node decides the later round and commits its block.
func TestForgedCopyCannotReplaceAStoredBlock(t *testing.T) {
	network := newHandNet(t, 4)
	self := 0
	for self == network.proposer(1, 0) || self == network.proposer(1, 1) {
		self++
	}
	node, _ := network.node(self)
	round0 := network.proposal(node, 1, 0, ledger.BlockContent{}) // lost on its way to the node
	round1 := network.proposal(node, 1, 1, network.payment(self, 1, "5"))
	node.handle(event{kind: eventTimeout, step: consensus.StepPropose, round: 0, height: 1})
	network.voteAll(node, self, 1, 0, consensus.Precommit, [32]byte{})
	node.handle(event{kind: eventTimeout, step: consensus.StepPrecommit, round: 0, height: 1})
	if node.driver.Round() != 1 {
		t.Fatal("premise: the node moved on to round 1")
	}

	node.handle(event{kind: eventProposal, proposal: &round1})
	swappedRound1, swappedRound0 := round1, round0 // each round's genuine signature, with the other round's block
	swappedRound1.Block, swappedRound0.Block = round0.Block, round1.Block
	for _, forged := range []ProposalMessage{swappedRound1, swappedRound0} {
		node.handle(event{kind: eventProposal, proposal: &forged})
	}
	network.voteAll(node, self, 1, 1, consensus.Precommit, round1.Proposal.BlockHash)
	if node.height() != 2 || balance(node.state.Accounts, network.name(self)).Cmp(rational("95")) != 0 {
		t.Fatal("the node decided round 1 but could not commit its block")
	}
}

// A node takes a proposal only from the round's proposer, under the key the set holds for that name
// (spec 4.11 steps 4 and 5). Another validator's proposal is dropped whether it gives its own name or
// the round proposer's: the signature covers the name, and the key is looked up by it. Neither is held,
// so the round proposer's own proposal is still taken.
func TestProposalIsTakenOnlyFromTheRoundProposer(t *testing.T) {
	network := newHandNet(t, 3)
	self, scheduled := network.nonProposer(1), network.proposer(1, 0)
	var other int // the third validator: neither this node nor the round proposer
	for i := range network.names {
		if i != self && i != scheduled {
			other = i
		}
	}
	node, recorder := network.node(self)
	block := ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{}, ledger.BlockContent{})
	underItsOwnName := network.sign(other, 1, 0, block)
	relabelled := proposalOf(network.signers[other], network.name(scheduled), 1, 0, block)
	for _, proposal := range []ProposalMessage{underItsOwnName, relabelled} {
		node.handle(event{kind: eventProposal, proposal: &proposal})
		if recorder.sent(MessageProposal) != 0 || len(node.heightProposals) != 0 {
			t.Fatalf("the node took a proposal %s signed under the name %s, though %s proposes the round", network.name(other), proposal.Proposal.Proposer, network.name(scheduled))
		}
	}
	genuine := network.sign(scheduled, 1, 0, block)
	node.handle(event{kind: eventProposal, proposal: &genuine})
	if recorder.sent(MessageProposal) != 1 {
		t.Fatal("the node did not take the round proposer's own proposal after the others")
	}
}
