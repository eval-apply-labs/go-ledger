# C33-resolve-binding

Round-1 reports merged into this cluster (1):

## [node-input] NODE-IN-4 — /hdls/resolve answer is not provably bound to its digest; documented cross-check (compare digests) is insufficient, and Height is off by one
- severity/confidence (round 1): low/high
- category: docs-drift
- locations: node/httptransport.go:270-300, node/httptransport.go:253-266, docs/operating-a-network.md:198, docs/security.md:87
- attacker: one dishonest reference node
- known_status: New (answers the brief's resolve question). Builds on fix #4 (Address now in the digest), which makes the correct cross-check possible but is not what the docs instruct.

**Description.** handleResolve returns a record (name, publicKey, address, description) read from committed accounts plus StateDigest=hex(snap.Decided) and Height=snap.Height. The fields are genuinely under that digest (Address was added to StateDigest by fix #4). But the answer carries NO commit certificate and NO inclusion proof, and the node simply asserts the 32-byte digest string. The ResolveRecord comment (:257) and operating-a-network.md:198 tell a distrustful client to 'query K nodes and compare — matching digests mean the same ⅔-agreed state'. That is weaker than claimed: a single dishonest node can return a fabricated record alongside the honest digest string it copied from others, and a client comparing only digests is fooled. The sound check is to compare the actual key/address FIELDS across nodes (which honest nodes agree on precisely because Address is in the digest). Also Height=snap.Height is the in-progress height (committed+1), while the digest finalizes committed height snap.Height-1.

**Failure scenario.** A client follows the docs and resolves alice via its reference node, comparing the returned stateDigest against a couple of other nodes. The reference node returns {address: attacker-controlled, publicKey: alice, stateDigest: <value copied from honest nodes>}. All digests match, so the client concludes the answer is ⅔-agreed and uses the attacker's address (e.g. for peer/endpoint discovery). Impact is bounded for payments (the wallet pays by ToName, resolved on-chain at apply time — ui/wallet.go), so this is address/display integrity, not fund redirection.

**PoC.** none — mechanism is evident from httptransport.go:270-300 (no certificate/inclusion proof served; digest is an unverified node assertion) and the wallet pay-by-name path (ui/wallet.go:216-222) bounding impact; rated low/high-confidence without a failing test.

**Suggested fix.** Either serve the Commit certificate (and ideally an inclusion proof) with the resolve answer so a client can verify the record against a ⅔ quorum, or correct the docs to say clients must compare the actual publicKey/address fields across independent nodes (digest equality alone is insufficient). Separately, label Height as the committed height the digest finalizes (snap.Height-1) or rename the field.
