# go_ledger — a distributed ledger in a single static binary

[![CI](https://github.com/eval-apply-labs/go-ledger/actions/workflows/ci.yml/badge.svg)](https://github.com/eval-apply-labs/go-ledger/actions/workflows/ci.yml)

A Byzantine-fault-tolerant, delegated-proof-of-stake ledger with a consensus-backed **naming
registry**: pay people by name, resolve names on-chain, and run the whole thing — validator node and
wallet — from one `go build`, with **no runtime and no third-party dependencies**. Tendermint-style
⅔ commit safety, exact-rational money, self-amending governance, and a browser wallet, in a codebase
small enough to read. It descends from the original Hypernode ledger (see [Origins](#origins)) but
stands on its own.

## Try it in one command

    make demo

Brings up a self-contained network on this machine — a single-validator node with funded, named
accounts (founder/bakery/cafe) plus the wallet + admin UI — and prints the URL. Send a payment by
*name*, register your own name, or vote a fee change into effect, then watch it commit. Ctrl-C to stop.

![The browser wallet against a running local network](docs/wallet.png)

![The admin / governance page: live parameters, vote tallies, and balance reassignment](docs/admin.png)

## Design goals

- **No JVM, no dependencies** — a single static binary per command, trivially deployable on scavenged
  / older hardware (the initial target is a handful of laptops on a local intranet).
- **Small and auditable** — the domain core is a pure, dependency-free package with no I/O,
  crypto or networking mixed in, so it can be read and tested in isolation.
- **Separate executables, separate codebases** — the **ledger node / server** and the **wallet
  UI** are independent modules/executables so each stays small and auditable. The wallet lives
  under [`ui/`](ui/) (its own Go module) for now and can be split into its own repo unchanged.

## Status

A runnable, race-free multi-node ledger — the **Go port of the Java Hypernode engine** (see
[Origins](#origins)), re-implemented one layer at a time and pinned by tests. Porting was also a
chance to harden it: the ledger has been through **several rounds of review** — a from-the-start
security audit (worked through in passes) and a release-candidate pass over the naming/DNS work — all
recorded in [`findings.md`](findings.md), and their fixes are baked in below.

- [x] `ledger` — account state, payment application, state digest, and block application, with
      the audited supply-integrity fixes in from the start (B1/B2).
- [x] `signing` — domain-separated signatures (Ed25519); cross-domain / cross-network /
      field-injection replay rejected by test (audit C1/C6/C7).
- [x] `consensus` — ⅔ commit safety core (A1/A2/A4/C5) plus the round driver (locking, timeouts).
- [x] `node` — race-free actor-model validator: event loop, mempool, block production, HTTP
      transport + node API. Four nodes reach consensus over HTTP in tests (under `-race`).
- [x] `cmd/server` + `cmd/keygen` — a config-driven node binary and a key generator.
- [x] onboarding / rejoin sync — a node with `bootstrap` peers fetches and certificate-verifies the
      committed state and fast-forwards, so a restarted node rejoins at the current height.
- [x] snapshot persistence — a node with `snapshotPath` persists its committed state after every
      block and reloads it on restart (recovers a whole-network outage with no peer to sync from).
- [x] payment gossip + replay protection — payments flood to peers so they reach the proposer, and
      each is bound to the exact block it names (a resubmitted, cleared payment is stale and never
      re-applied). Submitting to a node returns a signed receipt.
- [x] stake-weighted proposer — a validator proposes proportionally to its voting power.
- [x] Hypernode dimensional gossip (`maxPeers`) — auto-scaled Hamming-graph topology + relay, so
      fan-out is ~d·(k−1) instead of N−1 while gossip still reaches everyone; all-to-all by default.
- [x] dynamic validator admission (`validatorThreshold`) — the validator set is derived from account
      stake each block, so a funded account crossing the threshold joins automatically.
- [x] dynamic-set onboarding — a joiner adopts a checkpoint whose validator set grew while it was
      away, under the BFT skipping-trust rule (>⅓ of a trusted anchor set co-signed the commit).
- [x] straggler catch-up — a running node that missed a commit window polls peers and fast-forwards.
- [x] f+1 higher-round skip — a round-lagging node skips forward on >⅓ power from a higher round.
- [x] off-machine backup (`backupPath`) — each committed checkpoint is mirrored to a second path.
- [x] fork-binding tier (optional `PrevStateDigest`) — pin a payment to one chain history; for
      self-hosted high-value users (run your own node), while `BlockID` alone suffices for everyone else.
- [x] payment identity by content, not signature — a malleated signature can't double-spend (mempool,
      gossip, and `ProcessPayments` all dedup by `PaymentID`).
- [x] resource bounds — mempool size ceiling + payment comment cap (`maxMempool` / `maxCommentLength`),
      plus a governed per-message serialized-size cap (`maxMessageBytes`), so one transaction fee can't
      write a megabyte into permanent, every-block-rehashed state. A message that is oversized — or
      resolves to nobody (an unregistered name) — is a zero-fee no-op and so is kept out of a block
      entirely (refused at intake, excluded by the proposer, rejected on apply), not carried for free.
- [x] slashing — equivocation proof carried in a block (`Block.Evidence`) removes the double-signer's
      account (confiscates stake, ejects from a dynamic set); unforgeable, so it can't frame the honest.
- [x] overdraw intake guardrail — a payment that would overdraw the payer (counting its already-queued
      outgoing) is refused at intake with no receipt, at the client endpoint and the gossip ingress
      alike (`node.admitPayment`), so an honest wallet can't trip the ledger's overdraw ban; a
      committed overdraw then implies only a misused/cloned key or a proposer that bypassed intake.
- [x] vote re-gossip — each node periodically re-broadcasts the current height's votes (`reGossipLoop`),
      so a partition that drops every side below quorum self-heals on merge instead of freezing.
- [x] adversarial test harness — a deterministic driver-level simulator (`consensus/sim_test.go`) and a
      fault-injecting node-level chaos suite (`node/chaos_test.go`): partitions, message loss / dup /
      reorder, equivocators and byzantine agents, with *no fork* and *no silent mint* held as invariants
      across thousands of randomized, replayable schedules.

The distinctive engine — a consensus-backed **naming/DNS service with payments underneath** — is
restored on top of the hardened core:

- [x] naming registry — a signed `AccountUpdate` claims a unique name (a "domain"), re-delegates
      stake, or sets a description; committed in blocks, first-come-unique.
- [x] payment by name — the signed payment carries the *name*; every node resolves it against the
      committed registry independently (as the original did), so no single node can remap it. A name
      that resolves to nobody (a typo) is refused with a clear error, not silently accepted as a no-op.
- [x] on-chain node addressing — validators publish a gossip address on-chain; nodes resolve their
      peers from committed state (the self-describing peer DNS) rather than a static list.
- [x] name-server endpoint — `GET /hdls/resolve?name=alice` returns the name's public key AND its
      published address from committed state, each answer anchored to the committed digest so
      clients can cross-check independent nodes: a decentralized replacement for a name server.
- [x] catch-up from gossip peers — any node that falls behind self-recovers from a peer that's ahead.
- [x] supply-conservation property test — payments can never mint (value only moves or burns as fees).
- [x] governance — self-amending parameters (currently the transaction cost) by a > ⅔ validator
      vote, carried in a block as a replay-guarded change certificate: see [`docs/governance.md`](docs/governance.md).
- [x] balance reassignment — a > ⅔-ratified override of one account's balance (the community's
      deliberate exception to payment-conservation: it can mint or reduce), same certificate shape,
      replay-guarded by the balance it amends, with a non-negative guard.
- [x] admin / governance UI — a page in the wallet to propose and vote on transaction-cost changes and
      balance reassignments, with live vote tallies (reads `GET /hdls/governance`).

The port is complete and reviewed. What's left is scope beyond a testnet-grade node: an independent
**human** audit (the real gate before value rides on it — an AI review is an input, not a substitute);
a remote/airgapped validator signer (fits behind the `signing` abstraction); and PoS operating
realities to *understand* rather than code — weak subjectivity for long-range attacks, stake
concentration vs the ⅓/⅔ thresholds. The original Java implementation is upstream and out of scope.

See [`docs/`](docs/) for the architecture, consensus design, security model, how to run a network, and
— just as important as the code — [**managing the currency**](docs/managing-the-currency.md): the
monetary policy of a burn-and-mint community currency, and the honest limits of it.

## Layout

    cmd/server/   the node binary (config-driven)
    cmd/keygen/   Ed25519 key-pair generator
    ledger/       pure domain core: Account, Payment, ProcessPayments, StateDigest, Block (+ tests)
    signing/      domain-separated signatures: Scheme (Ed25519), Domain, Signer (+ tests)
    consensus/    safety core + round driver: ValidatorSet, Vote, Commit, Evidence, Driver (+ tests)
    node/         the running validator: event loop, mempool, HTTP transport + API (+ tests)
    docs/         architecture, consensus, security, and operations

## Audit invariants already enforced in `ledger`

From the 2026-07 security audit:

- **B1** — a negative-amount payment cannot mint to the payer.
- **B2** — a payment from a non-existent account cannot mint to the payee.

`ProcessPayments` only moves value for payments whose amount is at least the transaction
cost (so strictly positive) and whose payer exists. Money is exact (`math/big.Rat`); there
is no floating point anywhere in the money path.

## Signatures (domain separation)

The central audit finding (C1): signing every payload type with one unscoped key over bare
`:`-joined strings lets a signature coerced in one context (e.g. a peer-controlled join handshake)
be replayed as a payment. The `signing` package rules that out structurally:

- There is **no "sign raw bytes" API.** Every signature is scoped to a `Domain`
  (`payment`, `server-auth`, `account-update`, …).
- The signed preimage is **canonical and length-prefixed**, and binds a fixed protocol tag,
  the **network id**, the domain, and each field — so field boundaries can't be shifted (C7),
  and a signature can't cross domains (C1) or networks/forks (C6).
- The algorithm sits behind a `Scheme` interface (Ed25519 by default). A post-quantum scheme,
  ML-DSA, drops in without touching the domain-separation layer — the commented, non-default slot is
  at [`signing/mldsa.go`](signing/mldsa.go); see **Cryptographic agility** below.

The tests assert the replay attacks are actually rejected: a `server-auth` signature does not
verify as a `payment`, a `testnet` signature does not verify on `mainnet`, and re-splitting a
field's bytes does not verify.

## Cryptographic agility (and post-quantum)

The signature algorithm is a **whole-network choice fixed at genesis** — a signature is
scheme-specific and every node must agree on the exact verifier, the same reason "one Ed25519
implementation is a consensus rule" (see [`docs/security.md`](docs/security.md)). It is Ed25519
today. A post-quantum scheme, **ML-DSA** (FIPS 204), already fits the `Scheme` interface with no
change to the domain-separation layer; it sits commented and non-default in
[`signing/mldsa.go`](signing/mldsa.go), un-activated only because Go's standard library ships no
ML-DSA signer yet and wiring a third-party one would break the zero-dependency rule (see
[`TODO.md`](TODO.md)).

"Fixed at genesis" does **not** mean "locked forever." Because a community can **export a faithful
snapshot of the agreed ledger and reseed a fresh network from it** (the ledger is deliberately
portable — see Origins), migrating the whole currency to a new signature scheme is a real, available
decision, not a wall: agree on a snapshot height, capture the committed state, reseed genesis under
the new scheme, and have everyone re-key. The cost is a short, *planned* downtime — on the order of a
couple of hours to snapshot, agree, and reseed — rather than a hard fork or a lost ledger. So if the
day comes that Ed25519 is judged at risk, moving to post-quantum is a governance call the community
gets to make, priced at a known and bounded outage.

## Consensus

The audit's central consensus finding (A1/A4): finalising a block on a single ">½ of all stake"
snapshot of one node's local gossip view — no quorum intersection, no durable equivocation
accountability — lets a sub-½ Byzantine coalition fork the chain under a partition, and risks voting
on a value derived from a *different* input than the state that gets applied (A2).

The `consensus` package is the pure, tested **safety core** that fixes this:

- **Finalisation needs a > ⅔ stake quorum** of precommit votes for one block digest
  (`ValidatorSet.Quorum() = floor(2·total/3)+1`). Requiring more than two-thirds is what makes
  any two quorums intersect in an honest validator, so two conflicting blocks cannot both
  commit while ≤ ⅓ of power is Byzantine. `TestConflictingCommitsRequireEquivocation` proves it:
  two commits for different digests must share a validator, i.e. a double-signer.
- **Votes are signed** through the `signing` layer (block-revision domain, network-bound), and a
  validator that casts a second conflicting vote is recorded as transferable `Evidence` and
  un-counted — a durable, slashable proof.
- **A `Commit` binds the applied-state digest** (`ledger.StateDigest`, SHA-256, sorted,
  length-prefixed — replacing MD5, audit C5). What got agreed is exactly what gets applied (A2),
  and any node or light client can verify a `Commit` against the validator set after the fact.

The **round driver** (`Driver`) turns that safety core into a live protocol: a pure, event-driven
Tendermint-style state machine over two phases (prevote → precommit) with a lock. It selects the
round's proposer, casts prevotes/precommits under the locking rule (a node that has locked value
X will not prevote a conflicting Y without a proof-of-lock — so it can never precommit two
conflicting values), and advances rounds on timeout. It is deterministic and fully unit-tested:
timeouts arrive as `Timeout` events (not real clocks), and the caller executes the `Action`s it
returns (broadcast a proposal/vote, arm a timer, record a decision). Tests cover the happy-path
decision, timeout-driven round advance, and the locking rule directly.

The **`node`** package wires this into a running validator (see [`docs/architecture.md`](docs/architecture.md)):
a single-goroutine event loop owns the ledger state, mempool and per-height driver — race-free by
construction, the deliberate fix for lock-free shared mutable state — with an HTTP transport
carrying proposals and votes between peers. Nodes periodically **re-gossip the current height's
votes**, so a network partition that drops every side below quorum self-heals when it merges rather
than freezing on the stuck votes. The safety and liveness of the whole stack are exercised by an
**adversarial harness** — a deterministic driver-level simulator plus a fault-injecting node-level
chaos suite (partitions, loss, reorder, equivocators) — that holds *no fork* and *no silent mint* as
invariants across thousands of randomized schedules.

## Build & test

    go test -short ./...       # fast: unit + integration (skips the chaos scenarios)
    go test -race ./...        # full: adds the adversarial chaos + driver-simulator scenarios
    go build ./cmd/server

Requires Go 1.22+. The chaos scenarios (`node/chaos_test.go`) are skipped under `-short`; a run
prints a `GOLEDGER_CHAOS_SEED` you can set to replay its exact fault schedule, the randomized soak
lengthens with `GOLEDGER_CHAOS_SOAK_SECONDS`, and the driver simulator's seed count scales with
`GOLEDGER_SIM_SEED_FACTOR`.

## Money representation

Amounts are `*big.Rat` (exact, arbitrary-precision rationals). This is a deliberate first choice for
exactness; switching to fixed-point integers (minor units) is a possible later optimisation once a
canonical scale is fixed.

## Origins

`go_ledger` is **not** an original work. It is a **Go port of the original Hypernode distributed
ledger, a Java (JVM) project** — a from-scratch, deliberately small delegated-proof-of-stake ledger
with a consensus-backed naming registry. The design, consensus model, naming registry, and
governance mechanism all originate in that Java implementation; this repository re-implements them in
Go (dropping the JVM for a single static binary) and hardens them, but all credit for the original
design belongs upstream:

> **Original project (Java):** <https://github.com/JauntyJackalope351/hypernode-ledger> — *Hypernode
> distributed ledger, AGPL-3.0.*

The original Java tree is untouched and out of scope here. This repository is a derivative work and is
likewise licensed **AGPL-3.0** (see [`LICENSE`](LICENSE)), as the original's copyleft requires.

