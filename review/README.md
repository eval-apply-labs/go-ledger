# The review folder

The reviews of go-ledger, and the record of the Phase 2 build. One line per file and folder; start
with the first, the build record.

- [`phase2_progress.md`](phase2_progress.md): the Phase 2 build record: where the branch stands, how
  each slice and each fix run was built and checked, the owner's decisions, the cost, and how to
  resume.
- [`phase2_spec.md`](phase2_spec.md): the Phase 2 spec, one new genesis: what to build (§1 to §9),
  the slice plan (§10), the docs to change (§11), the owner's questions and decisions (§12), and the
  record of its five reviews (Appendix).
- [`phase2_code_notes.md`](phase2_code_notes.md): the 75 notes, C1 to C75, of the last read-through
  of the code before the push, each with what was wrong, how it was checked and how it was fixed;
  all were fixed on 2026-10-31.
- [`post_emudhra_review_2026-10-27.md`](post_emudhra_review_2026-10-27.md): the post-eMudhra
  review: 45 findings, E1 to E45, on commit `9a60643`, and the fix plan in three phases. What
  became of each finding is in [`../findings_20261027.md`](../findings_20261027.md).
- [`renames_2026-10-27.md`](renames_2026-10-27.md): the names spelled out after that review: what
  changed, how a tool proved the renamed tree is the same program, and the map from the old names
  to the new.
- [`_evidence_2026-10-27/`](_evidence_2026-10-27/README.md): the evidence behind that review and the
  rename: proof-of-concept tests, harnesses, logs, prototype fixes, the rename tool and its maps.
  Its README says where to look. The key files under `other/vwk-e2e/` are throwaway Ed25519 test
  keys from a Phase 1 run; Phase 2 uses ML-DSA-65, so they are valid on no network.
- [`_evidence_phase2/`](_evidence_phase2/): the brief and the reports of each Phase 2 slice
  (`S<n>_reports.json`) and of each fix run of the code notes (`fixes_<notes>_reports.json`),
  `movecheck/`, the checker that compared the code before and after the file moves, and `wrapup/`,
  the reports of the hand-off pass of 2026-10-31.
- [`glm_code_review.md`](glm_code_review.md): an external automated review (GLM) of the code of the
  v1.0.0 release, 2026-08-02, kept verbatim. Nothing in it is pending.
- [`glm_documentation_review.md`](glm_documentation_review.md): the same reviewer's review of the
  docs of v1.0.0, kept verbatim. Nothing in it is pending.

## The commits these files cite

The files here cite commits by hash. On `main`, the work they record, from the post-eMudhra review
of 2026-10-27 to the end of Phase 2 on 2026-10-31, is one commit, the child of `9a60643`. The hashes
name commits of the development history squashed into it, 430 of them, which the maintainer keeps
and which are not in this repository. The squashed commit's tree is that history's last tree, file
for file.

## The external review (GLM), 2026-08-02

Both GLM reports ran against the v1.0.0 release, commit `0ac193b`, which is not in this
repository: its history starts at `9a60643` (v1.0.3). Every finding was verified against the source
and dispositioned the same day; see the *External review (GLM) — 2026-08-02* section of
[`findings.md`](../findings.md) for the full triage. In short: the three real code defects (C1, C3,
C4) are fixed with regression tests (C3's verified impact was a silent per-payment mint, worse than
the review's "bricked network" claim); C2 is accepted as LOW with rationale; C5's "fork risk" did
not survive verification. The coverage gaps the review prioritised (cmd/server, the wallet's
CSRF/receipt surface, the overdraw-ban pin) are closed, and the doc warnings it found missing are
in the manuals, except D9/D10, which the triage found overstated for this wallet.
