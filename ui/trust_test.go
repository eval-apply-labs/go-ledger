package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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
	honest.setState([]signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)},
		ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	if balance, err := balanceOf(wallet); err != nil || balance.Cmp(big.NewRat(5, 1)) != 0 {
		t.Fatalf("first read: balance %v, err %v; want 5", balance, err)
	}
	if _, err := os.Stat(keyfile + ".trust"); err != nil {
		t.Fatalf("the first read pinned nothing: %v", err)
	}

	impostor := testNodeSigner(t)
	forged := newGovernanceNode(t)
	forged.setState([]signing.Signer{impostor}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5000, 1)})
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
	testNode.setState([]signing.Signer{testNodeSigner(t)}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5000, 1)})
	if _, err := balanceOf(wallet); err == nil {
		t.Fatal("believed a state signed by a validator the operator's list does not name")
	}
	testNode.setState([]signing.Signer{validator}, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
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
		_ = json.NewEncoder(responseWriter).Encode(signedCheckpoint([]signing.Signer{validator}, nil))
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
	handOver := func(validator signing.Signer) { // the operator's list replaces the file, and the node's validators are those
		list := fmt.Sprintf(`{"networkId": %q, "validators": [{"Name": %q, "PublicKey": %q, "Power": 1}]}`, testNetworkID, nameOf(validator), validator.PublicKeyBase64())
		if err := os.WriteFile(keyfile+".trust", []byte(list), 0o600); err != nil {
			t.Fatal(err)
		}
		testNode.setState([]signing.Signer{validator})
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
	pinned := consensus.NewValidatorSet(signedCheckpoint([]signing.Signer{firstPinned, secondPinned, thirdPinned}, nil).Validators)
	for i, testCase := range []struct {
		signers []signing.Signer
		want    bool
	}{
		{[]signing.Signer{firstPinned, secondPinned, thirdPinned, firstNewcomer}, true},                    // all pinned, plus a newcomer
		{[]signing.Signer{firstPinned, secondPinned, firstNewcomer, secondNewcomer, thirdNewcomer}, false}, // two of three pinned, newcomers the majority
		{[]signing.Signer{firstPinned, secondPinned}, false},                                               // two of three pinned is not more than ⅔
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
	validators := []signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)}
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
	validators := []signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)}
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
	impostors := map[string]signing.Signer{}
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
	if !forgedCheckpoint.Consistent() || !forgedCheckpoint.Commit.Verify(signing.NetworkScheme(), testNetworkID, consensus.NewValidatorSet(forgedCheckpoint.Validators)) {
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
	accounts := withValidatorAccounts([]signing.Signer{validator}, []ledger.Account{
		{Name: "alice", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(5, 1)},
		{Name: "bob", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: big.NewRat(6, 1)},
	})
	accounts[0], accounts[1] = accounts[1], accounts[0] // bob before alice
	checkpoint := signedBy(node.Checkpoint{State: ledger.LedgerState{Accounts: accounts, Parameters: governanceParameters, GovernanceSequence: governanceSequence, Height: 9},
		Validators: []consensus.Validator{{Name: nameOf(validator), PublicKey: validator.PublicKeyBase64(), Power: 1}}}, []signing.Signer{validator})
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
	if !ledger.VerifyPayment(signing.NetworkScheme(), testNetworkID, wallet.PublicKey(), payment) || ledger.VerifyPayment(signing.NetworkScheme(), "testnet", wallet.PublicKey(), payment) {
		t.Error("the wallet's payment is not signed for the pinned network id, and for it only")
	}

	anotherNetwork, anotherNetworkID := newGovernanceNode(t), ledger.NetworkID("othernet", [32]byte{1})
	anotherNetwork.mutex.Lock()
	anotherNetwork.networkID = anotherNetworkID
	anotherNetwork.mutex.Unlock()
	anotherValidator, err := signing.Generate(signing.NetworkScheme(), anotherNetworkID) // its state is signed for its own id
	if err != nil {
		t.Fatal(err)
	}
	anotherNetwork.setState([]signing.Signer{anotherValidator})
	anotherGenesis := newGovernanceNode(t) // its validators sign for testNetworkID
	anotherGenesis.mutex.Lock()
	anotherGenesis.networkID = ledger.NetworkID("testnet", [32]byte{2})
	anotherGenesis.mutex.Unlock()
	validator := testNodeSigner(t)
	unhashed := signedCheckpoint([]signing.Signer{validator}, nil)
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
// <keyfile>.state, and never shows a state older than that height as current (spec 7.2), even after a
// restart: its node cannot roll it back to before a pending change it showed. While it runs, it shows the
// newest state it kept instead; after a restart, which kept none, it refuses the older state.
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
	if checkpoint := verified(t, wallet); checkpoint.State.Height != 9 {
		t.Errorf("the wallet shows the state of block %d after block 9's, want the kept state of block 9", checkpoint.State.Height)
	}
	restarted, err := LoadOrCreateWallet(keyfile, "testnet", testNode.baseURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.lastName() != "alice" {
		t.Errorf("after a restart the wallet's last name is %q, want alice", restarted.lastName())
	}
	if _, err := restarted.verifiedState(); err == nil || !strings.Contains(err.Error(), "older than the state of block 9") {
		t.Errorf("after a restart the wallet took the state of block 8 after block 9's: %v", err)
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
		_ = json.NewEncoder(responseWriter).Encode(signedCheckpoint([]signing.Signer{validator}, nil))
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

// seat is a validator's name and the signer its node signs with: a re-key gives the name a new signer.
type seat struct {
	name   string
	signer signing.Signer
}

// seatedCheckpoint is a checkpoint at height whose state gives each carried seat an account under its
// signer's key, and whose validators are those seats, power 1 each: both given in name order. Its commit
// holds a vote of each of the signing seats, under its signer, for the block that produced that state.
func seatedCheckpoint(height int64, carried, signers []seat, accounts ...ledger.Account) node.Checkpoint {
	checkpoint := node.Checkpoint{State: ledger.LedgerState{Parameters: governanceParameters, GovernanceSequence: governanceSequence, Height: height}}
	for _, validator := range carried {
		checkpoint.Validators = append(checkpoint.Validators, consensus.Validator{Name: validator.name, PublicKey: validator.signer.PublicKeyBase64(), Power: 1})
		accounts = append(accounts, ledger.Account{Name: validator.name, PublicKey: validator.signer.PublicKeyBase64(), Amount: new(big.Rat)})
	}
	slices.SortFunc(accounts, func(first, second ledger.Account) int { return strings.Compare(first.Name, second.Name) })
	checkpoint.State.Accounts = accounts
	checkpoint.LastBlock = ledger.BlockHeader{Height: height, ValidatorsHash: ledger.ValidatorsHash(checkpoint.Validators)}
	checkpoint.Commit = consensus.Commit{Height: height, BlockHash: checkpoint.LastBlock.BlockHash(checkpoint.State.Digest())}
	for _, signer := range signers {
		vote, _ := consensus.SignVote(signer.signer, consensus.Vote{Height: height, Type: consensus.Precommit, BlockHash: checkpoint.Commit.BlockHash, Validator: signer.name})
		checkpoint.Commit.Votes = append(checkpoint.Commit.Votes, vote)
	}
	return checkpoint
}

// checkpointNode is a node that serves the checkpoint in served, and its status, for testNetworkID.
func checkpointNode(t *testing.T, served *atomic.Pointer[node.Checkpoint]) string {
	t.Helper()
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/hdls/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"heightInProgress": served.Load().State.Height + 1, "networkId": testNetworkID})
	})
	serveMux.HandleFunc("/hdls/checkpoint", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(*served.Load())
	})
	server := httptest.NewServer(serveMux)
	t.Cleanup(server.Close)
	return server.URL
}

// pinnedKeys is each name's key in the wallet's pin file.
func pinnedKeys(t *testing.T, wallet *Wallet) map[string]string {
	t.Helper()
	var pin walletPin
	if data, err := os.ReadFile(wallet.pinPath); err != nil || json.Unmarshal(data, &pin) != nil {
		t.Fatalf("cannot read the pin: %v", err)
	}
	keys := map[string]string{}
	for _, validator := range pin.Validators {
		keys[validator.Name] = validator.PublicKey
	}
	if pin.NetworkID != testNetworkID {
		t.Errorf("the pin's network id is %q, want %q: it never moves", pin.NetworkID, testNetworkID)
	}
	return keys
}

// The pin follows the chain (Q17): when a state that pinned names holding more than ⅔ of the pinned
// power signed carries another validator set, and its commit is a quorum of that set too, the wallet
// pins that set, in memory and in <keyfile>.trust. So it follows the banks' re-keys one after another,
// although the second leaves only two of the four first pinned keys, and a restarted wallet takes the
// moved pin.
func TestPinFollowsTheChainThroughReKeys(t *testing.T) {
	bankA, bankB, bankC, bankD := seat{"bank-a", testNodeSigner(t)}, seat{"bank-b", testNodeSigner(t)}, seat{"bank-c", testNodeSigner(t)}, seat{"bank-d", testNodeSigner(t)}
	var served atomic.Pointer[node.Checkpoint]
	serve := func(height int64, seats ...seat) {
		checkpoint := seatedCheckpoint(height, seats, seats)
		served.Store(&checkpoint)
	}
	serve(9, bankA, bankB, bankC, bankD)
	wallet := newTestWallet(t, checkpointNode(t, &served))
	verified(t, wallet) // the first use pins the four banks

	rekeyedD := seat{"bank-d", testNodeSigner(t)}
	serve(10, bankA, bankB, bankC, rekeyedD) // the ⅔ re-keyed bank-d: the next block's commit carries its new key
	verified(t, wallet)
	if keys := pinnedKeys(t, wallet); keys["bank-d"] != rekeyedD.signer.PublicKeyBase64() || keys["bank-a"] != bankA.signer.PublicKeyBase64() || len(keys) != 4 {
		t.Fatal("the pin did not move to the set the verified state carries, bank-d's new key in it")
	}
	if wallet.pinnedKey("bank-d") != rekeyedD.signer.PublicKeyBase64() {
		t.Error("the pin in memory did not move: bank-d's receipts would not verify")
	}

	rekeyedC := seat{"bank-c", testNodeSigner(t)}
	serve(11, bankA, bankB, rekeyedC, rekeyedD) // of the keys first pinned, only bank-a's and bank-b's sign now
	verified(t, wallet)
	if keys := pinnedKeys(t, wallet); keys["bank-c"] != rekeyedC.signer.PublicKeyBase64() || keys["bank-d"] != rekeyedD.signer.PublicKeyBase64() {
		t.Error("the pin did not follow the second re-key")
	}
	restarted, err := LoadOrCreateWallet(strings.TrimSuffix(wallet.pinPath, ".trust"), "testnet", wallet.nodeURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	verified(t, restarted)
}

// A list an operator put in <keyfile>.trust while the wallet ran is kept when the pin moves (Q17): the
// wallet follows the chain in memory, and takes that list once restarted, as it takes any list.
func TestMovingPinKeepsAListPutInPlaceWhileTheWalletRan(t *testing.T) {
	bankA, bankB, bankC, bankD := seat{"bank-a", testNodeSigner(t)}, seat{"bank-b", testNodeSigner(t)}, seat{"bank-c", testNodeSigner(t)}, seat{"bank-d", testNodeSigner(t)}
	var served atomic.Pointer[node.Checkpoint]
	serve := func(height int64, seats ...seat) {
		checkpoint := seatedCheckpoint(height, seats, seats)
		served.Store(&checkpoint)
	}
	serve(9, bankA, bankB, bankC, bankD)
	wallet := newTestWallet(t, checkpointNode(t, &served))
	verified(t, wallet)
	list := fmt.Sprintf(`{"networkId": %q, "validators": [{"Name": "bank-a", "PublicKey": %q, "Power": 1}]}`, testNetworkID, bankA.signer.PublicKeyBase64())
	if err := os.WriteFile(wallet.pinPath, []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	rekeyedD := seat{"bank-d", testNodeSigner(t)}
	serve(10, bankA, bankB, bankC, rekeyedD)
	verified(t, wallet)
	if wallet.pinnedKey("bank-d") != rekeyedD.signer.PublicKeyBase64() {
		t.Error("the pin in memory did not follow bank-d's re-key")
	}
	if data, _ := os.ReadFile(wallet.pinPath); string(data) != list {
		t.Errorf("the operator's list was replaced: %.80s…", data)
	}
}

// The pin moves only on a state that pinned names holding more than ⅔ of the pinned power signed, under
// their pinned keys, and only to a set that state's commit is a quorum of (Q17). Two banks of four that
// re-key at once leave half the pinned power: nothing verifies and nothing moves. A commit of the four
// pinned keys whose state carries a key the commit was not signed under verifies, but moves nothing.
func TestPinDoesNotMoveOnACheckpointThePinDoesNotVouchFor(t *testing.T) {
	bankA, bankB, bankC, bankD := seat{"bank-a", testNodeSigner(t)}, seat{"bank-b", testNodeSigner(t)}, seat{"bank-c", testNodeSigner(t)}, seat{"bank-d", testNodeSigner(t)}
	pinned := []seat{bankA, bankB, bankC, bankD}
	first := seatedCheckpoint(9, pinned, pinned)
	var served atomic.Pointer[node.Checkpoint]
	served.Store(&first)
	wallet := newTestWallet(t, checkpointNode(t, &served))
	verified(t, wallet)
	before, err := os.ReadFile(wallet.pinPath)
	if err != nil {
		t.Fatal(err)
	}
	pinUnmoved := func(when string) {
		t.Helper()
		if after, _ := os.ReadFile(wallet.pinPath); string(after) != string(before) {
			t.Errorf("%s: the pin file changed", when)
		}
		if wallet.pinnedKey("bank-c") != bankC.signer.PublicKeyBase64() || wallet.pinnedKey("bank-d") != bankD.signer.PublicKeyBase64() {
			t.Errorf("%s: the pin in memory moved", when)
		}
	}

	rekeyed := []seat{bankA, bankB, {"bank-c", testNodeSigner(t)}, {"bank-d", testNodeSigner(t)}}
	halfPinned := seatedCheckpoint(10, rekeyed, rekeyed)
	served.Store(&halfPinned)
	if _, err := wallet.verifiedState(); !errors.Is(err, errBanksChanged) {
		t.Errorf("a state that two of four pinned keys signed: %v; want errBanksChanged", err)
	}
	pinUnmoved("two of four pinned keys signed")

	carriesAnotherKey := seatedCheckpoint(11, []seat{bankA, bankB, bankC, {"bank-d", testNodeSigner(t)}}, pinned)
	served.Store(&carriesAnotherKey)
	if !carriesAnotherKey.Consistent() {
		t.Fatal("premise: the checkpoint is consistent")
	}
	verified(t, wallet)
	pinUnmoved("the commit is not a quorum of the set the state carries")
}

// When no state verifies because the pinned names among its signers hold no more than ⅔ of the pinned
// power, while its commit is a quorum of the set it carries, the pages say in plain words that the
// banks that sign have changed, and how to take a new list: the bank's list in place of the pin file,
// or no pin file and a first use at another bank (Q17). The freeze button still works. A state that no
// set signed keeps the plain refusal.
func TestBanksChangedSaysHowToTakeANewList(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	alice := ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)}
	testNode.setState(nil, alice)
	verified(t, wallet)
	testNode.setState([]signing.Signer{testNodeSigner(t), testNodeSigner(t)}, alice) // other banks now sign
	for _, path := range []string{"/", "/requests", "/admin"} {
		body := page(t, wallet, path)
		for _, want := range []string{"the banks that sign have changed since this wallet last checked", "Get the current list of banks from any bank",
			"save it as " + wallet.pinPath + ", and restart the wallet", "switch to another bank: delete " + wallet.pinPath,
			"Take a new list, or pin again, only at a bank, never through a node you do not trust: a node that is not your bank's can show this message to get its own list trusted."} {
			if !strings.Contains(body, want) {
				t.Errorf("%s does not say %q", path, want)
			}
		}
	}
	if body := page(t, wallet, "/"); !strings.Contains(body, "No verified state") || !strings.Contains(body, "Freeze alice") {
		t.Error("the main page does not say that no state verifies, or no longer offers to freeze alice")
	}

	unsigned := signedCheckpoint([]signing.Signer{testNodeSigner(t)}, []ledger.Account{alice})
	unsigned.Commit.Votes = nil // a quorum of no set
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(unsigned)
	}))
	defer server.Close()
	wallet.nodeURL = server.URL
	if _, err := wallet.verifiedState(); err == nil || errors.Is(err, errBanksChanged) {
		t.Errorf("a state no set signed: %v; want a refusal that does not say the banks changed", err)
	}
}

// A checkpoint from a file (spec §7.8), loaded when the node can't be reached, is verified under the pin
// exactly as the node's checkpoint is. A wallet with no pin takes none: a file names no network to pin
// on first use. A forged file, whether a genuine one with a balance changed or one signed by a set made
// up to sign for itself, another network's (the same banks' keys, signing for another network of the
// same name), one where two of four banks re-keyed at once, and one older than the newest state the
// wallet verified are refused, each with its reason, which speaks of the file, and move nothing. One where the ⅔ re-keyed one bank
// moves the pin (Q17). The pages then show its state "as of height H, from a file", and a payment saved
// as text names its block from that height. While the node serves an older state the file stands, and
// once the node's state verifies again the wallet forgets the file.
func TestCheckpointFromAFileIsVerifiedUnderThePin(t *testing.T) {
	scheme := signing.NetworkScheme()
	otherNetworkID := ledger.NetworkID("testnet", [32]byte{2})
	var banks, otherNetworkBanks []seat
	for _, name := range []string{"bank-a", "bank-b", "bank-c", "bank-d"} {
		_, privateKey, err := scheme.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		testNetworkSigner, _ := signing.NewSigner(scheme, testNetworkID, privateKey)
		otherNetworkSigner, _ := signing.NewSigner(scheme, otherNetworkID, privateKey)
		banks, otherNetworkBanks = append(banks, seat{name, testNetworkSigner}), append(otherNetworkBanks, seat{name, otherNetworkSigner})
	}
	var served atomic.Pointer[node.Checkpoint]
	bankNode := checkpointNode(t, &served)
	unreachable := httptest.NewServer(nil)
	unreachable.Close()
	wallet := newTestWallet(t, unreachable.URL)
	customers := []ledger.Account{{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)}, {Name: "shop", PublicKey: testNodeSigner(t).PublicKeyBase64(), Amount: new(big.Rat)}}
	file := func(checkpoint node.Checkpoint) string {
		text, _ := json.Marshal(checkpoint)
		return string(text)
	}
	refused := func(checkpoint node.Checkpoint, reason string) {
		t.Helper()
		refusal := redirectError(submitForm(wallet, "/checkpoint", url.Values{"checkpoint": {file(checkpoint)}}))
		if !strings.HasPrefix(refusal, "the checkpoint file is refused: ") || !strings.Contains(refusal, reason) {
			t.Errorf("a file at height %d: %q; want it refused because %q", checkpoint.State.Height, refusal, reason)
		}
	}

	pinnedAtNine := seatedCheckpoint(9, banks, banks, customers...)
	refused(pinnedAtNine, "this wallet trusts no banks yet")
	served.Store(&pinnedAtNine)
	wallet.nodeURL = bankNode
	verified(t, wallet) // the first use pins the four banks
	wallet.nodeURL = unreachable.URL
	pinBefore, _ := os.ReadFile(wallet.pinPath)

	forgedBalance := seatedCheckpoint(10, banks, banks, customers...)
	forgedBalance.State.Accounts[0].Amount = big.NewRat(5000, 1) // alice's
	refused(forgedBalance, "the file's state is not in name order, or does not match its commit")
	impostors := []seat{{"bank-a", testNodeSigner(t)}, {"bank-b", testNodeSigner(t)}, {"bank-c", testNodeSigner(t)}, {"bank-d", testNodeSigner(t)}}
	banksChangedInTheFile := errBanksChanged.Error() + ", and too few of the banks this wallet trusts signed this file's state"
	refused(seatedCheckpoint(10, impostors, impostors, customers...), banksChangedInTheFile)
	refused(seatedCheckpoint(10, impostors, impostors, customers...), "never through a file you do not trust: a file that is not your bank's can show this message")
	refused(seatedCheckpoint(10, otherNetworkBanks, otherNetworkBanks, customers...), "not signed by the validators pinned in "+wallet.pinPath+", nor by the validators it lists: this file does not serve your network's ledger")
	twoRekeyed := []seat{banks[0], banks[1], {"bank-c", testNodeSigner(t)}, {"bank-d", testNodeSigner(t)}}
	refused(seatedCheckpoint(10, twoRekeyed, twoRekeyed, customers...), banksChangedInTheFile)
	if pinAfter, _ := os.ReadFile(wallet.pinPath); string(pinAfter) != string(pinBefore) || wallet.pinnedKey("bank-d") != banks[3].signer.PublicKeyBase64() {
		t.Fatal("a refused file moved the pin")
	}
	if _, err := wallet.verifiedState(); err == nil {
		t.Fatal("with the node unreachable and every file refused, a state verified")
	}

	rekeyedD := seat{"bank-d", testNodeSigner(t)}
	oneRekeyed := []seat{banks[0], banks[1], banks[2], rekeyedD}
	if _, err := wallet.LoadCarriedCheckpoint(file(seatedCheckpoint(10, oneRekeyed, oneRekeyed, customers...))); err != nil {
		t.Fatalf("a file whose commit the pinned ⅔ signed was refused: %v", err)
	}
	if keys := pinnedKeys(t, wallet); keys["bank-d"] != rekeyedD.signer.PublicKeyBase64() || wallet.pinnedKey("bank-d") != rekeyedD.signer.PublicKeyBase64() {
		t.Error("the pin did not follow bank-d's re-key in the file")
	}
	if body := page(t, wallet, "/"); !strings.Contains(body, "your balance (as of height 10, from a file)") || !strings.Contains(body, `<div class="balance">5</div>`) {
		t.Error("the main page does not show alice's balance as of height 10, from a file")
	}
	saved, err := wallet.SavePayment(verified(t, wallet), "shop", "1")
	if err != nil || saved.BlockID != 10+1+wallet.lookahead {
		t.Errorf("a payment saved from the file names block %d (err %v), want %d", saved.BlockID, err, 10+1+wallet.lookahead)
	}
	refused(pinnedAtNine, "older than the state of block 10")

	wallet.nodeURL = bankNode // the node answers, but with the older state of block 9
	if checkpoint := verified(t, wallet); checkpoint.State.Height != 10 {
		t.Errorf("with the node behind the file, the wallet shows block %d, want the file's 10", checkpoint.State.Height)
	}
	atEleven := seatedCheckpoint(11, oneRekeyed, oneRekeyed, customers...)
	served.Store(&atEleven)
	if checkpoint, keptFrom, err := wallet.newestVerifiedState(); err != nil || checkpoint.State.Height != 11 || keptFrom != "" {
		t.Error("the node's state of block 11 verifies, but the wallet does not show it")
	}
	wallet.nodeURL = unreachable.URL
	if _, err := wallet.verifiedState(); err == nil {
		t.Error("the wallet kept the file after the node's state verified again")
	}
}

// A checkpoint from a file (spec §7.8) stands in for the node's state only when the node's checkpoint
// can't be read (no answer, or an HTTP error status), or when the node's state verifies but is older
// than the file. Any other refusal is shown as it is, file or no file, and no payment or update goes to
// that node: here a node whose newer state a set of its own making signed (the banks changed), one that
// no set signed, and one that does not match its commit. The freeze still goes out, as whenever no state
// verifies, for the block the node's report gives. While the file stands in, a freeze names its block
// from the file's height in progress, or from the node's report when that is later.
func TestCarriedCheckpointDoesNotHideTheNodesRefusal(t *testing.T) {
	testNode := newGovernanceNode(t) // one validator, height 9
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	alice := ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)}
	testNode.setState(nil, alice, accountOf(shop, "0"))
	verified(t, wallet) // pins the one validator, at block 9
	validators := testNode.validators

	nodeURL := wallet.nodeURL
	unreachable := httptest.NewServer(nil)
	unreachable.Close()
	wallet.nodeURL = unreachable.URL
	file, _ := json.Marshal(signedCheckpointAt(10, testNode.validators, testNode.accounts))
	if _, err := wallet.LoadCarriedCheckpoint(string(file)); err != nil {
		t.Fatal(err)
	}
	fileStandsIn := func(when string) {
		t.Helper()
		if body := page(t, wallet, "/"); !strings.Contains(body, "your balance (as of height 10, from a file)") {
			t.Errorf("%s, the page does not show the file", when)
		}
	}
	freezesFor := func(when string, want int64) {
		t.Helper()
		if freeze, err := wallet.Freeze(); err != nil || freeze.BlockID != want {
			t.Errorf("%s, the freeze names block %d (err %v), want %d", when, freeze.BlockID, err, want)
		}
	}
	fileStandsIn("with the node unreachable")
	wallet.nodeURL = nodeURL
	testNode.change(func() { testNode.checkpointFails, testNode.reportedHeight = http.StatusServiceUnavailable, 5 })
	fileStandsIn("with the node answering its checkpoint with 503")
	freezesFor("with the file standing in and the node reporting block 5 in progress", 10+1+wallet.lookahead)
	testNode.change(func() { testNode.reportedHeight = 30 })
	freezesFor("with the file standing in and the node reporting block 30 in progress", 30+wallet.lookahead)
	testNode.change(func() { testNode.checkpointFails, testNode.reportedHeight = 0, 0 }) // it serves its state of block 9
	fileStandsIn("with the node's state older than the file")

	impostor := testNodeSigner(t)
	for _, refusal := range []struct {
		reason     string
		validators []signing.Signer // the set that signs the node's state of block 40
		forge      func(checkpoint *node.Checkpoint)
	}{
		{errBanksChanged.Error(), []signing.Signer{impostor}, nil},
		{"not signed by the validators pinned in " + wallet.pinPath + ", nor by the validators it lists", validators,
			func(checkpoint *node.Checkpoint) { checkpoint.Commit.Votes = nil }},
		{"the node's state is not in name order, or does not match its commit", validators,
			func(checkpoint *node.Checkpoint) { checkpoint.State.Accounts[0].Amount = big.NewRat(5000, 1) }}, // alice's
	} {
		testNode.change(func() { testNode.height, testNode.forge = 40, refusal.forge })
		testNode.setState(refusal.validators, alice, accountOf(shop, "0"))
		postsBefore := len(testNode.posted())
		if body := page(t, wallet, "/"); !strings.Contains(body, refusal.reason) || strings.Contains(body, "as of height 10, from a file") {
			t.Errorf("with a file loaded, the page does not show the node's refusal %q, or shows the file", refusal.reason)
		}
		if _, _, err := wallet.Send(nameOf(shop), "1"); err == nil || !strings.Contains(err.Error(), refusal.reason) {
			t.Errorf("a payment, with the node refused because %q: %v", refusal.reason, err)
		}
		if _, err := wallet.UpdateAccount("hello", "", "", "", ""); err == nil || !strings.Contains(err.Error(), refusal.reason) {
			t.Errorf("an account update, with the node refused because %q: %v", refusal.reason, err)
		}
		if posts := testNode.posted(); len(posts) != postsBefore {
			t.Errorf("with the node refused because %q, the wallet sent it %s", refusal.reason, posts[postsBefore].path)
		}
		freezesFor("with the node refused because "+refusal.reason, 41+wallet.lookahead)
	}
}

// While a checkpoint from a file stands in because the node's checkpoint can't be read, the page says
// to save payments as text. So "Sign and send" and an account update are refused before they sign or
// take a block of the payment counter, and nothing is posted to the node, though it would take a post:
// here it answers its checkpoint with 503 and every post with 202. Saving the payment as text, and the
// freeze, still work.
func TestUnreadableNodeGetsNoPaymentOrUpdate(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)}, accountOf(shop, "0"))
	verified(t, wallet) // pins the one validator, at block 9
	file, _ := json.Marshal(signedCheckpointAt(10, testNode.validators, testNode.accounts))
	if _, err := wallet.LoadCarriedCheckpoint(string(file)); err != nil {
		t.Fatal(err)
	}
	testNode.change(func() { testNode.checkpointFails = http.StatusServiceUnavailable })
	if _, keptFrom, err := wallet.newestVerifiedState(); err != nil || keptFrom != "file" {
		t.Fatalf("premise: with the node answering its checkpoint with 503, the state is kept from %q (err %v), want the file", keptFrom, err)
	}
	lastPaymentBlockID := func() int64 {
		wallet.mutex.Lock()
		defer wallet.mutex.Unlock()
		return wallet.saved.LastPaymentBlockID
	}
	counterBefore := lastPaymentBlockID()

	if _, _, err := wallet.Send(nameOf(shop), "1"); err == nil || !strings.Contains(err.Error(), "your bank's node can't be read") ||
		!strings.Contains(err.Error(), "save the payment as text") {
		t.Errorf("a payment to the node whose checkpoint can't be read: %v; want it refused, saying to save it as text", err)
	}
	if _, err := wallet.UpdateAccount("hello", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "your bank's node can't be read") {
		t.Errorf("an account update to the node whose checkpoint can't be read: %v; want it refused", err)
	}
	if posts := testNode.posted(); len(posts) != 0 {
		t.Errorf("the wallet posted %s to the node whose checkpoint can't be read", posts[0].path)
	}
	if counter := lastPaymentBlockID(); counter != counterBefore {
		t.Errorf("the refused payment and update moved the counter from %d to %d", counterBefore, counter)
	}

	if saved, err := wallet.SavePayment(verified(t, wallet), nameOf(shop), "1"); err != nil || saved.BlockID != 10+1+wallet.lookahead {
		t.Errorf("a payment saved as text names block %d (err %v), want %d, from the file", saved.BlockID, err, 10+1+wallet.lookahead)
	}
	if _, err := wallet.Freeze(); err != nil {
		t.Errorf("the freeze, with the node's checkpoint unreadable: %v", err)
	}
	if posts := testNode.posted(); len(posts) != 1 || posts[0].path != "/hdls/name-request" {
		t.Errorf("the node got %d posts, want the freeze alone", len(posts))
	}
}

// A CDN's cache servers may hold copies of a bank's checkpoint of different ages (spec §6.3). When the
// node serves a state its validators signed, but older than the newest the wallet verified, the wallet
// shows that newest state, which it kept, marked as read before, instead of refusing with the rollback
// alarm. It never shows the older state as current, and acts on the kept one: a payment names its block
// from it. Once the node's state is as new again, the page shows the node's.
func TestOlderCopyOfTheCheckpointShowsTheNewestStateRead(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	shop := testNodeSigner(t)
	testNode.change(func() { testNode.height = 14 }) // the copy one cache server took a moment ago
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)}, accountOf(shop, "0"))
	verified(t, wallet)
	testNode.change(func() { testNode.height = 10 }) // the copy another cache server took 4 seconds earlier
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(40, 1)}, accountOf(shop, "0"))

	if checkpoint, err := wallet.verifiedState(); err != nil || checkpoint.State.Height != 14 {
		t.Fatalf("with the node serving block 10 after block 14: the state of block %d (err %v), want the kept state of block 14", checkpoint.State.Height, err)
	}
	body := page(t, wallet, "/")
	if !strings.Contains(body, "your balance (as of height 14, read before)") || !strings.Contains(body, `<div class="balance">50</div>`) ||
		!strings.Contains(body, "your bank's node now serves an older state") || strings.Contains(body, "may be hiding a newer change") {
		t.Error("the main page does not show the kept state of block 14, marked as read before")
	}
	if _, _, err := wallet.Send(nameOf(shop), "1"); err != nil {
		t.Fatal(err)
	}
	var payment ledger.Payment
	if posts := testNode.posted(); len(posts) != 1 || json.Unmarshal(posts[0].body, &payment) != nil || payment.BlockID != 14+1+wallet.lookahead {
		t.Errorf("the payment names block %d, want %d: from the kept state", payment.BlockID, 14+1+wallet.lookahead)
	}

	testNode.change(func() { testNode.height = 15 })
	if body := page(t, wallet, "/"); !strings.Contains(body, "your balance (block 15)") || strings.Contains(body, "read before") {
		t.Error("once the node serves block 15, the page does not show the node's state")
	}
}

// A file the wallet stored stands in only while it is the newest state the wallet verified. When a newer
// state verified meanwhile (a page read the node's state of block 12 while the file loaded), the file is
// never shown, even if it was stored.
func TestStoredFileBelowTheNewestStateIsNeverShown(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)})
	verified(t, wallet)
	unreachable := httptest.NewServer(nil)
	unreachable.Close()
	wallet.nodeURL = unreachable.URL
	file, _ := json.Marshal(signedCheckpointAt(10, testNode.validators, testNode.accounts))
	if _, err := wallet.LoadCarriedCheckpoint(string(file)); err != nil {
		t.Fatal(err)
	}
	wallet.mutex.Lock()
	wallet.saved.NewestHeight = 12 // what the race leaves: the state of block 12 verified, and the file of block 10 stored
	wallet.mutex.Unlock()
	if checkpoint, err := wallet.verifiedState(); err == nil {
		t.Errorf("the wallet shows the file of block %d below the state of block 12 it verified", checkpoint.State.Height)
	}
}

// The checkpoint form takes a file as large as a node takes (node.MaxCheckpointBytes), though Go's form
// parser alone stops at 10 MB: here a network of 3,500 accounts, whose file is over 10 MB url-encoded.
// A form the parser can't read is refused as unreadable, not as a file cut short.
func TestLargeCheckpointFileIsTakenByTheForm(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	accounts := []ledger.Account{{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)}}
	key := testNodeSigner(t).PublicKeyBase64() // one key for all: the checks the wallet makes do not compare keys
	for i := range 3500 {
		accounts = append(accounts, ledger.Account{Name: fmt.Sprintf("c%05d", i), PublicKey: key, Amount: new(big.Rat)})
	}
	testNode.setState(nil, accounts...)
	verified(t, wallet)
	unreachable := httptest.NewServer(nil)
	unreachable.Close()
	wallet.nodeURL = unreachable.URL
	file, _ := json.Marshal(signedCheckpointAt(10, testNode.validators, testNode.accounts))
	if encoded := (url.Values{"checkpoint": {string(file)}}).Encode(); len(encoded) <= 10<<20 {
		t.Fatalf("premise: the form is %d bytes, not over Go's 10 MB", len(encoded))
	}
	if refusal := redirectError(submitForm(wallet, "/checkpoint", url.Values{"checkpoint": {string(file)}})); refusal != "" {
		t.Errorf("a genuine checkpoint of 3,500 accounts is refused by the form: %q", refusal)
	}
	if checkpoint := verified(t, wallet); checkpoint.State.Height != 10 {
		t.Errorf("the wallet shows the state of block %d, want the file's 10", checkpoint.State.Height)
	}

	request := httptest.NewRequest("POST", "http://127.0.0.1:8090/checkpoint", strings.NewReader("checkpoint=%zz"))
	request.Header.Set("Origin", "http://127.0.0.1:8090")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	routes(wallet, "127.0.0.1:8090", "").ServeHTTP(recorder, request)
	if refusal := redirectError(recorder); !strings.Contains(refusal, "unreadable") || strings.Contains(refusal, "paste the whole file") {
		t.Errorf("a form the parser can't read: %q; want it refused as unreadable", refusal)
	}
}

// A pasted checkpoint longer than a node takes (node.MaxCheckpointBytes) is refused, with the reason the
// form gives when it is itself too large, though the form's bound lets through a text up to three times
// as long. Here a genuine checkpoint, padded with spaces to one byte past the bound, which a decoder
// alone would take.
func TestCheckpointTextPastTheBoundIsRefused(t *testing.T) {
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	testNode.setState(nil, ledger.Account{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(50, 1)})
	verified(t, wallet) // pins the one validator, at block 9
	file, _ := json.Marshal(signedCheckpointAt(10, testNode.validators, testNode.accounts))
	padded := string(file) + strings.Repeat(" ", node.MaxCheckpointBytes+1-len(file))
	if _, err := wallet.LoadCarriedCheckpoint(padded); err == nil || err.Error() != "it is too large: a checkpoint file has at most 64 MiB" {
		t.Errorf("a checkpoint text of %d bytes, one past the bound: %v; want it refused as too large", len(padded), err)
	}
	if checkpoint := verified(t, wallet); checkpoint.State.Height != 9 {
		t.Errorf("the wallet shows the state of block %d, want the node's 9, not the refused file's", checkpoint.State.Height)
	}
}
