package node

import (
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
// in progress and its last block hash, any other path with the checkpoint.
func servePeer(responseWriter http.ResponseWriter, request *http.Request, checkpoint Checkpoint) {
	if request.URL.Path == "/hdls/status" {
		writeJSON(responseWriter, map[string]any{"heightInProgress": checkpoint.Commit.Height + 1, "lastBlockHash": hex.EncodeToString(checkpoint.Commit.BlockHash[:])})
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

// Catch-up asks each peer at the first of its URLs that answers its status, and at no other (review N8):
// a peer's URLs reach one node, so a peer ahead sends its checkpoint once, not once for each URL. Here a
// gossip peer has three URLs: the first refuses connections, the second answers, and the third is never
// asked.
func TestCatchUpAsksEachPeerAtTheFirstURLThatAnswers(t *testing.T) {
	names := []string{"founder", "straggler"}
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	checkpoint := checkpointOf(t, 5, testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0), validatorsOf(names), nil)
	var answeringAsked, laterAsked atomic.Int32
	serve := func(asked *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			asked.Add(1)
			servePeer(responseWriter, request, checkpoint)
		}))
	}
	answering, later := serve(&answeringAsked), serve(&laterAsked)
	defer answering.Close()
	defer later.Close()
	refusing := httptest.NewServer(http.NotFoundHandler())
	refusing.Close() // its address now refuses connections

	recorder := &peeredRecorder{peers: [][]string{{refusing.URL, answering.URL, later.URL}}}
	straggler := New(testConfig(staticChain(names...), "straggler", genesis), recorder)
	if _, _, found := straggler.catchUpCheckpoint(); !found || answeringAsked.Load() != 2 || laterAsked.Load() != 0 {
		t.Errorf("found %v; the URL that answered was asked %d times, want 2, its status and its checkpoint; the URL after it %d times, want none",
			found, answeringAsked.Load(), laterAsked.Load())
	}
}

// The fetches from the other validators' public addresses, a source of catch-up and the pull of public
// bundles, refuse a loopback destination (review N7): a validator could publish http://127.0.0.1:8080,
// and every bank stuck at a height would fetch its own operator port's routes. A private address is
// reached as any other. Here a validator's public face listens on 127.0.0.1, and serves a checkpoint
// ahead and a public bundle: neither a catch-up nor a stuck node's pull asks it, until the test lets
// loopback through.
func TestPublicAddressFetchesRefuseLoopback(t *testing.T) {
	for address, refused := range map[string]bool{"127.0.0.1:8080": true, "127.1.2.3:8080": true, "[::1]:8080": true,
		"[::ffff:127.0.0.1]:8080": true, "0.0.0.0:8080": true, "[::]:8080": true,
		"172.28.70.11:8080": false, "192.168.1.10:443": false, "[2001:db8::1]:443": false} {
		if err := refuseLoopback("tcp", address, nil); (err != nil) != refused {
			t.Errorf("a connection to %s: refused %v, want %v", address, err != nil, refused)
		}
	}

	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	checkpoint := checkpointOf(t, 5, testGenesis(network.genesis, network.timeout, time.Hour), network.validatorSet.Validators(), nil)
	headerOnly := EncodeBundle(BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID}, nil, MaxPublicBundleBytes)
	var asked atomic.Int32
	publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		asked.Add(1)
		if request.URL.Path == "/hdls/bundle" {
			responseWriter.Write(headerOnly) // imports nothing
			return
		}
		servePeer(responseWriter, request, checkpoint)
	}))
	defer publicFace.Close()
	network.genesis[(self+1)%len(network.names)].PublicAddress = publicFace.URL
	node, _ := network.node(self) // without the run loop, its driver runs height 1 from now
	for _, allowLoopback := range []bool{false, true} {
		node.config.allowLoopbackPublicAddresses = allowLoopback
		asked.Store(0)
		_, _, found := node.catchUpCheckpoint()
		node.heightStartedAt = time.Now().Add(-4 * network.timeout) // stuck: the next re-gossip tick pulls
		node.handle(event{kind: eventRegossip})
		waitFor(t, 5*time.Second, func() bool { return !node.pullRoundRunning.Load() }, "a round of pulls never ended")
		if found != allowLoopback || asked.Load() > 0 != allowLoopback {
			t.Errorf("loopback allowed %v: catch-up found the checkpoint %v, and the public face on 127.0.0.1 was asked %d times",
				allowLoopback, found, asked.Load())
		}
	}
}

// The fetches from the other validators' public addresses refuse the node's own machine at an address
// that is not loopback too (review N7): in a container the operator port listens on every interface
// (listen :8080), and some addresses are every bank's own, such as rootless Podman's 10.0.2.100. Here an
// operator port listens on every interface, and serves a public bundle. A plain client reaches it at this
// machine's first IPv4 address that is not loopback; the public-address client is refused there before
// it sends a request.
func TestPublicAddressFetchesRefuseTheNodesOwnMachineAtAnyAddress(t *testing.T) {
	var ownAddress netip.Addr
	interfaceAddresses, _ := net.InterfaceAddrs()
	for _, interfaceAddress := range interfaceAddresses {
		if prefix, err := netip.ParsePrefix(interfaceAddress.String()); err == nil && prefix.Addr().Is4() && !prefix.Addr().IsLoopback() {
			ownAddress = prefix.Addr()
			break
		}
	}
	if !ownAddress.IsValid() {
		t.Skip("this machine has no IPv4 address but loopback ones")
	}
	operatorPort, err := net.Listen("tcp", ":0") // every interface
	if err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	headerOnly := EncodeBundle(BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID}, nil, MaxPublicBundleBytes)
	server := &http.Server{Handler: http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		asked.Add(1)
		responseWriter.Write(headerOnly)
	})}
	go server.Serve(operatorPort)
	defer server.Close()
	ownURL := "http://" + netip.AddrPortFrom(ownAddress, uint16(operatorPort.Addr().(*net.TCPAddr).Port)).String()
	if fetchPublicBundle(NewHTTPClient(5*time.Second), ownURL) == nil || asked.Load() != 1 {
		t.Fatalf("premise: a plain client reaches the operator port at %s", ownURL)
	}

	node := New(testConfig(staticChain("founder"), "founder", testGenesis(nil, time.Second, 0)), NewHTTPTransport(nil))
	if bundle := fetchPublicBundle(node.publicAddressClient(), ownURL); bundle != nil || asked.Load() != 1 {
		t.Errorf("the public-address client reached the node's own operator port at %s, this machine's own address", ownURL)
	}
}

// Catch-up asks every other validator of its trusted frame at its public face, at every call, besides its
// bootstrap and gossip peers (Phase 4 spec §4.4): one public face a call, in turn, as review R2-2 had it,
// let a gossip peer at the node's height and a public face that withholds the newest checkpoint keep the
// node behind for a call. Here a gossip peer answers at the node's height, and two validators' public
// faces each hold a checkpoint ahead, of the same block: at each call both are asked their status, and
// the checkpoint is fetched from one, since the other reports the same block.
func TestCatchUpAsksEveryValidatorsPublicFace(t *testing.T) {
	names := []string{"auditor", "founder", "straggler"} // in name order, as a checkpoint carries them
	ahead := checkpointOf(t, 5, testGenesis([]ledger.Account{testAccount("auditor", "0"), testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0),
		validatorsOf(names), nil)
	var statusAsked, checkpointAsked [2]atomic.Int32 // the requests each public face answered
	founder, auditor := testAccount("founder", "1000"), testAccount("auditor", "0")
	for i, account := range []*ledger.Account{&founder, &auditor} {
		publicFace := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/hdls/status" {
				statusAsked[i].Add(1)
			} else {
				checkpointAsked[i].Add(1)
			}
			servePeer(responseWriter, request, ahead)
		}))
		defer publicFace.Close()
		account.PublicAddress = publicFace.URL
	}
	gossipPeerAtTheNodesHeight := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		writeJSON(responseWriter, map[string]int64{"heightInProgress": 1}) // the node's own height in progress
	}))
	defer gossipPeerAtTheNodesHeight.Close()

	genesis := testGenesis([]ledger.Account{founder, auditor, testAccount("straggler", "0")}, 30*time.Second, 0)
	config := testConfig(staticChain(names...), "straggler", genesis)
	config.allowLoopbackPublicAddresses = true // the public faces listen on 127.0.0.1
	straggler := New(config, &peeredRecorder{peers: [][]string{{gossipPeerAtTheNodesHeight.URL}}})
	for call := range 2 {
		for i := range statusAsked {
			statusAsked[i].Store(0)
			checkpointAsked[i].Store(0)
		}
		checkpoint, _, found := straggler.catchUpCheckpoint()
		if !found || checkpoint.Commit.Height != 5 || statusAsked[0].Load() != 1 || statusAsked[1].Load() != 1 ||
			checkpointAsked[0].Load()+checkpointAsked[1].Load() != 1 {
			t.Errorf("call %d: found the checkpoint %v; the public faces were asked their status %d and %d times, want once each, and for the checkpoint %d times in all, want once",
				call+1, found, statusAsked[0].Load(), statusAsked[1].Load(), checkpointAsked[0].Load()+checkpointAsked[1].Load())
		}
	}
}

// One peer that withholds the newest checkpoint does not keep the node behind (Phase 4 spec §4.4): the
// node asks every peer and every validator's public face, and fetches from those that report the
// highest height first. Here the bootstrap peer serves an older checkpoint, of height 3, which used to be
// taken alone, since a bootstrap peer that yields one was the only one asked; a gossip peer reports
// height 1000, and serves a checkpoint whose state is not the one its commit signs; a validator's public
// face serves the newest, of height 7. One call finds the newest.
func TestOnePeerWithholdingTheNewestCheckpointDoesNotKeepTheNodeBehind(t *testing.T) {
	names := []string{"founder", "straggler"}
	founder := testAccount("founder", "1000")
	stateWith := func(founderAmount string) ledger.LedgerState {
		return testGenesis([]ledger.Account{testAccount("founder", founderAmount), testAccount("straggler", "0")}, 30*time.Second, 0)
	}
	older, newest := checkpointOf(t, 3, stateWith("990"), validatorsOf(names), nil), checkpointOf(t, 7, stateWith("950"), validatorsOf(names), nil)
	damaged := checkpointOf(t, 999, stateWith("900"), validatorsOf(names), nil)
	damaged.State.Accounts = stateWith("1").Accounts // another state than the one its commit signs
	serve := func(checkpoint Checkpoint) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			servePeer(responseWriter, request, checkpoint)
		}))
		t.Cleanup(server.Close)
		return server
	}
	withholding, lying, publicFace := serve(older), serve(damaged), serve(newest)
	founder.PublicAddress = publicFace.URL

	config := testConfig(staticChain(names...), "straggler", testGenesis([]ledger.Account{founder, testAccount("straggler", "0")}, 30*time.Second, 0))
	config.Bootstrap = []string{withholding.URL}
	config.allowLoopbackPublicAddresses = true // the public face listens on 127.0.0.1
	straggler := New(config, &peeredRecorder{peers: [][]string{{lying.URL}}})
	if checkpoint, _, found := straggler.catchUpCheckpoint(); !found || checkpoint.Commit.Height != 7 {
		t.Errorf("catch-up found a checkpoint %v of height %d, want the newest, of height 7, that the public face serves", found, checkpoint.Commit.Height)
	}
}

// Catch-up asks a peer's /hdls/status first, and fetches /hdls/checkpoint, about 2.9 KB per account,
// only from a peer whose height is ahead, or whose last block is not the node's (spec 6.6): on an idle
// chain every peer is at the node's height, on its last block, tick after tick.
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

	for _, nodeHeight := range []int64{6, 1} { // at the peer's height, on its block, then behind it
		node := New(testConfig(staticChain(names...), "straggler", genesis), NewHTTPTransport(nil))
		if nodeHeight == 6 {
			node.adopt(checkpoint, nil)
		}
		if node.height() != nodeHeight {
			t.Fatalf("premise: the node works on height %d, not %d", nodeHeight, node.height())
		}
		mutex.Lock()
		asked = nil
		mutex.Unlock()
		_, _, found := node.bestCheckpoint([]catchUpPeer{{urls: []string{peer.URL}, client: NewHTTPClient(5 * time.Second)}})
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

// No set signed the genesis, so a node there passes no checkpoint of height 0 (trustedFrameOf): a peer
// that reports another genesis, and serves a checkpoint of it that is sound on its own, costs its fetch
// and no more. The check has no set to check it against, and must not stop the node for want of one.
func TestNoCheckpointOfTheGenesisHeightPasses(t *testing.T) {
	names := []string{"founder", "straggler"}
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	otherGenesis := checkpointOf(t, 0, testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "100")}, 30*time.Second, 0), validatorsOf(names), nil)
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		servePeer(responseWriter, request, otherGenesis)
	}))
	defer peer.Close()

	node := New(testConfig(staticChain(names...), "straggler", genesis), NewHTTPTransport(nil))
	if _, _, found := node.bestCheckpoint([]catchUpPeer{{urls: []string{peer.URL}, client: NewHTTPClient(5 * time.Second)}}); found {
		t.Error("catch-up took a checkpoint of the genesis height")
	}
}

// A node follows no redirect (NewHTTPClient): a peer that answers a catch-up request or a gossiped
// message with one has failed, and the URL it names is never asked. Otherwise a peer, or anyone on the
// plain-HTTP path, could send the node's requests on to any URL the node can reach (review follow-up).
func TestNodeFollowsNoRedirect(t *testing.T) {
	names := []string{"founder", "straggler"}
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0)
	checkpoint := checkpointOf(t, 5, testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0), validatorsOf(names), nil)
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		reached.Add(1)
		servePeer(responseWriter, request, checkpoint)
	}))
	defer target.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Redirect(responseWriter, request, target.URL+request.URL.Path, http.StatusTemporaryRedirect) // 307 keeps a POST and its body
	}))
	defer redirecting.Close()

	config := testConfig(staticChain(names...), "straggler", genesis)
	config.Bootstrap = []string{redirecting.URL}
	node := New(config, NewHTTPTransport(nil))
	if _, _, found := node.catchUpCheckpoint(); found || reached.Load() != 0 {
		t.Errorf("catch-up followed a redirect: found a checkpoint %v, the URL it names asked %d times", found, reached.Load())
	}
	NewHTTPTransport(nil).post([]string{redirecting.URL}, "/hdls/vote", []byte("{}"))
	if reached.Load() != 0 {
		t.Error("gossip followed a redirect: the URL it names got the message")
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
