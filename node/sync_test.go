package node

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// testTimeSlack widens the real-time deadlines these integration tests assert under. The chaos and
// gossip scenarios are tuned for a developer machine; a starved shared CI runner can execute the
// same schedule ~10x slower (observed twice: a seed that fails there replays green locally in a
// tenth of the wall time). CI sets GOLEDGER_CHAOS_SLACK to stretch every deadline by that factor,
// so the deadlines stay tight everywhere else.
var testTimeSlack = func() time.Duration {
	if slackSetting := os.Getenv("GOLEDGER_CHAOS_SLACK"); slackSetting != "" {
		if slackFactor, err := strconv.Atoi(slackSetting); err == nil && slackFactor > 1 {
			return time.Duration(slackFactor)
		}
	}
	return 1
}()

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, failureMessage string) {
	t.Helper()
	deadline := time.Now().Add(timeout * testTimeSlack)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(failureMessage)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// servePeer answers as a peer whose last committed checkpoint is checkpoint: /hdls/status with its height
// in progress, any other path with the checkpoint.
func servePeer(responseWriter http.ResponseWriter, request *http.Request, checkpoint Checkpoint) {
	if request.URL.Path == "/hdls/status" {
		writeJSON(responseWriter, map[string]int64{"heightInProgress": checkpoint.Commit.Height + 1})
		return
	}
	writeJSON(responseWriter, checkpoint)
}

// A fresh node given a bootstrap peer must fetch that peer's checkpoint, verify its commit
// certificate against the validator set, and fast-forward to the committed state -- not restart at
// genesis. It proves the state is real by holding a balance that only exists post-payment.
func TestNodeSyncsFromCheckpoint(t *testing.T) {
	chain := staticChain("founder")
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("recipient", "0")}, 5*time.Second, 20*time.Millisecond)

	// The founder's node: sole validator (quorum 1, so it finalises on its own). Serve its API for sync.
	founderNode := New(testConfig(chain, "founder", genesis), NewHTTPTransport(nil))
	founderServer := httptest.NewServer(NewServer(founderNode).Handler())
	defer founderServer.Close()

	founderNode.SubmitPayment(signedPayment("founder", "recipient", "10", 1))
	founderNode.Start()
	defer founderNode.Stop()
	keepBusy(t, "founder", founderNode)

	// Wait until the founder has committed the payment and advanced a few heights.
	waitFor(t, 4*time.Second, func() bool {
		snapshot := founderNode.Snapshot()
		return snapshot.Height >= 3 && balance(snapshot.Accounts, "recipient").Sign() > 0
	}, "A did not commit the payment")

	// The joiner's node: fresh, same validator set, bootstrap = the founder's node. It is not itself a
	// validator; it just syncs.
	joinerConfig := testConfig(chain, "joiner", genesis)
	joinerConfig.Bootstrap = []string{founderServer.URL}
	joinerNode := New(joinerConfig, NewHTTPTransport(nil))
	joinerNode.Start()
	defer joinerNode.Stop()

	// The joiner must fast-forward past genesis and hold the post-payment state (recipient = 9.9).
	waitFor(t, 4*time.Second, func() bool {
		snapshot := joinerNode.Snapshot()
		return snapshot.Height >= 2 && balance(snapshot.Accounts, "recipient").Cmp(rational("9.9")) == 0
	}, "B did not sync to the committed state")
}

// A checkpoint whose certificate does not verify (wrong validator set) must NOT be adopted: the
// node stays at genesis rather than trusting an unverifiable snapshot.
func TestNodeRejectsUnverifiableCheckpoint(t *testing.T) {
	// The founder is the only validator in ITS set, and commits a payment.
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("recipient", "0"), testAccount("other", "0")},
		5*time.Second, 20*time.Millisecond)
	founderNode := New(testConfig(staticChain("founder"), "founder", genesis), NewHTTPTransport(nil))
	founderServer := httptest.NewServer(NewServer(founderNode).Handler())
	defer founderServer.Close()
	founderNode.SubmitPayment(signedPayment("founder", "recipient", "10", 1))
	founderNode.Start()
	defer founderNode.Stop()
	keepBusy(t, "founder", founderNode)
	waitFor(t, 4*time.Second, func() bool { return founderNode.Snapshot().Height >= 3 }, "A did not advance")

	// The joiner's chain has a DIFFERENT validator (another name, another key), so the founder's
	// certificate is not vouched for. The joiner must refuse the checkpoint and stay at genesis.
	joinerConfig := testConfig(staticChain("other"), "joiner", genesis)
	joinerConfig.Bootstrap = []string{founderServer.URL}
	joinerNode := New(joinerConfig, NewHTTPTransport(nil))
	joinerNode.Start()
	defer joinerNode.Stop()

	// Give the sync a moment; the joiner must not have adopted the founder's state, which it cannot verify.
	time.Sleep(300 * time.Millisecond)
	snapshot := joinerNode.Snapshot()
	if balance(snapshot.Accounts, "recipient").Sign() > 0 {
		t.Fatal("B adopted a checkpoint whose certificate does not verify against its validator set")
	}
}

// A catch-up source is untrusted until its checkpoint verifies, so fetching one is bounded: a body
// over the limit (MaxCheckpointBytes in production) is refused rather than read and decoded in full,
// and so is a non-200 answer, whatever its body.
func TestFetchCheckpointRefusesOversizedAndFailedAnswers(t *testing.T) {
	body := `{"Accounts":[{"Name":"x","Amount":"0","Description":"` + strings.Repeat("A", 4<<10) + `"}]}`
	serve := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			responseWriter.WriteHeader(status)
			io.WriteString(responseWriter, body)
		}))
	}
	peer, failing := serve(http.StatusOK), serve(http.StatusInternalServerError)
	defer peer.Close()
	defer failing.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	if _, err := fetchCheckpoint(client, peer.URL, int64(len(body))); err != nil {
		t.Fatalf("a checkpoint within the limit must be fetched, got: %v", err)
	}
	if _, err := fetchCheckpoint(client, peer.URL, int64(len(body))-1); err == nil {
		t.Fatal("a checkpoint body over the limit must be refused")
	}
	if _, err := fetchCheckpoint(client, failing.URL, int64(len(body))); err == nil {
		t.Fatal("a non-200 answer must be refused")
	}
}

// Catch-up asks a peer's /hdls/status first, and fetches /hdls/checkpoint, about 2.9 KB per account,
// only from a peer whose height is ahead (spec 6.6): on an idle chain every peer is at the node's height,
// tick after tick.
func TestCatchUpAsksStatusBeforeTheCheckpoint(t *testing.T) {
	names := []string{"founder", "straggler"}
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	checkpoint := checkpointOf(t, 5, testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0), validatorsOf(names), nil)
	var mutex sync.Mutex
	var asked []string
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		asked = append(asked, request.URL.Path)
		mutex.Unlock()
		servePeer(responseWriter, request, checkpoint)
	}))
	defer peer.Close()

	for _, nodeHeight := range []int64{6, 1} { // at the peer's height, then behind it
		genesis.Height = nodeHeight - 1
		node := New(testConfig(staticChain(names...), "straggler", genesis), NewHTTPTransport(nil))
		mutex.Lock()
		asked = nil
		mutex.Unlock()
		_, found := node.bestCheckpoint([]string{peer.URL})
		mutex.Lock()
		want := []string{"/hdls/status"}
		if nodeHeight < 6 {
			want = append(want, "/hdls/checkpoint")
		}
		if found != (nodeHeight < 6) || !slices.Equal(asked, want) {
			t.Errorf("a node at height %d, the peer at 6: found %v, asked %v, want %v", nodeHeight, found, asked, want)
		}
		mutex.Unlock()
	}
}

// The re-gossip interval, the wait of a node back without its sign log and the catch-up interval follow
// the committed parameters, so a ⅔ vote changes them (C21; owner, 2026-10-31). They used to follow the
// genesis for good: after a vote for a day per step, re-gossip still ran at the genesis step timeout.
// Here a vote raises the step timeout from 50 ms to an hour and the block interval from 0 to 2 s. From
// its next tick on, the re-gossip loop stops ticking every 50 ms, and catch-up runs every five block
// intervals. A second vote lowers the step timeout to 250 ms, the least one validator allows, and
// re-gossip resumes within a second: the loop does not wait out the hour it started (review follow-up).
func TestTimingsFollowAVote(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100")}, 50*time.Millisecond, 0)
	node := New(testConfig(staticChain("validator"), "validator", genesis), &recordingTransport{}) // driven by hand: a sole validator commits within startHeight
	defer node.Stop()
	go node.reGossipLoop() // no event loop runs: the test reads the events the re-gossip loop sends
	reGossipsWithin := func(duration time.Duration) int {
		count, deadline := 0, time.After(duration)
		for {
			select {
			case event := <-node.events:
				if event.kind == eventRegossip {
					count++
				}
			case <-deadline:
				return count
			}
		}
	}
	if count := reGossipsWithin(time.Second); count < 3 || node.catchUpInterval() != 3*time.Second {
		t.Fatalf("premise: at the genesis timings the loop re-gossips every 50 ms (%d times in a second) and catch-up runs every 3 s (%v)", count, node.catchUpInterval())
	}

	raised := genesis.Parameters
	raised.StepTimeoutMs, raised.BlockIntervalMs = time.Hour.Milliseconds(), (2 * time.Second).Milliseconds()
	vote, _ := ledger.SignParameterVote(testSigner("validator"), "validator", raised, genesis.Parameters.Digest(), 0)
	if pooled, err := node.addParameterVotes(ledger.ParameterChange{NewParameters: raised, PreviousParametersDigest: genesis.Parameters.Digest(), Votes: []ledger.ParameterVote{vote}}); !pooled {
		t.Fatalf("premise: the sole validator's change is pooled (%v)", err)
	}
	if node.startHeight(); node.state.Parameters.StepTimeoutMs != time.Hour.Milliseconds() {
		t.Fatal("premise: block 1 carries the change, and commits")
	}
	reGossipsWithin(200 * time.Millisecond) // the tick that reads the new step timeout
	if count := reGossipsWithin(time.Second); count != 0 {
		t.Errorf("after a vote for a step timeout of an hour, the loop re-gossiped %d times in a second", count)
	}
	if node.reGossipInterval() != time.Hour || node.catchUpInterval() != 10*time.Second {
		t.Errorf("after the vote, the re-gossip interval, half the wait without a sign log, is %v, want an hour, and the catch-up interval is %v, want 10 s",
			node.reGossipInterval(), node.catchUpInterval())
	}

	lowered := raised
	lowered.StepTimeoutMs = ledger.AssumedGossipHopMs
	vote, _ = ledger.SignParameterVote(testSigner("validator"), "validator", lowered, raised.Digest(), 1)
	if pooled, err := node.addParameterVotes(ledger.ParameterChange{NewParameters: lowered, PreviousParametersDigest: raised.Digest(), Sequence: 1, Votes: []ledger.ParameterVote{vote}}); !pooled {
		t.Fatalf("premise: the second change is pooled (%v)", err)
	}
	if node.startHeight(); node.state.Parameters.StepTimeoutMs != ledger.AssumedGossipHopMs {
		t.Fatal("premise: block 2 carries the second change, and commits")
	}
	if count := reGossipsWithin(2 * time.Second); count < 3 {
		t.Errorf("after a vote lowers the step timeout from an hour to 250 ms, the loop re-gossiped %d times in 2 s, want at least 3", count)
	}
}
