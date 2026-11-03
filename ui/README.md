# go_ledger_ui — wallet

Area: **UI**. A local wallet for a go_ledger network. It holds your key on your own machine, or, as
a bank's wallet, signs through the bank's signer. It shows your name, your balance and the accounts
on the ledger, pays by name, freezes your name, and drafts and signs name requests and governance
votes with simple forms. When the bank can't be reached, it can sign a payment as text for someone
to carry to a bank, and read a checkpoint from a file. Code: `ui/`, with its pages in
`ui/templates/`.

It is a separate program and Go module from the node. It lives under `ui/` and can move to its own
repository once the root module is published: drop the `replace` in `go.mod` and require that
version. The guide for the people who use it is [USER_MANUAL.md](USER_MANUAL.md).

## Build and run

    go build -o wallet .
    ./wallet -node https://bank-a.example -network village -wallet wallet.json -listen 127.0.0.1:8090

Then open <http://127.0.0.1:8090> in a browser. From the repository root, `make build` builds it as
`bin/go_ledger_ui`.

| Flag | Meaning | Default |
|---|---|---|
| `-node` | where the wallet reads its state and sends what it signs: a bank's public address for a customer, or, for the bank's operator, its engine's operator port (below) | `http://127.0.0.1:8080` |
| `-network` | the network's name; the network id the node serves must start with it and `/` | `village` |
| `-wallet` | the key file; created on first run if absent | `wallet.json` |
| `-listen` | where the wallet serves its pages (keep it local) | `127.0.0.1:8090` |
| `-lookahead` | how many blocks ahead a payment or a freeze names, at most 32: a burst of payments runs up to 32 blocks further, inside the node's 64-block window | `3` |
| `-signer-url` | a bank's wallet whose key is in the bank's signer: where the signer listens, such as `http://172.28.71.254:7000`. `-wallet` then holds the bank's public key only (below) | none |
| `-signer-client` | with `-signer-url`: the client name the signer's config gives this wallet, such as `desk` or `admin` | none |
| `-signer-secret-file` | with `-signer-url`: the file that holds that client's secret, as `keygen -secret` prints it | none |
| `-password-file` | a file holding the password the wallet asks for, under any user name; required unless `-listen` is a loopback IP address, such as `127.0.0.1` (see "Security notes") | none |

## Which address to give `-node`

A bank's engine has two ports: the gossip port, for the other validators, and the operator port, for
the bank's own machines. The bank's public face, a fetcher behind a CDN, serves the public the files
the engine writes ([api.md](../docs/api.md#two-ports-and-the-public-face)):

- **A customer's wallet** uses the bank's **public address**: the `PublicAddress` its account
  publishes, such as `https://bank-a.example`. That is a CDN in front of the bank's fetcher. It serves
  the files the engine writes, and passes on payments, account updates and name requests. Every page
  of a customer's wallet works there.
- **The bank operator's wallet** uses its own engine's **operator port**, such as
  `http://10.0.0.2:8080` on the bank's own network. It serves every route. Only there can the admin
  page submit a certificate (`/hdls/parameter-vote`, `/hdls/reassignment-vote`). Against a public face
  the admin page still shows governance, and a submitted certificate gets "this node does not take …:
  votes are submitted on the bank's operator node, not at its public address, which passes on only
  payments, account updates and name requests" instead of the face's `404`. The wallet tells the two
  kinds of node apart by that answer alone: it probes no route. Any other `404`, as from a `-node`
  that is no bank's node, names the path and the answer: "this node does not take /hdls/update: it
  answered 404 Not Found".
- **A node with one port** (no `gossipListen`, as `make demo` runs it) serves every route on that
  port: give its address.
- **Never the gossip port.** It lets in only the IP addresses on its allow list: the validators',
  and the few the bank adds.

What the wallet asks of its node:

| Route | When | Public face | Operator port |
|---|---|---|---|
| `GET /hdls/status` | at first use, for the network id; when a request is drafted; for a freeze when no state verifies, or when a state read before stands in (below) | a file | yes |
| `GET /hdls/checkpoint` | once for each page | a file | yes |
| `GET /hdls/governance` | the admin page | a file | yes |
| `POST /hdls/pay` | a payment | passed on | yes |
| `POST /hdls/update` | an account update | passed on | yes |
| `POST /hdls/name-request` | a freeze; a request's *Submit* step | passed on | yes |
| `POST /hdls/parameter-vote`, `/hdls/reassignment-vote` | a certificate's *Submit* step | `404` | yes |

The main page also asks `GET /hdls/status` of every validator, at its public address (below).

## Its files

The wallet keeps three files side by side. Back them up and restore them together.

- **`wallet.json`, the key file.** For a bank's wallet that signs through its signer, the bank's
  public key only, `{"publicKey": "<base64>"}` (below). Otherwise `publicKey`, the key the seed
  derives, and the 32-byte seed:
  `privateKey` in plain base64, or `encryptedPrivateKey`, sealed with a passphrase (PBKDF2-HMAC-SHA256
  and AES-256-GCM, mode `0600`). On load the wallet derives the key from the seed and refuses a file
  whose `publicKey` differs. A file whose private key is not a 32-byte seed belongs to an earlier
  network: make a new wallet. To use a pair from `keygen` (a bank's, or a genesis account's), write
  the file before the wallet's first start with the two values `keygen` printed, as
  `{"publicKey": "…", "privateKey": "…"}`. The wallet seals it at the first start that has a
  passphrase (see [operating-a-network.md](../docs/operating-a-network.md#generate-keys)).
- **`wallet.json.trust`, the pin.** The network id and the validators the wallet trusts, each by
  name, key and power:

  ```json
  {"networkId": "village/f41a2488…", "validators": [{"Name": "bank-a", "PublicKey": "<2604>", "Power": 1}]}
  ```

  If the file is absent, the wallet pins on first use, when a page first reads a state, not at
  start: it takes `networkId` from the node's `/hdls/status`, refuses it unless it starts with
  `-network` and `/`, checks that the first checkpoint's validators hash to its header and signed
  its commit, and pins both. Afterwards the wallet believes a state only when pinned names holding
  more than ⅔ of the pinned power signed it, each under its pinned key, and it signs for the pinned
  network id only. A pin with no network id is an earlier network's.

  **First use trusts the path.** A first use through a public address pins whatever banks that
  address, and the CDN in front of it, serve; over plain HTTP, anyone on the path can choose them
  too. So a customer gets the list from the bank, the `.trust` file of the bank's own wallet, and
  saves it beside the wallet file (`wallet.json.trust` for `wallet.json`) before the wallet's first
  start. Or the customer does the first use at the bank, where the bank can point the wallet at its
  own node.

  The pin follows the chain. When a state verifies and carries another validator set, because
  banks re-keyed, joined or left, and its commit is a quorum of that set too, the wallet pins that
  set and rewrites the file (a new file renamed over the old one). The network id never changes. So
  the pin moves only to a set that more than ⅔ of the pinned power signed for, never by a node's
  looser catch-up rule, where a third suffices: a pin may hold a key that leaked, often the reason a
  bank was re-keyed. If the file no longer holds the wallet's pin, because a list was put there
  while the wallet ran, the wallet keeps the file and reads it at its next start.

  If banks holding a third or more of the pinned power were frozen or re-keyed between two reads,
  no state verifies until a new list is installed, and the pages say: "the banks that sign have
  changed since this wallet last checked, and too few of the banks this wallet trusts signed this
  node's state", and the two ways out. (For a checkpoint from a file, below, the message says "this
  file's state", and names a file wherever it names a node.)

  - get the current list from any bank, save it as `wallet.json.trust`, and restart the wallet: it
    reads the file only at start;
  - or switch to another bank: delete `wallet.json.trust`, restart the wallet with that bank's
    public address (`-node`), and open its page at the bank: it pins on first use.

  Take a new list, or pin again, only at a bank, never through a node you do not trust: any node,
  or anyone on the plain-HTTP path, can show this message by serving a made-up set of banks that
  signs for itself, to get its own list trusted. The message says so too.

  A bank's list is the `.trust` of its own wallet, which follows the chain like any other. If that
  wallet fell behind too, the bank deletes the file, restarts its wallet against its own node,
  opens its page once, and hands out the new file.
- **`wallet.json.state`, the sidecar**, kept per network name: the payment counter
  (`lastPaymentBlockID`), the blocks of the requests this wallet pays for (`requestBlockIDs`), the
  last name and the newest height it verified (`lastName`, `newestHeight`), and the freeze it last
  sent (`freezeName`, `freezeBlockID`). Started on another network name, the wallet begins a fresh
  one.

## A bank's wallet through its signer

A bank whose key is in its signer (`cmd/signer`, see
[signature.md](../docs/signature.md#the-signer)) runs its wallet with no key. The wallet asks the
signer for each signature, as one of the signer's clients, and checks each answer under the bank's
public key before it uses it. Everything else, the pin, the drafts and the pages, stays as it is.

    ./wallet -node http://10.0.0.2:8080 -network village -wallet bank-a.wallet.json \
        -signer-url http://172.28.71.254:7000 -signer-client desk -signer-secret-file desk.secret

- **Its file** holds the bank's public key only, the one the engine's config gives:
  `{"publicKey": "<base64>"}`. The wallet never writes a key into it, and asks for no passphrase.
  Its `.trust` and `.state` files go beside it, as for any wallet.
- **Its client.** The signer signs only the domains its config lists for the client the wallet
  names: the desk's (`registration`, `name-request-bank`, `freeze`), the admin page's
  (`parameter-vote`, `reassignment-vote`, `payment`, `account-update`, `freeze`), or both, for one
  wallet that is both, as the quickstart's `wallet` client is. A page that asks for any other gets the
  signer's refusal. No client gets `key-change` or `bank-change`: a bank's own key changes by the ⅔
  name action.
- **Who may reach it.** A bank's wallet signs as the bank: whoever drives it signs with the bank's
  key. Keep it on `127.0.0.1`, or on a network only its operator's machine joins, never on a network
  others share. Anywhere but a loopback address it asks for a password (see "Security notes").
- **It shares the signer with the engine.** The signer answers one request, then the next, so a
  wallet, or a script, that sends request after request delays the bank's votes.

At start it checks its flags and its file, not the signer, and refuses with one of:

- `-signer-url, -signer-client and -signer-secret-file go together: where the signer listens, the
  client name its config gives this wallet, and that client's secret file`
- `with -signer-url, the wallet file must hold the bank's public key, as {"publicKey": "<base64>"}: …`,
  such as `open bank-a.wallet.json: no such file or directory`: a missing file is an error, never a
  new key
- `wallet file bank-a.wallet.json holds a private key, and -signer-url names a signer: a wallet signs
  with its own key or through the signer, not both. A bank's wallet file for its signer holds the
  bank's public key only`
- `wallet file bank-a.wallet.json: its publicKey is not a public key of the network's scheme: …`
- `-signer-secret-file: the secret file desk.secret does not hold 64 lowercase hex digits, as keygen
  -secret prints them`

And a file that holds a public key only, started without the flags, gets: `wallet file
bank-a.wallet.json holds a public key only, as a bank's wallet whose key is in its signer does: name
the signer with -signer-url, -signer-client and -signer-secret-file`.

The first signature is the first time it asks the signer. When the signer says no, the page shows
why:

| The page says | What it means |
|---|---|
| `the signer answered 403 Forbidden in parameter-vote: domain "parameter-vote" is not in this client's list` | this wallet's client may not sign that: use the terminal whose client may |
| `the signer answered 401 Unauthorized in payment: unknown client` | the signer's config has no client of the `-signer-client` name |
| `the signer answered 401 Unauthorized in payment: the HMAC does not match the body under this client's secret` | the wallet's secret file is not the signer's for that client |
| `Post "http://172.28.71.254:7000/sign": dial tcp 172.28.71.254:7000: connect: connection refused` | the signer is down, or not at `-signer-url` |
| `the signer failed less than a second ago: no request until a second has passed` | within a second of a failure, every request fails at once: try again |
| `the signer's answer in payment does not verify under publicKey: the signer holds another key, or another program answered` | the wallet file's key is not the signer's |

A payment or an account update the signer refuses has still taken its block of the payment counter
(below), as one the node refuses does.

## How it works

- **One verified state per page.** Each page reads the node's `/hdls/checkpoint` once, checks it
  against the pin, and shows and acts on that state. The wallet refuses a state older than the
  newest it verified, so its node cannot roll it back to before a pending change. It keeps the
  newest state it verified, from its node or from a file, in memory, and that state stands in for
  the node's in two cases only:
  - **the node serves an older state** that the pinned banks signed, as a cache may for a few
    seconds, or a node that fell behind does. The page shows the newer state, "as of height H, read
    before", and says the node serves an older one;
  - **the kept state is a checkpoint from a file** (below), and the node's checkpoint can't be
    read: no answer, or an HTTP error status. The page shows it "as of height H, from a file".

  Any other refusal is shown as it is, even with a file loaded: a state the pinned banks did not
  sign, or one that does not match its commit. Payments and updates are then refused, so none goes
  to that node; the freeze button still works. While a file's state stands in, payments and updates
  are refused too, before anything is signed: save a payment as text instead. The wallet shows the
  node's state again as soon as it verifies and is as new. Nothing is kept across a restart: after
  one, a node that serves an older state than the newest the wallet verified is refused, with the
  reason.
- **The other banks.** A bank that colludes with a thief could hide a pending key change from its
  customer: its node keeps serving the last state from before the change, which verifies. So once a
  state verifies at block H, the main page asks each validator of that state for `/hdls/status`, at
  the public address its account publishes, all at once, waiting at most 2 seconds; an answer counts
  only if it names the pinned network id. The wallet builds each question from the address's scheme
  and host alone, follows no redirect (a redirect is no answer), and stops asking when the browser
  leaves the page. So a bank cannot point the question elsewhere: at the wallet's own page, which
  would ask the banks again, or at a device on your network. The main page is `/` alone, so a bank
  that publishes the wallet's own address gets "not found". Then it says one of:
  - **behind**, in red: banks holding more than ⅓ of the power report a block past H + 10, so the
    node may be hiding newer blocks; it lists each bank's newest block and the share of the power
    ahead. The 10 blocks are what the network commits, at the default of a block a second, while
    the wallet reads its checkpoint (at most 5 seconds) and asks (at most 2). A hidden change keeps
    the chain busy for a day, so the warning comes some 10 blocks after it is filed;
  - **unchecked**: the banks that answered hold no more than ⅔ of the power, and the page says
    which share it could check. A bank that publishes no public address counts as one that does not
    answer;
  - **aligned**, quietly: more than ⅔ answered, and the state is as current as theirs.

  Separately, the wallet remembers, while it runs, when it first saw the network's newest block (the
  newest that banks holding more than ⅓ of the power reach, or H if newer). If that has not moved
  for twice the time an idle chain takes between blocks, the network's block interval plus the
  node's 5-minute heartbeat (about 10 minutes at the default interval of a second, and longer if the
  ⅔ raised it), the page says the chain may have stalled. The heights are not signed, and need not
  be: they choose a note, never the state the page shows, which the pinned ⅔ signed. Lying nodes
  raise a false "behind" only if their banks hold more than ⅓ of the power, and make a hiding node
  look aligned only if they hold more than ⅓ too.
- **The TLS pin.** A bank may list its web server's TLS keys on its account. Once the wallet holds a
  verified state, it reaches the public address of a bank of that state, if the address is
  `https://` and the bank lists TLS keys and is not frozen, only through a server whose
  certificate's key is one of them (`tlspin.go`). It asks no certificate authority then, so the
  chain's record stands in for one. Where two banks publish one address, the key must be one that
  each lists. It holds for the wallet's own node and for the other banks' check.
  - Only a bank's record counts, a validator's: any name may list any address, so a name that is not
    a bank could otherwise list a bank's address with a key of its own.
  - The address must match as the bank wrote it on chain, `https://host[:port]`.
  - The keys are those of the newest state the wallet verified. A bank that switches its server to a
    key it listed after that state is refused by a running wallet, which then cannot read the newer
    state through that server, until it restarts. So a bank lists its next key well before it
    switches to it.
  - Before the wallet verifies a state, at its first read after each start, there is nothing to pin,
    and the usual check by certificate authority applies. So a bank keeps a certificate one signed.
    The pin guards the channel after that; the state itself is believed only when the pinned ⅔
    signed it, whatever channel it came by.
  - A server whose key is not on chain is refused: "bank-a's web server at https://bank-a.example
    shows a key that is not the one on chain: someone may stand between this wallet and the bank, or
    the bank changed its web server's key without listing the new one first". From the wallet's own
    node, the page shows it after "cannot read a verified state from the node"; from another bank,
    that bank counts as one that did not answer.
- **Its name.** The wallet's name is the account whose current key is the wallet's key. A name whose
  pending key change is to this key shows as "becomes yours at block N". With neither, the page
  says "not registered" and shows the key's fingerprint (the first 16 hex digits of the SHA-256 of
  the key) and a button that copies the full key. If the wallet held a name before and that name now
  has another key, the page says so. When no state verifies, the pages say "No verified state"
  instead, never "not registered", and the admin page does not say whether the wallet is a
  validator. When that is because the banks that sign changed, they say so, and how to install a
  new list (above).
- **Names.** A typed name is lowercased and trimmed, and the page shows the name it used. The
  accounts table shows each name's bank of record, frozen mark, balance and key fingerprint, never
  a full key.
- **Payments.** By name only. The first step shows the payee's name and its bank of record, and signs
  nothing; the second signs the payment (`ledger.SignPayment`) and POSTs it to `/hdls/pay`. The
  wallet shows "verified ✓" only for a receipt that names a pinned validator, verifies under its
  pinned key, and acknowledges this payment. So a receipt from a bank that re-keyed shows ✓ once
  the wallet has read a state signed since the re-key, which moved the pin to its new key.
- **A payment saved as text.** The second step can instead *Sign and save as text*: the wallet signs
  the payment after the same checks, from the same counter, and sends nothing
  (`Wallet.SavePayment`). It then sends the browser to `/send`, which shows the payment saved last
  as a one-message bundle. Reloading that page, or going Back and Forward, shows the same payment
  and signs nothing. The text is two lines:

  ```
  {"bundle":1,"networkId":"village/f41a2488…","from":"alice","heightInProgress":1234}
  {"type":"payment","body":{…}}
  ```

  The header names the pinned network id, the wallet's name and the height in progress of the
  page's state. The second line's body is the signed payment as `/hdls/pay` takes it. Anyone can
  carry the text to any bank, and the bank takes it in on its engine's operator port:
  `curl --data-binary @payment.txt http://10.0.0.2:8080/hdls/bundle`. The answer counts the payment
  as `taken` or `refused`, without the reason. The page names the block the payment is for:
  - if a bank has not taken it in before that block passes, the payment lapses and nothing is taken;
  - its block is at least `-lookahead` past the height in progress of the page's state (3 by
    default, at most 32), and a node takes no payment for a block more than 64 past its own height
    in progress, so a payment can't be signed to wait longer. When the whole network moves at the
    carrier's pace, that is many carries. When the rest of the network still runs at full speed, it
    is little: a busy chain makes a block a second by default, and a quiet one a block about every 5
    minutes. A larger `-lookahead` gives a saved payment more time. From a file's state far behind
    the network, the block may have passed already;
  - the saved payment takes a block of the counter, as a sent one does. Signing the same payment
    again, while the text may still arrive, makes a second payment for another block: both could be
    paid.
- **A checkpoint from a file.** When the node can't be reached, the main page offers a form, *Load a
  checkpoint from a file*: paste a bank's `hdls/checkpoint`, as someone copied it from its public
  folder (the fetcher serves it at `/hdls/checkpoint`). The form shows only while no state verifies,
  or while the page shows a file's state. It takes a file as large as a node takes, 64 MiB
  (`node.MaxCheckpointBytes`). A larger one is refused as "too large", whether the form or the text
  in it passes the bound, and a form that can't be read as "unreadable".
  - **What is checked:** exactly what is checked on the node's checkpoint. Pinned names holding
    more than ⅔ of the pinned power must have signed it, it must be no older than the newest state
    the wallet verified, and its validator set moves the pin as the node's would. A forged file,
    another network's, or an older one is refused with the reason.
  - **It needs a pin.** A file carries no network id to pin on first use, so a wallet that trusts
    no banks yet refuses it: open its page once at the bank first, or install the bank's list.
  - **When it stands in:** only while the node's checkpoint can't be read, or the node serves an
    older state (above). Any other refusal of the node's state is shown even with a file loaded.
  - **What the pages show:** the state "as of height H, from a file", with a note that the bank's
    node can't be reached, or its state is older, and that payments are saved as text. A payment
    saved meanwhile names its block from H.
  - **What it sends:** no payment and no update. The review step still offers *Sign and send*, but
    the wallet then refuses before it signs or takes a block: "your bank's node can't be read, or
    its state is older than your file's: save the payment as text, and hand it to whoever can reach
    a bank". An update gets the same, ending "try the update again later". *Sign and save as text*
    and the freeze button work.
  - **What stays:** the file is kept in memory only, and a restart asks the node again. But like the
    node's state, it moves the newest height the wallet verified, and may move the pin: while the
    wallet runs, the file's state stands in for a node that serves an older one, and after a
    restart such a node is refused as older.
- **Account form.** A description; the name your balance backs as stake (a delegate); a public
  address (a bank's public face, or a website); up to 4 IP addresses, separated by spaces, which
  replace the whole list; and up to 2 TLS keys, separated by spaces, which replace the whole list
  too. A validator's IP addresses are where the others send it gossip, on the network's gossip port,
  and the addresses the others take its gossip from (the allow list). A blank field is left as it
  is; `-` clears it.
  - A public address is `http://` or `https://`, a host in lowercase and an optional `:port`, nothing
    after it (for example `https://bank-a.example`, never `https://bank-a.example/`).
  - An IP address is written in its one plain form: `10.0.0.2`, or IPv6 in its shortest lowercase
    form such as `2001:db8::1`; no zone, no IPv4 address written as IPv6, no `0.0.0.0`, `::` or
    multicast address, none twice.
  - A TLS key is the SHA-256 of the web server's key (its SubjectPublicKeyInfo), as 64 lowercase hex
    digits: the current key and the next, so the server can change keys without a gap. The runbook
    gives the `openssl` command
    ([operating-a-network.md](../docs/operating-a-network.md#the-genesis-file)).
  - The wallet and every node refuse another form with the reason, as the ledger does.
  - The form says what it publishes is public. It stays public: anyone may have kept a copy, even
    after it is cleared.
- **The freeze button.** After a confirm step it signs a freeze of the wallet's own name and posts it
  to `/hdls/name-request`. It names the block `height + lookahead`, from the verified state's height
  in progress, or the name's pending change's block if that is sooner, so the freeze still cancels
  the change. It uses no block of the payment counter. While a state read before, or a file's,
  stands in for the node's, the chain may have gone past it: the height in progress is then the
  larger of that state's height + 1 and the height the node reports. When no state verifies, it
  freezes the last name the wallet verified as its own, at the height its node reports, the only one
  it has then. The page shows the freeze as sent until a verified state shows the name frozen. It
  says the freeze did not land only once a verified state reaches the freeze's block without the
  name frozen. An idle chain still commits a block about every 5 minutes (the node's heartbeat,
  after the block interval), so a freeze the node dropped or never took shows as not landed within
  about lookahead heartbeats.
- **The Requests page** (`/requests`): registrations, key changes and bank changes, in three steps.
  *Draft* fills in what the state says, names a block `lead` blocks ahead (120 by default, at most
  3,600), shows the request as text, and signs nothing. *Sign* takes request text, checks it against
  the state, and signs every role this wallet holds: requestor, bank, or new key. *Submit* posts a
  complete request. A request travels between wallets as text; no node route signs anything. The
  page offers the wallet's hash name: `k-` and the first 15 bytes of the key's SHA-256 in lowercase
  base32, 26 characters in all, a convention the protocol knows nothing of.
- **The bank's desk** is the Requests page of the bank operator's wallet, which holds the bank's one
  key. It also lists the pending changes on the bank's customers. Its Sign step shows a request's
  facts once it has signed, and sends nothing: before the bank submits or hands on the text, it
  compares the new key's fingerprint there with the one the new key's own wallet page shows. A
  request's *Submit* posts to `/hdls/name-request`, which a public face passes on, so the desk works
  against either address.
- **The admin page** (`/admin`) drafts a parameter change (all ten parameters) or a ⅔ name action,
  shows what it binds, signs the vote and prints it as text; *Submit certificate* assembles pasted
  votes and submits them once they reach the quorum, on the operator port only (above). A name
  action with a new key goes to the new key's wallet first, which signs the proof; the validators
  then sign. A wallet whose name alone is a quorum, as in the demo, submits at once.

## The payment counter

Every payment, sent or saved as text, and every account update names a block of its own, strictly
after the last one this wallet named, so the wallet never puts two signed payments for one block into
the world, whatever a node reports. Its `height` is the height in progress of the verified state,
never the one a node reports on `/hdls/status`: validators signed that state, and the wallet refuses
one older than the newest it verified, so a node that lies can only make a payment miss its block. A
checkpoint from a file is such a state too. The counter skips the blocks of requests this wallet pays
the fee for, since those payments would lapse. It never runs more than 32 blocks past
`height + lookahead`.

Known behaviour:

- A block is used up before the payment or update goes out, or as the payment is saved as text, so
  one the node refuses, or never receives, still uses its block. The wallet first makes a node's
  intake checks against its verified state (the ledger's
  `PaymentFitsState`, `CreditFitsBalanceCap` and `UpdateFitsState`), and one that fails them uses no
  block. Once this wallet's payments, updates and requests name every block up to 32 past
  `height + lookahead`, it refuses to sign: "wait for the chain to advance". From a fresh counter on
  an idle chain, that is after 33 refusals by the node in a row, fewer if earlier payments or
  requests still name blocks ahead. A restart does not clear it. As the chain advances, the wallet
  signs again; any payment or update anyone makes moves it, and an idle chain's heartbeat moves it
  a block about every 5 minutes. Giving the block back on a refusal would break the counter's rule.
- A request drafted with a lead under about 64 blocks can name a block one of this wallet's payments
  already names. The node then refuses the request (`409`, with the reason) or the payment lapses.

## Running as a service

The passphrase is read from **`GOLEDGER_WALLET_PASSPHRASE`** when it is set, and otherwise asked on a
terminal. When stdin is not a terminal (systemd, nohup, a container) and the variable is not set, a
*new* key is stored unencrypted, and a sealed key refuses to open with a hint to set the variable. A
plaintext key is sealed in place at the first start that has a passphrase; until then the wallet
warns on every start.

    Environment=GOLEDGER_WALLET_PASSPHRASE=…   # systemd unit

## Running in a container

`make images` builds the wallet's image, `go-ledger-ui` (`deploy/ui.Dockerfile`): one static binary in
an empty image, run as the numeric user 65532. It listens on `:8090` in the container, where any
other container on its network reaches it, so it asks for a password, from a file you mount, and
keeps its three files in `/wallet`:

    docker run -p 127.0.0.1:8090:8090 -v wallet:/wallet -v "$PWD/wallet.password":/wallet.password:ro \
        go-ledger-ui -node https://bank-a.example -network village -password-file /wallet.password

- `wallet.password` holds the password on one line; make one with
  `head -c 18 /dev/urandom | base64 > wallet.password`, before the `docker run`, or Docker makes a
  folder of that name. The container's user, 65532, must be able to read it: keep it in a folder
  only you can open, and make the file itself readable (`chmod 644 wallet.password`). The browser
  asks for the password once, under any user name.
- Publish the port on this machine only (`127.0.0.1`), and under the same number, 8090: the wallet
  refuses a request for another port (see "Security notes").
- A container has no terminal unless it is started with `-it`. Without one, pass the passphrase in
  `GOLEDGER_WALLET_PASSPHRASE` (`-e`), or a new key is stored unencrypted, as above.

With the quickstart (`make quickstart`, four banks on one machine), the banks' public addresses are
their fetchers' names on the compose file's network, such as `http://bank-a-fetcher:8080`. A wallet
on the machine itself reaches its bank's fetcher at `http://127.0.0.1:18081` to `18084`, as the
commands `cmd/quickstart` prints show, but cannot reach those names. It asks every bank at its
public address, its own bank's included, so its check of the other banks says it could check banks
holding 0% of the power. A wallet run in a container on that network checks them all;
[operating-a-network.md](../docs/operating-a-network.md#four-banks-on-one-machine-the-quickstart)
gives the command.

The quickstart also runs each bank's own wallet, in a container that signs through the bank's
signer, at `http://127.0.0.1:18091` to `18094`. It asks for the password the quickstart wrote for it,
such as `deploy/quickstart/bank-a.wallet.password`, under any user name. It is on the Compose
network, so it checks all four banks.

## Security notes

- The wallet answers only requests addressed to it: a `Host` naming the `-listen` host, `localhost`
  or a loopback address on the `-listen` port, or, when it listens on every interface, any IP
  address on that port. So it can be reached across the LAN by IP, but not under a host name another
  page controls (DNS rebinding). A form must come from the wallet's own page (its `Origin` or
  `Referer`), and no other page can frame it.
- That guard stops another page in the browser, never a program on the network, which sends the
  headers the guard wants. On a loopback IP address the wallet asks for nothing more, so keep it on
  `127.0.0.1`. Listening anywhere else, such as `:8090` in a container, or on a host name, even
  `localhost`, which the hosts file or DNS resolves, it refuses to start without `-password-file`,
  a file holding a password, and asks for that password, under any user name, in the browser's own
  login box, behind the guard. Over plain http the password crosses the network as it is: never let
  others watch the wallet's traffic.
- The "copy my full key" button uses the browser's clipboard, which browsers allow only in a secure
  context. `localhost` and `127.0.0.1`, the default `-listen`, work. A wallet opened by LAN address
  over plain http cannot copy its full key, and a key change needs the new wallet's full key.
- Amounts are typed as plain decimals with at most 8 decimal places (`10`, `10.5`, `.5`); anything
  else is refused before anything is signed.
- The wallet reads its state through one node. It refuses an older state, reports a freeze that
  did not land, and asks the other banks for their heights, so a node that hides newer blocks is
  caught as long as banks holding more than ⅓ of the power answer. That needs the banks to publish
  their public addresses on their accounts. Asking them shows each bank's public face, and the CDN
  in front of it, this machine's address.
- A checkpoint from a file is checked as the node's is, so whoever carried it can delay the state
  the wallet shows, never forge it.
- A first use through a public address trusts whatever banks that address and its CDN serve. Give
  customers the bank's list, or have their first use at the bank (see "Its files").
- A bank's wallet that signs through its signer signs as the bank. Reach it from the operator's
  machine only, never from a network others share, and beyond a loopback address give it its password (see "A
  bank's wallet through its signer").
- One account per wallet: a multi-account wallet is not planned (spec §1 lists it as a non-goal).
  A pair from `keygen` goes in only by writing the key file before the first start (above); there
  is no import or export page.
