# Signatures

Area: **signature**: the key, and the programs that sign with it. Since Phase 4 a bank's key can
live in the signer (`cmd/signer`), a program of its own on the bank's private network, and the
engine and the bank's wallet ask it for every signature. Code: `signing/`, `cmd/keygen/`,
`cmd/signer/`, and the engine's sign log, which lives in `node/persist.go`.

This doc covers the signature scheme, what every signature binds, keys and `keygen`, the engine's
sign log that keeps a node from signing twice, what a double-sign costs, and the signer: its
clients, its rules, its two logs, and what a break-in on each machine gets. How to run a signer is
in [operating-a-network.md](operating-a-network.md#running-a-signer).

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
Its one field is the node's name. That signature is never sent. The requestor signs in the domain of
its request's kind, so a signer that sees only the domain can tell a bank paying for a customer's
registration from a holder changing its own key. That is how the signer serves a bank's desk
`registration`, `name-request-bank` and `freeze`, and never `key-change` or `bank-change` (see
[The signer](#the-signer)).

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

Two more forms serve the signer:

- **`keygen -encrypt <key file> -passphrase-file <file>`** writes a fresh pair into a new key file,
  sealed under the passphrase that file holds, and prints the public key only. The key file has the
  wallet's format (`signing.KeyFile`): the public key, and the seed sealed with AES-256-GCM under a
  key that PBKDF2-HMAC-SHA256 derives from the passphrase in 600,000 iterations. It never replaces a
  file that is there, and refuses an empty passphrase file. The signer and the wallet both read it.
- **`keygen -secret`** prints a new client secret for the signer: 32 random bytes, as 64 lowercase
  hex digits, and nothing else.

With a signer, the key file is on the signer's machine only. The engine's config and the bank's
wallet file hold the public key alone.

## The sign log

This is the engine's sign log. A signer keeps one more, its own (see [Its two logs](#its-two-logs)).

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
another machine, or another `listen` address, is not caught. A signer catches more: two engines
that ask one signer get one signature per slot between them, and the other is refused as a
conflict. Nothing catches two signers that hold one key: run each key in one signer, on one sign
log.

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
  pair, or the new public key with a signer that holds the new key (see
  [operating-a-network.md](operating-a-network.md#a-bank-whose-key-leaked)).

Only consensus votes and proposals have a conflict slot. Two different payments, updates, name
requests or governance votes from one key are not evidence: wallets keep no sign log, and an honest
retry must never freeze a name. A stolen key is stopped by its holder's freeze instead.

## The signer

The signer, `cmd/signer`, holds a bank's one key, and is the only program that does. It runs on a
machine, or in a container, on the bank's private network, and accepts nothing from the internet.
The engine and the bank's wallet hold the public key only, and ask the signer for each signature.
Before Phase 4 the key was in two places, the engine's config and the bank operator's wallet, and a
break-in on either took it. An engine can still hold its key itself (`privateKey` in its config),
as `cmd/demo`'s node and the tests do, and a wallet in its key file.

### Its clients and their domains

A client is a program the signer answers. Its config lists each one: a name, a secret file of its
own, and the domains it may ask for. A domain not in the client's list is refused.

| Client | Domains | Why |
|---|---|---|
| `engine` | `block-revision` (votes), `proposal`, `receipt` | the engine signs nothing else |
| `desk` | `registration`, `name-request-bank`, `freeze` | the bank paying, as requestor, for its customer's registration; its co-signature as the bank; and its own freeze |
| `admin` | `parameter-vote`, `reassignment-vote`, `payment`, `account-update`, `freeze` | governance, and the bank's own money and records |

That is the layout of the Phase 4 spec (`review/phase4_spec.md`, §2.3). The signer takes any layout
its config writes, but for the new-key domains, and three points hold:

- **`key-change` and `bank-change` are in no list.** Their requestor is the name itself, so the bank
  signs one only to change its own key or its own bank, which no terminal should do. A bank's key
  changes by the ⅔ name action. The signer would take them in a list; no list should give them.
- **`name-request-new-key` and `reassignment-new-key` are never the bank's.** The new key signs
  them, as proof that its holder has it. The signer refuses a client list that names them.
- **`freeze` is in two lists on purpose.** A bank whose key may be exposed freezes its own name at
  once, from whichever terminal it reaches first.

Every client may also ask for `key-self-test`, the engine's signature at start (above). It has one
field, the bank's name, and the signer signs it for no other name: an engine whose config names
another bank stops at start, not at its first vote.

The quickstart and the compose file run one wallet per bank, the desk and the admin page in one, so
each signer there has two clients: `engine`, and `wallet` with the desk's domains and the admin
page's together (see
[operating-a-network.md](operating-a-network.md#four-banks-on-one-machine-the-quickstart)). A bank may
run them on two terminals, with two clients, to keep a desk terminal from paying or voting.

### How a client reaches it

The signer serves one route, `POST /sign`, on one address (`listen`). It refuses, before it reads a
byte, every connection from an IP address that is not in its `allowFrom` list, as the engine's
gossip port does. Each request carries the domain and the fields, and the header
`Authorization: HDLS-HMAC <client> <hex of HMAC-SHA256(client secret, body)>`. The route, its
body and its answers are in [api.md](api.md).

- **No TLS.** Nothing on the link is secret: the requests are messages the bank will publish, and the
  answers are signatures it will publish. The link needs to know who asks, which the HMAC tells, from
  the standard library, and safe against a quantum computer.
- **No replay guard.** Signing is deterministic, so a replayed request gets the signature its first
  answer carried, and nothing new comes out.
- **The client checks every answer.** The engine and the wallet verify each signature under the
  bank's public key before they use it (`signing.RemoteSigner`). A signer that holds another key, or
  another program that answers, is caught.
- **One request at a time.** The signer decides and signs one request, then the next, whichever
  client sends it. So the wallet's requests and the engine's votes wait for each other: a client that
  sends request after request can delay the bank's votes.

### Its rules

1. **It never signs raw bytes.** A request is a domain and the fields the ledger signs in it. The
   signer checks their number and form against the domain's layout (`cmd/signer/layout.go`), then
   builds the preimage itself, for the network its genesis file makes. A client cannot make it sign
   for another network, or outside a domain.
2. **It signs only in its own name.** Where the fields name who signs (the validator of a vote, the
   proposer, the node of a receipt, the requestor or the bank of a name request, the name of a
   freeze, the payer, the name of an account update), that name must be the bank's, the config's
   `name`. A governance vote names no voter, so for those the client's list is the only gate.
3. **One signature per slot.** A slot is a vote's height, round and type, or a proposal's height and
   round. The same request again gets the same signature. A request with other fields is refused,
   and written to the audit log with both requests, marked `REFUSED CONFLICT`: the engine is broken,
   or someone else drives it.
4. **Never below its own height.** A vote or a proposal below the highest height it signed is
   refused, but for a repeat of a slot its log holds.
5. **Log, then fsync, then sign.** It appends the slot and the fields to its sign log and fsyncs it,
   and only then signs and answers. The engine does it the other way round, sign then log, which is
   safe only because the engine's key never leaves its process.
6. **No slot for the rest.** Receipts, name requests, freezes, governance votes, payments and
   updates have no slot: two different ones are never evidence (above). The signer checks rules 1
   and 2 for them, and writes each to the audit log.

It does not check its key against the chain, which it does not read. The engine's key self-test
does, and every client checks every answer.

### Its two logs

- **The sign log** (`signLogPath`): one JSON line naming the network, then one per vote or proposal
  signed, with its slot and its fields. It keeps the slots of its highest height and the one below,
  and is rewritten at each start. Rules 3 and 4 read it. A signer with no sign log refuses every vote
  and proposal until it is started once with `-resume-above <height>`, which writes a new log that
  refuses every slot at or below that height
  ([operating-a-network.md](operating-a-network.md#running-a-signer) says when and with what height).
- **The audit log** (`auditLogPath`): one JSON line per answer, signature or refusal: the time, the
  client, the domain, a summary a person can read, such as `precommit at height 1, round 0, for block
  948fe493`, and the reason of a refusal. Nothing reads it but people. It is how a bank finds out,
  after the fact, what a stolen desk signed. A signature whose line can't be written is not given.

The engine keeps its own sign log as before ([The sign log](#the-sign-log)), with its lock and its
restore. The two guard different things: the engine's guards the lock, the signer's guards the key.
So the signer does not stop a fork on its own. A guard on the slot alone still forked in testing
(`review/post_emudhra_review_2026-10-27.md`, E2, pass 3), because a node can vote unlocked in a later
round. The engine and the signer can each lose their log without the other.

### The engine and the wallet through it

The engine names its signer in its config, in place of `privateKey`
([operating-a-network.md](operating-a-network.md#the-node-config)). It signs on its event loop as
before, through `signing.RemoteSigner`:

- **At start** it makes the key self-test through the signer and checks it under `publicKey`, so a
  signer that is down, holds another key, or serves another bank stops the engine before it joins.
  The engine needs its signer up when it starts.
- **Each request** gives up after a second. After a failure, every request fails at once for the
  next second, so a signer that is down does not slow the loop at every step. A vote that fails is
  dropped and cast again later, as after a failed sign-log write. A bank whose signer is down is a
  validator that does not vote: the chain goes on while the others hold more than ⅔.
- **The slot memory.** The engine remembers the fields it first sent in each slot, and sends those
  again rather than other fields: a request that timed out may have been signed. It is in memory
  only. After a restart, an engine that had a request time out may send other fields in that slot,
  and the signer refuses them as a conflict. That costs the vote, never a second signature.
- **One receipt at a time.** A payment that comes while another receipt is out gets `202` and no
  receipt, so a burst of payments never queues ahead of a vote. Its wallet then says "payment
  submitted" without the ✓.
- **No proxy.** The requests go straight to the signer, whatever `HTTP_PROXY` says.

A bank's wallet signs through the signer the same way, with `-signer-url`, `-signer-client` and
`-signer-secret-file`, and its wallet file holds the bank's public key only (see
[ui/README.md](../ui/README.md#a-banks-wallet-through-its-signer)). A customer's wallet keeps its
own key.

### The key at rest

The signer reads a key file that `keygen -encrypt` wrote, sealed, and the passphrase from a file of
its own (`passphraseFile`), never from its config. It reads both only at start. A plaintext key
file still works, for tests, and the signer logs that it is fine for a test, not for a bank. While
the signer runs, `crypto/mldsa` holds the key in its memory: keeping it off a running machine needs
hardware, a TPM or an HSM, which needs libraries outside the standard library.

### What a break-in on each machine gets

- **The engine**: votes and proposals, at most one per slot, and receipts. It cannot make the key
  sign two conflicting votes, so it cannot get the bank's name frozen for a double-sign. It can keep
  the bank from voting, as any broken engine can, and it can have votes signed for heights the chain
  has not reached: the signer then refuses every vote below them. The operator keeps the sign log
  and re-keys the bank through the ⅔
  ([operating-a-network.md](operating-a-network.md#a-break-in-on-the-engine)). Its receipts are the
  bank's word that it holds a payment, so a stolen engine can lie in receipts.
- **The desk**: co-signatures as the bank, registrations the bank pays the fee for, and a freeze of
  the bank's own name, never a vote, a payment or a governance vote. A co-signature alone changes no
  key: a key change also needs its holder and its new key. The freeze is the thief's to pull too:
  it takes the bank out of the set until the ⅔ re-key it.
- **The admin page**: what its domains allow, which is much: payments of the bank's money, updates
  of the bank's account (its public address, the IP addresses the others gossip to, its TLS keys),
  the bank's governance votes, which count toward a ⅔ certificate, and the freeze. Keep it on a
  terminal of its own, or in the same wallet as the desk only where one person holds both.
- **The signer itself**: the key, once its passphrase is read. That is why it accepts nothing from
  the internet, and why its passphrase stays off its disk in production.

The audit log shows, after the fact, what each client had signed.
