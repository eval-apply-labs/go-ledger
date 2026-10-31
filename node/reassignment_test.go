package node

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// End to end: a validator's balance-reassignment vote, once it reaches the quorum, is bundled into a
// block and overrides the account's committed balance. A lone validator (quorum 1) grants the co-op.
func TestNodeEnactsReassignmentByVote(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("coop", "10")}, 5*time.Second, 30*time.Millisecond)
	validatorNode := New(testConfig(staticChain("founder"), "founder", genesis), NewHTTPTransport(nil)) // quorum 1
	validatorNode.Start()
	defer validatorNode.Stop()

	waitFor(t, 2*time.Second, func() bool { return validatorNode.Snapshot().Height >= 2 }, "network did not start")

	// Vote to grant the co-op — raise its balance from 10 to 500 (a ⅔-ratified mint).
	previousAmount := rational("10")
	newAmount := rational("500")
	sequence := validatorNode.Snapshot().GovernanceSequence
	action := ledger.Reassignment{Name: "coop", PreviousAmount: previousAmount, NewAmount: newAmount, Sequence: sequence}
	vote, _ := ledger.SignReassignmentVote(testSigner("founder"), "founder", action)
	action.Votes = []ledger.ReassignmentVote{vote}
	validatorNode.AdmitReassignmentCertificate(action)

	waitFor(t, 4*time.Second, func() bool { return balance(validatorNode.Snapshot().Accounts, "coop").Cmp(rational("500")) == 0 },
		"the quorum-approved reassignment was not enacted in committed state")

	// And the read-only governance view is reachable (used by the admin UI).
	if view := validatorNode.GovernanceView(); view.Quorum != 1 {
		t.Fatalf("governance view quorum = %d, want 1", view.Quorum)
	}
}
