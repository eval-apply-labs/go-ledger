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
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func main() {
	nodeListen := flag.String("listen", "127.0.0.1:8080", "node HTTP API address")
	uiListen := flag.String("ui-listen", "127.0.0.1:8091", "wallet + admin UI address")
	uiBinary := flag.String("ui", "bin/go_ledger_ui", "path to the wallet UI binary (blank to skip)")
	network := flag.String("network", "village", "network id")
	flag.Parse()

	scheme := signing.Ed25519{}

	// The founder is the sole validator AND the wallet's account, so a governance vote reaches the
	// quorum (1) instantly — you can watch a parameter change or reassignment enact in a block or two.
	founder, founderPublic, founderPrivate := newAccount(scheme, *network)
	bakery, _, _ := newAccount(scheme, *network)
	cafe, _, _ := newAccount(scheme, *network)

	founderKey := founder.PublicKeyBase64()
	genesis := []ledger.Account{
		{PublicKey: founderKey, Name: "founder", Amount: rat("1000"), ValidatorNode: founderKey, Description: "the founding node"},
		{PublicKey: bakery.PublicKeyBase64(), Name: "bakery", Amount: rat("250"), Description: "Alice's bakery"},
		{PublicKey: cafe.PublicKeyBase64(), Name: "cafe", Amount: rat("175"), Description: "the corner cafe"},
	}
	config := node.Config{
		NetworkID:        *network,
		Scheme:           scheme,
		Signer:           founder,
		Validators:       consensus.NewValidatorSet([]consensus.Validator{{PublicKey: founderKey, Power: 1}}),
		TransactionCost:  rat("0.1"),
		Timeout:          3 * time.Second,
		MinBlockInterval: 1 * time.Second,
		Genesis:          genesis,
	}

	ledgerNode := node.New(config, node.NewHTTPTransport(nil))
	server := node.NewServer(ledgerNode)
	go func() { log.Fatalf("node server: %v", http.ListenAndServe(*nodeListen, server.Handler())) }()
	ledgerNode.Start()

	nodeURL := "http://" + *nodeListen
	log.Printf("demo node up on %s — validator 'founder', funded accounts: founder/bakery/cafe", nodeURL)

	// A wallet file holding the founder key, so the wallet opens on the funded, validating account.
	walletPath := filepath.Join(os.TempDir(), "go_ledger_demo_wallet.json")
	if err := writeWallet(walletPath, founderPublic, founderPrivate); err != nil {
		log.Fatalf("wallet file: %v", err)
	}

	walletUI := startWalletUI(*uiBinary, nodeURL, *network, walletPath, *uiListen)
	if walletUI != nil {
		fmt.Printf("\n  ✅ open the wallet + admin UI:  http://%s\n"+
			"     (admin / governance is linked at the bottom of the wallet page)\n\n", *uiListen)
	} else {
		fmt.Printf("\n  node is up on %s. Start the wallet yourself:\n"+
			"    go_ledger_ui -node %s -network %s -wallet %s -listen %s\n\n",
			nodeURL, nodeURL, *network, walletPath, *uiListen)
	}
	fmt.Println("  Ctrl-C to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	if walletUI != nil && walletUI.Process != nil {
		_ = walletUI.Process.Kill()
	}
	log.Print("demo stopped")
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
	ui := exec.Command(binary, "-node", nodeURL, "-network", network, "-wallet", walletPath, "-listen", listen)
	ui.Stdout, ui.Stderr = os.Stdout, os.Stderr
	// A pipe (not the default /dev/null, which stats as a character device) makes the wallet's
	// interactive() check come out false, so it skips the set-a-passphrase prompt: the demo key is
	// ephemeral, and a prompt here would print into the demo's console with no way to answer it.
	ui.Stdin = strings.NewReader("")
	if err := ui.Start(); err != nil {
		log.Printf("could not start the wallet UI (%v) — start it yourself", err)
		return nil
	}
	return ui
}

func newAccount(scheme signing.Scheme, network string) (*signing.Signer, []byte, []byte) {
	public, private, err := scheme.GenerateKey()
	if err != nil {
		log.Fatalf("keygen: %v", err)
	}
	return signing.NewSigner(scheme, network, public, private), public, private
}

func rat(value string) *big.Rat {
	amount, ok := new(big.Rat).SetString(value)
	if !ok {
		log.Fatalf("bad amount %q", value)
	}
	return amount
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
	return os.WriteFile(path, data, 0o600)
}
