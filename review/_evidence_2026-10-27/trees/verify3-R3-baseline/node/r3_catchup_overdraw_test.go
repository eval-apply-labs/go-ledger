package node

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// recordingTransport captures what a partitioned node broadcasts (nothing is delivered).
type recordingTransport struct {
	mu   sync.Mutex
	msgs []Message
}

func (r *recordingTransport) Broadcast(from string, msg Message) {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
}

func (r *recordingTransport) proposalAt(h int64) *ProposalMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.msgs {
		if m.Type == MsgProposal && m.Proposal != nil && m.Proposal.Height == h {
			p := *m.Proposal
			return &p
		}
	}
	return nil
}

// Regression for NEW-2, driven only through production paths: client intake (AdmitPayment), real
// consensus among the connected nodes, and the partitioned node's OWN catchUpLoop polling a peer's
// /hdls/checkpoint over HTTP (bestCheckpoint -> evCatchUp -> catchUp -> fastForward -> startHeight).
// Desired invariant (security.md): an honest node never proposes a payment set that overdraws a payer.
func TestR3CatchUpLoopMustNotProposeOverdraw(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	const target int64 = 3
	signers := make([]*signing.Signer, 4)
	vals := make([]consensus.Validator, 4)
	for i := range signers {
		signers[i], _ = signing.Generate(scheme, network)
		vals[i] = consensus.Validator{PublicKey: signers[i].PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	aIdx := -1
	for i, s := range signers {
		if vs.ProposerAt(target, 0) == s.PublicKeyBase64() {
			aIdx = i
		}
	}
	var others []int
	for i := range signers {
		if i != aIdx {
			others = append(others, i)
		}
	}

	payer, _ := signing.Generate(scheme, network)
	P := payer.PublicKeyBase64()
	genesis := []ledger.Account{{PublicKey: P, Name: "payer", Amount: rat("100"), ValidatorNode: P}}
	mk := func(i int, tr Transport, bootstrap []string) *Node {
		return New(Config{NetworkID: network, Scheme: scheme, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: 300 * time.Millisecond, MinBlockInterval: 500 * time.Millisecond,
			Genesis: genesis, Bootstrap: bootstrap}, tr)
	}

	// The three connected validators (quorum 3 of 4) on a full-mesh bus.
	b := newBus()
	connected := map[int]*Node{}
	for _, i := range others {
		connected[i] = mk(i, b, nil)
		b.register(signers[i].PublicKeyBase64(), connected[i])
	}
	peer := connected[others[0]]
	srv := httptest.NewServer(NewServer(peer).Handler())
	defer srv.Close()

	// Node A: partitioned from gossip (nothing it relays arrives anywhere), but it can still reach one
	// peer's HTTP API, which is its catch-up source.
	rec := &recordingTransport{}
	a := mk(aIdx, rec, []string{srv.URL})
	a.Start()
	defer a.Stop()

	for _, nd := range connected {
		nd.Start()
	}
	stopped := map[int]bool{}
	defer func() {
		for i, nd := range connected {
			if !stopped[i] {
				nd.Stop()
			}
		}
	}()

	// The payer's two 60-unit payments, each accepted by a different reference node: X1 by A while
	// partitioned (its relay is lost), X2 by a connected node that never saw X1. Both intakes are honest.
	x1, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: "shop", Amount: rat("60"), BlockID: target})
	x2, _ := ledger.SignPayment(payer, ledger.Payment{PublicKeyFrom: P, PublicKeyTo: "cafe", Amount: rat("60"), BlockID: target - 1})
	if err := a.AdmitPayment(x1); err != nil {
		t.Fatalf("premise: A admits X1: %v", err)
	}
	if err := peer.AdmitPayment(x2); err != nil {
		t.Fatalf("premise: peer admits X2: %v", err)
	}

	// Let the connected nodes commit up to target-1, then halt two of them so the network sits at
	// `target` while A catches up (models a slow round, keeps the catch-up height deterministic).
	waitFor(t, 20*time.Second, func() bool {
		for _, nd := range connected {
			if nd.Snapshot().Height < target {
				return false
			}
		}
		return true
	}, "connected nodes did not reach the target height")
	for _, i := range others[1:] {
		connected[i].Stop()
		stopped[i] = true
	}
	ps := peer.Snapshot()
	if ps.Height != target {
		t.Skipf("network overshot target (peer at %d); rerun", ps.Height)
	}
	if got := ledger.AccountAmount(ps.Accounts, P); got.Cmp(rat("40")) != 0 {
		t.Fatalf("premise: X2 committed, payer holds 40; has %s", got.RatString())
	}

	// A's own catchUpLoop (3 s ticker) adopts the peer's verified checkpoint and starts `target`.
	waitFor(t, 20*time.Second, func() bool { return a.Snapshot().Height == target && rec.proposalAt(target) != nil },
		"A did not catch up and propose at the target height")
	prop := rec.proposalAt(target)
	committed := ledger.LedgerState{Accounts: ps.Accounts, Parameters: ps.Parameters, GovernanceSequence: ps.GovernanceSequence}
	next, valid := ledger.ApplyBlock(scheme, network, committed, prop.Block, vs)
	present := false
	for _, acc := range next.Accounts {
		if acc.PublicKey == P {
			present = true
		}
	}
	t.Logf("A's height-%d proposal: %d payment(s); valid for every honest peer=%v; payer after=%s present=%v",
		target, len(prop.Block.Payments), valid, ledger.AccountAmount(next.Accounts, P).RatString(), present)
	if valid && !present {
		t.Errorf("after the real catch-up path, honest node A proposed the now-overdrawing X1: payer banned, remaining 40 destroyed")
	}
}
