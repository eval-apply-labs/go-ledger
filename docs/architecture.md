# Architecture

`go_ledger` is a delegated-proof-of-stake ledger built as a small stack of pure, independently
testable layers, with a thin networked shell on top. It is the Go port of the Java Hypernode ledger
(see the [README](../README.md#origins)); the design principle throughout is the one that motivated
the port from the JVM: **keep the rules pure and auditable, isolate the I/O.** A reader should be
able to understand any layer without holding the others in their head.

## Package dependency graph

```
                 cmd/server ── cmd/keygen
                     │
                    node                 (the running validator: I/O, timers, HTTP)
                   /    \
             consensus   \                (safety core + round driver: pure)
                 │        │
              signing    ledger           (crypto; money + block application: pure)
                 └────────┘
```

Dependencies point downward only. `ledger`, `signing` and `consensus` are pure — no I/O, no
networking, no clocks — so each is exercised by ordinary unit tests. Everything time- and
network-dependent lives in `node`.

## Layers

### `ledger` — the money and the state transition (pure)

- `Account`, `Payment` — the domain types. Amounts are `math/big.Rat` (exact, arbitrary-precision
  rationals); there is no floating point anywhere in the money path.
- `ProcessPayments` — applies a batch of payments to a set of accounts. It enforces the audited
  supply-integrity invariants up front (a payment moves value only if its amount is at least the
  transaction cost — hence strictly positive — and its payer exists), so neither the
  negative-amount mint nor the phantom-payer mint is representable.
- `StateDigest` — a deterministic SHA-256 digest of the sorted, length-prefixed account set.
  `LedgerState.Digest` combines it with the parameters and the governance sequence into the single
  value consensus commits (a block's `StateDigest`), so agreement binds the balances/registry, the
  parameters in force, and the governance sequence together.
- `Block`, `ProposeBlock`, `ApplyBlock` — the deterministic state transition. `ApplyBlock`
  re-executes a block and accepts it only if it builds on the prior state, every payment is
  signature-valid, and the recomputed digest equals the block's claimed digest.

### `signing` — domain-separated signatures (pure)

Every signature is scoped to a `Domain` and bound to a network via a canonical, length-prefixed
preimage. There is deliberately no "sign raw bytes" primitive. A `Scheme` interface abstracts the
algorithm (Ed25519 by default). See [consensus.md](consensus.md) and [security.md](security.md).

### `consensus` — agreement (pure)

- The **safety core**: `ValidatorSet` (stake-weighted power, ⅔ quorum), `Vote`, `VoteSet`
  (tally + equivocation `Evidence`), and `Commit` (a ⅔ precommit certificate binding a digest).
- The **round driver** (`Driver`): a Tendermint-style state machine — proposer selection,
  prevote→precommit with locking, and timeout-driven round advancement. It is event-driven
  (`Start`/`ReceiveProposal`/`ReceiveVote`/`Timeout`) and returns `Action`s for the caller to
  execute, so it has no dependency on real clocks or sockets and is fully unit-tested.

### `node` — the running validator (I/O)

`Node` is an **actor**: a single goroutine (the run loop) owns *all* mutable state — the ledger
accounts, the payment mempool, and the current-height `Driver`. Every input arrives as an event on
a channel:

- `SubmitProposal` / `SubmitVote` — from peers (via the transport / HTTP handlers).
- `SubmitPayment` — from clients.
- timer fires — armed via `time.AfterFunc`, delivered back as an event.
- `Snapshot` — a query, answered from within the loop.

Because only the loop touches state, the node is **race-free by construction** — the deliberate
fix for lock-free shared mutable state, where a scheduler thread and HTTP workers can mutate the
same unsynchronised sets. This is verified: the multi-node integration tests pass under Go's
`-race` detector.

The loop drives consensus per height: build a candidate block from the mempool, run the `Driver`,
execute its actions (broadcast a proposal/vote, arm a timeout, or on decide apply the block and
advance to the next height). A `Transport` abstracts message delivery; `HTTPTransport` +
`Server` are the HTTP implementation. A `Message` carries one of several payloads — a signed
`ProposalMsg` (a full block, so peers can validate it), a self-contained signed `Vote`, a gossiped
`Payment` or `AccountUpdate`, or a governance `ParameterChange` / `Reassignment` vote — each on its
own `/hdls/*` endpoint.

### `cmd/server`, `cmd/keygen`

`server` loads a JSON config (see [operating-a-network.md](operating-a-network.md)), starts a
`Node`, and serves the HTTP API. `keygen` prints a fresh Ed25519 key pair. Both are thin.

## Why this shape

Mixing consensus math, crypto, HTTP handlers and shared mutable state together is what lets subtle
bugs (a minting payment, an unsigned account rewrite, a fork under partition, data races) hide.
Splitting the pure rules from the I/O means:

- the parts that *must* be correct (money, signatures, agreement) are pure functions with tests;
- the part that is *hard to test* (timing, networking) is a thin shell with one obvious
  concurrency model (one owning goroutine);
- each piece is small enough to audit on its own — the point of the whole project.
