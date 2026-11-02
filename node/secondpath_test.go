package node

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A node pulls the other validators' public bundles (spec §7.7) only while its driver has run the height
// in progress for 4 step timeouts without a commit, and runs one round of pulls at a time. The test plays
// the run loop (handNet): the step timeout is an hour, so no timer fires, and the test sets the time the
// height started by hand. The slow-down switch is on, at ten hours: the 4 step timeouts are the governed
// ones all the same, the re-gossip interval. Of the four validators, two others publish a public
// address, and one other none; the node's own public face must never be asked, nor any but the two.
// Each public face counts the public bundles it serves, and serves only a header, which imports nothing.
//
// The node does not pull as its driver starts, nor a minute before 4 step timeouts; at 4 it does, and the
// next tick, while that round still waits for its answers, skips its own; the tick after, it pulls again.
// Once it has committed the height, it does not pull, nor while it idles at the next height, however
// long; and when work wakes it there, its count starts again.
func TestPublicBundlesArePulledOnlyWhileTheRunningHeightIsStuck(t *testing.T) {
	logged := captureLog(t)
	network := newHandNet(t, 4)
	self := network.nonProposer(1, 2)
	withoutAddress := (self + 1) % len(network.names)
	headerOnly := EncodeBundle(BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID}, nil, MaxPublicBundleBytes)
	fetches := make([]atomic.Int32, len(network.names)) // the public bundles each validator's public face served
	answersReleased := make(chan struct{})              // the public faces answer only once it is closed
	for i := range network.names {
		publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/hdls/bundle" {
				http.NotFound(responseWriter, request)
				return
			}
			fetches[i].Add(1)
			<-answersReleased
			responseWriter.Write(headerOnly)
		}))
		defer publicFace.Close()
		if i != withoutAddress {
			network.genesis[i].PublicAddress = publicFace.URL
		}
	}
	releaseAnswers := sync.OnceFunc(func() { close(answersReleased) })
	defer releaseAnswers() // before the public faces close, which waits for the answers in flight

	totalFetches := func() int32 {
		var total int32
		for i := range fetches {
			total += fetches[i].Load()
		}
		return total
	}
	node, _ := network.node(self) // without the run loop, its driver runs height 1 from now
	node.config.MinimumStepTimeout = 10 * network.timeout
	node.config.allowLoopbackPublicAddresses = true // the public faces listen on 127.0.0.1
	reGossipTick := func() { node.handle(event{kind: eventRegossip}) }
	// noPullAtTick hands the node a re-gossip tick, and fails the test if the node started a round of
	// pulls then: one still running, or one already ended, whose fetches the public faces counted.
	noPullAtTick := func(failure string) {
		t.Helper()
		before := totalFetches()
		reGossipTick()
		if node.pullRoundRunning.Load() || totalFetches() != before {
			t.Fatal(failure)
		}
	}
	roundEnded := func() {
		t.Helper()
		waitFor(t, 5*time.Second, func() bool { return !node.pullRoundRunning.Load() }, "a round of pulls never ended")
	}
	// wantFetches checks that each of the two public faces the node may ask served count bundles, and
	// the others none.
	wantFetches := func(count int32) {
		t.Helper()
		for i := range fetches {
			want := count
			if i == self || i == withoutAddress {
				want = 0
			}
			if served := fetches[i].Load(); served != want {
				t.Errorf("the public face of %s served %d public bundles, want %d", network.name(i), served, want)
			}
		}
	}
	fourStepTimeouts := 4 * network.timeout

	noPullAtTick("the node pulled as soon as its driver started")
	node.heightStartedAt = time.Now().Add(-fourStepTimeouts + time.Minute)
	noPullAtTick("the node pulled before its driver had run the height for 4 step timeouts")

	node.heightStartedAt = time.Now().Add(-fourStepTimeouts)
	reGossipTick()
	if !node.pullRoundRunning.Load() {
		t.Fatal("the node did not pull once its driver had run the height for 4 step timeouts without a commit")
	}
	waitFor(t, 5*time.Second, func() bool { return totalFetches() == 2 }, "the round did not ask the two public faces it may ask, and only them")
	reGossipTick() // the round still waits for its answers: this tick skips its own
	releaseAnswers()
	roundEnded()
	wantFetches(1)
	if !strings.Contains(logged.String(), "public bundles: height 1 has run for 4 step timeouts without a commit") {
		t.Errorf("no line logged that the node started to pull, in:\n%s", logged)
	}

	reGossipTick()
	waitFor(t, 5*time.Second, func() bool { return totalFetches() == 4 }, "the node did not pull again at the next tick")
	roundEnded()
	wantFetches(2)

	network.commit(node, self, 1)
	noPullAtTick("the node pulled after it committed the height")
	if !strings.Contains(logged.String(), "public bundles: the node stops pulling them, having committed height 1") {
		t.Errorf("no line logged that the node stopped pulling, in:\n%s", logged)
	}

	node.handle(event{kind: eventNextHeight, height: node.height()})
	if !node.idle {
		t.Fatal("premise: height 2 has no work, so the node idles there")
	}
	node.heightStartedAt = time.Now().Add(-fourStepTimeouts) // as if it had idled for that long
	noPullAtTick("the node pulled while it idled")
	proposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	if node.driver == nil {
		t.Fatal("premise: the proposal woke the node, and its driver runs height 2")
	}
	noPullAtTick("the node pulled as soon as work woke it: its 4 step timeouts count from then")
}

// secondPathConfigs is the config of each of four validators whose gossip pushes will all fail, but
// whose public faces get through: each one writes its public folder, and an httptest server serves the
// folder's hdls/bundle, hdls/status and hdls/checkpoint, as a fetcher would. The server's URL is the
// validator's public address in the genesis, so the servers start first. They listen on 127.0.0.1, which
// a node refuses to fetch from unless its config lets it (allowLoopbackPublicAddresses). The caller opens
// each node with a transport that delivers nothing. The timings are the second path test's (see
// TestValidatorsWhoseGossipFailsCommitByPullingPublicBundles).
func secondPathConfigs(t *testing.T) []Config {
	t.Helper()
	names := validatorNames(4)
	publicPaths := make([]string, len(names))
	var accounts []ledger.Account
	for i, name := range names {
		publicPaths[i] = t.TempDir()
		publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/hdls/bundle", "/hdls/status", "/hdls/checkpoint":
				http.ServeFile(responseWriter, request, filepath.Join(publicPaths[i], request.URL.Path))
			default:
				http.NotFound(responseWriter, request)
			}
		}))
		t.Cleanup(publicFace.Close) // after the test's deferred calls, which stop the nodes
		if !ledger.CanonicalAddress(publicFace.URL) {
			t.Fatalf("premise: the public face's URL %q is a public address in its one plain form", publicFace.URL)
		}
		account := testAccount(name, "1")
		account.PublicAddress = publicFace.URL
		accounts = append(accounts, account)
	}
	genesis := testGenesis(accounts, 100*time.Millisecond, 0)
	var configs []Config
	for i, name := range names {
		config := testConfig(staticChain(names...), name, genesis)
		config.PublicPath = publicPaths[i]
		config.MinimumStepTimeout = 2 * time.Second
		config.idleHeartbeat = time.Millisecond
		config.allowLoopbackPublicAddresses = true
		configs = append(configs, config)
	}
	return configs
}

// The second path (spec §7.7, §10): four validators whose gossip pushes all fail, but whose public
// bundles can be fetched, commit by pulling them (see secondPathConfigs), and every node decides each
// height it is seen at with the same block: the test notes each node's last commit as the pigeon
// harness does (pigeonRun), every time it asks. Each one's transport delivers nothing.
//
// The governed step timeout is 100 ms, so a node pulls once its driver has run a height for 400 ms, and
// then every 100 ms. The slow-down switch is on, at 2 s, as the banks set it when the network splits
// (spec §7.9): the round timers then outlast the pulls, so a round can complete, its proposal, prevotes
// and precommits each brought by a pull. Without the switch, round 0 never commits by
// pulls: the propose timer fires after one step timeout, and the pull starts after four. Each height
// then runs rounds until their timers outlast a pull, which under the race detector, where writing the
// public bundle at a tick takes about as long as a tick, can take seconds. An idle heartbeat of a
// millisecond keeps every node busy (see runPigeonNetwork), so the chain goes on past block 1, which
// commits with no work, and each height is stuck, and pulled, anew.
func TestValidatorsWhoseGossipFailsCommitByPullingPublicBundles(t *testing.T) {
	const heights = 3
	var nodes []*Node
	for _, config := range secondPathConfigs(t) {
		nodes = append(nodes, New(config, &recordingTransport{})) // delivers nothing
	}
	for _, node := range nodes {
		node.Start()
		defer node.Stop()
	}
	run := newPigeonRun()
	waitFor(t, 20*time.Second, func() bool {
		allCommitted := true
		for _, node := range nodes {
			lastCommit := node.Snapshot().LastCommit
			run.noteLastCommit(node.self, lastCommit)
			allCommitted = allCommitted && lastCommit.Height >= heights
		}
		return allCommitted
	}, "the validators did not all commit by pulling each other's public bundles")
	t.Logf("%d heights seen committed, at the highest rounds %v", run.committedHeights(), run.highestRounds)
	if len(run.forks) > 0 {
		t.Fatalf("nodes decided different blocks: %v", run.forks)
	}
}

// A validator that falls behind while gossip is blocked catches up through the other validators' public
// addresses (review B1), where the fetcher serves hdls/status and hdls/checkpoint. The public bundles
// carry no checkpoint, only what decided the last height, and its other catch-up sources, bootstrap peers
// and gossip peers, are none here, as when the network splits. Three validators of four commit 3 heights
// by the second path (see secondPathConfigs); then the fourth starts, from genesis, with no bootstrap
// peer. Its catch-up takes it to the others' height, and it then commits with them.
func TestValidatorBehindCatchesUpThroughThePublicAddresses(t *testing.T) {
	configs := secondPathConfigs(t)
	var others []*Node
	for _, config := range configs[:3] {
		node := New(config, &recordingTransport{}) // delivers nothing
		node.Start()
		defer node.Stop()
		others = append(others, node)
	}
	othersCommitted := func() int64 {
		lowest := others[0].Snapshot().LastCommit.Height
		for _, node := range others[1:] {
			lowest = min(lowest, node.Snapshot().LastCommit.Height)
		}
		return lowest
	}
	waitFor(t, 20*time.Second, func() bool { return othersCommitted() >= 3 }, "premise: three validators of four commit 3 heights by the second path")

	othersAt := othersCommitted()
	behind := New(configs[3], &recordingTransport{})
	behind.Start()
	defer behind.Stop()
	waitFor(t, 20*time.Second, func() bool { return behind.Snapshot().LastCommit.Height >= othersAt },
		"the validator behind did not catch up through the other validators' public addresses")
	caughtUpAt := behind.Snapshot().LastCommit.Height
	waitFor(t, 20*time.Second, func() bool { return behind.Snapshot().LastCommit.Height > caughtUpAt },
		"once caught up, the validator did not commit with the others")
}

// A pulled public bundle's votes are checked off the loop, and its import stops at the first vote that
// fails (review B4): a hostile public face can make the node's loop check no forged vote. The node has
// committed height 1, and its driver runs height 2. The public bundle it pulls holds a precommit of the
// last commit, of height 1, then a prevote of height 2, both vouched for by the committed view; then a
// prevote whose signature covers another block; then a prevote that would pass. The node takes the
// first two, and never the last.
func TestPulledPublicBundleStopsAtAVoteThatFails(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1, 2)
	var others []int
	for i := range network.names {
		if i != self {
			others = append(others, i)
		}
	}
	var served atomic.Pointer[[]byte] // the public bundle the public face serves
	publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Write(*served.Load())
	}))
	defer publicFace.Close()
	network.genesis[others[0]].PublicAddress = publicFace.URL
	node, _ := network.node(self)
	node.config.allowLoopbackPublicAddresses = true // the public face listens on 127.0.0.1
	network.commit(node, self, 1)
	node.handle(event{kind: eventNextHeight, height: 2})
	proposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal}) // work: the driver starts
	if node.driver == nil {
		t.Fatal("premise: the driver runs height 2")
	}

	lastCommitVote := node.lastCommit.Votes[0]
	vouched := network.vote(others[0], 2, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	forged := network.vote(others[1], 2, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	forged.BlockHash = [32]byte{7} // its signature covers the proposal's block hash, not this one
	after := network.vote(others[2], 2, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	bundle := EncodeBundle(BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID}, []Message{
		{Type: MessageVote, Vote: &lastCommitVote}, {Type: MessageVote, Vote: &vouched}, {Type: MessageVote, Vote: &forged}, {Type: MessageVote, Vote: &after},
	}, MaxPublicBundleBytes)
	served.Store(&bundle)

	node.heightStartedAt = time.Now().Add(-4 * network.timeout) // stuck: the next re-gossip tick pulls
	node.handle(event{kind: eventRegossip})
	waitFor(t, 5*time.Second, func() bool { return !node.pullRoundRunning.Load() }, "the round of pulls never ended")
	handed := len(node.events)
	for len(node.events) > 0 { // the loop's part: what the import handed it
		node.handle(<-node.events)
	}
	if handed != 2 || !node.holdsVote(vouched) || node.holdsVote(after) {
		t.Errorf("the import handed the loop %d votes, want 2; the node holds the prevote before the forged one: %v, want true; the one after: %v, want false",
			handed, node.holdsVote(vouched), node.holdsVote(after))
	}
}

// A pulled public bundle's proposals are checked off the loop as its votes are, and the pull's import
// stops at the first line that fails that check, is unreadable, or that its intake refuses (review R2-1):
// a hostile public face makes the loop check no forged proposal, and spend it one line of anything else,
// not 8 MiB of them at every pull. The node has committed height 1, and its driver runs height 2. An
// honest public bundle passes whole: the last commit's precommits and proposal, of height 1, checked
// against the set that signed it, then the proposal of height 2 and a prevote for it. Each other bundle
// starts with a line that must end the import before the prevote after it: a proposal whose signature
// covers another block, one a validator signed for a round it is not scheduled to propose, an empty
// proposal, a line that is no JSON, one of an unknown type, and a certificate the loop refuses.
func TestPulledPublicBundleStopsAtTheFirstLineThatFails(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1, 2)
	var served atomic.Pointer[[]byte] // the public bundle the public face serves
	publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Write(*served.Load())
	}))
	defer publicFace.Close()
	network.genesis[(self+1)%len(network.names)].PublicAddress = publicFace.URL
	node, _ := network.node(self)
	node.config.allowLoopbackPublicAddresses = true // the public face listens on 127.0.0.1
	network.commit(node, self, 1)
	node.handle(event{kind: eventNextHeight, height: 2})
	proposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal}) // work: the driver starts
	if node.driver == nil {
		t.Fatal("premise: the driver runs height 2")
	}

	// pull has the node pull bundle as a stuck node does, playing the loop meanwhile: it handles each
	// event the import hands it, and counts the proposals and votes among them.
	pull := func(bundle []byte) (handed int) {
		served.Store(&bundle)
		node.heightStartedAt = time.Now().Add(-4 * network.timeout) // stuck: the next re-gossip tick pulls
		node.handle(event{kind: eventRegossip})
		for deadline := time.Now().Add(5 * time.Second); node.pullRoundRunning.Load() || len(node.events) > 0; {
			if time.Now().After(deadline) {
				t.Fatal("the round of pulls never ended")
			}
			select {
			case incoming := <-node.events:
				if incoming.kind == eventProposal || incoming.kind == eventVote {
					handed++
				}
				node.handle(incoming)
			case <-time.After(time.Millisecond):
			}
		}
		return handed
	}
	header := BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID}
	lineOf := func(message Message) string {
		_, line, _ := bytes.Cut(EncodeBundle(header, []Message{message}, MaxPublicBundleBytes), []byte("\n"))
		return string(bytes.TrimSpace(line))
	}
	bundleOf := func(lines ...string) []byte {
		headerLine, _, _ := bytes.Cut(EncodeBundle(header, nil, MaxPublicBundleBytes), []byte("\n"))
		return []byte(string(headerLine) + "\n" + strings.Join(lines, "\n") + "\n")
	}

	scheduled := network.proposer(2, 0)
	prevote := network.vote((scheduled+1)%len(network.names), 2, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	prevoteLine := lineOf(Message{Type: MessageVote, Vote: &prevote})
	var honest []string
	for _, message := range node.lastCommitMessages() {
		honest = append(honest, lineOf(message))
	}
	honest = append(honest, lineOf(Message{Type: MessageProposal, Proposal: &proposal}), prevoteLine)
	if handed := pull(bundleOf(honest...)); handed != len(honest) {
		t.Fatalf("the import handed the loop %d messages of an honest public bundle, want its %d", handed, len(honest))
	}

	forged := proposal
	forged.Proposal.BlockHash = [32]byte{7} // its signature covers the proposal's block hash, not this one
	notScheduled := network.sign((scheduled+2)%len(network.names), 2, 0, proposal.Block)
	for name, firstLine := range map[string]string{
		"a forged proposal":                       lineOf(Message{Type: MessageProposal, Proposal: &forged}),
		"a proposal by a validator not scheduled": lineOf(Message{Type: MessageProposal, Proposal: &notScheduled}),
		"an empty proposal":                       `{"type":"proposal","body":{}}`,
		"a line that is no JSON":                  "not json",
		"a line of an unknown type":               `{"type":"telegram","body":{}}`,
		"a certificate the loop refuses":          `{"type":"parameter-vote","body":{}}`,
	} {
		if handed := pull(bundleOf(firstLine, prevoteLine)); handed != 0 {
			t.Errorf("a public bundle that starts with %s: the import handed the loop %d messages, want none", name, handed)
		}
	}
}

// A pull verifies each vote once at a committed height (review R2-4): a public bundle holds the same
// votes pull after pull, and each check costs the node half a millisecond, at every pull of every bank's
// bundle while it is stuck. A second pull of the same bundle verifies nothing again. A copy of a vote's
// signature on another block hash is not the vote: it is verified, and fails. Once the node commits, the
// same votes, now of its last commit, are verified anew, against the set that signed it.
func TestPullVerifiesEachVoteOnceAHeight(t *testing.T) {
	network := newHandNet(t, 4)
	checks := &atomic.Int64{}
	network.chain.Scheme = countingScheme{Scheme: signing.NetworkScheme(), checks: checks}
	self := network.nonProposer(1)
	node, _ := network.node(self)
	var votes []Message
	for i := range network.names {
		for _, voteType := range []consensus.VoteType{consensus.Prevote, consensus.Precommit} {
			if i != self {
				vote := network.vote(i, 1, 0, voteType, [32]byte{})
				votes = append(votes, Message{Type: MessageVote, Vote: &vote})
			}
		}
	}
	header := BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID}
	// pull imports bundle as a pull does, and returns the signature checks it cost and what it handed
	// the loop, which the test drops.
	pull := func(bundle []byte) (checked int64, handed int) {
		checks.Store(0)
		counts, _ := node.importBundle(bundle, node.pulledCheck())
		for len(node.events) > 0 {
			<-node.events
		}
		return checks.Load(), counts.Queued
	}
	bundle := EncodeBundle(header, votes, MaxPublicBundleBytes)
	if checked, handed := pull(bundle); checked != int64(len(votes)) || handed != len(votes) {
		t.Fatalf("premise: the first pull checked %d signatures and handed the loop %d votes, want %d of each", checked, handed, len(votes))
	}
	if checked, handed := pull(bundle); checked != 0 || handed != len(votes) {
		t.Errorf("the second pull of the same bundle checked %d signatures, want none, and handed the loop %d votes, want %d", checked, handed, len(votes))
	}
	moved := *votes[0].Vote
	moved.BlockHash = [32]byte{7}
	if checked, handed := pull(EncodeBundle(header, []Message{{Type: MessageVote, Vote: &moved}}, MaxPublicBundleBytes)); checked != 1 || handed != 0 {
		t.Errorf("a vote's signature on another block hash cost %d signature checks, want 1, and was handed the loop %d times, want never", checked, handed)
	}
	network.commit(node, self, 1)
	if checked, handed := pull(bundle); checked != int64(len(votes)) || handed != len(votes) {
		t.Errorf("once the node committed, a pull checked %d signatures and handed the loop %d votes, want %d of each", checked, handed, len(votes))
	}
}
