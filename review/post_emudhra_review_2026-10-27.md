# Post-eMudhra review — 2026-10-27

> **Status: open.** Every finding below was verified against the code as cloned on 2026-10-27
> (single commit `9a60643`). None is fixed yet. Proof-of-concept tests and prototype fixes were
> written in throwaway copies of the tree (paths in the appendix); this checkout was not modified.
>
> (2026-10-31: this report is kept as it was written. Its findings were worked through afterwards:
> [`../findings_20261027.md`](../findings_20261027.md) records what was fixed and what was not.)
>
> Code quotes, names and line numbers below refer to that commit. The fixes are listed in
> [`../findings_20261027.md`](../findings_20261027.md), and the names in the code were spelled out
> afterwards: [`renames_2026-10-27.md`](renames_2026-10-27.md) maps the old names to the new ones.

## Bottom line

- **45 weak points confirmed:** 1 critical, 11 high, 20 medium, 10 low and 3 informational. One
  claim was refuted. Each finding was reproduced through the production code paths, most with a
  test that fails on today's code.
- **The eMudhra failure can't happen here:** every key comes from `crypto/rand`. The burn rule does
  not hold yet, though. Slashing deletes an account but leaves the key voting, and an honest
  validator that reboots mid-height signs against itself.
- **The three worst:**
  - a single validator can re-apply payments from earlier blocks, including payments to itself (E1);
  - a validator restart can fork the chain with no dishonest validator involved (E2);
  - in dynamic-validator mode, nodes trust the genesis keys forever, so a restart can silently roll a
    node back to genesis and a node that falls behind never catches up (E3).
- **Anyone who can reach a node's port** can freeze governance for good (E4), stall a node for
  seconds per message (E6), or halt the chain with free account updates (E8). The planned edge proxy
  does not remove these, because it must forward the wallet and admin routes they use.
- **Most fixes are small.** Most are under 20 lines; the largest is the sign log, at about 140. The
  format changes and the agreed new features fit into one new genesis.

## Why this review

In October 2026 eight eMudhra intermediate CA keys turned out to hold only 3–8 bits of entropy: a
key-ceremony script error produced a weak second key set that was used by mistake. The keys were
recoverable from the public certificates and stayed exposed for about 17 months. This review asks
two questions of go-ledger in that light: could the same failure happen here, and does the owner's
rule — "two different signings means the key is burned" — catch a leaked or weak key?

## How it was run

Three passes, each by independent agents working on private copies of the tree:

1. **Find** — nine reviewers, one per area: key lifecycle, the burn rule, consensus core, ledger
   state transition, governance, sync/trust anchors, node input boundary, wallet UI, and the flaky
   test plus the full `-race` suite. 53 findings, merged into 36 clusters.
2. **Verify** — eight groups tried to refute every cluster by re-running or rewriting the PoCs
   through production paths, plus a completeness critic. Two hypotheses from the lead were added.
   Result: 35 confirmed, 2 plausible, 1 refuted, and 19 new findings.
3. **Verify the new ones** — five more groups re-checked the 19 new findings with their own tests,
   several on real multi-process networks running `cmd/server`. All 19 held up. Two duplicated
   others in the batch, and one is a limit of a suggested fix rather than of today's code. This pass
   also showed that two suggested fixes don't work (noted under E2 and E4).

Findings with one root cause are merged below, which gives 45. Severity follows the threat model in
`docs/security.md`: one dishonest validator, anyone on the LAN, sleeping or rebooting laptops, and
operator mistakes. **Critical** means one in-scope party routinely moves or destroys other people's
money. **High** means a fork, a permanent halt, lost money or names, or a network-wide stall under
realistic conditions. **Medium** means the same harms under narrower conditions, or an outage that
recovers. **Low** means local or unlikely. **Info** means no harm today.

Baseline: `go build` and `go vet` are clean on both modules; the full suite (chaos scenarios
included) passed twice under `-race`. `TestPaymentGossipReceiptAndReplay` is flaky (about 1 run in
8 fails); the cause is a real liveness bug (E14), not test timing. Pass 3 also saw
`TestChaosDynamicAdmissionConverges` fail on unmodified code (2 of 5 and 3 of 12 runs, slack 4, on a
heavily loaded machine). `TODO.md` already tracks that as the starved-runner flake; it was not
root-caused here.

## The eMudhra questions, answered

**Could weak keys happen here? No.** Every key-generation path — `signing.Ed25519.GenerateKey`,
`cmd/keygen`, the wallet, `cmd/demo` — draws from `crypto/rand`. There is no seeded, derived,
time-based or `math/rand` path a deployment could fall into, and the ML-DSA slot is inert comments.
The wallet's at-rest sealing (PBKDF2-HMAC-SHA256, 600k iterations, AES-256-GCM, fresh salt and
nonce per seal) checks out against the standard library on 300 random vectors. The key-handling
gaps that do exist are about *loading* and *storing* keys, not generating them (E26, E27, E37).

**Does "two different signings means the key is burned" hold? Only for consensus votes, and even
there the key is not burned.**

| Signed statement | Conflict slot | Detected? | Durable evidence? | Key really burned? |
|---|---|---|---|---|
| Prevote / precommit | same height, round, type; different digest | yes | yes, carried in a block | **no** (E13) |
| Proposal | same height and round; different block | no | no | no (E16) |
| Payment | none (one signature drains an account) | — | — | no |
| Account update, parameter vote, reassignment vote | none enforced | no | no | no (E43) |
| Receipt | none | — | — | n/a |

- A slash deletes the offender's *account*, not its key. In static-validator mode the key keeps full
  voting and governance power; in dynamic mode stake delegated by others keeps it seated; a
  self-staked offender is re-admitted by one incoming payment (E13).
- A thief holding a bank's key clashes with the bank only when both sign consensus votes for the
  same round. Governance votes and payments have no conflict slot, so the thief can use the key there
  without ever clashing (E43, E44).
- A thief with a cracked user key never produces two conflicting signings: one payment drains the
  account. The rule can't catch that. The owner's recovery model (below) covers it, but it needs a
  freeze and a name-reassignment action that don't exist yet (E29).
- The opposite also happens: a dishonest proposer can apply one honest signature twice (E1).
- **Making the rule strict today would burn honest validators.** A validator that reboots while its
  height is open re-signs round 0 with no memory of its earlier votes. That is valid equivocation
  evidence against itself, and it can also fork the chain (E2). Persisting the node's own signatures
  must land first.
- Old keys are a standing risk: in dynamic-validator mode, a node joining or restarting trusts the
  genesis validators' keys forever, so retired or weak founder keys can forge its starting state (E3).

## Decisions taken during the review

These were agreed with the owner and shape the fix plan:

- **No users yet; breaking changes are allowed.** Batch state-digest and signing-format changes
  into one new genesis.
- **Prefer the smallest fix**; route rare recovery cases to the ⅔ governance vote.
- **Recovery model.** Validators are "banks". A theft victim identifies themselves at their bank;
  banks vote (⅔) to freeze the account, key and name; after the police decide, a ⅔ vote reassigns
  the name and, best effort, the money. A weak key is treated like a lost wallet: the identity
  (name) must be recoverable, the money is recovered when possible. No recovery keys.
- **Names.** A named account is never deleted by a balance event (zero balance, overdraw ban,
  slash): the balance goes, the name stays with its key. An account update may claim a name only if
  the account has none; renames, moves and sales go through a ⅔ name-reassignment vote.
- **Delegate to a name**, not a key, so a bank's seat follows its identity when its key is burned
  and the name is voted to a new key. A seat whose key is burned leaves the validator set (and the
  quorum total) until the name is reassigned.
- **Amounts** are accepted only as plain decimals (no exponent, bounded digits and decimal places).
- **Caps** on a single payment and on one key's balance, as ledger parameters (genesis + ⅔ vote).
- **Deployment direction** (later): an edge proxy facing the web, the engine talking only to the
  proxy, and a separate signer holding the key, possibly on another machine.

## Findings at a glance

"P1" and "P2" are the fix phases in the plan below.

| ID | Severity | Finding | Who can trigger it | Fix |
|---|---|---|---|---|
| E1 | critical | Block height never checked: a proposer re-applies old payments and updates | one validator | P1 guard, P2 height in state |
| E2 | high | A restarted validator forgets its votes: fork, halt or self-slashing | no one (a reboot) | P1 sign log |
| E3 | high | Dynamic mode anchors trust on genesis: roll-back, stuck nodes, forgeable history | no one; old founder keys | P1 |
| E4 | high | Governance vote pools fillable by anyone | anyone on the port | P1 |
| E5 | high | A claw-back is dodged by a same-block payment, which mints money | the reassignment target | P1 rule |
| E6 | high | Exponent-form amounts cost seconds and megabytes before any check | anyone on the port | P1 |
| E7 | high | Unbounded amount precision slows every node | any account holder | P1 rule, P2 format |
| E8 | high | No block size limit: free updates halt the chain | anyone on the port | P1 |
| E9 | high | Names are freed by balance events and renames | a thief, or a watcher | P1 rule |
| E10 | high | Wallet origin check beaten by DNS rebinding | any website | P1 wallet |
| E11 | high | Peer addresses published by only some validators partition the network | no one | P1 |
| E12 | high | Proposal flood | one validator | P1 |
| E13 | medium | Slashing deletes the account, not the key | — | P2 name freeze |
| E14 | medium | Proposals in the post-commit window are dropped (the flaky test) | no one | P1 |
| E15 | medium | A proposal's signature does not cover its block | a validator or LAN sniffer | P1 interim, P2 |
| E16 | medium | Two different proposals from one proposer go unnoticed | one validator | P1, P2 evidence |
| E17 | medium | Votes are stored and relayed before they are checked | anyone on the port | P1 |
| E18 | medium | A voter who leaves the set spoils a certificate | churn, or a validator | P1 |
| E19 | medium | A ⅔ vote can set timings that halt the chain for good | ⅔ (or one key holding quorum) | P1 rule |
| E20 | medium | A re-genesis that keeps the network id replays the old chain | operator mistake | P1 docs, P2 |
| E21 | medium | Far-future payments and updates sit in the pools forever | anyone on the port | P1 |
| E22 | medium | The state digest can't tell a missing amount from zero | one catch-up source | P1 |
| E23 | medium | Checkpoint fetches have no size limit | one catch-up source | P1 |
| E24 | medium | One over-balance payment destroys the payer's balance | one validator | P1 rule |
| E25 | medium | Catch-up skips the overdraw purge | no one (rare timing) | P1 |
| E26 | medium | Keys are loaded without checking the pair | operator mistake | P1 |
| E27 | medium | Setting the wallet passphrase doesn't seal an existing plaintext key | — | P1 wallet |
| E28 | medium | One bad height poisons the wallet's block counter for good | the reference node | P1 wallet |
| E29 | medium | No freeze, and no way to move a name to a new key | — | P2 |
| E30 | medium | Seizure votes are public before they count | the target | P1 |
| E31 | medium | The consensus driver spins forever if signing fails | a down signer | P1 with E2 |
| E32 | medium | A hung backup mount slowly kills the node | no one | P1 |
| E33 | low | A same-block credit lets a banned payer keep its balance | one validator | P1 rule |
| E34 | low | One key has many accepted base64 spellings | a validator | P1 rule |
| E35 | low | Late double-signs are never slashed | a validator | P1 after E2 |
| E36 | low | Admin votes resolve a typed name through the node, unseen | ⅔ on a dishonest node | P1 wallet |
| E37 | low | The demo leaves the founder key in plaintext in `/tmp` | local user | P1 |
| E38 | low | The fork-binding pin compares the wrong digest | no one | P2 |
| E39 | low | Genesis accepts duplicate keys, non-positive amounts, duplicate names | operator mistake | P1 |
| E40 | low | `/hdls/resolve` answers are unsigned; the docs' digest cross-check is weak | one node | P1 docs |
| E41 | low | Observer nodes get no peers on `maxPeers` networks | no one | P1 |
| E42 | low | Accounts created by a grant back no validator; the wallet can't delegate | — | P1 docs, wallet |
| E43 | info | Conflicting account updates and governance votes are not detected | — | — |
| E44 | info | Payments have no conflict slot for the burn rule | — | — |
| E45 | info | Validator power saturates at 2^40 units | — | docs |

## Critical and high findings

### E1 — A block's height is never checked, so a proposer can re-apply old payments (critical)

- **Where:** `onProposal` (`node/node.go:494`) checks the message's height but never the block's,
  and `commit` (`node/node.go:627`) applies the stored block as is. `ApplyBlock` checks each
  payment's and update's `BlockID` against `block.Height` (`ledger/block.go:186`, `:200`), and the
  state digest does not include the height.
- **What happens:** the scheduled proposer labels its block with an old height and fills it with
  payments signed for that height. Every honest node accepts the block, and the payments apply
  again. The same trick:
  - applies a pending, future-dated payment early and then again at its real height (wallet
    payments are always dated 3 blocks ahead);
  - replays an old account update (delegation, name, address);
  - carries evidence older than `maxEvidenceAge`;
  - pushes a payer over its balance so the overdraw ban wipes it.

  Governance certificates are not affected; their sequence number protects them.
- **Who:** one validator with any stake, at the heights where it proposes. Validators receive every
  payment by gossip, so the material is always at hand.
- **Verified:** the PoC fails 3 of 3 runs on today's code through the HTTP intake (alice 70 → 40, all
  honest nodes agree), and passes 3 of 3 with the guard below with no loss of liveness. The
  `findings.md` #7 note ("not currently exploitable", "the node guards this") is wrong.
- **Fix:**
  - Phase 1, about 6 lines: in `onProposal`, treat a proposal whose block height is not `n.height`
    as invalid (prevote nil) and don't store it; in `commit`, refuse such a block. Don't store the
    mislabelled block: its digest can equal an honest empty candidate's and overwrite it.
  - Phase 2: put `Height` into `LedgerState` and its digest, and have `ApplyBlock` require the
    previous height + 1, so the ledger enforces it for every caller.
- **Severity:** pass 3 rated this high. It is ranked critical here because one validator can
  re-apply, again and again, any payment ever made to it — theft from anyone who ever paid it.

### E2 — A restarted validator forgets its own votes: fork, halt or self-slashing (high)

- **Where:** a validator's lock, round and own votes live only in memory (`consensus.Driver`).
  After a restart, `New` reloads the last committed snapshot, and `startHeight`
  (`node/node.go:466`) starts a fresh driver at round 0: unlocked, with an empty mempool. Nothing is
  written to disk before a vote is sent. `TODO.md:158` calls the missing write-ahead log "not a
  safety issue". It is one.
- **What happens:**
  - *Fork.* Some validators precommit block D1 and lose power before their snapshot is written.
    Meanwhile a node outside that group (say, a laptop on battery) has already decided D1. After the
    reboot, the group reaches quorum on a different block, D2, before the decider is reachable. Both
    certificates verify. When the decider rejoins, catch-up silently moves it to D2 and reverses its
    finalized payments, and nobody is slashed. Reproduced 5 of 5 runs in each schedule tried,
    including a shared power outage with no partition and a set where no validator holds ⅓.
  - *Halt.* A restarted validator with half the power stalled the chain for good (2 of 2 runs).
  - *Self-slashing.* A restarted round-0 proposer re-proposes from its empty mempool and prevotes a
    different block, which is valid equivocation evidence against itself.
- **Who:** no attacker; just power loss or a reboot at the wrong moment. The window is milliseconds
  to tens of milliseconds per block on a LAN. `docs/security.md` advises putting stake on
  always-on machines, so a shared power cut is exactly the trigger. Its "no consensus fork under
  ≤ ⅓ Byzantine" (line 59) holds only while no validator restarts mid-height.
- **Fix:** Phase 1, about 140 lines. The prototype passes every suite under `-race`, with no fork in
  any schedule.
  - Keep a write-ahead sign log at `<snapshotPath>.votes`. Append and fsync each own vote before
    sending it, plus the proposal for a non-nil precommit. If the write fails, don't send.
  - On a restart at the same height, a new `Driver.Resume` restores the sent votes, the lock and the
    round, and the node re-gossips them.
  - Require `snapshotPath` for validators, and refuse to start if the log is more than one height
    ahead of the snapshot.
  - Land it together with E31.
- **What does not work (pass 3):**
  - A signer guard that only refuses to re-sign the same height, round and type does not stop the
    fork. It still forked in all four schedules tried, because the node votes unlocked in a later
    round.
  - Restoring the lock without the proposal body halts the chain for good after a total outage.

### E3 — Dynamic mode trusts the genesis keys forever: silent roll-back, stuck nodes, forgeable history (high)

- **Where:** `loadSnapshot` (`node/persist.go:31`) and `bestCheckpoint` (`node/sync.go:98`) accept
  a checkpoint only if more than ⅓ of **genesis** power signed its commit. Commits carry only the
  first quorum that arrived.
- **What happens** (dynamic validator mode only; static mode is unaffected):
  - *Roll-back.* Once the signers of the last commit hold ⅓ of genesis power or less, a node rejects
    its own snapshot and silently restarts from genesis. That is always the case after the founders
    pay out, and about half the time while a newcomer holds most of a quorum. After a simultaneous
    restart — the outage the docs promise snapshots recover from — the chain rolled back 7 blocks. A
    founder rebooting alone started a parallel chain that new joiners anchored on genesis preferred.
  - *Stuck nodes.* A running node that misses one commit window can never catch up, because every
    honest checkpoint fails the genesis test. On a real 5-process network, stragglers appeared
    within seconds of a founder payout with no fault injected. Once more than ⅓ of the power
    straggles, the chain halts.
  - *Forgery.* A joiner trusts genesis keys forever, so retired or stolen founder keys holding more
    than ⅓ of genesis power can forge the state it starts from.
- **Fix:** Phase 1, about 25 lines (prototypes in passes 2 and 3).
  - Load the node's own snapshot on self-consistency: its commit is a quorum of the set it carries
    and binds the digest. If the file exists but fails that check, refuse to start; never fall back
    to genesis silently.
  - Anchor `bestCheckpoint` on the node's last committed accounts, through an atomic pointer set in
    `New`, `commit` and `fastForward`.
  - Joiners seed `snapshotPath` from a trusted bank. That is the weak-subjectivity pin, and it needs
    no new config.
  - Don't add a `cp.Validators == deriveValidators(cp.Accounts)` check: it compares the pre-block
    set with post-block balances and rejects honest checkpoints.
- **Remaining limit:** a node that falls behind across a block that replaces more than ⅔ of the set
  still needs a checkpoint from a trusted source.

### E4 — Anyone on the port can freeze governance for good (high)

- **Where:** `addParameterVotes` (`node/governance.go:41`) and `addReassignmentVotes`
  (`node/reassignment.go:43`) create a pool entry before checking any vote. Each pool holds 64
  entries and drops one only when the sequence, the parameters or the target balance changes.
- **What happens:** 128 small unsigned POSTs per node fill both pools with entries that bind the current
  sequence and are never pruned. Every real parameter change and reassignment is then refused. A
  restart helps only until the next fill.
- **Who:** anyone who can reach a node's HTTP port. No key, no stake.
- **Fix:** Phase 1.
  - Pool only certificates that are already complete and valid against the current state: about 3
    lines per pool, trialled for reassignments with every suite green. The partial-tally code can
    then go.
  - Banks collect votes among themselves. The wallet gains "sign vote only" and "submit
    certificate" (about 40 lines). This also keeps a seizure hidden until it's ready (E30).
- **What does not work (pass 3):** the softer gate "pool once at least one vote verifies" still
  lets a single validator fill both pools with 64 self-signed proposals.

### E5 — A claw-back can be dodged by a same-block payment, and the dodge mints money (high)

- **Where:** `Reassignment.Valid` checks the target's balance before the block
  (`ledger/reassignment.go:90`). `ProposeBlock` and `ApplyBlock` (`ledger/block.go:151-156`,
  `:230-235`) apply payments first and then *set* the target's balance to the voted value.
- **What happens:** the target watches `GET /hdls/governance`, sees the reassignment reach quorum,
  and pays its balance away in the same block. The payment goes through, and the reassignment then
  overwrites the target's balance anyway. The money it paid out escapes, and whatever the vote set
  comes on top, so the supply grows. Pass 3: the target escaped 3 of 3 times on today's code and 0 of
  3 with the fix.
- **Who:** the target of a ⅔ reassignment (a thief, a custodian, or a credited victim who pays in
  that block). One signed payment is enough.
- **Fix:** Phase 1 rule, about 15 lines. Once a reassignment is ratified, the proposer drops every
  payment from or to the target in that block, and `ApplyBlock` rejects a block that carries both.
  Governance wins, and the dropped payments lapse. This is a prerequisite for the freeze and
  name-reassignment actions.

### E6 — Exponent-form amounts cost seconds and megabytes before any check (high)

- **Where:** amounts are `*big.Rat` values decoded by `encoding/json`, which accepts exponent
  notation. Decoding, and the `RatString` used for sizes, keys and signatures, run before any size,
  signature or membership check: in the HTTP handlers (`node/httptransport.go:130-240`) and in
  checkpoint fetches.
- **What happens:** Go 1.26 caps the exponent, but the cap is high.
  - `"1p10000000"` (11 bytes) decodes to a 10-million-bit number, about 1.8 MB of heap, and
    `RatString` on it takes about 2 s.
  - One unauthenticated 521-byte governance vote stalled a node's single event loop for 8.5 s, and
    the pooled entry kept the block rate down from 64 to 2 per 2 s.
  - A 268-byte payment stalled the loop for 1.8 s, and it can be replayed.
  - A 1 MiB message holds tens of thousands of such fields, enough to exhaust memory (extrapolated,
    not run).

  Pass 2 corrected pass 1's "unbounded magnitude": the size of each field is capped, but the cap is
  what makes it hurt.
- **Who:** anyone who can reach the port.
- **Fix:** Phase 1: give amounts a type whose decoder rejects exponent forms and overlong strings
  *before* `big.Rat` allocates. Use it for every amount: payments, accounts (including inside
  checkpoints), parameters and reassignments. Add the message-size bound to governance intake, which
  has none. Phase 2 narrows the same decoder to plain decimals (E7). A bit-length check after
  decoding is too late.

### E7 — Amounts have unbounded precision, which slows every node (high)

- **Where:** amounts are exact fractions with no smallest unit, and `MaxMessageBytes` bounds a
  message, not a balance.
- **What happens:**
  - Paying an account amounts whose denominators are large and share no factors makes its balance's
    denominator grow with every payment. After 1,000 such payments (100 units of fees), each balance
    was 1.56 MB. The state digest then took 0.93 s, and one ordinary payment to that account took
    14.1 s, on every node, several times per block.
  - A cheaper variant never pays anything. 140 far-future payments of 1/d each, below the fee, sit
    in every mempool and are re-summed after every commit. The block rate fell from 512 to 0.5 per
    second, and the payer was never debited.
- **Who:** any account holder; free if the fee is 0.
- **Fix:** amounts on a fixed grid of 10⁻⁸, as the agreed plain decimals.
  - Phase 1, as a validity rule: check at intake, in `ProposeBlock` and `ApplyBlock`, and for
    parameters, reassignments and genesis. About 10 lines. `RatString` of an on-grid value is
    unchanged, so no signature changes.
  - Phase 2: plain decimals as the JSON form.
  - Pair it with E21's `BlockID` window, which is what evicts the far-future payments.

### E8 — No block size limit: free account updates halt the chain (high)

- **Where:** `ProposeBlock` (`ledger/block.go:95`) carries every valid payment and update for the
  height, with no size or count limit. Every peer decodes proposals under a 1 MiB limit
  (`node/httptransport.go:308`).
- **What happens:** about 545 account updates of 1.9 KB each push every honest proposal for a height
  past 1 MiB. They are free and need no account (E21). Peers reject the proposal, no proposal for
  that height can succeed, and the chain halts.
- **Who:** anyone who can reach `/hdls/update`, the wallet-facing route the edge proxy must forward.
- **Fix:** Phase 1 proposer policy, about 20 lines, no rule change. Stop adding messages once the
  block's content reaches 768 KiB, payments first. Allow at most one update per account per block,
  and apply E21's account and window rules. Keep the budget a constant: a governed one would change
  the parameter digest.

### E9 — Names are freed by balance events and renames, so a thief or a watcher can take them (high)

- **Where:** every way of removing an account drops its name: zero balance (`ledger/ledger.go:209`),
  the overdraw ban, and `applySlashes` (`ledger/block.go:72`). A rename releases the old name
  explicitly (`ledger/account_update.go:96`). Updates apply after payments, so a claim in the same
  block succeeds.
- **What happens:**
  - A thief with a stolen key drains the account to exactly zero and, in the same block, claims the
    victim's name from its own account. Every later "pay alice" then goes to the thief.
  - Without moving any money, a stolen key can rename the account, which frees the name for a claim.
  - With no theft at all, anyone can pre-stage claims and catch a name the moment its owner empties
    the account. The user manual recommends exactly that when moving to a new wallet
    (`ui/USER_MANUAL.md:41-42`).
- **Fix:** the agreed naming change, Phase 1 rule, about 15 lines in total, prototyped.
  - A named account is never deleted by a balance event: the balance goes, the name stays.
  - An update may set a name only if the account has none. The release branch is deleted.
  - Renames then wait for the ⅔ name-reassignment vote (Phase 2).
  - Land it with E22. It also fixes E33.

### E10 — Wallet: DNS rebinding gets past the origin check (high)

- **Where:** `sameOrigin` (`ui/main.go:76`) requires `Origin` to match `Host`, but never checks
  `Host` against the address the wallet listens on.
- **What happens:** a web page the user visits rebinds its own hostname to 127.0.0.1. Its requests
  then carry matching `Origin` and `Host` headers and pass. The wallet signs with the local key, so
  the page can send payments and governance votes.
- **Who:** any website the user visits while the wallet is running.
- **Fix:** Phase 1, wallet only. Accept only a `Host` equal to the configured listen address (and
  `localhost` on the same port), and deny framing.

### E11 — Peer addresses published by only some validators partition the network for good (high)

- **Where:** `node/peerdns.go:11-45`. A validator with an on-chain address resolves its peers among
  the validators that published one, and sends only to them. A validator without one keeps its
  configured peers and sends to everyone.
- **What happens:** publishers hear everyone, but non-publishers hear only each other. Once two or
  more publishers hold between ⅓ and ⅔ of the power, no block can commit. There is no way out without
  a code change or a re-genesis:
  - an address can't be withdrawn;
  - a restart re-applies the split;
  - publishing the missing addresses needs a block.

  The same split happens when a validator that has an address loses its account, by paying out its
  whole balance, a ban or a slash. Two of four such validators halted a real network.
- **Who:** no attacker. A bank-by-bank rollout of the documented optional address passes through
  this state, and a dishonest validator can complete it as the second publisher.
- **Verified:** on real `cmd/server` processes the network froze at height 6, against 82 for the
  control. That held for all-to-all, `maxPeers=2`, partial-genesis and three-node setups.
- **Fix:** Phase 1, about 15 lines. Resolve peers only when every current validator has an address,
  and otherwise restore the configured peers (saved at the first resolution, not in `New`).
  Document that resolution starts once every validator has published.

### E12 — A proposer can flood every node with proposals (high)

- **Where:** `onProposal` (`node/node.go:494`) has no round bound and drops only identical copies.
  Votes do have a round bound (`maxRoundLookahead`, `consensus/driver.go:216`).
- **What happens:** a validator can send many different proposals for its own round, and proposals
  for far-future rounds where it is also the proposer. Every honest node fully applies, stores,
  remembers and relays each one. Each 1 MiB proposal costs about 0.3 s of event-loop time and
  1.3 MiB of memory per node, plus a vote set per round that slows every later vote. While the loop
  churns, the governance vote that would remove the attacker can't commit.
- **Who:** one validator key (a bank, or a stolen bank key).
- **Fix:** Phase 1, about 8 lines (prototype green under `-race`). In `onProposal`, before applying
  anything, keep only the first authentic proposal per round, whatever its digest, and only for
  rounds from 0 to the driver's round + 1. This also stops the relay half of E16, and E8's budget
  bounds the one proposal allowed.

## Medium findings

**E13 — Slashing deletes the account, not the key.** `applySlashes` (`ledger/block.go:72`)
deletes the offender's account, and nothing records the key.
- In static mode (`validatorsFor`, `node/validators.go:14`) the key stays in the set with full
  power. After its slash, it still cast the deciding ⅔ vote on a 1,000-unit reassignment.
- In dynamic mode, stake delegated by others keeps it seated, and a self-staked offender is
  re-admitted by one incoming payment, after which nobody re-slashes it.

The harm is accountability, governance weight and a permanently used-up fault budget, not a fork.
`README.md:77`, `docs/consensus.md:79` and `docs/managing-the-currency.md:40` overstate what a slash
does. *Fix (Phase 2, strictly after E2):* a double-sign freezes the validator's name, which leaves
the validator set in both modes until the ⅔ re-keys it (Phase 2 plan). That replaced the committed
burn set of canonical keys first proposed (`results/burnset-prototype.patch`). On that prototype
without E2's fix, an ordinary reboot burned an honest validator.

**E14 — Proposals in the post-commit window are dropped (the flaky test).** After `commit`
increments the height, the old driver stays until `startHeight` runs one `MinBlockInterval` later.
A proposal for the new height that arrives in that window is checked against the old height's
proposer and dropped, so the round is lost. That happens in normal operation whenever nodes commit
at slightly different times. Consensus timers are not tied to a height either, so a stale timer can
fire into the next height's driver. This is why `TestPaymentGossipReceiptAndReplay` fails about 1
run in 8. *Fix (Phase 1, about 45 lines; prototype 40 of 40 clean):* in the window, authenticate and
buffer the first proposal per round (round ≤ 1) and replay it into the new driver; ignore timer
events from another height.

**E15 — A proposal's signature does not cover its block.** `proposalSignFields`
(`node/messages.go:45`) signs height, round, valid round and state digest, but not the block. Anyone
who sees a genuine proposal (a validator, or a LAN sniffer, since gossip is plain HTTP) can attach a
junk block to the real signature and get it to a node first. That node stores and relays the junk,
drops the genuine copy as a duplicate, and can't commit the height until checkpoint catch-up.
*Fix:* Phase 1 interim: act only on copies that `ApplyBlock` accepts, and never overwrite a stored
valid block. Phase 2: add a block hash to the signed fields.

**E16 — Two different proposals from one proposer go unnoticed.** `onProposal` drops only an
identical copy. A second, different proposal for the same height and round is applied, stored and
relayed, and nothing records the conflict, because `Evidence()` scans only votes. The direct harm
is small (the attacker wastes its own round), but it is exactly the "two signings" the burn rule is
about. *Fix:* Phase 1: first proposal per round wins (shared with E12), and keep the conflicting
pair for operators. Phase 2: proposal evidence carried in blocks, feeding the double-sign freeze
(after E2).

**E17 — Votes are stored and relayed before they are checked.** `handleVote`
(`node/httptransport.go:139`) passes votes on unverified. The event loop stores every vote for the
current height in `heightVotes`, which has no cap, and relays it whether or not the driver accepted
it. Anyone on the port can make every node store and re-gossip junk. *Fix (Phase 1):* store and
relay only votes the driver would count (valid signature, current validator, current height, round
within the bound); cap `heightVotes`.

**E18 — A voter who leaves the set spoils a certificate (dynamic mode).** `Valid`
(`ledger/governance.go:79`, `ledger/reassignment.go:97`) rejects a whole certificate if any vote
comes from a non-member, and the pools never drop a vote. Ordinary churn (re-delegation, a balance
dipping under the threshold, a slash), or a deliberate griefer, then blocks a change that a clear
live quorum supports. *Fix (Phase 1, about 12 lines):* when picking a certificate, keep only votes
from current validators and check that copy.

**E19 — A ⅔ vote can set timings that halt the chain for good.** `TimingConsistent` and
`Acceptable` (`ledger/parameters.go:76-97`) enforce only lower bounds. A ratified block interval of
86,400,000,000 ms stops the node. Each restart produces one block and re-arms the timer, and the
vote pools live in memory, so the revert never enacts. One operator can do this wherever one key
holds quorum. *Fix (Phase 1 rule):* a ceiling between 1 and 7 days; the documented pigeon mode needs
at least a day.

**E20 — A re-genesis that keeps the network id replays the old chain.** Signatures bind only the
network-id string. Payments are protected against replay only by `BlockID`, and governance only by
its sequence, and both restart at a re-genesis. On a same-id re-genesis, old payments and ⅔ grants
are accepted again (bob was paid twice). Worse, if one node keeps its old snapshot (a forgotten
file, a laptop asleep during the migration, or a dishonest validator), catch-up pulls every node
back onto the old chain within seconds, and the edit is lost (3 of 3 runs). *Fix:* Phase 1, docs:
every re-genesis takes a new `networkId`, deletes `snapshotPath` and restores no old backups
(`docs/operating-a-network.md`). Phase 2: derive the effective network id from the genesis contents.

**E21 — Far-future payments and updates sit in the pools forever.** `admitPayment`
(`node/node.go:432`) and update intake (`node/node.go:283`) put no upper bound on `BlockID`, and the
pools evict only past heights.
- Payments below the fee are admitted, receipted and carried for free.
- Updates need no account at all, so a fresh key can fill the 10,000-slot update pool on every node.
  From then on every honest name registration, re-delegation and peer address is answered 202 and
  silently dropped: one registration landed on 0 of 4 nodes.

*Fix (Phase 1):*
- accept a payment or update only for `BlockID` within [height, height + 64];
- refuse payments below the fee;
- accept updates only from existing accounts;
- evict entries whose window has passed or whose account is gone.

**E22 — The state digest can't tell a missing amount from zero.** `StateDigest`
(`ledger/digest.go:15`) hashes a nil amount as "0". A relaying peer can null out a zero balance in a
genuine checkpoint, and it still verifies. The node then writes it to its snapshot and panics on the
next payment to that account, and again after every restart. Zero balances exist today after a
claw-back to 0, and the naming decision (named accounts kept at zero) will make them common. *Fix
(Phase 1, one line, no committed digest changes):* hash a nil amount as "". It must land with or
before E9.

**E23 — Checkpoint fetches have no size limit.** `fetchCheckpoint` (`node/sync.go:108`) decodes
the whole response with no limit and no status check. Catch-up polls every few seconds even when the
node is not behind. One dishonest catch-up source (a bootstrap peer, or any validator in an
all-to-all setup) can exhaust memory. With E6 amounts, a 4 KB reply cost a running node about
200 MB, and a restarted node re-polls within seconds. *Fix (Phase 1):* reject non-200 responses and
read through a `LimitReader` (about 64 MiB). E6's decoder is what closes the amplification.

**E24 — One over-balance payment destroys the payer's whole balance.** The overdraw ban is meant
for two conflicting payments that together overdraw. But `ApplyBlock` has no per-payment check, and
the wallet signs any amount. So a single over-balance payment carried by a dishonest proposer bans
the payer, whether it was a typo or a "pay everything" while another payment is pending. Honest
intake refuses such payments, so the trigger is one dishonest validator that sees the payment (the
payer's reference node, or whoever runs the planned edge proxy). *Fix (Phase 1 rule):*
`ProposeBlock` skips, and `ApplyBlock` rejects, any payment larger than the payer's balance before
the block. The ban then fires only for two or more payments, which is the owner's "two signings"
case.

**E25 — Catch-up skips the overdraw purge.** `fastForward` (`node/node.go:341`) replaces balances
but never runs the pool clean-up that `commit` runs (`dropOverdrawn` and the expiry passes). A node
that catches up can then propose payments that together overdraw, and the ban wipes the payer. This
needs an overspend split across two nodes during a partition, plus the right proposer, so it is
unlikely, but no attacker is required. *Fix (Phase 1, about 3 lines):* run `commit`'s pool clean-up
at the end of `fastForward`.

**E26 — Keys are loaded without checking the pair.**
- `load` (`cmd/server/config.go:57`) checks key lengths, but not that the private key matches the
  configured public key. A node assembled from two keygen runs signs under an identity nobody
  expects and silently drops out of consensus.
- The wallet trusts the stored public key over the private key, and its sealed file doesn't
  authenticate its version, KDF parameters or public key.

*Fix (Phase 1):* derive the public key from the private key and compare, with a sign-and-verify
self-test at start. In the wallet, derive the identity from the decrypted key.

**E27 — Setting the wallet passphrase does not seal an existing plaintext key.** The documented
remedy for a plaintext wallet is to set `GOLEDGER_WALLET_PASSPHRASE`. Doing so skips the upgrade
(`ui/keyfile.go:90`), leaves the key in plaintext and stops the warning. *Fix (Phase 1):* re-seal in
place, atomically, whenever a passphrase is available. At minimum, warn on every start while the
file is plaintext.

**E28 — One bad height poisons the wallet's block counter for good.** The wallet's next `BlockID`
is `max(height + 3, last + 1)`, persisted to `<wallet>.state` with no sanity bound and no network
scope. One huge height from the reference node, dishonest or buggy, latches every later payment to a
far-future block. Nodes accept and receipt those payments, but they never land, even after switching
to an honest node or a new chain. *Fix (Phase 1):* clamp the reported height and the counter, and
scope `.state` to the network. E21's window makes such payments fail loudly.

**E29 — No freeze, and no way to move a name to a new key.** An account's identity is its public
key. A name can only be claimed while it is free, and the only balance-moving governance action sets
a balance; nothing freezes a key. So the agreed recovery model (banks freeze, the police decide, a
vote reassigns) has only its last step, and a thief keeps spending while votes are collected.
*Fix (Phase 2):* a ⅔ freeze action and a ⅔ name-reassignment action, both under E5's exclusion
rule. The freeze must not be bound to the balance, and it must be re-checked at intake and in
`ProcessPayments`.

**E30 — Seizure votes are public before they count.** The admin page sends one vote per POST.
The node pools and relays it, and the unauthenticated `GET /hdls/governance` shows the target, the
amounts and the tally. A target who is watching moves its money first, and a certificate bound to
the old balance can then never enact. *Fix:* E4's "complete certificates only" closes this (sign
off-node, submit once), together with E5. *Remaining limit:* a target expecting a claw-back can
change its balance every block and defeat any certificate collected at human pace. So the freeze
action (E29) must not be bound to the balance.

**E31 — The consensus driver spins forever if signing fails.** When signing fails, `castVote`
(`consensus/driver.go:253`) produces no vote, but `step1` still reports progress, so `evaluate`
loops forever on the node's only event-loop goroutine. That is unreachable with today's in-memory
key. It becomes reachable with a separate signer that is down, or with E2's fail-closed sign log.
The existing test covers only the timeout path, which is why it passes. *Fix (Phase 1, 3 lines plus
a retry on the next timer):* report progress only when the vote was actually sent. Land it with E2.

**E32 — A hung backup mount slowly kills the node.** `saveSnapshot` starts a new
`go writeAtomic` for every commit (`node/persist.go:65`), with no limit. On a mount that hangs
instead of failing, such as a stale hard NFS mount, each goroutine pins an OS thread. At one per
block, Go aborts the process at 10,000 threads. *Fix (Phase 1, 4 lines):* allow at most one mirror
in flight and skip while it is busy. A write deadline is better.

## Low and informational findings

| ID | Finding | Fix |
|---|---|---|
| E33 | A banned payer that also receives a payment in the same block keeps its whole balance and name, contrary to the documented ban. | Shares E9's three lines in `ProcessPayments`. |
| E34 | One Ed25519 key has several accepted base64 spellings, so in dynamic mode one key can hold two validator seats and vote twice without evidence. It gains nothing over two real keys, but it matters for double-sign evidence. `Strict()` alone misses the newline spellings. | Accept only canonical spellings in `signing.VerifyBase64` (`signing/signer.go:60`, one line) and in key fields that create identities. |
| E35 | Late double-signs are never slashed: a driver's evidence is dropped when its height ends by catch-up, and conflicting votes that arrive after a decision are ignored (38 of 38 in a test). | After E2 only: harvest evidence at `startHeight` and check late precommits against the last commit. |
| E36 | *(plausible)* Admin reassignment votes resolve a typed name to a key through the node, and sign without showing the key. Abuse needs ⅔ to use a dishonest node, which is not the documented setup. | Show and confirm the resolved key, name and balance before signing. |
| E37 | The demo writes the founder key in plaintext to a fixed `/tmp` path and never deletes it. The symlink attack is blocked on this kernel. | Private temp dir, `O_EXCL`, delete on exit. |
| E38 | The `PrevStateDigest` pin compares an accounts-only digest, while the docs and `/hdls/resolve` give the full state digest. A pin computed as documented never lands, yet the payer gets a receipt. | Compare against the full digest, and check it at intake. |
| E39 | Genesis accepts duplicate keys, zero or negative amounts and duplicate names; a duplicated key silently loses balance. | Validate in `load`. |
| E40 | `/hdls/resolve` returns an unsigned answer. Comparing digests across nodes, as the docs advise, doesn't catch a node that lies about the address. Its height is off by one. | Docs: compare the key and address fields. Fix the height; optionally serve the commit certificate. |
| E41 | On `maxPeers` networks, a node whose key is not a validator (an observer or merchant node) gets zero peers. It accepts payments and signs receipts, but never relays them. | Keep the configured peers when the node is not a validator (`cmd/server/config.go:211`, about 2 lines). |
| E42 | Accounts created by a grant back no validator in dynamic mode, and the wallet has no field to delegate. | Docs, plus a delegate field in the wallet's update form. Don't auto-delegate: a large grant would hand one key quorum. Keep the treasury undelegated. |
| E43 | *(info)* Conflicting account updates and governance votes from one key are not detected. Harmless today. | Optional record for operators. Drop or document the unused `validator-message` and `server-auth` signing domains. |
| E44 | *(info)* Payments have no conflict slot, so the burn rule can never fire on a stolen payment key. | Covered by the freeze and name actions (E29). |
| E45 | *(info, plausible)* Validator power is capped at 2^40, so validators above it count as equal. Unreachable at community scale. | Document it in `docs/security.md`. |

**Refuted:** the claim that float `math.Pow`/`Ceil` in `GroupParameters` could split nodes on
different architectures. 1.3 million (participants, `maxPeers`) cases on amd64 and 386 gave
identical results.

## Fix plan

Three phases.
- **Phase 1** needs no new genesis: snapshots, wallet files and signatures stay valid. Items marked
  **rule** change what makes a block valid, so every validator must switch to them together; the
  rest can roll out node by node.
- **Phase 2** changes the state digest, signed formats or the JSON form, and adds the agreed
  features. It starts from one new genesis.
- **Phase 3** is the deployment split.

### Phase 1 — fixes to today's code

In this order:

1. **Stop the replay (E1).** Height guard in `onProposal` and `commit`. About 6 lines.
2. **Sign log (E2, E31).** Write-ahead, fsynced and fail-closed, with `Driver.Resume`; the driver
   reports progress only for votes it actually sent. About 145 lines. Everything that punishes
   double-signing waits for this step.
3. **Trust anchors (E3).** Load the node's own snapshot on self-consistency and fail loudly; anchor
   catch-up on the last committed accounts. About 25 lines.
4. **Proposal intake (E12, E14, E15 interim, E16).**
   - First authentic proposal per round wins.
   - Rounds only up to the driver's round + 1.
   - Act only on copies `ApplyBlock` accepts.
   - Buffer the next height's proposal during the post-commit window.
   - Timers scoped to their height.

   About 60 lines. This also fixes the flaky test.
5. **Message intake (E6, E17, E21, E8, E23).**
   - An amount decoder that rejects exponent forms and overlong values.
   - Votes stored and relayed only when valid, with a capped `heightVotes`.
   - A `BlockID` window of 64 for payments and updates.
   - Updates only from existing accounts, one per account per block.
   - The proposer stops at 768 KiB of content.
   - Checkpoint fetches check the status and are limited to 64 MiB.
6. **Governance pools (E4, E30, E18).** Pool only complete, currently valid certificates, and drop
   departed voters when picking one. The wallet gets "sign vote" and "submit certificate". Ship the
   node and wallet halves together.
7. **Rules, all validators together.** About 60 lines in `ledger/` and `signing/`:
   - E9 with E22 and E33: names are never deleted by balance events and are claimed only once, and a
     nil amount hashes differently from zero;
   - E5: the claw-back exclusion;
   - E24: a lone over-balance payment is rejected;
   - E7: the 10⁻⁸ amount grid;
   - E19: the timing ceiling;
   - E34: canonical key spellings.
8. **After step 2 only: evidence and catch-up (E35, E25).** Harvest evidence at `startHeight`, and
   run the pool clean-up after catch-up.
9. **Operations (E11, E41, E32).** All-or-nothing peer DNS that restores the configured peers;
   observers keep their peers; one backup write at a time.
10. **Wallet and config (E10, E26, E27, E28, E36, E37, E39, E42).**
    - Wallet: `Host` allowlist; identity derived from the private key; plaintext keys re-sealed; the
      block counter clamped and scoped to the network; resolved keys confirmed before voting; a
      delegate field.
    - Config and demo: a key-pair check and self-test at start; genesis validation; the demo key in a
      private temp dir.
11. **Docs** (list below).

### Phase 2 — one new genesis

- **Height in `LedgerState` and its digest** (E1): `ApplyBlock` requires the previous height + 1.
  About 57 test sites build states or blocks and need updating.
- **Plain-decimal amounts** in JSON, on the 10⁻⁸ grid, with bounded digits (the final form of E6
  and E7).
- **Caps as ledger parameters:** the largest single payment and the largest balance per key
  (agreed).
- **Network id bound to genesis** (E20).
- **Proposals:** the signature covers the block (E15), and proposal evidence is carried in blocks
  (E16).
- **⅔ freeze and name action** (E29), under E5's exclusion rule. The name action is a Reassignment
  with two optional fields, `NewKey` (with the new key's signature) and `Bank`. It binds only the name
  and the governance sequence, so a key change in the same block cannot make it stale. It is the
  only way to unfreeze a name (always with a new key, see below), re-key a seated or frozen name, or
  change a seated name's bank. Holders' own registrations, key and bank changes and freezes are the
  requests described below; a sale is a key change to the buyer's key.
- **Delegate to a name** (agreed), so a seat follows its bank.
- **A double-sign freezes the name** (E13; decided after the review, replacing a burn set of keys;
  an adversarial check, `_evidence_2026-10-27/results/name_freeze_check.json`, shaped the rules).
  Valid evidence, from votes or proposals, freezes the validator's name. The name then leaves the
  validator set in both modes, with the power delegated to it, until a ⅔ name action re-keys it. No
  list of burned keys is kept. Keys are fresh and free to generate, so listing an old one would stop
  nothing, and an ML-DSA key is about 2 KB.
  - Evidence is judged from the pre-block state and its two signed messages alone. It is valid when
    both halves sit in the same slot (height, round and type for votes; height and round for
    proposals), name the same signer, sign different blocks, and verify under that name's current
    key, and the name is not already frozen. It is not checked against a past validator set: the
    record of past sets differs from node to node, so nodes would disagree on which blocks are
    valid. Instead, a node signs votes and proposals only at heights where its own name is in the
    set.
  - So evidence counts only while the key it convicts is still that name's current key, with no
    other age limit (decided). Once the ⅔ has re-keyed the name, older evidence is moot, and
    resubmitting it cannot freeze the name again. This rule replaces `maxEvidenceAge`.
  - Nodes keep a pending proof until a block carries it, the name is frozen, or the convicted key is
    no longer the name's current key, whether or not the name is still in the set. Today's code
    drops a proof once its validator leaves the set (`node/node.go:519-523`, `:889-894`), which would
    let a double-signer escape by moving its stake away in the same block.
  - Every unfreeze re-keys. The ⅔ name action lifts a freeze only when its signed fields say so and
    its new key differs from the frozen one; otherwise the same proof would freeze the name again,
    and a rotation certificate signed before the double-sign could lift a freeze nobody voted on.
  - A proposal is in the sign log before it is sent, and a node never signs a different block for a
    round it has logged. Phase 1 now does both ("E2: log a proposal before sending it (follow-up)"),
    after a probe confirmed that the earlier code could propose two blocks for one round after a
    failed log write and a restart. Proposal evidence is two proposals for one height and round
    whose signed block hashes differ (after E15).
  - **One-third cap** (decided). A block's evidence freezes a name only if, afterwards, all frozen
    names together hold under a third of the power of seated and frozen names. Power is a name's
    static power, or the stake delegated to it, and a frozen name takes no new delegations. Evidence
    over the cap stays valid while its key is current and waits, or the ⅔ re-keys the name. Without
    the cap, one honest fault in a bank holding more than ⅔ (the setup the docs recommend) would hand
    the chain to the rest, and a bug shared by every node could freeze them all. A dishonest third
    can veto its own evidence anyway, so the cap gives up no deterrence. `docs/security.md` states
    the scope: the freeze handles double-signing by under a third of the power; at a third or more,
    recovery is a re-genesis with new keys and a new network id (E20).
  - This needs Phase 1 step 2 (the sign log) first, so an honest restart never freezes a bank.
- **Fork-binding pin** compares the full state digest (E38).
- **Signature record** (agreed after the review). For `RetainBlocks` heights, every node keeps each
  block's commit certificate, its proposer's signature (over the block, after E15) and the
  validator set whenever it changes. Parameter-change and reassignment certificates are kept for
  the life of the chain, and so is every double-sign proof a block carried (decided: about 7 KB each
  with ML-DSA-65, and rare), so the ⅔ and later audits can see why a name was frozen. Payments and
  account updates are never kept. Nodes serve the record, so one honest node that kept it is enough
  to expose a forged certificate.
  - `RetainBlocks` is a ledger parameter that bounds only how long the record is kept. Whether a
    double-sign is punishable does not depend on it (see the double-sign freeze).
  - A vote can lengthen it, but a cut applies only to records written after the vote, so the ⅔
    cannot vote away the record of their own votes or seizures. It never goes below 1000.
  - The record must not leak payments. `StateDigest` is a plain hash of public account fields, and
    `/hdls/checkpoint` serves today's balances to anyone. A record of every height's signed digest
    would let anyone walk a quiet ledger backwards one guessed payment at a time, about 10⁸ guesses
    for a single-payment block on 200 accounts.
  - So the signed block digest mixes in a random value the proposer draws, which nodes drop along
    with the payments. The record keeps that digest and the link to the previous block, never the
    bare state digest.
  - Size: about 0.8 KB per block with 4 validators and 3.5 KB with 21, which is about 2 GB and
    9 GB a month at one block per second.
  - Tracing a theft stays outside the protocol, in the opt-in archive node (`TODO.md`). The record
    makes that archive checkable: every block it holds must match the kept signatures.
  - The "historyless" wording changes with it (`docs/managing-the-currency.md:316` and `:353`,
    `docs/operating-a-network.md:138`).
- **Signature scheme: ML-DSA-65** (decided after the review). Go 1.27 ships `crypto/mldsa`, so the
  post-quantum slot in `signing/mldsa.go` needs no dependency, and a genesis is the only time the
  scheme can change. ML-DSA-65 keys are 1952 bytes and its signatures 3309 bytes, against 32 and 64
  for Ed25519. Settle the sizes first, because they drive the rest of the phase. The same genesis
  must:
  - sign with `SignDeterministic`. The consensus driver assumes a signature is a function of what is
    signed (`consensus/driver.go:25`), and Go's `Sign` adds fresh randomness;
  - re-size what was sized for Ed25519. With names as the identity (below), a payment or a vote is
    about 4.6 KB, nearly all of it signature. `MaxMessageBytes` (2000, minimum 512) needs new
    values, the 768 KiB block budget then holds about 170 payments, and a checkpoint carries each
    account's key, about 2.6 KB, once;
  - stop an idle chain from committing a block every second. Otherwise the signature record (about
    50 GB a month with 4 validators and 250 GB with 21, at one block per second) and vote gossip
    become the main cost;
  - replace the Ed25519 assumptions spread through the code: the wallet's `signing.Ed25519{}`
    literals, the 32-byte test in `looksLikePublicKey`, and the key-size checks in
    `cmd/server/config.go`.
- **Names are the account identity** (decided after the review). An account is its name, and its key
  is the name's current credential, kept in state. Payments, delegations and validator seats name
  accounts by name. A signed message names its signer, and nodes look the key up, so only the
  requests below carry a key, the new one. Paying to a raw key goes away, so a key without a name
  can neither pay nor be paid.
  - Names that hash the key are a wallet convention for anonymous wallets, and the prepaid,
    cash-like path uses them. Someone who sees a registration on the network can race it through
    their own bank, at the cost of a fee and a bank co-signature that trace back to that bank; the ⅔
    name action undoes it.
  - The node's trust anchor and the wallet's pin (`<keyfile>.trust`) store name, key and power
    together. Otherwise a forged checkpoint could give real bank names keys an attacker holds.
  - `AccountUpdate` loses its name claim. Delegation stays a key-signed update and has no say in
    who co-signs.
  - Still to settle: one spelling per name. E34's lesson applies to names too: fix a character set
    and a canonical form (for example lowercase ASCII letters, digits and hyphens), so no name can
    imitate another.
  - `docs/managing-the-currency.md:274-287` (the prepaid key, and "every account forced to carry a
    registered identity" as a surveillance smell) is rewritten for banks of record and hash names.
- **Registering a name, changing its key or bank, and freezing it** (agreed after the review, shaped
  by a five-agent design review: `_evidence_2026-10-27/results/new_key_design_review.json`). Each
  account has a bank of record, by name, under the state digest. Every request names its target block
  (`BlockID`, within the intake window), and each signature has its own domain and covers all of the
  request's fields.
  - **Registration of a free name.** "Free" means never registered; a burn or a freeze never frees a
    name. The requestor, any named account, signs and pays the fee like a payment, which records who
    funded the identity. The requestor's bank co-signs, and the new key signs to prove its holder
    has it. The co-signing bank becomes the name's bank of record. It applies at once.
  - **Key change.** The name's current key signs, its bank of record co-signs and the new key signs.
    The fee is burned from the name. It takes effect `ChangeDelayBlocks` later.
  - **Bank change.** The name's current key signs and the new bank co-signs. The old bank is not
    asked, so it cannot hold its customer hostage. It costs the same fee and waits the same delay.
  - **Freeze.** It is signed by the name's current key alone, with no fee, so a drained victim can
    still send it, and it applies at once. It cancels any pending change. A frozen name neither pays
    nor is paid, and changes neither key, bank nor delegation, until the ⅔ name action re-keys it.
    Only the ⅔ can unfreeze, so a thief holding the same key cannot undo it.
  - **Why it holds.** A thief with the key alone needs the bank of record's co-signature to change
    the key, and every change waits, during which the still-valid key can freeze the name. Moving
    the name to a careless bank first costs a second wait. Pending changes are in the signed state,
    so banks can email their customers about them, and the wallet warns when its verified state
    shows one on its own name. Someone who misses both windows goes to the ⅔, as before.
  - **Rules.**
    - `NewKeyFee` and `ChangeDelayBlocks` are ledger parameters, in the digest and the vote
      preimage. The fee is a signed field and must equal the pre-block `NewKeyFee`. The delay is
      about a day: 86,400 blocks at one per second.
    - A name in the validator set changes key or bank only by the ⅔ name action. Otherwise a stolen
      bank key could move a seat, or a double-signer could change key before its evidence lands. The
      check runs again when a pending change falls due: it lapses if its name is in the set then,
      or if its new key has meanwhile become some name's key. The ⅔ action's new key must not be
      any other name's key either.
    - Inside a block everything is judged against the pre-block state; then freezes apply (from
      evidence, holders or the ⅔), then the ⅔ name action, then the changes that fall due.
    - A request is invalid if the name or a bank in it is frozen, the new key is already some name's
      key, the bank differs from the bank of record (key change), or a key or
      signature is not its exact size. A block carries at most one request per name and per new key,
      and a name has at most one pending change.
    - A block that carries a request carries no payment from its fee payer, and a payer's fees in one
      block add up to at most its pre-block balance. Intake, the proposer and `ApplyBlock` share one
      fee check, and intake counts a pooled request's fee as pending outgoing.
    - The ⅔ freeze and name action win over a request in the same block.
    - Intake checks sizes, the window, the fee and the frozen marks before any signature,
      and verifies off the event loop. A key change is about 16 KB (one key, three signatures), so
      its fixed field sizes bound it rather than `MaxMessageBytes`.
    - Bank and new-key signatures are not double-sign evidence, and two different requests for one
      name are logged for operators and freeze nothing. Wallets have no sign log, so a re-signed
      retry must never freeze an honest name.
  - **One key per bank** (decided). A bank co-signs with the same key it votes with. A compromised
    bank key is frozen at once, which stops its votes and its co-signatures together: one emergency
    brake. A frozen seated name leaves the validator set, with the power delegated to it, until the
    ⅔ name action re-keys it. Otherwise freezing banks that hold a third of the power would halt the
    chain and the ⅔ vote that repairs it.
  - **No history of requests** (decided). State keeps each name's current key, its pending change
    and a frozen list, and nothing else about past pairings.
    - A pending change is public for the whole delay, so the holder, the bank and the wallet can see
      it and freeze in time. The delay is the freeze window: a day by default, and the ⅔ can
      lengthen it.
    - A frozen name and its key stay listed until the ⅔ name action resolves the name, whether a
      holder, the ⅔ or a double-sign froze it. Nothing is kept about a key after that: keys are
      generated fresh, and the new key's signature already stops anyone binding someone else's key
      to a name.
    - The signature record keeps the ⅔ actions (vote freezes, re-keys, releases), as it keeps every
      governance certificate, but not the holders' own requests.

### Phase 3 — the deployment split

The single Go application stays the testing setup (decided after the review). Deployment ships as
three containers that can live on separate devices: the fetcher (the edge proxy facing the network),
the engine, and the signer. What the findings mean for them:

- **The signer** must refuse a second, different signature for the same height, round and type, and
  persist that before it signs. This stops self-slashing but not the fork: the engine still needs
  its own sign log with lock restore (E2). The engine must also cope with a signer that is down or
  refuses (E31).
- **Gossip routes** (proposal, vote, payment gossip, checkpoint) should be reachable only from other
  validators. The proxy forwards the wallet and admin routes.
- **The Phase 1 intake fixes are still needed**, because `/hdls/update` and the governance routes
  must be forwarded (E4, E8, E21).
- **The bank's desk** co-signs registrations and key and bank changes with the bank's one key,
  through the signer. The signer serves the desk only the co-signature and freeze domains, so a
  compromised desk terminal can co-sign but never vote. Cutting its access stops it. If the key
  itself may be exposed, the bank freezes its name.

### Order constraints

- The sign log (Phase 1, step 2) comes before anything that punishes double-signing: evidence
  harvest (E35), proposal evidence and the double-sign freeze (E13, E16).
- E31 lands with E2.
- E22 lands with or before E9.
- E34 comes before the double-sign freeze.
- E5 comes before relying on claw-backs, and before the freeze and name actions.
- E6's decoder comes before or with E4, because certificate checks stringify amounts.
- E4's node change ships with the wallet's sign-and-submit flow.
- The double-sign freeze's current-key rule replaces `maxEvidenceAge`. The signature record keeps
  nothing until the proposer's random value is in the signed digest.

### Tests that pin today's behaviour

- `TestSlashRemovesEquivocator` and `TestEvidenceAgeWindow` (`ledger/slash_test.go`) expect a
  slashed account to disappear. With E9 a named offender stays at zero, and with the double-sign
  freeze its name leaves the set.
- `TestDriverBrokenSignerDropsVoteQuietly` covers only the timeout path; extend it to the proposer
  and prevote paths (E31).
- Three node tests use `BlockID` 1000 fixtures that E21's window refuses.
- `TestOverdrawBanDestroysPayerBalance` should stay green through E24 and E33. Add cases for a
  credited payer and a named payer.
- About 57 test sites build ledger states or blocks without a height (Phase 2).

### Docs to correct

- `docs/security.md:59`: "no consensus fork under ≤ ⅓ Byzantine" holds only while no validator
  restarts mid-height (E2). Also note the 2^40 power cap (E45).
- `TODO.md:158`: the missing consensus WAL is a safety issue (E2).
- `findings.md` #7: the replay is exploitable today (E1). Also correct its overdraw trace ("dropped
  from the result entirely") (E33).
- `README.md:77`, `docs/consensus.md:79`, `docs/managing-the-currency.md:40`: what a slash really
  does (E13).
- `docs/governance.md:74-77`:
  - payments in the same block as a reassignment (E5);
  - grant-created accounts are undelegated (E42).
- `docs/operating-a-network.md`:
  - a re-genesis needs a new `networkId` and an empty `snapshotPath` (E20);
  - peer DNS applies only once every validator has published (E11);
  - joiners seed from a trusted node (E3);
  - the pin uses the full digest (E38);
  - compare resolve fields, not digests (E40).
- `ui/USER_MANUAL.md:41-42` and `:82`: paying yourself to a new wallet frees your name until E9
  lands, and renames go through the vote afterwards.
- `ui/USER_MANUAL.md` and `ui/README.md:40-47`: passphrase sealing (E27).
- `docs/managing-the-currency.md:308`: it lists seizures "with no public notice" as a misuse sign.
  Reconcile that with freeze first, then a public decision (E30). Keep the treasury undelegated
  (E42).

## Appendix — evidence

The PoC tests, harnesses, run logs and prototype fixes are in
[`_evidence_2026-10-27/`](_evidence_2026-10-27/README.md), whose README maps each finding to its
tests and prototype patch. The raw output of the three passes is in `_evidence_2026-10-27/results/`.
