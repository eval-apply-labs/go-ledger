// Command server runs a hypernode ledger validator node: it loads a config file and the genesis file
// it names, binds its addresses, starts the consensus node, and serves the node HTTP API. With
// gossipListen set it serves two ports (spec §3): the operator port on listen, with every route, and
// the gossip port on gossipListen, with only the routes validators use, behind the allow list. Without
// it, listen serves every route to anyone, as a one-machine trial needs. The startup log prints the
// effective network id, which the genesis makes, and the slow-down switch when it is on.
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
	"strconv"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
)

func main() {
	configPath := flag.String("config", "node.json", "path to the node config JSON")
	flag.Parse()

	nodeConfig, listen, gossipListen, peers, err := load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	listener, gossipListener, ledgerNode, err := bindThenStart(nodeConfig, listen, gossipListen, peers)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("hypernode node up: network=%q name=%q listen=%s peers=%d %s",
		nodeConfig.Chain.NetworkID, nodeConfig.Name, listen, len(peers), validatorsLabel(nodeConfig.Chain.ValidatorRule))
	if nodeConfig.MinimumStepTimeout > 0 {
		log.Printf("the slow-down switch is on: minimumStepTimeoutMs=%d, so each round waits at least %v per step for a proposal or votes; "+
			"set it to 0 and restart once the network is back", nodeConfig.MinimumStepTimeout.Milliseconds(), nodeConfig.MinimumStepTimeout)
	}
	server := node.NewServer(ledgerNode)
	if gossipListener == nil {
		log.Printf("the gossip routes are open to anyone: without gossipListen, listen=%s serves every route, with no allow list", listen)
	} else {
		log.Printf("gossip port: gossipListen=%s serves the gossip routes to the current validators' IP addresses and %d allow entries",
			gossipListen, len(nodeConfig.GossipAllow))
		logPortWarnings(listen, gossipListen, nodeConfig.Chain.GossipPort)
		go serve(gossipListener, server.GossipHandler())
	}
	serve(listener, server.Handler())
}

// logPortWarnings logs, at the start of a node with a gossip port, a line for each of two settings that
// are allowed but are most often a mistake. The operator port on every interface (no host, 0.0.0.0 or
// ::) serves every route, with no allow list, to whoever reaches it. A gossip port other than the
// genesis gossipPort, where the other validators send, is reached only through a port mapping in front
// that forwards the one to the other, which is why it is not refused.
func logPortWarnings(listen, gossipListen string, gossipPort int) {
	if host, _, err := net.SplitHostPort(listen); err == nil && (host == "" || net.ParseIP(host).IsUnspecified()) {
		log.Printf("listen=%s is on every interface: the operator port serves every route, payments and bundle and checkpoint imports among them, "+
			"with no allow list, so it must not be reachable from outside the bank's own network", listen)
	}
	if _, port, err := net.SplitHostPort(gossipListen); err == nil && port != strconv.Itoa(gossipPort) {
		log.Printf("gossipListen=%s is not on the genesis gossipPort %d, where the other validators send: they reach this node only if a port mapping in front forwards %d to it",
			gossipListen, gossipPort, gossipPort)
	}
}

// serve serves handler on listener, and ends the program when it stops.
func serve(listener net.Listener, handler http.Handler) {
	httpServer := &http.Server{
		Handler: handler,
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
	log.Fatalf("serve %s: %v", listener.Addr(), httpServer.Serve(listener))
}

// bindThenStart binds the listen address, and the gossip listen address when there is one, and only
// then opens and starts the node. So a second start of a config on a machine that already runs it fails
// here, on an address the first one holds, before its node could sign: two nodes signing with one key
// double-sign, and the name is frozen. It catches only that mistake: the same key on another machine,
// or under other listen addresses, is not caught. It returns the gossip listener behind the node's
// allow list (node.NewAllowListListener), or nil when gossipListen is "".
func bindThenStart(nodeConfig node.Config, listen, gossipListen string, peers []string) (listener, gossipListener net.Listener, ledgerNode *node.Node, err error) {
	if listener, err = net.Listen("tcp", listen); err != nil {
		return nil, nil, nil, err
	}
	if gossipListen != "" {
		if gossipListener, err = net.Listen("tcp", gossipListen); err != nil {
			listener.Close()
			return nil, nil, nil, err
		}
	}
	if ledgerNode, err = node.Open(nodeConfig, node.NewHTTPTransport(peers)); err != nil {
		listener.Close()
		if gossipListener != nil {
			gossipListener.Close()
		}
		return nil, nil, nil, fmt.Errorf("node: %w", err)
	}
	ledgerNode.Start()
	if gossipListener != nil {
		gossipListener = node.NewAllowListListener(gossipListener, ledgerNode)
	}
	return listener, gossipListener, ledgerNode, nil
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
