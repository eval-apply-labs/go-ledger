# Consensus

Area: **node**. Code: `consensus/` (the validator set, votes, commits, proposals, evidence, the vote
set and the round driver), and the node's use of them in `node/node.go`, `node/proofs.go` and
`node/sync.go`. This doc explains the commit rule, why it is safe, the timers and the slow-down
switch, and what a double-sign does.

## The problem it avoids

The audit found that finalising once more than ½ of all stake agrees, in one node's local view, lets
a coalition under ½ fork the chain under a partition: a validator with 40% sends X to one side and Y
to the other, and each side clears ½. And the value voted on was not the value applied.

## The rule

Finalisation needs precommits for one block hash from validators holding a strict > ⅔ of the power:

```
Quorum = floor(2 · totalPower / 3) + 1
```

1. **Quorum intersection: no fork while at most ⅓ of the power is Byzantine.** Two sets that each
   hold more than ⅔ overlap in more than ⅓, so the overlap holds an honest validator, and in one
   round an honest validator never precommits two blocks
   (`TestConflictingCommitsRequireEquivocation`). Across rounds the lock carries it: once a block
   has > ⅔ precommits in a round, the honest validators among them, more than ⅓ of the power, are
   locked on it, so no other block gets > ⅔ prevotes, or an honest precommit, in a later round.
2. **The block hash covers the applied state.** A commit is over the block hash, which covers the
   height, the previous block hash, the signing set's hash, the content, the proposer's nonce and the
   digest of the resulting state (see [architecture.md](architecture.md#the-block-and-its-hashes)).
   Every node re-runs the block with `ApplyBlock` and takes it only if it gets that digest.

Voting power saturates at 2^40 per validator (`consensus.MaxValidatorPower`).

## What is signed

- A **vote** (`consensus.Vote`): height, round, type (prevote or precommit), block hash, and the
  validator's **name**. A vote carries no key: it verifies under the key the validator set holds for
  that name, the name's current key in the state. A nil vote has an all-zero block hash.
- A **signed proposal** (`consensus.SignedProposal`): height, round, valid round, block hash and the
  proposer's name. Since the block hash covers the whole block, the proposer signs the block (E15).
  The node sends it with its block as a `node.ProposalMessage`.
- A **commit** (`consensus.Commit`): the precommits of one height and round for one block hash,
  from distinct names of the set, whose power reaches the quorum (`Commit.Verify`).

## The round driver

`consensus.Driver` is a Tendermint-style state machine for one height, with two phases and a lock:

- **Propose.** The round's proposer (`ValidatorSet.ProposerAt`, weighted by power) proposes its
  valid value, the block it last saw more than ⅔ prevote for, if it has one; otherwise its
  candidate. A validator can hold a valid value without a lock.
- **Prevote.** A validator prevotes the proposal only if it is valid and the validator holds no
  lock, is locked on that block, or sees a proof-of-lock from a round at or above its own locked
  round. Otherwise it prevotes nil. On > ⅔ prevotes for the round's proposal it locks it and
  precommits it.
- **Precommit.** On > ⅔ precommits in any round for that round's proposal, it decides. It holds
  only the first proposal of each round, so a node that took an equivocating proposer's other block
  cannot decide that round, even once the decided block reaches it; it recovers by checkpoint
  catch-up. On > ⅔ prevotes for nil it precommits nil. When a step times out without a decision, it
  moves on, and in the end to the next round.
- **Timeouts** grow with the round: `stepTimeoutMs` × (round + 1). They reach the driver as
  `Timeout` events, never from a clock, so the driver is deterministic and tested directly. The
  propose timeout starts with each round, but the prevote and precommit timeouts start only once
  that step's votes from more than ⅔ of the power are in, whatever they are for. So a partition
  that leaves no side above ⅔ stalls in its round instead of running through rounds.

The lock keeps a validator from prevoting another block in a later round, unless it holds > ⅔
prevotes for that block from a round at or above its locked round
(`TestDriverLockingPreventsConflictingPrevote`; the release is `TestDriverPOLUnlockReleasesLock`).
An honest validator can precommit two blocks at one height, in two rounds: that is no double-sign.
On > ⅓ of the power from a later round, the driver skips to that round, which helps liveness and
does not touch the commit rule. The lock must survive a restart too; the sign log does that (see
[signature.md](signature.md#the-sign-log)).

## The timers, and the slow-down switch

The node arms the driver's timers (`Node.armTimeout`). Each waits a base step timeout times the
round plus one.

- **The propose timer** starts with each round. A node waits for the round's proposal, 3 s at
  round 0 by default, then prevotes nil: it votes "no block".
- **The prevote and precommit timers** start only once that step's votes from more than ⅔ of the
  power are in. So when no side of a split holds more than ⅔, every node stops at the step where
  it needs the other side's votes, with no timer running. Votes carried across by hand then let the
  step complete.
- **The catch.** The propose timer always runs. A proposal that travels slower than it, carried by
  hand, comes too late, and rounds pass with no block.

The base is the governed `stepTimeoutMs`, or the node's **slow-down switch**,
`minimumStepTimeoutMs` in its config, when that is longer (`Node.stepTimeout`):
max(`stepTimeoutMs`, `minimumStepTimeoutMs`) × (round + 1). The switch is read at start, from 0
(off) to a day. It changes the round timers, and one wait: a node back without its sign log waits
2 × max(`stepTimeoutMs`, `minimumStepTimeoutMs`) at each height it may have signed at
(`Node.ownSignaturesWait`), since in pigeon mode its own earlier votes come back only with the next
carry. A restart that keeps its sign log never waits. Re-gossip, catch-up, the idle heartbeat and
the pull of public bundles keep the governed timings.

- **Why it is safe.** No rule reads the clock: `ledger` and `consensus` do not import `time`
  (`TestNoRuleReadsTheClock`), a block carries no timestamp, and every deadline counts in blocks. A
  longer timer only makes a node wait longer before it votes "no block": it cannot make it sign
  twice or take an invalid block. Each node may set its own value, and mixed values are safe.
- **When it helps.** Once banks holding more than ⅔ of the power have it on, a round waits for a
  carrier: blocks commit at the carrier's pace, in about three carries a round (the proposal, the
  prevotes, the precommits). One bank alone gains nothing: the others still vote "no block" after
  their own timer. `TestPigeonCourierCarriesTheChain` runs four validators with no network link and
  a courier between them: with the switch on, most heights commit in round 0; with it off, the same
  carries commit fewer heights.

How the banks use it, with bundles carried by hand and the public bundles as a second path, is in
[operating-a-network.md](operating-a-network.md#when-the-network-splits).

## Intake of proposals and votes

A node checks a proposal (`Node.onProposal`) in this order, cheap steps first:

1. it is for the height in progress, and the node has a set for it;
2. no proposal is held for its round yet (a second one from the proposer is a double-sign, below);
3. its round is at most one past the driver's, and fewer than two authentic proposals of that
   round failed to apply;
4. it names the round's scheduled proposer;
5. its signature verifies under the set's key for that name;
6. its block hashes to the signed block hash, at the right height;
7. `ApplyBlock` accepts the block.

A copy that fails step 5 or 6 is not authentic: it is dropped, and a genuine copy that comes later
is still taken.

A node drops a vote that is byte for byte a vote it holds, before any check: signing is
deterministic, so every relayed copy of an honest vote is the same bytes. It keeps a new vote only
if the height in progress counts it: the validator is in the set, the type is known, the round is
not too far ahead, and it verifies. It keeps at most two votes per validator, round and type. While
a driver runs, the node re-sends the height's votes and the current round's proposal every
`stepTimeoutMs` in force (a ⅔ change reaches it within a second; the slow-down switch never changes
it), so a merged partition re-exchanges what each side held. Votes and proposals that come in a
bundle, carried by hand or pulled from a public face, go through this same intake. A pulled public
bundle's votes and proposals are checked first, off the loop. Each must be of the height in
progress, or of the last committed height, and that height's set must vouch for it (for the last
height, the set that signed the last commit): a vote is by one of its validators, a proposal by the
proposer it schedules for the round, and the signature verifies. The pull stops at the first line
that fails, is unreadable or is refused (see [architecture.md](architecture.md#bundles)).

## Signing only while seated

A node signs votes and proposals only at heights where its name is seated with exactly its
configured key (`Node.seatedSigner`). Otherwise its driver has no signer: it follows the rounds and
decides, but signs no vote or proposal. If its name is seated under another key, as after a ⅔
re-key, the node logs that once, when it next starts a height. An observer, a node whose name is
not seated, signs no vote or proposal. Every node still signs a receipt, with its configured key,
for each payment a wallet posts to it (`/hdls/pay`) that it newly pools.

## Equivocation freezes the name

A double-sign is two votes of one name for the same height, round and type with different block
hashes, or two signed proposals of one name for the same height and round with different block
hashes. Two blocks that differ only in their nonce are two blocks. A different valid round alone is
none.

- **In the round.** Once a vote set holds both votes, the double-signer's power leaves the tally.
  Until then its first vote counts, so a commit may include it. A node that decides before it has
  precommitted precommits the decided block first, so its commit carries its own vote.
- **As evidence.** A node keeps a double-sign as a pending proof (`node.pendingProof`), by name,
  with the key it convicts. It finds them in three places: the vote sets of a height it leaves
  (`Node.retireDriver`); a late precommit that conflicts with the last commit
  (`Node.recordLateDoubleSign`); and a second authentic proposal for a held round
  (`Node.reportEquivocation`). A proof reaches the snapshot file at the node's next commit or
  catch-up, and from then on a restart does not lose it. Until then it lives only in memory, as
  does a double-sign in the vote sets of the height in progress. A proof is dropped only when its
  name is frozen or no longer has the convicted key.
- **In a block.** The node offers its pending proofs to `ProposeBlock` at every height.
  `ApplyBlock` takes `VoteEvidence` and `ProposalEvidence` when both signatures verify under the
  name's current key in the pre-block state, the name exists and is not frozen, and the double-sign
  is below the block's height. There is no other age limit. A block carries one item per name, and
  an honest proposer at most 8.
- **The effect.** The block freezes the name. It leaves the validator set, with the power delegated
  to it, and keeps its balance: there is no slash. The ⅔ decide about the money. Only the ⅔ name
  action unfreezes the name, with a new key, and after that the old evidence no longer verifies.
- **The one-third cap.** A block carries evidence only while the frozen names would hold under a
  third of the power. With N the names frozen before the block plus those it freezes, and each
  name's power counted as if no account were frozen: 3 × power(N) < power(seated names and N).
  Evidence over the cap waits. Freezes by a holder or by the ⅔ have no cap.

Known behaviour:

- Evidence the cap holds back stays pending in the snapshot of each node that holds it, until a
  block can carry it, the name is frozen, or the name's key changes: by the ⅔ name action, or by a
  key change the name files once it is no longer seated. No node sends a proof on its own, outside
  a block: only the nodes that saw both signatures hold it.
- A late double-sign wakes an idle chain. But a proof the cap holds back does not keep the chain
  moving, and a proof kept while the chain idles lives only in memory until the next commit writes
  the snapshot, at most an idle heartbeat (five minutes) later. If every node restarts before then,
  that proof is lost. It needs frozen names already near a third of the power, the case that calls
  for a new genesis.

The freeze handles double-signing by under a third of the power. Its limits are in
[security.md](security.md#the-freeze-and-its-limits).

## Catching up

- **The last commit.** As a node commits, it sends the commit's precommits and the decided proposal
  to its peers, and again on every re-gossip while it runs a driver. An idle node re-gossips
  nothing, so a node one precommit short of a height the others committed gets the last commit once,
  at commit time, and otherwise by checkpoint catch-up.
- **The last commit in a bundle.** A bundle carries the last commit's precommits and its decided
  proposal first, then the current round's votes and proposal, then the height's other votes: the
  past rounds', newest first, then the future rounds', nearest first. So a node one height behind
  that imports it decides the last height first, and then takes the next height's messages.
- **Checkpoint catch-up.** Every 3 s, or every 5 block intervals if that is longer, a
  running node asks its peers' `/hdls/status` and fetches a checkpoint only from a peer that is
  ahead (`Node.catchUpLoop`, `Node.catchUpCheckpoint`, `Node.bestCheckpoint`). It asks each peer at
  the first of its URLs that answers. Bootstrap peers come first; gossip peers are asked only when
  no bootstrap peer gives a checkpoint that verifies; the other validators' public addresses come
  last, when neither does. It asks every one of those if no bootstrap or gossip peer answered at
  all, or while it is stuck on its height, and otherwise one of them a tick, each in turn. So a node
  that gossip can't reach, or that the others' allow lists no longer let in, still catches up; an
  idle node, which runs no round and so is never stuck, finds banks ahead of its peers within about
  as many ticks as there are validators; and on a healthy network each tick costs one small request.
  A connection gives up after 2 s. A bootstrap peer can be a bank's public address too, which
  serves the same files.
- **A carried checkpoint.** `POST /hdls/checkpoint` on the operator port takes a checkpoint someone
  carried in, and the node checks it as catch-up does (below), with the same event
  (`Node.ImportCheckpoint`).

## Onboarding and the checkpoint

A `node.Checkpoint` is what a joining or restarting node adopts: `State` (the committed state),
`LastBlock` (the header of the block that produced it: height, previous block hash, validators hash,
nonce, content hash), `Commit`, and `Validators` (the set that signed the commit: name, key, power).
The set is not signed, but the block hash covers its hash. A node checks a peer's checkpoint
(`Node.verifyCheckpoint`) in this order, each step only if the one before passed:

1. **Shape:** state, header and commit are of one height; the state's names are strictly ascending;
   the set is sorted by name, distinct, each with power.
2. **Hashes:** the set hashes to the header's validators hash, and the block hash recomputed from
   the header and the state's digest is the commit's block hash.
3. **Anchor:** names of the node's anchor holding more than ⅓ of its power signed the commit, each
   under the key the anchor holds for it. One check per anchor name, so padding costs nothing.
4. **Commit:** the commit is a valid > ⅔ quorum of the carried set.

The anchor is the set (name, key, power) of the node's last committed state. So a checkpoint that
gives real bank names keys an attacker holds fails, and a set padded with made-up validators fails
step 2 before any signature is checked. A node trusts its own snapshot when it passes steps 1, 2 and
4, and refuses to start, rather than fall back to genesis, if it does not.

A joiner with no snapshot anchors on the genesis set. On a network whose banks have re-keyed or
left, or in dynamic mode, start a joiner from a snapshot of a node you trust (see
[operating-a-network.md](operating-a-network.md#rejoining-after-a-restart)). The same holds for a
node so far behind that the names of its anchor that still sign, each under the key the anchor
holds for it, hold a third or less of the anchor's power: step 3 then fails.

## Mapping to the audit

| Audit finding | Fix here |
|---|---|
| A1, sub-½ fork under partition | > ⅔ quorum with intersection |
| A2, vote ≠ applied state | the commit signs the block hash, which covers the resulting state; `ApplyBlock` re-checks it |
| A4, no BFT margin | ⅔ quorum, round timeouts and locking |
| C5, MD5 content hash | SHA-256 everywhere |
| equivocation, view-local and non-durable | transferable evidence that freezes the name |
