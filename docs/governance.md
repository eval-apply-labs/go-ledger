# Governance

Area: **node**. Code: `ledger/parameters.go`, `ledger/governance.go` (`ParameterChange`),
`ledger/reassignment.go` (the ⅔ name action), and the pools in `node/governance.go` and
`node/reassignment.go`. The admin page that drafts and signs the votes is in `ui/admin.go`. The
names as a name service: `ledger/account_update.go`, `ledger/address.go` and `node/zone.go`.

Validators holding more than ⅔ of the power can do two things that no single holder can: change
the network's parameters, and act on one name (its balance, its key, its bank of record, its frozen
mark). Each is a certificate of signed votes, carried in a block.

What the ⅔ cannot change is the validator rule, which the genesis file sets and the genesis hash
covers: in static mode the list of validators and their powers, in dynamic mode the
`validatorThreshold`. Changing it takes a new genesis (see
[operating-a-network.md](operating-a-network.md#starting-a-new-network)). The ⅔ can still freeze a
validator's name, which takes it out of the set, or give it a new key; in dynamic mode, a balance
they change also changes the stake it backs.

## The rule: ⅔, not ½ + 1

A governance action needs votes from validators of the current set holding a strict > ⅔ of the
power, the quorum of block finality (`ValidatorSet.Quorum`). By quorum intersection, a third or less
can neither force an action nor ratify two conflicting ones. A third or more can block one, which
for governance is a feature.

## The governance sequence: each certificate is single-use

`LedgerState.GovernanceSequence` counts the governance actions applied. Every vote binds the
sequence it amends; `ApplyBlock` applies a certificate only while that sequence is current, then
adds one. So a certificate applies once, even if the state later cycles back to what it was. Both
kinds share the sequence, so a block carries at most one governance action.

## The parameters

`ledger.LedgerParameters` are part of the committed state and its digest. The genesis file sets
them (keys in lower camel case); a parameter change replaces all ten at once.

| Parameter | Bounds (`LedgerParameters.Acceptable`) | Meaning |
|---|---|---|
| `transactionCost` | ≥ 0 | burned from each payment |
| `blockIntervalMs` | 0 to 86,400,000 (one day) | the wait after a commit before the next height starts |
| `stepTimeoutMs` | 1 to 86,400,000, and at least 250 ms per gossip hop | the base consensus step timeout |
| `maxPeers` | 0 to 1,000 | the gossip fan-out; 0 is all-to-all |
| `maxMessageBytes` | 0 (no bound) or 5,000 to 1,048,576 | the largest JSON of one payment or account update |
| `newKeyFee` | > 0 | the fee of a registration, a key change or a bank change |
| `changeDelayBlocks` | 1 to 2,592,000 | how long a key or bank change waits |
| `retainBlocks` | 1,000 to 315,360,000 | how long the signature record keeps a block |
| `maxPaymentAmount` | > 0 | the largest single payment |
| `maxAccountBalance` | > 0 | the largest balance a payment or the ⅔ may raise a name to |

Keep `maxPaymentAmount` at or above `transactionCost`. Nothing refuses a lower value (owner,
2026-10-28), and then no payment can pass until a vote raises it.

- **The timing profile is coupled.** The step timeout must cover the gossip fan-out: at least
  250 ms (`AssumedGossipHopMs`) times the topology's dimension for the validator count and
  `maxPeers` (`ledger.GroupParameters`). A ratified typo can hold blocks back by a day per step at
  most, never for good.
- **When a change takes effect.** From the next block. A lowered `maxAccountBalance` applies from
  the block after the one that lowers it, and a balance already above it may stay and shrink. A
  shorter `changeDelayBlocks` affects only requests filed after it. A longer `retainBlocks`
  applies at once, to old records too; a shorter one only to records written after it, so the ⅔
  cannot vote away the record of their own votes.
- **What follows the governed values.** The step timeouts, the block interval, the re-gossip
  interval, the wait of a node back without its sign log, the catch-up interval, and the fan-out of
  peers resolved from the chain. A running node's re-gossip and catch-up loops take a new value
  within a second, whether it is shorter or longer. A node's slow-down switch,
  `minimumStepTimeoutMs` in its config, can make its round timers longer than the governed step
  timeout, never shorter, and the wait of a node back without its sign log with them; it changes
  nothing else (see [consensus.md](consensus.md#the-timers-and-the-slow-down-switch)).

## Parameter change

`ledger.ParameterChange` holds `NewParameters`, `PreviousParametersDigest` (the parameters it
amends), `Sequence` and `Votes`. Each vote (`ParameterVote`: a validator's name and a signature in
the `parameter-vote` domain) signs the ten new parameters, the previous digest and the sequence.
`ParameterChange.Valid` accepts it only if the sequence and the previous digest match the state,
the new parameters are acceptable for the current validator count, and the votes come from distinct
names of the set, each verifying under the set's key for that name, with power reaching the quorum.

## The ⅔ name action

`ledger.Reassignment` acts on one name: `Name`, `PreviousAmount` and `NewAmount`, `NewKey` with its
`NewKeySignature` (the new key's proof that its holder has it), `Bank`, `Freeze`, `Unfreeze`,
`Sequence` and `Votes`. Each vote, in the `reassignment-vote` domain, signs every field but the votes
and the proof. It binds the name and the sequence, never the name's current key, so a key change
that lands first does not make it stale.

It is valid (`Reassignment.FitsState`, `Reassignment.Valid`) only if:

- the sequence is current, and the votes reach the quorum as above;
- the name is canonical, and `Freeze` is not set with `Unfreeze` or `NewKey`;
- the two amounts are both null or both set; if set, `PreviousAmount` is the name's balance (0 for
  a new name), `NewAmount` is not negative, and it raises no balance above `maxAccountBalance`;
- a `NewKey` is a key the network's scheme accepts, in its one spelling, is no name's key, and its
  proof verifies under it;
- `Bank` is `""` (leave it), `-` (none), or another name that exists and is not frozen;
- for a name no account has: it sets a key and a balance, and neither freezes nor unfreezes;
- for a frozen name: it unfreezes exactly when it gives a new key, so every unfreeze re-keys;
- for a name that is not frozen: it does not unfreeze.

A node also refuses an action that alone would leave no name seated.

Its effect, in the block that carries it: a `Freeze` applies with the block's other freezes. The
rest applies after them: a new name is created with the new key, the new balance and the bank, and
backs no one; otherwise the new balance, bank and key replace the old ones, and `Unfreeze` clears
the frozen mark. Any pending change of the name is cleared, so a day-old key change cannot undo the
⅔ decision. The name sends, receives and requests nothing else in that block: its payments for that
block lapse, so the target cannot pay the money away first.

Uses: re-key a name whose key was stolen or burned (the holder freezes first, the ⅔ re-key and
unfreeze); move a name to a new holder; give a name a bank of record; mint into a treasury or claw
a balance back. It is the only way to create money (see
[managing-the-currency.md](managing-the-currency.md)).

## Collecting the votes

Votes are collected off the network. Each validator drafts the action in its own wallet's admin
page, checks what it binds, signs it, and hands the signed vote, as text, to whoever collects them.
An action with a new key goes to the new key's wallet first, which signs the proof; the validators
then sign the text it hands back. Once votes holding more than ⅔ of the power are in hand, the
collector submits the certificate once, to `POST /hdls/parameter-vote` or
`POST /hdls/reassignment-vote`. The route answers `202` once the node pools it, or `409` with the
reason it refuses it.

A node pools and gossips only a complete certificate, valid against the current state. So nobody
can fill the pools (64 certificates of each kind) with actions that never enact, and a seizure's
target learns of it only once it can enact. The node leaves out the votes of validators who have
left the set first. A copy with the same votes as a pooled certificate is dropped before any
signature check. A certificate with other votes for the same change replaces the pooled one, is
relayed, and wakes an idle chain. After each commit or catch-up the pools drop what no longer
applies. Pooled certificates are verified again at every height, and `ProposeBlock` checks the one
it carries.

A certificate has only bounded fields, so `maxMessageBytes` does not apply to it: a parameter change
is about 0.5 KB plus 4.4 KB per vote, a name action about 7 KB plus 4.4 KB per vote.

`GET /hdls/governance` shows the parameters in force, the validators, the quorum, and the pooled
certificates with their voters' power. The signature record keeps every governance certificate for
the life of the chain, with each signer's name, key and power (see
[architecture.md](architecture.md#the-signature-record)).

## Names as a name service

The names on chain are a name service: each name publishes where it can be reached, and signs what
it publishes.

| A name publishes | Who changes it |
|---|---|
| its key | a key change, which the holder, its bank and the new key sign; or the ⅔ name action |
| its bank of record | a bank change, which the holder and the new bank sign; or the ⅔ name action |
| its public address: `http(s)://host[:port]`, its public face, such as a bank's CDN name or any name's website | the name alone, by an account update its key signs |
| its IP addresses: at most 4, IPv4 or IPv6, what DNS A and AAAA records would hold | the name alone, by an account update its key signs |
| its TLS keys: at most 2, each the SHA-256 of a TLS key's SubjectPublicKeyInfo, as 64 lowercase hex digits, the hash DANE's TLSA records call "3 1 1" | the name alone, by an account update its key signs |
| its description | the name alone, by an account update its key signs |

The genesis sets each of them for the names it creates. In an update, the IP addresses and the TLS
keys are each one list: an empty list leaves them, `["-"]` clears them, and any other list replaces
the whole list. A third TLS key, a key in another form, or one given twice is refused.

- **Only the name signs its addresses and its TLS keys.** No bank co-signs them, and the ⅔ name
  action does not set them. A frozen name sends no update, so they stay as they were. A key change,
  a sale included, keeps them: the new holder changes them.
- **Two TLS keys** let a name roll its web server's key without a gap: the current one and the
  next. A name's payment key should never be its TLS key: a web server's key sits on a machine
  facing the internet, and a payment key must not. The TLS key's algorithm is the web server's
  choice, not the chain's. A wallet takes a bank's web server, at the bank's `https://` public
  address, only with one of the TLS keys the bank lists, and asks no certificate authority; only a
  bank's record counts (see [ui/README.md](../ui/README.md)).
- **A validator's IP addresses do two more jobs.** The other validators gossip to them, on the
  network's gossip port, and the gossip port's allow list lets in connections from them (see
  [api.md](api.md#two-ports-and-the-public-face)). So the chain is the network's routing table, and
  no node looks a name up in DNS.
- **An address is a claim.** Nothing on chain checks that a name controls the IP addresses it
  lists, and two names may list one. The lookup from an address to names tells who *claims* it, not
  who uses it, and shows every claimant. A rule of one name per address would let the first claimant
  squat on someone else's, so there is none. When an address floods a bank, its names are who to
  call: the bank of record calls its customer, and if that fails the ⅔ freeze the name. Nothing bans
  anyone on its own (see [operating-a-network.md](operating-a-network.md)).
- **Where to read them.** `GET /hdls/resolve?name=` on a bank's operator port, or the files
  `/hdls/resolve/name/<name>`, `/hdls/resolve/key/<SHA-256 of the key's bytes, in hex>` and
  `/hdls/resolve/ip/<address>` on any bank's public face, and its zone file, `/hdls/zone` (see
  [api.md](api.md#the-public-face)). Each answer is a bank's word: a reader that trusts no single
  bank asks several, or checks the record against a checkpoint, which carries its ⅔ commit.
- **Public for good.** What a name publishes is in every node's state, every checkpoint and every
  account list. An update replaces it on chain, but does not take back what anyone copied
  meanwhile.

**The zone.** An engine whose config sets `zone`, such as `hdls.example.org`, writes the names'
records into its public folder as a zone file, `hdls/zone`, in the standard text format of DNS (RFC
1035) that any DNS server (NSD, Knot, BIND, CoreDNS) loads as an authoritative zone. A DNS operator
fetches it from any bank's public face, or from several and compares them. It holds:

- the SOA record, whose serial is the height at which the engine last wrote the file, modulo 2³²,
  and an NS record for each of the config's `zoneNameServers`;
- for each name that is not frozen, in name order: an A or AAAA record for each IP address it lists;
  a TXT record `"hdls-public-address=<its public address>"`; and, when its public address is an
  `https://` one, a TLSA record `3 1 1 <hash>` for each TLS key, under `_<port>._tcp.<name>`, the
  port of that address, 443 without one;
- `validators-of-the-height-in-progress`: an A or AAAA record for each IP address of each validator
  of the height in progress, once each, loopback addresses left out. It is the IP filter: a firewall
  that takes DNS names in its rules, or a script that resolves the name every minute, keeps an allow
  list without reading the chain. The label is 36 characters, more than a name may have, so no name
  can take it;
- `<hex>._ip`: for each IP address a name lists, frozen names included, one TXT record with the
  names that list it, one string each, in name order, a frozen one as `"<name> (frozen)"`. The label
  is the address's bytes in hex, 8 digits for IPv4 and 32 for IPv6: a name cannot publish real
  reverse DNS for an address it does not own, so the lookup lives in the zone. Past 100 names, a
  last string says how many more there are and names the `hdls/resolve/ip/<address>` file that lists
  them all.

```
$ORIGIN hdls.example.org.
$TTL 60
@ SOA ns1.example.org. hostmaster.example.org. 1043 300 60 86400 60
@ NS ns1.example.org.
bank-a A 203.0.113.10
bank-a TXT "hdls-public-address=https://bank-a.example.org"
_443._tcp.bank-a TLSA 3 1 1 a1a1…a1
validators-of-the-height-in-progress A 203.0.113.10
cb00710a._ip TXT "bank-a"
```

- **Anyone can check it.** The file follows from the committed accounts, the validator set and the
  zone settings, so anyone can rebuild it from the public `hdls/accounts` and compare. Two banks
  with the same zone settings write files of one state that match but for the serial. A commit that
  changes no record leaves the file and its serial as they are, so a DNS server reloads the zone
  only when it changed.
- **What it is not.** The engine runs no DNS server and signs no zone: an operator signs the file
  with their own DNS tools if they want DNSSEC. A TLSA record counts for a DANE client only in a
  zone signed so. A TLSA record under `_443._tcp.bank-a` speaks for the host `bank-a.<zone>`, whose
  A and AAAA records are the name's IP addresses, its gossip addresses for a validator, which need
  not be its web server's. Running the zone, the DNS server's settings and where to delegate it are
  in [operating-a-network.md](operating-a-network.md).

## Extending it

To govern another value:

1. Add the field to `LedgerParameters` and to `fields()`, which feeds both the digest and the vote.
2. Give it bounds in `Acceptable`.
3. Add it to the genesis file's keys, `genesisParameterNames` in `ledger/genesis.go`.
4. For an amount, add it to `LedgerParameters`' JSON methods in `ledger/amount.go` and to
   `ParseGenesis`'s list of amount parameters.
5. Show it in `node.ParametersView` (`node/governance.go`) and on the admin page: `ui/admin.go`,
   the form reading in `ui/main.go` (`proposeParameter`) and `ui/templates/admin.html`, so
   validators can draft a vote on it in their wallets.

It is then governed by the same certificate. Without step 5, only a hand-built certificate can
change it. Changing the fields changes the digest and the signed preimage, so it needs a new
genesis.
