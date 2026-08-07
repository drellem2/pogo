#!/bin/bash
# Tests for scripts/go-test-budget.sh (mg-a465, gh#107).
#
# The contract under test:
#   1. POSITIVE CONTROL FIRST: a package that exceeds the budget is REPORTED AS
#      A TIMEOUT — the output names the package, names the budget, names the
#      tests still running, and says in words that this is an overrun and not a
#      crash. This is the whole point of the ticket, and it is the half that
#      `go test -timeout` does NOT give you: Go implements -timeout by
#      panicking, so the flag alone moves the panic later without changing its
#      shape. "It passed" is worth nothing here until "it reports" is shown.
#   2. The report is the TAIL of the output. A classification a reader has to
#      scroll back past a goroutine dump to find has not classified anything,
#      and a truncating CI log keeps the tail.
#   3. The flag is actually APPLIED, not silently dropped: Go's own panic text
#      reports the configured budget, and changing POGO_GO_TEST_TIMEOUT changes
#      what Go reports. Asserting only our own banner would pass against a
#      script that prints a budget it never passes to `go test`.
#   4. NEGATIVE CONTROLS, and they carry the weight: a non-timeout panic
#      (`panic: runtime error: ...`) and an ordinary t.Fatal must NOT be
#      reported as overruns. Without these, a classifier that shouted TIMEOUT
#      at every failure would pass case 1.
#   5. A passing run is unaffected — exit 0 and no report.
#   6. The default budget is 20m when nothing overrides it.
#   7. A caller-supplied -timeout is refused, so the enforced budget and the
#      reported budget cannot disagree.
#   8. WIRING: test.sh and ci.yml route the suite through this script, and
#      neither carries an unbounded `go test`. #107's own argument is that a
#      partial fix is worse than none — someone who learns "test.sh has a
#      timeout now" will assume the suite is bounded everywhere and be wrong in
#      the one place nobody watches interactively. These two cases are what
#      stop the fix being silently unwired later.
#
# Every Go fixture is a throwaway module in a temp dir, so the repo's own
# packages are never run, mutated or read by this file. Cases 8-9 are the only
# ones that touch the real tree, and they only read it.
#
# COST: 12.1s measured end-to-end at load avg ~6 on this host. 5s of that is
# deliberate sleeping (a 2s budget and a 3s budget); the rest is fixture
# compiles against the sandbox's cold GOCACHE. The budget is overridable
# precisely so this file costs seconds instead of 20 minutes — a control nobody
# can afford to run is not a control.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUDGET_SH="${SCRIPT_DIR}/go-test-budget.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

# The packaged test isolation (mg-78a5). This suite has no business near the
# developer's ~/.pogo — its fixtures are throwaway Go modules and its only reads
# of the real tree are test.sh and ci.yml — but "this one doesn't need it" is
# exactly the reasoning that produced mg-6092 / mg-e8e7 / mg-5336 / mg-3412, so
# the envelope is adopted rather than argued with.
#
# Note the ordering constraint the harness documents: pogo_sandbox_create moves
# no environment variable, and isolate pins the Go toolchain (mg-cdf1) so the
# `go test` runs below cannot re-download ~70MB into a HOME this run deletes.
# REPO_ROOT is resolved above, before the move, because after it $HOME is gone.
# shellcheck source=/dev/null
source "$SCRIPT_DIR/pogo-sandbox"
pogo_sandbox_create gotestbudget
pogo_sandbox_isolate
trap pogo_sandbox_down EXIT

WORK="$POGO_SANDBOX_DIR/work"
mkdir -p "$WORK"

echo "=== go-test-budget.sh tests ==="

# --- Test 1: syntax --------------------------------------------------------
echo ""
echo "Test 1: Script syntax check"
if bash -n "$BUDGET_SH" 2>/dev/null; then
    pass "go-test-budget.sh has valid bash syntax"
else
    fail "go-test-budget.sh has syntax errors"
fi

# Fixture builder. Each module gets only the packages a case needs, so a case
# never pays for another case's sleep.
make_module() {
    local dir=$1
    mkdir -p "$dir"
    cat > "$dir/go.mod" <<'EOF'
module example.com/budgetfixture

go 1.25.0
EOF
}

add_slow_pkg() {
    local dir=$1 name=$2
    mkdir -p "$dir/$name"
    cat > "$dir/$name/slow_test.go" <<EOF
package $name

import (
	"testing"
	"time"
)

// Idle goroutines, so the dump this produces has the shape a real daemon
// suite's does rather than a two-frame toy's.
func TestSleepsPast${name}Budget(t *testing.T) {
	for i := 0; i < 4; i++ {
		go func() { select {} }()
	}
	time.Sleep(10 * time.Minute)
}
EOF
}

add_ok_pkg() {
    local dir=$1 name=$2
    mkdir -p "$dir/$name"
    cat > "$dir/$name/ok_test.go" <<EOF
package $name

import "testing"

func TestPassesImmediately(t *testing.T) {}
EOF
}

add_fatal_pkg() {
    local dir=$1 name=$2
    mkdir -p "$dir/$name"
    cat > "$dir/$name/fatal_test.go" <<EOF
package $name

import "testing"

func TestOrdinaryAssertionFailure(t *testing.T) {
	t.Fatal("an ordinary failure, not a timeout")
}
EOF
}

add_panic_pkg() {
    local dir=$1 name=$2
    mkdir -p "$dir/$name"
    cat > "$dir/$name/panic_test.go" <<EOF
package $name

import "testing"

func TestNonTimeoutPanic(t *testing.T) {
	var p *int
	_ = *p
}
EOF
}

# run_budget <module-dir> <budget-or-empty> [args...] -> stdout+stderr in $OUT, status in $STATUS
run_budget() {
    local dir=$1 budget=$2
    shift 2
    set +e
    if [ -n "$budget" ]; then
        OUT="$(cd "$dir" && POGO_GO_TEST_TIMEOUT="$budget" bash "$BUDGET_SH" "$@" 2>&1)"
    else
        OUT="$(cd "$dir" && unset POGO_GO_TEST_TIMEOUT && bash "$BUDGET_SH" "$@" 2>&1)"
    fi
    STATUS=$?
    set -e
}

# --- Tests 2-5: one mixed run carries the positive and both negatives -------
#
# Deliberately one run rather than four: the negatives are only worth something
# if the classifier saw a real timeout in the SAME output it declined to
# classify them from. Run separately, "no TIMEOUT reported" is also what a
# totally broken classifier prints.
echo ""
echo "Test 2: POSITIVE CONTROL — an overrun is reported as a timeout, with the negatives alongside"
MIXED="$WORK/mixed"
make_module "$MIXED"
add_slow_pkg  "$MIXED" slowpkg
add_ok_pkg    "$MIXED" okpkg
add_fatal_pkg "$MIXED" fatalpkg
add_panic_pkg "$MIXED" panicpkg
run_budget "$MIXED" 2s ./...

if [ "$STATUS" -ne 0 ]; then
    pass "an overrun exits non-zero (status $STATUS)"
else
    fail "an overrun exited 0 — the gate would have merged it"
fi

if printf '%s\n' "$OUT" | grep -q '^TIMEOUT: 1 package exceeded the per-package test budget of 2s\.$'; then
    pass "the report names the count and the budget"
else
    fail "no 'TIMEOUT: 1 package ... budget of 2s' line in the output"
fi

if printf '%s\n' "$OUT" | grep -q 'example\.com/budgetfixture/slowpkg'; then
    pass "the report names the package that overran"
else
    fail "the report does not name example.com/budgetfixture/slowpkg"
fi

if printf '%s\n' "$OUT" | grep -q 'still running at the budget: TestSleepsPastslowpkgBudget'; then
    pass "the report names the test that was still running"
else
    fail "the report does not name the still-running test"
fi

if printf '%s\n' "$OUT" | grep -q 'BUDGET OVERRUN, not a crash'; then
    pass "the report says in words that this is not a crash"
else
    fail "the report does not distinguish an overrun from a crash"
fi

# The two causes an overrun can have need opposite responses; the report is
# only useful if it points at the distinction rather than just naming a number.
if printf '%s\n' "$OUT" | grep -q 'deadlock' && printf '%s\n' "$OUT" | grep -q 'loaded host'; then
    pass "the report names both causes a reader has to tell apart"
else
    fail "the report does not name the deadlock/loaded-host distinction"
fi

echo ""
echo "Test 3: the report is the TAIL, not something buried above the goroutine dump"
LAST="$(printf '%s\n' "$OUT" | grep -v '^[[:space:]]*$' | tail -1)"
if printf '%s' "$LAST" | grep -q '^=\+$'; then
    pass "the last non-empty line is the report's closing rule"
else
    fail "the output does not end in the report (ends with: ${LAST:0:70})"
fi

# The dump must still be present. Suppressing it would pass every assertion
# above while destroying the only evidence that separates the two causes.
if printf '%s\n' "$OUT" | grep -q '^panic: test timed out after 2s$'; then
    pass "the underlying dump is preserved, not swallowed"
else
    fail "the goroutine dump was suppressed — the deadlock evidence is gone"
fi

echo ""
echo "Test 4: NEGATIVE CONTROLS — ordinary failures in the same run are not called timeouts"
# Exactly one package overran; the t.Fatal package and the nil-deref package
# must not have been swept into the count. Test 2 already pinned "1 package",
# which is that assertion's teeth; these name the mechanism.
if printf '%s\n' "$OUT" | grep -q 'FAIL[[:space:]]*example\.com/budgetfixture/fatalpkg'; then
    pass "the t.Fatal package did fail (the negative control is live)"
else
    fail "the t.Fatal package did not fail — the negative control proves nothing"
fi

if printf '%s\n' "$OUT" | grep -q 'panic: runtime error'; then
    pass "the nil-deref package did panic (the negative control is live)"
else
    fail "the nil-deref package did not panic — the negative control proves nothing"
fi

TIMEOUT_LINES="$(printf '%s\n' "$OUT" | grep -c 'still running at the budget:' || true)"
if [ "$TIMEOUT_LINES" -eq 1 ]; then
    pass "exactly one package was classified as an overrun"
else
    fail "expected 1 classified overrun, got $TIMEOUT_LINES"
fi

echo ""
echo "Test 5: a passing run is unaffected"
OKMOD="$WORK/okonly"
make_module "$OKMOD"
add_ok_pkg  "$OKMOD" okpkg
run_budget "$OKMOD" 2s ./...
if [ "$STATUS" -eq 0 ]; then
    pass "a clean run exits 0"
else
    fail "a clean run exited $STATUS"
fi
if printf '%s\n' "$OUT" | grep -q 'TIMEOUT:'; then
    fail "a clean run printed a timeout report"
else
    pass "a clean run prints no timeout report"
fi

# --- Test 6: the budget is really passed to `go test` ----------------------
#
# Go's own panic text is the witness, not our banner: a script that printed a
# budget it never passed through would satisfy the banner and fail here.
echo ""
echo "Test 6: the configured budget is the one Go enforces"
SLOWMOD="$WORK/slowonly"
make_module "$SLOWMOD"
add_slow_pkg "$SLOWMOD" slowpkg
run_budget "$SLOWMOD" 3s ./...
if printf '%s\n' "$OUT" | grep -q '^panic: test timed out after 3s$'; then
    pass "POGO_GO_TEST_TIMEOUT=3s reaches go test (go reports 3s, not the 2s of the run before)"
else
    fail "go did not report the configured 3s budget — the flag may be ignored"
fi
if printf '%s\n' "$OUT" | grep -q 'budget enforced by go: 3s'; then
    pass "the report quotes go's enforced figure rather than restating our own"
else
    fail "the report does not quote go's enforced budget"
fi

# --- Test 7: the shipped default --------------------------------------------
echo ""
echo "Test 7: the default budget is 20m"
run_budget "$OKMOD" "" ./...
if printf '%s\n' "$OUT" | grep -q 'per-package budget: 20m'; then
    pass "with nothing set, the budget is 20m"
else
    fail "the default budget is not 20m"
fi

# --- Test 8: a caller-supplied -timeout is refused ---------------------------
echo ""
echo "Test 8: a caller-supplied -timeout is refused"
run_budget "$OKMOD" 2s -timeout 5s ./...
if [ "$STATUS" -eq 2 ]; then
    pass "an explicit -timeout is a usage error (exit 2)"
else
    fail "an explicit -timeout was accepted (exit $STATUS) — two budgets could disagree"
fi
run_budget "$OKMOD" 2s -timeout=5s ./...
if [ "$STATUS" -eq 2 ]; then
    pass "the -timeout=VALUE spelling is refused too"
else
    fail "-timeout=5s was accepted (exit $STATUS)"
fi

# --- Tests 9-10: WIRING against the real tree --------------------------------
#
# Pinned against the real test.sh and ci.yml rather than fixtures: the day
# either one stops routing through this script, the property the ticket bought
# is gone, and a fixture would not notice.
echo ""
echo "Test 9: test.sh routes the Go suite through the budget script"
TEST_SH_CODE="$(grep -vE '^[[:space:]]*#' "$REPO_ROOT/test.sh")"
if printf '%s\n' "$TEST_SH_CODE" | grep -q 'go-test-budget\.sh'; then
    pass "test.sh invokes scripts/go-test-budget.sh"
else
    fail "test.sh does not invoke scripts/go-test-budget.sh"
fi

UNBOUNDED="$(printf '%s\n' "$TEST_SH_CODE" | grep -E '(^|[^-[:alnum:]_])go test' | grep -v -- '-timeout' || true)"
if [ -z "$UNBOUNDED" ]; then
    pass "test.sh carries no unbounded 'go test'"
else
    fail "test.sh still has an unbounded 'go test': $UNBOUNDED"
fi

echo ""
echo "Test 10: ci.yml carries no unbounded 'go test' either"
CI_YML="$REPO_ROOT/.github/workflows/ci.yml"
CI_CODE="$(grep -vE '^[[:space:]]*#' "$CI_YML")"
if printf '%s\n' "$CI_CODE" | grep -q 'go-test-budget\.sh'; then
    pass "ci.yml's test job routes through scripts/go-test-budget.sh"
else
    fail "ci.yml's test job does not route through scripts/go-test-budget.sh"
fi

# The pi-e2e job runs its own `go test ./internal/pi/ -v -timeout 300s`. That
# is already bounded and is deliberately left alone; what this asserts is the
# general property — every go test invocation in CI is bounded by something.
CI_UNBOUNDED="$(printf '%s\n' "$CI_CODE" | grep -E '(^|[^-[:alnum:]_])go test' | grep -v -- '-timeout' || true)"
if [ -z "$CI_UNBOUNDED" ]; then
    pass "every 'go test' in ci.yml is bounded"
else
    fail "ci.yml still has an unbounded 'go test': $CI_UNBOUNDED"
fi

# --- Summary ---------------------------------------------------------------
echo ""
echo "=== go-test-budget.sh: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
