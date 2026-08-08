package node

import "sort"

// resolvePeersFromState makes the network SELF-DESCRIBING — the on-chain peer DNS. It sets the
// transport's gossip peers from the committed addresses of the CURRENT validators (each validator
// publishes its address via an account update), applying the Hypernode topology over them, so the
// committed state IS the address book. If no validator has published an address yet, the configured
// bootstrap peers are left in place. It is a no-op for a transport whose peers can't be set (the
// in-memory test bus), so tests are unaffected.
func (n *Node) resolvePeersFromState() {
	peerSetter, transportSupportsResolution := n.transport.(interface{ SetPeers([]string) })
	if !transportSupportsResolution {
		return
	}
	if resolvedAddresses := n.resolvedPeerAddresses(); resolvedAddresses != nil {
		peerSetter.SetPeers(resolvedAddresses)
	}
	// nil = nothing published on-chain yet — keep the configured bootstrap peers.
}

// resolvedPeerAddresses is the pure resolution: the gossip addresses of this node's Hypernode
// neighbours among the current validators that have published an on-chain address. Returns nil if
// none have (so the caller keeps the configured peers).
func (n *Node) resolvedPeerAddresses() []string {
	currentValidators := n.validatorsFor(n.accounts)
	addressByValidatorKey := make(map[string]string)
	var validatorKeysWithAddress []string
	for _, account := range n.accounts {
		if account.Address != "" && currentValidators.Contains(account.PublicKey) {
			addressByValidatorKey[account.PublicKey] = account.Address
			validatorKeysWithAddress = append(validatorKeysWithAddress, account.PublicKey)
		}
	}
	if len(validatorKeysWithAddress) == 0 {
		return nil
	}
	sort.Strings(validatorKeysWithAddress)
	var resolvedAddresses []string
	for _, validatorKey := range Neighbors(validatorKeysWithAddress, n.self, n.parameters.MaxPeers) {
		if address := addressByValidatorKey[validatorKey]; address != "" {
			resolvedAddresses = append(resolvedAddresses, address)
		}
	}
	return resolvedAddresses
}
