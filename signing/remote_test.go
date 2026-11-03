package signing

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSigner is a test server in the place of the signer program: it reads each request's domain and
// fields and has answer answer them. It counts the requests it gets.
func fakeSigner(t *testing.T, answer func(responseWriter http.ResponseWriter, domain Domain, fields [][]byte)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		var signRequest struct {
			Domain Domain   `json:"domain"`
			Fields [][]byte `json:"fields"`
		}
		if err := json.NewDecoder(request.Body).Decode(&signRequest); err != nil {
			http.Error(responseWriter, "not a request", http.StatusBadRequest)
			return
		}
		answer(responseWriter, signRequest.Domain, signRequest.Fields)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// answerWith answers with keySigner's signature over the request.
func answerWith(keySigner Signer) func(http.ResponseWriter, Domain, [][]byte) {
	return func(responseWriter http.ResponseWriter, domain Domain, fields [][]byte) {
		signature, _ := keySigner.SignBase64(domain, fields...)
		json.NewEncoder(responseWriter).Encode(map[string]string{"signature": signature})
	}
}

// remoteFor is a remote signer of the bank's key, asking server.
func remoteFor(server *httptest.Server, bank Signer) *RemoteSigner {
	return NewRemoteSigner(server.URL, "engine", bytes.Repeat([]byte{1}, 32), bank.PublicKey(), "testnet", NetworkScheme())
}

// The remote signer returns a signature only once it verifies under the bank's public key: an answer
// signed by another key, or for another network, or one that is no signature, is refused.
func TestTheRemoteSignerRefusesAnAnswerThatDoesNotVerify(t *testing.T) {
	bank, other, elsewhere := mustSigner(t, "testnet"), mustSigner(t, "testnet"), mustSigner(t, "elsewhere")
	for _, test := range []struct {
		name   string
		answer func(http.ResponseWriter, Domain, [][]byte)
		wantOK bool
	}{
		{"the bank's key", answerWith(bank), true},
		{"another key", answerWith(other), false},
		{"another network", answerWith(elsewhere), false},
		{"no signature", func(responseWriter http.ResponseWriter, _ Domain, _ [][]byte) { responseWriter.Write([]byte(`{}`)) }, false},
	} {
		server, _ := fakeSigner(t, test.answer)
		signature, err := remoteFor(server, bank).Sign(DomainReceipt, []byte("bank-a"), []byte("payment"))
		if test.wantOK != (err == nil) {
			t.Errorf("an answer signed by %s: %v, want taken %v", test.name, err, test.wantOK)
		}
		if err != nil && (signature != nil || !strings.Contains(err.Error(), "does not verify under publicKey")) {
			t.Errorf("an answer signed by %s: the error is %v, with %d signature bytes", test.name, err, len(signature))
		}
	}
}

// After a failure, every call fails at once, without a request, for the next second; then it asks
// again. The error carries the signer's status and its reason. The failures are logged once, not at
// every call, and not again within the minute.
func TestTheRemoteSignerFailsFastForASecondAfterAFailure(t *testing.T) {
	logged := &bytes.Buffer{}
	previousOutput := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	bank := mustSigner(t, "testnet")
	var signerUp atomic.Bool
	server, requests := fakeSigner(t, func(responseWriter http.ResponseWriter, domain Domain, fields [][]byte) {
		if !signerUp.Load() {
			http.Error(responseWriter, "the signer failed: its log says why", http.StatusInternalServerError)
			return
		}
		answerWith(bank)(responseWriter, domain, fields)
	})
	remote := remoteFor(server, bank)

	if _, err := remote.Sign(DomainReceipt); err == nil || !strings.Contains(err.Error(), "500 Internal Server Error in receipt: the signer failed: its log says why") {
		t.Fatalf("a failing signer gave %v, want its status and its reason", err)
	}
	if _, err := remote.Sign(DomainReceipt); err == nil || requests.Load() != 1 {
		t.Fatalf("a call just after a failure gave %v, and %d requests reached the signer, want an error and 1", err, requests.Load())
	}
	time.Sleep(remoteFailFastWindow)
	if _, err := remote.Sign(DomainReceipt); err == nil || requests.Load() != 2 {
		t.Fatalf("a call a second after a failure gave %v, and %d requests reached the signer, want an error and 2", err, requests.Load())
	}
	signerUp.Store(true)
	time.Sleep(remoteFailFastWindow)
	if _, err := remote.Sign(DomainReceipt); err != nil || requests.Load() != 3 {
		t.Fatalf("a call to a signer back up gave %v, and %d requests reached the signer, want a signature and 3", err, requests.Load())
	}
	if lines := strings.Count(logged.String(), "signer: "); lines != 1 || !strings.Contains(logged.String(), "requests to the signer at "+server.URL+"/sign fail") {
		t.Fatalf("the log holds %d lines about the signer, want 1 that says it fails:\n%s", lines, logged)
	}
}

// The slot memory forgets every slot below the highest height sent: the signer refuses them anyway.
func TestTheSlotMemoryForgetsHeightsBelowTheHighestSent(t *testing.T) {
	remote := NewRemoteSigner("http://signer", "engine", nil, nil, "testnet", NetworkScheme())
	voteAt := func(height string, blockByte byte) [][]byte {
		return [][]byte{[]byte(height), []byte("0"), {1}, bytes.Repeat([]byte{blockByte}, 32), []byte("bank-a")}
	}
	if err := remote.claimSlot(DomainBlockRevision, voteAt("5", 1)); err != nil {
		t.Fatal(err)
	}
	if err := remote.claimSlot(DomainBlockRevision, voteAt("5", 2)); err == nil {
		t.Fatal("other fields in a slot sent were not refused")
	}
	if err := remote.claimSlot(DomainBlockRevision, voteAt("6", 1)); err != nil || len(remote.slotFields) != 1 {
		t.Fatalf("a slot at height 6 gave %v, and the memory holds %d slots, want 1", err, len(remote.slotFields))
	}
}
