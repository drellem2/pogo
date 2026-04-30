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
| `StandardOutPath` / `StandardErrorPath` | `~/Library/Logs/pogo/pogod.log` | macOS-standard location for user-scope app logs; surfaces in Console.app and avoids any collision with arbitrary files at the $HOME root. |
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
