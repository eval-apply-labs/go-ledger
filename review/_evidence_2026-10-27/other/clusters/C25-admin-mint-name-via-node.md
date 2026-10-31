# C25-admin-mint-name-via-node

Round-1 reports merged into this cluster (1):

## [wallet-ui] KEY-4 — Admin reassignment (mint) votes resolve the typed name to a key via the untrusted node — validator signs a certificate targeting a key it never saw
- severity/confidence (round 1): medium/medium
- category: governance
- locations: ui/admin.go:156-167, ui/admin.go:172-200, ui/admin.go:203-209
- attacker: the wallet's reference node (a single dishonest/compromised node, amplified if validators share it)
- known_status: new angle on an accepted design (admin.go:151-155 comment argues the ⅔/independent-node quorum makes this safe). The new angle: the identical node-remap class was fixed for payments but remains in the reassignment/mint path, the UI never surfaces the resolved key before signing, and the 'independent honest node per validator' premise fails for the common shared-node village deployment.

**Description.** VoteReassignment maps the operator's typed name to a key with resolveAccountKey, which trusts whatever /hdls/accounts returns, and binds PreviousAmount from balanceOf (same untrusted source). It then signs the reassignment vote over the node-supplied Account key and PreviousAmount without ever showing the resolved key to the operator or checking it is a key they intended. A malicious/compromised reference node returns a fabricated account {Name:'coop', PublicKey: attacker, Amount: X}; the validator, intending to vote 'coop', signs a certificate targeting the attacker's key with a node-chosen previous amount. Because reassignment is the MINT path, if ≥⅔ of validators share that node (plausible when a village points every wallet at the founder's node) the quorum forms on the attacker's key and mints to it. The payment path was explicitly hardened (payments sign the NAME and nodes resolve on-chain — findings 'Third pass') to kill exactly this node-remap vector; the mint path was left resolving client-side, guarded only by the comment's assumption that every validator uses an independent honest node.

**Failure scenario.** PoC: a node whose /hdls/accounts maps 'coop'→attackerKey. Operator calls VoteReassignment('coop','1000000'); the submitted vote has Account == attackerKey and PreviousAmount == the node's fabricated 7. The UI showed/accepted only the name 'coop'. With a shared/compromised reference node across a ⅔ validator set, the mint enacts.

**PoC.** <scratch>/wallet-ui/ui/poc_admin_test.go (TestPoCReassignmentVoteTargetsNodeChosenKey — FAILS: 'user typed "coop" but signed a reassignment vote targeting the node-supplied attacker key'; also TestPoCReassignmentVoteSignsUnresolvedNameLiterally for the unregistered-name case).

**Suggested fix.** Do not let the node choose the target key for a signed mint vote. Require the operator to enter/confirm a raw public key for reassignment (or display the resolved key + current balance and require explicit confirmation before signing), and/or resolve names on-chain the way payments do. At minimum refuse to sign when resolveAccountKey falls through to a non-key literal.
