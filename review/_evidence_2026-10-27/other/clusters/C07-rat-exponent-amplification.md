# C07-rat-exponent-amplification

Round-1 reports merged into this cluster (3):

## [ledger-transition] RAT-DECODE-1 — Exponent-form big.Rat amounts: an 11-byte JSON field costs ~1.5 MB of heap and seconds of CPU before any size check
- severity/confidence (round 1): high/high
- category: dos
- locations: ledger/ledger.go:37, ledger/payment.go:43-47, ledger/payment.go:75-80, ledger/block.go:17-23, ledger/block.go:180-198, node/httptransport.go:129-136, node/httptransport.go:150-164, node/httptransport.go:220-240, node/httptransport.go:302-314, node/node.go:432-440, node/sync.go:108-119
- attacker: anyone reaching the port
- known_status: Related to deferred #10 (unauthenticated API / rate limiting). New angle: a single small request amplifies 10^4-10^5x in memory and CPU, which per-client rate limiting does not bound.

**Description.** Rat.SetString, which JSON decoding uses for every ledger amount, accepts exponent notation, e.g. "1p9999999" (2^9999999, 11 bytes). Decoding allocates the full number. The size cap is measured only by re-serializing (WithinMessageBound → json.Marshal → decimal conversion), and VerifyPayment/PaymentID RatString the amount first. So the cost is paid before any bound rejects the message:
- at JSON decode on every endpoint (proposals are decoded before proposer authentication, and decode() does not require a JSON Content-Type);
- on handler goroutines (signature check);
- on the single event loop (admitPayment).
The same applies to account amounts in a peer-served checkpoint (no body limit at all) and to governance Rats.

**Failure scenario.** Measured: decoding 40 fields of {"Amount":"1p9999999"} (921 bytes) adds 59 MB of live heap, 1529 KB per field. A 1 MiB body holds about 45k such fields, roughly 66 GB extrapolated (not run on the shared machine). Any POST to /hdls/proposal, /hdls/pay or /hdls/payment is decoded this way before authentication. A validly signed payment carrying that amount costs 1.42 s in VerifyPayment and 1.55 s in WithinMessageBound per submission before it is refused.

**PoC.** <scratch>/ledger-transition/ledger/poc_rat_decode_amplification_test.go: TestPoCRatDecodeAmplification FAILs (1529 KB of heap per 22-byte field; VerifyPayment 1.42 s, WithinMessageBound 1.55 s).

**Suggested fix.** Give amounts a custom JSON type that accepts only plain "a/b" or decimal strings with a digit cap and no exponent, before allocating. Add a cheap Num().BitLen()+Denom().BitLen() precheck in WithinMessageBound and before any RatString (VerifyPayment, PaymentID, intake). Bound the checkpoint response body, and require Content-Type: application/json.

## [governance] GOV-7 — Unauthenticated governance vote stalls the single event loop ~9s via big.Rat exponent amplification
- severity/confidence (round 1): medium/high
- category: dos
- locations: ledger/governance.go:31, ledger/reassignment.go:265, node/governance.go:64, node/reassignment.go:302, node/node.go:289
- attacker: anyone who can reach the node's HTTP port (unauthenticated)
- known_status: Strengthens deferred #10 and the recorded MEDIUM 'unauthenticated write API + single-loop serialization'. New angle: specific amplification (RatString on attacker rationals, pre-verification, repeated per carried vote and per height) makes it a ~500-byte PERSISTENT stall, materially worse than the 1MiB one-shot recorded.

**Description.** Governance amounts travel as JSON strings parsed into *big.Rat. big.Rat.SetString accepts exponent notation, so the ~12-byte string '1p10000000' decodes (~2ms) to a 10-Mbit integer whose RatString() costs ~1.8s. parameterVoteFields/reassignmentVoteFields (governance.go:31-35, reassignment.go:265-273) call RatString to build the preimage, and this happens BEFORE signature verification: VerifyParameterVote/VerifyReassignmentVote receive the preimage as an already-evaluated argument (node/governance.go:64, reassignment.go:302), and addParameterVotes only dedups POOLED voters — so one message can repeat a (public) validator key thousands of times, each triggering a RatString, before any bad signature is rejected. All of this runs on the single event-loop goroutine (node.go:289-296). The pooled entry (GOV-3) then re-stringifies on EVERY startHeight via pendingParameterChange→Valid. Unlike the recorded 1MiB/ApplyBlock DoS, a ~500-byte packet suffices and the damage persists.

**Failure scenario.** Attacker (no key) reads GET /hdls/governance for the current digest/sequence, then POSTs a 521-byte ParameterChange whose NewParameters.TransactionCost is '1p10000000' and whose Votes repeat the public validator key 3x with empty signatures. PoC: the event loop stalls ~9s (~2.25s per carried vote); a 1MiB body holds ~14,000 such votes → ~8.8 hours of single-loop stall from one request. The entry then pools (binds current sequence/digest, never pruned) and every height re-processes it: observed block rate collapses from 64 to 2 per 2s and stays there. Repeatable/stackable → sustained throughput denial of the whole node from tiny unauthenticated packets.

**PoC.** <scratch>/governance/node/zz_govreview_poc_test.go (TestPoCGovHugeRationalStallsEventLoop) — FAILs: 9.02s loop stall, block rate 64→2 per 2s

**Suggested fix.** Verify the signature before building the RatString preimage (or bound the serialized size / numerator+denominator bit-length of a submitted amount at intake, and reject exponent notation); dedup a message's carried voters before verifying; and apply GOV-3's don't-pool-until-verified fix.

## [node-input] NODE-IN-1 — Tiny exponent-form big.Rat amounts cause superlinear per-request work on the single event loop (1 MiB body bound does not bound the work)
- severity/confidence (round 1): medium/high
- category: dos
- locations: node/node.go:439, node/node.go:450, ledger/block.go:17-23, ledger/payment.go:18, ledger/payment.go:46, node/httptransport.go:308, node/governance.go:18-25, node/reassignment.go:15-27
- attacker: anyone reaching the port
- known_status: Extends deferred #10 (unauthenticated-API single-loop DoS) and GLM C6 (big.Rat JSON footgun, rated 'non-exploitable today'). New angle: the amplification is ~60 bytes -> ~150ms of serialized loop work, replayable, and it defeats the specific in-process guard #10 named as residual (the 1 MiB body bound) and the governed MaxMessageBytes cap. Materially worse and code-fixable, not just 'rate-limit the port'.

**Description.** Payment.Amount is a *big.Rat decoded from attacker JSON. big.Rat.UnmarshalText accepts exponent form, so the ~10-byte literal "1e1000000" (body ~60 bytes, far under the 1 MiB MaxBytesReader at httptransport.go:308) expands to a 3.3M-bit rational. admitPayment runs ON THE EVENT LOOP and pays superlinear big.Rat cost regardless of config: WithinMessageBound→json.Marshal (block.go:21) when MaxMessageBytes>0, and mem.contains→PaymentID→RatString (payment.go:46) when it is 0. Measured ~135ms (bound=2000) / ~91ms (bound=0) vs ~70us normal — a >1000x amplification of serialized loop time from a cheap, replayable, self-signed request needing no balance or validator slot. Same vector via ParameterChange.NewParameters.TransactionCost (proposalKey→Digest→RatString, governance.go:18) and Reassignment amounts (reassignmentKey, reassignment.go:15), both before any auth.

**Failure scenario.** Attacker signs one Payment{PublicKeyFrom: own key, Amount: "1e1000000", BlockID: huge} and POSTs it to /hdls/pay (or /hdls/payment) repeatedly. VerifyPayment passes (own key). Each request enqueues onto the loop; admitPayment spends ~100-150ms marshalling/RatStringing the expanded rational before refusing it. A few requests/second saturate the single consensus goroutine, stalling block production network-wide for the duration of the flood. security.md's "Bounded request bodies ... avoiding the unbounded-JSON DoS" guarantee does not hold.

**PoC.** <scratch>/node-input/node/zz_poc_test.go (TestPoCHugeAmountStallsEventLoop) — PASS, logs normal=~70us vs huge=~135ms (bound=2000) and ~91ms (bound=0)

**Suggested fix.** At intake, bound the decoded amount's magnitude cheaply before any RatString/Marshal — e.g. reject a Payment/TransactionCost/Reassignment amount whose Num().BitLen()+Denom().BitLen() exceeds a small cap (a few thousand bits covers any real balance). Do it in admitPayment, addParameterVotes, addReassignmentVotes and VerifyPayment's callers, and correct security.md's 'bounded bodies' claim to 'bounded decoded magnitude'.
