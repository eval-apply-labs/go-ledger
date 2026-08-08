# Security model

`go_ledger` is a Go port of the Java Hypernode ledger (see the [README](../README.md#origins)),
hardened as it was ported: its threat model was driven by review, and the fixes from four rounds are
recorded in [`findings.md`](../findings.md). This document states the threat model, what the design
fixes by construction, and what a deployment is still responsible for.

## Threat model and target

The initial target is a **small, known-identity, high-trust community** running its own local
currency on a handful of machines (e.g. 8 laptops on an isolated intranet). That shapes the model:

- Participants are known and socially accountable; the adversary is not an anonymous global
  attacker with unbounded resources.
- Value is low-to-moderate; the network is small and largely co-located.
- The realistic threats are *bugs that corrupt the ledger with no attacker*, *a single dishonest
  or compromised validator*, and *unreliable hardware* (laptops sleeping, rebooting, dropping off).

The design still aims to be Byzantine-fault-tolerant up to < ⅓ of stake, so it degrades gracefully
as a deployment grows beyond that trust envelope.

## Fixed by construction

Each of these was a concrete audit finding; here it is structurally impossible rather than patched.

- **Supply integrity (B1/B2).** `ledger.ProcessPayments` moves value only for a payment whose
  amount is at least the transaction cost (so strictly positive) from an account that exists. A
  negative-amount "payment" and a payment from a non-existent account — both of which could mint
  money — cannot be represented. Money is exact (`big.Rat`), no floating point.

- **No cross-protocol signature reuse (C1/C6/C7).** Every signature is scoped to a `Domain` and
  bound to a network via a canonical, length-prefixed preimage, and there is no "sign arbitrary
  bytes" API and no blind signing oracle. A signature made as one kind of statement cannot be
  replayed as another, across networks, or with shifted field boundaries. (The audited anti-pattern:
  one unscoped key signing `:`-joined strings, plus a raw signing endpoint.)

- **Account changes require the owner's signature.** An account-attribute/delegation change is only
  applied if it carries a valid signature by the account owner. (The audited flaw: applying these
  with no signature check, enabling stake capture.)

- **Signature malleability can't double-spend.** A payment's identity is a hash of its signed
  *content* (`ledger.PaymentID`), never its signature bytes, so a malleated copy — a second valid
  encoding of the same signature, which some Ed25519 verifiers accept — dedups to the same payment
  at the mempool, at gossip, and in the ledger core (`ProcessPayments` collapses duplicate content
  before applying). A payer is debited once even if a block carries the payment twice under two
  signatures. This is the Bitcoin transaction-malleability lesson: never key identity on a signature.

- **One Ed25519 implementation is a consensus rule.** Ed25519 libraries disagree on edge cases
  (non-canonical scalars, small-order points, cofactor — see *"Taming the Many EdDSAs"*, 2020), so
  two nodes running different implementations could disagree on a signature's validity and fork.
  Every node here uses the same Go `crypto/ed25519`; a port to another language must pin identical
  validation rules (e.g. ZIP-215). This is the analogue of Bitcoin's OpenSSL-DER near-fork. The
  scheme is likewise a whole-network choice fixed at genesis (behind the `signing.Scheme` interface),
  **but not locked forever**: a community can migrate — e.g. to the post-quantum ML-DSA scheme parked
  in `signing/mldsa.go` — by exporting a faithful snapshot of the agreed ledger and reseeding a fresh
  network under the new scheme (a planned outage of a couple of hours, not a hard fork). Crypto
  agility is therefore a governance decision, not a dead end. See `docs/operating-a-network.md`.

- **No consensus fork under ≤ ⅓ Byzantine (A1/A2/A4).** Finalisation needs a > ⅔ stake quorum whose
  intersection guarantees no two conflicting blocks can both commit; the committed digest is the
  digest of the applied state; equivocation yields a durable, slashable proof. See
  [consensus.md](consensus.md).

- **SHA-256, not MD5 (C5).** All content hashing uses SHA-256.

- **Replay protection without a spent-set.** A payment is signed over its target block (`BlockID`,
  and the network id, in the domain-separated preimage) and is included *only* in the block whose
  height equals it. Since each block number occurs once, a payment applies at most once, and a
  resubmitted already-cleared payment names a past block and is rejected — no persistent nonce/spent
  set to maintain. (A stronger, fork-resistant binding to the previous block hash is available as a
  future option for users who don't trust their reference node; the base guard suffices under the
  ⅔ no-fork consensus.)

- **An honest user cannot trip the overdraw ban.** The ledger's deliberate rule for a block whose
  payments overdraw the payer is harsh: the payer is banned and its remaining balance destroyed
  (supply can only shrink, never mint). Every intake point therefore refuses — with no receipt — a
  payment whose amount plus the payer's already-queued outgoing exceeds its committed balance, at
  the client endpoint and the gossip ingress alike, and the pending pool is re-checked after every
  commit. An honest node can neither hold nor propose an overdrawing payment set, so a committed
  overdraw implies a misused/cloned key or a proposer running modified code — exactly the parties
  the ban is meant for.

- **Race-free node.** All node state is owned by one goroutine; inputs arrive as events. Verified
  under Go's `-race` detector. (The audited flaw: shared unsynchronised state mutated from multiple
  threads.)

- **Bounded request bodies.** The HTTP API caps request size, avoiding the unbounded-JSON DoS the
  audit found on the spend path.

## The deployment is responsible for

The code cannot enforce these; an operator must:

- **Keep private keys private.** Keys are generated locally by `keygen` and never transmitted.
  Never put a private key on a network endpoint. The `keygen` output is yours to guard.
- **Keep the network off the public internet** unless you have added transport security and access
  control. On an isolated LAN the node API is reachable only by the machines on it; do not
  port-forward it. There is currently no TLS or peer authentication at the transport layer beyond
  the per-message signatures — a peer is trusted to relay, not to be honest, and the signatures are
  what make votes/blocks trustworthy regardless of who relayed them.
- **Provision validators for liveness.** Finalisation needs > ⅔ of stake online and agreeing. On
  flaky hardware, concentrate enough stake on the machines you can keep awake (disable laptop sleep)
  that they alone hold quorum, and let unreliable machines be light participants. This trades some
  decentralisation for liveness — an explicit, appropriate choice at this scale. A network partition
  that leaves *no* side above quorum-any (an even 2|2 split) stalls — it cannot fork or mint, only
  wait — but it **self-heals** once the partition lifts: each node periodically re-gossips the
  current height's votes, so the merged network re-exchanges the frozen votes, reaches quorum, and
  advances (see the round driver and `Node.reGossipLoop`). Both the freeze-then-recover behaviour
  and majority progress under loss are exercised by the adversarial harness (`node/chaos_test.go`,
  `consensus/sim_test.go`).
- **Enable persistence, and keep an off-machine backup.** A node with `snapshotPath` set writes its
  committed state to disk after every block and reloads it on restart, so a single node — or the
  whole co-located network after a simultaneous outage — recovers without needing a peer; a node
  that was behind then catches up via `bootstrap` (onboarding sync, verified against the validator
  set — see [consensus.md](consensus.md)). Snapshots are per-node, so a disk failure loses that
  one node's copy (it re-syncs from peers). For real value, also keep a periodic off-machine export
  (`GET /hdls/accounts`) in case of correlated disk loss.

## Status of this rewrite

This is new code. The pure layers (money, signing, consensus) are covered by tests including the
adversarial cases above, and the node is race-tested. The operational layers once deferred —
onboarding/rejoin sync, snapshot persistence, straggler catch-up — are implemented and tested (see
[consensus.md](consensus.md)). It has **not** had an independent third-party audit, and the
adversarial consensus harness in `TODO.md` is the main missing test tier; both are needed before
any deployment holding real value. Treat it as testnet-grade.
