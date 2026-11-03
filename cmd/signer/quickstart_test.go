package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Each bank's signer config that cmd/quickstart writes is one the signer loads (spec §5), in the
// quickstart's folder, where deploy/compose.yaml's signer finds the same files under the same names (it
// runs in /quickstart): the key file opens with the passphrase file, the clients' secrets read, and the
// sign log is the new network's. So the engine, asking under its secret, gets its first vote signed,
// with no -resume-above, under the key the bank's wallet file holds.
func TestQuickstartSignersLoad(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "quickstart")
	if output, err := exec.Command("go", "run", "../quickstart", "-folder", folder).CombinedOutput(); err != nil {
		t.Fatalf("go run ../quickstart: %v: %s", err, output)
	}
	t.Chdir(folder)
	for _, bank := range []string{"bank-a", "bank-b", "bank-c", "bank-d"} {
		configFile, allowFrom, err := load(bank+".signer.json", -1)
		if err != nil {
			t.Fatalf("%s's signer config: %v", bank, err)
		}
		service, err := newSigningService(configFile, -1)
		if err != nil {
			t.Fatalf("%s's signer: %v", bank, err)
		}
		if service.name != bank || configFile.Listen != ":7000" || len(allowFrom) != 1 {
			t.Errorf("%s's signer signs as %q, listens on %s and lets in %v; want its name, :7000 and its signing network", bank, service.name, configFile.Listen, allowFrom)
		}
		walletData, err := os.ReadFile(bank + ".wallet.json")
		if err != nil {
			t.Fatal(err)
		}
		var wallet struct{ PublicKey string }
		if err := json.Unmarshal(walletData, &wallet); err != nil {
			t.Fatal(err)
		}
		publicKey, _ := base64.StdEncoding.DecodeString(wallet.PublicKey)
		engineSecret, err := signing.ReadSecretFile(bank + ".engine.secret")
		if err != nil {
			t.Fatal(err)
		}
		engineSigner := signing.NewRemoteSigner(serveSigner(t, service).URL, "engine", engineSecret, publicKey, service.networkID, signing.NetworkScheme())
		if _, err := consensus.SignVote(engineSigner, consensus.Vote{Height: 1, Type: consensus.Prevote, Validator: bank}); err != nil {
			t.Errorf("%s's engine's first vote: %v", bank, err)
		}
	}
}
