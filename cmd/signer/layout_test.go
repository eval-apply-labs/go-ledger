package main

import (
	"crypto/sha256"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The fields of each message, written by hand from the signFields of its type (unexported). The tests
// below check each against the code: a signature over them verifies with the type's own Verify.

func voteFields(vote consensus.Vote) [][]byte {
	return [][]byte{[]byte(strconv.FormatInt(vote.Height, 10)), []byte(strconv.FormatInt(int64(vote.Round), 10)),
		{byte(vote.Type)}, vote.BlockHash[:], []byte(vote.Validator)}
}

func proposalFields(proposal consensus.SignedProposal) [][]byte {
	return [][]byte{[]byte(strconv.FormatInt(proposal.Height, 10)), []byte(strconv.FormatInt(int64(proposal.Round), 10)),
		[]byte(strconv.FormatInt(int64(proposal.ValidRound), 10)), proposal.BlockHash[:], []byte(proposal.Proposer)}
}

func receiptFields(receipt ledger.Receipt) [][]byte {
	return [][]byte{[]byte(receipt.Node), []byte(receipt.PaymentID), []byte(strconv.FormatInt(receipt.BlockID, 10))}
}

func nameRequestFields(request ledger.NameRequest) [][]byte {
	return [][]byte{[]byte(request.Kind), []byte(request.Name), []byte(request.Requestor), []byte(request.NewKey),
		[]byte(request.Bank), []byte(ledger.FormatAmount(request.Fee)), []byte(strconv.FormatInt(request.BlockID, 10))}
}

func paymentFields(payment ledger.Payment) [][]byte {
	return [][]byte{[]byte(payment.From), []byte(payment.To), []byte(ledger.FormatAmount(payment.Amount)), []byte(payment.Comment),
		[]byte(strconv.FormatInt(payment.BlockID, 10)), []byte(payment.PreviousStateDigest)}
}

func accountUpdateFields(update ledger.AccountUpdate) [][]byte {
	fields := [][]byte{[]byte(update.Name), []byte(update.Delegate), []byte(update.Description), []byte(update.PublicAddress),
		[]byte(strconv.FormatInt(update.BlockID, 10))}
	for _, list := range [][]string{update.IPAddresses, update.TLSKeys} {
		fields = append(fields, []byte(strconv.Itoa(len(list))))
		for _, item := range list {
			fields = append(fields, []byte(item))
		}
	}
	return fields
}

func parameterVoteFields(parameters ledger.LedgerParameters, previousParametersDigest [32]byte, sequence int64) [][]byte {
	return [][]byte{[]byte(ledger.FormatAmount(parameters.TransactionCost)), []byte(strconv.FormatInt(parameters.BlockIntervalMs, 10)),
		[]byte(strconv.FormatInt(parameters.StepTimeoutMs, 10)), []byte(strconv.FormatInt(parameters.MaxPeers, 10)),
		[]byte(strconv.FormatInt(parameters.MaxMessageBytes, 10)), []byte(ledger.FormatAmount(parameters.NewKeyFee)),
		[]byte(strconv.FormatInt(parameters.ChangeDelayBlocks, 10)), []byte(strconv.FormatInt(parameters.RetainBlocks, 10)),
		[]byte(ledger.FormatAmount(parameters.MaxPaymentAmount)), []byte(ledger.FormatAmount(parameters.MaxAccountBalance)),
		previousParametersDigest[:], []byte(strconv.FormatInt(sequence, 10))}
}

func reassignmentVoteFields(reassignment ledger.Reassignment) [][]byte {
	flag := map[bool][]byte{true: []byte("1"), false: []byte("0")}
	return [][]byte{[]byte(reassignment.Name), []byte(ledger.FormatAmount(reassignment.PreviousAmount)),
		[]byte(ledger.FormatAmount(reassignment.NewAmount)), []byte(reassignment.NewKey), []byte(reassignment.Bank),
		flag[reassignment.Freeze], flag[reassignment.Unfreeze], []byte(strconv.FormatInt(reassignment.Sequence, 10))}
}

// testMessages is one message of each kind the bank signs, in its name.
type testMessages struct {
	vote          consensus.Vote
	proposal      consensus.SignedProposal
	payment       ledger.Payment
	receipt       ledger.Receipt
	registration  ledger.NameRequest
	keyChange     ledger.NameRequest
	bankChange    ledger.NameRequest
	freeze        ledger.NameRequest
	accountUpdate ledger.AccountUpdate
	reassignment  ledger.Reassignment
}

func newTestMessages(t *testing.T) testMessages {
	customerKey, err := signing.Generate(signing.NetworkScheme(), "")
	if err != nil {
		t.Fatal(err)
	}
	blockHash := sha256.Sum256([]byte("a block"))
	payment := ledger.Payment{From: testBank, To: "alice", Amount: big.NewRat(25, 2), Comment: "rent", BlockID: 9}
	return testMessages{
		vote:     consensus.Vote{Height: 7, Round: 1, Type: consensus.Precommit, BlockHash: blockHash, Validator: testBank},
		proposal: consensus.SignedProposal{Height: 7, Round: 1, ValidRound: -1, BlockHash: blockHash, Proposer: testBank},
		payment:  payment,
		receipt:  ledger.Receipt{Node: testBank, PaymentID: ledger.PaymentID(payment), BlockID: 9},
		registration: ledger.NameRequest{Kind: ledger.RegistrationKind, Name: "alice", Requestor: testBank,
			NewKey: customerKey.PublicKeyBase64(), Bank: testBank, Fee: big.NewRat(1, 1), BlockID: 9},
		keyChange: ledger.NameRequest{Kind: ledger.KeyChangeKind, Name: testBank, Requestor: testBank,
			NewKey: customerKey.PublicKeyBase64(), Bank: otherBank, Fee: big.NewRat(1, 1), BlockID: 9},
		bankChange: ledger.NameRequest{Kind: ledger.BankChangeKind, Name: testBank, Requestor: testBank, Bank: otherBank, Fee: big.NewRat(1, 1), BlockID: 9},
		freeze:     ledger.NameRequest{Kind: ledger.FreezeKind, Name: testBank, Requestor: testBank, BlockID: 9},
		accountUpdate: ledger.AccountUpdate{Name: testBank, Description: "the bank", PublicAddress: "https://bank-a.example.org",
			IPAddresses: []string{"203.0.113.10", "2001:db8::10"}, TLSKeys: []string{strings.Repeat("0d", 32)}, BlockID: 9},
		reassignment: ledger.Reassignment{Name: "carol", PreviousAmount: big.NewRat(5, 1), NewAmount: big.NewRat(0, 1),
			Bank: ledger.ClearField, Freeze: true, Sequence: 4},
	}
}

// The signer signs each domain's fields, as the code lays them out, from the client whose list has the
// domain; and each signature verifies with signing.Verify under the bank's key (see signature), and with
// the message type's own Verify, which rebuilds the fields from the message: so the fields written by
// hand above are the code's.
func TestEachDomainsSignatureVerifiesAsTheCodeChecksIt(t *testing.T) {
	signer := newTestSigner(t)
	scheme, networkID, publicKey := signing.NetworkScheme(), signer.networkID, signer.publicKey
	messages := newTestMessages(t)
	otherBankKey, err := signing.Generate(scheme, networkID)
	if err != nil {
		t.Fatal(err)
	}

	signer.signature(t, "engine", keySelfTest, [][]byte{[]byte(testBank)})
	vote := messages.vote
	vote.Signature = signer.signature(t, "engine", signing.DomainBlockRevision, voteFields(vote))
	proposal := messages.proposal
	proposal.Signature = signer.signature(t, "engine", signing.DomainProposal, proposalFields(proposal))
	receipt := messages.receipt
	receipt.Signature = signer.signature(t, "engine", signing.DomainReceipt, receiptFields(receipt))
	if !vote.Verify(scheme, networkID, publicKey) || !proposal.Verify(scheme, networkID, publicKey) || !receipt.Verify(scheme, networkID, publicKey) {
		t.Error("the engine's vote, proposal or receipt does not verify as the code checks it")
	}

	// A registration the bank requests and co-signs; a key change and a bank change of its own name,
	// co-signed by another bank; a freeze of its own name, from either terminal.
	signedRequest := func(request ledger.NameRequest, requestorDomain signing.Domain, clientName string) ledger.NameRequest {
		fields := nameRequestFields(request)
		request.RequestorSignature = signer.signature(t, clientName, requestorDomain, fields)
		if request.Bank == testBank {
			request.BankSignature = signer.signature(t, "desk", signing.DomainNameRequestBank, fields)
		} else if request.Bank != "" {
			request, _ = ledger.SignAsBank(otherBankKey, request)
		}
		return request
	}
	newKeyHolder, _ := signing.Generate(scheme, networkID)
	for _, test := range []struct {
		request ledger.NameRequest
		domain  signing.Domain
		client  string
		bankKey string
		withKey bool
	}{
		{messages.registration, signing.DomainRegistration, "desk", publicKey, true},
		{messages.keyChange, signing.DomainKeyChange, "admin", otherBankKey.PublicKeyBase64(), true},
		{messages.bankChange, signing.DomainBankChange, "admin", otherBankKey.PublicKeyBase64(), false},
		{messages.freeze, signing.DomainFreeze, "desk", "", false},
		{messages.freeze, signing.DomainFreeze, "admin", "", false},
	} {
		request := test.request
		if test.withKey {
			request.NewKey = newKeyHolder.PublicKeyBase64()
			request, _ = ledger.SignAsNewKey(newKeyHolder, request)
		}
		request = signedRequest(request, test.domain, test.client)
		if !ledger.VerifyNameRequest(scheme, networkID, publicKey, test.bankKey, request) {
			t.Errorf("the %s from the %s does not verify as the code checks it", request.Kind, test.client)
		}
	}

	payment := messages.payment
	payment.Signature = signer.signature(t, "admin", signing.DomainPayment, paymentFields(payment))
	accountUpdate := messages.accountUpdate
	accountUpdate.Signature = signer.signature(t, "admin", signing.DomainAccountUpdate, accountUpdateFields(accountUpdate))
	parameters := ledger.DefaultParameters()
	previousDigest := parameters.Digest()
	parameters.NewKeyFee = big.NewRat(3, 2)
	parameterVote := ledger.ParameterVote{Validator: testBank,
		Signature: signer.signature(t, "admin", signing.DomainParameterVote, parameterVoteFields(parameters, previousDigest, 3))}
	reassignmentVote := ledger.ReassignmentVote{Validator: testBank,
		Signature: signer.signature(t, "admin", signing.DomainReassignmentVote, reassignmentVoteFields(messages.reassignment))}
	if !ledger.VerifyPayment(scheme, networkID, publicKey, payment) || !ledger.VerifyAccountUpdate(scheme, networkID, publicKey, accountUpdate) ||
		!ledger.VerifyParameterVote(scheme, networkID, publicKey, parameters, previousDigest, 3, parameterVote) ||
		!ledger.VerifyReassignmentVote(scheme, networkID, publicKey, messages.reassignment, reassignmentVote) {
		t.Error("the admin's payment, account update, parameter vote or reassignment vote does not verify as the code checks it")
	}
	if entry := signer.lastAuditEntry(t); entry.Client != "admin" || entry.Domain != signing.DomainReassignmentVote ||
		entry.Refused != "" || !strings.Contains(entry.Summary, `"carol"`) {
		t.Errorf("the audit log's last line is %+v, want the admin's reassignment vote on carol", entry)
	}
}

// A field that names who signs must name the bank: the self-test's name, the validator, the proposer,
// the node, the requestor, a freeze's name, the co-signing bank, the payer and the updated name. The
// refusal is 403, and written to the audit log.
func TestFieldsNamingAnotherBankAreRefused(t *testing.T) {
	signer := newTestSigner(t)
	messages := newTestMessages(t)
	byOther := func(request ledger.NameRequest, change func(request *ledger.NameRequest)) [][]byte {
		change(&request)
		return nameRequestFields(request)
	}
	vote, proposal, receipt, payment, accountUpdate := messages.vote, messages.proposal, messages.receipt, messages.payment, messages.accountUpdate
	vote.Validator, proposal.Proposer, receipt.Node, payment.From, accountUpdate.Name = otherBank, otherBank, otherBank, otherBank, otherBank
	for _, test := range []struct {
		client string
		domain signing.Domain
		fields [][]byte
		role   string
	}{
		{"engine", keySelfTest, [][]byte{[]byte(otherBank)}, "name"},
		{"engine", signing.DomainBlockRevision, voteFields(vote), "validator"},
		{"engine", signing.DomainProposal, proposalFields(proposal), "proposer"},
		{"engine", signing.DomainReceipt, receiptFields(receipt), "node"},
		{"desk", signing.DomainRegistration, byOther(messages.registration, func(request *ledger.NameRequest) { request.Requestor = otherBank }), "requestor"},
		{"admin", signing.DomainKeyChange, byOther(messages.keyChange, func(request *ledger.NameRequest) { request.Name, request.Requestor = otherBank, otherBank }), "requestor"},
		{"admin", signing.DomainBankChange, byOther(messages.bankChange, func(request *ledger.NameRequest) { request.Name, request.Requestor = otherBank, otherBank }), "requestor"},
		{"desk", signing.DomainFreeze, byOther(messages.freeze, func(request *ledger.NameRequest) { request.Name, request.Requestor = otherBank, otherBank }), "name"},
		{"admin", signing.DomainFreeze, byOther(messages.freeze, func(request *ledger.NameRequest) { request.Name = otherBank }), "name"},
		{"admin", signing.DomainFreeze, byOther(messages.freeze, func(request *ledger.NameRequest) { request.Requestor = otherBank }), "requestor"},
		{"desk", signing.DomainNameRequestBank, byOther(messages.registration, func(request *ledger.NameRequest) { request.Bank = otherBank }), "bank"},
		{"desk", signing.DomainNameRequestBank, nameRequestFields(messages.keyChange), "bank"},
		{"admin", signing.DomainPayment, paymentFields(payment), "payer"},
		{"admin", signing.DomainAccountUpdate, accountUpdateFields(accountUpdate), "name"},
	} {
		signer.refused(t, test.client, test.domain, test.fields, http.StatusForbidden, "the "+test.role+" is \""+otherBank+"\"")
		if entry := signer.lastAuditEntry(t); entry.Domain != test.domain || !strings.Contains(entry.Refused, "its own name") {
			t.Errorf("%s: the audit log's last line is %+v, want the refusal", test.domain, entry)
		}
	}
}

// Fields that do not fit the domain's layout are refused with 400: a wrong count, a decimal in any form
// but strconv's, a type, hash, flag or amount of another form, list counts that do not add up, a name
// request whose kind is not its domain's.
func TestFieldsOfAnotherLayoutAreRefused(t *testing.T) {
	signer := newTestSigner(t)
	messages := newTestMessages(t)
	changed := func(fields [][]byte, index int, value string) [][]byte {
		fields[index] = []byte(value)
		return fields
	}
	vote := func() [][]byte { return voteFields(messages.vote) }
	proposal := func() [][]byte { return proposalFields(messages.proposal) }
	update := func() [][]byte { return accountUpdateFields(messages.accountUpdate) } // 2 IP addresses, 1 TLS key
	parameters := ledger.DefaultParameters()
	for _, test := range []struct {
		name   string
		client string
		domain signing.Domain
		fields [][]byte
	}{
		{"a self-test without its name", "engine", keySelfTest, nil},
		{"a vote of four fields", "engine", signing.DomainBlockRevision, vote()[:4]},
		{"a vote of six fields", "engine", signing.DomainBlockRevision, append(vote(), []byte("x"))},
		{"a height with a leading zero", "engine", signing.DomainBlockRevision, changed(vote(), 0, "07")},
		{"a height with a sign", "engine", signing.DomainBlockRevision, changed(vote(), 0, "+7")},
		{"a negative height", "engine", signing.DomainBlockRevision, changed(vote(), 0, "-7")},
		{"an empty round", "engine", signing.DomainBlockRevision, changed(vote(), 1, "")},
		{"a round past an int32", "engine", signing.DomainBlockRevision, changed(vote(), 1, "2147483648")},
		{"a vote type 3", "engine", signing.DomainBlockRevision, changed(vote(), 2, "\x03")},
		{"a vote type in text", "engine", signing.DomainBlockRevision, changed(vote(), 2, "1")},
		{"a hash of 31 bytes", "engine", signing.DomainBlockRevision, changed(vote(), 3, strings.Repeat("x", 31))},
		{"a valid round -2", "engine", signing.DomainProposal, changed(proposal(), 2, "-2")},
		{"a valid round -0", "engine", signing.DomainProposal, changed(proposal(), 2, "-0")},
		{"a receipt's block id in hex", "engine", signing.DomainReceipt, changed(receiptFields(messages.receipt), 2, "0x9")},
		{"a registration signed as a key change", "admin", signing.DomainKeyChange, nameRequestFields(messages.registration)},
		{"a freeze co-signed by a bank", "desk", signing.DomainNameRequestBank, nameRequestFields(messages.freeze)},
		{"a fee with a trailing zero", "desk", signing.DomainRegistration, changed(nameRequestFields(messages.registration), 5, "1.0")},
		{"an amount with a leading zero", "admin", signing.DomainPayment, changed(paymentFields(messages.payment), 2, "012.5")},
		{"a payment without its amount", "admin", signing.DomainPayment, changed(paymentFields(messages.payment), 2, "")},
		{"an update with no lists", "admin", signing.DomainAccountUpdate, update()[:5]},
		{"an update with one list", "admin", signing.DomainAccountUpdate, update()[:8]},
		{"an IP address count past the fields", "admin", signing.DomainAccountUpdate, changed(update(), 5, "9")},
		{"a TLS key count short of the fields", "admin", signing.DomainAccountUpdate, changed(update(), 8, "0")},
		{"a TLS key count past the fields", "admin", signing.DomainAccountUpdate, changed(update(), 8, "2")},
		{"an update with a field after its lists", "admin", signing.DomainAccountUpdate, append(update(), []byte("x"))},
		{"a parameter vote of eleven fields", "admin", signing.DomainParameterVote, parameterVoteFields(parameters, parameters.Digest(), 3)[:11]},
		{"a parameter in another form", "admin", signing.DomainParameterVote, changed(parameterVoteFields(parameters, parameters.Digest(), 3), 1, "1e3")},
		{"a flag that is neither 1 nor 0", "admin", signing.DomainReassignmentVote, changed(reassignmentVoteFields(messages.reassignment), 5, "true")},
	} {
		status, answer := signer.ask(t, test.client, test.domain, test.fields)
		if status != http.StatusBadRequest || !strings.Contains(answer, "layout") {
			t.Errorf("%s: got %d %q, want 400", test.name, status, answer)
		}
	}
	withoutLists := messages.accountUpdate
	withoutLists.IPAddresses, withoutLists.TLSKeys = nil, nil
	signer.signature(t, "admin", signing.DomainAccountUpdate, accountUpdateFields(withoutLists)) // seven fields, the least
}
