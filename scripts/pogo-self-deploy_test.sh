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

# --- json_bool: the drain flag the restore trap reads back (mg-8b48) --------
# json_str CANNOT read `"draining":true` (asserted above), so the restore needs
# its own extractor. It must read BOTH values, and must yield EMPTY rather than
# guess when the field is absent or is not a bool — restore_drain turns an empty
# read into the "?" sentinel, and "?" is what stops it from ASSERTING a state it
# never observed.
[ "$(printf '%s' "$DRAIN" | json_bool draining)" = "true" ] \
    && pass "json_bool reads draining=true off a real drain payload" || fail "json_bool true ($(printf '%s' "$DRAIN" | json_bool draining))"
DRAIN_OFF='{"draining":false,"count":0,"polecats":[]}'
[ "$(printf '%s' "$DRAIN_OFF" | json_bool draining)" = "false" ] \
    && pass "json_bool reads draining=false (and does not collapse it to empty/true)" || fail "json_bool false"
# false must NOT read as absent: "off" and "unreadable" drive different branches
# in restore_drain — one restores, the other admits it is assuming.
[ -n "$(printf '%s' "$DRAIN_OFF" | json_bool draining)" ] \
    && pass "json_bool: draining=false is a VALUE, not an absence" || fail "json_bool false read as absent"
[ -z "$(printf '%s' '{"count":0,"polecats":[]}' | json_bool draining)" ] \
    && pass "json_bool yields empty when the field is absent (-> '?', never a guess)" || fail "json_bool absent field"
[ -z "$(printf '%s' '{"draining":"yes"}' | json_bool draining)" ] \
    && pass "json_bool yields empty for a non-bool value (-> '?', never a guess)" || fail "json_bool non-bool value"

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

# Sets, one id per line — the shape the driver really passes (mg-ea3e).
FIVE=$'mail-check-architect\nmail-check-mayor\nmail-check-pa\nmail-check-pm-dealdesk\nmail-check-pm-pogo'
FOUR=$'mail-check-architect\nmail-check-mayor\nmail-check-pa\nmail-check-pm-dealdesk'

# The 2026-07-17 outage: 5 mail-checks in, 0 out, no polecats killed.
[ "$(classify_mail_check_restore "$FIVE" "" "")" = "missing:mail-check-architect mail-check-mayor mail-check-pa mail-check-pm-dealdesk mail-check-pm-pogo" ] \
    && pass "post-check FAILS on the live mg-de08 incident (5 reaped -> all 5 NAMED)" \
    || fail "post-check did not fire on the mg-de08 incident ($(classify_mail_check_restore "$FIVE" "" ""))"
# A partial loss must fire too — the outage was only caught because ONE agent's
# heartbeat went stale; four surviving loops must not mask the fifth. And the
# verdict must say WHICH agent lost its loop: that is the name you go nudge.
[ "$(classify_mail_check_restore "$FIVE" "$FOUR" "")" = "missing:mail-check-pm-pogo" ] \
    && pass "post-check FAILS on a partial loss and NAMES the one that went (5 -> 4)" \
    || fail "post-check missed a partial loss ($(classify_mail_check_restore "$FIVE" "$FOUR" ""))"

# ...and the other direction: an intact fleet is OK, or the check cries wolf on
# every deploy and gets ignored — which is how mg-de08 stayed quiet.
[ "$(classify_mail_check_restore "$FIVE" "$FIVE" "")" = "ok" ] \
    && pass "post-check passes when every mail-check survives" || fail "post-check false alarm (5 -> 5)"
[ "$(classify_mail_check_restore "$FIVE" "$FIVE"$'\nmail-check-mg-9999' "")" = "ok" ] \
    && pass "post-check passes when a mail-check is ADDED (5 -> 6)" || fail "post-check false alarm on an added schedule"
[ "$(classify_mail_check_restore "" "" "")" = "ok" ] \
    && pass "post-check passes on an empty fleet (0 -> 0)" || fail "post-check false alarm on empty fleet"
# Same count in, same count out, but they are NOT THE SAME SCHEDULES. The old
# count arithmetic called this OK — 5 >= 5 — while a crew agent sat mute and a
# fresh polecat's loop stood in for it. Identity is the only thing that sees it.
[ "$(classify_mail_check_restore "$FIVE" "$FOUR"$'\nmail-check-mg-9999' "")" = "missing:mail-check-pm-pogo" ] \
    && pass "post-check FAILS on a swap: 5 in, 5 out, but pm-pogo's loop is gone" \
    || fail "a swap slipped through — the check is still counting ($(classify_mail_check_restore "$FIVE" "$FOUR"$'\nmail-check-mg-9999' ""))"

# A --force bounce kills polecats; their mail-checks are reaped ON PURPOSE.
# Counting that as damage would cry wolf on every forced deploy. Slack is the
# SET of schedules those polecats held — granted by name, spendable only on the
# name it was granted for.
SLACK2=$'mail-check-mg-aaaa\nmail-check-mg-bbbb'
PRE7="$FIVE"$'\nmail-check-mg-aaaa\nmail-check-mg-bbbb'
[ "$(classify_mail_check_restore "$PRE7" "$FIVE" "$SLACK2")" = "ok" ] \
    && pass "post-check tolerates the loss of 2 force-killed polecats' own loops" \
    || fail "post-check flagged an expected polecat loss ($(classify_mail_check_restore "$PRE7" "$FIVE" "$SLACK2"))"
# But slack must not swallow a real loss beyond the polecats that died.
[ "$(classify_mail_check_restore "$PRE7" "$FOUR" "$SLACK2")" = "missing:mail-check-pm-pogo" ] \
    && pass "post-check still fires on a loss beyond the force-killed polecats" \
    || fail "slack swallowed a real loss ($(classify_mail_check_restore "$PRE7" "$FOUR" "$SLACK2"))"

# THE mg-ea3e CASE, at the classifier. Two polecats die on --force; only ONE of
# them had a mail-check. Slack is therefore ONE schedule, not two — and the crew
# loss it would otherwise have paid for is reported, by name.
#
# Under the old count this was: pre 6, post 4, slack 2 (two dead polecats) ->
# 4 >= 6-2 -> OK. A real crew loss, absorbed in silence by an allowance the
# no-mail-check polecat never earned. That is the bug, and this is the assertion
# that would have caught it.
PRE6="$FIVE"$'\nmail-check-mg-aaaa'   # 5 crew + ONE polecat with a loop; mg-bbbb has none
SLACK1=$'mail-check-mg-aaaa'          # only mg-aaaa had a schedule to lose
[ "$(classify_mail_check_restore "$PRE6" "$FOUR" "$SLACK1")" = "missing:mail-check-pm-pogo" ] \
    && pass "mg-ea3e: a no-mail-check polecat's death buys NO slack — the crew loss is NAMED" \
    || fail "mg-ea3e: crew loss hid behind a dead polecat's allowance ($(classify_mail_check_restore "$PRE6" "$FOUR" "$SLACK1"))"
# Slack for a schedule that never existed is not slack for some other schedule.
# Reasoning about identity means an unearned allowance cannot be SPENT anywhere,
# not merely that it is smaller.
[ "$(classify_mail_check_restore "$PRE6" "$FOUR" $'mail-check-mg-aaaa\nmail-check-mg-bbbb')" = "missing:mail-check-pm-pogo" ] \
    && pass "mg-ea3e: slack naming an absent schedule cannot be spent on a present one" \
    || fail "mg-ea3e: phantom slack was redeemed against a real loss"

# Unreachable pogod -> unknown, never "everything is gone". mail_check_ids
# echoes the "?" sentinel (not an empty set) when curl fails, because with sets
# an empty string is a legitimate answer — "reachable, holds nothing" — and it
# must not read as a fleet-wide wipe.
[ "$(classify_mail_check_restore "$FIVE" "?" "")" = "unknown" ] \
    && pass "post-check reports unknown (not FAILED) when the post-set is unreadable" \
    || fail "unreadable post-set misclassified ($(classify_mail_check_restore "$FIVE" "?" ""))"
[ "$(classify_mail_check_restore "?" "$FIVE" "")" = "unknown" ] \
    && pass "post-check reports unknown when the pre-set is unreadable" || fail "unreadable pre-set misclassified"
# ...and the distinction the sentinel exists to protect: a REACHABLE daemon
# holding nothing is a fact, not an outage, and must not be laundered into one.
[ "$(classify_mail_check_restore "" "" "")" = "ok" ] \
    && pass "an empty set is a fact (0 -> 0 = ok), a '?' is an absence of one (-> unknown)" \
    || fail "empty set did not read as a real, reachable, empty fleet"

# --- extract_mail_check_ids against a representative /scheduler/schedules body ---
SCHEDS='[{"id":"mail-check-mayor","agent":"mayor","cron":"*/10 * * * *"},{"id":"sweep-morning","agent":"crew-pm-pogo","cron":"0 9 * * *"},{"id":"mail-check-pm-pogo","agent":"pm-pogo","cron":"*/10 * * * *"},{"id":"mail-check-mg-de08","agent":"de08","cron":"*/10 * * * *"}]'
[ "$(printf '%s' "$SCHEDS" | extract_mail_check_ids)" = $'mail-check-mayor\nmail-check-mg-de08\nmail-check-pm-pogo' ] \
    && pass "extract_mail_check_ids returns only mail-check-* ids, by name" \
    || fail "extract_mail_check_ids ($(printf '%s' "$SCHEDS" | extract_mail_check_ids | tr '\n' ' '))"
# The sweeps are exactly what made the outage invisible — agents kept LOOKING
# scheduled. The parser must not be fooled by them.
[ -z "$(printf '%s' '[{"id":"sweep-morning","agent":"crew-pm-pogo"},{"id":"sweep-evening","agent":"crew-pm-pogo"}]' | extract_mail_check_ids)" ] \
    && pass "extract_mail_check_ids does not count surviving sweeps as mail-checks" || fail "extract_mail_check_ids picked up a sweep"
[ -z "$(printf '%s' '[]' | extract_mail_check_ids)" ] \
    && pass "extract_mail_check_ids handles an empty schedule list" || fail "extract_mail_check_ids on empty list"

# --- the slack lookup: names in, names out (mg-ea3e) ------------------------
# The pre-bounce world: 2 crew loops, polecat de08 with a loop, polecat f00d
# WITHOUT one (it never registered — mg-6fe0's nil-registrar drop is a live way
# to get here), plus a decoy sweep.
#
# de08's schedule is LAST in the body ON PURPOSE. The body's final object has no
# trailing newline, and a bare `while read` sets the line and then reports EOF —
# silently dropping whatever sorts last. This fixture caught exactly that during
# mg-ea3e; keep the schedule under test in the last position and it stays caught.
PRE_BODY='[{"id":"mail-check-mayor","agent":"mayor","cron":"*/10 * * * *"},{"id":"mail-check-pm-pogo","agent":"pm-pogo","cron":"*/10 * * * *"},{"id":"sweep-morning","agent":"pm-pogo","cron":"0 9 * * *"},{"id":"mail-check-mg-de08","agent":"de08","cron":"*/10 * * * *"}]'
# The bounce killed BOTH polecats. The snapshot is the only record of who they were.
SNAP='{"draining":true,"count":2,"polecats":[{"name":"de08","pid":111,"work_item_id":"mg-de08","worktree_dir":"/w/de08","source_repo":"/r"},{"name":"f00d","pid":222,"work_item_id":"mg-f00d","worktree_dir":"/w/f00d","source_repo":"/r"}]}'

[ "$(printf '%s' "$SNAP" | snapshot_polecat_names)" = $'de08\nf00d' ] \
    && pass "snapshot_polecat_names reads both dead polecats' names off the drain snapshot" \
    || fail "snapshot_polecat_names ($(printf '%s' "$SNAP" | snapshot_polecat_names | tr '\n' ' '))"

# THE ASSERTION THE TICKET IS ABOUT. Two polecats died. Slack is ONE schedule —
# de08's — because f00d had none to lose. The old code said "2".
[ "$(expected_lost_mail_checks "$PRE_BODY" "$SNAP")" = "mail-check-mg-de08" ] \
    && pass "mg-ea3e: 2 dead polecats, 1 mail-check between them -> slack names exactly that one" \
    || fail "mg-ea3e: slack is not the set of schedules that vanished ($(expected_lost_mail_checks "$PRE_BODY" "$SNAP" | tr '\n' ' '))"
# Kill only the polecat that had no loop: NOTHING is excused. A count would have
# granted 1 and eaten the next real loss whole.
SNAP_NOLOOP='{"draining":true,"count":1,"polecats":[{"name":"f00d","pid":222,"work_item_id":"mg-f00d","worktree_dir":"/w/f00d","source_repo":"/r"}]}'
[ -z "$(expected_lost_mail_checks "$PRE_BODY" "$SNAP_NOLOOP")" ] \
    && pass "mg-ea3e: killing a polecat that had NO mail-check grants ZERO slack" \
    || fail "mg-ea3e: a polecat with no mail-check was granted slack ($(expected_lost_mail_checks "$PRE_BODY" "$SNAP_NOLOOP" | tr '\n' ' '))"
# Slack is scoped to the dead. A crew agent's loop is never excused, and the
# decoy sweep is not a mail-check no matter whose it is.
SNAP_CREWNAME='{"draining":true,"count":1,"polecats":[{"name":"pm-pogo","pid":333,"work_item_id":"mg-xxxx","worktree_dir":"/w/x","source_repo":"/r"}]}'
[ "$(expected_lost_mail_checks "$PRE_BODY" "$SNAP_CREWNAME")" = "mail-check-pm-pogo" ] \
    && pass "expected_lost_mail_checks matches on the agent a schedule is ADDRESSED to, and takes the sweep with nothing" \
    || fail "expected_lost_mail_checks agent match ($(expected_lost_mail_checks "$PRE_BODY" "$SNAP_CREWNAME" | tr '\n' ' '))"
# An empty snapshot (nothing died) excuses nothing — the un-forced deploy path.
[ -z "$(expected_lost_mail_checks "$PRE_BODY" '')" ] \
    && pass "no dead polecats -> no slack at all" || fail "slack granted with an empty snapshot"

echo ""
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
