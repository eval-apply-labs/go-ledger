LEDGER STATE AND MESSAGES: hand-off review report (reviewer: state, names, governance)

The code and the spec agree in every behaviour of my area. The code also honours every owner decision I checked: R1 to R11, Q3 (no rule tying the cost to the largest payment), Q22, Q23 and Q24 (a freeze may name a block at most 64 ahead). The problems are in the docs, in a few stale sentences of the spec, and in some code comments.

LED-1. wrong + missing: delegation, and what governance cannot change
- Where: docs/managing-the-currency.md 87-92 ("Voting power follows delegation … Account holders re-delegating is how the community keeps that honest"); same file, levers table at 272 ("delegation | each account holder | decides who holds the votes above"); same file, 321-324 and 404-410 (the validator-entry bar and "a punitive stake threshold" presented as a dial the community can turn); docs/governance.md 7-9 lists what the ⅔ can do, never what it cannot.
- Code: in static mode ledger/validators.go ValidatorRule.SetFor takes each power from StaticValidator.Power and ignores Delegate and balances. Chain.ValidatorRule (static list and powers, or the dynamic validatorThreshold) is set only by ValidateGenesis and is part of GenesisHash. No message changes it: ParameterChange covers the ten parameters, Reassignment one account's balance, key, bank of record and frozen mark. The demo and the runbook's example genesis are both static.
- Ran: a scratch test made every account delegate to bank-a; the static set stayed {bank-a:1, bank-b:1}.
- Change the docs. governance.md after line 9: "What the ⅔ cannot change is the validator rule: in static mode the genesis file's validators and their powers, in dynamic mode its validatorThreshold. Both are in the genesis hash, so adding a validator or changing a power or the threshold takes a new genesis; the ⅔ can only freeze or re-key a validator's name." managing-the-currency.md 88: start with "In dynamic mode (a validatorThreshold genesis), voting power follows delegation …; in static mode the genesis fixes each validator's power and delegation moves none." Qualify the table row and the threshold bullets the same way.

LED-2. wrong: "every name has a bank of record"
- Where: docs/managing-the-currency.md 299 ("Every name is registered at a bank, which co-signs it and becomes its bank of record") and 312 ("Every name already has a bank of record"); findings_20261027.md 559 ("Each account has a bank of record", marked Built (S5, S9)); spec §3.1 line 226 (Bank // … "" only for genesis or ⅔-created names (R2)).
- Code: a genesis account may have bank ""; the demo's founder has none (cmd/demo/main_test.go asserts it). The ⅔ can create a name without a bank, and can clear an existing name's bank: Reassignment.FitsState accepts Bank "-" and applyReassignment applies it (TestNameActionCanClearTheBank).
- Ran: a scratch ⅔ action with Bank "-" on alice took her bank from "bank-a" to "".
- Change: managing-the-currency.md 299: "Every name a holder registers is registered at a bank, which co-signs it and becomes its bank of record (a genesis name, or one the ⅔ create, may have none)"; 312: "Every registered name has a bank of record;". Spec comment: "" for none: a genesis or ⅔-created name may have none, and the ⅔ clear one with "-". findings_20261027.md 559: add "(genesis and ⅔-created names may have none, R2)".

LED-3. unclear: README omits that a bank change waits
- README.md 46-48: "A key change … waits about a day; a bank change needs the holder and the new bank." applyNameRequests gives a bank change the same h + ChangeDelayBlocks wait. architecture.md 85, governance.md 38 and USER_MANUAL.md 100 say both wait.
- Change: "A key change (a sale too) needs the holder, its bank and the new key; a bank change needs the holder and the new bank; both wait about a day."

LED-4. unclear: spec R9 intake rule for the name action is narrower than the code
- Spec §4.8 Intake, line 663, refuses only "a Freeze that alone would leave no name seated (R9)". node/reassignment.go reassignmentFits runs ledger.LeavesANameSeated on the whole action, at intake and in pruneReassignments.
- Ran: a dynamic chain whose only seated name loses its stake to a ⅔ claw-back (1 to 0, no Freeze): FitsState nil, Valid true; LeavesANameSeated false, so intake refuses it; ProposeBlock drops it; ApplyBlock refuses it under rule 24. §0.3 R9 and governance.md 89 already say "an action".
- Change the spec: "…, rules 6 to 8 (the target's frozen mark), or an action that alone would leave no name seated (R9): a freeze or, in dynamic mode, a balance cut."

LED-5. stale: names in the spec that the code does not use
- §4.1 step 5 (line 424) names AdmitPayment(payment, verifiedKey), §4.3 step 4 (line 536) AdmitNameRequest(request, verifiedKeys). In the code the exported AdmitPayment(payment) and AdmitNameRequest(request) run the whole intake; the on-loop steps are admitPayment(payment, verifiedKey) in node/mempool.go and admitNameRequest(request, requestorKey, bankKey) in node/requestpool.go.
- maxRequestLookahead (lines 531, 545, 1358, 1701) is node.MaxRequestLookahead.
- Change the spec to use those code names.

LED-6. stale: spec sentences about behaviour that has changed
- §4.2 line 471: "(today it answers 202 and drops the update silently)". handleAccountUpdate answers 409 with the reason. Delete the parenthetical.
- §5.3 line 906: "(today it returns a bool)". ApplyBlock returns an error. Delete it.
- §3.3 line 278: "The decoder refuses null for every other amount." NameRequest.UnmarshalJSON accepts a null Fee for every kind, and NameRequestShapeError refuses it for any kind but a freeze. New wording: "…every other amount, except a name request's Fee, which the shape check (§4.3) refuses for every kind but a freeze."

LED-7. stale code comments and tests (ledger)
- parameters.go 33: "one fee buys a permanently large ledger entry (a megabyte description)". Updates are free. New: "one free account update writes a permanently large entry".
- block.go 18-20, WithinMessageBound: "a megabyte description or comment" would be re-hashed into every later digest. A payment's comment is never kept in state; only a description is.
- receipt.go 9: "a validator node's signed acknowledgement". Every node signs a receipt; an unnamed observer's has Node "".
- messagesize_test.go 72 and 106: "Every peer decodes a proposal under 1 MiB". maxProposalBytes is 2 MiB.
- amount_test.go 84, 85 and 159: the Reassignment JSON uses the pre-Phase-2 key "Account", which decoding silently ignores. It should be "Name".

Checked and consistent: spec §3, §4.1-4.9, §5.5-5.8 against code (struct fields, digest tags and order, name regex, amount parser, canonical writer, bounds and defaults of all ten parameters, preimage fields and domains, message ids, shape checks, each kind's rules and effects, R1 to R11, pool keys, certificate pools); node intake against architecture.md; governance.md (bounds table, when changes take effect, "Extending it" names), architecture.md state/accounts/names/messages, managing-the-currency.md mechanics, README names/banks/payments/governance; all 134 backticked identifiers in those docs exist. Message sizes recomputed: payment 4511/4961/6361, update 4576, registration/key change/bank change/freeze 16136/16134/9119/4645, ⅔ name action 7299 + 4473 per vote, parameter change 473 + 4473 per vote. Quorum floor(2T/3)+1; supply formula covers every balance change; the demo screenshot adds up. Pending: none in the ledger (no TODO/FIXME, all ten parameters read, every exported ledger identifier used, no skipped tests).

Outside the area: review/phase2_progress.md "Two more points before the push" still says the screenshots predate Phase 1 (they were retaken on 2026-10-31).
