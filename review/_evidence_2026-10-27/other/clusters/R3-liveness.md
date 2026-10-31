# R3-liveness — new findings from the second pass (not yet independently verified)

## NEW-3-partial-peerdns-halt (found by verifier group 'completeness-critic-pass2') — Incremental on-chain address publication (the documented peer-DNS rollout) asymmetrically cuts the gossip graph and permanently halts consensus with all validators honest
- severity/confidence claimed: high/high
- locations: node/peerdns.go:25-46 (resolvedPeerAddresses resolves neighbours ONLY among validators that have published an address), node/peerdns.go:11-20 (resolvePeersFromState calls SetPeers only when resolvedPeerAddresses is non-nil), node/topology.go:76-116 (Neighbors returns nil when self is not in the key list), docs/operating-a-network.md:52 and README.md:97-98 (document publishing addresses 'in genesis or via an update' as equivalent)
- attacker: None — triggered by normal operator action (the documented incremental address-publication path). An attacker-validator could also trigger it deliberately by being the first/only publisher.

**Description.** When a proper non-empty subset of validators has published an on-chain address, resolvedPeerAddresses on a publisher returns Neighbors over only the address-holders, so each publisher's transport is SetPeers() to just the other publishers — it stops gossiping to the non-publishers. A non-publisher's resolvedPeerAddresses returns nil (its own key is not among the address-holders, so Neighbors returns nil), so it keeps its static all-to-all peers and still broadcasts to everyone, but never hears back from the publishers. The graph is now asymmetric: publishers {0,2} talk only to each other; non-publishers {1,3} broadcast to all but receive nothing from {0,2}. No node ever assembles a 3-of-4 quorum whose precommits also propagate — {0,2} get prevote-quorum but their precommits reach only each other; {1,3} never reach prevote-quorum — so no block ever commits. Because publishing the remaining addresses needs a committed block, there is no recovery and no checkpoint to catch up to. This is reachable by following the documented 'publish via an update' rollout, or by a genesis where only some validators carry an address.

**Failure scenario.** 4 equal honest validators. Two of them submit the documented AccountUpdate publishing their gossip address (bound to block 2). Blocks 1-2 commit (both addresses land in committed state on all nodes). From height 3 the gossip graph is cut as above and the network stops: PoC shows heights [3,3,3,3] after 6s versus the control (no publication) racing to height 76. No attacker; no node recovers.

**PoC.** .../scratchpad/verify-critic/node/poc_critic_peerdns_test.go::TestPoCCriticPartialPeerDNSPublicationHalts -> FAIL. publishers=[] (control) reaches height 76; publishers=[0,2] halts at height 3 (addresses confirmed committed on all nodes).

**Suggested fix.** Fall back to the UNION of resolved addresses and the static/all-to-all peers while any current validator still lacks a published address (resolve peers only once EVERY current validator has an address), and make Neighbors over a key set that excludes self still connect self to that set. Document that addresses must be set for all validators in genesis, atomically, not rolled out incrementally.

## VSTR-NEW-2 (found by verifier group 'sync-trust-replay') — Running stragglers can never catch up once commits lack >1/3 genesis power: bestCheckpoint's genesis filter runs before the live-anchored check
- severity/confidence claimed: medium/high
- locations: node/sync.go:38-55, node/sync.go:87-106, node/node.go:357-369, docs/consensus.md:118-121
- attacker: none

**Description.** catchUpLoop → bestCheckpoint verifies every candidate against cfg.Genesis before the loop's catchUp re-verifies it against live state, although consensus.md says the anchor is 'genesis, or its last committed state'. After a full or partial rotation, honest checkpoints never reach catchUp, so a node that misses one commit window is stuck permanently. Sleeping laptops are in the threat model, so attrition can eventually remove >1/3 of live power. The existing TestStragglerCatchUp enqueues evCatchUp directly, bypassing bestCheckpoint, so the suite never covers this.

**Failure scenario.** HTTP network rotated A→X; observer S is partitioned for 1 s and healed. S stays at height 4 while X reaches 178, and bestCheckpoint over X's checkpoint finds nothing. The same checkpoint handed directly to catchUp is accepted.

**PoC.** <scratch>/verify-sync-trust-replay/node/zz_verify_c19_test.go TestVerifyC19StragglerStuckAfterRotation; partial-rotation rate in zz_verify_c19b_test.go

**Suggested fix.** Anchor bestCheckpoint on the last committed accounts, published through an atomic pointer at New/commit/fastForward (in c19-minimal-fix.patch). All existing tests pass and the race detector is clean.

## NEW-7-nonvalidator-maxpeers-isolated (found by verifier group 'completeness-critic-pass2') — cmd/server load() discards the operator's peers list for a node whose key is not in validators when maxPeers>0, starting it with zero gossip peers
- severity/confidence claimed: low/high
- locations: cmd/server/config.go:196-216 (maxPeers>0 path replaces peers with Neighbors(validatorKeys, fc.PublicKey); Neighbors returns nil if self is not a validator key), node/topology.go:84-87 (Neighbors returns nil when self not found in keys)
- attacker: None (operator config footgun / typo).

**Description.** With maxPeers>0 and a URL on every validator, load() replaces the configured peers with this node's Hypernode neighbours among the validator keys. If the node's own publicKey is not one of the validator entries (a non-validator reference/light node, or a simple key typo), Neighbors returns nil, so effective peers is empty: the node serves HTTP, admits payments and signs receipts, but relays to nobody and hears from nobody except via inbound POSTs. Silent — no error at load.

**Failure scenario.** PoC: a node whose key is not in the 4-entry validators list, maxPeers=2, peers set to two real URLs -> load() returns effective peers = [] (the configured peers are discarded).

**PoC.** .../scratchpad/verify-critic/cmd/server/poc_critic_peers_test.go::TestPoCCriticNonValidatorWithMaxPeersGetsNoPeers -> FAIL (configured 2 peers, effective 0).

**Suggested fix.** If Neighbors returns nil under maxPeers>0 (self not in the validator universe), fall back to the configured static peers rather than an empty list, or fail load() with a clear 'this node's publicKey is not among validators' error.

## NB-1 (found by verifier group 'names-ban') — Zero-balance or ban deletion drops a static validator's on-chain Address, so peers stop dialing a validator that still votes
- severity/confidence claimed: low/medium
- locations: ledger/ledger.go:191-214, ledger/block.go:72-89, node/peerdns.go:resolvedPeerAddresses, node/node.go:670
- attacker: No attacker needed (an operator emptying the validator account), or a single dishonest proposer holding one over-balance signature from the validator's key

**Description.** ProcessPayments deletes any account whose balance reaches 0, and banned payers too. That removes its Address, the on-chain peer-DNS record. resolvedPeerAddresses lists only validators that have an Address in state, and resolvePeersFromState calls SetPeers with that list after every commit.

In static mode the validator stays in the configured set and keeps voting, but peers resolving from state stop sending it proposals and votes. It is effectively offline for consensus while it still counts toward quorum. The decided D1 rule keeps only named accounts, so unnamed validator accounts are still exposed.

**Failure scenario.** Three static validators publish addresses. Validator B spends its account to zero, or a dishonest proposer bans it with a hoarded over-balance payment (C06). A's gossip targets go from [c, b] to [c]. With n>=4 and quorum 3, two such validators stall the chain.

**PoC.** <scratch>/verify-names-ban/node/verify_addr_loss_test.go TestVerifyAddressLostOnZeroBalance prints targets before=[http://c:1 http://b:1] and after=[http://c:1], with B still in the static set. This is mechanism only; the network-level stall was not run. With exactly 2 validators the resolved list is empty, so SetPeers is skipped and the stale peers are kept.

**Suggested fix.** Extend D1's keep condition to accounts that carry an Address (`|| old.Address != ""`). Alternatively, fall back to the configured URL for a static validator that has no on-chain Address.
