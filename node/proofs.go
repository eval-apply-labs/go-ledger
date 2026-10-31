package node

import (
	"fmt"
	"log"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// pendingProof is a double-sign proof the node holds until a block carries it (spec 6.4): one vote
// evidence or one proposal evidence item, and the key it was checked under, the convicted key. The
// snapshot file keeps it once the next commit, catch-up or adopt saves it; a restart before that loses
// it.
type pendingProof struct {
	VoteEvidence     *consensus.VoteEvidence
	ProposalEvidence *consensus.ProposalEvidence
	ConvictedKey     string
}

// retireDriver ends the driver of the height the node leaves, by a commit or a catch-up, and keeps the
// double-signs its vote sets saw as pending proofs, each convicting the key the height's set holds for
// its name, the key the votes were checked under. Its caller has not replaced heightValidators yet.
func (node *Node) retireDriver() {
	if node.driver == nil {
		return
	}
	for _, evidence := range node.driver.Evidence() {
		node.pendingProofs[evidence.Name] = pendingProof{VoteEvidence: &evidence, ConvictedKey: node.heightValidators.PublicKey(evidence.Name)}
	}
	node.driver = nil
}

// recordLateDoubleSign keeps as a pending proof a precommit for the last commit's height and round
// that conflicts with its validator's precommit in the commit certificate: a double-sign sent once
// the height had decided, which no driver counts any more. Both precommits must verify under the key
// the set that signed the commit holds for the validator's name, the key the proof convicts. An idle
// node wakes for it (spec 6.6: the candidate carries it), or the proof would wait in memory for other
// work, and a restart of every node meanwhile, as for a release upgrade, would lose it (spec 6.4).
func (node *Node) recordLateDoubleSign(vote consensus.Vote) {
	if vote.Type != consensus.Precommit || vote.Height != node.lastCommit.Height || vote.Round != node.lastCommit.Round {
		return
	}
	for _, committedVote := range node.lastCommit.Votes {
		if committedVote.Validator != vote.Validator {
			continue
		}
		convictedKey := node.lastCommitValidators.PublicKey(vote.Validator)
		evidence := consensus.VoteEvidence{Name: vote.Validator, First: committedVote, Second: vote}
		if evidence.Valid(node.config.Chain.Scheme, node.config.Chain.NetworkID, convictedKey) {
			node.pendingProofs[vote.Validator] = pendingProof{VoteEvidence: &evidence, ConvictedKey: convictedKey}
			node.wakeIfIdle()
		}
	}
}

// reportEquivocation keeps as a pending proof, and logs, a proposal that the proposer of the held one
// signed for the same height and round with another block hash: two signed proposals for one round, the
// proposer's double-sign, even when the two blocks differ only in their nonce (spec 6.4). A different
// ValidRound alone is none. The held proposal is authentic (see onProposal), and the other must verify
// under the key the height's set holds for the proposer, the key the proof convicts. It acts only on the
// first such pair of a round, so a proposer cannot flood the log with more blocks.
func (node *Node) reportEquivocation(held, proposal consensus.SignedProposal) {
	key := fmt.Sprintf("equivocation:%d:%d", proposal.Height, proposal.Round)
	convictedKey := node.heightValidators.PublicKey(proposal.Proposer)
	if proposal.Proposer != held.Proposer || proposal.BlockHash == held.BlockHash || node.relaySeen[key] ||
		!proposal.Verify(node.config.Chain.Scheme, node.config.Chain.NetworkID, convictedKey) {
		return
	}
	node.relaySeen[key] = true
	node.pendingProofs[proposal.Proposer] = pendingProof{
		ProposalEvidence: &consensus.ProposalEvidence{Name: proposal.Proposer, First: held, Second: proposal}, ConvictedKey: convictedKey}
	log.Printf("proposal equivocation: %s signed blocks %x and %x for height %d round %d",
		proposal.Proposer, held.BlockHash, proposal.BlockHash, proposal.Height, proposal.Round)
}

// pendingEvidence is the evidence of every pending proof, which the node offers to ledger.ProposeBlock
// at every height (spec 6.4). ProposeBlock carries what the block can: the items that are valid against
// the state, within the one-third cap.
func (node *Node) pendingEvidence() ([]consensus.VoteEvidence, []consensus.ProposalEvidence) {
	var voteEvidence []consensus.VoteEvidence
	var proposalEvidence []consensus.ProposalEvidence
	for _, proof := range node.pendingProofs {
		if proof.VoteEvidence != nil {
			voteEvidence = append(voteEvidence, *proof.VoteEvidence)
		}
		if proof.ProposalEvidence != nil {
			proposalEvidence = append(proposalEvidence, *proof.ProposalEvidence)
		}
	}
	return voteEvidence, proposalEvidence
}

// prunePendingProofs drops each pending proof whose name the committed state holds frozen, or under
// another key than the one the proof convicts: no block can carry it any more (spec 6.4). A name's key
// changes by the ⅔ action, or, once it is unseated in dynamic mode, by a key change it files with its
// bank. Nothing else drops a proof, so a double-signer does not escape it by moving its stake away and
// leaving the set.
func (node *Node) prunePendingProofs() {
	for name, proof := range node.pendingProofs {
		if account, _ := ledger.FindAccount(node.state.Accounts, name); account.Frozen || account.PublicKey != proof.ConvictedKey {
			delete(node.pendingProofs, name)
		}
	}
}
