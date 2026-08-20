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
| `EnvironmentVariables.POGO_LOG_LEVEL` | unset (commented out in this template) | Log level for pogod's indexing, diagnostics and project loggers — `trace`, `debug`, `info` (default), `warn`, `error`, `off`. Must be declared in a plist: launchd does not pass the invoking shell's environment to a job, so `POGO_LOG_LEVEL=debug pogo server start` cannot reach a launchd-managed daemon. Unload and load the job after changing it; launchd hands a job its environment at spawn. **`pogo service install` does not carry this key** — it renders its own template and overwrites the installed plist, so on that path add the key to `~/Library/LaunchAgents/com.pogo.daemon.plist` directly and re-add it after every re-install. Uncommenting it in *this* file only affects the manual `sed` install below. Does not cover `internal/driver`'s plugin loggers, which are built at `debug` independently. |

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
24 hours away (mg-8f7e). They are **not** the mitigation for a connectivity
outage — three instants two hours apart are swallowed whole by an outage of the
length this box has actually produced. That is the vigil's job; see gate 7.

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

**A merged change to `scripts/launchd/pogo-deploy.sh` is not live until it is
installed.** launchd executes `~/.pogo/bin/pogo-deploy.sh` — a static copy that
nothing refreshes, not the nightly and not a `pogo` upgrade. Twice now a runner
fix has sat on `main` while 03:00 ran the pre-fix file (mg-bcc1 2026-07-29,
mg-45b9 2026-08-19; the second was mg-9fc9's fleet bounce, three revisions
behind). Nothing detects it either: `pogo doctor` compares the **plist** against
the shipped template and says nothing about the runner's contents. Check it by
hand, and believe the hashes rather than a command that reported success:

```bash
git hash-object scripts/launchd/pogo-deploy.sh ~/.pogo/bin/pogo-deploy.sh
git hash-object scripts/lib/net-control.sh     ~/.pogo/bin/net-control.sh
```

Two files, because `install-deploy` ships the runner and its positive-control
library as a pair and a divergence in how they are found is how one of them
silently stops being shipped.

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

   A sync failure is **classified before it is blamed** (mg-0d70): the runner
   measures whether the remote's host:port answers a TCP connection rather than
   reading git's English, and only the classes that established *nothing about
   the tree* (`network`, `remote`, `unclassified`) are retried. Those retry in
   **three tiers**, and the third is the one sized for a real outage:

   | Tier | Patience | Bound |
   |------|----------|-------|
   | Blip (mg-0d70) | 4 attempts at 15s / 45s / 120s | `POGO_DEPLOY_SYNC_ATTEMPTS`, `POGO_DEPLOY_SYNC_RETRY_BUDGET` |
   | Vigil (mg-5515) | a probe every 300s, indefinitely | the **window**: it stops when a drain could no longer finish (≈05:30) |
   | Cross-fire (mg-8f7e) | the 04:00 and 05:00 fires | exit 10 reopens the night |

   The vigil exists because the first and third tiers are both sized for a blip.
   Three minutes of backoff and three fire *instants* two hours apart spend, in
   total, about nine minutes of a four-hour window — and the one outage measured
   on this box ran **2h50m** (2026-08-07, 13:24:30Z → 16:14:52Z), which swallows
   all three fires. No agent can cover that: an outage of that shape takes every
   agent on the box out at once. Re-spacing three instants cannot cover 170
   minutes either, and widening the window to fit more of them would lengthen
   every drain and push the fleet bounce toward the working day. So the vigil
   spends the window's *unused* time instead: it keeps probing and deploys the
   moment connectivity returns.

   Two things follow. A vigil run **holds the lock for hours**, so the later
   fires find it held and exit 0 (correct — they would have failed identically),
   and the vigil refreshes the lock's mtime so the stale-lock reclaim cannot fire
   under a live run. And `SYNC_VIGIL_SPENT` — reported in the log, the alert and
   the recovery notice — is a probed **lower bound on the outage duration**;
   mg-5515 had exactly one such measurement and no distribution.

   **What it does not reach.** The vigil's reach is 2h30m from the 03:00 fire
   (3h30m from a 02:00 wake-fire), which is shorter than the 2h50m that prompted
   it. An outage of exactly that length starting at exactly 03:00 still costs the
   night, and no patience fixes that — it ends at 05:50 and `POGO_DEPLOY_RESERVE`
   alone is 20 minutes. Saving it needs a **wider window**, which is deliberately
   not chosen here: there is no distribution to size one from. Set
   `POGO_DEPLOY_SYNC_VIGIL=0` to restore the pre-mg-5515 bound.

   **7b. And when the transport is out for N nights running, bounce the fleet
   anyway** (mg-9fc9). Everything above makes the deploy more patient about a
   network it needs; none of it helps on the night the network never comes back.
   The nightly deploy is this box's only *automatic* recovery path — it restarts
   the fleet, and a restart is what clears a wedged agent — so an outage that
   takes the deploy out takes recovery out with it. On 2026-08-15..19 that cost a
   **118-hour blackout**: five nights, `ssh: Could not resolve hostname
   github.com` on every one, and recovery only when a human typed a message.

   A restart needs no remote. So after `POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER`
   (default **2**) consecutive nights lost at the TRANSPORT step — and only once
   the night's last fire is spent — the runner calls `pogo-self-deploy bounce`,
   which is `redeploy` with the fetch, the build and `do_prove` removed. It
   delivers **no code**; it ends a blackout the agents were only in because
   nothing had restarted them.

   | | |
   |---|---|
   | Counted | `network`, `remote`, `unclassified`, `timeout` — the classes where the sync never reached the tree. |
   | Not counted | `dirty`, `diverged`, `checkout` **clear** the streak (they are read after a successful fetch, so the transport worked); `config` **leaves** it (it fails before any network call). A bad tree must never accumulate toward a fleet bounce. |
   | Unit | **nights**, not fires — the count is idempotent per date, so three fires of one bad night cannot cross a threshold of two. |
   | Record | `~/.pogo/deploy-transport-streak.stamp`, `<date> <count> <last-bounce-date>`. Unreadable reads as no streak, so a corrupt file delays a bounce and cannot invent one. |
   | Window | its own reserve, `POGO_DEPLOY_BOUNCE_RESERVE` (300s), **not** the deploy's 1200s. The vigil probes until the *deploy's* budget hits zero, so charging the bounce the deploy's reserve would starve it on exactly the nights it exists for. |
   | Drain | the same gate, and `--force` is **refused** by `bounce`. A polecat holding commits that exist only in its worktree stops the bounce; that is reported, not overridden. |
   | Announcement | a mail to `$POGO_DEPLOY_ALERT_TO` and `human` out of the **local** maildir, plus a `deploy_transport_fallback` event. On the night this fires, the network is what is broken. |
   | After a bounce | the streak resets, so a week-long outage bounces once every N nights rather than every night. A refused bounce does **not** reset it. |

   **Has it ever fired? Not on this box — but the path is driven end to end in
   the suite** (mg-62eb). The question was open for a real reason: on 08-17,
   08-18 and 08-19 the nightly aborted at the transport with class
   `unclassified` (a **counted** class) and the streak stamp was *absent*, with
   zero bounce events. Those three nights predate the mechanism — it merged
   11:27Z on 08-19 and the installed runner did not carry it until 12:00Z — so
   they are evidence about nothing. What they left behind was a fallback that
   had never run, and 08-20's deploy *succeeded*, which exercises the streak's
   **clear** and not its bump.

   `scripts/pogo-deploy_test.sh` now drives the real `main()` through four arms:
   a fresh box plus a transport failure writes `<today> 1 -`; a second
   consecutive night carries it to `2`; at the threshold the fallback invokes
   `pogo-self-deploy bounce --yes --drain-timeout N` and the completed bounce
   resets the count and stamps the date; and a night whose sync *reaches the
   tree* clears to 0 without considering the fallback at all. Before that, the
   wiring was covered only by greps of this runner's source for the **line
   number** of the `fallback_bounce` call — assertions about where text sits in
   a file, which cannot tell a wired call from one whose branch is never
   reached. (`fallback_bounce` itself was unit-covered all along; what was not
   was the sync-abort path reaching it with a count it had derived.) What is still untested is whether a real `bounce` unwedges agents
   stuck the way the August fleet was; the suite asserts the invocation, not the
   cure.

   Set `POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER=0` to disable the fallback entirely.
   Two couplings survive and are documented at `pogo-deploy.sh` section 5c: the
   state this fires in makes a drain refusal *more* likely (a wedged polecat that
   committed without pushing is what the drain refuses to orphan), and the
   fallback lives inside this job, so a fault that stops the job firing at all
   takes it too — that one is `internal/staleness/nofire.go`'s question, not this
   one's.
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
   asserts it. (`bounce`, the mg-9fc9 fallback at 7b, goes further and *refuses*
   `--force` outright — it exists to restart a fleet safely, and a flag that is
   merely "not passed" is one edit away from being passed.)
10. **Outcome.** Exit 0 → wait out a grace period and re-read the mail-check
   schedules, alerting on any that existed before the bounce and did not come
   back. Non-zero → mail `$POGO_DEPLOY_ALERT_TO` **and** `human`, and stop — unless a retry is
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
| `POGO_DEPLOY_FIRE_HOURS` | unset — **derived** | PINS the fire hours instead of reading them. Production leaves it unset: the runner reads its hours from the LOADED launchd job (`launchctl print`), falling back to the plist file, so the list it answers "will a retry follow?" with cannot drift from the schedule it describes. It used to default to `3 4 5`, drifted from a plist carrying one 03:00 fire, and made the RED alert promise retries that did not exist (mg-fc99 / mg-8dcb). |
| `POGO_DEPLOY_LABEL` | `com.pogo.deploy` | The launchd label whose schedule is read. |
| `POGO_DEPLOY_PLIST` | `~/Library/LaunchAgents/com.pogo.deploy.plist` | The plist file cross-checked against the loaded job. A disagreement means the file was edited and never reloaded, and is reported. |
| `POGO_DEPLOY_LAUNCHCTL` | `/bin/launchctl` | Pin a `launchctl`. Controls only. |
| `POGO_DEPLOY_PLISTBUDDY` | `/usr/libexec/PlistBuddy` | Pin a `PlistBuddy`. Controls only; without it the reader falls back to `plutil`. |
| `POGO_DEPLOY_STAMP` | `$POGO_HOME/deploy-attempt.stamp` | Where the night's outcome is recorded for the retry gate. |
| `POGO_DEPLOY_SYNC_ATTEMPTS` | `4` | Blip-tier sync attempts on a class that established nothing. |
| `POGO_DEPLOY_SYNC_BACKOFF` | `15 45 120` | Blip-tier delays, seconds. The last value repeats, so a shortened list degrades into a constant rather than a hammer at zero. |
| `POGO_DEPLOY_SYNC_RETRY_BUDGET` | `300` | Ceiling on total **blip-tier** sleeping. Does not bound the vigil — the window does. |
| `POGO_DEPLOY_SYNC_VIGIL` | `1` | `0` disables the vigil tier, restoring the pre-mg-5515 bound exactly. |
| `POGO_DEPLOY_SYNC_VIGIL_INTERVAL` | `300` | Seconds between vigil probes. Flat, not geometric: a ramp would sleep through the recovery it exists to catch. |
| `POGO_DEPLOY_PROBE_TIMEOUT` | `5` | Seconds the reachability probe waits before reporting `unclassified`. |
| `POGO_NET_CONTROL_LIB` | beside the runner, then `../lib/` | Where the positive-control library is loaded from. If nothing is found the runner reports `net_control=unknown` and names the paths it tried; it never falls back to interpreting a one-endpoint probe. |
| `POGO_NET_CONTROL_TARGETS` | `1.1.1.1:443 8.8.8.8:443 9.9.9.9:443` | The control's reference set, by literal IP. Three operators on three networks: with one target a dead target and a dead box are the same observation. Empty disables the arm. |
| `POGO_NET_CONTROL_NAME_TARGETS` | `one.one.one.one:443 dns.google:443` | The same operators reached by NAME, so an IP-up/DNS-down box gets its own line instead of being folded into a verdict. |
| `POGO_NET_CONTROL_TIMEOUT` | `5` | Per-probe seconds for the control. |
| `POGO_NET_CONTROL_MIN_TARGETS` | `2` | Targets that must be probed before `down` is available at all. |
| `POGO_DEPLOY_STALE_LOCK_MIN` | `180` | Minutes after which a lock is reclaimed. The vigil refreshes the lock's mtime, so this means "no run has made progress in 180min". |
| `POGO_DEPLOY_GRACE` | `120` | Seconds before the post-bounce mail-check re-read. |
| `POGO_DEPLOY_ZSHENV` | `~/.zshenv` | Where `GH_TOKEN` is read from. |
| `GIT` | first candidate that prints `git version` | Pins a specific git. Still checked by execution — a pin that cannot run is the same outage as no pin. |
| `POGO_DEPLOY_ALERT_TO` | `mayor` | First alert recipient; `human` is always copied. |

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

### What re-asserts an installed plist against the shipped one

**Nothing does, and after mg-de0c nothing is going to — but the nightly NOTICES,
and mails.** This is the general question mg-fc99 left open, mg-b201 recorded,
and mg-b9e7 answered on the detection half. The reconciliation half is now
answered too, and the answer is **no**: see
[docs/design/launchd-reconcile-decision.md](../../docs/design/launchd-reconcile-decision.md)
for the blast radius measured per job.

Merging a change under `scripts/launchd/` changes nothing on any machine. The
only writer of `~/Library/LaunchAgents/com.pogo.deploy.plist` is `pogo service
install-deploy`, which a human or an agent has to run. Nothing runs it at boot,
at login, or on a `WatchPaths` event — so a merged schedule and an installed
schedule can sit apart indefinitely, and did: between 2026-07-31 and 2026-08-07
the box ran a one-fire plist against three-fire code, which is what mg-b201 was
filed to end.

**What changed (mg-b9e7).** `pogo check-activation` is the same comparison the
`launchd activation` doctor row makes, as a top-level command with an exit code
— `0` activated, `1` drifted, `3` could-not-compare — and
`scripts/pogo-self-deploy` runs it every night, immediately after the deploy has
built, installed and verified a binary from `main`. A drift now costs one night
of silence and arrives as mail, instead of sitting until somebody happens to run
`doctor`. Nothing reconciles: the deploy reports and stops.

```bash
pogo check-activation           # 0 / 1 / 3, per-job remedies, and the build it compared FROM
pogo check-activation --json
```

Three traps sit on this path. The first two have bitten; the third is why the
command is shaped the way it is.

1. **The installer writes the schedule *its own build* embeds.** The plist is a
   Go template with `deployHours` bound in, not a copy of the file in this
   directory. Running a `pogo` older than the schedule change therefore
   *reinstalls the old schedule* — and reports success while doing it. On
   2026-08-07 the `pogo` on `PATH` was built 07-30 and the retry fires landed
   07-31, so the obvious command would have been a no-op that looked like a fix.
   Build the binary from the checkout you are installing from, and read back.
   `check-activation` prints the build it compared from for this reason, and the
   nightly asks the binary it just installed *because that is the one moment the
   "which build?" question has a right answer.*

2. **The drift detector is inside the same binary, so it is subject to the
   defect it detects.** `pogo doctor --check` grew a `launchd activation` row
   (mg-fc99) that compares every managed plist against the one this build would
   write. It only *reports*; it never reconciles — deliberately, since
   `install-deploy` rewrites a schedule and `install` bounces the daemon, and
   neither is something a checklist should do unasked. But the row is absent
   from any `pogo` predating it, which on 2026-08-07 meant the detector for
   "merged but not installed" was itself merged but not installed. **A doctor run
   that shows no `launchd activation` row is not a clean bill of health; it is
   an old binary.**

3. **So is the new command — which is why it is top-level and why it prints a
   marker.** `pogo service <unknown>` exits **0** and prints help; `pogo
   <unknown>` exits 1. Filing this check under `service` would have let an old
   binary answer a scheduled caller with a success, reproducing trap 2 one layer
   down. Top-level it exits nonzero — but so does a drift, and both are 1, so
   every verdict line leads with `activation:` and the nightly refuses to read an
   exit status as a verdict without it. An old binary is reported as **NO
   VERDICT**, which is its own finding, and mails.

   What is still uncovered: this reading runs *inside* a deploy, so a box whose
   nightly has been failing for a week gets no report. That is the same argument
   that keeps `com.pogo.revisionprobe` outside the audit's registry (mg-a03d).

**Reconciliation stays manual, decided rather than deferred (mg-de0c).** All
four installers were measured against the question "could the nightly run this?"
and all four refuse for a named reason: `install` bounces the daemon *and*, on
this box, the drift is the legacy `POGO_HOME=$HOME` — reconciling it moves the
running daemon's state root; `install-recovery` bootstraps a `RunAtLoad=true`
job that kickstarts pogod whenever the recovery queue is non-empty;
`install-deploy` boots out the job the nightly is running in, and was measured
to kill the process *before* its `bootstrap` line, leaving the plist correct on
disk and the job unloaded; `com.pogo.reclaim` is absent rather than drifted, and
the audit says in its own output that it cannot tell "deliberately uninstalled"
from "never installed". The tempting dodge — write the plist without reloading —
is a regression, not a partial fix: the audit's predicate is byte equality on
disk, so it would flip the verdict to `ACTIVATED` while launchd still runs the
old job. Full argument and the conditions that would reopen it:
[docs/design/launchd-reconcile-decision.md](../../docs/design/launchd-reconcile-decision.md).

The read-back is the part that proves the manual reconcile happened. The install
command's own output does not:

```bash
pogo service install-deploy
/usr/libexec/PlistBuddy -c "Print :StartCalendarInterval" \
    ~/Library/LaunchAgents/com.pogo.deploy.plist        # expect three Hour entries
launchctl print gui/$UID/com.pogo.deploy | grep -c calendarinterval
```

The second read is worth the extra line: the plist on disk is what an installer
wrote, while `launchctl print` is what launchd actually registered. A plist it
rejected or only partly parsed is still a perfectly good-looking file.

Install outside the 02:00–06:00 window, or check `~/.pogo/deploy.lock.d` and
`pgrep -f pogo-deploy` first. `install-deploy` does `launchctl bootout` before
`bootstrap`, and booting out a job mid-fire would kill a deploy in progress.

## Disk Reclaim Agent (`com.pogo.reclaim`)

Samples free space every 30 minutes and runs `go clean -modcache` when — and
only when — **both** a free-space floor and a cache-size floor are crossed.

```bash
pogo service install-reclaim      # install / re-install
pogo service uninstall-reclaim    # remove; state under ~/.pogo/reclaim is kept
```

### If this job has never once done anything

**That is very likely correct, not a broken cron.** On this host the volume sits
at ~99% capacity, so the free-space arm is satisfied continuously, while the
module cache sits at ~680M — far under the 5G floor. Both arms must hold, so
every sample exits **4 (CANNOT HELP)**, and it should: deleting 680M off a 415G
fill returns 680M and costs a full re-download.

The job is answering a question, and on this box the answer is *"not me"*. It
starts acting the moment either input changes — when the cache accumulates past
5G, or when someone reclaims part of the ~414G it does not own.

That is measured, not hypothetical. The volume was watched dropping on
2026-08-12 and the module cache was measured **not** to be the grower:

```
11:51Z   6.9 GiB free
12:19Z   5.6 GiB free      -1.3 GiB in 28 min, 6 polecats running

~/go/pkg/mod                680M   UNCHANGED   <- what this job reclaims
~/Library/Caches/go-build    34G   UNCHANGED   <- see below
~/.pogo/polecats            4.3G   (2.6G of it one stale worktree)
~/.pogo/refinery            2.1G
/var/folders/.../go-build*         accumulating, ~100M per build
```

### The 34G Go cache this job does not touch

There are **two** Go caches on this box and they differ by a factor of fifty:

| Path | Size | Reclaimed by | This job? |
|---|---|---|---|
| `~/go/pkg/mod` | 680M | `go clean -modcache` | **yes** |
| `~/Library/Caches/go-build` | 34G | `go clean -cache` | **no** |

"The Go cache is large" is therefore ambiguous, and the larger reading is the one
this job does nothing about. It is named here, in the script header, and in every
scope note the job prints, so that nobody concludes the 34G is covered because
something with "reclaim" in its name is installed.

**Not touching it is deliberate.** `go clean -cache` discards every compiled
package on the box, so the next `./build.sh` — which *is* the refinery's merge
gate — recompiles the world. That trades a disk problem for a gate-latency
problem on every merge until the cache refills. If the build cache deserves
reclaiming, it deserves its own ticket with its own argument about what a cold
gate costs.

This job also does **not** delete polecat worktrees or refinery state, even
though they are what actually grew in the window above. That is `gitgc`'s job and
it has a live-agent witness protecting it; a cron that removes a worktree under a
running polecat is a worse failure than a full disk.

### Why it exists

On 2026-08-12 this box measured:

```
/dev/disk3s5   460Gi   422Gi   571Mi   100%   /System/Volumes/Data
/Users/daniel/go/pkg/mod   7.3G
```

`./build.sh` in a polecat worktree failed at Step 2 with `link: mapping output
file failed: no space left on device` across ~40 packages. `./build.sh` is also
the refinery's merge gate, so at that fill level every merge on the host was one
build away from failing.

The cost was not the outage. **It was that the outage was misattributed** — a
full volume presents as a compile/link error naming specific packages, which
reads like a broken branch.

### The trigger is two numbers, ANDed

| Knob | Default | What it maps to |
|---|---|---|
| `POGO_RECLAIM_FREE_FLOOR_GB` | 20 | The **observed damage**. A build fails because the volume is full. |
| `POGO_RECLAIM_CACHE_FLOOR_GB` | 5 | **What the reclaim can return.** Deleting a 200M cache off a full disk returns 200M and a re-download. |
| `POGO_RECLAIM_CRITICAL_FREE_GB` | 2 | Below this, an in-flight build no longer wins the tie. |

Either arm alone is worse than useless:

- **Free-space alone** fires on a full disk whose cache is small, deletes almost
  nothing, exits 0, and writes a log line that reads like the disk was handled.
  That is this ticket's own defect, reproduced by its own fix.
- **Cache-size alone** throws away a cache that costs a network round to rebuild
  on a box with 300G free.

The cache floor is set against a measurement rather than a hunch. After a manual
`go clean -modcache` on 2026-08-12, the cache came back to **680M after one
build and was flat across three readings spanning a full gate run** — so the
plateau holds under load, not only at rest. That is enough to design against,
not enough to call a measured steady state. It says the pre-clean 7.3G was
mostly *stale accumulation* (old module versions, superseded toolchains) rather
than live working set, which makes this a maintenance measure with a long fuse
rather than a bailing bucket. 5G is ~7.5× that reading: ordinary build traffic
cannot reach it, and the 7.3G that produced the ticket would have.

### The schedule is a sampler, not the trigger

launchd has no size trigger — `StartInterval`, `StartCalendarInterval` and
`WatchPaths` are the whole vocabulary, and a directory growing is not a
WatchPaths event. So the job wakes every 30 minutes and **the size decides**.

That is affordable only because the measurements are ordered cheap-first: one
`df` per fire, and the `du` of a multi-gigabyte tree only once `df` has already
established the disk is low. In the steady state a fire is one `df` and one log
line. A nightly `StartCalendarInterval` was the alternative and is worse here:
the volume went from healthy to 571 MiB free inside a working day.

### It defers while something is building

`go clean -modcache` deletes module trees a running build is **reading**, so a
racing build does not get slower — it fails, with a missing-file error that
reads like a broken branch. The fire therefore defers (exit 5) when `pgrep -x`
finds a `go`/`compile`/`link` process or `pogo refinery queue --json` shows a
processing MR.

Two honest limits, both stated in the log rather than assumed away:

- The check cannot see a build that starts one second later. The race is
  narrowed, not closed.
- If the check **cannot be made** (no `pogo` on PATH, daemon down) it proceeds
  and logs `in-flight check PARTIAL`. Deferring forever on an unanswerable
  question is how the disk fills, and a full disk breaks every build rather than
  one.

Below `CRITICAL_FREE_GB` the deferral is skipped outright: at 571 MiB free the
in-flight build fails whether or not this job runs.

### What it does not fix, and why the job says so out loud

**This job reclaims the Go module cache and nothing else.** On the box that
prompted it, that is 7.3G of a 422G fill. Largest consumers under `/Users/daniel`
measured 2026-08-12:

```
Library 73G   tools 15G   chrome 12G   research 9.8G
go 8.0G       .pogo 6.4G  Virtual Machines 5.0G   dev 4.6G
```

So every path through the script that could be mistaken for "the disk is
handled" states the opposite, computed from the run's own numbers rather than
fixed:

- A **successful reclaim** prints a `WHAT THIS DOES NOT FIX` block with the
  post-run fill, and — if the volume is still under the floor — logs
  `STILL BELOW THE FLOOR` and mails `human`.
- A **low disk with a small cache** does not fire at all. It exits **4**, logs
  `the Go module cache is NOT what is filling this volume`, and mails `human`
  (rate-limited to once per 24h).

On this host *today* the free-space arm is satisfied continuously (99% used,
~6.9 GiB free), so the cache-size arm is the only one deciding and every sample
exits 4. That is the true answer, not a malfunction. It stops being the answer
when someone reclaims part of the ~414G this job does not own.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Nothing to do (above the floor), or a reclaim that happened |
| 1 | `go clean -modcache` ran and failed |
| 3 | UNKNOWN — could not measure (no `go`, unreadable `df`/`du`). Never a pass. |
| 4 | CANNOT HELP — disk is low and the cache is not what is filling it |
| 5 | DEFERRED — a build is in flight and free space is not yet critical |

### Merging this does not install it

Three separate things have to happen, and only the first is a merge:

1. the branch merges;
2. the `pogo` **binary** is rebuilt onto a revision carrying `install-reclaim`
   (the nightly deploy, or a manual `pogo service install-deploy` run) — until
   then the subcommand does not exist on the box;
3. somebody runs `pogo service install-reclaim`.

Until step 3, `~/Library/LaunchAgents/com.pogo.reclaim.plist` does not exist and
no sample has ever run. `pogo doctor` reports the job as **absent** with the
remedy attached — which is the point of registering it: "shipped" and "armed"
are different states and the box can tell you which one it is in.

### The static-copy trap

launchd runs `~/.pogo/bin/pogo-reclaim.sh`, which `install-reclaim` copies out of
the repo. **A merge to main does not refresh it.** A fix to the runner is not
live on the box until `pogo service install-reclaim` is re-run — the same
standing trap the nightly deploy runner has.

Two things make that visible instead of silent:

- Every fire logs `runner: <path> (mtime …)`, so a log answers *which copy ran*
  without anybody trusting main.
- `com.pogo.reclaim` is registered in `managedLaunchAgents()`, so `pogo doctor`
  compares the installed **plist** against what the current build renders. Note
  the asymmetry: that audit covers the plist, not the script — the script's
  answer is the `runner:` line.

### Dry run

```bash
POGO_RECLAIM_DRY_RUN=1 POGO_RECLAIM_FREE_FLOOR_GB=999 ~/.pogo/bin/pogo-reclaim.sh
```

Takes both measurements, prints the verdict and the scope note, removes nothing.

## Revision Probe Agent (`com.pogo.revisionprobe`)

The smallest of these jobs, and the one that is not installed by the `pogo`
binary: it runs
`~/.pogo/deploy-src/scripts/revision-probe.sh` hourly at :20, which does two
reads and no build — the running revision from `GET /version`, the reference
from the tip of `origin/main` — and alerts when they have differed for longer
than 24h. It never builds, installs, restarts or writes anything but its own
stamp and ledger.

```bash
scripts/install-revision-probe.sh              # install / re-install, then verify
scripts/install-revision-probe.sh --dry-run    # render and print, touch nothing
scripts/install-revision-probe.sh --uninstall  # bootout and remove
```

### Why it has to be a launchd job

mg-ce10 landed the probe and armed it with nothing: 501 lines, zero schedules,
zero plists, zero callers. That is the limiting case of the rule the probe
implements — *a detector for "X did not happen" must not be activated by X* —
because a detector activated by **nothing** is present by existence and absent
by effect.

The two alternatives were considered and refused:

| candidate | why not |
|---|---|
| `pogo schedule` | its scheduler lives inside `pogod`, and it delivers a **nudge or mail to an agent** — it cannot run a command. Arming this way needs a live `pogod` *and* an agent turn. A stopped `pogod` is the state the probe most needs to report (mg-6d2f), and turns that never run are half of this lineage. |
| a call from the deploy runner | refused by the rule. A probe invoked by the deploy cannot witness the deploy that never fired — four of eight failing nights (mg-2def). That is driftwatch's shape (mg-5bd2), not a fix for it. |

launchd is triggered by the OS clock: independent of `pogod`, the deploy, the
refinery and any agent turn.

### Why it is NOT `com.pogo.deploy`

The deploy job is the thing being watched. Folding the witness into it would
make the alarm for "the deploy did not run" fire only when the deploy ran.

### Plist keys that differ from the other three

| key | value | why |
|---|---|---|
| `StartCalendarInterval` | `Minute 20` only, i.e. hourly | the divergence clock matures only as fast as the probe samples. A daily probe first *sees* a divergence a day late, so a 24h threshold would need three failed nights to fire. |
| `RunAtLoad` | `true` | the opposite of `com.pogo.deploy`, and for the stated reason: a fire of that job bounces the fleet, a fire of this one is two reads and no mutation. The first thing anyone wants after arming a witness is evidence that it fires. |
| `EnvironmentVariables.PATH` | **no** `~/.pogo/bin`, and `go`/`pogo`/`pogod` are not required | the probe must run on a box where the deploy has been failing for a week. `~/go/bin` is present for `mg` alone, and `revision-probe.sh` makes every candidate self-identify as macguffin first, because `/usr/bin/mg` is the Micro-Emacs editor. |
| `GIT_TERMINAL_PROMPT` | `0` | an unattended probe that stops to ask for a password does not fail, it **hangs**, and a hung probe is a silent one. |
| two log paths | `revision-probe.log` and `revision-probe.report.log` | the first is the **ledger** — one line per run, green or red — and it is the heartbeat. Mixing the narrative into it would bury the newest line. |

### Replay policy, declared

launchd has no field for it, so the behaviour is the OS's and this is where it
is chosen. `StartCalendarInterval` is **deferred-once** across sleep: one run on
wake for any number of fires missed while asleep. Right here, because the report
is not a per-interval sample — the age comes from a persisted stamp read against
wall clock, so a late report is still true and still names the correct age. A
skip policy would discard the first report after a wake, which is the one most
likely to carry news.

**A host that is powered OFF misses the fire outright.** launchd defers across
sleep, not across shutdown. The 2026-08-07 no-fire nights were a power-off, so
this witness would have been dark for them too.

### The read-back is the part that proves it

As with `com.pogo.deploy`, the install command's own output does not:

```bash
scripts/install-revision-probe.sh
launchctl print gui/$(id -u)/com.pogo.revisionprobe | head -20
tail -5 ~/Library/Logs/pogo/revision-probe.log     # RunAtLoad means there is already a line
```

A ledger whose newest line is hours old means the witness stopped — which looks
exactly like health if you only watch for alerts.

## Fleet Liveness Agent (`com.pogo.fleetliveness`)

The other job that is **not** installed by the `pogo` binary, and for a reason
one level out from the revision probe's: *a detector hosted INSIDE the population
it watches cannot report that population failing.* It runs
`~/.pogo/deploy-src/scripts/fleet-liveness-probe.sh` every fifteen minutes at
:07 :22 :37 :52, and its whole predicate is a `stat` and a subtraction — the
NEWEST mtime across `~/.pogo/agents/turnlog/*.log` against now, alerting past 2h.

It exists because on 2026-08-14 all seven crew agents stopped inside a ten-minute
window and stayed stopped for ~118 hours, and the check built for exactly that
outage (`deploy-verify` §0) never ran: it is one of architect's own schedules and
architect was one of the agents that stopped. So this job may not be a `pogo
schedule` (pogod's scheduler can only deliver to an agent, and needs a turn to
run), may not be a crew schedule (that is the circularity verbatim), and may not
be rendered by `internal/service` (which ships in the `pogo` binary). launchd is
the only substrate that is independent of pogod, the fleet, the deploy and every
agent turn.

`--stale-after 2h` is on the NEWEST turnlog across ALL agents, never per-agent: an
idle PM legitimately goes hours between turns and this box carries an `a270.log`
untouched since 2026-08-11 by design. The all-of-them condition is what separates
a fleet stop from ordinary idleness.

It alerts by MAIL to `human`, never by nudge — measured during the outage, nudges
recovered the merely-unreachable agents and did nothing for the wedged ones, and
mail is the only wake channel that survived both the idle gate and wake-silence
suppression. During a fleet-wide stop there is no in-fleet actor left to act.

It also runs a **positive control on its own delivery path** on a cadence, because
a detector that never fires never tests its notification path. See
`docs/operations.md` for the full argument and the ledger's `mail=` states.

```bash
scripts/install-fleet-liveness-probe.sh
launchctl print gui/$(id -u)/com.pogo.fleetliveness | head -20
tail -5 ~/Library/Logs/pogo/fleet-liveness.log     # RunAtLoad means there is already a line
```

Same caveat as above, and it is the reason the ledger exists at all: a ledger
whose newest line is hours old means the witness stopped, and nothing on this box
watches this witness.
