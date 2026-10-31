# C14-regenesis-networkid-replay

Round-1 reports merged into this cluster (2):

## [key-lifecycle] KEY-2 — Signatures bind only the network-id string, not a genesis/chain digest — same-id deployments and the documented re-genesis migration replay payments and governance certificates
- severity/confidence (round 1): medium/high
- category: naming
- locations: signing/domain.go:38-59, ledger/payment.go:159-173, ledger/reassignment.go:48-57, ledger/parameters.go:251-263, cmd/demo/main.go:35, ui/main.go:31, docs/operating-a-network.md:135-158
- attacker: anyone who observed an old signed payment/certificate (every gossip peer / validator / archive), given a re-genesis or a second same-id deployment
- known_status: New. C6 (findings.md) tests cross-NETWORK rejection but only with DIFFERENT ids; the universal-default collision and the lack of genesis binding are not covered. The 2026-07-31 re-genesis note addresses mixed-version digest divergence, not replay of old signatures onto the re-genesised chain.

**Description.** Every preimage binds protocolTag + networkID + domain + fields, but nothing binds a genesis/chain identity. networkID is a free-form string whose default is "village" in the demo, the operator example config, the wallet and the user manual. Consequences: (a) two independently-stood-up "village" networks are NOT isolated — a payment or governance certificate signed on one is signature-valid on the other wherever the key/account exists; (b) the documented migration/upgrade path (operating-a-network.md 'Migrating the ledger'; findings.md 2026-07-31 'coordinated re-genesis') restarts heights at 1 and the governance sequence at 0, so unless it also changes the scheme (re-keys) or the payment signFields, old signed payments and — most sharply — old reassignment grant certificates re-apply on the new chain, re-minting value with no new vote. The docs claim networkId 'isolates testnet/mainnet/forks', which the mechanism does not deliver across same-id deployments or a same-id re-genesis.

**Failure scenario.** A community re-genesises its "village" chain (a plain restart, or a digest-field software upgrade that leaves reassignment-vote fields untouched) without changing the id or re-keying. Anyone who kept an old ⅔ grant certificate (prev 0 → 100, sequence 0) resubmits it: on the fresh chain the newcomer is at 0 and sequence is 0, so Valid() passes and 100 is minted again with no new vote (PoC). Separately, two co-located operators each default to "village"; a validator present on both has its governance votes/payments cross-replayable.

**PoC.** <scratch>/key-lifecycle/ledger/keylife_regenesis_test.go — 3 tests FAIL asserting the safe behaviour (payment re-applies after re-genesis; grant certificate re-ratifies at reset sequence; cert valid on a different same-id deployment). Also <scratch>/key-lifecycle/ledger/poc_networkid_test.go — PASS confirming cross-deployment Valid() and id-only VerifyPayment.

**Suggested fix.** Bind a unique chain identifier (genesis digest, or a random chain-id chosen at genesis) into the signing preimage alongside networkID, so signatures do not cross a re-genesis or an id collision; at minimum, make keygen/config generate a random networkId default and document that a re-genesis MUST change it (and that migration should re-key).

## [governance] GOV-6 — Governance certificates and payments replay across a re-genesis (sequence and BlockID reset, networkId reused)
- severity/confidence (round 1): medium/high
- category: safety
- locations: ledger/parameters.go:198, node/node.go:98, node/node.go:128, ledger/governance.go:31, ledger/reassignment.go:265, docs/operating-a-network.md:160
- attacker: anyone retaining an old-chain certificate or payment (all nodes pooled/gossiped them), after an operator-run re-genesis
- known_status: New. The within-chain cycle replay is correctly closed by the sequence (verified: TestReassignmentSequenceGuardBlocksCycleReplay, TestChaosGovernanceCycleReplayStaysInert). This is the CROSS-genesis reset the sequence does not cover; the re-genesis procedure in operating-a-network.md does not warn of it (docs-drift component).

**Description.** The only replay anchors for governance are the GovernanceSequence (parameters.go:198) and, for payments, BlockID. Both are chain-local and restart at 0/height-1 when a node boots fresh (node.go:98,128). The vote/payment preimages are bound to networkId but NOT to any genesis identifier. operating-a-network.md:136-171 documents re-genesis as the normal path for ANY release that changes the state digest or signing preimage ('restart every node on the new version from the captured state'), reusing the SAME networkId and validator keys and re-asserting balances. On the reseeded chain every old certificate/payment every node pooled or gossiped is signature-valid again at its old sequence/BlockID; it applies whenever its value guard matches (a prev=0 newcomer grant matches immediately if the grantee holds 0). The doc says only 'the cryptography (and the genesis hash) change' — but a plain software upgrade keeps the scheme and networkId, so nothing stops the replay.

**Failure scenario.** On the old chain, seq 0 grants a newcomer 500; the newcomer later spends it. The community does a documented software-upgrade re-genesis from the captured state (same networkId, same keys). On the new chain the GovernanceSequence starts at 0 again. Anyone holding the old grant certificate (it was gossiped network-wide) resubmits it verbatim; it is Valid at sequence 0 (newcomer holds 0 on the new chain) and re-mints 500 with no new vote. PoC also shows the old height-2 payment replaying at height 2 of the new chain. Supply inflates by every historically-ratified grant and every re-submittable old payment.

**PoC.** <scratch>/governance/ledger/zz_govreview_poc_test.go (TestPoCGovCertificateReplaysAcrossReGenesis) — FAILs: old grant re-mints 500 and old payment replays on the reseeded chain

**Suggested fix.** Bind a genesis identifier (genesis digest / an epoch counter) into every signed preimage (votes and payments), or mandate a fresh networkId on every re-genesis and say so prominently in operating-a-network.md. Then old signatures are invalid on the new chain.
