// Command quickstart writes a network of four banks for deploy/compose.yaml, which make quickstart then
// starts: a key pair for each bank, the genesis, each bank's node config, a wallet file for each of two
// funded customers, and network.env, which gives the compose file the name of the engines' record files.
// It writes them into a new folder, deploy/quickstart by default, and refuses a folder that is already
// there, so it never replaces the keys of a network that may be running.
//
// Usage:
//
//	quickstart [-folder deploy/quickstart]
package main

import (
	"encoding/base64"
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
)

// bank is one of the four validators: its name, its engine's fixed IP address on the compose file's
// network, and the port on this machine where the compose file publishes its fetcher.
type bank struct {
	name      string
	ipAddress string
	hostPort  int
}

var banks = []bank{
	{"bank-a", "172.28.70.11", 18081},
	{"bank-b", "172.28.70.12", 18082},
	{"bank-c", "172.28.70.13", 18083},
	{"bank-d", "172.28.70.14", 18084},
}

// publicAddress is the bank's public face as the genesis publishes it: its fetcher, by its name on the
// compose file's network. No CDN stands in front of it here.
func (bank bank) publicAddress() string {
	return fmt.Sprintf("http://%s-fetcher:%d", bank.name, fetcherPort)
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
	GenesisPath  string `json:"genesisPath"`
	Listen       string `json:"listen"`
	GossipListen string `json:"gossipListen"`
	Name         string `json:"name"`
	PublicKey    string `json:"publicKey"`
	PrivateKey   string `json:"privateKey"`
	SnapshotPath string `json:"snapshotPath"`
	PublicPath   string `json:"publicPath"`
}

// walletFile is a wallet's key file before its first start, as the wallet reads it: the pair in
// plaintext. The wallet seals it with a passphrase at the first start that has one.
type walletFile struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

func main() {
	folder := flag.String("folder", "deploy/quickstart", "the folder to write the network into; it must not exist yet")
	flag.Parse()

	chain, err := writeNetwork(*folder)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("the network %s is in %s:\n", chain.NetworkID, *folder)
	fmt.Println("  genesis.json, the four banks' node configs (each holds its bank's private key), the customers' wallet files and network.env")
	fmt.Println("once it runs, the fetchers, each bank's public face, are at http://127.0.0.1:18081 to 18084. A customer's wallet, at its bank (after make build):")
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
// The folder is readable by its owner only, since the node configs and the wallet files hold private
// keys. The node configs, the genesis and network.env are readable by anyone inside it: the compose file
// mounts each config and the genesis into a container whose user, 65532, is not this machine's user.
func writeNetwork(folder string) (ledger.Chain, error) {
	scheme := signing.NetworkScheme()
	parameters := ledger.DefaultParameters()
	// As in cmd/demo: a key change waits 20 blocks, so it shows within a minute, and the signature
	// record keeps the fewest blocks it may.
	parameters.ChangeDelayBlocks = 20
	parameters.RetainBlocks = 1000
	genesis := ledger.Genesis{NetworkName: networkName, GossipPort: gossipPort, Parameters: parameters}

	configs := make(map[string]nodeConfig)
	for _, bank := range banks {
		publicKey, privateKey, err := scheme.GenerateKey()
		if err != nil {
			return ledger.Chain{}, err
		}
		publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKey)
		genesis.Validators = append(genesis.Validators, ledger.GenesisValidator{Name: bank.name, Power: 1})
		genesis.Accounts = append(genesis.Accounts, ledger.GenesisAccount{Name: bank.name, PublicKey: publicKeyBase64, Amount: "1000",
			Delegate: bank.name, Description: "a quickstart bank", PublicAddress: bank.publicAddress(), IPAddresses: []string{bank.ipAddress}})
		configs[bank.name+".json"] = nodeConfig{
			GenesisPath:  containerGenesisPath,
			Listen:       operatorListen,
			GossipListen: fmt.Sprintf(":%d", gossipPort),
			Name:         bank.name,
			PublicKey:    publicKeyBase64,
			PrivateKey:   base64.StdEncoding.EncodeToString(privateKey),
			SnapshotPath: containerSnapshotPath,
			PublicPath:   containerPublicPath,
		}
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
	for fileName, config := range configs {
		if err := writeJSONFile(filepath.Join(folder, fileName), config, 0o644); err != nil {
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
