# C16-below-cost-free-carriage

Round-1 reports merged into this cluster (1):

## [ledger-transition] FEE-1 — Below-cost payments are admitted, receipted and carried by honest nodes with no fee; other zero-fee no-ops still pass ApplyBlock
- severity/confidence (round 1): medium/high
- category: economic
- locations: node/node.go:432-461, ledger/block.go:105-120, ledger/block.go:180-204, ledger/ledger.go:132-155, node/mempool.go:27-41, node/mempool.go:96-106
- attacker: any account holder (tiny balance)
- known_status: Partially fixed: the RC review closed unresolvable recipients only, and the original findings.md gossip-intake HIGH noted far-future residency (its signature fix stands). New: below-cost amounts remove the overdraw guard's economic bound.

**Description.** ProcessPayments silently drops payments that are below the cost, come from a non-existent payer, or duplicate another payment's content: no debit, no fee. The release-candidate fix kept only unresolvable-recipient payments out of blocks. Honest intake rejects only non-positive amounts, so a below-cost payment gets a receipt, is gossiped, and is carried by honest proposers for free. Intake has no upper bound on BlockID, and the overdraw guard just sums amounts. So tiny payments naming a far-future block can sit in the bounded mempool indefinitely and get honest payments refused. Proposers can also pad blocks with phantom-payer or duplicate payments and with no-op updates.

**Failure scenario.** PoCs:
- 50 of 50 payments of 0.001 (fee 0.1) are admitted, all 50 are carried in the honest proposal, the block applies, and the payer is charged nothing.
- With a 50-slot pool, 50 such payments naming block 2^40 survive dropBelow while locking 5e-8 units, and an honest 5-unit payment is refused ('mempool full').
- A block carrying a ghost-payer payment, a below-cost payment and a ghost update applies with no state change.

**PoC.** <scratch>/ledger-transition/node/poc_below_cost_intake_test.go: TestPoCBelowCostPaymentAdmittedAndCarriedFree and TestPoCBelowCostFarFutureFillsMempool both FAIL. <scratch>/ledger-transition/ledger/poc_noop_and_pin_test.go: TestPoCApplyBlockAcceptsOtherZeroFeeNoOps FAILs.

**Suggested fix.** Refuse Amount < cost at intake. ProposeBlock should filter, and ApplyBlock reject, every payment ProcessPayments would drop (below cost, absent payer, duplicate content) and every no-op update (absent account). Bound BlockID to height+window at intake.
