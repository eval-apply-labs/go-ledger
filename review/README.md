**REFERENCING v1.0.0 — NOW ADDRESSED.**

# External review — v1.0.0 (resolved)

Two independent review reports produced by an external automated reviewer (GLM) against the
**v1.0.0 release** (commit `0ac193b`), kept verbatim as a record of what an outside pass found:

- [`glm_code_review.md`](glm_code_review.md) — code: test coverage and landmines, five new findings.
- [`glm_documentation_review.md`](glm_documentation_review.md) — docs: where a user or operator
  wasn't warned about a behaviour that would bite them.

**Nothing in these files is a pending issue.** Every finding was verified against the source and
dispositioned the same day — see the *External review (GLM) — 2026-08-02* section of
[`findings.md`](../findings.md) for the full triage. In short: the three real code defects (C1, C3,
C4) are fixed with regression tests — C3's verified impact was a silent per-payment mint, worse
than the review's "bricked network" claim; C2 is accepted as LOW with rationale; C5's "fork risk"
did not survive verification. The coverage gaps the review prioritised (cmd/server, the wallet's
CSRF/receipt surface, the overdraw-ban pin) are closed, and the doc warnings it found missing are
in the manuals — except D9/D10, which the triage found overstated for this wallet.
