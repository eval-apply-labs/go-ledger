# R3-dos — new findings from the second pass (not yet independently verified)

## NF1-proposal-flood-unbounded (found by verifier group 'proposals-liveness') — Proposal intake has no per-round dedup before ApplyBlock/store/relay and no round bound: one validator key can freeze every node's event loop and grow memory
- severity/confidence claimed: high/high
- locations: node/node.go:498-516, node/node.go:511, consensus/driver.go:201-207, node/relay.go:28-29, ledger/block.go:177-182
- attacker: Any single validator, or anyone who stole one validator key. Any power above 0, any time during a height, from any LAN host.

**Description.** onProposal drops a held round's proposal only when the digest matches. It never bounds p.Round. Driver.ReceiveProposal calls ensureRound with no maxRoundLookahead check (votes have one). So the scheduled proposer of ANY round of the current height, including round 100000 (every validator with power above 0 owns about its stake share of rounds), can submit any number of distinct signed proposals. Every node runs ApplyBlock on each one on its single event loop. If PrevStateDigest is set correctly, ValidPayments verifies every junk payment signature, about 0.33s per 1 MiB body. Each node also keeps the block in n.blocks until commit and relays it once, keyed by digest. This goes beyond deferred #10: the sender is an allowlisted validator and honest nodes relay its messages, so a front-door allowlist or rate limit does not stop it.

**Failure scenario.** Node level: 59 signed junk proposals of about 1 MiB each (20 digests for one scheduled round, plus scheduled rounds up to 100077) took 19.6s of one node's event loop. All were relayed, 60 blocks were retained (56 MiB), and heightProposals held round 100077. Network level: in a 4-validator cluster, one key POSTing 8 junk proposals/s to the 3 honest nodes cut the chain from about 14 heights/s to 2 heights in 6s; it recovered after the attack stopped. Meanwhile the ⅔ governance vote that would remove the attacker cannot be committed, and memory grows about 1 MiB per junk proposal per node.

**PoC.** <scratch>/verify-proposals-liveness/node/verify_pl_poc_test.go::TestVPLProposalFloodUnboundedCPUAndMemory and ::TestVPLNetworkFreezeByOneValidator (run with VPL_TICK_MS=125 GOLEDGER_CHAOS_SLACK=4).

**Suggested fix.** In onProposal, before ApplyBlock: return false if heightProposals[p.Round] is held (any digest; first wins, as in the driver), and if p.Round<0 or p.Round>n.driver.Round()+1. Apply the same bound in the C21 window path. This is node-local and can be rolled out node by node. C15's block-size and count cap bounds the cost of the one allowed proposal per round. The prototype (<scratch>/verify-pl-logs/combined-proto.diff) dropped the PoC to 0 relayed and 0 retained; the full -race suite and 3x chaos runs (partition heal, lost proposal, catch-up) stay green.

## NEW-4-unbounded-proposal-rounds (found by verifier group 'completeness-critic-pass2') — Proposals are not bounded by maxRoundLookahead: one validator floods a single height with far-future-round proposals, growing node maps and making every later vote O(rounds)
- severity/confidence claimed: medium/high
- locations: node/node.go:494-517 (onProposal accepts any round for which p.Proposer==driver.Proposer(round); no lookahead bound), consensus/driver.go:201-207 (ReceiveProposal ensureRound for any round), consensus/driver.go:216-218 (ReceiveVote DOES bound rounds to d.round+256 — the asymmetry), consensus/driver.go:303-329 (step1 scans sortedPrecommitRounds and all prevote rounds every evaluate), node/node.go:498-512,511 (each accepted proposal is stored in heightProposals and n.blocks and relayed)
- attacker: A single validator (needs a validator key to be an authentic proposer; below 1/3).

**Description.** ReceiveVote rejects rounds beyond d.round+maxRoundLookahead (256), but onProposal/ReceiveProposal apply no such bound. ProposerAt is defined for every int32 round, and a single validator is the scheduled proposer for ~1/N of all rounds, so an attacker-validator can send authentic (correctly-signed, correct-proposer) proposals for ~2^31/N distinct rounds WITHIN one height. Each is stored in heightProposals and n.blocks (bodies up to ~1 MiB each over HTTP), relayed to peers (distinct relay keys, so no dedup), and allocates per-round VoteSets on first vote; every subsequent evaluate() pays an O(rounds) scan. These maps are reset at commit, but the height may not commit while under attack (and other validators are dragged along via relay).

**Failure scenario.** 4 validators, one Byzantine. Hand-driven PoC: 20000 proposals for rounds >= 1000 (far beyond d.round+256) are all accepted, stored and marked for relay within height 1; n.blocks grows to 20001, live heap +23 MiB with a tiny 200-payment body, and one honest peer vote goes from 0.65ms to 21ms (O(rounds) evaluate). Scaling the body toward 1 MiB and the round count up exhausts memory / stalls the event loop network-wide via relay.

**PoC.** .../scratchpad/verify-critic/node/poc_critic_rounds_test.go::TestPoCCriticProposalRoundsUnbounded -> FAIL (20000 far-future-round proposals accepted/stored/relayed; per-vote cost 0.65ms->21ms).

**Suggested fix.** Apply the same maxRoundLookahead bound to onProposal/ReceiveProposal that ReceiveVote already uses (drop proposals with Round > d.round+256 or < 0), and cap heightProposals/n.blocks entries per height.

## BE-NEW-2 (found by verifier group 'blocks-economics') — The account-update pool can be filled permanently by a key with no account (far-future BlockID); honest name registrations, delegations and address updates get 202 and are silently dropped network-wide
- severity/confidence claimed: medium/high
- locations: node/node.go:283-288, node/updatepool.go:20-31, node/updatepool.go:38-48, node/httptransport.go:170-181, node/node.go:138
- attacker: Anyone who can reach any node's /hdls/update; no account, anonymous key.

**Description.** evUpdate checks only the size bound. updatePool.add caps at MaxMempool (default 10000) with no BlockID ceiling, no account check and no fee. dropBelow prunes only past heights. handleAccountUpdate returns 202 before admission, so the caller never learns of a refusal. A key with no account fills every node's pool through one node via relay, permanently. There is no governance remedy, because there is no balance to zero; only restarting every node clears it, and the attacker can refill. This is distinct from C10 (governance pools) and from C16 (payment mempool).

**Failure scenario.** On a 4-node HTTP network with the pool cap at 60, a ghost key sends 60 updates naming BlockID 2^40 to node0. An honest owner's name claim sent to node2 afterwards gets 202 and never lands; the control without the fill lands. At the default cap the attack takes 10000 small requests.

**PoC.** <scratch>/verify-blocks-economics/node/zz_verify_c16_test.go TestVerifyUpdatePoolPermanentFillByGhostKey (FAIL), TestVerifyUpdatePoolControlNoFill (PASS).

**Suggested fix.** At update intake, require BlockID in [height, height+W] and require From to exist in committed state. Carry and accept only updates from existing accounts in ProposeBlock and ApplyBlock (shared with the C15/C16 fixes). Ideally give update intake a verdict instead of an unconditional 202.

## NEW-5-updatepool-squat-accountless (found by verifier group 'completeness-critic-pass2') — The account-update pool admits fee-free, far-future updates from a key with no account and evicts only past blocks, so anyone reachable can fill it and block all honest registrations/re-delegations/address updates
- severity/confidence claimed: medium/high
- locations: node/node.go:283-288 (evUpdate: admits any update that verifies its own signature and fits the message bound — no account/fee/BlockID-ceiling check), node/updatepool.go:130-141 (add: bounded by maxMempool, content-dedup only), node/updatepool.go:147-157 (dropBelow evicts only BlockID < height), node/httptransport.go:170-181 (handleAccountUpdate verifies only the self-signature, then SubmitUpdate)
- attacker: Anyone who can reach the node's HTTP port (no account, no validator key) — the documented LAN trust boundary.

**Description.** An AccountUpdate is admitted and gossiped if it carries a valid signature by its own From key and fits MaxMessageBytes. It need not correspond to any existing account, carries no fee, and has no BlockID upper bound; the pool (default maxMempool 10000) is pruned only for BlockID < height. A fresh keypair with no account on the ledger can sign maxMempool distinct updates naming a far-future block; they fill the pool, survive every commit, and from then on every honest update (name registration, re-delegation, the peer-DNS address publication of NEW-3) is refused at the full pool and never pools, gossips, or enacts. Mirrors the governance-pool-squat (C10) but on the update pool, which round 1 recorded as 'size-bounded and content-deduped' (sound) without noting the accountless/far-future/no-fee fill.

**Failure scenario.** PoC with a 50-slot pool (stands for the 10000 default): a stranger key with NO account signs 50 updates naming block 2^40+i; across 9 commits they are never evicted and alice's valid name registration is refused (control with 0 squatters: alice's registration commits).

**PoC.** .../scratchpad/verify-critic/node/poc_critic_updatepool_test.go::TestPoCCriticUpdatePoolSquatByAccountlessKey -> FAIL; control TestPoCCriticUpdatePoolControl -> PASS.

**Suggested fix.** At update intake require From to be an existing account and bound BlockID to height+window; evict pooled updates for accounts that no longer exist or whose block window has passed. (Same root cause as C16's 'bound BlockID at intake' and C15's no-fee update fill — fixing those should cover this pool too.)

## VBS-N1 (found by verifier group 'burn-slash') — Driver live-locks the node's event loop when a vote signature fails
- severity/confidence claimed: medium/high
- locations: consensus/driver.go:253-262, consensus/driver.go:282-292, consensus/driver.go:335, consensus/driver.go:364
- attacker: No attacker needed (a fault). Anyone able to disrupt the node-to-signer link can trigger it.

**Description.** When SignVote fails, castVote returns nil without setting sentPrevote or sentPrecommit. The step1 branches that call it (the prevote-on-proposal branch and the nil-precommit branch) still return progressed=true. evaluate() loops while progressed is true, so the same branch fires forever. Driver.Start and ReceiveVote run on the node's single event-loop goroutine, so the whole node hangs. Today this is unreachable: the key is in-memory Ed25519 and its length is checked at config load. It becomes reachable as soon as signing can fail — with the planned separate signer (network error, timeout, signer down) or with the double-sign refusal proposed as H1's fix.

**Failure scenario.** In a separate-signer deployment, the signer is briefly unreachable or refuses at the moment this node is the round's proposer. startHeight calls Driver.Start, evaluate() spins, and the event loop freezes: no votes, no loop-backed HTTP, and the network halts if this node's power is needed. With a persisted refusal guard, every restart hangs again at the same height, and catch-up cannot run because it also lives on the frozen loop.

**PoC.** <scratch>/verify-burn-slash/consensus/zz_vbs_signfail_test.go TestVBSDriverSpinsWhenSigningFails: with a proposer whose signer fails, Driver.Start() never returns (3s timeout).

**Suggested fix.** On a signing error, castVote must not report progress: make step1 return progressed=false, or record the attempt and emit an ActSignFailed so the node retries on its next timer or event. About 5 lines.

## NEW-6-backup-goroutine-leak (found by verifier group 'completeness-critic-pass2') — saveSnapshot spawns one untracked backup goroutine per commit; on a hung BackupPath mount they accumulate one blocked OS thread per block until the process aborts
- severity/confidence claimed: medium/high
- locations: node/persist.go:280-290 (go writeAtomic(...) per commit, deliberately untracked, may outlive Stop()), node/persist.go:297-319 (writeAtomic blocks in OpenFile/Write/Sync on the mount), node/node.go:655 (saveSnapshot called every commit)
- attacker: None (operational: a backup mount that hangs rather than errors — exactly the NFS/CIFS case the docs steer away from but still support).

**Description.** When BackupPath is set, each commit launches a detached goroutine to mirror the checkpoint. If the backup target blocks (a stuck NFS/CIFS mount: open/write/fsync never return), the goroutine parks in a blocking syscall, which pins an OS thread AND holds a full copy of the checkpoint. There is no bound, no in-flight guard, and the goroutines are explicitly untracked, so they accumulate one per block. Go aborts the process once runtime threads hit 10000, so a long-enough stall on a mount the operator was told to use for durability crashes the validator. The operator guide does warn to prefer a local disk and that a failed mirror is 'silently ignored', which it is for errors — but a HANG is not an error and is not mentioned.

**Failure scenario.** PoC: BackupPath points at a directory where the per-height temp path is a FIFO with no reader (models a hung mount). After ~150 commits the node has ~205 goroutines (was 2) and ~221 OS threads (was 6) — one per commit — while consensus keeps committing. Extrapolated, the process aborts at 10000 threads.

**PoC.** .../scratchpad/verify-critic/node/poc_critic_backup_test.go::TestPoCCriticBackupGoroutinesAccumulateOnHungMount -> FAIL (goroutines 2->205, OS threads 6->221 over 153 commits).

**Suggested fix.** Serialise backup writes through a single worker with a depth-1 queue (drop/coalesce if the previous write is still in flight) and a write deadline/context, so a hung mount costs at most one blocked goroutine, not one per block.

## NF-C10-single-validator-squat (found by verifier group 'unauth-amplification') — Proposed C10 fix (pool only after >=1 verified vote) still lets ONE current validator permanently freeze governance with self-signed junk
- severity/confidence claimed: medium/medium
- locations: node/governance.go:47, node/reassignment.go:49
- attacker: a single current validator (<=1/3 stake), known-identity but Byzantine

**Description.** The round-1 C10 fix 'do not pool until at least one carried vote verifies as a current validator' stops the unauthenticated squat but not a single current validator. That validator can sign 64 distinct Acceptable parameter proposals (e.g. varying BlockIntervalMs) and 64 distinct reassignments, each carrying its own one valid vote, filling both 64-slot pools with entries that can never reach quorum. The entries bind the current sequence/digest so they survive pruning, and the sequence cannot advance, so governance (the owner's theft-recovery mechanism) is permanently frozen by one 'bank'.

**Failure scenario.** A validator holding a small stake (<1/3) self-signs 128 junk proposals; both pools fill with its own validly-signed-but-never-quorum entries; every legitimate fee change, freeze or claw-back vote is refused at the >=64 cap. Governance is frozen and cannot even vote to replace/slash the offender.

**PoC.** Not separately reproduced; follows directly from the >=64 hard cap plus the >=1-verified-vote insertion rule. The existing TestPoCGovernancePoolFillBlocksLegitVotes shows the cap wall; substituting 64 self-signed single-vote proposals for the voteless ones fills it identically post-fix.

**Suggested fix.** Add a small per-proposer proposal cap (a validator may hold only k open proposals in a pool) OR evict the lowest-voted-power proposal when a genuine higher-power proposal arrives; prefer eviction-by-progress over a flat cap so a near-quorum real proposal always displaces junk.

## BE-NEW-1 (found by verifier group 'blocks-economics') — Pending payments with coprime denominators make every commit (dropOverdrawn) and every admission (outgoingFor) super-linear on every node, permanently and with no fee when the BlockID is far in the future
- severity/confidence claimed: high/high
- locations: node/mempool.go:51-59, node/mempool.go:67-89, node/node.go:432-456, node/node.go:657-662
- attacker: Any account holder (1 unit).

**Description.** The overdraw guard re-sums the payer's pending amounts with big.Rat on every admission (outgoingFor) and after every commit (dropOverdrawn). big.Rat.Add normalises via GCD. With pairwise-coprime denominators of about 783 digits, the running sum's denominator is their LCM, so one sum costs roughly O(k^3) in the number of pending payments. An amount of 1/d_i is below the cost (still admitted today), fits in 2000 B, and the whole set totals about 0. With BlockID 2^40 the payments are never included, never charged and never pruned.

**Failure scenario.** A 1-unit payer POSTs 140 such payments to /hdls/pay. All are receipted, at a cost of 2m50s of event-loop time. A single validator's throughput then drops from 495.5 to 0.43 blocks/s and stays there; the payer's balance is untouched. Per-admission cost is 0.94 s at 100 pending and 3.5 s at 160 (roughly cubic). Gossip makes every node do the same work.

**PoC.** <scratch>/verify-blocks-economics/node/zz_verify_new_dropoverdrawn_test.go TestVerifyNewFarFutureCoprimePendingSlowsEveryCommit (FAIL); the cost curve is in node/zz_verify_c08_test.go TestVerifyC08IntakeCostOfPendingCoprimeAmounts.

**Suggested fix.** C08's fixed money unit checked at intake closes it. C16's BlockID window and below-cost refusal also bound how long such payments stay and make each one cost a fee. Today a 2/3 reassignment that zeroes the payer evicts them, cheaply.
