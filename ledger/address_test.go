package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A public address has one plain form (spec 4.2): "http://" or "https://", a host in lowercase, an
// optional port, and nothing after it. Wallets ask it for its height, so anything more would make
// another request of it: a path or a query turned a wallet's question into a request for the wallet's
// own page, which asked again, without end (review, 2026-11-01). The genesis refuses every other form,
// and so do an update's intake check (UpdateFitsState), ProposeBlock and ApplyBlock.
func TestAddressHasOnePlainForm(t *testing.T) {
	for _, address := range []string{"http://10.0.0.2:8080", "https://bank-a.example", "http://localhost", "http://[::1]:8080", "http://10.0.0.2:65535", "http://[::1]", "http://bank-a.example.org:443"} {
		if !CanonicalAddress(address) {
			t.Errorf("CanonicalAddress(%q) = false, want true", address)
		}
	}
	previousState := buildLedgerState(4, []Account{namedAccount("alice", "100"), seatedAccount()}, rational("0.1"))
	for _, address := range []string{
		"http://10.0.0.2:8080/", "http://10.0.0.2:8080/hdls/status", // a path, even "/"
		"http://10.0.0.2:8080/?", "http://10.0.0.2:8080?", "http://10.0.0.2:8080?x=1", // a query, even an empty one
		"http://user@10.0.0.2:8080", "http://@10.0.0.2:8080", // user info
		"http://10.0.0.2:8080#", "http://10.0.0.2:8080#x", // a fragment
		"ftp://10.0.0.2", "file:///etc/passwd", "10.0.0.2:8080", "//10.0.0.2:8080", "http:10.0.0.2", // another scheme, or none
		"HTTP://10.0.0.2:8080", "http://Bank-A:8080", "http://10.0.0.2:08080", "http://10.0.0.2:", " http://10.0.0.2", // another spelling
		"http://:8080", "http://10.0.0.2:0", "http://10.0.0.2:65536", "http://a b", "-", // no host, a port out of range, not a URL
		"http://h<o>", "http://h\"o", "http://host;x", "http://.", "http://-", "http://_", "http://bank-a.", "http://-bank", // not a host name
		"http://[::1%25lo]:8090", "http://[::1]x", "http://[::1", "http://[]", "http://[g::1]", "http://10.0.0.2:+80", // a bracket or a port gone wrong
	} {
		t.Run(fmt.Sprintf("%q", address), func(t *testing.T) {
			if CanonicalAddress(address) {
				t.Errorf("CanonicalAddress(%q) = true, want false", address)
			}
			genesis := exampleGenesis()
			genesis.Accounts[2].PublicAddress = address // alice's
			expectGenesisRefused(t, genesis, fmt.Sprintf(`the public address %q of "alice" is not an address`, address))
			if address == ClearField {
				return // in an update, ClearField sets the public address to none
			}
			update := signedUpdate(AccountUpdate{Name: "alice", PublicAddress: address, BlockID: 5})
			if err := UpdateFitsState(previousState, update); err == nil || !strings.Contains(err.Error(), "is not an address") {
				t.Errorf("UpdateFitsState: %v, want a refusal of the address", err)
			}
			if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}}); len(block.Content.AccountUpdates) != 0 {
				t.Error("ProposeBlock carried the update")
			}
			if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, update)); err == nil {
				t.Error("ApplyBlock accepted a block carrying the update")
			}
		})
	}
	accepted := signedUpdate(AccountUpdate{Name: "alice", PublicAddress: "http://10.0.0.2:8080", BlockID: 5})
	if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, accepted)); err != nil {
		t.Errorf("an update to a plain address was refused: %v", err)
	}
}

// A name publishes at most MaxIPAddresses IP addresses (spec §4), each in its one plain form, the one
// netip writes: no other spelling, no IPv4 address in IPv6 dress, no zone, no unspecified or multicast
// address, and none twice. Private and loopback addresses are taken, so a LAN network and the tests
// work. The genesis refuses every other list, and so do an update's intake check (UpdateFitsState),
// ProposeBlock and ApplyBlock: it is a rule of the block. An update's list replaces the whole of the
// account's.
func TestIPAddressesHaveOnePlainForm(t *testing.T) {
	for _, ipAddress := range []string{"10.0.0.2", "203.0.113.7", "127.0.0.1", "192.168.1.1", "2001:db8::1", "::1", "fe80::1", "2001:db8:0:1:1:1:1:1"} {
		if !CanonicalIPAddress(ipAddress) {
			t.Errorf("CanonicalIPAddress(%q) = false, want true", ipAddress)
		}
	}
	type refusedList struct {
		description string
		ipAddresses []string
		want        string
	}
	var refusedLists []refusedList
	for description, ipAddress := range map[string]string{
		"a leading zero": "010.0.0.2", "IPv6 in capitals": "2001:DB8::1", "IPv6 zeros not shortened": "2001:db8:0::1",
		"IPv6 written out": "2001:db8:0:0:0:0:0:1", "an IPv4 address in IPv6 dress": "::ffff:10.0.0.2", "a zone": "fe80::1%eth0",
		"the unspecified IPv4 address": "0.0.0.0", "the unspecified IPv6 address": "::", "an IPv4 multicast address": "224.0.0.1",
		"an IPv6 multicast address": "ff02::1", "a host name": "bank-a.example", "a URL": "http://10.0.0.2:8090", "a port": "10.0.0.2:8090",
		"brackets": "[::1]", "a prefix": "10.0.0.0/8", "a space": " 10.0.0.2", "nothing": "", "the dash among addresses": ClearField,
	} {
		ipAddresses := []string{ipAddress}
		if ipAddress == ClearField {
			ipAddresses = []string{"10.0.0.2", ClearField} // alone, it clears the list
		}
		refusedLists = append(refusedLists, refusedList{description, ipAddresses, fmt.Sprintf("the IP address %q is not an IP address", ipAddress)})
	}
	refusedLists = append(refusedLists,
		refusedList{"a duplicate", []string{"10.0.0.2", "2001:db8::2", "10.0.0.2"}, `the IP address "10.0.0.2" is given twice`},
		refusedList{"a fifth address", []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"}, "5 IP addresses, above the 4 a name may publish"})

	previousState := buildLedgerState(4, []Account{namedAccount("alice", "100"), seatedAccount()}, rational("0.1"))
	for _, refused := range refusedLists {
		t.Run(refused.description, func(t *testing.T) {
			genesis := exampleGenesis()
			genesis.Accounts[2].IPAddresses = refused.ipAddresses // alice's
			expectGenesisRefused(t, genesis, `the IP addresses of "alice": `+refused.want)
			update := signedUpdate(AccountUpdate{Name: "alice", IPAddresses: refused.ipAddresses, BlockID: 5})
			if err := UpdateFitsState(previousState, update); err == nil || !strings.Contains(err.Error(), refused.want) {
				t.Errorf("UpdateFitsState: %v, want the refusal %q", err, refused.want)
			}
			if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}}); len(block.Content.AccountUpdates) != 0 {
				t.Error("ProposeBlock carried the update")
			}
			if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, update)); err == nil {
				t.Error("ApplyBlock accepted a block carrying the update")
			}
		})
	}
	genesis := exampleGenesis()
	genesis.Accounts[2].IPAddresses = []string{ClearField}
	expectGenesisRefused(t, genesis, `the IP addresses of "alice": the IP address "-" is not an IP address`)

	// Four addresses, IPv4 and IPv6, private and loopback ones too, are a name's in the genesis and by
	// an update; the update's list replaces the whole list, and a later one with one address replaces
	// the four.
	four := []string{"203.0.113.7", "2001:db8::7", "10.0.0.7", "::1"}
	genesis = exampleGenesis()
	genesis.Accounts[2].IPAddresses = four
	if _, state, err := ValidateGenesis(genesis); err != nil || !slices.Equal(find(state.Accounts, "alice").IPAddresses, four) {
		t.Errorf("a genesis with four IP addresses: %v, alice holds %v", err, find(state.Accounts, "alice").IPAddresses)
	}
	alice := namedAccount("alice", "100")
	alice.IPAddresses = []string{"192.168.1.1"}
	previousState = buildLedgerState(4, []Account{alice, seatedAccount()}, rational("0.1"))
	setsFour := signedUpdate(AccountUpdate{Name: "alice", IPAddresses: four, BlockID: 5})
	next, err := ApplyBlock(testChain(), previousState, zeroHash, ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{setsFour}}))
	if err != nil || !slices.Equal(find(next.Accounts, "alice").IPAddresses, four) {
		t.Fatalf("an update to four IP addresses: %v, alice holds %v", err, find(next.Accounts, "alice").IPAddresses)
	}
	setsOne := signedUpdate(AccountUpdate{Name: "alice", IPAddresses: []string{"2001:db8::8"}, BlockID: 6})
	next, err = ApplyBlock(testChain(), next, zeroHash, ProposeBlock(testChain(), next, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{setsOne}}))
	if err != nil || !slices.Equal(find(next.Accounts, "alice").IPAddresses, []string{"2001:db8::8"}) {
		t.Fatalf("an update to one IP address: %v, alice holds %v, want that address alone", err, find(next.Accounts, "alice").IPAddresses)
	}
}

// exampleTLSKey is a TLS key record in its one form: the SHA-256, in lowercase hex, of seed, which
// stands in for a key's SubjectPublicKeyInfo.
func exampleTLSKey(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(hash[:])
}

// A name lists at most MaxTLSKeys TLS keys (Phase 4 spec §3.3), the current one and the next, each the
// SHA-256 of a SubjectPublicKeyInfo as 64 lowercase hex digits, the hash a TLSA "3 1 1" record holds,
// and none twice. The genesis refuses every other list, and so do an update's intake check
// (UpdateFitsState), ProposeBlock and ApplyBlock: it is a rule of the block. An update that changes
// only the TLS keys is a change: it sets them, a later one replaces the whole list, and "-" alone
// clears it.
func TestTLSKeysHaveOneForm(t *testing.T) {
	current, next := exampleTLSKey("current"), exampleTLSKey("next")
	if !CanonicalTLSKey(current) {
		t.Fatalf("CanonicalTLSKey(%q) = false, want true", current)
	}
	type refusedList struct {
		description string
		tlsKeys     []string
		want        string
	}
	var refusedLists []refusedList
	for description, tlsKey := range map[string]string{
		"capitals": strings.ToUpper(current), "a digit short": current[:63], "a digit more": current + "0", "a 0x prefix": "0x" + current[2:],
		"a letter past f": "g" + current[1:], "a space": " " + current[1:], "base64": "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU=",
		"nothing": "", "the dash among keys": ClearField,
	} {
		tlsKeys := []string{tlsKey}
		if tlsKey == ClearField {
			tlsKeys = []string{current, ClearField} // alone, it clears the list
		}
		refusedLists = append(refusedLists, refusedList{description, tlsKeys, fmt.Sprintf("the TLS key %q is not a TLS key", tlsKey)})
	}
	refusedLists = append(refusedLists,
		refusedList{"a duplicate", []string{current, current}, fmt.Sprintf("the TLS key %q is given twice", current)},
		refusedList{"a third key", []string{current, next, exampleTLSKey("third")}, "3 TLS keys, above the 2 a name may list"})

	previousState := buildLedgerState(4, []Account{namedAccount("alice", "100"), seatedAccount()}, rational("0.1"))
	for _, refused := range refusedLists {
		t.Run(refused.description, func(t *testing.T) {
			genesis := exampleGenesis()
			genesis.Accounts[2].TLSKeys = refused.tlsKeys // alice's
			expectGenesisRefused(t, genesis, `the TLS keys of "alice": `+refused.want)
			update := signedUpdate(AccountUpdate{Name: "alice", TLSKeys: refused.tlsKeys, BlockID: 5})
			if err := UpdateFitsState(previousState, update); err == nil || !strings.Contains(err.Error(), refused.want) {
				t.Errorf("UpdateFitsState: %v, want the refusal %q", err, refused.want)
			}
			if block := ProposeBlock(testChain(), previousState, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}}); len(block.Content.AccountUpdates) != 0 {
				t.Error("ProposeBlock carried the update")
			}
			if _, err := ApplyBlock(testChain(), previousState, zeroHash, forgeUpdateBlock(previousState, 5, update)); err == nil {
				t.Error("ApplyBlock accepted a block carrying the update")
			}
		})
	}
	genesis := exampleGenesis()
	genesis.Accounts[2].TLSKeys = []string{ClearField}
	expectGenesisRefused(t, genesis, `the TLS keys of "alice": the TLS key "-" is not a TLS key`)

	two := []string{current, next}
	genesis = exampleGenesis()
	genesis.Accounts[2].TLSKeys = two
	if _, state, err := ValidateGenesis(genesis); err != nil || !slices.Equal(find(state.Accounts, "alice").TLSKeys, two) {
		t.Errorf("a genesis with two TLS keys: %v, alice holds %v", err, find(state.Accounts, "alice").TLSKeys)
	}
	state := previousState
	for _, step := range []struct {
		description string
		tlsKeys     []string
		want        []string
	}{
		{"sets two", two, two},
		{"replaces them with one", []string{next}, []string{next}},
		{"clears them", []string{ClearField}, nil},
	} {
		update := signedUpdate(AccountUpdate{Name: "alice", TLSKeys: step.tlsKeys, BlockID: state.Height + 1})
		block := ProposeBlock(testChain(), state, zeroHash, zeroHash, BlockContent{AccountUpdates: []AccountUpdate{update}})
		var err error
		if state, err = ApplyBlock(testChain(), state, zeroHash, block); err != nil || len(block.Content.AccountUpdates) != 1 || !slices.Equal(find(state.Accounts, "alice").TLSKeys, step.want) {
			t.Fatalf("an update that %s: %v, alice holds %v, want %v", step.description, err, find(state.Accounts, "alice").TLSKeys, step.want)
		}
	}
}
