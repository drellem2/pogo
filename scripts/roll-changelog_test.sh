#!/bin/bash
# Tests for scripts/roll-changelog.sh and scripts/changelog-links.sh (mg-cef7).
#
# The contract under test:
#   1. Rolling [Unreleased] → [X.Y.Z] emits the `[X.Y.Z]:` compare link AND
#      re-points `[Unreleased]:` at the new tag. A heading without its link
#      reference renders as LITERAL TEXT in the published changelog.
#   2. REGRESSION (the RED this ticket exists to kill): the old unanchored sed
#      also rewrote `## [Unreleased]` where it appears in an entry's PROSE, so
#      each cut injected a spurious heading and the count rose by TWO per cut
#      (measured 9 → 11 → 13 → 15 across v0.6.0/v0.7.0/v0.8.0). Matching is now
#      exact, first-occurrence-only and fence-aware, so prose is left alone.
#   3. The roll REFUSES (non-zero, file untouched) rather than emitting a
#      half-formed entry it cannot link.
#   4. changelog-links.sh compares SETS, not counts, and reports
#      heading-without-linkref / linkref-without-heading / duplicate-heading as
#      DISTINCT findings. Critically: on the input that actually occurred
#      (spurious duplicate headings) it must report duplicate-heading and must
#      NOT report missing link references — the count check's misdiagnosis whose
#      obvious remedy would have entrenched the corruption.
#   5. bump-version.sh REFUSES --tag off `main`, because the refinery re-commits
#      what it merges and the tag would dangle off a commit no branch contains.
#
# Every case runs the real scripts against fixtures in a temp dir via the
# CHANGELOG_ROLL_FILE / CHANGELOG_LINKS_FILE overrides, so the repo's own
# CHANGELOG.md is never touched.
set -e

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Route through the shared test isolation before anything that can reach $HOME.
# These cases drive the real bump-version.sh, which runs git and changelog-coverage
# — a stray global git config or a pogo call would otherwise land in the
# developer's live ~/.pogo (mg-6092 / mg-e8e7 / mg-5336 / mg-3412).
source "$HERE/pogo-sandbox"
pogo_sandbox_create rollchangelog
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

SCRIPT_DIR="$HERE"
ROLL="${SCRIPT_DIR}/roll-changelog.sh"
LINKS="${SCRIPT_DIR}/changelog-links.sh"
BUMP="${SCRIPT_DIR}/bump-version.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== roll-changelog.sh / changelog-links.sh tests ==="

# --- Test 1: syntax --------------------------------------------------------
echo ""
echo "Test 1: Script syntax checks"
for s in "$ROLL" "$LINKS"; do
    if bash -n "$s" 2>/dev/null; then
        pass "$(basename "$s") has valid bash syntax"
    else
        fail "$(basename "$s") has syntax errors"
    fi
done

# A well-formed changelog with a link-reference block.
make_changelog() {
    cat > "$1" <<'EOF'
# Changelog

## [Unreleased]

### Added

- a shipped thing (mg-1111).

## [0.5.0] - 2026-07-10

### Added

- prior release entry (mg-0000).

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
}

# --- Test 2: the roll emits the link ref and re-points [Unreleased] --------
echo ""
echo "Test 2: rolling a release emits [X.Y.Z]: and re-points [Unreleased]:"
T="$(mktemp -d)"
make_changelog "$T/CHANGELOG.md"
CHANGELOG_ROLL_FILE="$T/CHANGELOG.md" bash "$ROLL" 0.6.0 2026-07-30 >/dev/null

if grep -q '^## \[0.6.0\] - 2026-07-30$' "$T/CHANGELOG.md"; then
    pass "the ## [0.6.0] heading was inserted"
else
    fail "no ## [0.6.0] heading"
fi
if grep -q '^\[0.6.0\]: https://github.com/drellem2/pogo/compare/v0.5.0\.\.\.v0.6.0$' "$T/CHANGELOG.md"; then
    pass "the [0.6.0]: compare link was emitted against the previous tag"
else
    fail "the [0.6.0]: compare link is missing or malformed"
    grep -n '^\[' "$T/CHANGELOG.md" | sed 's/^/      /' >&2
fi
if grep -q '^\[Unreleased\]: https://github.com/drellem2/pogo/compare/v0.6.0\.\.\.HEAD$' "$T/CHANGELOG.md"; then
    pass "[Unreleased] was re-pointed at v0.6.0"
else
    fail "[Unreleased] still claims the commits v0.6.0 shipped"
    grep -n '^\[Unreleased\]' "$T/CHANGELOG.md" | sed 's/^/      /' >&2
fi
# The entries that were under [Unreleased] must now sit under [0.6.0], and
# [Unreleased] must be left empty.
if awk '/^## \[Unreleased\]/{u=1;next} u&&/^## \[/{exit} u&&/^- /{c++} END{exit !(c==0)}' "$T/CHANGELOG.md"; then
    pass "[Unreleased] is empty after the roll"
else
    fail "[Unreleased] still holds entries after the roll"
fi
# And the freshly-rolled file must pass the link check.
if CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" >/dev/null 2>&1; then
    pass "the rolled changelog passes changelog-links.sh"
else
    fail "the rolled changelog FAILS its own link check"
    CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1 | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 3: REGRESSION — prose mentioning the heading is NOT rewritten ----
# This is the exact shape of the live corruption: the mg-d917 entry mentions
# `## [Unreleased]` inside an inline-code span in its body. The old sed matched
# it (s/// hits the first match on EVERY line) and injected a heading there on
# every cut, which also split the code span across a blank line and so promoted
# the injected lines to real H2 sections in the rendered output.
echo ""
echo "Test 3: REGRESSION — an inline-code mention in prose is left untouched"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Changed

- **Changelog entries are now fragments (mg-d917).** Every change used to
  append to the same
  `## [Unreleased]` lines, so any two concurrent branches collided there.

## [0.5.0] - 2026-07-10

### Added

- prior release entry (mg-0000).

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
before="$(grep -c '^## \[' "$T/CHANGELOG.md")"
CHANGELOG_ROLL_FILE="$T/CHANGELOG.md" bash "$ROLL" 0.6.0 2026-07-30 >/dev/null
after="$(grep -c '^## \[' "$T/CHANGELOG.md")"
delta=$((after - before))
if [ "$delta" -eq 1 ]; then
    pass "exactly ONE heading added (was 2 per cut under the old sed)"
else
    fail "heading count rose by $delta, expected 1 (spurious injection is back)"
    grep -n '^## \[' "$T/CHANGELOG.md" | sed 's/^/      /' >&2
fi
if grep -q '^  `## \[Unreleased\]` lines, so any two concurrent branches collided there\.$' "$T/CHANGELOG.md"; then
    pass "the prose line survived verbatim, inline-code span intact"
else
    fail "the prose line was rewritten"
    grep -n 'Unreleased' "$T/CHANGELOG.md" | sed 's/^/      /' >&2
fi
if CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" >/dev/null 2>&1; then
    pass "no duplicate headings introduced (link check clean)"
else
    fail "the roll corrupted the file"
    CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1 | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 4: a fenced `## [Unreleased]` is not a section ------------------
echo ""
echo "Test 4: a ## [Unreleased] inside a fenced code block is not rolled"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Added

- documents the format (mg-2222):

```markdown
## [Unreleased]
```

## [0.5.0] - 2026-07-10

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
CHANGELOG_ROLL_FILE="$T/CHANGELOG.md" bash "$ROLL" 0.6.0 2026-07-30 >/dev/null
# Exactly one 0.6.0 heading, and the fenced example still reads [Unreleased].
n060="$(grep -c '^## \[0.6.0\]' "$T/CHANGELOG.md")"
if [ "$n060" -eq 1 ]; then
    pass "one 0.6.0 heading emitted, the fenced example untouched"
else
    fail "expected 1 ## [0.6.0] heading, got $n060"
    sed 's/^/      /' "$T/CHANGELOG.md" >&2
fi
rm -rf "$T"

# --- Test 5: the roll REFUSES rather than emitting an unlinkable heading ---
echo ""
echo "Test 5: LOUD REFUSAL — no [Unreleased]: link ref means no cut"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Added

- a thing (mg-3333).

## [0.5.0] - 2026-07-10
EOF
orig="$(cat "$T/CHANGELOG.md")"
set +e
out="$(CHANGELOG_ROLL_FILE="$T/CHANGELOG.md" bash "$ROLL" 0.6.0 2026-07-30 2>&1)"
status=$?
set -e
if [ "$status" -ne 0 ]; then
    pass "refused the roll (exit $status)"
else
    fail "rolled anyway with no link-reference block to extend"
fi
if [ "$orig" = "$(cat "$T/CHANGELOG.md")" ]; then
    pass "CHANGELOG left untouched by the refusal"
else
    fail "CHANGELOG was modified despite the refusal"
fi
if echo "$out" | grep -q 'literal text'; then
    pass "the refusal explains WHY (renders as literal text)"
else
    fail "the refusal does not say why it matters"
fi

# No [Unreleased] section at all.
printf '# Changelog\n\n## [0.5.0] - 2026-07-10\n\n[Unreleased]: https://x/compare/v0.5.0...HEAD\n' \
    > "$T/CHANGELOG.md"
set +e
out="$(CHANGELOG_ROLL_FILE="$T/CHANGELOG.md" bash "$ROLL" 0.6.0 2026-07-30 2>&1)"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q "no '## \[Unreleased\]' section"; then
    pass "refused when there is no [Unreleased] section, and named the reason"
else
    fail "expected a refusal naming the missing [Unreleased] section, got exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 6: links — a clean file passes and says what it verified ---------
echo ""
echo "Test 6: changelog-links.sh passes a clean file"
T="$(mktemp -d)"
make_changelog "$T/CHANGELOG.md"
set +e
out="$(CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "clean file exits 0"
else
    fail "clean file reported findings"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'each matched by exactly one link reference'; then
    pass "the pass message states the property verified, not just OK"
else
    fail "the pass message is not evidence"
fi
rm -rf "$T"

# --- Test 7: links — heading with no link ref, reported AS THAT -----------
echo ""
echo "Test 7: heading-without-linkref is its own finding"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.6.0] - 2026-07-30

## [0.5.0] - 2026-07-10

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.6.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
set +e
out="$(CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q '\[heading-without-linkref\]'; then
    pass "reports heading-without-linkref"
else
    fail "did not report heading-without-linkref (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q '0\.6\.0'; then
    pass "NAMES the unmatched version rather than printing a count difference"
else
    fail "did not name the unmatched version"
fi
if echo "$out" | grep -qi 'remedy: add'; then
    pass "the remedy is to ADD the link reference"
else
    fail "no add-the-link remedy given"
fi
rm -rf "$T"

# --- Test 8: links — link ref with no heading, a DIFFERENT finding ---------
echo ""
echo "Test 8: linkref-without-heading is a different finding with a different remedy"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.5.0] - 2026-07-10

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
[0.4.0]: https://github.com/drellem2/pogo/compare/v0.3.0...v0.4.0
EOF
set +e
out="$(CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q '\[linkref-without-heading\]'; then
    pass "reports linkref-without-heading for 0.4.0"
else
    fail "did not report linkref-without-heading (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
if ! echo "$out" | grep -q '\[heading-without-linkref\]'; then
    pass "does NOT confuse it with the opposite direction"
else
    fail "reported the wrong direction too"
fi
if echo "$out" | grep -q 'Do NOT add an'; then
    pass "warns against satisfying the check with an empty section"
else
    fail "no warning against the wrong remedy"
fi
rm -rf "$T"

# --- Test 9: THE CASE THAT ACTUALLY OCCURRED ------------------------------
# Three spurious headings injected into an entry body by the old sed. The count
# check reported "3 missing link references" and the obvious remedy — add them —
# would have given the spurious headings link targets and entrenched the
# corruption. The set-based check must name the RIGHT object instead.
echo ""
echo "Test 9: spurious duplicate headings report as duplicate-heading, NOT as missing link refs"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.6.0] - 2026-07-30

### Changed

- **Fragments now (mg-d917).** Every change used to append to the same
  `## [Unreleased]

## [0.6.0] - 2026-07-30

## [0.5.0] - 2026-07-10` lines, so any two branches collided there.

## [0.5.0] - 2026-07-10

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/drellem2/pogo/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
set +e
out="$(CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q '\[duplicate-heading\]'; then
    pass "reports duplicate-heading"
else
    fail "did not report duplicate-heading (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
# The load-bearing assertion: it must NOT claim link references are missing.
if ! echo "$out" | grep -q '\[heading-without-linkref\]'; then
    pass "does NOT report missing link references (the count check's misdiagnosis)"
else
    fail "MISDIAGNOSED as missing link refs — the remedy would entrench the corruption"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'Do NOT add link'; then
    pass "explicitly warns that adding link refs would entrench the corruption"
else
    fail "no warning against the entrenching remedy"
fi
if echo "$out" | grep -q '0\.6\.0' && echo "$out" | grep -q '0\.5\.0'; then
    pass "names both duplicated versions"
else
    fail "did not name the duplicated versions"
fi
rm -rf "$T"

# --- Test 10: links — [Unreleased] left pointing at a superseded tag ------
echo ""
echo "Test 10: unreleased-linkref-stale catches [Unreleased] claiming shipped commits"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.6.0] - 2026-07-30

## [0.5.0] - 2026-07-10

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.6.0]: https://github.com/drellem2/pogo/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
set +e
out="$(CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q '\[unreleased-linkref-stale\]'; then
    pass "reports unreleased-linkref-stale"
else
    fail "did not catch the stale [Unreleased] compare base (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'claiming the commits'; then
    pass "says what the staleness DOES (claims shipped commits)"
else
    fail "does not explain the consequence"
fi
rm -rf "$T"

# --- Test 11: links — an indented `## [x]` is flagged, not counted --------
echo ""
echo "Test 11: an indented ## [x] is flagged as not-a-section"
T="$(mktemp -d)"
cat > "$T/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Added

- an example (mg-4444):

  ## [9.9.9] - 2099-01-01

## [0.5.0] - 2026-07-10

[Unreleased]: https://github.com/drellem2/pogo/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/drellem2/pogo/releases/tag/v0.5.0
EOF
set +e
out="$(CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "the indented line did not fabricate a missing-linkref finding"
else
    fail "an indented ## [x] was treated as a section (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'treated as NOT sections'; then
    pass "but it IS flagged for the reader rather than silently dropped"
else
    fail "the indented line was silently ignored"
    echo "$out" | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 12: bump-version.sh REFUSES --tag off main ----------------------
# The gate only matters if it is WIRED IN, and it must refuse BEFORE writing
# anything — a pushed release tag cannot be unpublished.
echo ""
echo "Test 12: bump-version.sh refuses --tag off main, allows it on main"
T="$(mktemp -d)"
mkdir -p "$T/internal/version" "$T/scripts/lib" "$T/changelog.d"
printf 'package version\n\nconst Version = "0.5.0"\n' > "$T/internal/version/version.go"
cp "$BUMP" "$ROLL" "$LINKS" "$SCRIPT_DIR/assemble-changelog.sh" \
   "$SCRIPT_DIR/changelog-coverage.sh" "$T/scripts/"
cp "$SCRIPT_DIR/lib/common.sh" "$T/scripts/lib/"
make_changelog "$T/CHANGELOG.md"
printf -- '- a described change (mg-8888).\n' > "$T/changelog.d/mg-8888.added.md"
git -C "$T" init -q -b main 2>/dev/null || git -C "$T" init -q
git -C "$T" config user.email "test@example.com"
git -C "$T" config user.name "Test"
git -C "$T" config commit.gpgsign false
git -C "$T" add -A
git -C "$T" commit -qm "chore: scaffold"

git -C "$T" checkout -q -b polecat-abcd
set +e
out="$( cd "$T" && bash scripts/bump-version.sh 0.6.0 --commit --tag 2>&1 )"
status=$?
set -e
if [ "$status" -ne 0 ] && echo "$out" | grep -q "refusing --tag on branch 'polecat-abcd'"; then
    pass "REFUSES --tag on a polecat branch and names the branch"
else
    fail "expected a refusal on polecat-abcd, got exit $status"
    echo "$out" | sed 's/^/      /' >&2
fi
if echo "$out" | grep -q 'RE-COMMITS'; then
    pass "the refusal explains the mechanism (the refinery re-commits)"
else
    fail "the refusal does not explain why the tag would dangle"
fi
if echo "$out" | grep -q 'git ls-remote --tags origin'; then
    pass "the refusal prescribes verifying the tag reached the REMOTE"
else
    fail "no remote-verification step in the prescribed sequence"
fi
if grep -q '0.5.0' "$T/internal/version/version.go"; then
    pass "refused BEFORE writing anything (version.go untouched)"
else
    fail "version.go was modified before the refusal"
fi

# On main the same invocation must NOT be refused by this gate.
git -C "$T" checkout -q main
set +e
out="$( cd "$T" && bash scripts/bump-version.sh 0.6.0 --commit --tag 2>&1 )"
status=$?
set -e
if ! echo "$out" | grep -q 'refusing --tag on branch'; then
    pass "does NOT refuse on main (the gate is not a blanket refusal)"
else
    fail "refused on main, which is the documented release path"
    echo "$out" | sed 's/^/      /' >&2
fi
if [ "$status" -eq 0 ] && git -C "$T" tag -l | grep -q '^v0.6.0$'; then
    pass "the on-main cut tags as before"
else
    fail "the on-main cut did not complete (exit $status)"
    echo "$out" | sed 's/^/      /' >&2
fi
# And the cut it produced must be link-consistent — the gate wired into the cut.
if CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" >/dev/null 2>&1; then
    pass "the cut CHANGELOG passes the link check"
else
    fail "the cut produced a link-inconsistent changelog"
    CHANGELOG_LINKS_FILE="$T/CHANGELOG.md" bash "$LINKS" 2>&1 | sed 's/^/      /' >&2
fi
rm -rf "$T"

# --- Test 13: the real repo's CHANGELOG is link-consistent ---------------
echo ""
echo "Test 13: the repo's own CHANGELOG.md passes the link check"
set +e
out="$(bash "$LINKS" 2>&1)"
status=$?
set -e
if [ "$status" -eq 0 ]; then
    pass "real CHANGELOG.md is link-consistent"
    echo "$out" | sed 's/^/      /'
else
    fail "real CHANGELOG.md has link findings"
    echo "$out" | sed 's/^/      /' >&2
fi

# --- Results ---------------------------------------------------------------
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
