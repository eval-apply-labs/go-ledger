# go_ledger docs

The docs follow the four parts Phase 3 splits the system into, one container each. Today one
`cmd/server` binary runs the node, its API and its signing, and the wallet is a second program.

**Signature**: the signer. ML-DSA-65, the signing domains and what a signature binds, keys, seeds
and `keygen`, the sign log that keeps a node from signing twice, and what a double-sign costs.
Code: `signing/`, `cmd/keygen/`, the sign log in `node/persist.go`.

- [signature.md](signature.md)

**Node**: the engine. The ledger's state and rules, the block and its hashes, the pools, consensus
(rounds, quorum, evidence and the freeze), the ⅔ certificates, the snapshot, catch-up, the idle
chain, and writing the signature record. Code: `ledger/`, `consensus/`, and `node/` but its HTTP
side.

- [architecture.md](architecture.md): the packages, the state, the block, the pools, the event
  loop, the idle chain and the signature record.
- [consensus.md](consensus.md): the ⅔ commit rule, the round driver, intake of proposals and votes,
  the double-sign freeze and the one-third cap, catch-up and the checkpoint.
- [governance.md](governance.md): the ten parameters, parameter changes and the ⅔ name action.

**API/CDN**: the proxy in front of the engine. The HTTP routes and what each costs, peers and
relay, the record served a unit at a time, and the server's config. Code: `node/httptransport.go`
(the record routes too), `node/relay.go`, `node/topology.go`, `node/peerdns.go`, the fetches in
`node/sync.go`, `cmd/server/`, `cmd/demo/`.

- [api.md](api.md): every route, its answer and its cost; how messages travel.
- [operating-a-network.md](operating-a-network.md): build, keys, the genesis file, the node config,
  restarts, the record on disk, a leaked bank key, a new genesis, and `make demo`.

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
  of the freeze and of the banks, and what a deployment must do.
- [managing-the-currency.md](managing-the-currency.md): monetary policy for a burn-and-mint
  community currency, and where the design's assumptions stop.
