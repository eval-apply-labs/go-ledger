**REFERENCING v1.0.0 — NOW ADDRESSED.**

# Code Review — go-ledger

> **Status: resolved — no pending items.** This review was run against the **v1.0.0 release**
> (commit `0ac193b`) and is kept verbatim. Every finding has since been verified and dispositioned
> in [`findings.md`](../findings.md) (*External review (GLM) — 2026-08-02*): C1/C3/C4 fixed with
> regression tests (C3's verified impact — a silent mint — was worse than described below), C2
> accepted as LOW, C5's fork-risk claim refuted; the G1/G2/G3 coverage gaps are closed.

**Reviewer:** GLM (automated review)
**Date:** 2026-08-02
**Release reviewed:** v1.0.0 (commit `0ac193b`)
**Scope:** `ledger`, `signing`, `consensus`, `node`, `ui`, `cmd` — ~10,300 lines of source across the tree.
**Method:** Static reading of source and tests, cross-checked against `findings.md` and `TODO.md`.

> **Important caveat.** Go is not on the `PATH` in the review environment (installed only as a
> Homebrew dependency, no `go` binary), so the test suite was **not executed**. All coverage claims
> below are from reading the test files, not from `go test -cover`. Coverage percentages are not
> reported; what is reported is which behaviors are pinned by tests and which are not.

---

## Executive summary

This is a mature, honestly-documented codebase that has already been through several review passes
(recorded in `findings.md`). The safety-critical core — malleability defence, domain-separated
signatures, equivocation evidence, governance replay guards, supply conservation — is well-pinned by
tests at the right layer, and the adversarial harness (`node/chaos_test.go`, `consensus/sim_test.go`)
is genuinely strong and rare at this scale.

The most actionable findings are **four new defects not in `findings.md`**, the most serious of which
is a guaranteed nil-pointer panic on a documented, shipped deployment path (dynamic validator mode).
After that, the priorities are closing the **zero-coverage gaps on the UI security surface and the
`cmd/` binaries**, and adding overflow/guard rails the audit didn't flag.

Findings are ordered by priority. Each carries a `file:line` reference and a concrete recommendation.

---

## Part 1 — Test coverage

### 1.1 What is well covered

The audit-finding regression tests all genuinely exist and target the right code. Each was confirmed
present by name and location:

| Audit finding | Regression test | Location |
|---|---|---|
| B1/B2 supply-integrity (negative amount, phantom payer) | `TestNegativeAmountPaymentDoesNotMint`, `TestPhantomPayerPaymentDoesNotMint` | `ledger/ledger_test.go` |
| Mixed-round commit rejection (#1) | `TestCommitRejectsMixedRounds`, `TestConflictingCommitsRequireEquivocation` | `consensus/consensus_test.go:176, 213` |
| Evidence age window | `TestEvidenceAgeWindow` | `ledger/slash_test.go:64` |
| Nil transaction cost | `TestNilTransactionCostTreatedAsZero` | `ledger/ledger_test.go:60` |
| Reassignment cycle-replay | `TestReassignmentSequenceGuardBlocksCycleReplay` | `ledger/reassignment_test.go` |
| Vote re-gossip liveness | `TestChaos2x2PartitionFreezesThenRecovers` | `node/chaos_test.go:514` |
| Proposal re-gossip | `TestChaosLostProposalRecoversViaReGossip` | `node/chaos_test.go:399` |
| Governance cycle stays inert | `TestChaosGovernanceCycleReplayStaysInert` | `node/chaos_test.go:673` |
| Signing domain separation (C1/C6/C7) | `TestCrossDomainSignatureRejected`, `TestCrossNetworkSignatureRejected`, `TestFieldInjectionResisted` | `signing/signing_test.go:28, 39, 51` |
| Proposal forgery (wrong signer, swapped body) | `proposal_relay_test.go` | `node/` |

The **adversarial harness is the standout**. `node/chaos_test.go` (873 lines) runs the real HTTP
stack under partitions, message loss/duplication/reorder, and byzantine equivocators, holding
*no fork* and *no silent mint* as invariants across randomized, replayable schedules.
`consensus/sim_test.go` (266 lines) is a deterministic single-threaded driver simulator covering
delay/reorder/loss/clock-skew and equivocation. This is unusual at this scale and is the highest-
value testing in the project.

### 1.2 Coverage gaps — prioritized

#### Priority 1 — Security-critical, zero test coverage

**G1. UI security surface is essentially untested.** The CSRF `guard()` / `sameOrigin()`
(`ui/main.go:62-86`), the wallet's inline receipt verification (`ui/wallet.go:242-245`), and every
admin handler in `ui/admin.go` (`proposeParameter`, `voteReassignment`) have **no tests at all** —
despite CSRF being a HIGH in `findings.md` and the receipt-verification fix being a documented
resolution. This is the most attack-exposed code in the project and the least tested.

Suggested tests:
- `TestCSRFCrossSitePostBlocked` — a POST with a cross-site `Origin` is rejected.
- `TestCSRFAllowsLocalOperator` — a POST with no browser headers (curl) is accepted (documents the
  deliberate fallback).
- `TestWalletReceiptVerificationRejectsNonValidator` — a receipt from a non-validator node is not
  marked verified.
- `TestWalletReceiptVerificationRejectsPaymentIDMismatch` — a receipt over a different payment is
  rejected.
- `TestAdminProposeParameterValidatesSignature` / `TestAdminVoteReassignmentValidatesSignature`.

**G2. `cmd/` has zero tests across all three binaries** (`cmd/server`, `cmd/keygen`, `cmd/demo`).
The highest-value target is `cmd/server/config.go:56` `load()` — config parsing, validation, and
env handling. This is exactly where the **C1** landmine below lives, and nothing catches it.

Suggested tests:
- `TestServerConfigLoadValid` — a well-formed config loads.
- `TestServerConfigRejectsBadAmount` / `TestServerConfigRejectsBadThreshold`.
- `TestServerConfigDynamicValidatorMode` — `validatorThreshold` set leaves `Validators` nil without
  crashing (this test would catch **C1**).

#### Priority 2 — Accepted landmines with no pinning test

**G3. Overdraw ban balance-destruction is not directly tested.** `conservation_test.go` only asserts
supply never *increases* (`Cmp > 0`); no test asserts the payer's balance is actually *zeroed* when
they overdraw. This is the most counter-intuitive behaviour in the codebase (see **L1**) and it
deserves an explicit pin so a future "fix" doesn't silently relax it.

Suggested: `TestProcessPaymentsOverdrawBanDestroysPayerBalance` — fund 10, two 6-unit payments in
one block, assert the payer's account is absent from the result and supply dropped by 10.

**G4. `ApplyBlock` rejecting a block carrying BOTH a parameter change and a reassignment**
(`ledger/block.go:211-213`) has no test. Note: there are now **12** distinct `return false` rejection
reasons in `ApplyBlock` (the audit said 8), all collapsing to one `bool`.

Suggested: `TestApplyBlockRejectsBlockWithBothGovernanceActions`.

**G5. `catchUp` re-entry guard** (finding #13). The guard at `node/node.go:466-470` (`heightStarted`)
works, but no test directly exercises the stale-`evNextHeight` re-entrancy scenario.

#### Priority 3 — Boundary / "unreachable" paths left unverified

**G6. The consensus POL/unlock branch** (`prevoteFor` when `lockedRound <= vr`) — the proof-of-lock
release path, the most complex Tendermint logic in the repo — is not exercised by `driver_test.go`
or `sim_test.go`. Suggested: `TestDriverPOLUnlockReleasesLock`.

**G7. `castVote` signing-error path** (`consensus/driver.go:256`) is documented "unreachable" with
no test. Needs an injectable signer to test; worth adding the seam regardless.

**G8. `maxRoundLookahead` far-future-round rejection** (`consensus/driver.go:216`) — no test.
Suggested: `TestDriverRejectsFarFutureRound`.

**G9. `Preimage` uint32 length-prefix framing** (`signing/domain.go:48`) — no test verifies framing
round-trips or that the `uint32`/`uint64` inconsistency with `hashFields` is cosmetic.

**G10. `evidence.Valid`** (`consensus/evidence.go:11`) has no direct unit test — only surfaced
indirectly via the driver. `Receipt.Verify` likewise has no ledger-layer unit test.

**G11. f+1 skip's separate vote-type counting** (#18). `driver_test.go:47` only tests the prevote
case; the claim that precommits are counted *separately* is not verified.

---

## Part 2 — Landmines

`findings.md` already documents many landmines as "accepted" or "deferred." Those were verified
against the current code (Section 2.1). Section 2.2 lists **new** landmines not in `findings.md`.

### 2.1 Documented landmines — verified still real

| # | Landmine | Status (verified) |
|---|---|---|
| L1 | **Overdraw ban destroys the payer's whole balance.** `ledger/ledger.go:155-209` — a banned payer is in neither the pass-through loop (`:188`) nor `affected` (`:192`), so its pre-existing balance vanishes. Verified it can **only reduce** supply, never mint (every credit is funded by a debit net of fee; banned payers contribute no credit). | ACCEPTED, verified |
| L2 | **No height continuity in the ledger layer.** `LedgerState` has no height field (`parameters.go:109-113`); `ApplyBlock` accepts any `block.Height` if `PrevStateDigest` matches (`block.go:176-179`). The node layer guards this (`node.go:466-471`). | DEFERRED (#7), verified |
| L3 | **f+1 skip counts vote types separately.** `consensus/driver.go:322`. Strictly safe, slower catch-up. | LOW (#18), verified |
| L4 | **Content-dedup drops genuinely identical payments.** `ledger/ledger.go:143-146` — two payments with identical signed content collapse to one. Guidance exists in the comment at `:119-123` ("vary the Comment"). | LOW (#21), verified |
| L5 | **Snapshot shares `*big.Rat` pointers with live state.** `node/node.go:302-315` — `Account` structs are copied by value but `Amount` pointers are shared. Safety relies on `ledger` treating amounts as immutable (verified: `ProcessPayments`, `addRat`, `applyReassignment` all allocate fresh `big.Rat`). The comment at `:302-306` is the only guard. | LOW, verified, invariant intact |
| L6 | **`catchUp` re-trigger by stale `evNextHeight`** — the `heightStarted` guard (`node.go:466-470`) **works**; this is listed as Fixed in `findings.md`, not deferred. | FIXED, guard verified |
| L7 | **`uint32` length prefix in `signing.Preimage`.** `signing/domain.go:49`. Unreachable given the 1 MiB HTTP body bound (`httptransport.go:308`); the `uint64`/`uint32` inconsistency with `hashFields` is cosmetic. | LOW (#24), accepted |
| L8 | **`height()` returns 0 on node error.** `ui/wallet.go:183`. Benign — a `BlockID` derived from it is rejected as stale at intake; no bad payment lands. | LOW (#29), accepted |
| L9 | **Variadic `validators` footgun.** `ledger/block.go:95, 176` and `governance.go:39-44`. Forgetting the arg silently drops governance content. The node always passes the set. | design nit (#25), verified |
| L10 | **`ApplyBlock` returns a bare `bool`.** Now **12** rejection reasons (not 8) collapse to one `bool` (`block.go:178-241`). | design nit (#26), verified |
| L11 | **Unauthenticated write API + single-loop DoS.** `node/httptransport.go:106-121` — all mutating endpoints open; 1 MiB bound is the only guard; handler goroutines block on the event-loop channel when saturated. | DEFERRED (#10), verified |

### 2.2 New landmines — not in `findings.md`

#### C1 — HIGH — `cmd/server/main.go` panics in dynamic-validator mode

`cmd/server/main.go:31`:
```go
log.Printf("hypernode node up: network=%q listen=%s peers=%d validators=%d",
    cfg.NetworkID, listen, len(peers), cfg.Validators.TotalPower())
```

When `validatorThreshold` is set in config — the **dynamic validator admission** feature, documented
in `README.md:60-61` and shipped — `config.go:104-112` deliberately leaves `cfg.Validators` as **nil**
(the set is derived from stake at runtime). `TotalPower()` (`consensus/validator.go:43`) does
`return vs.total`, a field dereference that **panics on a nil receiver**. The node's own
`validatorsFor()` helper (`node/validators.go:15-18`) handles the nil case correctly; this log line
bypasses it.

**Impact:** an operator deploying the documented dynamic-validator path gets an immediate crash with
no guidance, before the node serves a single request. Zero test coverage (no `cmd/` tests).

**Fix:** guard the log line, e.g.:
```go
vp := int64(0)
if cfg.Validators != nil { vp = cfg.Validators.TotalPower() }
```
Or derive via the same helper the node uses.

#### C2 — MEDIUM — Voting power `int64` overflow / silent truncation

`node/validators.go:42`:
```go
power := new(big.Int).Quo(s.Num(), s.Denom()).Int64() // floor(stake)
```

`big.Int.Int64()` returns the low 64 bits **with no overflow check**. A reassignment
(`Reassignment.NewAmount`, an arbitrary `*big.Rat`) can mint any balance, so delegated stake can
legitimately exceed `math.MaxInt64` (~9.2e18). Truncation can:
- wrap to **negative** → filtered by `power <= 0` (`:43`) → validator silently dropped (liveness
  stall if it held >⅓ power);
- wrap to a **small positive** → wrong power → breaks the quorum math that safety relies on.

The overflow propagates: `consensus/validator.go:34` `vs.total += v.Power`, `:49` `Quorum()` computes
`2*total` first (overflows if `total > MaxInt64/2`), and `ProposerAt` (`:73`) does
`h.Sum64() % uint64(vs.total)` (negative `total` → ~1.8e19 modulus → broken proposer selection). No
overflow check anywhere in the power path.

**Fix:** check `IsInt64()` before `Int64()`; reject or clamp out-of-range stake. Consider an upper
bound on a single validator's power at the `Acceptable`/config layer.

#### C3 — MEDIUM — Genesis config skips `Acceptable()` validation (negative cost bricks the network)

`cmd/server/config.go:load()` clamps `MaxMessageBytes` (`:122-130`) but **never calls
`LedgerParameters.Acceptable()`** on the assembled genesis parameters. A **negative
`transactionCost`** in the config (`SetString` at `:74` accepts `"-1"`) bypasses the `Sign() < 0`
check that governance proposals must pass (`ledger/parameters.go:90`). With a negative cost,
`Amount.Cmp(transactionCost) < 0` (`ledger/ledger.go:128`) is true for every positive amount →
**every payment is dropped → the network is silently bricked at genesis**, with no error.

This is the same class of bug the `MaxMessageBytes` clamp was added for; `TransactionCost` was missed.
`MinBlockIntervalMs` can also be negative (committed into the digest; the node treats `<= 0` as "no
interval" so it runs, but the genesis state is malformed).

**Fix:** after assembling `cfg.Genesis` parameters, call `Acceptable(participants)` and fail `load()`
with a clear message if it returns false.

#### C4 — MEDIUM — Ed25519 key size not validated at config load

`cmd/server/config.go:66-73` base64-decodes `publicKey`/`privateKey` but never checks their lengths.
A truncated key starts the node (no fail-fast); then `SignVote` fails → `castVote`
(`consensus/driver.go:255-262`) **swallows the error** and returns nil → the node silently never
votes or proposes. It looks "up" (HTTP serves) but is a **consensus ghost**. `SignReceipt` also
fails → `handlePay` returns 202 with no receipt body. The wallet side (`looksLikePublicKey`,
`ui/wallet.go:95-98`) checks `len == 32`; the server side does not.

**Fix:** validate `len(pub) == ed25519.PublicKeySize` and `len(priv) == ed25519.PrivateKeySize` in
`load()`, fail fast with a clear message.

#### C5 — MEDIUM — Nondeterministic map iteration in pending-governance selection

`node/governance.go:79` and `node/reassignment.go:80` iterate a map and return the **first** proposal
that has reached quorum and amends current state. Map iteration order in Go is randomized; if two
proposals both satisfy the condition, different nodes can pick different ones → different proposed
blocks → different `StateDigest` → fork risk.

Safe *today* under the BFT assumption (quorum intersection prevents two conflicting proposals both
reaching >⅔), but it is the same latent-nondeterminism class `findings.md` flags at
`driver.go:272` / `voteset.go:78` — and it is **not** mentioned there.

**Fix:** sort candidates (by `NewParameters.Digest()` or by proposer key) before selecting.

#### Lower-severity new landmines

| # | Landmine | Location | Severity |
|---|---|---|---|
| C6 | `big.Rat.UnmarshalText` silently swallows malformed amounts (`json.Unmarshal` of `{"amount":"abc"}` succeeds with `Amount=0`, no error). Non-exploitable today (signatures bind `RatString()`), but a latent footgun for any future code that trusts a `*big.Rat` from JSON without re-verifying. | `ledger/ledger.go:128`, `reassignment.go:87`, `governance.go:90` | LOW |
| C7 | HTTP handlers have no request-side timeout (`cmd/server/main.go:33`). Combined with L11, a saturated event loop causes handler goroutines to pile up indefinitely — the concrete DoS failure mode. | `cmd/server/main.go:33` | LOW |
| C8 | `BackupPath` write runs in an untracked goroutine (`node/persist.go:56-62`) — can outlive `Stop()`, race on `os.Rename` to the same path, and the "atomic" guarantee breaks on NFS (no atomic rename). | `node/persist.go:56-62` | LOW |
| C9 | `looksLikePublicKey` hard-codes Ed25519's 32 bytes (`ui/wallet.go:95-98`). If ML-DSA drops in (keys ~3.5 KB base64), every key input is misclassified as a name. The `signing` package is scheme-agnostic; this UI seam is not, and `mldsa.go`'s claim that "nothing else in the framing changes" is slightly optimistic. | `ui/wallet.go:95-98` | LOW |
| C10 | `sameOrigin` CSRF guard is narrower than its name (`ui/main.go:76-86`) — blocks cross-site *browser* requests but allows any client that omits `Origin`/`Sec-Fetch-Site` (curl, local processes). Documented as intentional for the local operator, but easy to misread as authentication. | `ui/main.go:76-86` | LOW |
| C11 | `deriveValidators` mutates a shared `*big.Rat` in place (`node/validators.go:31-35`) — safe today (fresh allocation), but the immutability invariant is documented only at the snapshot site (L5), not here. A future "optimization" reusing a buffer would activate the snapshot-pointer race. | `node/validators.go:31-35` | LOW |
| C12 | `tallyVoters` and other power accumulations (`node/governance.go:212`, `consensus/voteset.go:70,91`, `consensus/commit.go:40`) use unguarded `int64 +=`; safe only if total power stays well below `MaxInt64` — i.e. dependent on C2 being unreachable. | various | LOW |

---

## Part 3 — Recommendations (prioritized)

### Must fix before a network carries value

1. **C1 — dynamic-validator nil panic.** One-line guard in `cmd/server/main.go:31`. Guaranteed crash on
   a documented deployment path. Add a `cmd/` test that catches it.
2. **C3 — genesis `Acceptable()` validation.** A negative `transactionCost` silently bricks the
   network at genesis with no error. Call `Acceptable()` in `config.go:load()`.
3. **C2 — voting-power overflow guards.** Add `IsInt64()` checks; an out-of-range stake should be
   rejected, not silently truncated, because the truncation breaks quorum math.

### Should fix (hardening + coverage)

4. **C4 — validate key sizes at config load.** Fail fast instead of the silent "ghost node."
5. **G1 — UI security tests.** CSRF, receipt verification, and admin handlers are the most
   attack-exposed code and have zero coverage.
6. **G2 — `cmd/server/config.go` tests.** The most bug-prone untested code; would have caught C1.
7. **C5 — sort pending-governance candidates** before selection to close the latent fork risk.

### Nice to have (defence-in-depth + pinning)

8. **G3 — pin the overdraw-ban balance-destruction** with an explicit test (L1 is the most
   surprising behaviour in the codebase).
9. **C7 — add HTTP request timeouts** (`ReadTimeout`/`WriteTimeout`) to bound goroutine accumulation.
10. **G6 — test the POL/unlock branch**, the most complex untested Tendermint logic.
11. **C8/C11 — document or deep-copy** the `BackupPath` goroutine and `deriveValidators` mutation
    invariants where they live, not just at the snapshot site.

---

## What's already good (worth preserving)

- The `signing` package is textbook domain separation: length-prefixed, protocol/network/domain/
  field-bound, injective preimage, no raw-sign escape hatch, with tests that attack the properties.
- Content-addressed malleability defence keyed on `PaymentID` (hash of signed content), independent
  of the crypto lib rejecting non-canonical signatures.
- Propose/apply symmetry: `ApplyBlock` independently recomputes every asserted effect.
- The actor-model node is real and verified — `-race` is the build default for tests, with genuine
  multi-node HTTP tests (4-node all-to-all, 9-node Hamming-graph relay, catch-up, replay).
- `big.Rat` end-to-end money with canonical `RatString`, copy-on-write throughout, deterministic
  sorted output. No floating point in the money path.
- Zero external dependencies in both modules; high comment quality with traceable audit-finding IDs.
- The adversarial harness is genuinely valuable and rare at this scale.
