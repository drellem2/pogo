- **`pogo check-staleness`'s prompt reference carries its own limit, and the
  witness can see the window that matters (mg-afd0).** The prompt half compared
  the installed corpus against `~/.pogo/deploy-src @ origin/main` and labelled
  the row exactly that. `deploy-src` is fetched **at deploy time and never
  after**, so its `origin/main` is frozen at the *deployed* revision — and a
  reference pinned there structurally cannot witness the command's own headline
  claim (*"the fleet is running something older than what shipped"*) for
  anything that shipped after the last deploy, which is the only window in which
  the fleet is running something older. Measured 2026-08-13: 17 commits behind
  the real `origin/main`, five touching the corpus, one of them `d27ecc1`'s fix
  to `crew/doctor.md` — and the report read `ok: all 9 shipped prompt(s) match
  the reference` while every agent read the superseded file.

  **Two things the row was missing, both now printed on every run.** *When* the
  reference last fetched, from `FETCH_HEAD`'s mtime — not the remote-tracking
  ref's own mtime, which dates the last time the branch *moved*, so a mirror
  that fetched an hour ago and found nothing new would date itself days old and
  read as abandoned. And *whether the remote has moved past it*, asked with
  `git ls-remote`, which queries the remote and writes nothing. When the
  reference already holds those objects the gap is quantified and the
  corpus-touching commits are **named** — a count says how far behind, only a
  subject says behind on *what*. When it does not, which is the usual state of a
  mirror that has not fetched since 03:00, the gap is reported as **unknown**
  rather than as zero, and unknown is a finding.

  **A gap that touches nothing under the corpus is deliberately NOT a finding.**
  This witness judges the prompt corpus; commits elsewhere leave that verdict
  exactly right, and reporting them here would be the same over-claiming the
  change exists to remove. Likewise an unreachable remote is printed loudly and
  not counted — a check that exits 1 on every offline laptop gets its exit
  status ignored, which is the failure the whole command is built to avoid. The
  disarm is *declared* rather than skipped, for the reason the did-not-run half
  already declares its own: a witness whose finding is an absence and a fleet
  that is up to date produce identical silence.

  **Nothing fetches by default.** `ls-remote` is a query; the existing property
  that the detector does not mutate the tree it judges is intact. `--fetch` opts
  in, runs the comparison against what *shipped*, and still names where the
  reference stood **before** — that is the deployed revision, and the fetch is
  the only thing that erases the local record of it, which is why it is captured
  first. That fetch passes `--no-write-fetch-head`, because a plain `git fetch`
  rewrites the one file the "last fetched" row is read from: the remedy would
  otherwise erase the evidence of the fault it repairs, and every later run
  would report the reference as freshly fetched when what it meant was *this
  detector fetched it*. On a git too old for the flag the run says the timestamp
  moved rather than moving it in silence. `--skip-remote` disarms the query.

  The same rule was applied to the fix's own output twice: after `--fetch` the
  row no longer prints "anything pushed since is invisible to the comparison
  below", with or without a datable prior fetch. A caveat that no longer holds,
  travelling with the output that disproves it, is the defect this entry is
  about.
