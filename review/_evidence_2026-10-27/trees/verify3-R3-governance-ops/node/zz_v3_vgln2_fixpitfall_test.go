package node

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// What the suggested VGL-N2 fix (ValidatorNode = account on creation) would do to the documented
// treasury pattern: a ⅔ mint into a FRESH treasury key, in dynamic mode (threshold 100).
func TestV3VGLN2SuggestedFixTreasuryPitfall(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	var accounts []ledger.Account
	var banks []*signing.Signer
	for i := 0; i < 4; i++ {
		s, _ := signing.Generate(scheme, network)
		banks = append(banks, s)
		accounts = append(accounts, ledger.Account{PublicKey: s.PublicKeyBase64(), Amount: rat("1000"), ValidatorNode: s.PublicKeyBase64()})
	}
	set := deriveValidators(accounts, rat("100"))
	treasury := "fresh-treasury-key"
	cert := ledger.Reassignment{Account: treasury, PreviousAmount: rat("0"), NewAmount: rat("100000")}
	for _, b := range banks[:3] {
		v, _ := ledger.SignReassignmentVote(b, treasury, rat("0"), rat("100000"), 0)
		cert.Votes = append(cert.Votes, v)
	}
	prev := ledger.LedgerState{Accounts: accounts, Parameters: ledger.LedgerParameters{TransactionCost: rat("0.1")}}
	blk := ledger.ProposeBlock(scheme, network, 5, prev, ledger.BlockContent{Reassignment: &cert}, set)
	next, ok := ledger.ApplyBlock(scheme, network, prev, blk, set)
	if !ok {
		t.Fatal("rejected")
	}
	today := deriveValidators(next.Accounts, rat("100"))
	fixed := append([]ledger.Account(nil), next.Accounts...)
	for i := range fixed {
		if fixed[i].PublicKey == treasury {
			fixed[i].ValidatorNode = treasury // what the suggested one-liner would produce
		}
	}
	after := deriveValidators(fixed, rat("100"))
	show := func(label string, vs *consensus.ValidatorSet) {
		t.Logf("%s: total power %d, quorum %d, treasury power %d (alone >= quorum: %v)", label, vs.TotalPower(), vs.Quorum(), vs.Power(treasury), vs.Power(treasury) >= vs.Quorum())
	}
	show("today (grant-created account undelegated)", today)
	show("with suggested fix (self-delegated)", after)
}
