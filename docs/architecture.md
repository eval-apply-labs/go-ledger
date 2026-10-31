# Architecture

Area: **node**, the engine that Phase 3 runs in a container of its own. Code: `ledger/`,
`consensus/`, and `node/` but its HTTP side. This doc maps the packages, then describes the ledger's
rules, the block, the pools, the event loop, the idle chain and the signature record. Consensus is
in [consensus.md](consensus.md) and the ⅔ certificates in [governance.md](governance.md).

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

Imports point down only. `ledger` uses the consensus validator set, the ⅔ certificate tally
(`CertificateTally`) and the two evidence types, never the driver. `ledger`, `consensus` and
`signing` do no I/O and read no clock.

| Area | Code |
|---|---|
| signature ([signature.md](signature.md)) | `signing/`, `cmd/keygen/`, the sign log in `node/persist.go` |
| node (this doc) | `ledger/`, `consensus/`, `node/` but its HTTP side |
| API/CDN ([api.md](api.md)) | `node/httptransport.go` (the record routes too), `node/relay.go`, `node/topology.go`, `node/peerdns.go`, the fetches in `node/sync.go`, `cmd/server/`, `cmd/demo/` |
| UI ([`ui/README.md`](../ui/README.md)) | `ui/` |

`cmd/keygen` prints a fresh ML-DSA-65 key pair, the private key as the 32-byte seed it derives
from. `cmd/server` runs one node from a config file and a genesis file. `cmd/demo` runs one node and
one wallet on one machine.

## The state

`ledger.LedgerState` is the whole committed state: `Accounts`, `Parameters`, `GovernanceSequence`
and `Height`, the height of the last block applied (0 at genesis). Its `Digest` hashes all four,
so agreement on a block is agreement on the accounts, the parameters in force, the sequence and the
height. The accounts are kept in strictly ascending byte order of name; a decoded state out of that
order is refused before anything else (`LedgerState.NamesAscending`).

`ledger.Account` holds one name: `Name`, `PublicKey` (the name's current key), `Amount`, `Bank`
(its bank of record), `Delegate` (the name its balance backs as stake), `Description`, `Address`
(a validator's gossip URL), `Frozen`, and at most one pending change: `PendingKey` or
`PendingBank`, due at the end of block `PendingHeight`.

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
- **Account updates** (`ledger.AccountUpdate`): set `Delegate`, `Description` and `Address`. `""`
  leaves a field, `-` clears it. An update is free. It never names or renames an account.
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
`Nonce` (32 random bytes the proposer draws), `Content` and `StateDigest` (of the resulting
state). `ledger.BlockContent` holds payments, account updates, name requests, vote evidence,
proposal evidence, and at most one governance action.

| Hash (`ledger/hash.go`) | Over |
|---|---|
| `ContentHash` | the content's JSON |
| `SealedDigestOf` | the nonce, the state digest and the content hash |
| `ValidatorsHash` | each member's name, key and power, in name order |
| `BlockHashOf` | the height, the previous block hash, the validators hash and the sealed digest |

Votes, commits and proposals sign the block hash. So a signature covers the block's body and the
set that signs it, and the nonce keeps the state digest out of the signature record.

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
and must have the digest the block claims.

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
publishes the committed view, reconciles the pools, re-resolves peers, sends the last commit to
peers still at that height, and starts the next height after `BlockIntervalMs`.

The snapshot file, at `snapshotPath`, holds the network id, the committed checkpoint and the
node's pending proofs (`node.savedSnapshot`). `backupPath` mirrors it, one write at a time.

## The idle chain

A height starts only when there is work (`Node.hasWork`). Work is any of:

- no block committed yet, so block 1 always commits and a new wallet has a commit to pin;
- the candidate block carries something;
- a pool holds an entry for a later block, which lands only if the chain reaches that block;
- some account has a pending change, which falls due only if the chain reaches its height;
- the sign log holds a vote or proposal for the height (a restart mid-height);
- a peer's proposal or countable vote for the height has arrived.

Otherwise the node sets `idle` and runs no driver. An idle node signs, re-gossips and records
nothing. `Node.wakeIfIdle` starts the height once a payment, update, name request or certificate is
pooled, a proposal is taken, a countable vote is kept, or a late double-sign is kept as a proof.

What this costs: a pending change keeps the chain moving at `BlockIntervalMs` for
`ChangeDelayBlocks` blocks, about a day. Updates are free and count as work, so one name can keep
the chain busy for nothing; the ⅔ can freeze such a name. A late double-sign wakes an idle chain.
A proof that the one-third cap holds back does not keep the chain moving, and while the chain
idles it lives only in memory until the next commit writes the snapshot.

## The signature record

For each block it commits, a node with a `snapshotPath` writes a rolling entry
(`node.RecordEntry`): the height, the previous block hash, the sealed digest, the proposal of the
deciding round, the commit, the signing set (when it differs from the entry before, and in the
first entry of each unit file), and `KeepUntilHeight`. From these anyone recomputes each block hash
and checks the chain. For a block that carries a governance action or evidence it also writes a
lifelong entry (`node.LifelongRecordEntry`), with the name, key and power of each signer. Payments,
updates, name requests, nonces and state digests are never written.

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
