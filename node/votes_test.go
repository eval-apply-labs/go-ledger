package node

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Votes reach a node unchecked (POST /hdls/vote). The node keeps for re-gossip, and relays, only a
// vote the height in progress counts, and of each validator only the first vote per round and type
// plus one conflicting vote: junk from anyone on the port, or any number of votes one validator
// signs, cannot grow what every node stores and re-sends on every tick. The same holds in the
// post-commit window, for the next height's votes.
func TestOnlyCountableVotesAreKeptAndRelayed(t *testing.T) {
	network := newHandNet(t, 4)
	self := network.nonProposer(1, 2)
	peer := (self + 1) % 4
	node, recorder := network.node(self)
	for i := 0; i < 5000; i++ {
		junk := consensus.Vote{Height: 1, Round: 1 << 30, Type: consensus.Prevote, Validator: "outsider", Signature: "junk-" + strconv.Itoa(i)}
		node.handle(event{kind: eventVote, vote: &junk})
	}
	forged := network.vote(peer, 1, 0, consensus.Prevote, [32]byte{1})
	forged.Signature = network.vote(peer, 1, 0, consensus.Prevote, [32]byte{2}).Signature
	stranger, _ := consensus.SignVote(testSigner("outsider"), consensus.Vote{Height: 1, Type: consensus.Prevote, BlockHash: [32]byte{1}, Validator: "outsider"})
	impostor, _ := consensus.SignVote(network.signers[(peer+1)%4], consensus.Vote{Height: 1, Type: consensus.Prevote, BlockHash: [32]byte{1}, Validator: network.name(peer)})
	for _, vote := range []consensus.Vote{
		forged,
		stranger,
		impostor, // a validator's name on a vote signed under another validator's key
		network.vote(peer, 2, 0, consensus.Prevote, [32]byte{1}),
		network.vote(peer, 1, -1, consensus.Prevote, [32]byte{1}),
		network.vote(peer, 1, consensus.MaxRoundLookahead+1, consensus.Prevote, [32]byte{1}),
		network.vote(peer, 1, 0, consensus.VoteType(3), [32]byte{1}),
	} {
		node.handle(event{kind: eventVote, vote: &vote})
	}
	if len(node.heightVotes) != 0 || recorder.sent(MessageVote) != 0 {
		t.Fatalf("the node kept %d and relayed %d votes its height does not count", len(node.heightVotes), recorder.sent(MessageVote))
	}

	for digestByte := byte(1); digestByte <= 10; digestByte++ { // one validator signs ten different prevotes for one round
		vote := network.vote(peer, 1, 0, consensus.Prevote, [32]byte{digestByte})
		node.handle(event{kind: eventVote, vote: &vote})
	}
	if len(node.heightVotes) != 2 || recorder.sent(MessageVote) != 2 {
		t.Fatalf("of a validator's ten conflicting prevotes the node kept %d and relayed %d, want the first two", len(node.heightVotes), recorder.sent(MessageVote))
	}
	node.reGossip()
	if got := recorder.sent(MessageVote) - 2; got != 2 {
		t.Fatalf("a re-gossip tick sent %d votes, want the two kept", got)
	}

	node, recorder = network.node(self)
	network.commit(node, self, 1)
	sent := recorder.sent(MessageVote)
	junk := consensus.Vote{Height: 2, Type: consensus.Prevote, Validator: network.name(peer), Signature: "junk"}
	good := network.vote(peer, 2, 0, consensus.Prevote, [32]byte{1})
	for _, vote := range []consensus.Vote{junk, good} {
		node.handle(event{kind: eventVote, vote: &vote})
	}
	if len(node.heightVotes) != 1 || recorder.sent(MessageVote)-sent != 1 {
		t.Fatalf("in the post-commit window the node kept %d and relayed %d of one junk and one good vote", len(node.heightVotes), recorder.sent(MessageVote)-sent)
	}
}

// A relayed copy of a vote the node holds, signature bytes included, is dropped before any signature
// check (spec 4.10): signing is deterministic, so every validator's relay of an honest vote is
// byte-identical, and each copy would otherwise cost a check on the loop. The same holds for a copy of
// the conflicting vote kept beside it.
func TestRelayedVoteCopyCostsNoSignatureCheck(t *testing.T) {
	network := newHandNet(t, 4)
	checks := &atomic.Int64{}
	network.chain.Scheme = countingScheme{Scheme: signing.NetworkScheme(), checks: checks}
	self := network.nonProposer(1)
	peer := (self + 1) % 4
	node, _ := network.node(self)
	first, conflicting := network.vote(peer, 1, 0, consensus.Prevote, [32]byte{1}), network.vote(peer, 1, 0, consensus.Prevote, [32]byte{2})
	for _, vote := range []consensus.Vote{first, conflicting} {
		node.handle(event{kind: eventVote, vote: &vote})
	}
	if len(node.heightVotes) != 2 || checks.Load() == 0 {
		t.Fatal("premise: the node checked and kept the vote and the conflicting one")
	}
	checks.Store(0)
	for _, vote := range []consensus.Vote{first, conflicting} {
		node.handle(event{kind: eventVote, vote: &vote})
	}
	if checks.Load() != 0 {
		t.Fatalf("relayed copies of held votes cost %d signature checks, want none", checks.Load())
	}
}
