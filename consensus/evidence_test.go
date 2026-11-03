package consensus

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// The driver must surface a transferable, verifiable equivocation proof when a validator double-signs
// — which is what a node carries into a block to freeze the offender's name.
func TestDriverSurfacesEquivocationEvidence(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	me := signers[0]
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, 1, digest(0x01))
	driver.Start()

	cheat := signers[1]
	driver.ReceiveVote(mustVote(t, cheat, 1, 0, Prevote, digest(0x11)))
	driver.ReceiveVote(mustVote(t, cheat, 1, 0, Prevote, digest(0x22))) // conflicting second vote

	allEvidence := driver.Evidence()
	found := false
	for _, evidence := range allEvidence {
		if evidence.Name == nameOf(cheat) && evidence.Valid(scheme, network, cheat.PublicKeyBase64()) {
			found = true
		}
	}
	if !found {
		t.Fatal("driver should surface valid equivocation evidence against the double-signer")
	}
}

// Two signed proposals of one proposer for one height and round are evidence against it only when their
// block hashes differ (spec 4.12). A different ValidRound alone is no double-sign, nor is a pair that
// names another proposer, spans two heights or two rounds, or does not verify under the key given.
func TestADifferentValidRoundIsNotEvidence(t *testing.T) {
	signers, _ := makeValidators(t, 2)
	proposer, other := signers[0], signers[1]
	sign := func(signer signing.Signer, name string, height int64, round, validRound int32, blockHash [32]byte) SignedProposal {
		t.Helper()
		signed, err := SignProposal(signer, SignedProposal{Height: height, Round: round, ValidRound: validRound, BlockHash: blockHash, Proposer: name})
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	name := nameOf(proposer)
	first := sign(proposer, name, 3, 1, -1, digest(0x01))
	evidenceAgainst := func(second SignedProposal) ProposalEvidence {
		return ProposalEvidence{Name: name, First: first, Second: second}
	}
	if !evidenceAgainst(sign(proposer, name, 3, 1, -1, digest(0x02))).Valid(scheme, network, proposer.PublicKeyBase64()) {
		t.Fatal("two proposals of one round for different blocks are not evidence")
	}
	for what, second := range map[string]SignedProposal{
		"another valid round, the same block": sign(proposer, name, 3, 1, 0, digest(0x01)),
		"another height":                      sign(proposer, name, 4, 1, -1, digest(0x02)),
		"another round":                       sign(proposer, name, 3, 2, -1, digest(0x02)),
		"another proposer's name":             sign(other, nameOf(other), 3, 1, -1, digest(0x02)),
		"another key under the same name":     sign(other, name, 3, 1, -1, digest(0x02)),
	} {
		if evidenceAgainst(second).Valid(scheme, network, proposer.PublicKeyBase64()) {
			t.Errorf("%s: counted as a double-sign", what)
		}
	}
	if (ProposalEvidence{Name: nameOf(other), First: first, Second: sign(proposer, name, 3, 1, -1, digest(0x02))}).Valid(scheme, network, proposer.PublicKeyBase64()) {
		t.Error("evidence naming another name than its proposals' counted as a double-sign")
	}
}
