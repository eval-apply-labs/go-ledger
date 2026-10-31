package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Wallet holds the user's key locally and talks to a node over HTTP. It never sends the private
// key anywhere -- it signs payments locally and submits only the signed payment.
type Wallet struct {
	privateKey  []byte // the key's seed; the wallet signs with it for the pinned network id (see signer)
	publicKey   string // the key the seed derives, in base64
	networkName string // the -network flag: the network id the wallet pins must start with it and "/"
	nodeURL     string
	lookahead   int64 // how many blocks ahead a payment names, so it reaches the proposer in time
	client      *http.Client

	// saved is what <keyfile>.state keeps across restarts (walletState), written at statePath. Guarded
	// by mutex (handlers run concurrently).
	statePath string
	mutex     sync.Mutex
	saved     walletState

	// heightSeen is the last height accepted from the node, at heightSeenAt (see height). Guarded by mutex.
	heightSeen   int64
	heightSeenAt time.Time

	// networkID and pinned are what the wallet believes, kept in pinPath (trust.go): the network id it
	// signs and verifies for, and the validator set whose signatures count. They are empty until the
	// first use pins them. networkSigner signs with the wallet's key for networkID, built once (see
	// signer). Guarded by mutex.
	pinPath       string
	networkID     string
	pinned        *consensus.ValidatorSet
	networkSigner *signing.Signer
}

// blockSlack bounds how far a payment's block may run past height + lookahead (a burst of payments
// gets one block each), and how far a reported height may outrun the last one accepted, beyond one
// block per millisecond since then.
const blockSlack = 32

// LoadOrCreateWallet loads the key from keyfile (asking for its passphrase if it is sealed — see
// keyfile.go), or generates and saves a new one if the file is absent, for the network named
// networkName, and what it pinned for that network, if anything. A lookahead below 1 is 3. One above
// node.MaxBlockIDLookahead - blockSlack is refused: a node takes a payment, an update or a freeze only
// up to node.MaxBlockIDLookahead blocks past its height, and a burst of payments runs up to blockSlack
// blocks past the lookahead.
func LoadOrCreateWallet(keyfile, networkName, nodeURL string, lookahead int64) (*Wallet, error) {
	if lookahead < 1 {
		lookahead = 3
	}
	if lookahead > node.MaxBlockIDLookahead-blockSlack {
		return nil, fmt.Errorf("a lookahead of %d blocks is too far: at most %d, so that a burst of payments, up to %d blocks further, stays inside the node's %d-block window",
			lookahead, node.MaxBlockIDLookahead-blockSlack, blockSlack, node.MaxBlockIDLookahead)
	}
	publicKey, privateKey, err := loadOrCreateKey(keyfile)
	if err != nil {
		return nil, err
	}
	statePath := keyfile + ".state"
	pinPath := keyfile + ".trust"
	networkID, pinned, err := loadPin(pinPath, networkName)
	if err != nil {
		return nil, err
	}
	return &Wallet{
		privateKey:  privateKey,
		publicKey:   base64.StdEncoding.EncodeToString(publicKey),
		networkName: networkName,
		nodeURL:     nodeURL,
		lookahead:   lookahead,
		client:      &http.Client{Timeout: 5 * time.Second},
		statePath:   statePath,
		saved:       readWalletState(statePath, networkName), // resume the counter, the last name and the newest height
		pinPath:     pinPath,
		networkID:   networkID,
		pinned:      pinned,
	}, nil
}

// walletState is the small sidecar (<keyfile>.state) that survives a restart. It belongs to one
// network: started on another network name, or on a file of an earlier network, which names none, the
// wallet starts it afresh.
type walletState struct {
	NetworkName string `json:"networkName"`

	// LastPaymentBlockID enforces strictly increasing BlockIDs across this wallet's payments (and its
	// account updates: a block carries one update per account). The ledger destroys the balance of a
	// payer whose payments in ONE block together overdraw, so the wallet must never put two signed
	// payments into the world naming the same block: a dishonest intake node could hoard the pair and
	// later propose both. Never signing two same-block payments closes that at the source, whatever any
	// node reports. It is kept here so a restart within the lookahead window cannot reissue a block a
	// still-pending pre-restart payment already named. (The one case local state cannot cover is one key
	// loaded on two machines at once: see ui/USER_MANUAL.md, "One key, one machine".) It only grows, and
	// it never runs more than blockSlack past the verified height in progress + lookahead (see
	// nextPaymentBlockID).
	LastPaymentBlockID int64 `json:"lastPaymentBlockID"`

	// RequestBlockIDs are the blocks of the name requests this wallet signed as their fee payer (spec
	// 7.3). A block prefers a payer's requests, so its payments for that block would lapse: the payment
	// counter skips these blocks.
	RequestBlockIDs []int64 `json:"requestBlockIDs,omitempty"`

	// LastName is the last name a verified state gave this wallet's key, and NewestHeight the height of
	// the newest state the wallet verified (spec 7.2). The wallet refuses an older state, so its node
	// cannot roll it back to before a pending change, and when no state verifies its freeze button
	// still signs a freeze for LastName.
	LastName     string `json:"lastName,omitempty"`
	NewestHeight int64  `json:"newestHeight,omitempty"`

	// FreezeName and FreezeBlockID are the freeze this wallet last sent, until a verified state shows
	// the name frozen (see freezeStatus).
	FreezeName    string `json:"freezeName,omitempty"`
	FreezeBlockID int64  `json:"freezeBlockID,omitempty"`
}

func readWalletState(path, networkName string) walletState {
	fresh := walletState{NetworkName: networkName}
	data, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	var savedState walletState
	if json.Unmarshal(data, &savedState) != nil || savedState.NetworkName != networkName {
		return fresh // unreadable, or another network's, or an earlier network's, which names none
	}
	return savedState
}

// saveState writes saved to <keyfile>.state. The caller holds the mutex. It is best-effort: a lost
// write only re-risks the narrow restart window, or forgets a height or a name until the next read.
func (wallet *Wallet) saveState() {
	data, _ := json.Marshal(wallet.saved)
	_ = os.WriteFile(wallet.statePath, data, 0o600)
}

// PublicKey is the wallet's key (base64), the current key of the name it holds, if any.
func (wallet *Wallet) PublicKey() string { return wallet.publicKey }

// pinnedNetworkID is the network id the wallet pinned (trust.go), or "" before its first use.
func (wallet *Wallet) pinnedNetworkID() string {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	return wallet.networkID
}

// signer signs with the wallet's key for the pinned network id (spec 7.2). The wallet signs only after
// it has read a verified state, which pins the id on first use. The signer is built from the seed once,
// since deriving the key costs almost half a signature.
func (wallet *Wallet) signer() (*signing.Signer, error) {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	if wallet.networkSigner == nil {
		if wallet.networkID == "" {
			return nil, errors.New("no network is pinned yet: the wallet has not read a state its node's validators signed")
		}
		signer, err := signing.NewSigner(signing.MLDSA65{}, wallet.networkID, wallet.privateKey)
		if err != nil {
			return nil, err
		}
		wallet.networkSigner = signer
	}
	return wallet.networkSigner, nil
}

// errNotRegistered is why a wallet whose key is no name's current key cannot pay, update or vote: every
// message names its signer, and nodes check it under that name's key.
var errNotRegistered = errors.New("this wallet's key is not the key of any name on the ledger")

// nameOfKey is the name of the account whose current key is publicKey, or "" when there is none.
func nameOfKey(accounts []ledger.Account, publicKey string) string {
	for _, account := range accounts {
		if account.PublicKey == publicKey {
			return account.Name
		}
	}
	return ""
}

// registeredName is the wallet's name in a verified state (spec 7.2): the account whose current key is
// the wallet's key, or errNotRegistered when there is none.
func (wallet *Wallet) registeredName(checkpoint node.Checkpoint) (string, error) {
	if name := nameOfKey(checkpoint.State.Accounts, wallet.PublicKey()); name != "" {
		return name, nil
	}
	return "", errNotRegistered
}

// typedName is a name as a person typed it, lowercased and trimmed (spec 3.2), or an error when that is
// not a name in its one spelling. The pages show the name it returns.
func typedName(typed string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(typed))
	if !ledger.CanonicalName(name) {
		return "", fmt.Errorf("%q is not a name: a name is 1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or a digit", typed)
	}
	return name, nil
}

// typedKey is a public key as a person pasted it, trimmed, or an error when it is not an ML-DSA-65 key
// in its one spelling.
func typedKey(typed string) (string, error) {
	key := strings.TrimSpace(typed)
	if !signing.ValidPublicKeyBase64(key) {
		return "", errors.New("the new key is not a public key: paste the whole key the other wallet copied")
	}
	return key, nil
}

// fingerprint is how the pages show a key (spec 7.2): the first 16 hex digits of the SHA-256 of the raw
// key. A key is 2604 characters, so no page prints another name's key in full.
func fingerprint(publicKey string) string {
	raw, _ := base64.StdEncoding.DecodeString(publicKey)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:8])
}

// pendingChange describes an account's pending change, as "key change (to key ab12…)" or "bank change
// (to bank-b)", or is "" when it has none. The change takes effect at the end of block PendingHeight.
func pendingChange(account ledger.Account) string {
	switch {
	case account.PendingKey != "":
		return fmt.Sprintf("key change (to key %s…)", fingerprint(account.PendingKey))
	case account.PendingBank != "":
		return fmt.Sprintf("bank change (to %s)", account.PendingBank)
	}
	return ""
}

// UpdateAccount submits a signed account update for the wallet's name: a description, the name its
// balance backs as stake (a validator's name; its own, for a bank), and the address a validator
// publishes for its peers. A blank field is left unchanged, and ledger.ClearField ("-") clears it. The
// update lands when the block it names commits. The wallet first checks it against the verified state
// as a node's intake does (ledger.UpdateFitsState, its size measured as signed), so an update that
// could not apply uses up no block; the node refuses, with the reason, one that its own state does not
// take. Like a payment, each update names a block of its own (see nextPaymentBlockID): a block carries
// one update per account, so of two updates naming one block, one would be lost. It returns the update
// it signed, whose delegate the page shows as the wallet read it (spec 3.2).
func (wallet *Wallet) UpdateAccount(description, delegate, address string) (ledger.AccountUpdate, error) {
	delegate, address = strings.TrimSpace(delegate), strings.TrimSpace(address)
	if description == "" && delegate == "" && address == "" {
		return ledger.AccountUpdate{}, errors.New("nothing to update: give a description, a name to delegate to, or an address")
	}
	if delegate != "" && delegate != ledger.ClearField {
		var err error
		if delegate, err = typedName(delegate); err != nil {
			return ledger.AccountUpdate{}, fmt.Errorf("delegate to a validator's name, or %q for none: %w", ledger.ClearField, err)
		}
	}
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		return ledger.AccountUpdate{}, err
	}
	name, err := wallet.registeredName(checkpoint)
	if err != nil {
		return ledger.AccountUpdate{}, err
	}
	update := ledger.AccountUpdate{Name: name, Delegate: delegate, Description: description, Address: address}
	// The node measures the signed update against the message bound, so check a copy that already has
	// a signature's length and the widest block number (a few bytes more than the real one).
	measured := update
	measured.BlockID, measured.Signature = math.MaxInt64, strings.Repeat("A", signing.SignatureBase64Length)
	if err := ledger.UpdateFitsState(checkpoint.State, measured); err != nil {
		return ledger.AccountUpdate{}, err // the node's intake check, before the update takes a block
	}
	if update.BlockID, err = wallet.nextPaymentBlockID(checkpoint.State.Height + 1); err != nil {
		return ledger.AccountUpdate{}, err
	}
	signer, err := wallet.signer()
	if err != nil {
		return ledger.AccountUpdate{}, err
	}
	signedUpdate, err := ledger.SignAccountUpdate(signer, update)
	if err != nil {
		return ledger.AccountUpdate{}, err
	}
	return signedUpdate, wallet.postJSON("/hdls/update", signedUpdate)
}

// nextPaymentBlockID names the block a new payment, or account update, clears in: near-future
// (heightInProgress + lookahead), and always past the last block this wallet named, so the counter
// never goes back. Two payments in quick succession therefore land in consecutive blocks instead of
// sharing one — see LastPaymentBlockID for why sharing a block must never happen. heightInProgress is
// the height in progress of the verified state the caller acts on (Send, UpdateAccount), never a height
// a node merely reports: its validators signed it, so no node can push the counter far ahead, and the
// wallet refuses a state older than the newest it verified, so a node that lies can only make a payment
// miss its block. It skips the blocks of the requests this wallet pays for (RequestBlockIDs). The
// block, skips included, is never more than blockSlack past heightInProgress + lookahead. Past that the
// wallet refuses to sign until the chain advances: payments that name the blocks before it may still
// be pending, as after a burst.
func (wallet *Wallet) nextPaymentBlockID(heightInProgress int64) (int64, error) {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	window := heightInProgress + wallet.lookahead
	blockID := max(window, wallet.saved.LastPaymentBlockID+1)
	wallet.saved.RequestBlockIDs = slices.DeleteFunc(wallet.saved.RequestBlockIDs, func(requestBlockID int64) bool { return requestBlockID < heightInProgress })
	for slices.Contains(wallet.saved.RequestBlockIDs, blockID) {
		blockID++ // a request this wallet pays for names that block, and the payment would lapse there
	}
	if blockID > window+blockSlack {
		return 0, fmt.Errorf("this wallet's payments, updates and requests already name the blocks up to %d, as far past the height in progress %d as a payment may go: wait for the chain to advance", blockID-1, heightInProgress)
	}
	wallet.saved.LastPaymentBlockID = blockID
	wallet.saveState() // persist before the payment goes out, so a restart resumes here
	return blockID, nil
}

// keepRequestBlock records the block of a request this wallet signed as its fee payer, so the payment
// counter skips it (spec 7.3).
func (wallet *Wallet) keepRequestBlock(blockID int64) {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	if !slices.Contains(wallet.saved.RequestBlockIDs, blockID) {
		wallet.saved.RequestBlockIDs = append(wallet.saved.RequestBlockIDs, blockID)
		wallet.saveState()
	}
}

// maxStatusBytes bounds a read of the node's /hdls/status, whose honest answer is under 200 bytes.
const maxStatusBytes = 4 << 10

// maxReceiptBytes bounds a read of the node's receipt for a payment, whose honest answer is about 4.6
// KB (its signature is 4,412 characters).
const maxReceiptBytes = 16 << 10

// status is what the node's /hdls/status answers: the height in progress, one above its committed
// height, and the network id it serves. Like every answer the wallet reads from its node, which it does
// not trust, it is refused with a status of 300 or more, and read only up to a bound: here
// maxStatusBytes.
func (wallet *Wallet) status() (heightInProgress int64, networkID string, err error) {
	response, err := wallet.client.Get(wallet.nodeURL + "/hdls/status")
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return 0, "", fmt.Errorf("the node answered its status with %s", response.Status)
	}
	var status struct {
		HeightInProgress int64  `json:"heightInProgress"`
		NetworkID        string `json:"networkId"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, maxStatusBytes)).Decode(&status)
	return status.HeightInProgress, status.NetworkID, err
}

// height is the node's height in progress as its /hdls/status reports it, unverified. A freeze and a
// drafted request name their block from it (a freeze must work when no state verifies); payments and
// updates take theirs from the verified state (nextPaymentBlockID). A report that outruns the last
// accepted one by more than blockSlack plus one block per millisecond since then (faster than any node
// commits) is ignored in favour of that last one, so a single wild answer cannot send a freeze or a
// draft to a block far ahead.
func (wallet *Wallet) height() (int64, error) {
	heightInProgress, _, err := wallet.status()
	if err != nil {
		return 0, err
	}
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	if !wallet.heightSeenAt.IsZero() && heightInProgress > wallet.heightSeen+blockSlack+time.Since(wallet.heightSeenAt).Milliseconds() {
		return wallet.heightSeen, nil
	}
	wallet.heightSeen, wallet.heightSeenAt = heightInProgress, time.Now()
	return heightInProgress, nil
}

// paymentReview is what the wallet shows before it signs a payment (spec 7.3): the payee's name, as the
// wallet read what was typed, and its bank of record in the verified state, so a lookalike name shows
// a bank the payer does not expect.
type paymentReview struct {
	To, Amount, Bank string
}

// ReviewPayment checks a payment against a verified state, and signs and sends nothing: the amount is
// positive, the wallet holds a name, and the payment passes the checks a node's intake makes against
// that state (ledger.PaymentFitsState and ledger.CreditFitsBalanceCap). So a payment the ledger's rules
// refuse uses up no block of the payment counter (Send takes one only after this).
func (wallet *Wallet) ReviewPayment(checkpoint node.Checkpoint, recipient, amount string) (paymentReview, error) {
	parsedAmount, err := parseAmount(amount)
	if err != nil {
		return paymentReview{}, fmt.Errorf("invalid amount %q: %w", amount, err)
	}
	if parsedAmount.Sign() <= 0 {
		return paymentReview{}, errors.New("amount must be positive")
	}
	// A payment is to a name, which every node looks up in its own state: the wallet's node never
	// translates it, so a dishonest node cannot remap it to its own key.
	to, err := typedName(recipient)
	if err != nil {
		return paymentReview{}, fmt.Errorf("pay a name: %w", err)
	}
	name, err := wallet.registeredName(checkpoint)
	if err != nil {
		return paymentReview{}, err
	}
	payment := ledger.Payment{From: name, To: to, Amount: parsedAmount}
	if err := ledger.PaymentFitsState(checkpoint.State, payment); err != nil {
		return paymentReview{}, err
	}
	if !ledger.CreditFitsBalanceCap(checkpoint.State, payment) {
		return paymentReview{}, fmt.Errorf("it would lift %s above the largest balance the network allows (MaxAccountBalance)", to)
	}
	payee, _ := ledger.FindAccount(checkpoint.State.Accounts, to)
	return paymentReview{To: to, Amount: ledger.FormatAmount(parsedAmount), Bank: payee.Bank}, nil
}

// Send builds, signs and submits a payment from the wallet's name to the name `recipient`, after the
// checks of ReviewPayment, and returns the node's signed receipt plus whether that receipt is genuinely
// trustworthy: it must name a validator this wallet pinned (trust.go), verify under the key pinned for
// that name, and acknowledge THIS payment (its PaymentID matches) — a dishonest non-validator can
// otherwise self-sign a receipt over an unrelated payment. The receipt is still only a provisional
// acceptance; final assurance is the block's commit.
func (wallet *Wallet) Send(recipient, amount string) (ledger.Receipt, bool, error) {
	checkpoint, err := wallet.verifiedState()
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	review, err := wallet.ReviewPayment(checkpoint, recipient, amount)
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	name, _ := wallet.registeredName(checkpoint) // ReviewPayment found it
	// A near-future block, unique among this wallet's payments, from the verified state's height.
	blockID, err := wallet.nextPaymentBlockID(checkpoint.State.Height + 1)
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	signer, err := wallet.signer()
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	parsedAmount, _ := ledger.ParseAmount(review.Amount)
	payment, err := ledger.SignPayment(signer, ledger.Payment{From: name, To: review.To, Amount: parsedAmount, BlockID: blockID})
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	body, _ := json.Marshal(payment)
	response, err := wallet.client.Post(wallet.nodeURL+"/hdls/pay", "application/json", bytes.NewReader(body))
	if err != nil {
		return ledger.Receipt{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return ledger.Receipt{}, false, fmt.Errorf("node rejected payment: %s", string(responseBody))
	}
	var receipt ledger.Receipt
	// Best-effort: a node may accept without a receipt, and one past maxReceiptBytes is not read.
	_ = json.NewDecoder(io.LimitReader(response.Body, maxReceiptBytes)).Decode(&receipt)
	pinnedKey := wallet.pinnedKey(receipt.Node)
	verified := receipt.Node != "" && receipt.Signature != "" && pinnedKey != "" &&
		receipt.PaymentID == ledger.PaymentID(payment) &&
		receipt.Verify(signing.MLDSA65{}, wallet.pinnedNetworkID(), pinnedKey)
	return receipt, verified, nil
}

// parseAmount reads an amount a person typed with the network's own parser, so whatever it accepts
// every node accepts, and also takes a leading decimal point: ".5" is 0.5.
func parseAmount(typed string) (*big.Rat, error) {
	typed = strings.TrimSpace(typed)
	if strings.HasPrefix(typed, ".") {
		typed = "0" + typed
	}
	return ledger.ParseAmount(typed)
}

// pinnedKey returns the key this wallet pinned (trust.go) for the validator named `validatorName`,
// never one the node merely lists, or "" if that name is not pinned or nothing is pinned yet.
func (wallet *Wallet) pinnedKey(validatorName string) string {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	if wallet.pinned == nil {
		return ""
	}
	return wallet.pinned.PublicKey(validatorName)
}

// Freeze signs a freeze of the wallet's own name and submits it (spec 7.3). A freeze takes no value of
// the payment counter: it names the block height + lookahead, or the name's pending change's block if
// that is sooner, since freezes apply before due changes and so the freeze still cancels it. The height
// is the verified state's height in progress, as for a payment, so a node cannot make the freeze name a
// block no verified state will reach, which would hide that it did not land. When no state verifies
// (its banks re-keyed, or its node misbehaves), it freezes the last name a verified state gave this
// wallet's key, at the height the node reports, the only one left. The wallet keeps the freeze it sent
// until a verified state shows the name frozen (freezeStatus).
func (wallet *Wallet) Freeze() (ledger.NameRequest, error) {
	var name string
	var blockID int64
	if checkpoint, err := wallet.verifiedState(); err == nil {
		if name, err = wallet.registeredName(checkpoint); err != nil {
			return ledger.NameRequest{}, err
		}
		heightInProgress := checkpoint.State.Height + 1
		blockID = heightInProgress + wallet.lookahead
		account, _ := ledger.FindAccount(checkpoint.State.Accounts, name)
		if account.PendingHeight >= heightInProgress && account.PendingHeight < blockID {
			blockID = account.PendingHeight
		}
	} else if name = wallet.lastName(); name == "" {
		return ledger.NameRequest{}, fmt.Errorf("no state verifies (%v), and this wallet has never held a name to freeze", err)
	} else {
		height, err := wallet.height()
		if err != nil {
			return ledger.NameRequest{}, err
		}
		blockID = height + wallet.lookahead
	}
	signer, err := wallet.signer()
	if err != nil {
		return ledger.NameRequest{}, err
	}
	freeze, err := ledger.SignAsRequestor(signer, ledger.NameRequest{Kind: ledger.FreezeKind, Name: name, Requestor: name, BlockID: blockID})
	if err != nil {
		return ledger.NameRequest{}, err
	}
	if err := wallet.postJSON("/hdls/name-request", freeze); err != nil {
		return ledger.NameRequest{}, err
	}
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	wallet.saved.FreezeName, wallet.saved.FreezeBlockID = name, blockID
	wallet.saveState()
	return freeze, nil
}

// lastName is the last name a verified state gave this wallet's key, or "" when none ever did.
func (wallet *Wallet) lastName() string {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	return wallet.saved.LastName
}

// freezeStatus says what became of the freeze this wallet last sent (spec 7.3), given the verified
// state, or nil when none verifies: "" once a verified state shows its name frozen, when the wallet
// forgets it (the page then says the name is frozen); a warning that it did not land once its block
// has committed without that; and otherwise that it was sent.
func (wallet *Wallet) freezeStatus(state *ledger.LedgerState) string {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	name, blockID := wallet.saved.FreezeName, wallet.saved.FreezeBlockID
	if name == "" {
		return ""
	}
	if state != nil {
		if account, _ := ledger.FindAccount(state.Accounts, name); account.Frozen {
			wallet.saved.FreezeName, wallet.saved.FreezeBlockID = "", 0
			wallet.saveState()
			return ""
		}
		if state.Height >= blockID {
			return fmt.Sprintf("Your freeze of %s for block %d did not land: block %d has passed and %s is not frozen. Contact your bank, or another bank.", name, blockID, blockID, name)
		}
	}
	return fmt.Sprintf("Your freeze of %s was sent for block %d. It is not confirmed until a verified state shows %s frozen.", name, blockID, name)
}

// postJSON posts body to the node, and returns the node's reason when it refuses it.
func (wallet *Wallet) postJSON(path string, body any) error {
	data, _ := json.Marshal(body)
	response, err := wallet.client.Post(wallet.nodeURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("node rejected the submission: %s", string(message))
	}
	return nil
}
