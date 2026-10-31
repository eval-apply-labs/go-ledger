# C20-checkpoint-fetch-unbounded

Round-1 reports merged into this cluster (1):

## [sync-trust] SYNC-2 — Catch-up checkpoint fetch has no response-size bound (memory DoS)
- severity/confidence (round 1): medium/high
- category: dos
- locations: node/sync.go:108-119, node/sync.go:50-52, node/httptransport.go:308
- attacker: one validator (via a published on-chain address) or any configured bootstrap/sync peer
- known_status: new — distinct from deferred #10 (open mutating endpoints + event-loop DoS, guarded by the 1 MiB intake bound and server timeouts): this is the CLIENT-side checkpoint read, a different code path not covered by fronting the server with a TLS/rate-limit proxy.

**Description.** fetchCheckpoint reads a peer's /hdls/checkpoint reply with a bare json.NewDecoder(resp.Body).Decode(&cp) and no size limit (sync.go:115), while the node's own server intake caps every request body at 1 MiB via http.MaxBytesReader (httptransport.go:308). catchUpLoop polls catch-up sources every few seconds (sync.go:38-55) and bestCheckpoint fetches from each. A malicious source can stream an arbitrarily large (or slow) body within the 5s client timeout, forcing the polling node to allocate it into memory, repeatedly. For a node with no bootstrap configured, catch-up sources are its gossip peers = the on-chain validator addresses (peerdns.go), so a single validator that publishes its Address becomes a source — one dishonest validator can memory-pressure its peers, which the BFT model should tolerate.

**Failure scenario.** A validator (dynamic mode) publishes an on-chain Address pointing at an attacker-controlled HTTP server (or a bootstrap operator turns malicious). Victim nodes without bootstrap peers resolve that address as a catch-up source and poll /hdls/checkpoint every few seconds. The server answers each poll with a multi-hundred-MB JSON body (streamed fast for <5s). Each victim decodes the whole thing into a Checkpoint, spiking RSS on the single event-loop machine on every tick; sustained, it drives nodes to OOM or GC thrash while honest traffic starves. No signature or verification runs until AFTER the full body is decoded, so the cost is paid before any check.

**PoC.** <scratch>/sync-trust/node/synctrust_poc_test.go — TestPoCUnboundedCheckpointFetch: PASS. An httptest server returns a 4 MiB checkpoint (4x the 1 MiB server bound); fetchCheckpoint decodes it with no error and the full oversized field survives, proving no cap.

**Suggested fix.** Wrap the response body in an io.LimitReader (or set resp.Body via a MaxBytesReader-equivalent) with a generous but bounded cap sized for the largest legitimate account set, mirroring the 1 MiB server intake; reject and move on if exceeded. Consider a configurable max-checkpoint-bytes and bounding bestCheckpoint's total work per tick.
