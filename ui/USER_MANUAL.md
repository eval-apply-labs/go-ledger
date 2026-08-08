# Wallet — user guide

A short guide to using your community wallet. No technical knowledge needed.

## What the wallet is

Your wallet is a small program that runs on your own computer. It keeps your account for the
community currency and lets you check your balance and pay other people. Your secret stays on your
machine — the wallet never sends it anywhere.

## Starting the wallet

Your community organiser will give you three things:

- the wallet program,
- the address of a node to connect to (something like `http://10.0.0.2:8080`),
- the network name (for example `village`).

Run the wallet (your organiser will show you the exact command), then open your web browser to
**<http://127.0.0.1:8090>**. You'll see your wallet page.

The first time you run it, the wallet creates your account automatically, asks you to choose a
**passphrase**, and saves the account in a file called `wallet.json`. The passphrase locks the file
the way a passphrase locks an SSH key: you type it each time you start the wallet, and a copied or
stolen `wallet.json` is useless without it. (Pressing Enter on an empty passphrase stores the file
unlocked — only do that on a machine nobody else can touch. An old unlocked file keeps working and
offers you the upgrade on startup.)

> **Keep that file safe — it *is* your money.** If you lose the file, or forget its passphrase, you
> lose access to your account. Make a backup somewhere only you can reach (a USB stick you keep
> safe, for example) — with a passphrase set, the backup itself is safe to store anywhere. Back up
> **both** `wallet.json` **and** the small `wallet.json.state` file next to it: the second is a tiny
> counter that stops a restored wallet from accidentally clashing with a payment you sent just
> before the backup was restored. Copy the pair together, restore the pair together.

> **One key, one machine.** Never run two wallets from the same `wallet.json` at the same time —
> for example a backup restored onto a second computer while the first still runs. If payments from
> both copies land in the same block and together spend more than you have, the network treats it
> as attempted double-spending and destroys your **entire balance**, not just the overspent amount.
> This is by design (it is what makes double-spending pointless) and cannot be undone. To use a
> different machine, **move** `wallet.json` and its `.state` file — don't copy them — or create a
> fresh wallet there and pay yourself to it.

## Your wallet page

At the top you see:

- **your balance** — how much community currency you hold;
- **your account** — a long code (your *public key*). This is like your account number; share it
  with anyone who wants to pay you.

Below that is a **Send a payment** form, and a list of **all the accounts** on the ledger with their
balances.

## Getting paid

Give the person paying you **your account code** (the long code shown under "your account"). They
paste it into their wallet's *recipient* box and send. Your balance goes up within a few seconds —
refresh the page to see it.

## Sending a payment

1. Find the person you want to pay in the accounts list and copy their **public key** (the long code
   in the middle column) — or ask them to send it to you.
2. Paste it into the **recipient** box.
3. Type the **amount** (for example `10.00`).
4. Click **Send**.

You'll see a confirmation like *"payment queued for block 42 — receipt from node abc… verified ✓"*.
That receipt is the node's signed promise that it accepted your payment (a bit like a card machine's
"approved" beep) — the wallet checks the node's signature for you. Within a few seconds the
recipient's balance goes up and yours goes down. A small transaction fee is taken on each payment.
(If the node accepts the payment but doesn't return a receipt, you'll just see "payment submitted" —
the payment is still queued.)

## Registering a name

In the **Register a name** box, type a short name (like `alice`, up to 32 characters) and,
optionally, a short description, then
click **Register**. Once it's confirmed (a few seconds), people can pay you by typing your *name*
instead of your long public key. Names are first-come: if someone already has the one you want,
you'll keep your old one — just pick another. Your own name is shown next to "your account" at the top.
(A very long description can exceed the network's per-message size limit; if so the wallet tells you
to shorten it rather than letting the registration quietly fail to land.)

## Paying by name

In the **recipient** box you can type either a registered **name** or a full public key. If you type
a name, the name itself goes into the signed payment and the *network* pays whoever holds it on the
ledger — no single node decides who that is. If no one holds that name (a typo, say), the node
refuses the payment and tells you, rather than accepting money for a recipient that does not exist —
so a mistyped name is a clear error, not a silent loss.

## Common questions

**I sent to the wrong person.** Payments are final — there is no undo. Always double-check the
recipient code before sending. (If it was a neighbour, ask them to send it back.)

**My balance didn't change.** Refresh the page. If it still hasn't after a moment, the node may be
briefly offline — wait and try again, or ask your organiser.

**I got a receipt, but the money never moved.** A payment is scheduled for one specific upcoming
block; if it doesn't reach the network in time for that block, it simply expires — nothing is taken
from your balance. Wait until the wallet shows the node again and send it once more. (If this
happens often on your network, tell your organiser — the wallet's schedule can be widened.)

**The node refused my payment.** When the network is very busy, or the payment would spend more
than you have (counting payments you just sent that haven't cleared yet), the node says no and
nothing is debited. Wait a moment and send again.

**It says "cannot reach node".** The wallet can't talk to the node right now. Check you're on the
community network and the node address is right, or ask your organiser.

**Is my money safe if my laptop breaks?** Your account lives on the shared community ledger, not
only on your laptop — but you need your `wallet.json` file (your key) and its passphrase to get to
it. Keep a safe backup of that file, and don't forget the passphrase.

## Privacy

Everything on this ledger is **public**: your balance, your registered name and description, and
every payment — who paid whom, and how much — are visible to everyone on the network, and once
anyone keeps a copy, permanently. Don't put anything private in a name or description, and treat
your payment history as something your whole community can read.
