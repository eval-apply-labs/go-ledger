package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
)

// tlsBankNode is a bank's node behind TLS, as httptest serves it: its certificate is signed by no
// authority the wallet trusts, and its /hdls/status answers for block 9 of the test network. serverKey
// is its key's TLS key record: the SHA-256 of its SubjectPublicKeyInfo, in hex.
func tlsBankNode(t *testing.T) (address, serverKey string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(statusAt(9, testNetworkID)())
	}))
	t.Cleanup(server.Close)
	keyHash := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	return server.URL, hex.EncodeToString(keyHash[:])
}

// A bank that publishes an https:// address and lists TLS keys on chain is reached there only if its
// web server shows one of them, and then with no certificate authority (spec §3.3, Q10): by the
// wallet's client to its node, and by the other banks' check alike. A server that shows another key is
// refused with a message that names the bank. The normal check, which refuses the test server's
// certificate, applies otherwise: before the wallet verified a state, for a bank that lists no TLS
// keys, for a bank whose name is frozen, and for keys that a name that is not a bank lists at the
// bank's address. Where two banks publish one address, the key must be one that each of them lists.
func TestWalletPinsABanksWebServerByTheChain(t *testing.T) {
	address, serverKey := tlsBankNode(t)
	otherKey := strings.Repeat("0123456789abcdef", 4)
	const (
		notOnChain  = "shows a key that is not the one on chain"
		normalCheck = "certificate signed by unknown authority"
	)
	for _, testCase := range []struct {
		name         string
		publish      func(bank, thirdBank, alice *ledger.Account) // bank publishes the server's address
		firstContact bool                                         // the wallet asks before it verified a state
		refusal      string                                       // what the refusal says, or "" when the wallet takes the answer
		checked      string                                       // the share of the power the other banks' check reaches
	}{
		{"its key on chain", func(bank, _, _ *ledger.Account) { bank.TLSKeys = []string{otherKey, serverKey} }, false, "", "66%"},
		{"another key on chain", func(bank, _, _ *ledger.Account) { bank.TLSKeys = []string{otherKey} }, false, notOnChain, "33%"},
		{"no TLS keys on chain", func(bank, _, _ *ledger.Account) {}, false, normalCheck, "33%"},
		{"its key on a frozen name", func(bank, _, _ *ledger.Account) { bank.TLSKeys, bank.Frozen = []string{serverKey}, true }, false, normalCheck, "33%"},
		{"its key at the bank's address, listed by a name that is not a bank", func(_, _, alice *ledger.Account) {
			alice.PublicAddress, alice.TLSKeys = address, []string{serverKey}
		}, false, normalCheck, "33%"},
		{"two banks publish the address, each lists its key", func(bank, thirdBank, _ *ledger.Account) {
			bank.TLSKeys = []string{serverKey}
			thirdBank.PublicAddress, thirdBank.TLSKeys = address, []string{otherKey, serverKey}
		}, false, "", "100%"},
		{"two banks publish the address, one lists its key", func(bank, thirdBank, _ *ledger.Account) {
			bank.TLSKeys = []string{otherKey}
			thirdBank.PublicAddress, thirdBank.TLSKeys = address, []string{serverKey}
		}, false, notOnChain, "33%"},
		{"its key on chain, before the wallet verified a state", func(bank, _, _ *ledger.Account) { bank.TLSKeys = []string{serverKey} }, true, normalCheck, ""},
	} {
		wallet, testNode := threeBanks(t, address, "")
		bankName := nameOf(testNode.validators[1])
		testNode.change(func() {
			account := func(name string) *ledger.Account {
				return &testNode.accounts[slices.IndexFunc(testNode.accounts, func(account ledger.Account) bool { return account.Name == name })]
			}
			testCase.publish(account(bankName), account(nameOf(testNode.validators[2])), account("alice"))
		})
		var checkpoint node.Checkpoint
		if !testCase.firstContact {
			var err error
			if checkpoint, err = wallet.verifiedState(); err != nil {
				t.Fatalf("%s: %v", testCase.name, err)
			}
		}

		wallet.nodeURL = address // the wallet's node is now the bank behind TLS
		_, _, err := wallet.status()
		switch {
		case testCase.refusal == "" && err != nil:
			t.Errorf("%s: the wallet refused the bank's web server: %v", testCase.name, err)
		case testCase.refusal != "" && (err == nil || !strings.Contains(err.Error(), testCase.refusal)):
			t.Errorf("%s: the wallet's request to the bank's web server ended with %v, want a refusal that says %q", testCase.name, err, testCase.refusal)
		case testCase.refusal == notOnChain && !strings.Contains(err.Error(), bankName+"'s web server at "+address+" "+notOnChain):
			t.Errorf("%s: the refusal %q does not name the bank %s and its address", testCase.name, err, bankName)
		}

		if !testCase.firstContact {
			check := wallet.checkOtherBanks(context.Background(), checkpoint)
			if notes := check.Unchecked + check.Aligned; !strings.Contains(notes, "banks holding "+testCase.checked+" of the power") {
				t.Errorf("%s: the other banks' check says %q, want banks holding %s of the power", testCase.name, notes, testCase.checked)
			}
		}
	}
}

// A connection opened under one pin is never used under another: once the bank lists other TLS keys,
// the wallet's next request to its web server opens a new connection, checked against them, rather
// than reuse the one that the server's key, then on chain, let through.
func TestChangedTLSKeysAreCheckedOnANewConnection(t *testing.T) {
	address, serverKey := tlsBankNode(t)
	wallet, testNode := threeBanks(t, address, "")
	bankName := nameOf(testNode.validators[1])
	for _, tlsKeys := range [][]string{{serverKey}, {strings.Repeat("0123456789abcdef", 4)}} {
		testNode.change(func() {
			testNode.accounts[slices.IndexFunc(testNode.accounts, func(account ledger.Account) bool { return account.Name == bankName })].TLSKeys = tlsKeys
		})
		wallet.nodeURL = testNode.baseURL
		if _, err := wallet.verifiedState(); err != nil {
			t.Fatal(err)
		}
		wallet.nodeURL = address
		_, _, err := wallet.status()
		if serverOnChain := slices.Contains(tlsKeys, serverKey); serverOnChain != (err == nil) {
			t.Errorf("with the TLS keys %v on chain, the wallet's request to the bank's web server ended with %v", tlsKeys, err)
		}
	}
}
