package node

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The confirmation and the two alarms (Phase 4 spec §4.4, §4.5). A node that joined from a seed, a
// snapshot its bank handed it, trusts that seed (R5). Its own accepted signature then confirms it: a
// validator precommits only a block it applied to its own state, so the first commit that holds its own
// precommit is more than ⅔ of the current set agreeing with its state. Until then it is not confirmed.
// The two alarms stop a node for people to look, and it waits for its operator:
//
//   - the fork alarm: two checkpoints that both pass carry commits for different blocks at one height,
//     so more than ⅓ of a set signed twice (raiseForkAlarm);
//   - the divergence alarm: the node, seated, sees more than ⅔ of the set commit a block its own state
//     refuses, so its state and the network's disagree (noteDivergence, divergesFrom); or, seated or
//     not, a checkpoint that passes commits another block than the node's own at its committed height
//     (raiseDivergenceAlarmAt).

// refusedBlock is what the node keeps of a block it refused at the height in progress (see onProposal):
// its header, and why it refused it, for the divergence alarm.
type refusedBlock struct {
	Header ledger.BlockHeader
	Reason string
}

// seatedIn reports whether the node's name is seated in validators under the node's own key, so that it
// signs at their height (see seatedSigner).
func (node *Node) seatedIn(validators *consensus.ValidatorSet) bool {
	return validators != nil && validators.PublicKey(node.self) == node.config.Signer.PublicKeyBase64()
}

// noteConfirmation confirms the node at the height of commit, the commit it has just decided, if it is the
// first that holds the node's own precommit (Phase 4 spec §4.5): node.heightValidators, the set that
// signed it, seats its name under its own key, so a precommit in its name is its own. The status says so
// from then on (statusOf). It is kept in memory only: a restart starts unconfirmed. An observer, which
// never signs, is never confirmed so.
func (node *Node) noteConfirmation(commit consensus.Commit) {
	if node.confirmedHeight.Load() != 0 || !node.seatedIn(node.heightValidators) ||
		!slices.ContainsFunc(commit.Votes, func(vote consensus.Vote) bool { return vote.Validator == node.self }) {
		return
	}
	node.confirmedHeight.Store(commit.Height)
	log.Printf("confirmed: the commit of height %d holds this node's own precommit, so more than ⅔ of the set agree with its state", commit.Height)
}

// halt stops the node until its operator restarts it, on the fork alarm or the divergence alarm: it ends
// the height's driver and starts no other (see startHeight), so it signs nothing more, and it adopts no
// checkpoint (adopt, catchUp, catchUpLoop). It does not follow the chain either: on a fork it would follow
// whichever side its peers are on, and people must find out which is right. The status says why, in the
// public folder too (statusOf). The first alarm is the one kept. It runs on the loop.
func (node *Node) halt(alarm string) {
	if node.haltedBecause.CompareAndSwap(nil, &alarm) {
		log.Printf("%s. The node signs and adopts nothing more until its operator restarts it.", alarm)
	}
	node.retireDriver()
	if node.publicFolder != nil {
		node.publishAnswer("hdls/status", node.statusOf(node.view.Load()))
	}
}

// raiseForkAlarm is the fork alarm: first and second, the commits of two checkpoints that both pass, are
// for different blocks at one height. The node adopts neither, logs both commits in full, as the evidence
// people need, and hands the loop the alarm, which halts it (halt): it does not pick one. Catch-up's
// goroutine calls it (see bestCheckpoint).
func (node *Node) raiseForkAlarm(first, second consensus.Commit) {
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	alarm := fmt.Sprintf("fork alarm: two checkpoints that both verify commit different blocks at height %d, %x and %x",
		first.Height, first.BlockHash, second.BlockHash)
	log.Printf("%s: more than a third of a set signed both. The first commit: %s The second commit: %s", alarm, firstJSON, secondJSON)
	node.enqueue(event{kind: eventHalt, alarm: alarm})
}

// raiseDivergenceAlarmAt is the divergence alarm of a checkpoint that passes at the node's committed
// height: its commit decides another block than ownBlockHash, the node's own there, so more than ⅔ of a
// frame the node trusts committed a block its state did not. The node adopts nothing, logs the commit in
// full, as the evidence people need, and hands the loop the alarm, which halts it (halt). Catch-up's
// goroutine calls it (see bestCheckpoint).
func (node *Node) raiseDivergenceAlarmAt(commit consensus.Commit, ownBlockHash [32]byte) {
	commitJSON, _ := json.Marshal(commit)
	alarm := fmt.Sprintf("divergence alarm: more than ⅔ of the set committed block %x at height %d, and this node committed block %x there",
		commit.BlockHash, commit.Height, ownBlockHash)
	log.Printf("%s. The network's commit: %s", alarm, commitJSON)
	node.enqueue(event{kind: eventHalt, alarm: alarm})
}

// noteDivergence raises the divergence alarm when the node, seated in the height's set under its own key,
// holds precommits of more than ⅔ of the set, in one round, for blockHash, the block of an authentic
// proposal it refused (see onProposal): the network committed a block its state says is invalid. onProposal
// calls it as it refuses a block, and the loop as it takes a vote (handle), so the alarm comes whichever of
// the two reaches the node last. The precommits are those countable took, whose signatures it checked.
func (node *Node) noteDivergence(blockHash [32]byte) {
	refused, found := node.refusedBlocks[blockHash]
	if !found || !node.seatedIn(node.heightValidators) {
		return
	}
	tallies := map[int32]*consensus.CertificateTally{} // the precommits for the block, by round
	for _, vote := range node.heightVotes {
		if vote.Type != consensus.Precommit || vote.BlockHash != blockHash {
			continue
		}
		if tallies[vote.Round] == nil {
			tallies[vote.Round] = consensus.NewCertificateTally(node.heightValidators)
		}
		if tallies[vote.Round].Add(vote.Validator, func(string) bool { return true }) && tallies[vote.Round].Reached() {
			node.raiseDivergenceAlarm(refused.Header, refused.Reason)
			return
		}
	}
}

// divergesFrom raises the divergence alarm, and reports so, when the node, seated, is handed a checkpoint
// that passes, of its height in progress, whose block does not build on the node's last block: more than
// ⅔ of the set committed a block there that the node's state refuses. A checkpoint further ahead does not
// say which block of the node's height it builds on, and a catch-up to it is taken as before. adopt and
// catchUp call it once the checkpoint has passed.
func (node *Node) divergesFrom(checkpoint Checkpoint) bool {
	if !node.seatedIn(node.view.Load().validators) || checkpoint.Commit.Height != node.height() ||
		checkpoint.LastBlock.PreviousBlockHash == node.lastBlockHash {
		return false
	}
	node.raiseDivergenceAlarm(checkpoint.LastBlock, "the block does not build on the last block: its previous block hash is another")
	return true
}

// raiseDivergenceAlarm is the divergence alarm (Q15): more than ⅔ of the set committed the block whose
// header is networkBlock, at the node's height in progress, and the node's own state refuses it, for the
// reason given. It is a fork, a broken node, or a false seed. The node logs both blocks, the network's and
// its own last one, and halts (halt): it does not seed itself again, and waits for its operator. It runs
// on the loop.
func (node *Node) raiseDivergenceAlarm(networkBlock ledger.BlockHeader, reason string) {
	networkJSON, _ := json.Marshal(networkBlock)
	ownJSON, _ := json.Marshal(node.lastBlockHeader)
	alarm := fmt.Sprintf("divergence alarm: more than ⅔ of the set committed a block at height %d that this node's state refuses: %s", node.height(), reason)
	log.Printf("%s. The network's block: %s This node's last block, of height %d and hash %x: %s", alarm, networkJSON, node.state.Height, node.lastBlockHash, ownJSON)
	node.halt(alarm)
}
