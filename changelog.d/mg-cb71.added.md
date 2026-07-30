- **`pogo doctor --check` now measures auto-memory health on THREE axes, not
  just index SIZE — and one of the two new axes is one the size check read
  BACKWARDS (mg-cb71).**
  A memory store fails in three ways. All three are invisible from inside the
  session that has them, and only the first was measured:

  | failure | why it is invisible | measured before |
  |---|---|---|
  | over-cap truncation | the dropped entries never arrive | yes |
  | an unindexed note | nothing points at it | **no** |
  | a stale hook | it reads as current | **no** |

  The middle one was not merely unmeasured. A size check reads it as an
  **improvement**, because dropping a hook makes `MEMORY.md` smaller — the
  instrument moved the wrong way at the exact moment a note became unrecallable.

  **`memory index parity`** asserts that every `*.md` in a memory dir is
  referenced by the index beside it. Measured across all 16 memory dirs on the
  development box: **16 unreferenced notes in 3 dirs**, the other 13 at exact
  parity. Written, costing disk, unreachable by recall. One concrete cost found
  while measuring: a 1352-byte note describing `mayor.md` git drift sat
  unrecallable on disk while that exact drift was being re-diagnosed from
  scratch.

  Shipping this as a bare count would have made it unusable, because **deliberate
  non-indexing is a correct action that produces a parity "defect"** — two of the
  16 were a hook dropped on purpose (it asserted an open review for an
  since-archived item) and a 127KB working scratch queue that should never have a
  hook. A note opts out with `index: none` in its frontmatter; opt-outs are
  reported separately and never counted as defects, so a reader can tell a clean
  store from one that silenced itself.

  **`memory index staleness`** resolves the work-item IDs named in tense-bearing
  index lines and compares. Of 6 candidate lines on the box, the 2 genuinely
  stale ones now fire — a hook still presenting a **shelved** item as "blocked",
  and another still presenting an **archived** audit as "Piece 2 blocked".

  The predicate is **consistency, not keywords**, because a keyword filter gets
  the third resolvable line wrong: `(superseded) … RESOLVED … mg-ba0b done.`
  names an archived item and is *correctly maintained*. Flagging it would be
  worse than silence — the natural response to a "stale" verdict is deleting the
  hook, so a false positive destroys a correct memory. Hence four suppressions,
  each calibrated against real lines: the line must not record its own
  resolution; `OPEN:` keeps its colon (a bare `open` matches filenames like
  `project_..._open.md`); word boundaries stop `unblocked` matching `blocked`,
  which is the opposite claim; and the assertion must sit within 60 characters of
  the ID — measured at 13 and 17 for the stale pair versus 109 and 222 for
  generic guidance whose ID is a trailing citation.

  **An unresolvable ID is silent, never stale.** Short-ID lookup genuinely fails
  on a real box — `mg show mg-3119` is ambiguous across two archived twins (exit
  4) and `mg-zzzz` is not found (exit 3) — so an ambiguous or missing result
  suppresses its whole line.

  **A blind resolver is no longer reported as health.** The required positive
  control caught this check shipping the very bug it was written to prevent:
  macOS ships `/usr/bin/mg`, an unrelated micro-emacs clone, so a PATH check
  alone said "available" while every lookup failed, no hook fired, and the axis
  passed clean. An availability probe cannot catch that — the wrong binary *is*
  available. The honest signal is the hit rate, so lookups attempted with zero
  resolved now warns `cannot check` and says so explicitly.

  Both checks are **detect-only**, like the size axis (mg-15c0): auto-appending
  hooks would index a working scratch queue, and auto-deleting a "stale" hook is
  the destructive move the suppressions exist to avoid. Both walk the same
  `memcheck.Locate` population as the size check, so they cover
  `~/.pogo/agents/*/*/memory` and every harness glob from the provider registry
  rather than a hard-coded dotdir (mg-5a06).
