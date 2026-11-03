package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// maxRequestBytes caps a request's body. It holds the fields of the largest message the ledger takes,
// ledger.MaxMessageBytesLimit (1 MiB), in base64, which adds a third, with a few bytes of JSON each:
// twice the limit, 2 MiB.
const maxRequestBytes = 2 * ledger.MaxMessageBytesLimit

// conflictMark marks, in the audit log and the answer, a request refused because its slot is signed with
// other fields: the engine is broken, or someone else is driving it (spec §2.4, rule 3).
const conflictMark = "REFUSED CONFLICT"

// SigningService answers POST /sign (spec §2.2 to §2.5). It holds the bank's key, and signs with it
// only what the rules allow: a client it knows, by the HMAC of its secret; a domain in that client's
// list; fields of the domain's layout that name the bank wherever they name who signs; and for a vote or
// a proposal, a slot it has not signed otherwise, logged before it is signed. One request is decided
// and signed at a time: correctness over throughput.
type SigningService struct { // implements http.Handler
	mutex        sync.Mutex
	keySigner    signing.Signer    // the bank's key, bound to its network
	networkID    string            // the network the genesis makes, for the start line
	name         string            // the bank's name: the one name it signs in
	clients      map[string]client // by name
	signLog      *signLog          // nil without a sign log: every vote and proposal is refused (spec §2.6)
	auditLogPath string
	// signLogFolder is the sign log's folder, kept open, and so locked, while the program runs (start).
	signLogFolder *os.File
	// stopProgram is log.Fatalf: it logs a line and ends the program, with exit status 1, on a sign log
	// write that failed (takeSlot). A test replaces it.
	stopProgram func(format string, arguments ...any)
}

// The compiler checks that *SigningService implements http.Handler.
var _ http.Handler = (*SigningService)(nil)

// client is a program the signer answers: its secret, and the domains it may ask for (spec §2.3).
type client struct {
	secret  []byte // the 32 bytes of HMAC-SHA256's key
	domains []signing.Domain
}

// refusal is a request the signer will not sign: the status it answers, a 4xx, and why, in a few words.
type refusal struct { // implements error
	status int
	reason string
}

// The compiler checks that refusal implements error.
var _ error = refusal{}

// Error is the reason, which the answer carries as plain text.
func (refused refusal) Error() string { return refused.reason }

// auditEntry is one line of the audit log (spec §2.5): a signature or a refusal, for people to read.
type auditEntry struct {
	Time         string         `json:"time"`   // UTC, RFC 3339
	Client       string         `json:"client"` // the client the request names, known or not
	Domain       signing.Domain `json:"domain,omitempty"`
	Summary      string         `json:"summary,omitempty"`      // what was signed, or asked for
	Refused      string         `json:"refused,omitempty"`      // why it was refused, or failed; "" for a signature
	Fields       [][]byte       `json:"fields,omitempty"`       // for a conflict, the refused request's fields
	SignedFields [][]byte       `json:"signedFields,omitempty"` // and those signed in the slot before
}

// ServeHTTP serves the one route, POST /sign. Another method on it is 405, any other path 404.
func (service *SigningService) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	switch {
	case request.URL.Path != "/sign":
		http.NotFound(responseWriter, request)
	case request.Method != http.MethodPost:
		responseWriter.Header().Set("Allow", http.MethodPost)
		http.Error(responseWriter, "only POST", http.StatusMethodNotAllowed)
	default:
		service.handleSign(responseWriter, request)
	}
}

// handleSign answers a request with {"signature": "<base64>"}, or refuses it with a short plain-text
// reason: 401 for a missing or malformed Authorization header, an unknown client or an HMAC that does
// not match the body; 400 for a body that is not a request, or fields that do not fit the domain's
// layout; 403 for a domain not in the client's list, or fields that name another than the bank; 409
// for a vote or a proposal its slot rules refuse; 413 for a body over 2 MiB (maxRequestBytes). A failure
// of the signer itself, its audit log or its key, is 500; one of its sign log stops it (takeSlot). Every
// answer is written to the audit log first, and a signature whose line cannot be written is not given.
// A request whose client has gone by its turn gets nothing.
func (service *SigningService) handleSign(responseWriter http.ResponseWriter, request *http.Request) {
	// The body is read before the lock, so a slow client holds up no one else.
	body, err := io.ReadAll(http.MaxBytesReader(responseWriter, request.Body, maxRequestBytes))
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if request.Context().Err() != nil {
		// Its client gave up while it waited its turn, as the engine does after a second: it is dropped,
		// unsigned, with no answer and no line, so a backlog never keeps the requests behind it waiting
		// past their own deadlines.
		return
	}
	var signature []byte
	var entry auditEntry
	if err != nil {
		entry.Client, _ = parseAuthorization(request.Header.Get("Authorization"))
		err = refusal{http.StatusRequestEntityTooLarge, "the body is over 2 MiB, or could not be read"}
	} else {
		signature, entry, err = service.sign(request.Header.Get("Authorization"), body)
	}
	entry.Time = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		entry.Refused = err.Error()
	}
	if auditErr := service.writeAuditLine(entry); auditErr != nil {
		log.Printf("audit log: %v", auditErr)
		if err == nil {
			err = errors.New("the audit log could not be written")
		}
	}
	var refused refusal
	switch {
	case errors.As(err, &refused):
		http.Error(responseWriter, refused.reason, refused.status)
	case err != nil:
		log.Printf("sign: %v", err)
		http.Error(responseWriter, "the signer failed: its log says why", http.StatusInternalServerError)
	default:
		responseWriter.Header().Set("Content-Type", "application/json")
		json.NewEncoder(responseWriter).Encode(struct {
			Signature string `json:"signature"`
		}{base64.StdEncoding.EncodeToString(signature)})
	}
}

// sign applies the rules to one request (spec §2.4), in order: who asks, for which domain, with fields
// of what layout, in whose name, and for a vote or a proposal, its slot. It returns the signature and
// the audit log's line, or the refusal.
func (service *SigningService) sign(authorization string, body []byte) ([]byte, auditEntry, error) {
	var entry auditEntry
	clientName, givenHMAC := parseAuthorization(authorization)
	entry.Client = clientName
	client, known := service.clients[clientName]
	switch {
	case clientName == "":
		return nil, entry, refusal{http.StatusUnauthorized, "want the header Authorization: HDLS-HMAC <client> <lowercase hex of HMAC-SHA256(secret, body)>"}
	case !known:
		return nil, entry, refusal{http.StatusUnauthorized, "unknown client"}
	case !hmac.Equal([]byte(givenHMAC), []byte(bodyHMAC(client.secret, body))):
		return nil, entry, refusal{http.StatusUnauthorized, "the HMAC does not match the body under this client's secret"}
	}

	var signRequest struct {
		Domain signing.Domain `json:"domain"`
		Fields [][]byte       `json:"fields"` // each in standard base64
	}
	if err := json.Unmarshal(body, &signRequest); err != nil {
		return nil, entry, refusal{http.StatusBadRequest, `want the body {"domain": "...", "fields": ["<base64>", ...]}`}
	}
	domain, fields := signRequest.Domain, signRequest.Fields
	entry.Domain = domain
	if domain != keySelfTest && !slices.Contains(client.domains, domain) {
		return nil, entry, refusal{http.StatusForbidden, fmt.Sprintf("domain %q is not in this client's list", domain)}
	}
	// Every domain in a client's list has a layout (readClients), and key-self-test has one.
	requestSlot, summary, err := layouts[domain](domain, fields, service.name)
	entry.Summary = summary
	if err != nil {
		return nil, entry, err
	}
	if requestSlot != nil {
		if err := service.takeSlot(*requestSlot, fields, &entry); err != nil {
			return nil, entry, err
		}
	}
	signature, err := service.keySigner.Sign(domain, fields...)
	return signature, entry, err
}

// takeSlot applies rules 3 to 5 to a vote or a proposal for requestSlot (spec §2.4): one signature per
// slot, none below the highest height signed, and the log written and fsynced before the signature is
// made. A request for a slot signed with these same fields passes, to be signed again: ML-DSA signs
// deterministically, so it gets the signature it got before. One with other fields is a conflict,
// refused and written to the audit log with both requests.
func (service *SigningService) takeSlot(requestSlot slot, fields [][]byte, entry *auditEntry) error {
	signLog := service.signLog
	if signLog == nil {
		return refusal{http.StatusConflict, "no sign log: every vote and proposal is refused until the signer is started with -resume-above <height>"}
	}
	signedFields, signed := signLog.slots[requestSlot]
	switch {
	case signed && slices.EqualFunc(signedFields, fields, bytes.Equal):
		return nil
	case signed:
		entry.Fields, entry.SignedFields = fields, signedFields
		return refusal{http.StatusConflict, conflictMark + ": this slot is signed already, with other fields"}
	case requestSlot.Height <= signLog.resumeAbove:
		return refusal{http.StatusConflict, fmt.Sprintf("height %d is at or below %d, the height the sign log was resumed above", requestSlot.Height, signLog.resumeAbove)}
	case requestSlot.Height < signLog.highestHeight:
		return refusal{http.StatusConflict, fmt.Sprintf("height %d is below %d, the highest height signed", requestSlot.Height, signLog.highestHeight)}
	}
	if err := signLog.record(requestSlot, fields); err != nil {
		// The log on disk may hold the line, whole or in part, or not, so the signer stops, without
		// signing: a restart drops a line cut short, and reads a whole one as signed, which is safe.
		// Running on, it would append after a part line, which stops its next start, or sign other
		// fields in a slot the log on disk holds.
		service.stopProgram("sign log: %v: the signer stops, and has not signed; once the disk is fixed, start it again", err)
		return err // reached in a test only, whose stopProgram returns
	}
	return nil
}

// parseAuthorization returns the client name and the HMAC an Authorization header gives, or two empty
// strings for a header of another form.
func parseAuthorization(authorization string) (clientName, givenHMAC string) {
	parts := strings.Split(authorization, " ")
	if len(parts) != 3 || parts[0] != "HDLS-HMAC" {
		return "", ""
	}
	return parts[1], parts[2]
}

// bodyHMAC is the HMAC-SHA256 of body under secret, in lowercase hex: what a client's Authorization
// header must carry.
func bodyHMAC(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// writeAuditLine appends entry to the audit log as one JSON line. The file is opened for each line, so
// a log rotated by renaming it carries on in a new file. It is not fsynced: the sign log is what the
// rules read, the audit log is for people.
func (service *SigningService) writeAuditLine(entry auditEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(service.auditLogPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
