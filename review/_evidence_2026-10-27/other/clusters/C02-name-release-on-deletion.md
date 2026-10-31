# C02-name-release-on-deletion

Round-1 reports merged into this cluster (2):

## [burn-rule] NAME-1 — Deleting an account (spend-to-zero or slash) frees its registered name — a leaked key both drains AND hijacks the victim's name and all future pay-by-name traffic
- severity/confidence (round 1): high/high
- category: naming
- locations: ledger/ledger.go:209, ledger/block.go:230, ledger/block.go:231, ledger/account_update.go:75, ledger/account_update.go:93
- attacker: holder of one stolen or cracked key (drain + name capture); also anyone reaching the port can claim a name freed by a natural spend-to-zero
- known_status: new. Related to findings.md MEDIUM 'evidence replayable against recreated accounts' (fixed via age window) and the third-pass pay-by-name resolution hardening, but neither addresses name DURABILITY on account deletion / same-block re-registration. Not in findings.md or TODO.md.

**Description.** ProcessPayments drops any account whose balance reaches 0 (ledger.go:209), discarding its Name. applyAccountUpdates runs AFTER payments in the same block (block.go:230-231) and builds ownerByName from the post-payment accounts, so a name whose owner was just deleted is 'free' and another account's same-block update claims it. A slash (applySlashes) likewise deletes the offender and frees its name next block. The burn rule's opposite of burning: deleting an identity hands its name to the first claimant. Since pay-by-name resolves recipients via nameRegistry, the new owner then receives every future payment addressed to that name. A slashed/spent key is also recreated by any later payment (no tombstone).

**Failure scenario.** A thief holds alice's leaked key (eMudhra case). In one block: (1) signs alice→thief for alice's EXACT balance, draining her to 0 → alice's account+name deleted; (2) from the thief's own funded account signs an AccountUpdate claiming Name='alice' for this block. Payments apply first (alice gone), then the claim succeeds (name free). Now nameRegistry['alice']=thief, so every future 'pay alice' credits the thief — permanent identity hijack, not a one-time drain. No leaked key is even needed for the natural-spend-to-zero variant: anyone reaching the port can pre-stage a claim and grab any name the moment its owner spends to zero.

**PoC.** <scratch>/burn-rule/ledger/burnrule_poc_test.go — TestPoCSpendToZeroFreesNameInSameBlock (FAIL: name resolves to the other account), TestPoCSlashFreesValidatorNameForAnyone (FAIL), TestPoCSlashedKeyIsRecreatedByAnyPayment (FAIL: burned key recreated, self-delegated).

**Suggested fix.** Make a name survive its account's deletion for a window, or tombstone freed names: keep released names in committed state and refuse re-registration for N blocks / until a governance action. Alternatively resolve same-block name claims against PRE-payment ownership and refuse to re-grant a name whose owner is deleted in the same block. Tie name ownership to a monotonic claim sequence so a freed name cannot be grabbed in the same/next block.

## [ledger-transition] NAME-1 — Zero-balance deletion releases a registered name; another account can claim it in the same block and receive pay-by-name payments
- severity/confidence (round 1): high/high
- category: naming
- locations: ledger/ledger.go:191-214, ledger/ledger.go:164-169, ledger/block.go:72-89, ledger/block.go:151-153, ledger/block.go:230-232, ledger/account_update.go:118-146, ui/USER_MANUAL.md:36-42, ui/USER_MANUAL.md:86-90
- attacker: any account holder (fee-free update), once the name holder's balance reaches zero or it is banned or slashed
- known_status: new. The findings.md third pass moved name resolution on-chain; the release-on-zero lifecycle is not discussed anywhere.

**Description.** An account whose balance reaches zero is deleted, and its Name goes with it, with no owner-signed release. applyAccountUpdates runs after payments, against the post-payment accounts, so a fee-free update from any other account naming that block claims the freed name in the same block. Pay-by-name payments resolve at apply time. Payments already signed by name for later blocks, and all future ones, therefore credit the new holder. The user manual's migration advice ('create a fresh wallet there and pay yourself to it') empties the old account, and bans and slashes release names the same way. No doc says that emptying an account gives up its name.

**Failure scenario.** PoC: Alice ('alice', balance 10) moves 10 to her new wallet in block 5, and the same block carries another account's update claiming 'alice'. Bob's payment of 20 to 'alice', signed earlier for block 6, credits the claimant (1 → 20.90). The claimant keeps the name (first-come), so later payers typing 'alice' also pay it.

**PoC.** <scratch>/ledger-transition/ledger/poc_name_release_test.go: TestPoCZeroBalanceReleasesNameToAnotherKey FAILs (the claimant owns 'alice' and is credited with Bob's payment).

**Suggested fix.** Decouple identity from balance. Keep accounts that hold a Name, delegation or Address at zero balance, or tombstone a released name for a governed grace period. Release a name only through an owner-signed update. Warn in the wallet and manual.
