# C32-float-topology

Round-1 reports merged into this cluster (1):

## [ledger-transition] TOPO-1 — Consensus-critical Acceptable() depends on float Pow/Ceil at integer boundaries
- severity/confidence (round 1): info/low
- category: safety
- locations: ledger/topology.go:26, ledger/topology.go:36, ledger/parameters.go:76-97, ledger/governance.go:317-323
- attacker: no attacker (bug)
- known_status: new

**Description.** GroupParameters derives the gossip dimension with math.Ceil(math.Pow(n, 1/e)). That dimension decides TimingConsistent → Acceptable → ParameterChange.Valid inside ApplyBlock. math.Pow is not correctly rounded and is not bit-identical across Go ports: amd64 uses assembly Exp/Log, other ports use pure Go. At perfect powers the result can land just above or below the integer: on amd64, 125^(1/3) = 5.0000000000000009, so Ceil gives 6. Comparing amd64 with GOARCH=386, 5 of 141 perfect powers differ bit-wise, but none of those differences straddle an integer.

**Failure scenario.** Hypothetical: validators on two architectures compute different dimensions for the same (participants, maxPeers). A ⅔-ratified profile whose StepTimeoutMs sits between the two thresholds is then valid on one side and invalid on the other, and the block carrying it splits the validators.

**PoC.** none: the only evidence is a standalone script comparing math.Pow bits on amd64 and 386 (5 of 141 differ); no divergence that crosses an integer was demonstrated.

**Suggested fix.** Compute the group size and dimension with integer arithmetic (an integer k-th root by binary search).
