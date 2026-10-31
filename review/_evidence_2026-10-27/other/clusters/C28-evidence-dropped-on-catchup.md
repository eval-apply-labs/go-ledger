# C28-evidence-dropped-on-catchup

Round-1 reports merged into this cluster (1):

## [consensus-core] SLASH-1 — Equivocation evidence is dropped when a node advances a height by catch-up instead of deciding locally
- severity/confidence (round 1): low/medium
- category: governance
- locations: node/node.go:644-648, node/node.go:357-369, node/node.go:341-352, consensus/evidence.go:26
- attacker: one validator (the equivocator); exploited against nodes that resolve the height by catch-up
- known_status: New. The findings.md evidence items (#5 replay, maxEvidenceAge) concern re-including/aging evidence, not the harvest-skip on the catch-up path; this is a distinct lifetime gap. Safety (no fork) is unaffected — this is an accountability/deterrence gap.

**Description.** A driver records equivocation (two conflicting votes for one height/round/type) as transferable Evidence. But the node harvests driver.Evidence() into pendingSlashes ONLY inside commit() (the local-decide path, node.go:644). A node that instead advances the height via catchUp() — verifyCheckpoint→fastForward→startHeight, the normal recovery when it fell behind — replaces the driver without harvesting, and fastForward does not touch pendingSlashes. So a double-signer the node witnessed during that height is never queued for slashing on that node. Also, late conflicting votes that arrive after commit() ran (same height, before the next startHeight) are added to the old driver but likewise never re-harvested. This weakens the owner's stated 'two different signings = burned key' guarantee.

**Failure scenario.** Node is slightly behind. During height H it receives validator B's two conflicting prevotes (equivocation is detectable in its driver). Before it decides H locally, a peer's verified height-H checkpoint arrives; the node catches up to H+1. B's evidence, held only in the discarded H-driver, is never added to pendingSlashes — this node will never carry the slash. If every node that DID decide H locally also missed one of B's votes (loss/partition), B escapes the burn network-wide despite double-signing.

**PoC.** <scratch>/consensus-core/node/poc_consensus_core_test.go :: TestPoCEquivocationEvidenceLostOnCatchUp (PASS — driver holds the evidence, pendingSlashes empty after catchUp to H+1)

**Suggested fix.** Harvest driver.Evidence() into pendingSlashes in fastForward/catchUp as well as commit (evidence survives a height change regardless of how the height was resolved); consider retaining cross-height evidence for a bounded window.
