package consensus

import (
	"errors"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// Start and Step are Driver methods that only this package's tests use. The node begins every
// height with Resume.

// Start begins the height at round 0, as Resume does with no votes and no proposals.
func (driver *Driver) Start() []Action { return driver.startRound(0) }

// Step exposes the driver's step.
func (driver *Driver) Step() Step { return driver.step }

// byName returns the signers indexed by their validator names, for picking the proposer /
// specific validators deterministically.
func byName(signers []signing.Signer) map[string]signing.Signer {
	signerByName := make(map[string]signing.Signer, len(signers))
	for _, signer := range signers {
		signerByName[nameOf(signer)] = signer
	}
	return signerByName
}

func findVote(actions []Action, voteType VoteType, round int32) *Vote {
	for _, action := range actions {
		if broadcast, isBroadcast := action.(ActionBroadcastVote); isBroadcast && broadcast.Vote.Type == voteType && broadcast.Vote.Round == round {
			vote := broadcast.Vote
			return &vote
		}
	}
	return nil
}

// findDecision returns the ActionDecided among actions, or nil.
func findDecision(actions []Action) *ActionDecided {
	for _, action := range actions {
		if decision, isDecision := action.(ActionDecided); isDecision {
			return &decision
		}
	}
	return nil
}

func hasProposal(actions []Action) bool {
	for _, action := range actions {
		if _, isProposal := action.(ActionProposal); isProposal {
			return true
		}
	}
	return false
}

func hasSchedule(actions []Action, step Step, round int32) bool {
	for _, action := range actions {
		if schedule, isSchedule := action.(ActionSchedule); isSchedule && schedule.Step == step && schedule.Round == round {
			return true
		}
	}
	return false
}

// A node behind in rounds must skip forward when it sees f+1 (>1/3) voting power from a higher
// round — an honest node is already there, so it is safe to catch up.
func TestDriverSkipsToHigherRoundOnFPlusOne(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4) // total 4 → SkipThreshold = 4/3+1 = 2
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0) // observing at round 0
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.Start()
	if driver.Round() != 0 {
		t.Fatalf("should start at round 0, got %d", driver.Round())
	}

	var others []signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			others = append(others, signer)
		}
	}
	digestX := digest(0x11)
	driver.ReceiveVote(mustVote(t, others[0], height, 2, Prevote, digestX)) // 1 of 4 — below threshold, no skip
	if driver.Round() != 0 {
		t.Fatalf("one higher-round vote should not skip, at round %d", driver.Round())
	}
	driver.ReceiveVote(mustVote(t, others[1], height, 2, Prevote, digestX)) // 2 of 4 — f+1, skip
	if driver.Round() != 2 {
		t.Fatalf("expected skip to round 2 on f+1 higher-round votes, at round %d", driver.Round())
	}
}

// nonProposer returns a signer that is NOT the proposer for any of the given rounds at height.
func nonProposer(validatorSet *ValidatorSet, signerByName map[string]signing.Signer, height int64, rounds ...int32) signing.Signer {
	excluded := map[string]bool{}
	for _, round := range rounds {
		excluded[validatorSet.ProposerAt(height, round)] = true
	}
	for _, validator := range validatorSet.Validators() {
		if !excluded[validator.Name] {
			return signerByName[validator.Name]
		}
	}
	return nil
}

// Happy path: proposer proposes, a quorum prevotes then precommits, the height decides.
func TestDriverHappyPathDecides(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)

	proposerName := validatorSet.ProposerAt(height, 0)
	me := signerByName[proposerName]
	var peers []signing.Signer
	for _, signer := range signers {
		if nameOf(signer) != proposerName {
			peers = append(peers, signer)
		}
	}

	proposedDigest := digest(0x42)
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, proposedDigest)

	actions := driver.Start()
	if !hasProposal(actions) {
		t.Fatal("proposer should emit a proposal on Start")
	}
	if prevote := findVote(actions, Prevote, 0); prevote == nil || prevote.BlockHash != proposedDigest {
		t.Fatal("proposer should prevote its own proposal")
	}

	// Two peer prevotes for proposedDigest -> with the proposer's own, that is quorum (3 of 4) ->
	// precommit.
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, proposedDigest))
	actions = driver.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, proposedDigest))
	if precommit := findVote(actions, Precommit, 0); precommit == nil || precommit.BlockHash != proposedDigest {
		t.Fatal("reaching 2/3 prevotes should make the node lock and precommit dg")
	}

	// Two peer precommits for proposedDigest -> quorum of precommits -> decide.
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, proposedDigest))
	actions = driver.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, proposedDigest))
	if decision := findDecision(actions); decision == nil || decision.Commit.BlockHash != proposedDigest {
		t.Fatal("reaching 2/3 precommits should decide proposedDigest")
	}
}

// A non-proposer with no proposal times out to prevote-nil, precommit-nil, then advances round.
func TestDriverTimeoutAdvancesRound(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)

	me := nonProposer(validatorSet, signerByName, height, 0) // must not be the round-0 proposer, so it arms the timeout
	var peers []signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, signer)
		}
	}

	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	actions := driver.Start()
	if !hasSchedule(actions, StepPropose, 0) {
		t.Fatal("a non-proposer should arm the propose timeout")
	}

	driver.Timeout(StepPropose, 0)
	if driver.Step() != StepPrevote {
		t.Fatal("propose timeout should move to prevote (nil)")
	}

	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, nilBlockHash))
	driver.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, nilBlockHash))
	if driver.Step() != StepPrecommit {
		t.Fatal("2/3 prevote-nil should move to precommit (nil)")
	}

	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, nilBlockHash))
	actions = driver.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, nilBlockHash))
	if !hasSchedule(actions, StepPrecommit, 0) {
		t.Fatal("2/3 precommits should arm the precommit timeout")
	}

	driver.Timeout(StepPrecommit, 0)
	if driver.Round() != 1 {
		t.Fatalf("precommit timeout should advance to round 1, got %d", driver.Round())
	}
}

// G8 (external review): a vote from a far-future round must be dropped WITHOUT allocating per-round
// state — MaxRoundLookahead exists so a rogue validator cannot force a VoteSet allocation per
// arbitrary future round (memory exhaustion). A negative round is rejected outright, and the
// boundary round (exactly current + lookahead) is accepted.
func TestDriverRejectsFarFutureAndNegativeRounds(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0)
	var peer signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peer = signer
			break
		}
	}
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.Start()

	farRound := driver.Round() + MaxRoundLookahead + 1
	if actions := driver.ReceiveVote(mustVote(t, peer, height, farRound, Prevote, digest(0x11))); len(actions) != 0 {
		t.Fatal("a far-future vote must produce no actions")
	}
	if driver.prevotes[farRound] != nil {
		t.Fatal("a far-future vote must not allocate a VoteSet")
	}
	driver.ReceiveVote(mustVote(t, peer, height, -1, Prevote, digest(0x11)))
	if driver.prevotes[-1] != nil {
		t.Fatal("a negative round must not allocate a VoteSet")
	}
	edge := driver.Round() + MaxRoundLookahead
	driver.ReceiveVote(mustVote(t, peer, height, edge, Prevote, digest(0x11)))
	if driver.prevotes[edge] == nil {
		t.Fatal("a vote at exactly the lookahead bound must be accepted")
	}
}

// returnsPromptly runs a driver call and fails the test if it does not return: a driver that keeps
// reporting progress for a vote it could not sign spins forever instead.
func returnsPromptly(t *testing.T, call func() []Action) []Action {
	t.Helper()
	done := make(chan []Action, 1)
	go func() { done <- call() }()
	select {
	case actions := <-done:
		return actions
	case <-time.After(5 * time.Second):
		t.Fatal("the driver call never returned: it spins on a vote it cannot sign")
		return nil
	}
}

// brokenScheme is a test scheme whose keys cannot sign. Its private key is only the public key it
// stands for, so a driver can hold a validator's key and still fail every signature.
type brokenScheme struct{ signing.Scheme } // implements signing.Scheme: the network scheme's methods, with ParsePrivateKey replaced

func (brokenScheme) ParsePrivateKey(publicKey []byte) (signing.PrivateKey, error) {
	return brokenKey(publicKey), nil
}

// brokenKey names a public key and fails every signature.
type brokenKey []byte // implements signing.PrivateKey

func (key brokenKey) PublicKey() []byte { return key }

func (brokenKey) Sign([]byte) ([]byte, error) { return nil, errors.New("this key cannot sign") }

// G7 (external review): the castVote signing-error branch. A driver whose signer cannot sign must
// drop the vote quietly — no panic, no broadcast, the vote not marked sent, the step not advanced,
// and the call returns — on every path that casts one: the proposer prevoting its own proposal on
// Start, a prevote on a received proposal, and the propose timeout, which arms itself again so the
// vote is retried. The signer's key comes from brokenScheme, whose Sign always fails.
func TestDriverBrokenSignerDropsVoteQuietly(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	broken := func(signer signing.Signer) *Driver {
		brokenSigner, err := signing.NewSigner(brokenScheme{signing.NetworkScheme()}, network, signer.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		return NewDriver(brokenSigner, nameOf(signer), validatorSet, scheme, network, height, digest(0x01))
	}
	dropped := func(driver *Driver, actions []Action) {
		t.Helper()
		if findVote(actions, Prevote, 0) != nil {
			t.Fatal("a broken signer must not broadcast a vote")
		}
		if driver.sentPrevote[0] {
			t.Fatal("the failed vote must not be marked sent")
		}
		if driver.Step() != StepPropose {
			t.Fatal("the failed transition must not advance the step")
		}
	}

	proposer := broken(signerByName[validatorSet.ProposerAt(height, 0)])
	actions := returnsPromptly(t, proposer.Start)
	dropped(proposer, actions)
	if !hasProposal(actions) || !hasSchedule(actions, StepPropose, 0) {
		t.Fatal("the proposer must still propose, and arm the propose timeout that retries its prevote")
	}

	driver := broken(nonProposer(validatorSet, signerByName, height, 0))
	driver.Start() // non-proposer: arms the propose timeout, nothing signed yet
	actions = returnsPromptly(t, func() []Action {
		return driver.ReceiveProposal(Proposal{Round: 0, BlockHash: digest(0x02), ValidRound: -1})
	})
	dropped(driver, actions)

	actions = driver.Timeout(StepPropose, 0) // would prevote nil — the signature fails instead
	dropped(driver, actions)
	if !hasSchedule(actions, StepPropose, 0) {
		t.Fatal("a timeout whose vote could not be signed must arm itself again")
	}
}

// A transient sign-log failure (a full disk, say) must not lose the vote: nothing is counted or sent
// while the log refuses it, and the next event retries it once the log accepts it.
func TestDriverRetriesVoteAfterSignLogFailure(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0)
	var peer signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peer = signer
			break
		}
	}
	full, logged := true, 0
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.SetSignLog(func(Vote, Proposal) error {
		if full {
			return errors.New("disk full")
		}
		logged++
		return nil
	})
	driver.Start()

	digestX := digest(0x11)
	actions := returnsPromptly(t, func() []Action {
		return driver.ReceiveProposal(Proposal{Round: 0, BlockHash: digestX, ValidRound: -1})
	})
	if findVote(actions, Prevote, 0) != nil || driver.sentPrevote[0] {
		t.Fatal("a vote the sign log refused must be neither sent nor marked sent")
	}
	full = false
	actions = driver.ReceiveVote(mustVote(t, peer, height, 0, Prevote, digestX))
	if prevote := findVote(actions, Prevote, 0); prevote == nil || prevote.BlockHash != digestX || logged != 1 {
		t.Fatal("once the sign log accepts it, the prevote must be logged and sent")
	}
}

// The nil precommit is cast by the same quorum of nil prevotes that arms the prevote timeout, and once
// a height's votes are in, no new one may come to retry it. So if the sign log refuses it, the
// driver must arm the prevote timeout, and the timeout retries the precommit.
func TestDriverRetriesNilPrecommitAfterSignLogFailure(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0)
	var peers []signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, signer)
		}
	}
	full := false
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.SetSignLog(func(Vote, Proposal) error {
		if full {
			return errors.New("disk full")
		}
		return nil
	})
	driver.Start()
	driver.Timeout(StepPropose, 0) // the proposer is down: prevote nil

	full = true
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, nilBlockHash))
	actions := returnsPromptly(t, func() []Action {
		return driver.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, nilBlockHash))
	})
	if findVote(actions, Precommit, 0) != nil || driver.sentPrecommit[0] {
		t.Fatal("a precommit the sign log refused must be neither sent nor marked sent")
	}
	if !hasSchedule(actions, StepPrevote, 0) {
		t.Fatal("with its nil precommit refused, the driver must arm the prevote timeout that retries it")
	}
	full = false
	if precommit := findVote(driver.Timeout(StepPrevote, 0), Precommit, 0); precommit == nil || precommit.BlockHash != nilBlockHash {
		t.Fatal("once the sign log accepts it, the prevote timeout must cast the nil precommit")
	}
}

// The precommit of a value with > 2/3 prevotes is cast by a one-time rule. If the sign log refuses it,
// the rule must consume nothing and lock nothing, so the next event casts it once the log accepts it,
// instead of the round going to the prevote timeout's nil precommit.
func TestDriverRetriesLockedPrecommitAfterSignLogFailure(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0)
	var peers []signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, signer)
		}
	}
	full := false
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.SetSignLog(func(Vote, Proposal) error {
		if full {
			return errors.New("disk full")
		}
		return nil
	})
	driver.Start()
	digestX := digest(0x11)
	if findVote(driver.ReceiveProposal(Proposal{Round: 0, BlockHash: digestX, ValidRound: -1}), Prevote, 0) == nil {
		t.Fatal("the driver must prevote the proposal")
	}

	full = true
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, digestX))
	actions := returnsPromptly(t, func() []Action {
		return driver.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, digestX)) // > 2/3 for X
	})
	if findVote(actions, Precommit, 0) != nil || driver.sentPrecommit[0] || driver.lockedValue != nil {
		t.Fatal("a precommit the sign log refused must be neither sent, nor marked sent, nor locked")
	}
	if !hasSchedule(actions, StepPrevote, 0) {
		t.Fatal("with its precommit refused, the driver must still arm the prevote timeout")
	}

	full = false
	actions = driver.ReceiveVote(mustVote(t, peers[2], height, 0, Prevote, digestX)) // any next event
	if precommit := findVote(actions, Precommit, 0); precommit == nil || precommit.BlockHash != digestX {
		t.Fatal("once the sign log accepts it, the next event must cast the precommit of X")
	}
	if driver.lockedValue == nil || *driver.lockedValue != digestX || driver.lockedRound != 0 {
		t.Fatal("the precommit of X must lock X")
	}
}

// Resume re-enters a height from the node's logged votes: they count as sent, so nothing is signed
// again for a round and type it already voted in — not even a proposal by the round's proposer —
// the lock of its precommit is back, and it resumes at the latest round it voted in.
func TestDriverResumeKeepsOwnVotesAndLock(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := signerByName[validatorSet.ProposerAt(height, 1)]
	digestX := digest(0x11)
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	actions := driver.Resume([]Vote{
		mustVote(t, me, height, 0, Prevote, digestX),
		mustVote(t, me, height, 0, Precommit, digestX),
		mustVote(t, me, height, 1, Prevote, nilBlockHash),
	}, nil)
	if hasProposal(actions) || findVote(actions, Prevote, 1) != nil {
		t.Fatal("a resumed driver must not propose or vote again in a round it already voted in")
	}
	if driver.Round() != 1 || driver.Step() != StepPrevote {
		t.Fatalf("expected to resume at round 1 after its prevote, got round %d step %d", driver.Round(), driver.Step())
	}
	if driver.lockedValue == nil || *driver.lockedValue != digestX || driver.lockedRound != 0 {
		t.Fatal("the lock of the round-0 precommit must be restored")
	}
	if actions := driver.ReceiveProposal(Proposal{Round: 1, BlockHash: digest(0x22), ValidRound: -1}); findVote(actions, Prevote, 1) != nil {
		t.Fatal("a proposal for a round it already prevoted in must not draw a second prevote")
	}
}

// A validator back without its sign log gets its own earlier votes back from peers' re-gossip. A vote
// of its own that the driver receives counts as cast: it signs no second, different vote for that
// round and type, and its precommit of a value is its lock.
func TestDriverCountsItsOwnReGossipedVoteAsCast(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0, 1)
	var peers []signing.Signer
	for _, signer := range signers {
		if signer != me {
			peers = append(peers, signer)
		}
	}
	digestX := digest(0x11)
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.Start()
	forged := mustVote(t, me, height, 0, Prevote, digestX)
	forged.Signature = mustVote(t, me, height, 0, Prevote, digest(0x33)).Signature
	if driver.ReceiveVote(forged); driver.sentPrevote[0] {
		t.Fatal("a vote in its name that it did not sign must not count as cast")
	}
	driver.ReceiveVote(mustVote(t, me, height, 0, Prevote, digestX))
	driver.ReceiveVote(mustVote(t, me, height, 0, Precommit, digestX))
	actions := driver.Timeout(StepPropose, 0) // would prevote nil
	for _, peer := range peers {
		actions = append(actions, driver.ReceiveVote(mustVote(t, peer, height, 0, Prevote, nilBlockHash))...) // would precommit nil
	}
	actions = append(actions, driver.Timeout(StepPrevote, 0)...)
	if findVote(actions, Prevote, 0) != nil || findVote(actions, Precommit, 0) != nil {
		t.Fatal("the driver signed a second vote for a round and type it had voted in")
	}

	for _, peer := range peers {
		driver.ReceiveVote(mustVote(t, peer, height, 0, Precommit, nilBlockHash))
	}
	driver.Timeout(StepPrecommit, 0)
	actions = driver.ReceiveProposal(Proposal{Round: 1, BlockHash: digest(0x22), ValidRound: -1})
	if prevote := findVote(actions, Prevote, 1); prevote == nil || prevote.BlockHash != nilBlockHash {
		t.Fatal("locked by its own precommit, the driver must prevote nil for another block without a proof-of-lock")
	}
}

// A proposal the driver holds for a round it proposes in is its own, signed before a restart (kept in
// its sign log, or re-gossiped back to it by peers). Reaching that round, it sends that proposal again:
// proposing the block it would propose now instead would sign two blocks for one round.
func TestDriverResendsTheProposalItHolds(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := signerByName[validatorSet.ProposerAt(height, 1)]
	var peers []signing.Signer
	for _, signer := range signers {
		if signer != me {
			peers = append(peers, signer)
		}
	}
	digestX := digest(0x11)
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.Start()
	driver.ReceiveProposal(Proposal{Round: 1, BlockHash: digestX, ValidRound: -1}) // its own, re-gossiped back
	driver.ReceiveVote(mustVote(t, peers[0], height, 1, Prevote, digestX))
	actions := driver.ReceiveVote(mustVote(t, peers[1], height, 1, Prevote, digestX)) // f+1 at round 1: it skips there
	for _, action := range actions {
		if proposal, isProposal := action.(ActionProposal); isProposal && proposal.Round == 1 && proposal.BlockHash != digestX {
			t.Fatal("the driver proposed a new block for a round whose proposal it holds")
		}
	}
	if !hasProposal(actions) {
		t.Fatal("the driver did not send again the proposal it holds for its round")
	}
}

// G6 (external review): the POL/unlock release path — the complement of
// TestDriverLockingPreventsConflictingPrevote below. A node locked on digestX MUST prevote a different
// value digestY when the round's proposal carries a proof-of-lock from a round vr >= its lockedRound
// (Tendermint lines 28–29: a ⅔ prevote quorum at vr proves the network moved past the lock).
// Without this release, two locked camps could stall a height forever.
func TestDriverPOLUnlockReleasesLock(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)
	me := nonProposer(validatorSet, signerByName, height, 0, 1, 2) // receives injected proposals in rounds 0..2
	var peers []signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, signer)
		}
	}
	digestX, digestY := digest(0x11), digest(0x22)
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x00))
	driver.Start()

	// Round 0: me locks digestX (proposal digestX + 2/3 prevotes) and precommits it.
	driver.ReceiveProposal(Proposal{Round: 0, BlockHash: digestX, ValidRound: -1})
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, digestX))
	driver.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, digestX))
	if driver.lockedValue == nil || *driver.lockedValue != digestX {
		t.Fatal("me should be locked on X after 2/3 prevotes for X")
	}
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, nilBlockHash))
	driver.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, nilBlockHash))
	driver.Timeout(StepPrecommit, 0)

	// Round 1: all three peers prevote digestY — a full ⅔ proof-of-lock for digestY at round 1,
	// which me witnesses but does not join (locked on digestX with no acceptable proposal, it times
	// out to nil).
	for _, peer := range peers {
		driver.ReceiveVote(mustVote(t, peer, height, 1, Prevote, digestY))
	}
	driver.Timeout(StepPropose, 1) // me prevotes nil (locked on digestX)
	driver.Timeout(StepPrevote, 1) // me precommits nil
	driver.ReceiveVote(mustVote(t, peers[0], height, 1, Precommit, nilBlockHash))
	driver.ReceiveVote(mustVote(t, peers[1], height, 1, Precommit, nilBlockHash))
	driver.Timeout(StepPrecommit, 1)
	if driver.Round() != 2 {
		t.Fatalf("expected round 2, got %d", driver.Round())
	}

	// Round 2: proposal digestY arrives carrying the round-1 POL. vr=1 >= lockedRound=0, and me
	// holds the round-1 prevote quorum proving it — the lock must release and me must prevote
	// digestY, not nil.
	actions := driver.ReceiveProposal(Proposal{Round: 2, BlockHash: digestY, ValidRound: 1})
	prevote := findVote(actions, Prevote, 2)
	if prevote == nil {
		t.Fatal("expected a round-2 prevote")
	}
	if prevote.BlockHash != digestY {
		t.Fatalf("a POL at vr=1 >= lockedRound=0 must release the lock: prevoted %x, want Y", prevote.BlockHash[:4])
	}
}

// Safety: once a node has locked value digestX, it must NOT prevote a conflicting value digestY in
// a later round without a proof-of-lock -- it prevotes nil instead. This is the locking rule a
// single-phase snapshot cannot provide.
func TestDriverLockingPreventsConflictingPrevote(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	signerByName := byName(signers)
	const height = int64(1)

	me := nonProposer(validatorSet, signerByName, height, 0, 1) // must not propose in round 0 or 1 (it receives injected proposals)
	var peers []signing.Signer
	for _, signer := range signers {
		if signer.PublicKeyBase64() != me.PublicKeyBase64() {
			peers = append(peers, signer)
		}
	}
	digestX, digestY := digest(0x11), digest(0x22)

	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x00))
	driver.Start()

	// Round 0: proposal digestX arrives; me prevotes digestX; two peers prevote digestX -> me locks
	// digestX, precommits digestX.
	driver.ReceiveProposal(Proposal{Round: 0, BlockHash: digestX, ValidRound: -1})
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Prevote, digestX))
	driver.ReceiveVote(mustVote(t, peers[1], height, 0, Prevote, digestX))
	if driver.lockedValue == nil || *driver.lockedValue != digestX {
		t.Fatal("me should be locked on X after 2/3 prevotes for X")
	}

	// Drive to round 1: gather 2/3 precommits (me=digestX, two peers=nil) then fire the
	// precommit timeout.
	driver.ReceiveVote(mustVote(t, peers[0], height, 0, Precommit, nilBlockHash))
	driver.ReceiveVote(mustVote(t, peers[1], height, 0, Precommit, nilBlockHash))
	driver.Timeout(StepPrecommit, 0)
	if driver.Round() != 1 {
		t.Fatalf("expected round 1, got %d", driver.Round())
	}

	// Round 1: a fresh proposal for a DIFFERENT value digestY (no POL). me is locked on digestX.
	actions := driver.ReceiveProposal(Proposal{Round: 1, BlockHash: digestY, ValidRound: -1})
	prevote := findVote(actions, Prevote, 1)
	if prevote == nil {
		t.Fatal("expected a round-1 prevote")
	}
	if prevote.BlockHash != nilBlockHash {
		t.Fatal("a node locked on X must prevote nil for conflicting Y, not Y")
	}
}

// A node can hold a quorum of precommits for the proposed block before it precommits itself, and that
// quorum can count a double-signer whose conflicting precommit it has not seen yet. A node that has
// seen the conflict counts the double-signer out and needs an honest precommit in its place: the
// decider's. So the decider precommits the block it decides, and its commit carries that vote.
func TestDriverPrecommitsTheBlockItDecides(t *testing.T) {
	signers, validatorSet := makeValidators(t, 5) // quorum 4 of 5: with the double-signer counted out, all 4 honest
	const height = int64(1)
	var byzantine, decider, other signing.Signer
	var honest []signing.Signer
	for _, signer := range signers {
		switch {
		case nameOf(signer) != validatorSet.ProposerAt(height, 0) && byzantine == nil:
			byzantine = signer
			continue
		case nameOf(signer) != validatorSet.ProposerAt(height, 0) && decider == nil:
			decider = signer
		case nameOf(signer) != validatorSet.ProposerAt(height, 0) && other == nil:
			other = signer
		}
		honest = append(honest, signer)
	}
	block := digest(0x42)
	startDriver := func(signer signing.Signer) *Driver {
		driver := NewDriver(signer, nameOf(signer), validatorSet, scheme, network, height, digest(0x01))
		driver.Start()
		driver.ReceiveProposal(Proposal{Round: 0, BlockHash: block, ValidRound: -1})
		return driver
	}

	deciderDriver := startDriver(decider) // it has prevoted the block, and holds no prevote quorum
	var sent []Vote
	for _, signer := range append([]signing.Signer{byzantine}, honest...) {
		if signer == decider {
			continue
		}
		for _, item := range deciderDriver.ReceiveVote(mustVote(t, signer, height, 0, Precommit, block)) {
			switch action := item.(type) {
			case ActionBroadcastVote:
				sent = append(sent, action.Vote)
			case ActionDecided:
				sent = append(sent, action.Commit.Votes...)
			}
		}
	}
	if !deciderDriver.decided {
		t.Fatal("premise: the double-signer's precommit and three honest ones decide the block")
	}

	otherDriver := startDriver(other) // saw the double-signer's other precommit first
	otherDriver.ReceiveVote(mustVote(t, byzantine, height, 0, Precommit, digest(0x66)))
	for _, vote := range sent {
		otherDriver.ReceiveVote(vote)
	}
	if !otherDriver.decided {
		t.Fatal("a node that counts the double-signer out could not complete the quorum the decider saw")
	}
}

// A driver with no signer, for a node whose name is not seated with its key (spec 6.3), follows the
// rounds and decides, but never signs: as the round's proposer it proposes nothing, it casts no vote,
// and a timeout whose vote it would cast moves the round on instead of arming itself again.
func TestDriverWithoutSignerNeverSigns(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	const height = int64(1)
	self := validatorSet.ProposerAt(height, 0)
	var others []signing.Signer
	for _, signer := range signers {
		if nameOf(signer) != self {
			others = append(others, signer)
		}
	}
	driver := NewDriver(nil, self, validatorSet, scheme, network, height, digest(0x01))
	var actions []Action
	actions = append(actions, driver.Start()...)
	timeout := driver.Timeout(StepPropose, 0) // it would prevote nil
	if hasSchedule(timeout, StepPropose, 0) || driver.Step() != StepPrevote {
		t.Fatal("the propose timeout armed itself again instead of moving the round on")
	}
	actions = append(actions, timeout...)
	for _, voteType := range []VoteType{Prevote, Precommit} { // the others give up on round 0
		for _, other := range others {
			actions = append(actions, driver.ReceiveVote(mustVote(t, other, height, 0, voteType, nilBlockHash))...)
		}
	}
	actions = append(actions, driver.Timeout(StepPrecommit, 0)...)
	actions = append(actions, driver.ReceiveProposal(Proposal{Round: 1, BlockHash: digest(0x02), ValidRound: -1})...)
	for _, voteType := range []VoteType{Prevote, Precommit} { // and decide round 1's block
		for _, other := range others {
			actions = append(actions, driver.ReceiveVote(mustVote(t, other, height, 1, voteType, digest(0x02)))...)
		}
	}
	if decision := findDecision(actions); decision == nil || decision.Commit.BlockHash != digest(0x02) {
		t.Fatal("the driver without a signer did not decide the block the others decided")
	}
	for _, action := range actions {
		switch action.(type) {
		case ActionProposal, ActionBroadcastVote:
			t.Fatalf("the driver without a signer asked to send %T", action)
		}
	}
}

// countingScheme is the network's scheme, counting its signature checks.
type countingScheme struct { // implements signing.Scheme: the network scheme's methods, with Verify replaced
	signing.Scheme
	checks *int
}

func (scheme countingScheme) Verify(publicKey, message, signature []byte) bool {
	*scheme.checks++
	return scheme.Scheme.Verify(publicKey, message, signature)
}

// The vote set that counts a vote checks its signature, and the driver takes that check's word: a vote
// of its own, re-gossiped back to it, costs one signature check, not two, and counts as cast.
func TestDriverChecksItsOwnReGossipedVoteOnce(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	const height = int64(1)
	me := nonProposer(validatorSet, byName(signers), height, 0)
	checks := 0
	driver := NewDriver(me, nameOf(me), validatorSet, countingScheme{Scheme: signing.NetworkScheme(), checks: &checks}, network, height, digest(0x01))
	driver.Start()
	driver.ReceiveVote(mustVote(t, me, height, 0, Prevote, digest(0x11)))
	if checks != 1 || !driver.sentPrevote[0] {
		t.Fatalf("its own vote cost %d signature checks, want 1; counted as cast: %v", checks, driver.sentPrevote[0])
	}
}

// Resume, like ReceiveVote, takes as cast only a vote in its name that verifies under the key the set
// holds for it: a vote it did not sign does not stop it from voting in that round.
func TestDriverResumeSkipsAVoteThatDoesNotVerify(t *testing.T) {
	signers, validatorSet := makeValidators(t, 4)
	const height = int64(1)
	me := nonProposer(validatorSet, byName(signers), height, 0)
	digestX := digest(0x11)
	forged := mustVote(t, me, height, 0, Prevote, digestX)
	forged.Signature = mustVote(t, me, height, 0, Prevote, digest(0x33)).Signature
	driver := NewDriver(me, nameOf(me), validatorSet, scheme, network, height, digest(0x01))
	driver.Resume([]Vote{forged}, nil)
	actions := driver.ReceiveProposal(Proposal{Round: 0, BlockHash: digestX, ValidRound: -1})
	if prevote := findVote(actions, Prevote, 0); prevote == nil || prevote.BlockHash != digestX {
		t.Fatal("a vote in its name that does not verify kept the resumed driver from prevoting the proposal")
	}
}
