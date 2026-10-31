Recheck of the fix round (9936dd7, 88a92ff, ee5eb8c), a fresh agent after a machine restart
(2026-10-31). Both majors of the first check closed (their tests fail with node/ reverted to 096c8d3
and 9936dd7, pass on HEAD); the normal path unchanged (a node with its log keeps the bound at 0 and
never waits; 46 targeted tests x 5 runs, 230 passes). New findings:
- Major: a node that took back its own proposal and votes from re-gossip during a wait never wrote
  them to its new sign log; after a second, ordinary reboot it found a log for its height, started at
  once and proposed a second block for the same round ("the proposer signed two blocks for height 2
  round 0", and height 4 after a catch-up). Not a regression: the first-wait case fails on 90f4885
  too. Fix: a new sign log is seeded with the node's own votes and proposals it holds for the height.
- Minor: a bound noted while the sign log is below the height in progress (during a commit-into or
  catch-up wait) was kept only in memory; fix: rewrite whenever a log exists.
- Minor: the end-to-end round-2 test never failed (0 in 458 runs, under load and on 2 CPUs) but came
  within 8 ms: the wait is exactly one re-gossip interval while the peers' ticks come at the interval
  plus drift, so a tick can fall just outside it, in production too. Fix: wait two intervals.
- Minor: three surviving mutants (the max in startFirstHeight, the bound restored after emptying a
  lower log, the round exception for any validator); two tests kill them.
- Minor: the round-bound exception accepts any round and the key the name is seated under, not only
  config.Signer; fix: cap at MaxRoundLookahead, and say so.
- Minor: docs (the findings' known-limit text had no dated note; gap lists; "a restart keeps it"
  only once a log is written; the waitForOwnSignatures comment on a catch-up above the bound; TODO's
  test list). Nit: a crafted bound in a log makes the node wait at every height (a delay only).
Suites on HEAD: gofmt and vet clean; go test -race ./... and -C ui all ok.

Final recheck of the second fix round (23ee929, 9b6ca07, 8561571, 5327f48, 8524e16), the same agent:
no blocker and no major; ready to merge. Each fix, reverted alone, fails its new test; the seeding
copies only the node's own votes and proposals for the height in progress, signed under its seated
key, and a node that never lost its log never seeds anything; a lock restored from a taken-back
precommit survives without its block, which costs only liveness; the two-interval wait left at least
589 ms to spare in 30 instrumented runs (the one-interval wait had come within 8 ms), with 82 more
runs under load passing; the cap is 256 rounds with no off-by-one. Nits left: dated records still
say "one re-gossip interval"; under a stolen key the sign log can hold many blocks. Suites: gofmt and
vet clean; go test -race ./... and -C ui ok; 52 targeted node tests x 3, all passing.
