package ledger

// Governance-area review PoCs (2026-10-27). Each test asserts the SECURE behaviour, so a FAIL
// demonstrates the defect; the t.Logf lines print the concrete bad outcome.

import (
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type govFixture struct {
	scheme     signing.Scheme
	network    string
	signers    []*signing.Signer
	validators *consensus.ValidatorSet
}

func newGovFixture(t *testing.T, n int) govFixture {
	t.Helper()
	f := govFixture{scheme: signing.Ed25519{}, network: "testnet"}
	vals := make([]consensus.Validator, n)
	for i := 0; i < n; i++ {
		s, err := signing.Generate(f.scheme, f.network)
		if err != nil {
			t.Fatal(err)
		}
		f.signers = append(f.signers, s)
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	f.validators = consensus.NewValidatorSet(vals)
	return f
}

func (f govFixture) reassignment(t *testing.T, account string, prev, next *big.Rat, seq int64, by ...*signing.Signer) *Reassignment {
	t.Helper()
	r := &Reassignment{Account: account, PreviousAmount: prev, NewAmount: next, Sequence: seq}
	for _, s := range by {
		v, err := SignReassignmentVote(s, account, prev, next, seq)
		if err != nil {
			t.Fatal(err)
		}
		r.Votes = append(r.Votes, v)
	}
	return r
}

func supplyOf(accounts []Account) *big.Rat {
	sum := new(big.Rat)
	for _, a := range accounts {
		if a.Amount != nil {
			sum.Add(sum, a.Amount)
		}
	}
	return sum
}

func accountOf(accounts []Account, key string) (Account, bool) {
	for _, a := range accounts {
		if a.PublicKey == key {
			return a, true
		}
	}
	return Account{}, false
}

// FINDING: a reassignment is validated against the PRE-block balance but applied as an ABSOLUTE
// value AFTER the block's payments. If the target account pays out in the same block, the outflow is
// honoured AND the reassignment re-sets the balance — so the money the target moved out is minted a
// second time. With the documented "treasury pattern" (a ⅔ top-up of a treasury: prev=1000 → 5000),
// the treasury's key holder times one outgoing payment into the top-up block and 1000 extra units
// appear that no validator voted for. A ⅔ SEIZURE (1000 → 500) likewise becomes a net MINT.
// Honest code builds such a block: ProposeBlock carries both, ApplyBlock accepts it.
func TestPoCGovReassignmentSameBlockOutflowIsMintedTwice(t *testing.T) {
	f := newGovFixture(t, 3) // quorum 3
	cost := big.NewRat(1, 10)
	custodian, _ := signing.Generate(f.scheme, f.network) // holds the treasury key — NOT a validator
	stash, _ := signing.Generate(f.scheme, f.network)     // the custodian's private second key
	T, X := custodian.PublicKeyBase64(), stash.PublicKeyBase64()

	for _, tc := range []struct {
		name      string
		newAmount *big.Rat
	}{
		{"treasury top-up 1000->5000", big.NewRat(5000, 1)},
		{"partial seizure 1000->500", big.NewRat(500, 1)},
		{"full seizure 1000->0", big.NewRat(0, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := []Account{{PublicKey: T, Name: "treasury", Amount: big.NewRat(1000, 1), ValidatorNode: T}}
			state := LedgerState{Accounts: prev, Parameters: LedgerParameters{TransactionCost: cost}}
			cert := f.reassignment(t, T, big.NewRat(1000, 1), tc.newAmount, 0, f.signers...)
			// The custodian's outgoing payment naming the block the certificate will land in. Honest
			// intake admits it (pending outgoing 1000 <= committed balance 1000).
			pay, err := SignPayment(custodian, Payment{PublicKeyFrom: T, PublicKeyTo: X, Amount: big.NewRat(1000, 1), BlockID: 1})
			if err != nil {
				t.Fatal(err)
			}
			blk := ProposeBlock(f.scheme, f.network, 1, state, BlockContent{Payments: []Payment{pay}, Reassignment: cert}, f.validators)
			if blk.Reassignment == nil || len(blk.Payments) != 1 {
				t.Fatalf("setup: honest ProposeBlock should carry both (reassignment=%v payments=%d)", blk.Reassignment != nil, len(blk.Payments))
			}
			next, ok := ApplyBlock(f.scheme, f.network, state, blk, f.validators)
			if !ok {
				t.Fatal("setup: ApplyBlock rejected the block")
			}
			before, after := supplyOf(prev), supplyOf(next.Accounts)
			ratifiedDelta := new(big.Rat).Sub(tc.newAmount, big.NewRat(1000, 1)) // what the voters signed: prev -> new
			// Upper bound of an honest outcome: supply moves by at most the ratified delta (minus the fee).
			bound := new(big.Rat).Add(before, ratifiedDelta)
			tAcct, _ := accountOf(next.Accounts, T)
			t.Logf("supply before=%s after=%s ; ratified delta=%s ; treasury=%s (name=%q validatorNode=%q) stash=%s",
				before.RatString(), after.RatString(), ratifiedDelta.RatString(),
				AccountAmount(next.Accounts, T).RatString(), tAcct.Name, tAcct.ValidatorNode, AccountAmount(next.Accounts, X).RatString())
			if after.Cmp(bound) > 0 {
				extra := new(big.Rat).Sub(after, bound)
				t.Errorf("UNRATIFIED MINT: supply grew %s beyond what the certificate ratified (the same-block outflow was minted twice)", extra.FloatString(1))
			}
		})
	}
}

// FINDING: LedgerParameters.Acceptable has only LOWER bounds on the timing profile. A ratified change
// can set BlockIntervalMs (or StepTimeoutMs) to centuries; node.commit then schedules the next height
// that far out, so the chain never produces another block — and since every governance action needs
// a block, nothing on-chain can revert it. (MinMessageBytes exists precisely so governance can't
// brick the network; the timing fields have no equivalent ceiling.)
func TestPoCGovAcceptableAllowsChainHaltingTiming(t *testing.T) {
	f := newGovFixture(t, 4)
	current := LedgerParameters{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: 3000}
	// "One day" typed in microseconds instead of ms, or seconds*1e6: 8.64e10 ms ≈ 2.7 years.
	// And 9e12 ms ≈ 285 years — still no int64 overflow in time.Duration(ms)*time.Millisecond.
	for _, bad := range []LedgerParameters{
		{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 86_400_000_000, StepTimeoutMs: 3000},
		{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 9_000_000_000_000, StepTimeoutMs: 3000},
		{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: 9_000_000_000_000},
		{TransactionCost: big.NewRat(1, 10), BlockIntervalMs: 1000, StepTimeoutMs: math.MaxInt64}, // wraps negative in the node
	} {
		change := &ParameterChange{NewParameters: bad, PreviousParametersDigest: current.Digest()}
		for _, s := range f.signers {
			v, _ := SignParameterVote(s, bad, current.Digest(), 0)
			change.Votes = append(change.Votes, v)
		}
		interval := time.Duration(bad.BlockIntervalMs) * time.Millisecond // node.blockInterval
		step := time.Duration(bad.StepTimeoutMs) * time.Millisecond       // node.stepTimeout
		t.Logf("BlockIntervalMs=%d StepTimeoutMs=%d → node waits %.1f years between blocks; step timeout %v (%.1f years); Acceptable=%v Valid=%v",
			bad.BlockIntervalMs, bad.StepTimeoutMs, interval.Hours()/24/365, step, step.Hours()/24/365,
			bad.Acceptable(4), change.Valid(f.scheme, f.network, f.validators, current, 0))
		if change.Valid(f.scheme, f.network, f.validators, current, 0) {
			t.Errorf("a ⅔ certificate for a chain-halting timing profile (interval %d ms, step %d ms) is ACCEPTED", bad.BlockIntervalMs, bad.StepTimeoutMs)
		}
	}
}

// FINDING ("two different signings means the key is burned" does not hold for governance, static
// mode): slashing deletes the offender's ACCOUNT, but a static validator set is config, so the burned
// key stays a full-power validator. Its reassignment/parameter votes keep counting toward the ⅔
// quorum forever after the slash (and its consensus votes keep counting too).
func TestPoCGovSlashedStaticValidatorStillRatifies(t *testing.T) {
	f := newGovFixture(t, 4) // power 1 each, quorum 3
	burned := f.signers[3]
	B := burned.PublicKeyBase64()
	accounts := []Account{}
	for _, s := range f.signers {
		k := s.PublicKeyBase64()
		accounts = append(accounts, Account{PublicKey: k, Amount: big.NewRat(100, 1), ValidatorNode: k})
	}
	cost := big.NewRat(1, 10)
	var dX, dY [32]byte
	dX[0], dY[0] = 1, 2
	vX, _ := consensus.SignVote(burned, consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: B})
	vY, _ := consensus.SignVote(burned, consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: B})
	ev := consensus.Evidence{Validator: B, A: vX, B: vY}

	state := LedgerState{Accounts: accounts, Parameters: LedgerParameters{TransactionCost: cost}}
	blk := ProposeBlock(f.scheme, f.network, 2, state, BlockContent{Evidence: []consensus.Evidence{ev}}, f.validators)
	slashed, ok := ApplyBlock(f.scheme, f.network, state, blk, f.validators)
	if !ok || AccountAmount(slashed.Accounts, B).Sign() != 0 {
		t.Fatal("setup: the equivocator should be slashed")
	}
	// After the slash: the burned key + two others mint 1,000,000 to an attacker-chosen key.
	mint := f.reassignment(t, "attacker", new(big.Rat), big.NewRat(1_000_000, 1), slashed.GovernanceSequence, f.signers[0], f.signers[1], burned)
	withoutBurned := f.reassignment(t, "attacker", new(big.Rat), big.NewRat(1_000_000, 1), slashed.GovernanceSequence, f.signers[0], f.signers[1])
	t.Logf("after slash: validator set still contains burned key=%v (power %d); cert WITH burned vote valid=%v; WITHOUT it valid=%v",
		f.validators.Contains(B), f.validators.Power(B),
		mint.Valid(f.scheme, f.network, f.validators, slashed.Accounts, slashed.GovernanceSequence),
		withoutBurned.Valid(f.scheme, f.network, f.validators, slashed.Accounts, slashed.GovernanceSequence))
	if mint.Valid(f.scheme, f.network, f.validators, slashed.Accounts, slashed.GovernanceSequence) {
		t.Errorf("a key burned for equivocation still supplies the deciding governance vote after its slash")
	}
}

// FINDING: a reassignment resurrects a slashed ("burned") key. (a) Same block: an honest proposer
// that holds both a pending reassignment for V and V's equivocation evidence carries both; applySlashes
// runs BEFORE applyReassignment, so V is deleted and immediately re-created with NewAmount. (b) Any
// later block: V is absent, AccountAmount reports 0, so a grant prev=0 → X to the burned key is valid.
func TestPoCGovReassignmentResurrectsSlashedKey(t *testing.T) {
	f := newGovFixture(t, 3)
	burned, _ := signing.Generate(f.scheme, f.network)
	V := burned.PublicKeyBase64()
	accounts := []Account{{PublicKey: V, Name: "v", Amount: big.NewRat(100, 1), ValidatorNode: V}}
	state := LedgerState{Accounts: accounts, Parameters: LedgerParameters{TransactionCost: big.NewRat(1, 10)}}
	var dX, dY [32]byte
	dX[0], dY[0] = 1, 2
	vX, _ := consensus.SignVote(burned, consensus.Vote{Height: 1, Round: 0, Type: consensus.Prevote, BlockDigest: dX, Validator: V})
	vY, _ := consensus.SignVote(burned, consensus.Vote{Height: 1, Round: 0, Type: consensus.Prevote, BlockDigest: dY, Validator: V})
	ev := consensus.Evidence{Validator: V, A: vX, B: vY}

	// (a) same block: a pending (honestly ratified, e.g. partial-seizure 100 → 50) reassignment + the evidence.
	cert := f.reassignment(t, V, big.NewRat(100, 1), big.NewRat(50, 1), 0, f.signers...)
	blk := ProposeBlock(f.scheme, f.network, 2, state, BlockContent{Evidence: []consensus.Evidence{ev}, Reassignment: cert}, f.validators)
	next, ok := ApplyBlock(f.scheme, f.network, state, blk, f.validators)
	if !ok || len(blk.Evidence) != 1 || blk.Reassignment == nil {
		t.Fatal("setup: block with evidence + reassignment should apply")
	}
	_, present := accountOf(next.Accounts, V)
	t.Logf("(a) block slashing V also carried a reassignment: V present after slash=%v balance=%s", present, AccountAmount(next.Accounts, V).RatString())
	if present {
		t.Errorf("(a) the slashed key survives its own slash block with %s (reassignment applied after the slash)", AccountAmount(next.Accounts, V).RatString())
	}

	// (b) a later block: only the slash first, then a newcomer-style grant 0 → 500 to the burned key.
	slashBlk := ProposeBlock(f.scheme, f.network, 2, state, BlockContent{Evidence: []consensus.Evidence{ev}}, f.validators)
	afterSlash, _ := ApplyBlock(f.scheme, f.network, state, slashBlk, f.validators)
	grant := f.reassignment(t, V, new(big.Rat), big.NewRat(500, 1), afterSlash.GovernanceSequence, f.signers...)
	t.Logf("(b) grant 0→500 to the burned key valid after its slash=%v", grant.Valid(f.scheme, f.network, f.validators, afterSlash.Accounts, afterSlash.GovernanceSequence))
	if grant.Valid(f.scheme, f.network, f.validators, afterSlash.Accounts, afterSlash.GovernanceSequence) {
		t.Errorf("(b) a burned key can be re-funded by reassignment (no tombstone): it is indistinguishable from a never-seen key")
	}
}

// FINDING: the governance sequence (and the payment BlockID) are the ONLY replay anchors, and both
// restart from zero on a re-genesis — the documented procedure for any release that changes the
// state digest ("restart every node on the new version from the captured state"), which keeps the
// network id and the validator keys. Every old certificate then becomes valid again at its old
// sequence number whenever its value guard matches; the newcomer-grant case (prev=0) matches as soon
// as the grantee holds nothing. Anyone holding the old certificate (every node pooled/gossiped it)
// re-mints with no new vote. Same for old payments at their old BlockID.
func TestPoCGovCertificateReplaysAcrossReGenesis(t *testing.T) {
	f := newGovFixture(t, 3)
	cost := big.NewRat(1, 10)
	founder, _ := signing.Generate(f.scheme, f.network)
	newcomer, _ := signing.Generate(f.scheme, f.network)
	F, N := founder.PublicKeyBase64(), newcomer.PublicKeyBase64()
	genesis1 := LedgerState{Accounts: []Account{{PublicKey: F, Amount: big.NewRat(1000, 1), ValidatorNode: F}}, Parameters: LedgerParameters{TransactionCost: cost}}

	// Old chain: seq 0 grants the newcomer 500; at height 2 the newcomer spends it all.
	grant := f.reassignment(t, N, new(big.Rat), big.NewRat(500, 1), 0, f.signers...)
	b1 := ProposeBlock(f.scheme, f.network, 1, genesis1, BlockContent{Reassignment: grant}, f.validators)
	s1, ok := ApplyBlock(f.scheme, f.network, genesis1, b1, f.validators)
	if !ok || AccountAmount(s1.Accounts, N).Cmp(big.NewRat(500, 1)) != 0 {
		t.Fatal("setup: grant should apply on the old chain")
	}
	spend, _ := SignPayment(newcomer, Payment{PublicKeyFrom: N, PublicKeyTo: F, Amount: big.NewRat(500, 1), BlockID: 2})
	b2 := ProposeBlock(f.scheme, f.network, 2, s1, BlockContent{Payments: []Payment{spend}}, f.validators)
	s2, ok := ApplyBlock(f.scheme, f.network, s1, b2, f.validators)
	if !ok {
		t.Fatal("setup: spend should apply")
	}
	// Re-genesis from the captured state (same network id, same keys): config has no field for the
	// governance sequence or the height, so both restart (node.New: height 1, sequence 0).
	genesis2 := LedgerState{Accounts: s2.Accounts, Parameters: s2.Parameters}
	supplyBefore := supplyOf(genesis2.Accounts)

	// Replay the OLD grant certificate, verbatim, at height 1 of the new chain.
	r1 := ProposeBlock(f.scheme, f.network, 1, genesis2, BlockContent{Reassignment: grant}, f.validators)
	n1, ok := ApplyBlock(f.scheme, f.network, genesis2, r1, f.validators)
	replayed := ok && r1.Reassignment != nil
	// And replay the old height-2 payment at height 2 (BlockID binding restarts too).
	r2 := ProposeBlock(f.scheme, f.network, 2, n1, BlockContent{Payments: []Payment{spend}}, f.validators)
	n2, ok2 := ApplyBlock(f.scheme, f.network, n1, r2, f.validators)
	t.Logf("new chain: old grant replayed=%v (newcomer balance %s, supply %s → %s); old payment replayed=%v (payments carried %d, founder %s → %s)",
		replayed, AccountAmount(n1.Accounts, N).RatString(), supplyBefore.RatString(), supplyOf(n1.Accounts).RatString(),
		ok2 && len(r2.Payments) == 1, len(r2.Payments), AccountAmount(n1.Accounts, F).RatString(), AccountAmount(n2.Accounts, F).RatString())
	if replayed {
		t.Errorf("an old-chain governance certificate re-minted %s on the re-genesised chain with no new vote", "500")
	}
}
