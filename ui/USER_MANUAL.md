# Wallet — user guide

Area: **UI**. A short guide to using your community wallet. No technical knowledge needed.

## What the wallet is

Your wallet is a small program on your own computer. It holds your key, the secret that proves you
are you, and lets you check your balance, pay people by name, and protect your name. Your key never
leaves your machine.

## Starting the wallet

Your community organiser gives you three things: the wallet program, your bank's **public address**
(like `https://bank-a.example`), and the network's name (like `village`). Run the wallet as your
organiser shows you, with that address, then open your browser at **<http://127.0.0.1:8090>**. (A
bank's own wallet uses another address: see "For banks" below.)

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

1. Start your wallet for the first time **at your bank**, and open its page there. Your wallet notes
   which banks vouch for the network in `wallet.json.trust`, and from then on believes only
   balances they signed. When the banks change, your wallet follows them, as long as most of the
   banks it knows sign the change. The first time matters most: your wallet then trusts whichever
   banks it is shown. Through your bank's public address, that is whatever the address, and the
   company that serves it, shows. At the bank, the bank can connect your wallet to its own node
   for this first start. (If you can't start it at the bank, ask the bank for its list, its
   `.trust` file, and save it as `wallet.json.trust`, next to `wallet.json`, before the first
   start.)
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
Then any warning about your name, what the other banks say, and the **Freeze** button. Below:
**Send a payment**, the **description, delegation and addresses** form, and the list of accounts on
the ledger, each with its bank of record and balance.

The form can also publish where your name is reached. Most people leave these blank:

- a **public address**, such as your website or, for a bank, its public face: `http://` or
  `https://`, the host in lowercase and maybe a port, with nothing after, such as
  `https://bank-a.example`;
- up to 4 **IP addresses**, separated by spaces, such as `203.0.113.7 2001:db8::7`. They replace the
  ones you published before. A bank's are where the other banks send it their messages.

The wallet refuses another form, a trailing `/` included, and says why. **What you publish is public
for good:** anyone can read it, and anyone may have kept a copy, even after you clear it with `-`.

Your wallet reads the ledger from your bank's public address, and asks the other banks how far the
network has gone. Usually a grey line says *"Checked with banks holding 100% of the power: this
state is as current as theirs."* The common questions below explain the other notes.

The copy button works when you open the wallet at `127.0.0.1` or `localhost`, as usual. Opened from
another machine by its LAN address, the browser does not let it copy.

## Getting paid and paying

To get paid, tell people your **name**.

To pay someone:

1. Type their **name** and the **amount** (like `10.00` or `.5`: at most 8 digits after the point).
2. Click **Review payment**. The wallet shows the name and that name's **bank of record**.
3. Check the bank. Names can look alike (`rn` and `m`, `1` and `l`); if it is not the bank you
   expect for that person, don't pay.
4. Click **Sign and send**. (If your bank can't be reached, click **Sign and save as text** instead:
   see "When your bank can't be reached" below.)

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
  your bank, or another bank. The page can only tell once the network reaches block N. A quiet
  network still makes a block about every 5 minutes, so a freeze that went astray shows as not landed
  within about a quarter of an hour.
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

## When your bank can't be reached

If the network is cut, your wallet may not reach your bank. It can still show a balance someone
carried in on a file, and sign a payment as text for someone to carry to a bank. People who carry
messages between places cut off from each other are called carriers: a person, a USB stick, an
email, a radio link.

**Load a checkpoint from a file.** A checkpoint is a bank's signed summary of the ledger. Someone
copies it from a bank and carries it to you as a file.

1. When your page shows no balance, it offers **Load a checkpoint from a file**. Open the file in a
   text editor, copy all of it, paste it there, and click **Load checkpoint**.
2. Your wallet checks it exactly as it checks your bank: the banks your wallet trusts must have
   signed it. It refuses a forged file, another network's, or one older than the balance you saw
   last, and says why.
3. Your page then shows your balance **as of height H, from a file**. It may be out of date: it is
   the ledger as it was when the file was made.

Your wallet needs to know the banks first: open its page once at your bank, as you did the first
time. It shows the file's balance only while your bank can't be reached, or shows an older
balance. As soon as your bank answers with a balance as new, your wallet uses your bank again. If
your bank answers with something your wallet refuses, such as a balance the banks did not sign,
your page says so even with a file loaded, and refuses payments; the **Freeze** button still works.
The wallet forgets the file when you stop it.

While your page shows a balance from a file, the wallet sends no payment and no update to your
bank. **Sign and send** is still on the page, but it says *"your bank's node can't be read, or its
state is older than your file's: save the payment as text"*, and nothing is signed. Use **Sign and
save as text**. The **Freeze** button still works.

The box takes a file of up to 64 MiB, as large as a bank's own checkpoint can be. A larger one is
refused as "too large".

**Save a payment as text.**

1. Review the payment as usual, then click **Sign and save as text**. Nothing is sent.
2. The page shows the payment as two lines of text. Copy all of it, and hand it to whoever carries
   messages to a bank next. Any bank can take it in. Reloading the page shows the same payment
   again: it signs nothing new.
3. The payment is for one block, which the page names. If no bank has taken it in by the time the
   network reaches that block, it lapses and nothing is taken. When the whole network waits for
   carriers, there is time. But if the rest of the network runs normally, that block comes soon,
   within seconds or minutes, so a payment carried out of a cut-off area may well lapse. Once your
   page shows the network past that block, and your balance without the payment, it has lapsed:
   only then sign it again.
4. **Don't sign the same payment again while the text may still arrive.** Each payment you sign is
   for a block of its own, so both could be paid.

## For banks: your own wallet

Run your bank's own wallet with your engine's **operator port** as its address, such as
`http://10.0.0.2:8080` on your bank's own network, not with your public address. Only there can the
admin page submit a vote. At your public address the admin page still shows the parameters and the
votes waiting for a block, but a vote you submit gets: *"votes are submitted on the bank's operator
node, not at its public address, which passes on only payments, account updates and name
requests"*. Your desk, below, works at either address.

A customer may bring a payment saved as text. Save the text as a file, and take it in on your
engine's operator port, as [the runbook](../docs/operating-a-network.md#bundles) shows:
`curl --data-binary @payment.txt http://10.0.0.2:8080/hdls/bundle`. The answer says whether it was
taken or refused, not why.

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

**My balance didn't change.** Refresh the page. If it still hasn't, your bank may be offline; wait,
or ask your organiser.

**I got a receipt, but the money never moved.** A payment is for one upcoming block; if it misses
it, it expires and nothing is taken. Send it again.

**The node refused my payment.** It says why: the payment would spend more than you have (counting
payments you just sent), it is below the fee or above the largest payment, or the name cannot be
paid. Nothing is debited.

**It says "wait for the chain to advance".** Your wallet gives every payment its own block, and a
payment the node refuses still uses one; one the wallet refuses when you review it does not. After
33 refusals by the node in a row on a quiet network, or fewer if you sent payments just before, the
wallet waits for the network to move. Any payment or update anyone makes moves it, and a quiet
network moves by itself about every 5 minutes. Restarting the wallet does not help.

**It says "the banks that sign have changed since this wallet last checked".** Your wallet follows
the banks as they change, but too many changed since you last opened it. Don't send anything. Ask
any bank for its current list (the `.trust` file of the bank's own wallet), save it as
`wallet.json.trust`, and restart the wallet. Or switch to another bank: delete `wallet.json.trust`,
and start the wallet with that bank's public address, at the bank, as you did the first time. Take
a new list, or pin again, only at a bank, never through a node you do not trust: a node that is not
your bank's can show this message to get its own list trusted. The freeze button still works
meanwhile.

**It says the node's state is not signed by the pinned validators.** Your wallet is talking to a
node it should not trust. Don't send anything; ask your bank or your organiser for another node.

**It says "Your node may be hiding newer blocks".** Banks holding more than a third of the power
report blocks your node does not show. A change on your name may be waiting that your page cannot
show. Check your name at another bank, or start your wallet with another bank's public address. If
a change you did not ask for is waiting there, freeze your name from there.

**It says it "could only check banks holding" some share of the power.** Too few of the other
banks answered, or they publish no public address, so your wallet cannot tell whether your node
shows the newest blocks. A node that answers with a redirect counts as no answer: the wallet follows
none, so no node can send it to another page. Your balance is still genuine, only maybe not the newest.

**It says the chain may have stalled.** No new block for over 10 minutes, or longer if your network
waits longer between blocks. A working network makes one about every 5 minutes, even when nobody
pays. Payments and changes wait until it moves again; tell your bank or your organiser.

**The page says "as of height H, read before".** Your bank's node now shows an older balance than
one your wallet read a moment ago, as a cache may for a few seconds, or as a node that fell behind
does. The page shows the newer one. If it lasts, ask your bank.

**It says the node serves an older state.** After a restart, your wallet refuses a balance older
than the newest it saw before. The node is behind, or hiding something newer. Try again later, or
ask your organiser for another node.

**It says "cannot read a verified state from the node", and "connection refused" or a timeout.**
Check that you are on the network and your bank's address is right. If your bank can't be reached
for a while, see "When your bank can't be reached".

**The page says "as of height H, from a file".** It shows the checkpoint file you loaded, not your
bank's newest state: your bank can't be reached, or shows an older balance. Save payments as text
meanwhile: **Sign and send** is refused. As soon as your bank answers again with a state as new,
the wallet uses it.

**It says "the checkpoint file is refused".** It says why:
- "this wallet trusts no banks yet": open your wallet's page once at your bank first;
- "not signed by the validators pinned": the file is forged, damaged, or from another network;
- "not in name order, or does not match its commit": the file is damaged;
- "the banks that sign have changed": see that question above;
- "older than the state … this wallet verified before": you have seen a newer balance already; ask
  for a newer file;
- "unreadable checkpoint": copy the whole file, from its first character to its last;
- "too large": a checkpoint file has at most 64 MiB; ask for the file again;
- "the form is unreadable": paste it again.

**It says "votes are submitted on the bank's operator node".** You run a bank, and your wallet uses
your public address. Restart it with your engine's operator port: see "For banks: your own wallet".

**It says "this node does not take …: it answered 404 Not Found".** The address your wallet uses
is not a bank's public address or node. Check it with your organiser.

## Privacy

Everything on the ledger is **public**: your name, your balance, your description, your bank of
record, any pending change on your name, and every payment, who paid whom and how much. A payment is
public as it travels and in the block that carries it; no node keeps it after the next block, but
anyone who watched may have kept a copy. Don't put anything private in a name or a description. A hash name is a pseudonym, but its bank is known.
Your wallet sees the network through one address, your bank's public address: it shows what that
address serves, checked against the banks' signatures. It also asks every bank, at the public address
each one publishes, how far the network has gone, so each bank, and the company that serves its
public address, sees your computer's address. It asks only while the page is open, only at the host
and port each bank publishes, and follows no redirect. A public address or IP addresses you publish
on your name are public for good.
