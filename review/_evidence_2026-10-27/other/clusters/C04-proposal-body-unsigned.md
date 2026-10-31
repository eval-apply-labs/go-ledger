# C04-proposal-body-unsigned

Round-1 reports merged into this cluster (1):

## [flake-suite] LIVE-3 — Swapped-body proposal under a genuine signature poisons a round (signature does not cover the block body)
- severity/confidence (round 1): medium/high
- category: liveness
- locations: node/messages.go:155, node/node.go:498, node/node.go:507, node/node.go:510, node/relay.go:29
- attacker: anyone reaching the port / a gossip peer (no key, no validator slot)
- known_status: new angle on a findings.md item recorded as FIXED. The 2026-07-31 RC review fixed 'a duplicate proposal re-relayed unverified' by making the held-proposal fast path never relay a duplicate; that closed the proposer-refloods-a-copied-digest path. It did NOT address the FIRST-sight copy: an invalid-body proposal under a genuine signature is still recorded, relayed, re-gossiped, and used to dedup the genuine block. Materially new: keyless, poisons the round rather than merely re-flooding.

**Description.** proposalSignFields (messages.go:155) signs height/round/validRound/StateDigest but NOT the block body. onProposal accepts the first copy whose signature verifies and whose p.Proposer is the scheduled proposer (node.go:507), stores p.Block under its StateDigest and remembers/relays it even when ApplyBlock says the body is invalid (node.go:510-513) — the held fast path (node.go:498) and relay dedup (relay.go:29) then key only on StateDigest, so every later genuine copy is discarded. Because the signature omits the body, a 'genuinely invalid block the proposer signed' and 'a swapped body under a copied signature' are indistinguishable. Any peer/relay (no key, not a validator) can replay a seen proposal with a swapped body; if its copy arrives first the node prevotes nil against the real block, re-floods the forgery every cfg.Timeout, ignores genuine copies, and can only advance via height catch-up.

**Failure scenario.** N1 proposes a valid block B for height H (digest D, signature S over D). A keyless observer that saw it re-sends ProposalMsg{same H,R,VR,Proposer=N1,Signature=S, Block=forged body, StateDigest=D}. VerifyProposal passes (body not signed). It reaches honest node N first: N runs ApplyBlock(forged)->invalid, stores forged under D, remembers it, relays it, feeds the driver Valid=false -> N prevotes nil. The 4 genuine copies that follow hit the held/relay dedup on D and are dropped. Even after 3/4 validators precommit B, N's stored proposal is Valid=false so it never decides H via consensus; it stalls until checkpoint catch-up. On a multi-hop topology a relay node can forward only the forged copy, spreading the stall.

**PoC.** <scratch>/flake-suite/node/flake_swapbody_poc_test.go (TestPoCSwappedBodyFirstSightPoisonsRound — FAIL: node prevotes nil against the valid block, relays the forgery, ignores 4 genuine copies, stays at the height though 3/4 precommitted).

**Suggested fix.** Bind the block body into the proposal signature (add StateDigest's preimage or a body hash to proposalSignFields), so a swapped body fails VerifyProposal and is distinguishable from a proposer's own invalid block; or in onProposal refuse to let an ApplyBlock-invalid first copy occupy the round's held/relay slot (verify body->digest before remember/relay). Ties to the owner's 'two signings = burned key' principle: a one-shot signature that under-binds its payload lets a forgery through with no second signing to detect.
