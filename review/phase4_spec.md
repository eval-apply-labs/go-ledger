# Phase 4 implementation spec: the signer, the name service, and a joiner that trusts no single peer

> **Status:** written on 2026-11-02 on branch `phase4`, before any build. The owner's decisions bind
> (§0.2), and §0.3 says how this spec reads them. Every question in §9 is decided: the owner took
> every default ("defaults, go ahead", 2026-11-02), and the build follows §7.
>
> **Built** on 2026-11-02 and 2026-11-03, on branch `phase4`: slices K1, L1, N1, S1, N2, J1, S2 and
> S3, from `59fbcbf` to `84acd8f`, with the fixes of the first two reviews merged between S2 and S3.
> Where the build departed from this spec, or settled what it left open, the section says so in a
> dated note marked **(as built)**. The progress record is `review/phase4_progress.md`.
>
> **Reviewed** on 2026-11-03: three independent reviews, of the joiner and of the name service on
> `bd3fb41`, and of the signer on `a1dffc7`. The joiner review found no way to forge a state and no
> false alarm. The name service review found the TLS keys on chain and the wallet's pin sound. The
> signer review found one way to make the key sign twice in a slot: a second start of a running
> signer. Their fixes are on `phase4`: `add5536` to `8877df0` and `a1dffc7` (the name service),
> `a4da640` to `13532dc` (the joiner), and `82e36a6` to `dea7c60` and `c1da31a` (the signer). Where
> a fix changed the design, the section says so in a dated note marked **(as built, after the
> review)**, with the finding. The docs (§8) are merged, from `f5738df` to `f0da4c1`.
> A recheck of the whole build on `c1da31a` found every fix sound, and `6ddeabb` safe but less
> complete (§4.4's note). It found one high finding, a bank's wallet that any container could
> drive (§5's note), and one low, in the divergence check (§4.4's note), and named limits for the
> docs. Both are fixed: `d3daa0e` for the low one, and a password for the bank's wallet, `b3a0554`
> to `4b8114d`, which an independent review read. Its notes are marked **(as built, after the
> review)** too, with "the recheck". The progress record says where each stands.
>
> **Inputs:** the owner's message of 2026-11-02 (§0.2); the signer item of `TODO.md` and of
> `review/post_emudhra_review_2026-10-27.md` (Phase 3, "the signer"); the notes for later of
> `review/phase3_spec.md` (§13: a DNS gateway, TLS key records, R7); and the known limit "joiners
> trust their seed" of `findings_20261027.md`. "Today" means `main` at `9e8aa6f`.

## 0. How to read this spec

### 0.1 Words

- **Signer**: `cmd/signer`, a new program in a container or on a device of its own. It holds the
  bank's one key and is the only thing that does.
- **Client**: a program the signer answers: the engine, the bank's desk, the bank's admin page.
  Each has its own secret and its own list of domains (§2.3).
- **Domain**: the first field of everything the ledger signs (`signing.Domain`: `block-revision`
  for votes, `proposal`, `receipt`, `registration`, `name-request-bank`, `freeze`, and so on). It
  says what kind of message a signature is for.
- **Slot**: for a vote, its height, round and type; for a proposal, its height and round. The
  signer gives each slot at most one signature (§2.4).
- **Zone file**: a text file in the standard format of DNS (RFC 1035, "master file") that any DNS
  server can load (§3.2).
- **Frame**: a validator set and the height from which it signs: the set the chain had between two
  changes of the set. The **trusted frame** is the newest frame a node already trusts (§4.1).
- "Must" marks a rule a test checks. "May" marks an option a builder can skip. Every new identifier
  is spelled out in full, as in the rest of the code.

### 0.2 The owner's decisions

- **D1. Write the signer spec and the name service.** The owner (2026-11-02): "yes write the
  signer spec and the name service, which should then enable the ip filter from the dns service
  itself and make the flooding a human resolvable issue."
- **D2. A joiner trusts its seed, but no single peer.** The owner (2026-11-02): "the joining node
  trusting a snapshot is "fine", someone needs to grandfather the new key in the shared ledger so
  you can see that there needs to be some trust. what you can do is read the previous frame node,
  gather 2/3 of the weights to validate a position, then do the validation on the 2/3 of the
  weights after your signature is accepted so you dont have to just trust one (which might be
  malicious)".
- **Earlier decisions that bind this spec:**
  - One key per bank (2026-10-27). The bank co-signs with its consensus key, so freezing a
    compromised bank key stops its votes and co-signatures together: the owner's "emergency brake".
  - The signer serves the bank's desk only the co-signing and freeze domains (2026-10-27), "so a
    compromised desk terminal can co-sign but never vote".
  - The signer refuses a second, different signature for the same height, round and type, and
    writes that to disk before it signs (`TODO.md`).
  - Phase 3's R7: a name's payment key never becomes its TLS key. A TLS key record is signed by the
    name's key, as DANE does in DNS.
  - Zero dependencies: the standard library only, in both modules.
  - Breaking changes are allowed: no network is deployed, and the changes go into one new genesis.

### 0.3 Readings of the decisions (flagged)

- **R1. "Enable the ip filter from the dns service itself."** Read as: the name service publishes
  what an IP filter needs, in DNS, so a firewall anywhere can use it, not only the engine's own
  allow list. That is one record holding every current validator's IP addresses, which a standard
  firewall resolves into its allow list, and a lookup from an IP address to the names that list it
  (§3.4). The engine's in-process allow list stays as built in Phase 3.
- **R2. "Make the flooding a human resolvable issue."** Read as: when an IP address floods a bank,
  the bank can say *which name* claims that address, so people resolve it: the bank of record
  calls its customer, and if that fails the ⅔ freeze the name. No code bans anyone on its own,
  and there are no new automatic rate limits (§3.4). A valid signed flood already pays the fee,
  priced at what processing costs (`docs/managing-the-currency.md`); attribution is for what
  arrives unsigned or invalid. **The other reading** is the gossip relay of `TODO.md` ("gossip
  floods every peer"): banks would choose by name which peers relay to which. §9 Q11 asks.
- **R3. "Read the previous frame, gather 2/3 of the weights to validate a position."** Read as: a
  node adopts a newer height and state only when validators holding **more than ⅔ of the power of
  the frame it already trusts** signed it, under that frame's keys (§4.2). Today it takes more than
  ⅓. Where the set changed too much since that frame, the node walks from frame to frame, each one
  signed off by more than ⅔ of the one before (§4.3).
- **R4. "Then do the validation on the 2/3 of the weights after your signature is accepted."** Read
  as: once a new validator's own precommit is counted in a commit, that commit is the network's
  more-than-⅔ agreement on a block the node computed from its own state, so its state is confirmed
  by the current set, not by whoever seeded it (§4.5). Until then the node reports itself as not
  yet confirmed.
- **R5. "Someone needs to grandfather the new key."** The first frame stays trusted: it is the
  genesis, or the snapshot the joiner's own bank hands it (as today). The spec does not try to
  remove that trust; it removes trust in any one peer after it (§4.6 says what is left).

## 1. Scope and non-goals

In scope:

1. The signer: a program that holds the key, answers its clients by domain, keeps its own sign log
   and an audit log, and keeps its key encrypted at rest (§2).
2. The engine and the wallet sign through it, or with a key in their own process as today (§2.7).
3. The name service: a zone file the engine writes into the public folder, TLS key records on chain,
   and a lookup from an IP address to names (§3).
4. The joiner: more than ⅔ of the trusted frame, the walk from frame to frame, every validator of
   the frame asked, and the confirmation by its own accepted signature (§4).
5. One new genesis for the changes to the account and the block header (§3.3, §4.3).
6. The images and the compose file: a signer per bank (§5); the docs (§8).

Not in scope:

- Hardware for the key (a TPM, an HSM, a smartcard). They need libraries outside the standard
  library. The signer is where one would plug in later.
- The encrypted-in-memory key of `TODO.md`'s cheaper tier. With the signer, the engine holds no key.
- A DNS server written in Go (§3.2 says why; §9 Q7 asks).
- DNSSEC signing of the zone. An operator signs the zone file with their own DNS tools, if they
  want to.
- Automatic bans or per-client rate limits (R2).
- TLS on the gossip port. Phase 3's Q12 stands; TLS key records make it possible later (§3.3).
- The archive node, the snapshot write off the event loop and the other `TODO.md` items.

## 2. The signer

### 2.1 What it protects, and what it does not

Today the key is in two places: the engine's config file, and the bank operator's wallet, which
signs the bank's co-signatures and governance votes (`ui/requests.go`, `ui/admin.go`). A break-in on
either machine takes the key.

With the signer the key is in one place, on a machine that accepts nothing from the internet. A
break-in on the engine, the desk or the admin page then gets:

- **the engine**: votes and proposals the signer accepts, at most one per slot, and receipts. It
  cannot make the key sign two conflicting votes, so it cannot get the bank's name frozen for a
  double-sign.
- **the desk**: co-signatures and freezes, never a vote or a payment.
- **the admin page**: what its domains allow.

The signer does not stop a fork. A guard on the slot alone still forked in testing
(`review/post_emudhra_review_2026-10-27.md`, E2, pass 3), because a node can vote unlocked in a
later round. The engine keeps its own sign log, the lock and its restore, exactly as today (§2.7).
The two logs guard different things: the engine's guards the lock, the signer's guards the key.

### 2.2 How a client reaches it

The signer listens on one address on the bank's private network (`listen`). It refuses, at accept,
any connection whose remote IP address is not in its `allowFrom` list, as the engine's gossip port
does (`node/allowlist.go`). Its port is never published.

Each request is one HTTP `POST /sign` with a JSON body and one header,
`Authorization: HDLS-HMAC <client name> <hex of HMAC-SHA256(client secret, body)>`. The signer
recomputes the HMAC with that client's secret and compares in constant time
(`hmac.Equal`); a mismatch is refused and logged.

- **Why no TLS.** Nothing on the link is secret: the requests are messages the bank will publish,
  and the answers are signatures it will publish. What the link needs is to know who asks. An HMAC
  with a per-client secret does that in a few lines, from the standard library, and stays safe
  against a quantum computer, which a TLS client certificate with an Ed25519 key would not.
- **Why no replay guard.** ML-DSA is used deterministically (`SignDeterministic`), so a replayed
  request gets the same signature its first answer already carried, and a replayed vote for a slot
  already signed is answered from the sign log. Nothing new comes out of a replay.
- **The client checks the answer.** Every client verifies the signature it gets under the bank's
  public key before it uses it, so a forged answer is caught.

§9 Q1 asks whether the signer should instead dial out to the engine, as some remote signers do, so
that it has no listening port at all.

**2026-11-02 (as built)** (`4b017e1`):
- **The answers.** A signature is `200` with `{"signature": "<base64>"}`. A refusal is a short
  reason in plain text: `401` for a missing or malformed `Authorization` header, an unknown client
  or an HMAC that does not match; `400` for a body that is not a request, or fields that do not fit
  the domain (§2.4, rule 1); `403` for a domain not in the client's list, or fields that name
  another bank; `409` for a vote or a proposal its slot rules refuse, whose reason begins
  `REFUSED CONFLICT` for a conflict (rule 3); `413` for a body over the cap. Any other path is
  `404`, another method `405`. A failure of the signer itself, such as its audit log or its key, is
  `500`.
- **The HMAC's key** is the 32 bytes that the secret file's 64 hex digits encode, as
  `keygen -secret` prints them.
- **A connection that `allowFrom` refuses** goes to the signer's own log (standard error), once a
  minute with a count and the last address, not to the audit log: it never reached a client's rules.

**2026-11-03 (as built, after the review)** (signer review):
- **The body cap** (`606ee39`) was 64 KiB, less than a message the ledger takes
  (`ledger.MaxMessageBytesLimit`, 1 MiB). It is now 2 MiB, twice that limit, which holds a
  message's fields in base64, a third larger, with the JSON around them.
- **No proxy** (`15ba31e`). The remote signer used Go's default transport, so an `HTTP_PROXY` set on
  the engine's machine would have sent its requests through a proxy. It has a transport of its own,
  with no proxy.
- **A client that has gone** (`4003253`). The signer takes one request at a time. A request whose
  client gave up while it waited its turn, as the engine does after a second, is dropped unsigned,
  with no answer and no audit line, so a backlog never outlives its deadlines.

### 2.3 The clients and their domains

The signer's config lists its clients, each with a name, a secret file and the domains it may ask
for. A domain not in a client's list is refused. The builder's default layout:

| Client | Domains | Why |
|---|---|---|
| `engine` | `block-revision` (votes), `proposal`, `receipt` | the engine signs nothing else today |
| `desk` | `registration`, `name-request-bank`, `freeze` | the co-signature of a registration (D-earlier), the fee the bank pays as requestor for its customer, and the bank's own freeze |
| `admin` | `parameter-vote`, `reassignment-vote`, `payment`, `account-update`, `freeze` | governance and the bank's own money and records |

- `name-request-new-key` and `reassignment-new-key` are never the bank's: the customer's own wallet
  signs them, as proof that it holds the new key.
- `key-change` and `bank-change` are in no list (corrected after review: this table first gave them
  to the desk). Their requestor is the name itself (`review/phase2_spec.md` §4), so a desk could only
  ever change the bank's own key or bank, which a desk must never do.
- `freeze` is in two lists on purpose: a bank whose key may be exposed freezes its own name at
  once, from whichever terminal it reaches first.
- Every client may ask for `key-self-test`, the signature with no fields that the engine makes at
  start to prove the key pair works (`cmd/server/config.go`). Its domain is used for nothing else.
- A bank may run the desk and the admin page in one wallet with one client, or on two terminals
  with two. §9 Q2 asks about the layout.

**2026-11-02 (as built)** (`4b017e1`). The signer has a layout (§2.4, rule 1) for each domain of
the table, and for `key-change` and `bank-change` too: an operator may list those for a client, and
the signer then signs them only with the bank as the requestor, of its own name. A domain with no
layout, such as `name-request-new-key` or `reassignment-new-key`, is refused in a client's list when
the signer starts. The domain `key-self-test` is a constant of its own in both `cmd/server` and
`cmd/signer`.

**2026-11-03 (as built)** (`84acd8f`). The quickstart runs the desk and the admin page in one wallet
per bank. So each signer has two clients: `engine`, with the engine's three domains, and `wallet`,
with the desk's and the admin page's together: `registration`, `name-request-bank`, `freeze`,
`parameter-vote`, `reassignment-vote`, `payment` and `account-update`.

**2026-11-03 (as built, after the review)** (signer review):
- **The key self-test carries the name** (`e15cfde`; finding 4). The bullet above says the
  self-test signs no fields. As built it signs one, the bank's name. The engine signs its own name,
  and the signer refuses a name that is not its bank's (`403`). Before, an engine whose config
  named another bank passed the self-test, then got `403` on every vote; now it stops at its start.
- **The desk's domains** (`f3ace7d`, `13c1436`; finding 6). This table first gave the desk
  `key-change` and `bank-change`, which `review/phase2_spec.md` §4 rules out. `f3ace7d` corrected
  the table, and the bullet above says so. `13c1436` corrected the signer's comments, and its tests,
  which now give those two domains to `admin`, in tests only, so that they still reach their layout.

### 2.4 The rules it enforces

1. **It never signs raw bytes.** A request is `{"domain": "...", "fields": ["<base64>", ...]}`:
   exactly the fields the ledger signs today (`signFields` of each message type). The signer checks
   the field count and form for the domain, then builds the preimage itself with
   `signing.Preimage(networkID, domain, fields...)`, its own network id from the genesis file, and
   signs that. A client cannot make it sign for another network or outside a domain.
2. **It signs only in its own name.** Where the fields name a signer (the validator of a vote, the
   proposer, the node of a receipt, the requestor or the bank of a name request, the payer of a
   payment, the name of an account update), that name must be the signer's bank. A governance vote's
   fields name no voter (`parameterVoteFields`, `Reassignment.voteFields`), so for those the
   client's domain list is the only gate.
3. **One signature per slot.** For `block-revision` the slot is (height, round, type); for
   `proposal` it is (height, round). A second request for a signed slot with the same fields gets
   the same signature again. With different fields it is **refused**, and the signer writes both
   requests to its log as one line marked `REFUSED CONFLICT`: either the engine is broken or someone
   else is driving it.
4. **Never below its own height.** A vote or proposal for a height below the highest height it has
   signed is refused (repeats of a logged slot excepted). This lets it forget old slots.
5. **Log, then sign.** It appends the slot and the fields to its sign log, `fsync`s, and only then
   signs and answers (`TODO.md`: "writes that to disk before it signs"). Today the engine does it
   the other way round, sign then log, which is safe only because the key never leaves the process.
6. **No rule for the rest.** Receipts, co-signatures, freezes, governance votes, payments and
   updates have no slot: two different ones are never evidence (`docs/signature.md`), so the signer
   only checks rules 1 and 2 for them and writes them to the audit log (§2.5).

**2026-11-02 (as built)** (`4b017e1`):
- **Rule 1, the form.** The signer checks every field its rules read (a slot, a name that must be
  the bank's), and the form of every number, hash, flag and list count, as the code writes them. A
  name, a key, an address or a comment it does not check: the ledger refuses a malformed message,
  whoever signed it.
- **Rule 3, a conflict,** is answered `409` with the reason `REFUSED CONFLICT`, and written to the
  audit log with the fields of both requests.
- **Rule 4** also refuses every slot at or below the height of `-resume-above` (§2.6).

**2026-11-03 (as built, after the review)** (signer review, finding 3):
- **No `REFUSED CONFLICT` line in the sign log** (`701e950`). Rule 3 above says the signer writes
  both requests to its log as one line marked `REFUSED CONFLICT`. As built, that line is in the
  audit log only. The sign log holds its first line and the slots signed, nothing else: a conflict's
  line there was one more write that could fail, for a line the next rewrite dropped anyway.
- **A sign log write that fails stops the signer** (`4c51e27`). An append or an `fsync` that fails
  now stops the program (exit status 1), and the signature is not made. Running on, the signer could
  append after a line cut short, which stopped its next start, or sign other fields in a slot the
  log on disk held. No double signature came of it, but the log then needed a person to edit it. A
  restart drops a line cut short, and reads a whole line as signed, which is safe.
- **A test holds the one-at-a-time rule** (`09c55a3`; finding 10): two different votes for one slot,
  asked at once, get one signature between them.

### 2.5 Its logs

- **The sign log** (`signLogPath`): JSON lines, one per consensus signature, kept from its highest
  height down to the height before it. It is what rule 3 reads.
- **The audit log** (`auditLogPath`): JSON lines, one per signature of any domain and one per
  refusal: the time, the client, the domain, and a summary a person can read ("co-signed the
  registration of `alice` requested by `bank-a`, block 1043"). Nothing reads it but people. It is
  how a bank finds out, after the fact, what a stolen desk terminal signed.

**2026-11-02 (as built)** (`4b017e1`):
- **The sign log's first line** names the network, and the height of `-resume-above` if it was
  started so. A sign log of another network stops the signer at its start.
- **It is written again at every start**, in one atomic step (a temporary file, synced, renamed over
  it, and its folder synced). That drops a last line a crash cut short, which was never synced and
  so never signed, and the slots below the height before the highest.
- **The audit log** is opened for each line and not synced: the sign log is what the rules read. So
  a log rotated by renaming it carries on in a new file. A signature whose audit line can't be
  written is not given (`500`).

**2026-11-03 (as built, after the review)** (signer review):
- **A second start binds, then locks, before it reads the sign log** (`82e36a6`; finding 1). The
  signer read and rewrote its sign log as it loaded its config, before it bound its port. A second
  start of a running signer, by an operator reading its key's digest, or by a second container on
  its volume, then renamed its rewrite over the live log and dropped the lines appended in between.
  A later restart freed a slot already signed: a double signature, which the review proved. Now the
  signer reads its config, binds `listen`, and takes an exclusive lock (`flock`) on the sign log's
  folder, held while it runs, and only then reads the sign log. A second start fails at the bind or
  at the lock. The folder is locked, not the file, since a rewrite renames a new file over the log;
  so two signers can't share a folder. The lock is built on Unix systems other than Solaris and AIX,
  whose Go has no `flock`. Elsewhere the signer still builds, and locks nothing: it is meant to run
  on Linux.
- **A sign log that is a symbolic link is refused** (`df7f6fd`; finding 7). The rewrite renamed a
  new file over the link, not over the file it pointed to. A restart that found the link again read
  that old file, and could sign again the slots signed since.
- **A limit: the audit log grows without bound** (minor finding). The review put it at about 1 GB a
  day at the fetcher's cap. A full disk withholds every signature, as above, so the bank stops
  voting. The runbook is to say how to rotate it.

**2026-11-03 (as built, after the review)** (the recheck). **A limit:** the lock is `flock`, which
does not hold across machines on NFS. Two signers on two machines that share one log folder over NFS
are not stopped by it. The docs are to say so.

### 2.6 When the signer has lost its sign log

A signer that starts with no sign log does not know which slots it already signed. It must refuse
every vote and proposal until the operator starts it with `-resume-above <height>`, read off the
network's status (any bank's public `/hdls/status`). It then refuses every slot at or below that
height. It is one number an operator can read out by phone, like the slow-down switch of Phase 3.
Co-signatures and governance votes are unaffected: they have no slot.

The engine's own lost-sign-log wait (`Node.waitForOwnSignatures`) stays as it is. The engine and
the signer can each lose their log without the other.

**2026-11-02 (as built)** (`4b017e1`). `-resume-above <height>` writes a new sign log, whose first
line records the height, and is refused where a sign log exists: that log knows what was signed. The
height stays refused after every restart. Without a sign log, the signer signs everything else and
writes no log.

**2026-11-03 (as built)** (`84acd8f`). The quickstart writes each signer the sign log of a new
network, as `-resume-above 0` would. The compose file starts a container the same way every time,
and the flag is refused once a sign log exists.

**2026-11-03 (as built, after the review)** (signer review, finding 9, for the docs). After a
break-in on the engine, the signer's sign log must not be reset: it holds the slots the intruder may
have had signed. The runbook is to say so.

### 2.7 The engine and the wallet

- **One interface.** `*signing.Signer` becomes an interface with the methods the code uses today:
  `Sign(domain, fields...)`, `SignBase64`, `PublicKey`, `PublicKeyBase64`. Two implementations:
  - the **local signer**, today's struct, with the key in the process: tests, `cmd/demo`, and any
    bank that chooses it;
  - the **remote signer**: it sends the domain and fields to the signer and verifies the answer.

  Every call site (`SignVote`, `SignProposal`, `SignReceipt`, the wallet's `SignAs...`) keeps its
  code; only the type of the signer changes.
- **The engine's config.** In place of `privateKey`, a `signer` object: `{"url": "...",
  "secretFile": "..."}`. `publicKey` stays. At start the engine makes its self-test signature
  (`keySelfTest`) through the signer and verifies it under `publicKey`, so a signer holding another
  key, or none, stops the engine before it joins.
- **Signing on the event loop.** The engine signs where it signs today, on its event loop, with a
  deadline of 1 s per request. A failed or late signature is treated as today's failed sign-log
  write: the vote is dropped and retried later (`consensus/driver.go`, `castVote`). After a failure
  the remote signer fails fast for the next second, so a signer that is down does not slow the loop
  on every step; the engine logs it once a minute, as it logs a failing peer. A bank whose signer is
  down is a validator that does not vote: the chain goes on while the others hold more than ⅔.
  §9 Q6 asks whether to sign off the loop instead.
- **The wallet.** A bank's wallet may name a signer instead of a key file: `-signer-url` and
  `-signer-secret-file`, with the client's secret. Everything else, the pin, the drafts and the
  pages, stays as it is. A customer's wallet keeps its own key file, as today.

**2026-11-03 (as built)** (S2: `e450e09`, `f123726`, `8a9c48d`, `d1eda1f`, `a094762`):
- **The interface.** `signing.Signer` is an interface. Today's struct is `LocalSigner`, still made
  by `NewSigner` and `Generate`; `RemoteSigner` is the second. Every call site changed only its
  type.
- **The config's signer object is `{"url", "client", "secretFile"}`.** The spec writes
  `{url, secretFile}`, but the HMAC header names the client, so the object names it too, as the
  signer's config does. Exactly one of `privateKey` and `signer` is set; both, or neither, is
  refused with a message that says which to set. The secret file is read by
  `signing.ReadSecretFile`, which the signer uses for its clients too.
- **The engine needs its signer at start.** The self-test goes through it, so an engine whose
  signer does not answer, holds another key, or does not know its client, stops before it joins,
  with the reason (`key self-test: ...`). The quickstart's engines restart on failure for that.
- **The slot memory** (`8a9c48d`). A request that timed out at the engine may still have been logged
  and signed by the signer, which then holds the slot. The driver casts a dropped vote again at its
  next event, and that may be another vote in the same slot, such as the nil prevote of the timeout:
  the signer would refuse it as a conflict, an alarm against an honest engine. So the remote signer
  keeps the fields of the first request it sent in each slot, sends those same fields again, and
  refuses other fields itself, without a request. It forgets the slots below the highest height
  sent. The cost: a slot is held even when its request never reached the signer, so the engine can
  lose its vote in that slot, at most one round.
- **A limit: the slot memory is in memory only.** After the engine restarts it is empty. If a vote
  timed out before the restart, after the signer signed it, the restarted engine may ask for another
  vote in that slot, and the signer refuses it as `REFUSED CONFLICT`. No second signature can come
  of it, but the audit log then shows a conflict against an honest engine: a conflict line just
  after an engine restart may be this, not a break-in.
- **The log lines** are `signer: requests to the signer at <url>/sign fail: <error>` and
  `signer: the signer at <url>/sign answers again`: one when the outcome changes, at most one a
  minute. A call refused without a request (in the fail-fast second, or by the slot memory) logs
  nothing and opens no fail-fast window.
- **Receipts and votes share one remote signer**, so a receipt that fails opens the fail-fast window
  for votes too.
- **The HMAC is computed in two places**, `signing/remote.go` and `cmd/signer`; a test checks that
  they agree.
- **The tests of engines that sign through signers** are in `cmd/signer` (`a094762`), beside the
  signer's service.

**2026-11-03 (as built)** (S3: `b387452`). The wallet's flags are `-signer-url`,
`-signer-secret-file` and `-signer-client`, the last added for the reason the engine's object names
its client; the three go together. The wallet file then holds the bank's public key alone,
`{"publicKey": "..."}`, so `-wallet`, the pin and the state files stay where they are. A wallet file
that holds a private key, beside a signer, is refused, and a missing one is an error, never a new
key. A refused domain comes back as the signer's reason, which the page shows.

**2026-11-03 (as built, after the review)** (signer review):
- **One receipt in flight** (`4003253`; finding 2). A burst of payments made the node ask for many
  receipts at once. They queued at the signer's one lock, the engine's next vote waited behind them
  past its second, and the fail-fast window then refused votes for a second more. The review proved
  it with a signer 5 ms slower per signature. Now the remote signer keeps at most one receipt in
  flight. A receipt asked for while another is out is refused at once, without a request, and that
  refusal opens no fail-fast window. The node already answers such a payment `202`, with no receipt.
  The signer drops a request whose client has gone (§2.2's note). A receipt that is sent and fails
  still opens the window, as a vote's does: the signer is then failing for both.
- **Finding 5, declined: a second outage after a quick recovery.** When the signer fails, comes
  back, and fails again, all within a minute of the line that said it fails, the second outage has
  no line of its own: the log shows one outage, until the line that it answers again. That line is
  never left out: the engine asks the signer at every block, and at least at every idle heartbeat (5
  minutes), so it comes at the first request after the minute.

**2026-11-03 (as built, after the review)** (the recheck). **A limit:** the wallet's requests, the
desk's and the admin page's, share the signer's one lock with the engine's votes. Only receipts
are held to one in flight, so a busy wallet can still hold a vote up. The docs are to say so.

### 2.8 The key at rest

The signer's key file uses the wallet's format (`ui/keyfile.go`): the private key encrypted with
AES-256-GCM under a key derived by PBKDF2-HMAC-SHA256 from a passphrase, 600,000 iterations. The
passphrase comes from a file (`passphraseFile`: a container secret, or a file on a USB stick), never
from the config. The code moves from `ui/` to the root module, so the signer and the wallet share
it. A plain key file stays possible, for tests. §9 Q4 asks.

`crypto/mldsa` holds the expanded key in memory while the signer runs. Keeping it off a running
machine needs hardware, which is out of scope (§1).

**2026-11-02 (as built)** (K1: `59fbcbf`, `e32edd8`):
- **The move.** The format is in `signing/keyfile.go`. The wallet keeps only finding the passphrase
  and making the key on first use. Two key files written by the wallet's code before the move are in
  `signing/testdata`, and still open.
- **An empty passphrase file is refused**, so `keygen -encrypt` never writes a plaintext key by
  mistake. Only one trailing newline is taken off a passphrase.
- **A nonce of the wrong size** is refused, where AES-GCM used to panic.
- **`keygen -encrypt <file> -passphrase-file <file>`** writes a new key pair as a sealed key file,
  mode 0600, refuses a path that exists, and prints the public key. The wallet's error messages
  about key files were reworded with the move.
- **The signer** (`4b017e1`) refuses a `passphraseFile` beside a plaintext key file, and logs that a
  plaintext key file is fine for a test, not for a bank. The public key the file carries must be the
  one its private key derives.

**2026-11-03 (as built, after the review)** (signer review):
- **A key file that holds its key both sealed and in plaintext is refused** (`61175c1`; finding 8).
  It was taken, with the sealed key used; the plaintext copy beside it made the seal pointless.
- **`WriteKeyFile` syncs the folder after its rename** (`dea7c60`; minor finding), so a crash can't
  lose the rename. Windows, which can't sync a folder, skips that step.

**2026-11-03 (as built, after the review)** (the recheck). **Declined:** ignoring `EINVAL` from
that folder sync, which some file systems return for a folder. A folder that can't be synced makes
`WriteKeyFile` return the error, after the rename, as the sign log's rewrite does.

## 3. The name service

### 3.1 What is on chain today

Every name may list a public address and up to 4 IP addresses, signed by its own key in an account
update (Phase 3, §4). Names are already valid DNS labels: 1 to 32 lowercase letters, digits and
hyphens, starting and ending with a letter or digit (`ledger/name.go`). Nothing on chain checks that
a name controls the addresses it lists, and two names may list the same IP address
(`node/allowlist.go` says so). The public folder has a file per name and per key
(`hdls/resolve/name/<name>`, `hdls/resolve/key/<hash>`), and none per IP address.

### 3.2 The zone file

The engine writes one more file into its public folder, `hdls/zone`, in the standard zone file
format, and the fetcher serves it like the others. Any DNS server (NSD, Knot, BIND, CoreDNS) loads
it as an authoritative zone. A DNS operator who adopts the name service (D5 of Phase 3) fetches
the file from any bank's public face, or from several and compares them, and reloads their server
when it changes.

- **Why a file and not a DNS server.** The standard library has no DNS server and no code to read
  or write DNS messages. A server would mean writing a parser for untrusted network input by hand,
  a few hundred lines that a reviewer must trust. A zone file is text the engine writes from the
  committed state, a few dozen lines, and the DNS servers that read it are run by people who run
  DNS. §9 Q7 asks.
- **Anyone can check it.** The file is a pure function of the committed accounts and the config's
  zone settings, so anyone can rebuild it from the public `hdls/accounts` file and compare.
- **When it is written.** At start, and after each commit or catch-up that changes a name record,
  like the resolve files. The SOA serial is the height at which it was written, modulo 2³².

What it holds, for a zone `hdls.example.org` set in the config:

```
$ORIGIN hdls.example.org.
$TTL 60
@                SOA  ns1.example.org. hostmaster.example.org. 1043 300 60 86400 60
@                NS   ns1.example.org.
bank-a           A    203.0.113.10
bank-a           TXT  "hdls-public-address=https://bank-a.example.org"
_validators      A    203.0.113.10
cb00710a._ip     TXT  "bank-a"
_443._tcp.bank-a TLSA 3 1 1 0d6f...e1
```

- **A and AAAA**: one per IP address a name lists.
- **TXT `hdls-public-address`**: the name's public address.
- **`_validators`**: one A or AAAA per IP address of every current validator: the IP filter of R1.
  A firewall that takes DNS names in its rules, or a script that resolves it every minute, keeps
  an allow list without reading the chain.
- **`<hex>._ip` TXT**: the names that list an IP address, one string each, under a label of the
  address's bytes in hex (8 digits for IPv4, 32 for IPv6). It is the lookup from an address to names
  (§3.4). A name could not publish real reverse DNS (`in-addr.arpa`) for an address it does not own,
  so the lookup lives in the zone.
- **TLSA**: one per TLS key record (§3.3), under the port of the name's public address (443 for
  `https://` without a port).
- **Frozen names** are left out, but for the `_ip` lookup, which names them with ` (frozen)` after
  the name. §9 Q8 asks.
- **The zone settings** in the engine's config: `zone` (the zone's name), `zoneNameServers` (the NS
  records), `zoneContact` (the SOA mailbox) and `zoneTimeToLive` (seconds, default 60). No zone
  setting, no zone file.

**2026-11-02 (as built)** (N1: `5e91b3b`, `231931c`):
- **When it is written.** The file is written again when it differs by more than its serial from the
  one last written: a change of a name's records, or of the validator set, whose addresses it lists.
  A commit that changes neither leaves the file and its serial as they are, so a DNS server, which
  reloads a zone when its serial grows, reloads it only when it changed.
- **Its form.** No record names a class: `IN` is the default. A TXT text longer than 255 bytes, the
  most one string holds, is split into several strings, which a reader joins. An address two
  validators list is in the validators' set once.
- **The config.** `zone` needs `publicPath`, where the file goes, and each name in the settings must
  be a domain name. A node started without a zone setting removes a zone file a former run left.
- **`named-checkzone` did not run in the build.** The test that runs BIND's checker on the zone of
  a known state is there, and skips where the checker is not installed. It was not installed on the
  build machine, and no container image holding it could be fetched then. On 2026-11-03 it ran from
  BIND 9.20's own image, and took that zone; `named` served it and answered for each kind of record.

**2026-11-03 (as built, after the review)** (name service review):
- **The validators' label** (`add5536`). `_validators` is now
  `validators-of-the-height-in-progress`. BIND's default check of names (`check-names`) refuses a
  whole primary zone when the owner of an A or AAAA record is not a host name, and an underscore is
  not allowed in one. The new label is 36 characters, more than the 32 a ledger name may have, so no
  name can take it. Wherever this spec says `_validators`, in the example above, the bullet on it,
  §3.4 and §9's Q8, read `validators-of-the-height-in-progress`.
- **`_ip` is one record per address** (`d23e6c3`). The bullet above gives one string per name; the
  first build wrote one TXT record per name instead. BIND 9.18.28 and later refuse a zone with more
  than 100 records of one type under one owner (`max-records-per-type`). Now each address has one
  TXT record, with a string for each name, in name order, at most 100. Past 100, a last string says
  `and <count> more: hdls/resolve/ip/<address>`, the IP lookup file, which lists them all.
- **The same BIND limit holds for the validators' label**: more than 100 addresses of one kind, as
  26 validators with 4 IPv4 addresses each would publish, make BIND refuse the zone. The zone keeps
  every address, and the runbook is to tell an operator to set `max-records-per-type 0;` for this
  zone.
- **A name server inside the zone is refused** (`8e1b01c`). Its address records would have to be in
  the zone, whose names are the ledger's, so its address would be whatever the customer holding that
  name lists, or none, and then BIND refuses the zone.
- **The zone's name is at most 208 characters** (`84d7c56`). The longest name the file writes,
  `_65535._tcp.<a name of 32 characters>.<zone>`, adds 45 to the zone's, and a domain name has at
  most 253.
- **TLSA records only for an `https://` public address** (`b90851e`). A name whose public address
  was `http://` got TLSA records under `_443`, claiming a TLS service it never declared.
- **Loopback addresses are left out of the validators' set** (`9428f50`), as the allow list leaves
  them out (Phase 3, review N1). A validator could otherwise publish `127.0.0.1` and put it on every
  firewall that resolves the set.
- **"Anyone can check it" holds apart from the serial** (`8877df0`). A zone rebuilt from
  `hdls/accounts` matches the file but for its SOA serial: the serial is the height at which this
  node last wrote it, which the accounts do not tell. The comments now say so. For the same reason,
  a change of the zone settings at a restart, with no block committed since the file was last
  written, rewrites it with the serial it had, and a secondary DNS server does not reload it.
- **For the runbook:** the zone should be a domain of its own, not one under the bank's web domain,
  since its names are the customers'; DANE clients use TLSA records only in a zone signed with
  DNSSEC; and a zone file whose write failed is written again only when the zone changes, or at a
  restart.
- **The owner kept the TLSA records** (2026-11-03), asked in `review/phase4_progress.md`: each bank
  can be a DNS provider fed by the ledger, and a name reached by its name in the zone gets its TLS
  keys there. BIND 9.20 checked and served the zone, TLSA records included.

### 3.3 TLS key records

A name may list up to 2 TLS keys, the current one and the next, so it can roll its web server's key
without a gap. Each is the lowercase hex SHA-256 of the key's SubjectPublicKeyInfo: the hash that
DANE's TLSA records call "3 1 1".

- On chain: a new field `TLSKeys` of the account, in the genesis, in the account update (signed by
  the name's key, like the IP addresses, with the same `"-"` to clear) and in the state digest. A
  new genesis.
- Published in the resolve files and as TLSA records in the zone.
- **What reads them now.** A bank's wallet may pin its bank's web server by the chain: when its
  bank's public address is `https://`, the wallet accepts the server's certificate only if its key's
  hash is one of the bank's TLS keys on chain, and needs no certificate authority. §9 Q10 asks
  whether to build that now or only publish the records.
- The payment key stays off the web server (R7 of Phase 3), and the TLS key's algorithm is the
  web server's choice, not the chain's.

**2026-11-02 (as built)** (L1: `db2ddcb`, `c2fb816`):
- **On chain.** `Account.TLSKeys` holds at most `MaxTLSKeys` (2), each 64 lowercase hex digits
  (`ledger.CanonicalTLSKey`), none twice. An update signs them after the IP addresses, count first,
  and the account digest covers them in the same place. An update that changes only the TLS keys is
  a change. The resolve answer and the resolve files carry them as `tlsKeys`.
- **The digest tags stay `v2`**, as Phase 3 left them when it added the IP addresses: the new layout
  comes with a new genesis either way.
- **The wallet's account form** gains a TLS keys field: keys separated by spaces replace the list,
  `-` clears it, and blank leaves it. The wallet checks the update with `ledger.UpdateFitsState`
  before it takes a block, as for the IP addresses. The wallet shows no name's IP addresses, and so
  shows no TLS keys either: the form only publishes them. The demo and the quickstart publish no TLS
  keys.

**2026-11-02 (as built)** (N2: `50eb12c`). The wallet's pin, as Q10 decided:
- **Only the banks' records count:** the validators of the newest state the wallet verified. Any
  name may publish any address (§3.1), so a name that is not a bank could otherwise list a bank's
  address with a key of its own, and take the bank's channel or break it. Where two banks publish
  one address, the server's key must be one each of them lists.
- **The address must match exactly:** the request's `scheme://host[:port]` is a bank's public
  address as the bank wrote it. A bank whose name is frozen pins nothing.
- **Before the wallet holds a verified state,** at first contact and after each start, the normal
  check applies: the state itself is believed only when the pinned ⅔ signed it, whatever channel it
  came by.
- **One transport per address and pin,** so a connection opened under one pin, or under none, is
  never used under another. Both of the wallet's clients keep their timeouts and their refusal of
  redirects.

### 3.4 The IP filter and the flood (R1, R2)

- **The filter.** `_validators` in the zone (§3.2), and, for programs that read HTTP, a public file
  `hdls/resolve/ip/<address>` listing the names that list that address (frozen ones marked), served
  by the fetcher at `GET /hdls/resolve/ip/{address}` after `ledger.CanonicalIPAddress` checks it.
- **Naming who floods.**
  - The engine's allow list refusal line (logged at most once a minute) names the claimants of the
    refused address: `refused 198.51.100.7: listed by carol (not a validator)`, or `listed by no
    name`.
  - The fetcher, once a minute in which its cap answered 429, logs the five addresses that sent
    the most submissions, each with the names that list it.
  - Behind a CDN the fetcher sees the CDN's addresses. It may take the client's address from a
    header the CDN sets (`-client-address-header`), but only from connections whose address is in
    `-cdn-addresses`. §9 Q11 asks.
- **What people do with it** (for the runbook): the bank of record of a named flooder calls its
  customer; if the flood goes on, the ⅔ freeze the name. For a validator, a ⅔ freeze also takes it
  off every bank's allow list, because the list is the current set's. An address no name lists goes
  to the CDN's or the provider's abuse desk, as today.
- **The honest limit.** A name can list an address it does not control. The lookup tells a bank who
  *claims* an address, not who uses it. Two names listing one address are both shown. A rule of
  one name per address would let the first claimant squat on someone else's, so this spec adds
  none. §9 Q9 asks.

**2026-11-02 (as built)** (N1: `5e91b3b`, `185e257`, `de309c2`):
- **The IP lookup file** `hdls/resolve/ip/<address>` is a JSON list of the names that list the
  address, in name order, a frozen one as `"<name> (frozen)"`. It is written when a commit or a
  catch-up changes it, and removed when no name lists the address any more.
- **The allow list's refusal line was built in N1, not in S2** (§7), since it is attribution. Once a
  minute, it names the address it refused the most in that minute, counting up to 1,024 addresses,
  and the names that list it, each marked as a validator or not: for example
  `..., 312 of them from 198.51.100.7: listed by carol (not a validator)`, or `listed by no name`.
- **The fetcher** takes the client's address from the last comma-separated entry of the header's
  last line, since a CDN appends the address it saw after any a client wrote there itself.
  `-client-address-header` and `-cdn-addresses` go together. The line it logs once a minute in which
  its cap answered `429` names the five addresses that sent the most, each with the names its
  engine's IP lookup file gives.

**2026-11-03 (as built, after the review)** (name service review):
- **A CDN header with no address makes the sender unknown** (`f43d2e6`, `a1dffc7`). On a connection
  from a CDN address, a header that held no address that parses, such as one with a port, or an IPv6
  address in brackets, left the sender as the CDN's own address. A name that listed that address was
  then named as the sender of everything the CDN passed. Such submissions now count as one unknown
  sender, and the line says `senders the CDN's header gave no address for sent <count>`.
- **A limit, for the runbook:** the counts are by exact address. A flooder that holds many IPv6
  addresses, as anyone with a /64 does, spreads its submissions over them, and may not show among
  the five.
- **The filter's label** is `validators-of-the-height-in-progress`, where this section's first
  bullet says `_validators` (`add5536`; §3.2's note).

**2026-11-03 (as built, after the review)** (the recheck). **Declined:** naming the sender of a CDN
header whose last entry carries a port. It stays an unknown sender, as `f43d2e6` chose: the fetcher
names a sender only from an entry that parses as an address.

## 4. The joiner: trust a frame, not a peer

### 4.1 Today

- A node checks a newer checkpoint (`verifyCheckpoint`, `node/sync.go`) against its **anchor**, the
  validator set of its own committed state: genesis for a fresh node. It adopts the checkpoint when
  names of the anchor holding **more than ⅓** of its power signed the checkpoint's commit under the
  anchor's keys (`anchorVouches`), and the commit is a quorum of the set the checkpoint carries.
- One peer's checkpoint is enough, and the node takes the highest one that passes.
- Nothing walks from one set to the next. When the names of the anchor that still sign hold ⅓ of
  its power or less, the node stays behind until an operator seeds it with a snapshot copied from a
  bank it trusts (`findings_20261027.md`, "joiners trust their seed").
- The block header names the set that signs the block (`ValidatorsHash`) but not the set that will
  sign the next one, so a node cannot check that a new set was the one the old set agreed to.

What one peer can do today: it cannot forge the signatures a commit carries, so it cannot hand a
node a false state that more than ⅓ of the anchor did not sign. It can withhold the newest
checkpoint, and if keys of more than ⅓ of an old set were stolen or sold, it can hand a node a
forged history signed with them.

### 4.2 More than ⅔ of the trusted frame (R3)

`anchorVouches` must require names holding **more than ⅔** of the trusted frame's power, not ⅓.
This holds for every checkpoint: a joiner's first one, the catch-up of a node that fell behind, and
a carried one (`POST /hdls/checkpoint`). Forging a history then takes the keys of more than ⅔ of the
power of a set the node already trusts, not ⅓.

The price: a node away while more than ⅓ of the set changed can no longer jump in one step. It walks
(§4.3).

**2026-11-02 (as built)** (J1: `9f05f44`, `d8ce87e`). `anchorVouches` asks more than ⅔, on every
path: the startup sync, catch-up and a carried checkpoint. A carried checkpoint walks no frames: a
carrier brings no record, so more than ⅔ of the node's own set must have signed it.

### 4.3 Frames that sign off their successor

- **The header names the next set.** `BlockHeader` gets a field `NextValidatorsHash`: the
  `ValidatorsHash` of `SetFor(state)` after this block, the set that will sign the next height.
  `ProposeBlock` and `ApplyBlock` set and check it, and it goes into the block hash. A new genesis.
  The commit of a block is then also more than ⅔ of its set signing off the set that comes next.
- **The walk.** From its trusted frame, a node that cannot adopt the newest checkpoint directly:
  1. reads the list of frames since its trusted height (§4.4);
  2. for the next frame, at height `b`, takes the record entry of height `b - 1`, whose commit must
     be more than ⅔ of its trusted frame under that frame's keys, and whose header's
     `NextValidatorsHash` must be the hash of the new frame's set, listed in the record entry of
     height `b`;
  3. makes the new frame its trusted frame, and repeats until it can check the newest checkpoint
     against the frame it holds.
- **How far back.** The record keeps each height for at least `retainBlocks` heights (604,800 by
  default). A node whose trusted frame is older than that cannot walk; it needs a newer seed from its
  bank, as today. The record entry gets `NextValidatorsHash` too, so the block hash can still be
  recomputed from it.

**2026-11-02 (as built)** (L1: `3e982a7`). `ProposeBlock` sets `NextValidatorsHash`, `ApplyBlock`
refuses a block that names another set, and `BlockHashOf` takes it after the validators hash. So
block `h + 1`'s `ValidatorsHash` is block `h`'s `NextValidatorsHash`, which a test checks in static
and dynamic mode. A checkpoint's own check (`Checkpoint.Consistent`) does not compare its header's
`NextValidatorsHash` with its state: the commit covers it, and the walk compares it with each
frame's set.

**2026-11-02 (as built)** (J1: `d8ce87e`). The walk:
- **When.** A checkpoint that more than ⅔ of the trusted frame did not sign, but that is sound on
  its own (its commit is a quorum of the set it carries, which hashes to its header), starts a walk.
- **Where.** It reads `hdls/frames` from the public face of each other validator of the trusted
  frame, in turn, and the record units it needs from the same face. It takes at most 100 frames
  (`maxFrames`) from a face, and reads each file within the checkpoint's cap, 64 MiB.
- **Its check is broader than step 2.** The set that signed the entry before a frame is the one the
  record lists for it (the last entry at or before it that lists a set), bound to the entry by the
  block hash, recomputed from the entry and that set. The commit must be a quorum of that set, and
  hold more than ⅔ of the frame the walker holds, under that frame's keys. So the frames need not be
  consecutive: a frame that more than ⅔ of the held one signed off is taken, as a checkpoint is.
- **When it ends.** Once more than ⅔ of the frame it holds signed the checkpoint, the loop checks
  the checkpoint again against that frame. A frame not signed off, or a face's last frame, ends the
  walk of that face, and the next face is read.

**2026-11-03 (as built, after the review)** (joiner review):
- **The walk checks only the entry before a frame** (`68d0798`; findings 2 and 5). Step 2 also reads
  the record entry of height `b`, and the first build checked that entry: that it builds on the
  block before, lists the frame's set, and that its commit is of its height. No signature the walk
  checks covers that entry, so a face that lies could make it pass; the checks only made an honest
  frame fail, when a face did not hold that entry yet. They are gone, and the walk reads one record
  entry per frame. The block hash covers the height, so the commit's height needs no check of its
  own. No test failed before with either check of the entry before taken out (`anchorVouches`, and
  the recomputed block hash); one does now.
- **A walk reads the frames only of a face ahead of the node** (`83f0df5`; finding 3). A checkpoint
  that is sound on its own, but that no walk reaches, costs a forger nothing to make. It was walked
  again for every peer that served it, at every tick, and each walk read every face's frames, even
  when no face was ahead of the node. Now a walk asks a face's status first, and skips a face that
  is not ahead: it lists no frame after the trusted one.
- **Not taken: one walk per catch-up call.** The review also suggested walking a candidate once per
  call, whatever the number of peers that serve it. That is a larger change to catch-up. So a node
  that is behind still walks such a checkpoint once for each peer that serves it, at each tick, each
  walk reading the frames of the faces ahead of it, at most 100 frames each.
- **A frame is listed only when the names or keys change** (`6ddeabb`; finding 4): see §4.4's note.
  The walk stays safe: a walker that holds an older frame's powers needs more than ⅔ of that frame,
  by those powers, to sign, which is the trust of a node that checks a checkpoint against its own
  older view. It is less complete: §4.4's note gives the limit the recheck proved.

### 4.4 Every validator of the frame is asked

- **The frames file.** The engine writes one more public file, `hdls/frames`: one JSON line per
  change of the validator set since the oldest height the record holds, with the height, the set's
  hash and the set. It is an index: everything in it is checked against the record entries (§4.3),
  so a wrong line only wastes a fetch.
- **Asking all of them.** A node catching up, or a joiner, asks every validator of its trusted
  frame, at its public address and at its gossip addresses where it is allowed in, not only its
  bootstrap peers. It adopts the highest checkpoint that passes §4.2 or §4.3, so a peer that
  withholds the newest one is outvoted by the others.
- **A fork is an alarm.** If two checkpoints that both pass carry commits for **different blocks at
  one height**, more than ⅓ of a set signed twice. The node stops, adopts neither, and logs both
  commits as evidence, for people to resolve. It does not pick one.

**2026-11-02 (as built)** (J1: `45a427c`, `d8ce87e`; the fetcher's route in N1, `de309c2`):
- **The frames file.** Each line is a frame: the first height its set signs, the set's hash in hex,
  and the set (each name, key and power). A frame is listed at the commit of the height before it,
  so the file lists only the frames whose start the node committed: its own record holds the entry a
  walker checks each line against, and none of the heights a catch-up skips. It keeps the frames
  that start within `retainBlocks` of the committed height, and the newest 100 at most. It is the
  only copy: `Open` reads it back. The fetcher serves it at `GET /hdls/frames`; that route was built
  in N1, the file in J1.
- **Asking all of them.** Catch-up, and the startup sync with it, asks every node it knows its
  status at once, at every tick: its bootstrap peers, its gossip peers, and every other validator of
  its trusted frame at its public address. It fetches a checkpoint only from the peers ahead, the
  highest first, and takes the highest that passes. The status now gives the last block's hash
  (`lastBlockHash`), and a peer that reports the block of a checkpoint that already passed is not
  fetched again. Phase 3's one public face a tick (review R2-2) is gone: every face is asked.

**2026-11-03 (as built)** (J1: `bd3fb41`). The fork alarm logs both commits in full and halts the
node (§4.5's note). Only a restart clears it.

**2026-11-03 (as built, after the review)** (joiner review):
- **A fork against the node's own block** (`a4da640`; finding 1). Catch-up fetched only from peers
  ahead of the node, and compared their checkpoints with each other, never with the node's own
  block. So a node on one side of a fork took the other side silently: a peer at its height on
  another block was cut off by the break, and one further ahead was adopted. Now catch-up knows the
  node's own last block. A peer at the node's height that reports it costs no fetch. One that
  reports another block is fetched, and if its checkpoint passes, the node raises the divergence
  alarm, seated or not, logs the network's commit, and halts.
- **What stays undetected.** A checkpoint does not say which block of the node's height it builds
  on: seeing that would need the chain of headers between the two. So when no peer answers at the
  node's own height, a node on one side of a fork adopts the other side's checkpoint silently if it
  is two or more heights ahead. An observer does so at one height ahead too, since the check of a
  checkpoint one height ahead (§4.5's note) is a seated node's only.
- **Lines above the height in progress are dropped** (`083fb8b`; finding 6). After a node is opened
  from an older snapshot than its last, or seeded onto the other side of a fork, `hdls/frames` still
  held lines of heights it has not committed. They came first, before the lines it lists next, and a
  walker stopped on them. `Open` now drops them, and writes the file again. The review also asked
  the node to drop such lines as it lists a new frame. That is left out: once `Open` drops them,
  none is left, since the height in progress never goes down while the node runs, so that code could
  never act, and no test could fail without it.
- **Frames only on a change of names or keys** (`6ddeabb`; finding 4). In dynamic mode a payment
  that moves a validator's stake changes the powers, and each such change started a frame: the 100
  frames spanned about 100 busy blocks, and the file, about 5.6 MB with 21 banks, was written again
  at almost every block. A frame is now listed only when the next set seats other names, or a name
  under another key, than the set that signed the block. Its line still gives the next set as it is,
  powers included, as the block's `NextValidatorsHash` names it. So where this section's first
  bullet says "one JSON line per change of the validator set", read: per change of its names or
  keys.
- **A limit: the walk reads only public faces** (finding 7). The gossip and operator ports serve no
  frames and no record units, so a validator with no public address, or whose public face is down,
  adds nothing to a walk. The review left it to the owner. It stays a limit, for the docs to state.

**2026-11-03 (as built, after the review)** (the recheck):
- **A limit of `6ddeabb`: the walk under power drift.** The recheck judged `6ddeabb` safe but less
  complete, and proved this limit with a test. A walker holds each frame with its powers as they
  were at the frame's first height, and in dynamic mode the powers drift within a frame as stake
  moves. When some of the frame's validators are absent, the ones that sign may hold more than ⅔ of
  the power now, but not of the powers the walker holds. The walk stops there, and the joiner needs
  a newer seed from its bank. The case is rare, and the owner's rule is that rare cases go to
  people: it is documented, not coded. Listing a frame at every change of power again would close
  it, at the cost finding 4 found.
- **A limit: the check against the node's own block needs a peer at the node's height** (`a4da640`).
  On a busy chain the peers seldom stay at the node's height, so the check seldom has one to compare
  with ("What stays undetected", above). The docs are to say so.
- **The divergence check at the node's own height used the next height's set** (low). A checkpoint
  of the node's committed height was checked against the committed view's set, which signs the
  height in progress, not the set that signed that height. Fixed in `d3daa0e`: such a checkpoint is
  checked against the set that signed the node's own block there, the committed view's
  `lastCommitValidators`, and no other. At the genesis there is none, and no checkpoint of height 0
  passes.

### 4.5 Confirmed by its own accepted signature (R4)

- A validator's precommit counts only toward a commit of the block it voted for, and it computed
  that block from its own state. So the first commit that holds the node's own precommit is more
  than ⅔ of the current set agreeing with the node's state.
- From then the node is **confirmed**. Its status (`/hdls/status` on the operator port) says
  `"confirmed": <height>`, or `"confirmed": null` before.
- If the node, while seated, sees a commit (more than ⅔ of the set) for a block that its own state
  says is invalid, its state and the network's disagree. It logs both blocks, stops signing, and
  waits for the operator; it does not re-seed itself. §9 Q15 asks.
- An observer, a node that never signs, is never confirmed this way. It relies on §4.2 to §4.4.

**2026-11-03 (as built)** (J1: `bd3fb41`):
- **The status** says `"confirmed"`: the height of the first commit that holds the node's own
  precommit, while its name is seated under its own key, or `null` before. It is kept in memory
  only, so a restart starts unconfirmed. It says `"halted"` too: the alarm, or `null`. Both are in
  the public folder's status, which stays the route's answer byte for byte.
- **The divergence alarm** comes two ways, each only while the node is seated: more than ⅔ of the
  set precommit, in one round, a block the node refused (it keeps each refused block's header and
  reason for the height); or the node is handed a checkpoint of its height in progress whose block
  does not build on its last block. It logs both blocks and halts.
- **A halted node** runs no driver, so it signs nothing; it adopts no checkpoint, carried ones
  included; and catch-up asks nothing. Only a restart clears it.

**2026-11-03 (as built, after the review)** (joiner review):
- **A third way**, for any node: a checkpoint of its committed height on another block
  (`a4da640`; finding 1; §4.4's note).
- **Tests of four rules that no test held** (`13532dc`; finding 8, which the commit calls 7, its
  place in the list of fixes): only the node's own precommit confirms it, not a precommit in its
  name while it runs under another key; an observer does not halt on a block it refused; a halted
  node's catch-up asks nothing; and a walk takes at most 100 frames from a face. Each test fails
  with its rule taken out.

### 4.6 What is still trusted

- **The first frame.** A fresh node trusts the genesis it is given; a joiner its bank seeds trusts
  that snapshot (R5). If the seed itself is false, every later check runs inside a false world. A
  joiner should check the seed's height and block hash with a second bank, by phone, before it
  starts. The runbook says so.
- **More than ⅓ of a set lying together** can still make two valid commits at one height. §4.4
  turns that into an alarm, not a silent choice.
- The wallet already takes a state only when more than ⅔ of its pinned power signed it
  (`ui/trust.go`), so this spec brings the node up to the wallet's rule.

**2026-11-03 (as built, after the review).** The joiner review found two more limits:
- **A node behind a fork may not see it.** §4.4 turns two valid commits at one height into an alarm
  only when the node is handed both, or one against its own block. A node that hears only the other
  side, ahead of it, adopts it (§4.4's note says when).
- **The public faces.** The walk reads frames and record units only from the validators' public
  faces (§4.4's note). Nothing a face serves is taken unchecked, but a face that serves nothing
  holds a walk back.

## 5. Config, commands and images

- **`cmd/signer`** (new): `-config <file>`, `-resume-above <height>`. Config fields: `listen`,
  `allowFrom` (IP addresses or prefixes), `genesis` (path), `name` (the bank's name), `keyFile`,
  `passphraseFile`, `signLogPath`, `auditLogPath`, `clients` (`[{"name", "secretFile", "domains"}]`).
  It refuses to start if its sign log is for another network. It does not check its key against the
  chain, which it does not read: the engine's self-test does (§2.7), and every client verifies every
  answer.
- **`cmd/keygen`**: `-secret` prints a new 32-byte client secret in hex. `-encrypt` writes an
  encrypted key file, reading the passphrase from a file.
- **`cmd/server`**: the `signer` object (§2.7); the zone settings (§3.2).
- **`cmd/fetcher`**: `GET /hdls/resolve/ip/{address}`, `GET /hdls/zone`, `GET /hdls/frames`;
  `-client-address-header` and `-cdn-addresses` (§3.4).
- **The wallet**: `-signer-url`, `-signer-secret-file` (§2.7).
- **`deploy/`**: the signer goes into the engine image as `/signer`. `compose.yaml` runs a signer
  per bank on an internal network with no published port, reachable from that bank's engine and
  from no other container. `cmd/quickstart` makes each bank's encrypted key file, passphrase file
  and client secrets, and no key goes into the engine's config.

**2026-11-02 (as built)** (S1: `4b017e1`; K1: `e32edd8`):
- **The signer's config is checked more strictly** than this list says. An unknown field is refused,
  so a misspelt one is never ignored. `name` must be a ledger name. `allowFrom` entries are written
  as the engine's `allow` entries are. Each client needs a secret of its own, or one could ask as
  another with the other's domains. A passphrase file beside a plaintext key file, and a domain the
  signer has no layout for, are refused (§2.3, §2.8).
- **Its start line** gives its listen address, `allowFrom`, its name, its network and its key's
  SHA-256, the name of the key's record in the public folder (`hdls/resolve/key/<hex>`), so that an
  operator can check it is the bank's key on chain.
- **`keygen -encrypt`** takes the file to write and `-passphrase-file` (§2.8's note).

**2026-11-03 (as built)** (S2: `d1eda1f`; S3: `b387452`, `7284c73`, `84acd8f`):
- **`cmd/server`'s signer object** names the client too (§2.7's note), and **the wallet** gains
  `-signer-client`, for the same reason.
- **The images and `make build`.** `deploy/Dockerfile` builds `cmd/signer` into the engine's image
  as `/signer`, and `make build` puts `bin/signer` beside the other programs, for a bank that runs
  it outside a container.
- **The compose file** runs, for each bank, a signer beside its engine and fetcher, and a wallet
  that signs through it, the desk and the admin page in one. The signer is on an internal network of
  its own, `signing-bank-a` to `signing-bank-d`, with no port published, joined by that bank's
  signer, engine and wallet only: the spec named the engine alone, but the wallet signs through it
  too.
  - `allowFrom` is the signing network's subnet. Podman 3.4 keeps one fixed address per container,
    on its first network by name, and the engine's fixed address must be on the ledger's network; so
    the signing networks' names sort after it, and the engine and the wallet take an address from
    the signing network's pool.
  - The engine and the wallet reach the signer by its fixed address: Podman runs no name service on
    an internal network.
  - The signer runs in `/quickstart`, where the quickstart's files keep their names, so its config
    gives relative paths.
  - An engine restarts on failure: its key self-test stops it until its signer answers.
- **The quickstart** writes, for each bank, its key file sealed under a new passphrase, the
  passphrase file, a secret for each of the signer's two clients, the signer's config and its log
  folder with a new sign log (§2.6's note), an engine config with a signer object and no
  `privateKey`, and the bank's wallet file, its public key only. The containers' user, 65532, is not
  the machine's user, so these files are readable by anyone, 0644, inside the quickstart's folder,
  which is its owner's only (0700); the log folder is 0777 inside it.
- **Run for real** under Podman: blocks commit, and each signer is reachable only from its bank's
  engine and wallet. The builder retagged the local images `go-ledger:before-p4-s3` and
  `go-ledger-ui:before-p4-s3` as a backup. The fixes to the signer's start (§2.5's note) came after
  that run; the recheck ran the containers again (below).

**2026-11-03 (as built, after the review)** (signer review, finding 1). The signer's start reads its
config, binds `listen`, locks the sign log's folder, and only then reads the sign log (§2.5's note).
So two signers can't share one log folder.

**2026-11-03 (as built, after the review)** (the recheck):
- **Any container on the shared network could drive a bank's wallet** (high). Each bank's wallet
  listened on the `ledger` network, which every engine, fetcher and wallet of the compose file
  shares, and the wallet signs through its bank's signer. Its guard, `Host`, `Origin` and
  `Sec-Fetch-Site`, stops a browser's page, never a program. The recheck proved it live: a payment
  sent from a container on `ledger` committed. A first fix moved each wallet to a network of its
  own bank, shared only with its engine. It was not merged: Podman 3.4 routes between networks that
  are not internal, and a container on `ledger` still drove each wallet there.
  The fix: the wallet takes `-password-file`, and asks for that password as HTTP Basic auth, under
  any user name, behind its guard. A wallet whose `-listen` is not a loopback IP address refuses to
  start without one (`b3a0554`). The quickstart writes a random password for each bank's wallet,
  `bank-a.wallet.password` and so on, and the compose file mounts it into that wallet only, as a
  secret (`589cede`). The wallets stay on `ledger`, so they still check the other banks. A customer's
  wallet on `127.0.0.1`, the default, asks for nothing. The docs: `4e41066`.
  An independent review of the fix found no way past the password; its low findings are fixed:
  the guard now comes first, so another site's page gets the guard's refusal, never a login box
  under its own name; the password is read before the wallet file, so a wallet that refuses to
  start writes no key; and `localhost` needs a password, since the hosts file or DNS resolves it
  (`09d5eb4`); the demo's flag help (`ab2415c`); and the docs (`4b8114d`). Over plain http the
  password crosses the network as it is, so a bank's wallet still belongs where only its operator
  reaches it.
  Run on Podman 3.4, rootless: each bank's wallet answered `401` to a container on `ledger`, with no
  password or a wrong one, and `403` under a rebound host name; with its password, `bank-a`'s
  wallet paid, with a verified receipt, and checked banks holding 100% of the power.
- **The containers ran again** after the signer fixes: 16 containers; every key self-test passed; a
  payment committed; and a second start of a signer was refused by the bind and by the lock, with
  its logs untouched.

## 6. Tests

Each rule marked "must" gets a test that fails before its code. Among them:

- **The signer:**
  - a second, different vote for a signed slot is refused and logged as a conflict;
  - the same vote again gets the same signature;
  - a vote below its highest height is refused;
  - a domain not in the client's list is refused;
  - a request with a bad HMAC is refused;
  - fields naming another bank are refused;
  - the sign log is on disk before the answer: a test kills the signer between the write and the
    answer and checks the slot is still taken after a restart;
  - without a sign log it refuses every vote until `-resume-above`, then refuses at or below it.
- **The engine with a remote signer:** the four-node network commits through four signers; a signer
  down for a while leaves its bank not voting and the others committing, and the bank votes again
  when it is back; a compromised engine (a test that drives it to ask for two different prevotes in
  one slot) gets one signature and a refusal.
- **The name service:**
  - the zone file of a known state matches a file in `testdata`;
  - rebuilding it from `hdls/accounts` gives the same bytes;
  - `named-checkzone`, where installed, accepts it (skipped otherwise);
  - a name with two IP addresses and a frozen name come out as §3.2 says;
  - the `_ip` lookup lists every claimant;
  - TLS keys: an update sets, replaces and clears them, and a third is refused;
  - the wallet refuses a bank's web server whose key is not on chain (if Q10 says now).
- **The joiner:**
  - a checkpoint signed by more than ⅓ but at most ⅔ of the trusted frame is refused;
  - a node away while ⅔ of the set changed in three steps walks the frames and catches up;
  - a frame whose `NextValidatorsHash` does not match is refused;
  - one peer withholding the newest checkpoint does not keep the node behind;
  - two valid commits for different blocks at one height stop the node with the fork alarm;
  - a seated joiner becomes confirmed at its first commit with its own precommit;
  - a seeded joiner whose seed was altered never becomes confirmed and stops with the alarm.

**2026-11-03 (as built):**
- **The crash between the sign log and the answer** is a key that panics as it is about to sign,
  after the log is written (`4b017e1`).
- **`named-checkzone`**: its test is there, and skips without the checker, which the build
  machine lacked; it later took the zone from BIND 9.20's image (§3.2's note).
- **The engines that sign through signers** are tested in `cmd/signer` (`a094762`), on test servers
  a test can take down or slow down. A bank's wallet is tested against the real `cmd/signer`, built
  and run by the test (`b387452`).
- **The altered seed** is signed again by keys of the seeding bank's own, which the set it carries
  gives the validators' names (`bd3fb41`). Signed with their real keys, it would be a double
  signature of each, which the others would freeze.
- **Not tested:** that the remote signer's transport uses no proxy (`15ba31e`).
- Each review fix added tests of its own, which its commit names.

## 7. The slice plan

Each slice starts from the branch after the ones it depends on. In the root module and in `ui/`,
`go build ./...`, `go vet ./...` and `go test -race ./...` must pass. Slices in one group never share
a file, test files included.

| Group | Slices | Starts after |
|---|---|---|
| 1 | L1 · K1 | `phase4` |
| 2 | S1 · N1 | L1, K1 |
| 3 | S2 · J1 | S1 |
| 4 | S3 · N2 | S2, N1 |
| 5 | D1 | everything |

- **L1. The new genesis** (§3.3, §4.3): `TLSKeys` on the account, the update, the genesis and the
  digest; `NextValidatorsHash` on the header, in `ProposeBlock`, `ApplyBlock`, the block hash and
  the record entry.
- **K1. The key file** (§2.8): the encrypted key file moves to the root module; `cmd/keygen
  -encrypt` and `-secret`.
- **S1. The signer** (§2.2 to §2.6): `cmd/signer`, its rules, its two logs.
- **N1. The zone and the lookups** (§3.2, §3.4): the zone file, `hdls/resolve/ip/`, the fetcher's
  routes and its once-a-minute line.
- **S2. The engine through the signer** (§2.7): the `Signer` interface, the remote signer, the
  engine's config, the allow list refusal line.
- **J1. The joiner** (§4): ⅔ of the trusted frame, the frames file, the walk, asking every validator,
  the fork alarm and the confirmation.
- **S3. The wallet through the signer, and the deployment** (§2.7, §5): the wallet's flags,
  `deploy/` and `cmd/quickstart`.
- **N2. The wallet's TLS pin** (§3.3), if §9 Q10 says now.
- **D1. Docs** (§8), then an independent review, its fixes and a recheck, and the check list.

**2026-11-03 (as built).** The plan ran in five rounds, set so that each round's slices shared no
file:
- **Round 1:** L1 and K1, as the table says.
- **Round 2:** S1, N1 and J1. J1 needs only L1, so it started beside S1 and N1, a group early. N2
  started once S1 was merged, since it touches the wallet only, and was merged before J1.
- **Round 3:** S2. The reviews of the joiner and of the name service ran beside it, on `bd3fb41`.
- **Round 4:** S3, beside the fixes of those two reviews, which were merged first, with the spec's
  desk correction (`f3ace7d`).
- **Round 5:** the signer review, on `a1dffc7`, and its fixes. Then the docs, with two writers side
  by side, and a recheck of the whole build.

So the reviews ran during the build, before the docs, where the table puts them after. Two items
moved: the allow list's refusal line went from S2 to N1, since it is attribution, and the fetcher's
`GET /hdls/frames` route was built in N1, though J1 writes the file. Each slice was built on a
branch of its own (`p4-…`) and merged into `phase4` by cherry-pick, in the order of
`review/phase4_progress.md`.

## 8. Docs to change

- `docs/signature.md`: the signer, its clients, its rules and its logs; what a break-in on each
  machine gets.
- `docs/operating-a-network.md`: running a signer; `-resume-above`; rotating a client secret;
  publishing the zone; what to do with a flooder's name; seeding a joiner and checking the seed
  with a second bank; the fork alarm and the divergence alarm.
- `docs/governance.md`: TLS key records in the table of who changes what; the zone in "names as a
  name service".
- `docs/consensus.md`: ⅔ of the trusted frame, the walk, the confirmation.
- `docs/api.md`: the new public files and routes; the signer's one route.
- `findings_20261027.md` and `TODO.md`: the joiner's limit narrowed to the first frame; the signer
  and the name service done.
- `ui/USER_MANUAL.md` and `ui/README.md`: a bank's wallet through a signer; the TLS pin.
- `docs/reading-the-code.md`, `docs/glossary.md` (signer, client, slot, frame, zone file).

**2026-11-03 (as built).** The docs are written last, on `c1da31a`, by two writers, each owning its
files. Beside this list they take `docs/security.md`, `docs/architecture.md`, `docs/README.md` and
`README.md`. They also take what the reviews left for the docs, which the notes above mark as the
runbook's or the docs'. This spec's notes and `review/phase4_progress.md` are a third writer's.

## 9. Open questions for the owner

Every row is decided: on 2026-11-02 the owner took every default ("defaults, go ahead").

| # | Question | Decided (the builder's default) |
|---|---|---|
| Q1 | The link to the signer: HMAC per client over HTTP on the private network (§2.2), or mutual TLS? And does the signer listen, or dial out to the engine so it has no open port? | **HMAC, the signer listens**: least code, safe against a quantum computer, and the desk and the admin page can reach it too |
| Q2 | The clients and their domains (§2.3): `engine`, `desk`, `admin` as in the table? | **as in the table** |
| Q3 | Receipts: through the signer (one call per payment taken in, up to the fetcher's cap), or a separate low-value key the engine keeps for receipts only? | **through the signer**: one key per bank, as decided |
| Q4 | The signer's key encrypted at rest with the wallet's format, the passphrase from a file (§2.8)? | **yes**; plain only for tests |
| Q5 | A signer that lost its sign log refuses votes until `-resume-above <height>` (§2.6)? | **yes** |
| Q6 | Sign on the engine's event loop with a 1 s deadline and fail-fast (§2.7), or move signing off the loop? | **on the loop**: today's code path, a small change |
| Q7 | The DNS part: a zone file that standard DNS servers load (§3.2), or a DNS server written in Go by hand? | **the zone file** |
| Q8 | The zone's content (§3.2): A/AAAA, TXT public address, `_validators`, the `_ip` lookup, TLSA; frozen names left out but for the lookup? | **as written** |
| Q9 | IP addresses stay claims: no check of control, every claimant listed (§3.4); or one name per address on chain? | **claims, every claimant listed** |
| Q10 | TLS key records (§3.3): up to 2 per name, a new genesis. Does the wallet pin its bank's web server by them now (slice N2), or later? | **records now, the wallet's pin now** (small, and the first user of the records) |
| Q11 | "Make the flooding a human resolvable issue": attribution by name (R2), or choosing gossip relays by name? And may the fetcher read the client's address from the CDN's header? | **attribution (R2); the header only from the configured CDN addresses** |
| Q12 | More than ⅔ of the trusted frame for every checkpoint, catch-up included (§4.2)? | **yes** (D2) |
| Q13 | `NextValidatorsHash` in the block header, so each frame signs off the next (§4.3)? A new genesis, with Q10's | **yes** |
| Q14 | A joiner asks every validator of its trusted frame, and two valid commits for different blocks at one height stop it (§4.4)? | **yes** |
| Q15 | A seated node whose state the network's commit says is wrong: stop signing and wait for the operator (§4.5), or adopt the network's checkpoint and carry on? | **stop and wait**: it is a fork or a broken node, and people should look |
