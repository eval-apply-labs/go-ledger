# Phase 3 review 3: the wallet and the new ledger fields (on b9defd2)

Summary: 0 blocker, 1 major, 4 minor, 5 nits. A carried checkpoint can't be forged, can't move the pin outside
the ⅔ rule, can't be confused with another network's; save-as-text never signs two payments for one block;
the ledger encodings are injective. Scratch tests, not committed: review3_scratch_test.go and
review3_fetcher_scratch_test.go, in ui/ (`go test -C ui -race -run TestReview3 -v ./...`;
the real-fetcher one needs REVIEW3_FETCHER=<built fetcher binary>).

W1 major — ui/trust.go:143 verifiedState, ui/wallet.go:560-568 Freeze. The carried file is used whenever the
node's state fails to verify for ANY reason, including errBanksChanged and "not signed by the validators
pinned". With a file loaded: (a) the refusal disappears (generic note only); (b) Send/UpdateAccount/Freeze POST
to the distrusted node (manual says "Don't send anything"); (c) the freeze names a block counted from the file's
height (stale; refused). Evidence TestReview3FileMasksTheNodesRefusal. Fix: fall back to the file only when
nodeCheckpoint failed (transport/HTTP status) or the node's state is older than the file (sentinel error);
return any other error as is. Optionally Freeze uses max(file height+1, wallet.height()).

W2 minor — cmd/fetcher/main.go:129 checkpoint max-age=5 vs ui/wallet.go:327, ui/trust.go:201. A CDN-cached
checkpoint: (a) blocks named from a stale height -> "stale block id" (freeze retry repeats the stale block);
(b) two CDN copies of different ages -> false rollback alarm "older than … may be hiding a newer change";
(c) after restart (file forgotten, NewestHeight kept) a behind bank's node gets the same alarm. Evidence
TestReview3CachedCheckpointMakesAPaymentStale, TestReview3CDNCopiesOfDifferentAgesRaiseTheRollbackAlarm.
Fix: checkpoint max-age=1 (spec §6.3); keep the newest verified checkpoint in memory and show it when the node
serves an older one, instead of refusing (keeps the rollback guard).

W3 minor — ui/main.go:184-190 save step answers its POST with the page (200); reload/Back-Forward re-posts and
signs a second payment for the next block (both valid). Evidence TestReview3SaveStepSignsAgainOnReload. Fix:
keep the last saved payment in memory, redirect to a GET that shows it.

W4 minor — ui/main.go:262 POST /checkpoint: Go's form parser caps url-encoded bodies at 10 MB unless wrapped in
MaxBytesReader; a genuine checkpoint of ~3,400+ accounts is refused with a misleading "paste the whole file".
Evidence TestReview3LargeCheckpointFileIsRefusedAsCutShort. Fix: request.Body = http.MaxBytesReader(w, body,
3*node.MaxCheckpointBytes) before FormValue; report a ParseForm error as "too large". (A file input would fit
"a file" better.)

W5 minor — ui/trust.go:191 first use vs spec §6.4, §8: first use through a public address (a CDN, plain http in
the quickstart) pins whatever validators that face serves: the CDN or the path can pin a new wallet to its own
network. §6.4's "never a false one" holds only once pinned. Reasoning only (existing TOFU path). Fix: docs and
manual say first use through a public address trusts the CDN; customers get the .trust file from the bank.
Optionally refuse first use over plain http.

W6 nit — ui/wallet.go:640 postJSON: every 404 becomes "votes are submitted on the bank's operator node", also for
/hdls/update, a freeze, a desk's name request. Fix: that text only for the two certificate paths.

W7 nit — ui/trust.go:194-198: for a file, errBanksChanged's text still says "this node's state … restart the
wallet with that bank's node". Fix: build it from source like the other two messages.

W8 nit — ui/trust.go:233-247 race: LoadCarriedCheckpoint verifies, unlocks, then stores; a concurrent
verifiedState may raise NewestHeight and clear the file in the gap; the older file is then stored and later shown
below NewestHeight. Reasoning only. Fix: store only if height >= NewestHeight under the same lock; skip a stored
file below NewestHeight.

W9 nit — cmd/demo/main.go:179: -listen 0.0.0.0:8080 now aborts ("0.0.0.0 is not an IP address…"); it ran before
and the comment at :132 says -listen can move the demo off 127.0.0.1. Fix: publish 127.0.0.1 for an unspecified
listen IP, or say in the flag help that a specific IP is needed.

W10 nit — ui/publicface_test.go:79: spec §8/§10 ask for a test against a fetcher; the stand-in forwards GETs to
the engine's handler instead of running cmd/fetcher over the public folder. The reviewer ran the wallet against
the real fetcher binary over a real engine's public folder: pay with verified receipt, update with public
address + two IPs, admin page, freeze, alice's name record — all work (TestReview3WalletAgainstTheRealFetcher).
Only the required test is missing.

Checked and sound: /checkpoint and the save step behind the same CSRF/Host guard; a carried file goes through the
same consistency/⅔/network-domain checks, refused before a pin, moves the pin only by the ⅔ rule; forged balance,
self-signed set, same-named other network, older file each refused; SavePayment/Send/UpdateAccount share
nextPaymentBlockID under the mutex, counter saved before signing, window bound holds with a file; list handling
consistent across genesis, UpdateFitsState, ProposeBlock, ApplyBlock (empty unchanged, ["-"] clears, refused in
genesis, mixed refused, dups and 5th refused; nil and [] hash the same); count-prefixed list inside
length-prefixed fields keeps digest/signFields/UpdateID injective; GenesisHash covers gossipPort; old genesis
`address` key refused; resolve fields only when set; banks.go reads PublicAddress scheme://host only; account page
runs the ledger check before taking a block; demo's founder addresses pass genesis.
