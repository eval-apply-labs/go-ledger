package main

// A bank's wallet whose key is in the signer program (spec §2.7): the wallet holds the bank's public key
// only, and every signature it makes comes from the signer.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A bank's desk signs through its signer, cmd/signer, built here and run as a bank runs it, with the
// client desk and its domains: registration, name-request-bank and freeze (review/phase2_spec.md §4). A customer's registration that the bank pays for gets the
// bank's two signatures, as requestor and as bank, and both verify under the bank's key: the wallet holds
// no key to make them with. A governance vote is not in the desk's domains: the signer refuses it, and
// the admin page shows the signer's reason.
func TestBanksDeskSignsThroughItsSigner(t *testing.T) {
	signerBinary := buildCommand(t, "signer")
	folder := t.TempDir()
	scheme := signing.NetworkScheme()
	bankPublicKey, bankPrivateKey, err := scheme.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bankPublicKeyBase64 := base64.StdEncoding.EncodeToString(bankPublicKey)

	// The signer signs for the network its genesis file makes, which it reads only for that id. The fake
	// node serves that network, with the bank as its one validator: its commit is the bank's precommit,
	// which the test makes with the bank's key, as the bank's engine would.
	genesisPath := filepath.Join(folder, "genesis.json")
	networkID := writeGenesis(t, genesisPath, bankPublicKeyBase64)
	bankKey, err := signing.NewSigner(scheme, networkID, bankPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	bankName := nameOf(bankKey)
	testNode := newGovernanceNode(t)
	testNode.networkID = networkID
	testNode.setState([]signing.Signer{bankKey}, accountOf(bankKey, "10"))

	keyFilePath, secretPath := filepath.Join(folder, "key.json"), filepath.Join(folder, "desk.secret")
	if err := signing.WriteKeyFile(keyFilePath, bankPublicKey, bankPrivateKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte(hex.EncodeToString(bytes.Repeat([]byte{7}, 32))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0") // a free port for the signer, released just before it starts
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	configPath := filepath.Join(folder, "signer.json")
	writeJSON(t, configPath, map[string]any{
		"listen": address, "allowFrom": []string{"127.0.0.1"}, "genesis": genesisPath, "name": bankName, "keyFile": keyFilePath,
		"signLogPath": filepath.Join(folder, "sign-log.jsonl"), "auditLogPath": filepath.Join(folder, "audit-log.jsonl"),
		"clients": []map[string]any{{"name": "desk", "secretFile": secretPath,
			"domains": []signing.Domain{signing.DomainRegistration, signing.DomainNameRequestBank, signing.DomainFreeze}}},
	})
	signer := exec.Command(signerBinary, "-config", configPath)
	var signerLog bytes.Buffer
	signer.Stdout, signer.Stderr = &signerLog, &signerLog
	if err := signer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = signer.Process.Kill()
		_ = signer.Wait()
		if t.Failed() {
			t.Logf("the signer's log:\n%s", signerLog.String())
		}
	})
	eventually(t, "the signer listening", func() bool {
		connection, err := net.Dial("tcp", address)
		if err == nil {
			connection.Close()
		}
		return err == nil
	})

	bankWalletPath := filepath.Join(folder, "bank.json")
	writeJSON(t, bankWalletPath, map[string]string{"publicKey": bankPublicKeyBase64})
	bank, err := loadWallet(bankWalletPath, "testnet", testNode.baseURL, 3, signerSettings{url: "http://" + address, client: "desk", secretFile: secretPath})
	if err != nil {
		t.Fatal(err)
	}
	customer := newTestWallet(t, testNode.baseURL)
	draft, err := customer.DraftNameRequest(verified(t, customer), requestForm{Kind: ledger.RegistrationKind, Name: "alice", Requestor: bankName})
	if err != nil {
		t.Fatal(err)
	}
	signedByCustomer, err := customer.SignNameRequest(verified(t, customer), draft.Text)
	if err != nil {
		t.Fatal(err)
	}
	signedByBank, err := bank.SignNameRequest(verified(t, bank), signedByCustomer.Text)
	if err != nil || !slices.Equal(signedByBank.Signed, []string{"requestor", "bank"}) {
		t.Fatalf("the bank signed as %v (err %v), want as requestor and bank", signedByBank.Signed, err)
	}
	var registration ledger.NameRequest
	if err := json.Unmarshal([]byte(signedByBank.Text), &registration); err != nil {
		t.Fatal(err)
	}
	if !ledger.VerifyNameRequest(scheme, networkID, bankPublicKeyBase64, bankPublicKeyBase64, registration) {
		t.Error("the registration's signatures do not verify under the bank's key")
	}

	parameterDraft, err := bank.DraftParameters(verified(t, bank), parameterForm{TransactionCost: "0.2"})
	if err != nil {
		t.Fatal(err)
	}
	refusal := redirectError(submitForm(bank, "/admin/parameter", url.Values{"step": {"sign"}, "draft": {parameterDraft.Certificate}}))
	if want := `domain "parameter-vote" is not in this client's list`; !strings.Contains(refusal, want) {
		t.Errorf("the desk's governance vote: the page says %q; want the signer's refusal, %q", refusal, want)
	}
}

// A wallet signs with its own key or through a signer, never both (spec §2.7). With a signer, the wallet
// file must hold a public key only: one with a private key is refused, and a missing one is an error,
// never a new key. The three signer flags go together. Without them, a file holding a public key only
// says how to name the signer.
func TestWalletTakesAKeyOrASignerNotBoth(t *testing.T) {
	folder := t.TempDir()
	scheme := signing.NetworkScheme()
	publicKey, privateKey, err := scheme.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyPath, publicKeyPath, missingPath := filepath.Join(folder, "key.json"), filepath.Join(folder, "public.json"), filepath.Join(folder, "missing.json")
	if err := signing.WriteKeyFile(keyPath, publicKey, privateKey, ""); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, publicKeyPath, map[string]string{"publicKey": base64.StdEncoding.EncodeToString(publicKey)})
	secretPath := filepath.Join(folder, "desk.secret")
	if err := os.WriteFile(secretPath, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signer := signerSettings{url: "http://127.0.0.1:7000", client: "desk", secretFile: secretPath}

	for _, refused := range []struct {
		label, path string
		settings    signerSettings
		reason      string
	}{
		{"a private key and a signer", keyPath, signer, "not both"},
		{"a signer without its client", publicKeyPath, signerSettings{url: signer.url, secretFile: secretPath}, "go together"},
		{"a client secret without a signer", publicKeyPath, signerSettings{client: "desk", secretFile: secretPath}, "go together"},
		{"a signer and no wallet file", missingPath, signer, "must hold the bank's public key"},
		{"a public key only and no signer", publicKeyPath, signerSettings{}, "-signer-url"},
	} {
		if _, err := loadWallet(refused.path, "testnet", "", 3, refused.settings); err == nil || !strings.Contains(err.Error(), refused.reason) {
			t.Errorf("%s: %v; want a refusal saying %q", refused.label, err, refused.reason)
		}
	}
	if _, err := os.Stat(missingPath); err == nil {
		t.Error("a wallet with a signer made a new key file")
	}
	wallet, err := loadWallet(publicKeyPath, "testnet", "", 3, signer)
	if err != nil || wallet.privateKey != nil || wallet.PublicKey() != base64.StdEncoding.EncodeToString(publicKey) {
		t.Errorf("a public key and a signer: %v; want the wallet of that key, with no private key", err)
	}
}

// writeGenesis writes a genesis file for a network named testnet that seats bank-a, with publicKey, and
// returns the network id it makes.
func writeGenesis(t *testing.T, path, publicKey string) string {
	t.Helper()
	parameters := ledger.DefaultParameters()
	writeJSON(t, path, map[string]any{
		"networkName": "testnet",
		"gossipPort":  8070,
		"parameters": map[string]any{
			"transactionCost": "0.1", "blockIntervalMs": 1000, "stepTimeoutMs": 1000, "maxPeers": 0,
			"maxMessageBytes": parameters.MaxMessageBytes, "newKeyFee": "1", "changeDelayBlocks": 86400,
			"retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000",
		},
		"validators": []map[string]any{{"name": "bank-a", "power": 1}},
		"accounts":   []map[string]any{{"name": "bank-a", "publicKey": publicKey, "amount": "10"}},
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := ledger.ParseGenesis(data)
	if err != nil {
		t.Fatal(err)
	}
	chain, _, err := ledger.ValidateGenesis(genesis)
	if err != nil {
		t.Fatal(err)
	}
	return chain.NetworkID
}

// writeJSON writes value to path as JSON.
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
