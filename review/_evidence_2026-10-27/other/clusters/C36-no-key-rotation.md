# C36-no-key-rotation

Round-1 reports merged into this cluster (1):

## [key-lifecycle] KEY-3 — No key rotation or revocation: a compromised account/validator key cannot be recovered faster than an attacker can spend, and 'two-signings-burns-the-key' does not cover ordinary keys
- severity/confidence (round 1): medium/high
- category: key-management
- locations: ledger/account_update.go:83-114, ledger/reassignment.go:80-107, consensus/evidence.go:5-23, ledger/ledger.go:157-169, docs/managing-the-currency.md:348-352
- attacker: holder of one stolen or cracked key (recovery gap; no cheap mint by itself)
- known_status: New framing in this area. Partly acknowledged in managing-the-currency.md (self-custody 'loss/theft fatal at scale; needs bolting on recovery/custodial'), but the absence of any rotation/transfer primitive and the narrow scope of the 'two-signings-burned' principle (validators only, not account keys) are not stated where the owner's premise expects them.

**Description.** Account identity IS the public key, and there is no primitive to move a name, stake delegation, validator seat or published address to a new key: applyAccountUpdates only lets an account claim a free name or edit its own attributes — never transfer ownership to another key. So once a key is known weak/leaked, the holder cannot rotate; a registered name can only be freed by the (compromised) key and then raced for. The sole community lever is a ⅔ Reassignment (seize-to-zero or mint-to-new-key), which needs vote collection plus a block — the attacker just signs a payment that clears in a near-future block on the same timescale, and typically wins. The owner's 'two different signings means the key is burned' principle is implemented ONLY for validator consensus equivocation (slashing, consensus/evidence.go); a leaked ORDINARY key that signs two conflicting payments is not burned — and the overdraw ban (two same-block payments) destroys the victim's own balance, aiding a griefer rather than protecting the owner. (For context on the eMudhra trigger: that was weak-entropy keys, which this code avoids via crypto/rand; the 'burned on double-sign' rule would not have applied to such user keys here.)

**Failure scenario.** Alice's key leaks. She generates a new key but cannot move her name/balance/delegations to it without the old key, and the attacker holds the old key too. She (or the community) initiates a ⅔ reassignment to a safe key; before it is collected and committed, the attacker signs a payment draining Alice's balance into the next block. Alice's name and validator seat remain bound to the dead key with no transfer path.

**PoC.** none — this is an absence-of-primitive / recovery-race observation, not a single failing assertion; traced end-to-end: account_update.go has no owner-transfer path, reassignment.go is the only balance-moving governance action, evidence.go slashes only vote equivocation.

**Suggested fix.** Add a signed key-rotation/account-transfer primitive (owner signs over a successor key; name, delegations and seat move atomically), and document the compromised-key playbook honestly (reassignment races the attacker; prefer register-then-pay and modest balances; no rotation today). Optionally extend 'burned on double-sign' to detect an account key that signs two conflicting same-block payments.
