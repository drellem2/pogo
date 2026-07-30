- **The `--body="` ratchet's own comments stop teaching that `--body='...'` is safe
  (mg-ff5c).**
  Two comments in `internal/agent/bodyratchet_test.go` asserted it outright — the
  `inlineBodyRE` comment said single quotes are not matched because the form *"reaches
  argv unmangled and is not the defect"*, and the boundary test's header called it
  *"safe, just awkward for bodies that contain single quotes."* "Awkward" is the wrong
  word for what it actually does.

  **Measured, under both `zsh` and `bash`, before rewording.** The failure mode is
  selected by the *parity* of the apostrophe count in the body:

  ```
  odd   --body='the polecat's PR flow'   -> unmatched ' , exit 1     LOUD
  even  --body='don't do it, it's fine'  ->              exit 0     SILENT
  ```

  The even case does two things, and the second is worse than the stripping: the
  apostrophes are removed from the content, **and the value is word-split across
  separate argv slots** — argv is `--body=dont`, then `do`, `it,`, `its fine` as
  further arguments. The callee therefore does not receive a corrupted body; it
  receives a *truncated* body plus junk positional args, at exit 0. Both shells behave
  identically, so this is not a zsh quirk.

  So `--body='...'` is safe only for content containing no apostrophes — not a property
  anyone tracks while writing prose. It is the `--body="` defect with a different
  trigger character: `--body="` eats `$` and backticks, `--body='` eats apostrophes and
  the argv shape.

  **Why a comment was worth a ticket.** This ratchet exists so shipped templates stop
  *teaching* an idiom that fails silently. A comment inside the ratchet's own control
  blessing a silently-failing idiom is that same defect one layer up, and it had already
  misled someone: polecat 78d2 said so during the mg-78d2 review — *"I had read the
  ratchet passing as the construct being safe, which is exactly the inference the
  ratchet's own header warns against"* — having written `--title '<prose>'` on that
  basis. A green ratchet means the predicate did not measure the line, never that the
  line is blessed, and the comments now say which.

  **Deliberately NOT the fix: widening the scanner.** Flagging `--body='...'` is exactly
  the false positive that refuted the metacharacter gate (`cmd/pogo/bodymetachar_test.go`,
  case C). The boundary test keeps passing and keeps existing, and both reworded comments
  keep naming *why* the scanner leaves this form alone, so the boundary stays deliberate
  rather than becoming an accident nobody can explain.

  **Blast radius, now measured rather than assumed.** No shipped template contains a
  literal apostrophe inside a single-quoted flag value. Ten template lines do teach
  `--result='{...}'` with free-prose placeholders an author fills in, so the hazard class
  is reachable through authored content even though no shipped byte trips it today.
  Noted, not swept — no prompt content changed here.
