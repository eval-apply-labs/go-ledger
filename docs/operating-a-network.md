# Operating a network

How to build the binaries, generate keys, write node configs, and run a validator network.

## Build

Requires Go 1.22+.

```
go build -o bin/server ./cmd/server
go build -o bin/keygen ./cmd/keygen
go test ./...            # run the suite (add -race for the node tests)
```

The result is a single static binary per command — no runtime, no JVM.

## Generate keys

Every validator (and every account holder) has an Ed25519 key pair. Generate one **on the machine
that will hold it**; the private key never needs to leave that machine.

```
$ ./bin/keygen
publicKey:  9Xr2...base64...=
privateKey: k7f0...base64...=
```

The public key is the identity (the "account name"); the private key is the secret. Keep it safe.

## Node config

Each node reads a JSON config. Fields:

| field | meaning |
|---|---|
| `networkId` | a string bound into every signature; nodes must share it (isolates testnet/mainnet/forks) |
| `listen` | address to serve the node HTTP API on, e.g. `"127.0.0.1:8080"` |
| `publicKey`, `privateKey` | this node's key pair (base64, from `keygen`) |
| `transactionCost` | fee burned per payment, exact decimal string, e.g. `"0.1"`. Must be **zero or positive** — zero means no spam-deterrent fee at all; a negative value is refused at load (it would mint on every payment) |
| `timeoutMs` | base consensus step timeout (grows per round); e.g. `3000` |
| `minBlockIntervalMs` | minimum wait between blocks, so an idle chain doesn't spin; e.g. `1000` |
| `maxPeers` | 0 = all-to-all gossip (default). >0 enables the auto-scaled Hypernode topology (each node gossips to ~this many peers and relays); requires a `url` on every validator entry |
| `validatorThreshold` | empty = fixed `validators` set. A decimal makes the set DYNAMIC — derived from account stake each block, so a funded account whose stake reaches the threshold becomes a validator (with power = its stake); the `validators` list then serves as the peer universe (URLs), powers ignored |
| `validators` | the validator set: `[{ "publicKey": "...", "power": N }]` — must be identical on every node |
| `peers` | base URLs of the other nodes, e.g. `["http://10.0.0.2:8080", ...]` |
| `bootstrap` | optional peer URLs to sync the committed state from on start (join/rejoin); empty for a genesis node |
| `snapshotPath` | optional file path; if set, the node persists its committed state here and reloads it on restart |
| `backupPath` | optional second path for off-machine durability; each committed checkpoint is mirrored there asynchronously. The atomic-rename write is guaranteed on a **local filesystem only** — on NFS/CIFS a rename may not be atomic, and a failed mirror is silently ignored. Prefer a local disk (and copy off-machine separately), or verify the mount supports atomic rename |
| `maxMempool` | cap on pending payments held by a node (0 → a sane default); bounds memory against a valid-payment flood |
| `maxCommentLength` | cap on a payment's comment length in bytes (0 → a sane default); over-long payments are dropped, not gossiped |
| `maxMessageBytes` | GENESIS value of the governed cap on one serialized payment/update (0 → default 2000); amended after genesis by ⅔ governance like the rest of the profile. Bounds what one fee can write into permanent state (every account is re-hashed into every digest), so it must match across founding nodes like the other genesis parameters |
| `genesis` | starting accounts: `[{ "publicKey","name","amount","validatorNode","description","address" }]` — identical on every node. `address` is a validator's optional on-chain gossip URL: once validators publish addresses (in genesis or via an update), nodes resolve their peers from committed state (the self-describing peer DNS) instead of the static `peers` list |

`validators` and `genesis` are the shared genesis state and **must match byte-for-byte across all
nodes**, or they will compute different digests and never agree. `publicKey`/`privateKey`/`listen`/
`peers` are per-node.

### Example: a 4-node network

Generate four key pairs (one per node) and pick a funded genesis account. A node config looks like:

```json
{
  "networkId": "village",
  "listen": "0.0.0.0:8080",
  "publicKey": "<this node's public key>",
  "privateKey": "<this node's private key>",
  "transactionCost": "0.1",
  "timeoutMs": 3000,
  "minBlockIntervalMs": 1000,
  "validators": [
    { "publicKey": "<node1 pub>", "power": 1 },
    { "publicKey": "<node2 pub>", "power": 1 },
    { "publicKey": "<node3 pub>", "power": 1 },
    { "publicKey": "<node4 pub>", "power": 1 }
  ],
  "peers": [
    "http://10.0.0.2:8080",
    "http://10.0.0.3:8080",
    "http://10.0.0.4:8080"
  ],
  "genesis": [
    { "publicKey": "<founder pub>", "name": "founder", "amount": "1000", "validatorNode": "<founder pub>" }
  ]
}
```

Each node's config differs only in `publicKey`/`privateKey`/`listen`/`peers` (list the *other three*
nodes). Start every node:

```
./bin/server -config node1.json
```

With > ⅔ of the four online (i.e. at least 3), the network finalises a block every
`minBlockIntervalMs`. A single node alone will not progress (1 of 4 < quorum 3) — but a
one-validator network (`power` totalling a set whose quorum is 1) will, which is handy for testing.

### Network exposure

**The node API has no TLS and no authentication** — every mutating endpoint (`/hdls/pay`,
`/hdls/update`, the governance votes, the peer intake) is open to anyone who can reach the port.
The example above listens on `0.0.0.0` because validators must reach each other across the LAN;
that same choice exposes the API to everything else on that LAN. Keep the network on an isolated /
trusted LAN or a VPN (e.g. WireGuard) between validators, and **never port-forward `listen` to the
public internet** — if remote access is unavoidable, front it with a TLS-terminating reverse proxy
plus an allowlist. A sustained flood of valid-looking requests can also stall the node's
single-threaded event loop, so put a rate-limiting proxy in front of any exposure beyond the
trusted LAN. See [security.md](security.md) for the full threat model.

### Rejoining after a restart

State is held in memory, so a node that restarts comes up at genesis. Set `bootstrap` to one or
more peer URLs and it will, on start, fetch a peer's current committed state and the `Commit`
certificate that finalised it, verify the certificate against the validator set, and fast-forward
to the current height — no manual intervention. Because the state is certificate-verified, the
bootstrap peer does not have to be trusted. A node whose key is in the validator set then resumes
validating; a node that is only observing can bootstrap the same way to follow the ledger.

Set `snapshotPath` and the node also **persists its committed state to disk after every block** and
reloads it on restart. That gives local recovery without needing a peer, and — importantly for a
co-located community — lets the network come back after a *simultaneous* outage: every node
restarts from its own snapshot and resumes. Use both together: `snapshotPath` for local durability
and `bootstrap` to catch up anything committed while a node was down. Note the snapshot write is
**best-effort** (a failed write is not surfaced) and per-node — a lost disk means re-syncing from
peers. For durability of the *ledger itself*, also keep a periodic `GET /hdls/accounts` (or
`/hdls/checkpoint`) export somewhere off-machine, as [security.md](security.md) recommends.

### Liveness note

Finalisation needs more than ⅔ of total stake online and agreeing. If you assign one unit of power
to each of many flaky machines, losing a third of them stalls the chain. On unreliable hardware,
put enough `power` on the machines you can keep awake that they alone exceed ⅔, and disable sleep on
them (`logind.conf: HandleLidSwitch=ignore`). See [security.md](security.md).

### Migrating the ledger (and the signature scheme)

The ledger is **historyless and portable**: at any committed height, the whole state *is* the
current accounts + parameters, with no chain to replay. So a community can lift the agreed state and
restart it as a fresh genesis — the mechanism behind "export and restart on a compatible chain." The
same move is how you'd change a network-wide invariant that is otherwise fixed at genesis, most
notably the **signature scheme** (e.g. migrating to the post-quantum ML-DSA scheme in
`signing/mldsa.go` if Ed25519 were ever judged at risk). It is a planned, coordinated outage, not a
hard fork:

1. **Agree a snapshot height** with the validators (announce it, so everyone stops at the same block).
2. **Capture the committed state** at that height — `GET /hdls/accounts` on any node gives the full
   account set, and `GET /hdls/checkpoint` gives the committed state plus the `Commit` certificate
   that finalises it, so the snapshot is verifiable, not just asserted.
3. **Everyone re-keys** under the new scheme (`keygen` with the new `signAlgo`) and publishes their
   new public key, so balances/validators can be mapped from old keys to new in the reseed genesis.
4. **Write the new `genesis`** from the captured state (same balances, names, delegations) and start a
   fresh network with the new scheme selected on every node.

Budget a short downtime — on the order of a couple of hours to stop cleanly, agree the snapshot,
re-key, and reseed — against a currency that is otherwise never forced to migrate. Because the money
supply and every balance carry over exactly, this is a currency *continuation*, not a reset; only the
cryptography (and the genesis hash) change. It is a genuine governance decision the community owns.

**Upgrading the software is the same kind of move whenever a release changes the state digest or a
signing preimage.** The committed state digest and the payment/update signatures are computed over a
fixed set of length-prefixed fields (see `ledger/parameters.go` `fields()` and `ledger/payment.go`
`signFields()`). Adding, removing, or reordering a field changes those hashes, so a node on the new
version and a node on the old version compute *different* digests for the identical logical state and
different preimages for the identical payment — they will not agree, and in-flight signatures cross-
fail. Such a release is therefore **not a rolling upgrade**: run it exactly like a scheme migration —
agree a snapshot height, stop the network cleanly, and restart every node on the new version from the
captured state — never node-by-node against a live network. A release whose changes are purely
behavioural (no digest/preimage field change) can be rolled node-by-node as usual; when in doubt,
diff `fields()`/`signFields()` against the running version. All founding/upgrading nodes must run the
same version for the same reason they must share genesis parameters.

## HTTP API

Peer endpoints (used by nodes; POST JSON):

- `POST /hdls/proposal` — a `ProposalMsg` (a block proposal).
- `POST /hdls/vote` — a signed consensus `Vote`.
- `POST /hdls/payment` — a gossiped `Payment` relayed from another node.
- `POST /hdls/update` — a signed `AccountUpdate` (name registration / re-delegation / description); accepted from a client or a peer, verified, queued, and gossiped.
- `POST /hdls/parameter-vote` — a validator's `ParameterChange` vote(s); pooled and gossiped, and enacted once pooled votes reach the > ⅔ quorum.
- `POST /hdls/reassignment-vote` — a validator's `Reassignment` vote(s) (a ⅔-ratified balance override); pooled, gossiped, and enacted at quorum.
- `GET  /hdls/checkpoint` — the committed state + `Commit` certificate, for onboarding sync.

Client endpoints:

- `POST /hdls/pay` — submit a signed `Payment`; the node validates the signature, queues and
  gossips it, and returns a signed `Receipt`. A payment the node will not hold — one that would
  overdraw the payer's balance counting its already-queued outgoing, names a past block, or
  overflows the mempool — is refused with `409` and no receipt (see the overdraw guard in
  [security.md](security.md)).
- `GET  /hdls/status` — `{ "height": N, "accounts": M }`.
- `GET  /hdls/accounts` — the full current account set.
- `GET  /hdls/resolve?name=alice` (or `?key=<public key>` for the reverse) — the decentralized name
  server: one lookup returns the registered name's public key AND its on-chain published address
  (plus the description), straight from committed state. Each answer carries the committed
  `height`/`stateDigest` it reflects, so a client that trusts no single node queries a few
  independent nodes and compares: matching digests mean the answers come from the same ⅔-agreed
  state. `404` if nothing on the ledger matches.
- `GET  /hdls/governance` — a read-only view for an admin UI: the current parameters, the validator set / quorum, and the pending parameter and reassignment proposals with their vote tallies.

## Trying it instantly

`make demo` builds everything and brings up a self-contained network on this machine — a
single-validator node with funded, named accounts (founder/bakery/cafe) plus the wallet + admin UI
pointed at it — and prints the URL. It's the quickest way to see a payment, a name registration, and a
governance vote (fee change or reassignment) go end-to-end. Ctrl-C stops it.

## Reference node, cheques and receipts

A wallet talks to **one node — its "reference node", like your branch of a bank**. It builds and
signs a `Payment` and POSTs it to that node's `/hdls/pay`; the node gossips it to the rest of the
network so it reaches whichever node proposes the block it names. A user can point their wallet at a
different reference node at any time — every node shares the same ledger, so it makes no difference
which one you use. This keeps the mental model simple and hides the consensus/gossip from the user.

Each payment names a **future block** (`BlockID`): it is included only in the block whose height
equals its `BlockID` and is stale afterward. That both lets you write a "cheque" for a later block
and makes replay impossible — a resubmitted, already-cleared payment names a past block and is never
re-applied. The wallet picks a small look-ahead (a few blocks); if payments frequently miss their
block (network too fast relative to gossip), raise the wallet's `-lookahead` or the nodes'
`minBlockIntervalMs`.

**Optional fork-binding tier — run your own node.** A payment may also carry a `PrevStateDigest`
(hex). If set, it is included only in a block whose prior state matches — pinning the payment to one
chain history so it cannot be replayed on a fork whose block at the same height had a different
predecessor. Its value depends on trusting the node that reports the live tip, so in practice it is
applied by the payer's **own node**: if you hold significant value, run a node and you get this extra
assurance directly (and all the other guarantees a validator gives you). Ordinary users on someone
else's reference node use `BlockID` alone, which already prevents ordinary replay — pinning to a tip
an untrusted node told you buys nothing, so this tier and self-hosting go together.

On `/hdls/pay` the node returns a signed **receipt** (`ledger.Receipt`): proof that *this* node
validated and queued the payment. A payer can hand it to a merchant, who verifies the node's
signature and that the node is a known validator — a provisional confirmation, like a card
authorisation, before the block finalises. For certainty a merchant waits for the block's commit.

## Submitting a payment (shape)

A payment is signed over its canonical fields in the `payment` domain, bound to the network. In Go:

```go
p := ledger.Payment{PublicKeyFrom: fromPub, PublicKeyTo: toPub, Amount: big.NewRat(10, 1), BlockID: h}
p, _ = ledger.SignPayment(signer, p)  // signer holds the payer's key, network = networkId
// POST json.Marshal(p) to http://<node>/hdls/pay
```

The wallet UI (separate repo) wraps this so end users never touch JSON or keys directly.

Two payments with **identical signed content** (same payer, recipient, amount, comment and
`BlockID`) collapse to one — that is the malleability defence, deduplicating by content id. A
client that genuinely means two such payments must vary a field: a different `Comment`, or a
different `BlockID` (the wallet UI already gives every payment a distinct `BlockID`, so this
concerns direct API clients only).
