package signing

// The remote signer (spec §2.7): the Signer of an engine whose key is in the signer program.

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
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// remoteRequestTimeout is the deadline of one request to the signer, and remoteFailFastWindow how
	// long after a failure every call fails at once, without a request (spec §2.7, §9 Q6): the engine
	// signs on its event loop, and a signer that is down must not slow the loop at every step.
	remoteRequestTimeout = time.Second
	remoteFailFastWindow = time.Second
	// remoteLogInterval is the least time between two lines about the signer (see finishRequest).
	remoteLogInterval = time.Minute
	// maxAnswerBytes caps the answer read from the signer: a signature of the network's scheme is under
	// 5 KiB in base64.
	maxAnswerBytes = 64 << 10
)

// RemoteSigner is a Signer whose key is in the signer program (cmd/signer), on the bank's private
// network (spec §2.2, §2.7). For each signature it sends POST <url>/sign, with the body {"domain":
// "...", "fields": ["<base64>", ...]} and the header Authorization: HDLS-HMAC <client> <lowercase hex
// of HMAC-SHA256(secret, body)>, and verifies the signature it gets under the bank's public key before
// it returns it, so a forged answer, or one by another key, is an error.
//
// The engine signs on its event loop, so a request gives up after a second, and after a failure every
// call fails at once, without a request, for the next second; a vote that fails is cast again later
// (consensus.Driver.castVote). A vote or a proposal is first checked against the slot memory (see
// claimSlot). It is safe to call from many goroutines at once: the node signs receipts on its HTTP
// handlers while its loop signs votes. It asks for one receipt at a time: one asked for while another
// is out is refused at once, without a request, so a burst of payments never queues receipts at the
// signer, which signs one request at a time, ahead of a vote. The node answers such a payment 202,
// with no receipt (node.Server.handlePay).
type RemoteSigner struct { // implements Signer
	signURL    string // <url>/sign
	clientName string // the name the signer knows the engine by
	secret     []byte // the client's secret, the key of the HMAC
	publicKey  []byte // the bank's key: every answer must verify under it
	networkID  string
	scheme     Scheme
	httpClient *http.Client

	mutex           sync.Mutex
	failingUntil    time.Time         // the end of the fail-fast window
	receiptInFlight bool              // whether a receipt's request is out
	slotFields      map[slot][][]byte // the fields first sent in each slot, kept from highestHeight up
	highestHeight   int64             // the highest height of a slot sent
	loggedFailing   bool              // whether the last line logged said the requests fail
	loggedAt        time.Time         // when it was logged
}

// The compiler checks that *RemoteSigner implements Signer.
var _ Signer = (*RemoteSigner)(nil)

// slot is what the signer signs at most once (spec §2.4, rule 3), read from the fields as cmd/signer
// reads it (checkVote, checkProposal): a vote's height, round and type, or a proposal's height and
// round, whose type is "".
type slot struct {
	domain   Domain
	height   int64
	round    int64
	voteType string
}

// NewRemoteSigner builds the signer that asks the signer program at signerURL (such as
// http://signer:7000), as the client clientName with its 32-byte secret, and verifies every answer
// under publicKey, a key of scheme, for networkID.
func NewRemoteSigner(signerURL, clientName string, secret, publicKey []byte, networkID string, scheme Scheme) *RemoteSigner {
	return &RemoteSigner{
		signURL:    strings.TrimSuffix(signerURL, "/") + "/sign",
		clientName: clientName,
		secret:     secret,
		publicKey:  publicKey,
		networkID:  networkID,
		scheme:     scheme,
		// The signer never redirects: a redirect is handed back as the answer, and refused as any status
		// but 200 is. The client has a transport of its own, with no proxy: http.DefaultTransport would
		// send the requests through the proxy HTTP_PROXY names.
		httpClient: &http.Client{
			Transport:     &http.Transport{},
			Timeout:       remoteRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slotFields: map[slot][][]byte{},
	}
}

// PublicKey returns the bank's public key, which every answer is verified under.
func (signer *RemoteSigner) PublicKey() []byte { return signer.publicKey }

// PublicKeyBase64 returns the public key in base64 wire form.
func (signer *RemoteSigner) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(signer.publicKey)
}

// Sign asks the signer for a signature over the canonical (networkID, domain, fields) preimage, which
// the signer builds itself, and returns it once it verifies under the bank's public key.
func (signer *RemoteSigner) Sign(domain Domain, fields ...[]byte) ([]byte, error) {
	if err := signer.startRequest(domain, fields); err != nil {
		return nil, err
	}
	signature, err := signer.request(domain, fields)
	signer.finishRequest(domain, err)
	return signature, err
}

// SignBase64 is Sign with the signature returned in base64 wire form.
func (signer *RemoteSigner) SignBase64(domain Domain, fields ...[]byte) (string, error) {
	return signBase64(signer, domain, fields)
}

// startRequest refuses, without a request, a call in the fail-fast window, a receipt while another is
// out, and a vote or a proposal the slot memory refuses (claimSlot). These refusals open no fail-fast
// window: the signer was not asked.
func (signer *RemoteSigner) startRequest(domain Domain, fields [][]byte) error {
	signer.mutex.Lock()
	defer signer.mutex.Unlock()
	switch {
	case time.Now().Before(signer.failingUntil):
		return errors.New("the signer failed less than a second ago: no request until a second has passed")
	case domain == DomainReceipt && signer.receiptInFlight:
		return errors.New("refused without asking the signer: another receipt is out, and the signer is asked for one at a time, " +
			"so that a vote never waits behind many")
	case domain == DomainReceipt:
		signer.receiptInFlight = true
	}
	return signer.claimSlot(domain, fields)
}

// claimSlot is the slot memory. It keeps the fields of the first request sent in a vote's or a
// proposal's slot, and refuses a later request in that slot with other fields, without sending it. A
// request that timed out here may still have been logged and signed there: the signer then holds the
// slot, and would refuse other fields as a conflict (spec §2.4, rule 3), an alarm against an honest
// engine. The driver casts a vote that failed again on its next event, and that may be another vote in
// the same slot: a nil prevote at the timeout after a prevote for a block. The same fields are sent
// again, and get the signature the signer gave them. Slots below the highest height sent are forgotten:
// the signer refuses them anyway (rule 4).
func (signer *RemoteSigner) claimSlot(domain Domain, fields [][]byte) error {
	requestSlot, hasSlot := slotOf(domain, fields)
	if !hasSlot {
		return nil
	}
	if sentFields, sent := signer.slotFields[requestSlot]; sent {
		if slices.EqualFunc(sentFields, fields, bytes.Equal) {
			return nil
		}
		return fmt.Errorf("refused without asking the signer: %s at height %d, round %d was sent to it with other fields, which it may have signed",
			domain, requestSlot.height, requestSlot.round)
	}
	if requestSlot.height > signer.highestHeight {
		signer.highestHeight = requestSlot.height
		maps.DeleteFunc(signer.slotFields, func(sentSlot slot, _ [][]byte) bool { return sentSlot.height < requestSlot.height })
	}
	signer.slotFields[requestSlot] = fields
	return nil
}

// slotOf returns the slot of a vote's or a proposal's fields: the height and the round are the first
// two fields of both, and a vote's type the third. It returns false for another domain, or fields
// that do not parse: those are sent as they are, and the signer refuses fields it cannot read.
func slotOf(domain Domain, fields [][]byte) (slot, bool) {
	if (domain != DomainBlockRevision && domain != DomainProposal) || len(fields) < 3 {
		return slot{}, false
	}
	height, heightErr := strconv.ParseInt(string(fields[0]), 10, 64)
	round, roundErr := strconv.ParseInt(string(fields[1]), 10, 64)
	if heightErr != nil || roundErr != nil {
		return slot{}, false
	}
	requestSlot := slot{domain: domain, height: height, round: round}
	if domain == DomainBlockRevision {
		requestSlot.voteType = string(fields[2])
	}
	return requestSlot, true
}

// request sends one request to the signer, and returns the signature it answers once that verifies
// under the bank's public key. Any other outcome is an error: no answer within the deadline, a status
// other than 200 with the signer's reason as it gives it, or an answer that is not a signature by the
// bank's key.
func (signer *RemoteSigner) request(domain Domain, fields [][]byte) ([]byte, error) {
	body, err := json.Marshal(struct {
		Domain Domain   `json:"domain"`
		Fields [][]byte `json:"fields"` // each in standard base64
	}{domain, fields})
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequest(http.MethodPost, signer.signURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, signer.secret)
	mac.Write(body)
	httpRequest.Header.Set("Authorization", "HDLS-HMAC "+signer.clientName+" "+hex.EncodeToString(mac.Sum(nil)))
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := signer.httpClient.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the signer answered %s in %s: %s", response.Status, domain, strings.TrimSpace(string(answer)))
	}
	var signed struct {
		Signature []byte `json:"signature"` // in standard base64
	}
	if err := json.Unmarshal(answer, &signed); err != nil || !Verify(signer.scheme, signer.networkID, signer.publicKey, domain, signed.Signature, fields...) {
		return nil, fmt.Errorf("the signer's answer in %s does not verify under publicKey: the signer holds another key, or another program answered", domain)
	}
	return signed.Signature, nil
}

// finishRequest takes the outcome of a request in domain: a failure starts the fail-fast window, and a
// receipt's end lets the next receipt go. It logs a line when the requests start failing, with the
// error, and one when the signer answers again, never one per request: a line only when the outcome is
// not what the last line said, and at most one a minute, as the engine logs a failing peer
// (node.HTTPTransport.post).
func (signer *RemoteSigner) finishRequest(domain Domain, err error) {
	failing, now := err != nil, time.Now()
	signer.mutex.Lock()
	if domain == DomainReceipt {
		signer.receiptInFlight = false
	}
	if failing {
		signer.failingUntil = now.Add(remoteFailFastWindow)
	}
	logIt := failing != signer.loggedFailing && now.Sub(signer.loggedAt) >= remoteLogInterval
	if logIt {
		signer.loggedFailing, signer.loggedAt = failing, now
	}
	signer.mutex.Unlock()
	switch {
	case logIt && failing:
		log.Printf("signer: requests to the signer at %s fail: %v", signer.signURL, err)
	case logIt:
		log.Printf("signer: the signer at %s answers again", signer.signURL)
	}
}
