# C31-genesis-validation

Round-1 reports merged into this cluster (1):

## [ledger-transition] GENESIS-1 — Genesis accepts duplicate keys, zero or negative amounts and duplicate names; a duplicated key silently loses balance
- severity/confidence (round 1): low/high
- category: robustness
- locations: cmd/server/config.go:100-110, ledger/ledger.go:118-121, ledger/ledger.go:191-214, ledger/digest.go:16-19
- attacker: no attacker (operator typo)
- known_status: new

**Description.** load() validates genesis amounts only for parse errors. ProcessPayments collapses a duplicated key through its map (the last entry wins) the first time the key is touched, destroying the other entry's balance. A negative genesis balance increases the supply sum when its holder is banned, and duplicate names resolve last-wins. StateDigest's comment says the digest must not depend on input order, but SliceStable makes it depend on the order of duplicates. One config typo makes states reachable that the transition itself never produces; all nodes share the config, so there is no fork.

**Failure scenario.** PoC: genesis A:10, A:5, B:1; A pays B 1. Supply goes from 16 to 5.90, i.e. 10 units are lost beyond the 0.1 fee. Swapping the duplicate entries changes the digest.

**PoC.** <scratch>/ledger-transition/ledger/poc_genesis_duplicates_test.go: TestPoCGenesisDuplicateKeyBreaksConservation FAILs.

**Suggested fix.** Validate genesis in load(): unique canonical 32-byte keys, positive amounts, and unique names no longer than MaxAccountNameLength. Fix the digest comment.
