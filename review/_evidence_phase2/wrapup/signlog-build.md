THE LOST-SIGN-LOG WAIT AT EVERY HEIGHT: the builder's reports (a fresh agent, 2026-10-31)

The owner, 2026-10-31: "do the fix if you think a fresh pair of eyes would read it better." The
hardening check's low finding (hardening.md, LOW) was built by a fresh agent and checked by another.

First commit, 096c8d3 "the lost-sign-log wait at each height the node may have signed at":
- A new field, mayHaveSignedUpTo, is the highest height the node may have signed at before it lost
  its sign log. startFirstHeight sets it to the height the node comes back at; noteOwnSignature raises
  it when a vote or proposal in the node's own name, for a height above the one it holds, verifies
  under its own key (on vote intake, for votes it cannot count, and in onProposal, for another height).
- waitForOwnSignatures is the opening wait, shared: it retires any driver, clears idle, sets the
  height's validator set and arms one re-gossip-interval timer for that height. It runs at the height
  the node comes back at; after a commit into a height up to the bound (the gap becomes the longer of
  the block interval and the re-gossip interval); and after a catch-up from a height up to the bound.
- Before a height it will wait at, commit and catchUp empty the sign log at the log's own height (as
  adopt does), so a restart during the wait waits too. eventNextHeight carries the height it was armed
  for, and the loop drops a stale one, so a leftover timer cannot cut a wait short.
- Its own votes for later heights are noted, not kept: the wait at that height brings them back
  through that height's checks.
- Tests: TestValidatorBackWithoutItsSignLogWaitsAtTheHeightItCatchesUpTo (three cases),
  ...ResumesItsProposalAfterACatchUp (end to end), ...WaitsAtTheHeightItCommitsInto (two cases), each
  failing on the parent; 11 pieces of the change undone one at a time, each caught.

Fix round, after the independent check (check.md):
- 9936dd7 "the sign log keeps the height a node may have signed up to": the bound is written in the
  sign log (MayHaveSignedUpTo), restored by loadSignLog before it empties a lower log, and rewritten
  when noteOwnSignature raises it for the height in progress. Tests: ...StillWaitsAfterAnotherRestart
  (four cases, all failing on 096c8d3: "restarted once more, the node started height 3 at once: it
  forgot that it may have signed there"), ...NeverLowersItsBound (a guard).
- 88a92ff "during a wait a node takes back its own proposal of any round": in onProposal, while no
  driver runs, the round bound does not apply to the node's own proposals. Tests:
  ...TakesBackItsProposalOfAnyRound (round 2, during the first wait or after a catch-up; on 9936dd7
  "the proposer signed two blocks for height 2 round 2"), ...ResendsItsProposalOfRoundTwo (end to end,
  X:2 A:1 B:1, the quorum needs X; on 9936dd7 "the proposer signed two blocks for height 5 round 2";
  10 of 10 passes under -race).
- ee5eb8c "the docs say exactly what the lost-sign-log wait recovers": spec §6.6 and §6.9,
  docs/signature.md, the runbook, TODO.md and the guide (step 14 runs 35 tests).
Suites on ee5eb8c: gofmt, vet and build clean; go test -race ./... and -C ui each passed twice; the
targeted -count=3 run of ./node/ passed.
