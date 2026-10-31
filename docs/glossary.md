# Glossary

Area: **general**. The words the docs use, in one place, and the ids the review records use. Each
entry says where to read more. "The spec" is [`review/phase2_spec.md`](../review/phase2_spec.md),
the Phase 2 design spec; "spec §5.3" is its section 5.3, and "today" in the spec means Phase 1.

## Names, money and governance

- **Name, account.** An account is a name: 1 to 32 characters of `a` to `z`, `0` to `9` and `-`,
  not starting or ending with `-`. It holds a key, a balance, a bank of record, a delegate, a
  description, an address, a frozen mark and any pending change. Payments go to names, never to
  keys. See [architecture.md](architecture.md#the-state).
- **Bank, bank of record.** A bank is a name that co-signs other names' registrations and changes.
  A name's bank of record is the bank that co-signed it; a genesis name, or one the ⅔ create, may
  have none. The validators are the banks.
- **Desk.** The Requests page of a bank operator's wallet, where the bank co-signs its customers'
  requests. See [ui/USER_MANUAL.md](../ui/USER_MANUAL.md).
- **Hash name.** A name made from a key's hash, `k-` and 24 characters (26 in all), for someone who
  wants a pseudonym. It is a wallet convention: to the ledger it is an ordinary name.
- **Name request.** One message type with four kinds: a registration, a key change, a bank change
  and a freeze. The requestor signs, the bank co-signs (a freeze needs only the holder), and a new
  key signs to prove its holder has it. See [governance.md](governance.md).
- **Pending change.** A key change or a bank change waits `changeDelayBlocks` (86,400 by default,
  about a day, since a pending change keeps the chain making blocks) before it takes effect.
- **Freeze, frozen.** A frozen name neither pays, nor is paid, nor signs, and is never seated as a
  validator; it keeps its balance. A holder can freeze their own name at once, for free. A
  double-sign freezes the name. Only a ⅔ name action unfreezes a name, and it gives it a new key.
- **Fee, cost, burn.** `transactionCost` is taken from each payment, out of what the payee
  receives, and destroyed ("burned"). `newKeyFee` is paid for a registration, a key change or a
  bank change, and burned too. A freeze is free. Older texts also say a key is "burned" for what
  Phase 2 calls a frozen name.
- **Caps.** `maxPaymentAmount` bounds one payment; `maxAccountBalance` bounds a balance a payment or
  a ⅔ action may raise an account to.
- **The ten parameters.** `transactionCost`, `blockIntervalMs`, `stepTimeoutMs`, `maxPeers`,
  `maxMessageBytes`, `newKeyFee`, `changeDelayBlocks`, `retainBlocks`, `maxPaymentAmount` and
  `maxAccountBalance`. The genesis file sets them; a parameter change replaces them all at once.
  See [governance.md](governance.md#the-parameters).
- **The ⅔.** Validators holding more than two thirds of the voting power, acting together by
  signing one certificate. "The ⅔ decide" means such a certificate.
- **Certificate.** Signed votes from distinct validators holding more than ⅔ of the power, all
  valid or none counted. A block's commit is one; so are a parameter change and a ⅔ name action.
- **Parameter change.** A ⅔ certificate that replaces the ten parameters.
- **⅔ name action** (`Reassignment` in the code). A ⅔ certificate that changes one name: its
  balance (the only way to create money), key, bank of record or frozen mark, or creates a name.
- **Governance sequence.** A counter in the state that every certificate names, so each certificate
  applies once.
- **Receipt.** A node's signed acknowledgement that it pooled a payment.
- **Block ID.** The height a payment or an update names, not a hash. Each payment of a wallet names
  a block of its own.

## Validators and agreement

- **Validator, seated, power.** A validator is a name in the validator set of a height ("seated").
  Its power is its voting weight. See [consensus.md](consensus.md).
- **Static mode, dynamic mode.** In static mode, the usual one, the genesis file lists the
  validators and their powers (1 to 2^40), and only a new genesis changes them. In dynamic mode
  (a `validatorThreshold` in the genesis), every name whose delegated stake reaches the threshold
  is seated, with power equal to that stake rounded down. Delegation moves power only in dynamic
  mode.
- **Quorum.** floor(2 × total power / 3) + 1: more than two thirds.
- **Height, round.** The height is the block number. A round is one try at a height: a proposal,
  then prevotes, then precommits.
- **Lock.** Once a validator precommits a block, it prevotes no other block in a later round of
  that height unless it sees more than ⅔ prevotes for it from a round at or after its lock.
- **Commit.** More than ⅔ precommits for one block in one round: the block is final.
- **Double-sign** (equivocation). Two different votes by one name for the same height, round and
  type, or two different proposals for the same height and round. A later block carries the proof
  and freezes the name. See [signature.md](signature.md).
- **One-third cap.** A block carries double-sign proofs only while the frozen names would hold under
  a third of the power. Past that the proof waits, and recovery is a new genesis.
- **Idle chain.** A height starts only when there is work. A network nobody uses signs, re-gossips
  and records nothing after block 1; catch-up still asks the peers' status.

## The node

- **Snapshot.** The node's file of its committed checkpoint and pending proofs (`snapshotPath`),
  rewritten at each commit and catch-up. A restart starts from it.
- **Checkpoint.** The committed state with the last block's header and its commit: what
  `GET /hdls/checkpoint` serves, and what a joining node or a wallet verifies.
- **Sign log.** The node's file of its own votes and proposals at the height in progress
  (`<snapshotPath>.votes`), written to disk before any is sent, so a restart never signs twice.
  Run each validator key on one node only.
- **Signature record.** Who signed each block, kept for `retainBlocks` heights in files of 100
  heights, and every governance certificate and double-sign proof for the life of the chain. It
  keeps no payment. See [api.md](api.md).
- **Committed view.** The node's read-only copy of its committed state. Routes that need no live
  state read it without waiting for the event loop.
- **Event loop.** The one goroutine that owns the node's live state; everything that changes it goes
  through it, one event at a time. See [architecture.md](architecture.md).
- **Pools.** The node's queues of pending payments, account updates, name requests and
  certificates, waiting for a block.
- **Relay, re-gossip.** A node passes each new message on to its own peers once (relay). While a
  height runs, it sends its votes and the current proposal again every `stepTimeoutMs` (re-gossip).
- **Catch-up, anchor.** A node that falls behind fetches a checkpoint from a peer and jumps to it.
  Its anchor is the validator set of its own last committed state: it trusts a checkpoint only if
  anchor names holding more than a third of the anchor's power signed its commit, under the keys the
  anchor holds. See [consensus.md](consensus.md#catching-up).
- **Bootstrap peers.** The peers a node asks first for a checkpoint, at start and at each catch-up.
- **Observer.** A node without a validator seat. It follows the chain and signs receipts.
- **Network name, network id, genesis.** The genesis file starts a network. The network id is the
  network name, a slash and the genesis hash in hex; every signature binds it, so a signature counts
  on one network only. A new genesis (a re-genesis) takes a new network name.

## The wallet

- **The three pins.** (1) The wallet's pin: the network id and the validators it trusts, kept in
  `<keyfile>.trust`. It shows a state only if pinned names holding more than ⅔ of the pinned power
  signed it under their pinned keys. (2) A payment's pin: its optional `PreviousStateDigest`, which
  binds it to the state its block builds on (the fork-binding pin). (3) A node's anchor (above) is
  the node's own pin of the validators it trusts.
- **First use.** With no `<keyfile>.trust`, the wallet pins the validators that signed the first
  state its node serves. An operator's list in that file is safer. The wallet reads the file only
  at start.
- **Verified state, no rollback.** The state the wallet checked against its pin. It refuses a state
  older than the newest it has verified.
- **Payment counter, lookahead.** Each payment or update the wallet signs names a block of its own,
  counted from the verified state's height plus the `-lookahead` (at most 32). The wallet never
  signs two payments or updates for one block. See [ui/README.md](../ui/README.md#the-payment-counter).
- **Admin page, Requests page.** The wallet's pages for validators' governance votes and for name
  requests and the bank's desk.

## From the Java project

go_ledger is a Go port of the Hypernode ledger, written in Java (README.md, "Origins").

- **Hypernode topology.** With `maxPeers` above 0, each validator gossips only to the validators
  that share all but one coordinate of its address in a grid over the validators' names; the grid's
  dimension grows with the network so that the fan-out fits `maxPeers`, and relayed messages reach
  everyone in a few hops. With `maxPeers` 0, every validator talks to every other.
- **Head node.** The Java project's word for a node that takes part in each round: here, a
  validator.
- **Exchange window.** The Java project's word for the time in which nodes exchange a round's
  messages. Here it is the block interval (`blockIntervalMs`), and a parameter change is refused
  unless the step timeout leaves time for the gossip to reach every node.

## The build and its records

- **Phase 1, 2 and 3.** Phase 1 fixed what needed no new genesis; Phase 2 is the one new genesis
  (ML-DSA-65, names as the identity, and the rest); Phase 3, planned, splits the system into four
  containers, one per docs area. See [TODO.md](../TODO.md).
- **Owner, orchestrator, builder, checker, fixer.** The owner is the person who owns the project
  and decides. The rest are Claude agents that built and checked the code under the owner's
  direction: the orchestrator ran builders, independent checkers and fixers, slice by slice. See
  [`review/phase2_progress.md`](../review/phase2_progress.md).

Ids, by the record that defines them. A letter can mean different things in different records; the
document you are reading says which.

| Ids | What they are | Where |
|---|---|---|
| A1, A2, A4, B1, B2, C1, C5, C6, C7 | Findings of the 2026-07 security audit, whose report predates this repository's history. Each is explained where it is cited. | README.md ("Audit invariants", "Signatures", "Consensus"), docs, code comments |
| #1 to #32 | The numbered findings of the 2026-07 resolution passes | [findings.md](../findings.md) |
| C1 to C12, G1 to G11, L1 to L11 | The external GLM review of 2026-08-02; TODO.md's follow-ups cite them | [review/glm_code_review.md](../review/glm_code_review.md) |
| E1 to E45 | The post-eMudhra review of 2026-10-27 | [the review](../review/post_emudhra_review_2026-10-27.md), fixes in [findings_20261027.md](../findings_20261027.md) |
| S1 to S12, S7b | The slices Phase 2 was built in | spec §10, [review/phase2_progress.md](../review/phase2_progress.md) |
| C1 to C75 | The code notes of Phase 2's last read-through, all fixed on 2026-10-31 | [review/phase2_code_notes.md](../review/phase2_code_notes.md) |
| Q1 to Q25 | The spec's open questions: each an owner decision or a builder default | spec §12 |
| R1 to R11 | The spec's readings of the owner's decisions | spec §0.3 |
| rule 1 to rule 25 | The rules a block must meet | spec §5.3 |
| SIG-, LED-, CON-, NODE-, UI-, GEN- and a number | The hand-off review of 2026-10-31, cited in its commits | [review/phase2_progress.md](../review/phase2_progress.md) |
