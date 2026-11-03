# Security model

Area: **general**: the threat model spans all four areas. `go_ledger` is a Go port of the Java
Hypernode ledger (see the [README](../README.md#origins)), hardened as it was ported. The review
rounds are in [`findings.md`](../findings.md) and [`findings_20261027.md`](../findings_20261027.md).
This doc states the threat model, what the design fixes by construction, the limits of the freeze
and of the banks, what the signer, the two ports, the public face, the name service and the joiner
protect, and what a deployment is still responsible for.

## Threat model and target

The target is a **small, known-identity, high-trust community** running its own currency on a
handful of machines, for example 8 laptops on an isolated intranet.

- Participants are known and socially accountable; the adversary is not an anonymous global
  attacker.
- Value is low to moderate; the network is small and mostly in one place.
- The realistic threats are bugs that corrupt the ledger with no attacker, one dishonest or
  compromised validator, anyone who can reach a node's ports, a stolen key, operator mistakes,
  unreliable hardware (laptops sleeping, rebooting, dropping off), and a network provider that
  slows, splits or cuts the links between the banks.

The design still aims to tolerate Byzantine validators holding under ⅓ of the power.

## Fixed by construction

- **Supply integrity (B1, B2).** `ledger.ProcessPayments` moves value only for a payment of at
  least the transaction cost, between two names that exist. A negative payment and a payment from
  nobody, both of which minted money, cannot be represented. Money is exact (`big.Rat`).
- **No signature reuse across purposes or networks (C1, C6, C7).** Every signature is scoped to a
  domain and bound to the network id by a length-prefixed preimage. `signing.Signer`, the only
  signing API the node and the wallet use, has no "sign raw bytes" call, and the node has no
  signing route. See [signature.md](signature.md#what-every-signature-binds).
- **Every change needs its name's current key.** Payments, account updates and name requests name
  their signer, and every node checks the signature under that name's key in the state.
- **Malleability cannot double-spend.** A message's id hashes its signed content, never its
  signature, so a second valid signature over the same payment is the same payment, in the pools,
  in gossip and in `ProcessPayments`.
- **One ML-DSA implementation is a consensus rule.** Two nodes that disagree on a signature's
  validity can fork. Every node uses Go's `crypto/mldsa`, verifies only exact sizes and the one
  canonical base64 spelling, and every node runs the same release. A port to another language must
  verify exactly as `crypto/mldsa` does.
- **No fork while under ⅓ is Byzantine (A1, A2, A4).** Finalisation needs a > ⅔ quorum. Two such
  quorums share an honest validator, who never precommits two blocks in one round, and the lock
  carries that across rounds, so two conflicting commits cannot form (see
  [consensus.md](consensus.md#the-rule)). The commit covers the applied state. The argument assumes
  an honest validator remembers what it signed, across restarts too, which is what its sign log is
  for. A double-sign freezes the name. Voting power saturates at 2^40 per validator, so the
  thresholds treat all validators above it as equal.
- **SHA-256 everywhere (C5).**
- **Replay protection without a spent-set.** Every payment, update and name request signs the one
  block that may carry it (`BlockID`) and the network id. Each height occurs once, so a message
  applies at most once. The optional pin (`PreviousStateDigest`) also binds a payment to the full
  state digest of the state its block builds on; intake takes a pin only for the next block.
- **An honest payer cannot trip the overdraw ban.** A block whose payments together overdraw their
  payer destroys the payer's balance. Every intake point refuses a payment, or a request fee, that
  with the payer's pooled payments and fees would pass its balance, and the pools are re-checked
  after each commit or catch-up. So a committed overdraw means a cloned key or a modified node.
- **Race-free node.** One goroutine owns all node state; the tests run under `-race`.
- **Bounded input.** Request bodies are read up to 1 MiB (2 MiB for a proposal, 64 MiB for a
  bundle or a carried checkpoint), a peer's checkpoint up to 64 MiB, which holds about 23,000
  accounts: a larger network cannot catch up or join by checkpoint until the bound is raised (see
  [api.md](api.md#two-ports-and-the-public-face)). A public bundle is read up to 8 MiB, and the
  pull checks each of its votes and proposals off the event loop, a vote once a committed height,
  and stops at the first line that fails, is unreadable or is refused: a forged bundle costs a
  stuck node one signature check, off the loop, or one line on it. Amounts are checked as text
  before they become numbers, so a few bytes cannot become a huge number. Signatures of the wrong
  size are refused before any work.
- **No redirect is followed.** Every request a node or a wallet makes, to gossip, to catch up, to
  pull a public bundle, or to read a bank, goes through `node.NewHTTPClient`, which hands a
  redirect back as a failed answer. Otherwise a node, or anyone on the plain-HTTP path, could send
  the request on to any URL the machine can reach: the wallet's own pages, or a router's on the
  user's network (review follow-up, 2026-11-01). The addresses nodes and wallets send to are plain
  too: a public address is `http(s)://host[:port]`, nothing more, and an IP address is in its one
  plain form, the forms the ledger takes (see [api.md](api.md#how-messages-travel)). A node's
  fetches from a public address, which each validator chooses for itself, also refuse to connect
  to the node's own machine, at a loopback address, at `0.0.0.0` or `::`, or at any of its other
  addresses: a validator can't point the others' pulls at their own operator ports.
- **Caps.** `maxPaymentAmount` bounds what one payment moves, and `maxAccountBalance` what a payment
  or the ⅔ can raise a balance to. They are policy, voted by the ⅔ (see
  [governance.md](governance.md#the-parameters)).

## The network id

The effective network id is the network's name, a `/`, and the hex of the genesis hash, which
covers the genesis state, its parameters and its validator rule (E20). Every signature binds it,
so a new genesis that changes anything does not accept the old network's payments, certificates or
votes. A new genesis with **identical** contents is the same network, and replaying it lets anyone
freeze the banks with their own old votes: the old and the new precommits for one height and round
sign different block hashes (new nonces) under the same key and id, and the old commits stay in
every node's signature record. So every new genesis takes a new `networkName`. See
[operating-a-network.md](operating-a-network.md#starting-a-new-network).

## The freeze and its limits

A name is frozen by its holder (free, at once), by a double-sign, or by the ⅔. A frozen name
neither pays nor is paid and leaves the validator set until the ⅔ re-key it.

- The double-sign freeze handles double-signing by validators holding **under a third** of the
  power: a block carries evidence only while the frozen names stay under a third. At a third or
  more, recovery is a new genesis with new keys and a new network id.
- Losing the largest validator must still leave more than ⅔ of the rest online, or the chain stops.
- A holder's own freeze has no cap. A bank holding a third or more that freezes itself hands the
  chain to the rest. Nodes that lag then walk the frames from the public faces, or, where they
  can't, need a snapshot from a node you trust (see
  [operating-a-network.md](operating-a-network.md#a-joining-node)), and wallets that pinned it need
  a new list.
- The last seated name cannot freeze itself, and no ⅔ action may freeze it: a block that seats no
  name could never be followed. So the last bank has no emergency brake.

## Banks and their co-signatures

A name registered at a bank has that bank as its bank of record; a genesis name, or one the ⅔
create, may have none. A bank uses one key, the one it votes with, for its votes and its
co-signatures, so freezing a leaked bank key stops both at once.

- A key change needs the holder's key, its bank's co-signature and the new key's proof, and it
  waits `changeDelayBlocks` (about a day). In that time the holder sees it as a pending change and
  can freeze the name, which cancels it. A bank change needs the holder and the new bank only, so
  no bank can hold its customer hostage.
- Freezing a bank clears every pending change it co-signed: its customers' key changes, and bank
  changes to it. Honest customers file again; the fee is not refunded.
- A wallet reads its state through one node. A bank that colludes with a thief can try to hide a
  pending change from its customer if the customer's wallet reads the bank's node: the node keeps
  serving the last genuine state from before the change was filed. The wallet refuses a state older
  than one it verified, reports a freeze that did not land, and the bank's desk lists its
  customers' pending changes. And after each state verifies, its main page asks every bank, at
  the public address on its account, for its height: at that address's scheme and host alone,
  following no redirect, and only while the page is open, so a bank cannot point the question at the
  wallet's own page, which would ask again, or at another device on the user's network (review
  follow-up, 2026-11-01). If banks holding more than ⅓ of the power
  report more than 10 blocks past the wallet's state, the page warns that its node may be hiding
  newer blocks; if the banks that answer hold no more than ⅔, it says how much it could check; if the
  network's newest block has not moved for twice the 5-minute heartbeat and the block interval (about
  10 minutes by default), it says the chain may have stalled. Those
  heights are not signed, and need not be: they only choose a warning, and the state the wallet
  shows is still the one the pinned ⅔ signed. A false "behind" needs lying banks holding more than
  ⅓ of the power, and so does hiding a node's lag from a wallet that heard from more than ⅔. See
  [ui/README.md](../ui/README.md#how-it-works).
- A wallet's pin follows the validator set, but only to a set that more than ⅔ of its pinned power
  signed for, the rule a node's catch-up follows too: a pin may hold keys that leaked. If banks
  holding a third or more of the pinned power change between two of its reads, it needs a new list,
  and its pages say so. Any node, or anyone on the plain-HTTP path, can make them say so, by serving
  a made-up set that signs for itself, to get its own list trusted; so the message says to take a
  new list, or pin again, only at a bank, never through a node the user does not trust.
- Anyone can register any free name, a hash name included; the ⅔ name action undoes a squat. Names
  can look alike (`rn` and `m`, `1` and `l`), so the wallet shows the payee's bank of record before
  it signs a payment.

## The signature record

Each node keeps who signed each block for `retainBlocks` heights, and every governance certificate
and double-sign proof for the life of the chain. It never keeps payments, updates, name requests,
nonces or state digests. The block hash mixes in the proposer's random nonce, so the record cannot
be matched against public balances to walk the history back.

In dynamic mode a seat's power is floor(stake), so most busy blocks change the set, and the record
shows each seat's stake per height. Static mode is not affected. This stays as it is (spec §12 Q21,
decided 2026-10-31). Every balance is public at the time through `GET /hdls/accounts`, so what the
record adds is the history of each seat's stake over the heights it keeps: an observer who polled a
node then has it anyway, and a later one can rebuild it. The alternative, power = floor(stake /
threshold), would hide it but weigh votes coarsely. Where that history matters, use static mode.

The record is public on purpose. The fetcher serves its files to anyone, and a CDN keeps a
complete unit for good. On the operator port, a request with `from` reads at most two unit files of
100 heights: in static mode about 3.7 MB with 4 validators and 14 MB with 21; in dynamic mode, where
most entries carry the set, about 5.8 MB and 26 MB. A request without `from` also lists the record
folder, about 6,000 files at the default retention. See [api.md](api.md#the-record-routes).

## The key's place: the signer

A bank's one key can live in the signer, `cmd/signer`, a program on the bank's private network that
accepts nothing from the internet. The engine and the bank's wallet hold the public key only, and ask
it for each signature, each as a client with its own secret and its own list of domains. Its rules,
its logs and its limits are in [signature.md](signature.md#the-signer).

- **What it changes.** A break-in on the engine or on a wallet terminal no longer takes the key. It
  gets only what that client's domains allow, in the bank's own name, and the signer gives each vote
  and proposal slot one signature, written to disk before it is made. So a stolen engine cannot get
  the bank frozen for a double-sign.
- **What it does not change.** A stolen engine still keeps the bank from voting, can have votes
  signed for heights to come, which stops the bank voting until it re-keys, and signs receipts that
  lie. A stolen admin terminal still pays the bank's money, changes its addresses and TLS keys, and
  signs its governance votes. A stolen desk co-signs, and can freeze the bank. A break-in on the
  signer's own machine, once its passphrase is read, takes the key: the key is in its memory while it
  runs, and only hardware, outside the standard library, would keep it out.
- **It does not stop a fork on its own.** The engine's own sign log, with its lock, still does that
  job.
- **The link has no TLS.** It carries only what the bank will publish. An HMAC under each client's
  secret tells who asks, and every client checks every answer under the bank's public key. Someone
  on the bank's private network can read the requests and drop them, which costs the bank its vote.

## The two ports and the public face

A bank's engine serves two ports, and a fetcher serves its public face behind a CDN (see
[api.md](api.md#two-ports-and-the-public-face)).

**The gossip port and its allow list.** The gossip port serves only the routes validators use, and
lets a connection in only from the IP addresses of the current validators, as the chain publishes
them, and the entries of the node's `allow`. A stranger's connection is closed before a byte of it
is read, and a validator's is closed once its address leaves the list. The list is a cost filter:
it keeps strangers off the routes that cost a node CPU and event-loop time. What it does not do:

- **It is not authentication of messages.** Signatures are. A message from an allowed address is
  checked as fully as any other.
- **It does not stop a flood aimed at the address itself.** The IP addresses are on chain, so they
  are public. A flood at the network level is the hosting provider's to absorb, as it is for any
  server.
- **It trusts each validator's own list.** A validator that publishes a third party's address
  opens the port to that third party. That costs no more than the validator could do itself, since
  it already sends.
- **It can be fooled by someone on the path.** Someone who can see and change the traffic between
  two banks can pass as an allowed address. Signatures still protect what that person sends.
- **It needs each caller's own address.** It trusts the address a connection comes from. A
  forwarder, or a TLS or TCP proxy in front of the gossip port, makes every caller one address,
  most often the engine's own or a loopback one. The list leaves out, from the chain, the addresses
  the node's own account publishes and loopback addresses, and the engine refuses a connection
  whose two ends are one address, as a forwarder inside its own network namespace opens it
  (rootless Podman's default port forwarding, at `10.0.2.100`). So such a front fails closed: every
  caller is refused, and the log says so, even where a validator publishes that address. Were those
  addresses let in, everyone would get in. For the same reason, a validator that publishes a
  loopback address puts it on no other bank's list.
- **It does not catch Docker's userland proxy.** That proxy runs outside the engine's network
  namespace, and makes every caller the bridge's gateway, such as `172.17.0.1`, which is no address
  of the engine's. Were the gateway on the list, by `allow` or because a validator publishes it,
  everyone would get in. So with Docker, publish the gossip port the default way, through iptables
  (DNAT), which keeps the caller's address; never run it with the userland proxy alone, iptables
  turned off; and never list a bridge's gateway in `allow`.
- **It has no TLS.** Nothing on the gossip port is secret, and every message is signed. Someone on
  the path can read it, and can drop or delay it, which costs liveness, as any network fault does.
  TLS in front of the port can't be combined with the allow list (above).
- **One engine per IP address.** The gossip port refuses a connection whose two ends are one
  address, so two engines that share an address can't gossip through it.

**The operator port** serves every route, with no allow list: customers' payments with their
receipts, the account list, governance votes, bundles and carried checkpoints. Only the bank's own
machines may reach it. Publishing it would open every route to anyone again. Beside a gossip port it
listens on `127.0.0.1:8080` unless the config says otherwise, and the start log warns when it is on
every interface.

**The public face holds nothing secret.** The fetcher holds no key and keeps no state. It serves
files the engine wrote for the public and the signature record, and passes three kinds of customer
message on to the engine: payments, updates and name requests, at most 100 a second by default.

- **What a cache can do.** The checkpoint verifies by its ⅔ signatures, so a cache or a CDN can
  serve an old one, never a false one. The other files are the bank's word, as the routes are: a
  reader that trusts no single bank compares several, or checks a record against a checkpoint.
- **A wallet's first use trusts the path.** A wallet with no list of banks pins, on first use,
  whatever banks its node serves. Through a public address, that is whatever the address and the
  CDN in front of it serve, and over plain HTTP anyone on the path too. Once the wallet holds its
  list, a cache can serve it an old state, never a false one. So a customer gets the list of banks
  from the bank, the `.trust` file to save beside the wallet file, or does the first use at the bank
  (see [ui/README.md](../ui/README.md#its-files)).
- **Limits per client** are the CDN's job. The fetcher's cap is global: it bounds what reaches the
  engine, so a flood above it costs the bank's customers their turn, never more engine work than
  the cap lets through.

**Published addresses are public for good.** A name's public address and IP addresses are on
chain: every node holds them, every checkpoint and account list carries them, and the public face
serves them to anyone. Clearing them later does not take back what anyone copied meanwhile. A name
that publishes the address of a home publishes it to the world.

What this leaves of `findings.md` #10, the open API: the gossip routes are behind the allow list
and their signatures, the public routes behind a CDN and the fetcher's cap, and the operator port,
which still has neither, is never published.

## The name service

The engine writes the names' records as a zone file, and a lookup from an IP address to the names
that list it (see [operating-a-network.md](operating-a-network.md#the-name-service)).

- **A record is a claim.** Nothing checks that a name controls the addresses it lists. The lookup
  says who *claims* an address, not who uses it, and names every claimant. So a flood that the
  allow-list refusal line or the fetcher's line attributes to a name is a reason to call that name's
  bank, not proof. No code bans anyone.
- **The zone is a bank's word.** It follows from the committed accounts and the zone settings, so
  two banks with one setting serve the same zone, apart from its serial, and anyone can compare
  them. The engine does not sign it with DNSSEC; without that, a DNS answer can be forged on the path,
  and a `TLSA` record counts for nothing.
- **The wallet pins a bank's web server by the chain.** When a bank lists TLS keys on its account, a
  wallet that verified a state takes, at that bank's public address, only a certificate whose key is
  one of them, and asks no certificate authority (`ui/tlspin.go`). Only a validator's record pins,
  since any name may list any address. Before the wallet verifies a state, at its first read after
  each start, the usual check by certificate authority applies.
- **An IPv6 flooder can spread over a /64**, so that each of its addresses sends little, and the
  lines that name the address that sent the most name none of them.

## Joining: trust a frame, not a peer

A node takes a newer checkpoint only when names holding more than ⅔ of the power of the validator
set it trusts signed it, under that set's keys: forging a state takes the keys of more than ⅔ of a
set the node already trusts, not ⅓ as before. Where the set changed too much, it walks from set to
set, each signed off by more than ⅔ of the one before. It asks every validator, so one peer that
withholds the newest checkpoint is outvoted, and two valid commits for different blocks at one
height stop it with an alarm, for people to look (see
[operating-a-network.md](operating-a-network.md#a-joining-node)).

What is still trusted:

- **The first frame.** A fresh node trusts its genesis, and a seeded one its seed. If the seed is
  false, every later check runs inside a false world, so a joiner checks its seed with a second bank.
  A seated node is confirmed once a commit holds its own precommit; a node whose seed was altered
  never is.
- **Old keys.** There is no trusting period. Keys of more than ⅔ of the power of any old set on the
  walk's path, stolen or sold, can sign a false history that a node away since then takes.
- **A fork it cannot see.** The check needs a peer still at the node's own height. A node behind on
  the other side of a fork, with every peer two or more heights ahead, as on a busy chain, takes
  their side without an alarm.
- **More than ⅓ lying together** can still make two valid commits at one height. The alarm turns that
  into a stop, not a silent choice.

## The deployment is responsible for

- **Keeping private keys private.** Keys are made by `keygen` or the wallet on the machine that
  holds them, and never sent. Never put a private key on a network endpoint. A bank's key goes in
  its signer, sealed, with its passphrase off the key's disk, and the signer's port reaches only the
  bank's engine and wallet (see
  [operating-a-network.md](operating-a-network.md#running-a-signer)).
- **Exposing each port only to whom it is for.** The gossip port faces the internet behind its
  allow list, at the IP addresses the bank publishes, with each caller's own address intact, and
  the bank publishes every address its engine connects from. The operator port is never published.
  The public face stands behind a CDN, which limits each client. A bank's wallet signs as the bank,
  so only its operator reaches it, never from a network others share, and anywhere but a loopback
  address it
  asks for a password. See
  [operating-a-network.md](operating-a-network.md#network-exposure).
- **Provisioning for liveness.** Finalisation needs > ⅔ of the power online. On flaky hardware, put
  enough power on the machines you can keep awake. A partition that leaves no side above ⅔ stalls;
  it cannot fork or mint, and it recovers once the partition lifts, since nodes running a height
  re-send its votes. Meanwhile the banks can carry their messages by hand (see
  [operating-a-network.md](operating-a-network.md#when-the-network-splits)).
- **Persistence, the sign log and backups.** A validator must set `snapshotPath`, keep its sign log,
  run its key on one node only (see [signature.md](signature.md#the-sign-log)), and never restart
  from an old backup. Keep an off-machine export of the state too. See
  [operating-a-network.md](operating-a-network.md#rejoining-after-a-restart). A signer's sign log is
  kept the same way, one per signer, and its audit log is rotated before it fills the disk, which
  would stop the bank voting.
- **A checked seed.** A node seeded from a snapshot trusts it; check its height and block with a
  second bank before its first start.
- **A clean new genesis.** A new `networkName`, and no snapshot, sign log or record files of the old
  network.

## Status

This is new code. The pure layers are tested, the adversarial cases included, and the node runs
under `-race` with a chaos suite. It has **not** had an independent human audit, which is needed
before any deployment holds real value. Treat it as testnet-grade.
