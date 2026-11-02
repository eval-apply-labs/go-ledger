# Phase 3 build: progress and how to resume

Phase 3 splits the deployment and gives the ledger a pigeon mode, as `review/phase3_spec.md` plans.
The spec was written, decided and built on 2026-11-01, on branch `phase3`, from `main` at
`c0130c9`. Every code slice is merged (P1 to P7, `17d4b8d` to `b9defd2`), and the docs (P8) are
written. Three independent reviews of the build found no blocker; their fixes are merged (`c0e5226`
to `b721caa`). The reviewers' recheck found nine more findings, fixed in a last round, and their
final rechecks found nothing new. The docs follow every fix (see
[The independent review](#the-independent-review-2026-11-01)). On 2026-11-02 the branch went to
`main` as one commit (see [the commits these files cite](README.md#the-commits-these-files-cite)).
The words this note uses are in [`docs/glossary.md`](../docs/glossary.md).

## Where the branch stands

`phase3` holds, in order:

- the spec, in four commits (`1f36956`, `8296e9b`, `11eb6d1`, `9645480`);
- the seven code slices, in 29 commits, `17d4b8d` to `b9defd2`;
- the docs (P8), from two writers: `p3-p8a` (`docs/`, `README.md`, `TODO.md` and the findings,
  merged as `92f9623` to `a049c69`) and `p3-p8b` (the wallet's docs, `docs/reading-the-code.md`,
  the spec's "as built" notes, and this record);
- the review's fixes, in 27 commits, `c0e5226` to `b721caa`;
- the wallet's fixes from the recheck, `620fade`, `09e4309` and `d201a2d`;
- the docs brought up to date with the fixes (`p3-docsfix`), `f9184a5` to `aa8b3e6`;
- the engine's fixes from the recheck, in 7 commits, `309d2df` to `ff4d853`;
- the docs' last pass (`p3-docsfix` again).

The check list on the merged tree (spec §11, P8) passed on `014c8a0`, in 691 s. What is left is
the owner's read. Each place the build departed from the spec, or settled what it left open, is a
dated note marked **(as built)** in the spec, with its commits and its reason, and each place a
review fix changed the design, a note marked **(as built, after the review)**.

## The owner's decisions (2026-11-01)

The owner decided the design in one day. The spec's §0.2 and §13 keep their words; the quotes below
are taken from there.

- **D1. One container facing the public, one facing the validators.** "shall we build the phase 3
  where we have one container showing toward the public behind a cdn and the other toward the
  validators within the allow list".
- **D2. The allow list is by IP address, inside the container, fed by the engine.** "i lean for 3a as
  we have an internal routing table already, the firewall must be inside the docker container and
  being fed by the engine tho".
- **D3. The public face is static and public.** "the cdn is public as there is nothing to compute
  just static elements and the bandwith that can be managed in the usual way".
- **D4. Pigeon mode gets a carrier, and a manual switch.** "this ledger's resilient is only as
  resilient as the network operator make it to be … if the internet provider segments it then we
  need to have a secondary way (the pidgeon) to load in and out the messages, maybe even manually",
  then "yes write it with the pigeon section, manual switch".
- **D5. Every name publishes a public address and IP addresses: a name service.** "the address we
  hold now is a publicaddress and we can add an ip address to the keys, so each key is potentially a
  website effectively and if we use the [Ed25519] setup we have a decentralized name service that
  dns operators can adopt and use for payments and tls, embedded with the ability to change
  signature method if needed by moving to a new chain."
- **D6. Name lookups, the account list and governance are public files.** "each of those could be
  its own endpoint, i think since those are cdn based ones and need no interaction they should live
  in the public container."

Every question of the spec's §13 is decided. The last three came in one message: "q1 in process,
q6 A, q13 plain ips, go ahead".

- **Q1:** the firewall runs in the engine's process, at accept (R2).
- **Q2:** today's address becomes the public address, and every name may add IP addresses (D5).
- **Q3:** `resolve`, `accounts` and `governance` are public files too (D6).
- **Q4:** the bank does not sign its status file. Status is a hint; the checkpoint is the signed
  document.
- **Q5:** the fetcher forwards at most 100 submissions a second by default. "i'd make that the
  default but make it editable by the single provider as nothing else depends from it".
- **Q6:** the switch is a config field, read at start (R4).
- **Q7:** the second path pulls the public bundles after 4 step timeouts stuck, the consensus part
  only, at most 8 MiB.
- **Q8:** payments carried out of a cut-off area keep the 64-block window, documented as a limit.
- **Q9:** export and import by `curl` on the operator port, in the runbook.
- **Q10:** a log line when a peer's pushes start failing and when they recover.
- **Q11:** the public checkpoint is written at every commit. "it is the recap and what makes the
  next step verifiable".
- **Q12:** the gossip port speaks plain HTTP; TLS is left to the operator. "if the name server
  becomes tied with this infrastructure in a small community the problem disappears".
- **Q13:** plain IP addresses, at most 4 per name, one gossip port for the network in the genesis
  (R6).

The spec went through four commits that day: written (`1f36956`), the answers to §13 taken
(`8296e9b`), Q1, Q6 and Q13 decided (`11eb6d1`), and the slice plan changed so the fetcher builds
beside P1 (`9645480`).

## The slice plan as run

| Group | Slices | Started after | Merged on `phase3` |
|---|---|---|---|
| 1 | P1 · P3b | `main` | P3b, then P1 |
| 2 | P2 | P1 | P2 |
| 3 | P3a | P2, P3b | P3a |
| 4 | P4 | P3a | P4 |
| 5 | P5 · P6 · P7 | P4 | P6, P7, then P5 |
| 6 | P8 | everything | `p3-p8a`, then `p3-p8b` |

- **P3 was split in two before the build** (`9645480`): the public folder (P3a), which needs the two
  ports, and the fetcher (P3b), a program of its own, which could be built beside P1.
- Each slice was built on a branch of its own (`p3-…`) and merged into `phase3` in the order above.
  Slices of one group shared no file, as the plan asks.
- **P8 had two writers side by side,** each owning its own files, so their commits could not
  conflict.

## Slices done

| Slice | Commits on `phase3` | From | Production lines (+/−) | Test lines (+/−) |
|---|---|---|---|---|
| P3b the fetcher | `17d4b8d` | `p3-fetcher` `bc5924b` | 293 / 0 | 433 / 0 |
| P1 addresses on chain | `e0cabf2`, `7a28ab4`, `64aef7c`, then `9528077` | `p3-p1` `f0ae35c`, `fb31082`, `807cca6` | 313 / 175 | 403 / 163 |
| P2 the two ports and the allow list | `9f34ba4`, `28c9c80`, `6c99958`, `f45b7c4` | `p3-p2` `821c9c7` to `65401af` | 283 / 63 | 448 / 30 |
| P3a the public folder | `ba78807`, `1b92a19`, `503f640` | `p3-p3a` `62b0fa0`, `3ee9879`, `a569efc` | 375 / 43 | 403 / 0 |
| P4 bundles and the switch | `30b9758`, `83f5aa5`, `e0f7149`, `8e2e840`, `b601914`, `7b4871f`, `cc1af7f`, `e9e7cb8` | `p3-p4` `2357e75` to `78b51e1` | 510 / 146 | 636 / 22 |
| P6 the wallet | `a456b3e`, `76a7b42`, `96a84e9` | `p3-p6` `b4d73fb`, `41c872d`, `19dd083` | 201 / 30 | 402 / 1 |
| P7 images, compose, the quickstart | `1a57001`, `c461754`, `0f81369`, then `58b7c66` | `p3-p7` `a99b3fd`, `043e905`, `d2a17b1` | 242 / 0, and 222 / 4 in `deploy/`, the `Makefile` and the ignore files | 199 / 0 |
| P5 the second path | `75ddb3b`, `b9defd2` | `p3-p5` `1a46cad`, `7820c1a` | 102 / 12 | 194 / 0 |

Production lines count the Go files and the wallet's templates. `9528077` and `58b7c66` were added
on `phase3` after their slice merged. In all, Phase 3's code is 2,319 production lines added and
469 removed, and 3,118 test lines added and 216 removed.

### P3b. The fetcher (`17d4b8d`)

`cmd/fetcher`, the origin a CDN pulls from. It serves an explicit list of paths from the public
folder and the record files, with the `Cache-Control` of spec §6.3, and passes the three customer
submissions to the engine's operator port under one global cap. It holds no key and keeps no state.
Any other path is `404`, another method `405`. `make build` builds it.

Checked: the tests cover every listed path by GET and HEAD, the refusals, a complete and a growing
record unit, the answers passed back, the 1 MiB cap and the 429 cap. Removing the clean-path check,
the regular-file check or the `413`, or moving the completeness rule by one, each fails a test.

### P1. Addresses on chain (`e0cabf2`, `7a28ab4`, `64aef7c`, `9528077`)

- An account's `Address` became `PublicAddress`, and every name may publish up to 4 `IPAddresses`,
  each in its one plain form (`ledger.CanonicalIPAddress`). Both are block rules, in the account
  digest and in an update's signed fields.
- The genesis gained `gossipPort`, required and hashed into the genesis hash; the usual value is
  8070 (`9528077`).
- The peer DNS builds each validator's gossip URLs from its IP addresses and the port. The
  transport holds each peer as a list of URLs, and tries the next when one gives no answer.
- The wallet's check of the other banks reads the public address, and its account form publishes
  both fields.
- The demo's founder publishes its node as its public address and the node's IP address.

### P2. The two ports and the allow list (`9f34ba4`, `28c9c80`, `6c99958`, `f45b7c4`)

- `Server.GossipHandler` serves only the gossip routes; `Handler` keeps every route.
- The allow list is computed on the loop from the chain and the config, published with the
  committed view, and enforced at accept by `allowListListener`. Refusals are logged once a minute.
- The transport logs one line when a peer's pushes start failing, and one when it answers again.
- `cmd/server` gained `gossipListen` and `allow`, binds both addresses before the node opens, and
  serves the gossip port behind the list.

Beyond the spec, a connection the list let in is checked again after each read, and closed once its
address has left the list. Without it, the end-to-end test failed: a validator that took its address
off the chain still had its precommit counted two heights later. The end-to-end test runs four
validators on `127.0.0.1` to `127.0.0.4`, each sending from its own address.

### P3a. The public folder (`ba78807`, `1b92a19`, `503f640`)

The engine writes the public folder under `publicPath`: the status, checkpoint, accounts and
governance, byte for byte the route answers, and a record per name and per key. The loop makes the
content; a writer writes it off the loop, one file at a time, through a temporary file and a rename.
`hdls/status` goes last, after the commit's record entry. A node catches up from a server that holds
only these static files.

Checked: the files are the route answers after a commit; a name's record is rewritten only when it
changes; the key's file follows a key change; `Open` removes stale files and keeps a matching
record's height; a reader that keeps reading while blocks commit never sees a partial file.

### P4. Bundles and the switch (`30b9758` to `e9e7cb8`)

- The intake of updates, name requests and certificates reports a newly pooled message, as the
  payment intake did, so an import counts what it took.
- Bundles: `GET /hdls/bundle` exports and `POST /hdls/bundle` imports, on the operator port only.
  Re-gossip and the export share `consensusMessages`, last commit first.
- A carried checkpoint: `POST /hdls/checkpoint`, verified as catch-up verifies one, with the
  reason when it is refused.
- The slow-down switch, `minimumStepTimeoutMs`, lengthens the round timers only.
- The public folder's `hdls/bundle`, and the fetcher serves it as `application/x-ndjson`.
- The pigeon test: four validators with no network link at all, and a test courier.

Checked: with the switch at 1 s, the courier's chain commits 4 heights in 12 carries, in practice
each in round 0; with it off, none in 6 carries, against two with it on. **Ten runs under `-race`
gave the same carries and rounds each time.** A test checks that no non-test file of `ledger` or
`consensus` imports `time`, which the switch's safety relies on.

### P6. The wallet (`a456b3e`, `76a7b42`, `96a84e9`)

- A customer's wallet works against a bank's public face. A `404` to a certificate becomes "votes
  are submitted on the bank's operator node", and the wallet probes no route.
- "Sign and save as text": a one-message bundle, which takes a block of the payment counter as a
  sent payment does.
- A checkpoint from a file, pasted into the main page, verified under the pin as the node's is, and
  shown "as of height H, from a file".

Checked against a stand-in for the fetcher in front of a real engine: the customer's pages ask for
nothing a public face does not serve; a saved payment imported at the bank commits; a forged file,
another network's, an older one and one where too many banks re-keyed are refused, and one the ⅔
re-keyed moves the pin.

### P7. Images, compose and the quickstart (`1a57001`, `c461754`, `0f81369`, `58b7c66`)

- `deploy/Dockerfile` (the engine, the fetcher and keygen) and `deploy/ui.Dockerfile` (the wallet):
  static binaries in `scratch` images, run as user 65532.
- `cmd/quickstart` writes four banks' keys, the genesis, the configs and two customers' wallet files;
  `deploy/compose.yaml` runs each bank as an engine and a fetcher.
- `make images`, `make quickstart`, `make quickstart-clean`.
- `58b7c66` added `.dockerignore`, so the quickstart's private keys never reach the builder.

**Checked for real on the dev machine, with Podman 3.4:** four banks; payments from wallets on the
machine itself, through the fetchers; heights 2 to 10 on all four engines; the allow list refusing a
stranger (`172.28.70.19`); the record units served; and `bank-d` restarted and caught up. Podman 3.4
needed its network plugins' config to say `cniVersion` 0.4.0. The verified run used a network created
beforehand and a compose override; the runbook's Podman steps were not run as written.

### P5. The second path (`75ddb3b`, `b9defd2`)

When a height has run for 4 step timeouts without a commit, the node pulls the other validators'
public bundles at each re-gossip tick, one round of fetches at a time, off the loop, and imports
them.

Checked: driven by hand, the node pulls exactly when stuck and only then, skips a tick while a round
runs, and stops at the commit; mutations of each rule (the switch's timeout, 3 step timeouts, no
driver check, no skip, itself not excluded, the clock not reset at a wake) each fail the test. End to
end, four validators whose transports deliver nothing commit by pulling each other's bundles, with
the switch on. **Ten runs under `-race` all passed, in 2.7 to 3.0 s.**

### P8. The docs

- `p3-p8a` rewrote the area docs for the two ports, the allow list, the public face, the bundle, the
  switch and the name service; the runbook gained "When the network splits" and the quickstart; the
  README, `TODO.md` and the findings say what is built.
- `p3-p8b` brought the wallet's README and manual up to date (which address, the payment saved as
  text, the checkpoint from a file, the privacy note), recounted and extended
  `docs/reading-the-code.md` (four new steps and a fourth journey), added the spec's "as built"
  notes, and wrote this record.

Checked in `p3-p8b`: every `go test` command of the guide was run with `-v -count=1`; each passes and
selects the tests it says. The new steps' commands also pass on a copy of the tree that holds only
the packages up to their step. `gofmt -l .` prints nothing, `go vet` and `go build` pass on both
modules, and `go test -race -count=1 ./...` and `go test -C ui -race -count=1 ./...` pass on
`b9defd2`.

## Open items for the review

The review ran on `b9defd2`. These are the points the build left for it; what it found is in the
next section.

**Departures and readings to confirm** (each is a note in the spec):

- the allow list's recheck of open connections (§5.3), about 15 lines beyond the spec;
- the bundle's order, last commit first (§7.3);
- the `Admit…` methods' `(newlyPooled, err)` (§7.4);
- another network's checkpoint refused as "does not verify", since a checkpoint carries no network
  id (§7.5);
- the fetcher's `413` and `502` (§6.2);
- P6's readings: the file's state stands in whenever the node's does not verify; it is held in memory
  only but moves the newest height and the pin; it needs a pin; it is pasted, up to Go's 10 MB form
  limit (§7.8);
- the desk works at a public face too (§8);
- the second path's end-to-end test runs with the switch on, since round 0 can't commit by pulls
  alone (§7.7, §10).

**Known limits:**

- The fetcher must run as the engine's numeric user: the record files are 0600 in 0700 folders. The
  compose file runs both as 65532. Changing the modes is the other way.
- No fsync on the public files (`Open` rewrites them), and `Stop` waits for their writer, so a disk
  that hangs holds `Stop` up.
- The pull's fetches are not tracked: they may outlive `Stop`, for 5 seconds at most.
- The public bundle is made on the loop at every re-gossip tick, even when nothing changed. Under the
  race detector, with about 25 votes, that took about 80 ms, since `EncodeBundle` marshals each body
  and then its line.
- An import answers how many messages it refused, not why: a bank can't tell a customer why a saved
  payment was refused.
- The import reads the whole bundle, up to 64 MiB, before it takes a line.
- `Account` and `AccountUpdate` have no JSON tags, so the wire and snapshot spell `PublicAddress` and
  `IPAddresses`, while the genesis file and the resolve answer spell `publicAddress` and
  `ipAddresses`. `Address` was the same before.
- With the quickstart, a wallet on the machine itself checks the other banks at 0%: the public
  addresses are names on the compose network. A wallet in a container on that network checks them
  all.
- The wallet's public-face tests use a stand-in for the fetcher, since `cmd/fetcher` is a `main`
  package the wallet's module can't import.

**Found while writing the docs (code is unchanged):**

- `postJSON` turns any `404` into "votes are submitted on the bank's operator node", also for
  `/hdls/update` and `/hdls/name-request`, which a public face does serve. There a `404` means a
  wrong `-node`, and the message misleads.
- The fetcher's default `-listen` is `:8080`, the operator port's default too, so on one host the two
  collide; the runbook uses `:8081`.
- `cmd/quickstart` prints both customers' wallet commands without `-listen`, so both would take 8090;
  the second needs `-listen 127.0.0.1:8092`.
- `docs/why-small-and-auditable.md` ("three containers … planned") and `docs/signature.md` ("Phase 3
  planned") still speak of Phase 3 as a plan.
- Not checked against a running system: the compose network's name, `deploy_ledger`, and the Podman
  steps the runbook gives.
- A request can't be drafted while the page shows a file's state: the draft reads the node's
  `/hdls/status`. The spec's offline steps are the payment and the checkpoint only.

## The independent review (2026-11-01)

Three reviewers read the build on `b9defd2`, each one area. None found a blocker. Their reports are
in [`_evidence_phase3/`](_evidence_phase3/README.md).

| Review | Area | Major | Minor | Nits |
|---|---|---|---|---|
| 1, network ([report](_evidence_phase3/review1_network.md)) | the two ports, the allow list, the transport, the fetcher, the config and the containers | 1 | 7 | 5 |
| 2, bundles ([report](_evidence_phase3/review2_bundles.md)) | bundles, the public folder, the switch and the second path | 1 | 4 | 3 |
| 3, wallet ([report](_evidence_phase3/review3_wallet.md)) | the wallet and the new ledger fields | 1 | 4 | 5 |

Every finding is fixed on `phase3`, or, for N10, N12 and W5, answered in the docs. Each fix
commit names its finding, and the spec's notes marked **(as built, after the review)** say what
each fix changed.

**Review 1, the network surfaces.**

| Id | Kind | What | Fix |
|---|---|---|---|
| N1 | major | The allow list held the node's own addresses, and any loopback address a validator published. A front that rewrites the caller's address made every caller one of those, and let everyone in. Now the list leaves both out, so such a front fails closed | `c0e5226`, `e557312` |
| N2 | minor | The fetcher validated by `Last-Modified` only, which counts whole seconds: a file rewritten within the second was answered `304`, and a short record unit could be kept for a year. Now an `ETag` alone | `fd500f1` |
| N3 | minor | A push moved to a peer's next URL after any error, so a slow peer got a message once per URL. Now only when the connection fails, or is not made within 1 s (2 s since N15, below) | `8029499` |
| N4 | minor | A peer that flapped got a log line per message. Now at most one a minute per peer | `8029499` |
| N5 | minor | A listed path's `404` had no `Cache-Control`, so a CDN kept it for its own default. Now `max-age=1` | `fd500f1` |
| N6 | minor | Beside a gossip port, `listen` defaulted to every interface. Now `127.0.0.1:8080`, and the start log warns of an operator port on every interface | `03fd051` |
| N7 | minor | A validator that published a loopback public address made every stuck bank pull its own operator port's export. Fetches from public addresses now refuse loopback and unspecified destinations | `96aa8ef` |
| N8 | minor | Catch-up never fell back to the validators' public addresses, and asked every URL of a peer. Now it does, and asks each peer at its first URL that answers | `02feef7`, with B1 |
| N9 | nit | The compose file's fetchers took addresses from the pool, so one could take a stopped engine's. Now every container has a fixed address | `f716fb3` |
| N10 | nit | Record paths name no network, so a CDN keeps an old network's units after a new genesis | docs: the runbook already said to purge the CDN after a new genesis; its CDN notes now point there too |
| N11 | nit | A gossip port off the genesis `gossipPort` was not reported. Now the start log says so | `03fd051` |
| N12 | nit | A wallet on the quickstart's machine reaches no bank at its public address, a name on the compose network | docs: the runbook and the wallet's README say it checks banks holding 0% of the power, its own bank's included, and how to run a wallet on the compose network |
| N13 | nit | `.dockerignore` let a wallet's key file under `ui/` into the build | `f2fdd3b` |

**Review 2, bundles, the public folder and pigeon mode.**

| Id | Kind | What | Fix |
|---|---|---|---|
| B1 | major | The second path carries no checkpoint, and catch-up asked only what a split blocks: a validator two or more heights behind never caught up without `bootstrap`. Catch-up now falls back to the other validators' public addresses | `02feef7`, then `b721caa` |
| B2 | minor | In pigeon mode, a node back without its sign log waited two re-gossip intervals, not a carry: a proposer could sign a second block and be burned. The wait is now 2 × max(step timeout, switch) | `c7d6383`, `c2e26e2` |
| B3 | minor | Past 8 MiB, the public bundle cut the current proposal first, then whole validators' votes. Now the current round comes first, and the cut drops the oldest rounds | `dae317b` |
| B4 | minor | A hostile public face could make a stuck node's loop check about 1,800 forged votes at every pull. Pulled votes are now checked off the loop, and the import stops at the first that fails | `5f26175` |
| B5 | minor | `changedAtHeight` differed between banks for one unchanged name. It is gone: every bank publishes the same record bytes | `11e6d5e` |
| B6 | nit | Every public write went through one temporary file, `.incoming/next`, unsafe for two writers sharing a folder. Now a temporary file per write, and `Open` clears `.incoming` | `a5016b5` |
| B7 | nit | An idle node rewrote `hdls/bundle` at every tick, and `Open` every record file. Now only what changed | `d403dc3` |
| B8 | nit | The second path's test did not check that the nodes decided the same blocks. It does now | `bd18774` |

**Review 3, the wallet and the new ledger fields.**

| Id | Kind | What | Fix |
|---|---|---|---|
| W1 | major | A loaded file stood in whenever the node's state failed to verify, so it hid a node the pin does not vouch for, and payments went to it; a freeze named a stale block. Now a file stands in only when the node can't be read or serves an older state, and every other refusal shows | `2b3229c`, `da2857b` |
| W2 | minor | A CDN's checkpoint, kept 5 s, named passed blocks, and two copies of different ages raised the rollback alarm. The checkpoint is kept `max-age=1`, and the wallet shows the newest state it verified while the node serves an older one | `fd500f1`, `2b3229c` |
| W3 | minor | A reload of the saved-payment page signed a second payment. The save step now posts, redirects and gets | `fab032a` |
| W4 | minor | Go's 10 MB form limit refused a checkpoint of about 3,400 accounts or more as cut short. The form now takes a file as large as a node takes, 64 MiB | `b1a685c` |
| W5 | minor | A first use through a public address pins whatever banks that address and its CDN serve | docs: the wallet's README and manual, the security doc and the runbook say so, and tell customers to get the `.trust` file from their bank or do the first use at the bank |
| W6 | nit | Every `404` said where votes are submitted. Now only a certificate's | `368c0f1` |
| W7 | nit | The banks-changed message spoke of a node when a file was refused. It names the source now | `5a44f92` |
| W8 | nit | A file verified while another page verified a newer state could be stored below it. The newest state is now kept under the same lock as its check | `2b3229c` |
| W9 | nit | The demo refused `-listen 0.0.0.0:8080`. An unspecified IP now publishes `127.0.0.1` | `55bdfe4` |
| W10 | nit | The wallet had no test against the real fetcher. `ui/fetcher_test.go` builds it and runs one | `9892c0b` |

**Beside the findings:**

- `5409243`: the quickstart prints each customer's wallet command with `-listen`, 8090 for alice
  and 8092 for bob, so both run at once (found while writing the docs, above).
- `b721caa`, the orchestrator's follow-up to B1 and N8: with the public addresses as catch-up's last
  source, every node asked every other bank's public face for its status at each catch-up tick.
  Now catch-up asks them only when no bootstrap or gossip peer answered at all, or while the node is
  stuck on its height. `bestCheckpoint` also reports whether any peer answered.
- Of the items found while writing the docs (above), W6 fixed the `404` message, `5409243` the
  quickstart's wallet ports, and the docs touch-up the two stale docs. The fetcher's default
  `-listen`, `:8080`, still collides with the operator port's on one host; the runbook gives
  `-listen :8081`.
- `TestAllowListEndToEnd` changed meaning with N1: the fourth validator, whose loopback address only
  the chain lists, is refused by the other three, which commit without it.
  `TestAllowListFollowsTheCommits` covers following the chain.

**The docs touch-up** (`p3-docsfix`) brought the docs written against `b9defd2` up to date with the
fixes: the runbook, the API, security, architecture, consensus, signature, governance and glossary
docs, the README and `TODO.md`, the wallet's README and manual, the guide to reading the code (its
commands rerun with `-v -count=1`, each selecting the tests it names), and the spec's notes. It
says what Phase 3 built in `docs/why-small-and-auditable.md` and `docs/signature.md`, which still
called it planned. It also fixed the comments the fixes left stale, in `node/httptransport.go` and
`node/idle.go`, and a fetcher test's fixture that still held `changedAtHeight`.

### The recheck and the last round

Each reviewer rechecked the fixes of its area on `b721caa`, and read the last round's fixes on
`ff4d853`. The reports are in [`_evidence_phase3/`](_evidence_phase3/README.md): the network's
([recheck](_evidence_phase3/recheck1_network.md)), the bundles'
([recheck](_evidence_phase3/recheck2_bundles.md)) and the wallet's
([recheck](_evidence_phase3/recheck3_wallet.md)), each with its final recheck at its end. No recheck
found a blocker.

**Recheck 1, the network surfaces.** Nine of the eleven code fixes held. N1 and N7 held only in
part, each with a new way around the fix, and N8 left a nit. The 1 s connection timeout of N3 made
one new finding.

| Id | Kind | What | Fix |
|---|---|---|---|
| N14 | minor | Rootless Podman's default port forwarding makes every caller the container's own address, `10.0.2.100`, the same on every such host. One validator that published it opened a bank published that way to everyone, with no refusal logged. The gossip port now refuses a connection whose two ends are one address | `309d2df` |
| N7, rest | (N7) | A node still reached its own operator port through one of its own addresses that is not loopback, such as `10.0.2.100`. The fetches from public addresses now drop a connection whose two ends are one address | `d27e72f` |
| N15 | minor | A connection timeout of 1 s is when Linux sends a connection's first packet again: one lost packet failed a push. It is now 2 s | `479cdc0` |
| N8, rest | (nit) | Catch-up's clients waited 5 s for each silent URL. Their connections now give up after the same 2 s, at start too | `479cdc0` |

The final recheck, on `ff4d853`: all four fixed, nothing new. It names two limits of N14, neither
of which opens the port: a connection that leaves from `127.0.0.1` to the node's own LAN address is
not caught, which matters only if an operator writes `127.0.0.1` in `allow`; and Docker's userland
proxy runs outside the engine's network namespace, so its callers show as the bridge's gateway,
which the refusal does not catch. The docs say so: with Docker, publish the gossip port through
iptables, its default, and never list a bridge's gateway in `allow`.

**Recheck 2, bundles, the public folder and pigeon mode.** All eight fixes held, B4 only for votes.
Four new findings, and a side note.

| Id | Kind | What | Fix |
|---|---|---|---|
| R2-1 | minor | B4's check covered votes only: a hostile public face could still spend a stuck node's loop with forged proposals, empty ones, or certificate lines the loop refuses one by one. Pulled proposals are now checked off the loop, and the pull's import stops at the first line that fails, is unreadable, or is refused | `2f9100f` |
| R2-2 | minor | An idle validator runs no round, so it is never stuck. When a peer answered at its own height, as one as far behind on its side of a split does, catch-up skipped the public faces until the heartbeat, about 5 minutes. Now, while a peer answers and none is ahead, catch-up asks one public face a tick, in turn | `ff4d853` |
| R2-3 | nit | B3's "newest round first" put a rogue validator's future-round votes, up to about 2.4 MB, before the past rounds. Now the past rounds come first, newest first, then the future ones, nearest first | `6d3df82` |
| R2-4 | minor | The pull verified every vote of every bundle at every pull: about 0.6 s of CPU per bundle of 1,200 votes. It now remembers the votes it verified, up to 4,096, until the committed height changes | `4a41d68` |
| side note | (nit) | A validator could publish an address of the node's own machine, or, in the quickstart, another engine's operator port | `d27e72f` for the node's own machine; the quickstart's case is a trial's, which the runbook now states |

The final recheck, on `ff4d853`: all four fixed, nothing new, and two optional nits, which stay as
known limits:

- with the puller's certificate pool full, a new honest certificate is refused at every pull, and
  the certificates after it in that bundle never come by this path;
- a rogue validator that signs more than 4,096 distinct votes a height can keep resetting the
  memory of verified votes, which costs CPU only, off the loop.

**Recheck 3, the wallet.** All nine code fixes held, W2's but for its part (c), which stays by
design (below). Three new nits.

| Id | Kind | What | Fix |
|---|---|---|---|
| R3-1 | nit | While a file stood in because the node's checkpoint could not be read, *Sign and send* still signed for the file's block, used up a block of the counter, and posted to that node. *Sign and send* and the account form now refuse while a file's state stands in | `620fade` |
| R3-2 | nit | A pasted text past 64 MiB, up to 192 MiB, passed the form's bound and was taken. It is refused as too large | `09e4309` |
| R3-3 | nit | With `-listen 0.0.0.0:8080`, the demo founder published `127.0.0.1` as its IP address but `http://0.0.0.0:8080` as its public address. Now `http://127.0.0.1:8080` | `d201a2d` |

The final recheck, on `ff4d853`: all three fixed, nothing new. **A known limit, by design:** W2 (c).
The kept state is in memory only, so after a restart a node that serves an older state than the
newest the wallet verified raises the rollback alarm, even when the node is only behind. The new
tests assert it.

**The docs' last pass** (`p3-docsfix`) brought the docs, the guide to reading the code (its
commands rerun with `-v -count=1`) and the spec's notes up to the last round. It copied the three
rechecks into the evidence, made the comment at `bothEndsOneAddress` say which forwarders it
catches, and, in tests and comments only, named the lost-sign-log wait by that name and wrapped
long comment lines.

## What remains after Phase 3

- **The signer.** The key off the networked machine, in a container or device of its own (spec R5).
  It stays in `TODO.md` as the next step.
- **The rest of the name service** (spec R7, §13's notes for later):
  - a DNS gateway, a small program that answers DNS queries for a zone from the chain's name
    records;
  - TLS key records, signed by the name's key, as DANE's TLSA records are, so that TLS works with no
    certificate authority wherever the client reads the chain.
- Out of scope, as the spec's §1 says: TLS on the gossip port, an automatic slow-down, per-client
  rate limits (the CDN's job), a longer window for payments signed offline, the archive node and the
  snapshot write off the event loop.

## How to resume

1. Make one worktree per change, from `phase3`. For example:
   `git worktree add -b p3-fix <scratch>/wt/p3-fix phase3`.
2. Merge each change into `phase3`, then run the check list on the merged tree, with
   `GOTOOLCHAIN=auto` and `GOLEDGER_CHAOS_SLACK=4`:
   - `gofmt -l .`, which must print nothing (CI checks `review/` too);
   - `go vet ./...` and `go vet -C ui ./...`;
   - `go build ./...` and `go build -C ui -o /dev/null .`;
   - `go test -race -count=1 ./...` and `go test -C ui -race -count=1 ./...`, each twice;
   - `go test -race -count=10 -run 'PigeonCourier|ValidatorsWhoseGossipFails' ./node`, the two
     tests whose timing matters most.
3. For a change to `deploy/` or `cmd/quickstart`, run `make quickstart` on Docker or Podman, as the
   runbook says, and `make quickstart-clean` after.
