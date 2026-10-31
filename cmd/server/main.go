// Command server runs a hypernode ledger validator node: it loads a config file and the genesis file
// it names, binds the listen address, starts the consensus node, and serves the node HTTP API (peer
// intake + client endpoints) on that address. The startup log prints the effective network id, which
// the genesis makes.
//
// Usage:
//
//	server -config node.json
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
)

func main() {
	configPath := flag.String("config", "node.json", "path to the node config JSON")
	flag.Parse()

	nodeConfig, listen, peers, err := load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	listener, ledgerNode, err := bindThenStart(nodeConfig, listen, peers)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("hypernode node up: network=%q name=%q listen=%s peers=%d %s",
		nodeConfig.Chain.NetworkID, nodeConfig.Name, listen, len(peers), validatorsLabel(nodeConfig.Chain.ValidatorRule))

	httpServer := &http.Server{
		Handler: node.NewServer(ledgerNode).Handler(),
		// Per-connection deadlines: a client that stalls mid-request or never drains its response is
		// disconnected instead of holding a goroutine forever. They do not free a handler that waits on
		// the event loop (it waits on the loop, never on its request), so a flood still piles goroutines
		// up behind a saturated loop; only a rate-limiting proxy in front bounds that (findings #10).
		// Generous for the biggest legitimate response (a full /hdls/checkpoint).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := httpServer.Serve(listener); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// bindThenStart binds the listen address, and only then opens and starts the node. So a second start of
// a config on a machine that already runs it fails here, on the address the first one holds, before its
// node could sign: two nodes signing with one key double-sign, and the name is frozen. It catches only
// that mistake: the same key on another machine, or under another listen address, is not caught.
func bindThenStart(nodeConfig node.Config, listen string, peers []string) (net.Listener, *node.Node, error) {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, nil, err
	}
	ledgerNode, err := node.Open(nodeConfig, node.NewHTTPTransport(peers))
	if err != nil {
		listener.Close()
		return nil, nil, fmt.Errorf("node: %w", err)
	}
	ledgerNode.Start()
	return listener, ledgerNode, nil
}

// validatorsLabel describes the configured validator rule for the startup log, as key=value fields:
// the number of static validators and their total power, or, in dynamic (stake-derived) mode, where
// the set is a function of the committed accounts, recomputed each block, no static set to report.
func validatorsLabel(validatorRule ledger.ValidatorRule) string {
	if len(validatorRule.StaticValidators) == 0 {
		return "validators=dynamic (stake-derived)"
	}
	var totalPower int64
	for _, validator := range validatorRule.StaticValidators {
		totalPower += validator.Power
	}
	return fmt.Sprintf("validators=%d validatorPower=%d", len(validatorRule.StaticValidators), totalPower)
}
