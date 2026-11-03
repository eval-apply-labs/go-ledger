package node

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// publicFile is the content of the file at path in node's public folder, once the writer is idle, or
// nil when there is none.
func publicFile(t *testing.T, node *Node, path string) []byte {
	t.Helper()
	node.publicFolder.waitUntilIdle()
	data, err := os.ReadFile(filepath.Join(node.config.PublicPath, path))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

// publicFileInformation is what os.Stat says of the file at path in node's public folder, once the
// writer is idle. A file the writer writes again is a new file renamed into place, so os.SameFile tells
// a file written again from one left alone.
func publicFileInformation(t *testing.T, node *Node, path string) os.FileInfo {
	t.Helper()
	node.publicFolder.waitUntilIdle()
	information, err := os.Stat(filepath.Join(node.config.PublicPath, path))
	if err != nil {
		t.Fatal(err)
	}
	return information
}

// recordOfFile is the name record a public folder's resolve file holds.
func recordOfFile(t *testing.T, data []byte) NameRecord {
	t.Helper()
	var record NameRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("a resolve file does not hold a record: %v: %q", err, data)
	}
	return record
}

// The public folder holds hdls/bundle, the public bundle (spec §7.7): the consensus part of the node's
// bundle, without its pools. A commit writes it, and so does each re-gossip tick: here a certificate
// pooled in the hour-long post-commit window reaches it with no commit, and a pooled payment never does.
func TestPublicFolderHoldsThePublicBundle(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "10")},
		250*time.Millisecond, time.Hour) // commits block 1, then holds height 2 for the whole test, re-gossiping every 250 ms
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.PublicPath = filepath.Join(t.TempDir(), "public")
	node := New(config, NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")

	header, lines := bundleLines(t, publicFile(t, node, "hdls/bundle"))
	if want := (BundleHeader{Bundle: 1, NetworkID: testNetworkID, From: "validator", HeightInProgress: 2}); header != want {
		t.Errorf("the public bundle's header is %+v, want %+v", header, want)
	}
	if want := []string{"vote", "proposal"}; !slices.Equal(lineTypes(lines), want) {
		t.Errorf("after block 1 the public bundle carries %v, want its last commit: %v", lineTypes(lines), want)
	}

	if _, err := node.AdmitPayment(signedPayment("alice", "validator", "1", 5)); err != nil {
		t.Fatalf("premise: the payment is pooled: %v", err)
	}
	parameters := node.Snapshot().Parameters
	proposed := parameters
	proposed.TransactionCost = rational("0.5")
	vote, _ := ledger.SignParameterVote(testSigner("validator"), "validator", proposed, parameters.Digest(), 0)
	if _, err := node.AdmitParameterCertificate(ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: parameters.Digest(),
		Votes: []ledger.ParameterVote{vote}}); err != nil {
		t.Fatalf("premise: the certificate is pooled: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		_, lines := bundleLines(t, publicFile(t, node, "hdls/bundle"))
		return slices.Equal(lineTypes(lines), []string{"vote", "proposal", "parameter-vote"})
	}, "no re-gossip tick wrote the pooled certificate, and only it, into the public bundle")
	if _, lines := bundleLines(t, node.ExportBundle()); !slices.Contains(lineTypes(lines), "payment") {
		t.Error("premise: the operator port's bundle carries the pooled payment")
	}
}

// After a commit, hdls/status, hdls/checkpoint, hdls/accounts and hdls/governance are the answers of
// their routes byte for byte (spec §6.1), and hdls/governance follows a certificate the node pools. The
// files are public, 0644 in folders of 0755, and once the writer is idle .incoming is empty.
func TestPublicFolderHoldsTheRouteAnswers(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "10")},
		5*time.Second, time.Hour) // commits block 1, then holds height 2 for the whole test
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.PublicPath = filepath.Join(t.TempDir(), "public") // created by Open
	node := New(config, NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	server := httptest.NewServer(NewServer(node).Handler())
	defer server.Close()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")

	routeAnswer := func(route string) []byte {
		response, err := http.Get(server.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return body
	}
	for _, route := range []string{"/hdls/status", "/hdls/checkpoint", "/hdls/accounts", "/hdls/governance"} {
		if answer, file := routeAnswer(route), publicFile(t, node, route[1:]); !bytes.Equal(answer, file) {
			t.Errorf("the public folder's %s is not the route's answer:\nfile   %.200q\nanswer %.200q", route[1:], file, answer)
		}
	}
	var status struct{ HeightInProgress int64 }
	if json.Unmarshal(publicFile(t, node, "hdls/status"), &status); status.HeightInProgress != 2 {
		t.Errorf("hdls/status shows the height in progress %d, want 2, after block 1", status.HeightInProgress)
	}

	parameters := node.Snapshot().Parameters
	proposed := parameters
	proposed.TransactionCost = rational("0.5")
	vote, _ := ledger.SignParameterVote(testSigner("validator"), "validator", proposed, parameters.Digest(), 0)
	if _, err := node.AdmitParameterCertificate(ledger.ParameterChange{NewParameters: proposed, PreviousParametersDigest: parameters.Digest(),
		Votes: []ledger.ParameterVote{vote}}); err != nil {
		t.Fatalf("premise: the certificate is pooled: %v", err)
	}
	var governance GovernanceView
	if file := publicFile(t, node, "hdls/governance"); !bytes.Equal(file, routeAnswer("/hdls/governance")) ||
		json.Unmarshal(file, &governance) != nil || len(governance.ParameterProposals) != 1 {
		t.Errorf("hdls/governance does not follow the pooled certificate: %.300q", file)
	}

	for _, folder := range []string{"", "hdls", nameRecordFolder, keyRecordFolder, incomingFolder} {
		information, err := os.Stat(filepath.Join(config.PublicPath, folder))
		if err != nil {
			t.Fatal(err)
		}
		if mode := information.Mode().Perm(); mode != 0o755 {
			t.Errorf("the folder %q has the mode %v, want 0755", folder, mode)
		}
	}
	for _, path := range []string{"hdls/status", "hdls/checkpoint", nameRecordPath("alice")} {
		if mode := publicFileInformation(t, node, path).Mode(); mode != 0o644 {
			t.Errorf("%s has the mode %v, want 0644", path, mode)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(config.PublicPath, incomingFolder)); err != nil || len(entries) != 0 {
		t.Errorf(".incoming holds %d files once the writer is idle (error %v), want none", len(entries), err)
	}
}

// A name's record files are written when a commit changes the record, and only then; a commit that
// changes only balances leaves them alone. When the key changes, the file of the old key goes and the
// new key's comes.
func TestPublicFolderRewritesANameRecordOnlyWhenItChanges(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("bank-a", "100"), banked("alice", "10", "bank-a"), banked("bob", "10", "bank-a")},
		time.Hour, time.Hour)
	genesis.Parameters.ChangeDelayBlocks = 1
	config := testConfig(staticChain("bank-a"), "bank-a", genesis)
	config.PublicPath = t.TempDir()
	node := New(config, &recordingTransport{}) // driven by hand: a sole validator commits a height within startHeight
	defer node.Stop()
	alice, _ := ledger.FindAccount(genesis.Accounts, "alice")
	oldKeyPath, newKeyPath := keyRecordPath(alice.PublicKey), keyRecordPath(testSigner("alice-new").PublicKeyBase64())

	opened := recordOfFile(t, publicFile(t, node, nameRecordPath("alice")))
	if opened.Bank != "bank-a" || opened.PublicKey != alice.PublicKey {
		t.Fatalf("alice's record at Open: %+v", opened)
	}
	if !bytes.Equal(publicFile(t, node, oldKeyPath), publicFile(t, node, nameRecordPath("alice"))) {
		t.Fatal("alice's key file does not hold her record")
	}
	untouched := map[string]os.FileInfo{} // the files of the names a block leaves alone, as they were before it
	keepUntouched := func(names ...string) {
		for _, name := range names {
			untouched[name] = publicFileInformation(t, node, nameRecordPath(name))
		}
	}
	checkUntouched := func(block string) {
		for name, before := range untouched {
			if !os.SameFile(before, publicFileInformation(t, node, nameRecordPath(name))) {
				t.Errorf("%s: %s's record file was written again, but the record did not change", block, name)
			}
		}
	}

	// Block 1 files alice's key change, due at the end of block 2, and moves her fee: her record changes,
	// bank-a's and bob's do not.
	keepUntouched("bank-a", "bob")
	aliceBefore := publicFileInformation(t, node, nameRecordPath("alice"))
	if err := admitRequest(node, nameRequestOf(ledger.KeyChangeKind, "alice", "alice", "bank-a", "alice-new", 1)); err != nil {
		t.Fatalf("premise: alice's key change is pooled: %v", err)
	}
	node.startHeight()
	pending := recordOfFile(t, publicFile(t, node, nameRecordPath("alice")))
	if pending.PendingKey != testSigner("alice-new").PublicKeyBase64() || pending.PendingHeight != 2 ||
		os.SameFile(aliceBefore, publicFileInformation(t, node, nameRecordPath("alice"))) {
		t.Errorf("block 1: alice's record file shows the pending key %.8s due at %d, want alice-new's due at 2",
			pending.PendingKey, pending.PendingHeight)
	}
	if !bytes.Equal(publicFile(t, node, oldKeyPath), publicFile(t, node, nameRecordPath("alice"))) {
		t.Error("block 1: alice's key file does not follow her record")
	}
	checkUntouched("block 1")

	// Block 2: the key changes. The old key's file goes, and the new key's holds the record.
	node.startHeight()
	changed := recordOfFile(t, publicFile(t, node, nameRecordPath("alice")))
	if changed.PublicKey != testSigner("alice-new").PublicKeyBase64() || changed.PendingKey != "" {
		t.Errorf("block 2: alice's record file shows the key %.8s, pending %.8s; want alice-new's, none",
			changed.PublicKey, changed.PendingKey)
	}
	if publicFile(t, node, oldKeyPath) != nil {
		t.Error("block 2: the file of alice's old key is still there")
	}
	if !bytes.Equal(publicFile(t, node, newKeyPath), publicFile(t, node, nameRecordPath("alice"))) {
		t.Error("block 2: the file of alice's new key does not hold her record")
	}
	checkUntouched("block 2")
}

// At Open the folder may hold another state's files. Open removes the resolve files of no current name
// or key, writes every current one, and leaves nothing in .incoming. A record file the folder holds as it
// is now it leaves alone, and one that differs it writes again (review B7). A catch-up to a state without
// a name removes that name's files.
func TestPublicFolderAtOpenAndAfterACatchUp(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "10"), testAccount("bob", "10"), testAccount("carol", "10")},
		time.Hour, time.Hour)
	genesis.Height = 7
	folder := t.TempDir()
	writeStale := func(path string, value any) {
		data, _ := json.Marshal(value)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(folder, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, path), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := ledger.FindAccount(genesis.Accounts, "alice")
	bob, _ := ledger.FindAccount(genesis.Accounts, "bob")
	writeStale(nameRecordPath("alice"), nameRecordOf(alice)) // as alice's record is now
	writeStale(keyRecordPath(alice.PublicKey), nameRecordOf(alice))
	bob.Description = "an older description"
	writeStale(nameRecordPath("bob"), nameRecordOf(bob))
	ghost := testAccount("ghost", "1")
	writeStale(nameRecordPath("ghost"), nameRecordOf(ghost))
	writeStale(keyRecordPath(ghost.PublicKey), nameRecordOf(ghost))
	writeStale(keyRecordPath(testSigner("bob-old").PublicKeyBase64()), nameRecordOf(bob))
	writeStale(incomingFolder+"/next", "a write a crash cut short")
	heldBefore := map[string]os.FileInfo{} // the record files as the folder held them before Open
	for _, path := range []string{nameRecordPath("alice"), keyRecordPath(alice.PublicKey), nameRecordPath("bob")} {
		information, err := os.Stat(filepath.Join(folder, path))
		if err != nil {
			t.Fatal(err)
		}
		heldBefore[path] = information
	}

	config := testConfig(staticChain("validator"), "validator", genesis)
	config.PublicPath = folder
	node := New(config, &recordingTransport{})
	defer node.Stop()
	for path, before := range heldBefore {
		if writtenAgain, want := !os.SameFile(before, publicFileInformation(t, node, path)), path == nameRecordPath("bob"); writtenAgain != want {
			t.Errorf("Open wrote %s again: %v, want %v (only a record that changed)", path, writtenAgain, want)
		}
	}
	for _, path := range []string{nameRecordPath("ghost"), keyRecordPath(ghost.PublicKey), keyRecordPath(testSigner("bob-old").PublicKeyBase64())} {
		if publicFile(t, node, path) != nil {
			t.Errorf("Open left the stale file %s", path)
		}
	}
	for _, account := range genesis.Accounts {
		if record := recordOfFile(t, publicFile(t, node, nameRecordPath(account.Name))); !reflect.DeepEqual(record, nameRecordOf(account)) {
			t.Errorf("%s's record at Open: %+v, want the current record", account.Name, record)
		}
		if !bytes.Equal(publicFile(t, node, keyRecordPath(account.PublicKey)), publicFile(t, node, nameRecordPath(account.Name))) {
			t.Errorf("%s's key file does not hold the name's record", account.Name)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(folder, incomingFolder)); len(entries) != 0 {
		t.Errorf(".incoming holds %d files after Open, want none", len(entries))
	}

	withoutCarol := genesis
	withoutCarol.Accounts = slices.DeleteFunc(slices.Clone(genesis.Accounts), func(account ledger.Account) bool { return account.Name == "carol" })
	carol, _ := ledger.FindAccount(genesis.Accounts, "carol")
	node.adopt(checkpointOf(t, 9, withoutCarol, validatorsOf([]string{"validator"}), nil), nil)
	if node.height() != 10 {
		t.Fatalf("premise: the node adopted the checkpoint of height 9, it works on %d", node.height())
	}
	if publicFile(t, node, nameRecordPath("carol")) != nil || publicFile(t, node, keyRecordPath(carol.PublicKey)) != nil {
		t.Error("the files of carol, gone from the state the node caught up to, are still there")
	}
	var status struct{ HeightInProgress int64 }
	if json.Unmarshal(publicFile(t, node, "hdls/status"), &status); status.HeightInProgress != 10 {
		t.Errorf("hdls/status shows the height in progress %d after the catch-up, want 10", status.HeightInProgress)
	}
}

// Two banks of one chain publish the same record files for a name, whatever the height each learned
// the record at (review B5): here bank A catches up to the state of height 3, where alice's description
// changed, and bank B, from the same genesis, to the same state at height 9, as a bank that was away
// longer does. The record used to say the height a bank first held it at: 3 at bank A, 9 at bank B.
func TestTwoBanksPublishTheSameRecordFiles(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "10")}, time.Hour, time.Hour)
	changed := genesis
	changed.Accounts = slices.Clone(genesis.Accounts)
	for i := range changed.Accounts {
		if changed.Accounts[i].Name == "alice" {
			changed.Accounts[i].Description = "changed at height 3"
		}
	}
	var banks []*Node
	for _, height := range []int64{3, 9} {
		config := testConfig(staticChain("validator"), "validator", genesis)
		config.PublicPath = t.TempDir()
		bank := New(config, &recordingTransport{})
		defer bank.Stop()
		bank.adopt(checkpointOf(t, height, changed, validatorsOf([]string{"validator"}), nil), nil)
		if bank.height() != height+1 {
			t.Fatalf("premise: the bank adopted the checkpoint of height %d, it works on %d", height, bank.height())
		}
		banks = append(banks, bank)
	}
	alice, _ := ledger.FindAccount(changed.Accounts, "alice")
	for _, path := range []string{nameRecordPath("alice"), keyRecordPath(alice.PublicKey)} {
		atA, atB := publicFile(t, banks[0], path), publicFile(t, banks[1], path)
		if recordOfFile(t, atA).Description != alice.Description || !bytes.Equal(atA, atB) {
			t.Errorf("%s differs between two banks of one chain: bank A's ends %q, bank B's %q", path, atA[max(0, len(atA)-40):], atB[max(0, len(atB)-40):])
		}
	}
}

// No partial file is ever visible: while a node commits block after block, a reader that keeps reading
// the public folder finds every file whole, since a file is only ever renamed into place. Once the
// writer is idle, nothing is left in .incoming.
func TestNoPartialPublicFileIsEverVisible(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("alice", "10")}, time.Second, time.Millisecond)
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.PublicPath = t.TempDir()
	node := New(config, NewHTTPTransport(nil))
	node.Start()
	keepBusy(t, "alice", node)

	var reads, partialReads atomic.Int64
	stopReading, readerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stopReading:
				return
			default:
			}
			for _, path := range []string{"hdls/status", "hdls/checkpoint", "hdls/accounts", "hdls/governance", nameRecordPath("alice")} {
				if data, err := os.ReadFile(filepath.Join(config.PublicPath, path)); err == nil {
					reads.Add(1)
					if !json.Valid(data) {
						partialReads.Add(1)
					}
				}
			}
		}
	}()
	waitFor(t, 10*time.Second, func() bool { return node.Snapshot().Height > 30 }, "the node did not commit 30 blocks")
	close(stopReading)
	<-readerDone
	node.Stop() // which waits until the writer is idle
	if reads.Load() == 0 || partialReads.Load() != 0 {
		t.Errorf("%d of %d reads found a partial file", partialReads.Load(), reads.Load())
	}
	if entries, err := os.ReadDir(filepath.Join(config.PublicPath, incomingFolder)); err != nil || len(entries) != 0 {
		t.Errorf(".incoming holds %d files once the writer is idle (error %v), want none", len(entries), err)
	}
}

// The public bundle is written again only when it changes (review B7): an idle node, whose bundle stays
// the same, does not write it at every re-gossip tick.
func TestPublicBundleIsNotWrittenAgainWhileUnchanged(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100")}, 50*time.Millisecond, 0) // a re-gossip tick every 50 ms
	config := testConfig(staticChain("validator"), "validator", genesis)
	config.PublicPath = t.TempDir()
	node := New(config, NewHTTPTransport(nil))
	node.Start()
	defer node.Stop()
	waitFor(t, 5*time.Second, func() bool { return node.Snapshot().Height == 2 }, "block 1 was not committed")
	time.Sleep(200 * time.Millisecond) // the node idles at height 2: nothing in its bundle changes
	before, content := publicFileInformation(t, node, "hdls/bundle"), publicFile(t, node, "hdls/bundle")
	time.Sleep(time.Second) // about 20 ticks
	if node.Snapshot().Height != 2 || !bytes.Equal(content, publicFile(t, node, "hdls/bundle")) {
		t.Fatal("premise: the node idles at height 2, and its public bundle stays the same")
	}
	if !os.SameFile(before, publicFileInformation(t, node, "hdls/bundle")) {
		t.Error("the idle node wrote its unchanged public bundle again")
	}
}

// The writer writes one file at a time, in the order first queued. A file queued again before its write
// starts keeps its place and takes the newer content, and a removal is queued in order with the writes.
func TestPublicFolderWriterKeepsTheNewestContentOfEachFile(t *testing.T) {
	folder := t.TempDir()
	for _, path := range []string{incomingFolder, "hdls"} {
		if err := os.MkdirAll(filepath.Join(folder, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(folder, "hdls/old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	writer := newPublicFolderWriter(folder)
	writer.writing = true // as if a write were in flight: what is queued now waits for it
	writer.write("hdls/first", []byte("1"))
	writer.write("hdls/second", []byte("2"))
	writer.remove("hdls/first")
	writer.write("hdls/first", []byte("1 again"))
	writer.remove("hdls/old")
	writer.write("hdls/second", []byte("2 again"))
	if !slices.Equal(writer.queue, []string{"hdls/first", "hdls/second", "hdls/old"}) {
		t.Errorf("the queue is %v, want each path once, in the order first queued", writer.queue)
	}
	writer.writeQueue() // the write in flight ends, and takes the queue
	writer.waitUntilIdle()
	for path, want := range map[string]string{"hdls/first": "1 again", "hdls/second": "2 again"} {
		if data, err := os.ReadFile(filepath.Join(folder, path)); err != nil || string(data) != want {
			t.Errorf("%s holds %q (error %v), want %q", path, data, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, "hdls/old")); !os.IsNotExist(err) {
		t.Errorf("hdls/old was not removed: %v", err)
	}
	if writer.writing || len(writer.queue) != 0 || len(writer.pending) != 0 {
		t.Error("the writer is not idle once its queue is written")
	}
}

// Two writers that share one public folder, by mistake, never write each other's files (review B6): each
// write goes through a temporary file of its own in .incoming. With one temporary name for every write,
// one writer's rename moved the other's file into place, or found its own file gone.
func TestTwoWritersOnOneFolderKeepTheirFilesWhole(t *testing.T) {
	folder := t.TempDir()
	for _, path := range []string{incomingFolder, "hdls"} {
		if err := os.MkdirAll(filepath.Join(folder, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logged := captureLog(t)
	first, second := newPublicFolderWriter(folder), newPublicFolderWriter(folder)
	firstContent, secondContent := bytes.Repeat([]byte("1"), 1<<20), bytes.Repeat([]byte("2"), 1<<16)
	wrongRounds := 0
	for range 200 {
		var writes sync.WaitGroup
		writes.Go(func() { first.writeFile("hdls/first", firstContent) })
		writes.Go(func() { second.writeFile("hdls/second", secondContent) })
		writes.Wait()
		firstHeld, _ := os.ReadFile(filepath.Join(folder, "hdls/first"))
		secondHeld, _ := os.ReadFile(filepath.Join(folder, "hdls/second"))
		if !bytes.Equal(firstHeld, firstContent) || !bytes.Equal(secondHeld, secondContent) {
			wrongRounds++
		}
	}
	if wrongRounds > 0 || strings.Contains(logged.String(), "public folder:") {
		t.Errorf("in %d of 200 rounds a file did not hold its writer's content; the log:\n%.1000s", wrongRounds, logged)
	}
	if entries, err := os.ReadDir(filepath.Join(folder, incomingFolder)); err != nil || len(entries) != 0 {
		t.Errorf(".incoming holds %d files once the writers are done (error %v), want none", len(entries), err)
	}
}

// A node whose bootstrap peer serves only the static files, as a fetcher does (spec §6.1: hdls/status
// and hdls/checkpoint keep the routes' paths and formats), catches up from them.
func TestNodeCatchesUpFromThePublicFolder(t *testing.T) {
	chain := staticChain("founder")
	genesis := testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("recipient", "0")}, 5*time.Second, 20*time.Millisecond)
	founderConfig := testConfig(chain, "founder", genesis)
	founderConfig.PublicPath = t.TempDir()
	founderNode := New(founderConfig, NewHTTPTransport(nil))
	founderNode.SubmitPayment(signedPayment("founder", "recipient", "10", 1))
	founderNode.Start()
	defer founderNode.Stop()
	keepBusy(t, "founder", founderNode)
	waitFor(t, 4*time.Second, func() bool {
		snapshot := founderNode.Snapshot()
		return snapshot.Height >= 3 && balance(snapshot.Accounts, "recipient").Sign() > 0
	}, "the founder did not commit the payment")

	staticFiles := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/hdls/status" && request.URL.Path != "/hdls/checkpoint" {
			http.NotFound(responseWriter, request)
			return
		}
		http.ServeFile(responseWriter, request, filepath.Join(founderConfig.PublicPath, request.URL.Path))
	}))
	defer staticFiles.Close()

	joinerConfig := testConfig(chain, "joiner", genesis)
	joinerConfig.Bootstrap = []string{staticFiles.URL}
	joinerNode := New(joinerConfig, NewHTTPTransport(nil))
	joinerNode.Start()
	defer joinerNode.Stop()
	waitFor(t, 4*time.Second, func() bool {
		snapshot := joinerNode.Snapshot()
		return snapshot.Height >= 2 && balance(snapshot.Accounts, "recipient").Cmp(rational("9.9")) == 0
	}, "the joiner did not catch up from the static files")
}

// Open fails when it can't create the public folder: an operator who set publicPath wants the files.
func TestOpenRefusesAPublicFolderItCannotCreate(t *testing.T) {
	notAFolder := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notAFolder, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	config := testConfig(staticChain("validator"), "validator", testGenesis([]ledger.Account{testAccount("validator", "100")}, time.Hour, time.Hour))
	config.PublicPath = filepath.Join(notAFolder, "public")
	if _, err := Open(config, NewHTTPTransport(nil)); err == nil {
		t.Fatal("Open took a public path under a file")
	}
}
