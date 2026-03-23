# pogod launchd Service

This directory contains a launchd plist for running `pogod` as a persistent user agent on macOS. The daemon starts on login and automatically restarts on crash.

## Recommended: Use `pogo service install`

The easiest way to install the service is:

```bash
pogo service install
```

This auto-detects your `pogod` binary path and sets up everything. Use `pogo service uninstall` to remove it.

## Manual Installation

If you prefer to install the plist manually:

### 1. Copy and customize the plist

```bash
cp com.pogo.daemon.plist ~/Library/LaunchAgents/com.pogo.daemon.plist
```

Edit the plist to set the correct `pogod` path:

```bash
# Find your pogod binary
which pogod

# Edit the plist — replace /usr/local/bin/pogod with your actual path
nano ~/Library/LaunchAgents/com.pogo.daemon.plist
```

### 2. Load the service

```bash
launchctl load ~/Library/LaunchAgents/com.pogo.daemon.plist
```

### 3. Verify it's running

```bash
launchctl list | grep pogo
curl http://127.0.0.1:10000/health
```

## Managing the Service

| Action | Command |
|--------|---------|
| Load (start) | `launchctl load ~/Library/LaunchAgents/com.pogo.daemon.plist` |
| Unload (stop) | `launchctl unload ~/Library/LaunchAgents/com.pogo.daemon.plist` |
| Check status | `launchctl list \| grep pogo` |
| View logs | `tail -f /tmp/pogo.log` |
| View errors | `tail -f /tmp/pogo.err.log` |

## Notes

- The plist uses `KeepAlive` with `SuccessfulExit: false`, meaning launchd restarts pogod whenever it exits with a non-zero status.
- `RunAtLoad: true` ensures pogod starts automatically when the plist is loaded (including on login/reboot).
- Log paths default to `/tmp/pogo.log` and `/tmp/pogo.err.log`. The programmatic installer (`pogo service install`) uses `~/.local/share/pogo/logs/` instead.
- The `PATH` environment variable is set explicitly since launchd agents run with a minimal environment.
