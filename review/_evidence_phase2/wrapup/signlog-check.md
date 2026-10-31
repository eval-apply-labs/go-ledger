THE LOST-SIGN-LOG WAIT AT EVERY HEIGHT: the independent check (a fresh agent, 2026-10-31)

First check, of 096c8d3. Verdict: safe to merge, no blocker; it closes the three paths of the
hardening finding (catch-up into, commit into, a restart during the wait), the normal path is
unchanged (a node with its sign log keeps the bound at 0), and every wait ends. Two major gaps, both
with small prototyped fixes:
- Major 1: the bound lived only in memory. A node that came back at h, learned during the wait that
  it signed at h+1, started h and signed there, then restarted again, reopened with a log for its
  height and forgot the bound: on 096c8d3 its checks fail with "the proposer signed two blocks for
  height 3 round 0" (and height 4 after a catch-up). Fix: keep the bound in the sign log.
- Major 2: during a wait no driver runs, so onProposal refused any proposal past round 1, and the
  node's own proposal for round 2 or later, which its peers keep re-gossiping when they need it for
  the quorum, was dropped; after the wait it signed a new block for that round ("signed two blocks for
  height 2 round 2"; end to end, X:2 A:1 B:1, 3 of 3 runs on both 096c8d3 and its parent). Fix: while
  no driver runs, the round bound does not apply to the node's own proposals.
Minor: a test that the bound never falls (the one surviving mutant of 20); spec §6.6 and §6.9 wording;
the noteOwnSignature comment; the docs' claims of what the wait recovers, with the remaining gaps (a
node opening two or more heights below the network, a decision precommit at a height the peers have
left, a node at genesis with no snapshot, a round the peers have left); the findings' known-limit text;
and nodes that already wait at their opening height (observers, unseated names, a validator that
adopted at start) now also wait once after a catch-up from it, a delay only.
Verified to hold: the three new tests fail on the parent and pass on the fix; 19 of 20 mutants
killed; safety on the opening, commit-into, catch-up (waiting, running or idle), adopt, idle wake,
restart during a wait, stale timer, forged votes (they do not raise the bound) and replayed genuine
votes (they raise it at most to the highest height the node really signed at); liveness (one timer
per wait, a stale timer dropped, at most one catch-up wait past the bound; end to end, a validator the
quorum needs, back without its log two writes behind, decided 8 of 8 runs with no double-sign, where
the parent never decided within 80 s in 1 of 3); the normal path (42 targeted tests, 5 runs, 210
passes); the docs true but for the items above. Suites: gofmt and vet clean; go test -race ./... and
-C ui all ok.

Recheck of the fix round (9936dd7, 88a92ff, ee5eb8c): started, then stopped for a planned machine
stop before it reported. It is still to do before the branch merges.
