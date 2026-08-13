- **The nightly redeploy now asks whether the box's launchd plists match the
  code it just installed, and mails when they do not (mg-b9e7).** Nothing on any
  machine re-asserts an installed LaunchAgent plist against the one the code
  ships. A merge under `scripts/launchd/` changes nothing until a human runs
  `pogo service install <...>`; there is no boot hook, no login hook, and there
  was no step of the nightly that looked. `pogo doctor --check` has compared them
  since mg-fc99 — but only when somebody runs `doctor`, so between two such runs
  drift is silent. It was silent for **seven days** in the case mg-b201 fixed:
  the box ran a one-fire deploy plist against three-fire code, and every fire the
  installed copy lacked was **inert** — no log line, no failure, nothing
  downstream that could observe its absence.

  **`pogo check-activation`** is that comparison with an exit code and a caller.
  It runs the same audit through the same renderer as the `launchd activation`
  doctor row, so the two surfaces cannot disagree, and exits `0` ACTIVATED,
  `1` DRIFTED, `3` UNKNOWN. `scripts/pogo-self-deploy` calls it every night —
  after the install, after `verify_running` and `verify_orchestration` — and
  escalates a drift through the deploy's existing alert path. It **reports only**:
  it never installs, bootstraps or kickstarts, and it never sets the deploy's
  exit code. Auto-reconciliation stays open, because `pogo service install`
  bounces the daemon and `install-deploy` rewrites this very job's schedule.

  **Where it runs is the point, not a detail.** The plists are Go templates with
  the building binary's constants bound in, so "matches this build" is a claim
  about an expectation. Run an old `pogo` and it reports its own older plist as
  the truth — on 2026-08-07 that build predated the schedule change, so
  "reconciling" from it would have restored the drift and printed success. The
  nightly asks the binary it has just built from `main` and verified, which is
  the one moment in the machine's day when *which build?* has a right answer. The
  build stamp is printed on the report for the same reason.

  **The remedy is subject to the defect it remedies, and two things are done
  about that.** The check ships inside the same binary it audits, so a build
  predating it does not carry it — which is precisely mg-b9e7's second gap, and
  an in-binary fix inherits it.

  - It is a **top-level** command rather than `pogo service check-activation`,
    where it belongs by subject. Measured: `pogo service <unknown>` exits **0**
    and prints help (cobra validates unknown args only on a root command with
    subcommands), while `pogo <unknown>` exits 1. Under `service`, an old binary
    would have answered a scheduled caller with a success — the original defect,
    reproduced by its own remedy. A test asserts this against cobra rather than
    remembering it from a shell session.
  - Nonzero is still not enough, because *unknown command* and *drifted* are both
    exit 1. Every verdict line leads with an `activation:` marker and the nightly
    refuses to read an exit status as a verdict without it. An old binary is
    reported as **NO VERDICT** — its own finding, escalated — never as drift and
    never as clean. The verdict word and the exit status are cross-checked, and a
    disagreement between them is a fifth outcome rather than a tie broken toward
    the friendlier reading.

  **`UNKNOWN` is never a pass**, which is where this command deliberately parts
  company with the doctor row: a plist that was **never installed at all** is a
  doctor `pass` (a person reading the row also reads the sentence naming the job)
  and an exit `3` here (a scheduled caller reads the status alone). Run against
  this box the day it landed, the answer was `DRIFTED` — two of four managed
  plists disagreed with `main`, and `com.pogo.reclaim`, shipped by mg-b7c3, had
  never been installed.

  **What is still uncovered, stated rather than implied:** this reading runs
  inside a deploy, so a box whose nightly has been failing for a week gets no
  report — the same argument that keeps `com.pogo.revisionprobe` outside the
  audit's registry (mg-a03d). And the comparison is bytes-on-disk against this
  build's rendering, so `launchctl print` remains the second read: a plist launchd
  rejected or only partly parsed is still a perfectly good-looking file.
