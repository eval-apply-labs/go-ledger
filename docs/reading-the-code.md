# Reading the code

This guide takes a reader who has never seen the code through all of it, one step at a time. The
steps follow the four areas of the docs (signature, node, API/CDN, UI) and the order in which the
packages import each other, so each step needs only the steps before it. The signer program
belongs to the signature area, but comes after the API/CDN area: it checks every message the ledger
signs, field by field (step 24). The last step puts the pieces together: it follows one payment,
one freeze, one ⅔ name action and one payment carried by hand through every function they pass.

Each step gives what it is for, its files in reading order, a table from each concept to the type or
function that holds it, the facts later steps take as given, and how to check it: the tests to read
and the command that runs them. The guide says where things are. Why they are so is in the area
docs each step links; [README.md](README.md) is their index. Code is named by package, file and
function or type, never by line number.

## Before you start

- **Size.** About 16,100 lines of Go in 74 production files, 497 template lines, and about 27,100
  lines of tests.
- **Go.** Go 1.27, standard library only. `go.mod` names the toolchain `go1.27.3`; with
  `GOTOOLCHAIN=auto` the `go` command fetches it.
- **Two modules.** The root holds `signing`, `consensus`, `ledger`, `node` and `cmd/`. The wallet,
  `ui/`, is a module of its own that imports the root one. `deploy/` holds the container images and
  the compose file, which are not Go (step 23).
- **Tests.** A test file ends in `_test.go` and sits beside its code. `go test ./ledger -run
  'regex'` runs the tests of one package whose names match; `-v` lists them. A package compiles as
  a whole, so a step's command also compiles the later files of its package, but never a later
  package: each command below was run on a copy of the tree that held only the packages up to its
  step. For the wallet, `-C ui` must be the first flag. `-short` skips the chaos scenarios; `-race`
  adds the race detector, as CI runs it.
- **Go idioms.** A line that starts with **Go:** explains one where it first appears.
- **Words.** [glossary.md](glossary.md) explains the project's words (a seated validator, the ⅔,
  the three pins; the engine, the fetcher, the allow list, a bundle) and the ids the docs cite, such
  as R9, rule 17 or spec §5.3.

## The map

| Area | Package | Steps | Production lines | Test lines |
|---|---|---|---|---|
| signature | `signing`, `cmd/keygen` | 1, and `signing/remote.go` 24 | 882 + 95 | 846 + 112 |
| node | `consensus` | 2, 9 | 1,042 | 1,627 |
| node | `ledger` | 3 to 8 | 2,915 | 4,096 |
| node | `node`, but its HTTP side, relay, peers, allow list and public face | 10 to 17 | 5,907, both sides | 12,402, both sides |
| API/CDN | `node`'s HTTP side, relay, peers, allow list and public face; `cmd/fetcher`; `cmd/server`, `cmd/demo`, `cmd/quickstart` | 18 to 23 | 450; 448 + 233 + 388 | 656; 726 + 136 + 471 |
| signature | `cmd/signer` | 24 | 1,061 | 1,437 |
| UI | `ui` | 25 to 27 | 2,692, and 497 in `ui/templates` | 4,607 |

The imports run `signing` → `consensus` → `ledger` → `node` → `cmd/server`, `cmd/demo` and `ui`;
`cmd/keygen` imports only `signing`, `cmd/fetcher` only `ledger`, `cmd/quickstart` `ledger` and
`signing`, and `cmd/signer` `signing`, `consensus` and `ledger`. `ledger` imports the consensus
types (the validator set, votes, proposals, evidence), never the driver. The diagram is in
[architecture.md](architecture.md#packages-and-areas). The last section lists every production file
and its step.

## Signature area

### Step 1. Signatures and keys

- **For:** every statement, a payment, a vote, a name request, is signed through this package. It
  fixes what a signature binds, and that there is one way to sign, wherever the key is: in the
  process, or in the signer program (step 24). The key file keeps a key sealed at rest. The sign
  log, which keeps a node from signing twice, belongs to this area but lives in `node/persist.go`:
  step 14. Why: [signature.md](signature.md).
- **Files:** `signing/scheme.go`, `signing/mldsa65.go`, `signing/domain.go`, `signing/signer.go`,
  `signing/keyfile.go`, `signing/mldsa65_fips140v1.go`, `cmd/keygen/main.go`.

| Concept | Where |
|---|---|
| A signature scheme | `signing.Scheme` (`Name`, `SignatureSize`, `ValidatePublicKey`, `GenerateKey`, `ParsePrivateKey`, `Verify`), `signing.PrivateKey` (`PublicKey`, `Sign`) |
| ML-DSA-65, the only scheme; no build without it | `signing.NetworkScheme`, the one place that chooses it, returns an `mldsa65` (a parsed key is an `mldsa65Key`); `signing/mldsa65_fips140v1.go` |
| Changing the scheme | replace the files `signing/mldsa65*.go`, and the two comments in `scheme.go` that name the implementations; no other code names ML-DSA. What a scheme must do is in `Scheme`'s comment (README, *Cryptographic agility*) |
| The keys a scheme accepts | `Scheme.ValidatePublicKey`: the size, decodable, not degenerate; `Verify` refuses every key it refuses. `mldsa65.ValidatePublicKey`: the size, and not one of the most degenerate weak keys, under which anyone can sign every message (`anyoneCanSignUnder`: every coefficient of t1 is 0 or 1023); other keys with small t1 coefficients pass, a sanity check's limit its comment explains |
| Sizes checked before any work; deterministic signing | required by `Scheme.Verify` and `PrivateKey.Sign`; `mldsa65.Verify`; `mldsa65Key.Sign` (`SignDeterministic`, no context string) |
| The purpose of a signature | `signing.Domain` and its 14 `Domain…` constants |
| The bytes actually signed | `signing.Preimage`: the tag `hypernode/v1`, the network id, the domain, each field, each after its 4-byte length |
| The one signing API, with no raw-bytes call | `signing.Signer`, an interface: `Sign`, `SignBase64`, `PublicKey`, `PublicKeyBase64`. `LocalSigner` holds the key in the process, made by `NewSigner`; `RemoteSigner` asks the signer program (step 24) |
| Checking a signature | `signing.Verify`, `signing.VerifyBase64` |
| One spelling per key and signature | `signing.CanonicalBase64`; `ValidatePublicKeyBase64`, a public key in its one spelling that `ValidatePublicKey` accepts, which every key that enters passes; `SignatureBase64Length`, a signature's base64 length, from the scheme's size |
| The key file | `KeyFile` (`publicKey`, and the private key in plaintext or sealed, `EncryptedKey`, never both), `ReadKeyFile`, `KeyFile.Unlock`, `WriteKeyFile` (a synced temporary file renamed into place, then its folder synced); the seal: AES-256-GCM under a key PBKDF2-HMAC-SHA256 stretches from the passphrase, `keyFileIterations` (600,000) times (`sealPrivateKey`, `EncryptedKey.open`, `gcmFor`); `ReadPassphraseFile`, `ReadSecretFile` (a signer client's secret, step 24) |
| A fresh key pair | `cmd/keygen` `main` → `NetworkScheme().GenerateKey`; with `-encrypt`, a new sealed key file, its passphrase from `-passphrase-file`; with `-secret`, a signer client's secret, 32 random bytes in lowercase hex |

**Go:** a name that starts with a capital letter is exported: other packages can use it. `mldsa65`
starts with a lower-case letter, so only package `signing` can name it. An interface lists methods;
any type that has them satisfies it, with no declaration. The code says so anyway, on the type's line
(`type mldsa65 struct{} // implements Scheme`), and a line such as `var _ Scheme = mldsa65{}` makes
the compiler check the claim: it does not build unless the type has
every method, with the same signature. `_` discards the value. For a type whose methods take a
pointer receiver, the line reads `var _ Transport = (*HTTPTransport)(nil)`, a nil pointer of the type.
The interface names its implementations in turn
(`type Scheme interface { // implemented by mldsa65`), with the test doubles in its comment. The
compiler does not check that direction: a new implementation must be added to the list by hand.
`func (signer *LocalSigner) Sign(…)` is a method, and `signer`, its receiver, is the value it is
called on. `*LocalSigner` is a pointer to a `LocalSigner`, so the method works on the caller's
value, not on a copy.
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
- Keys and signatures are standard base64 in one spelling, of the scheme's fixed sizes: with ML-DSA-65,
  a public key is 1952 bytes (2604 characters), a signature 3309 (4412), a private key the 32-byte
  seed (44). Every key that enters, from the genesis, a config, the wallet or a request, passes
  `ValidatePublicKeyBase64`.
- Signing is deterministic, and code outside `signing` signs only through a `Signer`, built for one
  network id, whether it holds the key or asks the signer for each signature.

**Check.** Read `signing/signing_test.go`: `TestCrossDomainSignatureRejected`,
`TestCrossNetworkSignatureRejected`, `TestFieldInjectionResisted`,
`TestVerifyBase64AcceptsOnlyTheCanonicalKeySpelling`,
`TestValidatePublicKeyBase64TakesOnlyAKeyTheSchemeAcceptsInItsOneSpelling`, `TestSignaturesAreDeterministic`,
`TestVerifyRefusesWrongSizesBeforeAnyWork`; then `signing/keyfile_test.go`. Run
`go test ./signing/` and `go test ./cmd/keygen`, and `go run ./cmd/keygen`.

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
- **Files:** `ledger/amount.go`, `ledger/name.go`, `ledger/address.go`, `ledger/ledger.go`, `ledger/digest.go`,
  `ledger/parameters.go`, `ledger/topology.go`. In amount.go, the JSON methods of `Account`,
  `Payment`, `Reassignment` and `LedgerParameters` follow the pattern in this step's Go note;
  `Payment`'s pair is for step 4 and `Reassignment`'s for step 6.

| Concept | Where |
|---|---|
| An amount: exact, a whole number of 0.00000001 units, a string in JSON | `*big.Rat`; `ParseAmount` (the one parser), `FormatAmount` (the one written form); `amountJSON`, `requireAmounts` |
| A name's one spelling | `CanonicalName` |
| A public address's one plain form, `http(s)://host[:port]` and nothing more | `CanonicalAddress`, `addressRule` (address.go) |
| A name's IP addresses: at most 4, each in its one plain form, none twice | `MaxIPAddresses`, `CanonicalIPAddress` (netip's own form; no zone, no IPv4 address in IPv6 dress, no unspecified or multicast address), `ipAddressRule`, `ipAddressesError` (address.go) |
| A name's TLS keys: at most 2, each the SHA-256 of a key's SubjectPublicKeyInfo in lowercase hex, none twice | `MaxTLSKeys`, `CanonicalTLSKey`, `tlsKeyRule`, `tlsKeysError` (address.go) |
| An account, and finding one by name | `Account` (`Name`, `PublicKey`, `Amount`, `Bank`, `Delegate`, `Description`, `PublicAddress`, `IPAddresses`, `TLSKeys`, `Frozen`, `PendingKey`, `PendingBank`, `PendingHeight`); `FindAccount`, `findAccountIndex` (binary search); `AccountAmount`, a name's balance or zero |
| The committed state, its accounts in strictly ascending name order | `LedgerState` (`Accounts`, `Parameters`, `GovernanceSequence`, `Height`), `LedgerState.NamesAscending` |
| The state digest | `LedgerState.Digest`, `AccountsDigest`, `LedgerParameters.Digest`, `lengthPrefixedDigest`; `listFields`, a list as its count and then its items (the IP addresses and the TLS keys, the last of an account's fields) |
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
`Block` (step 8); `TestEveryParameterIsInTheDigest` checks a parameter vote (step 6), and
`TestAddressHasOnePlainForm`, `TestIPAddressesHaveOnePlainForm` and `TestTLSKeysHaveOneForm`
(address_test.go) an update (step 4), the genesis (step 7) and blocks (step 8). Run (20 tests):
`go test ./ledger -run '^Test(ParseAmount|Amount|FormatAmount|RequiredAmount|CanonicalName|AccountsDigest|StateDigest|NamesAscending|EveryParameterIsInTheDigest|AcceptableEnforces|MaxMessageBytesGovernanceFloor|AddressHasOnePlainForm|IPAddressesHaveOnePlainForm|TLSKeysHaveOneForm)'`

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
| An account update | `AccountUpdate` (`Name`, `Delegate`, `Description`, `PublicAddress`, `IPAddresses`, `TLSKeys`, `BlockID`, `Signature`), `SignAccountUpdate`, `VerifyAccountUpdate`, `UpdateID`, `ClearField` (`-`); `applyAccountUpdates`, `applyField`, `applyListField`, `clearsList` |
| A node's receipt | `Receipt`, `SignReceipt`, `Receipt.Verify`; domain `receipt` |

**Later steps take as given:**
- A payment is from a name to a name; only the holder of the payer's current key can sign it. The
  payer pays `Amount`, the payee gets `Amount` less `TransactionCost`, and the cost is burned. It
  applies only in the block its `BlockID` names (step 8), the replay guard.
- One signed content is one payment (`PaymentID`): nodes dedup, relay and acknowledge by it.
- `ProcessPayments` moves value only for an amount of at least the cost between two existing names
  (audit B1, B2). A payer whose payments in one block exceed its balance loses that balance and
  those payments; honest intake (step 11) never lets that happen.
- In an update, `""` leaves a field and `-` clears it. An empty list of IP addresses, or of TLS
  keys, leaves the list, `["-"]` clears it, and any other list replaces it whole.

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
| The shape, checked first | `NameRequestShapeError`, with `signing.ValidatePublicKeyBase64` and `SignatureBase64Length` |
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
| The fixed facts of one chain | `Chain`: `Scheme`, `NetworkID`, `GenesisHash`, `GossipPort`, `ValidatorRule` |
| How the set follows from the state | `ValidatorRule` (`StaticValidators`, or a stake `Threshold`), `StaticValidator`, `ValidatorRule.SetFor` |
| The one-third cap (rule 22) and the power it counts | `withinOneThirdCap`, `powerIgnoringFreezes` |
| The genesis file | `Genesis` (with `GossipPort`, `gossipPort`: required, 1 to 65535, usually 8070), `GenesisValidator`, `GenesisAccount` (`publicAddress`, `ipAddresses`), `genesisParameterNames` |
| Reading and checking it | `ParseGenesis` (every parameter, no unknown key); `ValidateGenesis` (the chain and the state at height 0) |
| The genesis hash and the network id | `GenesisHash`: the name, the gossip port, the state digest and the validator rule; `NetworkID`: the name, `/`, the hash in hex |

**Later steps take as given:**
- The set that signs the block built on a state is `chain.ValidatorRule.SetFor(state)`, with each
  seated name's current key from that state. Every node computes the same set.
- A frozen name is never seated, and in dynamic mode a frozen balance backs no one (R10).
- The network id binds the genesis: a signature made for one genesis fails on another.
- Every validator gossips on one port, `Chain.GossipPort`, at each of its IP addresses. It changes
  only with a new genesis.
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
| The block | `Block` (`Height`, `PreviousBlockHash`, `ValidatorsHash`, `NextValidatorsHash`, `Nonce`, `Content`, `StateDigest`), `BlockContent` |
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
  every item is valid against `previousState`, and the result has the claimed digest and seats the
  set the block names as the next one. It is pure.
- A block's items are judged against the pre-block state, never against each other's effects. A name
  the block freezes, and the ⅔ action's name, send and receive nothing else in it (R6).
- `ProposeBlock` turns any content into a block `ApplyBlock` accepts: it keeps what is valid, checks
  signatures last, and stops at 768 KiB of messages and 8 evidence items.
- The block hash covers the height, the previous hash, the signing set, the next signing set, and a
  sealed digest of the nonce, the state digest and the content hash. So a commit signs off the set
  that signs the next height (step 16).

**Check.** Read `ledger/hash_test.go`, `block_test.go`, `blockid_test.go`, `prevbind_test.go`,
`evidence_test.go`, `messagesize_test.go`, and now the block tests of `payment_test.go`,
`account_update_test.go`, `name_request_test.go`, `governance_test.go` and `reassignment_test.go`.
Run the whole package (130 tests): `go test ./ledger`

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
| A node's configuration | `node.Config`; its `Signer` is step 1's interface, with the key in the process or in the signer (step 24); four of its fields belong to later steps: `MinimumStepTimeout`, the slow-down switch (step 17), `GossipAllow` (step 20), and `PublicPath` and `ZoneSettings` (step 21) |
| What nodes exchange, and how they send it | `Message`, `MessageType`, `ProposalMessage` (a signed proposal and its block); `gossipRoute`, each message's route and body, which the HTTP transport and a bundle share; the `Transport` interface (`Broadcast`; the HTTP one is step 18) |
| The committed state handed to a peer | `Checkpoint`, `Checkpoint.Consistent` |
| The node | `Node`; the fields after "state owned by run()" are touched only by the loop, except two atomic flags, `backupBusy`, which the backup goroutine clears (step 14), and `pullRoundRunning` (step 22); `confirmedHeight` and `haltedBecause`, which the loop sets and the status and catch-up read (step 15); and the public folder's queue, which its writer guards (step 21) |
| One goroutine that owns the state | `Node.run`, `handle`, `enqueue`; the `events` channel; `eventKind`, `event` |
| Inputs | `SubmitProposal`, `SubmitVote`, `AdmitPayment` (mempool.go), `AdmitUpdate` (updatepool.go), `AdmitNameRequest` (requestpool.go), `AdmitParameterCertificate`, `AdmitReassignmentCertificate`; a bundle's import feeds the same ones (step 17) |
| The loop's verdict; reads through the loop | `admission` (`newlyPooled`, `err`), `awaitAdmission`: each `Admit…` returns `(newlyPooled bool, err error)`, whether the loop newly pooled the message or the reason it refused it; `Node.Snapshot` (type `Snapshot`), `Node.GovernanceView`, `Node.ExportBundle` (step 17) |
| The committed state, read off the loop | `committedView` (the state, its digest, the validator set of the height in progress, and the gossip port's allow list, step 20), `publishView`, the `view` field |
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

`AdmitPayment`, `AdmitUpdate` and `AdmitNameRequest` return the loop's verdict: whether the message
was newly pooled, and relayed, or the reason it was refused. One already pooled is neither. A
bundle's import counts with it (step 17).

Some calls come later. `Node.relay` passes a message to the node's peers once (step 19).
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
- **Files:** in `node/node.go`, from `startHeight` to the end; then `node/idle.go`. A halted node
  (step 15) starts no height.

| Concept | Where |
|---|---|
| The candidate and the driver | `startHeight`: the last driver retired (`retireDriver`, step 13); the time the height began, `heightStartedAt` (step 22); a fresh nonce; `ledger.ProposeBlock` over the pools and the proofs and certificates of step 13 (`pendingEvidence`, `pendingParameterChange`, `pendingReassignment`); `consensus.NewDriver`; `Driver.Resume` |
| No work, no height until the heartbeat | `hasWork`, the `idle` field, `wakeIfIdle`; `IdleHeartbeat`, `armIdleHeartbeat`, `onIdleHeartbeat`, the `heartbeatHeight` field (idle.go) |
| Signing only while seated under the node's key | `seatedSigner` |
| The waits after a restart without a sign log | `startFirstHeight`; `waitForOwnSignatures`, at each height the node may have signed at, up to the `mayHaveSignedUpTo` field, which `noteOwnSignature` raises; `ownSignaturesWait`, how long: 2 × the larger of the re-gossip interval and the slow-down switch (step 17) |
| Taking a proposal | `onProposal` (seven checks, cheap ones first; checks 4 and 5, the scheduled proposer and the signature, are `fromItsProposer`, which the pull of step 22 shares; a proposal for a round it already holds one for goes to `reportEquivocation`, step 13; while no driver runs, one in its own name for any round up to `MaxRoundLookahead`; an authentic one whose block does not apply is kept in `refusedBlocks` for the divergence alarm, `noteDivergence`, step 15), `maxInvalidProposals`, `rememberProposal` |
| Taking a vote | the `eventVote` case of `handle`: `holdsVote`, `countable`, `rememberVote`, then `Driver.ReceiveVote`, and `noteDivergence` (step 15); a vote it cannot count goes to `noteOwnSignature` and `recordLateDoubleSign` (step 13) |
| Doing what the driver asks | `process`: sign, log and send a proposal; send a vote; arm a timer; commit |
| Timers | `armTimeout`: `stepTimeout` × (round + 1), for the height that armed it. `stepTimeout` is the governed `StepTimeoutMs`, or the slow-down switch when that is longer (step 17) |
| Committing | `commit`: `ledger.ApplyBlock` on the decided block, the new state, the confirmation noted (`noteConfirmation`, step 15), the driver retired (step 13), the snapshot saved and the sign log emptied (step 14), the record and a frame that starts (`noteFrame`, step 16), the view published, the pools reconciled, peers re-resolved (step 19), the last commit sent, the public folder written (`writePublicFolder`, step 21), the next height after `blockInterval` (at least `ownSignaturesWait` at a height it waits at, `waitForOwnSignatures`) |
| Keeping peers in step | `reGossip`, at each re-gossip tick (step 15), sends `consensusMessages`: what decided the last height (`lastCommitMessages`: its precommits and the decided proposal), then the votes held for the current round, then its proposal, then the other votes held for the height in progress: the past rounds', newest first, then the future rounds', nearest first; within a round, in the order of their slots (`voteSlot`). `sendLastCommit` sends `lastCommitMessages` as the node commits |

**Go:** `switch action := rawAction.(type)` in `process` is a type switch: each case runs with
`action` as that concrete type. `time.AfterFunc(delay, f)` runs `f` on its own goroutine after the
delay; here `f` only enqueues an event.

**Later steps take as given:**
- A node takes at most one proposal per round: from the round's proposer, under the key the height's
  set holds, whose block hashes to the signed hash and applies.
- It counts a vote only for the height in progress, from a member, under the set's key, within
  `MaxRoundLookahead` rounds; a byte-identical copy of a vote it holds costs nothing.
- A commit applies exactly the block the precommits named. With no work, no height starts until the
  idle heartbeat, which then starts it with an empty block. A node that is not seated, or is seated
  under another key, follows and signs nothing.

**Check.** Read `node/window_test.go`, `votes_test.go`, and the tests the command names in
`idle_test.go`, `proposal_relay_test.go`, `replay_test.go`, `http_test.go` and `evidence_test.go`.
Run (22 tests):
`go test ./node -run '^Test(PostCommitWindow|StaleTimer|ReGossipCarries|TheLastCommitCarries|FreshNetwork|IdleChain|IdleHeartbeat|PaymentWakes|PeerVoteWakes|PendingChangeKeeps|FutureBlockId|PaymentInThePostCommitWindow|ProposalFlood|InvalidProposals|SwappedBody|ForgedCopy|ProposalIsTakenOnly|OnlyCountableVotes|RelayedVoteCopy|ProposalLabelledWithOldHeight|ZeroBlockInterval|NodeWithAReplacedKey)'`

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
| Moving to a checkpoint | `fastForward`: the state, the last block's header, the commit and its signing set; the pools reconciled; peers re-resolved (step 19); after a catch-up, the public folder written (step 21) |
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
`go test ./node -run '^Test(NodePersistsAndReloads|NodeReloadsOwnSnapshot|OpenRefuses(Corrupt|AMissing|SignLog)|ValidatorRestarts|StateOutOfNameOrder|SnapshotKeepsOnlyTheLastNonce|SignLog|SnapshotOfAnEarlierNetwork|FailedWriteLeavesNoTemporaryFile|Restarted|TotalOutage|VoteIsReleasedOnlyOnceLogged|ProposalIsSentOnlyOnceLogged|SoleValidatorRestart|ValidatorBackWithoutItsSignLog|BackupMirrorsSnapshot|HungBackupMount|PendingProofSurvivesARestart|RestartMidHeight|NodeBackWithoutItsSignLog)'`

### Step 15. Catch-up, the checkpoint check and the alarms

- **For:** how a node that joins, restarts or falls behind takes a peer's state without trusting the
  peer, and what stops it when its state and the network's disagree. Why:
  [consensus.md](consensus.md#catching-up),
  [consensus.md](consensus.md#onboarding-and-the-checkpoint),
  [consensus.md](consensus.md#the-alarms-and-the-confirmation).
- **Files:** `node/sync.go`, then `node/alarms.go`.

| Concept | Where |
|---|---|
| Joining from bootstrap peers before the first height | `syncThenStart`, `adopt` |
| Catching up while running | `catchUpLoop` (it asks nothing while the node is halted), `catchUpInterval`, `catchUpCheckpoint`: every bootstrap peer, every gossip peer, and every other validator of the committed view's set at its public address (`otherValidatorsPublicAddresses`, through `publicAddressClient`, step 22), each a `catchUpPeer`. `catchUp` (from a height the node may have signed at before it lost its sign log, it waits at the new one first, step 12) returns the reason it did not fast-forward, which a carried checkpoint's import answers (step 17) |
| Asking, then fetching | `askStatuses`: every peer at once, each at the first of its URLs that answers (`fetchStatus`: `heightInProgress` and `lastBlockHash`), the highest first (`peerStatus`); `bestCheckpoint`: a checkpoint (`fetchCheckpoint`, at most `MaxCheckpointBytes`) only from a peer at the node's height or ahead that reports a block the node does not know, and the highest that passes; the clients are `catchUpClient` (step 18) and `publicAddressClient` |
| The check, in order | `trustedFrameOf`: `verifyCheckpoint` against the committed view's set, the trusted frame, or, for a checkpoint that is `selfConsistent` (step 14), the frame a walk reaches (`walkFrames`, step 16). `verifyCheckpoint`: `Checkpoint.Consistent`; `anchorVouches` (names holding more than ⅔ of the trusted frame's power signed, under its keys); `Commit.Verify` against the set the checkpoint carries |
| The alarms | `raiseForkAlarm` (two checkpoints that pass, for different blocks at one height); `raiseDivergenceAlarmAt` (a checkpoint of the node's committed height, checked against the set that signed it, on another block); `divergesFrom`, from `adopt` and `catchUp` (a seated node's height in progress, on another block than its last); `noteDivergence` (more than ⅔ of the set precommit a block `onProposal` refused, a `refusedBlock`), `raiseDivergenceAlarm`. Catch-up's goroutine hands its alarm to the loop as an `eventHalt` |
| Halted | `halt`: the `haltedBecause` field, the driver retired, the status written again; then `startHeight` and `adopt` do nothing, `catchUp` refuses, and `catchUpLoop` asks no one |
| Confirmed | `noteConfirmation`, from `commit`: the `confirmedHeight` field, once a commit holds the node's own precommit while `seatedIn` its set |
| Re-gossip cadence | `reGossipLoop`, `reGossipInterval` (the governed step timeout, never the slow-down switch); `everyInterval`, the timer both loops share, which follows a vote within a second. Each tick is an `eventRegossip`: `reGossip` (step 12), `publishBundle` (step 21) and `pullPublicBundles` (step 22) |

**Go:** `node.transport.(interface{ Peers() [][]string })` is a type assertion: it asks whether the
transport also has a `Peers` method, and its second result says whether it has. `[][]string` is a
slice of slices: one list of URLs for each peer (step 18). `asks.Go(func() {…})` runs each status
request on a goroutine of its own, and `asks.Wait()` waits for them all. `atomic.Pointer[string]`
holds the alarm: the loop stores it once (`CompareAndSwap`), and other goroutines read it.

**Later steps take as given:**
- A node takes a peer's checkpoint only if validators of a frame it trusts, with more than ⅔ of
  their power, signed its commit, and a quorum of the set it carries signed it.
- It asks every peer it knows of at each tick, and a peer that withholds the newest checkpoint does
  not hold it back. A node at its peers' height, on their block, fetches no checkpoint. `adopt` and
  `catchUp` save the checkpoint as the node's snapshot before the node signs on top of it.
- A fork or a divergence halts the node: it signs and adopts nothing more until its operator
  restarts it.

**Check.** Read `node/sync_test.go`, `catchup_test.go`, `onboarding_test.go`, `alarms_test.go`, and
`TestCatchUpKeepsEquivocationEvidence`; `TestNodeFollowsNoRedirect` also posts through the transport
(step 18), and `TestCatchUpGivesUpAURLThatNeverConnectsAfterTwoSeconds`, in
`httptransport_linux_test.go`, runs on Linux only. Run (30 tests on Linux; 29 elsewhere):
`go test ./node -run '^Test(NodeSyncsFromCheckpoint|NodeRejectsUnverifiableCheckpoint|FetchCheckpoint|CatchUp|StragglerCatchUp|DynamicStragglerCatchesUp|VerifyCheckpoint|CheckpointGivingRealNames|AnchorChecksOneVotePerName|CheckpointRecomputes|PaddedCheckpoint|TimingsFollowAVote|NodeFollowsNoRedirect|OnePeerWithholding|CheckpointSignedByTwoThirds|TwoValidCommits|SeatedJoiner|JoinerWithAnAlteredSeed|CheckpointOfTheNodesHeight|ValidCommitOfAnotherBlock|OnlyTheNodesOwnPrecommit|OnlyASeatedNode|HaltedNode)'`

### Step 16. The signature record and the frames

- **For:** the public record of who signed each committed block; then the frames file, the index of
  that record a node walks when the set changed while it was away. Why:
  [architecture.md](architecture.md#the-signature-record),
  [security.md](security.md#the-signature-record),
  [consensus.md](consensus.md#frames-and-the-walk).
- **Files:** `node/record.go`, then `node/frames.go`.

| Concept | Where |
|---|---|
| A block's entries | `RecordEntry` (rolling, with the block's `NextValidatorsHash`), `LifelongRecordEntry` (governance and evidence) |
| File names, by network | `recordPrefix`, `recordDirectory`, `lifelongRecordPath`, `unitPath`, `recordUnitHeights` (100) |
| Writing after each commit | `recordCommittedBlock`, `writeRecordEntry`, `lifelongEntryOf` |
| Deleting whole unit files | `pruneRecord`, `unitEnd`, `unitEnd.dueAt`, `unitFirstHeights`, the `oldestUnitEnd` field |
| Appending safely | `lastCompleteLine`, `placeOfNextLine`, `writeLine` |
| Reading entries back, for the routes of step 18 | `readRecord`, at most `maxRecordEntries` at once; the fetcher serves the unit files as they are on disk (step 21) |
| The frames file | `Frame` (`Height`, `ValidatorsHash`, `Validators`), `framesPath`, `maxFrames` (100), `frameLine`, the `frames` field; `noteFrame`, from `commit`: a line when the next set has other names, or a name under another key, than the set that signed the block, the oldest dropped past `RetainBlocks` or `maxFrames`; `openFrames`, at `Open` (step 21): a line that does not read, or is above the height in progress, dropped |
| The walk | `walkFrames`, from `trustedFrameOf` (step 15): each other validator's public face whose status is ahead (`fetchStatus`), its `hdls/frames` and its record unit files (`fetchLines`, each read up to `MaxCheckpointBytes`; `entryOf`), at most `maxFrames` frames a face; `signsOff`: the record entry of the height before the frame, whose block hash recomputes (`ledger.BlockHashOf`), whose `NextValidatorsHash` is the frame's set's hash, and whose commit more than ⅔ of the frame held signed (`anchorVouches`) and is a quorum of its own set (`Commit.Verify`) |

**Later steps take as given:**
- The record holds who signed each committed block, never payments, updates, holder requests,
  nonces or state digests.
- Each height is recorded once; heights a catch-up skipped are missing. A failed write is logged.
- A walk takes a frame only once a commit, by more than ⅔ of the frame it holds, names that frame's
  set as the next one: nothing a public face serves is taken unchecked.

**Check.** Read `node/record_test.go` but its three route tests and `TestPartialLastLineIsSkipped`,
then `node/frames_test.go`. Run (17 tests):
`go test ./node -run '^Test(RecordKeeps|RecordNever|RecordChain|RecordPrunes|RetentionCut|LongerRetention|PruningLists|RecordFilesAreNamed|RecordUnits|LifelongRecordKeeps|RecordHoldsEachHeightOnce|NodeWithoutASnapshotPath|FramesFile|NodeAwayWhileTheSetChanged|Walk)'`

### Step 17. Bundles, a carried checkpoint and the slow-down switch

- **For:** how nodes keep going when the network between them is cut, which the docs call pigeon
  mode. A bundle is a text file of signed messages that any carrier can move: a USB stick, an email,
  a radio link, a person. A checkpoint can be carried the same way. The slow-down switch lets a
  round wait for a carrier. Why:
  [managing-the-currency.md](managing-the-currency.md#it-outlives-the-internet-pigeon-mode),
  [architecture.md](architecture.md#bundles),
  [consensus.md](consensus.md#the-timers-and-the-slow-down-switch),
  [operating-a-network.md](operating-a-network.md#when-the-network-splits).
- **Files:** `node/bundle.go`; in `node/sync.go`, `ImportCheckpoint`; in `node/node.go`,
  `stepTimeout`.

| Concept | Where |
|---|---|
| A bundle: JSON lines, a header and then one message per line | `BundleHeader` (`Bundle`, `NetworkID`, `From`, `HeightInProgress`), `BundleVersion` (1); `BundleLine` (`Type`, the last segment of the message's gossip route, and `Body`, the JSON that route takes) |
| Its bounds | `MaxBundleBytes` (64 MiB: an export, and what the import route reads), `MaxPublicBundleBytes` (8 MiB: the public folder's file, step 21) |
| Writing one | `EncodeBundle`: the header's line, then each message's line (`gossipRoute`, step 10); it stops before the first line that would pass the bound, so a bundle cut short ends on a whole line |
| What the node would gossip now | `ExportBundle`, made on the loop (the `eventBundleQuery` event) → `bundle`: first `consensusMessages` (step 12), then the pooled certificates, the parameter changes' and then the name actions', each in key order; then, for the operator port's export, the pools: name requests, updates, then payments |
| Taking one in | `ImportBundle` → `importBundle`: the header's version and network id, or the whole bundle is refused; each line decoded as its route decodes its body, within that route's bound (`messageOf`); then the intake of its route: `SubmitProposal` and `SubmitVote` (queued), `AdmitPayment`, `AdmitUpdate`, `AdmitNameRequest`, `AdmitParameterCertificate`, `AdmitReassignmentCertificate`; the answer, `BundleCounts` (`Queued`, `Taken`, `Refused`, `Unreadable`). The pull of step 22 calls `importBundle` with a check for each vote and proposal (`pulledCheck`), and ends the import at the first line that fails it, is unreadable, or that its intake refuses; the operator's import goes on past such a line, and counts it |
| A carried checkpoint | `ImportCheckpoint` → the `eventCatchUp` event, with a channel for the verdict → `catchUp` (step 15), with no walk, which returns the reason it did not adopt it: not ahead, does not verify, diverges, the node is halted, or it is still starting |
| The slow-down switch | `Config.MinimumStepTimeout` (0 for off); `stepTimeout`, the base of `armTimeout`: the larger of the governed `StepTimeoutMs` and the switch; `ownSignaturesWait` (step 12), twice that larger value |

**Go:** `bytes.Cut(bundle, []byte("\n"))` splits at the first newline: the header, and the rest.
`json.RawMessage` keeps a JSON value's bytes undecoded, here a message's body until its type is
known. Go's maps have no order of their own, so `slices.Sorted(maps.Keys(pool))` lists a pool's keys
in order. `max(a, b)` is built into Go.

**Later steps take as given:**
- A message from a bundle is checked and relayed exactly as gossip is. Nothing is trusted for having
  come in a bundle, and the header's `from` and `heightInProgress` are for the people carrying it:
  only its version and network id are read.
- Importing a bundle twice takes nothing new, as a second gossip copy takes nothing.
- The consensus part comes first, and the last commit first of all. So a node one height behind,
  taking the lines in order, decides that height, then takes the next height's votes and proposal in
  its post-commit window. The current round's votes and proposal come next, then the past rounds'
  votes, newest first, then the future rounds', nearest first, so a bundle cut at its bound loses
  the farthest future rounds first, then the oldest past ones.
- A carried checkpoint is verified as catch-up verifies one, with the node's own last committed
  validators as the trusted frame and no walk, and adopted only if it is ahead.
- The switch lengthens the round timers, and the wait after a lost sign log, which a carrier needs
  as much. Re-gossip, catch-up and the idle heartbeat keep the governed timings. No rule reads the
  clock, so a longer timer can't make a node sign twice or take an invalid block.

**Check.** Read `node/bundle_test.go`, `pigeon_test.go`, `slowdown_test.go`, and
`TestCarriedCheckpointIsAdoptedOnlyWhenAheadAndVerified` in `catchup_test.go`; two of them also post
to the operator port's routes (step 18). In the pigeon test, four validators share no network link,
and a test courier carries their bundles; in the second pigeon test, a validator that lost its sign
log waits for the next carry. Run (9 tests):
`go test ./node -run '^Test(Bundle|PigeonCourier|PigeonValidator|SlowDownSwitch|NoRuleReadsTheClock|CarriedCheckpoint)'`

## API/CDN area

### Step 18. The HTTP routes and the two ports

- **For:** the node's two ports, the routes peers and wallets call, and the transport that calls
  them. The operator port serves every route, for the bank's own machines. The gossip port serves
  only the routes validators use. A node with no gossip port serves every route on its one port.
  Why: [api.md](api.md#two-ports-and-the-public-face).
- **Files:** `node/httptransport.go`.

| Concept | Where |
|---|---|
| Sending to peers | `HTTPTransport`: `NewHTTPTransport` (one URL for each configured peer), `Broadcast` (a POST per peer, each on its own goroutine, on the route `gossipRoute` names), `SetPeers`, `Peers`. A peer is the list of its URLs: one for a configured peer, one for each IP address a validator publishes (step 19) |
| One push, and its fallback | `post`: the peer's URLs in order; it tries the next one only when the connection fails: refused, unreachable, or not made within `peerDialTimeout` (2 s, not 1, since Linux sends a connection's first packet again after a second), which `NewHTTPTransport` sets on its client through `peerRoundTripper`. Once a request is sent, its outcome ends it, an answer of any status or the client's 5-second timeout: the peer may have taken the message |
| A peer whose pushes fail | the `peerLogLines` map, keyed by the peer's URLs, each a `peerLogLine` (whether the last line said the pushes fail, and when): `post` logs a line only when the outcome differs from the last line about the peer, and at most one a minute (`peerLogInterval`), never one line per message |
| The client of every request a node or a wallet makes: a timeout, and no redirect followed | `NewHTTPClient` |
| The routes of each port | `Server`, `NewServer`; `Server.Handler`, the operator port's: every route (18); `Server.GossipHandler`, the gossip port's: the nine routes of `addGossipRoutes`, the seven messages validators relay and the status and checkpoint a validator catches up from, and `404` to anything else, a POST of a checkpoint included |
| Messages from peers only | `handleProposal`, `handleVote`, `handleGossipPayment` |
| Messages from wallets only, on the operator port | `handlePay` (a signed `ledger.Receipt` for a newly pooled payment; `202` and none when the receipt could not be signed, as through a signer that is busy with another receipt or down) |
| Messages from wallets and from peers that relay them | `handleAccountUpdate`, `handleNameRequest`, `handleParameterVote`, `handleReassignmentVote`: gossip on the gossip port; on the operator port, the bank's own submissions and the ones its fetcher passes on (step 21) |
| Reads through the loop | `handleAccounts`, `handleCheckpoint`, `handleGovernance` |
| Reads from the committed view | `handleStatus` (→ `statusOf`: the height in progress, the accounts, the network id, the last block's hash, and `confirmed` and `halted`, step 15), `handleResolve`; `NameRecord` (a name's record: its key, bank, freeze, pending change, `publicAddress`, `ipAddresses` and `tlsKeys` when set, description), `nameRecordOf`, `ResolveRecord` (a `NameRecord`, then the committed height and the state digest) |
| The record routes | `handleRecord` (`?from=H`: heights H to H + 99), `handleLifelongRecord`, `recordFrom`; they read through `readRecord` (step 16) |
| What a carrier brings, on the operator port only | `handleExportBundle` (`application/x-ndjson`), `handleImportBundle` (at most `MaxBundleBytes`; `409` with the reason for a refused bundle, else the counts), `handleImportCheckpoint` (`202` once adopted, else `409` with the reason) (step 17) |
| Body bounds and answers | `decode`, `maxBodyBytes` (1 MiB), `maxProposalBytes` (2 MiB), `writeJSON` → `jsonAnswer`, the exact bytes a route answers, which the public folder copies (step 21) |

**Go:** a `sync.RWMutex` guards data several goroutines share: `Lock` for a writer, `RLock` for
readers. `HTTPTransport` needs one because the loop sets the peers (`resolvePeersFromState`, step
19) while the catch-up goroutine reads them (`catchUpCheckpoint` → `Peers`, step 15). A plain
`sync.Mutex`, `peerLogMutex`, guards `peerLogLines`, which the pushes' goroutines share, so two
pushes failing together log one line. `errors.As(err, &connectionError)` asks whether `err` is, or
wraps, a `*net.OpError`, and if so puts it in `connectionError`; its `Op` is `"dial"` when the
connection itself failed. `NameRecord` is embedded in `ResolveRecord` (step 3): its fields come
first in the JSON, as if written out.

**Later steps take as given:**
- A route decodes, calls one node method and answers. A refused payment, update, name request or
  certificate gets `409` and the reason. A proposal or a vote gets `202` once decoded, and the loop
  drops it without a word if it fails its checks.
- `/hdls/pay` returns a signed receipt only for a payment the node newly pooled.
- `/hdls/status` gives the height in progress, the network id, the last block's hash, and whether
  the node is confirmed or halted; `/hdls/checkpoint` the checkpoint of step 10; `/hdls/resolve` the
  committed height and the state digest a pin takes.
- The gossip port serves only the nine gossip routes. A wallet's payment, the account list, a name
  lookup, governance, the record, the bundle routes and a checkpoint's import are the operator
  port's.
- A push goes to the first of a peer's URLs it connects to, and to no other. A push that fails is
  lost, as gossip allows.

**Check.** Read the tests the command names in `node/http_test.go`, `httptransport_linux_test.go`,
`replay_test.go`, `record_test.go` and `account_update_test.go`. Run (23 tests on Linux; elsewhere
21, since `TestTransportGivesUpAURLThatNeverConnectsAfterTwoSeconds` and
`TestTransportPushSurvivesALostConnectionPacket` run on Linux only):
`go test ./node -run '^Test(NodesReachConsensusOverHTTP|ResolveEndpoint|StatusServes|RoutesAnswer405|ProposalRouteTakes|ResolveServes|ResolveTolerates|RecordRoute|LifelongRoute|PartialLastLine|PaymentGossipReceiptAndReplay|RepeatPaymentGetsNoNewReceipt|UpdateRefusalAnswers409|CertificateRefusalAnswers409|TransportTries|TransportLogs|TransportSends|TransportGivesUp|TransportPushSurvives|GossipPortServes)'`

### Step 19. Relay, topology and peers

- **For:** how a message reaches every validator, and how validators find each other on-chain.
  Why: [api.md](api.md#how-messages-travel).
- **Files:** `node/relay.go`, `node/topology.go`, `node/peerdns.go`.

| Concept | Where |
|---|---|
| Passing a message on, once | `Node.relay`, `messageKey`, the `relaySeen` field (cleared at each commit) |
| The Hypernode gossip topology | `Neighbors`, `indexToCoordinates`, `coordinatesToIndex`, with `ledger.GroupParameters` (step 3) |
| Peers from the IP addresses on chain | `resolvePeersFromState`, `resolvedPeerAddresses`: each neighbour's URLs, one for each IP address it publishes, in its order: `http://`, the address and `Chain.GossipPort`, joined by `net.JoinHostPort`; the `configuredPeers` and `peersResolved` fields |

**Go:** `net.JoinHostPort("2001:db8::1", "8070")` gives `[2001:db8::1]:8070`: an IPv6 address gets
its brackets, so the port is not read as part of it.

**Later steps take as given:**
- Each node passes a message it took to its peers once, so a message crosses a multi-hop topology.
- A validator takes its peers from the chain, from the moment it opens, only when every current
  validator has published an IP address; otherwise it keeps its configured peers.
- It pushes to a validator at its IP addresses on the network's one gossip port, trying them in
  order (step 18). No name is looked up.

**Check.** Read `node/topology_test.go`, `peerdns_test.go`, and
`TestOnProposalRelaysFirstSightOnlyNeverDuplicates`. Run (10 tests):
`go test ./node -run '^Test(FullMeshNeighbors|GroupParametersScalesDimension|HypercubeConnected|ConsensusOverHypercube|ResolvePeers|IncompletePeerResolution|PartialPeerAddresses|ValidatorLosingItsAddress|OnProposalRelaysFirstSight)'`

### Step 20. The allow list

- **For:** who may connect to the gossip port: every IP address of every current validator, from
  the chain, but for the node's own and loopback ones, and the entries of the node's config. The
  engine refuses anyone else in its own process,
  at accept, before it reads a byte. It is a cost filter, not authentication: the signatures are.
  Why: [security.md](security.md#the-two-ports-and-the-public-face),
  [operating-a-network.md](operating-a-network.md#network-exposure).
- **Files:** `node/allowlist.go`; in `node/node.go`, `publishView` again.

| Concept | Where |
|---|---|
| The list | `gossipAllowList`: every IP address of every validator of the committed view's set, as a one-address prefix, but for those the node's own account publishes (`selfName`, from `publishView`'s `node.self`) and loopback ones; and the prefixes of `Config.GossipAllow`, kept as they are. It looks up no name |
| When it changes | `publishView`, at `Open` and after each commit and catch-up: the `gossipAllowList` field of `committedView`, swapped in with the rest of the view |
| Refusing at accept | `allowListListener`, made by `NewAllowListListener` around the gossip port's listener (step 23); `Accept` closes a connection from an address not on the list, or whose two ends are one address (`bothEndsOneAddress`: a forwarder inside the engine's own network namespace), and takes the next one; `allows` (an IPv4 address in IPv6 dress counts as the IPv4 address, `Unmap`) |
| Refusals in the log | the `refusals` count, and by address (`refusedFrom`, `countRefusalFrom`, at most `maxReportedAddresses`); `reportRefusals`, one line a minute (`refusalReportInterval`) while there are any, with the address refused the most and the names that list it, each marked a validator or not (`claimantsOf`) |
| A connection whose address leaves the list | `allowListConnection`: its `Read` checks the address again after each read, and closes the connection once the address has left the list |

**Go:** `allowListListener` embeds `net.Listener` (step 3): it takes `Close` and `Addr` from it, and
its own `Accept` stands in for the embedded one. `prefix.Contains(address)` asks whether an address
is in a prefix such as `198.51.100.0/24`. `atomic.Int64`'s `Add` and `Swap` change the count in one
step, so goroutines share it with no lock.

**Later steps take as given:**
- A connection from an address on no list is closed before any HTTP is read, and so is one whose
  two ends are one address: there is one engine per IP address.
- A validator that leaves the set, or publishes other addresses, is shut out at the next commit,
  even on a connection it keeps open.
- The list trusts the address the connection comes from, so the gossip port must see each caller's
  own address. Behind a front that rewrites it to the node's own address, a loopback one, or the
  address of a forwarder inside the engine's own network namespace, every caller is refused. A
  forwarder outside it, as Docker's userland proxy is, is not caught: its address must never be on
  the list.
- The list guards the gossip port only. The operator port has none, and must stay on the bank's own
  network.

**Check.** Read `node/allowlist_test.go`. The end-to-end test runs four validators on `127.0.0.1` to
`127.0.0.4`, each sending from its own address. Each lists the first three's addresses in its
`allow`, since the list leaves loopback addresses from the chain out. The fourth, whose address only
the chain lists, is refused by the three, which commit without it. Run (6 tests):
`go test ./node -run '^TestAllowList'`

### Step 21. The public face: the public folder and the fetcher

- **For:** what the public reads, and how. The engine writes the public folder, and `cmd/fetcher`, a
  small program with no key and no state, serves it behind a CDN: status, checkpoint, accounts,
  governance, name lookups, the zone file, the public bundle, the frames and the record files. It
  passes three customer submissions on to the engine's operator port, and names who sent the most.
  Why: [api.md](api.md#the-public-face), [architecture.md](architecture.md#the-public-folder),
  [governance.md](governance.md#names-as-a-name-service),
  [operating-a-network.md](operating-a-network.md#the-public-face).
- **Files:** `node/publicfolder.go`, `node/zone.go`; `cmd/fetcher/main.go`.

| Concept | Where |
|---|---|
| Where, and which files | `Config.PublicPath`; `hdls/status`, `hdls/checkpoint`, `hdls/accounts`, `hdls/governance`, `hdls/bundle`; `hdls/resolve/name/<name>` and `hdls/resolve/key/<SHA-256 of the key's bytes, hex>` (`nameRecordFolder`, `keyRecordFolder`, `nameRecordPath`, `keyRecordPath`); `hdls/resolve/ip/<address>` (`ipLookupFolder`, `ipLookupPath`) and `hdls/zone` (`zonePath`), in zone.go; `hdls/frames` (step 16) |
| A name's record as a file | `publishNameRecord`: a `NameRecord` (step 18) alone; it leaves out the committed height and state digest, which change at every block, so every bank of one chain writes the same bytes for a name |
| At Open | `openPublicFolder`: `.incoming` cleared, the folders, the frames read back (`openFrames`, step 16), then every file once, but a record file or an IP lookup file that already holds its content byte for byte (`openNameService`, which also takes the zone file there as the one last written); the resolve files of no current name, key or address are removed |
| After a commit or a catch-up | `writePublicFolder` (from `commit` and `fastForward`): the files of a gone name and of a changed key removed, the changed records written, the changed IP lookup files (`writeIPLookups`), then `publishCommittedState`: `hdls/checkpoint`, `hdls/accounts`, `hdls/zone` (`publishZone`), `hdls/governance`, `hdls/bundle`, and `hdls/status` last |
| The IP lookup | `ipClaims`: for each address a name lists, the names that list it, in name order, a frozen one as `<name> (frozen)`; one JSON list per address |
| The zone file | `ZoneSettings` (`Zone`, `ZoneNameServers`, `ZoneContact`, `ZoneTimeToLive`, read by step 23), `Zone` → `zoneParts` and `zoneSerial`: the SOA and NS records; each name that is not frozen, its A or AAAA records (`addressRecordType`), its TXT `hdls-public-address`, and, for an `https://` public address, a TLSA record for each TLS key (`tlsPort`); `validatorsLabel`, every validator's address once, loopback left out; the `_ip` lookup, at most `maxZoneClaimants` names an address; `characterStrings`, a TXT record's quoted strings. `publishZone` writes it only when it differs from the last one apart from the serial (`sameApartFromSerial`, the writer's `zone` field) |
| Between commits | `publishGovernance`, when a certificate is newly pooled (step 13); `publishBundle`, at each re-gossip tick: the public bundle, `node.bundle` without the pools, at most `MaxPublicBundleBytes` (step 17), queued only when its bytes differ from the last ones queued (the `publishedBundle` field) |
| Content made on the loop | `publishAnswer` (→ `jsonAnswer`, step 18), `publishNameRecord` |
| Writing off the loop | `publicFolderWriter`: `write`, `remove`, `queueChange` (a file queued again before its write starts keeps its place and takes the newer content), `writeQueue` (one file at a time), `writeFile` (through a temporary file of its own in `.incoming`, from `os.CreateTemp`, and a rename; no fsync), `waitUntilIdle` (which `Stop` waits on) |
| The fetcher | cmd/fetcher `main` (`-listen`, `-folder`, `-record`, `-lifelong`, `-engine`, `-max-submissions-per-second`, 100 by default, and `-client-address-header` with `-cdn-addresses`), `Fetcher`, `NewFetcher` (the listed paths, each with its type and its `Cache-Control`; `hdls/frames` and `hdls/zone` among them) |
| Only the listed paths | `ServeHTTP` (`404` for a path not in its one clean form), `handleResolveName` (`ledger.CanonicalName`), `handleResolveKey` (`keyPattern`), `handleResolveIPAddress` (`ledger.CanonicalIPAddress`), `handleRecordUnit` (`recordUnitPattern`; `immutable` once the unit is complete, by the status's `heightInProgress`), `serveFile` (regular files only, through `http.ServeContent`, with an `ETag` of the size and the modification time in nanoseconds, and no `Last-Modified`; a missing file is `404` with `max-age=1`) |
| Submissions | `handleSubmission`: `no-store`; the global cap (`submissionLimit`, `allow`: `429` with `Retry-After: 1`); the body read whole, at most 1 MiB (`maxSubmissionBytes`; `413` above it); then the engine's operator port through `httputil.ReverseProxy` (`engineProxy`), whose answer comes back unchanged |
| Who sent the most | `allow` counts each sender (`senders`, at most `maxCountedSenders` addresses) and whether the cap refused one; `reportSenders`, once a minute (`sendersReportInterval`) after a `429`: the `reportedSenders` addresses that sent the most, each with `namesListing` (its IP lookup file); `clientAddress`: the connection's address, or, on a connection from `cdnAddresses`, the last entry of the CDN's header; `addressPrefixes` |

**Go:** a `sync.Cond` lets a goroutine wait for another: `idle.Wait()` unlocks the mutex, sleeps
until another goroutine calls `idle.Broadcast()`, then locks it again. A route pattern such as
`"GET /hdls/resolve/name/{name}"` serves only GET (and HEAD), and `request.PathValue("name")` gives
the segment. `http.ServeContent` sends a file, and answers HEAD, ranges and If-None-Match; given the
zero `time.Time{}`, it sends no Last-Modified.

**Later steps take as given:**
- `hdls/status`, `hdls/checkpoint`, `hdls/accounts` and `hdls/governance` are the answers of their
  routes, byte for byte, and keep their paths. So a wallet or a joining node reads a public face as
  it reads an operator port.
- No reader sees a partial file. A status never names a height before the record holds it, so the
  fetcher never takes a growing record unit as complete.
- The fetcher never looks inside a submission: the engine checks it. It holds no key.
- The zone file and the IP lookup files follow from the committed accounts and the validator set,
  so anyone can rebuild them from `hdls/accounts`; the zone file matches but for its serial.
- A cache can serve an old checkpoint, never a forged one: its ⅔ commit verifies. The other files
  are the bank's word, and a bundle's messages are each signed.

**Check.** Read `node/publicfolder_test.go`, `zone_test.go` and `cmd/fetcher/main_test.go`;
`TestNamedCheckzoneTakesTheZone` runs BIND's `named-checkzone` on a zone file, and skips where it is
not installed. Run (20 tests):
`go test ./node -run '^Test(PublicFolder|NoPartialPublicFile|NodeCatchesUpFromThePublicFolder|OpenRefusesAPublicFolder|TwoBanksPublish|PublicBundleIsNot|TwoWriters|Zone|NamedCheckzone|IPLookupFiles)'`
and (13 tests) `go test ./cmd/fetcher`.

### Step 22. The second path: pulling public bundles

- **For:** how validators whose gossip is cut, but whose public faces still answer, exchange votes
  and proposals all the same. The pull goes out, never in, so the allow list is not involved. Why:
  [operating-a-network.md](operating-a-network.md#the-second-path-public-bundles).
- **Files:** in `node/sync.go`, `pullPublicBundles`, `pulledCheck`, `verifiedVotes`,
  `otherValidatorsPublicAddresses`, `publicAddressClient`, `publicAddressTransport`,
  `publicAddressDialer`, `dialPublicAddress`, `refuseLoopback` and `fetchPublicBundle`; in
  `node/node.go`, the `eventRegossip` case of `handle`, `fromItsProposer`, the fields
  `heightStartedAt`, `pullingPublicBundles`, `pullRoundRunning` and `pulledVotes`, and the committed
  view's `lastCommitValidators`; in `node/bundle.go`, `importBundle`.

| Concept | Where |
|---|---|
| When a height is stuck | `pullPublicBundles`, at each re-gossip tick, after `reGossip` and `publishBundle`: a driver runs, and the height began (`heightStartedAt`, set by `startHeight`) at least 4 re-gossip intervals ago: the governed step timeout, never the switch's |
| Whom it asks | every other validator of the committed view's set that publishes a public address (`otherValidatorsPublicAddresses`, which catch-up and the walk share, steps 15 and 16) |
| One round of fetches at a time, off the loop | `pullRoundRunning`; a goroutine for each validator, with `publicAddressClient`: `NewHTTPClient`'s 5 seconds and no redirect, and `publicAddressTransport`, whose dialer, `dialPublicAddress`, refuses the node's own machine. Its `publicAddressDialer` gives up a connection after `peerDialTimeout`, and refuses a loopback destination, or `0.0.0.0` or `::` (`refuseLoopback`), once a name is resolved; then `dialPublicAddress` drops a connection whose two ends are one address (`bothEndsOneAddress`, step 20), before a request is sent. No proxy is used |
| One fetch | `fetchPublicBundle`: GET `<PublicAddress>/hdls/bundle`, a `200` only, at most `MaxPublicBundleBytes`; then `importBundle` (step 17) with `pulledCheck` |
| Each vote and proposal checked off the loop | `pulledCheck`: of the height in progress, against the view's set, or of the last committed height, against `lastCommitValidators`. A vote is by a validator of that set, and its signature verifies under the set's key; a proposal names the proposer the set schedules for its round, with a signature that verifies (`fromItsProposer`, step 12). The first line that fails, is unreadable, or is refused ends the import. A bank one height ahead gives its last commit, and the import stops at its first message of the height after |
| Each vote verified once a height | `verifiedVotes`, the `pulledVotes` field: `verifiedOnce` remembers each vote that verified, whole, under a mutex, so that the pulls verify it once at a committed height; it starts again when the committed height changes, or at `maxVerifiedVotes` (4,096) |
| The log | `pullingPublicBundles`: one line when the node starts pulling, one when it stops |

**Go:** `pullRoundRunning.CompareAndSwap(false, true)` sets the flag only if it was false, in one
step, and says whether it did: so a tick that finds a round still running skips its own. A
`net.Dialer`'s `Control` function runs just before a connection is made, on the address it goes
to: an error from it stops the connection.

**Later steps take as given:**
- Every message pulled goes through the intake of its route, as gossip does; a vote or a proposal
  passes the pull's check first, off the loop, and the import stops at the first line that fails,
  is unreadable, or is refused.
- An idle node runs no driver, so it never pulls. A public face that hangs holds a round up for 5
  seconds at most.
- No fetch from a public address reaches the node's own machine.

**Check.** Read `node/secondpath_test.go`, and the public address tests of `node/sync_test.go`: four
validators whose transports deliver nothing commit by pulling each other's public bundles; one
behind catches up through the public addresses; a pulled bundle's import stops at a vote, or a
line, that fails; a vote is verified once a height; a fetch to the node's own machine is refused,
at a loopback address or at another of its own. Run (8 tests):
`go test ./node -run '^Test(PublicBundlesArePulled|ValidatorsWhoseGossipFails|ValidatorBehindCatchesUp|PulledPublicBundle|PullVerifies|PublicAddressFetches)'`

### Step 23. The commands and the images

- **For:** the programs that run a node, the demo and the quickstart, and the container images.
  The signer program is the next step. Why: [operating-a-network.md](operating-a-network.md).
- **Files:** `cmd/server/config.go`, `cmd/server/main.go`, `cmd/demo/main.go`,
  `cmd/quickstart/main.go`; then `deploy/Dockerfile`, `deploy/ui.Dockerfile`, `deploy/compose.yaml`,
  `.dockerignore`, and the `Makefile`'s `images`, `quickstart` and `quickstart-clean`.

| Concept | Where |
|---|---|
| The node config file | `fileConfig`; among its fields `listen` (the operator port; if empty, `127.0.0.1:8080` beside `gossipListen`, and `:8080` without it), `gossipListen`, `allow`, `publicPath`, `minimumStepTimeoutMs`, the key's: `privateKey`, or `signer` (`signerConfig`: `url`, `client`, `secretFile`), and the zone's: `zone`, `zoneNameServers`, `zoneContact`, `zoneTimeToLive` |
| Reading it and its genesis | `load`: unknown keys refused; the public key's base64, size and spelling, and the name; `allow` refused without `gossipListen`, and each entry read by `allowPrefixes`; `minimumStepTimeoutMs` from 0 to `ledger.MaxTimingMs`; the zone settings (`zoneSettingsOf`: domain names, `domainName`; a zone of at most `maxZoneLength` characters; no name server inside the zone; a `publicPath`); `ledger.ParseGenesis`, `ledger.ValidateGenesis`; `snapshotPath` required on a node that can be a validator; defaults; then the key's signer (`keySigner`: exactly one of `privateKey`, a `signing.NewSigner` that must give `publicKey`, and `signer`, a `signing.NewRemoteSigner` with the client's secret from `signing.ReadSecretFile`, step 24), and `keySelfTest`: a signature over the node's name, which must verify under `publicKey`. It returns the node's config, `listen`, `gossipListen` and the peers |
| Running a node | cmd/server `main` → `bindThenStart`: `listen` and `gossipListen` bound first (`net.Listen`), so a second start on either address stops before it signs; then `node.Open` and `Node.Start`; the gossip listener put behind the allow list (`node.NewAllowListListener`, step 20). `main` then logs the switch when it is on, and `serve` serves `Server.Handler` on `listen` and `Server.GossipHandler` on `gossipListen`, each an `http.Server` with timeouts. Without `gossipListen`, `listen` serves every route, and the log says the gossip routes are open to anyone. With it, `logPortWarnings` logs a line for an operator port on every interface, and one for a gossip port off the genesis's `gossipPort`. `validatorsLabel` for the startup log |
| The demo network | cmd/demo `main`, `demoGenesis` (the founder publishes `-listen`'s IP address, or `127.0.0.1` when that is `0.0.0.0` or `::`, and `http://` with that IP address and `-listen`'s port as its public address; the gossip port is the node's own port), `demoNodeConfig` and `demoHTTPServer` (cmd/server's limits and timeouts), `newKeyPair`, `writeWallet`, `startWalletUI`, `walletArguments` |
| The quickstart network | cmd/quickstart `main`, `writeNetwork` (the four `banks`, each with a fixed IP address and its fetcher as its public address; two `customers`, each with a wallet port of its own; the genesis checked as a node checks it before anything is written; a new folder, never one that is there), `writeBank` (each bank's key file, sealed, and its passphrase; a secret for each of its signer's clients, `signerClients`: the engine, and the bank's wallet with the desk's and the admin page's domains; its signer's config, `signerConfig`, and its logs' folder; its engine's config, `nodeConfig`, which names the signer, `engineSignerConfig` and `bank.signerURL`, and holds no key; its wallet's file, with the public key only, and its wallet's password), `walletCommand` (the command it prints for each customer, with `-listen`), `genesisFile`, `walletFile`, `writeJSONFile`, `writeFile` |
| The images and the compose file | `deploy/Dockerfile` (`server`, `signer`, `fetcher` and `keygen`, static, in a `scratch` image run as user 65532), `deploy/ui.Dockerfile` (the wallet), `deploy/compose.yaml` (each bank an engine at `172.28.70.11` to `.14` and a fetcher at `.21` to `.24`, published at `127.0.0.1:18081` to `18084`; its signer on an internal network of the bank's own, which only its engine and its wallet join; and its wallet at `.31` to `.34`, published at `127.0.0.1:18091` to `18094`, which asks for its bank's wallet password, a secret; the details are in [operating-a-network.md](operating-a-network.md)), `.dockerignore` (keeps the quickstart's keys, and the wallet's files under `ui/`, out of the build) |

**Later steps take as given:**
- `cmd/server` refuses a config it cannot run safely: a key of the wrong size or spelling, both or
  neither of `privateKey` and `signer`, a private key that is not the public key's, a signer that
  holds another key or signs for another bank (or does not answer), a validator without a snapshot
  path, an allow entry not written as the gossip port compares it, a zone setting it cannot write.
- Every node of a network reads the same genesis file and logs the network id it makes.
- With `gossipListen`, the gossip routes are served only behind the allow list. The operator port
  has no list: it belongs on the bank's own network, never published, and beside a gossip port it
  listens on `127.0.0.1:8080` unless the config says otherwise.

**Check.** Read `cmd/server/config_test.go` (`TestKeygenOutputLoads` ties step 1 to this one),
`cmd/server/main_test.go`, `cmd/demo/main_test.go` and `cmd/quickstart/main_test.go`, which also
reads `deploy/compose.yaml`. Run `go test ./cmd/server ./cmd/demo ./cmd/quickstart`, and
`go run ./cmd/demo` to see a node run; it prints the wallet command, and Ctrl-C stops it.
`make demo`, which builds the wallet too, is step 27's. `make quickstart` builds the images and
runs the four banks with Docker or Podman;
[operating-a-network.md](operating-a-network.md#four-banks-on-one-machine-the-quickstart) gives the
commands.

## Signature area: the signer

### Step 24. The signer program

- **For:** the program that holds a bank's one key and signs for the bank's own programs, its
  clients; then the engine's and the wallet's side of it. It comes after the API/CDN area because it
  checks every message the ledger signs, field by field (steps 2 to 8), and the engine reaches it
  from its event loop (steps 10 and 12). Why: [signature.md](signature.md),
  [api.md](api.md#the-signers-route), [consensus.md](consensus.md#signing-only-while-seated).
- **Files:** `cmd/signer/main.go`, `cmd/signer/layout.go`, `cmd/signer/signlog.go`,
  `cmd/signer/service.go`, `cmd/signer/lock_unix.go`, `cmd/signer/lock_other.go`; then
  `signing/remote.go`.

| Concept | Where |
|---|---|
| Starting | `main` (`-config`, `-resume-above`) → `start`: `load` (unknown keys refused; the required fields; `allowFromPrefixes`), then the listen address bound, then the sign log's folder locked (`lockFolder`, an exclusive `flock`, so a second signer on that folder stops before it touches the log; on systems without `flock`, `lock_other.go` locks nothing), and only then `newSigningService` |
| Its config | `fileConfig` (`listen`, `allowFrom`, `genesis`, `name`, `keyFile`, `passphraseFile`, `signLogPath`, `auditLogPath`, `clients`), `clientConfig` (`name`, `secretFile`, `domains`) |
| The key | `readKey`: `signing.ReadKeyFile` (step 1), a sealed key with its `passphraseFile` (`signing.ReadPassphraseFile`), or a plaintext one, for tests; the file's public key must be the one its private key derives. Nothing checks it against the chain here: the engine's self-test does (step 23) |
| The clients | `readClients`: each a name, a secret of its own (`signing.ReadSecretFile`; two clients with one secret are refused), and only domains that have a layout; `client` |
| Who may connect | `allowFromListener`: `Accept` closes a connection from an address not in `allowFrom` before it reads a byte; `reportRefusals`, one line a minute while there are any |
| One request | `SigningService.ServeHTTP` (`POST /sign` only) → `handleSign`: the body read before the lock, at most `maxRequestBytes` (2 MiB); one request at a time (`mutex`); a request whose client has gone is dropped unanswered; `sign`; the audit line (`writeAuditLine`, `auditEntry`) before the answer, and no signature without it; a `refusal` carries its status |
| The rules, in order | `sign`: the client and its HMAC (`parseAuthorization`, `bodyHMAC`, `hmac.Equal`); the domain in its list (`keySelfTest` for every client); the fields against the domain's layout, which names the slot; for a vote or a proposal, the slot (`takeSlot`); then `keySigner.Sign` |
| The layouts | `layouts`, one per domain, field for field the `signFields` of the message that signs in it: `checkVote`, `checkProposal`, `checkReceipt`, `checkNameRequest`, `checkPayment`, `checkAccountUpdate`, `checkParameterVote`, `checkReassignmentVote`, `checkKeySelfTest`; `fits` and the field forms (`isDecimal`, `isRound`, `isVoteType`, `isHash`, `isAmount`, `isFlag` and the rest); `requireOwnName` (`403`), `malformed` (`400`) |
| The slot | `slot` (the domain, the height, the round, a vote's type); `takeSlot`: the same fields again pass; other fields are `REFUSED CONFLICT` (`conflictMark`), with both requests in the audit line; a height at or below the `-resume-above` one, or below the highest signed, is refused; a new slot is written to the sign log first (`signLog.record`), or the program stops (`stopProgram`) |
| The sign log | `signLog`, `signLogLine`: a first line naming the network and any `-resume-above` height, then one line per slot and its fields; `openSignLog` (a symbolic link refused; another network's log refused; a torn last line dropped, since it was never fsynced; the log written back at each start), `record` (one appended, fsynced line; at a new height, a `rewrite` that keeps the highest height and the one below), `appendLine`, `rewrite` (a temporary file, fsynced, renamed over the log, then its folder fsynced) |
| The engine's and the wallet's side | `signing.RemoteSigner`, from `NewRemoteSigner`: `Sign` → `startRequest` (no request for a second after a failure, `remoteFailFastWindow`; one receipt at a time, `receiptInFlight`; the slot memory, `claimSlot` and `slotOf`: a slot is sent with one set of fields only), `request` (the body, the `Authorization` header, a client with no proxy and a deadline of a second, `remoteRequestTimeout`; the answer verified under the bank's key), `finishRequest` (a log line when the requests start failing and when the signer answers again, at most one a minute) |

**Go:** `//go:build unix && !solaris && !aix` on the first line of `lock_unix.go` compiles that file
only on those systems; `lock_other.go` has the opposite condition, so the program builds everywhere.
`syscall.Flock` takes a lock the kernel drops when the process ends, however it ends.
`hmac.Equal` compares in constant time, so how long it takes says nothing about how much of an HMAC
matched. `[][]byte` fields travel in JSON as base64 strings: encoding/json writes a `[]byte` that
way.

**Later steps take as given:**
- The signer signs only for its own network and in its bank's name, only the fields of a domain's
  layout, and only for a client whose list holds the domain. It signs a vote or a proposal at most
  once per slot, on disk first.
- Every answer is written to the audit log before it goes out; a signature whose line can't be
  written is withheld; a sign log that can't be written stops the signer.
- The engine and a bank's wallet check each signature under the bank's public key, so a forged
  answer is an error. A vote that can't be signed is dropped and cast again (step 9).

**Check.** Read `cmd/signer/signer_test.go`; `layout_test.go`, which signs in each domain as the
code does and checks the layout takes it; `engine_test.go`, where four engines commit through four
signers, one bank's signer goes down and comes back, and a compromised engine asks for two votes in
one slot; `quickstart_test.go`; and `signing/remote_test.go`. Run (24 tests)
`go test ./cmd/signer`, and (3 tests)
`go test ./signing -run '^Test(TheRemoteSigner|TheSlotMemory)'`.

## UI area

### Step 25. The key file, the pin and the verified state

- **For:** what the wallet keeps on disk, and which state it believes: its node's, or a checkpoint
  from a file someone carried in. Why: [ui/README.md](../ui/README.md#its-files),
  [ui/README.md](../ui/README.md#how-it-works).
- **Files:** `ui/keyfile.go`; in `ui/wallet.go`, from the top to `saveState`, then `PublicKey`,
  `pinnedNetworkID`, `signer`, `nameOfKey`, `registeredName`; then `ui/trust.go`; then
  `ui/tlspin.go`.

| Concept | Where |
|---|---|
| The key file and its passphrase | `loadOrCreateKey`, over `signing.KeyFile` (step 1): a missing file makes a new key; a plaintext key is sealed once a passphrase is given; a file with the public key only is refused, unless the wallet signs through a signer; `getPassphrase`, `newPassphrase`, `interactive`, `readSecret`, `passphraseEnvironmentVariable` |
| The wallet | `Wallet`, `LoadOrCreateWallet` → `loadWallet` |
| A bank's wallet through its signer | `signerSettings` (`-signer-url`, `-signer-client`, `-signer-secret-file`, all three or none); `readSignerClient`: the wallet file holds the bank's public key only, `{"publicKey": "<base64>"}`, and the client's secret comes from `signing.ReadSecretFile`; a wallet file with a private key is refused beside a signer |
| What survives a restart | `walletState` (`<keyfile>.state`), `readWalletState`, `saveState` |
| The pin | `walletPin` (`<keyfile>.trust`), `loadPin`, `savePin` |
| One verified state | `Wallet.newestVerifiedState`, and `verifiedState`, which leaves out where the state came from: `nodeCheckpoint` (the node's `/hdls/checkpoint`, at most `node.MaxCheckpointBytes`; `reached` is false for no answer or an HTTP error status), then `verifyUnderPin`: `Checkpoint.Consistent`; at first use `servedNetworkID` and `Commit.Verify`, later `signedByPinned`, and `errBanksChanged` when only the set the checkpoint carries signed it; never older than `NewestHeight` (`errOlderState`) |
| The newest state verified | the `newestCheckpoint` and `newestSource` fields ("node" or "file"), in memory only, which `verifyUnderPin` sets under the same lock as its height check. `newestVerifiedState` gives that state in place of the node's, with its source (`keptFrom`), only when the node's state is older (`errOlderState`), or, for a file, when the node's checkpoint can't be read; any other refusal as it is |
| The pin follows the chain | `Wallet.movePin`, called by `verifyUnderPin` when the state carries another set and its commit is a quorum of it too; `replacePin` |
| A checkpoint from a file | `Wallet.LoadCarriedCheckpoint`: a pin first (`pinnedNetworkID`), a text no longer than `node.MaxCheckpointBytes` (`errCheckpointTooLarge`), then `verifyUnderPin`, as for the node's, which keeps it as the newest state verified, from a "file" |
| Signing for the pinned network; the wallet's name | `Wallet.signer` (a `signing.LocalSigner`, or, through a signer, a `signing.RemoteSigner`, step 24), `pinnedNetworkID`; `nameOfKey`, `registeredName` |
| A bank's web server pinned by the chain | `tlsPinningTransport`, the transport of the wallet's clients (set in `loadWallet`): `RoundTrip`; `banksWithTLSKeysAt`, the banks of the newest verified state, not frozen, whose public address is the request's and that list TLS keys; `checkBankWebServer`, on each new connection: the SHA-256 of the certificate's key must be one that each such bank lists, and no certificate authority is asked. Any other request takes the standard check |

**Go:** the wallet serves several pages at once, so `Wallet.mutex`, a plain `sync.Mutex` (`Lock`
and `Unlock`, see step 18), guards its shared fields.

**Later steps take as given:**
- At first use, when `<keyfile>.trust` does not exist, the wallet believes the state its node serves if
  the validators that state carries signed it, for the network id the node serves, and that id
  starts with the `-network` name and `/`. It then pins the id and those validators (trust on first
  use).
- From then on, or from the start when the operator's list is in that file, the wallet believes a
  state only if pinned validators with more than ⅔ of the pinned power signed it under their pinned
  keys.
- The pin then moves to the validator set that state carries, if its commit is a quorum of that set
  too: only to a set more than ⅔ of the pinned power signed for. The network id never moves.
- It never believes a state older than the newest state it verified. While it runs, that newest
  state stands in for an older one the node serves; after a restart, such a node is refused.
- A checkpoint from a file is believed by the same rules, and moves the newest height and the pin as
  the node's state does. It needs a pin: a file names no network to pin on first use. It stands in
  only while the node's checkpoint can't be read, or the node's state is older; any other refusal of
  the node's state is shown as it is.
- It signs only for the pinned network id, so only after it has read a verified state.
- Its name is the account whose current key is the wallet's key. A bank's wallet through its signer
  holds no private key: it asks the signer, and checks each answer under the bank's key.
- Once a state verifies, an `https://` request to the public address of a bank that lists TLS keys
  is answered only by a server whose key is one of them.

**Check.** Read `ui/keyfile_test.go`, `ui/trust_test.go`, `ui/signer_test.go`, whose desk test
builds the real `cmd/signer` and runs it (it skips under `-short`), and `ui/tlspin_test.go`. Run
(32 tests):
`go test -C ui -run '^Test(KeyFile|PlaintextKey|WalletBelieves|OperatorPin|PinnedQuorum|WalletPin|WalletRefusesA|FirstUse|WalletBuildsItsSigner|PinFollows|MovingPin|PinDoesNotMove|BanksChanged|CheckpointFromAFile|CarriedCheckpointDoesNot|UnreadableNode|OlderCopy|StoredFileBelow|LargeCheckpointFile|CheckpointTextPast|WalletTakesAKeyOrASigner|BanksDeskSignsThroughItsSigner|ChangedTLSKeys)' ./...`

### Step 26. The wallet's actions

- **For:** everything the wallet signs: payments, sent or saved as text, updates, freezes, name
  requests and governance votes. Why: [ui/README.md](../ui/README.md#how-it-works),
  [ui/README.md](../ui/README.md#the-payment-counter).
- **Files:** the rest of `ui/wallet.go`, `ui/requests.go`, `ui/admin.go`.

| Concept | Where |
|---|---|
| Typed input | `typedName`, `typedKey`, `parseAmount`, `fingerprint` |
| The payment counter, counted from the verified state's height | `nextPaymentBlockID`, `keepRequestBlock`, `blockSlack` |
| The height the node reports, for the requests, and for a freeze when no state verifies or a kept state stands in | `Wallet.height` (→ `Wallet.status` → `readStatus`) |
| Paying | `ReviewPayment` (shows the payee's bank, signs nothing), `Send` (→ `signPayment`; posts to `/hdls/pay`, checks the receipt; while a file's state stands in, it refuses before it signs, as `UpdateAccount` does), `pinnedKey`; `signPayment`: the checks of `ReviewPayment`, a block of the counter, the signature |
| Saving a payment as text | `SavePayment` (→ `signPayment`; `node.EncodeBundle` with a `node.BundleHeader`: the pinned network id, the wallet's name, the state's height in progress), `savedPayment`; the payment saved last, the `lastSaved` field, which `lastSavedPayment` gives |
| Updating; freezing | `UpdateAccount` (a description, a delegate, a public address, IP addresses and TLS keys, each list separated by spaces); `Freeze` (while a kept state stands in, its height in progress is the larger of that state's and the node's, `Wallet.height`), `freezeStatus`, `lastName`, `pendingChange` |
| Posting to the node | `postJSON`: a `404` to one of the two certificate routes becomes "votes are submitted on the bank's operator node": a public face answers `404` to them ([ui/README.md](../ui/README.md#which-address-to-give--node)). Any other `404` names the path and the answer |
| Name requests | `DraftNameRequest`, `SignNameRequest`, `SubmitNameRequest`, `requestFacts`, `hashName`, `defaultRequestLead`, `node.MaxRequestLookahead` |
| The bank's desk | `customerChanges`, `pendingRow` |
| Governance votes | `DraftParameters`, `DraftReassignment`, `fitsStateBeforeTheProof`, `parameterFacts`, `nameActionFacts`, `SignParameters`, `SignReassignment`, `handOver`, `SubmitParameters`, `SubmitReassignment`, `decodeVotes`, `incomplete` |
| The node's governance view | `Wallet.Governance`, which reads the node's own `GovernanceView` (step 13) |

**Go:** `decodeVotes[CertificateType any]` is a generic function: the caller names the type it
decodes, as in `decodeVotes[ledger.ParameterChange]`.

**Later steps take as given:**
- While a file's state stands in, the wallet sends no payment or update: it saves payments as text.
- The wallet never puts two signed payments or updates for one block into the world. A payment saved
  as text takes its block from the same counter as a sent one.
- Requests and votes travel between wallets as text; a node receives only a complete one.
- Every action but two checks the network's own rules first: `ledger.PaymentFitsState` and
  `ledger.CreditFitsBalanceCap` (before a payment takes a block), `ledger.UpdateFitsState` (before an
  update takes one), `ledger.NameRequestFitsState`, `Reassignment.FitsState`,
  `LedgerParameters.Acceptable`. The two are the freeze and a name request's submit step, which
  checks only the request's shape: they leave the rules to the node, and take no block of the
  counter.

**Check.** Read the action tests of `ui/wallet_test.go`, `requests_test.go`, `admin_test.go`, and
the receipt tests of `security_test.go`. Run (47 tests):
`go test -C ui -run '^Test(WalletFindsItsName|WalletPaysByName|WalletBoundsTheStatus|PaymentBlockID|BlockIDCounter|AccountUpdatesNameBlocks|AccountUpdateRuns|AccountFormDelegates|AmountsOffTheGrid|Freeze|PaymentShowsThePayeesBank|PaymentReview|WalletRefusesNonCanonicalNames|AccountFormTakes|Receipt|Registration|SignStep|Requests|RequestBlocks|HashName|DeskLists|Reassignment|ParameterVotes|ParameterTiming|SoleValidator|ParameterDraft|NameAction|GovernanceView|SavedPayments)' ./...`

### Step 27. The pages

- **For:** the wallet's web pages, and what keeps other sites from using them. Why:
  [ui/README.md](../ui/README.md), [ui/USER_MANUAL.md](../ui/USER_MANUAL.md).
- **Files:** `ui/main.go`, `ui/banks.go`, `ui/templates/wallet.html`, `requests.html`, `admin.html`.

| Concept | Where |
|---|---|
| Starting the wallet | `main` (its flags), `walletPassword` (read before the wallet file; required unless `loopbackOnly`: a loopback IP address), `routes` |
| Refusing other sites | `guard`, `sameOrigin`, `ownHost` |
| Asking for the password | `requirePassword`, behind `guard`: HTTP Basic auth, under any user name |
| The main page | `handler.index`, `render` (`KeptFrom`, "file" or "node" when a kept state stands in, which the page marks "as of height H, from a file" or "as of height H, read before"), `execute`, `pageData`, `accountRow`; wallet.html |
| Asking the other banks (behind, unchecked, aligned, stalled) | `Wallet.checkOtherBanks` (→ `statusURLAt` of each validator's `PublicAddress`, the URL from the address's scheme and host alone; a validator without one does not answer; `readStatus`, within the page's request), `bankCheck`, `bankHeight`, `heightOfAThird`; `otherBanksTimeout`, `behindTolerance`, `chainStallAfter` (2 × the heartbeat and the state's `BlockIntervalMs`) |
| Paying, updating, freezing | `handler.send` (its `confirm` step signs and sends; its `save` step signs and redirects to GET `/send`, which shows the payment saved last, `Saved`, so a reload signs nothing), `updateAccount`, `freeze`; `send` and `freeze` sign only in those steps, and `updateAccount` signs as soon as its form is posted |
| A checkpoint from a file | `handler.loadCheckpoint` (`/checkpoint`): the main page's form, shown while no state verifies or while the page shows a file's state; its body bounded at 3 × `node.MaxCheckpointBytes` (`http.MaxBytesReader`), since url-encoding can triple the file; a form too large, or unreadable, refused with that reason (too large: `errCheckpointTooLarge`, which `LoadCarriedCheckpoint` shares for a text past the bound) |
| The Requests page, also the bank's desk | `handler.requests`, `requestsPageData`; requests.html |
| The admin page | `handler.admin`, `renderAdmin`, `adminPageData`, `proposeParameter`, `voteReassignment`, `vote`; admin.html |
| The templates | `templateFiles`, `pageTemplates`, with the template function `fingerprint` |

**Go:** `//go:embed templates/*.html` puts the files into the binary at build time.
`html/template` escapes every value it writes into a page. In `checkOtherBanks`, `waitGroup.Go`
runs a function in a goroutine, and `waitGroup.Wait` waits until every one it started returned.

**Later steps take as given:**
- Every page reads one verified state, and shows and acts on that one state: the node's; or the
  newest the wallet verified, when the node serves an older one; or a file's, while the node can't
  be read. When none verifies, the freeze button still freezes the last name the wallet verified
  (`lastName`).
- A customer's wallet works against a bank's public face, which serves the files and passes on the
  payment, the update and the name request; only a certificate needs the operator port.
- The main page also asks every validator's node for its height. The answers are not signed: they
  choose a note on the page, never the state it shows.
- The admin page also shows the node's governance view (`Wallet.Governance`): the parameters in
  force, the total power, the quorum and the pooled certificates. Nothing verifies that view. A vote
  is drafted and checked against the verified state only.
- Every request must name the wallet's own host, and any but a GET or HEAD must come from the
  wallet's own pages; `guard` refuses the rest before any handler runs. Behind it, a wallet with a
  password refuses every request that does not carry it.

**Check.** Read the guard tests of `ui/security_test.go`, the page tests of `wallet_test.go`,
`banks_test.go` and `publicface_test.go`, whose tests run the wallet against a stand-in for a
fetcher in front of a real engine, and `fetcher_test.go`, whose test runs it against the real
fetcher, which it builds with the go command (it skips under `-short`). Run (26 tests):
`go test -C ui -run '^Test(CSRF|OwnHost|MutatingHandlers|DNSRebinding|PagesDeny|UnregisteredWallet|WalletWhoseNameHasAnotherKey|PendingKeyShows|PendingChangeOnOwnName|FrozenNameShows|UnknownPath|PagesReadTheCheckpointOnce|WalletChecksTheOtherBanks|OtherBanks|StalledChain|WalletFollowsNoRedirect|NoSelfLoop|CustomersWalletWorksAgainst|AdminPageAgainstAPublicFace|NotFoundSays|PaymentSavedAsText|SaveStep)' ./...`
Then run `make demo`: it builds every program and starts a node with the wallet, at the address it
prints; Ctrl-C stops it.

## Step 28. Putting it together

Four journeys through the whole code. Each row is one hop; an arrow means "calls".

**A payment.** The wallet of `alice` pays 10 to `bakery`, on a network of several validators. Her
wallet runs with `-node` set to her bank's public address, a fetcher behind a CDN, and the banks
gossip on their gossip ports.

| # | Where | Hop |
|---|---|---|
| 1 | wallet | The form posts to `/send`: `handler.send` → `Wallet.newestVerifiedState`, `Wallet.ReviewPayment` (→ `ledger.PaymentFitsState`, `ledger.CreditFitsBalanceCap`). The page shows `bakery`'s bank of record and signs nothing. |
| 2 | wallet | The confirm step: `handler.send` → `Wallet.Send` → `verifiedState`, `signPayment` → `ReviewPayment`, `registeredName`, `nextPaymentBlockID` (from the verified state's height in progress, `State.Height + 1`), `Wallet.signer` → `ledger.SignPayment` → `LocalSigner.SignBase64` → `signBase64` → `LocalSigner.Sign` → `signing.Preimage`, `mldsa65Key.Sign`. POST `/hdls/pay` to the bank's public address. |
| 3 | the bank's fetcher | `Fetcher.ServeHTTP` → `handleSubmission` → `submissionLimit.allow`, the body read (at most 1 MiB), `engineProxy` → POST `/hdls/pay` on the engine's operator port. |
| 4 | node, off the loop | `Server.handlePay` → `decode`, `Node.AdmitPayment` → `checkPayment` → `paymentIntakeError` (→ `ledger.PaymentFitsState`, `ledger.CreditFitsBalanceCap`), `ledger.VerifyPayment` → `signing.VerifyBase64` → `signing.Verify` → `signing.Preimage`, `mldsa65.Verify`. |
| 5 | node, on the loop | `enqueue` → `run` → `handle` (`eventPayment`) → `admitPayment` → `paymentIntakeError` again, the payer's key unchanged, `mempool.contains`, `requests.paysFeeIn` (no fee of the payer for that block), the overdraw guard (`mempool.outgoingFor`, `requests.feesOf`, `ledger.AccountAmount`), `mempool.add`, `relay` → `HTTPTransport.Broadcast`; `wakeIfIdle` → `startHeight` if the node was idle. The verdict goes back on the `admit` channel, where `AdmitPayment` waits in `awaitAdmission`. |
| 6 | node → wallet | `handlePay` → `ledger.SignReceipt`, through the engine's signer: with the key in the bank's signer, `RemoteSigner.Sign` → `startRequest`, `request` → POST `/sign` → the signer's `handleSign` → `sign` → `checkReceipt`, `writeAuditLine`; then the answer verified under the bank's key. The fetcher passes the answer back unchanged. `Send` checks it: `pinnedKey` for the node it names, its `PaymentID`, then `Receipt.Verify` under that key. |
| 7 | the other nodes | `HTTPTransport.post` → each peer's first URL it connects to, on its gossip port: `allowListListener.Accept` → `allows` lets the bank's address in; `Server.GossipHandler` → `handleGossipPayment` → `AdmitPayment`, as in hops 4 and 5. |
| 8 | the proposer | `startHeight` → `pendingEvidence`, `mempool.pendingCopy` (the pooled payments, copied) and the other pools' copies, `pendingParameterChange`, `pendingReassignment`, `ledger.ProposeBlock` (→ `paymentError`, `paymentSigned`), `hasWork`, `ledger.BlockHash`, `seatedSigner`, `consensus.NewDriver`, `Driver.Resume` → `startRound` → `ActionProposal`; `Node.process` → `signProposal` (→ `consensus.SignProposal`; through a signer, its `takeSlot` logs the slot before it signs), `logProposal`, `Transport.Broadcast`. |
| 9 | every node | `handleProposal` → `SubmitProposal`; `handle` (`eventProposal`) → `onProposal` (→ `ValidatorSet.ProposerAt`, `SignedProposal.Verify`, `ledger.BlockHash`, `ledger.ApplyBlock`) → `Driver.ReceiveProposal` → `evaluate` → `applyOneRule` → `prevoteFor` → `castVote` (→ `consensus.SignVote`, `logVote`, `count`) → `ActionBroadcastVote`; `process` → `Broadcast`; then `relay` passes the proposal on. |
| 10 | every node | `handleVote` → `SubmitVote`; `handle` (`eventVote`) → `holdsVote`, `countable`, `rememberVote`, `relay`, `Driver.ReceiveVote` → `count` → `VoteSet.Add`; `evaluate` → `applyOneRule`: a prevote quorum locks, and `castVote` signs a precommit; a precommit quorum gives `ActionDecided` with `VoteSet.Commit`. |
| 11 | every node | `process` → `Node.commit` → `ledger.ApplyBlock` → `applyContent` → `ProcessPayments`; `noteConfirmation`, `retireDriver`, `saveSnapshot`, `emptySignLog` (→ `writeSignLog`), `recordCommittedBlock`, `noteFrame`, `publishView` (with the new allow list), `reconcilePools` (the payment's block has passed: it leaves the pool), `resolvePeersFromState`, `sendLastCommit`, `writePublicFolder` → `writeIPLookups`, `publishCommittedState` (`hdls/checkpoint`, … `hdls/status` last, written by `publicFolderWriter` off the loop); `time.AfterFunc` → `eventNextHeight` → `startHeight` → `hasWork` is false: idle, and `armIdleHeartbeat`. |
| 12 | wallet | The next page: `handler.index` → `newestVerifiedState` → `nodeCheckpoint` → GET `/hdls/checkpoint` at the public address, through `tlsPinningTransport`: the fetcher's `serveFile` sends the public folder's file, byte for byte what `handleCheckpoint` answers on the operator port (→ `Node.Snapshot` → `enqueue`, `handle` (`eventQuery`) → `Node.snapshot`; then `Snapshot.checkpoint`). Then `verifyUnderPin` → `Checkpoint.Consistent`, `signedByPinned` → `Commit.Verify`; `render` shows the new balance, and `checkOtherBanks` → `statusURLAt`, `readStatus` asks each bank's public address for its height. |

**A freeze.** `alice` fears someone else holds her key, and presses the freeze button.

| # | Where | Hop |
|---|---|---|
| 1 | wallet | `handler.freeze` (confirm step) → `Wallet.Freeze` → `newestVerifiedState`, `registeredName` (or, when no state verifies, `lastName` and `Wallet.height`); the block is the verified height in progress (`State.Height + 1`, or, while a kept state stands in, the larger of that and `Wallet.height`) plus the lookahead, or her pending change's block if sooner; `Wallet.signer`, `ledger.SignAsRequestor` (domain `freeze`), `postJSON` (POST `/hdls/name-request`, which the fetcher passes on, as in the payment's hop 3); the wallet keeps `FreezeName` and `FreezeBlockID`. |
| 2 | node | `handleNameRequest` → `AdmitNameRequest` → `checkNameRequest` → `ledger.NameRequestShapeError`, `nameRequestIntakeError` (the 64-block window, `ledger.NameRequestFitsState`, `ledger.LeavesANameSeated`), `ledger.VerifyNameRequest`; `handle` (`eventNameRequest`) → `admitNameRequest` → `requestPool.add`, `relay`, `wakeIfIdle`. |
| 3 | the proposer | `ledger.ProposeBlock` takes freezes before evidence and requests, one per name, and locks the name: a payment from or to `alice` stays out of the block. |
| 4 | every node | `ledger.ApplyBlock` → `contentError` (the freeze, the locked names), `applyContent` → `freezeNames`: `Frozen` is set, her pending change is cleared, and so is any change she co-signed. `ValidatorRule.SetFor` no longer seats her. |
| 5 | every node | `commit` → `reconcilePools`: her pooled payments, updates and requests fail intake now and go; `prunePendingProofs` drops proofs against her name. |
| 6 | wallet | `handler.index` → `newestVerifiedState`, `render` → `freezeStatus`: a verified state shows the name frozen, and the wallet forgets the freeze it sent. |

**A ⅔ name action.** The validators give `alice`'s frozen name a new key and unfreeze it.

| # | Where | Hop |
|---|---|---|
| 1 | a validator's wallet | The admin form: `handler.voteReassignment` → `handler.vote` (review) → `verifiedState`, `Wallet.DraftReassignment` → `typedName`, `typedKey`, `fitsStateBeforeTheProof` → `Reassignment.FitsState`; `nameActionFacts`. `renderAdmin` shows the facts and signs nothing. |
| 2 | the new key's wallet | `vote` (sign) → `verifiedState`, `Wallet.SignReassignment` → `Wallet.signer`, `fitsStateBeforeTheProof`, `ledger.SignReassignmentNewKey`, `handOver`: the text goes back to the validators. |
| 3 | each validator's wallet | `vote` (sign) → `verifiedState`, `SignReassignment` → `Wallet.signer`, `FitsState`, `ledger.VerifyReassignmentNewKey`, `registeredName`, `ledger.SignReassignmentVote`, `handOver` (which submits at once only if this name alone is a quorum). |
| 4 | the collector's wallet | `vote` (submit) → `Wallet.SubmitReassignment` → `decodeVotes`, `verifiedState`, `CertificateTally.Add` with `ledger.VerifyReassignmentVote` for each vote, `Reassignment.Valid`, `postJSON` (POST `/hdls/reassignment-vote`, on the bank's operator port: a public face answers `404`, which `postJSON` turns into where votes go). |
| 5 | node | `handleReassignmentVote` → `AdmitReassignmentCertificate`; `handle` (`eventReassignmentCertificate`) → `addReassignmentVotes` (→ `currentReassignmentVoters`, `reassignmentFits`, `Reassignment.Valid`), `relay`, `wakeIfIdle`. |
| 6 | the proposer | `startHeight` → `pendingReassignment`, `ledger.ProposeBlock`, which takes the governance action first, locks its name and counts its new key as used. |
| 7 | every node | `ledger.ApplyBlock` → `contentError` (→ `Reassignment.Valid`), `applyContent` → `applyReassignment` (the new key, `Unfreeze`, the pending change cleared); the governance sequence moves on. |
| 8 | every node | `commit` → `recordCommittedBlock` → `lifelongEntryOf`: a lifelong entry, served by `/hdls/lifelong-record`. `reconcilePools` → `pruneReassignments` drops the certificate. |

**A payment carried by hand.** `alice`'s area is cut off from the banks, and the banks are cut off
from each other. A carrier moves files between them, and the banks holding more than ⅔ of the power
have turned the slow-down switch on.

| # | Where | Hop |
|---|---|---|
| 1 | a carrier | Copies a bank's `hdls/checkpoint`, which its `publishCommittedState` wrote at its last commit, and brings it to `alice`. |
| 2 | wallet | Her node can't be reached, so the page offers the form. `handler.loadCheckpoint` (POST `/checkpoint`) → `Wallet.LoadCarriedCheckpoint` → `pinnedNetworkID` (a pin is needed), `verifyUnderPin` → `Checkpoint.Consistent`, `signedByPinned` → `Commit.Verify`, and keeps it as the newest state verified (`newestCheckpoint`, from a "file"). On each page, `newestVerifiedState` finds the node out of reach (`nodeCheckpoint`'s `reached` is false) and gives the file's state, with `keptFrom` "file", so the page says "as of height H, from a file". |
| 3 | wallet | The review as in the payment's hop 1, then the save step: `handler.send` → `Wallet.SavePayment` → `signPayment` (its block counted from the file's height), `node.EncodeBundle` with a `node.BundleHeader`; then a redirect to GET `/send`, whose page shows the text (`lastSavedPayment`). Nothing is sent. The carrier takes it to a bank. |
| 4 | a bank's engine | POST `/hdls/bundle` on its operator port: `Server.handleImportBundle` → `Node.ImportBundle` → the header's version and network id, `messageOf`, `AdmitPayment`, as in the payment's hops 4 and 5: checked, pooled, and relayed to every peer the bank still reaches. The answer is `BundleCounts`. |
| 5 | each bank | GET `/hdls/bundle` on its operator port: `handleExportBundle` → `ExportBundle` → `bundle`: `consensusMessages`, the pooled certificates, then the pools, the payment among them. The carrier takes each bank's bundle to every other bank, which imports it as in hop 4: the payment, then each height's proposal and votes. With the switch on, `armTimeout` waits `stepTimeout`, longer than a carry, before a node votes "no block", so a proposal carried by hand arrives in time. |
| 6 | each bank | Where the banks' public faces still answer, the carrying needs no one: once a height is stuck, `pullPublicBundles` at each re-gossip tick → `fetchPublicBundle` (each other validator's `hdls/bundle`, which `publishBundle` writes) → `importBundle`, each vote and proposal checked first by `pulledCheck`. |
| 7 | every bank | The commit, as in the payment's hop 11. A bank more than a height behind takes a carried checkpoint instead: POST `/hdls/checkpoint` → `handleImportCheckpoint` → `ImportCheckpoint` → `catchUp` → `verifyCheckpoint`, `divergesFrom`, `fastForward`. |

**Why the wallet can believe what it shows.** A signature binds its network, domain and fields
(step 1). A commit needs a quorum of the set the state seats (2, 7). Every node applies exactly the
block it decided, and the block hash covers the state digest (8, 12). A checkpoint's block hash
recomputes from its header and state, and its commit verifies (10, 15). The wallet checks that
commit against the validators it pinned (25). A public face, a cache or a carrier can hand it an old
checkpoint, never a forged one: a file is checked as the node's is (21, 25). A message in a bundle,
or pulled from a public face, is checked as gossip is (17, 22).

**Run everything**, as CI does, with the chaos scenarios of `node/chaos_test.go` (loss, partitions,
a double-signer): `go test -race ./...` and `go test -C ui -race ./...`.

## Every production file and its step

| Package | Files and steps |
|---|---|
| `signing/` | scheme.go, mldsa65.go, domain.go, signer.go, keyfile.go, mldsa65_fips140v1.go: 1; remote.go: 24 |
| `cmd/keygen/` | main.go: 1 |
| `consensus/` | errors.go, validator.go, vote.go, commit.go, proposal.go, evidence.go, voteset.go: 2; driver.go: 9 |
| `ledger/` | amount.go, name.go, address.go, ledger.go, digest.go, parameters.go, topology.go: 3; payment.go, account_update.go, receipt.go: 4; name_request.go: 5; governance.go, reassignment.go: 6; validators.go, genesis.go: 7; hash.go, block.go: 8 |
| `node/` | messages.go: 10; node.go: 10 to 12; mempool.go, updatepool.go, requestpool.go: 11; idle.go: 12; proofs.go, governance.go, reassignment.go: 13; persist.go: 14; sync.go: 15, and its pull, 22; alarms.go: 15; record.go, frames.go: 16; bundle.go: 17; httptransport.go: 18; relay.go, topology.go, peerdns.go: 19; allowlist.go: 20; publicfolder.go, zone.go: 21 |
| `cmd/fetcher/` | main.go: 21 |
| `cmd/server/` | config.go, main.go: 23 |
| `cmd/demo/` | main.go: 23 |
| `cmd/quickstart/` | main.go: 23 |
| `cmd/signer/` | main.go, layout.go, signlog.go, service.go, lock_unix.go, lock_other.go: 24 |
| `ui/` | keyfile.go, trust.go, tlspin.go: 25; wallet.go: 25 and 26; requests.go, admin.go: 26; main.go, banks.go and templates/wallet.html, requests.html, admin.html: 27 |

Not Go: `deploy/Dockerfile`, `deploy/ui.Dockerfile`, `deploy/compose.yaml` and `.dockerignore`, step
23.
