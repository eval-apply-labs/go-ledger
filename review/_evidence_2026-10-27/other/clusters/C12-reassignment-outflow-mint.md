# C12-reassignment-outflow-mint

Round-1 reports merged into this cluster (1):

## [governance] GOV-1 — Reassignment applied post-payments as an absolute set mints the target's same-block outflow
- severity/confidence (round 1): high/high
- category: economic
- locations: ledger/block.go:151, ledger/block.go:155, ledger/block.go:230, ledger/block.go:234, ledger/reassignment.go:90, ledger/reassignment.go:328, docs/governance.md:77
- attacker: holder of one key that is the subject of any ⅔-ratified reassignment (e.g. a treasury custodian; or a seizure target)
- known_status: Related to findings.md LOW 'Reassignment validated pre-block but applied post-payments', which only documents the INBOUND case (a same-block credit silently overwritten, no supply effect). This is the OUTBOUND case: materially worse — it MINTS supply and defeats ⅔ seizures. New angle, not recorded.

**Description.** Reassignment.Valid checks the account holds PreviousAmount in the PRE-block state (reassignment.go:90), but ApplyBlock runs ProcessPayments first and applyReassignment last (block.go:230-234), setting the balance to the ABSOLUTE NewAmount. If the target's key-holder sends an outgoing payment naming the enacting block, the outflow is honoured (payee credited) AND the balance is re-set to NewAmount regardless — so whatever was moved out is created from nothing. Supply grows by (moved-out amount) beyond the ratified delta. An honest proposer builds this block: ProposeBlock carries both; ApplyBlock accepts it. The docs note the INBOUND direction (a same-block credit is overwritten) but not that the OUTBOUND direction mints. Breaks the B1/B2 'supply integrity, fixed by construction' claim via governance, and the 'every mint == its certificate' auditability managing-the-currency.md relies on (the extra mint is laundered as an ordinary payment).

**Failure scenario.** Treasury custodian holds key T (balance 1000), NOT a validator. The ⅔ set ratifies a treasury top-up 1000→5000. Custodian signs a payment T→X(own 2nd key) of 1000 naming block h; honest intake admits it (pending 1000 ≤ balance 1000). The proposer of h carries both. ApplyBlock: ProcessPayments debits T, credits X 999.9; applyReassignment sets T=5000. Result: T=5000, X=999.9 — supply +4999.9 while the certificate ratified +4000. 999.9 minted per top-up, repeatable. A ⅔ seizure 1000→0 is likewise defeated: victim pre-signs an outflow into the seizure block, keeps the money at X, account zeroes — confiscation nets nothing.

**PoC.** <scratch>/governance/ledger/zz_govreview_poc_test.go (TestPoCGovReassignmentSameBlockOutflowIsMintedTwice) — FAILs: top-up and both seizure sizes each mint 999.9

**Suggested fix.** Validate and apply the reassignment against POST-payment balances (move the PreviousAmount check into ApplyBlock after ProcessPayments), or forbid a block that carries a reassignment for an account that also appears as a payer/payee that block, or apply it as a delta (NewAmount-PreviousAmount) rather than an absolute set.
