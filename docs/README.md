# go_ledger docs

The docs follow the four parts the system is split into, one container each. Three are built: the
engine (`cmd/server`), the fetcher in front of it (`cmd/fetcher`), and the wallet (`ui/`). The
signer is not split out yet: the engine still holds the key and signs.

**Signature**: the signer. ML-DSA-65, the signing domains and what a signature binds, keys, seeds
and `keygen`, the sign log that keeps a node from signing twice, and what a double-sign costs.
Code: `signing/`, `cmd/keygen/`, the sign log in `node/persist.go`.

- [signature.md](signature.md)

**Node**: the engine. The ledger's state and rules, the block and its hashes, the pools, consensus
(rounds, quorum, the timers and the slow-down switch, evidence and the freeze), the ⅔ certificates,
the snapshot, catch-up, the idle chain, the signature record, the public folder it writes, and
bundles. Code: `ledger/`, `consensus/`, and `node/` but its HTTP side.

- [architecture.md](architecture.md): the packages, the state, the block, the pools, the event
  loop, the idle chain, the signature record, the public folder and bundles.
- [consensus.md](consensus.md): the ⅔ commit rule, the round driver, the timers and the slow-down
  switch, intake of proposals and votes, the double-sign freeze and the one-third cap, catch-up and
  the checkpoint.
- [governance.md](governance.md): the ten parameters, parameter changes, the ⅔ name action, and the
  names as a name service.

**API/CDN**: the engine's two ports, and the public face in front of them. The gossip port and its
allow list, the operator port, the fetcher that serves the public face behind a CDN, the HTTP routes
and what each costs, peers and relay, the record served a unit at a time, the server's and the
fetcher's config, and the containers. Code: `node/httptransport.go` (the record, bundle and
checkpoint routes too), `node/allowlist.go`, `node/relay.go`, `node/topology.go`, `node/peerdns.go`,
the fetches in `node/sync.go`, `cmd/server/`, `cmd/fetcher/`, `cmd/demo/`, `cmd/quickstart/`,
`deploy/`.

- [api.md](api.md): the two ports and the public face, every route, its answer and its cost; how
  messages travel.
- [operating-a-network.md](operating-a-network.md): build, keys, the genesis file, the node config,
  network exposure and the allow list, the fetcher and the CDN, restarts, the record on disk, a
  leaked bank key, when the network splits (pigeon mode), a new genesis, `make demo` and
  `make quickstart`.

**UI**: the wallet, its own Go module under [`../ui/`](../ui/).

- [`ui/README.md`](../ui/README.md): building and running the wallet, its files, its pages.
- [`ui/USER_MANUAL.md`](../ui/USER_MANUAL.md): the guide for the people who use it.

**General**: what spans the four.

- [glossary.md](glossary.md): the words these docs use, the three pins, the Java project's words,
  and every id the review records use (A1, C1, E1, Q1, R1 and the rest).
- [reading-the-code.md](reading-the-code.md): a step-by-step guide to reading the code, in the four
  areas and the order the packages import each other, each step with its tests.
- [why-small-and-auditable.md](why-small-and-auditable.md): the owner's account of why the project
  stays small, and why real value needs an independent audit first.
- [security.md](security.md): the threat model, what the design fixes by construction, the limits
  of the freeze and of the banks, what the two ports and the public face protect, and what a
  deployment must do.
- [managing-the-currency.md](managing-the-currency.md): monetary policy for a community currency
  (taxes and minting pay the public sector; the fee only prices the network's work), where the
  design's assumptions stop, and how it outlives the internet.
