#!/bin/bash
# Tests for scripts/unbounded-go-test.sh (mg-37d4).
#
# The contract under test:
#   1. POSITIVE CONTROL FIRST, and it is the reason this file exists. An
#      unbounded invocation is PLANTED in a fixture checkout and the checker
#      must FIRE on it — nonzero exit, and a report naming the file and the
#      line. A checker that has never been seen to fail is indistinguishable
#      from a checker that cannot fail, and this whole ticket exists because
#      mg-a465's two-filename check was green on a tree that had an unbounded
#      site in it.
#   2. THE REGRESSION CONTROL: the exact upgrade-smoke.sh line as it stood
#      before mg-37d4 is replanted verbatim, and must be caught. This is the
#      "would it have found the real one" question, answered against the real
#      text rather than against a convenient fixture.
#   3. NEGATIVE CONTROLS, and they carry the weight. Prose, grep patterns,
#      comments, documentation and a routed call must NOT fire. Without these,
#      a checker that shouted at every occurrence of the two words would pass
#      case 1 and would be worthless — presence-matching flagged 9 prose lines
#      against 1 real invocation when it was tried, which is what drove the
#      command-position rule.
#   4. Bounded forms are recognised as bounded: an explicit -timeout, a
#      --timeout, a -test.timeout, and a -timeout carried onto the next
#      physical line by a backslash continuation.
#   5. A STALE ALLOWLIST ENTRY is a hard error (exit 2), not a silent no-op —
#      and, in the other direction, an entry naming a file this tree does not
#      have is SKIPPED. Both halves are pinned; the second was a real bug.
#   6. WIRING against the real tree: the real checkout is clean, and test.sh
#      runs the checker. This is what makes the property continuous rather than
#      a sweep somebody did once.
#
# Fixtures are throwaway git checkouts in a temp dir. The real tree is only
# READ (cases 2 and 6), never mutated — planting an unbounded invocation into
# the developer's actual worktree to prove a point is precisely the kind of
# clever verification that leaves debris behind.
#
# WHY THE FIXTURES SAY @GOTEST@ INSTEAD OF THE COMMAND
#   This suite's whole job is to contain unbounded invocations, so writing them
#   literally makes the tree-wide checker report ten findings in its own test
#   file. Excusing those with ten allowlist entries would rebuild the very
#   list-of-files the checker exists to replace — the same mistake, one layer
#   up. So fixture bodies carry a placeholder and plant() substitutes it, which
#   is also the honest description of what this file is: it SYNTHESISES test
#   data, it does not invoke anything.
#
#   That indirection could rot into testing nothing at all, so it is not taken
#   on trust: Test 1 asserts the PLANTED FILE contains the real two-word
#   command. If the substitution ever silently stops happening, that assertion
#   fails rather than the whole suite passing vacuously.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHECKER="${SCRIPT_DIR}/unbounded-go-test.sh"
PASS=0
FAIL=0
GT="go test"

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

# The packaged test isolation (mg-78a5). This suite has a weak-looking claim to
# not needing it — its fixtures are throwaway git repos and its only reads of
# the real tree are Test 2's replant source and Test 9 — but "this one doesn't
# need it" is the exact reasoning that produced mg-6092 / mg-e8e7 / mg-5336 /
# mg-3412, and the adoption ledger is a ratchet that may only shrink. So the
# envelope is adopted rather than argued with.
#
# Ordering matters: SCRIPT_DIR / REPO_ROOT / CHECKER are resolved ABOVE, before
# the move, because after pogo_sandbox_isolate the old $HOME is gone. The
# fixtures live under $POGO_SANDBOX_DIR so pogo_sandbox_down takes them with it.
# shellcheck source=/dev/null
source "$SCRIPT_DIR/pogo-sandbox"
pogo_sandbox_create unboundedgotest
pogo_sandbox_isolate
trap pogo_sandbox_down EXIT

WORK="$POGO_SANDBOX_DIR/work"
mkdir -p "$WORK"

# A fixture checkout. git ls-files is the checker's input, so the fixture has to
# be a real repo with real tracked files.
new_fixture() {
    d="$WORK/$1"
    rm -rf "$d"
    mkdir -p "$d"
    (
        cd "$d"
        git init -q .
        git config user.email t@t
        git config user.name t
    )
    echo "$d"
}

plant() {
    # plant <dir> <relpath>   — body on stdin, @GOTEST@ substituted.
    mkdir -p "$(dirname "$1/$2")"
    sed "s|@GOTEST@|$GT|g" > "$1/$2"
    (cd "$1" && git add -f "$2" >/dev/null)
}

run_checker() {
    set +e
    OUT="$(bash "$CHECKER" --root "$1" 2>&1)"
    STATUS=$?
    set -e
}

# --- Test 1: POSITIVE CONTROL — a planted unbounded invocation FIRES ---------
echo "Test 1: a planted unbounded invocation is caught (positive control)"
FX="$(new_fixture positive)"
plant "$FX" "scripts/newthing.sh" <<'EOF'
#!/bin/bash
set -e
echo "doing a thing"
@GOTEST@ ./internal/agent -run TestSomething -count=1
EOF

# Before anything else: prove the fixture is real. If @GOTEST@ survived
# unsubstituted, every "caught it" below would be measuring nothing.
if grep -qF "$GT ./internal/agent" "$FX/scripts/newthing.sh"; then
    pass "the planted file contains the real command (the placeholder is honest)"
else
    fail "@GOTEST@ was not substituted — this suite would be testing nothing"
fi

run_checker "$FX"

if [ "$STATUS" -eq 1 ]; then
    pass "checker exits 1 on an unbounded invocation"
else
    fail "checker exited $STATUS on a planted unbounded invocation (expected 1)"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

if printf '%s\n' "$OUT" | grep -q 'scripts/newthing.sh:4'; then
    pass "the report names the file AND the line (scripts/newthing.sh:4)"
else
    fail "the report does not name scripts/newthing.sh:4"
fi

if printf '%s\n' "$OUT" | grep -q 'UNBOUNDED'; then
    pass "the report says UNBOUNDED in words"
else
    fail "the report never says UNBOUNDED"
fi

# The remedy has to be in the report. A checker that says "this is wrong"
# without saying what to do sends the reader to the source of the checker.
if printf '%s\n' "$OUT" | grep -q 'go-test-budget.sh'; then
    pass "the report points at the remedy (go-test-budget.sh)"
else
    fail "the report does not name the remedy"
fi

# And the reason the number cannot just be copied — this is the finding the
# ticket was most insistent about, so it is asserted rather than assumed.
if printf '%s\n' "$OUT" | grep -q 'Copying a'; then
    pass "the report warns against copying another site's budget"
else
    fail "the report does not warn against copying a budget"
fi

# --- Test 2: REGRESSION CONTROL — the real pre-fix line, verbatim ------------
echo ""
echo "Test 2: the actual upgrade-smoke.sh:348 line (pre-mg-37d4) is caught"
FX="$(new_fixture regression)"
# Verbatim, as it stood on main before this ticket. If a future refactor of the
# matcher stops catching THIS shape — a command inside $( ) after a `cd &&`,
# wrapped in an `if` — the checker has lost the case it was built for.
plant "$FX" "scripts/upgrade-smoke.sh" <<'EOF'
#!/usr/bin/env bash
say "Phase D"
info "\$ @GOTEST@ ./internal/agent -run TestWorkerRenameFreezesIdentifiers -count=1 -v"
if OUT_D="$(cd "$REPO_ROOT" && @GOTEST@ ./internal/agent -run TestWorkerRenameFreezesIdentifiers -count=1 -v 2>&1)"; then
    pass "D1"
fi
EOF
run_checker "$FX"

if [ "$STATUS" -eq 1 ] && printf '%s\n' "$OUT" | grep -q 'upgrade-smoke.sh:4'; then
    pass "the real pre-fix invocation is caught (\$( ) after 'cd &&', inside an if)"
else
    fail "the real pre-fix upgrade-smoke.sh invocation was NOT caught (exit $STATUS)"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

# The `info "\$ ..."` BANNER on the line above is prose, not an invocation, and
# must not be reported. Reporting it would double every real finding in this
# style of script and train the reader to skim the report.
if printf '%s\n' "$OUT" | grep -q 'upgrade-smoke.sh:3'; then
    fail "the display banner on line 3 was reported as an invocation"
else
    pass "the 'info \"\$ ...\"' display banner is not reported"
fi

# --- Test 3: NEGATIVE CONTROLS — prose must not fire ------------------------
echo ""
echo "Test 3: prose, patterns, comments and docs do NOT fire"
FX="$(new_fixture negative)"
plant "$FX" "scripts/proselike.sh" <<'EOF'
#!/bin/bash
# A comment explaining that @GOTEST@ ./... has no timeout by default.
pass "test.sh carries no unbounded '@GOTEST@'"
echo "Test 10: ci.yml carries no unbounded '@GOTEST@' either"
UNBOUNDED="$(printf '%s\n' "$CODE" | grep -E '(^|[^-[:alnum:]_])@GOTEST@')"
pass "POGO_GO_TEST_TIMEOUT=3s reaches @GOTEST@ (go reports 3s)"
EOF
plant "$FX" "README.md" <<'EOF'
Run `@GOTEST@ ./...` to test the project.
EOF
plant "$FX" "docs/guide.md" <<'EOF'
Then run @GOTEST@ ./... and wait.
EOF
plant "$FX" "test.sh" <<'EOF'
#!/bin/bash
bash scripts/go-test-budget.sh ./...
EOF
run_checker "$FX"

if [ "$STATUS" -eq 0 ]; then
    pass "prose, grep patterns, comments, docs and a routed call are all clean"
else
    fail "a negative control fired (exit $STATUS) — the checker over-matches"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

# --- Test 4: bounded forms are recognised -----------------------------------
echo ""
echo "Test 4: -timeout, --timeout, -test.timeout and a continued line are bounded"
FX="$(new_fixture bounded)"
plant "$FX" "scripts/a.sh" <<'EOF'
#!/bin/bash
@GOTEST@ ./internal/pi/ -v -timeout 300s
EOF
plant "$FX" "scripts/b.sh" <<'EOF'
#!/bin/bash
@GOTEST@ ./... --timeout=5m
EOF
plant "$FX" "scripts/c.sh" <<'EOF'
#!/bin/bash
@GOTEST@ ./... -test.timeout 90s
EOF
plant "$FX" "scripts/d.sh" <<'EOF'
#!/bin/bash
@GOTEST@ ./internal/agent \
    -run TestThing \
    -timeout 2m
EOF
run_checker "$FX"

if [ "$STATUS" -eq 0 ]; then
    pass "all four bounded spellings are accepted"
else
    fail "a bounded invocation was reported as unbounded (exit $STATUS)"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

# The continuation case specifically: assert it by REMOVING the timeout from
# the continued command and watching that one fire. Otherwise "d.sh passed"
# could mean the continuation join works, or could mean d.sh was never scanned.
plant "$FX" "scripts/d.sh" <<'EOF'
#!/bin/bash
@GOTEST@ ./internal/agent \
    -run TestThing \
    -count=1
EOF
run_checker "$FX"
if [ "$STATUS" -eq 1 ] && printf '%s\n' "$OUT" | grep -q 'scripts/d.sh:2'; then
    pass "the continuation-joined command is really scanned (fires with the timeout removed)"
else
    fail "the continued command was not scanned at all (exit $STATUS)"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

# --- Test 5: command-position forms that MUST fire --------------------------
echo ""
echo "Test 5: the other ways a command is actually written"
FX="$(new_fixture positions)"
plant "$FX" ".github/workflows/ci.yml" <<'EOF'
jobs:
  test:
    steps:
      - name: Test
        run: @GOTEST@ ./...
EOF
plant "$FX" "scripts/wrapped.sh" <<'EOF'
#!/bin/bash
time @GOTEST@ ./...
EOF
plant "$FX" "scripts/subshell.sh" <<'EOF'
#!/bin/bash
OUT=$(@GOTEST@ ./...)
EOF
plant "$FX" "scripts/dashc.sh" <<'EOF'
#!/bin/bash
bash -c "@GOTEST@ ./..."
EOF
plant "$FX" "scripts/piped.sh" <<'EOF'
#!/bin/bash
@GOTEST@ ./... | tee log.txt
EOF
run_checker "$FX"

for want in '.github/workflows/ci.yml:5' 'scripts/wrapped.sh:2' 'scripts/subshell.sh:2' 'scripts/dashc.sh:2' 'scripts/piped.sh:2'; do
    if printf '%s\n' "$OUT" | grep -q "$want"; then
        pass "caught: $want"
    else
        fail "MISSED: $want — a real invocation shape is invisible to the checker"
    fi
done

# --- Test 6: a shebang file with no extension is scanned --------------------
echo ""
echo "Test 6: an extensionless file with a sh shebang is scanned"
FX="$(new_fixture shebang)"
plant "$FX" "scripts/driver" <<'EOF'
#!/bin/bash
@GOTEST@ ./...
EOF
plant "$FX" "notes/plain" <<'EOF'
@GOTEST@ ./... is mentioned here but this file is not a script
EOF
run_checker "$FX"
if [ "$STATUS" -eq 1 ] && printf '%s\n' "$OUT" | grep -q 'scripts/driver:2'; then
    pass "scripts/driver (no extension, sh shebang) is scanned"
else
    fail "an extensionless shebang script was not scanned (exit $STATUS)"
fi
if printf '%s\n' "$OUT" | grep -q 'notes/plain'; then
    fail "a non-script text file was scanned"
else
    pass "a non-script text file with no shebang is not scanned"
fi

# --- Test 7: an empty scan is a FAILURE, not a clean bill of health ---------
echo ""
echo "Test 7: a tree with no tracked files is refused, not passed"
# A checker whose input silently became empty reports "everything is bounded"
# and exits 0, which is the most dangerous output it has: green, confident and
# derived from nothing. This is the vacuous-pass guard.
FX="$(new_fixture empty)"
run_checker "$FX"
if [ "$STATUS" -eq 2 ]; then
    pass "an empty tracked-file list exits 2 rather than passing vacuously"
else
    fail "an empty tree exited $STATUS — a vacuous pass is reachable"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

# --- Test 8: a stale allowlist entry is a HARD ERROR ------------------------
echo ""
echo "Test 8: a stale allowlist entry is rejected (exit 2), not silently ignored"
# Build a copy of the checker with an entry naming a file that IS tracked in the
# fixture but whose substring matches nothing in it. The real checker is not
# modified.
STALE_CHECKER="$WORK/stale-checker.sh"
awk '
    /^internal\/refinery\/gatedefaults_test\.go\t/ {
        print "notes.sh\tno-such-substring\tdeliberately stale entry for the test suite"
    }
    { print }
' "$CHECKER" > "$STALE_CHECKER"

FX="$(new_fixture stale)"
plant "$FX" "notes.sh" <<'EOF'
#!/bin/bash
echo hello
EOF
set +e
STALE_OUT="$(bash "$STALE_CHECKER" --root "$FX" 2>&1)"
STALE_STATUS=$?
set -e

if [ "$STALE_STATUS" -eq 2 ]; then
    pass "a stale allowlist entry exits 2"
else
    fail "a stale allowlist entry exited $STALE_STATUS (expected 2)"
    printf '%s\n' "$STALE_OUT" | sed 's/^/      | /'
fi
if printf '%s\n' "$STALE_OUT" | grep -q 'notes.sh'; then
    pass "the stale entry is named in the error"
else
    fail "the stale entry is not named"
fi

# The other half of the contract, and the one the positive control actually
# caught. An entry naming a file this tree does not contain must be SKIPPED.
# Before this, the real allowlist (which names pogo's own files) made the
# checker exit 2 against every other checkout — the anti-rot guard failing in
# exactly the way it exists to prevent.
FX="$(new_fixture stale_foreign)"
plant "$FX" "scripts/other.sh" <<'EOF'
#!/bin/bash
echo unrelated
EOF
set +e
FOREIGN_OUT="$(bash "$CHECKER" --root "$FX" 2>&1)"
FOREIGN_STATUS=$?
set -e
if [ "$FOREIGN_STATUS" -eq 0 ]; then
    pass "an allowlist entry for a file absent from this tree is skipped, not failed"
else
    fail "the checker exited $FOREIGN_STATUS on a foreign tree — the allowlist is not portable"
    printf '%s\n' "$FOREIGN_OUT" | sed 's/^/      | /'
fi

# --- Test 9: WIRING against the real tree -----------------------------------
echo ""
echo "Test 9: the real tree is clean and test.sh runs the checker"
run_checker "$REPO_ROOT"
if [ "$STATUS" -eq 0 ]; then
    pass "the real checkout has no unbounded invocation"
else
    fail "the real checkout has an unbounded invocation (exit $STATUS)"
    printf '%s\n' "$OUT" | sed 's/^/      | /'
fi

# The checker only makes the property CONTINUOUS if something runs it. Pinned
# against the real test.sh, because a checker nobody runs is the same defect
# wearing a check's clothes.
TEST_SH_CODE="$(grep -vE '^[[:space:]]*#' "$REPO_ROOT/test.sh")"
if printf '%s\n' "$TEST_SH_CODE" | grep -q 'unbounded-go-test\.sh'; then
    pass "test.sh runs scripts/unbounded-go-test.sh"
else
    fail "test.sh does not run the checker — the sweep is a one-off, not a gate"
fi

# And the site this ticket was filed for, asserted by name: upgrade-smoke.sh
# must route through the budget script. The checker above proves it is bounded;
# this proves it is bounded the way that also REPORTS, which is the half of
# gh#107 that a bare -timeout does not buy.
SMOKE_CODE="$(grep -vE '^[[:space:]]*#' "$REPO_ROOT/scripts/upgrade-smoke.sh")"
if printf '%s\n' "$SMOKE_CODE" | grep -q 'go-test-budget\.sh'; then
    pass "upgrade-smoke.sh routes Phase D through go-test-budget.sh"
else
    fail "upgrade-smoke.sh does not route through go-test-budget.sh"
fi

# --- Summary ---------------------------------------------------------------
echo ""
echo "=== unbounded-go-test.sh: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
