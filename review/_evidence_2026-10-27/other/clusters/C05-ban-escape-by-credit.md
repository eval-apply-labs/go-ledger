# C05-ban-escape-by-credit

Round-1 reports merged into this cluster (2):

## [burn-rule] BAN-1 — The overdraw ban is escaped when the banned payer also receives a same-block credit — the payer keeps its full balance and name, contradicting the documented/pinned 'balance destroyed' invariant
- severity/confidence (round 1): low/high
- category: safety
- locations: ledger/ledger.go:167, ledger/ledger.go:193, ledger/ledger.go:197, ledger/ledger.go:198
- attacker: no attacker (inconsistency / safety-by-accident); relevant to the hoarded-pair weaponization recorded in findings.md
- known_status: new angle on the accepted overdraw-ban design decision (findings.md). The decision and TestOverdrawBanDestroysPayerBalance assume the balance is always destroyed; the same-block-credit escape is not covered — reporting it as the brief's explicit (e) question, not to reverse the accepted decision.

**Description.** ProcessPayments bans an overdrawing payer by skipping its outgoing (ledger.go:174) and excluding it from the pass-through loop (ledger.go:193). But the `affected` re-add loop (ledger.go:197-213) does NOT check `banned`: a banned payer that received ANY credit this block is in `affected`, so it is re-created from its ORIGINAL byKey balance plus the credit (its own debits were skipped), surviving with balance intact and its Name intact. findings.md's accepted design decision and TestOverdrawBanDestroysPayerBalance assert the account 'vanishes entirely'; that holds only when no same-block credit lands. Supply is still conserved (no mint), so this softens — not breaks — a safety property.

**Failure scenario.** A holds 10, Name 'alice'. In one block A signs two 6-unit payments to B (12 > 10 → A banned) and C pays A 0.2. After ProcessPayments: A is re-added in the affected loop as 10 + 0.2 = 10.2 with Name 'alice' retained; B receives nothing; supply conserved. The documented harsh penalty (A destroyed) did not occur. This also means the hoarded-same-block-pair weaponization (findings.md second pass) can be dodged by any interleaving credit — benign direction, but it shows 'overdraw ⇒ balance destroyed' is not a reliable invariant and the regression test only covers the no-credit case.

**PoC.** <scratch>/burn-rule/ledger/burnrule_poc_test.go — TestPoCOverdrawBanEscapedByIncomingCredit (FAIL: banned payer A present with 10.10 and name retained).

**Suggested fix.** If the ban must mean 'destroyed', skip banned payers in the affected re-add loop too: `for k := range affected { if banned[k] { continue } ... }`. Otherwise document that an incoming same-block credit preserves a banned payer and relax the test accordingly.

## [ledger-transition] OVERDRAW-2 — A banned payer that also receives a credit in the same block keeps its whole balance
- severity/confidence (round 1): low/high
- category: docs-drift
- locations: ledger/ledger.go:183-214, findings.md:250-263, ledger/ledger_test.go:92-114
- attacker: no attacker (semantics); the proposer decides by including or omitting a credit
- known_status: Relates to the accepted overdraw-ban DESIGN DECISION. New: the documented trace is incomplete.

**Description.** The design decision says a banned payer is 'dropped from the result entirely'. In the code, a banned payer is skipped only in the pass-through loop. If any payment in the block credits it, by key or via a name it holds, it lands in `affected` and is re-added with its old balance plus the credit; its own payments are merely skipped. Whether the ban destroys anything therefore depends on unrelated payments the proposer chooses to include. TestOverdrawBanDestroysPayerBalance does not cover this case.

**Failure scenario.** PoC: A (balance 10) sends two 6-unit payments, which bans it, and C pays A 1 in the same block. A remains with 10.90 instead of being destroyed.

**PoC.** <scratch>/ledger-transition/ledger/poc_overdraw_semantics_test.go: TestPoCOverdrawBanSemantics FAILs (case 2: A present with 10.90).

**Suggested fix.** Decide which behaviour is intended and pin it with a regression test. Either skip banned keys in the affected loop too, or document that a same-block credit preserves the account.
