package main

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
)

// The Requests page (spec 7.3, 7.4) drafts, signs and submits name requests: a registration, a key
// change and a bank change. Like a governance vote, a request travels between wallets as text, and the
// node receives only the complete request. Draft shows the facts and the request text, and signs
// nothing. Sign takes request text, checks it against the verified state, and signs every role this
// wallet holds. Submit posts a complete request. A bank's desk is this page in the bank operator's
// wallet, which holds the bank's one key. No node route signs anything.

// The lead is how many blocks past the height in progress a drafted request names: how long its signers
// have to sign it. A node takes a request up to node.MaxRequestLookahead blocks ahead (spec 4.3).
const defaultRequestLead = 120

// hashName is the hash name of a key (spec 7.7), a wallet convention the protocol knows nothing of: "k-"
// and the first 15 bytes of the SHA-256 of the raw key, in lowercase base32 without padding. It is 26
// characters, a name, for a wallet that wants no name of its own.
func hashName(publicKey string) string {
	raw, _ := base64.StdEncoding.DecodeString(publicKey)
	digest := sha256.Sum256(raw)
	return "k-" + strings.ToLower(base32.StdEncoding.EncodeToString(digest[:15]))
}

// requestForm is a name request as typed in the Requests page's draft form. Name is the name to
// register (blank for this wallet's hash name), Requestor the name that pays for a registration, NewKey
// the key a key change moves the name to, Bank the new bank of a bank change, and Lead the blocks its
// signers need (blank for defaultRequestLead).
type requestForm struct {
	Kind, Name, Requestor, NewKey, Bank, Lead string
}

// requestStep is what a step of the Requests page shows: the request's facts, the roles this wallet
// signed (none in a draft), and the request as text, to hand to the next signer or to submit.
type requestStep struct {
	Facts  []string
	Signed []string
	Text   string
}

// DraftNameRequest drafts a request and signs nothing (spec 7.3). It fills in what the verified state
// says: a registration binds this wallet's key to the name, and its bank is the requestor itself when the
// requestor is a bank, else the requestor's bank of record; a key change or a bank change is about this
// wallet's name, and a key change's bank is that name's bank of record. Every request carries the
// network's new-key fee and names the block lead blocks past the height in progress. It takes no value
// of the payment counter. A request the network would refuse is refused here first.
func (wallet *Wallet) DraftNameRequest(checkpoint node.Checkpoint, form requestForm) (requestStep, error) {
	state, validators := checkpoint.State, consensus.NewValidatorSet(checkpoint.Validators)
	lead := int64(defaultRequestLead)
	if form.Lead != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(form.Lead), 10, 64)
		if err != nil || parsed < 1 || parsed > node.MaxRequestLookahead {
			return requestStep{}, fmt.Errorf("the signers' time is 1 to %d blocks, not %q", node.MaxRequestLookahead, form.Lead)
		}
		lead = parsed
	}
	height, err := wallet.height()
	if err != nil {
		return requestStep{}, err
	}
	request := ledger.NameRequest{Kind: form.Kind, Fee: state.Parameters.NewKeyFee, BlockID: height + lead}
	switch form.Kind {
	case ledger.RegistrationKind:
		request.NewKey, request.Name = wallet.PublicKey(), hashName(wallet.PublicKey())
		if strings.TrimSpace(form.Name) != "" {
			if request.Name, err = typedName(form.Name); err != nil {
				return requestStep{}, err
			}
		}
		if request.Requestor, err = typedName(form.Requestor); err != nil {
			return requestStep{}, fmt.Errorf("the requestor: %w", err)
		}
		requestor, _ := ledger.FindAccount(state.Accounts, request.Requestor)
		request.Bank = requestor.Bank
		if validators.Contains(request.Requestor) {
			request.Bank = request.Requestor // a bank pays for its own customer (R3)
		}
		if request.Bank == "" {
			return requestStep{}, fmt.Errorf("no bank can co-sign: the requestor %s is not a seated bank and has no bank of record", request.Requestor)
		}
	case ledger.KeyChangeKind, ledger.BankChangeKind:
		if request.Name, err = wallet.registeredName(checkpoint); err != nil {
			return requestStep{}, err
		}
		request.Requestor = request.Name
		if form.Kind == ledger.KeyChangeKind {
			account, _ := ledger.FindAccount(state.Accounts, request.Name)
			if request.Bank = account.Bank; request.Bank == "" {
				return requestStep{}, errors.New("your name has no bank of record to co-sign: only a ⅔ vote of the banks can change its key")
			}
			if request.NewKey, err = typedKey(form.NewKey); err != nil {
				return requestStep{}, err
			}
		} else if request.Bank, err = typedName(form.Bank); err != nil {
			return requestStep{}, fmt.Errorf("the new bank: %w", err)
		}
	default:
		return requestStep{}, errors.New("choose a registration, a key change or a bank change")
	}
	if err := ledger.NameRequestFitsState(state, validators, request); err != nil {
		return requestStep{}, fmt.Errorf("the network would refuse this request: %w", err)
	}
	text, _ := json.Marshal(request)
	return requestStep{Facts: requestFacts(request), Text: string(text)}, nil
}

// SignNameRequest signs request text, this wallet's own draft or one handed to it, in every role this
// wallet holds (spec 7.3): as requestor when its name is the request's Requestor, as bank when its name
// is the Bank, and as the new key when its key is the NewKey. It first checks the request against the
// verified state: its kind (a freeze goes through the freeze button), its fee, that only a registration
// names another requestor, that its block lies in the node's intake window (from the height in progress
// to node.MaxRequestLookahead past it, so a signature never lasts longer than a node would take it),
// and the rules of its kind (ledger.NameRequestFitsState). The full shape check waits for Submit, since
// a request is not complete until every role has signed. When it signs as the fee payer it keeps the
// request's block, which its payments then skip.
func (wallet *Wallet) SignNameRequest(checkpoint node.Checkpoint, text string) (requestStep, error) {
	var request ledger.NameRequest
	if json.Unmarshal([]byte(text), &request) != nil {
		return requestStep{}, errors.New("unreadable request: paste the whole request text")
	}
	switch {
	case request.Kind != ledger.RegistrationKind && request.Kind != ledger.KeyChangeKind && request.Kind != ledger.BankChangeKind:
		return requestStep{}, errors.New("not a registration, a key change or a bank change")
	case request.Fee == nil:
		return requestStep{}, errors.New("the request carries no fee")
	case request.Kind != ledger.RegistrationKind && request.Requestor != request.Name:
		return requestStep{}, errors.New("only a registration's requestor may be another name than the one the request is about")
	case request.BlockID <= checkpoint.State.Height:
		return requestStep{}, fmt.Errorf("the request names block %d, which has passed: draft and sign it again", request.BlockID)
	case request.BlockID > checkpoint.State.Height+1+node.MaxRequestLookahead:
		return requestStep{}, fmt.Errorf("the request names block %d, more than %d blocks past the height in progress %d, which no node takes: draft and sign it again", request.BlockID, node.MaxRequestLookahead, checkpoint.State.Height+1)
	}
	if err := ledger.NameRequestFitsState(checkpoint.State, consensus.NewValidatorSet(checkpoint.Validators), request); err != nil {
		return requestStep{}, fmt.Errorf("the network would refuse this request: %w", err)
	}
	signer, err := wallet.signer()
	if err != nil {
		return requestStep{}, err
	}
	name := nameOfKey(checkpoint.State.Accounts, wallet.PublicKey())
	var signed []string
	if name != "" && name == request.Requestor {
		if request, err = ledger.SignAsRequestor(signer, request); err != nil {
			return requestStep{}, err
		}
		wallet.keepRequestBlock(request.BlockID)
		signed = append(signed, "requestor")
	}
	if name != "" && name == request.Bank {
		if request, err = ledger.SignAsBank(signer, request); err != nil {
			return requestStep{}, err
		}
		signed = append(signed, "bank")
	}
	if request.NewKey == wallet.PublicKey() {
		if request, err = ledger.SignAsNewKey(signer, request); err != nil {
			return requestStep{}, err
		}
		signed = append(signed, "new key")
	}
	if len(signed) == 0 {
		return requestStep{}, errors.New("this wallet holds no role in this request: its name is not the requestor or the bank, and its key is not the new key")
	}
	signedText, _ := json.Marshal(request)
	return requestStep{Facts: requestFacts(request), Signed: signed, Text: string(signedText)}, nil
}

// SubmitNameRequest posts a complete request to the node, which answers with its reason when it refuses
// it.
func (wallet *Wallet) SubmitNameRequest(text string) error {
	var request ledger.NameRequest
	if json.Unmarshal([]byte(text), &request) != nil {
		return errors.New("unreadable request: paste the whole request text")
	}
	if err := ledger.NameRequestShapeError(request); err != nil {
		return fmt.Errorf("the request is not complete: %w", err)
	}
	return wallet.postJSON("/hdls/name-request", request)
}

// requestFacts are what a request binds, one fact per line, and which of its signatures it carries.
func requestFacts(request ledger.NameRequest) []string {
	facts := []string{"kind: " + request.Kind, "name: " + request.Name}
	if request.Kind == ledger.RegistrationKind {
		facts = append(facts, "requestor, who pays the fee: "+request.Requestor)
	}
	if request.NewKey != "" {
		facts = append(facts, "new key: "+fingerprint(request.NewKey))
	}
	if request.Kind == ledger.BankChangeKind {
		facts = append(facts, "new bank: "+request.Bank)
	} else {
		facts = append(facts, "co-signing bank: "+request.Bank)
	}
	return append(facts, "fee: "+ledger.FormatAmount(request.Fee), fmt.Sprintf("block: %d", request.BlockID),
		fmt.Sprintf("signed by the requestor: %v, the bank: %v, the new key: %v",
			request.RequestorSignature != "", request.BankSignature != "", request.NewKeySignature != ""))
}

// pendingRow is a pending change on a bank's customer: the desk lists them, so the bank can tell its
// customers (spec 7.4).
type pendingRow struct {
	Name, Change string
	At           int64 // the block at whose end it takes effect
}

// customerChanges are the pending changes on the names whose bank of record is bank, a key change or a
// bank change away from it, which the bank's customers may not know of, and the bank changes to bank.
func customerChanges(accounts []ledger.Account, bank string) []pendingRow {
	var rows []pendingRow
	for _, account := range accounts {
		if bank != "" && account.PendingHeight != 0 && (account.Bank == bank || account.PendingBank == bank) {
			rows = append(rows, pendingRow{Name: account.Name, Change: pendingChange(account), At: account.PendingHeight})
		}
	}
	return rows
}
