#!/bin/bash
set -e

# =============================================================================
# CHANGELOG RELEASE-ROLL FOR POGO
# =============================================================================
#
# Rolls the "## [Unreleased]" section into "## [X.Y.Z] - <date>" AND maintains
# the link-reference block at the bottom of the file:
#
#   - inserts  [X.Y.Z]: <base>/compare/vPREV...vX.Y.Z
#   - re-points [Unreleased]: <base>/compare/vX.Y.Z...HEAD
#
# WHY THIS EXISTS (mg-cef7):
#   bump-version.sh's update_changelog() used to be a single unanchored sed:
#
#       sed_i "s/## \[Unreleased\]/## [Unreleased]\n\n## [$version] - $date/"
#
#   That one line carried TWO defects, both silent, both recurring every cut:
#
#   1. NO LINK REFERENCE. It inserted the heading and nothing else, so the new
#      version had no `[X.Y.Z]:` compare link and `[Unreleased]` kept pointing
#      at the PREVIOUS tag — still claiming the commits the release just
#      shipped. A missing link reference does not error: Markdown renders
#      `[0.8.0]` as LITERAL TEXT in the published changelog, so it degrades a
#      user-facing artifact and reads as a typo rather than a tooling fault.
#      Repaired by hand at v0.7.0 and again at v0.8.0; each repair was correct
#      and left the next cut to rediscover it.
#
#   2. UNANCHORED MATCH. `s///` without `g` replaces the first match on EVERY
#      matching line, not just the section heading. The mg-d917 changelog entry
#      mentions `## [Unreleased]` inside an inline-code span in its prose, so
#      every cut injected a spurious `## [X.Y.Z]` heading into that entry's
#      body too. Measured: the heading count rose by TWO per cut (9 → 11 → 13
#      → 15 across v0.6.0/v0.7.0/v0.8.0) — one real section and one injected
#      into prose. The injection also split the inline-code span across a blank
#      line, which terminates it, so the renderer promoted the injected lines
#      to real H2 sections and the published changelog showed 0.8.0/0.7.0/
#      0.6.0 twice. The corruption compounds: one more per release, forever.
#
#   So the matching is now EXACT (`## [Unreleased]` alone on its own line),
#   FIRST-occurrence-only, and fence-aware. A mention of the heading in prose
#   or in a code block is left alone.
#
# WHAT IT REFUSES:
#   Nothing here guesses. If the file has no `## [Unreleased]` section, or no
#   `[Unreleased]:` link reference to derive the compare base and previous tag
#   from, it exits NON-ZERO and leaves the file untouched rather than emitting a
#   half-formed release entry — the failure mode this script exists to kill is
#   precisely a cut that "succeeds" and produces a wrong artifact.
#
# USAGE:
#   scripts/roll-changelog.sh <version> [date]
#
# OVERRIDES (used by the test harness so it never touches the real file):
#   CHANGELOG_ROLL_FILE   path to CHANGELOG.md   (default: repo root)
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

CHANGELOG="${CHANGELOG_ROLL_FILE:-$REPO_ROOT/CHANGELOG.md}"

VERSION="$1"
DATE="${2:-$(date +%Y-%m-%d)}"

if [ -z "$VERSION" ]; then
    echo "Usage: $0 <version> [date]" >&2
    exit 2
fi

if [ ! -f "$CHANGELOG" ]; then
    echo -e "${RED}Error: CHANGELOG not found at $CHANGELOG${NC}" >&2
    exit 1
fi

# --- Derive the compare base and the previous tag ---------------------------
# The `[Unreleased]:` link reference is the authoritative source for both: it
# always reads "<base>/compare/v<previous>...HEAD". Deriving PREV from it rather
# than from `git describe` keeps the emitted link consistent with the file even
# when the working tree's tags are incomplete (a polecat worktree often is).
read -r PREV BASE <<<"$(awk '
    function fence(l) { return l ~ /^[ ]{0,3}(```|~~~)/ }
    fence($0) { inf = !inf; next }
    inf       { next }
    /^\[Unreleased\]:[ \t]+/ {
        u = $2
        if (u ~ /\/compare\/v.*\.\.\.HEAD$/) {
            b = u; sub(/\/compare\/v.*$/, "", b)
            p = u; sub(/^.*\/compare\/v/, "", p); sub(/\.\.\.HEAD$/, "", p)
            print p " " b
            exit
        }
    }
' "$CHANGELOG")"

if [ -z "$PREV" ] || [ -z "$BASE" ]; then
    echo -e "${RED}Error: cannot derive the compare link for v$VERSION.${NC}" >&2
    echo -e "${RED}  $CHANGELOG has no usable '[Unreleased]:' link reference of the form${NC}" >&2
    echo -e "${RED}    [Unreleased]: <base>/compare/vPREVIOUS...HEAD${NC}" >&2
    echo -e "${RED}  Refusing to roll the changelog: emitting the '## [$VERSION]' heading${NC}" >&2
    echo -e "${RED}  without its '[$VERSION]:' link reference renders as literal text in${NC}" >&2
    echo -e "${RED}  the published changelog, which is the defect this script prevents.${NC}" >&2
    exit 1
fi

NEWFILE="$(mktemp)"
trap 'rm -f "$NEWFILE" 2>/dev/null || true' EXIT

# --- Rewrite ----------------------------------------------------------------
# EXACT match on a lone "## [Unreleased]" line, FIRST occurrence only, outside
# fenced blocks. The inline-code mention in the mg-d917 entry is indented and
# carries a backtick, so it can no longer be hit (defect 2 above).
set +e
awk -v ver="$VERSION" -v date="$DATE" -v prev="$PREV" -v base="$BASE" '
    function fence(l) { return l ~ /^[ ]{0,3}(```|~~~)/ }
    fence($0) { inf = !inf; print; next }
    inf       { print; next }
    !didHead && /^## \[Unreleased\][ \t]*$/ {
        print
        print ""
        print "## [" ver "] - " date
        didHead = 1
        next
    }
    !didLink && /^\[Unreleased\]:[ \t]+/ {
        print "[Unreleased]: " base "/compare/v" ver "...HEAD"
        print "[" ver "]: " base "/compare/v" prev "...v" ver
        didLink = 1
        next
    }
    { print }
    END {
        if (!didHead) exit 3
        if (!didLink) exit 4
    }
' "$CHANGELOG" > "$NEWFILE"
STATUS=$?
set -e

case "$STATUS" in
    0) ;;
    3)
        echo -e "${RED}Error: no '## [Unreleased]' section in $CHANGELOG.${NC}" >&2
        echo -e "${RED}  A lone '## [Unreleased]' line is required to roll a release.${NC}" >&2
        echo -e "${RED}  File left untouched.${NC}" >&2
        exit 1
        ;;
    4)
        echo -e "${RED}Error: no '[Unreleased]:' link reference in $CHANGELOG.${NC}" >&2
        echo -e "${RED}  File left untouched.${NC}" >&2
        exit 1
        ;;
    *)
        echo -e "${RED}Error: changelog roll failed (awk exit $STATUS). File left untouched.${NC}" >&2
        exit 1
        ;;
esac

cp "$NEWFILE" "$CHANGELOG"

echo -e "${GREEN}✓ Rolled [Unreleased] → [$VERSION] - $DATE${NC}"
echo -e "${GREEN}  [$VERSION]: $BASE/compare/v$PREV...v$VERSION${NC}"
echo -e "${GREEN}  [Unreleased] re-pointed at v$VERSION${NC}"
