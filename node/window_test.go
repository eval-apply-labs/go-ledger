package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// handNet is a static validator set whose keys the test holds, for driving one node by hand: the test
// calls handle() as the run loop would, so every step is deterministic and the node's state can be
// read directly. Each validator holds a genesis account of 100, and a shop's account takes payments.
type handNet struct {
	t            *testing.T
	names        []string
	signers      []*signing.Signer
	chain        ledger.Chain
	validatorSet *consensus.ValidatorSet
	genesis      []ledger.Account
	timeout      time.Duration // the nodes' step timeout; an hour, so no timer fires unless the test lets it
}

func newHandNet(t *testing.T, validatorCount int) *handNet {
	network := &handNet{t: t, timeout: time.Hour, names: validatorNames(validatorCount)}
	for _, name := range network.names {
		network.signers = append(network.signers, testSigner(name))
		network.genesis = append(network.genesis, testAccount(name, "100"))
	}
	network.genesis = append(network.genesis, testAccount("shop", "0"))
	network.chain = staticChain(network.names...)
	network.validatorSet = network.chain.ValidatorRule.SetFor(testGenesis(network.genesis, network.timeout, time.Hour))
	return network
}

// node builds validator self's node and starts its first height, without the run loop. The next
// height starts only when the test hands the node an eventNextHeight.
func (network *handNet) node(self int) (*Node, *recordingTransport) {
	recorder := &recordingTransport{}
	node := New(testConfig(network.chain, network.names[self], testGenesis(network.genesis, network.timeout, time.Hour)), recorder)
	node.started = true
	node.startHeight()
	return node, recorder
}

func (network *handNet) name(i int) string { return network.names[i] }

// proposer is the validator scheduled to propose at (height, round).
func (network *handNet) proposer(height int64, round int32) int {
	for i := range network.signers {
		if network.name(i) == network.validatorSet.ProposerAt(height, round) {
			return i
		}
	}
	network.t.Fatal("no proposer")
	return -1
}

// nonProposer is a validator that does not propose round 0 of any of the heights.
func (network *handNet) nonProposer(heights ...int64) int {
	for i := range network.signers {
		proposesNone := true
		for _, height := range heights {
			proposesNone = proposesNone && network.proposer(height, 0) != i
		}
		if proposesNone {
			return i
		}
	}
	network.t.Fatal("every validator proposes one of the heights")
	return -1
}

// sign is validator i's proposal of block for (height, round).
func (network *handNet) sign(i int, height int64, round int32, block ledger.Block) ProposalMessage {
	return proposalOf(network.signers[i], network.name(i), height, round, block)
}

// proposal is what the scheduled proposer of (height, round) sends: the block of content on node's
// committed state and last block, height being the height node works on. Its nonce is zero, so one
// content gives one block, as one proposer's candidate does.
func (network *handNet) proposal(node *Node, height int64, round int32, content ledger.BlockContent) ProposalMessage {
	block := ledger.ProposeBlock(network.chain, node.state, node.lastBlockHash, [32]byte{}, content)
	return network.sign(network.proposer(height, round), height, round, block)
}

func (network *handNet) vote(i int, height int64, round int32, voteType consensus.VoteType, digest [32]byte) consensus.Vote {
	vote, err := consensus.SignVote(network.signers[i], consensus.Vote{Height: height, Round: round, Type: voteType, BlockHash: digest, Validator: network.name(i)})
	if err != nil {
		network.t.Fatal(err)
	}
	return vote
}

// voteAll hands node every other validator's vote of type voteType for digest at (height, round).
func (network *handNet) voteAll(node *Node, self int, height int64, round int32, voteType consensus.VoteType, digest [32]byte) {
	for i := range network.signers {
		if i != self {
			vote := network.vote(i, height, round, voteType, digest)
			node.handle(event{kind: eventVote, vote: &vote})
		}
	}
}

// commit has node decide an empty block at height, round 0, where self is not the proposer.
func (network *handNet) commit(node *Node, self int, height int64) {
	network.t.Helper()
	proposal := network.proposal(node, height, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	network.voteAll(node, self, height, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	network.voteAll(node, self, height, 0, consensus.Precommit, proposal.Proposal.BlockHash)
	if node.height() != height+1 {
		network.t.Fatalf("the node did not commit height %d", height)
	}
}

// Nodes commit a height at slightly different times, so the next height's proposal and votes often
// reach a node in the block interval window after its commit, before it starts the next height.
// They must be checked against the next height's proposer and set, held (the proposal relayed), and
// fed to the next height's driver when it starts: the node then prevotes the proposal and decides
// at once, instead of losing the round. With four equal validators heights 1 and 2 have different
// round-0 proposers, with three the same one.
func TestPostCommitWindowHoldsTheNextHeightsProposalAndVotes(t *testing.T) {
	for _, validatorCount := range []int{3, 4} {
		t.Run(fmt.Sprintf("%d validators", validatorCount), func(t *testing.T) {
			network := newHandNet(t, validatorCount)
			self := network.nonProposer(1, 2)
			node, recorder := network.node(self)
			network.commit(node, self, 1)

			proposal := network.proposal(node, 2, 0, ledger.BlockContent{})
			node.handle(event{kind: eventProposal, proposal: &proposal})
			if recorder.proposalAt(2) == nil {
				t.Fatal("the next height's proposal was not taken in the window")
			}
			network.voteAll(node, self, 2, 0, consensus.Prevote, proposal.Proposal.BlockHash)
			network.voteAll(node, self, 2, 0, consensus.Precommit, proposal.Proposal.BlockHash)
			node.handle(event{kind: eventNextHeight, height: node.height()})

			if prevotes := recorder.votes(network.name(self), 2, 0, consensus.Prevote); len(prevotes) != 1 || prevotes[0].BlockHash != proposal.Proposal.BlockHash {
				t.Fatalf("the node did not prevote the proposal it held for height 2: %v", prevotes)
			}
			if node.height() != 3 {
				t.Fatal("the node did not decide height 2 from the proposal and votes it held")
			}
		})
	}
}

// A consensus timer belongs to the height that armed it. Round numbers restart every height, so the
// round-0 propose timer of height 1, firing once the node is at height 2, must not make it prevote
// nil there: the node still prevotes the height-2 proposal that arrives within its own timeout.
func TestStaleTimerDoesNotFireIntoTheNextHeight(t *testing.T) {
	network := newHandNet(t, 4)
	network.timeout = 50 * time.Millisecond
	self := network.nonProposer(1, 2)
	node, recorder := network.node(self)
	stale := <-node.events // height 1's propose timeout, the only timer armed so far
	network.commit(node, self, 1)
	keepBusyByHand(t, "shop", node) // work at height 2: an idle node has no driver for the timer to fire into (spec 6.6)
	node.handle(event{kind: eventNextHeight, height: node.height()})
	if node.driver == nil {
		t.Fatal("premise: the node runs height 2")
	}

	node.handle(stale)
	if prevotes := recorder.votes(network.name(self), 2, 0, consensus.Prevote); len(prevotes) != 0 {
		t.Fatalf("height 1's timer made the node prevote at height 2: %v", prevotes)
	}
	proposal := network.proposal(node, 2, 0, ledger.BlockContent{})
	node.handle(event{kind: eventProposal, proposal: &proposal})
	if prevotes := recorder.votes(network.name(self), 2, 0, consensus.Prevote); len(prevotes) != 1 || prevotes[0].BlockHash != proposal.Proposal.BlockHash {
		t.Fatalf("the node did not prevote the height-2 proposal: %v", prevotes)
	}
}

// A node that decided a height re-sends none of its votes, yet a node still at that height may lack
// one it needs: a validator that decided on its own precommit, logged before a restart, never sent
// it. Re-gossip at the next height therefore carries the precommits that decided the last one, and
// the node that was one short decides instead of waiting for a checkpoint.
func TestReGossipCarriesTheLastCommitToANodeStillAtThatHeight(t *testing.T) {
	network := newHandNet(t, 4)
	ahead := network.nonProposer(1, 2)
	behind := 0
	for behind == ahead || behind == network.proposer(1, 0) {
		behind++
	}
	nodeAhead, _ := network.node(ahead)
	nodeBehind, _ := network.node(behind)
	network.commit(nodeAhead, ahead, 1)
	keepBusyByHand(t, "shop", nodeAhead) // an idle node re-gossips nothing (spec 6.6)
	nodeAhead.handle(event{kind: eventNextHeight, height: nodeAhead.height()})

	proposal := network.proposal(nodeBehind, 1, 0, ledger.BlockContent{})
	nodeBehind.handle(event{kind: eventProposal, proposal: &proposal})
	network.voteAll(nodeBehind, behind, 1, 0, consensus.Prevote, proposal.Proposal.BlockHash)
	proposerPrecommit := network.vote(network.proposer(1, 0), 1, 0, consensus.Precommit, proposal.Proposal.BlockHash)
	nodeBehind.handle(event{kind: eventVote, vote: &proposerPrecommit}) // its own precommit and one other: one short of quorum
	if nodeBehind.height() != 1 {
		t.Fatal("premise: the node is still at height 1")
	}

	regossip := &recordingTransport{}
	nodeAhead.transport = regossip
	nodeAhead.handle(event{kind: eventRegossip})
	for _, message := range regossip.messages {
		if message.Vote != nil {
			nodeBehind.handle(event{kind: eventVote, vote: message.Vote})
		}
	}
	if nodeBehind.height() != 2 {
		t.Fatal("the node one precommit short did not decide height 1 from the re-gossiped last commit")
	}
}

// Three of four validators can decide a height while the fourth has lost every copy of the proposal
// they decide, and a precommit: once the height is decided no node re-sends that proposal as its
// current round's, and the precommits are no use without it. The last commit therefore carries its
// proposal, sent as a node commits and again on re-gossip, and the node still at that height decides
// from either instead of waiting for a checkpoint.
func TestTheLastCommitCarriesItsProposal(t *testing.T) {
	for _, reGossip := range []bool{false, true} {
		network := newHandNet(t, 4)
		ahead := network.nonProposer(1, 2)
		behind := 0
		for behind == ahead || behind == network.proposer(1, 0) {
			behind++
		}
		nodeAhead, _ := network.node(ahead)
		nodeBehind, _ := network.node(behind)
		proposal := network.proposal(nodeAhead, 1, 0, ledger.BlockContent{})
		nodeAhead.handle(event{kind: eventProposal, proposal: &proposal}) // every copy to the behind node is lost
		network.voteAll(nodeAhead, ahead, 1, 0, consensus.Prevote, proposal.Proposal.BlockHash)
		sent := &recordingTransport{} // ahead's own precommit, sent before, is lost too
		nodeAhead.transport = sent
		for i := range network.signers {
			if i != ahead && i != behind {
				vote := network.vote(i, 1, 0, consensus.Precommit, proposal.Proposal.BlockHash)
				nodeBehind.handle(event{kind: eventVote, vote: &vote})
				nodeAhead.handle(event{kind: eventVote, vote: &vote})
			}
		}
		if reGossip {
			keepBusyByHand(t, "shop", nodeAhead) // an idle node re-gossips nothing (spec 6.6)
			nodeAhead.handle(event{kind: eventNextHeight, height: nodeAhead.height()})
			sent = &recordingTransport{}
			nodeAhead.transport = sent
			nodeAhead.handle(event{kind: eventRegossip})
		}
		for _, message := range sent.messages {
			if message.Type == MessageProposal {
				nodeBehind.handle(event{kind: eventProposal, proposal: message.Proposal})
			} else if message.Type == MessageVote {
				nodeBehind.handle(event{kind: eventVote, vote: message.Vote})
			}
		}
		if nodeBehind.height() != 2 {
			t.Fatalf("the node that lost the proposal did not decide height 1 from the last commit (re-gossip: %v)", reGossip)
		}
	}
}
