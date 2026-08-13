#!/bin/bash
# =============================================================================
# tmpdir-leak-guard.sh — run a command with a PRIVATE $TMPDIR and report what
# it abandoned there (mg-60eb).
# =============================================================================
#
#   scripts/tmpdir-leak-guard.sh <command> [args...]
#
# WHY THIS EXISTS WHEN scripts/tmpdir-leak_test.sh ALREADY DOES
#
# mg-de3c landed the leak guard, and it works — on the five packages it names.
# Its slice was chosen as "every internal/testtmp caller", which is the set of
# packages the fix had just touched, so its coverage was a property of the FIX
# rather than of the TREE. Two TestMains outside that slice went on leaking one
# directory per run each, and on 2026-08-13 $TMPDIR reached ~5,000 fixture
# directories, the volume hit 100% with 204 MiB free, and ./build.sh died with
# Errno 28 on a temp fixture — failing every merge gate on the host, for a
# non-reason, attributed to whichever branch happened to be running (mg-60eb).
#
# So this wraps the run the gate ALREADY pays for — `go test ./...` — instead of
# re-running a hand-picked slice. Coverage becomes every package in the tree,
# including packages that do not exist yet, and it costs no additional test
# time. The measurement is the ticket's acceptance criterion: pin $TMPDIR, run
# the suite, look at what is left.
#
# WHY AN ALLOWLIST OF NAMES AND NOT A BEFORE/AFTER COUNT
#
# A count needs two runs over the same $TMPDIR (the first populates the
# fixed-name entries, the second measures growth), and reusing one directory
# across gate runs cannot be done on this box — several gates and polecats run
# at once and would count each other's fixtures. So the run is COLD and the rule
# is about identity: a cold whole-tree run may leave exactly the entries below,
# and every one of them is a fixed name that cannot grow with the number of
# runs. Anything else — in particular anything carrying an os.MkdirTemp random
# suffix — is per-run growth, which is the defect, and it fails by name.
#
# Adding to the allowlist is therefore a deliberate, reviewed act, which is the
# property the slice did not have: a new leak is not covered by "the list did
# not mention it".
#
# WHAT IT ALSO DOES: RECLAIM. The private $TMPDIR is removed at exit, on every
# path including the failure ones. A gate that measured the leak and then left
# it on the disk would be adding to the pile it exists to report.
#
# EXIT STATUS
#   the wrapped command's status, if it failed. A red suite is the finding; a
#   suite that failed is also the leakiest thing there is (fixtures whose
#   cleanup never ran), and reporting the leak as the verdict would restate this
#   ticket's own defect — an environmental reading presented as a branch verdict.
#   Leaks are still LISTED in that case, as a note.
#   1, if the command passed and something was abandoned.
#   0 otherwise.
#
# ENVIRONMENT
#   POGO_TMPDIR_GUARD=0        run the command with $TMPDIR untouched and make
#                              no measurement. Announced loudly on stderr — a
#                              guard that can be turned off quietly is one that
#                              has been off for months by the time anyone asks.
#   POGO_TMPDIR_GUARD_KEEP=1   do not remove the private $TMPDIR, and print its
#                              path. For looking at what a failing suite left.
#   POGO_TMPDIR_GUARD_BASE     where to put the private $TMPDIR. Default /tmp,
#                              which is SHORTER than darwin's per-user $TMPDIR
#                              (~52 bytes) and so leaves more of the 104-byte
#                              sun_path budget for the sockets these suites
#                              bind. internal/project's isEphemeralPath already
#                              treats /tmp, /private/tmp and os.TempDir() alike,
#                              so pinning here does not move any test across
#                              that boundary.
# =============================================================================
set -u

if [ "$#" -eq 0 ]; then
    echo "usage: tmpdir-leak-guard.sh <command> [args...]" >&2
    exit 2
fi

# Entries a cold whole-tree run is allowed to leave. Every one is a FIXED name:
# one appears per host, not one per run, so none of them can be the growth this
# guard is looking for.
#
#   pogo-test-tmp     internal/testtmp's root — the nest every test-mode store
#                     now lives in, swept by pid ownership on each first use.
#   pogo-prompts      agent.ExpandTemplateToFile's spool. A PRODUCTION path, not
#                     a test one, and its contents are a separate leak filed as
#                     mg-5197; the directory itself is one entry.
#   pogo-agents       config.AgentSocketDir's fallback nest (mg-a997). One entry
#                     holding one leaf per POGO_HOME.
#   go-build*         the Go toolchain's own build scratch. Removed by `go`
#                     itself; matched here so a toolchain that is killed
#                     mid-link cannot report as a pogo leak.
ALLOWED=(
    "pogo-test-tmp"
    "pogo-prompts"
    "pogo-agents"
)
ALLOWED_GLOBS=(
    "go-build*"
)

allowed() {
    local name="$1" a
    for a in "${ALLOWED[@]}"; do
        [ "$name" = "$a" ] && return 0
    done
    for a in "${ALLOWED_GLOBS[@]}"; do
        # shellcheck disable=SC2254 # the glob is the point
        case "$name" in $a) return 0 ;; esac
    done
    return 1
}

if [ "${POGO_TMPDIR_GUARD:-1}" = "0" ]; then
    echo "tmpdir-leak-guard.sh: DISABLED by POGO_TMPDIR_GUARD=0 — running with \$TMPDIR untouched and measuring nothing." >&2
    "$@"
    exit $?
fi

BASE="${POGO_TMPDIR_GUARD_BASE:-/tmp}"

# A REMEDY IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so it is subject to it.
# Everything below rests on the EXIT/INT/TERM/HUP trap, and nothing survives
# SIGKILL — which the refinery does use on a gate it has given up on. Without
# this sweep, a killed gate would abandon a directory holding an ENTIRE suite's
# fixtures, which is a worse leak than any this file reports.
#
# So the name carries the owning pid and the same ownership rule internal/testtmp
# applies is applied here: a sibling whose pid is gone is removed, at any age; a
# sibling whose pid is alive is left alone at any age, because on this box it is
# very likely another gate running right now. `kill -0` cannot separate ESRCH
# from EPERM in shell, so another user's directory reads as gone — /tmp's sticky
# bit is what stops the removal from succeeding, and a failed rm here costs
# nothing.
for stale in "$BASE"/pogo-gate-tmp.*; do
    [ -d "$stale" ] || continue
    stale_pid="${stale##*/pogo-gate-tmp.}"
    stale_pid="${stale_pid%%.*}"
    case "$stale_pid" in
        '' | *[!0-9]*) continue ;;  # not one of ours to judge
    esac
    if ! kill -0 "$stale_pid" 2>/dev/null; then
        chmod -R u+w "$stale" 2>/dev/null
        rm -rf "$stale" 2>/dev/null
    fi
done

PRIVATE_TMPDIR="$(mktemp -d "$BASE/pogo-gate-tmp.$$.XXXXXX")" || {
    echo "tmpdir-leak-guard.sh: could not create a private \$TMPDIR under $BASE" >&2
    exit 2
}

# Armed BEFORE the run, and naming the signals: a bare `trap ... EXIT` does not
# fire on SIGTERM, and this script's whole subject is cleanup that only happens
# on the happy path. The gate is killed by the refinery often enough that this
# is the normal case, not the exotic one.
cleanup() {
    if [ "${POGO_TMPDIR_GUARD_KEEP:-0}" = "1" ]; then
        echo "tmpdir-leak-guard.sh: keeping $PRIVATE_TMPDIR (POGO_TMPDIR_GUARD_KEEP=1)" >&2
        return
    fi
    # chmod first, and it is not belt-and-braces. A suite under this $TMPDIR can
    # stand up a fake $HOME and shell out to `go build`, which writes a module
    # cache READ-ONLY — 0444 files inside 0555 directories. `rm -rf` then fails
    # on every one of them and leaves the tree, so the reclaim this wrapper
    # promises would be a no-op exactly where the bytes are. Confined to a
    # directory this script created and has already decided to delete.
    chmod -R u+w "$PRIVATE_TMPDIR" 2>/dev/null
    rm -rf "$PRIVATE_TMPDIR"
}
trap 'cleanup' EXIT INT TERM HUP

export TMPDIR="$PRIVATE_TMPDIR"

"$@"
status=$?

# A newline-delimited string and a counter, not an array: /bin/bash on darwin is
# 3.2, where `${#arr[@]}` on an EMPTY array under `set -u` is an unbound-variable
# error — so the array spelling would abort exactly on the no-leak path, which is
# the one that has to be quiet.
leaked=""
leaked_n=0
while IFS= read -r name; do
    [ -n "$name" ] || continue
    if ! allowed "$name"; then
        leaked="$leaked$name
"
        leaked_n=$((leaked_n + 1))
    fi
done < <(find "$PRIVATE_TMPDIR" -mindepth 1 -maxdepth 1 -exec basename {} \; 2>/dev/null | sort)

if [ "$leaked_n" -gt 0 ]; then
    {
        echo ""
        echo "\$TMPDIR LEAK: the run abandoned $leaked_n entr$([ "$leaked_n" -eq 1 ] && echo y || echo ies) in its private \$TMPDIR:"
        printf '%s' "$leaked" | sed 's/^/    /'
        echo ""
        echo "  Each of these is created once per run and removed by nothing, so it is"
        echo "  disk growth proportional to how often the suite runs. On 2026-08-13 that"
        echo "  reached ~5,000 directories, filled the volume, and failed every merge gate"
        echo "  on this host with Errno 28 — attributed to whichever branch was running"
        echo "  (mg-60eb)."
        echo ""
        echo "  THE FIX IS NOT A defer/t.Cleanup ALONE. A helper that cleans up only when"
        echo "  its process reaches the end leaks exactly when tests fail, panic, overrun"
        echo "  -timeout (Go implements that by panicking) or are killed — which is when"
        echo "  suites are run most. For a directory that must outlive a single test, use"
        echo "  internal/testtmp.Dir(purpose): it nests under one root and is reaped by pid"
        echo "  ownership, so removal does not depend on any code being reached. Inside a"
        echo "  single test, t.TempDir() is correct and already covers the failure path."
        echo ""
        echo "  If an entry here is legitimately one-per-host rather than one-per-run, add"
        echo "  it to ALLOWED in scripts/tmpdir-leak-guard.sh with the reason."
        echo ""
    } >&2
    if [ "$status" -eq 0 ]; then
        status=1
    else
        echo "  (the wrapped command ALSO failed with status $status; that is reported instead —" >&2
        echo "   a leak is not a verdict about the branch.)" >&2
    fi
fi

exit "$status"
