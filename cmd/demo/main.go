// Command demo boots a complete, self-contained go_ledger network on this machine for a quick look:
// a single-validator node with a few funded, named accounts, and — if the wallet binary is present —
// the wallet + admin UI pointed at it. One command, ephemeral keys, nothing to configure.
//
//	make demo            # builds the wallet too and brings up everything (recommended)
//	go run ./cmd/demo    # node only; prints the wallet command to run yourself
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func main() {
	nodeListen := flag.String("listen", "127.0.0.1:8080", "node HTTP API address")
	uiListen := flag.String("ui-listen", "127.0.0.1:8091", "wallet + admin UI address")
	uiBinary := flag.String("ui", "bin/go_ledger_ui", "path to the wallet UI binary (blank to skip)")
	network := flag.String("network", "village", "network name (the network id is made from it and the genesis)")
	flag.Parse()

	scheme := signing.MLDSA65{}

	// The founder is the sole validator AND the wallet's account, so a governance vote reaches the
	// quorum (1) instantly — you can watch a parameter change or ⅔ name action enact in a block or two.
	founderPublic, founderPrivate := newKeyPair(scheme)
	bakeryPublic, _ := newKeyPair(scheme)
	cafePublic, _ := newKeyPair(scheme)

	// The genesis, checked as a node checks its genesis file. It gives the effective network id, which
	// every signature binds: the name and the hash of the genesis.
	chain, genesisState, err := ledger.ValidateGenesis(demoGenesis(*network, founderPublic, bakeryPublic, cafePublic))
	if err != nil {
		log.Fatal(err) // only -network can be wrong: it must be a name
	}

	// The node's snapshot (with the validator's sign log beside it) and the plaintext founder wallet
	// live in a private temp directory, removed when the demo stops.
	temporaryDirectory, err := os.MkdirTemp("", "go_ledger_demo")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	founder, err := signing.NewSigner(scheme, chain.NetworkID, founderPrivate)
	if err != nil {
		_ = os.RemoveAll(temporaryDirectory)
		log.Fatalf("founder key: %v", err)
	}
	config := demoNodeConfig(chain, genesisState, founder, filepath.Join(temporaryDirectory, "snapshot.json"))

	ledgerNode, err := node.Open(config, node.NewHTTPTransport(nil))
	if err != nil {
		_ = os.RemoveAll(temporaryDirectory)
		log.Fatalf("node: %v", err)
	}
	httpServer := demoHTTPServer(*nodeListen, node.NewServer(ledgerNode).Handler())
	served := make(chan error, 1)
	go func() { served <- httpServer.ListenAndServe() }()
	ledgerNode.Start()

	nodeURL := "http://" + *nodeListen
	log.Printf("demo node up on %s, network %s — validator 'founder', funded accounts: founder/bakery/cafe", nodeURL, chain.NetworkID)

	// A wallet file holding the founder key, so the wallet opens on the funded, validating account.
	walletPath := filepath.Join(temporaryDirectory, "wallet.json")
	if err := writeWallet(walletPath, founderPublic, founderPrivate); err != nil {
		_ = os.RemoveAll(temporaryDirectory)
		log.Fatalf("wallet file: %v", err)
	}

	walletUI := startWalletUI(*uiBinary, nodeURL, *network, walletPath, *uiListen)
	uiCommand := *uiBinary
	if walletUI != nil {
		fmt.Printf("\n  ✅ open the wallet + admin UI:  http://%s\n"+
			"     (admin / governance is linked at the bottom of the wallet page)\n\n", *uiListen)
	} else {
		uiCommand = "go_ledger_ui"
		fmt.Printf("\n  node is up on %s. Start the wallet yourself:\n    %s %s\n\n",
			nodeURL, uiCommand, strings.Join(walletArguments(nodeURL, *network, walletPath, *uiListen), " "))
	}
	// A second wallet, on a key file that does not exist yet, makes a fresh key with no name: register it
	// at the founder's bank, whose desk is the founder wallet, to see a registration end to end.
	fmt.Printf("  a second wallet, for a new name to register at the founder's bank:\n    %s %s\n\n",
		uiCommand, strings.Join(walletArguments(nodeURL, *network, filepath.Join(temporaryDirectory, "second-wallet.json"), "127.0.0.1:8092"), " "))
	fmt.Println("  Ctrl-C to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case err := <-served:
		log.Printf("node server: %v", err)
	}
	if walletUI != nil && walletUI.Process != nil {
		_ = walletUI.Process.Kill()
	}
	ledgerNode.Stop()
	_ = os.RemoveAll(temporaryDirectory)
	log.Print("demo stopped")
}

// demoNodeConfig is the founder node's config. Its pool and comment limits are the defaults that
// cmd/server's load gives a node whose config file leaves them out (0 would mean no limit).
func demoNodeConfig(chain ledger.Chain, genesisState ledger.LedgerState, founder *signing.Signer, snapshotPath string) node.Config {
	return node.Config{
		Chain:            chain,
		Signer:           founder,
		Name:             "founder",
		Genesis:          genesisState,
		SnapshotPath:     snapshotPath,
		MaxMempool:       10000,
		MaxCommentLength: 280,
	}
}

// demoHTTPServer serves the node's API with the timeouts cmd/server's main sets (findings #10), since
// -listen can move the demo off 127.0.0.1.
func demoHTTPServer(listen string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: listen, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// startWalletUI launches the wallet UI binary pointed at the node, or returns nil (printing a hint)
// if the binary is not available.
func startWalletUI(binary, nodeURL, network, walletPath, listen string) *exec.Cmd {
	if binary == "" {
		return nil
	}
	if _, err := os.Stat(binary); err != nil {
		log.Printf("wallet UI binary %q not found — run `make demo` to build it, or start it yourself", binary)
		return nil
	}
	walletUI := exec.Command(binary, walletArguments(nodeURL, network, walletPath, listen)...)
	walletUI.Stdout, walletUI.Stderr = os.Stdout, os.Stderr
	// A pipe (not the default /dev/null, which stats as a character device) makes the wallet's
	// interactive() check come out false, so it skips the set-a-passphrase prompt: the demo key is
	// ephemeral, and a prompt here would print into the demo's console with no way to answer it.
	walletUI.Stdin = strings.NewReader("")
	if err := walletUI.Start(); err != nil {
		log.Printf("could not start the wallet UI (%v) — start it yourself", err)
		return nil
	}
	return walletUI
}

// walletArguments are the wallet UI's flags: the node it reads, the network name it pins, its key
// file, and the address it serves on.
func walletArguments(nodeURL, network, walletPath, listen string) []string {
	return []string{"-node", nodeURL, "-network", network, "-wallet", walletPath, "-listen", listen}
}

// demoGenesis is the demo network's genesis (spec 8.3): the founder, the only validator, with no bank
// of record, and two customers of the founder's bank, the bakery and the cafe. A key change waits 20
// blocks, so it shows within a minute, and the signature record keeps the fewest blocks it may.
func demoGenesis(network string, founderKey, bakeryKey, cafeKey []byte) ledger.Genesis {
	parameters := ledger.DefaultParameters()
	parameters.ChangeDelayBlocks = 20
	parameters.RetainBlocks = 1000
	return ledger.Genesis{
		NetworkName: network,
		Parameters:  parameters,
		Validators:  []ledger.GenesisValidator{{Name: "founder", Power: 1}},
		Accounts: []ledger.GenesisAccount{
			{Name: "founder", PublicKey: base64.StdEncoding.EncodeToString(founderKey), Amount: "1000", Delegate: "founder", Description: "the founding node"},
			{Name: "bakery", PublicKey: base64.StdEncoding.EncodeToString(bakeryKey), Amount: "250", Bank: "founder", Description: "Alice's bakery"},
			{Name: "cafe", PublicKey: base64.StdEncoding.EncodeToString(cafeKey), Amount: "175", Bank: "founder", Description: "the corner cafe"},
		},
	}
}

// newKeyPair returns a fresh public key and its private key, the seed it derives from.
func newKeyPair(scheme signing.Scheme) (public, private []byte) {
	public, private, err := scheme.GenerateKey()
	if err != nil {
		log.Fatalf("keygen: %v", err)
	}
	return public, private
}

func writeWallet(path string, public, private []byte) error {
	walletKey := struct {
		PublicKey  string `json:"publicKey"`
		PrivateKey string `json:"privateKey"`
	}{
		PublicKey:  base64.StdEncoding.EncodeToString(public),
		PrivateKey: base64.StdEncoding.EncodeToString(private),
	}
	data, _ := json.MarshalIndent(walletKey, "", "  ")
	// Created exclusively: O_EXCL never writes through a link planted at the path, nor into a file
	// that already exists with a mode or owner the demo does not control.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
