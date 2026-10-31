# Evidence for the post-eMudhra review (2026-10-27)

The test cases, harnesses, logs and prototype fixes behind
[`../post_emudhra_review_2026-10-27.md`](../post_emudhra_review_2026-10-27.md). The directory name
starts with `_`, so `go build ./...`, `go vet ./...` and `go test ./...` ignore it.

## Layout

- `trees/<name>/` — files a reviewer added to a private copy of the tree at commit `9a60643`:
  proof-of-concept tests (`*_test.go`), harness programs and run logs.
- `trees/<name>/CHANGES-vs-9a60643.patch` — that copy's edits to existing files: prototype fixes,
  plus the occasional helper added to an existing test file.
- `results/` — the raw output of the three passes (`audit_round1.json` to `audit_round3.json`, and
  readable verdict digests) and the standalone prototype patches.
- `other/` — the pass-1 report clusters, the flaky-test hunt's logs and traces, and small
  measurement programs.
- `renametool/` — the stdlib-only tool that applied the spelled-out names and proves the renamed
  tree is the same program (see [`../renames_2026-10-27.md`](../renames_2026-10-27.md)); its own
  module, written in ordinary Go style.
- `renames/` — the rename maps it applied (`rename-map.json`, keyed by declaration position in
  `9b7965f`; `rename-map-round2.json`, keyed by position after the comment edits) and the file
  groups the naming agents worked on (`units.json`).

Paths inside these files point at the temporary directory the work was done in
(`/tmp/claude-<uid>/...`); it no longer exists. Read `trees/<name>` for those paths.

## Re-running a PoC

PoC tests assert the safe behaviour, so they **fail** on `9a60643` and pass once the finding is fixed.

```sh
git worktree add /tmp/poc 9a60643
cp review/_evidence_2026-10-27/trees/verify3-R3-integrity/node/poc_critic_height_test.go /tmp/poc/node/
cd /tmp/poc && GOLEDGER_CHAOS_SLACK=4 go test -run TestPoCCritic ./node/
```

If a PoC does not compile, it relies on a helper from its tree's `CHANGES-vs-9a60643.patch`; apply
the test-file hunks with `git apply --include='*_test.go'`.

## Where to look

| Findings | PoC tests | Prototype fix |
|---|---|---|
| E1 | `trees/verify3-R3-integrity` (`node/` and `ledger/poc_critic_height_test.go`) | `trees/verify3-R3-bothfix/CHANGES-vs-9a60643.patch` |
| E2 | `trees/verify3-R3-history-safety/node/zz_v3_*_test.go`, `trees/verify-burn-slash` (`TestVBSH1*`) | `trees/verify3-R3-history-safety-fix/CHANGES-vs-9a60643.patch` |
| E3 | `trees/verify3-R3-history-safety/node/zz_v3_rotation_test.go`, `trees/verify3-R3-liveness/cmd/pocdyn`, `trees/verify-sync-trust-replay` (`TestVerifyC19*`, `TestPoCLongRange*`) | same patch as E2 (snapshot half); `results/c19-minimal-fix.patch`, `trees/verify3-R3-liveness/fixtrial/CHANGES-vs-9a60643.patch` (catch-up anchor) |
| E4, E30 | `trees/verify3-c10`, `trees/verify3-R3-governance-ops` | `trees/verify3-R3-governance-ops-c12trial/CHANGES-vs-9a60643.patch` (complete certificates only) |
| E5, E18, E19 | `trees/verify-governance-logic` | `trees/verify-governance-logic-fixtrial/CHANGES-vs-9a60643.patch` |
| E6 | `trees/verify-unauth-amplification`, `trees/verify-governance-logic` (`TestPoCGovHugeRationalStallsEventLoop`) | — |
| E7, E8, E21, E24 | `trees/verify-blocks-economics`, `trees/verify3-R3-dos` | `trees/verify-blocks-economics/_fixproto2/CHANGES-vs-9a60643.patch` |
| E9, E22, E33 | `trees/verify-names-ban` | `trees/verify-names-ban/_fixtrial/CHANGES-vs-9a60643.patch` |
| E11, E41 | `trees/verify3-R3-liveness` (`cmd/pocnet`, `cmd/pocmisc`, `runs/`) | `trees/verify3-R3-liveness/fixtrial-peerdns/CHANGES-vs-9a60643.patch` |
| E12, E31, E32 | `trees/verify3-R3-dos` | `trees/verify3-R3-dos-fix/CHANGES-vs-9a60643.patch` |
| E13, E35 | `trees/verify-burn-slash` | `results/burnset-prototype.patch` (superseded: Phase 2 freezes the name instead) |
| E14, E15, E16 | `trees/flake-suite`, `trees/verify-proposals-liveness` | `trees/verify-pl-combined-clean/CHANGES-vs-9a60643.patch`, `trees/flake-suite-fix/CHANGES-vs-9a60643.patch` |
| E20, E23 | `trees/verify-sync-trust-replay`, `trees/verify3-R3-governance-ops` (`TestV3VSTRNEW3*`) | — |
| E25 | `trees/verify3-R3-integrity/node/r3_catchup_overdraw_test.go` | `trees/verify3-R3-bothfix/CHANGES-vs-9a60643.patch` |
| E10, E26–E28, E36, E37 | `trees/verify-wallet-keys`, `trees/wallet-ui`, `trees/key-lifecycle` | — |

`results/new_key_design_review.json` holds the four lens reviews and the synthesis behind the Phase 2
plan's name registration, key and bank changes and self-freeze.
`results/name_freeze_check.json` holds the three skeptics' attempts to break the Phase 2 rule that a
double-sign freezes the name, and the synthesis behind its final wording.
`results/chaos_root_cause.json` holds the investigations and independent verifications of the three
flaky chaos tests that turned out to be production liveness bugs after Phase 1.

The pass-1 reports for every cluster are in `other/clusters/`; `results/round2_verdicts.txt` and
`results/round3_verdicts.txt` hold the verification reasoning, preconditions and fix pitfalls.
