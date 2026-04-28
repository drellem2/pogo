# pogod launchd Service

This directory contains a launchd plist for running `pogod` as a persistent user agent on macOS. The daemon starts on login and automatically restarts on crash.

## Recommended: Use `pogo service install`

The easiest way to install the service is:

```bash
pogo service install
```

This auto-detects your `pogod` binary path, builds a plist matching the spec below (ProcessType=Interactive, KeepAlive=true, log to `~/Library/Logs/pogo/pogod.log`, PATH/HOME/POGO_HOME/POGO_PLUGIN_PATH wired up), and `launchctl bootstrap`s it into your GUI domain (`gui/$(id -u)`). The installer is idempotent — rerun it after upgrading pogod or changing the plist template, and it will replace the existing service in place.

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
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.pogo.daemon.plist
launchctl kickstart -k gui/$(id -u)/com.pogo.daemon
```

The legacy `launchctl load <plist>` form also works in a fresh GUI Terminal,
but it silently fails to start the program when invoked from a non-Aqua
process tree (e.g. an agent spawned under pogod). `bootstrap` targets the
GUI domain explicitly, which is why `pogo service install` uses it.

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
| Load (start) | `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.pogo.daemon.plist` |
| Unload (stop) | `launchctl bootout gui/$(id -u)/com.pogo.daemon` |
| Restart | `launchctl kickstart -k gui/$(id -u)/com.pogo.daemon` |
| Check status | `launchctl list \| grep com.pogo.daemon` (or `launchctl print gui/$(id -u)/com.pogo.daemon` for full state) |
| View logs | `tail -f ~/Library/Logs/pogo/pogod.log` |
