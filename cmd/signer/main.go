// Command signer holds a bank's one key and signs for the bank's own programs, its clients: the engine,
// the bank's desk and its admin page (spec §2). It never signs raw bytes: a client sends a domain and the
// fields the ledger signs in it, and the signer checks them against the domain's layout, and that they
// name the bank wherever they name who signs, before it signs them for its own network (layout.go). It
// gives each slot, a vote's height, round and type or a proposal's height and round, at most one
// signature, written to its sign log and fsynced before it is made (signlog.go), and it writes every
// signature and every refusal to its audit log, for people (service.go).
//
// It serves one route, POST /sign, on an address of the bank's private network, and accepts connections
// from the IP addresses in its allowFrom list only. A request carries the header Authorization:
// HDLS-HMAC <client> <lowercase hex of HMAC-SHA256(client secret, body)> and the body {"domain": "...",
// "fields": ["<base64>", ...]}; the answer is {"signature": "<base64>"}, or a refusal (see handleSign).
//
// A signer without a sign log refuses every vote and proposal (spec §2.6) until it is started once with
// -resume-above <height>, read off the network's status (any bank's /hdls/status): that writes a new sign
// log, which refuses every slot at or below that height. It is refused where a sign log exists.
//
// Usage:
//
//	signer -config signer.json
//	signer -config signer.json -resume-above <height>
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func main() {
	configPath := flag.String("config", "signer.json", "path to the signer config JSON")
	resumeAbove := flag.Int64("resume-above", -1, "write a new sign log that refuses every vote and proposal at or below this height, "+
		"read off the network's status; only where there is no sign log")
	flag.Parse()

	service, listener, allowFrom, err := start(*configPath, *resumeAbove)
	if err != nil {
		log.Fatal(err)
	}
	// The key is named by the SHA-256 of its bytes, the name of its record in the public folder
	// (hdls/resolve/key/<hex>), so an operator can check it is the bank's key on chain.
	keyDigest := sha256.Sum256(service.keySigner.PublicKey())
	log.Printf("hypernode signer up: listen=%s allowFrom=%v name=%q network=%q key=%x clients=%d",
		listener.Addr(), allowFrom, service.name, service.networkID, keyDigest, len(service.clients))
	switch {
	case service.signLog == nil:
		log.Printf("no sign log: every vote and proposal is refused until the signer is started with -resume-above <height>, " +
			"read off the network's status")
	case *resumeAbove >= 0:
		log.Printf("a new sign log refuses every vote and proposal at or below height %d", *resumeAbove)
	}
	httpServer := &http.Server{
		Handler: service,
		// Per-connection deadlines: a client that stalls mid-request or never reads its answer is
		// disconnected. The engine keeps its connection open from one signature to the next.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    8 << 10, // the one header it reads is about a hundred bytes
	}
	log.Fatal(httpServer.Serve(&allowFromListener{Listener: listener, allowFrom: allowFrom}))
}

// fileConfig is the signer's config file (spec §5).
type fileConfig struct {
	Listen         string         `json:"listen"`         // the address it serves on, on the bank's private network
	AllowFrom      []string       `json:"allowFrom"`      // the IP addresses or prefixes it accepts connections from
	Genesis        string         `json:"genesis"`        // the genesis file, whose hash makes the network id it signs for
	Name           string         `json:"name"`           // the bank's name: the one name it signs in
	KeyFile        string         `json:"keyFile"`        // the bank's key file (signing.ReadKeyFile)
	PassphraseFile string         `json:"passphraseFile"` // the passphrase of a sealed key file; "" for a plaintext one
	SignLogPath    string         `json:"signLogPath"`
	AuditLogPath   string         `json:"auditLogPath"`
	Clients        []clientConfig `json:"clients"`
}

// clientConfig is one client in the config: its name, the file holding its secret, and the domains it
// may ask for (spec §2.3).
type clientConfig struct {
	Name       string           `json:"name"`
	SecretFile string           `json:"secretFile"` // 64 lowercase hex digits, as keygen -secret prints them
	Domains    []signing.Domain `json:"domains"`
}

// start reads the config at configPath, binds its listen address, locks the sign log's folder
// (lockFolder), and only then builds the service, which reads the sign log and writes it back. So a
// second start of a running signer, by an operator reading its key's digest or by a second container on
// its volume, fails at the bind, as cmd/server's bindThenStart does, or at the lock, before it touches
// the sign log the running one appends to: rewritten under it, the log would lose the slots signed in
// between, and a restart would sign them again. The lock is held while the program runs.
func start(configPath string, resumeAbove int64) (service *SigningService, listener net.Listener, allowFrom []netip.Prefix, err error) {
	configFile, allowFrom, err := load(configPath, resumeAbove)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("config: %w", err)
	}
	if listener, err = net.Listen("tcp", configFile.Listen); err != nil {
		return nil, nil, nil, err
	}
	signLogFolder, err := lockFolder(filepath.Dir(configFile.SignLogPath))
	if err != nil {
		listener.Close()
		return nil, nil, nil, err
	}
	if service, err = newSigningService(configFile, resumeAbove); err != nil {
		listener.Close()
		signLogFolder.Close()
		return nil, nil, nil, fmt.Errorf("config: %w", err)
	}
	service.signLogFolder = signLogFolder
	return service, listener, allowFrom, nil
}

// load reads the config file at configPath, checks it, and returns it with the prefixes it accepts
// connections from. It writes nothing.
func load(configPath string, resumeAbove int64) (configFile fileConfig, allowFrom []netip.Prefix, err error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return configFile, nil, err
	}
	// An unknown key is refused, so a misspelt field is never silently ignored.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configFile); err != nil {
		return configFile, nil, fmt.Errorf("parse config: %w", err)
	}
	switch {
	case configFile.Listen == "" || configFile.Genesis == "" || configFile.KeyFile == "" || configFile.SignLogPath == "" || configFile.AuditLogPath == "":
		return configFile, nil, errors.New("listen, genesis, keyFile, signLogPath and auditLogPath are required")
	case !ledger.CanonicalName(configFile.Name):
		return configFile, nil, fmt.Errorf("name %q is not a name: 1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or a digit", configFile.Name)
	case len(configFile.AllowFrom) == 0:
		return configFile, nil, errors.New("allowFrom is empty: the signer would refuse every connection")
	case resumeAbove < -1:
		return configFile, nil, fmt.Errorf("-resume-above %d: want a height from 0 up", resumeAbove)
	}
	allowFrom, err = allowFromPrefixes(configFile.AllowFrom)
	return configFile, allowFrom, err
}

// newSigningService builds the service of a config: the network id its genesis makes, the key its key
// file holds, its clients and its sign log. With resumeAbove from 0 up, it writes a new sign log that
// refuses every slot at or below that height, and refuses to where one exists; with -1 it reads the sign
// log there is, and writes it back, or runs without one.
func newSigningService(configFile fileConfig, resumeAbove int64) (*SigningService, error) {
	genesisData, err := os.ReadFile(configFile.Genesis)
	if err != nil {
		return nil, fmt.Errorf("genesis: %w", err)
	}
	genesis, err := ledger.ParseGenesis(genesisData)
	if err != nil {
		return nil, err
	}
	chain, _, err := ledger.ValidateGenesis(genesis) // its hash makes the network id every signature binds
	if err != nil {
		return nil, err
	}
	keySigner, err := readKey(configFile.KeyFile, configFile.PassphraseFile, chain.NetworkID)
	if err != nil {
		return nil, err
	}
	clients, err := readClients(configFile.Clients)
	if err != nil {
		return nil, err
	}
	signLog, err := openSignLog(configFile.SignLogPath, chain.NetworkID, resumeAbove)
	if err != nil {
		return nil, err
	}
	return &SigningService{
		keySigner:    keySigner,
		networkID:    chain.NetworkID,
		name:         configFile.Name,
		clients:      clients,
		signLog:      signLog,
		auditLogPath: configFile.AuditLogPath,
		stopProgram:  log.Fatalf,
	}, nil
}

// readKey reads the bank's key from its key file, unsealed with the passphrase in passphraseFilePath,
// and makes the signer that signs with it for networkID. The key file's public key, which is outside the
// seal, must be the one its private key derives. The signer does not check the key against the chain,
// which it does not read: the engine's self-test does (spec §2.7), and every client checks every answer.
func readKey(keyFilePath, passphraseFilePath, networkID string) (signing.Signer, error) {
	keyFile, err := signing.ReadKeyFile(keyFilePath)
	if err != nil {
		return nil, fmt.Errorf("keyFile: %w", err)
	}
	passphrase := ""
	switch {
	case keyFile.Encrypted != nil && passphraseFilePath == "":
		return nil, fmt.Errorf("keyFile %s is sealed, and passphraseFile is not set", keyFilePath)
	case keyFile.Encrypted == nil && passphraseFilePath != "":
		return nil, fmt.Errorf("passphraseFile is set, but keyFile %s holds its key in plaintext (keygen -encrypt writes a sealed one)", keyFilePath)
	case keyFile.Encrypted == nil:
		log.Printf("keyFile %s holds its key in plaintext: fine for a test, not for a bank (keygen -encrypt writes a sealed one)", keyFilePath)
	default:
		if passphrase, err = signing.ReadPassphraseFile(passphraseFilePath); err != nil {
			return nil, fmt.Errorf("passphraseFile: %w", err)
		}
	}
	privateKey, err := keyFile.Unlock(passphrase)
	if err != nil {
		return nil, fmt.Errorf("keyFile %s: %w", keyFilePath, err)
	}
	keySigner, err := signing.NewSigner(signing.NetworkScheme(), networkID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("keyFile %s: not a private key of the network's scheme, %s: %w", keyFilePath, signing.NetworkScheme().Name(), err)
	}
	if keySigner.PublicKeyBase64() != keyFile.PublicKey {
		return nil, fmt.Errorf("keyFile %s: its publicKey is not the key its private key derives", keyFilePath)
	}
	return keySigner, nil
}

// readClients reads the config's clients, each with its secret. Each must have a name of its own, a
// secret of its own (or one client could ask as another, with the other's domains) and only domains the
// signer has a layout for: never name-request-new-key or reassignment-new-key, which a customer's own
// new key signs.
func readClients(clientConfigs []clientConfig) (map[string]client, error) {
	if len(clientConfigs) == 0 {
		return nil, errors.New("clients is empty: the signer would answer no one")
	}
	clients := map[string]client{}
	for _, clientEntry := range clientConfigs {
		if !ledger.CanonicalName(clientEntry.Name) {
			return nil, fmt.Errorf("client name %q: want 1 to 32 lowercase letters, digits and hyphens, such as engine, desk or admin", clientEntry.Name)
		}
		if _, listed := clients[clientEntry.Name]; listed {
			return nil, fmt.Errorf("client %q is listed twice", clientEntry.Name)
		}
		secret, err := signing.ReadSecretFile(clientEntry.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("client %q: %w", clientEntry.Name, err)
		}
		for otherName, other := range clients {
			if hmac.Equal(other.secret, secret) {
				return nil, fmt.Errorf("clients %q and %q have one secret: each needs its own, or one could ask as the other", otherName, clientEntry.Name)
			}
		}
		for _, domain := range clientEntry.Domains {
			if _, signable := layouts[domain]; !signable {
				return nil, fmt.Errorf("client %q: the signer signs nothing in domain %q", clientEntry.Name, domain)
			}
		}
		clients[clientEntry.Name] = client{secret: secret, domains: clientEntry.Domains}
	}
	return clients, nil
}

// allowFromPrefixes reads the config's allowFrom entries, as cmd/server reads its allow entries: each an
// IP address, which stands for itself, or a prefix, written as the listener compares it: an IPv4
// address as IPv4, never in IPv6 dress, no zone, and no bit set past a prefix's length. Anything else
// is refused, so a mistyped entry never shuts a client out, or lets a stranger in, unnoticed.
func allowFromPrefixes(entries []string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, entry := range entries {
		address, err := netip.ParseAddr(entry)
		prefix := netip.PrefixFrom(address, address.BitLen())
		if strings.Contains(entry, "/") {
			prefix, err = netip.ParsePrefix(entry)
			address = prefix.Addr()
		}
		if err != nil || address.Zone() != "" || address.Is4In6() || prefix != prefix.Masked() {
			return nil, fmt.Errorf("allowFrom: %q is not an IP address or prefix as the signer compares it: write one as 192.0.2.7, "+
				"198.51.100.0/24 or 2001:db8::/48, an IPv4 address as IPv4, with no zone, and no bit set past a prefix's length", entry)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// allowFromListener refuses, at accept, every connection whose remote IP address is not in allowFrom
// (spec §2.2), as the engine's gossip port refuses addresses off its allow list (node/allowlist.go): it
// closes the connection before reading a byte of it, so no HTTP is parsed for a stranger. It logs the
// refusals once a minute, while there are any, with the last address refused.
type allowFromListener struct { // implements net.Listener
	net.Listener                              // the listener of listen, whose Close and Addr this one keeps
	allowFrom          []netip.Prefix         // the config's allowFrom
	refusals           atomic.Int64           // the connections refused since the last report
	lastRefusedAddress atomic.Pointer[string] // the remote address of the last connection refused
}

// The compiler checks that *allowFromListener implements net.Listener.
var _ net.Listener = (*allowFromListener)(nil)

// Accept returns the next connection from an address in allowFrom, and closes every other one as it
// comes. An IPv4 address in IPv6 dress (::ffff:a.b.c.d), which is how a dual-stack socket reports an
// IPv4 caller, counts as the IPv4 address, and a zone, which no entry has, is dropped.
func (listener *allowFromListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if tcpAddress, isTCP := connection.RemoteAddr().(*net.TCPAddr); isTCP {
			address := tcpAddress.AddrPort().Addr().WithZone("").Unmap()
			if slices.ContainsFunc(listener.allowFrom, func(prefix netip.Prefix) bool { return prefix.Contains(address) }) {
				return connection, nil
			}
		}
		remoteAddress := connection.RemoteAddr().String()
		connection.Close()
		listener.lastRefusedAddress.Store(&remoteAddress)
		if listener.refusals.Add(1) == 1 {
			time.AfterFunc(time.Minute, listener.reportRefusals)
		}
	}
}

// reportRefusals logs how many connections were refused since the last report, and starts the count
// again.
func (listener *allowFromListener) reportRefusals() {
	log.Printf("refused %d connections in the last minute from addresses not in allowFrom, the last from %s",
		listener.refusals.Swap(0), *listener.lastRefusedAddress.Load())
}
