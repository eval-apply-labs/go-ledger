# Why go-ledger stays small and auditable

## What prompted another pass

In October 2026, eight eMudhra intermediate CA keys turned out to hold only 3–8 bits of entropy. A
key-ceremony script error had produced a weak second key set, which was then used by mistake. The
keys could be recovered from the public certificates, and they stayed exposed for about 17 months.

That made me take another hard look at this project. The question was not only whether the same
failure could happen here (it cannot: every key comes from `crypto/rand`). It was whether the rest
of the code would hold up to the same scrutiny, and whether my rule that "two different signings
means the key is burned" would actually catch a leaked or weak key.

## What it found

The review is in [`review/post_emudhra_review_2026-10-27.md`](../review/post_emudhra_review_2026-10-27.md),
and [`findings_20261027.md`](../findings_20261027.md) lists every finding and what happened to it.
In short:

- **45 weak points confirmed:** 1 critical, 11 high, 20 medium, 10 low and 3 informational. One
  claim was refuted.
- **The worst:**
  - a single validator could re-apply payments from earlier blocks, including payments to itself;
  - an ordinary validator restart could fork the chain;
  - in dynamic-validator mode, nodes trusted the genesis keys forever;
  - anyone who could reach a node's port could freeze governance, or stall or halt the chain.
- **The burn rule did not hold yet.** Slashing deleted an account but left its key voting.
- **The fixes had bugs of their own.** An adversarial review of them confirmed 24 more issues, one of
  them high.
- **Three tests long dismissed as flaky** turned out to be real liveness bugs.

All of this was in code that had already been reviewed several times (see
[`findings.md`](../findings.md)).

## What that means

This program was written and reworked with a frontier LLM, and this pass was done with one too. It
still had all of the bugs listed above. An LLM can find bugs, fix them and test the fixes, and it
did. What it cannot do is take responsibility for deciding that code is fit to hold other people's
money. That decision belongs to a human who understands the code and answers for it.

That is why the goal of this project is to stay small and auditable:

- zero third-party dependencies;
- only standard-library cryptography;
- one static binary;
- plain code that one person can read end to end;
- names spelled out in full, Java style, even where Go style would use one letter or an
  abbreviation: `proposal`, not `p`; `ProposalMessage`, not `ProposalMsg`; a method's receiver named
  after its type (`node *Node`). Only `err`, the loop counters `i`, `j` and `k`, and `t` and `b` in
  tests stay short.

I am not a Go expert. The project is built so that someone who is can check all of it, and so that
someone who is not can still follow it.

**If you want to run go-ledger for real value, hire an experienced cryptography and security
engineer to audit the code and your deployment first.** Treat the reviews in this repository as
inputs to that audit, not as a substitute for it.

## How it is meant to run

- **Testing:** the single Go application stays the testing setup. One binary runs a node
  (`cmd/server`), and `cmd/demo` starts a node and a wallet on one machine.
- **Deployment:** three containers that can live on separate devices. This is planned, not built
  yet (Phase 3 of the review's fix plan).
  - The **fetcher** faces the network and forwards only the routes the outside may use.
  - The **engine** runs the ledger and talks only to the fetcher and the signer.
  - The **signer** holds the key behind a firewall and refuses to sign two different things for the
    same slot.

(Owner, 2026-10-30: four containers, one per docs area, the wallet included; see
[`TODO.md`](../TODO.md).)

(2026-10-31: in Phase 2 a double-sign no longer slashes. It freezes the name, which then neither
signs nor pays, and only the ⅔ can give it a new key; see [consensus.md](consensus.md).)
