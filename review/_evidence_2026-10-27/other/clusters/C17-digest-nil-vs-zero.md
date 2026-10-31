# C17-digest-nil-vs-zero

Round-1 reports merged into this cluster (1):

## [ledger-transition] DIGEST-1 — State digest encodes nil and zero amounts identically; a checkpoint with null amounts verifies and then panics the state transition
- severity/confidence (round 1): medium/high
- category: dos
- locations: ledger/digest.go:29-32, ledger/reassignment.go:239-249, ledger/ledger.go:166, ledger/ledger.go:202, node/node.go:388-401, node/node.go:341-352, node/node.go:238-247, node/persist.go:160-186
- attacker: a gossip or bootstrap peer serving /hdls/checkpoint (catch-up sources default to gossip peers)
- known_status: new

**Description.** StateDigest writes "0" for a nil Amount, the same bytes as a real zero, so two different account sets share one committed digest. Zero-balance accounts are reachable in committed state: a ⅔ reassignment to 0 keeps the account, and genesis may contain "0". A peer serving a checkpoint can replace such an amount with null. verifyCheckpoint checks only the commit and the digest, so the node adopts it. The next block that credits or debits that account dereferences nil in ProcessPayments and panics. The run loop has no recover, so the process exits, and the snapshot it already wrote holds the null, so restarts fail the same way.

**Failure scenario.** PoC: a ratified reassignment sets 'coop' to 0. The committed state and a copy with coop's Amount=nil have equal LedgerState digests, and node-level verifyCheckpoint accepts the null-amount checkpoint. Applying the next honest block (a payment to 'coop') on the adopted state panics with a nil pointer dereference.

**PoC.** <scratch>/ledger-transition/ledger/poc_digest_nil_amount_test.go: TestPoCDigestConflatesNilAndZeroAmount FAILs (same digest, then a panic). <scratch>/ledger-transition/node/poc_checkpoint_nil_amount_test.go: TestPoCCheckpointWithNilAmountVerifies FAILs.

**Suggested fix.** Make the digest injective (encode nil distinctly, or reject nil amounts). Validate adopted checkpoints and snapshots before fastForward: non-nil and non-negative amounts, canonical and unique keys, unique names. Treat nil as zero defensively in ProcessPayments, and consider a recover-and-halt in the run loop.
