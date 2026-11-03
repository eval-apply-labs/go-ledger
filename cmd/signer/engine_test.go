package main

// The engine through the signer (spec §2.7, §6): engines of the node package, each signing with a
// signing.RemoteSigner that asks a SigningService of this package, over HTTP, as cmd/server's engine
// asks a running signer.

import (
	"encoding/base64"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// signerServer serves a signer's service on a test server, which a test can take down, as a signer that
// is not running, or make slow, as one whose answer comes after the engine's deadline of a second. It
// counts the requests that reach the service.
type signerServer struct {
	*httptest.Server
	down     atomic.Bool // every connection is dropped, unanswered
	slow     atomic.Bool // the request is signed and logged, and the answer comes two seconds later
	requests atomic.Int64
}

// serveSigner serves service on a new signerServer, closed when the test ends.
func serveSigner(t *testing.T, service *SigningService) *signerServer {
	t.Helper()
	server := &signerServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if server.down.Load() {
			panic(http.ErrAbortHandler) // the server drops the connection, and logs nothing
		}
		server.requests.Add(1)
		recorder := httptest.NewRecorder()
		service.ServeHTTP(recorder, request)
		if server.slow.Load() {
			time.Sleep(2 * time.Second)
		}
		maps.Copy(responseWriter.Header(), recorder.Header())
		responseWriter.WriteHeader(recorder.Code)
		responseWriter.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	return server
}

// remoteSigner is a new remote signer that asks server as the client engine, as an engine makes one at
// its start.
func (signer *testSigner) remoteSigner(server *signerServer) *signing.RemoteSigner {
	publicKey, _ := base64.StdEncoding.DecodeString(signer.publicKey)
	return signing.NewRemoteSigner(server.URL, "engine", signer.secrets["engine"], publicKey, signer.networkID, signing.NetworkScheme())
}

// conflicts is the number of the audit log's lines marked REFUSED CONFLICT.
func (signer *testSigner) conflicts(t *testing.T) int {
	t.Helper()
	return len(slices.DeleteFunc(signer.auditEntries(t), func(entry auditEntry) bool { return !strings.HasPrefix(entry.Refused, conflictMark) }))
}

// signedBank is a bank of a test network: its signer, the server it answers on, and its engine, which
// signs through it.
type signedBank struct {
	name    string
	signer  *testSigner
	server  *signerServer
	engine  *node.Node
	stopped bool // whether stopEngines has stopped its engine
}

// engineBus is the network between the engines, in memory: each proposal and vote an engine sends
// reaches every other engine, on a goroutine of its own, as the node tests' bus does.
type engineBus struct { // implements node.Transport
	engines map[string]*node.Node // filled before the engines start, and only read after
}

// The compiler checks that *engineBus implements node.Transport.
var _ node.Transport = (*engineBus)(nil)

// Broadcast hands message to every engine but the sender's, if it is a proposal or a vote.
func (bus *engineBus) Broadcast(from string, message node.Message) {
	for name, engine := range bus.engines {
		switch {
		case name == from:
		case message.Type == node.MessageProposal:
			go engine.SubmitProposal(*message.Proposal)
		case message.Type == node.MessageVote:
			go engine.SubmitVote(*message.Vote)
		}
	}
}

// startSignedNetwork starts a network of four banks, bank-a to bank-d, seated with power 1 each, whose
// engines each sign through a signer of their own. The engines stop when the test ends, if stopEngines
// has not stopped them.
func startSignedNetwork(t *testing.T) []*signedBank {
	t.Helper()
	publicKeys := map[string]string{}
	var banks []*signedBank
	for _, name := range []string{"bank-a", "bank-b", "bank-c", "bank-d"} {
		bank := &signedBank{name: name, signer: writeTestSigner(t)}
		publicKeys[name] = bank.signer.publicKey
		banks = append(banks, bank)
	}
	bus := &engineBus{engines: map[string]*node.Node{}}
	var chain ledger.Chain
	for _, bank := range banks {
		var genesisState ledger.LedgerState
		chain, genesisState = bank.signer.writeGenesis(t, "testnet", publicKeys)
		configFile := bank.signer.readConfig(t)
		configFile.Name = bank.name
		bank.signer.writeConfig(t, configFile)
		bank.signer.load(t, 0)
		bank.server = serveSigner(t, bank.signer.service)
		engine, err := node.Open(node.Config{Chain: chain, Signer: bank.signer.remoteSigner(bank.server), Name: bank.name, Genesis: genesisState}, bus)
		if err != nil {
			t.Fatal(err)
		}
		bank.engine, bus.engines[bank.name] = engine, engine
	}
	for _, bank := range banks {
		bank.engine.Start()
	}
	t.Cleanup(func() { stopEngines(banks) })

	// A payment queued for block 60 is work for every height up to it: an idle chain commits a block only
	// every five minutes (node.IdleHeartbeat). bank-a's key, which the test holds, signs it.
	payerKey, err := signing.NewSigner(chain.Scheme, chain.NetworkID, banks[0].signer.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	payment, err := ledger.SignPayment(payerKey, ledger.Payment{From: "bank-a", To: "bank-b", Amount: big.NewRat(1, 1), BlockID: 60})
	if err != nil {
		t.Fatal(err)
	}
	for _, bank := range banks {
		if _, err := bank.engine.AdmitPayment(payment); err != nil {
			t.Fatal(err)
		}
	}
	return banks
}

// stopEngines stops every engine not stopped yet, so its signer gets no more requests.
func stopEngines(banks []*signedBank) {
	for _, bank := range banks {
		if !bank.stopped {
			bank.engine.Stop()
			bank.stopped = true
		}
	}
}

// heightOf is the height bank's engine works on.
func heightOf(bank *signedBank) int64 { return bank.engine.Snapshot().Height }

// signedBy reports whether commit holds a precommit by name.
func signedBy(commit consensus.Commit, name string) bool {
	return slices.ContainsFunc(commit.Votes, func(vote consensus.Vote) bool { return vote.Validator == name })
}

// waitUntil waits until condition holds, for at most timeout, which GOLEDGER_CHAOS_SLACK stretches as
// it stretches the node tests' deadlines, and fails the test with message if it never does.
func waitUntil(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	if slack, err := strconv.Atoi(os.Getenv("GOLEDGER_CHAOS_SLACK")); err == nil && slack > 1 {
		timeout *= time.Duration(slack)
	}
	for deadline := time.Now().Add(timeout); !condition(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
	}
}

// Four banks whose engines each sign through a signer of their own commit block after block (spec §6),
// and every signer signs its bank's votes, and refuses none as a conflict.
func TestFourEnginesCommitThroughFourSigners(t *testing.T) {
	banks := startSignedNetwork(t)
	waitUntil(t, 20*time.Second, func() bool {
		return !slices.ContainsFunc(banks, func(bank *signedBank) bool { return heightOf(bank) < 4 })
	}, "the four engines did not commit three blocks through their signers")
	stopEngines(banks)
	for _, bank := range banks {
		votes := slices.DeleteFunc(bank.signer.auditEntries(t), func(entry auditEntry) bool {
			return entry.Domain != signing.DomainBlockRevision || entry.Refused != ""
		})
		if len(votes) == 0 || bank.signer.conflicts(t) != 0 {
			t.Errorf("%s's signer signed %d votes and refused %d as conflicts, want some and none", bank.name, len(votes), bank.signer.conflicts(t))
		}
	}
}

// A bank whose signer is down is a validator that does not vote (spec §2.7): the other three, more than
// ⅔, commit without it. Once its signer is back, the bank votes again, and no signer refused a
// conflict.
func TestABankWhoseSignerIsDownDoesNotVoteAndVotesAgainOnceItIsBack(t *testing.T) {
	banks := startSignedNetwork(t)
	witness, downBank := banks[0], banks[3]
	waitUntil(t, 20*time.Second, func() bool { return heightOf(witness) >= 3 }, "the network did not start")

	downBank.server.down.Store(true)
	downAt := heightOf(witness)
	waitUntil(t, 30*time.Second, func() bool { return heightOf(witness) >= downAt+3 }, "the other banks did not commit while one signer was down")
	if commit := witness.engine.Snapshot().LastCommit; signedBy(commit, downBank.name) {
		t.Fatalf("the commit of height %d holds a precommit by %s, whose signer has been down since height %d", commit.Height, downBank.name, downAt)
	}

	downBank.server.down.Store(false)
	backAt := heightOf(downBank)
	waitUntil(t, 30*time.Second, func() bool {
		commit := downBank.engine.Snapshot().LastCommit
		return commit.Height >= backAt && signedBy(commit, downBank.name)
	}, "the bank did not vote again once its signer was back")
	stopEngines(banks)
	for _, bank := range banks {
		if conflicts := bank.signer.conflicts(t); conflicts != 0 {
			t.Errorf("%s's signer refused %d requests as conflicts", bank.name, conflicts)
		}
	}
}

// testBank's prevote at height 5, round 0, for a block, and its nil prevote in the same slot.
var (
	blockPrevote = consensus.Vote{Height: 5, Round: 0, Type: consensus.Prevote, BlockHash: [32]byte{1}, Validator: testBank}
	nilPrevote   = consensus.Vote{Height: 5, Round: 0, Type: consensus.Prevote, Validator: testBank}
)

// An engine someone else drives, made to ask for two different prevotes in one slot, here as an engine
// restarted with a remote signer that remembers nothing, gets one signature and a refusal: the signer
// refuses the second as a conflict, and its audit log holds both (spec §2.4, rule 3).
func TestACompromisedEngineGetsOneSignatureAndARefusalInASlot(t *testing.T) {
	signer := newTestSigner(t)
	server := serveSigner(t, signer.service)
	if _, err := consensus.SignVote(signer.remoteSigner(server), blockPrevote); err != nil {
		t.Fatal(err)
	}
	_, err := consensus.SignVote(signer.remoteSigner(server), nilPrevote)
	if err == nil || !strings.Contains(err.Error(), "409 Conflict in block-revision: "+conflictMark) {
		t.Fatalf("a second prevote in the slot gave %v, want the signer's refusal as a conflict", err)
	}
	if entry := signer.lastAuditEntry(t); !strings.HasPrefix(entry.Refused, conflictMark) || len(entry.Fields) == 0 || len(entry.SignedFields) == 0 {
		t.Fatalf("the audit log's last line is %+v, want the conflict with both requests", entry)
	}
}

// A vote whose request timed out at the engine may still have been logged and signed by the signer,
// which then holds its slot. The remote signer refuses, without asking, another vote in that slot, as
// the driver casts at the step's timeout: the signer would refuse it as a conflict, an alarm against an
// honest engine. The same vote again is sent, and gets the signature the signer gave it.
func TestAVoteThatTimedOutIsNotFollowedByAnotherInItsSlot(t *testing.T) {
	signer := newTestSigner(t)
	server := serveSigner(t, signer.service)
	remote := signer.remoteSigner(server)
	server.slow.Store(true)
	if _, err := consensus.SignVote(remote, blockPrevote); err == nil {
		t.Fatal("a vote answered after the deadline was taken")
	}
	server.slow.Store(false)
	if entry := signer.lastAuditEntry(t); entry.Domain != signing.DomainBlockRevision || entry.Refused != "" {
		t.Fatalf("premise: the signer signed the vote that timed out; its audit log's last line is %+v", entry)
	}

	time.Sleep(time.Second) // past the remote signer's fail-fast window
	if _, err := consensus.SignVote(remote, nilPrevote); err == nil || !strings.Contains(err.Error(), "refused without asking the signer") || server.requests.Load() != 1 {
		t.Fatalf("a nil prevote in the slot gave %v, and %d requests reached the signer, want a refusal and 1", err, server.requests.Load())
	}
	if _, err := consensus.SignVote(remote, blockPrevote); err != nil || server.requests.Load() != 2 {
		t.Fatalf("the prevote that timed out, cast again, gave %v, and %d requests reached the signer, want a signature and 2", err, server.requests.Load())
	}
	if conflicts := signer.conflicts(t); conflicts != 0 {
		t.Fatalf("the signer refused %d requests as conflicts, want none", conflicts)
	}
}

// slowScheme is the network's scheme, whose keys take a tenth of a second to sign: a signer on a slow or
// busy machine.
type slowScheme struct{ signing.Scheme } // implements signing.Scheme

// The compiler checks that slowScheme implements signing.Scheme.
var _ signing.Scheme = slowScheme{}

// ParsePrivateKey parses the key as the network's scheme does, into a key that is slow to sign.
func (scheme slowScheme) ParsePrivateKey(privateKey []byte) (signing.PrivateKey, error) {
	parsedKey, err := scheme.Scheme.ParsePrivateKey(privateKey)
	return slowKey{parsedKey}, err
}

// slowKey is a key that waits a tenth of a second before it signs.
type slowKey struct{ signing.PrivateKey } // implements signing.PrivateKey

// The compiler checks that slowKey implements signing.PrivateKey.
var _ signing.PrivateKey = slowKey{}

// Sign waits a tenth of a second, then signs.
func (key slowKey) Sign(message []byte) ([]byte, error) {
	time.Sleep(100 * time.Millisecond)
	return key.PrivateKey.Sign(message)
}

// A burst of receipts, as when many payments come in at once, does not keep the engine's next vote past
// its deadline of a second. The remote signer asks for one receipt at a time, and refuses the others at
// once, without a request and without its fail-fast window, so the vote waits behind one receipt at
// most, and is signed. Here each signature takes a tenth of a second: the twenty receipts, queued at the
// signer ahead of the vote, would take two.
func TestABurstOfReceiptsDoesNotKeepAVotePastItsDeadline(t *testing.T) {
	signer := newTestSigner(t)
	slowSigner, err := signing.NewSigner(slowScheme{signing.NetworkScheme()}, signer.networkID, signer.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	signer.service.keySigner = slowSigner
	server := serveSigner(t, signer.service)
	remote := signer.remoteSigner(server)

	var receipts sync.WaitGroup
	var refusedAtOnce atomic.Int64
	for i := range 20 {
		receipts.Go(func() {
			_, err := remote.Sign(signing.DomainReceipt, []byte(testBank), []byte("payment-"+strconv.Itoa(i)), []byte("5"))
			if err != nil && strings.Contains(err.Error(), "refused without asking the signer: another receipt is out") {
				refusedAtOnce.Add(1)
			}
		})
	}
	waitUntil(t, time.Second, func() bool { return server.requests.Load() > 0 }, "no receipt reached the signer")
	if _, err := consensus.SignVote(remote, blockPrevote); err != nil {
		t.Fatalf("the vote asked for during a burst of receipts was not signed: %v", err)
	}
	receipts.Wait()
	if refusedAtOnce.Load() == 0 {
		t.Fatal("no receipt of the burst was refused at once")
	}
}
