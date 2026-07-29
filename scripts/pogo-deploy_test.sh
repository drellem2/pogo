#!/usr/bin/env bash
# Tests for scripts/launchd/pogo-deploy.sh — the nightly redeploy TRIGGER.
#
# The runner's whole job is to decide whether to call pogo-self-deploy, so the
# interesting surface is its refusals: the two skips (outside-window, no-drift)
# and the three aborts (dirty tree, diverged tree, no token). Each of them, when
# wrong, produces the same visible symptom — a nightly that appears to run and
# deploys nothing — so each needs a test that can tell them apart.
#
# The script's main() is guarded by a BASH_SOURCE check, so sourcing it here
# exercises the helpers without firing a deploy.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="$HERE/launchd/pogo-deploy.sh"

RESULTS_FILE=$(mktemp)
WORK=$(mktemp -d)
trap 'rm -f "$RESULTS_FILE"; rm -rf "$WORK"' EXIT
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE"; }

# shellcheck source=/dev/null
source "$RUNNER"

# ---------------------------------------------------------------------------
# parse_window / in_window — the outside-window skip
# ---------------------------------------------------------------------------
# THIS IS THE SKIP THAT MATTERS MOST. StartCalendarInterval does not promise
# 03:00; it promises the job runs. A mac asleep at 03:00 gets its fire delivered
# on the next wake — 09:14, when Daniel opens the lid, or 14:30 mid-demo. A
# redeploy bounces the entire fleet, so a late fire must be dropped.
#
# And the mirror failure is just as real: a window too narrow means the job
# never deploys at all, which looks exactly like a job that was never installed.
parse_window "2-5" && [ "$WINDOW_START" = "2" ] && [ "$WINDOW_END" = "5" ] \
    && pass "parse_window reads the production 2-5 range" || fail "parse_window 2-5"

parse_window "" && fail "parse_window accepted an empty spec" || pass "parse_window rejects an empty spec"
parse_window "abc" && fail "parse_window accepted a non-numeric spec" || pass "parse_window rejects a non-numeric spec"
parse_window "5-2" && fail "parse_window accepted an inverted range" || pass "parse_window rejects an inverted range"

in_window 03 2 5 && pass "in_window: 03:00, the scheduled hour, deploys" || fail "in_window 03"
in_window 02 2 5 && pass "in_window: 02:00, the lower bound, is INSIDE (a wake at 02 should deploy)" || fail "in_window 02"
in_window 04 2 5 && pass "in_window: 04:00, a plausible Time Machine wake, deploys" || fail "in_window 04"

# Half-open at the top: 05:00 is the start of the day, not the end of the night.
in_window 05 2 5 && fail "in_window: 05:00 must be OUTSIDE (half-open range)" || pass "in_window: 05:00 is outside — the range is half-open"
in_window 09 2 5 && fail "in_window: 09:00 catch-up fire must be skipped" || pass "in_window: 09:00 lid-open catch-up fire is SKIPPED"
in_window 14 2 5 && fail "in_window: 14:00 mid-workday fire must be skipped" || pass "in_window: 14:00 mid-workday fire is SKIPPED"
in_window 23 2 5 && fail "in_window: 23:00 must be skipped" || pass "in_window: 23:00 is SKIPPED"

# `date +%H` emits ZERO-PADDED hours, and bash reads a leading zero as octal:
# "08" and "09" are invalid octal and abort the arithmetic. Untreated, the two
# most likely lid-open hours of the day would CRASH the runner instead of
# skipping — a non-zero exit that looks like a deploy failure and pages someone.
in_window 08 2 5 && fail "in_window: 08 must be skipped" || pass "in_window: '08' parses base-10 and is skipped (not an octal crash)"
in_window 09 2 5 && fail "in_window: 09 must be skipped" || pass "in_window: '09' parses base-10 and is skipped (not an octal crash)"
in_window 04 02 05 && pass "in_window: a zero-padded WINDOW bound parses base-10 too" || fail "in_window zero-padded bounds"

# ---------------------------------------------------------------------------
# The outside-window skip, end to end: exit 0, and NOTHING invoked
# ---------------------------------------------------------------------------
# The unit assertions above prove the predicate. This proves the wiring: that a
# late fire exits 0 (so launchd does not log a failure every morning) and gets
# there BEFORE resolving tools, sourcing a token, or touching git — an abort
# that has already fetched is an abort that has already had side effects.
OUT="$(POGO_DEPLOY_NOW=14 POGO_DEPLOY_SRC="$WORK/nonexistent" \
       HOME="$WORK" bash "$RUNNER" 2>&1)"; RC=$?
[ "$RC" -eq 0 ] && pass "outside-window run exits 0 (a deferred fire is not a failure)" || fail "outside-window exit was $RC"
printf '%s' "$OUT" | grep -q "outside \[2,5)" \
    && pass "outside-window run says WHY it skipped (a silent no-op is indistinguishable from a dead job)" || fail "outside-window reason not logged: $OUT"
printf '%s' "$OUT" | grep -qi "redeploy\|git fetch\|GH_TOKEN" \
    && fail "outside-window run touched the deploy path: $OUT" || pass "outside-window run touches nothing — no fetch, no token read, no redeploy"

# ---------------------------------------------------------------------------
# is_clean_verdict — the no-drift skip
# ---------------------------------------------------------------------------
# A fleet-wide bounce costs every agent its session. Doing that on a night when
# the running pogod is already at main is strictly worse than not running.
#
# The verdict is pogo-self-deploy's, not a second opinion computed here: reusing
# its classifier is what keeps this file a trigger rather than a rival deployer
# with its own idea of what "current" means. In particular its "clean" already
# accounts for CLI drift (mg-ddf1), which a naive running-rev == main check
# would miss.
is_clean_verdict "action    : clean — running == installed == main" \
    && pass "no-drift: a clean verdict is recognised (job skips, fleet keeps its sessions)" || fail "clean verdict not recognised"
is_clean_verdict "revision drift (repo: /x, ref: main)
  running        : abc
action    : clean — nothing owed" \
    && pass "no-drift: clean is found in a full multi-line check report" || fail "clean in multi-line report"

is_clean_verdict "action    : BUILD + RESTART owed: running == installed, both behind main" \
    && fail "BUILD+RESTART read as clean — the job would skip a real deploy" || pass "drift: BUILD + RESTART is NOT clean"
is_clean_verdict "action    : RESTART owed: installed == main, running stale" \
    && fail "RESTART read as clean" || pass "drift: RESTART owed is NOT clean"
is_clean_verdict "action    : BUILD owed (no restart): running pogod is main, but installed pogo is behind main" \
    && fail "CLI-only drift read as clean — cmd/pogo changes would stay dark indefinitely (mg-ddf1)" || pass "drift: CLI-only BUILD owed is NOT clean"
is_clean_verdict "" \
    && fail "empty check output read as clean — an unreachable pogod would look 'current' and skip forever" || pass "drift: empty output is NOT clean"
# The word must be the VERDICT, not a mention of it. A report that says the
# working tree is clean is not a report that says nothing is owed.
is_clean_verdict "note: the working tree is clean
action    : BUILD + RESTART owed" \
    && fail "matched 'clean' outside the action line" || pass "drift: 'clean' elsewhere in the report does not fake a clean verdict"

# ---------------------------------------------------------------------------
# describe_exit — the outcome classifier
# ---------------------------------------------------------------------------
# At 08:00 the operator has a number and a log. Collapsing every non-zero into
# "the deploy failed" is what makes a nightly unactionable: exit 9 and exit 5
# demand opposite responses (do nothing / pogod may be down).
[ "$(describe_exit 0)" = "OK" ] && pass "describe_exit 0 = OK" || fail "describe_exit 0"
case "$(describe_exit 9)" in
    *"do_prove RED"*"UNTOUCHED"*) pass "describe_exit 9 names do_prove RED AND says the running pogod survived it" ;;
    *) fail "describe_exit 9: $(describe_exit 9)" ;;
esac
case "$(describe_exit 4)" in *"BUILD FAILED"*) pass "describe_exit 4 names the build" ;; *) fail "describe_exit 4" ;; esac
case "$(describe_exit 5)" in *"DOWN"*) pass "describe_exit 5 warns pogod may be down (the one code that means an outage)" ;; *) fail "describe_exit 5" ;; esac
# exit 3 is confirm() refusing a non-interactive caller. If the nightly ever
# sees it, the wrapper stopped passing --yes — a bug HERE, and the message has
# to say so rather than send someone hunting through the deploy script.
case "$(describe_exit 3)" in *"this wrapper"*) pass "describe_exit 3 blames this wrapper for a missing --yes" ;; *) fail "describe_exit 3" ;; esac
case "$(describe_exit 42)" in *unclassified*) pass "describe_exit falls back for an unknown code" ;; *) fail "describe_exit 42" ;; esac

# ---------------------------------------------------------------------------
# load_gh_token — runtime, from a file, never logged
# ---------------------------------------------------------------------------
ZE="$WORK/zshenv"
printf 'export PATH=/nope\nexport GH_TOKEN=ghp_secretvalue123\nexport OTHER=1\n' > "$ZE"
unset GH_TOKEN
# NOT a command substitution: that would run load_gh_token in a subshell, where
# an export cannot be observed — and "it exported the value" is half of what
# this function is for.
TOKLOG="$WORK/tok.log"
load_gh_token "$ZE" > "$TOKLOG" 2>&1; TOKRC=$?
TOKOUT="$(cat "$TOKLOG")"
[ "$TOKRC" -eq 0 ] && pass "load_gh_token reads the export line out of a zshenv-shaped file" || fail "load_gh_token rc=$TOKRC: $TOKOUT"
[ "${GH_TOKEN:-}" = "ghp_secretvalue123" ] && pass "load_gh_token exports the value into the environment" || fail "GH_TOKEN not exported"
# The value must never reach the log. This job's stdout is a file on disk that
# accumulates across every nightly run, and a token there is a token disclosed
# for as long as nobody reads the log closely.
printf '%s' "$TOKOUT" | grep -q "ghp_secretvalue123" \
    && fail "load_gh_token LOGGED the token value" || pass "load_gh_token never logs the value (only that it is present)"
# It must read ONLY the one variable. Sourcing the whole file with bash would
# also apply that `export PATH=/nope`, which would strip go off PATH and turn
# the next build into the exact 07-23 "go: command not found" failure.
[ "$PATH" != "/nope" ] && pass "load_gh_token does not apply the rest of the file (PATH survived)" || fail "load_gh_token clobbered PATH by sourcing the whole file"

unset GH_TOKEN
printf 'export OTHER=1\n' > "$WORK/notoken"
load_gh_token "$WORK/notoken" >/dev/null 2>&1 \
    && fail "load_gh_token succeeded with no GH_TOKEN line" || pass "load_gh_token fails when the file has no GH_TOKEN (the mg-03ea class, caught before deploying)"
load_gh_token "$WORK/does-not-exist" >/dev/null 2>&1 \
    && fail "load_gh_token succeeded on a missing file" || pass "load_gh_token fails on an unreadable file"
# An empty assignment is not a token; treating it as one gets you an
# unauthenticated gh three steps later, where the error names nothing useful.
unset GH_TOKEN
printf 'export GH_TOKEN=\n' > "$WORK/emptytoken"
load_gh_token "$WORK/emptytoken" >/dev/null 2>&1 \
    && fail "load_gh_token accepted an empty value" || pass "load_gh_token rejects an empty value"
# An operator running this by hand from a shell already has the token; reading
# the file again would silently override what they set.
GH_TOKEN=from-the-environment
load_gh_token "$WORK/notoken" >/dev/null 2>&1 \
    && [ "$GH_TOKEN" = "from-the-environment" ] \
    && pass "load_gh_token prefers an already-set GH_TOKEN over the file" || fail "load_gh_token clobbered an env-provided token"
unset GH_TOKEN

# ---------------------------------------------------------------------------
# resolve_git — a WORKING git, not merely a present one (mg-36e3)
# ---------------------------------------------------------------------------
# git used to be pinned to /usr/bin/git, since git ships in /usr/bin on every
# macOS. It does — but that path is the Command Line Tools SHIM, and a damaged
# Xcode makes it fail every call, `git --version` included, with "unable to
# locate xcodebuild" and exit 71. On such a box the nightly cannot clone, cannot
# fetch and cannot read a rev: sync_src aborts, the job alerts, and nothing
# deploys — the silent nightly all over again, from a binary that passes every
# check short of running it.
#
# So these tests are all about the difference between "exists" and "runs". A
# fake that is executable and on PATH but broken must be REJECTED.
FAKEBIN="$WORK/fakebin"; mkdir -p "$FAKEBIN"
# Reproduces the real failure: exit 71, diagnostic on stderr, nothing on stdout.
cat > "$FAKEBIN/brokengit" <<'EOF'
#!/bin/sh
echo "Error loading required libraries. git: error: unable to locate xcodebuild" >&2
exit 71
EOF
cat > "$FAKEBIN/workinggit" <<'EOF'
#!/bin/sh
[ "$1" = "--version" ] && { echo "git version 9.9.9"; exit 0; }
exit 0
EOF
chmod +x "$FAKEBIN/brokengit" "$FAKEBIN/workinggit"

SAVED_GIT="${GIT:-}"

# The core regression. The old `GIT="${GIT:-/usr/bin/git}"` would have taken this
# path verbatim and failed on the very first clone.
GIT="$FAKEBIN/brokengit"
[ -x "$FAKEBIN/brokengit" ] \
    && pass "resolve_git premise: the broken fake IS executable (so -x cannot catch it)" \
    || fail "broken fake is not executable"
resolve_git >/dev/null 2>&1 \
    && [ "$GIT" != "$FAKEBIN/brokengit" ] \
    && pass "resolve_git REJECTS a pinned-but-broken git and falls through to a working one" \
    || fail "resolve_git accepted a broken git, or found no working git at all"
"$GIT" --version 2>/dev/null | grep -q '^git version' \
    && pass "resolve_git left GIT pointing at a git that actually runs" || fail "resolved GIT does not run"

# An operator pin that works is honoured verbatim — that is the escape hatch for
# a box with several gits installed.
GIT="$FAKEBIN/workinggit"
resolve_git >/dev/null 2>&1 && [ "$GIT" = "$FAKEBIN/workinggit" ] \
    && pass "resolve_git honours a working operator-pinned \$GIT" || fail "resolve_git ignored a valid pin"

# Unpinned resolution must still land on a working git.
GIT=""
resolve_git >/dev/null 2>&1 && [ -n "$GIT" ] \
    && "$GIT" --version 2>/dev/null | grep -q '^git version' \
    && pass "resolve_git resolves a working git with no pin at all" || fail "resolve_git unpinned"

# And the runner must not reintroduce the pin.
grep -qE '^[[:space:]]*GIT="\$\{GIT:-/usr/bin/git\}"' "$RUNNER" \
    && fail "the runner has gone back to hardcoding /usr/bin/git" \
    || pass "the runner does not hardcode /usr/bin/git (existence is not a health check)"

GIT="$SAVED_GIT"

# ---------------------------------------------------------------------------
# sync_src — never clobber, never diverge
# ---------------------------------------------------------------------------
mkrepo() {
    local d="$1"
    git init --quiet -b main "$d"
    git -C "$d" config user.email t@t; git -C "$d" config user.name t
    echo one > "$d/f"; git -C "$d" add f; git -C "$d" commit --quiet -m one
}
UPSTREAM="$WORK/upstream"
mkrepo "$UPSTREAM"

# Fresh clone, then a clean fast-forward: the ordinary night.
# GIT comes from resolve_git, not a hardcoded /usr/bin/git: on a box with a
# damaged Xcode CLT the pinned shim fails every call, and these tests then fail
# for a reason that has nothing to do with what they are checking (mg-36e3).
SRC="$WORK/src"; DEPLOY_REF=main
resolve_git >/dev/null 2>&1 || { echo "FATAL: no working git for the sync_src tests"; exit 1; }
POGO_DEPLOY_REMOTE="$UPSTREAM"; DEPLOY_REMOTE="$UPSTREAM"
sync_src >/dev/null 2>&1 && pass "sync_src bootstraps the dedicated checkout on first run" || fail "sync_src bootstrap"
echo two > "$UPSTREAM/f"; git -C "$UPSTREAM" commit --quiet -am two
sync_src >/dev/null 2>&1 \
    && [ "$(git -C "$SRC" rev-parse HEAD)" = "$(git -C "$UPSTREAM" rev-parse main)" ] \
    && pass "sync_src fast-forwards the checkout to origin/main" || fail "sync_src ff"

# A DIRTY tree aborts and is left EXACTLY as it was. This is the clobber guard:
# the dedicated checkout should never be dirty, so if it is, something nobody
# has explained is happening in it and a reset would destroy the evidence. The
# same code path is what protects a dev tree if POGO_DEPLOY_SRC is ever pointed
# at one by accident.
echo local-edit > "$SRC/f"
BEFORE="$(cat "$SRC/f")"
sync_src >/dev/null 2>&1 && fail "sync_src proceeded on a DIRTY tree" || pass "sync_src ABORTS on a dirty tree rather than resetting it"
[ "$(cat "$SRC/f")" = "$BEFORE" ] && pass "sync_src left the dirty edit untouched (no clobber)" || fail "sync_src clobbered a local edit"
git -C "$SRC" checkout --quiet -- f

# A DIVERGED tree aborts too: merging would deploy commits nobody meant to
# build, and resetting would erase them. --ff-only refuses both.
echo local-commit > "$SRC/g"; git -C "$SRC" add g
git -C "$SRC" -c user.email=t@t -c user.name=t commit --quiet -m local
echo three > "$UPSTREAM/f"; git -C "$UPSTREAM" commit --quiet -am three
sync_src >/dev/null 2>&1 && fail "sync_src merged a DIVERGED tree" || pass "sync_src ABORTS on divergence (--ff-only, never a reset)"
[ -f "$SRC/g" ] && pass "sync_src preserved the diverging commit for inspection" || fail "sync_src destroyed local commits"

# ---------------------------------------------------------------------------
# missing_ids — the post-bounce mail-check re-check
# ---------------------------------------------------------------------------
# On 07-17 the schedules were present right after the bounce and were reaped
# minutes later as agent_gone, leaving crew with no mail loop while pogod was up
# and every health signal read green. "Could not read" and "nothing is missing"
# must therefore be different answers — collapsing them is how the degraded
# fleet stayed invisible.
[ -z "$(missing_ids "$(printf 'mail-check-a\nmail-check-b')" "$(printf 'mail-check-a\nmail-check-b')")" ] \
    && pass "missing_ids: nothing lost across the bounce" || fail "missing_ids identical sets"
[ "$(missing_ids "$(printf 'mail-check-a\nmail-check-b')" "mail-check-a")" = "mail-check-b" ] \
    && pass "missing_ids names the schedule that did not come back" || fail "missing_ids lost id"
[ -z "$(missing_ids "mail-check-a" "$(printf 'mail-check-a\nmail-check-new')")" ] \
    && pass "missing_ids ignores schedules that appeared after the bounce" || fail "missing_ids new id"
[ "$(missing_ids "?" "mail-check-a")" = "?" ] \
    && pass "missing_ids: an unreadable BEFORE is '?', not 'nothing lost'" || fail "missing_ids ? pre"
[ "$(missing_ids "mail-check-a" "?")" = "?" ] \
    && pass "missing_ids: an unreadable AFTER is '?', not 'nothing lost'" || fail "missing_ids ? post"

# ---------------------------------------------------------------------------
# --force is not reachable
# ---------------------------------------------------------------------------
# The two things --force overrides are killing live polecats and bouncing a
# fleet whose idleness could not be established. Neither is a call an unattended
# 03:00 job gets to make, so the flag is not passed, not plumbed, and not
# settable by env — grep is the enforcement because the failure is a one-word
# edit somebody makes at 2am to get a stuck deploy through.
#
# The check targets INVOCATIONS of the deploy script rather than the string
# anywhere in the file. The header explains at length why the flag is absent and
# the RED alert tells the operator not to reach for it — a check that cannot
# tell an explanation from a use would have to be deleted the first time
# somebody documented the rule, and then it would not be there for the edit it
# was written to catch.
grep -E '^[^#]*"\$DEPLOY"' "$RUNNER" | grep -q -- '--force' \
    && fail "an invocation of pogo-self-deploy passes --force" \
    || pass "no invocation of pogo-self-deploy passes --force (a bad deploy is never forced through)"
# ...and no env var can smuggle it in either.
grep -v '^[[:space:]]*#' "$RUNNER" | grep -qE 'POGO_DEPLOY_FORCE|FORCE=' \
    && fail "the runner has force plumbing an env var could flip" \
    || pass "the runner has no force plumbing at all — --force is unreachable, not merely unused"
grep -q -- 'redeploy --yes' "$RUNNER" \
    && pass "the runner passes --yes (confirm() exits 3 without a tty)" || fail "the runner does not pass --yes"

# ---------------------------------------------------------------------------
echo
echo "--- Results ---"
grep -c '^PASS' "$RESULTS_FILE" | sed 's/^/passed: /'
if grep -q '^FAIL' "$RESULTS_FILE"; then
    grep '^FAIL' "$RESULTS_FILE"
    exit 1
fi
echo "all pogo-deploy trigger tests passed"
