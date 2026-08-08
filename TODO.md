# TODO

The revival is complete (naming registry, pay-by-name, self-describing peer DNS, ⅔ governance) on a
hardened core (malleability, resource bounds, slashing, catch-up). None of the below is blocking; it
is the honest remaining work, roughly by priority.

## Build

- [x] **Adversarial / Jepsen-style consensus test harness.** Two tiers, both `-race`-green:
      - `consensus/sim_test.go` — a DETERMINISTIC driver-level simulator on a virtual clock (the
        Driver is a pure state machine, so the whole network runs single-threaded with one seeded
        RNG; a failing seed replays exactly). Scenarios: agreement+liveness under delay/reorder/clock
        skew, agreement under message loss, and agreement + slashable-evidence with an equivocating
        validator. Scale with `GOLEDGER_SIM_SEED_FACTOR`.
      - `node/chaos_test.go` — the REAL stack (event loop, mempool, governance, slashing,
        snapshot/catch-up) under a fault-injecting transport (loss, duplication, reorder,
        partitions) plus byzantine agents holding real keys. Two invariants checked throughout: no
        fork (one digest per committed height) and no silent minting (supply ≤ cap). Scenarios:
        loss/dup/reorder liveness, minority partition (can't progress or fork), gossip-isolated
        catch-up via verified checkpoints + rejoin, equivocator slashed-out without fork, the
        governance value-cycle replay staying inert, dynamic validator admission converging, and a
        randomized soak (`GOLEDGER_CHAOS_SOAK_SECONDS`, `GOLEDGER_CHAOS_SEED` to replay). Skipped
        under `-short`.

      Findings surfaced (liveness, not safety — safety held in every run): with no vote re-gossip
      and step timeouts that grow per round, a partition that leaves NO side above quorum-any (a 2|2
      split, or a reduced quorum with zero slot slack under loss) could freeze rather than self-heal
      after the partition lifts. FIXED by vote re-gossip (see the small-refinements item below);
      the harness now asserts freeze-then-recover and majority-progress-under-loss instead.
- [ ] **Remote / airgapped validator signer** — restores a capability the original DistributedLedger
      had (`EncryptionEntity_ExternalServer`) that the Go port dropped in favour of an integrated
      signer only. Abstract the node's signer behind an interface: `LocalSigner` (holds the key —
      today's behaviour) vs `RemoteSigner` (POSTs the length-prefixed *preimage* to a signing service /
      enclave; the private key never enters the network-exposed node process). Fits cleanly behind the
      `signing` package — the preimage is already the unit that crosses that boundary.
      **Not a threat in the target model:** a known, co-located, low-value village network on an
      isolated LAN is not defending against key exfiltration off a node, so this stays optional here.
      It earns its keep only as the deployment leaves that envelope — a validator holding real stake,
      or an internet-exposed node — i.e. the same scale at which `docs/managing-the-currency.md` says
      you are building a different system. Worth keeping on the map; not worth blocking on.

      Cheaper in-process tier (more useful here than the remote signer, and a free win from dropping
      the JVM): keep the key **encrypted in RAM and decrypt only into an `mlock`ed buffer at sign
      time, zeroed immediately after** — the pattern `github.com/awnumar/memguard` already implements
      (guarded pages, `mlock` against swap, encrypt-at-rest-in-RAM, wipe-on-use). This is exactly
      where losing the JVM pays off: Java fought you (immutable, interned `String`s that linger in the
      heap; a compacting GC that copies key bytes you can never reliably zero; heap dumps), whereas
      Go's non-moving GC lets you hold the key in a `[]byte` you can zero in place and `mlock` against
      swap. Honest ceiling: stock `crypto/ed25519` operates on the scalar **in RAM** during signing,
      so "only ever in CPU registers / never in RAM" is not reachable without hardware (TPM / HSM /
      SGX-style enclave / smartcard) — which is just the remote-signer path co-located. So the two are
      one ladder: memguard-style hardening cheaply mitigates *at-rest / forensic* leaks (swap files,
      core dumps, cold-boot), and the enclave/remote signer is what raises the bar against a *live
      privileged* attacker.
- [ ] **Ledger archive / history node — a separate app, off the lean validators.** The core is
      deliberately historyless (only "now" + the last commit), which is what keeps a validator light
      and the past un-rewritable — but it discards the audit trail the original DistributedLedger kept
      in `LedgerHistory`, and some questions are historical: *was that mint authorised by the vote that
      supposedly ratified it? whose balance moved in the block where supply jumped? is one account's
      funded, signed payments consistently never landing (soft censorship)?* An **archive node** — a
      separate app alongside `cmd/server`, meant to run on a box with real disk — rebuilds that record
      WITHOUT weighing the validators down: it observes the committed block stream (each `Block` carries
      its full content *and* its ⅔ `Commit` certificate, so the archive is verifiable and
      tamper-evident — it cannot fabricate a past no quorum signed), retains the whole chain of state
      transitions the nodes throw away, and exposes forensic queries.
    - Shape: its own module (like `ui/`) or a `cmd/historian`, so it can use whatever storage it needs
      (a real DB, lots of disk) without touching the zero-dependency core. It can run as a passive
      observer or as a validator that also archives.
    - What it answers: supply over time; every mint / reassignment / slash certificate cross-checked
      against the votes that authorised it (an unmandated one is the smell); a per-account history;
      validator-set and delegated-stake concentration over time; and censorship (an account whose
      valid payments never make it into a block). This turns the "smells of misuse" in
      `docs/managing-the-currency.md` from a claim ("abuse is legible") into an actual instrument —
      the thing that lets you investigate a chain shock and decide whether someone played dirty.
    - The deliberate tension (already named in managing-the-currency.md): an archive rebuilds the
      permanent record the historyless design avoided. So it stays OPT-IN and off to the side — run
      one, or a few independently to cross-check each other, when a community wants forensic oversight;
      the base network keeps its light, historyless, nothing-to-rewrite property.

## Follow-ups from the external review (2026-08-02, see `review/` + findings.md)

The review's real defects (C1/C3/C4) are fixed and pinned; the rest of its items are now closed
too (same day, follow-up pass):

- [x] **C2 — stake → `int64` voting-power truncation.** DECIDED: power **saturates** at
      `consensus.MaxValidatorPower` (2⁴⁰) instead of truncating or dropping. Enforced in
      `NewValidatorSet` — the single choke point both the fixed-config and stake-derived paths go
      through — with an `IsInt64()` guard in `deriveValidators` for the floor itself. Rationale:
      raw `Int64()` can wrap negative (validator silently dropped — a liveness stall) or
      small-positive (corrupted quorum denominators); rejection is the same silent drop; saturation
      keeps the whale seated with the largest possible voice while every power sum stays far inside
      int64 for any realistic set. Pinned by `TestValidatorSetSaturatesPower` and
      `TestDeriveValidatorsSaturatesHugeStake`. Behaviour differs from before only for stakes past
      2⁶³ — unreachable in any real deployment — so it rolls node-by-node.
- [x] **C7 — HTTP server timeouts.** `cmd/server/main.go` sets `ReadHeaderTimeout`/`ReadTimeout`/
      `WriteTimeout`/`IdleTimeout`, bounding handler-goroutine pile-up behind a saturated event
      loop (the concrete DoS mode of deferred #10 — which stays open for rate limiting/auth).
- [x] **G6/G7/G8 — the untested consensus paths.** `TestDriverPOLUnlockReleasesLock` exercises the
      proof-of-lock release (locked on X, POL for Y at vr ≥ lockedRound → prevotes Y) — the
      complement of the existing locking test. `TestDriverBrokenSignerDropsVoteQuietly` pins the
      castVote error branch with a wrong-length-key signer (no injection seam needed — `Ed25519.Sign`
      length-checks at sign time). `TestDriverRejectsFarFutureAndNegativeRounds` pins the
      `maxRoundLookahead` allocation bound including the boundary round.
- [x] **C5 — deterministic pending-governance selection.** `pendingParameterChange` /
      `pendingReassignment` scan sorted keys (hygiene only — verified not a fork risk).
- [x] **C8/C11 — invariants documented where they live.** The `BackupPath` goroutine's lifetime
      (may outlive `Stop()`; worst case one stale-but-valid, certificate-verified checkpoint) and
      NFS caveat are at the `go writeAtomic` site; the fresh-accumulator immutability note is on
      `deriveValidators` itself.
- [x] **CI: chaos-test deadline slack.** The starved-runner flake recurred (twice on the same test,
      different seeds, both replaying green locally in a tenth of the wall time), which was the
      agreed trigger: `GOLEDGER_CHAOS_SLACK` now stretches the integration tests' real-time
      deadlines (`testTimeSlack` in `node/sync_test.go`, applied in `waitFor` and the one inline
      deadline), and CI sets it to 4. Deadlines stay tight for developer machines.

Standing deferrals from the earlier review rounds (tracked in findings.md): height/nonce in
`LedgerState` (#7), rate limiting / authenticated transport on the open endpoints (#10),
`ApplyBlock` returning a reason instead of a bare bool (#26).

## External (not code)

- [ ] **Independent human audit** — the real gate before significant value rides on it. An LLM review
      (this one) is a real input, not a substitute for expert human eyes on the consensus code.

## Housekeeping / user-side

- [x] **Origins URL.** `README.md` (## Origins) points at the upstream Java project as the origin.
- [ ] **Deployment** (optional): a systemd unit / Dockerfile / compose for running a node as a service.

> The original Java implementation is out of scope and stays as-is — this repository is the Go port
> and nothing here depends on building or changing the Java tree.

## Small / optional refinements

- [x] **Vote re-gossip** (liveness, surfaced by the chaos harness). Each node now periodically
      re-broadcasts the current height's votes (`Node.reGossipLoop` → `evRegossip` →
      `reGossipVotes`), so a partition leaving no side above quorum-any no longer freezes permanently:
      once the split heals the merged network re-exchanges the held votes, reaches quorum, and
      advances. Safety is untouched (the driver dedups on receipt). Regression test:
      `TestChaos2x2PartitionFreezesThenRecovers` (freeze while split, self-heal on merge).
      The proposal caveat is closed too: the CURRENT round's proposal is re-sent on the same cadence
      (`reGossipProposal`), so a round whose proposal broadcast was lost decides instead of burning
      the round timing out to nil. Only the current round's (large) block is re-flooded, and every
      receiver dedups it before re-running `ApplyBlock` (`onProposal`'s held-proposal fast path).
      Regression test: `TestChaosLostProposalRecoversViaReGossip` — every proposal's FIRST
      transmission is dropped network-wide, so any commit at all proves the re-send path.
- [ ] **Multi-scheme / post-quantum signing** — the original DistributedLedger tested RSA *and*
      ML-DSA-87 (switchable via a commented `application.properties` line). The Go port is Ed25519-only
      but kept the `signing.Scheme` interface, so ML-DSA drops in without touching domain separation.
      The commented, non-default slot now lives at `signing/mldsa.go` with the uncomment-ready
      skeleton. It stays commented — not a two-line flip like Java — because Go's stdlib ships no
      ML-DSA signer (`crypto/mlkem` is KEM, not signatures), so activating it needs either a future
      stdlib `crypto/mldsa` (keeps zero-dep) or a third-party module (breaks zero-dep). Deliberate
      deferral, not forgotten; low value at village scale. Note it is not a one-way door: because a
      live network can snapshot its agreed ledger and reseed a fresh genesis under a new scheme (a
      planned ~2h outage, not a hard fork — see `docs/operating-a-network.md`, "Migrating the
      ledger"), moving to post-quantum stays a real governance decision even after genesis.
- [ ] **Consensus-state WAL** — a node persists only COMMITTED checkpoints, so a crash mid-height
      loses its in-progress votes and it restarts the height (or catches up from a peer). Benign at
      this scale and not a safety issue (re-sync covers it); a write-ahead log of votes/step would let
      a node resume its exact consensus position after a crash, as Tendermint does. Note it, don't
      rush it.
- [ ] Targeted "whisper" relay: gossip currently *floods* (broadcasts to all peers). A fallen-off node's
      vote still gets in, but if bandwidth matters, a point-to-point whisper-then-relay would be leaner.
- [ ] More governable parameters: the ⅔ mechanism is generic — the transaction cost and the timing
      profile (block interval / step timeout / max-peers, coupled) are governed; to add mempool bounds
      or the validator stake threshold, add the field to `LedgerParameters.fields` and extend
      `Acceptable` if it has an invariant.
