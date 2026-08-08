# go_ledger docs

- [architecture.md](architecture.md) — the layered design, the actor-model node, and why the
  rules are kept pure and the I/O isolated.
- [consensus.md](consensus.md) — the ⅔ Tendermint-style commit rule, the round driver with
  locking, the safety argument, and the mapping to the audit findings.
- [security.md](security.md) — threat model, what the port fixes by construction, and what a
  deployment is responsible for.
- [operating-a-network.md](operating-a-network.md) — build, keygen, the node config format, running
  a multi-node network, the HTTP API, and the instant `make demo`.
- [governance.md](governance.md) — self-amending the operating parameters and reassigning balances by
  a > ⅔ vote: the change certificate, the coupled timing profile, and the admin UI.
- [managing-the-currency.md](managing-the-currency.md) — the monetary policy of a burn-and-mint
  community currency: supply, the treasury, the tax/services/stability trilemma and its honest limits,
  the smells of misuse at national scale, and where the design's assumptions cap.

The wallet UI (a friendlier front end than raw JSON) is a separate module under [`../ui/`](../ui/),
with its own README and an end-user [USER_MANUAL.md](../ui/USER_MANUAL.md).
