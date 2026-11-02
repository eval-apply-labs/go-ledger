package node

import (
	"fmt"
	"net"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The network is self-describing: a node resolves its gossip peers from the on-chain IP addresses of
// the current validators (the peer DNS), not from static config — and never resolves itself.
// publishedAccounts is the accounts of the three test validators self, peer-one and peer-two, each
// with its IP addresses: peer-one has two, the second an IPv6 address.
func publishedAccounts() []ledger.Account {
	var accounts []ledger.Account
	for name, ipAddresses := range map[string][]string{"self": {"10.0.0.1"}, "peer-one": {"10.0.0.2", "2001:db8::2"}, "peer-two": {"10.0.0.3"}} {
		account := testAccount(name, "1")
		account.IPAddresses = ipAddresses
		accounts = append(accounts, account)
	}
	return testGenesis(accounts, time.Second, 0).Accounts // in name order
}

// gossipChain is staticChain with the gossip port 8090.
func gossipChain(names ...string) ledger.Chain {
	chain := staticChain(names...)
	chain.GossipPort = 8090
	return chain
}

// A peer's URLs are "http://", each of its IP addresses in the order it lists them, and the gossip
// port, an IPv6 address in brackets (spec §4).
func TestResolvePeersFromOnChainAddresses(t *testing.T) {
	validatorNode := New(testConfig(gossipChain("self", "peer-one", "peer-two"), "self", testGenesis(publishedAccounts(), time.Second, 0)), NewHTTPTransport(nil))

	want := [][]string{{"http://10.0.0.2:8090", "http://[2001:db8::2]:8090"}, {"http://10.0.0.3:8090"}}
	if resolvedAddresses := validatorNode.resolvedPeerAddresses(); !reflect.DeepEqual(resolvedAddresses, want) {
		t.Fatalf("should resolve the two other validators' gossip URLs, and never its own: got %v, want %v", resolvedAddresses, want)
	}
}

// A node takes its peers from the validators' IP addresses as it opens, once every validator has one in
// the state it opens at (spec 8.2): on a maxPeers network a validator gossips to its neighbours in the
// topology, and with maxPeers 0 to every other validator. A node whose name is not a validator (an
// observer or a merchant's node) has no place in it, so it keeps its configured peers; it used to get
// the empty neighbour list, and accepted and receipted payments it never relayed (E41). Until every
// validator has an IP address, every node keeps its configured peers.
func TestResolvePeersAtOpen(t *testing.T) {
	names := []string{"bank-2", "bank-3", "bank-4", "founder"}
	var accounts []ledger.Account
	for i, name := range names {
		account := testAccount(name, "1")
		account.IPAddresses = []string{fmt.Sprintf("10.0.0.%d", i+1)}
		accounts = append(accounts, account)
	}
	configuredPeers := [][]string{{"http://configured:8080"}}
	peersAtOpen := func(self string, maxPeers int64, accounts []ledger.Account) [][]string {
		genesis := testGenesis(append(accounts, testAccount("observer", "1")), time.Second, 0)
		genesis.Parameters.MaxPeers = maxPeers
		transport := NewHTTPTransport([]string{"http://configured:8080"})
		New(testConfig(gossipChain(names...), self, genesis), transport)
		return transport.Peers()
	}

	if peers := peersAtOpen("observer", 2, accounts); !reflect.DeepEqual(peers, configuredPeers) {
		t.Errorf("observer peers = %v, want its configured peers", peers)
	}
	if peers := peersAtOpen("founder", 2, accounts); len(peers) != 2 || !strings.HasPrefix(peers[0][0], "http://10.0.0.") || !strings.HasPrefix(peers[1][0], "http://10.0.0.") {
		t.Errorf("validator peers = %v, want its 2 topology neighbours", peers)
	}
	if peers := peersAtOpen("founder", 0, accounts); len(peers) != 3 {
		t.Errorf("validator peers with maxPeers 0 = %v, want the other 3 validators", peers)
	}
	withoutAddress := slices.Clone(accounts)
	withoutAddress[2].IPAddresses = nil // one validator has no IP address yet
	if peers := peersAtOpen("founder", 2, withoutAddress); !reflect.DeepEqual(peers, configuredPeers) {
		t.Errorf("peers = %v while a validator has no address, want the configured peers", peers)
	}
}

// Until every current validator has published an IP address, a node keeps its configured peers, and
// it goes back to them when a validator's addresses disappear (it clears them).
func TestIncompletePeerResolutionFallsBackToConfiguredPeers(t *testing.T) {
	everyonePublished := publishedAccounts() // peer-one, peer-two, self
	peerTwoUnpublished := append([]ledger.Account(nil), everyonePublished...)
	peerTwoUnpublished[1].IPAddresses = nil
	configuredPeers := [][]string{{"http://configured-one:8080"}, {"http://configured-two:8080"}}
	transport := NewHTTPTransport([]string{"http://configured-one:8080", "http://configured-two:8080"})
	validatorNode := New(testConfig(gossipChain("self", "peer-one", "peer-two"), "self", testGenesis(peerTwoUnpublished, time.Second, 0)), transport)

	validatorNode.resolvePeersFromState()
	if peers := transport.Peers(); !reflect.DeepEqual(peers, configuredPeers) {
		t.Fatalf("with a validator's address missing the configured peers must stay, got %v", peers)
	}
	validatorNode.state.Accounts = everyonePublished
	validatorNode.resolvePeersFromState()
	if resolvedPeers := transport.Peers(); !reflect.DeepEqual(resolvedPeers, [][]string{{"http://10.0.0.2:8090", "http://[2001:db8::2]:8090"}, {"http://10.0.0.3:8090"}}) {
		t.Fatalf("with every address published the resolved peers must be used, got %v", resolvedPeers)
	}
	validatorNode.state.Accounts = peerTwoUnpublished // peer two's address is gone
	validatorNode.resolvePeersFromState()
	if peers := transport.Peers(); !reflect.DeepEqual(peers, configuredPeers) {
		t.Fatalf("once an address disappears the configured peers must be restored, got %v", peers)
	}
}

// listenOnOnePort opens a listener on each of count loopback addresses, from 127.0.0.1 up, all on one
// free port: validators gossip on one port, each at its own IP address (spec §4). It returns the
// listeners and the port.
func listenOnOnePort(t *testing.T, count int) ([]net.Listener, int) {
	t.Helper()
	for range 10 { // another program may hold the port at one of the other addresses: then try another port
		first, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := first.Addr().(*net.TCPAddr).Port
		listeners := []net.Listener{first}
		for i := 2; i <= count; i++ {
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.%d:%d", i, port))
			if err != nil {
				break
			}
			listeners = append(listeners, listener)
		}
		if len(listeners) == count {
			return listeners, port
		}
		for _, listener := range listeners {
			listener.Close()
		}
	}
	t.Fatal("no port was free at every loopback address")
	return nil, 0
}

// startPeerDNSNetwork runs validatorCount validators of equal power over real HTTP, wired all-to-all by
// configuration, each with a genesis account. Validator i listens at 127.0.0.<i+1>, every one on the
// network's gossip port, and the accounts for which publish(i) holds carry their node's IP address on
// chain. It returns the nodes and their names.
func startPeerDNSNetwork(t *testing.T, validatorCount int, publish func(int) bool) ([]*Node, []string) {
	t.Helper()
	names := validatorNames(validatorCount)
	listeners, gossipPort := listenOnOnePort(t, validatorCount)
	chain := staticChain(names...)
	chain.GossipPort = gossipPort
	servers := make([]*httptest.Server, validatorCount)
	urls := make([]string, validatorCount)
	var accounts []ledger.Account
	for i, name := range names {
		servers[i] = httptest.NewUnstartedServer(nil)
		servers[i].Listener.Close()
		servers[i].Listener = listeners[i]
		urls[i] = "http://" + listeners[i].Addr().String()
		account := testAccount(name, "100")
		if publish(i) {
			account.IPAddresses = []string{listeners[i].Addr().(*net.TCPAddr).IP.String()}
		}
		accounts = append(accounts, account)
	}
	genesis := testGenesis(accounts, 400*time.Millisecond, 40*time.Millisecond)
	nodes := make([]*Node, validatorCount)
	for i := range nodes {
		var peers []string
		for j, nodeURL := range urls {
			if j != i {
				peers = append(peers, nodeURL)
			}
		}
		nodes[i] = New(testConfig(chain, names[i], genesis), NewHTTPTransport(peers))
		servers[i].Config.Handler = NewServer(nodes[i]).Handler()
		servers[i].Start()
	}
	for _, node := range nodes {
		node.Start()
	}
	t.Cleanup(func() {
		for i := range nodes {
			nodes[i].Stop()
			servers[i].Close()
		}
	})
	keepBusy(t, names[0], nodes...)
	return nodes, names
}

func allReach(nodes []*Node, height int64) func() bool {
	return func() bool {
		for _, node := range nodes {
			if node.Snapshot().Height < height {
				return false
			}
		}
		return true
	}
}

// With addresses published by only some validators, the network keeps committing. Resolving among the
// publishers alone had them gossip only to each other while the rest still gossiped to everyone, and
// from the first commit on no block could reach a quorum.
func TestPartialPeerAddressesDoNotPartitionNetwork(t *testing.T) {
	nodes, _ := startPeerDNSNetwork(t, 3, func(i int) bool { return i < 2 })
	waitFor(t, 10*time.Second, allReach(nodes, 6), "the network stopped committing with only some validators' addresses published")
}

// A validator that clears its on-chain IP addresses (an update with "-") leaves the network without a
// complete address book. The rest of the network keeps committing instead of splitting into the
// validators that still have an address, gossiping among themselves, and those that no longer hear
// them.
func TestValidatorLosingItsAddressDoesNotPartitionNetwork(t *testing.T) {
	nodes, names := startPeerDNSNetwork(t, 3, func(int) bool { return true })
	leaver := names[2]
	waitFor(t, 10*time.Second, allReach(nodes, 2), "the network did not start") // each node resolved every peer as it opened

	waitFor(t, 10*time.Second, func() bool {
		if account, _ := ledger.FindAccount(nodes[0].Snapshot().Accounts, leaver); len(account.IPAddresses) == 0 {
			return true
		}
		// (Re)submit the update for a near block; while an attempt is still pending it is refused.
		_, _ = nodes[2].AdmitUpdate(signedUpdate(ledger.AccountUpdate{Name: leaver, IPAddresses: []string{ledger.ClearField}, BlockID: nodes[2].Snapshot().Height + 3}))
		return false
	}, "the validator's cleared address did not commit")
	cleared := nodes[0].Snapshot().Height
	waitFor(t, 10*time.Second, allReach(nodes, cleared+5), "the network stopped committing once a validator's address disappeared")
}
