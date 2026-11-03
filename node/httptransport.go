package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// HTTPTransport broadcasts messages to peers over HTTP with fire-and-forget POSTs. A peer is the list
// of base URLs one other node is reached at, in the order they are tried (post): a configured peer has
// one, and a validator resolved from the chain one for each IP address it publishes. Delivery is
// asynchronous (a goroutine per peer), as required by the Transport contract, so it never blocks the
// node's event loop. SetPeers and Peers are not part of Transport: the node finds them with a type
// assertion (resolvePeersFromState, catchUpCheckpoint).
type HTTPTransport struct { // implements Transport
	mutex        sync.RWMutex
	peers        [][]string // each peer's base URLs, in the order post tries them
	client       *http.Client
	peerLogMutex sync.Mutex
	peerLogLines map[string]peerLogLine // the last line logged about each peer, keyed by its URLs; posts on many goroutines share it, under peerLogMutex (see post)
}

// The compiler checks that *HTTPTransport implements Transport. Its methods take a pointer receiver,
// so it is the pointer, *HTTPTransport, that implements it; nil is a pointer of that type.
var _ Transport = (*HTTPTransport)(nil)

// peerLogLine is the last line the transport logged about a peer: whether it said the peer's pushes
// fail, and when.
type peerLogLine struct {
	failing  bool
	loggedAt time.Time
}

const (
	// peerDialTimeout is how long a node waits for a connection to one of a peer's URLs, before post tries
	// the next, or catch-up asks the next peer (catchUpClient, publicAddressDialer): a line that drops
	// packets, instead of refusing, costs two seconds, not the 5-second timeout of the whole request. Not
	// one: Linux sends a connection's first packet again after a second, so a timeout of one second would
	// fail every push whose first packet, or its answer, is lost once (review N15).
	peerDialTimeout = 2 * time.Second
	// peerLogInterval is the least time between two lines about one peer (see post).
	peerLogInterval = time.Minute
)

// NewHTTPTransport builds a transport for the given peer base URLs, one URL for each peer, as a node
// config lists them (SetPeers may replace them later). Its client is NewHTTPClient's, with a 5-second
// timeout, whose connections give up after peerDialTimeout (peerRoundTripper).
func NewHTTPTransport(peerURLs []string) *HTTPTransport {
	peers := make([][]string, len(peerURLs))
	for i, peerURL := range peerURLs {
		peers[i] = []string{peerURL}
	}
	client := NewHTTPClient(5 * time.Second)
	client.Transport = peerRoundTripper()
	return &HTTPTransport{peers: peers, client: client, peerLogLines: make(map[string]peerLogLine)}
}

// peerRoundTripper is a transport of http.DefaultTransport's settings, whose connections give up after
// peerDialTimeout: the pushes' (NewHTTPTransport) and catch-up's (catchUpClient).
func peerRoundTripper() *http.Transport {
	roundTripper := http.DefaultTransport.(*http.Transport).Clone()
	roundTripper.DialContext = (&net.Dialer{Timeout: peerDialTimeout}).DialContext
	return roundTripper
}

// NewHTTPClient is the HTTP client of every request a node or a wallet makes: it gives up after
// timeout, and never follows a redirect. A redirect (a status from 300 to 399) is handed back as the
// answer, and every caller treats it as a failed one, as it treats any status but a success. Otherwise
// a peer, or anyone on the plain-HTTP path, could send the request on to any URL the node or the
// wallet can reach: the wallet's own pages, or a router's on the user's network. Nothing relies on a
// redirect: a node never answers with one.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// SetPeers replaces the peer list: each peer's base URLs, in the order post tries them.
func (httpTransport *HTTPTransport) SetPeers(peers [][]string) {
	httpTransport.mutex.Lock()
	httpTransport.peers = append([][]string(nil), peers...)
	httpTransport.mutex.Unlock()
}

// Peers returns a copy of the current peer list (asked for a checkpoint to catch up to; see
// Node.catchUpCheckpoint).
func (httpTransport *HTTPTransport) Peers() [][]string {
	httpTransport.mutex.RLock()
	defer httpTransport.mutex.RUnlock()
	return append([][]string(nil), httpTransport.peers...)
}

// Broadcast POSTs message to every peer asynchronously, on its gossip route (gossipRoute).
func (httpTransport *HTTPTransport) Broadcast(from string, message Message) {
	httpTransport.mutex.RLock()
	peers := append([][]string(nil), httpTransport.peers...)
	httpTransport.mutex.RUnlock()

	routeName, body := gossipRoute(message)
	if routeName == "" {
		return
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return
	}
	// A peer's URLs are base URLs: a configured one is the operator's, and a resolved one is made of a
	// validator's IP address, which the ledger takes only in its one plain form
	// (ledger.CanonicalIPAddress), and the gossip port, so the route is the whole path, and nothing in
	// the address can make another request of it.
	for _, peerURLs := range peers {
		go httpTransport.post(peerURLs, "/hdls/"+routeName, encodedBody)
	}
}

// post sends one message to one peer, at the first of its URLs it connects to. When the connection to a
// URL fails (refused, the address unreachable, or not made within peerDialTimeout), it tries the next,
// so a bank with a second line to the internet stays reachable. Once a request is sent, its outcome
// ends it, whatever it is: an answer, whatever its status, or no answer before the client's 5-second
// timeout. The peer may have taken the message, and sending it again at another URL would give a slow
// peer the same message once per URL. The client follows no redirect (NewHTTPClient).
//
// When the push fails, it is lost, as gossip allows, and the peer is failing. The transport logs one line
// when a peer's pushes start failing, with the error, and one when it answers again, never one per
// message (spec §5.4): a line only when the outcome is not what the last line about the peer said, and
// at most one a minute (peerLogInterval), so a peer that flaps between the two costs one line a minute.
// It is how a bank finds that the others' allow lists refuse it, because it sends from an address it
// does not publish.
func (httpTransport *HTTPTransport) post(peerURLs []string, path string, body []byte) {
	var err error
	for _, peerURL := range peerURLs {
		var response *http.Response
		if response, err = httpTransport.client.Post(peerURL+path, "application/json", bytes.NewReader(body)); err == nil {
			response.Body.Close()
			break
		}
		var connectionError *net.OpError
		if !errors.As(err, &connectionError) || connectionError.Op != "dial" {
			break // connected: the peer may have taken the request, and must not get it twice
		}
	}

	peerKey, failing, now := strings.Join(peerURLs, " "), err != nil, time.Now()
	httpTransport.peerLogMutex.Lock()
	lastLine := httpTransport.peerLogLines[peerKey] // a peer never logged about is taken as answering
	logIt := failing != lastLine.failing && now.Sub(lastLine.loggedAt) >= peerLogInterval
	if logIt {
		httpTransport.peerLogLines[peerKey] = peerLogLine{failing: failing, loggedAt: now}
	}
	httpTransport.peerLogMutex.Unlock()
	switch {
	case logIt && failing:
		log.Printf("gossip: pushes to the peer at %s fail: %v", peerKey, err)
	case logIt:
		log.Printf("gossip: the peer at %s answers again", peerKey)
	}
}

// Server exposes a Node over HTTP: the routes Handler registers, for the operator port, and the part
// of them GossipHandler registers, for the gossip port (spec §3).
type Server struct{ node *Node }

// NewServer wraps a node for HTTP serving.
func NewServer(node *Node) *Server { return &Server{node: node} }

// Handler returns every route of the node, each with its method: the router answers 405 to another
// method before any handler runs. It is the operator port's, or the one port's when a node has no
// gossip port.
func (server *Server) Handler() http.Handler {
	router := http.NewServeMux()
	server.addGossipRoutes(router)
	router.HandleFunc("POST /hdls/pay", server.handlePay)
	router.HandleFunc("GET /hdls/accounts", server.handleAccounts)
	router.HandleFunc("GET /hdls/resolve", server.handleResolve)
	router.HandleFunc("GET /hdls/governance", server.handleGovernance)
	router.HandleFunc("GET /hdls/record", server.handleRecord)
	router.HandleFunc("GET /hdls/lifelong-record", server.handleLifelongRecord)
	router.HandleFunc("GET /hdls/bundle", server.handleExportBundle)
	router.HandleFunc("POST /hdls/bundle", server.handleImportBundle)
	router.HandleFunc("POST /hdls/checkpoint", server.handleImportCheckpoint)
	return router
}

// GossipHandler returns only the routes other validators use, the gossip port's (spec §3): it answers
// 404 to any other path, a wallet's payment, the account list and the record among them, and to a POST
// of a checkpoint. On this port the routes a wallet uses too (an update, a name request, the two
// certificates) are gossip.
func (server *Server) GossipHandler() http.Handler {
	router := http.NewServeMux()
	server.addGossipRoutes(router)
	// The router serves GET /hdls/checkpoint, so it would answer a POST of it with 405. Importing a
	// checkpoint is the operator port's alone, and this port answers 404, as to any route it lacks.
	router.HandleFunc("POST /hdls/checkpoint", http.NotFound)
	return router
}

// addGossipRoutes registers the gossip routes, which both ports serve: the messages validators relay
// to each other, and the status and checkpoint a validator catches up from.
func (server *Server) addGossipRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /hdls/proposal", server.handleProposal)
	router.HandleFunc("POST /hdls/vote", server.handleVote)
	router.HandleFunc("POST /hdls/payment", server.handleGossipPayment)
	router.HandleFunc("POST /hdls/update", server.handleAccountUpdate)
	router.HandleFunc("POST /hdls/name-request", server.handleNameRequest)
	router.HandleFunc("POST /hdls/parameter-vote", server.handleParameterVote)
	router.HandleFunc("POST /hdls/reassignment-vote", server.handleReassignmentVote)
	router.HandleFunc("GET /hdls/status", server.handleStatus)
	router.HandleFunc("GET /hdls/checkpoint", server.handleCheckpoint)
}

// handleCheckpoint serves the node's current committed state, the header of the block that produced it
// and the certificate that finalised it, for a joining/rebooting peer to verify and adopt.
func (server *Server) handleCheckpoint(responseWriter http.ResponseWriter, request *http.Request) {
	writeJSON(responseWriter, server.node.Snapshot().checkpoint())
}

func (server *Server) handleProposal(responseWriter http.ResponseWriter, request *http.Request) {
	var proposal ProposalMessage
	if !decode(responseWriter, request, &proposal, maxProposalBytes) {
		return
	}
	server.node.SubmitProposal(proposal)
	responseWriter.WriteHeader(http.StatusAccepted)
}

func (server *Server) handleVote(responseWriter http.ResponseWriter, request *http.Request) {
	var vote consensus.Vote
	if !decode(responseWriter, request, &vote, maxBodyBytes) {
		return
	}
	server.node.SubmitVote(vote)
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handleGossipPayment receives a payment relayed from a peer node and queues it locally, through the
// same intake as the client endpoint (Node.AdmitPayment): the payer's signature is verified at ingress,
// under the payer's key in the committed state, so an unauthenticated attacker cannot fill the bounded
// mempool with junk that the proposer would then re-verify every block.
func (server *Server) handleGossipPayment(responseWriter http.ResponseWriter, request *http.Request) {
	var payment ledger.Payment
	if !decode(responseWriter, request, &payment, maxBodyBytes) {
		return
	}
	if _, err := server.node.AdmitPayment(payment); err != nil {
		http.Error(responseWriter, "payment refused: "+err.Error(), http.StatusConflict)
		return // refused (e.g. it would overdraw the payer) — dropped, not queued, not relayed onward
	}
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handleAccountUpdate receives a signed account update — a re-delegation, a description or addresses
// — from a client or a peer, and hands it to the node's intake (Node.AdmitUpdate), which pools it only
// if it can still apply and its signature verifies under its account's key, and gossips it onward so it
// reaches whichever node proposes the block it names. A refusal answers 409 with the reason.
func (server *Server) handleAccountUpdate(responseWriter http.ResponseWriter, request *http.Request) {
	var update ledger.AccountUpdate
	if !decode(responseWriter, request, &update, maxBodyBytes) {
		return
	}
	if _, err := server.node.AdmitUpdate(update); err != nil {
		http.Error(responseWriter, "update refused: "+err.Error(), http.StatusConflict)
		return
	}
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handleNameRequest receives a signed name request — a registration, a key change, a bank change or a
// freeze — from a client or a peer, and hands it to the node's intake (Node.AdmitNameRequest), which
// pools it only if it can still apply and its signatures verify, and gossips it onward. It answers 202,
// or 409 with the reason.
func (server *Server) handleNameRequest(responseWriter http.ResponseWriter, request *http.Request) {
	var nameRequest ledger.NameRequest
	if !decode(responseWriter, request, &nameRequest, maxBodyBytes) {
		return
	}
	if _, err := server.node.AdmitNameRequest(nameRequest); err != nil {
		http.Error(responseWriter, "name request refused: "+err.Error(), http.StatusConflict)
		return
	}
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handleParameterVote receives a parameter-change certificate — the proposed parameters, the
// parameters they amend, and the validators' signatures — from a validator or a peer. The node pools
// and gossips it only if it is complete, its votes already meeting the > ⅔ quorum (see
// addParameterVotes); it is verified again on block application. It answers 202 once the certificate
// is pooled, now or before, or 409 with the reason.
func (server *Server) handleParameterVote(responseWriter http.ResponseWriter, request *http.Request) {
	var change ledger.ParameterChange
	if !decode(responseWriter, request, &change, maxBodyBytes) {
		return
	}
	if _, err := server.node.AdmitParameterCertificate(change); err != nil {
		http.Error(responseWriter, "certificate refused: "+err.Error(), http.StatusConflict)
		return
	}
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handleReassignmentVote receives a ⅔ name action's certificate — the name, what the action changes
// (its balance, key, bank of record, frozen mark), and the validators' signatures. The node pools and
// gossips it only if it is complete, its votes already meeting the > ⅔ quorum (see
// addReassignmentVotes); it is verified again on block application. It answers 202 once the
// certificate is pooled, now or before, or 409 with the reason.
func (server *Server) handleReassignmentVote(responseWriter http.ResponseWriter, request *http.Request) {
	var reassignment ledger.Reassignment
	if !decode(responseWriter, request, &reassignment, maxBodyBytes) {
		return
	}
	if _, err := server.node.AdmitReassignmentCertificate(reassignment); err != nil {
		http.Error(responseWriter, "certificate refused: "+err.Error(), http.StatusConflict)
		return
	}
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handleGovernance serves a read-only view of the current parameters, the validator set/quorum, and
// the pooled parameter-change and reassignment certificates with their vote tallies — for an admin UI.
func (server *Server) handleGovernance(responseWriter http.ResponseWriter, request *http.Request) {
	writeJSON(responseWriter, server.node.GovernanceView())
}

// handleExportBundle answers a bundle of everything the node would gossip now (spec §7.3), which the
// operator saves for a carrier: curl -o bank-a.bundle http://engine:8081/hdls/bundle. It is JSON lines,
// application/x-ndjson.
func (server *Server) handleExportBundle(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "application/x-ndjson")
	responseWriter.Write(server.node.ExportBundle())
}

// handleImportBundle takes a bundle a carrier brought (spec §7.4), of at most MaxBundleBytes, and answers
// what became of its messages (BundleCounts), or 409 with the reason it refused the whole bundle. It
// reads the whole bundle before it takes a line, so the server's read deadline bounds the upload only,
// not the import's work.
func (server *Server) handleImportBundle(responseWriter http.ResponseWriter, request *http.Request) {
	bundle, err := io.ReadAll(http.MaxBytesReader(responseWriter, request.Body, MaxBundleBytes))
	if err != nil {
		http.Error(responseWriter, "the bundle could not be read: it is larger than 64 MiB, or its upload was cut short", http.StatusBadRequest)
		return
	}
	counts, err := server.node.ImportBundle(bundle)
	if err != nil {
		http.Error(responseWriter, "bundle refused: "+err.Error(), http.StatusConflict)
		return
	}
	writeJSON(responseWriter, counts)
}

// handleImportCheckpoint takes a checkpoint a carrier brought (spec §7.5), such as the public folder's
// hdls/checkpoint of a bank that is ahead, of at most MaxCheckpointBytes. It answers 202 once the node
// has adopted it, or 409 with the reason it did not: not ahead, or does not verify (ImportCheckpoint).
func (server *Server) handleImportCheckpoint(responseWriter http.ResponseWriter, request *http.Request) {
	var checkpoint Checkpoint
	if !decode(responseWriter, request, &checkpoint, MaxCheckpointBytes) {
		return
	}
	if err := server.node.ImportCheckpoint(checkpoint); err != nil {
		http.Error(responseWriter, "checkpoint refused: "+err.Error(), http.StatusConflict)
		return
	}
	responseWriter.WriteHeader(http.StatusAccepted)
}

// handlePay is the client endpoint: a wallet submits a signed payment to its reference node. The
// payment must carry a valid signature under its payer's key; the node queues it (and gossips it to
// peers so it reaches whichever node proposes the block it names) and returns a receipt, signed under
// the node's name, that the payer can show a merchant as proof the node accepted and queued it.
func (server *Server) handlePay(responseWriter http.ResponseWriter, request *http.Request) {
	var payment ledger.Payment
	if !decode(responseWriter, request, &payment, maxBodyBytes) {
		return
	}
	// Admission is synchronous and guarded (see Node.AdmitPayment): a payment that would overdraw
	// the payer — or is stale, unsigned, a flood overflow, etc. — is refused HERE, with no receipt, so
	// the receipt below always means "this node holds the payment, queued for its block". A payment it
	// already holds gets 202 and no new receipt, so a replay of a gossiped payment costs no signature.
	newlyPooled, err := server.node.AdmitPayment(payment)
	if err != nil {
		http.Error(responseWriter, "payment refused: "+err.Error(), http.StatusConflict)
		return
	}
	if !newlyPooled {
		responseWriter.WriteHeader(http.StatusAccepted)
		return
	}
	receipt, err := ledger.SignReceipt(server.node.config.Signer, server.node.self, payment)
	if err != nil {
		responseWriter.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(responseWriter, receipt)
}

// handleStatus serves the status of the committed view (statusOf), off the event loop: every wallet
// page and every catch-up tick asks it.
func (server *Server) handleStatus(responseWriter http.ResponseWriter, request *http.Request) {
	writeJSON(responseWriter, server.node.statusOf(server.node.view.Load()))
}

// statusOf is the status of a committed view: the height in progress (heightInProgress: one above the
// committed height), the number of accounts, the effective network id (spec 2.3), which a wallet pins on
// first use, the hash of the last committed block, in hex (lastBlockHash), by which catch-up tells a peer
// at the block of a checkpoint it already holds from one at another block (see bestCheckpoint), the
// height at which the node's own precommit first confirmed its state (confirmed, null before; see
// noteConfirmation), and the alarm that halted the node (halted, null while it runs; see halt).
// /hdls/status answers it, and the public folder's hdls/status holds it.
func (node *Node) statusOf(view *committedView) map[string]any {
	var confirmed, halted any // null until the node is confirmed, and while it runs
	if height := node.confirmedHeight.Load(); height > 0 {
		confirmed = height
	}
	if alarm := node.haltedBecause.Load(); alarm != nil {
		halted = *alarm
	}
	return map[string]any{
		"heightInProgress": view.state.Height + 1,
		"accounts":         len(view.state.Accounts),
		"networkId":        node.config.Chain.NetworkID,
		"lastBlockHash":    hex.EncodeToString(view.lastBlockHash[:]),
		"confirmed":        confirmed,
		"halted":           halted,
	}
}

func (server *Server) handleAccounts(responseWriter http.ResponseWriter, request *http.Request) {
	writeJSON(responseWriter, server.node.Snapshot().Accounts)
}

// NameRecord is a name's record — the ledger acting as a decentralized name server. A committed name
// maps to the account's current public key, its bank of record, whether it is frozen, any key or bank
// change waiting on it, AND where it is reached: its public address, its IP addresses and its TLS keys,
// each only when set (plus the description), so one query replaces both a key directory and DNS for
// anything registered on-chain. /hdls/resolve answers it with the committed state it was read from
// (ResolveRecord), and the public folder holds it alone, so every bank of one chain publishes the same
// bytes for a name (publishNameRecord).
type NameRecord struct {
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Bank        string `json:"bank"`        // the bank of record; "" for none
	Frozen      bool   `json:"frozen"`      // a frozen name neither pays nor is paid
	PendingKey  string `json:"pendingKey"`  // a key change waiting for PendingHeight; "" for none
	PendingBank string `json:"pendingBank"` // a bank change waiting for PendingHeight; "" for none
	// PendingHeight is the height at whose end the pending change takes effect; 0 for none.
	PendingHeight int64    `json:"pendingHeight"`
	PublicAddress string   `json:"publicAddress,omitempty"` // the name's public face (e.g. "https://bank-a.example")
	IPAddresses   []string `json:"ipAddresses,omitempty"`   // what DNS A and AAAA records would hold; a validator's gossip addresses
	TLSKeys       []string `json:"tlsKeys,omitempty"`       // what DNS TLSA records would hold: the hashes of its web server's keys
	Description   string   `json:"description,omitempty"`
}

// nameRecordOf is the record of an account's name.
func nameRecordOf(account ledger.Account) NameRecord {
	return NameRecord{
		Name:          account.Name,
		PublicKey:     account.PublicKey,
		Bank:          account.Bank,
		Frozen:        account.Frozen,
		PendingKey:    account.PendingKey,
		PendingBank:   account.PendingBank,
		PendingHeight: account.PendingHeight,
		PublicAddress: account.PublicAddress,
		IPAddresses:   account.IPAddresses,
		TLSKeys:       account.TLSKeys,
		Description:   account.Description,
	}
}

// ResolveRecord is one name-lookup answer: the name's record, and the committed state it was read
// from. Height and StateDigest name that state: StateDigest is its LedgerState.Digest() in hex, the
// value a payment's pin takes (E38), never the hash of the block that produced it. They are the serving
// node's word, not a proof: a client that does not trust a single node queries K independent nodes and
// compares the records themselves (key and addresses), since a dishonest node can pair a forged record
// with the honest digest.
type ResolveRecord struct {
	NameRecord         // embedded: its fields are ResolveRecord's own, and come first in the JSON
	Height      int64  `json:"height"`      // the last committed height; the answer reflects the state its block produced
	StateDigest string `json:"stateDigest"` // hex of that state's digest (LedgerState.Digest)
}

// handleResolve is GET /hdls/resolve?name=<name> (or ?key=<public key> for the reverse lookup: the
// name whose current key it is), answered from the committed view. 404 when nothing on the committed
// ledger matches.
func (server *Server) handleResolve(responseWriter http.ResponseWriter, request *http.Request) {
	name, publicKey := request.URL.Query().Get("name"), request.URL.Query().Get("key")
	// Standard base64 keys contain '+', which query parsing decodes to a space — so a raw (properly
	// unencoded) key pasted into ?key= arrives with spaces and matches nothing. A space can never
	// appear in a base64 key, so restoring '+' is safe and lets a raw key work as documented (a
	// client that percent-encodes '+' as %2B already arrives correctly and is unaffected).
	publicKey = strings.ReplaceAll(publicKey, " ", "+")
	if (name == "") == (publicKey == "") {
		http.Error(responseWriter, "pass exactly one of ?name= or ?key=", http.StatusBadRequest)
		return
	}
	view := server.node.view.Load()
	var account ledger.Account
	var found bool
	if name != "" {
		account, found = ledger.FindAccount(view.state.Accounts, name) // by halves: the accounts are in name order
	} else {
		for _, candidate := range view.state.Accounts { // a key only by a scan
			if candidate.PublicKey == publicKey {
				account, found = candidate, true
				break
			}
		}
	}
	if !found {
		http.Error(responseWriter, "not registered", http.StatusNotFound)
		return
	}
	writeJSON(responseWriter, ResolveRecord{
		NameRecord:  nameRecordOf(account),
		Height:      view.state.Height,
		StateDigest: hex.EncodeToString(view.stateDigest[:]),
	})
}

// handleRecord serves GET /hdls/record?from=H: the rolling entries for heights H to H + 99, from at
// most two unit files. Without a from it serves the newest unit file's entries: the latest signatures.
func (server *Server) handleRecord(responseWriter http.ResponseWriter, request *http.Request) {
	from, hasFrom, ok := recordFrom(responseWriter, request)
	if !ok {
		return
	}
	directory := server.node.recordDirectory()
	if !hasFrom {
		firstHeights, _ := unitFirstHeights(directory)
		if len(firstHeights) > 0 {
			from = firstHeights[len(firstHeights)-1] // the newest unit file's first height
		}
	}
	paths := []string{unitPath(directory, from), unitPath(directory, from+recordUnitHeights)}
	writeJSON(responseWriter, server.node.readRecord(paths, from, from+recordUnitHeights-1, false))
}

// handleLifelongRecord serves GET /hdls/lifelong-record?from=H: the first 100 lifelong entries of height
// H and above. Without a from it serves the newest 100.
func (server *Server) handleLifelongRecord(responseWriter http.ResponseWriter, request *http.Request) {
	if from, hasFrom, ok := recordFrom(responseWriter, request); ok {
		writeJSON(responseWriter, server.node.readRecord([]string{server.node.lifelongRecordPath()}, from, math.MaxInt64, !hasFrom))
	}
}

// recordFrom reads a record route's from, a height of 0 or more, if the query has one, and whether it
// has; or it answers 400.
func recordFrom(responseWriter http.ResponseWriter, request *http.Request) (int64, bool, bool) {
	query := request.URL.Query()
	from, err := strconv.ParseInt(query.Get("from"), 10, 64)
	if query.Has("from") && (err != nil || from < 0) {
		http.Error(responseWriter, "pass ?from=<height>, or nothing for the newest entries", http.StatusBadRequest)
		return 0, false, false
	}
	return from, query.Has("from"), true
}

// maxBodyBytes bounds the body every POST route decodes (audit: the spend path accepted unbounded
// JSON), except a proposal's, which maxProposalBytes bounds: a block's content takes up to 768 KiB, and
// its evidence and a governance certificate come on top (spec 4.11). It is ledger.MaxMessageBytesLimit,
// the most a vote can set MaxMessageBytes to, so a message the parameters allow always fits a body.
const (
	maxBodyBytes     = ledger.MaxMessageBytesLimit
	maxProposalBytes = 2 << 20
)

// decode reads a POST body of at most maxBytes into destination, or answers the request with an error.
func decode(responseWriter http.ResponseWriter, request *http.Request, destination any, maxBytes int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(responseWriter, request.Body, maxBytes))
	if err := decoder.Decode(destination); err != nil {
		http.Error(responseWriter, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

// writeJSON answers a request with value's JSON (jsonAnswer). A value that does not marshal, which none
// a route answers does, gets an empty body.
func writeJSON(responseWriter http.ResponseWriter, value any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	if answer, err := jsonAnswer(value); err == nil {
		responseWriter.Write(answer)
	}
}

// jsonAnswer is the body a route answers value with: its JSON and a newline, as a json.Encoder writes
// it. The public folder's files are made with it too (see publishAnswer), so a file is the answer of
// its route byte for byte.
func jsonAnswer(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	return append(data, '\n'), err
}
