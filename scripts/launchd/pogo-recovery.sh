#!/bin/bash
# pogo-recovery.sh — tier-3 recovery agent (mg-6749 / parent mg-f5fc).
#
# Triggered by launchd's WatchPaths on $POGO_RECOVERY_DIR/queue. Drains
# any *.req files by issuing `launchctl kickstart -k gui/$UID/com.pogo.daemon`,
# subject to a 60s rate limit. Stays independent of pogod's process tree:
# uses only kernel primitives (flock, mv, launchctl).
#
# WHAT IT VERIFIES AFTER THE RESTART (mg-ed4a). It used to verify the kickstart's
# own exit code and nothing else. That code says launchd ACCEPTED the request —
# not that a daemon exists afterwards, and certainly not that it is running the
# binary you meant. A kickstart re-execs whatever is on disk, so silently
# reinstating a stale binary is its normal behaviour when the disk is stale, and
# this box spent eight days healthy, alive and 92 commits behind while every
# check but the deploy script's passed.
#
# So the drain now ends by asking `pogo service verify-revision` whether the
# daemon that came back is the revision launchd was configured to exec. That
# command shares one implementation (internal/revcheck) with the two
# internal/service restart paths, so all four paths answer the same question the
# same way.
#
# THE VERIFY IS ADVISORY TO THE ARCHIVE AND DECISIVE TO THE EXIT CODE. Requests
# are archived on the kickstart's result, unchanged: a kickstart that happened
# has been serviced, and re-queueing on a bad revision would loop against a
# broken artifact — the unbounded-reaper shape. But the script's exit code now
# carries the verdict, because "kickstart succeeded" was allowed to be the whole
# story for too long. No restart loop can come of that: the requests are already
# out of the queue, last_restart is already written, and com.pogo.recovery is
# KeepAlive=false so launchd does not respawn on a nonzero exit.
#
# INDEPENDENCE IS PRESERVED. `pogo service verify-revision` reads a plist and
# makes one loopback HTTP GET; it does not ask pogod to do anything and does not
# join its process tree. If `pogo` is not on PATH — or is too old to have the
# subcommand — the verdict is UNKNOWN and says so, which is the one thing it
# must never quietly render as agreement.

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

# The revision verifier and how long it may poll. Overridable so the harness can
# substitute a stub, and so an operator can shorten the wait on a slow box.
POGO_CLI="${POGO_CLI:-pogo}"
VERIFY_TIMEOUT="${POGO_RECOVERY_VERIFY_TIMEOUT:-45s}"
# Set to 0 to drain without verifying — for a box whose `pogo` predates the
# subcommand and where the UNKNOWN line is pure noise. It is opt-OUT, not
# opt-in: a verification you have to remember to enable is one that is off on
# the box that needed it.
VERIFY_REVISION="${POGO_RECOVERY_VERIFY_REVISION:-1}"

# Exit codes, mirroring `pogo service verify-revision` so a reader of one is a
# reader of the other.
EXIT_REVISION_DIFFERS=1
EXIT_REVISION_UNKNOWN=3

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

# --- did the daemon that came back carry the right code? (mg-ed4a) -----------
#
# Everything above this line is about the KICKSTART. This is the only part that
# is about the DAEMON, and it is the question the previous version of this
# script never asked.
verify_revision() {
    if [ "$VERIFY_REVISION" = "0" ]; then
        log "revision check SKIPPED (POGO_RECOVERY_VERIFY_REVISION=0) — this drain did NOT establish which revision came back"
        return 0
    fi

    if ! command -v "$POGO_CLI" >/dev/null 2>&1; then
        # UNKNOWN, stated. Not silence, and emphatically not a pass: the whole
        # point is that a check which cannot measure must say so rather than
        # letting the kickstart's exit code stand in for an answer.
        log "revision check UNKNOWN: '$POGO_CLI' is not on PATH ($PATH) — cannot establish which revision pogod came back on"
        return "$EXIT_REVISION_UNKNOWN"
    fi

    # A `pogo` predating mg-ed4a exits 1 on an unknown subcommand, which is the
    # SAME code this script reads as DIFFERS. That would turn "your CLI is old"
    # into "your daemon is running the wrong code" — a confidently wrong alarm
    # from a check whose entire purpose is not producing those. So establish
    # that the subcommand exists before trusting any code it returns.
    if ! "$POGO_CLI" service verify-revision --help >/dev/null 2>&1; then
        log "revision check UNKNOWN: '$POGO_CLI' has no 'service verify-revision' (predates mg-ed4a)"
        log "  Upgrade the CLI, or set POGO_RECOVERY_VERIFY_REVISION=0 to stop asking."
        return "$EXIT_REVISION_UNKNOWN"
    fi

    local out rc
    out="$("$POGO_CLI" service verify-revision --timeout "$VERIFY_TIMEOUT" 2>&1)"
    rc=$?
    [ -n "$out" ] && log "  $out"

    case "$rc" in
        0)  log "revision check AGREES — the daemon came back on the binary launchd execs"
            return 0 ;;
        "$EXIT_REVISION_DIFFERS")
            log "revision check DIFFERS — THE RESTART PUT THE WRONG CODE BACK."
            log "  The kickstart succeeded and the requests were serviced; the daemon is NOT"
            log "  running the binary launchd is configured to exec. A kickstart re-execs"
            log "  whatever is on disk, so this usually means the binary on disk is not what"
            log "  you think it is, or a second pogod holds the port. Redeploy — another"
            log "  restart alone will reinstate the same code."
            return "$EXIT_REVISION_DIFFERS" ;;
        *)  log "revision check UNKNOWN (rc=$rc) — this drain did NOT establish which revision came back"
            return "$EXIT_REVISION_UNKNOWN" ;;
    esac
}

verify_revision
VERIFY_RC=$?
exit "$VERIFY_RC"
