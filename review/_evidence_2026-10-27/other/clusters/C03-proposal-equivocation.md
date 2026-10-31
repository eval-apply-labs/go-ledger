# C03-proposal-equivocation

Round-1 reports merged into this cluster (2):

## [burn-rule] PROP-1 — Proposer equivocation (two different signed proposals for one height/round) is never detected or slashed — and the second is relayed and re-applied network-wide
- severity/confidence (round 1): medium/high
- category: safety
- locations: consensus/driver.go:203, consensus/driver.go:204, consensus/evidence.go:26, node/node.go:498, node/node.go:507, node/node.go:516
- attacker: one validator (the scheduled proposer for a round)
- known_status: new. Distinct from findings.md #2/#8 (unauthenticated proposals) and the RC-review 'duplicate proposal re-relayed unverified' (same-digest body swap): this is a DIFFERENT-digest equivocation by the AUTHENTIC proposer, which no code detects.

**Description.** Proposals are signed (DomainProposal) but there is no proposal-equivocation evidence: Driver.Evidence (evidence.go:26-35) scans only vote sets, and the driver keeps the first proposal per round (driver.go:203-204). onProposal's held-proposal fast path only short-circuits on a MATCHING Block.StateDigest (node.go:498); a SECOND, different-digest proposal from the authentic round proposer passes the proposer+signature gate (node.go:507), is ApplyBlock'd, and is relayed onward (returns true, node.go:516). So a proposer can sign two conflicting blocks for one (height,round) with zero penalty — the exact 'two different signings' the principle targets, for the one role that is never burned.

**Failure scenario.** The round proposer signs block X and block Y (different payments, different digest) for (h,r) and sends X to half the peers and Y to the other half. Each honest node records whichever it saw first and prevotes it; prevotes split, the round fails to reach ⅔, and it times out to nil. Every node also runs ApplyBlock on BOTH blocks and relays both (2× validation work network-wide). The proposer is never slashed. A validator with ~p of stake proposes ~p of rounds and can tax every such round this way — a sustained, unpunished liveness/CPU drain from a validator position. Safety holds (locking + quorum), so it is griefing, not a fork.

**PoC.** <scratch>/burn-rule/node/burnrule_poc_test.go — TestPoCProposalEquivocationUndetectedAndRelayed (FAIL: both distinct proposals accepted/relayed, 0 evidence produced).

**Suggested fix.** Treat two validly-signed proposals for the same (height,round) with different digests as equivocation: record a proposal-evidence pair, carry it into a block, and slash it like vote equivocation. Also stop relaying a second distinct-digest proposal from the same proposer (relay only the first per (height,round,proposer)).

## [consensus-core] SLASH-2 — Proposal equivocation is unslashable and both conflicting proposals are relayed
- severity/confidence (round 1): low/high
- category: liveness
- locations: node/node.go:494-517, node/node.go:498-506, consensus/evidence.go:13-16
- attacker: one validator (whichever is the round's scheduled proposer)
- known_status: New. Cannot fork (safety unaffected) and cannot stall permanently (honest-proposer rounds still commit); it is a bounded liveness/bandwidth tax plus a hole in the burned-key principle the owner cares about.

**Description.** The owner's 'two signings = burned key' principle is enforced for VOTES (prevote/precommit → Evidence → slash) but not for PROPOSALS. The round's scheduled proposer can sign two different, validly-signed proposals for the same (height,round) — two signatures over conflicting statements by one key — and there is no evidence type or slash for it. Worse, onProposal relays BOTH: the held-proposal fast path (node.go:498) only dedups an IDENTICAL StateDigest, so a second, different-digest proposal from the same proposer passes the proposer+signature checks and returns true (relayed), flooding a second full block and splitting that round's prevote.

**Failure scenario.** Byzantine proposer for (H, r) sends block A to half the nodes and block B to the other half, both validly signed. Each node relays whichever it sees first AND relays the other on arrival (fast path misses, different digest). The round's prevote splits; the round times out to nil. The proposer is never recorded as a double-signer. Over a height it can do this on every round it is selected for (~its stake fraction), a liveness tax plus doubled proposal bandwidth, at zero cost to its key.

**PoC.** <scratch>/consensus-core/node/poc_consensus_core_test.go :: TestPoCProposerEquivocationUnslashedAndBothRelayed (PASS — both conflicting proposals accepted/relayed, no pendingSlashes entry)

**Suggested fix.** Treat two differently-signed proposals for one (height,round) by the scheduled proposer as slashable proposal-equivocation evidence (a new Evidence variant over the proposal preimage); and never relay a second proposal for a round already held.
