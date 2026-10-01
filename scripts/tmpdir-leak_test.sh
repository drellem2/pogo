#!/bin/bash
# The $TMPDIR leak guard (mg-de3c).
#
# THE MEASUREMENT IS THE TEST. Count $TMPDIR's entries, run a suite that creates
# fixtures, count again. That is the whole acceptance criterion on the ticket,
# and on 2026-08-12 it failed by 37,083 entries and 61 GB — enough that
# ./build.sh hit "no space left on device" in the refinery gate and a healthy
# branch was rejected as defective (mg-b41f).
#
# Measured on this tree before the fix: one `go test ./...` left 33 directories
# in $TMPDIR, every one of them a test fixture nothing would ever delete.
#
# WHAT IS ASSERTED
#
#   Test 2  POSITIVE CONTROL, and it runs first. The check is a count, so
#           "the count did not grow" is worth nothing until "the count grows
#           when something leaks" has been shown against the same code path.
#   Test 3  A COLD $TMPDIR gains EXACTLY ONE entry — internal/testtmp's root.
#   Test 4  A WARM $TMPDIR gains NOTHING. This is the acceptance criterion
#           verbatim: count, run, count, unchanged.
#   Test 5  The sweep RECLAIMS. Nesting alone would only rename the problem;
#           repeated runs must not grow the root's own contents either.
#
# Tests 6-13 are mg-60eb's, and they are about scripts/tmpdir-leak-guard.sh —
# the wrapper that puts the measurement above on the WHOLE tree instead of on
# the slice named below. Their own load-bearing cases are Test 6 (the positive
# control for the wrapper's allowlist), Test 9 (a failing suite reports ITS
# status, never the leak's) and Test 11 (a read-only Go module cache is
# reclaimed, which is where 120 MB was sitting unremovable).
#
# Tests 14-15 are mg-b9621's. 14 is a positive control for a spelling none of
# the above plant: a TEMPLATE-LESS `mktemp -d`, which on darwin ignores $TMPDIR
# and so was invisible to the guard. 15 requires every gate row, not just the
# Go one, to run under the guard.
#
# WHAT IS NOT COVERED, AND WHY IT IS NAMED HERE RATHER THAN LEFT QUIET
#
#   config.AgentSocketDir's fallback is FIXED, and not here. It is not in the
#   slice below because nothing in that slice boots a pogod, which is the only
#   thing that ever created one: all three per-run entries came from the real
#   pogod binaries cmd/pogod's boot tests build. Its guard is therefore a Go
#   test beside them, cmd/pogod/fallbacksocketdir_test.go, which pins the same
#   count assertion this file makes against a pinned $TMPDIR.
#
#   The note this replaces said there was no room to nest that leaf under a
#   shared parent. That arithmetic was wrong by one character: the leaf was
#   "pogo-agents-<hash>" and is now "pogo-agents/<hash>", the same length to the
#   byte, so $TMPDIR gains one entry rather than one per POGO_HOME. Reaping the
#   leaves inside it needed a different ownership key than this file's — a
#   fallback socket dir is owned by a POGO_HOME, not a pid — and that is written
#   out in agent.PrepareFallbackSocketDir (mg-a997).
#
#   agent.ExpandTemplateToFile's $TMPDIR/pogo-prompts is likewise untouched: one
#   entry, but 15,819 files and 74 MB inside it, and a PRODUCTION leak rather
#   than a test one. Filed as mg-5197.
#
# The slice below is chosen to run in seconds while touching every internal/
# testtmp caller: events, ghintake, ghteardown, testsandbox and the witness /
# claim-release resolvers in internal/agent. A new leak anywhere in it fails
# this suite by name.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HERE="$SCRIPT_DIR"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PASS=0
FAIL=0

# The packaged shell isolation (mg-78a5). This suite runs `go test`, and a Go
# test binary reads HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT — so without
# this the counts below would be a property of the developer's live tree, and
# the runs would write to it. It is also the ratchet in
# internal/testsandbox/adoption_test.go: a new *_test.sh that skips this fails
# the build rather than being added to a ledger.
#
# It pins HOME and friends but NOT $TMPDIR, which is what makes it composable
# here: every measurement below supplies its own TMPDIR per invocation.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create tmpdirleak
pogo_sandbox_isolate
trap 'pogo_sandbox_down' EXIT

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

# The root internal/testtmp owns. Read from the Go constant rather than
# duplicated, so renaming it there cannot leave this suite asserting a name
# nothing writes any more.
ROOT_NAME="$(cd "$REPO_ROOT" && go list -f '{{range .ConstNames}}{{.}}{{end}}' ./internal/testtmp >/dev/null 2>&1; \
    grep -E '^const RootName = ' internal/testtmp/testtmp.go 2>/dev/null | sed -E 's/.*"(.*)".*/\1/')"
if [ -z "$ROOT_NAME" ]; then
    echo "SETUP FAILURE: could not read RootName from internal/testtmp/testtmp.go" >&2
    exit 1
fi

# The per-package budget keeps every `go test` here bounded — scripts/
# unbounded-go-test.sh sweeps the tree for invocations that are not.
BUDGET="${POGO_GO_TEST_TIMEOUT:-5m}"

# Packages that between them exercise every internal/testtmp caller.
SLICE=(./internal/events/ ./internal/ghintake/ ./internal/ghteardown/ ./internal/testsandbox/ ./internal/testtmp/)
# internal/agent in full is ~300s; these three names reach the witness store,
# the claim-release store and testsandbox.Main in about two seconds.
AGENT_RUN='TestWitness|TestMacguffinStoreRoot|TestClaimRelease'

# count_entries prints the number of TOP-LEVEL entries in a directory. Top-level
# is the whole point: the defect is $TMPDIR's entry count, not its depth.
count_entries() {
    find "$1" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' '
}

# list_entries prints them, for a failure message that names what appeared
# rather than only how many.
list_entries() { find "$1" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort; }

# run_slice runs the fixture-creating suite with $TMPDIR pinned to its argument.
# Output is captured; a failing suite is reported as a SETUP failure below,
# because a suite that did not run creates no fixtures and would sail through
# every count in this file.
run_slice() {
    local tmp=$1 log=$2
    (
        cd "$REPO_ROOT"
        TMPDIR="$tmp" go test -timeout "$BUDGET" -count=1 "${SLICE[@]}" >"$log" 2>&1 &&
        TMPDIR="$tmp" go test -timeout "$BUDGET" -count=1 -run "$AGENT_RUN" ./internal/agent/ >>"$log" 2>&1
    )
}

# setup_failed WHAT LOG — report a run_slice that did not pass, and say what it
# actually was before saying what this file could not measure.
#
# The order is the point (mg-a9d8). On 2026-08-14 this file printed
# "SETUP: the fixture-creating suite did not pass on the third run" over a log
# whose content was `lookup proxy.golang.org: no such host` — the sandbox left
# GOMODCACHE empty, so a run of this file had to fetch what it imports (measured:
# 2 zips, 2 .mod files, 752 KB, once), and a DNS blip mid-fetch landed as
# `[setup failed]` in internal/agent, which in the same gate run passed in 358s.
# Read top-down that is a $TMPDIR problem in a package that had none. It cost a
# held worker slot, a withdrawn hypothesis and a mis-scoped ticket.
#
# And it was never going to name any other package: the SLICE above imports no
# external module at all, so every byte of that fetch is the ./internal/agent
# invocation. GOMODCACHE is pinned now and the fetch is gone; this stays because
# the translation is what a person reads first, and the next module failure will
# have some other cause.
setup_failed() {
    local what="$1" log="$2"
    fail "SETUP: the fixture-creating suite did not pass $what"
    if pogo_sandbox_go_module_failure "$log" >&2; then
        return 0
    fi
    [ -r "$log" ] && sed -n '1,40p' "$log" >&2
    return 0
}

echo "=== \$TMPDIR leak guard (mg-de3c) ==="
echo "    testtmp root: $ROOT_NAME"

# Inside the sandbox root, so the EXIT trap above owns its removal — a suite
# about leaked temp directories that leaked its own would be its own defect.
WORK="$POGO_SANDBOX_DIR/work"
mkdir -p "$WORK"

# --- Test 1: this file's own syntax ----------------------------------------
echo ""
echo "Test 1: Script syntax check"
if bash -n "$0" 2>/dev/null; then
    pass "tmpdir-leak_test.sh has valid bash syntax"
else
    fail "tmpdir-leak_test.sh has syntax errors"
fi

# --- Test 2: POSITIVE CONTROL ----------------------------------------------
# Before any "the count did not grow" assertion is allowed to mean anything, the
# same counting code has to be shown reporting growth. This plants one directory
# of exactly the shape the defect produces — an os.MkdirTemp-style entry sitting
# directly in $TMPDIR that nothing will ever remove.
echo ""
echo "Test 2: POSITIVE CONTROL — the count detects a planted leak"
control="$WORK/control"
mkdir -p "$control"
before=$(count_entries "$control")
mktemp -d "$control/pogo-leaked-fixture.XXXXXX" >/dev/null
after=$(count_entries "$control")
if [ "$after" -gt "$before" ]; then
    pass "a planted directory moves the count $before -> $after"
else
    fail "the count did not move when a directory was planted ($before -> $after); every other assertion in this file is vacuous"
fi

# --- Test 3: a cold TMPDIR gains exactly one entry --------------------------
echo ""
echo "Test 3: a COLD \$TMPDIR gains exactly one entry, and it is the testtmp root"
cold="$WORK/cold"
mkdir -p "$cold"
if ! run_slice "$cold" "$WORK/cold.log"; then
    setup_failed "on the cold run, so this file measured nothing" "$WORK/cold.log"
else
    n=$(count_entries "$cold")
    if [ "$n" -eq 1 ] && [ "$(list_entries "$cold")" = "$ROOT_NAME" ]; then
        pass "one entry after a cold run, and it is $ROOT_NAME"
    else
        fail "a cold run left $n top-level entries, want exactly 1 ($ROOT_NAME):"
        list_entries "$cold" | sed 's/^/        /' >&2
    fi
fi

# --- Test 4: the acceptance criterion, verbatim -----------------------------
# Count before, run the suite, count after. Unchanged.
echo ""
echo "Test 4: a WARM \$TMPDIR is UNCHANGED by a run that creates fixtures"
before=$(count_entries "$cold")
if ! run_slice "$cold" "$WORK/warm.log"; then
    setup_failed "on the warm run" "$WORK/warm.log"
else
    after=$(count_entries "$cold")
    if [ "$after" -eq "$before" ]; then
        pass "entry count unchanged across a run: $before -> $after"
    else
        fail "entry count grew across a run: $before -> $after. New entries:"
        list_entries "$cold" | sed 's/^/        /' >&2
    fi
fi

# --- Test 5: the sweep reclaims --------------------------------------------
# Nesting alone would turn 37,083 entries in $TMPDIR into 37,083 entries one
# level down. The sweep is what makes the fix a fix, so it gets its own
# assertion: repeated runs must not grow the root's own contents either.
echo ""
echo "Test 5: repeated runs do not grow the testtmp root"
inner=$(count_entries "$cold/$ROOT_NAME")
if ! run_slice "$cold" "$WORK/sweep.log"; then
    setup_failed "on the third run" "$WORK/sweep.log"
else
    grown=$(count_entries "$cold/$ROOT_NAME")
    # Not "== inner": the last process to touch the root leaves its own entry
    # behind for the next run to reap, so the steady state is a small constant
    # rather than zero. What must not happen is growth PER RUN.
    if [ "$grown" -le "$((inner + 1))" ]; then
        pass "root contents steady across runs: $inner -> $grown"
    else
        fail "root contents grew $inner -> $grown across one run; the sweep is not reclaiming:"
        list_entries "$cold/$ROOT_NAME" | sed 's/^/        /' >&2
    fi
fi

# =============================================================================
# THE WHOLE-TREE GUARD (mg-60eb)
# =============================================================================
# Everything above measures a SLICE — five packages plus three test names — and
# that slice was chosen as "every internal/testtmp caller", which is the set of
# packages mg-de3c's own fix had just touched. Its coverage was therefore a
# property of the fix rather than of the tree, and two TestMains outside it
# (cmd/pogo, internal/refinery) went on leaking one directory per run until
# $TMPDIR reached ~5,000 fixture directories, the volume hit 100% with 204 MiB
# free, and ./build.sh died with Errno 28 in the refinery gate — failing every
# merge on the host and presenting as a defect in whichever branch was running.
#
# scripts/tmpdir-leak-guard.sh closes that by wrapping the whole-tree `go test`
# the gate already runs, so a new leak anywhere in the tree is reported the day
# it is written. The cases below are about the WRAPPER, and the load-bearing one
# is Test 6 for the same reason Test 2 is above: an allowlist that has only ever
# been seen letting things through is not known to stop anything.
#
# Test 9 is the one that keeps this ticket from recreating its own defect. The
# incident was an environmental failure — a full disk — read as a verdict about
# a branch. A wrapper that reported a leak in place of the wrapped suite's own
# failure would be the same substitution with the arrow reversed, so the suite's
# status has to win.
echo ""
echo "Test 6: POSITIVE CONTROL — the wrapper FAILS on a planted leak, and names it"
guard_log="$WORK/guard-leak.log"
if bash "$HERE/tmpdir-leak-guard.sh" bash -c \
        'mktemp -d "$TMPDIR/pogo-leaked-fixture.XXXXXX" >/dev/null' >"$guard_log" 2>&1; then
    fail "the wrapper exited 0 with a directory abandoned in its \$TMPDIR; every wrapper assertion below is vacuous"
elif grep -q "pogo-leaked-fixture" "$guard_log"; then
    pass "a planted leak fails the wrapper and is named in the report"
else
    fail "the wrapper failed but did not name the leaked entry:"
    sed -n '1,20p' "$guard_log" >&2
fi

echo ""
echo "Test 7: a run that leaves nothing passes"
if bash "$HERE/tmpdir-leak-guard.sh" true >"$WORK/guard-clean.log" 2>&1; then
    pass "a clean run exits 0"
else
    fail "a run that created nothing was reported as a leak:"
    sed -n '1,20p' "$WORK/guard-clean.log" >&2
fi

echo ""
echo "Test 8: the allowlisted one-per-host entries do not fail the wrapper"
# Named individually rather than in one command: an allowlist entry that stopped
# matching would otherwise hide behind the others.
allow_fail=0
for name in "$ROOT_NAME" pogo-prompts pogo-agents go-build9999; do
    if ! bash "$HERE/tmpdir-leak-guard.sh" bash -c "mkdir -p \"\$TMPDIR/$name\"" >"$WORK/guard-allow.log" 2>&1; then
        fail "the allowlisted entry $name was reported as a leak:"
        sed -n '1,20p' "$WORK/guard-allow.log" >&2
        allow_fail=1
    fi
done
[ "$allow_fail" -eq 0 ] && pass "pogo-test-tmp, pogo-prompts, pogo-agents and go-build* pass the allowlist"

echo ""
echo "Test 9: a FAILING wrapped command reports its OWN status, not the leak's"
# `|| guard_status=$?` rather than a bare call: this file runs under `set -e`,
# and the whole point of the case is a command that exits nonzero.
guard_status=0
bash "$HERE/tmpdir-leak-guard.sh" bash -c \
    'mktemp -d "$TMPDIR/pogo-leaked-fixture.XXXXXX" >/dev/null; exit 3' >"$WORK/guard-both.log" 2>&1 \
    || guard_status=$?
if [ "$guard_status" -eq 3 ]; then
    pass "the wrapped command's status 3 survives a simultaneous leak finding"
else
    fail "a wrapped command that exited 3 while leaking was reported as $guard_status; a leak is not a verdict about the branch"
    sed -n '1,20p' "$WORK/guard-both.log" >&2
fi

echo ""
echo "Test 10: the wrapper RECLAIMS — its private \$TMPDIR is gone afterwards"
# The guard exists to stop disk growth; one that measured the leak and left it
# on the volume would be adding to the pile it reports.
bash "$HERE/tmpdir-leak-guard.sh" bash -c \
    'mktemp -d "$TMPDIR/pogo-leaked-fixture.XXXXXX" >/dev/null; echo "$TMPDIR" >"'"$WORK"'/guard-path"' \
    >/dev/null 2>&1 || true
used="$(cat "$WORK/guard-path" 2>/dev/null || true)"
if [ -z "$used" ]; then
    fail "could not read back the private \$TMPDIR the wrapper used"
elif [ -e "$used" ]; then
    fail "the wrapper left its private \$TMPDIR behind: $used"
else
    pass "the private \$TMPDIR ($(basename "$used")) was removed"
fi

echo ""
echo "Test 11: the wrapper reclaims a READ-ONLY Go module cache"
# The bytes are here and `rm -rf` cannot touch them. A suite under this $TMPDIR
# can stand up a fake $HOME and shell out to `go build`, and Go writes its module
# cache READ-ONLY — 0444 files inside 0555 directories. Measured on this box:
# $TMPDIR/pogo-test-tmp held 148 such roots and 120 MB, each already selected for
# removal and removable by nothing. A reclaim that stops at the first EACCES is a
# reclaim that misses exactly the largest thing it was pointed at.
guard_ro_log="$WORK/guard-ro.log"
bash "$HERE/tmpdir-leak-guard.sh" bash -c '
    d="$TMPDIR/pogo-test-tmp/home/go/pkg/mod/example.com/dep@v1.0.0"
    mkdir -p "$d"
    printf "package dep\n" >"$d/dep.go"
    chmod 0444 "$d/dep.go"
    chmod 0555 "$d"
    echo "$TMPDIR" >"'"$WORK"'/guard-ro-path"
' >"$guard_ro_log" 2>&1 || true
ro_used="$(cat "$WORK/guard-ro-path" 2>/dev/null || true)"
if [ -z "$ro_used" ]; then
    fail "could not read back the private \$TMPDIR used for the read-only case"
elif [ -e "$ro_used" ]; then
    fail "a read-only module cache survived the wrapper's reclaim: $ro_used"
    chmod -R u+w "$ro_used" 2>/dev/null; rm -rf "$ro_used" 2>/dev/null
else
    pass "a 0444-in-0555 module cache was reclaimed with the private \$TMPDIR"
fi

echo ""
echo "Test 12: the wrapper REAPS a killed run's private \$TMPDIR"
# The wrapper's own removal rides on a trap, and nothing rides through SIGKILL —
# which the refinery does use on a gate it has given up on. That abandoned
# directory holds an ENTIRE suite's fixtures, so it is a worse leak than any this
# file reports, and it is the form this ticket's defect takes in the remedy.
#
# A dead pid is chosen rather than a killed child so the case is deterministic:
# the sweep's rule is ownership, and what has to be shown is that an entry whose
# owner is gone is removed while a LIVE owner's entry survives — both directions,
# because a sweep that removed everything would pass the first alone and would
# delete a concurrent gate's fixtures.
reap_base="$WORK/reap"
mkdir -p "$reap_base"
dead_pid="$(bash -c 'echo $$')"   # exited before this line was read
mkdir -p "$reap_base/pogo-gate-tmp.$dead_pid.DEAD00" "$reap_base/pogo-gate-tmp.$$.LIVE00"
POGO_TMPDIR_GUARD_BASE="$reap_base" bash "$HERE/tmpdir-leak-guard.sh" true >/dev/null 2>&1 || true
if [ -e "$reap_base/pogo-gate-tmp.$dead_pid.DEAD00" ]; then
    fail "a dead owner's abandoned \$TMPDIR survived the sweep"
elif [ ! -e "$reap_base/pogo-gate-tmp.$$.LIVE00" ]; then
    fail "the sweep removed a LIVE owner's \$TMPDIR — a concurrent gate's fixtures would go with it"
else
    pass "the dead owner's entry was reaped and the live owner's was kept"
fi

echo ""
echo "Test 13: the wrapper is WIRED — test.sh's whole-tree run goes through it"
# The property mg-de3c's slice did not have. Asserted on the file rather than by
# running the gate: a guard present in the tree and called by nothing is the
# limiting case of coverage-by-list.
#
# ASSERTED ON ORDER, NOT ON ADJACENCY (mg-cbc3). This used to require the guard
# and `go-test-budget.sh ./...` to be literally next to each other, which is a
# stricter claim than the property: what has to hold is that the whole-tree run
# happens INSIDE the guard, so the guard's $TMPDIR is the one the suites write
# into. mg-cbc3 inserted scripts/signal-sender.sh between them and this test
# failed — correctly reporting a break in a property that had not broken. A
# check that fires on a rearrangement it does not care about teaches the next
# person to widen it without reading it.
ROW="$(grep -E '^gate_step "Testing Go packages"' "$REPO_ROOT/test.sh" || true)"
case "$ROW" in
    *tmpdir-leak-guard.sh*go-test-budget.sh\ ./...*)
        pass "test.sh runs the whole-tree suite through tmpdir-leak-guard.sh" ;;
    "")
        fail "test.sh has no 'Testing Go packages' row at all" ;;
    *)
        fail "test.sh's whole-tree suite does not go through tmpdir-leak-guard.sh; the guard covers nothing: $ROW" ;;
esac

echo ""
echo "Test 14: POSITIVE CONTROL — a TEMPLATE-LESS mktemp leak is caught (mg-b9621)"
# Every planted leak above passes an explicit "$TMPDIR/..." template, which is
# the one spelling that was always visible. The shell suites spell it `mktemp
# -d`, and on darwin that IGNORES $TMPDIR: the fixture lands in the per-user
# confstr directory, outside the guard, and the guard reads clean. That is how
# 2,910 tmp.* directories (13.4G, mg-2f3ba's figures) piled up in the real
# $TMPDIR under a gate that never reported one.
#
# 14a is the control on the CONTROL: it shows the raw tool really does escape,
# on this host, so 14b passing means the shim did something rather than that
# mktemp happened to behave. Off darwin there is nothing to escape and 14a says
# so instead of asserting.
# Resolved PAST the shim: this suite itself runs under the guard in the gate,
# which puts lib/tmpdir-mktemp first on PATH, and 14a is about the raw tool.
# Walked by hand, not `PATH=... command -v`: bash answers that from its command
# hash, which already maps mktemp to the shim, whatever PATH the lookup is given.
real_mktemp=""
IFS=: read -r -a path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
    case "$d" in */lib/tmpdir-mktemp) continue ;; esac
    if [ -x "$d/mktemp" ]; then real_mktemp="$d/mktemp"; break; fi
done
escape_dir="$WORK/escape"
mkdir -p "$escape_dir"
escaped="$(TMPDIR="$escape_dir" "$real_mktemp" -d)"
case "$escaped" in
    "$escape_dir"/*)
        if [ "$(uname -s)" = Darwin ]; then
            fail "14a: darwin's mktemp -d honoured \$TMPDIR here ($escaped) — the premise of scripts/lib/tmpdir-mktemp has changed; re-measure before trusting 14b"
        else
            pass "14a: (not darwin) mktemp -d already honours \$TMPDIR; the shim is a pass-through here"
        fi ;;
    *)
        pass "14a: the raw mktemp -d escaped \$TMPDIR to $(dirname "$escaped") — invisible to a guard that only pins \$TMPDIR" ;;
esac
# Ours, by exact path, and outside the sandbox root, so removed here.
case "$escaped" in "$escape_dir"/*) ;; *) [ -n "$escaped" ] && rm -rf "$escaped" ;; esac

guard_bare_log="$WORK/guard-bare.log"
if bash "$HERE/tmpdir-leak-guard.sh" bash -c \
        'mktemp -d >/dev/null; mktemp >/dev/null; mktemp -t pogo-bare-prefix >/dev/null' >"$guard_bare_log" 2>&1; then
    fail "14b: the guard exited 0 over three template-less mktemp calls — shell fixtures are still invisible to it"
else
    n_named="$(grep -cE '^    (tmp|pogo-bare-prefix)\.' "$guard_bare_log" || true)"
    if [ "$n_named" -eq 3 ]; then
        pass "14b: bare 'mktemp -d', 'mktemp' and 'mktemp -t prefix' are all caught and named by the guard"
    else
        fail "14b: expected 3 named template-less entries, the guard named $n_named:"
        sed -n '1,20p' "$guard_bare_log" >&2
    fi
fi

# 14c: the shim must not move a call that already said where. An explicit
# template and an explicit -p are the caller's decision.
mkdir -p "$WORK/pdir"
shim_rel="$(cd "$WORK" && TMPDIR="$escape_dir" PATH="$HERE/lib/tmpdir-mktemp:$PATH" mktemp rel.XXXXXX)"
shim_p="$(TMPDIR="$escape_dir" PATH="$HERE/lib/tmpdir-mktemp:$PATH" mktemp -d -p "$WORK/pdir")"
if [ "$shim_rel" = "${shim_rel#/}" ] && [ -f "$WORK/$shim_rel" ] && [ "${shim_p#"$WORK/pdir/"}" != "$shim_p" ]; then
    pass "14c: an explicit template and an explicit -p pass through the shim unchanged"
else
    fail "14c: the shim moved a call that named its own location: template -> '$shim_rel', -p -> '$shim_p'"
fi

echo ""
echo "Test 15: EVERY gate row runs under the guard, not only the Go row (mg-b9621)"
# The guard was wired to one row and the leak was in the others. Asserted on
# test.sh rather than by running the gate, like Test 13, so a NEW row that skips
# the wrapper fails here the day it is added.
#
# One named exception, and it is named rather than pattern-matched so it cannot
# quietly cover a second row: gate-profile_test.sh abandons a
# pogo-gate-profile.* file per run, which is mg-c1a5e's defect in
# scripts/lib/gate-profile.sh. When that lands, wrap the row and delete this.
UNGUARDED_OK="scripts/gate-profile_test.sh"
unguarded=""
while IFS= read -r row; do
    case "$row" in
        *tmpdir-leak-guard.sh*) ;;
        *"$UNGUARDED_OK"*) ;;
        *) unguarded="$unguarded
    $row" ;;
    esac
done < <(grep -E '^gate_step ' "$REPO_ROOT/test.sh")
n_rows="$(grep -cE '^gate_step ' "$REPO_ROOT/test.sh")"
if [ "$n_rows" -lt 10 ]; then
    fail "parsed only $n_rows gate_step rows out of test.sh — the check below would be vacuous"
elif [ -n "$unguarded" ]; then
    fail "these gate rows run outside tmpdir-leak-guard.sh, so a fixture they abandon reaches the real \$TMPDIR unmeasured:$unguarded"
else
    pass "all $n_rows gate rows but the named mg-c1a5e exception run under tmpdir-leak-guard.sh"
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
