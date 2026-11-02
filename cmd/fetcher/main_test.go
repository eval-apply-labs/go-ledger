package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The files an engine whose height in progress is 200 would have written.
const (
	testStatus      = `{"accounts":2,"heightInProgress":200,"networkId":"village/00ab"}`
	testCheckpoint  = `{"checkpoint":"the committed state, its block header and its commit"}`
	testAccounts    = `[{"name":"alice"},{"name":"bank"}]`
	testGovernance  = `{"parameters":{}}`
	testBundle      = `{"bundle":1,"networkId":"village/00ab","from":"bank","heightInProgress":200}`
	testNameRecord  = `{"name":"alice","publicKey":"…","bank":"bank","frozen":false}`
	testUnit        = "{\"Height\":0}\n{\"Height\":1}\n"
	testLifelong    = "{\"Height\":7}\n"
	testSecret      = "a file outside the folders"
	testIncoming    = "a write the engine has not renamed into place yet"
	firstUnit       = "00000000000000000000.jsonl"
	secondUnit      = "00000000000000000100.jsonl"
	unitInProgress  = "00000000000000000200.jsonl"
	unitAsDirectory = "00000000000000000300.jsonl"
)

// testKey is the SHA-256 of alice's public key, in hex: the name of her key's record.
var testKey = strings.Repeat("ab", 32)

// testFolders is where writeTestFolders put an engine's files.
type testFolders struct {
	publicFolder, recordFolder, lifelongRecordPath string
}

// writeTestFolders writes a public folder, a record folder and a lifelong record file as an engine
// would, and around them what a request must never reach: a folder, a file being written in .incoming,
// temporary files, and a secret beside the folders.
func writeTestFolders(t *testing.T) testFolders {
	root := t.TempDir()
	folders := testFolders{
		publicFolder:       filepath.Join(root, "public"),
		recordFolder:       filepath.Join(root, "snapshot.json.0011223344556677.record"),
		lifelongRecordPath: filepath.Join(root, "snapshot.json.0011223344556677.lifelong.jsonl"),
	}
	files := map[string]string{
		filepath.Join(root, "secret"):                                                testSecret,
		filepath.Join(folders.publicFolder, "secret"):                                testSecret,
		filepath.Join(folders.publicFolder, "hdls", "status"):                        testStatus,
		filepath.Join(folders.publicFolder, "hdls", "checkpoint"):                    testCheckpoint,
		filepath.Join(folders.publicFolder, "hdls", "accounts"):                      testAccounts,
		filepath.Join(folders.publicFolder, "hdls", "governance"):                    testGovernance,
		filepath.Join(folders.publicFolder, "hdls", "bundle"):                        testBundle,
		filepath.Join(folders.publicFolder, "hdls", "resolve", "name", "alice"):      testNameRecord,
		filepath.Join(folders.publicFolder, "hdls", "resolve", "name", "alice.tmp"):  testIncoming,
		filepath.Join(folders.publicFolder, "hdls", "resolve", "key", testKey):       testNameRecord,
		filepath.Join(folders.publicFolder, ".incoming", "status"):                   testIncoming,
		filepath.Join(folders.publicFolder, "hdls", ".incoming", "status"):           testIncoming,
		filepath.Join(folders.publicFolder, "hdls", "resolve", "name", ".incoming"):  testIncoming,
		filepath.Join(folders.recordFolder, firstUnit):                               testUnit,
		filepath.Join(folders.recordFolder, secondUnit):                              testUnit,
		filepath.Join(folders.recordFolder, unitInProgress):                          testUnit,
		filepath.Join(folders.recordFolder, firstUnit+".tmp"):                        testIncoming,
		filepath.Join(folders.recordFolder, ".incoming"):                             testIncoming,
		folders.lifelongRecordPath:                                                   testLifelong,
		filepath.Join(folders.publicFolder, "hdls", "resolve", "name", "carol", "x"): testSecret, // carol is a folder
		filepath.Join(folders.recordFolder, unitAsDirectory, "x"):                    testSecret,
	}
	for filePath, content := range files {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return folders
}

// newTestFetcher serves folders, forwarding submissions to engineURL with the given cap.
func newTestFetcher(t *testing.T, folders testFolders, engineURL string, maxSubmissionsPerSecond int) *Fetcher {
	parsedEngineURL, err := url.Parse(engineURL)
	if err != nil {
		t.Fatal(err)
	}
	return NewFetcher(folders.publicFolder, folders.recordFolder, folders.lifelongRecordPath, parsedEngineURL, maxSubmissionsPerSecond)
}

// serve sends one request, its target as it would stand on the request line, raw, so ".." and %2e
// reach the fetcher as a client wrote them.
func serve(handler http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// Every listed path is served, by GET and by HEAD, with its file's bytes, its type and its
// Cache-Control (spec 6.2, 6.3), the checkpoint's as short as the status's (review W2). The status says
// 200 is in progress, so the units of 0 and 100 are complete and the unit of 200 is still growing.
func TestFetcherServesEveryListedPath(t *testing.T) {
	fetcher := newTestFetcher(t, writeTestFolders(t), "http://engine.invalid", 100)
	for _, test := range []struct {
		target, wantBody, wantContentType, wantCacheControl string
	}{
		{"/hdls/status", testStatus, "application/json", "max-age=1"},
		{"/hdls/checkpoint", testCheckpoint, "application/json", "max-age=1"},
		{"/hdls/accounts", testAccounts, "application/json", "max-age=5"},
		{"/hdls/governance", testGovernance, "application/json", "max-age=5"},
		{"/hdls/bundle", testBundle, "application/x-ndjson", "max-age=1"},
		{"/hdls/resolve/name/alice", testNameRecord, "application/json", "max-age=5"},
		{"/hdls/resolve/key/" + testKey, testNameRecord, "application/json", "max-age=5"},
		{"/hdls/record/" + firstUnit, testUnit, "application/x-ndjson", "max-age=31536000, immutable"},
		{"/hdls/record/" + secondUnit, testUnit, "application/x-ndjson", "max-age=31536000, immutable"},
		{"/hdls/record/" + unitInProgress, testUnit, "application/x-ndjson", "max-age=5"},
		{"/hdls/lifelong-record.jsonl", testLifelong, "application/x-ndjson", "max-age=5"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := serve(fetcher, method, test.target, nil)
			wantBody := test.wantBody
			if method == http.MethodHead {
				wantBody = ""
			}
			if response.Code != http.StatusOK || response.Body.String() != wantBody {
				t.Errorf("%s %s: %d %q, want 200 %q", method, test.target, response.Code, response.Body.String(), wantBody)
			}
			if got := response.Header().Get("Content-Type"); got != test.wantContentType {
				t.Errorf("%s %s: Content-Type %q, want %q", method, test.target, got, test.wantContentType)
			}
			if got := response.Header().Get("Cache-Control"); got != test.wantCacheControl {
				t.Errorf("%s %s: Cache-Control %q, want %q", method, test.target, got, test.wantCacheControl)
			}
		}
	}
}

// A record unit is complete only when its first height + 100 ≤ the height in progress the status
// gives. With no status, an unreadable one, or one past the read bound, no unit is complete.
func TestRecordUnitIsImmutableOnlyOnceComplete(t *testing.T) {
	folders := writeTestFolders(t)
	fetcher := newTestFetcher(t, folders, "http://engine.invalid", 100)
	statusPath := filepath.Join(folders.publicFolder, "hdls", "status")
	cacheControlOf := func(unit string) string {
		response := serve(fetcher, http.MethodGet, "/hdls/record/"+unit, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d, want 200", unit, response.Code)
		}
		return response.Header().Get("Cache-Control")
	}
	const complete, growing = "max-age=31536000, immutable", "max-age=5"
	for _, test := range []struct {
		status       string // "" removes the file
		unit         string
		wantComplete bool
	}{
		{`{"heightInProgress":200}`, secondUnit, true},  // 100 + 100 ≤ 200: height 199 is committed
		{`{"heightInProgress":199}`, secondUnit, false}, // height 199 is not
		{`{"heightInProgress":199}`, firstUnit, true},
		{"", firstUnit, false},
		{`not json`, firstUnit, false},
		{`{"heightInProgress":"200"}`, firstUnit, false},
		{`{"padding":"` + strings.Repeat("x", maxStatusBytes) + `","heightInProgress":200}`, firstUnit, false},
	} {
		os.Remove(statusPath)
		if test.status != "" {
			if err := os.WriteFile(statusPath, []byte(test.status), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want := growing
		if test.wantComplete {
			want = complete
		}
		if got := cacheControlOf(test.unit); got != want {
			t.Errorf("status %.40q, unit %s: Cache-Control %q, want %q", test.status, test.unit, got, want)
		}
	}
}

// Anything but a listed path is 404, and another method on a listed path is 405 with an Allow header,
// as the node's router answers. Nothing outside the listed files is ever sent: not a folder, not
// .incoming or a temporary file, not a path that climbs out with "..", raw or escaped. None of these
// refusals carries a Cache-Control: no file can ever be there.
func TestFetcherRefusesEverythingElse(t *testing.T) {
	stub := &engineStub{}
	engine := httptest.NewServer(stub)
	defer engine.Close()
	fetcher := newTestFetcher(t, writeTestFolders(t), engine.URL, 100)
	for _, test := range []struct {
		method, target string
		wantCode       int
		wantAllow      string
	}{
		// Unknown paths, and the engine's routes the public face does not serve.
		{"GET", "/", 404, ""},
		{"GET", "/secret", 404, ""},
		{"GET", "/hdls/nothing", 404, ""},
		{"GET", "/hdls/resolve?name=alice", 404, ""},
		{"GET", "/hdls/record?from=0", 404, ""},
		{"GET", "/hdls/lifelong-record", 404, ""},
		{"POST", "/hdls/vote", 404, ""},
		{"POST", "/hdls/proposal", 404, ""},
		{"POST", "/hdls/parameter-vote", 404, ""},
		{"POST", "/hdls/bundle", 405, "GET, HEAD"},
		// Folders and their listings.
		{"GET", "/hdls", 404, ""},
		{"GET", "/hdls/", 404, ""},
		{"GET", "/hdls/resolve/name", 404, ""},
		{"GET", "/hdls/resolve/name/", 404, ""},
		{"GET", "/hdls/record", 404, ""},
		{"GET", "/hdls/record/", 404, ""},
		// .incoming and temporary files.
		{"GET", "/.incoming/status", 404, ""},
		{"GET", "/hdls/.incoming/status", 404, ""},
		{"GET", "/hdls/resolve/name/.incoming", 404, ""},
		{"GET", "/hdls/resolve/name/alice.tmp", 404, ""},
		{"GET", "/hdls/record/.incoming", 404, ""},
		{"GET", "/hdls/record/" + firstUnit + ".tmp", 404, ""},
		// "..", ".", doubled and trailing slashes, raw: the router would redirect them.
		{"GET", "/hdls/resolve/name/../../status", 404, ""},
		{"GET", "/hdls/resolve/name/../../../secret", 404, ""},
		{"GET", "/hdls/record/../../../secret", 404, ""},
		{"GET", "/hdls/./status", 404, ""},
		{"GET", "/hdls//status", 404, ""},
		{"GET", "/hdls/status/", 404, ""},
		{"POST", "/hdls/x/../pay", 404, ""},
		// The same, escaped, and an escaped slash.
		{"GET", "/hdls/resolve/name/%2e%2e", 404, ""},
		{"GET", "/hdls/resolve/name/%2E%2E%2F%2E%2E%2F%2E%2E%2Fsecret", 404, ""},
		{"GET", "/hdls/resolve/name/..%2f..%2f..%2fsecret", 404, ""},
		{"GET", "/hdls/record/..%2f..%2fsecret", 404, ""},
		{"GET", "/hdls/%2e%2e/secret", 404, ""},
		{"GET", "/hdls%2fstatus", 404, ""},
		{"GET", "/hdls/resolve/name/alice%2fx", 404, ""},
		// Names outside the name grammar.
		{"GET", "/hdls/resolve/name/Alice", 404, ""},
		{"GET", "/hdls/resolve/name/-alice", 404, ""},
		{"GET", "/hdls/resolve/name/alice-", 404, ""},
		{"GET", "/hdls/resolve/name/" + strings.Repeat("a", 33), 404, ""},
		{"GET", "/hdls/resolve/name/ali%20ce", 404, ""},
		// Keys that are not 64 lowercase hex digits.
		{"GET", "/hdls/resolve/key/" + testKey[1:], 404, ""},
		{"GET", "/hdls/resolve/key/" + testKey + "a", 404, ""},
		{"GET", "/hdls/resolve/key/" + strings.ToUpper(testKey), 404, ""},
		{"GET", "/hdls/resolve/key/" + strings.Repeat("g", 64), 404, ""},
		{"GET", "/hdls/resolve/key/alice", 404, ""},
		// Record units that are not 20 digits and ".jsonl".
		{"GET", "/hdls/record/0000000000000000000.jsonl", 404, ""},
		{"GET", "/hdls/record/000000000000000000000.jsonl", 404, ""},
		{"GET", "/hdls/record/+0000000000000000000.jsonl", 404, ""},
		{"GET", "/hdls/record/00000000000000000000.json", 404, ""},
		{"GET", "/hdls/record/00000000000000000000", 404, ""},
		{"GET", "/hdls/record/99999999999999999999.jsonl", 404, ""},
		// Another method on a listed path.
		{"POST", "/hdls/status", 405, "GET, HEAD"},
		{"PUT", "/hdls/checkpoint", 405, "GET, HEAD"},
		{"DELETE", "/hdls/resolve/name/alice", 405, "GET, HEAD"},
		{"POST", "/hdls/resolve/key/" + testKey, 405, "GET, HEAD"},
		{"POST", "/hdls/record/" + firstUnit, 405, "GET, HEAD"},
		{"POST", "/hdls/lifelong-record.jsonl", 405, "GET, HEAD"},
		{"GET", "/hdls/pay", 405, "POST"},
		{"HEAD", "/hdls/update", 405, "POST"},
		{"PUT", "/hdls/name-request", 405, "POST"},
	} {
		response := serve(fetcher, test.method, test.target, nil)
		if response.Code != test.wantCode || response.Header().Get("Allow") != test.wantAllow {
			t.Errorf("%s %s: %d, Allow %q; want %d, Allow %q", test.method, test.target,
				response.Code, response.Header().Get("Allow"), test.wantCode, test.wantAllow)
		}
		if body := response.Body.String(); strings.Contains(body, testSecret) || strings.Contains(body, testIncoming) {
			t.Errorf("%s %s sent a file it must not: %q", test.method, test.target, body)
		}
		if got := response.Header().Get("Cache-Control"); got != "" {
			t.Errorf("%s %s: a refusal carries Cache-Control %q", test.method, test.target, got)
		}
	}
	if received := stub.receivedSoFar(); len(received) != 0 {
		t.Errorf("refused requests reached the engine: %q", received)
	}
}

// A listed path whose file is not there, or is a folder, is 404 for a second, max-age=1 (review N5): the
// file can appear at any commit, and a cache would otherwise keep the 404 for its own default, minutes
// on some CDNs.
func TestFetcherAnswersAMissingListedFile404ForASecond(t *testing.T) {
	folders := writeTestFolders(t)
	fetcher := newTestFetcher(t, folders, "http://engine.invalid", 100)
	if err := os.Remove(filepath.Join(folders.publicFolder, "hdls", "bundle")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"/hdls/bundle",
		"/hdls/resolve/name/nobody",
		"/hdls/resolve/name/carol", // a folder
		"/hdls/resolve/key/" + strings.Repeat("cd", 32),
		"/hdls/record/00000000000000000400.jsonl",
		"/hdls/record/" + unitAsDirectory,
	} {
		response := serve(fetcher, http.MethodGet, target, nil)
		if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "max-age=1" || strings.Contains(response.Body.String(), testSecret) {
			t.Errorf("GET %s: %d, Cache-Control %q, %q; want 404, max-age=1", target, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
		}
	}
}

// A cache revalidates a file by its ETag, its size and its modification time to the nanosecond, never
// by its time in whole seconds (review N2). A file rewritten within the second a cache fetched it is
// sent again, not answered 304, though its size is the same, and a record unit that took its last entry
// in that second is sent whole, not answered 304 immutable to a cache that holds it short. A file that
// did not change is answered 304.
func TestFetcherRevalidatesByTheETag(t *testing.T) {
	folders := writeTestFolders(t)
	fetcher := newTestFetcher(t, folders, "http://engine.invalid", 100)
	statusPath := filepath.Join(folders.publicFolder, "hdls", "status")
	unitPath := filepath.Join(folders.recordFolder, secondUnit) // heights 100 to 199
	unitTarget := "/hdls/record/" + secondUnit
	second := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	write := func(path, content string, at time.Duration) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, second.Add(at), second.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	// revalidate asks for target again with the validators of the earlier answer, as a cache does.
	revalidate := func(target string, earlier *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		if etag := earlier.Header().Get("ETag"); etag != "" {
			request.Header.Set("If-None-Match", etag)
		}
		if lastModified := earlier.Header().Get("Last-Modified"); lastModified != "" {
			request.Header.Set("If-Modified-Since", lastModified)
		}
		recorder := httptest.NewRecorder()
		fetcher.ServeHTTP(recorder, request)
		return recorder
	}

	// Height 199 is in progress, and the unit still grows. A cache fetches both at .100 of a second.
	write(statusPath, `{"heightInProgress":199}`, 100*time.Millisecond)
	write(unitPath, "{\"Height\":100}\n{\"Height\":198}\n", 100*time.Millisecond)
	earlierStatus, earlierUnit := serve(fetcher, http.MethodGet, "/hdls/status", nil), serve(fetcher, http.MethodGet, unitTarget, nil)
	if earlierUnit.Header().Get("ETag") == "" || earlierUnit.Header().Get("Last-Modified") != "" {
		t.Errorf("a unit is sent with ETag %q and Last-Modified %q; want an ETag and no Last-Modified", earlierUnit.Header().Get("ETag"), earlierUnit.Header().Get("Last-Modified"))
	}
	if response := revalidate(unitTarget, earlierUnit); response.Code != http.StatusNotModified {
		t.Errorf("an unchanged unit is revalidated %d, want 304", response.Code)
	}

	// Height 199 commits at .900 of the same second: its entry is appended, and the status moves on.
	write(unitPath, "{\"Height\":100}\n{\"Height\":198}\n{\"Height\":199}\n", 900*time.Millisecond)
	write(statusPath, `{"heightInProgress":200}`, 900*time.Millisecond)
	if response := revalidate("/hdls/status", earlierStatus); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "200") {
		t.Errorf("the status, rewritten in the same second, is revalidated %d %q; want 200 and the new status", response.Code, response.Body.String())
	}
	if response := revalidate(unitTarget, earlierUnit); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "199") {
		t.Errorf("the unit, complete since the same second, is revalidated %d (Cache-Control %q); want 200 and its last entry", response.Code, response.Header().Get("Cache-Control"))
	}
}

// engineStub stands in for the engine's operator port: it answers each submission route as the engine
// does, and keeps what it received.
type engineStub struct { // implements http.Handler
	mutex    sync.Mutex
	received []string // "<method> <path> <body>"
}

// The compiler checks that *engineStub implements http.Handler.
var _ http.Handler = (*engineStub)(nil)

func (stub *engineStub) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	stub.mutex.Lock()
	stub.received = append(stub.received, request.Method+" "+request.URL.Path+" "+string(body))
	stub.mutex.Unlock()
	switch request.URL.Path {
	case "/hdls/pay":
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.Write([]byte(`{"receipt":"signed by the bank"}`))
	case "/hdls/update":
		responseWriter.WriteHeader(http.StatusAccepted)
	default:
		http.Error(responseWriter, "the name is taken", http.StatusConflict)
	}
}

// receivedSoFar is a copy of what the stub has received. The stub's handler runs on the server's
// goroutines, so the test reads it under the mutex.
func (stub *engineStub) receivedSoFar() []string {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return append([]string(nil), stub.received...)
}

// The three submissions reach the engine's operator port under their own paths with their bodies,
// and the engine's status, type and body come back unchanged, marked never to be cached.
func TestFetcherPassesSubmissionsToTheEngine(t *testing.T) {
	stub := &engineStub{}
	engine := httptest.NewServer(stub)
	defer engine.Close()
	fetcher := newTestFetcher(t, writeTestFolders(t), engine.URL, 100)
	for _, test := range []struct {
		target, body    string
		wantCode        int
		wantContentType string
		wantBody        string
	}{
		{"/hdls/pay", `{"from":"alice"}`, 200, "application/json", `{"receipt":"signed by the bank"}`},
		{"/hdls/update", `{"name":"alice"}`, 202, "", ""},
		{"/hdls/name-request", `{"name":"bob"}`, 409, "text/plain; charset=utf-8", "the name is taken\n"},
	} {
		response := serve(fetcher, http.MethodPost, test.target, []byte(test.body))
		if response.Code != test.wantCode || response.Body.String() != test.wantBody {
			t.Errorf("POST %s: %d %q, want the engine's %d %q", test.target, response.Code, response.Body.String(), test.wantCode, test.wantBody)
		}
		if got := response.Header().Get("Content-Type"); got != test.wantContentType {
			t.Errorf("POST %s: Content-Type %q, want the engine's %q", test.target, got, test.wantContentType)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("POST %s: Cache-Control %q, want no-store", test.target, got)
		}
		received := stub.receivedSoFar()
		if want := "POST " + test.target + " " + test.body; received[len(received)-1] != want {
			t.Errorf("the engine received %q, want %q", received[len(received)-1], want)
		}
	}
}

// A body of 1 MiB is forwarded whole; one byte more is refused with 413, and the engine never sees it.
func TestFetcherCapsTheSubmissionBody(t *testing.T) {
	stub := &engineStub{}
	engine := httptest.NewServer(stub)
	defer engine.Close()
	fetcher := newTestFetcher(t, writeTestFolders(t), engine.URL, 100)

	wholeBody := bytes.Repeat([]byte("x"), maxSubmissionBytes)
	response := serve(fetcher, http.MethodPost, "/hdls/update", wholeBody)
	received := stub.receivedSoFar()
	if response.Code != http.StatusAccepted || len(received) != 1 || received[0] != "POST /hdls/update "+string(wholeBody) {
		t.Fatalf("a 1 MiB body: %d, the engine received %d requests; want it forwarded whole", response.Code, len(received))
	}
	response = serve(fetcher, http.MethodPost, "/hdls/update", append(wholeBody, 'x'))
	if received := stub.receivedSoFar(); response.Code != http.StatusRequestEntityTooLarge || len(received) != 1 {
		t.Fatalf("a body over 1 MiB: %d, the engine received %d requests; want 413 and nothing forwarded", response.Code, len(received)-1)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("413: Cache-Control %q, want no-store", got)
	}
}

// The window counts submissions per second of the clock: the cap's worth pass, the next is refused
// until the next second, and a refused submission is not counted.
func TestSubmissionLimitAllowsTheCapEachSecond(t *testing.T) {
	limit := &submissionLimit{perSecond: 2}
	second := time.Unix(1_000_000, 0)
	for i, test := range []struct {
		at        time.Time
		wantAllow bool
	}{
		{second, true},
		{second.Add(400 * time.Millisecond), true},
		{second.Add(999 * time.Millisecond), false},
		{second.Add(999 * time.Millisecond), false},
		{second.Add(time.Second), true},
		{second.Add(time.Second + time.Millisecond), true},
		{second.Add(time.Second + 2*time.Millisecond), false},
		{second.Add(5 * time.Second), true},
	} {
		if got := limit.allow(test.at); got != test.wantAllow {
			t.Errorf("submission %d at %v: allowed %v, want %v", i, test.at, got, test.wantAllow)
		}
	}
}

// Above the cap the fetcher answers 429 with Retry-After: 1, and the engine never sees the submission.
// With a cap of 1, the second submission within one second is refused; the loop stops at the first
// refusal, so it does not depend on where the second's boundary falls.
func TestFetcherAnswers429AboveTheCap(t *testing.T) {
	stub := &engineStub{}
	engine := httptest.NewServer(stub)
	defer engine.Close()
	fetcher := newTestFetcher(t, writeTestFolders(t), engine.URL, 1)
	forwarded := 0
	for range 100 {
		response := serve(fetcher, http.MethodPost, "/hdls/pay", []byte(`{}`))
		if response.Code != http.StatusTooManyRequests {
			forwarded++
			continue
		}
		if response.Header().Get("Retry-After") != "1" || response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("429: Retry-After %q, Cache-Control %q; want 1 and no-store",
				response.Header().Get("Retry-After"), response.Header().Get("Cache-Control"))
		}
		if received := stub.receivedSoFar(); len(received) != forwarded {
			t.Errorf("the engine received %d submissions, want the %d forwarded", len(received), forwarded)
		}
		return
	}
	t.Fatal("100 submissions at a cap of 1 a second: none refused")
}
