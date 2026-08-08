# Consensus

`go_ledger` finalises blocks with a Tendermint-style Byzantine-fault-tolerant protocol. This
document explains the rule and why it is safe.

## The problem it avoids

A naive rule finalises a block when signatures representing **more than ½ of all stake** agree, from
a single snapshot of one node's local gossip view — no two-phase commit, no quorum-intersection
guarantee, no durable equivocation accountability. The audit showed the consequences of that
approach:

- **A sub-½ Byzantine coalition can fork the chain under a partition.** If the network splits and a
  validator with, say, 40% stake sends result X to one side and Y to the other, each side sees its
  local honest stake + 40% clear ½, and the two sides finalise conflicting blocks. Detection came
  only *after* both sides collected both messages — too late; the fork was already final.
- **The value voted on was not the value applied.** The tally ran over a string derived from one
  transport message while the state was rebuilt from another, so two nodes could "agree" and still
  compute different ledgers.

## The rule

Finalisation requires a **strict > ⅔ stake quorum** of *precommit* votes for one block digest:

```
Quorum = floor(2 · totalPower / 3) + 1
```

Two properties follow, and they are the whole point:

1. **Quorum intersection ⇒ no forks under ≤ ⅓ Byzantine.** Any two sets that each hold more than
   ⅔ of the power must overlap in more than ⅓ of it. If at most ⅓ of power is Byzantine, that
   overlap contains at least one honest validator — who will not precommit two conflicting digests.
   So two conflicting blocks can never both reach quorum. The single-½ rule has no such guarantee:
   two disjoint halves can each clear ½.

   This is asserted directly in code: `TestConflictingCommitsRequireEquivocation` builds two
   commits for different digests and shows they must share a validator — i.e. a double-signer.

2. **The digest *is* the applied state.** A `Commit` binds `ledger.StateDigest` (SHA-256 of the
   sorted, length-prefixed accounts). Every node recomputes that digest with `ApplyBlock` and
   accepts a block only if it matches, so "what was agreed" and "what was applied" cannot diverge.

## The round driver

A single ⅔ snapshot is safe but not live: after a failed round nodes need to try again without
ever contradicting an earlier commit. The `Driver` is the state machine that provides this, over
two phases with a **lock**:

- **Propose.** The round's proposer (stake-weighted over the validator set — see
  `ValidatorSet.ProposerAt`) broadcasts a block — its locked value if it holds one, otherwise a fresh
  block from its mempool. The proposal is signed by the proposer, and peers accept it only from the
  round's scheduled proposer, so it cannot be front-run.
- **Prevote.** A validator prevotes the proposed value only if the value is valid **and** it holds
  no lock, is locked on that value, or there is a proof-of-lock from a round ≥ its own locked round.
  Otherwise it prevotes nil. On seeing ⅔ prevotes for a value it **locks** it and precommits it.
- **Precommit.** On ⅔ precommits for a real digest it **decides**. On ⅔ prevotes/precommits for nil,
  or on a timeout, it moves on.
- **Timeouts** (`propose → prevote-nil`, `prevote → precommit-nil`, `precommit → next round`) grow
  with the round number, so under an unreliable network the rounds eventually stretch past message
  latency and progress resumes (eventual synchrony). They are delivered to the driver as `Timeout`
  events, never read from a real clock — which is what makes the driver deterministic and testable.

The lock is what preserves safety *across* rounds: a node that locked X will not prevote a
conflicting Y without a proof-of-lock, so it can never precommit two conflicting values. This is
tested directly (`TestDriverLockingPreventsConflictingPrevote`).

## Equivocation

A validator that signs two different votes for the same (height, round, type) is recorded as
`Evidence` — a pair of conflicting signed votes. Because each vote is self-authenticating, this
evidence is **transferable and durable**, and it is acted on at two levels:

- **In-round:** inside a vote set the equivocator's power is removed from the tally entirely, so a
  double-signer cannot help form a quorum.
- **Durably (slashing):** a node carries the proof into a block (`Block.Evidence`); `ApplyBlock`
  re-verifies it (`Evidence.Valid` — two validly-signed conflicting votes by the same validator,
  which only that validator could produce, so it is unforgeable and can't frame the honest) and
  **removes the offender's account**, confiscating their stake and ejecting them from a dynamic set.
  It zeroes out the double-signer with portable, transferable proof rather than a view-local check —
  any node applies the same slash deterministically.

## What is deferred (and why it's safe to defer)

- ~~The "f+1 messages from a higher round → skip to it" catch-up for stragglers.~~ **Implemented**
  (`Driver.step1`): on seeing > ⅓ of power from a round ahead, the driver skips forward to it. A
  liveness aid — it does not affect safety, since the commit rule is unchanged.
- ~~Straggler catch-up and dynamic membership.~~ **Implemented.** A running node that missed a
  commit window periodically polls its bootstrap peers' checkpoints and fast-forwards (`catchUpLoop`
  → `catchUp`) instead of staying stuck. Dynamic membership is the `validatorThreshold` stake-derived
  set, with the skipping-trust onboarding above.
- ~~Stake-weighted proposer order.~~ **Implemented.** Proposer selection is stake-weighted
  (`ValidatorSet.ProposerAt`), so a validator proposes proportionally to its power — a fairness/liveness
  property, not a safety one.

None of these changes the commit rule, so none can introduce a fork.

## Onboarding / rejoin (implemented)

A node configured with `bootstrap` peer URLs syncs on start instead of replaying history:

1. It fetches each bootstrap peer's `GET /hdls/checkpoint` — that peer's current accounts plus the
   `Commit` certificate that finalised them.
2. It **verifies the certificate** against its validator set (a > ⅔ quorum of precommits for the
   digest) and checks that the certificate binds the digest of the supplied accounts. An
   unverifiable or mismatched checkpoint is rejected.
3. It adopts the highest verified checkpoint and resumes consensus at that height.

So a joiner trusts the quorum's signatures, not the peer that served the snapshot — safe even from
an untrusted bootstrap peer, because the snapshot is trusted for its quorum certificate, not for who
served it. This is what lets a rebooted node rejoin at the current height.

### Dynamic validators — the "skipping" trust rule

With **dynamic validators** (`validatorThreshold`) the set that signed a checkpoint's commit may
differ from what its accounts yield now, so the checkpoint carries that **signing set** and the
joiner adopts it under the standard BFT *skipping* rule (`Node.verifyCheckpoint`):

1. the `Commit` is a valid > ⅔ quorum of the carried signing set, and binds the accounts' digest;
2. **and** more than ⅓ of the power of a set the joiner already trusts (its *anchor* — genesis, or
   its last committed state) is among the commit's signers.

Condition (2) means at least one validator the joiner trusts co-signed the commit, so a growing set
is adopted safely without trusting the serving peer — an honest anchored validator only signs valid
commits, and a fabricated all-stranger set has no anchor overlap and is rejected. The static path is
a trivial special case (anchor = signing set, so (2) always holds). The one bound: *full* validator
rotation past the anchor (no overlap left) would need a chain of intermediate checkpoints to walk;
for the incremental growth this system actually sees, the anchor overlaps the signing set.

## Mapping to the audit

| Audit finding | Fix here |
|---|---|
| A1 — sub-½ fork under partition | > ⅔ quorum with intersection |
| A2 — vote ≠ applied state | commit binds `StateDigest`; `ApplyBlock` re-checks |
| A4 — no BFT margin, hard liveness cliff | ⅔ quorum + round timeouts + locking |
| C5 — MD5 content hash | SHA-256 `StateDigest` |
| (equivocation) — view-local, non-durable | transferable `Evidence` |
