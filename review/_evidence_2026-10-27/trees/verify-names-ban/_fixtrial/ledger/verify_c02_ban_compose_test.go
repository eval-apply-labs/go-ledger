package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// C02 x C06: a proposer holding ONE over-balance payment the victim signed (refused at intake with
// 409, but the signature exists) includes it plus its own claim on the victim's name. ApplyBlock has
// no overdraw rejection, so every node accepts: balance destroyed AND name taken in one block.
func TestVerifyC02BanThenClaimSameBlock(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	victim, _ := signing.Generate(scheme, network)
	attacker, _ := signing.Generate(scheme, network)
	V, M := victim.PublicKeyBase64(), attacker.PublicKeyBase64()
	s := st([]Account{
		{PublicKey: V, Name: "alice", Amount: rat("10"), ValidatorNode: V},
		{PublicKey: M, Amount: rat("1"), ValidatorNode: M},
	}, rat("0.1"))
	over, _ := SignPayment(victim, Payment{PublicKeyFrom: V, PublicKeyTo: M, Amount: rat("12"), BlockID: 5})
	claim, _ := SignAccountUpdate(attacker, AccountUpdate{From: M, Name: "alice", BlockID: 5})
	s5, b := apply(t, scheme, network, 5, s, BlockContent{Payments: []Payment{over}, Updates: []AccountUpdate{claim}})
	t.Logf("carried payments=%d updates=%d; victim present=%v; owner(alice)=attacker:%v; attacker=%s",
		len(b.Payments), len(b.Updates), has(s5.Accounts, V), ownerOf(s5.Accounts, "alice") == M, balance(s5.Accounts, M).FloatString(2))
}
