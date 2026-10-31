# Governance

Area: **node**. Code: `ledger/parameters.go`, `ledger/governance.go` (`ParameterChange`),
`ledger/reassignment.go` (the ⅔ name action), and the pools in `node/governance.go` and
`node/reassignment.go`. The admin page that drafts and signs the votes is in `ui/admin.go`.

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
  within a second, whether it is shorter or longer.

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
- a `NewKey` has the exact size, is no name's key, and its proof verifies under it;
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
