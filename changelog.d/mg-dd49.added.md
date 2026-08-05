- **`pogo check-staleness` — two witnesses for a redeploy that did not happen
  and a prompt corpus behind the repo (mg-dd49).**
  Between 2026-07-31 and 2026-08-05 the nightly redeploy did not succeed once.
  Four nights it **never fired** — the box was powered off through each 03:00
  window, and launchd replays a missed `StartCalendarInterval` on *wake* but not
  across a *power cycle*, with `RunAtLoad=false` meaning boot does not stand in
  for it. The fifth night it fired and died one second in on a transient ssh
  failure. **Nothing alarmed on any of the five.** `pogod` served 52-commit,
  6-day-old code, all nine repo-shipped prompts differed from `main`, and every
  polecat dispatched that day ran a superseded template. Both facts were found by
  hand — one by running `ls` on a binary, one by a dispatch eating a 409.

  **A missed run cannot be detected from the deploy's own output.** There is no
  log line for a fire that did not occur, no non-zero exit, no mail. Every
  detector downstream of the runner is blind to the case by construction, which
  is why four nights passed unnoticed under a runner that alerts loudly when it
  fails. So the redeploy half reads an **expectation** — the deploy schedule and
  the clock — against `~/.pogo/deploy-attempt.stamp`, and a night after the
  record's date with nothing in it *is* the signature of a fire that never
  happened. Two reasons, because they send a reader to different places:
  `no-fire` (nothing recorded — look at launchd and the host's uptime) and
  `failed` (a record with a non-zero rc — read `pogo-deploy.log`).

  **`pogo doctor --check` was not lying, and could not have caught the prompt
  half.** Its drift check compares the installed corpus against the running
  binary's **embedded** copy. A missed redeploy stales the binary and the
  prompts *together*, so the two still matched and it passed. That check answers
  "did the installer run since this binary was built"; the question here is "is
  what the fleet reads what the repo ships", and only a comparison against the
  repo can answer it. The prompt half therefore compares `~/.pogo/agents`
  against a **git ref**, reading the object store so it never checks anything out
  and never writes to the reference tree.

  **Neither half consults this binary's own build or embed**, deliberately. A
  staleness alarm that a stale install disables has failed at the first failure
  it exists to catch — the same argument that put the deploy runner out-of-tree
  in mg-42ac/mg-b7d0. The redeploy half reads a text file and a schedule
  constant; the prompt half reads git.

  **Two-sided and content-based.** The decision is a hash of the file body with
  the install stamp stripped. Line counts are printed for a human and consulted
  for nothing: an edit that swaps one line for another is the ordinary shape of a
  prompt change and is invisible to any length test, and the installed tree is
  not simply an older `main`. That last hazard resolves on measurement — the
  `polecat-build-pr.md` "231 installed vs 230 on `main`" recorded in mg-8bcb is
  **the install stamp line itself**; the bodies hash identically
  (`cbd6f88…`), so nothing is ahead of `main` and the bulk-reconcile hazard
  mg-8bcb is parked on does not exist.

  **The reference can itself be stale, and the report says which one it used.**
  `--repo` defaults to the deploy checkout at `~/.pogo/deploy-src`, whose
  `origin/main` is only as fresh as its last successful fetch — and a failed
  fetch is one of the things this command is for. On the box today that mirror
  sits at `b3efaa2` (2026-07-31), five days behind, so the run reports 8 of 9
  stale where a current reference reports 8 of 9 with a different ninth. The
  resolved commit and its date print every run.

  **A check that could not run has not found its subject healthy.** An absent or
  unparseable attempt record, an unresolvable ref and an unreadable prompt are
  all *findings*, never folded into "0 missed nights" or "0 deltas". The strict
  parse is the opposite of `pogo-deploy.sh`'s own reading of the same file, where
  a corrupt stamp degrades to "first attempt of the night" so it costs one extra
  deploy rather than disabling the nightly; here the safe direction is reversed,
  because a stamp this code cannot read is one it cannot vouch for.

  **Both halves were shown to fire.** An alarm that has only ever been silent
  has not been shown to work, and silence is precisely what those five nights
  produced. `--stamp`, `--now`, `--repo`, `--ref` and `--prompts-dir` make both
  states constructible by hand, and both were run on the live box: the four-night
  gap reproduces exactly (`2026-08-01` … `2026-08-04`, all `no-fire`) and goes
  quiet at the same instant against a record dated `2026-08-04`; the live prompt
  corpus reports 8 stale files and the same witness reports `all 9 match` against
  a corpus rebuilt from the ref. The grace boundary is covered too — a night
  still inside the drain budget is not reported, because a witness that fires on
  a deploy that is at that moment succeeding teaches its readers to skip the
  line.

  **The first live run found and fixed a hazard of its own.**
  `~/.pogo/agents/templates/.#polecat.md` is an Emacs lock file — a dangling
  symlink whose extension is `.md` — and reading it took the entire prompt
  witness down with `no such file or directory`, reporting nothing about the nine
  prompts it was there to judge. Dotfiles are now skipped (the installer never
  writes one; editors put lock and swap files nowhere else) and an unreadable
  non-dotfile is reported per-file rather than raised, so one stray file cannot
  silence the detector.

  **Report-only, and not armed by itself.** Like every `check-*` command it never
  installs, rebuilds or reconciles, and nothing runs it on a schedule until
  someone arms it — see `docs/operations.md` for the `pogo schedule` line.

  **Siblings it does not replace.** `pogo service status` covers the running
  daemon's revision. mg-fc99 checks the installed plist's fire hours against the
  schedule the code declares, catching an inert retry *before* a night needs it;
  this command would only see the consequence the next morning. mg-0d70 is about
  a sync failure's alert naming the wrong cause; this command emits no such
  alert and reports a non-zero rc as a non-zero rc.
