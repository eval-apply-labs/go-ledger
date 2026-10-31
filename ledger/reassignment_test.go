package ledger

import (
	"strconv"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// reassignmentBy is a ⅔ action setting the balance of the account `name` from previousAmount to
// newAmount at the governance sequence, with a vote from each of the test validators `voters`.
func reassignmentBy(name, previousAmount, newAmount string, sequence int64, voters ...string) *Reassignment {
	return votedAction(Reassignment{Name: name, PreviousAmount: rational(previousAmount), NewAmount: rational(newAmount), Sequence: sequence}, voters...)
}

// votedAction is action with a vote from each of the test validators `voters`.
func votedAction(action Reassignment, voters ...string) *Reassignment {
	for _, voter := range voters {
		vote, err := SignReassignmentVote(testSigner(voter), voter, action)
		if err != nil {
			panic(err)
		}
		action.Votes = append(action.Votes, vote)
	}
	return &action
}

// rekeyedTo is action with the new key of the test name keyHolder and that key's proof of possession,
// which covers every other field, so the votes come after it.
func rekeyedTo(keyHolder string, action Reassignment) Reassignment {
	action.NewKey = testSigner(keyHolder).PublicKeyBase64()
	signed, err := SignReassignmentNewKey(testSigner(keyHolder), action)
	if err != nil {
		panic(err)
	}
	return signed
}

// A balance reassignment is ratified only by a > ⅔ stake quorum, applies to committed state (it may
// mint or reduce), rejects a negative target, and cannot be replayed once the balance has moved on.
func TestReassignmentRequiresTwoThirds(t *testing.T) {
	chain, validators := testChain("v0", "v1", "v2"), testValidators("v0", "v1", "v2") // total 3 → quorum 3
	previousState := buildLedgerState(0, []Account{namedAccount("v0", "100"), namedAccount("v1", "0"), namedAccount("v2", "0"), namedAccount("coop", "10")}, rational("0.1"))

	// All three approve: valid, and applied in a block the balance is overridden (a mint: 10 to 250).
	ratified := reassignmentBy("coop", "10", "250", 0, "v0", "v1", "v2") // genesis governance sequence 0
	if !ratified.Valid(signing.MLDSA65{}, testNetwork, validators, previousState) {
		t.Fatal("a reassignment approved by all validators should be ratified")
	}
	block := ProposeBlock(chain, previousState, zeroHash, zeroHash, BlockContent{Reassignment: ratified})
	nextState, err := ApplyBlock(chain, previousState, zeroHash, block)
	if err != nil {
		t.Fatalf("a block with a ratified reassignment should apply: %v", err)
	}
	if AccountAmount(nextState.Accounts, "coop").Cmp(rational("250")) != 0 {
		t.Fatalf("balance not overridden: got %s", FormatAmount(AccountAmount(nextState.Accounts, "coop")))
	}
	// The previous state was not mutated (copy-on-write).
	if AccountAmount(previousState.Accounts, "coop").Cmp(rational("10")) != 0 {
		t.Fatal("applying a reassignment must not mutate the previous state")
	}

	// Only one of three approves (< quorum): not ratified, and a block carrying it is rejected.
	subQuorum := reassignmentBy("coop", "10", "250", 0, "v0")
	if subQuorum.Valid(signing.MLDSA65{}, testNetwork, validators, previousState) {
		t.Fatal("a sub-⅔ reassignment must not be ratified")
	}
	rejected := block
	rejected.Content.Reassignment = subQuorum
	if _, err := ApplyBlock(chain, previousState, zeroHash, rejected); err == nil {
		t.Fatal("ApplyBlock must reject a block whose reassignment lacks a ⅔ quorum")
	}

	// A vote counts only under the key the set holds for its name: v2's name on a vote v0's key signed
	// does not complete the quorum.
	misattributed, _ := SignReassignmentVote(testSigner("v0"), "v2", Reassignment{Name: "coop", PreviousAmount: rational("10"), NewAmount: rational("250")})
	forged := reassignmentBy("coop", "10", "250", 0, "v0", "v1")
	forged.Votes = append(forged.Votes, misattributed)
	if forged.Valid(signing.MLDSA65{}, testNetwork, validators, previousState) {
		t.Fatal("a vote naming v2 but signed under v0's key was counted")
	}

	// All or nothing: a full quorum with one bad vote in front of it is not ratified (spec 4.8 rule 1).
	padded := reassignmentBy("coop", "10", "250", 0, "v0", "v1", "v2")
	padded.Votes = append([]ReassignmentVote{misattributed}, padded.Votes...)
	if padded.Valid(signing.MLDSA65{}, testNetwork, validators, previousState) {
		t.Fatal("a reassignment with a full quorum and one bad vote was ratified: one bad vote must make it invalid")
	}

	// Non-negative guard: a negative target amount is never valid.
	if reassignmentBy("coop", "10", "-5", 0, "v0", "v1", "v2").Valid(signing.MLDSA65{}, testNetwork, validators, previousState) {
		t.Fatal("a reassignment to a negative balance must be rejected")
	}

	// Replay guard: once the balance has moved on, the certificate no longer amends it.
	movedOn := buildLedgerState(0, []Account{namedAccount("coop", "11")}, rational("0.1"))
	if ratified.Valid(signing.MLDSA65{}, testNetwork, validators, movedOn) {
		t.Fatal("a certificate must not re-apply against a balance it did not amend (replay guard)")
	}
}

// The ⅔ action can create a name no account has (spec 4.8 rule 6): with a key, proven by its proof of
// possession, and a balance, and optionally a bank of record. Without a key or a balance it creates
// nothing, and it can neither freeze nor unfreeze a name that does not exist.
func TestNameActionCreatesANewName(t *testing.T) {
	chain, state := requestFixture()
	creation := rekeyedTo("newcomer-key", Reassignment{Name: "newcomer", PreviousAmount: rational("0"), NewAmount: rational("50"), Bank: "bank-a"})
	_, next := nextBlock(t, chain, state, BlockContent{Reassignment: votedAction(creation, "bank-a", "bank-b")})
	newcomer := accountOf(t, next, "newcomer")
	if newcomer.PublicKey != testSigner("newcomer-key").PublicKeyBase64() || newcomer.Amount.Cmp(rational("50")) != 0 || newcomer.Bank != "bank-a" || !next.NamesAscending() {
		t.Errorf("the created account is %+v, want the new key, 50 and bank-a, in name order", newcomer)
	}
	wrongProof := creation
	wrongProof.NewKeySignature = rekeyedTo("mallory", creation).NewKeySignature
	for name, action := range map[string]Reassignment{
		"without a key":            {Name: "newcomer", PreviousAmount: rational("0"), NewAmount: rational("50")},
		"without a balance":        rekeyedTo("newcomer-key", Reassignment{Name: "newcomer"}),
		"frozen at birth":          rekeyedTo("newcomer-key", Reassignment{Name: "newcomer", PreviousAmount: rational("0"), NewAmount: rational("50"), Freeze: true}),
		"under a name in capitals": rekeyedTo("newcomer-key", Reassignment{Name: "Newcomer", PreviousAmount: rational("0"), NewAmount: rational("50")}),
		"with another key's proof": wrongProof,
	} {
		if votedAction(action, "bank-a", "bank-b").Valid(chain.Scheme, chain.NetworkID, chain.ValidatorRule.SetFor(state), state) {
			t.Errorf("an action creating a name %s was valid", name)
		}
	}
}

// The ⅔ re-key a frozen name and unfreeze it in one action: the name gets the new key, is no longer
// frozen, and keeps its balance; the governance sequence moves on.
func TestNameActionRekeysAndUnfreezes(t *testing.T) {
	chain, state := requestFixture()
	_, frozen := nextBlock(t, chain, state, requests(freezeOf("alice", requestBlock)))
	recovery := votedAction(rekeyedTo("alice-recovered", Reassignment{Name: "alice", Unfreeze: true, Sequence: frozen.GovernanceSequence}), "bank-a", "bank-b")
	_, recovered := nextBlock(t, chain, frozen, BlockContent{Reassignment: recovery})
	alice := accountOf(t, recovered, "alice")
	if alice.Frozen || alice.PublicKey != testSigner("alice-recovered").PublicKeyBase64() || alice.Amount.Cmp(rational("10")) != 0 || recovered.GovernanceSequence != frozen.GovernanceSequence+1 {
		t.Errorf("after the action alice is %+v at sequence %d: want unfrozen, re-keyed, her balance kept and the sequence moved on", alice, recovered.GovernanceSequence)
	}
}

// Every unfreeze re-keys, to a key no name holds, and a frozen name's key changes only with an
// unfreeze (spec 4.8 rules 2, 4, 7 and 8): an unfreeze without a new key, an unfreeze to another
// name's key, a new key for a frozen name without an unfreeze, an unfreeze of a name that is not
// frozen, and a freeze together with an unfreeze or a new key are all invalid.
func TestUnfreezeRequiresANewKey(t *testing.T) {
	chain, state := requestFixture()
	_, frozen := nextBlock(t, chain, state, requests(freezeOf("alice", requestBlock)))
	for _, testCase := range []struct {
		name   string
		state  LedgerState
		action Reassignment
	}{
		{"an unfreeze without a new key", frozen, Reassignment{Name: "alice", Unfreeze: true}},
		{"an unfreeze to bob's key", frozen, rekeyedTo("bob", Reassignment{Name: "alice", Unfreeze: true})},
		{"a frozen name's new key without an unfreeze", frozen, rekeyedTo("alice-recovered", Reassignment{Name: "alice"})},
		{"an unfreeze of a name that is not frozen", state, rekeyedTo("alice-recovered", Reassignment{Name: "alice", Unfreeze: true})},
		{"a freeze with an unfreeze", frozen, rekeyedTo("alice-recovered", Reassignment{Name: "alice", Freeze: true, Unfreeze: true})},
		{"a freeze with a new key", state, rekeyedTo("alice-recovered", Reassignment{Name: "alice", Freeze: true})},
	} {
		if votedAction(testCase.action, "bank-a", "bank-b").Valid(chain.Scheme, chain.NetworkID, chain.ValidatorRule.SetFor(testCase.state), testCase.state) {
			t.Errorf("%s was valid", testCase.name)
		}
	}
}

// The ⅔ set a name's bank of record, or clear it with "-" (Q23); "" leaves it. The bank must be another
// name that exists and is not frozen.
func TestNameActionCanClearTheBank(t *testing.T) {
	chain, state := requestFixture()
	for bank, want := range map[string]string{"-": "", "": "bank-a", "bank-b": "bank-b"} {
		_, next := nextBlock(t, chain, state, BlockContent{Reassignment: votedAction(Reassignment{Name: "alice", Bank: bank}, "bank-a", "bank-b")})
		if got := accountOf(t, next, "alice").Bank; got != want {
			t.Errorf("an action with bank %q left alice's bank of record %q, want %q", bank, got, want)
		}
	}
	_, frozen := nextBlock(t, chain, state, requests(freezeOf("bob", requestBlock)))
	for name, bank := range map[string]string{"the name itself": "alice", "a frozen name": "bob", "no name at all": "nobody"} {
		if votedAction(Reassignment{Name: "alice", Bank: bank}, "bank-a", "bank-b").Valid(chain.Scheme, chain.NetworkID, chain.ValidatorRule.SetFor(frozen), frozen) {
			t.Errorf("an action making %s alice's bank was valid", name)
		}
	}
}

// The ⅔ action wins its block (R6): its name requests nothing and is frozen by no request in the
// block, and nothing names it as requestor or bank. The proposer leaves those requests out, and a
// block that carries one is refused.
func TestNameActionWinsOverASameBlockRequest(t *testing.T) {
	chain, state := requestFixture()
	action := votedAction(Reassignment{Name: "alice", PreviousAmount: rational("10"), NewAmount: rational("0")}, "bank-a", "bank-b")
	for name, request := range map[string]NameRequest{
		"its key change":   keyChangeOf("alice", "bank-a", "alice-new", requestBlock),
		"its freeze":       freezeOf("alice", requestBlock),
		"its registration": registrationOf("dave", "alice", "bank-a", "dave-key"),
	} {
		content := BlockContent{Reassignment: action, NameRequests: []NameRequest{request}}
		if block, _ := nextBlock(t, chain, state, content); block.Content.Reassignment == nil || len(block.Content.NameRequests) != 0 {
			t.Errorf("%s: want the action carried and the request left out", name)
		}
		t.Run(name, func(t *testing.T) { refuses(t, chain, state, content, "rule") })
	}
}

// The ⅔ action clears any pending change of its name (R7), so its decision is not overwritten when
// the change falls due. The fee stays burned.
func TestNameActionClearsThePendingChange(t *testing.T) {
	chain, state := requestFixture()
	_, filed := nextBlock(t, chain, state, requests(keyChangeOf("alice", "bank-a", "thief-key", requestBlock)))
	_, acted := nextBlock(t, chain, filed, BlockContent{Reassignment: votedAction(Reassignment{Name: "alice", Sequence: filed.GovernanceSequence}, "bank-a", "bank-b")})
	if alice := accountOf(t, acted, "alice"); alice.PendingKey != "" || alice.PendingHeight != 0 || alice.PublicKey != testSigner("alice").PublicKeyBase64() {
		t.Errorf("after the action alice is %+v: want her change gone and her key kept", alice)
	}
}

// Within a block, freezes and the ⅔ action apply before the changes that fall due (spec 5.3 step I).
// So a bank's freeze clears the change it co-signed even when that change falls due in the same block
// (R11), and a change to the key the action gives another name lapses rather than bind one key to two
// names.
func TestInBlockOrderIsFreezesThenActionThenDueChanges(t *testing.T) {
	chain, state := requestFixture()
	carol := namedAccount("carol", "10")
	carol.Bank = "bank-b"
	state = buildLedgerState(requestBlock-1, append(append([]Account(nil), state.Accounts[:4]...), carol), rational("0.1")) // alice, bank-a, bank-b, bob
	state.Parameters.ChangeDelayBlocks = 1
	_, filed := nextBlock(t, chain, state, requests(
		keyChangeOf("carol", "bank-b", "carol-new", requestBlock), // co-signed by bank-b, frozen below
		keyChangeOf("bob", "bank-a", "shared-key", requestBlock),  // to the key the action gives alice
	))
	action := votedAction(rekeyedTo("shared-key", Reassignment{Name: "alice", Sequence: filed.GovernanceSequence}), "bank-a", "bank-b")
	block, due := nextBlock(t, chain, filed, BlockContent{Reassignment: action, NameRequests: []NameRequest{freezeOf("bank-b", requestBlock+1)}})
	if block.Content.Reassignment == nil || len(block.Content.NameRequests) != 1 {
		t.Fatal("premise: the block carries the action and bank-b's freeze, in the block both changes fall due")
	}
	if carol := accountOf(t, due, "carol"); carol.PublicKey != testSigner("carol").PublicKeyBase64() || carol.PendingHeight != 0 {
		t.Error("carol's change, co-signed by the bank frozen in its due block, applied: the freeze must come first")
	}
	if bob := accountOf(t, due, "bob"); bob.PublicKey != testSigner("bob").PublicKeyBase64() || accountOf(t, due, "alice").PublicKey != testSigner("shared-key").PublicKeyBase64() {
		t.Error("bob's change to the key the action gave alice applied: the action must come first")
	}
}

// The ⅔ action binds the name and the governance sequence, never the name's key: a key change that
// lands after the votes were signed does not make it stale.
func TestNameActionSurvivesAnEarlierKeyChange(t *testing.T) {
	chain, state := requestFixture()
	state.Parameters.ChangeDelayBlocks = 1
	action := votedAction(Reassignment{Name: "alice", PreviousAmount: rational("9"), NewAmount: rational("5"), Sequence: state.GovernanceSequence}, "bank-a", "bank-b")
	_, filed := nextBlock(t, chain, state, requests(keyChangeOf("alice", "bank-a", "alice-new", requestBlock)))
	_, rekeyed := nextBlock(t, chain, filed, BlockContent{})
	if accountOf(t, rekeyed, "alice").PublicKey != testSigner("alice-new").PublicKeyBase64() {
		t.Fatal("premise: alice's key changed after the votes")
	}
	block, acted := nextBlock(t, chain, rekeyed, BlockContent{Reassignment: action})
	if block.Content.Reassignment == nil || accountOf(t, acted, "alice").Amount.Cmp(rational("5")) != 0 {
		t.Error("the action signed before alice's key change was not carried and applied")
	}
}

// The specific replay the review found: a reassignment guarded only by the previous BALANCE can be
// replayed whenever the balance cycles back — a grant (prev=0 → X) re-mints X forever once the grantee
// spends to zero. The governance SEQUENCE closes it: applying bumps the sequence, so the certificate
// (bound to the old sequence) can never re-apply, even when the balance returns to prev.
func TestReassignmentSequenceGuardBlocksCycleReplay(t *testing.T) {
	validators := testValidators("v0", "v1", "v2")
	grant := reassignmentBy("grantee", "0", "500", 0, "v0", "v1", "v2")

	// At sequence 0 with the grantee at 0, the grant is valid.
	funders := buildLedgerState(0, []Account{namedAccount("v0", "100"), namedAccount("grantee", "0")}, rational("0.1"))
	if !grant.Valid(signing.MLDSA65{}, testNetwork, validators, funders) {
		t.Fatal("the grant should be valid against sequence 0 when the grantee holds 0")
	}

	// The grantee later spends everything back to zero. The balance-only guard would now re-accept the
	// SAME certificate — but the sequence has advanced, so it must be rejected.
	afterSpend := funders // the grantee at 0 again → AccountAmount == 0 == PreviousAmount
	afterSpend.GovernanceSequence = 1
	if grant.Valid(signing.MLDSA65{}, testNetwork, validators, afterSpend) {
		t.Fatal("a spent-back-to-zero grant must NOT re-mint: the governance sequence advanced (replay guard)")
	}
}

// A reassignment sets an absolute balance after the block's payments, so its account could pay its
// balance away in the enacting block and still be set to the voted amount: a seizure was dodged and
// every outcome minted what was paid out. Its block now carries no payment from or to the account:
// ProposeBlock drops them and ApplyBlock rejects a block that carries one.
func TestReassignmentExcludesPaymentsTouchingItsAccount(t *testing.T) {
	chain := testChain("v0", "v1", "v2")
	previous := buildLedgerState(0, []Account{
		namedAccount("v0", "0"), namedAccount("v1", "0"), namedAccount("v2", "0"),
		namedAccount("treasury", "1000"), namedAccount("payer", "100"), namedAccount("stash", "0"),
	}, rational("0.1"))
	previous.Parameters.MaxMessageBytes = 0 // unbounded: the filler payments below are 64 KiB each
	outflow := signedPayment("treasury", "stash", "1000", 1)
	inflow := signedPayment("payer", "treasury", "10", 1)

	for _, newAmount := range []string{"5000", "500", "0"} { // a top-up, a partial and a full seizure
		certificate := reassignmentBy("treasury", "1000", newAmount, 0, "v0", "v1", "v2")
		block := ProposeBlock(chain, previous, zeroHash, zeroHash, BlockContent{Payments: []Payment{outflow, inflow}, Reassignment: certificate})
		if block.Content.Reassignment == nil || len(block.Content.Payments) != 0 {
			t.Fatalf("%s: want the reassignment carried and both payments dropped, got %d payments", newAmount, len(block.Content.Payments))
		}
		next, err := ApplyBlock(chain, previous, zeroHash, block)
		if err != nil || AccountAmount(next.Accounts, "treasury").Cmp(rational(newAmount)) != 0 || AccountAmount(next.Accounts, "stash").Sign() != 0 {
			t.Fatalf("%s: the account must end at exactly the voted balance with nothing paid out (error %v)", newAmount, err)
		}

		// A proposer that carries a payment anyway, with a self-consistent digest: rejected.
		for _, payment := range []Payment{outflow, inflow} {
			paid := ProcessPayments(previous.Accounts, []Payment{payment}, previous.Parameters.TransactionCost)
			forged := block
			forged.Content.Payments = []Payment{payment}
			forged.StateDigest = LedgerState{Accounts: applyReassignment(paid, *certificate), Parameters: previous.Parameters, GovernanceSequence: 1, Height: 1}.Digest()
			if _, err := ApplyBlock(chain, previous, zeroHash, forged); err == nil {
				t.Errorf("%s: ApplyBlock accepted a reassignment together with a payment touching its account", newAmount)
			}
		}
	}

	// The account's payments take no room in the block either: ones that would fill the content budget
	// on their own must not push out a payment that does not touch it.
	certificate := reassignmentBy("treasury", "1000", "0", 0, "v0", "v1", "v2")
	var payments []Payment
	for i := 0; i < 12; i++ { // 12 x 64 KiB: more than the 768 KiB budget
		filler, _ := SignPayment(testSigner("treasury"), Payment{From: "treasury", To: "stash", Amount: rational("1"), Comment: strings.Repeat("x", 1<<16) + strconv.Itoa(i), BlockID: 1})
		payments = append(payments, filler)
	}
	other, _ := SignPayment(testSigner("payer"), Payment{From: "payer", To: "stash", Amount: rational("10"), Comment: strings.Repeat("y", 1<<16), BlockID: 1})
	block := ProposeBlock(chain, previous, zeroHash, zeroHash, BlockContent{Payments: append(payments, other), Reassignment: certificate})
	if block.Content.Reassignment == nil || len(block.Content.Payments) != 1 || block.Content.Payments[0].From != "payer" {
		t.Fatalf("want the reassignment and the payment that does not touch its account, got %d payments", len(block.Content.Payments))
	}
	if _, err := ApplyBlock(chain, previous, zeroHash, block); err != nil {
		t.Fatalf("the proposed block does not apply: %v", err)
	}
}
