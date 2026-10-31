package node

// PoC tests for the "two different signings means the key is burned" review (label: burn-rule),
// node layer. These assert what the burn principle / docs promise and FAIL on the current code.

import (
	"math/big"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// BURN-3 (static mode): slashing removes the offender's ACCOUNT, but in static-validator mode the
// consensus set is Config.Validators — untouched by account deletion. So a validator proven to
// equivocate is NOT ejected from consensus: it keeps full voting power forever. If its (fixed) power
// is >=1/3 of the set, the remaining <2/3 can never reach quorum again, so the documented slash
// cannot restore liveness — a permanent stall from one compromised key, exactly what the "burn"
// is meant to prevent.
func TestPoCStaticModeSlashDoesNotEjectValidator(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	off, _ := signing.Generate(scheme, network)
	h1, _ := signing.Generate(scheme, network)
	h2, _ := signing.Generate(scheme, network)
	O, H1, H2 := off.PublicKeyBase64(), h1.PublicKeyBase64(), h2.PublicKeyBase64()
	// A static 3-validator set, equal power: quorum = floor(2*3/3)+1 = 3, so ALL three are needed.
	static := consensus.NewValidatorSet([]consensus.Validator{
		{PublicKey: O, Power: 1}, {PublicKey: H1, Power: 1}, {PublicKey: H2, Power: 1},
	})
	n := New(Config{
		NetworkID: network, Scheme: scheme, Signer: h1, Validators: static,
		TransactionCost: rat("0.1"), Timeout: time.Second,
		Genesis: []ledger.Account{
			{PublicKey: O, Amount: rat("100"), ValidatorNode: O},
			{PublicKey: H1, Amount: rat("100"), ValidatorNode: H1},
			{PublicKey: H2, Amount: rat("100"), ValidatorNode: H2},
		},
	}, NewHTTPTransport(nil))

	// Simulate the state AFTER the offender was slashed out (its account removed by applySlashes).
	accountsAfterSlash := []ledger.Account{
		{PublicKey: H1, Amount: rat("100"), ValidatorNode: H1},
		{PublicKey: H2, Amount: rat("100"), ValidatorNode: H2},
	}
	vs := n.validatorsFor(accountsAfterSlash)
	if vs.Contains(O) {
		q := vs.Quorum()
		honest := vs.Power(H1) + vs.Power(H2)
		t.Fatalf("static mode: the slashed equivocator %s... is STILL a validator (power %d of total %d, quorum %d); "+
			"the two honest validators hold only %d < %d, so consensus can never reach quorum again — "+
			"the slash did not burn the key out of the set, and liveness is permanently stalled",
			O[:8], vs.Power(O), vs.TotalPower(), q, honest, q)
	}
}

// BURN-4 (dynamic mode): deriveValidators sums the stake of EVERY account whose ValidatorNode is the
// offender. applySlashes deletes only the offender's OWN account; it never touches accounts that
// DELEGATED to the offender. So stake delegated by others keeps the offender in the derived set with
// power = that delegated stake — it stays a validator, and its key keeps proposing/voting, after
// being "slashed". slashEvidence then re-carries the same proof every block to no effect.
func TestPoCDynamicModeDelegationKeepsSlashedValidator(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	off, _ := signing.Generate(scheme, network)
	delegator, _ := signing.Generate(scheme, network)
	O, D := off.PublicKeyBase64(), delegator.PublicKeyBase64()
	threshold := rat("50")
	n := New(Config{
		NetworkID: network, Scheme: scheme, Signer: delegator, ValidatorThreshold: threshold,
		TransactionCost: rat("0.1"), Timeout: time.Second,
		Genesis: []ledger.Account{
			{PublicKey: O, Amount: rat("100"), ValidatorNode: O},
			{PublicKey: D, Amount: rat("100"), ValidatorNode: O}, // D delegates its stake to O
		},
	}, NewHTTPTransport(nil))

	// State AFTER the offender's own account is slashed out: the delegator D remains, still pointing
	// its 100 stake at O.
	accountsAfterSlash := []ledger.Account{
		{PublicKey: D, Amount: rat("100"), ValidatorNode: O},
	}
	vs := n.validatorsFor(accountsAfterSlash)
	if vs.Contains(O) {
		t.Fatalf("dynamic mode: the slashed equivocator %s... is STILL a validator with power %d, "+
			"sourced entirely from stake OTHERS delegated to it (its own account is gone). Slashing "+
			"deleted an account, not the key's place in the set — the offender keeps voting",
			O[:8], vs.Power(O))
	}
}

// BURN-5: a proposer that signs two DIFFERENT valid proposals for the same (height, round) is not
// detected or slashed. The driver records only the first proposal per round and produces no evidence
// for proposals (Driver.Evidence only scans vote sets), yet the second, different-digest proposal
// from the real proposer still passes onProposal's proposer+signature gate and is RELAYED and
// re-ApplyBlock'd network-wide. So proposal equivocation is free: unslashed, and it doubles
// block-validation work and splits honest prevotes for the round.
func TestPoCProposalEquivocationUndetectedAndRelayed(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	proposer, _ := signing.Generate(scheme, network) // sole validator → round-0 proposer
	observer, _ := signing.Generate(scheme, network) // node under test (not a validator)
	P := proposer.PublicKeyBase64()
	n := New(Config{
		NetworkID: network, Scheme: scheme, Signer: observer,
		Validators:      consensus.NewValidatorSet([]consensus.Validator{{PublicKey: P, Power: 1}}),
		TransactionCost: rat("0.1"), Timeout: time.Second,
		Genesis: []ledger.Account{{PublicKey: P, Amount: rat("100"), ValidatorNode: P}},
	}, NewHTTPTransport(nil))
	n.startHeight()

	mkProposal := func(tag byte) ProposalMsg {
		var d [32]byte
		copy(d[:], "proposal-equivocation-digest-xx!")
		d[0] = tag // two DIFFERENT digests for the same (height=1, round=0)
		p := ProposalMsg{Height: 1, Round: 0, ValidRound: -1, Block: ledger.Block{Height: 1, StateDigest: d}}
		if err := SignProposal(proposer, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first, second := mkProposal(0xA1), mkProposal(0xB2)

	relayed1 := n.onProposal(first)
	relayed2 := n.onProposal(second) // different digest, same round, same (real) proposer

	// Drive a commit path so Driver.Evidence() has gathered everything it would. There is no proposal
	// evidence type at all, so this is necessarily empty — the assertion is the point.
	ev := n.driver.Evidence()
	if relayed2 && len(ev) == 0 {
		t.Fatalf("proposer equivocation went unpunished: two different signed proposals for (h=1,r=0) were "+
			"both accepted (relayed1=%v relayed2=%v) and the driver produced 0 equivocation proofs for the "+
			"proposer — votes burn a double-signer, proposals do not", relayed1, relayed2)
	}
}

var _ = big.NewRat
