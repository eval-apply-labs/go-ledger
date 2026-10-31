# Reading the code

This guide takes a reader who has never seen the code through all of it, one step at a time. The
steps follow the four areas of the docs (signature, node, API/CDN, UI) and the order in which the
packages import each other, so each step needs only the steps before it. The last step puts the
pieces together: it follows one payment, one freeze and one ⅔ name action through every function
they pass.

Each step gives what it is for, its files in reading order, a table from each concept to the type or
function that holds it, the facts later steps take as given, and how to check it: the tests to read
and the command that runs them. The guide says where things are. Why they are so is in the area
docs each step links; [README.md](README.md) is their index. Code is named by package, file and
function or type, never by line number.

## Before you start

- **Size.** About 10,100 lines of Go in 55 production files, 449 template lines, and about 16,700
  lines of tests.
- **Go.** Go 1.27, standard library only. `go.mod` names the toolchain `go1.27.3`; with
  `GOTOOLCHAIN=auto` the `go` command fetches it.
- **Two modules.** The root holds `signing`, `consensus`, `ledger`, `node` and `cmd/`. The wallet,
  `ui/`, is a module of its own that imports the root one.
- **Tests.** A test file ends in `_test.go` and sits beside its code. `go test ./ledger -run
  'regex'` runs the tests of one package whose names match; `-v` lists them. A package compiles as
  a whole, so a step's command also compiles the later files of its package, but never a later
  package: each command below was run on a copy of the tree that held only the packages up to its
  step. For the wallet, `-C ui` must be the first flag. `-short` skips the chaos scenarios; `-race`
  adds the race detector, as CI runs it.
- **Go idioms.** A line that starts with **Go:** explains one where it first appears.
- **Words.** [glossary.md](glossary.md) explains the project's words (a seated validator, the ⅔,
  the three pins) and the ids the docs cite, such as R9, rule 17 or spec §5.3.

## The map

| Area | Package | Steps | Production lines | Test lines |
|---|---|---|---|---|
| signature | `signing`, `cmd/keygen` | 1 | 263 + 26 | 327 |
| node | `consensus` | 2, 9 | 1,036 | 1,627 |
| node | `ledger` | 3 to 8 | 2,604 | 3,711 |
| node | `node`, but its HTTP side, relay and peers | 10 to 16 | 3,564, both sides | 7,685 |
| API/CDN | `node`'s HTTP side, relay and peers; `cmd/server`, `cmd/demo` | 17 to 19 | 224 + 218 | 349 + 110 |
| UI | `ui` | 20 to 22 | 2,171, and 449 in `ui/templates` | 2,927 |

The imports run `signing` → `consensus` → `ledger` → `node` → `cmd/server`, `cmd/demo` and `ui`;
`cmd/keygen` imports only `signing`. `ledger` imports the consensus types (the validator set, votes,
proposals, evidence), never the driver. The diagram is in
[architecture.md](architecture.md#packages-and-areas). The last section lists every production file
and its step.

## Signature area

### Step 1. Signatures and keys

- **For:** every statement, a payment, a vote, a name request, is signed through this package. It
  fixes what a signature binds, and that there is one way to sign. The sign log, which keeps a node
  from signing twice, belongs to this area but lives in `node/persist.go`: step 14. Why:
  [signature.md](signature.md).
- **Files:** `signing/scheme.go`, `signing/mldsa65.go`, `signing/domain.go`, `signing/signer.go`,
  `signing/fips140v1.go`, `cmd/keygen/main.go`.

| Concept | Where |
|---|---|
| A signature scheme | `signing.Scheme` (`GenerateKey`, `ParsePrivateKey`, `Verify`), `signing.PrivateKey` (`PublicKey`, `Sign`) |
| ML-DSA-65, the only scheme; no build without it | `signing.MLDSA65` (a parsed key is an `mldsa65Key`); `signing/fips140v1.go` |
| Sizes checked before any work; deterministic signing | `MLDSA65.Verify`; `mldsa65Key.Sign` (`SignDeterministic`, no context string) |
| The purpose of a signature | `signing.Domain` and its 14 `Domain…` constants |
| The bytes actually signed | `signing.Preimage`: the tag `hypernode/v1`, the network id, the domain, each field, each after its 4-byte length |
| The one signing API, with no raw-bytes call | `signing.Signer`: `NewSigner`, `Sign`, `SignBase64`, `PublicKeyBase64` |
| Checking a signature | `signing.Verify`, `signing.VerifyBase64` |
| One spelling per key and signature | `signing.CanonicalBase64`; `ValidPublicKeyBase64`, a public key's size and spelling; the base64 lengths `PublicKeyBase64Length` and `SignatureBase64Length` |
| A fresh key pair | `cmd/keygen` `main` → `MLDSA65.GenerateKey` |

**Go:** a name that starts with a capital letter is exported: other packages can use it. An
interface lists methods; any type that has them satisfies it, with no declaration. The code says so
anyway, on the type's line (`type MLDSA65 struct{} // implements Scheme`), and a line such as
`var _ Scheme = MLDSA65{}` makes the compiler check the claim: it does not build unless the type has
every method, with the same signature. `_` discards the value. For a type whose methods take a
pointer receiver, the line reads `var _ Transport = (*HTTPTransport)(nil)`, a nil pointer of the type.
The interface names its implementations in turn
(`type Scheme interface { // implemented by MLDSA65`), with the test doubles in its comment. The
compiler does not check that direction: a new implementation must be added to the list by hand.
`func (signer *Signer) Sign(…)` is a method, and `signer`, its receiver, is the value it is called
on. `*Signer` is a pointer to a `Signer`, so the method works on the caller's value, not on a copy.
`&value` takes a value's address; `*pointer` reads the value it points to. `fields ...[]byte` takes
any number of arguments as one slice. `write := func(component []byte) {…}` in `Preimage` is a
closure: a function value that uses, and here changes, the variables around it (`preimage`).
`//go:build fips140v1.0` compiles a file only in that build, where its `var _ int = "…"` is a type
error on purpose.

**Later steps take as given:**
- A signature verifies only for the same network id, domain and fields, in the same order. Each
  message type lists its signed fields once: a `signFields` or `voteFields` method, or the function
  `parameterVoteFields` for a parameter change. Each signer signs them in the domain of its role; a
  name request (step 5) and a ⅔ name action (step 6) have more than one role.
- Keys and signatures are standard base64 in one spelling: a public key is 1952 bytes (2604
  characters), a signature 3309 (4412), a private key the 32-byte seed (44).
- Signing is deterministic, and code outside `signing` signs only through a `Signer`, built for one
  network id.

**Check.** Read `signing/signing_test.go`: `TestCrossDomainSignatureRejected`,
`TestCrossNetworkSignatureRejected`, `TestFieldInjectionResisted`,
`TestVerifyBase64AcceptsOnlyTheCanonicalKeySpelling`, `TestSignaturesAreDeterministic`,
`TestVerifyRefusesWrongSizesBeforeAnyWork`. Run `go test ./signing/` and `go run ./cmd/keygen`.

## Node area

### Step 2. The consensus vocabulary

- **For:** the types the ledger and the node use to say who signs a block. The round driver that
  uses them is step 9. Why: [consensus.md](consensus.md#the-rule),
  [consensus.md](consensus.md#what-is-signed).
- **Files:** `consensus/errors.go` (the package comment), `validator.go`, `vote.go`, `commit.go`,
  `proposal.go`, `evidence.go`, `voteset.go`. The package comment also covers the round driver,
  `driver.go`, which is step 9; only gossip and the timers are in `node`.

| Concept | Where |
|---|---|
| A seated name, its key and its power | `consensus.Validator` |
| The set: sorted by name, one entry per name, power above 0 and capped at 2^40 | `ValidatorSet`, `NewValidatorSet`, `MaxValidatorPower` |
| More than ⅔ of the power; more than ⅓ | `ValidatorSet.Quorum` (floor(2 × total / 3) + 1); `SkipThreshold` |
| A name's key and power in the set | `ValidatorSet.PublicKey`, `Power`, `Contains`, `Validators`, `TotalPower` |
| Who proposes at a height and round | `ValidatorSet.ProposerAt`: power-weighted, deterministic |
| A vote | `Vote` (height, round, `Prevote` or `Precommit`, block hash, the signer's name, signature); `SignVote`, `Vote.Verify`; domain `block-revision` |
| The proof a block was decided | `Commit`, `Commit.Verify` |
| The > ⅔ certificate rule, written once for a commit, a parameter change, a ⅔ name action and the wallet | `CertificateTally`: `NewCertificateTally`, `Add` (one signer, with a callback that checks its signature), `Power`, `Reached` |
| A proposer's signed proposal | `SignedProposal`, `SignProposal`, `SignedProposal.Verify`; domain `proposal` |
| A double vote; a double proposal | `VoteEvidence`, `VoteEvidence.Valid`; `ProposalEvidence`, `ProposalEvidence.Valid` |
| The votes of one height, round and type | `VoteSet`: `Add`, `TwoThirdsMajority`, `HasQuorumFor`, `HasQuorumAny`, `TotalVoted`, `Commit` |

**Go:** `_, found := validatorSet.byName[name]` in `ValidatorSet.Contains` reads a map: `found` says
whether the key was there, and `_` discards the value. A missing key gives the zero value, so
`Power` of an unknown name is 0. `append([]Validator(nil), validatorSet.validators...)` in
`Validators` copies a slice into new storage, so the caller cannot change the set. `[32]byte` is a
fixed-size array, copied by value and compared with `==`.

**Later steps take as given:**
- Votes, proposals and governance votes name their signer and carry no key. The key comes from the
  validator set, and the set from the state (step 7).
- `Commit.Verify` passes only for precommits of one height, round and block hash, from distinct
  members, each under the key the set holds for it, with power at least the quorum. One vote that
  does not count makes the whole commit invalid.
- A `VoteSet` counts a validator once; a second, different vote un-counts it and becomes evidence.
  Two signatures by one name for two block hashes in one slot are a proof any node can check later.

**Check.** Read `consensus/consensus_test.go`, `power_test.go`, `proposal_test.go`,
`proposer_test.go`, and `TestADifferentValidRoundIsNotEvidence` in `evidence_test.go`. Run (18
tests):
`go test ./consensus -run '^Test(QuorumIsTwoThirdsPlusOne|VoteSet|WrongVote|Equivocation|Commit|CertificateTally|Conflicting|VoteCounts|ValidatorSet|SignedProposal|Proposer|ADifferentValidRound)'`

### Step 3. Amounts, names and the state

- **For:** the values everything else is made of: money, names, accounts, the ten parameters, and
  the state with its digest. Why: [architecture.md](architecture.md#the-state),
  [governance.md](governance.md#the-parameters).
- **Files:** `ledger/amount.go`, `ledger/name.go`, `ledger/ledger.go`, `ledger/digest.go`,
  `ledger/parameters.go`, `ledger/topology.go`. In amount.go, the JSON methods of `Account`,
  `Payment`, `Reassignment` and `LedgerParameters` follow the pattern in this step's Go note;
  `Payment`'s pair is for step 4 and `Reassignment`'s for step 6.

| Concept | Where |
|---|---|
| An amount: exact, a whole number of 0.00000001 units, a string in JSON | `*big.Rat`; `ParseAmount` (the one parser), `FormatAmount` (the one written form); `amountJSON`, `requireAmounts` |
| A name's one spelling | `CanonicalName` |
| An account, and finding one by name | `Account` (`Name`, `PublicKey`, `Amount`, `Bank`, `Delegate`, `Description`, `Address`, `Frozen`, `PendingKey`, `PendingBank`, `PendingHeight`); `FindAccount`, `findAccountIndex` (binary search); `AccountAmount`, a name's balance or zero |
| The committed state, its accounts in strictly ascending name order | `LedgerState` (`Accounts`, `Parameters`, `GovernanceSequence`, `Height`), `LedgerState.NamesAscending` |
| The state digest | `LedgerState.Digest`, `AccountsDigest`, `LedgerParameters.Digest`, `lengthPrefixedDigest` |
| The ten parameters, their defaults and bounds | `LedgerParameters`, `DefaultParameters`, `Acceptable`, `TimingConsistent`, and the `Min…`/`Max…` constants of parameters.go |
| The gossip dimension a step timeout must cover | `GroupParameters` (topology.go), `AssumedGossipHopMs` |

**Go:** `type plain Account` has `Account`'s fields and none of its methods. encoding/json calls a
type's own `MarshalJSON`, so amount.go encodes through `plain` to avoid calling itself forever. A
field written as a bare type is embedded: its fields are promoted into the outer struct. A `switch`
with no value, as in `Acceptable`, tests each `case` condition in turn and runs the first that holds.

**Later steps take as given:**
- Every amount is read through `ParseAmount`: no sign, exponent or a/b, at most 8 decimals. An
  account's amount is never nil.
- The ledger never changes an amount in place; it makes a new `big.Rat`. The node relies on this to
  share accounts across goroutines (steps 10 and 11).
- Accounts are kept in strictly ascending name order. One state digest means one list of accounts,
  in that order, one set of parameters, one governance sequence and one height.
- The parameters in force passed `Acceptable` for the validator count when they were set (the
  genesis, step 7; every change, step 6). The bounds do not depend on the set, so they always
  hold: no amount is nil and the step timeout is above zero. The timing check depends on the count,
  and is not run again when the set grows.

**Check.** Read `ledger/amount_test.go`, `name_test.go`, `digest_test.go`, and the parameter tests
the command names in `governance_test.go` and `messagesize_test.go`. Four amount tests
(`TestAmountDecodingRejectsExponentAndOffGridInputs`, `TestAmountsTravelAsPlainDecimals`,
`TestRequiredAmountRefusesNull`, `TestAmountJSONRoundTrips`) also use a `Payment`, a
`ParameterChange` or a `Reassignment` (steps 4 and 6), and `TestAmountsTravelAsPlainDecimals` a
`Block` (step 8); `TestEveryParameterIsInTheDigest` checks a parameter vote (step 6). Run (17 tests):
`go test ./ledger -run '^Test(ParseAmount|Amount|FormatAmount|RequiredAmount|CanonicalName|AccountsDigest|StateDigest|NamesAscending|EveryParameterIsInTheDigest|AcceptableEnforces|MaxMessageBytesGovernanceFloor)'`

### Step 4. Payments, updates and receipts

- **For:** the two messages a name signs on its own, and a node's acknowledgement of a payment.
  Why: [architecture.md](architecture.md#what-changes-the-state).
- **Files:** `ledger/payment.go`, `ledger/account_update.go`, `ledger/receipt.go`.

| Concept | Where |
|---|---|
| A payment | `Payment`: `From`, `To`, `Amount`, `Comment`, `BlockID`, `PreviousStateDigest`, `Signature` |
| Signing and checking it | `Payment.signFields` (domain `payment`), `SignPayment`; `VerifyPayment`, under the key the caller takes from the state for `From` |
| Its identity: its content, not its signature | `PaymentID`, `hashFields` |
| Moving the money of a batch | `ProcessPayments`, `addToTotal` |
| An account update | `AccountUpdate`, `SignAccountUpdate`, `VerifyAccountUpdate`, `UpdateID`, `ClearField` (`-`); `applyAccountUpdates`, `applyField` |
| A node's receipt | `Receipt`, `SignReceipt`, `Receipt.Verify`; domain `receipt` |

**Later steps take as given:**
- A payment is from a name to a name; only the holder of the payer's current key can sign it. The
  payer pays `Amount`, the payee gets `Amount` less `TransactionCost`, and the cost is burned. It
  applies only in the block its `BlockID` names (step 8), the replay guard.
- One signed content is one payment (`PaymentID`): nodes dedup, relay and acknowledge by it.
- `ProcessPayments` moves value only for an amount of at least the cost between two existing names
  (audit B1, B2). A payer whose payments in one block exceed its balance loses that balance and
  those payments; honest intake (step 11) never lets that happen.
- In an update, `""` leaves a field and `-` clears it.

**Check.** Read `ledger/ledger_test.go`, `malleability_test.go`, `conservation_test.go`, and the
tests the command names in `payment_test.go` and `account_update_test.go`. Run (16 tests):
`go test ./ledger -run '^Test(VerifyPayment|PaymentSignatureCoversBothNames|PaymentIDIgnoresSignature|MalleatedDuplicateDebitsOnce|.*DoesNotMint|PaymentToANameWithNoAccount|OverdrawBan|NilTransactionCost|NormalPaymentStillWorks|SupplyIsNeverMinted|UpdateAndReceiptSignatures)'`

### Step 5. Name requests

- **For:** how a name is registered, moved to a new key or bank, and frozen by its holder. Why:
  [architecture.md](architecture.md#what-changes-the-state),
  [security.md](security.md#banks-and-their-co-signatures).
- **Files:** `ledger/name_request.go`.

| Concept | Where |
|---|---|
| The four kinds | `RegistrationKind`, `KeyChangeKind`, `BankChangeKind`, `FreezeKind` |
| A request, and the roles its kind uses | `NameRequest` (`Kind`, `Name`, `Requestor`, `NewKey`, `Bank`, `Fee`, `BlockID`, three signatures); `usesNewKey`, `usesBank`, `fee` |
| Who signs, in which domain | the requestor in its kind's domain (`requestorDomains`), the bank in `name-request-bank`, the new key in `name-request-new-key` |
| Signing and checking the roles | `SignAsRequestor`, `SignAsBank`, `SignAsNewKey`; `VerifyNameRequest` |
| The shape, checked first | `NameRequestShapeError`, with `signing.PublicKeyBase64Length` and `SignatureBase64Length` |
| The rules of each kind against a state | `NameRequestFitsState`, `keyInUse`, `hasPendingChange` |
| The one fee rule | `FeesFit`: fees within the balance, and no payment by that payer in that block |
| A request's identity | `NameRequestID` |
| Applying requests | `applyNameRequests`, `freezeNames`, `applyDueChanges`, `clearPendingChange` |

**Later steps take as given:**
- Every signature on a request covers the same fields: all but the signatures.
- A registration creates the account at once, with a zero balance and its bank as bank of record.
  A key or bank change waits `ChangeDelayBlocks` as the pending change. A freeze is free and
  immediate.
- A seated name files no key or bank change.
- A freeze keeps the balance (R5) and clears the name's pending change and the changes it co-signed
  (R11). A due change lapses if its name is seated then, or its new key is another name's by then.

**Check.** Read the four tests the command names in `ledger/name_request_test.go`; the file's
other tests build blocks, so read them after step 8. Run:
`go test ./ledger -run '^Test(NameRequestShapeIsCheckedFirst|NameRequestWithANullFeeIsRefusedAgainstTheState|RequestorSignsInTheDomainOfItsKind|NameRequestFeeTravelsAsPlainDecimal)$'`

### Step 6. The ⅔ certificates

- **For:** the two decisions the validators take together: a parameter change and the ⅔ name
  action. Why: [governance.md](governance.md), [managing-the-currency.md](managing-the-currency.md).
- **Files:** `ledger/governance.go`, `ledger/reassignment.go`.

| Concept | Where |
|---|---|
| A parameter change and its votes | `ParameterChange` (`NewParameters`, `PreviousParametersDigest`, `Sequence`, `Votes`), `ParameterVote` |
| One vote | `SignParameterVote`, `VerifyParameterVote`, `parameterVoteFields`; domain `parameter-vote` |
| A ratified change | `ParameterChange.Valid` |
| The ⅔ name action | `Reassignment` (`Name`, `PreviousAmount`, `NewAmount`, `NewKey`, `NewKeySignature`, `Bank`, `Freeze`, `Unfreeze`, `Sequence`, `Votes`), `ReassignmentVote` |
| What its votes and the new key sign | `Reassignment.voteFields`, `flag`; `SignReassignmentVote`, `VerifyReassignmentVote`, `SignReassignmentNewKey`, `VerifyReassignmentNewKey` |
| Its identity | `ReassignmentID` |
| Its rules, all but signatures; a ratified action | `Reassignment.FitsState`; `Reassignment.Valid` |
| Applying it, all but a freeze | `applyReassignment` |

**Later steps take as given:**
- A certificate is valid only if every vote is from a distinct name of the current set and verifies
  under the set's key, and their power reaches the quorum. One bad vote makes the whole certificate
  invalid, so the node drops the votes of names that left the set first (step 13).
- Both kinds bind the governance sequence, and applying one moves it on: each certificate applies
  once. A parameter change also binds the parameters in force, and its new ones pass `Acceptable`.
- The name action binds the name, not its key, and clears the name's pending change (R7). It is the
  one way to create money.

**Check.** Read the four tests the command names, in `ledger/governance_test.go`,
`reassignment_test.go` and `amount_test.go`. Run:
`go test ./ledger -run '^Test(ParameterChangeEnforcesTimingCoupling|ParameterChangeRejectsChainHaltingTimings|ReassignmentSequenceGuardBlocksCycleReplay|SignedFieldsCarryPlainDecimals)$'`

### Step 7. Validators and the genesis

- **For:** how the set that signs a block follows from the state, and how a network starts. The
  genesis comes here, not with the parameters, because `ValidateGenesis` returns a `Chain` and seats
  the first set with `ValidatorRule.SetFor`. Why:
  [operating-a-network.md](operating-a-network.md#the-genesis-file),
  [security.md](security.md#the-network-id).
- **Files:** `ledger/validators.go`, `ledger/genesis.go`.

| Concept | Where |
|---|---|
| The fixed facts of one chain | `Chain`: `Scheme`, `NetworkID`, `GenesisHash`, `ValidatorRule` |
| How the set follows from the state | `ValidatorRule` (`StaticValidators`, or a stake `Threshold`), `StaticValidator`, `ValidatorRule.SetFor` |
| The one-third cap (rule 22) and the power it counts | `withinOneThirdCap`, `powerIgnoringFreezes` |
| The genesis file | `Genesis`, `GenesisValidator`, `GenesisAccount`, `genesisParameterNames` |
| Reading and checking it | `ParseGenesis` (every parameter, no unknown key); `ValidateGenesis` (the chain and the state at height 0) |
| The genesis hash and the network id | `GenesisHash`; `NetworkID`: the name, `/`, the hash in hex |

**Later steps take as given:**
- The set that signs the block built on a state is `chain.ValidatorRule.SetFor(state)`, with each
  seated name's current key from that state. Every node computes the same set.
- A frozen name is never seated, and in dynamic mode a frozen balance backs no one (R10).
- The network id binds the genesis: a signature made for one genesis fails on another.
- A genesis is a state the ledger could reach: canonical, distinct names and keys, seated banks,
  parameters that pass `Acceptable`, balances within the cap.

**Check.** Read `ledger/validators_test.go` and `ledger/genesis_test.go`. Run (13 tests):
`go test ./ledger -run '^Test(StaticSet|DynamicSet|FrozenNameIsNotSeated|FrozenAccountBacksNoDelegate|Genesis|NetworkId)'`

### Step 8. The block

- **For:** `ApplyBlock`, which every node runs on every block, and `ProposeBlock`, which builds one.
  They compose steps 3 to 7. Why: [architecture.md](architecture.md#the-block-and-its-hashes).
- **Files:** `ledger/hash.go`, then `ledger/block.go`: `Block` and `BlockContent` first, then the
  file from the top, with `contentError` and `ApplyBlock`, at its end, before `ProposeBlock`, which
  builds what they accept.

| Concept | Where |
|---|---|
| The block | `Block` (`Height`, `PreviousBlockHash`, `ValidatorsHash`, `Nonce`, `Content`, `StateDigest`), `BlockContent` |
| The block hash | `BlockHash`, `BlockHashOf`, `SealedDigestOf`, `ContentHash`, `ValidatorsHash` |
| What a checkpoint carries of the last block | `BlockHeader`, `Block.Header`, `BlockHeader.BlockHash` |
| A message's size bound | `WithinMessageBound` (`MaxMessageBytes`) |
| A payment, an update, a request against a state | `PaymentFitsState`, `UpdateFitsState`; in a block `paymentError` (its block, its pin), `updateError`, `nameRequestError`, and `paymentSigned`, `updateSigned`, `nameRequestSigned` |
| The balance cap | `CreditFitsBalanceCap` (for intake), `creditedBalance`, `withinBalanceCap` (rule 23) |
| Evidence in a block | `evidenceFitsState`, `voteEvidenceError`, `proposalEvidenceError`, `maxEvidencePerBlock` |
| The names a block freezes | `frozenNames` |
| Checking content against the pre-block state | `contentError` (steps C to H of spec 5.3) |
| Applying checked content, in order | `applyContent`: payments, updates, requests, freezes, the governance action, due changes |
| At least one name seated (R9, rule 24) | `seatsAName`, `LeavesANameSeated` |
| The whole check | `ApplyBlock` |
| Building a block from what the pools hold | `ProposeBlock`, `maxBlockContentBytes`, `serializedSize`, `withoutLastItem` |

**Go:** `canFreeze := func(…) …` inside `ProposeBlock` is a closure (step 1). It reads
`frozenByTheBlock` and `locked`, which the loops after it change, and adds a name only to a copy
(`maps.Clone`).

**Later steps take as given:**
- `ApplyBlock(chain, previousState, previousBlockHash, block)` returns the next state only if the
  block is the next height, builds on `previousBlockHash`, names the set `previousState` seats,
  every item is valid against `previousState`, and the result has the claimed digest. It is pure.
- A block's items are judged against the pre-block state, never against each other's effects. A name
  the block freezes, and the ⅔ action's name, send and receive nothing else in it (R6).
- `ProposeBlock` turns any content into a block `ApplyBlock` accepts: it keeps what is valid, checks
  signatures last, and stops at 768 KiB of messages and 8 evidence items.
- The block hash covers the height, the previous hash, the signing set, and a sealed digest of the
  nonce, the state digest and the content hash.

**Check.** Read `ledger/hash_test.go`, `block_test.go`, `blockid_test.go`, `prevbind_test.go`,
`evidence_test.go`, `messagesize_test.go`, and now the block tests of `payment_test.go`,
`account_update_test.go`, `name_request_test.go`, `governance_test.go` and `reassignment_test.go`.
Run the whole package (125 tests): `go test ./ledger`

### Step 9. The round driver

- **For:** the state machine that turns step 2's vocabulary into agreement on one block per height.
  Why: [consensus.md](consensus.md#the-round-driver).
- **Files:** `consensus/driver.go`.

| Concept | Where |
|---|---|
| The consensus state of one height | `Driver`, `NewDriver` |
| The phases of a round | `Step`: `StepPropose`, `StepPrevote`, `StepPrecommit` |
| A proposal as the driver sees it | `Proposal` (`Round`, `BlockHash`, `ValidRound`): the caller passes only one whose block applied |
| What it asks its caller to do | `Action`: `ActionProposal`, `ActionBroadcastVote`, `ActionSchedule`, `ActionDecided` (with the `Commit`) |
| Its inputs | `Resume` (the node starts every height with it; `Start`, in `driver_test.go`, only tests use), `ReceiveProposal`, `ReceiveVote`, `Timeout` |
| The rules, applied until none fires | `evaluate`, `applyOneRule`, `startRound` |
| Locking | `lockedValue`, `lockedRound`, `validValue`, `validRound`, `prevoteFor` |
| Its own votes | `castVote`, `timeoutVote`; with a nil signer it signs nothing and only follows |
| A vote made durable before it counts | `SetSignLog`; after a restart `Resume`, `count`, `voted` |
| Votes from far rounds | `MaxRoundLookahead` (256) |
| The double votes its vote sets saw | `Driver.Evidence` |

**Go:** `type Action interface{ isAction() }` has one unexported method, so only this package's
four action types satisfy it: each says `// implements Action`, and its empty `isAction` method is
what makes it one. The caller tells them apart with a type switch (step 12). `iota` numbers a
block of constants 0, 1, 2.

**Later steps take as given:**
- The driver reads no clock and does no I/O: timeouts arrive as `Timeout` calls, and it answers
  every input with a list of actions.
- It decides a block hash only with a proposal of it, whose block applied (the caller passes no
  other), and a quorum of precommits for it, at the lowest such round; the decision carries the
  `Commit`.
- With its sign log, it never signs two different votes for one round and type, even across a
  restart.

**Check.** Read `consensus/driver_test.go`, `sim_test.go`, and
`TestDriverSurfacesEquivocationEvidence`. Run (21 tests; `-short` runs fewer simulator seeds):
`go test ./consensus -run '^Test(Driver|Sim)'`

### Step 10. The node and its event loop

- **For:** how one running node holds the state of steps 3 to 9, and takes its inputs. Why:
  [architecture.md](architecture.md#the-event-loop).
- **Files:** `node/messages.go`; in `node/node.go`, from the top to `publishView`.

| Concept | Where |
|---|---|
| A node's configuration | `node.Config` |
| What nodes exchange, and how they send it | `Message`, `MessageType`, `ProposalMessage` (a signed proposal and its block); the `Transport` interface (`Broadcast`; the HTTP one is step 17) |
| The committed state handed to a peer | `Checkpoint`, `Checkpoint.Consistent` |
| The node | `Node`; the fields after "state owned by run()" are touched only by the loop, except `backupBusy`, an atomic flag the backup goroutine clears (step 14) |
| One goroutine that owns the state | `Node.run`, `handle`, `enqueue`; the `events` channel; `eventKind`, `event` |
| Inputs | `SubmitProposal`, `SubmitVote`, `AdmitPayment` (mempool.go), `AdmitUpdate` (updatepool.go), `AdmitNameRequest` (requestpool.go), `AdmitParameterCertificate`, `AdmitReassignmentCertificate` |
| The loop's verdict; reads through the loop | `admission`, `awaitAdmission`; `Node.Snapshot` (type `Snapshot`), `Node.GovernanceView` |
| The committed state, read off the loop | `committedView`, `publishView`, the `view` field |
| The height in progress | `Node.height`: the committed height plus one |
| Opening, starting, stopping | `Open` (it loads the snapshot and sign log, step 14), `Start`, `Stop` |

`handle` hands each event to a function of a later step (11 to 15); read it here as the list of
events.

**Go:** `go node.run()` starts a goroutine, a function that runs concurrently with its caller.
`chan event` is a channel: one goroutine sends values, another receives them in order. `select`
runs the first of several channel operations that is ready. `atomic.Pointer[committedView]` is a
generic type, with its type argument in brackets: a pointer to a `committedView` that one goroutine
publishes and others read safely. `defer close(node.done)` runs as `run` returns.

**Later steps take as given:**
- All node state changes on one goroutine. HTTP handlers and timers only post events, with a
  channel for the answer when they need one (`admit`, `query`), so the engine has no lock.
- The intake of payments, updates and name requests checks signatures off the loop, against the
  committed view, which never changes once published (step 11). Proposals, votes and certificates
  are checked on the loop.
- `Start` also starts three goroutines of sync.go (step 15): `syncThenStart` when bootstrap peers
  are set, `catchUpLoop` and `reGossipLoop`.

**Check.** Read `node/node_test.go`: its in-memory `bus` transport, `testConfig`, `New` (an `Open`
that panics), and `SubmitPayment` and `SubmitUpdate` (the intake without the verdict) are the harness
most node tests use. Run:
`go test ./node -run '^Test(NodesReachConsensusAndApplyPayment|NodeHeightComesFromState|BlockOneBuildsOnTheGenesisHash)$'`

### Step 11. Intake and the pools

- **For:** how a payment, an update or a name request gets into a node, and what the node keeps
  until a block can carry it. Why: [architecture.md](architecture.md#intake-and-the-pools).
- **Files:** `node/mempool.go`, `node/updatepool.go`, `node/requestpool.go`; in `node/node.go`,
  `reconcilePools`.

| Concept | Where |
|---|---|
| Pending payments | `mempool` (`add`, `contains`, `outgoingFor`, `paysIn`, `keep`, `pendingCopy`) |
| Pending updates; pending requests, each one per name and block | `updatePool`, `updateSlot`; `requestPool`, `requestSlot`, `feesOf`, `paysFeeIn` |
| How far ahead a message may name its block | `MaxBlockIDLookahead` (64: payments, updates, freezes), `MaxRequestLookahead` (3,600: other requests) |
| Payment intake, off the loop | `AdmitPayment` → `checkPayment`: shape, `paymentIntakeError` (window, pin, `ledger.PaymentFitsState`, `ledger.CreditFitsBalanceCap`), then `ledger.VerifyPayment` |
| Payment intake, on the loop | `admitPayment`: the same checks on the live state, the key unchanged, no fee of the payer for that block, the overdraw guard (`pendingOutgoing`: pooled payments and fees); pool, relay, wake |
| Update intake | `AdmitUpdate` → `checkUpdate`, `updateIntakeError`; `admitUpdate` |
| Name request intake | `AdmitNameRequest` → `checkNameRequest`, `nameRequestIntakeError`; `admitNameRequest` |
| After each commit | `reconcilePools` |

Some calls come later. `Node.relay` passes a message to the node's peers once (step 18).
`Node.wakeIfIdle` starts the height if the node is idle (step 12). `reconcilePools` ends with
`pruneParameterCertificates`, `pruneReassignments` and `prunePendingProofs` (step 13).

**Go:** `keep(wanted func(ledger.Payment) bool)` takes a function as a parameter; `reconcilePools`
passes closures.

**Later steps take as given:**
- A pooled entry fits the committed state, names a block within its window, and verified under its
  signer's key in that state. Each pool holds at most `MaxMempool` entries (0: no bound).
- A payer's pooled payments and fees never exceed its balance, so an honest block never triggers the
  overdraw ban; a payer never has a payment and a fee pooled for one block.
- After each commit an entry goes when its block has passed, its signer's key changed, or it no
  longer fits the state or its payer's balance.

**Check.** Read `node/admission_test.go`, `overdraw_guard_test.go`, `bounds_test.go`,
`malleability_test.go`, `account_update_test.go` (but `TestUpdateRefusalAnswers409`), and the intake
and pool tests of `requestpool_test.go`. Run (24 tests):
`go test ./node -run '^Test(Intake|DynamicValidatorAdmission|LoopRechecksTheSignersKey|PaymentAboveTheLargestPayment|PaymentLiftingAPayee|Mempool|NodeAppliesAnUpdateEndToEnd|UpdateIntake|UpdatePool|RequestPool|PoolCountsPendingFees|RequestIntake|RequestWindow|RegistrationEndToEnd|LoopRefusesWhatWasChecked)'`

### Step 12. One height, from candidate to commit

- **For:** what a node does at each height: build a candidate, run the driver, take proposals and
  votes, commit. Why: [architecture.md](architecture.md#the-idle-chain),
  [consensus.md](consensus.md#intake-of-proposals-and-votes),
  [consensus.md](consensus.md#signing-only-while-seated).
- **Files:** in `node/node.go`, from `startHeight` to the end; then `node/idle.go`.

| Concept | Where |
|---|---|
| The candidate and the driver | `startHeight`: the last driver retired (`retireDriver`, step 13); a fresh nonce; `ledger.ProposeBlock` over the pools and the proofs and certificates of step 13 (`pendingEvidence`, `pendingParameterChange`, `pendingReassignment`); `consensus.NewDriver`; `Driver.Resume` |
| No work, no height | `hasWork`, the `idle` field, `wakeIfIdle` (idle.go) |
| Signing only while seated under the node's key | `seatedSigner` |
| The waits after a restart without a sign log | `startFirstHeight`; `waitForOwnSignatures`, at each height the node may have signed at, up to the `mayHaveSignedUpTo` field, which `noteOwnSignature` raises |
| Taking a proposal | `onProposal` (seven checks, cheap ones first; a proposal for a round it already holds one for goes to `reportEquivocation`, step 13; while no driver runs, one in its own name for any round up to `MaxRoundLookahead`), `maxInvalidProposals`, `rememberProposal` |
| Taking a vote | the `eventVote` case of `handle`: `holdsVote`, `countable`, `rememberVote`, then `Driver.ReceiveVote`; a vote it cannot count goes to `noteOwnSignature` and `recordLateDoubleSign` (step 13) |
| Doing what the driver asks | `process`: sign, log and send a proposal; send a vote; arm a timer; commit |
| Timers | `armTimeout`: `stepTimeout` × (round + 1), for the height that armed it |
| Committing | `commit`: `ledger.ApplyBlock` on the decided block, the new state, the driver retired (step 13), the snapshot saved and the sign log emptied (step 14), the record (step 16), the view published, the pools reconciled, peers re-resolved (step 18), the last commit sent, the next height after `blockInterval` (at least two re-gossip intervals at a height it waits at, `waitForOwnSignatures`) |
| Keeping peers in step | `reGossipVotes`, `reGossipProposal`, `sendLastCommit` |

**Go:** `switch action := rawAction.(type)` in `process` is a type switch: each case runs with
`action` as that concrete type. `time.AfterFunc(delay, f)` runs `f` on its own goroutine after the
delay; here `f` only enqueues an event.

**Later steps take as given:**
- A node takes at most one proposal per round: from the round's proposer, under the key the height's
  set holds, whose block hashes to the signed hash and applies.
- It counts a vote only for the height in progress, from a member, under the set's key, within
  `MaxRoundLookahead` rounds; a byte-identical copy of a vote it holds costs nothing.
- A commit applies exactly the block the precommits named. With no work, no height starts. A node
  that is not seated, or is seated under another key, follows and signs nothing.

**Check.** Read `node/window_test.go`, `votes_test.go`, and the tests the command names in
`idle_test.go`, `proposal_relay_test.go`, `replay_test.go`, `http_test.go` and `evidence_test.go`.
Run (21 tests):
`go test ./node -run '^Test(PostCommitWindow|StaleTimer|ReGossipCarries|TheLastCommitCarries|FreshNetwork|IdleChain|PaymentWakes|PeerVoteWakes|PendingChangeKeeps|FutureBlockId|PaymentInThePostCommitWindow|ProposalFlood|InvalidProposals|SwappedBody|ForgedCopy|ProposalIsTakenOnly|OnlyCountableVotes|RelayedVoteCopy|ProposalLabelledWithOldHeight|ZeroBlockInterval|NodeWithAReplacedKey)'`

### Step 13. Double-signs and certificates on the node

- **For:** what the node keeps for a later block besides the pools: double-sign proofs and complete
  ⅔ certificates. Why: [consensus.md](consensus.md#equivocation-freezes-the-name),
  [governance.md](governance.md#collecting-the-votes).
- **Files:** `node/proofs.go`, `node/governance.go`, `node/reassignment.go`.

| Concept | Where |
|---|---|
| A proof waiting for a block | `pendingProof` (one evidence item and its `ConvictedKey`), the `pendingProofs` field |
| Where proofs come from | `retireDriver` (the driver's vote sets), `recordLateDoubleSign` (a precommit against the last commit), `reportEquivocation` (a second proposal for a round) |
| Offering and dropping them | `pendingEvidence`; `prunePendingProofs` |
| Parameter-change certificates | `addParameterVotes`, `parameterChangeKey`, `currentParameterVoters`, `pendingParameterChange`, `pruneParameterCertificates` |
| Name-action certificates | `addReassignmentVotes`, `reassignmentKey`, `reassignmentFits`, `currentReassignmentVoters`, `pendingReassignment`, `pruneReassignments` |
| The admin page's view | `GovernanceView`, `ParametersView`, `ParameterProposalView`, `ReassignmentView`, `governanceView`, `tallyVoters` |

**Later steps take as given:**
- A proof stays until a block carries it, its name is frozen, or its name's key changes; the
  snapshot keeps it (step 14). A proof the one-third cap holds back keeps no height moving.
- The node pools only complete certificates, at most 64 of each kind; a relayed copy with the same
  votes costs no check, and the votes of names that left the set are dropped.
- Each candidate carries at most one certificate, picked in sorted order.

**Check.** Read `node/governance_test.go`, `reassignment_test.go`, and the tests the command names
in `evidence_test.go`, `proposal_relay_test.go`, `idle_test.go` and `requestpool_test.go`. Run (19
tests):
`go test ./node -run '^Test(LateDoubleSign|ProposalEquivocationIsCarried|ProofOutlives|ProofIsDropped|PendingProofIsWork|ProposerEquivocation|NonceOnlyDifference|NodeEnacts|GovernancePools|DepartedVoter|GovernanceView|ReplacingCertificate|NameActionPoolKeys|RelayedCertificateCopy|NoBlockLeavesTheSetEmpty)'`

### Step 14. The snapshot and the sign log

- **For:** what a node keeps on disk to restart safely. The sign log belongs to the signature area:
  it keeps a node from signing twice. Why: [signature.md](signature.md#the-sign-log),
  [operating-a-network.md](operating-a-network.md#rejoining-after-a-restart).
- **Files:** `node/persist.go`.

| Concept | Where |
|---|---|
| The snapshot file | `savedSnapshot` (`NetworkID`, `Checkpoint`, `PendingProofs`), at `Config.SnapshotPath` |
| Writing it | `saveSnapshot` after each commit and catch-up; `writeAtomic` (temp file, fsync, rename, fsync the folder); the `BackupPath` copy, `backupBusy` |
| Loading it | `loadSnapshot`, from `Open`: this network's id; `selfConsistent` (`Checkpoint.Consistent`, and `Commit.Verify` against the set the checkpoint carries); `fastForward` |
| Moving to a checkpoint | `fastForward`: the state, the last block's header, the commit and its signing set; the pools reconciled; peers re-resolved (step 18) |
| A file of another network | `ofAnotherNetwork` |
| The sign log | `signLog` (`NetworkID`, `Height`, `Votes`, `Proposals`, `Locked`, `MayHaveSignedUpTo`) at `signLogPath`, the snapshot path plus `.votes` |
| On disk before it counts or leaves | `logVote` (given to the driver by `startHeight` with `Driver.SetSignLog`); `logProposal` (called by `process`) |
| Writing and reloading the log | `heightSignLog` (a new log starts with the node's own votes and proposals it holds for the height), `writeSignLog`, `emptySignLog`, `loadSignLog` |

**Go:** `defer directory.Close()` runs the call when the function returns, on every path.
`errors.Is(err, os.ErrNotExist)` asks what kind of error `err` is.

**Later steps take as given:**
- A restart resumes from the snapshot and the sign log of its height, and never signs a second,
  different vote or proposal for a round it signed before.
- `Open` refuses a snapshot folder that does not exist, a snapshot or sign log of another network, a
  sign log ahead of the snapshot, and a file that does not verify.
- Once a height's snapshot is on disk, `commit` writes an empty log for the next height, and
  `catchUp` and `adopt` empty the log of a height they leave. `Open` (`loadSignLog`) empties a log
  it finds below the height it restarts at, at that log's own height, as `adopt` does: the node
  still waits as after a lost log. Before a height the node waits at after a lost log, `commit` and
  `catchUp` too empty the log at its own height. A failed snapshot write is logged, not fatal, and
  the log stays.

**Check.** Read `node/persist_test.go`, `restart_test.go`, `backup_test.go`, `backup_unix_test.go`,
`TestPendingProofSurvivesARestart`, and the two restart tests of `idle_test.go`. Run (41 tests):
`go test ./node -run '^Test(NodePersistsAndReloads|NodeReloadsOwnSnapshot|OpenRefuses|ValidatorRestarts|StateOutOfNameOrder|SnapshotKeepsOnlyTheLastNonce|SignLog|SnapshotOfAnEarlierNetwork|FailedWriteLeavesNoTemporaryFile|Restarted|TotalOutage|VoteIsReleasedOnlyOnceLogged|ProposalIsSentOnlyOnceLogged|SoleValidatorRestart|ValidatorBackWithoutItsSignLog|BackupMirrorsSnapshot|HungBackupMount|PendingProofSurvivesARestart|RestartMidHeight|NodeBackWithoutItsSignLog)'`

### Step 15. Catch-up and the checkpoint check

- **For:** how a node that joins, restarts or falls behind takes a peer's state without trusting the
  peer. Why: [consensus.md](consensus.md#catching-up),
  [consensus.md](consensus.md#onboarding-and-the-checkpoint).
- **Files:** `node/sync.go`.

| Concept | Where |
|---|---|
| Joining from bootstrap peers before the first height | `syncThenStart`, `adopt` |
| Catching up while running | `catchUpLoop`, `catchUpInterval`, `catchUpCheckpoint`, `catchUp` (from a height the node may have signed at before it lost its sign log, it waits at the new one first, step 12) |
| Fetching | `bestCheckpoint`: `fetchHeight` (`/hdls/status` first), `fetchCheckpoint` (`/hdls/checkpoint`, at most `MaxCheckpointBytes`) |
| The check, in order | `verifyCheckpoint`: `Checkpoint.Consistent`; `anchorVouches` (more than ⅓ of the node's own set signed); `Commit.Verify` against the set the checkpoint carries. It is `selfConsistent` (step 14) with the anchor added |
| Re-gossip cadence | `reGossipLoop`, `reGossipInterval`; `everyInterval`, the timer both loops share, which follows a vote within a second |

**Go:** `node.transport.(interface{ Peers() []string })` is a type assertion: it asks whether the
transport also has a `Peers` method, and its second result says whether it has.

**Later steps take as given:**
- A node takes a peer's checkpoint only if validators it already trusts, with more than ⅓ of their
  power, signed its commit, and a quorum of the set it carries signed it.
- A node at its peers' height fetches no checkpoint. `adopt` and `catchUp` save the checkpoint as
  the node's snapshot before the node signs on top of it.

**Check.** Read `node/sync_test.go`, `catchup_test.go`, `onboarding_test.go`, and
`TestCatchUpKeepsEquivocationEvidence`. Run (16 tests):
`go test ./node -run '^Test(NodeSyncsFromCheckpoint|NodeRejectsUnverifiableCheckpoint|FetchCheckpoint|CatchUp|StragglerCatchUp|DynamicStragglerCatchesUp|VerifyCheckpoint|CheckpointGivingRealNames|AnchorChecksOneVotePerName|CheckpointRecomputes|PaddedCheckpoint|TimingsFollowAVote)'`

### Step 16. The signature record

- **For:** the public record of who signed each committed block. Why:
  [architecture.md](architecture.md#the-signature-record),
  [security.md](security.md#the-signature-record).
- **Files:** `node/record.go`.

| Concept | Where |
|---|---|
| A block's entries | `RecordEntry` (rolling), `LifelongRecordEntry` (governance and evidence) |
| File names, by network | `recordPrefix`, `recordDirectory`, `lifelongRecordPath`, `unitPath`, `recordUnitHeights` (100) |
| Writing after each commit | `recordCommittedBlock`, `writeRecordEntry`, `lifelongEntryOf` |
| Deleting whole unit files | `pruneRecord`, `unitEnd`, `unitEnd.dueAt`, `unitFirstHeights`, the `oldestUnitEnd` field |
| Appending safely | `lastCompleteLine`, `placeOfNextLine`, `writeLine` |
| Reading entries back, for the routes of step 17 | `readRecord`, at most `maxRecordEntries` at once |

**Later steps take as given:**
- The record holds who signed each committed block, never payments, updates, holder requests,
  nonces or state digests.
- Each height is recorded once; heights a catch-up skipped are missing. A failed write is logged.

**Check.** Read `node/record_test.go` but its three route tests and `TestPartialLastLineIsSkipped`.
Run (12 tests):
`go test ./node -run '^Test(RecordKeeps|RecordNever|RecordChain|RecordPrunes|RetentionCut|LongerRetention|PruningLists|RecordFilesAreNamed|RecordUnits|LifelongRecordKeeps|RecordHoldsEachHeightOnce|NodeWithoutASnapshotPath)'`

## API/CDN area

### Step 17. The HTTP routes

- **For:** the node's one port: the routes peers and wallets call, and the transport that calls
  them. Why: [api.md](api.md).
- **Files:** `node/httptransport.go`.

| Concept | Where |
|---|---|
| Sending to peers | `HTTPTransport`: `NewHTTPTransport`, `Broadcast` (a POST per peer, each on its own goroutine), `post`, `SetPeers`, `Peers` |
| The routes | `Server`, `NewServer`, `Server.Handler` (15 routes) |
| Messages from peers only | `handleProposal`, `handleVote`, `handleGossipPayment` |
| Messages from wallets only | `handlePay` (a signed `ledger.Receipt` for a newly pooled payment) |
| Messages from wallets and from peers that relay them | `handleAccountUpdate`, `handleNameRequest`, `handleParameterVote`, `handleReassignmentVote` |
| Reads through the loop | `handleAccounts`, `handleCheckpoint`, `handleGovernance` |
| Reads from the committed view | `handleStatus`, `handleResolve`, `ResolveRecord` |
| The record routes | `handleRecord` (`?from=H`: heights H to H + 99), `handleLifelongRecord`, `recordFrom`; they read through `readRecord` (step 16) |
| Body bounds and answers | `decode`, `maxBodyBytes` (1 MiB), `maxProposalBytes` (2 MiB), `writeJSON` |

**Go:** a `sync.RWMutex` guards data several goroutines share: `Lock` for a writer, `RLock` for
readers. `HTTPTransport` needs one because the loop sets the peers (`resolvePeersFromState`, step
18) while the catch-up goroutine reads them (`catchUpCheckpoint` → `Peers`, step 15).

**Later steps take as given:**
- A route decodes, calls one node method and answers. A refused payment, update, name request or
  certificate gets `409` and the reason. A proposal or a vote gets `202` once decoded, and the loop
  drops it without a word if it fails its checks.
- `/hdls/pay` returns a signed receipt only for a payment the node newly pooled.
- `/hdls/status` gives the height in progress and the network id, `/hdls/checkpoint` the checkpoint
  of step 10, `/hdls/resolve` the committed height and the state digest a pin takes.

**Check.** Read the tests the command names in `node/http_test.go`, `replay_test.go`,
`record_test.go` and `account_update_test.go`. Run (16 tests):
`go test ./node -run '^Test(NodesReachConsensusOverHTTP|ResolveEndpoint|StatusServes|RoutesAnswer405|ProposalRouteTakes|ResolveServes|ResolveTolerates|RecordRoute|LifelongRoute|PartialLastLine|PaymentGossipReceiptAndReplay|RepeatPaymentGetsNoNewReceipt|UpdateRefusalAnswers409|CertificateRefusalAnswers409)'`

### Step 18. Relay, topology and peers

- **For:** how a message reaches every validator, and how validators find each other on-chain.
  Why: [api.md](api.md#how-messages-travel).
- **Files:** `node/relay.go`, `node/topology.go`, `node/peerdns.go`.

| Concept | Where |
|---|---|
| Passing a message on, once | `Node.relay`, `messageKey`, the `relaySeen` field (cleared at each commit) |
| The Hypernode gossip topology | `Neighbors`, `indexToCoordinates`, `coordinatesToIndex`, with `ledger.GroupParameters` (step 3) |
| Peers from on-chain addresses | `resolvePeersFromState`, `resolvedPeerAddresses`; the `configuredPeers` and `peersResolved` fields |

**Later steps take as given:**
- Each node passes a message it took to its peers once, so a message crosses a multi-hop topology.
- A validator takes its peers from the chain, from the moment it opens, only when every current
  validator has published an address; otherwise it keeps its configured peers.

**Check.** Read `node/topology_test.go`, `peerdns_test.go`, and
`TestOnProposalRelaysFirstSightOnlyNeverDuplicates`. Run (10 tests):
`go test ./node -run '^Test(FullMeshNeighbors|GroupParametersScalesDimension|HypercubeConnected|ConsensusOverHypercube|ResolvePeers|IncompletePeerResolution|PartialPeerAddresses|ValidatorLosingItsAddress|OnProposalRelaysFirstSight)'`

### Step 19. The commands

- **For:** the programs that run a node and the demo. Why:
  [operating-a-network.md](operating-a-network.md).
- **Files:** `cmd/server/config.go`, `cmd/server/main.go`, `cmd/demo/main.go`.

| Concept | Where |
|---|---|
| The node config file | `fileConfig` |
| Reading it and its genesis | `load`: unknown keys refused; both keys' base64, the public key's size and spelling, and the name; `ledger.ParseGenesis`, `ledger.ValidateGenesis`; `snapshotPath` required on a node that can be a validator; defaults; then the seed (`signing.NewSigner`), the pair, and `keySelfTest` |
| Running a node | cmd/server `main` → `bindThenStart`: the listen address bound first (`net.Listen`), so a second start on that address stops before it signs; then `node.Open` and `Node.Start`; `main` then serves an `http.Server` with timeouts on that listener; `validatorsLabel` for the startup log |
| The demo network | cmd/demo `main`, `demoGenesis`, `demoNodeConfig` and `demoHTTPServer` (cmd/server's limits and timeouts), `newKeyPair`, `writeWallet`, `startWalletUI`, `walletArguments` |

**Later steps take as given:**
- `cmd/server` refuses a config it cannot run safely: a key of the wrong size or spelling, a seed
  that is not the public key's, a validator without a snapshot path.
- Every node of a network reads the same genesis file and logs the network id it makes.

**Check.** Read `cmd/server/config_test.go` (`TestKeygenOutputLoads` ties step 1 to this one),
`cmd/server/main_test.go` and `cmd/demo/main_test.go`. Run `go test ./cmd/...`, and
`go run ./cmd/demo` to see a node run; it prints the wallet command, and Ctrl-C stops it.
`make demo`, which builds the wallet too, is step 22's.

## UI area

### Step 20. The key file, the pin and the verified state

- **For:** what the wallet keeps on disk, and which state it believes. Why:
  [ui/README.md](../ui/README.md#its-files).
- **Files:** `ui/keyfile.go`; in `ui/wallet.go`, from the top to `saveState`, then `PublicKey`,
  `pinnedNetworkID`, `signer`, `nameOfKey`, `registeredName`; then `ui/trust.go`.

| Concept | Where |
|---|---|
| The key file and its passphrase | `keyFile`, `encryptedKey`, `loadOrCreateKey`, `saveKey`, `sealPrivateKey`, `encryptedKey.open`, `gcmFor`; `getPassphrase`, `newPassphrase`, `interactive`, `readSecret` |
| The wallet | `Wallet`, `LoadOrCreateWallet` |
| What survives a restart | `walletState` (`<keyfile>.state`), `readWalletState`, `saveState` |
| The pin | `walletPin` (`<keyfile>.trust`), `loadPin`, `savePin` |
| One verified state | `Wallet.verifiedState`: `Checkpoint.Consistent`; at first use `servedNetworkID` and `Commit.Verify`, later `signedByPinned`; never older than `NewestHeight` |
| Signing for the pinned network; the wallet's name | `Wallet.signer`, `pinnedNetworkID`; `nameOfKey`, `registeredName` |

**Go:** the wallet serves several pages at once, so `Wallet.mutex`, a plain `sync.Mutex` (`Lock`
and `Unlock`, see step 17), guards its shared fields.

**Later steps take as given:**
- At first use, when `<keyfile>.trust` does not exist, the wallet believes the state its node serves if
  the validators that state carries signed it, for the network id the node serves, and that id
  starts with the `-network` name and `/`. It then pins the id and those validators (trust on first
  use).
- From then on, or from the start when the operator's list is in that file, the wallet believes a
  state only if pinned validators with more than ⅔ of the pinned power signed it under their pinned
  keys.
- It never believes a state older than the newest state it verified.
- It signs only for the pinned network id, so only after it has read a verified state.
- Its name is the account whose current key is the wallet's key.

**Check.** Read `ui/keyfile_test.go` and `ui/trust_test.go`. Run (19 tests):
`go test -C ui -run '^Test(KeyFile|SealedKey|PlaintextKey|WalletSealed|WalletBelieves|OperatorPin|PinnedQuorum|WalletPin|WalletRefusesA|FirstUse|WalletBuildsItsSigner)' ./...`

### Step 21. The wallet's actions

- **For:** everything the wallet signs: payments, updates, freezes, name requests and governance
  votes. Why: [ui/README.md](../ui/README.md#how-it-works),
  [ui/README.md](../ui/README.md#the-payment-counter).
- **Files:** the rest of `ui/wallet.go`, `ui/requests.go`, `ui/admin.go`.

| Concept | Where |
|---|---|
| Typed input | `typedName`, `typedKey`, `parseAmount`, `fingerprint` |
| The payment counter, counted from the verified state's height | `nextPaymentBlockID`, `keepRequestBlock`, `blockSlack` |
| The height the node reports, for the requests, and for a freeze when no state verifies | `Wallet.height` (→ `Wallet.status`) |
| Paying | `ReviewPayment` (shows the payee's bank, signs nothing), `Send` (signs, posts to `/hdls/pay`, checks the receipt), `pinnedKey` |
| Updating; freezing | `UpdateAccount`; `Freeze`, `freezeStatus`, `lastName`, `pendingChange` |
| Posting to the node | `postJSON` |
| Name requests | `DraftNameRequest`, `SignNameRequest`, `SubmitNameRequest`, `requestFacts`, `hashName`, `defaultRequestLead`, `node.MaxRequestLookahead` |
| The bank's desk | `customerChanges`, `pendingRow` |
| Governance votes | `DraftParameters`, `DraftReassignment`, `fitsStateBeforeTheProof`, `parameterFacts`, `nameActionFacts`, `SignParameters`, `SignReassignment`, `handOver`, `SubmitParameters`, `SubmitReassignment`, `decodeVotes`, `incomplete` |
| The node's governance view | `Wallet.Governance`, which reads the node's own `GovernanceView` (step 13) |

**Go:** `decodeVotes[CertificateType any]` is a generic function: the caller names the type it
decodes, as in `decodeVotes[ledger.ParameterChange]`.

**Later steps take as given:**
- The wallet never puts two signed payments or updates for one block into the world.
- Requests and votes travel between wallets as text; a node receives only a complete one.
- Every action but two checks the network's own rules first: `ledger.PaymentFitsState` and
  `ledger.CreditFitsBalanceCap` (before a payment takes a block), `ledger.UpdateFitsState` (before an
  update takes one), `ledger.NameRequestFitsState`, `Reassignment.FitsState`,
  `LedgerParameters.Acceptable`. The two are the freeze and a name request's submit step, which
  checks only the request's shape: they leave the rules to the node, and take no block of the
  counter.

**Check.** Read the action tests of `ui/wallet_test.go`, `requests_test.go`, `admin_test.go`, and
the receipt tests of `security_test.go`. Run (46 tests):
`go test -C ui -run '^Test(WalletFindsItsName|WalletPaysByName|WalletBoundsTheStatus|PaymentBlockID|BlockIDCounter|AccountUpdatesNameBlocks|AccountUpdateRuns|AccountFormDelegates|AmountsOffTheGrid|Freeze|PaymentShowsThePayeesBank|PaymentReview|WalletRefusesNonCanonicalNames|AccountFormTakes|Receipt|Registration|SignStep|Requests|RequestBlocks|HashName|DeskLists|Reassignment|ParameterVotes|ParameterTiming|SoleValidator|ParameterDraft|NameAction|GovernanceView)' ./...`

### Step 22. The pages

- **For:** the wallet's web pages, and what keeps other sites from using them. Why:
  [ui/README.md](../ui/README.md), [ui/USER_MANUAL.md](../ui/USER_MANUAL.md).
- **Files:** `ui/main.go`, `ui/templates/wallet.html`, `requests.html`, `admin.html`.

| Concept | Where |
|---|---|
| Starting the wallet | `main` (its flags), `routes` |
| Refusing other sites | `guard`, `sameOrigin`, `ownHost` |
| The main page | `handler.index`, `render`, `execute`, `pageData`, `accountRow`; wallet.html |
| Paying, updating, freezing | `handler.send`, `updateAccount`, `freeze`; `send` and `freeze` sign only in their confirm step, and `updateAccount` signs as soon as its form is posted |
| The Requests page, also the bank's desk | `handler.requests`, `requestsPageData`; requests.html |
| The admin page | `handler.admin`, `renderAdmin`, `adminPageData`, `proposeParameter`, `voteReassignment`, `vote`; admin.html |
| The templates | `templateFiles`, `pageTemplates`, with the template function `fingerprint` |

**Go:** `//go:embed templates/*.html` puts the files into the binary at build time.
`html/template` escapes every value it writes into a page.

**Later steps take as given:**
- Every page reads one verified state, and shows and acts on that one state. When none verifies,
  the freeze button still freezes the last name the wallet verified (`lastName`).
- The admin page also shows the node's governance view (`Wallet.Governance`): the parameters in
  force, the total power, the quorum and the pooled certificates. Nothing verifies that view. A vote
  is drafted and checked against the verified state only.
- Every request must name the wallet's own host, and any but a GET or HEAD must come from the
  wallet's own pages; `guard` refuses the rest before any handler runs.

**Check.** Read the guard tests of `ui/security_test.go` and the page tests of `wallet_test.go`.
Run (13 tests):
`go test -C ui -run '^Test(CSRF|OwnHost|MutatingHandlers|DNSRebinding|PagesDeny|UnregisteredWallet|WalletWhoseNameHasAnotherKey|PendingKeyShows|PendingChangeOnOwnName|FrozenNameShows|UnknownPath|PagesReadTheCheckpointOnce)' ./...`
Then run `make demo`: it builds every program and starts a node with the wallet, at the address it
prints; Ctrl-C stops it.

## Step 23. Putting it together

Three journeys through the whole code. Each row is one hop; an arrow means "calls".

**A payment.** The wallet of `alice` pays 10 to `bakery`, on a network of several validators.

| # | Where | Hop |
|---|---|---|
| 1 | wallet | The form posts to `/send`: `handler.send` → `Wallet.verifiedState`, `Wallet.ReviewPayment` (→ `ledger.PaymentFitsState`, `ledger.CreditFitsBalanceCap`). The page shows `bakery`'s bank of record and signs nothing. |
| 2 | wallet | The confirm step: `handler.send` → `Wallet.Send` → `verifiedState`, `ReviewPayment`, `registeredName`, `nextPaymentBlockID` (from the verified state's height in progress, `State.Height + 1`), `Wallet.signer` → `ledger.SignPayment` → `Signer.SignBase64` → `Signer.Sign` → `signing.Preimage`, `mldsa65Key.Sign`. POST `/hdls/pay`. |
| 3 | node, off the loop | `Server.handlePay` → `decode`, `Node.AdmitPayment` → `checkPayment` → `paymentIntakeError` (→ `ledger.PaymentFitsState`, `ledger.CreditFitsBalanceCap`), `ledger.VerifyPayment` → `signing.VerifyBase64` → `signing.Verify` → `signing.Preimage`, `MLDSA65.Verify`. |
| 4 | node, on the loop | `enqueue` → `run` → `handle` (`eventPayment`) → `admitPayment` → `paymentIntakeError` again, the payer's key unchanged, `mempool.contains`, `requests.paysFeeIn` (no fee of the payer for that block), the overdraw guard (`mempool.outgoingFor`, `requests.feesOf`, `ledger.AccountAmount`), `mempool.add`, `relay` → `HTTPTransport.Broadcast`; `wakeIfIdle` → `startHeight` if the node was idle. The verdict goes back on the `admit` channel, where `AdmitPayment` waits in `awaitAdmission`. |
| 5 | node → wallet | `handlePay` → `ledger.SignReceipt`. `Send` checks it: `pinnedKey` for the node it names, its `PaymentID`, then `Receipt.Verify` under that key. |
| 6 | the other nodes | `handleGossipPayment` → `AdmitPayment`, as in hops 3 and 4. |
| 7 | the proposer | `startHeight` → `pendingEvidence`, `mempool.pendingCopy` (the pooled payments, copied) and the other pools' copies, `pendingParameterChange`, `pendingReassignment`, `ledger.ProposeBlock` (→ `paymentError`, `paymentSigned`), `hasWork`, `ledger.BlockHash`, `seatedSigner`, `consensus.NewDriver`, `Driver.Resume` → `startRound` → `ActionProposal`; `Node.process` → `signProposal` (→ `consensus.SignProposal`), `logProposal`, `Transport.Broadcast`. |
| 8 | every node | `handleProposal` → `SubmitProposal`; `handle` (`eventProposal`) → `onProposal` (→ `ValidatorSet.ProposerAt`, `SignedProposal.Verify`, `ledger.BlockHash`, `ledger.ApplyBlock`) → `Driver.ReceiveProposal` → `evaluate` → `applyOneRule` → `prevoteFor` → `castVote` (→ `consensus.SignVote`, `logVote`, `count`) → `ActionBroadcastVote`; `process` → `Broadcast`; then `relay` passes the proposal on. |
| 9 | every node | `handleVote` → `SubmitVote`; `handle` (`eventVote`) → `holdsVote`, `countable`, `rememberVote`, `relay`, `Driver.ReceiveVote` → `count` → `VoteSet.Add`; `evaluate` → `applyOneRule`: a prevote quorum locks, and `castVote` signs a precommit; a precommit quorum gives `ActionDecided` with `VoteSet.Commit`. |
| 10 | every node | `process` → `Node.commit` → `ledger.ApplyBlock` → `applyContent` → `ProcessPayments`; `retireDriver`, `saveSnapshot`, `emptySignLog` (→ `writeSignLog`), `recordCommittedBlock`, `publishView`, `reconcilePools` (the payment's block has passed: it leaves the pool), `resolvePeersFromState`, `sendLastCommit`; `time.AfterFunc` → `eventNextHeight` → `startHeight` → `hasWork` is false: idle. |
| 11 | wallet | The next page: `handler.index` → `verifiedState` → GET `/hdls/checkpoint` (`handleCheckpoint` → `Node.Snapshot` → `enqueue`, `handle` (`eventQuery`) → `Node.snapshot`; then `Snapshot.checkpoint`) → `Checkpoint.Consistent`, `signedByPinned` → `Commit.Verify`; `render` shows the new balance. |

**A freeze.** `alice` fears someone else holds her key, and presses the freeze button.

| # | Where | Hop |
|---|---|---|
| 1 | wallet | `handler.freeze` (confirm step) → `Wallet.Freeze` → `verifiedState`, `registeredName` (or, when no state verifies, `lastName` and `Wallet.height`); the block is the verified height in progress (`State.Height + 1`) plus the lookahead, or her pending change's block if sooner; `Wallet.signer`, `ledger.SignAsRequestor` (domain `freeze`), `postJSON` (POST `/hdls/name-request`); the wallet keeps `FreezeName` and `FreezeBlockID`. |
| 2 | node | `handleNameRequest` → `AdmitNameRequest` → `checkNameRequest` → `ledger.NameRequestShapeError`, `nameRequestIntakeError` (the 64-block window, `ledger.NameRequestFitsState`, `ledger.LeavesANameSeated`), `ledger.VerifyNameRequest`; `handle` (`eventNameRequest`) → `admitNameRequest` → `requestPool.add`, `relay`, `wakeIfIdle`. |
| 3 | the proposer | `ledger.ProposeBlock` takes freezes before evidence and requests, one per name, and locks the name: a payment from or to `alice` stays out of the block. |
| 4 | every node | `ledger.ApplyBlock` → `contentError` (the freeze, the locked names), `applyContent` → `freezeNames`: `Frozen` is set, her pending change is cleared, and so is any change she co-signed. `ValidatorRule.SetFor` no longer seats her. |
| 5 | every node | `commit` → `reconcilePools`: her pooled payments, updates and requests fail intake now and go; `prunePendingProofs` drops proofs against her name. |
| 6 | wallet | `handler.index` → `verifiedState`, `render` → `freezeStatus`: a verified state shows the name frozen, and the wallet forgets the freeze it sent. |

**A ⅔ name action.** The validators give `alice`'s frozen name a new key and unfreeze it.

| # | Where | Hop |
|---|---|---|
| 1 | a validator's wallet | The admin form: `handler.voteReassignment` → `handler.vote` (review) → `verifiedState`, `Wallet.DraftReassignment` → `typedName`, `typedKey`, `fitsStateBeforeTheProof` → `Reassignment.FitsState`; `nameActionFacts`. `renderAdmin` shows the facts and signs nothing. |
| 2 | the new key's wallet | `vote` (sign) → `verifiedState`, `Wallet.SignReassignment` → `Wallet.signer`, `fitsStateBeforeTheProof`, `ledger.SignReassignmentNewKey`, `handOver`: the text goes back to the validators. |
| 3 | each validator's wallet | `vote` (sign) → `verifiedState`, `SignReassignment` → `Wallet.signer`, `FitsState`, `ledger.VerifyReassignmentNewKey`, `registeredName`, `ledger.SignReassignmentVote`, `handOver` (which submits at once only if this name alone is a quorum). |
| 4 | the collector's wallet | `vote` (submit) → `Wallet.SubmitReassignment` → `decodeVotes`, `verifiedState`, `CertificateTally.Add` with `ledger.VerifyReassignmentVote` for each vote, `Reassignment.Valid`, `postJSON` (POST `/hdls/reassignment-vote`). |
| 5 | node | `handleReassignmentVote` → `AdmitReassignmentCertificate`; `handle` (`eventReassignmentCertificate`) → `addReassignmentVotes` (→ `currentReassignmentVoters`, `reassignmentFits`, `Reassignment.Valid`), `relay`, `wakeIfIdle`. |
| 6 | the proposer | `startHeight` → `pendingReassignment`, `ledger.ProposeBlock`, which takes the governance action first, locks its name and counts its new key as used. |
| 7 | every node | `ledger.ApplyBlock` → `contentError` (→ `Reassignment.Valid`), `applyContent` → `applyReassignment` (the new key, `Unfreeze`, the pending change cleared); the governance sequence moves on. |
| 8 | every node | `commit` → `recordCommittedBlock` → `lifelongEntryOf`: a lifelong entry, served by `/hdls/lifelong-record`. `reconcilePools` → `pruneReassignments` drops the certificate. |

**Why the wallet can believe what it shows.** A signature binds its network, domain and fields
(step 1). A commit needs a quorum of the set the state seats (2, 7). Every node applies exactly the
block it decided, and the block hash covers the state digest (8, 12). A checkpoint's block hash
recomputes from its header and state, and its commit verifies (10, 15). The wallet checks that
commit against the validators it pinned (20).

**Run everything**, as CI does, with the chaos scenarios of `node/chaos_test.go` (loss, partitions,
a double-signer): `go test -race ./...` and `go test -C ui -race ./...`.

## Every production file and its step

| Package | Files and steps |
|---|---|
| `signing/` | scheme.go, mldsa65.go, domain.go, signer.go, fips140v1.go: 1 |
| `cmd/keygen/` | main.go: 1 |
| `consensus/` | errors.go, validator.go, vote.go, commit.go, proposal.go, evidence.go, voteset.go: 2; driver.go: 9 |
| `ledger/` | amount.go, name.go, ledger.go, digest.go, parameters.go, topology.go: 3; payment.go, account_update.go, receipt.go: 4; name_request.go: 5; governance.go, reassignment.go: 6; validators.go, genesis.go: 7; hash.go, block.go: 8 |
| `node/` | messages.go: 10; node.go: 10 to 12; mempool.go, updatepool.go, requestpool.go: 11; idle.go: 12; proofs.go, governance.go, reassignment.go: 13; persist.go: 14; sync.go: 15; record.go: 16; httptransport.go: 17; relay.go, topology.go, peerdns.go: 18 |
| `cmd/server/` | config.go, main.go: 19 |
| `cmd/demo/` | main.go: 19 |
| `ui/` | keyfile.go, trust.go: 20; wallet.go: 20 and 21; requests.go, admin.go: 21; main.go and templates/wallet.html, requests.html, admin.html: 22 |
