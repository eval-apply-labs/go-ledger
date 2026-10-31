package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

// dynamicRule is a dynamic validator rule with the given threshold.
func dynamicRule(threshold string) ValidatorRule {
	return ValidatorRule{Threshold: rational(threshold)}
}

// A static validator set holds names and powers only; each seat's key is its account's current key in
// the state, so the ⅔ re-keying a validator moves its seat to the new key with no change to the rule,
// and a validator name with no account holds no seat.
func TestStaticSetTakesKeysFromState(t *testing.T) {
	rule := ValidatorRule{StaticValidators: []StaticValidator{{Name: "bank-a", Power: 2}, {Name: "bank-b", Power: 1}, {Name: "bank-c", Power: 1}}}
	state := buildLedgerState(0, []Account{namedAccount("bank-a", "0"), namedAccount("bank-b", "0")}, rational("0.1"))
	validators := rule.SetFor(state)
	if validators.PublicKey("bank-a") != testSigner("bank-a").PublicKeyBase64() || validators.Power("bank-a") != 2 || validators.Power("bank-b") != 1 {
		t.Fatalf("the static set must seat each name with its key from the state and its configured power: %+v", validators.Validators())
	}
	if validators.Contains("bank-c") {
		t.Fatal("a static validator with no account holds a seat, under no key")
	}

	rekeyed := state
	rekeyed.Accounts = append([]Account(nil), state.Accounts...)
	rekeyed.Accounts[0].PublicKey = testSigner("bank-a-new-key").PublicKeyBase64()
	if key := rule.SetFor(rekeyed).PublicKey("bank-a"); key != testSigner("bank-a-new-key").PublicKeyBase64() {
		t.Fatal("once the state gives bank-a a new key, its seat must take that key")
	}
}

// In dynamic mode each balance backs its Delegate, a name, and a name whose total reaches the
// threshold is seated with that total as its power, under its own current key. A balance with no
// delegate backs no one, and a name may back itself.
func TestDynamicSetFollowsDelegationToNames(t *testing.T) {
	accounts := []Account{
		namedAccount("bank-a", "1000"), // backs itself, over the threshold
		namedAccount("bank-b", "50"),   // backs itself, under the threshold
		namedAccount("alice", "120"),   // backs bank-c
		namedAccount("bob", "30"),      // backs bank-c (bank-c's total: 150)
		namedAccount("bank-c", "0"),
		namedAccount("carol", "500"), // backs no one
	}
	for i, delegate := range []string{"bank-a", "bank-b", "bank-c", "bank-c", "", ""} {
		accounts[i].Delegate = delegate
	}
	validators := dynamicRule("100").SetFor(buildLedgerState(0, accounts, rational("0.1")))
	if validators.Power("bank-a") != 1000 || validators.Power("bank-c") != 150 {
		t.Fatalf("powers: bank-a=%d (want 1000), bank-c=%d (want 150)", validators.Power("bank-a"), validators.Power("bank-c"))
	}
	if validators.Contains("bank-b") || validators.Contains("carol") || len(validators.Validators()) != 2 {
		t.Fatalf("only bank-a and bank-c reach the threshold: %+v", validators.Validators())
	}
	if validators.PublicKey("bank-c") != testSigner("bank-c").PublicKeyBase64() {
		t.Fatal("a seat delegated to a name must take that name's own key")
	}
	if dynamicRule("100").SetFor(buildLedgerState(0, accounts[:4], rational("0.1"))).Contains("bank-c") {
		t.Fatal("stake delegated to a name with no account seated it, with no key")
	}
}

// A frozen name is never seated, in either mode: it neither votes nor proposes, whatever stake backs it
// or whatever seat the static list gives it.
func TestFrozenNameIsNotSeated(t *testing.T) {
	frozen := namedAccount("bank-a", "1000")
	frozen.Delegate, frozen.Frozen = "bank-a", true
	other := namedAccount("bank-b", "1000")
	other.Delegate = "bank-a" // a balance that is not frozen, backing the frozen name
	state := buildLedgerState(0, []Account{frozen, other}, rational("0.1"))
	static := ValidatorRule{StaticValidators: []StaticValidator{{Name: "bank-a", Power: 1}, {Name: "bank-b", Power: 1}}}
	if static.SetFor(state).Contains("bank-a") || !static.SetFor(state).Contains("bank-b") {
		t.Fatal("the static set must leave out the frozen name, and only it")
	}
	if dynamicRule("100").SetFor(state).Contains("bank-a") {
		t.Fatal("the dynamic set seated a frozen name")
	}
}

// A frozen account's balance backs no delegate (R10): otherwise a thief who re-delegated stolen
// balances would keep that power after the victims freeze their names.
func TestFrozenAccountBacksNoDelegate(t *testing.T) {
	victim := namedAccount("victim", "500")
	victim.Delegate = "thief"
	thief := namedAccount("thief", "60")
	thief.Delegate = "thief"
	state := buildLedgerState(0, []Account{thief, victim}, rational("0.1"))
	if power := dynamicRule("100").SetFor(state).Power("thief"); power != 560 {
		t.Fatalf("before the freeze the thief holds %d, want 560", power)
	}
	state.Accounts[1].Frozen = true // the victim freezes its name
	if dynamicRule("100").SetFor(state).Contains("thief") {
		t.Fatal("the frozen victim's balance still backs the thief's seat")
	}
}

// C2 (external review): a delegated stake past 2^63 must saturate at MaxValidatorPower — raw
// Int64() truncation would wrap negative (the whale silently dropped: a liveness stall if it held
// >⅓ of power) or small-positive (corrupted quorum math). Reachable in principle via genesis or a
// ⅔-vote reassignment minting an enormous balance.
func TestDynamicSetSaturatesHugeStake(t *testing.T) {
	whale := namedAccount("whale", "0")
	whale.Amount, whale.Delegate = new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 100)), "whale" // 2^100 whole units
	small := namedAccount("small", "5")
	small.Delegate = "small"
	validators := ValidatorRule{}.SetFor(buildLedgerState(0, []Account{whale, small}, rational("0.1")))
	if got := validators.Power("whale"); got != consensus.MaxValidatorPower {
		t.Fatalf("whale power = %d, want the saturation cap %d", got, consensus.MaxValidatorPower)
	}
	if got := validators.Power("small"); got != 5 {
		t.Fatalf("small validator power = %d, want 5 (unaffected by the whale)", got)
	}
	if validators.TotalPower() != consensus.MaxValidatorPower+5 {
		t.Fatalf("total = %d, want cap+5", validators.TotalPower())
	}
	if validators.Quorum() <= 0 || validators.ProposerAt(1, 0) == "" {
		t.Fatal("quorum and proposer selection must stay well-formed on a saturated set")
	}
}
