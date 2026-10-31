# The HTTP API

Area: **API/CDN**, the part Phase 3 moves into an edge proxy (the "fetcher") in front of the engine.
Code: `node/httptransport.go` (`Server`, with the record routes `handleRecord` and
`handleLifelongRecord`, and `HTTPTransport`), `node/relay.go`, `node/peerdns.go`,
`node/topology.go`, the fetches in `node/sync.go`, and `cmd/server`.

This doc lists every route, what it answers and what it costs a node, then how messages travel
between nodes. Running the server is in [operating-a-network.md](operating-a-network.md).

## One open port

A node serves every route on one port, `listen` in its config. There is no TLS and no
authentication: anyone who reaches the port can call any route. Signatures make votes, blocks and
payments trustworthy whoever relays them, but nothing limits how often anyone calls. Keep the port
on an isolated LAN or a VPN, and never forward it to the internet. See [security.md](security.md).

`cmd/server` sets per-connection deadlines: 5 s to read the headers, 15 s to read the request,
30 s to write the answer, 2 minutes idle. Every POST body is read up to 1 MiB, except a proposal's,
up to 2 MiB. A body that does not decode, or is over its limit, gets `400`. A route called with
another method gets `405`, with an `Allow` header naming the methods it takes: `POST`, or
`GET, HEAD` (a GET route also answers HEAD).

A node reads at most 64 MiB of a peer's checkpoint (`node.MaxCheckpointBytes`): about 23,000
accounts at 2.9 KB each, fewer while key changes are pending (5.5 KB for such an account). Past
that, catch-up and joining by checkpoint fail, and nothing is logged. The bound is a constant in
the code: raise it before a network grows that large.

A payment's, update's or name request's checks and signatures run in the HTTP handler, off the
node's event loop, against the committed view (the last committed state, published when the node
opens and after each commit and each catch-up). So do `/hdls/status`, `/hdls/resolve` and the two
record routes. The other routes queue an event on the loop, and the rows below say so: a flood of
those delays consensus on that node.

## Routes that take messages (POST, JSON)

| Route | Body | Answer | Cost to the node |
|---|---|---|---|
| `/hdls/pay` | a signed `Payment` | the node's signed `Receipt` when it newly pools the payment; `202` and no receipt for a payment it already holds; `409` with the reason when it refuses | checks against the committed view, then one signature check, off the loop; the cheap checks again on the loop; one receipt signature (about 0.9 ms) per new payment |
| `/hdls/payment` | a payment a peer relays | `202`, or `409` with the reason | as `/hdls/pay`, without the receipt |
| `/hdls/update` | a signed `AccountUpdate` | `202`, or `409` with the reason | as a payment: one signature check off the loop |
| `/hdls/name-request` | a signed `NameRequest` (registration, key change, bank change or freeze) | `202`, or `409` with the reason | off the loop, the shape and the cheap checks first, then the signatures: three for a registration or a key change, two for a bank change, one for a freeze; the cheap checks again on the loop |
| `/hdls/parameter-vote` | a complete `ParameterChange` certificate | `202` once the node pools it, now or before, or `409` with the reason | on the loop: a copy with the votes of a pooled certificate is dropped before any check; otherwise one check per vote |
| `/hdls/reassignment-vote` | a complete ⅔ name action (`Reassignment`) | `202` once the node pools it, now or before, or `409` with the reason | as a parameter change, plus the new key's proof |
| `/hdls/proposal` | a `ProposalMessage`: the signed proposal and its block | `202` | on the loop: the cheap checks, one signature check, the block hash, then `ApplyBlock`; after two authentic proposals of one round fail to apply, none more for that round |
| `/hdls/vote` | a signed consensus `Vote` | `202` | on the loop: a byte-identical copy of a vote the node holds is dropped before any check; a new vote costs two checks |

A payment or an update must name a block from the height in progress to 64 blocks past it. A name
request may name a block up to 3,600 blocks past it, except a freeze, which gets the 64 blocks of a
payment (owner, 2026-10-30). A node holds one update per name and block, and one name request per
name and block. The intake rules are in [architecture.md](architecture.md#intake-and-the-pools).

## Routes that answer reads (GET)

| Route | Answer | Cost to the node |
|---|---|---|
| `/hdls/status` | `heightInProgress` (one above the committed height), `accounts`, and `networkId`, the effective network id a wallet pins | off the loop, from the committed view |
| `/hdls/accounts` | every account (`Name`, `PublicKey`, `Amount`, `Bank`, `Delegate`, `Description`, `Address`, `Frozen`, `PendingKey`, `PendingBank`, `PendingHeight`) | an event on the loop; the answer is about 2.9 KB per account |
| `/hdls/checkpoint` | the committed state, the last block's header, its commit and the set that signed it (see [consensus.md](consensus.md#onboarding-and-the-checkpoint)) | as `/hdls/accounts`, plus the commit and the set |
| `/hdls/governance` | the ten parameters, the governance sequence, the validators by name, total power, quorum, and the pooled certificates with their voters' power | an event on the loop |
| `/hdls/resolve?name=N` or `?key=K` | always the account's `name` (the answer to `?key=`), `publicKey`, `bank`, `frozen`, `pendingKey`, `pendingBank`, `pendingHeight`, the committed `height` and `stateDigest`, and, only when set, `address` and `description`; `400` unless exactly one of `name` and `key` is given; `404` if no account matches | off the loop, from the committed view |
| `/hdls/record` | rolling record entries (below) | off the loop: at most two unit files |
| `/hdls/lifelong-record` | lifelong record entries (below) | off the loop: a scan of the lifelong file |

`/hdls/status` gives the height in progress as `heightInProgress`; `height` in `/hdls/resolve` is the
committed height.

`/hdls/resolve` is the on-chain name server. Its answer is the serving node's word, not a proof: a
dishonest node can pair a forged record with the honest digest. A client that trusts no single node
asks several and compares the key and the address. `stateDigest` is the hex of the committed
state's digest (`LedgerState.Digest`): the value a payment's pin takes, never a block hash.

## The record routes

The signature record keeps who signed each block (see
[architecture.md](architecture.md#the-signature-record)). Two routes serve it, read from the files
beside the snapshot:

- `GET /hdls/record?from=H` answers a JSON array of the rolling entries the node holds for heights
  H to H + 99. The record is kept in unit files of 100 aligned heights, so the answer reads at most
  two unit files. With no `from`, it answers the newest unit file's entries: the latest signatures.
  An entry carries the set that signed it only when the set changed since the entry before, and in
  the first entry of each unit file. So a client that checks the entries asks from the first height
  of a unit, H − (H mod 100).
- `GET /hdls/lifelong-record?from=H` answers at most 100 lifelong entries of height H and above.
  With no `from`, it answers the newest 100.
- A negative or malformed `from` is a `400`. A node without a snapshot path has no record and
  answers `[]`.

The owner chose units of 100 (2026-10-30) to bound what one request costs:

- With `from`, at most two unit files: in static mode about 3.7 MB with 4 validators and 14 MB
  with 21; in dynamic mode, where most entries carry the set, about 5.8 MB and 26 MB.
- With no `from`, the route also lists the record folder to find the newest unit: about 6,000 unit
  files at the default retention of 604,800 heights.
- Both lifelong reads scan the lifelong file from its start: with `from`, until they have 100
  entries; without `from`, the whole file. That file grows only with governance actions and
  evidence.

Once the chain is past a unit's 100 heights, its file does not change until it is pruned, so a
proxy could serve it as a static file.

## How messages travel

- **Broadcast.** `HTTPTransport.Broadcast` POSTs a message to each gossip peer, each in its own
  goroutine, with a 5-second timeout. It never blocks the event loop.
- **Relay.** A node passes on a message it took to its own peers, once (`Node.relay`). The relay
  key is the content id for payments, updates and name requests, the id of the change for a
  certificate, the signer and signature for a vote, and height, round and block hash for a
  proposal. The node forgets these keys at each commit. Relaying is not free. With `maxPeers` 0,
  each node that takes a message sends it to all its peers, so among N nodes a message travels
  N × (N − 1) times and each node receives about N − 1 copies. A copy of a vote or a certificate
  is dropped before any check, and a copy of a proposal once it is read and decoded (up to 2 MiB).
  A copy of a payment, an update or a name request gets its full check, signatures included, off
  the loop, before the loop finds it pooled: for a full block of payments, about 0.13 s of CPU on
  each node with 4 validators and 0.85 s with 21 (spec §9.4).
- **Peer DNS and fan-out.** A validator can publish its gossip address on-chain (`Address`, set in
  the genesis or by an account update). A node starts on its configured `peers` (see
  [operating-a-network.md](operating-a-network.md#the-node-config)). Once every current validator
  has an address, a validator takes its peers from the committed state instead, as it opens and
  after each commit or catch-up (`Node.resolvePeersFromState`): every other validator with
  `maxPeers` 0, or with `maxPeers` above 0 its neighbours in a Hypernode topology over the
  validators' names (`node.Neighbors`). So when the genesis gives every validator an address, a
  validator gossips to them from the start. Relaying carries a message on to every validator. A
  validator goes back to its configured peers whenever an address is missing.
- **Nodes that are not validators.** A node whose name is not a validator, and the only validator
  of a network, keep their configured `peers`. Once validators take their peers from the chain,
  they push nothing to a node that is not a validator: it follows the chain by checkpoint
  catch-up.
- **Catch-up.** A node that falls behind asks its bootstrap peers, then, if none of them gives a
  checkpoint that verifies, its gossip peers. It asks each peer's `/hdls/status` first, and fetches
  `/hdls/checkpoint` only from a peer whose height is ahead (`Node.bestCheckpoint`). It tries every
  3 s, or every 5 block intervals if that is longer.

## A payment, as a direct API client builds it

Every message's JSON is its Go struct in package `ledger` (a `Payment`, an `AccountUpdate`, a
`NameRequest`, a `ParameterChange`, a `Reassignment`): the field names are the keys, amounts are
plain decimal strings, and spec §4 lists each message's fields and which of them are signed.

```go
signer, _ := signing.NewSigner(signing.MLDSA65{}, networkID, seed) // networkID from /hdls/status
payment := ledger.Payment{From: "alice", To: "bakery", Amount: big.NewRat(10, 1), BlockID: height}
payment, _ = ledger.SignPayment(signer, payment)
// POST json.Marshal(payment) to http://<node>/hdls/pay
```

`height` is `heightInProgress` from `/hdls/status`, or up to 64 blocks past it. The wallet
(`ui/`) does this for its users, but takes the height from the state it verified
(`State.Height + 1`), never from `/hdls/status` (see
[the payment counter](../ui/README.md#the-payment-counter)). Two payments with identical signed
content (payer, payee, amount, comment, block and pin) are one payment. A client that means two
must vary the comment or the block. The wallet gives every payment a block of its own.
