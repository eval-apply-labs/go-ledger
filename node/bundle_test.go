package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// bundleLines reads a bundle's header and its message lines.
func bundleLines(t *testing.T, bundle []byte) (BundleHeader, []BundleLine) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(bundle), "\n"), "\n")
	var header BundleHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("the bundle's first line is not a header: %v: %.200q", err, lines[0])
	}
	var messageLines []BundleLine
	for _, line := range lines[1:] {
		var messageLine BundleLine
		if err := json.Unmarshal([]byte(line), &messageLine); err != nil {
			t.Fatalf("a bundle line is not a message: %v: %.200q", err, line)
		}
		messageLines = append(messageLines, messageLine)
	}
	return header, messageLines
}

// lineTypes is the type of each line, in order.
func lineTypes(lines []BundleLine) []string {
	var types []string
	for _, line := range lines {
		types = append(types, line.Type)
	}
	return types
}

// A bundle exported from one node and imported into another, with no network between them, carries
// what the first holds (spec §7.3, §7.4): its vote and its proposal at the height in progress, its two
// pooled certificates, and an entry of each pool, in that order. The second node takes each through the
// intake of its route, and relays it as it relays gossip, so it then holds every message of the bundle.
// Imported a second time, the bundle takes nothing new and changes nothing: the node's own export is
// the same before and after.
func TestBundleCarriesMessagesWithNoNetwork(t *testing.T) {
	names := validatorNames(4)
	chain := staticChain(names...)
	accounts := []ledger.Account{testAccount("alice", "100"), testAccount("bob", "0"), testAccount("carol", "10"), testAccount("dave", "10")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, time.Hour, 0) // no timer fires during the test
	proposerName := consensus.NewValidatorSet(validatorsOf(names)).ProposerAt(1, 0)
	importerName := names[0]
	if importerName == proposerName {
		importerName = names[1]
	}
	importerTransport := &recordingTransport{}
	exporter := New(testConfig(chain, proposerName, genesis), &recordingTransport{}) // delivers nothing
	importer := New(testConfig(chain, importerName, genesis), importerTransport)
	for _, node := range []*Node{exporter, importer} {
		node.Start()
		defer node.Stop()
	}

	// The exporter proposed block 1 as it started. Each pool entry and certificate comes after, and names
	// block 2, so it stays pooled.
	parameters := genesis.Parameters
	raised := parameters
	raised.TransactionCost = rational("0.2")
	parameterChange := ledger.ParameterChange{NewParameters: raised, PreviousParametersDigest: parameters.Digest()}
	nameAction := ledger.Reassignment{Name: "dave", Freeze: true}
	var nameActionVotes []ledger.ReassignmentVote
	for _, name := range names[:3] { // 3 of 4: the ⅔ quorum
		parameterVote, _ := ledger.SignParameterVote(testSigner(name), name, raised, parameters.Digest(), 0)
		parameterChange.Votes = append(parameterChange.Votes, parameterVote)
		nameActionVote, _ := ledger.SignReassignmentVote(testSigner(name), name, nameAction)
		nameActionVotes = append(nameActionVotes, nameActionVote)
	}
	nameAction.Votes = nameActionVotes
	for kind, admit := range map[string]func() (bool, error){
		"payment": func() (bool, error) { return exporter.AdmitPayment(signedPayment("alice", "bob", "10", 2)) },
		"update": func() (bool, error) {
			return exporter.AdmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "alice", Description: "carried", BlockID: 2}))
		},
		"name request": func() (bool, error) {
			return exporter.AdmitNameRequest(nameRequestOf(ledger.FreezeKind, "carol", "carol", "", "", 2))
		},
		"parameter change": func() (bool, error) { return exporter.AdmitParameterCertificate(parameterChange) },
		"name action":      func() (bool, error) { return exporter.AdmitReassignmentCertificate(nameAction) },
	} {
		if newlyPooled, err := admit(); !newlyPooled {
			t.Fatalf("premise: the exporter pools the %s (%v)", kind, err)
		}
	}

	bundle := exporter.ExportBundle()
	header, lines := bundleLines(t, bundle)
	if want := (BundleHeader{Bundle: 1, NetworkID: testNetworkID, From: proposerName, HeightInProgress: 1}); header != want {
		t.Errorf("the header is %+v, want %+v", header, want)
	}
	if want := []string{"vote", "proposal", "parameter-vote", "reassignment-vote", "name-request", "update", "payment"}; !slices.Equal(lineTypes(lines), want) {
		t.Fatalf("the bundle carries %v, want %v", lineTypes(lines), want)
	}

	counts, err := importer.ImportBundle(bundle)
	if want := (BundleCounts{Queued: 2, Taken: 5}); err != nil || counts != want {
		t.Fatalf("the import counts %+v (error %v), want %+v", counts, err, want)
	}
	held := importer.ExportBundle()
	_, heldLines := bundleLines(t, held)
	for _, line := range lines {
		if !slices.ContainsFunc(heldLines, func(heldLine BundleLine) bool {
			return heldLine.Type == line.Type && bytes.Equal(heldLine.Body, line.Body)
		}) {
			t.Errorf("the importer does not hold the bundle's %s: %.200s", line.Type, line.Body)
		}
	}
	relayed := map[MessageType]int{}
	for messageType := MessageProposal; messageType <= MessageNameRequest; messageType++ {
		relayed[messageType] = importerTransport.sent(messageType)
	}
	// The importer relays each message it took, and sends its own prevote of the proposal it took.
	if want := map[MessageType]int{MessageProposal: 1, MessageVote: 2, MessagePayment: 1, MessageUpdate: 1, MessageNameRequest: 1,
		MessageParameterCertificate: 1, MessageReassignmentCertificate: 1}; !maps.Equal(relayed, want) {
		t.Errorf("the importer sent %v of each message type, want %v", relayed, want)
	}

	counts, err = importer.ImportBundle(bundle)
	if want := (BundleCounts{Queued: 2}); err != nil || counts != want {
		t.Errorf("the second import counts %+v (error %v), want %+v: nothing taken", counts, err, want)
	}
	if again := importer.ExportBundle(); !bytes.Equal(again, held) {
		t.Error("the second import changed what the importer holds")
	}
	for messageType, sent := range relayed {
		if importerTransport.sent(messageType) != sent {
			t.Errorf("the second import relayed a message of type %d again", messageType)
		}
	}
}

// An import counts and skips a line that is no message, and counts a message its intake refuses (spec
// §7.4). It refuses a whole bundle with no header, a header of another format, or another network's,
// and takes nothing from it. The operator port's routes answer the same: 409 with the reason, or 200
// with the counts; and GET /hdls/bundle answers the node's bundle as JSON lines.
func TestBundleImportSkipsBadLinesAndRefusesAnotherNetwork(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "100"), testAccount("bob", "0")}, time.Hour, time.Hour)
	node := New(testConfig(staticChain("validator"), "validator", genesis), &recordingTransport{})
	node.Start()
	defer node.Stop()
	handler := NewServer(node).Handler()
	post := func(bundle string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hdls/bundle", strings.NewReader(bundle)))
		return recorder
	}

	messageLine := func(routeName string, body any) string {
		encodedBody, _ := json.Marshal(body)
		return `{"type":"` + routeName + `","body":` + string(encodedBody) + `}`
	}
	headerLine := func(networkID string) string {
		return fmt.Sprintf(`{"bundle":1,"networkId":%q,"from":"carrier","heightInProgress":1}`, networkID)
	}
	payment := messageLine("payment", signedPayment("alice", "bob", "1", 5))
	for _, refused := range []struct{ bundle, reason string }{
		{"", "no bundle header"},
		{payment + "\n", "no bundle header"}, // a message where the header goes
		{`{"bundle":2,"networkId":"` + testNetworkID + `"}` + "\n" + payment, "no bundle header"}, // another format
		{headerLine("elsewhere") + "\n" + payment, "another network's bundle"},
	} {
		if counts, err := node.ImportBundle([]byte(refused.bundle)); err == nil || !strings.Contains(err.Error(), refused.reason) || counts != (BundleCounts{}) {
			t.Errorf("a bundle refused for %q: counts %+v, error %v", refused.reason, counts, err)
		}
		if answer := post(refused.bundle); answer.Code != http.StatusConflict || !strings.Contains(answer.Body.String(), refused.reason) {
			t.Errorf("POST /hdls/bundle answered a bundle refused for %q with %d %q, want 409 and the reason", refused.reason, answer.Code, answer.Body)
		}
	}

	bundle := strings.Join([]string{
		headerLine(testNetworkID),
		"not json",
		`{"type":"telegram","body":{}}`, // an unknown type
		`{"type":"vote","body":"a string"}`,
		payment, // new to the node: the refused bundles took nothing
		messageLine("payment", signedPayment("bob", "alice", "1", 5)), // bob has nothing to pay with
		"",
	}, "\n")
	answer := post(bundle)
	var counts BundleCounts
	if err := json.Unmarshal(answer.Body.Bytes(), &counts); answer.Code != http.StatusOK || err != nil ||
		counts != (BundleCounts{Taken: 1, Refused: 1, Unreadable: 3}) {
		t.Errorf("POST /hdls/bundle answered %d %q, want 200 and 1 taken, 1 refused and 3 unreadable", answer.Code, answer.Body)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hdls/bundle", nil))
	if contentType := recorder.Header().Get("Content-Type"); recorder.Code != http.StatusOK || contentType != "application/x-ndjson" {
		t.Errorf("GET /hdls/bundle answered %d as %q, want 200 as application/x-ndjson", recorder.Code, contentType)
	}
	if _, lines := bundleLines(t, recorder.Body.Bytes()); !slices.Contains(lineTypes(lines), "payment") {
		t.Errorf("GET /hdls/bundle does not carry the pooled payment: %v", lineTypes(lines))
	}
}

// An export stops at the last whole line within its bound (spec §7.2), in the order of §7.3, and leaves
// out a message of no known type.
func TestBundleEndsAtTheLastWholeLineWithinItsBound(t *testing.T) {
	header := BundleHeader{Bundle: BundleVersion, NetworkID: testNetworkID, From: "bank-a", HeightInProgress: 7}
	first, second := signedPayment("alice", "bob", "1", 7), signedPayment("alice", "bob", "2", 7)
	messages := []Message{{Type: MessagePayment, Payment: &first}, {Type: MessageType(99)}, {Type: MessagePayment, Payment: &second}}
	whole := EncodeBundle(header, messages, MaxBundleBytes)
	if _, lines := bundleLines(t, whole); len(lines) != 2 {
		t.Fatalf("the bundle has %d message lines, want the two payments", len(lines))
	}
	cut := EncodeBundle(header, messages, len(whole)-1)
	if _, lines := bundleLines(t, cut); len(lines) != 1 || !bytes.HasPrefix(whole, cut) {
		t.Errorf("one byte short of the whole, the bundle is %d bytes with %d message lines, want the first payment's line alone", len(cut), len(lines))
	}
}

// The consensus part of a bundle (spec §7.3) goes in this order: the last commit, the votes of the
// current round, its proposal, then the other votes of the height, the past rounds' newest first (review
// B3), then the future rounds' nearest first (review R2-3). So the public bundle of a bank stuck for many
// rounds, cut at its bound, drops the future rounds and then the oldest rounds first, and never the
// current round's votes or proposal; and a rogue validator's votes for rounds ahead never push the recent
// past rounds past the cut. Here the node commits height 1, runs rounds of height 2 to nil, then takes
// the proposal of the next round and its prevotes, and one validator's votes for the two rounds after; a
// bound of half the bundle keeps the current round's votes and proposal. The node gives the same bundle
// each time.
func TestBundleCarriesTheCurrentRoundFirstAndCutsTheOldestRounds(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1, 2)
	node, _ := network.node(self)
	network.commit(node, self, 1)
	node.handle(event{kind: eventNextHeight, height: 2})
	currentRound := int32(0)
	for ; currentRound < 5 || network.proposer(2, currentRound) == self; currentRound++ {
		network.voteAll(node, self, 2, currentRound, consensus.Prevote, [32]byte{})
		network.voteAll(node, self, 2, currentRound, consensus.Precommit, [32]byte{})
	}
	proposal := network.proposal(node, 2, currentRound, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	network.voteAll(node, self, 2, currentRound, consensus.Prevote, proposal.Proposal.BlockHash)
	ahead := (self + 1) % len(network.names) // one validator of four: too few to make the driver skip a round
	for _, round := range []int32{currentRound + 2, currentRound + 1} {
		for _, voteType := range []consensus.VoteType{consensus.Prevote, consensus.Precommit} {
			vote := network.vote(ahead, 2, round, voteType, [32]byte{})
			node.handle(event{kind: eventVote, vote: &vote})
		}
	}
	if node.round() != currentRound || !node.holdsVote(network.vote(ahead, 2, currentRound+2, consensus.Prevote, [32]byte{})) {
		t.Fatalf("premise: the driver runs round %d, want %d, and holds the votes of the rounds after", node.round(), currentRound)
	}
	heldInTheCurrentRound := 0
	for _, vote := range node.heightVotes {
		if vote.Round == currentRound {
			heldInTheCurrentRound++
		}
	}

	whole := node.bundle(false, MaxBundleBytes)
	_, lines := bundleLines(t, whole)
	const lastCommit, currentVotes, currentProposal, pastVotes, futureVotes = 0, 1, 2, 3, 4 // the parts, in order
	part, previousRound, carriedInTheCurrentRound := lastCommit, currentRound, 0
	for i, line := range lines {
		var vote consensus.Vote
		var proposal ProposalMessage
		var height int64
		var round int32
		if line.Type == "vote" && json.Unmarshal(line.Body, &vote) == nil {
			height, round = vote.Height, vote.Round
		} else if line.Type == "proposal" && json.Unmarshal(line.Body, &proposal) == nil {
			height, round = proposal.Proposal.Height, proposal.Proposal.Round
		} else {
			t.Fatalf("line %d is no vote or proposal: %.200s", i, line.Body)
		}
		linePart := pastVotes
		switch {
		case height == 1:
			linePart = lastCommit
		case line.Type == "proposal" && round == currentRound:
			linePart = currentProposal
		case line.Type == "vote" && round == currentRound:
			linePart, carriedInTheCurrentRound = currentVotes, carriedInTheCurrentRound+1
		case line.Type == "vote" && round > currentRound:
			linePart = futureVotes
		}
		if linePart < part {
			t.Errorf("line %d, a %s of height %d round %d, comes after a later part of the bundle", i, line.Type, height, round)
		}
		if linePart == part && (linePart == pastVotes && round > previousRound || linePart == futureVotes && round < previousRound) {
			t.Errorf("line %d, a vote of round %d, comes after a vote of round %d: the past rounds go newest first, and the future rounds nearest first",
				i, round, previousRound)
		}
		part = max(part, linePart)
		if linePart >= pastVotes {
			previousRound = round
		}
	}
	if part != futureVotes || carriedInTheCurrentRound != heldInTheCurrentRound {
		t.Errorf("the bundle carries %d of the %d votes of round %d, and ends in part %d, want the future rounds' votes last", carriedInTheCurrentRound,
			heldInTheCurrentRound, currentRound, part)
	}

	cut := node.bundle(false, len(whole)/2)
	_, cutLines := bundleLines(t, cut)
	carriesTheCurrentProposal := slices.ContainsFunc(cutLines, func(line BundleLine) bool {
		var cutProposal ProposalMessage
		return line.Type == "proposal" && json.Unmarshal(line.Body, &cutProposal) == nil && cutProposal.Proposal == proposal.Proposal
	})
	if !bytes.HasPrefix(whole, cut) || len(cutLines) >= len(lines) || !carriesTheCurrentProposal {
		t.Errorf("cut at half its size, the bundle keeps %d of %d lines, without the proposal of round %d", len(cutLines), len(lines), currentRound)
	}
	if again := node.bundle(false, MaxBundleBytes); !bytes.Equal(again, whole) {
		t.Error("the node gave another bundle from the same messages")
	}
}
