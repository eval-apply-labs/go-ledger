package ledger

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// CanonicalAddress reports whether text is a public address in its one plain form: "http://" or
// "https://", a host, and an optional ":port", with nothing after it, for example
// "https://bank-a.example". An account's PublicAddress is "" or such an address; ClearField in an
// update sets it to "". Wallets ask it how far the chain has gone, so it must name a server and
// nothing more: no user info, no path (not even "/"), no query and no fragment, which would make
// another request of it, to any URL the wallet can reach (spec 4.2).
//
// It is written out by hand, not left to a URL parser, because a block's validity depends on it: every
// node must take the same addresses, whichever Go release built it. The host is a host name in
// lowercase (labels of 1 to 63 letters, digits and hyphens, not starting or ending with a hyphen,
// joined by dots; an IPv4 address is one) or an IPv6 address in brackets, of hexadecimal digits, colons
// and dots, with no zone. So an address has one spelling, as a name does (CanonicalName): "http://Bank-A"
// is refused and "http://bank-a" taken. A port is a number from 1 to 65535 with no leading zero, and
// "http://host:", an empty port, is refused. An explicit default port, "http://host:80", is taken: it
// names the same node as "http://host", and nothing compares addresses.
func CanonicalAddress(text string) bool {
	rest, isHTTP := strings.CutPrefix(text, "http://")
	if !isHTTP {
		var isHTTPS bool
		if rest, isHTTPS = strings.CutPrefix(text, "https://"); !isHTTPS {
			return false
		}
	}
	host, port, hasPort := strings.Cut(rest, ":")
	if strings.HasPrefix(rest, "[") { // an IPv6 address: its colons are inside the brackets
		literal, afterBracket, closed := strings.Cut(rest[1:], "]")
		if !closed || !ipv6Literal(literal) {
			return false
		}
		host, port, hasPort = "["+literal+"]", strings.TrimPrefix(afterBracket, ":"), afterBracket != ""
		if hasPort && !strings.HasPrefix(afterBracket, ":") {
			return false
		}
	} else if !hostName(host) {
		return false
	}
	return !hasPort || canonicalPort(port)
}

// hostName reports whether host is 1 to 253 characters of dot-joined labels, each 1 to 63 lowercase
// letters, digits and hyphens, not starting or ending with a hyphen.
func hostName(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			if !(label[i] >= 'a' && label[i] <= 'z' || label[i] >= '0' && label[i] <= '9' || label[i] == '-') {
				return false
			}
		}
	}
	return true
}

// ipv6Literal reports whether literal, the text between the brackets, is 2 to 45 lowercase hexadecimal
// digits, colons and dots with at least one colon: an IPv6 address's characters, with no zone.
func ipv6Literal(literal string) bool {
	if len(literal) < 2 || len(literal) > 45 || !strings.Contains(literal, ":") {
		return false
	}
	for i := 0; i < len(literal); i++ {
		if !(literal[i] >= '0' && literal[i] <= '9' || literal[i] >= 'a' && literal[i] <= 'f' || literal[i] == ':' || literal[i] == '.') {
			return false
		}
	}
	return true
}

// canonicalPort reports whether port is a number from 1 to 65535 with no leading zero.
func canonicalPort(port string) bool {
	number, err := strconv.Atoi(port)
	return err == nil && number >= 1 && number <= 65535 && strconv.Itoa(number) == port
}

// addressRule is the form CanonicalAddress takes, as a refusal names it.
const addressRule = "http:// or https://, a host in lowercase and an optional :port, with nothing after it, for example http://10.0.0.2:8080"

// MaxIPAddresses is the most IP addresses a name publishes, as a DNS name holds a few A and AAAA
// records: enough for a bank's main line, a second line and their IPv6 addresses.
const MaxIPAddresses = 4

// CanonicalIPAddress reports whether text is an IP address in its one plain form, exactly as
// netip.Addr's String writes it: "10.0.0.2" or "2001:db8::1", never "010.0.0.2", "2001:DB8::1" or
// "2001:db8:0::1". An IPv4 address is never written as IPv6 ("::ffff:10.0.0.2"), and no address has a
// zone ("fe80::1%eth0"), which names an interface of one machine. So an address has one spelling, and
// two equal addresses are equal strings. The unspecified addresses ("0.0.0.0", "::") and multicast
// addresses are refused: neither names one machine. Private and loopback addresses are taken, so a
// network on a LAN, or on one machine, works.
//
// A block's validity depends on it, as on CanonicalAddress, and here a parser is used: the plain forms
// are fixed by standards (dotted decimal, and RFC 5952 for IPv6), which netip follows, so every Go
// release takes the same addresses.
func CanonicalIPAddress(text string) bool {
	address, err := netip.ParseAddr(text)
	return err == nil && address.String() == text && address.Zone() == "" && !address.Is4In6() &&
		!address.IsUnspecified() && !address.IsMulticast()
}

// ipAddressRule is the form CanonicalIPAddress takes, as a refusal names it.
const ipAddressRule = "an IPv4 address such as 10.0.0.2, or an IPv6 address in its shortest lowercase form such as 2001:db8::1; " +
	"no zone, no IPv4 address written as IPv6, no 0.0.0.0 or ::, no multicast address"

// ipAddressesError says why a list of IP addresses can't be a name's, or is nil: at most
// MaxIPAddresses, each CanonicalIPAddress, none twice. An empty list is a name's too: it has none.
func ipAddressesError(ipAddresses []string) error {
	if len(ipAddresses) > MaxIPAddresses {
		return fmt.Errorf("%d IP addresses, above the %d a name may publish", len(ipAddresses), MaxIPAddresses)
	}
	for i, ipAddress := range ipAddresses {
		if !CanonicalIPAddress(ipAddress) {
			return fmt.Errorf("the IP address %q is not an IP address: %s", ipAddress, ipAddressRule)
		}
		if slices.Contains(ipAddresses[:i], ipAddress) {
			return fmt.Errorf("the IP address %q is given twice", ipAddress)
		}
	}
	return nil
}
