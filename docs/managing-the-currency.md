# Managing the currency

Area: **general**: monetary policy spans the whole system.

> Monetary policy for a community running this ledger. The mechanics (fees burn, the treasury is
> minted by a ⅔ vote) are enforced by the code; the *policy* — and its honest limits — is the
> community's. This distils the design philosophy the currency was built around.


This is the monetary side of the ledger — for whoever stewards a community's currency, not just runs
a node. It matters as much as the code: the software enforces the *rules*, but the community sets the
*policy* within them. The short version: a > ⅔ vote can **create money out of nothing** — typically
into a community **treasury** — while ordinary use quietly **burns** it away. Neither inflation nor
deflation is a default of the system; the **net** of what you mint against what burns is your monetary
policy, and it is a deliberate choice made in the open by supermajority.

## What "the money supply" is

Every account holds an exact balance (a whole number of 0.00000001 units — no floating-point drift,
no rounding leaks). The **total supply** is just the sum of all balances. You can read it at any time
from a node: `GET /hdls/accounts` returns every account and its amount; add them up.

## Where the supply comes from: genesis

The genesis accounts *are* the entire starting money supply. Whoever writes the genesis file decides
who begins with what, and that total is the supply until governance changes it. Choose it on purpose —
there is no mining, no faucet, no automatic issuance to dilute it later.

## Normal use conserves value, and burns the fee

A payment moves value from payer to payee, and the **transaction fee is burned** — the payer is
debited the full amount, the payee is credited the amount *minus* the fee, and the difference is
destroyed, paid to no one (not even the validators — there are **no block rewards** here; validating
does not mint). So:

- A payment can never create money. The supply-conservation property test proves it: payments only
  move value or burn it.
- Every payment shrinks the total supply by one fee. With activity, the supply **slowly declines**.
  That deflationary bias is deliberate — the fee is both a spam cost and a supply sink.
- Every registration, key change and bank change burns one **new-key fee** from whoever requests it.

One more rule removes money: a block in which one payer's payments together overdraw it destroys
the balance that payer had before the block and cancels its payments; money paid to it in that
block still lands (`ledger.ProcessPayments`). Every intake point refuses a payment that
would overdraw its payer, so honest nodes never build such a block (see
[security.md](security.md#fixed-by-construction)).

Nothing else removes money on its own. A validator caught double-signing is **frozen**, not fined:
its name leaves the validator set and its balance stays where it is. A holder can freeze their own
name too, and the ⅔ can freeze any name. A freeze only stops the money moving; deciding where it
goes is a separate, public ⅔ vote (below).

## The only way to create money: the ⅔ name action

The ⅔ name action (see [governance.md](governance.md)) is the **sole** mechanism that can set a
balance outside a payment — i.e. the sole source of new supply. The minted amount is **created out of
nothing**: it is not taken from any other account, so it is genuinely new money. It is deliberately
hard to use:

- It requires a **> ⅔ supermajority** of the current validators' power. A minority (≤ ⅓) **cannot**
  create money, full stop.
- It is visible: the change is a certificate carried in a block, the new balance shows up on-chain
  for anyone to see and sum, and the signature record of each node that committed that block keeps
  the certificate for good.
- It is bounded to the balance it amends (it can't be replayed), cannot drive an account negative, and
  cannot raise a balance above the largest balance the network allows (`maxAccountBalance`).

**The treasury pattern.** In practice a community designates one account as its **treasury** and mints
into *that* — then spends it deliberately on grants, public works, paying contributors, or simply
replenishing what fees have burned. The treasury turns "create money out of nothing" from a one-off
act into a standing instrument of policy: the balance is on-chain and auditable, every top-up is a
⅔-ratified certificate, and outflows are ordinary signed payments from the treasury account. Nothing
in the code privileges such an account — it's a convention — but it's the natural home for the mint.
Keep it **undelegated**: where seats follow stake, a delegated balance is voting power, and a
delegated treasury could hand its validator the quorum. A name the ⅔ create backs no one, and so does
a genesis account with an empty `delegate`; an account update sets a delegation, or clears it with
`-`.

So inflation is not something that *happens to* this currency — it is something the community must
*choose*, by supermajority, in the open.

## Preventing inflation

Because minting is gated behind a ⅔ vote, "preventing inflation" is really about **disciplining the one
lever**:

1. **Guard the supermajority.** The only way money is ever created is ⅔ of validator power agreeing to
   it. Keep that threshold meaningful: don't let voting power concentrate. In dynamic mode (a
   genesis with a `validatorThreshold`), voting power follows **delegation** — an account holder's
   stake counts toward the validator name it delegates to — so who controls monetary policy is
   exactly who holds delegated stake. Watch for one bank accumulating enough delegated stake to
   approach ⅓ (a veto) or, worse, ⅔ (unilateral minting). Account holders re-delegating is how the
   community keeps that honest. In static mode (a genesis that lists its validators), the genesis
   fixes each validator's power and delegation moves none: only a new genesis adds a validator or
   changes a power.
2. **Make minting rare and principled.** Agree, as a community, on what a mint is *for* — a grant
   to a newcomer, a correction, replenishing burned fees — and treat anything else as out of bounds.
   The software won't stop a ⅔ majority; a social convention is what keeps it from being abused.
3. **Publish the supply.** Sum `GET /hdls/accounts` on a schedule and post it. A mint is on-chain and
   visible; watching the total is how the community notices if policy is drifting.
4. **Tune the fee thoughtfully.** The fee is governed (also a ⅔ vote). A higher fee burns supply faster
   (more deflation, and it discourages small payments); a lower fee weakens the sink. It is your
   deflation dial.
5. **Set the caps on purpose.** `maxPaymentAmount` bounds what one payment, a thief's included, can
   move; `maxAccountBalance` bounds what one name can hold, and what one mint can raise it to. Both
   are ⅔-governed. Keep the largest payment at or above the fee, or no payment can pass.

## Monetary policy: the line to toe

Put the two flows together and you have the whole policy. Over any period:

    net supply change  =  treasury mints (⅔ votes)  −  fees burned  −  ⅔ reductions  −  overdraw bans

The last term is zero while every node is honest (above).

The goal is usually **not** zero — but be precise about *what* you are targeting, because money-supply
growth and price inflation are not the same thing. Prices move with the supply *relative to real
activity* (roughly, prices ≈ money × velocity ÷ output). So if your community grows — more members, more
goods, more trade — you can and *should* mint faster and still hold prices steady, because there is more
to buy underneath; mint the same amount into a *flat* economy and it becomes price inflation. The target
is therefore not a fixed percentage of supply but **to grow the money supply roughly in step with the
real activity it can buy** — mild, steady price growth falls out of that, and it beats both hard
deflation (hoarding, a thinning float) and high inflation (lost trust).

Getting there is a balancing act — the trilemma of **low taxes, high services, and a stable currency**.
You cannot maximise all three at once; stewardship is toeing the line between them, and this system
gives you two dials:

- **The fee is your tax** — a deflationary sink that pulls money out of the private sector. It shares
  VAT's best properties (automatic, on every payment, netted on receipt, impossible to avoid, and it
  never stops anyone from working — unlike income or property taxes), but it is a **flat** charge per
  payment, *not* a percentage of the amount. That is a deliberate choice: a percentage would tax large
  and internal transfers alike — moving your own funds between wallets, consolidating, a treasury
  payout — and so tax *velocity itself*, which is the opposite of what a community wants; a flat
  friction leaves a big legitimate transfer nearly free per unit. Raise it to cool inflation or thin
  the float; lower it to leave more money in people's hands.
- **The treasury mint is your spending** — you create money to pay the people who do work *for* the
  community (its "employees": whoever keeps things running, settles disputes, builds shared things).
  They accept minted pay because they can spend it on real goods and services inside the community.

But the two dials are **not equal.** Minting is a strong one — arbitrary money creation. The fee is a
weak one: a small flat charge across a modest number of payments burns only a trickle against the whole
supply, so its real job is spam deterrence and a mild sink, not inflation control. **Inflation is
governed almost entirely by minting restraint**; don't imagine you can tax it back with the fee —
mostly you can't.

The three ways to get it wrong, from the original design's notes:

- **Too much tax** (fee too high) → productivity falls, a black market appears, and your most
  productive, honest people leave.
- **Too much minting** → inflation, which you can't always tax back without starving the private
  sector against the public one.
- **Too little spending** (services) → discontent; people drift to a community that offers more.

There is no automatic answer baked in — the fee and the treasury are both governed (⅔), and **how you
balance them is whether you run inflation or deflation, and whether the community feels well-served or
over-taxed.** It is a deliberate, supermajority, on-chain decision. (Note that the fee does not *fund*
the treasury — burning and minting are separate flows; spending is financed by fresh minting, and the
burn only offsets the inflation minting causes. A public sector on the order of a quarter of economic
activity is a historically workable size to aim for — enough for shared infrastructure while still
rewarding private enterprise — but it is a dial to tune, not a constant.)

## There is no solution — it's the short blanket

Say it plainly, because it is the most important thing on this page: the trilemma **has no solution**.
It is the *short-blanket* problem — pull the blanket up to cover your head and your feet get cold; pull
it down and your head is out in the cold. Every setting that helps one corner (lower taxes, more
services, a steadier currency) takes from another. There is no configuration of the fee and the
treasury that gives you all three at once, because the blanket is simply too short.

This is not a bug to be patched or an optimum to be computed. It is a hard, permanent, contested
problem — a question of values, not of engineering. Different communities will answer it differently
and be right to; the same community will answer it differently in fat years and lean ones. Anyone who
tells you there is a correct setting, or that the software has solved it, is mistaken or selling
something.

So this system deliberately **refuses to decide it for you**. What it offers instead of a false answer
is honest tools: a small, auditable set of levers; a supermajority requirement so no faction can impose
its answer unilaterally; and enough transparency to keep having the argument in the open, on the
record, as the community and its circumstances change. Manage it with judgement, revisit it often, and
make your peace with the fact that it is never "done."

## Minting is a tax on cash — made honest

Be clear-eyed about what a mint *is*. Creating an amount into the treasury dilutes every existing holder
by roughly that amount spread over the whole supply — economically a **flat wealth levy on all cash
balances**, whose proceeds fund the treasury. That is the *inflation tax* (seigniorage), and there is no
escaping it: to mint is to tax cash. The fee running the other way is its mirror — the **burn is an
anti-inflation-tax**, lifting the real value of the cash that remains, a small dividend to holders.

Structurally, this makes the system a clean, literal instance of what MMT *describes*: because the tax
(burn) and the spend (mint) are decoupled and there is no debt instrument, public spending is financed
by money creation and taxation exists only to control the resulting inflation — the currency can never
"run out of money," only create too much. Where it parts ways with MMT is exactly the part MMT is
accused of waving away — the budget constraint. Here that constraint is not a promise of restraint but a
*structure*: the ⅔ gate, the exit door (over-mint and holders flee to another currency), and the
trilemma that won't let anyone pretend a mint is free. The magic money tree, replaced by an
un-ignorable tradeoff.

None of this is new — only the honesty is. Italy ran precisely this tax for decades through the lira,
and, in its way, ran it astutely: a society shy of direct taxation, with a public sector to fund and an
industrial backbone that needed cash *circulating* more than it needed another layer of regulation, was
squared with a single near-frictionless lever — the one tax no one can evade, levied through inflation
and devaluation, quietly keeping the machine from squeaking. (Mario Draghi was at the Treasury through
that era; you can dislike the tax and still admit the execution.) The tell came when the lever was
*lost*: bound to the euro and unable to devalue, the state reached for the crude, *visible* version —
the 1992 overnight levy straight out of every bank account — and it caused panic precisely *because* it
could be seen. The hidden tax had been politically usable *because* it was hidden.

That is the tradeoff this design makes with its eyes open. It performs the same economic act — a mint is
a tax on cash — but on a public ledger, by ⅔ vote, with the dilution measurable to the cent, and
*defaulted off* (the deflationary bias means the resting state is no inflation tax at all). The honest
cost is real: a visible inflation tax is a *resisted* inflation tax, so you trade the frictionless
efficacy Italy enjoyed for consent and legitimacy — a trade you can only afford at community scale,
where consent is actually reachable. This is the concrete meaning of the currency's Austrian accent: not
sound-money piety, but the rule that *the inflation tax must be opt-in and visible, never the silent
default* — "never again the invisible lira," turned into architecture.

One inheritance worth claiming. Because the mint lands *directly* — in the treasury, then in the hands
of the people doing community work — it sidesteps the failure that dogged the post-crisis central banks:
money pushed through *banks* (cheap credit, negative rates), meant to reach the real economy and instead
pooling with those nearest the spigot — the Cantillon effect, the reset that mostly the inner circle got
to enjoy. Direct issuance is the equitable version those tools could not deliver. The catch is that
"direct" only stays fair if you can *see* where it goes: friend-circle capture of the treasury is
possible here too — it is simply, unlike a rate channel, a visible smell (see below).

## What actually gives the currency value

Not the fact that it "buys things" — that's downstream. A currency is a ledger of a *community*, and
what makes it valuable is **access to that community**: the goods, services, and people you can reach
by holding it. (Canadian dollars are hard to spend in Europe not because they're worthless, but because
the Canadian community isn't close at hand.) So the currency is worth as much as the community behind
it produces and trades — which is why funding real services, and keeping productive people willing to
stay, *is* monetary policy, not charity.

## Getting the currency circulating

A few ways communities bootstrap it (any mix works):

- Pay the people doing community work in the currency, and promise to accept it back in your own
  business.
- Extend a little credit or a gift to a promising newcomer so they can start transacting.
- Build or buy something the community needs with your own effort, and offer it for the currency.
- Start tiny — 5 to 10 people settling real exchanges — and grow by invitation.
- As a group builds something together, mint a print that records each person's contribution.

## It outlives the internet (pigeon mode)

You do not even need the internet to run this ledger. The consensus assumes nothing about how *fast*
messages travel — only that the head nodes exchange each round's votes before the exchange window
closes — so the window can be anything up to a whole day. And the window is a governed parameter
(a ⅔ vote), so a community can set its tempo to match its connectivity: seconds on a live shared
network, hours or a day when the link is a nightly courier — or, if you truly want, **avian carriers**.
A block still commits, the ledger stays global and auditable, and only the finality slows to the beat
of whatever carries the data.

The bird is the joke; the property is not. A currency that degrades gracefully to sneakernet instead of
failing is a real asset for communities that can't — or won't — connect to the mainstream internet: it
does not die when the infrastructure does, because its survival never depended on infrastructure it
doesn't control. The only cost is throughput and finality — a day-long window means roughly one block a
day, and payments count their deadline in days rather than blocks — so it is a *resilience* mode, not a
daily-commerce one: the fallback that lets a community keep its books no matter what the network is (or
isn't) doing.

## The levers, at a glance

| lever | who controls it | effect on supply |
|---|---|---|
| genesis balances | whoever writes the genesis file | sets the starting supply |
| transaction fee | governed, > ⅔ vote | burns each payment — the deflation dial |
| new-key fee | governed, > ⅔ vote | burns each registration, key change and bank change |
| ⅔ name action (the treasury tap) | governed, > ⅔ vote | the **only** way to mint — creates supply out of nothing (or reduces a balance) |
| caps (largest payment, largest balance) | governed, > ⅔ vote | none directly; they bound what one payment moves and one name holds |
| the freeze | the holder, a double-sign, or a > ⅔ vote | none: it stops a name's money moving until the ⅔ decide |
| delegation | each account holder | in dynamic mode, decides who holds the votes above; in static mode, none (the genesis fixes the powers) |

## A steward's checklist

- Publish the total supply regularly (sum of `GET /hdls/accounts`).
- Keep a written convention for when a ⅔ name action that mints is acceptable.
- In dynamic mode, watch delegated-stake concentration — no bank should quietly approach a ⅓ veto or
  a ⅔ majority.
- Review the fee against how fast supply is burning, and adjust by vote if it drifts.
- Remember the guarantee the code gives you: **no minority can create money, and no payment can.**

## At national scale: caveats, and the smells of misuse

This ledger was built for a small, high-trust, co-located community, and much of what makes it safe and
legible follows from that scope. A state adopting it in place of its mint deletes those assumptions, so
it's worth being clear-eyed — about what changes, and, just as important, about what *doesn't*.

**It is not a regression on the frightening powers — because those powers already exist.** Every fear
raised about a programmable national currency — the state can freeze your account, seize your balance,
print money, watch your transactions — is already true of the banking system you use today, usually with
far *less* visibility. A > ⅔ validator supermajority is a high, reasonable bar, and the powers it guards
are the same ones a central bank and a commercial bank already hold over you, exercised behind a counter
you cannot see. So the honest question at national scale is not "can this be abused?" — every monetary
system can — but "when it is abused, can anyone *tell*?" That is where this instrument is actually
different: every mint, seizure, parameter change, and validator sits on a public, auditable ledger. It
cannot make abuse impossible; it makes abuse **legible**, and hands ordinary people the book to check.

**Privacy is cash, not cryptography here — the hash name.** You don't need confidential-transaction
wizardry to have private money. A holder registers a name at a bank, which co-signs it and becomes its
bank of record (a genesis name, or one the ⅔ create, may have none), but a name need not be yours: a
wallet can register the *hash name* of its own key (`k-` and 24 more characters), fund it, and spend
from it without tying it to a person — used like cash or a prepaid card. The tradeoff is honest: it is
pseudonymity, not secrecy — the *flows* and the bank of record are public, and the bank that co-signed
knows who came to it — so it is private the way cash is private, not the way a numbered vault is.
Which is exactly why the freedom to hold such names is a civil-liberties bellwether: watch what a
government does to it.

**The smells of misuse.** Because the ledger is transparent, a state that captures or abuses it leaves
tracks. A national deployment should be watched for these — they are the nose that tells you the
instrument is being turned against the people it was meant to serve:

- **Anonymous names get squeezed** — banks refuse to co-sign hash names, or must file a person's
  identity behind every name they co-sign. Every name a holder registers gets a bank of record; a bank
  that may no longer keep a customer's identity to itself is the cash-like option disappearing, and the
  surveillance state arriving.
- **Money moves without a mandate** — mints or claw-backs that don't match an authorising democratic
  vote: unsubstantiated cash grabs, or supply created faster than the public record justifies. Cross-
  check the mint certificates against the votes; a gap is the smell.
- **The books get harder to read** — public read access to the account set or the total supply is
  restricted, the codebase goes closed or bloats past auditing, or the source of truth is obscured.
  Disabling the transparency is disabling the only real defence.
- **You can't get in as a node** — those who want to run a validator / head node are refused, stalled,
  priced out by a punitive stake threshold, or otherwise gated out of the main net. No vote moves that
  bar: the genesis writes it, as the stake threshold in dynamic mode or the list of validators in
  static mode, so watch whoever writes a new genesis. The barrier can be political *or* economic (see
  "the validator-entry bar" below); either way, a shrinking or hand-picked validator set is capture in
  progress.
- **Stake concentrates** — in dynamic mode, delegation pools into a few hands until a small group nears
  a ⅓ veto or ⅔ control. This is visible on-chain; a healthy currency keeps its power distribution wide.
- **Seizures get personal** — the ⅔ name action is used against *specific named accounts*
  (dissidents, journalists, opponents) rather than for general policy. On an open ledger a targeted
  zeroing is plainly visible — which is the point.
- **Soft censorship** — an account is never formally frozen; its payments simply never make it into a
  block. Deniable, and subtler than a seizure, but visible over time as one account's transactions
  consistently failing to land while everyone else's clear.
- **Votes stop having dissent** — every governance change passes with the same validators in perfect
  lockstep, forever. Unanimity that never wavers smells of coercion, not consensus.
- **Changes skip daylight** — mints, seizures, and parameter changes are executed with no public notice
  and no window to contest them (a healthy national deployment would announce and delay the big ones).
  One step cannot wait: money suspected stolen must be frozen before its holder hears of it, or it is
  gone. But a freeze only holds the money; deciding where it goes is the seizure, and that belongs in
  public, with a window to contest. Speed and secrecy around monetary power beyond that freeze is
  itself the warning. (The freeze exists: a holder freezes their own name at once, for free, and the ⅔
  can freeze any name; neither moves money. Moving the money is a separate ⅔ name action, whose votes
  are collected off the network; announce it once it has enacted.)
- **The fee is weaponised** — the transaction fee is set punitively to tax or exclude particular people
  or activity, rather than tuned as honest monetary policy.
- **Exit is blocked** — holding or trading alternative currencies is forbidden and there is no way to
  cash out. A currency that traps you is relying on the trap, not on being good.

One thing you needn't smell for: the ledger keeps **no payment history** (a payment is public as it
travels and in its block, but no node keeps it after the next block) — it carries the current
committed state and a record of who signed each block, every governance certificate included, not a
rewritable archive of payments — so there is no history to quietly re-write, and no "emergency"
retroactive rule change to fear. The only way to falsify what happened is to reinject a wholly
fabricated state, which breaks the chain of signed block hashes in plain sight. Tampering with the past here
isn't subtle; it is a blatant break or it is nothing. (And splitting wealth across sock-puppet accounts
buys no extra power — stake is stake whether it sits in one key or a hundred, so sybils don't move the
needle; only *real* concentration does, which is the smell above.)

**Where the cap is: the tradeoffs that fit small and cost big.** None of the choices below are
mistakes — each is a *prioritisation* that is right for a small, trusted, co-located community, which is
the whole point: there is no setting correct everywhere, only one correct *here*. This is not a scaling
guide and not encouragement to leave the envelope; it is an honest map of where each assumption starts
to degrade, how far you can hold it together, and — if you are ever forced past the line — the friction
to expect rather than be ambushed by.

- **Full replication — every node holds the whole ledger.** *Fits small:* trivial, and it is what gives
  everyone total visibility and a one-person audit. *Costs big:* you cannot replicate a nation's ledger
  to every node; you shard or go to light clients, and "everyone sees everything" — the thing that killed
  double-spend for free and made the ledger openly auditable — is the first casualty. *Friction:* the day
  you shard, trust moves from "I can see all of it" to "I trust the proofs and the shard-holders."
- **Full transparency.** *Fits small:* on one broadcast domain it dissolves partitions and double-spends
  and grounds audit-by-reading. *Costs big:* it becomes a surveillance graph, and hash-name
  pseudonymity is porous to a resourced analyst who pins the ramps and clusters the flows. *Hold it
  together:* hash names give real cash-like privacy against casual eyes; genuine privacy against a
  hostile state needs confidential transactions (ZK) — which fight the very auditability transparency
  gave you. Your best feature and your worst liability are the same feature.
- **BFT liveness — progress needs > ⅔ online, messages eventually delivered.** *Fits small:* co-located
  and reliable; on one WiFi partitions don't exist, so the hard part of consensus is free. *Costs big:*
  wide-area partitions are weaponisable — a state can censor or split validators — and "eventually
  synchronous" meets an adversary who controls "eventually." *Hold it together:* distribute validators
  across jurisdictions; the day-long window tolerates *latency* but not active *censorship*. You bought
  consistency with liveness, and at scale someone can attack the liveness on purpose.
- **Self-custody — you hold your own key.** *Fits small:* you chose it, you understand it, the loss is
  yours to price. *Costs big:* fatal — a whole population cannot hold keys safely; loss, theft, and
  coercion become mass problems. *Hold it together:* only with institutional recovery, which this design
  has in its smallest form: a holder freezes a stolen name at once, the ⅔ give it a new key (its
  bank can vouch for the holder, off the chain), and the money comes back by a ⅔ claw-back, when it
  can. That makes the banks trusted parties, and at scale it is a whole adjacent system, not a
  tweak. This is the assumption that most cleanly does *not* survive contact with scale.
- **No payment history — the ledger is "now," plus who signed.** Each node keeps a signature record:
  who signed each block, for `retainBlocks` heights, and every governance certificate and double-sign
  proof for good. It never keeps payments. *Fits small:* simple, light, tamper-evident, and the ⅔
  answer for their votes. *Costs big:* institutions, courts, and regulators want a record of payments
  — forensics, dispute evidence, audit trails over time — and a critical bug has no rollback. *Hold it
  together:* you can run archive nodes off to the side (an opt-in **history node** that observes the
  committed block stream, each block with its ⅔ commit certificate, and retains the payments the lean
  validators discard; it is the natural home for the misuse-watching below, and is on the roadmap in
  `TODO.md`), but that rebuilds the permanent surveillance record you were avoiding. The ephemerality
  that protects you is also the paper trail a large institution will demand and not find.
- **One global chain — a single total order.** *Fits small:* one definitive ledger, clean objective
  finality, no ambiguity about "the" state. *Costs big:* throughput-capped; national volume forces
  sharding or an L2 that fractures the single-ledger simplicity. *Hold it together:* the node-signed
  **receipt** gives instant provisional settlement (commerce doesn't feel the lag, like a card
  authorisation) while final settlement lands in the block — but sustained volume still needs a scaling
  layer, and every layer is a piece of the "hold it in one head" auditability you spend.
- **The validator-entry bar (stake to validate).** In dynamic mode it is the genesis's
  `validatorThreshold`, which no vote changes: it is set once per genesis (a static genesis lists its
  validators instead). *Fits small:* a low human bar — the people who would validate are known, and a
  little stake at risk is enough skin in the game. *Costs big:* the bar becomes a centralisation dial —
  set it high and only state-approved or custodial giants qualify; set it low and validators have too
  little to lose. *Hold it together:* balance it so someone able to stand behind a node can self-custody
  and run their own without a custodian, while betraying the role still costs something. One knob doing
  two fighting jobs — accountability and openness — that at scale can't fully satisfy both.
- **⅔ governance + delegated-stake validators.** *Fits small:* a community override among people who know
  each other, where capture is socially obvious and reversible. *Costs big:* the identical power is
  confiscation and debasement, and delegated stake means concentration *is* monetary capture. *Hold it
  together:* the smells make abuse legible (detectable, not preventable), and a serious deployment would
  add constitutional limits — things even a supermajority cannot do — which this design does not have. At
  village scale the supermajority can be sovereign; at national scale it must be *bounded*, and that
  bounding is unbuilt.
- **The 10,000-line, one-head-auditable codebase.** *Fits small:* the entire trust model — you trust it
  because you (or someone you trust) can read all of it. *Costs big:* sharding, privacy, custody, and
  high-throughput networking each add code until no one person holds the whole picture; trust migrates to
  formal proofs, repeated independent audits, and a track record. Making it national-grade dissolves the
  auditability that was its reason to be trusted — the sharpest statement of where the cap is.

**Where the line actually is.** The instrument is at home wherever *all* of these still hold at once: the
whole ledger fits on every node, everyone can see everything, people can hold their own keys, and the
validator set is small enough to know. That is roughly a handful of people up to a town, a bounded
community, or a single-purpose rail — not a national economy. The cap is the point where you are forced
to break the *first* of them, because breaking one forces the rest: shard the ledger and you lose
universal visibility; add custody and you add trusted parties; scale the validators and you lose the
knowable set. Past that line you are no longer running this system with more users — you are building a
*different* system in its clothes, and it deserves its own design, its own audit, and its own name.
Drawing the line is the point; you needn't be encouraged across it to know what it costs.

The through-line: this instrument does not, and cannot, stop a state from wielding monetary power — no
system does. What it changes is that it drags that power into daylight and gives every citizen the ledger
to audit it. Whether a country uses it to serve its people or to surveil and squeeze them is a human
question the code can't answer; the most it can honestly promise is to make the answer **visible**.
