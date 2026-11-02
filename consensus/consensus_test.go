package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

const network = "mainnet"

var scheme = signing.NetworkScheme()

// nameOf is a test validator's name, derived from its key so that a test can pass signers around and
// still name them: "v-" and the first 12 hex digits of the key's SHA-256.
func nameOf(signer *signing.Signer) string {
	keyHash := sha256.Sum256(signer.PublicKey())
	return "v-" + hex.EncodeToString(keyHash[:6])
}

func makeValidators(t *testing.T, count int) ([]*signing.Signer, *ValidatorSet) {
	t.Helper()
	signers := make([]*signing.Signer, count)
	validators := make([]Validator, count)
	for i := 0; i < count; i++ {
		signer, err := signing.Generate(scheme, network)
		if err != nil {
			t.Fatal(err)
		}
		signers[i] = signer
		validators[i] = Validator{Name: nameOf(signer), PublicKey: signer.PublicKeyBase64(), Power: 1}
	}
	return signers, NewValidatorSet(validators)
}

// weightedValidators is like makeValidators, with the given powers: signer i holds powers[i].
func weightedValidators(t *testing.T, powers ...int64) ([]*signing.Signer, *ValidatorSet) {
	t.Helper()
	signers, _ := makeValidators(t, len(powers))
	validators := make([]Validator, len(powers))
	for i, power := range powers {
		validators[i] = Validator{Name: nameOf(signers[i]), PublicKey: signers[i].PublicKeyBase64(), Power: power}
	}
	return signers, NewValidatorSet(validators)
}

func digest(fillByte byte) [32]byte {
	var blockDigest [32]byte
	for i := range blockDigest {
		blockDigest[i] = fillByte
	}
	return blockDigest
}

func mustVote(t *testing.T, signer *signing.Signer, height int64, round int32, voteType VoteType, blockDigest [32]byte) Vote {
	t.Helper()
	vote, err := SignVote(signer, Vote{Height: height, Round: round, Type: voteType, BlockHash: blockDigest, Validator: nameOf(signer)})
	if err != nil {
		t.Fatal(err)
	}
	return vote
}

func TestQuorumIsTwoThirdsPlusOne(t *testing.T) {
	_, fourValidators := makeValidators(t, 4)
	if got := fourValidators.Quorum(); got != 3 { // floor(8/3)+1 = 3
		t.Fatalf("quorum(4) = %d, want 3", got)
	}
	_, sevenValidators := makeValidators(t, 7)
	if got := sevenValidators.Quorum(); got != 5 { // floor(14/3)+1 = 5
		t.Fatalf("quorum(7) = %d, want 5", got)
	}
}

func TestVoteSetReachesMajority(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAA)
	voteSet := NewVoteSet(validatorSet, 1, 0, Precommit)
	for i := 0; i < 3; i++ { // 3 of 4 == quorum
		if _, err := voteSet.Add(scheme, network, mustVote(t, signers[i], 1, 0, Precommit, blockDigest)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	got, hasMajority := voteSet.TwoThirdsMajority()
	if !hasMajority || got != blockDigest {
		t.Fatalf("expected majority for digest, ok=%v", hasMajority)
	}
}

func TestVoteSetBelowQuorumNoMajority(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAA)
	voteSet := NewVoteSet(validatorSet, 1, 0, Precommit)
	for i := 0; i < 2; i++ { // 2 of 4 < quorum 3
		voteSet.Add(scheme, network, mustVote(t, signers[i], 1, 0, Precommit, blockDigest))
	}
	if _, hasMajority := voteSet.TwoThirdsMajority(); hasMajority {
		t.Fatal("2 of 4 must not be a majority")
	}
}

func TestWrongVoteAndNonValidatorRejected(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	voteSet := NewVoteSet(validatorSet, 1, 0, Precommit)
	// wrong round
	if _, err := voteSet.Add(scheme, network, mustVote(t, signers[0], 1, 9, Precommit, digest(1))); err != ErrWrongVote {
		t.Fatalf("want ErrWrongVote, got %v", err)
	}
	// a signer that is not in the validator set
	outsider, _ := signing.Generate(scheme, network)
	if _, err := voteSet.Add(scheme, network, mustVote(t, outsider, 1, 0, Precommit, digest(1))); err != ErrNotValidator {
		t.Fatalf("want ErrNotValidator, got %v", err)
	}
}

func TestEquivocationProducesEvidenceAndUncounts(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	voteSet := NewVoteSet(validatorSet, 1, 0, Prevote)
	digestX, digestY := digest(0x11), digest(0x22)

	voteSet.Add(scheme, network, mustVote(t, signers[0], 1, 0, Prevote, digestX))
	if _, err := voteSet.Add(scheme, network, mustVote(t, signers[0], 1, 0, Prevote, digestY)); err != ErrEquivocation {
		t.Fatalf("expected equivocation, got %v", err)
	}
	if len(voteSet.Evidence()) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(voteSet.Evidence()))
	}
	// The equivocator's power must count for neither digest. Add two honest votes for X:
	voteSet.Add(scheme, network, mustVote(t, signers[1], 1, 0, Prevote, digestX))
	voteSet.Add(scheme, network, mustVote(t, signers[2], 1, 0, Prevote, digestX))
	if _, hasMajority := voteSet.TwoThirdsMajority(); hasMajority {
		t.Fatal("equivocator un-counted: X has power 2 < quorum 3, must not be a majority")
	}
}

func TestCommitVerifies(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAB)
	voteSet := NewVoteSet(validatorSet, 5, 0, Precommit)
	for i := 0; i < 3; i++ {
		voteSet.Add(scheme, network, mustVote(t, signers[i], 5, 0, Precommit, blockDigest))
	}
	commit, hasCommit := voteSet.Commit()
	if !hasCommit {
		t.Fatal("expected a commit")
	}
	if !commit.Verify(scheme, network, validatorSet) {
		t.Fatal("commit did not verify")
	}
	if commit.Height != 5 || commit.BlockHash != blockDigest {
		t.Fatalf("commit height/digest wrong: %d %x", commit.Height, commit.BlockHash[:2])
	}
}

func TestCommitBelowQuorumRejected(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAB)
	commit := Commit{Height: 1, BlockHash: blockDigest, Votes: []Vote{
		mustVote(t, signers[0], 1, 0, Precommit, blockDigest),
		mustVote(t, signers[1], 1, 0, Precommit, blockDigest),
	}}
	if commit.Verify(scheme, network, validatorSet) {
		t.Fatal("a 2-of-4 commit must not verify")
	}
}

func TestCommitDuplicateVoterRejected(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAB)
	repeatedVote := mustVote(t, signers[0], 1, 0, Precommit, blockDigest)
	commit := Commit{Height: 1, BlockHash: blockDigest, Votes: []Vote{repeatedVote, repeatedVote, repeatedVote}} // padded with one voter
	if commit.Verify(scheme, network, validatorSet) {
		t.Fatal("a commit padded with duplicate voters must not verify")
	}
}

func TestCommitTamperedVoteRejected(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAB)
	votes := []Vote{
		mustVote(t, signers[0], 1, 0, Precommit, blockDigest),
		mustVote(t, signers[1], 1, 0, Precommit, blockDigest),
		mustVote(t, signers[2], 1, 0, Precommit, blockDigest),
	}
	votes[2].BlockHash = digest(0xCD) // tamper: claim a different digest under the same sig
	commit := Commit{Height: 1, BlockHash: blockDigest, Votes: votes}
	if commit.Verify(scheme, network, validatorSet) {
		t.Fatal("a commit containing a tampered/mismatched vote must not verify")
	}
}

// A commit must be single-round: mixing precommits from different rounds, or carrying a Round that
// does not match its votes, is not a proof that any one round decided the block, and must be rejected
// — the forged-mixed-round-certificate attack on onboarding/checkpoint sync.
func TestCommitRejectsMixedRounds(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4) // quorum 3
	blockDigest := digest(0xAB)

	mixed := Commit{Height: 5, Round: 0, BlockHash: blockDigest, Votes: []Vote{
		mustVote(t, signers[0], 5, 0, Precommit, blockDigest),
		mustVote(t, signers[1], 5, 0, Precommit, blockDigest),
		mustVote(t, signers[2], 5, 1, Precommit, blockDigest), // round 1 — does not belong to a round-0 commit
	}}
	if mixed.Verify(scheme, network, validatorSet) {
		t.Fatal("a commit mixing rounds must not verify")
	}

	wrongRound := Commit{Height: 5, Round: 1, BlockHash: blockDigest, Votes: []Vote{
		mustVote(t, signers[0], 5, 0, Precommit, blockDigest),
		mustVote(t, signers[1], 5, 0, Precommit, blockDigest),
		mustVote(t, signers[2], 5, 0, Precommit, blockDigest),
	}}
	if wrongRound.Verify(scheme, network, validatorSet) {
		t.Fatal("a commit whose Round mismatches its votes must not verify")
	}

	good := Commit{Height: 5, Round: 2, BlockHash: blockDigest, Votes: []Vote{
		mustVote(t, signers[0], 5, 2, Precommit, blockDigest),
		mustVote(t, signers[1], 5, 2, Precommit, blockDigest),
		mustVote(t, signers[2], 5, 2, Precommit, blockDigest),
	}}
	if !good.Verify(scheme, network, validatorSet) {
		t.Fatal("a single-round commit at its matching round should verify")
	}
}

// Safety property (the point of the whole layer): two commits for DIFFERENT digests in one round
// of a height cannot both be produced honestly, because any two >2/3 quorums intersect -- an
// overlapping validator must have precommitted BOTH digests in that round, i.e. equivocated. This
// is exactly what a >1/2 rule cannot guarantee (two disjoint halves committing conflicting
// blocks under a partition). Across rounds an honest validator may precommit two blocks; there the
// lock keeps the commits from conflicting (TestDriverLockingPreventsConflictingPrevote).
func TestConflictingCommitsRequireEquivocation(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4) // quorum 3
	digestX, digestY := digest(0x01), digest(0x02)

	commitX := Commit{Height: 9, BlockHash: digestX, Votes: []Vote{
		mustVote(t, signers[0], 9, 0, Precommit, digestX),
		mustVote(t, signers[1], 9, 0, Precommit, digestX),
		mustVote(t, signers[2], 9, 0, Precommit, digestX),
	}}
	commitY := Commit{Height: 9, BlockHash: digestY, Votes: []Vote{
		mustVote(t, signers[1], 9, 0, Precommit, digestY),
		mustVote(t, signers[2], 9, 0, Precommit, digestY),
		mustVote(t, signers[3], 9, 0, Precommit, digestY),
	}}
	if !commitX.Verify(scheme, network, validatorSet) || !commitY.Verify(scheme, network, validatorSet) {
		t.Fatal("each commit individually has quorum and should verify")
	}

	overlap := commitOverlap(commitX, commitY)
	if len(overlap) == 0 {
		t.Fatal("two conflicting quorum commits MUST share a validator (quorum intersection)")
	}

	// Feeding both sets of precommits into one vote set surfaces the overlap as
	// equivocation evidence -- the durable proof a single-phase snapshot never produces.
	voteSet := NewVoteSet(validatorSet, 9, 0, Precommit)
	for _, vote := range commitX.Votes {
		voteSet.Add(scheme, network, vote)
	}
	for _, vote := range commitY.Votes {
		voteSet.Add(scheme, network, vote)
	}
	if len(voteSet.Evidence()) == 0 {
		t.Fatal("the overlapping double-signers must be caught as equivocation evidence")
	}
}

func commitOverlap(first, second Commit) []string {
	votedInFirst := make(map[string]bool)
	for _, vote := range first.Votes {
		votedInFirst[vote.Validator] = true
	}
	var sharedVoters []string
	for _, vote := range second.Votes {
		if votedInFirst[vote.Validator] {
			sharedVoters = append(sharedVoters, vote.Validator)
		}
	}
	return sharedVoters
}

// A vote names its signer and carries no key: the vote set and a commit check it under the key the
// validator set holds for that name. A vote signed under any other key, another validator's included,
// counts for nothing, so holding one validator's key never casts another's vote.
func TestVoteCountsOnlyUnderTheSetKey(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	blockDigest := digest(0xAB)
	forged, err := SignVote(signers[1], Vote{Height: 1, Round: 0, Type: Precommit, BlockHash: blockDigest, Validator: nameOf(signers[0])})
	if err != nil {
		t.Fatal(err)
	}
	if forged.Verify(scheme, network, validatorSet.PublicKey(nameOf(signers[0]))) {
		t.Fatal("a vote naming one validator verified under that validator's key, though another key signed it")
	}

	voteSet := NewVoteSet(validatorSet, 1, 0, Precommit)
	if counted, err := voteSet.Add(scheme, network, forged); counted || err != ErrBadSignature {
		t.Fatalf("a vote signed under another validator's key: counted %v, error %v; want not counted, ErrBadSignature", counted, err)
	}
	if counted, err := voteSet.Add(scheme, network, mustVote(t, signers[0], 1, 0, Precommit, blockDigest)); !counted || err != nil {
		t.Fatalf("the genuine vote: counted %v, error %v; want counted", counted, err)
	}

	commit := Commit{Height: 1, BlockHash: blockDigest, Votes: []Vote{
		forged,
		mustVote(t, signers[2], 1, 0, Precommit, blockDigest),
		mustVote(t, signers[3], 1, 0, Precommit, blockDigest),
	}}
	if commit.Verify(scheme, network, validatorSet) {
		t.Fatal("a commit verified with a vote signed under a key that is not its name's")
	}
	commit.Votes[0] = mustVote(t, signers[0], 1, 0, Precommit, blockDigest)
	if !commit.Verify(scheme, network, validatorSet) {
		t.Fatal("the genuine commit did not verify")
	}
}

// CertificateTally holds the > ⅔ certificate rule that Commit.Verify, ParameterChange.Valid,
// Reassignment.Valid and the wallet share. A name counts once, only if it is in the set, and only for a
// signature that verifies under the key the set holds for it; a repeated name and a name outside the
// set cost no signature check. A name whose vote did not count can still count with a good one (the
// wallet keeps that vote), and the tally reaches the quorum at exactly its power, not one below.
func TestCertificateTallyCountsEachNameOnceUnderTheSetsKey(t *testing.T) {
	signers, validatorSet := weightedValidators(t, 3, 3, 2, 1) // total 9, quorum 7
	outsiders, _ := makeValidators(t, 1)
	blockDigest := digest(0xAB)
	precommitBy := func(signer *signing.Signer) Vote { return mustVote(t, signer, 1, 0, Precommit, blockDigest) }
	checks := 0
	tally := NewCertificateTally(validatorSet)
	add := func(vote Vote) bool {
		return tally.Add(vote.Validator, func(publicKey string) bool {
			checks++
			return vote.Verify(scheme, network, publicKey)
		})
	}

	if !add(precommitBy(signers[0])) || add(precommitBy(signers[0])) {
		t.Error("a name must count once: its first vote counts, its repeat does not")
	}
	if add(precommitBy(outsiders[0])) {
		t.Error("a name outside the set counted")
	}
	if checks != 1 {
		t.Errorf("a repeated name and a name outside the set cost %d signature checks, want none", checks-1)
	}
	inAnotherName, _ := SignVote(outsiders[0], Vote{Height: 1, Round: 0, Type: Precommit, BlockHash: blockDigest, Validator: nameOf(signers[1])})
	if add(inAnotherName) {
		t.Error("a vote signed under another key than the one the set holds for its name counted")
	}
	if !add(precommitBy(signers[1])) || tally.Power() != 6 || tally.Reached() {
		t.Errorf("the name's good vote counts after its bad one, and power 6 is one below the quorum 7: power %d, reached %v", tally.Power(), tally.Reached())
	}
	if !add(precommitBy(signers[3])) || tally.Power() != 7 || !tally.Reached() {
		t.Errorf("power 7 is exactly the quorum: power %d, reached %v", tally.Power(), tally.Reached())
	}
}

// A commit is all or nothing: one vote that does not count (a bad signature, a repeated name, a name
// outside the set) makes it invalid, even when the votes that count reach the quorum. Votes of exactly
// the quorum's power verify; one power below does not.
func TestCommitWithOneBadVoteDoesNotVerify(t *testing.T) {
	signers, validatorSet := weightedValidators(t, 3, 3, 2, 1) // total 9, quorum 7
	outsiders, _ := makeValidators(t, 1)
	blockDigest := digest(0xAB)
	precommitBy := func(signer *signing.Signer) Vote { return mustVote(t, signer, 1, 0, Precommit, blockDigest) }
	first, second, third, fourth := precommitBy(signers[0]), precommitBy(signers[1]), precommitBy(signers[2]), precommitBy(signers[3])
	inAnotherName, _ := SignVote(outsiders[0], Vote{Height: 1, Round: 0, Type: Precommit, BlockHash: blockDigest, Validator: nameOf(signers[3])})
	for name, testCase := range map[string]struct {
		votes []Vote
		valid bool
	}{
		"power 7, exactly the quorum":        {[]Vote{first, second, fourth}, true},
		"power 6, one below the quorum":      {[]Vote{first, second}, false},
		"power 8":                            {[]Vote{first, second, third}, true},
		"power 8 and a bad signature":        {[]Vote{first, second, third, inAnotherName}, false},
		"power 8 and a repeated name":        {[]Vote{first, second, third, first}, false},
		"power 8 and a name outside the set": {[]Vote{first, second, third, precommitBy(outsiders[0])}, false},
	} {
		commit := Commit{Height: 1, BlockHash: blockDigest, Votes: testCase.votes}
		if valid := commit.Verify(scheme, network, validatorSet); valid != testCase.valid {
			t.Errorf("%s: the commit verifies %v, want %v", name, valid, testCase.valid)
		}
	}
}
