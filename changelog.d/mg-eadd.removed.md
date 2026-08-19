- **`client.ArchiveMGDoneItems()` is deleted — a dead function whose doc comment
  reached a shipped prompt (mg-eadd).** The helper wrapped `mg archive
  --days=0` and had **zero callers**: the refinery's call site was removed on
  2026-03-26 by `3902942` (mg-1f67), deliberately, so completions stay visible
  to the coordinator long enough to act on. The comment was never updated and
  went on asserting *"Called by the refinery after a successful merge"* for 146
  days — and that comment is where `mayor.md`'s retracted "the refinery archives
  the work item automatically — no action needed from you" came from. mg-c2e1
  retracted the claim in the prompt; this removes the code that still agreed
  with the retracted version. `mayor.md`'s §Work item archival now records the
  deletion rather than pointing at a helper to be ticketed.

- **A guard test replaces it, because deleting the function does not stop the
  next author writing it back.** `internal/client/nosweep_test.go` fails the
  build if any non-test Go file invokes `mg archive --days=0` as an argument
  list, and carries a positive control that plants the reintroduction and
  requires the scan to catch it. It matches the argv form, not the prose form,
  on purpose: `cmd/pogo/checkprompts.go` and `internal/promptcli/surface.go`
  both NAME the sweep in order to warn about it, and a check that could not tell
  a warning from a call would be quieted by deleting the warning. Verified
  against `main`'s own `internal/client/agent.go:428`, which the regexp matches.
  The reason the sweep must not return to code: measured 2026-08-19 with `mg
  archive --days=0 --dry-run`, **4 items would have been archived and not one of
  them was the coordinator's** — two architect's, one a PM's, one a request the
  human filed that morning — and it cannot see a `gh-issue` gate at all, because
  carrier state is pogod's parse of the item body and not a field `mg show
  --json` carries. It has eaten live gate carriers twice.

- **Three code comments repeating the same false claim are corrected**, since
  the claim rather than the function is what the ticket exists to remove:
  `internal/workitem/result.go`, `internal/workitem/result_test.go` and
  `cmd/pogod/filernotify_live_test.go` each justified searching `archive/` with
  "the refinery runs `mg archive --days=0` after a merge". The justified SHAPE
  is right and unchanged — a coordinator archiving by id races that reader
  identically — only the premise was stale. `filernotify_live_test.go` still
  runs the sweep and is still correct to: `--root` pins it to a store the test
  created, which is why the guard exempts `_test.go` files.
