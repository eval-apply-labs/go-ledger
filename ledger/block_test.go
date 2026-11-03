package ledger

import (
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// forgeBlock is the block at height that carries payments on previousState, with the hashes of the
// signing set and of the next one, and the digest of the state they result in: a block whose only fault
// can be a rule about the payments themselves.
func forgeBlock(previousState LedgerState, height int64, payments ...Payment) Block {
	next := buildLedgerState(height, ProcessPayments(previousState.Accounts, payments, previousState.Parameters.TransactionCost), previousState.Parameters.TransactionCost)
	return Block{Height: height, ValidatorsHash: signingSetHash(testChain(), previousState), NextValidatorsHash: signingSetHash(testChain(), next),
		Content: BlockContent{Payments: payments}, StateDigest: next.Digest()}
}

func TestProposeAndApplyBlockRoundTrip(t *testing.T) {
	previousState := buildLedgerState(0, []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}, rational("0.1"))
	payment := signedPayment("alice", "bob", "10", 1)

	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{payment}})
	next, err := ApplyBlock(testChain(), previousState, zeroHash, block)
	if err != nil {
		t.Fatalf("a well-formed block failed to apply: %v", err)
	}
	if next.Digest() != block.StateDigest {
		t.Fatal("applied state digest mismatch")
	}
	if balance(next.Accounts, "alice").Cmp(rational("90")) != 0 {
		t.Errorf("alice = %s, want 90", balance(next.Accounts, "alice").FloatString(2))
	}
	if balance(next.Accounts, "bob").Cmp(rational("9.9")) != 0 {
		t.Errorf("bob = %s, want 9.9", balance(next.Accounts, "bob").FloatString(2))
	}
}

func TestApplyBlockRejectsTamperedDigest(t *testing.T) {
	previousState := buildLedgerState(0, []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}, rational("0.1"))
	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{signedPayment("alice", "bob", "10", 1)}})
	block.StateDigest[0] ^= 0xFF // tamper with the claimed result
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, block); err == nil {
		t.Fatal("a block with a tampered state digest must not apply")
	}
}

func TestApplyBlockRejectsUnsignedPayment(t *testing.T) {
	previousState := buildLedgerState(0, []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}, rational("0.1"))
	unsigned := Payment{From: "alice", To: "bob", Amount: rational("10"), BlockID: 1}
	// a block whose digest is self-consistent but whose payment is unsigned
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 1, unsigned)); err == nil {
		t.Fatal("a block containing an unsigned payment must not apply")
	}
}

func TestApplyBlockRejectsWrongPreviousState(t *testing.T) {
	accounts := []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}
	block := ProposeBlock(testChain(), buildLedgerState(0, accounts, rational("0.1")), zeroHash, zeroHash, BlockContent{Payments: []Payment{signedPayment("alice", "bob", "10", 1)}})
	other := buildLedgerState(0, []Account{namedAccount("alice", "999"), namedAccount("bob", "0"), seatedAccount()}, rational("0.1"))
	if _, err := ApplyBlock(testChain(), other, zeroHash, block); err == nil {
		t.Fatal("a block must not apply onto a different prior state")
	}
}

// A payment larger than its payer's pre-block balance never enters a block: ProposeBlock skips it and
// ApplyBlock rejects a block a dishonest proposer built with it. One such payment (a typo the payer's
// own reference node refused but kept) used to ban the payer and destroy its whole balance. Same-block
// credits do not count, a name with no account has nothing, and two payments that each fit but
// together overdraw — two signings — still ban.
func TestLoneOverBalancePaymentNeverEntersABlock(t *testing.T) {
	previousState := buildLedgerState(3, []Account{namedAccount("victim", "10"), namedAccount("payee", "50"), seatedAccount()}, rational("0.1"))
	for name, lone := range map[string][]Payment{
		"over the balance":                  {signedPayment("victim", "payee", "100", 4)},
		"over the balance despite a credit": {signedPayment("payee", "victim", "40", 4), signedPayment("victim", "payee", "30", 4)},
		"from a name with no account":       {signedPayment("ghost", "payee", "1", 4)},
	} {
		if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: lone}); len(block.Content.Payments) != len(lone)-1 {
			t.Errorf("%s: ProposeBlock must skip only the over-balance payment, carried %d of %d", name, len(block.Content.Payments), len(lone))
		}
		if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, lone...)); err == nil {
			t.Errorf("%s: ApplyBlock accepted a block carrying it", name)
		}
	}

	pay := func(comment string) Payment {
		payment, _ := SignPayment(testSigner("victim"), Payment{From: "victim", To: "payee", Amount: rational("6"), Comment: comment, BlockID: 4})
		return payment
	}
	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{pay("a"), pay("b")}})
	if next, err := ApplyBlock(testChain(), previousState, zeroHash, block); err != nil || len(block.Content.Payments) != 2 || balance(next.Accounts, "victim").Sign() != 0 {
		t.Fatal("two payments that each fit but together overdraw must still be carried and ban the payer")
	}
}

// A payment below the transaction cost moves nothing (ProcessPayments drops it), so the proposer does
// not carry it as free filler; the cost can have risen since intake admitted it. A payment of exactly
// the cost still burns the fee and is carried.
func TestBelowCostPaymentIsNotCarried(t *testing.T) {
	previousState := buildLedgerState(3, []Account{namedAccount("payer", "10"), namedAccount("payee", "0"), seatedAccount()}, rational("0.5")) // the cost rose from 0.1
	below, atCost := signedPayment("payer", "payee", "0.2", 4), signedPayment("payer", "payee", "0.5", 4)
	block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{below, atCost}})
	if len(block.Content.Payments) != 1 || block.Content.Payments[0].Amount.Cmp(rational("0.5")) != 0 {
		t.Fatalf("want only the payment at the cost carried, got %d payments", len(block.Content.Payments))
	}
}

// Rule 17 holds every payment a block carries to at least the transaction cost (spec 5.3). A payment
// below it moves nothing, so a block that carries one has the digest of a block without it, and only
// this rule refuses it: otherwise a proposer could fill blocks with signed payments that pay no fee.
func TestApplyBlockRefusesAPaymentBelowTheCost(t *testing.T) {
	previousState := buildLedgerState(3, []Account{namedAccount("payer", "10"), namedAccount("payee", "0"), seatedAccount()}, rational("0.5"))
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, signedPayment("payer", "payee", "0.5", 4))); err != nil {
		t.Fatalf("a payment of exactly the cost was refused: %v", err)
	}
	_, err := ApplyBlock(testChain(), previousState, zeroHash, forgeBlock(previousState, 4, signedPayment("payer", "payee", "0.2", 4)))
	if err == nil {
		t.Fatal("ApplyBlock accepted a block carrying a payment below the transaction cost")
	}
	t.Logf("refused as it should be: %v", err)
}

// A block builds on the block before it (spec 5.3 rule 2): ProposeBlock writes the previous block hash
// and the nonce it is given, and ApplyBlock refuses a block that names another previous block, however
// valid its content.
func TestApplyBlockRequiresThePreviousBlockHash(t *testing.T) {
	previousState := buildLedgerState(4, []Account{namedAccount("payer", "100"), namedAccount("payee", "0"), seatedAccount()}, rational("0.1"))
	lastBlockHash, nonce := [32]byte{7}, [32]byte{9}
	block := ProposeBlock(testChain(), previousState, lastBlockHash, nonce, BlockContent{Payments: []Payment{signedPayment("payer", "payee", "10", 5)}})
	if block.PreviousBlockHash != lastBlockHash || block.Nonce != nonce {
		t.Fatal("premise: ProposeBlock writes the previous block hash and the nonce it is given")
	}
	if _, err := ApplyBlock(testChain(), previousState, lastBlockHash, block); err != nil {
		t.Fatalf("the block does not apply on the block it names: %v", err)
	}
	if _, err := ApplyBlock(testChain(), previousState, [32]byte{8}, block); err == nil {
		t.Fatal("a block naming another previous block applied")
	}
}

// A block names the set that signs it, by its hash (spec 5.3 rule 3): the set the pre-block state
// seats, each name with its current key and power. A block naming another set is refused, so votes for
// its hash cannot claim another signing set.
func TestApplyBlockRequiresTheSigningSetHash(t *testing.T) {
	chain := testChain("bank-a", "bank-b")
	previousState := buildLedgerState(0, []Account{namedAccount("bank-a", "0"), namedAccount("bank-b", "0"), namedAccount("payer", "10"), namedAccount("payee", "0")}, rational("0.1"))
	block := ProposeBlock(chain, previousState, zeroHash, zeroHash, BlockContent{Payments: []Payment{signedPayment("payer", "payee", "1", 1)}})
	if block.ValidatorsHash != signingSetHash(chain, previousState) {
		t.Fatal("premise: ProposeBlock names the set the state seats")
	}
	if _, err := ApplyBlock(chain, previousState, zeroHash, block); err != nil {
		t.Fatalf("the block does not apply: %v", err)
	}
	bankA := consensus.Validator{Name: "bank-a", PublicKey: testSigner("bank-a").PublicKeyBase64(), Power: 1}
	for name, otherSet := range map[string][]consensus.Validator{
		"a set without bank-b":     {bankA},
		"bank-b under another key": {bankA, {Name: "bank-b", PublicKey: testSigner("thief").PublicKeyBase64(), Power: 1}},
		"bank-b with more power":   {bankA, {Name: "bank-b", PublicKey: testSigner("bank-b").PublicKeyBase64(), Power: 2}},
	} {
		forged := block
		forged.ValidatorsHash = ValidatorsHash(otherSet)
		if _, err := ApplyBlock(chain, previousState, zeroHash, forged); err == nil {
			t.Errorf("a block naming %s applied", name)
		}
	}
	if _, err := ApplyBlock(testChain("bank-a"), previousState, zeroHash, block); err == nil {
		t.Error("a block applied on a chain whose rule seats another set")
	}
}

// A block names, by its hash, the set that signs the next block (Phase 4 spec §4.3): the set the state
// it results in seats. So block h+1's ValidatorsHash is block h's NextValidatorsHash, in static and in
// dynamic mode, across a block that changes the set too: a static freeze takes a seat away, and a
// dynamic delegation seats a new name. The block hash covers it, so a block's commit is its set signing
// off the next one, and ApplyBlock refuses a block that names any other next set.
func TestEachBlockNamesTheNextSigningSet(t *testing.T) {
	staticChain, staticState := requestFixture()
	dynamicState := buildLedgerState(0, []Account{namedAccount("alice", "100"), namedAccount("bob", "0"), seatedAccount()}, rational("0.1"))
	for _, testCase := range []struct {
		mode     string
		chain    Chain
		state    LedgerState
		contents []BlockContent // the second changes the set
	}{
		{"static", staticChain, staticState, []BlockContent{
			{Payments: []Payment{signedPayment("alice", "bob", "1", requestBlock)}},
			requests(freezeOf("bank-b", requestBlock+1)),
			{},
		}},
		{"dynamic", testChain(), dynamicState, []BlockContent{
			{Payments: []Payment{signedPayment("alice", "bob", "10", 1)}},
			{AccountUpdates: []AccountUpdate{signedUpdate(AccountUpdate{Name: "alice", Delegate: "bob", BlockID: 2})}},
			{},
		}},
	} {
		t.Run(testCase.mode, func(t *testing.T) {
			state := testCase.state
			var blocks []Block
			var statesBefore []LedgerState
			for _, content := range testCase.contents {
				statesBefore = append(statesBefore, state)
				var block Block
				block, state = nextBlock(t, testCase.chain, state, content)
				blocks = append(blocks, block)
			}
			if blocks[1].NextValidatorsHash == blocks[1].ValidatorsHash || blocks[0].NextValidatorsHash != blocks[0].ValidatorsHash {
				t.Fatal("premise: the second block, and only it, changes the set")
			}
			for i := 0; i+1 < len(blocks); i++ {
				if blocks[i+1].ValidatorsHash != blocks[i].NextValidatorsHash {
					t.Errorf("block %d is signed by another set than the one block %d named as the next", blocks[i+1].Height, blocks[i].Height)
				}
			}
			for name, nextValidatorsHash := range map[string][32]byte{"the set before the change": blocks[1].ValidatorsHash, "no set": zeroHash} {
				forged := blocks[1]
				forged.NextValidatorsHash = nextValidatorsHash
				if _, err := ApplyBlock(testCase.chain, statesBefore[1], zeroHash, forged); err == nil || !strings.Contains(err.Error(), "next signing set") {
					t.Errorf("a block naming %s as the next applied, or was refused for another reason: %v", name, err)
				}
			}
		})
	}
}
