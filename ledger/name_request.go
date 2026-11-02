package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The four kinds of name request (spec 4.3).
const (
	RegistrationKind = "registration" // a requestor pays for a free name, and a seated bank co-signs
	KeyChangeKind    = "key-change"   // a holder moves its name to a new key, and its bank of record co-signs
	BankChangeKind   = "bank-change"  // a holder moves its name to a new seated bank, which co-signs
	FreezeKind       = "freeze"       // a holder freezes its own name, free and at once
)

// requestorDomains is the domain the requestor signs in, for each kind: the kind's own, so a signer
// that sees only the domain knows what it signs (spec 4.0).
var requestorDomains = map[string]signing.Domain{
	RegistrationKind: signing.DomainRegistration,
	KeyChangeKind:    signing.DomainKeyChange,
	BankChangeKind:   signing.DomainBankChange,
	FreezeKind:       signing.DomainFreeze,
}

// NameRequest is a holder's or requestor's signed request about one name (spec 4.3): a registration, a
// key change, a bank change or a freeze. Every signature on it covers the same fields, all but the
// signatures, so none can be lifted onto another request or another kind.
type NameRequest struct { // implements json.Marshaler and json.Unmarshaler
	Kind               string
	Name               string   // the name the request is about
	Requestor          string   // the name that signs first and pays the fee; the name itself, except in a registration
	NewKey             string   // the key to bind, for a registration or a key change; else ""
	Bank               string   // the co-signing bank, or for a bank change the new bank; "" for a freeze
	Fee                *big.Rat // NewKeyFee, burned from the requestor; nil for a freeze
	BlockID            int64    // the height of the one block that may carry it (the replay guard)
	RequestorSignature string   // the requestor's, in the domain of the kind
	BankSignature      string   // the bank's, in the "name-request-bank" domain; "" for a freeze
	NewKeySignature    string   // the new key's, in the "name-request-new-key" domain; "" without a new key
}

// usesNewKey reports whether the request's kind binds a new key: a registration or a key change.
func (request NameRequest) usesNewKey() bool {
	return request.Kind == RegistrationKind || request.Kind == KeyChangeKind
}

// usesBank reports whether the request's kind has a bank, a bank signature and a fee: all but a freeze.
func (request NameRequest) usesBank() bool { return request.Kind != FreezeKind }

// fee is the request's fee, zero when it is null (a freeze has none).
func (request NameRequest) fee() *big.Rat {
	if request.Fee == nil {
		return new(big.Rat)
	}
	return request.Fee
}

func (request NameRequest) signFields() [][]byte {
	return [][]byte{
		[]byte(request.Kind),
		[]byte(request.Name),
		[]byte(request.Requestor),
		[]byte(request.NewKey),
		[]byte(request.Bank),
		[]byte(FormatAmount(request.Fee)), // "" for a freeze's null fee
		[]byte(strconv.FormatInt(request.BlockID, 10)),
	}
}

// NameRequestID is a malleability-proof identity for a request: a hash of its signed fields, never its
// signatures. The pool and the relay key on it.
func NameRequestID(request NameRequest) string {
	return hashFields("name-request-id", request.signFields())
}

// SignAsRequestor returns a copy of request with the requestor's signature, made by signer in the
// domain of the request's kind.
func SignAsRequestor(signer *signing.Signer, request NameRequest) (NameRequest, error) {
	signature, err := signer.SignBase64(requestorDomains[request.Kind], request.signFields()...)
	request.RequestorSignature = signature
	return request, err
}

// SignAsBank returns a copy of request with the co-signing bank's signature, made by signer.
func SignAsBank(signer *signing.Signer, request NameRequest) (NameRequest, error) {
	signature, err := signer.SignBase64(signing.DomainNameRequestBank, request.signFields()...)
	request.BankSignature = signature
	return request, err
}

// SignAsNewKey returns a copy of request with the new key's proof of possession, made by signer, which
// holds the new key.
func SignAsNewKey(signer *signing.Signer, request NameRequest) (NameRequest, error) {
	signature, err := signer.SignBase64(signing.DomainNameRequestNewKey, request.signFields()...)
	request.NewKeySignature = signature
	return request, err
}

// VerifyNameRequest reports whether every signature the request's kind uses verifies: the requestor's
// under requestorKey, the bank's under bankKey, and the new key's under NewKey. The caller takes the
// two keys from the state, the current keys of Requestor and Bank. Shape checks come first
// (NameRequestShapeError).
func VerifyNameRequest(scheme signing.Scheme, networkID, requestorKey, bankKey string, request NameRequest) bool {
	fields := request.signFields()
	if !signing.VerifyBase64(scheme, networkID, requestorKey, requestorDomains[request.Kind], request.RequestorSignature, fields...) {
		return false
	}
	if request.usesBank() && !signing.VerifyBase64(scheme, networkID, bankKey, signing.DomainNameRequestBank, request.BankSignature, fields...) {
		return false
	}
	return !request.usesNewKey() || signing.VerifyBase64(scheme, networkID, request.NewKey, signing.DomainNameRequestNewKey, request.NewKeySignature, fields...)
}

// NameRequestShapeError is the shape check of spec 4.3, which runs before anything else: a known kind;
// canonical names; a requestor that is the name itself, except in a registration; each field the kind
// uses set and each other one empty; a new key the network's scheme accepts, in its one spelling
// (signing.ValidatePublicKeyBase64); signatures of the exact length; a block id above 0.
func NameRequestShapeError(request NameRequest) error {
	if _, known := requestorDomains[request.Kind]; !known {
		return fmt.Errorf("unknown kind %q", request.Kind)
	}
	scheme := signing.NetworkScheme()
	switch {
	case !CanonicalName(request.Name) || !CanonicalName(request.Requestor) || (request.Bank != "" && !CanonicalName(request.Bank)):
		return errors.New("a name it carries is not a canonical name")
	case request.Kind != RegistrationKind && request.Requestor != request.Name:
		return errors.New("only a registration's requestor may be another name")
	case request.RequestorSignature == "",
		request.usesNewKey() != (request.NewKey != "") || request.usesNewKey() != (request.NewKeySignature != ""),
		request.usesBank() != (request.Bank != "") || request.usesBank() != (request.BankSignature != "") || request.usesBank() != (request.Fee != nil):
		return fmt.Errorf("its fields do not fit a %s: it sets one its kind leaves empty, or leaves one empty that its kind sets", request.Kind)
	case request.NewKey != "" && signing.ValidatePublicKeyBase64(scheme, request.NewKey) != nil:
		return errors.New("its new key is not a public key of the network's scheme in its one spelling")
	case request.BlockID <= 0:
		return errors.New("its block id is not above 0")
	}
	for _, signature := range []string{request.RequestorSignature, request.BankSignature, request.NewKeySignature} {
		if signature != "" && len(signature) != signing.SignatureBase64Length(scheme) {
			return errors.New("a signature is not of the exact length")
		}
	}
	return nil
}

// keyInUse reports whether key is some account's current key.
func keyInUse(accounts []Account, key string) bool {
	for _, account := range accounts {
		if account.PublicKey == key {
			return true
		}
	}
	return false
}

// hasPendingChange reports whether the account has a key or bank change waiting.
func hasPendingChange(account Account) bool { return account.PendingHeight != 0 }

// NameRequestFitsState checks a request of a known kind against a state and the validator set it
// seats, the pre-block state in a block or the committed state at intake: the rules of its kind (spec
// 4.4 to 4.7) except the signatures, and that the fee payer's balance covers the fee. It returns an
// error naming the first rule the request breaks. The block it names, the signatures and the rules
// about the other items of a block are the caller's to check.
func NameRequestFitsState(state LedgerState, validators *consensus.ValidatorSet, request NameRequest) error {
	account, exists := FindAccount(state.Accounts, request.Name)
	requestor, requestorExists := FindAccount(state.Accounts, request.Requestor) // the name itself, except in a registration
	bank, bankExists := FindAccount(state.Accounts, request.Bank)
	switch request.Kind {
	case RegistrationKind:
		switch {
		case exists:
			return fmt.Errorf("the name %q is taken", request.Name)
		case !requestorExists || requestor.Frozen:
			return fmt.Errorf("the requestor %q has no account, or is frozen", request.Requestor)
		case !bankExists || bank.Frozen || !validators.Contains(request.Bank):
			return fmt.Errorf("the bank %q is not a seated bank that is not frozen", request.Bank)
		case request.Bank != request.Requestor && request.Bank != requestor.Bank:
			return fmt.Errorf("the bank %q is neither the requestor nor its bank of record", request.Bank)
		}
	case KeyChangeKind, BankChangeKind:
		switch {
		case !exists || account.Frozen:
			return fmt.Errorf("the name %q has no account, or is frozen", request.Name)
		case validators.Contains(request.Name):
			return fmt.Errorf("the name %q is seated: a seated name's key and bank change only by the ⅔", request.Name)
		case hasPendingChange(account):
			return fmt.Errorf("the name %q already has a change waiting", request.Name)
		case !bankExists || bank.Frozen:
			return fmt.Errorf("the bank %q has no account, or is frozen", request.Bank)
		case request.Kind == KeyChangeKind && request.Bank != account.Bank:
			return fmt.Errorf("the bank %q is not the name's bank of record", request.Bank)
		case request.Kind == BankChangeKind && (request.Bank == request.Name || !validators.Contains(request.Bank)):
			return fmt.Errorf("the new bank %q is not another, seated name", request.Bank)
		}
	case FreezeKind:
		if !exists || account.Frozen {
			return fmt.Errorf("the name %q has no account, or is frozen already", request.Name)
		}
	}
	switch {
	case request.usesNewKey() && keyInUse(state.Accounts, request.NewKey):
		return errors.New("the new key is already a name's key")
	case request.usesBank() && request.fee().Cmp(state.Parameters.NewKeyFee) != 0:
		return errors.New("the fee is not the network's new-key fee")
	case request.fee().Cmp(AccountAmount(state.Accounts, request.Requestor)) > 0:
		return errors.New("the fee is above the requestor's balance")
	}
	return nil
}

// FeesFit is the one fee rule (spec 5.5), for intake, ProposeBlock and ApplyBlock alike: a payer can
// pay fees adding up to totalFees for a block when they are at most its balance and it sends no payment
// in that block. A block prefers a payer's requests: its payments for that block lapse.
func FeesFit(balance, totalFees *big.Rat, sendsPayment bool) bool {
	return !sendsPayment && totalFees.Cmp(balance) <= 0
}

// applyNameRequests applies the requests of a block other than its freezes, which its caller has
// checked (spec 5.3 step I.3): each fee is burned from its requestor, each registration creates its
// account at once, with a zero balance and its bank as the bank of record, and each key or bank change
// waits until dueHeight. The accounts stay in name order.
func applyNameRequests(accounts []Account, requests []NameRequest, dueHeight int64) []Account {
	result := append([]Account(nil), accounts...) // copy-on-write: never mutate the previous state's accounts
	for _, request := range requests {
		if request.Kind == FreezeKind {
			continue
		}
		requestorIndex, _ := findAccountIndex(result, request.Requestor)
		result[requestorIndex].Amount = new(big.Rat).Sub(result[requestorIndex].Amount, request.Fee) // a new big.Rat: amounts are shared, never mutated
		index, _ := findAccountIndex(result, request.Name)
		switch request.Kind {
		case RegistrationKind:
			result = slices.Insert(result, index, Account{Name: request.Name, PublicKey: request.NewKey, Amount: new(big.Rat), Bank: request.Bank})
		case KeyChangeKind:
			result[index].PendingKey, result[index].PendingHeight = request.NewKey, dueHeight
		case BankChangeKind:
			result[index].PendingBank, result[index].PendingHeight = request.Bank, dueHeight
		}
	}
	return result
}

// clearPendingChange drops an account's waiting key or bank change.
func clearPendingChange(account *Account) {
	account.PendingKey, account.PendingBank, account.PendingHeight = "", "", 0
}

// freezeNames freezes the accounts named in frozen (spec 5.3 step I.4, 4.7): each is marked frozen and
// loses its pending change, and so does every pending change it co-signed (R11), the key change of a
// name whose bank of record it is and a bank change to it. The balance stays (R5).
func freezeNames(accounts []Account, frozen map[string]bool) []Account {
	if len(frozen) == 0 {
		return accounts
	}
	result := append([]Account(nil), accounts...)
	for index := range result {
		account := &result[index]
		switch {
		case frozen[account.Name]:
			account.Frozen = true
			clearPendingChange(account)
		case account.PendingKey != "" && frozen[account.Bank], account.PendingBank != "" && frozen[account.PendingBank]:
			clearPendingChange(account)
		}
	}
	return result
}

// applyDueChanges applies or lets lapse each pending change that falls due at the end of the block at
// height (spec 5.7), in name order, each judged against the accounts the changes before it left. A
// change lapses if its name is seated in validators, the block's set, or if its new key is by then some
// name's key; otherwise the new key or bank replaces the current one. Either way the pending fields are
// cleared, and the fee stays burned.
func applyDueChanges(accounts []Account, validators *consensus.ValidatorSet, height int64) []Account {
	var result []Account // a copy, made at the first due change
	for index, account := range accounts {
		if account.PendingHeight != height {
			continue
		}
		if result == nil {
			result = append([]Account(nil), accounts...)
		}
		lapses := validators.Contains(account.Name) || (account.PendingKey != "" && keyInUse(result, account.PendingKey))
		switch {
		case lapses:
		case account.PendingKey != "":
			result[index].PublicKey = account.PendingKey
		default:
			result[index].Bank = account.PendingBank
		}
		clearPendingChange(&result[index])
	}
	if result == nil {
		return accounts
	}
	return result
}

// MarshalJSON writes a NameRequest with its fee in canonical form, or null for a freeze.
func (request NameRequest) MarshalJSON() ([]byte, error) {
	type plain NameRequest
	return json.Marshal(struct {
		plain
		Fee amountJSON
	}{plain(request), amountJSON{&request.Fee}})
}

// UnmarshalJSON decodes a NameRequest, reading its fee through ParseAmount. The fee may be null (a
// freeze has none); the shape check refuses a null fee for the other kinds.
func (request *NameRequest) UnmarshalJSON(data []byte) error {
	type plain NameRequest
	return json.Unmarshal(data, &struct {
		*plain
		Fee amountJSON
	}{(*plain)(request), amountJSON{&request.Fee}})
}
