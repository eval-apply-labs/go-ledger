# Wallet — user guide

Area: **UI**. A short guide to using your community wallet. No technical knowledge needed.

## What the wallet is

Your wallet is a small program on your own computer. It holds your key, the secret that proves you
are you, and lets you check your balance, pay people by name, and protect your name. Your key never
leaves your machine.

## Starting the wallet

Your community organiser gives you three things: the wallet program, the address of a node (like
`http://10.0.0.2:8080`), and the network's name (like `village`). Run the wallet as your organiser
shows you, then open your browser at **<http://127.0.0.1:8090>**.

The first time, the wallet makes your key, right there on your computer, asks you to choose a
**passphrase**, and saves the key in `wallet.json`. The passphrase locks the file: you type it each
time you start the wallet, and a copied or stolen `wallet.json` is useless without it. (An empty
passphrase stores the file unlocked; only do that on a machine nobody else can touch. The wallet
offers to lock it each time it starts.)

> **Keep these files safe: they are your money.** Back up `wallet.json` and the two small files next
> to it: `wallet.json.trust`, the banks your wallet trusts, and `wallet.json.state`, a counter that
> stops a restored wallet from clashing with payments you sent just before. Copy them together and
> restore them together. If you lose the key file or forget its passphrase, you lose your name and
> its money, unless the banks vote to give your name a new key.

> **One key, one machine.** Never run two copies of one `wallet.json` at the same time. If payments
> from both copies land in one block and together spend more than you have, the network destroys
> your **entire balance**. To use another machine, **move** the files, don't copy them.

## Getting your name: the double signature setup

Your account is a **name**, like `alice`. You make your key yourself, and your bank registers it.
Your bank is one of the banks that run the network.

1. Start your wallet for the first time **at your bank**, connected to the bank's node, and open its
   page there. Your wallet notes which banks vouch for the network in `wallet.json.trust`, and from
   then on believes only balances they signed. (If you can't start it at the bank, ask the bank for
   its list and save it as `wallet.json.trust` first.)
2. Your page says **not registered** and shows a short fingerprint of your key.
3. Open the **Requests** page. Choose "registration", type the name you want (or leave it blank for
   a hash name, below), and type the requestor: whoever pays the fee, usually your bank. Click
   **Draft request**, then paste the text into **Sign my roles**: your wallet signs as the new key.
4. Hand the signed text to your bank. The bank checks who you are, signs on its own Requests page,
   checks that the request's new key has the fingerprint your page shows as "your key", and submits
   it.
5. The request names a block about 120 blocks ahead, so its signers have time; at one block a
   second that is two minutes. Once the network reaches it, your page shows your name and your
   balance. Your bank is now your **bank of record**.

A name is 1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or a
digit. The wallet lowercases what you type and shows you the result.

## Your wallet page

At the top: your balance and your name, your key's fingerprint, and a **copy my full key** button.
Then any warning about your name, and the **Freeze** button. Below: **Send a payment**, the
**description, delegation and address** form, and the list of accounts on the ledger, each with its
bank of record and balance.

The copy button works when you open the wallet at `127.0.0.1` or `localhost`, as usual. Opened from
another machine by its LAN address, the browser does not let it copy.

## Getting paid and paying

To get paid, tell people your **name**.

To pay someone:

1. Type their **name** and the **amount** (like `10.00` or `.5`: at most 8 digits after the point).
2. Click **Review payment**. The wallet shows the name and that name's **bank of record**.
3. Check the bank. Names can look alike (`rn` and `m`, `1` and `l`); if it is not the bank you
   expect for that person, don't pay.
4. Click **Sign and send**.

You'll see *"payment queued for block 42 — receipt from validator bank-a verified ✓"*. The receipt
is a bank's signed promise that it took your payment, like a card machine's "approved"; the wallet
shows ✓ only if it comes from a bank your wallet trusts. A small fee is taken on each payment, out
of what the payee receives: pay 10 at a fee of 0.1 and they get 9.90. Payments are final.

## If something is wrong: freeze your name

If someone else may have your key, or you see a change on your name you did not ask for, press
**Freeze** on your page, then **Sign and send the freeze**. The freeze is free and takes effect within
a few blocks. A frozen name can neither pay nor be paid, and any pending change on it is cancelled.
Only a vote of the banks can unfreeze it, and they give it a new key when they do.

- The page then says your freeze was **sent for block N, not confirmed**, until it shows your name
  frozen.
- If block N passes and your name is not frozen, the page says the freeze **did not land**. Contact
  your bank, or another bank. The page can only tell once the network reaches block N, so on a quiet
  network a freeze that went astray can stay "sent, not confirmed" for a while.
- Then go to your bank with a new wallet. The banks vote to give your name the new wallet's key; the
  new wallet signs first, on its admin page, and your bank tells you when.

## The warning about a pending change

*"A key change (to key ab12…) on your name takes effect at block N. If you did not ask for it, freeze
your name now."* A key change or a bank change waits about a day before it takes effect. That wait
is your chance to stop one you did not ask for: freeze your name.

## Moving your name to a new key, or selling it

1. The new wallet (yours on a new machine, or the buyer's) copies its full key.
2. In your wallet, on the Requests page, draft a "key change" with that key, and sign it.
3. The new wallet signs the text; your bank of record signs it too, then someone submits it. You pay
   the fee.
4. Both wallets show the pending change. About a day later the new wallet holds the name, and your
   old wallet shows "not registered".

A name with no bank of record changes its key only by a vote of the banks.

## Changing your bank

Draft a "bank change" with the new bank's name and sign it; the new bank signs it on its own page and
submits it. It waits about a day. Your old bank is not asked.

## A name that doesn't say who you are

Leave the name blank when you draft a registration, and the wallet uses your **hash name**: `k-`
followed by 24 letters and digits made from your key, 26 characters in all. Anyone can still pay
it. It is a pseudonym: your bank of record is public, and the bank that registered it knows who
you are.

## For banks: the desk

Your desk is the Requests page of your bank's wallet, which holds the bank's key. Co-sign a request
only after checking the holder in person or by a second channel. For a key change, confirm with the
name's holder, by a second channel, that they asked for it.

When you sign, the page shows the request's facts; nothing is sent yet. If they name a **new key**,
compare its fingerprint with the "your key" fingerprint on the new key's own wallet page: the
customer's, for a registration. Submit the request or hand it on only if they match; otherwise
throw the text away.

The page lists the pending changes on your customers' names: tell each one, so they can freeze if
they did not ask for it.

## Common questions

**My balance didn't change.** Refresh the page. If it still hasn't, the node may be offline; wait,
or ask your organiser.

**I got a receipt, but the money never moved.** A payment is for one upcoming block; if it misses
it, it expires and nothing is taken. Send it again.

**The node refused my payment.** It says why: the payment would spend more than you have (counting
payments you just sent), it is below the fee or above the largest payment, or the name cannot be
paid. Nothing is debited.

**It says "wait for the chain to advance".** Your wallet gives every payment its own block, and a
payment the node refuses still uses one; one the wallet refuses when you review it does not. After
33 refusals by the node in a row on a quiet network, or fewer if you sent payments just before, the
wallet waits for the network to move. Any payment or update anyone makes moves it. Restarting the
wallet does not help.

**It says the node's state is not signed by the pinned validators.** Your wallet is talking to a
node it should not trust, or the banks have changed their keys. Don't send anything; ask your bank
for its new list, save it as `wallet.json.trust`, and restart the wallet.

**It says the node serves an older state.** The node is behind, or hiding something newer. Try
again later, or ask your organiser for another node.

**It says "cannot read a verified state from the node", and "connection refused" or a timeout.**
Check that you are on the community network and the node address is right.

## Privacy

Everything on the ledger is **public**: your name, your balance, your description, your bank of
record, any pending change on your name, and every payment, who paid whom and how much. A payment is
public as it travels and in the block that carries it; no node keeps it after the next block, but
anyone who watched may have kept a copy. Don't put anything private in a name or a description. A hash name is a pseudonym, but its bank is known.
Your wallet sees the network through one node, your bank's or your organiser's: it shows what that
node serves, checked against the banks' signatures.
