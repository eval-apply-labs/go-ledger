package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// fileConfig is the on-disk node configuration (JSON, spec 8.2). What every node must agree on, the
// network's name, its parameters, its validators and its accounts, is in the genesis file it names.
type fileConfig struct {
	GenesisPath      string   `json:"genesisPath"`  // the genesis file (spec 2), the same on every node
	Listen           string   `json:"listen"`       // the operator port: every route (spec §3); "" is :8080, or 127.0.0.1:8080 with gossipListen set
	GossipListen     string   `json:"gossipListen"` // the gossip port, behind the allow list; "" serves the gossip routes on listen, open to anyone
	Allow            []string `json:"allow"`        // IP addresses or prefixes the gossip port always lets in, besides the validators' (spec §5.1)
	Name             string   `json:"name"`         // this node's account name, which its votes carry; "" for an unnamed observer
	PublicKey        string   `json:"publicKey"`
	PrivateKey       string   `json:"privateKey"`
	Peers            []string `json:"peers"`
	Bootstrap        []string `json:"bootstrap"`
	SnapshotPath     string   `json:"snapshotPath"`
	BackupPath       string   `json:"backupPath"`
	PublicPath       string   `json:"publicPath"`       // the public folder the fetcher serves (spec §6.1); "" writes none
	MaxMempool       int      `json:"maxMempool"`       // cap on each pool of pending payments, updates and name requests (0 → a sane default)
	MaxCommentLength int      `json:"maxCommentLength"` // cap on payment comment bytes (0 → a sane default)
	// MinimumStepTimeoutMs is the slow-down switch (spec §7.6): the least the round timers wait per step,
	// from 0 (off) to ledger.MaxTimingMs, a day. Read at start: a restart is how it changes.
	MinimumStepTimeoutMs int64 `json:"minimumStepTimeoutMs"`
}

// keySelfTest is the domain of the one signature load makes to prove the key pair works. That
// signature is never sent, and no protocol statement is signed in this domain.
const keySelfTest signing.Domain = "key-self-test"

// load parses a node config file, and the genesis file it names, into a node.Config plus the listen
// address, the gossip listen address ("" for none) and the configured peer URLs. On a network of two or
// more validators, a validator's node replaces those peers with the validators' IP addresses on the
// gossip port once every validator has one (node.Open, which calls resolvePeersFromState); any other
// node keeps them.
func load(path string) (nodeConfig node.Config, listen, gossipListen string, peers []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nodeConfig, "", "", nil, err
	}
	// An unknown key is refused, so a misspelt field, or one that now lives in the genesis file, is
	// never silently ignored.
	var configFile fileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configFile); err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("parse config: %w", err)
	}

	publicKey, err := base64.StdEncoding.DecodeString(configFile.PublicKey)
	if err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("publicKey: %w", err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(configFile.PrivateKey)
	if err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("privateKey: %w", err)
	}
	// The key check below only gives a clearer message, with the scheme's reason. Without it, the
	// comparison further down would still refuse any publicKey other than the one the private key
	// derives, a wrong-length or non-canonical one included, with the less precise message that the pair
	// does not belong together. A private key of the wrong form is NewSigner's to refuse, below: only the
	// scheme knows its form.
	if err := signing.ValidatePublicKeyBase64(signing.NetworkScheme(), configFile.PublicKey); err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("publicKey: not a key of the network's scheme, %s, as keygen writes it: %w",
			signing.NetworkScheme().Name(), err)
	}
	if configFile.Name != "" && !ledger.CanonicalName(configFile.Name) {
		return nodeConfig, "", "", nil, fmt.Errorf("name %q is not a name: 1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or a digit", configFile.Name)
	}
	// The allow list guards only the gossip port. Without one, every route is served on listen, open to
	// anyone, so an allow entry would hold nothing back: it is refused, not ignored.
	if len(configFile.Allow) > 0 && configFile.GossipListen == "" {
		return nodeConfig, "", "", nil, fmt.Errorf("allow is set but gossipListen is not: the allow list guards only the gossip port, and without one listen serves every route to anyone")
	}
	gossipAllow, err := allowPrefixes(configFile.Allow)
	if err != nil {
		return nodeConfig, "", "", nil, err
	}
	if configFile.MinimumStepTimeoutMs < 0 || configFile.MinimumStepTimeoutMs > ledger.MaxTimingMs {
		return nodeConfig, "", "", nil, fmt.Errorf("minimumStepTimeoutMs %d is out of range: from 0 (off) to %d, a day", configFile.MinimumStepTimeoutMs, ledger.MaxTimingMs)
	}

	// The genesis: the network's name, its parameters, its validator rule and its accounts. Its hash
	// makes the effective network id that every signature binds (spec 2.3).
	genesisData, err := os.ReadFile(configFile.GenesisPath)
	if err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("genesisPath: %w", err)
	}
	genesis, err := ledger.ParseGenesis(genesisData)
	if err != nil {
		return nodeConfig, "", "", nil, err
	}
	chain, genesisState, err := ledger.ValidateGenesis(genesis)
	if err != nil {
		return nodeConfig, "", "", nil, err
	}
	staticValidators := chain.ValidatorRule.StaticValidators

	// A node that can validate keeps the votes it signs in a log next to its snapshot, so a restart in
	// the middle of a height cannot make it sign against itself. In dynamic mode any name can become a
	// validator.
	isStaticValidator := slices.ContainsFunc(staticValidators, func(validator ledger.StaticValidator) bool { return validator.Name == configFile.Name })
	if configFile.SnapshotPath == "" && (chain.ValidatorRule.Threshold != nil || isStaticValidator) {
		return nodeConfig, "", "", nil, fmt.Errorf("snapshotPath is required on a node that can be a validator: its sign log is kept next to the snapshot")
	}

	maxMempool := configFile.MaxMempool
	if maxMempool <= 0 {
		maxMempool = 10000 // sane default bound
	}
	maxComment := configFile.MaxCommentLength
	if maxComment <= 0 {
		maxComment = 280 // sane default bound
	}

	// The pair must belong together: the signer signs as the key its private key derives, so a
	// publicKey from another keygen run would name a key this node never signs for. The node would
	// serve HTTP but never sign: a silent non-participant.
	// Derive and compare, then prove it with one signature.
	signer, err := signing.NewSigner(chain.Scheme, chain.NetworkID, privateKey)
	if err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("privateKey: not a private key of the network's scheme, %s, as keygen writes it: %w", chain.Scheme.Name(), err)
	}
	if signer.PublicKeyBase64() != configFile.PublicKey {
		return nodeConfig, "", "", nil, fmt.Errorf("privateKey does not belong to publicKey (are they from the same keygen run?)")
	}
	if signature, err := signer.Sign(keySelfTest); err != nil || !signing.Verify(chain.Scheme, chain.NetworkID, publicKey, keySelfTest, signature) {
		return nodeConfig, "", "", nil, fmt.Errorf("privateKey self-test failed: its signature does not verify under publicKey")
	}

	nodeConfig = node.Config{
		Chain:              chain,
		Signer:             signer,
		Name:               configFile.Name,
		Genesis:            genesisState,
		Bootstrap:          configFile.Bootstrap,
		SnapshotPath:       configFile.SnapshotPath,
		BackupPath:         configFile.BackupPath,
		MaxMempool:         maxMempool,
		MaxCommentLength:   maxComment,
		GossipAllow:        gossipAllow,
		PublicPath:         configFile.PublicPath,
		MinimumStepTimeout: time.Duration(configFile.MinimumStepTimeoutMs) * time.Millisecond,
	}
	listen = configFile.Listen
	if listen == "" {
		listen = ":8080"
		if configFile.GossipListen != "" {
			// The operator port serves every route, bundle and checkpoint imports among them, with no
			// allow list: with a gossip port beside it, it is the bank's own, and listens on this machine
			// only unless the config says otherwise.
			listen = "127.0.0.1:8080"
		}
	}
	return nodeConfig, listen, configFile.GossipListen, configFile.Peers, nil
}

// allowPrefixes reads the config's allow entries (spec §5.1): each an IP address, which stands for
// itself (a /32 or a /128), or a prefix. Each must be written as the gossip port compares it: an IPv4
// address as IPv4, never in IPv6 dress, no zone, and no bit set past a prefix's length (198.51.100.7/24
// could mean the address or the network). Anything else is refused, so a mistyped entry never shuts a
// bank out, or lets a stranger in, unnoticed.
func allowPrefixes(entries []string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, entry := range entries {
		address, err := netip.ParseAddr(entry)
		prefix := netip.PrefixFrom(address, address.BitLen())
		if strings.Contains(entry, "/") {
			prefix, err = netip.ParsePrefix(entry)
			address = prefix.Addr()
		}
		if err != nil || address.Zone() != "" || address.Is4In6() || prefix != prefix.Masked() {
			return nil, fmt.Errorf("allow: %q is not an IP address or prefix as the gossip port compares it: write one as 203.0.113.7, "+
				"198.51.100.0/24 or 2001:db8::/48, an IPv4 address as IPv4, with no zone, and no bit set past a prefix's length", entry)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}
