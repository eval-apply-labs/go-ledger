# C30-prevstatedigest-pin

Round-1 reports merged into this cluster (1):

## [ledger-transition] PIN-1 — PrevStateDigest tier compares the account-only digest, but the docs and the API point clients at the full committed digest
- severity/confidence (round 1): low/high
- category: docs-drift
- locations: ledger/ledger.go:40-48, ledger/block.go:98-100, ledger/block.go:110-112, ledger/block.go:183-191, docs/operating-a-network.md:224-226, node/httptransport.go:270-294, node/node.go:432-461
- attacker: no attacker (bug)
- known_status: new

**Description.** The Payment field comment says a pinned payment is included only in a block whose PrevStateDigest matches, and the operator guide says 'prior state'. Block.PrevStateDigest is the full LedgerState digest (accounts + parameters + sequence), and it is what /hdls/resolve reports as stateDigest. The ledger, however, compares the pin against StateDigest(accounts) only. A client following the docs builds a payment that can never be included. admitPayment doesn't check the field, so the client still gets a receipt. Pinning only the account digest also leaves parameter and governance history unpinned.

**Failure scenario.** PoC: a payment pinned to hex(prev.Digest()), which equals the block's PrevStateDigest, is excluded by ProposeBlock.

**PoC.** <scratch>/ledger-transition/ledger/poc_noop_and_pin_test.go: TestPoCPrevStateDigestPinPerDocsNeverIncludable FAILs.

**Suggested fix.** In both paths, compare the pin against previousState.Digest() (the block's PrevStateDigest). Alternatively, expose the account digest and correct the docs. Check the pin at intake.
