#!/bin/bash
# =============================================================================
# PER-STEP PROFILE FOR THE MERGE GATE
# =============================================================================
#
# WHY THIS EXISTS (mg-eed9):
#   `./build.sh` is the refinery's quality gate. It runs at least TWICE per
#   merge — once in the polecat's worktree, once as the gate — and again on
#   every retry, and it is what sets the fleet's per-repo concurrency cap of 3
#   ("what saturates is one repo's test suite run concurrently", mg-3977).
#   Build time is therefore not an annoyance, it is the dispatch queue.
#
#   And nobody could say what it was spent ON. The gate emitted a flat stream
#   of `echo` announcements and hundreds of lines of suite output with no
#   timings in it at all, so every proposal to make it faster — select by what
#   changed, move the live suites to a schedule, parallelise the shell suites,
#   fix hermeticity first — was an argument about a distribution nobody had
#   measured. The ticket that raised this asked for the profile FIRST and
#   explicitly forbade starting with a fix.
#
#   This file is the instrument. `test.sh` and `build.sh` wrap every step in
#   `gate_step`, and each run prints a ranked table at the end.
#
# WHY IT MEASURES CPU AND NOT ONLY WALL-CLOCK — the load-bearing design choice:
#   The observation that started this was a gate run at 7m19s showing
#   `cpu: 0.0 cores busy`: the gate was WAITING, not computing. Wall-clock
#   alone cannot tell those apart, and they take OPPOSITE remedies —
#
#       compute-bound       many cores busy for a long time. Only doing less
#                           work helps: run fewer packages, cache, select by
#                           what changed. Parallelising helps only until the
#                           cores run out, which is also what sets the cap.
#       wall-clock-bound    few cores busy for a long time. A sleep, a daemon
#                           boot wait, a poll interval. It costs the gate its
#                           whole duration and costs the HOST almost nothing,
#                           so it parallelises nearly free and is the cheapest
#                           thing to move onto a schedule.
#
#   A ranked list by wall-clock alone would put those two in the same column
#   and invite the same fix for both. So every row carries `cores` — CPU
#   seconds divided by wall seconds — and that number, not the ranking, is what
#   says which lever applies.
#
# HOW THE CPU NUMBER IS OBTAINED, AND THE TRAP IN IT:
#   bash's `times` builtin reports the cumulative user+sys time of children
#   this shell has WAITED FOR, which is what we want: a step's whole process
#   subtree lands in it, because each intermediate process's own children are
#   folded into its total when it is reaped.
#
#   The trap: `t=$(times)` DOES NOT WORK, and it fails silently. A command
#   substitution forks, and a forked child's children-times start at zero, so
#   the capture reads `0m0.000s 0m0.000s` forever and every step looks
#   wall-clock-bound. Measured on bash 3.2.57 (darwin): the same call reads
#   0.242s from a function body in the main shell and 0.000s from a subshell
#   one line later. `times > "$file"` is a builtin with a redirection and does
#   NOT fork, so the value is written by the shell that did the waiting; the
#   parse of the file may then subshell freely. Every read below goes through
#   the file for that reason.
#
#   What it does NOT count: descendants that are never waited for. A test that
#   detaches a daemon and abandons it contributes that daemon's CPU to nobody.
#   That biases `cores` DOWNWARD for exactly the live-daemon suites, i.e. it
#   can call a step wall-clock-bound that was partly computing elsewhere. It
#   cannot do the reverse, so a high `cores` reading is trustworthy and a low
#   one is a floor.
#
# WHAT IT COSTS: two `date`s, one `uptime` and one `times` per step — under
#   10ms against steps measured in seconds and minutes. It is on by default
#   because a profile nobody runs is not an instrument, and because the
#   distribution under FLEET LOAD is the interesting one and cannot be
#   requested in advance — it has to already be recording when the load
#   happens.
#
# USAGE:
#   SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
#   source "$SCRIPT_DIR/scripts/lib/gate-profile.sh"
#   gate_profile_begin "test.sh"
#   trap 'gate_profile_report' EXIT
#   gate_step "Testing neovim plugin" bash nvim/test_nvim.sh
#
#   `gate_step` echoes its label exactly as the bare `echo` it replaces did,
#   runs the command in the CURRENT shell, and returns the command's status
#   unchanged — so `set -e` still aborts the script on a failing step, and the
#   EXIT trap still prints the profile of everything that ran before it. A
#   failed gate is the run whose profile is most worth having.
#
# ENVIRONMENT:
#   POGO_GATE_PROFILE=0        disable; `gate_step` still runs the command and
#                              echoes the label, it just records nothing.
#   POGO_GATE_PROFILE_JSON=<f> append one JSON object per run to <f>. Default
#                              unset: the gate writes no file anywhere. The
#                              gate runs in ephemeral polecat worktrees under
#                              a developer's live HOME, and a default write
#                              path would be a new piece of exactly the live-
#                              state coupling this ticket is about (mg-5551).
#                              stdout is the durable channel — the refinery
#                              already captures a running gate's output text
#                              (mg-9adc), so the table lands in the record of
#                              the run it describes, with no new file.
# =============================================================================

GATE_PROFILE_LABEL=""
GATE_PROFILE_COUNT=0
GATE_PROFILE_T0=""
GATE_PROFILE_CLOCK=""
GATE_PROFILE_TIMES_FILE=""
GATE_PROFILE_CPU0=""

# Parallel arrays; bash 3.2 has no associative arrays and this file runs on the
# stock /bin/bash of a mac as well as on ubuntu-latest.
GATE_PROFILE_NAMES=()
GATE_PROFILE_WALL=()
GATE_PROFILE_CPU=()
GATE_PROFILE_LOAD=()
GATE_PROFILE_STATUS=()

gate_profile_enabled() {
    [ "${POGO_GATE_PROFILE:-1}" != "0" ]
}

# Pick a clock once. `date +%s%N` is nanoseconds on GNU date and on darwin 24;
# older BSD date has no %N and emits the epoch with a literal "N" glued on,
# which would parse as a plausible-looking number three orders of magnitude
# wrong. So the probe demands all-digits AND full width, and falls back rather
# than trusting the format string.
gate_profile_detect_clock() {
    local probe
    probe="$(date +%s%N 2>/dev/null)"
    case "$probe" in
        '' | *[!0-9]*) ;;
        *)
            if [ "${#probe}" -ge 16 ]; then
                GATE_PROFILE_CLOCK="ns"
                return
            fi
            ;;
    esac
    if command -v perl >/dev/null 2>&1; then
        GATE_PROFILE_CLOCK="perl"
        return
    fi
    GATE_PROFILE_CLOCK="seconds"
}

gate_profile_now_ns() {
    case "$GATE_PROFILE_CLOCK" in
        ns) date +%s%N ;;
        perl) perl -MTime::HiRes=time -e 'printf "%.0f", Time::HiRes::time() * 1000000000' ;;
        *) echo "$(date +%s)000000000" ;;
    esac
}

# One-minute load average, from `uptime`, on both label spellings:
#   darwin  "load averages: 4.24 4.32 4.48"   (space separated)
#   linux   "load average: 0.52, 0.58, 0.59"  (comma separated)
gate_profile_load() {
    local raw
    raw="$(uptime 2>/dev/null | sed -e 's/.*load average[s]*: *//' -e 's/,.*//' -e 's/ .*//')"
    case "$raw" in
        '' | *[!0-9.]*) echo "-" ;;
        *) echo "$raw" ;;
    esac
}

# Cumulative child CPU seconds (user+sys) for THIS shell. Must be called from
# the shell that waited for the children — see the header. Writes through a
# file precisely so the parse can subshell without destroying the reading.
gate_profile_child_cpu() {
    awk 'NR == 2 {
        total = 0
        for (i = 1; i <= NF; i++) {
            split($i, part, "m")
            sub(/s$/, "", part[2])
            total += part[1] * 60 + part[2]
        }
        printf "%.3f", total
        found = 1
    }
    END { if (!found) printf "0.000" }' "$GATE_PROFILE_TIMES_FILE"
}

gate_profile_begin() {
    GATE_PROFILE_LABEL="${1:-gate}"
    gate_profile_enabled || return 0

    gate_profile_detect_clock
    GATE_PROFILE_TIMES_FILE="$(mktemp "${TMPDIR:-/tmp}/pogo-gate-profile.XXXXXX")"
    GATE_PROFILE_T0="$(gate_profile_now_ns)"
    times > "$GATE_PROFILE_TIMES_FILE"
    GATE_PROFILE_CPU0="$(gate_profile_child_cpu)"
}

# gate_step <label> <command> [args...]
gate_step() {
    local name="$1"
    shift

    if ! gate_profile_enabled; then
        echo "$name"
        "$@"
        return $?
    fi

    local load start end cpu_before cpu_after status restore_errexit=""
    load="$(gate_profile_load)"
    times > "$GATE_PROFILE_TIMES_FILE"
    cpu_before="$(gate_profile_child_cpu)"
    start="$(gate_profile_now_ns)"

    echo "$name"

    # errexit is suspended for exactly the duration of the step, so a failing
    # step is RECORDED before it propagates. Restoring it is conditional on it
    # having been set: this file is sourced by callers, and turning on an
    # option the caller never asked for is a way to break a script by
    # profiling it.
    case "$-" in *e*) restore_errexit="yes" ;; esac
    set +e
    "$@"
    status=$?
    if [ -n "$restore_errexit" ]; then
        set -e
    fi

    end="$(gate_profile_now_ns)"
    times > "$GATE_PROFILE_TIMES_FILE"
    cpu_after="$(gate_profile_child_cpu)"

    GATE_PROFILE_NAMES[$GATE_PROFILE_COUNT]="$name"
    GATE_PROFILE_WALL[$GATE_PROFILE_COUNT]="$(awk -v a="$start" -v b="$end" 'BEGIN { d = (b - a) / 1000000000; if (d < 0) d = 0; printf "%.2f", d }')"
    GATE_PROFILE_CPU[$GATE_PROFILE_COUNT]="$(awk -v a="$cpu_before" -v b="$cpu_after" 'BEGIN { d = b - a; if (d < 0) d = 0; printf "%.2f", d }')"
    GATE_PROFILE_LOAD[$GATE_PROFILE_COUNT]="$load"
    GATE_PROFILE_STATUS[$GATE_PROFILE_COUNT]="$status"
    GATE_PROFILE_COUNT=$((GATE_PROFILE_COUNT + 1))

    return "$status"
}

gate_profile_report() {
    gate_profile_enabled || return 0
    [ "$GATE_PROFILE_COUNT" -gt 0 ] || return 0

    local total_wall total_cpu now i
    now="$(gate_profile_now_ns)"
    total_wall="$(awk -v a="$GATE_PROFILE_T0" -v b="$now" 'BEGIN { printf "%.2f", (b - a) / 1000000000 }')"

    total_cpu=0
    for ((i = 0; i < GATE_PROFILE_COUNT; i++)); do
        total_cpu="$(awk -v t="$total_cpu" -v c="${GATE_PROFILE_CPU[$i]}" 'BEGIN { printf "%.2f", t + c }')"
    done

    echo ""
    echo "============================================================================="
    echo "GATE STEP PROFILE — ${GATE_PROFILE_LABEL}: ${GATE_PROFILE_COUNT} steps, ${total_wall}s wall, ${total_cpu}s cpu"
    echo ""
    printf '  %5s  %9s  %7s  %9s  %7s  %6s  %s\n' "rank" "wall" "share" "cpu" "cores" "load" "step"

    # Sorted by wall descending. sort(1) rather than a shell sort: the rows are
    # already text and the ranking is the point of the table.
    for ((i = 0; i < GATE_PROFILE_COUNT; i++)); do
        printf '%s\t%s\t%s\t%s\t%s\n' \
            "${GATE_PROFILE_WALL[$i]}" \
            "${GATE_PROFILE_CPU[$i]}" \
            "${GATE_PROFILE_LOAD[$i]}" \
            "${GATE_PROFILE_STATUS[$i]}" \
            "${GATE_PROFILE_NAMES[$i]}"
    done | sort -t$'\t' -k1,1nr | awk -F "\t" -v total="$total_wall" '
        BEGIN { rank = 0 }
        {
            rank++
            wall = $1 + 0
            cpu = $2 + 0
            share = (total > 0) ? (wall * 100.0 / total) : 0
            cores = (wall > 0) ? (cpu / wall) : 0
            marker = ($4 != "0") ? "  [FAILED]" : ""
            printf "  %5d  %8.2fs  %6.1f%%  %8.2fs  %7.2f  %6s  %s%s\n", rank, wall, share, cpu, cores, $3, $5, marker
        }'

    echo ""
    echo "  wall   wall-clock seconds the step held the gate."
    echo "  cpu    CPU seconds (user+sys) of the step's reaped process subtree."
    echo "  cores  cpu/wall — how many cores the step kept busy on average."
    echo "  load   1-minute host load average sampled as the step STARTED, so a"
    echo "         row measured under fleet load is distinguishable from a quiet one."
    echo ""
    echo "  A step with a high 'cores' is COMPUTE-BOUND: it is doing work, it"
    echo "  contends with every other gate on the host, and only doing less work"
    echo "  makes it faster. A step with 'cores' near zero is WALL-CLOCK-BOUND —"
    echo "  a sleep, a daemon boot wait, a poll interval. It costs the gate its"
    echo "  full duration and costs the host almost nothing, so it is the cheapest"
    echo "  thing to overlap or move off the merge path. 'cores' is a FLOOR:"
    echo "  detached descendants that are never waited for are counted for nobody."
    echo "============================================================================="

    gate_profile_write_json "$total_wall" "$total_cpu"

    # The scratch file the `times` readings go through. Removed here rather than
    # under a trap of this file's own: callers install their own EXIT trap to
    # call this function, and a second trap installed from a sourced library
    # would silently replace theirs.
    [ -n "$GATE_PROFILE_TIMES_FILE" ] && rm -f "$GATE_PROFILE_TIMES_FILE"
    return 0
}

# Optional machine-readable record: one JSON object per run, appended. Off
# unless POGO_GATE_PROFILE_JSON names a path — see the header for why there is
# no default location.
gate_profile_write_json() {
    local total_wall="$1" total_cpu="$2" out="${POGO_GATE_PROFILE_JSON:-}" i sep=""
    [ -n "$out" ] || return 0

    {
        printf '{"label":"%s","wall_seconds":%s,"cpu_seconds":%s,"steps":[' \
            "$GATE_PROFILE_LABEL" "$total_wall" "$total_cpu"
        for ((i = 0; i < GATE_PROFILE_COUNT; i++)); do
            printf '%s{"step":"%s","wall_seconds":%s,"cpu_seconds":%s,"load":"%s","status":%s}' \
                "$sep" \
                "$(printf '%s' "${GATE_PROFILE_NAMES[$i]}" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')" \
                "${GATE_PROFILE_WALL[$i]}" \
                "${GATE_PROFILE_CPU[$i]}" \
                "${GATE_PROFILE_LOAD[$i]}" \
                "${GATE_PROFILE_STATUS[$i]}"
            sep=","
        done
        printf ']}\n'
    } >> "$out"
}
