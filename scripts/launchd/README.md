# pogod launchd Service

This directory contains a launchd plist for running `pogod` as a persistent user agent on macOS. The daemon starts on login and automatically restarts on crash.

## Recommended: Use `pogo service install`

The easiest way to install the service is:

```bash
pogo service install
```

This auto-detects your `pogod` binary path, builds a plist matching the spec below (ProcessType=Interactive, KeepAlive=true, log to `~/Library/Logs/pogo/pogod.log`, PATH/HOME/POGO_HOME/POGO_PLUGIN_PATH wired up), and `launchctl load`s it. The installer is idempotent — rerun it after upgrading pogod or changing the plist template, and it will replace the existing service in place.

If a manually-started `pogod` is already running, the installer stops it first so the launchctl load doesn't collide on the lockfile.

Use `pogo service uninstall` to remove it.

## Running the install detached (for agents)

If the caller of `pogo service install` is itself a child of the running `pogod` — a polecat, a crew agent, a refinery worker — it MUST detach before invoking the installer. The install path stops `pogod` (so `launchctl load` can claim the lockfile and port), and any process that was a child of that `pogod` gets SIGHUP'd and exits mid-install.

Use the built-in `--detach` flag:

```bash
pogo service install --detach
```

This re-execs `pogo` in a new session via Go's `syscall.Setsid`, redirects stdio to `/tmp/pogo-service-install.log`, and exits 0 within ~100ms. The detached child runs the install, restarts `pogod` under launchd, and mails mayor with the result. No `nohup`, `setsid`, or `disown` needed at the call site — and unlike `setsid`, this works on macOS (where `setsid` is not in base or Homebrew).

The caller can return as soon as `pogo service install --detach` exits. **Do not wait on it** — the install will outlive you. Verify completion via mail instead:

- On success, the installer mails mayor with subject `[install] com.pogo.daemon installed and running`.
- On failure, mayor receives `[install] FAILED com.pogo.daemon` with `launchctl print` output and a tail of `~/Library/Logs/pogo/pogod.log` in the body.

The post-install mayor (which is now a child of the new `pogod`) picks up the mail and dispatches a verification polecat without any human in the loop.

## Manual Installation

If you prefer to install the plist manually:

### 1. Customize the plist

Replace `YOUR_USERNAME` in `com.pogo.daemon.plist` with your actual username, and set the `pogod` path correctly:

```bash
which pogod  # confirm location
sed "s/YOUR_USERNAME/$USER/g" com.pogo.daemon.plist > ~/Library/LaunchAgents/com.pogo.daemon.plist
mkdir -p ~/Library/Logs/pogo
```

### 2. Stop any running pogod

If you previously ran `pogo server start` manually, stop it before loading the service to avoid a port/lockfile collision:

```bash
pogo server stop --all
```

### 3. Load the service

```bash
launchctl load ~/Library/LaunchAgents/com.pogo.daemon.plist
```

### 4. Verify it's running

```bash
launchctl list | grep com.pogo.daemon   # should show the service with a PID
curl http://127.0.0.1:10000/health      # should return OK
```

### 5. Smoke test auto-restart

```bash
PID=$(launchctl list com.pogo.daemon | awk '/PID/ {print $3}' | tr -d ';')
kill -9 "$PID"
sleep 5
launchctl list | grep com.pogo.daemon   # PID should be different — launchd restarted it
```

## Plist contract

The plist must include all of these keys for pogod to behave correctly under launchd:

| Key | Value | Why |
|-----|-------|-----|
| `RunAtLoad` | `true` | Start on login. |
| `KeepAlive` | `true` (unconditional) | Restart on any exit, clean or crashing. The older `<dict><SuccessfulExit>false</SuccessfulExit></dict>` form does NOT restart after a clean exit. |
| `ProcessType` | `Interactive` | Prevents App Nap from throttling timers. Without this, macOS coalesces wake-ups for "background" daemons, delaying refinery polling and agent idle detection. |
| `StandardOutPath` / `StandardErrorPath` | `~/Library/Logs/pogo/pogod.log` | macOS-standard location for user-scope app logs; surfaces in Console.app and avoids any collision with arbitrary files at the $HOME root. launchd opens this in append mode, so output accumulates across KeepAlive respawns — crash traces from a prior run survive for post-mortems. pogod rotates the file at startup once it exceeds 10 MiB (`pogod.log.1` = most recent prior chunk, 3 kept), so the previous run's tail is always in `pogod.log` or `pogod.log.1` (mg-6d02). |
| `EnvironmentVariables.PATH` | Includes `~/.local/bin`, `~/go/bin`, `/opt/homebrew/bin`, `/usr/local/bin`, system dirs | pogod spawns claude / git / mg as children; launchd's default PATH does not include these. |
| `EnvironmentVariables.HOME` | User's home dir | launchd sometimes does not set this. |
| `EnvironmentVariables.POGO_HOME` | `~/.pogo` | Where pogo state, agent metadata, and refinery data live. |
| `EnvironmentVariables.POGO_PLUGIN_PATH` | `~/.pogo/plugin` | Where pogod looks for plugins. |

## Managing the Service

| Action | Command |
|--------|---------|
| Load (start) | `launchctl load ~/Library/LaunchAgents/com.pogo.daemon.plist` |
| Unload (stop) | `launchctl unload ~/Library/LaunchAgents/com.pogo.daemon.plist` |
| Restart | `launchctl kickstart -k gui/$(id -u)/com.pogo.daemon` |
| Check status | `launchctl list \| grep com.pogo.daemon` |
| View logs | `tail -f ~/Library/Logs/pogo/pogod.log` |

## Recovery Agent (`com.pogo.recovery`)

A second, independent launchd job — the tier-3 fallback in mg-f5fc's three-tier supervision model. Its sole job is to bounce `pogod` via `launchctl kickstart -k` when something signals it. Independence is the whole point: if `pogod` is wedged, this agent is the only thing that can recover it without human intervention, so it deliberately does NOT depend on `pogod` for anything.

### Files

- `com.pogo.recovery.plist` — plist template (with `YOUR_USERNAME` placeholder).
- `pogo-recovery.sh` — the script launchd runs on each trigger (~80 lines, shell).

### Recommended: `pogo service install-recovery`

```bash
pogo service install-recovery
```

This is intentionally **separate** from `pogo service install`. If the recovery install were folded into the daemon install, a wedged `pogod` would block its own recovery — the very situation tier-3 exists to handle. `pogo service install` prints a one-line nudge pointing at this command after a successful daemon install.

The installer copies `pogo-recovery.sh` to `~/.pogo/bin/`, creates `~/.pogo/recovery/{queue,processed,failed}/`, writes the plist to `~/Library/LaunchAgents/com.pogo.recovery.plist`, and `launchctl bootstrap`s it. Idempotent.

Use `pogo service uninstall-recovery` to remove it. State under `~/.pogo/recovery/` (queue, processed history, failed/, `last_restart`) is left in place so you can inspect post-mortem.

### Signaling a recovery

```bash
pogo recovery request --reason="some explanation"
```

This drops a `.req` file into `~/.pogo/recovery/queue/` using the temp-then-rename pattern (so launchd's `WatchPaths` trigger never reads a partial file). The command exits 0 once the request is enqueued — it does **not** block on the actual restart. Within ~2s, the recovery agent runs, rate-limits, and `launchctl kickstart -k`s `pogod`.

### Plist contract

| Key | Value | Why |
|-----|-------|-----|
| `WatchPaths` | `~/.pogo/recovery/queue` | Edge-triggered: launchd re-runs the script every time the queue dir changes. No polling, no battery drain when idle. |
| `RunAtLoad` | `true` | Drains anything left from a prior login session before the WatchPaths hook arms. |
| `KeepAlive` | `false` | One-shot per trigger. The script always exits 0 once the queue is drained; KeepAlive=true would loop forever. |
| `ProcessType` | `Background` | App-Nap-friendly. The agent reacts to file events, not timers, so unlike `pogod` (Interactive) it doesn't need wake-up fidelity. |
| `StandardOutPath` / `StandardErrorPath` | `~/Library/Logs/pogo/recovery.log` | Same convention as `pogod.log`. |
| `EnvironmentVariables.PATH` | Includes `~/go/bin`, `~/.pogo/bin`, `/opt/homebrew/bin`, system dirs | `launchctl` and `flock` must be reachable. |
| `EnvironmentVariables.HOME` | User's home dir | launchd does not always set this. |
| `EnvironmentVariables.POGO_RECOVERY_DIR` | Parent of the `WatchPaths` dir (`~/.pogo/recovery`) | Must be set. `pogo-recovery.sh` otherwise defaults to `$HOME/.pogo/recovery`, so a `POGO_HOME` pointing elsewhere leaves launchd watching one directory while the script drains another — the job spawns, logs `queue empty`, and drops every request. |

### Recovery script behavior

On each trigger the script:

1. Acquires a non-blocking `flock` on `~/.pogo/recovery/recovery.lock` (if held, exits 0 — another invocation is already draining).
2. Globs `~/.pogo/recovery/queue/*.req`. If empty, exits 0.
3. Reads `~/.pogo/recovery/last_restart` (unix seconds). If `now - last < 60`, leaves queue files in place, schedules a `sleep && touch .tickle` follow-up (so retriggering does not depend on a fresh user write), and exits 0.
4. Runs `launchctl kickstart -k gui/$(id -u)/com.pogo.daemon` once for the whole drained batch.
5. On success, writes `now` to `last_restart` (atomic temp+rename) and moves `*.req` to `processed/` with a timestamp prefix.
6. On failure, moves the requests to `failed/` and exits non-zero.
7. Best-effort prunes archives older than 7 days.

The script never `kill -9`s `pogod` — only `launchctl kickstart -k`. Tier-3 is for *controlled* restarts.

### Manual install (advanced)

```bash
sed "s/YOUR_USERNAME/$USER/g" com.pogo.recovery.plist > ~/Library/LaunchAgents/com.pogo.recovery.plist
mkdir -p ~/.pogo/bin ~/.pogo/recovery/queue ~/.pogo/recovery/processed ~/.pogo/recovery/failed ~/Library/Logs/pogo
cp pogo-recovery.sh ~/.pogo/bin/pogo-recovery.sh
chmod +x ~/.pogo/bin/pogo-recovery.sh
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.pogo.recovery.plist
```

### Managing the recovery agent

| Action | Command |
|--------|---------|
| Bootstrap (start) | `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.pogo.recovery.plist` |
| Bootout (stop) | `launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.pogo.recovery.plist` |
| Trigger a restart | `pogo recovery request --reason="..."` |
| Check status | `launchctl list \| grep com.pogo.recovery` |
| View logs | `tail -f ~/Library/Logs/pogo/recovery.log` |
| Inspect queue | `ls ~/.pogo/recovery/queue ~/.pogo/recovery/processed ~/.pogo/recovery/failed` |

## Nightly Deploy Agent (`com.pogo.deploy`)

A **third** launchd job, and deliberately not a mode of either of the other two.
It fires at 03:00 local — and again at 04:00 and 05:00, as retries — running
`~/.pogo/bin/pogo-deploy.sh`, which decides whether to hand off to
`scripts/pogo-self-deploy redeploy`. **At most one deploy happens per night**:
the later fires exist only so that a run which gave up waiting for the fleet to
quiesce gets another chance before morning, instead of the next attempt being
24 hours away (mg-8f7e).

### Why it has to be a launchd job

`pogo-self-deploy` cannot call itself. Its first line is `assert_out_of_band`
(mg-1bbf): it refuses any caller inside `pogod`'s process tree, because the
`launchctl kickstart -k` it ends with kills that tree — including the caller,
mid-deploy, with nothing left running to report what happened. Every crew agent
and every polecat is such a descendant, so *no agent can ever redeploy*. That is
the gap this job closes: launchd parents it, so it clears the guard by
construction.

Before it existed, the only redeploy path was a human running the script by
hand. Merged `pogod` work sat inert for days at a time — not because anything
was broken, but because nothing scheduled was allowed to run it.

### Why it is NOT `com.pogo.recovery`

Recovery is the tier-3 safety net: it bounces a wedged `pogod` and nothing else
(no build, no install). mg-cf48 examined extending it to redeploy and recommended
**against** it — a deploy holding recovery's lock through `do_prove` silently
drops genuine recovery requests, and recovery's 5-minute stale-lock reclaim would
kickstart a live deploy mid-build. Worse, the two have opposite preconditions:
recovery needs `pogod` unresponsive, the deploy's drain needs it responsive
enough to report, so a deploy on recovery's trigger would refuse in exactly
recovery's design case.

So: two labels, two plists, two logs, two lock directories. Nothing is shared.

### Install

```bash
pogo service install-deploy      # pogo service uninstall-deploy to remove
```

Idempotent. It does **not** clone the build checkout — the runner does that on
its first real run, keeping a network operation out of an install an operator
may be running because the box is already unhealthy.

### What the runner does (and refuses to do)

`pogo-deploy.sh` is a trigger, not a deployer. Every gate that matters already
lives in `pogo-self-deploy`; anything here that starts to look like deploy logic
belongs there instead. In order:

1. **Window guard.** If the local hour is outside `[02:00, 06:00)`, log the
   reason and exit 0. `StartCalendarInterval` does not promise 03:00 — it
   promises the job *runs*. A mac asleep at 03:00 gets the fire delivered on the
   next wake, which may be 09:14 when Daniel opens the lid, or 14:30 mid-demo. A
   redeploy bounces the whole fleet, so a deferred fire is dropped rather than
   honoured late. The window is a **range** and not an instant for the opposite
   failure: too narrow and the job never deploys at all, which looks identical
   to a job that was never installed. It widened from `2-5` to `2-6` with
   mg-8f7e, because the drain budget is now derived from the distance to the
   window's end — the window's width *is* the deploy's patience.
2. **Lock.** `~/.pogo/deploy.lock.d`, its own, never recovery's. A redeploy can
   legitimately run for two hours while the drain waits on polecats; a second
   fire must not start a competing drain. With three fires a night this is no
   longer a rare case — it is the normal outcome when the 03:00 attempt is still
   draining at 04:00, and the right one.
3. **Retry gate** (mg-8f7e). The night's outcome is recorded in
   `~/.pogo/deploy-attempt.stamp` as `<date> <attempts> <last_rc>`. A later fire
   proceeds only when the earlier one exited **7** (drain stalled) — the one
   failure a retry can fix, because it is a statement about how busy the fleet
   was and nothing was built or bounced. Every other outcome settles the night:
   re-running a failed build reproduces the failure and mails a duplicate alert.
   An absent or unparseable record reads as "first attempt", so a corrupt stamp
   costs at most one extra attempt rather than silently disabling the nightly.
4. **Drain budget** (mg-8f7e). `--drain-timeout` is computed, not fixed:
   `(seconds until the window closes) - POGO_DEPLOY_RESERVE`, capped at
   `POGO_DEPLOY_MAX_DRAIN`. A 03:00 fire gets 2h where the script's own default
   would have given 30 minutes. If under `POGO_DEPLOY_MIN_DRAIN` remains, the
   fire skips entirely — a drain that cannot finish has still stopped dispatch
   for its whole length and delivered nothing.
5. **Tools.** `mg`, `pogo` and `git` are resolved to **absolute paths**, and `mg`
   and `git` only after the candidate proves itself by **running**. On macOS
   `/usr/bin/mg` is the Micro-Emacs editor; it satisfies `command -v mg`, panics
   headless, and delivers no alert at all (mg-015f, mg-dd5f). The alert path is
   resolved *first*, before anything that can fail — a job whose first failure is
   "I cannot tell you about failures" is the silent nightly all over again.

   `git` was the one primitive still trusted on sight, pinned to `/usr/bin/git`;
   it is now checked by execution like the others (mg-b72a). That path is the
   Xcode Command Line Tools shim, and a damaged install behind it makes it fail
   *every* call — `git --version` included — with `unable to locate xcodebuild`
   and exit 71, while staying executable and on `PATH`. So each candidate must
   print `git version` before it is accepted, a real Homebrew/local git is
   preferred, and the shim stays in the list so a CLT-only box still deploys.
   `GIT=` pins one explicitly and is health-checked too. **No such breakage has
   been reproduced here** — this is a consistency change, and on a host whose
   only git is a healthy `/usr/bin/git` its whole effect is that a future broken
   shim would abort once with an alert instead of failing separately inside every
   `git` call in `sync_src`.
6. **`GH_TOKEN` at run time**, matched out of `~/.zshenv` one line at a time and
   `eval`'d alone — never sourced wholesale (that file's `export PATH=` would
   strip `go` and reproduce the 07-23 `go: command not found` failure), and
   **never** in the plist: `~/Library/LaunchAgents` is world-readable. The value
   is never logged.
7. **Safe sync** of `~/.pogo/deploy-src` — a **dedicated** checkout, never
   `~/dev/pogo`. The dev tree is a place a human works; a 03:00
   fetch/checkout/merge there can land on a half-finished edit or an in-progress
   rebase. Even in the dedicated tree a **dirty** working copy aborts rather than
   resets (a reset would destroy the evidence of whatever made it dirty), and a
   **diverged** tree aborts too — `--ff-only`, because merging would deploy
   commits nobody meant to build.
8. **Drift gate.** `pogo-self-deploy check` is read-only and never acts. If its
   verdict is `clean`, log it and exit 0 **without bouncing**: a fleet-wide
   bounce costs every agent its session, and doing it for a no-op is strictly
   worse than not running. The verdict is reused rather than recomputed here so
   this stays a trigger and not a rival deployer with its own idea of "current" —
   notably, `check`'s notion of clean already covers CLI drift (mg-ddf1).
9. **Redeploy**: `pogo-self-deploy redeploy --yes --drain-timeout <budget>`.
   `--yes` because `confirm()`
   exits 3 without a tty. **Never `--force`** — the two things it overrides are
   killing live polecats and bouncing a fleet whose idleness could not be
   established, and neither is a call an unattended 03:00 job gets to make. The
   flag is not passed, not plumbed, and not settable by env; `pogo-deploy_test.sh`
   asserts it.
10. **Outcome.** Exit 0 → wait out a grace period and re-read the mail-check
   schedules, alerting on any that existed before the bounce and did not come
   back. Non-zero → mail `pm-pogo` **and** `human`, and stop — unless a retry is
   really coming, in which case the attempt emits an event and the mail waits
   for the night's last chance (three identical REDs would be the cost of having
   retries at all).

   Each code names a different operator response, and the alert now carries the
   **remedy for the code it actually got** (mg-8f7e). It used to carry one
   paragraph, about exit 9, under every code — so the 07-31 exit 7 told its
   reader the control suite had gone RED and "the artifact is the problem", for
   a failure that never reaches the build. The advice that followed was right by
   accident, which is worse than wrong: a reader who trusts the reasoning goes
   and reads a build log that does not exist. **Exit 9 is `do_prove` RED**, which
   runs after the build and *before* the kickstart — the running `pogod` was
   never touched, so it is a clean negative control rather than an outage.
   **Exit 7 is a stalled drain** — nothing was built and nothing was bounced, and
   it is a statement about how long the fleet's work takes, not about the
   artifact. Neither is a reason to retry with `--force`.

### Plist contract

| Key | Value | Why |
|-----|-------|-----|
| `StartCalendarInterval` | **array** of `Hour=3, 4, 5` at `Minute=0` | 03:00 local, the off-hours window disruptive fleet ops were already moved into, plus two retry fires (mg-8f7e). A `StartInterval` would fire relative to *load* time, so the "nightly" would drift into the working day every time someone reinstalled it. The later fires are retries, not extra deploy opportunities: the runner's retry gate settles the night on anything but a stalled drain, and a fire that lands while an attempt is still running takes no lock and exits 0. Every hour must sit inside the runner's window guard — `internal/service` asserts it. |
| `RunAtLoad` | `false` | Installing or reloading the job must never bounce the fleet as a side effect. This is the one key that differs in spirit from `com.pogo.recovery`, where `true` is harmless. |
| `KeepAlive` | `false` | One-shot per fire. The runner exits on every no-drift night; `true` would relaunch it in a loop. |
| `ProcessType` | `Background` | It reacts to a clock, not to interactive latency. |
| `StandardOutPath` / `StandardErrorPath` | `~/Library/Logs/pogo/pogo-deploy.log` | Its own log. Deploy history must not be interleaved with recovery bounces. |
| `EnvironmentVariables.PATH` | Same value as `com.pogo.recovery` (via `launchdPath()`) | Must resolve **both** `go` (`/opt/homebrew/bin`) and `mg`/`pogo` (`~/go/bin`). launchd's default PATH has neither, and the 07-23 manual redeploy died on `go: command not found` for exactly this. |
| `EnvironmentVariables.POGO_DEPLOY_SRC` | `~/.pogo/deploy-src` | Bound here so the plist and the script cannot disagree about which tree is built. Must never name a developer working tree. |
| `EnvironmentVariables.GH_TOKEN` | **absent, deliberately** | `~/Library/LaunchAgents` is world-readable. The runner sources the token at run time instead. |

### Runner env overrides

All optional; the defaults are the production values.

| Variable | Default | Purpose |
|----------|---------|---------|
| `POGO_DEPLOY_SRC` | `~/.pogo/deploy-src` | The dedicated build checkout. |
| `POGO_DEPLOY_REMOTE` | origin of `~/dev/pogo` | Clone URL, used once to bootstrap the checkout. |
| `POGO_DEPLOY_WINDOW` | `2-6` | Half-open local-hour range in which a fire is honoured. Also the source of the drain budget: the width of this window is how patient the deploy is allowed to be. |
| `POGO_DEPLOY_SKIP_WINDOW` | unset | `1` bypasses the window guard. For controls only. |
| `POGO_DEPLOY_NOW` | unset | `HH` override for the clock (window guard and drain budget). Minutes and seconds read as zero under it. Tests only. |
| `POGO_DEPLOY_DATE` | unset | `YYYY-MM-DD` override for which night a fire belongs to. Tests only. |
| `POGO_DEPLOY_RESERVE` | `1200` | Seconds of the window held back for the build, `do_prove`, the kickstart and verification — everything the run still owes after the drain returns. |
| `POGO_DEPLOY_MAX_DRAIN` | `7200` | Ceiling on one drain. Nothing dispatches while `draining=true`, so unbounded patience trades a missed deploy for a night of no work. |
| `POGO_DEPLOY_MIN_DRAIN` | `600` | Floor. Below this a fire skips rather than start a drain it cannot finish. |
| `POGO_DEPLOY_FIRE_HOURS` | `3 4 5` | The plist's fire hours, ascending. Used only to answer "will a retry follow?" so the RED alert can say so truthfully. |
| `POGO_DEPLOY_STAMP` | `$POGO_HOME/deploy-attempt.stamp` | Where the night's outcome is recorded for the retry gate. |
| `POGO_DEPLOY_GRACE` | `120` | Seconds before the post-bounce mail-check re-read. |
| `POGO_DEPLOY_ZSHENV` | `~/.zshenv` | Where `GH_TOKEN` is read from. |
| `GIT` | first candidate that prints `git version` | Pins a specific git. Still checked by execution — a pin that cannot run is the same outage as no pin. |
| `POGO_DEPLOY_ALERT_TO` | `pm-pogo` | First alert recipient; `human` is always copied. |

### Managing the deploy agent

| Action | Command |
|--------|---------|
| Install / reinstall | `pogo service install-deploy` |
| Remove | `pogo service uninstall-deploy` |
| Check status | `launchctl list \| grep com.pogo.deploy` |
| Run it now, gates and all | `POGO_DEPLOY_SKIP_WINDOW=1 ~/.pogo/bin/pogo-deploy.sh` |
| Rehearse without deploying | `POGO_DEPLOY_SKIP_WINDOW=1 ~/.pogo/bin/pogo-deploy.sh --dry-run` |
| View logs | `tail -f ~/Library/Logs/pogo/pogo-deploy.log` |

Note that running it by hand from a terminal is **out of band** and therefore
allowed — but it is also not the environment it runs in nightly. To reproduce
the launchd environment, use `env -i PATH=<the plist's PATH> HOME=$HOME`; an
interactive shell hides exactly the two failures (`go` off PATH, `GH_TOKEN`
missing) this job was shaped around.

## One-Shot Stamp Fix (`com.pogo.stampfix`)

A **fourth** job, and the only deliberately **disposable** one. It exists to
break a single deadlock **once**, and it deletes itself on the way out.

### The deadlock

Every deployed binary carried a vcs stamp naming a commit from a *different*
repository:

```
installed pogod: c28e26e28921...  <FOREIGN — no such commit in ~/.pogo/deploy-src>
installed pogo : c28e26e28921...  <FOREIGN — no such commit in ~/.pogo/deploy-src>
```

`pogo-self-deploy redeploy` runs `classify_drift` before it does anything, and
`classify_drift` returns 1 on a foreign stamp — so `cmd_redeploy` exits at the
gate, **before** reaching the `go install` that is the only thing which would
clear that stamp. `--force` and `--skip-drain` both act *after* the gate, which
is why neither helps. The nightly `com.pogo.deploy` aborted on this every night
from 2026-07-29 onward.

The break is to run the `go install` **outside** the script, so that by the time
`redeploy` runs, the stamp it would have refused is already gone. Nothing in
`pogo-self-deploy` is patched or bypassed — it is handed a tree it accepts.

### How the stamp got foreign in the first place

A `go install` run from a **polecat worktree**. A worktree's `.git` is a *file*
(a gitdir pointer), and `go build` does not follow it — it walks *up* until it
finds a real `.git` directory, finds `~/.pogo`, and stamps that repo's commit.
Measured directly:

```
$ cd ~/.pogo/polecats/3888 && go build -o /tmp/pogod ./cmd/pogod
$ go version -m /tmp/pogod | grep vcs.revision
    vcs.revision=e957ced765fd...   # <- ~/.pogo's HEAD
$ git rev-parse HEAD
e7d13f0c654b...                    # <- the worktree's actual HEAD
```

So the runner asserts `$POGO_DEPLOY_SRC/.git` is a real **directory** rather than
trusting it, and refuses outright on a worktree. **Never build or install pogo
binaries from a polecat worktree.**

### Why the install and the redeploy are one job

Splitting them opens the mg-49bc protocol-mismatch window. `go install` rewrites
both binaries but bounces nothing, so between the install and the restart the
on-disk `pogo` CLI is new while the running `pogod` is old, and new subcommands
404 against it. Doing the install now and leaving the restart to the 03:00
nightly would hold that window open for hours. They run back to back, with only
the stamp verification between them.

### Why it is a launchd job and not an agent

Same reason as `com.pogo.deploy`: `assert_out_of_band` refuses any caller that is
a descendant of `pogod` **or** has `POGO_AGENT_NAME` set. Every agent is both. A
LaunchAgent is parented by launchd and carries neither, so it passes the guard
*by construction* — and it survives the `launchctl kickstart -k` for the same
reason, because launchd owns it and `pogod` does not. An agent authors and
installs this job; **launchd runs it**.

The runner refuses an agent caller *up front*, before the `go install`, so a
wrong caller changes nothing at all. (Refusing afterwards would leave the
binaries rewritten with no restart coming — the mismatch window, held open.)

### One-shot, and fail-closed

- **One-shot.** A sentinel (`~/.pogo/stampfix-mg-3888.ran`) is written **before**
  the redeploy, and any later fire that sees it exits 0. Before rather than after
  on purpose: the redeploy takes minutes and bounces the fleet, and a second
  calendar fire landing mid-bounce must not start a second one. The cost is that
  a *failed* run does not retry itself — the safe direction, since a repeating
  fleet-wide bounce is worse than a stalled one.
- **Fail-closed.** The fleet is bounced only if the stamp is *verifiably* fixed.
  If `go install` succeeds but the binaries still name a commit `deploy-src` does
  not have, the runner exits without redeploying: a bounce would cost the fleet
  and leave the deadlock in place anyway.
- **`--skip-drain` is required, not a convenience.** The daemon being replaced
  predates `/agents/drain`, and `pogo-self-deploy`'s drain precondition turns that
  answer into a hard `exit 6`.

### If the redeploy fails after a successful install

The window is open but **not indefinitely**, and this is the reassuring property
of the ordering: once the stamp is cleared the deadlock is broken *permanently*.
`classify_drift` stops returning 1, so the 03:00 nightly — which had been failing
at exactly that gate — reclassifies as `RESTART owed` and finishes the restart on
its own. A failed redeploy degrades to "the nightly completes it tonight", not
"stuck until a human intervenes".

Rollback pair (the previously running 0.5.0 binaries):
`~/.pogo/rollback-pogo-2026-07-30/{pogo,pogod}`.

### Install

The fire time is set a few minutes into the future, because **the installing
agent must be gone before the job runs** — the redeploy ends in a
`launchctl kickstart -k` that kills `pogod`'s entire process tree, including
whichever agent installed this. That is expected, and is why the fire is
scheduled rather than immediate (`RunAtLoad` is `false`).

```bash
mkdir -p ~/.pogo/bin ~/Library/Logs/pogo
cp scripts/launchd/pogo-stampfix.sh ~/.pogo/bin/pogo-stampfix.sh
chmod +x ~/.pogo/bin/pogo-stampfix.sh

# Fire 5 minutes out, in LOCAL time (StartCalendarInterval is local).
sed -e "s/YOUR_USERNAME/$USER/g" \
    -e "s/FIRE_HOUR/$(date -v+5M +%H)/" -e "s/FIRE_MINUTE/$(date -v+5M +%M)/" \
    scripts/launchd/com.pogo.stampfix.plist > ~/Library/LaunchAgents/com.pogo.stampfix.plist
plutil -lint ~/Library/LaunchAgents/com.pogo.stampfix.plist
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.pogo.stampfix.plist
```

`plutil -lint` is in the snippet because an unsubstituted placeholder is the one
mistake here that produces a job which simply never fires: the literal
`FIRE_HOUR` lints as `Hex digit in non-hex <integer>` (its `F` and `E` are hex
digits), so linting turns a silent no-op into an install-time error. The
zero-padded output of `date +%H` needs no stripping — `<integer>09</integer>`
parses as `9`, not as octal (verified with `plutil -extract`).

### Rehearsing it

A one-shot is otherwise a script whose first execution is also its only test, and
this one's untested half ends in a fleet-wide bounce. `POGO_STAMPFIX_DRY_RUN=1`
runs everything that *can* be rehearsed — tool resolution, the worktree and
dirty-tree refusals, the `go install`, and the fail-closed stamp verification —
then stops at the bounce and prints the command it would have run. Point `GOBIN`
at a scratch directory and it touches nothing the fleet uses:

```bash
git clone ~/.pogo/deploy-src /tmp/stampfix-rehearsal
mkdir -p /tmp/stampfix-gobin
env -u POGO_AGENT_NAME GOBIN=/tmp/stampfix-gobin \
    POGO_DEPLOY_SRC=/tmp/stampfix-rehearsal \
    POGO_STAMPFIX_DRY_RUN=1 ~/.pogo/bin/pogo-stampfix.sh
```

It writes no sentinel, sends no mail and removes no plist — a rehearsal that
armed the one-shot guard would block the real run it was meant to de-risk.

Two things make the rehearsal usable *and* safe:

- **The caller check is waived in dry-run, and only there.** It exists because a
  caller inside `pogod`'s tree cannot survive the bounce to finish the second
  step; dry-run performs no bounce, so there is no second step to be cut off
  from — and the agent authoring this job is exactly who needs to rehearse it.
- **`GOBIN` must not be the live one.** Dry-run skips the bounce but still runs a
  *real* `go install`, so with a default `GOBIN` it would land on top of the
  running `pogod` — a "rehearsal" that silently performs the riskiest half of the
  real thing. The runner compares the install target against the directory the
  live `pogod` was exec'd from and refuses:

      ERROR: DRY RUN refusing: the install target is /Users/you/go/bin, which is
      where the RUNNING pogod lives.

  Docs telling you to set `GOBIN` are not a mechanism; that check is.
  `POGO_STAMPFIX_ALLOW_LIVE_GOBIN=1` overrides it when overwriting them is
  genuinely intended.

### Runner env overrides

| Variable | Default | Purpose |
|----------|---------|---------|
| `POGO_DEPLOY_SRC` | `~/.pogo/deploy-src` | The checkout to build. Must have a real `.git` **directory**. |
| `POGO_STAMPFIX_DRY_RUN` | unset | Do everything except the bounce. |
| `POGO_STAMPFIX_SENTINEL` | `~/.pogo/stampfix-mg-3888.ran` | One-shot marker. |
| `POGO_STAMPFIX_ALERT_TO` | `mayor` | Coordinator mailbox (`human` is always mailed too). |
| `POGO_STAMPFIX_ALLOW_LIVE_GOBIN` | unset | Let a dry-run install over the live binaries. |
| `POGO_STAMPFIX_NO_SELF_REMOVE` | unset | Keep the job loaded after it runs. |
| `POGO_STAMPFIX_PLIST` | `~/Library/LaunchAgents/com.pogo.stampfix.plist` | What self-removal deletes. |

### Managing it

| Action | Command |
|--------|---------|
| Check status | `launchctl list \| grep com.pogo.stampfix` |
| View logs | `tail -f ~/Library/Logs/pogo/pogo-stampfix.log` |
| Cancel before it fires | `launchctl bootout gui/$(id -u)/com.pogo.stampfix && rm ~/Library/LaunchAgents/com.pogo.stampfix.plist` |
| Re-arm after a failed run | `rm ~/.pogo/stampfix-mg-3888.ran`, then re-install |
| Confirm the fix landed | `~/.pogo/deploy-src/scripts/pogo-self-deploy check --repo ~/.pogo/deploy-src` |

Once it has run successfully this job has no further purpose: it removes its own
plist and boots its label out. The sentinel is left behind on purpose, as the
record that it ran.
