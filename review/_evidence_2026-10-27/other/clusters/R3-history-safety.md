# R3-history-safety — new findings from the second pass (not yet independently verified)

## VBS-N2 (found by verifier group 'burn-slash') — Reboot forgets the consensus lock: a ≥1/3-power validator restarting mid-height enables a fork with zero Byzantine validators
- severity/confidence claimed: high/high
- locations: node/node.go:466-486, node/persist.go:41-67, consensus/driver.go:100-104, TODO.md:158-162, docs/security.md:59
- attacker: No attacker needed: an honest crash plus a partition. Someone who can force a reboot (power, an OOM-inducing DoS) and partition a LAN could engineer it.

**Description.** Tendermint-style safety assumes an honest validator never forgets its lock or its votes. Here both live only in the driver. After any restart the node re-enters the open height at round 0, unlocked, with a new candidate block. A validator holding ≥1/3 of power forms a quorum with each side of a partition. So its pre-crash precommit (counted with A) and its post-restart votes (counted with B) produce two valid ⅔ commit certificates for different blocks at the same height. A restart is effectively an amnesia fault that counts against the 1/3 budget, yet the deployment advice ('concentrate stake on the machines you can keep awake') encourages exactly such validators. This contradicts docs/security.md's 'no consensus fork under ≤1/3 Byzantine' and TODO.md's 'not a safety issue'.

**Failure scenario.** Static set X:2, A:1, B:1 (quorum 3).
1. B's Wi-Fi drops at height H.
2. X proposes block D1, which holds a payment. X and A prevote and precommit D1, and A decides D1.
3. X's process dies before A's precommit reaches it.
4. A then drops off and B returns.
5. X restarts from its H-1 snapshot, unlocked and with an empty mempool, and proposes the empty block D2. X and B decide D2 at height H.
Both certificates verify against the static set. Later, A's catch-up fast-forwards it onto the X/B branch, reverting a payment that A's clients saw finalized.

**PoC.** <scratch>/verify-burn-slash/node/zz_vbs_h1_test.go TestVBSH1LockAmnesiaForkNoByzantine: FORK at height H, and both certificates verify. Forked in 8 of 14 runs, which is every run where the precondition was reached. The 6 misses were harness timing: X's round-0 precommit was nil 4 times, and B lagged below height H twice (the in-memory harness has no catch-up).

**Suggested fix.** Use H1's persist-then-restore fix, which must restore the lock from the node's own logged non-nil precommits. A Tendermint-style HRS-monotonic signer guard alone also blocks this fork, but it turns it into a halt (healed only by later catch-up), and with VBS-N1 unfixed it hangs the node permanently. Until fixed, document that no single validator should hold ≥1/3 of power.

## VSTR-NEW-1 (found by verifier group 'sync-trust-replay') — Own snapshot rejected after rotation causes a silent restart at genesis and a rollback of committed history on a simultaneous restart
- severity/confidence claimed: high/high
- locations: node/persist.go:16-35, node/node.go:121-148, node/node.go:388-401, docs/security.md:111-117, docs/operating-a-network.md:119-127
- attacker: none (normal validator-set evolution plus a power outage)

**Description.** loadSnapshot verifies the node's OWN snapshot against genesis (n.accounts at New), and on failure the node silently starts at genesis height 1. In dynamic mode this happens once the last commit's signers carry ≤1/3 of genesis power: after a full rotation, or when a new validator holds most of a quorum (commits carry only the first quorum; 19 of 24 served checkpoints failed with all founders active). After a simultaneous outage, which security.md says self-recovers from snapshots, any genesis validators still running nodes form the height-1 validator set again and re-commit a new chain from genesis. History is silently lost, honest validators double-sign old heights, and old payments and certificates become replayable at their BlockIDs (the C14 class).

**Failure scenario.** Founder A pays X 990 at h1, making X the sole validator; X commits h2..h8 and writes snapshots. Both nodes restart. X's own snapshot (valid vs the live set, not genesis) is discarded. A, at genesis, re-commits h1, h2, and so on: X's 989.9 is gone and A is back to 1000.

**PoC.** <scratch>/verify-sync-trust-replay/node/zz_verify_c19_test.go TestVerifyC19OwnSnapshotRejectedAndRollbackAfterRotation (PASS, logs ROLLBACK)

**Suggested fix.** Accept the node's own snapshot on self-consistency only (quorum of the carried set plus digest binding), and refuse to start if snapshotPath exists but fails. See <scratch>/c19-minimal-fix.patch (no digest or preimage change).
