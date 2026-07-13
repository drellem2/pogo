# Rating-dialog modal-watcher missed the stuck mayor dialog (mg-f36b)

_Investigation + fix, 2026-07-13. Anchors: `internal/claude/modal_hook.go`,
merge tickets mg-ef6b / mg-4421 (watcher shipped 2026-05-19)._

## Incident

On 2026-07-13 the mayor's PTY got a stuck Claude Code mid-session rating dialog
(`How is Claude doing this session? 1:Bad 2:Fine 3:Good 0:Dismiss`) that wedged
stdin for ~2.5h. The modal captured every keystroke, so all pogod PTY nudges
were swallowed and 5 mails sat unread. `~/.pogo/events.log` around
2026-07-13T20:14–20:37Z shows repeated `nudge_sent` to `crew-mayor`, all
`mode=idle`, none draining. doctor cleared it manually with
`pogo nudge mayor 0 --immediate`.

## What was investigated

**1. Is the watcher goroutine actually running / not regressed?** Yes, intact.
`claude.Provider.SessionHook = ModalHook` (`internal/claude/provider.go`), and
`Registry.invokeSessionHook` starts it in both the spawn path and the
restart path (`internal/agent/agent.go`). No regression since the 2026-05-19
merge — the hook still runs for the agent's lifetime.

**2. Does the matcher cover the actual dialog string/layout? — NO. Root cause.**
The `rating-dialog` matcher's `LineMarker` is the literal `"1:Bad 2:Fine 3:Good
0:Dismiss"` (spaces between options), compared with `bytes.Contains` against
`StripANSI(rawBuf)`. But Claude Code renders that option row as a TUI **footer**
whose columns are positioned with cursor-forward escapes (`ESC[<n>C`), **not**
literal spaces. `StripANSI` deletes those escapes outright — it does not
substitute a space — so the on-screen `1:Bad 2:Fine 3:Good 0:Dismiss` reaches
the scan buffer run-together as `1:Bad2:Fine3:Good0:Dismiss`. The spaced marker
never matched.

This is the same space-collapse class as the prompt-ready sentinel bug
(gh#76 / mg-d06a): TUI footers use per-column moves, so spaces vanish under
`StripANSI`. The existing six fixture tests all fed the marker as clean text or
inside simple SGR color codes (spaces intact), so they passed while production
never matched.

Corroborating evidence: across the full 48 MB `events.log`, `modal_dismissed` /
`rating_dialog_dismissed` appears **zero times** — the watcher has never
successfully dismissed a rating dialog in production since it merged.

**3. Secondary — `pogo agent diagnose` reporting `Health:idle` for a redrawing
PTY.** Confirmed pre-existing (called out in mg-ef6b's background). It does not
factor into this fix: the `rating-dialog` matcher uses `ModeScannerIdle`, which
measures its idle window off the scanner's own last-chunk timestamp on the
injected clock (`modalScanner.LastChunk`, mg-872b) — it never consults
`diagnose`'s idle signal. So the watcher already does not rely on the buggy
signal; the diagnose mis-classification is a separate, out-of-scope bug.

## Fix

Marker matching is now whitespace-insensitive. `matchNormalize` strips ASCII
whitespace from **both** the ANSI-stripped buffer and the marker before
`bytes.Contains`; normalized markers are precomputed once per scanner. This:

- matches the real column-move footer (`1:Bad2:Fine3:Good0:Dismiss`),
- still matches a literal-spaces render (back-compat),
- absorbs lesser drift for free (a space after each colon, doubled spaces),
- does not weaken false-positive protection: the `ModeScannerIdle` gate
  (500 ms of no output + re-verify marker still visible) remains the guard
  against transcript mentions, and the run-together marker forms are specific
  enough not to coincide with ordinary output.

Files: `internal/claude/modal_hook.go` (matcher normalization + doc comment).

## How the fix was verified

- **Reproduced the gap empirically.** With the real `StripANSI` regex, a
  column-move footer `\x1b[2K\x1b[38;5;244m1:Bad\x1b[3C2:Fine\x1b[3C3:Good\x1b[3C0:Dismiss\x1b[0m`
  cleans to `1:Bad2:Fine3:Good0:Dismiss`; `bytes.Contains(clean, literalMarker)`
  returns **false** (the production gap), and the whitespace-normalized compare
  returns **true**.
- **Regression tests** (`internal/claude/modal_hook_test.go`):
  - `TestModalScannerColumnMoveFooter` — asserts the column-move footer is
    detected AND that the old literal compare would NOT have matched it (so the
    test genuinely reproduces the field failure).
  - `TestModalHook_ColumnMoveRatingDialogFires` — drives the full watcher
    against the column-move footer and asserts a `0\n` dismissal fires.
  - `TestMatchNormalize` — unit-covers the helper, including drift variants.
- Full gate green: `./build.sh` (fmt + `go test ./...` + build).
