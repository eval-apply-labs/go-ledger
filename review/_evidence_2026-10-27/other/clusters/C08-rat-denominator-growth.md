# C08-rat-denominator-growth

Round-1 reports merged into this cluster (1):

## [ledger-transition] RAT-GROWTH-1 — MaxMessageBytes bounds messages, not balances: coprime-denominator payments grow one balance without limit, at super-linear digest and arithmetic cost
- severity/confidence (round 1): medium/high
- category: dos
- locations: ledger/parameters.go:26-31, ledger/block.go:17-23, ledger/ledger.go:160-214, ledger/digest.go:29-31, ledger/reassignment.go:208-219
- attacker: any account holder paying fees (free if transactionCost is 0)
- known_status: Relates to the findings.md third-pass item 'one fee could buy an arbitrarily large permanent ledger entry — FIXED'. That fix bounds messages, not accumulated balances.

**Description.** Each in-bound payment can carry an amount whose denominator is coprime to the balance's. The balance's denominator becomes their LCM, growing by roughly the per-message digit budget per payment. Every LedgerState.Digest RatString-encodes every balance, several times per block per node, and Cmp/Add/Sub on such balances pay big-number multiply and GCD costs. The owner cannot normalize the balance, because an amount with that denominator exceeds the cap. Once the encoding passes 1 MiB, no reassignment can carry the exact PreviousAmount either. Cost is one fee per payment: free if transactionCost is 0 (documented as allowed), and a single payment suffices if MaxMessageBytes is 0.

**Failure scenario.** PoC (shared machine, so timings are approximate): 1000 payments of 0.1+1/d_i, each within the 2000-byte cap, cost 100 units of fees. Afterwards the payee's and payer's balances each encode to about 1.56 MB. StateDigest takes about 0.9 s per call, and one ordinary 1-unit payment touching these accounts takes about 13.5 s in ProcessPayments, beyond typical step timeouts, on every node.

**PoC.** <scratch>/ledger-transition/ledger/poc_rat_growth_test.go: TestPoCBalanceRepresentationGrowsPastMessageCap FAILs (1,560,790-byte balance; digest about 0.9 s; one payment 13.5 s; runs about 90 s).

**Suggested fix.** Fix the money unit: require amounts and the cost to be integer multiples of a governed smallest unit (e.g. 10^-8), so every denominator divides a fixed D and every balance's size is bounded. Alternatively, reject any payment whose application would push a balance's encoding over a bound.
