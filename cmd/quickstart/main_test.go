package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The quickstart writes a network that the nodes and the wallets take: a genesis that validates and
// seats the four banks, each at its IP address and with its fetcher as its public address; for each
// bank a node config whose key pair belongs together and is the bank's genesis key; for each customer a
// wallet file with its genesis key; and network.env, with the genesis hash that names the record files.
// The keys are readable by their owner only, and it never writes into a folder that is there already.
func TestQuickstartWritesANetworkTheNodesTake(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "quickstart")
	chain, err := writeNetwork(folder)
	if err != nil {
		t.Fatal(err)
	}
	if information, err := os.Stat(folder); err != nil || information.Mode().Perm() != 0o700 {
		t.Errorf("the folder: %v; want mode 0700, as it holds the private keys", err)
	}

	genesis, err := ledger.ParseGenesis(readFile(t, folder, "genesis.json", 0o644))
	if err != nil {
		t.Fatal(err)
	}
	genesisChain, state, err := ledger.ValidateGenesis(genesis)
	if err != nil {
		t.Fatalf("the genesis is refused: %v", err)
	}
	if genesisChain.NetworkID != chain.NetworkID || !strings.HasPrefix(chain.NetworkID, networkName+"/") || chain.GossipPort != gossipPort {
		t.Errorf("the network id is %q and the gossip port %d; want the written genesis's %q, and %d", chain.NetworkID, chain.GossipPort, genesisChain.NetworkID, gossipPort)
	}
	accountsByName := make(map[string]ledger.Account)
	for _, account := range state.Accounts {
		accountsByName[account.Name] = account
	}

	var seatedNames []string
	for _, validator := range chain.ValidatorRule.SetFor(state).Validators() {
		seatedNames = append(seatedNames, validator.Name)
	}
	for _, bank := range banks {
		if !slices.Contains(seatedNames, bank.name) {
			t.Errorf("%s is not seated: the seated names are %v", bank.name, seatedNames)
		}
		account := accountsByName[bank.name]
		if account.PublicAddress != "http://"+bank.name+"-fetcher:8080" || !slices.Equal(account.IPAddresses, []string{bank.ipAddress}) {
			t.Errorf("%s publishes %q and %v; want its fetcher and its engine's IP address %s", bank.name, account.PublicAddress, account.IPAddresses, bank.ipAddress)
		}

		var config nodeConfig
		decoder := json.NewDecoder(bytes.NewReader(readFile(t, folder, bank.name+".json", 0o644)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			t.Fatalf("the config of %s: %v", bank.name, err)
		}
		if config.Name != bank.name || config.PublicKey != account.PublicKey {
			t.Errorf("the config of %s names %q with another key than the genesis gives it", bank.name, config.Name)
		}
		if signer := newSigner(t, chain, config.PrivateKey); signer.PublicKeyBase64() != config.PublicKey {
			t.Errorf("the private key of %s does not belong to its public key", bank.name)
		}
		if config.GenesisPath != containerGenesisPath || config.Listen != operatorListen || config.GossipListen != ":8070" ||
			config.SnapshotPath != containerSnapshotPath || config.PublicPath != containerPublicPath {
			t.Errorf("the config of %s does not use the compose file's paths and ports: %+v", bank.name, config)
		}
	}

	for _, customer := range customers {
		var wallet walletFile
		if err := json.Unmarshal(readFile(t, folder, customer.name+".wallet.json", 0o600), &wallet); err != nil {
			t.Fatal(err)
		}
		account := accountsByName[customer.name]
		if account.PublicKey != wallet.PublicKey || account.Bank != customer.bank || account.Amount.Sign() <= 0 {
			t.Errorf("%s's wallet file does not hold its funded genesis account at %s: %+v", customer.name, customer.bank, account)
		}
		if signer := newSigner(t, chain, wallet.PrivateKey); signer.PublicKeyBase64() != wallet.PublicKey {
			t.Errorf("the private key in %s's wallet file does not belong to its public key", customer.name)
		}
		if bankIndex := slices.IndexFunc(banks, func(bank bank) bool { return bank.name == customer.bank }); banks[bankIndex].hostPort != customer.hostPort {
			t.Errorf("%s's wallet would read port %d, not its bank's fetcher, at %d", customer.name, customer.hostPort, banks[bankIndex].hostPort)
		}
	}

	if environment, want := string(readFile(t, folder, "network.env", 0o644)), fmt.Sprintf("\nGENESIS_HASH_PREFIX=%x\n", chain.GenesisHash[:8]); !strings.HasSuffix(environment, want) {
		t.Errorf("network.env is %q; want it to end with %q", environment, want)
	}

	if _, err := writeNetwork(folder); err == nil {
		t.Error("the quickstart wrote into a folder that was there already")
	}
}

// deploy/compose.yaml runs the network the quickstart writes. Each engine has its bank's IP address,
// on the network's subnet, reads its own config as a secret and the genesis where the config says, and
// keeps its data in its volume. Each fetcher reaches its engine's operator port, mounts the same volume
// read-only, serves the public folder and the record files where the engine writes them, and is
// published on its bank's host port. Every container has a fixed IP address on the subnet, none twice,
// so that none takes one from the network's pool: one that took a stopped engine's address would be let
// in by the others (review N9).
func TestComposeFileRunsTheQuickstartNetwork(t *testing.T) {
	compose, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	composeText := string(compose)
	subnet := netip.MustParsePrefix(regexp.MustCompile(`subnet: (\S+)`).FindStringSubmatch(composeText)[1])

	_, servicesSection, _ := strings.Cut(composeText, "\nservices:\n")
	servicesSection, _, _ = strings.Cut(servicesSection, "\nnetworks:\n")
	services := regexp.MustCompile(`(?m)^  ([a-z0-9-]+):$`).FindAllStringSubmatch(servicesSection, -1)
	if len(services) != 2*len(banks) {
		t.Errorf("the compose file has %d services, want an engine and a fetcher for each of the %d banks", len(services), len(banks))
	}
	serviceAt := make(map[netip.Addr]string)
	for _, service := range services {
		fixedAddress := regexp.MustCompile(`ipv4_address: (\S+)`).FindStringSubmatch(serviceBlock(t, composeText, service[1]))
		if fixedAddress == nil {
			t.Errorf("%s has no fixed IP address: it would take one from the network's pool", service[1])
			continue
		}
		address, err := netip.ParseAddr(fixedAddress[1])
		if err != nil || !subnet.Contains(address) || serviceAt[address] != "" {
			t.Errorf("%s is at %s, which is not an address of %s, or is %s's too", service[1], fixedAddress[1], subnet, serviceAt[address])
		}
		serviceAt[address] = service[1]
	}
	// The fetchers' record flags name the files as the engine does: the snapshot path, the first 16 hex
	// digits of the genesis hash, which network.env gives, and .record or .lifelong.jsonl.
	hashPrefixVariable := regexp.MustCompile(`\$\{GENESIS_HASH_PREFIX[^}]*\}`)

	for _, bank := range banks {
		engine := serviceBlock(t, composeText, bank.name+"-engine")
		if !strings.Contains(engine, "ipv4_address: "+bank.ipAddress) || !subnet.Contains(netip.MustParseAddr(bank.ipAddress)) {
			t.Errorf("%s's engine is not at %s, in %s:\n%s", bank.name, bank.ipAddress, subnet, engine)
		}
		for _, want := range []string{
			fmt.Sprintf(`["/server", "-config", "/run/secrets/%s-config"]`, bank.name),
			fmt.Sprintf("secrets: [%s-config]", bank.name),
			fmt.Sprintf(`"./quickstart/genesis.json:%s:ro"`, containerGenesisPath),
			fmt.Sprintf(`"%s-data:%s"`, bank.name, filepath.Dir(containerSnapshotPath)),
		} {
			if !strings.Contains(engine, want) {
				t.Errorf("%s's engine lacks %s:\n%s", bank.name, want, engine)
			}
		}
		if !strings.Contains(composeText, fmt.Sprintf("  %s-config:\n    file: ./quickstart/%s.json\n", bank.name, bank.name)) {
			t.Errorf("the secret %s-config is not the quickstart's %s.json", bank.name, bank.name)
		}

		fetcher := hashPrefixVariable.ReplaceAllString(serviceBlock(t, composeText, bank.name+"-fetcher"), "0123456789abcdef")
		for _, want := range []string{
			fmt.Sprintf(`"-engine", "http://%s-engine%s"`, bank.name, operatorListen),
			fmt.Sprintf(`"-folder", "%s"`, containerPublicPath),
			fmt.Sprintf(`"-record", "%s.0123456789abcdef.record"`, containerSnapshotPath),
			fmt.Sprintf(`"-lifelong", "%s.0123456789abcdef.lifelong.jsonl"`, containerSnapshotPath),
			fmt.Sprintf(`"%s-data:%s:ro"`, bank.name, filepath.Dir(containerSnapshotPath)),
			fmt.Sprintf(`"127.0.0.1:%d:%d"`, bank.hostPort, fetcherPort),
		} {
			if !strings.Contains(fetcher, want) {
				t.Errorf("%s's fetcher lacks %s:\n%s", bank.name, want, fetcher)
			}
		}
	}
}

// The quickstart prints a command for each customer's wallet, at its bank's fetcher, each on a port of
// its own, so that the two run at once: both on the wallet's default port would not.
func TestQuickstartPrintsAWalletCommandForEachCustomer(t *testing.T) {
	customerAt := make(map[string]string)
	for _, customer := range customers {
		command := walletCommand("deploy/quickstart", customer)
		listen := "127.0.0.1:8090" // the wallet's default
		if flagValue := regexp.MustCompile(` -listen (\S+)`).FindStringSubmatch(command); flagValue != nil {
			listen = flagValue[1]
		}
		if !strings.Contains(command, fmt.Sprintf(" -node http://127.0.0.1:%d ", customer.hostPort)) ||
			!strings.Contains(command, " -wallet deploy/quickstart/"+customer.name+".wallet.json") || customerAt[listen] != "" {
			t.Errorf("%s's wallet command, %q, is not at its bank's fetcher with its wallet file, or listens where %s's does", customer.name, command, customerAt[listen])
		}
		customerAt[listen] = customer.name
	}
}

// The images' build context leaves out the quickstart's keys, and the wallet's key and state files, in
// case a developer ran the wallet from ui/, which deploy/ui.Dockerfile copies whole (review N13), but
// none of the sources the images build from. The test reads .dockerignore's patterns as Docker does.
func TestTheBuildContextLeavesTheKeysOut(t *testing.T) {
	dockerignore, err := os.ReadFile(filepath.Join("..", "..", ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	var patterns [][]string
	for _, line := range strings.Split(string(dockerignore), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, strings.Split(line, "/"))
		}
	}
	leftOut := func(filePath string) bool {
		return slices.ContainsFunc(patterns, func(pattern []string) bool { return matchesSegments(pattern, strings.Split(filePath, "/")) })
	}
	for _, filePath := range []string{"deploy/quickstart/bank-a.json", "ui/wallet.json", "ui/wallet.json.state", "ui/wallet.json.trust",
		"ui/wallet.json.trust.new", "ui/wallet.json.tmp123", "ui/alice.wallet.json", "ui/templates/wallet.json", "ui/key.state", "ui/key.trust"} {
		if !leftOut(filePath) {
			t.Errorf("%s goes into the build context", filePath)
		}
	}
	for _, filePath := range []string{"go.mod", "node/node.go", "cmd/server/main.go", "ui/go.mod", "ui/main.go", "ui/templates/wallet.html"} {
		if leftOut(filePath) {
			t.Errorf("%s, which an image builds from, is left out of the build context", filePath)
		}
	}
}

// matchesSegments reports whether a .dockerignore pattern, split at its slashes, matches a path, split
// likewise: each segment as path.Match matches it, a "**" segment standing for any number of them, and
// a pattern that matches a folder matches everything in it.
func matchesSegments(pattern, segments []string) bool {
	switch {
	case len(pattern) == 0:
		return true // the path, or a folder it is in
	case pattern[0] == "**":
		return matchesSegments(pattern[1:], segments) || (len(segments) > 0 && matchesSegments(pattern, segments[1:]))
	case len(segments) == 0:
		return false
	}
	matched, _ := path.Match(pattern[0], segments[0])
	return matched && matchesSegments(pattern[1:], segments[1:])
}

// serviceBlock is the lines of the compose file's service name: from its line, indented by two spaces,
// to the next line indented by two spaces or less.
func serviceBlock(t *testing.T, composeText, name string) string {
	t.Helper()
	_, rest, found := strings.Cut(composeText, "\n  "+name+":\n")
	if !found {
		t.Fatalf("the compose file has no service %s", name)
	}
	if end := regexp.MustCompile(`\n {0,2}\S`).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return rest
}

// readFile reads the file name of folder, and checks its mode.
func readFile(t *testing.T, folder, name string, mode os.FileMode) []byte {
	t.Helper()
	path := filepath.Join(folder, name)
	if information, err := os.Stat(path); err != nil || information.Mode().Perm() != mode {
		t.Errorf("%s: %v; want mode %04o", name, err, mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// newSigner is the signer of a base64 private key, on chain's network.
func newSigner(t *testing.T, chain ledger.Chain, privateKeyBase64 string) *signing.Signer {
	t.Helper()
	privateKey, err := base64.StdEncoding.DecodeString(privateKeyBase64)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signing.NewSigner(chain.Scheme, chain.NetworkID, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
