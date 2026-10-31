# C26-keypair-consistency

Round-1 reports merged into this cluster (2):

## [key-lifecycle] KEY-1 — Config load does not verify the private key matches the configured public key (incomplete C4 fix; no sign+verify self-test) → silent consensus ghost
- severity/confidence (round 1): medium/high
- category: key-management
- locations: cmd/server/config.go:78-83, cmd/server/config.go:170-173, node/node.go:121-149, consensus/driver.go:253-262
- attacker: no attacker (operator misconfiguration / key-handling slip)
- known_status: Relates to C4 (findings.md 'External review (GLM) 2026-08-02', recorded FIXED). Verified the fix is incomplete in current code: config.go:78-83 checks only len(pub)==32 and len(priv)==64, not that priv derives pub, and there is no startup sign+verify. New angle: a correctly-sized but mismatched pair reproduces the C4 failure mode.

**Description.** The C4 fix validates Ed25519 key LENGTHS at load but never checks that the configured privateKey actually signs for the configured publicKey, and nothing does a sign+verify self-test at startup. Go's ed25519.Sign binds the private key's own embedded public bytes (priv[32:]) into every signature (confirmed in the stdlib signWithDom), so if an operator pastes publicKey from one keygen run and privateKey from another (both 32/64 bytes), the node's identity is the configured pub while its votes/proposals/receipts verify only under priv's real pub. castVote swallows the resulting situation silently; the node logs "up" and serves HTTP but can never contribute a counted vote. If that validator holds >1/3 power this is a liveness stall; otherwise it is a validator that is quietly absent from quorum.

**Failure scenario.** Operator assembles node.json from two keygen outputs (or rotates one field of the pair). load() accepts it. The node joins as a listed validator but every Vote it signs fails Verify under its configured identity (PoC: SignVote then Verify == false), so quorum silently excludes it. No error at load, no error at runtime — the exact 'consensus ghost' C4 set out to prevent, reachable past the length-only check.

**PoC.** <scratch>/key-lifecycle/cmd/server/poc_keymismatch_test.go — PASS (TestPoCConfigAcceptsMismatchedKeypair): load() accepts the mismatched config and the node's own signed vote fails verification under its configured pub.

**Suggested fix.** In load(), after decoding, derive the public key from the private seed (ed25519.PrivateKey(priv).Public()) and require it to equal the configured pub; and/or have node.New/Start do a one-shot sign+verify self-test with cfg.Signer and fail fast with a clear message.

## [wallet-ui] KEY-5 — Wallet trusts the stored publicKey over the private key; GCM uses empty associated data (KDF params / version / pubkey unauthenticated)
- severity/confidence (round 1): low/medium
- category: key-management
- locations: ui/keyfile.go:141, ui/keyfile.go:74-84, ui/keyfile.go:145-170, ui/USER_MANUAL.md:31
- attacker: holder of a stolen/tampered backup the victim later restores, or no attacker (file corruption of the unauthenticated field)
- known_status: new — the brief asks about GCM associated-data binding; findings records the sealing feature but not that KDF params/pubkey are outside the AEAD or that the stored pubkey is trusted over the private key.

**Description.** sealPrivateKey calls gcm.Seal(nil, nonce, priv, nil) — empty associated data — so none of the surrounding fields are authenticated: KDF name, Iterations, Salt, and especially the plaintext publicKey live outside the AEAD. loadOrCreateKey decodes k.PublicKey and uses it as the wallet identity WITHOUT deriving/checking it against the private key (an Ed25519 private key embeds its public key, so this is checkable). A tampered or corrupted publicKey field therefore yields a wallet whose displayed 'your account' / receive-address and whose PublicKeyFrom on outgoing payments is a substituted key, with no error at load. The manual tells users a sealed backup is 'safe to store anywhere', implying integrity as well as confidentiality, which the format does not provide for the public part.

**Failure scenario.** An attacker who can get the victim to restore a tampered (but still sealed) backup flips publicKey to the attacker's own key. The wallet shows and the victim shares the attacker's key as their receive address, so inbound payments are misdirected to the attacker. (Outgoing payments then fail node verification — a loud signal — which bounds stealth; a bit-flip in publicKey from disk corruption produces the same silent wrong-identity with no attacker.)

**PoC.** <scratch>/wallet-ui/ui/poc_keyfile_test.go (TestPoCPublicKeyNotBoundToPrivateKey) — FAILS: 'a key file whose publicKey does not match its private key loaded without error; wallet identity is now ...'.

**Suggested fix.** Derive the public key from the decrypted Ed25519 private key and either ignore the stored publicKey or verify equality and error on mismatch. Bind version + KDF params (kdf name, iterations, salt) and the public key into the GCM associated data so tampering any of them fails Open. Correct the 'safe to store anywhere' wording to note integrity of the public header is not cryptographically protected.
