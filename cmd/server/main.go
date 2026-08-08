// Command server runs a hypernode ledger validator node: it loads a config file, starts the
// consensus node, and serves the node HTTP API (peer intake + client endpoints).
//
// Usage:
//
//	server -config node.json
package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/node"
)

func main() {
	cfgPath := flag.String("config", "node.json", "path to the node config JSON")
	flag.Parse()

	cfg, listen, peers, err := load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	transport := node.NewHTTPTransport(peers)
	n := node.New(cfg, transport)
	n.Start()

	log.Printf("hypernode node up: network=%q listen=%s peers=%d validators=%s",
		cfg.NetworkID, listen, len(peers), validatorsLabel(cfg.Validators))

	srv := &http.Server{
		Addr: listen, Handler: node.NewServer(n).Handler(),
		// Per-connection deadlines, so handler goroutines cannot accumulate without bound behind a
		// saturated event loop (the open-endpoint DoS surface, findings #10): a client that stalls
		// mid-request or never drains its response is disconnected instead of holding a goroutine
		// forever. Generous for the biggest legitimate response (a full /hdls/checkpoint).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// validatorsLabel describes the configured validator set for the startup log. In dynamic
// (stake-derived) mode Config.Validators is deliberately nil — the set is a function of the
// committed accounts, recomputed each block — so there is no static total power to report.
func validatorsLabel(vs *consensus.ValidatorSet) string {
	if vs == nil {
		return "dynamic (stake-derived)"
	}
	return strconv.FormatInt(vs.TotalPower(), 10)
}
