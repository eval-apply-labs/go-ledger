# Phase 2 implementation spec: one new genesis

> **Status:** the spec of the Phase 2 build, on branch `post-emudhra-phase2`. It was written on
> 2026-10-27, revised after five reviews (the Appendix records each finding and what was done), and
> amended during the build with the code-note fixes and the owner's decisions, through 2026-10-31
> (§12). The build is done: §1 to §9 describe the code as it stands, and §10 (the slices), §11 (the
> docs) and the Appendix are the record of how it was built. How it went is in
> `review/phase2_progress.md`.
>
> **Inputs:** the plan in `review/post_emudhra_review_2026-10-27.md` ("Phase 2 — one new genesis")
> and the findings it cites (E1, E6, E7, E13, E15, E16, E20, E29, E38, E44); the two design checks
> `review/_evidence_2026-10-27/results/new_key_design_review.json` and `name_freeze_check.json`;
> and the owner's decisions of 2026-10-27, which bind. Where this spec reads a decision one way, it
> says so in §0.3 and asks again in §12.

## 0. How to read this spec

Since this spec was written, the code was renamed to spelled-out names (commits `803518d` and
`5182d8a`; the map is in [`renames_2026-10-27.md`](renames_2026-10-27.md)). Names in this spec
follow the new code; any old-style name left here (a one-letter variable, a `Msg` or `ev` prefix)
is to be read and written in the spelled-out style. The spec was written as changes to Phase 1:
"today" and "as today" mean the Phase 1 code the build started from (`post-emudhra-phase1`).

### 0.1 Words and names

- "Must" marks a rule that a test checks. "May" marks an option a builder can skip.
- Code is named by file and function as it stands after the build, for example `node/node.go`
  `onProposal`; code the build removed, by its Phase 1 name. `review/renames_2026-10-27.md` maps
  every package-level name the rename changed, from its spelling before the rename (`9b7965f`) to
  the new one, for example `MsgPayment` to `MessagePayment`.
- Every new identifier is spelled out in full: `KeyChangeKind`, `changeDelayBlocks`, `proposal`.
  Never `p`, `kc` or `Msg`.
- "The block" is the block being validated, at height `h`. "The pre-block state" is the state it
  builds on, at height `h − 1`. "V" is the validator set derived from the pre-block state (§5.2).

### 0.2 Encoding conventions

| Thing | Encoding |
|---|---|
| `lp(x)` | 4-byte big-endian length of `x`, then `x`. Every digest below is SHA-256 over a sequence of `lp()` fields, starting with a tag field. |
| Integers in digests and signed fields | decimal ASCII, as `strconv.FormatInt` writes them (`-1` for a ValidRound of −1) |
| Booleans in digests and signed fields | `1` or `0` |
| Amounts | the canonical decimal of §3.3, in digests, signed fields and JSON alike |
| 32-byte digests and nonces | the raw 32 bytes in digests and preimages; in JSON, Go's default for `[32]byte` (an array of 32 numbers), as today |
| Public keys | standard base64 with padding, canonical (`signing.CanonicalBase64`), of exactly 1952 bytes: 2604 characters |
| Signatures | the same, of exactly 3309 bytes: 4412 characters |
| Private keys | a 32-byte seed (`mldsa.NewPrivateKey(mldsa.MLDSA65(), seed)`); base64, 44 characters |
| JSON keys of ledger, consensus and node wire types | the Go field names, with no tags, as today. A tag the rename pass added to keep an old wire key goes when this spec redefines the type. HTTP views and config files keep their lowercase tags. |

No old format is read. A Phase 1 snapshot, sign log, wallet key file or pin file fails to load, and
the error says it belongs to an earlier network. There is no migration code.

### 0.3 Readings of the decisions (flagged)

Each reading is a builder default: the build follows it, and §12 asks the owner about each one.

- **R1.** "AccountUpdate loses its name claim and keeps only delegation." Read as: it keeps
  delegation and also its `Description` and `Address` fields, which are not identity. `Address` is
  the on-chain peer address that `node/peerdns.go` (the E11 fix) and `/hdls/resolve` use. Dropping
  it would remove the peer DNS feature. If the owner meant that, a later slice deletes the two
  fields and `node/peerdns.go`.
- **R2.** "Each account has a bank of record." An account created in the genesis or by the ⅔ name
  action may have none (`Bank` is ""). The demo has one bank, and a name is never its own bank. A
  name without a bank of record changes its key only by the ⅔ action, and it can get a bank by a
  bank change.
- **R3.** "The requestor's bank co-signs." A requestor that is itself seated may be that bank.
  Otherwise a genesis bank, which has no bank of record, could never pay for a customer's
  registration.
- **R4.** Banks of record are banks. A registration's co-signing bank and a bank change's new bank
  must be seated in V. A key change's co-signing bank (the bank of record) needs no seat, only not
  to be frozen, as the design review said. Without R4 a thief could make a name it controls the
  victim's bank and co-sign for itself.
- **R5.** A double-sign freezes the name and leaves its balance alone. Today's slash, which sets
  the balance to zero (`ledger/block.go` `applySlashes`), goes. The ⅔ decide about the money, by
  claw-back.
- **R6.** Every freeze wins in its block. A name that the block freezes (by evidence, its holder or
  the ⅔), and the ⅔ action's target, send and receive nothing else in that block. This extends E5's
  exclusion from the ⅔ action to every freeze.
- **R7.** Applying the ⅔ name action clears any pending change of its name.
- **R8.** `ChangeDelayBlocks` must be at least 1, so tests and the demo can use small values. A ⅔
  vote may cut it. A cut affects only requests filed after it, because each pending change keeps
  the due height it got when it was filed.
- **R9.** A block whose resulting state seats no name is invalid. Freezes or payments could
  otherwise empty the validator set, and then no block could ever commit again. The item that
  would empty it is left out, never the whole block (§5.4), and intake refuses a freeze or ⅔
  action that alone would. So the last seated bank has no emergency brake.
- **R10.** "A frozen name changes nothing." Read as: a frozen account's balance backs no delegate.
  Otherwise a thief who re-delegated stolen balances keeps that power after the victims freeze.
- **R11.** "One key per bank ... stops its votes and its co-signatures together." Read as: a freeze
  also clears every pending change the frozen name co-signed, so a thief's key change co-signed
  with a leaked bank key never lands, even if the ⅔ re-key the bank within the day.

**Conflict (flagged, both decisions kept).** The record keeps validator-set changes, and the record
must not leak payments. In dynamic mode a seat's power is `floor(stake)`, so an ordinary payment
between customers of two banks changes the set, and the record shows each seat's stake per height.
Decided on 2026-10-31 (§12 Q21): the record keeps the sets, and `docs/security.md` says what that
shows.

## 1. Scope and non-goals

| # | Phase 2 item | Finding |
|---|---|---|
| 1 | Height in `LedgerState` and its digest; `ApplyBlock` requires the previous height + 1 | E1 |
| 2 | Amounts are plain decimals on the 1e-8 grid, with bounded digits, in JSON and in signed fields | E6, E7 |
| 3 | Caps as ledger parameters: the largest single payment and the largest balance per name | review decision |
| 4 | The network id is bound to the genesis | E20 |
| 5 | The proposal signature covers the block | E15 |
| 6 | Proposal evidence is carried in blocks | E16 |
| 7 | A double-sign freezes the name; no burn set; current-key rule instead of `maxEvidenceAge`; the one-third cap | E13 (with E16, E35) |
| 8 | The ⅔ freeze and name action: `Reassignment` with `NewKey`, `Bank`, `Freeze` and `Unfreeze` | E29 (with E5, E30) |
| 9 | Holder requests: registration, key change, bank change, freeze; the bank of record | E29, E44 |
| 10 | The fork-binding pin compares the full state digest, checked at intake | E38 |
| 11 | The signature record (`RetainBlocks`), with a proposer nonce in the signed digest | review decision |
| 12 | ML-DSA-65 is the only signature scheme | decision |
| 13 | Names are the account identity; delegation is to a name; `AccountUpdate` loses its name claim | decision (E9, E42) |
| 14 | One key per bank; a frozen seated name leaves the validator set | decision |
| 15 | The node's trust anchor and the wallet pin hold (name, key, power); each block hash covers its signing set | design review |
| 16 | An idle chain stops committing a block every second | plan |
| 17 | A node signs only while its name is seated; proofs are kept until carried, frozen or re-keyed | name freeze check |

Non-goals: Phase 3 (four containers, one per docs area of §11: the signer, the engine, the fetcher
and the wallet; owner, 2026-10-30); recovery keys; per-window spending limits; a burn set of keys;
any history of holder requests; migration from Phase 1 state; transport security, rate limits and
the archive node (`TODO.md`); light-client proofs; changes to the round driver beyond §6.3; a
multi-account wallet. Phase 3 is served only by keeping every signature in a domain of its own
(§4.0).

## 2. The genesis file

All nodes read one genesis file, byte for byte the same (`genesisPath` in the node config, §8.2).
`ledger.ValidateGenesis` checks it, and `ledger.GenesisHash` hashes it (new file
`ledger/genesis.go`).

### 2.1 Fields

| Field | Type | Meaning |
|---|---|---|
| `networkName` | string | the readable half of the network id; a canonical name (§3.2) |
| `parameters` | object | the genesis `LedgerParameters` (§3.4), every one present, keyed exactly as in §2.5 (lower camel case) |
| `validators` | array | static mode: `[{"name": "...", "power": N}]`. Empty in dynamic mode. |
| `validatorThreshold` | amount string | dynamic mode: the stake a name needs to be seated. "" in static mode. |
| `accounts` | array | `[{"name","publicKey","amount","bank","delegate","description","address"}]` |

Exactly one of `validators` and `validatorThreshold` is non-empty. Validator URLs are gone: a
validator's gossip URL is its account's `address` (§8.2).

### 2.2 Validation

`ParseGenesis` reads the file first. It refuses a missing parameter, a parameter key in any other
spelling, a malformed amount parameter (naming it), and any key that matches no field. Outside
`parameters`, a key matches its field whatever its case, as `encoding/json` reads it.

Then `ValidateGenesis` refuses the file, with a message naming the field, if:

1. `networkName` or an account `name` is not canonical (§3.2), or two accounts share a name.
2. A `publicKey` is not canonical base64 of 1952 bytes, or two accounts share a key.
3. An `amount` does not parse (§3.3), or exceeds `maxAccountBalance`. Zero is allowed.
4. A `delegate` is neither "" nor an account name.
5. A `bank` is neither "" nor an account name, is the account's own name, or is not seated in the
   genesis validator set.
6. Static mode: a validator name is not an account, appears twice, or has power below 1 or above
   2^40 (`consensus.MaxValidatorPower`; code note C4, 2026-10-31).
   Dynamic mode: `validatorThreshold` does not parse or is not above 0.
7. The parameters fail `Acceptable` (§3.4) for the genesis validator count.
8. The genesis seats no name.

### 2.3 Genesis state, genesis hash and network id

- The genesis state is `LedgerState{Accounts: sorted by name, Parameters, GovernanceSequence: 0,
  Height: 0}`. No account is frozen or has a pending change.
- `GenesisHash = SHA-256( lp("go-ledger genesis v2") || lp(networkName) ||
  lp(LedgerState.Digest() of the genesis state) || rule )`, parameters included, where `rule` is,
  in static mode,
  `lp("static")` followed by `lp(name) || lp(power)` for each validator in ascending byte order of
  name, and in dynamic mode `lp("dynamic") || lp(threshold)`.
- The **effective network id** is `networkName + "/" + lowercase hex(GenesisHash)` (64 hex
  characters), for example `village/3f9a…`. Every signature binds it, through `signing.Preimage`.
  A re-genesis with any change gets a new id, so old payments and certificates do not verify on it
  (E20). A re-genesis with identical contents is the same chain, and replaying it lets anyone
  freeze the banks with their own old votes: the old and the new precommit for one height and
  round sign different block hashes (new nonces) under the same key and id, and the old chain's
  commits stay in every node's record. So every re-genesis changes `networkName`; the docs say so.
- Block 1's `PreviousBlockHash` is `GenesisHash`, carried as `ledger.Chain.GenesisHash` (§5.2).
- `/hdls/status` serves the effective id; the wallet pins it (§7.2). Tests may pass any string as
  the network id, and a zero genesis hash, in `node.Config`.

### 2.4 Parameter defaults

| Parameter | Default | Status |
|---|---|---|
| `transactionCost` | `0.1` | as today's examples |
| `blockIntervalMs` | `1000` | as today |
| `stepTimeoutMs` | `3000` | as today |
| `maxPeers` | `0` | as today |
| `maxMessageBytes` | `6400` | owner to confirm (§12): fits the largest payment, 6361 bytes when every comment byte is escaped |
| `newKeyFee` | `1` | owner to confirm |
| `changeDelayBlocks` | `86400` | decided: about a day at one block per second |
| `retainBlocks` | `604800` | owner to confirm: a week at one block per second |
| `maxPaymentAmount` | `100000` | owner to confirm |
| `maxAccountBalance` | `10000000` | owner to confirm |

### 2.5 Example

```json
{
  "networkName": "village",
  "parameters": {
    "transactionCost": "0.1", "blockIntervalMs": 1000, "stepTimeoutMs": 3000, "maxPeers": 0,
    "maxMessageBytes": 6400, "newKeyFee": "1", "changeDelayBlocks": 86400,
    "retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000"
  },
  "validators": [ { "name": "bank-a", "power": 1 }, { "name": "bank-b", "power": 1 } ],
  "validatorThreshold": "",
  "accounts": [
    { "name": "bank-a", "publicKey": "<2604>", "amount": "1000", "bank": "", "delegate": "bank-a",
      "description": "", "address": "http://10.0.0.2:8080" },
    { "name": "bank-b", "publicKey": "<2604>", "amount": "1000", "bank": "bank-a",
      "delegate": "bank-b", "description": "", "address": "http://10.0.0.3:8080" },
    { "name": "alice", "publicKey": "<2604>", "amount": "50", "bank": "bank-a", "delegate": "",
      "description": "", "address": "" }
  ]
}
```

## 3. Ledger state

### 3.1 Go structs

```go
// Account is one name and what the ledger holds for it.
type Account struct {
	Name          string   // the identity, a canonical name (§3.2)
	PublicKey     string   // the name's current key; it verifies every message the name signs
	Amount        *big.Rat // the balance; never nil in state
	// Bank is the bank of record, a name, or "" for none: a genesis or ⅔-created name may have
	// none, and the ⅔ action clears one with "-" (R2, §4.8).
	Bank          string
	Delegate      string   // the name this balance backs as stake; "" for none
	Description   string
	Address       string   // the gossip URL a validator publishes (peer DNS)
	Frozen        bool
	PendingKey    string   // a key change waiting for PendingHeight; "" for none
	PendingBank   string   // a bank change waiting for PendingHeight; "" for none
	PendingHeight int64    // the height at whose end the pending change takes effect; 0 for none
}

// LedgerState is the whole committed state. Its Digest is what consensus commits, through the
// block hash (§5.1).
type LedgerState struct {
	Accounts           []Account // strictly ascending byte order of Name; the digest hashes this order
	Parameters         LedgerParameters
	GovernanceSequence int64
	Height             int64 // the height of the last applied block; 0 for the genesis
}
```

At most one of `PendingKey` and `PendingBank` is set, and `PendingHeight > 0` exactly when one is.
A frozen account has no pending change. A key in state is the current key of exactly one name.

### 3.2 Names

- A name is 1 to 32 bytes of lowercase ASCII letters, digits and hyphens. It starts and ends with
  a letter or a digit: `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`. Owner to confirm (§12).
- The canonical form is the name itself. Nothing is normalised in the protocol: `Alice` is not a
  name. The wallet lowercases and trims what a person types, shows the result, and refuses
  anything else.
- `ledger.CanonicalName(text) bool` is the one check, used for account names, the network name,
  every name field of every message, and the genesis.
- A name is free only if no account has it. Names are never deleted, so a name is registered once.

### 3.3 Amounts

- An amount is a whole number of 1e-8 units, held as `*big.Rat`, as today.
- **Parsing** (`ledger.ParseAmount`, used for JSON and config): one to 18 digits, optionally
  followed by `.` and one to 8 digits. Nothing else: no sign, no exponent, no `a/b` fraction, no
  leading or trailing point, at most 27 characters. Leading zeros and trailing fraction zeros are
  accepted. The text is checked before `big.Rat` sees it.
- **Writing** (`ledger.FormatAmount`): the canonical decimal. The integer part has no leading zeros
  (`0` when it is zero). A non-zero fraction follows a `.` without trailing zeros. An integer has
  no point. Examples: `0.5`, `12`, `1000.00000001`.
- JSON writes an amount as a string in canonical form, and `null` for an optional amount that is
  absent. Every signed field and digest uses the canonical form ("" for an absent optional amount,
  E22). This replaces `RatString`, whose `a/b` output goes.
- The types that hold amounts (`Account`, `Payment`, `Reassignment`, `LedgerParameters`,
  `NameRequest`) write them through `MarshalJSON` methods on **value** receivers. A pointer-receiver
  method runs only for an addressable value, so a plain value or a field of one would fall back to
  `big.Rat`'s `a/b` text, and `WithinMessageBound` would measure that.
- Only three amounts are optional: `Reassignment.PreviousAmount` and `NewAmount`, and a freeze's
  `Fee`. The decoder refuses `null` for every other amount, except a name request's `Fee`, which it
  takes for every kind; the shape check (§4.3) then refuses a null `Fee` for every kind but a
  freeze. `FormatAmount(nil)` returns "" as a backstop (E22), and `Acceptable` refuses a nil
  parameter.
- Every amount in state and messages is below 10^18 units, so it always fits the parser.

### 3.4 Parameters

```go
type LedgerParameters struct {
	TransactionCost   *big.Rat // burned per payment; >= 0
	BlockIntervalMs   int64    // 0..MaxTimingMs
	StepTimeoutMs     int64    // 1..MaxTimingMs, and covers the gossip fan-out (TimingConsistent)
	MaxPeers          int64    // 0..1000
	MaxMessageBytes   int64    // 0 (unbounded) or MinMessageBytes (5000)..1048576 (the body limit)
	NewKeyFee         *big.Rat // > 0; the fee of a registration, key change or bank change
	ChangeDelayBlocks int64    // 1..2592000 (R8; 30 days at one block per second)
	RetainBlocks      int64    // 1000..315360000 (ten years); how long the record keeps a block (§6.8)
	MaxPaymentAmount  *big.Rat // > 0; the largest single payment
	MaxAccountBalance *big.Rat // > 0; the largest balance a payment or the ⅔ may raise a name to
}
```

- `fields()` lists the parameters in exactly the order above, each as text (amounts canonical,
  integers decimal). It feeds both the parameter digest and the parameter-vote preimage, as today.
- `Acceptable(participants)` must refuse a nil amount and any value outside the bounds in the
  comments. The ceilings (owner to confirm, §12 Q22) keep `h + ChangeDelayBlocks` and
  `h + RetainBlocks` far inside int64, so one vote or one typo cannot file changes that never fall
  due. `MaxPeers` and `MaxMessageBytes` are int64, so a 32-bit node decodes every value a 64-bit
  node does.
- `ledger.DefaultParameters()` returns the §2.4 defaults. The demo, the genesis example and test
  fixtures start from it. Ledger code may assume parameters that pass `Acceptable`, since the
  genesis and every parameter change do, so it never meets a nil cap or a zero delay.

### 3.5 Digest layout

Every digest is SHA-256 of the `lp()` fields listed, in order.

| Digest | Fields |
|---|---|
| `AccountsDigest(accounts)` | `"go-ledger accounts v2"`; then for each account in slice order: `Name`, `PublicKey`, `Amount`, `Bank`, `Delegate`, `Description`, `Address`, `Frozen`, `PendingKey`, `PendingBank`, `PendingHeight` |
| `Parameters.Digest()` | `"go-ledger parameters v2"`, then the ten `fields()` |
| `LedgerState.Digest()` (the **state digest**) | `"go-ledger state v2"`, `AccountsDigest` (32 raw bytes), `Parameters.Digest()` (32 raw bytes), `GovernanceSequence`, `Height` |

`Frozen` is `1` or `0`. `PendingHeight` is `0` when there is no pending change. The digest does not
sort: `ApplyBlock` keeps the slice ascending, and a decoded state (a snapshot, a peer's checkpoint,
the state a wallet reads) whose names are not strictly ascending is refused before any other
check, which also proves them unique. Each slice that touches a digest adds a golden test: it builds the preimage by hand
from this table and compares the hash with the function's.

### 3.6 Never stored

- No history of holder requests, keys or pairings. Past keys are not kept anywhere in state.
- No burn set and no list of frozen keys apart from the frozen accounts themselves.
- Blocks, payments, account updates, holder requests and nonces are not kept after the next commit.
  The node keeps only the last block's header for its checkpoint (§4.14). The sign log, which holds
  the node's own proposals with their blocks, is emptied once the height's snapshot is on disk
  (§6.9), so an idle chain keeps no block on disk. The node keeps the last decided proposal, its
  block included, in memory until the next commit or catch-up, to send the last commit again to a
  node that missed it (`sendLastCommit`; decided 2026-10-31: the rule is about the disk).
- The bare state digest of a past height is never written to the signature record (§6.8).

## 4. Messages

### 4.0 What every message shares

- **Preimage.** `signing.Preimage(networkID, domain, fields...)`, unchanged: the protocol tag
  `hypernode/v1`, the effective network id, the domain and each field, each `lp()`-framed.
- **Signing.** ML-DSA-65 `SignDeterministic` over the preimage, with no context string. The round
  driver needs a signature to be a function of what is signed. Verification decodes canonical
  base64, checks the exact key and signature sizes, and calls `mldsa.Verify`; it returns false and
  never panics on bad input.
- **Who verifies what.** A message names its signer. Nodes look the key up by name in the
  pre-block state, or, at intake, in the published committed state (§6.5). Only a registration, a
  key change and the ⅔ action carry a key, the new one.
- **Identity.** A message's id hashes its signed fields, never its signature, with today's
  `hashFields` (8-byte lengths) and a tag per type. Dedup and replay rules key on that id.
- **Size.** Payments and account updates are bounded by `MaxMessageBytes` on their JSON, as today.
  Name requests, certificates and consensus messages have only bounded fields, so their exact field
  sizes bound them instead.

Signature domains (`signing/domain.go`):

| Domain | Signed by | Over |
|---|---|---|
| `payment` | the payer | a payment |
| `account-update` | the account | an account update |
| `registration` | the requestor | a registration |
| `key-change` | the holder | a key change |
| `bank-change` | the holder | a bank change |
| `freeze` | the holder | a freeze |
| `name-request-bank` | the bank | a name request |
| `name-request-new-key` | the new key, as proof of possession | a registration or key change |
| `reassignment-vote` | a validator | a ⅔ name action |
| `reassignment-new-key` | the new key, as proof of possession | a ⅔ name action |
| `parameter-vote` | a validator | a parameter change |
| `block-revision` | a validator | a vote |
| `proposal` | the proposer | a signed proposal |
| `receipt` | the node | a payment receipt |

The unused `validator-message` and `server-auth` domains go (E43). `cmd/server` keeps its
`key-self-test` domain. `RequestorSignature` is signed in the domain named by the request's
`Kind`, so a signer that sees only the domain can tell a bank paying for a registration (R3) from
a holder changing its own key or bank. In Phase 3 the signer can serve a bank's desk exactly
`registration`, `name-request-bank` and `freeze`, and never `key-change`, `bank-change`,
`block-revision` or `proposal`.

### 4.1 Payment

```go
type Payment struct {
	From                string   // the payer's name; its current key signs
	To                  string   // the payee's name
	Amount              *big.Rat
	Comment             string
	BlockID             int64    // the height of the one block that may carry it
	// PreviousStateDigest is "" or 64 lowercase hex: the state digest of the state block BlockID
	// builds on (E38).
	PreviousStateDigest string
	Signature           string
}
```

```json
{"From":"alice","To":"bakery","Amount":"12.5","Comment":"bread","BlockID":1042,
 "PreviousStateDigest":"","Signature":"<4412>"}
```

- **Preimage fields:** `From`, `To`, `Amount`, `Comment`, `BlockID`, `PreviousStateDigest`.
  **Domain:** `payment`. **Signer:** `From`'s current key. **Id:** `hashFields("payment-id", …)`.
- **Size:** JSON at most `MaxMessageBytes`. 4511 bytes at least; 4961 bytes with a 280-byte
  comment, two 32-byte names, a pin, a 27-character amount and a 19-digit `BlockID`; 6361 bytes
  when every comment byte is `<`, `>`, `&` or a control character, which `json.Marshal` writes as
  six bytes.
- Paying a key is gone: `PublicKeyFrom`, `PublicKeyTo`, `ToName` and the recipient resolution in
  `ledger/ledger.go` (`resolveRecipient`, `RecipientResolves`, `carriableRecipient`) are deleted.
- **Intake** (`/hdls/pay` for clients, `/hdls/payment` for gossip), in order. Steps 1 to 4 run in
  the HTTP handler, off the event loop: the handler decodes the body, and `AdmitPayment(payment)`
  runs steps 2 to 4 (`checkPayment`), then hands the payment to the loop. H is the height in
  progress of the published committed state S.
  1. The body is at most 1 MiB and decodes; the amount parser runs before `big.Rat`.
  2. `From` and `To` are canonical names. `Signature` has the exact size. `Amount > 0`. `Comment`
     is at most the node's `MaxCommentLength`.
  3. Against S: `H <= BlockID <= H + 64`; the JSON is at most `MaxMessageBytes`;
     `TransactionCost <= Amount <= MaxPaymentAmount`; `From` exists and is not frozen; `To` exists
     and is not frozen; `Amount` is at most `From`'s balance; `To`'s balance plus
     `Amount − TransactionCost` is at most `MaxAccountBalance`; `PreviousStateDigest` is "", or
     `BlockID == H` and it equals hex of S's state digest (a pin can bind only the next block).
  4. The signature verifies under `From`'s key in S.
  5. On the loop (`admitPayment(payment, verifiedKey)`): step 3 again against the live state;
     `From`'s key is still `verifiedKey`; a payment with the same id already pooled is accepted
     again (idempotent); `From` is not the fee payer of a pooled request for the same `BlockID`
     (§5.5); `From`'s pending outgoing (pooled payments plus pooled request fees, §6.5) plus
     `Amount` is at most its balance; the pool has room. Then it is pooled and relayed.
  6. `/hdls/pay` answers `409` with the reason on any refusal. On success the handler signs a
     receipt, once: a payment that was already pooled gets `202` and no new receipt, so a replay
     of a gossiped payment costs no signature.
- **Receipt:** `Receipt{Node, PaymentID, BlockID, Signature}`, with `Node` the node's name (""
  for an unnamed observer). Preimage fields `Node`, `PaymentID`, `BlockID`; domain `receipt`. The
  wallet believes it only under the pinned key of a pinned validator name.

### 4.2 Account update (delegation)

```go
type AccountUpdate struct {
	Name        string // the account; its current key signs
	Delegate    string // "" leaves it unchanged, "-" clears it; else the name the balance backs
	Description string // "" leaves it unchanged, "-" clears it
	Address     string // "" leaves it unchanged, "-" clears it; a validator's gossip URL (R1)
	BlockID     int64
	Signature   string
}
```

"-" is never a name (§3.2), so it can mean "none" (owner to confirm, §12 Q23). Without it a
delegation, for example a treasury's (E42), or a published address (E11) could never be withdrawn.

```json
{"Name":"alice","Delegate":"bank-a","Description":"","Address":"","BlockID":1043,
 "Signature":"<4412>"}
```

- **Preimage fields:** `Name`, `Delegate`, `Description`, `Address`, `BlockID`. **Domain:**
  `account-update`. **Signer:** `Name`'s current key. **Id:** `hashFields("account-update-id", …)`.
- The name claim goes, with the `MaxAccountNameLength` comment about key lengths. An update is
  free, and it counts as work on an idle chain (§6.6, Q5).
- **Size:** JSON at most `MaxMessageBytes`; 4576 bytes at most without text, plus the text fields.
- **Intake**, in order (1 to 3 off the loop):
  1. The body is at most 1 MiB and decodes. `Name` is canonical; `Delegate` is "", "-" or
     canonical; the signature has the exact size; at least one of `Delegate`, `Description`,
     `Address` is set.
  2. Against S: `H <= BlockID <= H + 64`; the JSON is at most `MaxMessageBytes`; `Name` exists and
     is not frozen; `Delegate` is "", "-", or exists and is not frozen.
  3. The signature verifies under `Name`'s key in S.
  4. On the loop: step 2 again; the key is unchanged; an update with the same id already pooled is
     accepted again; no other update is pooled for the same `Name` and `BlockID`. Then it is pooled
     and relayed. The route answers `202` once the update is pooled, now or before, or `409` with
     the reason.

### 4.3 Name requests: the shared struct

Registration, key change, bank change and freeze are one struct with a kind, one pool, one route
(`/hdls/name-request`) and one block field. New file `ledger/name_request.go`.

```go
const (
	RegistrationKind = "registration"
	KeyChangeKind    = "key-change"
	BankChangeKind   = "bank-change"
	FreezeKind       = "freeze"
)

// NameRequest is a holder's or requestor's signed request about one name.
type NameRequest struct {
	Kind               string
	Name               string   // the name the request is about
	Requestor          string   // the name that signs first and pays the fee
	NewKey             string   // the key to bind, for a registration or a key change
	Bank               string   // the co-signing bank, or for a bank change the new bank
	Fee                *big.Rat // NewKeyFee; null for a freeze
	BlockID            int64    // the height of the one block that may carry it
	RequestorSignature string
	BankSignature      string
	NewKeySignature    string
}
```

| Field | Registration | Key change | Bank change | Freeze |
|---|---|---|---|---|
| `Name` | the free name | the name | the name | the name |
| `Requestor` | the funding name | = `Name` | = `Name` | = `Name` |
| `NewKey` | the key to bind | the new key | "" | "" |
| `Bank` | the co-signing bank; it becomes the bank of record | the bank of record | the new bank | "" |
| `Fee` | `NewKeyFee` | `NewKeyFee` | `NewKeyFee` | null |
| `RequestorSignature` | requestor, `registration` | holder, `key-change` | holder, `bank-change` | holder, `freeze` |
| `BankSignature` | bank, `name-request-bank` | bank, `name-request-bank` | new bank, `name-request-bank` | "" |
| `NewKeySignature` | new key, `name-request-new-key` | new key, `name-request-new-key` | "" | "" |

- **Preimage fields, the same for every signature of a request:** `Kind`, `Name`, `Requestor`,
  `NewKey`, `Bank`, `Fee` ("" for null), `BlockID`. A signature therefore covers every field and
  cannot be lifted onto another request or another kind.
- **Signers:** `RequestorSignature` under `Requestor`'s current key, `BankSignature` under `Bank`'s
  current key (the bank's one key, the one it votes with), `NewKeySignature` under `NewKey`.
- **Fee payer:** `Requestor`. The fee is burned.
- **Id:** `hashFields("name-request-id", the preimage fields)`.
- **Size:** fixed fields bound it. A registration is 16136 bytes at most, a key change 16134, a
  bank change 9119 and a freeze 4645 (27-character fee, 19-digit `BlockID`).
- **Shape checks, for every kind, before anything else:** `Kind` is one of the four; `Name`,
  `Requestor` and every non-empty `Bank` are canonical names; `Requestor` equals `Name` except in a
  registration; each field the table marks "" or null is empty, and each other field is set;
  `NewKey` and every signature the kind uses have the exact size; `BlockID > 0`.
- Bank and new-key signatures are not double-sign evidence. Two different requests for one name are
  logged for operators and freeze nothing: wallets keep no sign log, so a re-signed retry must never
  freeze an honest name.
- **Intake**, the same for every kind. Steps 1 to 3 run off the loop: the HTTP handler decodes the
  body, and `AdmitNameRequest(request)` runs the rest of them (`checkNameRequest`), then hands the
  request to the loop.
  1. The body is at most 1 MiB and decodes; the shape checks pass.
  2. Against S: `H <= BlockID <= H + MaxRequestLookahead`, or `H + 64` for a freeze; the fee
     payer's balance is at least `Fee` (a freeze has none); every check of the kind (§4.4 to §4.7)
     except the signatures and the per-block rules; and, for a freeze, that freezing `Name` alone
     would still leave a name seated (rule 24, R9).
  3. The signatures verify.
  4. On the loop (`admitNameRequest(request, requestorKey, bankKey)`): step 2 again; every
     signer's key is unchanged; a request with the same id already pooled is accepted again; no
     other request is pooled for the same `Name` and `BlockID` (a second, different one is logged
     and refused); the fee payer has no pooled payment for the same `BlockID` (§5.5); the fee
     payer's pending outgoing plus `Fee` is at most its balance; the pool has room. Then it is
     pooled and relayed.
     Since code note C39 (2026-10-31) the pool's own `add` checks the slot, after the fee checks,
     so a request that breaks both gets the fee's reason. A full pool and a taken slot share one
     logged refusal.
     The route answers `202`, or `409` with the reason.
- `node.MaxRequestLookahead` is 3,600 blocks, an hour at one block per second (owner to confirm,
  §12 Q24), against 64 for payments and updates. Every signature covers `BlockID`, and three
  people may sign one request on different machines, so they need more than a minute when the
  chain is moving. Pending fees and one request per `Name` and `BlockID` still bound the pool. A
  freeze pays no fee, so it gets the 64 blocks of payments instead: otherwise one name could hold
  3,601 pool slots for nothing. An honest freeze names a block a few ahead (§7.3), since it should
  land at once (owner, 2026-10-30).

### 4.4 Registration

Checked against the pre-block state and V. A registration is invalid unless:

1. no account has `Name`;
2. `Requestor` exists and is not frozen;
3. `Bank` exists, is not frozen, is seated in V (R4), and is `Requestor` itself (R3) or
   `Requestor`'s bank of record;
4. `NewKey` is no name's current key;
5. `Fee` equals the pre-block `NewKeyFee`;
6. the three signatures verify.

Effect, in the block that carries it: the account `{Name, PublicKey: NewKey, Amount: 0, Bank,
Delegate: ""}` is created, and the fee is burned from `Requestor`. It takes effect at once.

### 4.5 Key change

Invalid unless:

1. `Name` exists, is not frozen, is not seated in V, and has no pending change;
2. `Name` has a bank of record, `Bank` equals it, and that bank is not frozen (it needs no seat);
3. `NewKey` is no name's current key;
4. `Fee` equals the pre-block `NewKeyFee`;
5. the three signatures verify.

Effect: the fee is burned from `Name`; `PendingKey = NewKey` and
`PendingHeight = h + ChangeDelayBlocks`. The old key stays current through block `PendingHeight`.
§5.7 says when the change applies or lapses. A sale is a key change to the buyer's key.

### 4.6 Bank change

Invalid unless:

1. `Name` exists, is not frozen, is not seated in V, and has no pending change;
2. `Bank` (the new bank) is not `Name`, exists, is not frozen, and is seated in V (R4);
3. `Fee` equals the pre-block `NewKeyFee`;
4. `RequestorSignature` (the holder) and `BankSignature` (the new bank) verify.

Effect: the fee is burned from `Name`; `PendingBank = Bank` and
`PendingHeight = h + ChangeDelayBlocks`. The old bank is not asked.

### 4.7 Freeze

Invalid unless `Name` exists and is not frozen, and `RequestorSignature` verifies under `Name`'s
key in the `freeze` domain. A seated name may freeze itself: that is the bank's emergency brake.

Effect: `Frozen = true`. Its own pending change is cleared, and so is every pending change it
co-signed: the key change of a name whose bank of record it is, and a bank change to it (R11). It
is free and takes effect at once. A frozen name neither pays nor is paid, sends no update or
request, takes no new delegation, backs no delegate with its balance (R10), and leaves the
validator set with the power delegated to it, until the ⅔ name action re-keys it. A freeze that
would leave no name seated is refused (R9).

### 4.8 The ⅔ name action (`Reassignment`)

```go
type Reassignment struct {
	Name            string
	PreviousAmount  *big.Rat // null, or the balance the name holds now (0 for a new name)
	NewAmount       *big.Rat // null leaves the balance; else the balance to set
	NewKey          string   // "" leaves the key
	NewKeySignature string   // the new key's proof of possession; "" when NewKey is ""
	Bank            string   // "" leaves the bank of record; "-" sets none (§4.2)
	Freeze          bool
	Unfreeze        bool
	Sequence        int64
	Votes           []ReassignmentVote // {Validator: a name, Signature}
}
```

```json
{"Name":"alice","PreviousAmount":null,"NewAmount":null,"NewKey":"<2604>",
 "NewKeySignature":"<4412>","Bank":"","Freeze":false,"Unfreeze":true,"Sequence":7,
 "Votes":[{"Validator":"bank-a","Signature":"<4412>"}]}
```

- **Vote preimage fields:** `Name`, `PreviousAmount`, `NewAmount` (each "" for null), `NewKey`,
  `Bank`, `Freeze`, `Unfreeze`, `Sequence`. **Domain:** `reassignment-vote`. `NewKeySignature` signs
  the same fields in `reassignment-new-key`.
- It binds the name and the governance sequence, and its own fields, never the name's key, so a key
  change landing first cannot make it stale.
- **Size:** fixed fields; 7 KB plus 4.4 KB per vote. `MaxMessageBytes` no longer applies to
  certificates (`node/governance.go` `addParameterVotes` and `node/reassignment.go`
  `addReassignmentVotes`).
- **Valid** against the pre-block state and V only if:
  1. `Sequence` equals the pre-block sequence, and the votes come from distinct names in V, each
     verifying under V's key for that name, with power at least V's quorum;
  2. `Name` is canonical, and `Freeze` is not set together with `Unfreeze` or `NewKey`;
  3. `PreviousAmount` and `NewAmount` are both null or both set. If set, `PreviousAmount` equals
     the name's balance (0 for a new name), and `NewAmount` is at most
     `max(MaxAccountBalance, PreviousAmount)`;
  4. if `NewKey` is set, it has the exact size, is no name's current key, and `NewKeySignature`
     verifies under it; if not, `NewKeySignature` is "";
  5. `Bank` is "" or "-", or exists, is not frozen, and is not `Name`;
  6. for a new name (no account has it): `NewKey` and `NewAmount` are set, and `Freeze` and
     `Unfreeze` are not;
  7. for a frozen name: `Unfreeze` is set exactly when `NewKey` is. Every unfreeze re-keys, and a
     frozen name's key changes only with an unfreeze;
  8. for a name that is not frozen: `Unfreeze` is not set.
- **Effect** (§5.3 has the order): `Freeze` applies in the freeze step. The rest applies in the
  name-action step: a new name is created as `{Name, PublicKey: NewKey, Amount: NewAmount, Bank,
  Delegate: ""}`; otherwise `NewAmount`, `Bank` and `NewKey` replace what they name, and `Unfreeze`
  clears `Frozen`. A `Bank` of "-" sets "". The pending change is cleared (R7). The sequence goes
  up by one.
- **Intake:** `/hdls/reassignment-vote`, a complete certificate only, as today. The pool key
  (`node/reassignment.go` `reassignmentKey`) hashes every vote preimage field, so two actions that
  differ only in `NewKey`, `Bank`, `Freeze` or `Unfreeze` do not share a slot. A certificate whose
  key is already pooled with the same votes is dropped before any signature check, so relayed
  copies cost nothing. Intake refuses, and after each commit or catch-up the pool drops, a
  certificate that breaks rule 1's sequence or rules 2 to 8 but the new key's proof
  (`Reassignment.FitsState`; after a commit: the sequence, the `PreviousAmount`, a `NewKey` now
  some name's key, a `Bank` now frozen, the target's frozen mark), or whose action alone would
  leave no name seated (R9): a freeze or, in dynamic mode, a balance cut (`node/reassignment.go`
  `reassignmentFits`). `ProposeBlock` checks the signatures again.

### 4.9 Parameter change

`ParameterChange{NewParameters, PreviousParametersDigest, Sequence, Votes}`, as today, with the ten
parameters of §3.4. `ParameterVote.Validator` is a name. **Vote preimage:** the ten `fields()`,
then `PreviousParametersDigest` (32 raw bytes), then `Sequence`. **Domain:** `parameter-vote`.
Valid as today: the sequence matches, the digest matches, `Acceptable(len(V))` holds, and the votes
are distinct names in V verifying under V's keys with power at least the quorum. No
`MaxMessageBytes` check. A certificate already pooled with the same votes is dropped before
`Valid`, as in §4.8. Size: about 0.5 KB plus 4.4 KB per vote.

### 4.10 Vote

```go
type Vote struct {
	Height    int64
	Round     int32
	Type      VoteType // Prevote (1) or Precommit (2)
	BlockHash [32]byte // §5.1; all zero for a nil vote
	Validator string   // the signer's name
	Signature string
}
```

- **Preimage fields:** `Height`, `Round`, one byte `Type`, `BlockHash` (32 raw bytes),
  `Validator`. **Domain:** `block-revision`. **Signer:** the key V holds for `Validator`.
  `Vote.Verify(scheme, networkID, publicKey)` takes the key.
- **Size:** 4674 bytes.
- **Intake** (on the loop, `node/node.go` `countable`, as today): first, a vote equal, signature
  bytes included, to the one held in its slot (`rememberVote`) is dropped, with no check: signing
  is deterministic, so relayed copies of an honest vote are byte-identical. Then the height is the
  height in progress; `Validator` is in V; the type is known; the round is from 0 to the driver's
  round plus `MaxRoundLookahead`; then the signature under V's key. So each distinct vote costs
  two checks (here and in the vote set), not one per relayed copy.

### 4.11 Proposal

```go
// SignedProposal is what a proposer signs: one block for one height and round.
// New file consensus/proposal.go.
type SignedProposal struct {
	Height     int64
	Round      int32
	ValidRound int32
	BlockHash  [32]byte
	Proposer   string // the proposer's name
	Signature  string
}

// ProposalMessage carries a signed proposal and its block (node/messages.go).
type ProposalMessage struct {
	Proposal SignedProposal
	Block    ledger.Block
}
```

- **Preimage fields:** `Height`, `Round`, `ValidRound`, `BlockHash` (32 raw bytes), `Proposer`.
  **Domain:** `proposal`. **Signer:** the key V holds for `Proposer`. The block hash covers the
  whole block (§5.1), so the signature covers the block (E15).
- **Size:** 4.7 KB for the signed proposal; a full block adds up to 768 KiB of content, the
  evidence and one certificate. The `/hdls/proposal` route decodes up to 2 MiB; every other route
  keeps 1 MiB.
- **Intake** (`node/node.go` `onProposal`), in order:
  1. `Proposal.Height` is the height in progress, and a set exists;
  2. no proposal is held for the round yet (a second, authentic one with another block hash
     becomes proposal evidence, §6.4);
  3. the round is from 0 to the driver's round plus one, and fewer than `maxInvalidProposals`
     copies failed for it;
  4. `Proposal.Proposer` is V's proposer for the height and round;
  5. the signature verifies under V's key for the proposer;
  6. `ledger.BlockHash(Proposal.Block)` equals `Proposal.BlockHash`, and the block's height
     matches;
  7. `ApplyBlock` accepts the block.

  A copy that fails step 5 or 6 is not authentic: it is dropped, never held, and a genuine copy that
  comes later is taken.

### 4.12 Double-sign evidence

```go
type VoteEvidence struct {
	Name   string // the signer
	First  Vote
	Second Vote
}

type ProposalEvidence struct {
	Name   string
	First  SignedProposal
	Second SignedProposal
}
```

- **Vote evidence is valid** when `First` and `Second` have the same `Height`, `Round` and
  `Type` (prevote or precommit); both name `Name` as `Validator`, byte for byte; their `BlockHash`es
  differ; and both verify under `Name`'s current key in the pre-block state.
- **Proposal evidence is valid** when both have the same `Height` and `Round`; both name `Name` as
  `Proposer`; their `BlockHash`es differ; and both verify under `Name`'s current key. A different
  `ValidRound` or different signature bytes never count.
- In both cases `Name` must exist and not be frozen before the block, and the evidence height must
  be below the block's height. There is no age limit and no check against any past validator set:
  `maxEvidenceAge` and `evidenceInWindow` go. Once a name's key changes, older evidence against it
  is moot, because it no longer verifies.
- **Size:** 9410 bytes for vote evidence; about the same for proposal evidence.
- Nodes collect evidence themselves (§6.4). No route takes evidence from clients.

### 4.13 Commit

`Commit{Height, Round, BlockHash, Votes}`, as today but over the block hash. `Commit.Verify(scheme,
networkID, set)` checks that every vote is a precommit for that height, round and hash, from a
distinct name in the set, verifying under the set's key for that name, and that their power reaches
the set's quorum. Size: 14 KB with 4 validators (quorum 3), 70 KB with 21 (quorum 15).

### 4.14 Checkpoint

```go
type Checkpoint struct {
	State      ledger.LedgerState     // the committed state after the last block
	LastBlock  ledger.BlockHeader     // what is needed to recompute that block's hash
	Commit     consensus.Commit       // the certificate that finalised it
	Validators []consensus.Validator  // the set that signed Commit: name, key and power
}
```

- `BlockHeader{Height, PreviousBlockHash, ValidatorsHash, Nonce, ContentHash}`.
- The check runs in this order, and each step runs only if the one before passed:
  1. **Shape:** `State.Height == LastBlock.Height == Commit.Height`; `State`'s names are strictly
     ascending (§3.5); `Validators` is sorted by name, with distinct names and power at least 1.
  2. **Hashes:** the block hash recomputed from `LastBlock` and `State.Digest()` (§5.1) equals
     `Commit.BlockHash`, and `ValidatorsHash(Validators)` equals `LastBlock.ValidatorsHash`.
  3. **Anchor** (`verifyCheckpoint` only): the commit's votes from anchor names whose key in
     `Validators` equals the anchor's key are verified, one per name; the names whose votes verify
     must hold more than a third of the anchor's power. The anchor is the set (name, key, power)
     derived from the node's last committed state (§6.7).
  4. **Commit:** `Commit.Verify` against `Validators`.
- **Self-consistent** (the node's own snapshot) means steps 1, 2 and 4; **trusted** (a peer's
  checkpoint) means all four. The checkpoint carries the signing set in both modes, since keys live
  in state and the ⅔ can re-key a static validator. The set is not signed, but the block hash
  covers its hash, so after step 3 it is the real set. So a forged checkpoint that gives real bank
  names keys an attacker holds is refused, and so is one padded with made-up validators and their
  votes, which the Phase 1 check accepts after verifying every pad. A check costs at most one
  signature check per anchor name plus one per member of `Validators`.
- **Size:** 2864 bytes per account (5480 with a pending key), 2684 bytes per validator, plus the
  commit. The 64 MiB fetch limit holds about 23,000 accounts.

## 5. Block validity and application

### 5.1 The block and its hashes

```go
type Block struct {
	Height            int64
	PreviousBlockHash [32]byte // the hash of the block before; GenesisHash for block 1
	ValidatorsHash    [32]byte // ValidatorsHash(V): the set that signs this block (§5.2)
	Nonce             [32]byte // random, drawn by the proposer; kept only in the last block's header (§3.6)
	Content           BlockContent
	StateDigest       [32]byte // the digest of the resulting state
}

type BlockContent struct {
	Payments         []Payment
	AccountUpdates   []AccountUpdate
	NameRequests     []NameRequest
	VoteEvidence     []consensus.VoteEvidence
	ProposalEvidence []consensus.ProposalEvidence
	ParameterChange  *ParameterChange
	Reassignment     *Reassignment
}
```

New file `ledger/hash.go`:

| Hash | Fields |
|---|---|
| `ContentHash` | `"go-ledger block content v2"`, then `json.Marshal(block.Content)` as one field |
| `SealedDigest` | `"go-ledger sealed digest v2"`, `Nonce`, `StateDigest`, `ContentHash` (32 raw bytes each) |
| `ValidatorsHash` | `"go-ledger validators v2"`, then for each member of the set in name order: `Name`, `PublicKey`, `Power` |
| `BlockHash` | `"go-ledger block hash v2"`, `Height`, `PreviousBlockHash`, `ValidatorsHash`, `SealedDigest` |

- Functions: `ContentHash(content)`, `SealedDigestOf(nonce, stateDigest, contentHash)`,
  `ValidatorsHash(set)`, `BlockHashOf(height, previousBlockHash, validatorsHash, sealedDigest)`,
  `BlockHash(block)`, which chains them, and `block.Header()`, which gives the `BlockHeader` a
  checkpoint carries (§4.14). A checkpoint check computes `BlockHashOf(header.Height,
  header.PreviousBlockHash, header.ValidatorsHash, SealedDigestOf(header.Nonce, state.Digest(),
  header.ContentHash))`.
- Votes, commits and proposals sign `BlockHash`, so they also sign the set that signs them: a
  checkpoint or record can carry that set unsigned, and a reader checks it against the hash. The
  signature record keeps `PreviousBlockHash` and `SealedDigest`, from which anyone recomputes
  `BlockHash` and checks the chain, but never the state digest, which the random nonce hides (§6.8).
- The state digest no longer identifies a block: two blocks that differ only in their nonce share
  it. Every place that keys or compares a block by `Block.StateDigest` today uses
  `ledger.BlockHash(block)` instead: `node.blocks` and the driver's value (`node/node.go`
  `startHeight`, `onProposal`, `process`, `commit`), `reportEquivocation`, the proposal relay key
  (`node/relay.go` `messageKey`) and `logProposal`'s refusal of another block for a logged round
  (`node/persist.go`). `Block` keeps a `StateDigest` field, so the compiler flags none of them.
- `json.Marshal` is deterministic for a given Go value and binary, and every node runs the same
  release. Receivers hash the decoded value, not the bytes they received: a relay's change that
  decodes to the same value changes nothing, and any other change breaks the proposal signature.
- `BlockID` of every payment, update and request must equal `Height`, the replay guard, as today.
- The node draws the nonce from `crypto/rand` when it builds its candidate block and passes it to
  `ProposeBlock`; the ledger stays free of I/O. A restarted proposer re-sends its logged proposal,
  nonce included (Phase 1 logs a proposal before sending it).

### 5.2 Chain rules and the validator set by name

```go
// Chain is the fixed facts of one chain.
type Chain struct {
	Scheme        signing.Scheme
	NetworkID     string        // the effective network id
	GenesisHash   [32]byte      // block 1's PreviousBlockHash; tests may leave it zero
	ValidatorRule ValidatorRule
}

// ValidatorRule says how the validator set follows from the state.
type ValidatorRule struct {
	StaticValidators []StaticValidator // static mode: names and powers; empty in dynamic mode
	Threshold        *big.Rat          // dynamic mode: the stake a name needs; nil in static mode
}

type StaticValidator struct {
	Name  string
	Power int64
}
```

- `consensus.Validator` becomes `{Name, PublicKey, Power}`. `ValidatorSet` is keyed and sorted by
  name, and gains `PublicKey(name)`. `ProposerAt` returns a name.
- `ValidatorRule.SetFor(state)` moves from `node/validators.go` (`validatorsFor`,
  `deriveValidators`) into new file `ledger/validators.go`:
  - static: each static validator whose account exists and is not frozen, with its configured power
    and the account's current key;
  - dynamic: each unfrozen account's balance counts toward its `Delegate` (R10); a name whose
    total reaches the threshold, that exists and is not frozen, is seated with power
    `floor(stake)`, saturating at `MaxValidatorPower`, and its current key.
- `powerIgnoringFreezes(rule, state)` is the same derivation as if no account were frozen. It gives
  each name's power for the one-third cap (§5.8), which R10 therefore leaves unchanged.

### 5.3 `ApplyBlock`, step by step

`ApplyBlock(chain, previousState, previousBlockHash, block) (LedgerState, error)`. It returns an
error whose text names the broken rule. Everything in steps A to G is judged against the pre-block
state; nothing a block does counts for its own checks.

**A. Header**

1. `block.Height == previousState.Height + 1` (E1).
2. `block.PreviousBlockHash == previousBlockHash`.

**B. Sets**

3. V = `SetFor(previousState)`. P = `powerIgnoringFreezes(previousState)`.
   `block.ValidatorsHash == ValidatorsHash(V)`.

**C. Governance**

4. Not both `ParameterChange` and `Reassignment`.
5. A `ParameterChange` is valid (§4.9).
6. A `Reassignment` is valid (§4.8).

**D. Freezes and locked names**

7. Each vote evidence and proposal evidence item is valid (§4.12).
8. At most one evidence item per name.
9. Each freeze request passes the shape checks (§4.3), has `BlockID == h`, and is valid (§4.7).
10. No evidence or freeze request names the `Reassignment`'s target.
11. F = the evidence names, the freeze-request names, and the `Reassignment`'s name when it sets
    `Freeze`. L (the locked names) = F plus the `Reassignment`'s name.

**E. Name requests other than freezes**

12. Each passes the shape checks (§4.3), has `BlockID == h`, and is valid for its kind (§4.4 to
    §4.6).
13. At most one request of any kind per `Name`.
14. Each new key appears at most once among the block's registrations, key changes and
    `Reassignment`.
15. No request's `Name`, `Requestor` or `Bank` is in L (R6).
16. For each fee payer, its fees in the block add up to at most its pre-block balance.

**F. Payments**

17. Each payment has `BlockID == h`; a valid signature under `From`'s key; a JSON size at most
    `MaxMessageBytes`; `TransactionCost <= Amount <= MaxPaymentAmount`; an `Amount` at most `From`'s
    pre-block balance; `From` and `To` that exist and are not frozen; and a `PreviousStateDigest`
    that is "" or hex of `previousState.Digest()` (E38).
18. `From` and `To` are not in L (R6, E5).
19. `From` pays no fee in this block.

**G. Account updates**

20. Each has `BlockID == h`, a valid signature, a size at most `MaxMessageBytes`, a `Name` that
    exists and is not frozen, a `Delegate` that is "", "-", or exists and is not frozen, and at
    least one field set.
21. At most one update per `Name`; neither `Name` nor `Delegate` is in L.

**H. The one-third cap**, only when the block carries evidence

22. With N = the names frozen before the block plus F: `3 × ΣP(N) < ΣP(V ∪ N)`.

**I. Apply, in this order**

1. Payments, through `ProcessPayments` keyed by name: the duplicate-content collapse, the fee
   burn and the overdraw ban, as today.
2. Account updates: `Delegate`, `Description`, `Address` ("-" sets "").
3. Name requests other than freezes: burn each fee from its payer; create each registered account;
   set each key change's or bank change's pending fields.
4. Freezes, from every source in F: set `Frozen`; clear the frozen name's pending change and every
   pending change it co-signed (R11). The balance is not touched (R5).
5. The `Reassignment`, its non-freeze parts (§4.8), clearing the name's pending change (R7).
6. Due changes: each account, in name order, whose `PendingHeight == h`, each judged against the
   state the changes before it left (§5.7).
7. `Height = h`; the new parameters if a `ParameterChange` applied; the sequence plus one if any
   governance action applied.

**J. Result**

23. No account ends above the pre-block `MaxAccountBalance` unless its balance did not rise in this
    block. New accounts count as rising from 0. A cap lowered by this block applies from the next.
24. `SetFor(nextState)` seats at least one name (R9).
25. `nextState.Digest() == block.StateDigest`.

### 5.4 `ProposeBlock`

`ProposeBlock(chain, previousState, previousBlockHash, nonce, content) Block` builds a block that
passes §5.3, choosing in this order:

1. At most one governance action, a valid parameter change before a valid reassignment, as today.
2. Valid freeze requests, at most one per name, none for the action's target.
3. Valid evidence, vote evidence first and then proposal evidence, each in name order: at most one
   item per name and eight per block, none for a locked name (the action's target, or a name step 2
   freezes), and each kept only while rule 22 still holds with its name and every name the block
   freezes so far; the rest wait.
4. L, from 1 to 3.
5. Valid name requests whose `Name`, `Requestor` and `Bank` are not in L, one per `Name`, one
   per new key (the set of used new keys starts with the action's `NewKey`, rule 14), keeping each
   payer's fees within its balance.
6. Valid payments whose `From` and `To` are not in L and whose `From` pays no fee in the block,
   keeping each payee's running balance within `MaxAccountBalance`.
7. Valid account updates whose `Name` and `Delegate` are not in L, one per name.

Freeze requests, name requests, payments and updates, in that order, stop at 768 KiB of JSON
(`maxBlockContentBytes`, unchanged); evidence and the governance action sit outside it.

Signature checks come last. For each pooled entry `ProposeBlock` first applies the cheap rules
(`BlockID == h`, sizes, existence and frozen marks, L, balances, fees, caps, the budget), and it
verifies the entry's signatures only when the entry is about to be added; it stops once the budget
is full. Every node builds a candidate at every height, so a height then costs the signature
checks of one block, whatever the pools hold.

The resulting state gives `StateDigest`. If it would seat no name (rule 24), `ProposeBlock` takes
items out, the last added first, until it seats one; those items lapse or wait, like evidence over
the cap (R9).

### 5.5 Fees

One function, used by intake, `ProposeBlock` and `ApplyBlock`, decides whether a payer can pay a
set of fees: their sum is at most the payer's balance, and the payer sends no payment in that block.
Intake counts the fees of pooled requests as the payer's pending outgoing (§6.5). A block prefers a
payer's requests: its payments for that block lapse. So intake refuses (`409`) a payment whose
payer is the fee payer of a pooled request for the same block, and the reverse, rather than give a
receipt for a payment that would lapse.

### 5.6 Caps

- `MaxPaymentAmount` bounds each payment, at intake, in `ProposeBlock` and in `ApplyBlock`.
- `MaxAccountBalance` bounds the balance a payment or the ⅔ raises a name to. Intake checks one
  payment against the committed balance. `ProposeBlock` keeps a running total per payee. Rule 23
  checks the result against the pre-block cap, as intake and `ProposeBlock` do. A balance already
  above a lowered cap may stay there and shrink.

### 5.7 Pending changes

- Filing sets `PendingKey` or `PendingBank`, and `PendingHeight = h + ChangeDelayBlocks`, with the
  pre-block value of `ChangeDelayBlocks`.
- At the end of block `PendingHeight` (step I.6), in name order, a pending change **lapses** if the
  name is seated in V, or, for a key change, `PendingKey` is by then some name's key. "By then"
  means after steps I.1 to I.5 and the due changes before it in name order, so two due changes to
  one key never both apply. Otherwise `PendingKey` becomes `PublicKey`, or `PendingBank` becomes
  `Bank`. Either way the pending fields are cleared. The fee is not refunded.
- A freeze clears its name's pending change and every pending change it co-signed (R11). The
  co-signer of a pending key change is always the name's current bank of record, since no bank
  change can be pending beside it, and that of a pending bank change is its `PendingBank`. So a
  bank change never meets a frozen new bank when it falls due. The ⅔ action clears its name's
  pending change (R7).
- Pooled messages signed by a key that is no longer its name's key are dropped after the commit
  that changed it (§6.5).

### 5.8 Freezes and the one-third cap

- A frozen name is out of V and stays out until the ⅔ action unfreezes it with a new key.
- The cap counts each name's power from the pre-block state, frozen or not (P). Evidence that would
  bring the frozen names to a third or more of the power of seated and frozen names together waits:
  it stays valid while the key is current, or the ⅔ re-key makes it moot. Holder and ⅔ freezes have
  no cap.
- The scope, for `docs/security.md`: the freeze handles double-signing by under a third of the
  power. At a third or more, recovery is a re-genesis with new keys and a new network id. Losing
  the largest validator must still leave more than ⅔ of the rest online. A holder's self-freeze is
  not capped: a bank holding a third or more that brakes hands the chain to the rest, and lagging
  nodes and pinned wallets then need a re-seed or a new list (§6.7, §7.2).

## 6. Consensus and node

### 6.1 Node config

```go
type Config struct {
	Chain            ledger.Chain       // scheme, effective network id, validator rule
	Signer           *signing.Signer
	Name             string             // this node's account name; "" for an unnamed observer
	Genesis          ledger.LedgerState // height 0, with the genesis parameters
	Bootstrap        []string
	SnapshotPath     string
	BackupPath       string
	MaxMempool       int
	MaxCommentLength int
}
```

`TransactionCost`, `Timeout`, `MinBlockInterval`, `MaxMessageBytes`, `MaxPeers`, `Validators` and
`ValidatorThreshold` go: they live in `Genesis.Parameters` and `Chain.ValidatorRule`. The re-gossip
interval is the `StepTimeoutMs` in force, the wait after a start without a sign log two of them
(§6.9), and the catch-up interval is five `BlockIntervalMs` in force (at least 3 s). A ⅔ change of
the timings reaches them within a second (owner, 2026-10-31, code note C21; it was the genesis
`StepTimeoutMs`).

### 6.2 Proposals

- The proposer signs `SignedProposal` over the block hash (§4.11), logs it in the sign log, and
  sends it (`node/node.go` `process`, as today).
- `onProposal` follows §4.11's order: everything cheap, then one signature, then the block hash,
  then `ApplyBlock`.
- The node keeps the hash and header of its last committed block, at genesis `GenesisHash` and no
  header, and passes the hash to `ProposeBlock` and `ApplyBlock`.
- The node may keep the state `ApplyBlock` returned in `onProposal`, by block hash, and reuse it at
  commit instead of applying the block a second time.

### 6.3 Votes, and signing only while seated

- The driver's `self` is the node's name, and votes are counted under V's keys.
- **A node signs only at heights where its name is seated in V with exactly its configured key.**
  Otherwise `startHeight` gives the driver no signer: it follows the rounds and decides, but never
  signs a vote or a proposal, and keeps no sign-log entries. In `castVote`, a driver with no signer
  marks the vote as cast and returns an empty, non-nil action list, so the rules move on and no
  timer re-arms forever; `startRound` emits no proposal. When the name is seated under another
  key, the node logs once that its key is no longer the name's key.

### 6.4 Evidence and pending proofs

1. `pendingSlashes` becomes `pendingProofs`, keyed by name. Each entry holds one vote evidence or
   proposal evidence item and the key it was checked under (the convicted key).
2. Sources: the driver's vote sets when a height ends (`retireDriver`); late precommits that
   conflict with the last commit (`recordLateDoubleSign`); and a second authentic proposal for a
   held round whose block hash differs (`reportEquivocation`, which now also records the pair).
3. The proposer offers every pending proof to `ProposeBlock`, which applies the validity rules and
   the cap. The filter on set membership in `slashEvidence` goes.
4. After each commit or catch-up, a proof is dropped when its name is frozen, or its convicted key
   is no longer the name's current key: the ⅔ action re-keyed the name, or a key change it filed
   once unseated (§4.5) applied. The prune by membership in `commit` goes. Nothing else drops a
   proof: moving the stake away alone does not.
5. Proofs on disk survive a restart. The cap can hold a proof back for days, and a release upgrade
   restarts every node. `saveSnapshot` writes the snapshot file as `savedSnapshot{NetworkID,
   Checkpoint, PendingProofs}` at each commit, catch-up and adopt, each proof with its convicted
   key; the served `Checkpoint` never carries them. `loadSnapshot` reloads them and prunes them by
   item 4 against the loaded state. Until the next snapshot write, a proof lives only in memory (the
   running driver's vote sets, a late precommit, a second proposal), and a restart loses it. A proof
   the cap holds back while the chain idles stays in memory until other work brings a commit (§6.6;
   accepted on 2026-10-31, §12).

### 6.5 Intake, pools and the committed view

- The node publishes an immutable **committed view** after `Open`, each commit and each catch-up:
  the committed state, its digest, accounts by name, and V for the next height. It replaces the
  anchor that `publishAnchor` stores. HTTP handlers run the cheap checks and the signature checks
  against it, off the event loop. The loop re-runs the cheap checks against live state and confirms
  that each signer's key is still the key that was checked.
- Pools: payments (`mempool`), account updates (`updatePool`), name requests (new
  `node/requestpool.go`; one per `Name` and `BlockID`; capacity `MaxMempool`). The payment pool and
  the request pool share the **pending outgoing** of a payer: its pooled payments plus its pooled
  fees.
- After each commit or catch-up (`reconcilePools`): drop entries whose block has passed; drop
  entries whose signer, requestor or bank is frozen or has a new key; re-run the cheap checks
  (R9's included) and drop failures; then drop, in arrival order, whatever no longer fits a payer's
  balance, payments and fees together.
- Gossip: new message type `MessageNameRequest`, beside the renamed `MessagePayment` and the rest,
  with relay key `"request:" + request id`.

### 6.6 Idle chain (owner to confirm)

- After a commit, the next height starts after `BlockIntervalMs`, as today (at a height a node back
  without its sign log waits at, after the longer of it and two re-gossip intervals, §6.9), but only
  when there is work. Work is any of: the chain has committed no block yet (so block 1 always
  commits, and a new wallet has a commit to pin); the candidate block carries something; a pool
  holds an entry for a later block; some account has a pending change; the sign log holds a vote or
  proposal for the height (a restart mid-height; the empty log of §6.9 does not count); the node
  holds a proposal or a countable vote for the height from a peer.
- A node with no driver is in one of three states: the window after a commit (`BlockIntervalMs`,
  or two re-gossip intervals if longer at a height it waits at), the re-gossip wait of a node back
  without its sign log (`waitForOwnSignatures`, §6.9), or idle. Only `startHeight` sets the `idle`
  field: right after it builds the candidate, if `hasWork` is false, it sets `idle` and returns with
  no driver. Otherwise it clears `idle` and goes on. The wait clears it too, after a catch-up.
- An idle node signs nothing and re-gossips nothing. `wakeIfIdle` does nothing unless `idle` is
  set; then it calls `startHeight`. It is called once a payment, update, name request or certificate
  is pooled (a certificate that replaces a pooled one too: it may be the first a block can carry),
  once `onProposal` takes a proposal, once `rememberVote` keeps a countable vote, and once
  `recordLateDoubleSign` keeps a proof (S10's check: otherwise the proof waits in memory, and a
  restart of every node meanwhile loses it; kept, 2026-10-31, §12). The
  two waits keep their own timers, so a peer's vote during the lost-log wait cannot make the node
  sign before its own earlier votes come back, and a payment in the post-commit window does not
  start the next height early.
- So a chain that nobody uses commits and records nothing. A pending change keeps it moving at
  `BlockIntervalMs` until it falls due, so `ChangeDelayBlocks` keeps its meaning of about a day. An
  entry for a later block keeps it moving until that block, or that entry would never land. So one
  name can keep the chain busy for free with updates (Q5): the cost is the busy rate of §9.3, and
  the ⅔ can freeze such a name.
- Catch-up asks a peer's `/hdls/status` first, and fetches `/hdls/checkpoint` only from a peer whose
  height is ahead (a checkpoint is now about 2.9 KB per account).
- Code: `hasWork(candidateBlock ledger.Block) bool` and `wakeIfIdle()` in new file
  `node/idle.go`. Slice S5 adds the `idle` field to `Node`, the check in `startHeight` and every
  `wakeIfIdle` call above, with stub bodies (`hasWork` returns true, `wakeIfIdle` does nothing);
  slice S10 fills in the two bodies.

### 6.7 Catch-up, trust anchor, checkpoint verification

- The anchor is the committed view's V: (name, key, power) for each seated name. `bestCheckpoint`
  and the loop's `adopt` and `catchUp` verify against it, as in §4.14.
- A joiner's anchor is the genesis set. On a network whose banks have re-keyed or left, a joiner
  seeds its snapshot from a bank it trusts, as Phase 1's docs already say.
- `loadSnapshot` trusts the node's own snapshot when it is self-consistent, as today.
- `fastForward` sets the state, the last block hash (the hash recomputed from the checkpoint) and
  the last header.

### 6.8 The signature record

**What is kept.** For each committed block, a rolling entry:

```go
type RecordEntry struct {
	Height            int64
	PreviousBlockHash [32]byte
	SealedDigest      [32]byte                 // mixes the nonce; never the bare state digest
	Proposal          consensus.SignedProposal // the proposal of the round that decided it
	Commit            consensus.Commit
	// Validators is the signing set, written when its ValidatorsHash differs from the entry
	// before's and in the first entry of each unit file; otherwise empty.
	Validators        []consensus.Validator
	KeepUntilHeight   int64
}
```

For a block that carries a governance action or evidence, also a lifelong entry:

```go
type LifelongRecordEntry struct {
	Height           int64
	BlockHash        [32]byte
	ParameterChange  *ledger.ParameterChange
	Reassignment     *ledger.Reassignment
	VoteEvidence     []consensus.VoteEvidence
	ProposalEvidence []consensus.ProposalEvidence
	// Signers holds the name, key and power, at that height, of every name whose signature the
	// entry holds (power 0 for a non-validator). They can be checked against the rolling record
	// while it covers that height; after that, only by comparing nodes' copies.
	Signers          []consensus.Validator
}
```

Payments, account updates, holder requests, nonces and state digests are never written.

**Files**, beside the snapshot, only when `SnapshotPath` is set:

- `<snapshotPath>.<network>.record/<first height, 20 digits>.jsonl`: rolling entries, one JSON
  object per line, in units of 100 heights. The unit of height h starts at h − (h mod 100).
- `<snapshotPath>.<network>.lifelong.jsonl`: lifelong entries, one per line, never pruned.
- `<network>` is the first 16 hex digits of the genesis hash. So a re-genesis starts new record
  files and never reads or extends the old ones; the operator deletes those, as the docs say
  (owner, 2026-10-30, slice S7b; S7 built one name for every network and segments of 10,000
  heights).
- Written in `commit` after the snapshot. A failed write is logged, not fatal. A partial last line
  is skipped when read. A node that catches up records nothing for the heights it skipped.

**Retention.** `KeepUntilHeight = h + RetainBlocks`, with the pre-block value, and never less than
the entry before it in the same unit file. At each commit the node deletes the oldest unit file once
the new height passes both its last entry's `KeepUntilHeight` and its last height plus the current
`RetainBlocks`. So a longer retention applies at once, to old unit files too, and a cut applies
only to records written after it. The node remembers how the oldest unit file ended, and lists the
record folder only once that unit is due (S7b's check: with units of 100, a listing at every commit
cost 12 ms at the default retention).

**Routes.** `GET /hdls/record?from=H` returns a JSON array of the rolling entries the node holds
for heights H to H + 99, so it reads at most two unit files. With no `from` it returns the newest
unit file's entries: the latest signatures. `GET /hdls/lifelong-record?from=H` returns at most 100
lifelong entries with height at least H, and with no `from` the newest 100. A negative or malformed
`from` is a 400 (S7b: from −99 to −1 would serve unit 0 twice). So the unit bounds
what one request costs: at most two unit files, about 3.7 MB with 4 validators and 14 MB with 21
(§9.3). The lifelong file grows only with governance actions and evidence. A complete unit file
never changes until it is pruned, so it can be served as a static file. The owner, 2026-10-30:
"pool up blocks into units of 10/100/1000 or so so you can ask for the records and specify a
period and it will give you that element and up to 100 slots in the future (so you have the same
logic for current signatures) and doing it blank will only give you the last file". S7 built a
`count` parameter, and segments of 10,000 heights that one request could read whole: about
180 MB with 4 validators.

**Check.** From each rolling entry, anyone recomputes `BlockHashOf(Height, PreviousBlockHash,
ValidatorsHash(set), SealedDigest)`, with the latest `Validators` the record holds at or before
that height as the set; checks that it equals `Commit.BlockHash` and `Proposal.BlockHash`; verifies
both under that set; and checks that each entry's `PreviousBlockHash` is the hash of the entry
before. Because each block hash covers its set, one trusted block hash, from a commit verified
under a wallet pin or a node anchor, vouches for every earlier entry and its sets, back along
`PreviousBlockHash`. Without one, a node can serve a record with made-up sets that checks.

In dynamic mode a seat's power is `floor(stake)`, so most busy blocks change the set, and the
record shows each seat's stake per height (the conflict flagged in §0.3).

**Code:** `node/record.go` writes and reads the record. `commit` calls
`node.recordCommittedBlock(previousState, block, proposal, commit, signingSet)` after the snapshot,
with `previousState` taken before `commit` replaces the state (for the pre-block `RetainBlocks` and
the signers' keys), and `readRecord` reads the files for the routes. The routes' handlers,
`handleRecord` and `handleLifelongRecord`, sit in `node/httptransport.go` with the other routes
since code note C43 (2026-10-31). Slice S4 added the hook and the two handlers with stub bodies,
all three in `node/record.go`, and slice S7 filled them in.

### 6.9 Sign log

Only a node that signs (§6.3) writes it, and it keeps only that node from signing twice: two nodes
holding one key each keep their own (§8.2). `loadSignLog` verifies the logged votes and proposals
under the node's own key. Beyond the new message formats:

- `logProposal` refuses another block for a logged round by block hash, not by state digest
  (§5.1). Otherwise two empty candidates with different nonces would both be signed.
- Once `commit` has written the snapshot of height h to disk without error, a node whose log holds
  h writes an empty log for h + 1, unless it waits there (below), when it empties the log at the
  log's own height. So the last signed block, its payments and nonce included, is not kept while
  the chain idles (§3.6), and a restart at h + 1 finds a complete log and starts at once. After a
  failed snapshot write it keeps the old log, since `loadSignLog` refuses a log ahead of the
  snapshot. `saveSnapshot` therefore returns its error.
- `catchUp` empties the log for the new height once its snapshot is on disk, as `commit` does,
  unless the node waits there (below). `adopt` empties it at the log's own height, so a node that
  adopts a checkpoint after a restart still waits before it signs (code note C10, 2026-10-31).
- At `Open`, `loadSignLog` empties a log left below the restart height, at the log's own height, as
  `adopt` does (wrap-up, 2026-10-31). A stop between `commit`'s snapshot and its empty log leaves
  such a log, block, payments and nonce included; they go at the next start, and the node still
  waits before it signs (`startFirstHeight`).
- The wait after a lost log covers every height the node may have signed at, not only the one it
  starts at (`waitForOwnSignatures`; found by the final hardening check, built in the wrap-up,
  2026-10-31). Those heights run up to `mayHaveSignedUpTo`: the height it came back at, raised by
  each vote or proposal of its own name for a later height that verifies under its key
  (`noteOwnSignature`). The wait lasts two re-gossip intervals: a peer's ticks drift later by the
  time each takes, so one interval can end just before a tick (after the recheck; it was one). A
  commit into such a height starts it after that wait, or the block interval if longer, and a
  catch-up from such a height waits at the network's height too.
  Meanwhile `commit` and `catchUp` empty the log at its own height, so a restart during the wait
  waits too, and a start timer starts only the height it was armed for. The node keeps no vote or
  proposal of its own for a later height: the re-gossip at that height brings it again, through
  that height's checks. The waits end: `mayHaveSignedUpTo` rises only with the node's own
  signatures, and a catch-up waits only from a height up to it. A node that never lost a log but
  waits at the height it opens at anyway (an observer, an unseated name, a validator that adopted
  a checkpoint at start) now also waits once after a catch-up from that height: a delay only.
- The wait is best effort, by timing, and recovers only what running peers hold and re-gossip
  during it: the votes of their height, their current round's proposal (any round up to 256, see
  below), and the commit of the height just below theirs, its precommits and proposal. Nothing else
  comes back, so a node can still sign against its own earlier votes and proposals at a height two
  or more below its peers' (the one it opens at, say), where it signs after the wait, before it
  catches up; against those at the height just below theirs that the commit lacks, its precommit
  for that height's decision among them; and against its proposal of a round its peers have left. A
  node with no snapshot starts at genesis, with no wait (findings of the independent check of the
  wait, 2026-10-31).
- The sign log keeps `mayHaveSignedUpTo` (its `MayHaveSignedUpTo`, written with every log), and
  `loadSignLog` takes it back from any log of the node's network: a node that signed at the height
  it came back at, and restarts with that log, starts there at once, and must still wait above it.
  `noteOwnSignature` writes the node's log again, whatever its height, when it raises the bound
  (wrap-up, 2026-10-31, after the independent check of the wait and its recheck); a node with no log
  yet writes the bound with its first, and a restart before then waits where it opens and learns the
  bound again. A new log of a height starts with
  the node's own votes and proposals it holds there, those it took back, so that a restart which
  starts at once from that log does not forget them (`heightSignLog`; after the recheck).
- While no driver runs, `onProposal` takes a proposal in the node's own name for any round up to
  `MaxRoundLookahead` (256), past which no vote counts either, not only up to round 1: its peers
  re-gossip their current round's proposal, whatever its number, and the node sends it again once
  their votes take it to that round. Only the key its name is seated under signs those, one per
  round: the node's own key, or the new key of a re-keyed name (after the recheck: the cap).
- `Open` refuses a `SnapshotPath` whose folder does not exist (code note C9).

### 6.10 HTTP routes

| Route | Change |
|---|---|
| `POST /hdls/pay`, `/hdls/payment` | payments by name; off-loop checks; `409` with the reason; a repeat of a pooled payment gets `202` and no new receipt |
| `POST /hdls/update` | delegation, description, address; synchronous `409` with the reason |
| `POST /hdls/name-request` | **new**: registration, key change, bank change, freeze; `202` or `409` |
| `POST /hdls/parameter-vote`, `/hdls/reassignment-vote` | new fields; votes by name; `202` once the certificate is pooled, or `409` with the reason (code note C8) |
| `POST /hdls/proposal` | `ProposalMessage`; 2 MiB limit |
| `POST /hdls/vote` | votes by name |
| `GET /hdls/status` | adds `networkId` (the effective id); read off the event loop, from the committed view; its height key is `heightInProgress`, the committed height + 1 (owner, 2026-10-31, code notes C13 and C74) |
| `GET /hdls/checkpoint` | the new `Checkpoint` |
| `GET /hdls/accounts` | the new `Account` |
| `GET /hdls/resolve?name=` / `?key=` | adds `bank`, `frozen`, `pendingKey`, `pendingBank`, `pendingHeight`; `height` is the committed height (E40); `stateDigest` is hex of the committed state's `LedgerState.Digest()`, the value a pin takes, never the block hash the node now decides on (E38) |
| `GET /hdls/governance` | the ten parameters; validators by name; pooled name actions with their new fields |
| `GET /hdls/record`, `/hdls/lifelong-record` | **new** (§6.8) |

## 7. Wallet and admin UI

### 7.1 Key generation and key file

- A new wallet draws a 32-byte seed from `crypto/rand`. The key file holds the seed, as
  `privateKey` (plaintext) or sealed in `encryptedPrivateKey` (PBKDF2 and AES-GCM over the seed, as
  today), and `publicKey`, the derived 1952-byte key, for display.
- On load the wallet derives the key from the seed and refuses a file whose `publicKey` differs
  (E26). A file whose private key is not 32 bytes is an earlier network's: the wallet says so and
  asks for a new wallet.

### 7.2 Identity and pin

- The wallet's name is the account in its verified state whose key equals the wallet's key. An
  account whose `PendingKey` equals it is shown as "becomes yours at block N". With neither, the
  page says "not registered", shows the key's fingerprint (the first 16 hex characters of its
  SHA-256) and a button that copies the full key. With no verified state, a page says "No verified
  state" instead, and claims nothing about the name (wrap-up, 2026-10-31).
- `<keyfile>.state` also keeps the last name the wallet verified as its own and the height of the
  newest state it verified. The wallet refuses a state older than that height, so its node cannot
  roll it back to before a pending change, nor take its payment counter back (§7.3). When no state
  verifies, the freeze button still signs a freeze for that last name.
- The pin, `<keyfile>.trust`:

  ```json
  {"networkId": "village/3f9a…",
   "validators": [{"Name": "bank-a", "PublicKey": "<2604>", "Power": 1}]}
  ```

  At first use, at the bank, the wallet takes `networkId` from `/hdls/status`, refuses it unless it
  starts with the `-network` flag followed by `/`, checks that the first checkpoint's `Validators`
  hash to its header's `ValidatorsHash`, verifies the checkpoint against them, and pins both. Later
  it believes a state only when pinned names holding more than ⅔ of the pinned power signed it,
  each under its pinned key. The pin does not update itself: when banks re-key or change, the bank
  hands over a new list, as Phase 1's docs say. Until then, if banks holding a third or more of the
  pinned power are frozen or re-keyed, no state verifies (Q17).
- The wallet signs with the pinned network id.
- It reads the checkpoint once per page, not once per field. Its accounts table shows each name's
  bank of record, frozen mark and balance, and a key fingerprint on demand, never full keys (2.6 KB
  each).

### 7.3 Flows, end to end

Block numbers:

- Payments and updates take their block from the wallet's counter, as today: `height + lookahead`,
  and above every block the wallet named before. Their height is the height in progress of the
  verified state the wallet checks them against (`State.Height + 1`), never the node's unverified
  `/hdls/status` (wrap-up, 2026-10-31). The wallet refuses a state older than the newest it verified
  (§7.2), so no node can make a restarted wallet drop its counter and name a block it used before;
  a node that lies can only make a payment miss its block.
- A freeze takes no counter value. It takes its height from the verified state, as a payment does,
  so a node cannot make it name a block no verified state reaches and hide that it did not land
  (wrap-up, 2026-10-31); only when no state verifies does it fall back on the node's
  `/hdls/status`, so it still works then (§7.2). It names `height + lookahead`, or the name's
  `PendingHeight` if that is sooner: freezes apply before due changes (§5.3 step I), so the freeze
  still cancels the change. It wins its block (R6), so a payment of the wallet's own for that block
  simply lapses.
- A name request takes no counter value either. Draft asks how long the signers need and names
  `height + lead`, with the `/hdls/status` height: 120 blocks by default, up to 3,600
  (`node.MaxRequestLookahead`, §4.3) for a sale or a remote signer. A wallet that signs a request
  as its requestor or holder keeps that block in `<keyfile>.state`, and its counter skips it, since
  the payer's payments lapse in that block (§5.5). A request that misses its block is drafted and
  signed again.

- **Registration, the "double signature setup".** The customer starts the wallet at the bank; it
  pins the bank's node and shows "not registered". On the new **Requests** page it drafts a
  registration: the name (or the hash name, §7.7), `NewKey` = its own key, the requestor (often the
  bank), `Bank` = the requestor's bank of record or the requestor itself when it is a bank, the fee
  and the block, all filled from its verified state. Its Sign step then signs `NewKeySignature`,
  and it shows the request as text. The bank's desk signs `BankSignature`, and `RequestorSignature`
  when the bank pays. A third-party requestor signs in its own wallet. Whoever holds the complete
  request submits it. The name applies in its block, and the customer's wallet then shows it.
- **Key change, or a sale.** The new wallet shows its key. The holder's wallet drafts a key change
  to it (the bank of record and the fee filled in) and signs. The new wallet signs the proof of
  possession, the bank's desk co-signs, and someone submits. Both wallets show the pending change
  until it applies; then the new wallet owns the name.
- **Bank change.** The holder drafts it with the new bank and signs; the new bank's desk co-signs;
  it is submitted, and the change is pending for the delay.
- **Freeze.** One button on the main page. After a confirm step the wallet signs a freeze for its
  own name and submits it. The page shows the freeze as sent until a verified state shows the name
  frozen. If its block passes first, the page says the freeze did not land and to contact the bank,
  or another bank.
- **Payment.** By name only; the wallet refuses anything that is not a canonical name. Before it
  signs, it shows the payee's name and bank of record from its verified state.
- **Delegation.** The account form takes a delegate name, a description and an address, each with
  "-" for none.
- **Recovery** stays institutional: freeze, go to the bank, and the ⅔ act. The victim's new wallet
  signs the name action's proof of possession (§7.6).

The Requests page has three steps, like the governance votes: **Draft** shows the facts and the
request text, and signs nothing. **Sign** takes request text, the drafter's own included, checks it
against the verified state, shows the facts, and signs every role this wallet holds: requestor
when its name is `Requestor`, bank when its name is `Bank`, new key when its key is `NewKey`.
**Submit** posts a complete request.

### 7.4 The bank's desk

In Phase 2 the desk is the Requests page of the bank operator's own wallet, which holds the bank's
one key, the key its node votes with. Requests travel between wallets as text, like governance
votes, and the node receives only the complete request. No node route signs anything. The page
reminds the operator to co-sign only after checking the holder in person or by a second channel,
and lists the pending changes on the bank's customers, so the bank can tell them. Phase 3 moves
the key into the signer, which serves the desk only `registration`, `name-request-bank` and
`freeze` (§4.0).

### 7.5 Warnings

- A pending change on the wallet's own name: "A key change (to key ab12…) or a bank change (to
  bank-b) on your name takes effect at block N. If you did not ask for it, freeze your name now",
  next to the freeze button.
- The wallet's own name is frozen: "Your name is frozen. Only a ⅔ vote of the banks can unfreeze
  it, with a new key. Contact your bank."
- The wallet's key is no longer its name's key: it shows "not registered", names the last name it
  held (from `<keyfile>.state`), and says that the name now has another key.

### 7.6 Admin page

- The parameter draft has all ten parameters.
- The name action draft takes a name, an optional new balance, an optional new key, an optional
  bank (or none, "-"), and freeze or unfreeze. It shows the name's current key fingerprint,
  balance, bank, frozen mark and pending change.
- A draft with a new key goes to the new key's wallet first, whose Sign step signs
  `NewKeySignature`. The validators then sign their votes, and the certificate is assembled and
  submitted as today.

### 7.7 Hash names

A wallet convention only; the protocol knows nothing of it. The hash name of a key is `k-`
followed by the first 15 bytes of SHA-256 of the raw key, in lowercase base32 without padding: 26
characters, a canonical name. The Requests page offers it for anonymous and prepaid wallets.
Anyone can register any name, a hash name included; the ⅔ action undoes a squat.

## 8. Commands and config

### 8.1 `keygen`

Prints a fresh ML-DSA-65 pair: `publicKey:` the base64 public key (2604 characters), then
`privateKey:` the base64 seed (44 characters). (This spec listed the seed first; the code prints the
public key first, and the spec follows it, 2026-10-31.)

### 8.2 Server config

| Field | Meaning |
|---|---|
| `genesisPath` | the genesis file (§2), the same on every node |
| `listen` | as today |
| `name` | this node's account name; "" for an unnamed observer |
| `publicKey`, `privateKey` | as `keygen` prints them; `load` derives the key from the seed, refuses a mismatch, and makes one test signature (E26) |
| `peers`, `bootstrap`, `snapshotPath`, `backupPath`, `maxMempool`, `maxCommentLength` | as today |

- Removed from the node config: `networkId`, `transactionCost`, `timeoutMs`, `minBlockIntervalMs`,
  `maxPeers`, `maxMessageBytes`, `validatorThreshold`, `validators` and `genesis`. They live in the
  genesis file. `load` refuses a key that matches no field, so a misspelt key, or one of these,
  never passes silently.
- `load` reads the genesis, validates it, computes the effective network id, and builds the signer
  with it.
- `snapshotPath` is required when `name` is a static validator, or in dynamic mode, as today.
- `load` computes no topology: it passes the configured `peers` on. `Open` computes the node's
  gossip neighbours from the validators' addresses in the state it opens with, and every commit and
  catch-up computes them again (`resolvePeersFromState`; code note C36, 2026-10-31). A validator
  takes them once every validator has an address, whatever `maxPeers` is: the neighbours of its
  topology with `maxPeers > 0`, all the other validators with `maxPeers` 0. So with every address in
  the genesis, a fresh validator gossips to the validators from the start, not from block 1. A node
  outside the set, or a network's only validator, keeps its `peers`. In dynamic mode the addresses
  are the on-chain ones (peer DNS), as today.
- The startup log prints the effective network id.
- `cmd/server` binds its `listen` address before `node.Open`, so a second start of one config on
  one machine stops on the busy address before it opens the node (wrap-up, 2026-10-31). That
  catches one mistake only. The sign log keeps only its own node from signing twice, so two nodes
  holding one validator key, on two machines or on two addresses, double-sign, which freezes the
  name (§5.8). The docs say to run each validator key on one node only.

### 8.3 Demo

- Genesis: `founder` (the only validator, power 1, no bank), `bakery` and `cafe` (bank `founder`).
  Parameters as §2.4, except `changeDelayBlocks: 20` and `retainBlocks: 1000`, so a key change is
  visible within a minute.
- The founder wallet holds the founder seed, so the demo user votes and runs the desk. The demo
  prints the command for a second wallet, which shows registration end to end. The founder is the
  only seated name, so its own freeze is refused with that reason (R9).
- Files stay in the private temp directory, as today.

## 9. Sizes and performance

Measured on this machine (Intel Xeon E5-2680 v4, 2.4 GHz, 2016; one core per benchmark; go1.27.3;
load average 2 to 3), in scratch copies.

### 9.1 Crypto

| Operation | ML-DSA-65 | Ed25519 (today) |
|---|---|---|
| sign (`SignDeterministic`, mean over 256 messages) | 0.84 – 0.94 ms | 0.033 ms |
| verify | 0.242 – 0.248 ms | 0.077 ms |
| parse a public key | 0.007 ms | — |
| key from a seed | 0.36 ms | — |
| generate a key | 0.36 – 0.41 ms | — |
| public key / signature | 1952 / 3309 bytes | 32 / 64 bytes |

Deriving the key from the seed costs 0.36 ms, about 40% of a signature. So the signer parses the
seed once: `signing.Scheme` gains `ParsePrivateKey(privateKey []byte) (PrivateKey, error)`, where
`PrivateKey` has `PublicKey() []byte` and `Sign(message []byte) ([]byte, error)`, and
`NewSigner(scheme, networkID, privateKey) (*Signer, error)` derives the public key itself. Builds
must not select the FIPS 140-3 v1.0.0 module, which lacks `crypto/mldsa`.

### 9.2 Messages, blocks, checkpoints (JSON)

| Item | Size |
|---|---|
| payment | 4511 (least), 4531 (typical), 4961 (280-byte comment, pin, longest fields), 6361 (the same, every comment byte escaped) |
| account update | 4576 at most without text, plus text |
| registration / key change / bank change / freeze | 16136 / 16134 / 9119 / 4645 at most |
| vote; vote evidence | 4674; 9410 |
| account in state; with a pending key | 2864; 5480 |
| validator entry (name, key, power) | 2684 |
| commit: 1 / 4 / 7 / 21 validators | 4.7 / 14.0 / 23.4 / 70.1 KB |
| 768 KiB block budget holds | 173 typical payments, 158 largest (123 escaped), 48 registrations or key changes, 86 bank changes |
| content hash of a full block | 3.3 ms |
| checkpoint | 2.9 KB per account plus 2.7 KB per validator plus the commit; 64 MiB ≈ 23,000 accounts |

### 9.3 Signature record

| Validators (quorum) | Entry | Entry with set | Per day at 1 block/s | `RetainBlocks` default (604,800) |
|---|---|---|---|---|
| 1 (1) | 9.5 KB | 12.2 KB | 0.82 GB | 5.7 GB |
| 4 (3) | 18.4 KB | 29.2 KB | 1.59 GB | 11.2 GB |
| 7 (5) | 27.4 KB | 46.2 KB | 2.37 GB | 16.6 GB |
| 21 (15) | 72.2 KB | 128.6 KB | 6.23 GB | 43.6 GB |

A chain nobody uses records nothing (§6.6). A pending change keeps the chain moving for
`ChangeDelayBlocks` blocks: a key change costs one day of record, 1.6 GB with 4 validators. Free
updates can keep it moving for good (Q5); the last column bounds what that costs. In dynamic mode
most busy blocks change the set (§6.8), so most entries carry it: 2.52 GB a day with 4 validators
and 11.1 GB with 21, or 17.7 GB and 77.8 GB over the default retention.

### 9.4 CPU per block

A full block of 173 payments costs 44 ms of signature checks per `ApplyBlock`. Every node builds a
candidate with `ProposeBlock` at every height, and it verifies only what it adds (§5.4): at most
another 44 ms. A node applies a block in `onProposal` and again at commit (88 ms, or 44 ms with the
reuse in §6.2). Intake verifies every gossiped copy of a payment off the loop, N − 1 copies with
all-to-all relay: about 0.13 s of CPU per full block with 4 banks and 0.85 s with 21, spread over
the cores. The node a wallet pays through signs one receipt per payment (0.9 ms). On the loop,
relayed copies of a vote are dropped before any check (§4.10), and each distinct vote is verified
twice: about 21 ms per round with 21 validators. A checkpoint check costs at most one signature
check per anchor name and one per signer (§4.14): about 10 ms with 21.

### 9.5 Test suite

The mechanical swap of `signing.Ed25519{}` for an ML-DSA-65 scheme that parses each seed once,
before any other Phase 2 change:

| Package | `go test` | `go test -race` |
|---|---|---|
| consensus | 1.9 → 10.2 s | 12.6 → 44.8 s |
| ledger, without its three size-bound tests | 0.07 → 0.55 s | 1.4 → 2.6 s |
| node | 31.0 → 36.0 s | 38.1 → 52.7 s |
| wall time: root module (`go test`), or consensus, ledger and node (`-race`) | 33.2 → 37.9 s | 41.9 → 56.7 s |

Parsing the seed at every signature instead doubles consensus: 23.7 s, and 62.0 s under `-race`.
Only tests that assume 32- or 64-byte keys failed, plus the size-bound tests (the old 2000-byte
`MaxMessageBytes`; one of them panicked, which is why the ledger row leaves them out). The node
tests are bound by timers, not crypto, and none turned flaky. `GOLEDGER_SIM_SEED_FACTOR` still
scales the simulator.

## 10. The slice plan

Every slice starts from the branch after the previous ones it depends on. It is done when, in the
root module and in `ui/`, `go build ./...`, `go vet ./...` and `go test -race ./...` pass (chaos
scenarios included). Each slice adds its tests, rewrites the old tests its change breaks, and
carries no code for old formats. File lists name the main files; a slice also edits the tests of
the packages it changes. Slices in one group never share a file, test files included.

### 10.1 Groups

| Group | Slices, in parallel within the group | Starts after |
|---|---|---|
| 1 | S1 | the rename |
| 2 | S2 | S1 |
| 3 | S3 | S2 |
| 4 | S4 | S3 |
| 5 | S5 · S7 · S8 · S11 (draft) | S4 |
| 6 | S6 · S9 | S5 |
| 7 | S10 | S6 (S9 may still run: they share no file) |
| 8 | S7b | S10 |
| 9 | S11 (final pass) | everything |
| 10 | S12 | S11 |

The critical path is S1 → S2 → S3 → S4 → S5 → S6 → S10. S3 is the largest, S5 the richest in
rules. S10 is the last code slice: once a chain can idle, the many node tests that wait for empty
blocks, S6's and S7's included, need work to wait on.

### 10.2 Slices

**S1 — ML-DSA-65 is the only scheme.**
- Scope: new `signing/mldsa65.go` (seed private keys, `ParsePrivateKey`, `SignDeterministic`,
  exact-size `Verify`); `NewSigner` derives the public key; delete `signing/ed25519.go` and the
  comment slot `signing/mldsa.go`; drop the `validator-message` and `server-auth` domains; switch
  every call site; `cmd/server` checks sizes and derives the key from the seed; `keygen` prints a
  seed; the wallet key file holds a seed; `looksLikePublicKey` checks 1952 bytes. Payments and
  updates carry keys until S3 (a plain key-to-key payment is 9743 bytes), so S1 sets
  `MinMessageBytes = 10000` and a default `maxMessageBytes` of 12000; S3 sets the final values.
- Files: `signing/*`, `cmd/keygen/main.go`, `cmd/server/config.go`, `cmd/demo/main.go`,
  `ui/keyfile.go`, `ui/wallet.go`, `ui/trust.go`, `ui/admin.go`, `ledger/parameters.go`, and every
  test file with an `Ed25519` literal. Broken-signer tests use a test scheme whose key's `Sign`
  fails.
- Depends on: the rename. Parallel with: none, since it touches nearly every test file.
- Tests that fail before and pass after: `TestSignaturesAreDeterministic`,
  `TestVerifyRefusesWrongKeyAndSignatureSizes`, `TestPrivateKeyIsA32ByteSeed`,
  `TestSignerDerivesItsPublicKey` (signing); `TestLoadRefusesAPublicKeyThatIsNotTheSeeds`
  (cmd/server); `TestKeyFileHoldsTheSeed` (ui). The size-bound tests (`ledger/messagesize_test.go`,
  `node/admission_test.go`) take the interim values.
- Size: about 250 changed lines, 120 of them production, plus a mechanical edit of about 45 test
  files.

**S2 — Height, amounts, parameters and caps.**
- Scope: `LedgerState.Height` in the digest, and `ApplyBlock` requires the previous height plus one
  (E1); plain-decimal amounts in JSON and in every signed field, with the §3.3 parser,
  value-receiver writers, and no `null` for a required amount (E6, E7); the five new parameters
  in `fields()`, `Digest` and `Acceptable`, with §3.4's floors and ceilings, and `MaxPeers` and
  `MaxMessageBytes` as int64; `ledger.DefaultParameters()`, which every fixture starts from; the
  caps at intake, in `ProposeBlock` and in `ApplyBlock`, rule 23 against the pre-block cap; the
  §3.5 tags for the parameter and state digests; the node takes its height from state;
  `Config.Genesis` becomes a `LedgerState`; the governance view and the admin draft carry the new
  parameters; `cmd/server` reads them (from the node config, until S8). `ApplyBlock` and
  `ProposeBlock` keep today's signatures until S3.
- Files: `ledger/amount.go`, `ledger/parameters.go`, `ledger/block.go`, `ledger/ledger.go`,
  `ledger/payment.go`, `ledger/reassignment.go`, `ledger/governance.go`, `node/node.go`,
  `node/governance.go`, `node/persist.go`, `ui/admin.go`, `ui/templates/admin.html`,
  `cmd/server/config.go`, `cmd/demo/main.go`.
- Depends on: S1. Parallel with: none.
- Tests: `TestApplyBlockRequiresTheNextHeight`, `TestStateDigestCoversTheHeight`,
  `TestStateDigestLayout` (golden), `TestAmountsTravelAsPlainDecimals` (a value, a pointer and a
  nested field), `TestAmountParserRefusesFractionsExponentsAndOverlongDigits`,
  `TestRequiredAmountRefusesNull`, `TestEveryParameterIsInTheDigest`,
  `TestAcceptableEnforcesTheFloorsAndCeilings` (the largest delay and retention cannot overflow),
  `TestPaymentAboveTheLargestPaymentIsRefused` (intake, proposer, apply),
  `TestPaymentLiftingAPayeeAboveTheBalanceCapIsRefused`, `TestLoweredCapAppliesFromTheNextBlock`
  (a cap-lowering change and a payment that fits only the old cap, in one block),
  `TestNodeHeightComesFromState`.
- Size: about 350 production and 350 test lines.

**S3 — Names are the identity.**
- Scope: the final `Account` (§3.1) and the accounts digest (§3.5), in slice order, with a decoded
  state refused unless its names are strictly ascending; `ledger.CanonicalName` (§3.2); payments
  by name, pay-to-key deleted; `AccountUpdate` by name with delegation to a name and "-" to clear a
  field, name claim deleted, and `/hdls/update` answering `409` with the reason; consensus
  `Validator`, `ValidatorSet`, `Vote`, `Commit` and `VoteEvidence` by name, with keys from the
  set; `ledger.Chain`, `ValidatorRule` and `SetFor` (moved from `node/validators.go`), honouring
  `Frozen` and R10; the final `ApplyBlock` and `ProposeBlock` signatures of §5.3 and §5.4, error
  return included, with a zero previous block hash and nonce until S4, so their call sites change
  once; receipts by node name, and none for a repeat (§4.1); `Config.Name` and `Config.Chain`; the
  committed view (§6.5), with handlers checking against it; the anchor and checkpoint check by
  (name, key, power), with the signing set carried in both modes; `/hdls/resolve` adds `bank` and
  `frozen`; the frozen-name checks of §4.1 and §4.2 (the field exists now; S5 adds the ways to set
  it); `MinMessageBytes = 5000` and the default `maxMessageBytes` of 6400; the wallet finds its
  name by key, pays by name, delegates by name, and pins (name, key, power); topology and peer DNS
  by name; `cmd/server` genesis entries and validators by name; the demo by name. Until S6,
  evidence still zeroes a balance, now by name.
- Files: nearly all production files, and most tests. A shared test helper per package makes named
  accounts and signers. Suggested order: consensus, ledger, node, cmd, ui, then the tests package by
  package; the compiler lists the call sites.
- Depends on: S2. Parallel with: none.
- Tests: `TestCanonicalNameRefusesOtherSpellings`, `TestPaymentIsSignedByThePayersCurrentKey`,
  `TestPaymentToAnUnknownNameIsRefused`, `TestPaymentFromOrToAFrozenNameIsRefused`,
  `TestUpdateDelegatesToAName`, `TestUpdateClearsAFieldWithADash`,
  `TestDelegationToAnUnknownOrFrozenNameIsRefused`, `TestUpdateRefusalAnswers409`,
  `TestAccountsDigestLayout` (golden), `TestStateOutOfNameOrderIsRefused`,
  `TestStaticSetTakesKeysFromState`, `TestDynamicSetFollowsDelegationToNames`,
  `TestFrozenNameIsNotSeated`, `TestFrozenAccountBacksNoDelegate`,
  `TestVoteCountsOnlyUnderTheSetKey`, `TestCheckpointGivingRealNamesForeignKeysIsRefused`,
  `TestRepeatPaymentGetsNoNewReceipt`, `TestMinimumMessageBoundFitsAPayment`,
  `TestWalletFindsItsNameByKey`, `TestWalletPinHoldsNameKeyAndPower`, and
  `TestNodesReachConsensusAndApplyPayment` (adapted).
- Size: about 700 production and 1,300 test lines.

**S4 — Block hash, nonce, signed proposals, network id.**
- Scope: the §5.1 block, content, header and hashes, `ValidatorsHash` included, with rule 3's
  check; votes and commits over the block hash; every block lookup and comparison by block hash,
  not state digest (§5.1's list); `SignedProposal`, `ProposalMessage`, and the §4.11 intake order
  (E15); the `ProposalEvidence` type and block field, with `ApplyBlock` refusing any until S6; the
  node's nonce, last block hash and header; the new `Checkpoint` and its four-step check (§4.14),
  in the node and at the wallet's first use; the snapshot holds only the last header; the empty
  sign log after a durable snapshot (§6.9); the E38 pin against the full digest, with the intake
  rule for the next block only, and `/hdls/resolve` serving that digest; `GenesisHash` and the
  effective network id in `ledger/genesis.go`, used by `cmd/server` and the demo, served by
  `/hdls/status` and pinned by the wallet (E20); the record hooks with stub bodies (§6.8).
- Files: `ledger/block.go`, `ledger/hash.go` (new), `ledger/genesis.go` (new),
  `consensus/vote.go`, `consensus/commit.go`, `consensus/proposal.go` (new),
  `consensus/evidence.go`, `consensus/driver.go`, `node/messages.go`, `node/node.go`,
  `node/persist.go`, `node/sync.go`, `node/httptransport.go`, `node/relay.go`, `node/record.go`
  (stubs), `ui/trust.go`, `ui/wallet.go`, `cmd/server/config.go`, `cmd/demo/main.go`.
- Depends on: S3. Parallel with: none.
- Tests: `TestBlockHashLayout` (golden), `TestBlockHashCoversEveryPart` (height, previous hash,
  signing set, nonce, state digest and each content field each change it),
  `TestApplyBlockRequiresThePreviousBlockHash`, `TestApplyBlockRequiresTheSigningSetHash`,
  `TestSwappedBodyIsNotAuthenticAndTheGenuineCopyStillCounts`,
  `TestCheckpointRecomputesTheBlockHash`, `TestPaddedCheckpointIsRefusedBeforeAnyPadIsVerified`
  (counts signature checks), `TestNonceOnlyDifferenceIsReportedAsEquivocation`,
  `TestProposalIsSentOnlyOnceLogged` (a new case: two empty blocks with different nonces for one
  round, and only the first is signed), `TestPinUsesTheFullStateDigest`,
  `TestResolveServesTheStateDigest`, `TestIntakeRefusesAPinForALaterBlock`,
  `TestNetworkIdBindsTheGenesis` (golden, over `LedgerState.Digest()`),
  `TestWalletPinsTheNetworkIdOnFirstUse`, `TestSnapshotKeepsOnlyTheLastNonce`,
  `TestSignLogIsEmptiedAfterTheSnapshot`.
- Size: about 420 production and 450 test lines.

**S5 — Name requests and the ⅔ name action.**
- Scope: `NameRequest` with its four kinds and their domains (§4.0, §4.3 to §4.7), and
  `MaxRequestLookahead`; intake's cheap checks, the fee payer's balance included, before any
  signature; the shared fee rule and the same-block refusal between a payer's payments and its
  requests (§5.5); pending changes, due changes judged in order, and lapses (§5.7); the freeze
  step, its clearing of co-signed changes (R11) and the locked names (R6); `Reassignment` becomes
  the name action (§4.8), with its proof-of-possession domain, name creation, "-" for no bank, and
  R7; a relayed certificate copy dropped before `Valid`; `ApplyBlock` steps C, D without evidence,
  E to G, I and J; `ProposeBlock` §5.4 without evidence, with signatures checked last, the
  action's `NewKey` among the used keys, and rule 24 by item (R9), and with intake and the pools
  refusing a freeze or action that alone would leave no name seated; the request pool,
  `/hdls/name-request`, `MessageNameRequest`, pending fees and `reconcilePools`; the pending fields
  of `/hdls/resolve`; the governance view; UI compile fixes; the idle hooks with stub bodies (§6.6).
- Files: `ledger/name_request.go` (new), `ledger/block.go`, `ledger/reassignment.go`,
  `ledger/ledger.go`, `ledger/account_update.go`, `signing/domain.go`, `node/requestpool.go` (new),
  `node/idle.go` (new, stubs), `node/node.go`, `node/mempool.go`, `node/messages.go`,
  `node/relay.go`, `node/httptransport.go`, `node/reassignment.go`, `node/governance.go`,
  `ui/admin.go`, `ui/main.go`.
- Depends on: S4. Parallel with: S7, S8, S11.
- Tests: `TestRegistrationNeedsAllThreeSignatures`, `TestRequestorSignsInTheDomainOfItsKind`,
  `TestRegisteredNameCannotBeRegisteredAgain`,
  `TestRegistrationChargesTheRequestorAndSetsTheBankOfRecord`, `TestSeatedRequestorMayBeItsOwnBank`,
  `TestKeyChangeWaitsChangeDelayBlocks`, `TestKeyChangeNeedsTheBankOfRecord`,
  `TestBankChangeNeedsOnlyTheNewSeatedBank`, `TestSeatedNameCannotFileAChange`,
  `TestPendingChangeLapsesWhenItsNameIsSeatedAtMaturity`,
  `TestPendingKeyChangeLapsesWhenTheKeyIsTakenMeanwhile`,
  `TestTwoDueChangesToOneKeyApplyOnlyTheFirst`,
  `TestFreezeIsFreeImmediateAndCancelsThePendingChange`,
  `TestFreezingABankClearsTheChangesItCoSigned`,
  `TestFrozenNameNeitherPaysNorIsPaidAfterItsFreezeLands`, `TestFreezeWinsOverASameBlockPayment`,
  `TestOneRequestPerNameAndPerNewKeyPerBlock`,
  `TestProposeBlockSkipsARequestSharingTheActionsNewKey`,
  `TestFeePayerSendsNoPaymentInTheSameBlock`,
  `TestIntakeRefusesAPaymentAndARequestOfOnePayerForOneBlock`, `TestFeesAddUpToAtMostTheBalance`,
  `TestNameActionRekeysAndUnfreezes`, `TestUnfreezeRequiresANewKey`,
  `TestNameActionCreatesANewName`, `TestNameActionCanClearTheBank`,
  `TestNameActionWinsOverASameBlockRequest`,
  `TestNameActionClearsThePendingChange`, `TestInBlockOrderIsFreezesThenActionThenDueChanges`,
  `TestNameActionSurvivesAnEarlierKeyChange`, `TestNoBlockLeavesTheSetEmpty` (`ProposeBlock` takes
  the item out; intake refuses the last bank's freeze and such an action),
  `TestProposeBlockVerifiesOnlyWhatItCarries` (counts signature checks with a full pool for later
  blocks), `TestRequestIntakeRunsCheapChecksBeforeSignatures` (counts signature checks on refused
  requests, an unfunded requestor's included: zero), `TestRequestWindowIsMaxRequestLookahead`,
  `TestRelayedCertificateCopyCostsNoSignatureCheck`, `TestPoolCountsPendingFees`,
  `TestRegistrationEndToEnd` (four nodes: register, then pay the new name).
- Size: about 700 production and 850 test lines.

**S6 — A double-sign freezes the name.**
- Scope: vote and proposal evidence under the name's current key, with no age limit
  (`maxEvidenceAge` deleted) and below the block's height; one evidence item per name; evidence
  joins the freeze step and leaves the balance alone (`applySlashes` deleted, R5); the one-third
  cap with `powerIgnoringFreezes`; proposal evidence from `onProposal`; `pendingProofs` (§6.4),
  saved in the snapshot file; a driver with no signer for a node that is not seated with its key
  (§6.3); late double-signs by name.
- Files: `consensus/evidence.go`, `consensus/voteset.go`, `consensus/driver.go`, `ledger/block.go`,
  `ledger/validators.go`, `node/node.go`, `node/persist.go`.
- Depends on: S5. Parallel with: S9.
- Tests: `TestDoubleSignFreezesTheNameAndKeepsTheBalance`, `TestEvidenceUnderAReplacedKeyIsMoot`,
  `TestEvidenceHasNoAgeLimit`, `TestProposalEvidenceFreezes`,
  `TestNonceOnlyDifferenceIsProposalEvidence`, `TestADifferentValidRoundIsNotEvidence`,
  `TestEvidenceNeedsNoSeat`, `TestOneThirdCapHoldsEvidenceBack`,
  `TestFrozenValidatorLeavesTheSetWithItsDelegatedPower`, `TestProofOutlivesLeavingTheSet` (the
  probe from the name freeze check: a double-sign and a re-delegation in one block),
  `TestProofIsDroppedWhenTheKeyChanges`, `TestPendingProofSurvivesARestart`,
  `TestDriverWithoutSignerNeverSigns`, `TestNodeWithAReplacedKeyStopsSigning`.
- Size: about 280 production and 450 test lines.

**S7 — The signature record.**
- Scope: the bodies of `node/record.go`: rolling segments, the lifelong file, `KeepUntilHeight`,
  pruning by the larger of the kept and the current retention, and the two routes (§6.8).
- Files: `node/record.go`, `node/record_test.go`, which also holds any helper its tests need.
- Depends on: S4 (the hooks and the block hash). Parallel with: S5, S8, S11.
- Tests: `TestRecordKeepsCertificatesAndNeverPayments`,
  `TestRecordNeverHoldsTheBareStateDigest` (searches the files for every block's state digest),
  `TestRecordChainChecksFromItsOwnFields` (each block hash recomputed with its set's
  `ValidatorsHash`), `TestRecordPrunesWholeSegmentsAfterKeepUntilHeight`,
  `TestRetentionCutAppliesOnlyToNewRecords`, `TestLongerRetentionKeepsOldSegments`,
  `TestLifelongRecordKeepsGovernanceAndEvidence`, `TestRecordRoutesServeAtMostAHundredEntries`,
  `TestPartialLastLineIsSkipped`.
- Size: about 210 production and 270 test lines.

**S8 — Genesis file, server config, keygen, demo.**
- Scope: the §2 file format and `ledger.ValidateGenesis`; the §8.2 node config; topology from
  genesis addresses; keygen; the §8.3 demo.
- Files: `ledger/genesis.go`, `cmd/server/config.go`, `cmd/server/main.go`, `cmd/keygen/main.go`,
  `cmd/demo/main.go`. Its ledger tests go in a new `ledger/genesis_test.go` only, since S5 edits
  the other ledger tests at the same time.
- Depends on: S4. Parallel with: S5, S7, S11.
- Tests: `TestLoadReadsTheGenesisFile`, `TestGenesisRefusesNonCanonicalOrDuplicateNames`,
  `TestGenesisRefusesDuplicateKeys`, `TestGenesisRefusesABankThatIsNotSeated`,
  `TestGenesisRefusesAnOwnBank`, `TestNetworkIdIsTheNamePlusTheGenesisHash`,
  `TestKeygenOutputLoads`, `TestDemoGenesisIsValid`.
- Size: about 250 production and 250 test lines.

**S9 — Wallet and admin UI.**
- Scope: §7 in full: the name status, a pending key included; the last verified name and height in
  `<keyfile>.state`, and refusing an older state; the warnings; the freeze button, with its block
  rule, its landing check and its fallback when no state verifies; the Requests page (draft with a
  lead, sign the roles held, submit), whose blocks leave the payment counter alone; the desk, with
  its list of pending changes; hash names; payments that show the payee's bank; the delegation form
  by name; the accounts table without full keys; the admin parameter and name-action drafts; one
  checkpoint read per page.
- Files: `ui/*.go`, `ui/templates/*.html`.
- Depends on: S5 (and S4 for the pin). Parallel with: S6, and S10 once S6 is done.
- Tests: `TestUnregisteredWalletShowsItsKeyFingerprint`, `TestPendingKeyShowsBecomesYours`,
  `TestRegistrationCollectsThreeSignatures`, `TestSignStepSignsOnlyTheRolesThisWalletHolds`,
  `TestRequestsLeaveThePaymentCounterAlone`, `TestPendingChangeOnOwnNameShowsAWarning`,
  `TestFrozenNameShowsAWarning`, `TestFreezeButtonSubmitsAFreeze`,
  `TestFreezeNamesTheBlockBeforeThePendingChange`, `TestFreezeThatDidNotLandIsReported`,
  `TestFreezeWorksWhenNoStateVerifies`, `TestWalletRefusesAnOlderState`,
  `TestPaymentShowsThePayeesBank`, `TestHashNameConvention`,
  `TestNameActionDraftCollectsTheNewKeyProof`, `TestWalletRefusesNonCanonicalNames`.
- Size: about 550 production and 450 test lines.

**S10 — Idle chain, cheaper catch-up and vote intake.**
- Scope: the bodies of `hasWork` and `wakeIfIdle` (§6.6), block 1 included; catch-up asks
  `/hdls/status` first; vote intake drops relayed copies before any check (§4.10); a shared test
  helper that keeps a test network busy without moving money (a `Description` update for a later
  block), used by every node test that waits for blocks with no work.
- Files: `node/idle.go`, `node/sync.go`, `node/node.go` (vote intake only), `node/idle_test.go`
  (new), and the node tests that wait for empty blocks: at least `http_test.go`,
  `governance_test.go`, `reassignment_test.go`, `replay_test.go`, `backup_unix_test.go`,
  `peerdns_test.go`, `chaos_test.go` and `record_test.go`, and any S5 or S6 node test that does.
- Depends on: S6, which rewrites some of the same tests. Parallel with: S9.
- Tests: `TestFreshNetworkCommitsBlockOneThenIdles` (its checkpoint then carries a commit for a new
  wallet to pin), `TestIdleChainCommitsNothing` (from block 1 on), `TestPaymentWakesAnIdleChain`,
  `TestPeerVoteWakesAnIdleNode`, `TestPendingChangeKeepsTheChainMoving`,
  `TestFutureBlockIdKeepsTheChainMoving`, `TestRestartMidHeightResumesWhileIdle`,
  `TestNodeBackWithoutItsSignLogSignsNothingBeforeTheRegossip` (a peer's countable vote arrives
  first), `TestPaymentInThePostCommitWindowDoesNotStartTheHeightEarly`,
  `TestRelayedVoteCopyCostsNoSignatureCheck`, `TestCatchUpAsksStatusBeforeTheCheckpoint`.
- Size: about 120 production and 450 test lines.

**S7b — The record in units of 100, named by network (owner, 2026-10-30).**
- Scope: §6.8 as amended on 2026-10-30. The record files carry the genesis hash's first 16 hex
  digits in their names. The rolling record is written in aligned units of 100 heights, each
  unit file starting with its signing set as every segment did. The routes take `from` only: the
  rolling one answers heights H to H + 99, or the newest unit with no `from`; the lifelong one
  answers 100 entries from H, or the newest 100. Retention keeps its meaning, a unit file at a
  time.
- Files: `node/record.go` and `node/record_test.go`, plus `node/httptransport.go` only if the
  route registration must change.
- Depends on: S10, which rewrites `node/record_test.go`. It runs alone.
- Tests: `TestRecordFilesAreNamedByNetwork` (a node of another genesis on the same snapshot path
  neither reads nor extends the old files), `TestRecordUnitsHoldAHundredAlignedHeights`,
  `TestRecordRouteServesAHundredHeights` (`from=H` answers H to H + 99 only, and opens at most
  two unit files), `TestRecordRouteWithoutFromServesTheNewestUnit`,
  `TestLifelongRouteWithoutFromServesTheNewest`.
- Size: about 40 production lines changed; the route code should shrink.

**S11 — Docs.**
- Scope: §11, in the four areas §11 opens with. Draft from this spec in group 5; finish after S10
  and S7b with an accuracy pass against the code; mark the Phase 2 items fixed in
  `findings_20261027.md`.
- Files: `docs/*.md`, `README.md`, `TODO.md`, `ui/README.md`, `ui/USER_MANUAL.md`,
  `findings_20261027.md`.
- Depends on: S4 (draft), everything (final). Parallel with: every code slice.
- Tests: none; the final pass checks each claim against the code.
- Size: about 450 changed lines.

**S12 — A guide to reading the code (owner, 2026-10-30).**
- Scope: a new `docs/reading-the-code.md`, for a reader who has never seen the code. It takes them
  through all of it one step at a time: about 10,000 production lines and 440 template lines,
  with the tests (another 15,000) read alongside as each step's check. The owner asked for "a
  step by step guide on how to read the full codebase", made "to minimize dependencies and to
  compose the outline as you can read and validate one piece at a time and you put it all
  together in the end", where "each concept is mapped to the relative method and it is possible
  for the user to orient himself in the code".
- Order: the four areas of §11, which also follow the import order, so each step needs only
  the steps before it.
  - Signature: `signing` and `keygen`.
  - Node: the consensus types `ledger` imports (validator set, vote, commit, proposal, evidence,
    vote set); `ledger`, from amounts and names through accounts, the state and its digest,
    parameters and the genesis, payments, account updates, name requests, the ⅔ certificates and
    the validators, to `ApplyBlock` and `ProposeBlock` last, since they compose the rest; the
    consensus driver, which imports nothing from `ledger` but reads more easily once a block is
    known; the engine in `node` (messages, the pools, the event loop, the snapshot and the sign
    log, catch-up, governance, writing the record, the idle chain). The sign log is read here,
    where its code is today, and the guide points back to the signature area for it.
  - API/CDN: the HTTP routes, relay and peers, the record routes, `cmd/server` and `cmd/demo`.
  - UI: the wallet in `ui` (key file, trust pin, wallet, pages, requests, admin).
- Each step gives:
  - what the step is for;
  - its files in reading order;
  - a table from each concept to the type or function that holds it;
  - the facts it establishes that later steps take as given;
  - how to check it: the tests to read, and the `go test` command to run. Each command works
    without the later packages.
- A last step puts the pieces together. It follows one payment from the wallet's signature,
  through the pool, the proposal, the votes and the commit, to the wallet's verified state, then
  a freeze and a ⅔ name action. Each hop names the function it passes through.
- Code is named, never cited by line number, so the guide survives edits. The README's `Layout`
  stub becomes a short package map that points to the guide, and `docs/README.md` lists it.
- Files: `docs/reading-the-code.md` (new), `README.md` (`Layout`), `docs/README.md`.
- Depends on: S11, which rewrites `README.md` and the docs the guide links to.
- Tests: none in code. The check reads the guide as a newcomer would, in order:
  - every file, type and function it names exists and does what it says;
  - no step leans on a concept a later step introduces;
  - every check command passes;
  - the steps together cover every production file.
- It is also the last read-through before the push. The owner: "it will also be a good way to
  recheck that everything is tight before pushing". Writing the guide and checking it each read
  every production file, so both list what does not read cleanly: dead code, a name or comment
  that misleads, a rule enforced in two places, a step that needs a later one, anything hard to
  explain simply. The guide itself does not change the code. The list goes to the owner, who
  picks what is fixed before the push.
- Size: about 700 lines.

## 11. Docs to change

**Four areas (owner, 2026-10-30).** The docs are organized in four areas, one for each container
Phase 3 splits the system into: signature, node, API/CDN and UI. The owner: "i'd also say we can
make the docs into 4 macro areas which correspond to the 4 docker containers: signature, node,
api/cdn and ui". `docs/README.md` opens with the four and what each holds. Each doc belongs to one
area, or to a short "general" list for what spans them (`why-small-and-auditable.md`,
`managing-the-currency.md`, the threat model). Where a doc's content belongs to another area, it
moves there; S11 settles the files. A starting point:

- **Signature**, Phase 3's signer: domains and preimages, ML-DSA-65, keys, seeds and `keygen`, the
  sign log that keeps a node from signing twice, and what a double-sign costs (the freeze). Code:
  `signing/`, `cmd/keygen/`, and the sign log, in `node/persist.go` today.
- **Node**, the engine: the ledger's rules and the block, consensus (rounds, quorum, evidence and
  the freeze), governance certificates, the pools, the snapshot, catch-up, the idle chain, and
  writing the signature record. Code: `ledger/`, `consensus/`, and `node/` but its HTTP side.
- **API/CDN**, the proxy: the HTTP routes and what each costs, peers and relay, the record served
  a unit at a time, and the server's config. Code: `node/httptransport.go`, `node/relay.go`,
  `node/peerdns.go`, the record routes, `cmd/server/`.
- **UI**, the wallet: `ui/`, with `ui/README.md` and `ui/USER_MANUAL.md`.

The table below lists what each doc must say; S11 places it in its area.

| File | Change |
|---|---|
| `README.md` | ML-DSA-65 is the scheme (signing, cryptographic agility, keygen; the `Layout` section is S12's); the naming registry is registration at a bank, not an account update; pay by name only; slashing becomes the freeze (R5); plain-decimal amounts and caps; the fork-binding tier pins the full digest of the next block; the signature record; the idle chain |
| `docs/architecture.md` | `Account` and `Payment` by name; the block's fields and hashes; validator derivation in `ledger`; keygen prints an ML-DSA seed; the record files |
| `docs/consensus.md` | votes and proposals sign the block hash, with the nonce and the signing set's hash; equivocation freezes the name (no slash), proposal evidence, the one-third cap, signing only while seated, pending proofs saved in the snapshot; onboarding with (name, key, power) anchors and the checkpoint header and its check order |
| `docs/governance.md` | the five new parameters; the name action (`NewKey`, `Bank`, `Freeze`, `Unfreeze`; it binds the name and sequence; it clears a pending change; it wins its block); certificates bounded by field sizes; the record keeps certificates for life |
| `docs/security.md` | one ML-DSA implementation is a consensus rule; the freeze's scope as §5.8 states it, the largest-validator caveat and an uncapped self-freeze included; the network id bound to the genesis, and an identical re-genesis letting anyone freeze the banks with their old votes; the bank co-signature model and its limits (a freeze clears what the bank co-signed; a wallet sees one node's view); the record and the nonce, and in dynamic mode the stake per seat the record shows (§0.3); caps |
| `docs/operating-a-network.md` | keygen output; the genesis file and the node config; the effective network id, and a new `networkName` at every re-genesis; new and changed routes; the record files beside the snapshot; the idle chain, and freezing a name that keeps it busy for nothing; a bank with a leaked key freezes first and is re-keyed after; "historyless and portable" becomes "keeps a signature record, no payments"; the fork-binding tier, whose pin is `/hdls/resolve`'s `stateDigest` |
| `docs/managing-the-currency.md` | slashing and the supply formula (the freeze takes no money; the ⅔ decide); the levers table; the prepaid key becomes banks of record and hash names, and the "every account forced to carry a registered identity" smell is rewritten; "historyless" becomes the record; the freeze now exists ("Changes skip daylight"); caps as policy levers |
| `TODO.md` | ML-DSA done; the archive node relative to the record; Phase 3 |
| `ui/README.md` | the key file holds a seed; the pin's new shape; what `<keyfile>.state` keeps; names only; the Requests page, the desk and the freeze |
| `ui/USER_MANUAL.md` | onboarding: make your key, register it at your bank (the double signature setup); pay by name only, and check the payee's bank; the freeze button and what "did not land" means; the pending-change warning; key change and sale; bank change; hash names for anonymous wallets; privacy: your bank of record and any pending change on your name are public, and a hash name is a pseudonym whose bank is known; your wallet sees the network through one node |
| `findings_20261027.md` | mark the Phase 2 items fixed as their slices land |
| `docs/why-small-and-auditable.md` | no change: it is the owner's account of the review, in the owner's words |
| `docs/reading-the-code.md` | new, slice S12: the step-by-step guide to reading the code; the README's `Layout` points to it |

## 12. Open questions for the owner

Each row's default is a **builder default**: every slice was built with it, and it holds until the
owner decides. A row the owner has decided says so, with the date. The owner's decisions since the
build began:

- **2026-10-28** (Q3): `Acceptable` does not require `TransactionCost <= MaxPaymentAmount`. The owner:
  "that is a technical possibility that is mitigated by human realities, to happen all of the voters
  should do a dumb thing at the same time uncoordinated. i'd leave it out", and "that at most should
  be a line in the documentation saying how to (not) configure it". The docs say to keep the largest
  payment at or above the fee.
- **2026-10-30** (Q24): a freeze names a block at most 64 ahead, the payments' window, not the 3,600
  blocks of other name requests (`6ded0f8`, §4.3). The owner: "anyone that says 'this key is
  compromised' and can back that up with a signature should not be made to wait", and "let's make
  the signature a immediate thing with no flooding".
- **2026-10-31** (code notes): every note in `review/phase2_code_notes.md` is fixed. The owner's
  choices: the re-gossip, restart-wait and catch-up intervals follow a ⅔ vote of the timings (C21,
  §6.1); `/hdls/status` names its height `heightInProgress` (C74, §6.10); one helper,
  `consensus.CertificateTally`, holds the > ⅔ certificate rule (C34); the file moves come last
  (C40 to C43).
- **2026-10-31** (the open points of S10 and S11). The owner: "they seem edge cases someone that has
  spent time with the code can choose better than me … the choice is yours". The orchestrator's
  choices:
  - a late double-sign wakes an idle chain (§6.6): kept;
  - a proof the one-third cap holds back, kept while the chain idles, lives only in memory until
    the next commit: accepted as documented, since it needs frozen names near a third of the power,
    the case §5.8 sends to re-genesis;
  - the wallet's payment counter can stall after 33 refusals by the node on an idle chain: kept as
    documented. Giving a block back would break the rule the counter exists for, and since code
    note C2 the wallet checks the ledger's rules before it takes a block;
  - `make build` sets `CGO_ENABLED=0`, so each binary is static, as the README's tagline says;
  - `keygen` prints the public key first, and §8.1 follows the code;
  - Q21: the record keeps the validator sets;
  - §3.6 says the node keeps no block on disk; memory keeps the last decided proposal;
  - Phase 3 has four containers, one per docs area (signature, node, API/CDN, UI), as the owner
    said on 2026-10-30. `findings_20261027.md` keeps its dated three, with its note.
- **2026-10-31** (the wrap-up review; the owner left these choices to the orchestrator too):
  - the wallet's payment counter takes its height from the verified state, not from
    `/hdls/status`, so a node cannot make a restarted wallet name a used block again (§7.3);
  - `loadSignLog` empties a sign log left below the restart height, at its own height (§6.9);
  - `cmd/server` binds its address before it opens the node, and the docs say to run each
    validator key on one node only (§8.2);
  - a freeze also takes its height from the verified state when one verifies, so a node cannot
    hide that a freeze did not land (§7.3; found by the final hardening check).
- **2026-10-30** (Q4's record): the record files carry the genesis hash's first 16 hex digits in
  their names, and the docs say to delete an earlier network's files (§6.8, S7b).
- **2026-10-30** (Q4's record): the rolling record is kept in aligned units of 100 heights;
  `from=H` answers H to H + 99, and no `from` the newest unit, so a request reads at most two unit
  files (§6.8, S7b).
- **2026-10-30** (§11): the docs in four areas, one per Phase 3 container: signature, node, API/CDN
  and UI.
- **2026-10-30** (§10.2): slice S12, a guide to reading the code, which is also the last
  read-through before the push.

Still open: every builder default in the table but Q21 (Q3, Q4 and Q24 carry an owner decision
beside theirs), and the readings R1 to R11 of §0.3. The open points of S10 and S11 are decided
(above).

| # | Question | Builder default | Why |
|---|---|---|---|
| Q1 | Name character set and canonical form | 1 to 32 lowercase ASCII letters, digits and hyphens, starting and ending with a letter or digit; no normalisation in the protocol | one spelling per name (E34's lesson for keys); DNS-like and readable; the wallet lowercases input. Lookalikes remain (`rn` and `m`, `vv` and `w`, `1` and `l`, `0` and `o`), so the wallet shows the payee's bank before paying. Option: the ledger refuses a name whose skeleton matches an existing name's, about ten lines |
| Q2 | `NewKeyFee` default | `1` (ten payments at a cost of `0.1`) | a request is 16 KB, as much block space as 3.5 payments, and costs three signature checks per node |
| Q3 | Caps defaults | `maxPaymentAmount` 100,000; `maxAccountBalance` 10,000,000. Owner, 2026-10-28: no rule ties `maxPaymentAmount` to `transactionCost`; the docs say how not to configure it | roomy for a village currency (the demo holds 1,425 in all), but real numbers an operator must think about; both ⅔-amendable |
| Q4 | `RetainBlocks` default | 604,800 (a week at one block per second). Owner, 2026-10-30: the record's files are units of 100 heights named by network (§6.8); the default itself is still the builders' | 11 GB worst case with 4 banks, 44 GB with 21 (18 GB and 78 GB in dynamic mode, §9.3); a chain nobody uses adds nothing |
| Q5 | Idle-chain policy | start a height only when there is work (§6.6); updates stay free and count as work | a chain nobody uses then signs, re-gossips and records nothing (catch-up still asks the peers' status). Costs: a pending change keeps blocks coming for a day, 1.6 GB of record with 4 banks; and one name can keep the chain busy for free with updates, at the busy rate of §9.3, until the ⅔ freeze it. Alternatives: a fee on updates, or recording only blocks that carry a message |
| Q6 | `MaxMessageBytes` and the block budget | 6,400 bytes, floor 5,000; block content 768 KiB as today; at most 8 evidence items per block; `/hdls/proposal` takes 2 MiB | the largest payment is 6,361 bytes when every comment byte is escaped (4,961 otherwise); 768 KiB holds 173 payments; a 21-bank certificate is about 100 KB |
| Q7 | How the requestor reaches the bank's co-signature in Phase 2 | request text between wallets, within the lead of Q24; the desk is the Requests page of the bank operator's wallet, which holds the bank's one key (§7.4) | no signing route on the network-exposed node (the audit's C1 lesson); the same flow as governance votes |
| Q8 | R1: does `AccountUpdate` keep `Description` and `Address`? | keep both | `Address` is the peer DNS; nothing in the plan removes it |
| Q9 | R2: may genesis and ⅔-created names have no bank of record? | yes | a single-bank network's bank cannot be its own bank |
| Q10 | R3: may a seated requestor be its own co-signing bank? | yes | otherwise a genesis bank cannot pay for a customer's registration |
| Q11 | R4: must a registration's bank and a bank change's new bank be seated? | yes; a key change's bank needs no seat | otherwise any name could become a bank of record and co-sign for a thief |
| Q12 | R5: does a double-sign still take the balance? | no; the freeze replaces the slash, and the ⅔ decide about the money | an honest fault must not cost money; the ⅔ can claw back |
| Q13 | R6: does every freeze win its block against payments, updates and requests of that name? | yes | E5's rule, extended; a victim's freeze beats a thief's same-block payment |
| Q14 | R7: does the ⅔ action clear a pending change of its name? | yes, always | the ⅔ decision must not be overwritten a day later |
| Q15 | R8: a floor for `ChangeDelayBlocks` | 1; a cut affects only later requests | tests and the demo need small values; the ⅔ can set any delay they choose |
| Q16 | R9: must every block leave at least one name seated? | yes; the item that would empty the set is left out, never the whole block | otherwise a block can empty the set and halt the chain for good. Cost: the last seated bank has no emergency brake (the demo's founder cannot freeze itself) |
| Q17 | Should the wallet pin follow re-keyed banks by itself? | no; the bank hands over a new list | the plan's model; a self-updating pin is a light client, which is later work. Cost: once banks holding a third or more of the pinned power are frozen or re-keyed, no state verifies until the new list arrives; the freeze button still works |
| Q18 | Checkpoint size limit | keep 64 MiB (about 23,000 accounts) | a village network is far below; raise it with the network |
| Q19 | R10: does a frozen account's balance stop backing its delegate? | yes | otherwise a thief who re-delegated stolen balances keeps that power after the victims freeze, and with a third of it vetoes the ⅔ for good; the cap still counts it (§5.2) |
| Q20 | R11: does a freeze clear the pending changes the frozen name co-signed? | yes, when the freeze applies | a key change co-signed with a leaked bank key must not land a day later, even if the ⅔ re-key the bank within the day; honest customers file again, and the fee is not refunded |
| Q21 | Conflict (§0.3): in dynamic mode the record's validator sets show each seat's stake per height | keep the sets, as decided, and say so in `docs/security.md` | **Decided 2026-10-31: keep the sets.** Every balance is public at the time through `GET /hdls/accounts`, so the record adds the history of each seat's stake over the heights it keeps: what an observer who polled a node then has anyway, and what a later one can rebuild. The alternative, power = floor(stake / threshold), is one line in `SetFor` but weighs votes coarsely. Static mode is not affected; where the history matters, use it |
| Q22 | Parameter ceilings | `changeDelayBlocks` ≤ 2,592,000 (30 days), `retainBlocks` ≤ 315,360,000 (ten years), `maxMessageBytes` ≤ 1,048,576, `maxPeers` ≤ 1,000 | `h + value` stays inside int64, and a typo cannot leave every later change pending for good, as E19 did for the timings |
| Q23 | How does an update or the name action set a field to none? | "-" clears `Delegate`, `Description` and `Address`, and the action's `Bank` | "-" is never a name. Otherwise a delegation (E42's treasury), a published address (E11) or a bank of record could never be withdrawn |
| Q24 | Signing window for name requests | intake takes `BlockID` up to H + 3,600; the wallet's lead is 120 blocks by default. Owner, 2026-10-30: a freeze gets H + 64, the payments' window | every signature covers `BlockID`, and a sale has three signers on different machines. A long lead keeps a busy chain moving until that block (§6.6) |
| Q25 | Should the wallet read a second validator's node? | no; it refuses an older state, reports a freeze that did not land, and the manual says one node gives one view | a bank of record that colludes with a thief can hide a pending change from its customer. Reading another pinned validator's checkpoint at its on-chain address, about 30 lines, would catch that when the address is reachable |

## Appendix: Review record

Five reviewers checked the first draft of this spec. Each finding was checked against the code at
9b7965f and against the plan, and every one held. No fix overrides a binding decision; #22 shows a
conflict between two of them, flagged in §0.3. Where a fix needs the owner's choice, the default
is in §12. "Partly" marks a finding whose remaining part went to §12.

| # | Lens | Severity | Finding | Done |
|---|---|---|---|---|
| 1 | consensus | major | Pending proofs are lost on a restart | Fixed: saved in the snapshot file, reloaded and pruned (§6.4 item 5); S6 test |
| 2 | consensus | major | `wakeIfIdle` can cut the lost-log wait and the post-commit window short | Fixed: an `idle` field that only `startHeight` sets; `wakeIfIdle` acts only on it (§6.6); two S10 tests |
| 3 | consensus | minor | Free updates keep the chain from idling | Fixed: stated in §6.6, §9.3 and Q5; later-block entries still count as work; the ⅔ can freeze such a name |
| 4 | consensus | minor | An identical re-genesis lets anyone freeze the banks with old votes | Fixed: §2.3 and the docs require a new `networkName`; the optional `Open` check not taken (an operator rule, like E20) |
| 5 | consensus | minor | Uncapped self-freeze; largest-validator caveat missing | Fixed: §5.8 scope text and the `docs/security.md` row; no rule change |
| 6 | consensus | minor | Sign log and relay key still compare state digests | Fixed: §5.1 lists every site, which uses the block hash; §6.9; S4 tests |
| 7 | state | major | `ProposeBlock`'s new-key set leaves out the action's `NewKey` | Fixed: §5.4 step 5 starts the set with it; S5 test |
| 8 | state | major | Rule 23 does not say which cap | Fixed: the pre-block cap (rule 23, §5.6); S2 test |
| 9 | state | major | R9 empties the whole block, and the item returns at every height | Fixed: items come out last-added first; intake and the pools refuse a freeze or action that alone empties the set (§4.3, §4.8, §5.4, §6.5); Q16 states the lost brake; S5 test |
| 10 | state | major | `ChangeDelayBlocks` and `RetainBlocks` have no ceilings | Fixed: ceilings in §3.4 (Q22); S2 test |
| 11 | state | major | A bank's freeze keeps the key changes it co-signed | Fixed by R11: the freeze clears every pending change it co-signed, which also covers a bank the ⅔ unfreeze within the day (Q20); S5 test |
| 12 | state | minor | The digest sorts, so reordered accounts verify | Fixed: hashed in slice order; a decoded state out of name order is refused (§3.5, §4.14); S3 test |
| 13 | state | minor | `null` accepted for required amounts | Fixed: refused except for the three optional amounts; `FormatAmount(nil)` is ""; `Acceptable` refuses nil (§3.3, §3.4); S2 test |
| 14 | state | minor | `MaxPeers` and `MaxMessageBytes` are 32-bit on 386 | Fixed: int64 with ceilings (§3.4, Q22) |
| 15 | state | minor | `GenesisHash` names the accounts-only digest | Fixed: `LedgerState.Digest()` (§2.3); S4 golden test |
| 16 | state | minor | Two same-block due changes to one key | Fixed: each is judged after those before it (§5.3 I.6, §5.7); S5 test |
| 17 | state | minor | Sizes ignore JSON escaping | Fixed: re-measured (payment 6361, requests 16136 / 16134 / 9119 / 4645); default `maxMessageBytes` 6400 (§2.4, §4.1, §4.3, §9.2, Q6) |
| 18 | state | minor | A pointer-receiver `MarshalJSON` writes `a/b` for values | Fixed: value receivers (§3.3); S2 round-trip test |
| 19 | state | minor | "" means unchanged, so nothing can be withdrawn | Fixed: "-" clears `Delegate`, `Description`, `Address` and the action's `Bank` (§4.2, §4.8, Q23) |
| 20 | crypto | blocker | A padded checkpoint costs thousands of signature checks | Fixed: `ValidatorsHash` in the block hash, and the anchor checked before the full commit (§4.14, §5.1); S4 test counts the checks |
| 21 | crypto | major | `ProposeBlock` verifies the whole pool before filtering | Fixed: signatures last, only for entries added (§5.4); §9.4 corrected. Not taken: candidates only on proposers, since `hasWork` and a later round need the candidate |
| 22 | crypto | major | Dynamic-mode record leaks payments through validator sets | Flagged as a conflict between two decisions (§0.3, Q21); default keeps the sets and documents it; §9.3 corrected |
| 23 | crypto | minor | The record check trusts sets the record itself holds | Fixed by `ValidatorsHash`: one trusted block hash vouches for earlier entries (§6.8); lifelong `Signers` wording corrected |
| 24 | crypto | minor | Every relayed vote copy is verified | Fixed: copies dropped before any check (§4.10), certificates likewise (§4.8, §4.9); §9.4 corrected; S5 and S10 tests |
| 25 | crypto | minor | A resubmitted pooled payment gets a fresh receipt | Fixed: a repeat gets `202` and no new receipt (§4.1, §6.10); S3 test. Storing receipts not needed: the wallet already accepts none |
| 26 | crypto | minor | One `name-request` domain covers two roles | Fixed: the requestor signs in the domain named by `Kind` (§4.0, §4.3); S5 test |
| 27 | crypto | minor | The sign log keeps the last block while idle | Fixed: an empty log for h + 1 after a durable snapshot (§3.6, §6.9); S4 test |
| 28 | crypto | minor | Request intake checks the payer's balance after the signatures | Fixed: in step 2 (§4.3); S5 test |
| 29 | crypto | minor | The state digest no longer identifies a block | Fixed with #6 (§5.1) |
| 30 | identity | blocker | A fresh network never commits block 1 | Fixed: no committed block yet counts as work (§6.6); S10 test |
| 31 | identity | major | About a minute to collect a request's signatures | Fixed: intake window 3,600 blocks, wallet lead 120 by default (§4.3, §7.3, Q24) |
| 32 | identity | major | The warning and the freeze go through one node | Partly: the wallet refuses an older state and reports a freeze that did not land; the desk lists pending changes; the manual says one node is one view (§7.2 to §7.4, §11). A second node: Q25 |
| 33 | identity | major | Freezing a bank keeps the key changes it co-signed | Fixed by R11, as proposed (Q20) |
| 34 | identity | major | A frozen bank over a third of the pin stops every wallet | Fixed: last verified name kept; the freeze works without a verified state (§7.2); Q17 and the docs state the cost |
| 35 | identity | major | A frozen balance still backs its delegate | Fixed by R10 (§5.2, Q19); the cap is unchanged; S3 test |
| 36 | identity | minor | A drafted request blocks the counter; a late freeze misses | Fixed: requests and freezes take no counter value; a freeze names the sooner of `height + lookahead` and `PendingHeight`, and its landing is checked (§7.3); S9 tests |
| 37 | identity | minor | A payment and a request of one payer for one block | Fixed: `409` both ways (§4.1, §4.3, §5.5); S5 test |
| 38 | identity | minor | Lookalike names | Partly: the wallet shows the payee's bank before paying (§7.3); Q1 states the risk and the ledger option |
| 39 | identity | minor | Three wallet rules contradict or are missing | Fixed: Draft signs nothing and Sign signs the drafter's roles; a `PendingKey` shows "becomes yours"; the last name is kept (§7.2, §7.3, §7.5) |
| 40 | identity | minor | The page prints full keys; the manual misses public facts | Fixed: fingerprints on demand (§7.2); the manual row adds the bank of record, pending changes and hash-name pseudonyms (§11) |
| 41 | build | major | S1 cannot fit a key-carrying payment in 5000 bytes | Fixed: S1 uses 10000 / 12000; S3 sets 5000 / 6400 and owns the test |
| 42 | build | major | S10 breaks node tests that wait for empty blocks; group 6 shares files | Fixed: S10 runs after S6 (group 7), names those test files and adds a busy-network helper |
| 43 | build | major | A key change is not re-checked for its co-signing bank | Fixed by R11 (#11) |
| 44 | build | major | A fresh network deadlocks at block 1 | Fixed with #30 |
| 45 | build | minor | Request block numbers clash with the counter | Fixed with #36 |
| 46 | build | minor | `/hdls/resolve` would serve the block hash | Fixed: it serves `LedgerState.Digest()` (§6.10); S4 test; docs row |
| 47 | build | minor | The stubs are not pinned down | Fixed: `recordCommittedBlock`'s arguments and handlers, the `idle` field and every `wakeIfIdle` call are named (§6.6, §6.8) |
| 48 | build | minor | CPU sizing counts each copy once | Fixed: votes and certificates deduplicated before checks; payment copies stay verified off the loop, now costed (§9.4). A shared seen-set would add code to save off-loop CPU only |
| 49 | build | minor | Free updates defeat the idle chain | Fixed with #3 (Q5); updates stay work, or a later entry would never land |
| 50 | build | minor | No ceilings for the delay and the retention | Fixed with #10 |
| 51 | build | minor | R9 has no test and empties the block | Fixed with #9 (`TestNoBlockLeavesTheSetEmpty`, S5) |
| 52 | build | minor | A payment and a request of one payer for one block | Fixed with #37 |
| 53 | build | minor | §3.2, the update `409` and the resolve fields belong to no slice | Fixed: S3 owns them, with tests; S5 adds the pending fields, S4 the digest |
| 54 | build | minor | Unset parameters in fixtures | Fixed: `ledger.DefaultParameters()` in S2; code may assume `Acceptable` parameters (§3.4) |
| 55 | build | minor | `ApplyBlock` and `ProposeBlock` change signature three times | Fixed: the final signatures once, in S3 |
| 56 | build | minor | "A longer retention applies at once" was false | Fixed: deletion also waits for the current retention (§6.8); S7 test |
| 57 | build | minor | The desk claim contradicts R3 | Fixed with #26; R3 kept, since dropping it needs a funding account per bank in every genesis |
| 58 | build | minor | `MsgNameRequest` breaks the naming rule | Fixed: `MessageNameRequest`, like the renamed `MessagePayment` (§6.5) |
