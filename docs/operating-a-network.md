# Operating a network

Area: **API/CDN**: running `cmd/server`, the node binary. Code: `cmd/server/` and `cmd/demo/`. It
also covers `cmd/keygen/`, of the signature area, and the genesis file, `ledger/genesis.go`, of the
node area. The routes the server serves are in [api.md](api.md); keys and the sign log in
[signature.md](signature.md).

## Build

Requires Go 1.27 or later.

```
go build -o bin/server ./cmd/server
go build -o bin/keygen ./cmd/keygen
go test ./...            # the suite; add -race as CI does
```

Each command is one binary that needs nothing installed to run. `make build` builds all four into
`bin/` with `CGO_ENABLED=0`, so each is static and runs on any Linux of the same architecture. A
plain `go build` on a machine with a C compiler links the server, the demo and the wallet against
the system's C library instead; add `CGO_ENABLED=0` (for example
`CGO_ENABLED=0 go build -o bin/server ./cmd/server`) to keep them static.

## Generate keys

Every node needs an ML-DSA-65 key pair in its config, an observer included, since every node signs
a receipt for each payment a wallet posts to it (`/hdls/pay`). So does every genesis account.
Generate each pair on the machine that will hold it; the private key never leaves that machine.

```
$ ./bin/keygen
publicKey:  76kiA96VYFo6sp0MhkT+rh97sh/5hUYTEE8vSYCUhpWv/qYY…   (2604 characters)
privateKey: 6KAw6+2LUXVFz4FVvvpX2Lx/lpKyXKfVd1+xu7aAORQ=
```

The private key is the 32-byte seed the key derives from. The public key goes into the genesis file
as the key of a name; the name is the identity. People who join later make their key in their
wallet and register it at their bank (see the [user manual](../ui/USER_MANUAL.md)).

A wallet can hold a pair from `keygen`. Before the wallet's first start, write its key file
(`wallet.json` by default) with the two values `keygen` printed:

```json
{"publicKey": "<publicKey>", "privateKey": "<privateKey>"}
```

The wallet then seals the key with a passphrase at the first start that has one (see
[`ui/README.md`](../ui/README.md#its-files)). A bank's operator holds the bank's pair this way. The
bank's wallet is its desk and has its freeze button, and it signs with the key the bank's node
votes with, so run that wallet on the bank's machine. A genesis account's holder can do the same,
or start a wallet once before the network exists and give the genesis writer the `publicKey` from
its key file.

A node's config holds its private key in plaintext, and so does a key file written by hand until
the wallet seals it. Make each readable by its owner only: `chmod 600 node.json wallet.json`.

## The genesis file

Every node of a network reads the same genesis file, byte for byte (`genesisPath` in its config).
`ledger.ParseGenesis` reads it and `ledger.ValidateGenesis` checks it.

| Field | Meaning |
|---|---|
| `networkName` | the readable half of the network id; a name |
| `parameters` | the ten parameters, every one given (table below) |
| `validators` | static mode: `[{"name": "...", "power": N}]`; empty in dynamic mode |
| `validatorThreshold` | dynamic mode: the stake, as an amount, a name needs to be seated; `""` in static mode |
| `accounts` | `[{"name", "publicKey", "amount", "bank", "delegate", "description", "address"}]` |

| Parameter | Default | Note |
|---|---|---|
| `transactionCost` | `"0.1"` | burned from each payment |
| `blockIntervalMs` | `1000` | the wait between a commit and the next height |
| `stepTimeoutMs` | `3000` | the base step timeout, and the re-gossip interval |
| `maxPeers` | `0` | 0 gossips to every peer |
| `maxMessageBytes` | `6400` | the largest payment or update JSON; fits the largest payment |
| `newKeyFee` | `"1"` | the fee of a registration, key change or bank change |
| `changeDelayBlocks` | `86400` | about a day at one block a second |
| `retainBlocks` | `604800` | how long the signature record keeps a block |
| `maxPaymentAmount` | `"100000"` | keep it at or above `transactionCost`: nothing refuses a lower value, and then no payment can pass until a vote raises it |
| `maxAccountBalance` | `"10000000"` | the largest balance a payment or the ⅔ may raise a name to |

Amounts are strings: plain decimals with at most 8 decimal places. The keys are in lower camel case;
the parameters' bounds are in [governance.md](governance.md#the-parameters).

`ValidateGenesis` refuses the file, naming the field to fix, if: a name is not canonical (1 to 32
lowercase letters, digits and hyphens, starting and ending with a letter or a digit) or is given
twice; a key is not an ML-DSA-65 key in canonical base64, or is given twice; an amount does not
parse or is above `maxAccountBalance` (zero is allowed); a delegate is not an account; a bank is
the account's own name or not a name the genesis seats; both or neither of `validators` and
`validatorThreshold` are set; a static validator is not an account, is listed twice, or has power
below 1 or above 2^40 (`consensus.MaxValidatorPower`); the threshold is not above 0; the genesis
seats no name; or the parameters fail the bounds a vote is held to. `ParseGenesis` refuses a
missing parameter and any unknown key.

```json
{
  "networkName": "village",
  "parameters": {
    "transactionCost": "0.1", "blockIntervalMs": 1000, "stepTimeoutMs": 3000, "maxPeers": 0,
    "maxMessageBytes": 6400, "newKeyFee": "1", "changeDelayBlocks": 86400,
    "retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000"
  },
  "validators": [{ "name": "bank-a", "power": 1 }, { "name": "bank-b", "power": 1 },
                 { "name": "bank-c", "power": 1 }, { "name": "bank-d", "power": 1 }],
  "validatorThreshold": "",
  "accounts": [
    { "name": "bank-a", "publicKey": "<bank-a's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-a", "description": "", "address": "http://10.0.0.1:8080" },
    { "name": "bank-b", "publicKey": "<bank-b's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-b", "description": "", "address": "http://10.0.0.2:8080" },
    { "name": "bank-c", "publicKey": "<bank-c's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-c", "description": "", "address": "http://10.0.0.3:8080" },
    { "name": "bank-d", "publicKey": "<bank-d's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-d", "description": "", "address": "http://10.0.0.4:8080" },
    { "name": "alice", "publicKey": "<alice's publicKey>", "amount": "50", "bank": "bank-a",
      "delegate": "", "description": "", "address": "" }
  ]
}
```

A static validator signs with its account's key and gossips at its account's `address`. Once every
validator has an address, as here, validators take their peers from the chain as they open, and
push nothing to a node that is not a validator (see [api.md](api.md#how-messages-travel)). A
genesis name may have no bank of record (`""`); such a name changes its key only by the ⅔, and can
get a bank by a bank change.

## The network id

The effective network id is `networkName`, a `/`, and the 64 hex digits of the genesis hash, for
example `village/f41a2488…`. The genesis hash covers the name, the genesis state with its
parameters, and the validator rule. Every signature binds the id; `GET /hdls/status` serves it, the
server logs it at start, and a wallet pins it on first use.

## The node config

Each node reads its own JSON config (`server -config node.json`).

| Field | Meaning |
|---|---|
| `genesisPath` | the genesis file, the same on every node |
| `listen` | the address to serve the API on, for example `"10.0.0.1:8080"`; `":8080"` if empty |
| `name` | this node's account name; `""` for an observer |
| `publicKey`, `privateKey` | the pair as `keygen` prints it. The node derives the public key from the seed, refuses a pair that does not match, and makes one test signature at start |
| `peers` | base URLs of the other nodes, for example `["http://10.0.0.2:8080"]` |
| `bootstrap` | peer URLs to fetch a verified checkpoint from at start, and the first ones the running catch-up asks, every 3 s or every 5 block intervals if that is longer; empty on a new network |
| `snapshotPath` | where the node keeps its snapshot; its sign log and record files go beside it. Required on a node whose name is a static validator, and on every node in dynamic mode |
| `backupPath` | optional: an off-machine copy of the snapshot, for example on a mounted share; never the sign log; one write at a time. The rename is atomic only on a local file system: on NFS or CIFS a mirror can fail, and a failed mirror is ignored, with no log line |
| `maxMempool` | the cap on each pool of payments, updates and name requests; 10,000 if 0 |
| `maxCommentLength` | the cap on a payment's comment, in bytes; 280 if 0 |

The node refuses an unknown key, so a misspelt field, or one that now lives in the genesis file,
never passes silently. With an address on every validator's genesis account, a validator gossips to
the validators from the start: to every other one with `maxPeers` 0, and only to its topology
neighbours with `maxPeers` above 0. A node outside the topology keeps its `peers`.

```json
{
  "genesisPath": "genesis.json",
  "listen": "10.0.0.1:8080",
  "name": "bank-a",
  "publicKey": "<bank-a's publicKey>",
  "privateKey": "<bank-a's privateKey>",
  "peers": ["http://10.0.0.2:8080", "http://10.0.0.3:8080", "http://10.0.0.4:8080"],
  "bootstrap": [],
  "snapshotPath": "data/snapshot.json"
}
```

Each bank's config differs in `listen`, `name`, the key pair and `peers`. The server reads
`genesisPath`, `snapshotPath` and `backupPath` relative to the folder it starts in, not to the
config file. It does not create the snapshot's folder: create it first. Without it the node refuses
to start, and its error names the missing folder. In the folder
that holds `node.json` and `genesis.json`:

```
mkdir -p data
./bin/server -config node.json
```

With at least 3 of the 4 online, the network commits block 1 and then idles until there is work. A
single-validator network commits on its own, which is handy for testing.

## Network exposure

The node API has no TLS and no authentication: every route is open to anyone who reaches the port.
Validators must reach each other, so the API is exposed to the LAN they share. Keep that LAN
isolated, or run a VPN (for example WireGuard) between validators, and never forward `listen` to
the internet. A flood of requests can stall a node's event loop; put a rate-limiting proxy in front
of any wider exposure. See [security.md](security.md).

## Rejoining after a restart

A node with `snapshotPath` writes its committed state after every block and reloads it on restart,
so a node, or a whole network after a power cut, comes back without a peer. A node with `bootstrap`
peers also fetches a checkpoint from them at start, verifies it against the validators of its own
last committed state, and fast-forwards if it is ahead. Use both. A failed snapshot write is logged,
not fatal. For the ledger itself, keep a periodic off-machine copy of `GET /hdls/checkpoint`.

A node with no snapshot checks a checkpoint against the genesis validators, who may have left or
re-keyed since. So start a joiner from a node you trust: copy that node's snapshot file to the
joiner's `snapshotPath` before its first start. The joiner trusts that file as its own. The same
goes for a node so far behind that a third or less of the validator power it knew still signs.

A validator's **sign log**, `<snapshotPath>.votes`, holds what it signed at the height in progress
(see [signature.md](signature.md#the-sign-log)).

- Run each validator's key on one node only: no hot standby, and no copy of its data folder
  running elsewhere. The sign log keeps only its own node from signing twice: two nodes with one
  key double-sign, and a double-sign freezes the name. `cmd/server` binds its listen address
  before it opens the node, so a second start of one config on the same machine stops before it
  signs. A second node on another machine, or on another address, is not caught.
- Never delete the sign log, except at a new genesis, together with the snapshot.
- `backupPath` mirrors the snapshot, not the log, and it can be a height or more behind: the
  mirror skips commits while one is still being written. Never restart a validator from a backup
  while the network is at or below the last height it voted in.
- An ordinary restart between heights finds the empty log a commit leaves for the next height, and
  starts at once.
- A node that starts above its genesis without a sign log for the height it starts at signs no vote
  or proposal there for two re-gossip intervals (two `stepTimeoutMs` in force), so peers can send
  its own earlier votes and proposal back, then resumes from them. It waits so at every height it
  may have voted in: the one it starts at; each later one it commits into, up to the highest at
  which a vote or proposal of its own comes back to it; and the network's height, where a catch-up
  from those takes it. Meanwhile its sign log stays below that height, so a restart during the wait
  waits too; its first new sign log at a height holds the votes and proposal it took back there;
  and once it has a sign log, the log keeps the highest height it may have voted in, so a later
  restart still waits there. It is best effort, by timing, and recovers only what running peers
  still hold and re-gossip during it: the votes of the height they are at, their current round's
  proposal, of any round up to 256, and the commit of the height just below theirs. So it can still
  sign against its own earlier votes and proposals at a height two or more below its peers' (the
  one it starts at, say), where it signs after the wait, before it catches up; against those at the
  height just below theirs that the commit lacks; and against its proposal of a round its peers
  have left. A node that never lost a log but waits at the height it starts at anyway (an observer,
  an unseated name, a validator that adopted a checkpoint at start) also waits once after a
  catch-up from there: a delay only.
- If a validator loses its log (a dead disk, a restored backup), restart it while its peers are up,
  with bootstrap peers that answer: it then takes the network's current checkpoint before it starts
  a height, and waits at that height for its own earlier votes. From a snapshot below the last
  height it voted in, it waits so at each such height it reaches, as above, but it can still sign
  against what does not come back (above), and a double-sign freezes its name. A node with no
  snapshot starts at genesis and does not wait at all. So the safest restart is from its own
  snapshot once `GET /hdls/status` on a peer shows `heightInProgress` past the last height it voted
  in, and never from an empty data directory or an older backup on its own.

A node refuses to start if its snapshot or sign log cannot be read, belongs to another network, or
does not verify, or if the sign log is ahead of the snapshot. It never falls back to genesis on its
own, so look at the files before deleting anything.

## The record on disk, and the idle chain

A node with `snapshotPath` keeps the signature record beside the snapshot, in
`<snapshotPath>.<network>.record/` (one unit file per 100 heights) and
`<snapshotPath>.<network>.lifelong.jsonl` (never pruned). `<network>` is the first 16 hex digits of
the genesis hash. A unit file is deleted once its heights are past `retainBlocks` (604,800 by
default). See [architecture.md](architecture.md#the-signature-record).

A height starts only when there is work, so a network nobody uses commits and records nothing after
block 1 ([architecture.md](architecture.md#the-idle-chain)). A busy chain records one entry per
block: in static mode, about 1.6 GB a day with 4 validators at one block a second, and 6.2 GB with
21. In dynamic mode most entries also carry the validator set: about 2.5 GB and 11.1 GB a day. A
pending key or bank change keeps blocks coming until it falls due, about a day. Account updates are
free and count as work, so one name can keep the chain busy for nothing: the ⅔ can freeze such a
name.

## A bank whose key leaked

1. Freeze the bank's name at once: the freeze button in the bank operator's wallet, which holds the
   bank's one key. The name leaves the validator set, and every pending change it co-signed is
   cleared. A bank that holds a third or more of the power hands the chain to the rest. The last
   seated name cannot freeze itself.
2. Make a new key with `keygen`, on the bank's machine, and write it into a new wallet key file
   (see [Generate keys](#generate-keys)). That wallet is the bank's wallet from now on.
3. The other banks vote a ⅔ name action that gives the name the new key and unfreezes it. The new
   wallet signs the proof first (see [governance.md](governance.md#collecting-the-votes)).
4. Put the new pair in the bank's node config and restart it. Until then the node signs no vote or
   proposal, and at the next height the chain starts it logs that its name is seated under another
   key. It still signs payment receipts, with the old, leaked key.
5. Tell the bank's customers: key changes the bank co-signed were cleared, and they file again.

## Liveness

Finalisation needs more than ⅔ of the power online and agreeing. If each of many flaky machines
holds one unit of power, losing a third of them stalls the chain. On unreliable hardware, put
enough power on the machines you can keep awake that they alone exceed ⅔, and disable sleep on
them (`logind.conf: HandleLidSwitch=ignore`).

## Starting a new network

The node keeps a signature record but no payments: at any committed height, the state is the
accounts and the parameters. So a community can carry its state into a new genesis. This is how the
signature scheme or anything else fixed at genesis changes. It is a planned outage, not a hard fork:

1. Agree a height, and stop every node there.
2. Capture the committed state: `GET /hdls/checkpoint` gives the accounts with the commit that
   finalised them, so the copy can be checked.
3. Write the new genesis file from it: the same names, balances, banks and delegations, and new
   keys where needed. A pending change or a frozen mark does not carry over. A frozen name comes
   back unfrozen, under whatever key the file gives it: give it a key its rightful holder made, or
   leave it out and let the ⅔ create it later.
4. Choose a **new `networkName`**. An identical genesis is the same network, and replaying it lets
   anyone freeze the banks with their own old votes (see
   [security.md](security.md#the-network-id)).
5. On every node, delete the snapshot, its `.votes` sign log, and the old network's record files
   (`<snapshotPath>.<old network>.record/` and `.lifelong.jsonl`: the 16 hex digits in their names
   are the old network's). Restore no old backup.
6. Wallets start with the new network name (`-network`), and with the new validator list in their
   `.trust` file, or with no `.trust` file to pin on first use: a wallet refuses a pin of another
   network.

A release that changes the state digest, the block hash or a signed preimage is the same kind of
move: old and new nodes compute different hashes and cannot agree. Never roll it node by node. The
block hash covers a block's content as JSON, as Go writes it, so a field added to, renamed in or
dropped from anything a block carries can change it, and so can a change to how `ledger/hash.go`
builds it. A signed preimage is what `LedgerParameters.fields` lists, or the fields a message
signs (`signFields`, `voteFields`, `parameterVoteFields`). Only a release that changes none of
these can roll node by node. Even then, run one release on every node: each must hash and verify
exactly as the others do.

## Trying it instantly

`make demo` builds the node, the key generator, the demo and the wallet, then starts one node on
`127.0.0.1:8080` and the wallet with its admin page on `127.0.0.1:8091`. The founder is the only
validator and the wallet's account; the bakery and the cafe are customers of the founder's bank. A
key change waits 20 blocks there. The demo prints the command for a second wallet, on a new key,
to try a registration at the founder's bank. Ctrl-C stops it and deletes its files. The founder's
"Freeze" button is refused there: the founder is the only validator, and a freeze may not leave no
validator seated (spec R9).

## Reference node, receipts and the pin

A wallet talks to one node, its reference node, like a branch of a bank, and that node gossips its
payments on. Each payment names one block, at most 64 ahead, and is stale after it; if payments often
miss their block, raise the wallet's `-lookahead` (at most 32) or the network's `blockIntervalMs`.
The node answers with a signed receipt, like a card authorisation. The wallet believes a receipt, and
every balance it shows, only if validators it pinned signed it, in `<wallet>.trust` (see the
[wallet README](../ui/README.md)). Publish that list to your users, and again when the banks change
or re-key. A wallet reads that file only at start; how a bank makes the list is in the wallet
README's [Its files](../ui/README.md#its-files).

A payment may also carry a pin, `PreviousStateDigest`: the hex `stateDigest` that `/hdls/resolve`
serves, the digest of the committed state. A pinned payment must name the next block, the one built
on that state, and intake refuses a pin for any other block or digest with `409`. The pin binds a
payment to one chain history. Its value depends on trusting the node that reports the digest, so it
goes with running your own node. The wallet does not pin.
