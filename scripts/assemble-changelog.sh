#!/bin/bash
set -e

# =============================================================================
# CHANGELOG FRAGMENT ASSEMBLER FOR POGO
# =============================================================================
#
# WHY THIS EXISTS (mg-d917):
#   Every polecat used to append its entry to the SAME tail of CHANGELOG.md
#   under "## [Unreleased]". Two concurrent polecats therefore always touched
#   the same lines, and the refinery's rebase-onto-main merge collided there.
#   That was the dominant recorded merge-conflict cause (4 of 5 conflicts the
#   refinery ever recorded were this one file), and the rate scales with
#   concurrency — quadratic in polecats in flight.
#
#   The fix is structural, not a serialization: each change writes its own
#   file, `changelog.d/<slug>.<category>.md`. Two polecats never touch the same
#   path, so the conflict class is IMPOSSIBLE, not merely rarer. This script
#   assembles those fragments into the "## [Unreleased]" section at RELEASE cut
#   time (called from bump-version.sh), then deletes the consumed fragments.
#
# FRAGMENT FORMAT:
#   Path:     changelog.d/<slug>.<category>.md      (e.g. changelog.d/mg-d917.changed.md)
#   Category: the last dot-segment of the basename. One of:
#               added changed deprecated removed fixed security documentation
#             (docs/doc are aliases for documentation). A missing/unknown
#             category defaults to "Changed" with a note on stderr.
#   Body:     Keep-a-Changelog markdown bullets, e.g.
#               - **Short headline (mg-<id>).** Longer prose continuation…
#   README.md in changelog.d/ is documentation, never a fragment — it is skipped.
#
# LOUD-EMPTY INVARIANT (mg-d917 bar):
#   A check that cannot fail is worthless. After assembly, if the resulting
#   "## [Unreleased]" section has ZERO entries (no top-level `- ` bullet), this
#   script exits NON-ZERO and leaves CHANGELOG.md untouched — an empty
#   changelog.d MUST NOT silently produce an empty release.
#
# OVERRIDES (used by the test harness so it never touches the real files):
#   CHANGELOG_ASSEMBLE_FILE   path to CHANGELOG.md   (default: repo root)
#   CHANGELOG_ASSEMBLE_DIR    path to changelog.d/   (default: repo root)
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

CHANGELOG="${CHANGELOG_ASSEMBLE_FILE:-$REPO_ROOT/CHANGELOG.md}"
FRAG_DIR="${CHANGELOG_ASSEMBLE_DIR:-$REPO_ROOT/changelog.d}"

# Canonical section order (Keep a Changelog + this project's "Documentation").
CANON_ORDER="Added Changed Deprecated Removed Fixed Security Documentation"

# Normalize a raw category token to its canonical Title-case section name, or
# empty string if it is not a recognized category.
normalize_category() {
    case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
        added)                     echo "Added" ;;
        changed)                   echo "Changed" ;;
        deprecated)                echo "Deprecated" ;;
        removed)                   echo "Removed" ;;
        fixed)                     echo "Fixed" ;;
        security)                  echo "Security" ;;
        documentation|docs|doc)    echo "Documentation" ;;
        *)                         echo "" ;;
    esac
}

if [ ! -f "$CHANGELOG" ]; then
    echo -e "${RED}Error: CHANGELOG not found at $CHANGELOG${NC}" >&2
    exit 1
fi

# --- Collect fragments ------------------------------------------------------
# Build an ADDITIONS temp file with @@CATEGORY@@<Title> markers so a single awk
# pass can bucket bullets by section. Track consumed files for later removal.
ADDITIONS="$(mktemp)"
CONSUMED="$(mktemp)"
trap 'rm -f "$ADDITIONS" "$CONSUMED" "$NEWFILE" 2>/dev/null || true' EXIT
NEWFILE="$(mktemp)"

frag_count=0
if [ -d "$FRAG_DIR" ]; then
    # Sorted for deterministic ordering within a category.
    while IFS= read -r frag; do
        [ -n "$frag" ] || continue
        base="$(basename "$frag" .md)"
        [ "$base" = "README" ] && continue
        seg="${base##*.}"
        cat="$(normalize_category "$seg")"
        if [ -z "$cat" ]; then
            cat="Changed"
            echo -e "${YELLOW}Note: fragment $(basename "$frag") has no recognized category segment; filing under Changed${NC}" >&2
        fi
        printf '@@CATEGORY@@%s\n' "$cat" >> "$ADDITIONS"
        cat "$frag" >> "$ADDITIONS"
        # Guarantee the fragment's last bullet is newline-terminated.
        printf '\n' >> "$ADDITIONS"
        echo "$frag" >> "$CONSUMED"
        frag_count=$((frag_count + 1))
    done < <(find "$FRAG_DIR" -maxdepth 1 -type f -name '*.md' | LC_ALL=C sort)
fi

# --- Merge fragments into the [Unreleased] section --------------------------
# First file (ADDITIONS): bucket added[] by category. Second file (CHANGELOG):
# buffer the Unreleased body verbatim, then re-emit its sections in canonical
# order with the fragment bullets appended — preserving any hand-written
# entries already there (transition period) and multi-line bullet prose.
awk -v ORDER="$CANON_ORDER" '
function trim(s){ sub(/^\n+/,"",s); sub(/\n+$/,"",s); return s }
function emit_unreleased(   i,cat,j,n,lines,line,curcat,text,c){
    n = split(body, lines, "\n")
    curcat = ""
    for (j = 1; j <= n; j++) {
        line = lines[j]
        if (line ~ /^### /) { curcat = substr(line, 5); order_seen[++nseen] = curcat; continue }
        if (curcat != "") raw[curcat] = raw[curcat] line "\n"
    }
    print ""                               # single blank line after "## [Unreleased]"
    for (i = 1; i <= NORD; i++) {
        cat = ORD[i]
        text = raw[cat]
        if (add[cat] != "") { if (trim(text) != "") text = trim(text) "\n"; text = text add[cat] }
        text = trim(text)
        if (text != "") { print "### " cat; print ""; print text; print "" }
        done_cat[cat] = 1
    }
    # Preserve any non-canonical sections a human wrote, in original order.
    for (c = 1; c <= nseen; c++) {
        cat = order_seen[c]
        if (done_cat[cat]) continue
        done_cat[cat] = 1
        text = trim(raw[cat])
        if (text != "") { print "### " cat; print ""; print text; print "" }
    }
}
BEGIN { NORD = split(ORDER, ORD, " ") }
FNR == NR {
    if ($0 ~ /^@@CATEGORY@@/) { cur = substr($0, 13); next }   # 13 = len("@@CATEGORY@@")+1
    add[cur] = add[cur] $0 "\n"
    next
}
!inUnrel && /^## \[Unreleased\]/ { print; inUnrel = 1; body = ""; next }
inUnrel && /^## \[/ { emit_unreleased(); inUnrel = 0; print; next }
inUnrel { body = body $0 "\n"; next }
{ print }
END { if (inUnrel) emit_unreleased() }
' "$ADDITIONS" "$CHANGELOG" > "$NEWFILE"

# --- LOUD-EMPTY GUARD -------------------------------------------------------
# Count top-level entries in the resulting Unreleased section. Zero => refuse.
entry_count="$(awk '
    /^## \[Unreleased\]/ { u = 1; next }
    u && /^## \[/        { u = 0 }
    u && /^- /           { c++ }
    END { print c + 0 }
' "$NEWFILE")"

if [ "$entry_count" -eq 0 ]; then
    echo -e "${RED}Error: assembly produced an EMPTY [Unreleased] section.${NC}" >&2
    echo -e "${RED}  Fragments found in $FRAG_DIR: $frag_count${NC}" >&2
    echo -e "${RED}  Refusing to cut a changelog with no entries. Add a fragment:${NC}" >&2
    echo -e "${RED}    changelog.d/<slug>.<category>.md   (e.g. mg-1234.fixed.md)${NC}" >&2
    exit 1
fi

# --- Commit the result to disk ---------------------------------------------
cp "$NEWFILE" "$CHANGELOG"

# Remove consumed fragments (README.md and non-fragment files were never listed).
removed=0
if [ -s "$CONSUMED" ]; then
    while IFS= read -r frag; do
        [ -n "$frag" ] || continue
        rm -f "$frag"
        removed=$((removed + 1))
    done < "$CONSUMED"
fi

echo -e "${GREEN}✓ Assembled $frag_count fragment(s) into [Unreleased]; $entry_count entr(y/ies) present; removed $removed consumed fragment(s).${NC}"
