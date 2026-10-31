# C35-payments-no-burn-slot

Round-1 reports merged into this cluster (1):

## [burn-rule] PAY-1 — Payments have no conflict/burn slot — the burn rule never fires against a leaked payment key (the owner's eMudhra hypothesis does not hold in this code)
- severity/confidence (round 1): info/high
- category: key-management
- locations: ledger/ledger.go:160, ledger/ledger.go:181, node/node.go:432, signing/domain.go:11
- attacker: holder of one stolen or cracked key
- known_status: new framing — directly answers the owner's stated hypothesis. The architecture is intentional (payments are authorised by one signature); the point is that the burn principle's scope excludes payments, so it provides no leaked-key protection.

**Description.** There is no equivocation concept for payments. Two different payments from one payer for the same BlockID are not flagged: either both apply (if the balance covers them) or the payer is overdraw-banned (ledger.go:160-181) — never recorded as a double-sign, never yielding durable evidence, never burning the key. admitPayment (node.go:432) refuses a second same-block payment only when it would overdraw; it is a refusal, not a burn. So the 'two different signings means the key is burned' principle simply does not cover the statement that moves money. A leaked/cracked key (the eMudhra scenario the owner cites) drains an account with ONE signing — there is no second conflicting signing for the rule to catch.

**Failure scenario.** A thief obtains a user's key (leaked, or cracked from a low-entropy key as in eMudhra). The thief signs one payment draining the account for a near-future block the owner is not using. Honest intake accepts it (valid signature, funds present) and it commits. No 'two different signings' condition ever arises; the owner may never even produce a competing signature. The rule the owner expected to 'stop the issue immediately' is vote-only and does nothing here. Combined with NAME-1, the same leaked key also captures the victim's name. The rule protects consensus votes, not funds.

**PoC.** none (architectural scope observation); the concrete end-state harm of a leaked key is demonstrated by NAME-1's PoCs (drain-to-zero + same-block name capture) in <scratch>/burn-rule/ledger/burnrule_poc_test.go.

**Suggested fix.** Set expectations: the 'two signings = burn' rule is consensus-vote-only and cannot catch a single-signing drain. If leaked-key resilience for funds is wanted, add out-of-band mechanisms — key rotation/revocation, social recovery, or a hold/challenge window on large or name-changing transfers — rather than relying on equivocation detection.
