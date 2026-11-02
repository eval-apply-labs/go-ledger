# Phase 3 implementation spec: the deployment split and pigeon mode

> **Status:** written on 2026-11-01 on branch `phase3`, before any build. It was revised the same
> day with the owner's answers to §13. The owner's decisions bind (§0.2), and §0.3 says how this
> spec reads them. Every question in §13 is decided ("q1 in process, q6 A, q13 plain ips, go
> ahead"), and the build follows §11.
>
> **Built** on 2026-11-01, on branch `phase3`: slices P1 to P7, `17d4b8d` to `b9defd2`, then the
> docs (P8). The independent review of the whole build follows, then its fixes, a recheck and the
> check list (§11, P8). Where the build departed from this spec, or settled what it left open, the
> section says so in a dated note marked **(as built)**. The progress record is
> `review/phase3_progress.md`.
>
> **Reviewed** on 2026-11-01: three independent reviews of `b9defd2` found no blocker, 3 major
> findings, 15 minor and 13 nits. Their fixes are on `phase3`, `c0e5226` to `b721caa`. The
> reviewers' recheck found the fixes holding, but for parts of N1, N7 and B4, and nine new findings
> (N14, N15, R2-1 to R2-4, R3-1 to R3-3), fixed in a last round (`620fade` to `d201a2d`, and
> `309d2df` to `ff4d853`); its final rechecks found nothing new. Where a fix changed the design, the
> section says so in a dated note marked **(as built, after the review)**, with the finding's id.
> The reports are in `review/_evidence_phase3/`.
>
> **Inputs:** the Phase 3 item of `TODO.md` and of `findings_20261027.md` ("Phase 3: the deployment
> split"); pigeon mode in `docs/managing-the-currency.md` ("It outlives the internet"); the open
> item on rate limiting and authentication (`findings.md` #10); and the owner's decisions below.
> "Today" means `main` at `c0130c9`.

## 0. How to read this spec

### 0.1 Words

- **Engine**: `cmd/server`, the node: ledger, consensus, pools, snapshot, sign log, record. Until
  the signer is split out (a later spec), it also holds the key.
- **Gossip port**: the engine's port that other validators reach. Only the allow list gets in.
- **Operator port**: the engine's port for the bank's own machines: its fetcher, its operator's
  wallet, its operator's commands. Never published.
- **Allow list**: the IP addresses that may connect to the gossip port. The engine reads it from
  the chain and its own config, and enforces it in its own process (§5).
- **Public folder**: the files the engine writes for the public (§6.1).
- **Fetcher**: `cmd/fetcher`, a new small program. It serves the public folder and the record files,
  and passes customers' submissions to the engine. It is the CDN's origin.
- **Public address, IP addresses**: what a name publishes on chain about where it can be reached
  (§4).
- **Bundle**: a text file of signed messages, one per line, that any carrier can move (§7.2).
- **Carrier**: whatever moves a bundle: a USB stick, email, radio, a person, a pigeon.
- **Slow-down switch**: a node config field that makes that node's round timers longer, never
  shorter (§7.6).
- "Must" marks a rule a test checks. "May" marks an option a builder can skip. Every new identifier
  is spelled out in full, as in the rest of the code.

### 0.2 The owner's decisions (2026-11-01)

- **D1. One public container behind a CDN, one container facing the validators.** The owner: "shall
  we build the phase 3 where we have one container showing toward the public behind a cdn and the
  other toward the validators within the allow list".
- **D2. The allow list is by IP address, enforced inside the container and fed by the engine.** The
  owner: "i lean for 3a as we have an internal routing table already, the firewall must be inside
  the docker container and being fed by the engine tho".
- **D3. The public face is static and public.** The owner: "the cdn is public as there is nothing to
  compute just static elements and the bandwith that can be managed in the usual way".
- **D4. Pigeon mode gets a carrier, and a manual switch to enter it.** The owner: "this ledger's
  resilient is only as resilient as the network operator make it to be … if the internet provider
  segments it then we need to have a secondary way (the pidgeon) to load in and out the messages,
  maybe even manually", and then "yes write it with the pigeon section, manual switch".
- **D5. Every name publishes a public address and IP addresses: a name service.** The owner (Q2):
  "the address we hold now is a publicaddress and we can add an ip address to the keys, so each key
  is potentially a website effectively and if we use the [Ed25519] setup we have a decentralized
  name service that dns operators can adopt and use for payments and tls, embedded with the ability
  to change signature method if needed by moving to a new chain."
- **D6. Name lookups, the account list and governance are public files too.** The owner (Q3): "each
  of those could be its own endpoint, i think since those are cdn based ones and need no
  interaction they should live in the public container."
- The other answers are in §13.

### 0.3 Readings of the decisions (flagged)

- **R1.** "3a" in D2 is option (a) of the third question in the message before it: the allow list
  by IP address. The alternative was (b), mutual TLS with a cheap key per validator.
- **R2.** "The firewall must be inside the docker container and being fed by the engine" is read as:
  the engine's own process refuses a connection to the gossip port at accept, before it reads a
  byte, when the remote address is not on the list. This needs no extra container privileges, no
  host firewall and no tool in the image, and Go tests can check it. A kernel firewall inside the
  container (nftables, written by the engine) is the alternative (§13 Q1).
- **R3.** "Nothing to compute, just static elements" is read as: the fetcher only serves files and
  passes three customer submissions through to the engine without looking inside them. Customers
  need some way to submit, and the CDN forwards a POST to its origin uncached. Limits per client
  are the CDN's job ("the usual way"). The fetcher keeps one global cap that protects the engine.
- **R4.** "Manual switch" is read as a field in each node's config file, set by the operator, which
  takes effect at the node's next start. No route changes it while the node runs (§13 Q6).
- **R5.** The signer container (the key off the networked machine) is not in this spec. It stays
  in `TODO.md` as the next step.
- **R6.** D5's "an ip address to the keys" is read as plain IP addresses, at most 4 per name, like
  DNS A and AAAA records. Validators gossip on one port for the whole network, fixed in the
  genesis, as DNS uses port 53. With IP addresses on chain the allow list needs no DNS lookup:
  the chain is the "internal routing table" of D2 (§13 Q13).
- **R7.** D5's TLS part is a later phase. Phase 3 publishes the records. A name's payment key should
  not become its TLS key: a web server's key sits on a machine facing the internet, and a payment
  key must not. A later phase can add a TLS key record that the name's key signs, as DANE does in
  DNS (§13, notes for later).

## 1. Scope and non-goals

In scope:

1. The engine serves two ports: the gossip port, behind the allow list, and the operator port (§3).
2. Every name may publish a public address and up to 4 IP addresses, and the network has one gossip
   port. The genesis format changes, so this is a new genesis (§4).
3. The allow list: the current validators' IP addresses from the chain, plus fixed entries in the
   node config, enforced at accept on the gossip port (§5).
4. The public folder, written by the engine, and the fetcher that serves it behind a CDN: status,
   checkpoint, name lookups, accounts, governance, the bundle and the record (§6).
5. Pigeon mode: bundle export and import, checkpoint import, the slow-down switch, the public
   bundle as a second path, and the wallet's offline steps (§7, §8).
6. Container images, and a compose file that runs four banks on one machine: the four-validator
   quickstart of `TODO.md` (§9).
7. The docs (§12).

Not in scope:

- The signer container, and any hardware for the key (R5).
- TLS on the gossip port. Every message is signed and nothing on it is secret. An operator may put
  TLS in front (Q12).
- The rest of the name service: a gateway that answers DNS queries from the chain, and TLS key
  records (R7). Both go in `TODO.md`.
- An automatic slow-down. The owner chose a manual switch (D4).
- Per-client rate limits in the fetcher (R3).
- A longer payment window for payments signed offline (§7.8, Q8).
- The archive node, the snapshot write off the event loop, and the other `TODO.md` items.

## 2. The containers

| Container | Program | Listens on | Reaches | Holds |
|---|---|---|---|---|
| engine | `cmd/server` | gossip port (published); operator port (private network only) | other validators' gossip ports, at their IP addresses; bootstrap and public addresses for catch-up | the key, the snapshot, the sign log, the record, the public folder (writes) |
| fetcher | `cmd/fetcher` (new) | its HTTP port, the CDN's origin | the engine's operator port | nothing: the public folder and the record, mounted read-only |
| wallet | `ui` | `127.0.0.1` on the user's machine | a bank's public address (a customer); its own engine's operator port (the bank's operator) | the user's key file |

The single-port setup stays. A config without `gossipListen` serves every route on `listen`, with no
allow list, as today. The tests, `make demo` and a one-machine trial keep working unchanged, and
the start log says that the gossip routes are open to anyone.

## 3. The engine's two ports, and the public face

| Route | Gossip port | Operator port | Public face (fetcher) |
|---|---|---|---|
| `POST /hdls/proposal`, `/hdls/vote` | yes | yes | no |
| `POST /hdls/payment` (relayed payment) | yes | yes | no |
| `POST /hdls/update`, `/hdls/name-request` | yes | yes | passed through |
| `POST /hdls/parameter-vote`, `/hdls/reassignment-vote` | yes | yes | no |
| `GET /hdls/status`, `/hdls/checkpoint` | yes (catch-up between validators) | yes | file |
| `POST /hdls/pay` (a customer's payment, with a receipt) | no | yes | passed through |
| `GET /hdls/accounts`, `/hdls/governance` | no | yes | file |
| `GET /hdls/resolve` | no | yes | a file per name and per key (§6.1) |
| `GET /hdls/record`, `/hdls/lifelong-record` | no | yes (`?from=`) | the record files (§6.2) |
| `GET /hdls/bundle` | no | yes: the full bundle | file: the public bundle (§7.7) |
| `POST /hdls/bundle`, `POST /hdls/checkpoint` (§7) | no | yes | no |

- The operator port keeps every route of today. The gossip port serves only the first five rows,
  and answers `404` to anything else. Both must be tested.
- This settles the routes that do two jobs (update, name request, the two governance votes) by port,
  not by new names. On the gossip port they are gossip. On the operator port they are the bank's
  own submissions, and its customers' submissions passed through by the fetcher. No route is renamed.
- The handlers stay as they are. `Server` gets a second method, `GossipHandler`, which registers
  only the gossip routes. `cmd/server` serves `Handler` on `listen`, and serves `GossipHandler` on
  `gossipListen` behind the allow list.

**2026-11-01 (as built).** `Handler` registers 18 routes and `GossipHandler` 9, both through one
function (`addGossipRoutes`), so no route is listed twice. The gossip port serves
`GET /hdls/checkpoint`, so its router would answer a POST of it with `405`. It answers `404`
instead, as it does to any route it lacks (`e0f7149`).

## 4. Addresses on chain: a public address and IP addresses

Today an account has one `Address`, a URL `http(s)://host[:port]`. Validators gossip to it (the
peer DNS), and since Q25 wallets read `/hdls/status` from it. The owner (D5): it is a public
address, and each name gets IP addresses too, so that the names on chain are a name service whose
records each name signs.

- **`PublicAddress`**, today's `Address` renamed: the name's public face. For a bank, its CDN name
  (`https://bank-a.example`), which wallets and joining nodes read from. For any other name, its
  website if it has one. Same grammar as today (`ledger.CanonicalAddress`).
- **`IPAddresses`**: up to 4 IP addresses (`MaxIPAddresses`), IPv4 or IPv6. Any name may set them,
  and for anyone they are what a DNS A or AAAA record would hold. For a validator they are also
  where gossip goes and where gossip may come from:
  - the others push to `http://<first address>:<gossipPort>`, and to the next address when a push
    to one fails to connect;
  - they accept gossip connections from any of the addresses (§5).
- **`gossipPort`**: a genesis field, one port for the whole network (required; usually 8070,
  clear of the operator port's 8080 and the wallet's 8090), and part of the genesis hash. It is not
  governed: it changes only with a new genesis.

Rules, each tested:

- **Where they are set:** both fields may be set in the genesis and by an account update. An
  update's `IPAddresses` is empty to leave the list unchanged, `["-"]` (`ClearField`) to clear it,
  and otherwise replaces the whole list.
- **The address grammar** (`ledger.CanonicalIPAddress`):
  - each address is written in its one plain form, exactly what Go's `netip.Addr.String` prints for
    it, with an IPv4 address never in IPv6 dress and no zone;
  - unspecified (`0.0.0.0`, `::`) and multicast addresses are refused;
  - private and loopback addresses are taken, so that a LAN network, the compose file and the tests
    work, and the docs tell a public network to list public addresses;
  - no duplicates, at most 4.
- **Enforcement:** genesis validation and `UpdateFitsState` refuse anything else. It is a
  block-validity rule, as the address rule is today.
- **A new genesis:** `Address` (account, update, genesis key `address`) is replaced by
  `PublicAddress` (`publicAddress`) and `IPAddresses` (`ipAddresses`). The account digest and the
  update's signed fields change, so this is a new genesis, as allowed (no deployed network).
- **The peer DNS** (`resolvedPeerAddresses`) builds each current validator's gossip URLs from its
  IP addresses and `gossipPort` (`net.JoinHostPort`, so IPv6 gets its brackets). Otherwise it is
  unchanged: until every current validator has an IP address, the configured peers stay in use.
- **The transport:** `HTTPTransport` holds the URLs of each peer in order. `post` tries the next one
  when a connection to one fails, so a bank with a second line to the internet stays reachable.
- **The resolve answer** has `publicAddress` and `ipAddresses`, each only when set.
- **The wallet:** its check of the other banks (`ui/banks.go`, `statusURLAt`) reads `PublicAddress`.
  A validator without one counts as not answering, so its power counts toward "unchecked".
- **The demo:** `cmd/demo` publishes the founder's public address and its IP address.

**2026-11-01 (as built).**
- **The gossip port has no default.** The first draft said 8090, the wallet's port. The genesis
  takes no default for any of its parameters, so `gossipPort` has none either: it is required,
  from 1 to 65535. The usual value is 8070, clear of 8080 (the operator port and the fetcher) and
  8090 (the wallet) (`7a28ab4`, `9528077`).
- **The layouts.** The port is hashed into the genesis hash right after the network name. The IP
  addresses go last among an account's digest fields and an update's signed fields: their count,
  then each address (`ledger.listFields`).
- **JSON names.** `Account` and `AccountUpdate` have no JSON tags, as before. So a snapshot, a
  checkpoint and an update's body spell the fields `PublicAddress` and `IPAddresses`, while the
  genesis file and the resolve answer spell them `publicAddress` and `ipAddresses`.
- **The wallet's form** takes the IP addresses separated by spaces, and `-` alone clears them.
- **The demo** publishes `http://` and its `-listen` as the founder's public address, and the
  address's IP as its IP address, and its gossip port is the node's own port. So `-listen` must now
  be an IP address and a port: `localhost:8080` is refused.

## 5. The allow list

### 5.1 What it holds

The IP addresses that may open a connection to the gossip port:

1. Every IP address of every current validator: the validators of the committed view
   (`committedView.validators`), the set that signs the height in progress. The node's own are
   included; they do no harm.
2. The entries of `allow` in the node config: IP addresses or prefixes (`"203.0.113.7"`,
   `"198.51.100.0/24"`, `"2001:db8::/48"`). These cover a network whose validators have not all
   published an address, a bank whose address is changing (§5.4), or a monitoring host.

- **The list is the union of the two.** Unlike the peer DNS, it does not wait for every validator to
  have an address: a validator with one is allowed at once.
- **IPv4 inside IPv6:** an IPv4 address carried inside IPv6 (`::ffff:a.b.c.d`) counts as the IPv4
  address, at both ends.
- **No DNS lookup:** the node does none for the list. The chain is the routing table. DNS is one
  more thing a provider can cut or poison, so not depending on it is part of D4.

**2026-11-01 (as built, after the review)** (`c0e5226`, `e557312`; review N1). The node's own
addresses are no longer on its list, as item 1 above says they are.
- **What it leaves out.** From the chain, the list leaves out the addresses the node's own account
  publishes, even where another validator publishes them too, and loopback addresses. The `allow`
  entries are kept as they are, a loopback one too.
- **Why.** The list trusts the address the socket reports. A front that rewrites it, a userland port
  forwarder or a TLS or TCP proxy, makes every caller one address, most often the engine's own or a
  loopback one. With those on the list, everyone got in. Now every caller is refused, and the
  refusal line shows it. A validator could also publish a loopback address to put it on every other
  bank's list.
- **What a deployment needs.** The gossip port must see each caller's real address: a port that
  rootful Docker or Podman publishes, whose address translation (DNAT) keeps it, host networking, or
  rootless Podman's `slirp4netns` port handler, not its default one. TLS in front of the port (§1,
  Q12) can't be combined with the allow list.
- **Who lists an address in `allow`.** A bank that another bank reaches from the bank's own
  address, as two banks behind one NAT do, and a trial on loopback addresses: the tests list their
  `127.0.0.x` there.
- **Both ends one address** (`309d2df`; review N14). Rootless Podman's default port forwarding
  runs inside the engine's network namespace and makes every caller the container's own address,
  `10.0.2.100`, the same on every such host. One validator that published it opened that bank to
  everyone. The listener now refuses, and counts, any connection whose remote address is its local
  one (`bothEndsOneAddress`, §5.3): no other engine connects that way, since there is one engine per
  IP address (§13, Q13). So two engines that share an address, as on one machine with host
  networking, can't gossip through it. The refusal does not catch a forwarder outside the
  namespace: Docker's userland proxy, alone with iptables turned off, makes every caller the
  bridge's gateway, such as `172.17.0.1`. With Docker, the gossip port is published through
  iptables (DNAT), its default, and no `allow` entry names a bridge's gateway.

### 5.2 Who feeds it, and when

The engine is the only source. It computes the list on the event loop, at `Open`, after each
commit and after each catch-up, where `resolvePeersFromState` runs. That is cheap: at most 4
addresses per validator, and no lookup. The new list replaces the old one in one atomic swap (an
`atomic.Pointer`), which the listener reads. A validator that leaves the set leaves the list at the
next commit.

**2026-11-01 (as built).** The list is a field of the committed view
(`committedView.gossipAllowList`), which `publishView` computes with the rest of the view. So the
swap is the view's own atomic pointer, and there is no second one (`28c9c80`).

### 5.3 Where it is enforced: the firewall inside the container

A `net.Listener` wrapper (`allowListListener`) sits in front of the gossip port's `http.Server`:

- `Accept` takes the next connection and reads its remote address. If the address is not on the
  list, it closes the connection at once, before reading a byte, and accepts the next one. No TLS
  and no HTTP is parsed for a refused caller.
- Refusals are counted, and the count goes in one log line a minute while it is above zero. This is
  for an operator diagnosing §5.4, without flooding the log.
- It needs no privileges and no tool in the image, so the image can stay `scratch` (R2).

**2026-11-01 (as built), beyond the spec.** A connection the list let in is checked again after each
read (`allowListConnection`), and closed once its address has left the list. HTTP keeps a
connection open from one request to the next, and a validator that pushes all the time never opens
a new one. Without the recheck, a validator that took its address off the chain still had its
precommit counted two heights later, and the end-to-end test failed. It is about 15 lines
(`28c9c80`). Also, `cmd/server` refuses `allow` without `gossipListen`, since the entries would hold
nothing back, and refuses an entry not written as the port compares it: an IPv4 address in IPv6
dress, a zone, or a bit set past a prefix's length (`f45b7c4`).

**2026-11-01 (as built, after the review)** (`309d2df`; review N14). `Accept` also refuses, and
counts, a connection whose two ends are one address (`bothEndsOneAddress`): see §5.1's note.

### 5.4 The rule for the addresses a bank connects from

The list holds the addresses each validator publishes. So among its up to 4 addresses, a bank lists
the one the others reach it at, and every address its engine's connections come from, or the others
refuse its pushes.

- **On a host with one public address,** Docker's default address translation sends from the host's
  address, and one entry covers both.
- **Behind a cloud NAT gateway,** list the gateway's address as well.
- **With a second line to the internet,** list that line's address. Pushes then fall back to it
  (§4).
- **With IPv6 temporary addresses,** turn them off on the engine's interface, or list them.

**Changing addresses.** A change is an account update, so the others refuse the new address until
the update commits. A bank whose address changed sends that update through another bank's public
face, since the update route is one the fetcher passes through. Or it asks the others to put the new
address in their `allow` meanwhile. A bank needs a stable address; banks are meant to be visible
entities.

**Diagnosing a broken rule.** A bank that breaks it finds that its pushes fail. `HTTPTransport.post`
ignores errors today, so the transport must log one line when a peer starts failing and one when it
recovers, never one line per message (Q10).

**2026-11-01 (as built).** The transport keeps the failing peers in a `sync.Map`, keyed by each
peer's URLs (`6c99958`). Catch-up's requests to the others' gossip ports (their status and
checkpoint) leave from the host's default address too, so this rule covers them as well.

**2026-11-01 (as built, after the review)** (`8029499`; review N3, N4):
- **The fallback.** A push moves to a peer's next URL only when the connection fails: refused,
  unreachable, or not made within 2 s (`peerDialTimeout`). Once a request is sent, its outcome ends
  the push, whatever it is. Before, any error moved it on, so a slow peer got a message once per
  URL, and a line that drops packets held every push for the client's 5 seconds. The timeout was
  1 s at first, exactly when Linux sends a connection's first packet again, so one lost packet
  failed a push; it is 2 s since `479cdc0` (review N15). Catch-up's connections give up after the
  same 2 s (review N8), at start too.
- **The log line.** A failing peer's line is now
  `gossip: pushes to the peer at <its URLs> fail: <error>`, since a push can also fail on one URL's
  timeout. The transport logs at most one line a minute for each peer, so a peer that flaps costs
  one line a minute, not one per message. The `sync.Map` became a map under a mutex, which keeps
  each peer's last line and its time. The line that a peer answers again can come minutes late on
  an idle network.

### 5.5 What it does not do

- **It is not authentication of messages.** Signatures are, as today. The list is a cost filter: it
  keeps strangers off the routes that cost a node CPU and event-loop time.
- **It does not stop a flood aimed at the address itself.** The IP addresses are on chain, so they
  are public. A flood at the network level is the hosting provider's to absorb, as it is for any
  server.
- **It trusts each validator's own list.** A validator that lists a third party's address opens the
  port to that third party. That costs no more than the validator could do itself, since it
  already sends.
- **It can be fooled by someone on the path.** Only someone who can see and change traffic between
  two banks can pass as an allowed address. Signatures still protect what that person sends.

## 6. The public face

### 6.1 The public folder

With `publicPath` set in its config, the engine writes these files under it:

| File | Content | Written |
|---|---|---|
| `hdls/status` | the `/hdls/status` answer, byte for byte | after each commit and catch-up |
| `hdls/checkpoint` | the `/hdls/checkpoint` answer, byte for byte | after each commit and catch-up |
| `hdls/accounts` | the `/hdls/accounts` answer, byte for byte | after each commit and catch-up |
| `hdls/governance` | the `/hdls/governance` answer, byte for byte | after each commit and catch-up, and when a certificate is pooled |
| `hdls/resolve/name/<name>` | the name's record (below) | when a commit or catch-up changes the name; removed with the name |
| `hdls/resolve/key/<SHA-256 of the public key, hex>` | the same record, found by key | as above; the old key's file goes when the key changes |
| `hdls/bundle` | the public bundle: the consensus part of a bundle (§7.7) | at each re-gossip tick and each commit |

- **A name's record** has the fields `/hdls/resolve` answers: `name`, `publicKey`, `bank`, `frozen`,
  the pending fields, and `publicAddress`, `ipAddresses` and `description` when set. It also has
  `changedAtHeight`, the commit that last changed it.
  - It leaves out the committed `height` and `stateDigest`. Those change at every block, so every
    file would have to be rewritten at every block. A reader takes them from `hdls/status` or
    `hdls/checkpoint`.
  - Names are safe as file names: the name grammar allows only `a`–`z`, `0`–`9` and inner hyphens,
    at most 32 characters.
- **Each write is atomic:** a temporary file in `publicPath/.incoming/`, then a rename into place.
  The fetcher never serves `.incoming`.
- **The marshal runs on the event loop,** as the snapshot's does. The writes run off it, one at a
  time per file, and a newer pending write replaces an older one that has not started.
- **`hdls/status` and `hdls/checkpoint` keep today's paths and formats.** So `bootstrap` can name a
  bank's public address, and `bestCheckpoint` and the wallet read the static files unchanged.
- **The record files stay where they are,** beside the snapshot. The fetcher serves them read-only
  (§6.2), so the engine writes nothing twice.
- **Cost:** the checkpoint and the account list are each about 2.9 KB per account, marshalled at
  every commit (Q11: the checkpoint "is the recap and what makes the next step verifiable"). That
  is the snapshot's cost twice more, fine for a community and a reason to move the writes off the
  loop before a network grows large (the open `TODO.md` item).

**2026-11-01 (as built)** (`ba78807`, `1b92a19`, `b601914`):
- **`changedAtHeight`** is the height of the commit or catch-up that changed the record. At `Open`
  a record the folder already holds byte for byte keeps its height; any other gets the height the
  node opened at. The ledger never removes a name, so in practice only `Open` removes the files of
  an older state.
- **One temporary file**, `.incoming/next`, since one file is written at a time. No fsync: `Open`
  writes every file again, so a file a power cut left empty is rewritten. `Stop` waits until the
  writer is idle, so a disk that hangs holds `Stop` up.
- **The order:** `hdls/status` is written last, after the commit's record entry is on disk, so the
  fetcher never takes a growing record unit as complete.
- **Modes:** the public files are 0644, in folders of 0755. The record files stay 0600 in 0700
  folders, so the fetcher must run as the engine's numeric user (§9's compose file runs both as
  65532).
- The answers a file copies come from one function each: `jsonAnswer` (the route's exact bytes),
  `statusOf`, and `NameRecord`, which `ResolveRecord` and the file's record both embed.

**2026-11-01 (as built, after the review)** (`11e6d5e`, `a5016b5`, `d403dc3`; review B5, B6,
B7):
- **No `changedAtHeight`.** It was the height this bank first held the record at, not the commit
  that changed it: after a catch-up, or in a fresh folder, two banks of one chain published
  different files for one unchanged name. Nothing read it, and its true value needs the history. A
  record file is now the `NameRecord` alone, so every bank publishes the same bytes for a name.
- **A temporary file for each write.** Each write takes a name of its own in `.incoming`
  (`os.CreateTemp`), so two writers that share a folder by mistake can't move each other's files
  into place. `Open` clears `.incoming`, which may hold the file of a write a crash cut short.
- **Unchanged files are not written again.** `Open` leaves alone a record file that already holds
  its record byte for byte. `hdls/bundle` is queued only when its bytes differ from the last ones
  queued: an idle node wrote it at every tick, the same each time. It is still made on the loop at
  each tick (§7.7's cost).

### 6.2 The fetcher

`cmd/fetcher` is a small program (about 200 lines with its flags) with no state and no key. It
serves an explicit list of paths, never a directory:

| Path | From | Methods |
|---|---|---|
| `/hdls/status`, `/hdls/checkpoint`, `/hdls/accounts`, `/hdls/governance`, `/hdls/bundle` | the public folder | GET, HEAD |
| `/hdls/resolve/name/<name>`, `/hdls/resolve/key/<64 hex digits>` | the public folder | GET, HEAD |
| `/hdls/record/<20 digits>.jsonl` | the engine's record folder (`-record`) | GET, HEAD |
| `/hdls/lifelong-record.jsonl` | the engine's lifelong record file (`-lifelong`) | GET, HEAD |
| `/hdls/pay`, `/hdls/update`, `/hdls/name-request` | passed to the engine's operator port (`-engine`) | POST |

Rules, each tested:

- **What it serves:** any other path gets `404`, and another method `405`. That covers a name
  outside the name grammar, a key that isn't 64 lowercase hex digits, a directory, any file outside
  the list, and `.incoming` or a temporary file.
- **Files:** sent with `http.ServeContent`. The type is `application/json`, and
  `application/x-ndjson` for the record files.
- **Submissions:** forwarded with `httputil.ReverseProxy`, the body capped at 1 MiB as the node
  caps it, and the engine's answer passed back unchanged: a receipt, `202` or `409`.
- **The global cap:** at most `-max-submissions-per-second` submissions are forwarded (default 100,
  each bank's own setting: nothing else depends on it). Above it the answer is `429` with
  `Retry-After: 1`. Limits per client are the CDN's (R3).
- **What it adds:** the `Cache-Control` header of §6.3, and nothing else.
- **How it runs:** as a non-root user, with the folders mounted read-only.

The record files change form at the edge. Today `GET /hdls/record?from=H` answers a JSON array.
The fetcher serves the unit file as it is on disk, one entry per line, named by the unit's first
height. A reader finds the unit of height H at `H − (H mod 100)`. The operator port keeps the
`?from=` routes.

**2026-11-01 (as built)** (`17d4b8d`, `cc1af7f`):
- **Answers the spec left open.** A body over 1 MiB gets `413` (the engine answers `400` to the
  same), and a submission gets `502` when the engine can't be reached (`httputil.ReverseProxy`'s
  answer). A path not in its one clean form (a `.` or `..` segment, raw or escaped, a doubled or
  trailing slash) gets `404`, since the router would otherwise redirect it.
- **The bundle's type** is `application/x-ndjson`, as the record files', since a bundle is JSON
  lines.
- **Flags:** `-engine`, `-folder`, `-record` and `-lifelong` are required.
- **Size:** 293 lines with its flags, not about 200.
- **The user:** it must run as the engine's numeric user, to read the record files (§6.1).

### 6.3 What the CDN may cache

| Path | `Cache-Control` | Why |
|---|---|---|
| `hdls/status` | `max-age=1` | the wallet's "behind" check allows 10 blocks at about 1 s each |
| `hdls/checkpoint` | `max-age=5` | carries its ⅔ commit: a cache can serve an old one, never a forged one |
| `hdls/accounts`, `hdls/governance`, `hdls/resolve/…` | `max-age=5` | change at most once a block |
| `hdls/bundle` | `max-age=1` | the second path of §7.7 needs it fresh |
| a record unit whose first height + 100 ≤ the height in progress | `max-age=31536000, immutable` | complete units never change |
| any other record unit, and the lifelong file | `max-age=5` | still growing |
| the three submission routes | `no-store` | never cached |

The fetcher reads `hdls/status`, which is under 200 bytes, to decide whether a unit is complete.

**2026-11-01 (as built, after the review)** (`fd500f1`; review N2, N5, W2):
- **`hdls/checkpoint` is kept `max-age=1`**, as the status is. A wallet names a payment's block
  from it, and a copy a few blocks old names one that has passed. Two cached copies of different
  ages also raised the wallet's rollback alarm (§7.8's note).
- **A missing file at a listed path** is `404` with `max-age=1`. The file can appear at any commit,
  and a CDN would otherwise keep the `404` for its own default, minutes on some. The `404`s of paths
  no file can ever be at carry no `Cache-Control`.
- **An `ETag` validates, and nothing else.** Every file carries an `ETag` of its size and its
  modification time in nanoseconds, in hex, and no `Last-Modified`, which counts whole seconds. By
  it, a file rewritten within the second a cache fetched it was answered `304`, and a record unit
  that took its last entry in that second would be kept for a year without it.

### 6.4 The CDN

Any CDN can do the job: the public face has no secrets and nothing to compute (D3).

- **Origin and address:** the fetcher is the origin, the CDN serves `https`, and the CDN's name is
  the bank's `PublicAddress`.
- **What a cache can do:** the checkpoint verifies by its ⅔ signatures, so a cache or a CDN can
  hand out an old one but never a false one.
- **What it can't guarantee:** the other files are a bank's word, as the routes are today. They are
  not signed as files (Q4), and a reader that trusts no single bank compares several, or checks a
  record against the checkpoint. Every message in a bundle is signed on its own.

## 7. Pigeon mode

### 7.1 What a network operator can and cannot break

**What the network can't break: correctness.**
- **No rule reads the clock.** `ledger` and `consensus` never import `time`, a block carries no
  timestamp, and every deadline counts in blocks.
- **Every message is signed.** Each one proves itself, whoever carries it.
- So a provider that slows, splits or blocks the network can stop the chain, but can't fork it or
  move money. When the link comes back, the chain resumes.

**What it can break: progress.**
- **One bank cut off:** if the rest hold more than ⅔ of the power, they carry on. The cut-off bank
  and its customers wait, then catch up.
- **No side above ⅔:** every node parks at the step where it needs the other side's votes. The
  prevote and precommit timers start only after ⅔ of the votes are in (`consensus/driver.go`), so
  that wait has no timer, and votes carried across by hand would let the step complete.
- **The catch:** the propose timer always runs. A node waits `StepTimeoutMs × (round + 1)` for the
  block proposal (3 s at round 0 by default), then votes "no block". A proposal carried by hand
  arrives too late, so rounds pass without a block.

**Pigeon mode as documented today.** The ⅔ vote both timings up to a day each (`MaxTimingMs`), and
the chain runs at the carrier's pace. Two pieces are missing:
1. **A carrier.** The engine can only send and receive by HTTP.
2. **A way in once the network has split.** Changing the timings is a ⅔ vote, and the vote needs a
   block, which needs the network that is gone.

The bundle (§7.2 to §7.5) is the carrier, and the slow-down switch (§7.6) is the way in.

### 7.2 The bundle

A bundle is the envelope for messages that travel without the network. It is a text file, one
JSON object per line:

```
{"bundle":1,"networkId":"village/…","from":"bank-a","heightInProgress":1234}
{"type":"vote","body":{…}}
{"type":"proposal","body":{…}}
{"type":"payment","body":{…}}
```

- **The first line is the header.** `networkId` must match the importing node's network, or the
  import refuses the whole bundle. `from` and `heightInProgress` are for the people carrying it;
  no rule reads them.
- **Every other line is one message.** `type` is the route's last segment: `proposal`, `vote`,
  `payment`, `update`, `name-request`, `parameter-vote` or `reassignment-vote`. `body` is exactly
  the JSON that route takes.
- **The bound:** at most 64 MiB (`MaxBundleBytes`). An export that would pass it stops at the last
  whole line that fits, in the order of §7.3.
- **What it carries in practice:** a vote is about 4.7 KB, and a proposal with its block up to
  2 MiB. A height's messages fit a USB stick, an email or a radio data link, but not one QR code.
- **Why a file:** every message in it is signed, so whoever carries the file can lose it or delay
  it, but can't change a message without the change being refused.

### 7.3 Export

`GET /hdls/bundle` on the operator port answers a bundle of everything the node would gossip now:

1. **The consensus part:** what `reGossipVotes` and `reGossipProposal` send (the votes the node
   holds for the height in progress, the current round's proposal, and `sendLastCommit`'s
   precommits and decided proposal), then the pooled certificates.
2. **The pools:** name requests, updates, then payments, each in pool order.

The public bundle file (§6.1) holds only part 1. The operator saves an export with a command the
runbook gives (Q9):

```
curl -o bank-a.bundle http://engine:8081/hdls/bundle
```

**2026-11-01 (as built): the order** (`83f5aa5`). Re-gossip and the export share the code that picks
the messages. `reGossipVotes` and `reGossipProposal` became `reGossip`, which sends
`consensusMessages`, in this order:
1. what decided the last height (`lastCommitMessages`: its precommits and the decided proposal);
2. the votes held for the height in progress, in the order of their slots;
3. the current round's proposal.

The export then adds the pooled certificates (the parameter changes', then the name actions', each
in key order) and the pools (name requests, updates, payments). The last commit comes first so that
a node one height behind, taking the lines in order, decides that height and then takes the next
height's votes and proposal in its post-commit window. The export is served as
`application/x-ndjson`.

**2026-11-01 (as built, after the review)** (`dae317b`; review B3). The order is now:
1. what decided the last height (`lastCommitMessages`);
2. the votes held for the current round;
3. the current round's proposal;
4. the other votes held for the height in progress: the past rounds', newest first, then the future
   rounds', nearest first (`6d3df82`; review R2-3).

Within a round the votes keep the order of their slots, so the export stays deterministic. Before,
the 8 MiB cut of the public bundle dropped the current proposal first, then every vote of the
validators whose names sort last, the current round's included. Now, after the pooled certificates,
which come last, it drops the farthest future rounds, then the oldest past ones, and never the
current round. The future rounds come after the past ones because a rogue validator can sign votes
for up to `MaxRoundLookahead` (256) rounds ahead, about 2.4 MB, which would otherwise push past the
cut the recent rounds, where the prevotes a proposal's valid round names are.

### 7.4 Import

`POST /hdls/bundle` on the operator port takes a bundle:

- **Each line goes through the intake of its gossip route:** `SubmitProposal`, `SubmitVote`,
  `AdmitPayment`, `AdmitUpdate`, `AdmitNameRequest`, `AdmitParameterCertificate` and
  `AdmitReassignmentCertificate`. The checks and signatures are the same as for gossip, and what
  the node takes it relays to its connected peers, as it relays gossip. So one bank's import
  reaches every bank it can still reach.
- **Bad lines:** a line that doesn't parse, or that has an unknown type, is counted and skipped.
- **The answer** is `200` with the counts `{"queued":…,"taken":…,"refused":…,"unreadable":…}`.
  Proposals and votes are queued on the event loop, so they count as queued, not taken.
- **The whole bundle is refused** with `409` when the header is missing or names another network.
- **Duplicates cost nothing new:** a bundle imported twice is dropped the way a second gossip copy
  is.
- **It never uses the gossip port.** The allow list stays a filter for the live network. A bundle's
  protection is its signatures (§5.5).

```
curl --data-binary @bank-b.bundle http://engine:8081/hdls/bundle
```

**2026-11-01 (as built)** (`30b9758`, `83f5aa5`):
- **The intake reports what it pooled.** `AdmitUpdate`, `AdmitNameRequest` and the two
  `Admit…Certificate` methods now return `(newlyPooled bool, err error)`, as `AdmitPayment` already
  did. A line counts as `taken` only when its intake newly pooled it, so a bundle imported twice
  takes nothing the second time. A message the node already held counts in none of the four.
- **Unreadable** also counts a line whose body is larger than its route takes.
- **Refused whole** (`409`): a missing or unreadable header, another format's version, or another
  network's id. A bundle over 64 MiB, or one whose upload was cut short, gets `400`.
- **No reason per line.** The answer counts a refused message, but does not say why.

### 7.5 Carrying a checkpoint

A node more than one height behind needs a checkpoint, not votes.

- **The file:** the public folder's `hdls/checkpoint`. A carrier copies it next to the bundle.
- **The route:** `POST /hdls/checkpoint` on the operator port takes it, up to `MaxCheckpointBytes`.
- **The checks:** it is verified as catch-up verifies one (`verifyCheckpoint`, with the node's last
  committed validators as the anchor), and adopted if it is ahead, through the same event as
  catch-up.
- **The answer:** `202` when adopted, and `409` with the reason otherwise ("not ahead", "does not
  verify", "another network").

This also gives a joining node a direct way in from a file. The known limit stays: a joiner trusts
its seed (`findings_20261027.md`).

**2026-11-01 (as built): the reasons** (`e0f7149`). `catchUp` now returns the reason it does not
fast-forward, and the import answers it:
- "not ahead: the checkpoint is of height H, and this node has committed height C";
- "does not verify: it is not a state this network's validators signed, so it is another network's
  checkpoint, or damaged". A checkpoint carries no network id, only signatures that bind it, so
  another network's fails the same check as a damaged one, and one reason names both;
- a node still syncing from its bootstrap peers answers that it is still starting.

The gossip port answers `404` to the POST (§3).

### 7.6 The slow-down switch

A new node config field, `minimumStepTimeoutMs`, from 0 (off) up to `MaxTimingMs`.

- **What it changes:** `Node.armTimeout` uses `max(governed StepTimeoutMs, minimumStepTimeoutMs) ×
  (round + 1)`. Nothing else changes: re-gossip, catch-up, the idle heartbeat and the lost-sign-log
  wait keep the governed timings. So the connected part of the network still re-gossips at its
  usual pace, and a restart does not wait days.
- **When it takes effect:** at the node's next start (R4), logged in the start line. A crash or
  restart does not switch it off.
- **Why it is safe:** no rule reads the clock (§7.1). A longer timer makes a node wait longer before
  voting "no block". It can't make it sign twice or take an invalid block. Every bank may set its
  own value, and mixed settings are safe.
- **When it works:** blocks commit at the carrier's pace once banks holding more than ⅔ of the
  power have it on. One bank alone gains nothing: the others still vote "no block" after 3 s.
- **How to set it:** at least twice the time a carrier takes to bring a bundle from every bank to
  every other bank. A round then completes in about three carries: the proposal, then the prevotes,
  then the precommits.
- **How the banks agree:** out of band, by phone, radio or in person, which is the "manual" of D4.
  When the network is back, each bank sets it back to 0 and restarts.
- **The governed timings stay** the way to set a slow tempo for everyone for good, by a ⅔ vote
  (`docs/managing-the-currency.md`). The switch is the way in when the network can't carry that
  vote.

**2026-11-01 (as built)** (`8e2e840`). The node's field is `Config.MinimumStepTimeout`, a duration.
`cmd/server` reads `minimumStepTimeoutMs`, refuses a value below 0 or above `MaxTimingMs` at load,
and logs the switch in a start line when it is on. The pull of §7.7 keeps the governed timing too.
A test checks that no non-test file of `ledger` or `consensus` imports `time`.

**2026-11-01 (as built, after the review)** (`c7d6383`, `c2e26e2`; review B2). The wait of a node
back without its sign log follows the switch: 2 × max(governed step timeout,
`minimumStepTimeoutMs`) (`ownSignaturesWait`), at the height it comes back at and at each later one
it may have signed at. In pigeon mode its earlier votes and proposal come back only with the next
carry, not at a re-gossip tick. With the old wait, two re-gossip intervals, a proposer that lost its
sign log signed a second block for its round, and its name was burned. The switch is at least twice
a carrier's time, so twice the switch outlasts the next carry. The reason the first bullet gives,
that "a restart does not wait days", does not hold: a restart that keeps its sign log never waits,
so a long switch delays only a node that lost its log. Re-gossip, catch-up and the idle heartbeat
still keep the governed timings.

### 7.7 The public bundle: a second path

The engine keeps `hdls/bundle` in its public folder: the consensus part of §7.3, at most 8 MiB.

- **When it pulls:** once a node has been running a height (not idle) for 4 step timeouts without
  committing it. It then fetches `<PublicAddress>/hdls/bundle` of every other current validator
  once per re-gossip interval, and imports each through §7.4's intake, until it commits or goes
  idle.
- **What that buys:** where direct gossip is blocked but the CDNs still get through, the banks
  still exchange votes and proposals. Every message is signed, so the path needs no trust, and the
  allow list is not involved: the pull goes out, it doesn't come in.
- **Bounds:** the client is `NewHTTPClient(5 s)`, so it follows no redirect, and a read stops at
  8 MiB.

**2026-11-01 (as built)** (`b601914`, `75ddb3b`):
- **Stuck** means: a driver runs (the node is not idle, nor in its post-commit window), and the
  height began at least 4 re-gossip intervals ago. `startHeight` notes the time (`heightStartedAt`,
  the node's clock, which no rule reads). The interval is the governed step timeout, never the
  switch's: with the switch on, the pull starts as soon as on a network at full speed.
- **One round at a time.** The loop decides, and the fetches run off it, a goroutine for each
  validator. A tick that finds the last round still running skips its own (`pullRoundRunning`). The
  round is not tracked: it may outlive `Stop`, for 5 seconds at most.
- **Two log lines**, one when the node starts pulling and one when it stops.
- **A bundle over 8 MiB** is read up to 8 MiB: the import takes the whole lines, and counts the cut
  one as unreadable.
- **The switch matters here too.** Without it, round 0 can't commit by pulls alone: the propose
  timer fires after one step timeout, and the pulls start after four. Each height then runs rounds
  until their timers outlast a pull. So the second path works best with the switch on, as §7.9
  sets it.
- **Cost:** the public bundle is made on the loop at each tick. Under the race detector, with about
  25 votes, that took about 80 ms, since `EncodeBundle` marshals each body and then its line.

**2026-11-01 (as built, after the review)** (`96aa8ef`, `5f26175`, `02feef7`, `b721caa`, then
`d27e72f`, `479cdc0`, `2f9100f`, `4a41d68`, `ff4d853`; review N7, B4, B1, N8, R2-1, R2-4, R2-2):
- **Votes and proposals are checked off the loop.** The pull imports through `importBundle` with
  `pulledCheck`. In the pull's goroutine, each vote and each proposal must be of the height in
  progress, against the committed view's set, or of the last committed height, against the set that
  signed the last commit (now in the committed view). A vote must be by a validator of that set, a
  proposal must name the proposer the set schedules for its round (`fromItsProposer`, which
  `onProposal` shares), and the signature must verify. Before, a hostile public face could fill
  8 MiB with about 1,800 forged votes, or 1,600 forged proposals (R2-1), each a signature check on
  the loop, at every pull.
- **The import stops at the first bad line**: one that fails that check, is unreadable or of an
  unknown type, or that its intake refuses (R2-1). So 8 MiB of tiny lines, or of certificates the
  loop refuses one by one, costs one line. The only such line an honest public bundle can hold is a
  certificate the node refuses; certificates come last, so it loses only what follows. The
  operator's import (§7.4) is unchanged: it goes on past every such line.
- **A bank one height ahead** gives its last commit, which decides the node's height, but not its
  messages of the height after: the import stops at the first of them, and the node takes them at
  its pulls at that height.
- **The cost:** a signature check for each proposal pulled, and for each vote once a committed
  height (`verifiedVotes`; R2-4). The pulls remember the votes that verified, whole, up to 4,096,
  and start again when the committed height changes. Before, a stuck node verified every vote of
  every bundle at every pull: about 0.6 s of CPU per bundle of 1,200 votes.
- **Not the node's own machine.** The fetches from public addresses, the pull's and catch-up's
  (below), refuse a loopback destination, or `0.0.0.0` or `::`, once a name is resolved, through no
  proxy. A validator could otherwise publish `http://127.0.0.1:8080` and make every stuck bank pull
  its own operator port's export, which that bank's loop builds. Since `d27e72f` (N7, rechecked)
  they also drop a connection whose two ends are one address, before a request is sent: the node
  reaching its own machine through another of its addresses, such as rootless Podman's
  `10.0.2.100`, which every such bank has. Another private address works, as the quickstart's do.
  A connection gives up after 2 s (`479cdc0`).
- **Catch-up falls back to the public addresses.** The second path carries votes and proposals, not
  checkpoints. Catch-up asked only the bootstrap peers and the gossip peers, exactly what is blocked
  when the second path is needed, so a validator two or more heights behind never caught up without
  `bootstrap`, which the quickstart does not set. Catch-up now asks the bootstrap peers, then the
  gossip peers, then the other current validators' public addresses, where the fetcher serves the
  status and the checkpoint. It asks every public address when no bootstrap or gossip peer answered
  at all, or while the node is stuck on its height (`b721caa`). Otherwise it asks one of them a
  tick, each in turn (`publicFaceTurn`; `ff4d853`, R2-2): an idle node runs no round, so it is never
  stuck, and when a peer answered at its own height, one as far behind on its side of a split, it
  found the banks ahead only once its heartbeat started a round, minutes later. One small request a
  tick finds them within about as many ticks as there are validators. It asks each peer at the first
  of its URLs that answers, not at every one, and a connection gives up after 2 s, not the
  request's 5 (`479cdc0`, N8). So the second path no longer needs `bootstrap` set to bring a lagging
  node back.
- **Known limits** (the final recheck). With the puller's certificate pool full, a new honest
  certificate is refused at every pull, and the certificates after it in that bundle never come by
  this path. A rogue validator that signs more than 4,096 distinct votes a height can keep resetting
  the memory of verified votes: that costs CPU only, off the loop.

### 7.8 Customers

- **A payment saved as text.** The wallet's send page can sign a payment and show it as a
  one-message bundle, instead of sending it. Anyone can carry it to any bank, and that bank imports
  it (§7.4). The payment counter's rule holds: the wallet spends a block for it, as for a sent
  payment, so it never signs two payments for one block.
- **A checkpoint from a file.** When the node can't be reached, the wallet can load a checkpoint
  file someone carried in. It is verified under the pin exactly as the node's (Q17's ⅔ rule), and
  the page then shows "as of height H, from a file". The wallet needs a recent checkpoint to choose
  the block a payment names.
- **The limit (Q8).** A payment may name a block at most 64 past the height in progress
  (`MaxBlockIDLookahead`). In pigeon mode that is many carries, so it is fine. But while the rest
  of the network runs at full speed, a payment carried out of a cut-off area expires in about a
  minute when the chain is busy, or about five hours when it idles. A customer cut off from a fast
  chain needs some network path, or has to wait.

**2026-11-01 (as built): the wallet's readings** (`76a7b42`, `96a84e9`):
- **The saved payment** is a one-message bundle: its header names the pinned network id, the
  wallet's name and the height in progress of the page's state. It takes a block of the payment
  counter, as a sent payment does. Its block is `-lookahead` past that height, or later, so it
  lapses as soon as the network passes that block, which the page says.
- **The checkpoint is pasted** into a form on the main page, not uploaded. Go's form reader takes at
  most 10 MB: a checkpoint of about 3,500 accounts.
- **It stands in whenever the node's state does not verify**, not only when the node can't be
  reached: also when the node serves an older state, or one the pin does not vouch for. The wallet
  forgets the file once the node's state verifies again.
- **Memory only, but not without effect.** The file is not saved, but like the node's state it moves
  the newest height the wallet verified, and may move the pin (`.trust`).
- **It needs a pin.** A file carries no network id to pin on first use.

**2026-11-01 (as built, after the review)** (`2b3229c`, `da2857b`, `fab032a`, `b1a685c`,
`5a44f92`; review W1, W2, W3, W4, W7, W8):
- **A file stands in less often.** The wallet keeps the newest state it verified, from its node or
  from a file, in memory. A file's state stands in only when the node's checkpoint can't be read (no
  answer, or an HTTP error status). A kept state of either source stands in when the node serves an
  older state its validators signed, as a CDN's copies of different ages may. Any other refusal, a
  state the pin does not vouch for or one that does not match its commit, is shown as it is, even
  with a file loaded, and payments and updates are refused; the freeze still works. Before, a loaded
  file hid every refusal, and payments went to the node the wallet did not trust. The page marks a
  kept state "as of height H, from a file" or "as of height H, read before". A kept state below the
  newest height verified never stands in. After a restart nothing is kept, so a node that serves an
  older state is refused.
- **The freeze**, while a kept state stands in, names its block from max(the kept state's height +
  1, the height in progress the node reports).
- **The save step** follows post, redirect, get: the POST signs and keeps the payment, and GET
  `/send` shows the payment saved last, so a reload never signs a second payment.
- **The form takes a file as large as a node takes**, 64 MiB (`node.MaxCheckpointBytes`). Its body
  is bounded at three times that, since url-encoding can triple a file, which also lifts Go's 10 MB.
  A larger form is refused as too large, a malformed one as unreadable. A pasted text past 64 MiB,
  which needs no encoding and so passes the form's bound, is refused as too large too (`09e4309`;
  review R3-2).
- **The banks-changed message** names its source, the node or the file.
- **No payment or update goes out while a file stands in** (`620fade`; review R3-1). *Sign and
  send* and the account form refuse before they sign or take a block of the counter: "your bank's
  node can't be read, or its state is older than your file's: save the payment as text", or "try
  the update again later". Before, a payment signed for the file's block went to a node that could
  not be read. *Sign and save as text* and the freeze work.
- **Known limit, by design** (W2 (c)). The kept state is in memory only. So after a restart, a
  node that serves an older state than the newest the wallet verified is refused with the rollback
  alarm, even when it is only behind.

### 7.9 Running in pigeon mode, step by step (for the runbook)

1. The banks notice the split: no commits, and pushes failing in the log (§5.4).
2. The banks holding more than ⅔ of the power agree, out of band, to slow down. Each sets
   `minimumStepTimeoutMs` and restarts its engine.
3. On each carry, every bank exports its bundle (§7.3), and the carrier takes the bundles, and any
   bank's newest checkpoint, to every other bank. Each bank imports what it receives (§7.4, §7.5).
4. Customers cut off hand signed payments to whoever carries next (§7.8).
5. When the network is back, the banks set the switch to 0 and restart. Gossip and catch-up do the
   rest.

**2026-11-01 (as built, after the review)**, step 2 (review B2, B1). A restart that keeps its sign
log starts at once. An engine back without its sign log waits 2 × max(step timeout, switch) at each
height it may have signed at (§7.6's note), long enough for the next carry. An engine that a restart
leaves behind catches up from the other banks' public faces, with no `bootstrap` set (§7.7's note).

## 8. The wallet

- **A customer's wallet** runs with `-node` set to the bank's public address. It reads
  `/hdls/status` and `/hdls/checkpoint` (static files) and posts `/hdls/pay`, `/hdls/update` and
  `/hdls/name-request`, which the fetcher passes through. This must be checked by a test against a
  fetcher, not an engine.
- **The bank operator's wallet** runs with `-node` set to its engine's operator port.
  - **The desk** and certificate submission (the two certificate routes) work only there.
  - **The admin page** can read governance from either place. Against a public face it shows
    governance and says that votes are submitted on the bank's operator node, instead of an error.
- **The other banks:** Q25's check reads each bank's `PublicAddress` (§4).
- **The account page:** it can publish the name's public address and IP addresses, as it publishes
  the address today.
- **Offline steps:** the payment saved as text and the checkpoint from a file (§7.8).
- **The manual:** `ui/USER_MANUAL.md` says which address to use, what the offline steps are, that
  wallets contact every bank's public address, and that IP addresses published on chain are public
  for good. The privacy note stays.

**2026-11-01 (as built)** (`a456b3e`):
- **No probing.** The wallet tells a public face from an operator port only by the answer to a
  submission: a `404` becomes "votes are submitted on the bank's operator node". It asks no route
  to find out.
- **The desk works at either address.** Its *Submit* posts `/hdls/name-request`, which the fetcher
  passes on. Only the two certificate routes need the operator port.
- **The test** runs the wallet against a stand-in for the fetcher, in front of a real engine
  (`ui/publicface_test.go`). The fetcher itself is a `main` package of the root module, which the
  wallet's module can't import.

**2026-11-01 (as built, after the review)** (`368c0f1`, `9892c0b`; review W6, W10, and W5, docs
only):
- **Only a certificate's `404`** says where votes are submitted. Any other `404` names the path and
  the answer.
- **The test against a fetcher** (`ui/fetcher_test.go`) builds `cmd/fetcher` with the go command,
  runs it over a real engine's public folder, and points a customer's wallet at it: a payment with
  a verified receipt, an account update, the admin page and a freeze. It skips under `-short`, and
  when no toolchain is at hand.
- **First use trusts the path** (W5). A wallet's first use through a public address pins whatever
  banks that address, and the CDN in front of it, serve. The wallet's docs and the security doc say
  so, and tell customers to get the `.trust` file from their bank, or to do the first use at the
  bank. The wallet's code is unchanged.

## 9. Config, commands and images

New genesis field: `gossipPort` (§4), required, usually 8070.

`cmd/server` config, new fields (unknown keys are still refused):

| Field | Meaning |
|---|---|
| `listen` | the operator port: every route (§3). As today |
| `gossipListen` | the gossip port, behind the allow list, normally `":<gossipPort>"`. `""`: gossip is served on `listen` with no allow list, as today (§2) |
| `allow` | IP addresses or prefixes always allowed on the gossip port (§5.1) |
| `publicPath` | the public folder (§6.1). `""`: none |
| `minimumStepTimeoutMs` | the slow-down switch (§7.6). 0: off |

- **`bootstrap`** may now name banks' public addresses, since the static files keep today's paths.
- **`cmd/fetcher` flags:** `-listen`, `-folder` (the public folder), `-record` (the record folder),
  `-lifelong` (the lifelong file), `-engine` (the operator port URL),
  `-max-submissions-per-second` (default 100).

Images and compose, under `deploy/`:

- **`deploy/Dockerfile`:** a multi-stage build, `CGO_ENABLED=0`, into a `scratch` image holding
  `server`, `fetcher` and `keygen`, run as a numeric non-root user. The Go version is pinned to the
  one CI uses.
- **`deploy/ui.Dockerfile`:** the wallet, built the same way.
- **`deploy/compose.yaml`:** four banks on one Docker network, each an engine and a fetcher, with a
  data volume per engine that its fetcher mounts read-only.
  - Each engine has a fixed address on the Docker network, which the genesis lists as its IP
    address.
  - Public addresses are the fetchers (`http://bank-a-fetcher:8080`), with no CDN.
  - The node config, which holds the private key, is mounted read-only as a secret.
- **`make quickstart`:** writes the keys, the genesis and the four configs into
  `deploy/quickstart/`, then starts the compose file. This is the four-validator quickstart of
  `TODO.md`. A small Go program is preferred to a shell script, so it needs no `jq`.
- **`make images`:** builds the images.

**2026-11-01 (as built)** (`1a57001`, `c461754`, `0f81369`, `58b7c66`):
- **The images** build with `golang:1.27.3`, the version `go.mod`'s toolchain line names, and run
  as 65532:65532. They carry the certificate authorities, for public addresses behind https. The
  engine's image owns `/data` to that user, so a new volume mounted there takes that owner. Both
  builds copy only the Go sources, and `.dockerignore` keeps the quickstart's keys, `bin` and `.git`
  out of the build context.
- **The compose file:** a network on `172.28.70.0/24`, the engines at `.11` to `.14`, the fetchers
  published at `127.0.0.1:18081` to `18084`. The gossip and operator ports are not published. Each
  config is a compose secret, and the genesis a read-only bind mount. The fetchers run as the same
  user as the engines, to read the record files. It sets no `ip_range`: Podman's network plugins
  refuse a fixed address outside the range.
- **`network.env`.** The engines name their record files with the first 16 hex digits of the
  genesis hash. `cmd/quickstart` writes them into `network.env`, and every compose command needs
  `--env-file deploy/quickstart/network.env`; the `Makefile` passes it.
- **The quickstart** writes the four banks' keys, the genesis (checked as a node checks it first),
  the four configs, and wallet files for two funded customers, `alice` and `bob`. It refuses a
  folder that is there already. The folder is 0700; the configs and the genesis inside are 0644,
  since the containers' user is not the machine's.
- **`make quickstart-clean`** takes the network down with its volumes and removes
  `deploy/quickstart`. `COMPOSE` and `CONTAINER` choose the tools (for example
  `COMPOSE=docker-compose`).

**2026-11-01 (as built, after the review)** (`03fd051`, `f716fb3`, `5409243`, `f2fdd3b`; review
N6, N11, N9, N13):
- **`listen`** defaults to `127.0.0.1:8080` when `gossipListen` is set, and stays `:8080` without
  it. The start log warns of an operator port on every interface beside a gossip port, and of a
  gossip port that is not the genesis `gossipPort`, which is not refused: a port mapping in front
  may forward the one to the other.
- **Every container of the compose file has a fixed address**, the fetchers `.21` to `.24`. Before,
  a fetcher could take a stopped engine's address from the pool, and the other engines would let it
  in. A container started on the network from outside the file still takes one from the pool.
- **The quickstart's wallet commands** carry `-listen`: 8090 for alice and 8092 for bob, so both
  run at once.
- **`.dockerignore`** also leaves out the wallet's files under `ui/`: `ui/**/*.json`,
  `ui/**/*.json.*`, `ui/**/*.state` and `ui/**/*.trust`.
- **The demo** takes `-listen 0.0.0.0:8080` or `[::]:8080` (`55bdfe4`; review W9). The founder then
  publishes `127.0.0.1` as its IP address, and `http://127.0.0.1:8080` as its public address
  (`d201a2d`; review R3-3).
- **In the quickstart** every operator port listens on the shared network, so a validator could
  publish another engine's operator port as its public address and make every stuck engine pull
  that engine's full export (the bundles recheck's side note). The runbook says so: on a real
  network no other bank reaches an operator port.

## 10. Tests

Each slice adds its tests, and the full check list must pass. These are required:

- **The address fields:**
  - genesis and update take a public address and IP addresses;
  - they refuse a non-canonical address, an IPv4 address in IPv6 dress, an unspecified or multicast
    address, a duplicate and a fifth address;
  - they clear with `ClearField`.
  - The peer DNS builds `http://<address>:<gossipPort>` (IPv6 in brackets), and the wallet reads
    the public address.
- **The fallback:** the transport tries a validator's next address when the first refuses
  connections.
- **The allow list, at the listener:** with fixed remote addresses (a fake `net.Conn`), a listed
  address is accepted and an unlisted one is closed before a byte is read. Also IPv4 inside IPv6,
  and prefixes from `allow`. The list follows a commit that seats a validator or changes its
  addresses.
- **The allow list, end to end:** four nodes on loopback addresses (`127.0.0.1` to `127.0.0.4`,
  each node's transport dialling from its own address) with gossip ports and allow lists commit
  blocks. Taking a node's address off the chain stops its
  pushes from being taken, and the rest (above ⅔) still commit.
- **Ports:** the gossip port answers `404` to `/hdls/pay`, `/hdls/accounts`, `/hdls/bundle` and the
  record routes. The operator port serves everything.
- **The public folder:**
  - after a commit, `hdls/status`, `hdls/checkpoint`, `hdls/accounts` and `hdls/governance` are byte
    for byte the route answers;
  - a name's record is rewritten when the name changes and only then;
  - the key file follows a key change;
  - no partial file is ever visible;
  - a node with `bootstrap` set to a fetcher's address catches up from the static files.
- **The fetcher:** it serves only the listed paths, refuses a directory, `.incoming`, `..`, a bad
  name and a bad key, sets the cache headers of §6.3, passes the three submissions through with the
  engine's answer, and answers `429` above the cap.
- **Bundles:** export, then import into another node with no network between them, carries votes,
  the proposal, certificates and pools. A bad line is counted and skipped. Another network's bundle
  is refused whole. Importing twice changes nothing.
- **The pigeon test.** Four validators share no network link at all, and a test "courier" carries
  bundles between them on a fixed beat.
  - With the switch on, set above the beat as §7.6 says, the chain commits several heights with no
    fork, and most heights commit in an early round.
  - With the switch off and a short governed step timeout, it does not keep that pace.
- **Checkpoint import:** an ahead, verifying checkpoint is adopted. One behind, one that doesn't
  verify, or one from another network is refused with its reason.
- **The second path:** a network whose gossip pushes all fail, but whose fetchers answer, commits
  by pulling public bundles.
- **The switch:** `armTimeout` uses the larger of the two values, and re-gossip keeps the governed
  interval.
- **No clock in the rules:** a test checks that `ledger` and `consensus` do not import `time`, since
  §7.6 relies on it.
- **The wallet:**
  - a customer's wallet works against a fetcher;
  - a saved payment imports at a bank and commits;
  - a checkpoint from a file moves the pin under the ⅔ rule, and a forged one is refused.

**2026-11-01 (as built):**
- **The pigeon test** (`7b4871f`): a governed step timeout of 20 ms, a courier that rests 100 ms
  between carries, and an idle heartbeat of a millisecond, so every node starts its next height at
  once. With the switch at 1 s, the chain commits 4 heights in 12 carries, in practice each in round
  0 (the test asks for most). With
  the switch off, rounds 0 and 1 fail every time, so no height commits before the seventh carry; the
  test checks that in 6 carries the switched-off network commits fewer heights than the switched-on
  one did (none against two). Ten runs under `-race` gave the same carries and rounds.
- **The second path** (`b9defd2`) runs with the switch on, at 2 s, as §7.9 sets it: without it,
  round 0 can't commit by pulls alone (§7.7). Ten runs under `-race` passed, in 2.7 to 3.0 s.
- **The customer's wallet** is tested against a stand-in for the fetcher (§8).

**2026-11-01 (as built, after the review):**
- **The allow list, end to end** now shows a validator whose loopback address only the chain lists
  refused by the other three, which list their own addresses in `allow` and commit without it (N1).
  Following the chain is covered at the listener, by `TestAllowListFollowsTheCommits`.
- **The second path's test** checks that every node decided the same blocks (`bd18774`, B8).
- **The customer's wallet** is tested against the real fetcher too (§8's note).
- **Three tests run on Linux only** (`node/httptransport_linux_test.go`): a push that gives up a
  URL after 2 s, a push that survives a lost connection packet, and catch-up giving up a URL after
  2 s.
- Each fix added tests of its own, which its commit names.

## 11. The slice plan

Each slice starts from the branch after the ones it depends on. In the root module and in `ui/`,
`go build ./...`, `go vet ./...` and `go test -race ./...` must pass. Slices in one group never
share a file, test files included.

| Group | Slices | Starts after |
|---|---|---|
| 1 | P1 · P3b | `main` |
| 2 | P2 | P1 |
| 3 | P3a | P2, P3b |
| 4 | P4 | P3a |
| 5 | P5 · P6 · P7 | P4 |
| 6 | P8 | everything |

- **P1. Addresses on chain** (§4): `ledger` (account, update, genesis, `gossipPort`, digest,
  `CanonicalIPAddress`), `node/peerdns.go`, the transport's fallback to the next address, the
  resolve answer, `ui/banks.go` and the account page, `cmd/demo`, and the tests of each.
- **P2. The two ports and the allow list** (§3, §5): `GossipHandler`, `allowListListener`, the list
  from the chain and `allow`, the transport's failing-peer log line, and the `cmd/server` config
  fields `gossipListen` and `allow`.
- **P3a. The public folder** (§6.1): the engine's writer (status, checkpoint, accounts, governance,
  resolve) and `publicPath`.
- **P3b. The fetcher** (§6.2, §6.3): `cmd/fetcher`, a program of its own, so it is built beside P1.
- **P4. Bundles and the switch** (§7.2 to §7.6): export, import, checkpoint import,
  `minimumStepTimeoutMs`, the public bundle file, and the pigeon test.
- **P5. The second path** (§7.7): pulling public bundles when stuck.
- **P6. The wallet** (§8): the admin page against a public face, the payment saved as text, and the
  checkpoint from a file.
- **P7. Images, compose and the quickstart** (§9): `deploy/` and the `Makefile` targets.
- **P8. Docs** (§12), then an independent review, its fixes and a recheck, and the check list.

**2026-11-01 (as built).** The plan ran as the table says, with P3 split in two before the build
(`9645480`) so that the fetcher, a program of its own, was built beside P1. Group 5's three slices
were built side by side and merged in the order P6, P7, P5. P8 had two writers side by side: one for
`docs/`, `README.md`, `TODO.md` and the findings, and one for the wallet's docs, the guide to reading
the code, this spec and `review/phase3_progress.md`. The independent review follows.

## 12. Docs to change

- **`docs/api.md`:** "One open port" becomes the two ports and the public face. Add the route table
  of §3, the static files, the bundle and checkpoint routes, and the IP addresses and gossip port in
  "How messages travel".
- **`docs/operating-a-network.md`:** the genesis field and the new config fields, "Network exposure"
  rewritten (the allow list, the rule of §5.4, changing addresses, the CDN), the compose file and
  the quickstart, and a new section, "When the network splits", from §7.9.
- **`docs/security.md`:** "The deployment is responsible for" no longer says to keep the network off
  the internet. It says what the allow list does and doesn't do (§5.5), that the public face holds
  nothing secret, and that published IP addresses are public.
- **`docs/managing-the-currency.md`:** the pigeon mode section gets the carrier and the switch.
- **`docs/consensus.md`:** the timers, what parks without a timer, and the switch.
- **`docs/architecture.md`:** the public folder writer and the bundle.
- **`docs/governance.md`:** the names as a name service: what a name publishes and who signs it.
- **`docs/glossary.md`:** engine, fetcher, gossip port, operator port, allow list, public folder,
  bundle, carrier, slow-down switch, public address, IP addresses.
- **`docs/README.md`:** the four areas, with the fetcher built.
- **`ui/README.md`, `ui/USER_MANUAL.md`:** §8.
- **`README.md`:** the deployment paragraph and the quickstart.
- **`TODO.md`:**
  - the Phase 3 item: the network split and pigeon mode are built, and the signer remains;
  - the quickstart: done;
  - Deployment: done;
  - `findings.md` #10: authentication is now the allow list, and rate limiting is at the CDN and
    the fetcher's cap;
  - new: the rest of the name service (a DNS gateway, TLS key records; R7).
- **`findings_20261027.md`:** "Phase 3 is a plan" becomes what is built and what remains.

**2026-11-01 (as built).** `docs/reading-the-code.md` changed too, though this list left it out. It
names the functions Phase 3 renamed or added, and gained four steps: bundles and the switch, the
allow list, the public face, and the second path.

## 13. Open questions for the owner

Every row is decided. Each says so, with the date and, where the owner gave them, the owner's
words.

| # | Question | Answer or builder default |
|---|---|---|
| Q1 | The firewall inside the container: refuse in the engine's process at accept (R2), or a kernel firewall (nftables) in the container, written by the engine? | **decided 2026-11-01: in the process** (R2) |
| Q2 | Two addresses on chain? | **decided 2026-11-01**, changed into D5: today's address becomes the public address, and every name may add IP addresses (§4) |
| Q3 | Put `resolve`, `accounts` and `governance` in the public folder too? | **decided 2026-11-01: yes** (D6, §6.1) |
| Q4 | Should the bank sign its status file? | **decided 2026-11-01: no.** Status is a hint, and the checkpoint is the signed document |
| Q5 | The fetcher's global cap on submissions | **decided 2026-11-01:** 100 per second by default, each bank's own setting. The owner: "i'd make that the default but make it editable by the single provider as nothing else depends from it" |
| Q6 | The switch: a config field read at start (R4), or also a route on the operator port that changes it while the node runs? | **decided 2026-11-01: the config field, read at start** (R4). It survives a crash, and it is one line an operator can read out by phone |
| Q7 | The second path (§7.7): pull public bundles after 4 step timeouts stuck, the consensus part only, at most 8 MiB? | **decided 2026-11-01: yes** |
| Q8 | Payments carried out of a cut-off area expire after 64 blocks of the fast chain | **decided 2026-11-01:** no change, documented as a limit |
| Q9 | Export and import by `curl` on the operator port, or also buttons on the operator's admin page? | **decided 2026-11-01:** `curl`, in the runbook |
| Q10 | A log line when a peer's pushes start failing and when they recover | **decided 2026-11-01: yes** |
| Q11 | Write the public checkpoint at every commit, or at most every N seconds? | **decided 2026-11-01: every commit.** The owner: "it is the recap and what makes the next step verifiable" |
| Q12 | The gossip port in plain HTTP, TLS left to the operator? | **decided 2026-11-01: yes.** The owner: "if the name server becomes tied with this infrastructure in a small community the problem disappears" |
| Q13 | D5's details (R6): plain IP addresses, at most 4 per name, one gossip port for the network in the genesis, pushes falling back to the next address? | **decided 2026-11-01: plain IP addresses** (R6). One engine per public IP address |

Notes for later (not Phase 3):

- **A DNS gateway:** a small program that answers DNS queries for a zone from the chain's name
  records. Then ordinary resolvers, and the DNS operators of D5, can use them.
- **TLS key records** (R7): a name publishes a TLS key, or its hash, signed by the name's key, as
  DANE's TLSA records do. TLS then works with no certificate authority wherever the client reads
  the chain: between banks, and in wallets.
  - The payment key stays off the web server.
  - The chain's own scheme doesn't limit the TLS key's algorithm.
  - Browsers accept neither DANE nor Ed25519 certificates for websites today, so this serves custom
    clients first.
