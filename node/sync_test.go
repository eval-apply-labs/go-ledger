package node

import (
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// testTimeSlack widens the real-time deadlines these integration tests assert under. The chaos and
// gossip scenarios are tuned for a developer machine; a starved shared CI runner can execute the
// same schedule ~10x slower (observed twice: a seed that fails there replays green locally in a
// tenth of the wall time). CI sets GOLEDGER_CHAOS_SLACK to stretch every deadline by that factor,
// so the deadlines stay tight everywhere else.
var testTimeSlack = func() time.Duration {
	if v := os.Getenv("GOLEDGER_CHAOS_SLACK"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			return time.Duration(n)
		}
	}
	return 1
}()

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout * testTimeSlack)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A fresh node given a bootstrap peer must fetch that peer's checkpoint, verify its commit
// certificate against the validator set, and fast-forward to the committed state -- not restart at
// genesis. It proves the state is real by holding a balance that only exists post-payment.
func TestNodeSyncsFromCheckpoint(t *testing.T) {
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	recipient, _ := signing.Generate(signing.Ed25519{}, "testnet")
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: a.PublicKeyBase64(), Power: 1}})
	genesis := []ledger.Account{
		{PublicKey: a.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: a.PublicKeyBase64()},
	}

	// Node A: sole validator (quorum 1, so it finalises on its own). Serve its API for sync.
	aCfg := Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis,
	}
	aNode := New(aCfg, NewHTTPTransport(nil))
	aServer := httptest.NewServer(NewServer(aNode).Handler())
	defer aServer.Close()

	pay, _ := ledger.SignPayment(a, ledger.Payment{
		PublicKeyFrom: a.PublicKeyBase64(), PublicKeyTo: recipient.PublicKeyBase64(), Amount: rat("10"), BlockID: 1,
	})
	aNode.SubmitPayment(pay)
	aNode.Start()
	defer aNode.Stop()

	// Wait until A has committed the payment and advanced a few heights.
	waitFor(t, 4*time.Second, func() bool {
		s := aNode.Snapshot()
		return s.Height >= 3 && balance(s.Accounts, recipient.PublicKeyBase64()).Sign() > 0
	}, "A did not commit the payment")

	// Node B: fresh, same validator set, bootstrap = A. It is not itself a validator; it just syncs.
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	bCfg := Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: b, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis, Bootstrap: []string{aServer.URL},
	}
	bNode := New(bCfg, NewHTTPTransport(nil))
	bNode.Start()
	defer bNode.Stop()

	// B must fast-forward past genesis and hold the post-payment state (recipient = 9.9).
	waitFor(t, 4*time.Second, func() bool {
		s := bNode.Snapshot()
		return s.Height >= 2 && balance(s.Accounts, recipient.PublicKeyBase64()).Cmp(rat("9.9")) == 0
	}, "B did not sync to the committed state")
}

// A checkpoint whose certificate does not verify (wrong validator set) must NOT be adopted: the
// node stays at genesis rather than trusting an unverifiable snapshot.
func TestNodeRejectsUnverifiableCheckpoint(t *testing.T) {
	// A is the only validator in ITS set, and commits a payment.
	a, _ := signing.Generate(signing.Ed25519{}, "testnet")
	recipient, _ := signing.Generate(signing.Ed25519{}, "testnet")
	aVals := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: a.PublicKeyBase64(), Power: 1}})
	genesis := []ledger.Account{
		{PublicKey: a.PublicKeyBase64(), Name: "founder", Amount: rat("1000"), ValidatorNode: a.PublicKeyBase64()},
	}
	aNode := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: a, Validators: aVals,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond, Genesis: genesis,
	}, NewHTTPTransport(nil))
	aServer := httptest.NewServer(NewServer(aNode).Handler())
	defer aServer.Close()
	pay, _ := ledger.SignPayment(a, ledger.Payment{PublicKeyFrom: a.PublicKeyBase64(), PublicKeyTo: recipient.PublicKeyBase64(), Amount: rat("10"), BlockID: 1})
	aNode.SubmitPayment(pay)
	aNode.Start()
	defer aNode.Stop()
	waitFor(t, 4*time.Second, func() bool { return aNode.Snapshot().Height >= 3 }, "A did not advance")

	// B's configured validator set is a DIFFERENT validator (some other key), so A's certificate
	// does not verify against it. B must refuse the checkpoint and stay at genesis.
	other, _ := signing.Generate(signing.Ed25519{}, "testnet")
	bVals := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: other.PublicKeyBase64(), Power: 1}})
	b, _ := signing.Generate(signing.Ed25519{}, "testnet")
	bNode := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: b, Validators: bVals,
		TransactionCost: rat("0.1"), Timeout: 5 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: genesis, Bootstrap: []string{aServer.URL},
	}, NewHTTPTransport(nil))
	bNode.Start()
	defer bNode.Stop()

	// Give the sync a moment; B must not have adopted A's (unverifiable-to-B) state.
	time.Sleep(300 * time.Millisecond)
	s := bNode.Snapshot()
	if balance(s.Accounts, recipient.PublicKeyBase64()).Sign() > 0 {
		t.Fatal("B adopted a checkpoint whose certificate does not verify against its validator set")
	}
}
