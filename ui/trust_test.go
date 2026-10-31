package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// A wallet pins the validators that signed the first state it reads and from then on believes only
// states they signed. A node (or anyone between it and the wallet) that signs its own commit over a
// made-up balance and lists itself as the validator set is refused after a restart: no balance, and
// its receipts do not verify.
func TestWalletBelievesOnlyPinnedValidators(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	honest := newGovernanceNode(t)
	wallet, err := LoadOrCreateWallet(keyfile, "testnet", honest.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	honest.setState([]*signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)},
		ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if balance, err := balanceOf(wallet); err != nil || balance.Cmp(big.NewRat(5, 1)) != 0 {
		t.Fatalf("first read: balance %v, err %v; want 5", balance, err)
	}
	if _, err := os.Stat(keyfile + ".trust"); err != nil {
		t.Fatalf("the first read pinned nothing: %v", err)
	}

	impostor := testNodeSigner(t)
	forged := newGovernanceNode(t)
	forged.setState([]*signing.Signer{impostor}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5000, 1)})
	restarted, err := LoadOrCreateWallet(keyfile, "testnet", forged.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := balanceOf(restarted); err == nil {
		t.Errorf("believed a balance of %s that the pinned validators never signed", balance.RatString())
	}
	if restarted.pinnedKey(nameOf(impostor)) != "" {
		t.Error("a validator the node lists, but the wallet never pinned, vouches for receipts")
	}
}

// An operator's list of validators (name, key and power) pins from the first read: there is no trust on
// first use to hijack.
func TestOperatorPinIsHonouredFromTheFirstRead(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	validator := testNodeSigner(t)
	list := fmt.Sprintf(`{"networkId": %q, "validators": [{"Name": %q, "PublicKey": %q, "Power": 1}]}`, testNetworkID, nameOf(validator), validator.PublicKeyBase64())
	if err := os.WriteFile(keyfile+".trust", []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	testNode := newGovernanceNode(t)
	wallet, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	testNode.setState([]*signing.Signer{testNodeSigner(t)}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5000, 1)})
	if _, err := balanceOf(wallet); err == nil {
		t.Fatal("believed a state signed by a validator the operator's list does not name")
	}
	testNode.setState([]*signing.Signer{validator}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if balance, err := balanceOf(wallet); err != nil || balance.Cmp(big.NewRat(5, 1)) != 0 {
		t.Fatalf("balance %v, err %v; want 5 from the pinned validator's state", balance, err)
	}
	var pinned walletPin
	if data, _ := os.ReadFile(keyfile + ".trust"); json.Unmarshal(data, &pinned) != nil || len(pinned.Validators) != 1 {
		t.Errorf("the operator's list was rewritten: %s", data)
	}
}

// The wallet reads its node's checkpoint as it reads any answer of its node, which it does not trust
// (UI-4): it refuses one with a status of 300 or more, as a node refuses it when it fetches a checkpoint,
// and reads at most node.MaxCheckpointBytes. It used to take a checkpoint from an error answer.
func TestWalletRefusesAFailedCheckpointAnswer(t *testing.T) {
	validator := testNodeSigner(t)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/hdls/status" {
			_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": 10, "networkId": testNetworkID})
			return
		}
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(responseWriter).Encode(signedCheckpoint([]*signing.Signer{validator}, nil))
	}))
	defer server.Close()
	if _, err := newTestWallet(t, server.URL).verifiedState(); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("a checkpoint the node answered with 503: %v; want a refusal naming the status", err)
	}
}

// The wallet reads <keyfile>.trust only at start (UI-3). A list put there while it runs changes nothing
// until it restarts, both before its first use and after it pinned, and each refusal says to restart.
func TestOperatorPinIsReadOnlyAtStart(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	testNode := newGovernanceNode(t)
	start := func() *Wallet {
		wallet, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
		if err != nil {
			t.Fatal(err)
		}
		return wallet
	}
	handOver := func(validator *signing.Signer) { // the operator's list replaces the file, and the node's validators are those
		list := fmt.Sprintf(`{"networkId": %q, "validators": [{"Name": %q, "PublicKey": %q, "Power": 1}]}`, testNetworkID, nameOf(validator), validator.PublicKeyBase64())
		if err := os.WriteFile(keyfile+".trust", []byte(list), 0o600); err != nil {
			t.Fatal(err)
		}
		testNode.setState([]*signing.Signer{validator})
	}
	for _, pinnedBefore := range []bool{false, true} {
		wallet := start()
		if pinnedBefore {
			verified(t, wallet)
		}
		handOver(testNodeSigner(t))
		if _, err := wallet.verifiedState(); err == nil || !strings.Contains(err.Error(), "restart the wallet") {
			t.Errorf("a list put in place while the wallet ran (pinned before: %v): %v; want a refusal that says to restart", pinnedBefore, err)
		}
		verified(t, start())
	}
}

// The quorum counts only pinned validators: keys that joined since the pin neither break it nor make
// it up.
func TestPinnedQuorumIgnoresOtherKeys(t *testing.T) {
	firstPinned, secondPinned, thirdPinned, firstNewcomer, secondNewcomer, thirdNewcomer := testNodeSigner(t), testNodeSigner(t), testNodeSigner(t), testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)
	pinned := consensus.NewValidatorSet(signedCheckpoint([]*signing.Signer{firstPinned, secondPinned, thirdPinned}, nil).Validators)
	for i, testCase := range []struct {
		signers []*signing.Signer
		want    bool
	}{
		{[]*signing.Signer{firstPinned, secondPinned, thirdPinned, firstNewcomer}, true},                    // all pinned, plus a newcomer
		{[]*signing.Signer{firstPinned, secondPinned, firstNewcomer, secondNewcomer, thirdNewcomer}, false}, // two of three pinned, newcomers the majority
		{[]*signing.Signer{firstPinned, secondPinned}, false},                                               // two of three pinned is not more than ⅔
	} {
		if signed := signedByPinned(signedCheckpoint(testCase.signers, nil).Commit, pinned, testNetworkID); signed != testCase.want {
			t.Errorf("case %d: got %v, want %v", i, signed, testCase.want)
		}
	}
}

// A pinned name whose node signs under a new key, after the ⅔ re-keyed that bank, is left out of the
// quorum, not refused (spec 7.2, Q17): the pin holds while the other pinned names hold more than ⅔ of
// the pinned power, and fails once they hold less.
func TestPinnedQuorumLeavesOutARekeyedPinnedName(t *testing.T) {
	validators := []*signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)}
	checkpoint := signedCheckpoint(validators, nil)
	pinned := consensus.NewValidatorSet(checkpoint.Validators) // four of power 1: the quorum is 3
	votes := checkpoint.Commit.Votes
	rekeyed, err := consensus.SignVote(testNodeSigner(t), votes[3]) // the fourth pinned name, under its new key
	if err != nil {
		t.Fatal(err)
	}
	for i, testCase := range []struct {
		votes []consensus.Vote
		want  bool
	}{
		{[]consensus.Vote{votes[0], votes[1], votes[2], rekeyed}, true}, // three of four pinned under their pinned keys
		{[]consensus.Vote{votes[0], votes[1], rekeyed}, false},          // two of four, and the re-keyed one does not count
	} {
		commit := checkpoint.Commit
		commit.Votes = testCase.votes
		if signed := signedByPinned(commit, pinned, testNetworkID); signed != testCase.want {
			t.Errorf("case %d: got %v, want %v", i, signed, testCase.want)
		}
	}
}

// The pin holds each trusted validator's name, key and power (spec 7.2): written on first use from the
// set that signed the first state, and from then on a state counts only when pinned names sign it, each
// under its pinned key. So a node that keeps the real names but signs with keys of its own — the forged
// state of a stolen network — is refused. A pin from an earlier network, a list of validators without
// the network id, with names or without, does not load at all, and nor does a pin of a network of
// another name than the one the wallet was started for.
func TestWalletPinHoldsNameKeyAndPower(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	validators := []*signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)}
	honest := newGovernanceNode(t)
	wallet, err := LoadOrCreateWallet(keyfile, "testnet", honest.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	honest.setState(validators, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if _, err := balanceOf(wallet); err != nil {
		t.Fatal(err)
	}
	var pin walletPin
	if data, err := os.ReadFile(keyfile + ".trust"); err != nil || json.Unmarshal(data, &pin) != nil {
		t.Fatalf("no pin was written: %v", err)
	}
	pinned, want := pin.Validators, signedCheckpoint(validators, nil).Validators
	if len(pinned) != len(want) {
		t.Fatalf("the pin holds %d validators, want %d", len(pinned), len(want))
	}
	for i := range want {
		if pinned[i] != want[i] {
			t.Errorf("pinned validator %d is %+v, want its name, key and power %+v", i, pinned[i], want[i])
		}
	}

	// The same names, but every vote signed under another key, which the set the checkpoint carries gives
	// the name, so that the checkpoint is consistent: the names alone vouch for nothing.
	forgedCheckpoint := signedCheckpoint(validators, []ledger.Account{{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5000, 1)}})
	impostors := map[string]*signing.Signer{}
	for i := range forgedCheckpoint.Validators {
		impostor := testNodeSigner(t)
		impostors[forgedCheckpoint.Validators[i].Name] = impostor
		forgedCheckpoint.Validators[i].PublicKey = impostor.PublicKeyBase64() // as the forger lists them
	}
	forgedCheckpoint.LastBlock.ValidatorsHash = ledger.ValidatorsHash(forgedCheckpoint.Validators)
	forgedCheckpoint.Commit.BlockHash = forgedCheckpoint.LastBlock.BlockHash(forgedCheckpoint.State.Digest())
	for i, vote := range forgedCheckpoint.Commit.Votes {
		vote.BlockHash = forgedCheckpoint.Commit.BlockHash
		forgedCheckpoint.Commit.Votes[i], _ = consensus.SignVote(impostors[vote.Validator], vote)
	}
	if !forgedCheckpoint.Consistent() || !forgedCheckpoint.Commit.Verify(signing.MLDSA65{}, testNetworkID, consensus.NewValidatorSet(forgedCheckpoint.Validators)) {
		t.Fatal("premise: the forged checkpoint is consistent, and its commit a quorum of the set it carries")
	}
	forgedServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(forgedCheckpoint)
	}))
	defer forgedServer.Close()
	restarted, err := LoadOrCreateWallet(keyfile, "testnet", forgedServer.URL, 3)
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := balanceOf(restarted); err == nil {
		t.Errorf("believed a balance of %s signed under keys the pin does not hold for those names", balance.RatString())
	}

	for refusedPin, reason := range map[string]string{
		fmt.Sprintf(`[{"PublicKey": %q, "Power": 1}]`, validators[0].PublicKeyBase64()):                                    "earlier network",
		fmt.Sprintf(`[{"Name": %q, "PublicKey": %q, "Power": 1}]`, nameOf(validators[0]), validators[0].PublicKeyBase64()): "earlier network",
		fmt.Sprintf(`{"networkId": %q, "validators": [{"Name": %q, "PublicKey": %q, "Power": 1}]}`,
			ledger.NetworkID("othernet", [32]byte{1}), nameOf(validators[0]), validators[0].PublicKeyBase64()): `not one named "testnet"`,
	} {
		refused := filepath.Join(t.TempDir(), "refused.json")
		if err := os.WriteFile(refused+".trust", []byte(refusedPin), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateWallet(refused, "testnet", honest.baseURL, 3); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("the pin %.60s… loaded: %v; want a refusal saying %q", refusedPin, err, reason)
		}
	}
}

// A state's accounts are in strictly ascending order of name: the wallet refuses one served out of that
// order before its digest or any signature is checked, even with a commit its validators signed over it.
func TestWalletRefusesAStateOutOfNameOrder(t *testing.T) {
	validator := testNodeSigner(t)
	accounts := withValidatorAccounts([]*signing.Signer{validator}, []ledger.Account{
		{Name: "alice", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)},
		{Name: "bob", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(6, 1)},
	})
	accounts[0], accounts[1] = accounts[1], accounts[0] // bob before alice
	checkpoint := signedBy(node.Checkpoint{State: ledger.LedgerState{Accounts: accounts, Parameters: governanceParameters, GovernanceSequence: governanceSequence, Height: 9},
		Validators: []consensus.Validator{{Name: nameOf(validator), PublicKey: validator.PublicKeyBase64(), Power: 1}}}, []*signing.Signer{validator})
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(checkpoint)
	}))
	defer server.Close()
	if _, err := balanceOf(newTestWallet(t, server.URL)); err == nil || !strings.Contains(err.Error(), "name order") {
		t.Fatalf("the wallet took a state out of name order: %v", err)
	}
}

// At first use, at the bank, the wallet takes the network id its node serves, refuses it unless it is
// one of the network the wallet was started for (the -network name and "/"), checks that the first
// checkpoint's validators hash to its header and signed it for that id, and pins the id with them (spec
// 7.2). From then on it signs with the pinned id, so its payments verify on that network and on no other
// (E20). A node of another network, one that serves an id of the right name that its validators do not
// sign for, or one whose validators are not the block's, gets nothing pinned and nothing signed.
func TestWalletPinsTheNetworkIdOnFirstUse(t *testing.T) {
	testNode := newGovernanceNode(t)
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	wallet, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	shop := testNodeSigner(t)
	testNode.setState(nil, ledger.Account{Name: "bob", PublicKey: wallet.PublicKey(), Amount: big.NewRat(7, 1)}, accountOf(shop, "0"))
	if _, _, err := wallet.Send(nameOf(shop), "1"); err != nil {
		t.Fatal(err)
	}
	var pin walletPin
	if data, err := os.ReadFile(keyfile + ".trust"); err != nil || json.Unmarshal(data, &pin) != nil || pin.NetworkID != testNetworkID || len(pin.Validators) != 1 {
		t.Fatalf("the wallet pinned the network %q and %d validators, want its node's %q and validator", pin.NetworkID, len(pin.Validators), testNetworkID)
	}
	var payment ledger.Payment
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &payment) != nil {
		t.Fatalf("want one payment sent, got %d request(s)", len(posts))
	}
	if !ledger.VerifyPayment(signing.MLDSA65{}, testNetworkID, wallet.PublicKey(), payment) || ledger.VerifyPayment(signing.MLDSA65{}, "testnet", wallet.PublicKey(), payment) {
		t.Error("the wallet's payment is not signed for the pinned network id, and for it only")
	}

	anotherNetwork, anotherNetworkID := newGovernanceNode(t), ledger.NetworkID("othernet", [32]byte{1})
	anotherNetwork.mutex.Lock()
	anotherNetwork.networkID = anotherNetworkID
	anotherNetwork.mutex.Unlock()
	anotherValidator, err := signing.Generate(signing.MLDSA65{}, anotherNetworkID) // its state is signed for its own id
	if err != nil {
		t.Fatal(err)
	}
	anotherNetwork.setState([]*signing.Signer{anotherValidator})
	anotherGenesis := newGovernanceNode(t) // its validators sign for testNetworkID
	anotherGenesis.mutex.Lock()
	anotherGenesis.networkID = ledger.NetworkID("testnet", [32]byte{2})
	anotherGenesis.mutex.Unlock()
	validator := testNodeSigner(t)
	unhashed := signedCheckpoint([]*signing.Signer{validator}, nil)
	unhashed.Validators[0].Power = 2 // a set the commit is a quorum of, but not the one the block's hash covers
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hdls/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": 10, "networkId": testNetworkID})
	})
	serveMux.HandleFunc("/hdls/checkpoint", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(unhashed)
	})
	unhashedNode := httptest.NewServer(serveMux)
	defer unhashedNode.Close()
	for name, nodeURL := range map[string]string{"another network's node": anotherNetwork.baseURL,
		"a node serving an id its validators do not sign for": anotherGenesis.baseURL, "a node whose validators are not the block's": unhashedNode.URL} {
		otherKeyfile := filepath.Join(t.TempDir(), "wallet.json")
		otherWallet, err := LoadOrCreateWallet(otherKeyfile, "testnet", nodeURL, 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := balanceOf(otherWallet); err == nil {
			t.Errorf("%s: the wallet believed its state", name)
		}
		if _, err := os.Stat(otherKeyfile + ".trust"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the wallet pinned it (%v)", name, err)
		}
	}
}

// The wallet keeps the height of the newest state it verified, and the name that state gave its key, in
// <keyfile>.state, and refuses a state older than that height (spec 7.2), even after a restart: its node
// cannot roll it back to before a pending change it showed.
func TestWalletRefusesAnOlderState(t *testing.T) {
	testNode := newGovernanceNode(t)
	keyfile := filepath.Join(t.TempDir(), "wallet.json")
	wallet, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	verified(t, wallet)
	testNode.mutex.Lock()
	testNode.height = 8
	testNode.mutex.Unlock()
	if _, err := wallet.verifiedState(); err == nil || !strings.Contains(err.Error(), "older than the state of block 9") {
		t.Errorf("the wallet took the state of block 8 after block 9's: %v", err)
	}
	restarted, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.lastName() != "alice" {
		t.Errorf("after a restart the wallet's last name is %q, want alice", restarted.lastName())
	}
	if _, err := restarted.verifiedState(); err == nil {
		t.Error("after a restart the wallet took the state of block 8 after block 9's")
	}
	testNode.mutex.Lock()
	testNode.height = 9
	testNode.mutex.Unlock()
	verified(t, restarted)
}

// At first use the wallet asks its node for the network id without holding its mutex, so a slow node
// holds up no other page of the wallet meanwhile.
func TestFirstUseAsksForTheNetworkIdWithoutTheMutex(t *testing.T) {
	validator := testNodeSigner(t)
	var wallet *Wallet
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hdls/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		if !wallet.mutex.TryLock() {
			t.Error("the wallet holds its mutex while it asks for the network id")
		} else {
			wallet.mutex.Unlock()
		}
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": 10, "networkId": testNetworkID})
	})
	serveMux.HandleFunc("/hdls/checkpoint", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(signedCheckpoint([]*signing.Signer{validator}, nil))
	})
	server := httptest.NewServer(serveMux)
	defer server.Close()
	wallet = newTestWallet(t, server.URL)
	verified(t, wallet)
}

// The wallet builds its signer from the seed once, not for every signature: deriving the key costs
// almost half a signature.
func TestWalletBuildsItsSignerOnce(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	verified(t, wallet) // pins the network id the signer signs for
	first, err := wallet.signer()
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := wallet.signer(); second != first {
		t.Error("the wallet built a second signer")
	}
}
