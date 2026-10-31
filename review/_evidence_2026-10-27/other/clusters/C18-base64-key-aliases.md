# C18-base64-key-aliases

Round-1 reports merged into this cluster (1):

## [ledger-transition] KEYALIAS-1 — One Ed25519 key has unboundedly many accepted base64 spellings; in dynamic mode it can hold several validator identities and equivocate across them without evidence
- severity/confidence (round 1): medium/high
- category: key-management
- locations: signing/signer.go:60-70, ledger/ledger.go:118-121, ledger/ledger.go:197-200, ledger/account_update.go:148-150, node/validators.go:31-42, consensus/voteset.go:45-70, consensus/evidence.go:11-22
- attacker: one validator (controls the delegation strings of its own accounts)
- known_status: new

**Description.** Identities are compared as strings, but signatures verify against the decoded key. Go's base64 decoding ignores the 2 trailing bits of a 32-byte key and any \r or \n, even with Strict(). So one key has 4 spellings, plus unboundedly many with embedded newlines. Stake delegated under two spellings creates two validator identities for one key. That key can cast one conflicting vote per spelling in the same (height, round, type): both are counted, the VoteSet records no equivocation, and Evidence.Valid rejects a cross-spelling pair. 'Two different signings means the key is burned' is enforced per spelling, not per key. Payments to an alias also create a separate account for the same key.

**Failure scenario.** PoC: D1 delegates 400 to V's canonical string and D2 delegates 400 to an alias. deriveValidators yields both identities (400 + 400 of 1700). V signs prevote X as V and prevote Y as the alias at height 9, round 0. Both are counted, no evidence is recorded, and hand-built evidence is invalid.

**PoC.** <scratch>/ledger-transition/node/poc_key_alias_test.go: TestPoCKeyAliasEvadesEquivocationDetection FAILs.

**Suggested fix.** Accept a key string only if it decodes to 32 bytes and base64.StdEncoding.EncodeToString(decoded) == s. Enforce this in signing.VerifyBase64 and wherever the ledger stores a key (PublicKeyTo, Delegated, genesis, Validator fields), rejecting non-canonical keys at intake and in ApplyBlock. Strict() alone is not enough.
