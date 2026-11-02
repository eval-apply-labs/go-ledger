# TODO

The port is complete (names, pay-by-name, the self-describing peer DNS, ⅔ governance), and Phase 2
of the post-eMudhra review is built: ML-DSA-65, names as the identity, registration at a bank, key
and bank changes, the freeze, the ⅔ name action, the signature record and the idle chain (see
[`findings_20261027.md`](findings_20261027.md)). Phase 3, the network split and pigeon mode, is
built too, but for the signer. The owner's decisions are in `review/phase2_spec.md` §12 and
`review/phase3_spec.md` §13. None of the below blocks a testnet; it is the honest remaining work,
roughly by priority.

## Build

- [x] **Adversarial, Jepsen-style consensus test harness.** Two tiers, both green under `-race`:
      - `consensus/sim_test.go`: a deterministic driver-level simulator on a virtual clock (the
        driver is a pure state machine, so the whole network runs on one thread with one seeded
        random source; a failing seed replays exactly). Scenarios: agreement and liveness under
        delay, reorder and clock skew, agreement under message loss, and agreement with evidence
        against a double-signing validator. Scale it with `GOLEDGER_SIM_SEED_FACTOR`.
      - `node/chaos_test.go`: the real stack (event loop, pools, governance, the freeze, snapshots
        and catch-up) under a fault-injecting transport (loss, duplication, reorder, partitions)
        plus byzantine agents holding real keys. Two invariants hold throughout: no fork (one block
        hash per committed height) and no silent minting. Scenarios include a minority partition,
        catch-up by verified checkpoints, a double-signer frozen without a fork, the governance
        value-cycle replay staying inert, dynamic admission, and a random soak
        (`GOLEDGER_CHAOS_SOAK_SECONDS`, `GOLEDGER_CHAOS_SEED` to replay). Skipped under `-short`.

      It found a liveness gap, never a safety one: with no vote re-gossip, a partition that left
      no side above ⅔ froze for good. Vote re-gossip fixed it (below).
- [x] **Phase 3: the network split and pigeon mode.** Built on 2026-11-01, as a new genesis
      ([`review/phase3_spec.md`](review/phase3_spec.md)). The single Go program stays the testing
      setup, and deployment is containers that can live on separate machines, one per area of the
      docs ([`docs/README.md`](docs/README.md)):
      - the **engine** (`cmd/server`) serves a gossip port and an operator port. The gossip port
        lets in only the current validators' IP addresses, read from the chain, but for its own
        and loopback ones, and the config's `allow` entries, in the engine's own process. The
        operator port is never published;
      - every name may publish a public address and up to 4 IP addresses, and the genesis fixes one
        gossip port for the network;
      - the engine writes a public folder, and the **fetcher** (`cmd/fetcher`) serves it and the
        record files behind a CDN (a complete record unit as a file the CDN keeps for good), and
        passes payments, updates and name requests on, under a global cap;
      - pigeon mode: bundles exported and imported on the operator port, carried checkpoints, the
        slow-down switch (`minimumStepTimeoutMs`), the public bundle as a second path, and the
        wallet's payment saved as text and checkpoint from a file;
      - the images and the compose file (`deploy/`), and the quickstart (below).

      See [`docs/operating-a-network.md`](docs/operating-a-network.md).
- [ ] **The signer.** The fourth container: it holds the key, refuses a second, different signature
      for the same height, round and type, and writes that to disk before it signs. A bank's desk
      co-signs through the signer, which serves it only `registration`, `name-request-bank` and
      `freeze`. Not built: the engine still holds the key.

      A cheaper tier inside one process: keep the key encrypted in memory and decrypt it only into
      a locked buffer at signing time, zeroed after. Go's garbage collector does not move objects,
      so a key in a `[]byte` can be zeroed in place. `crypto/mldsa` still holds the expanded key in
      memory while it signs, so keeping the key off the machine needs hardware (a TPM, an HSM, a
      smartcard) or the remote signer.
- [ ] **Known limits**, listed in
      [`findings_20261027.md`](findings_20261027.md#not-fixed-or-not-covered): catch-up prefers a
      live bootstrap peer, even one behind the gossip peers, and the start-time sync asks only the
      bootstrap peers; heavy message loss can strand a node until it catches up; losing the sign log
      is only partly covered; a joiner trusts the snapshot it is seeded with.
- [ ] **Ledger archive, a history node off the lean validators.** A node keeps no payments: only the
      current state, the last block's header and commit, and the signature record (who signed each
      block for `retainBlocks` heights, and every governance certificate and double-sign proof for
      good). Some questions need more: *whose balance moved in the block where supply jumped? is one
      account's funded, signed payments consistently never landing (soft censorship)?* An archive
      node, a separate program on a machine with real disk, would keep what the validators discard:
      it observes the committed blocks, each with its ⅔ commit, so it cannot invent a past no quorum
      signed, and keeps every block's content.
    - Shape: its own module (like `ui/`) or a `cmd/historian`, so it can use whatever storage it
      needs without touching the zero-dependency core.
    - What it answers: supply over time; per-account history; validator-set and delegated-stake
      concentration over time; censorship. The signature record already answers who signed each
      block and which certificates were voted; the archive adds the payments.
    - The tension: an archive rebuilds the permanent record of payments the design avoids. So it
      stays opt-in and off to the side.
- [ ] **The rest of the name service.** Names publish a public address and IP addresses, which
      their own keys sign ([`docs/governance.md`](docs/governance.md#names-as-a-name-service)). Two
      pieces are for later (`review/phase3_spec.md`, R7 and the notes for later in §13):
    - A DNS gateway: a small program that answers DNS queries for a zone from the chain's name
      records. Then ordinary resolvers, and the DNS operators who adopt it, can use them.
    - TLS key records: a name publishes a TLS key, or its hash, signed by the name's key, as DANE's
      TLSA records do in DNS. TLS then works with no certificate authority wherever the client
      reads the chain: between banks, and in wallets. The payment key stays off the web server, and
      the chain's own scheme does not limit the TLS key's algorithm. Browsers accept neither DANE
      nor such certificates for websites today, so this serves custom clients first.

## Follow-ups from the external review (2026-08-02, see `review/` and `findings.md`)

- [x] **C2: stake to `int64` voting power.** Power saturates at `consensus.MaxValidatorPower`
      (2^40), enforced in `consensus.NewValidatorSet`, the one choke point for both modes, with an
      `IsInt64` guard in `ledger.ValidatorRule.SetFor`. Pinned by `TestValidatorSetSaturatesPower`
      and `TestDynamicSetSaturatesHugeStake`. Since code note C4, `ledger.ValidateGenesis` refuses a
      static power above the cap instead of cutting it down
      (`TestGenesisRefusesWhatNoLedgerCouldHold`).
- [x] **C7: HTTP server timeouts.** `cmd/server/main.go` sets read, write and idle timeouts. Rate
      limiting and authentication (#10) are the deployment's since Phase 3: the gossip routes are
      behind the allow list, the public face behind a CDN, which limits each client, and the
      fetcher's global cap; the operator port has neither, and is never published.
- [x] **G6, G7, G8: untested consensus paths.** `TestDriverPOLUnlockReleasesLock`,
      `TestDriverBrokenSignerDropsVoteQuietly` (a test scheme whose keys cannot sign) and
      `TestDriverRejectsFarFutureAndNegativeRounds`.
- [x] **C5: deterministic pending-governance selection.** `pendingParameterChange` and
      `pendingReassignment` scan sorted keys.
- [x] **C8, C11: invariants documented where they live.** The backup goroutine's lifetime and the
      NFS caveat at `saveSnapshot`; the fresh-accumulator note on `ValidatorRule.SetFor`.
- [x] **CI: chaos-test deadline slack.** `GOLEDGER_CHAOS_SLACK` stretches the integration tests'
      deadlines (`testTimeSlack` in `node/sync_test.go`); CI sets it to 4.

Earlier deferrals (tracked in `findings.md`): the height in `LedgerState` (#7) and `ApplyBlock`
returning a reason instead of a bool (#26) are done in Phase 2. Rate limiting and authentication on
the open endpoints (#10) are answered by the deployment in Phase 3, as above. The gossip port has
no TLS: every message on it is signed, and nothing on it is secret.

## External (not code)

- [ ] **Independent human audit**: the real gate before significant value rides on it. An LLM review
      is an input, not a substitute for expert human eyes on the consensus code.

## Housekeeping

- [x] **Origins URL.** `README.md` (Origins) points at the upstream Java project.
- [x] **Deployment.** `deploy/Dockerfile` builds the engine, the fetcher and `keygen` into one
      image, `deploy/ui.Dockerfile` the wallet into another, and `deploy/compose.yaml` runs the
      quickstart (Phase 3). No systemd unit is written.
- [x] **Screenshots.** `docs/wallet.png` and `docs/admin.png` were retaken from `make demo` on
      2026-10-31, and `README.md` shows them again.
- [ ] **Test messages.** A few test failure messages open with an upper-case scenario label
      (`FORK:` in `consensus/sim_test.go` and `node/restart_test.go`, `REPLAY LANDED:` in
      `node/chaos_test.go`); write them as plain sentences.

> The original Java implementation is out of scope and stays as it is.

## Small and optional refinements

- [x] **The lost-sign-log wait at every height.** A validator back without its sign log waited one
      re-gossip interval only at the height it started at, so from a snapshot below the last height
      it voted in it could catch up or commit into that height and double-sign. Now it waits, two
      re-gossip intervals, or twice the slow-down switch when that is longer
      (`Node.ownSignaturesWait`, since the Phase 3 review's B2), at every height it may have signed
      at (`Node.waitForOwnSignatures`): the one it starts at; each later one it commits into, up to
      the highest at which a vote or proposal of its own, verified under its key, comes back to it
      (`Node.noteOwnSignature`); and the network's height, which a catch-up from those takes it to.
      It keeps no vote of its own for a later height: the wait there brings it back. While it waits
      its sign log stays below the height, so a restart then waits too, and it takes back its own
      proposal of any round up to 256; its first new sign log at a height holds what it took back,
      and once it has a log, the log keeps the bound, so later restarts keep both; a start timer
      starts only the height it was armed for. The wait is best effort, by timing. Found by the
      hardening check of 2026-10-31; the independent check and its recheck added the rest. Tests,
      in `node/restart_test.go`:
      `TestValidatorBackWithoutItsSignLogWaitsAtTheHeightItCatchesUpTo`,
      `TestValidatorBackWithoutItsSignLogResumesItsProposalAfterACatchUp`,
      `TestValidatorBackWithoutItsSignLogWaitsAtTheHeightItCommitsInto`,
      `TestValidatorBackWithoutItsSignLogStillWaitsAfterAnotherRestart`,
      `TestValidatorBackWithoutItsSignLogNeverLowersItsBound`,
      `TestValidatorBackWithoutItsSignLogTakesBackItsProposalOfAnyRound`,
      `TestValidatorBackWithoutItsSignLogResendsItsProposalOfRoundTwo`,
      `TestValidatorBackWithoutItsSignLogLogsWhatItTookBack`,
      `TestValidatorBackWithoutItsSignLogKeepsABoundNotedDuringALaterWait`,
      `TestValidatorBackWithoutItsSignLogKeepsItsBoundThroughRestartsDuringAWait`,
      `TestValidatorBackWithoutItsSignLogWaitsTwoReGossipIntervals`,
      `TestValidatorBackWithoutItsSignLogTakesBackOnlyItsOwnProposalsOfLaterRounds`, and
      `TestSignLogOfANodeThatNeverLostItHoldsNoBound`; in `node/pigeon_test.go`, with the switch on,
      `TestPigeonValidatorWithoutItsSignLogWaitsForTheNextCarry`.
- [x] **A four-validator quickstart.** `make quickstart` makes the keys, a genesis file and four
      node configs with `cmd/quickstart`, a small Go program, and runs four banks on one machine,
      each an engine and a fetcher, with Docker Compose or Podman (`deploy/compose.yaml`).
      `make quickstart-clean` removes them. See
      [`docs/operating-a-network.md`](docs/operating-a-network.md#four-banks-on-one-machine-the-quickstart).
- [x] **Vote re-gossip.** While a node runs a height, it re-sends the height's votes and the current
      round's proposal every `stepTimeoutMs` in force (`Node.reGossipLoop`), so a merged partition
      re-exchanges what each side held. Tests: `TestChaos2x2PartitionFreezesThenRecovers`,
      `TestChaosLostProposalRecoversViaReGossip`. An idle node re-gossips nothing.
- [x] **Post-quantum signing.** ML-DSA-65 from Go's `crypto/mldsa` is the only scheme since the
      Phase 2 genesis. Changing the scheme again means a new genesis (see
      [`docs/operating-a-network.md`](docs/operating-a-network.md#starting-a-new-network)).
- [x] **Consensus-state write-ahead log**, done as the sign log (`<snapshotPath>.votes`): each own
      vote and proposal is on disk before it is counted or sent, and a restart resumes from it.
- [ ] Targeted "whisper" relay: gossip floods every peer. A point-to-point relay would use less
      bandwidth, if that ever matters.
- [ ] The snapshot write: on a node with `snapshotPath`, every commit and catch-up marshals the
      whole checkpoint (about 2.9 KB per account) and writes and fsyncs it on the event loop. With a
      `publicPath`, the loop also makes the public folder's checkpoint and account list, each about
      as large, at every commit; their writes already run off the loop. Move the work off the loop,
      or make it incremental, before a network grows large (the checkpoint's size:
      `review/phase2_spec.md` §9.2 and §12 Q18; the public folder's: `review/phase3_spec.md` §6.1).
- [ ] More governable values: ten parameters are governed. The steps to govern another are in
      [`docs/governance.md`](docs/governance.md#extending-it); it needs a new genesis.
