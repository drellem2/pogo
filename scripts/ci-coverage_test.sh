#!/bin/bash
# Tests for scripts/ci-coverage.sh and scripts/lib/gate-exit.sh (mg-82a6).
#
# The contract under test:
#   1. Syntax.
#   2. POSITIVE CONTROL — the count MOVES. On a fixture pair whose overlap is
#      known by construction, the reported numbers are exactly that overlap. A
#      coverage instrument that reports a plausible number is worth nothing
#      until the same code has been shown to report a DIFFERENT plausible
#      number for a different tree.
#   3. POSITIVE CONTROL — prose does not count as coverage. A gate script whose
#      path appears in the workflow ONLY inside a comment must be reported as
#      NOT run by CI. This is the false-positive class that made a presence
#      rule unusable for the tree-wide `go test` sweep (mg-37d4): there, a
#      presence check flagged 9 prose lines against 1 real invocation.
#   4. Token matching, not substring: `thing.sh` is not covered by a workflow
#      that runs `thing_test.sh`.
#   5. A wrapper the gate applies and CI does not is reported as covered NOT
#      IDENTICALLY, and the wrapper is NAMED. This is the one real instance in
#      the tree — the gate wraps the shared Go row in a $TMPDIR-count assertion
#      that can fail on its own with every Go test passing.
#   6. POSITIVE CONTROL — unmeasurable is loud. A missing file, a workflow with
#      no `run:` commands, and a gate with no `gate_step` rows each exit 2 and
#      say the parser could not measure. Reporting 0 shared rows because the
#      parse rotted would be the same defect this instrument exists to stop,
#      wearing the instrument's clothes.
#   7. --json parses and its numbers agree with the human output.
#   8. WIRING — sourcing scripts/lib/gate-exit.sh and arming the same trap
#      test.sh arms prints the notice, prints the do-not-escalate paragraph on
#      a FAILING run, and does not change the exit status either way.
#   9. WIRING against the real tree — test.sh arms gate_exit_report, and the
#      instrument runs cleanly against the repo's own two files.
#
# Cases 2-7 run against fixture files in temp dirs via --gate-file/--ci-file, so
# nothing here has to be edited when a row is added to test.sh or a job to
# ci.yml.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HERE="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
COV="${SCRIPT_DIR}/ci-coverage.sh"
PASS=0
FAIL=0

# The packaged test isolation (mg-78a5), not a hand-rolled mktemp root. Nothing
# in this file means to reach the developer's live ~/.pogo — it reads two
# tracked files and writes fixtures — but "this suite does not need it" is
# exactly the reasoning that produced four separate tickets for one defect
# (mg-6092, mg-e8e7, mg-5336, mg-3412), and it is not an accepted entry in the
# adoption ledger. It is also not true here by inspection alone: Test 8 sources
# scripts/lib/gate-profile.sh and runs a real gate driver, so the code under
# test is not confined to this file.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create ci-coverage
pogo_sandbox_isolate

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() {
    FAIL=$((FAIL + 1))
    echo "  FAIL: $1" >&2
}

echo "=== ci-coverage.sh tests ==="

# --- Test 1: syntax --------------------------------------------------------
echo ""
echo "Test 1: script syntax"
if bash -n "$COV" 2>/dev/null; then
    pass "ci-coverage.sh has valid bash syntax"
else
    fail "ci-coverage.sh has syntax errors"
fi
if bash -n "$SCRIPT_DIR/lib/gate-exit.sh" 2>/dev/null; then
    pass "scripts/lib/gate-exit.sh has valid bash syntax"
else
    fail "scripts/lib/gate-exit.sh has syntax errors"
fi

# Fixture builder. `gate` is a file of gate_step rows; `ci` is a workflow.
# Fixtures live inside the sandbox root so they are torn down by the same
# pogo_sandbox_down that releases everything else, rather than by a second
# cleanup path that could be forgotten.
T="$POGO_SANDBOX_DIR/fixtures"
mkdir -p "$T"
trap 'pogo_sandbox_down' EXIT INT TERM HUP

run_cov() {
    local gate="$1" ci="$2"
    shift 2
    bash "$COV" --gate-file "$gate" --ci-file "$ci" "$@"
}

# --- Test 2: POSITIVE CONTROL — the numbers follow the tree ----------------
echo ""
echo "Test 2: POSITIVE CONTROL — the reported overlap is the constructed overlap"

cat > "$T/gate-a.sh" <<'EOF'
gate_step "Row one" bash scripts/one.sh
gate_step "Row two" bash scripts/two.sh
gate_step "Row three" bash scripts/three.sh
gate_step "Row four" bash scripts/four.sh
EOF
cat > "$T/ci-a.yml" <<'EOF'
name: CI
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - name: One
        run: bash scripts/one.sh
EOF
set +e
out="$(run_cov "$T/gate-a.sh" "$T/ci-a.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'runs 1 of this gate.s 4 rows'; then
    pass "1 of 4 on the 1-of-4 fixture"
else
    fail "expected '1 of ... 4 rows', got exit $status: $out"
fi

# The SAME code, a different tree, a different answer. Without this the passing
# case above is consistent with a script that prints whatever it likes.
cat > "$T/ci-b.yml" <<'EOF'
name: CI
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: bash scripts/one.sh
      - run: |
          bash scripts/two.sh
          bash scripts/three.sh
EOF
set +e
out="$(run_cov "$T/gate-a.sh" "$T/ci-b.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'runs 3 of this gate.s 4 rows'; then
    pass "3 of 4 once CI gains a block-scalar run: with two more of the rows"
else
    fail "expected '3 of ... 4 rows' on the widened fixture, got exit $status: $out"
fi
if echo "$out" | grep -q '1 rows run ONLY here'; then
    pass "names the gate-only remainder (1) beside the shared count"
else
    fail "did not report the gate-only remainder: $out"
fi

# --- Test 3: POSITIVE CONTROL — prose is not coverage ----------------------
echo ""
echo "Test 3: POSITIVE CONTROL — a path named only in a comment is NOT coverage"
cat > "$T/ci-comment.yml" <<'EOF'
name: CI
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      # We used to run bash scripts/one.sh here and should again some day.
      - run: |
          # bash scripts/two.sh is disabled while it is flaky
          echo nothing
EOF
set +e
out="$(run_cov "$T/gate-a.sh" "$T/ci-comment.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'runs 0 of this gate.s 4 rows'; then
    pass "0 of 4 — neither the YAML comment nor the in-run comment counts"
else
    fail "prose counted as coverage; got exit $status: $out"
fi

# --- Test 4: token matching, not substring ---------------------------------
echo ""
echo "Test 4: a workflow running thing_test.sh does not cover a gate row for thing.sh"
cat > "$T/gate-tok.sh" <<'EOF'
gate_step "The thing" bash scripts/thing.sh
EOF
cat > "$T/ci-tok.yml" <<'EOF'
name: CI
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: bash scripts/thing_test.sh
EOF
set +e
out="$(run_cov "$T/gate-tok.sh" "$T/ci-tok.yml" --quiet 2>&1)"
set -e
if echo "$out" | grep -q 'runs 0 of this gate.s 1 rows'; then
    pass "substring match rejected — 0 of 1"
else
    fail "thing_test.sh was accepted as coverage for thing.sh: $out"
fi

# --- Test 5: a wrapper CI does not run is reported, and NAMED --------------
echo ""
echo "Test 5: a row CI runs WITHOUT the gate's wrapper is 'not identically', named"
cat > "$T/gate-wrap.sh" <<'EOF'
gate_step "Wrapped row" bash scripts/guard.sh bash scripts/inner.sh ./...
EOF
cat > "$T/ci-wrap.yml" <<'EOF'
name: CI
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: bash scripts/inner.sh ./...
EOF
set +e
out="$(run_cov "$T/gate-wrap.sh" "$T/ci-wrap.yml" 2>&1)"
set -e
if echo "$out" | grep -q 'run DIFFERENTLY \.* *1' &&
    echo "$out" | grep -q 'scripts/guard.sh; CI does not'; then
    pass "counted as covered-but-different and the wrapper is named"
else
    fail "wrapper divergence not reported; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi

# Different ARGUMENTS, same script, no wrapper: also 'not identically'.
cat > "$T/ci-args.yml" <<'EOF'
name: CI
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: bash scripts/inner.sh ./internal/only
EOF
cat > "$T/gate-args.sh" <<'EOF'
gate_step "Arg row" bash scripts/inner.sh ./...
EOF
set +e
out="$(run_cov "$T/gate-args.sh" "$T/ci-args.yml" 2>&1)"
set -e
if echo "$out" | grep -q 'different arguments'; then
    pass "a shared script run with different arguments is reported, not silently shared"
else
    fail "argument divergence not reported; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi

# --- Test 6: POSITIVE CONTROL — unmeasurable exits 2 and says so -----------
# The check is a COUNT. "0 of 27 shared" is the loudest thing it can say, so the
# one reading it must never be able to produce by accident is that one.
echo ""
echo "Test 6: POSITIVE CONTROL — a parse that measures nothing exits 2, not 0"

set +e
out="$(run_cov "$T/does-not-exist.sh" "$T/ci-a.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 2 ] && echo "$out" | grep -q 'CANNOT MEASURE'; then
    pass "a missing gate file exits 2 and says CANNOT MEASURE"
else
    fail "expected exit 2 on a missing gate file, got $status: $out"
fi

set +e
out="$(run_cov "$T/gate-a.sh" "$T/does-not-exist.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 2 ] && echo "$out" | grep -q 'CANNOT MEASURE'; then
    pass "a missing workflow file exits 2 and says CANNOT MEASURE"
else
    fail "expected exit 2 on a missing workflow file, got $status: $out"
fi

printf 'name: CI\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n' > "$T/ci-empty.yml"
set +e
out="$(run_cov "$T/gate-a.sh" "$T/ci-empty.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 2 ] && echo "$out" | grep -q "no 'run:' commands"; then
    pass "a workflow with no run: commands exits 2 rather than reporting 0 shared"
else
    fail "expected exit 2 on a run:-less workflow, got $status: $out"
fi

printf '#!/bin/bash\necho "no gate rows here"\n' > "$T/gate-empty.sh"
set +e
out="$(run_cov "$T/gate-empty.sh" "$T/ci-a.yml" --quiet 2>&1)"
status=$?
set -e
if [ "$status" -eq 2 ] && echo "$out" | grep -q 'no gate_step rows'; then
    pass "a gate file with no gate_step rows exits 2 rather than reporting 0 of 0"
else
    fail "expected exit 2 on a rowless gate file, got $status: $out"
fi

if echo "$out" | grep -q 'Do not read it as'; then
    pass "the unmeasurable message says it is the parser, not a finding about the tree"
else
    fail "the unmeasurable message does not disclaim itself: $out"
fi

# --- Test 7: --json ---------------------------------------------------------
echo ""
echo "Test 7: --json parses and agrees with the human output"
set +e
js="$(run_cov "$T/gate-a.sh" "$T/ci-b.yml" --json 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "--json exits 0 on a measurable pair"
else
    fail "--json exited $status: $js"
fi
if command -v jq > /dev/null 2>&1; then
    if echo "$js" | jq -e . > /dev/null 2>&1; then
        pass "--json output parses as valid JSON"
    else
        fail "--json output is not valid JSON:"
        echo "$js" | sed 's/^/      /' >&2
    fi
    got="$(echo "$js" | jq -r '"\(.gate_rows) \(.run_by_ci) \(.gate_only) \(.rows|length)"' 2>/dev/null || true)"
    if [ "$got" = "4 3 1 4" ]; then
        pass "json counts match the fixture (4 rows, 3 in CI, 1 gate-only, 4 records)"
    else
        fail "json counts wrong: got [$got], wanted [4 3 1 4]"
    fi
else
    echo "  SKIP: jq not available for the JSON checks"
fi

# --- Test 8: WIRING — the EXIT trap test.sh arms ---------------------------
# Sources the REAL scripts/lib/gate-exit.sh and arms the REAL trap, so this is
# the code test.sh runs and not a restatement of it. Costs about a second; the
# alternative — running the gate — is twelve minutes and several live daemons,
# which is the same as never testing it.
echo ""
echo "Test 8: WIRING — gate_exit_report prints the notice and preserves the status"

cat > "$T/mini-gate.sh" <<EOF
set -e
. "$REPO_ROOT/scripts/lib/gate-profile.sh"
. "$REPO_ROOT/scripts/lib/gate-exit.sh"
gate_profile_begin "mini-gate"
trap 'gate_exit_report' EXIT
gate_step "a step that passes" true
if [ "\${MINI_FAIL:-0}" = "1" ]; then
    gate_step "a step that fails" bash -c 'exit 7'
fi
EOF

set +e
out="$(bash "$T/mini-gate.sh" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "a passing gate still exits 0 with the trap armed"
else
    fail "the trap changed a passing gate's status to $status"
fi
if echo "$out" | grep -q 'CI COVERAGE'; then
    pass "the coverage notice is printed on a passing run"
else
    fail "no coverage notice on a passing run; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'GATE STEP PROFILE'; then
    pass "the per-step profile still prints alongside it (mg-eed9 is not displaced)"
else
    fail "the profile stopped printing once gate-exit.sh took the trap"
fi
if echo "$out" | grep -q 'THIS RUN FAILED'; then
    fail "the do-not-escalate paragraph printed on a PASSING run"
else
    pass "the do-not-escalate paragraph is absent on a passing run"
fi

set +e
out="$(MINI_FAIL=1 bash "$T/mini-gate.sh" 2>&1)"
status=$?
set -e
if [ "$status" -eq 7 ]; then
    pass "a failing gate still exits with the step's own status (7), not the trap's"
else
    fail "expected exit 7 from the failing step, got $status"
fi
if echo "$out" | grep -q 'THIS RUN FAILED (exit 7)'; then
    pass "the failing run's notice names the exit status"
else
    fail "no failure-specific notice; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi
# The load-bearing sentence: this is the inference the ticket was filed about.
if echo "$out" | grep -q 'does NOT contradict this' &&
    echo "$out" | grep -q 'not a reproduction'; then
    pass "the failure notice says a green CI is not a refutation AND one run is not a reproduction"
else
    fail "the failure notice is missing one of its two claims; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi

set +e
out="$(POGO_CI_COVERAGE_NOTICE=0 bash "$T/mini-gate.sh" 2>&1)"
set -e
if echo "$out" | grep -q 'CI COVERAGE'; then
    fail "POGO_CI_COVERAGE_NOTICE=0 did not suppress the notice"
else
    pass "POGO_CI_COVERAGE_NOTICE=0 suppresses the notice"
fi

# --- Test 9: WIRING against the real tree ----------------------------------
# Pinned against the repository's own files: the day test.sh stops arming this
# trap, the fact stops appearing next to the failures it is a caption on, and no
# fixture would notice. Deliberately NOT an assertion on the coverage NUMBER —
# that number moves as rows and jobs are added, and this suite must not have to
# be edited each time. What is asserted is that it can be measured at all.
echo ""
echo "Test 9: the real test.sh arms gate_exit_report, and the real pair measures"
TEST_SH_CODE="$(grep -vE '^[[:space:]]*#' "$REPO_ROOT/test.sh")"
if printf '%s\n' "$TEST_SH_CODE" | grep -q "trap 'gate_exit_report' EXIT"; then
    pass "test.sh arms gate_exit_report on EXIT"
else
    fail "test.sh no longer arms gate_exit_report — the notice will not reach a failing gate"
fi
if printf '%s\n' "$TEST_SH_CODE" | grep -q 'lib/gate-exit\.sh'; then
    pass "test.sh sources scripts/lib/gate-exit.sh"
else
    fail "test.sh does not source scripts/lib/gate-exit.sh"
fi

set +e
real="$(cd "$REPO_ROOT" && bash "$COV" --quiet 2>&1)"
status=$?
set -e
echo "      $real"
if [ "$status" -eq 0 ]; then
    pass "measures the repository's own test.sh against its own ci.yml (exit 0)"
else
    fail "could not measure the real pair (exit $status): $real"
fi
# Reported, not asserted, for the same reason: the composition of the two files
# is not this suite's business. The one property that IS asserted is that the
# gate is genuinely wider than CI — if that ever stops being true the notice is
# saying something false, and it is better to fail here than to keep printing it.
if echo "$real" | grep -qE 'rows run ONLY here'; then
    pass "the real pair reports a gate-only remainder"
else
    fail "the real pair reports no gate-only rows — the notice's premise is gone: $real"
fi

# --- Summary ---------------------------------------------------------------
echo ""
echo "=== ci-coverage.sh: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
