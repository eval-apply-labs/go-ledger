package ledger

import (
	"math/big"
	"strconv"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

// AccountAmount is an account's current balance in `accounts`, or zero if it has no entry. Exported
// so a node can check whether a reassignment still amends the balance its voters agreed on.
func AccountAmount(accounts []Account, publicKey string) *big.Rat {
	for _, account := range accounts {
		if account.PublicKey == publicKey {
			if account.Amount != nil {
				return account.Amount
			}
			return new(big.Rat)
		}
	}
	return new(big.Rat)
}

// ReassignmentVote is one validator's signed approval of a proposed balance reassignment (its
// signature is over the reassignment's shared (Account, PreviousAmount, NewAmount, Sequence)).
type ReassignmentVote struct {
	Validator string // the validator's public key
	Signature string // over the (Account, PreviousAmount, NewAmount, Sequence) preimage, domain "reassignment-vote"
}

// Reassignment is a > ⅔-ratified override of one account's balance — the community's deliberate
// exception to payment-conservation (money normally only moves via a signed payment, or burns as a
// fee). It sets `Account`'s balance to `NewAmount` (which may be higher — minting — or lower). It is
// bound to `PreviousAmount` (the balance it amends, so voters agree on the starting point) AND to the
// committed governance Sequence: applying it bumps the sequence, so the certificate is single-use and
// cannot be replayed even after the balance cycles back to PreviousAmount (e.g. a newcomer grant of
// prev=0 → X, once the grantee spends back to zero). NewAmount must be non-negative. The bundle of
// validator signatures IS the proof, verified the same way by every node.
type Reassignment struct {
	Account        string   // the account whose balance is set (its public key)
	PreviousAmount *big.Rat // the balance it must currently hold (what voters agreed on)
	NewAmount      *big.Rat // the balance to set it to (⅔-ratified; may mint or reduce; non-negative)
	Sequence       int64    // the governance sequence this reassignment amends (single-use guard)
	Votes          []ReassignmentVote
}

func reassignmentVoteFields(account string, previousAmount, newAmount *big.Rat, sequence int64) [][]byte {
	var previous, next string
	if previousAmount != nil {
		previous = previousAmount.RatString()
	}
	if newAmount != nil {
		next = newAmount.RatString()
	}
	return [][]byte{[]byte(account), []byte(previous), []byte(next), []byte(strconv.FormatInt(sequence, 10))}
}

// SignReassignmentVote is a validator's approval of setting `account`'s balance from `previousAmount`
// to `newAmount`, at governance `sequence`.
func SignReassignmentVote(signer *signing.Signer, account string, previousAmount, newAmount *big.Rat, sequence int64) (ReassignmentVote, error) {
	signature, err := signer.SignBase64(signing.DomainReassignmentVote, reassignmentVoteFields(account, previousAmount, newAmount, sequence)...)
	if err != nil {
		return ReassignmentVote{}, err
	}
	return ReassignmentVote{Validator: signer.PublicKeyBase64(), Signature: signature}, nil
}

// VerifyReassignmentVote reports whether `vote` is a valid signature by its validator approving
// (account, previousAmount, newAmount, sequence) — used to filter votes as they are gossiped.
func VerifyReassignmentVote(scheme signing.Scheme, networkID, account string, previousAmount, newAmount *big.Rat, sequence int64, vote ReassignmentVote) bool {
	return signing.VerifyBase64(scheme, networkID, vote.Validator, signing.DomainReassignmentVote, vote.Signature, reassignmentVoteFields(account, previousAmount, newAmount, sequence)...)
}

// Valid reports whether the reassignment is ratified against `currentAccounts` at `currentSequence`:
// the new balance is non-negative, it amends this sequence (single-use replay guard), the account
// still holds exactly PreviousAmount (what voters agreed on), and every vote is a distinct current
// validator signing exactly these amounts, with combined power meeting the > ⅔ quorum. A nil
// validator set or nil amount is never valid.
func (reassignment Reassignment) Valid(scheme signing.Scheme, networkID string, validators *consensus.ValidatorSet, currentAccounts []Account, currentSequence int64) bool {
	if validators == nil || reassignment.PreviousAmount == nil || reassignment.NewAmount == nil {
		return false
	}
	if reassignment.Sequence != currentSequence {
		return false // single-use: bound to a governance sequence, and applying bumps it
	}
	if reassignment.NewAmount.Sign() < 0 {
		return false // the non-negative guard (an account cannot be driven below zero)
	}
	if AccountAmount(currentAccounts, reassignment.Account).Cmp(reassignment.PreviousAmount) != 0 {
		return false // no longer amends the balance the voters signed off on
	}
	fields := reassignmentVoteFields(reassignment.Account, reassignment.PreviousAmount, reassignment.NewAmount, reassignment.Sequence)
	seen := make(map[string]bool)
	var approvingPower int64
	for _, vote := range reassignment.Votes {
		if !validators.Contains(vote.Validator) || seen[vote.Validator] {
			return false // unknown validator, or one counted twice
		}
		if !signing.VerifyBase64(scheme, networkID, vote.Validator, signing.DomainReassignmentVote, vote.Signature, fields...) {
			return false
		}
		seen[vote.Validator] = true
		approvingPower += validators.Power(vote.Validator)
	}
	return approvingPower >= validators.Quorum()
}

// applyReassignment sets `account`'s balance to `newAmount`, creating the account if it does not yet
// exist (so a ratified reassignment can also fund a newcomer).
func applyReassignment(accounts []Account, account string, newAmount *big.Rat) []Account {
	result := make([]Account, len(accounts))
	copy(result, accounts) // copy-on-write: never mutate the caller's (previous-state) accounts
	for index := range result {
		if result[index].PublicKey == account {
			result[index].Amount = new(big.Rat).Set(newAmount)
			return result
		}
	}
	return append(result, Account{PublicKey: account, Amount: new(big.Rat).Set(newAmount)})
}
