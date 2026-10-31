# Review findings — 2026-07-08

An external code review of the whole tree (ledger, signing, consensus, node, ui, docs).
Build is clean (`go build ./...`, `go vet ./...` on both modules) and the full suite passes
under `-race`. What follows is the list of defects and weak spots, ranked by severity within
each area. File:line references are to the state of the tree at review time.

These are review inputs, not a substitute for an independent human audit (see `TODO.md`).

---

## External review (GLM) — 2026-08-02

A post-publication external review (`review/glm_code_review.md`, `review/glm_documentation_review.md`)
surfaced five new code findings (C1–C5) and fourteen doc gaps (D1–D14). Each was verified against the
source before acting; dispositions below. The doc gaps were closed the same day (user manual: one-key-
one-machine, `.state` backup, privacy; operator guide: network exposure, NFS/backup and snapshot
caveats, corrected `transactionCost` row; wallet README: `-lookahead`, service passphrase env var).

**Fixed, with regression tests**
- **C1 (HIGH): dynamic-validator mode panicked at startup.** `cmd/server/main.go` logged
  `cfg.Validators.TotalPower()`; with `validatorThreshold` set the static set is deliberately nil, so
  the node crashed on a documented deployment path before serving a request. Now behind
  `validatorsLabel` (nil → "dynamic (stake-derived)"), pinned by the new `cmd/server` config tests.
- **C3 (was MEDIUM; verified worse than reported): genesis parameters skipped `Acceptable()`.**
  Confirmed gap, but the review's impact analysis was inverted: a negative `transactionCost` does NOT
  brick the network — `Amount.Cmp(cost) < 0` is false for positive amounts against a negative cost, so
  payments flow and every payee is credited `Amount−cost > Amount`, silently minting |cost| per
  payment; negative amounts also pass the below-cost filter again (re-opening the B1 mint class).
  Fixed twice over: `load()` refuses a negative cost and validates the assembled genesis profile with
  `Acceptable()` (mirroring what `node.New` commits), and `ProcessPayments` clamps a negative cost to
  zero like nil, so supply is conserved even for a caller that bypasses both.
  `TestNegativeTransactionCostDoesNotMint` pins it.
- **C4 (MEDIUM): key lengths unvalidated at config load.** A truncated Ed25519 key started a node that
  served HTTP but could never sign a vote or receipt (the driver swallows signing errors by design) —
  a consensus ghost. `load()` now fails fast on wrong-length keys.

**Coverage closed**
- `cmd/server` had zero tests (G2): `config_test.go` now pins valid load, dynamic mode (C1), negative/
  malformed cost (C3), truncated keys (C4), malformed genesis amounts, and genesis `Acceptable()`.
- The UI security surface had zero tests (G1): `ui/security_test.go` now pins the CSRF guard table
  (cross-site/same-site/foreign-Origin blocked; curl and same-origin allowed; all four mutating
  handlers behind the guard) and receipt verification against a fake node (validator receipt verifies;
  non-validator, wrong-PaymentID, and tampered-signature receipts do not).
- The overdraw ban's full consequence was pinned (G3): `TestOverdrawBanDestroysPayerBalance` asserts
  the banned payer's account vanishes, the payee receives nothing, and supply drops by exactly the
  destroyed balance — so any future relaxation is a conscious choice.
- The remaining consensus-path gaps were closed in the same-day follow-up pass:
  `TestDriverPOLUnlockReleasesLock` (G6 — the proof-of-lock release, the complement of the existing
  locking test), `TestDriverBrokenSignerDropsVoteQuietly` (G7 — the castVote error branch, driven by
  a wrong-length-key signer; no injection seam needed), and
  `TestDriverRejectsFarFutureAndNegativeRounds` (G8 — the `maxRoundLookahead` allocation bound,
  including the boundary round). C7 (HTTP server timeouts) and C8/C11 (invariants documented at
  their sites) landed alongside; see `TODO.md` for the item-by-item record.

**Accepted / reclassified**
- **C2 (stake → `int64` truncation in `deriveValidators`): initially accepted as LOW, then CLOSED the
  same day with a considered design.** Voting power now SATURATES at `consensus.MaxValidatorPower`
  (2⁴⁰) in `NewValidatorSet` — the single choke point for both the fixed-config and stake-derived
  paths — with an `IsInt64()` guard on the floor in `deriveValidators`. Saturation was chosen over
  truncation (can wrap negative → validator silently dropped, or small-positive → corrupted quorum
  denominators) and over rejection (the same silent drop): the whale keeps the largest possible voice
  and every power sum stays far inside int64 for any realistic validator count. Pinned by
  `TestValidatorSetSaturatesPower` and `TestDeriveValidatorsSaturatesHugeStake`. This also retires
  C12 (the unguarded `int64 +=` tallies), which was only dangerous while a single power could exceed
  the cap.
- **C5 (map-iteration order in pending-governance selection): no fork risk; hygiene nit — closed.**
  The selection runs only when THIS node builds a proposal; consensus then votes on one specific block
  and `ApplyBlock` takes the governance action from the block deterministically. Divergent proposer
  picks across rounds are normal BFT operation — one block commits per height. The scan is now over
  sorted keys anyway, for reproducibility.
- **D9 (identical payments collapse — user manual): not applicable to the wallet.** The wallet gives
  every payment a strictly-increasing `BlockID`, so two payments from one wallet can never have
  identical signed content. Documented for direct API clients in `operating-a-network.md` instead.
- **D10 (stale-block payment "silently never lands"): overstated.** Intake refuses a payment naming a
  past block with `409` and the wallet surfaces the error. The real residual case — a payment missing
  its named block in flight — is now a "receipt but no money moved" entry in the user manual.

---

## Release-candidate review — 2026-07-31

A fourth pass, focused on the commits added AFTER the 2026-07-08 rounds below — the pay-by-name /
on-chain-resolve / governed-message-size-cap / name-server range — run as a release-readiness check.
A multi-agent review of that range surfaced 10 distinct defects; each was confirmed against the code
before fixing. All are now fixed with regression tests, and both modules are `-race`-green.

**Fixed**
- **Unresolvable-recipient payments were free permanent block space (consensus/economic).** A
  signature-valid payment to an unregistered name (or naming both/neither a key and a name) was
  carried into a block by ProposeBlock/ApplyBlock but dropped by ProcessPayments before the fee — a
  zero-cost no-op defeating the anti-spam fee. Recipient resolution is now one shared function;
  ProposeBlock excludes such a payment, ApplyBlock rejects a block padded with one, and
  `node.admitPayment` refuses it at intake instead of returning a receipt for a no-op.
- **A duplicate proposal was re-relayed unverified (consensus/DoS).** `onProposal`'s held-proposal
  fast path relayed on a `Block.StateDigest` match — a field the proposal signature covers but the
  block BODY does not — so a copied digest could carry a swapped body, and the proposer (whose own
  broadcast bypasses relay dedup) would re-flood it. The fast path now never relays a duplicate;
  first-sight flood + `reGossipProposal` cover propagation.
- **Wallet key silently stored plaintext under a service stdin (security).** `interactive()` read
  `/dev/null` (a char device) as a terminal; it now confirms a real tty via `stty`, gives the
  sealed-wallet hint instead of a raw EOF, and warns loudly on any plaintext fallback.
- **Genesis `maxMessageBytes` of 1..511 bypassed the `MinMessageBytes` floor (safety)** — the config
  now clamps it, so a network can't start already bricked.
- **Pay-by-name / resolve UX (correctness):** a mistyped/truncated key is refused rather than signed
  as a no-op name; `RegisterName` pre-checks the update against the governed size cap (the node drops
  an over-bound update silently); `/hdls/resolve` tolerates a raw `+` in `?key=`; the wallet's
  same-block payment guard is persisted so a restart can't reissue a still-pending block.

**Documented (not a code fix)**
- Adding a field to the state digest or a signing preimage (as this range did with `MaxMessageBytes`
  and `ToName`) diverges a mixed-version network. Not a v1 blocker (no prior deployed network); such
  a release is now documented as a coordinated re-genesis, not a rolling upgrade
  (`docs/operating-a-network.md`).

The independent human audit in `TODO.md` remains the real gate before significant value rides on it.

---

## Resolution — 2026-07-08

Worked through in batches, each `-race`-green and committed separately. Regression tests added for the
headline fixes (mixed-round commit rejection; governance-sequence replay including the spend-back-to-
zero re-mint).

**Fixed**
- Consensus: `Commit` round-scoped (#1, + test); driver halts on decide (#16); deterministic decision
  round (#17); prevote-quorum step≥prevote guard (#14); per-round allocation bound (#15); castVote
  sign-error documented (#19).
- Proposals authenticated — signed by the round's proposer, verified on receipt, relayed only if
  authentic (#2 / #8).
- Governance-certificate replay closed with a monotonic **governance sequence** in committed state:
  reassignment cycle-replay (#3), parameter cycle-replay (#6). Evidence age-window (#5).
- `Account.Address` bound into the state digest (#4). Gossip payment verified at ingress (#9).
- Snapshot write is crash-safe (fsync temp + dir) (#11); catch-up re-entry guarded (#13); snapshot
  pointer-sharing invariant documented (#12).
- Ledger robustness: content-hash type tag (#23); receipt keyed by PaymentID not signature (#22);
  stable digest sort (#24); nil transaction-cost guarded (#24); package doc corrected (#24).
- UI: cross-site (CSRF) guard on mutating endpoints (#27); receipt "verified ✓" now checks node
  signature + PaymentID match + node-is-validator (#28).
- Overdraw intake guardrail (the design-decision section's recommendation, added 2026-07-08): every
  intake (client `/hdls/pay` AND gossip ingress) refuses — with no receipt — a payment whose amount
  plus the payer's already-queued outgoing exceeds its committed balance, and the pool is re-checked
  after each commit (`mempool.dropOverdrawn`). An honest node can neither hold nor propose an
  overdrawing payment set, so a committed overdraw now requires a misused/cloned key or a proposer
  that bypassed intake. (Framing corrected in the second pass: the ban falls on the PAYER, not on
  that proposer — see the design-decision section for the residual vector and the wallet-side
  closure.) The ban itself stands as designed.
- Docs drift: stake-weighted proposer (#30), committed-digest scope (#31), message types (#32),
  reassignment-vs-payment ordering (#20).

**Deferred (with reason)**
- Height continuity (#7, MEDIUM) — deferred as not exploitable, wrongly: the node checked the
  proposal's height but not its block's, so a proposer could re-apply old payments. Fixed in the node
  on 2026-10-27; the full ledger-layer fix (block height in `LedgerState`) is a further digest
  change, left for later.
- Unauthenticated write API / single-loop DoS (#10, MEDIUM) — a deployment concern (front the node with
  TLS + an allowlist / rate-limit) rather than a code fix; the 1 MiB body bound stays as the in-process
  guard.
- Variadic `validators` footgun (#25) and `ApplyBlock` bool-vs-error (#26) — ergonomics/design, no
  behavioural bug (the node always passes the set); left to avoid churn.
- f+1 skip counts vote types separately (#18, LOW) — strictly safe, only slower catch-up.
- `uint32` length prefix in `signing.Preimage` (#24, LOW) — unreachable (fields bounded by the 1 MiB
  request); changing it would break every signature. Accepted.
- `height()` returns 0 on node error (#29, LOW) — benign (the follow-up POST fails; no bad payment
  lands); the block label is cosmetic. Accepted.
- Content-dedup drops genuinely-identical payments (#21, LOW) — documented (vary the Comment).

The pattern the review named (value-bound replay guards) is closed by the governance sequence; the
consensus HIGHs (mixed-round commits, unauthenticated proposals) are fixed and, for the commit, tested.
The Jepsen-style adversarial harness in `TODO.md` was the highest-value next step — since built
(`consensus/sim_test.go`, `node/chaos_test.go`); it surfaced a partition-merge liveness stall, fixed
by vote re-gossip.

---

## Second pass — 2026-07-08 (independent re-review of the resolution)

Every fix above was re-verified against the code — all confirmed, full suite `-race`-green — and the
loose ends that pass found are now closed:

- **Overdraw design decision, framing corrected + wallet-side closure.** The resolution claimed the
  ban "only reaches a misused/cloned key or a proposer that bypassed intake". The ban actually falls
  on the *payer*, who can be honest: the wallet named `BlockID = height + lookahead`, so paying two
  people in quick succession signed two same-block payments; honest intake refuses the second, but
  the refusing node already holds both signatures — a *malicious* intake node could hoard the pair
  and, as a later proposer, include both in a perfectly valid block, destroying the payer's whole
  balance (unslashably, at no cost to itself). Closed at the source: the wallet now enforces
  strictly increasing BlockIDs across its payments (`ui/wallet.go`, `nextPaymentBlockID`), so it
  never signs two payments naming the same block and the pair a hoarder needs cannot come from an
  honest wallet. The ledger behavior itself is unchanged, as accepted.
- **Missing regression tests added**: evidence age-window, both bounds (`TestEvidenceAgeWindow`);
  nil transaction-cost (`TestNilTransactionCostTreatedAsZero`).
- **`TwoThirdsMajority` determinism invariant documented** (`consensus/voteset.go`): its map
  iteration is order-independent only because a strict >⅔ quorum admits at most one digest per set.
- **Proposal re-gossip** (closing the vote re-gossip caveat in `TODO.md`): the current round's
  proposal is now re-sent on the same cadence as votes, so a round whose proposal broadcast is lost
  decides instead of burning the round; receivers dedup before re-running `ApplyBlock`. Regression
  test: `TestChaosLostProposalRecoversViaReGossip` (fails without the fix).
- Record-keeping: receipt verification (#28) lives inline in `Wallet.Send` — the review-time
  `VerifyReceipt` function no longer exists, so don't grep for it. The UI origin guard's
  deliberate no-header fallback (curl / the local operator) was already documented at the
  `sameOrigin` comment in `ui/main.go`.

---

## Third pass — 2026-07-08 (observations beyond the original review's scope)

Four systemic observations were raised after the second pass; three are resolved, one reframed:

- **Wallet→node trust in name resolution — FIXED (restores the original's semantics).** The wallet
  resolved a recipient name to a key by asking its own node, so a dishonest node could remap any
  name to its own key at payment time. Now the payment carries the NAME inside the signed content
  (`Payment.ToName`, exactly one of key/name allowed) and every node resolves it independently
  against the committed registry at apply time, using the pre-block owner (updates apply after
  payments, so a name moving in the same block cannot fork resolution). An unregistered name is a
  deterministic network-wide no-op — nothing is debited — which also makes typos safe. This is how
  the original DistributedLedger did it; the Go port had regressed to wallet-side resolution.
- **Receipt "verified ✓" checks the validator set via the same node — REFRAMED, accepted.** The
  check is a UI convenience, not the trust anchor: a signed receipt is a non-repudiable admission
  that THIS payment reached THAT node. If the payment then never lands, the receipt is transferable
  evidence — grounds to ban the node's operator and reimburse the payer by ⅔ reassignment (the
  treasury path). So a node that fakes the validator list to get a ✓ is manufacturing evidence
  against itself. Final assurance remains the block commit, as documented.
- **One fee could buy an arbitrarily large permanent ledger entry — FIXED.** Amounts are exact
  rationals and names/descriptions are free-form, with only the 1 MiB HTTP bound above them — so a
  single ~0.1-fee payment could lodge a ~1 MB entry in state that every validator stores and
  re-hashes into EVERY subsequent digest. New governed parameter `MaxMessageBytes` (genesis default
  2000, floor `MinMessageBytes` = 512 so governance cannot brick payments, 0 = unbounded) caps one
  serialized payment/update, enforced at intake (`admitPayment` / `evUpdate`), in `ProposeBlock`,
  and in `ApplyBlock` — a proposer that bypassed intake cannot smuggle one in. While plumbing it,
  fixed a latent pool bug: `proposalKey` keyed proposals by cost only, so two proposals sharing a
  cost but differing elsewhere fought over one pool slot; it now keys the full profile digest.
- **`wallet.json` stored the key in plaintext — FIXED.** The private key is now sealed like an SSH
  key: passphrase → PBKDF2-HMAC-SHA256 (600k iterations, vectors-tested, stdlib-only) → AES-256-GCM
  (`ui/keyfile.go`). Empty passphrase keeps the ssh-keygen-style plaintext form; legacy plaintext
  files load and are offered a one-time upgrade; `GOLEDGER_WALLET_PASSPHRASE` serves scripts. A
  wrong passphrase or corrupt file is now an error instead of silently generating a fresh key over
  the old file.

Still open, by design: the ledger is fully transparent (every balance, name, payment and comment is
public, permanently once an archive node exists) — legibility is the governance mechanism here, but
the user manual should keep saying it plainly.

---

## Consensus-affecting

> The overdraw item below is an **accepted design decision**, kept here for the record. Everything
> after it in this section is a genuine defect to fix before value rides on the network.

### DESIGN DECISION (accepted) — Overdrawing payer's whole balance is destroyed
`ledger/ledger.go:119`

A payer whose total outgoing across a block exceeds their balance is *banned*: their payments are
skipped (`ledger.go:100`) **and** their balance from before the block is destroyed, so it vanishes
from supply. Trace: A holds 10, two signed 6-unit payments in one block → `outgoing[A]=12 > 10` →
banned → A is rebuilt from a zero balance → A's 10 units are destroyed. Credits A receives in the
same block still land, and a named A stays on the ledger with its name, at zero if nothing lands.
(This trace first said A was dropped from the result entirely. In fact a banned payer that also
received a payment in that block kept its whole balance, until 2026-10-27.)

**This is intentional network behavior, not a bug to fix.** An account that tries to overdraw is
sent to the shadow realm — the balance disappearing is an accepted consequence and does not threaten
the design (it can only *reduce* supply, never mint; the consequence falls entirely on the account
that overdrew). The one-line "keep the balance" change is therefore explicitly declined; the current
line 119 stands as written.

The trigger needs two individually-valid, genuinely-signed payments from the same payer in one block
— e.g. an honest wallet paying two recipients with a fee miscount, a resubmission, or a proposer
assembling both from gossip.

**Recommended guardrail (defense-in-depth) — IMPLEMENTED 2026-07-08:** at payment intake, refuse a
payment — and return no receipt — if the payer's already-promised outgoing plus this new payment
would overdraw (`node.admitPayment`, applied at `/hdls/pay` and at the gossip ingress, plus a
post-commit re-check `mempool.dropOverdrawn`). This keeps the honest flow sane without changing the
ledger's accepted behavior for a block that already contains an overdraw.

**Correction (second pass, 2026-07-08):** the guard does NOT mean the ban only reaches bad actors —
the account destroyed is the *payer's*, and the payer can be honest. A malicious intake node can
keep the two same-block signed payments an honest wallet handed it (accepting one, refusing the
other) and, when it is later the proposer, include both: the block is fully valid, commits, and
destroys the payer's entire balance at no cost to the proposer. So the wallet now also enforces
strictly increasing BlockIDs across its own payments (`ui/wallet.go`, `nextPaymentBlockID`) — it
never signs two payments naming the same block, so an honest wallet can no longer produce the pair
this attack needs, whatever any node tells it. The ledger's accepted behavior is unchanged.

Test note: `conservation_test.go`'s overspend case only asserts supply never *increases*
(`Cmp(...) > 0`), which correctly matches the intended behavior (supply may drop). No change needed;
just be aware the suite does not — and by design should not — assert supply is *exactly* preserved.

### HIGH — `Commit` certificate is not round-scoped
`consensus/commit.go:10-36`

`Commit` carries `Height`, `BlockDigest`, `Votes` but no `Round`, and `Verify` never checks the
precommits share a round. The quorum-intersection safety argument only holds *within one round*:
an honest validator can legitimately precommit X at round r (locked), unlock via a proof-of-lock,
and precommit Y at r′ — both durable, neither slashable (equivocation is per-round,
`evidence.go:15`). Across a multi-round height an adversary with ≤⅓ stake can therefore assemble a
mixed-round set of 2f+1 precommits for a block that was never decided, and it passes `Verify`.

This matters because `Commit.Verify` is the trust anchor for onboarding/restart/checkpoint sync
(`node/node.go:341`, `node/sync.go:72`, `node/persist.go:30`) — a forged certificate can make a
joining node adopt a conflicting state. The driver's own certificates are safe (single-round
`VoteSet`), but the verifier can't tell them from forgeries. Fix: add `Round` to `Commit`, require
`v.Round == c.Round` in `Verify`.

### HIGH — Proposals are unauthenticated and first-one-wins
`consensus/driver.go:183`, `node/node.go:391-399`, `node/messages.go:31-36`

The driver's doc comment delegates "checked to be from the round's proposer" to the caller; the
caller (`onProposal`) never compares against `Driver.Proposer(round)`, and `ProposalMsg` carries no
signature. The driver keeps only the *first* proposal per round. Consequences: (i) any machine —
not even a validator — can front-run the legitimate proposer, making stake-weighted selection
decorative; (ii) front-running every round with an *invalid* block makes honest nodes prevote nil
forever — a permanent, unauthenticated liveness kill from a single box. Needs a signed
`Proposal{Height, Round, Digest, ValidRound, Proposer, Signature}` and a driver-side proposer check.

### HIGH — Reassignment certificate replay when a balance cycles
`ledger/reassignment.go:82`

The only replay guard is `AccountAmount(current, r.Account) != r.PreviousAmount`. Since
`AccountAmount` reports a missing account as 0, the documented use case (fund a newcomer,
`prev=0, new=X`) becomes re-mintable *forever*: each time the grantee spends to zero, any proposer
can re-include the old certificate and mint X again, with no new votes. Bind a freshness component
(block height, expiry, or the prior-state digest) into the vote preimage (`reassignment.go:44-53`).

### HIGH — `Account.Address` is excluded from the state digest
`ledger/digest.go:26-36`

`StateDigest` hashes PublicKey, Name, Amount, ValidatorNode, Description — but not `Address`,
which is mutable on-chain state (`account_update.go:101`) and drives gossip peer selection
(`peerdns.go:30`). A synced checkpoint is verified by recomputing the digest against the commit
(`node/node.go:344`), so a malicious peer can rewrite every validator's gossip address and the
checkpoint still verifies — an eclipse-attack vector on any syncing node. Add
`field([]byte(a.Address))` to the digest. Breaking change; do it before shipping.

---

## Certificate / state-cycle replay (same root cause as reassignment)

### MEDIUM — Equivocation evidence is replayable against recreated accounts
`ledger/block.go:45-62`, `consensus/evidence.go:11-22`

Evidence validity is timeless and nothing records consumed evidence. Slashing is deletion
(idempotent) — until the account is recreated (`ProcessPayments` recreates any paid-to key,
`ledger.go:126`). A proposer can then re-include old evidence and burn the newly received funds.
Track consumed evidence, enforce an evidence-age window vs `block.Height`, or tombstone slashed keys.

### MEDIUM — Parameter-change certificate replay on parameter cycles
`ledger/governance.go:64`

Same "bind to previous value" flaw: a ratified A→B change is replayable any time parameters return
to exactly A (e.g. a later B→A revert lets a proposer unilaterally re-flip to B). Bind a height into
`parameterVoteFields`.

### MEDIUM — `ApplyBlock` does not enforce height continuity
`ledger/block.go:136-151`

`LedgerState` carries no height; `ApplyBlock` accepts any `block.Height` as long as
`PrevStateDigest` matches. Since a payment's replay guard is `BlockID == block.Height`, a pure-ledger
caller not independently enforcing monotonic height could double-apply a payment across two blocks
both claiming height N. This review took the `node` layer to guard it (`node/node.go:286,299`) and
called it not currently exploitable. That was wrong: the node checked the proposal message's height
but never its block's, so a scheduled proposer could label its block with an old height and re-apply
the payments signed for that height, payments to itself included. Since 2026-10-27 the node refuses a
proposal or a commit whose block names another height. Adding height to `LedgerState` (and its
digest) is still the ledger-layer fix.

---

## Node — untrusted-input boundary (liveness / DoS)

### HIGH — Incoming proposals not checked against the round's proposer
`node/node.go:391-400`

See the consensus HIGH above; restated here because the enforcement point is in the node. The
driver records the first proposal per round, so racing a different valid block to different nodes
splits the vote → round times out → repeat per round → indefinite stall. Not a safety break; a cheap
unauthenticated griefing vector. Untested.

### HIGH — Gossip payment intake is unverified, feeding the bounded mempool
`node/httptransport.go:146-153`

`handleGossipPayment` calls `SubmitPayment` with no `VerifyPayment` (unlike `handlePay` at :210).
`mempool.add` dedups by content hash only. An unauthenticated attacker fills the mempool
(`MaxMempool`, default 10000) with junk; `ProposeBlock`→`ValidPayments` then re-verifies every
pending Ed25519 signature *every block* on the single loop goroutine, and `dropBelow` only evicts
`BlockID < height` — so payments with a far-future `BlockID` stay resident and re-verified forever,
while real payments are starved by the full-mempool backpressure. Verify the signature at the gossip
ingress before admitting to the mempool.

### MEDIUM — Unauthenticated write API + single-loop serialization = DoS surface
`node/httptransport.go:104-118`

Every mutating endpoint is open (no TLS, no allowlist, no rate limit); the 1 MB body bound is the
only guard. A 1 MB proposal triggers a full `ApplyBlock` (digest over all accounts + verify every
carried payment) on the event loop. `Snapshot()`/`GovernanceView()`/`enqueue` select only on `quit`,
so a saturated loop makes HTTP handler goroutines block indefinitely and pile up. The actor model
that makes the node race-free also makes it a single chokepoint that needs an admission/rate-limit
layer in front.

### MEDIUM — "Atomic" snapshot write is not crash-safe
`node/persist.go:65-70`

`writeAtomic` does `WriteFile(tmp)` then `Rename` with no fsync of the temp file before the rename
nor of the directory after. On power loss the rename can be durable while the data blocks are not, so
a committed checkpoint can be silently lost (then fails JSON-parse on load → restart at genesis).
Impact is bounded (a networked node re-syncs from peers), but the "durable crash recovery" framing is
overstated, and `persist_test.go` only exercises a graceful `Stop()`. fsync the temp file, then the
dir.

### LOW — `Snapshot` shares `*big.Rat` pointers with live state
`node/node.go:261-273`

`evQuery` copies accounts by value, but the `Amount` pointers are shared with the loop's live state.
Safe today only because `ledger.ProcessPayments` treats amounts as immutable (allocates fresh
`big.Rat`). "Race-free by construction" is really "…+ immutable-value discipline in `ledger`"; one
future in-place mutation introduces a race current tests wouldn't surface. Worth a comment on the
invariant, or a deep copy.

### LOW — `catchUp` can be re-triggered by a stale `evNextHeight`
`node/node.go:309-321, 492-496`

`catchUp` calls `startHeight()`; a previously scheduled `evNextHeight` (or a redundant `evCatchUp`)
firing afterward calls it again mid-height, discarding the fresh driver's in-progress votes. Narrow,
self-correcting, but an unguarded re-entry into height start.

---

## Consensus driver — Tendermint deviations

### MEDIUM — Prevote-quorum one-shot lacks the `step ≥ prevote` guard (algorithm line 36)
`consensus/driver.go:316-327`

The rule fires and consumes `gotPrevoteQuorum[r]` even at `StepPropose`. Reachable case: a
re-proposal with `ValidRound ≥ 0` whose POL prevotes haven't arrived, then 2f+1 current-round
prevotes for the digest arrive — the one-shot is consumed at propose step, the lock/precommit branch
is skipped, the flag never resets, so the node never locks/precommits that value and times out to nil
where Tendermint commits. Safety preserved; silently degrades rounds. Guard with `step >= StepPrevote`.

### MEDIUM — Unbounded per-round allocation from attacker-chosen rounds
`consensus/driver.go:192-206`

`ensureRound(v.Round)` allocates for any validly-signed vote at any `int32` round. One rogue
validator key can sign prevotes at rounds 1..10^6 and force two `VoteSet`s per round — memory
exhaustion. Window vote acceptance near the current round.

### LOW — Driver keeps voting after deciding
`driver.go:255-265, 301-344`

The prevote/precommit rules aren't guarded by `!d.decided`, so a late vote/proposal can still emit a
vote after decision. Cannot produce a conflicting commit under ≤f Byzantine, but Tendermint halts the
height on decide and this should too.

### LOW — Nondeterministic map iteration in decision / majority selection
`driver.go:272`, `voteset.go:78`

Iterating `d.precommits` / `s.power` in map order makes the deciding round nondeterministic across
nodes when two rounds hold quorums for the same digest. Harmless today; becomes visible the moment
`Commit` gains a `Round` (finding above).

### LOW — f+1 skip counts vote types separately
`driver.go:286-298`

Requires >⅓ power within a single vote set, vs Tendermint line 55 counting f+1 messages of any kind
from the higher round. Strictly safe, just slower catch-up.

### LOW — `castVote` swallows signing errors
`driver.go:232-235`

Returns nil actions and skips `evaluate`, silently losing the transition. Unlikely with Ed25519, but
surface it.

---

## Ledger — lower-severity correctness / robustness

### LOW — Reassignment validated pre-block but applied post-payments
`ledger/block.go:107 vs 114-116`

A payment crediting the target in the same block is silently overwritten by the override (payer
debited, fee burned, transfer vanishes). Deterministic and consistent, but validate against
post-payment balances or document.

### LOW — Content-dedup silently drops identical payments
`ledger/ledger.go:77-82`

Two genuinely intended identical payments (same payee/amount/comment/block) collapse to one. Correct
as a malleability defence, but a UX footgun — wallets must vary `Comment`. Document it.

### LOW — `Receipt` identifies payments by signature bytes, not content ID
`ledger/receipt.go:19,34`

`PaymentID` exists precisely because signature bytes aren't a reliable identity, yet receipts bind
`PaymentSig`. Use `PaymentID(p)`.

### LOW — `hashFields` lacks a type tag
`ledger/payment.go:23-32`

`PaymentID` and `UpdateID` share an untagged 6-field hash, so cross-type ID collisions are
constructible. Harmless (separate dedup pools) but one shared map from a bug. Prefix a domain byte.

### LOW — Assorted robustness nits
- `ledger/ledger.go:71` — `Amount.Cmp(transactionCost)` panics if `Parameters.TransactionCost` is nil.
- `ledger/digest.go:17` — non-stable `sort.Slice` on `PublicKey` alone → nondeterministic digest on
  duplicate keys. Use `SliceStable` or reject duplicates.
- `signing/domain.go:48` — `uint32(len(b))` truncates for fields ≥ 4 GiB; `hashFields` uses `uint64`.
  Inconsistent framings; make both `uint64`.
- `ledger/ledger.go:1-4` — package doc claims "no crypto"; false since block/payment verify signatures.

### Design nits
- Variadic `validators ...*consensus.ValidatorSet` (`block.go:68,136`, `governance.go:33`) silently
  drops ratified governance content if the arg is forgotten. Make it explicit and nil-able.
- `ApplyBlock` returns a bare `bool` for eight distinct rejection reasons; an `error` with sentinels
  would make debugging far easier.

---

## UI (wallet)

### HIGH — CSRF / drive-by payments — no origin check on mutating endpoints
`ui/main.go:44-48`

`/send`, `/register`, `/admin/parameter`, `/admin/reassignment` are plain POST handlers reading
`r.FormValue(...)` — accepting `application/x-www-form-urlencoded`, exactly what a cross-site
auto-submitting form sends with no CORS preflight. The wallet listens on localhost and auto-signs
with the local key, so any page the user visits while it runs can silently trigger a signed payment
or governance vote. Add a SameSite/CSRF token or an `Origin`/`Sec-Fetch-Site` same-origin check.

### MEDIUM — Receipt "verification" is weaker than the UI implies
`ui/wallet.go:177-179`

`VerifyReceipt` checks only that *some* key signed the receipt for the network — not that `r.Node` is
a validator, nor that `r.PaymentSig` matches the payment just sent (the field exists,
`ledger/receipt.go:19`, but is ignored). Yet the UI shows "verified ✓" (`ui/main.go:85`,
`USER_MANUAL.md:54`). A dishonest non-validator node can self-sign a receipt over an unrelated payment
and it reads as verified. Check node ∈ validator set and `PaymentSig` equality.

### LOW — `height()` returns 0 on node error, mislabeling the block
`ui/wallet.go:123-134`

Ignores the decode error and returns 0; a payment built right after names `BlockID = 0 + lookahead`
and the page shows "block 0" as authoritative. (Subsequent POST also fails, so no bad payment lands.)

---

## Docs — accuracy drift (prose lags code)

- `docs/consensus.md:50,90-91` — says proposer selection is "uniform round-robin"; the code is already
  stake-weighted (`consensus/validator.go:59-82`).
- `docs/architecture.md:31` — says `StateDigest` (amounts) is "the single value consensus commits";
  the committed digest covers accounts **and** parameters (`ledger/parameters.go:92`). `consensus.md`
  and `governance.md` get this right; `architecture.md` is stale.
- `docs/architecture.md:74-76` — a `Message` is described as a block or a `Vote`; there are six message
  types (`node/httptransport.go:56-71`).

Spot-checks that **passed** (docs match code): fee-burned/no-block-rewards, reassignment mint bound
to current balance + non-negative + ⅔-gated, supply integrity B1/B2, malleability-proof content IDs,
⅔ quorum intersection, 1 MiB body bound, governed timing coupling, owner-signed account updates, and
all `operating-a-network.md` config/route names.

---

## Strengths (worth preserving)

- `signing` is textbook domain separation: length-prefixed, protocol/network/domain/field-bound,
  injective preimage, no raw-sign escape hatch, and tests that attack the properties.
- Content-addressed malleability defence: dedup/replay keyed on `PaymentID` (hash of signed content),
  independent of the crypto lib rejecting non-canonical signatures.
- Propose/apply symmetry: `ApplyBlock` independently recomputes every asserted effect.
- The actor-model node is real and verified — `-race` passes over genuine multi-node HTTP tests
  (4-node all-to-all, 9-node Hamming-graph relay, catch-up, replay).
- Onboarding verification is sound and honest: real >⅔ quorum + digest binding + >⅓ anchor overlap
  (BFT skipping-trust), with the full-rotation limit documented, not glossed.
- The consensus safety core is faithful Tendermint: correct quorum math, a properly-implemented
  locking rule with proof-of-lock verified against locally-received prevotes, and elegant,
  unforgeable equivocation evidence carried into blocks for slashing.
- `big.Rat` end-to-end money with canonical `RatString`, copy-on-write throughout, deterministic
  sorted output.
- Zero external dependencies in both modules; unusually high comment quality with traceable
  audit-finding IDs.
- Docs are honest and well-structured; `managing-the-currency.md` is economically coherent and
  refuses to oversell.

## The pattern behind the serious findings

Two of the HIGHs plus two MEDIUMs share one root cause: **replay guards bound to a *value* (previous
balance, previous parameter) rather than a *height/nonce*** — which fails whenever state cycles back.
Adding a height to `LedgerState` and into the certificate preimages closes several at once. The
consensus HIGHs (mixed-round commits, unauthenticated proposals) are properties the tests confirm
*where guards exist* but never probe at the boundaries — state cycles, the POL/unlock path, commit
round-scoping, multi-driver interaction. The Jepsen-style adversarial harness named in `TODO.md` is
the highest-value next step and would likely have caught them.
