package ledger

import (
	"math/big"
	"testing"

	"github.com/eval-apply-co-uk/go-ledger/consensus"
	"github.com/eval-apply-co-uk/go-ledger/signing"
)

func has(accounts []Account, key string) bool {
	for _, a := range accounts {
		if a.PublicKey == key {
			return true
		}
	}
	return false
}

// A block carrying valid equivocation evidence slashes the offender out of the state (confiscating
// stake, ejecting them from a dynamic set); a block that carries forged/invalid evidence is rejected
// — so an honest validator cannot be framed.
func TestSlashRemovesEquivocator(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	honest, _ := signing.Generate(scheme, network)
	cheat, _ := signing.Generate(scheme, network)
	A, B := honest.PublicKeyBase64(), cheat.PublicKeyBase64()
	prev := []Account{
		{PublicKey: A, Name: "a", Amount: big.NewRat(1000, 1), ValidatorNode: A},
		{PublicKey: B, Name: "b", Amount: big.NewRat(500, 1), ValidatorNode: B},
	}
	cost := big.NewRat(1, 10)

	var dX, dY [32]byte
	dX[0], dY[0] = 0x11, 0x22
	vX, _ := consensus.SignVote(cheat, consensus.Vote{Height: 3, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: B})
	vY, _ := consensus.SignVote(cheat, consensus.Vote{Height: 3, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: B})
	ev := consensus.Evidence{Validator: B, A: vX, B: vY}

	blk := ProposeBlock(scheme, network, 5, st(prev, cost), BlockContent{Evidence: []consensus.Evidence{ev}})
	next, ok := ApplyBlock(scheme, network, st(prev, cost), blk)
	if !ok {
		t.Fatal("a block carrying valid equivocation evidence should apply")
	}
	if has(next.Accounts, B) {
		t.Fatal("the equivocator B should be slashed out of the state")
	}
	if !has(next.Accounts, A) {
		t.Fatal("the honest validator A should remain")
	}

	// Framing attempt: evidence that claims A equivocated but is signed by B → invalid → rejected.
	frame := consensus.Evidence{Validator: A, A: vX, B: vY}
	tampered := blk
	tampered.Evidence = []consensus.Evidence{frame}
	if _, ok := ApplyBlock(scheme, network, st(prev, cost), tampered); ok {
		t.Fatal("ApplyBlock must reject a block whose evidence does not verify (no framing honest validators)")
	}
}

// Evidence is only actionable within maxEvidenceAge blocks of the equivocation, so old evidence
// cannot be re-included later to confiscate a same-key account recreated in the meantime — and
// future-dated evidence (a vote height beyond the block) is never actionable.
func TestEvidenceAgeWindow(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	honest, _ := signing.Generate(scheme, network)
	cheat, _ := signing.Generate(scheme, network)
	A, B := honest.PublicKeyBase64(), cheat.PublicKeyBase64()
	prev := []Account{
		{PublicKey: A, Name: "a", Amount: big.NewRat(1000, 1), ValidatorNode: A},
		{PublicKey: B, Name: "b", Amount: big.NewRat(500, 1), ValidatorNode: B},
	}
	cost := big.NewRat(1, 10)

	var dX, dY [32]byte
	dX[0], dY[0] = 0x11, 0x22
	const equivocationHeight = 3
	vX, _ := consensus.SignVote(cheat, consensus.Vote{Height: equivocationHeight, Round: 0, Type: consensus.Precommit, BlockDigest: dX, Validator: B})
	vY, _ := consensus.SignVote(cheat, consensus.Vote{Height: equivocationHeight, Round: 0, Type: consensus.Precommit, BlockDigest: dY, Validator: B})
	ev := consensus.Evidence{Validator: B, A: vX, B: vY}
	content := BlockContent{Evidence: []consensus.Evidence{ev}}

	// At the window's edge the evidence still slashes.
	edge := int64(equivocationHeight + maxEvidenceAge)
	blk := ProposeBlock(scheme, network, edge, st(prev, cost), content)
	if len(blk.Evidence) != 1 {
		t.Fatal("evidence at the window edge should still be carried")
	}
	next, ok := ApplyBlock(scheme, network, st(prev, cost), blk)
	if !ok || has(next.Accounts, B) {
		t.Fatal("evidence at the window edge should apply and slash B")
	}

	// One block past the window: ProposeBlock drops it, and a hand-crafted block carrying it is rejected.
	stale := ProposeBlock(scheme, network, edge+1, st(prev, cost), content)
	if len(stale.Evidence) != 0 {
		t.Fatal("ProposeBlock must drop evidence older than maxEvidenceAge")
	}
	tampered := stale
	tampered.Evidence = []consensus.Evidence{ev}
	if _, ok := ApplyBlock(scheme, network, st(prev, cost), tampered); ok {
		t.Fatal("ApplyBlock must reject stale re-included evidence")
	}

	// Future-dated evidence (vote height beyond the block) is equally non-actionable.
	early := ProposeBlock(scheme, network, equivocationHeight-1, st(prev, cost), content)
	if len(early.Evidence) != 0 {
		t.Fatal("ProposeBlock must drop evidence dated after the block")
	}
	tampered = early
	tampered.Evidence = []consensus.Evidence{ev}
	if _, ok := ApplyBlock(scheme, network, st(prev, cost), tampered); ok {
		t.Fatal("ApplyBlock must reject future-dated evidence")
	}
}
