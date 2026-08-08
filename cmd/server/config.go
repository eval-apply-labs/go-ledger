package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// fileConfig is the on-disk node configuration (JSON). Amounts are exact decimal strings.
type fileConfig struct {
	NetworkID          string          `json:"networkId"`
	Listen             string          `json:"listen"`
	PublicKey          string          `json:"publicKey"`
	PrivateKey         string          `json:"privateKey"`
	TransactionCost    string          `json:"transactionCost"`
	TimeoutMs          int             `json:"timeoutMs"`
	MinBlockIntervalMs int             `json:"minBlockIntervalMs"`
	MaxPeers           int             `json:"maxPeers"`           // >0 enables Hypernode dimensional gossip (auto-scaled)
	ValidatorThreshold string          `json:"validatorThreshold"` // set → DYNAMIC validators derived from stake; empty → fixed set
	MaxMempool         int             `json:"maxMempool"`         // cap on pending payments (0 → a sane default)
	MaxCommentLength   int             `json:"maxCommentLength"`   // cap on payment comment bytes (0 → a sane default)
	MaxMessageBytes    int             `json:"maxMessageBytes"`    // genesis per-message serialized-size cap, governed thereafter (0 → a sane default)
	Validators         []fileValidator `json:"validators"`
	Peers              []string        `json:"peers"`
	Bootstrap          []string        `json:"bootstrap"`
	SnapshotPath       string          `json:"snapshotPath"`
	BackupPath         string          `json:"backupPath"`
	Genesis            []fileAccount   `json:"genesis"`
}

type fileValidator struct {
	PublicKey string `json:"publicKey"`
	Power     int64  `json:"power"`
	URL       string `json:"url"` // base URL; required (on every validator) to use maxPeers gossip
}

type fileAccount struct {
	PublicKey     string `json:"publicKey"`
	Name          string `json:"name"`
	Amount        string `json:"amount"`
	ValidatorNode string `json:"validatorNode"`
	Description   string `json:"description"`
	Address       string `json:"address"` // optional on-chain gossip address (peer DNS)
}

// load parses a node config file into a node.Config plus the listen address and peer URLs.
func load(path string) (cfg node.Config, listen string, peers []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, "", nil, err
	}
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return cfg, "", nil, fmt.Errorf("parse config: %w", err)
	}

	pub, err := base64.StdEncoding.DecodeString(fc.PublicKey)
	if err != nil {
		return cfg, "", nil, fmt.Errorf("publicKey: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(fc.PrivateKey)
	if err != nil {
		return cfg, "", nil, fmt.Errorf("privateKey: %w", err)
	}
	// A wrong-length key must fail HERE: it would otherwise start a node that serves HTTP but can
	// never sign a vote or a receipt (the consensus driver swallows signing errors by design — see
	// castVote), i.e. a silent non-participant that looks up.
	if len(pub) != ed25519.PublicKeySize {
		return cfg, "", nil, fmt.Errorf("publicKey: %d bytes after base64 decode, want %d (an Ed25519 key from keygen)", len(pub), ed25519.PublicKeySize)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return cfg, "", nil, fmt.Errorf("privateKey: %d bytes after base64 decode, want %d (an Ed25519 key from keygen)", len(priv), ed25519.PrivateKeySize)
	}
	cost, ok := new(big.Rat).SetString(fc.TransactionCost)
	if !ok {
		return cfg, "", nil, fmt.Errorf("bad transactionCost %q", fc.TransactionCost)
	}
	if cost.Sign() < 0 {
		// A negative cost would credit every payee MORE than the payer is debited (minting |cost| per
		// payment) and re-admit negative amounts past the below-cost filter. Governance can never vote
		// one in (LedgerParameters.Acceptable); the genesis value must clear the same bar.
		return cfg, "", nil, fmt.Errorf("transactionCost %q is negative; use \"0\" for no fee", fc.TransactionCost)
	}

	vals := make([]consensus.Validator, 0, len(fc.Validators))
	for _, v := range fc.Validators {
		vals = append(vals, consensus.Validator{PublicKey: v.PublicKey, Power: v.Power})
	}

	genesis := make([]ledger.Account, 0, len(fc.Genesis))
	for _, a := range fc.Genesis {
		amt, ok := new(big.Rat).SetString(a.Amount)
		if !ok {
			return cfg, "", nil, fmt.Errorf("bad genesis amount %q for %s", a.Amount, a.PublicKey)
		}
		genesis = append(genesis, ledger.Account{
			PublicKey: a.PublicKey, Name: a.Name, Amount: amt,
			ValidatorNode: a.ValidatorNode, Description: a.Description, Address: a.Address,
		})
	}

	timeout := time.Duration(fc.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	// Fixed validator set, or dynamic (derived from stake) if a threshold is configured.
	var validators *consensus.ValidatorSet
	var threshold *big.Rat
	if fc.ValidatorThreshold != "" {
		th, ok := new(big.Rat).SetString(fc.ValidatorThreshold)
		if !ok {
			return cfg, "", nil, fmt.Errorf("bad validatorThreshold %q", fc.ValidatorThreshold)
		}
		threshold = th
	} else {
		validators = consensus.NewValidatorSet(vals)
	}

	maxMempool := fc.MaxMempool
	if maxMempool <= 0 {
		maxMempool = 10000 // sane default bound
	}
	maxComment := fc.MaxCommentLength
	if maxComment <= 0 {
		maxComment = 280 // sane default bound
	}
	maxMessage := fc.MaxMessageBytes
	if maxMessage <= 0 {
		maxMessage = 2000 // sane default: one serialized payment/update, everything included
	} else if maxMessage < ledger.MinMessageBytes {
		// Governance can never vote the cap below MinMessageBytes (LedgerParameters.Acceptable), which
		// would brick payments — the same floor must hold for the genesis value, or an operator could
		// start a network already bricked (1..511 previously passed straight through).
		maxMessage = ledger.MinMessageBytes
	}

	// The assembled genesis parameters must clear the same well-formedness bar (Acceptable) that
	// every post-genesis governance change is held to — mirroring exactly what node.New commits into
	// the genesis state. Without this, a malformed genesis (negative interval, fan-out the timeout
	// cannot cover) starts a network whose recorded state governance could never have produced.
	participants := len(fc.Validators)
	if participants == 0 {
		participants = 1 // dynamic mode with an empty peer universe: a single-node genesis
	}
	genesisParameters := ledger.LedgerParameters{
		TransactionCost: cost,
		BlockIntervalMs: int64(fc.MinBlockIntervalMs),
		StepTimeoutMs:   timeout.Milliseconds(),
		MaxPeers:        fc.MaxPeers,
		MaxMessageBytes: maxMessage,
	}
	if !genesisParameters.Acceptable(participants) {
		return cfg, "", nil, fmt.Errorf("genesis parameters malformed (governance could never enact them): "+
			"minBlockIntervalMs must be >= 0, maxPeers >= 0, and timeoutMs must cover the gossip fan-out "+
			"(>= %dms per relay hop for %d validators with maxPeers=%d)",
			ledger.AssumedGossipHopMs, participants, fc.MaxPeers)
	}

	cfg = node.Config{
		NetworkID:          fc.NetworkID,
		Scheme:             signing.Ed25519{},
		Signer:             signing.NewSigner(signing.Ed25519{}, fc.NetworkID, pub, priv),
		Validators:         validators,
		ValidatorThreshold: threshold,
		TransactionCost:    cost,
		Timeout:            timeout,
		MinBlockInterval:   time.Duration(fc.MinBlockIntervalMs) * time.Millisecond,
		Genesis:            genesis,
		Bootstrap:          fc.Bootstrap,
		SnapshotPath:       fc.SnapshotPath,
		BackupPath:         fc.BackupPath,
		MaxMempool:         maxMempool,
		MaxCommentLength:   maxComment,
		MaxMessageBytes:    maxMessage,
		MaxPeers:           fc.MaxPeers,
	}
	listen = fc.Listen
	if listen == "" {
		listen = ":8080"
	}

	// Gossip peers: all-to-all (the `peers` list) by default; if maxPeers is set and every validator
	// carries a URL, use the auto-scaled Hypernode topology and gossip only to this node's neighbours.
	peers = fc.Peers
	if fc.MaxPeers > 0 {
		keys := make([]string, 0, len(fc.Validators))
		urls := make(map[string]string, len(fc.Validators))
		haveAll := true
		for _, v := range fc.Validators {
			keys = append(keys, v.PublicKey)
			if v.URL == "" {
				haveAll = false
			} else {
				urls[v.PublicKey] = v.URL
			}
		}
		if haveAll {
			sort.Strings(keys)
			var np []string
			for _, k := range node.Neighbors(keys, fc.PublicKey, fc.MaxPeers) {
				np = append(np, urls[k])
			}
			peers = np
		}
	}
	return cfg, listen, peers, nil
}
