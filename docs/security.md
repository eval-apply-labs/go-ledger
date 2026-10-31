# Security model

Area: **general**: the threat model spans all four areas. `go_ledger` is a Go port of the Java
Hypernode ledger (see the [README](../README.md#origins)), hardened as it was ported. The review
rounds are in [`findings.md`](../findings.md) and [`findings_20261027.md`](../findings_20261027.md).
This doc states the threat model, what the design fixes by construction, the limits of the freeze
and of the banks, and what a deployment is still responsible for.

## Threat model and target

The target is a **small, known-identity, high-trust community** running its own currency on a
handful of machines, for example 8 laptops on an isolated intranet.

- Participants are known and socially accountable; the adversary is not an anonymous global
  attacker.
- Value is low to moderate; the network is small and mostly in one place.
- The realistic threats are bugs that corrupt the ledger with no attacker, one dishonest or
  compromised validator, anyone on the LAN, a stolen key, operator mistakes, and unreliable
  hardware (laptops sleeping, rebooting, dropping off).

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
- **Bounded input.** Request bodies are read up to 1 MiB (2 MiB for a proposal), a peer's
  checkpoint up to 64 MiB, which holds about 23,000 accounts: a larger network cannot catch up or
  join by checkpoint until the bound is raised (see [api.md](api.md#one-open-port)). Amounts are
  checked as text before they become numbers, so a few bytes cannot become a huge number.
  Signatures of the wrong size are refused before any work.
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
  chain to the rest. Nodes that lag then need a snapshot from a node you trust (see
  [operating-a-network.md](operating-a-network.md#rejoining-after-a-restart)), and wallets that
  pinned it need a new list.
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
- A wallet sees the network through one node. A bank that colludes with a thief can hide a pending
  change from its customer if the customer's wallet reads the bank's node. The wallet refuses a
  state older than one it verified, reports a freeze that did not land, and the bank's desk lists
  its customers' pending changes. Reading a second node is not built.
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

The record routes are public on purpose and unauthenticated. A request with `from` reads at most
two unit files of 100 heights: in static mode about 3.7 MB with 4 validators and 14 MB with 21; in
dynamic mode, where most entries carry the set, about 5.8 MB and 26 MB. A request without `from`
also lists the record folder, about 6,000 files at the default retention. See
[api.md](api.md#the-record-routes).

## The deployment is responsible for

- **Keeping private keys private.** Keys are made by `keygen` or the wallet on the machine that
  holds them, and never sent. Never put a private key on a network endpoint.
- **Keeping the network off the public internet.** The node API has no TLS, no authentication and
  no rate limit. Signatures make votes and blocks trustworthy whoever relays them, but anyone who
  reaches the port can call any route as often as they like. Use an isolated LAN or a VPN.
- **Provisioning for liveness.** Finalisation needs > ⅔ of the power online. On flaky hardware, put
  enough power on the machines you can keep awake. A partition that leaves no side above ⅔ stalls;
  it cannot fork or mint, and it recovers once the partition lifts, since nodes running a height
  re-send its votes.
- **Persistence, the sign log and backups.** A validator must set `snapshotPath`, keep its sign log,
  run its key on one node only (see [signature.md](signature.md#the-sign-log)), and never restart
  from an old backup. Keep an off-machine export of the state too. See
  [operating-a-network.md](operating-a-network.md#rejoining-after-a-restart).
- **A clean new genesis.** A new `networkName`, and no snapshot, sign log or record files of the old
  network.

## Status

This is new code. The pure layers are tested, the adversarial cases included, and the node runs
under `-race` with a chaos suite. It has **not** had an independent human audit, which is needed
before any deployment holds real value. Treat it as testnet-grade.
