# C06-single-overdraw-ban

Round-1 reports merged into this cluster (1):

## [ledger-transition] OVERDRAW-1 — Overdraw ban fires on a single over-balance payment; the wallet signs any amount, so the documented same-block closure does not protect honest payers
- severity/confidence (round 1): high/medium
- category: economic
- locations: ledger/ledger.go:157-169, ledger/block.go:176-241, ui/wallet.go:198-238, ui/wallet.go:29-36, ui/wallet.go:168-191, node/node.go:451-454, findings.md:265, findings.md:275-282, docs/security.md:74-81, ui/USER_MANUAL.md:107-109
- attacker: one validator acting as the payer's reference node (proposer at the payment's BlockID)
- known_status: Relates to the accepted overdraw-ban DESIGN DECISION and its second-pass wallet closure. New angle: the closure is incomplete, because a single payment with no same-block pair triggers the ban. security.md, USER_MANUAL.md and the wallet.go comment overstate the protection.

**Description.** The ban (an accepted design) destroys the payer's whole balance whenever its outgoing in a block exceeds its balance. One payment is enough, as is a later-block payment that exceeds what an earlier one left. The second-pass closure assumes the trigger needs two same-block payments, and stops only that case. The wallet has no local balance check: it signs whatever amount is typed and posts it, and only then gets the node's 409. Honest proposers never include such a payment, but a block that does is fully valid. Docs-drift: the manual says a refused payment means 'nothing is debited', and security.md says an honest user cannot trip the ban.

**Failure scenario.** PoC: a payer holds 10 and signs one payment of 100. A block carrying it at its BlockID applies, and the payer's account (10) is destroyed. In practice: a typo of 100 instead of 10 is refused with 409 by the reference node, which now holds the signature. If that validator is the proposer at the payment's BlockID and includes it, the payer's whole balance is destroyed. The wallet derives the BlockID from the height that same node reports.

**PoC.** <scratch>/ledger-transition/ledger/poc_overdraw_semantics_test.go: TestPoCOverdrawBanSemantics FAILs (case 1: a block with one over-balance payment applies and the payer is destroyed).

**Suggested fix.** Ledger: drop a lone over-balance payment without destroying the balance, and reserve the ban for provable double-spends (two or more distinct same-block payments, each individually within the balance). Wallet: track balance minus pending locally and refuse to sign more. Correct the docs.
