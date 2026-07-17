#!/usr/bin/env bash
# Tests for scripts/pogo-self-deploy — the out-of-band pogod redeployer.
# Exercises the pure logic (JSON extraction, three-way drift classification)
# by sourcing the script (its main() is guarded by a BASH_SOURCE check) so no
# daemon, go install, or launchctl is touched.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RESULTS_FILE=$(mktemp)
trap 'rm -f "$RESULTS_FILE"' EXIT
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE"; }

# Source the driver — main() will NOT run because BASH_SOURCE != $0.
# shellcheck source=/dev/null
source "$HERE/pogo-self-deploy"

# --- json_str / json_num against a representative /agents/drain payload ---
DRAIN='{"draining":true,"count":2,"polecats":[{"name":"cat-a","pid":11,"work_item_id":"mg-aaaa","worktree_dir":"/wt/a","source_repo":"/repo"},{"name":"cat-b","pid":12,"work_item_id":"mg-bbbb","worktree_dir":"/wt/b","source_repo":"/repo"}]}'
[ "$(printf '%s' "$DRAIN" | json_num count)" = "2" ] \
    && pass "json_num extracts count" || fail "json_num count"
[ "$(printf '%s' "$DRAIN" | json_str draining)" = "" ] \
    && pass "json_str skips non-string (draining is bool)" || fail "json_str draining"
VER='{"revision":"abc123def456","time":"2026-07-14T00:45:56Z","modified":false}'
[ "$(printf '%s' "$VER" | json_str revision)" = "abc123def456" ] \
    && pass "json_str extracts revision" || fail "json_str revision"

# --- classify_drift: the four cases from the mg-6afa ruling ---
classify() { RUNNING="$1"; INSTALLED="$2"; MAIN="$3"; classify_drift; }

classify aaa aaa aaa
{ [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = false ] && [[ "$ACTION" == clean* ]]; } \
    && pass "clean: running==installed==main" || fail "clean case ($ACTION)"

# installed==main but running stale -> restart only, NO build
classify old new new
{ [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = true ] && [[ "$ACTION" == RESTART* ]]; } \
    && pass "restart-only: installed==main, running stale" || fail "restart-only case ($ACTION)"

# running==installed, both behind main -> build+restart
classify old old new
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = true ] && [[ "$ACTION" == BUILD* ]]; } \
    && pass "build+restart: running==installed behind main" || fail "build+restart case ($ACTION)"

# all three differ -> build+restart
classify a b c
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = true ]; } \
    && pass "build+restart: all three differ" || fail "all-differ case ($ACTION)"

# main unknown -> cannot classify (non-zero)
if classify aaa aaa "" ; then fail "empty main should fail classify"; else pass "empty main fails classify"; fi

# --- classify_drain_precondition: the mg-065e bootstrap disambiguation ---
# 2xx -> proceed with drain
[ "$(classify_drain_precondition 200)" = "drain" ] \
    && pass "drain-precond: 200 -> drain" || fail "drain-precond 200 ($(classify_drain_precondition 200))"
[ "$(classify_drain_precondition 204)" = "drain" ] \
    && pass "drain-precond: 204 -> drain" || fail "drain-precond 204"
# 404 -> bootstrap (server up, endpoint predates mg-cae1) — NOT "pogod down"
[ "$(classify_drain_precondition 404)" = "bootstrap" ] \
    && pass "drain-precond: 404 -> bootstrap" || fail "drain-precond 404 ($(classify_drain_precondition 404))"
# 000 / empty -> pogod genuinely unreachable (connection refused / timeout)
[ "$(classify_drain_precondition 000)" = "down" ] \
    && pass "drain-precond: 000 -> down" || fail "drain-precond 000"
[ "$(classify_drain_precondition "")" = "down" ] \
    && pass "drain-precond: empty -> down" || fail "drain-precond empty"
# any other status -> error:<code>, refuse rather than guess
[ "$(classify_drain_precondition 500)" = "error:500" ] \
    && pass "drain-precond: 500 -> error:500" || fail "drain-precond 500 ($(classify_drain_precondition 500))"
[ "$(classify_drain_precondition 401)" = "error:401" ] \
    && pass "drain-precond: 401 -> error:401" || fail "drain-precond 401"

# --skip-drain flag defaults false and is settable (bootstrap remedy)
[ "$SKIP_DRAIN" = false ] && pass "skip-drain defaults false" || fail "skip-drain default"

# --- mail-check post-check: the mg-de08 positive control -------------------
# A check that cannot fail is not a check. The FIRST assertion below is the one
# that earns the rest: it drives the exact live incident — five crew mail-checks
# present before a bounce, reaped as agent_gone during it, zero after — and
# proves the check reports RED. Only then is a green from it worth anything.

# The 2026-07-17 outage: 5 mail-checks in, 0 out, no polecats killed.
[ "$(classify_mail_check_restore 5 0 0)" = "missing:5" ] \
    && pass "post-check FAILS on the live mg-de08 incident (5 reaped -> missing:5)" \
    || fail "post-check did not fire on the mg-de08 incident ($(classify_mail_check_restore 5 0 0))"
# A partial loss must fire too — the outage was only caught because ONE agent's
# heartbeat went stale; four surviving loops must not mask the fifth.
[ "$(classify_mail_check_restore 5 4 0)" = "missing:1" ] \
    && pass "post-check FAILS on a partial loss (5 -> 4)" \
    || fail "post-check missed a partial loss ($(classify_mail_check_restore 5 4 0))"

# ...and the other direction: an intact fleet is OK, or the check cries wolf on
# every deploy and gets ignored — which is how mg-de08 stayed quiet.
[ "$(classify_mail_check_restore 5 5 0)" = "ok" ] \
    && pass "post-check passes when every mail-check survives" || fail "post-check false alarm (5 -> 5)"
[ "$(classify_mail_check_restore 5 6 0)" = "ok" ] \
    && pass "post-check passes when a mail-check is ADDED (5 -> 6)" || fail "post-check false alarm on an added schedule"
[ "$(classify_mail_check_restore 0 0 0)" = "ok" ] \
    && pass "post-check passes on an empty fleet (0 -> 0)" || fail "post-check false alarm on empty fleet"

# A --force bounce kills polecats; their mail-checks are reaped ON PURPOSE.
# Counting that as damage would cry wolf on every forced deploy.
[ "$(classify_mail_check_restore 5 3 2)" = "ok" ] \
    && pass "post-check tolerates the loss of 2 force-killed polecats' loops" \
    || fail "post-check flagged an expected polecat loss ($(classify_mail_check_restore 5 3 2))"
# But slack must not swallow a real loss beyond the polecats that died.
[ "$(classify_mail_check_restore 5 2 2)" = "missing:1" ] \
    && pass "post-check still fires on a loss beyond the force-killed polecats" \
    || fail "slack swallowed a real loss ($(classify_mail_check_restore 5 2 2))"

# Unreachable pogod -> unknown, never "everything is gone". mail_check_count
# returns EMPTY (not 0) when curl fails, and an empty count must not read as a
# fleet-wide wipe.
[ "$(classify_mail_check_restore 5 "" 0)" = "unknown" ] \
    && pass "post-check reports unknown (not FAILED) when the count is unreadable" \
    || fail "unreadable post-count misclassified ($(classify_mail_check_restore 5 "" 0))"
[ "$(classify_mail_check_restore "" 5 0)" = "unknown" ] \
    && pass "post-check reports unknown when the pre-count is unreadable" || fail "unreadable pre-count misclassified"

# --- count_mail_checks against a representative /scheduler/schedules body ---
SCHEDS='[{"id":"mail-check-mayor","agent":"mayor","cron":"*/10 * * * *"},{"id":"sweep-morning","agent":"crew-pm-pogo","cron":"0 9 * * *"},{"id":"mail-check-pm-pogo","agent":"pm-pogo","cron":"*/10 * * * *"},{"id":"mail-check-mg-de08","agent":"de08","cron":"*/10 * * * *"}]'
[ "$(printf '%s' "$SCHEDS" | count_mail_checks)" = "3" ] \
    && pass "count_mail_checks counts only mail-check-* ids" \
    || fail "count_mail_checks ($(printf '%s' "$SCHEDS" | count_mail_checks))"
# The sweeps are exactly what made the outage invisible — agents kept LOOKING
# scheduled. The counter must not be fooled by them.
[ "$(printf '%s' '[{"id":"sweep-morning","agent":"crew-pm-pogo"},{"id":"sweep-evening","agent":"crew-pm-pogo"}]' | count_mail_checks)" = "0" ] \
    && pass "count_mail_checks does not count surviving sweeps as mail-checks" || fail "count_mail_checks counted a sweep"
[ "$(printf '%s' '[]' | count_mail_checks)" = "0" ] \
    && pass "count_mail_checks handles an empty schedule list" || fail "count_mail_checks on empty list"

echo ""
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
