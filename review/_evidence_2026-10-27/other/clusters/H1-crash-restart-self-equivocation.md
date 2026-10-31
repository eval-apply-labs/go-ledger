# H1-crash-restart-self-equivocation (new hypothesis from the lead reviewer — NOT yet tested)

**Claim.** An honest validator that crashes or reboots while its height is still open can sign a vote that conflicts with one it signed before the crash, producing valid equivocation Evidence against itself, so its own account gets slashed.

**Mechanism as read in the code (verify every step):**
- No last-signed consensus state is persisted anywhere: grep finds no WAL / sign-state / high-water mark. TODO.md ("Consensus-state WAL") says a crash mid-height loses in-progress votes and the node restarts the height.
- On start the node loads the committed snapshot (height H-1) or syncs via bootstrap, then `startHeight` (node/node.go ~466-486) builds a fresh `consensus.Driver` at round 0 with no memory of earlier votes, and a fresh candidate block from its (now empty / different) mempool.
- The driver prevotes nil when the propose timeout fires (consensus/driver.go ~239), or prevotes its own candidate if it is the round-0 proposer.
- Peers keep every round's vote sets for the current height until it commits. If before the crash the node prevoted X at (H,0) and after the restart it prevotes nil or Y at (H,0), a peer's `VoteSet.Add` records Evidence, the evidence goes into pendingSlashes at that peer's commit, and a later block carrying it removes the honest node's account (slash).

**When it is likely:** exactly when the node's absence stalls the height — e.g. a high-power validator in the "concentrate stake on the machines you can keep awake" setup docs/security.md recommends, or any set where the remaining validators lack quorum without it — because then height H stays open until the node returns. Laptops "sleeping, rebooting, dropping off" are an explicit part of the threat model.

**What to establish:**
1. Whether the restarted node actually emits a (H,0) vote that differs from its pre-crash (H,0) vote in realistic sequences, including when peers have moved to round r>0 (does it still produce round-0 votes before f+1-skipping? does the propose timeout fire for round 0? does reGossipProposal re-send only the current round's proposal?).
2. Whether peers still hold the first vote (same height's driver still alive) and record Evidence, and whether the slash lands in a block.
3. Also the proposer case: a validator that proposed block B at (H,0), crashes, restarts and proposes a different candidate at (H,0) then prevotes it.
4. With the owner's planned stricter burn rule (tombstone the key on evidence), this would permanently burn an honest validator after an ordinary reboot.

**Expected minimal fix to evaluate:** persist the last signed (height, round, step/type, digest) to disk (fsync) BEFORE releasing any vote/proposal, and on restart refuse to sign anything for a slot at or below the persisted high-water mark with a different value (Tendermint's priv_validator_state / tmkms "last sign state"). This is also the state the owner's planned remote-signer container would keep.

Build a node-level PoC in your own scratch copy (see node/*_test.go helpers, e.g. node/node_test.go, node/chaos_test.go, node/sync_test.go for multi-node setups, snapshot paths, stopping/restarting nodes).
