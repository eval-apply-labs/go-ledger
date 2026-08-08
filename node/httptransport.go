package node

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/ledger"
)

// HTTPTransport broadcasts messages to peers over HTTP with fire-and-forget POSTs. Peers are the
// base URLs of the other nodes. Delivery is asynchronous (a goroutine per peer), as required by
// the Transport contract, so it never blocks the node's event loop.
type HTTPTransport struct {
	mu     sync.RWMutex
	peers  []string
	client *http.Client
}

// NewHTTPTransport builds a transport for the given peer base URLs (which may be set later via
// SetPeers).
func NewHTTPTransport(peers []string) *HTTPTransport {
	return &HTTPTransport{
		peers:  append([]string(nil), peers...),
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// SetPeers replaces the peer list.
func (t *HTTPTransport) SetPeers(peers []string) {
	t.mu.Lock()
	t.peers = append([]string(nil), peers...)
	t.mu.Unlock()
}

// Peers returns a copy of the current peer list (used as catch-up sources when no bootstrap peers
// are configured).
func (t *HTTPTransport) Peers() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]string(nil), t.peers...)
}

// Broadcast POSTs msg to every peer asynchronously.
func (t *HTTPTransport) Broadcast(from string, msg Message) {
	t.mu.RLock()
	peers := append([]string(nil), t.peers...)
	t.mu.RUnlock()

	var path string
	var body any
	switch msg.Type {
	case MsgProposal:
		path, body = "/hdls/proposal", msg.Proposal
	case MsgVote:
		path, body = "/hdls/vote", msg.Vote
	case MsgPayment:
		path, body = "/hdls/payment", msg.Payment
	case MsgUpdate:
		path, body = "/hdls/update", msg.Update
	case MsgParameterVote:
		path, body = "/hdls/parameter-vote", msg.ParameterChange
	case MsgReassignmentVote:
		path, body = "/hdls/reassignment-vote", msg.Reassignment
	default:
		return
	}
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	for _, peer := range peers {
		go t.post(peer+path, b)
	}
}

func (t *HTTPTransport) post(url string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// Server exposes a Node over HTTP: peer message intake (/hdls/proposal, /hdls/vote) plus client
// endpoints (/hdls/pay, /hdls/status, /hdls/accounts).
type Server struct{ node *Node }

// NewServer wraps a node for HTTP serving.
func NewServer(n *Node) *Server { return &Server{node: n} }

// Handler returns the node's HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hdls/proposal", s.handleProposal)
	mux.HandleFunc("/hdls/vote", s.handleVote)
	mux.HandleFunc("/hdls/payment", s.handleGossipPayment)
	mux.HandleFunc("/hdls/update", s.handleAccountUpdate)
	mux.HandleFunc("/hdls/parameter-vote", s.handleParameterVote)
	mux.HandleFunc("/hdls/reassignment-vote", s.handleReassignmentVote)
	mux.HandleFunc("/hdls/pay", s.handlePay)
	mux.HandleFunc("/hdls/status", s.handleStatus)
	mux.HandleFunc("/hdls/accounts", s.handleAccounts)
	mux.HandleFunc("/hdls/resolve", s.handleResolve)
	mux.HandleFunc("/hdls/governance", s.handleGovernance)
	mux.HandleFunc("/hdls/checkpoint", s.handleCheckpoint)
	return mux
}

// handleCheckpoint serves the node's current committed state plus the certificate that finalised
// it, for a joining/rebooting peer to verify and adopt.
func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	snap := s.node.Snapshot()
	writeJSON(w, Checkpoint{Accounts: snap.Accounts, Parameters: snap.Parameters, GovernanceSequence: snap.GovernanceSequence, Commit: snap.LastCommit, Validators: snap.Validators})
}

func (s *Server) handleProposal(w http.ResponseWriter, r *http.Request) {
	var p ProposalMsg
	if !decode(w, r, &p) {
		return
	}
	s.node.SubmitProposal(p)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	var v consensus.Vote
	if !decode(w, r, &v) {
		return
	}
	s.node.SubmitVote(v)
	w.WriteHeader(http.StatusAccepted)
}

// handleGossipPayment receives a payment relayed from a peer node and queues it locally. The payer
// signature is verified at ingress — the same check as the client endpoint — so an unauthenticated
// attacker cannot fill the bounded mempool with junk that the proposer would then re-verify every block.
func (s *Server) handleGossipPayment(w http.ResponseWriter, r *http.Request) {
	var p ledger.Payment
	if !decode(w, r, &p) {
		return
	}
	if !ledger.VerifyPayment(s.node.cfg.Scheme, s.node.cfg.NetworkID, p) {
		http.Error(w, "invalid payment signature", http.StatusBadRequest)
		return
	}
	if err := s.node.AdmitPayment(p); err != nil {
		http.Error(w, "payment refused: "+err.Error(), http.StatusConflict)
		return // refused (e.g. it would overdraw the payer) — dropped, not queued, not relayed onward
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleAccountUpdate receives a signed account-attribute update — a name registration, a
// re-delegation, or a description change — from a client or a peer. It verifies the owner's signature
// and queues it; the node gossips it onward so it reaches whichever node proposes the block it names.
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	var update ledger.AccountUpdate
	if !decode(w, r, &update) {
		return
	}
	if !ledger.VerifyAccountUpdate(s.node.cfg.Scheme, s.node.cfg.NetworkID, update) {
		http.Error(w, "invalid update signature", http.StatusBadRequest)
		return
	}
	s.node.SubmitUpdate(update)
	w.WriteHeader(http.StatusAccepted)
}

// handleParameterVote receives a validator's parameter-change vote(s) — the proposed parameters, the
// parameters they amend, and the signatures — from a validator or a peer, and pools/gossips them. A
// change enacts only once pooled votes reach the > ⅔ quorum (verified again on block application).
func (s *Server) handleParameterVote(w http.ResponseWriter, r *http.Request) {
	var change ledger.ParameterChange
	if !decode(w, r, &change) {
		return
	}
	s.node.SubmitParameterVote(change)
	w.WriteHeader(http.StatusAccepted)
}

// handleReassignmentVote receives a validator's balance-reassignment vote(s) — the account, the
// balance it holds now, the balance to set, and the signatures — and pools/gossips them. It enacts
// only once pooled votes reach the > ⅔ quorum (verified again on block application).
func (s *Server) handleReassignmentVote(w http.ResponseWriter, r *http.Request) {
	var reassignment ledger.Reassignment
	if !decode(w, r, &reassignment) {
		return
	}
	s.node.SubmitReassignmentVote(reassignment)
	w.WriteHeader(http.StatusAccepted)
}

// handleGovernance serves a read-only view of the current parameters, the validator set/quorum, and
// the pending parameter and reassignment proposals with their vote tallies — for an admin UI.
func (s *Server) handleGovernance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.node.GovernanceView())
}

// handlePay is the client endpoint: a wallet submits a signed payment to its reference node. The
// payment must carry a valid payer signature; the node queues it (and gossips it to peers so it
// reaches whichever node proposes the block it names) and returns a signed receipt the payer can
// show a merchant as proof the node accepted and queued it.
func (s *Server) handlePay(w http.ResponseWriter, r *http.Request) {
	var p ledger.Payment
	if !decode(w, r, &p) {
		return
	}
	if !ledger.VerifyPayment(s.node.cfg.Scheme, s.node.cfg.NetworkID, p) {
		http.Error(w, "invalid payment signature", http.StatusBadRequest)
		return
	}
	// Admission is synchronous and guarded (see Node.admitPayment): a payment that would overdraw
	// the payer — or is stale, a flood overflow, etc. — is refused HERE, with no receipt, so the
	// receipt below always means "this node holds the payment, queued for its block".
	if err := s.node.AdmitPayment(p); err != nil {
		http.Error(w, "payment refused: "+err.Error(), http.StatusConflict)
		return
	}
	receipt, err := ledger.SignReceipt(s.node.cfg.Signer, p)
	if err != nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.node.Snapshot()
	writeJSON(w, map[string]any{
		"height":   snap.Height,
		"accounts": len(snap.Accounts),
	})
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.node.Snapshot().Accounts)
}

// ResolveRecord is one name-lookup answer — the ledger acting as a decentralized name server. A
// committed name maps to the account's public key AND its published network address (plus the
// description), so one query replaces both a key directory and DNS for anything registered
// on-chain. Height and StateDigest anchor the answer to the exact committed state it reflects:
// a client that does not trust a single node queries K independent nodes and compares — matching
// digests mean the answers come from the SAME ⅔-agreed state, no extra protocol needed.
type ResolveRecord struct {
	Name        string `json:"name,omitempty"`
	PublicKey   string `json:"publicKey"`
	Address     string `json:"address,omitempty"` // the on-chain published address (e.g. "http://10.0.0.2:8080")
	Description string `json:"description,omitempty"`
	Height      int64  `json:"height"`
	StateDigest string `json:"stateDigest"` // hex committed digest of the state this answer reflects
}

// handleResolve is GET /hdls/resolve?name=<registered name> (or ?key=<public key> for the reverse
// lookup). 404 when nothing on the committed ledger matches.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	name, key := r.URL.Query().Get("name"), r.URL.Query().Get("key")
	// Standard base64 keys contain '+', which query parsing decodes to a space — so a raw (properly
	// unencoded) key pasted into ?key= arrives with spaces and matches nothing. A space can never
	// appear in a base64 key, so restoring '+' is safe and lets a raw key work as documented (a
	// client that percent-encodes '+' as %2B already arrives correctly and is unaffected).
	key = strings.ReplaceAll(key, " ", "+")
	if (name == "") == (key == "") {
		http.Error(w, "pass exactly one of ?name= or ?key=", http.StatusBadRequest)
		return
	}
	snap := s.node.Snapshot()
	for _, account := range snap.Accounts {
		if (name != "" && account.Name == name) || (key != "" && account.PublicKey == key) {
			writeJSON(w, ResolveRecord{
				Name:        account.Name,
				PublicKey:   account.PublicKey,
				Address:     account.Address,
				Description: account.Description,
				Height:      snap.Height,
				StateDigest: hex.EncodeToString(snap.Decided[:]),
			})
			return
		}
	}
	http.Error(w, "not registered", http.StatusNotFound)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	// Bound the request body (audit: the spend path accepted unbounded JSON).
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
