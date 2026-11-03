// Command quickstart writes a network of four banks for deploy/compose.yaml, which make quickstart then
// starts: the genesis; for each bank, its key, sealed, and the files of its signer, which holds the key
// (spec §2, §5), its engine's config, which names the signer and holds no key, and its wallet's file; a
// wallet file for each of two funded customers; and network.env, which gives the compose file the name
// of the engines' record files. It writes them into a new folder, deploy/quickstart by default, and
// refuses a folder that is already there, so it never replaces the keys of a network that may be
// running.
//
// Usage:
//
//	quickstart [-folder deploy/quickstart]
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The network as deploy/compose.yaml runs it. The test checks the compose file against these.
const (
	networkName = "quickstart"
	gossipPort  = 8070 // every engine gossips on it, at its IP address, behind its allow list
	// operatorListen is each engine's operator port, which serves every route; only its fetcher uses it.
	operatorListen = ":8080"
	// fetcherPort is each fetcher's port in its container. The public addresses on chain name it.
	fetcherPort = 8080
	// Where the compose file puts the genesis, and the engine's volume, in the engine's container.
	containerGenesisPath  = "/genesis.json"
	containerSnapshotPath = "/data/snapshot.json" // the record files go beside it, as the fetcher's flags say
	containerPublicPath   = "/data/public"
	// containerSecretsFolder is where the compose file mounts each secret, under its name: an engine's
	// client secret is <bank>-engine-secret, a wallet's <bank>-wallet-secret.
	containerSecretsFolder = "/run/secrets"
	// signerPort is each signer's port, on its bank's signing network. No port of a signer is published.
	signerPort = 7000
)

// bank is one of the four validators: its name, its engine's fixed IP address on the compose file's
// network ledger, and the port on this machine where the compose file publishes its fetcher. Its signer
// sits on the bank's own signing network, whose subnet is signingSubnet, at signerAddress. No container
// joins that network but the signer and its two clients, the bank's engine and its wallet. The compose
// file publishes the wallet on this machine at walletPort.
type bank struct {
	name          string
	ipAddress     string
	hostPort      int
	signingSubnet string
	signerAddress string
	walletPort    int
}

var banks = []bank{
	{"bank-a", "172.28.70.11", 18081, "172.28.71.0/24", "172.28.71.254", 18091},
	{"bank-b", "172.28.70.12", 18082, "172.28.72.0/24", "172.28.72.254", 18092},
	{"bank-c", "172.28.70.13", 18083, "172.28.73.0/24", "172.28.73.254", 18093},
	{"bank-d", "172.28.70.14", 18084, "172.28.74.0/24", "172.28.74.254", 18094},
}

// publicAddress is the bank's public face as the genesis publishes it: its fetcher, by its name on the
// compose file's network. No CDN stands in front of it here.
func (bank bank) publicAddress() string {
	return fmt.Sprintf("http://%s-fetcher:%d", bank.name, fetcherPort)
}

// signerURL is where the bank's engine and wallet ask its signer: at its address, which the engine and
// the wallet reach on the signing network, where no name service runs (Podman runs none on an internal
// network).
func (bank bank) signerURL() string {
	return fmt.Sprintf("http://%s:%d", bank.signerAddress, signerPort)
}

// signerClients are the clients of each bank's signer, and the domains each may ask for (spec §2.3): the
// engine, and the bank's wallet, which the compose file runs as the desk and the admin page in one, so
// with the domains of both. The desk's are registration, name-request-bank and freeze, never key-change
// or bank-change, which a name's holder signs for itself (review/phase2_spec.md §4).
var signerClients = []signerClientConfig{
	{Name: "engine", Domains: []signing.Domain{signing.DomainBlockRevision, signing.DomainProposal, signing.DomainReceipt}},
	{Name: "wallet", Domains: []signing.Domain{signing.DomainRegistration, signing.DomainNameRequestBank, signing.DomainFreeze,
		signing.DomainParameterVote, signing.DomainReassignmentVote, signing.DomainPayment, signing.DomainAccountUpdate}},
}

// customer is a funded account of the genesis, at one of the banks, whose wallet file the quickstart
// writes, so that a payment can be tried at once.
type customer struct {
	name, bank, amount string
	hostPort           int // the port of its bank's fetcher on this machine, for its wallet
	walletPort         int // the port its wallet serves its page on, on this machine: one each, so that both run at once
}

var customers = []customer{
	{"alice", "bank-a", "500", 18081, 8090},
	{"bob", "bank-b", "500", 18082, 8092},
}

// nodeConfig is a bank's node config, with the fields of cmd/server's config that the quickstart sets.
// It names no peers: every validator publishes its IP address in the genesis, so each engine takes its
// peers from the chain. Nor bootstrap: a new network has no checkpoint to fetch, and an engine that falls
// behind catches up from those peers.
type nodeConfig struct {
	GenesisPath  string             `json:"genesisPath"`
	Listen       string             `json:"listen"`
	GossipListen string             `json:"gossipListen"`
	Name         string             `json:"name"`
	PublicKey    string             `json:"publicKey"`
	Signer       engineSignerConfig `json:"signer"`
	SnapshotPath string             `json:"snapshotPath"`
	PublicPath   string             `json:"publicPath"`
}

// engineSignerConfig is the node config's signer object (spec §2.7): the engine holds no key, and asks
// its bank's signer for every signature, as the client engine.
type engineSignerConfig struct {
	URL        string `json:"url"`
	Client     string `json:"client"`
	SecretFile string `json:"secretFile"`
}

// signerConfig is a bank's signer config, as cmd/signer reads it (spec §5). Its paths are relative: the
// compose file runs the signer in the folder /quickstart, and mounts there the bank's files under the
// names they have in the quickstart's folder, so the config reads the same files in either place.
type signerConfig struct {
	Listen         string               `json:"listen"`
	AllowFrom      []string             `json:"allowFrom"`
	Genesis        string               `json:"genesis"`
	Name           string               `json:"name"`
	KeyFile        string               `json:"keyFile"`
	PassphraseFile string               `json:"passphraseFile"`
	SignLogPath    string               `json:"signLogPath"`
	AuditLogPath   string               `json:"auditLogPath"`
	Clients        []signerClientConfig `json:"clients"`
}

// signerClientConfig is one client in a signer config: its name, its secret's file and its domains.
type signerClientConfig struct {
	Name       string           `json:"name"`
	SecretFile string           `json:"secretFile"`
	Domains    []signing.Domain `json:"domains"`
}

// walletFile is a wallet's key file before its first start, as the wallet reads it. A customer's holds
// the pair in plaintext, which the wallet seals with a passphrase at the first start that has one. A
// bank's holds the bank's public key only: the bank's wallet signs through its signer.
type walletFile struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey,omitempty"`
}

func main() {
	folder := flag.String("folder", "deploy/quickstart", "the folder to write the network into; it must not exist yet")
	flag.Parse()

	chain, err := writeNetwork(*folder)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("the network %s is in %s:\n", chain.NetworkID, *folder)
	fmt.Println("  genesis.json, network.env, the customers' wallet files, and for each bank, such as bank-a:")
	fmt.Println("  its key, sealed (bank-a.key.json), its passphrase (bank-a.passphrase), its signer's config (bank-a.signer.json) and logs (bank-a.signer-logs),")
	fmt.Println("  its signer's clients' secrets (bank-a.engine.secret, bank-a.wallet.secret), its engine's config, with no key (bank-a.json),")
	fmt.Println("  and its wallet's file, its public key only (bank-a.wallet.json), and the password its wallet asks for (bank-a.wallet.password)")
	fmt.Println("once it runs, the fetchers, each bank's public face, are at http://127.0.0.1:18081 to 18084, and the banks' wallets,")
	fmt.Println("which sign through their signers, at http://127.0.0.1:18091 to 18094, under any user name and their password.")
	fmt.Println("A customer's wallet, at its bank (after make build):")
	for _, customer := range customers {
		fmt.Println("  " + walletCommand(*folder, customer))
	}
}

// walletCommand is the command that runs customer's wallet, from the repository's root, at its bank's
// fetcher, with its wallet file in folder.
func walletCommand(folder string, customer customer) string {
	return fmt.Sprintf("bin/go_ledger_ui -node http://127.0.0.1:%d -network %s -wallet %s -listen 127.0.0.1:%d",
		customer.hostPort, networkName, filepath.Join(folder, customer.name+".wallet.json"), customer.walletPort)
}

// writeNetwork makes the keys and the genesis, checks the genesis as a node reads and checks it, and
// only then writes the network's files into a new folder, which it creates. It returns the network's
// chain.
//
// The folder is readable by its owner only, since it holds the banks' keys, their passphrases and their
// clients' secrets, and the customers' private keys. What the compose file mounts into a container is
// readable by anyone inside it, since the containers' user, 65532, is not this machine's user: the
// genesis, network.env and each bank's files (writeBank). A customer's wallet file is its owner's only.
func writeNetwork(folder string) (ledger.Chain, error) {
	scheme := signing.NetworkScheme()
	parameters := ledger.DefaultParameters()
	// As in cmd/demo: a key change waits 20 blocks, so it shows within a minute, and the signature
	// record keeps the fewest blocks it may.
	parameters.ChangeDelayBlocks = 20
	parameters.RetainBlocks = 1000
	genesis := ledger.Genesis{NetworkName: networkName, GossipPort: gossipPort, Parameters: parameters}

	publicKeys, privateKeys := make(map[string][]byte), make(map[string][]byte) // each bank's, by its name
	for _, bank := range banks {
		publicKey, privateKey, err := scheme.GenerateKey()
		if err != nil {
			return ledger.Chain{}, err
		}
		publicKeys[bank.name], privateKeys[bank.name] = publicKey, privateKey
		genesis.Validators = append(genesis.Validators, ledger.GenesisValidator{Name: bank.name, Power: 1})
		genesis.Accounts = append(genesis.Accounts, ledger.GenesisAccount{Name: bank.name, PublicKey: base64.StdEncoding.EncodeToString(publicKey),
			Amount: "1000", Delegate: bank.name, Description: "a quickstart bank", PublicAddress: bank.publicAddress(), IPAddresses: []string{bank.ipAddress}})
	}
	wallets := make(map[string]walletFile)
	for _, customer := range customers {
		publicKey, privateKey, err := scheme.GenerateKey()
		if err != nil {
			return ledger.Chain{}, err
		}
		publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKey)
		genesis.Accounts = append(genesis.Accounts, ledger.GenesisAccount{Name: customer.name, PublicKey: publicKeyBase64,
			Amount: customer.amount, Bank: customer.bank, Description: "a customer of " + customer.bank})
		wallets[customer.name+".wallet.json"] = walletFile{PublicKey: publicKeyBase64, PrivateKey: base64.StdEncoding.EncodeToString(privateKey)}
	}

	genesisData, err := genesisFile(genesis)
	if err != nil {
		return ledger.Chain{}, err
	}
	parsedGenesis, err := ledger.ParseGenesis(genesisData)
	if err != nil {
		return ledger.Chain{}, err
	}
	chain, _, err := ledger.ValidateGenesis(parsedGenesis)
	if err != nil {
		return ledger.Chain{}, err
	}

	if err := os.Mkdir(folder, 0o700); errors.Is(err, os.ErrExist) {
		return ledger.Chain{}, fmt.Errorf("%s is there already: it may hold a running network's keys. Remove it (make quickstart-clean) to make a new network", folder)
	} else if err != nil {
		return ledger.Chain{}, err
	}
	if err := writeFile(filepath.Join(folder, "genesis.json"), genesisData, 0o644); err != nil {
		return ledger.Chain{}, err
	}
	for _, bank := range banks {
		if err := writeBank(folder, bank, chain, publicKeys[bank.name], privateKeys[bank.name]); err != nil {
			return ledger.Chain{}, err
		}
	}
	for fileName, wallet := range wallets {
		if err := writeJSONFile(filepath.Join(folder, fileName), wallet, 0o600); err != nil {
			return ledger.Chain{}, err
		}
	}
	// The engine names its record files <snapshotPath>.<first 16 hex digits of the genesis hash>.record
	// and .lifelong.jsonl (node/record.go), and the compose file gives those names to each fetcher.
	environment := fmt.Sprintf("# The first 16 hex digits of the genesis hash, which name the engines' record files.\nGENESIS_HASH_PREFIX=%x\n",
		chain.GenesisHash[:8])
	if err := writeFile(filepath.Join(folder, "network.env"), []byte(environment), 0o644); err != nil {
		return ledger.Chain{}, err
	}
	return chain, nil
}

// writeBank writes a bank's files into folder, each named after the bank, such as bank-a.key.json:
//   - its key file, sealed with a new passphrase (spec §2.8), and the passphrase file;
//   - a new secret for each of its signer's clients, the engine and the wallet (spec §2.3);
//   - its signer's config, and the folder of its signer's logs, with the sign log of a new network;
//   - its engine's config, which names the signer and holds no key (spec §2.7);
//   - its wallet's file, which holds its public key only, and the password its wallet asks for: the
//     wallet listens beyond loopback in its container, where any other container could reach it.
//
// Each is readable by anyone in the folder, and the logs' folder is writable by anyone in it: the
// compose file mounts them into containers whose user, 65532, is not this machine's user.
func writeBank(folder string, bank bank, chain ledger.Chain, publicKey, privateKey []byte) error {
	inFolder := func(suffix string) string { return filepath.Join(folder, bank.name+suffix) }
	passphrase := rand.Text() // 26 random capital letters and digits: 130 bits
	if err := writeFile(inFolder(".passphrase"), []byte(passphrase+"\n"), 0o644); err != nil {
		return err
	}
	if err := signing.WriteKeyFile(inFolder(".key.json"), publicKey, privateKey, passphrase); err != nil {
		return err
	}
	if err := os.Chmod(inFolder(".key.json"), 0o644); err != nil { // WriteKeyFile writes it for its owner only
		return err
	}

	// The signer lets in its whole signing network, which only its two clients join besides itself. Their
	// addresses on it are the network's to give: Podman 3.4 keeps one fixed address per container, and
	// the engine's and the wallet's is on ledger, where the engine's is the one the genesis lists.
	signerFile := signerConfig{
		Listen:         fmt.Sprintf(":%d", signerPort),
		AllowFrom:      []string{bank.signingSubnet},
		Genesis:        "genesis.json",
		Name:           bank.name,
		KeyFile:        bank.name + ".key.json",
		PassphraseFile: bank.name + ".passphrase",
		SignLogPath:    bank.name + ".signer-logs/sign-log.jsonl",
		AuditLogPath:   bank.name + ".signer-logs/audit-log.jsonl",
	}
	for _, client := range signerClients {
		client.SecretFile = bank.name + "." + client.Name + ".secret"
		secret := make([]byte, 32)
		rand.Read(secret) // it never fails: crypto/rand stops the program instead
		if err := writeFile(filepath.Join(folder, client.SecretFile), []byte(hex.EncodeToString(secret)+"\n"), 0o644); err != nil {
			return err
		}
		signerFile.Clients = append(signerFile.Clients, client)
	}
	if err := writeJSONFile(inFolder(".signer.json"), signerFile, 0o644); err != nil {
		return err
	}
	// The sign log of a new network, which has signed nothing yet, as the signer's -resume-above 0 writes
	// it (cmd/signer/signlog.go). The compose file starts a signer the same way every time, and the flag
	// is refused where a sign log exists, so it can't give the flag the first time only.
	if err := os.Mkdir(inFolder(".signer-logs"), 0o777); err != nil {
		return err
	}
	if err := os.Chmod(inFolder(".signer-logs"), 0o777); err != nil { // undo the umask
		return err
	}
	firstLine, err := json.Marshal(map[string]string{"networkId": chain.NetworkID})
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(inFolder(".signer-logs"), "sign-log.jsonl"), append(firstLine, '\n'), 0o644); err != nil {
		return err
	}

	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKey)
	engineConfig := nodeConfig{
		GenesisPath:  containerGenesisPath,
		Listen:       operatorListen,
		GossipListen: fmt.Sprintf(":%d", gossipPort),
		Name:         bank.name,
		PublicKey:    publicKeyBase64,
		Signer:       engineSignerConfig{URL: bank.signerURL(), Client: "engine", SecretFile: containerSecretsFolder + "/" + bank.name + "-engine-secret"},
		SnapshotPath: containerSnapshotPath,
		PublicPath:   containerPublicPath,
	}
	if err := writeJSONFile(inFolder(".json"), engineConfig, 0o644); err != nil {
		return err
	}
	if err := writeJSONFile(inFolder(".wallet.json"), walletFile{PublicKey: publicKeyBase64}, 0o644); err != nil {
		return err
	}
	return writeFile(inFolder(".wallet.password"), []byte(rand.Text()+"\n"), 0o644)
}

// genesisFile is the genesis as a genesis file holds it. ledger.ParseGenesis reads each parameter by
// its name in lower camel case, such as transactionCost, while ledger.LedgerParameters writes its Go
// field names, such as TransactionCost. So the parameters are written again under the file's names,
// each Go name with its first letter in lowercase, in a Parameters field that hides the genesis's own.
func genesisFile(genesis ledger.Genesis) ([]byte, error) {
	parametersData, err := json.Marshal(genesis.Parameters)
	if err != nil {
		return nil, err
	}
	var parametersByGoName map[string]json.RawMessage
	if err := json.Unmarshal(parametersData, &parametersByGoName); err != nil {
		return nil, err
	}
	parametersByFileName := make(map[string]json.RawMessage)
	for goName, value := range parametersByGoName {
		parametersByFileName[strings.ToLower(goName[:1])+goName[1:]] = value
	}
	return json.MarshalIndent(struct {
		ledger.Genesis
		Parameters map[string]json.RawMessage `json:"parameters"`
	}{genesis, parametersByFileName}, "", "  ")
}

// writeJSONFile writes value as indented JSON into a new file at path, with exactly mode.
func writeJSONFile(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'), mode)
}

// writeFile writes data into a new file at path, with exactly mode: the chmod undoes the umask, which
// could otherwise hide a file from the containers' user.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
