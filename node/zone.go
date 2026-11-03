package node

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The name service (spec §3.2, §3.4) is two kinds of file in the public folder (publicfolder.go):
//
//   - hdls/zone, the zone file: the names' records in the standard text format of DNS (RFC 1035, "master
//     file"), which any DNS server (NSD, Knot, BIND, CoreDNS) loads as an authoritative zone; written
//     only with a zone setting (ZoneSettings), and only when more than its serial changed (publishZone);
//   - hdls/resolve/ip/<address>, the IP lookup: the names that list the address, as a JSON list, written
//     when a commit or a catch-up changes it and removed when no name lists the address any more.
//
// Both follow from the committed accounts (the zone also from the validator set, which follows from them
// and the genesis, and from the zone settings), so anyone can rebuild them from the public hdls/accounts
// and compare: an IP lookup file byte for byte, the zone file apart from its SOA serial. The serial is
// the height at which this node last wrote the zone, not one the accounts tell: a commit that changes
// none of its records leaves the file as it is (publishZone). An IP address a name lists is a claim:
// nothing checks that the name controls it, and every name that lists an address is shown, a frozen one
// as "<name> (frozen)".

// The name service's paths, relative to Config.PublicPath.
const (
	zonePath       = "hdls/zone"
	ipLookupFolder = "hdls/resolve/ip"
)

// validatorsLabel is the owner, in the zone, of the validators' addresses (spec R1). It is a host name,
// letters, digits and hyphens, as BIND requires by default of the owner of an A or AAAA record
// (check-names), and is 36 characters, more than the 32 a name may have (ledger.CanonicalName), so no
// name can take it.
const validatorsLabel = "validators-of-the-height-in-progress"

// maxZoneClaimants is the most names the zone's _ip lookup gives for one address. At most 42 bytes each,
// they keep the address's one record some 4 kB, well within the 65535 bytes DNS allows a record's data.
const maxZoneClaimants = 100

// DefaultZoneTimeToLive is the zone's time to live, in seconds, when the settings give none.
const DefaultZoneTimeToLive = 60

// ZoneSettings are the zone settings of the node's config (spec §3.2). Without a Zone the node writes no
// zone file. cmd/server checks them; Zone takes them as they are.
type ZoneSettings struct {
	Zone            string   // the zone's domain name, without the final dot, such as "hdls.example.org"; "" for no zone file
	ZoneNameServers []string // the domain names of the zone's name servers, at least one, its NS records; the first is the SOA's primary
	ZoneContact     string   // the SOA mailbox as a domain name: "hostmaster.example.org" for hostmaster@example.org
	ZoneTimeToLive  int      // how long, in seconds, a resolver may keep a record; 0 for DefaultZoneTimeToLive
}

// Zone is the zone file of the committed accounts, with validators the set of the height in progress,
// under settings, and with height, modulo 2³², as its SOA serial (spec §3.2). For each name that is not
// frozen, in ascending order as the accounts are:
//
//   - an A or AAAA record for each IP address it lists, in its order on chain;
//   - a TXT record "hdls-public-address=<address>" for its public address;
//   - when its public address is an https:// one, a TLSA record "3 1 1 <hash>" for each TLS key it
//     lists, under _<port>._tcp.<name>, the port of that address, 443 without one.
//
// Then validatorsLabel, an A or AAAA record for each IP address of each validator, once each: the IP
// filter a firewall resolves (spec R1). A loopback address is left out, as gossipAllowList leaves it
// out, and for its reason: behind a port forwarder or a proxy every caller can be one, so a validator
// that published one would open every firewall that took it.
//
// Last the lookup from an address to names: for each address a name lists, in the order of
// netip.Addr.Compare, one TXT record under <hex>._ip, the address's bytes in hex (8 digits for IPv4, 32
// for IPv6), with a string for each name that lists it, in name order, a frozen one included as
// "<name> (frozen)". One record, not one a name: BIND 9.18.28 and later refuse a zone with more than 100
// records of one type under one owner (max-records-per-type). Past maxZoneClaimants names, a last string
// says how many more there are and which IP lookup file lists them all, such as
// "and 3 more: hdls/resolve/ip/198.51.100.7".
func Zone(settings ZoneSettings, accounts []ledger.Account, validators *consensus.ValidatorSet, height int64) string {
	head, tail := zoneParts(settings, accounts, validators)
	return head + zoneSerial(height) + tail
}

// zoneParts is the zone file without its serial: what comes before it and what comes after. The SOA
// asks a secondary server to check for a new serial every 5 minutes, to retry after a minute, and to
// stop serving the zone after a day without its primary, and a resolver to keep the answer "no such
// name" as long as a record.
func zoneParts(settings ZoneSettings, accounts []ledger.Account, validators *consensus.ValidatorSet) (head, tail string) {
	timeToLive := cmp.Or(settings.ZoneTimeToLive, DefaultZoneTimeToLive)
	head = fmt.Sprintf("$ORIGIN %s.\n$TTL %d\n@ SOA %s. %s. ", settings.Zone, timeToLive, settings.ZoneNameServers[0], settings.ZoneContact)
	var zone strings.Builder
	fmt.Fprintf(&zone, " 300 60 86400 %d\n", timeToLive)
	for _, nameServer := range settings.ZoneNameServers {
		fmt.Fprintf(&zone, "@ NS %s.\n", nameServer)
	}
	for _, account := range accounts {
		if account.Frozen {
			continue
		}
		for _, ipAddress := range account.IPAddresses {
			fmt.Fprintf(&zone, "%s %s %s\n", account.Name, addressRecordType(ipAddress), ipAddress)
		}
		if account.PublicAddress != "" {
			fmt.Fprintf(&zone, "%s TXT %s\n", account.Name, characterStrings("hdls-public-address="+account.PublicAddress))
		}
		if host, isHTTPS := strings.CutPrefix(account.PublicAddress, "https://"); isHTTPS {
			for _, tlsKey := range account.TLSKeys {
				fmt.Fprintf(&zone, "_%s._tcp.%s TLSA 3 1 1 %s\n", tlsPort(host), account.Name, tlsKey)
			}
		}
	}
	written := map[string]bool{} // the validators' addresses written so far: two validators may list one
	for _, validator := range validators.Validators() {
		account, _ := ledger.FindAccount(accounts, validator.Name)
		for _, ipAddress := range account.IPAddresses {
			address, _ := netip.ParseAddr(ipAddress) // canonical (ledger.CanonicalIPAddress), so it parses
			if !address.IsLoopback() && !written[ipAddress] {
				written[ipAddress] = true
				fmt.Fprintf(&zone, "%s %s %s\n", validatorsLabel, addressRecordType(ipAddress), ipAddress)
			}
		}
	}
	claims := ipClaims(accounts)
	for _, address := range slices.SortedFunc(maps.Keys(claims), netip.Addr.Compare) {
		claimants := claims[address]
		var quotedClaimants []string
		for _, claimant := range claimants[:min(len(claimants), maxZoneClaimants)] {
			quotedClaimants = append(quotedClaimants, characterStrings(claimant))
		}
		if len(claimants) > maxZoneClaimants {
			more := fmt.Sprintf("and %d more: %s", len(claimants)-maxZoneClaimants, ipLookupPath(address))
			quotedClaimants = append(quotedClaimants, characterStrings(more))
		}
		fmt.Fprintf(&zone, "%x._ip TXT %s\n", address.AsSlice(), strings.Join(quotedClaimants, " "))
	}
	return head, zone.String()
}

// zoneSerial is the SOA serial of the zone written at height: the height modulo 2³².
func zoneSerial(height int64) string { return strconv.FormatUint(uint64(uint32(height)), 10) }

// sameApartFromSerial reports whether zone is head, a serial and tail: the zone file of zoneParts, at
// any serial.
func sameApartFromSerial(zone, head, tail string) bool {
	serial, hasHead := strings.CutPrefix(zone, head)
	serial, hasTail := strings.CutSuffix(serial, tail)
	_, err := strconv.ParseUint(serial, 10, 32)
	return hasHead && hasTail && err == nil
}

// addressRecordType is the record type of an IP address: AAAA for IPv6, which alone has colons, A for
// IPv4.
func addressRecordType(ipAddress string) string {
	if strings.Contains(ipAddress, ":") {
		return "AAAA"
	}
	return "A"
}

// tlsPort is the port of host, an https:// public address without its "https://": the one it gives,
// else 443. The address is canonical (ledger.CanonicalAddress), so a port is what follows the last colon
// after any IPv6 address's closing bracket.
func tlsPort(host string) string {
	if colon := strings.LastIndex(host, ":"); colon > strings.LastIndex(host, "]") {
		return host[colon+1:]
	}
	return "443"
}

// characterStrings is text as the data of a TXT record (RFC 1035 §5.1): quoted strings of at most 255
// bytes each, the most one holds, with a backslash before a quote or a backslash, and any other byte
// outside printable ASCII written as \DDD. A long text is so split into several strings, which a reader
// joins.
func characterStrings(text string) string {
	var quoted []string
	for len(text) > 0 {
		piece := text[:min(len(text), 255)]
		text = text[len(piece):]
		var escaped strings.Builder
		for i := 0; i < len(piece); i++ {
			switch character := piece[i]; {
			case character == '"' || character == '\\':
				escaped.WriteByte('\\')
				escaped.WriteByte(character)
			case character < ' ' || character > '~':
				fmt.Fprintf(&escaped, "\\%03d", character)
			default:
				escaped.WriteByte(character)
			}
		}
		quoted = append(quoted, `"`+escaped.String()+`"`)
	}
	return strings.Join(quoted, " ")
}

// ipClaims maps each IP address a name lists to the names that list it, in name order, a frozen one as
// "<name> (frozen)": the zone's _ip lookup and the IP lookup files hold it. The ledger takes an address
// only in its one plain form (ledger.CanonicalIPAddress), so it parses, and its String is that form.
func ipClaims(accounts []ledger.Account) map[netip.Addr][]string {
	claims := map[netip.Addr][]string{}
	for _, account := range accounts {
		claimant := account.Name
		if account.Frozen {
			claimant += " (frozen)"
		}
		for _, ipAddress := range account.IPAddresses {
			address, _ := netip.ParseAddr(ipAddress)
			claims[address] = append(claims[address], claimant)
		}
	}
	return claims
}

// ipLookupPath is the path of the IP lookup file of address: its plain form, which has no "/" and is
// never "." or "..", so it is a safe file name.
func ipLookupPath(address netip.Addr) string { return ipLookupFolder + "/" + address.String() }

// openNameService is the name service's part of openPublicFolder. It writes each IP lookup file the
// folder does not hold as it is now, and marks it current, so that openPublicFolder removes the others.
// It takes the zone file the folder holds as the one last written, so that a restart writes it again
// only if more than its serial changed (publishZone), and removes it when no zone is set.
func (node *Node) openNameService(currentPaths map[string]bool) {
	for address, claimants := range ipClaims(node.state.Accounts) {
		path := ipLookupPath(address)
		currentPaths[path] = true
		content, _ := jsonAnswer(claimants) // strings: it always marshals
		if held, _ := os.ReadFile(filepath.Join(node.config.PublicPath, path)); !bytes.Equal(held, content) {
			node.publicFolder.write(path, content)
		}
	}
	held, err := os.ReadFile(filepath.Join(node.config.PublicPath, zonePath))
	node.publicFolder.zone = string(held)
	if err == nil && node.config.ZoneSettings.Zone == "" {
		node.publicFolder.remove(zonePath)
	}
}

// writeIPLookups queues the IP lookup files a commit or a catch-up changed, given the accounts of the
// state the node left: the file of each address whose names changed, and the removal of the file of each
// address no name lists any more.
func (node *Node) writeIPLookups(previousAccounts []ledger.Account) {
	before, now := ipClaims(previousAccounts), ipClaims(node.state.Accounts)
	for address := range before {
		if _, listed := now[address]; !listed {
			node.publicFolder.remove(ipLookupPath(address))
		}
	}
	for address, claimants := range now {
		if !slices.Equal(claimants, before[address]) {
			node.publishAnswer(ipLookupPath(address), claimants)
		}
	}
}

// publishZone queues hdls/zone, the zone file of the committed state, with the committed height as its
// serial, when it differs by more than its serial from the one last queued, or held at Open. So a commit
// that changes no name's records and no validator leaves the file and its serial as they are, and a DNS
// server, which reloads a zone when its serial grows, reloads it only when it changed. Without a zone
// setting it does nothing.
func (node *Node) publishZone() {
	settings := node.config.ZoneSettings
	if settings.Zone == "" {
		return
	}
	view := node.view.Load()
	head, tail := zoneParts(settings, view.state.Accounts, view.validators)
	if !sameApartFromSerial(node.publicFolder.zone, head, tail) {
		node.publicFolder.zone = head + zoneSerial(view.state.Height) + tail
		node.publicFolder.write(zonePath, []byte(node.publicFolder.zone))
	}
}
