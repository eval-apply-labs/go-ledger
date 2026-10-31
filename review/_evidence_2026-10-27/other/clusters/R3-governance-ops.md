# R3-governance-ops — new findings from the second pass (not yet independently verified)

## VGL-N1 (found by verifier group 'governance-logic') — Pending seizure/freeze votes are public from the first vote, so the target can move its funds first
- severity/confidence claimed: medium/high
- locations: node/governance.go:141-199, node/httptransport.go:118, node/httptransport.go:209-211, node/reassignment.go:43-74
- attacker: Any key holder who is the target of a seizure or freeze; no privileges needed.

**Description.** addReassignmentVotes creates a pool entry on the first submission and relays it to the node's peers. The unauthenticated GET /hdls/governance on any node lists every pooled reassignment with its account key, amounts, voters and tally. The target of a seizure therefore sees it as soon as the first bank votes. Collecting a ⅔ vote at human pace gives the target plenty of time to move the balance in an earlier block. The certificate's PreviousAmount then no longer matches, and the entry is pruned. This is independent of C12, which is the same-block variant. It undermines the owner's freeze/claw-back model unless votes are collected off-pool.

**Failure scenario.** Banks vote one at a time over an afternoon to seize 'thief' 1000→0. The thief polls /hdls/governance, sees its key after the first vote, and pays 1000 to a fresh key in the next block. The seizure certificate becomes stale and is pruned, and the claw-back fails.

**PoC.** The mechanism is the one TestVerifyC12* uses: the thief detects the certificate via GovernanceView().Reassignments (<scratch>/verify-governance-logic/node/zz_verify_c12_test.go). To show this variant, have the thief pay as soon as the entry appears, before quorum.

**Suggested fix.** Operational, already supported: banks collect signatures off-pool and submit the complete certificate in one POST (Reassignment.Votes can carry many votes). Optionally, do not serve partial reassignment tallies on public endpoints; in the three-tier deployment the proxy can block /hdls/governance. Together with the C12 exclusion rule this closes the race.

## VGL-N2 (found by verifier group 'governance-logic') — applyReassignment creates bare accounts: no self-delegation, and a same-block drain strips the name
- severity/confidence claimed: low/high
- locations: ledger/reassignment.go:111-121, ledger/ledger.go:200, ledger/ledger.go:209, node/validators.go:34
- attacker: None needed (consistency issue); the target of a reassignment can trigger the name strip.

**Description.** A payment that creates an account gives it ValidatorNode = its own key (ledger.go:200). An account created by applyReassignment gets only PublicKey and Amount, so in dynamic mode its stake counts toward no validator (validators.go:34). A grant-funded new 'bank' is not admitted until it sends a self-delegating update. In addition, when the target is drained to zero in the reassignment block (C12), ProcessPayments drops the account and applyReassignment recreates it without Name, Description, Address or ValidatorNode. Its name is released for anyone to claim.

**Failure scenario.** In TestVerifyC12* on pristine code, T was created with Name 'thief' and ValidatorNode T. After the seizure block it has name "" and validatorNode "" (shown in the ledger PoC log and the node test log).

**PoC.** Output of TestPoCGovReassignmentSameBlockOutflowIsMintedTwice: name="" validatorNode="". Output of TestVerifyC12SeizureDefeatedThroughNode: (name "").

**Suggested fix.** In applyReassignment, create new accounts with ValidatorNode = account, matching ProcessPayments. The C12 exclusion rule and naming decision (1) together remove the drain-and-recreate path.

## VSTR-NEW-3 (found by verifier group 'sync-trust-replay') — Same-id re-genesis with a leftover snapshot file silently resumes the old chain
- severity/confidence claimed: low/high
- locations: node/persist.go:16-35, docs/operating-a-network.md:135-171
- attacker: none (operator footgun)

**Description.** The migration procedure never says to clear snapshotPath. With the same networkId and keys, the old chain's snapshot verifies against the new genesis: its signers are the new genesis validators, and the digest matches unless the release changed the digest function. New() therefore resumes the OLD state at H+1 and discards the migration edits, splitting nodes that cleared the file from nodes that did not.

**Failure scenario.** Old chain runs to height 6; re-genesis with one edited account and the same id. The node starts at height 6 with the edit lost. With a fresh id it starts at height 1.

**PoC.** <scratch>/verify-sync-trust-replay/node/zz_verify_c14b_test.go TestVerifyC14StaleSnapshotResumesOldChain

**Suggested fix.** A fresh networkId per re-genesis (the C14 fix) fences this. Also add 'clear snapshotPath/backupPath' to the migration checklist.
