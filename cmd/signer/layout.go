package main

// The layouts: what each domain's fields must be for the signer to sign them (spec §2.4, rules 1 and 2).
// Each is the signFields of the message type that signs in the domain, field for field:
// consensus/vote.go, consensus/proposal.go, ledger/receipt.go, ledger/name_request.go,
// ledger/payment.go, ledger/account_update.go, ledger/governance.go and ledger/reassignment.go. A change
// to one of those must change its layout here, or the signer refuses what the code signs; the tests
// check each layout against the code.
//
// The signer checks the fields its rules read (a slot, a name that must be the bank's) and the form of
// every number, hash, flag and list count, as the code writes them. The rest, another name, a key, an
// address, a comment, it leaves to the ledger, which refuses a malformed message whoever signed it.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// keySelfTest is the domain of the signature that the engine makes at start, to prove its key pair
// works (cmd/server/config.go). Its one field is the engine's name, which must be the bank's, so an
// engine of another name stops at its start, not at its first vote. Every client may ask for it.
const keySelfTest signing.Domain = "key-self-test"

// slot is what the signer signs at most once (spec §2.4, rule 3): a vote's height, round and type, or a
// proposal's height and round.
type slot struct {
	Domain   signing.Domain `json:"domain"` // block-revision for a vote, or proposal
	Height   int64          `json:"height"`
	Round    int64          `json:"round"`
	VoteType int            `json:"voteType,omitempty"` // a vote's: 1, prevote, or 2, precommit; 0 for a proposal
}

// layout checks fields against a domain's layout, and that each field naming who signs names ownName,
// the bank. It returns the slot the request takes, nil for a domain without one, and a summary a person
// can read, for the audit log; or the refusal: 400 for fields of another layout, 403 for another name.
type layout func(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error)

// layouts holds the layout of every domain the signer signs in. name-request-new-key and
// reassignment-new-key are not among them: a customer's own new key signs those (spec §2.3).
var layouts = map[signing.Domain]layout{
	keySelfTest:                    checkKeySelfTest,
	signing.DomainBlockRevision:    checkVote,
	signing.DomainProposal:         checkProposal,
	signing.DomainReceipt:          checkReceipt,
	signing.DomainRegistration:     checkNameRequest,
	signing.DomainKeyChange:        checkNameRequest,
	signing.DomainBankChange:       checkNameRequest,
	signing.DomainFreeze:           checkNameRequest,
	signing.DomainNameRequestBank:  checkNameRequest,
	signing.DomainPayment:          checkPayment,
	signing.DomainAccountUpdate:    checkAccountUpdate,
	signing.DomainParameterVote:    checkParameterVote,
	signing.DomainReassignmentVote: checkReassignmentVote,
}

// checkKeySelfTest is key-self-test's layout: the name, the bank.
func checkKeySelfTest(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, anyBytes) {
		return nil, "", malformed(domain)
	}
	return nil, "the key self-test", requireOwnName(fields[0], ownName, "name")
}

// checkVote is block-revision's layout (Vote.signFields): the height, the round, the type, the block
// hash (all zeros for a nil vote), and the validator, the bank.
func checkVote(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, isDecimal, isRound, isVoteType, isHash, anyBytes) {
		return nil, "", malformed(domain)
	}
	height, round, voteType := decimalValue(fields[0]), decimalValue(fields[1]), consensus.VoteType(fields[2][0])
	summary := fmt.Sprintf("%s at height %d, round %d, for %s", voteType, height, round, blockLabel(fields[3]))
	return &slot{Domain: domain, Height: height, Round: round, VoteType: int(voteType)}, summary, requireOwnName(fields[4], ownName, "validator")
}

// checkProposal is proposal's layout (SignedProposal.signFields): the height, the round, the valid
// round (-1 for a fresh proposal), the block hash, and the proposer, the bank.
func checkProposal(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, isDecimal, isRound, isValidRound, isHash, anyBytes) {
		return nil, "", malformed(domain)
	}
	height, round := decimalValue(fields[0]), decimalValue(fields[1])
	summary := fmt.Sprintf("proposal at height %d, round %d, valid round %s, of %s", height, round, fields[2], blockLabel(fields[3]))
	return &slot{Domain: domain, Height: height, Round: round}, summary, requireOwnName(fields[4], ownName, "proposer")
}

// checkReceipt is receipt's layout (Receipt.signFields): the node, the bank; the payment's id; and the
// block it is queued for.
func checkReceipt(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, anyBytes, anyBytes, isDecimal) {
		return nil, "", malformed(domain)
	}
	summary := fmt.Sprintf("receipt for payment %.8s, queued for block %s", fields[1], fields[2])
	return nil, summary, requireOwnName(fields[0], ownName, "node")
}

// checkNameRequest is a name request's layout (NameRequest.signFields), which its requestor signs in
// the domain of its kind, and its bank in name-request-bank: the kind, the name, the requestor, the new
// key, the bank, the fee ("" for a freeze's none) and the block id. The bank signs as its requestor a
// registration, and a key change or a bank change, whose requestor is the name's holder: of its own
// name. It signs a freeze only of its own name, and a co-signature as the bank. Its desk asks for
// registration, name-request-bank and freeze only, never key-change or bank-change (spec §2.3).
func checkNameRequest(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, anyBytes, anyBytes, anyBytes, anyBytes, anyBytes, isOptionalAmount, isDecimal) {
		return nil, "", malformed(domain)
	}
	kind := string(fields[0])
	coSignedKinds := []string{ledger.RegistrationKind, ledger.KeyChangeKind, ledger.BankChangeKind}
	if kind != string(domain) && !(domain == signing.DomainNameRequestBank && slices.Contains(coSignedKinds, kind)) {
		return nil, "", malformed(domain)
	}
	summary := fmt.Sprintf("%s of %q requested by %q, bank %q, block %s", kind, fields[1], fields[2], fields[4], fields[6])
	switch domain {
	case signing.DomainNameRequestBank:
		return nil, "co-signed the " + summary, requireOwnName(fields[4], ownName, "bank")
	case signing.DomainFreeze:
		if err := requireOwnName(fields[1], ownName, "name"); err != nil {
			return nil, summary, err
		}
	}
	return nil, summary, requireOwnName(fields[2], ownName, "requestor")
}

// checkPayment is payment's layout (Payment.signFields): the payer, the bank; the payee; the amount;
// the comment; the block id; and the state digest it is pinned to, or "".
func checkPayment(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, anyBytes, anyBytes, isAmount, anyBytes, isDecimal, anyBytes) {
		return nil, "", malformed(domain)
	}
	summary := fmt.Sprintf("payment of %s from %q to %q, block %s", fields[2], fields[0], fields[1], fields[4])
	return nil, summary, requireOwnName(fields[0], ownName, "payer")
}

// checkAccountUpdate is account-update's layout (AccountUpdate.signFields): the name, the bank; the
// delegate; the description; the public address; the block id; then the IP addresses and the TLS keys,
// each list as its count and its items (listFields).
func checkAccountUpdate(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if len(fields) < 7 || !fits(fields[:6], anyBytes, anyBytes, anyBytes, anyBytes, isDecimal, isDecimal) {
		return nil, "", malformed(domain)
	}
	ipAddressCount := decimalValue(fields[5])
	if ipAddressCount > int64(len(fields)-7) {
		return nil, "", malformed(domain)
	}
	tlsKeyCount := fields[6+ipAddressCount]
	if !isDecimal(tlsKeyCount) || decimalValue(tlsKeyCount) != int64(len(fields))-7-ipAddressCount {
		return nil, "", malformed(domain)
	}
	summary := fmt.Sprintf("account update of %q, block %s", fields[0], fields[4])
	return nil, summary, requireOwnName(fields[0], ownName, "name")
}

// checkParameterVote is parameter-vote's layout (parameterVoteFields): the ten parameters, as
// LedgerParameters.fields writes them, then the digest of the parameters they replace and the
// governance sequence. It names no voter: the client's domain list is its only gate.
func checkParameterVote(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, isAmount, isDecimal, isDecimal, isDecimal, isDecimal, isAmount, isDecimal, isDecimal, isAmount, isAmount, isHash, isDecimal) {
		return nil, "", malformed(domain)
	}
	return nil, fmt.Sprintf("parameter vote at governance sequence %s", fields[11]), nil
}

// checkReassignmentVote is reassignment-vote's layout (Reassignment.voteFields): the name, the balance
// it holds and the one to set ("" for none), the new key, the bank, freeze and unfreeze, and the
// governance sequence. It names no voter: the client's domain list is its only gate.
func checkReassignmentVote(domain signing.Domain, fields [][]byte, ownName string) (*slot, string, error) {
	if !fits(fields, anyBytes, isOptionalAmount, isOptionalAmount, anyBytes, anyBytes, isFlag, isFlag, isDecimal) {
		return nil, "", malformed(domain)
	}
	return nil, fmt.Sprintf("reassignment vote on %q at governance sequence %s", fields[0], fields[7]), nil
}

// malformed refuses fields that do not fit domain's layout.
func malformed(domain signing.Domain) error {
	return refusal{http.StatusBadRequest, fmt.Sprintf("the fields do not fit the layout of %s", domain)}
}

// requireOwnName refuses a field that names someone other than the bank where it names who signs, in
// role (spec §2.4, rule 2).
func requireOwnName(field []byte, ownName, role string) error {
	if string(field) == ownName {
		return nil
	}
	return refusal{http.StatusForbidden, fmt.Sprintf("the %s is %q, not this bank, %q: the signer signs only in its own name", role, field, ownName)}
}

// fits reports whether there is one field for each form, and each field has the form at its place.
func fits(fields [][]byte, forms ...func(field []byte) bool) bool {
	if len(fields) != len(forms) {
		return false
	}
	for i, form := range forms {
		if !form(fields[i]) {
			return false
		}
	}
	return true
}

// The forms of a field, as the code that signs writes it.

// anyBytes is a field the signer does not read the form of: a name, a key, an address, free text.
func anyBytes([]byte) bool { return true }

// isDecimal is a number from 0 up as strconv.FormatInt writes it: digits only, no sign, no leading zero.
func isDecimal(field []byte) bool {
	value, err := strconv.ParseInt(string(field), 10, 64)
	return err == nil && value >= 0 && strconv.FormatInt(value, 10) == string(field)
}

// decimalValue is the value of a field isDecimal accepted.
func decimalValue(field []byte) int64 {
	value, _ := strconv.ParseInt(string(field), 10, 64)
	return value
}

// isRound is a round: a decimal that fits an int32, as consensus keeps it.
func isRound(field []byte) bool { return isDecimal(field) && decimalValue(field) <= math.MaxInt32 }

// isValidRound is a proposal's valid round: a round, or -1 for a fresh proposal.
func isValidRound(field []byte) bool { return string(field) == "-1" || isRound(field) }

// isVoteType is a vote's type: one byte, 1 (prevote) or 2 (precommit).
func isVoteType(field []byte) bool {
	return len(field) == 1 && (consensus.VoteType(field[0]) == consensus.Prevote || consensus.VoteType(field[0]) == consensus.Precommit)
}

// isHash is a block hash or a digest: 32 bytes.
func isHash(field []byte) bool { return len(field) == 32 }

// isAmount is an amount as ledger.FormatAmount writes it.
func isAmount(field []byte) bool {
	amount, err := ledger.ParseAmount(string(field))
	return err == nil && ledger.FormatAmount(amount) == string(field)
}

// isOptionalAmount is an amount, or "", FormatAmount's form of none.
func isOptionalAmount(field []byte) bool { return len(field) == 0 || isAmount(field) }

// isFlag is a boolean as the ledger signs it: 1 or 0.
func isFlag(field []byte) bool { return string(field) == "1" || string(field) == "0" }

// blockLabel names a block for a person by the first 8 hex digits of its hash, or "nil" for a nil
// vote's hash of all zeros.
func blockLabel(hash []byte) string {
	if bytes.Equal(hash, make([]byte, 32)) {
		return "nil"
	}
	return "block " + hex.EncodeToString(hash[:4])
}
