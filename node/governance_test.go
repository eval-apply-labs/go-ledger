package node

import (
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// newlyPooled is the first result of addParameterVotes or addReassignmentVotes: whether the certificate
// was newly pooled. Called as newlyPooled(node.addParameterVotes(change)), it takes both results.
func newlyPooled(pooled bool, _ error) bool { return pooled }

// End to end: a validator's parameter-change vote, once it reaches the quorum, is bundled into a
// block and amends the committed parameters. Here a lone validator (quorum 1) raises the fee.
func TestNodeEnactsParameterChangeByVote(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000")}, 5*time.Second, 30*time.Millisecond)
	validatorNode := New(testConfig(staticChain("founder"), "founder", genesis), NewHTTPTransport(nil)) // quorum 1
	validatorNode.Start()
	defer validatorNode.Stop()

	waitFor(t, 2*time.Second, func() bool { return validatorNode.Snapshot().Height >= 2 }, "network did not start")

	// Vote to raise the transaction fee to 0.5, keeping the (valid) timing profile the node booted with.
	snapshot := validatorNode.Snapshot()
	currentParameters := snapshot.Parameters
	sequence := snapshot.GovernanceSequence
	newParameters := currentParameters
	newParameters.TransactionCost = rational("0.5")
	vote, _ := ledger.SignParameterVote(testSigner("founder"), "founder", newParameters, currentParameters.Digest(), sequence)
	validatorNode.AdmitParameterCertificate(ledger.ParameterChange{
		NewParameters:            newParameters,
		PreviousParametersDigest: currentParameters.Digest(),
		Sequence:                 sequence,
		Votes:                    []ledger.ParameterVote{vote},
	})

	waitFor(t, 4*time.Second, func() bool {
		cost := validatorNode.Snapshot().Parameters.TransactionCost
		return cost != nil && cost.Cmp(rational("0.5")) == 0
	}, "the quorum-approved parameter change was not enacted in committed state")
}

// The governance pools take only complete certificates, whose votes already meet the > ⅔ quorum.
// Anyone could fill a 64-slot pool with voteless entries, and one validator with entries carrying
// only its own vote; they bound the current sequence, so no prune dropped them and no real change
// could ever be pooled again. A partial tally was public too: a seizure's target could read the first
// vote on GET /hdls/governance and move its money before the quorum formed.
func TestGovernancePoolsTakeOnlyCompleteCertificates(t *testing.T) {
	minority, banks := "minority", []string{"bank-0", "bank-1", "bank-2"}
	chain := staticChain()
	chain.ValidatorRule.StaticValidators = []ledger.StaticValidator{
		{Name: minority, Power: 1}, // well under a third
		{Name: banks[0], Power: 10}, {Name: banks[1], Power: 10}, {Name: banks[2], Power: 10},
	}
	accounts := []ledger.Account{testAccount("target", "100"), testAccount(minority, "0")}
	for _, bank := range banks {
		accounts = append(accounts, testAccount(bank, "0"))
	}
	validatorNode := New(testConfig(chain, banks[0], testGenesis(accounts, time.Second, 0)),
		NewHTTPTransport(nil)) // not started: the pools are driven here as the loop would drive them

	current, sequence := validatorNode.state.Parameters, validatorNode.state.GovernanceSequence
	parameterChange := func(cost string, voters ...string) ledger.ParameterChange {
		proposed := current
		proposed.TransactionCost = rational(cost)
		change := ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: current.Digest(), Sequence: sequence}
		for _, voter := range voters {
			vote, _ := ledger.SignParameterVote(testSigner(voter), voter, proposed, current.Digest(), sequence)
			change.Votes = append(change.Votes, vote)
		}
		return change
	}
	reassignment := func(name, newAmount string, voters ...string) ledger.Reassignment {
		previous := ledger.AccountAmount(validatorNode.state.Accounts, name)
		certificate := ledger.Reassignment{Name: name, PreviousAmount: previous, NewAmount: rational(newAmount), Sequence: sequence}
		for _, voter := range voters {
			vote, _ := ledger.SignReassignmentVote(testSigner(voter), voter, certificate)
			certificate.Votes = append(certificate.Votes, vote)
		}
		return certificate
	}

	for i := 1; i <= 64; i++ {
		validatorNode.addParameterVotes(parameterChange(strconv.Itoa(i)))                // no vote at all
		validatorNode.addParameterVotes(parameterChange(strconv.Itoa(i)+".5", minority)) // the minority's own vote only
		validatorNode.addReassignmentVotes(reassignment("target", strconv.Itoa(i)))
		validatorNode.addReassignmentVotes(reassignment("target", strconv.Itoa(i)+".5", minority))
	}
	validatorNode.addReassignmentVotes(reassignment("target", "0", banks[0])) // a seizure, one vote of three so far
	if view := validatorNode.governanceView(); len(view.ParameterProposals) != 0 || len(view.Reassignments) != 0 {
		t.Fatalf("incomplete certificates were pooled and shown: %d parameter changes, %d reassignments",
			len(view.ParameterProposals), len(view.Reassignments))
	}

	feeChange, seizure := parameterChange("0.2", banks...), reassignment("target", "0", banks...)
	if !newlyPooled(validatorNode.addParameterVotes(feeChange)) || !newlyPooled(validatorNode.addReassignmentVotes(seizure)) {
		t.Fatal("complete certificates must be pooled and relayed")
	}
	if newlyPooled(validatorNode.addParameterVotes(feeChange)) || newlyPooled(validatorNode.addReassignmentVotes(seizure)) {
		t.Fatal("a certificate already pooled must not be relayed again")
	}
	if pending := validatorNode.pendingParameterChange(); pending == nil || pending.NewParameters.TransactionCost.Cmp(rational("0.2")) != 0 {
		t.Fatal("the complete fee change must be ready for the next block")
	}
	if pending := validatorNode.pendingReassignment(); pending == nil || pending.Name != "target" {
		t.Fatal("the complete seizure must be ready for the next block")
	}
}

// A validator that leaves the set after voting (it re-delegates, its stake drops under the threshold,
// or it is frozen) no longer blocks the certificate it signed: its vote is set aside, and the change
// still goes into the next block while the remaining voters meet the quorum. Valid rejects a whole
// certificate for a single non-member vote, so the departed vote used to block it for good. The same
// holds for a certificate completed after its voter left, and submitted then: intake sets the vote
// aside too, where it used to refuse the whole certificate.
func TestDepartedVoterDoesNotBlockCertificate(t *testing.T) {
	genesis := testGenesis([]ledger.Account{selfDelegated("remaining", "60"), selfDelegated("grantee", "30"), selfDelegated("departing", "15")}, time.Second, 0)
	validatorNode := New(testConfig(dynamicChain("10"), "remaining", genesis), // dynamic: power is stake, quorum 71 of 105
		NewHTTPTransport(nil)) // not started: the pools are driven here as the loop would drive them

	current, sequence := validatorNode.state.Parameters, validatorNode.state.GovernanceSequence
	proposed := current
	proposed.TransactionCost = rational("0.5")
	change := ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: current.Digest(), Sequence: sequence}
	grant := ledger.Reassignment{Name: "grantee", PreviousAmount: rational("30"), NewAmount: rational("40"), Sequence: sequence}
	for _, voter := range []string{"remaining", "departing"} { // 75 of 105
		parameterVote, _ := ledger.SignParameterVote(testSigner(voter), voter, proposed, current.Digest(), sequence)
		change.Votes = append(change.Votes, parameterVote)
		reassignmentVote, _ := ledger.SignReassignmentVote(testSigner(voter), voter, grant)
		grant.Votes = append(grant.Votes, reassignmentVote)
	}
	if !newlyPooled(validatorNode.addParameterVotes(change)) || !newlyPooled(validatorNode.addReassignmentVotes(grant)) {
		t.Fatal("setup: both certificates should be pooled")
	}

	// The departing validator re-delegates its stake to the remaining one and leaves the set, as a
	// committed update would leave the state: the remaining validator alone now holds 75 of 105.
	departed := selfDelegated("departing", "15")
	departed.Delegate = "remaining"
	validatorNode.state.Accounts = []ledger.Account{departed, selfDelegated("grantee", "30"), selfDelegated("remaining", "60")}
	validatorNode.publishView() // as a commit does
	pendingChange, pendingGrant := validatorNode.pendingParameterChange(), validatorNode.pendingReassignment()
	if pendingChange == nil || pendingGrant == nil {
		t.Fatalf("certificates still carried by a quorum of current validators must stay ready (parameter change: %v, reassignment: %v)",
			pendingChange != nil, pendingGrant != nil)
	}
	if len(pendingChange.Votes) != 1 || len(pendingGrant.Votes) != 1 {
		t.Fatal("the departed validator's vote must be left out of the certificate carried in a block")
	}

	proposed.TransactionCost = rational("0.4")
	lateChange := ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: current.Digest(), Sequence: sequence}
	lateGrant := ledger.Reassignment{Name: "grantee", PreviousAmount: rational("30"), NewAmount: rational("50"), Sequence: sequence}
	for _, voter := range []string{"departing", "remaining"} { // the departing validator signed before it left, the remaining one after
		parameterVote, _ := ledger.SignParameterVote(testSigner(voter), voter, proposed, current.Digest(), sequence)
		lateChange.Votes = append(lateChange.Votes, parameterVote)
		reassignmentVote, _ := ledger.SignReassignmentVote(testSigner(voter), voter, lateGrant)
		lateGrant.Votes = append(lateGrant.Votes, reassignmentVote)
	}
	if !newlyPooled(validatorNode.addParameterVotes(lateChange)) || !newlyPooled(validatorNode.addReassignmentVotes(lateGrant)) {
		t.Fatal("a certificate carried by current validators must be pooled even with a departed validator's vote in it")
	}
}

// The governance view shows all ten parameters, those in force and those of each pooled change,
// amounts in canonical form.
func TestGovernanceViewShowsTheTenParameters(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100")}, 2*time.Second, 500*time.Millisecond)
	genesis.Parameters.NewKeyFee, genesis.Parameters.ChangeDelayBlocks, genesis.Parameters.RetainBlocks = rational("2.5"), 20, 1000
	genesis.Parameters.MaxPaymentAmount, genesis.Parameters.MaxAccountBalance = rational("500"), rational("9000.5")
	validatorNode := New(testConfig(staticChain("validator"), "validator", genesis),
		NewHTTPTransport(nil)) // not started: the view is read here as the loop would read it

	proposed := ledger.LedgerParameters{TransactionCost: rational("0.2"), BlockIntervalMs: 600, StepTimeoutMs: 2500, MaxPeers: 7,
		MaxMessageBytes: 0, NewKeyFee: rational("3"), ChangeDelayBlocks: 21, RetainBlocks: 1001,
		MaxPaymentAmount: rational("600"), MaxAccountBalance: rational("9001")}
	vote, _ := ledger.SignParameterVote(testSigner("validator"), "validator", proposed, genesis.Parameters.Digest(), 0)
	if !newlyPooled(validatorNode.addParameterVotes(ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: genesis.Parameters.Digest(), Votes: []ledger.ParameterVote{vote}})) {
		t.Fatal("premise: the sole validator's change is pooled")
	}

	view := validatorNode.governanceView()
	inForce := ParametersView{TransactionCost: "0.1", BlockIntervalMs: 500, StepTimeoutMs: 2000, MaxPeers: 0, MaxMessageBytes: ledger.DefaultParameters().MaxMessageBytes,
		NewKeyFee: "2.5", ChangeDelayBlocks: 20, RetainBlocks: 1000, MaxPaymentAmount: "500", MaxAccountBalance: "9000.5"}
	if view.Parameters != inForce {
		t.Errorf("the view shows the parameters in force as %+v, want %+v", view.Parameters, inForce)
	}
	pooled := ParametersView{TransactionCost: "0.2", BlockIntervalMs: 600, StepTimeoutMs: 2500, MaxPeers: 7, MaxMessageBytes: 0,
		NewKeyFee: "3", ChangeDelayBlocks: 21, RetainBlocks: 1001, MaxPaymentAmount: "600", MaxAccountBalance: "9001"}
	if len(view.ParameterProposals) != 1 || view.ParameterProposals[0].NewParameters != pooled {
		t.Errorf("the view shows the pooled change as %+v, want %+v", view.ParameterProposals, pooled)
	}
}

// The governance view names validators and voters by name, and each pooled reassignment by the name of
// the account it sets.
func TestGovernanceViewNamesValidatorsAndAccounts(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("coop", "10")}, time.Second, 0)
	validatorNode := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	action := ledger.Reassignment{Name: "coop", PreviousAmount: rational("10"), NewAmount: rational("20")}
	vote, _ := ledger.SignReassignmentVote(testSigner("validator"), "validator", action)
	action.Votes = []ledger.ReassignmentVote{vote}
	if !newlyPooled(validatorNode.addReassignmentVotes(action)) {
		t.Fatal("premise: the sole validator's reassignment is pooled")
	}
	view := validatorNode.governanceView()
	if len(view.Validators) != 1 || view.Validators[0] != "validator" {
		t.Errorf("the view lists the validators as %v, want their names", view.Validators)
	}
	if len(view.Reassignments) != 1 || view.Reassignments[0].Name != "coop" || len(view.Reassignments[0].Voters) != 1 || view.Reassignments[0].Voters[0] != "validator" {
		t.Errorf("the view shows the pooled reassignment as %+v, want coop's, voted by validator", view.Reassignments)
	}
}

// The governance view lists the pooled certificates in key order, the order a block picks them from, so
// /hdls/governance gives the same list every time. It used to range over the pools' maps, in random
// order.
func TestGovernanceViewListsCertificatesInKeyOrder(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("coop", "10")}, time.Second, 0)
	validatorNode := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	var wantCosts, wantAmounts []string // in the order of their keys
	costByKey, amountByKey := map[string]string{}, map[string]string{}
	for i := 1; i <= 8; i++ {
		proposed := genesis.Parameters
		proposed.TransactionCost = rational(strconv.Itoa(i))
		change := ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: genesis.Parameters.Digest()}
		parameterVote, _ := ledger.SignParameterVote(testSigner("validator"), "validator", proposed, genesis.Parameters.Digest(), 0)
		change.Votes = []ledger.ParameterVote{parameterVote}
		action := ledger.Reassignment{Name: "coop", PreviousAmount: rational("10"), NewAmount: rational(strconv.Itoa(10 + i))}
		reassignmentVote, _ := ledger.SignReassignmentVote(testSigner("validator"), "validator", action)
		action.Votes = []ledger.ReassignmentVote{reassignmentVote}
		if !newlyPooled(validatorNode.addParameterVotes(change)) || !newlyPooled(validatorNode.addReassignmentVotes(action)) {
			t.Fatal("premise: the sole validator's certificates are pooled")
		}
		costByKey[parameterChangeKey(change)] = strconv.Itoa(i)
		amountByKey[reassignmentKey(action)] = strconv.Itoa(10 + i)
	}
	for _, key := range slices.Sorted(maps.Keys(costByKey)) {
		wantCosts = append(wantCosts, costByKey[key])
	}
	for _, key := range slices.Sorted(maps.Keys(amountByKey)) {
		wantAmounts = append(wantAmounts, amountByKey[key])
	}

	view := validatorNode.governanceView()
	var costs, amounts []string
	for _, proposal := range view.ParameterProposals {
		costs = append(costs, proposal.NewParameters.TransactionCost)
	}
	for _, reassignment := range view.Reassignments {
		amounts = append(amounts, reassignment.NewAmount)
	}
	if !slices.Equal(costs, wantCosts) || !slices.Equal(amounts, wantAmounts) {
		t.Errorf("the view lists the parameter changes as %v and the name actions as %v, want %v and %v (key order)", costs, amounts, wantCosts, wantAmounts)
	}
}
