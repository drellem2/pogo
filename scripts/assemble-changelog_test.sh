#!/bin/bash
# Tests for scripts/assemble-changelog.sh (mg-d917).
#
# The contract under test:
#   1. Fragments in changelog.d/ are bucketed by category into "## [Unreleased]"
#      in canonical Keep-a-Changelog order, and consumed (deleted) afterward.
#   2. Existing hand-written [Unreleased] entries are PRESERVED and merged with
#      fragment bullets in the same category (the transition period).
#   3. README.md in changelog.d/ is never treated as a fragment.
#   4. An uncategorized fragment defaults to "Changed".
#   5. LOUD-EMPTY: no fragments + empty [Unreleased] => non-zero exit, file
#      untouched. A check that cannot fail is worthless.
#   6. PROOF (the RED this ticket exists to kill): two branches from one tip,
#      each adding its own fragment, merge CLEAN with no rebase — while the old
#      shared-tail append on the same tip DOES conflict.
#
# Every case runs the real assemble-changelog.sh against fixtures in a temp dir
# via the CHANGELOG_ASSEMBLE_FILE / CHANGELOG_ASSEMBLE_DIR overrides, so the
# repo's own CHANGELOG.md and changelog.d/ are never touched.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ASSEMBLE="${SCRIPT_DIR}/assemble-changelog.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== assemble-changelog.sh tests ==="

# --- Test 1: syntax --------------------------------------------------------
echo ""
echo "Test 1: Script syntax check"
if bash -n "$ASSEMBLE" 2>/dev/null; then
    pass "assemble-changelog.sh has valid bash syntax"
else
    fail "assemble-changelog.sh has syntax errors"
fi

# Fixture builder: an empty [Unreleased] over a prior release.
make_changelog() {
    cat > "$1" <<'EOF'
# Changelog

## [Unreleased]

## [0.5.0] - 2026-07-10

### Added

- prior release entry (mg-0000).
EOF
}

# --- Test 2: bucketing + ordering + consumption ----------------------------
echo ""
echo "Test 2: fragments bucket by category, in canonical order, and are consumed"
T="$(mktemp -d)"; mkdir -p "$T/d"
make_changelog "$T/CHANGELOG.md"
printf -- '- fixed thing (mg-1111).\n'   > "$T/d/mg-1111.fixed.md"
printf -- '- added thing (mg-2222).\n'   > "$T/d/mg-2222.added.md"
printf -- '- secured thing (mg-3333).\n' > "$T/d/mg-3333.security.md"
CHANGELOG_ASSEMBLE_FILE="$T/CHANGELOG.md" CHANGELOG_ASSEMBLE_DIR="$T/d" bash "$ASSEMBLE" >/dev/null
# Added must appear before Fixed must appear before Security (canonical order).
order="$(grep -n '^### ' "$T/CHANGELOG.md" | grep -E 'Added|Fixed|Security' | head -3 | sed 's/.*### //' | tr '\n' ',')"
if [ "$order" = "Added,Fixed,Security," ]; then
    pass "sections emitted in canonical order (got: $order)"
else
    fail "wrong section order (got: $order)"
fi
if grep -q 'added thing (mg-2222)' "$T/CHANGELOG.md" \
   && grep -q 'fixed thing (mg-1111)' "$T/CHANGELOG.md" \
   && grep -q 'secured thing (mg-3333)' "$T/CHANGELOG.md"; then
    pass "all fragment bodies present in output"
else
    fail "a fragment body is missing from output"
fi
if [ -z "$(find "$T/d" -type f -name '*.md')" ]; then
    pass "consumed fragments were removed"
else
    fail "fragments were not removed: $(find "$T/d" -type f)"
fi
# Prior release section untouched.
if grep -q 'prior release entry (mg-0000)' "$T/CHANGELOG.md"; then
    pass "prior release history left intact"
else
    fail "prior release history was altered"
fi
rm -rf "$T"

# --- Test 3: merge with existing hand-written Unreleased (transition) -------
echo ""
echo "Test 3: existing [Unreleased] entries preserved and merged by category"
T="$(mktemp -d)"; mkdir -p "$T/d"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Fixed

- hand-written fix (mg-9999).

## [0.5.0] - 2026-07-10

### Added

- prior (mg-0000).
EOF
printf -- '- fragment fix (mg-1111).\n' > "$T/d/mg-1111.fixed.md"
CHANGELOG_ASSEMBLE_FILE="$T/CHANGELOG.md" CHANGELOG_ASSEMBLE_DIR="$T/d" bash "$ASSEMBLE" >/dev/null
# Both the hand-written and fragment bullet must sit under a single Fixed header.
fixed_headers="$(awk '/^## \[Unreleased\]/{u=1;next} u&&/^## \[/{u=0} u&&/^### Fixed/{c++} END{print c+0}' "$T/CHANGELOG.md")"
if [ "$fixed_headers" = "1" ] \
   && grep -q 'hand-written fix (mg-9999)' "$T/CHANGELOG.md" \
   && grep -q 'fragment fix (mg-1111)' "$T/CHANGELOG.md"; then
    pass "hand-written + fragment merged under one ### Fixed"
else
    fail "transition merge wrong (Fixed headers=$fixed_headers)"
fi
rm -rf "$T"

# --- Test 4: README skipped, uncategorized defaults to Changed --------------
echo ""
echo "Test 4: README.md skipped; uncategorized fragment defaults to Changed"
T="$(mktemp -d)"; mkdir -p "$T/d"
make_changelog "$T/CHANGELOG.md"
printf 'This directory holds changelog fragments.\n' > "$T/d/README.md"
printf -- '- uncategorized entry (mg-4444).\n'        > "$T/d/mg-4444.md"
CHANGELOG_ASSEMBLE_FILE="$T/CHANGELOG.md" CHANGELOG_ASSEMBLE_DIR="$T/d" bash "$ASSEMBLE" >/dev/null 2>&1
if grep -q 'uncategorized entry (mg-4444)' "$T/CHANGELOG.md" \
   && awk '/^## \[Unreleased\]/{u=1;next} u&&/^## \[/{u=0} u&&/^### Changed/{f=1} u&&f&&/mg-4444/{print;exit}' "$T/CHANGELOG.md" | grep -q 'mg-4444'; then
    pass "uncategorized fragment landed under ### Changed"
else
    fail "uncategorized fragment not under Changed"
fi
if [ -f "$T/d/README.md" ]; then
    pass "README.md was not consumed"
else
    fail "README.md was wrongly deleted"
fi
if ! grep -q 'This directory holds changelog fragments' "$T/CHANGELOG.md"; then
    pass "README.md content did not leak into CHANGELOG"
else
    fail "README.md content leaked into CHANGELOG"
fi
rm -rf "$T"

# --- Test 5: LOUD-EMPTY guard ----------------------------------------------
echo ""
echo "Test 5: empty changelog.d + empty [Unreleased] => loud non-zero exit"
T="$(mktemp -d)"; mkdir -p "$T/d"
make_changelog "$T/CHANGELOG.md"
before="$(md5 -q "$T/CHANGELOG.md" 2>/dev/null || md5sum "$T/CHANGELOG.md" | awk '{print $1}')"
set +e
CHANGELOG_ASSEMBLE_FILE="$T/CHANGELOG.md" CHANGELOG_ASSEMBLE_DIR="$T/d" bash "$ASSEMBLE" >/dev/null 2>&1
rc=$?
set -e
after="$(md5 -q "$T/CHANGELOG.md" 2>/dev/null || md5sum "$T/CHANGELOG.md" | awk '{print $1}')"
if [ "$rc" -ne 0 ]; then
    pass "assembler exited non-zero on empty release"
else
    fail "assembler exited 0 on empty release (silent empty changelog!)"
fi
if [ "$before" = "$after" ]; then
    pass "CHANGELOG.md left untouched on loud-empty abort"
else
    fail "CHANGELOG.md was mutated despite loud-empty abort"
fi
rm -rf "$T"

# --- Test 6: THE PROOF — two branches from one tip merge clean -------------
# This is the RED mg-7e0c and mg-738f hit. We reproduce BOTH the fix (clean)
# and the old mechanism (conflict) from the same tip, in a throwaway git repo.
echo ""
echo "Test 6: PROOF — concurrent fragment adds merge clean; shared-tail collides"
T="$(mktemp -d)"
(
    cd "$T"
    git init -q
    git config user.email t@t; git config user.name t
    git config commit.gpgsign false
    mkdir changelog.d
    printf '# Changelog\n\n## [Unreleased]\n\n## [0.5.0] - 2026-07-10\n\n### Added\n\n- prior (mg-0000).\n' > CHANGELOG.md
    printf 'fragments here\n' > changelog.d/README.md
    git add -A && git commit -qm tip
    TIP="$(git rev-parse HEAD)"

    # --- FIX: each branch writes its OWN fragment file ---
    git checkout -q -b cat-a "$TIP"
    printf -- '- work from polecat A (mg-aaaa).\n' > changelog.d/mg-aaaa.added.md
    git add -A && git commit -qm A

    git checkout -q -b cat-b "$TIP"
    printf -- '- work from polecat B (mg-bbbb).\n' > changelog.d/mg-bbbb.fixed.md
    git add -A && git commit -qm B

    # Merge B's branch into A's WITHOUT rebasing — this is what the refinery does.
    git checkout -q cat-a
    if git merge --no-edit cat-b >/dev/null 2>&1; then
        echo "CLEAN"
    else
        git merge --abort 2>/dev/null || true
        echo "CONFLICT"
    fi
) > "$T/fix_result" 2>/dev/null
if [ "$(cat "$T/fix_result")" = "CLEAN" ]; then
    pass "two concurrent fragment adds merged with NO conflict (class eliminated)"
else
    fail "fragment adds still conflicted: $(cat "$T/fix_result")"
fi

# Counter-example: the OLD shared-tail append, same tip, MUST conflict — this
# proves the test can distinguish the fix from the bug (a green that can go red).
T2="$(mktemp -d)"
(
    cd "$T2"
    git init -q
    git config user.email t@t; git config user.name t
    git config commit.gpgsign false
    printf '# Changelog\n\n## [Unreleased]\n\n### Added\n\n- prior (mg-0000).\n' > CHANGELOG.md
    git add -A && git commit -qm tip
    TIP="$(git rev-parse HEAD)"

    git checkout -q -b tail-a "$TIP"
    printf -- '- work from polecat A (mg-aaaa).\n' >> CHANGELOG.md
    git add -A && git commit -qm A

    git checkout -q -b tail-b "$TIP"
    printf -- '- work from polecat B (mg-bbbb).\n' >> CHANGELOG.md
    git add -A && git commit -qm B

    git checkout -q tail-a
    if git merge --no-edit tail-b >/dev/null 2>&1; then
        echo "CLEAN"
    else
        git merge --abort 2>/dev/null || true
        echo "CONFLICT"
    fi
) > "$T2/tail_result" 2>/dev/null
if [ "$(cat "$T2/tail_result")" = "CONFLICT" ]; then
    pass "old shared-tail append still conflicts (control: the RED is real)"
else
    fail "shared-tail control did NOT conflict — test proves nothing (got: $(cat "$T2/tail_result"))"
fi
rm -rf "$T" "$T2"

# --- Summary ---------------------------------------------------------------
echo ""
echo "=== assemble-changelog.sh: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
