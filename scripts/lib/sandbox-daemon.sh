#!/bin/bash
# =============================================================================
# SANDBOX DAEMON PLUMBING FOR THE LIVE POGOD CONTROLS (mg-3412)
# =============================================================================
#
# Source this from a control that stands up a throwaway pogod:
#
#   source "$REPO_ROOT/scripts/lib/sandbox-daemon.sh"
#   sandbox_port_reserve                        # -> $SANDBOX_PORT, lock held
#   sandbox_daemon_start "$BIN" "$SANDBOX_PORT" "$LOG" /agents/drain
#                                               # -> $SANDBOX_DAEMON_PID, proven ours
#
# WHY THIS EXISTS — THE DEFECT IT REPLACES
# ----------------------------------------
# Both live controls used to pick a port like this:
#
#     for candidate in $(seq 17731 17799); do
#         if ! curl -sf ... "http://127.0.0.1:$candidate/..."; then PORT=$candidate; break; fi
#     done
#
# That is a probe, not a reservation, and the two are not the same thing. The
# window between "nothing answered" and "our pogod bound it" is wide open, and
# every concurrent run of every control walks the SAME range from the SAME end,
# so under load they all choose the same first candidate. The loser's pogod hits
# `log.Fatalf: failed to listen` (cmd/pogod/main.go) and exits — while the
# readiness curl that follows is answered, happily, by the WINNER'S daemon.
#
# From there the control is driving a stranger's sandbox: it registers schedules
# another run is deleting, deletes ones another run is asserting on, and — worst
# — §5's "kill the daemon and prove a dead one reads as UNKNOWN" kills its own
# already-dead pid while the foreign daemon keeps answering. The observed result
# (mg-3412, polecat-4469's gate) was fourteen assertion failures including
# verbatim fail-open security findings — `FAIL-OPEN: dead pogod + a live
# witnessed polecat returned '0|0'` — about a tree that was provably fine: the
# same branch, resubmitted unchanged, merged clean.
#
# THE THREE GUARANTEES THIS FILE ADDS, AND WHY EACH IS NEEDED
# -----------------------------------------------------------
# 1. AN ATOMIC RESERVATION, not a probe. `set -o noclobber` makes the lock file
#    an O_EXCL create, so exactly one process on the box can hold a given port —
#    a fact the kernel decides, not a fact we inferred from silence one moment
#    earlier. Combined with a per-process start offset, four concurrent runs take
#    four different ports on their first try rather than fighting over one.
#
# 2. THE DAEMON MUST BE PROVEN OURS. A reservation stops US colliding with
#    ourselves; it says nothing about anything else on the machine. So after the
#    launch we check that the process we started is still alive (a pogod that
#    lost the bind is a pogod that is GONE, which no amount of curl-answered
#    green would reveal), and, where lsof is available, that the pid holding the
#    listening socket IS ours. Ownership is asserted, never assumed.
#
# 3. INFRASTRUCTURE FAILS AS INFRASTRUCTURE. When any of the above cannot be
#    established the run stops AT THAT POINT with a distinct banner and a
#    distinct exit code — it does not proceed to produce assertion failures about
#    a tree it never managed to test. This is the whole of mg-3412's third ask:
#    the incident's first line already said "could not remove ... from the
#    sandbox daemon" and the next thirteen buried it, which cost a coordinator
#    fifteen minutes and produced the wrong diagnosis.
#
# WHAT THIS FILE DELIBERATELY DOES NOT DO
# ---------------------------------------
# It does not retry, and it must never learn to. A control that reruns itself on
# failure converts a real regression into a merged one — mg-3412 forbids it in
# terms. Setup failures are LOUD AND TERMINAL; assertion failures are the
# control's verdict. Keeping those two apart is the entire point, and anything
# here that made a red run more likely to end green would be the fail-open trade
# this file was written to close.
# =============================================================================

# The exit code an infrastructure failure leaves. Deliberately not 1: `exit 1` is
# what the assertion tally uses, and a caller that cannot tell "the suite ran and
# something failed" from "the suite never ran" is back where mg-3412 started.
# Overridable so a control with its own convention can pick another, but every
# caller today wants this one.
SANDBOX_SETUP_RC="${SANDBOX_SETUP_RC:-99}"

# The port range. Deliberately NOT the old 17731-17799: any process still running
# the pre-mg-3412 probe loop parks in that range, and a fresh range means the new
# reservation scheme does not have to share with a scheme that does not honour
# it. 500 ports is far more than the fleet's concurrency and leaves room for the
# occasional leaked lock to be irrelevant rather than fatal.
SANDBOX_PORT_LO="${POGO_SANDBOX_PORT_LO:-18500}"
SANDBOX_PORT_HI="${POGO_SANDBOX_PORT_HI:-18999}"

# Per-uid, and under /tmp rather than $TMPDIR: on macOS $TMPDIR is a per-user
# per-bootstrap path that agents spawned by different parents do not reliably
# share, and a lock directory two processes cannot both see is not a lock. It is
# also read BEFORE any caller overrides HOME, which is why it hangs off neither.
SANDBOX_PORT_LOCKDIR="${POGO_SANDBOX_PORT_LOCKDIR:-/tmp/pogo-sandbox-ports-$(id -u)}"

# Set by sandbox_port_reserve / sandbox_daemon_start. Globals rather than stdout
# ON PURPOSE: these functions can end the run, and `PORT="$(reserve)"` would run
# the exit inside a command substitution — killing only the subshell, handing the
# caller an empty string, and carrying straight on into the cascade this file
# exists to prevent. A function that may terminate the script must never be
# called in a context that swallows termination.
SANDBOX_PORT=""
SANDBOX_PORT_LOCK=""
SANDBOX_DAEMON_PID=""

# sandbox_setup_fail MSG [LOGFILE] — stop the run as INFRASTRUCTURE, loudly.
#
# The wording is aimed at the reader of a failed gate at 03:00, because that is
# who gets hurt by ambiguity here. It says, in the output itself, that this run
# makes no claim about the tree — so a real regression cannot hide behind it and
# it cannot be mistaken for one.
sandbox_setup_fail() {
    local msg="$1" log="${2:-}"
    echo ""
    echo "=== SETUP FAILURE — the sandbox could not be established (exit $SANDBOX_SETUP_RC) ==="
    echo "  $msg"
    if [ -n "$log" ] && [ -s "$log" ]; then
        echo ""
        echo "  daemon log:"
        sed 's/^/    /' "$log"
    fi
    echo ""
    echo "  This is an INFRASTRUCTURE failure, not an assertion failure. The controls"
    echo "  in this file did NOT run, so this result says NOTHING about the tree under"
    echo "  test: do not read it as a regression, and do not read a later green run as"
    echo "  having cleared one. Fix the sandbox and run it again."
    exit "$SANDBOX_SETUP_RC"
}

# sandbox_port_reserve — claim a private port for this process, atomically.
# Sets $SANDBOX_PORT and $SANDBOX_PORT_LOCK. Ends the run if it cannot.
sandbox_port_reserve() {
    local span lo hi start i port lockpath owner

    lo="$SANDBOX_PORT_LO"; hi="$SANDBOX_PORT_HI"
    span=$(( hi - lo + 1 ))

    mkdir -p "$SANDBOX_PORT_LOCKDIR" 2>/dev/null \
        || sandbox_setup_fail "could not create the sandbox port lock directory $SANDBOX_PORT_LOCKDIR"

    # Prune locks whose owner died between the O_EXCL create and the pid write —
    # a window of microseconds, but one that would otherwise leak a port forever.
    # Age-based because that is the only evidence available for a lock with no
    # readable owner; an hour is far longer than any control's runtime.
    find "$SANDBOX_PORT_LOCKDIR" -type f -mmin +60 -delete 2>/dev/null

    # Start where nobody else starts. Two runs walking 18500 upward would collide
    # on their first candidate every time and then walk in lockstep; offsetting by
    # pid makes the common case a first-try win for everyone.
    start=$(( $$ % span ))

    for (( i = 0; i < span; i++ )); do
        port=$(( lo + (start + i) % span ))
        lockpath="$SANDBOX_PORT_LOCKDIR/$port"

        if ! ( set -o noclobber; printf '%s\n' "$$" > "$lockpath" ) 2>/dev/null; then
            # Held. Reclaim ONLY on positive proof the owner is gone: an empty or
            # unreadable owner file is a claim in flight, not a dead one, and
            # stealing it would put two runs on one port — the exact bug. With 500
            # ports, skipping an ambiguous lock costs nothing; the prune above is
            # what stops an unreadable one lasting.
            owner="$(cat "$lockpath" 2>/dev/null)"
            [ -n "$owner" ] || continue
            kill -0 "$owner" 2>/dev/null && continue
            rm -f "$lockpath" 2>/dev/null
            ( set -o noclobber; printf '%s\n' "$$" > "$lockpath" ) 2>/dev/null || continue
        fi

        # We hold the reservation. It binds every process that honours this
        # scheme — and nothing else on the box. So before committing, confirm the
        # port really is idle. If something is listening, drop the claim and move
        # on rather than handing our caller a port it will lose the bind on.
        if sandbox_port_in_use "$port"; then
            rm -f "$lockpath" 2>/dev/null
            continue
        fi

        SANDBOX_PORT="$port"
        SANDBOX_PORT_LOCK="$lockpath"
        return 0
    done

    sandbox_setup_fail "no free port in $lo-$hi for the sandbox daemon (lockdir $SANDBOX_PORT_LOCKDIR); every candidate was reserved by a live process or already had a listener"
}

# sandbox_port_in_use PORT — true if anything is listening. Two independent
# reads, because either alone can be wrong in the direction that matters: lsof is
# blind to other users' sockets, and a listener that is not an HTTP server (or is
# still starting) answers no curl.
sandbox_port_in_use() {
    local port="$1"
    if command -v lsof >/dev/null 2>&1; then
        [ -n "$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null)" ] && return 0
    fi
    curl -s --max-time 1 "http://127.0.0.1:$port/" >/dev/null 2>&1 && return 0
    return 1
}

# sandbox_port_release — give the claim back. Safe to call from an EXIT trap and
# safe to call twice.
sandbox_port_release() {
    [ -n "$SANDBOX_PORT_LOCK" ] || return 0
    rm -f "$SANDBOX_PORT_LOCK" 2>/dev/null
    SANDBOX_PORT_LOCK=""
}

# sandbox_daemon_start BIN PORT LOGFILE READY_PATH — launch a sandbox pogod and
# do not return until it is answering AND has been shown to be ours.
# Sets $SANDBOX_DAEMON_PID. Ends the run if either cannot be established.
sandbox_daemon_start() {
    local bin="$1" port="$2" log="$3" ready="$4" pid

    "$bin" -port "$port" > "$log" 2>&1 &
    pid=$!

    for _ in $(seq 1 80); do
        # THE CHECK THE OLD LOOP DID NOT HAVE, and the one that would have ended
        # the mg-3412 episode on its first line. pogod log.Fatalf's when it cannot
        # bind, so a lost race is a DEAD process — and a dead process next to a
        # port that answers is precisely the state that let a control spend the
        # next thirteen assertions interrogating a stranger's daemon.
        if ! kill -0 "$pid" 2>/dev/null; then
            wait "$pid" 2>/dev/null
            sandbox_setup_fail "the sandbox pogod exited before it answered on 127.0.0.1:$port — it could not bind (another daemon holds the port) or it crashed at startup" "$log"
        fi
        if curl -sf --max-time 2 "http://127.0.0.1:$port$ready" >/dev/null 2>&1; then
            sandbox_daemon_assert_owner "$pid" "$port" "$log"
            SANDBOX_DAEMON_PID="$pid"
            return 0
        fi
        sleep 0.25
    done

    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    sandbox_setup_fail "the sandbox pogod never answered http://127.0.0.1:$port$ready within 20s (it is running, pid $pid, but not serving)" "$log"
}

# sandbox_daemon_assert_owner PID PORT LOGFILE — the answering daemon must be the
# one we started. Belt to the pid-liveness braces: if pogod ever stops exiting on
# a failed bind, liveness alone would go quiet again, and this is the check that
# does not depend on that behaviour.
#
# Silent when lsof cannot see the socket at all — that is lsof's permission
# boundary (another user's process), not evidence of anything, and turning "I
# could not look" into a refusal is the same fail-closed-on-unknown mistake in
# the other direction. The pid check above still stands in that case.
sandbox_daemon_assert_owner() {
    local pid="$1" port="$2" log="$3" listeners
    command -v lsof >/dev/null 2>&1 || return 0
    listeners="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null)"
    [ -n "$listeners" ] || return 0
    printf '%s\n' "$listeners" | grep -qx "$pid" && return 0
    sandbox_setup_fail "127.0.0.1:$port is served by pid(s) [$(printf '%s' "$listeners" | tr '\n' ' ')], NOT by the sandbox pogod this run started (pid $pid) — the controls would be driving a daemon belonging to someone else" "$log"
}
