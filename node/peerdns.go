package node

// resolvePeersFromState makes the network SELF-DESCRIBING — the on-chain peer DNS. It sets the
// transport's gossip peers from the committed addresses of the CURRENT validators (each validator
// publishes its address via an account update), applying the Hypernode topology over them, so the
// committed state IS the address book. It is all or nothing: until EVERY current validator has an
// address, the configured peers stay in use. Resolving among only the validators that have published
// would have them gossip to each other alone while the rest still gossip to everyone, and the network
// can then lose its quorum for good. The configured peers are saved when a resolution is first applied
// and restored whenever resolution stops being complete: a validator's account, and with it its
// address, can disappear. Only HTTPTransport has both SetPeers and Peers. For any other transport
// (the in-memory test networks) it is a no-op, so tests are unaffected.
func (node *Node) resolvePeersFromState() {
	transport, transportSupportsResolution := node.transport.(interface {
		SetPeers([]string)
		Peers() []string
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

// resolvedPeerAddresses is the pure resolution: the gossip addresses of this node's Hypernode
// neighbours among the current validators, by name, once every one of them has published an on-chain
// address. Returns nil until then (so the caller uses the configured peers).
func (node *Node) resolvedPeerAddresses() []string {
	currentValidators := node.view.Load().validators
	addressByValidatorName := make(map[string]string)
	var validatorNamesWithAddress []string // in name order, as the accounts are
	for _, account := range node.state.Accounts {
		if account.Address != "" && currentValidators.Contains(account.Name) {
			addressByValidatorName[account.Name] = account.Address
			validatorNamesWithAddress = append(validatorNamesWithAddress, account.Name)
		}
	}
	if len(validatorNamesWithAddress) == 0 || len(validatorNamesWithAddress) < len(currentValidators.Validators()) {
		return nil
	}
	var resolvedAddresses []string
	for _, validatorName := range Neighbors(validatorNamesWithAddress, node.self, int(node.state.Parameters.MaxPeers)) {
		resolvedAddresses = append(resolvedAddresses, addressByValidatorName[validatorName])
	}
	return resolvedAddresses
}
