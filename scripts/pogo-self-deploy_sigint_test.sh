#!/usr/bin/env bash
# Standalone interrupt-safety control for the DEPLOY SCRIPT's SIGINT trap
# (mg-e201; relocated from pogo-self-deploy_live_test.sh section (g), where the
# assertion originally lived under mg-8b48 and was de-flaked by mg-e91e).
#
# WHY THIS FILE EXISTS SEPARATELY FROM THE live_test.sh CONTROL
# ------------------------------------------------------------
# pogo-self-deploy_live_test.sh proves the pogod DETECTOR — it emits
# `PROVED: RED` / `PROVED: GREEN` tokens, and the driver's do_prove refuses to
# deploy unless it observes BOTH. do_prove runs that file inside a command
# substitution (`out="$(bash live_test.sh)"`) so it can grep those tokens back.
#
# This assertion is a DIFFERENT KIND of control. It proves the DEPLOY SCRIPT's
# INT-trap logic in cmd_redeploy — that a Ctrl-C during the drain window stops
# the deploy (exit 130) and restores dispatch, rather than a returning handler
# that restores dispatch and then carries on building and kickstarting with the
# fleet live. It emits NEITHER `PROVED:` token, because it proves nothing about
# the detector. It was miscategorised into live_test.sh's artifact-gate and so
# was dragged into do_prove's comsub, where it does not belong (mg-e201).
#
# AND THE COMSUB IS WHY IT COULD NOT LIVE THERE. The control signals its own
# process GROUP to model a terminal Ctrl-C faithfully (a real Ctrl-C hits every
# process in the foreground group at once — mg-e91e). It launches sigtest into
# its OWN process group (perl setsid) so `kill -INT 0` stays contained to the
# sigtest tree. That containment holds in the DIRECT context this file runs in —
# `bash pogo-self-deploy_sigint_test.sh` from test.sh, a natural process-group
# boundary, the exact context where mg-e91e's fix already passed 19x green. It
# did NOT hold under the real deploy, which runs detached with no TTY:
# redeploy-launch.py(setsid) -> pogo-self-deploy -> do_prove ->
# `out="$(bash live_test.sh)"` — do_prove's own comsub adds a group/session
# layer, so the sigtest sat in a pgid/session topology it was never written for
# and deterministically observed exit 4, blocking every redeploy through
# do_prove. Relocating to the direct/suite context dissolves the whole class:
# no TTY-interrupt control ever fights the deploy's no-TTY topology again.
#
# WHAT IT NEEDS, AND WHY IT STANDS UP A REAL DAEMON
# -------------------------------------------------
# cmd_redeploy's drain phase makes REAL curls: drain_state reads the daemon,
# drain_post true mutates it, and on SIGINT the EXIT trap's restore_drain curls
# it back to the prior value. The final assertion — dispatch really was restored
# on the way out — can only be read from a live daemon. So this file stands up a
# real pogod in a sandbox pinned to a throwaway HOME/XDG/POGO_HOME and a spare
# port, exactly as live_test.sh does, so it cannot see or touch the live fleet.
# It needs neither the pogo CLI nor the mail-check roster nor the artifact
# discipline of that file — only a daemon that answers /agents/drain and a git
# fixture whose HEAD has diverged from its deploy ref (so the redeploy really
# reaches the drain window rather than short-circuiting as "nothing owed").

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
    # Make the tree writable before removing it: any `go` call under the sandbox
    # HOME materialises the toolchain module into $SANDBOX/home/go/pkg/mod, which
    # Go marks 0444 by design, so a plain `rm -rf` fails "Permission denied"
    # (mg-e91e). chmod clears the read-only bit so the removal completes.
    chmod -R u+w "$SANDBOX" 2>/dev/null
    rm -rf "$SANDBOX"
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

# set -u, deliberately not set -e (a failed `>>` must not abort silently), and a
# guarded ledger write so an unreadable RESULTS_FILE reports its own failure AT
# the point of failure rather than being inferred from a downstream tally that
# cannot tell "zero failures" from "recorded nothing" (mg-c02d rationale).
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

# The daemon under test. Built from $REPO_ROOT — this is a control on the COMMIT
# in the normal suite, not the artifact gate (that is do_prove's job, and this
# file is deliberately outside it). Build FIRST, under the real HOME, before the
# sandbox override below: Go resolves GOPATH/GOMODCACHE off $HOME, so building
# after the override would re-download the whole module cache into $SANDBOX.
echo "Building pogod into the sandbox..."
if ! (cd "$REPO_ROOT" && go build -o "$SANDBOX/pogod" ./cmd/pogod); then
    fail "could not build cmd/pogod — the interrupt-safety control cannot run"
    exit 1
fi

# --- sandbox: a real pogod that cannot reach the real fleet ------------------
# POGO_HOME must be pinned explicitly: this box exports POGO_HOME=$HOME from a
# stale profile, so setting HOME alone leaks onto the live ~/.pogo. Likewise
# XDG_CONFIG_HOME (config.toml is layered). POGO_AGENT_AUTOSTART=false so the
# sandbox daemon starts no crew.
export HOME="$SANDBOX/home"
export XDG_CONFIG_HOME="$SANDBOX/xdg"
export POGO_HOME="$SANDBOX/home/.pogo"
export POGO_AGENT_AUTOSTART=false
mkdir -p "$HOME" "$XDG_CONFIG_HOME"

# A spare port, probed free. Never the default 10000: that is the live daemon.
PORT=""
for candidate in $(seq 17731 17799); do
    if ! curl -sf --max-time 1 "http://127.0.0.1:$candidate/agents/drain" >/dev/null 2>&1; then
        PORT="$candidate"; break
    fi
done
if [ -z "$PORT" ]; then
    fail "no free port in 17731-17799 for the sandbox daemon"
    exit 1
fi

"$SANDBOX/pogod" -port "$PORT" > "$SANDBOX/pogod.log" 2>&1 &
POGOD_PID=$!

URL="http://127.0.0.1:$PORT"
up=false
for _ in $(seq 1 80); do
    if curl -sf --max-time 2 "$URL/agents/drain" >/dev/null 2>&1; then up=true; break; fi
    sleep 0.25
done
if ! $up; then
    fail "sandbox pogod never answered on $URL"
    sed 's/^/  pogod: /' "$SANDBOX/pogod.log"
    exit 1
fi

# Point the driver's own primitives at the sandbox daemon, then source it.
# main() will NOT run because BASH_SOURCE != $0. dr_state below reads /agents/drain
# through the driver's own json_bool, so the verdict parses the body the same way
# the code under test does.
export POGO_PORT="$PORT"
# shellcheck source=/dev/null
source "$REPO_ROOT/scripts/pogo-self-deploy"

[ "$(base_url)" = "$URL" ] \
    && pass "driver resolves base_url to the sandbox daemon (not the live fleet)" \
    || fail "base_url is $(base_url), expected $URL — the test would be probing the WRONG daemon"

# A pogo checkout whose HEAD is NOT $DEPLOY_REF -> do_build's first exit 4, so a
# redeploy that is NOT interrupted reaches (and dies in) do_build AFTER the drain
# window — the window this control fires the signal in. Built here rather than
# reusing $REPO_ROOT so it does not depend on whether this worktree is clean.
DR_REPO="$SANDBOX/drain-repo"
mkdir -p "$DR_REPO"
(
    cd "$DR_REPO" && git init -q . && git config user.email t@t && git config user.name t
    echo one > f && git add f && git commit -qm one
    git branch -f main-fixture
    echo two > f && git commit -qam two   # HEAD now != main-fixture
) >/dev/null 2>&1

# POGO_GOBIN -> an empty dir so installed_rev reports <missing> != MAIN ->
# NEEDS_BUILD=true -> do_build really runs (the fallthrough case for an
# uninterrupted run).
mkdir -p "$SANDBOX/nobin"

# dr_state — the live draining flag, read the way the code under test writes it.
dr_state() { curl -sf --max-time 5 "$URL/agents/drain" 2>/dev/null | json_bool draining; }

# ===========================================================================
# A SIGNAL RESTORES AND STOPS — it does not restore and CARRY ON. A bash signal
# handler that returns resumes the script at the point of interruption, so the
# obvious `trap restore_drain EXIT INT TERM` would turn dispatch back on and then
# keep building and kickstarting with the fleet live: a cleanup that fires and
# then un-fires itself. Ctrl-C during a 30-minute drain wait is the most likely
# way a human ever enters this path, so it gets an assertion rather than an
# argument. Driven with a real signal against a real daemon.
# ===========================================================================
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
# Runs as its OWN script, launched into its OWN process group (perl setsid — this
# box's bash is 3.2 and macOS ships no `setsid` binary), for two reasons. First,
# 3.2 has no $BASHPID, so inside a ( subshell ) `$$` is still the PARENT's pid and
# a self-signal would hit this test file, not the code under test. Second — and
# this is what makes the control DETERMINISTIC instead of a timing flake (mg-e91e)
# — the signal below is delivered to the whole process GROUP, exactly as a
# terminal Ctrl-C is: the kernel signals every process in the foreground group at
# once. The own-group launch bounds that blast radius to this sigtest tree, so
# `kill -INT 0` cannot reach the harness, the sandbox pogod, or the live fleet.
#
# WHY A GROUP SIGNAL AND NOT `kill -INT $$` (mg-e91e). The old form fired a
# single-target async SIGINT at the parent from INSIDE a `$(drain_wait)`
# command-substitution child that then ran `sleep 2; echo 0; return 0` and exited
# 0 CLEANLY. Whether the parent's pending INT trap ran (exit 130) or the signal
# was coalesced/lost as the child returned 0 was a bash-3.2 signal-delivery race:
# green under light load, and deterministically RED under the full control suite,
# where it observed exit 4 and blocked every redeploy through do_prove. A real
# Ctrl-C has no such race — it hits the child too, so the child never returns 0.
# Signalling the group models that faithfully and removes the clean-return path,
# and with it the race.
#
# rc is the discriminator, and it is what makes this control able to fail:
#   130 = the signal stopped the deploy (correct).
#     4 = the handler RETURNED, drain_wait completed, and the deploy carried on
#         into do_build — the returning-handler bug, which restores dispatch and
#         then rebuilds and kickstarts the fleet anyway.
# rc ALONE cannot tell a returning handler from a signal that never arrived —
# both leave the abort unproven — so the parent's own trap message is captured as
# a POSITIVE sub-assertion below: a lost/coalesced signal fails LOUD as "never
# delivered", not as a false returning-handler verdict.
cat > "$SANDBOX/sigtest.sh" <<SIGEOF
#!/bin/bash
set -u
source "$REPO_ROOT/scripts/pogo-self-deploy"
# This script sources the driver FRESH, so it does not inherit any running_rev
# override and needs its own (mg-8f09 — the sandbox daemon's real stamp is
# foreign to the DR_REPO fixture, and a foreign stamp is now a refusal, which
# would stop this run before it ever reached drain_wait).
running_rev() { git -C "$DR_REPO" rev-parse HEAD 2>/dev/null; }
# Likewise the out-of-band guard (mg-1bbf): this child is a descendant of the
# pogod that spawned the agent running test.sh, so the real assert_out_of_band
# would refuse before the drain window this control exists to interrupt. The
# run is fully sandboxed (fixture repo, POGO_GOBIN=\$SANDBOX/nobin, sandbox
# daemon) and never reaches launchctl. Source-level only — a real invocation
# cannot stub anything, and pogo-self-deploy_test.sh pins the live wiring.
assert_out_of_band() { :; }
# The human hits Ctrl-C while the deploy waits for the fleet to quiesce. Model it
# faithfully: reset THIS command-substitution child's INT to default first, so the
# group signal kills the child SILENTLY — a child that inherited the driver's INT
# trap would print the parent's "interrupted (SIGINT)" line and forge the positive
# delivery evidence the verdict below reads — then signal the whole foreground
# process GROUP as a terminal Ctrl-C does. The child dies instead of returning 0,
# so there is no clean-return race with the parent's pending trap (mg-e91e).
drain_wait() { trap - INT; kill -INT 0; sleep 2; echo 0; return 0; }
POGO_GOBIN="$SANDBOX/nobin"
REPO="$DR_REPO" DEPLOY_REF=main-fixture
ASSUME_YES=true FORCE=false SKIP_DRAIN=false
cmd_redeploy
SIGEOF
# setsid puts sigtest in its own process group so `kill -INT 0` stays contained;
# macOS has no `setsid` binary and bash 3.2 cannot self-setpgid, so perl does it
# (it exec's bash, so bash's exit status is what $? captures). stderr is kept, not
# discarded — the positive sub-assertion reads the parent trap's message from it.
POGO_PORT="$PORT" perl -e 'use POSIX; POSIX::setsid() or die "setsid: $!"; exec("/bin/bash", $ARGV[0]) or die "exec: $!"' \
    "$SANDBOX/sigtest.sh" >/dev/null 2>"$SANDBOX/sigtest.err"
DR_SIG_RC=$?
# POSITIVE sub-assertion (mg-e91e): the SIGINT actually reached the PARENT's INT
# trap. The driver's handler (pogo-self-deploy: `trap '...exit 130' INT`) prints
# this exact line on its way out, and the child was silenced above, so this line
# can ONLY be the parent. Its ABSENCE means the signal was lost/coalesced before
# the parent could act — a control-harness delivery fault that must fail LOUD, not
# be read as a live verdict about the handler.
if grep -q 'interrupted (SIGINT) during the drain window' "$SANDBOX/sigtest.err"; then
    pass "the drain-window SIGINT was DELIVERED to the parent trap (the driver's INT handler ran) — the rc verdict below is about the handler, not about a lost signal"
    case "$DR_SIG_RC" in
        130) pass "SIGINT in the drain window STOPS the real cmd_redeploy at the signal (exit 130) — it does not restore dispatch and then carry on building" ;;
        4)   fail "SIGINT's handler RETURNED and the deploy resumed into do_build (exit 4) — a returning INT handler restores dispatch and then rebuilds and kickstarts the fleet anyway" ;;
        *)   fail "the INT trap fired but cmd_redeploy exited $DR_SIG_RC, not 130 — the signal was delivered yet the deploy did not abort cleanly" ;;
    esac
else
    fail "the drain-window SIGINT NEVER reached cmd_redeploy's INT trap (no 'interrupted (SIGINT)' from the driver; rc=$DR_SIG_RC) — a lost/coalesced signal, i.e. a control-harness delivery fault, NOT a returning-handler bug. Read no handler verdict from this run."
fi
[ "$(dr_state)" = "false" ] \
    && pass "SIGINT in the drain window restores dispatch on the way out (Ctrl-C cannot strand the fleet either)" \
    || fail "SIGINT left the live daemon at draining=$(dr_state) — an aborted deploy strands the fleet exactly like a failed build"

# Leave the daemon dispatching for anything downstream.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1

echo ""
# The ledger must be readable and non-empty before any verdict is drawn from it:
# `grep -c` exits 1 on no-match, so the `|| true` below cannot tell a real zero
# from "could not read the file". This makes that distinction first.
[ -s "$RESULTS_FILE" ] || { echo "ledger unreadable/empty — verdict cannot be trusted"; exit 1; }
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
