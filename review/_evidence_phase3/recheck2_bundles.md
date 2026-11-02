# Recheck of B1–B8 at b721caa (reviewer 2)

All eight fixed; B4 only for votes (the same attack works through proposal and certificate lines of a pulled
bundle). Two new minors (one from b721caa, one a cost of the B4 fix), two nits. No regressions; no fix breaks
safety. Root suite with -race passes on b721caa (node 133 s); the 25 timing-sensitive tests in the area passed 64
times each under -race (16 processes, -cpu=2, -count=4). Scratch file zz_review_test.go, not committed, updated for
allowLoopbackPublicAddresses and the removed changedAtHeight.

- B1 fixed: no bootstrap, "late validator … 10 s later it has committed 13, the others 13" (was 0 vs 9);
  TestValidatorBehindCatchesUpThroughThePublicAddresses 64/64.
- B2 fixed: no second proposal within 300 ms (was another block after 40 ms); ownSignaturesWait at node.go:697; the
  driver never re-proposes a round it already prevoted in (consensus/driver.go:256).
- B3 fixed: 21 validators, 50 rounds: "current proposal carried: true; validators with no vote of round 49
  carried: []".
- B4 partly: votes: 8 MiB of forged votes 3 ms (was 951 ms), tiny votes 17 ms (was 723 ms); proposals and
  certificates not covered (R2-1). The vote check passes honest bundles at the same height, one height ahead (stops at
  the next-height votes, ~4 step timeouts slower per height, not stalled), and across a set or key change
  (lastCommitValidators set before publishView in commit and fastForward; nil only at genesis).
- B5 fixed (identical bytes). B6 fixed (0 of 200 wrong; Open's RemoveAll(.incoming) can cut a co-tenant's in-flight
  write: logged, redone at the next change; acceptable for a misconfiguration). B7 fixed (0 rewrites idle; a failed
  hdls/bundle write is retried only when the content next changes). B8 fixed.

New:
- R2-1 minor — node/bundle.go:179 (proposals), :196 (certificates) in a pulled bundle: a hostile face still spends a
  stuck node's time. Evidence TestReviewHostilePulledBundleCostAfterTheFix (8 MiB): 1,610 forged proposals 844 ms of
  loop; 279,619 `{"type":"proposal","body":{}}` 980 ms; 233,015 tiny parameter-vote lines 5.0 s; 215,091
  reassignment-vote lines 2.9 s (also holding back honest pulls). Fix: check pulled proposals off the loop
  (ProposerAt(height, round) of view.validators, or of lastCommitValidators for the last height; verify), stop at the
  first failure; in the pulled import also stop at the first unreadable or refused line.
- R2-2 minor — node/sync.go:59 (b721caa's condition): an idle validator runs no driver, so is never "stuck"; if a
  peer answers at its own height (on its side of a split, as far behind), catch-up skips the public faces until the
  heartbeat starts a driver and 4 step timeouts pass: up to ~5 min, repeating each time it idles after catching up.
  Evidence TestReviewIdleLaggingNodeWithAnAnsweringPeerWaitsForItsHeartbeat (15 s test heartbeat over 35 s): answering
  peer: 2+ heights behind for 24.25 s, longest 13 s; silent peer: 9.25 s, longest 2.25 s. Fix: when no answering peer
  is ahead and the node isn't stuck, still ask one other validator's public-face status per tick, round-robin.
- R2-3 nit — node/node.go:930-944 B3's order: "newest round first" puts future rounds before past ones, so a rogue
  validator's up to 514 future-round votes (~2.4 MB) come before the past rounds holding the ValidRound prevotes.
  Fix: past rounds before future ones, or order by distance from the current round.
- R2-4 minor (cost) — node/sync.go:263 pulledVoteCheck verifies every vote of every pulled bundle at every pull, held
  ones included; a hostile face can replay one real vote to the bound (~0.9 CPU-s per pull). ~0.51 ms per vote;
  1,200 honest votes 614 ms per bundle per pull; 21 banks at cap ~18 CPU-s per round while stuck. Fix: remember the
  (validator, signature) pairs verified at the current height, reset when the height changes (~8 lines).
- Side note nit — refuseLoopback refuses only loopback and unspecified; in the quickstart every engine's operator port
  listens on :8080 on all interfaces and the engines' IPs are on chain, so a validator publishing
  http://172.28.70.11:8080 makes every stuck engine fetch bank-a's full export, built on bank-a's loop. Fix: refuse
  the node's own interface addresses, or bind the quickstart's operator port to the address only its fetcher uses.

## Final recheck at ff4d853 (R2-1..R2-4)

All four fixed, right and small, no regression, no new finding (two optional nits). Root suite -race ok; the 28
timing-sensitive tests in the area passed 64 times each under -race (16 processes, -cpu=2, -count=4).
- R2-1 fixed: 8 MiB hostile bundles through the pull's import: forged proposals 844 ms → 3 ms; tiny proposal lines
  980 → 20 ms; tiny parameter-vote lines 5.0 s → 15 ms (1 refused); tiny reassignment-vote lines 2.9 s → 10 ms;
  forged votes 3 ms; tiny votes 17 → 14 ms. Can't stop an honest bundle realistically: votes and proposals are never
  "refused" (a proposal of a passed round passes the check, is queued and dropped on the loop); a refused certificate
  cuts only those after it; fromItsProposer uses onProposal's set or lastCommitValidators (nil at genesis → false, and
  no such proposal exists there). Nit: with the puller's certificate pool full, a new honest parameter certificate is
  refused at every pull and the name-action certificates after it never come by this path; acceptable.
- R2-2 fixed: idle node with an answering peer (15 s heartbeat, 35 s): 2+ behind 24.25 s → 5.25 s, longest 13 s →
  1.75 s, most 8 → 3 heights (silent-peer control 5.5 s / 1.75 s / 3). The turn cycles every validator with a public
  address: a lag found within ~N−1 ticks; one ~100-byte GET a tick.
- R2-3 fixed: current round 4 and a rogue's prevotes for 5..260 give [4, proposal, 3, 2, 1, 0, 5, 6, 7, …]; the
  21-validator, 50-round case still carries the current proposal and every current-round vote.
- R2-4 fixed: 1,200 honest votes 538 ms first pull, 2.5 ms second; no poisoning (only verified votes, whole-vote
  key; a copied signature on another hash refused); cap holds (6,200 distinct valid votes → 2,104 held after the
  reset at 4,096). Nit: a rogue signing > 4,096 distinct votes a height can keep resetting it (CPU only, off the loop).
