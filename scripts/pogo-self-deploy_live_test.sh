#!/usr/bin/env bash
# Live end-to-end control for the mg-de08 redeploy mail-check post-check (mg-c02d).
#
# WHY THIS FILE EXISTS SEPARATELY FROM pogo-self-deploy_test.sh
# -------------------------------------------------------------
# Its sibling exercises the PURE functions with hand-written strings and never
# starts a daemon. That proves `classify_mail_check_restore` can fail. It does
# NOT prove the thing that actually runs at redeploy can fail, because the
# wired chain is
#
#     verify_mail_checks_restored -> mail_check_count -> live curl
#         -> JSON parse -> classify_mail_check_restore -> log/err
#
# and every arrow in it was ARGUED, never demonstrated (mg-c02d; architect:
# "Proving a pure function can fail proves the function; the failure mode a
# positive control exists to catch lives in the WIRING — a curl that errors, a
# count that comes back empty, a classifier handed "" instead of "0"").
#
# If empty parses as OK anywhere in that seam, the post-check fails OPEN and
# reports GREEN over a real outage — mg-de08's exact pathology reappearing
# inside mg-de08's own detector. So this test stands up a REAL pogod in a
# sandbox, lets the REAL GC reap REAL mail-check schedules as agent_gone (the
# literal 2026-07-17 incident), and asserts the ASSEMBLED path reports RED.
#
# Nothing here is mocked. mail_check_count really curls; the body it parses is
# really what Go's json.Encoder emitted; the count really drops because pogod
# really reaped. The daemon is pinned to a sandbox HOME/XDG_CONFIG_HOME/
# POGO_HOME and a spare port, so it cannot see, touch, or be confused with the
# machine's live fleet.
#
# The four assertions are load-bearing in different directions, and a detector
# that cannot do BOTH is not a detector (mg-c02d field evidence: architect's
# awk filter matched every line and so could never return zero; mayor's
# `schedule_added` discriminator would have returned a confident GREEN from an
# event type pogod never emits). Hence:
#
#   1. SEAM      — the live body parses to the true count (not a fixture's).
#   2. NEGATIVE  — an intact fleet reports OK, so the RED below is conditional
#                  on the thing being measured rather than unconditional.
#   3. POSITIVE  — a real reap makes the assembled path report RED. THE ASK.
#   4. FAIL-OPEN — a dead daemon reports UNKNOWN, never OK and never "all gone".

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

RESULTS_FILE=$(mktemp)
SANDBOX=$(mktemp -d)
POGOD_PID=""

cleanup() {
    # Kill by PID only. An unanchored `pkill -f pogod` on this box would take
    # out the machine's live daemon and every agent poller with it.
    [ -n "$POGOD_PID" ] && kill "$POGOD_PID" 2>/dev/null
    [ -n "$POGOD_PID" ] && wait "$POGOD_PID" 2>/dev/null
    rm -rf "$SANDBOX"
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE"; }

# Build FIRST, under the real HOME. Go resolves GOPATH/GOMODCACHE off $HOME, so
# building after the sandbox override below sends it to re-download the whole
# module cache and toolchain into $SANDBOX — minutes of network, and a module
# cache that is read-only by design and so defeats the cleanup rm -rf.
echo "Building pogod into the sandbox..."
if ! (cd "$REPO_ROOT" && go build -o "$SANDBOX/pogod" ./cmd/pogod); then
    fail "could not build cmd/pogod — the live control cannot run"
    exit 1
fi

# --- sandbox: a real pogod that cannot reach the real fleet ------------------
# POGO_HOME must be pinned explicitly: this box exports POGO_HOME=$HOME from a
# stale profile, so setting HOME alone leaks onto the live ~/.pogo. Likewise
# XDG_CONFIG_HOME — config.toml is layered and would otherwise be read from the
# real user config. With no config file pogod starts no crew (belt and braces:
# POGO_AGENT_AUTOSTART=false), which is what makes every agent below "gone" and
# so gives us a real reap to observe.
export HOME="$SANDBOX/home"
export XDG_CONFIG_HOME="$SANDBOX/xdg"
export POGO_HOME="$SANDBOX/home/.pogo"
export POGO_AGENT_AUTOSTART=false
mkdir -p "$HOME" "$XDG_CONFIG_HOME"

# A spare port, probed free. Never the default 10000: that is the live daemon.
PORT=""
for candidate in $(seq 17731 17799); do
    if ! curl -sf --max-time 1 "http://127.0.0.1:$candidate/scheduler/schedules" >/dev/null 2>&1; then
        PORT="$candidate"; break
    fi
done
if [ -z "$PORT" ]; then
    fail "no free port in 17731-17799 for the sandbox daemon"
    exit 1
fi

"$SANDBOX/pogod" -port "$PORT" > "$SANDBOX/pogod.log" 2>&1 &
POGOD_PID=$!
BOOT_T0=$(date +%s)

URL="http://127.0.0.1:$PORT"
up=false
for _ in $(seq 1 80); do
    if curl -sf --max-time 2 "$URL/scheduler/schedules" >/dev/null 2>&1; then up=true; break; fi
    sleep 0.25
done
if ! $up; then
    fail "sandbox pogod never answered on $URL"
    sed 's/^/  pogod: /' "$SANDBOX/pogod.log"
    exit 1
fi

# Point the driver's own primitives at the sandbox daemon, then source it.
# main() will NOT run because BASH_SOURCE != $0. From here on, every call below
# is the driver's real code path against a real daemon.
export POGO_PORT="$PORT"
# shellcheck source=/dev/null
source "$REPO_ROOT/scripts/pogo-self-deploy"

[ "$(base_url)" = "$URL" ] \
    && pass "driver resolves base_url to the sandbox daemon (not the live fleet)" \
    || fail "base_url is $(base_url), expected $URL — the test would be probing the WRONG daemon"

# --- register the fleet's mail-checks + a decoy ------------------------------
# Six crew mail-checks: the real roster the operational hand-check expects
# (pa, pm-pogo, pm-onethird, pm-dealdesk, architect, mayor). Plus a sweep, which
# is exactly what made the live outage invisible — agents kept LOOKING scheduled
# because their sweeps survived. The counter must not be fooled by it.
CREW="pa pm-pogo pm-onethird pm-dealdesk architect mayor"
for a in $CREW; do
    curl -sf -X POST "$URL/scheduler/schedules" -H 'Content-Type: application/json' \
        -d "{\"id\":\"mail-check-$a\",\"agent\":\"$a\",\"cron\":\"*/10 * * * *\",\"delivery\":\"nudge\",\"message\":\"check mail\"}" \
        >/dev/null || fail "could not register mail-check-$a on the sandbox daemon"
done
curl -sf -X POST "$URL/scheduler/schedules" -H 'Content-Type: application/json' \
    -d '{"id":"sweep-morning","agent":"pm-pogo","cron":"0 9 * * *","delivery":"nudge","message":"sweep"}' \
    >/dev/null || fail "could not register the decoy sweep"

# ===========================================================================
# 1. SEAM — the live curl + parse half, against a body Go really emitted.
# ===========================================================================
# The sibling unit test feeds count_mail_checks a hand-typed JSON string. That
# string is an ASSUMPTION about json.Encoder's output; if the encoder ever
# emitted a space after the colon, or renamed the field, every unit assertion
# would keep passing while the live count silently went to 0 — and 0 reads as a
# fleet-wide wipe. This is the assertion that ties the fixture to reality.
LIVE_PRE="$(mail_check_count)"
if [ "$LIVE_PRE" = "6" ]; then
    pass "mail_check_count reads 6 from the LIVE daemon (real curl, real json.Encoder body)"
else
    fail "mail_check_count returned '$LIVE_PRE' from the live daemon, expected 6 — the curl/parse seam is broken"
    # Everything below reads the count through this same seam, so with it broken
    # they cannot mean what they say: a count stuck at 0 makes "the fleet was
    # reaped" trivially true and the OK verdict trivially right. Refuse to emit
    # those PASSes rather than bank credibility a broken seam did not earn.
    fail "seam is broken — the controls below cannot be driven and were NOT run"
    echo ""
    echo "=== Results: aborted — live curl/parse seam broken (see above) ==="
    exit 1
fi

# ===========================================================================
# 2. NEGATIVE CONTROL — an intact fleet reports OK.
# ===========================================================================
# Runs inside pogod's 30s startup GC settle window, where the reap is GUARANTEED
# held (mg-de08 Part B), so the fleet is provably intact here. Without this, a
# post-check hard-wired to RED would pass assertion 3 — the same shape as the
# awk filter that could not return zero.
OUT_OK="$(verify_mail_checks_restored "$LIVE_PRE" 0 2>&1)"
case "$OUT_OK" in
    *"post-check: OK"*)
        pass "assembled path reports OK against a live, intact fleet (the RED below is conditional)" ;;
    *)
        fail "assembled path did not report OK on an intact fleet: $OUT_OK" ;;
esac

# ===========================================================================
# 3. POSITIVE CONTROL — a REAL reap drives the ASSEMBLED path RED.  THE ASK.
# ===========================================================================
# No agent in this sandbox exists, so once the startup gate opens pogod's own
# heartbeat GC reaps every mail-check as agent_gone — the 2026-07-17 incident,
# performed by the real daemon rather than described by a fixture. We only wait
# for it; we never remove a schedule ourselves.
echo "Waiting for pogod's GC to reap the fleet's mail-checks (opens ~30s after boot)..."
reaped=false
while [ $(( $(date +%s) - BOOT_T0 )) -lt 120 ]; do
    if [ "$(mail_check_count)" = "0" ]; then reaped=true; break; fi
    sleep 1
done
if ! $reaped; then
    fail "sandbox pogod never reaped the mail-checks within 120s — cannot drive the positive control"
    sed 's/^/  pogod: /' "$SANDBOX/pogod.log"
else
    pass "sandbox pogod really reaped 6 mail-check schedules as agent_gone (the mg-de08 incident)"

    # The decoy must have survived, or "count went to 0" proves nothing about
    # mail-checks specifically — it would just mean the scheduler emptied.
    printf '%s' "$(curl -sf "$URL/scheduler/schedules")" | grep -q '"id":"sweep-morning"' \
        && pass "the reap took the mail-checks and left the sweep (the count fell for the RIGHT reason)" \
        || fail "sweep-morning also vanished — the count reaching 0 is not evidence about mail-checks"

    # THE ASSERTION THIS TICKET EXISTS FOR. Same call the redeployer makes at
    # scripts/pogo-self-deploy:484, against a daemon whose fleet really is gone.
    # If this ever reports OK or UNKNOWN, the post-check fails OPEN and its
    # GREEN is worthless — do not "fix" this test, fix the driver.
    # MAIL_CHECK_TIMEOUT is a prefix assignment on the driver's own shell var —
    # the verdict is already terminal, so polling the full 30s only adds wall
    # clock. It still probes the live daemon; only the retry budget shrinks.
    OUT_RED="$(MAIL_CHECK_TIMEOUT=2 verify_mail_checks_restored "$LIVE_PRE" 0 2>&1)"
    case "$OUT_RED" in
        *"post-check: FAILED"*"6 mail-check schedule(s) LOST"*)
            pass "positive control: assembled path reports RED on a real reap (6 -> 0 = FAILED, 6 LOST)" ;;
        *)
            fail "positive control FAILED: real reap of 6 mail-checks, assembled path did NOT report RED. Got: $OUT_RED" ;;
    esac
fi

# ===========================================================================
# 4. FAIL-OPEN SEAM — a dead daemon must be UNKNOWN, never OK, never "all gone".
# ===========================================================================
# The specific thing architect named: a classifier handed "" instead of "0".
# mail_check_count must yield EMPTY (not 0) when curl fails, because
# "unreachable" and "zero schedules" are different facts. If empty ever reads as
# 0, an unreachable daemon reports the whole fleet lost; if it ever reads as OK,
# the check is decoration. Proven here against a genuinely dead process, not a
# stubbed curl.
kill "$POGOD_PID" 2>/dev/null
wait "$POGOD_PID" 2>/dev/null
POGOD_PID=""
for _ in $(seq 1 40); do
    curl -sf --max-time 1 "$URL/scheduler/schedules" >/dev/null 2>&1 || break
    sleep 0.25
done

DEAD_COUNT="$(mail_check_count)"
[ -z "$DEAD_COUNT" ] \
    && pass "mail_check_count yields EMPTY (not 0) against a genuinely dead daemon" \
    || fail "mail_check_count returned '$DEAD_COUNT' from a dead daemon — empty must not read as a count"

OUT_UNK="$(MAIL_CHECK_TIMEOUT=2 verify_mail_checks_restored 6 0 2>&1)"
case "$OUT_UNK" in
    *"post-check: UNKNOWN"*)
        pass "assembled path reports UNKNOWN against a dead daemon (does not fail open, does not cry wolf)" ;;
    *"post-check: OK"*)
        fail "FAIL-OPEN: dead daemon reported OK — the post-check would report GREEN over a total outage: $OUT_UNK" ;;
    *)
        fail "dead daemon did not report UNKNOWN: $OUT_UNK" ;;
esac

echo ""
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
