package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseConfig is a minimal well-formed node config (a one-validator testnet). Tests override single
// fields to probe load()'s validation.
func baseConfig(t *testing.T) map[string]any {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	return map[string]any{
		"networkId":          "testnet",
		"listen":             "127.0.0.1:0",
		"publicKey":          pubB64,
		"privateKey":         base64.StdEncoding.EncodeToString(priv),
		"transactionCost":    "0.1",
		"timeoutMs":          3000,
		"minBlockIntervalMs": 1000,
		"validators":         []map[string]any{{"publicKey": pubB64, "power": 3}},
		"genesis": []map[string]any{
			{"publicKey": pubB64, "name": "founder", "amount": "1000", "validatorNode": pubB64},
		},
	}
}

func writeConfig(t *testing.T, c map[string]any) string {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "node.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadErr asserts load() fails and that the error mentions the offending field, so an operator can
// tell WHAT to fix from the message alone.
func loadErr(t *testing.T, c map[string]any, wantSubstring string) {
	t.Helper()
	_, _, _, err := load(writeConfig(t, c))
	if err == nil {
		t.Fatalf("load accepted a config that must be refused (want error mentioning %q)", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("error %q does not mention %q", err, wantSubstring)
	}
}

func TestLoadValidConfig(t *testing.T) {
	cfg, listen, _, err := load(writeConfig(t, baseConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	if listen != "127.0.0.1:0" {
		t.Errorf("listen = %q", listen)
	}
	if cfg.Validators == nil || cfg.Validators.TotalPower() != 3 {
		t.Errorf("fixed validator set not loaded")
	}
	if cfg.TransactionCost.RatString() != "1/10" {
		t.Errorf("transactionCost = %s", cfg.TransactionCost.RatString())
	}
}

// C1 regression (external review): dynamic-validator mode (validatorThreshold set) deliberately
// leaves the static set nil — the set is derived from stake each block. Startup used to dereference
// the nil set in its log line and panic before serving a single request.
func TestLoadDynamicValidatorModeLeavesStaticSetNil(t *testing.T) {
	c := baseConfig(t)
	c["validatorThreshold"] = "100"
	cfg, _, _, err := load(writeConfig(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Validators != nil {
		t.Fatal("dynamic mode must leave the static validator set nil (derived from stake at runtime)")
	}
	if cfg.ValidatorThreshold == nil || cfg.ValidatorThreshold.RatString() != "100" {
		t.Fatal("validatorThreshold must be parsed")
	}
	if got := validatorsLabel(cfg.Validators); got != "dynamic (stake-derived)" {
		t.Fatalf("validatorsLabel(nil) = %q — the startup log must not dereference a nil set", got)
	}
}

// C3 regression (external review): a negative genesis transactionCost would mint |cost| on every
// payment (payees are credited Amount-cost) and re-admit negative amounts. Governance can never
// vote one in; the genesis value must be refused at load.
func TestLoadRejectsNegativeTransactionCost(t *testing.T) {
	c := baseConfig(t)
	c["transactionCost"] = "-1"
	loadErr(t, c, "negative")
}

func TestLoadRejectsMalformedTransactionCost(t *testing.T) {
	c := baseConfig(t)
	c["transactionCost"] = "abc"
	loadErr(t, c, "transactionCost")
}

// C4 regression (external review): a truncated key must fail at load. It used to start a node that
// served HTTP but silently never voted or proposed (the driver swallows signing errors) — a
// consensus ghost that looks up.
func TestLoadRejectsTruncatedKeys(t *testing.T) {
	half := base64.StdEncoding.EncodeToString(make([]byte, 16))

	c := baseConfig(t)
	c["publicKey"] = half
	loadErr(t, c, "publicKey")

	c = baseConfig(t)
	c["privateKey"] = half
	loadErr(t, c, "privateKey")
}

func TestLoadRejectsMalformedGenesisAmount(t *testing.T) {
	c := baseConfig(t)
	c["genesis"] = []map[string]any{{"publicKey": "X", "amount": "not-a-number"}}
	loadErr(t, c, "genesis amount")
}

// The genesis parameters must clear the same Acceptable() bar as every later governance change: a
// negative block interval (committed into the genesis digest) is a malformed profile governance
// could never have produced.
func TestLoadRejectsNegativeBlockInterval(t *testing.T) {
	c := baseConfig(t)
	c["minBlockIntervalMs"] = -5
	loadErr(t, c, "genesis parameters")
}
