package node

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// signedUpdate is update signed under the key of its test account, update.Name.
func signedUpdate(update ledger.AccountUpdate) ledger.AccountUpdate {
	signed, err := ledger.SignAccountUpdate(testSigner(update.Name), update)
	if err != nil {
		panic(err)
	}
	return signed
}

// describedAs reports whether the account named name holds the description in node's committed state.
func describedAs(node *Node, name, description string) func() bool {
	return func() bool {
		account, _ := ledger.FindAccount(node.Snapshot().Accounts, name)
		return account.Description == description
	}
}

// End to end: a signed account update submitted to a running node flows through the update pool into
// a block, and its delegation and description land in committed state.
func TestNodeAppliesAnUpdateEndToEnd(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("alice", "1000"), testAccount("bank-a", "0")}, 5*time.Second, 40*time.Millisecond)
	validatorNode := New(testConfig(staticChain("alice"), "alice", genesis), NewHTTPTransport(nil)) // quorum 1
	validatorNode.Start()
	defer validatorNode.Stop()

	update := signedUpdate(ledger.AccountUpdate{Name: "alice", Delegate: "bank-a", Description: "the founder", BlockID: validatorNode.Snapshot().Height + 4})
	if err := validatorNode.AdmitUpdate(update); err != nil {
		t.Fatalf("a valid update was refused: %v", err)
	}
	waitFor(t, 5*time.Second, describedAs(validatorNode, "alice", "the founder"), "the update did not reach committed state")
	if alice, _ := ledger.FindAccount(validatorNode.Snapshot().Accounts, "alice"); alice.Delegate != "bank-a" {
		t.Fatalf("the delegation did not land: %+v", alice)
	}
}

// Update intake pools only updates that can apply: one from a name with no account, or naming a block
// beyond the intake window, is refused. Such updates used to fill the bounded pool, after which every
// honest update was accepted (HTTP 202) and silently dropped.
func TestUpdateIntakeRefusesUpdatesThatCannotApply(t *testing.T) {
	const poolSize = 4
	config := testConfig(staticChain("owner"), "owner", testGenesis([]ledger.Account{testAccount("owner", "1000")}, 5*time.Second, 20*time.Millisecond))
	config.MaxMempool = poolSize
	validatorNode := New(config, NewHTTPTransport(nil))

	// Queued before Start, so the loop takes them in this order at height 1.
	for i := 0; i < poolSize; i++ {
		accountless := signedUpdate(ledger.AccountUpdate{Name: "ghost", Description: strconv.Itoa(i), BlockID: 2})
		farFuture := signedUpdate(ledger.AccountUpdate{Name: "owner", Description: strconv.Itoa(i), BlockID: 2 + MaxBlockIDLookahead})
		validatorNode.SubmitUpdate(accountless)
		validatorNode.SubmitUpdate(farFuture)
	}
	validatorNode.SubmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "owner", Description: "landed", BlockID: 2}))
	validatorNode.Start()
	defer validatorNode.Stop()

	waitFor(t, 5*time.Second, describedAs(validatorNode, "owner", "landed"),
		"the update was dropped: updates that could never apply held the update pool")
}

// A block carries one update per account, so the pool holds one per account and block. An account
// that signs many updates for one block, each different and free, used to fill the bounded pool with
// them, after which every other account's update was accepted (HTTP 202) and silently dropped.
func TestUpdatePoolHoldsOneUpdatePerAccountAndBlock(t *testing.T) {
	const poolSize = 4
	config := testConfig(staticChain("owner"), "owner", testGenesis([]ledger.Account{testAccount("owner", "1000"), testAccount("squatter", "1")}, 5*time.Second, 20*time.Millisecond))
	config.MaxMempool = poolSize
	validatorNode := New(config, NewHTTPTransport(nil))

	// Queued before Start, so the loop takes them in this order at height 1.
	for i := 0; i < 2*poolSize; i++ {
		validatorNode.SubmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "squatter", Description: strconv.Itoa(i), BlockID: 2}))
	}
	validatorNode.SubmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "owner", Description: "landed", BlockID: 2}))
	validatorNode.Start()
	defer validatorNode.Stop()

	waitFor(t, 5*time.Second, describedAs(validatorNode, "owner", "landed"),
		"the update was dropped: one account's updates for its block held the update pool")
}

// After a commit the update pool drops an update whose block has passed, and forgets it, so it no
// longer holds a slot (see reconcilePools).
func TestUpdatePoolDropsUpdatesThatCanNoLongerApply(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("alice", "100"), testAccount("bank-a", "0")}, time.Hour, time.Hour)
	genesis.Height = 4 // the height in progress is 5
	node := New(testConfig(staticChain("bank-a"), "bank-a", genesis), NewHTTPTransport(nil))
	kept := signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "kept", BlockID: 10})
	passed := signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "passed", BlockID: 4})
	for _, update := range []ledger.AccountUpdate{kept, passed} {
		if !node.updates.add(update) {
			t.Fatalf("%s should enter the pool", update.Description)
		}
	}

	node.reconcilePools(node.state.Accounts)

	if pending := node.updates.pendingCopy(); len(pending) != 1 || pending[0].Description != "kept" {
		t.Fatalf("only the update that can still apply should remain, got %v", pending)
	}
	if !node.updates.add(passed) {
		t.Fatal("a dropped update must be forgotten")
	}
}

// POST /hdls/update answers 409, with the reason, for an update the node refuses, and 202 for one it
// pools. It used to answer 202 and drop a refused update silently, so a wallet reported success for an
// update that never landed. Among the refusals: a delegate that is no name with an account, or that is
// frozen, and an update from a frozen name (spec 4.2); the checks run against the committed state, and
// the signature under the account's key in it.
func TestUpdateRefusalAnswers409(t *testing.T) {
	frozen := testAccount("frozen", "100")
	frozen.Frozen = true
	genesis := testGenesis([]ledger.Account{testAccount("alice", "100"), testAccount("bank-a", "0"), frozen}, time.Hour, time.Hour)
	node := New(testConfig(staticChain("bank-a"), "bank-a", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	post := func(update ledger.AccountUpdate) (int, string) {
		body, _ := json.Marshal(update)
		response, err := http.Post(server.URL+"/hdls/update", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		reason, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(reason)
	}

	misattributed, _ := ledger.SignAccountUpdate(testSigner("bank-a"), ledger.AccountUpdate{Name: "alice", Description: "mine", BlockID: 2}) // bank-a's key signs an update of alice's
	for _, testCase := range []struct {
		name   string
		update ledger.AccountUpdate
		reason string
	}{
		{"a delegate with no account", signedUpdate(ledger.AccountUpdate{Name: "alice", Delegate: "nobody", BlockID: 2}), `no account is named "nobody"`},
		{"a frozen delegate", signedUpdate(ledger.AccountUpdate{Name: "alice", Delegate: "frozen", BlockID: 2}), `the delegate "frozen" is frozen`},
		{"from a frozen name", signedUpdate(ledger.AccountUpdate{Name: "frozen", Description: "still here", BlockID: 2}), `the account "frozen" is frozen`},
		{"from a name with no account", signedUpdate(ledger.AccountUpdate{Name: "ghost", Description: "boo", BlockID: 2}), `no account is named "ghost"`},
		{"changing nothing", signedUpdate(ledger.AccountUpdate{Name: "alice", BlockID: 2}), "changes nothing"},
		{"a stale block", signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "late", BlockID: 0}), "outside the intake window"},
		{"signed under another name's key", misattributed, "invalid update signature"},
	} {
		if status, reason := post(testCase.update); status != http.StatusConflict || !strings.Contains(reason, testCase.reason) {
			t.Errorf("%s: answered %d %q, want 409 mentioning %q", testCase.name, status, reason, testCase.reason)
		}
	}
	accepted := signedUpdate(ledger.AccountUpdate{Name: "alice", Delegate: "bank-a", BlockID: 2})
	for range 2 { // a repeat of a pooled update is accepted again
		if status, reason := post(accepted); status != http.StatusAccepted {
			t.Fatalf("a valid update answered %d %q, want 202", status, reason)
		}
	}
	another := signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "second", BlockID: 2})
	if status, reason := post(another); status != http.StatusConflict || !strings.Contains(reason, "same block") {
		t.Errorf("a second update of alice's for the same block answered %d %q, want 409", status, reason)
	}
}
