package main

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The demo's wallet file holds the founder key in plaintext, so it is created exclusively: it must
// not write through a link planted at its path (into a file the planter chose), nor into a file that
// already exists (whose mode and owner it does not control).
func TestWriteWalletCreatesExclusively(t *testing.T) {
	publicKey, privateKey, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()

	target := filepath.Join(directory, "planter-chosen")
	link := filepath.Join(directory, "linked.json")
	if err := os.Symlink(target, link); err != nil {
		t.Logf("symlinks unsupported here, skipping that case: %v", err)
	} else {
		if err := writeWallet(link, publicKey, privateKey); err == nil {
			t.Error("writeWallet wrote through a planted symlink")
		}
		if _, err := os.Stat(target); err == nil {
			t.Error("the private key landed in the symlink's target")
		}
	}

	existing := filepath.Join(directory, "existing.json")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWallet(existing, publicKey, privateKey); err == nil {
		t.Error("writeWallet reused a file that already existed")
	}

	fresh := filepath.Join(directory, "wallet.json")
	if err := writeWallet(fresh, publicKey, privateKey); err != nil {
		t.Fatal(err)
	}
	if fileInformation, err := os.Stat(fresh); err != nil || fileInformation.Mode().Perm() != 0o600 {
		t.Errorf("fresh wallet file: %v; want mode 0600", err)
	}
}

// The demo's genesis passes the checks a node's genesis file does (spec 8.3): the founder is the only
// seated name, with power 1 and no bank of record, and the bakery and the cafe are customers of the
// founder's bank. The founder publishes its node as its public address, and the node's IP address,
// and the gossip port is the node's port.
// The parameters are the defaults, except a key change that waits 20 blocks, so it shows within a
// minute, and a record kept 1000 blocks. -network must be a name, and -listen an IP address and a
// port. With an unspecified IP address (0.0.0.0 or ::), listening on every interface, the founder
// publishes 127.0.0.1, which names this machine, as its IP address and in its public address.
func TestDemoGenesisIsValid(t *testing.T) {
	var keys [3][]byte
	for i := range keys {
		publicKey, _, err := signing.NetworkScheme().GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = publicKey
	}
	chain, state, err := ledger.ValidateGenesis(demoGenesis("village", "127.0.0.1:8080", keys[0], keys[1], keys[2]))
	if err != nil {
		t.Fatalf("the demo's genesis is refused: %v", err)
	}
	if !strings.HasPrefix(chain.NetworkID, "village/") {
		t.Errorf("the network id %q does not start with the network name", chain.NetworkID)
	}
	seated := chain.ValidatorRule.SetFor(state).Validators()
	if len(seated) != 1 || seated[0].Name != "founder" || seated[0].Power != 1 {
		t.Errorf("the demo seats %d names under the rule %+v, want the founder alone, with power 1", len(seated), chain.ValidatorRule)
	}
	if founder, _ := ledger.FindAccount(state.Accounts, "founder"); founder.PublicAddress != "http://127.0.0.1:8080" ||
		!slices.Equal(founder.IPAddresses, []string{"127.0.0.1"}) || chain.GossipPort != 8080 {
		t.Errorf("the founder publishes %q and %v, on the gossip port %d; want its node, http://127.0.0.1:8080, its IP address 127.0.0.1 and its port 8080",
			founder.PublicAddress, founder.IPAddresses, chain.GossipPort)
	}
	for name, wantBank := range map[string]string{"founder": "", "bakery": "founder", "cafe": "founder"} {
		if account, found := ledger.FindAccount(state.Accounts, name); !found || account.Bank != wantBank {
			t.Errorf("%s has the bank %q (found: %v), want %q", name, account.Bank, found, wantBank)
		}
	}
	want := ledger.DefaultParameters()
	want.ChangeDelayBlocks, want.RetainBlocks = 20, 1000
	if state.Parameters.Digest() != want.Digest() {
		t.Errorf("the demo's parameters are %+v, want the defaults with a change delay of 20 and a retention of 1000", state.Parameters)
	}

	if _, _, err := ledger.ValidateGenesis(demoGenesis("Village", "127.0.0.1:8080", keys[0], keys[1], keys[2])); err == nil {
		t.Error("the demo accepted a network name that is not a name")
	}
	for _, listen := range []string{"localhost:8080", ":8080", "127.0.0.1"} {
		if _, _, err := ledger.ValidateGenesis(demoGenesis("village", listen, keys[0], keys[1], keys[2])); err == nil {
			t.Errorf("the demo accepted -listen %q, which is not an IP address and a port", listen)
		}
	}
	for _, listen := range []string{"0.0.0.0:8080", "[::]:8080"} {
		chain, state, err := ledger.ValidateGenesis(demoGenesis("village", listen, keys[0], keys[1], keys[2]))
		if err != nil {
			t.Errorf("the demo refused -listen %s, every interface: %v", listen, err)
			continue
		}
		if founder, _ := ledger.FindAccount(state.Accounts, "founder"); founder.PublicAddress != "http://127.0.0.1:8080" ||
			!slices.Equal(founder.IPAddresses, []string{"127.0.0.1"}) || chain.GossipPort != 8080 {
			t.Errorf("with -listen %s the founder publishes %q and the IP addresses %v, on the gossip port %d; want http://127.0.0.1:8080, 127.0.0.1 and 8080",
				listen, founder.PublicAddress, founder.IPAddresses, chain.GossipPort)
		}
	}
}

// The demo's node has the limits and timeouts of a cmd/server node: the pool and comment caps that
// cmd/server's load gives by default (0 would mean no limit), and the HTTP timeouts its main sets
// (findings #10).
func TestDemoNodeHasTheServerLimitsAndTimeouts(t *testing.T) {
	config := demoNodeConfig(ledger.Chain{}, ledger.LedgerState{}, nil, "snapshot.json")
	if config.MaxMempool != 10000 || config.MaxCommentLength != 280 {
		t.Errorf("the demo node's pool cap is %d and its comment cap %d, want cmd/server's defaults 10000 and 280", config.MaxMempool, config.MaxCommentLength)
	}
	httpServer := demoHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if httpServer.ReadHeaderTimeout != 5*time.Second || httpServer.ReadTimeout != 15*time.Second ||
		httpServer.WriteTimeout != 30*time.Second || httpServer.IdleTimeout != 2*time.Minute {
		t.Errorf("the demo's HTTP timeouts are %v, %v, %v and %v, want cmd/server's 5s, 15s, 30s and 2m",
			httpServer.ReadHeaderTimeout, httpServer.ReadTimeout, httpServer.WriteTimeout, httpServer.IdleTimeout)
	}
}
