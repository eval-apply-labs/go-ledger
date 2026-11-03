package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// A bank may list its web server's TLS keys on chain (spec §3.3): the SHA-256 of each key's
// SubjectPublicKeyInfo, the hash DANE's TLSA records call "3 1 1". The wallet then pins the bank's web
// server by the chain (Q10). When a bank of the newest state the wallet verified (trust.go) lists TLS
// keys, an https:// request whose scheme://host[:port] is its public address, as the bank wrote it, is
// answered only by a server whose certificate's key is one of them. No certificate authority is asked:
// the chain's record replaces it, so a bank may serve a certificate it signed itself. Any other request
// gets the standard library's normal check, as before: to an address no bank publishes, to a bank that
// lists no TLS keys, or to a bank whose name is frozen.
//
// Only a bank's record counts: a validator of that state. Any name may publish any address, and nothing
// checks that it controls it (spec §3.1), so a name that is not a bank could otherwise list a bank's
// address with a key of its own, and take the bank's channel or break it. Where two banks publish one
// address, the server's key must be one that each of them lists.
//
// Before the wallet holds a verified state (at first contact, after each start) there is nothing to
// pin, and the normal check applies. That is enough: the state itself is believed only when the pinned
// ⅔ signed it, whatever channel it came by, so the pin protects the channel after first contact.

// tlsPinningTransport is the transport of the wallet's HTTP clients, to its node (wallet.go) and to the
// other banks (banks.go); node.NewHTTPClient still gives each client its timeout and refuses redirects.
// It sends a request through http.DefaultTransport, as a client with no transport of its own does,
// unless a bank's TLS keys pin the request's address: then through a transport of its own for that
// address and those banks' keys, which takes no certificate authority's word and checks the server's
// key (checkBankWebServer). Each pin has its own transport, so a connection opened under no pin, at
// first contact, or under keys the banks have since changed, is never reused under another. There is
// one per address and pin the wallet met since it started: a few, as banks seldom change their keys.
type tlsPinningTransport struct { // implements http.RoundTripper
	wallet     *Wallet
	mutex      sync.Mutex
	transports map[string]*http.Transport // by the address and the banks' keys pinned for it
}

// The compiler checks that *tlsPinningTransport implements http.RoundTripper.
var _ http.RoundTripper = (*tlsPinningTransport)(nil)

func (pinning *tlsPinningTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	address := request.URL.Scheme + "://" + request.URL.Host
	banks := pinning.wallet.banksWithTLSKeysAt(address)
	if request.URL.Scheme != "https" || len(banks) == 0 {
		return http.DefaultTransport.RoundTrip(request)
	}
	pin := address
	for _, bank := range banks {
		pin += " " + bank.Name + "=" + strings.Join(bank.TLSKeys, ",")
	}
	pinning.mutex.Lock()
	transport := pinning.transports[pin]
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // no certificate authority: the check is checkBankWebServer's, on every connection
			VerifyConnection:   func(state tls.ConnectionState) error { return checkBankWebServer(address, banks, state) },
		}
		if pinning.transports == nil {
			pinning.transports = map[string]*http.Transport{}
		}
		pinning.transports[pin] = transport
	}
	pinning.mutex.Unlock()
	return transport.RoundTrip(request)
}

// banksWithTLSKeysAt is the accounts of the banks that pin address, a request's scheme://host[:port]:
// the validators of the newest state the wallet verified whose names are not frozen, whose public
// address is address, and which list TLS keys. It is none before the wallet verified a state.
func (wallet *Wallet) banksWithTLSKeysAt(address string) []ledger.Account {
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	if wallet.newestCheckpoint == nil {
		return nil
	}
	var banks []ledger.Account
	for _, validator := range wallet.newestCheckpoint.Validators {
		account, _ := ledger.FindAccount(wallet.newestCheckpoint.State.Accounts, validator.Name)
		if account.PublicAddress == address && len(account.TLSKeys) > 0 && !account.Frozen {
			banks = append(banks, account)
		}
	}
	return banks
}

// checkBankWebServer accepts a TLS connection to address only if the key of the server's certificate
// (the first one it shows: the standard library refuses a server that shows none) is one that each of
// banks lists on chain. Otherwise it names the bank whose record does not list it.
func checkBankWebServer(address string, banks []ledger.Account, state tls.ConnectionState) error {
	keyHash := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
	for _, bank := range banks {
		if !slices.Contains(bank.TLSKeys, hex.EncodeToString(keyHash[:])) {
			return fmt.Errorf("%s's web server at %s shows a key that is not the one on chain: someone may stand between this wallet and the bank, or the bank changed its web server's key without listing the new one first",
				bank.Name, address)
		}
	}
	return nil
}
