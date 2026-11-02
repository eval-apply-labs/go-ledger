package node

import (
	"log"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// gossipAllowList is who may connect to the gossip port (spec §5.1): every IP address of every
// validator of validators, the set of the height in progress, as accounts publish them, and the
// prefixes configured in Config.GossipAllow. Unlike the peer DNS it does not wait for every validator
// to publish: one that has is let in at once. It looks up no name: the chain is the routing table, and
// DNS would be one more thing a provider can cut or poison. publishView computes it, on the loop, at
// Open and after each commit and catch-up.
//
// It leaves out two kinds of address the chain lists: those the node's own account, selfName, publishes,
// even where another validator publishes them too, and loopback addresses. The list trusts the address
// the socket reports, so the gossip port must see each caller's own address. Something in front that
// rewrites it, such as a userland port forwarder or a TLS or TCP proxy, makes every caller one address,
// most often the engine's own or a loopback address. Were those on the list, everyone would get in;
// left out, every caller is refused, and the refusal report says so (reportRefusals). A validator could
// also publish a loopback address to put it on every other bank's list. A forwarder inside the engine's
// own network namespace, such as rootless Podman's slirp4netns, makes every caller the container's own
// address there, 10.0.2.100, which another validator may publish: the listener refuses it all the same,
// as it refuses every connection whose two ends are one address (see Accept). An entry the operator
// configures is kept as it is, a loopback one too: a test that runs nodes on 127.0.0.x lists them there,
// and so does a bank for another bank that reaches it from the bank's own address, behind one NAT.
func gossipAllowList(accounts []ledger.Account, validators *consensus.ValidatorSet, selfName string, configuredPrefixes []netip.Prefix) []netip.Prefix {
	allowList := slices.Clone(configuredPrefixes)
	ownAccount, _ := ledger.FindAccount(accounts, selfName)
	for _, validator := range validators.Validators() {
		account, _ := ledger.FindAccount(accounts, validator.Name)
		for _, ipAddress := range account.IPAddresses {
			// The ledger takes an address only in its one plain form (ledger.CanonicalIPAddress), so it
			// parses, and the same address is the same string in every account.
			if address, err := netip.ParseAddr(ipAddress); err == nil && !address.IsLoopback() && !slices.Contains(ownAccount.IPAddresses, ipAddress) {
				allowList = append(allowList, netip.PrefixFrom(address, address.BitLen()))
			}
		}
	}
	return allowList
}

// allowListListener is the firewall inside the container (spec §5.3, R2). It stands in front of the
// gossip port's http.Server, and lets in only a connection whose remote address is on the allow list
// the node last published (committedView.gossipAllowList). It closes any other at once, before reading
// a byte of it, so no HTTP is parsed for a stranger, and goes on to the next. It needs no privilege and
// no tool in the image. It is a cost filter, not authentication: the signatures are (spec §5.5).
type allowListListener struct { // implements net.Listener
	net.Listener              // the gossip port's own listener, whose Close and Addr this one keeps
	node         *Node        // whose committed view holds the allow list
	refusals     atomic.Int64 // the connections refused since the last report (see reportRefusals)
}

// The compiler checks that *allowListListener implements net.Listener.
var _ net.Listener = (*allowListListener)(nil)

// NewAllowListListener puts listener, the gossip port's, behind node's allow list. cmd/server serves
// the gossip routes (Server.GossipHandler) on what it returns.
func NewAllowListListener(listener net.Listener, node *Node) net.Listener {
	return &allowListListener{Listener: listener, node: node}
}

// refusalReportInterval is how often the gossip port logs the connections it refused, while it refuses
// any.
const refusalReportInterval = time.Minute

// Accept returns the next connection from an address on the allow list, unless its remote address is its
// local one (bothEndsOneAddress). It closes every other one as it comes, and counts it. The first refusal
// after a report schedules the next report, so strangers cost one log line a minute however many
// connections they open.
func (listener *allowListListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if tcpAddress, isTCP := connection.RemoteAddr().(*net.TCPAddr); isTCP && listener.allows(tcpAddress.AddrPort().Addr()) && !bothEndsOneAddress(connection) {
			return &allowListConnection{Conn: connection, listener: listener, remoteAddress: tcpAddress.AddrPort().Addr()}, nil
		}
		connection.Close()
		if listener.refusals.Add(1) == 1 {
			time.AfterFunc(refusalReportInterval, listener.reportRefusals)
		}
	}
}

// allows reports whether address is on the allow list the node last published. An IPv4 address in IPv6
// dress (::ffff:a.b.c.d), which is how a dual-stack socket reports an IPv4 caller, counts as the IPv4
// address (spec §5.1), and a zone, which no entry has, is dropped.
func (listener *allowListListener) allows(address netip.Addr) bool {
	address = address.WithZone("").Unmap()
	return slices.ContainsFunc(listener.node.view.Load().gossipAllowList, func(prefix netip.Prefix) bool { return prefix.Contains(address) })
}

// bothEndsOneAddress reports whether connection's remote IP address is its local one, an IPv4 address in
// IPv6 dress counting as the IPv4 address. No real peer connects that way: there is one engine per IP
// address (spec §13, Q13), so two engines never share one. What does is the engine's own machine: a
// forwarder inside the engine's own network namespace, such as rootless Podman's slirp4netns at
// 10.0.2.100, which every caller then seems to come from (review N14), or the node itself, reaching its
// own machine through one of its own addresses (see dialPublicAddress). A forwarder outside that
// namespace is not caught: Docker's userland proxy makes every caller the bridge's gateway, such as
// 172.17.0.1, which is no address of the engine's. With Docker, the gossip port must be published
// through iptables (DNAT), its default, which keeps the caller's address, and no allow entry may name
// a bridge's gateway.
func bothEndsOneAddress(connection net.Conn) bool {
	localAddress, localIsTCP := connection.LocalAddr().(*net.TCPAddr)
	remoteAddress, remoteIsTCP := connection.RemoteAddr().(*net.TCPAddr)
	return localIsTCP && remoteIsTCP && localAddress.AddrPort().Addr().Unmap() == remoteAddress.AddrPort().Addr().Unmap()
}

// reportRefusals logs how many connections the allow list refused since the last report, and starts
// the count again. It is how an operator finds a bank whose connections come from an address it does
// not publish (spec §5.4).
func (listener *allowListListener) reportRefusals() {
	log.Printf("gossip port %s: refused %d connections in the last minute from addresses not on the allow list", listener.Addr(), listener.refusals.Swap(0))
}

// allowListConnection is a connection the allow list let in. HTTP keeps a connection open from one
// request to the next, and a validator that sends all the time never needs a new one, so the address
// is checked again after each read. Once it has left the list (its validator left the set, or
// published other addresses), the connection is closed, and what was read is dropped unparsed.
type allowListConnection struct { // implements net.Conn
	net.Conn
	listener      *allowListListener
	remoteAddress netip.Addr
}

// The compiler checks that *allowListConnection implements net.Conn.
var _ net.Conn = (*allowListConnection)(nil)

// Read reads as the connection does, then closes the connection if its address has left the allow
// list since it was let in.
func (connection *allowListConnection) Read(buffer []byte) (int, error) {
	count, err := connection.Conn.Read(buffer)
	if !connection.listener.allows(connection.remoteAddress) {
		connection.Conn.Close()
		return 0, net.ErrClosed
	}
	return count, err
}
