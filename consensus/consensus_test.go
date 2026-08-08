package consensus

import (
	"testing"

	"github.com/eval-apply-co-uk/go-ledger/signing"
)

const network = "mainnet"

var scheme = signing.Ed25519{}

func makeValidators(t *testing.T, n int) ([]*signing.Signer, *ValidatorSet) {
	t.Helper()
	signers := make([]*signing.Signer, n)
	vals := make([]Validator, n)
	for i := 0; i < n; i++ {
		s, err := signing.Generate(scheme, network)
		if err != nil {
			t.Fatal(err)
		}
		signers[i] = s
		vals[i] = Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	return signers, NewValidatorSet(vals)
}

func digest(b byte) [32]byte {
	var d [32]byte
	for i := range d {
		d[i] = b
	}
	return d
}

func mustVote(t *testing.T, s *signing.Signer, height int64, round int32, typ VoteType, d [32]byte) Vote {
	t.Helper()
	v, err := SignVote(s, Vote{Height: height, Round: round, Type: typ, BlockDigest: d, Validator: s.PublicKeyBase64()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestQuorumIsTwoThirdsPlusOne(t *testing.T) {
	_, vs := makeValidators(t, 4)
	if got := vs.Quorum(); got != 3 { // floor(8/3)+1 = 3
		t.Fatalf("quorum(4) = %d, want 3", got)
	}
	_, vs7 := makeValidators(t, 7)
	if got := vs7.Quorum(); got != 5 { // floor(14/3)+1 = 5
		t.Fatalf("quorum(7) = %d, want 5", got)
	}
}

func TestVoteSetReachesMajority(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	d := digest(0xAA)
	set := NewVoteSet(vs, 1, 0, Precommit)
	for i := 0; i < 3; i++ { // 3 of 4 == quorum
		if _, err := set.Add(scheme, network, mustVote(t, signers[i], 1, 0, Precommit, d)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	got, ok := set.TwoThirdsMajority()
	if !ok || got != d {
		t.Fatalf("expected majority for digest, ok=%v", ok)
	}
}

func TestVoteSetBelowQuorumNoMajority(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	d := digest(0xAA)
	set := NewVoteSet(vs, 1, 0, Precommit)
	for i := 0; i < 2; i++ { // 2 of 4 < quorum 3
		set.Add(scheme, network, mustVote(t, signers[i], 1, 0, Precommit, d))
	}
	if _, ok := set.TwoThirdsMajority(); ok {
		t.Fatal("2 of 4 must not be a majority")
	}
}

func TestWrongVoteAndNonValidatorRejected(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	set := NewVoteSet(vs, 1, 0, Precommit)
	// wrong round
	if _, err := set.Add(scheme, network, mustVote(t, signers[0], 1, 9, Precommit, digest(1))); err != ErrWrongVote {
		t.Fatalf("want ErrWrongVote, got %v", err)
	}
	// a signer that is not in the validator set
	outsider, _ := signing.Generate(scheme, network)
	if _, err := set.Add(scheme, network, mustVote(t, outsider, 1, 0, Precommit, digest(1))); err != ErrNotValidator {
		t.Fatalf("want ErrNotValidator, got %v", err)
	}
}

func TestEquivocationProducesEvidenceAndUncounts(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	set := NewVoteSet(vs, 1, 0, Prevote)
	dX, dY := digest(0x11), digest(0x22)

	set.Add(scheme, network, mustVote(t, signers[0], 1, 0, Prevote, dX))
	if _, err := set.Add(scheme, network, mustVote(t, signers[0], 1, 0, Prevote, dY)); err != ErrEquivocation {
		t.Fatalf("expected equivocation, got %v", err)
	}
	if len(set.Evidence()) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(set.Evidence()))
	}
	// The equivocator's power must count for neither digest. Add two honest votes for X:
	set.Add(scheme, network, mustVote(t, signers[1], 1, 0, Prevote, dX))
	set.Add(scheme, network, mustVote(t, signers[2], 1, 0, Prevote, dX))
	if _, ok := set.TwoThirdsMajority(); ok {
		t.Fatal("equivocator un-counted: X has power 2 < quorum 3, must not be a majority")
	}
}

func TestCommitVerifies(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	d := digest(0xAB)
	set := NewVoteSet(vs, 5, 0, Precommit)
	for i := 0; i < 3; i++ {
		set.Add(scheme, network, mustVote(t, signers[i], 5, 0, Precommit, d))
	}
	c, ok := set.Commit()
	if !ok {
		t.Fatal("expected a commit")
	}
	if !c.Verify(scheme, network, vs) {
		t.Fatal("commit did not verify")
	}
	if c.Height != 5 || c.BlockDigest != d {
		t.Fatalf("commit height/digest wrong: %d %x", c.Height, c.BlockDigest[:2])
	}
}

func TestCommitBelowQuorumRejected(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	d := digest(0xAB)
	c := Commit{Height: 1, BlockDigest: d, Votes: []Vote{
		mustVote(t, signers[0], 1, 0, Precommit, d),
		mustVote(t, signers[1], 1, 0, Precommit, d),
	}}
	if c.Verify(scheme, network, vs) {
		t.Fatal("a 2-of-4 commit must not verify")
	}
}

func TestCommitDuplicateVoterRejected(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	d := digest(0xAB)
	v0 := mustVote(t, signers[0], 1, 0, Precommit, d)
	c := Commit{Height: 1, BlockDigest: d, Votes: []Vote{v0, v0, v0}} // padded with one voter
	if c.Verify(scheme, network, vs) {
		t.Fatal("a commit padded with duplicate voters must not verify")
	}
}

func TestCommitTamperedVoteRejected(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	d := digest(0xAB)
	votes := []Vote{
		mustVote(t, signers[0], 1, 0, Precommit, d),
		mustVote(t, signers[1], 1, 0, Precommit, d),
		mustVote(t, signers[2], 1, 0, Precommit, d),
	}
	votes[2].BlockDigest = digest(0xCD) // tamper: claim a different digest under the same sig
	c := Commit{Height: 1, BlockDigest: d, Votes: votes}
	if c.Verify(scheme, network, vs) {
		t.Fatal("a commit containing a tampered/mismatched vote must not verify")
	}
}

// A commit must be single-round: mixing precommits from different rounds, or carrying a Round that
// does not match its votes, is not a proof that any one round decided the block, and must be rejected
// — the forged-mixed-round-certificate attack on onboarding/checkpoint sync.
func TestCommitRejectsMixedRounds(t *testing.T) {
	signers, vs := makeValidators(t, 4) // quorum 3
	d := digest(0xAB)

	mixed := Commit{Height: 5, Round: 0, BlockDigest: d, Votes: []Vote{
		mustVote(t, signers[0], 5, 0, Precommit, d),
		mustVote(t, signers[1], 5, 0, Precommit, d),
		mustVote(t, signers[2], 5, 1, Precommit, d), // round 1 — does not belong to a round-0 commit
	}}
	if mixed.Verify(scheme, network, vs) {
		t.Fatal("a commit mixing rounds must not verify")
	}

	wrongRound := Commit{Height: 5, Round: 1, BlockDigest: d, Votes: []Vote{
		mustVote(t, signers[0], 5, 0, Precommit, d),
		mustVote(t, signers[1], 5, 0, Precommit, d),
		mustVote(t, signers[2], 5, 0, Precommit, d),
	}}
	if wrongRound.Verify(scheme, network, vs) {
		t.Fatal("a commit whose Round mismatches its votes must not verify")
	}

	good := Commit{Height: 5, Round: 2, BlockDigest: d, Votes: []Vote{
		mustVote(t, signers[0], 5, 2, Precommit, d),
		mustVote(t, signers[1], 5, 2, Precommit, d),
		mustVote(t, signers[2], 5, 2, Precommit, d),
	}}
	if !good.Verify(scheme, network, vs) {
		t.Fatal("a single-round commit at its matching round should verify")
	}
}

// Safety property (the point of the whole layer): two commits for DIFFERENT digests at the
// same height cannot both be produced honestly, because any two >2/3 quorums intersect -- an
// overlapping validator must have precommitted BOTH digests, i.e. equivocated. This is exactly
// what a >1/2 rule cannot guarantee (two disjoint halves committing conflicting
// blocks under a partition).
func TestConflictingCommitsRequireEquivocation(t *testing.T) {
	signers, vs := makeValidators(t, 4) // quorum 3
	dX, dY := digest(0x01), digest(0x02)

	commitX := Commit{Height: 9, BlockDigest: dX, Votes: []Vote{
		mustVote(t, signers[0], 9, 0, Precommit, dX),
		mustVote(t, signers[1], 9, 0, Precommit, dX),
		mustVote(t, signers[2], 9, 0, Precommit, dX),
	}}
	commitY := Commit{Height: 9, BlockDigest: dY, Votes: []Vote{
		mustVote(t, signers[1], 9, 0, Precommit, dY),
		mustVote(t, signers[2], 9, 0, Precommit, dY),
		mustVote(t, signers[3], 9, 0, Precommit, dY),
	}}
	if !commitX.Verify(scheme, network, vs) || !commitY.Verify(scheme, network, vs) {
		t.Fatal("each commit individually has quorum and should verify")
	}

	overlap := commitOverlap(commitX, commitY)
	if len(overlap) == 0 {
		t.Fatal("two conflicting quorum commits MUST share a validator (quorum intersection)")
	}

	// Feeding both sets of precommits into one vote set surfaces the overlap as slashable
	// equivocation evidence -- the durable proof a single-phase snapshot never produces.
	set := NewVoteSet(vs, 9, 0, Precommit)
	for _, v := range commitX.Votes {
		set.Add(scheme, network, v)
	}
	for _, v := range commitY.Votes {
		set.Add(scheme, network, v)
	}
	if len(set.Evidence()) == 0 {
		t.Fatal("the overlapping double-signers must be caught as equivocation evidence")
	}
}

func commitOverlap(a, b Commit) []string {
	inA := make(map[string]bool)
	for _, v := range a.Votes {
		inA[v.Validator] = true
	}
	var out []string
	for _, v := range b.Votes {
		if inA[v.Validator] {
			out = append(out, v.Validator)
		}
	}
	return out
}
