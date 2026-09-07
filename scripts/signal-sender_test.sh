#!/bin/bash
# =============================================================================
# TESTS FOR scripts/signal-sender.sh + scripts/signal-sender.c (mg-cbc3)
# =============================================================================
#
# THE PROPERTY THIS SUITE EXISTS FOR
#
# mg-3bd1 closed the gate-SIGTERM investigation with the sender unidentified and
# recorded the reason as unrecoverable on darwin. It is recoverable: si_pid in
# an SA_SIGINFO handler names the sending process, with no privilege. This suite
# is the control on that claim, and Test 3 is the one that carries it.
#
# WHY TEST 2 ALONE WOULD BE WORTHLESS
#
# In the natural arrangement the process that sends the signal IS the wrapper's
# parent, so a program that simply printed getppid() would pass. Test 3
# therefore signals from a process that is NOT the wrapper's parent and NOT the
# wrapper itself, and demands that pid — it is the only test here that can tell
# a real reading from a plausible-looking substitute.
#
# THE CONTROL THAT MUST NOT BE DROPPED (Test 6)
#
# A child that runs `exit 143` on purpose is indistinguishable from a killed one
# by exit status alone, and mg-3bd1's whole point was that manufacturing a kill
# out of a status is the same error as missing one. This wrapper reports ONLY
# what was delivered to it in a handler, so Test 6 requires SILENCE on a chosen
# 143 — a suite that only ever kills things cannot tell a measurement from a
# hardcoded sentence.
#
# Test 5 is the silence control for the clean path, and Test 8 is the control on
# the degradation: an instrument that cannot build must leave the gate running
# and must SAY it is not watching.
# =============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HERE="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SENDER="$REPO_ROOT/scripts/signal-sender.sh"
SENDER_C="$REPO_ROOT/scripts/signal-sender.c"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create signalsender
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== signal-sender tests ==="

WORK="$POGO_SANDBOX_DIR/work"
mkdir -p "$WORK"
export POGO_SIGNAL_WITNESS_DIR="$WORK/witness"

# --- Test 1: both halves are well-formed ------------------------------------
echo ""
echo "Test 1: Syntax check, and the C compiles with warnings on"
if bash -n "$SENDER" 2>/dev/null; then
    pass "signal-sender.sh has valid bash syntax"
else
    fail "signal-sender.sh has syntax errors"
fi
CC_BIN="${CC:-cc}"
if ! command -v "$CC_BIN" >/dev/null 2>&1; then
    # Not a failure: this suite must be runnable on a box with no compiler,
    # which is exactly the case the degradation path exists for.
    pass "no C compiler on this host — the compile check is skipped, and Test 8 covers that path"
    NO_CC=1
else
    NO_CC=0
    CC_OUT="$WORK/cc.out"
    if "$CC_BIN" -O1 -Wall -Wextra -o "$WORK/probe-build" "$SENDER_C" >"$CC_OUT" 2>&1; then
        if [ -s "$CC_OUT" ]; then
            fail "signal-sender.c compiles but emits diagnostics: $(head -3 "$CC_OUT" | tr '\n' ' ')"
        else
            pass "signal-sender.c compiles clean under -Wall -Wextra"
        fi
    else
        fail "signal-sender.c does not compile: $(head -5 "$CC_OUT" | tr '\n' ' ')"
    fi
fi

# --- Test 2: it is transparent on the happy path ----------------------------
echo ""
echo "Test 2: The wrapped command's status and stdout pass through unchanged"
OUT="$WORK/passthrough.out"
set +e
bash "$SENDER" sh -c 'echo hello-from-child; exit 5' >"$OUT" 2>"$WORK/passthrough.err"
RC=$?
set -e
[ "$RC" -eq 5 ] \
    && pass "exit status 5 passed through" \
    || fail "exit status was $RC, want 5"
grep -q 'hello-from-child' "$OUT" \
    && pass "the child's stdout reached the caller" \
    || fail "the child's stdout was swallowed"

# --- Test 3: THE control — si_pid names the ACTUAL sender -------------------
# The sender is a subshell that is neither this shell, nor the wrapper, nor the
# wrapper's parent. A wrapper that printed getppid(), or its own pid, or the
# pid of the shell that launched the suite, fails here and passes everywhere
# else.
echo ""
echo "Test 3: the recorded sender is the pid that really sent the signal, not the wrapper's parent"
if [ "$NO_CC" -eq 1 ]; then
    pass "skipped: no C compiler on this host (Test 8 covers the degraded path)"
else
    ERR3="$WORK/sender.err"
    set +e
    bash "$SENDER" sleep 30 2>"$ERR3" &
    WRAPPER_SHELL=$!
    # Wait for the wrapper to be the process actually running: the front-end
    # execs into the binary, so $WRAPPER_SHELL is the wrapper itself.
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
        kill -0 "$WRAPPER_SHELL" 2>/dev/null || break
        pgrep -P "$WRAPPER_SHELL" >/dev/null 2>&1 && break
        sleep 0.2
    done
    # The sender: a subshell whose pid is captured from inside itself, so the
    # expected value is read from the sending process rather than assumed.
    SENDER_PID_FILE="$WORK/senderpid"
    ( sh -c 'echo $PPID' > "$SENDER_PID_FILE"; kill -TERM "$WRAPPER_SHELL" )
    wait "$WRAPPER_SHELL"
    RC3=$?
    set -e
    EXPECT_PID="$(cat "$SENDER_PID_FILE" 2>/dev/null || echo unknown)"
    if grep -q "SENDER pid=${EXPECT_PID} " "$ERR3"; then
        pass "si_pid = ${EXPECT_PID}, the pid that sent the signal"
    else
        fail "the report does not name the sending pid ${EXPECT_PID}: $(grep -m1 'SENDER pid=' "$ERR3" || echo '(no SENDER line at all)')"
    fi
    if grep -q "SENDER pid=${EXPECT_PID} " "$ERR3" && ! grep -q "SENDER pid=$$ " "$ERR3"; then
        pass "and it is NOT this suite's own shell ($$), which a getppid() substitute would have printed"
    else
        fail "the reported sender is indistinguishable from the wrapper's ancestry"
    fi
    [ "$RC3" -eq 143 ] \
        && pass "the wrapper died OF the signal, so its wait status still says 143" \
        || fail "the wrapper exited $RC3, not 143 — a wrapper that converts a kill to an ordinary exit destroys the evidence"
fi

# --- Test 4: the signal is forwarded to the wrapped command ------------------
echo ""
echo "Test 4: a caught signal reaches the wrapped command too"
if [ "$NO_CC" -eq 1 ]; then
    pass "skipped: no C compiler on this host"
else
    OUT4="$WORK/forward.out"
    # The child waits on a BACKGROUND sleep, not a foreground one, and the file
    # exists so the trap body can be single-quoted. Both are load-bearing: a
    # shell runs a trap only once its FOREGROUND command returns, so the
    # obvious `trap ...; sleep 30` version does catch the signal and then sits
    # for the full 30 seconds before acting on it. This test passed that way
    # and cost the gate 30s of the 33s it took (measured, rank 5 of 33).
    cat > "$WORK/forward-child.sh" <<'CHILD'
trap 'echo CHILD_GOT_TERM; kill $P 2>/dev/null; exit 143' TERM
sleep 30 &
P=$!
wait $P
CHILD
    set +e
    bash "$SENDER" sh "$WORK/forward-child.sh" >"$OUT4" 2>/dev/null &
    W4=$!
    sleep 1
    kill -TERM "$W4"
    wait "$W4" >/dev/null 2>&1
    set -e
    grep -q 'CHILD_GOT_TERM' "$OUT4" \
        && pass "the child saw the signal — the wrapper is transparent under a kill, not a shield" \
        || fail "the child never saw the signal; the wrapper absorbed it and changed what the step does"
fi

# --- Test 5: silence on a clean run -----------------------------------------
echo ""
echo "Test 5: nothing is reported when nothing is signalled"
ERR5="$WORK/clean.err"
set +e
bash "$SENDER" true >/dev/null 2>"$ERR5"
set -e
grep -q 'SIGNAL SENDER' "$ERR5" \
    && fail "the wrapper reported on a run that was never signalled" \
    || pass "a clean run says nothing"

# --- Test 6: a CHOSEN 143 must not be reported as a kill --------------------
echo ""
echo "Test 6: a command that exits 143 on purpose produces NO sender report"
ERR6="$WORK/chosen143.err"
set +e
bash "$SENDER" sh -c 'exit 143' >/dev/null 2>"$ERR6"
RC6=$?
set -e
[ "$RC6" -eq 143 ] \
    && pass "the chosen 143 passed through unchanged" \
    || fail "status was $RC6, want 143"
grep -q 'SIGNAL SENDER' "$ERR6" \
    && fail "a chosen exit 143 was reported as a delivered signal — the false fact this instrument exists to avoid" \
    || pass "and it is NOT reported as a signal: this wrapper reports only what a handler caught"

# --- Test 7: the record outlives the run, in a file --------------------------
echo ""
echo "Test 7: the sender is also written to a file under the witness dir"
if [ "$NO_CC" -eq 1 ]; then
    pass "skipped: no C compiler on this host"
else
    FILES="$(ls "$POGO_SIGNAL_WITNESS_DIR"/*.sender.txt 2>/dev/null | wc -l | tr -d ' ')"
    if [ "${FILES:-0}" -ge 1 ]; then
        pass "$FILES sender record(s) written under $POGO_SIGNAL_WITNESS_DIR"
        if grep -qh 'SENDER pid=' "$POGO_SIGNAL_WITNESS_DIR"/*.sender.txt 2>/dev/null; then
            pass "and the file carries the sending pid, not just the fact of a signal"
        else
            fail "the record file does not name a sender"
        fi
    else
        fail "no record file was written; the gate's worktree is deleted when the MR resolves and stderr goes with it"
    fi
fi

# --- Test 8: it degrades LOUDLY and does not take the gate with it ----------
echo ""
echo "Test 8: with no usable compiler the command still runs, and the silence is announced"
ERR8="$WORK/degrade.err"
set +e
CC=/nonexistent/definitely-not-a-compiler POGO_SIGNAL_WITNESS_DIR="$WORK/witness-degraded" \
    bash "$SENDER" sh -c 'exit 9' >/dev/null 2>"$ERR8"
RC8=$?
set -e
[ "$RC8" -eq 9 ] \
    && pass "the wrapped command ran and its status survived a failed build" \
    || fail "a missing compiler changed the step's exit status to $RC8 — the instrument took the gate with it"
grep -q 'UNWITNESSED' "$ERR8" \
    && pass "and the degradation is announced: an instrument that is off quietly has been off for months by the time anyone asks" \
    || fail "the wrapper degraded SILENTLY"

# --- Test 9: the disable is announced too -----------------------------------
echo ""
echo "Test 9: POGO_SIGNAL_SENDER=0 is announced"
ERR9="$WORK/disabled.err"
set +e
POGO_SIGNAL_SENDER=0 bash "$SENDER" sh -c 'exit 4' >/dev/null 2>"$ERR9"
RC9=$?
set -e
[ "$RC9" -eq 4 ] && grep -q 'DISABLED by POGO_SIGNAL_SENDER=0' "$ERR9" \
    && pass "a disabled wrapper runs the command and says it is disabled" \
    || fail "the disable is silent, or changed the status ($RC9)"

# --- Test 10: the cached binary is keyed on the SOURCE ----------------------
# A cache that is not keyed on content serves a binary built from an older .c
# forever, which is the shape that makes an instrument quietly stop measuring
# what its source says it measures.
echo ""
echo "Test 10: editing signal-sender.c changes the cached binary's name"
if [ "$NO_CC" -eq 1 ]; then
    pass "skipped: no C compiler on this host"
else
    COPY="$WORK/copy"
    mkdir -p "$COPY"
    cp "$SENDER" "$SENDER_C" "$COPY/"
    set +e
    POGO_SIGNAL_WITNESS_DIR="$WORK/w10" bash "$COPY/signal-sender.sh" true >/dev/null 2>&1
    BEFORE="$(ls "$WORK/w10/bin" 2>/dev/null | head -1)"
    printf '\n/* cache-key control */\n' >> "$COPY/signal-sender.c"
    POGO_SIGNAL_WITNESS_DIR="$WORK/w10" bash "$COPY/signal-sender.sh" true >/dev/null 2>&1
    AFTER="$(ls "$WORK/w10/bin" 2>/dev/null | wc -l | tr -d ' ')"
    set -e
    if [ -n "$BEFORE" ] && [ "${AFTER:-0}" -eq 2 ]; then
        pass "a changed source produced a second cache entry rather than reusing $BEFORE"
    else
        fail "the cache is not keyed on the source (before=${BEFORE:-none}, entries after edit=${AFTER:-0})"
    fi
fi

# --- Test 11: it is WIRED, and wired INSIDE the guard -----------------------
# An instrument nobody runs is the same defect as no instrument. The position
# matters and is the whole reason this is a separate file from the witness: the
# three recorded kills reached tmpdir-leak-guard.sh and everything below it, and
# NOT the guard's ancestors — so a recorder above the guard would have caught
# nothing on any of the three.
echo ""
echo "Test 11: the Go-test row runs the sender INSIDE the tmpdir guard"
TEST_SH="$REPO_ROOT/test.sh"
ROW="$(grep -E '^gate_step "Testing Go packages"' "$TEST_SH" || true)"
if [ -z "$ROW" ]; then
    fail "test.sh has no 'Testing Go packages' row to check"
else
    case "$ROW" in
        *tmpdir-leak-guard.sh*signal-sender.sh*go-test-budget.sh*)
            pass "the row is guard -> signal-sender -> budget, which is inside the signalled region" ;;
        *signal-sender.sh*)
            fail "signal-sender.sh is on the row but not between the guard and the budget shell: $ROW" ;;
        *)
            fail "the Go-test row does not run signal-sender.sh at all: $ROW" ;;
    esac
fi

# --- Test 12: the state root is normalised the way config.PogoHome does -----
# Same property, and the same reason, as scripts/signal-witness_test.sh's Test
# 9: on this box ~/.zshrc still exports the LEGACY POGO_HOME=$HOME, so
# "${POGO_HOME:-$HOME/.pogo}" names $HOME while config.PogoHome names
# $HOME/.pogo. The legacy row is the one that discriminates — the unset and
# explicit rows pass against the broken expression too.
echo ""
echo "Test 12: the record lands under the pogo state root, normalised as config.PogoHome does"
grep -q '^resolve_pogo_home()' "$SENDER" \
    && pass "signal-sender.sh carries resolve_pogo_home, the spelling the tree-wide guard sanctions" \
    || fail "resolve_pogo_home is not defined in signal-sender.sh"

# bin_dir_for HOME POGO_HOME_OR_EMPTY — where the cached binary lands, read out
# of the filesystem rather than out of the script's text.
bin_dir_for() {
    local fake_home="$1" ph="$2" out
    mkdir -p "$fake_home"
    if [ -n "$ph" ]; then
        out="$(env HOME="$fake_home" POGO_HOME="$ph" POGO_SIGNAL_WITNESS_DIR= \
            bash "$SENDER" true 2>&1 >/dev/null || true)"
    else
        out="$(env HOME="$fake_home" -u POGO_HOME POGO_SIGNAL_WITNESS_DIR= \
            bash "$SENDER" true 2>&1 >/dev/null || true)"
    fi
    find "$fake_home" -type d -name bin 2>/dev/null | head -1
}
LEGACY_HOME="$WORK/legacyhome"
GOT="$(bin_dir_for "$LEGACY_HOME" "$LEGACY_HOME")"
if [ "$GOT" = "$LEGACY_HOME/.pogo/signal-witness/bin" ]; then
    pass "legacy POGO_HOME=\$HOME resolves to \$HOME/.pogo/signal-witness, as config.PogoHome does"
elif [ -z "$GOT" ]; then
    pass "no compiler on this host, so nothing was cached — the resolver text check above stands alone"
else
    fail "legacy POGO_HOME=\$HOME put the cache in $GOT, not $LEGACY_HOME/.pogo/signal-witness/bin"
fi

# --- Test 13: the stderr block cannot evict the failure text ----------------
# A remedy is an artifact of the same kind as the defect. The refinery persists
# 8 KB of gate output head+tail, and an agent's argv on this box runs past a
# kilobyte — so nine resolved ancestors printed in full would push the failure
# text out of the record this block is attached to, which is precisely the
# defect mg-3bd1 avoided by writing its process table to a file. The stderr copy
# is therefore capped per line and the record file is not.
echo ""
echo "Test 13: long argv is truncated on stderr and kept in full in the record file"
if [ "$NO_CC" -eq 1 ]; then
    pass "skipped: no C compiler on this host"
else
    PAD="PADDING$(printf 'x%.0s' $(seq 1 600))END"
    ERR13="$WORK/wide.err"
    W13DIR="$WORK/witness-wide"
    set +e
    POGO_SIGNAL_WITNESS_DIR="$W13DIR" bash "$SENDER" sleep 30 2>"$ERR13" &
    W13=$!
    sleep 1
    # The sender STAYS ALIVE for a few seconds after signalling, deliberately:
    # if it exits first, `ps` resolves nothing and the truncation path this test
    # exists for is never executed — the test would pass while checking nothing.
    sh -c ": $PAD; kill -TERM $W13; sleep 5" &
    SENDER13=$!
    wait "$W13" >/dev/null 2>&1
    set -e
    LONGEST="$(awk '{ if (length($0) > m) m = length($0) } END { print m + 0 }' "$ERR13")"
    if [ "${LONGEST:-0}" -le 300 ]; then
        pass "no stderr line exceeds 300 columns (longest ${LONGEST})"
    else
        fail "an stderr line is ${LONGEST} columns; nine of those displace the failure text under the 8 KB cap"
    fi
    if grep -q 'more chars; full line in the record file' "$ERR13"; then
        pass "the truncation is announced rather than silent"
    else
        fail "the sender's ${#PAD}-char argv was not truncated on stderr; the cap did not run"
    fi
    if grep -qh "$PAD" "$W13DIR"/*.sender.txt 2>/dev/null; then
        pass "and the record file carries the argv in full — the cap is on the stderr copy only"
    else
        fail "the record file lost the full argv, so the truncation is a loss rather than a split"
    fi
    kill "$SENDER13" 2>/dev/null || true
    wait "$SENDER13" 2>/dev/null || true
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
