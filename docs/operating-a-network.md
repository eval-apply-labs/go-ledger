# Operating a network

Area: **API/CDN**: running `cmd/server`, the engine, and `cmd/fetcher`, its public face, by hand or
in containers. Code: `cmd/server/`, `cmd/fetcher/`, `cmd/demo/`, `cmd/quickstart/` and `deploy/`.
It also covers `cmd/keygen/`, of the signature area, and the genesis file, `ledger/genesis.go`, of
the node area. The routes the server serves are in [api.md](api.md); keys and the sign log in
[signature.md](signature.md).

## Build

Requires Go 1.27 or later.

```
go build -o bin/server ./cmd/server
go build -o bin/fetcher ./cmd/fetcher
go build -o bin/keygen ./cmd/keygen
go test ./...            # the suite; add -race as CI does
```

Each command is one binary that needs nothing installed to run. `make build` builds all five (the
server, the fetcher, the key generator, the demo and the wallet) into `bin/` with `CGO_ENABLED=0`,
so each is static and runs on any Linux of the same architecture. A plain `go build` on a machine
with a C compiler links the server, the demo and the wallet against the system's C library instead;
add `CGO_ENABLED=0` (for example `CGO_ENABLED=0 go build -o bin/server ./cmd/server`) to keep them
static. `make images` builds the same programs into container images (see
[the quickstart](#four-banks-on-one-machine-the-quickstart)).

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
| `gossipPort` | the one port every validator gossips on, at each of its IP addresses: from 1 to 65535, required, with no default. 8070 is the usual choice: it is clear of the operator port's usual 8080 and the wallet's 8090 |
| `parameters` | the ten parameters, every one given (table below) |
| `validators` | static mode: `[{"name": "...", "power": N}]`; empty in dynamic mode |
| `validatorThreshold` | dynamic mode: the stake, as an amount, a name needs to be seated; `""` in static mode |
| `accounts` | `[{"name", "publicKey", "amount", "bank", "delegate", "description", "publicAddress", "ipAddresses"}]` |

| Parameter | Default | Note |
|---|---|---|
| `transactionCost` | `"0.1"` | burned from each payment |
| `blockIntervalMs` | `1000` | the wait between a commit and the next height |
| `stepTimeoutMs` | `3000` | the base step timeout, and the re-gossip interval |
| `maxPeers` | `0` | 0 gossips to every peer |
| `maxMessageBytes` | `6400` | the largest payment or update JSON; fits the largest payment, with every comment byte escaped. It follows the scheme's signature size: 6400 is ML-DSA-65's |
| `newKeyFee` | `"1"` | the fee of a registration, key change or bank change |
| `changeDelayBlocks` | `86400` | about a day at one block a second |
| `retainBlocks` | `604800` | how long the signature record keeps a block |
| `maxPaymentAmount` | `"100000"` | keep it at or above `transactionCost`: nothing refuses a lower value, and then no payment can pass until a vote raises it |
| `maxAccountBalance` | `"10000000"` | the largest balance a payment or the ⅔ may raise a name to |

Amounts are strings: plain decimals with at most 8 decimal places. The keys are in lower camel case;
the parameters' bounds are in [governance.md](governance.md#the-parameters).

`ValidateGenesis` refuses the file, naming the field to fix, if: a name is not canonical (1 to 32
lowercase letters, digits and hyphens, starting and ending with a letter or a digit) or is given
twice; the gossip port is not from 1 to 65535; a key is not an ML-DSA-65 key in canonical base64, or
is given twice; an amount does not parse or is above `maxAccountBalance` (zero is allowed); a
public address or an IP address is not in its one plain form (below); a delegate is not an account;
a bank is the account's own name or not a name the genesis seats; both or neither of `validators`
and `validatorThreshold` are set; a static validator is not an account, is listed twice, or has
power below 1 or above 2^40 (`consensus.MaxValidatorPower`); the threshold is not above 0; the
genesis seats no name; or the parameters fail the bounds a vote is held to. `ParseGenesis` refuses a
missing parameter and any unknown key.

```json
{
  "networkName": "village",
  "gossipPort": 8070,
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
      "delegate": "bank-a", "description": "", "publicAddress": "https://bank-a.example",
      "ipAddresses": ["203.0.113.1"] },
    { "name": "bank-b", "publicKey": "<bank-b's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-b", "description": "", "publicAddress": "https://bank-b.example",
      "ipAddresses": ["203.0.113.2"] },
    { "name": "bank-c", "publicKey": "<bank-c's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-c", "description": "", "publicAddress": "https://bank-c.example",
      "ipAddresses": ["203.0.113.3"] },
    { "name": "bank-d", "publicKey": "<bank-d's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-d", "description": "", "publicAddress": "https://bank-d.example",
      "ipAddresses": ["203.0.113.4"] },
    { "name": "alice", "publicKey": "<alice's publicKey>", "amount": "50", "bank": "bank-a",
      "delegate": "", "description": "", "publicAddress": "", "ipAddresses": [] }
  ]
}
```

A static validator signs with its account's key. It gossips at its account's IP addresses, on the
network's gossip port, and its public face is its account's public address. Any name may publish
both: see [governance.md](governance.md#names-as-a-name-service).

- **A public address** has one plain form: `http://` or `https://`, a host in lowercase, and an
  optional `:port`, with nothing after it, as above. The genesis and every account update refuse
  anything more, a trailing `/` included: nodes and wallets send requests to it, and a path or a
  query in it would turn those into requests for something else. A bank's public address is its
  CDN's name.
- **IP addresses** are at most 4 (`ledger.MaxIPAddresses`), IPv4 or IPv6, none twice, each in the
  one form Go's `netip` prints it: `203.0.113.1`, `2001:db8::1`, never `2001:DB8::1` or an IPv4
  address in IPv6 dress (`::ffff:203.0.113.1`), and with no zone. `0.0.0.0`, `::` and multicast
  addresses are refused: none names one machine. Private and loopback addresses are taken, so a
  network on a LAN, the quickstart and the tests work. A network on the internet lists public
  addresses. Which addresses a bank lists is in [Network exposure](#network-exposure).

Once every validator has an IP address, as here, validators take their peers from the chain as they
open, and push nothing to a node that is not a validator (see
[api.md](api.md#how-messages-travel)). A genesis name may have no bank of record (`""`); such a
name changes its key only by the ⅔, and can get a bank by a bank change.

## The network id

The effective network id is `networkName`, a `/`, and the 64 hex digits of the genesis hash, for
example `village/f41a2488…`. The genesis hash covers the name, the gossip port, the genesis state
with its parameters, and the validator rule. Every signature binds the id; `GET /hdls/status` serves
it, the server logs it at start, and a wallet pins it on first use.

## The node config

Each node reads its own JSON config (`server -config node.json`).

| Field | Meaning |
|---|---|
| `genesisPath` | the genesis file, the same on every node |
| `listen` | the operator port: every route (see [api.md](api.md#two-ports-and-the-public-face)), for the bank's own machines. For example `"127.0.0.1:8080"`, or an address on the bank's own network. If empty: `"127.0.0.1:8080"` when `gossipListen` is set, and `":8080"`, every address of the machine, when it is not, as a trial on one machine needs. Never publish it |
| `gossipListen` | the gossip port, which the other validators reach, behind the allow list: `":"` and the genesis's `gossipPort`, such as `":8070"`. `""`: no gossip port, and `listen` serves the gossip routes too, to anyone, with no allow list, as a trial on one machine needs |
| `allow` | IP addresses or prefixes the gossip port always lets in, besides the current validators' addresses, such as `["198.51.100.7", "203.0.113.0/24", "2001:db8::/48"]`. Each written as the port compares it: an IPv4 address as IPv4, no zone, and no bit set past a prefix's length. Refused without `gossipListen` |
| `name` | this node's account name; `""` for an observer |
| `publicKey`, `privateKey` | the pair as `keygen` prints it. The node derives the public key from the seed, refuses a pair that does not match, and makes one test signature at start |
| `peers` | base URLs of the other validators' gossip ports, for example `["http://203.0.113.2:8070"]`. A validator uses them until every validator has an IP address on chain; an observer, and the only validator of a network, keep them |
| `bootstrap` | base URLs to fetch a verified checkpoint from at start, and the first ones the running catch-up asks, every 3 s or every 5 block intervals if that is longer: banks' public addresses, such as `"https://bank-b.example"`, or gossip ports that let this node in. Empty on a new network |
| `snapshotPath` | where the node keeps its snapshot; its sign log and record files go beside it. Required on a node whose name is a static validator, and on every node in dynamic mode |
| `backupPath` | optional: an off-machine copy of the snapshot, for example on a mounted share; never the sign log; one write at a time. The rename is atomic only on a local file system: on NFS or CIFS a mirror can fail, and a failed mirror is ignored, with no log line |
| `publicPath` | the public folder, which the engine writes for the fetcher (see [architecture.md](architecture.md#the-public-folder)); `""`: none. The engine creates it |
| `minimumStepTimeoutMs` | the slow-down switch, for when the network splits (see [the slow-down switch](#the-slow-down-switch)): from 0, off, to 86,400,000, a day. Read at start |
| `maxMempool` | the cap on each pool of payments, updates and name requests; 10,000 if 0 |
| `maxCommentLength` | the cap on a payment's comment, in bytes; 280 if 0 |

The node refuses an unknown key, so a misspelt field, or one that now lives in the genesis file,
never passes silently. It also refuses an `allow` entry it can't read, and a switch out of its
range. With an IP address on every validator's genesis account, a validator gossips to the
validators from the start: to every other one with `maxPeers` 0, and only to its topology
neighbours with `maxPeers` above 0.

```json
{
  "genesisPath": "genesis.json",
  "listen": "127.0.0.1:8080",
  "gossipListen": ":8070",
  "name": "bank-a",
  "publicKey": "<bank-a's publicKey>",
  "privateKey": "<bank-a's privateKey>",
  "bootstrap": [],
  "snapshotPath": "data/snapshot.json",
  "publicPath": "data/public"
}
```

Each bank's config differs in `name` and the key pair. It needs no `peers`: the genesis gives every
validator an IP address. The server reads `genesisPath`, `snapshotPath`, `backupPath` and
`publicPath` relative to the folder it starts in, not to the config file. It does not create the
snapshot's folder: create it first. Without it the node refuses to start, and its error names the
missing folder. In the folder that holds `node.json` and `genesis.json`:

```
mkdir -p data
./bin/server -config node.json
```

Its first lines name the network, and say which port serves what:

```
hypernode node up: network="village/b93a37e6…" name="bank-a" listen=127.0.0.1:8080 peers=0 validators=4 validatorPower=4
gossip port: gossipListen=:8070 serves the gossip routes to the current validators' IP addresses and 0 allow entries
```

Without `gossipListen`, the second line says instead that the gossip routes are open to anyone.

With a gossip port, two more lines can follow. Each warns of a setting that is allowed, but most
often a mistake:

```
listen=:8080 is on every interface: the operator port serves every route, payments and bundle and checkpoint imports among them, with no allow list, so it must not be reachable from outside the bank's own network
gossipListen=:8071 is not on the genesis gossipPort 8070, where the other validators send: they reach this node only if a port mapping in front forwards 8070 to it
```

- **The first** comes when `listen` names no host, or `0.0.0.0` or `::`. Keep that port behind the
  machine's firewall, or give `listen` a host. The quickstart's engines print it: their operator
  ports serve their fetchers on the compose network.
- **The second** comes when the port of `gossipListen` is not the genesis's `gossipPort`. It is not
  refused, since a port mapping in front may forward the one to the other.

With at least 3 of the 4 online, the network commits block 1 and then idles until there is work,
with an empty block about every five minutes meanwhile (the idle heartbeat). A single-validator
network commits on its own, which is handy for testing.

## Network exposure

A bank serves three things, each for its own callers:

| What | Who reaches it | Where it is set |
|---|---|---|
| the gossip port | the other validators, from the IP addresses they publish | `gossipListen` (usually `":8070"`) |
| the operator port | the bank's own machines: its fetcher, its operator's wallet, its operator's `curl` | `listen` |
| the public face | anyone: customers' wallets, joining nodes, the other banks' second path | the fetcher, behind a CDN named by the bank's `publicAddress` |

- **The gossip port** serves only the routes validators use. Open it to the internet, at the IP
  addresses the bank publishes. Its allow list keeps out everyone but the current validators and
  the `allow` entries (see [api.md](api.md#two-ports-and-the-public-face)). It has no TLS: every
  message on it is signed, and nothing on it is secret. A network that wants the link encrypted can
  run its validators over a VPN, such as WireGuard, and publish the VPN addresses as their IP
  addresses.
- **The gossip port must see each caller's own address.** The allow list trusts it. Publish the
  port with rootful Docker or Podman, whose address translation keeps it; run the engine on the
  host's network; or, with rootless Podman, use the `slirp4netns` port handler
  (`--network slirp4netns:port_handler=slirp4netns`), not the default one. A forwarder or a TLS or
  TCP proxy in front makes every caller one address. When that address is the engine's own, a
  loopback one, or, with rootless Podman's default port forwarding, the container's own
  `10.0.2.100`, the engine refuses every caller, and the refusal line below shows it.
- **With Docker, publish the gossip port the default way**, through iptables (DNAT), which keeps
  the caller's address. Don't run it with Docker's userland proxy alone, with iptables turned off:
  that proxy runs outside the engine's network namespace, and makes every caller the bridge's
  gateway, such as `172.17.0.1`. The engine does not catch that. Never list a bridge's gateway in
  `allow`: everyone would get in.
- **One engine per IP address.** The gossip port refuses a connection whose two ends are one
  address, and so do the fetches from public addresses. So two engines that share an address, as
  on one machine with host networking, can't gossip with each other, or reach each other's public
  faces, through it.
- **The operator port** serves every route: customers' payments with their receipts, the account
  list, the governance votes of the admin page, bundles and carried checkpoints. Never publish it.
  Beside a gossip port it listens on `127.0.0.1:8080` unless `listen` says otherwise. Bind `listen`
  to `127.0.0.1` or to an address on the bank's own network, or keep it behind the machine's
  firewall. It has no allow list, and a flood there could stall the event loop.
- **The public face** holds nothing secret: see [The public face](#the-public-face) below.

What the allow list does not do is in
[security.md](security.md#the-two-ports-and-the-public-face): it keeps strangers off the routes that
cost a node work, but it is not authentication, and it does not stop a flood aimed at the address
itself.

### The addresses a bank connects from

The allow list holds the addresses each validator publishes. So among its up to 4 IP addresses, a
bank lists the one the others reach it at, and every address its engine's connections come from:
its pushes to the other validators, and its catch-up requests, which leave from the machine's
default address. The others refuse a connection from any other address.

- **On a host with one public address**, the connections leave from it, Docker's default address
  translation included, and one entry covers both jobs.
- **Behind a cloud NAT gateway**, list the gateway's address as well.
- **With a second line to the internet**, list that line's address too. The others' pushes fall
  back to it when the first address gets no answer.
- **With IPv6 temporary addresses**, turn them off on the engine's interface, or list them.
- **Two banks behind one NAT**, which both publish its address, reach each other from that
  address. Each one's allow list leaves out the addresses its own account publishes, that one
  among them, so each lists it in `allow`.

The others' pushes to a bank fall back to its next address only when the connection to one fails:
it is refused, the address can't be reached, or no connection is made within 2 s.

A bank that breaks the rule finds that its pushes fail. Its log says so once per peer when the
pushes start failing, and once when they work again, never once per message, and at most once a
minute for each peer:

```
gossip: pushes to the peer at http://203.0.113.2:8070 fail: <the error>
gossip: the peer at http://203.0.113.2:8070 answers again
```

The second line comes with the first push that works a minute or more after the first line. On an
idle network, which pushes little, it can come minutes after the peer answers again.

On the other side, the gossip port logs, once a minute while it refuses any, how many connections
it refused:

```
gossip port [::]:8070: refused 7 connections in the last minute from addresses not on the allow list
```

### Changing addresses

A bank changes its addresses with an account update, from the account page of its wallet. The
others refuse the new address until that update commits, and the bank's own pushes from it fail
meanwhile. So a bank whose address changed sends that update through another bank's public face:
the fetcher passes updates on. Or it asks the others to put the new address in their `allow`, and
restart, until the update commits. An update's `IPAddresses` replace the whole list, so it gives
every address the bank keeps. A bank needs a stable address; banks are meant to be visible.

## The public face

The fetcher (`cmd/fetcher`) is a bank's public face. It serves the files the engine writes in its
public folder, and the record files, read-only, and passes customers' payments, updates and name
requests on to the operator port, at most 100 a second by default. It holds no key and keeps no
state. A CDN stands in front of it.

Run it on the engine's machine, as the same user as the engine. The engine keeps its record files
readable by its own user only (files of mode 0600 in folders of mode 0700). The public folder is
readable by anyone. In the folder the engine runs in:

```
./bin/fetcher -listen :8081 -folder data/public -engine http://127.0.0.1:8080 \
    -record data/snapshot.json.<network>.record -lifelong data/snapshot.json.<network>.lifelong.jsonl
```

`<network>` is the first 16 hex digits of the genesis hash: the first 16 after the `/` of the
network id the server logs at start.

| Flag | Meaning |
|---|---|
| `-listen` | the address it serves on, the CDN's origin; `:8080` by default |
| `-folder` | the public folder: the engine's `publicPath`. Required |
| `-record` | the engine's record folder, `<snapshotPath>.<network>.record`. Required |
| `-lifelong` | the engine's lifelong record file, `<snapshotPath>.<network>.lifelong.jsonl`. Required |
| `-engine` | the base URL of the engine's operator port, such as `http://127.0.0.1:8080`. Required |
| `-max-submissions-per-second` | the most payments, updates and name requests it passes on each second; 100 by default, at least 1. Each bank sets its own: nothing else depends on it |

It logs one line as it starts, `hypernode fetcher up:` and its flags. The paths it serves, the
`Cache-Control` each file gets, and its answers are in
[api.md](api.md#the-public-face).

**The CDN.** Any CDN can do the job: the public face has no secret and nothing to compute.

- **The origin** is the fetcher. The CDN serves `https` under the bank's public address, the name on
  chain, such as `https://bank-a.example`.
- **Caching** follows the fetcher's `Cache-Control` headers: a second for the status, the
  checkpoint and the public bundle, and for a listed file that is not there yet; five seconds for
  the other files; and for good for a complete record unit. The three submission routes are
  `no-store`: the CDN passes them on to the origin. The CDN revalidates a file by its `ETag`; the
  fetcher sends no `Last-Modified`.
- **Limits per client** are the CDN's job. The fetcher's cap only keeps a flood off the engine.
- **After a new genesis**, purge the CDN's cache (see
  [Starting a new network](#starting-a-new-network)).

**The wallets.** A customer's wallet runs with `-node` set to its bank's public address. The bank
operator's wallet runs with `-node` set to the engine's operator port, such as
`http://127.0.0.1:8080`: the desk and the governance votes work only there. See
[`ui/README.md`](../ui/README.md).

A wallet's first use through the public address trusts whatever banks that address, and the CDN in
front of it, serve. So give each customer the bank's list of banks, the `.trust` file of the bank's
own wallet, to save beside the wallet file (as `wallet.json.trust` for `wallet.json`) before the
first start. Or have the customer's first use take place at the bank.

## Rejoining after a restart

A node with `snapshotPath` writes its committed state after every block and reloads it on restart,
so a node, or a whole network after a power cut, comes back without a peer. A node with `bootstrap`
peers also fetches a checkpoint from them at start, verifies it against the validators of its own
last committed state, and fast-forwards if it is ahead. Use both. A failed snapshot write is logged,
not fatal. For the ledger itself, keep a periodic off-machine copy of `GET /hdls/checkpoint`, or of
the public folder's `hdls/checkpoint`, which is the same file.

A node with no snapshot checks a checkpoint against the genesis validators, who may have left or
re-keyed since. So start a joiner from a node you trust: copy that node's snapshot file to the
joiner's `snapshotPath` before its first start. The joiner trusts that file as its own. The same
goes for a node so far behind that a third or less of the validator power it knew still signs.

A validator's **sign log**, `<snapshotPath>.votes`, holds what it signed at the height in progress
(see [signature.md](signature.md#the-sign-log)).

- Run each validator's key on one node only: no hot standby, and no copy of its data folder
  running elsewhere. The sign log keeps only its own node from signing twice: two nodes with one
  key double-sign, and a double-sign freezes the name. `cmd/server` binds its listen addresses
  (`listen`, and `gossipListen` when set) before it opens the node, so a second start of one
  config on the same machine stops before it signs. A second node on another machine, or on other
  addresses, is not caught.
- Never delete the sign log, except at a new genesis, together with the snapshot.
- `backupPath` mirrors the snapshot, not the log, and it can be a height or more behind: the
  mirror skips commits while one is still being written. Never restart a validator from a backup
  while the network is at or below the last height it voted in.
- An ordinary restart between heights finds the empty log a commit leaves for the next height, and
  starts at once.
- A node that starts above its genesis without a sign log for the height it starts at signs no vote
  or proposal there for twice the longer of `stepTimeoutMs` in force and the slow-down switch,
  `minimumStepTimeoutMs` (two re-gossip intervals with the switch off), so peers can send its own
  earlier votes and proposal back, or, in pigeon mode, the next carry can bring them, then resumes
  from them. It waits so at every height it
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

A height starts at once when there is work, and otherwise after the idle heartbeat of five minutes,
so a network nobody uses commits and records one empty block about every five minutes
([architecture.md](architecture.md#the-idle-chain)): 288 entries a day, about 5.3 MB a day and
37 MB a week with 4 validators, and 21 MB a day and 147 MB a week with 21. If the height stays the
same for well over five minutes plus `blockIntervalMs`, the chain is stalled, not idle. A busy
chain records one entry per block: in static mode, about 1.6 GB a day with 4 validators at one
block a second, and 6.2 GB with 21. In dynamic mode most entries also carry the validator set:
about 2.5 GB and 11.1 GB a day. A pending key or bank change keeps blocks coming until it falls
due, about a day. Account updates are free and count as work, so one name can keep the chain busy
for nothing: the ⅔ can freeze such a name.

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
them (`logind.conf: HandleLidSwitch=ignore`). When the network itself is cut, see the next section.

## When the network splits

An internet provider can slow the network, split it, or block it. This section is the runbook for
that case: pigeon mode, where the banks carry their messages by hand.

### What a split can and cannot break

**It cannot break correctness.**

- **No rule reads the clock.** The packages `ledger` and `consensus` never import Go's `time`
  package (`TestNoRuleReadsTheClock` checks it), a block carries no timestamp, and every deadline
  counts in blocks.
- **Every message is signed.** Each one proves itself, whoever carries it.
- So a provider that slows, splits or blocks the network can stop the chain, but it cannot fork it
  or move money. When the link comes back, the chain goes on.

**It can break progress.**

- **One bank cut off.** If the rest hold more than ⅔ of the power, they carry on. The cut-off bank
  and its customers wait, then catch up.
- **No side above ⅔.** Every node stops at the step where it needs the other side's votes. The
  prevote and precommit timers start only once votes from more than ⅔ of the power are in, so at
  that step no timer runs, and votes carried across by hand let the step complete.
- **The catch.** The propose timer always runs. A node waits `stepTimeoutMs` × (round + 1) for the
  round's proposal, 3 s at round 0 by default, then votes "no block". A proposal carried by hand
  comes too late, and rounds pass with no block. The slow-down switch, below, is the way out.

### Running in pigeon mode, step by step

1. **Notice the split.** No commits, and pushes failing in the log (see
   [The addresses a bank connects from](#the-addresses-a-bank-connects-from)).
2. **Agree to slow down.** The banks holding more than ⅔ of the power agree, out of band (by phone,
   by radio, in person), on a value for the slow-down switch. Each sets `minimumStepTimeoutMs` in
   its node config and restarts its engine. A restart that keeps the sign log starts at once. An
   engine that comes back without its sign log waits twice the switch at each height it may have
   signed at, for the next carry to bring its own earlier votes back (see
   [Rejoining after a restart](#rejoining-after-a-restart)). An engine the restart leaves behind
   catches up from the other banks' public faces, where they answer, or from a carried checkpoint.
3. **Carry the messages.** On each carry, every bank exports its bundle, and the carrier takes the
   bundles, and the newest checkpoint any bank has, to every other bank. Each bank imports what it
   receives.
4. **Carry the customers' payments.** Customers who are cut off hand their payments, saved as text,
   to whoever carries next.
5. **When the network is back,** each bank sets the switch back to 0 and restarts. Gossip and
   catch-up do the rest.

### Bundles

A bundle is a text file of signed messages, one JSON object per line. The first line is its header,
and every other line is one message, exactly as its gossip route takes it:

```
{"bundle":1,"networkId":"village/…","from":"bank-a","heightInProgress":1234}
{"type":"vote","body":{…}}
{"type":"proposal","body":{…}}
{"type":"payment","body":{…}}
```

The header names the format's version, 1, and the network: an import refuses the whole bundle of
another network, or one without that header. `from` and `heightInProgress` are for the people who
carry it: no rule reads them. A vote is about 4.7 KB, and a proposal with its
block up to 2 MiB, so a height's messages fit a USB stick, an email or a radio data link, but not a
QR code. A bundle is at most 64 MiB. Every message in it is signed, so whoever carries the file can
lose it or delay it, but cannot change a message without the change being refused. The format, and
the order of the lines, are in [architecture.md](architecture.md#bundles).

**Export.** On the bank's own machine, ask the operator port for everything the engine would gossip
now:

```
curl -o bank-a.bundle http://127.0.0.1:8080/hdls/bundle
```

**Import.** At each other bank, give the operator port the bundle a carrier brought:

```
curl --data-binary @bank-a.bundle http://127.0.0.1:8080/hdls/bundle
```

It answers what became of the messages, for example `{"queued":4,"taken":2,"refused":0,"unreadable":0}`:

- **queued**: proposals and votes, handed to the engine's consensus with no verdict;
- **taken**: payments, updates, name requests and certificates the engine newly pooled;
- **refused**: messages the engine refused, as it would refuse them from gossip;
- **unreadable**: lines that are no message.

A message the engine already holds counts in none of them, so importing a bundle twice does no
harm. A bundle of another network, or one without its header line, is refused whole, with `409` and
the reason. What the engine takes it relays to the banks it can still reach, so one import reaches
every bank on its side of the split. The gossip port never takes a bundle.

### Carrying a checkpoint

A bank more than one height behind needs a checkpoint, not votes. A checkpoint is the committed
state with the commit that signed it, and it proves itself by its ⅔ signatures. Take it from a bank
that is ahead, from its public folder's `hdls/checkpoint`, or:

```
curl -o bank-a.checkpoint http://127.0.0.1:8080/hdls/checkpoint
```

The bank that is behind imports it on its operator port:

```
curl --data-binary @bank-a.checkpoint http://127.0.0.1:8080/hdls/checkpoint
```

The engine verifies it as catch-up verifies one: names of its own last committed validator set,
holding more than ⅓ of its power, must have signed it (see
[consensus.md](consensus.md#onboarding-and-the-checkpoint)). It answers `202`, with no body, once it
has adopted the checkpoint. Otherwise it answers `409` and the reason: the checkpoint is not ahead,
it does not verify (it is another network's checkpoint, or damaged), or the node is still starting.
Add `-i` to the command to see the status. A joining node can start from a carried checkpoint too,
with the limit every joiner has: it trusts its seed.

### The slow-down switch

`minimumStepTimeoutMs`, in the node config, is the least the round timers wait per step: from 0,
off, to 86,400,000, a day.

- **What it changes.** Each round timer waits the larger of the governed `stepTimeoutMs` and the
  switch, times the round plus one. One other wait follows it: a node back without its sign log
  waits twice the larger of the two at each height it may have signed at, since in pigeon mode its
  own earlier votes come back only with the next carry. A restart that keeps its sign log never
  waits. Nothing else changes: re-gossip, catch-up, the idle heartbeat and the second path keep the
  governed timings. So the part of the network that still connects re-gossips at its usual pace.
- **How to set it.** At least twice the time a carrier takes to bring a bundle from every bank to
  every other bank. A round then completes in about three carries: the proposal, then the prevotes,
  then the precommits. A day is the most, so for blocks in the first rounds a carrier brings every
  bank's bundle to every other within half a day. With a slower carrier, blocks still commit, in a
  later round, since each round waits longer than the one before.
- **Who must set it.** The banks holding more than ⅔ of the power. One bank alone gains nothing:
  the others still vote "no block" after their governed step timeout, 3 s by default. Each bank may
  set its own value, and mixed values are safe.
- **When it takes effect.** At the engine's next start. No route changes it while the engine
  runs, and a crash or a restart does not switch it off. The start log says that it is on, here
  at 10 minutes:

  ```
  the slow-down switch is on: minimumStepTimeoutMs=600000, so each round waits at least 10m0s per step for a proposal or votes; set it to 0 and restart once the network is back
  ```
- **How to turn it off.** When the network is back, set it to 0, or remove it, and restart.
- **Why it is safe.** No rule reads the clock. A longer timer only makes a node wait longer before
  it votes "no block": it cannot make it sign twice or take an invalid block.

The governed timings, `stepTimeoutMs` and `blockIntervalMs`, remain the way to set a slow tempo for
everyone, for good, by a ⅔ vote (see [governance.md](governance.md#the-parameters)). The switch is
the way in when the network cannot carry that vote.

### The second path: public bundles

Where direct gossip is blocked but the banks' public faces still get through, the engines exchange
votes and proposals through them on their own. Nobody has to act.

- **What each engine publishes.** An engine with a `publicPath` keeps `hdls/bundle` there, the
  public bundle: the consensus part of its bundle (no payments, updates or name requests), at most
  8 MiB. The engine makes it at each commit and catch-up and at each re-gossip tick, and writes it
  when it changed. Past 8 MiB, as after dozens of rounds, the cut drops what comes last: the pooled
  certificates, then the oldest rounds' votes, never the current round's votes or proposal (see
  [architecture.md](architecture.md#bundles)).
- **When it pulls.** Once the engine has run a height for 4 step timeouts (the governed ones, never
  the switch's) without a commit, it fetches `<public address>/hdls/bundle` of every other current
  validator that publishes a public address. It does so once each re-gossip interval, and imports
  each, until it commits. It follows no redirect, gives up on a connection after 2 s and on a fetch
  after 5 s, reads at most 8 MiB of each, and refuses to connect to the engine's own machine, at a
  loopback address, at `0.0.0.0` or `::`, or at any of its other addresses.
- **What it checks.** Before a pulled vote or proposal reaches the engine's event loop, the pull
  checks it. It must be of the height in progress, or of the last committed height, and the set of
  that height (for the last height, the set that signed the last commit) must vouch for it: a vote
  is by a validator of the set, a proposal names the proposer the set schedules for its round, and
  its signature verifies under the key the set holds. The import stops at the first line that fails
  the check, is unreadable or of an unknown type, or that its intake refuses. The only such line an
  honest public bundle can hold is a certificate the engine refuses, and certificates come last. So
  a forged bundle, from a hostile public face or from anyone on a plain-HTTP path to it, costs the
  engine one check, off the loop, or one line on it. A bank one height ahead gives its last commit,
  which decides the engine's height, but not its messages of the height after: the import stops
  there, and the engine takes them at its pulls at that height. An operator's import on the operator
  port is not checked this way: it hands every line to its intake, and goes on past a refused one.
- **What it costs.** A signature check for each proposal pulled, and for each vote pulled the first
  time, off the event loop. A vote that verified is not checked again: the engine remembers such
  votes, up to 4,096, until its committed height changes.
- **Its log lines.** One as it starts pulling, and one as it stops:

  ```
  public bundles: height 1234 has run for 4 step timeouts without a commit: the node pulls the other validators' public bundles until it commits
  public bundles: the node stops pulling them, having committed height 1234
  ```
- **What it needs.** Every validator's engine has a `publicPath`, a fetcher serves it at the
  public address the validator publishes, and the engines can reach the others' public faces. And
  it needs the slow-down switch, for blocks to commit in the first rounds: without it, the propose
  timer fires after one step timeout, and the pull starts only after four, so round 0 never commits
  by pulls. A height then commits only in a later round, once the round timers have grown longer
  than the pulls take.
- **Catching up the same way.** An engine more than one height behind needs a checkpoint, and the
  public faces serve one. When none of its bootstrap or gossip peers answers, or while it is stuck,
  catch-up asks every other validator's public address; otherwise one of them a tick, each in turn,
  so an idle engine whose gossip peers lag with it finds the banks ahead too (see
  [api.md](api.md#how-messages-travel)). So an engine that a restart or a crash left behind
  recovers through the public faces, with no `bootstrap` set.
- **What it does not need.** Trust: every message is signed. Nor the allow list: the pull goes out,
  it does not come in.

### Customers

- **A payment saved as text.** The wallet's send page can sign a payment and show it as text, a
  bundle of one message, instead of sending it. Anyone can carry it to any bank, and that bank
  imports it like any bundle. The wallet spends a block of its payment counter on it, as on a sent
  payment, so it never signs two payments for one block.
- **A checkpoint from a file.** When the wallet can't reach its bank, it can load a checkpoint
  someone carried in, and verifies it as it verifies its bank's. The file's state stands in only
  while the bank's node can't be read, or serves an older state. Meanwhile the wallet sends no
  payment or update to that node: it saves payments as text, and the freeze still works. See the
  [user manual](../ui/USER_MANUAL.md).
- **The limit.** A payment counts only in the block it names, which the wallet picks at most 64
  blocks past the height in progress (`node.MaxBlockIDLookahead`). In pigeon mode that is many
  carries away. But while the rest of the network runs at full speed, the chain passes that block in
  about a minute when it is busy, or about five hours when it idles, and a payment that arrives
  after it is refused. A customer cut off from a fast chain needs some network path, or has to wait
  (owner, 2026-11-01).

## Starting a new network

The node keeps a signature record but no payments: at any committed height, the state is the
accounts and the parameters. So a community can carry its state into a new genesis. This is how the
signature scheme or anything else fixed at genesis changes. It is a planned outage, not a hard fork:

1. Agree a height, and stop every node there.
2. Capture the committed state: `GET /hdls/checkpoint` gives the accounts with the commit that
   finalised them, so the copy can be checked.
3. Write the new genesis file from it: the same names, balances, banks, delegations, public
   addresses and IP addresses, and new keys where needed. A pending change or a frozen mark does
   not carry over. A frozen name comes back unfrozen, under whatever key the file gives it: give it
   a key its rightful holder made, or leave it out and let the ⅔ create it later.
4. Choose a **new `networkName`**. An identical genesis is the same network, and replaying it lets
   anyone freeze the banks with their own old votes (see
   [security.md](security.md#the-network-id)).
5. On every node, delete the snapshot, its `.votes` sign log, and the old network's record files
   (`<snapshotPath>.<old network>.record/` and `.lifelong.jsonl`: the 16 hex digits in their names
   are the old network's). Restore no old backup. The engine writes its public folder again as it
   starts.
6. Restart each fetcher with the new network's record files in `-record` and `-lifelong`, and purge
   the CDN's cache: a complete record unit is cached for good, under a path that does not name the
   network.
7. Wallets start with the new network name (`-network`), and with the new validator list in their
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

The demo's node serves every route on its one port, with no allow list. Its genesis publishes that
port as the founder's public address, and its address as the founder's IP address, so the demo's
`-listen` takes an IP address and a port, such as `127.0.0.1:8080`: `localhost:8080` is refused.
`0.0.0.0:8080` or `[::]:8080` serves on every interface; the founder then publishes `127.0.0.1` as
its IP address, and `http://127.0.0.1:8080` as its public address, since the ledger takes neither
`0.0.0.0` nor `::` as a name's address.

## Four banks on one machine: the quickstart

`make quickstart` runs a network of four banks on this machine, in containers, with the steps of
this runbook done for you. It needs Docker with Compose, or Podman (below).

```
make quickstart
```

1. **It builds the images** (`make images`): `go-ledger`, with the engine, the fetcher and `keygen`
   (`deploy/Dockerfile`), and `go-ledger-ui`, the wallet (`deploy/ui.Dockerfile`). Each program is
   a static binary in an empty image, with no shell, run as the numeric user 65532, never root.
   `.dockerignore` keeps out of the build `deploy/quickstart/`, `bin` and `.git`, and, under `ui/`,
   the wallet's files: a key file named `*.json` (`wallet.json` by default), the files the wallet
   writes beside it, and any `.state` or `.trust` file. A key file of another name under `ui/` would
   go in: keep it elsewhere.
2. **The first time, it writes a new network** into `deploy/quickstart/`, with `cmd/quickstart`:
   - a key pair for each of four banks, `bank-a` to `bank-d`, each a validator of power 1;
   - the genesis: the network `quickstart`, gossip port 8070, the default parameters but for a key
     change that waits 20 blocks and a record that keeps 1,000 blocks;
   - each bank's node config, with the bank's private key;
   - a wallet file for each of two funded customers, `alice` at `bank-a` and `bob` at `bank-b`,
     500 each;
   - `network.env`, which names the engines' record files for the fetchers.

   It prints the network id and the commands for the two wallets. It refuses a folder that is there
   already, so it never replaces the keys of a network that may be running: later runs start the
   same network again.
3. **It starts `deploy/compose.yaml`**: each bank is an engine and a fetcher.

| Container | Address | Ports |
|---|---|---|
| `bank-a-engine` to `bank-d-engine` | `172.28.70.11` to `172.28.70.14`, on the compose network `172.28.70.0/24`; the genesis lists them as the banks' IP addresses | the gossip port 8070 and the operator port 8080, on the compose network only |
| `bank-a-fetcher` to `bank-d-fetcher` | `172.28.70.21` to `172.28.70.24`; the genesis gives their names, `http://bank-a-fetcher:8080` and so on, as the banks' public addresses | 8080, published on this machine only, at `127.0.0.1:18081` to `127.0.0.1:18084` |

- Each engine's allow list lets in the other three engines' addresses only. No port of an engine is
  published on this machine. On a real network the operator port sits on the bank's own network;
  here every container shares one, and each engine's start log warns that its operator port is on
  every interface. So here a validator could publish another engine's operator port, such as
  `http://172.28.70.11:8080`, as its public address, and every stuck engine would pull that
  engine's full export. It is a trial: on a real network no other bank reaches an operator port.
- Every container of the compose file has a fixed address, so none of them takes a stopped engine's
  address from the network's pool: the other engines would let it in. A container you start on that
  network yourself still takes an address from the pool, and may take a stopped engine's.
- There is no CDN: the fetchers are the public faces.
- Each engine keeps its snapshot, sign log, record files and public folder in a volume of its own,
  `bank-a-data` and so on, which its fetcher mounts read-only. Both run as the same user, so the
  fetcher can read the record files.
- Each node config is mounted read-only into its engine, as a Compose secret. The folder
  `deploy/quickstart/` is readable by its owner only, but the configs and the genesis inside it
  are readable by anyone, since the containers' user is not yours. Git and the image builds leave
  the folder out (`.gitignore`, `.dockerignore`). It is a trial: put no value on its keys.

**Every Compose command needs `--env-file deploy/quickstart/network.env`.** That file gives the
fetchers the first 16 hex digits of the genesis hash, which name the engines' record files, and
Compose refuses the compose file without it. For example, to follow bank-a's engine:

```
docker compose -f deploy/compose.yaml --env-file deploy/quickstart/network.env logs -f bank-a-engine
```

`make quickstart-clean` removes the whole network: its containers, its Compose network, its volumes
with the chain, and `deploy/quickstart/` with the keys. The next `make quickstart` makes a new one.

**A wallet.** Build the wallet, then run a customer's wallet on this machine against its bank's
fetcher, as the quickstart prints:

```
make build
bin/go_ledger_ui -node http://127.0.0.1:18081 -network quickstart -wallet deploy/quickstart/alice.wallet.json -listen 127.0.0.1:8090
```

Open `http://127.0.0.1:8090`. At its first start the wallet asks for a passphrase to seal the key
(empty keeps it plaintext). Bob's wallet is the same with `18082`, `bob.wallet.json` and
`-listen 127.0.0.1:8092`, so both run at once; his page is at `http://127.0.0.1:8092`.

Its limits there:

- **The other banks' check.** The public addresses on chain are names on the Compose network,
  which this machine cannot look up. The wallet asks every bank at its public address, its own
  bank's included, so a wallet on this machine reaches none of the four, and its main page says
  that it could only check banks holding 0% of the power. A wallet in a container on the Compose
  network reaches all four. Compose names that network `deploy_ledger`, after the folder of the
  compose file:

  ```
  docker run --rm --network deploy_ledger -p 127.0.0.1:8090:8090 -v carol-wallet:/wallet \
      -e GOLEDGER_WALLET_PASSPHRASE=<a passphrase> go-ledger-ui -node http://bank-a-fetcher:8080 -network quickstart
  ```

  It makes a new key, with no name yet, in the volume `carol-wallet`. The container has no terminal
  to ask for a passphrase, so `GOLEDGER_WALLET_PASSPHRASE` gives one, which seals the key. Publish
  the wallet's port on this machine only, and under the same number: the wallet refuses a request
  for another port.
- **No bank operator's wallet.** The banks' desks and admin pages need a wallet that holds a bank's
  key and reaches its engine's operator port. The quickstart writes none, and only the Compose
  network reaches the operator ports.

**With Podman.** `make` uses Docker by default. With Podman and the older Compose
(`docker-compose`, version 1), as on Ubuntu 22.04, start Podman's Docker-compatible socket for
your user, point Compose at it, and tell `make` which commands to use:

```
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
make quickstart CONTAINER=podman COMPOSE=docker-compose
```

Give `make quickstart-clean` the same `COMPOSE=docker-compose`, and write `docker-compose` for
`docker compose` in the commands above. Podman 3.4, with container network plugins older than
version 1.0 (Ubuntu 22.04's), writes the Compose network's settings with `"cniVersion": "1.0.0"`,
which those plugins refuse: the containers do not start, and Podman says it cannot find the CNI
network. Set it to `0.4.0` in that network's file, then run `make quickstart` again:

```
sed -i 's/"cniVersion": *"1.0.0"/"cniVersion": "0.4.0"/' ~/.config/cni/net.d/deploy_ledger.conflist
```

## Reference node, receipts and the pin

A customer's wallet talks to one bank, at the bank's public address, like a branch of a bank, and
that bank's engine gossips its payments on. Each payment names one block, at most 64 ahead, and is
stale after it; if payments often miss their block, raise the wallet's `-lookahead` (at most 32) or
the network's `blockIntervalMs`. The engine answers with a signed receipt, like a card
authorisation. The wallet believes a receipt, and every balance it shows, only if validators it
pinned signed it, in `<wallet>.trust` (see the [wallet README](../ui/README.md)). Publish that list
to your users. A wallet follows the banks as they change, while more than ⅔ of the power it pinned
signs each change; one that missed more than that needs the list again. A wallet reads that file
only at start; how a bank makes the list is in the wallet README's
[Its files](../ui/README.md#its-files). Give every validator a public address on its account
(`publicAddress` in the genesis, or the account form): wallets ask each bank's public address for
its height, to catch a bank that hides newer blocks.

A payment may also carry a pin, `PreviousStateDigest`: the hex `stateDigest` that `/hdls/resolve`
serves, the digest of the committed state. A pinned payment must name the next block, the one built
on that state, and intake refuses a pin for any other block or digest with `409`. The pin binds a
payment to one chain history. Its value depends on trusting the node that reports the digest, so it
goes with running your own node. The wallet does not pin.
