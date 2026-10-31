# go_ledger_ui — wallet

Area: **UI**. A local wallet for a go_ledger network. It holds your key on your own machine, shows
your name, your balance and the accounts on the ledger, pays by name, freezes your name, and drafts
and signs name requests and governance votes with simple forms. Code: `ui/`, with its pages in
`ui/templates/`.

It is a separate program and Go module from the node. It lives under `ui/` and can move to its own
repository once the root module is published: drop the `replace` in `go.mod` and require that
version. The guide for the people who use it is [USER_MANUAL.md](USER_MANUAL.md).

## Build and run

    go build -o wallet .
    ./wallet -node http://<a-node>:8080 -network village -wallet wallet.json -listen 127.0.0.1:8090

Then open <http://127.0.0.1:8090> in a browser. From the repository root, `make build` builds it as
`bin/go_ledger_ui`.

| Flag | Meaning | Default |
|---|---|---|
| `-node` | the base URL of a node, the wallet's reference node | `http://127.0.0.1:8080` |
| `-network` | the network's name; the network id the node serves must start with it and `/` | `village` |
| `-wallet` | the key file; created on first run if absent | `wallet.json` |
| `-listen` | where the wallet serves its pages (keep it local) | `127.0.0.1:8090` |
| `-lookahead` | how many blocks ahead a payment or a freeze names, at most 32: a burst of payments runs up to 32 blocks further, inside the node's 64-block window | `3` |

## Its files

The wallet keeps three files side by side. Back them up and restore them together.

- **`wallet.json`, the key file.** `publicKey`, the key the seed derives, and the 32-byte seed:
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
  its commit, and pins both. So open the wallet's page for the first time at your bank, or put your
  operator's list in the file before the wallet starts. Afterwards the wallet believes a state only
  when pinned names holding more than ⅔ of the pinned power signed it, each under its pinned key,
  and it signs for the pinned network id only. The pin never updates itself: when banks holding a
  third or more of the pinned power are frozen or re-key, no state verifies until the bank's new
  list replaces the file and the wallet is restarted: it reads the file only at start. A pin with
  no network id is an earlier network's.

  A bank's list is the `.trust` its own wallet pinned against its own node. After the banks re-key,
  the bank deletes that file, restarts its wallet, opens its page once, and hands out the new file.
- **`wallet.json.state`, the sidecar**, kept per network name: the payment counter
  (`lastPaymentBlockID`), the blocks of the requests this wallet pays for (`requestBlockIDs`), the
  last name and the newest height it verified (`lastName`, `newestHeight`), and the freeze it last
  sent (`freezeName`, `freezeBlockID`). Started on another network name, the wallet begins a fresh
  one.

## How it works

- **One verified state per page.** Each page reads the node's `/hdls/checkpoint` once, checks it
  against the pin, and shows and acts on that state. The wallet refuses a state older than the
  newest it verified, so its node cannot roll it back to before a pending change.
- **Its name.** The wallet's name is the account whose current key is the wallet's key. A name whose
  pending key change is to this key shows as "becomes yours at block N". With neither, the page
  says "not registered" and shows the key's fingerprint (the first 16 hex digits of the SHA-256 of
  the key) and a button that copies the full key. If the wallet held a name before and that name now
  has another key, the page says so. When no state verifies, the pages say "No verified state"
  instead, never "not registered", and the admin page does not say whether the wallet is a
  validator.
- **Names.** A typed name is lowercased and trimmed, and the page shows the name it used. The
  accounts table shows each name's bank of record, frozen mark, balance and key fingerprint, never
  a full key.
- **Payments.** By name only. The first step shows the payee's name and its bank of record, and signs
  nothing; the second signs the payment (`ledger.SignPayment`) and POSTs it to `/hdls/pay`. The
  wallet shows "verified ✓" only for a receipt that names a pinned validator, verifies under its
  pinned key, and acknowledges this payment. So a receipt from a bank that re-keyed since the pin
  shows no ✓ until the wallet takes the bank's new list.
- **Account form.** A description, the name your balance backs as stake (a delegate), and, for a
  validator, its gossip address. A blank field is left as it is; `-` clears it.
- **The freeze button.** After a confirm step it signs a freeze of the wallet's own name and posts it
  to `/hdls/name-request`. It names the block `height + lookahead`, from the verified state's height
  in progress, or the name's pending change's block if that is sooner, so the freeze still cancels
  the change. It uses no block of the payment counter. When no state verifies, it freezes the last
  name the wallet verified as its own, at the height its node reports, the only one it has then. The page shows the freeze as sent until a verified state shows
  the name frozen. It says the freeze did not land only once a verified state reaches the freeze's
  block without the name frozen. So on an idle chain, a freeze the node dropped or never took can
  stay "sent, not confirmed".
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
  compares the new key's fingerprint there with the one the new key's own wallet page shows.
- **The admin page** (`/admin`) drafts a parameter change (all ten parameters) or a ⅔ name action,
  shows what it binds, signs the vote and prints it as text; *Submit certificate* assembles pasted
  votes and submits them once they reach the quorum. A name action with a new key goes to the new
  key's wallet first, which signs the proof; the validators then sign. A wallet whose name alone is
  a quorum, as in the demo, submits at once.

## The payment counter

Every payment and account update names a block of its own, strictly after the last one this wallet
named, so the wallet never puts two signed payments for one block into the world, whatever a node
reports. Its `height` is the height in progress of the verified state, never the one a node reports
on `/hdls/status`: validators signed that state, and the wallet refuses one older than the newest it
verified, so a node that lies can only make a payment miss its block. The counter skips the blocks
of requests this wallet pays the fee for, since those payments would lapse. It never runs more than
32 blocks past `height + lookahead`.

Known behaviour:

- A block is used up before the payment or update goes out, so one the node refuses still uses its
  block. The wallet first makes a node's intake checks against its verified state (the ledger's
  `PaymentFitsState`, `CreditFitsBalanceCap` and `UpdateFitsState`), and one that fails them uses no
  block. Once this wallet's payments, updates and requests name every block up to 32 past
  `height + lookahead`, it refuses to sign: "wait for the chain to advance". From a fresh counter on
  an idle chain, that is after 33 refusals by the node in a row, fewer if earlier payments or
  requests still name blocks ahead. A restart does not clear it. As the chain advances, the wallet
  signs again; any payment or update anyone makes moves it. Giving the block back on a refusal would
  break the counter's rule.
- A request drafted with a lead under about 64 blocks can name a block one of this wallet's payments
  already names. The node then refuses the request (`409`, with the reason) or the payment lapses.

## Running as a service

The passphrase is read from **`GOLEDGER_WALLET_PASSPHRASE`** when it is set, and otherwise asked on a
terminal. When stdin is not a terminal (systemd, nohup, a container) and the variable is not set, a
*new* key is stored unencrypted, and a sealed key refuses to open with a hint to set the variable. A
plaintext key is sealed in place at the first start that has a passphrase; until then the wallet
warns on every start.

    Environment=GOLEDGER_WALLET_PASSPHRASE=…   # systemd unit

## Security notes

- The wallet answers only requests addressed to it: a `Host` naming the `-listen` host, `localhost`
  or a loopback address on the `-listen` port, or, when it listens on every interface, any IP
  address on that port. So it can be reached across the LAN by IP, but not under a host name another
  page controls (DNS rebinding). Anyone who can reach it can use it: it has no login, so keep it on
  `127.0.0.1`. A form must come from the wallet's own page (its `Origin` or `Referer`), and no other
  page can frame it.
- The "copy my full key" button uses the browser's clipboard, which browsers allow only in a secure
  context. `localhost` and `127.0.0.1`, the default `-listen`, work. A wallet opened by LAN address
  over plain http cannot copy its full key, and a key change needs the new wallet's full key.
- Amounts are typed as plain decimals with at most 8 decimal places (`10`, `10.5`, `.5`); anything
  else is refused before anything is signed.
- The wallet sees the network through one node. It refuses an older state and reports a freeze that
  did not land, but a node that hides a pending change can only be caught by reading another node.
- One account per wallet: a multi-account wallet is not planned (spec §1 lists it as a non-goal).
  A pair from `keygen` goes in only by writing the key file before the first start (above); there
  is no import or export page.
