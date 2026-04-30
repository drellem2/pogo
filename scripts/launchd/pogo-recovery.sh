#!/bin/bash
# pogo-recovery.sh — tier-3 recovery agent (mg-6749 / parent mg-f5fc).
#
# Triggered by launchd's WatchPaths on $POGO_RECOVERY_DIR/queue. Drains
# any *.req files by issuing `launchctl kickstart -k gui/$UID/com.pogo.daemon`,
# subject to a 60s rate limit. Stays independent of pogod's process tree:
# uses only kernel primitives (flock, mv, launchctl).

set -u

POGO_RECOVERY_DIR="${POGO_RECOVERY_DIR:-$HOME/.pogo/recovery}"
QUEUE_DIR="$POGO_RECOVERY_DIR/queue"
PROCESSED_DIR="$POGO_RECOVERY_DIR/processed"
FAILED_DIR="$POGO_RECOVERY_DIR/failed"
LOCK_DIR="$POGO_RECOVERY_DIR/recovery.lock.d"
LAST_RESTART_FILE="$POGO_RECOVERY_DIR/last_restart"
MIN_INTERVAL="${POGO_RECOVERY_MIN_INTERVAL:-60}"
DAEMON_LABEL="${POGO_RECOVERY_LABEL:-com.pogo.daemon}"
LAUNCHCTL="${LAUNCHCTL:-launchctl}"
STALE_LOCK_MIN=5

mkdir -p "$QUEUE_DIR" "$PROCESSED_DIR" "$FAILED_DIR"

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "[$(ts)] $*"; }

# Non-blocking exclusive lock via atomic mkdir. Portable across macOS (no
# /usr/bin/flock) and Linux. A stale lock older than $STALE_LOCK_MIN minutes
# (a crashed prior invocation) is reclaimed; otherwise we exit 0 because
# another invocation is already draining and will see whatever we'd have
# processed.
acquire_lock() {
    if mkdir "$LOCK_DIR" 2>/dev/null; then return 0; fi
    if find "$LOCK_DIR" -maxdepth 0 -type d -mmin +"$STALE_LOCK_MIN" 2>/dev/null | grep -q .; then
        log "reclaiming stale lock (>${STALE_LOCK_MIN}min old)"
        rm -rf "$LOCK_DIR"
        mkdir "$LOCK_DIR" 2>/dev/null && return 0
    fi
    return 1
}
if ! acquire_lock; then
    log "lock held; another invocation is draining — exiting 0"
    exit 0
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

shopt -s nullglob
REQS=("$QUEUE_DIR"/*.req)
shopt -u nullglob

if [ "${#REQS[@]}" -eq 0 ]; then
    log "queue empty — exiting 0"
    exit 0
fi

now="$(date +%s)"
last=0
[ -f "$LAST_RESTART_FILE" ] && last="$(cat "$LAST_RESTART_FILE" 2>/dev/null || echo 0)"
[ -z "$last" ] && last=0
delta=$((now - last))

if [ "$delta" -lt "$MIN_INTERVAL" ]; then
    log "rate-limited; deferring ${#REQS[@]} request(s) (delta=${delta}s < ${MIN_INTERVAL}s)"
    # Leave queue files in place so the next trigger drains them. Schedule a
    # follow-up tickle so we don't depend on a fresh user write to retrigger
    # WatchPaths after the floor elapses.
    ( sleep $((MIN_INTERVAL - delta + 5)) && /usr/bin/touch "$QUEUE_DIR/.tickle" ) >/dev/null 2>&1 &
    exit 0
fi

# Snapshot the request list and prune-archive each one as we process. We do
# the kickstart once per drain, then mark every snapshotted .req as
# processed (success) or failed (kickstart error).
log "draining ${#REQS[@]} request(s)"
for f in "${REQS[@]}"; do
    log "  request: $(basename "$f"): $(head -1 "$f" 2>/dev/null || echo '<empty>')"
done

target="gui/$(id -u)/$DAEMON_LABEL"
log "launchctl kickstart -k $target"
KICK_OUT="$($LAUNCHCTL kickstart -k "$target" 2>&1)"
KICK_RC=$?
[ -n "$KICK_OUT" ] && log "kickstart output: $KICK_OUT"

archive_dir="$PROCESSED_DIR"
[ "$KICK_RC" -ne 0 ] && archive_dir="$FAILED_DIR"

stamp="$(date +%Y%m%dT%H%M%SZ)"
for f in "${REQS[@]}"; do
    base="$(basename "$f")"
    mv "$f" "$archive_dir/${stamp}-${base}" 2>/dev/null || log "  warn: could not archive $base"
done

if [ "$KICK_RC" -ne 0 ]; then
    log "kickstart failed (rc=$KICK_RC); ${#REQS[@]} request(s) moved to failed/"
    exit "$KICK_RC"
fi

echo "$now" > "$LAST_RESTART_FILE.tmp" && mv "$LAST_RESTART_FILE.tmp" "$LAST_RESTART_FILE"
log "kickstart succeeded; ${#REQS[@]} request(s) moved to processed/"

# Prune archives older than 7 days. Best-effort.
find "$PROCESSED_DIR" "$FAILED_DIR" -type f -mtime +7 -delete 2>/dev/null || true

exit 0
