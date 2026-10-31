package consensus

import "testing"

// A proposal's signature covers its height, round, valid round, block hash and the proposer's name
// (spec 4.11): a copy that changes any of them, the name included, does not verify. So a proposal cannot
// be moved to another round or block, nor relabelled as another validator's, and since the block hash
// covers the block, nobody can attach another body to it (E15).
func TestSignedProposalCoversEveryField(t *testing.T) {
	signers, _ := makeValidators(t, 1)
	key := signers[0].PublicKeyBase64()
	signed, err := SignProposal(signers[0], SignedProposal{Height: 3, Round: 1, ValidRound: 0, BlockHash: digest(0x42), Proposer: nameOf(signers[0])})
	if err != nil || !signed.Verify(scheme, network, key) {
		t.Fatalf("premise: the signed proposal verifies (err %v)", err)
	}
	for field, change := range map[string]func(*SignedProposal){
		"the height":      func(proposal *SignedProposal) { proposal.Height++ },
		"the round":       func(proposal *SignedProposal) { proposal.Round++ },
		"the valid round": func(proposal *SignedProposal) { proposal.ValidRound = -1 },
		"the block hash":  func(proposal *SignedProposal) { proposal.BlockHash[0]++ },
		"the proposer":    func(proposal *SignedProposal) { proposal.Proposer = "v-another" },
	} {
		changed := signed
		change(&changed)
		if changed.Verify(scheme, network, key) {
			t.Errorf("a proposal with %s changed still verifies", field)
		}
	}
	if signed.Verify(scheme, "othernet", key) {
		t.Error("a proposal verifies on another network")
	}
}
