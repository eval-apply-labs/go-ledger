# C24-wallet-blockid-counter-poison

Round-1 reports merged into this cluster (1):

## [wallet-ui] KEY-3 — Persisted payment-block counter is poisoned permanently by one bad/malicious height, and survives a documented re-genesis — payments then never land
- severity/confidence (round 1): medium/high
- category: liveness
- locations: ui/wallet.go:168-178, ui/wallet.go:180-191, ui/wallet.go:70-85, ui/USER_MANUAL.md:48-49
- attacker: the wallet's reference node (one bad /hdls/status), or no attacker at all (a documented re-genesis / chain reset)
- known_status: materially worse than #29 (height() returns 0 on node error, accepted as benign). Here a non-zero out-of-range height is PERSISTED and poisons all future payments across restarts and across a switch to honest infrastructure; the re-genesis interaction (with the manual's copy-both-files guidance) gives a no-attacker trigger.

**Description.** nextPaymentBlockID = max(height()+lookahead, lastPaymentBlockID+1), then persists the result to <wallet>.state. height() trusts the node's /hdls/status with no sanity bound, and the persisted counter is monotonic and NOT scoped to a network/genesis. One out-of-range height — a dishonest or buggy reference node reporting a huge value, or simply the OLD chain's height carried across a documented re-genesis (migration restarts at height 1, but the manual tells users to copy wallet.json AND wallet.json.state together) — latches lastPaymentBlockID to that large value forever. Every later payment, even against an honest node and across restarts, then names a far-future BlockID. The node accepts it (admitPayment only rejects BlockID < height, node/node.go:436) and signs a receipt, but the payment waits for a block that effectively never arrives (mempool.dropBelow only evicts past blocks), so it never commits: a 'receipt but money never moved' outcome the user cannot clear.

**Failure scenario.** PoC: node reports height 2^40 once; wallet signs one payment (persists 2^40). Node then reports honest height 10; after restart the wallet still names block 1099511627780 — every future payment is unspendable. Re-genesis variant needs no attacker: after a migration to height 1, a wallet whose .state holds 5000 names block 5001+ and cannot pay on the new chain until it reaches 5001.

**PoC.** <scratch>/wallet-ui/ui/poc_blockid_test.go (TestPoCPersistedBlockIDCounterUnbounded) — FAILS: 'with the honest tip at height 10 the next payment names block 1099511627780'.

**Suggested fix.** Clamp/sanity-check height(): ignore a status height that is wildly beyond the last observed one, and never let a single report jump the counter by more than a bounded window. Scope the .state file to (networkID, genesis digest) so it resets on a new chain. Cap nextPaymentBlockID at height()+lookahead+small-slack and detect/log when the persisted counter is far ahead of the live tip.
