# C10-governance-pool-squat

Round-1 reports merged into this cluster (2):

## [governance] GOV-3 — Governance vote pools are fillable with unauthenticated voteless entries, freezing governance permanently
- severity/confidence (round 1): high/high
- category: dos
- locations: node/governance.go:41, node/governance.go:49, node/governance.go:229, node/reassignment.go:49, node/reassignment.go:279, node/httptransport.go:186
- attacker: anyone who can reach the node's HTTP port (unauthenticated; no validator key)
- known_status: Relates to deferred #10 (unauthenticated write API / rate limiting), but is a distinct logic flaw, not just a volumetric concern: entries are pooled before verification and the cap+no-eviction makes a single 128-request burst a PERMANENT freeze that rate-limiting alone does not fix. New angle / materially worse than #10 as recorded.

**Description.** addParameterVotes/addReassignmentVotes allocate a pool slot for a proposal BEFORE verifying any vote (governance.go:47-54, reassignment.go:49-54): a ParameterChange with an Acceptable profile and ZERO (or bogus-signature) votes still creates an entry. The pools are capped at 64 each. pruneParameterProposals/pruneReassignments only drop entries whose sequence or value guard no longer matches current state (governance.go:229-236) — a voteless entry bound to the current sequence/digest (or to balance 0 of some unknown key) is NEVER pruned. Once 64 junk entries fill a pool, a legitimate proposal is a new key and is refused by the >=64 cap, so it never pools or gossips. The governance sequence can only advance via a pooled certificate reaching quorum, which can no longer happen — permanent deadlock. All endpoints are unauthenticated (httptransport.go:186-205); GET /hdls/governance hands the attacker the current sequence/digest to bind.

**Failure scenario.** An attacker who can reach the node port (no key needed) reads GET /hdls/governance, then POSTs 64 distinct voteless ParameterChanges (each an Acceptable profile binding the current digest+sequence) to /hdls/parameter-vote and 64 voteless Reassignments (distinct unknown accounts, prev=0) to /hdls/reassignment-vote — 128 small requests per node. Both pools are now full of un-prunable junk. A legitimate validator's genuine vote for a fee change or a reassignment is refused at the >=64 cap, never pools, never gossips, never enacts. Governance is frozen for the life of the network. PoC: a quorum-1 validator's fully-valid fee change AND reassignment never enact after the squat; pools stay 64/64 across 15 blocks.

**PoC.** <scratch>/governance/node/zz_govreview_poc_test.go (TestPoCGovPoolSquatFreezesGovernance) — FAILs: GOVERNANCE FROZEN, quorum-1 change+reassignment never enact

**Suggested fix.** Do not pool a proposal until it carries at least one VERIFIED current-validator vote; key/evict by whether progress is possible; or reserve capacity / use a per-sender bound. At minimum, drop pooled entries that have made no progress and never pool a voteless certificate.

## [node-input] NODE-IN-3 — Governance vote pools create a proposal slot before verifying any vote — unauthenticated 64-slot fill permanently bricks parameter/reassignment voting
- severity/confidence (round 1): medium/high
- category: governance
- locations: node/governance.go:41-72, node/governance.go:49-53, node/reassignment.go:43-74, node/reassignment.go:51-55, node/httptransport.go:186-205
- attacker: anyone reaching the port
- known_status: New. Within deferred #10's area but a specific create-before-verify defect with a concrete governance-liveness impact and a persistence (pruning-evasion) twist that #10's generic 'rate-limit the port' framing does not capture.

**Description.** handleParameterVote/handleReassignmentVote are unauthenticated. addParameterVotes (governance.go:41) checks only NewParameters.Acceptable, then CREATES the pool entry (governance.go:52-53) before iterating/verifying any carried vote; a change with zero valid votes still occupies a slot. The pool is capped at 64 (governance.go:49), so once full, a genuine validator's valid vote for a new proposal is refused (return false, not pooled, not relayed, not counted). addReassignmentVotes (reassignment.go:43) is the same and has no Acceptable gate at all. GET /hdls/governance exposes the current parameters and GovernanceSequence, so an attacker sets PreviousParametersDigest/Sequence to the current values and the junk entries survive commit-time pruning (pruneParameterProposals/pruneReassignments only drop stale-digest/sequence entries).

**Failure scenario.** Attacker POSTs 64 distinct, vote-less ParameterChange messages (varying BlockIntervalMs; PreviousParametersDigest=current, Sequence=current) to /hdls/parameter-vote on each node. Pools fill to 64 and survive pruning. Every subsequent genuine validator parameter-change vote is refused at intake, so no proposal ever reaches quorum and no proposer can carry one — governance (fee changes, reassignments/grants) is bricked until nodes restart, whereupon the attacker refills (64 tiny requests/node).

**PoC.** <scratch>/node-input/node/zz_poc_test.go (TestPoCGovernancePoolFillBlocksLegitVotes) — PASS: 64 vote-less junk proposals fill + survive pruning; a real validator's valid vote is refused

**Suggested fix.** Do not allocate a pool entry until at least one carried vote verifies as a current validator (move the create below the vote loop, or require >=1 verified vote before insertion). Count the 64 cap over proposals that hold >=1 verified validator vote.
