package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
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

// The quickstart writes a network that the nodes, the signers and the wallets take: a genesis that
// validates and seats the four banks, each at its IP address and with its fetcher as its public address;
// for each bank, its key file, sealed, which its passphrase file opens to the bank's genesis key, its
// signer's config (which cmd/signer's tests load), and a node config that names the signer and holds no
// private key (spec §2.7, §5); for each customer a wallet file with its genesis key; and network.env,
// with the genesis hash that names the record files. The keys are readable by their owner only, and it
// never writes into a folder that is there already.
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

	// The signer's two clients (spec §2.3): the engine, and the bank's wallet, which serves as both the desk
	// and the admin page.
	wantClients := []signerClientConfig{
		{Name: "engine", Domains: []signing.Domain{"block-revision", "proposal", "receipt"}},
		{Name: "wallet", Domains: []signing.Domain{"registration", "name-request-bank", "freeze", // the desk's: never key-change or bank-change
			"parameter-vote", "reassignment-vote", "payment", "account-update"}}, // and the admin page's, but freeze, which is the desk's too
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
		configData := readFile(t, folder, bank.name+".json", 0o644)
		if bytes.Contains(configData, []byte("privateKey")) {
			t.Errorf("the config of %s holds a private key; want it in the signer only", bank.name)
		}
		decodeStrictly(t, configData, &config)
		if config.Name != bank.name || config.PublicKey != account.PublicKey {
			t.Errorf("the config of %s names %q with another key than the genesis gives it", bank.name, config.Name)
		}
		wantSigner := engineSignerConfig{URL: "http://" + bank.signerAddress + ":7000", Client: "engine", SecretFile: "/run/secrets/" + bank.name + "-engine-secret"}
		if config.Signer != wantSigner {
			t.Errorf("the config of %s names the signer %+v; want %+v", bank.name, config.Signer, wantSigner)
		}
		if config.GenesisPath != containerGenesisPath || config.Listen != operatorListen || config.GossipListen != ":8070" ||
			config.SnapshotPath != containerSnapshotPath || config.PublicPath != containerPublicPath {
			t.Errorf("the config of %s does not use the compose file's paths and ports: %+v", bank.name, config)
		}

		// The key file is sealed, and its passphrase file opens it to the bank's genesis key.
		readFile(t, folder, bank.name+".key.json", 0o644)
		keyFile, err := signing.ReadKeyFile(filepath.Join(folder, bank.name+".key.json"))
		if err != nil || keyFile.Encrypted == nil || keyFile.PrivateKey != "" {
			t.Fatalf("the key file of %s: %v; want it sealed", bank.name, err)
		}
		readFile(t, folder, bank.name+".passphrase", 0o644)
		passphrase, err := signing.ReadPassphraseFile(filepath.Join(folder, bank.name+".passphrase"))
		if err != nil {
			t.Fatal(err)
		}
		privateKey, err := keyFile.Unlock(passphrase)
		if err != nil {
			t.Fatalf("the passphrase of %s does not open its key file: %v", bank.name, err)
		}
		if signer := newSigner(t, chain, base64.StdEncoding.EncodeToString(privateKey)); signer.PublicKeyBase64() != account.PublicKey || keyFile.PublicKey != account.PublicKey {
			t.Errorf("the key file of %s does not hold its genesis key", bank.name)
		}

		// The signer's config: the bank's name, its signing network, from which its two clients connect,
		// each client with a secret of its own and the domains of spec §2.3, and the sign log of a new
		// network, on this network.
		var signerFile signerConfig
		decodeStrictly(t, readFile(t, folder, bank.name+".signer.json", 0o644), &signerFile)
		if signerFile.Name != bank.name || signerFile.Listen != ":7000" || !slices.Equal(signerFile.AllowFrom, []string{bank.signingSubnet}) ||
			signerFile.Genesis != "genesis.json" || signerFile.KeyFile != bank.name+".key.json" || signerFile.PassphraseFile != bank.name+".passphrase" {
			t.Errorf("the signer config of %s: %+v", bank.name, signerFile)
		}
		if len(signerFile.Clients) != len(wantClients) {
			t.Fatalf("the signer of %s has %d clients; want the engine and the wallet", bank.name, len(signerFile.Clients))
		}
		var secrets []string
		for i, client := range signerFile.Clients {
			want := wantClients[i]
			want.SecretFile = bank.name + "." + want.Name + ".secret"
			readFile(t, folder, client.SecretFile, 0o644)
			secret, err := signing.ReadSecretFile(filepath.Join(folder, client.SecretFile))
			if err != nil || slices.Contains(secrets, string(secret)) || client.Name != want.Name || client.SecretFile != want.SecretFile || !slices.Equal(client.Domains, want.Domains) {
				t.Errorf("the signer of %s has the client %+v (%v); want %+v, with a secret of its own", bank.name, client, err, want)
			}
			secrets = append(secrets, string(secret))
		}
		if information, err := os.Stat(filepath.Join(folder, bank.name+".signer-logs")); err != nil || information.Mode().Perm() != 0o777 {
			t.Errorf("the signer's logs folder of %s: %v; want mode 0777, for the signer's container user", bank.name, err)
		}
		if signerFile.SignLogPath != bank.name+".signer-logs/sign-log.jsonl" || signerFile.AuditLogPath != bank.name+".signer-logs/audit-log.jsonl" {
			t.Errorf("the signer of %s logs to %s and %s; want its logs folder", bank.name, signerFile.SignLogPath, signerFile.AuditLogPath)
		}
		if signLog := string(readFile(t, folder, signerFile.SignLogPath, 0o644)); signLog != `{"networkId":"`+chain.NetworkID+"\"}\n" {
			t.Errorf("the sign log of %s is %q; want a new network's", bank.name, signLog)
		}

		var bankWallet walletFile
		decodeStrictly(t, readFile(t, folder, bank.name+".wallet.json", 0o644), &bankWallet)
		if bankWallet != (walletFile{PublicKey: account.PublicKey}) {
			t.Errorf("the wallet file of %s holds %+v; want its public key only", bank.name, bankWallet)
		}
		if password := strings.TrimSpace(string(readFile(t, folder, bank.name+".wallet.password", 0o644))); len(password) != 26 {
			t.Errorf("the wallet password of %s is %q; want 26 random letters and digits", bank.name, password)
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

// deploy/compose.yaml runs the network the quickstart writes: for each bank, its engine, its signer, its
// fetcher and its wallet.
//   - Every container on the network ledger has a fixed IP address there, on its subnet, none twice, so
//     that none takes one from its pool: one that took a stopped engine's address would be let in by
//     the others (review N9). For a container on two networks, ledger is the first by name: Podman 3.4
//     keeps one fixed address per container, on its first network by name.
//   - Each bank's signer joins only the bank's signing network, at the address the quickstart's configs
//     give it. That network has the subnet the signer's allowFrom lists, is internal, and no other
//     container joins it but the bank's engine and wallet. The signer publishes no port (spec §2.2, §5).
//   - Each of the bank's files is mounted into the containers that need it and no other: its key and its
//     passphrase into its signer only, each client's secret into that client and the signer (spec §5).
//     The signer runs in /quickstart, where each of its files has its name in deploy/quickstart, as its
//     config names them; all but the logs' folder are read-only.
//   - The engine reads its own config as a secret, its client secret where its config says, and the
//     genesis, and keeps its data in its volume. The fetcher reaches its engine's operator port, mounts the
//     same volume read-only, serves the public folder and the record files where the engine writes them,
//     and is published on its bank's host port. The wallet reads its engine, asks its signer as the client
//     wallet with that client's secret, holds the bank's wallet file, asks for the bank's wallet
//     password, and is published under the port it listens on.
func TestComposeFileRunsTheQuickstartNetwork(t *testing.T) {
	compose, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	composeText := string(compose)
	_, servicesSection, _ := strings.Cut(composeText, "\nservices:\n")
	servicesSection, networksSection, _ := strings.Cut(servicesSection, "\nnetworks:\n")
	subnets, internal := map[string]netip.Prefix{}, map[string]bool{}
	for _, network := range regexp.MustCompile(`(?m)^  ([a-z0-9-]+):\n((?:    .*\n)+)`).FindAllStringSubmatch(networksSection, -1) {
		if subnet := regexp.MustCompile(`subnet: (\S+)`).FindStringSubmatch(network[2]); subnet != nil {
			subnets[network[1]] = netip.MustParsePrefix(subnet[1])
		}
		internal[network[1]] = strings.Contains(network[2], "internal: true")
	}
	// Each secret's file in deploy/quickstart, by the secret's name.
	secretFiles := map[string]string{}
	for _, secret := range regexp.MustCompile(`(?m)^  ([a-z0-9-]+):\n    file: \./quickstart/(\S+)$`).FindAllStringSubmatch(composeText, -1) {
		secretFiles[secret[1]] = secret[2]
	}

	services := regexp.MustCompile(`(?m)^  ([a-z0-9-]+):$`).FindAllStringSubmatch(servicesSection, -1)
	if len(services) != 4*len(banks) {
		t.Errorf("the compose file has %d services, want an engine, a signer, a fetcher and a wallet for each of the %d banks", len(services), len(banks))
	}
	serviceAt := map[string]map[netip.Addr]string{} // by network, the service at each fixed address
	networksOf := map[string][]string{}             // by service, the networks it joins
	mountedBy := map[string][]string{}              // by file of deploy/quickstart, the services that mount it
	for _, service := range services {
		block := serviceBlock(t, composeText, service[1])
		for _, network := range regexp.MustCompile(`(?m)^      ([a-z0-9-]+):$(?:\n        ipv4_address: (\S+)$)?`).FindAllStringSubmatch(block, -1) {
			networksOf[service[1]] = append(networksOf[service[1]], network[1])
			if network[2] == "" {
				continue
			}
			address, err := netip.ParseAddr(network[2])
			if err != nil || !subnets[network[1]].Contains(address) || serviceAt[network[1]][address] != "" {
				t.Errorf("%s is at %s on %s, which is not an address of its subnet %s, or is %s's too", service[1], network[2], network[1], subnets[network[1]], serviceAt[network[1]][address])
			}
			if serviceAt[network[1]] == nil {
				serviceAt[network[1]] = map[netip.Addr]string{}
			}
			serviceAt[network[1]][address] = service[1]
		}
		onLedger := regexp.MustCompile(`(?m)^      ledger:\n        ipv4_address: \S+$`).MatchString(block)
		switch {
		case slices.Contains(networksOf[service[1]], "ledger") && !onLedger:
			t.Errorf("%s has no fixed IP address on ledger: it would take one from its pool", service[1])
		case onLedger && slices.Min(networksOf[service[1]]) != "ledger":
			t.Errorf("%s joins %v, of which ledger is not the first by name: Podman 3.4 would give it an address from ledger's pool", service[1], networksOf[service[1]])
		}
		for _, mount := range regexp.MustCompile(`"\./quickstart/([^:"]+):`).FindAllStringSubmatch(block, -1) {
			mountedBy[mount[1]] = append(mountedBy[mount[1]], service[1])
		}
		if secrets := regexp.MustCompile(`secrets: \[(.*)\]`).FindStringSubmatch(block); secrets != nil {
			for _, secret := range strings.Split(secrets[1], ", ") {
				mountedBy[secretFiles[secret]] = append(mountedBy[secretFiles[secret]], service[1])
			}
		}
	}
	// The fetchers' record flags name the files as the engine does: the snapshot path, the first 16 hex
	// digits of the genesis hash, which network.env gives, and .record or .lifelong.jsonl.
	hashPrefixVariable := regexp.MustCompile(`\$\{GENESIS_HASH_PREFIX[^}]*\}`)

	for _, bank := range banks {
		engineName, signerName, fetcherName, walletName, signingNetwork := bank.name+"-engine", bank.name+"-signer", bank.name+"-fetcher", bank.name+"-wallet", "signing-"+bank.name
		for _, seat := range []struct{ service, network, address string }{{engineName, "ledger", bank.ipAddress}, {signerName, signingNetwork, bank.signerAddress}} {
			if address, err := netip.ParseAddr(seat.address); err != nil || serviceAt[seat.network][address] != seat.service {
				t.Errorf("%s is not at %s on %s, where the quickstart's genesis and configs reach it", seat.service, seat.address, seat.network)
			}
		}
		if subnets[signingNetwork].String() != bank.signingSubnet {
			t.Errorf("%s has the subnet %s; want %s, which its signer's allowFrom lists", signingNetwork, subnets[signingNetwork], bank.signingSubnet)
		}
		var onSigningNetwork []string
		for _, service := range slices.Sorted(maps.Keys(networksOf)) {
			if slices.Contains(networksOf[service], signingNetwork) {
				onSigningNetwork = append(onSigningNetwork, service)
			}
		}
		if !internal[signingNetwork] || !slices.Equal(onSigningNetwork, []string{engineName, signerName, walletName}) || !slices.Equal(networksOf[signerName], []string{signingNetwork}) {
			t.Errorf("%s is internal: %v, and joined by %v, with the signer on %v; want an internal network of the bank's engine, signer and wallet, and the signer on no other",
				signingNetwork, internal[signingNetwork], onSigningNetwork, networksOf[signerName])
		}
		for file, want := range map[string][]string{
			bank.name + ".json":            {engineName},
			bank.name + ".signer.json":     {signerName},
			bank.name + ".key.json":        {signerName},
			bank.name + ".passphrase":      {signerName},
			bank.name + ".engine.secret":   {engineName, signerName},
			bank.name + ".wallet.secret":   {signerName, walletName},
			bank.name + ".wallet.json":     {walletName},
			bank.name + ".wallet.password": {walletName},
			bank.name + ".signer-logs":     {signerName},
		} {
			if mounters := slices.Sorted(slices.Values(mountedBy[file])); !slices.Equal(mounters, want) {
				t.Errorf("%s is mounted into %v; want %v only", file, mounters, want)
			}
		}

		engine := serviceBlock(t, composeText, engineName)
		for _, want := range []string{
			fmt.Sprintf(`["/server", "-config", "/run/secrets/%s-config"]`, bank.name),
			fmt.Sprintf("secrets: [%s-config, %s-engine-secret]", bank.name, bank.name), // the secret the engine's config names, in containerSecretsFolder
			fmt.Sprintf(`"./quickstart/genesis.json:%s:ro"`, containerGenesisPath),
			fmt.Sprintf(`"%s-data:%s"`, bank.name, filepath.Dir(containerSnapshotPath)),
			"restart: on-failure", // until its signer answers
		} {
			if !strings.Contains(engine, want) {
				t.Errorf("%s's engine lacks %s:\n%s", bank.name, want, engine)
			}
		}
		if secretFiles[bank.name+"-config"] != bank.name+".json" {
			t.Errorf("the secret %s-config is not the quickstart's %s.json", bank.name, bank.name)
		}

		signer := serviceBlock(t, composeText, signerName)
		for _, want := range []string{fmt.Sprintf(`["/signer", "-config", "%s.signer.json"]`, bank.name), "working_dir: /quickstart"} {
			if !strings.Contains(signer, want) {
				t.Errorf("%s's signer lacks %s:\n%s", bank.name, want, signer)
			}
		}
		for _, mount := range regexp.MustCompile(`"\./quickstart/([^:"]+):([^:"]+)(:ro)?"`).FindAllStringSubmatch(signer, -1) {
			if mount[2] != "/quickstart/"+mount[1] || (mount[3] == "") != (mount[1] == bank.name+".signer-logs") {
				t.Errorf("%s's signer mounts %s at %s%s; want it at /quickstart/%s, read-only but for its logs", bank.name, mount[1], mount[2], mount[3], mount[1])
			}
		}
		if strings.Contains(signer, "ports:") {
			t.Errorf("%s's signer publishes a port", bank.name)
		}

		fetcher := hashPrefixVariable.ReplaceAllString(serviceBlock(t, composeText, fetcherName), "0123456789abcdef")
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

		wallet := serviceBlock(t, composeText, walletName)
		for _, want := range []string{
			"image: go-ledger-ui",
			fmt.Sprintf(`"-node", "http://%s-engine%s", "-network", "%s", "-listen", ":%d"`, bank.name, operatorListen, networkName, bank.walletPort),
			fmt.Sprintf(`"-signer-url", "%s", "-signer-client", "wallet", "-signer-secret-file", "/run/secrets/%s-wallet-secret"`, bank.signerURL(), bank.name),
			fmt.Sprintf(`"-password-file", "/run/secrets/%s-wallet-password"`, bank.name), // it listens beyond loopback
			fmt.Sprintf("secrets: [%s-wallet-secret, %s-wallet-password]", bank.name, bank.name),
			fmt.Sprintf(`"./quickstart/%s.wallet.json:/wallet/wallet.json:ro"`, bank.name), // the image's -wallet
			fmt.Sprintf(`"127.0.0.1:%d:%d"`, bank.walletPort, bank.walletPort),
		} {
			if !strings.Contains(wallet, want) {
				t.Errorf("%s's wallet lacks %s:\n%s", bank.name, want, wallet)
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

// decodeStrictly decodes data into value, and fails the test on a field value has not.
func decodeStrictly(t *testing.T, data []byte, value any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		t.Fatalf("%s: %v", data[:min(len(data), 60)], err)
	}
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
func newSigner(t *testing.T, chain ledger.Chain, privateKeyBase64 string) signing.Signer {
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
