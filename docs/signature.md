# Signatures

Area: **signature**, the part that is to move into a signer container of its own. Phase 3 built the
other containers, but not the signer: the engine still holds the key. Code: `signing/`,
`cmd/keygen/`, and the sign log, which lives in `node/persist.go` today.

This doc covers the signature scheme, what every signature binds, keys and `keygen`, the sign log
that keeps a node from signing twice, and what a double-sign costs.

## The scheme: ML-DSA-65

The network has one signature scheme, ML-DSA-65 (FIPS 204), from Go's standard library
(`crypto/mldsa`). The code is `signing/mldsa65.go`. `signing.NetworkScheme()` is the one place that
chooses it, and no code outside `signing/mldsa65*.go` names ML-DSA; comments elsewhere give its sizes
only as the example.

| Item | Raw size | In base64 |
|---|---|---|
| public key | 1952 bytes | 2604 characters |
| signature | 3309 bytes | 4412 characters |
| private key, stored as the 32-byte seed the key derives from | 32 bytes | 44 characters |

- **Signing is deterministic** (`SignDeterministic`, no context string). The consensus driver needs a
  signature to be a function of what is signed. So every relayed copy of a vote is the same bytes,
  and a node drops a copy of a vote it holds before any check.
- **Verification refuses a wrong size first.** `mldsa65.Verify` refuses a key or a signature of any
  other size before any work, and never panics. Anyone can send a node a signature to check.
- **One spelling per key.** Keys and signatures travel as standard base64. Only the canonical
  spelling verifies (`signing.CanonicalBase64`), because the ledger compares keys as strings.
- **Every key that enters is one the scheme accepts.** `Scheme.ValidatePublicKey` refuses a key of
  the wrong size, one that does not decode, and a degenerate one, as the scheme defines it: a weak
  key it refuses, or a second encoding of a key. `Verify` refuses every key it refuses.
  `signing.ValidatePublicKeyBase64` checks the canonical spelling, then asks the scheme, and every key
  that enters goes through it: a genesis account's, the node config's, a key typed in the wallet, and
  the new key of a name request or a ⅔ name action. The genesis, the config and the wallet show the
  scheme's reason. ML-DSA-65 has no second encoding of a key, but it has weak keys, under which a
  signature with z = 0 and no hint verifies with no private key, and `crypto/mldsa`, like FIPS 204,
  accepts them all. `mldsa65.ValidatePublicKey` refuses the most degenerate: a key whose t1 has every
  coefficient 0 or 1023, under which anyone can sign every message, as
  `TestMLDSA65RefusesKeysAnyoneCanSignUnder` shows by forging a signature under each kind. It is a
  sanity check, not a guarantee. Other keys with small t1 coefficients let anyone sign many messages
  or all of them (one coefficient of 1 and the rest 0, or every coefficient 1: all 100 messages
  tried; every coefficient 2: about 80), and the scheme takes them, as the same test records. No short
  check covers that family, since whether the forgery works depends on the message; a key's holder
  can let anyone sign anyway, by publishing its seed; and honest key generation does not make such
  keys. Ed25519 has both weak keys and second encodings, the points of small order and the y
  coordinates of p or above, and an Ed25519 scheme refuses them
  (`review/_evidence_phase2/scheme-drop-in/ed25519.go`).
- **One implementation is a consensus rule.** Every node must accept exactly the signatures every
  other node accepts. Every node runs the same release, built on Go's `crypto/mldsa`. A build that
  selects the FIPS 140-3 v1.0.0 module, which lacks ML-DSA, does not compile
  (`signing/mldsa65_fips140v1.go`).
- **The scheme is fixed at genesis.** Changing it means a new network (see
  [operating-a-network.md](operating-a-network.md#starting-a-new-network)), built with the scheme's
  files replaced (README, *Cryptographic agility*).

Measured on the development machine: a signature takes about 0.9 ms, a verification about
0.25 ms, and deriving a key from its seed 0.36 ms. So a signer parses the seed once
(`Scheme.ParsePrivateKey`) and keeps the parsed key.

## What every signature binds

`signing.Preimage(networkID, domain, fields...)` is what gets signed: the protocol tag
`hypernode/v1`, the network id, the domain, then each field, each preceded by its length as 4 bytes
big-endian. So a field cannot shift into its neighbour, a signature for one network fails on
another, and a signature in one domain fails in every other. `signing.Signer`, the only signing
API the node and the wallet use, has no "sign these raw bytes" call: `Signer.Sign` takes a domain.
The scheme's `PrivateKey.Sign` signs whatever bytes it is given; only `Signer.Sign` calls it, with
a preimage that carries the domain.

The network id is the effective network id, the network's name, a `/`, and the hex of the genesis
hash (see [operating-a-network.md](operating-a-network.md#the-network-id)). So a signature counts on
one genesis only.

| Domain | Signed by | Over |
|---|---|---|
| `payment` | the payer | a payment |
| `account-update` | the account | an account update |
| `registration`, `key-change`, `bank-change`, `freeze` | the requestor | a name request of that kind |
| `name-request-bank` | the co-signing bank | a name request |
| `name-request-new-key` | the new key, as proof that its holder has it | a registration or a key change |
| `reassignment-vote` | a validator | a ⅔ name action |
| `reassignment-new-key` | the new key, as proof that its holder has it | a ⅔ name action |
| `parameter-vote` | a validator | a parameter change |
| `block-revision` | a validator | a consensus vote |
| `proposal` | the proposer | a signed proposal |
| `receipt` | a node | a payment receipt |

`cmd/server` also signs once at start, in the `key-self-test` domain, to prove its key pair works.
That signature is never sent. The requestor signs in the domain of its request's kind, so a signer
that sees only the domain can tell a bank paying for a customer's registration from a holder
changing its own key. The signer, still to come, is to serve a bank's desk only `registration`,
`name-request-bank` and `freeze`.

A message's id hashes its signed fields, never its signature (`ledger.PaymentID`,
`ledger.UpdateID`, `ledger.NameRequestID`, `ledger.ReassignmentID`). Pools and relays key on it.

## Keys and keygen

A key is the current credential of a name. The name is the identity: messages name their signer,
and nodes look the signer's key up by name in the state. A key in the state is the current key of
exactly one name.

`keygen` prints a fresh pair: the public key, then the private key, which is the seed.

```
$ ./bin/keygen
publicKey:  76kiA96VYFo6sp0MhkT+rh97sh/5hUYTEE8vSYCUhpWv/qYY…   (2604 characters)
privateKey: 6KAw6+2LUXVFz4FVvvpX2Lx/lpKyXKfVd1+xu7aAORQ=
```

Run it on the machine that will hold the key. Every key, here and in the wallet, comes from
`crypto/rand`. A node takes both halves in its config; `cmd/server` derives the public key from
the seed, refuses a pair that does not match, and makes one test signature
([operating-a-network.md](operating-a-network.md#the-node-config)). The wallet makes its own key,
or holds a pair from `keygen` written into its key file before its first start
([operating-a-network.md](operating-a-network.md#generate-keys)). A private key that is not a
32-byte seed, such as an earlier network's 64-byte key, is refused.

## The sign log

A validator writes what it signs at the height in progress to its sign log,
`<snapshotPath>.votes`, before the vote is counted or sent, and before its proposal is sent. The
write is synced to disk and renamed into place. A vote or proposal that is not on disk is never
released. The log holds the network id, the height, the node's own votes and proposals, the
proposal it is locked on, and, for a node back without its earlier log, the height up to which it
may have signed before (below).

- After a restart at the same height, the node resumes from the log (`consensus.Driver.Resume`):
  the same votes, the same lock, the same round. It re-sends a logged proposal and never signs
  another block for that round, even one that differs only in its nonce.
- Once the snapshot of height h is on disk, a node whose log holds h writes an empty log for h + 1
  (`Node.commit`). So while the chain idles, the sign log holds no block, no payment and no nonce.
  A node that caught up past a height it signed in empties that log too, once the new snapshot is
  on disk (`Node.emptySignLog`). After `Node.catchUp` the empty log is for the height it starts,
  unless the node waits there (below). After `Node.adopt` it keeps its old height, so the node
  still waits before it signs at the adopted height. `Open` empties a log left below the height
  the node restarts at, as a stop between the two writes leaves one, at the log's own height
  (`Node.loadSignLog`); the node then waits too.
  The snapshot still keeps the last block's header, its nonce included, which the checkpoint
  serves. The node also keeps the last decided proposal, its block included, in memory until the
  next commit or catch-up, to re-send the last commit.
- A node back without the log of the height it starts at (a dead disk, an older backup) may have
  signed there before. It signs nothing there for 2 × max(`stepTimeoutMs`, `minimumStepTimeoutMs`)
  (`Node.ownSignaturesWait`): two re-gossip intervals with the slow-down switch off, so that its
  peers' re-gossip, once an interval, brings its own earlier votes and proposal back first; and
  twice the switch when the switch is longer, so that in pigeon mode the next carry brings them.
  Then it resumes from them
  (`Node.waitForOwnSignatures`). A restart that keeps its sign log never waits, so a long switch
  delays only a node that lost its log. It waits so at every height it may have signed at: the
  one it starts at; each later one it commits into, up to the highest at which a vote or proposal
  of its own, verified under its key, comes back to it (`Node.noteOwnSignature`); and the network's
  height, where a catch-up from one of those takes it. While it waits at a height, its log stays at
  the log's own height, so a restart during the wait waits too; its first new log at a height holds
  the votes and proposal it took back there, so a restart that starts from that log keeps them too
  (`Node.heightSignLog`). Once it has a log, the log keeps the height up to which it may have
  signed, written again whenever that height rises, so a restart keeps it, even one that finds a
  log for its height and starts there at once; a restart before its first log waits at the height
  it opens at and learns that height again. It is best effort, by timing, and recovers only what
  running peers still hold and re-gossip during it: the votes of the height they are at, their
  current round's proposal, of any round up to 256, and the commit, precommits and proposal, of the
  height just below theirs. So it can still sign against its own earlier votes and proposals at a
  height two or more below its peers' (the one it starts at, say), where it signs after the wait,
  before it catches up; against those at the height just below theirs that the commit lacks, its
  precommit for that height's decision among them; and against its proposal of a round its peers
  have left. A node with no snapshot starts at genesis, with no wait at all.
- `Open` refuses a log of another network, a log ahead of the snapshot, and a log whose votes or
  proposals do not verify.
- A node signs votes and proposals only at heights where its name is seated with exactly its
  configured key (`Node.seatedSigner`). Otherwise its driver has no signer: it follows the rounds
  and decides, but signs and logs no vote or proposal. If its name is seated under another key, the
  node logs that once, when it next starts a height. Every node, seated or not, signs a receipt,
  with its configured key, for each payment a wallet posts to it (`/hdls/pay`) that it newly pools
  (see
  [api.md](api.md#routes-that-take-messages-post-json)).

The sign log keeps only its own node from signing twice. Run a validator's key on one node, one copy
at a time: no hot standby, and no copy of its data folder running elsewhere. Two nodes with one key
sooner or later sign two different votes or blocks for one slot, and that double-sign freezes the
name. `cmd/server` opens its port before it opens the node, so a second start of the same config on
the same machine, while the first runs, stops before it signs. That catches only this one mistake:
another machine, or another `listen` address, is not caught.

The operator's rules for the log (never delete it, never restart from an old backup) are in
[operating-a-network.md](operating-a-network.md#rejoining-after-a-restart).

## What a double-sign costs

A double-sign is two signatures by a name's current key for different block hashes in one slot:
two votes of the same height, round and type, or two proposals of the same height and round. A nil
vote has an all-zero block hash, so a nil vote and a vote for a block in one slot are a
double-sign. Two blocks that differ only in their nonce are two blocks. A node that sees both
signatures, in the places [consensus.md](consensus.md#equivocation-freezes-the-name) lists, keeps
them as a proof, and a later block carries it. That block freezes the name: it leaves the validator
set, keeps its balance, and only the ⅔ name action unfreezes it, with a new key. The proof counts
while the convicted key is the name's key, and only while the frozen names hold under a third of
the power.

For a bank, the freeze costs more than its seat:

- A frozen name neither pays nor is paid.
- Freezing a bank clears every pending change it co-signed: its customers' key changes, and bank
  changes to it. Those customers file again, and their fees are not refunded.
- After the ⅔ re-key, the node signs votes and proposals again only once its config holds the new
  pair (see [operating-a-network.md](operating-a-network.md#a-bank-whose-key-leaked)).

Only consensus votes and proposals have a conflict slot. Two different payments, updates, name
requests or governance votes from one key are not evidence: wallets keep no sign log, and an honest
retry must never freeze a name. A stolen key is stopped by its holder's freeze instead.

Still to come is the signer: a container of its own that holds the key, refuses a second, different
signature for the same height, round and type, and writes that to disk before it signs (see
[TODO.md](../TODO.md)). Until then the engine holds the key, and its sign log does this job.
