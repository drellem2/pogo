#!/bin/bash
# =============================================================================
# Tests for scripts/lib/version.sh, scripts/check-version.sh, and the version
# read/write in scripts/bump-version.sh (mg-cb8dc).
# =============================================================================
#
# The defect: both scripts read the version with `grep 'Version = '`, a TEXT
# match. A comment in version.go quoting that string (mg-3141) made it return
# three lines, and the v0.11.0 cut aborted with "sed: unterminated substitute
# pattern" (mg-3225). check-version.sh had the same defect and nobody saw it,
# because its CI job was `if: false`.
#
# Every fixture below that is meant to exercise the fix carries comments that
# quote the pattern verbatim, and the first case is a POSITIVE CONTROL showing
# that the old grep really does return more than one line on that fixture — a
# fixture the old code handled fine would prove nothing about the new code.
# =============================================================================

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# bump-version.sh runs git and changelog-coverage; keep them off the
# developer's real $HOME (same isolation as roll-changelog_test.sh).
source "$HERE/pogo-sandbox"
pogo_sandbox_create checkversion
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

source "$HERE/lib/version.sh"

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

# A version.go whose comments quote the pattern, including one that spells the
# whole declaration. $1 = file, $2 = declared version.
write_quoting_fixture() {
    cat > "$1" <<EOF
package version

// Read by grepping for 'Version = ' — this line quotes it (mg-3141).
// var Version = "0.0.1" was the first release.
/* Version = "9.9.9" inside a block comment */

// Version is set by goreleaser ldflags or bump-version.sh
var Version = "$2"

var Build = ""
EOF
}

# A throwaway repo laid out like pogo: internal/version/version.go plus the
# scripts check-version.sh needs. $1 = dir, $2 = declared version.
make_repo() {
    local d=$1
    mkdir -p "$d/internal/version" "$d/scripts/lib"
    cp "$HERE/check-version.sh" "$d/scripts/"
    cp "$HERE/lib/version.sh" "$d/scripts/lib/"
    write_quoting_fixture "$d/internal/version/version.go" "$2"
    git -C "$d" init -q -b main 2>/dev/null || git -C "$d" init -q
    git -C "$d" config user.email "test@example.com"
    git -C "$d" config user.name "Test"
    git -C "$d" config commit.gpgsign false
    git -C "$d" config tag.gpgsign false
    git -C "$d" add -A
    git -C "$d" commit -qm "chore: scaffold"
}

run_check() { # $1 = repo, rest = args; sets out and status
    set +e
    out="$(bash "$1/scripts/check-version.sh" "${@:2}" 2>&1)"
    status=$?
    set -e
}

echo "=== check-version.sh / lib/version.sh tests ==="

T="$(mktemp -d)"
trap 'rm -rf "$T"; pogo_sandbox_down' EXIT

# --- Test 1: positive control — the fixture breaks the OLD read -------------
echo ""
echo "Test 1: positive control: the old text grep matches more than one line"
F="$T/version.go"
write_quoting_fixture "$F" "0.5.0"
old_lines="$(grep -c 'Version = ' "$F" || true)"
if [ "$old_lines" -gt 1 ]; then
    pass "old grep 'Version = ' matches $old_lines lines on the fixture"
else
    fail "fixture is not load-bearing: old grep matched $old_lines line(s)"
fi

# --- Test 2: read_version finds exactly the declaration --------------------
echo ""
echo "Test 2: read_version reads the one declaration despite the comments"
got="$(read_version "$F")"
if [ "$got" = "0.5.0" ]; then
    pass "read_version -> 0.5.0"
else
    fail "read_version returned '$got'"
fi
if [ "$(printf '%s\n' "$got" | wc -l | tr -d ' ')" = 1 ]; then
    pass "exactly one line"
else
    fail "read_version returned more than one line"
fi
printf 'package version\n\nconst Version = "0.4.0"\n' > "$T/const.go"
if [ "$(read_version "$T/const.go")" = "0.4.0" ]; then
    pass "a const declaration is read too"
else
    fail "const Version was not read"
fi

# --- Test 3: read_version refuses zero or two declarations -----------------
echo ""
echo "Test 3: read_version refuses a missing or duplicated declaration"
printf 'package version\n\n// Version = "1.0.0"\n' > "$T/none.go"
if ! read_version "$T/none.go" >/dev/null 2>&1; then
    pass "refuses a file with only a comment quoting the pattern"
else
    fail "accepted a file with no declaration"
fi
cp "$F" "$T/two.go"
printf 'var Version = "0.6.0"\n' >> "$T/two.go"
set +e
msg="$(read_version "$T/two.go" 2>&1 >/dev/null)"; st=$?
set -e
if [ "$st" -ne 0 ] && echo "$msg" | grep -q 'found 2'; then
    pass "refuses two declarations and says how many it found"
else
    fail "two declarations: exit $st, message: $msg"
fi

# --- Test 4: write_version rewrites the declaration only -------------------
echo ""
echo "Test 4: write_version leaves comments quoting the old value alone"
cp "$F" "$T/w.go"
chmod 644 "$T/w.go"
write_quoting_fixture "$T/expect.go" "0.6.0"
write_version "$T/w.go" "0.6.0"
if diff -u "$T/expect.go" "$T/w.go" >/dev/null; then
    pass "only the declaration changed"
else
    fail "write_version changed more than the declaration"
    diff -u "$T/expect.go" "$T/w.go" | sed 's/^/      /' >&2 || true
fi
# GNU first: GNU `stat -f` means filesystem stat and prints before failing.
mode="$(stat -c '%a' "$T/w.go" 2>/dev/null || stat -f '%Lp' "$T/w.go")"
if [ "$mode" = 644 ]; then
    pass "file mode preserved (644)"
else
    fail "file mode changed to $mode"
fi
if ! write_version "$T/two.go" "0.7.0" 2>/dev/null && grep -q '"0.6.0"' "$T/two.go"; then
    pass "refuses to write a file with two declarations, and leaves it alone"
else
    fail "wrote through an ambiguous file"
fi

# --- Test 5: check-version.sh on a repo whose comments quote the pattern ---
echo ""
echo "Test 5: check-version.sh passes on main between cuts (version == newest tag)"
R="$T/repo"
make_repo "$R" "0.10.0"
git -C "$R" tag v0.9.0
git -C "$R" tag -a v0.10.0 -m "Release v0.10.0"
run_check "$R"
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'Current version: 0.10.0$'; then
    pass "passes, reading exactly 0.10.0 past the quoting comments"
else
    fail "expected a pass at 0.10.0, got exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'newest release tag v0.10.0'; then
    pass "compares by version order (0.10.0 is newer than 0.9.0)"
else
    fail "did not pick v0.10.0 as the newest tag"
    echo "$out" | sed 's/^/      /' >&2
fi

# --- Test 6: check-version.sh fails when behind the newest tag -------------
echo ""
echo "Test 6: check-version.sh fails when the release bump was not back-ported"
write_version "$R/internal/version/version.go" "0.9.0"
run_check "$R"
if [ "$status" -ne 0 ] && echo "$out" | grep -q 'v0.10.0 is already released'; then
    pass "0.9.0 behind v0.10.0 fails (not fooled by string order)"
else
    fail "expected a failure at 0.9.0 behind v0.10.0, got exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
write_version "$R/internal/version/version.go" "0.11.0"
run_check "$R"
if [ "$status" -eq 0 ]; then
    pass "an unreleased version ahead of the newest tag passes"
else
    fail "0.11.0 ahead of v0.10.0 failed"
    echo "$out" | sed 's/^/      /' >&2
fi

# --- Test 7: --require-untagged keeps the old duplicate-tag rule ------------
echo ""
echo "Test 7: --require-untagged refuses a version that is already tagged"
run_check "$R" --require-untagged
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'No existing tag for v0.11.0'; then
    pass "untagged 0.11.0 passes"
else
    fail "untagged 0.11.0: exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
write_version "$R/internal/version/version.go" "0.10.0"
run_check "$R" --require-untagged
if [ "$status" -ne 0 ] && echo "$out" | grep -q 'Tag v0.10.0 already exists'; then
    pass "tagged 0.10.0 is refused"
else
    fail "tagged 0.10.0 under --require-untagged: exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi

# --- Test 8: check-version.sh refuses an ambiguous version.go --------------
echo ""
echo "Test 8: check-version.sh fails on two declarations or a non-semver value"
printf 'var Version = "0.12.0"\n' >> "$R/internal/version/version.go"
run_check "$R"
if [ "$status" -ne 0 ] && echo "$out" | grep -q 'found 2'; then
    pass "two declarations fail"
else
    fail "two declarations: exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
write_quoting_fixture "$R/internal/version/version.go" "dev"
run_check "$R"
if [ "$status" -ne 0 ] && echo "$out" | grep -q 'not MAJOR.MINOR.PATCH'; then
    pass "a non-semver value fails"
else
    fail "non-semver value: exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi

# --- Test 9: the real bump-version.sh, end to end, with quoting comments ---
# This is the v0.11.0 cut's failure (mg-3225): the same fixture made the old
# script abort with "sed: unterminated substitute pattern".
echo ""
echo "Test 9: bump-version.sh cuts a release from a version.go that quotes the pattern"
B="$T/bump"
mkdir -p "$B/internal/version" "$B/scripts/lib" "$B/changelog.d"
write_quoting_fixture "$B/internal/version/version.go" "0.5.0"
cp "$HERE/bump-version.sh" "$HERE/roll-changelog.sh" "$HERE/changelog-links.sh" \
   "$HERE/assemble-changelog.sh" "$HERE/changelog-coverage.sh" "$B/scripts/"
cp "$HERE/lib/common.sh" "$HERE/lib/version.sh" "$B/scripts/lib/"
cat > "$B/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.5.0] - 2026-07-10

### Added

- prior release entry (mg-0000).

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
printf -- '- a described change (mg-8888).\n' > "$B/changelog.d/mg-8888.added.md"
git -C "$B" init -q -b main 2>/dev/null || git -C "$B" init -q
git -C "$B" config user.email "test@example.com"
git -C "$B" config user.name "Test"
git -C "$B" config commit.gpgsign false
git -C "$B" add -A
git -C "$B" commit -qm "chore: scaffold"
git -C "$B" checkout -q -b release/v0.6.0
set +e
out="$( cd "$B" && bash scripts/bump-version.sh 0.6.0 --commit 2>&1 )"
status=$?
set -e
if [ "$status" -eq 0 ] && echo "$out" | grep -q 'Version matches: 0.6.0'; then
    pass "bump-version.sh completes and verifies 0.6.0"
else
    fail "bump-version.sh failed (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
write_quoting_fixture "$T/bump-expect.go" "0.6.0"
if diff -u "$T/bump-expect.go" "$B/internal/version/version.go" >/dev/null; then
    pass "the declaration moved to 0.6.0 and every quoting comment is untouched"
else
    fail "bump-version.sh changed version.go beyond the declaration"
    diff -u "$T/bump-expect.go" "$B/internal/version/version.go" | sed 's/^/      /' >&2 || true
fi
if echo "$out" | grep -q 'Bumping version: 0.5.0 → 0.6.0'; then
    pass "read the current version as exactly 0.5.0"
else
    fail "did not read the current version as 0.5.0"
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
