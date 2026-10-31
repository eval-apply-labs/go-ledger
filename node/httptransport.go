package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// HTTPTransport broadcasts messages to peers over HTTP with fire-and-forget POSTs. Peers are the
// base URLs of the other nodes. Delivery is asynchronous (a goroutine per peer), as required by
// the Transport contract, so it never blocks the node's event loop. SetPeers and Peers are not part
// of Transport: the node finds them with a type assertion (resolvePeersFromState, catchUpCheckpoint).
type HTTPTransport struct { // implements Transport
	mutex  sync.RWMutex
	peers  []string
	client *http.Client
}

// The compiler checks that *HTTPTransport implements Transport. Its methods take a pointer receiver,
// so it is the pointer, *HTTPTransport, that implements it; nil is a pointer of that type.
var _ Transport = (*HTTPTransport)(nil)

// NewHTTPTransport builds a transport for the given peer base URLs (which may be set later via
// SetPeers).
func NewHTTPTransport(peers []string) *HTTPTransport {
	return &HTTPTransport{
		peers:  append([]string(nil), peers...),
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// SetPeers replaces the peer list.
func (httpTransport *HTTPTransport) SetPeers(peers []string) {
	httpTransport.mutex.Lock()
	httpTransport.peers = append([]string(nil), peers...)
	httpTransport.mutex.Unlock()
}

// Peers returns a copy of the current peer list (asked for a checkpoint to catch up to when no
// bootstrap peer yields one; see Node.catchUpCheckpoint).
func (httpTransport *HTTPTransport) Peers() []string {
	httpTransport.mutex.RLock()
	defer httpTransport.mutex.RUnlock()
	return append([]string(nil), httpTransport.peers...)
}

// Broadcast POSTs message to every peer asynchronously.
func (httpTransport *HTTPTransport) Broadcast(from string, message Message) {
	httpTransport.mutex.RLock()
	peers := append([]string(nil), httpTransport.peers...)
	httpTransport.mutex.RUnlock()

	var path string
	var body any
	switch message.Type {
	case MessageProposal:
		path, body = "/hdls/proposal", message.Proposal
	case MessageVote:
		path, body = "/hdls/vote", message.Vote
	case MessagePayment:
		path, body = "/hdls/payment", message.Payment
	case MessageUpdate:
		path, body = "/hdls/update", message.Update
	case MessageNameRequest:
		path, body = "/hdls/name-request", message.NameRequest
	case MessageParameterCertificate:
		path, body = "/hdls/parameter-vote", message.ParameterChange
	case MessageReassignmentCertificate:
		path, body = "/hdls/reassignment-vote", message.Reassignment
	default:
		return
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return
	}
	for _, peer := range peers {
		go httpTransport.post(peer+path, encodedBody)
	}
}

// post sends one message to one peer. The client's 5-second timeout bounds the whole exchange.
func (httpTransport *HTTPTransport) post(endpointURL string, body []byte) {
	response, err := httpTransport.client.Post(endpointURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	response.Body.Close()
}

// Server exposes a Node over HTTP: the routes Handler registers.
type Server struct{ node *Node }

// NewServer wraps a node for HTTP serving.
func NewServer(node *Node) *Server { return &Server{node: node} }

// Handler returns the node's HTTP routes, each with its method: the router answers 405 to another
// method before any handler runs.
func (server *Server) Handler() http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("POST /hdls/proposal", server.handleProposal)
	router.HandleFunc("POST /hdls/vote", server.handleVote)
	router.HandleFunc("POST /hdls/payment", server.handleGossipPayment)
	router.HandleFunc("POST /hdls/update", server.handleAccountUpdate)
	router.HandleFunc("POST /hdls/name-request", server.handleNameRequest)
	router.HandleFunc("POST /hdls/parameter-vote", server.handleParameterVote)
	router.HandleFunc("POST /hdls/reassignment-vote", server.handleReassignmentVote)
	router.HandleFunc("POST /hdls/pay", server.handlePay)
	router.HandleFunc("GET /hdls/status", server.handleStatus)
	router.HandleFunc("GET /hdls/accounts", server.handleAccounts)
	router.HandleFunc("GET /hdls/resolve", server.handleResolve)
	router.HandleFunc("GET /hdls/governance", server.handleGovernance)
	router.HandleFunc("GET /hdls/checkpoint", server.handleCheckpoint)
	router.HandleFunc("GET /hdls/record", server.handleRecord)
	router.HandleFunc("GET /hdls/lifelong-record", server.handleLifelongRecord)
	return router
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

// handleAccountUpdate receives a signed account update — a re-delegation, a description or an address
// — from a client or a peer, and hands it to the node's intake (Node.AdmitUpdate), which pools it only
// if it can still apply and its signature verifies under its account's key, and gossips it onward so it
// reaches whichever node proposes the block it names. A refusal answers 409 with the reason.
func (server *Server) handleAccountUpdate(responseWriter http.ResponseWriter, request *http.Request) {
	var update ledger.AccountUpdate
	if !decode(responseWriter, request, &update, maxBodyBytes) {
		return
	}
	if err := server.node.AdmitUpdate(update); err != nil {
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
	if err := server.node.AdmitNameRequest(nameRequest); err != nil {
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
	if err := server.node.AdmitParameterCertificate(change); err != nil {
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
	if err := server.node.AdmitReassignmentCertificate(reassignment); err != nil {
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

// handleStatus serves the height in progress (heightInProgress: one above the committed height), the
// number of accounts and the effective network id (spec 2.3), which a wallet pins on first use. It
// reads the committed view, off the event loop: every wallet page and every catch-up tick asks it.
func (server *Server) handleStatus(responseWriter http.ResponseWriter, request *http.Request) {
	state := server.node.view.Load().state
	writeJSON(responseWriter, map[string]any{
		"heightInProgress": state.Height + 1,
		"accounts":         len(state.Accounts),
		"networkId":        server.node.config.Chain.NetworkID,
	})
}

func (server *Server) handleAccounts(responseWriter http.ResponseWriter, request *http.Request) {
	writeJSON(responseWriter, server.node.Snapshot().Accounts)
}

// ResolveRecord is one name-lookup answer — the ledger acting as a decentralized name server. A
// committed name maps to the account's current public key, its bank of record, whether it is frozen,
// any key or bank change waiting on it, AND its published network address (plus the description), so one query replaces both a key directory
// and DNS for anything registered on-chain. Height and StateDigest name the committed state the answer
// was read from: StateDigest is that state's LedgerState.Digest() in hex, the value a payment's pin
// takes (E38), never the hash of the block that produced it. They are the serving node's word, not a
// proof: a client that does not trust a single node queries K independent nodes and compares the
// records themselves (key and address), since a dishonest node can pair a forged record with the
// honest digest.
type ResolveRecord struct {
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Bank        string `json:"bank"`        // the bank of record; "" for none
	Frozen      bool   `json:"frozen"`      // a frozen name neither pays nor is paid
	PendingKey  string `json:"pendingKey"`  // a key change waiting for PendingHeight; "" for none
	PendingBank string `json:"pendingBank"` // a bank change waiting for PendingHeight; "" for none
	// PendingHeight is the height at whose end the pending change takes effect; 0 for none.
	PendingHeight int64  `json:"pendingHeight"`
	Address       string `json:"address,omitempty"` // the on-chain published address (e.g. "http://10.0.0.2:8080")
	Description   string `json:"description,omitempty"`
	Height        int64  `json:"height"`      // the last committed height; the answer reflects the state its block produced
	StateDigest   string `json:"stateDigest"` // hex of that state's digest (LedgerState.Digest)
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
		Name:          account.Name,
		PublicKey:     account.PublicKey,
		Bank:          account.Bank,
		Frozen:        account.Frozen,
		PendingKey:    account.PendingKey,
		PendingBank:   account.PendingBank,
		PendingHeight: account.PendingHeight,
		Address:       account.Address,
		Description:   account.Description,
		Height:        view.state.Height,
		StateDigest:   hex.EncodeToString(view.stateDigest[:]),
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

func writeJSON(responseWriter http.ResponseWriter, value any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(value)
}
