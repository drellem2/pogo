#!/bin/bash
# =============================================================================
# TESTS FOR scripts/lib/gate-profile.sh — the merge gate's per-step profile
# =============================================================================
#
# THE PROPERTY THIS SUITE EXISTS FOR (mg-eed9):
#   The profile's whole value is the `cores` column — CPU seconds over wall
#   seconds — because that is the only thing separating a step that is DOING
#   WORK from a step that is WAITING, and those two take opposite remedies. A
#   table of wall-clock numbers with a silently-zero CPU column looks exactly
#   like a gate in which nothing computes, which is the conclusion someone
#   would then act on.
#
#   That failure is one character away at all times. `t=$(times)` forks, and a
#   forked child's children-times start at zero, so the capture reads 0.000
#   forever — no error, no warning, every step reported as pure waiting.
#   Test 4 is the positive control against exactly that: a step that burns CPU
#   must report CPU, and a step that sleeps must not.
#
# WHY TEST 4's THRESHOLDS ARE ON CPU SECONDS AND NOT ON `cores`:
#   `cores` is cpu/wall, and wall grows with host contention while CPU does
#   not — so a floor on `cores` is a floor that passes on a quiet box and fails
#   under fleet load on a byte-identical binary. That is mg-6c90's disease
#   (13/13 FAIL at load 52-106, 4/4 PASS at load 4.6-5.3), and this suite runs
#   on the merge gate, which is the thing generating the load. The invariants
#   asserted here are therefore load-INDEPENDENT: a CPU burner accumulates its
#   CPU seconds no matter how long it is made to wait for them, and a `sleep`
#   accumulates none however busy the box is.
#
# THE RULE THAT FOLLOWS FROM THAT, WHICH TEST 3 BROKE (mg-db12):
#   Never compare a WALL-CLOCK quantity against a CPU quantity, in either
#   direction. Test 3 asserted that a 1s `sleep` outranks a fixed CPU burn, and
#   that is exactly such a comparison: load stretches one and leaves the other
#   alone, so the ordering held on an idle box and reversed at load 53. It failed
#   a real merge gate on an unrelated branch, and the gate classed it DEFECT — "a
#   fact about the branch" — rather than infrastructure, so the retry that would
#   have passed never ran, and the author was told to fix something that was not
#   theirs. The rewritten Test 3 compares wall against wall, and against the
#   profiler's own record of the same run, so both sides move together.
#
#   Wall against wall is fine; CPU against CPU is fine; a LOWER bound on wall
#   (Test 4's last check) is fine, because contention can only add to it. An
#   upper bound on wall, or any cross-unit ordering, is the thing that must not
#   be written in this file.
# =============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HERE="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LIB="$REPO_ROOT/scripts/lib/gate-profile.sh"

# Route through the shared test isolation before anything that can reach $HOME
# (mg-457b). This suite is short and looks self-evidently harmless — it runs
# `sleep`, `awk` and `true` in a temp dir — which is exactly the reasoning that
# put 48 suites in the adoption ledger, and it is wrong here for a specific
# reason: `gate_profile_load` shells out to `uptime`, `gate_profile_begin`
# mktemps under $TMPDIR, and Test 9 reads the repository's own test.sh and
# build.sh. More to the point, this file is the instrument sent to measure a
# problem whose stated root cause is 48 suites reading the developer's live
# state; shipping it unisolated would make it an instance of what it measures.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create gateprofile
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== gate-profile.sh tests ==="

# Inside the private root, so pogo_sandbox_down removes it — there is one EXIT
# trap and the sandbox owns it.
tmpdir="$POGO_SANDBOX_DIR/work"
mkdir -p "$tmpdir"

# --- Test 1: the library is valid shell -------------------------------------
echo ""
echo "Test 1: Library syntax check"
if bash -n "$LIB" 2>/dev/null; then
    pass "gate-profile.sh has valid bash syntax"
else
    fail "gate-profile.sh has syntax errors"
fi

# --- The driver ---------------------------------------------------------------
# A stand-in for test.sh: sources the real library, runs a sleeping step, a
# CPU-burning step and a trivially fast one, and is armed exactly as test.sh
# arms it. `awk` is the burner rather than python/perl so the control depends on
# nothing the gate does not already require.
#
# THE STEP ORDER IS LOAD-BEARING (mg-db12). QUICK STEP runs FIRST, and it is the
# only step whose wall-clock is bounded above — `true` cannot take 0.9s on any
# host, and SLEEPING STEP cannot take less. So insertion order is guaranteed NOT
# to be ranked order, on a quiet box and on a contended one alike, and Test 3's
# monotonicity check therefore has something to catch. Ordered the other way
# round, as it was until mg-db12, a library that had stopped sorting entirely
# would have emitted rows already in descending order and passed.
cat > "$tmpdir/driver.sh" <<DRIVER
#!/bin/bash
set -e
. "$LIB"
gate_profile_begin "driver"
trap 'gate_profile_report' EXIT
gate_step "QUICK STEP" true
gate_step "SLEEPING STEP" sleep 1
gate_step "BURNING STEP" awk 'BEGIN { for (i = 0; i < 6000000; i++) x += i }'
DRIVER

JSON="$tmpdir/profile.jsonl"
OUT="$tmpdir/driver.out"
set +e
POGO_GATE_PROFILE_JSON="$JSON" bash "$tmpdir/driver.sh" > "$OUT" 2>&1
DRIVER_RC=$?
set -e

# --- Test 2: the table is printed and every step is in it -------------------
echo ""
echo "Test 2: The profile table is printed and names every step"
if [ "$DRIVER_RC" -eq 0 ]; then
    pass "the driver exited 0"
else
    fail "the driver exited $DRIVER_RC; output: $(cat "$OUT")"
fi
if grep -q 'GATE STEP PROFILE' "$OUT"; then
    pass "the table header was printed"
else
    fail "no table header in the output"
fi
MISSING=""
for step in "SLEEPING STEP" "BURNING STEP" "QUICK STEP"; do
    grep -q "$step" "$OUT" || MISSING="$MISSING [$step]"
done
if [ -z "$MISSING" ]; then
    pass "all three steps appear in the output"
else
    fail "steps missing from the output:$MISSING"
fi

# --- Test 3: rows are ranked by wall-clock, slowest first -------------------
# The ranking IS the deliverable — a per-step breakdown that does not order
# itself hands the reader the same undifferentiated list they started with.
#
# THIS ASSERTION USED TO NAME A STEP, AND THAT WAS THE BUG (mg-db12). It read
# "rank 1 is SLEEPING STEP", which is an assertion that a fixed 1s SLEEP outlasts
# a fixed amount of CPU WORK. Those two are only ordered on an idle host: load
# stretches wall-clock and leaves CPU-seconds alone, so at load 129 the burner
# took 2.43s and took rank 1, and at load 53.84 it took 1.81s and did it again.
# The profiler was right both times; what broke was the test's belief about which
# step is slowest. The second of those failed a real merge and was classed
# DEFECT — "establishes a fact about the branch" — against a branch that had
# never touched this file, blaming its author and suppressing the retry that
# would have passed. Same shape as mg-6c90.
#
# So the check no longer names a step. It reads the table's OWN wall column and
# asserts the property the ranking claims — non-increasing down the ranks — and
# cross-checks every number against the JSON record of the same run. That holds
# at any load, because both sides of the comparison move together.
#
# It is also STRICTLY STRONGER, in two ways that are checked rather than
# asserted: it constrains all N rows instead of row 1 (a table sorted correctly
# except for its tail used to pass), and with the driver's steps reordered above
# it fails on a table that was never sorted at all — which the old form passed.
# Test 3b runs the checker against a hand-written mis-ordered table to prove it
# can return non-zero; a monotonicity check that cannot fail would be the same
# inert control this suite exists to prevent.
echo ""
echo "Test 3: Rows are ranked slowest-first"

# Emit "wall<TAB>step" for each ranked row, in the order the table prints them.
# The row format is `  %5d  %8.2fs  %6.1f%%  %8.2fs  %7.2f  %6s  %s%s` — the
# pattern is anchored on rank/wall/share/cpu so the header, the legend and the
# banner cannot match, and the step name is everything from field 7 on because
# names contain spaces.
gate_profile_rank_rows() {
    awk '
        /^ +[0-9]+ +[0-9.]+s +[0-9.]+% +[0-9.]+s / {
            wall = $2; sub(/s$/, "", wall)
            name = ""
            for (i = 7; i <= NF; i++) name = name (i > 7 ? " " : "") $i
            sub(/ *\[FAILED\]$/, "", name)
            printf "%s\t%s\n", wall, name
        }' "$1"
}

# Non-increasing wall down the ranks, over at least one row. Exit status IS the
# verdict, so this can be pointed at a table known to be wrong.
gate_profile_ranking_ok() {
    gate_profile_rank_rows "$1" | awk -F "\t" '
        { cur = $1 + 0
          if (NR > 1 && cur > prev) { bad = 1; exit 1 }
          prev = cur; n++ }
        END { if (n == 0 || bad) exit 1 }'
}

ROWS="$(gate_profile_rank_rows "$OUT")"
ROW_COUNT="$(printf '%s\n' "$ROWS" | grep -c . || true)"
if [ "$ROW_COUNT" -eq 3 ]; then
    pass "all 3 steps are ranked rows in the table"
else
    fail "parsed $ROW_COUNT ranked rows, want 3; the table format changed and this test is reading nothing: $(cat "$OUT")"
fi

if gate_profile_ranking_ok "$OUT"; then
    pass "wall-clock is non-increasing down the ranks (rows: $(printf '%s' "$ROWS" | tr '\n' '|'))"
else
    fail "the rows are not ordered by wall-clock; rows were: $(printf '%s' "$ROWS" | tr '\n' '|')"
fi

# The ordering is only meaningful if the numbers being ordered are the ones that
# were measured. A table that sorted a wall column it had mangled would satisfy
# monotonicity and mislead every reader of it.
MISMATCH=""
while IFS="$(printf '\t')" read -r wall name; do
    [ -n "$name" ] || continue
    want="$(jq -r --arg s "$name" '.steps[] | select(.step == $s) | .wall_seconds' "$JSON")"
    # Compared numerically, not as strings: jq canonicalises number literals
    # differently across versions (1.6 prints 1.00 as 1, 1.7 preserves it), and
    # a test that failed on the reader's jq build would be its own flake.
    if [ -z "$want" ]; then
        MISMATCH="$MISMATCH [$name: absent from the JSON record]"
    elif ! awk -v a="$wall" -v b="$want" 'BEGIN { d = a - b; if (d < 0) d = -d; exit !(d < 0.005) }'; then
        MISMATCH="$MISMATCH [$name: table ${wall}s, record ${want}s]"
    fi
done <<ROWS_EOF
$ROWS
ROWS_EOF
if [ -z "$MISMATCH" ]; then
    pass "every ranked row's wall matches the JSON record of the same run"
else
    fail "the table's wall column disagrees with the measurement:$MISMATCH"
fi

# --- Test 3b: the ranking check can FAIL ------------------------------------
# The negative control for Test 3. Rows in ascending wall order, formatted
# exactly as the library formats them, must be rejected.
echo ""
echo "Test 3b: NEGATIVE CONTROL — the ranking check rejects an unordered table"
cat > "$tmpdir/unsorted.out" <<'UNSORTED'
  rank       wall    share        cpu    cores    load  step
      1      0.00s     0.0%      0.00s     0.00    1.00  QUICK STEP
      2      1.00s    45.0%      0.00s     0.00    1.00  SLEEPING STEP
      3      1.21s    55.0%      1.19s     0.98    1.00  BURNING STEP
UNSORTED
if [ "$(gate_profile_rank_rows "$tmpdir/unsorted.out" | grep -c .)" -eq 3 ]; then
    pass "the parser reads all 3 rows of the control table"
else
    fail "the parser did not read the control table, so Test 3 may be asserting over nothing"
fi
if gate_profile_ranking_ok "$tmpdir/unsorted.out"; then
    fail "the ranking check PASSED a table ordered fastest-first — it cannot detect an unsorted table"
else
    pass "the ranking check rejects rows that ascend"
fi

# --- Test 4: POSITIVE CONTROL — CPU is measured, and it discriminates --------
echo ""
echo "Test 4: CPU is actually measured (the subshell trap), and separates work from waiting"
if [ ! -s "$JSON" ]; then
    fail "POGO_GATE_PROFILE_JSON produced no record; every assertion below is void"
else
    if jq -e . "$JSON" >/dev/null 2>&1; then
        pass "the JSON record parses"
    else
        fail "the JSON record does not parse: $(cat "$JSON")"
    fi
    BURN_CPU="$(jq -r '.steps[] | select(.step == "BURNING STEP") | .cpu_seconds' "$JSON")"
    SLEEP_CPU="$(jq -r '.steps[] | select(.step == "SLEEPING STEP") | .cpu_seconds' "$JSON")"
    SLEEP_WALL="$(jq -r '.steps[] | select(.step == "SLEEPING STEP") | .wall_seconds' "$JSON")"

    # The load-bearing assertion. With `times` captured through a command
    # substitution this reads 0.00 and the whole instrument is a wall-clock
    # table wearing a CPU column.
    if awk -v c="$BURN_CPU" 'BEGIN { exit !(c >= 0.10) }'; then
        pass "the CPU-burning step reported ${BURN_CPU}s of CPU (>= 0.10s)"
    else
        fail "the CPU-burning step reported ${BURN_CPU}s of CPU — the times() reading is not reaching the recorder"
    fi

    if awk -v c="$SLEEP_CPU" 'BEGIN { exit !(c <= 0.10) }'; then
        pass "the sleeping step reported ${SLEEP_CPU}s of CPU (<= 0.10s)"
    else
        fail "the sleeping step reported ${SLEEP_CPU}s of CPU — sleeping is being charged as work"
    fi

    if awk -v b="$BURN_CPU" -v s="$SLEEP_CPU" 'BEGIN { exit !(b > s) }'; then
        pass "work and waiting are distinguishable (${BURN_CPU}s vs ${SLEEP_CPU}s)"
    else
        fail "work and waiting report the same CPU — the column carries no information"
    fi

    # Wall-clock is asserted only as a LOWER bound: a 1s sleep cannot finish in
    # less than 1s however quiet the box is, and may legitimately take much
    # longer when it is busy. An upper bound here would be the load-sensitive
    # assertion this file's header refuses to write.
    if awk -v w="$SLEEP_WALL" 'BEGIN { exit !(w >= 0.9) }'; then
        pass "the 1s sleep measured ${SLEEP_WALL}s of wall-clock"
    else
        fail "the 1s sleep measured ${SLEEP_WALL}s of wall-clock — the clock is wrong"
    fi
fi

# --- Test 5: a failing step still fails the script, and is still reported ----
# Both halves matter. If profiling swallowed a failure the gate would merge
# broken branches (mg-59d5's defect, reintroduced by an instrument); if the
# report were appended at the bottom instead of armed on EXIT, the profile
# would be absent from precisely the runs someone wants it for.
echo ""
echo "Test 5: A failing step propagates its status AND appears in the report"
cat > "$tmpdir/failing.sh" <<FAILDRIVER
#!/bin/bash
set -e
. "$LIB"
gate_profile_begin "failing"
trap 'gate_profile_report' EXIT
gate_step "GOOD STEP" true
gate_step "BAD STEP" bash -c 'exit 3'
gate_step "NEVER REACHED" true
FAILDRIVER

set +e
bash "$tmpdir/failing.sh" > "$tmpdir/failing.out" 2>&1
FAIL_RC=$?
set -e

if [ "$FAIL_RC" -eq 3 ]; then
    pass "the script exited with the failing step's own status (3)"
else
    fail "expected exit 3 from the failing step, got $FAIL_RC"
fi
if grep -q 'BAD STEP' "$tmpdir/failing.out" && grep -q '\[FAILED\]' "$tmpdir/failing.out"; then
    pass "the failing step is in the table and marked [FAILED]"
else
    fail "the failing step is not reported as failed"
fi
if grep -q 'GOOD STEP' "$tmpdir/failing.out"; then
    pass "steps that ran before the failure are still reported"
else
    fail "the pre-failure steps were lost with the failure"
fi
if grep -q 'NEVER REACHED' "$tmpdir/failing.out"; then
    fail "a step after the failure was run — set -e is not aborting"
else
    pass "no step after the failure ran"
fi

# --- Test 6: sourcing the library does not turn errexit ON for the caller ----
# A library that silently arms `set -e` breaks scripts merely by profiling
# them: every command whose non-zero status was being read as data becomes
# fatal. gate_step suspends errexit around the step and restores it only if the
# caller had it.
echo ""
echo "Test 6: gate_step does not arm errexit in a caller that did not set it"
cat > "$tmpdir/noerrexit.sh" <<NOEDRIVER
#!/bin/bash
. "$LIB"
gate_profile_begin "noerrexit"
gate_step "FAILING BUT TOLERATED" bash -c 'exit 1'
case "\$-" in
    *e*) echo "ERREXIT-ARMED" ;;
    *)   echo "ERREXIT-CLEAR" ;;
esac
echo "REACHED-THE-END"
NOEDRIVER

set +e
bash "$tmpdir/noerrexit.sh" > "$tmpdir/noerrexit.out" 2>&1
set -e
if grep -q 'ERREXIT-CLEAR' "$tmpdir/noerrexit.out" && grep -q 'REACHED-THE-END' "$tmpdir/noerrexit.out"; then
    pass "errexit was left as the caller had it and execution continued"
else
    fail "the library changed the caller's errexit state: $(cat "$tmpdir/noerrexit.out")"
fi

# --- Test 7: POGO_GATE_PROFILE=0 disables the table, not the steps ----------
echo ""
echo "Test 7: POGO_GATE_PROFILE=0 suppresses the table and still runs the steps"
set +e
POGO_GATE_PROFILE=0 bash "$tmpdir/driver.sh" > "$tmpdir/off.out" 2>&1
OFF_RC=$?
set -e
if [ "$OFF_RC" -eq 0 ] && ! grep -q 'GATE STEP PROFILE' "$tmpdir/off.out"; then
    pass "the table is suppressed and the driver still succeeded"
else
    fail "POGO_GATE_PROFILE=0 misbehaved (rc=$OFF_RC): $(cat "$tmpdir/off.out")"
fi
if grep -q 'SLEEPING STEP' "$tmpdir/off.out"; then
    pass "step labels are still announced with profiling off"
else
    fail "disabling the profile also silenced the step announcements"
fi

# --- Test 8: no JSON file is written unless one is asked for ----------------
# The gate runs in ephemeral worktrees under a developer's live HOME. A default
# write path would be a new instance of exactly the live-state coupling this
# ticket is about (mg-5551), added by the tool sent to measure it.
echo ""
echo "Test 8: No file is written when POGO_GATE_PROFILE_JSON is unset"
BEFORE="$(ls -A "$tmpdir" | sort | tr '\n' ' ')"
set +e
(cd "$tmpdir" && env -u POGO_GATE_PROFILE_JSON bash "$tmpdir/driver.sh" > "$tmpdir/nojson.out" 2>&1)
set -e
rm -f "$tmpdir/nojson.out"
AFTER="$(ls -A "$tmpdir" | sort | tr '\n' ' ')"
if [ "$BEFORE" = "$AFTER" ]; then
    pass "the run left no new files behind"
else
    fail "the run wrote files with no output path configured: before=[$BEFORE] after=[$AFTER]"
fi

# --- Test 9: WIRING against the real tree -----------------------------------
# Pinned against the real test.sh and build.sh: the day either stops routing
# through gate_step, the per-step breakdown this ticket bought is gone, and a
# fixture would not notice. The same reasoning as go-test-budget_test.sh's
# tests 9-10, for the same reason.
echo ""
echo "Test 9: test.sh and build.sh route their steps through gate_step"
TEST_SH_CODE="$(grep -vE '^[[:space:]]*#' "$REPO_ROOT/test.sh")"
BUILD_SH_CODE="$(grep -vE '^[[:space:]]*#' "$REPO_ROOT/build.sh")"

STEP_COUNT="$(printf '%s\n' "$TEST_SH_CODE" | grep -c '^gate_step ' || true)"
if [ "$STEP_COUNT" -ge 15 ]; then
    pass "test.sh profiles $STEP_COUNT steps"
else
    fail "test.sh profiles only $STEP_COUNT steps — suites are running unprofiled"
fi

# Every `bash <suite>` at the top level of test.sh must be inside a gate_step.
# Without this, a suite added later joins the gate's cost and never appears in
# the table that is supposed to rank it.
UNPROFILED="$(printf '%s\n' "$TEST_SH_CODE" | grep -E '^[[:space:]]*bash ' || true)"
if [ -z "$UNPROFILED" ]; then
    pass "test.sh has no unprofiled top-level suite invocation"
else
    fail "test.sh runs suites outside gate_step: $UNPROFILED"
fi

if printf '%s\n' "$BUILD_SH_CODE" | grep -q 'gate_step .*fmt\.sh' &&
    printf '%s\n' "$BUILD_SH_CODE" | grep -q 'gate_step .*test\.sh' &&
    printf '%s\n' "$BUILD_SH_CODE" | grep -q 'gate_step .*go build'; then
    pass "build.sh profiles all three of fmt, test and compile"
else
    fail "build.sh leaves one of fmt/test/compile unprofiled"
fi

if printf '%s\n' "$TEST_SH_CODE" | grep -q "trap 'gate_profile_report' EXIT" &&
    printf '%s\n' "$BUILD_SH_CODE" | grep -q "trap 'gate_profile_report' EXIT"; then
    pass "both arm the report on EXIT, so a failed gate still profiles"
else
    fail "a gate script appends its report instead of arming it on EXIT"
fi

echo ""
echo "=== Results: ${PASS} passed, ${FAIL} failed ==="
[ "$FAIL" -eq 0 ]
