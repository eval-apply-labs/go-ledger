# Consensus

Area: **node**. Code: `consensus/` (the validator set, votes, commits, proposals, evidence, the vote
set and the round driver), and the node's use of them in `node/node.go`, `node/proofs.go`,
`node/sync.go`, `node/frames.go` and `node/alarms.go`. This doc explains the commit rule, why it is
safe, the timers and the slow-down switch, what a double-sign does, and how a node catches up and
joins without trusting any one peer.

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
   height, the previous block hash, the signing set's hash, the hash of the set that signs the next
   block, the content, the proposer's nonce and the digest of the resulting state (see
   [architecture.md](architecture.md#the-block-and-its-hashes)). Every node re-runs the block with
   `ApplyBlock` and takes it only if it gets that digest and that next set.

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

The key is in the engine's own process, or in the bank's signer, `cmd/signer`, a program on a
machine of its own (see [signature.md](signature.md)). The engine signs the same way through either
(`signing.Signer`). Through the signer, a request gives up after a second. A vote the engine cannot
sign is dropped, as a vote whose sign log write fails is, and cast again at the next event. So a
bank whose signer is down is a validator that does not vote: the chain goes on while the others
hold more than ⅔.

**Two sign logs.** A bank with a signer keeps two, and each guards something the other cannot:

- **The engine's sign log** (`<snapshotPath>.votes`) holds its own votes and proposals of the height
  in progress, and its lock, so that a restart resumes the round where it stopped (see
  [signature.md](signature.md#the-sign-log)). It guards the lock.
- **The signer's sign log** holds every slot it signed, a vote's height, round and type or a
  proposal's height and round, and it refuses a second, different signature in a slot, whoever asks.
  It guards the key: an engine that is broken, or that someone else drives, cannot make the key sign
  two conflicting votes and get the bank's name frozen for a double-sign. A guard on the slot alone
  does not stop a fork: one still forked in testing, because a node can vote unlocked in a later
  round (review E2).

The signer knows nothing of the lock, and the engine's log cannot stop someone else driving the key,
so neither replaces the other. Each can be lost without the other.

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
- **Checkpoint catch-up.** Every 3 s, or every 5 block intervals if that is longer, a running node
  asks for the status of every node it knows of, all at once (`Node.catchUpLoop`,
  `Node.catchUpCheckpoint`, `Node.bestCheckpoint`): its bootstrap peers, its gossip peers, and every
  other validator of its trusted frame (below) at its public address. A status gives the height in
  progress and the hash of the last block. The node fetches a checkpoint, the highest first, only
  from a peer at its own height or ahead of it that reports a block it does not know: another block
  than its own at its own height, or, above it, another block than that of a checkpoint that already
  passed. So on an idle chain, where every peer is at the node's height on its block, it fetches
  nothing, and the honest peers at the network's height cost one fetch. It keeps a checkpoint only
  if it passes (below): one of its own height, checked against the set that signed it; one ahead of
  it, against the trusted frame, directly or by the walk. It adopts the highest one ahead of it. So
  a peer that withholds the newest checkpoint is outvoted by the others: one honest validator whose
  public face serves it is enough. A peer that lies about its height costs one fetch, and the next
  one is asked.
- **Whom it reaches, and how.** It asks each peer at the first of its URLs that answers, and at no
  other. A connection gives up after 2 s, and a request after 5 s. The other validators' public
  faces serve the status and the checkpoint as files, behind a CDN, so a node that gossip can't
  reach, or that the others' allow lists no longer let in, still catches up; a bootstrap peer can be
  a bank's public address too. Every one is asked at every tick: a status is a small file. A node
  with bootstrap peers asks the same peers once before its first height (`Node.syncThenStart`);
  without them it starts at once, and the loop catches it up. A halted node (below) asks nothing.
- **A carried checkpoint.** `POST /hdls/checkpoint` on the operator port takes a checkpoint someone
  carried in, and the node checks it as catch-up does (below), with the same event
  (`Node.ImportCheckpoint`), but walks no frames: a carrier brings no record, so more than ⅔ of the
  node's own set must have signed it.

## Onboarding and the checkpoint

A `node.Checkpoint` is what a joining or restarting node adopts: `State` (the committed state),
`LastBlock` (the header of the block that produced it: height, previous block hash, validators hash,
next validators hash, nonce, content hash), `Commit`, and `Validators` (the set that signed the
commit: name, key, power). The set is not signed, but the block hash covers its hash. A node checks
a peer's checkpoint (`Node.verifyCheckpoint`) in this order, each step only if the one before
passed:

1. **Shape:** state, header and commit are of one height; the state's names are strictly ascending;
   the set is sorted by name, distinct, each with power.
2. **Hashes:** the set hashes to the header's validators hash, and the block hash recomputed from
   the header and the state's digest is the commit's block hash.
3. **The trusted frame:** names of the node's trusted frame holding more than ⅔ of its power signed
   the commit, each under the key the frame holds for it (`Node.anchorVouches`). One check per name,
   so padding costs nothing.
4. **Commit:** the commit is a valid > ⅔ quorum of the carried set.

The **trusted frame** is the set (name, key, power) that the node's last committed state seats, or
a later one the walk reached from it (below). So a checkpoint that gives real bank names keys an
attacker holds fails, and a set padded with made-up validators fails step 2 before any signature is
checked. Forging a state takes the keys of more than ⅔ of the power of a set the node already
trusts. Before Phase 4, step 3 took more than ⅓, so the keys of a third of an old set, stolen or
sold, were enough. A node trusts its own snapshot when it passes steps 1, 2 and 4, and refuses to
start, rather than fall back to genesis, if it does not.

A joiner with no snapshot trusts the genesis set. A joiner its bank seeds with a snapshot trusts
that snapshot's set (see
[operating-a-network.md](operating-a-network.md#rejoining-after-a-restart)). When the names of the
trusted frame that signed a newer checkpoint, under the keys it holds for them, hold ⅔ of its power
or less, as after the set has changed while the node was away, step 3 fails, and the node walks.

## Frames and the walk

- **A frame** is a validator set and the height from which it signs: the set the chain has between
  two changes of its names or keys. Its powers may change in between, and in dynamic mode a payment
  that moves a validator's stake changes them at almost every busy block. A frame is the set as it
  was at its first height.
- **Each block names the next set.** The block header carries `NextValidatorsHash`, the hash of the
  set that the resulting state seats: the set that signs the next height. `ProposeBlock` sets it,
  `ApplyBlock` checks it, and the block hash covers it. The record entry carries it too, so the
  block hash still recomputes from the record. So the commit of a frame's last height is more than
  ⅔ of that frame, under its keys, signing off the next one. It came with Phase 4's new genesis.
- **The frames file.** The engine writes `hdls/frames` into its public folder: one line per frame,
  the oldest first, each with the frame's first height, its hash and its set, powers included (the
  format is in [api.md](api.md#the-public-face)). A frame is listed only when the next set has other
  names, or a name under another key, than the set that signed the block before it; a change of
  powers alone lists nothing. The node lists a frame as it commits the block before it, whose record
  entry the walk reads, so a frame whose start a catch-up skipped is not listed (`Node.noteFrame`).
  As it lists one, it drops those that started more than `retainBlocks` heights before, and keeps at
  most the newest 100. The file is their only copy: the node reads it back as it opens
  (`Node.openFrames`), and drops a line above its height in progress, which a newer snapshot it no
  longer has, or the other side of a fork, wrote.
- **The walk** (`Node.walkFrames`, `Node.signsOff`). A checkpoint that more than ⅔ of the trusted
  frame did not sign, but that is sound on its own (steps 1, 2 and 4), starts a walk. The node reads
  `hdls/frames` from the public face of each other validator of its trusted frame whose status is
  ahead of it, one face after another. From the first frame after the one it holds, it takes a frame
  only when the record entry of the height before that frame, read from the same face
  (`hdls/record/…`):
  1. holds a commit that is a quorum of the set that signed it, in which names holding more than ⅔
     of the held frame's power signed under the held frame's keys;
  2. recomputes to that commit's block hash; and
  3. names the new frame's set as its `NextValidatorsHash`.

  The node then holds the new frame, and stops as soon as more than ⅔ of the frame it holds signed
  the checkpoint: that frame is the trusted frame the checkpoint passes against, and the node adopts
  the checkpoint as above. A frame that is not signed off, or the face's last line, ends the walk of
  that face, and the next face is read. A walk takes at most 100 frames from one face, and reads
  each file up to 64 MiB, as a checkpoint.
- **Why listing only changes of names or keys is safe.** Nothing in the file is taken on a face's
  word: each frame is checked against a commit, as above, so a wrong line, or a missing one, costs a
  fetch, never a false frame. Between two listed frames the names and keys stay the same, and only
  the powers can change. The walker holds a frame's powers as they were at its first height, past
  later changes: the trust of a node that checks a checkpoint against its own older view, as every
  node did before frames. The signatures it counts are still those of the frame's own names, under
  the frame's own keys, and they must hold more than ⅔ of its power.
- **How far back.** The record keeps each height for `retainBlocks` heights (604,800 by default),
  and the file lists frames as far back. A node whose trusted frame is older than that cannot walk:
  it needs a newer seed from its bank.

## The alarms and the confirmation

- **The fork alarm** (`Node.raiseForkAlarm`). Two checkpoints that both pass commit different blocks
  at one height ahead of the node: more than ⅓ of a set signed twice. The node adopts neither, logs
  both commits in full, as the evidence people need, and halts. It does not pick one.
- **The divergence alarm** (`node/alarms.go`), in three cases:
  - any node: a checkpoint of the node's committed height, checked against the set that signed it,
    passes and commits another block than the node's own (`Node.raiseDivergenceAlarmAt`);
  - a seated node: a checkpoint that passes, of the node's height in progress, commits a block that
    does not build on the node's last block (`Node.divergesFrom`);
  - a seated node: it holds precommits from more than ⅔ of the set, in one round, for the block of
    an authentic proposal it refused (`Node.noteDivergence`): the network committed a block that its
    own state says is invalid.

  The node logs what the network committed, and its own block, and halts. It is a fork, a broken
  node, or a false seed, and people must look: the node does not seed itself again.
- **Halted** (`Node.halt`). A halted node ends its driver and starts no other, so it signs nothing
  more. It adopts no checkpoint and asks for none: on a fork it would otherwise follow whichever
  side its peers are on. Its status says why (`halted`, below), in its public folder too. The first
  alarm is the one kept, and only a restart clears it. What the operator does then is in
  [operating-a-network.md](operating-a-network.md).
- **The confirmation** (`Node.noteConfirmation`). A validator precommits only a block it applied to
  its own state, and its precommit counts only toward a commit of that block. So the first commit
  that holds the node's own precommit, while it is seated under its own key, is more than ⅔ of the
  current set agreeing with its state, whoever seeded it. From then the node is **confirmed**: its
  status says `"confirmed": <that height>`, and `"confirmed": null` before. It is kept in memory
  only: after a restart the node is unconfirmed until its next such commit.
- **An observer**, a node that never signs, is never confirmed. The fork alarm applies to it, and
  only the first case of the divergence alarm. It relies on the checks above.

The status (`/hdls/status`, and the public folder's `hdls/status`) carries `lastBlockHash`,
`confirmed` and `halted` (see [api.md](api.md#routes-that-answer-reads-get)).

## What a node still trusts, and the limits

- **The first frame.** A fresh node trusts the genesis it is given, and a joiner its bank seeds
  trusts that snapshot. Someone has to vouch for a new key once. If the seed itself is false, every
  later check runs inside a false world. A seated joiner on an altered seed is never confirmed, and
  halts with the divergence alarm once more than ⅔ of the set commit a block its state refuses
  (`TestJoinerWithAnAlteredSeedHaltsWithTheDivergenceAlarm`). An observer gets no such check. Check
  a seed's height and block hash with a second bank, by phone, before the joiner starts.
- **No single peer after it.** Every checkpoint needs more than ⅔ of a frame the node trusts, every
  frame of a walk is signed off by more than ⅔ of the one before, and every validator of the trusted
  frame is asked.

The limits:

- **A fork far ahead goes unseen.** The check against the node's own block needs a peer still at
  the node's height. A checkpoint two or more heights ahead, on the other side of a fork, is adopted
  without an alarm unless a peer on that side is at the node's own height: nothing else ties it to
  the node's own block. On an idle chain the peers wait at that height, and the check works. On a
  busy chain, a node that fell behind on the other side of a fork, with every peer already ahead,
  adopts their side silently. Seeing it would take a chain of headers. For an observer, which does
  not run the check of its height in progress, the same holds one height ahead.
- **The walk reads only public faces.** The gossip port and the operator port serve no frames and no
  record. A joiner that reaches no public face, or a network whose banks run no fetcher, must be
  seeded again across a large change of the set.
- **A carried checkpoint walks no frames** (`POST /hdls/checkpoint`, in pigeon mode): more than ⅔ of
  the node's own set must have signed it.
- **No trusting period.** The keys of more than ⅔ of any old frame on the walk's path, stolen or
  sold, can forge a history: an old frame never stops counting.
- **A large shift of power with no change of names or keys** can stop a walk. Frames are listed
  only on a change of names or keys, and the walker holds a frame's powers from its first height.
  If the powers drift a lot with no such change, and the validators whose power fell are absent
  from the commit before the next listed frame, that commit may hold ⅔ or less of the held frame's
  older powers. The walk then fails, and the node needs a newer seed. This was chosen over writing
  the frames file again at nearly every busy block, as a list of every change of powers would.
- **A reseed onto the other side of a fork** at the same height keeps the line of the frames file
  at exactly the height in progress, which may be the other side's. A walker checks it against the
  record, so it costs a fetch.
- **More than ⅓ of a set lying together** can still sign two commits at one height. The fork alarm
  turns that into a stop, not a silent choice, when the node sees both checkpoints in one round of
  catch-up.

## Mapping to the audit

| Audit finding | Fix here |
|---|---|
| A1, sub-½ fork under partition | > ⅔ quorum with intersection |
| A2, vote ≠ applied state | the commit signs the block hash, which covers the resulting state; `ApplyBlock` re-checks it |
| A4, no BFT margin | ⅔ quorum, round timeouts and locking |
| C5, MD5 content hash | SHA-256 everywhere |
| equivocation, view-local and non-durable | transferable evidence that freezes the name |
