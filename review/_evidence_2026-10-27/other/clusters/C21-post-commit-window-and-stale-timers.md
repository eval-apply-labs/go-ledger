# C21-post-commit-window-and-stale-timers

Round-1 reports merged into this cluster (3):

## [flake-suite] LIVE-1 — Block proposal arriving in the post-commit window is dropped and the height's first round is lost
- severity/confidence (round 1): medium/high
- category: liveness
- locations: node/node.go:495, node/node.go:466, node/node.go:656, node/node.go:587, node/sync.go:67
- attacker: no attacker (bug; normal operation, no faults needed)
- known_status: new — not in findings.md/TODO.md. Distinct from the 2026-07-31 'duplicate proposal re-relayed unverified' fix and from the vote/proposal re-gossip work (TODO 'Vote re-gossip'), which masks this by eventually re-sending; the first attempt is still always lost in the window.

**Description.** commit() does n.height++ then schedules startHeight(H+1) one MinBlockInterval later (node.go:656,678). In that window the new driver does not exist yet, but n.driver still points at the decided height-H driver. onProposal (node.go:495) requires n.driver!=nil and checks p.Proposer against n.driver.Proposer(round), which uses the OLD height; for H+1's proposal that is the wrong proposer (or, when consecutive proposers coincide, it is fed to the decided driver and then dedups every genuine copy). Either way the proposal is dropped, not relayed, and never re-processed. startHeight(H+1) builds a fresh driver with no proposal. The only recovery is the proposer's reGossipProposal every cfg.Timeout (sync.go:67). When >=2 of 4 nodes hit the window the height needs no quorum and stalls ~one step-timeout.

**Failure scenario.** 4 equal validators, localhost, no loss. N commits H at t, sets height=H+1, schedules startHeight at t+300ms. H+1's proposer broadcasts at ~its own t+300ms; the copy lands on N at ~t+312ms, before N's startHeight runs. onProposal checks it against the height-H schedule -> wrong proposer -> dropped, not relayed. N then starts H+1 with no proposal and cannot prevote. If a second node also dropped it, <3 nodes hold the proposal, so round 0 cannot reach quorum; every node waits ~cfg.Timeout for reGossipProposal, then commits at round 0/1 that many seconds late. Enough such heights before block 5 blow the test's 20s deadline (observed 5.6/10.6/15.9/20.0s).

**PoC.** <scratch>/flake-suite/node/flake_poc_test.go (TestPoCDeadWindowProposalDropped, TestPoCDeadWindowProposalHeldNeverProcessed — both FAIL, print the dropped/held proposal) and flake-suite/node/flake_throughput_poc_test.go (TestPoCThroughput3Validators/4Validators — FAIL: ~17-31 of ~104 ideal heights, ~8s/12s stalled). Prototype fix in /tmp/.../scratchpad/flake-suite-fix makes both PASS (105-111/104) and keeps the whole -race suite green.

**Suggested fix.** In commit() set n.driver=nil after the height advances, and in onProposal handle n.driver==nil during the window: authenticate against the NEW height's schedule (validatorsFor(n.accounts).ProposerAt(p.Height,p.Round)) and buffer the proposal in heightProposals; then have startHeight replay the buffered proposals (and heightVotes) into the freshly-built driver. See flake-suite-fix/node/node.go.

## [flake-suite] LIVE-2 — Consensus timeouts are not height-scoped: a stale timer fires into a later height's driver
- severity/confidence (round 1): low/high
- category: liveness
- locations: node/node.go:599, node/node.go:274, node/node.go:602
- attacker: no attacker (bug)
- known_status: new — not recorded in findings.md/TODO.md.

**Description.** arm() (node.go:599) posts event{kind:evTimeout, step, round} carrying no height, and the handler (node.go:274) feeds it to whatever n.driver currently is as long as n.driver!=nil. The driver only guards round!=d.round (driver.go:233), and round numbers restart each height, so a propose/prevote timer armed at height H (e.g. round 0) fires into height H+1's brand-new driver (also round 0) after the height advanced. The node then prevotes nil far earlier than its own H+1 propose timeout is due, and rejects a valid, timely H+1 proposal that arrives within the intended window. Impact is bounded (one burned round, self-healing) but it compounds LIVE-1 and independently costs rounds whenever fan-out latency is a meaningful fraction of the step timeout — the documented multi-hop / slow-LAN case.

**Failure scenario.** cfg.Timeout=1s, MinBlockInterval small. Node arms (propose, round 0) for height H at t (due t+1s). Height H commits and H+1 starts at t+0.68s. At t+1s the height-H timer fires; the handler feeds it to the height-H+1 driver (round 0, propose step) and the node prevotes NIL, though its own height-H+1 propose timeout is not due until ~t+1.68s. A valid height-H+1 proposal delivered at ~t+1.1s (0.42s into a 1s budget) is then refused, burning round 0.

**PoC.** <scratch>/flake-suite/node/flake_poc_test.go (TestPoCStaleTimeoutFiresIntoLaterHeight — FAIL, prints the premature nil prevote) and flake-suite/node/flake_staletimer_poc_test.go (TestPoCStaleTimersUnderSlowFanout — measurement: pristine ~30 heights with round>=1 occurrences vs ~55 clean under the full fix). Height-scoping the timer (flake-suite-fix) removes the premature prevote.

**Suggested fix.** Tag each armed timer with the current height (arm captures n.height) and drop the evTimeout in handle() unless e.height==n.height, as the catch-up/commit guards already do for messages.

## [flake-suite] DOC-1 — Docs claim healthy-network cadence and self-heal that the proposal-window drop contradicts
- severity/confidence (round 1): low/high
- category: docs-drift
- locations: docs/consensus.md:58, docs/governance.md:118, docs/security.md:106, README.md:215
- attacker: no attacker (documentation vs behaviour)
- known_status: new — the docs do not mention the post-commit-window drop; this surfaces with LIVE-1.

**Description.** docs/consensus.md and docs/governance.md describe timeouts as a fallback for an unreliable network ('under an unreliable network the rounds eventually stretch past message latency'), implying a healthy network commits at the block-interval cadence. In fact LIVE-1 makes an all-honest, zero-loss network lose round 0 of most heights to the post-commit-window drop and wait ~one step-timeout (default 3s) for reGossipProposal on the majority of blocks — a ~4x throughput shortfall with no faults. security.md:106 frames periodic re-gossip purely as partition self-heal; it is in fact the routine recovery path that masks the window bug every height. The claims are not false in principle but materially overstate steady-state liveness; worth correcting alongside the LIVE-1 fix (after which the cadence claim holds).

**Failure scenario.** An operator reads docs/governance.md's timing-profile section, sets minBlockIntervalMs=1000/timeoutMs=3000 as documented, and expects ~1 block/s on a healthy LAN. The network instead produces roughly one block per ~3s because nearly every height burns round 0 in the window and waits for reGossipProposal — contradicting the documented expectation, with no fault present.

**PoC.** <scratch>/flake-suite/node/flake_throughput_poc_test.go (ideal vs actual height counts quantify the cadence gap).

**Suggested fix.** Either fix LIVE-1 (then the cadence claim is accurate) or note in docs/consensus.md and docs/operating-a-network.md that current steady-state block time is governed by the step timeout, not the block interval, until the window drop is addressed.
