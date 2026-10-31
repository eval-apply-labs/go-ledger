# C29-power-saturation

Round-1 reports merged into this cluster (1):

## [consensus-core] SAT-1 — Power saturation equalizes all above-cap validators, inverting the '<1/3 of stake' premise
- severity/confidence (round 1): info/medium
- category: safety
- locations: consensus/validator.go:24, consensus/validator.go:40-41, docs/security.md
- attacker: <1/3 of true stake, but only reachable when multiple validators exceed 2^40 whole units — assumed away by the design, so no live break
- known_status: New angle on accepted C2 (truncation→saturation). C2 reasoned about the whale's own power not overflowing; it did not note that saturation equalizes multiple above-cap validators and can invert the BFT stake threshold. Also a docs-drift: security.md's '<1/3 of stake' claim does not hold once >1 validator exceeds the cap.

**Description.** NewValidatorSet saturates any power >2^40 to exactly 2^40. The C2 decision framed this as keeping a whale's 'largest possible voice', but it also discards the true-stake ordering AMONG above-cap validators: a validator with true stake ~2^63 and one with true stake 2^40+1 end up with identical power. A by-count majority of above-cap validators then meets quorum regardless of how small their true-stake share is relative to a larger whale — breaking the stake==power premise behind docs/security.md's 'BFT up to <1/3 of stake'.

**Failure scenario.** Set = {whale: true 2^63, byz1/byz2/byz3: true 2^40+1 each}. All four cap to 2^40. The three 'byz' (a vanishing fraction of the whale's true stake, i.e. far below 1/3 of true stake) hold 3·2^40 of capped power vs quorum ≈2.67·2^40 — they alone form a quorum and control finality. Governance/proposer views also show them as equals, masking the true disparity.

**PoC.** <scratch>/consensus-core/node/poc_consensus_core_test.go :: TestPoCSaturationEqualizesAboveCapValidators (PASS)

**Suggested fix.** If saturation can ever trigger in a deployment, scale all powers down by a common factor to preserve ratios instead of clamping independently; otherwise document that the <1/3-of-stake guarantee assumes no validator exceeds 2^40 whole units.
