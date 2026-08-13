- **`memory index parity` measures REACHABILITY, and stops calling 42 reachable
  notes unreachable (mg-d97f).** The check compared the note files on disk
  against the names in `MEMORY.md` and reported the difference as "on disk and
  unreachable by recall: nothing points at them". The index is where recall
  *starts*, not where it ends. A note an indexed note names is reached; a note
  listed in a sub-index that is itself hooked is reached for the price of one
  index line. Measured on the shared corpus at the moment the conflation was
  found:

      237 notes
      195 named by MEMORY.md directly
       42 reachable only through another note — 28 via ONE hooked sub-index
        0 unreachable by any route

  All 42 were being enumerated as defects, and something pointed at every one of
  them. The number then went into a corpus-policy argument about whether
  reachability and the size cap were jointly satisfiable — an argument that did
  not need to happen, because nothing was unreachable. A check that enumerates 42
  non-defects is a check that gets tuned out, and the real ones go with it.

  **The two populations are now separated and BOTH reported.** `UNREACHABLE` — no
  route at all — is the defect and is what warns. `INDIRECT` — reachable, but only
  via another note — is reported as a question about *prominence*, which is a real
  property worth tracking and a different one from reachability. The three tiers
  differ in discovery cost, not in reachability: `MEMORY.md` is auto-injected and
  surfaces unprompted, a sub-index is one hop and needs a reason to look, a
  link-only note needs the reader to already be in the neighbourhood. Which notes
  earn the auto-injected tier is the corpus owner's judgement and is stated as
  such — but it must not be reported in the vocabulary of unreachability.

  Across the three stores that were warning, this drops 63 reported defects to 14
  and keeps every one that is real: shared corpus 42 → 0, `pm-onethird` 13 → 6,
  `pm-pogo` 8 → 8.

  **Reachability starts at the index, and the controls say so.** A sub-index whose
  own hook is missing reaches nothing — otherwise dropping that one line, which
  makes the size check happier, would launder every note it lists into "reachable"
  through the remedy this checker now recommends. Two orphans that link to each
  other are still orphans. Detection also learned the `[[wikilink]]` form: notes
  name each other as `[[slug]]` with no `.md` in the string, so the
  filename-containment test could not see the corpus's own link graph at all.

  **An unreachable note's body is never opened.** The walk expands from notes
  already reached, so the bound falls out of the predicate instead of being
  imposed on it — the 127KB scratch queue nothing points at costs one directory
  entry and no read, and there is no scan limit for a link near the end of a long
  note to fall past.

- **The parity remedy stops recommending a destination nothing loads
  (mg-d97f).** It offered "RE-ROUTE — move a note that belongs to a
  less-pressured index (an agent-scoped dir rather than the shared one)". On this
  box those per-agent stores had stopped being loaded on 2026-07-07 and 153 notes
  sat in them unread for five weeks — the exact condition `pogo check-memdirs`
  was added to detect (mg-a9b3), recommended as a remedy by its own package.
  Following it would have moved notes into a directory nothing reads: the parity
  count would have dropped, the size check would have gone green, every number
  would have improved, and the content would have been unreachable. "Less
  pressured" is not evidence that a destination is loaded — an unpressured index
  is exactly what a store with no readers looks like, so the phrase selects *for*
  the failure.

  Replaced by **SUB-INDEX**: hook one secondary index from `MEMORY.md` and list
  the notes in it. One line however many notes it names — the only remedy on the
  list that scales with the count, which is what a shortfall of 42 notes against
  room for 13 hooks needs. A regression guard pins that no re-route is offered
  and that the constrained branch states the constraint a destination must meet.

- **Folding is reported as free against the index, not free (mg-d97f).** A fold
  costs zero index lines, which is what a margin policy rewards, so a reader
  following the incentive folds every time and never sees the aggregate. The cost
  lands in the host and nothing counted it: after a night of individually-correct
  folds, two notes had grown to 30,414 and 28,348 bytes against a 23,952-byte
  `MEMORY.md` — each larger than the whole index that reaches them — while the
  parity number went *down* as it happened, reading as improvement. Parity now
  measures host size and says so wherever it recommends a fold, naming the largest
  host and how many notes exceed their own index. It rides with the fold advice
  rather than being its own check: it is not a defect, it is the price of the
  remedy being recommended in the same sentence.
