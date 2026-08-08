package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-co-uk/go-ledger/ledger"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// Wallet holds the user's key locally and talks to a node over HTTP. It never sends the private
// key anywhere -- it signs payments locally and submits only the signed payment.
type Wallet struct {
	signer    *signing.Signer
	network   string
	nodeURL   string
	lookahead int64 // how many blocks ahead a payment names, so it reaches the proposer in time
	client    *http.Client

	// lastPaymentBlockID enforces strictly increasing BlockIDs across this wallet's payments. The
	// ledger BANS (balance-destroys) a payer whose payments in ONE block together overdraw, so the
	// wallet must never put two signed payments into the world naming the same block: a dishonest
	// intake node could hoard the pair and later propose both. Never signing two same-block payments
	// closes that at the source, whatever any node reports. Guarded by mu (handlers run concurrently).
	// It is PERSISTED (statePath) so a restart within the lookahead window cannot reissue a block a
	// still-pending pre-restart payment already named. (The one case local state cannot cover is the
	// SAME key loaded on two machines — documented; there, register-then-pay and modest amounts.)
	statePath          string
	mu                 sync.Mutex
	lastPaymentBlockID int64
}

// LoadOrCreateWallet loads the key from keyfile (asking for its passphrase if it is sealed — see
// keyfile.go), or generates and saves a new one if the file is absent.
func LoadOrCreateWallet(keyfile, network, nodeURL string, lookahead int64) (*Wallet, error) {
	pub, priv, err := loadOrCreateKey(keyfile)
	if err != nil {
		return nil, err
	}
	if lookahead < 1 {
		lookahead = 3
	}
	statePath := keyfile + ".state"
	return &Wallet{
		signer:             signing.NewSigner(signing.Ed25519{}, network, pub, priv),
		network:            network,
		nodeURL:            nodeURL,
		lookahead:          lookahead,
		client:             &http.Client{Timeout: 5 * time.Second},
		statePath:          statePath,
		lastPaymentBlockID: readWalletState(statePath), // resume the counter across restarts
	}, nil
}

// walletState is the small sidecar (<keyfile>.state) that survives a restart, so the same-block
// payment guard (lastPaymentBlockID) is not reset to zero when the process restarts.
type walletState struct {
	LastPaymentBlockID int64 `json:"lastPaymentBlockID"`
}

func readWalletState(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var s walletState
	if json.Unmarshal(data, &s) != nil {
		return 0
	}
	return s.LastPaymentBlockID
}

func writeWalletState(path string, lastPaymentBlockID int64) {
	data, _ := json.Marshal(walletState{LastPaymentBlockID: lastPaymentBlockID})
	_ = os.WriteFile(path, data, 0o600) // best-effort: a lost write only re-risks the narrow restart window
}

// PublicKey is the wallet's account identity (base64).
func (w *Wallet) PublicKey() string { return w.signer.PublicKeyBase64() }

// looksLikePublicKey reports whether the recipient input is a raw account key (base64 of a public
// key) rather than a registered name. Anything else is sent as a NAME inside the signed payment and
// resolved on-chain by every node against the committed registry — the wallet's node never
// translates it, so a dishonest node cannot remap a name to its own key. An unregistered name is a
// deterministic network-wide no-op (nothing is debited), which also makes typos safe.
func looksLikePublicKey(s string) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	return err == nil && len(raw) == 32 // Ed25519 public key size
}

// RegisterName submits a signed account update claiming a unique display name (a "domain") for this
// wallet, with an optional description. The name lands when the block it names commits; if another
// account already holds it, the claim is simply ignored by the network.
func (w *Wallet) RegisterName(name, description string) error {
	if len(name) > ledger.MaxAccountNameLength {
		return fmt.Errorf("name too long: at most %d characters (so a name can never look like a public key)", ledger.MaxAccountNameLength)
	}
	update := ledger.AccountUpdate{
		From:        w.PublicKey(),
		Name:        name,
		Description: description,
		BlockID:     w.height() + w.lookahead,
	}
	signedUpdate, err := ledger.SignAccountUpdate(w.signer, update)
	if err != nil {
		return err
	}
	// Pre-check the whole serialized update against the network's message-size cap. The node drops an
	// over-bound update SILENTLY (its intake has no error channel), so without this the wallet would
	// report success for a registration that never lands. Best-effort: if the node is unreachable for
	// the governance view, submit anyway and let the node decide.
	if view, gerr := w.Governance(); gerr == nil && !ledger.WithinMessageBound(view.MaxMessageBytes, signedUpdate) {
		return fmt.Errorf("registration too large: the name and description exceed the network's %d-byte message limit — shorten the description", view.MaxMessageBytes)
	}
	requestBody, _ := json.Marshal(signedUpdate)
	response, err := w.client.Post(w.nodeURL+"/hdls/update", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("node rejected the name registration: %s", string(responseBody))
	}
	return nil
}

func (w *Wallet) accounts() ([]ledger.Account, error) {
	resp, err := w.client.Get(w.nodeURL + "/hdls/accounts")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var accts []ledger.Account
	if err := json.NewDecoder(resp.Body).Decode(&accts); err != nil {
		return nil, err
	}
	return accts, nil
}

// Balance is the wallet account's current balance, or zero if the account has no entry yet.
func (w *Wallet) Balance() (*big.Rat, error) {
	accts, err := w.accounts()
	if err != nil {
		return nil, err
	}
	for _, a := range accts {
		if a.PublicKey == w.PublicKey() && a.Amount != nil {
			return a.Amount, nil
		}
	}
	return new(big.Rat), nil
}

// nextPaymentBlockID names the block a new payment clears in: near-future (height + lookahead), and
// strictly greater than any block this wallet's earlier payments named. Two payments in quick
// succession therefore land in consecutive blocks instead of sharing one — see lastPaymentBlockID
// for why sharing a block must never happen.
func (w *Wallet) nextPaymentBlockID() int64 {
	blockID := w.height() + w.lookahead
	w.mu.Lock()
	defer w.mu.Unlock()
	if blockID <= w.lastPaymentBlockID {
		blockID = w.lastPaymentBlockID + 1
	}
	w.lastPaymentBlockID = blockID
	writeWalletState(w.statePath, blockID) // persist before the payment goes out, so a restart resumes here
	return blockID
}

func (w *Wallet) height() int64 {
	resp, err := w.client.Get(w.nodeURL + "/hdls/status")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var st struct {
		Height int64 `json:"height"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	return st.Height
}

// Send builds, signs and submits a payment, and returns the node's signed receipt plus whether that
// receipt is genuinely trustworthy: it must carry a valid node signature, acknowledge THIS payment
// (its PaymentID matches), and come from a node that is actually in the current validator set — a
// dishonest non-validator can otherwise self-sign a receipt over an unrelated payment. The receipt is
// still only a provisional acceptance; final assurance is the block's commit.
func (w *Wallet) Send(to, amount string) (ledger.Receipt, bool, error) {
	amt, ok := new(big.Rat).SetString(amount)
	if !ok {
		return ledger.Receipt{}, false, fmt.Errorf("invalid amount %q", amount)
	}
	if amt.Sign() <= 0 {
		return ledger.Receipt{}, false, fmt.Errorf("amount must be positive")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return ledger.Receipt{}, false, fmt.Errorf("recipient is required")
	}
	p := ledger.Payment{
		PublicKeyFrom: w.PublicKey(),
		Amount:        amt,
		BlockID:       w.nextPaymentBlockID(), // a near-future block, unique among this wallet's payments
	}
	switch {
	case looksLikePublicKey(to):
		p.PublicKeyTo = to
	case len(to) <= ledger.MaxAccountNameLength:
		p.ToName = to // signed as a name; the NETWORK resolves it against the committed registry
	default:
		// Too long to be a registered name, but not a valid public key either — almost certainly a
		// mistyped/truncated key. Refuse it rather than sign it as a name that resolves to nobody: the
		// node would drop it and the sender would mistake the receipt for a completed payment.
		return ledger.Receipt{}, false, fmt.Errorf("recipient is neither a valid public key nor a name (a name is at most %d characters)", ledger.MaxAccountNameLength)
	}
	p, err := ledger.SignPayment(w.signer, p)
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	body, _ := json.Marshal(p)
	resp, err := w.client.Post(w.nodeURL+"/hdls/pay", "application/json", bytes.NewReader(body))
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ledger.Receipt{}, false, fmt.Errorf("node rejected payment: %s", string(b))
	}
	var receipt ledger.Receipt
	_ = json.NewDecoder(resp.Body).Decode(&receipt) // best-effort: a node may accept without a receipt
	verified := receipt.Node != "" && receipt.Signature != "" &&
		receipt.Verify(signing.Ed25519{}, w.network) &&
		receipt.PaymentID == ledger.PaymentID(p) &&
		w.isValidator(receipt.Node)
	return receipt, verified, nil
}

// isValidator reports whether `node` is in the current validator set (per the node's governance view).
func (w *Wallet) isValidator(node string) bool {
	view, err := w.Governance()
	if err != nil {
		return false
	}
	for _, validator := range view.Validators {
		if validator == node {
			return true
		}
	}
	return false
}
