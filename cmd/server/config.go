package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"regexp"
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
	GenesisPath      string        `json:"genesisPath"`  // the genesis file (spec 2), the same on every node
	Listen           string        `json:"listen"`       // the operator port: every route (spec §3); "" is :8080, or 127.0.0.1:8080 with gossipListen set
	GossipListen     string        `json:"gossipListen"` // the gossip port, behind the allow list; "" serves the gossip routes on listen, open to anyone
	Allow            []string      `json:"allow"`        // IP addresses or prefixes the gossip port always lets in, besides the validators' (spec §5.1)
	Name             string        `json:"name"`         // this node's account name, which its votes carry; "" for an unnamed observer
	PublicKey        string        `json:"publicKey"`    // the key the node's signatures verify under
	PrivateKey       string        `json:"privateKey"`   // the key in this process, as keygen prints it; or signer, not both
	Signer           *signerConfig `json:"signer"`       // the signer program that holds the key (spec §2.7); or privateKey, not both
	Peers            []string      `json:"peers"`
	Bootstrap        []string      `json:"bootstrap"`
	SnapshotPath     string        `json:"snapshotPath"`
	BackupPath       string        `json:"backupPath"`
	PublicPath       string        `json:"publicPath"`       // the public folder the fetcher serves (spec §6.1); "" writes none
	MaxMempool       int           `json:"maxMempool"`       // cap on each pool of pending payments, updates and name requests (0 → a sane default)
	MaxCommentLength int           `json:"maxCommentLength"` // cap on payment comment bytes (0 → a sane default)
	// MinimumStepTimeoutMs is the slow-down switch (spec §7.6): the least the round timers wait per step,
	// from 0 (off) to ledger.MaxTimingMs, a day. Read at start: a restart is how it changes.
	MinimumStepTimeoutMs int64 `json:"minimumStepTimeoutMs"`
	// The name service's zone (spec §3.2): with zone set, the node writes the zone file hdls/zone into
	// publicPath. See zoneSettings.
	Zone            string   `json:"zone"`            // the zone's domain name, such as hdls.example.org; "" writes no zone file
	ZoneNameServers []string `json:"zoneNameServers"` // the zone's name servers, its NS records, such as ns1.example.org
	ZoneContact     string   `json:"zoneContact"`     // the SOA mailbox as a domain name: hostmaster.example.org for hostmaster@example.org
	ZoneTimeToLive  int      `json:"zoneTimeToLive"`  // how long, in seconds, a resolver may keep a record (0 → 60)
}

// signerConfig is the config's signer object (spec §2.7): where the signer program listens, on the
// bank's private network, and the client the engine asks it as.
type signerConfig struct {
	URL        string `json:"url"`        // such as http://signer:7000
	Client     string `json:"client"`     // the client name the signer's config gives the engine, such as engine
	SecretFile string `json:"secretFile"` // that client's secret: 64 lowercase hex digits, as keygen -secret prints them
}

// keySelfTest is the domain of the one signature load makes to prove the key pair works. Its one field
// is the node's name, which the signer program signs only if it is its bank's. That signature is never
// sent, and no protocol statement is signed in this domain.
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
	// The key check below only gives a clearer message, with the scheme's reason. Without it, the
	// comparison in keySigner, or the self-test, would still refuse any publicKey other than the one the
	// node signs as, a wrong-length or non-canonical one included, with the less precise message that the
	// pair does not belong together. A private key of the wrong form is NewSigner's to refuse, in
	// keySigner: only the scheme knows its form.
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
	zoneSettings, err := zoneSettingsOf(configFile)
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

	// One signature proves the key works: the node signs its name as publicKey, for this network. Through
	// the signer program, it proves too that the signer answers, holds that key and signs for that name: a
	// signer holding another key, or none, or another bank's name, stops the engine here, before it joins
	// (spec §2.7), not at its first vote.
	signer, err := keySigner(configFile, chain, publicKey)
	if err != nil {
		return nodeConfig, "", "", nil, err
	}
	signature, err := signer.Sign(keySelfTest, []byte(configFile.Name))
	if err != nil {
		return nodeConfig, "", "", nil, fmt.Errorf("key self-test: %w", err)
	}
	if !signing.Verify(chain.Scheme, chain.NetworkID, publicKey, keySelfTest, signature, []byte(configFile.Name)) {
		return nodeConfig, "", "", nil, errors.New("key self-test: its signature does not verify under publicKey")
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
		ZoneSettings:       zoneSettings,
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

// keySigner is the signer of the config's key, which it takes from exactly one of two fields (spec
// §2.7). With privateKey, it is the key in this process, which must be the private key of publicKey.
// With signer, it is the signer program, which the engine asks as its client, under the client's
// secret, and whose every answer must verify under publicKey.
func keySigner(configFile fileConfig, chain ledger.Chain, publicKey []byte) (signing.Signer, error) {
	switch {
	case configFile.PrivateKey != "" && configFile.Signer != nil:
		return nil, errors.New("privateKey and signer are both set: set privateKey for a key in this process, or signer for the signer program that holds it, not both")
	case configFile.PrivateKey == "" && configFile.Signer == nil:
		return nil, errors.New("neither privateKey nor signer is set: set privateKey for a key in this process, or signer for the signer program that holds it")
	case configFile.Signer != nil:
		remote := configFile.Signer
		if remote.URL == "" || remote.Client == "" || remote.SecretFile == "" {
			return nil, errors.New("signer needs url, client and secretFile: where the signer listens, the client name its config gives the engine, and that client's secret file")
		}
		secret, err := signing.ReadSecretFile(remote.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("signer.secretFile: %w", err)
		}
		return signing.NewRemoteSigner(remote.URL, remote.Client, secret, publicKey, chain.NetworkID, chain.Scheme), nil
	}
	// The pair must belong together: the node signs as the key its private key derives, so a publicKey
	// from another keygen run would name a key this node never signs for. The node would serve HTTP but
	// never sign: a silent non-participant.
	privateKey, err := base64.StdEncoding.DecodeString(configFile.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("privateKey: %w", err)
	}
	localSigner, err := signing.NewSigner(chain.Scheme, chain.NetworkID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("privateKey: not a private key of the network's scheme, %s, as keygen writes it: %w", chain.Scheme.Name(), err)
	}
	if localSigner.PublicKeyBase64() != configFile.PublicKey {
		return nil, errors.New("privateKey does not belong to publicKey (are they from the same keygen run?)")
	}
	return localSigner, nil
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

// zoneSettingsOf reads the config's zone settings (spec §3.2). With a zone: the zone, each name server
// and the contact are domain names, the zone of at most maxZoneLength characters; there is at least one
// name server, and none inside the zone, where its address records would be those of a ledger name; the
// public folder the zone file goes into is set; and the time to live is from 0 (the default, 60) to
// 2³¹ - 1 seconds, the most DNS allows. Without one, no other zone setting is given: one would hold
// nothing, so it is refused, not ignored.
func zoneSettingsOf(configFile fileConfig) (node.ZoneSettings, error) {
	settings := node.ZoneSettings{Zone: configFile.Zone, ZoneNameServers: configFile.ZoneNameServers,
		ZoneContact: configFile.ZoneContact, ZoneTimeToLive: configFile.ZoneTimeToLive}
	if settings.Zone == "" {
		if len(settings.ZoneNameServers) > 0 || settings.ZoneContact != "" || settings.ZoneTimeToLive != 0 {
			return settings, errors.New("zoneNameServers, zoneContact or zoneTimeToLive is set but zone is not: they are the zone's settings, and without a zone the node writes no zone file")
		}
		return settings, nil
	}
	if configFile.PublicPath == "" {
		return settings, errors.New("zone is set but publicPath is not: the zone file is written into the public folder")
	}
	if !domainName(settings.Zone) {
		return settings, fmt.Errorf("zone %q is not a domain name: %s", settings.Zone, domainNameRule)
	}
	if len(settings.Zone) > maxZoneLength {
		return settings, fmt.Errorf("zone %q is %d characters, more than %d: the zone file's longest name, "+
			"_65535._tcp.<a name of 32 characters>.<zone>, would pass the 253 characters of a domain name", settings.Zone, len(settings.Zone), maxZoneLength)
	}
	if len(settings.ZoneNameServers) == 0 {
		return settings, errors.New("zone is set but zoneNameServers is not: the zone needs at least one name server, such as ns1.example.org")
	}
	for _, nameServer := range settings.ZoneNameServers {
		if !domainName(nameServer) {
			return settings, fmt.Errorf("zoneNameServers: %q is not a domain name: %s", nameServer, domainNameRule)
		}
		if nameServer == settings.Zone || strings.HasSuffix(nameServer, "."+settings.Zone) {
			return settings, fmt.Errorf("zoneNameServers: %q is inside the zone, whose names are the ledger's, so its address would be "+
				"whatever the customer holding that name lists, or none: use a name server outside the zone", nameServer)
		}
	}
	if !domainName(settings.ZoneContact) {
		return settings, fmt.Errorf("zoneContact %q is not the zone's contact as a domain name, hostmaster.example.org for hostmaster@example.org: %s",
			settings.ZoneContact, domainNameRule)
	}
	if settings.ZoneTimeToLive < 0 || settings.ZoneTimeToLive > math.MaxInt32 {
		return settings, fmt.Errorf("zoneTimeToLive %d is out of range: from 0 (60, the default) to %d seconds", settings.ZoneTimeToLive, math.MaxInt32)
	}
	return settings, nil
}

// maxZoneLength is the most characters a zone's name may have, so that every name the zone file writes
// is within the 253 characters of a domain name: the longest adds 45 to the zone's,
// _65535._tcp.<a name of 32 characters>.<zone>, and the _ip lookup's add 37.
const maxZoneLength = 253 - 45

// domainNamePattern is a domain name as the zone file writes it: labels of 1 to 63 lowercase letters,
// digits and hyphens, not starting or ending with a hyphen, joined by dots, without the final dot.
var domainNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// domainNameRule is the form domainName takes, as a refusal names it.
const domainNameRule = "labels of lowercase letters, digits and hyphens joined by dots, at most 253 characters, without the final dot, such as hdls.example.org"

// domainName reports whether text is a domain name as the zone file writes it (domainNamePattern), of
// at most 253 characters.
func domainName(text string) bool { return len(text) <= 253 && domainNamePattern.MatchString(text) }
