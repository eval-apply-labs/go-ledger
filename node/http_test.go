package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// End-to-end over real HTTP: four nodes, each behind an httptest server, exchange proposals and
// votes as JSON and must reach consensus and apply a payment. This also exercises JSON
// round-tripping of blocks (big.Rat amounts) and votes ([32]byte digests).
func TestNodesReachConsensusOverHTTP(t *testing.T) {
	const nodeCount = 4
	names := validatorNames(nodeCount)
	accounts := []ledger.Account{testAccount("alice", "100"), testAccount("bob", "0")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, 5*time.Second, 0)

	transports := make([]*HTTPTransport, nodeCount)
	nodes := make([]*Node, nodeCount)
	servers := make([]*httptest.Server, nodeCount)
	for i := 0; i < nodeCount; i++ {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(testConfig(staticChain(names...), names[i], genesis), transports[i])
		servers[i] = httptest.NewServer(NewServer(nodes[i]).Handler())
	}
	defer func() {
		for _, server := range servers {
			server.Close()
		}
	}()
	for i := 0; i < nodeCount; i++ {
		var peers [][]string
		for j := 0; j < nodeCount; j++ {
			if j != i {
				peers = append(peers, []string{servers[j].URL})
			}
		}
		transports[i].SetPeers(peers)
	}

	payment := signedPayment("alice", "bob", "10", 1)
	for _, node := range nodes {
		node.SubmitPayment(payment)
	}
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()

	deadline := time.Now().Add(6 * time.Second)
	var snapshots []Snapshot
	for {
		snapshots = snapshots[:0]
		allAdvanced := true
		for _, node := range nodes {
			snapshot := node.Snapshot()
			snapshots = append(snapshots, snapshot)
			if snapshot.Height < 2 {
				allAdvanced = false
			}
		}
		if allAdvanced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes did not reach height 2 over HTTP in time; heights=%v", heights(snapshots))
		}
		time.Sleep(20 * time.Millisecond)
	}

	want := ledger.AccountsDigest(snapshots[0].Accounts)
	for i, snapshot := range snapshots {
		if ledger.AccountsDigest(snapshot.Accounts) != want {
			t.Fatalf("node %d diverged over HTTP", i)
		}
	}
	if bobBalance := balance(snapshots[0].Accounts, "bob"); bobBalance.Cmp(rational("9.9")) != 0 {
		t.Fatalf("bob = %s, want 9.9 over HTTP", bobBalance.FloatString(2))
	}
}

// A peer is the list of its URLs (spec §4), so a validator that publishes a second IP address, a second
// line to the internet, stays reachable when the first refuses connections: the transport tries the
// next URL. It moves on only when a URL gives no answer. An answer, even an error status, means the
// peer was reached, and the URLs after it get nothing.
func TestTransportTriesAPeersNextAddressWhenOneRefusesConnections(t *testing.T) {
	refusing := httptest.NewServer(http.NotFoundHandler())
	refusingURL := refusing.URL
	refusing.Close() // nothing listens at its address any more, so a connection to it is refused
	var mutex sync.Mutex
	askedPaths := map[string][]string{} // the paths each server was asked for, by its name
	startServer := func(name string, status int) string {
		server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			mutex.Lock()
			askedPaths[name] = append(askedPaths[name], request.URL.Path)
			mutex.Unlock()
			responseWriter.WriteHeader(status)
		}))
		t.Cleanup(server.Close)
		return server.URL
	}
	asked := func(name string) []string {
		mutex.Lock()
		defer mutex.Unlock()
		return slices.Clone(askedPaths[name])
	}
	secondURL, thirdURL := startServer("second", http.StatusAccepted), startServer("third", http.StatusAccepted)
	failingURL := startServer("failing", http.StatusInternalServerError)

	transport := NewHTTPTransport(nil)
	transport.SetPeers([][]string{{refusingURL, secondURL, thirdURL}})
	transport.Broadcast("self", Message{Type: MessageVote, Vote: &consensus.Vote{Height: 1}})
	waitFor(t, 5*time.Second, func() bool { return len(asked("second")) > 0 }, "the vote did not reach the peer's second URL once its first refused the connection")
	if paths := asked("second"); !slices.Equal(paths, []string{"/hdls/vote"}) {
		t.Errorf("the second URL was asked for %v, want the vote route once", paths)
	}

	transport.post([]string{failingURL, thirdURL}, "/hdls/vote", []byte("{}"))
	if failed, third := asked("failing"), asked("third"); len(failed) != 1 || len(third) != 0 {
		t.Errorf("the URL that answered 500 was asked %d times and the URL after it %d times, want once and never", len(failed), len(third))
	}
}

// A peer whose pushes fail is logged once when they start failing and once when it answers again, never
// once per message (spec §5.4, Q10), and at most once a minute (review N4): so a bank whose connections
// the others' allow lists refuse finds it in its log, as does the operator of a peer that went down. A
// change within the minute after a line waits for the next push after it. The test lets a minute pass
// by moving the last line's time back.
func TestTransportLogsAPeerThatStartsFailingAndAnswersAgain(t *testing.T) {
	var refusing atomic.Bool
	refusing.Store(true)
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if refusing.Load() { // the connection closes with no answer, as when an allow list refuses it
			connection, _, _ := responseWriter.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		responseWriter.WriteHeader(http.StatusAccepted)
	}))
	defer peer.Close()
	logged := captureLog(t)
	transport := NewHTTPTransport([]string{peer.URL})
	pushThreeVotes := func() {
		for range 3 {
			transport.post(transport.Peers()[0], "/hdls/vote", []byte("{}"))
		}
	}
	linesSaying := func(phrase string) int {
		count := 0
		for _, line := range strings.Split(logged.String(), "\n") {
			if strings.Contains(line, peer.URL) && strings.Contains(line, phrase) {
				count++
			}
		}
		return count
	}

	aMinutePasses := func() {
		transport.peerLogMutex.Lock()
		defer transport.peerLogMutex.Unlock()
		lastLine := transport.peerLogLines[peer.URL]
		lastLine.loggedAt = lastLine.loggedAt.Add(-peerLogInterval)
		transport.peerLogLines[peer.URL] = lastLine
	}

	for _, step := range []struct {
		aMinutePassed, refusing    bool
		wantFailing, wantAnswering int
	}{{false, true, 1, 0}, {false, false, 1, 0}, {true, false, 1, 1}, {false, true, 1, 1}, {true, true, 2, 1}} {
		if step.aMinutePassed {
			aMinutePasses()
		}
		refusing.Store(step.refusing)
		pushThreeVotes()
		if failing, answering := linesSaying("fail"), linesSaying("answers again"); failing != step.wantFailing || answering != step.wantAnswering {
			t.Fatalf("after three pushes with the peer refusing %t, a minute passed %t: %d lines say the pushes fail and %d that the peer answers again, want %d and %d: %q",
				step.refusing, step.aMinutePassed, failing, answering, step.wantFailing, step.wantAnswering, logged.String())
		}
	}
}

// A peer that flaps, one push failing and the next answered, costs one log line a minute, not one per
// message (review N4).
func TestTransportLogsAFlappingPeerOnceAMinute(t *testing.T) {
	var pushes atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if pushes.Add(1)%2 == 0 {
			connection, _, _ := responseWriter.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		responseWriter.WriteHeader(http.StatusAccepted)
	}))
	defer peer.Close()
	logged := captureLog(t)
	transport := NewHTTPTransport([]string{peer.URL})
	for range 100 {
		transport.post(transport.Peers()[0], "/hdls/vote", []byte("{}"))
	}
	if lines := strings.Count(logged.String(), "\n"); lines != 1 || !strings.Contains(logged.String(), "fail") {
		t.Errorf("100 pushes to a peer that flaps wrote %d log lines, want one, that its pushes fail", lines)
	}
}

// Once a request is sent, the transport never sends it again at the peer's next URL (review N3): a peer
// that took a message but answers too slowly gets it once, not once per URL. The test shortens the
// client's 5-second timeout.
func TestTransportSendsASlowPeerAMessageOnce(t *testing.T) {
	var received atomic.Int32
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		received.Add(1)
		<-release // the peer took the request, but its loop is busy past the sender's timeout
		responseWriter.WriteHeader(http.StatusAccepted)
	}))
	defer slow.Close()
	defer close(release)
	transport := NewHTTPTransport(nil)
	transport.client.Timeout = 200 * time.Millisecond
	// The same peer at two URLs: 127.0.0.1 and localhost name one listener here, as a bank's two lines would.
	transport.post([]string{slow.URL, strings.Replace(slow.URL, "127.0.0.1", "localhost", 1)}, "/hdls/vote", []byte("{}"))
	if count := received.Load(); count != 1 {
		t.Errorf("a peer that took a push but answered too slowly received it %d times, want once", count)
	}
}

// The decentralized name server: GET /hdls/resolve turns a name into the account's current public key,
// its bank of record, whether it is frozen, AND where it is reached, its publicAddress and its
// ipAddresses, each only when set (or the reverse, by key), each answer naming the committed height
// and digest it was read from.
func TestResolveEndpointServesNameAndAddress(t *testing.T) {
	alice := testAccount("alice", "50")
	alice.Bank, alice.Description, alice.PublicAddress, alice.IPAddresses = "validator", "the bakery", "https://bakery.example", []string{"10.0.0.7", "2001:db8::7"}
	carol := testAccount("carol", "1")
	carol.Frozen = true
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), alice, carol},
		5*time.Second, time.Hour) // commits block 1, then holds height 2 for the whole test
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")

	answers := map[string]string{} // the body of each answer, by its query
	resolve := func(query string) (int, ResolveRecord) {
		response, err := http.Get(server.URL + "/hdls/resolve" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		answers[query] = string(body)
		var record ResolveRecord
		_ = json.Unmarshal(body, &record)
		return response.StatusCode, record
	}

	status, record := resolve("?name=alice")
	if status != http.StatusOK || record.PublicKey != alice.PublicKey || record.Bank != "validator" || record.Frozen ||
		record.PublicAddress != "https://bakery.example" || !slices.Equal(record.IPAddresses, alice.IPAddresses) || record.Description != "the bakery" ||
		!strings.Contains(answers["?name=alice"], `"publicAddress":"https://bakery.example","ipAddresses":["10.0.0.7","2001:db8::7"]`) {
		t.Fatalf("resolve by name: status=%d record=%+v", status, record)
	}
	if status, record := resolve("?name=carol"); status != http.StatusOK || !record.Frozen || record.Bank != "" {
		t.Fatalf("a frozen name must resolve as frozen: status=%d record=%+v", status, record)
	}
	if answer := answers["?name=carol"]; strings.Contains(answer, "publicAddress") || strings.Contains(answer, "ipAddresses") || strings.Contains(answer, `"address"`) {
		t.Errorf("the answer for a name that publishes no address names one: %s", answer)
	}
	// The answer names the state it was read from: block 1 and the digest of the state it committed,
	// not the height the node is working on.
	if stateDigest := committedState(node.Snapshot()).Digest(); record.Height != 1 || record.StateDigest != hex.EncodeToString(stateDigest[:]) {
		t.Fatalf("the answer must name the committed height and its digest, got height %d digest %q", record.Height, record.StateDigest)
	}
	if status, record = resolve("?key=" + url.QueryEscape(alice.PublicKey)); status != http.StatusOK || record.Name != "alice" {
		t.Fatalf("reverse lookup by key: status=%d record=%+v", status, record)
	}
	if status, _ = resolve("?name=nobody"); status != http.StatusNotFound {
		t.Fatalf("an unregistered name must 404, got %d", status)
	}
	if status, _ = resolve(""); status != http.StatusBadRequest {
		t.Fatalf("no selector must 400, got %d", status)
	}
	if status, _ = resolve("?name=alice&key=x"); status != http.StatusBadRequest {
		t.Fatalf("both selectors must 400, got %d", status)
	}
}

// /hdls/status serves the effective network id the node signs with (spec 2.3), which a wallet pins on
// first use.
func TestStatusServesTheNetworkId(t *testing.T) {
	node := New(testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, 5*time.Second, time.Hour)), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	recorder := httptest.NewRecorder()
	NewServer(node).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hdls/status", nil))
	var status struct {
		NetworkID string `json:"networkId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil || status.NetworkID != testNetworkID {
		t.Fatalf("the status serves the network id %q (err %v), want %q", status.NetworkID, err, testNetworkID)
	}
}

// Each route takes only its method: a read only GET, a message only POST. The router answers 405 to
// another method before any handler runs; the reads used to answer any method.
func TestRoutesAnswer405ToAnotherMethod(t *testing.T) {
	node := New(testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, 5*time.Second, time.Hour)), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	handler := NewServer(node).Handler()
	for _, path := range []string{"/hdls/status", "/hdls/accounts", "/hdls/governance", "/hdls/resolve?name=validator", "/hdls/record", "/hdls/lifelong-record"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s answered %d, want 405", path, recorder.Code)
		}
	}
	for _, path := range []string{"/hdls/proposal", "/hdls/vote", "/hdls/payment", "/hdls/update", "/hdls/name-request", "/hdls/parameter-vote", "/hdls/reassignment-vote", "/hdls/pay"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s answered %d, want 405", path, recorder.Code)
		}
	}
}

// The two ports (spec §3): the gossip port serves only the routes validators use, and answers 404 to a
// wallet's payment, the account list, a name lookup, governance, the record, the bundle routes and a
// checkpoint's import, all of which the operator port serves. Both serve the gossip routes.
func TestGossipPortServesOnlyTheGossipRoutes(t *testing.T) {
	node := New(testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, 5*time.Second, time.Hour)), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	gossipHandler, operatorHandler := NewServer(node).GossipHandler(), NewServer(node).Handler()
	answer := func(handler http.Handler, route string) int {
		method, path, _ := strings.Cut(route, " ")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader("{}")))
		return recorder.Code
	}
	for _, route := range []string{"POST /hdls/pay", "GET /hdls/accounts", "GET /hdls/resolve?name=validator", "GET /hdls/governance",
		"GET /hdls/record", "GET /hdls/record?from=0", "GET /hdls/lifelong-record", "GET /hdls/bundle", "POST /hdls/bundle", "POST /hdls/checkpoint"} {
		if code := answer(gossipHandler, route); code != http.StatusNotFound {
			t.Errorf("the gossip port answered %s with %d, want 404", route, code)
		}
		if code := answer(operatorHandler, route); code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			t.Errorf("the operator port answered %s with %d, want it served", route, code)
		}
	}
	for _, route := range []string{"POST /hdls/proposal", "POST /hdls/vote", "POST /hdls/payment", "POST /hdls/update", "POST /hdls/name-request",
		"POST /hdls/parameter-vote", "POST /hdls/reassignment-vote", "GET /hdls/status", "GET /hdls/checkpoint"} {
		for portName, handler := range map[string]http.Handler{"gossip": gossipHandler, "operator": operatorHandler} {
			if code := answer(handler, route); code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
				t.Errorf("the %s port answered %s with %d, want it served", portName, route, code)
			}
		}
	}
}

// /hdls/status serves the committed view, off the event loop (C13): every wallet page and every catch-up
// tick asks it, and it used to queue an event on the loop and copy the account list there. It answers
// even while the loop runs nothing, and names the height it serves heightInProgress, one above the
// committed height, where /hdls/resolve's height is the committed height (C74).
func TestStatusServesTheHeightInProgressOffTheLoop(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("shop", "0"), testAccount("validator", "100")}, time.Second, 0)
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(config.SnapshotPath, snapshotFile(checkpointOf(t, 3, genesis, validatorsOf([]string{"validator"}), nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := Open(config, NewHTTPTransport(nil)) // opened at height 4, and never started: no loop runs
	if err != nil {
		t.Fatal(err)
	}
	defer node.Stop()
	answered := make(chan []byte, 1)
	go func() {
		recorder := httptest.NewRecorder()
		NewServer(node).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hdls/status", nil))
		answered <- recorder.Body.Bytes()
	}()
	select {
	case body := <-answered:
		var status map[string]any
		if err := json.Unmarshal(body, &status); err != nil {
			t.Fatal(err)
		}
		if status["heightInProgress"] != 4.0 || status["accounts"] != 2.0 || status["networkId"] != testNetworkID || status["height"] != nil {
			t.Errorf("the status is %s, want heightInProgress 4, 2 accounts, the network id and no height key", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/hdls/status waited for the event loop")
	}
}

// The certificate routes answer as the update route does: 202 once the certificate is pooled, now or
// before, and 409 with the reason when the node refuses it. They used to answer 202 either way, so the
// admin page reported a refused freeze or seizure as submitted (C8).
func TestCertificateRefusalAnswers409(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("coop", "10"), testAccount("validator", "100")},
		5*time.Second, time.Hour) // commits block 1, then holds height 2 for the whole test
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")
	post := func(path string, certificate any) (int, string) {
		body, _ := json.Marshal(certificate)
		response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		reason, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(reason)
	}

	raised := genesis.Parameters
	raised.TransactionCost = rational("0.2")
	change := ledger.ParameterChange{NewParameters: raised, PreviousParametersDigest: genesis.Parameters.Digest()}
	if status, reason := post("/hdls/parameter-vote", change); status != http.StatusConflict || !strings.Contains(reason, "do not reach the ⅔ quorum") {
		t.Errorf("a parameter change without votes was answered %d %q, want 409 with the reason", status, reason)
	}
	parameterVote, _ := ledger.SignParameterVote(testSigner("validator"), "validator", raised, change.PreviousParametersDigest, 0)
	change.Votes = []ledger.ParameterVote{parameterVote}
	for range 2 { // a repeat of a pooled certificate is accepted again
		if status, reason := post("/hdls/parameter-vote", change); status != http.StatusAccepted {
			t.Errorf("a complete parameter change was answered %d %q, want 202", status, reason)
		}
	}

	seizure := ledger.Reassignment{Name: "coop", PreviousAmount: rational("11"), NewAmount: rational("0")} // the coop holds 10
	reassignmentVote, _ := ledger.SignReassignmentVote(testSigner("validator"), "validator", seizure)
	seizure.Votes = []ledger.ReassignmentVote{reassignmentVote}
	if status, reason := post("/hdls/reassignment-vote", seizure); status != http.StatusConflict || !strings.Contains(reason, "its previous amount is not the balance the name holds") {
		t.Errorf("a seizure of a balance the name does not hold was answered %d %q, want 409 with the reason", status, reason)
	}
	seizure.PreviousAmount = rational("10")
	reassignmentVote, _ = ledger.SignReassignmentVote(testSigner("validator"), "validator", seizure)
	seizure.Votes = []ledger.ReassignmentVote{reassignmentVote}
	if status, reason := post("/hdls/reassignment-vote", seizure); status != http.StatusAccepted {
		t.Errorf("a complete seizure was answered %d %q, want 202", status, reason)
	}
}

// The proposal route decodes up to 2 MiB (spec 4.11): a block's content takes up to 768 KiB, and its
// evidence and a governance certificate come on top. Every other route keeps 1 MiB.
func TestProposalRouteTakesTwoMebibytes(t *testing.T) {
	node := New(testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, time.Second, 0)), NewHTTPTransport(nil))
	handler := NewServer(node).Handler()
	post := func(path string, body any) int {
		data, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
		return recorder.Code
	}
	proposalOfSize := func(commentBytes int) ProposalMessage {
		payment := ledger.Payment{From: "validator", To: "validator", Amount: rational("1"), Comment: strings.Repeat("x", commentBytes)}
		return ProposalMessage{Block: ledger.Block{Content: ledger.BlockContent{Payments: []ledger.Payment{payment}}}}
	}
	if code := post("/hdls/proposal", proposalOfSize(3<<19)); code != http.StatusAccepted {
		t.Errorf("a 1.5 MiB proposal was answered %d, want 202", code)
	}
	if code := post("/hdls/proposal", proposalOfSize(5<<19)); code != http.StatusBadRequest {
		t.Errorf("a 2.5 MiB proposal was answered %d, want 400", code)
	}
	if code := post("/hdls/payment", proposalOfSize(3 << 19).Block.Content.Payments[0]); code != http.StatusBadRequest {
		t.Errorf("a 1.5 MiB payment was answered %d, want 400", code)
	}
}

// committedState is the state a node committed last, from its snapshot: one below the height it works on.
func committedState(snapshot Snapshot) ledger.LedgerState {
	return ledger.LedgerState{Accounts: snapshot.Accounts, Parameters: snapshot.Parameters, GovernanceSequence: snapshot.GovernanceSequence, Height: snapshot.Height - 1}
}

// /hdls/resolve serves the digest of the committed state, LedgerState.Digest() (spec 6.10, E38): the
// value a payment's pin takes, so a payment pinned to it for the next block is admitted.
func TestResolveServesTheStateDigest(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("payer", "100")},
		5*time.Second, time.Hour) // commits block 1, then holds height 2 for the whole test
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")

	response, err := http.Get(server.URL + "/hdls/resolve?name=payer")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var record ResolveRecord
	if err := json.NewDecoder(response.Body).Decode(&record); err != nil {
		t.Fatal(err)
	}
	stateDigest := committedState(node.Snapshot()).Digest()
	if record.StateDigest != hex.EncodeToString(stateDigest[:]) {
		t.Fatalf("resolve serves %q, want the committed state's digest %x", record.StateDigest, stateDigest)
	}
	pinned, _ := ledger.SignPayment(testSigner("payer"), ledger.Payment{From: "payer", To: "validator", Amount: rational("1"), BlockID: 2, PreviousStateDigest: record.StateDigest})
	if _, err := node.AdmitPayment(pinned); err != nil {
		t.Fatalf("a payment pinned to the digest resolve serves was refused: %v", err)
	}
}

// Regression: a standard base64 public key contains '+' in most cases, and query parsing decodes
// '+' to a space — so a raw (unescaped) key in ?key= used to match nothing and 404. handleResolve
// restores the '+', so the reverse lookup works with a key pasted verbatim.
func TestResolveToleratesRawPlusInKey(t *testing.T) {
	scheme := signing.NetworkScheme()
	validatorSigner := testSigner("validator")
	var alice *signing.Signer
	for i := 0; i < 4000 && alice == nil; i++ {
		if candidate, _ := signing.Generate(scheme, "testnet"); strings.Contains(candidate.PublicKeyBase64(), "+") {
			alice = candidate
		}
	}
	if alice == nil {
		t.Skip("no key containing '+' generated")
	}
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), {Name: "alice", PublicKey: alice.PublicKeyBase64(), Amount: rational("50")}}, 5*time.Second, 0)
	node := New(Config{Chain: staticChain("validator"), Signer: validatorSigner, Name: "validator", Genesis: genesis}, NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()

	// The key is placed raw in the query (its '+' NOT percent-encoded) — exactly what a client pasting
	// a key does. Without the fix its '+' arrives as a space and matches no account.
	response, err := http.Get(server.URL + "/hdls/resolve?key=" + alice.PublicKeyBase64())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var record ResolveRecord
	_ = json.NewDecoder(response.Body).Decode(&record)
	if response.StatusCode != http.StatusOK || record.Name != "alice" {
		t.Fatalf("a raw key with '+' must resolve: status=%d record=%+v", response.StatusCode, record)
	}
}

// Regression: a zero block interval (unset in config, or governed to 0) with a single-validator
// network used to recurse commit→startHeight→decide→commit in one call stack until it overflowed.
// The next height must always be scheduled through the loop, never re-entered synchronously.
func TestZeroBlockIntervalDoesNotRecurse(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100")}, 5*time.Second, 0) // a block interval of 0
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	keepBusy(t, "validator", node)
	waitFor(t, 4*time.Second, func() bool { return node.Snapshot().Height >= 20 },
		"a solo validator with a zero interval should commit freely (and not crash)")
}
