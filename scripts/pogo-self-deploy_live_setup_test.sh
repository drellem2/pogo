#!/usr/bin/env bash
# The control on the CONTROLS' SANDBOX (mg-3412).
#
# WHY THIS FILE EXISTS
# --------------------
# pogo-self-deploy_live_test.sh is the deploy's detector-of-detectors, and its
# verdict is only worth what its isolation is worth. On 2026-07-29 that was
# nothing: under four concurrent polecats it picked a port another run had
# already picked, its own pogod died on the bind, and the readiness curl was
# answered by the survivor's daemon. It then ran to completion against a
# stranger's sandbox and reported 14 assertion failures — several of them
# verbatim fail-open security findings, `FAIL-OPEN: dead pogod + a live
# witnessed polecat returned '0|0'` — about a tree that was provably fine. The
# same branch, resubmitted unchanged, merged clean.
#
# mg-3412's fix was to reserve the port atomically, prove the answering daemon is
# ours, and route every staging call through sandbox_setup_fail so infrastructure
# ends the run AS infrastructure. That fix has exactly one failure mode worth
# fearing: that it does not actually fire, and the next broken sandbox produces
# fourteen confident assertion failures again.
#
# So this file breaks the sandbox ON PURPOSE and reads back what comes out.
# mg-3412 asked for precisely this, in those words: "Prove the second by breaking
# it on purpose — a suite that has only ever been seen passing has not been shown
# to report infrastructure failure correctly."
#
# WHAT IS ASSERTED, AND WHY THE NEGATIVE HALF IS THE IMPORTANT ONE
# ----------------------------------------------------------------
# It is not enough that a broken sandbox exits non-zero. The whole defect was
# that a broken sandbox exited non-zero WITH THE VOCABULARY OF A REGRESSION, and
# a coordinator read that vocabulary and reached the wrong conclusion. So every
# case below asserts BOTH directions:
#
#   * the run exits with the setup code and says SETUP FAILURE, and
#   * it emits NO `FAIL:` line, NO `PASS:` line and NO `PROVED:` token.
#
# The second is the one that would have saved the fifteen minutes. A run that
# stops before it can claim anything cannot be mistaken for a run that measured
# something and found it broken — and, just as important, cannot hand do_prove
# the PROVED tokens it gates the nightly deploy on.
#
# WHERE THE POSITIVE DIRECTION LIVES. "A WORKING sandbox does not report a setup
# failure" is not asserted here, deliberately — test.sh runs the real live
# control immediately before this file, at full cost, and its 39 PASS lines and
# four PROVED tokens are that observation. Paying 60s to restate it would buy
# nothing, and a second copy could drift from the one that gates the deploy.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

RESULTS_FILE=$(mktemp)
WORK=$(mktemp -d)
cleanup() { rm -rf "$WORK"; rm -f "$RESULTS_FILE"; }
trap cleanup EXIT

# Same guarded ledger writes as the controls this file is about, and for the same
# reason: a tally drawn from an unwritable ledger reports "0 failed" and cannot
# tell that from "recorded nothing".
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

SETUP_RC=99   # scripts/lib/sandbox-daemon.sh's SANDBOX_SETUP_RC default

# --- the broken daemons ------------------------------------------------------
# A pogod that dies at once — what a LOST PORT RACE really looks like, because
# pogod log.Fatalf's on a failed bind. This is the incident's own shape.
DEAD_POGOD="$WORK/pogod-dies"
printf '#!/bin/sh\necho "pogod: failed to listen on 127.0.0.1:$2: address already in use" >&2\nexit 1\n' > "$DEAD_POGOD"
chmod +x "$DEAD_POGOD"

# A pogod that lives but never serves — the other half, and the one a liveness
# check alone cannot see. Startup wedge, wrong interface, a hung init.
MUTE_POGOD="$WORK/pogod-mute"
printf '#!/bin/sh\nexec sleep 60\n' > "$MUTE_POGOD"
chmod +x "$MUTE_POGOD"

# ===========================================================================
# 1. END TO END — the REAL live control, with a sandbox daemon that cannot boot.
# ===========================================================================
# Deliberately the whole file, not the library: the claim under test is about
# what pogo-self-deploy_live_test.sh DOES when its sandbox breaks, and a library
# that behaves correctly inside a file that ignores it would satisfy a narrower
# assertion while the gate stayed broken.
#
# Both binaries are handed in prebuilt so the run reaches the sandbox in about a
# second instead of building a pogod it is never going to start.
LIVE_OUT="$WORK/live-out.txt"
POGO_LIVE_CONTROL_POGOD="$DEAD_POGOD" \
POGO_LIVE_CONTROL_POGO="$DEAD_POGOD" \
    bash "$HERE/pogo-self-deploy_live_test.sh" > "$LIVE_OUT" 2>&1
LIVE_RC=$?

[ "$LIVE_RC" = "$SETUP_RC" ] \
    && pass "a live control whose sandbox pogod cannot boot exits $SETUP_RC (the SETUP code), not 1 (the assertion-tally code) — a caller can tell 'it never ran' from 'it ran and found something'" \
    || fail "the broken sandbox exited $LIVE_RC, expected $SETUP_RC — an infrastructure failure is still indistinguishable from an assertion failure by exit code, which is how mg-3412's 14 false findings reached a coordinator"

grep -q "SETUP FAILURE" "$LIVE_OUT" \
    && pass "the run names itself a SETUP FAILURE in its own output — the first line a human reads says which kind of failure this is" \
    || fail "the broken sandbox printed no 'SETUP FAILURE' banner; output was: $(head -20 "$LIVE_OUT" | tr '\n' '|')"

grep -q "could not bind" "$LIVE_OUT" \
    && pass "it names the CAUSE (the daemon exited before answering; it could not bind) — the incident's real first line, no longer buried under thirteen others" \
    || fail "the setup failure did not explain WHY the sandbox never came up: $(head -20 "$LIVE_OUT" | tr '\n' '|')"

# THE ASSERTION THIS FILE EXISTS FOR. Not "it failed" — "it did not LIE". A
# broken sandbox that emits even one FAIL: line has reproduced the defect,
# because that line is a claim about the tree that nothing measured.
BAD_FAILS="$(grep -c '^FAIL:' "$LIVE_OUT" || true)"; BAD_FAILS="${BAD_FAILS:-0}"
[ "$BAD_FAILS" = "0" ] \
    && pass "THE ASK: a broken sandbox produced ZERO assertion failures — no 'FAIL:' line, so nothing claims a control stopped working when no control ever ran" \
    || fail "the broken sandbox emitted $BAD_FAILS FAIL: line(s) — this is mg-3412 verbatim: assertion-shaped output about a tree that was never tested. $(grep '^FAIL:' "$LIVE_OUT" | head -3 | tr '\n' '|')"

BAD_PASSES="$(grep -c '^PASS:' "$LIVE_OUT" || true)"; BAD_PASSES="${BAD_PASSES:-0}"
[ "$BAD_PASSES" = "0" ] \
    && pass "...and ZERO passes: it banked no credibility a sandbox it never established could not have earned" \
    || fail "the broken sandbox emitted $BAD_PASSES PASS: line(s) — green from a control that never ran is the fail-open direction, and the more dangerous one"

# do_prove gates the nightly redeploy on observing PROVED: RED and PROVED: GREEN
# in this output. A setup failure that leaked either would let a deploy proceed
# on a detector that was never exercised — mg-bfe5's defect, rebuilt.
BAD_PROVED="$(grep -c '^PROVED:' "$LIVE_OUT" || true)"; BAD_PROVED="${BAD_PROVED:-0}"
[ "$BAD_PROVED" = "0" ] \
    && pass "and ZERO PROVED tokens — do_prove cannot be handed a deploy gate's evidence by a run that never stood up a daemon" \
    || fail "the broken sandbox emitted $BAD_PROVED PROVED: token(s) — do_prove would read them as 'the artifact's detector was demonstrated' and deploy on a run that never started a pogod"

# ===========================================================================
# 2. THE OTHER BREAK — a daemon that is ALIVE and never answers.
# ===========================================================================
# The liveness check in sandbox_daemon_start would pass here forever, so this is
# the case that proves the readiness deadline is a real refusal and not a
# formality. Driven against the library directly: the end-to-end path is already
# established above, and this costs the full 20s boot budget, which is worth
# paying once rather than through another whole live-control startup.
SETUP_OUT="$WORK/mute-out.txt"
(
    # shellcheck source=/dev/null
    source "$REPO_ROOT/scripts/lib/sandbox-daemon.sh"
    trap sandbox_port_release EXIT
    sandbox_port_reserve
    sandbox_daemon_start "$MUTE_POGOD" "$SANDBOX_PORT" "$WORK/mute.log" /agents/drain
    echo "RETURNED NORMALLY"
) > "$SETUP_OUT" 2>&1
MUTE_RC=$?

if [ "$MUTE_RC" = "$SETUP_RC" ] && grep -q "never answered" "$SETUP_OUT" && ! grep -q "RETURNED NORMALLY" "$SETUP_OUT"; then
    pass "a sandbox daemon that RUNS but never serves is refused at the readiness deadline (exit $SETUP_RC, 'never answered') — liveness alone is not readiness, and the start does not return on it"
else
    fail "an alive-but-mute daemon returned rc=$MUTE_RC — a wedged pogod would be handed to the controls as a working sandbox: $(head -10 "$SETUP_OUT" | tr '\n' '|')"
fi

# ===========================================================================
# 3. THE RESERVATION IS EXCLUSIVE — the fix's load-bearing claim.
# ===========================================================================
# Everything above is about REPORTING a broken sandbox. This is about not having
# one: the reason four concurrent runs no longer collide. The old code could not
# pass this — it probed, so every concurrent caller was told the same first
# candidate was free, which is the whole bug in one line.
#
# Six concurrent claimants, more than the four that broke it. If the reservation
# is atomic they get six distinct ports; if it is a probe they collide.
RES_DIR="$WORK/reservations"
mkdir -p "$RES_DIR"
for i in 1 2 3 4 5 6; do
    (
        # shellcheck source=/dev/null
        source "$REPO_ROOT/scripts/lib/sandbox-daemon.sh"
        sandbox_port_reserve
        echo "$SANDBOX_PORT" > "$RES_DIR/$i"
        # Hold the claim while the others race, exactly as a real run does for
        # its whole lifetime. Releasing here would let a later claimant take the
        # same port legitimately and the control would prove nothing.
        sleep 3
        sandbox_port_release
    ) &
done
wait
RES_TOTAL="$(cat "$RES_DIR"/* 2>/dev/null | grep -c .)"
RES_UNIQ="$(cat "$RES_DIR"/* 2>/dev/null | sort -u | grep -c .)"
if [ "$RES_TOTAL" = "6" ] && [ "$RES_UNIQ" = "6" ]; then
    pass "six CONCURRENT claimants got six DISTINCT ports ($(cat "$RES_DIR"/* | sort -n | tr '\n' ' ')) — the port is reserved, not probed, so the load that broke mg-3412 cannot hand two runs one daemon"
else
    fail "six concurrent claimants produced $RES_TOTAL reservations over $RES_UNIQ distinct ports — the allocator is not exclusive, and concurrent live controls will share a daemon again"
fi

# CONDITIONAL, not a leak: a released port must become claimable again. An
# allocator that never gives anything back would satisfy the assertion above by
# exhausting its range, and would strand the nightly after a few hundred deploys.
(
    # shellcheck source=/dev/null
    source "$REPO_ROOT/scripts/lib/sandbox-daemon.sh"
    sandbox_port_reserve
    FIRST="$SANDBOX_PORT"
    sandbox_port_release
    SANDBOX_PORT=""
    POGO_SANDBOX_PORT_LO="$FIRST" POGO_SANDBOX_PORT_HI="$FIRST" sandbox_port_reserve
    sandbox_port_release
    [ "$SANDBOX_PORT" = "$FIRST" ]
) >/dev/null 2>&1 \
    && pass "a RELEASED port is immediately claimable again — the reservation is a lease the EXIT trap returns, not a one-way consumption of the range" \
    || fail "a released port could not be re-claimed — reservations leak, and the range would be exhausted by repeated deploys until every live control setup-failed"

echo ""
[ -s "$RESULTS_FILE" ] || { echo "ledger unreadable/empty — verdict cannot be trusted"; exit 1; }
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
