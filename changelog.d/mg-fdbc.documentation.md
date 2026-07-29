- **The shipped prompt corpus stops teaching `--body="..."`, and the ratchet
  that permitted it is emptied in the same commit (mg-fdbc).** Every one of the
  40 grandfathered example lines in `internal/agent/prompts/` is converted to
  `--body-file -` with a **quoted** heredoc, and `bodyRatchet` — the frozen
  inventory shipped by mg-d91f — goes from nine entries to none.

  **The counts inverted, which is the whole point.** The architect's walk
  (mg-e0ca) found the gradient at its source: the corpus taught the unsafe
  inline form dozens of times and both safe forms zero times, and an agent does
  not learn its idioms from `--help`, it copies them from its own prompt. Both
  binaries grew `--body-file` months ago (mg-7850, mg-8380) and neither fix
  moved the number. After this sweep the corpus teaches `--body-file -` with
  `<<'EOF'` 40 times and `--body="..."` zero times.

  **Three numbers were in circulation and all three were right.** 62 counted
  every *mention*, a raw grep counted 58 because it includes prose, and 40 is
  what the tree actually **taught** on an example line — the predicate
  `scanBodyExamples` implements. They measure different populations; the header
  now says so, so nobody "fixes" one to match another. The load-bearing figure
  was always the one they agreed on, and it is the one that moved.

  **This had to be a two-file diff.** The ratchet is path-keyed and enforces
  *tightening*, not merely a ceiling: a swept file whose entry is not lowered in
  the same commit fails the suite by name. That is deliberate — leftover
  headroom is exactly how a swept file silently re-accumulates — and it means
  the same control that catches a new violation also catches a *partial* sweep.

  **Two lines refused the mechanical substitution, and saying so matters more
  than hiding it.** The release-cut hook in `pm/pm-template.md` interpolates
  `$tag`/`$days`/`$ahead`, and the PR announcement in
  `templates/polecat-build-pr.md` interpolates `$BRANCH`. A quoted heredoc would
  emit those literally and an unquoted `<<EOF` expands exactly like
  `--body="..."` — it is the original bug wearing the fix's clothes. Both are
  rewritten as `printf ... | mg ... --body-file -`, which keeps the values in
  argv slots where no shell parses them.

  **The order was the argument, not the sweep.** Before mg-d91f the inflow (~80
  new unsafe lines in 30 days) exceeded the entire standing stock, so a sweep
  alone would have decayed inside a month. The ratchet stopped the inflow first;
  this is the cheap bounded cleanup that became possible afterwards. The
  positive control was run on the swept tree rather than assumed: an unsafe line
  re-added to `templates/polecat-qa.md` makes the guard fire and name the file.

  **What this does not fix.** `mayor.md` still instructs the coordinator to
  "preserve the rest of the body when rewriting" — the capture-then-rewrite
  mandate behind tonight's lost updates. That sentence is *prose*, and the
  ratchet exempts prose on purpose (documenting the hazard must not be punished
  by the check that exists to reduce it), so this sweep leaves it byte-identical
  and does not claim it closed. `mg edit --append-body-file` (mg-f326) is the
  replacement it should point at; that is a separate scoping decision.
