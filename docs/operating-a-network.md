# Operating a network

Area: **API/CDN**: running `cmd/server`, the engine, and `cmd/fetcher`, its public face, by hand or
in containers. Code: `cmd/server/`, `cmd/fetcher/`, `cmd/demo/`, `cmd/quickstart/` and `deploy/`.
It also covers `cmd/keygen/` and `cmd/signer/`, of the signature area, and the genesis file,
`ledger/genesis.go`, of the node area. The routes the server serves are in [api.md](api.md); keys,
the sign log and the signer's rules in [signature.md](signature.md).

## Build

Requires Go 1.27 or later.

```
go build -o bin/server ./cmd/server
go build -o bin/signer ./cmd/signer
go build -o bin/fetcher ./cmd/fetcher
go build -o bin/keygen ./cmd/keygen
go test ./...            # the suite; add -race as CI does
```

Each command is one binary that needs nothing installed to run. `make build` builds all six (the
server, the signer, the fetcher, the key generator, the demo and the wallet) into `bin/` with
`CGO_ENABLED=0`, so each is static and runs on any Linux of the same architecture. A plain
`go build` on a machine with a C compiler links each of them against the system's C library instead;
add `CGO_ENABLED=0` (for example `CGO_ENABLED=0 go build -o bin/server ./cmd/server`) to keep them
static. `make images` builds the same programs into container images (see
[the quickstart](#four-banks-on-one-machine-the-quickstart)).

## Generate keys

Every node needs an ML-DSA-65 key pair, an observer included, since every node signs a receipt for
each payment a wallet posts to it (`/hdls/pay`): in its config, or in the signer its config names
(see [Running a signer](#running-a-signer)). So does every genesis account. Generate each pair on
the machine that will hold it; the private key never leaves that machine.

```
$ ./bin/keygen
publicKey:  76kiA96VYFo6sp0MhkT+rh97sh/5hUYTEE8vSYCUhpWv/qYY…   (2604 characters)
privateKey: 6KAw6+2LUXVFz4FVvvpX2Lx/lpKyXKfVd1+xu7aAORQ=
```

The private key is the 32-byte seed the key derives from. The public key goes into the genesis file
as the key of a name; the name is the identity. People who join later make their key in their
wallet and register it at their bank (see the [user manual](../ui/USER_MANUAL.md)).

A bank's key for its signer is made sealed, on the signer's machine, under a passphrase kept in a
file. `keygen` prints the public key only, and writes the key file, which it never replaces:

```
$ ./bin/keygen -encrypt bank-a.key.json -passphrase-file bank-a.passphrase
76kiA96VYFo6sp0MhkT+rh97sh/5hUYTEE8vSYCUhpWv/qYY…   (2604 characters)
```

Each of the signer's clients needs a secret of its own, which `keygen -secret` prints: 64 lowercase
hex digits. The signer and that client each get a copy of the file:

```
$ ./bin/keygen -secret > engine.secret
```

A wallet can hold a pair from `keygen`. Before the wallet's first start, write its key file
(`wallet.json` by default) with the two values `keygen` printed:

```json
{"publicKey": "<publicKey>", "privateKey": "<privateKey>"}
```

The wallet then seals the key with a passphrase at the first start that has one (see
[`ui/README.md`](../ui/README.md#its-files)). A genesis account's holder can do the same, or start a
wallet once before the network exists and give the genesis writer the `publicKey` from its key file.

The bank's wallet is its desk and has its freeze button, and it signs with the key the bank's node
votes with. With a signer, it holds no key: its wallet file holds the bank's public key only,
`{"publicKey": "<publicKey>"}`, and it signs through the signer (see
[`ui/README.md`](../ui/README.md#a-banks-wallet-through-its-signer)). Without one, the bank's
operator holds the bank's pair in a wallet file as above, and runs that wallet on the bank's machine.

A node's config without a signer holds its private key in plaintext, and so does a key file written
by hand until the wallet seals it. Make each readable by its owner only: `chmod 600 node.json
wallet.json`. Do the same for each client's secret file: it is what lets a program ask the signer.

## The genesis file

Every node of a network reads the same genesis file, byte for byte (`genesisPath` in its config).
`ledger.ParseGenesis` reads it and `ledger.ValidateGenesis` checks it.

| Field | Meaning |
|---|---|
| `networkName` | the readable half of the network id; a name |
| `gossipPort` | the one port every validator gossips on, at each of its IP addresses: from 1 to 65535, required, with no default. 8070 is the usual choice: it is clear of the operator port's usual 8080 and the wallet's 8090 |
| `parameters` | the ten parameters, every one given (table below) |
| `validators` | static mode: `[{"name": "...", "power": N}]`; empty in dynamic mode |
| `validatorThreshold` | dynamic mode: the stake, as an amount, a name needs to be seated; `""` in static mode |
| `accounts` | `[{"name", "publicKey", "amount", "bank", "delegate", "description", "publicAddress", "ipAddresses", "tlsKeys"}]` |

| Parameter | Default | Note |
|---|---|---|
| `transactionCost` | `"0.1"` | burned from each payment |
| `blockIntervalMs` | `1000` | the wait between a commit and the next height |
| `stepTimeoutMs` | `3000` | the base step timeout, and the re-gossip interval |
| `maxPeers` | `0` | 0 gossips to every peer |
| `maxMessageBytes` | `6400` | the largest payment or update JSON; fits the largest payment, with every comment byte escaped. It follows the scheme's signature size: 6400 is ML-DSA-65's |
| `newKeyFee` | `"1"` | the fee of a registration, key change or bank change |
| `changeDelayBlocks` | `86400` | about a day at one block a second |
| `retainBlocks` | `604800` | how long the signature record keeps a block |
| `maxPaymentAmount` | `"100000"` | keep it at or above `transactionCost`: nothing refuses a lower value, and then no payment can pass until a vote raises it |
| `maxAccountBalance` | `"10000000"` | the largest balance a payment or the ⅔ may raise a name to |

Amounts are strings: plain decimals with at most 8 decimal places. The keys are in lower camel case;
the parameters' bounds are in [governance.md](governance.md#the-parameters).

`ValidateGenesis` refuses the file, naming the field to fix, if: a name is not canonical (1 to 32
lowercase letters, digits and hyphens, starting and ending with a letter or a digit) or is given
twice; the gossip port is not from 1 to 65535; a key is not an ML-DSA-65 key in canonical base64, or
is given twice; an amount does not parse or is above `maxAccountBalance` (zero is allowed); a
public address or an IP address is not in its one plain form (below); a delegate is not an account;
a bank is the account's own name or not a name the genesis seats; both or neither of `validators`
and `validatorThreshold` are set; a static validator is not an account, is listed twice, or has
power below 1 or above 2^40 (`consensus.MaxValidatorPower`); the threshold is not above 0; the
genesis seats no name; or the parameters fail the bounds a vote is held to. `ParseGenesis` refuses a
missing parameter and any unknown key.

```json
{
  "networkName": "village",
  "gossipPort": 8070,
  "parameters": {
    "transactionCost": "0.1", "blockIntervalMs": 1000, "stepTimeoutMs": 3000, "maxPeers": 0,
    "maxMessageBytes": 6400, "newKeyFee": "1", "changeDelayBlocks": 86400,
    "retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000"
  },
  "validators": [{ "name": "bank-a", "power": 1 }, { "name": "bank-b", "power": 1 },
                 { "name": "bank-c", "power": 1 }, { "name": "bank-d", "power": 1 }],
  "validatorThreshold": "",
  "accounts": [
    { "name": "bank-a", "publicKey": "<bank-a's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-a", "description": "", "publicAddress": "https://bank-a.example",
      "ipAddresses": ["203.0.113.1"] },
    { "name": "bank-b", "publicKey": "<bank-b's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-b", "description": "", "publicAddress": "https://bank-b.example",
      "ipAddresses": ["203.0.113.2"] },
    { "name": "bank-c", "publicKey": "<bank-c's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-c", "description": "", "publicAddress": "https://bank-c.example",
      "ipAddresses": ["203.0.113.3"] },
    { "name": "bank-d", "publicKey": "<bank-d's publicKey>", "amount": "1000", "bank": "",
      "delegate": "bank-d", "description": "", "publicAddress": "https://bank-d.example",
      "ipAddresses": ["203.0.113.4"] },
    { "name": "alice", "publicKey": "<alice's publicKey>", "amount": "50", "bank": "bank-a",
      "delegate": "", "description": "", "publicAddress": "", "ipAddresses": [] }
  ]
}
```

A static validator signs with its account's key. It gossips at its account's IP addresses, on the
network's gossip port, and its public face is its account's public address. Any name may publish
both: see [governance.md](governance.md#names-as-a-name-service).

- **A public address** has one plain form: `http://` or `https://`, a host in lowercase, and an
  optional `:port`, with nothing after it, as above. The genesis and every account update refuse
  anything more, a trailing `/` included: nodes and wallets send requests to it, and a path or a
  query in it would turn those into requests for something else. A bank's public address is its
  CDN's name.
- **IP addresses** are at most 4 (`ledger.MaxIPAddresses`), IPv4 or IPv6, none twice, each in the
  one form Go's `netip` prints it: `203.0.113.1`, `2001:db8::1`, never `2001:DB8::1` or an IPv4
  address in IPv6 dress (`::ffff:203.0.113.1`), and with no zone. `0.0.0.0`, `::` and multicast
  addresses are refused: none names one machine. Private and loopback addresses are taken, so a
  network on a LAN, the quickstart and the tests work. A network on the internet lists public
  addresses. Which addresses a bank lists is in [Network exposure](#network-exposure).
- **TLS keys** are at most 2 (`ledger.MaxTLSKeys`), the web server's current key and the next: each
  the SHA-256 of the key's SubjectPublicKeyInfo, as 64 lowercase hex digits, the hash DANE's TLSA
  records call "3 1 1". When a bank lists them, a wallet takes from the bank's web server only a
  certificate whose key is one of them (see [The public face](#the-public-face)). For a
  certificate in `cert.pem`:
  `openssl x509 -in cert.pem -pubkey -noout | openssl pkey -pubin -outform der | sha256sum`.

Once every validator has an IP address, as here, validators take their peers from the chain as they
open, and push nothing to a node that is not a validator (see
[api.md](api.md#how-messages-travel)). A genesis name may have no bank of record (`""`); such a
name changes its key only by the ⅔, and can get a bank by a bank change.

## The network id

The effective network id is `networkName`, a `/`, and the 64 hex digits of the genesis hash, for
example `village/f41a2488…`. The genesis hash covers the name, the gossip port, the genesis state
with its parameters, and the validator rule. Every signature binds the id; `GET /hdls/status` serves
it, the server logs it at start, and a wallet pins it on first use.

## The node config

Each node reads its own JSON config (`server -config node.json`).

| Field | Meaning |
|---|---|
| `genesisPath` | the genesis file, the same on every node |
| `listen` | the operator port: every route (see [api.md](api.md#two-ports-and-the-public-face)), for the bank's own machines. For example `"127.0.0.1:8080"`, or an address on the bank's own network. If empty: `"127.0.0.1:8080"` when `gossipListen` is set, and `":8080"`, every address of the machine, when it is not, as a trial on one machine needs. Never publish it |
| `gossipListen` | the gossip port, which the other validators reach, behind the allow list: `":"` and the genesis's `gossipPort`, such as `":8070"`. `""`: no gossip port, and `listen` serves the gossip routes too, to anyone, with no allow list, as a trial on one machine needs |
| `allow` | IP addresses or prefixes the gossip port always lets in, besides the current validators' addresses, such as `["198.51.100.7", "203.0.113.0/24", "2001:db8::/48"]`. Each written as the port compares it: an IPv4 address as IPv4, no zone, and no bit set past a prefix's length. Refused without `gossipListen` |
| `name` | this node's account name; `""` for an observer |
| `publicKey` | the node's public key, as `keygen` prints it. Every signature the node makes, or its signer answers, must verify under it |
| `privateKey` | the private key, as `keygen` prints it, for a key in the engine's own process. The node derives the public key from the seed, refuses a pair that does not match, and makes one test signature at start. Set this or `signer`, not both |
| `signer` | the signer that holds the key, as `{"url": "http://172.28.71.254:7000", "client": "engine", "secretFile": "engine.secret"}`: where it listens, the client name its config gives the engine, and that client's secret file. At start the engine makes its test signature through it, so the signer must be up (see [Running a signer](#running-a-signer)) |
| `peers` | base URLs of the other validators' gossip ports, for example `["http://203.0.113.2:8070"]`. A validator uses them until every validator has an IP address on chain; an observer, and the only validator of a network, keep them |
| `bootstrap` | base URLs to fetch a verified checkpoint from at start: banks' public addresses, such as `"https://bank-b.example"`, or gossip ports that let this node in. The running catch-up asks them too, every 3 s or every 5 block intervals if that is longer, with its gossip peers and every other validator's public address (see [A joining node](#a-joining-node)). Empty on a new network |
| `snapshotPath` | where the node keeps its snapshot; its sign log and record files go beside it. Required on a node whose name is a static validator, and on every node in dynamic mode |
| `backupPath` | optional: an off-machine copy of the snapshot, for example on a mounted share; never the sign log; one write at a time. The rename is atomic only on a local file system: on NFS or CIFS a mirror can fail, and a failed mirror is ignored, with no log line |
| `publicPath` | the public folder, which the engine writes for the fetcher (see [architecture.md](architecture.md#the-public-folder)); `""`: none. The engine creates it |
| `minimumStepTimeoutMs` | the slow-down switch, for when the network splits (see [the slow-down switch](#the-slow-down-switch)): from 0, off, to 86,400,000, a day. Read at start |
| `maxMempool` | the cap on each pool of payments, updates and name requests; 10,000 if 0 |
| `maxCommentLength` | the cap on a payment's comment, in bytes; 280 if 0 |
| `zone` | the name service's zone, such as `"hdls.example.org"`: the engine then writes the zone file `hdls/zone` into `publicPath`, which it needs. At most 208 characters. `""`: no zone file (see [The name service](#the-name-service)) |
| `zoneNameServers` | the zone's name servers, its NS records, such as `["ns1.example.net"]`: at least one, each outside the zone. The first is the SOA's primary |
| `zoneContact` | the SOA mailbox, as a domain name: `"hostmaster.example.org"` for hostmaster@example.org |
| `zoneTimeToLive` | how long, in seconds, a resolver may keep a record; 60 if 0 |

The node refuses an unknown key, so a misspelt field, or one that now lives in the genesis file,
never passes silently. It also refuses an `allow` entry it can't read, a switch out of its range,
both or neither of `privateKey` and `signer`, and a zone setting without a `zone`. With an IP
address on every validator's genesis account, a validator gossips to the
validators from the start: to every other one with `maxPeers` 0, and only to its topology
neighbours with `maxPeers` above 0.

```json
{
  "genesisPath": "genesis.json",
  "listen": "127.0.0.1:8080",
  "gossipListen": ":8070",
  "name": "bank-a",
  "publicKey": "<bank-a's publicKey>",
  "privateKey": "<bank-a's privateKey>",
  "bootstrap": [],
  "snapshotPath": "data/snapshot.json",
  "publicPath": "data/public"
}
```

With a signer, `signer` takes the place of `privateKey`:

```json
  "publicKey": "<bank-a's publicKey>",
  "signer": {"url": "http://172.28.71.254:7000", "client": "engine", "secretFile": "engine.secret"},
```

Each bank's config differs in `name` and the key pair. It needs no `peers`: the genesis gives every
validator an IP address. The server reads `genesisPath`, `snapshotPath`, `backupPath`,
`publicPath` and the signer's `secretFile` relative to the folder it starts in, not to the config
file. It does not create the
snapshot's folder: create it first. Without it the node refuses to start, and its error names the
missing folder. In the folder that holds `node.json` and `genesis.json`:

```
mkdir -p data
./bin/server -config node.json
```

Its first lines name the network, and say which port serves what:

```
hypernode node up: network="village/b93a37e6…" name="bank-a" listen=127.0.0.1:8080 peers=0 validators=4 validatorPower=4
gossip port: gossipListen=:8070 serves the gossip routes to the current validators' IP addresses and 0 allow entries
```

Without `gossipListen`, the second line says instead that the gossip routes are open to anyone.

With a gossip port, two more lines can follow. Each warns of a setting that is allowed, but most
often a mistake:

```
listen=:8080 is on every interface: the operator port serves every route, payments and bundle and checkpoint imports among them, with no allow list, so it must not be reachable from outside the bank's own network
gossipListen=:8071 is not on the genesis gossipPort 8070, where the other validators send: they reach this node only if a port mapping in front forwards 8070 to it
```

- **The first** comes when `listen` names no host, or `0.0.0.0` or `::`. Keep that port behind the
  machine's firewall, or give `listen` a host. The quickstart's engines print it: their operator
  ports serve their fetchers on the compose network.
- **The second** comes when the port of `gossipListen` is not the genesis's `gossipPort`. It is not
  refused, since a port mapping in front may forward the one to the other.

With at least 3 of the 4 online, the network commits block 1 and then idles until there is work,
with an empty block about every five minutes meanwhile (the idle heartbeat). A single-validator
network commits on its own, which is handy for testing.

## Running a signer

The signer (`cmd/signer`) holds the bank's key, and signs for the bank's engine and wallet, its
clients. What it guards, its rules and its two logs are in
[signature.md](signature.md#the-signer). Run one per bank, on a machine or in a container of its
own, on the bank's private network: the bank's engine and wallet reach it, and nothing else does.
Never publish its port.

```
./bin/signer -config signer.json
```

| Field | Meaning |
|---|---|
| `listen` | the address it serves on, on the bank's private network, such as `":7000"` |
| `allowFrom` | the IP addresses or prefixes it takes connections from: its clients' addresses, such as `["172.28.71.0/24"]`, written as the node config's `allow` is. It closes any other connection before reading a byte of it. Required |
| `genesis` | the genesis file, the engine's: its hash makes the network id the signer signs for |
| `name` | the bank's name, the one name it signs in |
| `keyFile` | the key file `keygen -encrypt` wrote (see [Generate keys](#generate-keys)) |
| `passphraseFile` | the file that holds the key file's passphrase. `""` for a plaintext key file, which the signer takes, for a test, with a warning in its log |
| `signLogPath` | its sign log, in a folder of its own |
| `auditLogPath` | its audit log |
| `clients` | each client: its `name`, its `secretFile`, from `keygen -secret`, and its `domains`, such as `{"name": "engine", "secretFile": "engine.secret", "domains": ["block-revision", "proposal", "receipt"]}` |

The signer reads its paths relative to the folder it starts in. It refuses an unknown key, an empty
`allowFrom` or `clients`, a client named twice, two clients with one secret, and a domain it signs
nothing in, such as `name-request-new-key`. Which domains to give each client is in
[signature.md](signature.md#its-clients-and-their-domains): never `key-change` or `bank-change`.

```json
{
  "listen": ":7000",
  "allowFrom": ["172.28.71.0/24"],
  "genesis": "genesis.json",
  "name": "bank-a",
  "keyFile": "bank-a.key.json",
  "passphraseFile": "/run/secrets/bank-a-passphrase",
  "signLogPath": "signer-logs/sign-log.jsonl",
  "auditLogPath": "signer-logs/audit-log.jsonl",
  "clients": [
    {"name": "engine", "secretFile": "engine.secret", "domains": ["block-revision", "proposal", "receipt"]},
    {"name": "desk", "secretFile": "desk.secret", "domains": ["registration", "name-request-bank", "freeze"]},
    {"name": "admin", "secretFile": "admin.secret",
     "domains": ["parameter-vote", "reassignment-vote", "payment", "account-update", "freeze"]}
  ]
}
```

Its first line says what it runs with. `key` is the SHA-256 of its public key, in hex: the name of
the key's record on any bank's public face, `hdls/resolve/key/<key>`, so the operator can check that
it is the bank's key on chain:

```
hypernode signer up: listen=[::]:7000 allowFrom=[172.28.71.0/24] name="bank-a" network="village/9ce65ba0…" key=60de4dde… clients=3
```

Once a minute while it refuses any, it logs the connections `allowFrom` turned away:

```
refused 1 connections in the last minute from addresses not in allowFrom, the last from 172.28.70.11:49134
```

**The key and its passphrase.**

- The key file is sealed (`keygen -encrypt`). The signer reads it, and the passphrase file, once, at
  start, and again at each start.
- Keep the passphrase off the key's disk, so that a copy of that disk, or of its backup, does not
  open the key: a container secret, or a file on a USB stick that is plugged in for the start and
  taken out after. The quickstart keeps the passphrase beside the key, which is for a trial only.
- Make the key file, the passphrase file and the secret files readable by the signer's user only.

**The sign log.**

- **One sign-log folder per signer.** The signer locks the folder of its sign log while it runs. A
  second signer on that folder, in another container on the same volume too, stops at start:

  ```
  the sign log's folder signer-logs is locked by a signer running on it, such as this one: stop that one first, or give each signer a folder of its own (resource temporarily unavailable)
  ```

  It binds its `listen` address before it takes the lock, so a second start of the same config on
  the same machine stops sooner, with `bind: address already in use`. Two signers on one log would
  each rewrite it, lose the other's slots, and sign them again. The lock (`flock`) holds on one
  machine only: two machines that share the folder, over NFS say, do not see each other's. Keep the
  sign log on the signer's own disk.
- Never delete the sign log, never copy it to another signer, and give `signLogPath` as the real
  path, not a symbolic link (refused).
- A restart is safe. The signer reads the log back, drops a last line that a crash cut short (it was
  never fsynced, so its slot was never signed), and writes the log again.
- If a write to the sign log fails, as on a full disk, the signer stops at once, without signing:
  `sign log: <the error>: the signer stops, and has not signed; once the disk is fixed, start it again`.

**`-resume-above`: a signer without its sign log.** A signer that starts with no sign log does not
know which slots it signed, so it refuses every vote and proposal, and says so as it starts:

```
no sign log: every vote and proposal is refused until the signer is started with -resume-above <height>, read off the network's status
```

Co-signatures, freezes, payments and governance votes still work: they have no slot. Start it once
with `-resume-above <height>`: it writes a new sign log that refuses every vote and proposal at or
below that height, then runs. Start it without the flag from then on; the flag is refused where a
sign log exists.

- **A new network, or a new key**: nothing is signed yet, so `-resume-above 0`. The quickstart writes
  that log itself.
- **A log lost to a dead disk**: the largest `heightInProgress` that the banks' `hdls/status` show
  (any bank's public face, or ask by phone). The new log refuses that height and every one below,
  where the lost log may have signed. The bank votes again from the next height, so for that height
  the others must hold more than ⅔ of the power without it.
- **Never after a break-in on the engine** (below).

The engine's own sign log is apart: an engine back without it waits, as
[Rejoining after a restart](#rejoining-after-a-restart) says, whatever its signer holds.

**The audit log** grows without bound: about 130 bytes a line, and a line for each receipt among the
rest, so about 1 GB a day while payments fill the fetcher's default cap of 100 submissions a second. A signature whose
audit line cannot be written is not given, so a full disk under the audit log stops the bank voting.
Rotate it by renaming the file, as `logrotate` does: the signer opens the file for each line, so the
next line goes into a new one, with no restart. Read it for what each client had signed, after a
break-in on a terminal most of all.

**One request at a time.** The signer decides and signs one request, then the next, whichever
client sends it. So the bank's wallet shares the signer with the engine's votes, and a wallet, or a
script, that sends request after request can delay the bank's votes past their second. Receipts are
asked for one at a time already (see [signature.md](signature.md#the-engine-and-the-wallet-through-it)).

**Rotating a client secret.** The signer and its clients read the secrets only at start. Make a new
one with `keygen -secret`, put it in the client's secret file on the signer's side, and restart the
signer; then put the same secret in the client's file and restart the client. Between the two, the
signer refuses the client (`401 Unauthorized`, `the HMAC does not match the body under this
client's secret`), and an engine misses its votes, so do one bank at a time, quickly. Rotate a
secret when its client's machine was broken into, when the file may have been copied, and when
someone who held it leaves.

### Signer messages

The engine makes its key self-test through the signer at start. If the signer does not answer, or
answers no, the engine stops, and its last line says why:

| `config: key self-test: …` ends with | What to do |
|---|---|
| `Post "http://172.28.71.254:7000/sign": dial tcp 172.28.71.254:7000: connect: connection refused` | start the signer, or fix `url`. The engine needs its signer up when it starts: under Compose, `restart: on-failure` starts it again until its signer answers |
| `Post "…/sign": EOF` | the signer closed the connection: the engine's address is not in its `allowFrom` |
| `the signer answered 401 Unauthorized in key-self-test: unknown client` | the signer's config has no client named as the engine's `client` |
| `the signer answered 401 Unauthorized in key-self-test: the HMAC does not match the body under this client's secret` | the two secret files differ |
| `the signer answered 403 Forbidden in key-self-test: the name is "carol", not this bank, "bank-a": the signer signs only in its own name` | the engine's `name` is not the signer's `name` |
| `the signer's answer in key-self-test does not verify under publicKey: the signer holds another key, or another program answered` | the signer's key is not the engine's `publicKey` |

The config's own refusals come before that: `privateKey and signer are both set`, `neither
privateKey nor signer is set`, `signer needs url, client and secretFile`, and `signer.secretFile:
the secret file engine.secret does not hold 64 lowercase hex digits, as keygen -secret prints them`.

While it runs, the engine logs its signer once when the requests start failing, with the error, and
once when they work again, at most once a minute:

```
signer: requests to the signer at http://172.28.71.254:7000/sign fail: <the error>
signer: the signer at http://172.28.71.254:7000/sign answers again
```

When the signer comes back within the minute, the second line waits for the first request after
that minute. A vote that fails is dropped, and cast again later; meanwhile the bank does not vote. Besides the start's errors above, and a timeout after a
second, the error can be:

- `the signer answered 409 Conflict in block-revision: REFUSED CONFLICT: this slot is signed already, with other fields`
- `the signer answered 409 Conflict in block-revision: height 41 is below 57, the highest height signed`
- `the signer answered 409 Conflict in block-revision: no sign log: every vote and proposal is refused until the signer is started with -resume-above <height>`
- `the signer answered 500 Internal Server Error in block-revision: the signer failed: its log says why`:
  the signer could not write its audit log, as on a full disk, and its own log says `audit log: …`

The engine's line shows the first error of a run only. The signer's audit log has every refusal.

- **A `REFUSED CONFLICT` right after an engine restart can be honest.** The engine remembers the
  fields it sent in each slot in memory only. After a crash or a restart that followed a request
  that timed out, the signer may hold that slot signed, and the engine may ask it for other fields
  there. That costs the bank its vote in that slot, never a second signature.
- **A `REFUSED CONFLICT` while the engine runs is an alarm.** Another program asks with the engine's
  secret, or two engines share it. Stop the engine and look ([below](#a-break-in-on-the-engine)).

### A break-in on the engine

1. Stop the engine. Rebuild it from a clean image, on a clean machine, and give it a new client
   secret.
2. **Keep the signer's sign log as it is.** Never delete it, and never start the signer with
   `-resume-above`, while the old key is the bank's. The broken engine may have had votes signed for
   heights the chain has not reached. A new log would let the signer sign other votes in those
   slots, and the two together are a double-sign, which freezes the name.
3. Read the audit log for what the engine had signed. If it holds votes far ahead, the signer
   refuses every vote below them (rule 4), and the bank does not vote until the chain gets there.
4. Re-key the bank through the ⅔, as for [a key that leaked](#a-bank-whose-key-leaked). The new key
   gets a sign-log folder of its own, started once with `-resume-above 0`. Keep the old log until
   the re-key has committed.

## Network exposure

A bank serves three things, each for its own callers, and keeps a fourth to itself:

| What | Who reaches it | Where it is set |
|---|---|---|
| the gossip port | the other validators, from the IP addresses they publish | `gossipListen` (usually `":8070"`) |
| the operator port | the bank's own machines: its fetcher, its operator's wallet, its operator's `curl` | `listen` |
| the public face | anyone: customers' wallets, joining nodes, the other banks' second path | the fetcher, behind a CDN named by the bank's `publicAddress` |
| the signer | the bank's engine and wallet, and nothing else | the signer's `listen` and `allowFrom` (see [Running a signer](#running-a-signer)) |

- **The gossip port** serves only the routes validators use. Open it to the internet, at the IP
  addresses the bank publishes. Its allow list keeps out everyone but the current validators and
  the `allow` entries (see [api.md](api.md#two-ports-and-the-public-face)). It has no TLS: every
  message on it is signed, and nothing on it is secret. A network that wants the link encrypted can
  run its validators over a VPN, such as WireGuard, and publish the VPN addresses as their IP
  addresses.
- **The gossip port must see each caller's own address.** The allow list trusts it. Publish the
  port with rootful Docker or Podman, whose address translation keeps it; run the engine on the
  host's network; or, with rootless Podman, use the `slirp4netns` port handler
  (`--network slirp4netns:port_handler=slirp4netns`), not the default one. A forwarder or a TLS or
  TCP proxy in front makes every caller one address. When that address is the engine's own, a
  loopback one, or, with rootless Podman's default port forwarding, the container's own
  `10.0.2.100`, the engine refuses every caller, and the refusal line below shows it.
- **With Docker, publish the gossip port the default way**, through iptables (DNAT), which keeps
  the caller's address. Don't run it with Docker's userland proxy alone, with iptables turned off:
  that proxy runs outside the engine's network namespace, and makes every caller the bridge's
  gateway, such as `172.17.0.1`. The engine does not catch that. Never list a bridge's gateway in
  `allow`: everyone would get in.
- **One engine per IP address.** The gossip port refuses a connection whose two ends are one
  address, and so do the fetches from public addresses. So two engines that share an address, as
  on one machine with host networking, can't gossip with each other, or reach each other's public
  faces, through it.
- **The operator port** serves every route: customers' payments with their receipts, the account
  list, the governance votes of the admin page, bundles and carried checkpoints. Never publish it.
  Beside a gossip port it listens on `127.0.0.1:8080` unless `listen` says otherwise. Bind `listen`
  to `127.0.0.1` or to an address on the bank's own network, or keep it behind the machine's
  firewall. It has no allow list, and a flood there could stall the event loop.
- **The public face** holds nothing secret: see [The public face](#the-public-face) below.
- **The signer** holds the key. Never publish its port, and give its `allowFrom` the engine's and
  the wallet's addresses only, on a network nothing else joins.
- **The bank's wallet** signs as the bank: whoever drives it signs with the bank's key. Never let it
  listen where anyone but the bank's operator can reach it: keep it on `127.0.0.1`, or on a network
  only the operator's machine joins. Anywhere but a loopback address it refuses to start without
  `-password-file`, and asks for that password (see
  [ui/README.md](../ui/README.md#security-notes)).

What the allow list does not do is in
[security.md](security.md#the-two-ports-and-the-public-face): it keeps strangers off the routes that
cost a node work, but it is not authentication, and it does not stop a flood aimed at the address
itself.

### The addresses a bank connects from

The allow list holds the addresses each validator publishes. So among its up to 4 IP addresses, a
bank lists the one the others reach it at, and every address its engine's connections come from:
its pushes to the other validators, and its catch-up requests, which leave from the machine's
default address. The others refuse a connection from any other address.

- **On a host with one public address**, the connections leave from it, Docker's default address
  translation included, and one entry covers both jobs.
- **Behind a cloud NAT gateway**, list the gateway's address as well.
- **With a second line to the internet**, list that line's address too. The others' pushes fall
  back to it when the first address gets no answer.
- **With IPv6 temporary addresses**, turn them off on the engine's interface, or list them.
- **Two banks behind one NAT**, which both publish its address, reach each other from that
  address. Each one's allow list leaves out the addresses its own account publishes, that one
  among them, so each lists it in `allow`.

The others' pushes to a bank fall back to its next address only when the connection to one fails:
it is refused, the address can't be reached, or no connection is made within 2 s.

A bank that breaks the rule finds that its pushes fail. Its log says so once per peer when the
pushes start failing, and once when they work again, never once per message, and at most once a
minute for each peer:

```
gossip: pushes to the peer at http://203.0.113.2:8070 fail: <the error>
gossip: the peer at http://203.0.113.2:8070 answers again
```

The second line comes with the first push that works a minute or more after the first line. On an
idle network, which pushes little, it can come minutes after the peer answers again.

On the other side, the gossip port logs, once a minute while it refuses any, how many connections
it refused, and the address it refused the most, with the names that list that address on chain:

```
gossip port [::]:8070: refused 7 connections in the last minute from addresses not on the allow list, 5 of them from 198.51.100.7: listed by carol (not a validator)
```

The end of the line is `listed by no name` when no name lists the address. What to do about a name
it gives is in [Naming who floods](#naming-who-floods).

### Changing addresses

A bank changes its addresses with an account update, from the account page of its wallet. The
others refuse the new address until that update commits, and the bank's own pushes from it fail
meanwhile. So a bank whose address changed sends that update through another bank's public face:
the fetcher passes updates on. Or it asks the others to put the new address in their `allow`, and
restart, until the update commits. An update's `IPAddresses` replace the whole list, so it gives
every address the bank keeps. A bank needs a stable address; banks are meant to be visible.

## The public face

The fetcher (`cmd/fetcher`) is a bank's public face. It serves the files the engine writes in its
public folder, and the record files, read-only, and passes customers' payments, updates and name
requests on to the operator port, at most 100 a second by default. It holds no key and keeps no
state. A CDN stands in front of it.

Run it on the engine's machine, as the same user as the engine. The engine keeps its record files
readable by its own user only (files of mode 0600 in folders of mode 0700). The public folder is
readable by anyone. In the folder the engine runs in:

```
./bin/fetcher -listen :8081 -folder data/public -engine http://127.0.0.1:8080 \
    -record data/snapshot.json.<network>.record -lifelong data/snapshot.json.<network>.lifelong.jsonl
```

`<network>` is the first 16 hex digits of the genesis hash: the first 16 after the `/` of the
network id the server logs at start.

| Flag | Meaning |
|---|---|
| `-listen` | the address it serves on, the CDN's origin; `:8080` by default |
| `-folder` | the public folder: the engine's `publicPath`. Required |
| `-record` | the engine's record folder, `<snapshotPath>.<network>.record`. Required |
| `-lifelong` | the engine's lifelong record file, `<snapshotPath>.<network>.lifelong.jsonl`. Required |
| `-engine` | the base URL of the engine's operator port, such as `http://127.0.0.1:8080`. Required |
| `-max-submissions-per-second` | the most payments, updates and name requests it passes on each second; 100 by default, at least 1. Each bank sets its own: nothing else depends on it |
| `-client-address-header` | behind a CDN: the header the CDN sets to the client's address, such as `X-Forwarded-For`. The fetcher reads the last address in it. With `-cdn-addresses` only |
| `-cdn-addresses` | the CDN's addresses or prefixes, comma-separated, such as `198.51.100.0/24,2001:db8::/32`: the header is read only on a connection from one of them, since anyone else could write any address there |

It logs one line as it starts, `hypernode fetcher up:` and its flags. The paths it serves, the
`Cache-Control` each file gets, and its answers are in
[api.md](api.md#the-public-face). Among them are the name service's files, `hdls/zone` and
`hdls/resolve/ip/<address>`, and `hdls/frames`, which joining nodes read.

Once a minute in which its cap answered `429`, it logs the five addresses that sent the most
submissions, each with the names that list it, as the engine's `hdls/resolve/ip` files have them:

```
the cap of 100 submissions a second answered 429 in the last minute; the addresses that sent the most: 198.51.100.7 sent 5400, listed by carol; 203.0.113.9 sent 12, listed by no name
```

Behind a CDN, without the two flags above, every sender is the CDN's address. With them, a request
whose header holds no address it can read counts as `senders the CDN's header gave no address for
sent N`.

**The CDN.** Any CDN can do the job: the public face has no secret and nothing to compute.

- **The origin** is the fetcher. The CDN serves `https` under the bank's public address, the name on
  chain, such as `https://bank-a.example`.
- **Caching** follows the fetcher's `Cache-Control` headers: a second for the status, the
  checkpoint and the public bundle, and for a listed file that is not there yet; five seconds for
  the other files; and for good for a complete record unit. The three submission routes are
  `no-store`: the CDN passes them on to the origin. The CDN revalidates a file by its `ETag`; the
  fetcher sends no `Last-Modified`.
- **Limits per client** are the CDN's job. The fetcher's cap only keeps a flood off the engine.
- **TLS keys on chain.** A bank may list its web server's TLS keys on its account (see
  [The genesis file](#the-genesis-file), and the account form of its wallet). From then on a wallet
  that verified a state listing them takes, at the bank's public address, only a certificate whose
  key is one of them, whatever certificate authority signed it (see
  [`ui/README.md`](../ui/README.md#how-it-works)). To change the key, list the new one beside the
  old one first, and change the server's key well after that update committed: a running wallet
  that has not read a state since still takes only the old key, until it restarts. Then drop the
  old one. A wallet's first read after each start still uses the usual check by certificate
  authority, so keep a certificate one signed.
- **After a new genesis**, purge the CDN's cache (see
  [Starting a new network](#starting-a-new-network)).

**The wallets.** A customer's wallet runs with `-node` set to its bank's public address. The bank
operator's wallet runs with `-node` set to the engine's operator port, such as
`http://127.0.0.1:8080`: the governance votes work only there. With a signer, it names the signer
too, with `-signer-url`, `-signer-client` and `-signer-secret-file`. See
[`ui/README.md`](../ui/README.md).

A wallet's first use through the public address trusts whatever banks that address, and the CDN in
front of it, serve. So give each customer the bank's list of banks, the `.trust` file of the bank's
own wallet, to save beside the wallet file (as `wallet.json.trust` for `wallet.json`) before the
first start. Or have the customer's first use take place at the bank.

## The name service

Every name may publish a public address, up to 4 IP addresses and up to 2 TLS keys, signed by its own
key (see [governance.md](governance.md#names-as-a-name-service)). The engine publishes them in two
more forms, in its public folder, which the fetcher serves:

- **`hdls/resolve/ip/<address>`**: the names that list an IP address, as a JSON list, a frozen one
  as `"carol (frozen)"`. It is the lookup from an address to names, for people and programs that
  read HTTP. An address no name lists has no file: `404`.
- **`hdls/zone`**: a zone file in the standard format of DNS (RFC 1035), which any DNS server (NSD,
  Knot, BIND, CoreDNS) loads as an authoritative zone. The engine writes it only with a `zone` in its
  config (see [The node config](#the-node-config)).

The zone of `hdls.example.org` holds, for each name that is not frozen, an `A` or `AAAA` record for
each of its IP addresses, a `TXT` record with its public address, and, for an `https://` public
address, a `TLSA` record for each TLS key; then the IP filter, `validators-of-the-height-in-progress`,
with every current validator's addresses, loopback ones left out; then the lookup from an address to
names, under the address's bytes in hex:

```
$ORIGIN hdls.example.org.
$TTL 60
@ SOA ns1.example.net. hostmaster.example.org. 0 300 60 86400 60
@ NS ns1.example.net.
bank-a A 203.0.113.1
bank-a AAAA 2001:db8::1
bank-a TXT "hdls-public-address=https://bank-a.example"
_443._tcp.bank-a TLSA 3 1 1 956d21c415e30a783b12c7c244a2c2fcd4806b5323e2b22f1ca2d5fee708371d
carol A 203.0.113.1
validators-of-the-height-in-progress A 203.0.113.1
validators-of-the-height-in-progress AAAA 2001:db8::1
cb007101._ip TXT "bank-a" "carol"
20010db8000000000000000000000001._ip TXT "bank-a"
```

A firewall that takes DNS names in its rules, or a script that resolves
`validators-of-the-height-in-progress.hdls.example.org` every minute, keeps an allow list of the
validators without reading the chain. The `_ip` record names at most 100 names; past that, its last
string says how many more there are and which `hdls/resolve/ip/` file lists them all.

### Publishing the zone

The engine writes the zone file when it changes: after a commit or a catch-up that changes a record
in it, and at start, when the file there is not the zone of the state it opens at. Its SOA serial is
the height at which it wrote it. A DNS operator fetches `/hdls/zone` from a bank's public face, or
from several and compares them, writes it where the DNS server reads the zone, and reloads the zone
when the serial grows.

- **A name server outside the zone.** The zone's names are the ledger's: a customer's name holds the
  addresses the customer lists. So the engine refuses a name server inside the zone:
  `zoneNameServers: "ns1.hdls.example.org" is inside the zone, whose names are the ledger's, so its
  address would be whatever the customer holding that name lists, or none: use a name server outside
  the zone`.
- **A zone that is its own registrable domain**, such as `village-names.example`, not a child of a
  bank's web domain, such as `hdls.bank-a.example`. The customers point the zone's names where they
  like, and under the bank's own domain each would be a site of the bank's, to browsers and to
  certificate authorities.
- **One setting for every bank that publishes it.** The file follows from the committed accounts and
  the zone settings, so two banks with the same `zone`, `zoneNameServers`, `zoneContact` and
  `zoneTimeToLive` write the same zone, apart from the SOA serial: a bank that caught up past a
  change writes it at a later height. Compare two banks' files without that line, for example
  `diff <(grep -v ' SOA ' a.zone) <(grep -v ' SOA ' b.zone)`. Anyone can rebuild the zone from the
  public `hdls/accounts` the same way.
- **DANE needs a signed zone.** A `TLSA` record counts for a client only in a zone signed with
  DNSSEC. The engine does not sign the zone: the DNS operator signs it with their own DNS tools, or
  publishes it unsigned without DANE. The `TLSA` records sit under the name in the zone, such as
  `_443._tcp.bank-a.hdls.example.org`, whose addresses are the name's IP addresses, not its web
  server behind a CDN: they serve a client that reaches the name by its name in the zone. The
  wallets read TLS keys from the chain, not from the zone.

With BIND:

- **More than 100 addresses in the IP filter.** BIND 9.18.28 and later refuse a zone with more than
  100 records of one type under one name (`max-records-per-type`). Only
  `validators-of-the-height-in-progress` can pass it, in dynamic mode with many validators: more than
  100 IPv4 addresses, or 100 IPv6 ones. Then set `max-records-per-type 0;` in the zone's settings.
- **A zone write that failed.** The engine logs `public folder: hdls/zone: <the error>`, and writes
  the file again only when the zone next changes. Once the disk is fixed, restart the engine, which
  writes it at start.
- **A settings change before the next block.** A restart with other zone settings writes the zone
  with the height the engine is at as its serial. If no block committed since the zone was last
  written, as when the engine restarts twice while an idle chain waits for its next block, the
  serial stays the same, and secondary servers do not fetch the zone. Reload it on the primary, then run `rndc retransfer <zone>` on each secondary. A change of
  the names' records always comes with a commit, so with a new serial.

### Naming who floods

When an IP address floods a bank, the bank can say which name claims it, so that people resolve it.
Two lines name it: the gossip port's refusal line, once a minute, for connections from addresses not
on its allow list ([The addresses a bank connects from](#the-addresses-a-bank-connects-from)); and the
fetcher's line, once a minute in which its cap answered `429`
([The public face](#the-public-face)). Anyone can look an address up in `hdls/resolve/ip/<address>`,
or in the zone.

- **A name lists the address**: its bank of record calls the customer. If the flood goes on, the
  banks freeze the name by the ⅔ name action (see *The ⅔ name action* in
  [governance.md](governance.md)). For a validator, the ⅔ freeze also takes its
  addresses off every bank's allow list, since the list is the current set's.
- **No name lists it**: it goes to the CDN's or the hosting provider's abuse desk, as before.
- **No code bans anyone.** The names only tell people whom to call.

The limits:

- **An address on chain is a claim.** Nothing checks that a name controls the addresses it lists,
  and two names that list one address are both named. Call before freezing.
- **One flooder can be many addresses.** Each address is counted apart, and each line names only the
  address that sent the most. An IPv6 flooder can spread over its /64, and so show as many addresses
  that each sent little, and that no name lists. The gossip port counts up to 1,024 addresses a
  minute, and the fetcher up to 10,000.
- **Behind a CDN** the fetcher names the CDN's addresses, unless `-client-address-header` and
  `-cdn-addresses` are set.

## Rejoining after a restart

A node with `snapshotPath` writes its committed state after every block and reloads it on restart,
so a node, or a whole network after a power cut, comes back without a peer. A node with `bootstrap`
peers also fetches a checkpoint from them at start, verifies it against the validators of its own
last committed state, and fast-forwards if it is ahead. Use both. A failed snapshot write is logged,
not fatal. For the ledger itself, keep a periodic off-machine copy of `GET /hdls/checkpoint`, or of
the public folder's `hdls/checkpoint`, which is the same file.

A node with no snapshot checks a checkpoint against the genesis validators, who may have left or
re-keyed since; a node that was away, against the validators of its own last state. How it gets
across the changes since, and when it needs a snapshot from a node you trust, is in
[A joining node](#a-joining-node).

A validator's **sign log**, `<snapshotPath>.votes`, holds what it signed at the height in progress
(see [signature.md](signature.md#the-sign-log)).

- Run each validator's key on one node only: no hot standby, and no copy of its data folder
  running elsewhere. The sign log keeps only its own node from signing twice: two nodes with one
  key double-sign, and a double-sign freezes the name. `cmd/server` binds its listen addresses
  (`listen`, and `gossipListen` when set) before it opens the node, so a second start of one
  config on the same machine stops before it signs. A second node on another machine, or on other
  addresses, is not caught.
- Never delete the sign log, except at a new genesis, together with the snapshot.
- `backupPath` mirrors the snapshot, not the log, and it can be a height or more behind: the
  mirror skips commits while one is still being written. Never restart a validator from a backup
  while the network is at or below the last height it voted in.
- An ordinary restart between heights finds the empty log a commit leaves for the next height, and
  starts at once.
- A node that starts above its genesis without a sign log for the height it starts at signs no vote
  or proposal there for twice the longer of `stepTimeoutMs` in force and the slow-down switch,
  `minimumStepTimeoutMs` (two re-gossip intervals with the switch off), so peers can send its own
  earlier votes and proposal back, or, in pigeon mode, the next carry can bring them, then resumes
  from them. It waits so at every height it
  may have voted in: the one it starts at; each later one it commits into, up to the highest at
  which a vote or proposal of its own comes back to it; and the network's height, where a catch-up
  from those takes it. Meanwhile its sign log stays below that height, so a restart during the wait
  waits too; its first new sign log at a height holds the votes and proposal it took back there;
  and once it has a sign log, the log keeps the highest height it may have voted in, so a later
  restart still waits there. It is best effort, by timing, and recovers only what running peers
  still hold and re-gossip during it: the votes of the height they are at, their current round's
  proposal, of any round up to 256, and the commit of the height just below theirs. So it can still
  sign against its own earlier votes and proposals at a height two or more below its peers' (the
  one it starts at, say), where it signs after the wait, before it catches up; against those at the
  height just below theirs that the commit lacks; and against its proposal of a round its peers
  have left. A node that never lost a log but waits at the height it starts at anyway (an observer,
  an unseated name, a validator that adopted a checkpoint at start) also waits once after a
  catch-up from there: a delay only.
- If a validator loses its log (a dead disk, a restored backup), restart it while its peers are up,
  with bootstrap peers that answer: it then takes the network's current checkpoint before it starts
  a height, and waits at that height for its own earlier votes. From a snapshot below the last
  height it voted in, it waits so at each such height it reaches, as above, but it can still sign
  against what does not come back (above), and a double-sign freezes its name. A node with no
  snapshot starts at genesis and does not wait at all. So the safest restart is from its own
  snapshot once `GET /hdls/status` on a peer shows `heightInProgress` past the last height it voted
  in, and never from an empty data directory or an older backup on its own.

A node refuses to start if its snapshot or sign log cannot be read, belongs to another network, or
does not verify, or if the sign log is ahead of the snapshot. It never falls back to genesis on its
own, so look at the files before deleting anything.

## A joining node

A node takes a newer checkpoint, at start or once it falls behind, only when validators holding more
than ⅔ of the power of the frame it trusts signed it, each under the key that frame holds for it. A
frame is a validator set and the height from which it signs; the frame a node trusts is the set of
its own last committed state: the genesis set for a fresh node, or the set of the snapshot it was
seeded with. So no single peer can hand it a false state: that takes the keys of more than ⅔ of a
set it already trusts.

- **Every validator is asked.** At every catch-up tick, every 3 s or every 5 block intervals if that
  is longer, the node asks its `bootstrap` peers, its gossip peers and every other validator of its
  frame, at its public address, and adopts the highest checkpoint that passes. A peer that withholds
  the newest one is outvoted by the others.
- **The walk.** When the set changed too much since its frame, the node walks from frame to frame. It
  reads `hdls/frames` from the other validators' public faces: one JSON line for each change of the
  names or keys of the set, with the height it starts at and the new set. It takes a frame only once
  the record entry of the height before it, from the same public face, holds a commit by more than ⅔
  of the frame it holds, for a block whose header names the new set as the next one
  (`NextValidatorsHash`). Each line is checked against the record, so a false line costs a fetch,
  never trust. How it works is in [consensus.md](consensus.md#onboarding-and-the-checkpoint).
- **Confirmed.** A seated node is confirmed at the first commit that holds its own precommit: more
  than ⅔ of the current set then agree with the state it built on. Its log says so, and its status
  shows the height (`"confirmed": 1043` in `GET /hdls/status`, and in its public `hdls/status`),
  `null` before:

  ```
  confirmed: the commit of height 1043 holds this node's own precommit, so more than ⅔ of the set agree with its state
  ```

  It is kept in memory only, so a restart shows `null` until the next such commit. An observer,
  which never signs, is never confirmed.

### Seeding a joiner

A fresh node starts from the genesis set, and walks from there, as long as the public faces still
hold every frame since: the record keeps a height for `retainBlocks` heights (604,800 by default,
about a week at a block a second), and `hdls/frames` lists at most the newest 100 frames. Otherwise,
or where it can reach no public face, seed it: copy the snapshot file of a node you trust, your own
bank's, to the joiner's `snapshotPath` before its first start. The joiner trusts that file as its
own, and every later check runs inside it, so a false seed makes a false world.

**Check the seed with a second bank** before the first start. Read the seed's height and block hash
on the joiner's machine:

```
HEX='map(. as $b | "0123456789abcdef"[($b/16|floor):($b/16|floor)+1] + "0123456789abcdef"[($b%16):($b%16)+1]) | join("")'
jq -r ".Checkpoint.Commit.Height, (.Checkpoint.Commit.BlockHash | $HEX)" data/snapshot.json
```

Then ask a second bank, by phone, which block it committed at that height. Its operator reads it
from the bank's record, here for height 1043, in the unit file of the 100 heights from 1000:

```
curl -s https://bank-b.example/hdls/record/00000000000000001000.jsonl | jq -r "select(.Height == 1043) | .Commit.BlockHash | $HEX"
```

or, while its node is still at that height, as `lastBlockHash` in its `hdls/status`. Start the joiner
only if the two hashes match. Once it runs and is seated, `confirmed` in its status says that the
current set agrees with it.

### The fork alarm and the divergence alarm

Each alarm stops the node for people to look. It signs nothing more, adopts no checkpoint, and does
not follow the chain, since on a fork it would follow whichever side its peers are on. Its status
says why, in `"halted"` (`null` while it runs), and its log carries the evidence in full. Only a
restart clears it, and it comes back if its cause is still there.

- **The fork alarm**: two checkpoints that both pass commit different blocks at one height ahead of
  the node. More than ⅓ of a set signed both:

  ```
  fork alarm: two checkpoints that both verify commit different blocks at height 1043, 948fe493… and 1c0ffee5…: more than a third of a set signed both. The first commit: {…} The second commit: {…}
  ```

- **The divergence alarm**: the node's state and the network's disagree. Any node raises it for a
  checkpoint that passes and commits another block than the node's own at its last committed
  height. A seated node also raises it when more than ⅔ of the set precommit a block its own state
  refused, or when a checkpoint of its height in progress does not build on its last block. A false
  seed, a broken node, or a fork:

  ```
  divergence alarm: more than ⅔ of the set committed block 1c0ffee5… at height 1043, and this node committed block 948fe493… there. The network's commit: {…}
  ```

What the operator does:

1. **Look.** Copy the lines out of the log. Ask the other banks, by phone, for their block at that
   height (as for the seed, above), and find which side the node is on.
2. **The node is on the wrong side** (its seed was false, or it is broken): stop it, seed it again
   from a bank you trust, checked with a second bank, and restart it.
3. **The node is right, and the network forked**: more than ⅓ of a set signed two blocks at one
   height, and the two commits name who. That is for the banks to settle together; see
   [security.md](security.md#the-freeze-and-its-limits). Restart the node once they have.

### Its limits

- **The first frame is trusted.** The genesis, or the seed. Check a seed with a second bank.
- **No trusting period.** Keys stolen from more than ⅔ of the power of any old frame on the walk's
  path can sign a false history that the walk takes.
- **The walk reads public faces only.** The gossip and operator ports serve no frames and no record.
  A joiner that reaches no public face, or a network without fetchers, must be seeded again across a
  large change of the set.
- **The walk reaches back as far as the record.** A frame older than `retainBlocks`, or older than
  the newest 100 frames, needs a newer seed. So does a frame no face lists: a face lists only the
  frames whose start it committed, not those its catch-ups skipped.
- **Stake drift, in dynamic mode.** Power moves with stake at almost every busy block, but a frame
  is listed only on a change of names or keys, and the node holds each frame at its first powers. If
  the powers drift a lot, and the validators whose power fell are the ones absent from a commit, the
  commit before the next frame may not hold more than ⅔ of the held frame's powers, and the walk
  stops there. Such a joiner needs a newer seed.
- **A carried checkpoint does no walk.** `POST /hdls/checkpoint` checks against the node's own frame
  only (see [Carrying a checkpoint](#carrying-a-checkpoint)).
- **A fork it cannot see.** The divergence check needs a peer still at the node's own height. A node
  behind on the other side of a fork, when every peer is two or more heights ahead, as on a busy
  chain, takes their checkpoint and so their side without an alarm: telling the two apart would need
  the chain of headers in between. An observer misses it one height ahead too.
- **A stale line.** A node seeded again onto the other side of a fork, at the same height, may keep,
  at its height in progress, a frames line of the side it left. A walker that reads it stops at that
  face, and reads the next one.

## The record on disk, and the idle chain

A node with `snapshotPath` keeps the signature record beside the snapshot, in
`<snapshotPath>.<network>.record/` (one unit file per 100 heights) and
`<snapshotPath>.<network>.lifelong.jsonl` (never pruned). `<network>` is the first 16 hex digits of
the genesis hash. A unit file is deleted once its heights are past `retainBlocks` (604,800 by
default). See [architecture.md](architecture.md#the-signature-record).

A height starts at once when there is work, and otherwise after the idle heartbeat of five minutes,
so a network nobody uses commits and records one empty block about every five minutes
([architecture.md](architecture.md#the-idle-chain)): 288 entries a day, about 5.3 MB a day and
37 MB a week with 4 validators, and 21 MB a day and 147 MB a week with 21. If the height stays the
same for well over five minutes plus `blockIntervalMs`, the chain is stalled, not idle. A busy
chain records one entry per block: in static mode, about 1.6 GB a day with 4 validators at one
block a second, and 6.2 GB with 21. In dynamic mode most entries also carry the validator set:
about 2.5 GB and 11.1 GB a day. A pending key or bank change keeps blocks coming until it falls
due, about a day. Account updates are free and count as work, so one name can keep the chain busy
for nothing: the ⅔ can freeze such a name.

## A bank whose key leaked

1. Freeze the bank's name at once: the freeze button in the bank operator's wallet, which holds the
   bank's one key or signs through its signer (the desk and the admin page may both ask for a
   freeze). The name leaves the validator set, and every pending change it co-signed is cleared. A
   bank that holds a third or more of the power hands the chain to the rest. The last seated name
   cannot freeze itself.
2. Make a new key with `keygen`, on the bank's machine, and write it into a new wallet key file
   (see [Generate keys](#generate-keys)). That wallet is the bank's wallet from now on. With a
   signer, make it with `keygen -encrypt`, on the signer's machine. The wallet reads that key file
   too: start one on it, against the bank's engine, only to sign the proof in the next step, then
   stop it.
3. The other banks vote a ⅔ name action that gives the name the new key and unfreezes it. The new
   key's wallet signs the proof first (see [governance.md](governance.md#collecting-the-votes)). No
   signer signs that proof: its domain, `reassignment-new-key`, is never a bank's.
4. Put the new pair in the bank's node config and restart it. Until then the node signs no vote or
   proposal, and at the next height the chain starts it logs that its name is seated under another
   key. It still signs payment receipts, with the old, leaked key. With a signer, give the signer
   the new key file and its passphrase file, and a new sign-log folder, which it starts once with
   `-resume-above 0`; put the new public key in the engine's `publicKey` and in the bank's wallet
   file; and restart the signer, then the engine and the wallet. The engine checks every answer
   under its `publicKey`, so the signer and the engine change keys together.
5. Tell the bank's customers: key changes the bank co-signed were cleared, and they file again.

## Liveness

Finalisation needs more than ⅔ of the power online and agreeing. If each of many flaky machines
holds one unit of power, losing a third of them stalls the chain. On unreliable hardware, put
enough power on the machines you can keep awake that they alone exceed ⅔, and disable sleep on
them (`logind.conf: HandleLidSwitch=ignore`). When the network itself is cut, see the next section.

## When the network splits

An internet provider can slow the network, split it, or block it. This section is the runbook for
that case: pigeon mode, where the banks carry their messages by hand.

### What a split can and cannot break

**It cannot break correctness.**

- **No rule reads the clock.** The packages `ledger` and `consensus` never import Go's `time`
  package (`TestNoRuleReadsTheClock` checks it), a block carries no timestamp, and every deadline
  counts in blocks.
- **Every message is signed.** Each one proves itself, whoever carries it.
- So a provider that slows, splits or blocks the network can stop the chain, but it cannot fork it
  or move money. When the link comes back, the chain goes on.

**It can break progress.**

- **One bank cut off.** If the rest hold more than ⅔ of the power, they carry on. The cut-off bank
  and its customers wait, then catch up.
- **No side above ⅔.** Every node stops at the step where it needs the other side's votes. The
  prevote and precommit timers start only once votes from more than ⅔ of the power are in, so at
  that step no timer runs, and votes carried across by hand let the step complete.
- **The catch.** The propose timer always runs. A node waits `stepTimeoutMs` × (round + 1) for the
  round's proposal, 3 s at round 0 by default, then votes "no block". A proposal carried by hand
  comes too late, and rounds pass with no block. The slow-down switch, below, is the way out.

### Running in pigeon mode, step by step

1. **Notice the split.** No commits, and pushes failing in the log (see
   [The addresses a bank connects from](#the-addresses-a-bank-connects-from)).
2. **Agree to slow down.** The banks holding more than ⅔ of the power agree, out of band (by phone,
   by radio, in person), on a value for the slow-down switch. Each sets `minimumStepTimeoutMs` in
   its node config and restarts its engine. A restart that keeps the sign log starts at once. An
   engine that comes back without its sign log waits twice the switch at each height it may have
   signed at, for the next carry to bring its own earlier votes back (see
   [Rejoining after a restart](#rejoining-after-a-restart)). An engine the restart leaves behind
   catches up from the other banks' public faces, where they answer, or from a carried checkpoint.
3. **Carry the messages.** On each carry, every bank exports its bundle, and the carrier takes the
   bundles, and the newest checkpoint any bank has, to every other bank. Each bank imports what it
   receives.
4. **Carry the customers' payments.** Customers who are cut off hand their payments, saved as text,
   to whoever carries next.
5. **When the network is back,** each bank sets the switch back to 0 and restarts. Gossip and
   catch-up do the rest.

### Bundles

A bundle is a text file of signed messages, one JSON object per line. The first line is its header,
and every other line is one message, exactly as its gossip route takes it:

```
{"bundle":1,"networkId":"village/…","from":"bank-a","heightInProgress":1234}
{"type":"vote","body":{…}}
{"type":"proposal","body":{…}}
{"type":"payment","body":{…}}
```

The header names the format's version, 1, and the network: an import refuses the whole bundle of
another network, or one without that header. `from` and `heightInProgress` are for the people who
carry it: no rule reads them. A vote is about 4.7 KB, and a proposal with its
block up to 2 MiB, so a height's messages fit a USB stick, an email or a radio data link, but not a
QR code. A bundle is at most 64 MiB. Every message in it is signed, so whoever carries the file can
lose it or delay it, but cannot change a message without the change being refused. The format, and
the order of the lines, are in [architecture.md](architecture.md#bundles).

**Export.** On the bank's own machine, ask the operator port for everything the engine would gossip
now:

```
curl -o bank-a.bundle http://127.0.0.1:8080/hdls/bundle
```

**Import.** At each other bank, give the operator port the bundle a carrier brought:

```
curl --data-binary @bank-a.bundle http://127.0.0.1:8080/hdls/bundle
```

It answers what became of the messages, for example `{"queued":4,"taken":2,"refused":0,"unreadable":0}`:

- **queued**: proposals and votes, handed to the engine's consensus with no verdict;
- **taken**: payments, updates, name requests and certificates the engine newly pooled;
- **refused**: messages the engine refused, as it would refuse them from gossip;
- **unreadable**: lines that are no message.

A message the engine already holds counts in none of them, so importing a bundle twice does no
harm. A bundle of another network, or one without its header line, is refused whole, with `409` and
the reason. What the engine takes it relays to the banks it can still reach, so one import reaches
every bank on its side of the split. The gossip port never takes a bundle.

### Carrying a checkpoint

A bank more than one height behind needs a checkpoint, not votes. A checkpoint is the committed
state with the commit that signed it, and it proves itself by its ⅔ signatures. Take it from a bank
that is ahead, from its public folder's `hdls/checkpoint`, or:

```
curl -o bank-a.checkpoint http://127.0.0.1:8080/hdls/checkpoint
```

The bank that is behind imports it on its operator port:

```
curl --data-binary @bank-a.checkpoint http://127.0.0.1:8080/hdls/checkpoint
```

The engine verifies it as catch-up verifies one: names of its own last committed validator set,
holding more than ⅔ of its power, must have signed it (see
[consensus.md](consensus.md#onboarding-and-the-checkpoint)). It does not walk the frames, as catch-up
can: a bank away while more than ⅓ of the power changed needs a newer seed instead (see
[A joining node](#a-joining-node)). It answers `202`, with no
body, once it has adopted the checkpoint. Otherwise it answers `409` and the reason: the checkpoint
is not ahead, it does not verify (it is another network's checkpoint, or damaged), or the node is
still starting. Add `-i` to the command to see the status. A joining node can start from a carried
checkpoint too, with the limit every joiner has: it trusts its seed.

### The slow-down switch

`minimumStepTimeoutMs`, in the node config, is the least the round timers wait per step: from 0,
off, to 86,400,000, a day.

- **What it changes.** Each round timer waits the larger of the governed `stepTimeoutMs` and the
  switch, times the round plus one. One other wait follows it: a node back without its sign log
  waits twice the larger of the two at each height it may have signed at, since in pigeon mode its
  own earlier votes come back only with the next carry. A restart that keeps its sign log never
  waits. Nothing else changes: re-gossip, catch-up, the idle heartbeat and the second path keep the
  governed timings. So the part of the network that still connects re-gossips at its usual pace.
- **How to set it.** At least twice the time a carrier takes to bring a bundle from every bank to
  every other bank. A round then completes in about three carries: the proposal, then the prevotes,
  then the precommits. A day is the most, so for blocks in the first rounds a carrier brings every
  bank's bundle to every other within half a day. With a slower carrier, blocks still commit, in a
  later round, since each round waits longer than the one before.
- **Who must set it.** The banks holding more than ⅔ of the power. One bank alone gains nothing:
  the others still vote "no block" after their governed step timeout, 3 s by default. Each bank may
  set its own value, and mixed values are safe.
- **When it takes effect.** At the engine's next start. No route changes it while the engine
  runs, and a crash or a restart does not switch it off. The start log says that it is on, here
  at 10 minutes:

  ```
  the slow-down switch is on: minimumStepTimeoutMs=600000, so each round waits at least 10m0s per step for a proposal or votes; set it to 0 and restart once the network is back
  ```
- **How to turn it off.** When the network is back, set it to 0, or remove it, and restart.
- **Why it is safe.** No rule reads the clock. A longer timer only makes a node wait longer before
  it votes "no block": it cannot make it sign twice or take an invalid block.

The governed timings, `stepTimeoutMs` and `blockIntervalMs`, remain the way to set a slow tempo for
everyone, for good, by a ⅔ vote (see [governance.md](governance.md#the-parameters)). The switch is
the way in when the network cannot carry that vote.

### The second path: public bundles

Where direct gossip is blocked but the banks' public faces still get through, the engines exchange
votes and proposals through them on their own. Nobody has to act.

- **What each engine publishes.** An engine with a `publicPath` keeps `hdls/bundle` there, the
  public bundle: the consensus part of its bundle (no payments, updates or name requests), at most
  8 MiB. The engine makes it at each commit and catch-up and at each re-gossip tick, and writes it
  when it changed. Past 8 MiB, as after dozens of rounds, the cut drops what comes last: the pooled
  certificates, then the oldest rounds' votes, never the current round's votes or proposal (see
  [architecture.md](architecture.md#bundles)).
- **When it pulls.** Once the engine has run a height for 4 step timeouts (the governed ones, never
  the switch's) without a commit, it fetches `<public address>/hdls/bundle` of every other current
  validator that publishes a public address. It does so once each re-gossip interval, and imports
  each, until it commits. It follows no redirect, gives up on a connection after 2 s and on a fetch
  after 5 s, reads at most 8 MiB of each, and refuses to connect to the engine's own machine, at a
  loopback address, at `0.0.0.0` or `::`, or at any of its other addresses.
- **What it checks.** Before a pulled vote or proposal reaches the engine's event loop, the pull
  checks it. It must be of the height in progress, or of the last committed height, and the set of
  that height (for the last height, the set that signed the last commit) must vouch for it: a vote
  is by a validator of the set, a proposal names the proposer the set schedules for its round, and
  its signature verifies under the key the set holds. The import stops at the first line that fails
  the check, is unreadable or of an unknown type, or that its intake refuses. The only such line an
  honest public bundle can hold is a certificate the engine refuses, and certificates come last. So
  a forged bundle, from a hostile public face or from anyone on a plain-HTTP path to it, costs the
  engine one check, off the loop, or one line on it. A bank one height ahead gives its last commit,
  which decides the engine's height, but not its messages of the height after: the import stops
  there, and the engine takes them at its pulls at that height. An operator's import on the operator
  port is not checked this way: it hands every line to its intake, and goes on past a refused one.
- **What it costs.** A signature check for each proposal pulled, and for each vote pulled the first
  time, off the event loop. A vote that verified is not checked again: the engine remembers such
  votes, up to 4,096, until its committed height changes.
- **Its log lines.** One as it starts pulling, and one as it stops:

  ```
  public bundles: height 1234 has run for 4 step timeouts without a commit: the node pulls the other validators' public bundles until it commits
  public bundles: the node stops pulling them, having committed height 1234
  ```
- **What it needs.** Every validator's engine has a `publicPath`, a fetcher serves it at the
  public address the validator publishes, and the engines can reach the others' public faces. And
  it needs the slow-down switch, for blocks to commit in the first rounds: without it, the propose
  timer fires after one step timeout, and the pull starts only after four, so round 0 never commits
  by pulls. A height then commits only in a later round, once the round timers have grown longer
  than the pulls take.
- **Catching up the same way.** An engine more than one height behind needs a checkpoint, and the
  public faces serve one. At every tick, catch-up asks every other validator's public address, with
  its bootstrap and gossip peers, so an idle engine whose gossip peers lag with it finds the banks
  ahead too (see [A joining node](#a-joining-node)). So an engine that a restart or a crash left
  behind recovers through the public faces, with no `bootstrap` set.
- **What it does not need.** Trust: every message is signed. Nor the allow list: the pull goes out,
  it does not come in.

### Customers

- **A payment saved as text.** The wallet's send page can sign a payment and show it as text, a
  bundle of one message, instead of sending it. Anyone can carry it to any bank, and that bank
  imports it like any bundle. The wallet spends a block of its payment counter on it, as on a sent
  payment, so it never signs two payments for one block.
- **A checkpoint from a file.** When the wallet can't reach its bank, it can load a checkpoint
  someone carried in, and verifies it as it verifies its bank's. The file's state stands in only
  while the bank's node can't be read, or serves an older state. Meanwhile the wallet sends no
  payment or update to that node: it saves payments as text, and the freeze still works. See the
  [user manual](../ui/USER_MANUAL.md).
- **The limit.** A payment counts only in the block it names, which the wallet picks at most 64
  blocks past the height in progress (`node.MaxBlockIDLookahead`). In pigeon mode that is many
  carries away. But while the rest of the network runs at full speed, the chain passes that block in
  about a minute when it is busy, or about five hours when it idles, and a payment that arrives
  after it is refused. A customer cut off from a fast chain needs some network path, or has to wait
  (owner, 2026-11-01).

## Starting a new network

The node keeps a signature record but no payments: at any committed height, the state is the
accounts and the parameters. So a community can carry its state into a new genesis. This is how the
signature scheme or anything else fixed at genesis changes. It is a planned outage, not a hard fork:

1. Agree a height, and stop every node there.
2. Capture the committed state: `GET /hdls/checkpoint` gives the accounts with the commit that
   finalised them, so the copy can be checked.
3. Write the new genesis file from it: the same names, balances, banks, delegations, public
   addresses and IP addresses, and new keys where needed. A pending change or a frozen mark does
   not carry over. A frozen name comes back unfrozen, under whatever key the file gives it: give it
   a key its rightful holder made, or leave it out and let the ⅔ create it later.
4. Choose a **new `networkName`**. An identical genesis is the same network, and replaying it lets
   anyone freeze the banks with their own old votes (see
   [security.md](security.md#the-network-id)).
5. On every node, delete the snapshot, its `.votes` sign log, and the old network's record files
   (`<snapshotPath>.<old network>.record/` and `.lifelong.jsonl`: the 16 hex digits in their names
   are the old network's). Restore no old backup. The engine writes its public folder again as it
   starts. A signer refuses a sign log of another network: give it a new one, started once with
   `-resume-above 0`, and keep the old one apart.
6. Restart each fetcher with the new network's record files in `-record` and `-lifelong`, and purge
   the CDN's cache: a complete record unit is cached for good, under a path that does not name the
   network.
7. Wallets start with the new network name (`-network`), and with the new validator list in their
   `.trust` file, or with no `.trust` file to pin on first use: a wallet refuses a pin of another
   network.

A release that changes the state digest, the block hash or a signed preimage is the same kind of
move: old and new nodes compute different hashes and cannot agree. Never roll it node by node. The
block hash covers a block's content as JSON, as Go writes it, so a field added to, renamed in or
dropped from anything a block carries can change it, and so can a change to how `ledger/hash.go`
builds it. A signed preimage is what `LedgerParameters.fields` lists, or the fields a message
signs (`signFields`, `voteFields`, `parameterVoteFields`). Only a release that changes none of
these can roll node by node. Even then, run one release on every node: each must hash and verify
exactly as the others do.

## Trying it instantly

`make demo` builds the node, the key generator, the demo and the wallet, then starts one node on
`127.0.0.1:8080` and the wallet with its admin page on `127.0.0.1:8091`. The founder is the only
validator and the wallet's account; the bakery and the cafe are customers of the founder's bank. A
key change waits 20 blocks there. The demo prints the command for a second wallet, on a new key,
to try a registration at the founder's bank. Ctrl-C stops it and deletes its files. The founder's
"Freeze" button is refused there: the founder is the only validator, and a freeze may not leave no
validator seated (spec R9).

The demo's node serves every route on its one port, with no allow list. Its genesis publishes that
port as the founder's public address, and its address as the founder's IP address, so the demo's
`-listen` takes an IP address and a port, such as `127.0.0.1:8080`: `localhost:8080` is refused.
`0.0.0.0:8080` or `[::]:8080` serves on every interface; the founder then publishes `127.0.0.1` as
its IP address, and `http://127.0.0.1:8080` as its public address, since the ledger takes neither
`0.0.0.0` nor `::` as a name's address.

## Four banks on one machine: the quickstart

`make quickstart` runs a network of four banks on this machine, in containers, with the steps of
this runbook done for you. It needs Docker with Compose, or Podman (below).

```
make quickstart
```

1. **It builds the images** (`make images`): `go-ledger`, with the engine, the signer, the fetcher
   and `keygen` (`deploy/Dockerfile`), and `go-ledger-ui`, the wallet (`deploy/ui.Dockerfile`). Each
   program is a static binary in an empty image, with no shell, run as the numeric user 65532, never
   root. `.dockerignore` keeps out of the build `deploy/quickstart/`, `bin` and `.git`, and, under
   `ui/`, the wallet's files: a key file named `*.json` (`wallet.json` by default), the files the
   wallet writes beside it, and any `.state` or `.trust` file. A key file of another name under `ui/`
   would go in: keep it elsewhere.
2. **The first time, it writes a new network** into `deploy/quickstart/`, with `cmd/quickstart`:
   - the genesis: the network `quickstart`, gossip port 8070, the default parameters but for a key
     change that waits 20 blocks and a record that keeps 1,000 blocks, and four banks, `bank-a` to
     `bank-d`, each a validator of power 1;
   - for each bank, such as `bank-a`: its key, sealed (`bank-a.key.json`), under a new random
     passphrase (`bank-a.passphrase`); its signer's config (`bank-a.signer.json`); a secret for each
     of its signer's two clients (`bank-a.engine.secret`, `bank-a.wallet.secret`); the folder of its
     signer's logs (`bank-a.signer-logs/`), with the sign log of a new network in it, as
     `-resume-above 0` writes one; its engine's config (`bank-a.json`), which names the signer and
     holds no key; its wallet's file (`bank-a.wallet.json`), which holds its public key only; and
     the password its wallet asks for (`bank-a.wallet.password`);
   - a wallet file for each of two funded customers, `alice` at `bank-a` and `bob` at `bank-b`,
     500 each;
   - `network.env`, which names the engines' record files for the fetchers.

   It prints the network id and the commands for the two customers' wallets. It refuses a folder
   that is there already, so it never replaces the keys of a network that may be running: later runs
   start the same network again.
3. **It starts `deploy/compose.yaml`**: each bank is an engine, a signer, a fetcher and a wallet.

| Container | Address | Ports |
|---|---|---|
| `bank-a-engine` to `bank-d-engine` | `172.28.70.11` to `172.28.70.14`, on the compose network `ledger`, `172.28.70.0/24`; the genesis lists them as the banks' IP addresses. Each also joins its bank's signing network | the gossip port 8070, and the operator port 8080, on those networks only |
| `bank-a-signer` to `bank-d-signer` | `172.28.71.254` to `172.28.74.254`, each on its bank's signing network only: `signing-bank-a`, `172.28.71.0/24`, to `signing-bank-d`, `172.28.74.0/24` | 7000, on its signing network only |
| `bank-a-fetcher` to `bank-d-fetcher` | `172.28.70.21` to `172.28.70.24`; the genesis gives their names, `http://bank-a-fetcher:8080` and so on, as the banks' public addresses | 8080, published on this machine only, at `127.0.0.1:18081` to `127.0.0.1:18084` |
| `bank-a-wallet` to `bank-d-wallet` | `172.28.70.31` to `172.28.70.34`, on `ledger`, and on its bank's signing network | 18091 to 18094, published on this machine only, at `127.0.0.1:18091` to `127.0.0.1:18094` |

- **The signers.** Each bank's signing network is internal, so nothing on it reaches out, and no
  container joins it but the signer and its two clients, the bank's engine and wallet. The signer's
  `allowFrom` is that network's subnet, and no port of a signer is published. The engine and the
  wallet take their addresses on their bank's signing network from its pool: Podman 3.4 keeps one
  fixed address per container, on its first network by name, and theirs is on `ledger`.
- **Their clients.** `engine` may ask for `block-revision`, `proposal` and `receipt`. `wallet` is the
  desk and the admin page in one, so it may ask for the desk's domains and the admin page's together:
  `registration`, `name-request-bank`, `freeze`, `parameter-vote`, `reassignment-vote`, `payment`
  and `account-update` (see [signature.md](signature.md#its-clients-and-their-domains)).
- **The engines wait for their signers.** An engine's key self-test fails until its signer answers,
  and stops it; `restart: on-failure` starts it again, so its first lines may be such failures.
- **Each signer** runs in the folder `/quickstart`, where only its own bank's files are mounted, under
  the names its config gives them: the genesis, its config, its key, its passphrase and its clients'
  secrets, read-only, and the folder of its logs.
- **The banks' wallets** sign through their signers, and their wallet files hold the banks' public
  keys only, so they ask for no passphrase. Open `http://127.0.0.1:18091` for `bank-a`'s. The
  wallet's node is its engine's operator port, so the desk and the admin page work there. On
  `ledger`, any other container reaches the wallet's port, and whoever drives a bank's wallet signs
  as the bank through its signer. So each one asks for a password, under any user name: the one in
  `deploy/quickstart`, such as `bank-a.wallet.password`, mounted as a secret. A wallet listening
  anywhere but a loopback address refuses to start without one. On a real network, keep a bank's wallet where
  only its operator reaches it as well: the password crosses the network as it is.
- **Their check of the other banks.** A bank's wallet there reaches the four banks' public
  addresses, names on `ledger`, so it checks them all.
- Each engine's allow list lets in the other three engines' addresses only. No port of an engine is
  published on this machine. On a real network the operator port sits on the bank's own network;
  here every container on `ledger` shares one, and each engine's start log warns that its operator
  port is on every interface. So here a validator could publish another engine's operator port, such
  as `http://172.28.70.11:8080`, as its public address, and every stuck engine would pull that
  engine's full export. It is a trial: on a real network no other bank reaches an operator port.
- Every container of the compose file on `ledger` has a fixed address there, so none of them takes
  a stopped engine's address from the network's pool: the other engines would let it in. A container
  you start on that network yourself still takes an address from the pool, and may take a stopped
  engine's.
- There is no CDN: the fetchers are the public faces.
- Each engine keeps its snapshot, sign log, record files and public folder in a volume of its own,
  `bank-a-data` and so on, which its fetcher mounts read-only. Both run as the same user, so the
  fetcher can read the record files. Each bank's wallet keeps its `.trust` and `.state` files in a
  volume, `bank-a-wallet` and so on.
- Each engine's config and its client secret, and each wallet's client secret and password, are
  mounted read-only, as Compose secrets.
- The folder `deploy/quickstart/` is readable by its owner only, but what the containers read inside
  it is readable by anyone, the keys, the passphrases and the secrets included, and the folders of the
  signers' logs are writable by anyone, since the containers' user is not yours. The passphrase sits
  beside its key. Git and the image builds leave the folder out (`.gitignore`, `.dockerignore`). It
  is a trial: put no value on its keys. On a real network, see [Running a signer](#running-a-signer).

**Every Compose command needs `--env-file deploy/quickstart/network.env`.** That file gives the
fetchers the first 16 hex digits of the genesis hash, which name the engines' record files, and
Compose refuses the compose file without it. For example, to follow bank-a's engine, then its
signer:

```
docker compose -f deploy/compose.yaml --env-file deploy/quickstart/network.env logs -f bank-a-engine
docker compose -f deploy/compose.yaml --env-file deploy/quickstart/network.env logs -f bank-a-signer
```

The signers write their sign logs and audit logs into `deploy/quickstart/bank-a.signer-logs/` and so
on, as the containers' user, readable by that user only. Read them with `sudo cat` under Docker, or,
under rootless Podman, as the user namespace's root:

```
podman unshare cat deploy/quickstart/bank-a.signer-logs/audit-log.jsonl
```

`make quickstart-clean` removes the whole network: its containers, its Compose networks, its volumes
with the chain, and `deploy/quickstart/` with the keys and the signers' logs. The next
`make quickstart` makes a new one. Don't remove the volumes alone (`docker compose down -v`): the
chain then starts again from its genesis, while the signers' sign logs in `deploy/quickstart/` stay
at the heights they reached, and every signer refuses each vote below them. `make quickstart-clean`
removes both.

**A customer's wallet.** Build the wallet, then run a customer's wallet on this machine against its
bank's fetcher, as the quickstart prints:

```
make build
bin/go_ledger_ui -node http://127.0.0.1:18081 -network quickstart -wallet deploy/quickstart/alice.wallet.json -listen 127.0.0.1:8090
```

Open `http://127.0.0.1:8090`. At its first start the wallet asks for a passphrase to seal the key
(empty keeps it plaintext). Bob's wallet is the same with `18082`, `bob.wallet.json` and
`-listen 127.0.0.1:8092`, so both run at once; his page is at `http://127.0.0.1:8092`.

Its limit there is **the other banks' check.** The public addresses on chain are names on the
Compose network, which this machine cannot look up. The wallet asks every bank at its public
address, its own bank's included, so a wallet on this machine reaches none of the four, and its main
page says that it could only check banks holding 0% of the power. A wallet in a container on the
Compose network reaches all four. Compose names that network `deploy_ledger`,
after the folder of the compose file:

```
head -c 18 /dev/urandom | base64 > carol.password && chmod 644 carol.password
docker run --rm --network deploy_ledger -p 127.0.0.1:8090:8090 -v carol-wallet:/wallet \
    -v "$PWD/carol.password":/carol.password:ro -e GOLEDGER_WALLET_PASSPHRASE=<a passphrase> \
    go-ledger-ui -node http://bank-a-fetcher:8080 -network quickstart -password-file /carol.password
```

It makes a new key, with no name yet, in the volume `carol-wallet`. The container has no terminal
to ask for a passphrase, so `GOLEDGER_WALLET_PASSPHRASE` gives one, which seals the key. Every other
container on `ledger` reaches the wallet, so it asks for the password in `carol.password`, under
any user name: a file of one line, which the first command makes, and which the container's user,
65532, can read. Publish the wallet's port on this machine only, and under the same number: the
wallet refuses a request for another port.

**With Podman.** `make` uses Docker by default. With Podman and the older Compose
(`docker-compose`, version 1), as on Ubuntu 22.04, start Podman's Docker-compatible socket for
your user, point Compose at it, and tell `make` which commands to use:

```
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
make quickstart CONTAINER=podman COMPOSE=docker-compose
```

Give `make quickstart-clean` the same `COMPOSE=docker-compose`, and write `docker-compose` for
`docker compose` in the commands above. Podman 3.4, with container network plugins older than
version 1.0 (Ubuntu 22.04's), writes each Compose network's settings with `"cniVersion": "1.0.0"`,
which those plugins refuse: the containers do not start, and Podman says it cannot find the CNI
network. Set it to `0.4.0` in the files of all the networks, `deploy_ledger` and the four
`deploy_signing-bank-…`, then run `make quickstart` again:

```
sed -i 's/"cniVersion": *"1.0.0"/"cniVersion": "0.4.0"/' ~/.config/cni/net.d/deploy_*.conflist
```

## Reference node, receipts and the pin

A customer's wallet talks to one bank, at the bank's public address, like a branch of a bank, and
that bank's engine gossips its payments on. Each payment names one block, at most 64 ahead, and is
stale after it; if payments often miss their block, raise the wallet's `-lookahead` (at most 32) or
the network's `blockIntervalMs`. The engine answers with a signed receipt, like a card
authorisation. The wallet believes a receipt, and every balance it shows, only if validators it
pinned signed it, in `<wallet>.trust` (see the [wallet README](../ui/README.md)). Publish that list
to your users. A wallet follows the banks as they change, while more than ⅔ of the power it pinned
signs each change; one that missed more than that needs the list again. A wallet reads that file
only at start; how a bank makes the list is in the wallet README's
[Its files](../ui/README.md#its-files). Give every validator a public address on its account
(`publicAddress` in the genesis, or the account form): wallets ask each bank's public address for
its height, to catch a bank that hides newer blocks.

A payment may also carry a pin, `PreviousStateDigest`: the hex `stateDigest` that `/hdls/resolve`
serves, the digest of the committed state. A pinned payment must name the next block, the one built
on that state, and intake refuses a pin for any other block or digest with `409`. The pin binds a
payment to one chain history. Its value depends on trusting the node that reports the digest, so it
goes with running your own node. The wallet does not pin.
