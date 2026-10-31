# C15-block-size-unbounded

Round-1 reports merged into this cluster (1):

## [ledger-transition] BLOCK-SIZE-1 — No per-block size bound: fee-free updates push every proposal past peers' 1 MiB decode limit and halt the chain
- severity/confidence (round 1): high/high
- category: liveness
- locations: ledger/block.go:105-127, ledger/block.go:180-204, ledger/account_update.go:112-160, node/node.go:283-288, node/updatepool.go:130-141, node/node.go:466-486, node/httptransport.go:302-314
- attacker: any account holder (no fee; a 1-unit balance suffices)
- known_status: new. The findings.md third pass added a per-message bound only; there is no per-block bound. Distinct from deferred #10 (request flooding).

**Description.** MaxMessageBytes bounds each message, but nothing bounds a block. ProposeBlock carries every valid payment and update naming the height, and ApplyBlock accepts any number of them. Every receiver, though, decodes a proposal under a 1 MiB body limit. Account updates carry no fee, and the update pool admits any BlockID. So pooled content for one height can push every honest proposer's candidate (built once per height from the whole gossiped pools) past 1 MiB. Peers fail to decode it and prevote nil. The next proposer's candidate fails the same way, and nothing evicts the content, because pools are pruned only on commit.

**Failure scenario.** PoC on a 4-validator HTTP network: one account holding 1 unit signs 600 distinct updates, each 2000 bytes or less (within the default cap), all naming height 3, and they are gossiped to all nodes. Heights 1-2 commit. The candidate for height 3 serializes to 1,159,040 bytes, and the chain stays at height 3 for 100 s with all validators up (heights=[3 3 3 3]). A control run with 400 updates (772,841 bytes) commits in 1.2 s. Below-cost payments (FEE-1) are a second fee-free way to fill a block.

**PoC.** <scratch>/ledger-transition/node/poc_oversize_block_test.go: TestPoCOversizeBlockStallsHeight FAILs (stalled at height 3); control TestPoCOversizeBlockControlUnderLimit PASSes. Run with GOLEDGER_CHAOS_SLACK=4.

**Suggested fix.** Add a governed per-block byte budget (well under the transport limit) plus a count cap. ProposeBlock fills deterministically up to it, and ApplyBlock rejects blocks over it. Charge a fee for account updates or cap them per account per block. Refuse BlockIDs beyond height+window at intake, and drop pooled content for a height after N failed rounds.
