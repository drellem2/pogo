#!/bin/bash
set -e

# =============================================================================
# EVERY `go test` INVOCATION IN THE TREE IS BOUNDED — CHECKED BY WALKING, NOT
# BY REMEMBERING
# =============================================================================
#
# WHY THIS EXISTS (mg-37d4, after mg-a465 / gh#107):
#   Three unbounded `go test` invocations have been found in this repo, one at
#   a time, by three different people:
#
#       test.sh                    gh#107, fixed by mg-a465
#       .github/workflows/ci.yml   found while fixing #107, same commit
#       scripts/upgrade-smoke.sh   found by ba465 while fixing #107, LEFT
#                                  ALONE and filed separately (mg-37d4)
#
#   mg-a465 shipped a check for this, and it is a good check, but it is
#   spelled as a list of TWO FILENAMES (go-test-budget_test.sh, tests 9-10:
#   "test.sh carries no unbounded go test", "ci.yml carries no unbounded go
#   test"). A check that names the files it checks cannot fail on a file that
#   did not exist when it was written. upgrade-smoke.sh was already in the tree
#   and already unbounded on the day that check was written, and the check was
#   green.
#
#   That is the actual defect this file addresses, and it is not "one more
#   script has no timeout". It is that the property was enforced by
#   ENUMERATION, so the enforcement's coverage silently degraded every time
#   anyone added a file. The fourth site would have been found the same way as
#   the first three: by a person, one at a time, after it had already cost
#   somebody a confusing failure.
#
#   So this walks `git ls-files` and classifies every occurrence. New files are
#   covered on the day they are added, by construction, with nobody having to
#   remember to extend a list.
#
# WHAT "BOUNDED" MEANS HERE:
#   An invocation is bounded if it carries -timeout / --timeout / -test.timeout,
#   or if it does not invoke `go test` directly at all — routing through
#   scripts/go-test-budget.sh is the normal way to be bounded, and such a call
#   site has no `go test` token for this script to find. Both gate call sites
#   (test.sh, ci.yml) pass this way.
#
#   Bounding is NOT the same as reporting well. `go test -timeout` reports an
#   overrun BY PANICKING — see scripts/go-test-budget.sh's header, which is the
#   measured finding that made mg-a465 more than a two-line change. This script
#   checks the weaker property (a budget exists at all), because that is the
#   property that can be checked mechanically over a whole tree. Preferring
#   go-test-budget.sh over a bare -timeout is a review judgement, not something
#   asserted here.
#
# THE BIAS, STATED: FALSE POSITIVES ARE CHEAP, FALSE NEGATIVES ARE THE DISEASE.
#   Every rule below is written to over-match rather than under-match. A prose
#   mention that gets flagged costs one allowlist line with a reason attached; a
#   real invocation that gets missed costs another instance of exactly the
#   history above. Where a rule had to choose, it chose to flag.
#
#   The allowlist is therefore part of the design, not an escape hatch, and it
#   is kept honest by the stale-entry check: an allowlist entry that no longer
#   matches anything is an ERROR (exit 2), not a silent no-op. That is what
#   stops the allowlist from becoming a second list-of-filenames that rots in
#   the same way as the thing it replaced.
#
# WHAT IT SCANS, AND WHY THOSE:
#   shell       *.sh, plus any tracked file whose first line is a sh/bash/zsh
#               shebang (scripts/pogo-sandbox has no extension)
#   workflows   .github/workflows/*.yml, *.yaml
#   make        Makefile, makefile, GNUmakefile, *.mk
#   go          *.go, for the two shapes a Go file can actually execute one:
#               an exec.Command("go", "test", ...) argument vector, and a
#               shell-script string literal (`#!` and the command on one line)
#               that gets written out and run.
#
#   Documentation (*.md, docs/**, CHANGELOG.md, changelog.d/*) is NOT scanned.
#   This tree mentions `go test` in prose ~40 times and none of those lines are
#   executed by anything. That exclusion is by FILE TYPE and is stated here
#   rather than being a silent grep -v, because it is the one place a real
#   invocation could hide from this script: a README that tells a human to run
#   an unbounded command. That is a documentation-quality question, not a
#   hang-in-automation question, and it is deliberately out of scope.
#
# THE RESIDUAL GAP, ALSO STATED:
#   A Go program that assembles the command from pieces (`args := []string{v,
#   "test"}`), or a shell script that builds it in a variable, is not detected.
#   No such site exists in this tree today (checked). The realistic surface —
#   the one all three known sites came from — is a person writing a literal
#   command into a shell script or a CI job, and that surface is covered
#   exhaustively. Claiming more than that would be the same overconfidence that
#   made a two-filename check look sufficient.
#
# USAGE:
#   scripts/unbounded-go-test.sh              # check; exit 1 if any unbounded
#   scripts/unbounded-go-test.sh --all        # also list the bounded sites
#   scripts/unbounded-go-test.sh --root DIR   # check a different checkout
#
# EXIT STATUS:
#   0  every invocation is bounded
#   1  at least one unbounded invocation (the report names file, line and text)
#   2  usage error, not a git checkout, or a STALE ALLOWLIST ENTRY
# =============================================================================

ROOT=""
SHOW_ALL=0

while [ $# -gt 0 ]; do
    case "$1" in
        --all) SHOW_ALL=1; shift ;;
        --root) ROOT="$2"; shift 2 ;;
        --root=*) ROOT="${1#--root=}"; shift ;;
        -h|--help) sed -n '/^# USAGE:/,/^# =====/p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unbounded-go-test.sh: unknown argument: $1" >&2; exit 2 ;;
    esac
done

if [ -z "$ROOT" ]; then
    ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fi

if [ ! -d "$ROOT" ]; then
    echo "unbounded-go-test.sh: not a directory: $ROOT" >&2
    exit 2
fi

# Tracked files only. An untracked scratch file in somebody's worktree is not
# something the merge gate should have an opinion about, and `git ls-files` is
# also what makes "new files are covered by construction" true — there is no
# second place to add a path.
if ! FILES="$(cd "$ROOT" && git ls-files 2>/dev/null)"; then
    echo "unbounded-go-test.sh: $ROOT is not a git checkout (git ls-files failed)." >&2
    exit 2
fi

# An empty file list is REFUSED rather than passed. This is the vacuous-pass
# guard, and it guards the most dangerous output this script has: "every
# invocation is bounded", exit 0, derived from nothing at all. A checker whose
# input silently became empty looks exactly like a clean tree, and that failure
# is the same species as the one this whole ticket is about — an enforcement
# that has quietly stopped covering anything while still reporting green.
if [ -z "$(printf '%s\n' "$FILES" | grep -c . | tr -d ' ')" ] || [ "$(printf '%s\n' "$FILES" | grep -c . | tr -d ' ')" -eq 0 ]; then
    echo "unbounded-go-test.sh: $ROOT has no tracked files — refusing to report a clean tree." >&2
    echo "  An empty scan and a clean scan produce the same words, so this exits 2 instead." >&2
    exit 2
fi

# -----------------------------------------------------------------------------
# THE ALLOWLIST
# -----------------------------------------------------------------------------
# Format, tab-separated:   <path><TAB><substring><TAB><reason>
#
# An occurrence is allowed when its file is <path> AND its text contains
# <substring>. Both halves matter: a path alone would silence a file, and this
# script's whole complaint about its predecessor is that it silenced files.
#
# EVERY ENTRY MUST MATCH SOMETHING. An entry matching nothing is a hard error,
# because a stale entry is indistinguishable from coverage until the day it
# isn't. Delete an entry when the line it describes goes away.
#
# Spelled as a FUNCTION whose output is spooled to a file, rather than the
# obvious ALLOWLIST=$(cat <<'ENTRIES' ...). That obvious form does not parse on
# bash 3.2 — which is what macOS ships and therefore what test.sh runs — when
# any line of the heredoc contains an apostrophe: the 3.2 command-substitution
# parser scans the heredoc body for quotes and dies with "unexpected EOF while
# looking for matching `''". It parses fine on bash 5 (ci.yml, ubuntu-latest),
# so the failure mode is a script that works in CI and breaks on the developer
# machine. Found the hard way while writing this file.
allowlist() {
    cat <<'ENTRIES'
internal/refinery/gatedefaults_test.go	#!/bin/bash	fixture script CONTENT, never executed. defaultGates() (internal/refinery/merge.go:1145) only READS a worktree to decide which gate commands to return; this string is written into a t.TempDir() and parsed, never run. Adding a timeout would change what the test asserts about gate selection.
ENTRIES
}

WORKFILE="$(mktemp "${TMPDIR:-/tmp}/pogo-unbounded-go-test.XXXXXX")"
ALLOWFILE="$(mktemp "${TMPDIR:-/tmp}/pogo-unbounded-go-test-allow.XXXXXX")"
FILELIST="$(mktemp "${TMPDIR:-/tmp}/pogo-unbounded-go-test-files.XXXXXX")"
trap 'rm -f "$WORKFILE" "$ALLOWFILE" "$FILELIST"' EXIT
allowlist > "$ALLOWFILE"
printf '%s\n' "$FILES" > "$FILELIST"

# -----------------------------------------------------------------------------
# CLASSIFY
# -----------------------------------------------------------------------------
# Emitted per occurrence:  <verdict><TAB><file><TAB><line><TAB><text>
# verdict is BOUNDED or UNBOUNDED.
#
# The regexes below never contain the literal two-word command they look for —
# they are spelled with an explicit whitespace class — so this script does not
# match itself and does not need an allowlist entry for its own source. That is
# a deliberate small trick: a checker that has to allowlist itself teaches the
# reader that allowlisting is routine.
scan_lines() {
    # stdin: file text. $1: path. Comment-stripped, continuation-joined.
    awk -v path="$1" '
        # COMMAND POSITION, not mere presence. This tree writes the two-word
        # command inside prose strings constantly — `pass "... carries no
        # unbounded (cmd)"`, and grep PATTERNS that search for it — and a
        # presence test flagged 9 such lines in one file against 1 real
        # invocation. Nine allowlist entries to find one bug would have made
        # the allowlist into the very list-of-files this script exists to
        # replace, so the rule discriminates instead of the allowlist.
        #
        # Command position is: line start, after a separator (; & && | || ( {),
        # after `$(` or a backtick, after `sh -c "`, after a YAML `run:`, or
        # after a wrapper that takes a command as its argument (time, exec,
        # env, sudo, xargs, ...). `)` is deliberately NOT an opener — nothing
        # follows a close paren as a command, and admitting it re-flagged the
        # grep patterns above.
        function is_cmd(s) {
            return s ~ /(^[ \t]*|[;&|({][ \t]*|\$\([ \t]*|`[ \t]*|-c[ \t]+["'"'"']?|run:[ \t]*|(then|do|else|elif|if|while|until|time|exec|env|sudo|nice|command|xargs|eval)[ \t]+)go[ \t]+test([^-[:alnum:]_]|$)/
        }
        function has_timeout(s) { return s ~ /(^|[^[:alnum:]_])--?(test\.)?timeout([ \t=]|$)/ }
        {
            line = $0
            # Full-line comments only. A trailing `#` may live inside a quoted
            # string, and mistaking one for a comment would drop a real command
            # off the end of a line — a false negative, the direction this
            # script does not accept.
            if (line ~ /^[ \t]*#/) { next }

            # Join backslash continuations, so a -timeout on the next physical
            # line still counts as part of the same command.
            start = NR
            joined = line
            while (joined ~ /\\[ \t]*$/) {
                sub(/\\[ \t]*$/, " ", joined)
                if ((getline nextline) <= 0) { break }
                joined = joined nextline
            }
            if (!is_cmd(joined)) { next }
            verdict = has_timeout(joined) ? "BOUNDED" : "UNBOUNDED"
            text = joined
            sub(/^[ \t]+/, "", text)
            printf "%s\t%s\t%d\t%s\n", verdict, path, start, text
        }
    '
}

scan_go() {
    # Go source. Two executable shapes, over a 5-line window so an argument
    # vector split across lines is still seen as one command.
    awk -v path="$1" '
        { buf[NR] = $0 }
        END {
            for (i = 1; i <= NR; i++) {
                if (buf[i] ~ /^[ \t]*\/\//) { continue }
                w = ""
                for (j = i; j <= NR && j < i + 5; j++) { w = w " " buf[j] }

                exec_shape = (w ~ /"go"[ \t]*,[ \t]*"test"/)
                # A shell-script string literal: a shebang and the command on
                # the same source line is how such a fixture is actually
                # written (the `\n` between them is two characters in the Go
                # source, not a newline, so this stays a single-line test).
                script_shape = (buf[i] ~ /#!/ && buf[i] ~ /go[ \t]+test/)
                if (!exec_shape && !script_shape) { continue }
                if (exec_shape && seen_exec[i - 1]) { continue }
                if (exec_shape) { seen_exec[i] = 1 }

                scope = exec_shape ? w : buf[i]
                verdict = (scope ~ /(^|[^[:alnum:]_])--?(test\.)?timeout([ \t="]|$)/) ? "BOUNDED" : "UNBOUNDED"
                text = buf[i]
                sub(/^[ \t]+/, "", text)
                printf "%s\t%s\t%d\t%s\n", verdict, path, i, text
            }
        }
    '
}

printf '%s\n' "$FILES" > "$WORKFILE"
FINDINGS=""
while IFS= read -r f; do
    [ -n "$f" ] || continue
    [ -f "$ROOT/$f" ] || continue

    kind=""
    case "$f" in
        *.md|docs/*|CHANGELOG.md|changelog.d/*) continue ;;
        *.sh) kind="lines" ;;
        .github/workflows/*.yml|.github/workflows/*.yaml) kind="lines" ;;
        Makefile|makefile|GNUmakefile|*/Makefile|*.mk) kind="lines" ;;
        *.go) kind="go" ;;
        *)
            # Extensionless executables (scripts/pogo-sandbox, hooks/*): decide
            # by shebang, so a new hook or driver is covered without anyone
            # naming it here.
            first="$(head -n 1 "$ROOT/$f" 2>/dev/null || true)"
            case "$first" in
                '#!'*sh*) kind="lines" ;;
                *) continue ;;
            esac
            ;;
    esac

    if [ "$kind" = "go" ]; then
        out="$(scan_go "$f" < "$ROOT/$f" || true)"
    else
        out="$(scan_lines "$f" < "$ROOT/$f" || true)"
    fi
    [ -n "$out" ] || continue
    FINDINGS="${FINDINGS}${out}
"
done < "$WORKFILE"

# -----------------------------------------------------------------------------
# APPLY THE ALLOWLIST, AND CHECK IT FOR ROT
# -----------------------------------------------------------------------------
UNBOUNDED=""
BOUNDED=""
ALLOWED=""
USED=""

printf '%s' "$FINDINGS" > "$WORKFILE"
while IFS=$'\t' read -r verdict file line text; do
    [ -n "$verdict" ] || continue
    if [ "$verdict" = "BOUNDED" ]; then
        BOUNDED="${BOUNDED}${file}:${line}: ${text}
"
        continue
    fi

    hit=""
    while IFS=$'\t' read -r apath asub areason; do
        [ -n "$apath" ] || continue
        [ "$apath" = "$file" ] || continue
        case "$text" in
            *"$asub"*) hit="$apath	$asub"; areason_hit="$areason"; break ;;
        esac
    done < "$ALLOWFILE"

    if [ -n "$hit" ]; then
        ALLOWED="${ALLOWED}${file}:${line}: ${text}
      allowed: ${areason_hit}
"
        USED="${USED}${hit}
"
    else
        UNBOUNDED="${UNBOUNDED}${file}:${line}: ${text}
"
    fi
done < "$WORKFILE"

# An entry is STALE only if the file it names is TRACKED IN THIS TREE and the
# entry still matched nothing there. The distinction matters and was found by
# the positive control rather than by reasoning: without it, --root against any
# other checkout exits 2 before doing any work, because an allowlist written
# for this repo names files no other repo has. That is the anti-rot mechanism
# exhibiting the very defect it guards against — a check that fails on trees it
# was never meant to judge is as useless as one that passes on trees it was.
#
# When the named file is simply gone, the entry is inert rather than dangerous:
# there is no longer a line for it to excuse, so it cannot hide anything. It is
# skipped silently. When the file is PRESENT and the substring no longer
# matches, the line has changed shape underneath the excuse and that is the
# case worth failing on.
STALE=""
while IFS=$'\t' read -r apath asub areason; do
    [ -n "$apath" ] || continue
    case "$USED" in
        *"$apath	$asub"*) continue ;;
    esac
    grep -Fxq -- "$apath" "$FILELIST" || continue
    STALE="${STALE}  ${apath} (substring: ${asub})
"
done < "$ALLOWFILE"

# -----------------------------------------------------------------------------
# REPORT
# -----------------------------------------------------------------------------
n_unbounded="$(printf '%s' "$UNBOUNDED" | grep -c . || true)"
n_bounded="$(printf '%s' "$BOUNDED" | grep -c . || true)"
n_allowed="$(printf '%s' "$ALLOWED" | grep -c '^[^ ]' || true)"

if [ "$SHOW_ALL" -eq 1 ]; then
    echo "Bounded ${n_bounded}:"
    printf '%s' "$BOUNDED" | sed 's/^/  /'
    if [ -n "$ALLOWED" ]; then
        echo "Allowlisted ${n_allowed} (not invocations):"
        printf '%s' "$ALLOWED" | sed 's/^/  /'
    fi
fi

if [ -n "$STALE" ]; then
    echo "" >&2
    echo "unbounded-go-test.sh: STALE ALLOWLIST ENTRY — matched nothing in the tree:" >&2
    printf '%s' "$STALE" >&2
    echo "" >&2
    echo "  An allowlist entry that matches nothing is not harmless: it is indistinguishable" >&2
    echo "  from coverage right up until the line it was written for comes back in a" >&2
    echo "  different form and is silently excused. Delete the entry, or fix its substring." >&2
    exit 2
fi

if [ "$n_unbounded" -eq 0 ]; then
    echo "Every 'go test' invocation is bounded (${n_bounded} bounded, ${n_allowed} allowlisted non-invocations, $(printf '%s\n' "$FILES" | grep -c . || true) tracked files walked)."
    exit 0
fi

echo ""
echo "============================================================================="
echo "UNBOUNDED: ${n_unbounded} 'go test' invocation(s) carry no timeout."
echo ""
printf '%s' "$UNBOUNDED" | sed 's/^/  /'
echo ""
echo "Each of these inherits Go's DEFAULT 10-minute per-package timeout — a number"
echo "nobody chose — and Go implements that timeout by PANICKING. So the failure a"
echo "slow run produces here is a ten-minute wait ending in an unlabelled goroutine"
echo "dump, which reads as a crash rather than as a budget overrun. That confusion is"
echo "the whole of drellem2/pogo#107."
echo ""
echo "To fix one, either:"
echo "  route it through scripts/go-test-budget.sh, which sets a budget AND reports an"
echo "  overrun in words (naming the package and the budget) instead of leaving a bare"
echo "  panic — set the per-site budget with POGO_GO_TEST_TIMEOUT; or"
echo "  pass an explicit -timeout, if the site genuinely only needs a bound and not a"
echo "  readable report."
echo ""
echo "Choose the budget from what THAT site runs. The budgets in this tree differ by"
echo "three orders of magnitude for good reasons: 20m bounds the whole suite under"
echo "./..., while a single -run test measures a fraction of a second. Copying a"
echo "number from another site is how a budget becomes wrong somewhere and rots"
echo "everywhere."
echo ""
echo "If the line is NOT an invocation (a prose example, a fixture, a grep pattern),"
echo "add it to the ALLOWLIST in scripts/unbounded-go-test.sh with the reason. Stale"
echo "entries are rejected, so the allowlist cannot quietly become a list of files"
echo "nobody checks."
echo "============================================================================="
exit 1
