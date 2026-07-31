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
parse_window "2-6" && [ "$WINDOW_START" = "2" ] && [ "$WINDOW_END" = "6" ] \
    && pass "parse_window reads the production 2-6 range" || fail "parse_window 2-6"

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
printf '%s' "$OUT" | grep -q "outside \[2,6)" \
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
# resolve_git — a WORKING git, not merely a present one (mg-b72a)
# ---------------------------------------------------------------------------
# `mg` has always been proved by running it; `git` was pinned to /usr/bin/git on
# the reasoning that git ships in /usr/bin on every macOS. It does — but that
# path is the Command Line Tools SHIM, and a damaged install behind it makes it
# fail every call, `git --version` included, with "unable to locate xcodebuild"
# and exit 71, while remaining executable and on PATH. `-x` and `command -v`
# cannot tell that binary from a healthy one.
#
# These assertions are therefore all about the gap between "exists" and "runs",
# and they are HERMETIC: the candidate list is substituted for fakes, so the
# verdicts below do not depend on the health of this host's git. That matters
# more than it sounds. On any host with a working git the real candidate list
# can never produce a rejection, so a suite that only ever ran against the real
# list would report green whether or not the execution check existed at all.
FAKEBIN="$WORK/fakebin"; mkdir -p "$FAKEBIN"

# Reproduces the real failure exactly: exit 71, diagnostic on stderr, and
# NOTHING on stdout.
cat > "$FAKEBIN/brokengit" <<'EOF'
#!/bin/sh
echo "Error loading required libraries. git: error: unable to locate xcodebuild" >&2
exit 71
EOF
# A subtler impostor: exits 0, says nothing. Proves the test is the version
# string and not merely the exit status.
cat > "$FAKEBIN/silentgit" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$FAKEBIN/workinggit" <<'EOF'
#!/bin/sh
[ "$1" = "--version" ] && { echo "git version 9.9.9"; exit 0; }
exit 0
EOF
chmod +x "$FAKEBIN/brokengit" "$FAKEBIN/silentgit" "$FAKEBIN/workinggit"

SAVED_GIT="${GIT:-}"
REAL_GIT_CANDIDATES="$(declare -f git_candidates)"
# Substitute the candidate list. Everything until the restore below is decided
# entirely by the fakes.
fake_candidates() { printf '%s\n' "$@"; }

[ -x "$FAKEBIN/brokengit" ] \
    && pass "resolve_git premise: the broken fake IS executable (so -x cannot catch it)" \
    || fail "broken fake is not executable"
"$FAKEBIN/brokengit" --version >"$WORK/bg.out" 2>/dev/null; RC=$?
[ "$RC" -eq 71 ] && [ ! -s "$WORK/bg.out" ] \
    && pass "resolve_git premise: the broken fake reproduces exit 71 with empty stdout" \
    || fail "broken fake does not reproduce the failure (rc=$RC)"

# THE decisive assertion. If the execution check were removed — if resolve_git
# went back to accepting the first candidate that exists — this returns 0 and
# this test goes red. It cannot be satisfied by the host's git being healthy,
# because the host's git is not in this list.
GIT=""
git_candidates() { fake_candidates "$FAKEBIN/brokengit"; }
resolve_git >/dev/null 2>&1 \
    && fail "resolve_git ACCEPTED a git that exits 71 — existence was treated as health" \
    || pass "resolve_git REFUSES a list whose only git is present-but-broken (exit 71)"

# ...and it must be a refusal, not a silent fallthrough that leaves the caller
# holding a path it will discover is dead three git calls later.
GIT=""
git_candidates() { fake_candidates "$FAKEBIN/brokengit"; }
resolve_git >/dev/null 2>&1
[ -z "$GIT" ] && pass "a failed resolve_git leaves GIT unset rather than pointing at the broken candidate" \
    || fail "resolve_git set GIT=$GIT despite failing"

# A broken candidate ahead of a working one is SKIPPED, not fatal — the shim
# staying in the list is what keeps a CLT-only box deploying.
GIT=""
git_candidates() { fake_candidates "$FAKEBIN/brokengit" "$FAKEBIN/workinggit"; }
resolve_git >/dev/null 2>&1 && [ "$GIT" = "$FAKEBIN/workinggit" ] \
    && pass "resolve_git steps over an executable-but-broken candidate to a working one" \
    || fail "resolve_git did not fall through to the working git (GIT=$GIT)"

# Exiting 0 is not proof of anything; printing a version is.
GIT=""
git_candidates() { fake_candidates "$FAKEBIN/silentgit" "$FAKEBIN/workinggit"; }
resolve_git >/dev/null 2>&1 && [ "$GIT" = "$FAKEBIN/workinggit" ] \
    && pass "resolve_git rejects a candidate that exits 0 but prints no version" \
    || fail "resolve_git accepted a silent candidate (GIT=$GIT)"

eval "$REAL_GIT_CANDIDATES"   # back to the real candidate list

# An operator pin that works is honoured verbatim — the escape hatch for a box
# with several gits installed. This one goes through the real list, whose first
# entry is $GIT.
GIT="$FAKEBIN/workinggit"
resolve_git >/dev/null 2>&1 && [ "$GIT" = "$FAKEBIN/workinggit" ] \
    && pass "resolve_git honours a working operator-pinned \$GIT" || fail "resolve_git ignored a valid pin"

# And unpinned resolution on this host must still land on a git that runs.
GIT=""
resolve_git >/dev/null 2>&1 && [ -n "$GIT" ] \
    && "$GIT" --version 2>/dev/null | grep -q '^git version' \
    && pass "resolve_git resolves a working git on this host with no pin at all" || fail "resolve_git unpinned"

# Ratchets: the pin must not come back, and the resolver must actually be called
# — an unreferenced resolve_git is a health check nobody runs.
grep -qE '^[[:space:]]*GIT="\$\{GIT:-/usr/bin/git\}"' "$RUNNER" \
    && fail "the runner has gone back to hardcoding /usr/bin/git" \
    || pass "the runner does not hardcode /usr/bin/git (existence is not a health check)"
grep -qE '^[[:space:]]*resolve_git[[:space:]]*\|\|' "$RUNNER" \
    && pass "main() actually calls resolve_git and handles its failure" \
    || fail "resolve_git is defined but never called"

GIT="$SAVED_GIT"

# ---------------------------------------------------------------------------
# --help — bounded by the `set -u` sentinel, not by a line number
# ---------------------------------------------------------------------------
# The header is the documentation, and it grows. A hardcoded `sed -n '2,80p'`
# had already drifted past the end of the ENV list, so --help was truncating
# mid-list — the silent kind of doc rot, since the output still looks complete.
HELP="$(bash "$RUNNER" --help 2>&1)"; RC=$?
[ "$RC" -eq 0 ] && pass "--help exits 0" || fail "--help exit was $RC"
printf '%s' "$HELP" | grep -q 'POGO_DEPLOY_SRC' \
    && pass "--help reaches the start of the ENV list" || fail "--help lost the ENV list"
printf '%s' "$HELP" | grep -q 'POGO_DEPLOY_NOW' \
    && pass "--help reaches the LAST ENV entry (the truncation this replaces)" \
    || fail "--help is still truncating the ENV list"
printf '%s' "$HELP" | grep -q '^set -u' \
    && fail "--help leaked the sentinel line into its output" \
    || pass "--help stops at the sentinel without printing it"
printf '%s' "$HELP" | grep -qE '^(HOME|SRC|GIT)=' \
    && fail "--help ran past the header into shell code" || pass "--help prints the header only, no shell code"
grep -qE "sed -n '2,[0-9]+p'" "$RUNNER" \
    && fail "--help is bounded by a line number again (it will drift again)" \
    || pass "--help is bounded by the sentinel, not a line number"

# ---------------------------------------------------------------------------
# sync_src — never clobber, never diverge
# ---------------------------------------------------------------------------
# These tests need a git that works, and they used to name one: `GIT=/usr/bin/git`
# in the fixture, with the repo fixtures calling a bare `git` off PATH. That made
# the suite's verdict depend on the health of one hardcoded host path — the very
# assumption the block above exists to reject — and on a box whose CLT shim was
# broken these tests would have gone red for a reason that has nothing to do with
# clobbering or divergence. Resolve once, use the result everywhere (mg-b72a).
resolve_git >/dev/null 2>&1 || { echo "FATAL: no working git for the sync_src tests"; exit 1; }

mkrepo() {
    local d="$1"
    "$GIT" init --quiet -b main "$d"
    "$GIT" -C "$d" config user.email t@t; "$GIT" -C "$d" config user.name t
    echo one > "$d/f"; "$GIT" -C "$d" add f; "$GIT" -C "$d" commit --quiet -m one
}
UPSTREAM="$WORK/upstream"
mkrepo "$UPSTREAM"

# Fresh clone, then a clean fast-forward: the ordinary night.
SRC="$WORK/src"; DEPLOY_REF=main
POGO_DEPLOY_REMOTE="$UPSTREAM"; DEPLOY_REMOTE="$UPSTREAM"
sync_src >/dev/null 2>&1 && pass "sync_src bootstraps the dedicated checkout on first run" || fail "sync_src bootstrap"
echo two > "$UPSTREAM/f"; "$GIT" -C "$UPSTREAM" commit --quiet -am two
sync_src >/dev/null 2>&1 \
    && [ "$("$GIT" -C "$SRC" rev-parse HEAD)" = "$("$GIT" -C "$UPSTREAM" rev-parse main)" ] \
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
"$GIT" -C "$SRC" checkout --quiet -- f

# A DIVERGED tree aborts too: merging would deploy commits nobody meant to
# build, and resetting would erase them. --ff-only refuses both.
echo local-commit > "$SRC/g"; "$GIT" -C "$SRC" add g
"$GIT" -C "$SRC" -c user.email=t@t -c user.name=t commit --quiet -m local
echo three > "$UPSTREAM/f"; "$GIT" -C "$UPSTREAM" commit --quiet -am three
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
# drain_budget — the window-derived drain (mg-8f7e)
# ---------------------------------------------------------------------------
# The 2026-07-31 nightly exited 7 with three polecats still working at 1h33m,
# 1h19m and 38m uptime, against a 1800s budget. Two of the three had been
# running longer than the whole budget before the drain started, so no arrival
# rate and no dispatch race is needed to explain the failure — the constant was
# simply smaller than the work.
#
# Replacing it with a bigger constant only moves the wall. These assertions pin
# the property that makes the number self-maintaining: the budget is whatever is
# left of the deploy window after reserving time to build and bounce, so it
# tracks the window an operator already reasons about and needs no recalibrating
# when the queue shifts toward longer items.
parse_window "2-6" || fail "parse_window 2-6 (fixture for the budget assertions)"
B_END=6; B_RESERVE=1200; B_MAX=7200; B_MIN=600

[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 3 0 0)" = "7200" ] \
    && pass "drain_budget: the 03:00 fire gets the 7200s cap (2h), not 1800s" \
    || fail "drain_budget 03:00 = $(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 3 0 0)"

# The ticket's own number, as a test. 1h33m = 5580s was the longest blocker on
# 07-31; a budget that cannot outlast it cannot land a deploy on a working fleet.
[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 3 0 0)" -gt 5580 ] \
    && pass "drain_budget: the 03:00 fire outlasts the 1h33m polecat that blocked 07-31 (1800s did not)" \
    || fail "drain_budget 03:00 does not outlast the 07-31 blocker"

[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 4 0 0)" = "6000" ] \
    && pass "drain_budget: a 04:00 retry gets what the window actually has left (6000s), not a fixed number" \
    || fail "drain_budget 04:00 = $(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 4 0 0)"

[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 5 0 0)" = "2400" ] \
    && pass "drain_budget: a 05:00 retry gets 2400s — the window shrinks the budget by itself" \
    || fail "drain_budget 05:00 = $(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 5 0 0)"

# THE FLOOR. A drain holds draining=true for its whole length and nothing
# dispatches while it does, so an attempt that cannot finish costs the fleet
# real work and delivers nothing. Below the floor the answer is "not tonight".
[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 5 50 0)" = "0" ] \
    && pass "drain_budget: 05:50 is under the floor — 0, meaning skip, not a doomed attempt" \
    || fail "drain_budget 05:50 = $(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 5 50 0)"

# ...and it must be ZERO, never negative. A negative handed to --drain-timeout
# reads as an already-expired deadline: the drain would return instantly and the
# run would exit 7, manufacturing the exact failure this ticket is about out of
# a situation where nothing was wrong except the hour.
[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 7 0 0)" = "0" ] \
    && pass "drain_budget: past the window end it is 0, never a negative that would fake an instant exit 7" \
    || fail "drain_budget past-window = $(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 7 0 0)"

# The cap. Without it the budget would grow with the window, and a deploy that
# waits all night is a night with no dispatch at all.
[ "$(drain_budget 12 0 7200 600 2 0 0)" = "7200" ] \
    && pass "drain_budget: MAX_DRAIN caps a wide window — patience is bounded, not unlimited" \
    || fail "drain_budget cap"

# Zero-padded clock fields, the octal trap in_window already carries a test for.
[ "$(drain_budget $B_END $B_RESERVE $B_MAX $B_MIN 03 08 09)" = "7200" ] \
    && pass "drain_budget: '08'/'09' clock fields parse base-10 (not an octal crash)" \
    || fail "drain_budget zero-padded fields"

# ---------------------------------------------------------------------------
# next_fire_hour / retry_will_follow — is a retry REALLY coming?
# ---------------------------------------------------------------------------
# The RED alert asserted "did not retry" as a fact. Now that the plist has three
# fires the claim has to be computed, and it is only true when all three of
# these hold: the exit is the retryable one, a fire remains tonight, and that
# fire would get a usable budget. A retry announced and not delivered is worse
# than no retry — it tells a reader at 08:00 that the deploy is still coming.
[ "$(next_fire_hour 2 "3 4 5")" = "3" ] && pass "next_fire_hour: 02:xx -> 3" || fail "next_fire_hour 2"
[ "$(next_fire_hour 3 "3 4 5")" = "4" ] && pass "next_fire_hour: 03:xx -> 4 (strictly after, not the current fire again)" || fail "next_fire_hour 3"
next_fire_hour 5 "3 4 5" >/dev/null \
    && fail "next_fire_hour: 05:xx claimed another fire tonight" \
    || pass "next_fire_hour: after the last fire there is none left tonight"

FIRE_HOURS="3 4 5"; WINDOW_END=6; RESERVE=1200; MAX_DRAIN=7200; MIN_DRAIN=600
POGO_DEPLOY_NOW=3 retry_will_follow 7 \
    && pass "retry_will_follow: a 03:00 stall is followed by the 04:00 fire" || fail "retry_will_follow 03:00 rc=7"
POGO_DEPLOY_NOW=3 retry_will_follow 4 \
    && fail "retry_will_follow: a BUILD failure claimed a retry — it will fail identically and mail twice" \
    || pass "retry_will_follow: only exit 7 retries; a build failure settles the night"
POGO_DEPLOY_NOW=3 retry_will_follow 9 \
    && fail "retry_will_follow: a control-suite RED claimed a retry" \
    || pass "retry_will_follow: a do_prove RED settles the night (the artifact will not change by 04:00)"
POGO_DEPLOY_NOW=5 retry_will_follow 7 \
    && fail "retry_will_follow: a 05:00 stall claimed a fire that does not exist" \
    || pass "retry_will_follow: the last fire of the night knows it is the last — the alert can say so truthfully"

# The third condition, and the one easiest to forget: a fire exists but the
# window will have nothing left for it.
MIN_DRAIN=99999
POGO_DEPLOY_NOW=3 retry_will_follow 7 \
    && fail "retry_will_follow: promised a retry the budget floor would skip" \
    || pass "retry_will_follow: a fire that would be skipped for lack of window is not a retry"
MIN_DRAIN=600

# ---------------------------------------------------------------------------
# attempt_disposition — telling the two nights apart
# ---------------------------------------------------------------------------
# From inside a 04:00 fire, a night where 03:00 stalled on a busy fleet and a
# night where 03:00 failed to build look identical: both left a non-zero exit
# and an inert main. Only the first is worth repeating. Getting this wrong in
# the permissive direction mails a duplicate RED every hour; getting it wrong in
# the strict direction silently reinstates the 24-hour gap this ticket is about.
[ "$(attempt_disposition 2026-07-31 "")" = "first" ] \
    && pass "attempt_disposition: no record -> first attempt" || fail "disposition empty"
[ "$(attempt_disposition 2026-07-31 "2026-07-30 1 7")" = "first" ] \
    && pass "attempt_disposition: LAST night's record does not settle tonight" || fail "disposition stale date"
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 7")" = "retry" ] \
    && pass "attempt_disposition: tonight's drain stall -> RETRY (the failure a later fire can fix)" || fail "disposition rc=7"
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 4")" = "settled" ] \
    && pass "attempt_disposition: tonight's BUILD failure -> settled (no duplicate alert)" || fail "disposition rc=4"
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 9")" = "settled" ] \
    && pass "attempt_disposition: tonight's control-suite RED -> settled" || fail "disposition rc=9"
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 0")" = "settled" ] \
    && pass "attempt_disposition: a night that already DEPLOYED is settled — one bounce per night" || fail "disposition rc=0"

# A corrupt record must fail toward attempting, not toward skipping. The cost of
# the first is one extra deploy attempt; the cost of the second is a nightly
# that silently stops running and looks exactly like one that was uninstalled.
[ "$(attempt_disposition 2026-07-31 "garbage")" = "first" ] \
    && pass "attempt_disposition: an unparseable record reads as 'first' — a corrupt stamp cannot disable the nightly" || fail "disposition garbage"
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 seven")" = "first" ] \
    && pass "attempt_disposition: a non-numeric exit code reads as 'first', not as settled" || fail "disposition non-numeric rc"

[ "$(stamp_attempts "2026-07-31 2 7" 2026-07-31)" = "2" ] \
    && pass "stamp_attempts: counts tonight's attempts (the alert says which one it is)" || fail "stamp_attempts tonight"
[ "$(stamp_attempts "2026-07-30 2 7" 2026-07-31)" = "0" ] \
    && pass "stamp_attempts: last night's count does not carry over" || fail "stamp_attempts stale"
[ "$(stamp_attempts "" 2026-07-31)" = "0" ] && pass "stamp_attempts: no record is 0" || fail "stamp_attempts empty"

# ---------------------------------------------------------------------------
# remedy_for_exit — the alert must explain the exit it actually got (mg-8f7e)
# ---------------------------------------------------------------------------
# THE REGRESSION. The RED alert carried one paragraph, about exit 9, printed
# under every exit code. On the 07-31 exit 7 it told the reader the control
# suite had gone RED and "the artifact is the problem" — for a failure that
# never reaches the build and has no artifact. The closing advice happened to be
# correct, which is the worse case: a reader who trusts the stated reasoning
# goes and reads a build log that does not exist.
R7="$(remedy_for_exit 7)"; R9="$(remedy_for_exit 9)"; R4="$(remedy_for_exit 4)"

printf '%s' "$R7" | grep -qi 'drain' \
    && pass "remedy(7) names the drain — the thing that actually failed" || fail "remedy(7) does not mention the drain"
printf '%s' "$R7" | grep -qi 'control suite' \
    && fail "remedy(7) still blames the control suite — that is exit 9's story" \
    || pass "remedy(7) does NOT blame the control suite (it never ran)"
printf '%s' "$R7" | grep -qi 'the artifact is the problem' \
    && fail "remedy(7) still says the artifact is the problem — on exit 7 no artifact was built" \
    || pass "remedy(7) does NOT send the reader to a build log that does not exist"
printf '%s' "$R9" | grep -qi 'control suite' \
    && pass "remedy(9) keeps the exit-9 explanation, where it is true" || fail "remedy(9) lost its explanation"
printf '%s' "$R4" | grep -qi 'build' \
    && pass "remedy(4) names the build" || fail "remedy(4) does not mention the build"

[ "$R7" != "$R9" ] && [ "$R4" != "$R9" ] && [ "$R4" != "$R7" ] \
    && pass "remedy_for_exit: 4, 7 and 9 get DIFFERENT paragraphs — one paragraph for every code is the defect" \
    || fail "remedy_for_exit returns the same text for different exits"

# Every code the wrapper can report must produce something. A missing case arm
# would empty the remedy section of the alert silently, and an alert that stops
# explaining itself is indistinguishable from one that never explained itself.
for c in 1 2 3 4 5 6 7 8 9 42; do
    [ -n "$(remedy_for_exit "$c")" ] || fail "remedy_for_exit $c is empty"
done
pass "remedy_for_exit: every exit code, and an unclassified one, yields a non-empty remedy"

# ---------------------------------------------------------------------------
# dispatch_freeze_note — the cost of waiting, as a number
# ---------------------------------------------------------------------------
# `draining=true` refuses ALL new polecat dispatch, so a drain is an interval in
# which no new work starts for anyone. Unreported, that made "give the drain
# longer" look free. It is not free on a night that stalls — that night pays the
# whole budget and gets no activation for it — and the only way that trade-off
# stops being an argument is if every stalled night leaves a measurement behind.
MAX_DRAIN=7200
printf '%s' "$(dispatch_freeze_note 7 5400)" | grep -q '5400' \
    && pass "dispatch_freeze_note: an exit 7 reports how long dispatch was frozen" || fail "freeze note omits the elapsed time"
printf '%s' "$(dispatch_freeze_note 7 5400)" | grep -q '7200' \
    && pass "dispatch_freeze_note: names the cap that bounds the cost" || fail "freeze note omits MAX_DRAIN"

# Every other outcome reached the bounce, so the elapsed time includes the build
# and the restart. Quoting that as a freeze would overstate it — and an inflated
# cost argues for a shorter budget on exactly the evidence that does not support
# one.
[ -z "$(dispatch_freeze_note 0 5400)" ] \
    && pass "dispatch_freeze_note: a SUCCESSFUL deploy reports no freeze (elapsed there is mostly build and bounce)" || fail "freeze note fired on rc=0"
[ -z "$(dispatch_freeze_note 4 5400)" ] \
    && pass "dispatch_freeze_note: a build failure reports no freeze — the drain had already finished" || fail "freeze note fired on rc=4"
[ -z "$(dispatch_freeze_note 9 5400)" ] \
    && pass "dispatch_freeze_note: a control-suite RED reports no freeze" || fail "freeze note fired on rc=9"

# ---------------------------------------------------------------------------
# The skip gates, end to end: exit 0, stamp untouched, NOTHING invoked
# ---------------------------------------------------------------------------
# The unit assertions above prove the predicates. These prove the wiring, and
# specifically that a fire which skips leaves no trace: an EXIT trap that
# recorded an attempt for a skipping fire would settle a night nobody attempted
# — and on the 03:00 fire's own lock, that would cancel the real attempt still
# running.
STAMP_F="$WORK/attempt.stamp"

printf '2026-07-31 1 4\n' > "$STAMP_F"
OUT="$(POGO_DEPLOY_NOW=04 POGO_DEPLOY_DATE=2026-07-31 POGO_DEPLOY_STAMP="$STAMP_F" \
       POGO_DEPLOY_LOCK_DIR="$WORK/lock-settled.d" POGO_DEPLOY_SRC="$WORK/nonexistent" \
       HOME="$WORK" bash "$RUNNER" 2>&1)"; RC=$?
[ "$RC" -eq 0 ] && pass "settled-night fire exits 0 (a skipped retry is not a failure)" || fail "settled-night exit was $RC"
printf '%s' "$OUT" | grep -q "already settled" \
    && pass "settled-night fire says WHY it skipped" || fail "settled-night reason not logged: $OUT"
printf '%s' "$OUT" | grep -qi "git fetch\|GH_TOKEN\|drift" \
    && fail "settled-night fire reached the deploy path: $OUT" \
    || pass "settled-night fire touches nothing — no token read, no fetch, no drift check"
[ "$(cat "$STAMP_F")" = "2026-07-31 1 4" ] \
    && pass "settled-night fire leaves the record untouched (it attempted nothing)" || fail "stamp mutated by a skipping fire: $(cat "$STAMP_F")"

# The budget floor, end to end: a retry IS allowed through gate 3 and is then
# stopped by gate 4 for want of window. Both halves matter — this is the only
# assertion that shows a rc=7 record actually reopens the night.
printf '2026-07-31 1 7\n' > "$STAMP_F"
OUT="$(POGO_DEPLOY_NOW=05 POGO_DEPLOY_DATE=2026-07-31 POGO_DEPLOY_STAMP="$STAMP_F" \
       POGO_DEPLOY_MIN_DRAIN=99999 \
       POGO_DEPLOY_LOCK_DIR="$WORK/lock-floor.d" POGO_DEPLOY_SRC="$WORK/nonexistent" \
       HOME="$WORK" bash "$RUNNER" 2>&1)"; RC=$?
[ "$RC" -eq 0 ] && pass "under-floor fire exits 0" || fail "under-floor exit was $RC"
printf '%s' "$OUT" | grep -q "attempt: RETRY" \
    && pass "a recorded exit 7 REOPENS the night — the retry gate lets it past" || fail "rc=7 record did not produce a retry: $OUT"
printf '%s' "$OUT" | grep -q "budget: under" \
    && pass "the budget floor stops a fire with too little window left, and says so" || fail "budget floor not logged: $OUT"
printf '%s' "$OUT" | grep -qi "git fetch\|GH_TOKEN\|drift" \
    && fail "under-floor fire reached the deploy path: $OUT" \
    || pass "under-floor fire touches nothing"
[ "$(cat "$STAMP_F")" = "2026-07-31 1 7" ] \
    && pass "under-floor fire leaves the record untouched" || fail "stamp mutated by a skipping fire: $(cat "$STAMP_F")"

# ---------------------------------------------------------------------------
# The nightly passes a derived --drain-timeout
# ---------------------------------------------------------------------------
# Same shape as the --force check below, and for the same reason: the failure is
# a one-line edit that drops the flag, after which the run silently reverts to
# pogo-self-deploy's 1800s default and the 07-31 failure returns with no sign
# that anything changed.
grep -E '^[^#]*"\$DEPLOY" redeploy' "$RUNNER" | grep -q -- '--drain-timeout' \
    && pass "the nightly passes its window-derived --drain-timeout (not the 1800s default)" \
    || fail "the redeploy invocation does not pass --drain-timeout"

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
