#!/bin/bash
set -e

# =============================================================================
# CHANGELOG LINK-REFERENCE CHECK FOR POGO
# =============================================================================
#
# Verifies that every version SECTION in CHANGELOG.md has a matching link
# REFERENCE and vice versa, so a released version never renders as literal
# `[0.8.0]` text in the published changelog.
#
# WHY THE OBVIOUS VERSION OF THIS CHECK IS WRONG (mg-cef7):
#   mg-cef7 originally proposed "a cut-time check that the heading count matches
#   the link-reference count would make a recurrence loud." Measured on live
#   main, that check FIRES — and is WRONG ABOUT WHY:
#
#       version headings   14
#       link references    11
#       difference          3
#
#   That reads as three MISSING link references. It is not. Every version with a
#   heading also had a correctly-formed link reference; the difference was three
#   SPURIOUS headings injected into the body of the mg-d917 entry by
#   update_changelog()'s unanchored sed (see scripts/roll-changelog.sh).
#
#   The obvious remedy for the count check's report — add the missing link
#   references — would have ENTRENCHED the corruption: it would have given the
#   spurious headings link targets and made them look legitimate. A control that
#   fires correctly and diagnoses wrongly is worse than one that stays silent,
#   because it directs the repair at the wrong object.
#
# SO THIS CHECK, BY CONSTRUCTION:
#   1. Compares the SETS, not the counts — it names which version is unmatched
#      and in which direction. It never prints a difference of counts.
#   2. Treats "a heading with no link ref" and "a link ref with no heading" as
#      DIFFERENT findings with DIFFERENT remedies.
#   3. Discriminates section headings from `## [x]` text that is not a section:
#      occurrences inside a fenced code block are ignored, indented occurrences
#      are flagged as not-a-section, and a version whose heading appears MORE
#      THAN ONCE is reported as a DUPLICATE — explicitly telling the reader not
#      to add link references for the extra copies. That last case is the one
#      the count check misdiagnosed, and it is the one that actually occurred.
#   4. Checks that `[Unreleased]:` compares against the NEWEST released version.
#      A cut that rolls the heading but leaves this pointing at the previous tag
#      leaves `[Unreleased]` claiming the commits the release just shipped.
#
# USAGE:
#   scripts/changelog-links.sh [path/to/CHANGELOG.md]
#
# EXIT: 0 = all findings clear.  1 = at least one finding.  2 = usage error.
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

CHANGELOG="${1:-${CHANGELOG_LINKS_FILE:-$REPO_ROOT/CHANGELOG.md}}"

if [ ! -f "$CHANGELOG" ]; then
    echo -e "${RED}Error: CHANGELOG not found at $CHANGELOG${NC}" >&2
    exit 2
fi

set +e
awk -v RED="$RED" -v GREEN="$GREEN" -v YELLOW="$YELLOW" -v NC="$NC" -v FILE="$CHANGELOG" '
function fence(l)     { return l ~ /^[ ]{0,3}(```|~~~)/ }
function isversion(v) { return v ~ /^[0-9]+\.[0-9]+/ }

fence($0) { inf = !inf; next }
inf       { next }

# --- Section headings, at document top level (column 0) --------------------
/^## \[[^]]+\]/ {
    if (match($0, /^## \[[^]]+\]/)) {
        v = substr($0, RSTART + 4, RLENGTH - 5)
        if (v == "Unreleased") { unrelHeadLine = FNR; next }
        if (!isversion(v))     next
        headCount[v]++
        if (headCount[v] == 1) { headLine[v] = FNR; headOrder[++nhead] = v }
        else                   { dupLines[v] = dupLines[v] " " FNR }
    }
    next
}

# --- `## [x]` that is INDENTED: not a section, flagged not counted ---------
/^[ ]{1,3}## \[[^]]+\]/ {
    indented[++nindent] = FNR ": " $0
    next
}

# --- Link references, at document top level -------------------------------
/^\[[^]]+\]:[ \t]+/ {
    if (match($0, /^\[[^]]+\]:/)) {
        v = substr($0, RSTART + 1, RLENGTH - 3)
        if (v == "Unreleased") {
            unrelRefLine = FNR
            u = $2
            if (u ~ /\/compare\/v.*\.\.\.HEAD$/) {
                p = u; sub(/^.*\/compare\/v/, "", p); sub(/\.\.\.HEAD$/, "", p)
                unrelBase = p
            }
            next
        }
        if (!isversion(v)) next          # an ordinary markdown link definition
        refCount[v]++
        if (refCount[v] == 1) refLine[v] = FNR
        else                  dupRefLines[v] = dupRefLines[v] " " FNR
    }
    next
}

END {
    findings = 0

    # FINDING 1: heading with no link reference.
    # Remedy: ADD the link reference — the version shipped and renders as
    # literal text without it.
    for (v in headCount) {
        if (!(v in refCount)) {
            printf "%s[heading-without-linkref]%s %s:%d  version %s has a section but no [%s]: link reference\n", RED, NC, FILE, headLine[v], v, v
            printf "    renders as literal \"[%s]\" in the published changelog\n", v
            printf "    remedy: add  [%s]: <base>/compare/v<previous>...v%s\n", v, v
            findings++
        }
    }

    # FINDING 2: link reference with no heading. A DIFFERENT defect with a
    # DIFFERENT remedy — do not "fix" this by inventing a section.
    for (v in refCount) {
        if (!(v in headCount)) {
            printf "%s[linkref-without-heading]%s %s:%d  [%s]: link reference has no ## [%s] section\n", RED, NC, FILE, refLine[v], v, v
            printf "    remedy: either the section was lost and must be restored, or the\n"
            printf "            link reference is stale and should be removed. Do NOT add an\n"
            printf "            empty section to satisfy the check.\n"
            findings++
        }
    }

    # FINDING 3: duplicate headings. THIS is what the count check misread as
    # missing link references. The remedy is the opposite of adding one.
    for (v in dupLines) {
        printf "%s[duplicate-heading]%s %s:%d  version %s has a section heading MORE THAN ONCE (also at:%s)\n", RED, NC, FILE, headLine[v], v, dupLines[v]
        printf "    at most one of these is a real section. The others are almost certainly\n"
        printf "    prose or an unterminated inline-code span that Markdown is rendering as\n"
        printf "    an H2 — check whether a `%s` span was split across a blank line.\n", "## [" v "]"
        printf "    %sremedy: REMOVE or fence the spurious heading. Do NOT add link\n", YELLOW
        printf "            references for the extra copies — that entrenches the corruption\n"
        printf "            by making them look legitimate.%s\n", NC
        findings++
    }

    # FINDING 4: duplicate link references.
    for (v in dupRefLines) {
        printf "%s[duplicate-linkref]%s %s:%d  [%s]: defined MORE THAN ONCE (also at:%s)\n", RED, NC, FILE, refLine[v], v, dupRefLines[v]
        printf "    remedy: keep one definition; Markdown resolves the first and silently\n"
        printf "            ignores the rest, so a wrong one can shadow the right one.\n"
        findings++
    }

    # FINDING 5: [Unreleased] still comparing against a superseded tag.
    if (unrelRefLine == 0) {
        printf "%s[unreleased-linkref-missing]%s %s  no [Unreleased]: link reference\n", RED, NC, FILE
        printf "    remedy: add  [Unreleased]: <base>/compare/v<newest>...HEAD\n"
        findings++
    } else if (nhead > 0 && unrelBase != "" && unrelBase != headOrder[1]) {
        printf "%s[unreleased-linkref-stale]%s %s:%d  [Unreleased] compares against v%s, but the newest section is [%s]\n", RED, NC, FILE, unrelRefLine, unrelBase, headOrder[1]
        printf "    [Unreleased] is claiming the commits v%s already shipped\n", headOrder[1]
        printf "    remedy: re-point it at v%s\n", headOrder[1]
        findings++
    }

    # Flagged, not counted as a finding: `## [x]` that is not at top level.
    if (nindent > 0) {
        printf "%sNote: %d indented \"## [...]\" line(s) treated as NOT sections:%s\n", YELLOW, nindent, NC
        for (i = 1; i <= nindent; i++) printf "  %s:%s\n", FILE, indented[i]
    }

    if (findings == 0) {
        # Say what was actually verified. "OK" on its own is not evidence.
        printf "%s✓ %d version section(s) each matched by exactly one link reference%s\n", GREEN, nhead, NC
        printf "%s  [Unreleased] compares against v%s (the newest section)%s\n", GREEN, unrelBase, NC
        exit 0
    }
    printf "\n%s%d finding(s) in %s.%s\n", RED, findings, FILE, NC
    exit 1
}
' "$CHANGELOG"
STATUS=$?
set -e
exit $STATUS
