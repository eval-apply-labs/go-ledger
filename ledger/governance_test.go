package ledger

import (
	"math"
	"math/big"
	"reflect"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// testValidators is the set of the given test accounts, each of power 1, under the keys testSigner
// holds for them.
func testValidators(names ...string) *consensus.ValidatorSet {
	var validators []consensus.Validator
	for _, name := range names {
		validators = append(validators, consensus.Validator{Name: name, PublicKey: testSigner(name).PublicKeyBase64(), Power: 1})
	}
	return consensus.NewValidatorSet(validators)
}

// parameterVote is the test validator `name`'s vote for newParameters replacing the parameters that
// hash to previousDigest, at the governance sequence.
func parameterVote(name string, newParameters LedgerParameters, previousDigest [32]byte, sequence int64) ParameterVote {
	vote, _ := SignParameterVote(testSigner(name), name, newParameters, previousDigest, sequence)
	return vote
}

// A parameter change is ratified only by a > ⅔ stake quorum, applies to committed state, and cannot
// be replayed once the parameters it amends have moved on.
func TestParameterChangeRequiresTwoThirds(t *testing.T) {
	validators := testValidators("v0", "v1", "v2") // total 3 → quorum floor(6/3)+1 = 3
	chain := testChain("v0", "v1", "v2")

	// A well-formed timing profile (step timeout comfortably covers the fan-out); the change keeps it.
	current := DefaultParameters()
	proposed := parametersCosting(big.NewRat(1, 2)) // raise the fee to 0.5
	previousDigest := current.Digest()
	voteBy := func(name string) ParameterVote { return parameterVote(name, proposed, previousDigest, 0) } // genesis governance sequence 0

	// All three approve (>= quorum): the change is valid and, applied in a block, amends the params.
	ratified := &ParameterChange{NewParameters: proposed, PreviousParametersDigest: previousDigest,
		Votes: []ParameterVote{voteBy("v0"), voteBy("v1"), voteBy("v2")}}
	if !ratified.Valid(signing.NetworkScheme(), testNetwork, validators, current, 0) {
		t.Fatal("a change approved by all validators should be ratified")
	}
	previousState := LedgerState{Accounts: []Account{namedAccount("v0", "1"), namedAccount("v1", "1"), namedAccount("v2", "1")}, Parameters: current}
	block := ProposeBlock(chain, previousState, zeroHash, zeroHash, BlockContent{ParameterChange: ratified})
	nextState, err := ApplyBlock(chain, previousState, zeroHash, block)
	if err != nil {
		t.Fatalf("a block with a ratified parameter change should apply: %v", err)
	}
	if nextState.Parameters.TransactionCost.Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("parameters not amended: cost = %s", nextState.Parameters.TransactionCost.RatString())
	}

	// Only one of three approves (< quorum): not ratified, and a block carrying it is rejected.
	subQuorum := &ParameterChange{NewParameters: proposed, PreviousParametersDigest: previousDigest,
		Votes: []ParameterVote{voteBy("v0")}}
	if subQuorum.Valid(signing.NetworkScheme(), testNetwork, validators, current, 0) {
		t.Fatal("a sub-⅔ change must not be ratified")
	}
	rejected := block
	rejected.Content.ParameterChange = subQuorum
	if _, err := ApplyBlock(chain, previousState, zeroHash, rejected); err == nil {
		t.Fatal("ApplyBlock must reject a block whose parameter change lacks a ⅔ quorum")
	}

	// A vote counts only under the key the set holds for its name: v2's name on a vote v0's key signed
	// does not complete the quorum.
	misattributed := voteBy("v0")
	misattributed.Validator = "v2"
	forged := &ParameterChange{NewParameters: proposed, PreviousParametersDigest: previousDigest,
		Votes: []ParameterVote{voteBy("v0"), voteBy("v1"), misattributed}}
	if forged.Valid(signing.NetworkScheme(), testNetwork, validators, current, 0) {
		t.Fatal("a vote naming v2 but signed under v0's key was counted")
	}

	// All or nothing: a full quorum with one bad vote in front of it is not ratified (spec 4.9).
	padded := &ParameterChange{NewParameters: proposed, PreviousParametersDigest: previousDigest,
		Votes: []ParameterVote{misattributed, voteBy("v0"), voteBy("v1"), voteBy("v2")}}
	if padded.Valid(signing.NetworkScheme(), testNetwork, validators, current, 0) {
		t.Fatal("a change with a full quorum and one bad vote was ratified: one bad vote must make it invalid")
	}

	// Replay guard: applying the change bumped the governance sequence, so the ratified certificate
	// (bound to sequence 0) can never re-apply — even against the exact parameters it amended.
	if nextState.GovernanceSequence != 1 {
		t.Fatalf("applying a governance action should bump the sequence to 1, got %d", nextState.GovernanceSequence)
	}
	if ratified.Valid(signing.NetworkScheme(), testNetwork, validators, current, nextState.GovernanceSequence) {
		t.Fatal("a certificate must not re-apply once the governance sequence has advanced (replay guard)")
	}
}

// The window and the fan-out are coupled: capping peers raises the gossip dimension, so the step
// timeout must grow to cover it. A profile that breaks the coupling is rejected even with a full
// quorum; the same fan-out with a long-enough timeout is accepted.
func TestParameterChangeEnforcesTimingCoupling(t *testing.T) {
	validators := testValidators("v0", "v1", "v2")
	current := DefaultParameters()
	previousDigest := current.Digest()
	// Capping fan-out at 2 among 3 nodes forces dimension 2, so the timeout must cover 2 hops.
	makeChange := func(profile LedgerParameters) *ParameterChange {
		var votes []ParameterVote
		for _, name := range []string{"v0", "v1", "v2"} {
			votes = append(votes, parameterVote(name, profile, previousDigest, 0))
		}
		return &ParameterChange{NewParameters: profile, PreviousParametersDigest: previousDigest, Votes: votes}
	}
	tooShort, longEnough := DefaultParameters(), DefaultParameters()
	tooShort.StepTimeoutMs, tooShort.MaxPeers = 300, 2
	longEnough.StepTimeoutMs, longEnough.MaxPeers = 600, 2
	if makeChange(tooShort).Valid(signing.NetworkScheme(), testNetwork, validators, current, 0) {
		t.Fatal("a profile whose step timeout can't cover the fan-out must be rejected even with a full quorum")
	}
	if !makeChange(longEnough).Valid(signing.NetworkScheme(), testNetwork, validators, current, 0) {
		t.Fatal("the same fan-out with a long-enough step timeout should be accepted")
	}
}

// The timing profile has a ceiling: a ratified block interval of 86,400,000,000 ms (a day typed with
// three extra zeros) stopped block production, and with it the vote that would revert it. A profile
// over MaxTimingMs is refused even with a full quorum; a day-long window (pigeon mode) still passes.
func TestParameterChangeRejectsChainHaltingTimings(t *testing.T) {
	validators := testValidators("v0", "v1", "v2")
	current := DefaultParameters()
	withTimings := func(blockIntervalMs, stepTimeoutMs int64) LedgerParameters {
		profile := DefaultParameters()
		profile.BlockIntervalMs, profile.StepTimeoutMs = blockIntervalMs, stepTimeoutMs
		return profile
	}
	ratified := func(profile LedgerParameters) bool {
		change := &ParameterChange{NewParameters: profile, PreviousParametersDigest: current.Digest()}
		for _, name := range []string{"v0", "v1", "v2"} {
			change.Votes = append(change.Votes, parameterVote(name, profile, current.Digest(), 0))
		}
		return change.Valid(signing.NetworkScheme(), testNetwork, validators, current, 0)
	}
	for _, timing := range [][2]int64{
		{86_400_000_000, 3000}, {9_000_000_000_000, 3000}, {1000, 9_000_000_000_000}, {1000, math.MaxInt64},
		{MaxTimingMs + 1, 3000}, {1000, MaxTimingMs + 1},
	} {
		if ratified(withTimings(timing[0], timing[1])) {
			t.Errorf("a ratified profile with interval %d ms and step timeout %d ms must be refused", timing[0], timing[1])
		}
	}
	if !ratified(withTimings(MaxTimingMs, MaxTimingMs)) {
		t.Error("a day-long window (pigeon mode) must stay acceptable")
	}
}

// Every one of the ten parameters is in the parameter digest, and so in the state digest, and in the
// preimage a parameter vote signs: changing any one of them changes both digests and voids the vote.
func TestEveryParameterIsInTheDigest(t *testing.T) {
	base := DefaultParameters()
	vote := parameterVote("v0", base, [32]byte{}, 0)
	changes := map[string]func(parameters *LedgerParameters){
		"TransactionCost":   func(parameters *LedgerParameters) { parameters.TransactionCost = rational("0.2") },
		"BlockIntervalMs":   func(parameters *LedgerParameters) { parameters.BlockIntervalMs++ },
		"StepTimeoutMs":     func(parameters *LedgerParameters) { parameters.StepTimeoutMs++ },
		"MaxPeers":          func(parameters *LedgerParameters) { parameters.MaxPeers++ },
		"MaxMessageBytes":   func(parameters *LedgerParameters) { parameters.MaxMessageBytes++ },
		"NewKeyFee":         func(parameters *LedgerParameters) { parameters.NewKeyFee = rational("2") },
		"ChangeDelayBlocks": func(parameters *LedgerParameters) { parameters.ChangeDelayBlocks++ },
		"RetainBlocks":      func(parameters *LedgerParameters) { parameters.RetainBlocks++ },
		"MaxPaymentAmount":  func(parameters *LedgerParameters) { parameters.MaxPaymentAmount = rational("99999.5") },
		"MaxAccountBalance": func(parameters *LedgerParameters) { parameters.MaxAccountBalance = rational("9999999.5") },
	}
	if fieldCount := reflect.TypeOf(base).NumField(); len(changes) != fieldCount {
		t.Fatalf("the test changes %d parameters, LedgerParameters has %d", len(changes), fieldCount)
	}
	for name, change := range changes {
		changed := DefaultParameters()
		change(&changed)
		if changed.Digest() == base.Digest() {
			t.Errorf("changing %s does not change the parameter digest", name)
		}
		if (LedgerState{Parameters: changed}).Digest() == (LedgerState{Parameters: base}).Digest() {
			t.Errorf("changing %s does not change the state digest", name)
		}
		if VerifyParameterVote(signing.NetworkScheme(), testNetwork, testSigner("v0").PublicKeyBase64(), changed, [32]byte{}, 0, vote) {
			t.Errorf("a vote for the defaults still verifies with %s changed", name)
		}
	}
}

// Acceptable holds every parameter to its floor and ceiling (spec 3.4) and refuses a missing amount,
// so a vote or a genesis cannot set a fee or a cap to nothing, a message bound or a fan-out past what
// a node handles, or a delay or a retention that a height plus it would overflow: changes filed with
// such a delay would never fall due, and a record so kept would never be pruned.
func TestAcceptableEnforcesTheFloorsAndCeilings(t *testing.T) {
	const participants = 4
	for _, testCase := range []struct {
		name       string
		change     func(parameters *LedgerParameters)
		acceptable bool
	}{
		{"the defaults", func(*LedgerParameters) {}, true},
		{"a free payment", func(parameters *LedgerParameters) { parameters.TransactionCost = new(big.Rat) }, true},
		{"no transaction cost", func(parameters *LedgerParameters) { parameters.TransactionCost = nil }, false},
		{"a negative transaction cost", func(parameters *LedgerParameters) { parameters.TransactionCost = rational("-0.1") }, false},
		{"max peers at the ceiling", func(parameters *LedgerParameters) { parameters.MaxPeers = MaxPeersLimit }, true},
		{"max peers over the ceiling", func(parameters *LedgerParameters) { parameters.MaxPeers = MaxPeersLimit + 1 }, false},
		{"max peers negative", func(parameters *LedgerParameters) { parameters.MaxPeers = -1 }, false},
		{"an unbounded message size", func(parameters *LedgerParameters) { parameters.MaxMessageBytes = 0 }, true},
		{"the message floor", func(parameters *LedgerParameters) { parameters.MaxMessageBytes = MinMessageBytes() }, true},
		{"under the message floor", func(parameters *LedgerParameters) { parameters.MaxMessageBytes = MinMessageBytes() - 1 }, false},
		{"the message ceiling", func(parameters *LedgerParameters) { parameters.MaxMessageBytes = MaxMessageBytesLimit }, true},
		{"over the message ceiling", func(parameters *LedgerParameters) { parameters.MaxMessageBytes = MaxMessageBytesLimit + 1 }, false},
		{"the smallest new key fee", func(parameters *LedgerParameters) { parameters.NewKeyFee = rational("0.00000001") }, true},
		{"no new key fee", func(parameters *LedgerParameters) { parameters.NewKeyFee = nil }, false},
		{"a zero new key fee", func(parameters *LedgerParameters) { parameters.NewKeyFee = new(big.Rat) }, false},
		{"no largest payment", func(parameters *LedgerParameters) { parameters.MaxPaymentAmount = nil }, false},
		{"a zero largest payment", func(parameters *LedgerParameters) { parameters.MaxPaymentAmount = new(big.Rat) }, false},
		{"no largest balance", func(parameters *LedgerParameters) { parameters.MaxAccountBalance = nil }, false},
		{"a zero largest balance", func(parameters *LedgerParameters) { parameters.MaxAccountBalance = new(big.Rat) }, false},
		{"a delay of one block", func(parameters *LedgerParameters) { parameters.ChangeDelayBlocks = 1 }, true},
		{"no delay", func(parameters *LedgerParameters) { parameters.ChangeDelayBlocks = 0 }, false},
		{"the longest delay", func(parameters *LedgerParameters) { parameters.ChangeDelayBlocks = MaxChangeDelayBlocks }, true},
		{"a delay past the ceiling", func(parameters *LedgerParameters) { parameters.ChangeDelayBlocks = MaxChangeDelayBlocks + 1 }, false},
		{"a delay that overflows", func(parameters *LedgerParameters) { parameters.ChangeDelayBlocks = math.MaxInt64 }, false},
		{"the shortest retention", func(parameters *LedgerParameters) { parameters.RetainBlocks = MinRetainBlocks }, true},
		{"under the shortest retention", func(parameters *LedgerParameters) { parameters.RetainBlocks = MinRetainBlocks - 1 }, false},
		{"the longest retention", func(parameters *LedgerParameters) { parameters.RetainBlocks = MaxRetainBlocks }, true},
		{"a retention past the ceiling", func(parameters *LedgerParameters) { parameters.RetainBlocks = MaxRetainBlocks + 1 }, false},
		{"a retention that overflows", func(parameters *LedgerParameters) { parameters.RetainBlocks = math.MaxInt64 }, false},
	} {
		parameters := DefaultParameters()
		testCase.change(&parameters)
		if accepted := parameters.Acceptable(participants); accepted != testCase.acceptable {
			t.Errorf("%s: Acceptable = %v, want %v", testCase.name, accepted, testCase.acceptable)
		}
	}
	// So a height plus the longest delay or retention stays far inside int64: a chain that commits a
	// block every millisecond for a thousand years is at about 3.2e13.
	const thousandYearsOfBlocks = 1000 * 365 * 24 * 60 * 60 * 1000
	if math.MaxInt64-thousandYearsOfBlocks < MaxChangeDelayBlocks || math.MaxInt64-thousandYearsOfBlocks < MaxRetainBlocks {
		t.Fatal("a height plus the longest delay or retention can overflow")
	}
}

// A cap a block lowers applies from the next block: the block is judged against its pre-block
// parameters, so a payment that fits only the old cap, in the very block that lowers it, is carried
// and applies. From the next block a payment that raises the payee further is refused, while the payee
// keeps its balance above the new cap and may spend it down.
func TestLoweredCapAppliesFromTheNextBlock(t *testing.T) {
	chain := testChain("validator")
	previous := buildLedgerState(0, []Account{namedAccount("validator", "100"), namedAccount("payer", "1000"), namedAccount("payee", "0"), namedAccount("shop", "0")}, rational("0.1"))
	previous.Parameters.MaxAccountBalance = rational("500")
	lowered := parametersCosting(rational("0.1"))
	lowered.MaxAccountBalance = rational("100")
	change := &ParameterChange{NewParameters: lowered, PreviousParametersDigest: previous.Parameters.Digest(),
		Votes: []ParameterVote{parameterVote("validator", lowered, previous.Parameters.Digest(), 0)}}

	lowering := ProposeBlock(chain, previous, zeroHash, zeroHash, BlockContent{Payments: []Payment{signedPayment("payer", "payee", "300.1", 1)}, ParameterChange: change})
	if lowering.Content.ParameterChange == nil || len(lowering.Content.Payments) != 1 {
		t.Fatalf("the block lowering the cap must carry the payment that fits the old cap (change carried %v, %d payments)", lowering.Content.ParameterChange != nil, len(lowering.Content.Payments))
	}
	afterLowering, err := ApplyBlock(chain, previous, zeroHash, lowering)
	if err != nil || AccountAmount(afterLowering.Accounts, "payee").Cmp(rational("300")) != 0 || afterLowering.Parameters.MaxAccountBalance.Cmp(rational("100")) != 0 {
		t.Fatalf("the block lowering the cap, with a payment to 300, must apply (error %v)", err)
	}

	raise := signedPayment("payer", "payee", "10", 2)
	if block := ProposeBlock(chain, afterLowering, zeroHash, zeroHash, BlockContent{Payments: []Payment{raise}}); len(block.Content.Payments) != 0 {
		t.Error("after the cap was lowered, the proposer carried a payment raising the payee further above it")
	}
	forged := ProposeBlock(chain, afterLowering, zeroHash, zeroHash, BlockContent{})
	forged.Content.Payments = []Payment{raise}
	forged.StateDigest = LedgerState{Accounts: ProcessPayments(afterLowering.Accounts, forged.Content.Payments, rational("0.1")),
		Parameters: afterLowering.Parameters, GovernanceSequence: 1, Height: 2}.Digest()
	if _, err := ApplyBlock(chain, afterLowering, zeroHash, forged); err == nil {
		t.Error("after the cap was lowered, a block raising the payee further above it applied")
	}

	spend := ProposeBlock(chain, afterLowering, zeroHash, zeroHash, BlockContent{Payments: []Payment{signedPayment("payee", "shop", "50", 2)}})
	if afterSpending, err := ApplyBlock(chain, afterLowering, zeroHash, spend); err != nil || len(spend.Content.Payments) != 1 ||
		AccountAmount(afterSpending.Accounts, "payee").Cmp(rational("250")) != 0 {
		t.Errorf("a balance above a lowered cap must stay and may shrink (error %v)", err)
	}
}

// The ⅔ may set a balance up to the largest balance, and may leave one already above a lowered cap
// where it is or lower it, but may not raise one above the cap: such a reassignment is not valid, so
// the proposer does not carry it and a block that does is refused.
func TestReassignmentCannotRaiseABalanceAboveTheCap(t *testing.T) {
	chain, validators := testChain("validator"), testValidators("validator")
	previous := buildLedgerState(0, []Account{
		namedAccount("validator", "100"),
		namedAccount("target", "150"), // above the cap below, which a vote has lowered
		namedAccount("pauper", "0"),
	}, rational("0.1"))
	previous.Parameters.MaxAccountBalance = rational("100")
	for _, testCase := range []struct {
		reassignment *Reassignment
		valid        bool
	}{
		{reassignmentBy("target", "150", "150", 0, "validator"), true},           // left where it is
		{reassignmentBy("target", "150", "120", 0, "validator"), true},           // lowered, still above the cap
		{reassignmentBy("target", "150", "150.00000001", 0, "validator"), false}, // raised above the cap
		{reassignmentBy("pauper", "0", "100", 0, "validator"), true},             // from zero up to the cap
		{reassignmentBy("pauper", "0", "100.00000001", 0, "validator"), false},   // from zero above it
	} {
		if valid := testCase.reassignment.Valid(signing.NetworkScheme(), testNetwork, validators, previous); valid != testCase.valid {
			t.Errorf("%s from %s to %s: valid %v, want %v", testCase.reassignment.Name, FormatAmount(testCase.reassignment.PreviousAmount),
				FormatAmount(testCase.reassignment.NewAmount), valid, testCase.valid)
		}
	}

	raise := reassignmentBy("target", "150", "151", 0, "validator")
	if block := ProposeBlock(chain, previous, zeroHash, zeroHash, BlockContent{Reassignment: raise}); block.Content.Reassignment != nil {
		t.Error("the proposer carried a reassignment raising a balance above the cap")
	}
	forged := ProposeBlock(chain, previous, zeroHash, zeroHash, BlockContent{})
	forged.Content.Reassignment = raise
	forged.StateDigest = LedgerState{Accounts: applyReassignment(previous.Accounts, *raise), Parameters: previous.Parameters,
		GovernanceSequence: 1, Height: 1}.Digest()
	if _, err := ApplyBlock(chain, previous, zeroHash, forged); err == nil {
		t.Error("a block raising a balance above the cap by reassignment applied")
	}
}
