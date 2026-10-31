# Phase 2 build: progress and how to resume

Phase 2 is built in slices, as `review/phase2_spec.md` §10 plans. The build paused on 2026-10-28
after S4, at the owner's request, and resumed on 2026-10-30 with S5 and S8, then S7 on its own,
then group 6 (S6 and S9), then S10 (the idle chain), then S7b (the record in units of 100, named
by network), then S11 (the docs, in four areas), then S12 (a guide to reading the code, and the
last read-through of the code). Every slice is done. S7b, S11 and S12 were added or reshaped by the
owner on 2026-10-30. On 2026-10-31 every one of S12's 75 code notes was fixed, in nine runs (see
"Fixes from the code notes"), and a hand-off pass checked the spec, the docs and the code against
each other (see "The wrap-up", at the end). Nothing is pushed, and `post-emudhra-phase1` has not
changed since the rename (`3df90aa`). The words and ids this note uses are in
[`docs/glossary.md`](../docs/glossary.md).

## The machine change of 2026-10-30

On 2026-10-30 the owner removed the graphics cards and most of the RAM. The machine went from
251 GiB to 64 GB (62 GiB shown), with no GPU, and kept its 56 CPUs. After the reboot the check list
passed on `3891341`.

- Memory is no constraint. Measured on `8186d80`: one `go test -race -count=1 ./...` peaks at
  about 0.5 GiB, summed over `go test` and the test binaries it starts, in 60 s; the ui suite peaks
  at about 0.2 GiB, in 32 s. Nothing in go-ledger, its tests or the build uses a GPU.
- The CPU count is unchanged. It sets how many test packages `go test` runs at once, the
  workflow's limit of 16 agents at a time, and the timing the chaos tests rely on.
- `/tmp` is emptied at every boot (`D /tmp` in `/usr/lib/tmpfiles.d/tmp.conf`), and the build
  session's scratchpad with it. The evidence is committed in `review/_evidence_2026-10-27/` and
  `review/_evidence_phase2/`, and the workflow scripts live under `~/.claude/projects/`. To resume
  the build session after a reboot: `claude --resume <session>`, from
  the repository folder. A new session works too: its memory points at this file.

## Where the branch stands

`post-emudhra-phase2` holds the spec, every code slice (S1 to S10, and S7b), the owner's freeze
window, the docs in four areas (S11), the guide to reading the code (S12), and the fixes of all 75
code notes in `review/phase2_code_notes.md` (2026-10-31). Every slice and every fix run passed the
whole check list on its merged tree. The checks are listed under "How to resume".

Then the wrap-up began, on 2026-10-31:

- `make build` builds static binaries, as the README's tagline says (`93f5fc5`);
- `docs/wallet.png` and `docs/admin.png` are retaken from the demo, and the README shows them again
  (`b01178e`);
- the orchestrator decided the open points from S10 and S11, at the owner's request (`320dc62`;
  spec §12);
- a hand-off pass checked the spec, the docs and the code against each other, fixed what it
  found, and ended with a recheck, a cold read by a newcomer agent and a hardening check. "The
  wrap-up", at the end of this note, records it, with the code it changed.

Before the push, the owner:

- reads the docs, starting from `docs/README.md` and `docs/reading-the-code.md`;
- reviews the fixes to the code notes: each note has a Status column and a Fixed line, and the
  section "Fixes from the code notes" below lists the runs;
- looks at the choices the fix runs made that change behaviour or shape (details below):
  - C34: `CertificateTally` holds the ⅔ rule in one place but is about 22 lines longer;
  - C52: `Resume` counts an own vote as cast only if it verifies (same in production);
  - C10: `adopt` empties the sign log at its own height, keeping the lost-log wait;
  - C8: a refused parameter change gets one sentence listing the possible causes;
  - C36: a validator takes its gossip peers as it opens, not at block 1;
  - C39: the request pool checks a slot after the fees, and logs a full pool's refusals;
  - C54: cmd/server checks the seed's length only after the genesis file;
  - C42: two moves beyond the note (`selfConsistent` and `fastForward` to persist.go, `AdmitUpdate`
    to updatepool.go);
  - one phrase in `findings_20261027.md`'s Phase 2 note now says the step timeout in force;
- checks the choices the orchestrator made at the owner's request: on the points from S10 and S11
  (below, "For the owner, from S10" and "For the owner, from S11"; spec §12), and in the wrap-up:
  the payment counter and the freeze now count from the verified state (UI-1, and the hardening
  check's freeze), `cmd/server` binds its port before it opens the node (SIG-1), a restart empties a
  stale sign log (SIG-10), and a refused precommit is retried (CON-4); and the lost-sign-log wait
  at every height, built after the wrap-up at the owner's go-ahead (see "The wrap-up").

The local paths the review record used to name (the build session's scratchpad and folders, which
carried the machine's user name) were scrubbed from the whole unpushed history on 2026-10-31, at the
owner's request: see "The scrub", at the end.

## Fixes from the code notes

The owner, 2026-10-31: "fix C1 to C6 first", then "do C7 to C31 after … if you still have time do
progress with the rest of the fixes too". Their choices:
- C21: the timings follow the vote;
- C34: one shared helper holds the ⅔ rule;
- C40 to C43: the file moves are done last;
- C74: `/hdls/status`'s key becomes `heightInProgress`.

Each run is built like a slice, with a builder, an independent check, a fix round and the check
list on the merged tree. Its brief and reports are in
`review/_evidence_phase2/fixes_<notes>_reports.json`.

| Run | Notes | Head | Check | Check list |
|---|---|---|---|---|
| C1-C3 | C1, C2, C3 | `d227efa` | 1 minor (an update's size measured without its signature), fixed | |
| C4-C6 | C4, C5, C6 | `3c0f97c` | 1 minor (a comment), fixed | PASS on `3c0f97c`, with C1-C3, in 375 s |
| core | C18, C24 to C29, C32, C34, C38, C49 to C52, C56, C71 to C73 | `44578a3` | 1 major (no test that one bad vote voids a parameter change or a name action; the code was right), 2 minor (comments); all fixed, second check clean | PASS on `44578a3` in 378 s |
| node1 | C8 to C10, C13 with C74, C67 with C12 (status), C14, C16, C17, C19 to C23, C31 | `35d0ef4` | 5 minor, fixed (among them: a lowered timing waited one old interval before the loops took it) | PASS on `c798b3a`, with ui, in 385 s |
| ui | C7, C11, C12 (governance), C15, C30, C65, C66, C69 | `4be9f69` | 1 minor (a comment), fixed | as node1 |
| misc1 | C33 (its signing and consensus part), C45, C55, C64 | `30defe3` | 2 minor (a stale line count in the guide's map; a driver field only a test read), fixed | PASS on `30defe3` in 392 s |
| node2 | C33 (its node part), C35 to C37, C39, C44, C46, C47, C57 to C63, C70, C75 | `3474363` | 2 minor (a runbook sentence and two comments C36 made false), fixed; the orchestrator fixed one more comment | PASS on `3474363` in 390 s |
| last | C48, C53, C54, C68 | `5351e48` | 1 minor (C54 had dropped cmd/server's clearer seed-length message), fixed | PASS on `5351e48` in 393 s |
| moves | C40, C41, C42, C43 | `b2c5c07` | 2 minor (step 2's reading order; Payment's comment pointing at its own file), fixed; the orchestrator retitled step 16 | PASS on `b2c5c07` in 388 s |

C1-C3 fast-forwarded the branch, and C4-C6 was replayed on it without a conflict.
core was built from `25b222e` beside C1 to C6 and replayed on their merge without a conflict.
Points from core for the owner to look at:
- C34 holds the rule in one place, but it is not shorter. `CertificateTally` is a small type, and
  the code is about 22 lines longer, 15 of them comments. The owner asked to check that its shape
  reads plainly, since it is safety code.
- C52 also changed `Resume`: an own vote from the sign log counts as cast only if it verifies under
  the key the set holds for this validator. Production behaves the same, because the node checks
  every vote first, and with any other key the node has no signer.

node1 and ui were built from `3c0f97c` beside core. They were replayed after it, and the guide's
tables and test counts were merged by hand. Merge commit `c798b3a` points `Wallet.Governance` at
`node.MaxCheckpointBytes`, since node1 removed the wallet's `maxStateBytes`, and recounts the guide
with `go test -list`. With these, every note from C1 to C31 is fixed. Points from node1 and ui for
the owner:
- C10: `adopt` empties the sign log at the log's own height, not the new one. So a node that
  restarts and adopts a checkpoint still waits one re-gossip interval before it signs, the wait the
  runbook relies on. Emptying it for the new height, as `catchUp` does, is a one-argument change.
- C8: a refused name action gets the exact rule it breaks. A refused parameter change gets one
  sentence listing the possible causes, since `ParameterChange.Valid` returns only a bool.
- node1's fix round changed one phrase in `findings_20261027.md`: the Phase 2 note said "the
  genesis `stepTimeoutMs`" and now says "the `stepTimeoutMs` in force". The brief kept that file
  closed, but the note had become false.

misc1 and node2 were replayed in turn without a conflict. Points from node2 for the owner:
- C36: a validator now takes its gossip peers from the validators' addresses as it opens, not at
  block 1. With every address in the genesis, it gossips to the validators from the start, whatever
  `maxPeers` is. cmd/server's startup log counts the configured peers. Spec §8.2 says so.
- C39: the request pool checks a request's slot itself, after the fee checks. So a request that
  breaks both now gets the fee's reason, and a full pool logs each refusal it makes, as a taken slot
  already did. Spec §4.3 says so.

last was replayed after node2, keeping both runs' edits in `Node.commit`. A point for the owner:
- C54: only `signing` knows the scheme's sizes now. So cmd/server no longer checks the seed's
  length before it reads the genesis file. A wrong seed is still refused, with a message that says
  what `privateKey` takes, but after the genesis file is checked. Checking it first would mean
  `signing` exporting a seed size.

The moves came last, alone. A checker built for them, now in `review/_evidence_phase2/movecheck/`,
parses both modules before (`6f69702`) and after (`c232f09`). It finds 30 declarations moved, the
same 1,257 declarations and 31 package or floating comments, and no text changed but the two
comments the notes allowed (`AccountAmount`, `Payment`). `node/node.go` went from 1,221 to 877
lines, with the event loop, one height and commit. The guide's file lists, its map and its sizes
follow: 10,053 production lines in 55 files, 437 template lines, 16,405 test lines.

## How each slice was built

1. A builder implements the slice in its own worktree, with a test for each new behaviour. It
   proves each test by reverting the change in a scratch copy.
2. An independent checker compares the slice with the spec, not with the builder's report. It
   applies its own mutants, writes adversarial tests, and runs the whole suite list itself.
3. A fixer fixes what the check found.
4. If the check found a blocker or a major problem, a second check runs.
5. The orchestrator merges the slice and runs the suite list again on an export of the merged tree:
   both modules twice, plus the chaos and restart tests five times.

Slices of one group share no file, but they can still depend on each other's types. The owner,
on 2026-10-30: "i am not in a rush, if the work is likely to generate a manual conflict merge you
can run it one pass at a time". So S7 was taken out of group 5: its lifelong record keeps the
blocks that carry a name action, a type S5 was rewriting. It is built after S5, alone.

## Slices done

| Slice | Head | Commits | Production lines (+/−) | Test lines (+/−) | Check |
|---|---|---|---|---|---|
| S1 ML-DSA-65 | `df99946` | 7 | 185 / 233 | 541 / 276 | 1 major, 4 minor; all fixed, second check clean |
| S2 height, amounts, parameters, caps | `f2e257e` | 9 | 690 / 483 | 928 / 324 | 4 minor, fixed |
| S3 names are the identity | `0dbb89a` | 13 | 1384 / 1358 | 2587 / 2685 | 2 major, 3 minor; all fixed. The second check's 2 minor points were fixed by the orchestrator in `0dbb89a` |
| S4 block hash, nonce, signed proposals, network id | `31cc971` | 16 | 945 / 601 | 1175 / 392 | 7 minor, fixed |
| S5 name requests and the ⅔ name action | `5ec28dd` | 9 | 1260 / 327 | 1436 / 91 | 4 minor; 3 fixed, 1 went to the owner, who decided it on 2026-10-30 (below) |
| S8 genesis file, server config, demo | `e4832b0` | 6 | 297 / 227 | 560 / 247 | 3 minor, fixed |
| S7 the signature record | `eb7ca7d` | 5 | 348 / 9 | 581 / 0 | 5 minor; 3 fixed, 2 went to the owner, who decided them on 2026-10-30 (below) |
| S6 a double-sign freezes the name | `5090f4e` | 4 | 301 / 130 | 676 / 144 | none found |
| S9 wallet and admin pages | `bf02f76` | 9 | 1232 / 397 | 961 / 48 | 5 minor, fixed |
| S10 the idle chain | `8c08c56` | 7 | 98 / 22 | 585 / 3 | 1 major, 3 minor; the major and 2 minor fixed, second check clean; 1 minor is in `ui/` and went to the owner, and was decided on 2026-10-31 (below) |
| S7b the record in units of 100 | `50dd19a` | 4 | 88 / 80 | 304 / 91 | 1 minor, fixed |
| S11 the docs, in four areas | `5c5b72c` | 9 | docs: 1895 / 1457, in 15 files | none | 6 major, 27 minor; all fixed but the owner's points below; second check clean, its 6 minor fixed by the orchestrator in `5c5b72c` |
| S12 the guide to reading the code | `b142416` | 3 | docs: 891 / 7, in 3 files | none | 2 major, 25 minor. The guide's major and all the minor points were fixed. The other major is a code bug, C1 in the code notes. The second check found 3 minor, fixed by the orchestrator in `b142416` |

S8 was built beside S5 and replayed on top of it without a conflict (`a74e315` to `e4832b0`). The
orchestrator's `38e9fad` then reworded four stale comments the slices left for the merge. S7 was
built after both. `6ded0f8` then gave a freeze the 64-block window (see "Owner decisions").
S6 and S9 were built side by side; S9 touches only `ui/`, S6 never touches it, and S9 was replayed
on S6 without a conflict (`f98dc11` to `43f6bda`; its report names the hashes from before the
replay, `e6edb89` to `0411b0d`). S9's production count includes 220 template lines. The
orchestrator's `bf02f76` then exported `ledger.VerifyReassignmentNewKey`: the fix round had checked
a new key's proof by calling `Reassignment.Valid` under a validator set of the wallet alone, since
the ledger exported no proof check. The check list passed on `43f6bda` in 697 s, with the evidence
tests run 10 more times, and again on `bf02f76`. S10 was built alone and replayed on the review
edits without a conflict (`ce5462c` to `8c08c56`; its report names `19cc312` to `51a797d`). The
check list passed on `8c08c56` in 403 s, with the idle, wake and window tests run 5 more times.
S7b was built after S10, alone, and replayed on the review edits without a conflict (`e4e180b` to
`50dd19a`; its report names `921fbce` to `6739bf6`). The check list passed on `50dd19a` in 421 s,
with the record tests run 10 more times.
S11 was built on the merged tree and changes no Go file or template. The orchestrator's `5c5b72c`
fixed the second check's six minor points; every relative link and anchor in the docs resolves.
S12 likewise changes only `docs/reading-the-code.md`, `README.md`'s Layout section and
`docs/README.md`. The orchestrator's `b142416` fixed the recheck's three minor points; every link
and anchor resolves. The check list passed on `b142416` in 374 s.

What the checks found that mattered:

- **S1.** crypto/mldsa checks a signature's size only after it has expanded the public key. So a
  junk signature cost more than half a genuine verification, on paths anyone can reach. `Verify`
  now refuses a wrong size first: 5 ns instead of 0.17 ms, with a timing test.
  - `signing/fips140v1.go` stops any build that selects the FIPS 140-3 v1.0.0 module, which has no
    ML-DSA. In such a build the wallet would have called a valid seed "an earlier network's".
- **S2.** No blocker. The checker applied 38 mutants (each caught), ran 150 random states and 300
  forged blocks against an independent cap oracle, and fuzzed the amount parser with 200,000
  strings.
- **S3.**
  - A peer could pad a checkpoint with copies of one anchor name's vote and force one ML-DSA check
    per copy. Phase 1 had prevented this. Now each anchor name costs at most one check, with a
    counting test.
  - Three lookups of a key by name had no test that fails without them: update rule 20,
    reassignment votes, and the proposer's key. Tests were added. The fixer also closed two more
    gaps shown by surviving mutants: nothing tested that a proposal names the round's proposer, or
    that a counted vote is signed under the key for its own name.
  - A config with neither validators nor a validatorThreshold loaded as dynamic mode with no sign
    log. It is now refused.
- **S4.** No blocker or major problem. The tests added: block 1 builds on the genesis hash; the
  wallet's signature check at first use, under the served network id; the locked proposal's height
  in the sign log. An old-format path in the wallet's counter file was removed.
- **S5.** No blocker or major problem. The builder proved its tests with 87 mutants. The check
  found three rules with no test that fails without them: the loop's re-check of a request's
  signers' keys, one pooled request per name and block, and a ⅔ action's new key being no name's
  key. It also found that the window test did not pin the 3,600 blocks. All four now have tests.
  The fourth finding is the owner question below.
- **S8.** No blocker or major problem. A malformed genesis amount parameter had lost its field
  name in the refusal, which three old server tests had protected. It names the field again. Two
  tests were added: the validator count the genesis parameters are checked for, and the demo
  founder's power of 1.
- **S7.** No blocker or major problem. The builder proved its tests with 29 mutants. The check
  added two boundary tests (a longer retention applies from the block that carries it; a file that
  holds only a cut line is written over) and found that a height could be recorded twice when a
  snapshot write fails and the node restarts from the older snapshot, which broke the record's
  chain of block hashes. Each height is now recorded once, with a test. The builder once saw
  `TestRecordKeepsCertificatesAndNeverPayments` fail and could not reproduce it; one cause, a
  search for the bare text `alice` that random base64 can spell, is fixed. The orchestrator then
  ran the record tests 30 more times under `-race`: all passed. Name requests, freezes included,
  never make a block lifelong: spec 6.8 keeps holder requests out of the record.
- **S6.** No problem found. The builder and the checker each applied their own mutants (62 and
  62), and every one made a test fail; the new chaos test fails on the parent. The checker added
  adversarial tests that all pass, among them: a holder freeze of 4 of 7 seats with evidence
  (the cap holds the evidence back), the same block hash under 20 shuffles of mixed evidence, an
  unseated observer that signs nothing, and two double-signers on 4 equal seats (one is frozen,
  the other waits). Evidence is checked under the name's key before the block, the key the
  lifelong record lists, so `node/record.go` is unchanged. Readings the builder took, all judged
  sound: vote evidence of any vote type freezes (an honest node signs only prevotes and
  precommits); at most 8 evidence items is a limit on what an honest proposer builds, not a rule
  `ApplyBlock` enforces; proofs are pruned after each commit, so a snapshot may still hold one the
  same commit made moot, and `Open` prunes it.
- **S9.** No blocker or major problem; 41 mutants of the checker's own, two uncaught. The five
  minor findings, all fixed with a test each:
  - the Sign step of the Requests page signed a request for a block any distance ahead; it now
    refuses one past the node's intake window;
  - a run of request blocks could push a payment past the node's 64-block window, and the error
    then blamed the node;
  - a delegate typed in another case was lowercased and signed without being shown;
  - two rules had no test that fails without them (a request block surviving a restart, and one
    checkpoint read in the sign steps);
  - a validator voted on a new key's proof it never checked.
- **S10.** A height now starts only when there is work, as spec 6.6 lists it; vote intake drops a
  relayed copy of a vote it holds before any check; catch-up asks a peer's `/hdls/status` before
  fetching its checkpoint. 24 node tests that waited for empty blocks now give the network work
  with one line (`keepBusy`, a Description update for a later block). The builder applied 29
  mutants, the first check 27 of its own, the second 23: each made a test fail. What the checks
  found:
  - **Major:** `TestStaleTimerDoesNotFireIntoTheNextHeight` no longer tested anything: its node
    went idle at height 2, so the stale timer was dropped whatever the height guard said. It now
    gives the node work first, and checks that the node has a driver. `TestNodeWithAReplacedKeyStopsSigning`
    had lost its proposal check the same way and got the same fix.
  - A certificate that replaces a pooled one (its voters left the set) did not wake an idle chain.
    The builder found it; `addParameterVotes` and `addReassignmentVotes` now return true for any
    certificate they pool, so a replacement is also relayed (once per change and height).
  - A late double-sign kept while the chain idles waited in memory for other work, and a restart
    of every node meanwhile (a release upgrade) lost it. The fixer made it wake the chain: one
    line in `recordLateDoubleSign`. The check had called this an owner decision; see below.
  - The wallet's payment counter can stall on an idle chain; see below.
- **S7b.** No blocker or major problem. The builder applied 21 mutants and the check 33 of its
  own; two survived, both judged equivalent or contrived. The route code went from 78 to 70 lines.
  The check found one minor cost: pruning listed the record directory at every commit, and with
  units of 100 there are 100 times as many files (12.3 ms per commit at the default retention of
  604,800 heights, and more than half a second at a year's). The node now remembers how the oldest
  unit file ended and lists the directory only once that unit is due: 72 µs. Readings the builder
  took, judged sound: a negative `from` is a 400 (from −99 to −1 would serve unit 0 twice); and a
  height is refused only if its own unit file already reaches it, so after a restart from a
  snapshot older than a unit a catch-up skipped, the node records heights S7 would have refused,
  still each at most once.
- **S11.** One writer, then four independent checks, one per area, each against the code; they
  ran the documented commands and examples, among them a 4-bank network from the runbook's own
  genesis and config, the wallet's registration flow end to end, and the record routes. The six
  major findings, all fixed:
  - the sign log section said an idle node keeps no block or nonce, which is true of the sign log
    only (the snapshot keeps the last header, and the node keeps the last decided proposal in
    memory);
  - no doc said how a keygen pair gets into a wallet, which the bank's desk and freeze button need;
  - a hash name is `k-` and 24 characters, 26 in all, not `k-` and 26;
  - "nothing else removes money" left out the overdraw ban, and the supply formula with it;
  - the runbook's network never committed block 1 if its `data/` folder did not exist, and nothing
    said so;
  - the README's screenshots, from before Phase 1, read as today's pages.
  The docs grew about 10% (22,700 to 25,100 words), with two new area docs, `docs/signature.md`
  and `docs/api.md`.
- **S12.** The writer, then two checks: a newcomer who follows the guide in order, and an accuracy
  check against the code. Each ran every step's command. The writer ran each command on a copy of
  the tree that held only the packages up to that step, so no step leans on a later package. The
  guide is 874 lines in 22 steps plus a last step that follows a payment, a freeze and a ⅔ name
  action through every function. It closes with a table of every production file and its step.
  The two major findings:
  - the guide said every wallet action signs only in a confirm step; an account update signs at
    once (fixed);
  - **a code bug, found by the newcomer check:** when the ⅔ re-key one pinned bank, every wallet
    with the old pin shows no verified state, against spec Q17. It fails closed. This is C1 in the
    code notes.

  Beside the guide, three independent read-throughs of the code each listed what does not read
  cleanly:
  - signature, consensus and ledger;
  - the node and the commands;
  - the wallet.

  One verify agent checked their notes, the writer's and S11's, 98 in all, against the code. It
  merged duplicates, kept 74 and rejected 2. With C1, that makes `review/phase2_code_notes.md`:
  - 6 notes to fix before the push;
  - 25 worth fixing;
  - 44 optional.

Each slice's full reports, from the builder, the checks and the fixer, are in
`review/_evidence_phase2/S<n>_reports.json`. They include the builder's deviations from the spec
and the mutants each check applied. The scratch paths they name were temporary and are gone.

## Owner decisions

- 2026-10-28: `Acceptable` does **not** require `TransactionCost <= MaxPaymentAmount`. The owner:
  "that is a technical possibility that is mitigated by human realities, to happen all of the
  voters should do a dumb thing at the same time uncoordinated. i'd leave it out", and "that at
  most should be a line in the documentation saying how to (not) configure it". S11 adds that
  line.
- 2026-10-30, from S5's check: a freeze names a block at most 64 ahead, the payments' window,
  not the 3,600 blocks of other name requests (`6ded0f8`, spec 4.3). A freeze pays no fee, and the
  pool keeps one request per name and block, so one name could otherwise hold 3,601 pool slots for
  nothing. Its first freeze to land freezes the name and the pools then drop the rest, so the
  slots held and the blocks they are held for add up to about 3,600 per name; now to about 65. An
  honest freeze still takes effect in the block it names, a few ahead. The owner: "anyone that
  says 'this key is compromised' and can back that up with a signature should not be made to
  wait", and "let's make the signature a immediate thing with no flooding". One pooled freeze per
  name was considered and left out: a thief holding the key could pool a far freeze first and so
  keep the owner's sooner one out.
- 2026-10-30, from S7's check: **record files from an earlier network.** At a new genesis on the
  same snapshot path, the old record files stayed, and the node then recorded nothing below the
  old network's last height. The record files now carry the genesis hash's first 16 hex digits in
  their names, so a new network starts its own (2 lines, S7b); the docs say to delete the old
  ones. A refusal at start was considered: about 5 lines, and it could fire only when there is no
  snapshot at all, since the record may run ahead of a snapshot whose write failed.
- 2026-10-30, from S7's check: **the cost of the record routes.** The record holds no payments,
  only who signed each block, and is public on purpose. But one request could make a node read a
  whole segment of 10,000 heights: about 180 MB with 4 validators (the check measured 35 MB, on a
  test file of 2,000 heights). The owner chose units: "pool up blocks into units of 10/100/1000 or
  so so you can ask for the records and specify a period and it will give you that element and up
  to 100 slots in the future (so you have the same logic for current signatures) and doing it
  blank will only give you the last file". So the rolling record is kept in aligned units of 100
  heights, `from=H` answers H to H + 99, and no `from` answers the newest unit (spec 6.8, S7b). A
  request then reads at most two unit files: about 3.7 MB with 4 validators. `docs/security.md`
  states the cost.
- 2026-10-30: **the docs in four areas**, one per container of Phase 3: signature, node, API/CDN
  and UI (spec §11). S12's guide follows the same four.
- Still open: spec §12's builder defaults, Q1 to Q25 but Q21 (decided on 2026-10-31, below), and
  the §0.3 readings R1 to R11. The builders used the defaults, among them R5 (a double-sign
  freezes the name and leaves the balance, S6), Q7 (requests travel as text between wallets, and
  the bank desk is the Requests page of the bank operator's wallet, S9) and Q5 (a height starts
  only when there is work, and updates stay free, S10).
- For the owner, from S10. **All decided on 2026-10-31**: the owner left these to the orchestrator
  ("the choice is yours"), and spec §12 records each choice.
  - **Confirm: a late double-sign wakes an idle chain** (`8c08c56`, one line). Spec 6.6 counts a
    proof as work once the candidate block carries it, and the burn-on-double-sign stance points
    the same way. Without it, the proof waits in memory until other work arrives. *Kept.*
  - **A proof the one-third cap holds back, kept while the chain idles,** still lives only in
    memory until the next commit writes the snapshot: the wake builds a candidate without it, and
    the node idles again. If every node restarts before other work arrives, it is lost. This needs
    frozen names already near a third of the power, the case spec 5.8 sends to re-genesis. Closing
    it means writing the snapshot when a proof is kept, outside `commit`: more than a cheap fix.
    *Accepted as documented.*
  - **The wallet's payment counter can stall on an idle chain.** `nextPaymentBlockID` spends and
    saves a block before each payment or update goes out, so a refused one still uses its block.
    After 33 refusals in a row from a fresh counter (fewer if earlier payments still name blocks
    ahead) while no one else gives the chain work, the wallet refuses to sign
    ("wait for the chain to advance"), and a restart does not clear it. Before S10 the chain moved
    every second and this cleared itself. Giving the block back on a refusal would break the rule
    the counter exists for: never put two signed payments for one block into the world, whatever
    a node reports, since a dishonest node could say "refused" and keep the first. Any payment
    or update anyone makes moves the chain and clears it. S12 found that the wallet checks only
    part of the payment rule before it signs (code note C2): it does not check the payer's
    balance, the cap, the cost floor or an update's rule. So refusals are likelier than this note
    first said. C2 closed that gap in `e12c8d1`: now only a refusal by the node uses up a block.
    `ui/USER_MANUAL.md` states it. *Kept as documented.*
- For the owner, from S11 (the docs state each as current behaviour). **All settled on
  2026-10-31**, by the code-note fixes or by the orchestrator's choice; spec §12 records them:
  - **The README's tagline**, "a distributed ledger in a single static binary", and its Origins
    line. With a C compiler present, `go build` links the server, the demo and the wallet against
    the C library; `CGO_ENABLED=0` makes them static. The runbook and the Design goals say so; the
    tagline is the owner's to reword or keep. *`make build` now sets `CGO_ENABLED=0`, so the
    tagline holds (`93f5fc5`).*
  - **The certificate routes answer 202 even when the node refuses the certificate,** so the
    admin page can say "certificate submitted" for one the node did not pool (only if the state
    moved between the wallet's check and the post). `docs/api.md` states the 202. *Fixed by C8: a
    refused certificate gets 409 and the reason.*
  - **keygen prints `publicKey` first**; spec §8.1 lists `privateKey` first. The docs follow the
    code. *Spec §8.1 now follows the code too.*
  - **Q21** (open until 2026-10-31): in dynamic mode the record shows each seat's stake per height.
    `docs/security.md` states it. *Decided: the record keeps the sets; `docs/security.md` says why.*
  - **A missing snapshot folder makes a validator send nothing, silently,** until another node's
    commit lets it write. `Open` could refuse it, about 5 lines. *Fixed by C9.*
  - **A node that catches up past a height it signed in keeps that height's sign log on disk**
    until it next signs, block, payments and nonce included. `catchUp` and `adopt` could write an
    empty log, about 3 lines. *Fixed by C10.*
  - **Spec §3.6 says an idle chain does not keep the last block;** that is true of the disk. The
    node keeps the last decided proposal, block included, in memory, to re-send the last commit.
    *Spec §3.6 now says the rule is about the disk; C20 fixed the code comment.*
  - **Phase 3's container count:** `findings_20261027.md` says three (fetcher, engine, signer);
    TODO.md now says four, one per docs area, and the findings paragraph carries a dated note.
    *Four, as the owner said on 2026-10-30.*
  `review/phase2_code_notes.md` takes up the code points among these: the 202 is C8, the missing
  folder C9, the sign log C10, and the last proposal C20.

## Notes passed to the last slices (kept for the record)

Each list went to its slice's builder. No slice remains.

- **S11 and S12** are done; their briefs are in `review/_evidence_phase2/S11_reports.json` and
  `S12_reports.json`, with their reports. The S12 brief that follows is kept for the record.
- **S12, a guide to reading the code.** Added by the owner on 2026-10-30; spec §10.2 has the
  scope. It follows the four areas of §11, and runs after S11, alone, since both edit
  `README.md`. The production code is 9,940 Go lines in 54 files at `3c86304` (signing 246,
  consensus 1,000, ledger 2,594, node 3,467, the commands 465, ui 2,168), plus 437 template lines;
  the tests are 15,522. The imports run `signing` →
  `consensus` → `ledger` → `node` → the commands and `ui`; `ledger` imports only the consensus
  types, never the driver.
  - It is also the last read-through before the push (owner: "it will also be a good way to
    recheck that everything is tight before pushing"). The builder and the checker each list the
    code that does not read cleanly, and the owner picks what is fixed before the push.
- **S11 and S12 are docs.** `phase2-build-group.js` asks its builder for tests that fail without
  the change and its checker for mutants. For a docs slice, give both a docs brief instead: the
  checker checks each claim against the code and, for S12, follows the guide in order as a
  newcomer, running each step's commands.
- **Known behaviour, as the spec has it.** If two seated banks freeze themselves in one block and
  together would empty the set, `ProposeBlock` takes items out last-added first, so the block's
  other items lapse before the second freeze comes out. Intake refuses each freeze only on its
  own. Pooled certificates are verified at every height, as before S5.
  - Evidence the one-third cap holds back stays pending in each node that holds it, in its
    snapshot from the next commit or catch-up, until a block can carry it, the name is frozen, or
    the name's key changes (the ⅔ action, or a key change the name files once unseated). It has no
    other expiry, as spec 6.4 intends.
  - The wallet's payment counter skips the blocks of its own requests, but a request drafted with
    a lead under about 64 blocks can name a block one of its payments already names. Then the node
    refuses the request (409, with the reason on the page) or the payment lapses. The default lead
    is 120 blocks. Refusing it in the wallet would take about 3 lines.
- **In no slice.** Small points noted during Phase 1 and the rename. The first three were fixed
  as code notes C33 and C75 on 2026-10-31:
  - `Driver.Start` is used only by tests (now in `driver_test.go`).
  - In `Node.commit`, `block, valid := node.blocks[...]` called a map lookup's found flag `valid`.
  - `enqueue(event event)` and `handle(event event)` named a parameter after its type.
  - Some test failure messages use upper-case scenario labels. Still open.

## Cost so far

| Slice | Agents | Subagent tokens | Wall time |
|---|---|---|---|
| S1 | 4 | 1.05 M | 79 min |
| S2 | 3 | 1.31 M | 97 min |
| S3 | 4 | 2.11 M | 158 min |
| S4 | 3 | 1.45 M | 117 min |
| S5 and S8, in parallel | 6 | 1.48 M | 81 min |
| S7 | 3 | 0.57 M | 64 min |
| S6 and S9, in parallel | 5 | 1.51 M | 68 min |
| S10 | 4 | 0.94 M | 104 min |
| S7b | 3 | 0.44 M | 44 min |
| S11 | 7 | 2.21 M | 80 min |
| S12 | 9 | 2.78 M | 96 min |
| Total of the slices | 51 | 15.86 M | 16.5 h |
| Fixes C1 to C6 (two runs, in parallel) | 6 | 0.78 M | 46 min |
| Fixes: core | 4 | 0.64 M | 61 min |
| Fixes: node1 and ui, in parallel | 6 | 1.15 M | 65 min |
| Fixes: misc1 | 3 | 0.35 M | 34 min |
| Fixes: node2 | 3 | 0.59 M | 49 min |
| Fixes: last | 3 | 0.44 M | 37 min |
| Fixes: moves | 3 | 0.53 M | 42 min |
| Total of the fixes | 28 | 4.46 M | about 4 h of wall time, 07:20 to 11:25 UTC, with runs overlapping |
| Wrap-up: six area reviewers, in parallel | 6 | 2.35 M | 22 to 33 min each |
| Wrap-up: five fixers, in parallel | 5 | 1.96 M | 25 to 36 min each |
| Wrap-up: the recheck, the cold read (Sonnet) and the hardening check (Opus), in parallel | 3 | 1.42 M | 15 to 46 min each |
| Total of the wrap-up | 14 | 5.73 M | about 3 h of wall time, 11:45 to 14:45 UTC |
| The lost-sign-log fix: a builder (three rounds) and two checkers | 3 | 1.46 M | 16:00 to 19:00 UTC, with a machine stop |

## How to resume

1. Make one worktree per slice, from `post-emudhra-phase2`. For example:
   `git worktree add -b p2-fixes <scratch>/wt/p2-fixes post-emudhra-phase2`. Every slice, every
   code note and the wrap-up are done; what is left is the owner's review and the push. A
   change the review asks for can be built the same way. Before running two at once, check that
   neither needs the other's types.
2. The build workflow is `phase2-build-group.js`, in the build session's
   `~/.claude/projects/<project>/<session>/workflows/scripts/`.
   Its `SP` constant must name the scratchpad of the session that runs it. Resuming the build
   session keeps the same path, though after a reboot the folder starts empty; a new session must
   change it. Its argument is `{"slices": [{"id", "branch", "worktree", "notes"}]}`. Give each
   change its own notes, plus a short summary of what is merged.
3. Merge each slice into `post-emudhra-phase2`. Then run the check list on an export of the merged
   tree, with `GOTOOLCHAIN=auto` and `GOLEDGER_CHAOS_SLACK=4`:
   - `gofmt -l .`, which must print nothing (CI checks `review/` too);
   - `go vet ./...` and `go vet -C ui ./...`;
   - `go build ./...` and `go build -C ui -o /dev/null .`;
   - `go test -race -count=1 ./...` and `go test -C ui -race -count=1 ./...`, each twice;
   - `go test -race -count=5 -run 'Restart|Chaos|SignLog|Resume|CatchUp' ./...`.

## The wrap-up (2026-10-31)

The owner, on the afternoon of 2026-10-31: "yes scrub the personal info, and feel free to spin up a
python chromium browser to take the pictures"; on the open points of S10 and S11, "the choice is
yours"; then "finish the last few fix and do the doc pass as if it was an handoff / project wrap up
so you can validate the doc against the spec and the code against the doc, and see if everyone is
telling the same story". And last: a Sonnet agent, kept from the project's memory, reads the
repository cold to see that it stayed legible, and an Opus agent checks that it is still hardened.

1. **Static binaries and screenshots.** `make build` sets `CGO_ENABLED=0` on its build lines
   (`93f5fc5`; `make test` keeps cgo for `-race`). The screenshots were retaken with headless
   Chromium from `make demo`, after a payment of 12.50 from founder to bakery (`b01178e`).
2. **The S10 and S11 points**, decided by the orchestrator (`320dc62`, spec §12).
3. **Six reviewers**, one per docs area and one for the entry points and this record, each checked
   the spec, the docs and the code of its share against each other. 67 findings: SIG-1 to 14,
   LED-1 to 7, CON-1 to 9, NODE-1 to 13, UI-1 to 11, GEN-1 to 13. The code honoured every owner
   decision; most findings were stale or missing text. The ones that changed code:
   - **CON-4.** A refused precommit of a locked value (a signer that is down, a full disk) was never
     retried: the round went to nil. Liveness, not safety. Fixed in `f4bf601`, with a test.
   - **UI-1.** After a restart, a node that reported a low height could make the wallet drop its
     payment counter and name a used block again, against the counter's one rule: never two signed
     payments or updates for one block, whatever a node reports. The counter now counts from the
     verified state's height, which never goes back, and the branch that dropped it is gone.
   - **UI-2.** With no verified state, the pages said "not registered"; they now say "No verified
     state". **UI-4.** The wallet read a receipt without a size bound and took a checkpoint served
     with an error status; both are fixed.
   - **SIG-1.** No doc warned against running one validator key on two nodes. The docs now do, and
     `cmd/server` binds its port before it opens the node (`bindThenStart`), so a second start of
     one config on the same machine stops before it signs or touches the sign log.
   - **SIG-10.** A stop between a commit's snapshot write and its empty sign log left the old log
     (block, payments, nonce) on disk. A restart now empties it, at its own height, so the wait
     after a lost log stays.
4. **Five fixers**, each owning its own files: F1 the wallet, F2 the node, consensus and ledger code
   and comments, F3 the node and consensus docs, F4 the entry docs, the guide and TODO, F5 the spec
   and this record. Their 78 commits merged without a conflict. The orchestrator recounted the
   guide (10,106 production lines in 55 files, 449 template lines, 16,736 test lines, and the test
   count of each step) and fixed the rows the merge left behind.
5. **Three final reads** of the merged branch (`bc8b878`):
   - a recheck: 66 of the 67 findings resolved as planned, the 67th being this section, and four
     small new points, fixed;
   - a cold read by a Sonnet agent from the repository alone: "A capable newcomer can reorient on
     the system and the code from this repository alone, and quickly. The docs are unusually
     accurate." What it could not recover was the project's state and its ids, so
     `docs/glossary.md` now explains the words, the three pins, the Java project's words and every
     id family, the README says where things stand, and the old review banners carry dated notes
     (`fc27ebe`, `7e6fb1d`);
   - a hardening check by an Opus agent: no critical or high problem. Signatures, the sign log,
     commit safety, certificates, the one-third cap, intake and pool bounds, the HTTP limits and the
     wallet's trust model hold; a fault simulation over 720 seeds found no fork and no double-sign by
     an honest node; the 102 regression tests of the earlier findings pass. It found two things:
     - medium: a dishonest node could keep the wallet from ever saying that a freeze did not land,
       since the freeze still took its height from the node. It now takes it from the verified
       state, as a payment does (`f6536fa`, with a test);
     - low: a validator back without its sign log waited only at the height it starts at, so one
       restored from a backup even one height behind could double-sign. At the owner's go-ahead
       ("do the fix if you think a fresh pair of eyes would read it better") a fresh agent built it
       and another checked it, three times. The node now keeps the highest height it may have
       signed at (raised only by its own verified signatures, and kept in the sign log), waits two
       re-gossip intervals at every such height it reaches by commit or catch-up, takes back its
       own proposal of any round up to 256 during a wait, and starts each new sign log with what it
       took back. The checks found and closed four more gaps on the way (the bound kept only in
       memory, own proposals past round 1, a second restart after taking back its proposal, a wait
       one re-gossip tick could miss). It stays best effort: it recovers only what peers re-gossip
       during the wait (spec §6.9). Ten commits, `096c8d3` to `8524e16`, with 13 tests; the
       reports are `signlog-build.md`, `signlog-check.md` and `signlog-recheck.md`.

The check list passed on the merged branch (`bc8b878`, 383 s) and on the final tree (`79b4332`, 386 s), and with the
lost-sign-log fix merged (`145757c`, 519 s: the tests that run the real loop wait longer now).
The reviewers' and fixers' reports are in `review/_evidence_phase2/wrapup/`.

## The scrub (2026-10-31)

The owner asked for the personal information to be scrubbed before the push. It was only in
`review/`: the build session's local paths (its scratchpad, its temporary and project folders),
which carried the machine's user name, and the session's id. No file outside `review/`, and no
commit message, author or tag, held any of it.

- The 412 commits after `main` on `post-emudhra-phase1` and `post-emudhra-phase2` were rewritten
  with `git filter-branch`. Every file and message that held a path now holds a placeholder:
  `<repo>` for the repository's folder, `<scratch>` for the build session's scratchpad,
  `<session-tmp>` for its temporary folder, `~/.claude/projects/<project>/<session>` for its project
  folder, `<session>` for its id (the owner's local notes keep it), and `<user>` for the user name.
- In the same pass, every commit hash a file or a message cites was mapped to that commit's new
  hash, so the record still points at real commits. The six hashes this note labels as "from before
  the replay" were never on the branch, and stay as they were.
- `main` and the tags `v1.0.2` and `v1.0.3`, already public, were not touched.
- A check rebuilt the map from the two histories on its own and compared every file of both final
  trees with the old file put through the same rewrite: 1,593 files, 140 of them changed, all under
  `review/`, and no mismatch. No personal string is left in any file or message of either branch.
  Every hash this note, the code notes and the findings cite is on the new branch, but those six.
  Outside `review/` the trees are unchanged, and the code builds and vets.
- The branches as they were stay in this repository under `refs/original/`, which `git push` does
  not send. Delete them once the push is done:
  `git update-ref -d refs/original/refs/heads/post-emudhra-phase1`, and the same for
  `post-emudhra-phase2`.

## The owner's first read (2026-10-31)

Reading step 1 of the guide, the owner, from Java, could not see what tied `MLDSA65` to `Scheme`:
Go has no `implements` keyword. At their request the tie is now written down in both directions.
- `80dc3b7`: every type that implements an interface says so on its type line
  (`type MLDSA65 struct{} // implements Scheme`). In production code a line such as
  `var _ Scheme = MLDSA65{}` makes the compiler check the claim. This covers the standard
  library's `json.Marshaler`, `json.Unmarshaler` and `fmt.Stringer` too, which encoding/json and
  fmt look for at run time. The check list passed (520 s).
- `25b78e1`: each interface names its implementations, the test doubles included. The compiler
  does not check this direction. Comments only: gofmt, vet and build pass.
