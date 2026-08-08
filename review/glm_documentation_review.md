**REFERENCING v1.0.0 — NOW ADDRESSED.**

# Documentation Review — go-ledger

> **Status: resolved — no pending items.** This review was run against the **v1.0.0 release**
> (commit `0ac193b`) and is kept verbatim. The warnings it found missing are now in the user
> manual, wallet README and operator guide; D9/D10 were found overstated on verification (the
> wallet's strictly-increasing `BlockID` already prevents the D9 collapse, and a stale payment is
> refused with `409`, not lost silently). Full dispositions: [`findings.md`](../findings.md),
> *External review (GLM) — 2026-08-02*.

**Reviewer:** GLM (automated review)
**Date:** 2026-08-02
**Release reviewed:** v1.0.0 (commit `0ac193b`)
**Scope:** the user- and operator-facing docs — `README.md`, `docs/` (architecture, consensus,
security, governance, operating-a-network, managing-the-currency), `ui/README.md`,
`ui/USER_MANUAL.md`.
**Method:** each doc's claims cross-checked against the source; then the source scanned for
landmines a user/operator would hit that the docs fail to warn about.

> **What this review is, and isn't.** A separate code review (`glm_code_review.md`) verifies that
> the docs *match* the code — and they do, to an unusually high degree: every factual claim spot-
> checked (config fields, route paths, quorum math, defaults, the audit "Fixed" items) was accurate.
> This document does **not** re-litigate accuracy. It focuses on a different question: **where do the
> docs fail to adequately warn a user or operator about a behaviour that would bite them?** A code
> landmine with no doc warning is an actionable doc gap even when the code is "working as designed."

---

## Executive summary

The documentation is honest, well-structured, and accurate (verified separately). The gap is almost
entirely on the **user-facing** side: the end-user manual (`ui/USER_MANUAL.md`) does not warn about
the single most dangerous behaviour in the codebase — that running the same wallet key on two
machines, or restoring a backup without its `.state` sidecar, can destroy the user's *entire*
balance. The operator guide (`docs/operating-a-network.md`) is strong on config/route names but
omits the deployment-hardening warnings that live only in `docs/security.md` — the doc an operator
might not read.

Findings are ordered by user impact: money-loss and network-bricking first.

---

## Tier 1 — Can lose a user their money or brick an operator's network

### D1 — The overdraw ban is not in the end-user manual ★★★★★

**Code:** `ledger/ledger.go:155-209` (`ProcessPayments`). A payer whose total outgoing across one
block exceeds their balance is *banned*: their payments are skipped **and** their account is dropped
from the result entirely (`:188` excludes banned accounts from the pass-through loop; banned keys
never enter `affected` at `:192`), so their **entire remaining balance vanishes from supply**. This
is an accepted design decision (`findings.md:181-198`): "A holds 10, two signed 6-unit payments in
one block → banned → A's 10 units are destroyed." It can only *reduce* supply, never mint.

**Docs:** Documented in `docs/security.md:74-81` (a developer/threat-model doc) and listed in
`README.md:78-81` (a status checklist). **Not mentioned anywhere in `ui/USER_MANUAL.md`** — verified
by full-text search (no occurrence of "overdraw", "ban", "destroy", or "two machines"). The user
manual's "Common questions" section (`ui/USER_MANUAL.md:83-96`) covers sending to the wrong person,
a balance not changing, and not reaching the node — but never warns that the wrong key-handling can
destroy your whole balance.

**Why this is the top finding.** The wallet's strict-increasing-`BlockID` guard
(`ui/wallet.go:168-178`, persisted to `<keyfile>.state`) is the defence that closes this attack at
the source — but it only protects *within one wallet process*. Its own comment (`ui/wallet.go:36`)
names the residual case: "the SAME key loaded on two machines — documented; there, register-then-pay
and modest amounts." That "documented" caveat is in a **code comment**, not in the user manual. A
user who follows the manual's "keep a safe backup of that file" advice (`ui/USER_MANUAL.md:30-31`),
restores the backup to a second machine, and pays from both, can lose everything — and nothing in the
manual they read warns them.

**Suggested fix** (one paragraph, add to `ui/USER_MANUAL.md` near the backup advice):

> **One key, one machine.** Never run two wallets from the same `wallet.json` at the same time. If
> two payments from your key land in the same block, the network destroys your *entire* balance (not
> just the overdrawn amount) — this is by design, to make double-spending impossible. If you need to
> pay from a second machine, move the key (don't copy it), or pay yourself to a fresh key on the
> second machine first.

---

### D2 — The `.state` sidecar file is not in the backup instructions ★★★★

**Code:** `ui/wallet.go:64-85` persists a `walletState` (the strict-increasing `BlockID` counter) to
`<keyfile>.state` before each payment. This counter is what prevents a restarted wallet from
re-issuing a block a still-pending pre-crash payment already named (`findings.md:111-120`).

**Docs:** `ui/USER_MANUAL.md:30-31` says "Make a backup somewhere only you can reach (a USB stick
…) — with a passphrase set, the backup itself is safe to store anywhere." It names only `wallet.json`.
The `.state` sidecar is **not mentioned anywhere** in `ui/USER_MANUAL.md` or `ui/README.md`.

**Why actionable:** A user who backs up only `wallet.json` (as instructed) and restores after a
crash does **not** restore the `.state` counter — silently re-opening the within-lookahead
double-issue window the counter exists to close. The backup instructions are actively misleading by
omission: they tell you to back up the key, but not the small file that makes the key safe to reuse.

**Suggested fix** (extend the backup paragraph in `ui/USER_MANUAL.md`):

> Back up `wallet.json` **and** `wallet.json.state` together — the second is a tiny counter that
> stops a restored wallet from accidentally issuing a payment that lands in the same block as one
> you already sent (see *One key, one machine* above). Without it, a restored wallet can re-open a
> narrow double-spend window.

---

### D3 — `transactionCost` must be positive — not warned in the operator guide ★★★★

**Code:** `cmd/server/config.go:74-77` parses `transactionCost` as a `*big.Rat` and checks only
`SetString` success — not that the value is positive. `load()` never calls
`LedgerParameters.Acceptable()` on the genesis parameters. A `transactionCost` of `"0"` or `"-1"` is
accepted; with a negative cost, `Amount.Cmp(transactionCost) < 0` (`ledger/ledger.go:128`) is true
for every positive amount → **every payment is dropped → the network is silently bricked at genesis**,
with no error. (This is a code bug — see `glm_code_review.md` C3 — but the doc gap is independent:
there is no warning even for a *zero* cost, which the ledger accepts as "no spam protection.")

**Docs:** `docs/operating-a-network.md:39` says only:
> `| transactionCost | fee burned per payment, exact decimal string, e.g. "0.1" |`

No warning that it must be positive; no warning that a bad value bricks the network silently.

**Suggested fix** (extend the config-table row in `docs/operating-a-network.md`):

> `| transactionCost | fee burned per payment; must be a **positive** exact decimal, e.g. "0.1". Zero
> or negative silently disables the spam fee / bricks the network. |`

(And fix the code to call `Acceptable()` — see `glm_code_review.md` C3.)

---

### D4 — Dynamic-validator mode crashes at startup — no operator note ★★★★

**Code:** `cmd/server/main.go:31` logs `cfg.Validators.TotalPower()`. When `validatorThreshold` is
set, `config.go:102-109` leaves `cfg.Validators` **nil** (the set is derived from stake at runtime),
so `TotalPower()` (`consensus/validator.go:43`) dereferences a nil pointer → **panic at startup**.
This is a code bug (`glm_code_review.md` C1), but it affects the docs: an operator who wants dynamic
validators — a documented, marketed feature (`README.md:60-63`) — will set `validatorThreshold`,
follow `docs/operating-a-network.md:43`, and watch the binary crash with a nil panic, with no
guidance on what to do.

**Docs:** `docs/operating-a-network.md:43` documents that setting `validatorThreshold` makes the set
dynamic; `docs/consensus.md:113-115` documents the skipping-trust rule. Neither warns that this mode
has any setup caveat.

**Suggested fix:** fix the code (one-line guard, `glm_code_review.md` C1). Until then, add to
`docs/operating-a-network.md` near the `validatorThreshold` row:

> Note: dynamic-validator mode currently panics at startup (nil validator set) — use the fixed
> `validators` set until this is fixed.

---

## Tier 2 — Operator hardening (warnings live in the wrong doc)

### D5 — No TLS / no auth on mutating endpoints — warned in `security.md`, NOT in the operator guide ★★★

**Code:** `cmd/server/main.go:33-35` — `http.Server` with no TLS; every mutating endpoint
(`/hdls/pay`, `/hdls/update`, `/hdls/parameter-vote`, `/hdls/reassignment-vote`, peer `/hdls/proposal`,
`/hdls/vote`) is open (`findings.md:317-325`).

**Docs:** `docs/security.md:96-100` warns clearly: "Keep the network off the public internet unless
you have added transport security and access control… do not port-forward it. There is currently no
TLS or peer authentication at the transport layer." **But `docs/operating-a-network.md` — the doc an
operator follows to deploy — says nothing about TLS, auth, or allowlisting.** Worse, its example
config (`docs/operating-a-network.md:65`) shows `"listen": "0.0.0.0:8080"` (all interfaces) with no
warning that this exposes open mutating endpoints to the whole LAN.

**Why actionable:** The warning exists, but in a different doc than the one an operator naturally
reads when deploying. An operator following only the operator guide deploys an open node.

**Suggested fix:** add a "Network exposure" subsection to `docs/operating-a-network.md`:

> **The node API has no TLS and no authentication** — every mutating endpoint is open. Bind to
> `127.0.0.1` or keep the node on an isolated LAN; never port-forward `listen` to the public internet
> without a TLS-terminating reverse proxy + allowlist in front. See `docs/security.md`.

---

### D6 — `backupPath` on NFS / network filesystem — the operator guide suggests a NAS but doesn't warn ★★

**Code:** `node/persist.go:65-92` (`writeAtomic`) — temp file + `os.Rename` + fsync. The comment at
`:65` says "atomic on the same filesystem." `os.Rename` on NFS/CIFS is **not** atomic and can fail or
leave partial state. The backup copy is async (`node/persist.go:56-62`), so a hung mount won't stall
consensus, but a rename failure is silently ignored.

**Docs:** `docs/operating-a-network.md:48` says `backupPath` is "optional second path (e.g. a mounted
share/NAS)… for off-machine durability." It **actively suggests a NAS/share** — exactly the case
where atomic-rename semantics don't hold — with no warning.

**Why actionable:** An operator following the doc's own NAS suggestion may get silently-failed or
torn backups and believe they have durability they don't. The doc oversells by omission.

**Suggested fix** (extend the `backupPath` row in `docs/operating-a-network.md`):

> `| backupPath | optional second path for off-machine durability. **Atomic-rename durability is
> local-filesystem only**; on NFS/CIFS a rename may not be atomic and a failed mirror is silently
> ignored — prefer a local disk, or verify the mount supports atomic rename. |`

---

### D7 — No request timeout / DoS surface — not warned ★★

**Code:** the single event-loop goroutine serializes all work; `Snapshot()`/`GovernanceView()`/
`enqueue` select only on `quit`, so a saturated loop makes HTTP handler goroutines block indefinitely
and pile up (`findings.md:317-325`). The 1 MiB body bound is the only in-process guard; no rate
limit.

**Docs:** neither `docs/operating-a-network.md` nor `docs/security.md` warns the operator about the
DoS surface or tells them to rate-limit.

**Suggested fix** (add to the "Network exposure" subsection proposed in D5):

> A sustained flood of valid-looking requests can stall the single-threaded event loop; front any
> non-localhost exposure with a rate-limiting proxy.

---

### D8 — Snapshot durability slightly oversold in the operator guide ★

**Code:** `node/persist.go` — `saveSnapshot` is best-effort (errors ignored); per-node
(`docs/security.md:114-117`).

**Docs:** `docs/operating-a-network.md:108-112` says "persists its committed state to disk after
every block and reloads it on restart. That gives local recovery without needing a peer." Accurate
for the happy path, but omits that a write failure is silent and that a single node's snapshot loss
means re-sync from peers. `docs/security.md:114-117` is more honest and recommends periodic
`GET /hdls/accounts` exports for real value — but the operator guide doesn't repeat that.

**Suggested fix** (add to the snapshot section in `docs/operating-a-network.md`):

> Snapshot writes are best-effort (a failed write is silent) and per-node — a disk failure means
> re-syncing from peers. For real value, also keep a periodic `GET /hdls/accounts` export
> off-machine.

---

## Tier 3 — UX confusion (causes support load or mistaken "payment failed")

### D9 — Identical payments collapse to one — not in USER_MANUAL ★★★

**Code:** `ledger/ledger.go:143-146` — two payments with identical content (same payee/amount/
comment/block) collapse to one ("first occurrence wins"). The code comment at `:119-123` says "two
payments the payer genuinely means as distinct must differ in a field (e.g. the Comment)."

**Docs:** not in `ui/USER_MANUAL.md`. The "Sending a payment" section (`:50-63`) does not mention
the Comment field at all.

**Why actionable:** A user paying the same person the same amount twice in quick succession (e.g. two
invoice installments with no comment) would see only one land and assume the other failed silently.
The mempool dedup keys on content (`node/mempool.go:31-33`), so two identical payments pending at
once collapse to one.

**Suggested fix** (add to `ui/USER_MANUAL.md` "Sending a payment"):

> If you send two identical payments to the same person (same amount, no comment), the network treats
> them as one — add a different comment (e.g. an invoice number) to make each distinct.

---

### D10 — "Stale block id" / payment not landing after node unreachable — not explained ★★

**Code:** `ui/wallet.go:180-191` — `height()` returns `0` on any node error or decode failure. A
payment built while the node is unreachable names `BlockID = 0 + lookahead = 3`; if the chain is
already past block 3, the payment is stale and never lands (`node/mempool.go:96-106` `dropBelow`).

**Docs:** `ui/USER_MANUAL.md:88-92` covers "My balance didn't change" and "It says 'cannot reach
node'" but does **not** explain that a payment made while the node was unreachable may target a stale
block and silently never land — the user just sees "balance didn't change." The user has no model of
"block id" or "stale."

**Suggested fix** (add to `ui/USER_MANUAL.md` "Common questions"):

> If you sent a payment while the node was unreachable, it may name a block that already passed — it
> won't land and won't be debited. Wait until the wallet shows a current block height, then send
> again.

---

### D11 — Mempool full → payment refused — not in USER_MANUAL ★★

**Code:** `node/mempool.go:35-37` — if the pool is full (`maxMempool`, default 10000), `add` returns
false; `node/httptransport.go:161, 230` returns `409 Conflict` with no receipt.

**Docs:** `docs/operating-a-network.md:175` mentions the 409 refusal exists — but that's the
operator doc. `ui/USER_MANUAL.md` does not tell a user their payment might be refused under load.

**Suggested fix** (add to `ui/USER_MANUAL.md` "Common questions"):

> If the network is very busy, the node may refuse a payment with "mempool full" — wait a moment and
> send again; nothing was debited.

---

### D12 — Transparent ledger — not stated plainly in USER_MANUAL ★★

**Code/findings:** `findings.md:170-173` explicitly flags this: "the ledger is fully transparent
(every balance, name, payment and comment is public, permanently once an archive node exists) … the
user manual should keep saying it plainly." This is an open TODO from the project's own review.

**Docs:** `ui/USER_MANUAL.md` does **not** state that the user's balance, name, and payment comments
are visible to everyone, permanently. It implies visibility ("a list of all the accounts on the
ledger with their balances", `:41-42`) but never states the privacy implication. A user putting a
sensitive note in a payment Comment, or expecting their balance to be private, is not warned.
`docs/managing-the-currency.md:274-280` covers pseudonymity/privacy well — for stewards, not end
users.

**Suggested fix** (add a "Privacy" note to `ui/USER_MANUAL.md`):

> **Everything on this ledger is public** — your balance, your registered name, and the comment on
> every payment you send are visible to everyone on the network, permanently. Don't put private
> information in a payment comment.

---

### D13 — `-lookahead` flag missing from `ui/README.md` flag table ★★

**Code:** `ui/main.go:33` defines `-lookahead` (default 3, "blocks ahead a payment names (raise if
payments miss their block)").

**Docs:** `ui/README.md:22-25` flag table lists only `-node`, `-network`, `-wallet`, `-listen`.
`-lookahead` is **missing from the table**, though it is mentioned in the prose at `ui/README.md:44`.
A reader scanning the table (the natural reference) misses it.

**Suggested fix** (add a row to the flag table in `ui/README.md`):

> `| -lookahead | blocks ahead a payment names (raise if payments miss their block) | 3 |`

---

### D14 — Plaintext key fallback when run as a service / `GOLEDGER_WALLET_PASSPHRASE` — not in `ui/README.md` ★★

**Code:** `ui/keyfile.go:105-121` — empty passphrase stores plaintext. When stdin is not a terminal
(systemd, nohup), `interactive()` reads `/dev/null` and the wallet falls back to plaintext unless
`GOLEDGER_WALLET_PASSPHRASE` is set (`ui/keyfile.go:35`).

**Docs:** `ui/USER_MANUAL.md:25-27` does say an empty passphrase stores the file unlocked — good for
the interactive case. But the non-interactive/service case (where the fallback is *silent* unless the
env var is set) is **not in `ui/README.md` or the manual**, and the env var
`GOLEDGER_WALLET_PASSPHRASE` is not documented in `ui/README.md` at all. An operator running the
wallet as a service silently gets an unencrypted key.

**Suggested fix** (add a "Running as a service" note to `ui/README.md`):

> If stdin is not a terminal (systemd, nohup), the wallet key is stored **unencrypted** unless
> `GOLEDGER_WALLET_PASSPHRASE` is set — set it in your service environment to seal the key.

---

## Already well documented (no action needed)

For completeness, these were checked and are **not** gaps — the docs cover them honestly:

- **Pay-by-name: unregistered name is refused, money not debited** — `ui/USER_MANUAL.md:76-81` is
  clear and accurate.
- **Reassignment can mint or reduce/zero a specific balance (seizure by ⅔ vote)** —
  `docs/governance.md:55-61` and `docs/managing-the-currency.md:300-303, 376-381` document this
  thoroughly, including the capture risk.
- **`MaxMessageBytes` cap** — `docs/operating-a-network.md:51` documents the default (2000), the
  floor (512), and that it's governed. (Minor: `ui/USER_MANUAL.md:72-73` covers the registration-
  too-large case but not the payment-comment-too-large case — low value to add.)
- **Voting-power `int64` limits** — correctly out of scope for user docs; `docs/managing-the-
  currency.md` covers stake *concentration* risk appropriately.
- **Key size / `keygen` output** — `docs/operating-a-network.md:38` says "from keygen"; low value to
  add an explicit length warning, though the code should validate it (see `glm_code_review.md` C4).

---

## Summary — what to do, in priority order

**Fix first (money-loss / network-bricking) — all are one-paragraph doc additions:**
1. **D1** — overdraw ban destroys whole balance: add the "One key, one machine" warning to
   `ui/USER_MANUAL.md`.
2. **D2** — `.state` sidecar not in backup instructions: extend the backup paragraph in
   `ui/USER_MANUAL.md`.
3. **D3** — `transactionCost` must be positive: extend the config row in
   `docs/operating-a-network.md` (and fix the code, `glm_code_review.md` C3).
4. **D4** — dynamic-validator mode crashes: fix the code (`glm_code_review.md` C1); add an operator
   note until then.

**Fix second (operator hardening — warnings are written, just in the wrong doc):**
5. **D5** — add a "Network exposure" subsection (no TLS/auth) to `docs/operating-a-network.md`.
6. **D6** — `backupPath` NFS caveat: extend the config row in `docs/operating-a-network.md`.
7. **D7** — DoS/rate-limit: add to the "Network exposure" subsection.
8. **D8** — snapshot best-effort: add a line to the snapshot section in
   `docs/operating-a-network.md`.

**Fix third (UX confusion — small additions to `ui/USER_MANUAL.md`):**
9. **D9** — identical payments collapse: add to "Sending a payment".
10. **D10** — stale block after node unreachable: add to "Common questions".
11. **D11** — mempool full refusal: add to "Common questions".
12. **D12** — transparent ledger: add a "Privacy" note (the project's own review flagged this).
13. **D13** — `-lookahead` flag: add the missing table row in `ui/README.md`.
14. **D14** — plaintext fallback as a service / `GOLEDGER_WALLET_PASSPHRASE`: add a "Running as a
    service" note to `ui/README.md`.

Most of these are one or two sentences. The highest-leverage single change is **D1** — a user who
reads the manual and never learns the overdraw rule can lose everything, and the warning is a
paragraph.
