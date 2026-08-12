#!/bin/bash
# The $TMPDIR leak guard (mg-de3c).
#
# THE MEASUREMENT IS THE TEST. Count $TMPDIR's entries, run a suite that creates
# fixtures, count again. That is the whole acceptance criterion on the ticket,
# and on 2026-08-12 it failed by 37,083 entries and 61 GB — enough that
# ./build.sh hit "no space left on device" in the refinery gate and a healthy
# branch was rejected as defective (mg-b41f).
#
# Measured on this tree before the fix: one `go test ./...` left 33 directories
# in $TMPDIR, every one of them a test fixture nothing would ever delete.
#
# WHAT IS ASSERTED
#
#   Test 2  POSITIVE CONTROL, and it runs first. The check is a count, so
#           "the count did not grow" is worth nothing until "the count grows
#           when something leaks" has been shown against the same code path.
#   Test 3  A COLD $TMPDIR gains EXACTLY ONE entry — internal/testtmp's root.
#   Test 4  A WARM $TMPDIR gains NOTHING. This is the acceptance criterion
#           verbatim: count, run, count, unchanged.
#   Test 5  The sweep RECLAIMS. Nesting alone would only rename the problem;
#           repeated runs must not grow the root's own contents either.
#
# WHAT IS NOT COVERED, AND WHY IT IS NAMED HERE RATHER THAN LEFT QUIET
#
#   config.AgentSocketDir's `$TMPDIR/pogo-agents-<hash>` fallback still leaks
#   one directory per distinct POGO_HOME (3 per full suite run; 3,883 of the
#   37,083 entries measured). It is NOT fixed and it is NOT in the slice below.
#   The reason is a path budget, not an oversight: that leaf must be derived
#   IDENTICALLY by a test binary and by a non-test pogod sharing one POGO_HOME —
#   they find each other by computing the same path — so it can carry neither a
#   pid nor a shared parent directory. On this box the fallback is already 69
#   bytes of the 73 that AF_UNIX's sun_path leaves it, so there is no room to
#   nest it under a swept root, and making it differ between test and non-test
#   binaries would reintroduce mg-8532's split-socket bug. Left alone
#   deliberately and filed as mg-a997, with the budget arithmetic written out.
#
#   agent.ExpandTemplateToFile's $TMPDIR/pogo-prompts is likewise untouched: one
#   entry, but 15,819 files and 74 MB inside it, and a PRODUCTION leak rather
#   than a test one. Filed as mg-5197.
#
# The slice below is chosen to run in seconds while touching every internal/
# testtmp caller: events, ghintake, ghteardown, testsandbox and the witness /
# claim-release resolvers in internal/agent. A new leak anywhere in it fails
# this suite by name.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HERE="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PASS=0
FAIL=0

# The packaged shell isolation (mg-78a5). This suite runs `go test`, and a Go
# test binary reads HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT — so without
# this the counts below would be a property of the developer's live tree, and
# the runs would write to it. It is also the ratchet in
# internal/testsandbox/adoption_test.go: a new *_test.sh that skips this fails
# the build rather than being added to a ledger.
#
# It pins HOME and friends but NOT $TMPDIR, which is what makes it composable
# here: every measurement below supplies its own TMPDIR per invocation.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create tmpdirleak
pogo_sandbox_isolate
trap 'pogo_sandbox_down' EXIT

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

# The root internal/testtmp owns. Read from the Go constant rather than
# duplicated, so renaming it there cannot leave this suite asserting a name
# nothing writes any more.
ROOT_NAME="$(cd "$REPO_ROOT" && go list -f '{{range .ConstNames}}{{.}}{{end}}' ./internal/testtmp >/dev/null 2>&1; \
    grep -E '^const RootName = ' internal/testtmp/testtmp.go 2>/dev/null | sed -E 's/.*"(.*)".*/\1/')"
if [ -z "$ROOT_NAME" ]; then
    echo "SETUP FAILURE: could not read RootName from internal/testtmp/testtmp.go" >&2
    exit 1
fi

# The per-package budget keeps every `go test` here bounded — scripts/
# unbounded-go-test.sh sweeps the tree for invocations that are not.
BUDGET="${POGO_GO_TEST_TIMEOUT:-5m}"

# Packages that between them exercise every internal/testtmp caller.
SLICE=(./internal/events/ ./internal/ghintake/ ./internal/ghteardown/ ./internal/testsandbox/ ./internal/testtmp/)
# internal/agent in full is ~300s; these three names reach the witness store,
# the claim-release store and testsandbox.Main in about two seconds.
AGENT_RUN='TestWitness|TestMacguffinStoreRoot|TestClaimRelease'

# count_entries prints the number of TOP-LEVEL entries in a directory. Top-level
# is the whole point: the defect is $TMPDIR's entry count, not its depth.
count_entries() {
    find "$1" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' '
}

# list_entries prints them, for a failure message that names what appeared
# rather than only how many.
list_entries() { find "$1" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort; }

# run_slice runs the fixture-creating suite with $TMPDIR pinned to its argument.
# Output is captured; a failing suite is reported as a SETUP failure below,
# because a suite that did not run creates no fixtures and would sail through
# every count in this file.
run_slice() {
    local tmp=$1 log=$2
    (
        cd "$REPO_ROOT"
        TMPDIR="$tmp" go test -timeout "$BUDGET" -count=1 "${SLICE[@]}" >"$log" 2>&1 &&
        TMPDIR="$tmp" go test -timeout "$BUDGET" -count=1 -run "$AGENT_RUN" ./internal/agent/ >>"$log" 2>&1
    )
}

echo "=== \$TMPDIR leak guard (mg-de3c) ==="
echo "    testtmp root: $ROOT_NAME"

# Inside the sandbox root, so the EXIT trap above owns its removal — a suite
# about leaked temp directories that leaked its own would be its own defect.
WORK="$POGO_SANDBOX_DIR/work"
mkdir -p "$WORK"

# --- Test 1: this file's own syntax ----------------------------------------
echo ""
echo "Test 1: Script syntax check"
if bash -n "$0" 2>/dev/null; then
    pass "tmpdir-leak_test.sh has valid bash syntax"
else
    fail "tmpdir-leak_test.sh has syntax errors"
fi

# --- Test 2: POSITIVE CONTROL ----------------------------------------------
# Before any "the count did not grow" assertion is allowed to mean anything, the
# same counting code has to be shown reporting growth. This plants one directory
# of exactly the shape the defect produces — an os.MkdirTemp-style entry sitting
# directly in $TMPDIR that nothing will ever remove.
echo ""
echo "Test 2: POSITIVE CONTROL — the count detects a planted leak"
control="$WORK/control"
mkdir -p "$control"
before=$(count_entries "$control")
mktemp -d "$control/pogo-leaked-fixture.XXXXXX" >/dev/null
after=$(count_entries "$control")
if [ "$after" -gt "$before" ]; then
    pass "a planted directory moves the count $before -> $after"
else
    fail "the count did not move when a directory was planted ($before -> $after); every other assertion in this file is vacuous"
fi

# --- Test 3: a cold TMPDIR gains exactly one entry --------------------------
echo ""
echo "Test 3: a COLD \$TMPDIR gains exactly one entry, and it is the testtmp root"
cold="$WORK/cold"
mkdir -p "$cold"
if ! run_slice "$cold" "$WORK/cold.log"; then
    fail "SETUP: the fixture-creating suite did not pass, so this file measured nothing"
    sed -n '1,40p' "$WORK/cold.log" >&2
else
    n=$(count_entries "$cold")
    if [ "$n" -eq 1 ] && [ "$(list_entries "$cold")" = "$ROOT_NAME" ]; then
        pass "one entry after a cold run, and it is $ROOT_NAME"
    else
        fail "a cold run left $n top-level entries, want exactly 1 ($ROOT_NAME):"
        list_entries "$cold" | sed 's/^/        /' >&2
    fi
fi

# --- Test 4: the acceptance criterion, verbatim -----------------------------
# Count before, run the suite, count after. Unchanged.
echo ""
echo "Test 4: a WARM \$TMPDIR is UNCHANGED by a run that creates fixtures"
before=$(count_entries "$cold")
if ! run_slice "$cold" "$WORK/warm.log"; then
    fail "SETUP: the fixture-creating suite did not pass on the warm run"
    sed -n '1,40p' "$WORK/warm.log" >&2
else
    after=$(count_entries "$cold")
    if [ "$after" -eq "$before" ]; then
        pass "entry count unchanged across a run: $before -> $after"
    else
        fail "entry count grew across a run: $before -> $after. New entries:"
        list_entries "$cold" | sed 's/^/        /' >&2
    fi
fi

# --- Test 5: the sweep reclaims --------------------------------------------
# Nesting alone would turn 37,083 entries in $TMPDIR into 37,083 entries one
# level down. The sweep is what makes the fix a fix, so it gets its own
# assertion: repeated runs must not grow the root's own contents either.
echo ""
echo "Test 5: repeated runs do not grow the testtmp root"
inner=$(count_entries "$cold/$ROOT_NAME")
if ! run_slice "$cold" "$WORK/sweep.log"; then
    fail "SETUP: the fixture-creating suite did not pass on the third run"
else
    grown=$(count_entries "$cold/$ROOT_NAME")
    # Not "== inner": the last process to touch the root leaves its own entry
    # behind for the next run to reap, so the steady state is a small constant
    # rather than zero. What must not happen is growth PER RUN.
    if [ "$grown" -le "$((inner + 1))" ]; then
        pass "root contents steady across runs: $inner -> $grown"
    else
        fail "root contents grew $inner -> $grown across one run; the sweep is not reclaiming:"
        list_entries "$cold/$ROOT_NAME" | sed 's/^/        /' >&2
    fi
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
