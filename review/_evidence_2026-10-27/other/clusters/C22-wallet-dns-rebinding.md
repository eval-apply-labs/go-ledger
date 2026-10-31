# C22-wallet-dns-rebinding

Round-1 reports merged into this cluster (1):

## [wallet-ui] KEY-1 — DNS-rebinding bypasses the CSRF/origin guard — drive-by signed payments and governance votes with the local key
- severity/confidence (round 1): high/high
- category: ui-security
- locations: ui/main.go:76-86, ui/main.go:62-70, ui/main.go:48-51
- attacker: a web page the user visits (while the wallet runs)
- known_status: new — the CSRF guard (#27 / findings 'External review G1') is recorded as fixed and is tested only against classic cross-site/same-site/foreign-Origin and the curl fallback; DNS rebinding (same Origin==Host after rebind) was never in the test table and bypasses the fix.

**Description.** sameOrigin() only checks that Sec-Fetch-Site is not cross/same-site and that Origin.Host == r.Host — it never compares against a FIXED expected host (e.g. 127.0.0.1:<listen>). Under a DNS-rebinding attack the victim loads http://evil.example:8090 (attacker IP), the attacker re-points the name to 127.0.0.1, and the page fetches /send. The browser now sees page-origin and request-URL as the SAME origin, sending Origin: http://evil.example:8090, Host: evil.example:8090, Sec-Fetch-Site: same-origin — all of which the guard accepts (Origin.Host==r.Host holds by construction; same-origin is the trusted branch). The wallet, which auto-signs with the local key and listens on localhost, then signs and submits whatever the page asks. The deliberate no-header 'curl' fallback (returns true) is a second, independent bypass for any non-browser client that can reach the port. The prior CSRF HIGH (#27) is recorded fixed; this is a concrete bypass of that fix still present in current code.

**Failure scenario.** User runs the wallet (and, for governance, is a validator) and, while it runs, visits an attacker page. The page rebinds its hostname to 127.0.0.1:8090, reads GET / same-origin (balance, public key), then POSTs /send (pay the attacker) and /admin/reassignment (mint to the attacker via a ⅔ vote). PoC drives the real handlers through the real guard with Origin/Host = the rebound name and Sec-Fetch-Site: same-origin; the node receives a VALID wallet-signed payment and a VALID reassignment vote.

**PoC.** <scratch>/wallet-ui/ui/poc_rebind_test.go (TestPoCDNSRebindingBypassesGuard) — FAILS: logs 'BYPASS: node received a VALID payment signed by the wallet key ... amount=999' and a VALID reassignment vote 'set ... to 1000000', plus rebound GET / serving the wallet page+key.

**Suggested fix.** Bind the guard to a fixed expected authority: reject any request whose Host header is not the configured -listen host:port (or an explicit allowlist), independently of Origin, and additionally require Origin/Referer to equal that same fixed value when present. Keep the server on 127.0.0.1. Consider dropping the blanket no-header allow in favor of a localhost-only + fixed-Host check; if a no-browser escape hatch is kept for curl, gate it behind a loopback-peer check. Set X-Frame-Options/CSP frame-ancestors 'none' as defense-in-depth.
