# C34-nonvote-equivocation

Round-1 reports merged into this cluster (1):

## [burn-rule] GOV-1 — Account-update and governance-vote equivocation are undetected (two conflicting signings, no burn) — benign today, but the principle is absent
- severity/confidence (round 1): info/high
- category: governance
- locations: ledger/account_update.go:83, ledger/governance.go:78, ledger/reassignment.go:185, signing/domain.go:14
- attacker: one validator (benign under current guards)
- known_status: new (matrix completeness). No prior finding covers non-vote equivocation detection; the dead domains are not noted in findings.md.

**Description.** Two conflicting AccountUpdates from one key for the same block both apply in order (account_update.go:83-114, last-wins, deterministic) — no conflict detected. A validator may sign parameter-vote (governance.go) and reassignment-vote (reassignment.go) approvals for multiple competing proposals at the same governance sequence; none is flagged as equivocation. These are benign: account-update last-wins is deterministic and same-key-only, and quorum intersection plus the single-use GovernanceSequence prevent two conflicting governance actions from both ratifying. But the burn principle is not applied here either. Separately, the declared domains validator-message (DomainValidatorMsg) and server-auth (DomainServerAuth) are UNUSED in the codebase (no Sign/Verify caller) — reserved slots, so no statement of those types exists to conflict.

**Failure scenario.** No concrete harm. A validator double-votes YES on two contradictory reassignments at sequence N (e.g. grant Alice 1000 and grant Bob 1000); only one can enact because applying it bumps the sequence and the other becomes stale — no double-mint. A key signing two conflicting account-updates only changes its own attributes twice (last wins). Recorded for completeness of the matrix and because any future weakening of the quorum/sequence/determinism guards would turn these undetected equivocations into live risks.

**PoC.** <scratch>/burn-rule/ledger/burnrule_poc_test.go — TestPoCConflictingAccountUpdatesBothApply (FAIL: both conflicting same-key updates applied, last wins, no detection).

**Suggested fix.** Optional accountability only: detect and record conflicting same-key governance/account-update signings for the same slot if a social-punishment trail is wanted. Remove or document the unused validator-message/server-auth domains so they are not assumed to be protected statement types.
