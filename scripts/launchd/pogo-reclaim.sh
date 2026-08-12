#!/usr/bin/env bash
# pogo-reclaim.sh — SIZE-TRIGGERED reclaim of the Go module cache (mg-b7c3).
#
# ---------------------------------------------------------------------------
# WHAT PROMPTED IT, MEASURED
# ---------------------------------------------------------------------------
#     /dev/disk3s5   460Gi   422Gi   571Mi   100%   /System/Volumes/Data
#     /Users/daniel/go/pkg/mod   7.3G
#
# `./build.sh` in a polecat worktree failed at Step 2 with
# `link: mapping output file failed: no space left on device` across ~40
# packages. The refinery runs the same ./build.sh as its merge gate, so at that
# fill level every merge on the box was one build away from failing — and it
# fails as a compile/link error that reads like a broken branch.
#
# The expensive part was never the outage. It was that the outage was
# MISATTRIBUTED. Everything in this script that is not the `go clean` exists to
# stop it being misattributed in the other direction: as "we installed the cron,
# so the disk is handled".
#
# ===========================================================================
# IF YOU ARE READING THIS BECAUSE THE JOB HAS NEVER ONCE DONE ANYTHING
# ===========================================================================
# That is very likely the CORRECT outcome, not a broken cron. Read this before
# concluding the job is misconfigured.
#
# On the host this was written for, every sample exits 4 (CANNOT HELP): the
# volume sits at ~99% capacity, so the free-space arm is satisfied CONTINUOUSLY,
# while the module cache sits at ~680M — far under the 5G floor. Both arms must
# hold, so the reclaim never fires, and it should not: deleting 680M off a 415G
# fill returns 680M and costs a full re-download.
#
# AND THAT IS NOT A HYPOTHETICAL. The volume was watched dropping on 2026-08-12
# and the module cache was measured NOT to be the grower:
#
#     11:51Z   6.9 GiB free
#     12:19Z   5.6 GiB free     -1.3 GiB in 28 min, 6 polecats running
#
#     ~/go/pkg/mod            680M   UNCHANGED  <- what this job reclaims
#     ~/Library/Caches/go-build 34G  UNCHANGED  <- see below
#     ~/.pogo/polecats         4.3G  (2.6G of it one stale worktree)
#     ~/.pogo/refinery         2.1G
#     /var/folders/.../go-build*     accumulating, ~100M per build
#
# THE JOB IS ANSWERING A QUESTION, AND ON THIS BOX THE ANSWER IS "NOT ME". It
# starts acting the moment either input changes — when the cache accumulates
# past 5G (a slow fuse; see the plateau measurement below), or when someone
# reclaims part of the ~414G this job does not own and the free-space arm stops
# being trivially true.
#
# ---------------------------------------------------------------------------
# THE 34G GO CACHE THIS JOB DOES NOT TOUCH
# ---------------------------------------------------------------------------
# There are TWO Go caches on this box and they differ by a factor of fifty:
#
#     ~/go/pkg/mod                680M   the MODULE cache   <- `go clean -modcache`
#     ~/Library/Caches/go-build    34G   the BUILD cache    <- `go clean -cache`
#
# "The Go cache is large" is therefore ambiguous, and the larger reading is the
# one this job does nothing about. Say it here rather than let a reader assume
# the 34G is covered because a job with "reclaim" in its name is installed.
#
# NOT touching it is deliberate, not an oversight. `go clean -cache` discards
# every compiled package on the box, so the next `./build.sh` — which IS the
# refinery's merge gate — recompiles the world. That trades a disk problem for a
# gate-latency problem on every merge until the cache refills, which is a worse
# deal than the one this ticket was filed to make, and it is not what was asked
# for. If the build cache deserves reclaiming it deserves its own ticket, with
# its own argument about what a cold gate costs.
#
# The exit-4 log line says all of this on every fire. This block exists because
# an exit code whose normal state looks like a failure needs its explanation
# UPSTREAM of the log line: the person diagnosing it is reading the code.
#
# ---------------------------------------------------------------------------
# WHY THE TRIGGER IS TWO NUMBERS, ANDed
# ---------------------------------------------------------------------------
# Free space and cache size are different triggers and they fail differently:
#
#   free space   is the one that maps to the OBSERVED DAMAGE. A build fails
#                because the volume is full, not because a cache is large.
#   cache size   is the one that maps to WHAT THE RECLAIM CAN RETURN. Deleting
#                a 200M cache off a full disk returns 200M and a re-download.
#
# Either one alone is wrong in a way that is worse than useless:
#
#   free-space alone   fires on a full disk whose cache is small, deletes
#                      almost nothing, exits 0, and writes a log line that reads
#                      like the disk was handled. That is the misattribution
#                      this ticket was filed about, reproduced by its own fix.
#   cache-size alone   fires on a box with 300G free because the cache crossed
#                      5G, throwing away a cache that costs a network round to
#                      rebuild, for headroom nobody needed.
#
# So both must hold. When free space is low and the cache is NOT large, this
# script does not shrug — it exits 4 and says, in the log and in a mail, that
# the Go module cache is not what is filling this disk.
#
# ---------------------------------------------------------------------------
# WHY A PERIODIC FIRE ARMS A SIZE TRIGGER, AND NOT A CALENDAR ONE
# ---------------------------------------------------------------------------
# launchd has no size trigger. It has StartInterval, StartCalendarInterval and
# WatchPaths, and a directory's SIZE changing is not a WatchPaths event. So the
# schedule here is a SAMPLER and the size is the TRIGGER: launchd wakes this
# script every 30 minutes, and the script decides.
#
# That ordering is why the sampler is cheap. `df` on the volume runs first and
# costs nothing; the `du` of a multi-gigabyte tree is only paid on a fire that
# has already established the disk is low. In the steady state — the state this
# job is in almost always — a fire is one `df` and one log line.
#
# ---------------------------------------------------------------------------
# WHY IT CHECKS FOR BUILDS IN FLIGHT
# ---------------------------------------------------------------------------
# `go clean -modcache` is not free and it is not merely slow. It DELETES module
# source trees that a concurrent `go build` is reading, so a build racing this
# script does not get slower — it fails, with a missing-file error that reads
# like a broken branch. Which is this ticket's own complaint.
#
# So the fire defers while a build is running. Two honest limits on that check:
#
#   - It covers the refinery queue and any `go`/`compile`/`link` process on the
#     box. It does NOT know about a build that is about to start one second
#     from now. The race is narrowed, not closed.
#   - If the deferral check cannot be MADE (no `pogo` on PATH, daemon down), it
#     proceeds and says so. Deferring forever on an unanswerable question is how
#     the disk fills, and a full disk breaks every build rather than one.
#
# Below POGO_RECLAIM_CRITICAL_FREE_GB the deferral is skipped outright: at
# 571 MiB free the in-flight build is going to fail whether or not this script
# runs, and the next one after it too.
#
# ---------------------------------------------------------------------------
# THIS FILE IS A STATIC COPY ONCE INSTALLED
# ---------------------------------------------------------------------------
# launchd runs ~/.pogo/bin/pogo-reclaim.sh, which `pogo service install-reclaim`
# copies out of the repo. A merge to main does NOT refresh it. A fix to this file
# is not live on the box until install-reclaim is re-run — the same standing trap
# the nightly deploy runner has. The `runner:` line at the top of every fire logs
# the path and mtime of the copy that ACTUALLY RAN, so a log can answer "which
# version was this" without anybody trusting main.
#
# ---------------------------------------------------------------------------
# EXIT CODES
# ---------------------------------------------------------------------------
#   0  nothing to do (above the floor), or a reclaim that happened
#   1  the reclaim ran and failed
#   3  UNKNOWN — could not measure (no `go`, unreadable df/du). Never a pass.
#   4  CANNOT HELP — disk is low and the cache is not what is filling it
#   5  DEFERRED — a build is in flight and free space is not yet critical

set -u

# --- knobs -----------------------------------------------------------------
#
# THE MEASUREMENT THE CACHE FLOOR IS SET AGAINST (mayor, 2026-08-12):
#
#     11:35Z  go clean -modcache   free 7.7 GiB   modcache gone
#     11:45Z  after ~1 gate run    free 6.9 GiB   modcache 680M
#     11:47Z  gate still running   free 6.9 GiB   modcache 680M   <- FLAT
#     11:57Z  under a full gate    free 7.0 GiB   modcache 680M   <- STILL FLAT
#
# 680M after one build, flat across THREE readings spanning a full gate run —
# so the plateau holds under load, not only at rest. That is strong enough to
# design against and NOT strong enough to call a measured steady state, so it is
# quoted here as what it is rather than as "the working set is 680M".
#
# What it settles is the question that actually decides the cache floor: whether
# the pre-clean 7.3G was mostly LIVE working set or mostly STALE accumulation
# (old module versions, superseded toolchains). If it were live, a reclaim would
# be self-consuming and any high floor would never fire before the volume filled
# again. The plateau says accumulation, which makes this a maintenance measure
# with a long fuse rather than a bailing bucket — and makes a floor set well
# above the working set the right shape.
#
# Defaults, and why these numbers:
#
#   FREE_FLOOR_GB=20    The fleet builds the same Go tree in several polecat
#                       worktrees at once; the observed failure was a LINK,
#                       which maps the whole output file. 20G of a 460G volume
#                       (4.3%) leaves room for several concurrent builds plus
#                       the scratch the fleet writes, and is far enough above
#                       the observed 571 MiB to fire well before damage.
#   CACHE_FLOOR_GB=5    ~7.5x the 680M reading above, so ordinary build traffic
#                       cannot reach it and only accumulation can. Below this
#                       floor the reclaim returns less than the re-download
#                       costs and would mostly produce a log line overstating
#                       what happened. The pre-clean 7.3G was above it, so the
#                       condition that produced this ticket would have fired.
#   CRITICAL_FREE_GB=2  The "the build is doomed anyway" line. Above it, an
#                       in-flight build wins the tie; below it, the reclaim does.
#
# WHAT THESE TWO NUMBERS MEAN ON THIS HOST TODAY, stated because it is not the
# design's steady state: the volume is at 99% with ~6.9 GiB free, so the
# free-space arm is satisfied CONTINUOUSLY and the cache-size arm is the one
# actually deciding. Every sample therefore reaches the `du` and exits 4
# (CANNOT HELP) while the cache sits at 680M — which is the true answer, not a
# malfunction. It stops being the answer when someone reclaims some part of the
# ~414G this job does not own.
FREE_FLOOR_GB="${POGO_RECLAIM_FREE_FLOOR_GB:-20}"
CACHE_FLOOR_GB="${POGO_RECLAIM_CACHE_FLOOR_GB:-5}"
CRITICAL_FREE_GB="${POGO_RECLAIM_CRITICAL_FREE_GB:-2}"
ALERT_COOLDOWN_SEC="${POGO_RECLAIM_ALERT_COOLDOWN:-86400}"
ALERT_TO="${POGO_RECLAIM_ALERT_TO:-human}"
ALERT_FROM="${POGO_RECLAIM_ALERT_FROM:-mayor}"
DRY_RUN="${POGO_RECLAIM_DRY_RUN:-0}"

STATE_DIR="${POGO_RECLAIM_STATE_DIR:-${POGO_HOME:-$HOME/.pogo}/reclaim}"
LOCK_DIR="$STATE_DIR/reclaim.lock.d"
ALERT_STAMP="$STATE_DIR/last_alert"
STALE_LOCK_MIN=30

EXIT_OK=0
EXIT_FAILED=1
EXIT_UNKNOWN=3
EXIT_CANNOT_HELP=4
EXIT_DEFERRED=5

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "[$(ts)] pogo-reclaim: $*"; }

# Largest consumers under $HOME, measured 2026-08-12 while the volume was 100%
# full. Reproduced verbatim rather than re-measured on every fire: a `du` of
# $HOME costs minutes, and the point of the list is scale, not currency. It is
# dated in the output so a reader can tell a stale figure from a fresh one.
CONSUMERS_MEASURED="2026-08-12"
CONSUMERS="Library 73G, tools 15G, chrome 12G, research 9.8G, go 8.0G, .pogo 6.4G, Virtual Machines 5.0G, dev 4.6G"
# The single largest item this job is most likely to be MISTAKEN for handling.
# Same vendor, adjacent name, fifty times the size, different command, and
# deliberately out of scope — see the header.
SIBLING_CACHE="~/Library/Caches/go-build (the BUILD cache) measured 34G on $CONSUMERS_MEASURED and is NOT touched by this job; \`go clean -modcache\` does not reach it"

# --- measurement -----------------------------------------------------------

# kb_from_gb converts a (possibly fractional) GB threshold to KB.
kb_from_gb() { awk -v g="$1" 'BEGIN { printf "%d", g * 1024 * 1024 }'; }

# human_kb renders KB the way df -h does, so a log line can be compared against
# what an operator types by hand.
human_kb() {
    awk -v k="$1" 'BEGIN {
        if (k >= 1048576) printf "%.1fG", k / 1048576;
        else if (k >= 1024) printf "%.0fM", k / 1024;
        else printf "%dK", k;
    }'
}

# capacity_pct(used_kb, free_kb) reproduces the Capacity column of `df`, which
# is used/(used+available) and NOT used/total.
#
# On APFS those differ by a lot, because the volume reserves space that is in
# neither figure: this box measured 415.1G used of a 460.4G volume with 7.0G
# available — 90% by total, 98% by capacity, and `df -h` prints 99%. A log line
# that said 90% while every operator's `df -h` said 99% would be an instrument
# arguing with the tool it exists to agree with, on the one number the whole
# alarm turns on.
# It also ROUNDS UP, as BSD df does — `(used * 100 + d - 1) / d`. Rounding to
# nearest left this box reading 98% against df's 99%, and one point of daylight
# is enough for a reader to wonder which of the two is lying.
capacity_pct() {
    awk -v u="$1" -v f="$2" 'BEGIN { d = u + f; if (d <= 0) printf "?"; else printf "%d", int((u * 100 + d - 1) / d) }'
}

# df_field reads one column of `df -Pk`. -P is the POSIX form: it guarantees one
# physical line per filesystem, so a long device name cannot wrap the row and
# shift every column by one.
df_field() {
    local path="$1" col="$2"
    df -Pk "$path" 2>/dev/null | awk -v c="$col" 'NR == 2 { print $c }'
}

# --- lock ------------------------------------------------------------------
# Atomic mkdir, portable (macOS has no /usr/bin/flock). A `du` of a large cache
# plus a `go clean` can outlast the 30-minute sampling interval on a loaded box,
# and two concurrent `go clean -modcache` runs against one cache is not a state
# worth reasoning about.
mkdir -p "$STATE_DIR" 2>/dev/null
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
    log "lock held; a previous fire is still running — exiting 0"
    exit "$EXIT_OK"
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

# --- which copy of this script is running ----------------------------------
# The static-copy trap, answered in the log rather than in a doc. `stat -f` is
# BSD/macOS; the GNU spelling is the fallback so the line is still emitted when
# this runs from a checkout on Linux.
runner_mtime() {
    stat -f '%Sm' -t '%Y-%m-%dT%H:%M:%SZ' "$1" 2>/dev/null ||
        stat -c '%y' "$1" 2>/dev/null ||
        echo unknown
}
log "runner: $0 (mtime $(runner_mtime "$0")) — a merge does NOT refresh this copy; \`pogo service install-reclaim\` does"

# --- where is the cache ----------------------------------------------------
if [ -n "${POGO_RECLAIM_MODCACHE:-}" ]; then
    MODCACHE="$POGO_RECLAIM_MODCACHE"
elif command -v go >/dev/null 2>&1; then
    MODCACHE="$(go env GOMODCACHE 2>/dev/null)"
else
    # UNKNOWN, stated. Without `go` there is no reclaim to perform either, so
    # this is not a case where proceeding is an option — but it must not render
    # as "checked, nothing to do".
    log "UNKNOWN: \`go\` is not on PATH ($PATH) — cannot locate the module cache and could not reclaim it if it were found"
    exit "$EXIT_UNKNOWN"
fi

if [ -z "$MODCACHE" ] || [ ! -d "$MODCACHE" ]; then
    # An absent cache is genuinely nothing to do — but say which path was
    # checked, because a wrong GOMODCACHE reads exactly like an empty one.
    log "module cache not present at '${MODCACHE:-<empty>}' — nothing to reclaim"
    exit "$EXIT_OK"
fi

# --- step 1: the cheap measurement -----------------------------------------
# The volume that holds the CACHE, because that is the only volume this job can
# return space to. On this box it is also the boot volume, which is where the
# damage was observed; on a box where they differ, a full boot volume is outside
# what this script can do anything about and it must not claim otherwise.
FREE_KB="$(df_field "$MODCACHE" 4)"
TOTAL_KB="$(df_field "$MODCACHE" 2)"
USED_KB="$(df_field "$MODCACHE" 3)"
MOUNT="$(df -Pk "$MODCACHE" 2>/dev/null | awk 'NR == 2 { print $6 }')"

case "$FREE_KB" in
    '' | *[!0-9]*)
        log "UNKNOWN: could not read free space for $MODCACHE (df -Pk returned '${FREE_KB:-<empty>}')"
        exit "$EXIT_UNKNOWN"
        ;;
esac

FREE_FLOOR_KB="$(kb_from_gb "$FREE_FLOOR_GB")"
CACHE_FLOOR_KB="$(kb_from_gb "$CACHE_FLOOR_GB")"
CRITICAL_FREE_KB="$(kb_from_gb "$CRITICAL_FREE_GB")"

log "volume $MOUNT: $(human_kb "$FREE_KB") free of $(human_kb "$TOTAL_KB") ($(capacity_pct "$USED_KB" "$FREE_KB")% capacity); free floor $(human_kb "$FREE_FLOOR_KB")"

if [ "$FREE_KB" -ge "$FREE_FLOOR_KB" ]; then
    log "free space is above the floor — nothing to do (the cache was not measured; that \`du\` is only paid when the disk is low)"
    exit "$EXIT_OK"
fi

# --- step 2: the expensive measurement -------------------------------------
CACHE_KB="$(du -sk "$MODCACHE" 2>/dev/null | awk 'NR == 1 { print $1 }')"
case "${CACHE_KB:-}" in
    '' | *[!0-9]*)
        log "UNKNOWN: could not measure $MODCACHE (du -sk returned '${CACHE_KB:-<empty>}') — free space IS below the floor and this fire did not establish whether the cache is why"
        exit "$EXIT_UNKNOWN"
        ;;
esac
log "module cache $MODCACHE: $(human_kb "$CACHE_KB"); cache floor $(human_kb "$CACHE_FLOOR_KB")"

# scope_note is the sentence this ticket exists for as much as the `go clean`
# is. It is computed from the run's own numbers rather than fixed, so it cannot
# quietly become wrong as the box changes.
scope_note() {
    local free_kb="$1" total_kb="$2" used_kb="$3" reclaimed_kb="${4:-0}"
    echo "  WHAT THIS DOES NOT FIX:"
    if [ "$reclaimed_kb" -gt 0 ]; then
        echo "    This job reclaimed $(human_kb "$reclaimed_kb") — the Go module cache, one directory."
    else
        echo "    This job reclaims the Go module cache. That is one directory."
    fi
    echo "    $MOUNT is at $(capacity_pct "$used_kb" "$free_kb")% capacity: $(human_kb "$used_kb") used of $(human_kb "$total_kb"), $(human_kb "$free_kb") free."
    echo "    Largest consumers under \$HOME, measured $CONSUMERS_MEASURED: $CONSUMERS."
    echo "    NOT RECLAIMED BY THIS JOB: $SIBLING_CACHE."
    echo "    A cache reclaim against a fill of this size buys HEADROOM, NOT A FIX. If this"
    echo "    job is firing repeatedly, the disk is filling from somewhere it does not own,"
    echo "    and the next full-disk build failure will still present as a broken branch."
}

# alert mails the one reader who can act on a fill this job cannot reclaim.
# Rate-limited to once per cooldown so a disk that stays full does not deliver a
# mail every 30 minutes — which is how a real signal gets filtered into a rule.
# Best-effort throughout: a box without `mg` still gets the full log.
alert() {
    local subject="$1" body="$2" now last
    now="$(date +%s)"
    last=0
    [ -f "$ALERT_STAMP" ] && last="$(cat "$ALERT_STAMP" 2>/dev/null || echo 0)"
    case "$last" in '' | *[!0-9]*) last=0 ;; esac
    if [ $((now - last)) -lt "$ALERT_COOLDOWN_SEC" ]; then
        log "alert suppressed (last sent $((now - last))s ago, cooldown ${ALERT_COOLDOWN_SEC}s) — the log below is the full record"
        return 0
    fi
    if ! command -v mg >/dev/null 2>&1; then
        log "alert NOT SENT: \`mg\` is not on PATH ($PATH) — nobody was told outside this log"
        return 0
    fi
    if printf '%s\n' "$body" | mg mail send "$ALERT_TO" --from="$ALERT_FROM" --subject="$subject" --body-file - >/dev/null 2>&1; then
        echo "$now" > "$ALERT_STAMP.tmp" && mv "$ALERT_STAMP.tmp" "$ALERT_STAMP"
        log "alert sent to $ALERT_TO: $subject"
    else
        log "alert NOT SENT: \`mg mail send $ALERT_TO\` failed — nobody was told outside this log"
    fi
}

if [ "$CACHE_KB" -lt "$CACHE_FLOOR_KB" ]; then
    # THE CASE THAT MUST NOT READ AS SUCCESS.
    #
    # Free space is below the floor and the cache is not why. A free-space-only
    # trigger would fire here, delete a small cache, exit 0, and leave a log line
    # that an operator reads as "the disk job ran". Every subsequent full-disk
    # build failure then gets attributed to the branch, which is exactly the cost
    # this ticket was filed to stop.
    log "CANNOT HELP: free space is below the floor and the Go module cache is NOT what is filling this volume."
    log "  cache $(human_kb "$CACHE_KB") < floor $(human_kb "$CACHE_FLOOR_KB"); reclaiming it would return $(human_kb "$CACHE_KB") of a $(human_kb "$USED_KB") fill and cost a full re-download."
    scope_note "$FREE_KB" "$TOTAL_KB" "$USED_KB" 0
    log "  Not firing \`go clean -modcache\`. This is a disk that needs a human, not a cron."
    alert "disk low on $(hostname -s 2>/dev/null || echo this host) and the Go cache is not why" \
        "$(printf '%s\n' \
            "$MOUNT is at $(capacity_pct "$USED_KB" "$FREE_KB")% capacity — $(human_kb "$FREE_KB") free of $(human_kb "$TOTAL_KB")." \
            "" \
            "The Go module cache reclaim (com.pogo.reclaim, mg-b7c3) did NOT fire: the cache is" \
            "$(human_kb "$CACHE_KB"), below its $(human_kb "$CACHE_FLOOR_KB") floor. Deleting it would return" \
            "$(human_kb "$CACHE_KB") of a $(human_kb "$USED_KB") fill and cost a full re-download." \
            "" \
            "This is the case that job cannot fix. Largest consumers under \$HOME, measured" \
            "$CONSUMERS_MEASURED: $CONSUMERS." \
            "" \
            "NOT reclaimed by this job: $SIBLING_CACHE." \
            "" \
            "Why it matters now: ./build.sh is the refinery's merge gate, and on a full volume" \
            "it fails at the link step with 'no space left on device' — which reads like a" \
            "broken branch. Full log: ${POGO_RECLAIM_LOG:-~/Library/Logs/pogo/pogo-reclaim.log}")"
    exit "$EXIT_CANNOT_HELP"
fi

# --- step 3: is anything building right now --------------------------------
#
# Returns 0 and sets IN_FLIGHT_WHY when something is building. `pgrep -x` matches
# the EXACT process name; `pgrep -f` is never used here, because a pattern
# matched against full command lines matches half the fleet (see the standing
# rule about unanchored pkill on this box).
IN_FLIGHT_WHY=""
builds_in_flight() {
    local n name
    for name in go compile link; do
        n="$(pgrep -x "$name" 2>/dev/null | wc -l | tr -d ' ')"
        if [ "${n:-0}" -gt 0 ]; then
            IN_FLIGHT_WHY="$n process(es) named '$name' are running"
            return 0
        fi
    done
    if command -v pogo >/dev/null 2>&1; then
        # STDOUT ONLY: the CLI writes advisories to stderr, and folding them into
        # the JSON makes the parse fail in a way that reads as "no MRs".
        local q processing
        q="$(pogo refinery queue --json 2>/dev/null)"
        if [ -n "$q" ]; then
            if command -v jq >/dev/null 2>&1; then
                processing="$(printf '%s' "$q" | jq -r '[.[] | select(.status == "processing")] | length' 2>/dev/null)"
            else
                processing="$(printf '%s' "$q" | grep -c '"status": *"processing"')"
            fi
            case "${processing:-}" in '' | *[!0-9]*) processing=0 ;; esac
            if [ "$processing" -gt 0 ]; then
                IN_FLIGHT_WHY="$processing refinery merge(s) are processing"
                return 0
            fi
        else
            # Not a pass and not a block: say the check could not be made, and
            # let the caller decide. Silence here would be indistinguishable
            # from a quiet queue.
            IN_FLIGHT_WHY=""
            log "  in-flight check PARTIAL: \`pogo refinery queue --json\` returned nothing (daemon down?) — only the process check was made"
        fi
    else
        log "  in-flight check PARTIAL: \`pogo\` is not on PATH — only the process check was made"
    fi
    return 1
}

if [ "$FREE_KB" -lt "$CRITICAL_FREE_KB" ]; then
    log "free space $(human_kb "$FREE_KB") is below the critical floor $(human_kb "$CRITICAL_FREE_KB") — NOT deferring for in-flight builds; at this level they fail with or without this run"
elif builds_in_flight; then
    log "DEFERRED: $IN_FLIGHT_WHY. \`go clean -modcache\` deletes trees a running build is reading, so firing now would break that build with a missing-file error — the exact reading this job exists to prevent."
    log "  The next fire (every $((${POGO_RECLAIM_INTERVAL_SEC:-1800} / 60)) min) retries. Below $(human_kb "$CRITICAL_FREE_KB") free this deferral is skipped."
    exit "$EXIT_DEFERRED"
fi

# --- step 4: reclaim -------------------------------------------------------
if [ "$DRY_RUN" != "0" ]; then
    log "DRY RUN: would run \`go clean -modcache\` on $MODCACHE ($(human_kb "$CACHE_KB")); no files removed"
    scope_note "$FREE_KB" "$TOTAL_KB" "$USED_KB" 0
    exit "$EXIT_OK"
fi

log "reclaiming: go clean -modcache ($(human_kb "$CACHE_KB") in $MODCACHE)"
CLEAN_OUT="$(go clean -modcache 2>&1)"
CLEAN_RC=$?
[ -n "$CLEAN_OUT" ] && log "  go clean output: $CLEAN_OUT"

if [ "$CLEAN_RC" -ne 0 ]; then
    log "FAILED: \`go clean -modcache\` exited $CLEAN_RC — the cache was NOT reclaimed and the volume is still $(human_kb "$FREE_KB") free"
    exit "$EXIT_FAILED"
fi

FREE_AFTER_KB="$(df_field "$MODCACHE" 4)"
USED_AFTER_KB="$(df_field "$MODCACHE" 3)"
case "${FREE_AFTER_KB:-}" in
    '' | *[!0-9]*)
        # The clean succeeded; only the post-measurement failed. Report what is
        # known and do not invent a reclaimed figure.
        log "reclaimed the module cache, but the post-run \`df\` could not be read — the reclaimed figure is UNKNOWN"
        exit "$EXIT_UNKNOWN"
        ;;
esac
RECLAIMED_KB=$((FREE_AFTER_KB - FREE_KB))
[ "$RECLAIMED_KB" -lt 0 ] && RECLAIMED_KB=0

log "reclaimed $(human_kb "$RECLAIMED_KB"): $MOUNT went from $(human_kb "$FREE_KB") free to $(human_kb "$FREE_AFTER_KB") free"
scope_note "$FREE_AFTER_KB" "$TOTAL_KB" "$USED_AFTER_KB" "$RECLAIMED_KB"

if [ "$FREE_AFTER_KB" -lt "$FREE_FLOOR_KB" ]; then
    # The reclaim happened AND the disk is still below the floor. This is the
    # other half of the "we installed the cron" failure: the job worked, did
    # everything it can do, and the condition it was installed for persists.
    log "STILL BELOW THE FLOOR after a successful reclaim: $(human_kb "$FREE_AFTER_KB") free < $(human_kb "$FREE_FLOOR_KB"). This job has now done everything it can do."
    alert "disk still low on $(hostname -s 2>/dev/null || echo this host) after the Go cache reclaim" \
        "$(printf '%s\n' \
            "com.pogo.reclaim (mg-b7c3) ran \`go clean -modcache\` and reclaimed $(human_kb "$RECLAIMED_KB")." \
            "" \
            "$MOUNT is STILL at $(capacity_pct "$USED_AFTER_KB" "$FREE_AFTER_KB")% capacity — $(human_kb "$FREE_AFTER_KB") free of $(human_kb "$TOTAL_KB")," \
            "below the $(human_kb "$FREE_FLOOR_KB") floor. The reclaim worked and was not enough." \
            "" \
            "Largest consumers under \$HOME, measured $CONSUMERS_MEASURED: $CONSUMERS." \
            "" \
            "./build.sh is the refinery's merge gate; on a full volume it fails at the link" \
            "step with 'no space left on device', which reads like a broken branch." \
            "Full log: ${POGO_RECLAIM_LOG:-~/Library/Logs/pogo/pogo-reclaim.log}")"
fi

exit "$EXIT_OK"
