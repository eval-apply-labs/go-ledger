package node

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// C06 at node level. 4 validators of equal power: 3 run unmodified node code over HTTP; the 4th (M,
// the victim's reference node) is played by the test as a Byzantine validator. The victim (balance 10)
// mistypes 100: her wallet signs it (no local check) and posts it to M; honest-code intake would answer
// 409 (shown at an honest node) but M keeps the signature. M steered the BlockID (it reports the height
// the wallet adds its lookahead to) onto a height where M is the round-0 proposer, and proposes a block
// carrying just that one payment. The 3 honest nodes validate it with ApplyBlock, vote, and commit.
// mode "single": one over-balance payment. mode "pair": two individually-within-balance payments for the
// same block (the documented trigger) — the case the round-1 fix proposal would still ban.
func runC06(t *testing.T, mode string) {
	const n = 4
	signers := make([]*signing.Signer, n)
	vals := make([]consensus.Validator, n)
	for i := range signers {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers[i] = s
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	M := signers[3] // Byzantine reference node, not started
	victim, _ := signing.Generate(signing.Ed25519{}, "testnet")
	payee, _ := signing.Generate(signing.Ed25519{}, "testnet")
	V, P := victim.PublicKeyBase64(), payee.PublicKeyBase64()
	genesis := []ledger.Account{
		{PublicKey: V, Name: "victim", Amount: rat("10"), ValidatorNode: V},
		{PublicKey: P, Amount: rat("5"), ValidatorNode: P},
	}
	nodes := make([]*Node, 3)
	servers := make([]*httptest.Server, 3)
	transports := make([]*HTTPTransport, 3)
	for i := 0; i < 3; i++ {
		transports[i] = NewHTTPTransport(nil)
		nodes[i] = New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: time.Second, MinBlockInterval: 300 * time.Millisecond, Genesis: genesis,
			MaxMempool: 10000, MaxCommentLength: 280, MaxMessageBytes: 2000}, transports[i])
		servers[i] = httptest.NewServer(NewServer(nodes[i]).Handler())
	}
	for i := 0; i < 3; i++ {
		var peers []string
		for j := 0; j < 3; j++ {
			if j != i {
				peers = append(peers, servers[j].URL)
			}
		}
		transports[i].SetPeers(peers)
	}
	for _, nd := range nodes {
		nd.Start()
	}
	t.Cleanup(func() {
		for _, nd := range nodes {
			nd.Stop()
		}
		for _, s := range servers {
			s.Close()
		}
	})
	waitFor(t, 20*time.Second, func() bool { return nodes[0].Snapshot().Height >= 2 }, "live")

	// The height M will steer the wallet's BlockID to: the next one where M is the round-0 proposer.
	h := nodes[0].Snapshot().Height + 4
	for vs.ProposerAt(h, 0) != M.PublicKeyBase64() {
		h++
	}
	var payments []ledger.Payment
	if mode == "single" {
		p, _ := ledger.SignPayment(victim, ledger.Payment{PublicKeyFrom: V, PublicKeyTo: P, Amount: rat("100"), BlockID: h})
		payments = append(payments, p)
	} else {
		a, _ := ledger.SignPayment(victim, ledger.Payment{PublicKeyFrom: V, PublicKeyTo: P, Amount: rat("6"), BlockID: h, Comment: "a"})
		b, _ := ledger.SignPayment(victim, ledger.Payment{PublicKeyFrom: V, PublicKeyTo: P, Amount: rat("6"), BlockID: h, Comment: "b"})
		payments = append(payments, a, b)
	}
	// Honest intake refuses the over-balance (or second) payment with 409, and never relays it.
	codes := []int{}
	for _, p := range payments {
		codes = append(codes, postJSON(t, servers[1].URL+"/hdls/pay", p))
	}
	t.Logf("honest-node intake of the victim's payment(s) for block %d: HTTP %v", h, codes)

	waitFor(t, 60*time.Second, func() bool {
		for _, nd := range nodes {
			if nd.Snapshot().Height != h {
				return false
			}
		}
		return true
	}, "all honest nodes at the steered height")
	st := nodes[0].Snapshot()
	prev := ledger.LedgerState{Accounts: st.Accounts, Parameters: st.Parameters, GovernanceSequence: st.GovernanceSequence}
	// Byzantine M bypasses ProposeBlock's filter: it hand-builds the block with the old semantics.
	nextAccounts := ledger.ProcessPayments(prev.Accounts, payments, prev.Parameters.TransactionCost)
	blk := ledger.Block{Height: h, PrevStateDigest: prev.Digest(), Payments: payments,
		StateDigest: ledger.LedgerState{Accounts: nextAccounts, Parameters: prev.Parameters, GovernanceSequence: prev.GovernanceSequence}.Digest()}
	_, applies := ledger.ApplyBlock(signing.Ed25519{}, "testnet", prev, blk, vs)
	t.Logf("FIXED COPY: M's hand-built block applies under the fixed ApplyBlock: %v", applies)
	seenAt := time.Now()
	prop := ProposalMsg{Height: h, Round: 0, ValidRound: -1, Block: blk}
	if err := SignProposal(M, &prop); err != nil {
		t.Fatal(err)
	}
	// M re-gossips its proposal (as reGossipProposal does) so it lands once each node's driver for h
	// has started (a proposal arriving in the post-commit interval is judged against the old driver).
	for _, delay := range []time.Duration{0, 350, 500, 650, 800} {
		time.Sleep(delay * time.Millisecond)
		for _, s := range servers {
			if code := postJSON(t, s.URL+"/hdls/proposal", prop); code != http.StatusAccepted {
				t.Fatalf("proposal post: %d", code)
			}
		}
	}
	t.Logf("proposal re-sent until %v after all nodes reached height %d", time.Since(seenAt).Round(time.Millisecond), h)
	waitFor(t, 30*time.Second, func() bool { return nodes[0].Snapshot().Height > h }, "height commits")
	snapAfter := nodes[0].Snapshot()
	dec := snapAfter.Decided
	var round int32 = -1
	if len(snapAfter.LastCommit.Votes) > 0 {
		round = snapAfter.LastCommit.Votes[0].Round
	}
	t.Logf("decided digest at %d == M's block digest: %v (commit round %d)", h, dec == blk.StateDigest, round)
	after := nodes[2].Snapshot().Accounts
	present := false
	for _, a := range after {
		if a.PublicKey == V {
			present = true
		}
	}
	t.Logf("mode=%s: M's block for height %d carried %d payment(s); committed by the 3 honest nodes; victim (balance 10, name 'victim') present after=%v, balance=%s",
		mode, h, len(blk.Payments), present, ledger.AccountAmount(after, V).FloatString(2))
	if !present {
		t.Errorf("mode=%s: the victim's whole balance (10) was destroyed by a block proposed by one Byzantine validator", mode)
	}
}

func TestVerifyC06SingleOverBalanceViaByzantineReferenceNode(t *testing.T) { runC06(t, "single") }
func TestVerifyC06SameBlockPairViaByzantineReferenceNode(t *testing.T)     { runC06(t, "pair") }
