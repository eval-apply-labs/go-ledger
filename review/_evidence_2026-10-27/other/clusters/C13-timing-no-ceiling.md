# C13-timing-no-ceiling

Round-1 reports merged into this cluster (1):

## [governance] GOV-2 — LedgerParameters.Acceptable has no upper timing bound: a ⅔ vote can halt the chain irreversibly
- severity/confidence (round 1): high/high
- category: liveness
- locations: ledger/parameters.go:76, ledger/parameters.go:89, node/node.go:620, node/node.go:671, ui/admin.go:81
- attacker: a ⅔ validator coalition, or any single validator in a set whose quorum it meets (e.g. a one-validator or skewed-power deployment)
- known_status: New. TODO.md 'More governable parameters' notes 'extend Acceptable if it has an invariant' and the MinMessageBytes floor is the precedent for brick-proofing — the timing fields were given a lower bound but no upper bound. Not in findings.md.

**Description.** Acceptable/TimingConsistent (parameters.go:76-97) enforce only LOWER bounds: StepTimeoutMs>0, BlockIntervalMs>=0, MaxPeers>=0, StepTimeoutMs >= dimension*250. There is no CEILING on BlockIntervalMs or StepTimeoutMs. A ⅔-ratified ParameterChange can set BlockIntervalMs to billions of ms (centuries). After the enacting block commits, node.commit schedules evNextHeight that far in the future (node.go:671-678 via blockInterval), so the chain produces no further block. Because every governance action (including a revert) needs a block, nothing on-chain can undo it — the network is bricked until a manual re-genesis. MinMessageBytes exists precisely so governance cannot vote a chain-bricking value; the timing fields have no equivalent guard, despite the wallet/docs treating Acceptable as the brick-proofing. (StepTimeoutMs=MaxInt64 instead overflows time.Duration to negative, causing immediate-timeout round churn.)

**Failure scenario.** Validators holding ⅔ (or one validator in a small set) vote a profile with BlockIntervalMs=86_400_000_000 ('one day' mistyped with extra zeros, ~2.7 years) and otherwise-current fields. It passes Valid (Acceptable=true) and enacts. The enacting block commits; the next height is scheduled ~2.7 years out. The validator immediately votes the revert — it pools but is never carried into a block, because no block is ever produced. Chain halted permanently; only an out-of-band coordinated re-genesis recovers it. A hostile ⅔ coalition can do this deliberately as a kill switch; an honest fat-finger does it by accident.

**PoC.** ledger: .../governance/ledger/zz_govreview_poc_test.go (TestPoCGovAcceptableAllowsChainHaltingTiming) — FAILs, Valid=true for 2.7-yr and 285-yr intervals and MaxInt64 step. node: .../governance/node/zz_govreview_poc_test.go (TestPoCGovHugeBlockIntervalHaltsChain) — FAILs: live node stops committing, revert never enacts

**Suggested fix.** Add sane upper bounds to Acceptable (e.g. BlockIntervalMs and StepTimeoutMs each <= a few minutes/hours max), mirroring the MinMessageBytes floor, so a ratified profile can never stop block production. Also clamp StepTimeoutMs to avoid the Duration overflow.
