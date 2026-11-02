package node

import (
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// validatorsOf is the validator list of the given test names: each with the key testSigner holds for
// it and the power given in the same order, or 1 when no powers are given.
func validatorsOf(names []string, powers ...int64) []consensus.Validator {
	validators := make([]consensus.Validator, len(names))
	for i, name := range names {
		power := int64(1)
		if len(powers) > 0 {
			power = powers[i]
		}
		validators[i] = consensus.Validator{Name: name, PublicKey: testSigner(name).PublicKeyBase64(), Power: power}
	}
	return validators
}

// buildCommit assembles a ⅔ precommit certificate for a block hash: one precommit naming each validator
// listed, signed by signers[name], or by testSigner(name) for a name signers leaves out.
func buildCommit(t *testing.T, height int64, blockHash [32]byte, validators []consensus.Validator, signers map[string]*signing.Signer) consensus.Commit {
	t.Helper()
	var votes []consensus.Vote
	for _, validator := range validators {
		signer, found := signers[validator.Name]
		if !found {
			signer = testSigner(validator.Name)
		}
		vote := consensus.Vote{Height: height, Round: 0, Type: consensus.Precommit, BlockHash: blockHash, Validator: validator.Name}
		signed, err := consensus.SignVote(signer, vote)
		if err != nil {
			t.Fatal(err)
		}
		votes = append(votes, signed)
	}
	return consensus.Commit{Height: height, BlockHash: blockHash, Votes: votes}
}

// checkpointOf is the checkpoint of a state committed at height, so the state at that height, with the
// header of a block at that height signed by the validators listed: its commit is signed by every one
// of them (see buildCommit), over the block hash the header and the state's digest give, and it carries
// the list as the signing set. The state's accounts are taken as given, in their order.
func checkpointOf(t *testing.T, height int64, state ledger.LedgerState, validators []consensus.Validator, signers map[string]*signing.Signer) Checkpoint {
	t.Helper()
	state.Height = height
	header := ledger.BlockHeader{Height: height, ValidatorsHash: ledger.ValidatorsHash(validators)}
	return Checkpoint{State: state, LastBlock: header, Commit: buildCommit(t, height, header.BlockHash(state.Digest()), validators, signers), Validators: validators}
}

// selfDelegated is the test account of name with the balance, backing itself as stake.
func selfDelegated(name, amount string) ledger.Account {
	account := testAccount(name, amount)
	account.Delegate = name
	return account
}

// The dynamic-set onboarding refinement: a joiner that only trusts the genesis set must adopt a
// checkpoint whose commit was signed by a LARGER set that grew while it was away (because a
// validator it trusts is among the signers), yet reject one signed only by strangers.
func TestVerifyCheckpointSkippingTrust(t *testing.T) {
	// Genesis trusts only the founder (funded over the threshold); dynamic set.
	genesis := testGenesis([]ledger.Account{selfDelegated("founder", "1000")}, time.Second, 0)
	joiner := New(testConfig(dynamicChain("100"), "founder", genesis), NewHTTPTransport(nil))
	anchor := joiner.view.Load().validators

	// A committed state where the founder and the two newcomers are all validators, with a commit
	// signed by all three.
	grown := testGenesis([]ledger.Account{selfDelegated("founder", "800"), selfDelegated("newcomer-one", "500"), selfDelegated("newcomer-two", "400")}, time.Second, 0)
	grownCheckpoint := checkpointOf(t, 5, grown, validatorsOf([]string{"founder", "newcomer-one", "newcomer-two"}, 800, 500, 400), nil)
	if !joiner.verifyCheckpoint(grownCheckpoint, anchor) {
		t.Fatal("joiner should adopt a checkpoint whose grown-set commit its trusted validator co-signed")
	}

	// A checkpoint signed only by strangers the founder never vouches for must be rejected.
	strangerNames := []string{"stranger-one", "stranger-two", "stranger-three"}
	strangers := testGenesis([]ledger.Account{selfDelegated(strangerNames[0], "1000"), selfDelegated(strangerNames[1], "1000"), selfDelegated(strangerNames[2], "1000")}, time.Second, 0)
	strangerCheckpoint := checkpointOf(t, 5, strangers, validatorsOf(strangerNames, 1000, 1000, 1000), nil)
	if joiner.verifyCheckpoint(strangerCheckpoint, anchor) {
		t.Fatal("a checkpoint signed only by strangers must be rejected — no trusted validator vouches")
	}
}

// A checkpoint carries its signing set as (name, key, power), and the node's anchor holds each trusted
// name's key. A forged checkpoint that gives the real validators' names keys the forger holds, with a
// commit those keys signed, is a quorum of the set it carries, yet it is refused: a vote vouches only
// under the key the anchor holds for its name, so the names alone vouch for nothing. One real
// validator signing a forged state is not enough either: more than a third of the anchor must.
func TestCheckpointGivingRealNamesForeignKeysIsRefused(t *testing.T) {
	names := validatorNames(3)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(append(accounts, testAccount("forger", "0")), time.Second, 0)
	joiner := New(testConfig(staticChain(names...), "joiner", genesis), NewHTTPTransport(nil))
	anchor := joiner.view.Load().validators

	stolen := genesis // everything moved to the forger
	stolen.Accounts = append([]ledger.Account(nil), genesis.Accounts...)
	stolen.Accounts[0].Amount = rational("300") // forger
	for i := 1; i < len(stolen.Accounts); i++ {
		stolen.Accounts[i].Amount = rational("0")
	}
	forgedSet := validatorsOf(names)
	forgerSigners := map[string]*signing.Signer{}
	for i, name := range names {
		forgerSigners[name] = testSigner("forger-key-" + name)
		forgedSet[i].PublicKey = forgerSigners[name].PublicKeyBase64()
	}
	forged := checkpointOf(t, 5, stolen, forgedSet, forgerSigners)
	if !joiner.selfConsistent(forged) {
		t.Fatal("premise: the forged checkpoint's commit is a quorum of the set it carries")
	}
	if joiner.verifyCheckpoint(forged, anchor) {
		t.Fatal("a checkpoint giving the real validators' names the forger's keys was trusted")
	}

	oneReal := validatorsOf(names) // validator-0 under its own key signs the forged state; the others are the forger's
	for i := 1; i < len(oneReal); i++ {
		oneReal[i].PublicKey = forgedSet[i].PublicKey
	}
	delete(forgerSigners, names[0])
	if joiner.verifyCheckpoint(checkpointOf(t, 5, stolen, oneReal, forgerSigners), anchor) {
		t.Fatal("one real validator of three vouched for a checkpoint: more than a third must")
	}

	genuine := checkpointOf(t, 5, stolen, validatorsOf(names), nil)
	if !joiner.verifyCheckpoint(genuine, anchor) {
		t.Fatal("the same state, signed by the real validators under their own keys, was refused")
	}
}

// countingScheme is the network's scheme, counting its signature checks.
type countingScheme struct { // implements signing.Scheme: the network scheme's methods, with Verify replaced
	signing.Scheme
	checks *atomic.Int64
}

func (scheme countingScheme) Verify(publicKey, message, signature []byte) bool {
	scheme.checks.Add(1)
	return scheme.Scheme.Verify(publicKey, message, signature)
}

// A peer can pad a checkpoint's commit with copies of one anchor name's vote, each signed under some
// other key. The anchor checks one vote per name, whether or not it verifies, so the padding costs one
// signature check, not one per copy: checking a checkpoint costs at most one check per anchor name
// plus one per member of the set it carries (spec 4.14).
func TestAnchorChecksOneVotePerName(t *testing.T) {
	names := validatorNames(3)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(accounts, time.Second, 0)
	checks := &atomic.Int64{}
	chain := staticChain(names...)
	chain.Scheme = countingScheme{Scheme: signing.NetworkScheme(), checks: checks}
	joiner := New(testConfig(chain, "joiner", genesis), NewHTTPTransport(nil))

	// The real set signs, except that validator-0's vote is the impostor's; then 20 copies of that vote.
	checkpoint := checkpointOf(t, 5, genesis, validatorsOf(names), map[string]*signing.Signer{names[0]: testSigner("impostor")})
	for range 20 {
		checkpoint.Commit.Votes = append(checkpoint.Commit.Votes, checkpoint.Commit.Votes[0])
	}
	checks.Store(0)
	if joiner.verifyCheckpoint(checkpoint, joiner.view.Load().validators) {
		t.Fatal("a commit of copies of one vote, signed under a key that is not its name's, was trusted")
	}
	if bound := int64(len(names) + len(checkpoint.Validators)); checks.Load() > bound {
		t.Fatalf("checking the checkpoint made %d signature checks, more than one per anchor name plus one per member (%d)", checks.Load(), bound)
	}
}

// A checkpoint carries the last block's header, not its content, and the set that signed it (spec
// 4.14). The block hash is recomputed from the header and the digest of the state, and must be the one
// the commit decided; the set must hash to the header's ValidatorsHash. So a state, a header or a set
// changed after the commit was signed is refused, by a node's check of its own snapshot as of a peer's
// checkpoint.
func TestCheckpointRecomputesTheBlockHash(t *testing.T) {
	names := validatorNames(3)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(accounts, time.Second, 0)
	joiner := New(testConfig(staticChain(names...), "joiner", genesis), NewHTTPTransport(nil))
	anchor := joiner.view.Load().validators
	genuine := checkpointOf(t, 5, genesis, validatorsOf(names), nil)
	if !joiner.selfConsistent(genuine) || !joiner.verifyCheckpoint(genuine, anchor) {
		t.Fatal("premise: the genuine checkpoint is trusted")
	}
	for change, edit := range map[string]func(*Checkpoint){
		"a balance": func(checkpoint *Checkpoint) {
			checkpoint.State.Accounts = append([]ledger.Account(nil), checkpoint.State.Accounts...)
			checkpoint.State.Accounts[0].Amount = rational("101")
		},
		"a parameter":                      func(checkpoint *Checkpoint) { checkpoint.State.Parameters.TransactionCost = rational("0.2") },
		"the state's height":               func(checkpoint *Checkpoint) { checkpoint.State.Height++ },
		"the header's previous block hash": func(checkpoint *Checkpoint) { checkpoint.LastBlock.PreviousBlockHash[0]++ },
		"the header's nonce":               func(checkpoint *Checkpoint) { checkpoint.LastBlock.Nonce[0]++ },
		"the header's content hash":        func(checkpoint *Checkpoint) { checkpoint.LastBlock.ContentHash[0]++ },
		"a signer's power": func(checkpoint *Checkpoint) {
			checkpoint.Validators = append([]consensus.Validator(nil), checkpoint.Validators...)
			checkpoint.Validators[0].Power = 2
		},
	} {
		edited := genuine
		edit(&edited)
		if joiner.selfConsistent(edited) || joiner.verifyCheckpoint(edited, anchor) {
			t.Errorf("a checkpoint whose %s changed after its commit was signed was trusted", change)
		}
	}
}

// A peer can pad a checkpoint with made-up validators and a vote of each for the block, so that the
// votes of the real validators, the ones the anchor knows, are a small part of a commit that is still a
// quorum of the set the checkpoint carries; Phase 1 verified every pad before it trusted such a
// checkpoint. The block hash now covers its signing set: a padded set does not hash to the header, and a
// header forged to cover the pads gives another block hash, which the anchor's validators never signed.
// Either way the checkpoint is refused before any pad's vote is checked (spec 4.14).
func TestPaddedCheckpointIsRefusedBeforeAnyPadIsVerified(t *testing.T) {
	names := validatorNames(3)
	var accounts []ledger.Account
	for _, name := range names {
		accounts = append(accounts, testAccount(name, "100"))
	}
	genesis := testGenesis(accounts, time.Second, 0)
	checks := &atomic.Int64{}
	chain := staticChain(names...)
	chain.Scheme = countingScheme{Scheme: signing.NetworkScheme(), checks: checks}
	joiner := New(testConfig(chain, "joiner", genesis), NewHTTPTransport(nil))
	anchor := joiner.view.Load().validators

	genuine := checkpointOf(t, 5, genesis, validatorsOf(names), nil)
	var padNames []string
	for i := range 20 {
		padNames = append(padNames, fmt.Sprintf("pad-%02d", i))
	}
	pads := validatorsOf(padNames, slices.Repeat([]int64{1000}, len(padNames))...)
	padded := genuine
	padded.Validators = append(append([]consensus.Validator(nil), pads...), genuine.Validators...) // pads sort first
	padVotes := func(blockHash [32]byte) []consensus.Vote { return buildCommit(t, 5, blockHash, pads, nil).Votes }
	padded.Commit.Votes = append(append([]consensus.Vote(nil), genuine.Commit.Votes...), padVotes(genuine.Commit.BlockHash)...)

	forged := padded // its header covers the pads, so its block hash is another, which the pads sign
	forged.LastBlock.ValidatorsHash = ledger.ValidatorsHash(padded.Validators)
	forged.Commit.BlockHash = forged.LastBlock.BlockHash(forged.State.Digest())
	forged.Commit.Votes = append(padVotes(forged.Commit.BlockHash), genuine.Commit.Votes...)

	for name, checkpoint := range map[string]Checkpoint{"padded set": padded, "padded set and forged header": forged} {
		checks.Store(0)
		if joiner.verifyCheckpoint(checkpoint, anchor) {
			t.Fatalf("a checkpoint with a %s was trusted", name)
		}
		if checks.Load() > int64(len(names)) {
			t.Errorf("checking a checkpoint with a %s made %d signature checks: pads were verified", name, checks.Load())
		}
	}
}
