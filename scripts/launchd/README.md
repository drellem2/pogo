# pogod launchd Service

This directory contains a launchd plist for running `pogod` as a persistent user agent on macOS. The daemon starts on login and automatically restarts on crash.

## Recommended: Use `pogo service install`

The easiest way to install the service is:

```bash
pogo service install
```

This auto-detects your `pogod` binary path, builds a plist matching the spec below (ProcessType=Interactive, KeepAlive=true, log to `~/.pogo/log/pogod.log`, PATH/HOME/POGO_HOME/POGO_PLUGIN_PATH wired up), and `launchctl load`s it. The installer is idempotent — rerun it after upgrading pogod or changing the plist template, and it will replace the existing service in place.

If a manually-started `pogod` is already running, the installer stops it first so the launchctl load doesn't collide on the lockfile.

Use `pogo service uninstall` to remove it.

## Manual Installation

If you prefer to install the plist manually:

### 1. Customize the plist

Replace `YOUR_USERNAME` in `com.pogo.daemon.plist` with your actual username, and set the `pogod` path correctly:

```bash
which pogod  # confirm location
sed "s/YOUR_USERNAME/$USER/g" com.pogo.daemon.plist > ~/Library/LaunchAgents/com.pogo.daemon.plist
mkdir -p ~/.pogo/log
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
| `StandardOutPath` / `StandardErrorPath` | `~/.pogo/log/pogod.log` | Colocates daemon logs with the rest of pogo state. |
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
| View logs | `tail -f ~/.pogo/log/pogod.log` |
