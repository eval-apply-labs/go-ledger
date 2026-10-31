# Phase 2: code notes before the push

This is the last read-through of the code before Phase 2 is pushed. The owner, 2026-10-30: "it will
also be a good way to recheck that everything is tight before pushing". On 2026-10-31 the owner
asked for every note to be fixed, and all 75 are (see **Status**).

**Where they come from.**
- S12's guide writer read every production file in order as it wrote `docs/reading-the-code.md`.
- Three independent read-throughs went over the code alongside it:
  - signature, consensus and ledger;
  - the node and the commands;
  - the wallet.
- S11's writer and checks had already filed 14 notes.
- One verify agent checked all 98 notes against the code at `d467de6`. S12 changed no Go file, so
  the code is the same at `b142416`. The agent merged the notes that said the same thing and kept
  74. It rejected 2, listed at the end of this file.
- The guide's newcomer check found one more, a bug, that never reached the verify stage. It is C1.
  The recheck confirmed it with a test, and the orchestrator read the code.

That makes 75 notes in all.

**How the priorities were set.** The verify agent set them. The orchestrator agreed, and put C1
first.
- **Fix before the push:** a bug a user can hit, or a comment that misstates a security property.
  These are C1 to C6, about 25 lines of code plus tests in all.
- **Worth fixing:** one of these:
  - a wrong answer the user sees;
  - a cost a dishonest node or caller can impose;
  - a comment that misleads a reader of the code.

  These are C7 to C31.
- **Optional:** dead code, duplication, file moves and names. These are C32 to C75.

Each note gives:
- the file and the symbol;
- what is wrong;
- a suggestion and its size, as the verify agent estimated it;
- how it was checked.

**Status.** On 2026-10-31 the owner asked for every note to be fixed: C1 to C6 first, then C7 to
C31, then the rest. Their choices on four notes:
- C21: the timings follow the vote;
- C34: one shared helper holds the ⅔ rule;
- C40 to C43: the file moves are done last;
- C74: the status key is renamed.

Each fix goes through the same build as the slices: a builder, an independent check, a fix round,
and the check list on the merged tree. The tables' Status column gives the commits, and so does a
**Fixed** line under each note.

**Notes that touch each other.**
- C2 and the S10 counter stall (progress note, "For the owner, from S10"): the stall needs a run
  of refused payments, and C2 is where most refusals would come from.
- C3 and C16: exporting the node's 64-block window serves both.
- C7 and C8: a parameter draft the wallet should have refused (C7) is the likeliest certificate the
  node refuses while still answering 202 (C8).
- S11's points to the owner, now with numbers:
  - C8: the certificate routes' 202;
  - C9: a missing snapshot folder;
  - C10: catch-up keeps the sign log;
  - C13: `/hdls/status` on the loop;
  - C20: the last proposal kept in memory;
  - C21: the genesis-timed intervals.


| Fix before the push | Note | Size | Status |
|---|---|---|---|
| C1 | The wallet goes dark when the ⅔ re-key one pinned bank | about 2 lines and a test | fixed, `d46ce5c` |
| C2 | The wallet checks only part of the payment rule before it signs, and each refusal uses up a counter block | about 10 lines and 2 tests | fixed, `e12c8d1` and `d227efa` |
| C3 | The wallet takes any `-lookahead`; above 32 the node refuses its bursts, above 64 every payment and the freeze | about 4 lines | fixed, `dea5136` |
| C4 | A genesis power above 2^40 is cut down without a word, which changes the split between banks | about 2 lines | fixed, `d960736` |
| C5 | `writeAtomic` leaves its temporary file when it fails; the backup mirror leaves one per commit | about 4 lines | fixed, `2c04178` and `3c0f97c` |
| C6 | `signing`'s package comment says there is no raw-bytes signing API; `PrivateKey.Sign` is one | 3 comment lines | fixed, `2b9460e` |

| Worth fixing | Note | Size | Status |
|---|---|---|---|
| C7 | The admin page signs a parameter draft without checking it against the verified state | about 4 lines and a test | fixed, `712b7e2` |
| C8 | The certificate routes answer 202 even when the node refuses the certificate | about 15 lines | fixed, `9383fea` and `08e9295` |
| C9 | A missing snapshot folder makes a validator send nothing, without a log line | about 5 lines | fixed, `21a0723` |
| C10 | Catch-up keeps the sign log of a height it left behind on disk | about 3 lines | fixed, `fd1c66b` |
| C11 | Every unknown wallet URL, `/favicon.ico` included, reads and verifies a checkpoint | 2 lines | fixed, `fdb90d6` |
| C12 | Three wallet reads of the node have no size bound | 3 lines | fixed, `82ceab5` and `475a9f4` |
| C13 | `/hdls/status` goes through the consensus loop | about 5 lines | fixed, `11875f9` |
| C14 | `Message.From` is set in 15 places and read nowhere | about 16 lines removed | fixed, `b931dd4` |
| C15 | The wallet copies four node governance types field by field | about 45 lines removed | fixed, `d9b0043` |
| C16 | The wallet restates three node limits by hand | about 15 lines | fixed, `9a2d955` |
| C17 | "Proposal" names both a block proposal and a ⅔ certificate in `node` | 3 lines, or about 30 | fixed, `a376475` |
| C18 | `consensus`'s package comment says the round driver lives elsewhere | 3 comment lines | fixed, `8d65517` |
| C19 | The doc comments of package `node` and of `Node` describe an older node | about 15 comment lines | fixed, `b2d13a0` |
| C20 | `Node.commit`'s comment says an idle chain keeps no block; it keeps the last proposal in memory | 1 comment line | fixed, `c27620c` |
| C21 | `Config.Genesis`'s comment: three timings never follow a vote | 2 comment lines, or about 8 code lines | fixed, `5026082` and `05ed90b` |
| C22 | `relay`'s comment calls relaying a no-op on all-to-all peers; it is N × (N − 1) sends | 2 comment lines | fixed, `b6702e3` |
| C23 | The re-gossip comments still price a tick as they did with Ed25519 | about 8 comment lines | fixed, `b71f3ba` and `cce43ff` |
| C24 | `parameters.go` comments name a `timingConsistent` that does not exist and describe Phase 1's parameters | about 5 comment lines | fixed, `048372b` |
| C25 | `ParameterChange.Valid`'s comments say `Acceptable` checks two things; it checks every bound | 2 comment lines | fixed, `874cb13` |
| C26 | `ProposerAt`'s comment says power order; the code walks name order | 1 comment line | fixed, `f7890e0` |
| C27 | `ProcessPayments`' comment does not say that rule 17 already refuses what its guards drop | about 3 comment lines | fixed, `f344899` |
| C28 | keygen's comment calls a key an account's identity | 1 to 2 comment lines | fixed, `71e2705` |
| C29 | Two stale comments on the key checks in cmd/server's `load` | about 4 comment lines | fixed, `80da033` and `a3371de` |
| C30 | A wallet comment gives Phase 1 advice for one key on two machines | 1 to 2 comment lines | fixed, `5ffed3a` |
| C31 | `Server`'s comment lists 5 of the 15 routes | 1 comment line | fixed, `7668144` |

| Optional | Note | Size | Status |
|---|---|---|---|
| C32 | `VoteSet.PowerFor` is never called | 2 lines removed | fixed, `6bc42ea` |
| C33 | Seven functions only tests call | about 25 lines moved | fixed, `a2b2a60` (signing, consensus) and `0effb71` (node) |
| C34 | The ⅔ certificate rule is written out five times | 4 lines, or about 15 fewer | fixed, `8ccadd4` and `853dca2` |
| C35 | `maxBodyBytes` and `MaxMessageBytesLimit` are both 1 MiB, written separately | 1 line | fixed, `8867db5` |
| C36 | Two places compute a node's peers | about 25 lines fewer | fixed, `9f454b2`, `a973833` and `17f8d27` |
| C37 | The two certificate pools are written twice, side by side | about 15 lines | fixed, `467a47b` |
| C38 | `contentError`'s comment says steps C to G; it runs H too | 1 comment line | fixed, `64cd684` |
| C39 | The three pools do one job in three ways | about 18 edits | fixed, `ab592f5` |
| C40 | `VoteEvidence` and `Driver.Evidence` sit away from their kin | moves only | fixed, `ca35e69` and `12c588b` |
| C41 | Some ledger files do not hold the types their names suggest | moves only, about 150 lines | fixed, `2a8c307` and `c232f09` |
| C42 | `node.go` is 1,207 lines and holds parts of several guide steps | moves only, about 350 lines | fixed, `f7c0129` |
| C43 | `record.go` holds two HTTP handlers | a move of about 40 lines | fixed, `872aff4` |
| C44 | `handleResolve` scans every account; GET routes take any method | about 4 lines, and about 8 fewer | fixed, `f509c1b` |
| C45 | The demo's node has no pool or comment limit and no HTTP timeouts | about 8 lines | fixed, `aacd0ed` |
| C46 | `Node.self` copies `config.Name` | 1 line | fixed, `820177e` |
| C47 | "Snapshot" names four things | 6 lines | fixed, `e3768b2` |
| C48 | `Proposal.Valid` is always true | about 8 lines | fixed, `c346827` |
| C49 | Every block check hashes the whole state, for a field only pinned payments read | about 8 lines | fixed, `698456b` |
| C50 | Three power comments describe the setup from before the genesis file | 4 comment lines | fixed, `835f96a` |
| C51 | Two checks in the driver are always true | 3 lines | fixed, `4c91ba2` |
| C52 | The driver verifies its own vote twice | about 4 lines | fixed, `64d51a0` |
| C53 | `ActionDecided` repeats what its `Commit` holds | about 5 lines | fixed, `42058b0` |
| C54 | The ML-DSA-65 sizes are written in three forms outside `signing` | about 12 lines net | fixed, `cdfc389` and `5351e48` |
| C55 | The genesis parameter keys work only because JSON ignores case | 2 comment lines | fixed, `69b1e12` |
| C56 | `powerBeforeFreezes` reads as "before this block's freezes" | 3 renames | fixed, `bf684b1` |
| C57 | `fastForward` keeps the abandoned height's blocks in memory | 2 lines | fixed, `8a100c2` |
| C58 | `relaySeen` decides only for certificates | 2 comment lines, or about 20 | fixed, `523d81d` and `3474363` |
| C59 | `HTTPTransport.post` has two 5-second timeouts | about 6 lines fewer | fixed, `e9a2dda` |
| C60 | `Neighbors` checks three things that cannot happen | about 4 lines | fixed, `2968e38` |
| C61 | Eight functions recompute the validator set the view already holds | 8 lines | fixed, `6a936a0` |
| C62 | Three guards against zero timings never fire | 2 lines, or 6 | fixed, `dd636d0` |
| C63 | "Pending outgoing" is computed three times under two names | about 6 lines | fixed, `a69a30a` |
| C64 | The startup log's `validators=` is the total power | 1 line | fixed, `22ec327` |
| C65 | `adminPageData.PublicKey` is never shown | 2 lines | fixed, `3126e89` |
| C66 | A nil fee panics in `NameRequestFitsState`; only the wallet's own check stops it | 1 line | fixed, `13e7c9c` |
| C67 | Two wallet functions each GET `/hdls/status` | about 8 lines fewer | fixed, `82ceab5` |
| C68 | `incomplete`'s message speaks of a new key for parameter certificates too | 2 to 3 lines | fixed, `0362e8c` |
| C69 | `pageData.PublicKey`'s comment | 1 comment line | fixed, `a4f2e48` |
| C70 | `/hdls/governance` lists pooled certificates in random order | about 4 lines | fixed, `3f1c7fe` |
| C71 | `hashFields`' comment names two of its four callers | 1 comment line | fixed, `4e61245` |
| C72 | `ParseAmount`'s comment still mentions the node config | 1 comment line | fixed, `7639bd9` |
| C73 | The `ledger` package comment leaves out name requests and evidence | 1 comment line | fixed, `90bfa37` |
| C74 | "height" means two things on the wire | none, or about 4 lines at the next genesis | fixed, `11875f9` |
| C75 | Three small names from Phase 1 | 1 to 3 lines each | fixed, `e94219e` (and `a2b2a60` for Driver.Start) |

## Fix before the push

### C1. The wallet goes dark when the ⅔ re-key one pinned bank

*ui/trust.go: signedByPinned. Bug. Size: about 2 lines and a test.*

**Fixed** in `d46ce5c`. signedByPinned keeps a pinned name's vote only if it verifies under the
pinned key, then calls Commit.Verify. TestPinnedQuorumLeavesOutARekeyedPinnedName covers a re-keyed
pinned bank with the others above ⅔ (verifies) and below it (does not).

signedByPinned keeps every precommit from a pinned name, then calls Commit.Verify, which refuses the
whole commit if any one vote fails under the key the pin holds for that name. VoteSet.Commit puts
every counted precommit for the decided block into the commit. So once the ⅔ give one pinned bank a
new key (the recovery in docs/operating-a-network.md, 'A bank whose key leaked') and its node signs
under it, every commit that carries its precommit fails. Every wallet with the old pin then shows no
verified state, although the other pinned banks, holding more than ⅔ of the pinned power, signed
under their pinned keys. Spec Q17, trust.go's top comment and ui/README.md all say verification
stops only once a third or more of the pinned power has left or re-keyed. It fails closed: no wrong
balance is shown, and the freeze button still works through lastName.
TestPinnedQuorumIgnoresOtherKeys covers names outside the pin, never a pinned name under a new key.

**Suggestion.** In signedByPinned, keep only the votes of pinned names that verify under their
pinned key (vote.Verify with pinned.PublicKey(vote.Validator)), then call Commit.Verify. About 2
lines, plus a test case with one re-keyed pinned name. Or keep the strict rule; then trust.go's
comment, ui/README.md's pin paragraph and the guide's step 20 must each say that one re-keyed pinned
bank stops verification.

**Checked.** Found by the guide's newcomer check, which is why it is not in the verify stage's list.
The newcomer check and the recheck each ran a throwaway test in a scratch copy: four pinned
validators of power 1 (quorum 3), three signing under their pinned keys and the fourth under a new
key, gave false; without the fourth's vote, true. The orchestrator read signedByPinned,
Commit.Verify and VoteSet.Commit at b142416.

### C2. The wallet checks only part of the payment rule before it signs, and each refusal uses up a counter block

*ui/wallet.go: Wallet.ReviewPayment, Wallet.Send, Wallet.UpdateAccount. Bug. Size: about 10 lines and 2 tests.*

**Fixed** in `e12c8d1` and `d227efa`. ReviewPayment runs ledger.PaymentFitsState and
ledger.CreditFitsBalanceCap, and UpdateAccount runs ledger.UpdateFitsState on a copy measured as
signed, both before the counter takes a block. The tests check each refusal and that no block is
used. ui/README.md and ui/USER_MANUAL.md now say that only a refusal by the node uses a block.

ReviewPayment checks only part of the payment rule, by hand: the amount is positive, the payer holds
a name, and the payee exists and is not frozen. It does not call ledger.PaymentFitsState or
ledger.CreditFitsBalanceCap. Both are exported, and node.paymentIntakeError runs them against the
same committed state. So the wallet misses the cost floor, MaxPaymentAmount, a frozen payer, the
payer's balance and the payee's cap. UpdateAccount does not call ledger.UpdateFitsState, which
node.updateIntakeError runs. Send and UpdateAccount take a counter block (nextPaymentBlockID) before
they post, so every refusal uses up a block. That is what feeds the S10 counter stall.
review/phase2_progress.md ('For the owner, from S10') tells the owner that the wallet checks a
payment against the verified state before it signs, so 33 refusals in a row are unlikely. For these
causes it does not check. Every other signing path in the wallet runs the ledger's own rule first
(NameRequestFitsState, Reassignment.FitsState, Acceptable).

**Suggestion.** In ReviewPayment, keep the name from registeredName. Call
ledger.PaymentFitsState(checkpoint.State, ledger.Payment{From: name, To: to, Amount: parsedAmount})
and ledger.CreditFitsBalanceCap, and keep the payee lookup for the bank. In UpdateAccount, call
ledger.UpdateFitsState(checkpoint.State, update) before nextPaymentBlockID. About 10 lines, plus one
test each. Then correct the S10 sentence in the progress note.

**Checked.** Read ReviewPayment, Send (ReviewPayment, then nextPaymentBlockID, then the post) and
UpdateAccount (registeredName, then nextPaymentBlockID, with no fit check). PaymentFitsState,
CreditFitsBalanceCap and UpdateFitsState are exported and need only the state. Spec 7.3 does not ask
the wallet to skip these checks.

### C3. The wallet takes any `-lookahead`; above 32 the node refuses its bursts, above 64 every payment and the freeze

*ui/wallet.go, ui/main.go: LoadOrCreateWallet, the -lookahead flag, nextPaymentBlockID, Wallet.Freeze. Bug. Size: about 4 lines.*

**Fixed** in `dea5136`. node.MaxBlockIDLookahead is exported. LoadOrCreateWallet refuses a lookahead
above it minus blockSlack (32), before any key file is touched. The flag's help, ui/README.md and
docs/operating-a-network.md give the ceiling.

The wallet takes any -lookahead of 1 or more. A payment or an update names height + lookahead, and a
burst runs up to blockSlack (32) further (nextPaymentBlockID). A freeze names height + lookahead
(Wallet.Freeze). The node takes a payment, an update or a freeze only up to height + 64
(node.maxBlockIDLookahead, in paymentIntakeError, updateIntakeError and nameRequestIntakeError). So
above 32 a burst is refused, and above 64 every payment, every update and the freeze button are
refused. Each refused payment or update still uses up a counter block. The flag's help says 'raise
if they miss their block'. ui/README.md and docs/operating-a-network.md give no ceiling.

**Suggestion.** In LoadOrCreateWallet, refuse a lookahead outside 1 to 64 - blockSlack, with an
error that names the node's 64-block window, and give the ceiling in the flag's help. About 4 lines.
The 64 can come from an exported node constant (see the note on the wallet's copies of node limits).
The node's 64 is also slack for a node that is behind, so the owner may want a ceiling below 32.

**Checked.** Read LoadOrCreateWallet (only 'lookahead < 1' is checked), nextPaymentBlockID (window =
height + lookahead; it refuses only past window + blockSlack), Wallet.Freeze (blockID = height +
lookahead) and the three node intake checks. The wallet's height is the node's height in progress,
the same base the node checks against. Not deliberate: the owner's 2026-10-30 decision gives a
freeze the 64-block window so that it lands at once. Merges the guide writer's and the ui
read-through's notes.

### C4. A genesis power above 2^40 is cut down without a word, which changes the split between banks

*ledger/genesis.go: ValidateGenesis. Bug. Size: about 2 lines.*

**Fixed** in `d960736`. ValidateGenesis refuses a static power above consensus.MaxValidatorPower,
naming the field and the bound. docs/operating-a-network.md and spec §2.2 rule 6 give the ceiling.

ValidateGenesis refuses a static validator's power below 1, but accepts any power above
consensus.MaxValidatorPower (2^40). NewValidatorSet then cuts it down to 2^40 without a word. So a
genesis with powers of 2 x 2^40 and 3 x 2^40 seats both banks at 2^40, and the file's 2:3 split
becomes 1:1. GenesisHash hashes the raw powers, while ValidatorsHash and every quorum use the cut
ones. The function's doc says 'Each refusal names the field to fix', and MaxValidatorPower's own
comment argues against silent drops.

**Suggestion.** Next to the 'validator.Power < 1' check, also refuse validator.Power >
consensus.MaxValidatorPower, naming the bound. About 2 lines; ledger already imports consensus.

**Checked.** Read the static-mode loop in ValidateGenesis, NewValidatorSet's cap, and GenesisHash,
which writes validatorRule.StaticValidators' raw Power. Spec 2.2 rule 6 names only the floor, so the
missing ceiling is an omission, not a decision.

### C5. `writeAtomic` leaves its temporary file when it fails; the backup mirror leaves one per commit

*node/persist.go: writeAtomic, saveSnapshot (the BackupPath mirror). Bug. Size: about 4 lines.*

**Fixed** in `2c04178` and `3c0f97c`. Each error path after the temporary file is created removes
it. TestFailedWriteLeavesNoTemporaryFile makes the rename fail.

writeAtomic creates its temporary file, then writes, syncs, closes and renames it. If any of those
steps fails, it returns and leaves the temporary file behind. The snapshot and the sign log each use
one fixed temporary name, so each leaves at most one stale file. The BackupPath mirror names its
temporary file by height (<backupPath>.<height>.tmp). So a share that is full, or that refuses the
rename, gains one stale file for every mirrored commit, and the mirror's error is never logged.

**Suggestion.** When writeAtomic fails after it has created the file, remove the temporary file.
Either name the error result and add a deferred os.Remove(temporaryPath) on error, or call os.Remove
on each error path. About 4 lines. Optionally, log the mirror's first error.

**Checked.** Read writeAtomic: no error path after OpenFile removes the file. saveSnapshot's
goroutine ignores writeAtomic's error, and its temporary name contains node.lastCommit.Height.
backupBusy allows one mirror at a time, but each commit's mirror uses a new name.

### C6. `signing`'s package comment says there is no raw-bytes signing API; `PrivateKey.Sign` is one

*signing/scheme.go: package signing (doc comment), Scheme (doc comment). Misleading comment. Size: 3 comment lines.*

**Fixed** in `2b9460e`. The package comment says that Signer has no raw-bytes method and every check
rebuilds the domain preimage, and that PrivateKey.Sign takes raw bytes but outside tests only
Signer.Sign calls it. Scheme's comment says a Scheme does not sign.

The package comment says there is 'deliberately no sign these raw bytes API -- the only way to
produce a signature is through a Domain'. That is not true. Scheme.ParsePrivateKey is exported, and
it returns a PrivateKey whose exported Sign(message) signs any bytes, untagged. What does hold:
Signer has no raw-bytes method, production code calls PrivateKey.Sign only inside Signer.Sign, and
every check (Verify, VerifyBase64) rebuilds the domain preimage. This sentence is the package's
answer to audit C1, so it must be exact. Scheme's comment also says a Scheme 'never signs caller
free-text', but a Scheme does not sign at all; its PrivateKey does.

**Suggestion.** Reword the claim. 'Signer, the type the ledger, the node and the wallet sign with,
has no method that signs raw bytes, and every check rebuilds the domain preimage. PrivateKey.Sign
takes raw bytes: only Signer and test keys call it.' About 3 lines. Unexporting Sign is not
possible: brokenKey in consensus/driver_test.go implements signing.PrivateKey from outside the
package.

**Checked.** grep in production code: PrivateKey.Sign is called only in signing/signer.go
(Signer.Sign). ui/keyfile.go calls ParsePrivateKey only to derive the public key. brokenKey
(consensus/driver_test.go) implements Sign.

## Worth fixing

### C7. The admin page signs a parameter draft without checking it against the verified state

*ui/admin.go: Wallet.SignParameters. Inconsistency. Size: about 4 lines and a test.*

**Fixed** in `712b7e2`. SignParameters refuses a draft whose sequence or previous digest is not the
verified state's, or whose values fail Acceptable for its validator count, and says to draft it
again.

SignParameters signs any parameter draft that decodes. It does not check Sequence against
state.GovernanceSequence, PreviousParametersDigest against state.Parameters.Digest(), or
NewParameters.Acceptable. SignReassignment checks Reassignment.FitsState first, and SignNameRequest
checks NameRequestFitsState first. The facts it shows compare the checkpoint's parameters with the
draft's new ones. So a draft that amends a different parameter set still reads as 'current → new',
and the digest it amends is never shown. With one validator, handOver posts the vote at once; the
node refuses it, the route answers 202, and the page says 'certificate submitted'.

**Suggestion.** After decoding, refuse the draft unless change.Sequence == state.GovernanceSequence,
change.PreviousParametersDigest == state.Parameters.Digest() and
change.NewParameters.Acceptable(len(checkpoint.Validators)), and say 'draft it again'. About 4
lines, plus a test.

**Checked.** Read SignParameters, parameterFacts, handOver, SignReassignment and SignNameRequest.

### C8. The certificate routes answer 202 even when the node refuses the certificate

*node/httptransport.go: handleParameterVote, handleReassignmentVote. Inconsistency. Size: about 15 lines.*

**Fixed** in `9383fea` and `08e9295`. AdmitParameterCertificate and AdmitReassignmentCertificate
wait for the loop's verdict, as AdmitUpdate does: 202 once the certificate is pooled, or 409 with
the reason. The admin page's "certificate submitted" now follows only a pooled certificate. A
refused parameter change gets one sentence listing the causes, since ParameterChange.Valid returns
only a bool. TestCertificateRefusalAnswers409.

Both routes answer 202 whether or not addParameterVotes or addReassignmentVotes pooled the
certificate. /hdls/update and /hdls/name-request answer 409 with the reason. So the vote handler in
ui/main.go says 'certificate submitted — it enacts in the next block' even for a certificate the
node refused, such as a ⅔ freeze or seizure that the operator then believes is enacting. The wallet
checks Valid before it posts, so this happens only if the state moves in between, or through the
SignParameters gap above.

**Suggestion.** Send the loop's verdict back through an admission channel, as AdmitUpdate does, and
answer 409 with a reason. About 15 lines in node.go and httptransport.go. docs/api.md states the 202
today.

**Checked.** Both handlers call SubmitParameterVote or SubmitReassignmentVote, which enqueue the
event and return without a verdict, then write 202. From S11's notes; the owner was told of it in
the progress note.

### C9. A missing snapshot folder makes a validator send nothing, without a log line

*node/node.go, node/persist.go: Open, logVote, logProposal. Other. Size: about 5 lines.*

**Fixed** in `21a0723`. Open refuses a SnapshotPath whose folder does not exist, naming the folder.
TestOpenRefusesAMissingSnapshotFolder; two restart tests that used a missing folder to break the
sign log were rewritten.

Open treats a missing snapshot as a first start, even when the snapshot's folder does not exist.
Then every sign-log write fails. castVote drops the vote, process skips the proposal, and neither
logs anything. So the validator sends nothing. If another node commits, saveSnapshot logs the
failure; if no node commits, nothing is logged. S11's API check hit this when running the runbook's
4-bank example.

**Suggestion.** In Open, refuse a SnapshotPath whose folder does not exist (about 5 lines), or log a
failed sign-log write once.

**Checked.** loadSnapshot and loadSignLog return nil on ErrNotExist. castVote returns nil on a
signLog error without logging. process does 'continue' when logProposal fails. From S11's notes.

### C10. Catch-up keeps the sign log of a height it left behind on disk

*node/node.go: catchUp, adopt. Inconsistency. Size: about 3 lines.*

**Fixed** in `fd1c66b`. One helper, emptySignLog, serves commit, catchUp and adopt. catchUp empties
the log for the new height. adopt empties it at the log's own height, so a node that adopts a
checkpoint after a restart still waits one re-gossip interval before it signs. That keeps the
lost-log wait the runbook relies on; the owner may prefer the new height (one argument).
TestSignLogLeftBehindIsEmptied.

Node.commit writes an empty sign log for h + 1 once the snapshot of h is on disk. catchUp and adopt
call fastForward and saveSnapshot, but never touch the sign log. So a validator that signed at
height h and then caught up past it keeps h's log on disk, with its proposal's block, payments and
nonce, until it next signs. On an idle chain that can be days. Spec 3.6 and 6.9 intend that an idle
chain keeps none of this on disk.

**Suggestion.** After a successful saveSnapshot in catchUp and adopt, write the empty log for the
new height when the log is older, as commit does. About 3 lines.

**Checked.** Read commit, catchUp, adopt, heightSignLog and writeSignLog. From S11's notes.

### C11. Every unknown wallet URL, `/favicon.ico` included, reads and verifies a checkpoint

*ui/main.go: routes (the '/' pattern), handler.index. Other. Size: 2 lines.*

**Fixed** in `fdb90d6`. index answers 404 for any path but "/".

ServeMux's '/' pattern matches every path. So any unknown URL, /favicon.ico included, gets the full
wallet page. Each such request reads and verifies a checkpoint: up to 64 MiB and a quorum of ML-DSA
checks. It may also write the .state file.

**Suggestion.** In index, answer http.NotFound unless request.URL.Path == '/'. 2 lines. The exact
pattern '/{$}' does the same, but the code does not use that idiom yet.

**Checked.** routes registers '/' to index, and index calls verifiedState without looking at the
path.

### C12. Three wallet reads of the node have no size bound

*ui/trust.go, ui/wallet.go, ui/admin.go: Wallet.servedNetworkID, Wallet.height, Wallet.Governance. Inconsistency. Size: 3 lines.*

**Fixed** in `82ceab5` and `475a9f4`. Wallet.status() and Wallet.Governance refuse a status of 300
or more and read through an io.LimitReader.

verifiedState reads at most maxStateBytes of a checkpoint, because the wallet does not trust its
node. These three functions decode the node's answer without an io.LimitReader and without checking
the status code. So a dishonest node is bounded only by the 5-second client timeout.

**Suggestion.** Wrap each body in io.LimitReader, 3 lines. Optionally, refuse a status of 300 or
more, as postJSON does.

**Checked.** Read the three functions and verifiedState.

### C13. `/hdls/status` goes through the consensus loop

*node/httptransport.go: handleStatus (and handleAccounts, handleCheckpoint, handleGovernance). Inconsistency. Size: about 5 lines.*

**Fixed** in `11875f9`. /hdls/status is served from the committed view, off the event loop.
TestStatusServesTheHeightInProgressOffTheLoop.

These routes go through the event loop (Node.Snapshot or GovernanceView), and Snapshot copies the
account list on the loop. /hdls/status needs only the height, the account count and the network id.
All three are in the committed view, which handleResolve already reads off the loop. Every wallet
page and every catch-up tick asks /hdls/status. So unauthenticated GETs queue events on the
consensus loop, which has 1,024 slots; after that, enqueue blocks.

**Suggestion.** Serve /hdls/status from node.view: state.Height + 1, len(state.Accounts) and the
network id. About 5 lines. Moving the checkpoint and accounts routes off the loop would need
lastBlockHeader, lastCommit and the signing set in committedView, about 15 lines.

**Checked.** Read handleStatus, Snapshot (enqueue eventQuery), snapshot (append of the accounts),
enqueue and committedView. From S11's notes.

### C14. `Message.From` is set in 15 places and read nowhere

*node/messages.go: Message.From. Dead code. Size: about 16 lines removed.*

**Fixed** in `b931dd4`. Message.From and its settings are deleted.

13 production Message literals set From: node.self: 11 in node.go, 1 in updatepool.go and 1 in
requestpool.go. 2 test literals in chaos_test.go set it too. Nothing reads it.
HTTPTransport.Broadcast posts only the inner value, so From never travels. The test buses use
Broadcast's own from argument. A reader expects a sender field to be checked somewhere.

**Suggestion.** Delete the field and the 15 'From: ...' entries. About 16 lines, all removals.

**Checked.** grep found no read of .From on a Message in production or tests. The seven Broadcast
implementations in the tests do not read it. Merges the guide writer's and the node read-through's
notes.

### C15. The wallet copies four node governance types field by field

*ui/admin.go: governanceView, parametersView, parameterProposalView, reassignmentView. Duplicated rule. Size: about 45 lines removed.*

**Fixed** in `d9b0043`. The wallet decodes node.GovernanceView and its parts, and the four copies
are deleted.

These four types copy node.GovernanceView, node.ParametersView, node.ParameterProposalView and
node.ReassignmentView field by field, JSON tags included. The ui already imports node, for
node.Checkpoint, so the copies buy no independence. A new parameter must be added in both places.
governanceView.GovernanceSequence is decoded and never read. The comment speaks of 'the validator
set / quorum', but the copy has no validator set.

**Suggestion.** Make Governance return node.GovernanceView, and make adminPageData hold
node.ParametersView, []node.ParameterProposalView and []node.ReassignmentView. Delete the four
types. About 45 lines removed and 5 changed. The templates use the same field names.

**Checked.** Compared the field names and tags. The node types have only extra fields (Validators,
Voters). renderAdmin never reads GovernanceSequence. Merges the guide writer's and the ui
read-through's notes.

### C16. The wallet restates three node limits by hand

*ui/requests.go, ui/trust.go, ui/templates/requests.html: maxRequestLead, maxStateBytes, the text 'at most 3600'. Duplicated rule. Size: about 15 lines.*

**Fixed** in `9a2d955`. node.MaxRequestLookahead and node.MaxCheckpointBytes are exported, the
wallet's copies are gone, and requests.html prints the ceiling from its page data.

maxRequestLead (3600) restates node.maxRequestLookahead, which is unexported. SignNameRequest
refuses a block past it as one 'no node takes'. maxStateBytes (64 << 20) restates
node.maxCheckpointBytes. requests.html prints 'at most 3600' a third time. The wallet has no copy of
node.maxBlockIDLookahead (64), which is why -lookahead goes unchecked. Nothing ties the copies to
the node's values, so if one side changes, nothing fails. ui/admin.go already reads exported ledger
limits such as ledger.MaxPeersLimit and ledger.MinMessageBytes.

**Suggestion.** Export node.MaxRequestLookahead, node.MaxBlockIDLookahead and
node.MaxCheckpointBytes, and use them in ui/requests.go, ui/trust.go and ui/wallet.go. Pass the
ceiling to requests.html through requestsPageData. About 15 lines, mostly renames in node.

**Checked.** Read the constants on both sides and the template's label. Merges the guide writer's
and the ui read-through's notes.

### C17. "Proposal" names both a block proposal and a ⅔ certificate in `node`

*node/governance.go, node/node.go, node/messages.go: proposalKey, parameterProposals, reassignmentProposals, SubmitParameterVote, SubmitReassignmentVote, MessageParameterVote. Misleading name. Size: 3 lines, or about 30.*

**Fixed** in `a376475`. parameterChangeKey, parameterCertificatePool and
reassignmentCertificatePool; the inputs, messages and events say Certificate. JSON keys, routes and
view types are unchanged.

Everywhere else in the node package, 'proposal' means a block proposal: ProposalMessage, onProposal,
heightProposals, lastProposal, and messageKey's 'p:' key. But proposalKey, parameterProposals and
reassignmentProposals name ⅔ certificates. 'Vote' also names a whole certificate:
SubmitParameterVote(parameterChange), MessageParameterVote. The matching key for the name action is
reassignmentKey, which wraps ledger.ReassignmentID, while proposalKey hand-rolls a SHA-256 of three
fields.

**Suggestion.** Rename proposalKey to parameterChangeKey: the definition and 2 call sites, 3 lines.
Optionally, rename the maps and the Submit and Message identifiers (about 30 lines), keeping the
JSON keys and the routes, and add a ledger.ParameterChangeID like ReassignmentID (about 5 lines).

**Checked.** Read governance.go, relay.go's messageKey and the event handlers. Merges the guide
writer's and the node read-through's notes.

### C18. `consensus`'s package comment says the round driver lives elsewhere

*consensus/errors.go, consensus/driver.go: package consensus (doc comment). Misleading comment. Size: 3 comment lines.*

**Fixed** in `8d65517`. The package comment says the round driver is in package consensus; only
gossip, real timers and the block checks are in node.

The package comment says the package 'is the safety core only', and that the round driver (proposer
selection, timeouts, prevote/precommit locking) and gossip 'live in the networked layer above it'.
But the round driver is in this package: driver.go declares Driver, NewDriver, Resume,
ReceiveProposal, ReceiveVote and Timeout, and ValidatorSet.ProposerAt chooses the proposer. Only
gossip, real timers and the block checks are in node. A reader who follows the guide's order is sent
to the wrong place.

**Suggestion.** Replace the last paragraph. 'The round driver (driver.go) is here too, and is pure
as well: the node feeds it proposals, votes and fired timeouts, and carries out the Actions it
returns. Gossip, real timers and block checks are in node/node.go.' About 3 lines.

**Checked.** driver.go is in package consensus, and node/node.go only calls NewDriver, Resume,
ReceiveProposal, ReceiveVote, Timeout and Evidence. Merges the guide writer's and the signature
read-through's notes.

### C19. The doc comments of package `node` and of `Node` describe an older node

*node/messages.go, node/node.go: package node (doc), Node (doc), startHeight (doc), Config.SnapshotPath, handle, process, commit. Misleading comment. Size: about 15 comment lines.*

**Fixed** in `b2d13a0`. The package, Node, startHeight and SnapshotPath comments describe today's
node, and handle, process and commit have one line each.

The package doc calls the node 'a running validator' with 'a payment mempool'. It says the node
'exposes Submit* methods', with the HTTP layer a thin adapter over them. It calls the event loop
'the deliberate fix for the lock-free shared mutable state', an audit finding the reader does not
have. Node's doc says 'Node is a single validator' and that 'external callers only touch it through
Submit*/Snapshot'. The code differs:

- A node may be an observer, and it has three pools.
- The HTTP handlers use AdmitPayment, AdmitUpdate and AdmitNameRequest: the checks run off the loop,
  then the loop gives its verdict.
- GovernanceView also reads through the loop.
- handleResolve and the record routes read the committed view and the record files without the loop.

startHeight's doc says it starts the Driver, but with no work it sets idle and starts none.
Config.SnapshotPath names the snapshot and the sign log, but not the record files kept beside them.
handle, process and commit, the functions a reader follows most, have no doc comment.

**Suggestion.** Reword the package doc and the Node doc (about 8 lines). Add the idle case to
startHeight's doc and the record files to SnapshotPath's comment. Give handle, process and commit
one line each. About 15 comment lines in all.

**Checked.** Read messages.go's package comment, Node's comment, the Admit* functions,
GovernanceView, handleResolve, the record handlers, startHeight's hasWork branch, and recordPrefix
(snapshot path plus network). Merges the guide writer's and the node read-through's notes.

### C20. `Node.commit`'s comment says an idle chain keeps no block; it keeps the last proposal in memory

*node/node.go: Node.commit (the comment on emptying the sign log), lastProposal. Misleading comment. Size: 1 comment line.*

**Fixed** in `c27620c`. The comment says the sign log on disk keeps no block, while memory keeps the
last decided proposal.

The comment says the sign log is emptied so that 'an idle chain keeps no block (spec 3.6, 6.9)'. But
a few lines above, 'node.lastProposal = node.heightProposals[decision.Round]' keeps the decided
proposal, block included, in memory until the next commit, so that sendLastCommit can send it again.
The owner's §12 point from S11 already says that §3.6 holds for the disk only. The code comment
still makes the old claim.

**Suggestion.** Write '... is emptied, so the sign log on disk keeps no block while the chain idles;
the node keeps the last decided proposal in memory (sendLastCommit).' 1 line.

**Checked.** Read Node.commit and sendLastCommit. Spec 3.6 says blocks are 'not kept after the next
commit', which the memory copy obeys; only 'an idle chain does not keep the last block' is limited
to the disk. Merges the guide writer's, the node read-through's and S11's lastProposal notes. S11's
other option is rejected below.

### C21. `Config.Genesis`'s comment: three timings never follow a vote

*node/node.go, node/sync.go: Config.Genesis (field comment), reGossipInterval, catchUpLoop, startFirstHeight. Inconsistency. Size: 2 comment lines, or about 8 code lines.*

**Fixed** in `5026082` and `05ed90b`. The owner chose: follow the vote. The re-gossip interval, the
wait without a sign log and the catch-up interval read the committed view's timings, and a lowered
timing reaches the loops within a second (everyInterval). Spec §6.1, docs/governance.md and
docs/consensus.md follow. TestTimingsFollowAVote.

Config.Genesis's comment says the genesis parameters 'hold until a vote changes them; its step
timeout is also the re-gossip interval'. In fact three timings never follow a vote. reGossipLoop and
startFirstHeight's wait use the genesis StepTimeoutMs (reGossipInterval), and catchUpLoop uses five
times the genesis BlockIntervalMs, with a floor of 3 s. armTimeout and the post-commit wait do use
the governed values. So after a ⅔ change to the timings, for example a day per step in pigeon mode,
re-gossip still runs every 3 s. Spec 6.1 sets the re-gossip interval to the genesis StepTimeoutMs,
and docs/governance.md names the exception.

**Suggestion.** Fix the comment: '... its step timeout also sets the re-gossip interval and the wait
after a restart without a sign log, and its block interval sets the catch-up interval; a vote
changes none of these three.' 2 lines. Reading them from node.view on each tick (about 8 lines)
would change spec 6.1, so that is the owner's choice.

**Checked.** Read reGossipInterval, catchUpLoop, startFirstHeight, armTimeout/stepTimeout and
blockInterval, and spec 6.1. Merges the node read-through's note and S11's note.

### C22. `relay`'s comment calls relaying a no-op on all-to-all peers; it is N × (N − 1) sends

*node/relay.go: Node.relay (doc comment). Misleading comment. Size: 2 comment lines.*

**Fixed** in `b6702e3`. relay's comment gives the N × (N − 1) cost.

The comment says that 'With an all-to-all transport, relaying is a harmless deduplicated no-op after
the first hop.' It is not a no-op. Every node that takes a new proposal, vote, payment, update,
request or certificate sends it once to all of its peers. With all-to-all peers (maxPeers 0, the
default), a message therefore travels N x (N - 1) times instead of N - 1 times. With 21 validators,
a proposal of up to 768 KiB goes out 420 times instead of 20. No doc or comment states this cost for
proposals and votes.

**Suggestion.** State the cost: 'With all-to-all peers, every node that takes a new message sends it
once to all its peers: N x (N - 1) sends instead of N - 1, and the receivers drop the copies.' 2
lines. Stopping the relay on an all-to-all network is the owner's design choice, and it is not a
one-line change: Message.From never travels, so a receiver cannot tell who sent a message.

**Checked.** Traced the path. process broadcasts the node's own votes and proposals. On each
receiver, handle runs onProposal or rememberVote, which return true the first time, and then relay,
which passes relaySeen once per node and calls Broadcast to all peers.

### C23. The re-gossip comments still price a tick as they did with Ed25519

*node/node.go, node/sync.go: reGossipProposal, reGossipLoop (doc comments). Misleading comment. Size: about 8 comment lines.*

**Fixed** in `b71f3ba` and `cce43ff`. The re-gossip comments say what a tick sends, and that a
receiver decodes a whole proposal before it drops a copy.

reGossipLoop says 'votes are tiny; the proposal is deduped at every receiver'. reGossipProposal says
receivers dedup cheaply (onProposal's fast path, relaySeen), so a tick costs 'one map lookup per
peer'. Both claims date from Ed25519. A vote is now about 4.7 KB of JSON (spec 9.2). On each tick, a
node with a driver sends all of the following to every peer: every vote it holds for the height, the
last commit's precommits, the last decided proposal with its block (sendLastCommit), and the current
round's proposal with its block. Server.handleProposal reads and decodes the whole body (up to 2
MiB) before onProposal's map lookup drops the copy. relaySeen is not read when a message arrives:
only relay and reportEquivocation read it.

**Suggestion.** State the cost in both comments: what goes out on each tick, that a receiver decodes
a whole proposal before it drops the copy, and that nothing is re-gossiped while no driver runs.
About 4 lines each.

**Checked.** Read reGossipVotes (it returns early without a driver, then calls sendLastCommit),
sendLastCommit, reGossipProposal, Server.handleProposal (decode with maxProposalBytes), onProposal,
and every reader of relaySeen.

### C24. `parameters.go` comments name a `timingConsistent` that does not exist and describe Phase 1's parameters

*ledger/parameters.go: AssumedGossipHopMs, LedgerParameters, TimingConsistent (doc comments). Misleading comment. Size: about 5 comment lines.*

**Fixed** in `048372b`. The comments name TimingConsistent, list the ten parameters, and say "step
timeout" where they said "window".

AssumedGossipHopMs says 'see LedgerParameters.timingConsistent', and LedgerParameters says 'guarded
by timingConsistent'. The method is the exported TimingConsistent, which ui/admin.go calls.
LedgerParameters' doc still describes Phase 1's four parameters ('Beyond the transaction cost they
carry the network TIMING PROFILE'); there are ten now. TimingConsistent's doc speaks of 'the
exchange window', and says that moving the window changes how many nodes you can reach in it. But
the check compares only StepTimeoutMs with the gossip dimension. BlockIntervalMs, which its own
field comment calls 'the exchange-window cadence', is only bounded.

**Suggestion.** Write TimingConsistent in both comments. List the ten parameters in
LedgerParameters' doc. In TimingConsistent's doc, say 'the step timeout' where it says 'the window'.
About 5 lines.

**Checked.** No method named timingConsistent exists. TimingConsistent's body uses BlockIntervalMs
only in its bounds. Merges the guide writer's and the signature read-through's notes with S11's note
on LedgerParameters.

### C25. `ParameterChange.Valid`'s comments say `Acceptable` checks two things; it checks every bound

*ledger/governance.go: ParameterChange.Valid (doc comment and the Acceptable line comment). Misleading comment. Size: 2 comment lines.*

**Fixed** in `874cb13`. Both comments say the new parameters must pass Acceptable for the current
validator count.

The doc says the new parameters must be 'a well-formed operating profile (non-negative cost, and a
step timeout long enough for the gossip fan-out)'. The inline comment says 'an ill-formed profile:
negative cost, or a window too short for the fan-out'. Since S2, Acceptable checks all ten
parameters: the fee and both caps, the message bound, the delay and retention ranges, both timing
ceilings, and MaxPeers.

**Suggestion.** In both places: 'the new parameters pass Acceptable for the current validator count
(every bound LedgerParameters lists)'. 2 lines.

**Checked.** Read Acceptable and ParameterChange.Valid.

### C26. `ProposerAt`'s comment says power order; the code walks name order

*consensus/validator.go: ValidatorSet.ProposerAt (doc comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `f7890e0`. The comment says the walk is in name order.

The comment says that selection walks 'the power-ordered cumulative weights'. The code walks
validatorSet.validators, which NewValidatorSet sorts by name, as the loop's own comment says. This
comment is the one place a reader learns how a proposer is chosen.

**Suggestion.** 'Selection walks the validators in name order, adding up their power, until the sum
passes a well-mixed seed in [0, total power).' 1 line.

**Checked.** NewValidatorSet sorts by Name. The ProposerAt loop ranges over validatorSet.validators.

### C27. `ProcessPayments`' comment does not say that rule 17 already refuses what its guards drop

*ledger/ledger.go: ProcessPayments (doc and the cost clamp comment). Duplicated rule. Size: about 3 comment lines.*

**Fixed** in `f344899`. The code is unchanged. The comment says rule 17 refuses these payments
first, so the guards are a second line of defence, and that the genesis file sets the cost.

ProcessPayments has two guards. One drops a payment whose amount is nil or below the cost, or whose
payer or payee is missing (B1/B2). The other clamps a nil or negative cost. Neither fires in
production. Its only production caller is applyContent, and those payments already passed rule 17
(paymentError, then PaymentFitsState), in contentError or in ProposeBlock. The cost already passed
Acceptable. The comment presents the guards as protection 'for a caller that bypasses' the other
checks, but it does not say that rule 17 already refuses these payments. It also still says 'Config
load' for what is now the genesis file (S8). The PaymentID collapse is load-bearing and must stay:
contentError does not refuse a payment carried twice.

**Suggestion.** Keep the code and rewrite the comment, about 3 lines. Say that rule 17 refuses these
payments before a block applies, that the filters and the clamp are a second line of defence for the
money path (audit B1, B2), and that the genesis file sets the cost, not the config. Removing them
(about 15 lines) is possible. But they are the audit's B1/B2 fix and have their own tests in
ledger_test.go. Unexporting the function would also mean changing node/admission_test.go, which
calls ledger.ProcessPayments.

**Checked.** Read applyContent, contentError (F: paymentError on every payment), ProposeBlock and
LeavesANameSeated (no payments). grep found ProcessPayments' callers: block.go in production, and
tests in ledger and node.

### C28. keygen's comment calls a key an account's identity

*cmd/keygen/main.go: package main (doc comment). Misleading comment. Size: 1 to 2 comment lines.*

**Fixed** in `71e2705`. The comment says a pair is for a node's config or a name's key.

It says the pair is 'for use in a node config or as an account identity'. Since S3, a name is an
account's identity, and a key is only that name's current key.

**Suggestion.** 'for a node's config, or as a name's key in a genesis file, a registration or a key
change'. 1 to 2 lines.

**Checked.** Read the comment; spec 3 and the S3 changes make the name the identity.

### C29. Two stale comments on the key checks in cmd/server's `load`

*cmd/server/config.go: load (the key-pair checks and their comments). Misleading comment. Size: about 4 comment lines.*

**Fixed** in `80da033` and `a3371de`. The early key checks stay, for their clearer messages. The two
comments say what the checks do.

load first decodes publicKey, checks the length of both keys and checks that publicKey is canonical
base64. Then it derives the key from the seed and compares it. But signing.NewSigner already refuses
a seed of the wrong length. And the comparison refuses any publicKey that is not the canonical
spelling of the seed's key. So the early checks only choose which error message is printed. Two of
their comments are stale. 'A wrong-length key must fail HERE': the comparison below refuses that key
too. 'The checks below that compare keys as strings would miss it: a validator could start without
the sign log': since S3 the sign-log rule compares names, and the one key comparison refuses another
spelling rather than missing it.

**Suggestion.** Fix the two comments, about 4 lines. Optionally, drop the publicKey decode and the
three early checks (about 20 lines), verify the test signature under signer.PublicKey(), and word
the comparison's error so it covers every case. TestLoadRejectsTruncatedKeys' expected message then
changes. Keeping the checks is reasonable, since their messages tell an operator more.

**Checked.** MLDSA65.ParsePrivateKey refuses a seed that is not 32 bytes. The comparison is
signer.PublicKeyBase64() != configFile.PublicKey. isStaticValidator compares Name.

### C30. A wallet comment gives Phase 1 advice for one key on two machines

*ui/wallet.go: walletState.LastPaymentBlockID (comment). Misleading comment. Size: 1 to 2 comment lines.*

**Fixed** in `5ffed3a`. The comment points to ui/USER_MANUAL.md, "One key, one machine".

For the same key loaded on two machines, the comment advises 'register-then-pay and modest amounts'.
That is Phase 1 advice, from when a payment could go to a key. Today every payer and payee is a
registered name. ui/USER_MANUAL.md now covers the case under 'One key, one machine'.

**Suggestion.** '(The one case local state cannot cover is one key loaded on two machines at once:
see ui/USER_MANUAL.md, "One key, one machine".)' 1 to 2 lines.

**Checked.** Read the comment. The manual's section exists.

### C31. `Server`'s comment lists 5 of the 15 routes

*node/httptransport.go: Server (doc comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `7668144`. Server's comment points to the routes Handler registers.

The comment lists five routes. Handler registers 15.

**Suggestion.** 'Server exposes a Node over HTTP: the routes Handler registers.' 1 line.

**Checked.** Counted the HandleFunc calls in Handler. From S11's notes.

## Optional

### C32. `VoteSet.PowerFor` is never called

*consensus/voteset.go: VoteSet.PowerFor. Dead code. Size: 2 lines removed.*

**Fixed** in `6bc42ea`. Deleted.

Nothing calls it, in production or in tests.

**Suggestion.** Delete it and its comment. 2 lines.

**Checked.** grep found '.PowerFor(' nowhere outside its declaration. Merges two notes.

### C33. Seven functions only tests call

*signing/signer.go, node/node.go, consensus/driver.go: signing.Generate, node.New, Node.SubmitPayment, Node.SubmitUpdate, Driver.Start, Driver.Decision, Driver.Step. Dead code. Size: about 25 lines moved.*

**Fixed** in `a2b2a60` (signing, consensus) and `0effb71` (node). Driver.Start, Step and Decision
moved to consensus/driver_test.go, and the driver's decision field went with them. node.New,
SubmitPayment and SubmitUpdate moved to node/node_test.go, with handle's two admit checks.
signing.Generate stays, since tests in five packages call it, and its comment says so.

Only tests call these.

- signing.Generate: its comment says 'this is for client/local use', but keygen, the demo and the
  wallet call Scheme.GenerateKey. Tests in signing, consensus, ledger, node and ui call Generate (5
  packages, not 6).
- node.New: its own comment says it is for tests; the node tests call it 88 times.
- SubmitPayment and SubmitUpdate: no production caller, since the HTTP handlers use AdmitPayment and
  AdmitUpdate. They are the only reason Node.handle checks 'event.admit != nil' for payments and
  updates.
- Driver.Start: driver.go's file comment says the caller feeds 'Start (or Resume after a restart)',
  but the node calls Resume at every height. The progress note already lists Driver.Start.

**Suggestion.** Move New, SubmitPayment and SubmitUpdate, unchanged, into a _test.go file of package
node (a method can be declared there), and drop the two admit checks. About 25 production lines go,
with no test changes. Say in driver.go's comment that the node always calls Resume (1 line). Say in
Generate's comment that tests use it (1 line), or move it into test helpers.

**Checked.** grep of production callers: none for any of these. No ui or cmd code calls node.New.
Merges the guide writer's note and the two read-throughs' notes.

### C34. The ⅔ certificate rule is written out five times

*consensus/commit.go, ledger/governance.go, ledger/reassignment.go, ui/admin.go: Commit.Verify, ParameterChange.Valid, Reassignment.Valid, Wallet.SubmitParameters, Wallet.SubmitReassignment. Duplicated rule. Size: 4 lines, or about 15 fewer.*

**Fixed** in `8ccadd4` and `853dca2`. The owner chose one helper. consensus.CertificateTally
(NewCertificateTally, Add with a callback that verifies one signer, Power, Reached) holds the rule.
Commit.Verify, ParameterChange.Valid and Reassignment.Valid stop at the first signer that does not
count, so a certificate stays all-or-nothing, and the wallet's two Submit functions use it as a
filter, as before. Production code is about 22 lines longer, 15 of them comments, not 15 shorter:
the rule is in one place, but each certificate check keeps a two-line loop. New tests cover a
repeated name, a name outside the set, one bad signature and the quorum edge, and the check added a
case that one bad vote voids a parameter change or a name action.

The rule for a > ⅔ certificate is written out five times: each signer a distinct name in the set,
its signature checked under the key the set holds for it, the power summed and compared with Quorum.
ParameterChange.Valid and Reassignment.Valid also call signing.VerifyBase64 inline, instead of their
own package's VerifyParameterVote and VerifyReassignmentVote, which only the wallet calls.

**Suggestion.** Have the two Valid methods call VerifyParameterVote and VerifyReassignmentVote, 2
lines each. A shared helper for the tally would make the code about 15 lines shorter, but it touches
safety code, so it is the owner's call.

**Checked.** Read all five loops. Merges the guide writer's and the signature read-through's notes.

### C35. `maxBodyBytes` and `MaxMessageBytesLimit` are both 1 MiB, written separately

*node/httptransport.go, ledger/parameters.go: maxBodyBytes, MaxMessageBytesLimit. Duplicated rule. Size: 1 line.*

**Fixed** in `8867db5`. maxBodyBytes = ledger.MaxMessageBytesLimit, whose comment now names the
proposal route as the one exception.

The comment on ledger.MaxMessageBytesLimit calls it 'the 1 MiB every route decodes a body under'.
But the routes use their own constant, node.maxBodyBytes = 1 << 20, and nothing ties the two.

**Suggestion.** Define maxBodyBytes = ledger.MaxMessageBytesLimit, or drop the claim from the ledger
comment. 1 line.

**Checked.** Both constants are 1 << 20, written separately.

### C36. Two places compute a node's peers

*cmd/server/config.go, node/peerdns.go: load (its gossip peers), Node.resolvePeersFromState, resolvedPeerAddresses. Duplicated rule. Size: about 25 lines fewer.*

**Fixed** in `9f454b2`, `a973833` and `17f8d27`. Open calls resolvePeersFromState, and cmd/server's
load passes the configured peers on. TestResolvePeersAtOpen in package node carries the old test's
checks. The visible change, stated in docs/api.md, docs/operating-a-network.md and spec §8.2: with
every validator's address in the genesis, a fresh validator gossips to the validators from the
start, not from block 1.

Two places compute the Hypernode neighbours. load computes them from the static validators' genesis
addresses, and only when maxPeers is above 0. resolvePeersFromState computes them from the committed
state at every commit and fast-forward. It also runs inside Open on a restart (loadSnapshot calls
fastForward). So load's result matters only between a fresh start and block 1, and for maxPeers
above 0 both give the same peers. Spec 8.2 describes load's behaviour.

**Suggestion.** Call node.resolvePeersFromState() at the end of Open (1 line), and delete load's
topology block (about 25 lines). TestLoadObserverKeepsConfiguredPeersOnMaxPeersNetwork moves to the
node package. One visible change: with maxPeers 0 and every validator's address in the genesis, a
fresh node takes the validators as its peers at start, instead of at block 1. Spec 8.2's wording
changes with it.

**Checked.** Read load, resolvePeersFromState, resolvedPeerAddresses, Neighbors (nil for a name
outside the set) and fastForward. The guide writer asked whether maxPeers 0 gives the same peers as
today; it does not, as described. Merges two notes.

### C37. The two certificate pools are written twice, side by side

*node/governance.go, node/reassignment.go: addParameterVotes, addReassignmentVotes, pendingParameterChange, pendingReassignment, currentParameterVoters, currentReassignmentVoters, votersOfParameterChange, votersOfReassignment, tallyVoters. Needless complexity. Size: about 15 lines.*

**Fixed** in `467a47b`. maxPooledCertificates names the bound, the pending functions iterate
slices.Sorted(maps.Keys(...)), and tallyVoters returns only the power. No generic helper.

The two certificate pools are written twice, side by side.

- The pool bound is a bare 64 in both add functions, while the package names its other bounds;
  maxBlockIDLookahead is also 64, which invites confusion.
- pendingParameterChange and pendingReassignment sort their keys by hand, where record.go uses
  slices.Sorted(maps.Keys(...)).
- The voter-filter and voter-list functions are copies, one for each certificate type.
- tallyVoters returns its voters argument unchanged as a second result.

**Suggestion.** Add const maxPooledCertificates = 64 (3 lines). Use slices.Sorted(maps.Keys(...)) in
both pending functions (about 8 lines fewer). Make tallyVoters return only the power (about 3
lines). A generic helper for the copies is optional, about 15 lines fewer.

**Checked.** Read both files. Merges the guide writer's and the node read-through's notes.

### C38. `contentError`'s comment says steps C to G; it runs H too

*ledger/block.go: contentError (doc comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `64cd684`. Says steps C to H.

The comment says it checks 'spec 5.3 steps C to G'. It also runs step H, the one-third cap (rule
22), which the body labels 'H.'.

**Suggestion.** 'steps C to H'. 1 line.

**Checked.** Read the end of contentError.

### C39. The three pools do one job in three ways

*node/mempool.go, node/updatepool.go, node/requestpool.go: mempool, updatePool, requestPool, paymentKey, reassignmentKey. Inconsistency. Size: about 18 edits.*

**Fixed** in `ab592f5`. updatePool's fields are named as the other pools', the empty-id checks are
gone, requestPool.add enforces its slot (slotTaken is deleted), and paymentKey is dropped. A side
effect, recorded in spec §4.3: the slot is checked after the fee checks, and a full pool and a taken
slot share one logged refusal.

The three pools do one job in three ways.

- Fields: mempool and requestPool use pending, seen and maxSize; updatePool uses pendingUpdates,
  seenIdentities and maximumSize.
- mempool.add tests key == "" and updatePool.add tests updateIdentity == "". Neither can fire,
  because hashFields always returns 64 hex digits.
- updatePool.add enforces one update per name and block by itself. requestPool.add enforces nothing;
  admitNameRequest calls contains and slotTaken first, though add's comment says it 'pools a request
  whose slot is free'.
- updateSlot and requestSlot build the same key.
- paymentKey and reassignmentKey wrap ledger ids, while the other pools call ledger.UpdateID and
  ledger.NameRequestID directly.

**Suggestion.** Rename updatePool's fields (about 12 edits). Drop the two empty-id tests (2 lines).
Give add the same meaning in all three pools by moving the slot check into requestPool.add (about 4
lines). Optionally, drop the two one-line wrappers.

**Checked.** Read the three pools and hashFields. Merges two notes.

### C40. `VoteEvidence` and `Driver.Evidence` sit away from their kin

*consensus/voteset.go, consensus/evidence.go: VoteEvidence, Driver.Evidence. Other. Size: moves only.*

**Fixed** in `ca35e69` and `12c588b`. VoteEvidence's declaration is in evidence.go, beside its Valid
method, and Driver.Evidence is in driver.go. The guide's step 2 reads evidence.go before voteset.go,
which uses it.

VoteEvidence is declared in voteset.go, while its Valid method and ProposalEvidence are in
evidence.go. evidence.go also ends with a Driver method. Everywhere else, a type sits with its
methods.

**Suggestion.** Move the VoteEvidence declaration with its comment (about 10 lines) to evidence.go,
and Driver.Evidence to driver.go. Moves only.

**Checked.** Read both files. Merges two notes.

### C41. Some ledger files do not hold the types their names suggest

*ledger/parameters.go, ledger/ledger.go, ledger/validators.go, ledger/reassignment.go: LedgerState, Payment, ProcessPayments, Chain, AccountAmount. Other. Size: moves only, about 150 lines.*

**Fixed** in `2a8c307` and `c232f09`. LedgerState, its Digest and NamesAscending are in ledger.go,
Payment and ProcessPayments in payment.go, and AccountAmount beside FindAccount, with a comment that
says what it returns. Payment's comment points at SignPayment instead of its own file.

Some files do not hold the types their names suggest, so the guide has to send readers into the
middle of files.

- LedgerState, its Digest and NamesAscending are in parameters.go.
- Payment and ProcessPayments are in ledger.go, while payment.go holds only their id and signature.
- Chain is in validators.go.
- AccountAmount, a general lookup used in block.go, name_request.go, node and ui, is in
  reassignment.go. Its comment gives one narrow reason for exporting it, while its siblings
  FindAccount and findAccountIndex are in ledger.go.

**Suggestion.** Move LedgerState to ledger.go, and Payment and ProcessPayments to payment.go. Move
AccountAmount next to FindAccount, with a comment that says what it returns. Moves only, about 150
lines.

**Checked.** Read the files. Merges the guide writer's note and the signature read-through's
AccountAmount note.

### C42. `node.go` is 1,207 lines and holds parts of several guide steps

*node/node.go: checkPayment, paymentIntakeError, admitPayment, AdmitPayment; pendingProof to reportEquivocation; adopt, catchUp, verifyCheckpoint, anchorVouches. Other. Size: moves only, about 350 lines.*

**Fixed** in `f7c0129`. node.go went from 1,221 to 877 lines and keeps Config, the event loop, one
height and commit. Payment intake moved to mempool.go, AdmitUpdate to updatepool.go, the double-sign
proofs to a new proofs.go, the checkpoint checks to sync.go, and selfConsistent and fastForward to
persist.go, beside loadSnapshot, which calls them. The last two moves are the builder's reading of
the cut; putting them back is also a pure move.

node.go is 1,207 lines and holds parts of several guide steps. Payment intake is in node.go, while
update and request intake are in updatepool.go and requestpool.go. The double-sign proofs and the
checkpoint checks are also in node.go, while catch-up's fetches are in sync.go.

**Suggestion.** Move payment intake to mempool.go, the proof functions to a new proofs.go, and the
checkpoint checks to sync.go. Moves only, about 350 lines.

**Checked.** wc -l node/node.go gives 1,207. The functions are where the note says.

### C43. `record.go` holds two HTTP handlers

*node/record.go: handleRecord, handleLifelongRecord, recordFrom. Other. Size: a move of about 40 lines.*

**Fixed** in `872aff4`. handleRecord, handleLifelongRecord and recordFrom are in httptransport.go;
readRecord and the writing side stay in record.go. The guide's step 16 is now "The signature
record".

record.go holds the record's writing side, in the node area, and two HTTP handlers, in the API/CDN
area. The docs map and the guide have to split the file.

**Suggestion.** Move the two handlers and recordFrom to httptransport.go, and keep readRecord in
record.go. A move of about 40 lines.

**Checked.** Read record.go.

### C44. `handleResolve` scans every account; GET routes take any method

*node/httptransport.go: handleResolve, Server.Handler, decode. Inconsistency. Size: about 4 lines, and about 8 fewer.*

**Fixed** in `f509c1b`. All 15 routes are registered with their method, so ServeMux answers 405 to
any other, and the method checks in decode and handleResolve are gone. handleResolve finds ?name=
with ledger.FindAccount. TestRoutesAnswer405ToAnotherMethod.

For ?name=, handleResolve scans every account, though the view's accounts are in name order and
ledger.FindAccount searches by halves. Only ?key= needs the scan. handleResolve is also the only GET
route that refuses another method. /hdls/status, /hdls/accounts, /hdls/checkpoint, /hdls/governance
and the two record routes answer any method, while decode refuses anything but POST on the POST
routes.

**Suggestion.** Use FindAccount for ?name=, about 4 lines. Register each route with its method, for
example 'GET /hdls/status' and 'POST /hdls/pay': ServeMux answers 405 by itself since Go 1.22, and
go.mod says 1.27. Then delete the method checks in decode and handleResolve: 15 route strings
change, and about 8 lines go.

**Checked.** Read handleResolve, Handler and decode. Merges the guide writer's and the node
read-through's notes.

### C45. The demo's node has no pool or comment limit and no HTTP timeouts

*cmd/demo/main.go: main (node.Config, http.ListenAndServe). Inconsistency. Size: about 8 lines.*

**Fixed** in `aacd0ed`. demoNodeConfig sets cmd/server's caps (10000, 280), and demoHTTPServer has
cmd/server's timeouts. The values are written again in the demo, since cmd/demo cannot import
cmd/server; a test in each package pins them. TestDemoNodeHasTheServerLimitsAndTimeouts.

The demo builds node.Config without MaxMempool or MaxCommentLength. For both, 0 means unlimited,
while every cmd/server node gets 10,000 and 280 from load. The demo also serves with plain
http.ListenAndServe, without the timeouts cmd/server sets (findings #10), and its -listen flag can
move it off 127.0.0.1. The wallet never sends a comment, so the comment difference shows only
through the API.

**Suggestion.** Set MaxMempool: 10000 and MaxCommentLength: 280 in the demo's Config (2 lines), or
move the two defaults into node.Open so that 0 means the default everywhere. Use cmd/server's
http.Server literal, about 6 lines.

**Checked.** Read cmd/demo main, cmd/server load and main, and Wallet.Send (no Comment). Merges two
notes.

### C46. `Node.self` copies `config.Name`

*node/node.go, node/httptransport.go: Node.self, Config.Name. Needless complexity. Size: 1 line.*

**Fixed** in `820177e`. handlePay uses node.self.

Node.self is a copy of config.Name. Every use reads node.self, except handlePay, which reads
server.node.config.Name.

**Suggestion.** Use node.self in handlePay (1 line), or drop the field and read node.config.Name
everywhere (about 30 lines).

**Checked.** grep for config.Name and node.self.

### C47. "Snapshot" names four things

*node/mempool.go, node/updatepool.go, node/requestpool.go, node/node.go: the pools' snapshot method, Node.snapshot, Snapshot, savedSnapshot. Misleading name. Size: 6 lines.*

**Fixed** in `e3768b2`. The pools' method is pendingCopy.

'Snapshot' names four things: each pool's copy of its pending entries, Node.snapshot (the committed
view for checkpoints), the Snapshot type a read route returns, and the snapshot file (savedSnapshot,
Config.SnapshotPath).

**Suggestion.** Rename the pool method to pendingCopy. 6 lines.

**Checked.** Read the declarations.

### C48. `Proposal.Valid` is always true

*consensus/driver.go: Proposal.Valid. Dead code. Size: about 8 lines.*

**Fixed** in `c346827`. Proposal.Valid and its three checks are gone. The comments say the caller
passes only proposals whose block applied. The builder traced every path that hands the driver a
proposal: onProposal applies the block first, startHeight's held proposals come only from
rememberProposal, and the driver's own startRound proposes the candidate or a value it held.

Every Proposal the driver sees has Valid: true. That covers startRound's own proposal,
node.startHeight's held proposals and onProposal's. onProposal drops a block that fails ApplyBlock
before it reaches the driver. No test passes Valid: false. So the false branches of prevoteFor's 'if
proposal.Valid' and of the two 'proposal.Valid &&' guards never run, and that path has no test.

**Suggestion.** Drop the field and its three checks, and say in Proposal's comment that the caller
passes only proposals whose block applied. About 8 production lines, plus mechanical test edits. Or
keep the field so the driver stands alone, and add one driver test that passes Valid: false.

**Checked.** grep found no 'Valid: false' and no Proposal literal without Valid: true outside the
driver's own zero values.

### C49. Every block check hashes the whole state, for a field only pinned payments read

*ledger/block.go: contentError, ProposeBlock (stateDigestHex). Other. Size: about 8 lines.*

**Fixed** in `698456b`. contentError and ProposeBlock hash the pre-block state only when the first
pinned payment appears. TestOnlyAPinnedPaymentHashesThePreBlockState counts the allocations.

Both functions always compute digestHex(previousState.Digest()), but only a pinned payment reads it,
and pins are optional. I measured LedgerState.Digest on this machine, with 2604-character keys: 24
ms at 1,000 accounts and 390 ms at 23,000, the most a 64 MiB checkpoint holds. Each height, a node
runs ProposeBlock, ApplyBlock in onProposal, and ApplyBlock again at commit. That is up to three of
these, and publishView computes the committed digest once more.

**Suggestion.** Compute the digest the first time a payment carries a PreviousStateDigest. About 4
lines in each function. Q18 keeps networks far below 23,000 accounts, so this is optional.

**Checked.** A benchmark in a scratch copy (p2-scratch/s12-notes-check) measured 24.2 ms and 390 ms
per Digest.

### C50. Three power comments describe the setup from before the genesis file

*consensus/validator.go, ledger/validators.go: Validator.Power, MaxValidatorPower (doc), ValidatorRule.SetFor (doc). Misleading comment. Size: 4 comment lines.*

**Fixed** in `835f96a`. The three comments describe the genesis power and the stake path.

Since S8, a static seat's power comes from the genesis file, but three comments still describe the
older setup.

- Validator.Power says 'delegated stake, in whole units', which is true only in dynamic mode.
- MaxValidatorPower calls NewValidatorSet 'the single choke point both the fixed-config and
  stake-derived paths go through'. There is no fixed config any more, and SetFor also caps a stake
  past int64.
- SetFor seats a static validator 'with its configured power'.

**Suggestion.** Validator.Power: 'the genesis power in static mode, the floor of delegated stake in
dynamic mode'. MaxValidatorPower: 'the static (genesis) path and the stake path'. SetFor: 'with the
power the genesis file gives it'. About 4 lines.

**Checked.** Read the three comments and SetFor.

### C51. Two checks in the driver are always true

*consensus/driver.go: Driver.applyOneRule, Driver.evaluate. Dead code. Size: 3 lines.*

**Fixed** in `4c91ba2`. Both checks are gone.

applyOneRule returns at once when decided is true, and the decide loop returns whenever it sets
decided. So the 'if !driver.decided {' around the skip rule is always true. In evaluate, '||
driver.decided' only saves one call. decided can also become true inside startRound's nested
evaluate, after which the skip rule returns progressed true. The next applyOneRule then returns
(nil, false) at once.

**Suggestion.** Remove the wrapper (2 lines) and '|| driver.decided' (1 line).

**Checked.** Traced applyOneRule and evaluate, including the nested startRound path. The note's
reason for evaluate was incomplete, but its fix holds.

### C52. The driver verifies its own vote twice

*consensus/driver.go: Driver.count, Driver.ReceiveVote. Needless complexity. Size: about 4 lines.*

**Fixed** in `64d51a0`. count reports whether VoteSet.Add verified the vote, and ReceiveVote marks
its own vote cast on that without a second check. Resume now also marks as cast only an own vote
that verifies under the set's key. Production behaves the same, since the node checks every vote
before Resume sees it. TestDriverResumeSkipsAVoteThatDoesNotVerify pins this.

count throws away VoteSet.Add's result. So ReceiveVote checks the signature of one of the node's own
votes a second time (vote.Verify under driver.self's key) right after Add has checked the same bytes
under the same key. Resume marks votes as cast without that check.

**Suggestion.** Make count report whether Add verified the vote (err is nil or ErrEquivocation).
Have ReceiveVote mark its own vote as cast on that, without its own Verify call. About 4 lines.

**Checked.** In Add, every return after the signature check means the vote verified; ErrEquivocation
is returned only after it.

### C53. `ActionDecided` repeats what its `Commit` holds

*consensus/driver.go, node/node.go: ActionDecided. Needless complexity. Size: about 5 lines.*

**Fixed** in `42058b0`. ActionDecided holds only its Commit, and Node.commit reads
decision.Commit.BlockHash and Round.

ActionDecided carries Height, Round and BlockHash next to a Commit that holds the same three values,
built from the same variables. Node.commit reads BlockHash, Round and Commit, and never reads
Height.

**Suggestion.** Keep only Commit, and have Node.commit read decision.Commit.BlockHash and
decision.Commit.Round. About 5 lines, plus the tests that read the fields.

**Checked.** Read the decide rule and Node.commit.

### C54. The ML-DSA-65 sizes are written in three forms outside `signing`

*ledger/genesis.go, ledger/name_request.go, cmd/server/config.go, ui/wallet.go: ValidateGenesis key check, publicKeyLength, signatureLength, typedKey. Duplicated rule. Size: about 12 lines net.*

**Fixed** in `cdfc389` and `5351e48`. signing exports PublicKeyBase64Length, SignatureBase64Length
and ValidPublicKeyBase64 (exact decoded size and canonical spelling). ledger, cmd/server and the
wallet use them, and no package but signing imports crypto/mldsa. cmd/server's separate seed-length
check went with it: NewSigner refuses a wrong seed, with a message that says what privateKey takes,
but now only after the genesis file is read.

The ML-DSA-65 sizes are written in three forms.

- ledger/genesis.go imports crypto/mldsa and checks the decoded size and CanonicalBase64;
  ui/wallet.go's typedKey repeats that check, and cmd/server/config.go checks the decoded sizes
  again.
- ledger/name_request.go hard-codes the base64 lengths 2604 and 4412, and reassignment.go reuses
  them.

So the ledger reaches under signing, the package meant to be the one place that knows the scheme.

**Suggestion.** Export from signing the two base64 lengths and one ValidPublicKeyBase64(key) bool.
Use them in ledger, cmd/server and ui, and drop ledger's crypto/mldsa import. About 12 lines net.

**Checked.** grep for mldsa. and the two constants.

### C55. The genesis parameter keys work only because JSON ignores case

*ledger/genesis.go: genesisParameterNames, ParseGenesis. Other. Size: 2 comment lines.*

**Fixed** in `69b1e12`. Two comment lines say why the keys fill untagged fields, and that snapshots
and checkpoints write the Go names.

The genesis file's parameters use camelCase keys ('transactionCost'), but LedgerParameters has no
JSON tags (spec 0.2). The keys fill the fields only because encoding/json matches keys without
regard to case. Snapshots and checkpoints write the Go field names. No comment says this.

**Suggestion.** Add one comment on genesisParameterNames saying that encoding/json matches these
keys without regard to case, that the check above refuses any other spelling, and that snapshots and
checkpoints write the Go names. 2 lines.

**Checked.** LedgerParameters has no tags. ParseGenesis checks the exact key names before it
decodes.

### C56. `powerBeforeFreezes` reads as "before this block's freezes"

*ledger/block.go, ledger/validators.go: powerBeforeFreezes (local in ProposeBlock, parameter of withinOneThirdCap). Misleading name. Size: 3 renames.*

**Fixed** in `bf684b1`. Renamed powerIgnoringFreezes.

The value is powerIgnoringFreezes(...), the set as if no account were frozen. But 'before freezes'
reads as 'before this block's freezes', which would wrongly exclude names frozen earlier. One
concept has two names a few lines apart.

**Suggestion.** Name the variable and the parameter powerIgnoringFreezes. 3 occurrences.

**Checked.** Read ProposeBlock and withinOneThirdCap.

### C57. `fastForward` keeps the abandoned height's blocks in memory

*node/node.go: fastForward. Inconsistency. Size: 2 lines.*

**Fixed** in `8a100c2`. fastForward resets node.blocks and node.lastProposal.
TestCatchUpDropsTheBlocksOfTheHeightItLeaves.

commit resets node.blocks with the other per-height maps. fastForward resets heightVotes,
heightProposals and invalidProposals, but not node.blocks, and it keeps node.lastProposal. After a
catch-up, the abandoned height's blocks, with their payments and nonces, stay in memory until the
next commit. Nothing reads them: commit checks block.Height, and sendLastCommit checks
lastProposal's height.

**Suggestion.** Add node.blocks = map[[32]byte]ledger.Block{} and node.lastProposal =
ProposalMessage{} to fastForward. 2 lines.

**Checked.** Read fastForward, commit and sendLastCommit. onProposal stores blocks only for the
height in progress, so the reset loses nothing.

### C58. `relaySeen` decides only for certificates

*node/relay.go: Node.relay, Node.relaySeen. Duplicated rule. Size: 2 comment lines, or about 20.*

**Fixed** in `523d81d` and `3474363`. Comments only: relay's and relaySeen's say the mark decides
only for certificates and holds reportEquivocation's marks; addParameterVotes' says relay sends a
change's certificate once per height.

relay promises to forward a message only the first time the node sees it, but every caller except
the certificate handlers already guarantees that. onProposal takes one proposal per round,
rememberVote keeps a vote once, and the three admit functions relay only what their pool newly took.
The mark decides only for certificates, where addParameterVotes and addReassignmentVotes also return
true for a replacement. relaySeen also holds reportEquivocation's marks.

**Suggestion.** Say so in relay's comment and the field's comment (2 lines). Or keep the mark only
for the two certificate types and give the equivocation marks a map of their own, which removes five
of messageKey's seven cases (about 20 lines).

**Checked.** Traced every relay caller. The read-through's panic probe agrees.

### C59. `HTTPTransport.post` has two 5-second timeouts

*node/httptransport.go: HTTPTransport.post. Needless complexity. Size: about 6 lines fewer.*

**Fixed** in `e9a2dda`. post calls client.Post and relies on the client's timeout.

Each POST has two 5-second timeouts: one on the request's context, and the http.Client's own
Timeout, which already bounds the whole exchange.

**Suggestion.** Call httpTransport.client.Post(endpointURL, "application/json",
bytes.NewReader(body)) and close the body. About 6 lines fewer, and the context import goes.

**Checked.** NewHTTPTransport sets http.Client{Timeout: 5 * time.Second}.

### C60. `Neighbors` checks three things that cannot happen

*node/topology.go: Neighbors, indexToCoordinates. Dead code. Size: about 4 lines.*

**Fixed** in `2968e38`. The seen map and the two conditions are gone; a scratch comparison of the
old and new Neighbors over 4,110,600 cases found them equal. indexToCoordinates' comment says what
it computes.

Neighbors keeps a seen map and tests 'j >= 0' and 'j != selfIndex', but none of these can fire. Each
neighbour changes one digit of the node's index in base groupSize to a different value, and
groupSize^dimensions >= participants. So every index is distinct, non-negative, and never the node's
own. Only 'j < participantCount' matters. indexToCoordinates' comment cites 'the original
calculateAddress', a function of the Java project the reader does not have.

**Suggestion.** Drop the map and the two conditions, about 4 lines. Describe the mapping in plain
words: 'the digits of index in base groupSize, lowest first'.

**Checked.** Mixed-radix digits are unique when every digit is below groupSize, and GroupParameters
picks groupSize = ceil(participants^(1/d)).

### C61. Eight functions recompute the validator set the view already holds

*node/governance.go, node/reassignment.go, node/peerdns.go, node/node.go: the ValidatorRule.SetFor(node.state) calls. Needless complexity. Size: 8 lines.*

**Fixed** in `6a936a0`. The eight places read node.view.Load().validators; none runs between a state
change and publishView. A test that changes the state by hand now calls publishView, as a commit
does.

Eight functions recompute the validator set of the height in progress from node.state:
addParameterVotes, pendingParameterChange, governanceView, addReassignmentVotes,
pendingReassignment, resolvedPeerAddresses, startHeight and startFirstHeight.
node.view.Load().validators holds that same set, because publishView stores it after Open, each
commit and each fast-forward, before any of these run. In dynamic mode each call walks every
account.

**Suggestion.** Read node.view.Load().validators in those 8 places, one line each. Keep the calls in
publishView, commit (it runs before publishView) and loadSignLog.

**Checked.** Checked that none of the 8 runs between 'node.state =' and publishView in commit,
fastForward or Open.

### C62. Three guards against zero timings never fire

*node/node.go, node/sync.go: Node.stepTimeout, Node.blockInterval, Node.reGossipInterval. Dead code. Size: 2 lines, or 6.*

**Fixed** in `dd636d0`. blockInterval's guard is gone; the other two stay, for library callers.

The three guards against StepTimeoutMs <= 0 or BlockIntervalMs <= 0 never fire for a genesis that
passed ValidateGenesis or for a voted change (TimingConsistent refuses both). blockInterval's guard
is redundant in every case, since commit floors the interval at 1 ms. node.Open itself does not
validate Config.Genesis, though. Without its guard, reGossipInterval would make time.NewTicker panic
on a library caller's zero step timeout.

**Suggestion.** Drop blockInterval's guard (2 lines). Dropping the other two (about 6 lines) relies
on every caller validating the genesis, as cmd/server and cmd/demo do.

**Checked.** Read the three functions, TimingConsistent, Open and the testGenesis helper (no node
test passes a zero step timeout).

### C63. "Pending outgoing" is computed three times under two names

*node/node.go, node/requestpool.go: admitPayment, admitNameRequest, reconcilePools. Duplicated rule. Size: about 6 lines.*

**Fixed** in `a69a30a`. Node.pendingOutgoing(payerName) serves admitPayment and admitNameRequest,
and reconcilePools' map is pendingOutgoingSoFar.

Spec 6.5's 'pending outgoing' (pooled payments plus pooled fees) is written out three times under
two names: promised in admitPayment, pendingOutgoing in admitNameRequest, and a running map named
promised in reconcilePools.

**Suggestion.** Add node.pendingOutgoing(payerName), returning mempool.outgoingFor plus
requests.feesOf. Use it in the two admit functions, and rename reconcilePools' map. About 4 lines
added and 2 removed.

**Checked.** Read the three functions.

### C64. The startup log's `validators=` is the total power

*cmd/server/main.go: validatorsLabel (the startup log). Misleading name. Size: 1 line.*

**Fixed** in `22ec327`. The log prints validators=<count> validatorPower=<total> in static mode.

The startup log prints validators=%s. In static mode the value is the validators' total power, not
their count, so four validators with powers 1, 1, 1 and 2 are logged as validators=5.

**Suggestion.** Log validatorPower=%s, or both the count and the power. 1 line.

**Checked.** Read validatorsLabel.

### C65. `adminPageData.PublicKey` is never shown

*ui/main.go: adminPageData.PublicKey. Dead code. Size: 2 lines.*

**Fixed** in `3126e89`. Deleted.

renderAdmin sets data.PublicKey, but admin.html never reads .PublicKey.

**Suggestion.** Delete the field and the assignment. 2 lines.

**Checked.** grep of ui/templates: .PublicKey appears only in wallet.html and requests.html.

### C66. A nil fee panics in `NameRequestFitsState`; only the wallet's own check stops it

*ui/requests.go, ledger/name_request.go: Wallet.SignNameRequest (the request.Fee == nil case), NameRequestFitsState. Other. Size: 1 line.*

**Fixed** in `13e7c9c`. NameRequestFitsState compares the nil-safe fee(), with a test for a null
fee.

The Fee == nil case in SignNameRequest reads like a friendly message, but it also prevents a crash.
NameRequestFitsState compares request.Fee.Cmp(...) directly, not through the nil-safe request.fee().
SignNameRequest is the only caller that runs NameRequestFitsState before the shape check. net/http
recovers the panic, so the effect is a dropped request, not a crashed wallet.

**Suggestion.** Add a one-line comment on the Fee case, or compare request.fee() in
NameRequestFitsState. 1 line either way.

**Checked.** Read both functions. Every node path runs NameRequestShapeError first.

### C67. Two wallet functions each GET `/hdls/status`

*ui/wallet.go, ui/trust.go: Wallet.height, Wallet.servedNetworkID. Needless complexity. Size: about 8 lines fewer.*

**Fixed** in `82ceab5`. One Wallet.status() serves Wallet.height and servedNetworkID.
TestWalletBoundsTheStatusItReads.

Each function GETs /hdls/status and decodes one field into its own anonymous struct. Neither checks
the status code, while postJSON and Send do.

**Suggestion.** Write one status() that GETs /hdls/status, refuses a status of 300 or more, reads
through a LimitReader, and returns the height and the network id. height keeps its guard against a
height that jumps. About 8 lines fewer.

**Checked.** Read both functions.

### C68. `incomplete`'s message speaks of a new key for parameter certificates too

*ui/admin.go: incomplete. Misleading comment. Size: 2 to 3 lines.*

**Fixed** in `0362e8c`. incomplete takes its second sentence from the caller; a parameter
certificate names a moved sequence or digest, or values out of bounds.
TestParameterVotesForAMovedSequenceNameTheirCause.

incomplete serves both SubmitParameters and SubmitReassignment. When the power is enough, it blames
a changed state 'or the new key's proof'. A parameter certificate has no new key; its real causes
are a moved sequence or digest, or values out of bounds.

**Suggestion.** Pass the second sentence in as an argument, or give SubmitParameters its own
message. 2 to 3 lines.

**Checked.** Read incomplete and its two callers.

### C69. `pageData.PublicKey`'s comment

*ui/main.go: pageData.PublicKey (comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `a4f2e48`. The comment says render finds the wallet's name by the key.

The comment says the key is 'for its copy button only', but render also compares it with each
account's PublicKey and PendingKey to find the wallet's name.

**Suggestion.** 'the wallet's key in full: render finds the wallet's name by it, and the copy button
copies it'. 1 line.

**Checked.** Read render.

### C70. `/hdls/governance` lists pooled certificates in random order

*node/governance.go: governanceView. Inconsistency. Size: about 4 lines.*

**Fixed** in `3f1c7fe`. governanceView lists both kinds in key order.
TestGovernanceViewListsCertificatesInKeyOrder.

It ranges over the parameterProposals and reassignmentProposals maps, so /hdls/governance lists
pooled certificates in random order. pendingParameterChange and pendingReassignment sort their keys.

**Suggestion.** Iterate slices.Sorted(maps.Keys(...)) in both loops. About 4 lines.

**Checked.** Read governanceView. From S11's notes.

### C71. `hashFields`' comment names two of its four callers

*ledger/payment.go: hashFields (comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `4e61245`. Names the four callers.

The comment says it is 'Shared by PaymentID and UpdateID'. NameRequestID and ReassignmentID use it
too.

**Suggestion.** 'Shared by PaymentID, UpdateID, NameRequestID and ReassignmentID.' 1 line.

**Checked.** grep for 'hashFields(' gives four callers. From S11's notes.

### C72. `ParseAmount`'s comment still mentions the node config

*ledger/amount.go: ParseAmount (comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `7639bd9`. Names the genesis file and name requests.

The comment lists amounts read 'in JSON (payments, accounts, checkpoints, snapshots, parameters,
reassignments) and in a node's config'. Since S8 the node config holds no amount; amounts come from
the genesis file. Name requests' fees are missing from the list.

**Suggestion.** 'in JSON (..., reassignments, name requests) and in the genesis file'. 1 line.

**Checked.** Read the comment and cmd/server's fileConfig. From S11's notes.

### C73. The `ledger` package comment leaves out name requests and evidence

*ledger/ledger.go: package ledger (doc comment). Misleading comment. Size: 1 comment line.*

**Fixed** in `90bfa37`. Lists name requests and double-sign evidence.

The doc says the package holds 'the rules for applying a batch of payments, account updates and
governance'. It leaves out name requests and double-sign evidence, which ApplyBlock also applies.

**Suggestion.** Add 'name requests, double-sign evidence' to the list. 1 line.

**Checked.** Read applyContent. From S11's notes.

### C74. "height" means two things on the wire

*node/node.go, node/httptransport.go: Snapshot.Height and the status 'height', ResolveRecord.Height. Inconsistency. Size: none, or about 4 lines at the next genesis.*

**Fixed** in `11875f9`. The owner chose the rename: /hdls/status's key is heightInProgress, read by
catch-up's fetchHeight, the wallet and docs/api.md. /hdls/resolve keeps height, the committed
height.

The wire uses 'height' in two senses. /hdls/status serves the height in progress (committed + 1),
and /hdls/resolve serves the committed height. docs/api.md spells this out.

**Suggestion.** Leave it, as documented. Or rename the status key to heightInProgress at the next
genesis, about 4 lines across node and ui.

**Checked.** Read handleStatus and handleResolve. From S11's notes.

### C75. Three small names from Phase 1

*node/node.go, consensus/driver.go: Node.commit ('block, valid :='), enqueue(event event), handle(event event). Misleading name. Size: 1 to 3 lines each.*

**Fixed** in `e94219e` (and `a2b2a60` for Driver.Start). Node.commit's flag is found, and enqueue's
and handle's parameter is incoming.

Already listed in the progress note under 'In no slice'. In Node.commit, a map lookup's found flag
is named valid. enqueue and handle name their parameter after its type.

**Suggestion.** As the progress note proposes: 1 to 3 lines each.

**Checked.** Read Node.commit, enqueue and handle. From S11's notes.

## Rejected

The verify agent rejected 2 notes:

- Guide writer: drop the key self-test signature and the keySelfTest constant in
  cmd/server/config.go load, because the derive-and-compare already proves the pair and the
  'key-self-test' domain is not listed in signing/domain.go.
  Why not: Spec 8.2 asks load to derive the key, refuse a mismatch, and make one test signature
  (E26). The comparison proves the pair belongs together; the signature proves the build can sign
  and verify. The domain is tagged and the signature is never sent. domain.go's rule ('a new
  constant for each new signable payload type') is about payload types that travel. The node
  read-through also keeps the test signature.

- S11, lastProposal and sendLastCommit: as an alternative to rewording spec §3.6, have
  sendLastCommit re-send the last commit without the block.
  Why not: sendLastCommit exists for a node that lost every copy of the decided proposal while the
  others decided without it. Without the block, that node still cannot decide. The part of the note
  that holds, that the code comment repeats the 'idle chain keeps no block' claim, is merged into
  the confirmed note on Node.commit.

The guide's reports, its two checks, the fix round, the recheck, the three read-throughs and the
verify stage's full output are in `review/_evidence_phase2/S12_reports.json`. Each note's Checked
line says which notes it merges.
