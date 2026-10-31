# C19-long-range-and-rotation-brick

Round-1 reports merged into this cluster (3):

## [sync-trust] SYNC-1 — Long-range checkpoint forgery: joiners trust genesis validator keys forever (no weak subjectivity)
- severity/confidence (round 1): high/high
- category: key-management
- locations: node/node.go:388-401, node/node.go:374-379, node/sync.go:98, node/persist.go:31, node/sync.go:15-18
- attacker: holder of >1/3 (by genesis power) of the former/weak genesis validators' stolen or cracked keys
- known_status: new — findings.md calls onboarding 'sound ... with the full-rotation limit documented, not glossed'; no prior round considered compromise of FORMER/weak anchor keys (the eMudhra lens). Not deferred #7 (ledger height) or #10 (open API).

**Description.** A fresh or restarting node anchors checkpoint trust on the GENESIS validator set forever: bestCheckpoint passes cfg.Genesis (sync.go:98), adopt/loadSnapshot pass pre-start n.accounts (=genesis). verifyCheckpoint accepts any checkpoint whose Commit is a >2/3 quorum of the PEER-SUPPLIED signing set (cp.Validators, dynamic mode) and whose signers cover >1/3 of the genesis anchor's power. cp.Validators is never cross-checked against what cp.Accounts would derive. So an attacker holding >1/3 (by genesis power) of the GENESIS validators' private keys can fabricate arbitrary accounts, advertise those same keys as the signing set, self-sign the commit, and the joiner adopts attacker-controlled state. Those genesis keys may have left the active set (zero current stake, not slashable, ignored by live consensus) or been weakly generated (eMudhra). There is no weak-subjectivity checkpoint or pinning to retire the anchor.

**Failure scenario.** Dynamic-validator village. Founders A,B,C leave over a year; their old laptops/keys are discarded or were weakly generated. Attacker recovers A,B's keys (2 of 3 = >1/3). It builds a checkpoint at height 100: accounts crediting itself 9e9 (founders at 0), Validators=[A,B], a 2-of-2 self-signed commit over that digest. A new merchant node (or a founder's rebuilt node) boots with the original genesis config, polls a peer/attacker, verifyCheckpoint returns true, fastForward adopts it: the node now believes the attacker's balance and height. 'Two signings burns the key' never triggers — the forged commit is shown only to the victim, so the live network sees no equivocation and the keys hold no stake to slash.

**PoC.** <scratch>/sync-trust/node/synctrust_poc_test.go — TestPoCLongRangeCheckpointForgeryAcceptedByJoiner and TestPoCLongRangeNeedsOnlyOverOneThirdOfGenesisKeys: both PASS, printing that a genesis-anchored joiner accepts and adopts (height 101, attacker balance 9e9) a checkpoint signed only by former/abandoned keys, with 2-of-3 keys sufficing; the same checkpoint is rejected against a live-set anchor.

**Suggested fix.** Add a weak-subjectivity pin: let operators configure a recent trusted (height, stateDigest) and its validator-set hash, refreshed out-of-band, and anchor verifyCheckpoint to that instead of genesis for joiners — so abandoned genesis keys cannot authorise a checkpoint past the pin. Defense-in-depth: require cp.Validators to equal deriveValidators(cp.Accounts) in dynamic mode so a peer cannot advertise an arbitrary signing set. Document that the anchor keys are a root of trust that must stay secure (and be re-pinned when founders leave).

## [sync-trust] SYNC-3 — Full validator rotation past genesis permanently bricks onboarding and crash-recovery
- severity/confidence (round 1): medium/high
- category: liveness
- locations: node/node.go:395-400, node/sync.go:87-106, node/persist.go:28-34, docs/consensus.md:126-128
- attacker: no attacker — triggered by ordinary validator rotation over the network's lifetime
- known_status: sharper angle on the 'full-rotation limit' findings.md calls 'documented, not glossed': shown here to be a hard onboarding/recovery brick with no implemented bisection or pin, and to also block reloading a node's OWN snapshot — materially worse than a mere note.

**Description.** The flip side of SYNC-1. Because the joiner/restart anchor is always the genesis set, verifyCheckpoint's >1/3-overlap test (vouched*3 > trusted.TotalPower) fails once the live validator set has rotated fully past the genesis validators — even for a perfectly honest, >2/3-signed, digest-bound current checkpoint. bestCheckpoint fetches exactly ONE checkpoint per peer; there is no bisection/chain-walk and no weak-subjectivity pin, so the 'chain of intermediate checkpoints to walk' that consensus.md:126-128 says would be needed does not exist. A node also re-verifies its OWN on-disk snapshot against genesis (persist.go:31), so after rotation it cannot even reload its own state and falls back to genesis (height 1), then cannot catch up either. Onboarding and recovery silently break for good.

**Failure scenario.** A long-lived village currency: over time the 8 founding laptops are all replaced and the founders' accounts drop below the validator threshold, so none of the genesis validators remain in the live set. Everything runs fine for existing up-to-date nodes (their catch-up anchor tracks the live set). Then someone adds a new node, or a running node crashes and restarts: it boots on the original genesis config, fetches the current honest checkpoint, verifyCheckpoint rejects it (zero genesis overlap), and the node sits stuck at genesis forever. No error explains why; there is no documented knob to move the anchor forward.

**PoC.** <scratch>/sync-trust/node/synctrust_poc_test.go — TestPoCFullRotationBricksOnboarding: PASS. A genuine >2/3-signed current checkpoint signed by a fully-rotated set (X,Y,Z; founder A below threshold) is rejected by a genesis-anchored joiner.

**Suggested fix.** Implement the weak-subjectivity pin from SYNC-1 (also the fix here): a periodically-refreshed trusted recent checkpoint lets joiners/restarts anchor to the live-ish set. Alternatively implement the documented checkpoint bisection (walk intermediate checkpoints so each step overlaps the previous anchor). Persist the last-trusted signing-set hash alongside the snapshot so a node can at least reload its own state. Until then, document loudly that replacing all genesis validators disables onboarding/recovery.

## [sync-trust] SYNC-4 — Docs claim onboarding is 'safe even from an untrusted bootstrap peer' without the weak-subjectivity caveat
- severity/confidence (round 1): low/high
- category: docs-drift
- locations: docs/consensus.md:108-110, docs/consensus.md:124-125, docs/operating-a-network.md:116-117, docs/security.md:106-114
- attacker: no attacker — documentation gap
- known_status: new docs-drift, the documentation face of SYNC-1; findings.md's doc spot-checks did not cover the onboarding trust-assumption wording.

**Description.** consensus.md states a joiner is 'safe even from an untrusted bootstrap peer' and that 'an honest anchored validator only signs valid commits, and a fabricated all-stranger set has no anchor overlap and is rejected'; operating-a-network.md says 'the bootstrap peer does not have to be trusted.' These are true only under an unstated assumption the code bakes in: that the ANCHOR (genesis) validators' keys remain honest and uncompromised forever, including after those validators leave the active set. SYNC-1 shows that assumption is the whole vulnerability — abandoned or weakly-generated genesis keys (the eMudhra case) still authorise any checkpoint for a joiner. The docs present certificate verification as the complete trust story and omit that the trust ultimately reduces to those specific keys staying secure for the network's whole life.

**Failure scenario.** An operator reads 'the bootstrap peer does not have to be trusted' and 'safe even from an untrusted bootstrap peer', concludes onboarding needs no special key hygiene, and treats retired founder keys casually (deletes backups carelessly, reuses hardware, never re-pins). An attacker later recovers or finds a set of those retired keys and forges checkpoints per SYNC-1. The operator had no warning from the docs that the retired genesis keys were still a live root of trust.

**PoC.** none — documentation claim; the underlying code behavior is proven by the SYNC-1 PoC in <scratch>/sync-trust/node/synctrust_poc_test.go.

**Suggested fix.** State plainly that onboarding trust rests on the anchor (genesis, or a configured weak-subjectivity pin) validator keys staying uncompromised for the network's lifetime; that retired/founder keys remain a root of trust for joiners until a pin moves the anchor forward; and that losing/leaking >1/3 of them lets an attacker forge checkpoints for new or rebooting nodes. Tie the guidance to the planned pin/bisection fix.
