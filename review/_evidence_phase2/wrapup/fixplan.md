# Wrap-up fix plan (orchestrator, 2026-10-31)

The six review reports are in this folder's reports/ (signature.md, ledger.md, consensus.md, node.md,
ui.md, general.md), with finding ids SIG-, LED-, CON-, NODE-, UI-, GEN-. Each finding gives where,
what each source says, the evidence and a proposed fix. Read the full text of each finding you own
before you change anything, and check it against the code yourself: a reviewer can be wrong. If one
is, say so in your report instead of changing correct text.

Already done by the orchestrator: CON-4 (commit f4bf601: a refused precommit of a locked value is
retried; castVote's comment and finding E31 are now true). The S10/S11 owner points are decided
(spec §12, 2026-10-31).

## Decisions the orchestrator took (the owner left these choices to it on 2026-10-31)

- UI-1, the payment counter: fixed in code. Send and UpdateAccount pass the height in progress of the
  verified checkpoint they already hold (checkpoint.State.Height + 1) to nextPaymentBlockID, instead
  of the unverified /hdls/status height. A verified state never goes below the saved NewestHeight
  (the no-rollback rule), so a node can no longer make a restarted wallet drop its counter and name a
  used block again; a node that lies can only make a payment miss its block. Wallet.height() stays for
  the freeze and the requests, which do not use the counter (the freeze must work with no verified
  state). After the change the docs' "whatever a node reports" is true again.
- SIG-1: the docs warn (one validator key on one node only), and cmd/server binds its listener before
  node.Open/Start, so a second start of the same config on the same machine stops before it signs.
  That is a guard against one mistake only: another machine, or another listen address, is not
  caught, and the docs say so.
- SIG-10: fixed in code. loadSignLog empties a sign log left below the restart height at its own
  height, as adopt does, so the lost-log wait stays and the old block, payments and nonce go.
- UI-11: a multi-account wallet is not planned (spec §1 lists it as a non-goal). ui/README.md says
  so; TODO.md gets no item.
- CON-9 / NODE-13 / SIG-14: the sentence in node/persist.go goes, and TODO.md gets the item: every
  commit and catch-up marshals and fsyncs the whole checkpoint (about 2.9 KB per account) on the event
  loop; move it off the loop or make it incremental before a network grows large (spec Q18).
- GEN-3: docs/why-small-and-auditable.md is the owner's own account. Do not reword it; append one
  dated note only: "(Owner, 2026-10-30: four containers, one per docs area, the wallet included; see
  TODO.md.)"
- Dated records (findings.md, findings_20261027.md): never rewrite history. Where a sentence states
  current behaviour that is false, add a short dated note in brackets, "(2026-10-31: …)".

## Who owns what (no file is shared)

F1 ui: ui/*.go, ui/*_test.go, ui/templates/*, ui/README.md, ui/USER_MANUAL.md.
  UI-1 (code, as decided), UI-2, UI-3 (ui docs and trust.go's message; how a bank makes its list goes
  in ui/README.md), UI-4, UI-5, UI-6, UI-8, UI-11 (as decided), SIG-4 (ui/README.md's "unchanged").

F2 code: cmd/server/*.go, node/*.go and node tests, consensus/*.go comments and consensus_test.go,
  ledger/*.go comments and ledger tests, Makefile.
  SIG-1 (the bind-first code), SIG-10 (code and test), SIG-9 (the two comments), NODE-1, NODE-6 (the
  Bootstrap comment in node.go and idle.go's line), CON-1 (errors.go's package comment and the
  consensus_test.go comment), CON-6 (node/proofs.go's comment), CON-8, CON-9/NODE-13/SIG-14 (drop the
  persist.go sentence), LED-7, GEN-13 (ledger/payment.go's comments), GEN-9 (Makefile: test -C ui
  -race).

F3 node docs: docs/consensus.md, docs/architecture.md, docs/api.md, docs/security.md,
  docs/governance.md, docs/managing-the-currency.md, docs/signature.md, docs/README.md.
  CON-1, CON-2, CON-3, CON-5, CON-6 (docs), NODE-2, NODE-4, NODE-5 (api.md, security.md), NODE-7,
  NODE-10, NODE-11, NODE-12 and GEN-11, LED-1, LED-2 (managing-the-currency.md), SIG-1 (signature.md's
  sign-log section), SIG-13 (docs/README.md's API/CDN list).

F4 entry docs: README.md, TODO.md, docs/operating-a-network.md, docs/reading-the-code.md,
  docs/why-small-and-auditable.md (one dated note), findings.md and findings_20261027.md (dated notes
  only), review/README.md.
  CON-1 (README), LED-2 (findings note), LED-3, CON-3 (findings note), SIG-1, SIG-2, SIG-3, SIG-4
  (README), SIG-5, SIG-9 (the backupPath row), SIG-13 (the runbook's Code line), NODE-5 and NODE-6
  (runbook), UI-3 (one runbook sentence pointing to ui/README.md for the bank's list), UI-7, GEN-3,
  GEN-4 (review/README.md as an index of every file and folder in review/, starting with
  phase2_progress.md; and README.md's Status link), GEN-5 (TODO.md, with the decisions above), GEN-9
  (README's build line), GEN-10, GEN-12, GEN-13 (README), CON-9 (the TODO item).

F5 spec and build record: review/phase2_spec.md, review/phase2_progress.md, review/phase2_code_notes.md.
  SIG-6/NODE-3/GEN-2, SIG-7/GEN-3 (spec §1), SIG-8/GEN-8 (header, §0.1, and Appendix #58's garbled
  rename example), SIG-12, LED-2 (the §3.1 comment), LED-4, LED-5/NODE-9/UI-10 (names, and
  maxRequestLookahead to MaxRequestLookahead everywhere), LED-6, CON-6 (§6.4), CON-7, NODE-8, GEN-6,
  GEN-7, GEN-9 (the check list's gofmt line), GEN-1 and UI-9/SIG-11 (the progress note's "Where the
  branch stands": the screenshots are done; S10/S11 settled; the build record now says a wrap-up is
  in progress, which the orchestrator completes at the end), CON-6 (progress note's held-back proof
  line). Also make the spec describe the decisions above where it covers them: the counter's verified
  height (§7.3 or wherever the counter is specified), the sign log a restart empties (§6.9), and
  cmd/server's bind before start (§8.2).

## For every fixer

- Work only in your own worktree and your own files. Commit as you go, one commit per finding or
  small group, subject "phase 2 wrap-up: <what>", body naming the finding ids, and the trailer
  "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>". The repo config already authors commits.
  Never push, never change git config.
- Plain, short sentences: the owner is not a Go expert and will reorient from these docs. They/them
  for people.
- Code changes need a test that fails without them (prove it on a scratch copy with the change
  reverted), except comment-only changes. F1 and F2 run the whole suite list before finishing:
  gofmt -l . (no output); go vet ./... and go vet -C ui ./...; go build ./... and go build -C ui
  -o /dev/null .; GOLEDGER_CHAOS_SLACK=4 go test -race -count=1 ./... and the same with -C ui.
- Do not fix the guide's test counts for tests other fixers add; the orchestrator recounts after the
  merge. F4 keeps the counts it changes itself right.
- Report, per finding id: done (commit), or not done and why. Then anything you noticed outside your
  files, for the orchestrator.
