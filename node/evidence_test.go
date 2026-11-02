package node

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// carriesEvidenceAgainst reports whether the node's own proposal for height carries evidence, of either
// kind, against the name validatorName.
func carriesEvidenceAgainst(recorder *recordingTransport, height int64, validatorName string) bool {
	proposal := recorder.proposalAt(height)
	return proposal != nil && blockCarriesEvidenceAgainst(proposal.Block, validatorName)
}

// blockCarriesEvidenceAgainst reports whether block carries evidence, of either kind, against name.
func blockCarriesEvidenceAgainst(block ledger.Block, name string) bool {
	for _, evidence := range block.Content.VoteEvidence {
		if evidence.Name == name {
			return true
		}
	}
	for _, evidence := range block.Content.ProposalEvidence {
		if evidence.Name == name {
			return true
		}
	}
	return false
}

// candidateBlock is the block node built for the height in progress: once the height has started, and
// before any proposal for it came, the only block it holds.
func candidateBlock(t *testing.T, node *Node) ledger.Block {
	t.Helper()
	if len(node.blocks) != 1 {
		t.Fatalf("the node holds %d blocks for the height, want its candidate only", len(node.blocks))
	}
	var candidate ledger.Block
	for _, block := range node.blocks {
		candidate = block
	}
	return candidate
}

// frozenBy reports whether the block, applied on node's committed state, freezes the name.
func frozenBy(t *testing.T, node *Node, block ledger.Block, name string) bool {
	t.Helper()
	next, err := ledger.ApplyBlock(node.config.Chain, node.state, node.lastBlockHash, block)
	if err != nil {
		t.Fatalf("the block does not apply: %v", err)
	}
	account, _ := ledger.FindAccount(next.Accounts, name)
	return account.Frozen
}

// A validator that signs a conflicting precommit only after the height decided is still caught: its
// late precommit for the decided round, against its precommit in the commit certificate, is evidence,
// and the node carries it into its next proposal, though no driver counts that height any more. Both
// precommits must verify under the key the committing set holds for the name: a late precommit naming
// the validator under another key proves nothing against it, and the node keeps no proof.
func TestLateDoubleSignIsCarriedIntoTheNextBlock(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(2, 0)
	if network.proposer(1, 0) == self {
		t.Fatal("premise: with four equal validators heights 1 and 2 have different proposers")
	}
	cheat := (self + 1) % 4
	node, recorder := network.node(self)
	proposal := network.proposal(node, 1, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	network.voteAll(node, self, 1, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	for _, i := range []int{cheat, (cheat + 1) % 4, (cheat + 2) % 4} { // the cheat's precommit first, into the certificate
		vote := network.vote(i, 1, 0, consensus.Precommit, proposal.Proposal.BlockHash)
		node.handle(event{kind: eventVote, vote: &vote})
	}
	if node.height() != 2 {
		t.Fatal("premise: the node committed height 1")
	}

	impostor, err := consensus.SignVote(testSigner("impostor"), consensus.Vote{Height: 1, Round: 0, Type: consensus.Precommit, BlockHash: [32]byte{1}, Validator: network.name(cheat)})
	if err != nil {
		t.Fatal(err)
	}
	node.handle(event{kind: eventVote, vote: &impostor})
	if _, kept := node.pendingProofs[network.name(cheat)]; kept {
		t.Fatal("a late precommit naming the cheat, signed under another key, was kept as a proof against it")
	}
	late := network.vote(cheat, 1, 0, consensus.Precommit, [32]byte{1})
	node.handle(event{kind: eventVote, vote: &late})
	if proof := node.pendingProofs[network.name(cheat)]; proof.VoteEvidence == nil || proof.ConvictedKey != network.signers[cheat].PublicKeyBase64() {
		t.Fatalf("the late double-sign was not kept as a proof under the cheat's key: %+v", proof)
	}
	node.handle(event{kind: eventNextHeight, height: node.height()})
	if !carriesEvidenceAgainst(recorder, 2, network.name(cheat)) {
		t.Fatal("the node's next proposal does not carry the late double-sign")
	}
}

// A node that leaves a height by catch-up keeps the equivocations its driver saw there, as it would
// after committing the height itself: the double-signer's evidence goes into its next proposal.
func TestCatchUpKeepsEquivocationEvidence(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(2, 0)
	cheat := (self + 1) % 4
	node, recorder := network.node(self)
	for _, digestByte := range []byte{1, 2} {
		vote := network.vote(cheat, 1, 0, consensus.Prevote, [32]byte{digestByte})
		node.handle(event{kind: eventVote, vote: &vote})
	}

	state := node.state // an empty block's result: the same state, at height 1
	state.Height = 1
	var signingValidators []consensus.Validator
	for _, validator := range network.validatorSet.Validators() {
		if validator.Name != network.name(cheat) {
			signingValidators = append(signingValidators, validator)
		}
	}
	header := ledger.BlockHeader{Height: 1, ValidatorsHash: ledger.ValidatorsHash(network.validatorSet.Validators())}
	checkpoint := Checkpoint{State: state, LastBlock: header, Commit: buildCommit(t, 1, header.BlockHash(state.Digest()), signingValidators, nil),
		Validators: network.validatorSet.Validators()}
	node.handle(event{kind: eventCatchUp, checkpoint: &checkpoint})
	if node.height() != 2 {
		t.Fatal("premise: the node caught up to height 2")
	}
	if !carriesEvidenceAgainst(recorder, 2, network.name(cheat)) {
		t.Fatal("the node's next proposal does not carry the equivocation it saw before catching up")
	}
}

// A proposer that signs a second block for a round it already proposed in is caught when that block
// reaches a node holding the first (spec 6.4): the node keeps the pair as a pending proof, under the key
// of the height's set, and carries it into its next proposal, which freezes the proposer. A second
// proposal signed under another key proves nothing, and the node keeps no proof for it.
func TestProposalEquivocationIsCarriedIntoTheNextBlock(t *testing.T) {
	network := newHandNet(t, 4)
	self, cheat := network.proposer(2, 0), network.proposer(1, 0)
	if cheat == self {
		t.Fatal("premise: with four equal validators heights 1 and 2 have different proposers")
	}
	node, recorder := network.node(self)
	first := network.proposal(node, 1, 0, ledger.BlockContent{})
	second := network.proposal(node, 1, 0, network.payment(self, 1, "5"))
	forged := proposalOf(testSigner("impostor"), network.name(cheat), 1, 0, second.Block) // the proposer's name, another key
	for _, proposal := range []ProposalMessage{first, forged} {
		node.handle(event{kind: eventProposal, proposal: &proposal})
	}
	if _, kept := node.pendingProofs[network.name(cheat)]; kept {
		t.Fatal("a second proposal naming the proposer, signed under another key, was kept as a proof against it")
	}
	node.handle(event{kind: eventProposal, proposal: &second})
	proof := node.pendingProofs[network.name(cheat)]
	if proof.ProposalEvidence == nil || proof.ProposalEvidence.First != first.Proposal || proof.ProposalEvidence.Second != second.Proposal ||
		proof.ConvictedKey != network.signers[cheat].PublicKeyBase64() {
		t.Fatalf("the node did not keep the proposer's two proposals as a proof under its key: %+v", proof)
	}
	network.voteAll(node, self, 1, 0, consensus.Prevote, first.Proposal.BlockHash)
	network.voteAll(node, self, 1, 0, consensus.Precommit, first.Proposal.BlockHash)
	node.handle(event{kind: eventNextHeight, height: node.height()})
	if proposal := recorder.proposalAt(2); proposal == nil || len(proposal.Block.Content.ProposalEvidence) != 1 || !frozenBy(t, node, proposal.Block, network.name(cheat)) {
		t.Fatal("the node's next proposal does not carry the proposal evidence that freezes the proposer")
	}
}

// The probe of the name freeze check (spec 6.4): a validator double-signs at the height whose block moves
// its stake away, so it leaves the set with that block. Its proof does not leave with it: the node keeps
// the proof though the name holds no seat any more, and its next block freezes the name.
func TestProofOutlivesLeavingTheSet(t *testing.T) {
	network := newHandNet(t, 4)
	for i := range network.names { // dynamic: each validator backs itself with its 100
		network.genesis[i].Delegate = network.names[i]
	}
	network.chain = dynamicChain("50")
	network.validatorSet = network.chain.ValidatorRule.SetFor(testGenesis(network.genesis, network.timeout, time.Hour))
	self := network.nonProposer(1)
	cheat := (self + 1) % 4
	node, _ := network.node(self)

	moveAway := signedUpdate(ledger.AccountUpdate{Name: network.name(cheat), Delegate: network.name(self), BlockID: 1})
	proposal := network.proposal(node, 1, 0, ledger.BlockContent{AccountUpdates: []ledger.AccountUpdate{moveAway}})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	conflicting := network.vote(cheat, 1, 0, consensus.Prevote, [32]byte{9})
	node.handle(event{kind: eventVote, vote: &conflicting})
	network.voteAll(node, self, 1, 0, consensus.Prevote, proposal.Proposal.BlockHash) // the cheat's second prevote among them
	network.voteAll(node, self, 1, 0, consensus.Precommit, proposal.Proposal.BlockHash)
	if node.height() != 2 || node.heightValidators.Contains(network.name(cheat)) {
		t.Fatal("premise: the node committed height 1, whose block took the cheat out of the set")
	}

	node.handle(event{kind: eventNextHeight, height: node.height()})
	if candidate := candidateBlock(t, node); !blockCarriesEvidenceAgainst(candidate, network.name(cheat)) || !frozenBy(t, node, candidate, network.name(cheat)) {
		t.Fatal("the node's next block does not freeze the double-signer that left the set")
	}
}

// A pending proof is dropped once its convicted key is no longer its name's key (spec 6.4): here the ⅔
// re-key the double-signer in the block of the height it double-signed at. Under the new key the proof
// is moot, and no block could carry it.
func TestProofIsDroppedWhenTheKeyChanges(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	cheat := (self + 1) % 4
	node, _ := network.node(self)
	action := ledger.Reassignment{Name: network.name(cheat), NewKey: testSigner("re-keyed").PublicKeyBase64()}
	action, err := ledger.SignReassignmentNewKey(testSigner("re-keyed"), action)
	if err != nil {
		t.Fatal(err)
	}
	for i := range network.names {
		vote, err := ledger.SignReassignmentVote(network.signers[i], network.name(i), action)
		if err != nil {
			t.Fatal(err)
		}
		action.Votes = append(action.Votes, vote)
	}

	proposal := network.proposal(node, 1, 0, ledger.BlockContent{Reassignment: &action})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	conflicting := network.vote(cheat, 1, 0, consensus.Prevote, [32]byte{9})
	node.handle(event{kind: eventVote, vote: &conflicting})
	network.voteAll(node, self, 1, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	if evidence := node.driver.Evidence(); len(evidence) != 1 || evidence[0].Name != network.name(cheat) {
		t.Fatal("premise: the node saw the cheat double-sign")
	}
	network.voteAll(node, self, 1, 0, consensus.Precommit, proposal.Proposal.BlockHash)
	if account, _ := ledger.FindAccount(node.state.Accounts, network.name(cheat)); node.height() != 2 || account.PublicKey != action.NewKey {
		t.Fatal("premise: the node committed height 1, whose block re-keyed the cheat")
	}
	if proof, kept := node.pendingProofs[network.name(cheat)]; kept {
		t.Fatalf("the node kept a proof whose convicted key is no longer the name's: %+v", proof)
	}
}

// Pending proofs survive a restart once a snapshot holds them (spec 6.4): the one-third cap can hold one
// back for days, and a release upgrade restarts every node. The next commit, catch-up or adopt saves
// them in the snapshot file beside the checkpoint (here the commit of height 1, whose driver saw the
// double-sign), and the node reloads them, less those the loaded state makes moot: a proof against a
// frozen name, or under a key that is no longer its name's. Until that save a proof is only in memory.
func TestPendingProofSurvivesARestart(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1)
	cheat, other := (self+1)%4, (self+2)%4
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	config := testConfig(network.chain, network.name(self), genesis)
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	node := New(config, &recordingTransport{})
	node.started = true
	node.startHeight()
	conflicting := network.vote(cheat, 1, 0, consensus.Prevote, [32]byte{9})
	node.handle(event{kind: eventVote, vote: &conflicting})
	network.commit(node, self, 1) // the cheat's second prevote among the votes
	saved := node.pendingProofs[network.name(cheat)]
	if saved.VoteEvidence == nil {
		t.Fatal("premise: the node holds a proof against the cheat")
	}

	restarted, err := Open(config, &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if proof := restarted.pendingProofs[network.name(cheat)]; proof.VoteEvidence == nil || *proof.VoteEvidence != *saved.VoteEvidence || proof.ConvictedKey != saved.ConvictedKey {
		t.Fatalf("the restarted node lost the proof against the cheat: %+v", proof)
	}

	state := genesis // the shop's name is frozen in the snapshot of this restart
	state.Accounts = append([]ledger.Account(nil), genesis.Accounts...)
	if state.Accounts[0].Name != "shop" {
		t.Fatal("premise: the shop's account comes first in name order")
	}
	state.Accounts[0].Frozen = true
	snapshot, err := json.Marshal(savedSnapshot{NetworkID: testNetworkID, Checkpoint: checkpointOf(t, 1, state, network.validatorSet.Validators(), nil),
		PendingProofs: map[string]pendingProof{
			network.name(cheat): saved,
			"shop":              {VoteEvidence: saved.VoteEvidence, ConvictedKey: testSigner("shop").PublicKeyBase64()},
			network.name(other): {VoteEvidence: saved.VoteEvidence, ConvictedKey: testSigner("a replaced key").PublicKeyBase64()},
		}})
	if err != nil {
		t.Fatal(err)
	}
	config.SnapshotPath = filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(config.SnapshotPath, snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err = Open(config, &recordingTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if names := slices.Sorted(maps.Keys(restarted.pendingProofs)); !slices.Equal(names, []string{network.name(cheat)}) {
		t.Fatalf("the node reloaded proofs against %v, want the cheat's only", names)
	}
}

// A node signs only while its name is seated with exactly its key (spec 6.3). A node whose name is seated
// under another key (the ⅔ re-keyed it), and a node whose name holds no seat, follow the heights and
// commit, but sign no vote or proposal, here at a height whose proposer the first one's name is. The
// first logs, once, that its name is seated under another key.
func TestNodeWithAReplacedKeyStopsSigning(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.proposer(2, 0)
	if network.proposer(1, 0) == self {
		t.Fatal("premise: with four equal validators heights 1 and 2 have different proposers")
	}
	genesis := testGenesis(network.genesis, network.timeout, time.Hour)
	logged := captureLog(t)

	replaced := testConfig(network.chain, network.name(self), genesis)
	replaced.Signer = testSigner("a replaced key")
	for _, config := range []Config{replaced, testConfig(network.chain, "shop", genesis)} {
		recorder := &recordingTransport{}
		node := New(config, recorder)
		node.started = true
		node.startHeight()
		network.commit(node, self, 1)
		keepBusyByHand(t, "shop", node) // work at height 2: an idle node proposes nothing anyway (spec 6.6)
		node.handle(event{kind: eventNextHeight, height: node.height()})
		if node.driver == nil {
			t.Fatalf("%s: premise: the node runs height 2", config.Name)
		}
		if recorder.proposalAt(2) != nil {
			t.Fatalf("%s: the node proposed", config.Name)
		}
		network.commit(node, self, 2)
		for height := int64(1); height <= 2; height++ {
			for _, voteType := range []consensus.VoteType{consensus.Prevote, consensus.Precommit} {
				if votes := recorder.votes(config.Name, height, 0, voteType); len(votes) != 0 {
					t.Fatalf("%s: the node signed a %s at height %d", config.Name, voteType, height)
				}
			}
		}
	}
	if count := strings.Count(logged.String(), "is seated under another key"); count != 1 || !strings.Contains(logged.String(), network.name(self)) {
		t.Fatalf("the node whose name is seated under another key should say so once, got %d times: %q", count, logged.String())
	}
}
