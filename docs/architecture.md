# Architecture

Area: **node**: the engine, which runs in a container of its own (`deploy/Dockerfile`). Code:
`ledger/`, `consensus/`, and `node/` but its HTTP side. This doc maps the packages, then describes
the ledger's rules, the block, the pools, the event loop, the idle chain, the signature record, the
public folder and the bundle. Consensus is in [consensus.md](consensus.md) and the ⅔ certificates in
[governance.md](governance.md).

The principle is the one that motivated the port from the Java original (see the
[README](../README.md#origins)): keep the rules pure and auditable, and isolate the I/O. The parts
that must be correct (money, signatures, agreement) are pure functions with tests; the part that is
hard to test (timing, networking) is a thin shell with one concurrency model.

## Packages and areas

```
  cmd/keygen   cmd/server   cmd/demo        ui/  (the wallet, its own Go module)
       │            │           │             │
       │            └─── node ──┴─────────────┘   the running node: event loop, pools, HTTP
       │                   │
       │                 ledger                    the state and its rules (pure)
       │                   │
       │               consensus                   validator set, votes, commit, driver (pure)
       │                   │
       └────────────── signing                     ML-DSA-65 and the domains (pure)
```

Imports point down only. Three more commands sit beside them: `cmd/fetcher` imports only
`ledger`, `cmd/quickstart` only `ledger` and `signing`, and `cmd/signer` `ledger`, `consensus` and
`signing`, never `node`. `ledger` uses the consensus validator set, the ⅔ certificate tally
(`CertificateTally`) and the two evidence types, never the driver. `ledger` and `consensus` do no
I/O and read no clock: no file of theirs but a test imports `time` (`TestNoRuleReadsTheClock`).
Neither does `signing`, but for its key file and its remote signer (`signing/keyfile.go`,
`signing/remote.go`), which read files and ask the signer over HTTP, with a deadline: they serve
the engine, the signer and the wallet, never a rule.

| Area | Code |
|---|---|
| signature ([signature.md](signature.md)) | `signing/`, `cmd/keygen/`, `cmd/signer/`, the sign log in `node/persist.go` |
| node (this doc) | `ledger/`, `consensus/`, `node/` but its HTTP side |
| API/CDN ([api.md](api.md)) | `node/httptransport.go` (the record, bundle and checkpoint routes too), `node/allowlist.go`, `node/relay.go`, `node/topology.go`, `node/peerdns.go`, the fetches in `node/sync.go` and `node/frames.go`, `cmd/server/`, `cmd/fetcher/`, `cmd/demo/`, `cmd/quickstart/`, `deploy/` |
| UI ([`ui/README.md`](../ui/README.md)) | `ui/` |

`cmd/keygen` prints a fresh ML-DSA-65 key pair, the private key as the 32-byte seed it derives
from; with `-encrypt` it writes a sealed key file, and with `-secret` it prints a signer client's
secret. `cmd/server` runs one node, the engine, from a config file and a genesis file. `cmd/signer`
holds a bank's key and signs for the bank's own programs. `cmd/fetcher` serves a bank's public face
from the files the engine writes. `cmd/demo` runs one node and one wallet on one machine, and
`cmd/quickstart` writes a network of four banks for the compose file in `deploy/`.

**Where the key lives.** A bank has one key. The engine's config gives either `privateKey`, the key
in the engine's own process, or `signer`, which names the signer program that holds it (`url`,
`client` and `secretFile`), never both. A bank's wallet chooses the same way, by its flags. With the
signer, the key is on a machine of its own, sealed at rest under a passphrase (a plaintext key file
is for tests), and the signer takes connections only from the addresses in its `allowFrom` list,
the bank's own programs':

```
  the internet                    the bank's own machines
  wallets, joiners ── CDN ──────► fetcher ──► engine's operator port
  other validators ─────────────────────────► engine's gossip port
                                              engine ───── POST /sign ──► signer (the key)
                                  the bank's wallet ────── POST /sign ──► signer
                                  the bank's wallet ──► engine's operator port
```

The code signs through one interface either way, `signing.Signer`: a `LocalSigner` holds the key,
and a `RemoteSigner` asks the signer and checks each answer under the bank's public key. At start
the engine signs its key self-test, over its own name, and stops unless it verifies under its
`publicKey`. What the signer allows each client, and what a break-in on each machine gets, are in
[signature.md](signature.md); its route is in [api.md](api.md#the-signers-route).

## The state

`ledger.LedgerState` is the whole committed state: `Accounts`, `Parameters`, `GovernanceSequence`
and `Height`, the height of the last block applied (0 at genesis). Its `Digest` hashes all four,
so agreement on a block is agreement on the accounts, the parameters in force, the sequence and the
height. The accounts are kept in strictly ascending byte order of name; a decoded state out of that
order is refused before anything else (`LedgerState.NamesAscending`).

`ledger.Account` holds one name: `Name`, `PublicKey` (the name's current key), `Amount`, `Bank`
(its bank of record), `Delegate` (the name its balance backs as stake), `Description`,
`PublicAddress` (its public face, such as a bank's CDN name), `IPAddresses` (at most 4; a
validator's gossip addresses), `TLSKeys` (at most 2: the hashes of its web server's keys),
`Frozen`, and at most one pending change: `PendingKey` or `PendingBank`, due at the end of block
`PendingHeight`.

- **Names.** 1 to 32 lowercase ASCII letters, digits and hyphens, starting and ending with a letter
  or a digit (`ledger.CanonicalName`). Nothing is normalised: `Alice` is not a name. A name is
  registered once and never deleted. `-` (`ledger.ClearField`) is never a name; it clears a field.
- **Amounts.** Exact rationals (`big.Rat`), each a whole number of 0.00000001 units. They are read
  only as plain decimals, 1 to 18 digits and up to 8 decimals (`ledger.ParseAmount`), and written
  in one canonical form such as `0.5` or `12` (`ledger.FormatAmount`), in JSON as strings. There is
  no floating point in the money path.
- **Caps.** `MaxPaymentAmount` bounds each payment. `MaxAccountBalance` bounds the balance a
  payment or the ⅔ raise a name to; a balance already above a lowered cap may stay and shrink.

## What changes the state

- **Payments** (`ledger.Payment`): `From`, `To`, `Amount`, `Comment`, `BlockID`,
  `PreviousStateDigest`, `Signature`. A payment names its payer and its payee; the payer's current
  key signs it. It applies only in the block whose height is its `BlockID`, which is the replay
  guard. The payer pays `Amount`; the payee gets `Amount` less the transaction cost, which is
  burned. `ledger.ProcessPayments` moves value only for a payment of at least the cost between two
  existing names (audit B1, B2), applies identical content once, and bans a payer whose payments in
  one block overdraw it: its pre-block balance is destroyed. Honest intake never lets that happen.
- **Account updates** (`ledger.AccountUpdate`): set `Delegate`, `Description`, `PublicAddress`,
  `IPAddresses` and `TLSKeys`. `""` leaves a field, `-` clears it. For `IPAddresses` and `TLSKeys`,
  an empty list leaves them, `["-"]` clears them, and any other list replaces the whole list. An
  update is free. It never names or renames an account.
- **Name requests** (`ledger.NameRequest`), four kinds, all in one pool and one route:
  - a *registration* binds a free name to a new key. The requestor pays the fee and signs; a seated
    bank, which is the requestor itself or the requestor's bank of record, co-signs and becomes the
    name's bank of record; the new key signs to prove its holder has it. It applies at once.
  - a *key change* moves a name to a new key. The holder, its bank of record and the new key sign;
    the holder pays the fee. A sale is a key change to the buyer's key.
  - a *bank change* moves a name to another seated bank. The holder and the new bank sign; the
    holder pays the fee. The old bank is not asked.
  - a *freeze*. The holder alone signs it; it is free and applies at once.

  A key change or a bank change waits `ChangeDelayBlocks` (about a day at the default) as the
  name's pending change. A seated name cannot file one: its key and bank change only by the ⅔. At
  the end of its block the change lapses if the name is seated in that block's set, or if the new
  key is by then another name's key; otherwise it applies. The fee is never refunded.
- **A freeze**, from the holder, from a double-sign, or from the ⅔, sets `Frozen`. A frozen name
  pays nothing, is paid nothing, sends no update or request, takes no delegation, backs no
  delegate with its balance, and leaves the validator set. Its own pending change is cleared, and
  so is every pending change it co-signed: the key change of a name whose bank of record it is,
  and a bank change to it. Its balance stays. Only the ⅔ name action unfreezes it, with a new key.
- **Governance**: a parameter change or a ⅔ name action, each a certificate signed by validators
  holding more than ⅔ of the power ([governance.md](governance.md)).

`ledger.ValidatorRule.SetFor` derives the validator set from the state. Static mode: each listed
name whose account exists and is not frozen, with its listed power. Dynamic mode: each unfrozen
account's balance backs its `Delegate`, and a name whose total reaches the threshold is seated with
power floor(total), at most 2^40. A frozen name is never seated.

## The block and its hashes

`ledger.Block` holds `Height`, `PreviousBlockHash`, `ValidatorsHash` (of the set that signs it),
`NextValidatorsHash` (of the set that signs the next block: the one the resulting state seats),
`Nonce` (32 random bytes the proposer draws), `Content` and `StateDigest` (of the resulting
state). `ledger.BlockContent` holds payments, account updates, name requests, vote evidence,
proposal evidence, and at most one governance action.

| Hash (`ledger/hash.go`) | Over |
|---|---|
| `ContentHash` | the content's JSON |
| `SealedDigestOf` | the nonce, the state digest and the content hash |
| `ValidatorsHash` | each member's name, key and power, in name order |
| `BlockHashOf` | the height, the previous block hash, the validators hash, the next validators hash and the sealed digest |

Votes, commits and proposals sign the block hash. So a signature covers the block's body, the set
that signs it and the set that signs the next block, and the nonce keeps the state digest out of
the signature record. Each commit so signs off the next set, which a node that walks the frames
relies on (see [consensus.md](consensus.md#frames-and-the-walk)).

`ledger.ApplyBlock(chain, previousState, previousBlockHash, block)` returns the next state or an
error naming the broken rule. It checks, all against the pre-block state: the height is the next
one and the block builds on the last block hash; the validators hash is that of the set the state
seats; the governance action is valid; evidence and freezes are valid; the other name requests,
then the payments, then the updates are valid; and no evidence brings the frozen names to a third
or more of the power. A name that the block freezes, and the ⅔ action's name, send and receive
nothing else in that block. It then applies, in order: payments, updates, name requests other than
freezes, freezes from every source, then the governance action (a parameter change, or the ⅔
action's other parts), then the changes that fall due.
The result must keep each risen balance within the pre-block cap, must seat at least one name,
and must have the digest and the next set the block claims.

`ledger.ProposeBlock` builds a block that passes those checks from what the node's pools hold. It
checks each entry's cheap rules first and its signatures only when it is about to add it. It stops
adding freezes, requests, payments and updates at 768 KiB of JSON, and carries at most 8 evidence
items. If the result would seat no name, it takes items out, the last added first, until it seats
one; those lapse or wait. Known behaviour: if two seated banks freeze themselves in one block and
together would empty the set, the block's other items come out before the second freeze does.

## Intake and the pools

The node publishes a committed view when it opens and after each commit and each catch-up: the
committed state, its digest, and the set of the next height. HTTP handlers check messages against
it, signatures included, off the event loop. The loop runs the cheap checks again against its live
state, confirms the signer's key has not changed, and pools the message. Payments (`mempool`),
updates (`updatePool`) and name requests (`requestPool`) each hold at most `maxMempool` entries. A
payer's pending outgoing is its pooled payments plus its pooled fees, and intake refuses anything
that would take it past the balance. A payer's name request wins its block: intake refuses a
payment and a fee of one payer for the same block. Intake also refuses a freeze, or a ⅔ action,
that alone would leave no name seated.

After each commit or catch-up `Node.reconcilePools` drops what can no longer apply: entries whose
block has passed, entries whose signer now has another key, entries that fail intake against the
new state, and, in arrival order, whatever no longer fits a payer's balance.

## The event loop

`node.Node` is an actor: one goroutine owns all mutable state, and every input arrives as an
event. So the node is race-free by construction, and the tests run under `-race`.

At each height `Node.startHeight` draws a nonce, builds a candidate block with `ProposeBlock`, and
starts a consensus driver for the height. The driver's actions are executed by `Node.process`:
send a proposal or a vote, arm a timer, or commit. `Node.commit` applies the decided block, saves
the snapshot, empties the sign log once the snapshot is on disk, writes the signature record,
publishes the committed view (with the gossip port's allow list), reconciles the pools, re-resolves
peers, sends the last commit to peers still at that height, writes the public folder, and starts
the next height after `BlockIntervalMs`.

A re-gossip tick comes every `stepTimeoutMs` in force (`Node.reGossipLoop`). On it the loop
re-sends what it holds for the height (see [consensus.md](consensus.md#intake-of-proposals-and-votes)),
writes the public bundle, and pulls the other validators' public bundles when the height is stuck
(below).

The snapshot file, at `snapshotPath`, holds the network id, the committed checkpoint and the
node's pending proofs (`node.savedSnapshot`). `backupPath` mirrors it, one write at a time.

## The idle chain

A height starts at once when there is work (`Node.hasWork`), and otherwise only after the idle
heartbeat, `node.IdleHeartbeat`, five minutes. Work is any of:

- no block committed yet, so block 1 always commits and a new wallet has a commit to pin;
- the candidate block carries something;
- a pool holds an entry for a later block, which lands only if the chain reaches that block;
- some account has a pending change, which falls due only if the chain reaches its height;
- the sign log holds a vote or proposal for the height (a restart mid-height);
- a peer's proposal or countable vote for the height has arrived.

Otherwise the node sets `idle`, runs no driver, and arms the heartbeat (`Node.armIdleHeartbeat`).
An idle node signs, re-gossips and records nothing. `Node.wakeIfIdle` starts the height once a
payment, update, name request or certificate is pooled, a proposal is taken, a countable vote is
kept, or a late double-sign is kept as a proof. If none comes, the heartbeat fires
`IdleHeartbeat` after the node went idle, and `Node.onIdleHeartbeat` starts the height with no work:
the block is an ordinary empty one, committed and recorded like any other. The first validator whose
heartbeat fires proposes or votes, and that wakes the others. The heartbeat starts only the height
it was armed at, and only while the node is still idle there: it never cuts short the window after
a commit or the wait of a node back without its sign log, and never starts a second driver.

Why a heartbeat: a block about every five minutes (the heartbeat starts once the window after a
commit, `blockIntervalMs`, has passed) shows that the validators are alive and agree, and lets a
wallet tell an idle chain from a stalled one. It is a constant, not a ⅔-governed parameter, so a
wallet can rely on it.

What this costs: an unused chain records 288 empty blocks a day, about 5.3 MB with 4 validators and
21 MB with 21. A pending change keeps the chain moving at `BlockIntervalMs` for
`ChangeDelayBlocks` blocks, about a day. Updates are free and count as work, so one name can keep
the chain busy for nothing; the ⅔ can freeze such a name. A late double-sign wakes an idle chain.
A proof that the one-third cap holds back does not keep the chain moving, and while the chain
idles it lives only in memory until the next commit writes the snapshot, at most a heartbeat later.

## The signature record

For each block it commits, a node with a `snapshotPath` writes a rolling entry
(`node.RecordEntry`): the height, the previous block hash, the next validators hash, the sealed
digest, the proposal of the deciding round, the commit, the signing set (when it differs from the
entry before, and in the first entry of each unit file), and `KeepUntilHeight`. From these anyone
recomputes each block hash and checks the chain. For a block that carries a governance action or
evidence it also writes a lifelong entry (`node.LifelongRecordEntry`), with the name, key and power
of each signer. Payments, updates, name requests, nonces and state digests are never written.

- Files, beside the snapshot, named by network (the genesis hash's first 16 hex digits):
  `<snapshotPath>.<network>.record/<first height, 20 digits>.jsonl`, one unit file per 100
  aligned heights, and `<snapshotPath>.<network>.lifelong.jsonl`, never pruned. A node of another
  genesis on the same path neither reads nor extends them.
- Retention: an entry's `KeepUntilHeight` is its height plus the `RetainBlocks` in force before
  its block, and never less than the entry before it in its unit file. The oldest unit file is
  deleted, whole, once the new height passes both its last entry's `KeepUntilHeight` and its last
  height plus the current `RetainBlocks`. So a longer retention applies at once and a cut only to
  later entries. The node remembers how the oldest unit file ended, and lists the folder only once
  that unit is due.
- Each height is recorded once. A node that catches up records nothing for the heights it skipped.
  A failed write is logged, never fatal, and a partial last line is skipped when read.

The routes that serve it are in [api.md](api.md#the-record-routes).

## The public folder

With `publicPath` set, the engine writes the files its fetcher serves to the public
(`node/publicfolder.go`). The fetcher reads them, and the record files, and never writes.

| File | Content | Written |
|---|---|---|
| `hdls/status` | the `/hdls/status` answer, byte for byte | as the node opens, and after each commit and catch-up |
| `hdls/checkpoint` | the `/hdls/checkpoint` answer, byte for byte | as above |
| `hdls/accounts` | the `/hdls/accounts` answer, byte for byte | as above |
| `hdls/governance` | the `/hdls/governance` answer, byte for byte | as above, and when a certificate is newly pooled |
| `hdls/resolve/name/<name>` | the name's record | when a commit or catch-up changes the record |
| `hdls/resolve/key/<SHA-256 of the key's bytes, in hex>` | the same record, found by key | as above; the old key's file goes when the key changes |
| `hdls/bundle` | the public bundle: the consensus part of the node's bundle (below), at most 8 MiB | made with the files of a commit or catch-up, and at each re-gossip tick; written only when it changed |
| `hdls/resolve/ip/<address>` | the names that list the IP address, a frozen one marked | when a commit or catch-up changes them; removed once no name lists the address |
| `hdls/zone` | the zone file, with a `zone` setting only (see [governance.md](governance.md#names-as-a-name-service)) | with the files of a commit or catch-up, only when more than its serial changed |
| `hdls/frames` | one line per frame, the index of the walk (see [consensus.md](consensus.md#frames-and-the-walk)) | when a commit starts a frame of other names or keys; read back as the node opens, its only copy |

- **A name's record** (`Node.publishNameRecord`) has the fields `/hdls/resolve` answers, but the
  committed height and state digest, which change at every block: every file would be rewritten at
  every block. It holds nothing else, so every bank of one chain publishes the same bytes for a
  name. A balance or a delegate, which the record leaves out, changes no record. A name is never
  deleted, so its file stays; names are safe file names, since the name grammar allows only `a` to
  `z`, `0` to `9` and inner hyphens.
- **The order.** `hdls/status` comes last, after the commit's record entry is on disk: the fetcher
  takes a record unit as complete, for good, once the status's height in progress has passed it.
- **Off the loop.** The loop makes each file's content, as it makes the snapshot's. A writer
  goroutine writes the files one at a time, in the order they were queued, and a file queued again
  before its write has started takes the newer content. So a slow disk costs one write of each
  file's newest content, never a backlog of old ones. `Stop` waits for the writer.
- **Whole files only.** Each write goes to a temporary file of its own in `.incoming`, then is
  renamed into place, so a reader never sees a part of a file. No other writer can rename that file
  or write into it, even one that shares the folder by mistake. The fetcher never serves
  `.incoming`.
- **No wait for the disk.** The writes are not synced. As the node opens, it clears `.incoming`,
  which may hold the temporary file of a write a crash cut short. It writes the status, checkpoint,
  accounts, governance and bundle files again, and every record file that does not hold its record
  byte for byte, so a file a power cut left empty is whole again then. A record file that already
  holds its record is left alone, so a restart does not write every name's two files again. The
  files of names and keys no longer current are removed. A failed write is logged,
  `public folder: <file>: <error>`, and the file is written again when it next changes.
- **Public files.** They are readable by anyone: mode 0644, in folders of 0755. The record files
  beside the snapshot stay readable by the engine's user only, so the fetcher runs as that user.
- **The cost.** The checkpoint and the account list are each about 2.9 KB per account, made on the
  loop at every commit: the snapshot's cost twice more. The public bundle is made again on the loop
  at each re-gossip tick, idle or not, and compared with the last one queued: on an idle chain it
  holds the last commit's precommits and its decided proposal, the same tick after tick, and is not
  written again. That is fine for a community, and a reason to move the work off the loop before a
  network grows large ([TODO.md](../TODO.md)).

## Bundles

A bundle (`node/bundle.go`) carries signed messages where the network can't: a text file, JSON
lines, that any carrier can move.

```
{"bundle":1,"networkId":"village/…","from":"bank-a","heightInProgress":1234}
{"type":"vote","body":{…}}
{"type":"proposal","body":{…}}
```

- **The header** is the first line (`BundleHeader`). `bundle` is the format's version, 1
  (`BundleVersion`): an import takes no other. `networkId` must be the importing node's, or the
  import refuses the whole bundle. `from`, the exporting node's name, and `heightInProgress` are
  for the people who carry it: no rule reads them.
- **Every other line is one message** (`BundleLine`). `type` is the last segment of its gossip
  route: `proposal`, `vote`, `payment`, `update`, `name-request`, `parameter-vote` or
  `reassignment-vote`. `body` is exactly the JSON that route takes, within that route's bound.
- **The order** (`Node.bundle`). First the consensus part, what re-gossip sends
  (`Node.consensusMessages`):
  1. the last commit's precommits and its decided proposal;
  2. the votes the node holds for the current round;
  3. the current round's proposal;
  4. the other votes it holds for the height in progress: the past rounds' first, newest first,
     then the future rounds', nearest first.

  Within a round the votes go in the order of their slots, so two nodes that hold the same
  messages write the same lines. Then the pooled certificates: the parameter changes, then the ⅔
  name actions. Then, in an export only, the pools: name requests, updates and payments, each in
  pool order. The last commit comes first so that a node one height behind decides that height
  before the next one's messages reach it.
- **The bound.** An export is at most 64 MiB (`MaxBundleBytes`), and the public bundle 8 MiB
  (`MaxPublicBundleBytes`). A bundle that would pass its bound ends at the last whole line that
  fits, so what is cut is what comes last in that order. A bank stuck for dozens of rounds holds
  more votes than the public bundle takes: the cut then drops the future rounds' votes, farthest
  first, then the oldest rounds, which no longer decide anything, and never the current round's
  votes or proposal. A rogue validator can sign votes for up to 256 rounds ahead
  (`consensus.MaxRoundLookahead`), about 2.4 MB: coming after the past rounds, they can't push out
  the recent ones, which hold the prevotes that a proposal's valid round names.
- **Export** (`Node.ExportBundle`, `GET /hdls/bundle` on the operator port) is an event on the
  loop, which makes the bundle from what it holds.
- **Import** (`Node.ImportBundle`, `POST /hdls/bundle` on the operator port) hands each line to the
  intake of its gossip route: `SubmitProposal`, `SubmitVote`, `AdmitPayment`, `AdmitUpdate`,
  `AdmitNameRequest`, `AdmitParameterCertificate` or `AdmitReassignmentCertificate`. The checks and
  signatures are those of gossip, and what the node takes it relays to its peers, as it relays
  gossip. A line that is no message is counted and skipped. A message the node already holds is
  dropped, as a second gossip copy is. The answer counts what became of the lines (see
  [api.md](api.md#bundles-and-carried-checkpoints-operator-port)). A pulled public bundle (below)
  goes through `Node.importBundle` with a check of each vote and proposal, off the loop, and stops
  at the first line that fails it, is no message, or is refused.
- **A carried checkpoint** (`Node.ImportCheckpoint`, `POST /hdls/checkpoint` on the operator port)
  goes through the catch-up event: the loop verifies it as catch-up does, with the validators of
  its last committed state as the trusted frame and no walk of the frames, and adopts it if it is
  ahead. Otherwise it gives the reason: not ahead, does not verify, diverges, the node is halted, or
  it is still starting (see [consensus.md](consensus.md#catching-up)).
- **The second path** (`Node.pullPublicBundles`). At each re-gossip tick, the loop checks whether
  the driver has run the height for 4 re-gossip intervals, the governed step timeouts, since
  `Node.startHeight` began it (`heightStartedAt`). If so, it fetches the public bundle of every
  other current validator at its public address, one goroutine each, off the loop, and imports
  each. Each goroutine checks each vote and proposal before it hands it to the loop
  (`Node.pulledCheck`). It is of the height in progress, or of the last committed height, and the
  set of that height (for the last height, the set that signed the last commit) vouches for it: a
  vote is by a validator of the set, a proposal names the proposer the set schedules for its round
  (`Node.fromItsProposer`, which `Node.onProposal` shares), and the signature verifies. A vote that
  verified is remembered, up to 4,096 of them, until the committed height changes, and is not
  checked again. The import stops at the first line that fails the check, is no message, or is
  refused. One round of fetches runs at a time: a tick that finds the last round still running
  skips its own. The fetches refuse to connect to the node's own machine: at a loopback address,
  at `0.0.0.0` or `::`, or at any of its other addresses (`Node.publicAddressClient`). It logs one
  line as it starts pulling and one as it stops (see
  [operating-a-network.md](operating-a-network.md#the-second-path-public-bundles)).
