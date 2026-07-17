#!/usr/bin/env bash
# Live end-to-end control for the mg-de08 redeploy mail-check post-check
# (mg-c02d; extended by mg-ea3e with the name-slack control, #3 below).
#
# WHY THIS FILE EXISTS SEPARATELY FROM pogo-self-deploy_test.sh
# -------------------------------------------------------------
# Its sibling exercises the PURE functions with hand-written strings and never
# starts a daemon. That proves `classify_mail_check_restore` can fail. It does
# NOT prove the thing that actually runs at redeploy can fail, because the
# wired chain is
#
#     verify_mail_checks_restored -> mail_check_ids -> live curl
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
# Nothing here is mocked. mail_check_ids really curls; the body it parses is
# really what Go's json.Encoder emitted; the schedules really vanish because
# pogod really reaped or really honoured a DELETE. The daemon is pinned to a
# sandbox HOME/XDG_CONFIG_HOME/POGO_HOME and a spare port, so it cannot see,
# touch, or be confused with the machine's live fleet.
#
# The assertions are load-bearing in different directions, and a detector that
# cannot do BOTH is not a detector (mg-c02d field evidence: architect's awk
# filter matched every line and so could never return zero; mayor's
# `schedule_added` discriminator would have returned a confident GREEN from an
# event type pogod never emits). Hence:
#
#   1. SEAM       — the live body parses to the true schedule NAMES (not a
#                   fixture's, and not merely to the right tally — mg-ea3e).
#   2. NEGATIVE   — an intact fleet reports OK, so the REDs below are
#                   conditional on the thing being measured, not unconditional.
#   3. NAME-SLACK — on --force, a real crew loss beside a no-mail-check
#                   polecat's death reports RED and NAMES the loss, where the
#                   old COUNT slack said OK. mg-ea3e's ask, plus the
#                   counterfactual that proves it drives that exact case.
#   4. POSITIVE   — a real reap makes the assembled path report RED. mg-c02d's ask.
#   5. FAIL-OPEN  — a dead daemon reports UNKNOWN, never OK and never "all gone".
#   6. DRAIN      — a build that fails AFTER the drain restores dispatch on the
#                   way out, so a failed deploy cannot leave the fleet
#                   undispatchable. Includes the RED, demonstrated by neutering
#                   the restore and watching the live daemon really strand at
#                   draining=true. mg-8b48's ask.
#
# 6 drives the REAL cmd_redeploy end-to-end (drain -> failing build -> trap)
# rather than a pure helper, so unlike 1-5 it is a control on the DRIVER, not on
# the post-check. It shares this file because it needs the same thing: a real
# pogod whose state can be read back after the code under test has moved it.
#
# RUNNING AGAINST A PREBUILT ARTIFACT ($POGO_LIVE_CONTROL_POGOD) — mg-bfe5
# ----------------------------------------------------------------------
# By default this file builds its own pogod from $REPO_ROOT, which is what the
# refinery gate wants: a control on the COMMIT. `do_prove` in the driver sets
# POGO_LIVE_CONTROL_POGOD to the binary `go install` just produced and runs this
# same file against THAT, which is what a deploy wants: a measurement of the
# ARTIFACT. Those are different facts, and the difference is the whole of mg-bfe5
# — `do_build` runs `go install` and no tests, so before this every redeploy
# shipped a pogod whose detector had never been exercised against it. A merge-time
# pass is a claim about a commit; it is not an observation of the bytes that are
# about to become the fleet's daemon.
#
# The seam is deliberately just the binary. Everything else — the driver sourced
# below, the assertions, the sandbox — still comes from $REPO_ROOT, because
# do_build has already refused to install from a tree that is not exactly
# $DEPLOY_REF and clean. So at deploy time the shell code and the artifact are
# from the same commit, and the artifact is the installed one rather than a
# second build of it.
#
# WHAT `PROVED:` LINES ARE FOR (and why the exit code is not enough)
# -----------------------------------------------------------------
# Controls 2 and 4 are the two directions of the post-check: OK on an intact
# fleet, RED on a real reap. Each records a `PROVED: GREEN` / `PROVED: RED`
# token, and do_prove refuses to deploy unless it observes BOTH in this file's
# output.
#
# It reads the tokens rather than the exit code because those answer different
# questions. Exit 0 means "nothing that ran failed" — it cannot distinguish a run
# that demonstrated both directions from one where control 4 was deleted, or was
# skipped by an early `exit`, or never ran because the seam check bailed out at
# line ~200. All of those exit 0 with every surviving assertion passing, and all
# of them would hand a deploy a detector that had never been shown able to fail
# against that artifact. The tokens make the gate assert on what was OBSERVED
# rather than on what was not reported — a control that only demonstrates RED can
# be hard-wired to RED, and one that only demonstrates GREEN is decoration.

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

# Guard the WRITE, not the count. This file runs `set -u` and deliberately not
# `set -e`, so a failed `>>` would not abort — and RESULTS_FILE being EMPTY (what
# mktemp yields when it fails) is not something `set -u` catches: the variable IS
# set. Without this guard, every assertion below really runs, prints a clean PASS
# line to stdout, appends to nothing, and the verdict block tallies an unreadable
# ledger as `0 passed, 0 failed` -> exit 0 -> GREEN. The refinery reads only the
# exit code, and at 03:00 unattended this script is the SOLE detector: a green
# gate that recorded nothing is indistinguishable from a green gate that recorded
# everything. So the instrument reports its own failure AT the point of failure,
# on the FIRST assertion, instead of having its health inferred from a downstream
# tally that cannot tell "zero" from "unknown".
#
# NOTE: no number appears here, on purpose. A control that hard-codes a fact
# about the system it controls inherits that fact's decay — the first draft of
# this guard asserted a count of 4 assertions and was already stale when written
# (mg-ea3e had added a fifth). It would have failed the next legitimate diff and
# then been deleted by whoever it inconvenienced, leaving no protection at all.
# This guard asserts an invariant, not a measurement: add a sixth assertion and
# it keeps working untouched. "Did every assertion run?" is a DIFFERENT guard,
# it owns a literal and therefore owns its decay, and it is deliberately not here.
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

# A direction actually DEMONSTRATED against the artifact under test (mg-bfe5).
# Emitted only from the two sites that observe the assembled post-check reporting
# each way, and guarded like the ledger writes for the same reason: a deploy gate
# that reads these must not be handed silence by a failed append.
#
# It is deliberately NOT called from pass() — "an assertion passed" and "the
# detector was shown able to go RED against this binary" are different claims,
# and collapsing them would let any 24 green assertions satisfy do_prove.
proved() { echo "PROVED: $1"; echo "PROVED: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: PROVED $1"; exit 1; }; }

# The artifact under test. Default: build one from $REPO_ROOT (the refinery gate's
# control on the commit). $POGO_LIVE_CONTROL_POGOD: use the caller's prebuilt
# binary verbatim (the driver's do_prove, pointing at what `go install` just
# produced — a control on the artifact). See the header.
#
# Copied, not run in place: the sandbox daemon must stay killable and disposable,
# and the real ~/go/bin/pogod is a file a concurrent `go install` may rewrite
# underneath a live process (the same reason running_rev reads the PROCESS, not
# `go version -m` on disk). A copy pins the bytes for the length of the run.
#
# Build FIRST, under the real HOME. Go resolves GOPATH/GOMODCACHE off $HOME, so
# building after the sandbox override below sends it to re-download the whole
# module cache and toolchain into $SANDBOX — minutes of network, and a module
# cache that is read-only by design and so defeats the cleanup rm -rf.
if [ -n "${POGO_LIVE_CONTROL_POGOD:-}" ]; then
    # Refuse to silently fall back to a source build: the caller asked for a
    # specific artifact, and quietly testing a DIFFERENT binary than the one
    # about to be deployed is precisely the fail-open this ticket exists to close.
    if [ ! -x "$POGO_LIVE_CONTROL_POGOD" ]; then
        fail "POGO_LIVE_CONTROL_POGOD=$POGO_LIVE_CONTROL_POGOD is not an executable — refusing to fall back to a source build and report on the wrong binary"
        exit 1
    fi
    echo "Using prebuilt artifact: $POGO_LIVE_CONTROL_POGOD"
    if ! cp "$POGO_LIVE_CONTROL_POGOD" "$SANDBOX/pogod"; then
        fail "could not copy $POGO_LIVE_CONTROL_POGOD into the sandbox — the live control cannot run"
        exit 1
    fi
    chmod +x "$SANDBOX/pogod"
else
    echo "Building pogod into the sandbox..."
    if ! (cd "$REPO_ROOT" && go build -o "$SANDBOX/pogod" ./cmd/pogod); then
        fail "could not build cmd/pogod — the live control cannot run"
        exit 1
    fi
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
# because their sweeps survived. The parser must not be fooled by it.
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
# The sibling unit test feeds extract_mail_check_ids a hand-typed JSON string.
# That string is an ASSUMPTION about json.Encoder's output; if the encoder ever
# emitted a space after the colon, or renamed the field, every unit assertion
# would keep passing while the live read silently went empty — and empty reads
# as a fleet-wide wipe. This is the assertion that ties the fixture to reality.
#
# It asserts the six NAMES, not the number six (mg-ea3e). The driver reasons
# about identity now, so a seam assertion that only tallied would leave the
# question the driver actually asks — WHICH schedules are here — untied to
# reality. A parse that returned six wrong names would pass a count check.
LIVE_PRE="$(mail_check_ids)"
EXPECT_PRE="$(printf '%s\n' $CREW | sed 's/^/mail-check-/' | LC_ALL=C sort -u)"
if [ "$LIVE_PRE" = "$EXPECT_PRE" ]; then
    pass "mail_check_ids reads the six crew mail-checks BY NAME from the LIVE daemon (real curl, real json.Encoder body)"
else
    fail "mail_check_ids returned '$(printf '%s' "$LIVE_PRE" | tr '\n' ' ')' from the live daemon, expected '$(printf '%s' "$EXPECT_PRE" | tr '\n' ' ')' — the curl/parse seam is broken"
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
OUT_OK="$(verify_mail_checks_restored "$LIVE_PRE" "" 2>&1)"
case "$OUT_OK" in
    *"post-check: OK"*)
        pass "assembled path reports OK against a live, intact fleet (the RED below is conditional)"
        # The GREEN half of do_prove's deploy gate (mg-bfe5): this artifact's
        # detector was observed reporting OK on a fleet that really is intact.
        proved "GREEN" ;;
    *)
        fail "assembled path did not report OK on an intact fleet: $OUT_OK" ;;
esac

# ===========================================================================
# 3. NAME-SLACK CONTROL — a real crew loss must not hide behind a dead
#    polecat's allowance on --force (mg-ea3e).  THE SECOND ASK.
# ===========================================================================
# Runs inside the same 30s settle window (the GC must not reap anything out from
# under us; the reap gets its own control below), and restores the fleet to the
# six crew loops before it exits so control 4 still measures 6 -> 0.
#
# The scenario is the ticket, staged live: a --force bounce kills TWO polecats,
# and only ONE of them had a mail-check. cat-loop registered one; cat-noloop
# never did (a polecat spawned while the scheduler was still loading takes the
# nil-registrar path and gets no loop — mg-6fe0 — so this is a state the fleet
# really reaches, not a contrivance). Alongside them, crew agent `pa` really
# loses its loop. That is the REAL crew loss, and nothing about the bounce
# explains it.
#
# Under the old COUNT slack this state read: pre 7, post 5, slack 2 (two dead
# polecats) -> 5 >= 7-2 -> OK. Silence, over a crew agent gone mute for hours.
# The allowance cat-noloop never earned paid for pa's loss. Assertion (c) below
# pins that arithmetic in place so nobody has to take the claim on faith.
curl -sf -X POST "$URL/scheduler/schedules" -H 'Content-Type: application/json' \
    -d '{"id":"mail-check-cat-loop","agent":"cat-loop","cron":"*/10 * * * *","delivery":"nudge","message":"check mail"}' \
    >/dev/null || fail "could not register the force-killed polecat's mail-check"

# The pre-bounce world, read through the driver's own primitive from the live
# daemon: six crew loops + cat-loop's. cat-noloop is absent BY BEING ABSENT —
# there is no schedule to see, which is the entire point.
SLACK_PRE_BODY="$(schedules_body)"
SLACK_PRE_IDS="$(printf '%s' "$SLACK_PRE_BODY" | extract_mail_check_ids)"

# The pre-kickstart drain snapshot: exactly what the driver captures at
# scripts/pogo-self-deploy's step 1 and hands to step 5 after the bounce.
SLACK_SNAP='{"draining":true,"count":2,"polecats":[{"name":"cat-noloop","pid":222,"work_item_id":"mg-noloop","worktree_dir":"'"$SANDBOX/wt-noloop"'","source_repo":"'"$SANDBOX"'"},{"name":"cat-loop","pid":111,"work_item_id":"mg-loop","worktree_dir":"'"$SANDBOX/wt-loop"'","source_repo":"'"$SANDBOX"'"}]}'

# (a) Slack is granted by NAME, off the live body: one schedule, not two polecats.
SLACK_IDS="$(expected_lost_mail_checks "$SLACK_PRE_BODY" "$SLACK_SNAP")"
[ "$SLACK_IDS" = "mail-check-cat-loop" ] \
    && pass "slack off the LIVE body names exactly the one schedule that died with a polecat (2 dead, 1 loop)" \
    || fail "slack from the live body is '$(printf '%s' "$SLACK_IDS" | tr '\n' ' ')', expected just mail-check-cat-loop"

# Now perform the loss, for real, through the daemon's own endpoint: cat-loop's
# schedule goes because cat-loop is dead (legitimate), and pa's goes for no
# reason anyone can name (the outage).
for gone in mail-check-cat-loop mail-check-pa; do
    curl -sf -X DELETE "$URL/scheduler/schedules/$gone" >/dev/null \
        || fail "could not remove $gone from the sandbox daemon"
done

# (b) THE ASSERTION THIS TICKET EXISTS FOR. Same call the redeployer makes, same
# args, against a daemon that really is in the state described. It must go RED,
# and it must NAME pa — a verdict that only says "something is missing" does not
# tell you who to go nudge.
OUT_SLACK="$(MAIL_CHECK_TIMEOUT=2 verify_mail_checks_restored "$SLACK_PRE_IDS" "$SLACK_IDS" 2>&1)"
case "$OUT_SLACK" in
    *"post-check: FAILED"*"LOST: mail-check-pa"*)
        pass "mg-ea3e: a real crew loss beside a no-mail-check polecat's death reports RED and NAMES mail-check-pa" ;;
    *"post-check: OK"*)
        fail "mg-ea3e REGRESSION: the crew loss was absorbed by the dead polecats' allowance — SILENT MISS: $OUT_SLACK" ;;
    *)
        fail "mg-ea3e: expected RED naming mail-check-pa, got: $OUT_SLACK" ;;
esac

# (c) ...and the counterfactual, so the control above is provably driving the
# case the bug let through rather than some easier one. This is the arithmetic
# the old code ran, on the numbers this live state really produces. It says OK.
# If this ever stops saying OK, the scenario has drifted and (b) is no longer
# the ticket — fix the scenario, not this assertion.
OLD_PRE="$(printf '%s' "$SLACK_PRE_IDS" | grep -c .)"
OLD_POST="$(mail_check_ids | grep -c .)"
OLD_SLACK=2   # $leftover: the COUNT of polecats the forced bounce killed
if [ "$OLD_POST" -ge $(( OLD_PRE - OLD_SLACK )) ]; then
    pass "counterfactual: the OLD count slack ($OLD_PRE -> $OLD_POST, slack $OLD_SLACK) says OK on this exact state — the miss was real, and is now caught"
else
    fail "counterfactual did not reproduce: old count arithmetic ($OLD_PRE -> $OLD_POST, slack $OLD_SLACK) would have caught this, so the control above is not driving the mg-ea3e case"
fi

# Restore the six crew loops for control 4. cat-loop stays gone — it is dead.
curl -sf -X POST "$URL/scheduler/schedules" -H 'Content-Type: application/json' \
    -d '{"id":"mail-check-pa","agent":"pa","cron":"*/10 * * * *","delivery":"nudge","message":"check mail"}' \
    >/dev/null || fail "could not restore mail-check-pa"
[ "$(mail_check_ids)" = "$LIVE_PRE" ] \
    || fail "fleet not restored to the six crew mail-checks — control 4 below cannot mean what it says"

# ===========================================================================
# 4. POSITIVE CONTROL — a REAL reap drives the ASSEMBLED path RED.  THE ASK.
# ===========================================================================
# No agent in this sandbox exists, so once the startup gate opens pogod's own
# heartbeat GC reaps every mail-check as agent_gone — the 2026-07-17 incident,
# performed by the real daemon rather than described by a fixture. We only wait
# for it; we never remove a schedule ourselves.
echo "Waiting for pogod's GC to reap the fleet's mail-checks (opens ~30s after boot)..."
reaped=false
while [ $(( $(date +%s) - BOOT_T0 )) -lt 120 ]; do
    if [ -z "$(mail_check_ids)" ]; then reaped=true; break; fi
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

    # THE ASSERTION mg-c02d EXISTS FOR. Same call the redeployer makes at step
    # 5, against a daemon whose fleet really is gone.
    # If this ever reports OK or UNKNOWN, the post-check fails OPEN and its
    # GREEN is worthless — do not "fix" this test, fix the driver.
    # MAIL_CHECK_TIMEOUT is a prefix assignment on the driver's own shell var —
    # the verdict is already terminal, so polling the full 30s only adds wall
    # clock. It still probes the live daemon; only the retry budget shrinks.
    OUT_RED="$(MAIL_CHECK_TIMEOUT=2 verify_mail_checks_restored "$LIVE_PRE" "" 2>&1)"
    case "$OUT_RED" in
        *"post-check: FAILED"*"6 mail-check schedule(s) LOST"*)
            pass "positive control: assembled path reports RED on a real reap (6 -> 0 = FAILED, 6 LOST)"
            # The RED half of do_prove's deploy gate (mg-bfe5): this artifact's
            # detector was observed going RED on a real reap it really suffered.
            # Paired with the GREEN above, that RED is conditional rather than
            # hard-wired — which is the only form of it worth deploying on.
            proved "RED" ;;
        *)
            fail "positive control FAILED: real reap of 6 mail-checks, assembled path did NOT report RED. Got: $OUT_RED" ;;
    esac
fi

# ===========================================================================
# 6. DRAIN RESTORE — a build that fails AFTER the drain must not leave the
#    fleet undispatchable (mg-8b48).  THE FOURTH ASK.
# ===========================================================================
# Until the trap existed, `pogo-self-deploy redeploy` enabled drain on the live
# pogod and then, on any do_build failure, exited WITHOUT turning it back off.
# The old daemon stayed up with draining=true and dispatched no polecats,
# fleet-wide, until a human noticed — and nothing goes red when that happens.
# It fires on a dirty tree, which is a condition a human creates by accident.
#
# This drives the REAL cmd_redeploy — not a fixture, not the trap called by
# hand — against the REAL sandbox daemon, and makes do_build really fail on the
# really-dirty-tree branch. The redeploy runs in a ( subshell ) so its exit 4 is
# an assertion instead of the end of this file; a trap set inside a subshell
# fires on that subshell's exit, which is exactly the mechanism under test.
#
# Assertions, in the order that makes each one mean something:
#   (a) precondition — dispatch really is ON before we start, or (d) is vacuous.
#   (b) the RED, DEMONSTRATED — with restore_drain neutered, this exact scenario
#       really does strand the daemon at draining=true. This is the bug, live.
#       Without it, (d) could pass on a script that never drained at all.
#   (c) the build really failed AFTER the drain (exit 4), not before it.
#   (d) the FIX — the real trap restores dispatch on that same exit path.
#   (e) CONDITIONAL, not hard-wired — a run that gets past do_build leaves drain
#       ON at the kickstart boundary, so the restore is scoped to failure and is
#       not just "this script always ends with draining=false".

# A pogo checkout whose HEAD is NOT $DEPLOY_REF -> do_build's first exit 4.
# Built here rather than reusing $REPO_ROOT: this must not depend on whether the
# polecat's own worktree happens to be clean or on which ref it sits.
DR_REPO="$SANDBOX/drain-repo"
mkdir -p "$DR_REPO"
(
    cd "$DR_REPO" && git init -q . && git config user.email t@t && git config user.name t
    echo one > f && git add f && git commit -qm one
    git branch -f main-fixture
    echo two > f && git commit -qam two   # HEAD now != main-fixture
) >/dev/null 2>&1

# Drive the driver's own primitives at the sandbox: POGO_GOBIN points at an
# empty dir so installed_rev is empty != MAIN -> NEEDS_BUILD=true -> do_build
# really runs. POGO_DEPLOY_REF is the fixture branch HEAD has diverged from.
mkdir -p "$SANDBOX/nobin"
dr_run() {
    # One failing redeploy, start to finish, in a subshell. Echoes its exit code.
    # The redirect sits on the SUBSHELL, not on cmd_redeploy: the trap fires on
    # subshell EXIT, i.e. after any redirection scoped to the command inside it
    # has already ended, so redirecting the command alone lets restore_drain's
    # log leak into this function's stdout and corrupt the exit code it echoes.
    (
        POGO_GOBIN="$SANDBOX/nobin" \
        POGO_DEPLOY_REF=main-fixture \
        REPO="$DR_REPO" DEPLOY_REF=main-fixture \
        ASSUME_YES=true FORCE=false SKIP_DRAIN=false
        cmd_redeploy
    ) >/dev/null 2>&1
    echo $?
}
dr_state() { curl -sf --max-time 5 "$URL/agents/drain" 2>/dev/null | json_bool draining; }

curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1
# (a) precondition
[ "$(dr_state)" = "false" ] \
    && pass "drain-restore precondition: the sandbox daemon is dispatching (draining=false) before the run" \
    || fail "drain-restore precondition: daemon is not at draining=false — the controls below cannot mean what they say"

# (b) THE RED, DEMONSTRATED. Neuter ONLY the trap's body; everything else is the
# real path. If this ever reports draining=false, the scenario has stopped
# driving the bug and (d) below is proving nothing — fix the scenario, not this.
DR_REAL_BODY="$(declare -f restore_drain)"
restore_drain() { :; }
DR_RC_RED="$(dr_run)"
if [ "$(dr_state)" = "true" ]; then
    pass "RED demonstrated: with the restore neutered, a build failure after drain really does strand the LIVE daemon at draining=true (rc=$DR_RC_RED)"
else
    fail "RED did NOT reproduce: neutering restore_drain left draining=$(dr_state) — this scenario is not driving the mg-8b48 bug, so the PASS below would be worthless"
fi
eval "$DR_REAL_BODY"   # put the real trap body back

# (c) the failure really is do_build's, i.e. downstream of the drain
[ "$DR_RC_RED" = "4" ] \
    && pass "the run really fails in do_build (exit 4) — AFTER drain was enabled, BEFORE any kickstart" \
    || fail "expected the redeploy to exit 4 from do_build, got $DR_RC_RED — the window under test was never entered"

# (d) THE ASSERTION THIS TICKET EXISTS FOR. Same scenario, real trap.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1
DR_RC="$(dr_run)"
if [ "$(dr_state)" = "false" ]; then
    pass "mg-8b48: the trap restores dispatch (draining=false) after a build that failed post-drain (rc=$DR_RC)"
else
    fail "mg-8b48 REGRESSION: build failed after drain and the LIVE daemon is still draining=$(dr_state) — the fleet would dispatch NOTHING until a human noticed"
fi

# (e) RESTORE THE PRIOR VALUE, NOT A HARD false. Drain was already ON before this
# run for some unrelated reason; the deploy must put back what it found, not
# assert its own idea of the world and switch dispatch on under whoever drained.
#
# Note what this one does and does not discriminate: it separates "restores the
# prior value" from "asserts false" (the old timeout path's bare `drain_post
# false`, which would leave this case at false). It canNOT tell a correct restore
# from no trap at all — both end at true. That is what (b) and (d) are for, and
# this assertion leans on them rather than pretending to stand alone.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":true}' >/dev/null 2>&1
dr_run >/dev/null
[ "$(dr_state)" = "true" ] \
    && pass "mg-8b48: a pre-existing drain is RESTORED, not cleared — the trap restores state, it does not assert it" \
    || fail "mg-8b48: the trap forced draining=false over a drain that was already on before the deploy — asserting a state instead of restoring one"

# (f) CONDITIONAL, not hard-wired. The restore must be scoped to the failure
# window: disarming at the kickstart boundary is what makes drain a real deploy
# phase rather than a no-op this script always undoes. Reach that boundary with
# the trap armed and confirm drain is still ON, so (d) is a consequence of the
# FAILURE and not of the script simply always ending at draining=false.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1
(
    DRAIN_PRIOR=false
    DRAIN_ARMED=true
    trap restore_drain EXIT
    drain_post true >/dev/null          # the deploy's real mutation
    DRAIN_ARMED=false; trap - EXIT      # the real disarm, at the real boundary
    exit 0
)
[ "$(dr_state)" = "true" ] \
    && pass "restore is CONDITIONAL: past the kickstart boundary the trap is disarmed and drain STAYS on (the deploy really drains)" \
    || fail "drain was cleared past the disarm boundary — the restore is unconditional, so it proves nothing about the failure path"

# (g) A SIGNAL RESTORES AND STOPS — it does not restore and CARRY ON. A bash
# signal handler that returns resumes the script at the point of interruption,
# so the obvious `trap restore_drain EXIT INT TERM` would turn dispatch back on
# and then keep building and kickstarting with the fleet live: a cleanup that
# fires and then un-fires itself. Ctrl-C during a 30-minute drain wait is the
# most likely way a human ever enters this path, so it gets an assertion rather
# than an argument. Driven with a real signal against a real daemon.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1
#
# This drives the REAL cmd_redeploy with the REAL trap wiring the driver
# installs — it does not restate the trap setup here. A control that hand-rolled
# its own `trap` would pin the IDIOM and not the driver, and would keep passing
# while cmd_redeploy regressed to the returning-handler form. Overriding
# drain_wait is the seam: it puts the signal at the exact moment the human
# actually reaches for Ctrl-C — while the deploy sits waiting for polecats to
# finish, which is a 30-MINUTE window by default and by far the likeliest way
# anyone ever interrupts this script.
#
# Runs as its OWN script rather than a ( subshell ) because this box's bash is
# 3.2, which has no $BASHPID: inside a subshell `$$` is still the PARENT's pid,
# so `kill -INT $$` would signal this test file instead of the code under test,
# and the assertion would be reporting on nothing.
#
# rc is the discriminator, and it is what makes this control able to fail:
#   130 = the signal stopped the deploy (correct).
#     4 = the handler RETURNED, drain_wait completed, and the deploy carried on
#         into do_build — the returning-handler bug, which restores dispatch and
#         then rebuilds and kickstarts the fleet anyway.
cat > "$SANDBOX/sigtest.sh" <<SIGEOF
#!/bin/bash
set -u
source "$REPO_ROOT/scripts/pogo-self-deploy"
# The human hits Ctrl-C while the deploy waits for the fleet to quiesce.
drain_wait() { kill -INT \$\$; sleep 2; echo 0; return 0; }
POGO_GOBIN="$SANDBOX/nobin"
REPO="$DR_REPO" DEPLOY_REF=main-fixture
ASSUME_YES=true FORCE=false SKIP_DRAIN=false
cmd_redeploy
SIGEOF
POGO_PORT="$PORT" bash "$SANDBOX/sigtest.sh" >/dev/null 2>&1
DR_SIG_RC=$?
case "$DR_SIG_RC" in
    130) pass "SIGINT in the drain window STOPS the real cmd_redeploy at the signal (exit 130) — it does not restore dispatch and then carry on building" ;;
    4)   fail "SIGINT's handler RETURNED and the deploy resumed into do_build (exit 4) — a returning INT handler restores dispatch and then rebuilds and kickstarts the fleet anyway" ;;
    *)   fail "expected exit 130 from a SIGINT in the drain window, got $DR_SIG_RC" ;;
esac
[ "$(dr_state)" = "false" ] \
    && pass "SIGINT in the drain window restores dispatch on the way out (Ctrl-C cannot strand the fleet either)" \
    || fail "SIGINT left the live daemon at draining=$(dr_state) — an aborted deploy strands the fleet exactly like a failed build"

# Leave the daemon dispatching for anything downstream.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1

# ===========================================================================
# 7. THE EXTERNAL SINK — exit 7 must be observable from OUTSIDE the script
#    (mg-f206).  THE FIFTH ASK.
# ===========================================================================
# exit 7 is the drain-timeout refusal: polecats are still working, --force is not
# set, so the deploy restores dispatch and does NOTHING. Attended that reports
# itself — the human who ran it reads the two err lines. Unattended at 03:00 it
# is a deploy that silently never happens, and the fleet stops tracking main with
# every light green. The sink is what makes that outcome leave the script.
#
# So the assertion is NOT "the script printed something". It is that an
# INDEPENDENT READER — a separate `mg` process, reading a mailbox this script
# never wrote to directly — can see the alert. That is the whole content of
# architect's ruling ("loud must be observable from OUTSIDE the thing that
# failed"), and it is the only form of it that a log line cannot satisfy.
#
# WHAT IS REAL HERE, AND THE ONE THING THAT IS NOT. Real: cmd_redeploy, the
# drain POST against the live daemon, the FORCE branch, alert_drain_stalled,
# alert_external, mail_alert, the `mg` binary, the mail store, the readback, the
# restore trap, the exit code. Stubbed: `drain_wait`'s VERDICT, and nothing else.
# Driving a true drain timeout needs a live polecat that outlasts the deadline —
# a real PTY, a real provider, a real worktree — which this sandbox deliberately
# cannot make (POGO_AGENT_AUTOSTART=false, no config, no repo). So the scenario's
# PRECONDITION is asserted and everything downstream of it is exercised. That is
# the same move test 6 makes when it neuters restore_drain to demonstrate its
# RED, and the same bound applies: this proves the sink FIRES AND LANDS on the
# exit-7 path; it does not re-prove that drain_wait decides to time out
# correctly. drain_wait's verdict is mg-46a4's measurement, not this file's.
#
# MAIL ISOLATION IS LOAD-BEARING, NOT HYGIENE. do_prove runs this file on EVERY
# redeploy. `mg` resolves its store as --root > $MG_ROOT > $HOME/.macguffin, so
# HOME alone would be enough today only because MG_ROOT happens to be unset on
# this box — which is exactly the reasoning that put POGO_HOME on the live fleet
# from a stale profile export (see the sandbox block above). Pinned explicitly:
# unpinned, every deploy would mail the REAL coordinator a fake stall alert, and
# the sink would be indistinguishable from a spammer.
export MG_ROOT="$SANDBOX/home/.macguffin"

if ! command -v mg >/dev/null 2>&1; then
    fail "no 'mg' on PATH — the exit-7 sink cannot be proven, and an unproven sink is the thing mg-f206 exists to remove"
else
# The coordinator this control addresses. Overriding the driver's global rather
# than the config: the sandbox has no config.toml on purpose (tests 1-6 depend on
# that), and the name is what mail_alert is going to check.
COORDINATOR="sink-coordinator"
SINK_SUBJ="[deploy-stalled]"

# The coordinator's mailbox must EXIST before the run, because "the box existed"
# is precisely what mail_alert uses to tell a real delivery from a phantom. This
# seeding send is also the control on the sandbox itself: if mg cannot write here,
# every assertion below is measuring the wrong thing and we want to know now.
if mg mail send "$COORDINATOR" --from=live-control --subject="seed" --body="seed" >/dev/null 2>&1; then
    pass "sink sandbox: MG_ROOT=$MG_ROOT is writable and the coordinator mailbox exists (the real fleet's mail store is untouched)"
else
    fail "sink sandbox: could not seed a mailbox under MG_ROOT=$MG_ROOT — the sink controls below cannot mean anything"
fi

# These controls emit SINK-FIRED / SINK-QUIET, deliberately NOT the bare
# RED / GREEN that do_prove gates on. Same reasoning the `proved` helper gives
# for not calling itself from pass(): "the mail-check detector was shown able to
# go RED against this binary" and "the stall sink was shown able to fire" are
# different claims, and one token for both would let either stand in for the
# other — a run with the mail-check controls deleted would still satisfy
# do_prove off these. do_prove's contract is the ARTIFACT's detector (mg-bfe5),
# and the sink is not in the artifact: it is this script plus `mg`. These still
# gate every deploy, via the FAIL tally, which is the honest lever for them.
#
# An INDEPENDENT observation of the recipient's mailbox: a separate mg process,
# reading the store, exactly as a human would. Deliberately NOT mail_alert's own
# readback — a sink that grades its own homework proves nothing.
sink_mail_count() {
    mg mail list "$COORDINATOR" --all --json 2>/dev/null | grep -Fc "$SINK_SUBJ" || true
}

# (a) CONDITIONAL / NEGATIVE — the direction that stops this being a sink that
#     always fires. A run that fails for a DIFFERENT reason (do_build's exit 4,
#     driven by test 6's fixture) must send NOTHING. A sink wired to the wrong
#     scope — to the trap, to every exit — would pass the RED below and still be
#     useless, because an alert that fires on every failure tells you nothing
#     about the one failure that is otherwise invisible.
SINK_BEFORE_HEALTHY="$(sink_mail_count)"
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1
SINK_RC_QUIET="$(dr_run)"
SINK_AFTER_HEALTHY="$(sink_mail_count)"
[ "$SINK_RC_QUIET" = "4" ] \
    && pass "sink control (a) precondition: the non-stall run really failed in do_build (exit 4), i.e. it is a DIFFERENT failure than exit 7" \
    || fail "sink control (a): expected exit 4 from the do_build fixture, got $SINK_RC_QUIET — the negative direction is not exercising a real non-stall failure"
if [ "$SINK_AFTER_HEALTHY" = "$SINK_BEFORE_HEALTHY" ]; then
    pass "CONDITIONAL: a redeploy that failed for a NON-stall reason sent no stall alert (count stayed $SINK_BEFORE_HEALTHY) — the sink is scoped to exit 7, not wired to every exit"
    proved "SINK-QUIET"
else
    fail "the sink fired on a run that did NOT stall ($SINK_BEFORE_HEALTHY -> $SINK_AFTER_HEALTHY) — an alert that fires on everything is as useless as one that never fires"
fi

# (b) THE RED, AND THE ASK. Force the exit-7 precondition and watch the alert
#     arrive somewhere the script cannot reach.
sink_stall_run() {
    (
        POGO_GOBIN="$SANDBOX/nobin" \
        POGO_DEPLOY_REF=main-fixture \
        REPO="$DR_REPO" DEPLOY_REF=main-fixture \
        ASSUME_YES=true FORCE=false SKIP_DRAIN=false
        # The ONLY stub: assert the timeout verdict this sandbox cannot produce
        # honestly. Everything from the `if ! $FORCE` below it is the real path.
        drain_wait() { echo 2; return 1; }
        cmd_redeploy
    ) >/dev/null 2>&1
    echo $?
}
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1
SINK_BEFORE="$(sink_mail_count)"
SINK_RC="$(sink_stall_run)"
SINK_AFTER="$(sink_mail_count)"

[ "$SINK_RC" = "7" ] \
    && pass "the stall run really takes the exit-7 path (drain timeout, no --force, nothing deployed)" \
    || fail "expected exit 7 from the drain-timeout refusal, got $SINK_RC — the path the sink hangs off was never entered"

if [ "$SINK_AFTER" -gt "$SINK_BEFORE" ]; then
    pass "RED, OBSERVED FROM OUTSIDE: exit 7 put a '$SINK_SUBJ' alert in '$COORDINATOR''s mailbox, read back by a SEPARATE mg process ($SINK_BEFORE -> $SINK_AFTER)"
    proved "SINK-FIRED"
else
    fail "SILENT STALL: exit 7 delivered no mail to '$COORDINATOR' ($SINK_BEFORE -> $SINK_AFTER) — the nightly would fail closed at 03:00 and nobody would ever learn the deploy did not happen"
fi

# The alert has to be worth reading, not merely present. A stall alert whose body
# lost its remedies is a page that tells you something is wrong and not what to
# do — and this body is assembled through a temp file and --body-file precisely
# because --body would let the shell eat it silently (mg-8380).
SINK_ID="$(mg mail list "$COORDINATOR" --all --json 2>/dev/null | grep -F "$SINK_SUBJ" | tail -1 \
           | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
# --force: reading a mailbox we do not own is refused without it, and this
# control is by construction a third party to the coordinator's inbox — which is
# the entire point of the assertion.
SINK_DELIVERED="$(mg mail read "$COORDINATOR" "$SINK_ID" --force 2>/dev/null)"
# Two phrases, chosen because they are the two things the reader needs and the
# two most likely to be silently lost: the DIAGNOSIS (why nothing deployed) and
# the --force GUARD (the "fix" that would re-gate mg-0b77 and mg-13a3, which is
# the single most likely wrong move by whoever this alert wakes up).
if [ -n "$SINK_ID" ] \
   && printf '%s' "$SINK_DELIVERED" | grep -q "the drain waited" \
   && printf '%s' "$SINK_DELIVERED" | grep -q "adding --force"; then
    pass "the delivered alert carries the WHY (the drain waited) AND the --force guard in its body, not just a subject line — the multi-line body survived --body-file intact"
else
    fail "the alert arrived but its body did not survive intact — a page with no diagnosis is a louder log line with a mailbox (id=${SINK_ID:-<none>})"
fi

# (c) THE SINK REFUSES TO CLAIM A DELIVERY IT CANNOT SEE. The recipient name is a
#     CLAIM about config, and a wrong one does not error: mg creates the mailbox
#     and reports success, so the alert lands in a phantom nobody reads and the
#     send looks perfect. That is this ticket's own defect wearing the sink's
#     clothes, and it is not hypothetical — the live box carries such a phantom
#     today. mail_alert must call this UNDELIVERED, not OK.
#
# A REAL, NON-EMPTY body file: mg rejects an empty one outright ("a body is
# required"), which would make this assertion pass on the error it was not
# written to catch — a false green in the control for the false green in the sink.
SINK_PROBE_BODY="$SANDBOX/sink-probe-body.txt"
echo "phantom probe body" > "$SINK_PROBE_BODY"
SINK_PHANTOM_OUT="$(mail_alert "sink-no-such-box-$$" "$SINK_SUBJ phantom probe" "$SINK_PROBE_BODY" 2>&1)"
SINK_PHANTOM_RC=$?
# Non-zero is necessary but NOT sufficient: mail_alert returns 1 from several
# paths (mg absent, send error, readback miss), and a probe that "passes" because
# mg was missing would be reporting on nothing. Assert the REASON too, so this
# stays a control on the phantom-mailbox check specifically.
if [ "$SINK_PHANTOM_RC" -ne 0 ] && printf '%s' "$SINK_PHANTOM_OUT" | grep -q "had NO mailbox"; then
    pass "mail_alert reports UNDELIVERED, and names the reason, when the recipient had no mailbox — a send that INVENTS the box is not a delivery"
elif [ "$SINK_PHANTOM_RC" -ne 0 ]; then
    fail "mail_alert refused the phantom send but for the WRONG reason (not the mailbox_created check) — this control is not measuring what it claims: $SINK_PHANTOM_OUT"
else
    fail "mail_alert reported SUCCESS into a mailbox it had just created — a renamed coordinator would silently swallow every stall alert forever, exactly like the phantom box already sitting on this machine"
fi
fi   # command -v mg

# ===========================================================================
# 5. FAIL-OPEN SEAM — a dead daemon must be UNKNOWN, never OK, never "all gone".
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

DEAD_IDS="$(mail_check_ids)"; DEAD_RC=$?
[ "$DEAD_IDS" = "?" ] && [ "$DEAD_RC" -ne 0 ] \
    && pass "mail_check_ids yields the '?' sentinel (not an empty set) against a genuinely dead daemon" \
    || fail "mail_check_ids returned '$DEAD_IDS' rc=$DEAD_RC from a dead daemon — unreachable must not read as 'holds nothing'"

OUT_UNK="$(MAIL_CHECK_TIMEOUT=2 verify_mail_checks_restored "$LIVE_PRE" "" 2>&1)"
case "$OUT_UNK" in
    *"post-check: UNKNOWN"*)
        pass "assembled path reports UNKNOWN against a dead daemon (does not fail open, does not cry wolf)" ;;
    *"post-check: OK"*)
        fail "FAIL-OPEN: dead daemon reported OK — the post-check would report GREEN over a total outage: $OUT_UNK" ;;
    *)
        fail "dead daemon did not report UNKNOWN: $OUT_UNK" ;;
esac

echo ""
# Backstop to the write guard above: the ledger must be readable and non-empty
# before any verdict is drawn from it. The `|| true` on the two greps below is
# load-bearing and must stay — `grep -c` exits 1 on no-match, so a legitimate
# zero NEEDS it — but that same `|| true` cannot tell a real zero from "I could
# not read the file", and silently reports both as 0. This line makes that
# distinction before the tally is trusted, so the `|| true` only ever has to mean
# what it says. No literal here either: "the ledger has content" holds no matter
# how many assertions this file grows.
[ -s "$RESULTS_FILE" ] || { echo "ledger unreadable/empty — verdict cannot be trusted"; exit 1; }
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
