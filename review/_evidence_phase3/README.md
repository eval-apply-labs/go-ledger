# Evidence for the Phase 3 review (2026-11-01)

The reports of the independent review of the Phase 3 build, and of its recheck. Three reviewers
read the build on `b9defd2`, each one area. Each then rechecked the fixes of its area on `b721caa`,
and, once the last round of fixes was in, read those on `ff4d853`. Each finding has an id, and the
fix commits on `phase3` name it. What became of each finding is in
[`../phase3_progress.md`](../phase3_progress.md#the-independent-review-2026-11-01).

The reviews, on `b9defd2`:

- [`review1_network.md`](review1_network.md): the network surfaces: the two ports, the allow list,
  the transport, the fetcher, the config and the containers. Findings N1 to N13: 1 major, 7 minor,
  5 nits.
- [`review2_bundles.md`](review2_bundles.md): bundles, the public folder, the slow-down switch and
  the second path. Findings B1 to B8: 1 major, 4 minor, 3 nits.
- [`review3_wallet.md`](review3_wallet.md): the wallet and the new ledger fields. Findings W1 to
  W10: 1 major, 4 minor, 5 nits.

The rechecks, on `b721caa`, each with its final recheck on `ff4d853` at its end:

- [`recheck1_network.md`](recheck1_network.md): N1 and N7 held only in part; new findings N14 and
  N15. The final recheck: all fixed, nothing new.
- [`recheck2_bundles.md`](recheck2_bundles.md): B4 held only for votes; new findings R2-1 to
  R2-4. The final recheck: all fixed, nothing new, two optional nits.
- [`recheck3_wallet.md`](recheck3_wallet.md): W2 (c) stays by design; new findings R3-1 to R3-3.
  The final recheck: all fixed, nothing new.

No review or recheck found a blocker. The reports are kept as the reviewers wrote them, with two
changes:

- Each reviewer wrote its scratch tests in a copy of the tree of its own. The reports name each
  such file by its name alone. None of them was committed: the fixes added tests of their own,
  often from the reviewer's, which their commits name.
- The address of the machine the network review ran on is replaced by `<the host's LAN address>`.

The file and line numbers in each report are those of the commit it read. Many moved with the
fixes.
