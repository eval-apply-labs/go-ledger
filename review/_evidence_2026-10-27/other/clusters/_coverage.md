# Round-1 coverage map

## All merged clusters (titles)
- C01-slash-not-burn: [burn-rule] SLASH-1 — Static-validator mode: a slashed equivocator is NOT ejected from consensus (keeps full voting power); a ≥1/3 compromised key is an un-recoverable permanent stall | [burn-rule] SLASH-2 — Dynamic mode: stake delegated by OTHER accounts keeps a slashed equivocator in the validator set — an un-slashable Byzantine validator | [ledger-transition] SLASH-1 — A slash does not burn the key: delegated stake keeps the equivocator in the dynamic set, the static set is untouched, and there is no tombstone | [governance] GOV-4 — Slashing does not durably burn a proven double-signer's validating or governance power | [burn-rule] DOCS-1 — Docs claim slashing 'ejects the double-signer from the set' / 'zeroes it out' — the code removes an account, not the key's place in the validator set
- C02-name-release-on-deletion: [burn-rule] NAME-1 — Deleting an account (spend-to-zero or slash) frees its registered name — a leaked key both drains AND hijacks the victim's name and all future pay-by-name traffic | [ledger-transition] NAME-1 — Zero-balance deletion releases a registered name; another account can claim it in the same block and receive pay-by-name payments
- C03-proposal-equivocation: [burn-rule] PROP-1 — Proposer equivocation (two different signed proposals for one height/round) is never detected or slashed — and the second is relayed and re-applied network-wide | [consensus-core] SLASH-2 — Proposal equivocation is unslashable and both conflicting proposals are relayed
- C04-proposal-body-unsigned: [flake-suite] LIVE-3 — Swapped-body proposal under a genuine signature poisons a round (signature does not cover the block body)
- C05-ban-escape-by-credit: [burn-rule] BAN-1 — The overdraw ban is escaped when the banned payer also receives a same-block credit — the payer keeps its full balance and name, contradicting the documented/pinned 'balance destroyed' invariant | [ledger-transition] OVERDRAW-2 — A banned payer that also receives a credit in the same block keeps its whole balance
- C06-single-overdraw-ban: [ledger-transition] OVERDRAW-1 — Overdraw ban fires on a single over-balance payment; the wallet signs any amount, so the documented same-block closure does not protect honest payers
- C07-rat-exponent-amplification: [ledger-transition] RAT-DECODE-1 — Exponent-form big.Rat amounts: an 11-byte JSON field costs ~1.5 MB of heap and seconds of CPU before any size check | [governance] GOV-7 — Unauthenticated governance vote stalls the single event loop ~9s via big.Rat exponent amplification | [node-input] NODE-IN-1 — Tiny exponent-form big.Rat amounts cause superlinear per-request work on the single event loop (1 MiB body bound does not bound the work)
- C08-rat-denominator-growth: [ledger-transition] RAT-GROWTH-1 — MaxMessageBytes bounds messages, not balances: coprime-denominator payments grow one balance without limit, at super-linear digest and arithmetic cost
- C09-votes-relayed-unverified: [consensus-core] DOS-1 — Unauthenticated vote intake + unbounded heightVotes + wholesale re-gossip = amplifying DoS | [node-input] NODE-IN-2 — Consensus votes are relayed and stored (heightVotes) before any signature/validator check — junk amplification + unbounded map
- C10-governance-pool-squat: [governance] GOV-3 — Governance vote pools are fillable with unauthenticated voteless entries, freezing governance permanently | [node-input] NODE-IN-3 — Governance vote pools create a proposal slot before verifying any vote — unauthenticated 64-slot fill permanently bricks parameter/reassignment voting
- C11-departed-voter-poisons: [governance] GOV-5 — A departed voter's stuck pool vote poisons a proposal so a clear live quorum can never carry it
- C12-reassignment-outflow-mint: [governance] GOV-1 — Reassignment applied post-payments as an absolute set mints the target's same-block outflow
- C13-timing-no-ceiling: [governance] GOV-2 — LedgerParameters.Acceptable has no upper timing bound: a ⅔ vote can halt the chain irreversibly
- C14-regenesis-networkid-replay: [key-lifecycle] KEY-2 — Signatures bind only the network-id string, not a genesis/chain digest — same-id deployments and the documented re-genesis migration replay payments and governance certificates | [governance] GOV-6 — Governance certificates and payments replay across a re-genesis (sequence and BlockID reset, networkId reused)
- C15-block-size-unbounded: [ledger-transition] BLOCK-SIZE-1 — No per-block size bound: fee-free updates push every proposal past peers' 1 MiB decode limit and halt the chain
- C16-below-cost-free-carriage: [ledger-transition] FEE-1 — Below-cost payments are admitted, receipted and carried by honest nodes with no fee; other zero-fee no-ops still pass ApplyBlock
- C17-digest-nil-vs-zero: [ledger-transition] DIGEST-1 — State digest encodes nil and zero amounts identically; a checkpoint with null amounts verifies and then panics the state transition
- C18-base64-key-aliases: [ledger-transition] KEYALIAS-1 — One Ed25519 key has unboundedly many accepted base64 spellings; in dynamic mode it can hold several validator identities and equivocate across them without evidence
- C19-long-range-and-rotation-brick: [sync-trust] SYNC-1 — Long-range checkpoint forgery: joiners trust genesis validator keys forever (no weak subjectivity) | [sync-trust] SYNC-3 — Full validator rotation past genesis permanently bricks onboarding and crash-recovery | [sync-trust] SYNC-4 — Docs claim onboarding is 'safe even from an untrusted bootstrap peer' without the weak-subjectivity caveat
- C20-checkpoint-fetch-unbounded: [sync-trust] SYNC-2 — Catch-up checkpoint fetch has no response-size bound (memory DoS)
- C21-post-commit-window-and-stale-timers: [flake-suite] LIVE-1 — Block proposal arriving in the post-commit window is dropped and the height's first round is lost | [flake-suite] LIVE-2 — Consensus timeouts are not height-scoped: a stale timer fires into a later height's driver | [flake-suite] DOC-1 — Docs claim healthy-network cadence and self-heal that the proposal-window drop contradicts
- C22-wallet-dns-rebinding: [wallet-ui] KEY-1 — DNS-rebinding bypasses the CSRF/origin guard — drive-by signed payments and governance votes with the local key
- C23-wallet-env-passphrase-noop: [wallet-ui] KEY-2 — Setting GOLEDGER_WALLET_PASSPHRASE as instructed does NOT seal an existing plaintext wallet — key stays in plaintext, silently
- C24-wallet-blockid-counter-poison: [wallet-ui] KEY-3 — Persisted payment-block counter is poisoned permanently by one bad/malicious height, and survives a documented re-genesis — payments then never land
- C25-admin-mint-name-via-node: [wallet-ui] KEY-4 — Admin reassignment (mint) votes resolve the typed name to a key via the untrusted node — validator signs a certificate targeting a key it never saw
- C26-keypair-consistency: [key-lifecycle] KEY-1 — Config load does not verify the private key matches the configured public key (incomplete C4 fix; no sign+verify self-test) → silent consensus ghost | [wallet-ui] KEY-5 — Wallet trusts the stored publicKey over the private key; GCM uses empty associated data (KDF params / version / pubkey unauthenticated)
- C27-demo-tmp-key: [key-lifecycle] KEY-4 — Demo writes the sole-validator private key in plaintext to a predictable os.TempDir() path with no O_EXCL/symlink safety and never deletes it
- C28-evidence-dropped-on-catchup: [consensus-core] SLASH-1 — Equivocation evidence is dropped when a node advances a height by catch-up instead of deciding locally
- C29-power-saturation: [consensus-core] SAT-1 — Power saturation equalizes all above-cap validators, inverting the '<1/3 of stake' premise
- C30-prevstatedigest-pin: [ledger-transition] PIN-1 — PrevStateDigest tier compares the account-only digest, but the docs and the API point clients at the full committed digest
- C31-genesis-validation: [ledger-transition] GENESIS-1 — Genesis accepts duplicate keys, zero or negative amounts and duplicate names; a duplicated key silently loses balance
- C32-float-topology: [ledger-transition] TOPO-1 — Consensus-critical Acceptable() depends on float Pow/Ceil at integer boundaries
- C33-resolve-binding: [node-input] NODE-IN-4 — /hdls/resolve answer is not provably bound to its digest; documented cross-check (compare digests) is insufficient, and Height is off by one
- C34-nonvote-equivocation: [burn-rule] GOV-1 — Account-update and governance-vote equivocation are undetected (two conflicting signings, no burn) — benign today, but the principle is absent
- C35-payments-no-burn-slot: [burn-rule] PAY-1 — Payments have no conflict/burn slot — the burn rule never fires against a leaked payment key (the owner's eMudhra hypothesis does not hold in this code)
- C36-no-key-rotation: [key-lifecycle] KEY-3 — No key rotation or revocation: a compromised account/validator key cannot be recovered faster than an attacker can spend, and 'two-signings-burns-the-key' does not cover ordinary keys
- H1-crash-restart-self-equivocation: H1-crash-restart-self-equivocation (new hypothesis from the lead reviewer — NOT yet tested)
- H2-rename-release-hijack: H2-rename-release-hijack (new hypothesis from the lead reviewer — NOT yet tested)

## What round-1 reviewers examined and found sound (checked_ok)

### key-lifecycle
- Entropy source: every production key-gen path uses crypto/rand — signing/ed25519.go GenerateKey (ed25519.GenerateKey(rand.Reader)), cmd/keygen/main.go, ui/keyfile.go loadOrCreateKey, cmd/demo newAccount. No NewKeyFromSeed, fixed seed, math/rand, or time-based key anywhere in non-test code (math/rand appears only in sim/chaos fault scheduling).
- signing/mldsa.go is entirely commented out — no deterministic/alternate scheme can be reached by accident; scheme selection is Ed25519 everywhere.
- Wallet at-rest sealing (ui/keyfile.go): per-seal fresh 16-byte random salt and fresh random GCM nonce, PBKDF2-HMAC-SHA256 at 600k iterations (matches published vectors in keyfile_test.go), AES-256-GCM; a wrong passphrase or tampered ciphertext is a hard error, never a silent fresh-key overwrite of a funded wallet.
- cmd/server config load (C4/C3) verified present in current code: refuses wrong-length keys and a negative transactionCost, and runs Acceptable() on assembled genesis parameters.
- No private key is logged (cmd/server/main.go startup line logs only network/listen/peers and a validators label) and no HTTP endpoint serves private key material; keygen prints locally and transmits nothing.
- The 'two-signings-burned' principle is correctly and unforgeably implemented for validator consensus equivocation: consensus/evidence.go Valid requires two validly-signed conflicting votes by the same key, and ledger applySlashes removes the offender deterministically; an honest validator cannot be framed.
- Receipt/vote/update/payment/governance domains are distinct and network-bound (signing/domain.go), so a signature in one role cannot be replayed as another within a network (the id-binding gap in KEY-2 is about ACROSS deployments, not across domains).

### burn-rule
- Vote (prevote/precommit) equivocation IS detected in-round and acted on: VoteSet.Add un-counts the second conflicting vote and records Evidence (consensus/voteset.go:160-169) — the one working link of the burn chain.
- Equivocation evidence is unforgeable and cannot frame an honest key: Evidence.Valid requires both votes signed by the offender (consensus/evidence.go:11-22) and ApplyBlock rejects a block whose evidence does not verify (ledger/block.go:205-209); TestSlashRemovesEquivocator's framing case still passes.
- Evidence is age-windowed (evidenceInWindow, maxEvidenceAge=1000, ledger/block.go:59-65) so stale evidence cannot be replayed against a recreated account — verified not regressed.
- Quorum intersection (>⅔, consensus/validator.go:63) still prevents two conflicting block commits and two conflicting governance ratifications even if a validator double-votes across competing proposals.
- Governance single-use via GovernanceSequence (ledger/parameters.go:319-323, bumped in ApplyBlock) prevents value-cycle replay of parameter/reassignment certificates — confirmed against TestChaosGovernanceCycleReplayStaysInert and reassignment_test.go.
- Commit is round-scoped: Commit.Verify requires every precommit to share c.Round (consensus/commit.go:260), closing the mixed-round forge that would let a bad checkpoint verify.
- Payment identity is content-based (PaymentID, ledger/payment.go:244), so a malleated signature cannot double-count a payment at mempool/gossip/ledger.
- Gossip payment intake verifies the payer signature before admitting (node/httptransport.go:213), so the mempool cannot be filled with unsigned junk.

### consensus-core
- Commit.Verify is round-scoped and rejects mixed-round, wrong-Round, duplicate-voter, tampered, and below-quorum certificates (consensus/commit.go:155-172; consensus_test TestCommitRejectsMixedRounds et al.) — the forged-certificate-on-onboarding HIGH stays fixed.
- Decision rule (driver.go:303-312) fires only on a locally-held proposal that is p.Valid (ApplyBlock-validated by the caller), non-nil, with a same-round precommit quorum — a node cannot decide a value it never received or never validated, and never decides nil.
- Proof-of-lock is verified against the node's OWN prevote set (driver.go:337-340 checks d.prevotes[vr].HasQuorumFor), not trusted from the proposal; the unlock/lock rules (prevoteFor, driver.go:385-398) match Tendermint and the POL-release and conflicting-prevote tests pass.
- The step>=StepPrevote guard on the 2/3-prevote one-shot (driver.go:349) is present, preventing the propose-step consumption stall.
- maxRoundLookahead (256) bounds per-round VoteSet allocation; negative and far-future rounds are rejected before ensureRound (driver.go:216-218); future-HEIGHT votes are dropped by the driver (not buffered).
- Per-height validator set is frozen once at startHeight (node.go:472) and reused by driver, onProposal, and commit — in dynamic mode all honest nodes derive the same set from identical committed state, so they cannot disagree on who verifies votes for height H.
- Checkpoint skipping-trust rule is sound: Commit.Verify against the carried signing set + digest-binding to the supplied accounts + >1/3 anchor-power overlap (node.go:388-401); a fabricated all-stranger set has no anchor overlap and a nil-digest commit cannot bind a real SHA-256 state digest.
- Proposer selection (validator.go ProposerAt) is a deterministic pure function of (height,round,set) via FNV, returns '' for an empty/zero-power set, and is stake-weighted without a usable single-key grind (seed is fixed by height/round).
- Equivocation Evidence.Valid is unforgeable (both votes must carry the offender's own signatures), so an honest validator cannot be framed; in-round un-counting of equivocators is correct.
- onProposal authenticates proposals against driver.Proposer(round) and VerifyProposal before use, and does not relay an identical-digest duplicate — the unauthenticated-front-run HIGH stays fixed (the residual is the different-digest relay in SLASH-2).

### ledger-transition
- Propose/apply symmetry for accepted blocks: both run ProcessPayments → applyAccountUpdates → applySlashes → applyReassignment under the pre-block TransactionCost and MaxMessageBytes, and ApplyBlock rejects wherever ProposeBlock filters (signature, BlockID, pin, size, recipient, evidence window, governance validity, at most one governance action).
- Digest coverage: all six Account fields (incl. Address), all five LedgerParameters fields and GovernanceSequence are hashed; sub-digests are fixed-size; fields are length-prefixed; JSON-decoded Rats are normalized, so RatString is canonical (the only collision is DIGEST-1's nil vs 0).
- Governance replay: ParameterChange and Reassignment are bound to the sequence and single-use in both paths; at most one governance action per block is enforced.
- No mint through payments: negative or nil cost is clamped, negative amounts are excluded, phantom payers can't mint, malleated copies dedup by PaymentID, and self-payment nets exactly -fee.
- Pay-by-name resolves against the pre-block registry in ProcessPayments and in both block filters, so a same-block name move cannot fork resolution; payments naming both or neither recipient are rejected in both paths and at intake.
- Copy-on-write: ProcessPayments allocates fresh Rats, and applyAccountUpdates/applyReassignment copy the slice, so the previous state is never mutated.
- Multiple updates for one key or one name in a block apply deterministically in block order; the first claimant in block order wins a contested free name.
- Evidence verification (same validator string, same h/r/type, different digests, both signatures valid) and the evidence age window are enforced in both paths.
- MaxAccountNameLength (32) keeps names distinct from canonical 44-character keys.

### governance
- Governance sequence correctly blocks the within-chain value-cycle replay (A→B→A then replay A→B stays inert) — ledger TestReassignmentSequenceGuardBlocksCycleReplay and node TestChaosGovernanceCycleReplayStaysInert both pass; GOV-6 is specifically the cross-re-genesis reset the sequence cannot cover
- At-most-one-governance-action-per-block is enforced: ProposeBlock prefers the parameter change, and ApplyBlock (block.go:211) rejects a block carrying both a ParameterChange and a Reassignment (they would bind the same sequence)
- ParameterChange.Valid / Reassignment.Valid correctly require each vote to be a DISTINCT CURRENT validator (seen map + Contains) and combined power >= Quorum (strict >⅔); unknown-validator and double-counted votes are rejected
- The non-negative reassignment guard holds (NewAmount.Sign()<0 rejected) and nil validator set / nil amounts are rejected
- Every governed field is under both the committed digest and the vote preimage: LedgerParameters.fields() covers TransactionCost, BlockIntervalMs, StepTimeoutMs, MaxPeers, MaxMessageBytes, and LedgerState.Digest folds in the GovernanceSequence — no governed field is omitted from the digest
- proposalKey/reassignmentKey key the FULL proposed profile + previous digest + sequence (the C5 fix), so two proposals sharing only a cost pool separately; pendingParameterChange/pendingReassignment scan sorted keys (deterministic)
- MinMessageBytes floor is enforced in Acceptable and at config load, so governance cannot vote the message cap below a payable size (the one timing/size field that IS brick-proofed — unlike the interval/timeout of GOV-2)
- Negative transaction cost is refused at config load (config.go:88) and clamped to zero in ProcessPayments, and Acceptable rejects it — the C3 mint class stays closed
- Reassignment vote preimage binds the raw account key (not a name), so a dishonest node cannot retarget a reassignment at vote-collection time; the ⅔ quorum only forms if voters independently signed the same key+amounts
- ApplyBlock re-verifies every governance certificate against the validator set independently of ProposeBlock, and recomputes the result digest — a proposer cannot smuggle an unratified change past honest nodes

### sync-trust
- All three adoption paths (adopt node.go:327, catchUp node.go:357, loadSnapshot persist.go:16) call verifyCheckpoint, which requires a real >2/3 Commit.Verify of the signing set before fastForward — no path adopts state without the quorum it claims (brief point 6).
- verifyCheckpoint binds the commit to the FULL state: LedgerState{Accounts,Parameters,GovernanceSequence}.Digest() == cp.Commit.BlockDigest (node.go:392) — tampered/truncated accounts, params or sequence are rejected.
- Height monotonicity enforced on every path: adopt/catchUp/loadSnapshot all return early when cp.Commit.Height+1 <= n.height (node.go:331, 361; persist.go:28) — a stale checkpoint cannot roll a node back.
- Cross-network snapshots/checkpoints are rejected: commit votes are verified with cfg.NetworkID and the network is bound into each vote's signing preimage, so votes signed for another network fail Verify.
- Going-forward validator set is re-derived from verified state (startHeight -> validatorsFor(n.accounts), node.go:472); cp.Validators is used only to verify the adopting commit, not kept as authority.
- Snapshot write is crash-safe (writeAtomic fsyncs temp then dir, persist.go:73-96); a parse failure or verify failure on load is ignored and the node starts at genesis rather than adopting garbage.
- BackupPath mirror writes the identical verified Checkpoint JSON; on load it goes through loadSnapshot's certificate verification like any snapshot — the backup is not a separate trust bypass.
- Peer DNS uses only CURRENT validators' published addresses (resolvedPeerAddresses, peerdns.go:30 filters currentValidators.Contains) — a flood of non-validator accounts claiming addresses cannot influence peer selection or eclipse a node.
- Topology neighbor selection (Neighbors/toCoord/fromCoord) is deterministic over sorted validator keys; eclipse would require validator-level stake AND key-grinding into a victim's Hamming-graph positions, and relay is trusted only to relay (signatures protect safety), so the damage ceiling is censorship/liveness, not fund theft.
- cmd/server/config.go load() validates key lengths, non-negative cost, and genesis Acceptable() (C3/C4 fixes intact); bootstrap/snapshot/backup paths are plumbed through unchanged.
- The >1/3 anchor-overlap arithmetic (vouched*3 > trusted.TotalPower, node.go:400) is correct for the BFT skipping rule and does not overflow for any realistic set.

### node-input
- handleGossipPayment (httptransport.go:156) verifies the payer signature before AdmitPayment — the finding-#9/mempool-flood gate is intact; junk payments are not admitted or relayed
- handleAccountUpdate (httptransport.go:175) verifies the owner signature before SubmitUpdate/relay; evUpdate also enforces WithinMessageBound before relay (node.go:286)
- Payments/updates/governance votes relay ONLY on successful verified+deduped+bounded admission (mem.add / updates.add / addParameterVotes returning true) — votes (NODE-IN-2) are the sole unconditional-relay path
- onProposal drops proposals whose Height != n.height and verifies p.Proposer==driver.Proposer(round) and the proposer signature before relay/ApplyBlock (node.go:494-509); the held-duplicate fast path never relays (swapped-body guard)
- ReceiveVote bounds rounds to d.round+maxRoundLookahead (256) and drops wrong-height/negative-round votes (driver.go:213-218), so per-round VoteSet allocation is bounded
- Checkpoint intake (bestCheckpoint) quorum-verifies off-loop in a goroutine before enqueuing evAdopt/evCatchUp; verifyCheckpoint re-verifies against live state, so a malicious peer's junk checkpoint cannot drive loop work on attacker terms
- mempool and updatePool are size-bounded (MaxMempool) and content-deduped by PaymentID/UpdateID; dropBelow/dropOverdrawn prune stale and newly-overdrawing entries each commit
- blocks/heightProposals/relaySeen/pendingSlashes maps are reset or pruned each commit and keyed to bound growth across heights (node.go:650-669); only heightVotes (NODE-IN-2) lacks a cap
- cmd/server/main.go sets ReadHeaderTimeout/ReadTimeout/WriteTimeout/IdleTimeout (C7), bounding handler-goroutine pile-up behind a saturated loop
- decode() enforces POST and a 1 MiB MaxBytesReader on all mutating endpoints; handleResolve enforces GET and the raw-'+' key fix
- Static and dynamic validator modes both route power through NewValidatorSet saturation (MaxValidatorPower) and validatorsFor; no nil-set panic on the dynamic path (C1 fix confirmed via validatorsLabel)

### wallet-ui
- Template escaping: ui/main.go imports html/template and uses template.Must(ParseFS) (main.go:16,26), so on-chain names in wallet.html/admin.html get contextual auto-escaping; Description and Comment are never rendered in any template (grep confirms only .Name/.PublicKey/.Amount and numeric governance fields appear). No text/template anywhere. A malicious node's reflected error text lands only in escaped HTML-text context (<p class=err>{{.Error}}</p>).
- Receipt verification (wallet.go:242-245) correctly requires node signature valid AND PaymentID==PaymentID(p) AND node ∈ validator set; the four security_test.go cases (validator, non-validator, wrong-payment, bad-sig) pass. Fix for #28 holds.
- PBKDF2-HMAC-SHA256 (keyfile.go:183-207) matches crypto/pbkdf2 (stdlib) on 300 randomized inputs including passwords > 64-byte HMAC block size, empty password/salt, multi-block output (keyLen up to 100 > hashLen) and at the production 600k-iteration/32-byte setting — the hand-rolled KDF is correct beyond the four shipped vectors (poc_pbkdf2_crosscheck_test.go, PASS).
- Salt (16B) and GCM nonce (12B) are freshly crypto/rand per seal (keyfile.go:124-135); no fixed/reused IV; iterations stored per-file so they can be raised. open() returns a uniform wrong-passphrase-or-corrupt error and never yields garbage key bytes (AEAD-authenticated); confirmed by TestSealedKeyRoundTrip.
- GET endpoints have no side effects: / (index) and /admin only render; /send,/register,/admin/* redirect to GET on non-POST. The guard is POST-only, which is correct given no mutating GETs.
- Listen address defaults to 127.0.0.1:8090 and is passed straight to ListenAndServe; it can only become 0.0.0.0 by explicit operator -listen flag (documented 'keep it local'). Not a code-chosen exposure, but note the no-header guard fallback means any client reaching the port needs no browser headers — see KEY-1 fix.
- Payment construction: recipient is signed as a NAME on-chain for pay-by-name (wallet.go:216-225) and the network resolves it, so a malicious node cannot remap a payment recipient (the third-pass fix holds for payments — contrast KEY-4 for reassignments). looksLikePublicKey trims and requires exactly 32 decoded bytes.
- Mutating routes /send,/register,/admin/parameter,/admin/reassignment are all wrapped in guard() (main.go:48-51), and TestMutatingHandlersAreGuarded confirms a cross-site POST is blocked before the nil wallet is touched. The classic cross-site/same-site/foreign-Origin cases are blocked (it is only the rebind/same-origin-after-rebind and no-header paths that slip through — KEY-1).

### flake-suite
- Full suite without -short ran twice under -race with GOLEDGER_CHAOS_SLACK=4 (suite-run1.log, suite-run2.log): all packages ok, no data races, no fork/supply-cap violations from the chaos checkers. Seeds logged per scenario in both runs.
- ui module under -race (go test -C ui -race ./...): ok, 17 tests pass, no data races.
- The replay test's REPLAY assertion (resubmitting a past-block payment) is sound — mempool.dropBelow + the BlockID==height inclusion rule correctly make a cleared payment non-re-includable; the flake is entirely in reaching the first commit, not in replay.
- Safety held throughout: every slow/failing run of the flaky test still committed one digest per height and conserved supply; the bug is liveness-only (the chaos fork/supply checkers were green across both -race suite runs).
- The 2026-07-31 held-proposal re-relay fix (findings.md) is present and correct for its stated case (a held duplicate is not re-relayed); LIVE-3 is a different, first-sight path.
- Prototype fix (height-scoped timeouts + window hold-and-replay) passes the full node package under -race with GOLEDGER_CHAOS_SLACK=4 and all chaos scenarios (flake-suite-fix), and the flaky test ran 30x short + 15x short post-fix with max 1.94s and zero >2s outliers.
- ProposerAt is correctly deterministic and stake-weighted (verified the FNV seeding over many heights); with 3 equal validators consecutive heights share a round-0 proposer ~86% of the time, which selects the 'held' variant of LIVE-1 — not itself a bug, but it governs which LIVE-1 path fires.
