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
#
# WHICH HALF IS THE INSTRUMENT, AND WHICH IS THE SCAFFOLDING (mg-b4a5)
# --------------------------------------------------------------------
# This file breaks the sandbox ON PURPOSE, so "put it inside the sandbox" needs
# saying carefully. The two halves are:
#
#   THE INSTRUMENT — the thing being broken and read back. §1 runs the real live
#   control against a pogod that cannot boot; §2 hands the PACKAGED daemon start
#   a pogod that never serves; §3 races six claimants through the port allocator.
#   Every one of those runs in its OWN process or subshell, because each ends in
#   `exit 99` and a control that ran them in-process would take the exit with it.
#   None of them may be routed through this file's own envelope, and none is.
#
#   THE SCAFFOLDING — this file's root, its fake pogod binaries, its captured
#   output, its HOME. That IS routed through scripts/pogo-sandbox now, where it
#   used to be a bare `mktemp -d` and no environment override at all.
#
# The scaffolding half is worth converting for a reason specific to this file:
# §1 boots the REAL live suite end to end, and the failure this file exists to
# catch is that suite's isolation not working. If it ever does not work, the
# child writes to whatever HOME it inherited — so inheriting a sandbox one is
# what keeps the detector-of-detectors from being the thing that damages the
# developer's tree on the day it finds something. The child still establishes
# (and is still asserted to establish) its own sandbox; this is the floor under
# it, not a substitute for it.
#
# The direction NOT taken: this file's own envelope uses only create/isolate/down.
# It never reserves a port and never starts a daemon of its own, so nothing it
# depends on for its own setup is a thing §2 or §3 deliberately breaks.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

# The packaged sandbox (mg-78a5). Sourced at the top so its functions are
# inherited by every subshell below — §2 and §3 used to re-source
# lib/sandbox-daemon.sh for themselves, which is the third copy of an envelope
# that is supposed to have one.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
pogo_sandbox_create setup
WORK="$POGO_SANDBOX_DIR"
cleanup() { pogo_sandbox_down; rm -f "$RESULTS_FILE"; }
trap cleanup EXIT

# Go resolves GOPATH/GOMODCACHE/GOCACHE off $HOME, and the child control in §1
# builds its witness fixture FROM SOURCE after we have moved HOME — so without
# this it would re-download the module cache and the toolchain into the sandbox:
# minutes of network per run, and a 0444 tree the teardown then has to chmod its
# way out of. Read here, under the REAL HOME, and exported so the child keeps
# them. They are build caches, not pogo state: none of the four things
# pogo_sandbox_isolate pins (HOME, XDG_CONFIG_HOME, POGO_HOME, MG_ROOT) is
# reachable through them, so nothing the child asserts on comes from here.
GOMODCACHE="$(go env GOMODCACHE)"; export GOMODCACHE
GOCACHE="$(go env GOCACHE)"; export GOCACHE
GOPATH="$(go env GOPATH)"; export GOPATH

pogo_sandbox_isolate

# The child in §1 mints its OWN root. POGO_SANDBOX_ROOT is honoured by
# pogo_sandbox_create, so an exported one would put parent and child in the same
# directory — where the child's isolate would find a non-empty POGO_HOME and its
# teardown would rm -rf the root this run is still using. Ours has already been
# read; drop it before anything is spawned.
unset POGO_SANDBOX_ROOT

# Same guarded ledger writes as the controls this file is about, and for the same
# reason: a tally drawn from an unwritable ledger reports "0 failed" and cannot
# tell that from "recorded nothing".
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

SETUP_RC=99   # scripts/lib/sandbox-daemon.sh's SANDBOX_SETUP_RC default

# ===========================================================================
# 0. THE FLOOR UNDER §1 IS REALLY THERE — this run's own HOME is private.
# ===========================================================================
# Every other assertion in this file is about a sandbox being BROKEN. This one is
# about this file's own, and it exists because §1 boots the real live suite end
# to end: if that suite's isolation is the thing that has broken, the child writes
# to whatever HOME it inherited, and the only reason that is not the developer's
# is the pogo_sandbox_isolate above. An envelope nothing observes is an envelope
# that can be reordered away — moving isolate below §1 would cost nothing visible
# without this line.
#
# It is an OBSERVATION, not a restatement of isolate's own checks: it writes
# through $HOME and reads back WHERE THE BYTES LANDED. A pogo_sandbox_isolate
# hard-wired to return success would satisfy a re-comparison of the same paths
# and would fail this.
#
# (This is not the positive direction the header declines to assert. That one is
# "a WORKING sandbox does not report a setup failure", which is what test.sh's
# live run immediately before this file already observes at full cost.)
SBX_PROBE=".setup-control-probe-$$"
: > "$HOME/$SBX_PROBE" 2>/dev/null
if [ -f "$POGO_SANDBOX_DIR/home/$SBX_PROBE" ] && [ ! -e "$POGO_SANDBOX_REAL_HOME/$SBX_PROBE" ]; then
    pass "this control's OWN \$HOME is private: a write through it landed inside the sandbox root and nothing appeared in the developer's home — so the live suite booted in §1 has a floor under it even on the day its own isolation is what broke"
else
    fail "a write through this control's \$HOME did not land in the sandbox ($POGO_SANDBOX_DIR/home) — the run that breaks the live suite's sandbox on purpose in §1 is itself unisolated, and a child whose isolation has regressed would write to the developer's home"
fi
rm -f "$HOME/$SBX_PROBE"

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
# The liveness check would pass here forever, so this is the case that proves the
# readiness deadline is a real refusal and not a formality. It costs the full 20s
# boot budget, which is worth paying once rather than through another whole
# live-control startup — the end-to-end path is already established above.
#
# Driven through pogo_sandbox_daemon, the PACKAGED entry point, rather than
# lib/sandbox-daemon.sh's sandbox_daemon_start underneath it (mg-b4a5). Since
# mg-78a5 that is the call every real caller makes — live_test.sh, the sigint
# control, `pogo-sandbox run --daemon` — so a control that reached past it into
# the library would be proving the deadline still exists in a layer nobody enters
# directly, and would keep passing if the wrapper stopped calling it.
#
# In a SUBSHELL, and that is not cosmetic: the refusal under test IS `exit 99`,
# so in-process it would end this file at §2 with three sections unrun. The
# subshell is also why the port claim is released with sandbox_port_release and
# NOT pogo_sandbox_down — the root that would remove belongs to the parent, which
# is still using it.
SETUP_OUT="$WORK/mute-out.txt"
(
    trap sandbox_port_release EXIT
    pogo_sandbox_daemon "$MUTE_POGOD" /agents/drain "$WORK/mute.log"
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
#
# The range is narrowed to the just-released port by assigning SANDBOX_PORT_LO/HI
# directly rather than by re-sourcing the library with POGO_SANDBOX_PORT_LO set:
# those env vars are read ONCE, at source time, so with the library now inherited
# from the parent the old form would have re-claimed some OTHER free port and the
# comparison below would have failed for a reason that has nothing to do with the
# lease. Narrowing the range is scaffolding either way; the claim is that the
# release gave the port back.
(
    sandbox_port_reserve
    FIRST="$SANDBOX_PORT"
    sandbox_port_release
    SANDBOX_PORT=""
    SANDBOX_PORT_LO="$FIRST"; SANDBOX_PORT_HI="$FIRST"
    sandbox_port_reserve
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
