# Governance — self-amending parameters

The network can change its own operating parameters — the transaction cost and the **timing profile**
(block interval, step timeout, gossip fan-out) — by validator vote, rather than them being fixed
config. This is implemented.

## The rule: ⅔, not ½+1

A parameter change is ratified by a **> ⅔ stake supermajority** of the *current* validator set — the
same quorum as block finality (`ValidatorSet.Quorum`), deliberately. Anything that changes shared
state gets the BFT supermajority, for the same reason finality does: quorum intersection means a
minority (≤ ⅓) can neither force a change nor ratify two conflicting ones. The (acceptable) cost is
that a ⅓ minority can *block* a change — which, for governance, is a feature.

## The change certificate

`ledger.ParameterChange` bundles the proof, the same shape as a `Commit` or equivocation `Evidence`:

    ParameterChange {
        NewParameters            LedgerParameters
        PreviousParametersDigest [32]byte          // the parameters this change amends
        Sequence                 int64             // the governance sequence it amends (single-use)
        Votes                    []ParameterVote    // each a validator's signature over the three above
    }

`ParameterChange.Valid(scheme, network, validators, currentParameters, currentSequence)` accepts it
only if every vote is a distinct **current** validator signing exactly these `(NewParameters,
PreviousParametersDigest, Sequence)` in the `parameter-vote` domain, their combined power meets the
> ⅔ quorum, it amends the current parameters (`PreviousParametersDigest == currentParameters.Digest()`),
and it amends the current governance **sequence** (`Sequence == currentSequence`).

The **replay guard** is the sequence (see [The governance sequence](#the-governance-sequence-single-use-certificates)
below). Binding to the parameters' *value* alone was not enough — a security review showed a change
could be re-applied after the parameters cycle back to a previous value (an A→B→A revert). The
monotonic sequence, bumped on every applied governance action, makes each certificate single-use, with
no target-height coordination needed.

## Application (in committed state)

Parameters live in `LedgerState.Parameters`, and the block digest covers them
(`LedgerState.Digest = hash(accountsDigest, parametersDigest)`). A block carries an optional
`ParameterChange`; `ApplyBlock` re-verifies it against the validator set (passed variadically — only
a block with a change needs it) and, if ratified, sets the new parameters — otherwise the block is
rejected. So every node applies the same change deterministically, and the transaction cost is taken
from committed parameters, not chosen per block (a proposer can't set a rogue cost).

## Collecting the votes

A validator votes with `SignParameterVote(newParameters, currentParameters.Digest())` and POSTs a
`ParameterChange` carrying that vote to `POST /hdls/parameter-vote`. Nodes pool votes per proposal
(`node/governance.go`), verify and dedup them, and gossip (`MsgParameterVote`) so votes converge
network-wide. When a pooled proposal reaches quorum and still amends the current parameters, the
proposer carries it in its next block; stale proposals are pruned each commit.

## Balance reassignment (the second governance action)

The same certificate mechanism governs one more thing: a **> ⅔-ratified override of an account's
balance** (`ledger.Reassignment`). This is the community's deliberate exception to
payment-conservation — money normally only moves via a signed payment or burns as a fee, but a
supermajority can mint or reduce a specific balance (e.g. issue a community grant, or correct an
error). It is the Go form of the original Hypernode "reassign proposal".

    Reassignment {
        Account        pubkey
        PreviousAmount big.Rat             // the balance it must currently hold
        NewAmount      big.Rat             // what to set it to (non-negative)
        Votes          []ReassignmentVote  // each signs the three above, domain "reassignment-vote"
    }

`Reassignment.Valid(scheme, network, validators, currentAccounts)` accepts it only if `NewAmount` is
non-negative (the guard from the Java `reassign-non-negative` fix), the account **still holds
`PreviousAmount`** (the replay guard *and* the exact starting point the voters signed off on — so a
stale certificate can't replay once a payment or an earlier reassignment moves the balance), and the
votes meet the > ⅔ quorum. `ApplyBlock` re-verifies it and sets the balance (creating the account if
absent, so a grant can fund a newcomer); it is applied *after* payments, as the final override — so a
payment crediting the same account in the same block is superseded by the reassignment's absolute
value (deterministic, and reassignment is rare, but worth knowing). Applying it bumps the governance
sequence, which is what makes the certificate single-use (it cannot be replayed after the balance
cycles back — see the sequence discussion below).

The node side mirrors parameter voting exactly: a vote pool (`node/reassignment.go`), gossip via
`POST /hdls/reassignment-vote` (`MsgReassignmentVote`), a proposer carrying a quorum-reached
reassignment in a block, and pruning stale ones each commit.

Reassignment is the **only** way to create money (it mints out of nothing), so its use is monetary
policy, not just administration — see [managing-the-currency.md](managing-the-currency.md) for how to
wield it (the treasury pattern, and the honest limits of the tax/services/stability trilemma).

## The governance sequence (single-use certificates)

Committed state carries a monotonic `GovernanceSequence` (in `LedgerState`, and under the block
digest). Every governance certificate — a `ParameterChange` or a `Reassignment` — binds the sequence
it amends into each vote's signed preimage, and `ApplyBlock` applies it only while that sequence is
current, then **bumps the sequence by one**. So a ratified certificate is *single-use*: once applied,
the sequence has moved on and the certificate can never be re-applied — even if the parameters or the
balance it changed later cycle back to exactly what they were.

This closes a replay the review found: guarding only on the amended *value* (previous parameters,
previous balance) failed whenever the value cycled back — most sharply, a newcomer grant
(`prev 0 → X`) that could be re-minted every time the grantee spent to zero. The sequence anchor also
avoids target-height coordination: votes accumulate freely because the sequence is stable *between*
governance actions, and only advances when one applies. Because both certificate types share the one
sequence, a block carries **at most one** governance action (a second would bind a now-stale sequence);
`ApplyBlock` rejects a block that carries both.

## The admin UI

The wallet ships an **admin / governance page** (linked from the wallet, or `/admin`): it reads
`GET /hdls/governance` — the current parameters, the validator set / quorum, and the pending
parameter and reassignment proposals with live vote tallies — and lets a validator propose or add a
vote to a transaction-cost change or a balance reassignment. Votes are signed with the wallet key, so
only a validator's vote tallies toward the quorum (the page says so if the wallet isn't a validator).
`make demo` brings up a node + this UI with the founder as the sole validator, so a vote enacts in a
block or two.

## The timing profile (coupled parameters)

`LedgerParameters` carries more than the cost: the **exchange-window cadence** (`BlockIntervalMs`),
the **per-step consensus timeout** (`StepTimeoutMs`), and the **gossip fan-out** (`MaxPeers`). These
three are *interconnected* — the step timeout must be long enough for a proposal/vote to fan out
across the gossip dimension the fan-out implies at the current node count, or votes strand and
consensus stalls. Moving the window changes how many nodes you can reach in it; changing the fan-out
changes how long a window must be. So they are governed **as one profile**, and
`LedgerParameters.Acceptable(participants)` enforces the coupling: it rejects any profile whose
`StepTimeoutMs` is below `dimension × AssumedGossipHopMs`, where the dimension comes from the same
`ledger.GroupParameters` the topology uses (one source of truth). The check runs in
`ParameterChange.Valid` (so every node rejects an ill-formed profile deterministically), at vote
intake (so a hopeless proposal never pools or gossips), and in the wallet (so a validator gets a clear
reason up front). The node reads all of this from committed state — `arm`, the block pacing, and the
peer-DNS fan-out all use the governed values — so a ratified change re-tunes the live network. The
profile is seeded from a node's config at genesis (`Timeout`/`MinBlockInterval`/`MaxPeers`), which must
match across nodes, and governance is how it changes thereafter.

## Extending it

`LedgerParameters` can grow further (mempool bounds, the validator stake threshold): add the field,
include it in `LedgerParameters.fields` (which feeds both `Digest` and the vote preimage), extend
`Acceptable` if it has an invariant, and it becomes governable by the same parameter certificate.
