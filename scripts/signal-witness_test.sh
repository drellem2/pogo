#!/bin/bash
# =============================================================================
# TESTS FOR scripts/signal-witness.sh (mg-3bd1)
# =============================================================================
#
# THE PROPERTY THIS SUITE EXISTS FOR
#
# The witness's whole job is to separate two states that produced IDENTICAL
# records on this host three times over: a signal delivered to the gate's
# process group, and a signal delivered to a subtree below it. Its verdict is
# the ancestor reading, so the load-bearing controls are the two arms of that
# reading — and neither is worth anything without the other, because a witness
# that printed "EVERY ancestor survived" unconditionally would pass any suite
# that only ever killed a subtree.
#
# Test 4 is therefore the NEGATIVE-SHAPE control for Test 3: the same fixture,
# the same assertion instrument, an ancestor deliberately killed, and the
# opposite verdict demanded.
#
# THE OTHER CONTROL THAT MUST NOT BE DROPPED (Test 5)
#
# `$?` is 128+N for a child killed by signal N AND for a child that ran
# `exit $((128+N))` on purpose. A wrapper that called the second one a kill
# would be manufacturing exactly the false fact this whole investigation exists
# to stop. Test 5 runs a command that CHOOSES status 143 and requires the
# report to say AMBIGUOUS — a suite that only ever kills things cannot tell a
# correct witness from one that hardcodes the word "killed".
#
# Test 6 is the silence control. A witness that speaks on a clean run is one
# whose reports nobody reads, and every assertion above would still pass.
# =============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HERE="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WITNESS="$REPO_ROOT/scripts/signal-witness.sh"

# Route through the shared test isolation (mg-457b) before anything that can
# reach $HOME. This suite shells out to `ps` against the live host — that is the
# instrument under test — but everything it WRITES belongs in the sandbox.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create signalwitness
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== signal-witness.sh tests ==="

WORK="$POGO_SANDBOX_DIR/work"
mkdir -p "$WORK"

# --- Test 1: valid shell ----------------------------------------------------
echo ""
echo "Test 1: Syntax check"
if bash -n "$WITNESS" 2>/dev/null; then
    pass "signal-witness.sh has valid bash syntax"
else
    fail "signal-witness.sh has syntax errors"
fi

# --- Test 2: it is transparent on the happy path ----------------------------
echo ""
echo "Test 2: The wrapped command's status and output pass through unchanged"
OUT="$WORK/passthrough.out"
set +e
bash "$WITNESS" bash -c 'echo hello-from-wrapped; exit 7' > "$OUT" 2>&1
RC=$?
set -e
[ "$RC" -eq 7 ] \
    && pass "the wrapped command's exit status (7) is returned unchanged" \
    || fail "expected status 7, got $RC"
grep -q 'hello-from-wrapped' "$OUT" \
    && pass "the wrapped command's stdout reaches the caller" \
    || fail "the wrapped command's output was swallowed"

# --- The fixture ------------------------------------------------------------
# A stand-in for the gate's nesting: an OUTER shell (the ancestor whose survival
# is the verdict) runs the witness, which runs an INNER shell, which sleeps.
# Every pid is written to a file so the controls can address processes BY PID —
# an unanchored pkill on this box takes the fleet's own pollers with it, which
# idle in exactly this command (mg-c675).
cat > "$WORK/outer.sh" <<'OUTER'
#!/bin/bash
set -u
D="$1"; WITNESS="$2"
echo $$ > "$D/outer.pid"
bash "$WITNESS" bash "$D/inner.sh" "$D"
echo "$?" > "$D/witness.status"
OUTER
cat > "$WORK/inner.sh" <<'INNER'
#!/bin/bash
set -u
D="$1"
echo $$ > "$D/inner.pid"
sleep 120 &
echo $! > "$D/leaf.pid"
wait
INNER

# witness_case NAME KILL_ANCESTOR — run the fixture, signal the witness's whole
# SUBTREE the way the recorded incidents did (leaf, inner shell, witness) and,
# when asked, the ancestor above it too. Signalling the subtree rather than the
# witness alone is both faithful and necessary: bash defers a trap until the
# foreground command returns, so a witness whose child is left running does not
# report until that child ends — which in the fixture is 120 seconds away and in
# the gate would be the rest of the suite.
#
# Every process is addressed BY PID. An unanchored pkill on this box takes the
# fleet's own pollers with it, which idle in exactly this command (mg-c675).
CASE_OUT=""
witness_case() {
    local name="$1" kill_ancestor="$2" d i wpid
    d="$WORK/case-$name"
    rm -rf "$d"; mkdir -p "$d"
    cp "$WORK/inner.sh" "$d/inner.sh"
    CASE_OUT="$d/out"
    ( bash "$WORK/outer.sh" "$d" "$WITNESS" > "$CASE_OUT" 2>&1 & )
    # Settle on the fixture being fully up, not on a fixed sleep: a fixed wait is
    # a bet on host load, and this suite runs on the merge gate, which is the
    # thing generating the load.
    for i in $(seq 1 80); do
        [ -s "$d/inner.pid" ] && [ -s "$d/leaf.pid" ] && break
        sleep 0.25
    done
    [ -s "$d/inner.pid" ] || { fail "$name: fixture never came up"; return 1; }
    # The witness is the inner shell's parent.
    wpid="$(ps -o ppid= -p "$(cat "$d/inner.pid")" 2>/dev/null | tr -d ' ')"
    case "$wpid" in '' | *[!0-9]*) fail "$name: could not find the witness pid"; return 1 ;; esac
    if [ "$kill_ancestor" = "1" ]; then
        kill -TERM "$(cat "$d/outer.pid")" 2>/dev/null
    fi
    kill -TERM "$(cat "$d/leaf.pid")" "$(cat "$d/inner.pid")" "$wpid" 2>/dev/null
    for i in $(seq 1 40); do
        kill -0 "$wpid" 2>/dev/null || break
        sleep 0.1
    done
    # The report is written by the trap as the witness dies; give the write a
    # bounded chance to land rather than a fixed pause.
    for i in $(seq 1 40); do
        grep -q '=====$' "$CASE_OUT" 2>/dev/null && break
        sleep 0.1
    done
    for i in inner leaf outer; do
        [ -s "$d/$i.pid" ] && kill -9 "$(cat "$d/$i.pid")" 2>/dev/null
    done
    return 0
}

# --- Test 3: a signal that spares the ancestors is REPORTED as sparing them --
echo ""
echo "Test 3: A subtree-only signal — every ancestor survives, and the report says so"
if witness_case subtree 0; then
    grep -q 'SIGNAL WITNESS' "$CASE_OUT" \
        && pass "the witness printed a report" \
        || fail "no report was printed: $(head -5 "$CASE_OUT")"
    grep -q 'OBSERVED' "$CASE_OUT" \
        && pass "the report is labelled OBSERVED — the signal was caught in a trap, not inferred" \
        || fail "the report is not labelled OBSERVED: $(grep -m1 'SIGNAL WITNESS' "$CASE_OUT")"
    # Both arms must SPELL the signal the same way or a search across past
    # occurrences finds one arm and not the other — mg-0502's point about
    # `signal: terminated` applied to this file's own output.
    grep -q 'SIGNAL WITNESS.*SIGTERM' "$CASE_OUT" \
        && pass "and it names the signal as SIGTERM, the same spelling the AMBIGUOUS arm uses" \
        || fail "the OBSERVED headline does not say SIGTERM: $(grep -m1 'SIGNAL WITNESS' "$CASE_OUT")"
    grep -q 'EVERY ancestor survived' "$CASE_OUT" \
        && pass "the delivery shape is read as NOT a process-group kill" \
        || fail "the ancestor verdict is missing or wrong: $(grep -c 'ALIVE' "$CASE_OUT") ALIVE lines"
    grep -q 'PROCESS TABLE at the instant of the signal' "$CASE_OUT" \
        && pass "the report carries a process-table section" \
        || fail "no process table in the report"
    # The snapshot is the deliverable, and the assertion is on the FILE, not on
    # the sentence claiming there is one. It must also be outside the gate's
    # worktree — that directory is removed when the MR resolves, and a record
    # that dies with the worktree is the record we did not have three times.
    SNAP="$(sed -n 's/^ *\(.*\.ps\.txt\) *$/\1/p' "$CASE_OUT" | head -1)"
    if [ -n "$SNAP" ] && [ -s "$SNAP" ]; then
        pass "the report names a snapshot file that exists and is non-empty ($(wc -l < "$SNAP" | tr -d ' ') processes)"
    else
        fail "no readable snapshot file was named in the report (parsed: '$SNAP')"
    fi
    case "$SNAP" in
        "$POGO_SANDBOX_DIR"/*) pass "and it was written under the run's own POGO_HOME, not into the worktree" ;;
        *) fail "the snapshot landed outside the sandbox: $SNAP" ;;
    esac
fi

# --- Test 4: THE OPPOSITE ARM ------------------------------------------------
# Without this, Test 3 passes against a witness that prints "EVERY ancestor
# survived" with no reading behind it at all.
echo ""
echo "Test 4: An ancestor killed too — the same report reaches the OPPOSITE verdict"
if witness_case group 1; then
    if grep -q 'GONE ' "$CASE_OUT"; then
        pass "a dead ancestor is reported GONE, so the ancestor reading is a live measurement"
    else
        fail "the killed ancestor was still reported alive — the reading is not measuring anything"
    fi
    if grep -q 'EVERY ancestor survived' "$CASE_OUT"; then
        fail "the report claimed every ancestor survived while one was dead"
    else
        pass "and the not-a-group-kill conclusion is withheld when an ancestor is gone"
    fi
fi

# --- Test 5: a CHOSEN 143 must not be reported as a kill --------------------
echo ""
echo "Test 5: A command that EXITS 143 of its own accord is reported AMBIGUOUS, not killed"
OUT5="$WORK/chosen143.out"
set +e
bash "$WITNESS" bash -c 'exit 143' > "$OUT5" 2>&1
RC5=$?
set -e
[ "$RC5" -eq 143 ] \
    && pass "the chosen status is passed through unchanged (143)" \
    || fail "expected 143, got $RC5"
grep -q 'AMBIGUOUS' "$OUT5" \
    && pass "the report is labelled AMBIGUOUS — a shell cannot separate this from a kill" \
    || fail "a chosen 143 was not labelled AMBIGUOUS: $(grep -m1 'SIGNAL WITNESS' "$OUT5")"
grep -q 'OBSERVED' "$OUT5" \
    && fail "a chosen 143 was reported as OBSERVED — the witness is asserting a kill it did not see" \
    || pass "and it is NOT labelled OBSERVED"

# --- Test 6: silence on the happy path --------------------------------------
echo ""
echo "Test 6: Nothing is printed for a run that was not signalled"
OUT6="$WORK/quiet.out"
set +e
bash "$WITNESS" bash -c 'exit 1' > "$OUT6" 2>&1
RC6=$?
set -e
[ "$RC6" -eq 1 ] \
    && pass "an ordinary failure passes through as status 1" \
    || fail "expected 1, got $RC6"
grep -q 'SIGNAL WITNESS' "$OUT6" \
    && fail "the witness spoke on an ordinary red run — its reports will be skipped" \
    || pass "the witness is silent on an ordinary failure"

# --- Test 7: the disable switch announces itself ----------------------------
echo ""
echo "Test 7: POGO_SIGNAL_WITNESS=0 disables it LOUDLY"
OUT7="$WORK/disabled.out"
set +e
POGO_SIGNAL_WITNESS=0 bash "$WITNESS" bash -c 'exit 143' > "$OUT7" 2>&1
RC7=$?
set -e
[ "$RC7" -eq 143 ] \
    && pass "the wrapped status still passes through when disabled" \
    || fail "expected 143, got $RC7"
grep -q 'DISABLED by POGO_SIGNAL_WITNESS=0' "$OUT7" \
    && pass "the disable is announced on stderr" \
    || fail "the witness can be disabled silently"
grep -q 'SIGNAL WITNESS' "$OUT7" \
    && fail "a disabled witness still reported" \
    || pass "and a disabled witness records nothing"

# --- Test 8: it is actually wired into the gate -----------------------------
# A witness nobody runs is the same defect as no witness. The three occurrences
# this file exists for were all in the Go-test step, so that is the row checked.
echo ""
echo "Test 8: The witness is wired into test.sh's Go-test step"
TEST_SH="$REPO_ROOT/test.sh"
if grep -q 'signal-witness.sh' "$TEST_SH"; then
    pass "test.sh invokes scripts/signal-witness.sh"
else
    fail "test.sh does not run the witness — nothing will be recorded on the next occurrence"
fi
if grep -E '^gate_step "Testing Go packages"' "$TEST_SH" | grep -q 'signal-witness.sh'; then
    pass "and it wraps the Go-test row, which is where all three recorded kills landed"
else
    fail "the Go-test row does not go through the witness: $(grep -E '^gate_step "Testing Go packages"' "$TEST_SH")"
fi

# --- Test 9: the state root is normalised the way config.PogoHome does ------
# Required by internal/config's tree-wide guard (TestNoShellFileReDerivesThe-
# PogoStateRoot), which caught this file's first draft writing to
# "${POGO_HOME:-$HOME/.pogo}". That expression is not a normalisation: on this
# box ~/.zshrc still exports the LEGACY POGO_HOME=$HOME, so it names $HOME while
# config.PogoHome names $HOME/.pogo — and the snapshot would have landed beside
# a different tree from the one anyone would ever look in. For a file whose only
# job is to leave a record somebody finds later, that is the whole defect.
#
# Asserted on BEHAVIOUR — where the snapshot actually lands — rather than on the
# text of the resolver, and in the same shape scripts/fleet-liveness-probe_test.sh
# uses for the same property. THE LEGACY ROW IS THE ONE THAT DISCRIMINATES: the
# unset and explicit rows pass against the broken expression too, so all three
# run together and none of them is worth having alone.
echo ""
echo "Test 9: the snapshot lands under the pogo state root, normalised as config.PogoHome does"
grep -q '^resolve_pogo_home()' "$WITNESS" \
    && pass "signal-witness.sh carries resolve_pogo_home, the spelling the guard sanctions" \
    || fail "resolve_pogo_home is not defined in signal-witness.sh"

# snap_dir_for HOME POGO_HOME_OR_EMPTY — where a signalled run puts its snapshot.
# Read out of the report rather than by listing directories: the path the report
# NAMES is what a reader will follow, so that is the thing under test.
snap_dir_for() {
    local h="$1" ph="$2" out
    mkdir -p "$h"
    if [ -z "$ph" ]; then
        out="$(env -u POGO_HOME -u POGO_SIGNAL_WITNESS_DIR HOME="$h" bash "$WITNESS" bash -c 'exit 143' 2>&1)"
    else
        out="$(env -u POGO_SIGNAL_WITNESS_DIR HOME="$h" POGO_HOME="$ph" bash "$WITNESS" bash -c 'exit 143' 2>&1)"
    fi
    printf '%s\n' "$out" | sed -n 's|^ *\(/.*\)/[^/]*\.ps\.txt *$|\1|p' | head -1
}

FAKE="$WORK/pogohome"
UNSET_DIR="$(snap_dir_for "$FAKE/a" "")"
LEGACY_DIR="$(snap_dir_for "$FAKE/b" "$FAKE/b")"
EXPLICIT_DIR="$(snap_dir_for "$FAKE/c" "$FAKE/c/custom")"

[ "$UNSET_DIR" = "$FAKE/a/.pogo/signal-witness" ] \
    && pass "POGO_HOME unset -> \$HOME/.pogo/signal-witness" \
    || fail "POGO_HOME unset gave '$UNSET_DIR', wanted '$FAKE/a/.pogo/signal-witness'"
[ "$LEGACY_DIR" = "$FAKE/b/.pogo/signal-witness" ] \
    && pass "the LEGACY POGO_HOME=\$HOME is normalised to \$HOME/.pogo, as config.PogoHome does — not left as \$HOME" \
    || fail "POGO_HOME=\$HOME gave '$LEGACY_DIR', wanted '$FAKE/b/.pogo/signal-witness' (this is the row that catches the broken expression)"
[ "$EXPLICIT_DIR" = "$FAKE/c/custom/signal-witness" ] \
    && pass "an explicit POGO_HOME is honoured unchanged" \
    || fail "POGO_HOME=$FAKE/c/custom gave '$EXPLICIT_DIR'"

echo ""
echo "=== Results: ${PASS} passed, ${FAIL} failed ==="
[ "$FAIL" -eq 0 ]
