package node

import (
	"net"
	"strconv"
)

// resolvePeersFromState makes the network SELF-DESCRIBING — the on-chain peer DNS. It sets the
// transport's gossip peers from the committed IP addresses of the CURRENT validators (each validator
// publishes them in the genesis or by an account update), applying the Hypernode topology over them,
// so the committed state IS the address book. It is all or nothing: until EVERY current validator has
// an IP address, the configured peers stay in use. Resolving among only the validators that have
// published would have them gossip to each other alone while the rest still gossip to everyone, and
// the network can then lose its quorum for good. The configured peers are saved when a resolution is
// first applied and restored whenever resolution stops being complete: a validator's account, and
// with it its addresses, can disappear. Only HTTPTransport has both SetPeers and Peers. For any other
// transport (the in-memory test networks) it is a no-op, so tests are unaffected.
func (node *Node) resolvePeersFromState() {
	transport, transportSupportsResolution := node.transport.(interface {
		SetPeers([][]string)
		Peers() [][]string
	})
	if !transportSupportsResolution {
		return
	}
	switch resolvedAddresses := node.resolvedPeerAddresses(); {
	case resolvedAddresses != nil:
		if !node.peersResolved {
			node.configuredPeers, node.peersResolved = transport.Peers(), true
		}
		transport.SetPeers(resolvedAddresses)
	case node.peersResolved:
		transport.SetPeers(node.configuredPeers)
		node.peersResolved = false
	}
}

// resolvedPeerAddresses is the pure resolution: the gossip URLs of this node's Hypernode neighbours
// among the current validators, by name, once every one of them has published an IP address on chain.
// Returns nil until then (so the caller uses the configured peers). A neighbour's URLs are one for each
// of its IP addresses, in the order it lists them, the order HTTPTransport tries them in: "http://",
// the address and the network's gossip port, joined by net.JoinHostPort, which puts an IPv6 address in
// brackets. The ledger takes an address only in its one plain form (ledger.CanonicalIPAddress), so
// HTTPTransport appends a route to the URL as it does to a configured peer.
func (node *Node) resolvedPeerAddresses() [][]string {
	currentValidators := node.view.Load().validators
	gossipPort := strconv.Itoa(node.config.Chain.GossipPort)
	gossipURLsByValidatorName := make(map[string][]string)
	var validatorNamesWithAddress []string // in name order, as the accounts are
	for _, account := range node.state.Accounts {
		if len(account.IPAddresses) > 0 && currentValidators.Contains(account.Name) {
			for _, ipAddress := range account.IPAddresses {
				gossipURLsByValidatorName[account.Name] = append(gossipURLsByValidatorName[account.Name], "http://"+net.JoinHostPort(ipAddress, gossipPort))
			}
			validatorNamesWithAddress = append(validatorNamesWithAddress, account.Name)
		}
	}
	if len(validatorNamesWithAddress) == 0 || len(validatorNamesWithAddress) < len(currentValidators.Validators()) {
		return nil
	}
	var resolvedAddresses [][]string
	for _, validatorName := range Neighbors(validatorNamesWithAddress, node.self, int(node.state.Parameters.MaxPeers)) {
		resolvedAddresses = append(resolvedAddresses, gossipURLsByValidatorName[validatorName])
	}
	return resolvedAddresses
}
