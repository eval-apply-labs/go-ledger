# Phase 3 review 2: bundles, the public folder and pigeon mode (on b9defd2)

Summary: no safety break (imported/pulled/carried messages take the same intake as gossip; nothing here makes a
node sign twice that gossip couldn't). 1 major (liveness), 4 minor, 3 nits. Scratch tests, not committed:
zz_review_test.go, in node/.

B1 major — node/sync.go:37 catchUpCheckpoint: the second path carries votes and proposals, not checkpoints.
Catch-up asks only bootstrap and the gossip peers (http://IP:gossipPort), exactly what is blocked when the second
path is needed; the quickstart sets no bootstrap. A validator 2+ heights behind while gossip is blocked (a
restart, which §7.9 step 2 asks for; a crash; a short CDN outage) never catches up; with 4 banks one more failure
halts the chain. Evidence TestReviewLaggingNodeNeverCatchesUpOnTheSecondPath: no bootstrap: "the others had
committed 3 … 10 s later it has committed 0, the others 9" FAIL; with bootstrap = public addresses "15, the
others 15" PASS. Fix: when bootstrap and gossip peers yield nothing, fall back to the current validators' public
addresses (the fetcher serves status and checkpoint); tried: "14, the others 14". May be limited to while
pullingPublicBundles. Cheaper alternative: bootstrap = public addresses in the quickstart and runbook.

B2 minor — node/node.go:675 waitForOwnSignatures, spec §7.6: in pigeon mode a node back without its sign log
waits only 2 governed re-gossip intervals; its earlier votes/proposal come back only with the next carry, so a
proposer that lost its sign log signs a second block for the round and its name is burned. The spec's reason
("a restart does not wait days") doesn't apply: a restart keeping its sign log never waits. Evidence
TestReviewPigeonNodeWithoutItsSignLogProposesTwice (switch 1 h, step 20 ms; height-3 proposer's .votes deleted):
"before … proposed e14e26f28bd6; after it … d4b0cfeb54e2". Fix: time.AfterFunc(2*max(reGossipInterval(),
MinimumStepTimeout), …) and amend §7.6.

B3 minor — node/node.go:896-905 consensusMessages with the 8 MiB public cut: order is last commit, every held
vote sorted by "name:round:type", then the current proposal; past 8 MiB the cut drops the current proposal first,
then every vote of the validators whose names sort last, current round included. Evidence
TestReviewPublicBundleDropsTheCurrentProposalFirst (21 validators, 50 rounds of nil votes): "2006 votes held;
8385998 bytes, 1828 of 2007 lines; current proposal carried: false; validators with no vote of round 49 carried:
[validator-8 validator-9]"; with two full-block proposals the limit is hit at ~36 rounds; a rogue validator can add
514 non-equivocating future-round votes (2.37 MB) that honest nodes keep and publish. Fix: last commit, the
current round's votes, the proposal, then the other votes, newest rounds first, so the cut drops the oldest.

B4 minor — node/sync.go:154-166 pull into node/node.go:378 (eventVote → countable verifies on the loop): a hostile
public face (the bank's CDN, or anyone on a plain-http path) spends a stuck node's loop time: a forged vote never
kept is re-checked every copy; an 8 MiB bundle packs ~1,800 per GET. Evidence TestReviewHostilePublicBundleCost:
1,829 forged votes 951 ms of loop; 322,637 tiny vote lines 723 ms; ~1 s per pull per tick while stuck (slows, does
not wedge). Related: a loopback PublicAddress makes stuck nodes fetch their own 64 MiB export (= N7). Fix: in the
pull goroutine check each vote off the loop (height, signer in view.validators, signature) and drop the rest of a
public bundle at its first bad line (an honest one never has one); or document the cost in §7.7.

B5 minor — node/publicfolder.go:83, 133 changedAtHeight is the height this node first held the record at, not
"the commit that last changed it": after a catch-up every changed record gets the checkpoint's height, as does
every record a fresh folder writes at Open, so two banks publish different files for one unchanged name (a reader
comparing banks, §6.4, sees a mismatch). Evidence TestReviewChangedAtHeightDiffersBetweenBanks ("A says 3, B
says 9; otherwise equal"). Fix: redefine as "the height this bank first published it at", or drop it (nothing
reads it).

B6 nit — node/publicfolder.go:268 .incoming/next is unsafe if two writers share a folder (misconfiguration).
Evidence TestReviewTwoWritersShareIncomingNext (199 of 200 rounds wrong). Fix: os.CreateTemp(.incoming, "next-*").

B7 nit — node/node.go:436, node/publicfolder.go:92: an idle node marshals and rewrites hdls/bundle every tick
though unchanged (~1 MiB/tick after a full block); Open rewrites every name and key file at every start though it
compared the bytes. Evidence TestReviewIdleNodeRewritesThePublicBundleEveryTick (18 rewrites in 1 s, unchanged).
Fix: skip queueing the bundle when equal to the last queued; at Open skip when bytes equal and the key file exists.

B8 nit — node/secondpath_test.go:186 checks only height ≥ 3 on every node, never the same blocks; "commits in its
first rounds" not asserted. Fix: reuse the pigeon harness's block-hash comparison.

Checked and sound: intake equivalence (same intake, same size bounds, relayed as gossip, duplicates free, another
network refused whole, Admit* newly-pooled correct incl. a replacing certificate); no new double-sign path (own
messages through takesItsOwnBack/noteOwnSignature; votes before the proposal in a bundle); checkpoint import (no
rollback, binds anchor and network, same event as catch-up, same sign-log wait and emptying; verified on the loop,
acceptable on the operator port); the switch (only stepTimeout/armTimeout; config 0..MaxTimingMs; ledger and
consensus depend only on signing and import no time); the writer (one goroutine, coalescing queue, Stop waits;
status after the record entry; key-file removals/re-adds end right; Open removes stale files); the pull (decision on
the loop, one round at a time, only after 4 governed timeouts with a driver, never at its own face, no redirect,
8 MiB, heightStartedAt resets on wake/heartbeat/catch-up, no races); tests stable (pigeon and second path: 20x
-race, 60x -cpu=2 -race with 12 processes, 160x -cpu=1 -race with 80 processes); the second path works without the
switch on real hardware (3 heights in 3.5 s), so the suite's use of the switch is justified under -race.
