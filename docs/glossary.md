# Glossary

Area: **general**. The words the docs use, in one place, and the ids the review records use. Each
entry says where to read more. "The spec" is [`review/phase2_spec.md`](../review/phase2_spec.md),
the Phase 2 design spec; "spec §5.3" is its section 5.3, and "today" in the spec means Phase 1. The
Phase 3 spec is [`review/phase3_spec.md`](../review/phase3_spec.md), and the Phase 4 spec
[`review/phase4_spec.md`](../review/phase4_spec.md); the table at the end lists their ids too.

## Names, money and governance

- **Name, account.** An account is a name: 1 to 32 characters of `a` to `z`, `0` to `9` and `-`,
  not starting or ending with `-`. It holds a key, a balance, a bank of record, a delegate, a
  description, a public address, IP addresses, TLS keys, a frozen mark and any pending change.
  Payments go to names, never to keys. See [architecture.md](architecture.md#the-state).
- **Name service.** The names on chain, read as a directory: each name publishes its key, a public
  address, up to 4 IP addresses and up to 2 TLS keys, and only its own key signs the addresses and
  the TLS keys. See [governance.md](governance.md#names-as-a-name-service).
- **Zone file.** `hdls/zone`: the names' records as a DNS zone, in the standard text format of DNS
  (RFC 1035), which an engine whose config sets `zone` writes into its public folder, and which any
  DNS server can load. It holds each name's addresses, public address and TLS keys, the addresses of
  the validators of the height in progress (the IP filter), and the lookup from an IP address to the
  names that list it. See [governance.md](governance.md#names-as-a-name-service).
- **TLS keys.** Up to 2 per name: each the SHA-256 of a web server key's SubjectPublicKeyInfo, in
  hex, what DNS TLSA records hold. Two let a name roll its key without a gap. A wallet takes a
  bank's web server only with one of the bank's TLS keys, when the bank lists any.
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
- **Sign log.** The engine's file of its own votes and proposals at the height in progress, and
  its lock (`<snapshotPath>.votes`), written to disk before any is sent, so a restart never signs
  twice. Run each validator key on one node only. A signer keeps a sign log of its own (below).
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
- **Catch-up.** A node that falls behind asks every peer it knows of for its status, and fetches
  a checkpoint from one that is ahead, and jumps to it. It trusts a checkpoint only if names of its
  trusted frame holding more than two thirds of its power signed its commit, under the keys the
  frame holds, or a walk reaches such a frame. See [consensus.md](consensus.md#catching-up).
- **Frame, trusted frame, anchor.** A frame is a validator set and the height from which it signs:
  the set between two changes of its names or keys. A node's trusted frame is the newest frame it
  already trusts: the set its last committed state seats, or a later one a walk reached. Before
  Phase 4 the docs called the node's own set its anchor, and a third of it was enough. See
  [consensus.md](consensus.md#frames-and-the-walk).
- **Next validators hash.** `NextValidatorsHash` in a block's header: the hash of the set that
  signs the next height. The block hash covers it, so each commit signs off the set that comes next.
- **Walk.** How a node that was away while the set changed catches up without trusting one peer.
  From its trusted frame it takes each later frame that more than two thirds of the frame it holds
  signed off, by the commit of the height before, until more than two thirds of the frame it holds
  signed the newest checkpoint. It reads the frames file, `hdls/frames`, and the record, from the
  other validators' public faces. See [consensus.md](consensus.md#frames-and-the-walk).
- **Fork alarm.** Two checkpoints that both pass commit different blocks at one height ahead of the
  node: more than a third of a set signed twice. The node adopts neither, logs both, and halts.
- **Divergence alarm.** The network's commit and the node's own state disagree: a checkpoint of the
  node's committed height, checked against the set that signed it, passes and commits another block
  there (any node); or, for a seated node, a checkpoint of its height in progress does not build on
  its last block, or more than two thirds of the set precommit a block it refused. The node logs
  what the network committed, and halts. See
  [consensus.md](consensus.md#the-alarms-and-the-confirmation).
- **Halted.** What a node is after an alarm: it signs nothing, adopts no checkpoint, and asks for
  none, until its operator restarts it. Its status's `halted` gives the alarm.
- **Confirmed.** A node is confirmed once a commit holds its own precommit, while it is seated:
  more than two thirds of the set then agree with its state, whoever seeded it. Its status's
  `confirmed` gives that height, or `null`. It is kept in memory only, and an observer is never
  confirmed.
- **Bootstrap peers.** Peers the node's config names. The node asks them for a checkpoint before
  its first height, and, with its gossip peers and every other validator's public address, at each
  catch-up. A bank's public address will do: it serves the status and the checkpoint as files.
- **Observer.** A node without a validator seat. It follows the chain and signs receipts. It is
  never confirmed.
- **Network name, network id, genesis.** The genesis file starts a network. The network id is the
  network name, a slash and the genesis hash in hex; every signature binds it, so a signature counts
  on one network only. A new genesis (a re-genesis) takes a new network name.

## The signer

- **Signer.** `cmd/signer`: the program that holds a bank's one key, on a machine of its own, and
  signs for the bank's own programs, its clients, on the bank's private network (`POST /sign`). It
  never signs raw bytes: a client sends a domain and the fields the ledger signs in it, and the
  signer signs only in its bank's name. The engine and a bank's wallet sign through it, or with a
  key in their own process. See [signature.md](signature.md) and [api.md](api.md#the-signers-route).
- **Client** (of the signer). A program the signer answers: the engine, the bank's desk, its admin
  page. Each has a name, a secret of its own, by whose HMAC each request proves who asks, and a list
  of the domains it may ask for.
- **Domain.** The first field of everything the ledger signs (`signing.Domain`): what kind of
  message a signature is for, such as `block-revision` for votes, `proposal`, `receipt` or
  `registration`. A signature in one domain never verifies in another.
- **Slot.** What the signer signs at most once: a vote's height, round and type, or a proposal's
  height and round. A request for a signed slot with the same fields gets the same signature again;
  with other fields it is refused, and the refusal is marked `REFUSED CONFLICT`.
- **The signer's sign log, audit log.** The signer's sign log holds each slot it signed, written to
  disk before it signs; its rules read it. A signer that lost it refuses every vote and proposal
  until it is started with `-resume-above <height>`. The audit log holds a line for every signature
  and every refusal, of any domain: the time, the client, the domain and a summary a person can
  read. Nothing reads it but people: it is how a bank finds out what a stolen terminal signed. The
  engine keeps a sign log of its own, which guards the lock (see
  [consensus.md](consensus.md#signing-only-while-seated)).
- **Key self-test.** The signature, in the domain `key-self-test`, that the engine makes at start
  over its own name and checks under its `publicKey`: a signer that holds another key, or signs for
  another bank, stops the engine before it joins.

## The wallet

- **The three pins.** (1) The wallet's pin: the network id and the validators it trusts, kept in
  `<keyfile>.trust`. It shows a state only if pinned names holding more than ⅔ of the pinned power
  signed it under their pinned keys. It follows the chain: such a state's validator set, when its
  commit is a quorum of that set too, becomes the pin. The network id never changes. (2) A payment's pin: its optional `PreviousStateDigest`, which
  binds it to the state its block builds on (the fork-binding pin). (3) A node's trusted frame
  (above) is the node's own pin of the validators it trusts.
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
  log and the record. Its config names its signer, which holds the key, or holds the key itself.
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
  accounts, governance, a record per name and per key, the names that list each IP address, the
  public bundle, the frames file, and, with a zone setting, the zone file. See
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

- **Phase 1 to 4.** Phase 1 fixed what needed no new genesis; Phase 2 is the one new genesis
  (ML-DSA-65, names as the identity, and the rest); Phase 3, built on 2026-11-01 with a new genesis
  of its own, split the deployment: the engine's two ports, the public face, the name service's
  addresses, pigeon mode and the quickstart. Phase 4, built on 2026-11-02 and 2026-11-03 with a new
  genesis of its own, added the signer, the last of the four containers; the zone file and TLS keys;
  and a joiner that trusts no single peer. See [TODO.md](../TODO.md).
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
| D1, D2 | The owner's decisions for Phase 4 | [review/phase4_spec.md](../review/phase4_spec.md) §0.2 |
| R1 to R5 | The Phase 4 spec's readings of those decisions | the Phase 4 spec, §0.3 |
| Q1 to Q15 | The Phase 4 spec's questions, all decided on 2026-11-02: the owner took every default | the Phase 4 spec, §9 |
| L1, K1, S1, N1, S2, J1, S3, N2, D1 | The slices Phase 4 was built in; D1 is the docs slice, not the decision | the Phase 4 spec, §7 |
