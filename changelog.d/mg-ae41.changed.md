- **The QA and review templates ask the fires-check for its second half: name the
  change under which the check's answer would DIFFER (mg-ae41).** "A control must
  be able to fail — prove it fires" has been in the consolidated evidence block
  since `869348a` (mg-0d85). One evening produced three checks that **pass that
  test and are still blind to the defect they guard**:

  - **mg-8a5c** — a figure gate that passes when the figure a reader reads is
    wrong, at all three sites, because the correction prints the value twice and
    the gate is a *presence* test.
  - **mg-d0e2** — a repair's own check, *"43 rows, 0 label change(s)"*, which
    HOLDS on an artifact where every row reads `[FAIL]`, because it measures label
    *stability*, not correctness.
  - **mg-7dd3** — per-section checkers all green while a quotation is struck in
    section 4 and asserted live in section 0; a per-section checker cannot see a
    cross-section strike **by construction**.

  Every one of them can be made to fire. They are not broken instruments — each
  measures a property **invariant under the failure it guards**, so firing proves
  nothing about that failure. The rule now asks for the change that would move the
  answer, and adds the scope corollary from mg-7dd3: *a check scoped narrower than
  the defect cannot see it by construction — widen the check, do not add another at
  the same scope*, which refuses the natural wrong fix of a second checker at the
  blind scope.

  **Folded into the recovery demonstration, not added beside it.** From mg-a318,
  unprompted: the audit instrument was made to tell the truth on a **re-run**, not
  only on a first pass. That is the same concern as "does a legitimate baseline
  refresh disarm the guard", one step later in time, so it lands on the same
  clause — the guard must still fire *on a RE-RUN as well as a first pass*. A check
  that passes only against a fresh tree quietly stops meaning anything, and, like a
  sanctioned refresh, the disarming looks like maintenance.

  **Length, measured — this MODIFIES the rule, it does not add a sixth.**
  `polecat-qa.md` 242 → 244 lines, `polecat-review.md` 247 → 247 (its bullets are
  one line each, so the whole modification lands in place). The QA bullet 8 → 10
  lines, 680 → 1003 characters, 110 → 168 words; the review bullet 545 → 889
  characters, 93 → 157 words. **Net +2 lines across the two templates**, and the
  section still has five bullets — now checked structurally, by counting `- **`
  bullets between the heading and the next protocol step, so a sixth fails the
  build even if the heading still claims five.

  **Still not a gate.** Nothing verifies that an invariance question was asked —
  only what the worker writes down before they look — and the forbid list gained
  two entries (`do not report a verdict until you have named the change`,
  `refuse to verdict unless the check`) so it stays that way. The scope pin is
  unchanged: `polecat.md`, `polecat-build-pr.md`, `polecat-triage.md` and
  `polecat-architect.md` were deliberately left alone.

  **Left alone deliberately.** The other four bullets, the section heading, the
  "five habits" framing, and mg-1023's refuted-attribution pins are untouched; the
  only lines changed are the fires-check bullet in each file.

  **Near-miss, mine.** The first wrap of the QA bullet split
  `regenerate it and show the check still fires` and
  `the disarming looks like maintenance` across line breaks, silently dropping two
  pins that had been green since mg-0d85 — the exact defect mg-04c3's own first run
  caught in itself, and my pre-run prediction said the suite would pass. It failed;
  the wrap is now atom-aware and the phrases are contiguous. Both new assertions
  were exhibited failing before being trusted: reverting the templates fires 14
  errors naming all seven new literals across both files, a sixth bullet pasted into
  `polecat-qa.md` fires `6 evidence-discipline bullets, want exactly 5`, and a
  gate-shaped line fires the not-a-gate check.
