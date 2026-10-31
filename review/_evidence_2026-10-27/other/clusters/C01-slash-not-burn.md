# C01-slash-not-burn

Round-1 reports merged into this cluster (5):

## [burn-rule] SLASH-1 — Static-validator mode: a slashed equivocator is NOT ejected from consensus (keeps full voting power); a ≥1/3 compromised key is an un-recoverable permanent stall
- severity/confidence (round 1): high/high
- category: key-management
- locations: node/validators.go:15, node/validators.go:16, ledger/block.go:82, node/node.go:406, node/node.go:413
- attacker: one validator (holder of a compromised validator key holding ≥1/3 of a static set)
- known_status: new. The brief asks this directly. findings.md's slashing items concern evidence replay/age windows, not the static-mode non-ejection. docs/managing-the-currency.md:40 claims the offender 'is ejected from the set' unconditionally — false here (see DOCS-1).

**Description.** applySlashes removes only the offender's ACCOUNT. In static mode the consensus set is Config.Validators (validators.go:15-16), which account deletion never touches, so the offender keeps its fixed voting power and keeps proposing/voting. Honest nodes still accept its votes (it is in their set). The VoteSet only un-counts it WITHIN one (height,round,type); each height gets a fresh Driver, so it equivocates anew every height, producing fresh evidence that slashEvidence re-carries every block (node.go:406-417) to no effect — the account is already gone and the set is unchanged, so the key is never removed. This is the DEFAULT deployment mode (the operating-a-network.md example).

**Failure scenario.** A static set of 3 equal-power validators (quorum = 3, so all three are needed — a realistic 'concentrate stake on reliable machines' setup per security.md). One validator's key is compromised and equivocates. Its votes are un-counted everywhere, so the honest 2 can reach at most power 2 < quorum 3 → no block ever commits. Slashing destroys the offender's on-chain balance but leaves it in Config.Validators with power 1, so quorum stays 3. There is no in-protocol recovery: operators must edit every node's static validators and re-genesis. In dynamic mode the same slash shrinks the set and self-heals; static mode cannot.

**PoC.** <scratch>/burn-rule/node/burnrule_poc_test.go — TestPoCStaticModeSlashDoesNotEjectValidator (FAIL: slashed key still a validator; honest power 2 < quorum 3). Permanent-stall follows from quorum math; the load-bearing fact (still in set) is asserted directly.

**Suggested fix.** Carry a committed 'slashed keys' set and subtract it from the effective validator set in static mode (validatorsFor), so a proven double-signer is removed from quorum/power/proposer selection, not merely stripped of its account. Recompute quorum over the reduced set so the honest remainder can still finalise.

## [burn-rule] SLASH-2 — Dynamic mode: stake delegated by OTHER accounts keeps a slashed equivocator in the validator set — an un-slashable Byzantine validator
- severity/confidence (round 1): high/high
- category: key-management
- locations: node/validators.go:31, node/validators.go:37, node/validators.go:40, ledger/block.go:82, node/node.go:413
- attacker: one validator whose seat is backed by delegated stake (possibly its own sock-puppet accounts)
- known_status: new. The brief asks this directly. docs/consensus.md:79 and README.md:77 say slashing 'ejects from a dynamic set' / 'zeroes out the double-signer' — false when delegated stake backs the seat (see DOCS-1).

**Description.** deriveValidators sums the balance of EVERY account whose ValidatorNode == key (validators.go:33-42). applySlashes deletes only the offender's OWN account; it never touches accounts that delegated TO the offender. So delegated stake alone keeps the offender in the derived set with power = that stake, and its key keeps proposing/voting after being 'slashed'. slashEvidence re-carries the same proof every block (node.go:413) with no effect: the offender's account is already gone and delegators are untouched, so it is never actually removed. The only exit is for the honest delegators to re-delegate — impossible if the attacker controls (sock-puppets) the delegating accounts.

**Failure scenario.** Dynamic set (validatorThreshold). Account D (stake 100) delegates to validator O (ValidatorNode=O). O's own account holds any amount. O equivocates and is slashed: O's account is deleted, but deriveValidators still sees D's 100 pointed at O, so O remains a validator with power 100 and keeps double-signing. If O's delegated power is ≥1/3, the un-counted-equivocator stall of SLASH-1 recurs and is equally unrecoverable (the set never shrinks). An attacker who sets up O's seat via its own delegating accounts has a permanently un-slashable Byzantine validator; the honest majority cannot remove it.

**PoC.** <scratch>/burn-rule/node/burnrule_poc_test.go — TestPoCDynamicModeDelegationKeepsSlashedValidator (FAIL: slashed key still a validator with power 100 from delegated stake).

**Suggested fix.** On slash, neutralize delegations to the offender: carry a committed tombstone for slashed keys and exclude them from deriveValidators regardless of delegated stake (or zero/redirect ValidatorNode fields pointing at a slashed key). Otherwise deleting the account never removes the key from the set.

## [ledger-transition] SLASH-1 — A slash does not burn the key: delegated stake keeps the equivocator in the dynamic set, the static set is untouched, and there is no tombstone
- severity/confidence (round 1): high/high
- category: key-management
- locations: ledger/block.go:72-89, node/validators.go:31-42, node/node.go:403-418, node/node.go:642-655, README.md:76-77, docs/consensus.md:76-81, docs/managing-the-currency.md:39-41
- attacker: one validator (an equivocating or leaked validator key)
- known_status: new. The findings.md MEDIUM (evidence replay against recreated accounts) was fixed with an age window; nothing addresses delegated stake or exclusion of the key. Contradicts the owner's burned-key principle and the README/docs claims (docs-drift).

**Description.** applySlashes removes only the account whose PublicKey is the offender's. In this DPoS design most of a validator's power is delegated, i.e. comes from accounts whose ValidatorNode points at it. Those accounts are untouched, and deriveValidators keeps summing them, so the equivocator stays a validator with nearly unchanged power. In static mode the slash never affects the configured set. Nothing records the key as burned, so it can be re-funded or re-delegated and keeps voting, proposing and signing governance votes. The node re-carries the evidence every block until the 1000-block window closes, because the offender is still 'a validator'. README, consensus.md and managing-the-currency.md all claim the slash ejects the offender from the set.

**Failure scenario.** PoC: dynamic threshold 100; validator V owns 10, and two delegators delegate 400 each to V. A block with valid equivocation evidence against V applies and V's own account is removed. deriveValidators still reports V as a validator with power 800 (it was 810). A leaked validator key that is caught double-signing therefore keeps its voting power.

**PoC.** <scratch>/ledger-transition/node/poc_slash_delegation_test.go: TestPoCSlashDoesNotEjectDelegatedValidator FAILs (power 810 → 800, still a validator).

**Suggested fix.** Add a committed BurnedKeys set to LedgerState, bound into the digest; valid evidence adds the key. deriveValidators and the node's static set exclude burned keys regardless of delegations, and votes, proposals and governance votes from burned keys are refused. Optionally reset delegations that point at a burned key back to their owners.

## [governance] GOV-4 — Slashing does not durably burn a proven double-signer's validating or governance power
- severity/confidence (round 1): high/high
- category: key-management
- locations: ledger/block.go:72, ledger/block.go:153, node/validators.go:31, node/node.go:649, docs/consensus.md
- attacker: a single validator whose key is proven compromised/equivocating (the exact party slashing targets)
- known_status: New. docs/consensus.md and managing-the-currency.md present slashing as THE accountability lever and claim ejection from a dynamic set; the code achieves durable ejection in no mode. Includes a docs-drift component (consensus.md 'ejecting them from a dynamic set').

**Description.** The owner's principle is 'two different signings means the key is burned'. It does not hold. applySlashes (block.go:72-89) deletes only the offender's OWN account. (1) Dynamic + delegated: deriveValidators (validators.go:31-61) sums each account's balance by its ValidatorNode; depositors delegating to a bank keep their own accounts, so a slashed bank keeps nearly all its voting power and stays a validator — directly contradicting docs/consensus.md 'removes the offender's account ... ejecting them from a dynamic set'. (2) Dynamic + self-staked: the offender is ejected, but node.commit (node.go:649-654) prunes its evidence the moment it leaves the set, so a single later incoming payment re-creates the account (ValidatorNode defaults to self in ProcessPayments) and re-admits the key — nobody re-slashes it. (3) Static set: the set is config; slashing confiscates the balance but the key keeps FULL consensus + governance voting power forever. In every mode a burned key keeps or regains power.

**Failure scenario.** Dynamic/delegated: a 'bank' validator B (own balance 1, 200 delegated to it) equivocates and is slashed. On every node B's derived power is still 200 of 400 — above the skip threshold, able to veto or help ratify. B keeps casting consensus votes and ⅔ governance votes (parameter/reassignment) that count. PoC confirms B remains in the set post-slash. Static: PoC shows a slashed key still supplies the DECIDING governance vote (3-of-4) to mint 1,000,000 to an attacker account after its own slash. Self-staked: PoC shows an ejected key back in the set one 20-unit payment later. The compromise-exclusion the eMudhra retrospective wants is absent.

**PoC.** node: <scratch>/governance/node/zz_govreview_poc_test.go (TestPoCGovSlashDoesNotBurnDynamicValidatorKey) — FAILs on delegated-still-validating and re-admit-by-payment. ledger: <scratch>/governance/ledger/zz_govreview_poc_test.go (TestPoCGovSlashedStaticValidatorStillRatifies) — FAILs: burned key casts the deciding governance vote

**Suggested fix.** Tombstone slashed keys in committed state (a durable burn-set under the digest): exclude a tombstoned key from deriveValidators and from the static set's effective power, reject its votes/certificates, and refuse to re-create or re-fund it. Keep evidence actionable against a recreated key (retire the 'prune once it leaves the set' step for tombstoning).

## [burn-rule] DOCS-1 — Docs claim slashing 'ejects the double-signer from the set' / 'zeroes it out' — the code removes an account, not the key's place in the validator set
- severity/confidence (round 1): low/high
- category: docs-drift
- locations: docs/managing-the-currency.md:40, docs/consensus.md:79, README.md:77
- attacker: no attacker (documentation)
- known_status: new. The brief requires flagging doc/README claims the code contradicts in this area.

**Description.** docs/managing-the-currency.md:40 says a double-signer 'is ejected from the set' (unconditional). docs/consensus.md:79 and README.md:77 say slashing 'removes the offender's account ... ejecting them from a dynamic set' and 'zeroes out the double-signer'. The code only deletes the account: in static mode the key stays in Config.Validators with full power (SLASH-1), and in dynamic mode delegated stake keeps it in the derived set (SLASH-2). So 'ejected from the set' is false in static mode and false in dynamic mode whenever others delegate to the offender. The design slogan 'two different signings means the key is burned' is likewise overstated: the account's balance is burned, the key is not.

**Failure scenario.** An operator reads managing-the-currency.md and believes a caught double-signer is automatically removed from consensus, so no manual action is needed. In a static network (the documented default) the compromised validator keeps voting and, if ≥1/3, stalls the chain with no in-protocol recovery (SLASH-1). The operator has no doc warning that slashing is cosmetic for the consensus set in static mode, or that delegated stake defeats it in dynamic mode.

**PoC.** none (documentation finding); the contradicted behavior is proven by SLASH-1 and SLASH-2 PoCs in <scratch>/burn-rule/node/burnrule_poc_test.go.

**Suggested fix.** Either fix the code (SLASH-1/SLASH-2: a committed slashed-set removed from the effective validator set) and keep the docs, or correct the docs to state that slashing confiscates the account/stake but does NOT remove the key from a static Config.Validators set and is defeated by delegated stake in dynamic mode, with the manual re-genesis recovery step spelled out.
