package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The bank whose key the test signer holds, and another bank.
const (
	testBank  = "bank-a"
	otherBank = "bank-b"
)

// testSigner is a signer's files, as writeTestSigner writes them, and the service its last load made.
type testSigner struct {
	configPath   string
	genesisPath  string
	signLogPath  string
	auditLogPath string
	secrets      map[string][]byte // each client's
	privateKey   []byte
	publicKey    string // base64
	networkID    string
	service      *SigningService
}

// writeTestSigner writes the files of a signer for testBank: a genesis, a plaintext key file, a secret
// for each of the three clients of spec §2.3, and the config. It loads nothing. The admin has key-change
// and bank-change too, which the desk never has, so the tests reach their layout.
func writeTestSigner(t *testing.T) *testSigner {
	t.Helper()
	directory := t.TempDir()
	publicKey, privateKey, err := signing.NetworkScheme().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := &testSigner{
		configPath:   filepath.Join(directory, "signer.json"),
		genesisPath:  filepath.Join(directory, "genesis.json"),
		signLogPath:  filepath.Join(directory, "sign-log.jsonl"),
		auditLogPath: filepath.Join(directory, "audit-log.jsonl"),
		secrets:      map[string][]byte{},
		privateKey:   privateKey,
		publicKey:    base64.StdEncoding.EncodeToString(publicKey),
	}
	keyFilePath := filepath.Join(directory, "key.json")
	if err := signing.WriteKeyFile(keyFilePath, publicKey, privateKey, ""); err != nil {
		t.Fatal(err)
	}
	signer.writeGenesis(t, "testnet", map[string]string{testBank: signer.publicKey})
	clientDomains := map[string][]signing.Domain{
		"engine": {signing.DomainBlockRevision, signing.DomainProposal, signing.DomainReceipt},
		"desk":   {signing.DomainRegistration, signing.DomainNameRequestBank, signing.DomainFreeze},
		"admin": {signing.DomainParameterVote, signing.DomainReassignmentVote, signing.DomainPayment, signing.DomainAccountUpdate, signing.DomainFreeze,
			signing.DomainKeyChange, signing.DomainBankChange},
	}
	var clients []clientConfig
	for clientName, domains := range clientDomains {
		signer.secrets[clientName] = bytes.Repeat([]byte{byte(len(clients) + 1)}, 32)
		secretFile := filepath.Join(directory, clientName+".secret")
		if err := os.WriteFile(secretFile, []byte(hex.EncodeToString(signer.secrets[clientName])+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, clientConfig{Name: clientName, SecretFile: secretFile, Domains: domains})
	}
	signer.writeConfig(t, fileConfig{
		Listen:       "127.0.0.1:0",
		AllowFrom:    []string{"127.0.0.1"},
		Genesis:      signer.genesisPath,
		Name:         testBank,
		KeyFile:      keyFilePath,
		SignLogPath:  signer.signLogPath,
		AuditLogPath: signer.auditLogPath,
		Clients:      clients,
	})
	return signer
}

// newTestSigner is a test signer loaded with a new sign log that refuses nothing above height 0.
func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	signer := writeTestSigner(t)
	signer.load(t, 0)
	return signer
}

// writeGenesis writes a genesis for a network named networkName that seats each bank of publicKeys, a
// name and its key, with power 1, at timings short enough for engines to commit in a test
// (engine_test.go), and keeps the network id it makes. It returns the chain and the state it makes.
func (signer *testSigner) writeGenesis(t *testing.T, networkName string, publicKeys map[string]string) (ledger.Chain, ledger.LedgerState) {
	t.Helper()
	var validators, accounts []map[string]any
	for _, name := range slices.Sorted(maps.Keys(publicKeys)) {
		validators = append(validators, map[string]any{"name": name, "power": 1})
		accounts = append(accounts, map[string]any{"name": name, "publicKey": publicKeys[name], "amount": "1000", "delegate": name})
	}
	parameters := ledger.DefaultParameters()
	genesis := map[string]any{
		"networkName": networkName,
		"gossipPort":  8090,
		"parameters": map[string]any{
			"transactionCost": "0.1", "blockIntervalMs": 100, "stepTimeoutMs": 1000, "maxPeers": 0,
			"maxMessageBytes": parameters.MaxMessageBytes, "newKeyFee": "1", "changeDelayBlocks": 86400,
			"retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000",
		},
		"validators": validators,
		"accounts":   accounts,
	}
	writeJSONFile(t, signer.genesisPath, genesis)
	data, _ := os.ReadFile(signer.genesisPath)
	parsedGenesis, err := ledger.ParseGenesis(data)
	if err != nil {
		t.Fatal(err)
	}
	chain, state, err := ledger.ValidateGenesis(parsedGenesis)
	if err != nil {
		t.Fatal(err)
	}
	signer.networkID = chain.NetworkID
	return chain, state
}

// writeConfig writes configFile as the signer's config.
func (signer *testSigner) writeConfig(t *testing.T, configFile fileConfig) {
	t.Helper()
	writeJSONFile(t, signer.configPath, configFile)
}

// readConfig reads the signer's config back, for a test to change a field of it.
func (signer *testSigner) readConfig(t *testing.T) fileConfig {
	t.Helper()
	data, err := os.ReadFile(signer.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var configFile fileConfig
	if err := json.Unmarshal(data, &configFile); err != nil {
		t.Fatal(err)
	}
	return configFile
}

// load starts the signer, or restarts it, from its files: a new service, with resumeAbove as the
// -resume-above flag gives it (-1 without the flag).
func (signer *testSigner) load(t *testing.T, resumeAbove int64) {
	t.Helper()
	service, err := loadService(signer.configPath, resumeAbove)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	signer.service = service
}

// loadService is start without the bind and the lock: the service of the config at configPath.
func loadService(configPath string, resumeAbove int64) (*SigningService, error) {
	configFile, _, err := load(configPath, resumeAbove)
	if err != nil {
		return nil, err
	}
	return newSigningService(configFile, resumeAbove)
}

// ask sends the signer a request from clientName, under its secret, and returns the status and the
// answer's body.
func (signer *testSigner) ask(t *testing.T, clientName string, domain signing.Domain, fields [][]byte) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"domain": domain, "fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	return signer.send("HDLS-HMAC "+clientName+" "+bodyHMAC(signer.secrets[clientName], body), body)
}

// send sends the signer body with authorization as its Authorization header.
func (signer *testSigner) send(authorization string, body []byte) (int, string) {
	request := httptest.NewRequest(http.MethodPost, "/sign", bytes.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	signer.service.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// signature asks the signer for a signature that it must give, checks that it verifies with
// signing.Verify under the bank's key, and returns it in base64.
func (signer *testSigner) signature(t *testing.T, clientName string, domain signing.Domain, fields [][]byte) string {
	t.Helper()
	status, answer := signer.ask(t, clientName, domain, fields)
	if status != http.StatusOK {
		t.Fatalf("%s asking in %s: %d %s", clientName, domain, status, answer)
	}
	var signed struct{ Signature string }
	if err := json.Unmarshal([]byte(answer), &signed); err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(signed.Signature)
	publicKey, _ := base64.StdEncoding.DecodeString(signer.publicKey)
	if err != nil || !signing.Verify(signing.NetworkScheme(), signer.networkID, publicKey, domain, signature, fields...) {
		t.Fatalf("the signature in %s does not verify under the bank's key", domain)
	}
	return signed.Signature
}

// refused asks the signer for a signature that it must refuse with wantStatus and an answer holding
// wantReason.
func (signer *testSigner) refused(t *testing.T, clientName string, domain signing.Domain, fields [][]byte, wantStatus int, wantReason string) {
	t.Helper()
	status, answer := signer.ask(t, clientName, domain, fields)
	if status != wantStatus || !strings.Contains(answer, wantReason) {
		t.Fatalf("%s asking in %s: got %d %q, want %d and %q", clientName, domain, status, answer, wantStatus, wantReason)
	}
}

// auditEntries reads the audit log.
func (signer *testSigner) auditEntries(t *testing.T) []auditEntry {
	t.Helper()
	data, err := os.ReadFile(signer.auditLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []auditEntry
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var entry auditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("audit log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// lastAuditEntry is the audit log's last line.
func (signer *testSigner) lastAuditEntry(t *testing.T) auditEntry {
	t.Helper()
	entries := signer.auditEntries(t)
	return entries[len(entries)-1]
}

// vote is the fields of testBank's vote, as consensus/vote.go signs them, for a block whose hash is
// all blockByte.
func vote(height int64, round int32, voteType consensus.VoteType, blockByte byte) [][]byte {
	return voteFields(consensus.Vote{Height: height, Round: round, Type: voteType, BlockHash: [32]byte(bytes.Repeat([]byte{blockByte}, 32)), Validator: testBank})
}

// proposal is the fields of testBank's fresh proposal, as consensus/proposal.go signs them, of a block
// whose hash is all blockByte.
func proposal(height int64, round int32, blockByte byte) [][]byte {
	return proposalFields(consensus.SignedProposal{Height: height, Round: round, ValidRound: -1, BlockHash: [32]byte(bytes.Repeat([]byte{blockByte}, 32)), Proposer: testBank})
}

// writeJSONFile writes value as JSON to the file at path.
func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A second, different vote for a slot already signed is refused, and the audit log gets one line marked
// REFUSED CONFLICT with both requests. So is a second, different proposal. A vote of the other type, or
// of another round, is another slot.
func TestASecondDifferentVoteForASignedSlotIsRefusedAsAConflict(t *testing.T) {
	signer := newTestSigner(t)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 2), http.StatusConflict, conflictMark)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 0), http.StatusConflict, conflictMark) // nil
	entry := signer.lastAuditEntry(t)
	if !strings.HasPrefix(entry.Refused, conflictMark) || !bytes.Equal(entry.Fields[3], vote(5, 0, consensus.Prevote, 0)[3]) ||
		!bytes.Equal(entry.SignedFields[3], vote(5, 0, consensus.Prevote, 1)[3]) {
		t.Fatalf("the audit log's line for the conflict is %+v, want it marked %s with both requests", entry, conflictMark)
	}

	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 2))
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 1, consensus.Prevote, 2))
	signer.signature(t, "engine", signing.DomainProposal, proposal(5, 0, 1))
	signer.refused(t, "engine", signing.DomainProposal, proposal(5, 0, 2), http.StatusConflict, conflictMark)
	reproposal := proposal(5, 0, 1)
	reproposal[2] = []byte("0") // another valid round, the same slot
	signer.refused(t, "engine", signing.DomainProposal, reproposal, http.StatusConflict, conflictMark)
	signer.signature(t, "engine", signing.DomainProposal, proposal(5, 1, 2))
}

// Two different votes for one slot, asked at once, get one signature between them: the service decides
// and signs one request at a time.
func TestTwoVotesForOneSlotAskedAtOnceGetOneSignature(t *testing.T) {
	signer := newTestSigner(t)
	for round := range int32(100) {
		var votes sync.WaitGroup
		statuses := make([]int, 2)
		for i := range statuses {
			votes.Go(func() {
				statuses[i], _ = signer.ask(t, "engine", signing.DomainBlockRevision, vote(5, round, consensus.Prevote, byte(i+1)))
			})
		}
		votes.Wait()
		if statuses[0] == http.StatusOK && statuses[1] == http.StatusOK {
			t.Fatalf("round %d: both prevotes were signed", round)
		}
	}
}

// The same vote again gets the same signature, before and after a restart: ML-DSA signs
// deterministically, so a repeat gives away nothing new.
func TestTheSameVoteAgainGetsTheSameSignature(t *testing.T) {
	signer := newTestSigner(t)
	first := signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	if again := signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1)); again != first {
		t.Fatal("the same vote again got another signature")
	}
	signer.load(t, -1)
	if again := signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1)); again != first {
		t.Fatal("the same vote after a restart got another signature")
	}
}

// A vote or a proposal for a new slot below the highest height signed is refused. A repeat of a slot
// the log holds, of the highest height or the one below, is answered; once the height moves on past
// it, the slot is forgotten and refused like any other below.
func TestAVoteBelowTheHighestHeightIsRefused(t *testing.T) {
	signer := newTestSigner(t)
	atNine := signer.signature(t, "engine", signing.DomainBlockRevision, vote(9, 0, consensus.Prevote, 1))
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(10, 0, consensus.Prevote, 1))
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(9, 0, consensus.Precommit, 1), http.StatusConflict, "below 10")
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(9, 1, consensus.Prevote, 1), http.StatusConflict, "below 10")
	signer.refused(t, "engine", signing.DomainProposal, proposal(9, 1, 1), http.StatusConflict, "below 10")
	if again := signer.signature(t, "engine", signing.DomainBlockRevision, vote(9, 0, consensus.Prevote, 1)); again != atNine {
		t.Fatal("a repeat of a slot of the height below got another signature")
	}
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(11, 0, consensus.Prevote, 1))
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(9, 0, consensus.Prevote, 1), http.StatusConflict, "below 11")
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(10, 0, consensus.Prevote, 1))
	signer.load(t, -1) // the log read again holds the same
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(9, 0, consensus.Prevote, 1), http.StatusConflict, "below 11")
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(10, 0, consensus.Prevote, 2), http.StatusConflict, conflictMark)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(10, 0, consensus.Prevote, 1))
}

// A client asks only for the domains in its list, and key-self-test, which every client may ask for.
func TestADomainNotInTheClientsListIsRefused(t *testing.T) {
	signer := newTestSigner(t)
	payment := [][]byte{[]byte(testBank), []byte("alice"), []byte("5"), nil, []byte("9"), nil}
	signer.refused(t, "engine", signing.DomainPayment, payment, http.StatusForbidden, "not in this client's list")
	signer.refused(t, "desk", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1), http.StatusForbidden, "not in this client's list")
	signer.refused(t, "admin", signing.DomainProposal, proposal(5, 0, 1), http.StatusForbidden, "not in this client's list")
	signer.refused(t, "desk", signing.DomainNameRequestNewKey, nil, http.StatusForbidden, "not in this client's list")
	if entry := signer.lastAuditEntry(t); entry.Client != "desk" || entry.Domain != signing.DomainNameRequestNewKey || entry.Refused == "" {
		t.Fatalf("the audit log's last line is %+v, want the desk's refusal", entry)
	}
	for _, clientName := range []string{"engine", "desk", "admin"} {
		signer.signature(t, clientName, keySelfTest, [][]byte{[]byte(testBank)})
	}
	signer.signature(t, "admin", signing.DomainPayment, payment)
}

// A request with no Authorization header, one of another form, an unknown client, or an HMAC that does
// not match the body under the client's secret is refused, and written to the audit log.
func TestABadHMACAndAnUnknownClientAreRefused(t *testing.T) {
	signer := newTestSigner(t)
	body := []byte(`{"domain":"key-self-test","fields":["YmFuay1h"]}`) // bank-a's self-test
	rightHMAC := bodyHMAC(signer.secrets["engine"], body)
	for _, test := range []struct{ name, authorization string }{
		{"no header", ""},
		{"another scheme", "Bearer " + rightHMAC},
		{"no client", "HDLS-HMAC " + rightHMAC},
		{"an unknown client", "HDLS-HMAC teller " + rightHMAC},
		{"another client's secret", "HDLS-HMAC desk " + rightHMAC},
		{"the HMAC of another body", "HDLS-HMAC engine " + bodyHMAC(signer.secrets["engine"], append(body, ' '))},
		{"uppercase hex", "HDLS-HMAC engine " + strings.ToUpper(rightHMAC)},
	} {
		if status, answer := signer.send(test.authorization, body); status != http.StatusUnauthorized {
			t.Errorf("%s: got %d %s, want 401", test.name, status, answer)
		}
		if entry := signer.lastAuditEntry(t); entry.Refused == "" {
			t.Errorf("%s: the audit log's last line is %+v, want a refusal", test.name, entry)
		}
	}
	if status, answer := signer.send("HDLS-HMAC engine "+rightHMAC, body); status != http.StatusOK {
		t.Fatalf("the right HMAC: got %d %s", status, answer)
	}
	mac := hmac.New(sha256.New, signer.secrets["engine"])
	mac.Write(body)
	if hex.EncodeToString(mac.Sum(nil)) != rightHMAC {
		t.Fatal("bodyHMAC is not HMAC-SHA256 under the secret's 32 bytes, in lowercase hex")
	}
}

// The sign log is on disk before the answer. A signer restarted after it answered a vote refuses a
// conflicting one. A signer that crashes after it wrote the log and before it answered has the slot
// taken when it restarts: a conflicting vote is refused, and the same vote gets its signature.
func TestTheSignLogIsOnDiskBeforeTheAnswer(t *testing.T) {
	signer := newTestSigner(t)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	signer.load(t, -1)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 2), http.StatusConflict, conflictMark)

	crashingSigner, err := signing.NewSigner(crashingScheme{signing.NetworkScheme()}, signer.networkID, signer.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	signer.service.keySigner = crashingSigner
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the signer did not crash")
			}
		}()
		signer.ask(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 1))
	}()
	signer.load(t, -1)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 2), http.StatusConflict, conflictMark)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 1))
}

// crashingScheme is the network's scheme, whose keys crash the signer as they are about to sign: a test
// stops the signer between its sign log write and its answer.
type crashingScheme struct{ signing.Scheme } // implements signing.Scheme

// The compiler checks that crashingScheme implements signing.Scheme.
var _ signing.Scheme = crashingScheme{}

// ParsePrivateKey parses the key as the network's scheme does, into a key that crashes when it signs.
func (scheme crashingScheme) ParsePrivateKey(privateKey []byte) (signing.PrivateKey, error) {
	parsedKey, err := scheme.Scheme.ParsePrivateKey(privateKey)
	return crashingKey{parsedKey}, err
}

// crashingKey is a key that crashes the program when it signs.
type crashingKey struct{ signing.PrivateKey } // implements signing.PrivateKey

// The compiler checks that crashingKey implements signing.PrivateKey.
var _ signing.PrivateKey = crashingKey{}

// Sign crashes.
func (crashingKey) Sign([]byte) ([]byte, error) { panic("the signer crashed before it signed") }

// A signer started without a sign log refuses every vote and proposal, and signs the rest, and writes
// no log. Started with -resume-above, it writes one that refuses every slot at or below that height, and
// keeps refusing them after a restart. -resume-above where a log exists is an error.
func TestWithoutASignLogEveryVoteIsRefusedUntilResumeAbove(t *testing.T) {
	signer := writeTestSigner(t)
	signer.load(t, -1)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1), http.StatusConflict, "-resume-above")
	signer.refused(t, "engine", signing.DomainProposal, proposal(5, 0, 1), http.StatusConflict, "-resume-above")
	signer.signature(t, "engine", signing.DomainReceipt, [][]byte{[]byte(testBank), []byte("ab12"), []byte("5")})
	signer.signature(t, "admin", signing.DomainPayment, [][]byte{[]byte(testBank), []byte("alice"), []byte("5"), nil, []byte("9"), nil})
	if _, err := os.Stat(signer.signLogPath); !os.IsNotExist(err) {
		t.Fatalf("a signer without a sign log wrote one (%v)", err)
	}

	signer.load(t, 10)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(10, 0, consensus.Prevote, 1), http.StatusConflict, "at or below 10")
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(9, 3, consensus.Precommit, 1), http.StatusConflict, "at or below 10")
	signer.refused(t, "engine", signing.DomainProposal, proposal(10, 0, 1), http.StatusConflict, "at or below 10")
	atEleven := signer.signature(t, "engine", signing.DomainBlockRevision, vote(11, 0, consensus.Prevote, 1))

	if _, err := loadService(signer.configPath, 10); err == nil || !strings.Contains(err.Error(), "-resume-above") {
		t.Fatalf("-resume-above where a sign log exists: got %v, want an error", err)
	}
	signer.load(t, -1)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(10, 5, consensus.Prevote, 1), http.StatusConflict, "at or below 10")
	if again := signer.signature(t, "engine", signing.DomainBlockRevision, vote(11, 0, consensus.Prevote, 1)); again != atEleven {
		t.Fatal("the vote at height 11 after a restart got another signature")
	}
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(12, 0, consensus.Prevote, 1))
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(10, 5, consensus.Prevote, 1), http.StatusConflict, "at or below 10")
}

// A sign log of another network stops the signer. A last line a crash cut short, with no newline, was
// never fsynced, so its slot was never signed: the log is read without it.
func TestTheSignLogIsReadForItsNetworkOnly(t *testing.T) {
	signer := newTestSigner(t)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	file, err := os.OpenFile(signer.signLogPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(`{"slot":{"domain":"block-revision","height":5,"round":0,"voteType":2},"fields":["`)
	file.Close()
	signer.load(t, -1)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 2))
	signer.load(t, -1)
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 1), http.StatusConflict, conflictMark)

	signer.writeGenesis(t, "othernet", map[string]string{testBank: signer.publicKey})
	if _, err := loadService(signer.configPath, -1); err == nil || !strings.Contains(err.Error(), "another network's") {
		t.Fatalf("a sign log of another network: got %v, want an error", err)
	}
}

// A signLogPath that is a symbolic link is refused at start, and the link left as it is: the sign log's
// rewrite renames a new file over the link, not over the file it points to, and a restart that finds
// the link again, as in a container whose image holds it, would read that old file, which lacks the
// slots signed since, and sign them again.
func TestASignLogPathThatIsALinkIsRefused(t *testing.T) {
	signer := newTestSigner(t)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	realPath := filepath.Join(t.TempDir(), "sign-log.jsonl")
	if err := os.Rename(signer.signLogPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, signer.signLogPath); err != nil {
		t.Fatal(err)
	}
	if _, err := loadService(signer.configPath, -1); err == nil || !strings.Contains(err.Error(), "give signLogPath as the real path") {
		t.Fatalf("a signLogPath that is a link: got %v, want an error", err)
	}
	if linkInfo, err := os.Lstat(signer.signLogPath); err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced (%v)", err)
	}
}

// A request whose client has gone by its turn, as the engine gives up after a second, is not signed, so
// a backlog does not outlive its deadline: here the vote's slot stays free, and another vote in it is
// signed.
func TestARequestWhoseClientHasGoneIsNotSigned(t *testing.T) {
	signer := newTestSigner(t)
	body, err := json.Marshal(map[string]any{"domain": signing.DomainBlockRevision, "fields": vote(5, 0, consensus.Prevote, 1)})
	if err != nil {
		t.Fatal(err)
	}
	clientGone, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequestWithContext(clientGone, http.MethodPost, "/sign", bytes.NewReader(body))
	request.Header.Set("Authorization", "HDLS-HMAC engine "+bodyHMAC(signer.secrets["engine"], body))
	signer.service.ServeHTTP(httptest.NewRecorder(), request)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 2))
}

// A sign log write that fails stops the signer, and the vote is not signed: running on, the signer would
// leave a line cut short, which stops its next start, or a slot free here that the log on disk holds.
// Here the write finds no space, as on a full disk.
func TestASignLogWriteThatFailsStopsTheSigner(t *testing.T) {
	signer := newTestSigner(t)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	var stopLine string
	signer.service.stopProgram = func(format string, arguments ...any) { stopLine = fmt.Sprintf(format, arguments...) }
	if err := os.Remove(signer.signLogPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/full", signer.signLogPath); err != nil { // every write fails: no space left
		t.Fatal(err)
	}
	signer.refused(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 1), http.StatusInternalServerError, "the signer failed")
	if !strings.Contains(stopLine, "the signer stops") {
		t.Fatalf("a sign log write that failed did not stop the signer (%q)", stopLine)
	}
}

// A second start of a running signer, by an operator reading its key's digest or by a second container
// on its volume, is refused before it reads the sign log, and leaves the log as it was, byte for byte
// and the same file: on the same address at the bind, and on another, as from another network
// namespace, at the lock on the sign log's folder. Rewritten under the running signer, the log would
// lose the slots it signed in between.
func TestASecondStartWhileTheSignerRunsLeavesItsSignLog(t *testing.T) {
	signer := writeTestSigner(t)
	service, listener, _, err := start(signer.configPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	signer.service = service
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Precommit, 1))
	signLog, _ := os.ReadFile(signer.signLogPath)
	signLogFile, _ := os.Stat(signer.signLogPath)

	configFile := signer.readConfig(t)
	for _, test := range []struct{ listen, wantError string }{
		{listener.Addr().String(), "address already in use"},
		{"127.0.0.1:0", "locked by a signer running on it"},
	} {
		configFile.Listen = test.listen
		signer.writeConfig(t, configFile)
		if _, _, _, err := start(signer.configPath, -1); err == nil || !strings.Contains(err.Error(), test.wantError) {
			t.Errorf("a second start on %s: got %v, want an error with %q", test.listen, err, test.wantError)
		}
		after, _ := os.ReadFile(signer.signLogPath)
		afterFile, _ := os.Stat(signer.signLogPath)
		if !bytes.Equal(after, signLog) || !os.SameFile(afterFile, signLogFile) {
			t.Errorf("a second start on %s rewrote the sign log:\n%s", test.listen, after)
		}
	}
}

// The listener closes, at accept, a connection from an address not in allowFrom, before any HTTP; one
// from an address in it is served, and so is an IPv4 caller that a dual-stack listener reports in IPv6
// dress, ::ffff:127.0.0.1, when its IPv4 address is in allowFrom.
func TestAConnectionFromAnAddressNotInAllowFromIsRefused(t *testing.T) {
	signer := newTestSigner(t)
	for _, test := range []struct {
		allowFrom  []string
		dualStack  bool
		wantServed bool
	}{
		{[]string{"192.0.2.0/24", "2001:db8::/32"}, false, false},
		{[]string{"127.0.0.2"}, false, false},
		{[]string{"192.0.2.7", "127.0.0.1"}, false, true},
		{[]string{"127.0.0.0/8"}, false, true},
		{[]string{"127.0.0.1"}, true, true},
		{[]string{"127.0.0.2"}, true, false},
	} {
		allowFrom, err := allowFromPrefixes(test.allowFrom)
		if err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if test.dualStack {
			listener = dualStackListener{listener}
		}
		httpServer := &http.Server{Handler: signer.service}
		go httpServer.Serve(&allowFromListener{Listener: listener, allowFrom: allowFrom})
		response, err := http.Post("http://"+listener.Addr().String()+"/sign", "application/json", strings.NewReader("{}"))
		if served := err == nil; served != test.wantServed {
			t.Errorf("allowFrom %v, dual-stack %v: served %v (%v), want %v", test.allowFrom, test.dualStack, served, err, test.wantServed)
		}
		if err == nil {
			if response.StatusCode != http.StatusUnauthorized {
				t.Errorf("allowFrom %v: got %d, want 401 for a request with no Authorization", test.allowFrom, response.StatusCode)
			}
			response.Body.Close()
		}
		httpServer.Close()
	}
}

// dualStackListener reports each caller's IPv4 address in IPv6 dress, ::ffff:a.b.c.d, as a dual-stack
// listener does.
type dualStackListener struct{ net.Listener } // implements net.Listener

// The compiler checks that dualStackListener implements net.Listener.
var _ net.Listener = dualStackListener{}

// Accept accepts the next connection, and gives it its remote address in IPv6 dress.
func (listener dualStackListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	remoteAddress := connection.RemoteAddr().(*net.TCPAddr)
	return dualStackConnection{connection, &net.TCPAddr{IP: remoteAddress.IP.To16(), Port: remoteAddress.Port}}, nil
}

// dualStackConnection is a connection whose remote address is remoteAddress.
type dualStackConnection struct { // implements net.Conn
	net.Conn
	remoteAddress *net.TCPAddr
}

// The compiler checks that dualStackConnection implements net.Conn.
var _ net.Conn = dualStackConnection{}

// RemoteAddr returns remoteAddress.
func (connection dualStackConnection) RemoteAddr() net.Addr { return connection.remoteAddress }

// load reads a sealed key with its passphrase, and refuses a config that would sign with the wrong key,
// for anyone, or as one client for another.
func TestLoadRefusesAConfigItCannotKeep(t *testing.T) {
	signer := writeTestSigner(t)
	directory := filepath.Dir(signer.configPath)
	passphraseFile := filepath.Join(directory, "passphrase")
	if err := os.WriteFile(passphraseFile, []byte("throwaway test passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongPassphraseFile := filepath.Join(directory, "wrong-passphrase")
	if err := os.WriteFile(wrongPassphraseFile, []byte("another passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publicKey, _ := base64.StdEncoding.DecodeString(signer.publicKey)
	sealedKeyFile := filepath.Join(directory, "sealed-key.json")
	if err := signing.WriteKeyFile(sealedKeyFile, publicKey, signer.privateKey, "throwaway test passphrase"); err != nil {
		t.Fatal(err)
	}
	uppercaseSecretFile := filepath.Join(directory, "uppercase.secret")
	if err := os.WriteFile(uppercaseSecretFile, []byte(strings.ToUpper(hex.EncodeToString(bytes.Repeat([]byte{0xab}, 32)))), 0o600); err != nil {
		t.Fatal(err)
	}
	base := signer.readConfig(t)
	sealed := base
	sealed.KeyFile, sealed.PassphraseFile = sealedKeyFile, passphraseFile
	signer.writeConfig(t, sealed)
	signer.load(t, 0)
	signer.signature(t, "engine", signing.DomainBlockRevision, vote(5, 0, consensus.Prevote, 1))

	for _, test := range []struct {
		name   string
		change func(configFile *fileConfig)
	}{
		{"a sealed key without its passphrase", func(configFile *fileConfig) { configFile.KeyFile = sealedKeyFile }},
		{"a wrong passphrase", func(configFile *fileConfig) {
			configFile.KeyFile, configFile.PassphraseFile = sealedKeyFile, wrongPassphraseFile
		}},
		{"a passphrase for a plaintext key", func(configFile *fileConfig) { configFile.PassphraseFile = passphraseFile }},
		{"a name that is not one", func(configFile *fileConfig) { configFile.Name = "Bank A" }},
		{"no allowFrom", func(configFile *fileConfig) { configFile.AllowFrom = nil }},
		{"an allowFrom with bits past its length", func(configFile *fileConfig) { configFile.AllowFrom = []string{"198.51.100.7/24"} }},
		{"no clients", func(configFile *fileConfig) { configFile.Clients = nil }},
		{"two clients with one secret", func(configFile *fileConfig) {
			configFile.Clients = append(configFile.Clients[:0:0], configFile.Clients...)
			configFile.Clients[1].SecretFile = configFile.Clients[0].SecretFile
		}},
		{"a secret in uppercase", func(configFile *fileConfig) {
			configFile.Clients = append(configFile.Clients[:0:0], configFile.Clients...)
			configFile.Clients[0].SecretFile = uppercaseSecretFile
		}},
		{"a domain a customer's new key signs", func(configFile *fileConfig) {
			configFile.Clients = append(configFile.Clients[:0:0], configFile.Clients...)
			configFile.Clients[0].Domains = []signing.Domain{signing.DomainNameRequestNewKey}
		}},
	} {
		configFile := base
		test.change(&configFile)
		signer.writeConfig(t, configFile)
		if _, err := loadService(signer.configPath, -1); err == nil {
			t.Errorf("%s: loaded, want an error", test.name)
		}
	}
}
