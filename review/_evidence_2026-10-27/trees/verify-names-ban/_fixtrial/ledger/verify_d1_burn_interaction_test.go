package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Under D1 (trial), a slashed NAMED validator persists at zero with its name. Does pay-by-name traffic
// keep flowing to the burned key, and can the burned key spend it?
func TestVerifyD1SlashedNameStillReceivesAndSpends(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	val, _ := signing.Generate(scheme, network)
	payer, _ := signing.Generate(scheme, network)
	V, P := val.PublicKeyBase64(), payer.PublicKeyBase64()
	s0 := st([]Account{
		{PublicKey: V, Name: "bakery", Amount: rat("100"), ValidatorNode: V},
		{PublicKey: P, Amount: rat("500"), ValidatorNode: P},
	}, rat("0.1"))
	var dX, dY [32]byte
	dX[0], dY[0] = 1, 2
	vX, _ := consensus.SignVote(val, consensus.Vote{Height: 4, Round: 0, Type: consensus.Prevote, BlockDigest: dX, Validator: V})
	vY, _ := consensus.SignVote(val, consensus.Vote{Height: 4, Round: 0, Type: consensus.Prevote, BlockDigest: dY, Validator: V})
	s1, _ := apply(t, scheme, network, 5, s0, BlockContent{Evidence: []consensus.Evidence{{Validator: V, A: vX, B: vY}}})
	pay, _ := SignPayment(payer, Payment{PublicKeyFrom: P, ToName: "bakery", Amount: rat("50"), BlockID: 6})
	s2, _ := apply(t, scheme, network, 6, s1, BlockContent{Payments: []Payment{pay}})
	out, _ := SignPayment(val, Payment{PublicKeyFrom: V, PublicKeyTo: P, Amount: rat("40"), BlockID: 7})
	s3, _ := apply(t, scheme, network, 7, s2, BlockContent{Payments: []Payment{out}})
	t.Logf("after slash: V=%s nameKept=%v | after pay-by-name: V=%s | after burned key spends 40: V=%s",
		balance(s1.Accounts, V).FloatString(2), nameRegistry(s1.Accounts)["bakery"] == V,
		balance(s2.Accounts, V).FloatString(2), balance(s3.Accounts, V).FloatString(2))
}
