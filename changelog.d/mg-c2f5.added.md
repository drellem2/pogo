- **A consumer re-pointed away from the live data kept reporting health. Added
  the instrument that compares a consumer's CONFIGURED SOURCE against where data
  is ACTUALLY ARRIVING (mg-c2f5).** New `internal/sourcewatch` and a `consumer
  source liveness` row on `pogo doctor --check`. Documented in
  [docs/operations.md](../docs/operations.md).

  **The state it reports.** `com.pogo.notify` — the job whose name is "Daniel
  gets notified" — was loaded, healthy, polling on schedule and reporting no
  error while reading a directory the fleet does not write to, for at least 40
  hours. `launchctl list` said healthy: **true**. Its own log said polling, no
  errors: **true**. Daniel received nothing from it; the fail-open
  `com.pogo.deadman` behind it carried 100% of production. Everything went dark
  and every check stayed green, and no instrument on the machine reported the
  state — it took three agents independently going to look at one quiet log.

  **The routing is not what this fixes, deliberately.** A primary watching an
  empty box is the *designed* intermediate state of the mg-65d2 staged cutover,
  not a misconfiguration; finishing or reverting it is mg-8158 and is Daniel's
  decision. Nothing here touches `MAIL_DIR` or either plist. Re-pointing the
  primary back at `human` was proposed three times on 2026-08-09 by three
  separate routes and retracted every time — it reverts a step that is verified
  and re-opens the ordering hazard, and the most persuasive version of it was
  framed as *restoring redundancy*. **The missing alarm was the whole defect.**

  **It names no box, which is the point.** "Alarm if notify watches `daniel/`
  while agents write `human/`" is a control that DECAYS the moment the cutover
  completes — and completing it is the expected outcome. Consumers and their
  sources are discovered from the installed plists by two structural admission
  rules (a divergent binding between two instances of one program; a directory
  that is one of a family of like-shaped boxes), so the check catches the next
  re-point automatically, whoever performs it and in whichever direction.

  **It had to pass architect's test against itself:** *what would this instrument
  report if the thing it NAMES stopped entirely? If the answer is green, it is
  measuring its own execution.* A "poller health" check would have passed casual
  review and reproduced the exact defect inside the fix for it. Here, if all
  traffic stops, every source is quiet and the naive predicate ("someone has zero
  while someone else has more") finds nothing — so fleet-wide silence is
  `NOT CHECKED`, never a pass, and so are an unreadable source, an empty consumer
  population and a discovery that could not run. The row renders on every doctor
  run, clean or not, with the population it examined.

  **What it says about `com.pogo.notify` today, measured rather than asserted.**
  Against the live plists at 19:22Z on 2026-08-09 it reports the job STARVED at a
  30-minute window and LIVE at the six-hour default, because
  `~/.macguffin/mail/daniel/new` has in fact received seven messages during the
  day — mayor, pm-pogo and pa address `daniel` directly. The 40-hour silence is
  real and predates that traffic. So this does **not** convict the job at its
  default window right now, and should not: the box it reads is receiving. An
  instrument tuned until it agreed with its own ticket would be one more thing on
  this box measuring its own premises.

  **Two defects in the first cut were found by running it against the real
  machine, not by reasoning.** The sibling family of a mailbox here is *every*
  agent mailbox — 1364 directories — so the finding enumerated the live ones into
  a 400KB doctor row (now: exact counts, at most three named most-recent-first,
  "and N more"), and every run listed 1364 directories including one holding 1249
  files (now: a directory whose own mtime predates the window has had no entry
  created or unlinked since, so the listing is skipped — two full sweeps in
  0.13s). A third was found by its own test: the sibling rule is structural and
  therefore blind, and it admitted a job's `HOME` as a data source, so process
  environment variables are screened by NAME — which does not decay when the
  boxes move.

  **An empty box is not the same as an abandoned one.** A source a working
  consumer drains as fast as it fills holds no backlog; liveness therefore reads
  the directory's own mtime, which moves on unlink as well as create, so traffic
  THROUGH a box is not mistaken for the absence of it. Getting that wrong fires
  the check on the healthy consumer, which is how a detector gets switched off.

  **Report-only, and it warns rather than fails.** It reads plists and stats
  directories; it never edits a plist, re-points a consumer or bounces a job, and
  it does not set doctor's exit code. An **absent** row means an old binary, not
  a clean machine.
