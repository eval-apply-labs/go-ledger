# Recheck of W1–W10 at b721caa (reviewer 3)

All nine code findings fixed (W2 (c), the alarm after a restart against a node that is behind, remains by design:
the kept state is in memory only; the new tests assert it). The kept-state rule held against every hostile-node
attempt; 3 new nits. `go test -C ui -race ./...` ok (63 s); vet clean in both modules; -race ok for ./ledger,
./cmd/demo, ./cmd/fetcher. Scratch tests adapted, not committed (review3_scratch_test.go,
review3_fetcher_scratch_test.go); TestReview3CachedCheckpointMakesAPaymentStale still fails on purpose (models a
5-block-old copy, which the 1 s cache no longer allows).

- W1 fixed: rerun of TestReview3FileMasksTheNodesRefusal: the page shows the banks-changed refusal, Send and
  UpdateAccount refused, only the freeze posted, naming block 104 from the node's height in progress 101.
- W2 partly fixed: (a) stale blocks fixed in practice (1 s cache; a newer kept state stands in for an older copy);
  (b) the false alarm fixed (TestReview3CDNCopiesOfDifferentAgesRaiseTheRollbackAlarm reads nil); (c) not fixed,
  intended (TestReview3RestartLosesTheKeptState).
- W3 fixed: 303 to /send; two reloads show block 13; counter stays 13.
- W4 fixed: a 3,500-account file (10.3 MiB encoded) taken; over 3 × 64 MiB answered "too large".
- W6, W7, W8, W9, W10 fixed (W8: kept state set under the same lock as the height check, ui/trust.go:248).
- Kept-state rule (ui/trust.go:159-162) attacks: can't hide a refusal (malformed or over-long answers count as a
  reached node; 503 or a redirect hides nothing); can't roll back (taken only at or above the newest height); the
  freeze names max(kept+1, reported height): a lying report can push it ahead, no worse than main.

New:
- R3-1 nit — ui/wallet.go Send, UpdateAccount: while a file stands in because the node's checkpoint can't be read,
  "Sign and send" still signs for the file's block, uses up a counter block and posts to that node (the page says to
  save as text). Evidence TestReview3UnreadableNodeStillGetsPayments (503 on GET, 202 on POST: 1 post, counter 14).
  Fix: refuse in Send/UpdateAccount when keptFrom == "file": "your bank's node can't be read: save the payment as
  text".
- R3-2 nit — ui/trust.go LoadCarriedCheckpoint: a pasted text over 64 MiB (up to 192 MiB) is still taken; the
  too-large message says at most 64 MiB. Fix: refuse len(text) > node.MaxCheckpointBytes. Read, not run.
- R3-3 nit — cmd/demo/main.go:197: with -listen 0.0.0.0:8080 the founder's IP becomes 127.0.0.1 but its public
  address stays http://0.0.0.0:8080 (works on Linux and macOS). Fix: build the public address from the published IP.

## Final recheck at ff4d853 (R3-1..R3-3)

All three fixed, no new findings. go test -C ui -race ok (64 s); vet clean; -race ./cmd/demo ok.
- R3-1 fixed (620fade): TestReview3UnreadableNodeStillGetsPayments now refused, nothing posted, counter unmoved;
  save-as-text and freeze still work. A current node (block 12) with a file at 10: send goes through. A node behind
  the file (9): refused by design, clears when the node reaches the file's height. Reasoned only: a file loaded while
  the node works, with a CDN copy 1 s behind, would refuse for about a second.
- R3-2 fixed (09e4309): text one byte past 64 MiB refused before decoding with the shared error; a 192 MiB form gets
  "too large"; the 3,500-account file still taken.
- R3-3 fixed (d201a2d): 0.0.0.0 and [::] publish http://127.0.0.1:8080 and [127.0.0.1]; [::1], a LAN IP unchanged;
  :8080 and localhost refused as before.
