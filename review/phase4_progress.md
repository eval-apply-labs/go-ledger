# Phase 4 build: progress and how to resume

Phase 4 gives each bank a signer that holds its key, makes the names on chain a name service, and
makes a joining node trust a frame of validators, not a single peer, as `review/phase4_spec.md`
plans. The spec was written and decided on 2026-11-02, on branch `phase4`, from `phase3` at
`ba686f7`, whose tree is `main`'s at `9e8aa6f`. Every code slice is merged (K1, L1, N1, S1, N2, J1,
S2 and S3). Three independent reviews read the build on 2026-11-03, one of the joiner, one of the
name service and one of the signer, and their fixes are merged. A recheck of the whole build found
every fix sound, and one high and one low finding more, both now fixed. The docs are merged, and the
check list passed on the merged tree. On 2026-11-03 the branch went to `main` as one commit (see
[the commits these files cite](README.md#the-commits-these-files-cite)), and
[What is left](#what-is-left) says the rest.
The words this note uses are in [`docs/glossary.md`](../docs/glossary.md) and the spec's §0.1.

## Where the branch stands

`phase4` holds, in order:

- the spec, in two commits: written (`aee988b`), and every default of §9 taken (`4239df7`);
- K1, the key file: `59fbcbf`, `e32edd8`;
- L1, the new genesis: `db2ddcb`, `c2fb816`, `3e982a7`;
- N1, the zone, the lookups and the attribution: `5e91b3b`, `231931c`, `185e257`, `de309c2`;
- S1, the signer: `4b017e1`;
- N2, the wallet's TLS pin: `50eb12c`;
- J1, the joiner: `9f05f44`, `45a427c`, `d8ce87e`, `bd3fb41`;
- S2, the engine through the signer, in five commits: `e450e09` to `a094762`;
- the name service review's fixes, in eight commits, `add5536` to `8877df0`, and `a1dffc7`;
- the spec's correction of the desk's domains: `f3ace7d`;
- the joiner review's fixes, in six commits: `a4da640` to `13532dc`;
- S3, the wallet through the signer and the deployment: `b387452`, `7284c73`, `84acd8f`;
- the signer review's fixes, in twelve commits, `82e36a6` to `dea7c60`, and `c1da31a`;
- the docs and these records, in 22 commits, `f5738df` to `f0da4c1`, one of which, `3833e2d`,
  corrects a comment of `cmd/fetcher`;
- the recheck's two fixes: the divergence check's set, `d3daa0e`; and the bank wallets' password,
  `b3a0554`, `589cede` and `4e41066`, with the fixes of its review, `09d5eb4`, `ab2415c` and
  `4b8114d`.

Up to `c1da31a`, that is 54 commits, from `aee988b`. Three writers then worked side by side, from
`c1da31a`: `p4-da` and `p4-db` wrote the docs, and `p4-dc` wrote the spec's "as built" notes,
this record, and its lines in `review/README.md`. A reviewer rechecked the whole build beside
them (see [The recheck](#the-recheck-2026-11-03)).

The full test suite passed on `bd3fb41`, `a1dffc7`, `c1da31a` and `4e41066`, and the build for
Windows on `c1da31a`.

**The check list on the merged tree** passed on `4b8114d`, on 2026-11-03, in 706 seconds:
`gofmt`, `go vet` and `go build` of both modules; the full suite with the race detector, twice for
each module; and the restart, chaos, sign log, resume and catch-up tests five times. The build for
Windows passed on it too, and `PigeonCourier` and `ValidatorsWhoseGossipFails` ten times, in 82 seconds.
The commits after it touch `review/` only.

Each place the build departed from the spec, or settled what it left open, is a dated note marked
**(as built)** in the spec, with its commits and its reason. Each place a review fix changed the
design is a note marked **(as built, after the review)**, with its finding. So are the finding
declined and the limits the build leaves.

## The owner's decisions (2026-11-02)

The spec's §0.2 keeps the owner's words; the quotes below are taken from there.

- **D1. The signer and the name service.** "yes write the signer spec and the name service, which
  should then enable the ip filter from the dns service itself and make the flooding a human
  resolvable issue."
- **D2. A joiner trusts its seed, but no single peer.** "the joining node trusting a snapshot is
  "fine", someone needs to grandfather the new key in the shared ledger so you can see that there
  needs to be some trust. what you can do is read the previous frame node, gather 2/3 of the
  weights to validate a position, then do the validation on the 2/3 of the weights after your
  signature is accepted so you dont have to just trust one (which might be malicious)".
- **Earlier decisions that bind:** one key per bank; the desk gets the co-signing and freeze domains
  only; the signer refuses a second, different signature in a slot and logs before it signs; a
  name's payment key never becomes its TLS key; the standard library only; breaking changes in one
  new genesis.

Every question of the spec's §9 is decided: the owner took every default ("defaults, go ahead").
In short:

- **Q1:** HMAC per client over HTTP on the private network, and the signer listens.
- **Q2:** the clients `engine`, `desk` and `admin`, with the domains of §2.3's table.
- **Q3:** receipts go through the signer: one key per bank.
- **Q4:** the signer's key sealed at rest in the wallet's format, the passphrase from a file.
- **Q5:** a signer that lost its sign log refuses votes until `-resume-above <height>`.
- **Q6:** signing stays on the engine's event loop, with a deadline of a second and fail-fast.
- **Q7:** a zone file that standard DNS servers load, not a DNS server in Go.
- **Q8:** the zone's content as §3.2 writes it.
- **Q9:** IP addresses stay claims, and every claimant is listed.
- **Q10:** TLS key records now, and the wallet's pin now (slice N2).
- **Q11:** attribution by name; the fetcher reads the CDN's header only from the CDN's addresses.
- **Q12:** more than ⅔ of the trusted frame for every checkpoint.
- **Q13:** `NextValidatorsHash` in the block header.
- **Q14:** a joiner asks every validator of its frame, and two valid commits at one height stop it.
- **Q15:** a seated node whose state the network's commit says is wrong stops, and waits for its
  operator.

## The slice plan as run

| Round | Built side by side | Started from | Merged on `phase4` |
|---|---|---|---|
| 1 | L1 · K1 | `4239df7` | K1, then L1 |
| 2 | S1 · N1 · J1, and N2 once S1 was merged | `3e982a7`; N2 from `4b017e1` | N1, S1, N2, then J1 |
| 3 | S2, beside the reviews of the joiner and of the name service | `bd3fb41` | S2 |
| 4 | S3 · the name service fixes · the joiner fixes | `a094762`; the name service fixes from `bd3fb41` | the name service fixes, `f3ace7d`, the joiner fixes, then S3 |
| 5 | the signer review, then its fixes; then the docs and the recheck | `a1dffc7`; the docs from `c1da31a` | the signer fixes and `c1da31a`; then the docs, to `f0da4c1` |
| 6 | the recheck's two fixes, then a review of the password and its fixes | `c1da31a`; the password from `d3daa0e` | `d3daa0e`, then `b3a0554` to `4b8114d` |

- **The rounds differ from the spec's groups** (§7) so that the slices of a round share no file. J1
  needs only L1, so it was built a group early, beside S1 and N1. N2 touches the wallet only, so it
  started once S1 was merged.
- **Two items moved:** the allow list's refusal line went from S2 to N1, since it is attribution,
  and the fetcher's `GET /hdls/frames` route was built in N1, though J1 writes the file.
- **The reviews ran during the build,** before the docs, where the spec's plan puts them after.
- Each slice was built on a branch of its own (`p4-…`) and merged into `phase4` by cherry-pick.

## Slices done

| Slice | Commits on `phase4` | Production lines (+/−) | Test lines (+/−) |
|---|---|---|---|
| K1 the key file | `59fbcbf`, `e32edd8` | 274 / 151 | 257 / 48 |
| L1 the new genesis | `db2ddcb`, `c2fb816`, `3e982a7` | 166 / 89 | 244 / 68 |
| N1 the zone, the lookups, the attribution | `5e91b3b`, `231931c`, `185e257`, `de309c2` | 560 / 39 | 546 / 5 |
| S1 the signer | `4b017e1` | 1,015 / 2 | 876 / 0 |
| N2 the wallet's TLS pin | `50eb12c` | 114 / 2 | 126 / 0 |
| J1 the joiner | `9f05f44`, `45a427c`, `d8ce87e`, `bd3fb41` | 615 / 182 | 597 / 59 |
| S2 the engine through the signer | `e450e09`, `f123726`, `8a9c48d`, `d1eda1f`, `a094762` | 404 / 93 | 680 / 132 |
| S3 the wallet through the signer, the deployment | `b387452`, `7284c73`, `84acd8f` | 288 / 69, and 189 / 25 in `deploy/` and the `Makefile` | 516 / 55 |
| The name service fixes | `add5536` to `8877df0`, `a1dffc7` | 87 / 40 | 125 / 44 |
| The joiner fixes | `a4da640` to `13532dc` | 105 / 54 | 396 / 52 |
| The signer fixes | `82e36a6` to `dea7c60`, `c1da31a` | 202 / 104 | 294 / 54 |

Production lines count the Go files and the wallet's templates; test lines count the test files and
`testdata`. K1's removed lines are mostly the wallet's key file code, which moved. In all, from
`ba686f7` to `c1da31a`, Phase 4's code is 3,575 production lines added and 570 removed, 4,499 test
lines added and 359 removed, and 189 lines added and 25 removed in `deploy/` and the `Makefile`.

### K1. The key file (`59fbcbf`, `e32edd8`)

- The sealed key file moved from the wallet to `signing/keyfile.go`, so the signer reads the
  wallet's files. The wallet keeps finding the passphrase and making the key on first use.
- `ReadPassphraseFile` refuses an empty passphrase; a nonce of the wrong size is refused, not a
  panic.
- `keygen -secret` prints a client's secret, 32 random bytes in hex. `keygen -encrypt` writes a new
  key pair as a sealed key file, mode 0600, and refuses a path that exists.

Checked: two key files written by the wallet's code before the move still open; a sealed key goes
round; keygen's two new flags print and write what they say, and refuse to overwrite a file or to
write a plaintext key.

### L1. The new genesis (`db2ddcb`, `c2fb816`, `3e982a7`)

- A name lists up to two TLS keys, signed by its own key like its IP addresses, in the genesis, the
  update and the account digest. The wallet's account form publishes them.
- A block names the set that signs the next one (`NextValidatorsHash`). `ProposeBlock` sets it,
  `ApplyBlock` checks it, the block hash covers it, and the record entry carries it.

Checked: TLS keys have one form, none twice and at most two; an update sets, replaces and clears
them. Block `h + 1`'s set is the one block `h` named, in static and dynamic mode, across a block
that changes the set; the record test checks each entry's set against the one before.

### N1. The zone, the lookups and the attribution (`5e91b3b`, `231931c`, `185e257`, `de309c2`)

- The engine writes `hdls/zone`, the zone file, when its config sets a zone, and
  `hdls/resolve/ip/<address>` for each address a name lists.
- `cmd/server` reads the zone settings.
- The allow list's refusal line names who claims the address it refused the most.
- The fetcher serves the zone, the IP lookup and the frames, and, once a minute in which its cap
  answered `429`, logs the five addresses that sent the most, with the names that list them.

Checked: the zone of a known state matches `node/testdata/zone`; rebuilt from the public accounts it
matches apart from its serial; it is written only when more than its serial changes; the lookup
lists every claimant; the refusal line names the claimants; the config's zone settings and the
fetcher's two new flags are checked. BIND's
`named-checkzone` was not run then: it was not installed, and no container image holding it could
be fetched. Its test skips. It ran later, from BIND's own image (see the owner's answer on TLSA
records, below).

### S1. The signer (`4b017e1`)

`cmd/signer`, a program of its own. It holds the bank's key and serves `POST /sign` to its clients,
each with its secret and its domains, behind `allowFrom`. It checks each request's fields against
its domain's layout and the bank's name, gives each slot at most one signature, writes the slot to
its sign log and syncs it before it signs, and writes every signature and refusal to its audit log.

Checked: each domain's signature verifies as the code checks it, so the layouts match the code;
fields of another layout, or naming another bank, are refused; a second, different vote in a slot is
refused as a conflict, and the same vote again gets the same signature; a vote below the highest
height, a domain not in the client's list, a bad HMAC and an unknown client are refused; the slot is
taken after a crash between the sign log and the answer; without a sign log every vote is refused
until `-resume-above`; a sign log of another network is refused; a caller not in `allowFrom` is
refused; a config the signer can't keep is refused.

### N2. The wallet's TLS pin (`50eb12c`)

When a bank of the newest state the wallet verified lists TLS keys, the wallet reaches that bank's
`https://` public address only through a server whose key is one of them, and asks no certificate
authority.

Checked: the wallet takes the bank's server with a listed key and refuses it with another; a change
of the bank's keys is checked on a new connection.

### J1. The joiner (`9f05f44`, `45a427c`, `d8ce87e`, `bd3fb41`)

- A checkpoint needs more than ⅔ of the trusted frame, not ⅓.
- The engine writes `hdls/frames`, the frames it saw start.
- Catch-up asks every validator of its trusted frame, and walks the frames when the set changed too
  much.
- The fork and divergence alarms halt the node, and the status says `confirmed` and `halted`.

Checked: a checkpoint signed by exactly ⅔ of the frame is refused; a node away while three of four
validators were replaced in three steps walks the frames and catches up, and takes nothing while the
faces serve frames no block names; one peer withholding the newest checkpoint does not keep the node
behind; every public face is asked; two valid commits at one height halt the node; a seated
joiner is confirmed at its first commit; a joiner whose seed was altered halts with the divergence
alarm and is never confirmed; a checkpoint of the node's height on another block halts it.

### S2. The engine through the signer (`e450e09` to `a094762`)

- `signing.Signer` is an interface: `LocalSigner` holds the key in the process, `RemoteSigner` asks
  the signer program and checks its answer.
- `cmd/server` takes exactly one of `privateKey` and a `signer` object, and makes its key self-test
  through whichever it names.
- The remote signer gives up after a second, then fails fast for a second, logs at most a line a
  minute, and keeps a slot memory, so an honest engine never asks the signer for two votes in a
  slot while it runs.

Checked: four banks whose engines sign through four signers commit; a bank whose signer is down
does not vote while the others commit, and votes again once it is back; a compromised engine gets
one signature and a refusal in a slot; a vote that timed out after the signer signed it is not
followed by another in its slot; a forged answer, or one by another key, is refused.

### S3. The wallet through the signer, and the deployment (`b387452`, `7284c73`, `84acd8f`)

- A bank's wallet can sign through its signer (`-signer-url`, `-signer-client`,
  `-signer-secret-file`), with only the bank's public key in its wallet file.
- The engine image carries the signer as `/signer`, and `make build` builds it.
- The quickstart gives each bank a sealed key in its own signer, on a signing network of its own,
  reachable only from that bank's engine and wallet. No key is in an engine's config.

Checked: a bank's desk signs through a real `cmd/signer`, which the test builds and runs; a wallet
takes a key or a signer, not both; each quickstart signer's config loads. **Run for real under
Podman 3.4:** four banks, blocks committing, and each signer reachable only from its bank's engine
and wallet. The builder retagged the local images `go-ledger:before-p4-s3` and
`go-ledger-ui:before-p4-s3` before that run, as a backup; the owner may delete them. The signer
fixes, which change how the signer starts, came after the run. The recheck ran the containers
again, and they passed (see [The recheck](#the-recheck-2026-11-03)).

## The independent reviews (2026-11-03)

Three reviewers each read one area. Unlike Phase 3's, their reports are not in `review/`: each fix
commit says what its finding was, and the tables below sum them up.

| Review | Area | Read on | Found | Fixed in |
|---|---|---|---|---|
| The joiner | the ⅔ rule, the frames file, the walk, asking every validator, the alarms, the confirmation | `bd3fb41` | 3 medium, 5 low | `a4da640` to `13532dc` |
| The name service | the zone, the lookups, the attribution, the TLS keys on chain, the wallet's pin | `bd3fb41` | 3 real, 7 low | `add5536` to `8877df0`, `a1dffc7` |
| The signer | the signer (S1) and the engine through it (S2) | `a1dffc7` | 2 medium, 8 low, 4 minor | `82e36a6` to `dea7c60`, `c1da31a` |

S3, which was built while the signer review ran, was read by the recheck.

**The joiner review** found no way to forge a state and no false alarm. The ids are the review's
own. The fix commits cite them, but for `13532dc`, whose "finding 7" is its place in the list of
fixes: it is the review's finding 8.

| Id | Kind | What | Fix |
|---|---|---|---|
| 1 | medium | A fork against the node's own block went unseen. Catch-up compared the checkpoints of the peers ahead with each other, never with the node's own block: a peer at its height on another block was cut off by the break, and one further ahead was adopted silently. Now a peer at the node's height that reports another block is fetched, and if its checkpoint passes, the node halts with the divergence alarm, seated or not | `a4da640` |
| 2 | medium | The walk's two checks of the entry before a frame, `anchorVouches` and the recomputed block hash, had no test: none failed with either taken out | `68d0798` |
| 3 | medium | A sound checkpoint that no walk reaches, which costs a forger nothing, was walked again for every peer that served it, at every tick, and each walk read every face's frames, even with none ahead of the node. Now a walk skips a face that is not ahead | `83f0df5`, in part: see below |
| 4 | low | In dynamic mode every change of power started a frame. The 100 frames kept spanned about 100 busy blocks, and the file, about 5.6 MB with 21 banks, was written again at almost every block. Now a frame starts only with a change of names or keys | `6ddeabb` |
| 5 | low | The checks of a frame's own entry added only a risk: no signature covers that entry, and an honest frame failed them when a face did not hold it yet. They are gone | `68d0798` |
| 6 | low | After a node was opened from an older snapshot, or seeded onto the other side of a fork, `hdls/frames` kept lines of heights it had not committed, which stopped a walker. `Open` drops them | `083fb8b`, in part: see below |
| 7 | low | The walk reads frames and record units only from the public faces: the gossip and operator ports serve neither | not changed: a limit, for the docs to state. The review left it to the owner |
| 8 | low | Four rules had no test: what confirms a node, an observer not halting on a block it refused, a halted node's catch-up asking nothing, and a walk's 100 frames a face | `13532dc` |

What the joiner fixes did not take, and why:

- **One walk per catch-up call** (finding 3's larger fix). It changes how catch-up is built. So a
  node that is behind still walks such a checkpoint once for each peer that serves it, at each tick,
  reading the frames of the faces ahead, at most 100 frames from each.
- **Dropping lines as a new frame is listed** (finding 6's other half). Once `Open` drops the lines
  above the height in progress, none is left, and that height never goes down while the node runs:
  the code could never act, and no test could fail without it.
- **What stays undetected.** With no peer at the node's own height, a node on one side of a fork
  adopts the other side's checkpoint silently when it is two or more heights ahead, and an observer
  when it is one ahead too. A checkpoint does not say which block of the node's height it builds on;
  seeing it would need the chain of headers in between. The spec's §4.4 and §4.6 say so.
- **Tests not written:** that catch-up returns a checkpoint only above the node's height (catch-up
  refuses any other anyway). The walk's test crosses a change of names; a change of key is covered
  by the frames file's test and the walk's control.

**The name service review** found the TLS keys on chain and the wallet's pin sound. Its findings
were about the zone file and the fetcher. Its fix commits do not number them.

| Kind | What | Fix |
|---|---|---|
| real | BIND's default check of names refuses a whole primary zone whose A or AAAA owner is not a host name, as `_validators` was. It is now `validators-of-the-height-in-progress`, longer than any ledger name | `add5536` |
| real | BIND 9.18.28 and later refuse a zone with more than 100 records of one type under one owner (`max-records-per-type`, the fix of CVE-2024-1737). The `_ip` lookup wrote one TXT record per name; it is now one per address, with at most 100 names and a pointer to the IP lookup file for the rest. The validators' label holds one record per address, past 100 of one kind as soon as 26 validators publish 4 IPv4 addresses each | `d23e6c3` for `_ip`; for the validators' label, the runbook is to set `max-records-per-type 0;` |
| real | A name server inside the zone was taken. Its address records would be in the zone, whose names are the customers', so a customer holding a name such as `ns1` would hold the zone's name server, or BIND would refuse the zone | `8e1b01c`: refused |
| low | A zone name of up to 253 characters was taken, which made the longest names in the file longer than a domain name may be | `84d7c56`: at most 208 |
| low | A name whose public address is `http://` got TLSA records under `_443` | `b90851e` |
| low | A validator's loopback address went into the validators' set, and so onto every firewall that resolves it | `9428f50` |
| low | A CDN header with no address that parses left the sender as the CDN's own address, so a name that lists that address was named as the sender of everything the CDN passed | `f43d2e6`; `a1dffc7` names such senders as such in the line |
| low | The comments said a rebuilt zone matches the file; it matches apart from its serial | `8877df0` |
| low, and notes | Points for the runbook: a zone write that fails is written again only when the zone changes, or at a restart; a change of the zone settings at a restart, with no block committed since the last write, keeps the serial, so a secondary server does not reload; the zone should be a domain of its own, since its names are the customers'; DANE clients use TLSA records only in a zone signed with DNSSEC; a flooder with many IPv6 addresses spreads its submissions and evades counts by exact address | the docs |

**The signer review** found one double signature, finding 1, which it proved. It found none by the
other ways it tried: the spellings of a field, an overflow, two requests at once, the valid round, a
torn log line and a crash. Its fix commits do not number its findings; the spec's notes use these
ids.

| Id | Kind | What | Fix |
|---|---|---|---|
| 1 | medium | A second start of a running signer read and rewrote its sign log before it bound its port, and dropped the lines appended meanwhile. A later restart freed a slot already signed: a double signature. Now the signer binds its port, then locks the sign log's folder, before it reads the log | `82e36a6` |
| 2 | medium | A burst of receipts queued at the signer's one lock ahead of the engine's votes, which missed their second, and a failed receipt opened the fail-fast window for votes. Proved with a signer 5 ms slower per signature. Now one receipt is in flight at a time, and the signer drops a request whose client has gone | `4003253` |
| 3 | low | A sign log append that failed left a line cut short, which stopped the next start, or a slot free in memory that the disk held | `4c51e27`: a sign log write error stops the signer; `701e950`: the sign log holds no `REFUSED CONFLICT` lines, which only the audit log keeps |
| 4 | low | An engine whose config named another bank passed the key self-test, then got `403` on every vote. The self-test now carries the name | `e15cfde` |
| 5 | low | A second outage soon after a quick recovery got no log line of its own | declined: see below |
| 6 | low | The spec's table gave the desk `key-change` and `bank-change`, which `review/phase2_spec.md` §4 rules out | `f3ace7d` (the spec), `13c1436` (the comments and tests) |
| 7 | low | A `signLogPath` that was a symbolic link was replaced by a file, and a restart that found the link again read the old file | `df7f6fd`: refused |
| 8 | low | A key file holding its key both sealed and in plaintext was taken | `61175c1`: refused |
| 9 | low | The docs must say never to reset a signer's sign log after a break-in on its engine | the docs |
| 10 | low | Two rules had no test: the service's one request at a time, and an IPv4 caller in IPv6 dress let in by `allowFrom` | `09c55a3` |
| – | minor | The remote signer used Go's default transport, so `HTTP_PROXY` would route its requests through a proxy | `15ba31e` |
| – | minor | The body cap, 64 KiB, was less than a message the ledger takes | `606ee39`: 2 MiB |
| – | minor | `WriteKeyFile` did not sync the folder after its rename | `dea7c60` |
| – | minor | The audit log grows without bound, about 1 GB a day at the fetcher's cap, and a full disk stops the bank's votes | the docs: how to rotate it |

**Finding 5, declined.** When the signer fails, comes back, and fails again, all within a minute of
the line that said it fails, the log shows one outage, until the line that it answers again. That
line always comes: the engine asks the signer at every block, and at least at every idle heartbeat
(5 minutes), so it comes at the first request after the minute.

**Beside the findings:**

- `c1da31a`: `82e36a6` split the signer's start (bind, lock) from reading its config, so the
  quickstart's test loads each signer as the signer's own tests do, without binding a port or
  locking the quickstart's folders.
- The signer fixes left one gap in the tests: nothing checks that the remote signer's transport uses
  no proxy.

### The recheck (2026-11-03)

A reviewer rechecked the whole build on `c1da31a`: the three reviews' fixes, S3, which no review
read, and the containers under Podman.

- **The fixes.** Every fix of the three reviews was judged sound. `6ddeabb`, for the joiner's
  finding 4, is safe but less complete: see the walk under power drift, below.
- **The containers** ran after the signer fixes: 16 containers, four banks of an engine, a signer, a
  fetcher and a wallet each. Every key self-test passed, a payment committed, and a second start of
  a signer was refused both by the bind and by the lock, with its logs untouched.
- **The high finding's first fix was not merged.** It moved each bank's wallet to a network of its
  own bank, shared only with its engine. On Podman 3.4, which routes between networks that are not
  internal, a container on `ledger` still drove each wallet at its address there. The wallet's
  password replaced it, and the wallets stayed on `ledger`, where they still check the other banks.
- **An independent review of the password** found no way past it: every route, method and
  malformed `Authorization` header gets `401` without it, and `loopbackOnly` matches what
  `net.Listen` binds. Its findings were low: the password stood before the guard, so another
  site's page got a login box under its own name; the wallet file was loaded before the password
  was read, so a wallet that refused to start had written a key; `localhost` counted as loopback by
  name; and gaps in the docs. All fixed, in `09d5eb4`, `ab2415c` and `4b8114d`.
- **The containers ran again** with the password, on Podman 3.4, rootless: 16 containers up; each
  bank's wallet answered `401` to a container on `ledger`, with no password or a wrong one, and
  `403` under a rebound host name; with its password, `bank-a`'s wallet paid, with a verified
  receipt, and checked banks holding 100% of the power. Then `make quickstart-clean`.

| Kind | What | Fix |
|---|---|---|
| high | Each bank's wallet listened on the `ledger` network, which every engine, fetcher and wallet of the compose file shares, and the wallet signs through its bank's signer. Its guard stops a browser's page, never a program, so any container there could drive any bank's wallet. Proved live: a payment sent from a container on `ledger` committed. The fix: a wallet that listens beyond a loopback IP address asks for a password, from `-password-file`, behind its guard, and refuses to start without one; the quickstart writes one for each bank's wallet, mounted as a secret | fixed in `b3a0554`, `589cede`, `4e41066`; after its review, `09d5eb4`, `ab2415c`, `4b8114d` |
| low | The divergence check at the node's own height (`a4da640`) checked the checkpoint against the set that signs the height in progress, not the set that signed the node's height | fixed in `d3daa0e` |
| limit | The walk under power drift. With frames listed only on a change of names or keys (`6ddeabb`), a walker holds a frame's powers as they were at its first height. In dynamic mode the powers drift within a frame; when some of its validators are absent, the ones that sign may hold more than ⅔ of the power now, but not of the powers the walker holds, and a joiner is stranded until its bank gives it a newer seed. Proved with a test. The case is rare, and the owner's rule is that rare cases go to people: documented, not coded | the docs; spec §4.4 |
| limit | The check against the node's own block (`a4da640`) needs a peer at the node's height, where on a busy chain the peers seldom stay | the docs; spec §4.4 |
| limit | The validators' label passes BIND's 100 records of one kind once 26 validators publish 4 IPv4 addresses each, and then needs `max-records-per-type 0;` | the docs; spec §3.2 |
| limit | The signer's lock, `flock`, does not hold across machines on NFS | the docs; spec §2.5 |
| limit | A bank's wallet's requests share the signer's one lock with the engine's votes: only receipts are held to one in flight | the docs; spec §2.7 |
| declined | Naming the sender of a CDN header whose last entry carries a port. It stays an unknown sender, as `f43d2e6` chose on purpose | spec §3.4 |
| declined | Ignoring `EINVAL` from `WriteKeyFile`'s folder sync, which some file systems return | spec §2.8 |

## What is left

1. **The owner's read** of the spec's notes and this record. The owner released the branch knowing
   one change of the recheck's fix: a wallet that listens beyond a loopback IP address, a
   customer's in a container included, now refuses to start without `-password-file`.
2. **The owner's Gitea `main`** takes `phase4`'s granular history. GitHub's `main` took the branch
   as one squashed commit, on `9e8aa6f`, on 2026-11-03, as for Phase 3.
3. **The backup images.** The builder retagged the local container images `go-ledger:before-p4-s3`
   and `go-ledger-ui:before-p4-s3` before S3's run. The owner may delete them.

## The owner's answer on TLSA records in the zone (2026-11-03)

The zone file holds a name's TLS keys as TLSA records (spec §3.2, §3.3). This record asked whether
to keep them: a DANE client takes them only from a zone signed with DNSSEC, which the engine does
not sign, and only for a host reached by its name in the zone, `bank-a.<zone>`, while the examples
give each bank a public address in its own domain. The owner kept them: the point is that each
bank can be a DNS provider, serving existing DNS systems from the ledger's data, and a name that
is reached by its name in the zone gets its TLS keys there. The docs state both limits already.

**Checked with BIND 9.20** (the `internetsystemsconsortium/bind9` image, 2026-11-03): its
`named-checkzone` took the zone of the tests' known state (`loaded serial 1043`, `OK`), and `named`
served it, answering `dig` for each kind of record: a name's `A` and `TXT`, its `TLSA` records, two
of them for a name rolling its key, the IP filter, and the `_ip` lookup, with a frozen name in it
and no address for it.

## Known limits

Each is in the spec's notes, with its reason.

- **The slot memory is in memory only** (§2.7). After an engine restart, a vote that timed out
  before it, after the signer signed it, may be followed by another in its slot. The signer refuses
  it as `REFUSED CONFLICT`: no second signature, but a conflict line against an honest engine.
- **The joiner's undetected forks** (§4.4, §4.6), above.
- **The walk reads only the public faces** (§4.4): a validator with no public address, or whose face
  is down, adds nothing to a walk.
- **A forged checkpoint is walked once per peer that serves it**, at each tick, while the node is
  behind (§4.3).
- **The audit log grows without bound** (§2.5), and a full disk stops the bank's votes.
- **The validators' label** passes BIND's 100 records of one type once 26 validators publish 4
  IPv4 addresses each (§3.2): the runbook's `max-records-per-type 0;`.
- **Counts by exact address** (§3.4): a flooder with many IPv6 addresses may not show among the
  five.
- **The zone's serial** stays when the zone settings change at a restart with no new block (§3.2).
- **The engine needs its signer at start** (§2.7): its self-test stops it until the signer answers.
- **Two signers can't share a log folder** (§2.5): the lock is the folder's.
- **The signer locks its folder on Linux and other Unix systems only** (§2.5): on Solaris, AIX and
  Windows it builds, and locks nothing.
- **The lock does not hold across machines on NFS** (§2.5), from the recheck.
- **A bank's wallet shares the signer's lock with the engine's votes** (§2.7), from the recheck.
- **The wallet's password crosses the network as it is** over plain http (§5's note): a bank's
  wallet still belongs where only its operator reaches it.
- **The walk under power drift** (§4.4), from the recheck: with absent validators, a joiner may need
  a newer seed from its bank.
- **The check against the node's own block needs a peer at the node's height** (§4.4), from the
  recheck: on a busy chain there seldom is one.

## What remains after Phase 4

Out of scope, as the spec's §1 says:

- hardware for the key (a TPM, an HSM, a smartcard), which needs libraries outside the standard
  library; the signer is where one would plug in;
- a DNS server written in Go;
- DNSSEC signing of the zone, which an operator can do with their own DNS tools;
- automatic bans and per-client rate limits;
- TLS on the gossip port;
- the archive node, the snapshot write off the event loop and the other items of `TODO.md`.

## How to resume

1. Make one worktree per change, from `phase4`. For example:
   `git worktree add -b p4-fix <scratch>/wt/p4-fix phase4`.
2. Merge each change into `phase4`, then run the check list on the merged tree, with
   `GOTOOLCHAIN=auto` and `GOLEDGER_CHAOS_SLACK=4`:
   - `gofmt -l .` in the root and in `ui/`, which must print nothing;
   - `go vet ./...` and `go vet -C ui ./...`;
   - `go build ./...`, `go build -C ui -o /dev/null .`, and `GOOS=windows go build ./...`, since
     the signer's lock has a build of its own for other systems;
   - `go test -race -count=1 ./...` and `go test -C ui -race -count=1 ./...`, each twice;
   - `go test -race -count=10 -run 'PigeonCourier|ValidatorsWhoseGossipFails' ./node`, the two
     tests whose timing mattered most in Phase 3.
3. For a change to `deploy/`, `cmd/quickstart` or `cmd/signer`'s start, run `make quickstart` on
   Docker or Podman, as the runbook says, and `make quickstart-clean` after. A `deploy/quickstart`
   folder written before `589cede` has no wallet passwords, and Compose refuses it: clean it first.
