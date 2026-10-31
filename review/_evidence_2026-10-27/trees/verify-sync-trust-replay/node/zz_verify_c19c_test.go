package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Weak-subjectivity pin = seed snapshotPath with a checkpoint fetched out-of-band from a trusted node
// ("your bank"). Full rotation: genesis A,B,C; live X,Y,Z; attacker holds retired A,B. Logs the
// outcome (pristine: seeded file discarded, joiner captured; fixed: pin loads, forgery refused).
func TestVerifyC19SeededPin(t *testing.T) {
	const net = "village"
	gen := func() *signing.Signer { s, _ := signing.Generate(signing.Ed25519{}, net); return s }
	a, b, c, x, y, z, m := gen(), gen(), gen(), gen(), gen(), gen(), gen()
	A, B, C, X, Y, Z, M := a.PublicKeyBase64(), b.PublicKeyBase64(), c.PublicKeyBase64(), x.PublicKeyBase64(), y.PublicKeyBase64(), z.PublicKeyBase64(), m.PublicKeyBase64()
	params := ledger.LedgerParameters{TransactionCost: rat("0.1"), BlockIntervalMs: 20, StepTimeoutMs: 2000}
	genesis := []ledger.Account{
		{PublicKey: A, Amount: rat("1000"), ValidatorNode: A},
		{PublicKey: B, Amount: rat("1000"), ValidatorNode: B},
		{PublicKey: C, Amount: rat("1000"), ValidatorNode: C},
	}
	live := []ledger.Account{
		{PublicKey: X, Amount: rat("1000"), ValidatorNode: X},
		{PublicKey: Y, Amount: rat("1000"), ValidatorNode: Y},
		{PublicKey: Z, Amount: rat("1000"), ValidatorNode: Z},
	}
	liveSet := []consensus.Validator{{PublicKey: X, Power: 1000}, {PublicKey: Y, Power: 1000}, {PublicKey: Z, Power: 1000}}
	honest := Checkpoint{Accounts: live, Parameters: params,
		Commit:     buildCommit(t, 500, ledger.LedgerState{Accounts: live, Parameters: params}.Digest(), liveSet, map[string]*signing.Signer{X: x, Y: y, Z: z}),
		Validators: liveSet}
	forgedAccounts := []ledger.Account{{PublicKey: M, Amount: rat("9000000000"), ValidatorNode: M}}
	forgedSet := []consensus.Validator{{PublicKey: A, Power: 1}, {PublicKey: B, Power: 1}}
	forged := Checkpoint{Accounts: forgedAccounts, Parameters: params,
		Commit:     buildCommit(t, 900, ledger.LedgerState{Accounts: forgedAccounts, Parameters: params}.Digest(), forgedSet, map[string]*signing.Signer{A: a, B: b}),
		Validators: forgedSet}
	hs, as := serveCheckpoint(honest), serveCheckpoint(forged)
	defer hs.Close()
	defer as.Close()

	pin := filepath.Join(t.TempDir(), "pin.json")
	raw, _ := json.Marshal(honest)
	if err := os.WriteFile(pin, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	j := New(Config{NetworkID: net, Scheme: signing.Ed25519{}, Signer: gen(), ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis, SnapshotPath: pin, Bootstrap: []string{hs.URL, as.URL}}, NewHTTPTransport(nil))
	t.Logf("after New: height %d (seeded pin loaded=%v)", j.height, j.height == 501)
	j.Start()
	defer j.Stop()
	time.Sleep(4 * time.Second)
	s := j.Snapshot()
	t.Logf("after start + 1 catch-up tick: height=%d attacker=%s X=%s", s.Height, balance(s.Accounts, M).RatString(), balance(s.Accounts, X).RatString())
}
