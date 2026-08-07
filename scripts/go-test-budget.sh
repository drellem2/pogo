#!/bin/bash
set -e

# =============================================================================
# BOUNDED `go test` WITH A TIMEOUT THAT REPORTS AS A TIMEOUT
# =============================================================================
#
# WHY THIS EXISTS (mg-a465, gh#107):
#   `test.sh` ran a bare `go test ./...`. With no -timeout, every test binary
#   inherits Go's default of 10 minutes, and it reaches the merge gate:
#   build.sh runs ./test.sh, and the refinery gate defaults to ./build.sh.
#
#   The issue frames this as "dies at Go's 10-minute panic instead of failing
#   cleanly", with the implied remedy that setting -timeout makes it fail
#   cleanly. IT DOES NOT, and that was measured before this script was written.
#   Go implements -timeout BY PANICKING: the default 10m and an explicit 20m
#   take the identical code path (testing.(*M).startAlarm), and both end in
#
#       panic: test timed out after <budget>
#           running tests:
#               TestFoo (<budget>)
#       <goroutine dump for every goroutine in the binary>
#       FAIL    <package>   <elapsed>s
#
#   So `-timeout 20m` alone moves the panic later. It does not change its
#   shape, and the shape is the entire complaint in #107: a package that
#   merely ran long is indistinguishable at a glance from a mass regression,
#   and the tests that had already passed inside that binary are lost.
#
#   Setting the budget is therefore necessary but NOT sufficient. This script
#   is the sufficient half: it CLASSIFIES the overrun and says so in words, at
#   the tail of the output, naming the package and the budget.
#
# WHAT IT DOES NOT DO — the dump is kept, deliberately:
#   Suppressing the goroutine dump would be the obvious way to "not be a panic
#   dump", and it would destroy the only evidence that separates the two causes
#   an overrun can have, which need opposite responses:
#
#       a genuine deadlock  goroutines blocked on chan/select/mutex forever
#       a loaded host       goroutines progressing, just not fast enough
#
#   Those look identical in a bare "package X exceeded N minutes" line, and
#   telling them apart is the whole reason the reader is here. So the dump
#   stays and is LABELLED, and the label — not the dump — is the last thing
#   printed, because the tail is where a reader looks and what a truncating
#   CI log keeps.
#
# THE BUDGET, AND WHY 20m:
#   defaultGateTimeout is 60m (internal/refinery/gaterun.go), documented there
#   as roughly twice the longest gate run observed on this fleet (~30m). A 40m
#   per-package ceiling — the figure #107 suggests — is larger than the entire
#   observed gate run, so one wedged package would eat two-thirds of the gate
#   budget before reporting anything. 20m is ~2.2x the worst per-package figure
#   #107 reports (538.9s), surfaces a wedged package 20 minutes sooner, and
#   leaves the 60m gate bound reachable as the outer backstop — which kills
#   with a diagnostic heartbeat record rather than a panic dump. Choosing the
#   better-instrumented backstop is choosing a better failure, not merely an
#   earlier one.
#
#   HEADROOM, AND WHAT IT IS ON A LOADED HOST — read this before changing 20m.
#
#   Too short a budget converts load into spurious failures, which is the
#   disease mg-6c90 documents (a fixed CPU floor: 13/13 FAIL at load 52-106,
#   4/4 PASS at load 4.6-5.3, byte-identical binaries). So the number that
#   matters is not the quiet-host multiple, it is the margin when the box is
#   busy — and this host has recorded load 174 in a single night.
#
#   Quiet host, internal/agent (the slowest package), measured on THIS BRANCH:
#   268.7s isolated at load ~5 (`go test -count=1`, 2026-08-07). That is 4.5x
#   under the 20m budget. The comparable triage figures recorded in mg-a465
#   (206-217s; internal/refinery 81.2s isolated, 155.1s at load ~32) were taken
#   BEFORE this branch and are now optimistic — attribute them to that run, not
#   to this one.
#
#   Loaded host, and this is an ESTIMATE, not a measurement. The only
#   load-response pair anyone has measured here is internal/refinery's
#   81.2s@~7.8 -> 155.1s@~32, which fits runtime ~ load^0.46. Extrapolating
#   internal/agent with that exponent, from each of the three anchors available:
#
#       anchor                                load 100    load 150    load 174
#       triage, full suite (216.8s@~6)        13.1m 1.5x  15.8m 1.3x  16.9m 1.2x
#       review, this branch (279.6s@~10)      13.4m 1.5x  16.1m 1.2x  17.3m 1.2x
#       this branch, isolated (268.7s@~5)     17.7m 1.1x  21.3m 0.9x  22.8m 0.9x
#
#   So the honest statement is: the margin at load ~100 is somewhere around
#   1.1-1.5x, and at the load 150-174 this box has actually recorded, the
#   slowest package may LEGITIMATELY EXCEED 20m. Not 5.5x. A spurious timeout
#   on internal/agent under heavy fleet load is an anticipated outcome of this
#   budget, not a sign that something else broke.
#
#   Treat the exponent as rough. It is a two-point fit on one package, pushed
#   ~5x past its measured range and applied to a different package — and this
#   branch's own third point does not fit it: internal/refinery measured 99.2s
#   at load ~5 here against 81.2s at load ~7.8 at triage, i.e. LOWER load and
#   HIGHER time. Load average does not separate CPU contention from I/O wait,
#   and these suites spawn processes and PTYs. What is not in doubt is the
#   direction and the rough size: the real margin is single-digit tens of
#   percent at high load, not multiples.
#
#   20m is kept anyway, deliberately. Raising it to buy margin at load 174
#   spends the gate budget on a state where the box is failing at everything,
#   and the 60m gate bound is what should catch that. The point of recording
#   the above is that the next person to see `TIMEOUT: internal/agent` at load
#   150 should find it written down as expected — and the report this script
#   prints tells them to check the load average before reading the dump as a
#   bug. That is the whole difference between a legible failure and a triage
#   cycle. If it starts recurring at ordinary load, that is new information and
#   the number should move then.
#
# USAGE:
#   scripts/go-test-budget.sh ./...            # any `go test` package args
#
# ENVIRONMENT:
#   POGO_GO_TEST_TIMEOUT   per-package budget. Default: 20m.
#                          Any Go duration string (`go help testflag`).
#                          The test suite for this script uses it to make the
#                          positive control cost seconds instead of 20 minutes
#                          — a control nobody can afford to run is not one.
#
# EXIT STATUS:
#   `go test`'s own status, unchanged. A timeout is reported, not renumbered:
#   build.sh collapses everything to 1 anyway, and inventing a code here would
#   be a second thing for callers to get wrong.
#   2 is reserved for a usage error (see the -timeout guard below).
# =============================================================================

budget="${POGO_GO_TEST_TIMEOUT:-20m}"

# The budget is read from the ambient environment, and the merge gate runs in an
# environment this script does not own. An exported POGO_GO_TEST_TIMEOUT set for
# some other purpose would silently redefine the gate's budget — and the
# dangerous direction is DOWNWARD, because a too-small budget does not look like
# a misconfiguration, it looks like the branch failing (raised in review as
# mg-a3db advisory 3). A garbage value is the same problem wearing a different
# hat: `go test` would reject it with a flag error attributed to the run.
#
# So: parse it, and refuse a value below the floor unless the caller says out
# loud that a short budget is what it wants. The suite for this script is the
# one legitimate sub-floor caller — its positive control has to overrun in
# seconds — and it opts in explicitly rather than being special-cased here.
BUDGET_FLOOR_SECONDS=60

budget_seconds="$(awk -v d="$budget" '
    BEGIN {
        # A Go duration is one or more <number><unit> pairs (go help testflag).
        if (d !~ /^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+$/) { exit 1 }
        total = 0
        s = d
        while (match(s, /^[0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h)/)) {
            tok = substr(s, 1, RLENGTH)
            s = substr(s, RLENGTH + 1)
            match(tok, /[a-z]+$/)
            u = substr(tok, RSTART)
            n = substr(tok, 1, RSTART - 1) + 0
            if (u == "ns")      total += n / 1000000000
            else if (u == "us") total += n / 1000000
            else if (u == "ms") total += n / 1000
            else if (u == "s")  total += n
            else if (u == "m")  total += n * 60
            else if (u == "h")  total += n * 3600
        }
        printf "%.6f", total
    }' 2>/dev/null)" || budget_seconds=""

if [ -z "$budget_seconds" ]; then
    echo "go-test-budget.sh: POGO_GO_TEST_TIMEOUT=\"$budget\" is not a Go duration" >&2
    echo "  (expected forms: 20m, 90s, 1h30m — see \`go help testflag\`)." >&2
    exit 2
fi

if awk -v s="$budget_seconds" -v f="$BUDGET_FLOOR_SECONDS" 'BEGIN { exit !(s < f) }'; then
    if [ "${POGO_GO_TEST_BUDGET_ALLOW_SHORT:-}" != "1" ]; then
        echo "go-test-budget.sh: refusing a ${budget} budget — below the ${BUDGET_FLOOR_SECONDS}s floor." >&2
        echo "  A budget this small does not fail like a misconfiguration, it fails like the" >&2
        echo "  branch under test, and this script runs on the merge gate. If a short budget is" >&2
        echo "  deliberate (a control proving an overrun is reported), set" >&2
        echo "  POGO_GO_TEST_BUDGET_ALLOW_SHORT=1 alongside it." >&2
        exit 2
    fi
fi

# A caller-supplied -timeout would silently win (go takes the last one) and
# leave the reported budget disagreeing with the enforced one. Refuse instead:
# there is one budget and one place to set it.
for arg in "$@"; do
    case "$arg" in
        -timeout|-timeout=*|--timeout|--timeout=*|-test.timeout|-test.timeout=*)
            echo "go-test-budget.sh: refusing a caller-supplied $arg — set POGO_GO_TEST_TIMEOUT instead," >&2
            echo "  so the enforced budget and the reported budget cannot disagree." >&2
            exit 2
            ;;
    esac
done

echo "Testing Go packages (per-package budget: ${budget}; override with POGO_GO_TEST_TIMEOUT)"

# Explicit XXXXXX template: BSD mktemp accepts `-t prefix`, GNU mktemp does not
# treat it the same way, and this script runs on both (test.sh on darwin,
# ci.yml on ubuntu-latest).
log="$(mktemp "${TMPDIR:-/tmp}/pogo-go-test-budget.XXXXXX")"
trap 'rm -f "$log"' EXIT

# tee, not a capture-then-print: the refinery reads a running gate's output
# text live (mg-9adc), so the stream has to stay a stream. PIPESTATUS carries
# go's status past tee's.
set +e
go test -timeout "$budget" "$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
set -e

# Spelled as an `if`, not `[ ... ] && exit 0`: this script's whole job is to run
# a command that is EXPECTED to fail and then say something afterwards, so a
# `set -e` interaction that aborts before the report is the one bug it cannot
# afford. An `if` has no such interaction to reason about.
if [ "$status" -eq 0 ]; then
    exit 0
fi

# Classify. Anchored on Go's exact timeout panic text, so an ordinary panic
# (`panic: runtime error: ...`) and a plain t.Fatal are never reported as
# overruns — both are covered by negative controls in go-test-budget_test.sh.
#
# Field logic rather than line-position guessing: with FS=tab, go's per-package
# output is unambiguous, and packages are emitted as contiguous blocks even
# when they ran in parallel (measured). The timed-out package is the next
# `FAIL<tab>pkg<tab>elapsed` line after the panic header; the bare `FAIL`
# summary lines have NF==1 and cannot be mistaken for it.
timed_out="$(awk '
    BEGIN { FS = "\t"; in_timeout = 0 }
    NF == 1 && $1 ~ /^panic: test timed out after / {
        reported = $1
        sub(/^panic: test timed out after /, "", reported)
        in_timeout = 1
        tests = ""
        next
    }
    in_timeout && NF == 3 && $1 == "" && $2 == "" && $3 != "" {
        name = $3
        sub(/ \([^)]*\)$/, "", name)
        tests = (tests == "" ? name : tests ", " name)
        next
    }
    in_timeout && NF >= 3 && $1 == "FAIL" {
        print $2 "\t" reported "\t" tests
        in_timeout = 0
        next
    }
' "$log")"

if [ -z "$timed_out" ]; then
    # Failed, but not on the budget. Nothing to add — the output above already
    # says what went wrong, and narrating an ordinary failure as though it were
    # a category would train readers to skip this block.
    exit "$status"
fi

count="$(printf '%s\n' "$timed_out" | wc -l | tr -d ' ')"
plural="package"
if [ "$count" -gt 1 ]; then
    plural="packages"
fi

echo ""
echo "============================================================================="
echo "TIMEOUT: ${count} ${plural} exceeded the per-package test budget of ${budget}."
echo ""
printf '%s\n' "$timed_out" | while IFS=$'\t' read -r pkg reported tests; do
    echo "  ${pkg}"
    echo "      budget enforced by go: ${reported}"
    if [ -n "$tests" ]; then
        echo "      still running at the budget: ${tests}"
    else
        echo "      still running at the budget: none reported (TestMain, or setup)"
    fi
done
echo ""
echo "This is a BUDGET OVERRUN, not a crash. Go implements -timeout by panicking"
echo "inside the test binary, so the goroutine dump above IS what a timeout looks"
echo "like — it is not a segfault and not a mass regression. Tests that had already"
echo "passed inside those binaries were lost with it, so the run is not evidence"
echo "about them either way."
echo ""
echo "The dump is kept, not suppressed: it is the only thing separating the two"
echo "causes, which need opposite responses —"
echo "  deadlock     goroutines parked on chan/select/mutex with no progress"
echo "  loaded host  goroutines progressing, just not fast enough"
echo "Check the host load average for the window of the run before reading the"
echo "dump as a bug. Runtime here is strongly load-sensitive: internal/refinery"
echo "has measured 81.2s at load ~8 and 155.1s at load ~32 — 1.9x on contention"
echo "alone — and at the load 150+ this host has recorded, the slowest package"
echo "can exceed this budget legitimately. That case is expected, not a"
echo "regression; see the headroom table in this script's header."
echo ""
echo "Raise ${budget} deliberately, not reflexively: the refinery's 60m gate bound"
echo "is the outer backstop, and a per-package budget that approaches it stops"
echo "leaving room for the better-instrumented failure to happen instead."
echo "============================================================================="

exit "$status"
