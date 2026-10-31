package node

// Third-pass verifier PoC for VGL-N2 (from scratch): a reassignment-created account is bare (no
// self-delegation), unlike a payment-created one; and a same-block drain + reassignment recreates the
// target without its name/delegation. Real node paths: SubmitReassignmentVote / AdmitPayment /
// SubmitUpdate, the proposer's ProposeBlock and ApplyBlock, dynamic (stake-derived) validators.

import (
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

func v3Validators(nd *Node) map[string]bool {
	out := map[string]bool{}
	for _, v := range nd.GovernanceView().Validators {
		out[v] = true
	}
	return out
}

func v3Account(nd *Node, key string) (ledger.Account, bool) {
	for _, a := range nd.Snapshot().Accounts {
		if a.PublicKey == key {
			return a, true
		}
	}
	return ledger.Account{}, false
}

func TestV3VGLN2GrantCreatesBareAccount(t *testing.T) {
	founder, _ := signing.Generate(signing.Ed25519{}, "testnet")
	newBankGrant, _ := signing.Generate(signing.Ed25519{}, "testnet")
	newBankPaid, _ := signing.Generate(signing.Ed25519{}, "testnet")
	F, N, M := founder.PublicKeyBase64(), newBankGrant.PublicKeyBase64(), newBankPaid.PublicKeyBase64()
	nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: founder, ValidatorThreshold: rat("100"),
		TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 20 * time.Millisecond,
		Genesis: []ledger.Account{{PublicKey: F, Name: "founder", Amount: rat("10000"), ValidatorNode: F}}}, NewHTTPTransport(nil))
	nd.Start()
	defer nd.Stop()
	waitFor(t, 5*time.Second, func() bool { return nd.Snapshot().Height >= 2 }, "no start")

	// Newcomer bank N funded by a ⅔ grant (0 -> 200); newcomer bank M funded by a payment of 200.
	seq := nd.Snapshot().GovernanceSequence
	vote, _ := ledger.SignReassignmentVote(founder, N, rat("0"), rat("200"), seq)
	nd.SubmitReassignmentVote(ledger.Reassignment{Account: N, PreviousAmount: rat("0"), NewAmount: rat("200"), Sequence: seq, Votes: []ledger.ReassignmentVote{vote}})
	h := nd.Snapshot().Height
	pay, _ := ledger.SignPayment(founder, ledger.Payment{PublicKeyFrom: F, PublicKeyTo: M, Amount: rat("200.1"), BlockID: h})
	if err := nd.AdmitPayment(pay); err != nil {
		t.Fatalf("pay: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		_, n := v3Account(nd, N)
		_, m := v3Account(nd, M)
		return n && m
	}, "accounts not created")
	waitFor(t, 2*time.Second, func() bool { return nd.Snapshot().Height >= h+3 }, "stall")
	an, _ := v3Account(nd, N)
	am, _ := v3Account(nd, M)
	vals := v3Validators(nd)
	t.Logf("grant-created N: amount=%s validatorNode=%q isValidator=%v", an.Amount.RatString(), an.ValidatorNode, vals[N])
	t.Logf("payment-created M: amount=%s validatorNode==M %v isValidator=%v", am.Amount.RatString(), am.ValidatorNode == M, vals[M])

	// One free signed update fixes N.
	up, _ := ledger.SignAccountUpdate(newBankGrant, ledger.AccountUpdate{From: N, Delegated: N, BlockID: nd.Snapshot().Height})
	nd.SubmitUpdate(up)
	waitFor(t, 5*time.Second, func() bool { return v3Validators(nd)[N] }, "self-delegation did not admit N")
	t.Logf("after one self-delegating AccountUpdate: N isValidator=%v", v3Validators(nd)[N])
	if an.ValidatorNode == "" && am.ValidatorNode == M {
		t.Errorf("VGL-N2 CONFIRMED (consistency): a grant-created account is not self-delegated, a payment-created one is")
	}
}

// Drain + reassignment in the same block (the C12 path): the target comes back nameless and undelegated.
func TestV3VGLN2SameBlockDrainStripsNameAtLedger(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	var vals []consensus.Validator
	var banks []*signing.Signer
	for i := 0; i < 4; i++ {
		s, _ := signing.Generate(scheme, network)
		banks = append(banks, s)
		vals = append(vals, consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1})
	}
	set := consensus.NewValidatorSet(vals)
	thief, _ := signing.Generate(scheme, network)
	T := thief.PublicKeyBase64()
	prevState := ledger.LedgerState{Accounts: []ledger.Account{{PublicKey: T, Name: "thief", Description: "d", Address: "http://t", Amount: rat("1000"), ValidatorNode: T}},
		Parameters: ledger.LedgerParameters{TransactionCost: rat("0.1")}}
	cert := ledger.Reassignment{Account: T, PreviousAmount: rat("1000"), NewAmount: rat("500")}
	for _, b := range banks[:3] {
		v, _ := ledger.SignReassignmentVote(b, T, rat("1000"), rat("500"), 0)
		cert.Votes = append(cert.Votes, v)
	}
	pay, _ := ledger.SignPayment(thief, ledger.Payment{PublicKeyFrom: T, PublicKeyTo: "stash", Amount: rat("1000"), BlockID: 7})
	blk := ledger.ProposeBlock(scheme, network, 7, prevState, ledger.BlockContent{Payments: []ledger.Payment{pay}, Reassignment: &cert}, set)
	next, ok := ledger.ApplyBlock(scheme, network, prevState, blk, set)
	if !ok {
		t.Fatal("block rejected")
	}
	for _, a := range next.Accounts {
		if a.PublicKey == T {
			t.Logf("after drain+reassignment: T amount=%s name=%q validatorNode=%q description=%q address=%q", a.Amount.RatString(), a.Name, a.ValidatorNode, a.Description, a.Address)
		}
	}
	// Same seizure WITHOUT the drain keeps the attributes (applyReassignment's update-in-place branch).
	blk2 := ledger.ProposeBlock(scheme, network, 7, prevState, ledger.BlockContent{Reassignment: &cert}, set)
	next2, _ := ledger.ApplyBlock(scheme, network, prevState, blk2, set)
	for _, a := range next2.Accounts {
		if a.PublicKey == T {
			t.Logf("reassignment alone: T amount=%s name=%q validatorNode==T %v", a.Amount.RatString(), a.Name, a.ValidatorNode == T)
		}
	}
	// Same drain WITHOUT any reassignment: the name is released anyway (C02 behaviour).
	blk3 := ledger.ProposeBlock(scheme, network, 7, prevState, ledger.BlockContent{Payments: []ledger.Payment{pay}}, set)
	next3, _ := ledger.ApplyBlock(scheme, network, prevState, blk3, set)
	_, exists := func() (ledger.Account, bool) {
		for _, a := range next3.Accounts {
			if a.PublicKey == T {
				return a, true
			}
		}
		return ledger.Account{}, false
	}()
	t.Logf("drain alone: T account exists=%v (name released by deletion, C02)", exists)
}
