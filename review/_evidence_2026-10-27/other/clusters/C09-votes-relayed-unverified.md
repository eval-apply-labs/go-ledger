# C09-votes-relayed-unverified

Round-1 reports merged into this cluster (2):

## [consensus-core] DOS-1 — Unauthenticated vote intake + unbounded heightVotes + wholesale re-gossip = amplifying DoS
- severity/confidence (round 1): medium/high
- category: dos
- locations: node/httptransport.go:139-146, node/node.go:268-273, node/node.go:548-553, node/node.go:562-570, node/node.go:108
- attacker: anyone reaching the port (non-validator); worse with a stolen validator key, which also forces per-vote signature verification on every node each tick
- known_status: New angle on deferred #10 (unauthenticated write API / DoS). Materially worse: heightVotes is unbounded (no mempool-style cap) and the vote-re-gossip loop (a post-review liveness fix) amplifies one poster into a whole-network flood; the vote path is also the only gossip intake still lacking the ingress verification the gossip-payment fix added.

**Description.** handleVote decodes a Vote and calls SubmitVote with NO verification — unlike handlePay/handleGossipPayment/handleAccountUpdate, which VerifyPayment/VerifyAccountUpdate at ingress. In evVote the driver's ReceiveVote result is discarded, then rememberVote stores the vote keyed by validator+signature if v.Height==n.height (the ONLY thing checked — no signature, no validator-set membership, no round bound), and relay() re-broadcasts it. heightVotes has no cap (contrast mempool.maxSize). reGossipVotes then re-broadcasts the ENTIRE map to every peer each tick. So a non-validator who can POST /hdls/vote grows a node's memory without bound and makes every node flood the network with arbitrary junk every tick.

**Failure scenario.** Attacker POSTs N distinct junk Vote JSONs (Height=current, junk Validator/Signature, absurd Round) to /hdls/vote. Each is stored in heightVotes (no cap) and relayed. Every reGossipVotes tick re-broadcasts all N to every peer, which store+relay+re-gossip them too — N×peers broadcasts network-wide per tick, unbounded N. PoC stored 5000 junk votes and re-broadcast all 5000 in one tick.

**PoC.** <scratch>/consensus-core/node/poc_consensus_core_test.go :: TestPoCUnverifiedVotesStoredUnboundedAndReGossiped (PASS — confirms 5000 unverified votes stored, 5000 re-broadcast in one tick)

**Suggested fix.** Verify at vote ingress exactly like payments: check signature, validator-set membership, height, and round≤current+maxRoundLookahead BEFORE rememberVote/relay; store/re-gossip only accepted votes; cap heightVotes.

## [node-input] NODE-IN-2 — Consensus votes are relayed and stored (heightVotes) before any signature/validator check — junk amplification + unbounded map
- severity/confidence (round 1): medium/high
- category: dos
- locations: node/node.go:268-273, node/node.go:548-553, node/node.go:562-570, node/relay.go:13-26, node/httptransport.go:139-146
- attacker: anyone reaching the port
- known_status: New. Within deferred #10's area but a distinct code defect: votes are the ONLY intake path that relays/persists before verifying (payments/updates/governance votes all verify first), and heightVotes is the only intake-fed map with no size bound. A front-door rate limiter does not stop the 1->P amplification or the per-tick re-gossip of each admitted junk vote.

**Description.** handleVote (httptransport.go:139) decodes a Vote and calls SubmitVote with no verification — unlike handleGossipPayment (:156) and handleAccountUpdate (:175), which verify signatures first. In evVote (node.go:268) the node unconditionally rememberVote()s the vote into heightVotes (node.go:552, keyed by attacker-controlled Validator+Signature, NO size cap — the mempool has one, this map does not) and relay()s it to every peer (node.go:273). The driver's VoteSet.Add verifies only for TALLYING and silently drops non-validators/bad signatures, but relay and heightVotes storage happen regardless. So the 'verify before acting' rule the brief requires is violated for the relay/remember actions. reGossipVotes (node.go:562) then re-broadcasts the entire heightVotes set to all peers every timeout tick.

**Failure scenario.** Attacker reads the live height from GET /hdls/status, then floods POST /hdls/vote with distinct junk Votes (Height=current, random Validator/Signature strings). Each is relayed once to all P peers (1->P amplification) and stored in heightVotes; the uncapped map grows for the whole commit window, and every re-gossip tick re-floods the entire junk set to all peers. If the loop is also stalled (e.g. via NODE-IN-1) the map never resets and grows until OOM. Non-validator, no key, no stake required.

**PoC.** <scratch>/node-input/node/zz_poc_test.go (TestPoCUnauthenticatedVoteFloodAmplifiesAndGrows) — PASS: 5000 unverified junk votes, all relayed, all stored in heightVotes, all re-gossiped per tick

**Suggested fix.** In evVote, only rememberVote/relay a vote that passes v.Verify(scheme,networkID) AND heightVals.Contains(v.Validator) (cheap: ~50us Ed25519), mirroring the payment/update handlers. Add a size cap to heightVotes (validator count is the natural bound once membership is checked).
