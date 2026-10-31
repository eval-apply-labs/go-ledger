package node

import (
	"encoding/base64"
	"math/big"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// aliasOf returns a different base64 spelling of the same 32-byte key: the 43rd character carries
// 4 data bits + 2 trailing bits, which non-strict base64.StdEncoding (used by signing.VerifyBase64)
// ignores. So every Ed25519 key has four accepted string identities.
func aliasOf(t *testing.T, canonical string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := []byte(canonical)
	idx := 0
	for i := range alphabet {
		if alphabet[i] == b[42] {
			idx = i
		}
	}
	b[42] = alphabet[idx|1] // set one of the ignored trailing bits
	alias := string(b)
	raw1, _ := base64.StdEncoding.DecodeString(canonical)
	raw2, err := base64.StdEncoding.DecodeString(alias)
	if err != nil || alias == canonical || string(raw1) != string(raw2) {
		t.Fatalf("setup: alias construction failed (err=%v)", err)
	}
	return alias
}

// PoC (ledger-transition review, "duplicate-key accounts"): ledger identities (Account.PublicKey,
// ValidatorNode, Payment.PublicKeyFrom/To, Vote.Validator, Evidence.Validator) are compared as
// STRINGS, while signatures verify against the DECODED key. In dynamic mode, stake delegated under two
// spellings of one key yields two validator identities for that one key; it can then cast conflicting
// votes in the same (height, round, type) — one per spelling — and the VoteSet records NO equivocation
// and no Evidence can be built (Evidence.Valid requires one Validator string). The "two different
// signings ⇒ key burned" rule is enforced per spelling, not per key. FAILS on the current code.
func TestPoCKeyAliasEvadesEquivocationDetection(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	vSigner, _ := signing.Generate(scheme, network)
	V := vSigner.PublicKeyBase64()
	V2 := aliasOf(t, V)

	accounts := []ledger.Account{
		{PublicKey: "D1", Amount: big.NewRat(400, 1), ValidatorNode: V},  // delegated to one spelling
		{PublicKey: "D2", Amount: big.NewRat(400, 1), ValidatorNode: V2}, // and to the other
		{PublicKey: "H", Amount: big.NewRat(900, 1), ValidatorNode: "H"},
	}
	vals := deriveValidators(accounts, big.NewRat(100, 1))
	t.Logf("validator identities for ONE key: %v and %v (power %d + %d of %d)", vals.Contains(V), vals.Contains(V2), vals.Power(V), vals.Power(V2), vals.TotalPower())

	var dX, dY [32]byte
	dX[0], dY[0] = 0x11, 0x22
	vx, _ := consensus.SignVote(vSigner, consensus.Vote{Height: 9, Round: 0, Type: consensus.Prevote, BlockDigest: dX, Validator: V})
	vy, _ := consensus.SignVote(vSigner, consensus.Vote{Height: 9, Round: 0, Type: consensus.Prevote, BlockDigest: dY, Validator: V2})
	set := consensus.NewVoteSet(vals, 9, 0, consensus.Prevote)
	c1, err1 := set.Add(scheme, network, vx)
	c2, err2 := set.Add(scheme, network, vy)
	ev := consensus.Evidence{Validator: V, A: vx, B: vy}
	t.Logf("conflicting prevotes by one key: counted=%v/%v errs=%v/%v; evidence recorded=%d; hand-built evidence valid=%v",
		c1, c2, err1, err2, len(set.Evidence()), ev.Valid(scheme, network))
	if c1 && c2 && len(set.Evidence()) == 0 {
		t.Errorf("one key signed two conflicting prevotes for the same (height, round) and both were COUNTED with no equivocation evidence")
	}
}
