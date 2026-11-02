# Phase 3 review 1: the network surfaces (on b9defd2)

Summary: 0 blocker, 1 major, 7 minor, 5 nits. Root suite passes. Scratch tests, not committed:
fetcher_review_test.go for cmd/fetcher/, and review1_*_test.go for node/.

N1 major — node/allowlist.go:23 gossipAllowList, spec §5.1 "the node's own are included" and §1 "TLS in front".
The list trusts the socket's remote address. Anything in front that rewrites it (rootless Podman port forwarding,
a TLS/TCP front, a userland proxy) makes every caller one address: if that address is not listed, every
validator is refused; if it is listed, everyone gets in. Easy because the node's own addresses are on its list,
and a validator may publish loopback/gateway addresses (the ledger takes them), which go on every bank's list.
Evidence (rootless Podman 3.4): bank-a's engine at 172.28.70.11, gossip port published at <the host's LAN address>:18170;
from the host `curl …/hdls/status` 200 and `POST …/hdls/vote` 202; a busybox probe shows the container sees the
peer as its own address (::ffff:172.28.70.21). TLS-front and validator-listing cases reasoned. Fix: leave the
node's own name out of gossipAllowList (fail closed; the refusal log shows it); docs: the gossip port must reach
the engine with the caller's address intact (rootful DNAT, host networking, or Podman's slirp4netns port handler);
a TLS front can't be combined with the allow list. Optionally keep loopback addresses from the chain off the
list (tests put their 127.0.0.x in `allow`).

N2 minor (major if BlockIntervalMs < 1 s) — cmd/fetcher/main.go:242: only Last-Modified (1 s resolution) as
validator: a rewrite within the same second revalidates 304; a just-completed record unit's 304 carries
immutable for a year without its last entry; If-Range by date can mix versions. Evidence
TestReviewRecordUnitRevalidates304ImmutableWhileShort, TestReviewStatusRevalidates304AfterSameSecondRewrite.
Fix: ETag "%x-%x" of size and ModTime().UnixNano(), pass time.Time{} as modtime to ServeContent (tried: passes).

N3 minor — node/httptransport.go:111-121 post falls back on any error, not only "fails to connect": a slow peer
gets up to 4 copies; a blackholed first line delays every message 5 s (> 3 s step timeout), unlogged. Evidence
TestReview1FallbackResendsToASlowPeer, TestReview1FallbackWaitsOutABlackholedFirstAddress. Fix: short dialer
timeout (~1 s); fall through only on dial errors (errors.As *net.OpError with Op == "dial"); optionally try the
last URL that worked first.

N4 minor — node/httptransport.go:115-124 failing-peer log writes on every state change: a flapping peer gets one
line per message (Q10 rules it out); a malicious validator or the path can flood logs. Evidence
TestReview1FlappingPeerLogsOncePerMessage (100 pushes, 99 lines). Fix: at most one change line a minute per peer.

N5 minor — cmd/fetcher/main.go:226: a 404 for a listed path has no Cache-Control, so CDNs apply their default
(Cloudflare 3 min under cache-everything; CloudFront ≥10 s), hiding files that appear later (a new name, the
first lifelong file, the next unit, everything after a fresh start). Evidence: live quickstart 404s without
Cache-Control. Fix: Cache-Control max-age=1 (or no-store) on a listed path's 404.

N6 minor — cmd/server/config.go:161, cmd/server/main.go:49: with gossipListen set, listen still defaults to
:8080 on every interface, serving every route (gossip, bundle import/export up to 64 MiB built on the loop,
checkpoint import) to anyone, while the start log says gossip goes only to validators. Reasoned. Fix: when
gossipListen is set and listen is not, default listen to 127.0.0.1:8080, or log that listen must not be reachable.

N7 minor — node/sync.go:160, :251 the pull GETs <PublicAddress>/hdls/bundle; a malicious validator publishes
http://127.0.0.1:8080, so while stuck every honest engine pulls its own operator port's full export (up to
64 MiB, built on its loop) and re-verifies its own pool. Otherwise contained (fixed path, no redirect, bounded).
Reasoned. Fix: the pull's dialer Control refuses loopback (private addresses keep working for the quickstart).

N8 minor — node/sync.go:37-45 catchUpCheckpoint: with no bootstrap answering, catch-up falls back only to peers'
gossip ports, which refuse a node not (or no longer) on their lists (a validator dropped while offline, or whose
source address changed): it never recovers without bootstrap, which the quickstart doesn't write, though every
validator's public address is on chain. Also asks every URL of every peer in turn (a peer ahead fetched once per
address; 4 silent addresses add 20 s a round). Reasoned. Fix: when bootstrap yields nothing, also try the current
validators' PublicAddress (the list pullPublicBundles builds); per peer stop at the first URL that answers.

N9 nit — deploy/compose.yaml:96-98: no dynamic range, so a stopped engine's fixed address goes to the next
container started on the network, and the engines let it in. Evidence: busybox got .13/.14 with engines stopped
and was let into bank-a's gossip port. Fix: fixed addresses for the fetchers too, or a second network.

N10 nit — record paths carry no network: after a new genesis at the same public address the CDN keeps the old
network's complete units immutable for a year. Fix: genesis-hash prefix in the public record path, or the
runbook says to purge the CDN after a new genesis.

N11 nit — cmd/server: gossipListen's port is never compared with the genesis gossipPort; if they differ nobody
reaches the node and nothing is logged. Fix: log a line when they differ (don't refuse: port mapping).

N12 nit — cmd/quickstart/main.go:58: on-chain public addresses are container names; the host wallet the
quickstart prints can't reach them, so its other-banks check always reports "could only check banks holding 25%".
Reasoned.

N13 nit — deploy/ui.Dockerfile:22: COPY ui ui copies the whole folder; a developer who ran the wallet from ui/
has wallet.json there, which lands in the builder stage (kept as a dangling image by Podman); .dockerignore
doesn't exclude it.

Checked and sound: dual-stack listener matches IPv4 callers by their IPv4 entry, ::1 refused, refused caller gets
EOF; HTTP/2 prior-knowledge preface gets 400; gossip port 404s /hdls/accounts, POST bundle, POST checkpoint; a
connection whose address left the list is closed at the next read; refusal report one line a minute; config:
allow without gossipListen refused, IPv6-dress/zone/bits-past-prefix entries refused, slow-down range checked;
start order binds both before Open, closes both on failure, wraps the gossip listener before serving;
CanonicalIPAddress and update list rules; count-prefixed, length-prefixed encodings; gossipPort in the genesis
hash, range-checked; IPv6 in brackets; fetcher paths (.., %2e, %2F in a segment, NUL, CONNECT → 404/405), HEAD,
no directory or .incoming, 1 MiB cap, X-Forwarded dropped, Host rewritten, no-store, 429 cap; pull bounds (no
redirect, 5 s, 8 MiB, one round at a time); live quickstart on rootless Podman (CNI conflist set to 0.4.0 by hand,
a host issue): height 1 committed, fetchers serve status/checkpoint/unit/bundle with expected headers, read the
0600 record (same user), stranger refused at the gossip port, operator ports reachable from every container on
the shared network (documented), scratch images as 65532, configs 0644 in 0700, wallet files 0600, .dockerignore
keeps the keys out.
