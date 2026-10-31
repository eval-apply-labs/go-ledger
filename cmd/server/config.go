package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// fileConfig is the on-disk node configuration (JSON, spec 8.2). What every node must agree on, the
// network's name, its parameters, its validators and its accounts, is in the genesis file it names.
type fileConfig struct {
	GenesisPath      string   `json:"genesisPath"` // the genesis file (spec 2), the same on every node
	Listen           string   `json:"listen"`
	Name             string   `json:"name"` // this node's account name, which its votes carry; "" for an unnamed observer
	PublicKey        string   `json:"publicKey"`
	PrivateKey       string   `json:"privateKey"`
	Peers            []string `json:"peers"`
	Bootstrap        []string `json:"bootstrap"`
	SnapshotPath     string   `json:"snapshotPath"`
	BackupPath       string   `json:"backupPath"`
	MaxMempool       int      `json:"maxMempool"`       // cap on each pool of pending payments, updates and name requests (0 → a sane default)
	MaxCommentLength int      `json:"maxCommentLength"` // cap on payment comment bytes (0 → a sane default)
}

// keySelfTest is the domain of the one signature load makes to prove the key pair works. That
// signature is never sent, and no protocol statement is signed in this domain.
const keySelfTest signing.Domain = "key-self-test"

// load parses a node config file, and the genesis file it names, into a node.Config plus the listen
// address and the configured peer URLs. On a network of two or more validators, a validator's node
// replaces those peers with the validators' addresses once every validator has one (node.Open, which
// calls resolvePeersFromState); any other node keeps them.
func load(path string) (nodeConfig node.Config, listen string, peers []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nodeConfig, "", nil, err
	}
	// An unknown key is refused, so a misspelt field, or one that now lives in the genesis file, is
	// never silently ignored.
	var configFile fileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configFile); err != nil {
		return nodeConfig, "", nil, fmt.Errorf("parse config: %w", err)
	}

	publicKey, err := base64.StdEncoding.DecodeString(configFile.PublicKey)
	if err != nil {
		return nodeConfig, "", nil, fmt.Errorf("publicKey: %w", err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(configFile.PrivateKey)
	if err != nil {
		return nodeConfig, "", nil, fmt.Errorf("privateKey: %w", err)
	}
	// The key check below only gives a clearer message. Without it, the comparison further down would
	// still refuse any publicKey other than the one the seed derives, a wrong-length or non-canonical one
	// included, with the less precise message that the pair does not belong together. A seed of the
	// wrong length is NewSigner's to refuse, below: signing alone knows the seed's size.
	if !signing.ValidPublicKeyBase64(configFile.PublicKey) {
		return nodeConfig, "", nil, fmt.Errorf("publicKey: not an ML-DSA-65 key in canonical base64, as keygen writes it (%d characters)", signing.PublicKeyBase64Length)
	}
	if configFile.Name != "" && !ledger.CanonicalName(configFile.Name) {
		return nodeConfig, "", nil, fmt.Errorf("name %q is not a name: 1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or a digit", configFile.Name)
	}

	// The genesis: the network's name, its parameters, its validator rule and its accounts. Its hash
	// makes the effective network id that every signature binds (spec 2.3).
	genesisData, err := os.ReadFile(configFile.GenesisPath)
	if err != nil {
		return nodeConfig, "", nil, fmt.Errorf("genesisPath: %w", err)
	}
	genesis, err := ledger.ParseGenesis(genesisData)
	if err != nil {
		return nodeConfig, "", nil, err
	}
	chain, genesisState, err := ledger.ValidateGenesis(genesis)
	if err != nil {
		return nodeConfig, "", nil, err
	}
	staticValidators := chain.ValidatorRule.StaticValidators

	// A node that can validate keeps the votes it signs in a log next to its snapshot, so a restart in
	// the middle of a height cannot make it sign against itself. In dynamic mode any name can become a
	// validator.
	isStaticValidator := slices.ContainsFunc(staticValidators, func(validator ledger.StaticValidator) bool { return validator.Name == configFile.Name })
	if configFile.SnapshotPath == "" && (chain.ValidatorRule.Threshold != nil || isStaticValidator) {
		return nodeConfig, "", nil, fmt.Errorf("snapshotPath is required on a node that can be a validator: its sign log is kept next to the snapshot")
	}

	maxMempool := configFile.MaxMempool
	if maxMempool <= 0 {
		maxMempool = 10000 // sane default bound
	}
	maxComment := configFile.MaxCommentLength
	if maxComment <= 0 {
		maxComment = 280 // sane default bound
	}

	// The pair must belong together: the signer signs as the key its seed derives, so a publicKey from
	// another keygen run would name a key this node never signs for. The node would serve HTTP but never
	// sign: a silent non-participant.
	// Derive and compare, then prove it with one signature.
	signer, err := signing.NewSigner(chain.Scheme, chain.NetworkID, privateKey)
	if err != nil {
		return nodeConfig, "", nil, fmt.Errorf("privateKey: not an ML-DSA-65 seed, as keygen writes it: %w", err)
	}
	if signer.PublicKeyBase64() != configFile.PublicKey {
		return nodeConfig, "", nil, fmt.Errorf("privateKey does not belong to publicKey (are they from the same keygen run?)")
	}
	if signature, err := signer.Sign(keySelfTest); err != nil || !signing.Verify(chain.Scheme, chain.NetworkID, publicKey, keySelfTest, signature) {
		return nodeConfig, "", nil, fmt.Errorf("privateKey self-test failed: its signature does not verify under publicKey")
	}

	nodeConfig = node.Config{
		Chain:            chain,
		Signer:           signer,
		Name:             configFile.Name,
		Genesis:          genesisState,
		Bootstrap:        configFile.Bootstrap,
		SnapshotPath:     configFile.SnapshotPath,
		BackupPath:       configFile.BackupPath,
		MaxMempool:       maxMempool,
		MaxCommentLength: maxComment,
	}
	listen = configFile.Listen
	if listen == "" {
		listen = ":8080"
	}
	return nodeConfig, listen, configFile.Peers, nil
}
