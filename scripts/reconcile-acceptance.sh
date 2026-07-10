#!/usr/bin/env bash
# reconcile-acceptance.sh — end-to-end proof for mg-be0c's reconcile + drift check.
#
# This is the acceptance the ticket demands: don't close on "the script exists,"
# close by DRIFTING A HOST ON PURPOSE and observing (a) the drift check fire and
# name the specific file/job, (b) reconcile make the RUNNING process pick up the
# change — a NEW pid running NEW behavior — and (c) a non-drifted host report
# clean as a positive control.
#
# It is MANUAL and macOS-only: it creates a throwaway launchd job
# (com.pogo.be0c-demo) in a sandbox POGO_HOME, drives it through the full
# lifecycle, and boots it out on exit. It never touches the live deployed
# pollers, ~/Library/LaunchAgents, or the real config. It is deliberately NOT
# wired into test.sh — it manipulates the live launchd session, which has no
# place in CI.
#
# Run from a pogo checkout after `./build.sh`:
#   POGO=./bin/pogo bash scripts/reconcile-acceptance.sh
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "SKIP: reconcile-acceptance is macOS-only (needs launchctl)"; exit 0
fi

POGO="${POGO:-./bin/pogo}"
if [ ! -x "$POGO" ]; then
  # Fall back to a PATH pogo, but warn — a stale global binary won't have the
  # reconcile subcommands and the acceptance would test the wrong thing.
  POGO="$(command -v pogo || true)"
  [ -n "$POGO" ] || { echo "FATAL: no pogo binary; run ./build.sh first"; exit 1; }
  echo "WARN: using $POGO (set POGO=./bin/pogo to test your branch build)"
fi
POGO="$(cd "$(dirname "$POGO")" && pwd)/$(basename "$POGO")"

LABEL="com.pogo.be0c-demo"
SANDBOX="$(mktemp -d "${TMPDIR:-/tmp}/be0c-accept.XXXXXX")"
UID_NUM="$(id -u)"
TARGET_SVC="gui/${UID_NUM}/${LABEL}"

cleanup() {
  launchctl bootout "gui/${UID_NUM}" "$SANDBOX/agents/${LABEL}.plist" 2>/dev/null || true
  launchctl bootout "$TARGET_SVC" 2>/dev/null || true
  rm -rf "$SANDBOX"
}
trap cleanup EXIT

mkdir -p "$SANDBOX/repo/bin" "$SANDBOX/host/bin" "$SANDBOX/agents" \
         "$SANDBOX/pogohome" "$SANDBOX/xdg/pogo"

SOURCE="$SANDBOX/repo/bin/demo-poller.sh"
TARGET="$SANDBOX/host/bin/demo-poller.sh"
BEHAVIOR_LOG="$SANDBOX/behavior.log"
PLIST="$SANDBOX/agents/${LABEL}.plist"

# A long-lived poller: it records "pid=<pid> version=<V>" once on start, then
# loops forever. The version string IS the observable behavior — a kickstart of
# a reconciled copy appends a new line with a new pid and the new version. This
# is the same shape as the real pollers (a top-level `while` loop bash parses
# once and never re-reads).
write_poller() {  # $1 = version
  cat > "$SOURCE" <<EOF
#!/usr/bin/env bash
VERSION="$1"
echo "pid=\$\$ version=\$VERSION" >> "$BEHAVIOR_LOG"
while true; do sleep 1; done
EOF
  chmod 0755 "$SOURCE"
}

# config.toml declaring the one demo mirror. The CLI reads it via config.Load
# from XDG_CONFIG_HOME/pogo and POGO_HOME; we point both at the sandbox so the
# real user config is never read or written.
cat > "$SANDBOX/xdg/pogo/config.toml" <<EOF
[agents]
autostart = false

[reconcile]
mirrors = ["demo|$SOURCE|$TARGET|$LABEL"]
EOF

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>${LABEL}</string>
    <key>ProgramArguments</key><array><string>${TARGET}</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><false/>
    <key>StandardOutPath</key><string>${SANDBOX}/demo.log</string>
    <key>StandardErrorPath</key><string>${SANDBOX}/demo.log</string>
</dict>
</plist>
EOF

pogo() { env POGO_HOME="$SANDBOX/pogohome" XDG_CONFIG_HOME="$SANDBOX/xdg" "$POGO" "$@"; }

running_pid() { launchctl print "$TARGET_SVC" 2>/dev/null | awk -F' = ' '/^[[:space:]]*pid = /{print $2; exit}'; }

hr() { printf '\n===== %s =====\n' "$1"; }

# --- deploy v1 -----------------------------------------------------------------
hr "SETUP: deploy v1 and start the job"
write_poller "1"
cp "$SOURCE" "$TARGET"; chmod 0755 "$TARGET"
launchctl bootstrap "gui/${UID_NUM}" "$PLIST"
launchctl kickstart -k "$TARGET_SVC"
sleep 1
PID1="$(running_pid)"
echo "started job $LABEL, pid=$PID1"
echo "behavior log:"; cat "$BEHAVIOR_LOG"

# --- positive control: clean ---------------------------------------------------
hr "POSITIVE CONTROL: nothing drifted → drift check must report CLEAN (exit 0)"
if pogo service check-drift; then
  echo "PASS: clean host reported clean, exit 0"
else
  echo "FAIL: clean host reported drift"; exit 1
fi

# --- drift the host on purpose -------------------------------------------------
hr "DRIFT: bump the repo SOURCE to v2 (a merge that reached the repo, not the host)"
write_poller "2"
echo "source is now v2; deployed target is still v1"
if pogo service check-drift; then
  echo "FAIL: drift check did not fire on a drifted host"; exit 1
else
  echo "PASS: drift check FIRED (exit 1) and named the file above"
fi

# --- reconcile: file replaced + process restarted ------------------------------
hr "RECONCILE: atomic-replace the copy AND kickstart the running process"
pogo service reconcile
sleep 1
PID2="$(running_pid)"
echo "behavior log after reconcile:"; cat "$BEHAVIOR_LOG"

# --- verify the OUTCOME, not the artifact --------------------------------------
hr "VERIFY: new pid + new behavior (not merely 'the file changed')"
ok=1
if [ -n "$PID1" ] && [ -n "$PID2" ] && [ "$PID1" != "$PID2" ]; then
  echo "PASS: pid changed $PID1 -> $PID2 (the process was actually restarted)"
else
  echo "FAIL: pid did not change ($PID1 -> $PID2); the running process is stale"; ok=0
fi
if grep -q "pid=$PID2 version=2" "$BEHAVIOR_LOG"; then
  echo "PASS: the NEW process is running the NEW behavior (version=2)"
else
  echo "FAIL: new process is not running v2 behavior"; ok=0
fi

hr "POSITIVE CONTROL #2: after reconcile the host must be CLEAN again"
if pogo service check-drift; then
  echo "PASS: reconciled host reports clean"
else
  echo "FAIL: still drifted after reconcile"; ok=0
fi

hr "RESULT"
if [ "$ok" = 1 ]; then echo "ACCEPTANCE PASSED"; else echo "ACCEPTANCE FAILED"; exit 1; fi
