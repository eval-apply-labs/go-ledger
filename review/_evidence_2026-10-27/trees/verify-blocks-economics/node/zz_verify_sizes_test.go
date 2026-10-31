package node

import (
	"encoding/json"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Sizes of the non-payment/update parts of a proposal, to bound a safe per-block content budget.
func TestVerifySizesProposalOverhead(t *testing.T) {
	var d [32]byte
	for i := range d {
		d[i] = 255 // worst-case JSON for a [32]byte (3 digits each)
	}
	s, _ := signing.Generate(signing.Ed25519{}, "testnet")
	v, _ := consensus.SignVote(s, consensus.Vote{Height: 1 << 40, Round: 255, Type: consensus.Prevote, BlockDigest: d, Validator: s.PublicKeyBase64()})
	ev := consensus.Evidence{Validator: s.PublicKeyBase64(), A: v, B: v}
	evb, _ := json.Marshal(ev)
	empty := ProposalMsg{Height: 1 << 40, Round: 255, ValidRound: 254, Block: ledger.Block{Height: 1 << 40, PrevStateDigest: d, StateDigest: d}}
	_ = SignProposal(s, &empty)
	eb, _ := json.Marshal(empty)
	for _, n := range []int{8, 100} {
		var pv []ledger.ParameterVote
		var rv []ledger.ReassignmentVote
		for i := 0; i < n; i++ {
			x, _ := ledger.SignParameterVote(s, ledger.LedgerParameters{TransactionCost: rat("1/10")}, d, 1<<40)
			pv = append(pv, x)
			y, _ := ledger.SignReassignmentVote(s, "k", rat("12345678901234567890"), rat("1"), 1<<40)
			rv = append(rv, y)
		}
		pc := ledger.ParameterChange{NewParameters: ledger.LedgerParameters{TransactionCost: rat("1/10"), BlockIntervalMs: 1 << 40, StepTimeoutMs: 1 << 40, MaxPeers: 1 << 30, MaxMessageBytes: 1 << 30}, PreviousParametersDigest: d, Sequence: 1 << 40, Votes: pv}
		ra := ledger.Reassignment{Account: s.PublicKeyBase64(), PreviousAmount: rat("12345678901234567890/7"), NewAmount: rat("12345678901234567890"), Sequence: 1 << 40, Votes: rv}
		pcb, _ := json.Marshal(pc)
		rab, _ := json.Marshal(ra)
		t.Logf("N=%3d validators: empty signed proposal=%d B, one Evidence=%d B (x N = %d B), ParameterChange=%d B, Reassignment=%d B; worst non-content total ~ %d B",
			n, len(eb), len(evb), n*len(evb), len(pcb), len(rab), len(eb)+n*len(evb)+len(pcb))
	}
}
