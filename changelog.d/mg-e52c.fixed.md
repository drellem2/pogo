- **A PM's "Recently shipped (last 7d)" roadmap section was empty for every PM,
  by construction.** `pm-template.md` read completed work with
  `mg list --tag=<your-tag> --status=done`. Measured 2026-08-19 over the same
  7-day window and the same `jq` cutoff: `--status=done` returned **0** for both
  the `riemann` and `pogo` tags, while `done` + `archived` returned **41** and
  **125**. Not a partial miss — the entire completed population lives in
  `archived`. The query now reads `mg list --tag=<your-tag> --archived --json`
  and filters `select((.status=="done" or .status=="archived") and ...)`.

- **`done` is transient BY DESIGN, so this is not an archiving-cadence
  problem.** The refinery mails the coordinator, pogod closes the item, and the
  coordinator archives it — two separate steps under *any* archiving policy, so
  an item is `done` for the minutes between them and `archived` from then on.
  A twice-daily sweep's 7-day window catching one mid-transition is negligible.
  Slowing the archiver would mask the defect, not fix it, and the template now
  says so rather than leaving that inference to whoever reads it next.

- **The failure was silent and stated in the affirmative.** An empty
  "Recently shipped" renders as a well-formed section saying *nothing shipped
  this week*. It is indistinguishable from a stalled product and needs no
  explanation to be believed — and the roadmap is the artifact Daniel reads to
  see whether a product is moving. On the day it was caught, riemann had shipped
  four items overnight; pm-pogo hit the same zero on the pogo estate the same
  morning next to eight known merges.

- **Generalised past the one query, in both places the template teaches one.**
  The sweep's baseline gather (`mg list --repo=` / `--tag=`) was captioned "open
  / recently-closed work"; the default listing HIDES archived, so for tag `pogo`
  it showed 2 done against 829 archived. It now says, in the fence, that it is
  not a read of what recently closed and points at the roadmap query.

- **`--archived` alone would have been the same defect in the other
  direction.** It ADDS archived rows to the default active+done listing, so
  without the status filter every open item with a recent `mtime` reads as
  shipped: measured, the unfiltered form returned 155 rows for tag `pogo` —
  126 archived plus 28 `available` and 1 `claimed` that had shipped nothing.

- **Two caveats now travel with the query, because both change what the section
  means.** For an archived item `mtime` is the ARCHIVE time, not the close —
  they differ by the coordinator's poll interval, which is fine for a 7-day
  bucket and not fine for anything finer. And these rows must be sorted on the
  full `mtime`, never on the `HH:MM` part: `05:xx` sorts below `09:xx`, which
  silently drops everything from the current morning.

- **The coupling is written down, naming `mayor.md` literally.** That file's
  cleanup step is what empties `done`; each file now points at the other so the
  next editor cannot re-break this unknowingly. The filename is deliberately
  not `{{.Coordinator}}.md` — the coordinator's NAME is configurable but its
  prompt FILE is frozen at `mayor.md` (mg-04ce), so a placeholder there would
  resolve to a path that does not exist the moment the name changes.

- **Pinned by `TestPMTemplateShippedQueryCoversArchived`**, which scopes the
  `--status=done` regression to the fence's COMMAND lines — the prose has to
  quote the refused form in order to explain it — and asserts the status filter,
  both caveats, the coupling line, and the generalisation clause. Every one of
  its assertions was checked to fail against the pre-change file, so the test
  discriminates rather than passing on text that was already there.
