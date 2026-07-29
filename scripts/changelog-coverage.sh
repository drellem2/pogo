#!/bin/bash
set -e

# =============================================================================
# CHANGELOG COVERAGE CHECK FOR POGO
# =============================================================================
#
# WHY THIS EXISTS (mg-7904):
#   CONTRIBUTING.md requires a changelog.d/ fragment PER CHANGE. The only guard
#   standing in for that rule was assemble-changelog.sh's LOUD-EMPTY check,
#   which refuses to cut a changelog with ZERO entries. Those are not the same
#   property. At the time this script was written the repo had 95 mg-ids in
#   feat:/fix: commits since v0.5.0 and 51 fragments — and the empty-guard
#   passed, because 51 is not zero. A cut would have proceeded and shipped a
#   changelog describing about half the release, silently.
#
#   A passing guard is evidence about the property it checks, never about the
#   rule it was written to enforce. This script checks the rule: for the mg-ids
#   in a range, is each one actually DESCRIBED in what the cut will ship?
#
# WHAT IT MEASURES:
#   Population   every distinct mg-id named in the subject of a feat:/fix:
#                commit in the range. Named explicitly in the output — a bare
#                percentage with no population is not a reportable number.
#   Described    an id is described if ANY of:
#                  fragment   changelog.d/<id>.<category>.md exists on disk
#                  unreleased the id appears in CHANGELOG.md's [Unreleased]
#                             body (hand-written, transition-era; the assembler
#                             explicitly preserves these, so they do ship)
#                  released   the id appears elsewhere in CHANGELOG.md, i.e. it
#                             already shipped in an earlier release section
#                             (only possible when a range spans a release)
#   Undescribed  none of the above. These ship absent from the changelog.
#
# EXIT STATUS:
#   0  every id in the population is described
#   1  at least one id is undescribed (the ids are listed on stdout)
#   2  usage error
#
#   Non-zero is the point. This check is wired into bump-version.sh, which
#   requires an explicit --ack-changelog-gaps to cut anyway. It is deliberately
#   NOT a per-commit merge gate: at the coverage this was written under it would
#   fail loudly and immediately on work unrelated to the gap.
#
# USAGE:
#   scripts/changelog-coverage.sh [--range <rev-range>] [--json] [--quiet]
#
#   --range   default: <most recent tag>..HEAD
#   --json    machine-readable summary (population/described/undescribed + ids)
#   --quiet   suppress the per-id listing; totals only
#
# OVERRIDES (used by the test harness so it never reads the real repo):
#   CHANGELOG_COVERAGE_REPO   git repo to inspect        (default: repo root)
#   CHANGELOG_COVERAGE_FILE   path to CHANGELOG.md       (default: $REPO/CHANGELOG.md)
#   CHANGELOG_COVERAGE_DIR    path to changelog.d/       (default: $REPO/changelog.d)
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

REPO="${CHANGELOG_COVERAGE_REPO:-$REPO_ROOT}"
CHANGELOG="${CHANGELOG_COVERAGE_FILE:-$REPO/CHANGELOG.md}"
FRAG_DIR="${CHANGELOG_COVERAGE_DIR:-$REPO/changelog.d}"

RANGE=""
JSON=false
QUIET=false

while [ $# -gt 0 ]; do
    case "$1" in
        --range)  RANGE="$2"; shift 2 ;;
        --range=*) RANGE="${1#*=}"; shift ;;
        --json)   JSON=true; shift ;;
        --quiet)  QUIET=true; shift ;;
        -h|--help)
            sed -n '/^# USAGE:/,/^# OVERRIDES/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *)
            echo -e "${RED}Error: unknown option '$1'${NC}" >&2
            echo "Usage: $0 [--range <rev-range>] [--json] [--quiet]" >&2
            exit 2 ;;
    esac
done

if [ -z "$RANGE" ]; then
    last_tag="$(git -C "$REPO" describe --tags --abbrev=0 2>/dev/null || true)"
    if [ -n "$last_tag" ]; then
        RANGE="${last_tag}..HEAD"
    else
        RANGE="HEAD"   # no tags yet: the whole history is the release
    fi
fi

if ! git -C "$REPO" rev-list "$RANGE" >/dev/null 2>&1; then
    echo -e "${RED}Error: not a valid git range: $RANGE${NC}" >&2
    exit 2
fi

# --- Population -------------------------------------------------------------
# Distinct mg-ids named in the subject of a feat:/fix: commit in the range.
# Conventional-commit subjects carry the originating work-item id in parens,
# e.g. "fix(test): the live deploy control ... (mg-3412)".
IDS="$(git -C "$REPO" log --format='%s' "$RANGE" \
        | grep -E '^(feat|fix)(\([^)]*\))?!?:' \
        | grep -oE 'mg-[0-9a-f]{4,}' \
        | LC_ALL=C sort -u || true)"

population=0
[ -n "$IDS" ] && population="$(printf '%s\n' "$IDS" | wc -l | tr -d ' ')"

# --- The [Unreleased] body, and the rest of CHANGELOG.md --------------------
UNREL=""
RELEASED=""
if [ -f "$CHANGELOG" ]; then
    UNREL="$(awk '/^## \[Unreleased\]/{u=1;next} u&&/^## \[/{u=0} u' "$CHANGELOG")"
    RELEASED="$(awk '/^## \[Unreleased\]/{u=1;next} u&&/^## \[/{u=0} !u' "$CHANGELOG")"
fi

# --- Classify ---------------------------------------------------------------
n_frag=0; n_unrel=0; n_rel=0
UNDESCRIBED=""
for id in $IDS; do
    if compgen -G "$FRAG_DIR/$id.*.md" >/dev/null 2>&1; then
        n_frag=$((n_frag + 1))
    elif [ -n "$UNREL" ] && printf '%s' "$UNREL" | grep -q -- "$id"; then
        n_unrel=$((n_unrel + 1))
    elif [ -n "$RELEASED" ] && printf '%s' "$RELEASED" | grep -q -- "$id"; then
        n_rel=$((n_rel + 1))
    else
        UNDESCRIBED="$UNDESCRIBED$id"$'\n'
    fi
done

n_undesc=0
[ -n "$UNDESCRIBED" ] && n_undesc="$(printf '%s' "$UNDESCRIBED" | grep -c . || true)"
n_desc=$((n_frag + n_unrel + n_rel))

# --- Report -----------------------------------------------------------------
if [ "$JSON" = true ]; then
    printf '{\n'
    printf '  "range": "%s",\n' "$RANGE"
    printf '  "population": %d,\n' "$population"
    printf '  "population_definition": "distinct mg-ids in feat:/fix: commit subjects in the range",\n'
    printf '  "described": %d,\n' "$n_desc"
    printf '  "described_by_fragment": %d,\n' "$n_frag"
    printf '  "described_in_unreleased": %d,\n' "$n_unrel"
    printf '  "described_in_earlier_release": %d,\n' "$n_rel"
    printf '  "undescribed": %d,\n' "$n_undesc"
    printf '  "undescribed_ids": ['
    first=true
    for id in $UNDESCRIBED; do
        [ "$first" = true ] || printf ', '
        printf '"%s"' "$id"
        first=false
    done
    printf ']\n}\n'
else
    echo "Changelog coverage for $RANGE"
    echo "  population (distinct mg-ids in feat:/fix: commit subjects): $population"
    echo "  described by a changelog.d/ fragment:                      $n_frag"
    echo "  described by a hand-written [Unreleased] entry:            $n_unrel"
    [ "$n_rel" -gt 0 ] && \
    echo "  described in an earlier release section:                   $n_rel"
    echo "  UNDESCRIBED (would ship absent from the changelog):        $n_undesc"

    if [ "$n_undesc" -gt 0 ] && [ "$QUIET" = false ]; then
        echo ""
        echo -e "${RED}These ids are in the range and nothing describes them:${NC}"
        for id in $UNDESCRIBED; do
            subj="$(git -C "$REPO" log --format='%s' "$RANGE" | grep -m1 -- "$id" || true)"
            printf '  %s  %s\n' "$id" "$subj"
        done
        echo ""
        echo "Write a fragment for each:  changelog.d/<id>.<category>.md"
        echo "  categories: added changed deprecated removed fixed security documentation"
    fi
fi

if [ "$n_undesc" -gt 0 ]; then
    if [ "$JSON" = false ]; then
        echo ""
        echo -e "${RED}✗ $n_undesc of $population id(s) in $RANGE have no changelog entry.${NC}" >&2
    fi
    exit 1
fi

[ "$JSON" = false ] && \
    echo -e "${GREEN}✓ all $population id(s) in $RANGE are described in the changelog.${NC}"
exit 0
