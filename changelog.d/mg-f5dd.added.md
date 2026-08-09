- **A confirmed fleet-wide verdict-loss detector was sitting unrun in a research
  directory. Ported it to `pogo check-verdicts`, the ninth report-only detector
  (mg-f5dd).** New `internal/verdictwatch` and `cmd/pogo/checkverdicts.go`.
  Documented in [docs/CONFIGURATION.md](../docs/CONFIGURATION.md).

  **The state it reports.** An item that reached `done` or `archived` whose
  FILER never received a verdict mail from the worker. Both halves come from
  macguffin's own store — the landing from `work.done`/`work.archive` in
  `events.jsonl`, the filer from `creator:`, the worker from `polecat-<name>` in
  the result sidecar, the delivery from a message in the filer's mailbox whose
  `From:` is that worker. Against the live store on 2026-08-09 that is **1211
  dropped verdicts against 123 delivered**, ordered oldest landing first, because
  the report exists so a backlog can be RECOVERED and not merely alarmed about.

  **Why the obvious answer — register a schedule against the .py — was wrong.**
  `verdictwatch.py` was already fleet-wide by construction (`--filer` optional,
  `--root` defaulting to the live store); it was never an onethird instrument
  needing generalising. It lived in `research/onethird_program/code/` because
  that is where the investigation ran, **and that is exactly why nothing ran it**
  — code in a research working directory has no runner by construction. A
  schedule reaching into that tree would have made the fleet's verdict-integrity
  detector depend on the layout of an unrelated project's scratch directory: a
  worse coupling than the one it fixes.

  **The family criterion is what it DOES, not what it reads** (pm-pogo's ruling,
  checked against the eight rather than answered from memory): a read-only
  detector that reports a condition and takes no action. check-teardown already
  reads mg state and asks GitHub; check-intake reads GitHub and asks mg;
  check-strandedmail reads the macguffin mail tree; check-acks reads pogod's own
  counters. Four state sources across four siblings. **Report-only is the
  boundary and it is not a stage** — if a future version should FILE the missing
  verdict, that is a different command and it does not join this family.

  **THE ACCEPTANCE CRITERION WAS NOT "the probe runs" BUT "the probe can FAIL",**
  demonstrated on a known-bad and a known-good input. `pogo check-verdicts
  --probe` builds a throwaway macguffin store, drives the REAL `mg` through
  new/claim/done, drops one verdict on purpose and delivers its matched control:

  ```
  known-bad  (worker never mailed the filer) -> DROPPED   (RED)
  known-good (same work, verdict mailed)     -> DELIVERED (GREEN)
  ```

  Same instrument, two inputs, two verdicts. A detector that fired on both would
  be an alarm; one that fired on neither would be decoration.

  **The probe is exercised by something that would notice if a correct change
  killed it — which is the actual lesson, not an incident detail.** The
  original's two constructive probes were killed ~22 hours after landing by
  mg-d639, which made an unknown mail recipient a refusal rather than a silent
  create. That change was CORRECT and would be made again. Nobody noticed for two
  days, because the read-only census was untouched and stayed green — so a reader
  looking at the census could not tell a working detector from a dead one. The
  ported probe runs in `go test ./...`, which `build.sh` runs, which the refinery
  gate runs on **every merge**, so the same breakage now costs one merge. Its two
  outcomes are kept apart deliberately: **mg absent → skip** (as every live
  mg-driven test here always has), **mg present and the fixture will not build →
  FAIL**. A skip on the second would reproduce the original defect inside its own
  repair. Four new `internal/mgcontract` clauses name the mg behaviours the
  fixture rests on — `creator:` frontmatter, the branch in the result sidecar,
  the landing events, the maildir layout — so when mg moves again the red arrives
  once, by name, instead of as an unexplained failure inside a detector's probe.

  **It can say it MEASURED NOTHING, and that is the third answer.** Lose
  `events.jsonl` — renamed, rotated, a root one directory too high — and every
  item reads as never landed, so a careless detector reports "0 dropped" and
  exits 0 over a fleet losing every verdict it has. That case, an unreadable mail
  tree, an unresolvable store, and an *unscoped* scan that judged zero items all
  report INSTRUMENT FAILURE and exit **3**. A scan *scoped* by `--filer`/`--since`
  that matches nothing is a different thing — an answer to the question asked —
  and exits 0 while saying, in words, that it judged nothing. Both siblings did
  exactly this during the 2026-08-09 network outage rather than exiting clean.

  **Two defects were found by running the port against the real store, not by
  reasoning about it.** Side by side with the original it scanned 1575 items to
  the original's 1564: the live store holds **sixteen ids archived under two
  different months**, so the same item is on disk twice and one dropped verdict
  was being reported as two — a backlog report that inflates is one nobody
  finishes. Copies are now collapsed, the count is disclosed rather than
  absorbed, and the preference is declared instead of incidental: the copy
  carrying a result sidecar wins, since preferring the copy that names a worker
  can only move a row UNDECIDABLE → judged, never manufacture a drop. That
  recovered two rows the original left UNDECIDABLE on a glob-order coin flip;
  both were confirmed by hand to have no verdict mail from their recorded worker.
  Second, `--since` is a STRING prefix comparison, so `--since yesterday` sorts
  above every stamp in the store, excludes everything and would exit 0 — a silent
  wrong answer to a reasonable-looking invocation. It is refused as a usage error
  (exit 2) instead.

  **`mg` strips a leading `mg-`**, so a worker signing `mg-ab12` and one signing
  `ab12` are one agent. Matching runs through `mailbox.Canonical` — the same
  function the schedule-registration guard uses — rather than a second
  canonicalizer that can drift by one prefix, which is the bug this whole mail
  lineage keeps rediscovering.

  **WHO RUNS IT, and the cadence deliberately NOT added.** It is on the doctor
  crew agent's sweep, which is exactly where its closest sibling
  `check-strandedmail` runs — pogod schedules the doctor, so this is the same
  footing rather than a new mechanism. A pogod watcher that MAILS on a positive
  finding was considered and rejected **for now**, because the live backlog is
  1211: a detector that opens by delivering twelve hundred findings, every cycle,
  into an inbox that took 1,451 stall-watch mails in one day is a detector its
  reader mutes on day one, and muting it costs more than the port bought. Saying
  so here rather than leaving the absence to be inferred: the mailing watcher
  becomes the right change once the backlog is drained or bounded (`--since` is
  already the tool for the second), and that is a follow-up someone should file,
  not a thing this quietly did not do.

  **The original stays as the audit record.** `verdict_delivery_bf3f/` holds
  OUTCOMES, PREDICTIONS and the statistical finding behind it (verdict loss is
  INSTRUCTION DRIFT, not the reap: 42 of 42 delivering after pm-onethird's
  retrofit against 28 of 152 before, p = 3.9e-24), which is research record and
  is not pogo's to delete. What must not persist is two RUNNABLE copies where one
  is run and one is not, so the port is named in this repo's docs and the
  research-side banner has been mailed to that directory's owner.
