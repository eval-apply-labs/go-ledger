package ledger

import (
	"fmt"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// evidenceFixture is a state at height 4 on a chain that seats v0 to v3 (power 1 each), each with a
// balance of 100, beside "outsider", with 10, which holds no seat.
func evidenceFixture() (Chain, LedgerState) {
	state := buildLedgerState(4, []Account{namedAccount("v0", "100"), namedAccount("v1", "100"), namedAccount("v2", "100"),
		namedAccount("v3", "100"), namedAccount("outsider", "10")}, rational("0.1"))
	return testChain("v0", "v1", "v2", "v3"), state
}

// doubleSign is evidence that the test account `name` signed two precommits for different block hashes
// at (height, 0), each vote naming `name` and signed under the key testSigner(signerName) holds.
func doubleSign(name, signerName string, height int64) consensus.VoteEvidence {
	var firstHash, secondHash [32]byte
	firstHash[0], secondHash[0] = 0x11, 0x22
	firstVote, _ := consensus.SignVote(testSigner(signerName), consensus.Vote{Height: height, Round: 0, Type: consensus.Precommit, BlockHash: firstHash, Validator: name})
	secondVote, _ := consensus.SignVote(testSigner(signerName), consensus.Vote{Height: height, Round: 0, Type: consensus.Precommit, BlockHash: secondHash, Validator: name})
	return consensus.VoteEvidence{Name: name, First: firstVote, Second: secondVote}
}

// signedProposalOf is the proposal of the block with blockHash for (height, 0), naming `name` as its
// proposer and signed under the key testSigner(signerName) holds.
func signedProposalOf(name, signerName string, height int64, blockHash [32]byte) consensus.SignedProposal {
	proposal, _ := consensus.SignProposal(testSigner(signerName), consensus.SignedProposal{Height: height, Round: 0, ValidRound: -1, BlockHash: blockHash, Proposer: name})
	return proposal
}

// proposalDoubleSign is evidence that the test account `name` signed two proposals for different blocks
// at (height, 0), under the key testSigner(signerName) holds.
func proposalDoubleSign(name, signerName string, height int64) consensus.ProposalEvidence {
	return consensus.ProposalEvidence{Name: name, First: signedProposalOf(name, signerName, height, [32]byte{0x11}),
		Second: signedProposalOf(name, signerName, height, [32]byte{0x22})}
}

// voteEvidence is a block content of vote evidence.
func voteEvidence(items ...consensus.VoteEvidence) BlockContent {
	return BlockContent{VoteEvidence: items}
}

// carriedNames is the names a block's evidence is about, vote evidence first.
func carriedNames(block Block) []string {
	var names []string
	for _, evidence := range block.Content.VoteEvidence {
		names = append(names, evidence.Name)
	}
	for _, evidence := range block.Content.ProposalEvidence {
		names = append(names, evidence.Name)
	}
	return names
}

// A block carrying a double-sign freezes the signer's name and leaves its balance alone (R5): the name
// leaves the validator set, in static mode too, and the ⅔ decide about the money. The name is locked in
// that block (R6), so its payment there waits. Evidence is checked under the key the pre-block state holds
// for its name, so an honest validator cannot be framed, by votes naming it under another key or by
// another's votes. A name frozen already takes no more evidence.
func TestDoubleSignFreezesTheNameAndKeepsTheBalance(t *testing.T) {
	chain, previousState := evidenceFixture()
	evidence := doubleSign("v3", "v3", 3)
	payment := signedPayment("v3", "v0", "5", 5)
	block, next := nextBlock(t, chain, previousState, BlockContent{VoteEvidence: []consensus.VoteEvidence{evidence}, Payments: []Payment{payment}})
	if len(block.Content.VoteEvidence) != 1 || len(block.Content.Payments) != 0 {
		t.Fatalf("the block carries %d evidence items and %d payments, want the evidence only", len(block.Content.VoteEvidence), len(block.Content.Payments))
	}
	if cheat := accountOf(t, next, "v3"); !cheat.Frozen || cheat.Amount.Cmp(rational("100")) != 0 {
		t.Fatalf("the double-signer should be frozen with its balance of 100, got %+v", cheat)
	}
	if chain.ValidatorRule.SetFor(next).Contains("v3") {
		t.Fatal("the frozen name is still seated")
	}
	again := voteEvidence(doubleSign("v3", "v3", 4))
	if block := ProposeBlock(chain, next, zeroHash, zeroHash, again); len(block.Content.VoteEvidence) != 0 {
		t.Fatal("ProposeBlock carried evidence against a name frozen already")
	}
	refuses(t, chain, next, again, "frozen already")
	refuses(t, chain, previousState, BlockContent{VoteEvidence: []consensus.VoteEvidence{evidence}, Payments: []Payment{payment}}, "rule 18")

	for name, frame := range map[string]consensus.VoteEvidence{
		"votes naming v0, signed under v3's key": doubleSign("v0", "v3", 3),
		"v3's votes, claimed to be v0's":         {Name: "v0", First: evidence.First, Second: evidence.Second},
	} {
		if block := ProposeBlock(chain, previousState, zeroHash, zeroHash, voteEvidence(frame)); len(block.Content.VoteEvidence) != 0 {
			t.Errorf("%s: ProposeBlock carried the forged evidence", name)
		}
		refuses(t, chain, previousState, voteEvidence(frame), "not a double-sign")
	}
}

// Evidence is checked under its name's current key, and nothing else: once the ⅔ re-key the name, a
// double-sign under its old key is moot, as vote evidence and as proposal evidence.
func TestEvidenceUnderAReplacedKeyIsMoot(t *testing.T) {
	chain, previousState := evidenceFixture()
	_, rekeyed := nextBlock(t, chain, previousState, BlockContent{Reassignment: votedAction(rekeyedTo("v3-new", Reassignment{Name: "v3"}), "v0", "v1", "v2")})
	if accountOf(t, rekeyed, "v3").PublicKey != testSigner("v3-new").PublicKeyBase64() {
		t.Fatal("premise: the ⅔ re-keyed v3")
	}
	for kind, content := range map[string]BlockContent{
		"vote evidence":     voteEvidence(doubleSign("v3", "v3", 3)),
		"proposal evidence": {ProposalEvidence: []consensus.ProposalEvidence{proposalDoubleSign("v3", "v3", 3)}},
	} {
		if block := ProposeBlock(chain, rekeyed, zeroHash, zeroHash, content); len(carriedNames(block)) != 0 {
			t.Errorf("%s under the replaced key: ProposeBlock carried it", kind)
		}
		refuses(t, chain, rekeyed, content, "not a double-sign")
	}
}

// Evidence has no age limit: a double-sign thousands of blocks back still freezes the name while its key
// is current. It must only be below the block that carries it.
func TestEvidenceHasNoAgeLimit(t *testing.T) {
	chain, previousState := evidenceFixture()
	previousState.Height = 100_000
	if _, next := nextBlock(t, chain, previousState, voteEvidence(doubleSign("v3", "v3", 3))); !accountOf(t, next, "v3").Frozen {
		t.Fatal("a double-sign 99,997 blocks back did not freeze the name")
	}
	atThisBlock := voteEvidence(doubleSign("v3", "v3", previousState.Height+1))
	if block := ProposeBlock(chain, previousState, zeroHash, zeroHash, atThisBlock); len(block.Content.VoteEvidence) != 0 {
		t.Fatal("ProposeBlock carried a double-sign at the block's own height")
	}
	refuses(t, chain, previousState, atThisBlock, "not below this block's height")
}

// A proposer that signed two proposals for one height and round, for different blocks, is frozen like a
// double-signing voter, and keeps its balance. Proposals signed under another key frame nobody.
func TestProposalEvidenceFreezes(t *testing.T) {
	chain, previousState := evidenceFixture()
	evidence := proposalDoubleSign("v3", "v3", 3)
	block, next := nextBlock(t, chain, previousState, BlockContent{ProposalEvidence: []consensus.ProposalEvidence{evidence}})
	if len(block.Content.ProposalEvidence) != 1 {
		t.Fatal("the block does not carry the proposal evidence")
	}
	if cheat := accountOf(t, next, "v3"); !cheat.Frozen || cheat.Amount.Cmp(rational("100")) != 0 {
		t.Fatalf("the double-signing proposer should be frozen with its balance of 100, got %+v", cheat)
	}
	framed := BlockContent{ProposalEvidence: []consensus.ProposalEvidence{proposalDoubleSign("v0", "v3", 3)}}
	if block := ProposeBlock(chain, previousState, zeroHash, zeroHash, framed); len(block.Content.ProposalEvidence) != 0 {
		t.Fatal("ProposeBlock carried proposals naming v0 signed under v3's key")
	}
	refuses(t, chain, previousState, framed, "not a double-sign")
}

// Two blocks that differ only in their nonce share their state digest, but they are two blocks (spec
// 5.1): a proposer that signs both for one round has double-signed, and a later block freezes it.
func TestNonceOnlyDifferenceIsProposalEvidence(t *testing.T) {
	chain, previousState := evidenceFixture()
	earlierState := previousState
	earlierState.Height = 2
	first := ProposeBlock(chain, earlierState, zeroHash, [32]byte{1}, BlockContent{})
	second := ProposeBlock(chain, earlierState, zeroHash, [32]byte{2}, BlockContent{})
	if first.StateDigest != second.StateDigest || BlockHash(first) == BlockHash(second) {
		t.Fatal("premise: the two blocks differ only in their nonce")
	}
	evidence := consensus.ProposalEvidence{Name: "v3", First: signedProposalOf("v3", "v3", 3, BlockHash(first)), Second: signedProposalOf("v3", "v3", 3, BlockHash(second))}
	if _, next := nextBlock(t, chain, previousState, BlockContent{ProposalEvidence: []consensus.ProposalEvidence{evidence}}); !accountOf(t, next, "v3").Frozen {
		t.Fatal("the proposals of two blocks that differ only in their nonce did not freeze their proposer")
	}
}

// Evidence needs no seat: a name that signs two conflicting votes is frozen though it holds no seat, and
// its power of 0 counts nothing against the one-third cap.
func TestEvidenceNeedsNoSeat(t *testing.T) {
	chain, previousState := evidenceFixture()
	if chain.ValidatorRule.SetFor(previousState).Contains("outsider") {
		t.Fatal("premise: the outsider holds no seat")
	}
	if _, next := nextBlock(t, chain, previousState, voteEvidence(doubleSign("outsider", "outsider", 3))); !accountOf(t, next, "outsider").Frozen {
		t.Fatal("the double-signer without a seat was not frozen")
	}
}

// The one-third cap (rule 22): evidence that would bring the frozen names to a third or more of the
// power of the seated and frozen names together waits. Power is counted as if no name were frozen, and
// the names frozen before the block count. ProposeBlock adds evidence in name order while the cap holds;
// ApplyBlock refuses a block past it. Holder freezes have no cap.
func TestOneThirdCapHoldsEvidenceBack(t *testing.T) {
	_, previousState := evidenceFixture()
	weighted := testChain()
	weighted.ValidatorRule.StaticValidators = []StaticValidator{{Name: "v0", Power: 4}, {Name: "v1", Power: 1}, {Name: "v2", Power: 1}, {Name: "v3", Power: 1}} // total 7
	for kind, offered := range map[string]BlockContent{
		"vote evidence": voteEvidence(doubleSign("v3", "v3", 3), doubleSign("v2", "v2", 3), doubleSign("v1", "v1", 3)),
		"proposal evidence": {ProposalEvidence: []consensus.ProposalEvidence{
			proposalDoubleSign("v3", "v3", 3), proposalDoubleSign("v2", "v2", 3), proposalDoubleSign("v1", "v1", 3)}},
	} {
		if block, _ := nextBlock(t, weighted, previousState, offered); fmt.Sprint(carriedNames(block)) != "[v1 v2]" {
			t.Fatalf("%s: the block carries evidence against %v, want v1 and v2 (2 of 7), with v3 (3 of 7) waiting", kind, carriedNames(block))
		}
		refuses(t, weighted, previousState, offered, "rule 22")
	}
	_, next := nextBlock(t, weighted, previousState, voteEvidence(doubleSign("v1", "v1", 3), doubleSign("v2", "v2", 3)))

	if block := ProposeBlock(weighted, next, zeroHash, zeroHash, voteEvidence(doubleSign("v3", "v3", 3))); len(block.Content.VoteEvidence) != 0 {
		t.Fatal("evidence against v3 was carried though v1 and v2, frozen before the block, count toward the cap")
	}
	refuses(t, weighted, next, voteEvidence(doubleSign("v3", "v3", 3)), "rule 22")
	// v1 and v2 hold 2 of the 7 that the seated and frozen names hold together; a third name of no power
	// keeps the frozen names under a third of it.
	if _, afterOutsider := nextBlock(t, weighted, next, voteEvidence(doubleSign("outsider", "outsider", 3))); !accountOf(t, afterOutsider, "outsider").Frozen {
		t.Fatal("evidence against a name of no power waited, though the frozen names stay under a third")
	}

	if block := ProposeBlock(testChain("v0", "v1", "v2"), previousState, zeroHash, zeroHash, voteEvidence(doubleSign("v2", "v2", 3))); len(block.Content.VoteEvidence) != 0 {
		t.Fatal("evidence was carried that freezes exactly a third of the power")
	}
	if _, frozenByItself := nextBlock(t, weighted, previousState, requests(freezeOf("v0", 5))); !accountOf(t, frozenByItself, "v0").Frozen {
		t.Fatal("a holder's freeze of 4 of 7 did not land: holder freezes have no cap")
	}
}

// A frozen validator leaves the set with the power delegated to it (R10), and its delegators keep their
// balances and their delegation. The cap still counts that power for it (powerIgnoringFreezes).
func TestFrozenValidatorLeavesTheSetWithItsDelegatedPower(t *testing.T) {
	chain := testChain() // dynamic: any stake seats the name it backs
	cheat, delegator := namedAccount("cheat", "10"), namedAccount("delegator", "50")
	cheat.Delegate, delegator.Delegate = "cheat", "cheat"
	var accounts []Account
	for _, name := range []string{"bank-a", "bank-b", "bank-c"} {
		bank := namedAccount(name, "100")
		bank.Delegate = name
		accounts = append(accounts, bank)
	}
	previousState := buildLedgerState(4, append(accounts, cheat, delegator), rational("0.1"))
	if power := chain.ValidatorRule.SetFor(previousState).Power("cheat"); power != 60 {
		t.Fatalf("premise: cheat is seated with power 60, got %d", power)
	}

	_, next := nextBlock(t, chain, previousState, voteEvidence(doubleSign("cheat", "cheat", 3)))
	validators := chain.ValidatorRule.SetFor(next)
	if validators.Contains("cheat") || validators.TotalPower() != 300 {
		t.Fatalf("the frozen validator should leave the set with its 60, leaving 300, got %d", validators.TotalPower())
	}
	if after := accountOf(t, next, "delegator"); after.Amount.Cmp(rational("50")) != 0 || after.Delegate != "cheat" {
		t.Fatalf("the delegator should keep its balance and its delegation, got %+v", after)
	}
	if power := powerIgnoringFreezes(chain.ValidatorRule, next).Power("cheat"); power != 60 {
		t.Fatalf("the cap should count the frozen validator's 60, got %d", power)
	}
}

// A block carries at most one evidence item per name (rule 8), none against the ⅔ action's name (rule
// 10), and an honest proposer puts at most maxEvidencePerBlock items in one block.
func TestEvidenceIsOnePerNameNoneForTheActionAndEightPerBlock(t *testing.T) {
	chain, previousState := evidenceFixture()
	both := BlockContent{VoteEvidence: []consensus.VoteEvidence{doubleSign("v3", "v3", 3)}, ProposalEvidence: []consensus.ProposalEvidence{proposalDoubleSign("v3", "v3", 3)}}
	if block := ProposeBlock(chain, previousState, zeroHash, zeroHash, both); len(carriedNames(block)) != 1 {
		t.Fatalf("ProposeBlock carried %d items against v3, want one", len(carriedNames(block)))
	}
	refuses(t, chain, previousState, both, "rule 8")

	action := votedAction(Reassignment{Name: "v3", PreviousAmount: rational("100"), NewAmount: rational("90")}, "v0", "v1", "v2")
	withAction := BlockContent{VoteEvidence: []consensus.VoteEvidence{doubleSign("v3", "v3", 3)}, Reassignment: action}
	if block := ProposeBlock(chain, previousState, zeroHash, zeroHash, withAction); block.Content.Reassignment == nil || len(block.Content.VoteEvidence) != 0 {
		t.Fatal("ProposeBlock should carry the ⅔ action and leave out the evidence against its name")
	}
	refuses(t, chain, previousState, withAction, "rule 10")

	accounts, offered := append([]Account(nil), previousState.Accounts...), BlockContent{}
	for i := range maxEvidencePerBlock + 1 {
		name := fmt.Sprintf("outsider-%d", i)
		accounts = append(accounts, namedAccount(name, "1"))
		offered.VoteEvidence = append(offered.VoteEvidence, doubleSign(name, name, 3))
	}
	if block, _ := nextBlock(t, chain, buildLedgerState(4, accounts, rational("0.1")), offered); len(block.Content.VoteEvidence) != maxEvidencePerBlock {
		t.Fatalf("the block carries %d evidence items, want %d", len(block.Content.VoteEvidence), maxEvidencePerBlock)
	}
}

// Evidence that would leave no name seated waits (rule 24, R9): here the only validator's stake is the
// balance of a name that double-signs, which backs no one once frozen (R10). ProposeBlock takes the item
// out, proposal evidence like vote evidence.
func TestEvidenceThatWouldLeaveNoNameSeatedWaits(t *testing.T) {
	chain := testChain() // dynamic
	backer := namedAccount("backer", "10")
	backer.Delegate = "bank"
	previousState := buildLedgerState(4, []Account{namedAccount("bank", "0"), backer}, rational("0.1"))
	for kind, content := range map[string]BlockContent{
		"vote evidence":     voteEvidence(doubleSign("backer", "backer", 3)),
		"proposal evidence": {ProposalEvidence: []consensus.ProposalEvidence{proposalDoubleSign("backer", "backer", 3)}},
	} {
		if block, _ := nextBlock(t, chain, previousState, content); len(carriedNames(block)) != 0 {
			t.Errorf("%s: the block freezes the name whose balance seats the only validator", kind)
		}
		refuses(t, chain, previousState, content, "rule 24")
	}
}
