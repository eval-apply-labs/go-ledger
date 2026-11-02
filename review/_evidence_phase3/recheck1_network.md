# Recheck of N1–N13 at b721caa (reviewer 1)

Nine of eleven code findings fixed and holding; N1 and N7 partly fixed, each with a new bypass shown on the fixed
code; the 1 s dial timeout brings one new minor (N15). Both suites pass on b721caa (node 135 s). Scratch tests,
not committed. Images, containers, networks, volumes and the CNI file the reviewer made are removed.

- N1 partly: the original reproduction (bank-a at 172.28.70.11 under rootless Podman, gossip port published at
  <the host's LAN address>:18170) is now refused (curl exit 52). Doesn't cover a translated address a validator publishes: N14.
- N2 fixed: ETag of size and nanoseconds, no Last-Modified; a grown unit revalidates 200, an unchanged one 304
  (TestReviewRecheckETag). Same-tick mtimes on 5.15 matter only for two same-size rewrites within milliseconds.
- N3 fixed: a slow peer gets one copy (was two); a blackholed first address delays 1 s (was 5 s). The 1 s value
  causes N15.
- N4 fixed: 100 pushes to a flapping peer write 1 line (was 99).
- N5 fixed: a missing listed file gets 404 with max-age=1; unlisted paths stay without.
- N6 fixed: listen defaults to 127.0.0.1:8080 beside gossipListen; the wildcard warning shows in the live quickstart.
- N7 partly: loopback and unspecified destinations refused after resolution, no proxy; but the node still reaches its
  own operator port through the machine's own non-loopback address
  (TestReview1PublicAddressClientReachesOwnOperatorPortByInterfaceAddress: http://<the host's LAN address>:port served
  /hdls/bundle). Open wherever banks share a well-known address (rootless Podman's 10.0.2.100, the first container on
  a Docker bridge); in a container listen must be :8080, as the quickstart sets it. Fix: see N14.
- N8 fixed: first URL that answers; public faces when no peer answers or while stuck; a dropped validator reaches
  them. Nit: catch-up still uses NewHTTPClient(5 s), so each silent URL costs 5 s (4 silent addresses add 20 s);
  the transport's dialer would fix it.
- N9 fixed for the file's containers (fetchers .21–.24; rerun: height 1, checkpoint ETag and max-age=1, a stranger at
  .25 refused). A container started outside the file can still take a stopped engine's address; it already reaches
  every operator port, so no new exposure.
- N11 fixed. N13 fixed (Podman build of COPY ui kept wallet.json, .state, .trust, sub/other.json out).

New:
- N14 minor — node/allowlist.go:39 (Accept :76): the list admits any address a validator publishes. Rootless
  Podman's default port forwarding (slirp4netns) makes every caller 10.0.2.100, a fixed address on every such host
  and the container's own interface address. A bank publishing its gossip port that way fails closed after N1; but
  one validator publishing 10.0.2.100 (private, so the ledger takes it) opens that bank to everyone, with no
  refusals logged. Docker's userland proxy with a bridge gateway such as 172.17.0.1 likewise (reasoned). Evidence:
  bank-a published at <the host's LAN address>:18172, genesis edited so bank-b lists 10.0.2.100: curl /hdls/status 200, POST
  /hdls/vote 202; control with bank-b at 172.28.70.12 refused; netstat showed local 10.0.2.100:8070, remote
  10.0.2.100:33460. Fix: in Accept refuse a connection whose remote IP equals its local IP (a forwarder in the
  engine's own namespace always connects that way; no real peer does); for N7 the same rule after the dial in
  publicAddressTransport's dialer (also covers loopback).
- N15 minor — node/httptransport.go peerDialTimeout = 1 s is exactly Linux's initial SYN retransmission timeout:
  one lost SYN/SYN-ACK fails the push (a one-URL peer loses the message); a 5 s client recovers ~1 s later.
  Evidence TestReview1SynLoss under unshare -rn with iptables dropping every other SYN: plain client "received 1
  after 1.01s", transport "received 0 after 1s", 3/3. Fix: peerDialTimeout = 2 s.

No regressions seen: route split, dual-stack and IPv4-in-IPv6, start order, config checks, fetcher paths, 404/405,
proxy and cap; suites pass; live quickstart clean.

## Final recheck at ff4d853 (N14, N7, N15, N8)

All fixed, nothing new. Root suite -race ok (node 139.9 s), ui ok (62 s); images and builder layer removed.
- N14 fixed: the rootless Podman case rerun on an image built from ff4d853 (bank-a on slirp4netns, gossip port
  published on the host, bank-b listing 10.0.2.100): curl empty reply (exit 52), POST 000 (at b721caa: 200 and 202).
  Real dual-stack sockets: different addresses pass (127.0.0.2 → 127.0.0.1); same-address flagged for IPv4, ::1 and a
  LAN address. No legitimate Q13 setup refused (127.0.0.x test, two banks behind one NAT, a bank with two lines,
  containers on one Docker host). Two limits, neither fail-open: a self-connection that binds 127.0.0.1 as its source
  while dialling the node's LAN address isn't flagged (matters only if the operator writes 127.0.0.1 in `allow`);
  Docker's userland proxy runs outside the engine's namespace and shows as the bridge gateway, so the comment's "a
  userland proxy" is accurate only for one inside that namespace (reasoned; no Docker here).
- N7 fixed: http://127.0.0.1:port and http://<own LAN address>:port both refused; dropped right after the handshake,
  before any request.
- N15 fixed: TestReview1SynLoss under unshare -rn: the push arrived after 1.02 s, 3/3 (lost at b721caa). A silent
  first line now costs 2 s per push, under the 3 s step timeout.
- N8 fixed: catch-up and startup bootstrap clients share the 2 s connect timeout (5 s overall); public-address client
  the same.
- No regression from 2f9100f (a bundle starts with its last commit, so a node one height behind still takes it before
  any stop), 4a41d68 (only verified votes stored, capped, reset per height), ff4d853 (rotation correct; one status a
  tick).
