package node

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// fakeConnection is a connection between fixed addresses that notes whether it was read while open: the
// allow list must close a refused one before it reads a byte of it.
type fakeConnection struct { // implements net.Conn
	net.Conn      // nil: the allow list calls only the methods below
	remoteAddress *net.TCPAddr
	localAddress  *net.TCPAddr
	readWhileOpen bool
	closed        bool
}

// connectionFrom is a fakeConnection from address, as a socket reports it: an IPv4 address in four
// bytes, and one in IPv6 dress (::ffff:a.b.c.d) in sixteen, as a dual-stack socket reports an IPv4
// caller. It comes in at the fake listener's address.
func connectionFrom(address string) *fakeConnection {
	return &fakeConnection{remoteAddress: net.TCPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr(address), 40000)),
		localAddress: (&fakeListener{}).Addr().(*net.TCPAddr)}
}

func (connection *fakeConnection) RemoteAddr() net.Addr { return connection.remoteAddress }
func (connection *fakeConnection) LocalAddr() net.Addr  { return connection.localAddress }
func (connection *fakeConnection) Close() error         { connection.closed = true; return nil }
func (connection *fakeConnection) Read([]byte) (int, error) {
	connection.readWhileOpen = connection.readWhileOpen || !connection.closed
	return 0, io.EOF
}

// fakeListener hands out the connections queued in it, in order, and then reports itself closed.
type fakeListener struct { // implements net.Listener
	net.Listener // nil: the allow list calls only the methods below
	connections  []net.Conn
}

func (listener *fakeListener) Addr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort("192.0.2.1:8070"))
}

func (listener *fakeListener) Accept() (net.Conn, error) {
	if len(listener.connections) == 0 {
		return nil, net.ErrClosed
	}
	connection := listener.connections[0]
	listener.connections = listener.connections[1:]
	return connection, nil
}

// letsIn reports whether node's allow list lets in a connection from address.
func letsIn(node *Node, address string) bool {
	_, err := NewAllowListListener(&fakeListener{connections: []net.Conn{connectionFrom(address)}}, node).Accept()
	return err == nil
}

// The firewall inside the container (spec §5.1, §5.3): the gossip port lets in a connection from an
// IP address of another current validator, or one that a prefix of the node's allow entries holds, and
// closes any other before it reads a byte, then goes on to the next. A name that publishes addresses but
// holds no seat is not let in. An IPv4 address in IPv6 dress counts as the IPv4 address. The refusals
// are counted, and reported in one log line. The node, bank-a, publishes no address here.
func TestAllowListLetsInOnlyListedAddresses(t *testing.T) {
	bankA, bankB, bankC, shop := testAccount("bank-a", "100"), testAccount("bank-b", "100"), testAccount("bank-c", "100"), testAccount("shop", "100")
	bankB.IPAddresses, bankC.IPAddresses, shop.IPAddresses = []string{"10.0.0.2"}, []string{"10.0.0.1", "2001:db8::1"}, []string{"10.0.0.9"}
	config := testConfig(staticChain("bank-a", "bank-b", "bank-c"), "bank-a", testGenesis([]ledger.Account{bankA, bankB, bankC, shop}, time.Second, 0))
	config.GossipAllow = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8:1::/48"), netip.MustParsePrefix("203.0.113.7/32")}
	node := New(config, NewHTTPTransport(nil))

	for address, wantLetIn := range map[string]bool{
		"10.0.0.1":             true, // the validators' addresses
		"2001:db8::1":          true,
		"10.0.0.2":             true,
		"::ffff:10.0.0.2":      true, // in IPv6 dress
		"198.51.100.77":        true, // the configured prefixes
		"::ffff:198.51.100.77": true,
		"2001:db8:1:5::1":      true,
		"203.0.113.7":          true,
		"10.0.0.9":             false, // the shop's: a name, not a validator
		"10.0.0.3":             false,
		"::ffff:10.0.0.3":      false,
		"2001:db8::2":          false,
		"203.0.113.8":          false,
		"198.51.101.1":         false,
		"::a00:2":              false, // 10.0.0.2's bits in an IPv6 address, but not in IPv4 dress
	} {
		connection := connectionFrom(address)
		accepted, err := NewAllowListListener(&fakeListener{connections: []net.Conn{connection}}, node).Accept()
		switch {
		case wantLetIn && (err != nil || accepted.(*allowListConnection).Conn != connection || connection.closed):
			t.Errorf("a connection from %s was not let in: %v", address, err)
		case !wantLetIn && (err != net.ErrClosed || !connection.closed || connection.readWhileOpen):
			t.Errorf("a connection from %s was not closed unread: accepted %v, closed %t, read %t", address, err == nil, connection.closed, connection.readWhileOpen)
		}
	}

	refused, listed := []*fakeConnection{connectionFrom("10.0.0.3"), connectionFrom("10.0.0.9")}, connectionFrom("10.0.0.2")
	listener := NewAllowListListener(&fakeListener{connections: []net.Conn{refused[0], refused[1], listed}}, node).(*allowListListener)
	if accepted, err := listener.Accept(); err != nil || accepted.(*allowListConnection).Conn != listed || !refused[0].closed || !refused[1].closed {
		t.Fatalf("Accept did not close the two strangers and go on to the validator: %v", err)
	}
	logged := captureLog(t)
	listener.reportRefusals()
	if !strings.Contains(logged.String(), "refused 2 connections") || listener.refusals.Load() != 0 {
		t.Errorf("the report did not log the 2 refusals and start the count again: %q", logged.String())
	}
}

// The list leaves out the node's own addresses, and loopback addresses, from the chain (review N1): a
// front that rewrites every caller to the engine's own address, or to loopback, then lets nobody in,
// and the refusal report shows it, instead of letting everybody in. The node's own address stays out
// when another validator publishes it too. An allow entry is kept, a loopback one too.
func TestAllowListLeavesOutTheNodesOwnAndLoopbackAddresses(t *testing.T) {
	bankA, bankB := testAccount("bank-a", "100"), testAccount("bank-b", "100")
	bankA.IPAddresses = []string{"10.0.0.1", "2001:db8::1"}
	bankB.IPAddresses = []string{"10.0.0.2", "127.0.0.2", "::1", "10.0.0.1"}
	config := testConfig(staticChain("bank-a", "bank-b"), "bank-a", testGenesis([]ledger.Account{bankA, bankB}, time.Second, 0))
	config.GossipAllow = []netip.Prefix{netip.MustParsePrefix("127.0.0.5/32")}
	node := New(config, NewHTTPTransport(nil))

	for address, wantLetIn := range map[string]bool{
		"10.0.0.1":        false, // the node's own, which bank-b publishes too
		"::ffff:10.0.0.1": false,
		"2001:db8::1":     false, // the node's own
		"127.0.0.2":       false, // bank-b's, but loopback
		"::1":             false,
		"10.0.0.2":        true, // bank-b's
		"127.0.0.5":       true, // the allow entry
	} {
		if letsIn(node, address) != wantLetIn {
			t.Errorf("a connection from %s: let in %t, want %t", address, !wantLetIn, wantLetIn)
		}
	}

	// A front that rewrites every caller to the engine's own address: every connection is refused, and
	// the report says how many.
	var rewritten []net.Conn
	for range 3 {
		rewritten = append(rewritten, connectionFrom("10.0.0.1"))
	}
	listener := NewAllowListListener(&fakeListener{connections: rewritten}, node).(*allowListListener)
	if _, err := listener.Accept(); err != net.ErrClosed {
		t.Fatalf("a connection from the engine's own address was let in: %v", err)
	}
	logged := captureLog(t)
	listener.reportRefusals()
	if !strings.Contains(logged.String(), "refused 3 connections") {
		t.Errorf("the report does not show the 3 refused connections: %q", logged.String())
	}
}

// A forwarder inside the engine's own network namespace connects from the address it connects to
// (review N14): rootless Podman's port forwarding (slirp4netns) makes every caller 10.0.2.100, the
// container's own address there, and a userland proxy every caller the address it listens on. No real
// peer connects that way, there being one engine per IP address, so the allow list refuses such a
// connection, and counts it, even when a validator publishes the address: otherwise that one validator
// would open the bank to everyone. A socket may report either end in IPv6 dress.
func TestAllowListRefusesAConnectionFromItsOwnLocalAddress(t *testing.T) {
	bankA, bankB := testAccount("bank-a", "100"), testAccount("bank-b", "100")
	bankB.IPAddresses = []string{"10.0.2.100"} // the forwarder's address, which bank-b publishes
	node := New(testConfig(staticChain("bank-a", "bank-b"), "bank-a", testGenesis([]ledger.Account{bankA, bankB}, time.Second, 0)), NewHTTPTransport(nil))
	for _, test := range []struct {
		remoteAddress, localAddress string
		wantLetIn                   bool
	}{
		{"10.0.2.100", "10.0.2.15", true}, // bank-b, at the engine's own address
		{"10.0.2.100", "10.0.2.100", false},
		{"10.0.2.100", "::ffff:10.0.2.100", false},
		{"::ffff:10.0.2.100", "10.0.2.100", false},
	} {
		connection := connectionFrom(test.remoteAddress)
		connection.localAddress = net.TCPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr(test.localAddress), 8070))
		listener := NewAllowListListener(&fakeListener{connections: []net.Conn{connection}}, node).(*allowListListener)
		_, err := listener.Accept()
		if letIn := err == nil; letIn != test.wantLetIn || listener.refusals.Load() != map[bool]int64{true: 0, false: 1}[test.wantLetIn] || connection.readWhileOpen {
			t.Errorf("a connection from %s to %s: let in %t, want %t; refusals counted %d; read while open %t",
				test.remoteAddress, test.localAddress, letIn, test.wantLetIn, listener.refusals.Load(), connection.readWhileOpen)
		}
	}
}

// The list follows the chain (spec §5.2): a commit that seats a validator lets its addresses in, and one
// that changes them lets in the new and no longer the old. A connection let in from the old address
// before is closed at its next read, so a validator that keeps its connection open is shut out too.
func TestAllowListFollowsTheCommits(t *testing.T) {
	founder, newcomer := selfDelegated("founder", "1000"), selfDelegated("newcomer", "0")
	founder.IPAddresses, newcomer.IPAddresses = []string{"10.0.0.1"}, []string{"10.0.0.2"}
	node := New(testConfig(dynamicChain("100"), "founder", testGenesis([]ledger.Account{founder, newcomer}, 5*time.Second, 20*time.Millisecond)), NewHTTPTransport(nil))
	if letsIn(node, "10.0.0.1") || letsIn(node, "10.0.0.2") {
		t.Fatal("at genesis the list must hold neither the founder's address, the node's own, nor the newcomer's: it holds no seat")
	}
	node.SubmitPayment(signedPayment("founder", "newcomer", "200", 1)) // its stake passes the threshold
	node.Start()
	defer node.Stop()
	waitFor(t, 5*time.Second, func() bool { return letsIn(node, "10.0.0.2") }, "the commit that seated the newcomer did not let its address in")

	held := connectionFrom("10.0.0.2")
	heldConnection, err := NewAllowListListener(&fakeListener{connections: []net.Conn{held}}, node).Accept()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		if letsIn(node, "10.0.0.3") {
			return true
		}
		_, _ = node.AdmitUpdate(signedUpdate(ledger.AccountUpdate{Name: "newcomer", IPAddresses: []string{"10.0.0.3"}, BlockID: node.Snapshot().Height + 2}))
		return false
	}, "the commit that changed the newcomer's address did not let the new one in")
	if letsIn(node, "10.0.0.2") {
		t.Error("the newcomer's old address is still let in")
	}
	if _, err := heldConnection.Read(make([]byte, 1)); err != net.ErrClosed || !held.closed {
		t.Errorf("a connection held open from the old address read on (%v) instead of closing", err)
	}
}

// End to end (spec §10): four validators on 127.0.0.1 to 127.0.0.4, each serving only the gossip routes
// at its own address, behind its allow list, and sending from that address. Each publishes its address
// on chain, which routes the gossip to it, but the list leaves loopback addresses from the chain out
// (review N1), so every node's allow entries name the addresses of the first three. The fourth, whose
// address only the chain lists, is refused by the three: none of its votes counts in their commits, and
// they, above ⅔, commit without it. The fourth refuses no connection: the three send from their
// listed addresses.
func TestAllowListEndToEnd(t *testing.T) {
	const validatorCount = 4
	names := validatorNames(validatorCount)
	listeners, gossipPort := listenOnOnePort(t, validatorCount)
	chain := staticChain(names...)
	chain.GossipPort = gossipPort
	var accounts []ledger.Account
	var allowEntries []netip.Prefix
	nodeURLs := make([]string, validatorCount)
	for i, name := range names {
		address := netip.AddrFrom4([4]byte{127, 0, 0, byte(i + 1)})
		account := testAccount(name, "100")
		account.IPAddresses = []string{address.String()}
		accounts = append(accounts, account)
		if i < validatorCount-1 {
			allowEntries = append(allowEntries, netip.PrefixFrom(address, address.BitLen()))
		}
		nodeURLs[i] = "http://" + listeners[i].Addr().String()
	}
	genesis := testGenesis(accounts, 400*time.Millisecond, 40*time.Millisecond)
	nodes := make([]*Node, validatorCount)
	gossipListeners := make([]*allowListListener, validatorCount)
	servers := make([]*http.Server, validatorCount)
	for i := range nodes {
		// The configured peers are the same URLs as the resolved ones, which replace them at Open
		// (resolvePeersFromState).
		transport := NewHTTPTransport(slices.Delete(slices.Clone(nodeURLs), i, i+1))
		sendingAddress := &net.TCPAddr{IP: net.ParseIP(fmt.Sprintf("127.0.0.%d", i+1))}
		transport.client.Transport = &http.Transport{DialContext: (&net.Dialer{LocalAddr: sendingAddress}).DialContext}
		config := testConfig(chain, names[i], genesis)
		config.GossipAllow = allowEntries
		nodes[i] = New(config, transport)
		gossipListeners[i] = NewAllowListListener(listeners[i], nodes[i]).(*allowListListener)
		servers[i] = &http.Server{Handler: NewServer(nodes[i]).GossipHandler()}
		go servers[i].Serve(gossipListeners[i])
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

	unlisted, listed := names[3], nodes[:3]
	waitFor(t, 15*time.Second, func() bool {
		for i, node := range listed {
			if commit := node.Snapshot().LastCommit; slices.ContainsFunc(commit.Votes, func(vote consensus.Vote) bool { return vote.Validator == unlisted }) {
				t.Fatalf("validator %d counted the precommit of %s at height %d: its address is loopback, and only the chain lists it", i, unlisted, commit.Height)
			}
		}
		return allReach(listed, 6)()
	}, "the three validators the allow entries list, above ⅔, did not commit")
	for i, listener := range gossipListeners[:3] {
		if listener.refusals.Load() == 0 {
			t.Errorf("validator %d let in the connections of %s, whose loopback address only the chain lists", i, unlisted)
		}
	}
	if refusals := gossipListeners[3].refusals.Load(); refusals != 0 {
		t.Errorf("validator 3 refused %d connections, all from the addresses its allow entries list", refusals)
	}
}
