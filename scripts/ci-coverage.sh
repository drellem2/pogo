#!/bin/bash
# =============================================================================
# HOW MUCH OF THE MERGE GATE DOES GITHUB CI ACTUALLY RUN? (mg-82a6)
# =============================================================================
#
# WHY THIS EXISTS:
#   `./test.sh` is the merge gate. `.github/workflows/ci.yml` is not a second
#   opinion on it — it is a SUBSET of it, on a different operating system. The
#   two overlap on a handful of rows and diverge on the rest, and nothing in
#   either file said so.
#
#   On 2026-08-14 at 01:57Z one cold `./build.sh` reported [FAILED] on 1ebf2dc
#   and GitHub CI reported SUCCESS on the same commit. The load-bearing sentence
#   of the escalation that followed was:
#
#       "So CI and a cold local build disagree, which is itself the finding:
#        one of them is wrong about the tree that is about to be deployed."
#
#   Neither was wrong. They were answering different questions about different
#   rows on different platforms, so they cannot disagree in the sense that
#   sentence needs. That inference escalated one unreproduced failure into a
#   MAIN IS RED alarm four minutes before a nightly deploy, recommended a
#   dispatch hold, consumed a polecat and a PM cycle, and came one step from
#   waking the user — on a tree that was green throughout (re-run: exit 0, 74
#   packages, 0 FAIL, same commit). See mg-5fc8 for the incident and mg-82a6
#   for this instrument.
#
#   The belief is the bug, and it is a belief this fleet will keep re-deriving,
#   because "CI is green on that commit" reads as decisive to anyone who has
#   not counted the rows. So this script counts them, and `test.sh` prints the
#   count next to every gate result — loudest next to a failure, which is the
#   moment the inference gets made.
#
# WHY IT MEASURES RATHER THAN ASSERTS A NUMBER:
#   "5 of 27" is a claim, and a claim rots the first time either file changes.
#   Both files are parsed on every run, so the number is an observation of the
#   tree in front of you. If the parse stops working the script exits 2 and
#   says which half it could not read, rather than reporting a confident 0.
#
# WHAT IT DELIBERATELY DOES NOT DO:
#   It does not propose that CI run all the rows. Several are darwin-specific,
#   several stand up live daemons, and one drives the live fleet. The goal is to
#   stop the false inference, not to widen CI.
#
# HOW THE MATCH IS MADE:
#   A `gate_step` row's INNER command is the last `*.sh` path in it; anything
#   earlier is a WRAPPER. Row `bash scripts/tmpdir-leak-guard.sh bash
#   scripts/go-test-budget.sh ./...` therefore has inner `go-test-budget.sh`
#   and wrapper `tmpdir-leak-guard.sh`. CI is searched for both, and a row whose
#   inner command CI runs WITHOUT the gate's wrapper — or with different
#   arguments — is reported as covered NOT IDENTICALLY rather than as covered.
#   That distinction is the whole of the one Go row the two files share: the
#   gate wraps it in a $TMPDIR-count assertion that can fail on its own with
#   every Go test passing, and CI does not run that assertion at all.
#
#   Only the text inside YAML `run:` scalars is searched, and comments are
#   stripped from it. A script path named in a comment is prose, and counting
#   prose as coverage is the same false-positive class that made a presence
#   rule unusable for the tree-wide `go test` sweep (mg-37d4). Matching is on
#   whole whitespace-delimited tokens, so `go-test-budget.sh` is not satisfied
#   by `go-test-budget_test.sh`.
#
# USAGE:
#   bash scripts/ci-coverage.sh              # full breakdown, names every row
#   bash scripts/ci-coverage.sh --quiet      # the one-line summary
#   bash scripts/ci-coverage.sh --notice     # the block test.sh prints on exit
#   bash scripts/ci-coverage.sh --json       # machine-readable
#
#   --gate-status N   with --notice: the exit status of the gate run this
#                     notice is attached to. Non-zero adds the do-not-escalate
#                     paragraph, which is the one this ticket was filed for.
#   --gate-file F     read the gate from F instead of ./test.sh
#   --ci-file F       read the workflow from F instead of .github/workflows/ci.yml
#
# EXIT STATUS:
#   0  measured
#   2  could not measure — a file is missing, or a parse found no gate rows or
#      no CI commands at all. Loud rather than zero: "0 of 0 shared" is the
#      most alarming reading this script can produce and it must never be
#      producible by the parser having rotted.
# =============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

GATE_FILE="${CI_COVERAGE_GATE_FILE:-$REPO_ROOT/test.sh}"
CI_FILE="${CI_COVERAGE_CI_FILE:-$REPO_ROOT/.github/workflows/ci.yml}"
MODE="full"
GATE_STATUS=""

usage() {
    sed -n '/^# USAGE:/,/^# EXIT STATUS:/p' "$0" | sed -e 's/^# \{0,1\}//' -e '$d'
}

while [ $# -gt 0 ]; do
    case "$1" in
        --quiet) MODE="quiet" ;;
        --notice) MODE="notice" ;;
        --json) MODE="json" ;;
        --gate-status) GATE_STATUS="${2:-}"; shift ;;
        --gate-file) GATE_FILE="${2:-}"; shift ;;
        --ci-file) CI_FILE="${2:-}"; shift ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            echo "ci-coverage.sh: unknown flag: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
    shift
done

die_unmeasurable() {
    echo "CI COVERAGE: CANNOT MEASURE — $1" >&2
    echo "  gate file: $GATE_FILE" >&2
    echo "  ci file:   $CI_FILE" >&2
    echo "  This is the parser, not a finding about the tree. Do not read it as" >&2
    echo "  'CI shares no rows with the gate'." >&2
    exit 2
}

[ -f "$GATE_FILE" ] || die_unmeasurable "no such gate file"
[ -f "$CI_FILE" ] || die_unmeasurable "no such CI workflow file"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/pogo-ci-coverage.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT INT TERM HUP

# --- 1. Every shell command the workflow actually runs ----------------------
# One normalized command line per output line, taken ONLY from `run:` scalars
# (both the inline `run: cmd` and the block `run: |` forms). Everything else in
# the YAML — job names, comments, `uses:` steps — is not a command and must not
# be able to make a row look covered.
awk '
function emit(s) {
    sub(/^[ \t]+/, "", s)
    if (s ~ /^#/) return
    # A trailing comment on a shell line. Cuts a `#` inside a quoted string too;
    # the error direction is UNDER-reporting coverage, which is the safe one for
    # a warning instrument.
    sub(/[ \t]#.*$/, "", s)
    gsub(/[ \t]+/, " ", s)
    sub(/ +$/, "", s)
    if (s != "") print s
}
function indent_of(s) { match(s, /^ */); return RLENGTH }
{ line = $0; sub(/\r$/, "", line) }
in_block {
    if (line ~ /^[ \t]*$/) next
    if (indent_of(line) > block_indent) { emit(line); next }
    in_block = 0
}
{
    # `run:` as its own key, and the `- run:` form where it is the first key of
    # a list item. Both are ordinary YAML and a workflow may use either; missing
    # the second would under-report coverage silently, which is the reading this
    # instrument must never produce by accident.
    if (match(line, /^ *(- +)?run:[ \t]*/)) {
        # rest FIRST: indent_of() calls match() itself and would clobber the
        # RSTART/RLENGTH this substr depends on. Read in the other order the
        # parser silently drops every `run: |` block — it reads the `|` as part
        # of a command instead of as a block scalar, and reports a coverage
        # number that is too LOW with no sign anything went wrong.
        rest = substr(line, RSTART + RLENGTH)
        key_indent = indent_of(line)
        if (rest ~ /^[|>][-+0-9]*[ \t]*$/) {
            in_block = 1
            block_indent = key_indent
            next
        }
        emit(rest)
    }
}
' "$CI_FILE" > "$WORK/ci-commands"

# The platforms CI runs on, so the report can name the other half of the
# asymmetry without hardcoding "ubuntu-latest".
CI_PLATFORMS="$(sed -n 's/^ *runs-on: *//p' "$CI_FILE" | tr -d '"' | sort -u | tr '\n' ' ' | sed 's/ *$//')"
[ -n "$CI_PLATFORMS" ] || CI_PLATFORMS="unknown"

[ -s "$WORK/ci-commands" ] || die_unmeasurable "parsed no 'run:' commands out of the CI workflow"

# --- 2. Every gate_step row, classified against those commands --------------
# Record format, tab separated: STATE, label, inner command, detail.
awk -v cifile="$WORK/ci-commands" '
BEGIN {
    nci = 0
    while ((getline l < cifile) > 0) {
        nci++
        ciline[nci] = " " l " "
        n = split(l, t, " ")
        for (i = 1; i <= n; i++) citok[t[i]] = 1
    }
}
/^gate_step[ \t]/ {
    line = $0
    p1 = index(line, "\"")
    if (p1 == 0) next
    rest = substr(line, p1 + 1)
    p2 = index(rest, "\"")
    if (p2 == 0) next
    label = substr(rest, 1, p2 - 1)
    payload = substr(rest, p2 + 1)
    gsub(/\\\$/, "$", label)

    n = split(payload, t, /[ \t]+/)
    inner = ""; innerpos = 0; nwrap = 0
    for (i = 1; i <= n; i++) {
        if (t[i] ~ /\.sh$/) {
            if (inner != "") wrap[++nwrap] = inner
            inner = t[i]
            innerpos = i
        }
    }
    if (inner == "") next

    tail = ""
    for (i = innerpos; i <= n; i++) tail = (tail == "" ? t[i] : tail " " t[i])

    covered = (inner in citok)

    verbatim = 0
    for (i = 1; i <= nci; i++) if (index(ciline[i], " " tail " ") > 0) verbatim = 1

    missing = ""
    for (i = 1; i <= nwrap; i++) {
        if (!(wrap[i] in citok)) missing = (missing == "" ? wrap[i] : missing ", " wrap[i])
    }

    if (!covered) {
        printf "UNCOVERED\t%s\t%s\t\n", label, inner
    } else if (missing != "") {
        printf "DIFFERS\t%s\t%s\tthe gate wraps it in %s; CI does not\n", label, inner, missing
    } else if (!verbatim) {
        printf "DIFFERS\t%s\t%s\tthe gate runs `%s`; CI runs it with different arguments\n", label, inner, tail
    } else {
        printf "SHARED\t%s\t%s\t\n", label, inner
    }
    nwrap = 0
}
' "$GATE_FILE" > "$WORK/rows"

[ -s "$WORK/rows" ] || die_unmeasurable "parsed no gate_step rows out of the gate file"

TOTAL="$(wc -l < "$WORK/rows" | tr -d ' ')"
SHARED="$(grep -c '^SHARED' "$WORK/rows" || true)"
DIFFERS="$(grep -c '^DIFFERS' "$WORK/rows" || true)"
UNCOVERED="$(grep -c '^UNCOVERED' "$WORK/rows" || true)"
IN_CI=$((SHARED + DIFFERS))
GATE_PLATFORM="$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')"
[ -n "$GATE_PLATFORM" ] || GATE_PLATFORM="unknown"

summary_line() {
    local diff_note=""
    [ "$DIFFERS" -gt 0 ] && diff_note=" ($DIFFERS of them NOT identically)"
    echo "CI COVERAGE: GitHub CI runs $IN_CI of this gate's $TOTAL rows$diff_note — $UNCOVERED rows run ONLY here."
}

print_rows() {
    local state="$1" heading="$2" any=0
    while IFS="$(printf '\t')" read -r s label inner detail; do
        [ "$s" = "$state" ] || continue
        if [ "$any" -eq 0 ]; then
            echo ""
            echo "  $heading"
            any=1
        fi
        if [ -n "$detail" ]; then
            echo "    - $label"
            echo "        $detail"
        else
            echo "    - $label  [$inner]"
        fi
    done < "$WORK/rows"
}

case "$MODE" in
    quiet)
        summary_line
        ;;

    json)
        printf '{\n'
        printf '  "gate_file": "%s",\n' "$GATE_FILE"
        printf '  "ci_file": "%s",\n' "$CI_FILE"
        printf '  "gate_platform": "%s",\n' "$GATE_PLATFORM"
        printf '  "ci_platforms": "%s",\n' "$CI_PLATFORMS"
        printf '  "gate_rows": %s,\n' "$TOTAL"
        printf '  "run_by_ci": %s,\n' "$IN_CI"
        printf '  "run_by_ci_identically": %s,\n' "$SHARED"
        printf '  "run_by_ci_differently": %s,\n' "$DIFFERS"
        printf '  "gate_only": %s,\n' "$UNCOVERED"
        printf '  "rows": [\n'
        first=1
        while IFS="$(printf '\t')" read -r s label inner detail; do
            if [ "$first" -eq 1 ]; then first=0; else printf ',\n'; fi
            printf '    {"state": "%s", "step": "%s", "command": "%s", "note": "%s"}' \
                "$s" \
                "$(printf '%s' "$label" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')" \
                "$(printf '%s' "$inner" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')" \
                "$(printf '%s' "$detail" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"
        done < "$WORK/rows"
        printf '\n  ]\n'
        printf '}\n'
        ;;

    notice)
        echo ""
        echo "============================================================================="
        echo "CI COVERAGE — GitHub CI is a SUBSET of this gate, not a second opinion on it"
        echo ""
        printf '  %-30s %4s\n' "gate rows in $(basename "$GATE_FILE")" "$TOTAL"
        printf '  %-30s %4s%s\n' "also run by GitHub CI" "$IN_CI" \
            "$([ "$DIFFERS" -gt 0 ] && echo "   ($DIFFERS of them NOT identically)")"
        printf '  %-30s %4s\n' "run ONLY by this gate" "$UNCOVERED"
        printf '  %-30s %s\n' "platform" "this gate $GATE_PLATFORM, CI $CI_PLATFORMS"
        echo ""
        if [ -n "$GATE_STATUS" ] && [ "$GATE_STATUS" != "0" ]; then
            echo "  THIS RUN FAILED (exit $GATE_STATUS). Before reporting a red tree:"
            echo ""
            echo "    1. A green GitHub CI run on this commit does NOT contradict this"
            echo "       failure and is not evidence that it is spurious. CI never ran"
            echo "       $UNCOVERED of these $TOTAL rows, and ran them on $CI_PLATFORMS."
            echo "    2. One failing run is not a reproduction. Re-run this gate on the"
            echo "       same commit before escalating, and say in the report whether the"
            echo "       failure reproduced."
            echo ""
            echo "  On 2026-08-14 that first inference turned one unreproduced failure"
            echo "  into a MAIN IS RED alarm four minutes before a nightly deploy, on a"
            echo "  tree that was green throughout (mg-5fc8, mg-82a6)."
        else
            echo "  \"CI is green on this commit\" and \"this gate is red on this commit\""
            echo "  are not a contradiction and never were: they answer different"
            echo "  questions about different rows on different platforms. Reporting one"
            echo "  as evidence about the other escalated a routine flake into a"
            echo "  fleet-wide alarm on 2026-08-14 (mg-5fc8, mg-82a6)."
        fi
        echo ""
        echo "  Which rows, and which one CI runs differently:"
        echo "      bash scripts/ci-coverage.sh"
        echo "============================================================================="
        ;;

    *)
        echo "============================================================================="
        echo "CI COVERAGE — $(basename "$CI_FILE") vs $(basename "$GATE_FILE")"
        echo "============================================================================="
        echo ""
        # The gate's OWN platform is deliberately not claimed here. This script
        # is run both from the gate (darwin) and from the gate-scope job in the
        # workflow it is measuring (ubuntu-latest), and `uname` answers for
        # whoever is running the script, not for whoever runs the rows. Printing
        # it as the gate's platform would understate the asymmetry from inside
        # CI, which is the one direction this instrument must never err in.
        printf '  gate rows ....................... %4s\n' "$TOTAL"
        printf '  also run by GitHub CI ........... %4s   (%s)\n' "$IN_CI" "$CI_PLATFORMS"
        printf '    of those, run IDENTICALLY ..... %4s\n' "$SHARED"
        printf '    of those, run DIFFERENTLY ..... %4s\n' "$DIFFERS"
        printf '  run ONLY by this gate ........... %4s\n' "$UNCOVERED"
        printf '\n  (this reading taken on %s, from %s and %s)\n' \
            "$GATE_PLATFORM" "$(basename "$GATE_FILE")" "$(basename "$CI_FILE")"
        print_rows SHARED "Run by BOTH, identically:"
        print_rows DIFFERS "Run by both, but NOT the same command:"
        print_rows UNCOVERED "Run ONLY by this gate — GitHub CI never executes these:"
        echo ""
        echo "  A green CI run says these $IN_CI rows passed on $CI_PLATFORMS. It says"
        echo "  nothing whatever about the other $UNCOVERED, and it is not a second"
        echo "  opinion on this gate. Treating it as one escalated one unreproduced"
        echo "  gate failure into a MAIN IS RED alarm on 2026-08-14, four minutes"
        echo "  before a nightly deploy, on a tree that was green throughout"
        echo "  (mg-5fc8; this instrument is mg-82a6)."
        echo ""
        echo "  Widening CI is NOT the implied remedy — several of the gate-only rows"
        echo "  are darwin-specific, several stand up live daemons, and one drives the"
        echo "  live fleet. Knowing the asymmetry is the remedy."
        echo "============================================================================="
        ;;
esac
