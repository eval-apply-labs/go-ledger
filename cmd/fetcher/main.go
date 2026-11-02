// Command fetcher serves a bank's public face: it is the origin a CDN pulls from. It sends, read-only,
// the files the engine (cmd/server) writes: the public folder, the rolling record's unit files and the
// lifelong record file, each with the Cache-Control a CDN may keep it by. It passes a customer's
// payment, account update and name request on to the engine's operator port, under one global cap. It
// holds no key and keeps no state. It serves an explicit list of paths, never a directory: any other
// path is 404, and another method on a listed path is 405.
//
// The record flags name the files beside the engine's snapshot, named by network (node/record.go):
// <snapshotPath>.<first 16 hex digits of the genesis hash>.record and .lifelong.jsonl.
//
// Usage:
//
//	fetcher -listen :8080 -folder /data/public -engine http://engine:8081 \
//	    -record /data/snapshot.json.<network>.record -lifelong /data/snapshot.json.<network>.lifelong.jsonl
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

const (
	// maxSubmissionBytes caps a submission's body as the engine caps it: ledger.MaxMessageBytesLimit,
	// 1 MiB, the most a vote can set MaxMessageBytes to.
	maxSubmissionBytes = ledger.MaxMessageBytesLimit
	// maxStatusBytes bounds the read of hdls/status, an object under 200 bytes, so a broken file can't
	// make each record request read without limit.
	maxStatusBytes = 4 << 10
	// recordUnitHeights is the heights a rolling unit file covers, as node/record.go writes them.
	recordUnitHeights = 100

	jsonContentType      = "application/json"
	jsonLinesContentType = "application/x-ndjson" // one JSON value per line: the record files and the bundle
)

// recordUnitPattern is a rolling unit file's name: its first height in 20 digits, then ".jsonl".
// keyPattern is the file name of a key's record: the SHA-256 of the public key, in lowercase hex.
var (
	recordUnitPattern = regexp.MustCompile(`^[0-9]{20}\.jsonl$`)
	keyPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func main() {
	listen := flag.String("listen", ":8080", "the address to serve on: the CDN's origin")
	publicFolder := flag.String("folder", "", "the public folder the engine writes (its publicPath)")
	recordFolder := flag.String("record", "", "the engine's record folder of unit files")
	lifelongRecordPath := flag.String("lifelong", "", "the engine's lifelong record file")
	engine := flag.String("engine", "", "the base URL of the engine's operator port, such as http://engine:8081")
	maxSubmissionsPerSecond := flag.Int("max-submissions-per-second", 100, "the submissions forwarded to the engine each second, at most")
	flag.Parse()

	engineURL, err := url.Parse(*engine)
	if err != nil || (engineURL.Scheme != "http" && engineURL.Scheme != "https") || engineURL.Host == "" {
		log.Fatalf("-engine %q: want the operator port's base URL, such as http://engine:8081", *engine)
	}
	if *publicFolder == "" || *recordFolder == "" || *lifelongRecordPath == "" || *maxSubmissionsPerSecond < 1 {
		log.Fatal("-folder, -record and -lifelong are required, and -max-submissions-per-second must be at least 1")
	}

	log.Printf("hypernode fetcher up: listen=%s folder=%s record=%s lifelong=%s engine=%s maxSubmissionsPerSecond=%d",
		*listen, *publicFolder, *recordFolder, *lifelongRecordPath, *engine, *maxSubmissionsPerSecond)

	httpServer := &http.Server{
		Addr:    *listen,
		Handler: NewFetcher(*publicFolder, *recordFolder, *lifelongRecordPath, engineURL, *maxSubmissionsPerSecond),
		// Per-connection deadlines, as cmd/server sets them: a client that stalls mid-request or never
		// drains its answer is disconnected instead of holding a goroutine forever. The read deadline
		// also bounds how long a submission's body may take to arrive before it is forwarded. Generous
		// for the biggest file (a checkpoint, a record unit) to a CDN.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Fatal(httpServer.ListenAndServe())
}

// Fetcher serves the public face from three places on disk, and forwards submissions to the engine.
type Fetcher struct { // implements http.Handler
	publicFolder       string // hdls/status, hdls/resolve/name/<name>, …, as the engine writes them
	recordFolder       string // <first height, 20 digits>.jsonl, one per 100 heights
	lifelongRecordPath string
	engineProxy        *httputil.ReverseProxy
	submissionLimit    *submissionLimit
	router             *http.ServeMux
}

// The compiler checks that *Fetcher implements http.Handler.
var _ http.Handler = (*Fetcher)(nil)

// NewFetcher builds the public face over the engine's folders, forwarding submissions to engineURL, at
// most maxSubmissionsPerSecond of them each second.
func NewFetcher(publicFolder, recordFolder, lifelongRecordPath string, engineURL *url.URL, maxSubmissionsPerSecond int) *Fetcher {
	fetcher := &Fetcher{
		publicFolder:       publicFolder,
		recordFolder:       recordFolder,
		lifelongRecordPath: lifelongRecordPath,
		// SetURL sends each request to the engine under its own path, and Rewrite drops any
		// X-Forwarded header a client sent. The engine's status and body come back unchanged.
		engineProxy: &httputil.ReverseProxy{
			Rewrite: func(proxyRequest *httputil.ProxyRequest) { proxyRequest.SetURL(engineURL) },
		},
		submissionLimit: &submissionLimit{perSecond: maxSubmissionsPerSecond},
		router:          http.NewServeMux(),
	}
	// Each route names its method, so the router answers 405, with an Allow header, to another method
	// on a listed path, as the node's router does; a GET route also answers HEAD. The Cache-Control
	// values are what a CDN may keep each file for (spec 6.3).
	for _, file := range []struct{ name, contentType, cacheControl string }{
		{"status", jsonContentType, "max-age=1"},      // the wallet's "behind" check allows 10 blocks at about 1 s each
		{"checkpoint", jsonContentType, "max-age=1"},  // a wallet names a payment's block from it: a copy a few blocks old names one that has passed
		{"accounts", jsonContentType, "max-age=5"},    // changes at most once a block
		{"governance", jsonContentType, "max-age=5"},  // likewise
		{"bundle", jsonLinesContentType, "max-age=1"}, // the second path, pulling bundles when gossip is cut, needs it fresh
	} {
		fetcher.router.HandleFunc("GET /hdls/"+file.name, func(responseWriter http.ResponseWriter, request *http.Request) {
			serveFile(responseWriter, request, filepath.Join(fetcher.publicFolder, "hdls", file.name), file.contentType, file.cacheControl)
		})
	}
	fetcher.router.HandleFunc("GET /hdls/resolve/name/{name}", fetcher.handleResolveName)
	fetcher.router.HandleFunc("GET /hdls/resolve/key/{key}", fetcher.handleResolveKey)
	fetcher.router.HandleFunc("GET /hdls/record/{unit}", fetcher.handleRecordUnit)
	fetcher.router.HandleFunc("GET /hdls/lifelong-record.jsonl", func(responseWriter http.ResponseWriter, request *http.Request) {
		serveFile(responseWriter, request, fetcher.lifelongRecordPath, jsonLinesContentType, "max-age=5") // still growing
	})
	fetcher.router.HandleFunc("POST /hdls/pay", fetcher.handleSubmission)
	fetcher.router.HandleFunc("POST /hdls/update", fetcher.handleSubmission)
	fetcher.router.HandleFunc("POST /hdls/name-request", fetcher.handleSubmission)
	return fetcher
}

// ServeHTTP answers 404 to a path that is not in its one clean form: one with a "." or ".." segment,
// raw or escaped as %2e, or a doubled or trailing slash. The router would answer it with a redirect to
// the clean form; the public face has one spelling per file, and nothing else. Every other request goes
// to the router.
func (fetcher *Fetcher) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	if request.URL.Path != path.Clean(request.URL.Path) {
		http.NotFound(responseWriter, request)
		return
	}
	fetcher.router.ServeHTTP(responseWriter, request)
}

// handleResolveName serves a name's record. The name grammar allows only a to z, 0 to 9 and inner
// hyphens, so a name is a safe file name: no "/", no ".", never ".incoming" or a temporary file.
func (fetcher *Fetcher) handleResolveName(responseWriter http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	if !ledger.CanonicalName(name) {
		http.NotFound(responseWriter, request)
		return
	}
	serveFile(responseWriter, request, filepath.Join(fetcher.publicFolder, "hdls", "resolve", "name", name), jsonContentType, "max-age=5")
}

// handleResolveKey serves the record of the name that holds a key, found by the SHA-256 of the key.
func (fetcher *Fetcher) handleResolveKey(responseWriter http.ResponseWriter, request *http.Request) {
	key := request.PathValue("key")
	if !keyPattern.MatchString(key) {
		http.NotFound(responseWriter, request)
		return
	}
	serveFile(responseWriter, request, filepath.Join(fetcher.publicFolder, "hdls", "resolve", "key", key), jsonContentType, "max-age=5")
}

// handleRecordUnit serves a rolling unit file as it is on disk, one entry per line. A unit is complete
// once its last height is committed, when its first height + 100 ≤ the height in progress: the engine
// never writes to it again, so a CDN may keep it for good. Until then it is still growing.
func (fetcher *Fetcher) handleRecordUnit(responseWriter http.ResponseWriter, request *http.Request) {
	unit := request.PathValue("unit")
	// Twenty digits can name a height above the largest an int64 holds, which ParseInt refuses.
	firstHeight, err := strconv.ParseInt(strings.TrimSuffix(unit, ".jsonl"), 10, 64)
	if !recordUnitPattern.MatchString(unit) || err != nil {
		http.NotFound(responseWriter, request)
		return
	}
	cacheControl := "max-age=5"
	if heightInProgress, err := fetcher.heightInProgress(); err == nil && firstHeight+recordUnitHeights <= heightInProgress {
		cacheControl = "max-age=31536000, immutable"
	}
	serveFile(responseWriter, request, filepath.Join(fetcher.recordFolder, unit), jsonLinesContentType, cacheControl)
}

// heightInProgress reads the height in progress from the public folder's hdls/status, at most
// maxStatusBytes of it. A status that is missing or unreadable is an error, and the caller takes no
// unit as complete.
func (fetcher *Fetcher) heightInProgress() (int64, error) {
	file, err := os.Open(filepath.Join(fetcher.publicFolder, "hdls", "status"))
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var status struct {
		HeightInProgress int64 `json:"heightInProgress"`
	}
	err = json.NewDecoder(io.LimitReader(file, maxStatusBytes)).Decode(&status)
	return status.HeightInProgress, err
}

// serveFile sends the regular file at filePath with http.ServeContent, which also answers HEAD, ranges
// and If-None-Match. Every path it is given is a fixed name, or a fixed folder and a name its grammar
// checked, so nothing in a request can climb out of the engine's folders. A missing file, a directory or
// anything else that is not a regular file is 404, with max-age=1: the file can appear at any commit (a
// new name, the next record unit), and a cache would otherwise keep the 404 as long as its own default,
// minutes on some CDNs. A file that is there but can't be read (its permissions) is 500, and logged, for
// the operator to see.
//
// A cache revalidates a file by its ETag, its size and its modification time to the nanosecond, and by
// nothing else: the zero time given to ServeContent sends no Last-Modified, which counts whole seconds.
// By it, a file rewritten within the second a cache fetched it would be answered 304, and a record unit
// that took its last entry in that second would be kept for a year without it.
func serveFile(responseWriter http.ResponseWriter, request *http.Request, filePath, contentType, cacheControl string) {
	file, err := os.Open(filePath)
	if errors.Is(err, fs.ErrNotExist) {
		responseWriter.Header().Set("Cache-Control", "max-age=1")
		http.NotFound(responseWriter, request)
		return
	}
	if err != nil {
		log.Printf("serve %s: %v", request.URL.Path, err)
		http.Error(responseWriter, "the file can't be read", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	fileInformation, err := file.Stat()
	if err != nil || !fileInformation.Mode().IsRegular() {
		responseWriter.Header().Set("Cache-Control", "max-age=1")
		http.NotFound(responseWriter, request)
		return
	}
	responseWriter.Header().Set("Content-Type", contentType)
	responseWriter.Header().Set("Cache-Control", cacheControl)
	responseWriter.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fileInformation.Size(), fileInformation.ModTime().UnixNano()))
	http.ServeContent(responseWriter, request, "", time.Time{}, file)
}

// handleSubmission passes a customer's payment, account update or name request to the engine's
// operator port, and the engine's answer back as it is: a receipt, 202 or 409. It never looks inside:
// the engine checks every signature. It reads the whole body first, at most 1 MiB, so the engine never
// waits on a slow upload and never gets a body cut short.
func (fetcher *Fetcher) handleSubmission(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Cache-Control", "no-store")
	if !fetcher.submissionLimit.allow(time.Now()) {
		responseWriter.Header().Set("Retry-After", "1")
		http.Error(responseWriter, "too many submissions: retry in a second", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(responseWriter, request.Body, maxSubmissionBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(responseWriter, "a submission is at most 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(responseWriter, "the body could not be read", http.StatusBadRequest)
		return
	}
	request.Body, request.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	fetcher.engineProxy.ServeHTTP(responseWriter, request)
}

// submissionLimit is the global cap on submissions: at most perSecond in each second of the clock. Its
// one job is to keep a flood off the engine; limits per client are the CDN's. A window per second lets
// up to twice the cap through around a second's boundary, which is enough for that job.
type submissionLimit struct {
	mutex                 sync.Mutex
	perSecond             int
	currentSecond         int64 // the Unix second submissionsThisSecond counts
	submissionsThisSecond int
}

// allow counts a submission at now, and reports whether it is within the cap. A refused one is not
// counted: it never reaches the engine.
func (limit *submissionLimit) allow(now time.Time) bool {
	limit.mutex.Lock()
	defer limit.mutex.Unlock()
	if second := now.Unix(); second != limit.currentSecond {
		limit.currentSecond, limit.submissionsThisSecond = second, 0
	}
	if limit.submissionsThisSecond >= limit.perSecond {
		return false
	}
	limit.submissionsThisSecond++
	return true
}
