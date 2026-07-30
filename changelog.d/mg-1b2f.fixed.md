- **`pogo doctor --check` stops handing a near-cap memory index two instructions
  that cannot both be followed (mg-1b2f).** The parity and size axes added in
  mg-cb71 are individually correct and, close to the auto-inject cap, jointly
  unsatisfiable as they were presented. Parity is a *correctness* property (every
  note reachable); size is a *budget* property; and every unit of parity is bought
  with index lines, which is exactly what the budget is short of. Told both at
  once, a reader satisfies the one with the cheaper diff — and the cheapest diff
  is always to skip the hook. The size check then goes green while the note stays
  unreachable, so **abandoning the property that matters reports as compliance**.

  Neither finding is suppressed — suppressing parity near the cap would reproduce
  mg-cb71's original defect exactly, where an unindexed note made the index look
  *healthier*. What changed is what the two checks **say**:

  - The parity warn now states the fact that makes the dilemma false: **index
    lines are capped, note bodies are not.** The budget applies to `MEMORY.md`
    alone; note content is read on demand and never auto-injected. Two agents hit
    this independently and neither had it written down anywhere — one established
    it only by reading which file the checker names in its own output.
  - Because bodies are free, there are **three** remedies where the warn used to
    imply one. Alongside *add a hook*, it now names **fold** (append the content
    to an already-hooked note and delete the standalone file — parity satisfied by
    removing the orphan rather than pointing at it, at zero index cost) and
    **re-route** (move a note that belongs to a less-pressured, agent-scoped
    index). Both are offered even with headroom to spare, since a note that
    belongs in another index was misfiled rather than merely unhooked.
  - The warn now reports the **arithmetic** rather than a mood: how many
    characters the missing hooks would cost against the headroom actually
    remaining, priced from *that index's own* median hook line rather than a
    pinned constant. Where they do not fit, "add a hook for each" is stated
    plainly as unavailable instead of being offered. This replaces the judgement
    that caused the problem — *the size check is warning, so do not grow the
    file* — with a decidable question. On the corpus this shipped against, the
    hooks turn out to fit on every index that has orphans; the refusal to add them
    was costing reachability for nothing.
  - Folding carries its acceptance test, because the obvious success criterion is
    the wrong one. The test is **not** "the file is gone and the index did not
    grow" but "a reader looking for this content arrives at it". A fold into a
    plausible-but-wrong host converts a *loud, local* problem (an orphan you can
    enumerate) into a *silent* one, which is the same existence-versus-reachability
    failure parity exists to catch, one level in — so a careless fold is worse
    than no fold.
  - The **size** warn now names the competition too, so a reader who sees only
    that half still learns that the cheapest way to shrink is the forbidden one.
    It also retires compaction as a way to fund hooks: on an already-compacted
    index one careful pass recovered 71 characters and five compressions of the
    most verbose entries recovered ~190, against ~200 to add a single line. Where
    the zero-cost remedies do not apply, what is left is a corpus-policy question
    — which notes earn a permanent slot in every session's context and which
    should be found on demand — and the warn says outright that it belongs to
    whoever owns the corpus, not to the detector.

  Deliberately **not** a reordering remedy. Tail-first truncation is measured, but
  a fix built on it is a bet on the truncation *mechanism* holding, and it inverts
  into active harm if the harness ever changes or the measurement is corrected.
  Folding is direction-agnostic: it recovers characters whichever end truncates,
  and no later correction can invalidate it. A regression test enforces that the
  guidance does not drift back toward reordering.

  Hook cost is counted in **characters, not bytes**. `wc -m` returns bytes on a
  box with `LANG`/`LC_ALL` unset, which is how three agents produced independent
  byte-derived figures that corroborated each other and were all wrong; a test
  pins the in-process count so that instrument defect is not reproduced here.

  Verified with both arms live through the real CLI: the affordable branch on the
  development corpus, and the constrained branch on a staged near-cap index in a
  sandboxed `HOME`, where parity still fires 844 characters from the cap rather
  than going quiet.
