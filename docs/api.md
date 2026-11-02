# The HTTP API

Area: **API/CDN**: the engine's two ports, and the public face in front of them, which the fetcher
serves. Code: `node/httptransport.go` (`Server`, whose `Handler` serves the operator port and
`GossipHandler` the gossip port, with the record routes `handleRecord` and `handleLifelongRecord`,
the bundle and checkpoint routes, and `HTTPTransport`), `node/allowlist.go`, `node/relay.go`,
`node/peerdns.go`, `node/topology.go`, the fetches in `node/sync.go`, `cmd/server` and
`cmd/fetcher`.

This doc lists the two ports and the public face, every route, what it answers and what it costs a
node, then how messages travel between nodes. Running the engine and the fetcher is in
[operating-a-network.md](operating-a-network.md). The files the engine writes for the public are in
[architecture.md](architecture.md#the-public-folder), and the bundle's format in
[architecture.md](architecture.md#bundles).

## Two ports and the public face

A bank's engine (`cmd/server`) serves two ports:

- **The gossip port**, `gossipListen` in its config, usually `":8070"`: the network's `gossipPort`.
  The other validators reach it. It serves only the routes validators use, and only to the IP
  addresses on its allow list (below). It answers `404` to any other route.
- **The operator port**, `listen` in its config. It serves every route. Only the bank's own
  machines reach it: its fetcher, its operator's wallet and its operator's commands. It is never
  published. Beside a gossip port, `listen` is `127.0.0.1:8080` unless the config says otherwise.

In front of them is the **public face**: the fetcher (`cmd/fetcher`), behind a CDN whose name is
the bank's public address. It serves the files the engine writes for the public, and passes three
kinds of customer message on to the operator port. Anyone can reach it: customers' wallets,
joining nodes, and the other banks when their gossip is cut.

A config without `gossipListen` serves every route on `listen`, to anyone, with no allow list. The
tests, `make demo` and a trial on one machine run this way, and the server's start log says that
the gossip routes are open to anyone.

| Route | Gossip port | Operator port | Public face |
|---|---|---|---|
| `POST /hdls/proposal`, `/hdls/vote` | yes | yes | no |
| `POST /hdls/payment` (a relayed payment) | yes | yes | no |
| `POST /hdls/update`, `/hdls/name-request` | yes | yes | passed on to the operator port |
| `POST /hdls/parameter-vote`, `/hdls/reassignment-vote` | yes | yes | no |
| `GET /hdls/status`, `/hdls/checkpoint` | yes, for catch-up between validators | yes | a file |
| `POST /hdls/pay` (a customer's payment, with a receipt) | no | yes | passed on to the operator port |
| `GET /hdls/accounts`, `/hdls/governance` | no | yes | a file |
| `GET /hdls/resolve` | no | yes | a file per name and per key |
| `GET /hdls/record`, `/hdls/lifelong-record` | no | yes, with `?from=` | the record files |
| `GET /hdls/bundle` | no | yes: the full bundle | a file: the public bundle |
| `POST /hdls/bundle`, `POST /hdls/checkpoint` | no | yes | no |

Four routes do two jobs: an update, a name request and the two certificates. The port tells them
apart, not the name. On the gossip port they are gossip from other validators. On the operator
port they are the bank's own submissions, and its customers' ones that the fetcher passes on.

**The allow list.** The gossip port lets a connection in only from an IP address on the list:

- every IP address of every current validator (the set of the height in progress, as the last
  committed state seats it), but for two kinds: the addresses the node's own account publishes,
  even where another validator publishes them too, and loopback addresses;
- the entries of `allow` in the node's config: IP addresses or prefixes, such as `"203.0.113.7"`,
  `"198.51.100.0/24"` or `"2001:db8::/48"`. They are kept as they are, a loopback one too.

The node computes the list as it opens, after each commit and after each catch-up, and swaps it in
whole. It looks up no name: the chain is the routing table. A validator that publishes an address
is let in at once, without waiting for the others. An IPv4 address in IPv6 dress
(`::ffff:a.b.c.d`) counts as the IPv4 address.

The list is enforced in the engine's own process, in two places:

- **At accept.** A connection from an address not on the list is closed before a byte of it is
  read, so no HTTP is parsed for a stranger. So is a connection whose two ends are one address,
  which no other engine opens: there is one engine per IP address (spec §13, Q13).
- **After each read.** HTTP keeps a connection open from one request to the next, so the address
  is checked again after each read. Once it has left the list (its validator left the set, or
  published other addresses), the connection is closed, and what was read is dropped unparsed.

Refused connections are counted, and the count goes in one log line a minute while it is above
zero. How a bank chooses the addresses it publishes is in
[operating-a-network.md](operating-a-network.md#network-exposure). What the list does and does not
protect is in [security.md](security.md#the-two-ports-and-the-public-face).

**The caller's own address.** The list trusts the address a connection comes from, as the engine
sees it. So the gossip port must see each caller's real address: a port published by rootful
Docker or Podman, whose address translation (DNAT) keeps it, host networking, or rootless Podman
with the `slirp4netns` port handler, not its default one. A forwarder or a TLS or TCP proxy in
front makes every caller one address, and the allow list can't work behind it:

- When that address is the engine's own, a loopback address, or, with rootless Podman's default
  port forwarding, the container's own address, `10.0.2.100`, every caller is refused, even where a
  validator publishes that address. The list leaves out the first two, and the engine refuses a
  connection whose two ends are one address, which a forwarder inside the engine's own network
  namespace opens. The log's line of refusals shows it.
- Docker's userland proxy, alone, with iptables turned off, runs outside that namespace and makes
  every caller the bridge's gateway, such as `172.17.0.1`. The engine does not catch that: were the
  gateway on the list, by `allow` or because a validator publishes it, everyone would get in. So
  with Docker, publish the gossip port the default way, through iptables, and never list a bridge's
  gateway in `allow`.

Two cases need an `allow` entry:

- A bank that another bank reaches from the bank's own address, as two banks behind one NAT do,
  lists that address in `allow`.
- A trial on loopback addresses, such as the tests' `127.0.0.1` to `127.0.0.4`, lists them in
  `allow`.

**Limits on both ports.** There is no TLS. `cmd/server` sets per-connection deadlines: 5 s to read
the headers, 15 s to read the request, 30 s to write the answer, 2 minutes idle. Every POST body is
read up to 1 MiB, except a proposal's, up to 2 MiB, and a bundle's or a carried checkpoint's, up to
64 MiB. A body that does not decode, or is over its limit, gets `400`. A route called with another
method gets `405`, with an `Allow` header naming the methods it takes: `POST`, or `GET, HEAD` (a GET
route also answers HEAD).

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
| `/hdls/status` | `heightInProgress` (one above the committed height), `accounts`, and `networkId`, the effective network id a wallet pins; wallets ask every bank's public address for it, to check their own bank's height | off the loop, from the committed view |
| `/hdls/accounts` | every account (`Name`, `PublicKey`, `Amount`, `Bank`, `Delegate`, `Description`, `PublicAddress`, `IPAddresses`, `Frozen`, `PendingKey`, `PendingBank`, `PendingHeight`) | an event on the loop; the answer is about 2.9 KB per account |
| `/hdls/checkpoint` | the committed state, the last block's header, its commit and the set that signed it (see [consensus.md](consensus.md#onboarding-and-the-checkpoint)) | as `/hdls/accounts`, plus the commit and the set |
| `/hdls/governance` | the ten parameters, the governance sequence, the validators by name, total power, quorum, and the pooled certificates with their voters' power | an event on the loop |
| `/hdls/resolve?name=N` or `?key=K` | always the account's `name` (the answer to `?key=`), `publicKey`, `bank`, `frozen`, `pendingKey`, `pendingBank`, `pendingHeight`, the committed `height` and `stateDigest`, and, only when set, `publicAddress`, `ipAddresses` and `description`; `400` unless exactly one of `name` and `key` is given; `404` if no account matches | off the loop, from the committed view |
| `/hdls/record` | rolling record entries (below) | off the loop: at most two unit files |
| `/hdls/lifelong-record` | lifelong record entries (below) | off the loop: a scan of the lifelong file |

`/hdls/status` gives the height in progress as `heightInProgress`; `height` in `/hdls/resolve` is the
committed height.

`/hdls/resolve` is the on-chain name server. Its answer is the serving node's word, not a proof: a
dishonest node can pair a forged record with the honest digest. A client that trusts no single node
asks several and compares the key and the addresses. `stateDigest` is the hex of the committed
state's digest (`LedgerState.Digest`): the value a payment's pin takes, never a block hash. What a
name publishes, and who signs it, is in [governance.md](governance.md#names-as-a-name-service).

## Bundles and carried checkpoints (operator port)

A bundle carries signed messages where the network can't: a text file, one JSON object per line,
that any carrier can move. Its format is in [architecture.md](architecture.md#bundles), and how
banks carry them in [operating-a-network.md](operating-a-network.md#when-the-network-splits). Only
the operator port serves these three routes.

| Route | Body | Answer | Cost to the node |
|---|---|---|---|
| `GET /hdls/bundle` | | a bundle of everything the node would gossip now, at most 64 MiB, as `application/x-ndjson` | an event on the loop, which marshals every message it holds |
| `POST /hdls/bundle` | a bundle, at most 64 MiB | `200` with the counts `{"queued":…,"taken":…,"refused":…,"unreadable":…}`; `409` with the reason when its first line is not a bundle header, or names another network; `400` when it is larger than 64 MiB or its upload was cut short | each line goes through the intake of its gossip route, with the same checks and signatures as gossip |
| `POST /hdls/checkpoint` | a checkpoint, as `/hdls/checkpoint` answers it, at most 64 MiB | `202` once the node has adopted it; `409` with the reason when it has not: not ahead, does not verify (another network's checkpoint, or a damaged one), or the node is still starting | an event on the loop, which verifies it as catch-up verifies a checkpoint (see [consensus.md](consensus.md#onboarding-and-the-checkpoint)): signature checks for the names of the node's last committed set, then for the commit |

What the import's counts mean:

- **queued**: proposals and votes. They go to the event loop with no verdict, as on their routes.
- **taken**: payments, updates, name requests and certificates that the node newly pooled.
- **refused**: messages their intake refused.
- **unreadable**: lines that are no message: not JSON, of an unknown type, or with a body that does
  not decode or is larger than its route takes.

A message the node already holds is in none of the counts: it costs nothing new, as a second
gossip copy does. What the node takes from a bundle it relays to the peers it can still reach, as it
relays gossip.

## The public face

The fetcher serves a fixed list of paths, never a directory. It holds no key and keeps no state.
Each file is the engine's, read-only, and the fetcher adds only a `Cache-Control` header, which says
how long a CDN may keep it.

| Path | From | Methods | `Cache-Control` |
|---|---|---|---|
| `/hdls/status` | the public folder (`-folder`) | GET, HEAD | `max-age=1`: the wallet's "behind" check allows 10 blocks at about 1 s each |
| `/hdls/checkpoint` | the public folder | GET, HEAD | `max-age=1`: a wallet names a payment's block from it, and a copy a few blocks old names one that has passed. It carries its ⅔ commit, so a cache can serve an old one, never a forged one |
| `/hdls/accounts`, `/hdls/governance` | the public folder | GET, HEAD | `max-age=5`: they change at most once a block |
| `/hdls/resolve/name/<name>`, `/hdls/resolve/key/<64 hex digits>` | the public folder | GET, HEAD | `max-age=5` |
| `/hdls/bundle` | the public folder | GET, HEAD | `max-age=1`: the second path needs it fresh |
| `/hdls/record/<first height, 20 digits>.jsonl` | the engine's record folder (`-record`) | GET, HEAD | `max-age=31536000, immutable` once the unit is complete, when its first height + 100 ≤ the height in progress; `max-age=5` until then |
| `/hdls/lifelong-record.jsonl` | the engine's lifelong record file (`-lifelong`) | GET, HEAD | `max-age=5`: it is still growing |
| `/hdls/pay`, `/hdls/update`, `/hdls/name-request` | passed on to the engine's operator port (`-engine`) | POST | `no-store` |

- **Anything else is refused.** Another path gets `404`, and another method on a listed path
  `405`. These get `404` too: a path not in its one clean form (with a `.` or `..` segment, or a
  doubled or trailing slash), a name outside the name grammar, a key that is not 64 lowercase hex
  digits, a directory, and the engine's temporary files in `.incoming`.
- **A missing file** at a listed path gets `404` with `max-age=1`. The file can appear at any
  commit: a new name, the next record unit. Without the header, a CDN would keep the `404` for its
  own default, minutes on some. The other `404`s, for paths no file can ever be at, carry no
  `Cache-Control`.
- **Files** go out with Go's `http.ServeContent`, which also answers HEAD and ranges. Each file
  carries an `ETag`: its size and its modification time to the nanosecond, in hex. A cache
  revalidates a file by its `ETag` alone (`If-None-Match`). The fetcher sends no `Last-Modified`,
  which counts whole seconds: by it, a file rewritten within the second a cache fetched it would
  pass as unchanged. The type is `application/json`, and `application/x-ndjson` (one JSON value per
  line) for the record files and the bundle. The status file, under 200 bytes, tells the fetcher the
  height in progress, and so whether a record unit is complete.
- **Submissions** are read whole first, at most 1 MiB, and then passed on. The engine's answer
  comes back unchanged: a receipt, `202` or `409`. A body over 1 MiB gets `413` from the fetcher
  (the engine would answer `400`), and `502` means the engine did not answer.
- **The cap.** At most `-max-submissions-per-second` submissions go on to the engine each second
  (100 by default, each bank's own setting). Above it the answer is `429` with `Retry-After: 1`. The
  count starts again at each second of the clock, so up to twice the cap can pass around a second's
  boundary. The cap only keeps a flood off the engine: limits per client are the CDN's job.

**The record at the edge.** On the operator port `GET /hdls/record?from=H` answers a JSON array.
The fetcher serves each unit file as it is on disk instead: one entry per line, named by its first
height in 20 digits. The unit of height H starts at H − (H mod 100), so height 1234 is in
`/hdls/record/00000000000000001200.jsonl`.

**Names at the edge.** `/hdls/resolve/name/<name>` holds the fields `/hdls/resolve` answers, but
not the committed `height` and `stateDigest`: those change at every block, and a reader takes them
from the status file or the checkpoint. It holds nothing else, so every bank of one chain serves
the same bytes for a name. `/hdls/resolve/key/<…>` holds the same record, found by key: the file's
name is the SHA-256 of the key's bytes (the base64 key decoded), in lowercase hex.

**What a cache can and cannot do.** The checkpoint verifies by its ⅔ signatures, so a cache or a
CDN can hand out an old one, but never a false one. The other files are the bank's word, as the
routes are: they are not signed as files. A reader that trusts no single bank compares several
banks' files, or checks a record against a checkpoint. Every message in a bundle is signed on its
own.

## The record routes

The signature record keeps who signed each block (see
[architecture.md](architecture.md#the-signature-record)). Two routes serve it on the operator port,
read from the files beside the snapshot:

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

Once the chain is past a unit's 100 heights, its file does not change until it is pruned, so the
fetcher serves it as a static file that a CDN may keep for good.

## How messages travel

- **Broadcast.** `HTTPTransport.Broadcast` POSTs a message to each gossip peer, each in its own
  goroutine, with a 5-second timeout. It never blocks the event loop. A peer may have several URLs,
  one per IP address it publishes. The transport tries them in order, and moves to the next only
  when the connection fails: refused, the address unreachable, or not made within 2 s. So a bank
  with a second line to the internet stays reachable. The wait is 2 s, not 1: Linux sends a
  connection's first packet again after a second, and one lost packet must not fail the push. Once
  a request is sent, its outcome ends the push, whatever it is: an answer of any status, or no
  answer within the 5 seconds. The peer may have taken the message, and a slow peer must not get it
  once per URL.
- **A failing peer.** A push that fails is lost, as gossip allows. The transport logs one line when
  a peer's pushes start failing, with the error, and one when it answers again, never one line per
  message:
  `gossip: pushes to the peer at <its URLs> fail: <error>`, and
  `gossip: the peer at <its URLs> answers again`.
  It logs at most one line a minute for each peer, so a peer that flaps costs one line a minute.
  The line that a peer answers again comes with the first push that works a minute or more after
  the line before. On an idle network, which pushes little, it can come minutes late.
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
- **Peer DNS and fan-out.** A validator publishes up to 4 IP addresses on chain (`IPAddresses`,
  set in the genesis or by an account update, each in its one plain form;
  `ledger.CanonicalIPAddress`), and the network has one gossip port, `gossipPort` in the genesis. A
  node starts on its configured `peers` (see
  [operating-a-network.md](operating-a-network.md#the-node-config)). Once every current validator
  has an IP address, a validator takes its peers from the committed state instead, as it opens and
  after each commit or catch-up (`Node.resolvePeersFromState`): every other validator with
  `maxPeers` 0, or with `maxPeers` above 0 its neighbours in a Hypernode topology over the
  validators' names (`node.Neighbors`). Each peer's URLs are `http://<address>:<gossipPort>`, one
  for each of its addresses, in the order it lists them, with an IPv6 address in brackets. So when
  the genesis gives every validator an IP address, a validator gossips to them from the start.
  Relaying carries a message on to every validator. A validator goes back to its configured peers
  whenever an address is missing.
- **Nodes that are not validators.** A node whose name is not a validator, and the only validator
  of a network, keep their configured `peers`. Once validators take their peers from the chain,
  they push nothing to a node that is not a validator, and such a node is not on their allow lists
  either: it follows the chain by checkpoint catch-up from the banks' public addresses.
- **Catch-up.** A running node that falls behind asks three kinds of peer, in this order, and stops
  at the first kind that gives a checkpoint that verifies:
  1. its bootstrap peers;
  2. its gossip peers;
  3. the public addresses of the other current validators. It asks every one of them when no
     bootstrap or gossip peer answered at all, as when gossip is blocked or the others' allow
     lists no longer let the node in, and while the node is stuck on its height (the second path,
     below). Otherwise it asks one of them a tick, each in turn: one small request a tick. So a
     node whose peers lag with it, as on its side of a split, still finds within about as many
     ticks as there are validators that other banks are ahead. An idle node runs no round, so it is
     never stuck, and would otherwise not find out before its heartbeat, minutes later.

  It asks each peer at the first of its URLs that answers, and at no other. It asks each peer's
  `/hdls/status` first, and fetches `/hdls/checkpoint` only from a peer whose height is ahead
  (`Node.bestCheckpoint`). A connection gives up after 2 s, and a request after 5 s. It tries every
  3 s, or every 5 block intervals if that is longer. At start, before its first height, it asks its
  bootstrap peers only. The public face serves both files at the same paths, so a bootstrap peer
  can be a bank's public address.
- **The second path.** A validator whose height has run for 4 step timeouts without a commit
  fetches the public bundle, `<public address>/hdls/bundle`, of every other current validator that
  publishes a public address, once each re-gossip interval, and imports each, until it commits. The
  pull checks each vote and each proposal off the event loop before it hands it on. A vote or a
  proposal must be of the height in progress, or of the last committed height, and the set of that
  height (the set that signed the last commit, for the last height) must vouch for it: a vote is by
  a validator of the set, a proposal names the proposer the set schedules for its round, and the
  signature verifies under the key the set holds. A vote that verified is not checked again at that
  committed height. The import stops at the first line that fails that check, is unreadable or of
  an unknown type, or that its intake refuses. The only such line an honest public bundle can hold
  is a certificate the node refuses; certificates come last, so the import loses only what follows
  it. The pull goes out, so the allow list plays no part in it. See
  [operating-a-network.md](operating-a-network.md#the-second-path-public-bundles).
- **Fetches from a public address**, the pull's and catch-up's, refuse to connect to the node's own
  machine: to a loopback address, or to `0.0.0.0` or `::`, which reach it too, checked on the
  address the connection goes to, once a name is resolved; and to any of its other addresses, a
  connection whose two ends are one address, dropped before a request is sent. No proxy is used. A
  public address is on chain, so each validator chooses its own: one that published
  `http://127.0.0.1:8080`, or an address every bank has, such as rootless Podman's `10.0.2.100`,
  would otherwise make every stuck bank fetch its own operator port's export. Another private
  address works, as the quickstart's do. Two engines on one machine, sharing its address, can't use
  each other's public faces through that address.

## A payment, as a direct API client builds it

Every message's JSON is its Go struct in package `ledger` (a `Payment`, an `AccountUpdate`, a
`NameRequest`, a `ParameterChange`, a `Reassignment`): the field names are the keys, amounts are
plain decimal strings, and spec §4 lists each message's fields and which of them are signed.

```go
signer, _ := signing.NewSigner(signing.NetworkScheme(), networkID, seed) // networkID from /hdls/status
payment := ledger.Payment{From: "alice", To: "bakery", Amount: big.NewRat(10, 1), BlockID: height}
payment, _ = ledger.SignPayment(signer, payment)
// POST json.Marshal(payment) to <a bank's public address>/hdls/pay
```

`height` is `heightInProgress` from `/hdls/status`, or up to 64 blocks past it. The wallet
(`ui/`) does this for its users, but takes the height from the state it verified
(`State.Height + 1`), never from `/hdls/status` (see
[the payment counter](../ui/README.md#the-payment-counter)). Two payments with identical signed
content (payer, payee, amount, comment, block and pin) are one payment. A client that means two
must vary the comment or the block. The wallet gives every payment a block of its own.
