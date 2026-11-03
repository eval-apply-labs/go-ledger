package node

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// testZoneSettings are the zone settings of the zone tests: the zone of spec §3.2's example, with two
// name servers and the default time to live.
var testZoneSettings = ZoneSettings{Zone: "hdls.example.org", ZoneNameServers: []string{"ns1.example.org", "ns2.example.org"},
	ZoneContact: "hostmaster.example.org"}

// knownZoneState is the state of testdata/zone: three banks, the validators, of which bank-a lists two
// IP addresses and bank-c one of them too, and bank-b an https:// address with a port and two TLS keys;
// carol, frozen, and dave both list 198.51.100.7, and dave an http:// address and a TLS key; erin lists
// nothing.
func knownZoneState() ([]ledger.Account, *consensus.ValidatorSet) {
	bankA, bankB, bankC := testAccount("bank-a", "100"), testAccount("bank-b", "100"), testAccount("bank-c", "100")
	carol, dave, erin := testAccount("carol", "10"), testAccount("dave", "10"), testAccount("erin", "10")
	bankA.IPAddresses, bankA.PublicAddress, bankA.TLSKeys = []string{"203.0.113.10", "2001:db8::10"}, "https://bank-a.example.org", []string{strings.Repeat("a1", 32)}
	bankB.IPAddresses, bankB.PublicAddress = []string{"203.0.113.20"}, "https://bank-b.example.org:8443"
	bankB.TLSKeys = []string{strings.Repeat("b1", 32), strings.Repeat("b2", 32)}
	bankC.IPAddresses = []string{"203.0.113.10"}
	carol.IPAddresses, carol.PublicAddress, carol.Frozen = []string{"198.51.100.7"}, "https://carol.example.org", true
	dave.IPAddresses, dave.PublicAddress, dave.TLSKeys = []string{"2001:db8::7", "198.51.100.7"}, "http://[2001:db8::7]:8080", []string{strings.Repeat("d1", 32)}
	genesis := testGenesis([]ledger.Account{bankA, bankB, bankC, carol, dave, erin}, time.Second, 0)
	return genesis.Accounts, staticChain("bank-a", "bank-b", "bank-c").ValidatorRule.SetFor(genesis)
}

// zoneLines is the zone's lines, without the final newline's empty one.
func zoneLines(zone string) []string { return strings.Split(strings.TrimSuffix(zone, "\n"), "\n") }

// The zone of a known state is testdata/zone byte for byte (spec §3.2): a pure function of the
// accounts, the validator set and the settings, with the height as its serial.
func TestZoneOfAKnownState(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "zone"))
	if err != nil {
		t.Fatal(err)
	}
	accounts, validators := knownZoneState()
	if zone := Zone(testZoneSettings, accounts, validators, 1043); zone != string(want) {
		t.Errorf("the zone of the known state is not testdata/zone:\n%s\nwant:\n%s", zone, want)
	}
}

// The records come out as spec §3.2 says: an A or AAAA record for each of a name's IP addresses, in
// their order; its public address as a TXT record; a TLSA record for each TLS key under the port of its
// https:// public address, 443 without one, and none for an http:// one, which declares no TLS service;
// a frozen name left out; validatorsLabel holding each of the validators' addresses once; and the serial
// the height modulo 2³². A TXT text longer than 255 bytes goes in strings of 255 bytes at most.
func TestZoneRecordsAsTheSpecSays(t *testing.T) {
	accounts, validators := knownZoneState()
	lines := zoneLines(Zone(testZoneSettings, accounts, validators, 1<<32+1043))
	for _, want := range [][]string{
		{"$ORIGIN hdls.example.org.", "$TTL 60", "@ SOA ns1.example.org. hostmaster.example.org. 1043 300 60 86400 60",
			"@ NS ns1.example.org.", "@ NS ns2.example.org."},
		{"bank-a A 203.0.113.10", "bank-a AAAA 2001:db8::10", `bank-a TXT "hdls-public-address=https://bank-a.example.org"`,
			"_443._tcp.bank-a TLSA 3 1 1 " + strings.Repeat("a1", 32)},
		{"bank-b A 203.0.113.20", `bank-b TXT "hdls-public-address=https://bank-b.example.org:8443"`,
			"_8443._tcp.bank-b TLSA 3 1 1 " + strings.Repeat("b1", 32), "_8443._tcp.bank-b TLSA 3 1 1 " + strings.Repeat("b2", 32)},
		{"dave AAAA 2001:db8::7", "dave A 198.51.100.7", `dave TXT "hdls-public-address=http://[2001:db8::7]:8080"`},
		{validatorsLabel + " A 203.0.113.10", validatorsLabel + " AAAA 2001:db8::10", validatorsLabel + " A 203.0.113.20"},
	} {
		if start := slices.Index(lines, want[0]); start < 0 || !slices.Equal(lines[start:min(len(lines), start+len(want))], want) {
			t.Errorf("the zone does not hold, in this order:\n%s\nthe zone:\n%s", strings.Join(want, "\n"), strings.Join(lines, "\n"))
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "carol ") || strings.Contains(line, "carol.example.org") || strings.HasPrefix(line, "erin ") {
			t.Errorf("the zone holds %q: carol is frozen, erin lists nothing", line)
		}
		if strings.Contains(line, "._tcp.dave ") {
			t.Errorf("the zone holds %q: dave's public address is an http:// one", line)
		}
	}
	if count := strings.Count(strings.Join(lines, "\n"), validatorsLabel+" A 203.0.113.10"); count != 1 {
		t.Errorf("%s holds 203.0.113.10, which bank-a and bank-c list, %d times, want once", validatorsLabel, count)
	}

	long := accounts[slices.IndexFunc(accounts, func(account ledger.Account) bool { return account.Name == "erin" })]
	long.PublicAddress = "https://" + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".example:65535"
	if !ledger.CanonicalAddress(long.PublicAddress) {
		t.Fatal("premise: the long address is a public address")
	}
	text := "hdls-public-address=" + long.PublicAddress
	want := `erin TXT "` + text[:255] + `" "` + text[255:] + `"`
	if lines := zoneLines(Zone(testZoneSettings, []ledger.Account{long}, consensus.NewValidatorSet(nil), 1)); !slices.Contains(lines, want) {
		t.Errorf("a %d-byte TXT text is not split at 255 bytes: %q", len(text), lines)
	}
	if got := characterStrings("a \"quoted\" \\ text\n"); got != `"a \"quoted\" \\ text\010"` {
		t.Errorf("characterStrings escapes to %s", got)
	}
}

// The owner of every A or AAAA record is a host name, letters, digits and hyphens: by default BIND
// refuses a zone where one is not (check-names), such as one with an underscore.
func TestZoneAddressOwnersAreHostNames(t *testing.T) {
	accounts, validators := knownZoneState()
	for _, line := range zoneLines(Zone(testZoneSettings, accounts, validators, 1043)) {
		fields := strings.Fields(line)
		if (fields[1] == "A" || fields[1] == "AAAA") && strings.Trim(fields[0], "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			t.Errorf("%q: the owner of an address record is not a host name", line)
		}
	}
}

// A validator's loopback address is left out of validatorsLabel, as gossipAllowList leaves it out of the
// allow list.
func TestZoneLeavesLoopbackOutOfTheValidators(t *testing.T) {
	bankA := ledger.Account{Name: "bank-a", IPAddresses: []string{"127.0.0.1", "203.0.113.10", "::1"}}
	validators := consensus.NewValidatorSet([]consensus.Validator{{Name: "bank-a", PublicKey: "key", Power: 1}})
	var records []string
	for _, line := range zoneLines(Zone(testZoneSettings, []ledger.Account{bankA}, validators, 1)) {
		if strings.HasPrefix(line, validatorsLabel+" ") {
			records = append(records, line)
		}
	}
	if want := []string{validatorsLabel + " A 203.0.113.10"}; !slices.Equal(records, want) {
		t.Errorf("bank-a lists 127.0.0.1, 203.0.113.10 and ::1, and the validators' records are %q, want %q", records, want)
	}
}

// The _ip lookup is one TXT record for each address a name lists, under the address's bytes in hex: 8
// digits for IPv4, 32 for IPv6 (spec §3.2, §3.4). Its strings are the names that list it, a frozen one
// marked, at most maxZoneClaimants of them, and then how many more its IP lookup file lists.
func TestZoneLookupIsOneRecordForEachAddress(t *testing.T) {
	lookup := func(zone string) (records []string) {
		for _, line := range zoneLines(zone) {
			if strings.Contains(line, "._ip ") {
				records = append(records, line)
			}
		}
		return records
	}
	accounts, validators := knownZoneState()
	want := []string{
		`c6336407._ip TXT "carol (frozen)" "dave"`,
		`cb00710a._ip TXT "bank-a" "bank-c"`,
		`cb007114._ip TXT "bank-b"`,
		`20010db8000000000000000000000007._ip TXT "dave"`,
		`20010db8000000000000000000000010._ip TXT "bank-a"`,
	}
	if records := lookup(Zone(testZoneSettings, accounts, validators, 1043)); !slices.Equal(records, want) {
		t.Errorf("the _ip lookup is\n%s\nwant\n%s", strings.Join(records, "\n"), strings.Join(want, "\n"))
	}

	var claimants []ledger.Account
	record := "c0000201._ip TXT"
	for i := range maxZoneClaimants + 1 {
		claimants = append(claimants, ledger.Account{Name: fmt.Sprintf("claimant-%03d", i), IPAddresses: []string{"192.0.2.1"}})
		if i < maxZoneClaimants {
			record += fmt.Sprintf(` "claimant-%03d"`, i)
		}
	}
	record += ` "and 1 more: hdls/resolve/ip/192.0.2.1"`
	if records := lookup(Zone(testZoneSettings, claimants, consensus.NewValidatorSet(nil), 1)); !slices.Equal(records, []string{record}) {
		t.Errorf("the _ip lookup of 101 names that list 192.0.2.1 is\n%s\nwant\n%s", strings.Join(records, "\n"), record)
	}
}

// named-checkzone, BIND's zone checker, takes the zone of the known state, where it is installed.
func TestNamedCheckzoneTakesTheZone(t *testing.T) {
	checker, err := exec.LookPath("named-checkzone")
	if err != nil {
		t.Skip("named-checkzone is not installed")
	}
	accounts, validators := knownZoneState()
	path := filepath.Join(t.TempDir(), "zone")
	if err := os.WriteFile(path, []byte(Zone(testZoneSettings, accounts, validators, 1043)), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(checker, testZoneSettings.Zone, path).CombinedOutput(); err != nil {
		t.Errorf("named-checkzone refused the zone: %v\n%s", err, output)
	}
}

// zoneNode is a node driven by hand, the sole validator bank-a of a static chain, writing its public
// folder, with the zone settings of the tests, into folder; genesis's accounts are taken as they are.
func zoneNode(folder string, genesis ledger.LedgerState) *Node {
	config := testConfig(staticChain("bank-a"), "bank-a", genesis)
	config.PublicPath, config.ZoneSettings = folder, testZoneSettings
	return New(config, &recordingTransport{}) // a sole validator commits a height within startHeight
}

// commitUpdate pools the update and commits the next block, which takes it.
func commitUpdate(t *testing.T, node *Node, update ledger.AccountUpdate) {
	t.Helper()
	update.BlockID = node.height()
	update = signedUpdate(update)
	verifiedKey, err := node.checkUpdate(update)
	if err == nil {
		_, err = node.admitUpdate(update, verifiedKey)
	}
	if err != nil {
		t.Fatalf("premise: %s's update is pooled: %v", update.Name, err)
	}
	height := node.height()
	node.startHeight()
	if node.height() != height+1 {
		t.Fatalf("premise: the node committed block %d", height)
	}
}

// Anyone can check the zone file: rebuilt from the public hdls/accounts and the validator rule, it is
// the same bytes apart from the SOA serial (spec §3.2), the height at which the node last wrote it. Here
// that is the committed height of hdls/status, as the last block changed alice's records; after a block
// that changes no record, it would not be.
func TestZoneRebuildsFromThePublicAccounts(t *testing.T) {
	bankA, alice := testAccount("bank-a", "100"), testAccount("alice", "10")
	bankA.IPAddresses, bankA.PublicAddress = []string{"203.0.113.10"}, "https://bank-a.example.org"
	node := zoneNode(t.TempDir(), testGenesis([]ledger.Account{bankA, alice}, time.Hour, time.Hour))
	defer node.Stop()
	commitUpdate(t, node, ledger.AccountUpdate{Name: "alice", IPAddresses: []string{"198.51.100.7", "2001:db8::7"}, TLSKeys: []string{strings.Repeat("c1", 32)}})

	var accounts []ledger.Account
	var status struct{ HeightInProgress int64 }
	if err := json.Unmarshal(publicFile(t, node, "hdls/accounts"), &accounts); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(publicFile(t, node, "hdls/status"), &status); err != nil {
		t.Fatal(err)
	}
	validators := node.config.Chain.ValidatorRule.SetFor(ledger.LedgerState{Accounts: accounts})
	rebuilt, file := Zone(testZoneSettings, accounts, validators, status.HeightInProgress-1), string(publicFile(t, node, zonePath))
	if rebuilt != file || !strings.Contains(file, "alice AAAA 2001:db8::7") || !strings.Contains(file, " 1 300 60 86400 60") {
		t.Errorf("the zone rebuilt from hdls/accounts is not hdls/zone, or hdls/zone misses alice's block 1:\n%s\nhdls/zone:\n%s", rebuilt, file)
	}
}

// The zone file is written only when more than its serial changes: a block that changes no name's
// records leaves it alone, with its serial, and one that does writes it with the height as the serial.
// At Open the folder's file counts as the last one written: a node that opens at a later height on the
// same records leaves it, and one whose settings changed writes it again. Without a zone setting, Open
// removes it.
func TestZoneIsWrittenOnlyWhenMoreThanItsSerialChanges(t *testing.T) {
	folder := t.TempDir()
	bankA, alice := testAccount("bank-a", "100"), testAccount("alice", "10")
	bankA.IPAddresses = []string{"203.0.113.10"}
	genesis := testGenesis([]ledger.Account{bankA, alice}, time.Hour, time.Hour)
	node := zoneNode(folder, genesis)
	atOpen, content := publicFileInformation(t, node, zonePath), string(publicFile(t, node, zonePath))
	if !strings.Contains(content, "hostmaster.example.org. 0 300") || !strings.Contains(content, "bank-a A 203.0.113.10") {
		t.Fatalf("Open did not write the zone of genesis, serial 0:\n%s", content)
	}

	keepBusyByHand(t, "alice", node) // block 1 changes alice's description: no record of the zone
	node.startHeight()
	if node.height() != 2 {
		t.Fatal("premise: the node committed block 1")
	}
	if !os.SameFile(atOpen, publicFileInformation(t, node, zonePath)) || string(publicFile(t, node, zonePath)) != content {
		t.Error("block 1 changed no record of the zone, but the zone file was written again")
	}

	commitUpdate(t, node, ledger.AccountUpdate{Name: "alice", IPAddresses: []string{"198.51.100.7"}}) // block 2
	content = string(publicFile(t, node, zonePath))
	if !strings.Contains(content, "hostmaster.example.org. 2 300") || !strings.Contains(content, "alice A 198.51.100.7") {
		t.Errorf("block 2 gave alice an address, but the zone file is not block 2's, serial 2:\n%s", content)
	}
	written := publicFileInformation(t, node, zonePath)
	node.Stop()

	later := genesis // the same records, at a later height: only the serial would change
	later.Height, later.Accounts = 9, node.state.Accounts
	reopened := zoneNode(folder, later)
	if !os.SameFile(written, publicFileInformation(t, reopened, zonePath)) || string(publicFile(t, reopened, zonePath)) != content {
		t.Error("a node that opened at height 9 on the same records wrote the zone file again")
	}
	reopened.Stop()

	config := testConfig(staticChain("bank-a"), "bank-a", later)
	config.PublicPath, config.ZoneSettings = folder, testZoneSettings
	config.ZoneSettings.ZoneContact = "dns.example.org"
	changedSettings := New(config, &recordingTransport{})
	if content := string(publicFile(t, changedSettings, zonePath)); !strings.Contains(content, "@ SOA ns1.example.org. dns.example.org. 9 300") {
		t.Errorf("a node whose zone contact changed did not write the zone file again, at serial 9:\n%s", content)
	}
	changedSettings.Stop()

	config.ZoneSettings = ZoneSettings{}
	noZone := New(config, &recordingTransport{})
	defer noZone.Stop()
	if publicFile(t, noZone, zonePath) != nil {
		t.Error("a node without a zone setting left the zone file in its public folder")
	}
}

// The IP lookup files (spec §3.4): hdls/resolve/ip/<address> lists the names that list the address, a
// frozen one marked. At Open a file the folder holds as it is now is left alone, and one of an address
// no name lists is removed. A block writes the file of an address whose names it changed, and removes
// the file of an address no name lists any more; the others it leaves alone.
func TestIPLookupFiles(t *testing.T) {
	folder := t.TempDir()
	for path, content := range map[string]string{
		ipLookupFolder + "/2001:db8::7": "[\"alice\"]\n", // as it is now
		ipLookupFolder + "/192.0.2.99":  "[\"ghost\"]\n", // no name lists it
	} {
		if err := os.MkdirAll(filepath.Join(folder, ipLookupFolder), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	heldBefore, err := os.Stat(filepath.Join(folder, ipLookupFolder, "2001:db8::7"))
	if err != nil {
		t.Fatal(err)
	}
	alice, bob, carol := testAccount("alice", "10"), testAccount("bob", "10"), testAccount("carol", "10")
	alice.IPAddresses, bob.IPAddresses = []string{"198.51.100.7", "2001:db8::7"}, []string{"198.51.100.7"}
	carol.IPAddresses, carol.Frozen = []string{"203.0.113.9"}, true
	node := zoneNode(folder, testGenesis([]ledger.Account{testAccount("bank-a", "100"), alice, bob, carol}, time.Hour, time.Hour))
	defer node.Stop()

	lookup := func(address string) string { return string(publicFile(t, node, ipLookupFolder+"/"+address)) }
	for address, want := range map[string]string{"198.51.100.7": "[\"alice\",\"bob\"]\n", "2001:db8::7": "[\"alice\"]\n", "203.0.113.9": "[\"carol (frozen)\"]\n", "192.0.2.99": ""} {
		if got := lookup(address); got != want {
			t.Errorf("at Open the lookup of %s holds %q, want %q", address, got, want)
		}
	}
	if !os.SameFile(heldBefore, publicFileInformation(t, node, ipLookupFolder+"/2001:db8::7")) {
		t.Error("Open wrote again the lookup of 2001:db8::7, which the folder held as it is now")
	}

	untouched := publicFileInformation(t, node, ipLookupFolder+"/2001:db8::7")
	commitUpdate(t, node, ledger.AccountUpdate{Name: "bob", IPAddresses: []string{ledger.ClearField}})
	if got := lookup("198.51.100.7"); got != "[\"alice\"]\n" {
		t.Errorf("after bob cleared his addresses the lookup of 198.51.100.7 holds %q, want alice alone", got)
	}
	if !os.SameFile(untouched, publicFileInformation(t, node, ipLookupFolder+"/2001:db8::7")) {
		t.Error("bob's update wrote again the lookup of 2001:db8::7, whose names did not change")
	}
	commitUpdate(t, node, ledger.AccountUpdate{Name: "alice", IPAddresses: []string{"192.0.2.5"}})
	for address, want := range map[string]string{"198.51.100.7": "", "2001:db8::7": "", "192.0.2.5": "[\"alice\"]\n", "203.0.113.9": "[\"carol (frozen)\"]\n"} {
		if got := lookup(address); got != want {
			t.Errorf("after alice moved to 192.0.2.5 the lookup of %s holds %q, want %q", address, got, want)
		}
	}
}
