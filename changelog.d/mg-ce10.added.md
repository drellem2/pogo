- **The alarm for "the redeploy did not work" was installed by the redeploy.
  Added an external revision probe that is armed by a MERGE instead
  (mg-ce10).** New `scripts/revision-probe.sh`: two reads and no build — the
  running revision from `GET /version`, the reference from the tip of
  `origin/main` — alerting when they have differed for longer than N and naming
  the gap in commits. Documented in `docs/operations.md`.

  **The rule, stated generally: a detector for "X did not happen" must not be
  ACTIVATED BY X.** mg-5bd2 added a positive staleness detector — the right fix
  for the right defect — but all of it landed inside `pogod`
  (`cmd/pogod`, `internal/driftwatch`, `internal/config`), and `pogod` is
  installed by the redeploy. So the alarm was armed only by a redeploy that
  worked, and on a night the deploy fails it is dark for exactly the reason the
  old exit-code proxy was dark on 2026-08-01..08-04: the detector lived inside
  the thing whose absence it was supposed to report. This is the SECOND
  instance — mg-853a hit it and routed around it deliberately — which is why it
  is a rule and not a bug.

  **Why script-side actually fixes it: the activation paths differ.** Tracked
  files in `deploy-src` go live on `git fetch` + `--ff-only` merge, which
  `sync_src` runs before every deploy; the `pogod`/`pogo` binaries go live only
  on a *successful* build and install. A guard against deploy failure must live
  on the merge-activated path. A `git pull` arms this one — no `go install`, no
  build, no redeploy.

  **It touches nothing the deploy provides**, including `jq`. The controls
  poison `go`, `pogo`, `pogod` and `jq` first on `PATH` and assert both that no
  marker was written and that the probe still reached its own verdict; either
  assertion alone passes against a fallback that happens to agree.

  **The reference is read with `git ls-remote`, not `git rev-parse
  origin/main`.** A remote-tracking ref is only refreshed by a fetch, and in
  `deploy-src` the thing that fetches is the deploy runner — so on a night the
  deploy never fires, a probe keyed to that ref compares two stale numbers,
  finds them equal and reports health. Same defect, one layer down. Measured
  against the live box on 2026-08-07 this was not hypothetical: the running
  daemon was `d31297f4` against an `origin/main` of `3fbf3030`, and
  `~/.pogo/deploy-src` did not contain that tip at all — which the probe reports
  rather than swallowing.

  **The divergence clock is keyed on the RUNNING revision.** A changed running
  revision resets it (a deploy did happen — the event being watched for); a
  moved reference does not (`main` advances all day, and a clock it could reset
  would leave the alarm permanently disarmed). Exit `2` — no `curl`/`git`, an
  unreadable checkout, a silent daemon — is a finding, not a shrug, and an
  unreachable daemon is kept distinct from one that answers without naming a
  revision.

  **`driftwatch` is not removed or duplicated.** Inside pogod it answers "what
  am I running?", which is the daemon's own business and useful once live. This
  adds the external witness, because a component cannot be the sole reporter of
  its own absence.
