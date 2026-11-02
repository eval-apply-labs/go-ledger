# Glossary

Area: **general**. The words the docs use, in one place, and the ids the review records use. Each
entry says where to read more. "The spec" is [`review/phase2_spec.md`](../review/phase2_spec.md),
the Phase 2 design spec; "spec §5.3" is its section 5.3, and "today" in the spec means Phase 1. The
Phase 3 spec is [`review/phase3_spec.md`](../review/phase3_spec.md); the table at the end lists its
ids too.

## Names, money and governance

- **Name, account.** An account is a name: 1 to 32 characters of `a` to `z`, `0` to `9` and `-`,
  not starting or ending with `-`. It holds a key, a balance, a bank of record, a delegate, a
  description, a public address, IP addresses, a frozen mark and any pending change. Payments go to
  names, never to keys. See [architecture.md](architecture.md#the-state).
- **Name service.** The names on chain, read as a directory: each name publishes its key, a public
  address and up to 4 IP addresses, and only its own key signs the addresses. See
  [governance.md](governance.md#names-as-a-name-service).
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
- **Idle chain.** A height starts at once when there is work, and otherwise after the idle
  heartbeat, five minutes (`node.IdleHeartbeat`). After block 1, a network nobody uses signs,
  re-gossips and records only one empty block per heartbeat; catch-up still asks the peers' status.
  A height unchanged for well over the heartbeat and the block interval means a stall.

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
  A bank's public address will do: it serves the status and the checkpoint as files. A running node
  then asks its gossip peers, then the other validators' public addresses: all of them when no peer
  answers or it is stuck, and otherwise one a tick, in turn.
- **Observer.** A node without a validator seat. It follows the chain and signs receipts.
- **Network name, network id, genesis.** The genesis file starts a network. The network id is the
  network name, a slash and the genesis hash in hex; every signature binds it, so a signature counts
  on one network only. A new genesis (a re-genesis) takes a new network name.

## The wallet

- **The three pins.** (1) The wallet's pin: the network id and the validators it trusts, kept in
  `<keyfile>.trust`. It shows a state only if pinned names holding more than ⅔ of the pinned power
  signed it under their pinned keys. It follows the chain: such a state's validator set, when its
  commit is a quorum of that set too, becomes the pin. The network id never changes. (2) A payment's pin: its optional `PreviousStateDigest`, which
  binds it to the state its block builds on (the fork-binding pin). (3) A node's anchor (above) is
  the node's own pin of the validators it trusts.
- **First use.** With no `<keyfile>.trust`, the wallet pins the validators that signed the first
  state its node serves. An operator's list in that file is safer. The wallet reads the file only
  at start.
- **Verified state, no rollback.** The state the wallet checked against its pin. It refuses a state
  older than the newest it has verified.
- **The other banks' check.** After a state verifies, the wallet's main page asks every bank, at
  its public address, for its height and says whether its own bank is behind, aligned with them, or
  could not be checked, and whether the chain has stalled. See [ui/README.md](../ui/README.md#how-it-works).
- **Payment counter, lookahead.** Each payment or update the wallet signs names a block of its own,
  counted from the verified state's height plus the `-lookahead` (at most 32). The wallet never
  signs two payments or updates for one block. See [ui/README.md](../ui/README.md#the-payment-counter).
- **Admin page, Requests page.** The wallet's pages for validators' governance votes and for name
  requests and the bank's desk.

## Deployment and pigeon mode

- **Engine.** `cmd/server`: the node, with the ledger, consensus, the pools, the snapshot, the sign
  log and the record. Until the signer is split out, it also holds the key.
- **Gossip port.** The engine's port that the other validators reach, `gossipListen` in its config,
  usually 8070: the network's `gossipPort`, fixed in the genesis. It serves only the routes
  validators use, behind the allow list. See [api.md](api.md#two-ports-and-the-public-face).
- **Operator port.** The engine's port for the bank's own machines, `listen` in its config: its
  fetcher, its operator's wallet and its operator's commands. It serves every route, and is never
  published.
- **Allow list.** The IP addresses that may connect to the gossip port: the node's `allow` entries,
  and every current validator's, as the chain publishes them, but for the node's own addresses and
  loopback ones. The engine enforces it in its own process, when it accepts a connection and after
  each read, so it needs each caller's own address; it also refuses a connection whose two ends are
  one address.
- **Public address.** A name's public face, `http(s)://host[:port]`: for a bank, its CDN's name,
  which wallets and joining nodes read from; for any other name, its website if it has one.
- **IP addresses.** Up to 4 per name, IPv4 or IPv6, what DNS A and AAAA records would hold. A
  validator's are where the others gossip to, and where its gossip may come from.
- **Public folder.** The files the engine writes for the public (`publicPath`): status, checkpoint,
  accounts, governance, a record per name and per key, and the public bundle. See
  [architecture.md](architecture.md#the-public-folder).
- **Fetcher, public face.** `cmd/fetcher`: a small program with no key and no state, which serves
  the public folder and the record files, read-only, and passes customers' payments, updates and
  name requests on to the operator port. It is a bank's public face, and the origin its CDN pulls
  from. See [api.md](api.md#the-public-face).
- **CDN.** A content delivery network: caches in front of the fetcher that serve the bank's public
  address over `https` and limit each client.
- **Pigeon mode.** Running the chain when the network is cut, with the banks carrying their messages
  by hand. See [operating-a-network.md](operating-a-network.md#when-the-network-splits).
- **Bundle.** A text file of signed messages, one per line, that any carrier can move. The operator
  port exports one (`GET /hdls/bundle`) and imports one (`POST /hdls/bundle`). See
  [architecture.md](architecture.md#bundles).
- **Carrier.** Whatever moves a bundle: a USB stick, an email, a radio link, a person, a pigeon.
- **Carried checkpoint.** A checkpoint brought by hand to a bank that has fallen behind, and
  imported on its operator port (`POST /hdls/checkpoint`).
- **Slow-down switch.** `minimumStepTimeoutMs` in a node's config: it makes that node's round
  timers longer, never shorter, so a round can wait for a carrier. Read at start. See
  [consensus.md](consensus.md#the-timers-and-the-slow-down-switch).
- **Public bundle, second path.** The public bundle is the consensus part of a bank's bundle, in its
  public folder. A validator whose height is stuck pulls the others' public bundles from their
  public faces: the second path, for when direct gossip is blocked.
- **Quickstart.** `make quickstart`: four banks on one machine, in containers. See
  [operating-a-network.md](operating-a-network.md#four-banks-on-one-machine-the-quickstart).

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
  (ML-DSA-65, names as the identity, and the rest); Phase 3, built on 2026-11-01 with a new genesis
  of its own, split the deployment: the engine's two ports, the public face, the name service's
  addresses, pigeon mode and the quickstart. The signer, the last of the four containers, is still
  to come. See [TODO.md](../TODO.md).
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
| Q1 to Q25 | The Phase 2 spec's open questions: each an owner decision or a builder default | spec §12 |
| R1 to R11 | The Phase 2 spec's readings of the owner's decisions | spec §0.3 |
| rule 1 to rule 25 | The rules a block must meet | spec §5.3 |
| D1 to D6 | The owner's decisions for Phase 3 | [review/phase3_spec.md](../review/phase3_spec.md) §0.2 |
| R1 to R7 | The Phase 3 spec's readings of those decisions | the Phase 3 spec, §0.3 |
| Q1 to Q13 | The Phase 3 spec's questions, all decided on 2026-11-01 | the Phase 3 spec, §13 |
| P1, P2, P3a, P3b, P4 to P8 | The slices Phase 3 was built in | the Phase 3 spec, §11 |
| N1 to N15, B1 to B8, W1 to W10, R2-1 to R2-4, R3-1 to R3-3 | The findings of the independent review of Phase 3, 2026-11-01, and of its recheck: the network surfaces (N), bundles and the public folder (B, and R2- in the recheck), the wallet (W, and R3-) | [review/_evidence_phase3/](../review/_evidence_phase3/README.md), and what became of each in [review/phase3_progress.md](../review/phase3_progress.md#the-independent-review-2026-11-01) |
| SIG-, LED-, CON-, NODE-, UI-, GEN- and a number | The hand-off review of 2026-10-31, cited in its commits | [review/phase2_progress.md](../review/phase2_progress.md) |
