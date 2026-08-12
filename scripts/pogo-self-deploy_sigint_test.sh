#!/usr/bin/env bash
# Standalone interrupt-safety control for the DEPLOY SCRIPT's SIGINT trap
# (mg-e201; relocated from pogo-self-deploy_live_test.sh section (g), where the
# assertion originally lived under mg-8b48 and was de-flaked by mg-e91e, and made
# launch-context-independent by mg-a6c6 — see sigint_own_group_run).
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
# real pogod in a sandbox pinned to a throwaway HOME/XDG/POGO_HOME/MG_ROOT and a
# spare port, exactly as live_test.sh does, so it cannot see or touch the live
# fleet. It needs neither the pogo CLI nor the mail-check roster nor the artifact
# discipline of that file — only a daemon that answers /agents/drain and a git
# fixture whose HEAD has diverged from its deploy ref (so the redeploy really
# reaches the drain window rather than short-circuiting as "nothing owed").

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

# The packaged sandbox (mg-78a5), not lib/sandbox-daemon.sh directly.
#
# This file used to take the port allocator from the library and hand-roll the
# rest of the envelope: its own mktemp root, its own HOME/XDG/POGO_HOME exports,
# its own kill-and-rm cleanup. That is the shape four tickets were already filed
# for (mg-6092, mg-e8e7, mg-5336, mg-3412) — isolation re-derived at the call
# site is isolation somebody has to REMEMBER — and this copy had already drifted
# from the one next door in a way that mattered: it never pinned MG_ROOT, so the
# real cmd_redeploy it drives resolves `mg` against $HOME/.macguffin, and the
# only reason the live mail store stayed untouched was that the run aborts before
# the driver's first send. A control whose isolation depends on where the code
# under test happens to stop is not isolated; it is lucky.
#
# Sharing the ALLOCATOR was already the point (a lock only one of two callers
# honours is not a lock — mg-3412). Sharing the whole envelope is the same
# argument carried to its end: pogo_sandbox_isolate CHECKS what the exports above
# only asserted, refusing a HOME/POGO_HOME/MG_ROOT that resolves onto the
# developer's tree before a single assertion runs.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
# The private root, vetted before anything is written into it. No environment
# variable moves yet: the go build below must run under the REAL HOME (see the
# note above it), so pogo_sandbox_isolate comes after it.
pogo_sandbox_create sigint
SANDBOX="$POGO_SANDBOX_DIR"

cleanup() {
    # Kills the sandbox daemon BY PID (an unanchored `pkill -f pogod` on this box
    # would take out the machine's live daemon and every agent poller with it),
    # hands the port claim back, and removes the root — chmod'ing it writable
    # first, because any `go` call under the sandbox HOME materialises Go's module
    # cache there and Go marks it 0444 by design (mg-e91e). All four steps now
    # live in pogo_sandbox_down rather than being restated here.
    pogo_sandbox_down
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

# set -u, deliberately not set -e (a failed `>>` must not abort silently), and a
# guarded ledger write so an unreadable RESULTS_FILE reports its own failure AT
# the point of failure rather than being inferred from a downstream tally that
# cannot tell "zero failures" from "recorded nothing" (mg-c02d rationale).
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
# A THIRD outcome, because two cannot hold three states (mg-a6c6). PASS and FAIL
# are both claims about the code under test; "this harness cannot deliver a
# SIGINT at all" is a claim about the INSTRUMENT, and folding it into FAIL is how
# a launch-context fault gets reported as a defect in whatever branch is under
# test. Same argument as pogo-condition-controls.sh's instrument_fail: a control
# that says "the handler did not abort" when in truth it never managed to signal
# anything is worse than no control, because it produces a confident wrong
# diagnosis. INCONCLUSIVE rows are tallied separately and do NOT fail the run.
inconclusive() { echo "INCONCLUSIVE: $1"; echo "INCONCLUSIVE: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

# finish — the verdict, drawn once, from one place. A function rather than a tail
# block because there are now two ways to reach the end (the control ran, or the
# instrument could not run it) and a tally that only exists at the bottom of the
# file is a tally the early exit has to restate.
finish() {
    echo ""
    # The ledger must be readable and non-empty before any verdict is drawn from
    # it: `grep -c` exits 1 on no-match, so the `|| true` below cannot tell a real
    # zero from "could not read the file". This makes that distinction first.
    [ -s "$RESULTS_FILE" ] || { echo "ledger unreadable/empty — verdict cannot be trusted"; exit 1; }
    local pass_count fail_count skip_count
    pass_count=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
    fail_count=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
    skip_count=$(grep -c '^INCONCLUSIVE:' "$RESULTS_FILE" 2>/dev/null || true)
    pass_count=${pass_count:-0}; fail_count=${fail_count:-0}; skip_count=${skip_count:-0}
    echo "=== Results: $pass_count passed, $fail_count failed, $skip_count inconclusive ==="
    if [ "$skip_count" -gt 0 ]; then
        echo ""; echo "Inconclusive:"; grep '^INCONCLUSIVE:' "$RESULTS_FILE" | sed 's/^/  /'
    fi
    if [ "$fail_count" -gt 0 ]; then
        echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
        exit 1
    fi
    exit 0
}

# sigint_own_group_run SCRIPT ERRFILE — launch SCRIPT into its OWN process group
# with SIGINT/SIGQUIT PROVEN resettable, capturing its stderr. Returns the
# script's exit status.
#
# THE RESET IS THE WHOLE POINT (mg-a6c6). A shell without job control sets SIGINT
# and SIGQUIT to SIG_IGN for its ASYNCHRONOUS children, and SIG_IGN crosses both
# fork and exec. So every process below `./build.sh &` — build.sh, test.sh, this
# file, and the sigtest it launches — enters with SIGINT already ignored, and
# bash will not install a trap for a signal that was ignored on entry ("signals
# ignored upon entry to the shell cannot be trapped or reset"). The driver's
# `trap '...exit 130' INT` therefore never existed, `kill -INT 0` did nothing,
# cmd_redeploy carried on into do_build, and this control reported exit 4 —
# DETERMINISTICALLY, every time, on a tree with nothing whatsoever wrong with it.
# Measured both directions on one tree in one minute: foreground exit 0, `&`
# exit 1. That is an instrument failure wearing the costume of a defect, and the
# ergonomic invocation — an agent backgrounding a long gate while it works on
# something else — is the one that trips it.
#
# Bash cannot dig itself out (the quoted rule is exactly about that), but the
# launcher is not bash. perl already sits in front of the exec to do setsid, and
# perl CAN reset the disposition: `$SIG{INT} = "DEFAULT"` is a sigaction to
# SIG_DFL, and SIG_DFL is inherited across exec just as SIG_IGN is. So bash comes
# up with a resettable SIGINT and arms its trap normally. This is the same
# disposition-inheritance trap mg-9aa1 hit with SIGHUP, in a second guise; the
# reset is the SIGINT analogue of the `signal.Notify` reset used there.
#
# This makes the control MORE faithful, not less. What it models is a human
# hitting Ctrl-C at a terminal, and a terminal foreground process has SIGINT at
# SIG_DFL. The old form did not arrange that — it merely inherited it from
# whoever happened to launch the gate, which is why the verdict depended on the
# launcher instead of on the code.
#
# QUIT is reset alongside INT because SIG_IGN-for-async-children is a two-signal
# rule; leaving the sibling half armed just leaves the next guise of this trap in
# place for whoever adds a QUIT case. (TERM is untouched: nothing ignores it on
# our behalf, and the driver's TERM trap installs fine.)
sigint_own_group_run() {
    POGO_PORT="$PORT" perl -e '
        use POSIX;
        $SIG{INT}  = "DEFAULT";
        $SIG{QUIT} = "DEFAULT";
        POSIX::setsid() or die "setsid: $!";
        exec("/bin/bash", $ARGV[0]) or die "exec: $!";
    ' "$1" >/dev/null 2>"$2"
}

# The daemon under test. Built from $REPO_ROOT — this is a control on the COMMIT
# in the normal suite, not the artifact gate (that is do_prove's job, and this
# file is deliberately outside it). Build FIRST, under the real HOME, before the
# sandbox override below: Go resolves GOPATH/GOMODCACHE off $HOME, so building
# after the override would re-download the whole module cache into $SANDBOX.
echo "Building pogod into the sandbox..."
if ! (cd "$REPO_ROOT" && go build -o "$SANDBOX/pogod" ./cmd/pogod); then
    pogo_sandbox_fail "could not build cmd/pogod — the interrupt-safety control cannot run"
fi

# --- sandbox: a real pogod that cannot reach the real fleet ------------------
# HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT are pinned under the private root
# AND THEN PROVEN not to resolve onto the developer's — see pogo_sandbox_isolate.
# All four are needed and all four are checked: this box exports POGO_HOME=$HOME
# from a stale profile, so setting HOME alone leaks onto the live ~/.pogo
# (mg-5336); config.toml is layered, so XDG_CONFIG_HOME alone leaks the real user
# config; and `mg` resolves its store as --root > $MG_ROOT > $HOME/.macguffin, so
# the real cmd_redeploy driven below would otherwise reach the live mail store
# the moment it sends anything. POGO_AGENT_AUTOSTART=false (also set there) keeps
# the sandbox daemon from starting a crew.
pogo_sandbox_isolate

# A PRIVATE port, RESERVED, and a daemon PROVEN to be the one we started
# (mg-3412). Not a probe: the window between "nothing answered" and "our pogod
# bound it" is where two concurrent runs both decide they own 17731, one pogod
# dies on the bind, and the survivor's daemon answers the loser's readiness
# check. A control that then asserts on drain state is asserting on someone
# else's — and a failure of THIS file reads as an interrupt-safety regression.
pogo_sandbox_daemon "$SANDBOX/pogod" /agents/drain
PORT="$POGO_SANDBOX_PORT"
URL="$POGO_SANDBOX_URL"

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

# --- PREFLIGHT: can this harness deliver a drain-window SIGINT AT ALL? -------
# The reset above is the fix; this is the check that the fix took, and the guard
# for the case where some future launch context defeats delivery in a way the
# reset does not cover (mg-a6c6). Without it, ANY such context produces the same
# confident wrong diagnosis the reset just removed — and the next guise of a
# disposition trap will not look like this one.
#
# It measures the INSTRUMENT, and only the instrument. It never sources
# pogo-self-deploy, never calls cmd_redeploy, and installs its own trap — so a
# genuine returning-handler regression in the code under test CANNOT make this
# probe fail, and therefore cannot buy itself a skip. That is the property the
# whole three-outcome scheme rests on: an INCONCLUSIVE verdict must be
# unreachable by the defect this file exists to catch.
#
# The shape mirrors the real control exactly — own process group, an INT trap in
# the parent, a `$( )` child that resets its own INT and group-signals with
# `kill -INT 0` and would otherwise return 0 cleanly — so what it proves is the
# path the control actually uses, not an easier one. Same launcher function, so
# the two cannot drift into measuring different things.
cat > "$SANDBOX/sigprobe.sh" <<'PROBEEOF'
#!/bin/bash
set -u
trap 'echo "PROBE: INT DELIVERED" >&2; exit 130' INT
# Did the trap INSTALL? Bash reports an ignored-on-entry signal as an unset trap,
# so an empty `trap -p INT` right after arming one is the SIG_IGN inheritance
# showing itself directly — a distinct, nameable cause rather than "no signal
# arrived", which is the symptom shared by every delivery fault.
[ -n "$(trap -p INT)" ] || { echo "PROBE: INT TRAP DID NOT INSTALL — SIGINT was SIG_IGN on entry" >&2; exit 3; }
probe_wait() { trap - INT; kill -INT 0; sleep 2; echo 0; return 0; }
PROBE_OUT="$(probe_wait)"
echo "PROBE: INT NOT DELIVERED — the comsub child returned '$PROBE_OUT' cleanly and the trap never ran" >&2
exit 4
PROBEEOF
sigint_own_group_run "$SANDBOX/sigprobe.sh" "$SANDBOX/sigprobe.err"
PROBE_RC=$?
if [ "$PROBE_RC" = 130 ] && grep -q 'PROBE: INT DELIVERED' "$SANDBOX/sigprobe.err"; then
    pass "preflight: this harness CAN deliver a drain-window SIGINT to an armed trap (own process group, group signal) — so the verdict below is about cmd_redeploy, not about the launch context"
else
    inconclusive "this harness CANNOT deliver a SIGINT to a trap it armed itself (preflight rc=$PROBE_RC: $(tr '\n' ' ' < "$SANDBOX/sigprobe.err")). The interrupt-safety control was NOT RUN and this run makes NO claim about cmd_redeploy's INT handler — do not read it as either a pass or a regression."
    echo ""
    echo "  The probe never touches the code under test: it sources nothing, calls"
    echo "  no driver function, and signals only a trap it installed itself. Its"
    echo "  failure is therefore a fact about how this gate was INVOKED, not about"
    echo "  the branch under test."
    echo ""
    echo "  Most likely cause: SIGINT arrived as SIG_IGN and could not be reset."
    echo "  A shell without job control sets SIGINT/SIGQUIT to SIG_IGN for its"
    echo "  asynchronous children, and that disposition crosses fork AND exec — so"
    echo "  './build.sh &', 'nohup sh -c ... &' and '( ... ) &' all reach here with"
    echo "  SIGINT already ignored. sigint_own_group_run resets it via perl before"
    echo "  exec'ing bash, so seeing this message means that reset did not take."
    echo ""
    echo "  To get a real verdict, run the gate in the FOREGROUND:  ./build.sh"
    echo ""
    finish
fi

# ===========================================================================
# A SIGNAL RESTORES AND STOPS — it does not restore and CARRY ON. A bash signal
# handler that returns resumes the script at the point of interruption, so the
# obvious `trap restore_drain EXIT INT TERM` would turn dispatch back on and then
# keep building and kickstarting with the fleet live: a cleanup that fires and
# then un-fires itself. Ctrl-C during a 30-minute drain wait is the most likely
# way a human ever enters this path, so it gets an assertion rather than an
# argument. Driven with a real signal against a real daemon.
# ===========================================================================
# SCAFFOLDING, so it fails as scaffolding (mg-78a5). The final assertion reads
# `draining` back and calls a non-false value an interrupt-safety regression, so
# a staging call that silently did not land would produce that verdict about a
# precondition that was never established. pogo_sandbox_curl ends the run as
# INFRASTRUCTURE instead — the whole distinction mg-3412 was filed for.
pogo_sandbox_curl "could not reset the sandbox daemon to draining=false before the interrupt control" -- \
    -X POST "$URL/agents/drain" -H 'Content-Type: application/json' -d '{"draining":false}'
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
# rc ALONE cannot tell a returning handler from a handler that never ran — both
# leave the abort unproven — so the parent's own trap message is captured as a
# POSITIVE sub-assertion below, and a handler that never ran fails LOUD as
# exactly that rather than as a false returning-handler verdict. The third
# possibility, "no signal ever arrived", is not distinguished HERE at all: it is
# ruled out ahead of time by the preflight, which is why it can be. Three states
# need three tests, not a discriminator asked to carry one more than it has.
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
# Launched through the SAME function the preflight just proved, so "the probe
# passed" is a statement about this launch and not about a similar one: own
# process group (so `kill -INT 0` stays contained — macOS has no `setsid` binary
# and bash 3.2 cannot self-setpgid, so perl does it), SIGINT/SIGQUIT reset out of
# any inherited SIG_IGN, and bash's own exit status preserved through the exec.
# stderr is kept, not discarded — the positive sub-assertion below reads the
# parent trap's message from it.
sigint_own_group_run "$SANDBOX/sigtest.sh" "$SANDBOX/sigtest.err"
DR_SIG_RC=$?
# POSITIVE sub-assertion (mg-e91e): the SIGINT actually reached the PARENT's INT
# trap. The driver's handler (pogo-self-deploy: `trap '...exit 130' INT`) prints
# this exact line on its way out, and the child was silenced above, so this line
# can ONLY be the parent.
#
# WHAT ITS ABSENCE MEANS CHANGED WHEN THE PREFLIGHT WENT IN (mg-a6c6). This branch
# used to read "lost/coalesced signal — a control-harness delivery fault", because
# the harness was the only explanation anyone had for it, and under `./build.sh &`
# that reading was even CORRECT — and still misleading, because it named the
# symptom (no signal arrived) while the cause was the launch context, which the
# message could not see and did not mention. That cause is now both fixed and
# independently measured: the run cannot reach this line unless the preflight
# proved, in this same launch topology and through this same launcher, that a
# group SIGINT does reach an armed trap. So a missing message is no longer about
# delivery. It means the driver's INT handler did not run — the trap is gone,
# armed too late, or no longer prints this line. That is a verdict about the code
# under test, and it is stated as one. (Mutation-checked: deleting the driver's
# `trap ... INT` lands here and fails, backgrounded, with 0 inconclusive.)
if grep -q 'interrupted (SIGINT) during the drain window' "$SANDBOX/sigtest.err"; then
    pass "the drain-window SIGINT was DELIVERED to the parent trap (the driver's INT handler ran) — the rc verdict below is about the handler, not about a lost signal"
    case "$DR_SIG_RC" in
        130) pass "SIGINT in the drain window STOPS the real cmd_redeploy at the signal (exit 130) — it does not restore dispatch and then carry on building" ;;
        4)   fail "SIGINT's handler RETURNED and the deploy resumed into do_build (exit 4) — a returning INT handler restores dispatch and then rebuilds and kickstarts the fleet anyway" ;;
        *)   fail "the INT trap fired but cmd_redeploy exited $DR_SIG_RC, not 130 — the signal was delivered yet the deploy did not abort cleanly" ;;
    esac
else
    fail "cmd_redeploy's INT handler did NOT run (no 'interrupted (SIGINT)' from the driver; rc=$DR_SIG_RC) — the trap is missing, armed after the drain window opens, or no longer prints that line. This is NOT a delivery fault: the preflight above proved this harness delivers a group SIGINT to an armed trap, through this same launcher, in this same launch context."
fi
[ "$(dr_state)" = "false" ] \
    && pass "SIGINT in the drain window restores dispatch on the way out (Ctrl-C cannot strand the fleet either)" \
    || fail "SIGINT left the live daemon at draining=$(dr_state) — an aborted deploy strands the fleet exactly like a failed build"

# Leave the daemon dispatching for anything downstream. Deliberately NOT
# pogo_sandbox_curl: nothing below asserts on it, so it stages nothing, and a
# failure here after the verdict has been reached would replace a real FAIL with
# exit 99. Guarding the scaffolding is not guarding everything that looks like it.
curl -sf -X POST "$URL/agents/drain" -H 'Content-Type: application/json' \
    -d '{"draining":false}' >/dev/null 2>&1

finish
