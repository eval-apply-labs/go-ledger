package node

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// admit runs the payment intake on node without its loop, as the HTTP layer and then the loop would:
// the checks against the committed view, then the loop's against the live state.
func admit(node *Node, payment ledger.Payment) error {
	verifiedKey, err := node.checkPayment(payment)
	if err != nil {
		return err
	}
	_, err = node.admitPayment(payment, verifiedKey)
	return err
}

// A payment's pin names the state the block it names builds on (spec 4.1 step 3, E38). Intake knows
// only the committed state, the one the next block builds on, so it takes a pin to that state's digest
// for the next block, and refuses one for a later block, which no check here could vouch for, and a
// pin to anything else, the accounts digest Phase 1 compared included: none of them would land.
func TestIntakeRefusesAPinForALaterBlock(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("payer", "100"), testAccount("payee", "0")}, 5*time.Second, 0)
	genesis.Height = 4
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil)) // not started: at height 5
	pinned := func(digest [32]byte, blockID int64) ledger.Payment {
		payment, _ := ledger.SignPayment(testSigner("payer"), ledger.Payment{From: "payer", To: "payee", Amount: rational("1"),
			BlockID: blockID, PreviousStateDigest: hex.EncodeToString(digest[:])})
		return payment
	}
	stateDigest, accountsDigest := genesis.Digest(), ledger.AccountsDigest(genesis.Accounts)
	for name, payment := range map[string]ledger.Payment{
		"for a later block":            pinned(stateDigest, 6),
		"to the accounts digest (E38)": pinned(accountsDigest, 5),
	} {
		if err := admit(node, payment); err == nil {
			t.Errorf("intake took a payment pinned %s", name)
		}
	}
	if err := admit(node, pinned(stateDigest, 5)); err != nil {
		t.Fatalf("intake refused a payment pinned to the committed state for the next block: %v", err)
	}
}

// Dynamic admission: a funded payment lifts a new account's stake over the threshold, and it joins
// the validator set the committed state yields — no static config, no explicit admission message.
func TestDynamicValidatorAdmission(t *testing.T) {
	chain := dynamicChain("100") // DYNAMIC — the set derived from the accounts
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), selfDelegated("newcomer", "0")}, 5*time.Second, 20*time.Millisecond)
	node := New(testConfig(chain, "founder", genesis), NewHTTPTransport(nil))
	seated := func() bool {
		return chain.ValidatorRule.SetFor(ledger.LedgerState{Accounts: node.Snapshot().Accounts}).Contains("newcomer")
	}
	if chain.ValidatorRule.SetFor(genesis).Contains("newcomer") {
		t.Fatal("the newcomer should not be a validator at genesis")
	}

	// Pay the newcomer over the threshold (it receives 200 − fee > 100).
	node.SubmitPayment(signedPayment("founder", "newcomer", "200", 1))
	node.Start()
	defer node.Stop()
	waitFor(t, 4*time.Second, seated, "the newcomer was not admitted as a validator after being funded over the threshold")
}

// The governed per-message size cap at intake: an oversized payment is refused with no receipt and
// not gossiped, so one fee can't buy an arbitrarily large permanent ledger entry (see also the
// ledger-level enforcement in ProposeBlock/ApplyBlock, which catches a proposer that bypassed this).
func TestIntakeRefusesOversizedPayment(t *testing.T) {
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000"), testAccount("payee", "0")}, 5*time.Second, 50*time.Millisecond)
	genesis.Parameters.MaxMessageBytes = ledger.MinMessageBytes()
	node := New(testConfig(dynamicChain("100"), "founder", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()

	if _, err := node.AdmitPayment(signedPayment("founder", "payee", "1", MaxBlockIDLookahead)); err != nil {
		t.Fatalf("a plain payment should fit the bound, got: %v", err)
	}
	huge, _ := ledger.SignPayment(testSigner("founder"), ledger.Payment{From: "founder", To: "payee", Amount: rational("1"),
		Comment: strings.Repeat("x", int(4*ledger.MinMessageBytes())), BlockID: MaxBlockIDLookahead})
	if _, err := node.AdmitPayment(huge); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("an oversized payment must be refused at intake, got: %v", err)
	}
}

// Intake refuses a payment naming a block beyond the intake window — the pool evicts a payment only
// once its block has passed, so a pool filled with far-future payments used to refuse every honest
// payment after them — and one below the transaction cost, which the ledger drops without charging
// the fee.
func TestIntakeRefusesFarFutureAndBelowCostPayments(t *testing.T) {
	const poolSize = 8
	genesis := testGenesis([]ledger.Account{selfDelegated("filler", "100"), selfDelegated("honest", "100")}, 5*time.Second, 0)
	config := testConfig(dynamicChain("100"), "validator", genesis)
	config.MaxMempool = poolSize
	node := New(config, NewHTTPTransport(nil)) // not started: the intake runs here as it would, at height 1

	pay := func(from, to, amount string, blockID int64, comment string) ledger.Payment {
		payment, _ := ledger.SignPayment(testSigner(from), ledger.Payment{From: from, To: to, Amount: rational(amount), BlockID: blockID, Comment: comment})
		return payment
	}
	for i := 0; i < poolSize; i++ {
		if err := admit(node, pay("filler", "honest", "0.2", 1<<40, strconv.Itoa(i))); err == nil || !strings.Contains(err.Error(), "too far ahead") {
			t.Fatalf("a payment for a far-future block must be refused, got: %v", err)
		}
	}
	if err := admit(node, pay("filler", "honest", "0.2", 2+MaxBlockIDLookahead, "just past the window")); err == nil {
		t.Fatal("a payment naming a block just past the intake window must be refused")
	}
	if err := admit(node, pay("filler", "honest", "0.09", 2, "below cost")); err == nil || !strings.Contains(err.Error(), "below the transaction cost") {
		t.Fatalf("a payment below the transaction cost must be refused, got: %v", err)
	}
	if err := admit(node, pay("filler", "honest", "0.1", 2, "exactly the cost")); err != nil {
		t.Fatalf("a payment of exactly the cost pays the fee and must be admitted, got: %v", err)
	}
	if err := admit(node, pay("honest", "filler", "5", 1+MaxBlockIDLookahead, "honest")); err != nil {
		t.Fatalf("an honest payment at the edge of the window must be admitted, got: %v", err)
	}
}

// The loop pools a payment or an update only if its signer's key in the live state is still the key its
// signature was checked under off the loop (spec 4.1 step 5, 4.2 step 4): a commit between the two
// checks may have given the name another key, and the old key no longer signs for it.
func TestLoopRechecksTheSignersKey(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "100"), testAccount("bob", "0")}, 5*time.Second, 0)
	// Not started: the loop's intake runs here, at height 1.
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	formerKey := testSigner("mallory").PublicKeyBase64() // stands for a key alice held before
	payment := signedPayment("alice", "bob", "10", 1)
	if _, err := node.admitPayment(payment, formerKey); err == nil || !strings.Contains(err.Error(), "key changed") {
		t.Errorf("the loop took a payment checked under a key that is no longer the payer's: %v", err)
	}
	update := signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "hi", BlockID: 1})
	if _, err := node.admitUpdate(update, formerKey); err == nil || !strings.Contains(err.Error(), "key changed") {
		t.Errorf("the loop took an update checked under a key that is no longer the account's: %v", err)
	}
	if err := admit(node, payment); err != nil {
		t.Errorf("the same payment, checked under alice's current key, was refused: %v", err)
	}
}

// Intake refuses a payment whose comment is longer than the node's MaxCommentLength (spec 4.1 step 2),
// so a flood of valid payments cannot fill the pool, and every peer's, with long comments.
func TestIntakeRefusesAnOverlongComment(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "100"), testAccount("bob", "0")}, 5*time.Second, 0)
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.MaxCommentLength = 8
	node := New(config, NewHTTPTransport(nil)) // not started: the intake runs here, at height 1
	pay := func(comment string) ledger.Payment {
		payment, _ := ledger.SignPayment(testSigner("alice"), ledger.Payment{From: "alice", To: "bob", Amount: rational("1"), Comment: comment, BlockID: 1})
		return payment
	}
	if err := admit(node, pay(strings.Repeat("x", 9))); err == nil || !strings.Contains(err.Error(), "comment too long") {
		t.Errorf("intake took a payment whose comment is longer than MaxCommentLength: %v", err)
	}
	if err := admit(node, pay(strings.Repeat("x", 8))); err != nil {
		t.Errorf("intake refused a payment whose comment is exactly MaxCommentLength: %v", err)
	}
}

// capNetwork is an unstarted single-validator node whose genesis has small caps, with a payer, a payee
// and a newcomer at zero, for the cap tests: the intake runs as it would, at height 1.
type capNetwork struct {
	node  *Node
	chain ledger.Chain
}

func newCapNetwork(t *testing.T, maxPaymentAmount, maxAccountBalance, payeeBalance string) capNetwork {
	t.Helper()
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("payer", "1000"), testAccount("payee", payeeBalance), testAccount("newcomer", "0")}, 5*time.Second, 0)
	genesis.Parameters.MaxPaymentAmount, genesis.Parameters.MaxAccountBalance = rational(maxPaymentAmount), rational(maxAccountBalance)
	chain := staticChain("validator")
	return capNetwork{node: New(testConfig(chain, "validator", genesis), NewHTTPTransport(nil)), chain: chain}
}

// pay is the payer's payment of amount to recipient for block 1; the comment keeps payments of one
// amount apart.
func (network capNetwork) pay(recipient, amount, comment string) ledger.Payment {
	payment, _ := ledger.SignPayment(testSigner("payer"), ledger.Payment{From: "payer", To: recipient, Amount: rational(amount), Comment: comment, BlockID: 1})
	return payment
}

// forge is a block at height 1 carrying payments that the proposer left out, its state digest what
// applying them would give, so that only the rule under test can refuse it.
func (network capNetwork) forge(payments ...ledger.Payment) ledger.Block {
	state := network.node.state
	block := ledger.ProposeBlock(network.chain, state, [32]byte{}, [32]byte{}, ledger.BlockContent{})
	block.Content.Payments = payments
	block.StateDigest = ledger.LedgerState{Accounts: ledger.ProcessPayments(state.Accounts, payments, state.Parameters.TransactionCost),
		Parameters: state.Parameters, Height: 1}.Digest()
	return block
}

// The largest single payment (MaxPaymentAmount) bounds every payment: intake refuses one above it, the
// proposer leaves it out, and a block that carries it does not apply. One of exactly the cap goes.
func TestPaymentAboveTheLargestPaymentIsRefused(t *testing.T) {
	network := newCapNetwork(t, "50", "10000000", "0")
	above, atTheCap := network.pay("payee", "50.00000001", "above"), network.pay("payee", "50", "at the cap")
	if err := admit(network.node, above); err == nil || !strings.Contains(err.Error(), "largest single payment") {
		t.Errorf("intake took a payment above the largest single payment: %v", err)
	}
	if err := admit(network.node, atTheCap); err != nil {
		t.Errorf("intake refused a payment of exactly the largest single payment: %v", err)
	}
	block := ledger.ProposeBlock(network.chain, network.node.state, [32]byte{}, [32]byte{}, ledger.BlockContent{Payments: []ledger.Payment{above, atTheCap}})
	if len(block.Content.Payments) != 1 || block.Content.Payments[0].Comment != "at the cap" {
		t.Errorf("the proposer carried %d payments, want only the one at the cap", len(block.Content.Payments))
	}
	if _, err := ledger.ApplyBlock(network.chain, network.node.state, [32]byte{}, block); err != nil {
		t.Errorf("the proposed block does not apply: %v", err)
	}
	if _, err := ledger.ApplyBlock(network.chain, network.node.state, [32]byte{}, network.forge(above)); err == nil {
		t.Error("a block carrying a payment above the largest single payment applied")
	}
}

// The largest balance (MaxAccountBalance) bounds what a payment may lift its payee to, its credit being
// the amount less the transaction cost. Intake checks one payment against the committed balance, a
// payee at zero included; the proposer keeps a running balance per payee, so of two payments that each
// fit but together pass the cap it carries one; and a block whose payee ends above the cap, having
// risen in it, does not apply (rule 23).
func TestPaymentLiftingAPayeeAboveTheBalanceCapIsRefused(t *testing.T) {
	network := newCapNetwork(t, "100000", "200", "150")
	for _, refused := range []ledger.Payment{network.pay("payee", "50.2", "to 200.1"), network.pay("newcomer", "200.2", "to a new 200.1")} {
		if err := admit(network.node, refused); err == nil || !strings.Contains(err.Error(), "largest balance") {
			t.Errorf("intake took a payment lifting its payee above the largest balance (%s): %v", refused.Comment, err)
		}
	}
	if err := admit(network.node, network.pay("payee", "50.1", "to exactly 200")); err != nil {
		t.Errorf("intake refused a payment lifting its payee to exactly the largest balance: %v", err)
	}

	first, second := network.pay("payee", "30", "first"), network.pay("payee", "30", "second") // 179.9 each, 209.8 together
	block := ledger.ProposeBlock(network.chain, network.node.state, [32]byte{}, [32]byte{}, ledger.BlockContent{Payments: []ledger.Payment{first, second}})
	if len(block.Content.Payments) != 1 {
		t.Fatalf("the proposer carried %d of two payments that together lift the payee above the cap, want 1", len(block.Content.Payments))
	}
	if _, err := ledger.ApplyBlock(network.chain, network.node.state, [32]byte{}, block); err != nil {
		t.Errorf("the proposed block does not apply: %v", err)
	}
	if _, err := ledger.ApplyBlock(network.chain, network.node.state, [32]byte{}, network.forge(first, second)); err == nil {
		t.Error("a block lifting its payee above the largest balance applied")
	}
}
