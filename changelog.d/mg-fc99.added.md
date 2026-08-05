- **`pogo doctor --check` now compares every installed launchd plist against the
  plist the running build would write, so an install that never ran stops being
  invisible (mg-fc99).** New `launchd activation` row.

  mg-8f7e shipped **two artifacts on two separate install paths** — the
  `pogo-deploy.sh` runner and the `com.pogo.deploy` plist — and only the script
  was installed. `deployHours` said `{3,4,5}` and the runner documented 04:00 and
  05:00 as retries for a stalled drain; the plist on the box had `Hour = 3`
  alone, mtime **Jul 28 23:13**, predating both the commit (Jul 31 04:16) and the
  runner (Jul 31 04:51). The retry half of the fix was **inert for five days**
  while the ticket read as closed.

  **Why nothing noticed, and what that forces the check to be.** From inside the
  runner, a retry fire that never happens is *indistinguishable* from a night
  that needed no retry. There is no log line for a fire that did not occur. An
  absence cannot be detected by reading the output of the thing that was supposed
  to be triggered, so the only possible witness is the **installed plist itself**,
  compared against what the shipped code renders. Nothing did that comparison.

  **It is a registry, not a check for `Hour = 3`.** The generalisable defect is
  not the deploy plist: it is a ticket shipping artifacts with **separate
  activation paths**, where "merged" witnesses one and nothing witnesses the
  other. A literal assertion about hour 3 would rot the moment somebody
  re-rendered the plist and would say nothing about the other jobs. So
  `service.AuditLaunchAgents` ranges over every launchd job the package installs
  — `com.pogo.daemon`, `com.pogo.recovery`, `com.pogo.deploy` — and a test fails
  the build if a new plist renderer appears in the package without a registry
  row.

  That generalisation paid on its first real run. On Daniel's machine the new row
  reports **three** stale installs of the same class, not one:

      com.pogo.deploy   installed 03:00, expected 03:00, 04:00, 05:00
      com.pogo.daemon   POGO_HOME=/Users/daniel, expected /Users/daniel/.pogo
      com.pogo.recovery POGO_RECOVERY_DIR absent entirely

  The second is mg-3dc3's POGO_HOME normalisation and the third is the recovery
  plist's env binding — both merged, both never re-installed. A check written for
  the deploy plist alone would have found one of the three.

  **Drift is byte equality, because that is the installer's own predicate.**
  `InstallDeploy`, `InstallRecovery` and `installLaunchd` each rewrite when
  `string(existing) != rendered`, so "stale" here means exactly "re-running the
  installer would change this file" — there is no second, weaker notion of
  up-to-date that could disagree with the thing being audited. On top of that the
  audit **decodes `StartCalendarInterval` on both sides** and reports schedule
  drift as its own fact, because a plist whose log path moved and a plist missing
  two of its three fires are both "stale" and only the second leaves a job that
  is installed, loaded, listed by `launchctl`, and doing a fraction of what the
  code believes.

  **It warns and never fails.** `fail` sets doctor's exit code, and reconciling a
  plist is a machine-local ops action with a blast radius (`pogo service install`
  bounces the daemon). A detector that grows into a gate through the exit code is
  still a gate.

  **Four states, each said out loud.** `ok` / `stale` / `absent` / NOT CHECKED.
  The row renders on every run, and the last two are never phrased as the first —
  a check that reports nothing when it found nothing is invisible in exactly the
  way its subject fails. The audit's stated blind spot: it compares an *installed*
  plist against the code, so it cannot tell a job deliberately left uninstalled
  from one whose install never ran.

  The plist parser is pure Go rather than a shell-out to `PlistBuddy`, so every
  assertion about this detector runs on any platform — a detector whose tests
  only run on the host it protects is one nobody can prove works before shipping
  it. The mg-fc99 fixture is not hand-written: it is rendered through the shipped
  template with a one-element `Hours` list, which is byte-for-byte what the
  pre-mg-8f7e code produced.

  **NOT DONE HERE, and deliberately: the machine's plist was not re-rendered.**
  `pogo service install-deploy` is the remedy and it is Daniel's call; this
  ticket built the detector. The shared acceptance with **mg-0d70** — induce a
  sync failure at the 03:00 fire and observe a successful deploy later the same
  night — remains **UNMET**, and neither ticket can satisfy it alone: nothing
  fires at 04:00 until the plist is re-rendered, and the current retry policy
  treats only exit 7 as retryable and would refuse a sync-class retry until
  mg-0d70 lands.
