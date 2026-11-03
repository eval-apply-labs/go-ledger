package node

import (
	"bytes"
	"fmt"
	"log"
	"math/big"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// testNetworkID is the network id the node tests sign for.
const testNetworkID = "testnet"

// testSigners holds the signer of each test name, made on first use and shared by every test: a key
// costs about a millisecond to generate.
var (
	testSignersMutex sync.Mutex
	testSigners      = map[string]signing.Signer{}
)

// testSigner returns the signer that holds the key of the test name, the same one each time.
func testSigner(name string) signing.Signer {
	testSignersMutex.Lock()
	defer testSignersMutex.Unlock()
	if signer, found := testSigners[name]; found {
		return signer
	}
	signer, err := signing.Generate(signing.NetworkScheme(), testNetworkID)
	if err != nil {
		panic(err)
	}
	testSigners[name] = signer
	return signer
}

// testAccount is the account of the test name, with the key testSigner(name) holds and the balance.
func testAccount(name, amount string) ledger.Account {
	return ledger.Account{Name: name, PublicKey: testSigner(name).PublicKeyBase64(), Amount: rational(amount)}
}

// validatorNames is "validator-0" to "validator-<count-1>".
func validatorNames(count int) []string {
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("validator-%d", i)
	}
	return names
}

// staticChain is a test chain: the network's scheme on testNetworkID, the names given its static validators, each
// of power 1.
func staticChain(names ...string) ledger.Chain {
	var validatorRule ledger.ValidatorRule
	for _, name := range names {
		validatorRule.StaticValidators = append(validatorRule.StaticValidators, ledger.StaticValidator{Name: name, Power: 1})
	}
	return ledger.Chain{Scheme: signing.NetworkScheme(), NetworkID: testNetworkID, ValidatorRule: validatorRule}
}

// dynamicChain is a test chain whose validators follow stake, seated from the threshold given.
func dynamicChain(threshold string) ledger.Chain {
	return ledger.Chain{Scheme: signing.NetworkScheme(), NetworkID: testNetworkID, ValidatorRule: ledger.ValidatorRule{Threshold: rational(threshold)}}
}

// testConfig is the config of the node of the test name on chain, from genesis.
func testConfig(chain ledger.Chain, name string, genesis ledger.LedgerState) Config {
	return Config{Chain: chain, Signer: testSigner(name), Name: name, Genesis: genesis}
}

// New is Open for callers that have no snapshot to fail on (tests): it panics where Open returns an
// error.
func New(config Config, transport Transport) *Node {
	node, err := Open(config, transport)
	if err != nil {
		panic(err)
	}
	return node
}

// SubmitPayment is AdmitPayment without waiting for the loop's verdict. The loop puts its verdict in
// the channel, which holds it, and nobody reads it.
func (node *Node) SubmitPayment(payment ledger.Payment) {
	if verifiedKey, err := node.checkPayment(payment); err == nil {
		node.enqueue(event{kind: eventPayment, payment: &payment, verifiedKey: verifiedKey, admit: make(chan admission, 1)})
	}
}

// SubmitUpdate is AdmitUpdate without waiting for the loop's verdict, as SubmitPayment is.
func (node *Node) SubmitUpdate(update ledger.AccountUpdate) {
	if verifiedKey, err := node.checkUpdate(update); err == nil {
		node.enqueue(event{kind: eventUpdate, update: &update, verifiedKey: verifiedKey, admit: make(chan admission, 1)})
	}
}

// signedPayment is a payment of amount from the test name `from` to the name `to`, for the block
// blockID, signed under from's key.
func signedPayment(from, to, amount string, blockID int64) ledger.Payment {
	payment, err := ledger.SignPayment(testSigner(from), ledger.Payment{From: from, To: to, Amount: rational(amount), BlockID: blockID})
	if err != nil {
		panic(err)
	}
	return payment
}

// proposalOf is block proposed afresh (valid round -1) for (height, round), signed by signer under the
// name proposerName.
func proposalOf(signer signing.Signer, proposerName string, height int64, round int32, block ledger.Block) ProposalMessage {
	signed, err := consensus.SignProposal(signer, consensus.SignedProposal{Height: height, Round: round, ValidRound: -1, BlockHash: ledger.BlockHash(block), Proposer: proposerName})
	if err != nil {
		panic(err)
	}
	return ProposalMessage{Proposal: signed, Block: block}
}

// testGenesis is a test network's genesis state at height 0: the accounts, put in name order as the
// ledger keeps them, at the default parameters, with the step timeout and the block interval given.
func testGenesis(accounts []ledger.Account, stepTimeout, blockInterval time.Duration) ledger.LedgerState {
	parameters := ledger.DefaultParameters()
	parameters.StepTimeoutMs = stepTimeout.Milliseconds()
	parameters.BlockIntervalMs = blockInterval.Milliseconds()
	sorted := slices.SortedFunc(slices.Values(accounts), func(first, second ledger.Account) int { return strings.Compare(first.Name, second.Name) })
	return ledger.LedgerState{Accounts: sorted, Parameters: parameters}
}

// lockedBuffer is a buffer that the log package and a test can use at once.
type lockedBuffer struct { // implements io.Writer
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (logged *lockedBuffer) Write(data []byte) (int, error) {
	logged.mutex.Lock()
	defer logged.mutex.Unlock()
	return logged.buffer.Write(data)
}

func (logged *lockedBuffer) String() string {
	logged.mutex.Lock()
	defer logged.mutex.Unlock()
	return logged.buffer.String()
}

// captureLog sends the log to a lockedBuffer until the test ends, and returns the buffer. Another
// goroutine may log at any moment, even one a test before this one left behind: a transport whose
// peer stopped answering, or a gossip port's report of the connections it refused. So the buffer is
// locked, and a test looks only for its own lines.
func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	logged, previousOutput := &lockedBuffer{}, log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	return logged
}

func rational(literal string) *big.Rat {
	value, parsed := new(big.Rat).SetString(literal)
	if !parsed {
		panic("bad rat: " + literal)
	}
	return value
}

// bus is an in-memory Transport that delivers each broadcast to every other registered node,
// asynchronously (a goroutine per delivery), mimicking a full-mesh network.
type bus struct { // implements Transport
	mutex sync.Mutex
	nodes map[string]*Node
}

func newBus() *bus { return &bus{nodes: map[string]*Node{}} }

func (messageBus *bus) register(name string, node *Node) {
	messageBus.mutex.Lock()
	messageBus.nodes[name] = node
	messageBus.mutex.Unlock()
}

func (messageBus *bus) Broadcast(from string, message Message) {
	messageBus.mutex.Lock()
	targets := make(map[string]*Node, len(messageBus.nodes))
	for name, node := range messageBus.nodes {
		targets[name] = node
	}
	messageBus.mutex.Unlock()
	for name, target := range targets {
		if name == from {
			continue
		}
		node := target
		go func() {
			switch message.Type {
			case MessageProposal:
				node.SubmitProposal(*message.Proposal)
			case MessageVote:
				node.SubmitVote(*message.Vote)
			case MessagePayment:
				node.SubmitPayment(*message.Payment)
			case MessageNameRequest:
				node.AdmitNameRequest(*message.NameRequest)
			}
		}()
	}
}

// balance is the balance of the account named `name`, or zero when there is none.
func balance(accounts []ledger.Account, name string) *big.Rat {
	return ledger.AccountAmount(accounts, name)
}

// Four validator nodes, wired full-mesh in-memory, must all decide the same block and apply the
// submitted payment: from alice to bob, both by name.
func TestNodesReachConsensusAndApplyPayment(t *testing.T) {
	names := validatorNames(4)
	chain := staticChain(names...)
	// Genesis: the validators, alice (an account we control) funded, bob empty.
	accounts := []ledger.Account{testAccount("alice", "100"), testAccount("bob", "0")}
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "1"))
	}
	genesis := testGenesis(accounts, 5*time.Second, 0)

	bus := newBus()
	nodes := make([]*Node, len(names))
	for i, name := range names {
		nodes[i] = New(testConfig(chain, name, genesis), bus)
		bus.register(name, nodes[i])
	}

	// A signed payment alice -> bob 10, submitted to every node BEFORE start, so whichever node
	// proposes height 1 includes it.
	payment := signedPayment("alice", "bob", "10", 1)
	for _, node := range nodes {
		node.SubmitPayment(payment)
	}
	for _, node := range nodes {
		node.Start()
	}
	defer func() {
		for _, node := range nodes {
			node.Stop()
		}
	}()

	// Wait until every node has committed at least height 1 (i.e. reached height >= 2).
	deadline := time.Now().Add(4 * time.Second)
	var snapshots []Snapshot
	for {
		snapshots = snapshots[:0]
		allAdvanced := true
		for _, node := range nodes {
			snapshot := node.Snapshot()
			snapshots = append(snapshots, snapshot)
			if snapshot.Height < 2 {
				allAdvanced = false
			}
		}
		if allAdvanced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes did not reach height 2 in time; heights=%v", heights(snapshots))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// All nodes must agree on the committed state, and bob must have received 10 - fee.
	want := ledger.AccountsDigest(snapshots[0].Accounts)
	for i, snapshot := range snapshots {
		if ledger.AccountsDigest(snapshot.Accounts) != want {
			t.Fatalf("node %d diverged from node 0's state", i)
		}
	}
	if got := balance(snapshots[0].Accounts, "bob"); got.Cmp(rational("9.9")) != 0 {
		t.Fatalf("bob = %s, want 9.9 (payment not applied through consensus)", got.FloatString(2))
	}
}

func heights(snapshots []Snapshot) []int64 {
	result := make([]int64, len(snapshots))
	for i, snapshot := range snapshots {
		result[i] = snapshot.Height
	}
	return result
}

// The node takes its height from the state it holds and keeps no count of its own: opened on a state
// at height 7, it works on block 8, and commits a payment that names block 8. A node still at its
// genesis state starts at once, without the wait of a node back from a snapshot without its sign log.
func TestNodeHeightComesFromState(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100"), testAccount("payer", "100")},
		5*time.Second, 20*time.Millisecond) // a re-gossip interval longer than the wait below
	genesis.Height = 7
	node := New(testConfig(staticChain("validator"), "validator", genesis), NewHTTPTransport(nil))
	if node.height() != 8 {
		t.Fatalf("opened on a state at height 7, the node works on height %d, want 8", node.height())
	}
	node.SubmitPayment(signedPayment("payer", "validator", "10", 8))
	node.Start()
	defer node.Stop()
	waitFor(t, 3*time.Second, func() bool { return balance(node.Snapshot().Accounts, "payer").Cmp(rational("90")) == 0 },
		"the payment naming block 8 did not commit")
}

// Block 1 builds on the genesis hash (spec 2.3): a node opens with the chain's GenesisHash as its last
// block hash. So a sole validator's block 1 names it as the previous block hash, and an observer refuses
// a block 1 that names another one, here a zero hash, and takes the one that names it.
func TestBlockOneBuildsOnTheGenesisHash(t *testing.T) {
	chain := staticChain("validator")
	chain.GenesisHash = [32]byte{0xaa, 0xbb}
	genesis := testGenesis([]ledger.Account{testAccount("validator", "1000")}, time.Second, 0)
	recorder := &recordingTransport{}
	validator := New(testConfig(chain, "validator", genesis), recorder) // driven by hand: a sole validator decides and commits a height within startHeight
	if validator.startHeight(); validator.lastCommit.Height != 1 {
		t.Fatal("premise: the sole validator commits height 1 at once")
	}
	if proposal := recorder.proposalAt(1); proposal == nil || proposal.Block.PreviousBlockHash != chain.GenesisHash {
		t.Error("the validator's block 1 does not build on the genesis hash")
	}

	observer := New(testConfig(chain, "observer", genesis), &recordingTransport{})
	observer.startHeight()
	for _, testCase := range []struct { // in this order: the refused block is not held, so the round stays open
		previousBlockHash [32]byte
		taken             bool
	}{{[32]byte{}, false}, {chain.GenesisHash, true}} {
		block := ledger.ProposeBlock(chain, observer.state, testCase.previousBlockHash, [32]byte{}, ledger.BlockContent{})
		if taken := observer.onProposal(proposalOf(testSigner("validator"), "validator", 1, 0, block)); taken != testCase.taken {
			t.Errorf("a block 1 on the previous block hash %x: taken %v, want %v", testCase.previousBlockHash[:2], taken, testCase.taken)
		}
	}
}
