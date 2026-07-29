#!/bin/bash
# Tests for scripts/changelog-coverage.sh (mg-7904).
#
# The contract under test:
#   1. POSITIVE CONTROL FIRST: on a range containing a feat:/fix: commit whose
#      mg-id has no changelog entry, the check EXITS NON-ZERO and NAMES the id.
#      This is the whole point of the ticket. The guard it replaces
#      (assemble-changelog.sh's LOUD-EMPTY check) passes at 54% fragment
#      coverage, so "it passed" is worth nothing until "it can fail" is shown.
#   2. Adding the fragment for that same id flips the same range to exit 0.
#   3. A hand-written [Unreleased] entry counts as described (the assembler
#      preserves those, so they do ship) — but is reported in its OWN bucket,
#      not folded into the fragment count.
#   4. Population is feat:/fix: only — docs:/chore:/test: commits are not in it.
#   5. An id described in an earlier release section counts as described, so a
#      range spanning a release does not report false gaps.
#   6. --json emits the population, the buckets, and the undescribed ids.
#   7. bump-version.sh REFUSES to cut when ids are undescribed, and proceeds
#      only with --ack-changelog-gaps.
#
# Cases 1-6 run the real changelog-coverage.sh against synthetic git repos in
# temp dirs via CHANGELOG_COVERAGE_REPO / _FILE / _DIR, so the repo's own
# CHANGELOG.md and changelog.d/ are never read or written.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
COVERAGE="${SCRIPT_DIR}/changelog-coverage.sh"
BUMP="${SCRIPT_DIR}/bump-version.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== changelog-coverage.sh tests ==="

# --- Test 1: syntax --------------------------------------------------------
echo ""
echo "Test 1: Script syntax check"
if bash -n "$COVERAGE" 2>/dev/null; then
    pass "changelog-coverage.sh has valid bash syntax"
else
    fail "changelog-coverage.sh has syntax errors"
fi

# Fixture builder: a git repo with commits, a CHANGELOG, and a fragment dir.
# Usage: make_repo <dir>; then commit_subject <dir> <subject>
make_repo() {
    local d=$1
    mkdir -p "$d/changelog.d"
    git -C "$d" init -q 2>/dev/null
    git -C "$d" config user.email "test@example.com"
    git -C "$d" config user.name "Test"
    git -C "$d" config commit.gpgsign false
    cat > "$d/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.5.0] - 2026-07-10

### Added

- prior release entry (mg-0000).
EOF
    echo seed > "$d/seed.txt"
    git -C "$d" add -A
    git -C "$d" commit -qm "chore: seed"
}

commit_subject() {
    local d=$1 subj=$2
    echo "$RANDOM$subj" >> "$d/work.txt"
    git -C "$d" add -A
    git -C "$d" commit -qm "$subj"
}

run_coverage() {
    local d=$1; shift
    CHANGELOG_COVERAGE_REPO="$d" \
    CHANGELOG_COVERAGE_FILE="$d/CHANGELOG.md" \
    CHANGELOG_COVERAGE_DIR="$d/changelog.d" \
    bash "$COVERAGE" "$@"
}

# --- Test 2: POSITIVE CONTROL — the check must FAIL on a known gap ---------
# Written and run BEFORE any passing case. A guard that has never been seen to
# fail is not evidence of anything.
echo ""
echo "Test 2: POSITIVE CONTROL — a feat: commit with no fragment FAILS the check"
T="$(mktemp -d)"
make_repo "$T"
commit_subject "$T" "feat(thing): a shipped change nobody described (mg-aaaa)"
set +e
out="$(run_coverage "$T" 2>&1)"
status=$?
set -e
if [ "$status" -eq 1 ]; then
    pass "exits 1 on a range with a known-missing fragment (the RED)"
else
    fail "expected exit 1 on a known gap, got $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'mg-aaaa'; then
    pass "names the undescribed id (mg-aaaa) in its output"
else
    fail "did not name mg-aaaa; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'population.*: 1'; then
    pass "reports the population beside the number, not a bare percentage"
else
    fail "did not report a named population of 1"
fi

# --- Test 3: adding the fragment flips the SAME range to green -------------
echo ""
echo "Test 3: adding the missing fragment flips the same range to exit 0"
printf -- '- described at last (mg-aaaa).\n' > "$T/changelog.d/mg-aaaa.fixed.md"
set +e
out="$(run_coverage "$T" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "exits 0 once the fragment exists (the GREEN)"
else
    fail "expected exit 0 after adding the fragment, got $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'fragment: *1'; then
    pass "counts it in the fragment bucket"
else
    fail "fragment bucket did not report 1"
fi
rm -rf "$T"

# --- Test 4: hand-written [Unreleased] entry counts, in its own bucket ------
echo ""
echo "Test 4: a hand-written [Unreleased] entry counts as described, separately"
T="$(mktemp -d)"
make_repo "$T"
commit_subject "$T" "fix(x): described by hand, no fragment (mg-bbbb)"
# Hand-write it into [Unreleased], the transition-era path the assembler keeps.
awk '/^## \[Unreleased\]/{print; print ""; print "### Fixed"; print ""; print "- hand-written entry (mg-bbbb)."; next} {print}' \
    "$T/CHANGELOG.md" > "$T/c.tmp" && mv "$T/c.tmp" "$T/CHANGELOG.md"
set +e
out="$(run_coverage "$T" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "hand-written [Unreleased] entry counts as described"
else
    fail "expected exit 0 for a hand-written entry, got $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'hand-written \[Unreleased\] entry: *1' \
   && echo "$out" | grep -q 'fragment: *0'; then
    pass "reported in the [Unreleased] bucket, NOT folded into the fragment count"
else
    fail "buckets wrong; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 5: population is feat:/fix: only ---------------------------------
echo ""
echo "Test 5: docs:/chore:/test: commits are not in the population"
T="$(mktemp -d)"
make_repo "$T"
commit_subject "$T" "docs: explain a thing (mg-cccc)"
commit_subject "$T" "chore: tidy up (mg-dddd)"
commit_subject "$T" "test(x): add coverage (mg-eeee)"
set +e
out="$(run_coverage "$T" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'population.*: 0'; then
    pass "non-feat/fix commits excluded; population 0, exit 0"
else
    fail "expected empty population and exit 0, got $status"
    echo "$out" | sed 's/^/      /' >&2
fi
# A feat: among them must still be caught.
commit_subject "$T" "feat(y): this one counts (mg-ffff)"
set +e
out="$(run_coverage "$T" 2>&1)"
status=$?
set -e
if [ "$status" -eq 1 ] && echo "$out" | grep -q 'mg-ffff' \
   && ! echo "$out" | grep -q 'mg-cccc'; then
    pass "the feat: id is caught while the docs:/chore: ids stay out"
else
    fail "population filter wrong; output was:"
    echo "$out" | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 6: an id in an earlier release section is described --------------
echo ""
echo "Test 6: an id already in a released section does not report as a gap"
T="$(mktemp -d)"
make_repo "$T"
commit_subject "$T" "fix(z): shipped in 0.5.0 already (mg-0000)"
set +e
out="$(run_coverage "$T" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'earlier release section: *1'; then
    pass "counted in the earlier-release bucket, no false gap"
else
    fail "expected exit 0 with an earlier-release count, got $status"
    echo "$out" | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 7: --json shape --------------------------------------------------
echo ""
echo "Test 7: --json reports population, buckets, and the undescribed ids"
T="$(mktemp -d)"
make_repo "$T"
commit_subject "$T" "feat(a): undescribed (mg-1234)"
printf -- '- described (mg-5678).\n' > "$T/changelog.d/mg-5678.added.md"
commit_subject "$T" "fix(b): described (mg-5678)"
set +e
js="$(run_coverage "$T" --json 2>&1)"
set -e
ok=true
echo "$js" | grep -q '"population": 2'            || ok=false
echo "$js" | grep -q '"described_by_fragment": 1' || ok=false
echo "$js" | grep -q '"undescribed": 1'           || ok=false
echo "$js" | grep -q '"mg-1234"'                  || ok=false
if [ "$ok" = true ]; then
    pass "json carries population, buckets, and undescribed ids"
else
    fail "json shape wrong; output was:"
    echo "$js" | sed 's/^/      /' >&2
fi
if command -v jq >/dev/null 2>&1; then
    if echo "$js" | jq -e . >/dev/null 2>&1; then
        pass "json output parses as valid JSON"
    else
        fail "json output is not valid JSON"
    fi
else
    echo "  SKIP: jq not available for JSON parse check"
fi
rm -rf "$T"

# --- Test 8: bump-version.sh gates the cut on coverage ---------------------
# The check only matters if it is WIRED IN. Drive the real bump-version.sh in a
# synthetic repo: it must refuse, then proceed under the explicit ack.
echo ""
echo "Test 8: bump-version.sh refuses to cut with gaps, proceeds with the ack"
T="$(mktemp -d)"
make_repo "$T"
mkdir -p "$T/internal/version" "$T/scripts/lib"
printf 'package version\n\nconst Version = "0.5.0"\n' > "$T/internal/version/version.go"
cp "$COVERAGE" "$BUMP" "$SCRIPT_DIR/assemble-changelog.sh" "$T/scripts/"
cp "$SCRIPT_DIR/lib/common.sh" "$T/scripts/lib/"
# One described change so assembly has something to emit — otherwise the
# separate LOUD-EMPTY guard fires and we would not be testing THIS gate.
printf -- '- a described change (mg-8888).\n' > "$T/changelog.d/mg-8888.added.md"
git -C "$T" add -A && git -C "$T" commit -qm "chore: scaffold"
commit_subject "$T" "fix(p): described change (mg-8888)"
commit_subject "$T" "feat(q): undescribed change (mg-9999)"

set +e
out="$( cd "$T" && bash scripts/bump-version.sh 0.6.0 2>&1 )"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q 'mg-9999'; then
    pass "bump-version.sh REFUSES the cut and names the undescribed id"
else
    fail "expected a refused cut naming mg-9999, got exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if grep -q '0.5.0' "$T/internal/version/version.go"; then
    pass "version.go left untouched by the refused cut"
else
    fail "version.go was modified despite the refusal"
fi

set +e
out="$( cd "$T" && bash scripts/bump-version.sh 0.6.0 --ack-changelog-gaps 2>&1 )"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "proceeds under --ack-changelog-gaps"
else
    fail "expected the ack to allow the cut, got exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -qi 'Proceeding with known changelog gaps'; then
    pass "the ack path still says the release ships with gaps"
else
    fail "the ack path did not warn about the gaps"
fi
rm -rf "$T"

# --- Test 9: the real repo, reported not asserted --------------------------
# Requirement from the ticket: report the coverage number with the population
# named beside it. Deliberately NOT an assertion on the gap count — that number
# moves as fragments land, and this suite must not have to be edited each time.
echo ""
echo "Test 9: real-repo coverage (reported, not asserted)"
set +e
real="$(cd "$REPO_ROOT" && bash "$COVERAGE" --quiet 2>&1)"
status=$?
set -e
echo "$real" | sed 's/^/      /'
if [ "$status" -eq 0 ] || [ "$status" -eq 1 ]; then
    pass "runs cleanly against the real repo (exit $status)"
else
    fail "crashed on the real repo (exit $status)"
fi

# --- Summary ---------------------------------------------------------------
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
