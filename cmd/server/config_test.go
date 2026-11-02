package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// baseConfig is a minimal well-formed node: a one-validator testnet whose validator, the node itself,
// is named "founder". It returns the node config and the genesis file it names, as maps; tests change
// single fields of either to probe load()'s validation, and writeConfig writes both.
func baseConfig(t *testing.T) (config, genesis map[string]any) {
	t.Helper()
	publicKey, privateKey, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKey)
	config = map[string]any{
		"listen":       "127.0.0.1:0",
		"name":         "founder",
		"publicKey":    publicKeyBase64,
		"privateKey":   base64.StdEncoding.EncodeToString(privateKey),
		"snapshotPath": filepath.Join(t.TempDir(), "snapshot.json"),
	}
	genesis = map[string]any{
		"networkName": "testnet",
		"gossipPort":  8090,
		"parameters":  defaultParameters(),
		"validators":  []map[string]any{{"name": "founder", "power": 3}},
		"accounts": []map[string]any{
			{"name": "founder", "publicKey": publicKeyBase64, "amount": "1000", "delegate": "founder"},
		},
	}
	return config, genesis
}

// defaultParameters is the genesis file's parameters object at the defaults (spec 2.4).
func defaultParameters() map[string]any {
	return map[string]any{
		"transactionCost": "0.1", "blockIntervalMs": 1000, "stepTimeoutMs": 3000, "maxPeers": 0,
		"maxMessageBytes": ledger.DefaultParameters().MaxMessageBytes, "newKeyFee": "1", "changeDelayBlocks": 86400,
		"retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000",
	}
}

// freshKey is a new public key of the network's scheme, in base64.
func freshKey(t *testing.T) string {
	t.Helper()
	publicKey, _, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(publicKey)
}

// writeJSON writes value as JSON to a new file in a temporary directory, and returns its path.
func writeJSON(t *testing.T, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeConfig writes the genesis file and the node config that names it, and returns the config's path.
func writeConfig(t *testing.T, config, genesis map[string]any) string {
	t.Helper()
	config["genesisPath"] = writeJSON(t, "genesis.json", genesis)
	return writeJSON(t, "node.json", config)
}

// expectLoadError asserts load() fails and that the error mentions the offending field, so an
// operator can tell WHAT to fix from the message alone.
func expectLoadError(t *testing.T, config, genesis map[string]any, wantSubstring string) {
	t.Helper()
	_, _, _, _, err := load(writeConfig(t, config, genesis))
	if err == nil {
		t.Fatalf("load accepted a config that must be refused (want error mentioning %q)", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("error %q does not mention %q", err, wantSubstring)
	}
}

func TestLoadValidConfig(t *testing.T) {
	config, genesis := baseConfig(t)
	nodeConfig, listen, _, _, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	if listen != "127.0.0.1:0" {
		t.Errorf("listen = %q", listen)
	}
	if rule := nodeConfig.Chain.ValidatorRule; len(rule.StaticValidators) != 1 || rule.StaticValidators[0] != (ledger.StaticValidator{Name: "founder", Power: 3}) {
		t.Errorf("fixed validator set not loaded: %+v", rule)
	}
	if nodeConfig.Name != "founder" {
		t.Errorf("the node's name not loaded: %q", nodeConfig.Name)
	}
	// The effective network id is the name and the genesis hash (spec 2.3), and the node signs with it.
	genesisHash := ledger.GenesisHash("testnet", 8090, nodeConfig.Genesis, nodeConfig.Chain.ValidatorRule)
	if chain := nodeConfig.Chain; chain.GenesisHash != genesisHash || chain.NetworkID != ledger.NetworkID("testnet", genesisHash) || chain.GossipPort != 8090 {
		t.Errorf("the chain's genesis hash, network id or gossip port (%d) is not the genesis's: %q", chain.GossipPort, chain.NetworkID)
	}
	signature, err := nodeConfig.Signer.Sign(signing.DomainReceipt)
	if err != nil || !signing.Verify(signing.NetworkScheme(), nodeConfig.Chain.NetworkID, nodeConfig.Signer.PublicKey(), signing.DomainReceipt, signature) {
		t.Error("the node does not sign for the effective network id")
	}
	if nodeConfig.MaxMempool != 10000 || nodeConfig.MaxCommentLength != 280 {
		t.Errorf("maxMempool %d and maxCommentLength %d, want the defaults 10000 and 280", nodeConfig.MaxMempool, nodeConfig.MaxCommentLength)
	}
}

// What every node must agree on, the network's name, its parameters, its validators and its accounts,
// comes from the genesis file the config names (spec 8.2), checked by ledger.ValidateGenesis: the node
// starts from the state that file defines, under the network id its hash makes. The node config keeps
// only what is this node's own; a field that moved to the genesis file is refused, not ignored, so an
// operator never believes a setting holds that does not.
func TestLoadReadsTheGenesisFile(t *testing.T) {
	config, genesis := baseConfig(t)
	parameters := defaultParameters()
	parameters["changeDelayBlocks"], parameters["transactionCost"] = 20, "0.25"
	genesis["parameters"] = parameters
	genesis["accounts"] = append(genesis["accounts"].([]map[string]any),
		map[string]any{"name": "cafe", "publicKey": freshKey(t), "amount": "1", "bank": "founder", "publicAddress": "http://cafe:8080", "ipAddresses": []string{"10.0.0.9"}},
		map[string]any{"name": "bakery", "publicKey": freshKey(t), "amount": "2.5", "bank": "founder", "description": "the bakery"})
	nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, account := range nodeConfig.Genesis.Accounts {
		names = append(names, account.Name)
	}
	if strings.Join(names, ",") != "bakery,cafe,founder" {
		t.Fatalf("genesis accounts %v, want bakery,cafe,founder in name order", names)
	}
	bakery, cafe := nodeConfig.Genesis.Accounts[0], nodeConfig.Genesis.Accounts[1]
	if bakery.Bank != "founder" || ledger.FormatAmount(bakery.Amount) != "2.5" || bakery.Description != "the bakery" || cafe.PublicAddress != "http://cafe:8080" ||
		!slices.Equal(cafe.IPAddresses, []string{"10.0.0.9"}) {
		t.Errorf("the accounts were not read as the file gives them: %+v, %+v", bakery, cafe)
	}
	if parameters := nodeConfig.Genesis.Parameters; parameters.ChangeDelayBlocks != 20 || ledger.FormatAmount(parameters.TransactionCost) != "0.25" || parameters.MaxMessageBytes != ledger.DefaultParameters().MaxMessageBytes {
		t.Errorf("the parameters were not read from the file: %+v", parameters)
	}
	if nodeConfig.Genesis.Height != 0 || nodeConfig.Genesis.GovernanceSequence != 0 {
		t.Errorf("the genesis state is at height %d, sequence %d; want 0 and 0", nodeConfig.Genesis.Height, nodeConfig.Genesis.GovernanceSequence)
	}
	genesisHash := ledger.GenesisHash("testnet", 8090, nodeConfig.Genesis, nodeConfig.Chain.ValidatorRule)
	if nodeConfig.Chain.NetworkID != ledger.NetworkID("testnet", genesisHash) {
		t.Errorf("the network id %q is not made from the genesis file", nodeConfig.Chain.NetworkID)
	}

	// The genesis file's own checks hold at load.
	genesis["accounts"].([]map[string]any)[1]["bank"] = "bakery" // not seated
	expectLoadError(t, config, genesis, `the bank "bakery" of "cafe" is not a name the genesis seats`)

	// Fields that moved to the genesis file are refused in the node config.
	for field, value := range map[string]any{
		"networkName": "testnet", "transactionCost": "0.1", "timeoutMs": 3000, "minBlockIntervalMs": 1000, "maxPeers": 2,
		"maxMessageBytes": ledger.DefaultParameters().MaxMessageBytes, "validatorThreshold": "100", "validators": []any{}, "genesis": []any{}, "newKeyFee": "1",
		"gossipPort": 8070,
	} {
		config, genesis := baseConfig(t)
		config[field] = value
		expectLoadError(t, config, genesis, fmt.Sprintf("unknown field %q", field))
	}

	config, _ = baseConfig(t)
	config["genesisPath"] = filepath.Join(t.TempDir(), "missing.json")
	if _, _, _, _, err := load(writeJSON(t, "node.json", config)); err == nil || !strings.Contains(err.Error(), "genesisPath") {
		t.Errorf("a missing genesis file: error %v, want one naming genesisPath", err)
	}
}

// The gossip port's fields (spec §9): gossipListen, and allow, IP addresses or prefixes, each read into
// the prefix the gossip port compares, a single address as a /32 or a /128. An entry the port could not
// compare as written is refused at load, by name. So is allow without gossipListen: with no gossip
// port, listen serves every route to anyone, and the entries would hold nothing back.
func TestLoadReadsTheGossipPortAndItsAllowList(t *testing.T) {
	config, genesis := baseConfig(t)
	config["gossipListen"], config["allow"] = ":8070", []string{"203.0.113.7", "198.51.100.0/24", "2001:db8::/48", "2001:db8:1::1"}
	nodeConfig, listen, gossipListen, _, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.7/32"), netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("2001:db8::/48"), netip.MustParsePrefix("2001:db8:1::1/128")}
	if listen != "127.0.0.1:0" || gossipListen != ":8070" || !slices.Equal(nodeConfig.GossipAllow, want) {
		t.Errorf("listen %q, gossipListen %q and allow %v, want 127.0.0.1:0, :8070 and %v", listen, gossipListen, nodeConfig.GossipAllow, want)
	}

	for _, entry := range []string{"203.0.113.300", "bank-a.example", "", "::ffff:203.0.113.7", "::ffff:198.51.100.0/120", "fe80::1%eth0",
		"198.51.100.7/24", "198.51.100.0/33"} {
		config, genesis := baseConfig(t)
		config["gossipListen"], config["allow"] = ":8070", []string{"203.0.113.7", entry}
		expectLoadError(t, config, genesis, fmt.Sprintf("allow: %q is not an IP address or prefix", entry))
	}

	config, genesis = baseConfig(t)
	config["allow"] = []string{"203.0.113.7"}
	expectLoadError(t, config, genesis, "allow is set but gossipListen is not")

	config, genesis = baseConfig(t) // as before the two ports: one port, open to anyone
	if nodeConfig, _, gossipListen, _, err := load(writeConfig(t, config, genesis)); err != nil || gossipListen != "" || nodeConfig.GossipAllow != nil {
		t.Errorf("a config without gossipListen: gossipListen %q, allow %v, error %v; want neither", gossipListen, nodeConfig.GossipAllow, err)
	}
}

// Without listen, the operator port, which serves every route with no allow list, listens on this
// machine only, 127.0.0.1:8080, when gossipListen is set (review N6). Without gossipListen either, the
// one port serves every route to anyone, as a one-machine trial needs, on :8080.
func TestLoadDefaultsListenToThisMachineBesideAGossipPort(t *testing.T) {
	for gossipListen, wantListen := range map[string]string{":8070": "127.0.0.1:8080", "": ":8080"} {
		config, genesis := baseConfig(t)
		delete(config, "listen")
		if gossipListen != "" {
			config["gossipListen"] = gossipListen
		}
		if _, listen, _, _, err := load(writeConfig(t, config, genesis)); err != nil || listen != wantListen {
			t.Errorf("no listen, gossipListen %q: listen %q (error %v), want %q", gossipListen, listen, err, wantListen)
		}
	}
}

// publicPath (spec §9) is read into the node's PublicPath, the public folder the engine writes for the
// fetcher. Without it the node writes none.
func TestLoadReadsThePublicPath(t *testing.T) {
	config, genesis := baseConfig(t)
	publicPath := filepath.Join(t.TempDir(), "public")
	config["publicPath"] = publicPath
	if nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis)); err != nil || nodeConfig.PublicPath != publicPath {
		t.Errorf("publicPath %q (error %v), want %q", nodeConfig.PublicPath, err, publicPath)
	}
	config, genesis = baseConfig(t)
	if nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis)); err != nil || nodeConfig.PublicPath != "" {
		t.Errorf("a config without publicPath: publicPath %q (error %v), want none", nodeConfig.PublicPath, err)
	}
}

// minimumStepTimeoutMs, the slow-down switch (spec §7.6), is read into the node's MinimumStepTimeout,
// from 0 (off, and the default) to a day, MaxTimingMs. Anything else is refused at load.
func TestLoadReadsTheSlowDownSwitch(t *testing.T) {
	for milliseconds, want := range map[int64]time.Duration{600_000: 10 * time.Minute, ledger.MaxTimingMs: 24 * time.Hour, 0: 0} {
		config, genesis := baseConfig(t)
		config["minimumStepTimeoutMs"] = milliseconds
		if nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis)); err != nil || nodeConfig.MinimumStepTimeout != want {
			t.Errorf("minimumStepTimeoutMs %d gives the switch %v (error %v), want %v", milliseconds, nodeConfig.MinimumStepTimeout, err, want)
		}
	}
	config, genesis := baseConfig(t)
	if nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis)); err != nil || nodeConfig.MinimumStepTimeout != 0 {
		t.Errorf("a config without minimumStepTimeoutMs gives the switch %v (error %v), want it off", nodeConfig.MinimumStepTimeout, err)
	}
	for _, milliseconds := range []int64{-1, ledger.MaxTimingMs + 1} {
		config, genesis := baseConfig(t)
		config["minimumStepTimeoutMs"] = milliseconds
		expectLoadError(t, config, genesis, fmt.Sprintf("minimumStepTimeoutMs %d is out of range", milliseconds))
	}
}

// C1 regression (external review): dynamic-validator mode (validatorThreshold set) deliberately
// leaves the static list empty — the set is derived from stake each block. Startup used to dereference
// a nil set in its log line and panic before serving a single request.
func TestLoadDynamicValidatorModeLeavesStaticSetNil(t *testing.T) {
	config, genesis := baseConfig(t)
	delete(genesis, "validators")
	genesis["validatorThreshold"] = "100"
	nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	rule := nodeConfig.Chain.ValidatorRule
	if len(rule.StaticValidators) != 0 {
		t.Fatal("dynamic mode must leave the static validator list empty (the set is derived from stake at runtime)")
	}
	if rule.Threshold == nil || ledger.FormatAmount(rule.Threshold) != "100" {
		t.Fatal("validatorThreshold must be parsed")
	}
	if got := validatorsLabel(rule); got != "validators=dynamic (stake-derived)" {
		t.Fatalf("validatorsLabel = %q in dynamic mode", got)
	}
	if got := validatorsLabel(ledger.ValidatorRule{StaticValidators: []ledger.StaticValidator{{Name: "a", Power: 2}, {Name: "b", Power: 3}}}); got != "validators=2 validatorPower=5" {
		t.Fatalf("validatorsLabel = %q for static powers 2 and 3, want their count 2 and their total power 5", got)
	}
}

// C4 regression (external review): a truncated key must fail at load. It used to start a node that
// served HTTP but silently never voted or proposed (the driver swallows signing errors) — a
// consensus ghost that looks up. A seed of the wrong length is NewSigner's to refuse; load's message
// says what the field takes, and for the public key the scheme's reason (C29 kept the key checks for
// their clearer messages).
func TestLoadRejectsTruncatedKeys(t *testing.T) {
	half := base64.StdEncoding.EncodeToString(make([]byte, 16))
	reason := signing.NetworkScheme().ValidatePublicKey(make([]byte, 16))
	if reason == nil {
		t.Fatal("premise: the scheme refuses a 16-byte key")
	}

	config, genesis := baseConfig(t)
	config["publicKey"] = half
	expectLoadError(t, config, genesis, "publicKey: not a key of the network's scheme, "+signing.NetworkScheme().Name()+", as keygen writes it: "+reason.Error())

	config, genesis = baseConfig(t)
	config["privateKey"] = half
	expectLoadError(t, config, genesis, "privateKey: not a private key of the network's scheme")
}

// A key pair assembled from two keygen runs (both halves the right size) must fail at load too: it
// used to start a node that signed every vote under a key its peers do not list, a validator that
// never counts toward quorum. The private key is a seed and the node signs as the key the seed
// derives, so a publicKey from another run is refused, by name, before the test signature.
func TestLoadRefusesAPublicKeyThatIsNotTheSeeds(t *testing.T) {
	config, genesis := baseConfig(t)
	config["publicKey"] = freshKey(t)
	expectLoadError(t, config, genesis, "privateKey does not belong to publicKey")
}

// aliasOf spells key differently: the last character before the padding carries two bits per "=" the
// decoder ignores, and changing them gives another string for the same key.
func aliasOf(t *testing.T, key string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := len(strings.TrimRight(key, "=")) - 1
	if last == len(key)-1 {
		return key[:20] + "\n" + key[20:] // no padding, so no unused bits: a newline, which the decoder skips
	}
	alias := key[:last] + string(alphabet[strings.IndexByte(alphabet, key[last])^1]) + key[last+1:]
	if raw, err := base64.StdEncoding.DecodeString(alias); err != nil || base64.StdEncoding.EncodeToString(raw) != key {
		t.Fatalf("alias %q does not decode to key %q", alias, key)
	}
	return alias
}

// Signatures verify only under a key's canonical spelling, so the node's own key in another spelling
// names a key it never signs under. (The genesis keys are ValidateGenesis's to check.)
func TestLoadRejectsKeysInAnotherSpelling(t *testing.T) {
	config, genesis := baseConfig(t)
	config["publicKey"] = aliasOf(t, config["publicKey"].(string))
	expectLoadError(t, config, genesis, "publicKey")
}

// The node's own name is the one its votes carry: it is "" for an unnamed observer, or a name in its
// one spelling.
func TestLoadRefusesANonCanonicalNodeName(t *testing.T) {
	config, genesis := baseConfig(t)
	config["name"] = "Founder"
	expectLoadError(t, config, genesis, `name "Founder" is not a name`)
}

// A validator's sign log lives next to its snapshot, so a node that can be a validator must have a
// snapshotPath: in static mode when its name is in the validator list, in dynamic mode always. An
// observer in static mode may still run without one.
func TestLoadRequiresSnapshotPathOnValidators(t *testing.T) {
	config, genesis := baseConfig(t)
	delete(config, "snapshotPath")
	expectLoadError(t, config, genesis, "snapshotPath")

	dynamic := map[string]any{}
	for field, value := range genesis {
		dynamic[field] = value
	}
	delete(dynamic, "validators")
	dynamic["validatorThreshold"] = "100"
	expectLoadError(t, config, dynamic, "snapshotPath")

	config["name"] = "observer"
	if _, _, _, _, err := load(writeConfig(t, config, genesis)); err != nil {
		t.Errorf("an observer without snapshotPath was refused: %v", err)
	}
}

// keygen prints the two lines a node config takes (spec 8.1): the public key and the private key it
// derives from. Pasted into a config, they load, and the node signs as that public key.
func TestKeygenOutputLoads(t *testing.T) {
	output, err := exec.Command("go", "run", "../keygen").Output()
	if err != nil {
		t.Fatalf("go run ../keygen: %v", err)
	}
	printed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		label, value, found := strings.Cut(line, ":")
		if !found {
			t.Fatalf("keygen printed %q, want label: value lines", line)
		}
		printed[label] = strings.TrimSpace(value)
	}
	if err := signing.ValidatePublicKeyBase64(signing.NetworkScheme(), printed["publicKey"]); err != nil || printed["privateKey"] == "" {
		t.Fatalf("keygen printed %d and %d characters, want a public key (%v) and a private key", len(printed["publicKey"]), len(printed["privateKey"]), err)
	}
	config, genesis := baseConfig(t)
	config["publicKey"], config["privateKey"] = printed["publicKey"], printed["privateKey"]
	genesis["accounts"].([]map[string]any)[0]["publicKey"] = printed["publicKey"]
	nodeConfig, _, _, _, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatalf("keygen's output does not load: %v", err)
	}
	if nodeConfig.Signer.PublicKeyBase64() != printed["publicKey"] {
		t.Error("the node does not sign as the public key keygen printed")
	}
}
