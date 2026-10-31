# C11-departed-voter-poisons

Round-1 reports merged into this cluster (1):

## [governance] GOV-5 — A departed voter's stuck pool vote poisons a proposal so a clear live quorum can never carry it
- severity/confidence (round 1): medium/high
- category: liveness
- locations: node/governance.go:55, node/governance.go:88, node/governance.go:229, ledger/governance.go:79, ledger/reassignment.go:97
- attacker: one minority validator (griefing), or ordinary non-adversarial validator-set churn/slashing
- known_status: New. Not in findings.md/TODO.md; distinct from the C5 deterministic-selection fix (that was about map order, this is about stale votes never being evicted).

**Description.** ParameterChange.Valid / Reassignment.Valid reject the WHOLE certificate if ANY carried vote is from a key not in the current validator set (governance.go:79, reassignment.go:97). The node's pools accumulate votes by validator and never remove one (governance.go:55-71); addParameterVotes dedups but cannot drop a vote, and pruning only deletes a whole entry on a sequence/value change. So if a validator votes for a pooled proposal and then leaves the set (re-delegates, drops below the dynamic threshold, or is slashed), its signature stays wedged in that pooled certificate. pendingParameterChange calls Valid on the pooled entry, which now always returns false, so the proposal can never be carried — even though the remaining CURRENT validators hold a clear >⅔. The admin view still shows it 'ready ✓'. Re-submitting the good votes is a no-op (same pool key, already-voted). A fresh clean entry can't be created (key occupied).

**Failure scenario.** Dynamic set v1(60) v2(30) v3(15). Small validator v3 votes for a fee change, then re-delegates its stake to v1 (one signed account update) — leaving the set. v1 and v2 (now 105 of 105 power, well above quorum 71) both vote. The pooled certificate is [v3,v1,v2]; Valid hits v3 (not a current validator) and returns false, so pendingParameterChange returns nil and the change is never proposed, despite a 105/71 live quorum. PoC: votedPower=105, reached=true, amendsCurrent=true, yet pendingParameterChange()=false; a fresh [v1,v2] certificate is Valid=true but cannot be pooled. A minority validator can thus veto any proposal it voted on by moving its stake once; ordinary validator churn/slashing wedges in-flight proposals silently.

**PoC.** <scratch>/governance/node/zz_govreview_poc_test.go (TestPoCGovDepartedVoterPoisonsPooledProposal) — FAILs: votedPower 105 of quorum 71, reached=true, but pendingParameterChange()=false

**Suggested fix.** When building/validating a pooled certificate, filter its votes to current validators (drop non-members) rather than rejecting the whole certificate, and prune votes from departed validators each commit. Re-tally the surviving current-validator votes against the quorum.
