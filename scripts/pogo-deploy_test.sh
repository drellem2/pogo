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
# sync_src sets a CLASS, and the class is the step that failed (mg-0d70)
# ---------------------------------------------------------------------------
# On 2026-08-05 the nightly aborted one second in on
#
#     ssh: connect to host github.com port 22: Undefined error: 0
#
# and mailed Daniel "inspect 'git -C ~/.pogo/deploy-src status' — dirty or
# diverged aborts by design." The checkout was clean and on main. The runner had
# the fact it needed — sync_src knows perfectly well which of its five steps
# failed — and threw it away in favour of one paragraph printed under every
# outcome. These assertions pin the fact being kept.
SRC="$WORK/src"; DEPLOY_REF=main
POGO_DEPLOY_REMOTE="$UPSTREAM"; DEPLOY_REMOTE="$UPSTREAM"
# The divergence fixture above left a local commit in place on purpose; drop it
# so these assertions start from the ordinary night.
"$GIT" -C "$SRC" reset --hard --quiet HEAD~1
sync_src >/dev/null 2>&1
[ -z "$SYNC_CLASS" ] && pass "sync_src: a SUCCESSFUL sync leaves no failure class behind" || fail "sync_src set SYNC_CLASS=$SYNC_CLASS on success"

echo local-edit > "$SRC/f"
sync_src >/dev/null 2>&1
[ "$SYNC_CLASS" = "dirty" ] \
    && pass "sync_src: a dirty tree classifies as 'dirty' — the ONE case where the 08-05 remedy was the right one" || fail "dirty classified as '$SYNC_CLASS'"
printf '%s' "$SYNC_DETAIL" | grep -q 'f$' \
    && pass "sync_src: the dirty class carries the porcelain listing verbatim" || fail "dirty detail empty: $SYNC_DETAIL"
"$GIT" -C "$SRC" checkout --quiet -- f

echo local-commit > "$SRC/g"; "$GIT" -C "$SRC" add g
"$GIT" -C "$SRC" -c user.email=t@t -c user.name=t commit --quiet -m local
echo four > "$UPSTREAM/f"; "$GIT" -C "$UPSTREAM" commit --quiet -am four
sync_src >/dev/null 2>&1
[ "$SYNC_CLASS" = "diverged" ] \
    && pass "sync_src: a diverged tree classifies as 'diverged', NOT as the same thing as dirty" || fail "diverged classified as '$SYNC_CLASS'"
"$GIT" -C "$SRC" reset --hard --quiet HEAD~1

# main() resolves the probe before it syncs, so the assertions below run against
# the production path rather than a runner that never looked for one.
resolve_nc >/dev/null 2>&1 || true

# THE REGRESSION, reproduced. A fetch that cannot reach its remote must NOT come
# back as dirty-or-diverged. 127.0.0.1:1 is closed on every box, so the probe
# gets an immediate RST and this stays hermetic and fast — no DNS, no internet.
UNREACHABLE="$WORK/unreachable"
"$GIT" clone --quiet "$UPSTREAM" "$UNREACHABLE" 2>/dev/null
"$GIT" -C "$UNREACHABLE" remote set-url origin "ssh://git@127.0.0.1:1/nope.git"
SRC="$UNREACHABLE"
sync_src >/dev/null 2>&1; RC=$?
[ "$RC" -ne 0 ] && pass "sync_src: an unreachable remote still fails (the premise of the assertions below)" || fail "sync_src succeeded against an unreachable remote"
[ "$SYNC_CLASS" = "network" ] \
    && pass "sync_src: the 08-05 failure classifies as 'network' — the defect was reporting it as dirty-or-diverged" \
    || fail "an unreachable-remote fetch classified as '$SYNC_CLASS', not network"
[ "$SYNC_CLASS" != "dirty" ] && [ "$SYNC_CLASS" != "diverged" ] \
    && pass "sync_src: a fetch failure is NEVER dirty-or-diverged — the tree was never inspected" \
    || fail "a fetch failure blamed the tree"
[ -n "$SYNC_DETAIL" ] \
    && pass "sync_src: the failing step's stderr is kept verbatim for the alert to print" || fail "SYNC_DETAIL empty on a transport failure"

# ...and on a box where no probe can be proved, THE SAME FETCH FAILURE must stop
# short of naming the network. This is the safety property the three-valued probe
# exists for: the runner loses precision, never honesty.
SAVED_NC_E2E="$NC"; NC=""
sync_src >/dev/null 2>&1
[ "$SYNC_CLASS" = "unclassified" ] \
    && pass "sync_src: with no provable probe the SAME failure is 'unclassified', not 'network' — precision is lost, honesty is not" \
    || fail "with no probe, the fetch failure was still classified '$SYNC_CLASS'"
NC="$SAVED_NC_E2E"

SRC="$WORK/src"

# ---------------------------------------------------------------------------
# remote_endpoint — classification by STRUCTURE, never by git's English
# ---------------------------------------------------------------------------
# git prints "Please make sure you have the correct access rights and the
# repository exists" after ANY ssh failure, connectivity included, so the
# message cannot separate (a) from (b) — and a matcher that tried would stop
# working the day git rewords it (the trap t55ca refused on gh#113). The host
# and port come out of the URL's SCHEME instead, which is structure.
[ "$(remote_endpoint 'git@github.com:daniel/pogo.git')" = "github.com 22" ] \
    && pass "remote_endpoint: the scp-like form used by the real remote -> github.com:22" || fail "scp-like: $(remote_endpoint 'git@github.com:daniel/pogo.git')"
[ "$(remote_endpoint 'ssh://git@github.com:2222/daniel/pogo.git')" = "github.com 2222" ] \
    && pass "remote_endpoint: an ssh:// URL with an explicit port" || fail "ssh:// port: $(remote_endpoint 'ssh://git@github.com:2222/daniel/pogo.git')"
[ "$(remote_endpoint 'ssh://git@github.com/daniel/pogo.git')" = "github.com 22" ] \
    && pass "remote_endpoint: ssh:// defaults to 22" || fail "ssh:// default: $(remote_endpoint 'ssh://git@github.com/daniel/pogo.git')"
[ "$(remote_endpoint 'https://github.com/daniel/pogo.git')" = "github.com 443" ] \
    && pass "remote_endpoint: https:// defaults to 443 (the runner must probe the port git would use)" || fail "https: $(remote_endpoint 'https://github.com/daniel/pogo.git')"
[ "$(remote_endpoint 'git://github.com/daniel/pogo.git')" = "github.com 9418" ] \
    && pass "remote_endpoint: git:// defaults to 9418" || fail "git://: $(remote_endpoint 'git://github.com/daniel/pogo.git')"
[ "$(remote_endpoint 'ssh://git@[fe80::1]:2222/x.git')" = "fe80::1 2222" ] \
    && pass "remote_endpoint: a bracketed IPv6 host with a port" || fail "ipv6: $(remote_endpoint 'ssh://git@[fe80::1]:2222/x.git')"

# A local path has no endpoint, and saying so is the point: an unprobeable
# remote must produce "could not classify", not a guess.
remote_endpoint "/Users/daniel/dev/pogo" >/dev/null \
    && fail "remote_endpoint invented an endpoint for a local path" || pass "remote_endpoint: a local path has NO endpoint (so the failure stays unclassified rather than guessed)"
remote_endpoint "" >/dev/null \
    && fail "remote_endpoint accepted an empty URL" || pass "remote_endpoint: an empty URL has no endpoint"
remote_endpoint "file:///srv/pogo.git" >/dev/null \
    && fail "remote_endpoint invented an endpoint for file://" || pass "remote_endpoint: file:// has no endpoint"

# ---------------------------------------------------------------------------
# probe_tcp — the measurement the classification rests on
# ---------------------------------------------------------------------------
# BOTH controls are load-bearing and the POSITIVE one is the one that matters.
# A probe stuck at "unreachable" would classify every auth failure as a network
# blip, retry it three times, and mail a network remedy for a rejected key —
# the 08-05 defect with the blame moved rather than removed. Only a probe that
# can answer YES can rule the network out.
LISTENER_PORTFILE="$WORK/listener.port"
python3 - "$LISTENER_PORTFILE" >/dev/null 2>&1 <<'PY' &
import socket, sys, time
s = socket.socket(); s.bind(("127.0.0.1", 0)); s.listen(8)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
time.sleep(120)
PY
LISTENER_PID=$!
i=0
while [ ! -s "$LISTENER_PORTFILE" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$(( i + 1 )); done
LISTENER_PORT="$(cat "$LISTENER_PORTFILE" 2>/dev/null)"

# A second listener, for the by-NAME control below.
LISTENER_PORTFILE2="$WORK/listener2.port"
python3 - "$LISTENER_PORTFILE2" >/dev/null 2>&1 <<'PY' &
import socket, sys, time
s = socket.socket(); s.bind(("127.0.0.1", 0)); s.listen(8)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
time.sleep(120)
PY
LISTENER_PID2=$!
i=0
while [ ! -s "$LISTENER_PORTFILE2" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$(( i + 1 )); done
LISTENER_PORT2="$(cat "$LISTENER_PORTFILE2" 2>/dev/null)"

resolve_nc >/dev/null 2>&1 || true

if [ -n "$LISTENER_PORT" ]; then
    probe_tcp 127.0.0.1 "$LISTENER_PORT" 5 \
        && pass "probe_tcp POSITIVE CONTROL: a port that IS listening reads as reachable (without this, every failure is 'the network')" \
        || fail "probe_tcp could not reach a local listener on 127.0.0.1:$LISTENER_PORT"
else
    fail "probe_tcp positive control could not start a listener (python3 missing?) — the probe is unverified in the direction that matters"
fi
kill "$LISTENER_PID" 2>/dev/null; wait "$LISTENER_PID" 2>/dev/null

probe_tcp 127.0.0.1 1 5
[ "$?" -ne 0 ] && pass "probe_tcp NEGATIVE CONTROL: a closed port does not read as reachable" || fail "probe_tcp claimed a closed port was reachable"

# THE CONTROL THE FIRST VERSION OF THIS PROBE DID NOT HAVE, and the reason it
# shipped broken. Every assertion above uses 127.0.0.1 — a literal IP — so none
# of them can fail for the reason the probe actually failed on this box:
#
#     /bin/bash 3.2   exec 3<>/dev/tcp/github.com/22    HANGS
#     /bin/bash 3.2   exec 3<>/dev/tcp/20.26.156.215/22 connects
#
# macOS's bash hangs resolving a HOSTNAME in a /dev/tcp redirect. The deploy
# remote is a hostname, so the probe would have timed out every night and
# reported "unreachable" — classifying a rejected key, a 5xx and a real outage
# alike as `network`. A check invariant under the failure it guards fires for
# nothing, so this one uses a name.
#
# `localhost` resolves from /etc/hosts on every box, needs no DNS server, and is
# a NAME rather than an address — which is exactly the axis under test.
if [ -n "$LISTENER_PORT2" ]; then
    probe_tcp localhost "$LISTENER_PORT2" 5 \
        && pass "probe_tcp reaches a listener addressed by NAME, not just by IP — the case the first version hung on" \
        || fail "probe_tcp could not reach localhost:$LISTENER_PORT2 by name — it is IP-only, which is how it shipped broken"
else
    fail "the by-name control could not start a listener — the probe is unverified on the axis that broke it"
fi
kill "$LISTENER_PID2" 2>/dev/null; wait "$LISTENER_PID2" 2>/dev/null

# THE THREE-VALUED CONTRACT. "No answer" must be its own outcome: a probe that
# merely failed to complete must never be reported as "the network is down",
# because that is this ticket's defect committed inside its own fix.
#
# 192.0.2.0/24 is TEST-NET-1 and guaranteed non-routable, so a SYN to it is
# dropped rather than refused — the probe gets no answer at all.
SAVED_NC="$NC"
NC=""   # no proven primitive: the probe may confirm UP, never assert DOWN
T0=$(date +%s)
probe_tcp 192.0.2.1 22 2; PRC=$?
T1=$(date +%s)
[ "$PRC" -eq 2 ] \
    && pass "probe_tcp: with no proven primitive, a blackholed host returns 'could not probe' (2) — NOT 'unreachable'" \
    || fail "probe_tcp returned $PRC for a blackholed host with no proven primitive; 1 here is the misdiagnosis this ticket is about"
[ $(( T1 - T0 )) -le 8 ] \
    && pass "probe_tcp gives up on a BLACKHOLED host at its timeout ($(( T1 - T0 ))s), not at the kernel's ~75s" \
    || fail "probe_tcp took $(( T1 - T0 ))s against a blackholed host — the timeout is not working"
probe_tcp 127.0.0.1 1 3; PRC=$?
[ "$PRC" -eq 2 ] \
    && pass "probe_tcp: with no proven primitive, even a CLOSED port is 'could not probe' — /dev/tcp's failure is not evidence about the network" \
    || fail "probe_tcp asserted a definite answer ($PRC) from an unproven primitive"
NC="$SAVED_NC"

# The PRECISION half, which the controls above deliberately cannot cover. They
# assert that losing the probe costs no honesty; this asserts the probe is
# actually PREFERRED when present, because `localhost` resolves out of
# /etc/hosts and so a /dev/tcp fallback would still pass every by-name
# assertion above — measured: bash 3.2 refuses localhost:22 in 0s and hangs on
# github.com:22. Only a real DNS name separates them, and the suite must not
# need the internet, so the preference is checked directly instead.
NC_WITNESS="$WORK/nc.used"; rm -f "$NC_WITNESS"
cat > "$FAKEBIN/witnessnc" <<EOF
#!/bin/sh
echo used > "$NC_WITNESS"
exit 0
EOF
chmod +x "$FAKEBIN/witnessnc"
SAVED_NC="$NC"; NC="$FAKEBIN/witnessnc"
probe_tcp github.com 22 3 >/dev/null 2>&1
[ -f "$NC_WITNESS" ] \
    && pass "probe_tcp USES the resolved nc rather than falling back to /dev/tcp — the fallback hangs on any DNS name on this box" \
    || fail "probe_tcp ignored the resolved nc and went to /dev/tcp"
NC="$SAVED_NC"

# ---------------------------------------------------------------------------
# classify_transport — reachable rules the network OUT
# ---------------------------------------------------------------------------
# Hermetic: probe_tcp is substituted, so these verdicts do not depend on this
# host's connectivity. That matters for the same reason the resolve_git fakes do
# — a suite that only ever ran against a working network could not tell whether
# the classification existed at all.
REAL_PROBE="$(declare -f probe_tcp)"

probe_tcp() { return 0; }        # everything answers
SYNC_CLASS=""
classify_transport "git@github.com:daniel/pogo.git" >/dev/null 2>&1
[ "$SYNC_CLASS" = "remote" ] \
    && pass "classify_transport: a REACHABLE endpoint means the failure is at the far end, not the network" \
    || fail "reachable endpoint classified as '$SYNC_CLASS'"

probe_tcp() { return 1; }        # a DEFINITE refusal
SYNC_CLASS=""
classify_transport "git@github.com:daniel/pogo.git" >/dev/null 2>&1
[ "$SYNC_CLASS" = "network" ] \
    && pass "classify_transport: a DEFINITELY unreachable endpoint is the network — the 08-05 case" \
    || fail "unreachable endpoint classified as '$SYNC_CLASS'"

# THE DISTINCTION THE FIRST VERSION COLLAPSED. "The probe got no answer" and
# "the host is down" are different facts, and treating the first as the second
# is how a broken probe would have blamed the network for every rejected key.
probe_tcp() { return 2; }        # no answer at all
SYNC_CLASS=""
classify_transport "git@github.com:daniel/pogo.git" >/dev/null 2>&1
[ "$SYNC_CLASS" = "unclassified" ] \
    && pass "classify_transport: a probe that returns NO ANSWER yields 'unclassified' — no answer is not a no" \
    || fail "a probe that could not complete was reported as '$SYNC_CLASS' — that asserts a cause it never established"

# The third answer, and the one the ticket asks for by name: when the runner
# cannot classify it must SAY so and print the error, not fall back to the most
# common cause. That fallback is exactly what produced the misleading alert.
probe_tcp() { return 1; }
SYNC_CLASS=""
classify_transport "/Users/daniel/dev/pogo" >/dev/null 2>&1
[ "$SYNC_CLASS" = "unclassified" ] \
    && pass "classify_transport: an unprobeable remote yields 'unclassified' — it does not fall back to a guess" \
    || fail "unprobeable remote classified as '$SYNC_CLASS'"

eval "$REAL_PROBE"

# ---------------------------------------------------------------------------
# sync_with_retry — the network class retries, everything else settles
# ---------------------------------------------------------------------------
# Four hours of window went unused on 08-05 for a fault that lasted one second.
# The scoping is the same argument the drain retry makes about exit 7: a dirty
# tree, a diverged branch and a rejected key are all still true thirty seconds
# later, so retrying them spends the window and mails a duplicate alert. A
# dropped TCP connection is the one that may not be.
REAL_SYNC_SRC="$(declare -f sync_src)"
STUB_CLASS=network
STUB_SUCCEED_ON=999
SYNC_CALLS=0
sync_src() {
    SYNC_CALLS=$(( SYNC_CALLS + 1 ))
    SYNC_CLASS="$STUB_CLASS"; SYNC_DETAIL="stub failure"
    [ "$SYNC_CALLS" -ge "$STUB_SUCCEED_ON" ] && { SYNC_CLASS=""; return 0; }
    return 1
}
SYNC_BACKOFF="0"; SYNC_RETRY_BUDGET=300; SYNC_ATTEMPTS=4
# The BLIP tier is what this block asserts, so the mg-5515 vigil is off for it.
# Not incidental setup: with the vigil on, "patience is bounded" below is no
# longer the blip tier's property — the bound moves to the window, which the
# vigil block further down asserts separately. Two tiers, two sets of assertions.
SYNC_VIGIL=0; SYNC_VIGIL_INTERVAL=300
# Retries are charged against the deploy window (the ruling's condition 2), so
# these run as if it were 03:00. Without this they would be refused for lack of
# window — which is itself asserted, below.
WINDOW_END=6; RESERVE=1200; MAX_DRAIN=7200; MIN_DRAIN=600
export POGO_DEPLOY_NOW=3

# THE FIX, as an assertion. One transient failure followed by a success must not
# cost the night.
STUB_CLASS=network; STUB_SUCCEED_ON=2; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1 \
    && [ "$SYNC_CALLS" -eq 2 ] \
    && pass "sync_with_retry: a network blip on attempt 1 is RETRIED and the deploy proceeds (the 08-05 night, saved)" \
    || fail "sync_with_retry did not recover from a single transient failure (calls=$SYNC_CALLS)"

STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0
if sync_with_retry >/dev/null 2>&1; then
    fail "sync_with_retry succeeded against a permanently failing sync"
elif [ "$SYNC_CALLS" -eq 4 ]; then
    pass "sync_with_retry: with the vigil off, a sustained outage stops at POGO_DEPLOY_SYNC_ATTEMPTS (4) — the blip tier's patience is bounded by its attempt count"
else
    fail "sync_with_retry made $SYNC_CALLS attempts, expected 4"
fi

# THE DISCRIMINATOR, as pm-pogo ruled it: would re-running plausibly give a
# different answer for a reason UNRELATED TO THE CODE?
#
# The retryable side is the transport classes — the sync never reached the tree,
# so nothing about the repo state was established and re-asking is how you find
# out. `remote` is on this side despite naming the far end: it conflates a
# rejected key with the 5xx and rate-limit cases the ruling lists by name, and a
# TCP handshake cannot separate them without reading prose. The asymmetry then
# decides it — retrying a dead key costs one bounded, logged interval; not
# retrying a 5xx costs the night.
for c in network remote unclassified; do
    STUB_CLASS="$c"; STUB_SUCCEED_ON=2; SYNC_CALLS=0
    sync_with_retry >/dev/null 2>&1
    [ "$SYNC_CALLS" -eq 2 ] || fail "sync_with_retry did NOT retry '$c' — it established nothing about the tree, so the repo state is simply unknown"
done
pass "the discriminator RETRIES network, remote and unclassified — the classes where the sync never reached the tree"

# ...and each class that ESTABLISHED a fact is tried exactly ONCE. Getting this
# wrong in the permissive direction turns a dirty checkout into four identical
# failures and a late alert; getting it wrong in the strict direction reinstates
# the 08-05 defect.
for c in dirty diverged checkout config; do
    STUB_CLASS="$c"; STUB_SUCCEED_ON=999; SYNC_CALLS=0
    sync_with_retry >/dev/null 2>&1
    [ "$SYNC_CALLS" -eq 1 ] || fail "sync_with_retry retried a '$c' failure ($SYNC_CALLS attempts) — it established a fact and will re-establish it"
done
pass "the discriminator does NOT retry dirty, diverged, checkout or config — each established a fact that is as true in 30s"

sync_class_retryable network && sync_class_retryable remote && sync_class_retryable unclassified \
    && pass "sync_class_retryable: the transport classes are retryable" || fail "sync_class_retryable rejected a transport class"
sync_class_retryable dirty || sync_class_retryable diverged || sync_class_retryable checkout || sync_class_retryable config \
    && fail "sync_class_retryable accepted a class that established a fact" \
    || pass "sync_class_retryable: dirty, diverged, checkout and config are NOT retryable"
sync_class_retryable "" \
    && fail "sync_class_retryable accepted an empty class" || pass "sync_class_retryable: an unset class is not retryable (it fails toward the conservative side)"

# CONDITION 2, enforced rather than asserted: retries consume the deploy budget,
# they do not extend it. At 05:55 there is no window left to spend, so the retry
# must be refused even though attempts and retry-budget both remain — otherwise
# the backoff spends the fleet's window to arrive at the same skip, later.
POGO_DEPLOY_NOW=5; MIN_DRAIN=99999
STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1
[ "$SYNC_CALLS" -eq 1 ] \
    && pass "sync_with_retry: a retry is REFUSED when the window could not still afford a drain (retries consume the budget, they do not extend it)" \
    || fail "sync_with_retry retried past the usable window ($SYNC_CALLS attempts)"
MIN_DRAIN=600; POGO_DEPLOY_NOW=3

# CONDITION 1: "failed once" and "failed after four attempts" must be
# distinguishable, and the count is what the alert prints.
STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1
[ "$SYNC_TRIES" -eq 4 ] && pass "SYNC_TRIES reports four attempts, so the alert can say how hard it tried" || fail "SYNC_TRIES=$SYNC_TRIES after four attempts"
STUB_CLASS=dirty; STUB_SUCCEED_ON=999; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1
[ "$SYNC_TRIES" -eq 1 ] && pass "SYNC_TRIES reports ONE attempt for a non-retryable class — the two cases are distinguishable in the alert" || fail "SYNC_TRIES=$SYNC_TRIES after one attempt"

# CONDITION 3: a retried success must NAME the attempt that won. A silent retry
# converts a flaky night into an invisible one, and the count of recovered
# nights is the evidence that the network is the dominant failure mode here.
STUB_CLASS=network; STUB_SUCCEED_ON=3; SYNC_CALLS=0
RECOVER_LOG="$WORK/recover.log"
sync_with_retry > "$RECOVER_LOG" 2>&1
grep -q 'RECOVERED' "$RECOVER_LOG" && grep -q 'attempt 3' "$RECOVER_LOG" \
    && pass "a recovered sync NAMES the attempt that won (a silent retry makes a flaky night invisible)" \
    || fail "recovery not named in the log: $(cat "$RECOVER_LOG")"
STUB_CLASS=network; STUB_SUCCEED_ON=1; SYNC_CALLS=0
sync_with_retry > "$RECOVER_LOG" 2>&1
grep -q 'RECOVERED' "$RECOVER_LOG" \
    && fail "a first-attempt success claimed a recovery — that would inflate the flakiness evidence" \
    || pass "a first-attempt success reports NO recovery (the evidence counts real retries only)"

# The budget ceiling, independent of the attempt count: backoff is time taken
# from the drain, and it has to be bounded by something an operator can read off
# one variable rather than by multiplying the delay list out in their head.
#
# Scaled to 1s rather than the production 100s. The property is the ARITHMETIC —
# stop when the next delay would take the total past the ceiling — and it holds
# at any scale, whereas a test that sleeps the real numbers spends 100s of every
# suite run proving something a 1s version proves identically.
SYNC_BACKOFF="1"; SYNC_RETRY_BUDGET=1; SYNC_ATTEMPTS=9
STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1
[ "$SYNC_CALLS" -eq 2 ] \
    && pass "sync_with_retry: POGO_DEPLOY_SYNC_RETRY_BUDGET caps the total blip-tier backoff even when attempts remain" \
    || fail "retry budget did not cap the backoff (calls=$SYNC_CALLS, expected 2)"
SYNC_BACKOFF="15 45 120"; SYNC_RETRY_BUDGET=300; SYNC_ATTEMPTS=4

# The production numbers must fit inside the production budget, or the ceiling
# silently truncates the attempt count and POGO_DEPLOY_SYNC_ATTEMPTS stops
# meaning what it says.
[ $(( 15 + 45 + 120 )) -le 300 ] \
    && pass "the shipped backoff list (15+45+120=180s) fits inside the shipped 300s retry budget" \
    || fail "the shipped backoff exceeds the shipped retry budget"

# sync_backoff itself: the last entry repeats, so a shortened list degrades into
# a constant rather than into an unthrottled hammer at zero seconds.
[ "$(sync_backoff 1 "15 45 120")" = "15" ] && pass "sync_backoff: first retry waits 15s" || fail "sync_backoff 1"
[ "$(sync_backoff 3 "15 45 120")" = "120" ] && pass "sync_backoff: the third waits 120s" || fail "sync_backoff 3"
[ "$(sync_backoff 9 "15 45 120")" = "120" ] \
    && pass "sync_backoff: past the end of the list the last delay repeats (never 0)" || fail "sync_backoff overrun"

# ---------------------------------------------------------------------------
# The vigil tier — patience sized for an OUTAGE, not a blip (mg-5515)
# ---------------------------------------------------------------------------
# The blip tier asserted above spends three minutes; the three fires at 03/04/05
# span two hours between them. The one outage measured on this box ran 2h50m
# (2026-08-07, 13:24:30Z -> 16:14:52Z), so an outage of that length beginning at
# or before the first fire fails every one of them on arrival. No agent can cover
# it — an outage of that shape takes every agent on the box out at once — and
# re-spacing three instants cannot cover 170 minutes either.
#
# So the vigil keeps probing at a flat interval for as long as the WINDOW could
# still afford a drain. The window is the only bound; the vigil adds patience and
# never window, which is mg-0d70's condition 2 doing double duty.
SYNC_VIGIL=1; SYNC_VIGIL_INTERVAL=0; SYNC_BACKOFF="0"; SYNC_RETRY_BUDGET=300; SYNC_ATTEMPTS=4
POGO_DEPLOY_NOW=3; MIN_DRAIN=600

# The handover, as arithmetic. Exhausting four fast attempts is exactly what
# tells you this is an outage rather than a blip — which is the moment the vigil
# is for, and the moment that used to end the run.
SYNC_BLIP_SPENT=0
[ "$(sync_next_delay 1)" = "blip 0" ] \
    && pass "sync_next_delay: inside the attempt count the BLIP tier owns the wait" || fail "sync_next_delay 1: $(sync_next_delay 1)"
[ "$(sync_next_delay 4)" = "vigil 0" ] \
    && pass "sync_next_delay: at the attempt cap the blip tier HANDS OVER to the vigil (it does not end the run)" || fail "sync_next_delay 4: $(sync_next_delay 4)"
SYNC_BLIP_SPENT=99999
[ "$(sync_next_delay 1)" = "vigil 0" ] \
    && pass "sync_next_delay: a spent blip BUDGET hands over too, not just a spent attempt count" || fail "sync_next_delay with a spent budget: $(sync_next_delay 1)"
SYNC_BLIP_SPENT=0
SYNC_VIGIL=0
sync_next_delay 4 >/dev/null 2>&1 \
    && fail "sync_next_delay offered a vigil while POGO_DEPLOY_SYNC_VIGIL=0" \
    || pass "sync_next_delay: POGO_DEPLOY_SYNC_VIGIL=0 restores the pre-mg-5515 bound exactly (one escape hatch, one variable)"
SYNC_VIGIL=1

# THE FIX, as an assertion. A sync that recovers on attempt 8 is one NO number of
# fires reaches: the blip tier gave up at 4, and the 04:00 fire would have found
# the same outage still running. The vigil is what deploys the night.
STUB_CLASS=network; STUB_SUCCEED_ON=8; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1 \
    && [ "$SYNC_CALLS" -eq 8 ] \
    && pass "the vigil probes PAST the blip tier's attempt cap and recovers a night the fires could not (attempt 8 of a 4-attempt blip tier)" \
    || fail "the vigil did not carry the sync past attempt 4 (calls=$SYNC_CALLS)"
[ "$SYNC_TRIES" -eq 8 ] \
    && pass "SYNC_TRIES counts vigil probes too, so the alert can say how long it actually waited" || fail "SYNC_TRIES=$SYNC_TRIES after a vigil recovery"

# A TICKING stub for the sustained-outage cases below. The vigil's bound is the
# window, so it can only be shown to terminate against a clock that MOVES —
# POGO_DEPLOY_NOW is a fixed hour, and a permanently-failing stub under a frozen
# clock is an infinite loop rather than a failing assertion. Three probes per
# fake hour, which is enough to walk 03:00 out to the end of the window.
BLIP_STUB="$(declare -f sync_src)"
sync_src() {
    SYNC_CALLS=$(( SYNC_CALLS + 1 ))
    [ $(( SYNC_CALLS % 3 )) -eq 0 ] && POGO_DEPLOY_NOW=$(( POGO_DEPLOY_NOW + 1 ))
    SYNC_CLASS="$STUB_CLASS"; SYNC_DETAIL="stub failure"
    [ "$SYNC_CALLS" -ge "$STUB_SUCCEED_ON" ] && { SYNC_CLASS=""; return 0; }
    return 1
}

# The vigil's bound is the WINDOW and nothing else: it keeps probing well past
# the blip tier's cap, and it TERMINATES — because the clock walked drain_budget
# to zero, not because a counter ran out.
STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0; POGO_DEPLOY_NOW=3
if sync_with_retry >/dev/null 2>&1; then
    fail "sync_with_retry succeeded against a permanently unreachable transport"
elif [ "$SYNC_CALLS" -gt 4 ]; then
    pass "a sustained outage is waited out to the edge of the window rather than abandoned after $SYNC_ATTEMPTS attempts ($SYNC_CALLS probes) — and it terminates"
else
    fail "the vigil did not run: only $SYNC_CALLS attempts against a sustained outage"
fi

# ...and it stops the moment the window can no longer afford a drain. This is the
# assertion that keeps "adds patience, never window" true: without it the vigil
# would spend the fleet's window to arrive at the same skip, later.
POGO_DEPLOY_NOW=5; MIN_DRAIN=99999
STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0
sync_with_retry >/dev/null 2>&1
[ "$SYNC_CALLS" -eq 1 ] \
    && pass "the vigil is REFUSED when the window could not still afford a drain — it adds patience, never window" \
    || fail "the vigil probed past the usable window ($SYNC_CALLS probes)"
MIN_DRAIN=600

# The duration is REPORTED, not just spent. mg-5515's honest bound was n=1: one
# outage, measured once, with no distribution behind it. A vigil that waits
# silently would leave the next ticket with n=1 as well.
STUB_CLASS=network; STUB_SUCCEED_ON=999; SYNC_CALLS=0; POGO_DEPLOY_NOW=3
SYNC_VIGIL_INTERVAL=1; SYNC_BACKOFF="0"
VIGIL_LOG="$WORK/vigil.log"
sync_with_retry > "$VIGIL_LOG" 2>&1
grep -q 'VIGIL probe' "$VIGIL_LOG" \
    && pass "each vigil probe is logged as a vigil probe, so the log doubles as a duration measurement" \
    || fail "vigil probes not logged: $(cat "$VIGIL_LOG")"
[ "${SYNC_VIGIL_SPENT:-0}" -gt 0 ] \
    && pass "SYNC_VIGIL_SPENT is a probed LOWER BOUND on the outage — the distribution mg-5515 asked for starts here" \
    || fail "SYNC_VIGIL_SPENT stayed 0 across a vigil"
[ "$SYNC_RETRY_SPENT" -ge "$SYNC_VIGIL_SPENT" ] \
    && pass "SYNC_RETRY_SPENT remains the TOTAL wait (the drain's recomputed budget must not lose the vigil)" \
    || fail "SYNC_RETRY_SPENT ($SYNC_RETRY_SPENT) is less than SYNC_VIGIL_SPENT ($SYNC_VIGIL_SPENT)"
grep -q 'the vigil ends' "$VIGIL_LOG" \
    && pass "the vigil says WHY it ended — the outage outlasted the window, not the runner's patience" \
    || fail "the vigil's ending is not explained: $(cat "$VIGIL_LOG")"
eval "$BLIP_STUB"
POGO_DEPLOY_NOW=3

# touch_lock: the hazard this fix introduces, and its guard. acquire_lock
# reclaims a lock whose mtime is older than STALE_LOCK_MIN (180 min). Before the
# vigil no run could hold one that long; a vigil from a 02:00 wake-fire runs to
# ~05:30, which is 210 minutes. Unrefreshed, the 05:00 fire would reclaim a lock
# a LIVE run holds and start a competing deploy.
REAL_LOCK_DIR="$LOCK_DIR"
LOCK_DIR="$WORK/vigil.lock.d"; mkdir -p "$LOCK_DIR"
touch -t 202001010000 "$LOCK_DIR"
find "$LOCK_DIR" -maxdepth 0 -type d -mmin +180 | grep -q . \
    && pass "the stale-lock reclaim would fire on an unrefreshed vigil (the hazard is real, not hypothetical)" \
    || fail "could not stage a stale lock"
touch_lock
find "$LOCK_DIR" -maxdepth 0 -type d -mmin +180 | grep -q . \
    && fail "touch_lock did not refresh the lock mtime — a vigil past STALE_LOCK_MIN would be reclaimed under a live run" \
    || pass "touch_lock refreshes the lock, so STALE_LOCK_MIN means 'no run has made progress in 180min' rather than 'a run started 180min ago'"
rmdir "$LOCK_DIR"
touch_lock && pass "touch_lock is a no-op when there is no lock (it never fails a run)" || fail "touch_lock failed with no lock dir"
LOCK_DIR="$REAL_LOCK_DIR"

# The reach, as arithmetic against the shipped constants — and the RESIDUAL,
# pinned so it cannot quietly be read as solved. drain_budget hits zero when the
# remaining window is under RESERVE + MIN_DRAIN, so a vigil reaches 05:30 under
# the production window. A 03:00 fire therefore covers 2h30m, which is SHORTER
# than the 2h50m outage that prompted mg-5515: an outage of exactly that length
# starting at exactly 03:00 still costs the night, and no amount of patience
# fixes it (it ends at 05:50, and RESERVE alone is 20 minutes). Saving that
# specific night needs a wider window, which mg-5515 has no distribution to size.
[ "$(drain_budget 6 1200 7200 600 5 29 0)" -gt 0 ] \
    && pass "the vigil can still probe at 05:29 under the production window" || fail "vigil reach: 05:29"
[ "$(drain_budget 6 1200 7200 600 5 31 0)" -eq 0 ] \
    && pass "the vigil stops by 05:30 — its reach is 2h30m from the 03:00 fire and 3h30m from a 02:00 wake-fire" || fail "vigil reach: 05:31"
[ $(( (5 * 60 + 30) - (3 * 60) )) -lt $(( 2 * 60 + 50 )) ] \
    && pass "RESIDUAL, pinned: 2h30m of reach is less than the 2h50m outage measured on 2026-08-07 — the vigil changes the SHAPE of the loss (past 05:30, not merely covering three instants), it does not abolish it" \
    || fail "the residual arithmetic no longer holds — re-derive it before trusting the header"

SYNC_VIGIL=1; SYNC_VIGIL_INTERVAL=300; SYNC_BACKOFF="15 45 120"; SYNC_RETRY_BUDGET=300; SYNC_ATTEMPTS=4

eval "$REAL_SYNC_SRC"
unset POGO_DEPLOY_NOW   # do not leak a fake clock into the assertions below

# ---------------------------------------------------------------------------
# The alert text — each class gets the paragraph that is TRUE of it
# ---------------------------------------------------------------------------
# The same defect remedy_for_exit was fixed for on mg-8f7e, one layer down: one
# paragraph under every code. Here it sent Daniel to a `git status` that was
# clean, which is worse than no alert — it spends the reader's trust.
SNET="$(remedy_for_sync_class network)"; SDIRTY="$(remedy_for_sync_class dirty)"
SREM="$(remedy_for_sync_class remote)"; SDIV="$(remedy_for_sync_class diverged)"
SUNC="$(remedy_for_sync_class unclassified)"

printf '%s' "$SNET" | grep -q 'deploy-src status' \
    && fail "the NETWORK remedy still sends the reader to inspect the checkout — the 08-05 defect, verbatim" \
    || pass "the NETWORK remedy does NOT send the reader to a 'git status' that is clean"
printf '%s' "$SNET" | grep -qi 'not the place to look' \
    && pass "the NETWORK remedy says explicitly that the deploy tree is not the place to look" || fail "network remedy does not rule the tree out"
printf '%s' "$SDIRTY" | grep -q 'deploy-src status' \
    && pass "the DIRTY remedy keeps the checkout inspection, where it is the right advice" || fail "dirty remedy lost its inspection step"
printf '%s' "$SREM" | grep -qi 'ANSWERED a TCP connection' \
    && pass "the REMOTE remedy states the network was measured UP, so the reader does not chase it" || fail "remote remedy does not report the connectivity measurement"
# ...and it must not overclaim. The probe runs moments AFTER the failure, so a
# blip that had already ended reads exactly like an auth problem — the same
# species of defect as the one being fixed, one size smaller. The remedy has to
# carry that caveat rather than present an inference as a finding.
printf '%s' "$SREM" | grep -qi 'floor on connectivity' \
    && pass "the REMOTE remedy admits the probe is a FLOOR on connectivity, not a proof — it does not overclaim its own measurement" \
    || fail "remote remedy presents an inference as a finding"
printf '%s' "$SREM" | grep -qi 'correct access rights' \
    && pass "the REMOTE remedy warns that git's access-rights wording appears for network failures too" || fail "remote remedy does not warn about the ambiguous git wording"
printf '%s' "$SUNC" | grep -qi 'could not establish\|rather than naming the most common' \
    && pass "the UNCLASSIFIED remedy admits it does not know instead of naming the most likely cause" || fail "unclassified remedy guesses"

[ "$SNET" != "$SDIRTY" ] && [ "$SNET" != "$SREM" ] && [ "$SDIRTY" != "$SDIV" ] && [ "$SREM" != "$SUNC" ] \
    && pass "remedy_for_sync_class: the classes get DIFFERENT paragraphs — one paragraph for all of them is the defect" \
    || fail "remedy_for_sync_class returns the same text for different classes"

for c in network remote dirty diverged checkout config unclassified ''; do
    [ -n "$(remedy_for_sync_class "$c")" ] || fail "remedy_for_sync_class '$c' is empty"
    [ -n "$(describe_sync_class "$c")" ] || fail "describe_sync_class '$c' is empty"
done
pass "every sync class, and an empty one, yields a non-empty description and remedy"

case "$(describe_sync_class network)" in *NETWORK*) pass "describe_sync_class names the network" ;; *) fail "describe_sync_class network" ;; esac
case "$(describe_sync_class dirty)" in *DIRTY*) pass "describe_sync_class names the dirty checkout" ;; *) fail "describe_sync_class dirty" ;; esac

# ---------------------------------------------------------------------------
# The wiring — same shape as the --force and --drain-timeout checks
# ---------------------------------------------------------------------------
# Each of these is a one-line edit away from silently reverting to the 08-05
# behaviour, with the classifier still present and simply not consulted.
grep -qE '^[^#]*if ! sync_with_retry' "$RUNNER" \
    && pass "main() calls sync_with_retry, so a network abort is actually retried" \
    || fail "main() does not call sync_with_retry — the retry is defined but never reached"
# Non-comment lines only, for the reason the --force check states below: the
# header quotes the 08-05 alert verbatim to explain why it was wrong, and a
# check that cannot tell an explanation from a use has to be deleted the first
# time somebody documents the rule — after which it is not there for the edit it
# was written to catch.
grep -v '^[[:space:]]*#' "$RUNNER" | grep -q 'dirty or diverged aborts by design' \
    && fail "the unconditional 'dirty or diverged' remedy is back in the runner's alert" \
    || pass "the unconditional 'dirty or diverged' remedy is gone from the runner's alert (it survives only as the header's account of the defect)"
grep -q 'remedy_for_sync_class "\$SYNC_CLASS"' "$RUNNER" \
    && pass "the sync alert prints the remedy for the class it established" || fail "the sync alert does not use remedy_for_sync_class"
grep -qE '\$\{SYNC_DETAIL:-' "$RUNNER" \
    && pass "the sync alert prints the underlying error VERBATIM" || fail "the sync alert does not print SYNC_DETAIL"
# Backoff is time taken from the drain. A budget computed before the sleeping
# and handed to --drain-timeout after it is a window-derived number that has
# quietly stopped being derived from the window.
[ "$(grep -cE '^[^#]*budget="\$\(drain_budget' "$RUNNER")" -ge 2 ] \
    && pass "the drain budget is RECOMPUTED after the sync backoff, not reused from before it" \
    || fail "the drain budget is computed only once — backoff time would be double-spent"

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
# The lost-schedule REMEDY must branch on liveness (mg-6d7b)
# ---------------------------------------------------------------------------
# On 2026-08-10 this alert fired on the nightly bounce to b802170 and told mayor
# and human to "restore by nudging the affected agents to re-register". The
# affected agent was doctor, and doctor was absent from `pogo agent list`
# entirely: there was no process to nudge. The alert derived its finding from ONE
# observation — present before the bounce, absent after — and that observation
# has two causes with OPPOSITE remedies. A nudge into the void returns no error
# worth noticing, so following the printed remedy literally ends with a fleet
# reported as restored and a mail loop still dead.
#
# These tests are written against the two POLARITIES, not against one of them:
# an implementation that hardcodes "start the agent" is exactly as broken as the
# one that hardcoded "nudge it", and only a test that can fail in both
# directions can tell the fix from the swap.

OWNERS_FIX="$(printf '%s\n' \
    'mail-check-doctor doctor crew' \
    'mail-check-pa pa crew' \
    'mail-check-mg-6d7b c6d7b polecat' \
    'mail-check-pm-dealdesk pm-dealdesk crew' \
    'mail-check-architect architect crew')"

# The registry as `pogo agent list` reports it: NAME plus STATUS. Presence alone
# is not liveness — that command's own help says so, and a parked agent is
# listed with pid 0 and status "parked". doctor is absent (the 08-10 case), pa is
# running, pm-dealdesk is parked, architect is mid-restart.
STATES_FIX="$(printf '%s\n' \
    'mayor running' \
    'pa running' \
    'pm-dealdesk parked' \
    'architect restarting')"

# --- the case it actually fired on: agent gone -------------------------------
V="$(lost_schedule_verdict mail-check-doctor "$OWNERS_FIX" "$STATES_FIX")"
[ "$V" = "gone doctor absent" ] \
    && pass "verdict: a schedule whose agent is NOT in the registry is 'gone'" \
    || fail "lost_schedule_verdict said '$V' for an agent absent from the registry"

B="$(lost_schedule_body mail-check-doctor "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"pogo agent start doctor"*) pass "gone: the mail prescribes 'pogo agent start doctor' — the remedy that CAN work" ;;
    *) fail "gone: the mail does not tell anyone to start the agent: $B" ;;
esac
case "$B" in
    *"pogo nudge doctor"*) fail "gone: the mail STILL prescribes a nudge for an agent that is not there — this is the whole defect: $B" ;;
    *) pass "gone: the mail does NOT prescribe a nudge — there is nothing to nudge" ;;
esac
case "$B" in
    *"is NOT RUNNING"*) pass "gone: the mail says the agent is not running, so the reader can tell this case from the other" ;;
    *) fail "gone: the mail never states the fact the remedy turns on: $B" ;;
esac
# The second-order finding: an agent that cannot auto-start regenerates this
# alert every single night. Naming the class is what stops it reading as a new
# incident twelve nights running.
case "$B" in
    *auto_start*) pass "gone: the mail names auto_start, the reason a crew agent does not come back on its own" ;;
    *) fail "gone: nothing in the mail explains why the agent will be missing again tomorrow: $B" ;;
esac
# ...and naming it must not read as an invitation to switch it on. architect's
# constraint on this ticket, 2026-08-10 03:12Z: doctor's auto_start=false is a
# deliberate mitigation for mg-8677 (the reap lets auto_start override a corpse,
# mg-d9d1/mg-d6ac). An alert that names the missing flag as the cause is one
# quick read away from being taken as a request to add it, and adding it buys a
# quieter mail with a live reap bug. The mail has to close that reading itself —
# it is read at 02:03 by someone who does not have those ticket numbers.
case "$B" in
    *"DO NOT flip auto_start"*) pass "gone: the mail forbids flipping auto_start to silence itself" ;;
    *) fail "gone: naming auto_start reads as an invitation to enable it: $B" ;;
esac
case "$B" in
    *mg-8677*) pass "gone: it cites the bug the false setting mitigates, so the refusal is checkable rather than asserted" ;;
    *) fail "gone: the mail forbids a change without saying what it would break: $B" ;;
esac

# --- the OTHER polarity: agent alive, schedule lost --------------------------
# Without this the fix is a swap, not a branch.
V="$(lost_schedule_verdict mail-check-pa "$OWNERS_FIX" "$STATES_FIX")"
[ "$V" = "alive pa running" ] \
    && pass "verdict: a schedule whose agent IS in the registry is 'alive'" \
    || fail "lost_schedule_verdict said '$V' for an agent that is running"

B="$(lost_schedule_body mail-check-pa "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"pogo nudge pa"*) pass "alive: the nudge remedy SURVIVES for the case it was always right for" ;;
    *) fail "alive: the original remedy was lost rather than branched: $B" ;;
esac
case "$B" in
    *"pogo agent start pa"*) fail "alive: the mail tells someone to start an agent that is already running: $B" ;;
    *) pass "alive: the mail does not prescribe a start for a running agent" ;;
esac

# --- both at once: one mail, two different remedies --------------------------
# The real bounce loses several schedules at a time, and they are not all in the
# same class. A body that picks one remedy for the whole list is the same defect
# one layer up.
B="$(lost_schedule_body "$(printf 'mail-check-doctor\nmail-check-pa')" "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"pogo agent start doctor"*) pass "mixed: the gone agent still gets 'start' when a live one shares the mail" ;;
    *) fail "mixed: the gone agent's remedy was lost: $B" ;;
esac
case "$B" in
    *"pogo nudge pa"*) pass "mixed: the live agent still gets 'nudge' when a gone one shares the mail" ;;
    *) fail "mixed: the live agent's remedy was lost: $B" ;;
esac

# --- liveness UNKNOWN: no confident remedy at all ----------------------------
# "The registry could not be read" must not collapse into either branch. It
# collapsing into (a) is the bug being fixed; collapsing into (b) would tell
# someone to start a fleet that is fine.
V="$(lost_schedule_verdict mail-check-doctor "$OWNERS_FIX" "?")"
[ "$V" = "unknown doctor ?" ] \
    && pass "verdict: an unreadable registry is 'unknown' — a third answer, not a default" \
    || fail "lost_schedule_verdict said '$V' when liveness was unreadable"

B="$(lost_schedule_body mail-check-doctor "$OWNERS_FIX" "?" 120)"
case "$B" in
    *"could NOT determine"*) pass "unknown: the mail says it does not know" ;;
    *) fail "unknown: the mail hides its own ignorance: $B" ;;
esac
case "$B" in
    *"ONLY if it is"*) pass "unknown: both commands are printed, each gated on a check the reader must run" ;;
    *) fail "unknown: the mail prescribes without gating: $B" ;;
esac

# --- an EMPTY registry is not an unreadable one ------------------------------
# A fleet with nothing running is a real, reportable state, and every agent in it
# is genuinely gone. Only the read FAILING is unknown.
V="$(lost_schedule_verdict mail-check-doctor "$OWNERS_FIX" "")"
[ "$V" = "gone doctor absent" ] \
    && pass "verdict: an EMPTY registry means gone, not unknown" \
    || fail "lost_schedule_verdict said '$V' for an empty (but readable) registry"

# --- PRESENCE IS NOT LIVENESS: the parked agent (mg-6d7b, found in review) ---
# `pogo agent list` says so in its own one-line help, and it means it: a parked
# agent is LISTED, with pid 0 and status "parked". A fix that reads presence
# alone calls it alive and prescribes a nudge for a process that is not there —
# the identical defect, reintroduced by the repair for it. Parked is a THIRD
# class with a third remedy, and neither of the other two is even close: there
# is nothing to nudge, and starting it would undo a deliberate parking.
V="$(lost_schedule_verdict mail-check-pm-dealdesk "$OWNERS_FIX" "$STATES_FIX")"
[ "$V" = "parked pm-dealdesk parked" ] \
    && pass "verdict: a PARKED agent is its own class — present in the registry, not running" \
    || fail "lost_schedule_verdict said '$V' for a parked agent (presence read as liveness?)"

B="$(lost_schedule_body mail-check-pm-dealdesk "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"pogo nudge pm-dealdesk"*) fail "parked: the mail prescribes a nudge for an agent with no process: $B" ;;
    *) pass "parked: no nudge — a parked agent has nothing to nudge" ;;
esac
case "$B" in
    *"pogo agent start pm-dealdesk"*) fail "parked: the mail prescribes a start, which would undo the parking: $B" ;;
    *) pass "parked: no start — parking is deliberate and survives restarts" ;;
esac
case "$B" in
    *"pogo agent wake pm-dealdesk"*) pass "parked: the mail names 'wake', the only command that applies" ;;
    *) fail "parked: no applicable remedy is offered at all: $B" ;;
esac

# --- a status that is neither: no confident remedy --------------------------
# StatusRestarting exists. Guessing between nudge and start for it is guessing.
V="$(lost_schedule_verdict mail-check-architect "$OWNERS_FIX" "$STATES_FIX")"
[ "$V" = "odd architect restarting" ] \
    && pass "verdict: a status that is neither running nor parked is reported as itself" \
    || fail "lost_schedule_verdict said '$V' for a restarting agent"
B="$(lost_schedule_body mail-check-architect "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"status 'restarting'"*) pass "odd status: the mail quotes the status it actually read" ;;
    *) fail "odd status: the observed status is not in the mail: $B" ;;
esac
case "$B" in
    *"pogo nudge architect"*|*"pogo agent start architect"*) fail "odd status: a remedy was guessed anyway: $B" ;;
    *) pass "odd status: neither remedy is prescribed — the alert does not guess" ;;
esac

# --- a polecat is a third class, and 'pogo agent start' does not apply --------
# The fix must not exhibit the defect it removes. `pogo agent start` reads
# ~/.pogo/agents/crew/<name>.md; a polecat has no such file, so prescribing it
# for a polecat is another remedy that cannot work.
B="$(lost_schedule_body mail-check-mg-6d7b "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"pogo agent start c6d7b"*) fail "polecat: the mail prescribes a crew-only command for a polecat: $B" ;;
    *) pass "polecat: the mail does not prescribe 'pogo agent start' for a polecat" ;;
esac
case "$B" in
    *"mg show mg-6d7b"*) pass "polecat: the mail points at the WORK ITEM, which is what decides whether it is owed" ;;
    *) fail "polecat: the mail gives no way to tell a finished polecat from a lost one: $B" ;;
esac

# --- the owner is never guessed from the schedule id -------------------------
# mail-check-mg-6d7b belongs to agent c6d7b. Stripping the prefix names "mg-6d7b",
# an agent that has never existed — a remedy addressed to a nonexistent agent is
# the same unusable output in a new costume.
case "$(lost_schedule_body mail-check-mg-6d7b "$OWNERS_FIX" "$STATES_FIX" 120)" in
    *"nudge mg-6d7b"*|*"start mg-6d7b"*) fail "the remedy addressed the schedule id as if it were an agent name" ;;
    *) pass "the remedy addresses the OWNER (c6d7b), never the id stem (mg-6d7b)" ;;
esac

# An id with no owner in the map cannot be acted on, and the mail must say so
# rather than invent a name.
B="$(lost_schedule_body mail-check-orphan "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"did not name an owner"*) pass "unmapped id: the mail refuses to guess an agent name" ;;
    *) fail "unmapped id: the mail acted on an owner it does not have: $B" ;;
esac
case "$B" in
    *"nudge orphan"*|*"start orphan"*) fail "unmapped id: a name was invented from the id anyway: $B" ;;
    *) pass "unmapped id: no invented agent name reaches the remedy" ;;
esac

# --- what the body must KEEP ------------------------------------------------
# The one thing the old alert got right: this degradation looks healthy. Losing
# that sentence while fixing the remedy would trade one defect for another.
B="$(lost_schedule_body mail-check-doctor "$OWNERS_FIX" "$STATES_FIX" 120)"
case "$B" in
    *"WILL LOOK HEALTHY"*) pass "the body keeps the sentence the old alert got right" ;;
    *) fail "the 'looks healthy' warning was dropped: $B" ;;
esac
case "$B" in
    *"120s later"*) pass "the body still reports the grace it waited" ;;
    *) fail "the grace period is no longer in the mail: $B" ;;
esac
case "$B" in
    *mail-check-doctor*) pass "the body still names the schedule id that went missing" ;;
    *) fail "the missing id is no longer in the mail: $B" ;;
esac

# --- mail_check_owners parses the two documents it is given ------------------
# Fixture-driven, because the failure this guards is silent: an owner map that
# comes back empty degrades every remedy to "unknown" and the alert still sends.
OWNDIR="$WORK/owners"; mkdir -p "$OWNDIR"
cat > "$OWNDIR/pogo" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list") cat "$POGO_STUB_AGENTS" ;;
  "schedule list") cat "$POGO_STUB_SCHED" ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$OWNDIR/pogo"
cat > "$OWNDIR/agents.json" <<'STUB'
[
  {
    "name": "doctor",
    "pid": 5959,
    "type": "crew",
    "status": "running",
    "process_name": "pogo-crew-doctor"
  },
  {
    "name": "pm-dealdesk",
    "pid": 0,
    "type": "crew",
    "status": "parked",
    "process_name": "pogo-crew-pm-dealdesk"
  },
  {
    "name": "c6d7b",
    "pid": 3029,
    "type": "polecat",
    "status": "running",
    "process_name": "pogo-cat-c6d7b"
  }
]
STUB
cat > "$OWNDIR/sched.json" <<'STUB'
[
  {
    "id": "mail-check-doctor",
    "agent": "doctor",
    "kind": "mail-check"
  },
  {
    "id": "mail-check-mg-6d7b",
    "agent": "c6d7b",
    "kind": "mail-check"
  },
  {
    "id": "gc-sweep",
    "agent": "mayor",
    "kind": "other"
  }
]
STUB
OWN="$(POGO_CLI="$OWNDIR/pogo" POGO_STUB_AGENTS="$OWNDIR/agents.json" POGO_STUB_SCHED="$OWNDIR/sched.json" mail_check_owners)"
case "$OWN" in
    *"mail-check-doctor doctor crew"*) pass "mail_check_owners maps a crew schedule to its agent AND its type" ;;
    *) fail "mail_check_owners crew row: $OWN" ;;
esac
# The LAST agent object shares an awk record with the document separator. If the
# separator is honoured before that record is read as an agent, the last agent
# silently loses its type — and its remedy silently loses the polecat branch.
case "$OWN" in
    *"mail-check-mg-6d7b c6d7b polecat"*) pass "mail_check_owners keeps the type of the LAST agent in the list" ;;
    *) fail "mail_check_owners dropped the last agent's type: $OWN" ;;
esac
case "$OWN" in
    *gc-sweep*) fail "mail_check_owners included a schedule that is not a mail-check: $OWN" ;;
    *) pass "mail_check_owners ignores schedules that are not mail-checks" ;;
esac

# agent_states must carry the STATUS, and must distinguish "read it, it is
# empty" from "could not read".
R="$(POGO_CLI="$OWNDIR/pogo" POGO_STUB_AGENTS="$OWNDIR/agents.json" agent_states)"; RRC=$?
{ [ "$RRC" -eq 0 ] && [ "$R" = "$(printf 'c6d7b running\ndoctor running\npm-dealdesk parked')" ]; } \
    && pass "agent_states names only real agents (not process_name) and carries each one's status" \
    || fail "agent_states returned rc=$RRC states='$R'"
printf '[]' > "$OWNDIR/empty.json"
R="$(POGO_CLI="$OWNDIR/pogo" POGO_STUB_AGENTS="$OWNDIR/empty.json" agent_states)"; RRC=$?
{ [ "$RRC" -eq 0 ] && [ -z "$R" ]; } \
    && pass "agent_states: an empty fleet reads as empty, exit 0 — a real answer" \
    || fail "agent_states on an empty registry: rc=$RRC states='$R'"
R="$(POGO_CLI="" agent_states)"; RRC=$?
{ [ "$RRC" -ne 0 ] && [ -z "$R" ]; } \
    && pass "agent_states: no CLI to ask exits NON-ZERO, so the caller reaches the '?' sentinel" \
    || fail "agent_states with no CLI: rc=$RRC states='$R'"

# --- the callsite is wired to all of it -------------------------------------
# The helpers can be perfect and unreached. These read the runner itself, because
# the alternative is running a whole nightly bounce to find out.
grep -q 'lost_schedule_body "\$lost" "\$pre_owners" "\$states"' "$RUNNER" \
    && pass "the alert body is COMPUTED from the lost ids, the owner map and the registry" \
    || fail "the lost-schedule alert does not call lost_schedule_body with all three inputs"
grep -q 'states="\$(agent_states)" || states="?"' "$RUNNER" \
    && pass "the callsite turns an unreadable registry into '?' instead of into 'nothing is running'" \
    || fail "the callsite does not guard agent_states' failure"
grep -q 'pre_owners="\$(mail_check_owners)"' "$RUNNER" \
    && pass "the owner map is captured BEFORE the bounce, while the agents that own the schedules still exist" \
    || fail "the owner map is not captured pre-bounce"
[ "$(grep -c 'Restore by nudging the affected agents to re-register' "$RUNNER")" -eq 0 ] \
    && pass "the unconditional nudge remedy is GONE from the runner" \
    || fail "the runner still contains the unconditional 'nudge to re-register' remedy"

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
# fire_hours_from_plist / fire_hours_from_launchctl — READ, not duplicated
# ---------------------------------------------------------------------------
# THE POSITIVE CONTROL FOR THIS SECTION HAD TO BE BUILT, because the free one is
# gone. Until 2026-08-07 14:03:28 the installed plist carried a single 03:00 fire
# while the runner's hardcoded list said "3 4 5", so the world itself was the
# RED arm — mg-fc99 said so and said to take the control while the bug was still
# there. Nobody did; mg-b201 installed the correct three-fire plist, which was
# the right thing to do, and the control was spent. A check first run against a
# world where the defect is already fixed has never been shown to fire, so both
# arms below are constructed.
#
# AND THE BROKEN SHAPE IS THE POINT. What was installed was StartCalendarInterval
# as a BARE DICT, not an array of one:
#
#     Dict { Hour = 3, Minute = 0 }        <- what was actually installed
#     Array [ Dict { Hour = 3 } ]          <- NOT what was installed
#
# A reader that walks array elements passes clean on the dict: it finds no
# mismatching element because it finds no elements at all. That is the naive
# implementation of this very check reporting GREEN against the state that
# motivated it, so the RED arm uses the dict form and there is a test below
# proving an index-based reader would have missed it.
PLIST_DIR="$WORK/plists"
mkdir -p "$PLIST_DIR"

# THE BROKEN STATE, reproduced: one 03:00 fire, as a BARE DICT.
cat > "$PLIST_DIR/dict-0300.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.pogo.deploy</string>
    <key>StartCalendarInterval</key>
    <dict>
        <key>Hour</key>
        <integer>3</integer>
        <key>Minute</key>
        <integer>0</integer>
    </dict>
</dict>
</plist>
EOF

# The corrected state: three fires, as an ARRAY.
cat > "$PLIST_DIR/array-345.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.pogo.deploy</string>
    <key>StartCalendarInterval</key>
    <array>
        <dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>
        <dict><key>Hour</key><integer>4</integer><key>Minute</key><integer>0</integer></dict>
        <dict><key>Hour</key><integer>5</integer><key>Minute</key><integer>0</integer></dict>
    </array>
</dict>
</plist>
EOF

# An array of one — the shape people ASSUME the broken state had. Kept so the
# dict test above cannot be satisfied by a reader that only handles arrays.
cat > "$PLIST_DIR/array-0300.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>StartCalendarInterval</key>
    <array>
        <dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>
    </array>
</dict>
</plist>
EOF

# An entry with a Minute and NO Hour: launchd fires that EVERY hour. No hour list
# can say that, so the reader must refuse rather than return a shorter list.
cat > "$PLIST_DIR/dict-nohour.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>StartCalendarInterval</key>
    <dict>
        <key>Minute</key>
        <integer>30</integer>
    </dict>
</dict>
</plist>
EOF

# --- ARM 1: RED. The bare dict with one 03:00 fire. ------------------------
DICT_HOURS="$(fire_hours_from_plist "$PLIST_DIR/dict-0300.plist")" || DICT_HOURS="<unreadable>"
[ "$DICT_HOURS" = "3" ] \
    && pass "fire_hours_from_plist: the BARE DICT form reads as '3' — the shape a reader that walks arrays skips entirely" \
    || fail "fire_hours_from_plist bare dict: got '$DICT_HOURS', want '3'"

# The RED itself: with the world in that state, a 03:00 stall has NO retry, and
# the runner must say so. This is the assertion mg-fc99 ruled for.
next_fire_hour 3 "$DICT_HOURS" >/dev/null \
    && fail "RED ARM DID NOT FIRE: with a single 03:00 fire installed, the runner still claimed a later fire — this is the exact defect (a retry promised that does not exist)" \
    || pass "RED ARM: against the broken world (one 03:00 fire, bare dict) the runner finds NO later fire — the hardcoded '3 4 5' claimed 04:00 here and was wrong"

FIRE_HOURS="$DICT_HOURS"; FIRE_HOURS_SOURCE=plist
WINDOW_END=6; RESERVE=1200; MAX_DRAIN=7200; MIN_DRAIN=600
POGO_DEPLOY_NOW=3 retry_will_follow 7 \
    && fail "RED ARM: retry_will_follow promised a 04:00 retry against a plist with only 03:00" \
    || pass "RED ARM: retry_will_follow is FALSE against the broken plist — the alert alerts tonight instead of waiting for a fire that never comes"

# --- ARM 2: GREEN. The three-fire array actually installed now. ------------
ARR_HOURS="$(fire_hours_from_plist "$PLIST_DIR/array-345.plist")" || ARR_HOURS="<unreadable>"
[ "$ARR_HOURS" = "3 4 5" ] \
    && pass "fire_hours_from_plist: the ARRAY form reads as '3 4 5'" \
    || fail "fire_hours_from_plist array: got '$ARR_HOURS', want '3 4 5'"

FIRE_HOURS="$ARR_HOURS"; FIRE_HOURS_SOURCE=plist
POGO_DEPLOY_NOW=3 retry_will_follow 7 \
    && pass "GREEN ARM: against the corrected world (3/4/5) a 03:00 stall DOES have a retry — the check is not simply always-red" \
    || fail "GREEN ARM: retry_will_follow false against a three-fire plist"

# Both arms came from the same reader with no shape flag passed to it.
[ "$(fire_hours_from_plist "$PLIST_DIR/array-0300.plist")" = "3" ] \
    && pass "fire_hours_from_plist: an ARRAY OF ONE reads as '3' too — dict and array of the same schedule are indistinguishable to this reader" \
    || fail "fire_hours_from_plist array-of-one"

# THE TRAP, demonstrated rather than asserted: the obvious index-based read finds
# nothing on the dict, so a check built that way would have reported GREEN
# against the very state it existed to catch.
if [ -x /usr/libexec/PlistBuddy ]; then
    /usr/libexec/PlistBuddy -c 'Print :StartCalendarInterval:0:Hour' "$PLIST_DIR/dict-0300.plist" >/dev/null 2>&1 \
        && fail "the index-based read succeeded on a bare dict — the premise of this section is wrong" \
        || pass "TRAP CONFIRMED: 'Print :StartCalendarInterval:0:Hour' FAILS on the bare dict, so an array-walking check finds no mismatching element because it finds no elements"
    /usr/libexec/PlistBuddy -c 'Print :StartCalendarInterval:0:Hour' "$PLIST_DIR/array-345.plist" >/dev/null 2>&1 \
        && pass "...and the same index-based read SUCCEEDS on the array — the failure above is the shape, not a broken command" \
        || fail "the index-based read failed on the array too; the trap demo proves nothing"
fi

# An entry launchd fires every hour cannot be reported as an hour list.
fire_hours_from_plist "$PLIST_DIR/dict-nohour.plist" >/dev/null 2>&1 \
    && fail "fire_hours_from_plist returned hours for an entry with no Hour key (launchd fires that EVERY hour)" \
    || pass "fire_hours_from_plist refuses an entry with a Minute and no Hour rather than returning a shorter list"

fire_hours_from_plist "$PLIST_DIR/does-not-exist.plist" >/dev/null 2>&1 \
    && fail "fire_hours_from_plist invented hours for a plist that is not there" \
    || pass "fire_hours_from_plist: an absent plist is a refusal, not an empty schedule"

# The plutil FALLBACK, exercised rather than assumed. It is a second parser of a
# second output format, reached only on a host without PlistBuddy — which is to
# say never here, so nothing but this would ever run it. It has to be
# shape-agnostic on its own terms: it counts <dict> tags where the PlistBuddy
# branch counts `Dict {`, and neither indexes into the array.
PB_DICT="$(PLISTBUDDY=/nonexistent fire_hours_from_plist "$PLIST_DIR/dict-0300.plist")" || PB_DICT="<refused>"
PB_ARR="$(PLISTBUDDY=/nonexistent fire_hours_from_plist "$PLIST_DIR/array-345.plist")" || PB_ARR="<refused>"
[ "$PB_DICT" = "3" ] && [ "$PB_ARR" = "3 4 5" ] \
    && pass "the plutil fallback reads BOTH shapes too ('$PB_DICT' from the bare dict, '$PB_ARR' from the array) — a host without PlistBuddy is not a host that quietly stops knowing its schedule" \
    || fail "plutil fallback: dict gave '$PB_DICT' (want 3), array gave '$PB_ARR' (want 3 4 5)"
PLISTBUDDY=/nonexistent fire_hours_from_plist "$PLIST_DIR/dict-nohour.plist" >/dev/null 2>&1 \
    && fail "the plutil fallback returned hours for an entry with no Hour key" \
    || pass "...and it refuses the every-hour entry on the same terms as the PlistBuddy branch"

# ---------------------------------------------------------------------------
# The LOADED job, not the file — `launchctl print` (requirement 5)
# ---------------------------------------------------------------------------
# A corrected plist that was never reloaded is byte-identical to a working one on
# disk and does nothing at 04:00. mg-b201's own verification turned on exactly
# this distinction, so the authority is the loaded job.
LC_DIR="$WORK/launchctl"
mkdir -p "$LC_DIR"

# The three-fire loaded job, in the format `launchctl print` actually emits —
# captured from this host on 2026-08-13.
cat > "$LC_DIR/loaded-345.txt" <<'EOF'
	event triggers = {
		com.pogo.deploy.268435481 => {
			keepalive = 0
			service = com.pogo.deploy
			stream = com.apple.launchd.calendarinterval
			monitor = com.apple.UserEventAgent-Aqua
			descriptor = {
				"Minute" => 0
				"Hour" => 5
			}
		}
		com.pogo.deploy.268435480 => {
			keepalive = 0
			service = com.pogo.deploy
			stream = com.apple.launchd.calendarinterval
			monitor = com.apple.UserEventAgent-Aqua
			descriptor = {
				"Minute" => 0
				"Hour" => 4
			}
		}
		com.pogo.deploy.268435479 => {
			keepalive = 0
			service = com.pogo.deploy
			stream = com.apple.launchd.calendarinterval
			monitor = com.apple.UserEventAgent-Aqua
			descriptor = {
				"Minute" => 0
				"Hour" => 3
			}
		}
	}
EOF

# The pre-mg-b201 loaded job: ONE trigger. This is what `launchctl print` showed
# on the night the runner believed two retries were coming.
cat > "$LC_DIR/loaded-0300.txt" <<'EOF'
	event triggers = {
		com.pogo.deploy.268435479 => {
			keepalive = 0
			service = com.pogo.deploy
			stream = com.apple.launchd.calendarinterval
			monitor = com.apple.UserEventAgent-Aqua
			descriptor = {
				"Minute" => 0
				"Hour" => 3
			}
		}
	}
EOF

# A job whose only trigger is a StartInterval — no calendar hours to read.
cat > "$LC_DIR/loaded-interval.txt" <<'EOF'
	event triggers = {
		com.pogo.deploy.268435479 => {
			keepalive = 0
			service = com.pogo.deploy
			stream = com.apple.launchd.periodic
			monitor = com.apple.UserEventAgent-Aqua
			descriptor = {
				"Interval" => 3600
			}
		}
	}
EOF

[ "$(fire_hours_from_launchctl "$LC_DIR/loaded-345.txt")" = "3 4 5" ] \
    && pass "fire_hours_from_launchctl: three calendarinterval descriptors read as '3 4 5', SORTED ascending (launchctl prints them 5/4/3)" \
    || fail "fire_hours_from_launchctl 345: got '$(fire_hours_from_launchctl "$LC_DIR/loaded-345.txt")'"

[ "$(fire_hours_from_launchctl "$LC_DIR/loaded-0300.txt")" = "3" ] \
    && pass "fire_hours_from_launchctl: the pre-fix loaded job reads as '3' — one fire, and the runner will not claim a second" \
    || fail "fire_hours_from_launchctl 0300"

fire_hours_from_launchctl "$LC_DIR/loaded-interval.txt" >/dev/null 2>&1 \
    && fail "fire_hours_from_launchctl read hours out of a job with no calendar trigger at all" \
    || pass "fire_hours_from_launchctl: a StartInterval-only job yields no hours (a refusal, not an empty list)"

# A calendar descriptor with a Minute and NO Hour fires EVERY hour, and it sits
# ALONGSIDE two ordinary ones. This is the case that silently returns a shorter
# list than the truth if the reader's refusal is thrown away by a pipeline — the
# hours it CAN read are real, so the result looks perfectly plausible.
cat > "$LC_DIR/loaded-hourly.txt" <<'EOF'
	event triggers = {
		com.pogo.deploy.268435479 => {
			stream = com.apple.launchd.calendarinterval
			descriptor = {
				"Minute" => 0
				"Hour" => 3
			}
		}
		com.pogo.deploy.268435480 => {
			stream = com.apple.launchd.calendarinterval
			descriptor = {
				"Minute" => 30
			}
		}
		com.pogo.deploy.268435481 => {
			stream = com.apple.launchd.calendarinterval
			descriptor = {
				"Minute" => 0
				"Hour" => 4
			}
		}
	}
EOF
HOURLY_OUT="$(fire_hours_from_launchctl "$LC_DIR/loaded-hourly.txt")" && HOURLY_RC=0 || HOURLY_RC=$?
[ "$HOURLY_RC" -ne 0 ] \
    && pass "fire_hours_from_launchctl REFUSES a schedule containing an every-hour descriptor rather than reporting the two hours it could read" \
    || fail "fire_hours_from_launchctl returned '$HOURLY_OUT' for a schedule that also fires every hour at :30 — a shorter list than the truth, which is the defect this whole ticket is about"

# The reader runs an exec on the 03:00 path, which is the objection the old
# hardcoded constant was defended with. It is bounded, and it is bounded by the
# COMMAND'S STATUS rather than by run_bounded's BOUNDED_TIMED_OUT flag — that
# flag is set inside the subshell a command substitution creates, so this scope
# would read whatever an earlier bounded call left behind.
printf '#!/bin/sh\nsleep 300\n' > "$WORK/launchctl-hangs"
chmod +x "$WORK/launchctl-hangs"
LC_T0="$(date +%s)"
(
    LAUNCHCTL="$WORK/launchctl-hangs"
    TOOL_PROBE_TIMEOUT=2
    fire_hours_from_launchctl >/dev/null 2>&1
) && fail "a launchctl that never returns produced fire hours" \
  || pass "fire_hours_from_launchctl: a launchctl that NEVER RETURNS is a refusal — the schedule read cannot hang the one path that has to work when everything else is broken"
LC_ELAPSED=$(( $(date +%s) - LC_T0 ))
[ "$LC_ELAPSED" -lt 30 ] \
    && pass "...and it refused in ${LC_ELAPSED}s, not after the stub's 300 — the bound is real, not the stub exiting on its own" \
    || fail "fire_hours_from_launchctl took ${LC_ELAPSED}s against a 2s bound"

printf '#!/bin/sh\ncat %q\n' "$LC_DIR/loaded-345.txt" > "$WORK/launchctl-stub"
chmod +x "$WORK/launchctl-stub"
STALE_HOURS="$(
    BOUNDED_TIMED_OUT=true
    LAUNCHCTL="$WORK/launchctl-stub"
    fire_hours_from_launchctl 2>/dev/null
)"
[ "$STALE_HOURS" = "3 4 5" ] \
    && pass "fire_hours_from_launchctl reads a healthy launchctl even with a STALE BOUNDED_TIMED_OUT=true left over from an earlier bounded call — the read does not depend on a flag that cannot cross the subshell" \
    || fail "a stale BOUNDED_TIMED_OUT suppressed a successful schedule read (got '$STALE_HOURS')"

# THE RELOAD GAP, which is the reason the loaded job is the authority: the file
# says 3 4 5 and the job fires only at 03:00. Every file-based check is green.
RESOLVE_OUT="$WORK/resolve.log"
(
    POGO_DEPLOY_FIRE_HOURS=""
    DEPLOY_PLIST="$PLIST_DIR/array-345.plist"
    fire_hours_from_launchctl() { printf '3'; }
    resolve_fire_hours >"$RESOLVE_OUT" 2>&1
    printf '%s|%s\n' "$FIRE_HOURS" "$FIRE_HOURS_SOURCE" > "$WORK/resolve.vals"
)
read -r RESOLVED < "$WORK/resolve.vals"
[ "$RESOLVED" = "3|launchctl" ] \
    && pass "resolve_fire_hours: the LOADED job wins over the file — a plist corrected and never reloaded does not get to describe tonight" \
    || fail "resolve_fire_hours preferred the file over the loaded job (got '$RESOLVED')"
grep -q 'never reloaded' "$RESOLVE_OUT" \
    && pass "resolve_fire_hours SAYS SO when file and loaded job disagree, naming both lists and the command that fixes it" \
    || fail "resolve_fire_hours resolved a file/loaded disagreement silently: $(cat "$RESOLVE_OUT")"

# Neither source readable: the run must make NO claim, which is a third case and
# not the same sentence as "no fire is left tonight".
(
    POGO_DEPLOY_FIRE_HOURS=""
    DEPLOY_PLIST="$WORK/absent.plist"
    fire_hours_from_launchctl() { return 1; }
    resolve_fire_hours >/dev/null 2>&1
    printf '%s|%s|%s\n' "$FIRE_HOURS" "$FIRE_HOURS_SOURCE" "$(fires_left_phrase)" > "$WORK/resolve.unknown"
)
UNKNOWN_LINE="$(cat "$WORK/resolve.unknown")"
case "$UNKNOWN_LINE" in
    "|unknown|"*"could not read its own launchd schedule"*)
        pass "resolve_fire_hours: with neither source readable the run says it CANNOT TELL — it neither promises a retry nor asserts there is none" ;;
    *) fail "unreadable schedule did not produce the third case: '$UNKNOWN_LINE'" ;;
esac
case "$UNKNOWN_LINE" in
    *"no fire is left tonight"*) fail "the unknown case still asserted 'no fire is left tonight' — that is a claim about fires it never saw" ;;
    *) pass "the unknown case does NOT assert 'no fire is left tonight'" ;;
esac

# The override still works — the tests above use it — but it is not the default.
grep -q 'FIRE_HOURS="${POGO_DEPLOY_FIRE_HOURS:-}"' "$RUNNER" \
    && pass "the runner ships NO hardcoded fire-hour list: POGO_DEPLOY_FIRE_HOURS defaults to empty and the hours come from the world" \
    || fail "a hardcoded fire-hour default is back in the runner — that is the generator this ticket removed"
(
    POGO_DEPLOY_FIRE_HOURS="1 2"
    fire_hours_from_launchctl() { printf '3 4 5'; }
    resolve_fire_hours >/dev/null 2>&1
    printf '%s|%s\n' "$FIRE_HOURS" "$FIRE_HOURS_SOURCE" > "$WORK/resolve.override"
)
[ "$(cat "$WORK/resolve.override")" = "1 2|override" ] \
    && pass "POGO_DEPLOY_FIRE_HOURS still pins the list for a test or a manual run, and is LABELLED as a pin rather than as the world" \
    || fail "the override no longer works: $(cat "$WORK/resolve.override")"

# --- and against the machine, not a fixture -------------------------------
# The two fixtures above are transcriptions. This one is the world: if the job is
# loaded on this host, the reader must agree with `launchctl print`, and the file
# must agree with the loaded job (i.e. nobody has left an unreloaded edit here).
if [ -x /bin/launchctl ] && /bin/launchctl print "gui/$(id -u)/com.pogo.deploy" >/dev/null 2>&1; then
    LIVE_LOADED="$(DEPLOY_LABEL=com.pogo.deploy fire_hours_from_launchctl)" || LIVE_LOADED=""
    LIVE_EXPECT="$(/bin/launchctl print "gui/$(id -u)/com.pogo.deploy" 2>/dev/null \
        | awk '/"Hour"[ \t]*=>/ { h = $0; sub(/.*=>[ \t]*/, "", h); sub(/[^0-9].*$/, "", h); print h + 0 }' \
        | sort -n -u | tr '\n' ' ')"
    LIVE_EXPECT="${LIVE_EXPECT% }"
    [ -n "$LIVE_LOADED" ] && [ "$LIVE_LOADED" = "$LIVE_EXPECT" ] \
        && pass "LIVE: fire_hours_from_launchctl reads '$LIVE_LOADED' from the job actually loaded on this host, matching an independent scrape of the same output" \
        || fail "LIVE: reader said '$LIVE_LOADED', independent scrape of launchctl print said '$LIVE_EXPECT'"
    LIVE_FILE="$(fire_hours_from_plist "$HOME/Library/LaunchAgents/com.pogo.deploy.plist")" || LIVE_FILE=""
    if [ -n "$LIVE_FILE" ]; then
        [ "$LIVE_FILE" = "$LIVE_LOADED" ] \
            && pass "LIVE: the installed plist ('$LIVE_FILE') and the loaded job ('$LIVE_LOADED') agree — the file on this host has been reloaded" \
            || fail "LIVE: $HOME/Library/LaunchAgents/com.pogo.deploy.plist says '$LIVE_FILE' but the loaded job fires '$LIVE_LOADED' — an unreloaded edit is live on this machine"
    else
        echo "note: no readable com.pogo.deploy.plist on this host — the file/loaded cross-check did not run"
    fi
else
    echo "note: com.pogo.deploy is not loaded on this host — the live launchctl checks did not run (the fixture arms above did)"
fi

FIRE_HOURS="3 4 5"; FIRE_HOURS_SOURCE=plist
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
# Exit 10 — a sync that aborted on a class establishing NOTHING about the tree
# (mg-0d70). Same discriminator as exit 7, one layer up: the repo state is
# unknown, so re-asking is how you find out. This half is INERT on this box until
# mg-fc99 installs the 04:00 and 05:00 fires — the installed plist's
# StartCalendarInterval is a dict with Hour=3, so there is nothing to carry it.
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 10")" = "retry" ] \
    && pass "attempt_disposition: tonight's transient SYNC abort (10) -> RETRY, the 08-05 failure that cost a whole night" || fail "disposition rc=10"
[ "$(attempt_disposition 2026-07-31 "2026-07-31 1 1")" = "settled" ] \
    && pass "attempt_disposition: a NON-retryable sync abort still exits 1 and settles the night (dirty and diverged must not reopen it)" || fail "disposition rc=1"
rc_reopens_night 7 && rc_reopens_night 10 \
    && pass "rc_reopens_night: 7 and 10 — the two outcomes that established nothing" || fail "rc_reopens_night 7/10"
rc_reopens_night 1 || rc_reopens_night 4 || rc_reopens_night 9 || rc_reopens_night 0 \
    && fail "rc_reopens_night accepted a code that established a fact" \
    || pass "rc_reopens_night: 0, 1, 4 and 9 settle the night (each established something)"
case "$(describe_exit 10)" in
    *sync*) pass "describe_exit 10 names the sync, not the drain — the two retryable codes have different stories" ;;
    *) fail "describe_exit 10: $(describe_exit 10)" ;;
esac
[ "$(describe_exit 10)" != "$(describe_exit 7)" ] \
    && pass "describe_exit: 7 and 10 are both retryable but get DIFFERENT descriptions" || fail "describe_exit 7 and 10 are identical"

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
for c in 1 2 3 4 5 6 7 8 9 11 12 42; do
    [ -n "$(remedy_for_exit "$c")" ] || fail "remedy_for_exit $c is empty"
done
pass "remedy_for_exit: every exit code, and an unclassified one, yields a non-empty remedy"

# ---------------------------------------------------------------------------
# THE FLEET-DOWN BANNER (mg-6d2f) — "outage" must be legible in one line
# ---------------------------------------------------------------------------
# THE INCIDENT. On 2026-08-07 the nightly found orchestration already stopped,
# refused (correctly) to bounce a fleet it could not drain, and exited 6. It
# mailed pm-pogo and human at 02:00:10Z under the subject
#
#     [pogo-deploy] RED: nightly redeploy exited 6
#
# which is the same subject a build failure gets, and a build failure can wait
# until morning. This one could not: the fleet dispatched nothing for 10h39m and
# the outage ended because Daniel noticed by hand and started the server himself.
#
# The alert was sent. It was delivered. It was skimmed as ordinary. So the thing
# under test here is not whether an alert fires — that already worked — but
# whether the ONE LINE that travels distinguishes "tonight's deploy did not land"
# from "nothing is running right now".
D11="$(describe_exit 11)"; D12="$(describe_exit 12)"
case "$D11" in *"FLEET DOWN"*) pass "describe_exit 11 leads with FLEET DOWN" ;; *) fail "describe_exit 11: $D11" ;; esac
case "$D12" in *"FLEET DOWN"*) pass "describe_exit 12 leads with FLEET DOWN" ;; *) fail "describe_exit 12: $D12" ;; esac
[ "$D11" != "$D12" ] \
    && pass "describe_exit: 11 and 12 are different outages and get DIFFERENT descriptions" \
    || fail "describe_exit 11 and 12 are identical — 'came back wrong' and 'never restarted' need opposite reactions"
# 11 is post-restart, 12 is pre-restart, and which one you have decides whether
# the binary on disk is the new one. Each description must carry that.
case "$D11" in *restart*) pass "describe_exit 11 says the restart HAPPENED (the deploy landed; only the mode is wrong)" ;; *) fail "describe_exit 11 does not place itself relative to the restart: $D11" ;; esac
case "$D12" in *"before the restart"*) pass "describe_exit 12 says it refused BEFORE the restart (nothing was built, nothing bounced)" ;; *) fail "describe_exit 12 does not place itself relative to the restart: $D12" ;; esac

# fleet_is_down — the discriminator, in BOTH directions. A banner that fires on
# every failure is the generic subject again, so the negative half is the half
# that keeps it worth something.
for c in 5 8 11 12; do
    fleet_is_down "$c" || fail "fleet_is_down $c should be true — the fleet is not dispatching when the script exits $c"
done
pass "fleet_is_down: 5, 8, 11 and 12 are outages (kickstart failed / no pogod came back / came back index-only / already stopped)"
for c in 0 1 2 3 4 6 7 9 10 42; do
    fleet_is_down "$c" && fail "fleet_is_down $c should be false — the old pogod is still up and dispatching on that exit"
done
pass "fleet_is_down: 4, 7, 9 and the rest are NOT outages — the banner is spent only where it is true"

S12="$(alert_subject 12)"; S7="$(alert_subject 7)"
case "$S12" in
    *"FLEET DOWN"*) pass "alert_subject(12): the SUBJECT LINE says FLEET DOWN — the part that travels carries the fact" ;;
    *) fail "alert_subject(12) is still generic: $S12" ;;
esac
case "$S7" in
    *"FLEET DOWN"*) fail "alert_subject(7) shouts FLEET DOWN over a stalled drain — the old pogod is up and dispatching" ;;
    *"RED"*)        pass "alert_subject(7) stays RED — a missed deploy is not an outage" ;;
    *) fail "alert_subject(7): $S7" ;;
esac
[ "$S12" != "$S7" ] \
    && pass "alert_subject: an outage and a missed deploy no longer share a subject line" \
    || fail "alert_subject returns the same subject for exit 12 and exit 7 — this is the 08-07 defect exactly"
# The code stays in the subject. It is what the operator greps the log for, and
# the remedy paragraphs are keyed to it.
case "$S12" in *12*) pass "alert_subject keeps the exit code in the subject (the log and the remedy are keyed to it)" ;; *) fail "alert_subject(12) dropped the code: $S12" ;; esac

# The remedies must be actionable and must not contradict each other about the
# one fact everything else depends on: was the binary replaced?
R11="$(remedy_for_exit 11)"; R12="$(remedy_for_exit 12)"
printf '%s' "$R11" | grep -q 'pogo server start' \
    && pass "remedy(11) gives the command that ends the outage" || fail "remedy(11) does not say how to start orchestration"
printf '%s' "$R12" | grep -q 'pogo server start' \
    && pass "remedy(12) gives the command that ends the outage" || fail "remedy(12) does not say how to start orchestration"
printf '%s' "$R11" | grep -qi 'do not roll back\|deploy is complete' \
    && pass "remedy(11) says the deploy itself LANDED — do not roll back a binary that is fine" \
    || fail "remedy(11) does not tell the operator the install succeeded"
printf '%s' "$R12" | grep -qi 'nothing was built' \
    && pass "remedy(12) says nothing was built or bounced — the running pogod is untouched and still behind main" \
    || fail "remedy(12) does not say the deploy never ran"
# The 08-07 log ended on 'drain restore FAILED — pogod may STILL be draining',
# which is a claim about a flag that was never set. On exit 12 the drain flag is
# a dead end and the remedy must say so rather than send the reader after it.
printf '%s' "$R12" | grep -qi 'red herring\|never enabled' \
    && pass "remedy(12) defuses the drain flag — it was never enabled, and chasing it is what 08-07 invited" \
    || fail "remedy(12) leaves the reader to chase the drain flag"
[ "$R11" != "$R12" ] \
    && pass "remedy_for_exit: 11 and 12 get DIFFERENT paragraphs" || fail "remedy 11 and 12 are identical"

# The event carries the fact as DATA. A detector cannot filter on a subject line,
# and "the fleet is not dispatching" is the first outcome here that something
# other than a human needs to be able to react to.
alert_extra() {  # what the RED call site passes as alert()'s $3
    printf '"exit":%s,"fleet_down":%s' "$1" "$(fleet_is_down "$1" && echo true || echo false)"
}
case "$(alert_extra 12)" in
    *'"fleet_down":true'*) pass "the RED event carries fleet_down=true as structured data, not only in prose" ;;
    *) fail "alert_extra 12: $(alert_extra 12)" ;;
esac
case "$(alert_extra 7)" in
    *'"fleet_down":false'*) pass "the RED event carries fleet_down=false on a stalled drain" ;;
    *) fail "alert_extra 7: $(alert_extra 7)" ;;
esac
# alert() must actually splice $3 in, and must still emit valid JSON without it.
grep -q 'extra:+,$extra' "$RUNNER" \
    && pass "alert() splices its extra fields into the event details (and omits the comma when there are none)" \
    || fail "alert() does not thread its third argument into the emitted event"
grep -q 'alert_subject "$rc"' "$RUNNER" \
    && pass "the RED call site uses alert_subject, not a hard-coded 'RED: nightly redeploy exited' string" \
    || fail "the RED alert site still hard-codes its subject — the banner would never fire"

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
# The reason channel, end to end: a real refusal -> the mail an operator gets
# (mg-0155)
# ---------------------------------------------------------------------------
# THIS IS THE POSITIVE CONTROL THE TICKET ASKED FOR, and the shape of it is the
# point: "a remedy paragraph is only reviewable next to the failure it is
# claimed to describe." On 2026-08-07 every piece of the 03:00 alert was
# individually correct — describe_exit 6 returned what its case arm said,
# remedy_for_exit returned what its heredoc said — and the assembled mail was
# wrong on every material point. Pieces were what had been reviewed.
#
# So: drive the REAL refusals in the REAL deploy script, take the REAL record
# they leave, and print the REAL alert body the runner would mail. Nothing here
# is a fixture. If the two scripts ever disagree about the record's format, these
# fail — which is the only way a channel between two processes stays a channel.
SELF_DEPLOY="$HERE/pogo-self-deploy"
REC_DIR="$WORK/reasons"; mkdir -p "$REC_DIR"

# Run one real drain-precondition refusal in a fresh bash (a separate PROCESS,
# like the nightly, so the record is the only thing that crosses) and leave its
# record behind.
#
# The optional third argument is the RESPONSE BODY (mg-08e9) — what the daemon
# actually said. It is passed for a reason beyond coverage: the sample alerts
# printed by this file are read by humans deciding whether the alert is good
# enough, and an alert rendered from a body the harness never supplied shows
# "(empty body)" where production shows the diagnosis. A sample that
# under-represents the real mail is a bad instrument for that judgement.
make_record() {
    bash -c '
        set -u
        POGO_DEPLOY_REASON_FILE="$2"
        export POGO_DEPLOY_REASON_FILE
        # shellcheck source=/dev/null
        source "$1" >/dev/null 2>&1
        REASON_FILE="$2"
        ERR_LOG="$(mktemp)"
        DEPLOY_STAGE="drain"; DEPLOY_INSTALLED="no"
        DRAIN_PRIOR="?"; DRAIN_ARMED=true
        drain_post() { printf "\n000"; }
        # The `stopped` arm confirms its reading against /server/mode before it
        # announces anything (mg-6d2f). Pin it, or these samples depend on
        # whatever daemon is listening on :10000 while the suite runs.
        server_mode() { echo "index-only"; }
        trap on_deploy_exit EXIT
        refuse_drain_precondition "$3" "$4"
    ' _ "$SELF_DEPLOY" "$2" "$1" "${3:-}" >/dev/null 2>&1
    return $?
}

# The 2026-08-07 failure itself, first — with the body RequireOrchestration
# really answers (internal/server/server.go), so the sample alert below is the
# mail that a repeat of that night would actually produce.
make_record stopped "$REC_DIR/stopped" '{"error":"orchestration is stopped","mode":"index-only"}'; RC_STOPPED=$?
# 12, not 6, since mg-6d2f: a CONFIRMED stopped fleet is an outage in progress,
# and that is the fact the subject line and the banner are keyed off.
[ "$RC_STOPPED" -eq 12 ] \
    && pass "reason channel: the real 503 refusal exits 12 (FLEET DOWN) and leaves a record" \
    || fail "503 refusal exited $RC_STOPPED, want 12"
BODY_503="$(SRC="$WORK" GIT=/usr/bin/git ATTEMPT_N=2 red_alert_body 12 0 2026-08-07 5400 "$REC_DIR/stopped")"
echo "--- ALERT TEXT: exit 12, orchestration stopped (the 2026-08-07 failure) ---"
echo "$BODY_503"
echo "--- end ---"

# ARM ONE: the right story appears, and it is the deploy script's own sentence.
case "$BODY_503" in
    *"orchestration is STOPPED"*) pass "alert(503): the description is the deploy script's OWN sentence, carried not re-derived" ;;
    *) fail "alert(503) does not name orchestration" ;;
esac
case "$BODY_503" in
    *"WHAT THIS ATTEMPT CHANGED: NOTHING"*) pass "alert(503): states what the run changed — NOTHING — as a measured field, not an inference from the code" ;;
    *) fail "alert(503) does not state what the run changed" ;;
esac
case "$BODY_503" in
    *"pogo server start"*) pass "alert(503): carries the one-command remedy, verbatim from the site that knew it" ;;
    *) fail "alert(503) lost the remedy" ;;
esac

# ARM TWO: the wrong stories are GONE. Each of these is a line the 03:00 mail
# actually carried, and each was false.
for wrong in "post-restart verification failed" \
             "was installed and started" \
             "binary on disk is" \
             "roll the install back"; do
    case "$BODY_503" in
        *"$wrong"*) fail "alert(503) still claims: $wrong" ;;
        *) pass "alert(503) no longer claims '$wrong' — nothing was installed" ;;
    esac
done

# The other three dispositions: same code, same runner, different mail.
for d in bootstrap down error:500; do
    key="${d//:/_}"
    # `down` gets no body on purpose: 000 means nothing answered, and there is
    # no body for the harness to invent either.
    case "$d" in
        bootstrap) dbody='404 page not found' ;;
        error:500) dbody='{"error":"internal error"}' ;;
        *)         dbody='' ;;
    esac
    make_record "$d" "$REC_DIR/$key" "$dbody"
    eval "BODY_$key=\"\$(SRC=\"\$WORK\" GIT=/usr/bin/git ATTEMPT_N=0 red_alert_body 6 0 2026-08-07 5400 \"\$REC_DIR/\$key\")\""
    echo "--- ALERT TEXT: exit 6, disposition $d ---"
    eval "echo \"\$BODY_$key\""
    echo "--- end ---"
done
# THREE failures now share exit 6 — the confirmed `stopped` outage left for 12
# (mg-6d2f) — and the property is unchanged and is the point: four DIFFERENT
# mails, or the code is still the channel and this whole change bought nothing.
# The three that share a code are the sharper half of the claim.
[ "$BODY_503" != "$BODY_bootstrap" ] && [ "$BODY_503" != "$BODY_down" ] \
    && [ "$BODY_bootstrap" != "$BODY_down" ] && [ "$BODY_down" != "$BODY_error_500" ] \
    && [ "$BODY_bootstrap" != "$BODY_error_500" ] \
    && pass "alert: the four drain refusals produce four DIFFERENT mails — and the three sharing exit 6 are told apart" \
    || fail "two drain refusals produce identical alert bodies"
case "$BODY_bootstrap" in
    *"predates the /agents/drain endpoint"*"--skip-drain"*) pass "alert(bootstrap): names the chicken-and-egg AND its remedy" ;;
    *) fail "alert(bootstrap) is missing its own story" ;;
esac
case "$BODY_down" in
    *"not answering"*) pass "alert(down): says pogod did not answer at all" ;;
    *) fail "alert(down) is missing its own story" ;;
esac
case "$BODY_error_500" in
    *"unexpected HTTP 500"*) pass "alert(error:500): carries the status — all that is known, stated as all that is known" ;;
    *) fail "alert(error:500) is missing its own story" ;;
esac
# ...and none of them borrows another's.
for pair in "bootstrap:orchestration is STOPPED" "down:predates the /agents/drain" \
            "error_500:orchestration is STOPPED" "error_500:predates the /agents/drain"; do
    eval "b=\"\$BODY_${pair%%:*}\""
    case "$b" in *"${pair#*:}"*) fail "alert(${pair%%:*}) also tells ${pair#*:}'s story" ;; esac
done
pass "alert: no drain-refusal mail tells another refusal's story"

# THE FALLBACK IS REAL, and it must be: a record can be absent (an exit before
# cmd_redeploy arms one), unwritable, or left by an older deploy script. When it
# is, the alert falls back to describe_exit rather than going quiet.
BODY_NOREC="$(SRC="$WORK" GIT=/usr/bin/git ATTEMPT_N=0 red_alert_body 6 0 2026-08-07 5400 "$REC_DIR/does-not-exist")"
case "$BODY_NOREC" in
    *"drain precondition refused"*) pass "alert: with NO record, falls back to describe_exit and still says where the run stopped" ;;
    *) fail "alert without a record lost its description: $BODY_NOREC" ;;
esac
case "$BODY_NOREC" in
    *"WHAT THIS ATTEMPT CHANGED"*) fail "alert without a record asserts what changed — nothing measured that" ;;
    *) pass "alert: with no record it does NOT assert what the run changed — an unmeasured claim is what this ticket removes" ;;
esac
case "$BODY_NOREC" in
    *"was installed and started"*) fail "the exit-6 FALLBACK still asserts an install" ;;
    *) pass "alert: even the fallback stops asserting an install for exit 6 (it exits before do_build on every path)" ;;
esac

# --- the BANNER, and that it is FIRST (mg-6d2f) ----------------------------
# The subject is what a skim-reader gets; the banner is what the reader who
# OPENS the mail gets, and a mail client shows the first line in its preview.
# Both arms, and the position too: burying "the fleet is stopped" under the
# attempt/drain/elapsed bookkeeping is how the 08-07 alert managed to be sent,
# delivered, and still cost 10h39m — so "the sentence is present somewhere" is
# not the property. It has to be line one.
#
# This is assertable at all only because the banner lives inside red_alert_body
# rather than inline at the callsite. While it was inline the only way to see it
# was to fail a real nightly deploy — which is exactly the reason red_alert_body
# was made a function in the first place (mg-0155).
BAN12="$(SRC="$WORK" GIT=/usr/bin/git ATTEMPT_N=0 red_alert_body 12 0 2026-08-07 5400 "$REC_DIR/does-not-exist")"
BAN7="$(SRC="$WORK" GIT=/usr/bin/git ATTEMPT_N=0 red_alert_body 7 0 2026-08-07 5400 "$REC_DIR/does-not-exist")"
case "$(printf '%s\n' "$BAN12" | head -n 1)" in
    *"THE FLEET IS NOT DISPATCHING RIGHT NOW"*)
        pass "red_alert_body(12): the banner is the FIRST line of the mail — what a preview pane shows" ;;
    *) fail "red_alert_body(12): line one is not the banner: $(printf '%s\n' "$BAN12" | head -n 1)" ;;
esac
case "$BAN12" in
    *"outage, not a missed deploy"*) pass "red_alert_body(12): the banner names the distinction that was missing on 08-07" ;;
    *) fail "red_alert_body(12): the banner does not distinguish an outage from a missed deploy" ;;
esac
case "$BAN7" in
    *"THE FLEET IS NOT DISPATCHING"*) fail "red_alert_body(7): banners a stalled drain as an outage — the old pogod is up and dispatching, and a banner on every failure is no banner" ;;
    *) pass "red_alert_body(7): NO banner — the fleet is still dispatching, so the banner is not spent here" ;;
esac
# describe_exit 6 itself, since the stamp-retry log line quotes it directly.
case "$(describe_exit 6)" in
    *"post-restart"*) fail "describe_exit 6 still says 'post-restart verification failed'" ;;
    *"BEFORE the build"*) pass "describe_exit 6 says what is true of all four refusals: it stopped before the build" ;;
    *) fail "describe_exit 6: $(describe_exit 6)" ;;
esac
[ "$(remedy_for_exit 6)" != "$(remedy_for_exit 8)" ] \
    && pass "remedy_for_exit: 6 and 8 no longer share a paragraph (6 is pre-build, 8 is post-install)" \
    || fail "remedy_for_exit 6 and 8 are still identical"
case "$(remedy_for_exit 8)" in
    *"installed and started"*) pass "remedy_for_exit 8 KEEPS the install paragraph — there the install really did happen" ;;
    *) fail "remedy_for_exit 8 lost the paragraph that is true of it" ;;
esac

# what_the_run_changed: the two facts that come apart (see the mg-0155
# enumeration). A restart-only deploy installs NOTHING and still bounces pogod,
# so deriving "was the daemon touched?" from "was anything installed?" tells a
# failed kickstart that the running pogod is untouched. It was down.
mkrec() { printf 'exit=%s\nstage=%s\ninstalled=%s\nreason=%s\n--- verbatim ---\n%s\n' "$1" "$2" "$3" "$4" "$5" > "$6"; }
mkrec 5 restart no "launchctl kickstart failed" "launchctl kickstart failed" "$REC_DIR/restartonly"
CH="$(what_the_run_changed "$REC_DIR/restartonly")"
case "$CH" in
    *"pogod WAS bounced"*) pass "what_the_run_changed: a restart-only deploy that fails AT the kickstart reports the bounce" ;;
    *) fail "restart-only kickstart failure: $CH" ;;
esac
case "$CH" in
    *"exactly as they were"*) fail "what_the_run_changed calls a bounced daemon untouched: $CH" ;;
    *) pass "what_the_run_changed: does NOT call a bounced daemon untouched — installed=no does not mean nothing happened" ;;
esac
mkrec 4 build partial "go install failed" "go install failed" "$REC_DIR/partial"
case "$(what_the_run_changed "$REC_DIR/partial")" in
    *"IN PROGRESS"*"revision"*) pass "what_the_run_changed: a failure inside go install reports a possibly-partial GOBIN, and says how to check" ;;
    *) fail "partial install: $(what_the_run_changed "$REC_DIR/partial")" ;;
esac
mkrec 8 verify yes "new pogod did not report main revision within 60s" "..." "$REC_DIR/verifyfail"
case "$(what_the_run_changed "$REC_DIR/verifyfail")" in
    *"install COMPLETED and pogod WAS bounced"*) pass "what_the_run_changed: exit 8 reports BOTH — installed and bounced, which is why its remedy may talk about a binary on disk" ;;
    *) fail "verify failure: $(what_the_run_changed "$REC_DIR/verifyfail")" ;;
esac
[ -z "$(what_the_run_changed "$REC_DIR/nothing-here")" ] \
    && pass "what_the_run_changed: says nothing when nothing measured it (a missing record is not evidence of an untouched box)" \
    || fail "what_the_run_changed invented a claim from a missing file"

# A malformed record must not take the alerter down with it. An alerting path
# that fails on bad input goes quiet exactly when something has gone unusually
# wrong.
printf 'not a record at all\n\x00\n' > "$REC_DIR/junk" 2>/dev/null
[ -n "$(SRC="$WORK" GIT=/usr/bin/git ATTEMPT_N=0 red_alert_body 6 0 2026-08-07 5400 "$REC_DIR/junk")" ] \
    && pass "alert: a malformed record degrades to the fallback instead of producing an empty mail" \
    || fail "a malformed record produced an empty alert body"

# ---------------------------------------------------------------------------
# The enumeration is load-bearing (mg-0155)
# ---------------------------------------------------------------------------
# docs/deploy-exit-paths.md is a deliverable of the ticket, and a table nothing
# checks is a table that rots into a fifth generation of the same defect. So:
# every code the deploy script can exit with must appear in it, and every code in
# it must be describable. This is what makes "enumerate the whole class" a
# property of the repo rather than one polecat's good afternoon.
EXITDOC="$HERE/../docs/deploy-exit-paths.md"
[ -f "$EXITDOC" ] && pass "the exit-path enumeration exists at docs/deploy-exit-paths.md" \
    || fail "docs/deploy-exit-paths.md is missing"
if [ -f "$EXITDOC" ]; then
    CODES="$(grep -oE '^[^#]*\bexit [0-9]+' "$HERE/pogo-self-deploy" | grep -oE '[0-9]+$' | sort -un)"
    missing=""
    for c in $CODES; do
        grep -qE "^\| $c \|" "$EXITDOC" || missing="$missing $c"
    done
    [ -z "$missing" ] \
        && pass "every exit code pogo-self-deploy can produce has a row in the enumeration ($(echo "$CODES" | tr '\n' ' '))" \
        || fail "exit codes missing from docs/deploy-exit-paths.md:$missing"
    undescribed=""
    for c in $CODES; do
        [ "$(describe_exit "$c")" = "unclassified failure" ] && undescribed="$undescribed $c"
    done
    [ -z "$undescribed" ] \
        && pass "and describe_exit has a case for each of them — the fallback covers the whole range" \
        || fail "describe_exit falls through for:$undescribed"
    grep -q 'installed' "$EXITDOC" && grep -q 'Bounced' "$EXITDOC" \
        && pass "the enumeration answers BOTH questions per path: was anything installed, and was pogod bounced" \
        || fail "the enumeration is missing the installed/bounced columns"
fi
# ---------------------------------------------------------------------------
# register_alert_recipients — the alert path can be DELIVERED, not just run
# ---------------------------------------------------------------------------
# THE DEFECT (mg-7dc1). Until mg-d639, `mg mail send` filed mail for any name at
# all, so this job never had to provision the two names it mails — an ALERT_TO
# nobody had ever written to still reported Delivered. The ALERT_TO comment in
# the runner said exactly that, as a fact it was relying on. mg-d639 made an
# unknown recipient a refusal, so on a fresh install that copy of the alert now
# fails where it used to "succeed".
#
# resolve_mg answers "can the alert RUN". These answer "can it be DELIVERED",
# which since mg-d639 is a different question.
MG_LOG="$WORK/mg-register.log"
MG="$WORK/fake-mg"
cat > "$MG" <<'FAKEMG'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MG_LOG_TARGET"
exit 0
FAKEMG
chmod +x "$MG"

: > "$MG_LOG"
ALERT_TO=mayor MG_LOG_TARGET="$MG_LOG" register_alert_recipients
grep -q '^mail register mayor$' "$MG_LOG" \
    && pass "register_alert_recipients registers ALERT_TO before it is needed" || fail "ALERT_TO not registered: $(cat "$MG_LOG")"
grep -q '^mail register human$' "$MG_LOG" \
    && pass "register_alert_recipients registers 'human' too — alert() mails it independently, so it needs its own box" || fail "'human' not registered: $(cat "$MG_LOG")"

# A NON-DEFAULT ALERT_TO is the case this is actually for: `mayor` and `human`
# already exist on the machine that has been running the fleet. An install that
# sets POGO_DEPLOY_ALERT_TO to its own PM is the one where the box has never
# been written to and the alert is refused.
: > "$MG_LOG"
ALERT_TO=pm-elsewhere MG_LOG_TARGET="$MG_LOG" register_alert_recipients
grep -q '^mail register pm-elsewhere$' "$MG_LOG" \
    && pass "register_alert_recipients follows POGO_DEPLOY_ALERT_TO (the install where the box does NOT already exist)" || fail "non-default ALERT_TO not registered: $(cat "$MG_LOG")"

# THE POSITIVE CONTROL, and the point of the whole ticket. `mg mail register` and
# `mg mail send --create` both make the mailbox exist, and the difference between
# them is the entire value of mg-d639: --create on a send delivers to a name
# whether or not anyone meant it, so POGO_DEPLOY_ALERT_TO=mayro would quietly
# mint `mayro` and report success at 03:00 on the one night it mattered. That is
# the phantom-mailbox behaviour mg-d639 removed, restored under a new name.
#
# This asserts the runner never reaches for it. Without this line the two fixes
# are indistinguishable — the tests above pass just as happily on the wrong one.
grep -v '^[[:space:]]*#' "$RUNNER" | grep -q -- '--create' \
    && fail "the runner passes --create on a mail send — a typo'd POGO_DEPLOY_ALERT_TO would mint a phantom mailbox and report Delivered, which is what mg-d639 exists to stop" \
    || pass "the runner provisions with 'mail register', never 'mail send --create' (a typo'd recipient still refuses loudly)"

# NEVER FATAL. This runs before any deploy work, and its failure says nothing
# about whether the deploy can proceed — the sends are attempted regardless and
# alert() reports each one that fails. An old mg with no `mail register` verb
# exits non-zero here, and that build still files mail for an unknown name, so
# aborting on it would stop a nightly that was never at risk.
MG="$WORK/failing-mg"
printf '#!/usr/bin/env bash\nexit 1\n' > "$MG"
chmod +x "$MG"
ALERT_TO=mayor register_alert_recipients >/dev/null 2>&1 \
    && pass "register_alert_recipients returns 0 when mg cannot register — a provisioning hiccup must not stop the nightly" || fail "a failed registration aborted the run"
ALERT_TO=mayor register_alert_recipients 2>&1 | grep -q "could not register mailbox 'mayor'" \
    && pass "a failed registration is REPORTED (it is a degraded alert path, not a silent one)" || fail "a failed registration said nothing"

# Ordering: registration must come after resolve_mg (it invokes "$MG") and
# before the first thing that can abort with an alert. resolve_git is that first
# thing — its failure path calls alert() — so a registration placed after it
# would be too late for exactly the alert it exists to deliver.
awk '/^    resolve_mg/{mg=NR} /^    register_alert_recipients/{reg=NR} /^    resolve_git/{git=NR} END{exit !(mg && reg && git && mg < reg && reg < git)}' "$RUNNER" \
    && pass "register_alert_recipients runs after resolve_mg and before the first alerting abort" \
    || fail "registration is not ordered between resolve_mg and resolve_git"

# ---------------------------------------------------------------------------
# alert()'s return value — the question mg-7dc1 recorded as UNEXAMINED
# ---------------------------------------------------------------------------
# mg-7dc1 left open "whether a nonzero return from the alert path cascades into
# the deploy's own exit handling". It does not, and this pins the answer so it
# stays pinned:
#
#   - the runner sets `set -u` and NOT `set -e`, so a non-zero return never
#     aborts anything by itself;
#   - every alert() callsite is followed by an explicit `exit`, and none of them
#     reads alert's status.
#
# That makes a refused alert cost the operator nothing in exit code — which is
# the good news and the bad news, since it also means a run whose alert was
# never delivered still exits with the code it would have had. The delivery
# guarantee therefore has to come from the recipients existing, which is what
# register_alert_recipients is for.
grep -qE '^set -e|^set -eu|^set -ue' "$RUNNER" \
    && fail "the runner enables set -e; alert()'s rc=1 would now abort its caller mid-path" \
    || pass "the runner does not use set -e — a failed alert send cannot abort the path that called it"
# The callsites are MULTI-LINE — subject, a backslash, then a heredoc-shaped
# body running to a dozen lines — so a grep of the lines matching `alert "` reads
# only the first line of each and cannot see a `|| rc=1` on the last. That grep
# passes on this file and passes just as happily on a file where the cascade is
# real, which is the kind of control this ticket is about. So walk each callsite
# to the END of its command, tracking double-quote parity across the
# continuations, and check the terminating line — plus the opening line for the
# `if alert`/`! alert` spellings, where the status is read at the front instead.
ALERT_STATUS_PROBE='
function quotes(s,   n,i,c) { n=0; for(i=1;i<=length(s);i++){c=substr(s,i,1); if(c=="\"") n++} return n }
{ lines[NR]=$0 } END {
  bad=0
  for (i=1;i<=NR;i++) {
    L=lines[i]
    if (L !~ /(^|[^_[:alnum:]])alert "/) continue
    if (L ~ /^[[:space:]]*#/) continue
    if (L ~ /(if|while|until|!)[[:space:]]+alert "/) { print "line " i ": " L; bad++; continue }
    open = quotes(L) % 2
    j = i
    while ((open || lines[j] ~ /\\$/) && j < NR) { j++; open = (open + quotes(lines[j])) % 2 }
    if (lines[j] ~ /\|\||&&|\$\?/) { print "line " j ": " lines[j]; bad++ }
  }
  exit (bad>0)
}'
awk "$ALERT_STATUS_PROBE" "$RUNNER" \
    && pass "no alert() callsite reads its return value — a refused send cannot cascade into the deploy's exit handling (mg-7dc1's open question, answered)" \
    || fail "an alert() callsite branches on its return value; a refused mail would change control flow"

# The probe's own positive control. It is a hand-rolled parse of a multi-line
# shell command, and a parse that silently stopped matching would report this
# property as held on every future edit. Feed it a copy of the runner with the
# cascade INSERTED and require it to object.
# The injection lands on the TERMINATING line of the first alert callsite's
# multi-line command, which is the whole point: that is precisely the position a
# first-line grep cannot see.
#
# It is located STRUCTURALLY, by the same parity walk, rather than by matching a
# line of the alert's prose. An earlier version anchored on a specific body line
# and stopped applying the first time that alert was reworded — the fixture went
# on "passing" against an unmodified file, which is the exact failure mode this
# control exists to catch, reproduced inside the control itself. The guard below
# is what caught it; it stays.
CASCADE_COPY="$WORK/runner-with-cascade.sh"
awk '
function quotes(s,   n,i,c) { n=0; for(i=1;i<=length(s);i++){c=substr(s,i,1); if(c=="\"") n++} return n }
{ lines[NR]=$0 } END {
  hit=0
  for (i=1;i<=NR && !hit;i++) {
    L=lines[i]
    if (L !~ /(^|[^_[:alnum:]])alert "/ || L ~ /^[[:space:]]*#/) continue
    open = quotes(L) % 2
    j = i
    while ((open || lines[j] ~ /\\$/) && j < NR) { j++; open = (open + quotes(lines[j])) % 2 }
    lines[j] = lines[j] " || rc=99"
    hit=1
  }
  for (i=1;i<=NR;i++) print lines[i]
}' "$RUNNER" > "$CASCADE_COPY"
grep -q 'rc=99' "$CASCADE_COPY" \
    && pass "the cascade fixture applies (the positive control is testing a genuinely modified file)" \
    || fail "the cascade fixture did not apply — the probe's positive control is testing an unmodified file"
awk "$ALERT_STATUS_PROBE" "$CASCADE_COPY" >/dev/null \
    && fail "the alert-status probe reported a runner WITH a cascade as clean — it is not measuring anything" \
    || pass "the alert-status probe objects to an injected cascade (it measures the property, not the file)"

# ---------------------------------------------------------------------------
# THE BOUNDS ON A CALL THAT NEVER RETURNS (mg-56ac)
# ---------------------------------------------------------------------------
# Every test below drives a HANG, not a failure. That distinction is the ticket:
# on 2026-08-08 this runner fired on time, blocked in a git fetch for 31h39m,
# and produced no exit code, no alert and no stamp — while the same call failing
# three nights earlier produced four log lines and two mails within a second. A
# suite that only ever exercises the failing path has never tested the shape
# that actually cost the fleet 33 hours.
#
# So the fixtures here BLOCK. `hanggit` sleeps forever on any command but
# --version, which is exactly what the 08-08 fetch did as far as this runner
# could tell.

cat > "$FAKEBIN/hanggit" <<'EOF'
#!/bin/sh
[ "$1" = "--version" ] && { echo "git version 9.9.9 (hangs on everything else)"; exit 0; }
sleep 600
EOF
# A hang with a CHILD, for the kill_tree case: `git fetch` execs an ssh or
# git-remote-https child and it is the child that holds the half-open socket, so
# a killer that signals only the named process can leave the hang in place.
cat > "$FAKEBIN/hanggit-with-child" <<'EOF'
#!/bin/sh
[ "$1" = "--version" ] && { echo "git version 9.9.9"; exit 0; }
sleep 600 &
echo "$!" > "$HANG_CHILD_PIDFILE"
wait
EOF
chmod +x "$FAKEBIN/hanggit" "$FAKEBIN/hanggit-with-child"

# --- run_bounded: it must end a call that never returns ---------------------
BOUNDED_T0=$(date +%s)
run_bounded 2 sleep 60
BOUNDED_RC_SEEN=$?
BOUNDED_ELAPSED=$(( $(date +%s) - BOUNDED_T0 ))
[ "$BOUNDED_ELAPSED" -lt 15 ] \
    && pass "run_bounded ENDS a call that never returns (${BOUNDED_ELAPSED}s, not 60)" \
    || fail "run_bounded waited ${BOUNDED_ELAPSED}s on a 2s bound — the bound is not bounding"
$BOUNDED_TIMED_OUT \
    && pass "run_bounded reports the timeout as a timeout, not as an ordinary non-zero status" \
    || fail "BOUNDED_TIMED_OUT is false after a bound expired"
[ "$BOUNDED_RC_SEEN" -eq 124 ] \
    && pass "run_bounded returns 124 on expiry (timeout's convention, recognisable without this file)" \
    || fail "run_bounded returned $BOUNDED_RC_SEEN on expiry, want 124"

# The negative control, and it is the one that decides whether the two arms are
# distinguishable at all: a command that returns must not be reported as a
# timeout, and must keep its OWN status.
run_bounded 30 sh -c 'exit 3'
BOUNDED_RC_SEEN=$?
[ "$BOUNDED_RC_SEEN" -eq 3 ] && ! $BOUNDED_TIMED_OUT \
    && pass "run_bounded passes a returning command's own status through untouched (rc=3, not timed out)" \
    || fail "run_bounded mangled a returning command (rc=$BOUNDED_RC_SEEN timed_out=$BOUNDED_TIMED_OUT)"

run_bounded 0 sh -c 'exit 5'
[ "$?" -eq 5 ] && ! $BOUNDED_TIMED_OUT \
    && pass "run_bounded with a zero bound runs unbounded (the controlled-observation escape hatch)" \
    || fail "run_bounded did not honour a zero bound"

# --- kill_tree: leaves first ------------------------------------------------
# The property under test is NOT that the named process dies; it is that its
# grandchild does. A `git fetch` whose ssh child survives the kill holds the
# socket, and the hang continues under a killer that reported success.
HANG_CHILD_PIDFILE="$WORK/hangchild.pid"
export HANG_CHILD_PIDFILE
rm -f "$HANG_CHILD_PIDFILE"
"$FAKEBIN/hanggit-with-child" fetch >/dev/null 2>&1 &
TREE_PARENT=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s "$HANG_CHILD_PIDFILE" ] && break
    sleep 0.2
done
TREE_CHILD="$(cat "$HANG_CHILD_PIDFILE" 2>/dev/null)"
if [ -n "$TREE_CHILD" ] && kill -0 "$TREE_CHILD" 2>/dev/null; then
    pass "kill_tree premise: the fixture really does hold a grandchild (pid $TREE_CHILD)"
    kill_tree "$TREE_PARENT" TERM
    wait "$TREE_PARENT" 2>/dev/null
    sleep 0.5
    kill -0 "$TREE_CHILD" 2>/dev/null \
        && fail "kill_tree left the grandchild alive — the process holding the socket survives the kill" \
        || pass "kill_tree reaps the GRANDCHILD, not just the named process"
else
    fail "kill_tree fixture did not produce a live grandchild — the test is not measuring anything"
fi

# --- self_pid: the watchdog must be able to exclude itself ------------------
# If this idiom breaks, the deadline watchdog kills itself partway through its
# own kill and never reaches the SIGKILL — a watchdog that half-works, which is
# worse than none because it looks armed.
SELF_OUTER=$$
SELF_INNER="$( ( self_pid ) )"
[ -n "$SELF_INNER" ] && [ "$SELF_INNER" != "$SELF_OUTER" ] \
    && pass "self_pid returns the SUBSHELL's own pid, not the parent's (bash 3.2 has no BASHPID)" \
    || fail "self_pid returned '$SELF_INNER' in a subshell of $SELF_OUTER — the watchdog cannot exclude itself"

# --- git_step: a hung step is a TIMEOUT, and timeouts are retryable ---------
SAVED_GIT_TIMEOUT="$GIT_TIMEOUT"
GIT_TIMEOUT=2
GIT_STEP_T0=$(date +%s)
git_step "$FAKEBIN/hanggit" fetch --quiet origin >/dev/null 2>&1
GIT_STEP_RC=$?
GIT_STEP_ELAPSED=$(( $(date +%s) - GIT_STEP_T0 ))
[ "$GIT_STEP_ELAPSED" -lt 15 ] \
    && pass "git_step BOUNDS a fetch that never returns (${GIT_STEP_ELAPSED}s) — this is the 2026-08-08 call, bounded" \
    || fail "git_step waited ${GIT_STEP_ELAPSED}s on a hung fetch"
$GIT_STEP_TIMED_OUT \
    && pass "git_step records that the step was KILLED rather than that it failed" \
    || fail "GIT_STEP_TIMED_OUT is false after a killed step"
case "$SYNC_DETAIL" in
    *"did not return within"*) pass "the killed step's SYNC_DETAIL says it did not return — the alert prints a fact, not a guess" ;;
    *) fail "SYNC_DETAIL after a killed step does not describe the kill: $SYNC_DETAIL" ;;
esac
: "$GIT_STEP_RC"

# classify_transport must take the kill as the classification and NOT consult a
# probe. The probe measures a later instant — the link may well be up by then —
# and calling a hang `remote` sends the reader to ssh keys.
SYNC_CLASS=""
classify_transport "https://github.com/drellem2/pogo.git" >/dev/null 2>&1
[ "$SYNC_CLASS" = "timeout" ] \
    && pass "a KILLED step classifies as timeout, from this run's own observation rather than a later probe" \
    || fail "a killed step classified as '$SYNC_CLASS'"
sync_class_retryable timeout \
    && pass "timeout is RETRYABLE — a call that never returned established nothing at all, not even reachability" \
    || fail "timeout was treated as settling the night"
describe_sync_class timeout | grep -q 'TIMEOUT' \
    && pass "describe_sync_class names the timeout class" || fail "describe_sync_class has no timeout row"
remedy_for_sync_class timeout | grep -q '2026-08-08' \
    && pass "the timeout remedy names the incident it was measured from" || fail "the timeout remedy is generic"
# A step that RETURNS must clear the flag a previous step set. Left stale, the
# classifier would report the next failure as a timeout it never observed —
# which is a detector asserting something it did not measure, the defect this
# whole change is about, rebuilt inside its own fix.
GIT_TIMEOUT=2
git_step "$FAKEBIN/hanggit" fetch --quiet origin >/dev/null 2>&1   # sets the flag
git_step "$FAKEBIN/workinggit" fetch --quiet origin >/dev/null 2>&1 # must clear it
$GIT_STEP_TIMED_OUT \
    && fail "a returning git step left GIT_STEP_TIMED_OUT set by the previous one" \
    || pass "a git step that RETURNS clears the timeout flag a previous step set"
GIT_TIMEOUT="$SAVED_GIT_TIMEOUT"

# The negative control for the classifier: with no kill, the probe still decides.
GIT_STEP_TIMED_OUT=false
SYNC_CLASS=""
classify_transport "" >/dev/null 2>&1
[ "$SYNC_CLASS" = "unclassified" ] \
    && pass "with no kill, classify_transport still reaches its ordinary path (a probe-less URL is unclassified)" \
    || fail "classify_transport '$SYNC_CLASS' — the timeout short-circuit swallowed the ordinary path"
GIT_TIMEOUT="$SAVED_GIT_TIMEOUT"

# --- EVERY git call is bounded, not just the ones that look like steps ------
# THIS TEST EXISTS BECAUSE THE FIX FAILED IT. The first version bounded the four
# calls that go through git_step — clone, fetch, checkout, ff-merge — and left
# the queries bare: `remote get-url origin`, `status --porcelain`, `rev-parse
# --short HEAD`. Two of those run on the failure path IMMEDIATELY AFTER a fetch
# that has just been killed for hanging, against the same remote. The suite
# caught it by hanging, with the runner sitting in `remote get-url origin` having
# already been killed once for hanging.
#
# The reasoning that left them bare — "it only reads .git/config, it is local" —
# is the same reasoning that leaves any call unbounded, so the guard is
# structural rather than a list of the calls we happened to think of.
GIT_BARE="$(grep -n '"\$GIT"' "$RUNNER" \
    | grep -v 'git_step "\$GIT"' \
    | grep -v 'git_q "\$GIT"' \
    | grep -v 'cands+=("\$GIT")' || true)"
[ -z "$GIT_BARE" ] \
    && pass "every git invocation goes through a BOUNDED helper — no bare \"\$GIT\" call survives" \
    || fail "unbounded git call(s): $GIT_BARE"
# The probe's positive control: it must object to a bare call, or it is a grep
# that passes on every file.
printf 'x="$("$GIT" -C /tmp status)"\n' > "$WORK/bare-git-fixture.sh"
grep -n '"\$GIT"' "$WORK/bare-git-fixture.sh" | grep -v 'git_step "\$GIT"' | grep -v 'git_q "\$GIT"' | grep -q . \
    && pass "the bare-git probe objects to a bare call (it measures the property, not the file)" \
    || fail "the bare-git probe cannot see a bare call — it is not measuring anything"

# And the query helper really is bounded: a hanging `remote get-url` must not
# outlive the bound, because that is the exact call the failure path makes.
SAVED_GIT_TIMEOUT2="$GIT_TIMEOUT"
GIT_TIMEOUT=2
GITQ_T0=$(date +%s)
GITQ_OUT="$(git_q "$FAKEBIN/hanggit" -C /tmp remote get-url origin 2>/dev/null)"
GITQ_ELAPSED=$(( $(date +%s) - GITQ_T0 ))
[ "$GITQ_ELAPSED" -lt 15 ] \
    && pass "git_q bounds a hanging query (${GITQ_ELAPSED}s) — the classifier's own git call cannot re-hang the run it is classifying" \
    || fail "git_q waited ${GITQ_ELAPSED}s"
[ -z "$GITQ_OUT" ] \
    && pass "a timed-out query yields the empty string, which every callsite already treats as absent" \
    || fail "git_q returned '$GITQ_OUT' after a timeout"
GIT_TIMEOUT="$SAVED_GIT_TIMEOUT2"

# --- run_deadline: derived from the window, floored ------------------------
# 03:00, production window: 3h to the window's end plus 1800s of slack.
[ "$(run_deadline 6 1800 7200 1200 3 0 0)" = "12600" ] \
    && pass "run_deadline: a 03:00 fire is bounded at 3h30m — window end plus slack, not a constant" \
    || fail "run_deadline at 03:00 = $(run_deadline 6 1800 7200 1200 3 0 0)"
# 05:30 — the derived figure is below the floor, so the floor wins. A run may
# always have a full drain plus the reserve, wherever in the window it starts.
[ "$(run_deadline 6 1800 7200 1200 5 30 0)" = "10200" ] \
    && pass "run_deadline floors at MAX_DRAIN+RESERVE+slack — a late fire is not killed mid-drain" \
    || fail "run_deadline at 05:30 = $(run_deadline 6 1800 7200 1200 5 30 0)"
# The out-of-window controlled run, where the derivation is deeply negative.
[ "$(run_deadline 6 1800 7200 1200 14 0 0)" = "10200" ] \
    && pass "run_deadline: a 14:00 out-of-window run gets the floor, not an immediate kill" \
    || fail "run_deadline at 14:00 = $(run_deadline 6 1800 7200 1200 14 0 0)"
# And it is always long enough to cover a full drain — the invariant that keeps
# this from becoming a bound that kills healthy deploys.
DEADLINE_COVERS=true
for H in 2 3 4 5; do
    [ "$(run_deadline 6 1800 7200 1200 "$H" 0 0)" -gt $(( 7200 + 1200 )) ] || DEADLINE_COVERS=false
done
$DEADLINE_COVERS \
    && pass "at every hour of the window the deadline exceeds a full drain plus the reserve" \
    || fail "some fire hour gets a deadline shorter than a legitimate run"

# ---------------------------------------------------------------------------
# THE ACCEPTANCE: the whole-run deadline goes RED against a constructed hang
# ---------------------------------------------------------------------------
# "A check that has never produced a red is a check of unknown polarity." So
# this drives a real `pogo-deploy.sh` process against the 2026-08-08 condition
# reproduced exactly — an unbounded git call that never returns
# (POGO_DEPLOY_GIT_TIMEOUT=0 switches the per-step bound OFF on purpose, so what
# is being tested is the whole-run deadline and nothing else) — and requires
# four things the 08-08 run did not produce: a loud log line, a mail, a dead
# process, and a TERMINAL LINE naming an exit code.
#
# Everything the run touches is redirected into $WORK: HOME, GOBIN/GOPATH (which
# is how resolve_mg finds a fake macguffin instead of the real one), the lock,
# the stamp and the log. Nothing here mails a real mailbox or reads the real
# deploy log.
E2E="$WORK/e2e"
mkdir -p "$E2E/go/bin" "$E2E/src/.git" "$E2E/Library/Logs/pogo"
cat > "$E2E/go/bin/mg" <<EOF
#!/bin/sh
[ "\$1" = "--help" ] && { echo "macguffin — fake"; exit 0; }
[ "\$1" = "mail" ] && { echo "\$*" >> "$E2E/mail.log"; exit 0; }
exit 0
EOF
cat > "$E2E/go/bin/pogo" <<EOF
#!/bin/sh
echo "\$*" >> "$E2E/pogo.log"
exit 0
EOF
chmod +x "$E2E/go/bin/mg" "$E2E/go/bin/pogo"
printf 'export GH_TOKEN=fake-token-value-not-a-secret\n' > "$E2E/zshenv"
# Pre-warm `go env` against the redirected HOME. resolve_mg shells out to it
# twice, and its FIRST call under a never-before-used HOME takes ~20s while it
# builds its caches — which is a property of this fixture, not of the runner, and
# it is long enough to trip the short deadlines below. Left un-warmed, the
# negative control fails for a reason that has nothing to do with what it is
# testing. (On the real box HOME is warm and the 2026-08-08 log shows mg
# resolving in the same second as the start line.)
#
# GOMODCACHE is pinned to the real one because a module cache created under
# $WORK is written read-only, and the suite's `rm -rf "$WORK"` then cannot
# remove it — noise on exit that looks like a failing teardown.
E2E_GOMODCACHE="$(go env GOMODCACHE 2>/dev/null)"
export E2E_GOMODCACHE
HOME="$E2E" GOBIN="$E2E/go/bin" GOPATH="$E2E/go" GOMODCACHE="$E2E_GOMODCACHE" \
    go env GOBIN >/dev/null 2>&1 || true

run_e2e() {   # run_e2e LABEL GIT_BIN GIT_TIMEOUT RUN_DEADLINE -> log on stdout
    rm -rf "$E2E/lock" "$E2E/mail.log" "$E2E/stamp"
    HOME="$E2E" \
    GOBIN="$E2E/go/bin" GOPATH="$E2E/go" GOMODCACHE="$E2E_GOMODCACHE" \
    POGO_BIN="$E2E/go/bin/pogo" \
    GIT="$2" \
    POGO_DEPLOY_SRC="$E2E/src" \
    POGO_DEPLOY_ZSHENV="$E2E/zshenv" \
    POGO_DEPLOY_LOCK_DIR="$E2E/lock" \
    POGO_DEPLOY_STAMP="$E2E/stamp" \
    POGO_DEPLOY_SKIP_WINDOW=1 \
    POGO_DEPLOY_NOW=05 \
    POGO_DEPLOY_FIRE_HOURS="3" \
    POGO_DEPLOY_GIT_TIMEOUT="$3" \
    POGO_DEPLOY_RUN_DEADLINE="$4" \
    POGO_DEPLOY_DEADLINE_GRACE=2 \
    POGO_DEPLOY_SYNC_ATTEMPTS=1 \
    POGO_DEPLOY_SYNC_VIGIL=0 \
    POGO_DEPLOY_ALERT_TO=mayor \
        bash "$RUNNER" > "$E2E/$1.log" 2>&1
    echo "$?" > "$E2E/$1.rc"
}

# RED — the hang. Unbounded git, a 5s whole-run deadline.
E2E_T0=$(date +%s)
run_e2e hang "$FAKEBIN/hanggit" 0 5
E2E_ELAPSED=$(( $(date +%s) - E2E_T0 ))
E2E_RC="$(cat "$E2E/hang.rc")"

[ "$E2E_ELAPSED" -lt 60 ] \
    && pass "ACCEPTANCE: a run wedged in an unbounded git call is ENDED (${E2E_ELAPSED}s) — on 2026-08-08 the same condition ran 113598s" \
    || fail "the wedged run was not ended: ${E2E_ELAPSED}s elapsed"
grep -q 'DEADLINE EXCEEDED' "$E2E/hang.log" \
    && pass "ACCEPTANCE: the deadline says so LOUDLY in the log" \
    || fail "no DEADLINE line in the log of a killed run"
grep -q 'KILLED: the nightly run exceeded' "$E2E/mail.log" 2>/dev/null \
    && pass "ACCEPTANCE: the deadline MAILS — the 08-08 hang sent nothing, ever" \
    || fail "the killed run sent no mail (mail.log: $(cat "$E2E/mail.log" 2>/dev/null))"
grep -q 'pogo-deploy: end (rc=' "$E2E/hang.log" \
    && pass "ACCEPTANCE: the killed run writes a TERMINAL LINE with an exit code — 'no exit code' was the whole defect" \
    || fail "the killed run wrote no terminal line: $(tail -3 "$E2E/hang.log")"
[ "$E2E_RC" -ne 0 ] \
    && pass "ACCEPTANCE: a killed run exits non-zero ($E2E_RC), so the night is not recorded as a success" \
    || fail "the killed run exited 0"
grep -q 'pogo-deploy: start' "$E2E/hang.log" \
    && pass "the killed run did start — this fixture reproduces a HANG, not a job that never fired" \
    || fail "the hang fixture never started"

# GREEN — the same runner, the same deadline, a git that RETURNS. Nothing is
# killed, nothing is mailed about a deadline, and the terminal line is still
# there. Without this arm the RED above would be satisfied by a runner that
# kills every run it starts.
run_e2e ok "$FAKEBIN/workinggit" 5 20
grep -q 'DEADLINE EXCEEDED' "$E2E/ok.log" \
    && fail "the deadline fired on a run that finished in seconds — it would kill every healthy night" \
    || pass "NEGATIVE CONTROL: a run whose git returns is NOT killed by the deadline"
grep -q 'KILLED: the nightly run exceeded' "$E2E/mail.log" 2>/dev/null \
    && fail "a healthy run mailed a deadline alert" \
    || pass "NEGATIVE CONTROL: a healthy run mails no deadline alert"
grep -q 'pogo-deploy: end (rc=' "$E2E/ok.log" \
    && pass "NEGATIVE CONTROL: the healthy run writes the same terminal line — the marker is on EVERY path, not just the killed one" \
    || fail "a healthy run wrote no terminal line: $(tail -3 "$E2E/ok.log")"

# The per-step bound, end to end: with GIT_TIMEOUT set the fetch is killed long
# before the whole-run deadline, and the night is classified rather than
# guillotined. This is the outcome the deadline exists to make unnecessary.
run_e2e step "$FAKEBIN/hanggit" 2 600
grep -q 'step exceeded 2s and was killed' "$E2E/step.log" \
    && pass "the per-step bound kills the fetch itself, well inside the whole-run deadline" \
    || fail "the per-step bound did not fire: $(tail -5 "$E2E/step.log")"
grep -q 'TIMEOUT' "$E2E/step.log" \
    && pass "and the night is CLASSIFIED (timeout), which is what makes it retryable rather than lost" \
    || fail "a killed step produced no timeout classification"
grep -q 'pogo-deploy: end (rc=' "$E2E/step.log" \
    && pass "the step-bounded run writes a terminal line too" \
    || fail "the step-bounded run wrote no terminal line"

# The skip paths get one as well. A fire that is locked out exits in
# milliseconds and is perfectly healthy — and without a terminal line of its own
# it is indistinguishable, to a witness reading the log, from a run that started
# and never came back.
mkdir -p "$E2E/lock"
HOME="$E2E" GOBIN="$E2E/go/bin" GOPATH="$E2E/go" GOMODCACHE="$E2E_GOMODCACHE" POGO_BIN="$E2E/go/bin/pogo" \
    GIT="$FAKEBIN/workinggit" POGO_DEPLOY_SRC="$E2E/src" POGO_DEPLOY_ZSHENV="$E2E/zshenv" \
    POGO_DEPLOY_LOCK_DIR="$E2E/lock" POGO_DEPLOY_STAMP="$E2E/stamp" \
    POGO_DEPLOY_SKIP_WINDOW=1 POGO_DEPLOY_NOW=05 POGO_DEPLOY_STALE_LOCK_MIN=99999 \
    bash "$RUNNER" > "$E2E/locked.log" 2>&1
grep -q 'pogo-deploy: end (rc=0' "$E2E/locked.log" \
    && pass "a fire that skips on the LOCK still writes a terminal line — a healthy skip must not read as a hang" \
    || fail "the locked-out fire wrote no terminal line: $(cat "$E2E/locked.log")"
[ -d "$E2E/lock" ] \
    && pass "and it did NOT remove the lock the running deploy holds (LOCK_HELD gates the cleanup)" \
    || fail "a locked-out fire removed another run's lock — the earlier trap arming broke mutual exclusion"
rmdir "$E2E/lock" 2>/dev/null || true

# ---------------------------------------------------------------------------
# The positive control's WIRING into the runner (mg-db96)
# ---------------------------------------------------------------------------
# The control itself is tested in scripts/net-control_test.sh, including the
# assertion the ticket exists for: that it goes RED on a box with no network.
# What is tested here is the half that lives in this file — that the runner
# finds it, that it degrades to `unknown` rather than to silence when it cannot,
# and that the verdict reaches the reader attached to a sentence about what it
# changes.

# It loads out of the repo layout. This is not incidental: the runner looks
# beside itself first (the installed layout, ~/.pogo/bin/) and only then at
# ../lib, so an in-repo run exercises the second path and the nightly exercises
# the first. A search that lost the repo path would leave every test below
# running against the stub and still going green.
load_net_control >/dev/null 2>&1 \
    && [ -n "$NET_CONTROL_LIB" ] \
    && pass "load_net_control finds the library in the repo layout ($NET_CONTROL_LIB)" \
    || fail "load_net_control could not find scripts/lib/net-control.sh from the repo layout"

# THE DEGRADATION, and it is the one that matters. A runner whose control is
# missing must not fall back to the pre-mg-db96 behaviour of interpreting a
# one-endpoint probe as if it meant something — and it must not go quiet about
# it either. Driven by copying the runner somewhere with no library beside it
# and no ../lib above it, which is exactly what a half-finished install looks
# like.
NCW="$WORK/nolib"
mkdir -p "$NCW"
cp "$RUNNER" "$NCW/pogo-deploy.sh"
NC_STUB_OUT="$(
    set +u
    POGO_NET_CONTROL_LIB=""
    source "$NCW/pogo-deploy.sh" 2>/dev/null
    load_net_control >/dev/null 2>&1
    net_control >/dev/null 2>&1
    printf '%s|%s' "$NET_CONTROL_VERDICT" "$NET_CONTROL_REASON"
)"
case "$NC_STUB_OUT" in
    'unknown|'*) pass "a runner with no control library reports the control as UNKNOWN, never as a verdict it cannot back" ;;
    *)          fail "a runner with no control library produced: $NC_STUB_OUT" ;;
esac
case "$NC_STUB_OUT" in
    *"pogo service install-deploy"*) pass "and the missing-library reason names the command that fixes it, so the gap is actionable from the alert" ;;
    *)                               fail "the missing-library reason does not name its own fix: $NC_STUB_OUT" ;;
esac
case "$NC_STUB_OUT" in
    *"off the network"*) pass "and it states what was lost — the distinction between an off-network box and a blackholed remote — rather than just reporting a missing file" ;;
    *)                   fail "the missing-library reason does not say what the absence costs: $NC_STUB_OUT" ;;
esac

# The bridge. Three verdicts, three different instructions, and the one rule is
# that the vigil duration is only offered as an outage duration in the single
# case where the control established that the endpoint's silence WAS the box's
# silence. That number is quoted to mg-5515 as a measured lower bound today, and
# it is a lower bound on one endpoint not answering — which the reader has no
# way to tell apart without this.
NET_CONTROL_VERDICT=up
net_control_bridge | grep -q 'specific to the deploy remote' \
    && pass "bridge/up: the reader is sent to the remote, not to the link" \
    || fail "bridge/up did not name the remote as the locus"
net_control_bridge | grep -q 'Do not quote it as an outage duration' \
    && pass "bridge/up: the vigil duration is explicitly WITHDRAWN as an outage measurement" \
    || fail "bridge/up left the vigil duration standing as an outage measurement"

NET_CONTROL_VERDICT=down
net_control_bridge | grep -q 'could not reach ANYTHING' \
    && pass "bridge/down: the reader is sent to the link" \
    || fail "bridge/down did not name the link as the locus"
net_control_bridge | grep -q 'lower bound on the outage' \
    && pass "bridge/down: and ONLY here is the vigil duration offered as an outage duration, because only here is it one" \
    || fail "bridge/down did not license the vigil duration"

NET_CONTROL_VERDICT=unknown
net_control_bridge | grep -q 'nothing corroborates it' \
    && pass "bridge/unknown: the remedy below is marked as uncorroborated rather than presented as established" \
    || fail "bridge/unknown did not qualify the remedy"
net_control_bridge | grep -q 'do not quote it as an outage duration' \
    && pass "bridge/unknown: the vigil duration is withdrawn here too — an unknown control licenses nothing" \
    || fail "bridge/unknown left the vigil duration standing"

# The verdict has to REACH the reader, and the two functions above are only
# worth anything if they are in the alert. Asserted against the script text, the
# way the SYNC_DETAIL-is-printed-verbatim check above is: what is being guarded
# is that a later edit cannot quietly drop the block from the mail while every
# unit test for its contents keeps passing.
grep -q '$(net_control_report)' "$RUNNER" \
    && pass "the sync-abort alert prints the control's own report, table and all" \
    || fail "the sync-abort alert does not include net_control_report"
grep -q '$(net_control_bridge)' "$RUNNER" \
    && pass "and the sentence saying what the verdict CHANGES about the remedy under it" \
    || fail "the sync-abort alert does not include net_control_bridge"
grep -q 'net_control.*NET_CONTROL_VERDICT' "$RUNNER" \
    && pass "and the machine-readable path carries it too — the retry-pending event records the verdict alongside sync_class" \
    || fail "deploy_nightly_retry_pending does not record net_control"

# ORDER. A control swept after the alert was composed would report a verdict the
# mail does not contain, and the log would disagree with the mail about the same
# night.
NC_RUN_LINE="$(grep -n 'run_net_control$' "$RUNNER" | head -1 | cut -d: -f1)"
NC_ALERT_LINE="$(grep -n 'ABORTED: could not sync' "$RUNNER" | head -1 | cut -d: -f1)"
[ -n "$NC_RUN_LINE" ] && [ -n "$NC_ALERT_LINE" ] && [ "$NC_RUN_LINE" -lt "$NC_ALERT_LINE" ] \
    && pass "the control is swept BEFORE the alert is composed (line $NC_RUN_LINE < $NC_ALERT_LINE), so the mail and the log report the same instant" \
    || fail "run_net_control does not run before the sync-abort alert (run=$NC_RUN_LINE alert=$NC_ALERT_LINE)"

# The memoization. Two sweeps in one alert would give the reader two verdicts
# from two instants to reconcile, for a question that does not change.
NET_CONTROL_RAN=false
NC_CALLS=0
net_control() { NC_CALLS=$(( NC_CALLS + 1 )); NET_CONTROL_VERDICT=up; return 0; }
net_control_line() { echo "stub"; }
run_net_control >/dev/null 2>&1
run_net_control >/dev/null 2>&1
[ "$NC_CALLS" -eq 1 ] \
    && pass "run_net_control sweeps once per run, so an alert carries one verdict from one instant" \
    || fail "run_net_control ran the control $NC_CALLS times in one run"

# ---------------------------------------------------------------------------
# THE FALLBACK THAT NEEDS NO REMOTE (mg-9fc9)
# ---------------------------------------------------------------------------
# WHAT IS BEING TESTED, and it is not "does the bounce work". The bounce is
# pogo-self-deploy's, and its own suite covers it. What lives here is the
# TRIGGER's four decisions, each of which was a constraint in the ticket because
# each has a way of being wrong that reads as working:
#
#   which failures count      — a bad TREE must never accumulate toward a fleet
#                               bounce, and it is one careless `*)` away from
#                               doing so
#   how many nights           — N > 1, from config, and counted in NIGHTS rather
#                               than in fires
#   whether there is window    — the fallback must not be disabled by the very
#                               patience that discovered the outage
#   what gets said, and where — locally, because on the night this fires the
#                               network is what is broken

# --- transport_streak_verdict: the discriminator, and it has THREE answers ---
for c in network remote unclassified timeout; do
    [ "$(transport_streak_verdict "$c")" = "bump" ] \
        && pass "verdict($c) = bump — the sync never reached the tree, so the night delivered nothing AND restarted nothing" \
        || fail "verdict($c) = $(transport_streak_verdict "$c")"
done
# THE ONE THAT MATTERS MOST. Constraint 1: "a deploy that fails because the tree
# is bad must NOT trigger a fleet bounce — that is a different fault with a
# different remedy, and bouncing on it would be destructive noise." All three of
# these are read AFTER a successful fetch, so each is positive evidence that the
# transport works.
for c in dirty diverged checkout; do
    [ "$(transport_streak_verdict "$c")" = "clear" ] \
        && pass "verdict($c) = clear — a tree fault is evidence the transport WORKED, and must never accumulate toward a bounce" \
        || fail "verdict($c) = $(transport_streak_verdict "$c") — a tree fault must not count toward a fleet bounce"
done
# `config` fails before any network call is made, so it is evidence in neither
# direction. `leave` is the third answer, and the reason there are three.
[ "$(transport_streak_verdict config)" = "leave" ] \
    && pass "verdict(config) = leave — it fails before any network call, so the night establishes nothing about the transport either way" \
    || fail "verdict(config) = $(transport_streak_verdict config)"
[ "$(transport_streak_verdict '')" = "leave" ] && [ "$(transport_streak_verdict wat)" = "leave" ] \
    && pass "verdict: an empty or unrecognised class LEAVES the count — a missed bounce costs a night, a wrong one costs a fleet restart nobody asked for" \
    || fail "verdict: unknown class does not default to leave"

# COHERENCE WITH THE OTHER LIST. Two predicates ask "did this failure establish
# anything about the tree?" — sync_class_retryable for the retry decision and
# transport_streak_verdict for this one. They are separate functions on purpose
# (they answer different questions and could legitimately diverge), which is
# exactly why a drift between them has to be visible rather than silent: a class
# that bumps the streak but is NOT retryable would mean the fallback counting a
# night the retry tier considers settled.
DRIFT=""
for c in network remote unclassified timeout dirty diverged checkout config; do
    if [ "$(transport_streak_verdict "$c")" = "bump" ]; then
        sync_class_retryable "$c" || DRIFT="$DRIFT $c"
    fi
done
[ -z "$DRIFT" ] \
    && pass "every class that BUMPS the streak is also one sync_class_retryable calls established-nothing — the two lists agree today, and this is where they stop agreeing quietly" \
    || fail "classes bump the streak but are not retryable:$DRIFT"

# --- transport_streak_next: NIGHTS, not fires ------------------------------
[ "$(transport_streak_next 2026-08-19 "")" = "1" ] \
    && pass "streak: an absent record reads as no streak, and tonight makes it 1" || fail "streak from empty"
[ "$(transport_streak_next 2026-08-19 "2026-08-18 1 -")" = "2" ] \
    && pass "streak: last night's 1 becomes tonight's 2 — the threshold is crossed by consecutive nights" || fail "streak increment across nights"
[ "$(transport_streak_next 2026-08-19 "2026-08-18 4 2026-08-16")" = "5" ] \
    && pass "streak: a long run keeps counting past the threshold" || fail "streak increment from 4"
# IDEMPOTENT PER DATE, and this is load-bearing rather than tidy. Three fires a
# night can each reach the settling path (rc=10 reopens the night, and a run that
# could not read its own schedule believes no retry is coming). A streak that
# counted FIRES would cross a threshold of 2 in a single night — turning "two
# nights of evidence" into "two attempts an hour apart", which is the whole
# difference constraint 2 is about.
[ "$(transport_streak_next 2026-08-19 "2026-08-19 1 -")" = "1" ] \
    && pass "streak: a SECOND fire on the same night does not increment — the unit is a night, not a fire (three fires cannot manufacture a threshold)" \
    || fail "streak double-counted two fires of the same night"
[ "$(transport_streak_next 2026-08-19 "garbage")" = "1" ] \
    && pass "streak: a corrupt record reads as no streak — a bad file DELAYS a bounce, it cannot invent one" || fail "streak from garbage"
[ "$(transport_streak_field '' 1)" = "-" ] && [ "$(transport_streak_field '' 2)" = "0" ] && [ "$(transport_streak_field '' 3)" = "-" ] \
    && pass "streak fields: an absent record degrades to '- 0 -' rather than to an unset expansion under set -u" || fail "streak field defaults"
[ "$(transport_streak_field '2026-08-18 2 2026-08-15' 3)" = "2026-08-15" ] \
    && pass "streak: the last-bounce date survives a bump, so the mail can say when the fallback last fired" || fail "streak last-bounce field"

# --- transport_bounce_due: N > 1, and from the config ----------------------
transport_bounce_due 1 2 && fail "one lost night fired the fallback — N must be > 1" \
    || pass "one lost night does NOT fire the fallback (a bad night is not a run)"
transport_bounce_due 2 2 && pass "two consecutive lost nights DO fire it" || fail "N=2 at count 2"
transport_bounce_due 3 2 && pass "and it stays due past the threshold" || fail "N=2 at count 3"
transport_bounce_due 9 0 && fail "threshold 0 still bounced" || pass "POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER=0 disables the fallback entirely"
transport_bounce_due 9 '' && fail "an empty threshold bounced" || pass "a non-numeric threshold disables rather than crashes"
[ "$TRANSPORT_BOUNCE_AFTER" -gt 1 ] \
    && pass "the shipped default threshold is $TRANSPORT_BOUNCE_AFTER — greater than one, as the ticket requires, and stated in the config rather than hardcoded at the callsite" \
    || fail "the default threshold is $TRANSPORT_BOUNCE_AFTER — one bad night must not bounce the fleet"
[ "$(POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER=3 bash -c 'source "'"$RUNNER"'"; echo "$TRANSPORT_BOUNCE_AFTER"')" = "3" ] \
    && pass "the threshold is env-overridable — it is config, not a constant" || fail "POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER is not honoured"

# --- THE SELF-DEFEAT CHECK: the fallback must survive the vigil ------------
# This is the way this fix would most plausibly have exhibited the defect it
# remedies. The vigil probes until drain_budget hits ZERO on the DEPLOY's reserve
# (1200s of build + do_prove + kickstart + verify). If the fallback were charged
# that same reserve, then on every night the vigil ran to its end — which is
# every night this fallback exists for — the budget would already be zero and the
# bounce would refuse for lack of window. The remedy would have been disabled by
# the patience that discovered the outage.
[ "$BOUNCE_RESERVE" -lt "$RESERVE" ] \
    && pass "the bounce's reserve (${BOUNCE_RESERVE}s) is smaller than the deploy's (${RESERVE}s) — it owes no build and no do_prove" \
    || fail "BOUNCE_RESERVE ($BOUNCE_RESERVE) is not smaller than RESERVE ($RESERVE); the fallback would be starved on exactly the nights it exists for"
# Walk the window and find the first hour at which the DEPLOY budget is zero:
# that instant is where the vigil gives up and the fallback is asked to act.
VIGIL_END_H=""
for h in 02 03 04 05; do
    if [ "$(POGO_DEPLOY_NOW=$h drain_budget 6 "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")" -le 0 ]; then VIGIL_END_H="$h"; break; fi
done
# Under the production window (2-6) the deploy budget is still positive at 05:00
# and reaches zero between hours, so assert the property at the last hour the
# vigil can be alive rather than at a synthetic instant: the bounce must have
# strictly more window than the deploy at the same moment.
D_BUDGET="$(POGO_DEPLOY_NOW=05 drain_budget 6 "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
B_BUDGET="$(POGO_DEPLOY_NOW=05 drain_budget 6 "$BOUNCE_RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
[ "$B_BUDGET" -gt "$D_BUDGET" ] \
    && pass "at 05:00 the deploy has ${D_BUDGET}s of drain and the bounce has ${B_BUDGET}s — the fallback outlives the deploy's own budget inside the same window" \
    || fail "the bounce budget ($B_BUDGET) does not exceed the deploy's ($D_BUDGET) at 05:00"
# And a window with genuinely nothing left still refuses. A drain that cannot
# finish has stopped dispatch for its whole length and delivered nothing, which
# is worse than not bouncing.
[ "$(POGO_DEPLOY_NOW=05 drain_budget 5 "$BOUNCE_RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")" -eq 0 ] \
    && pass "a fallback at the very edge of the window gets a ZERO budget and will refuse — patience is not a licence to bounce into the working day" \
    || fail "the bounce budget did not go to zero at the window edge"

# --- the announcement: LOCAL, and it says what a bounce is not -------------
FALLBACK_STATUS=bounced FALLBACK_DETAIL="drained and restarted" SYNC_CLASS=network SYNC_VIGIL_SPENT=9000
BODY="$(fallback_body 2)"
case "$BODY" in *"NO code"*|*"no new code"*|*"No new code"*) pass "the mail says the bounce delivered NO CODE — a reader must not take a bounce for a deploy" ;; *) fail "the fallback mail does not say that no code was delivered" ;; esac
case "$BODY" in *"drain"*) pass "and that the drain still ruled" ;; *) fail "the fallback mail does not mention the drain" ;; esac
case "$BODY" in *"2 consecutive"*) pass "and how many nights were lost, which is the evidence the decision was made on" ;; *) fail "the fallback mail does not carry the streak" ;; esac
case "$BODY" in *"9000s"*) pass "and the vigil duration, as the measured lower bound on the outage" ;; *) fail "the fallback mail drops the vigil measurement" ;; esac
case "$BODY" in *POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER*) pass "and the knob that changes the threshold, including that 0 disables it" ;; *) fail "the fallback mail does not name the threshold env var" ;; esac
# THE SUBJECT IS THE PART THAT TRAVELS. "the fleet was restarted" and "the
# fallback could not restart it" need opposite reactions from the reader, so they
# must be distinguishable without opening the mail.
S_OK="$(FALLBACK_STATUS=bounced; fallback_subject 2)"
S_FAIL="$(FALLBACK_STATUS=failed; fallback_subject 2)"
S_REF="$(FALLBACK_STATUS=refused-drain; fallback_subject 2)"
{ [ "$S_OK" != "$S_FAIL" ] && [ "$S_FAIL" != "$S_REF" ] && [ "$S_OK" != "$S_REF" ]; } \
    && pass "the three outcomes get three different SUBJECTS — bounced / could not / declined are not one story" \
    || fail "fallback subjects collapse: [$S_OK] [$S_FAIL] [$S_REF]"
case "$S_OK" in *BOUNCED*) pass "and a successful bounce says so in the subject, because that is a fleet-wide event a reader has to know happened" ;; *) fail "the bounced subject does not name the bounce: $S_OK" ;; esac

# THE ANNOUNCEMENT PATH TOUCHES NOTHING REMOTE. Constraint 3: on the night this
# fires, the network is what is broken, so an announcement that needed it would
# be the ticket's own defect rebuilt inside its remedy. The maildir is local and
# `pogo events emit` is loopback; nothing that ACTS may reach for git, curl, gh,
# ssh or the reachability probe.
#
# Scoped to the three functions that act. fallback_body is excluded on purpose and
# the exclusion is the interesting part: its text QUOTES `curl -s
# http://127.0.0.1:10000/server/mode` as advice to the operator, which is loopback
# and is exactly what a reader should run — a probe that cannot tell a quoted
# remedy from a call would have to be satisfied by deleting the most useful line
# in the mail.
FALLBACK_SRC="$(sed -n '/^fallback_bounce() {/,/^}/p;/^fallback_announce() {/,/^}/p;/^resolve_bounce_script() {/,/^}/p' "$RUNNER")"
{ [ -n "$FALLBACK_SRC" ] && ! grep -qE '(^|[^_[:alnum:]])(curl|gh|ssh|probe_tcp|remote_endpoint|git_step|sync_src)([^_[:alnum:]]|$)' <<<"$FALLBACK_SRC"; } \
    && pass "no acting part of the fallback path calls curl, git, gh, ssh or the reachability probe — the announcement cannot need the resource whose loss it is announcing" \
    || fail "the fallback path reaches for the network: $(grep -nE '(^|[^_[:alnum:]])(curl|gh|ssh|probe_tcp|remote_endpoint|git_step|sync_src)([^_[:alnum:]]|$)' <<<"$FALLBACK_SRC" | head -3)"
# And the body is text, not a program: the only substitutions in it are into
# strings this runner already holds.
BODY_SRC="$(sed -n '/^fallback_body() {/,/^}/p' "$RUNNER")"
{ [ -n "$BODY_SRC" ] && ! grep -qE '\$\((curl|git|gh|ssh)' <<<"$BODY_SRC"; } \
    && pass "and fallback_body substitutes no command that could block on a network — its 127.0.0.1 line is quoted advice, not a call" \
    || fail "fallback_body runs a network command while composing the announcement"
{ grep -q '"\$MG" mail send' "$RUNNER" && grep -q 'alert "\$(fallback_subject' "$RUNNER"; } \
    && pass "and it announces through alert(), which mails the LOCAL maildir — the one channel this fault leaves standing" \
    || fail "the fallback does not announce through alert()"
# A DETECTOR FILTERS ON THE TYPE, NOT ON A SUBJECT STRING. A fleet bounce arriving
# as `deploy_nightly_failed` would be indistinguishable, to anything downstream,
# from the ordinary failed night it is a response to.
grep -q 'deploy_transport_fallback' "$RUNNER" \
    && pass "the fallback's event carries its own type — a bounce is an action taken, not a deploy that failed" \
    || fail "the fallback emits no distinct event type"

# --- resolve_bounce_script: EXECUTION, not existence ----------------------
# A pogo-self-deploy older than mg-9fc9 exits 2 on `bounce` with "unknown
# subcommand" — a correct refusal in the wrong voice, which would read as a
# broken fallback rather than an absent one. So each candidate is asked.
BSDIR="$(mktemp -d)"
mkdir -p "$BSDIR/src/scripts" "$BSDIR/boot/scripts"
cat > "$BSDIR/src/scripts/pogo-self-deploy" <<'STUB'
#!/bin/bash
echo "pogo-self-deploy check   # drift report"
echo "pogo-self-deploy redeploy [flags]"
STUB
cat > "$BSDIR/boot/scripts/pogo-self-deploy" <<'STUB'
#!/bin/bash
echo "pogo-self-deploy bounce [flags]        # guarded restart ONLY"
STUB
chmod +x "$BSDIR/src/scripts/pogo-self-deploy" "$BSDIR/boot/scripts/pogo-self-deploy"
BOUNCE_SCRIPT=""
( SRC="$BSDIR/src" BOOTSTRAP_REPO="$BSDIR/boot"; resolve_bounce_script >/dev/null 2>&1; echo "$BOUNCE_SCRIPT" ) > "$WORK/bs1"
[ "$(cat "$WORK/bs1")" = "$BSDIR/boot/scripts/pogo-self-deploy" ] \
    && pass "resolve_bounce_script SKIPS a pogo-self-deploy that predates the subcommand and takes the one that advertises it — an old script is reported absent, not broken" \
    || fail "resolve_bounce_script picked [$(cat "$WORK/bs1")]"
BOUNCE_SCRIPT=""
( SRC="$BSDIR/src" BOOTSTRAP_REPO="$BSDIR/src"; resolve_bounce_script >/dev/null 2>&1 && echo FOUND || echo NONE ) > "$WORK/bs2"
[ "$(cat "$WORK/bs2")" = "NONE" ] \
    && pass "and it FAILS rather than returning a script it could not confirm — the fallback reports that it has no bouncer" \
    || fail "resolve_bounce_script accepted a script with no bounce subcommand"
rm -rf "$BSDIR"

# --- fallback_bounce end to end, with a stubbed bouncer -------------------
# The five outcomes and what each leaves in the streak file. This is the code that
# decides whether a fleet gets restarted, so it is exercised rather than read.
#
# Each case runs in a CHILD bash that sources the runner fresh, for two reasons.
# The runner derives SRC, TRANSPORT_STREAK and the rest from POGO_DEPLOY_* at
# source time, so the fixture has to be in the environment BEFORE the source (an
# `SRC=...` in a subshell after it is simply overwritten — that mistake is what
# the first version of these assertions measured). And alert() is stubbed there
# rather than here, so no later assertion in this file is reading a fixture.
FB_DIR="$(mktemp -d)"
mkdir -p "$FB_DIR/src/scripts"
FB_MAIL="$FB_DIR/mail"; : > "$FB_MAIL"
fb_bouncer() {   # fb_bouncer RC — a pogo-self-deploy that advertises `bounce` and exits RC
    cat > "$FB_DIR/src/scripts/pogo-self-deploy" <<STUB
#!/bin/bash
[ "\$1" = "--help" ] && { echo "  pogo-self-deploy bounce [flags]        # guarded restart ONLY"; exit 0; }
echo "\$*" >> "$FB_DIR/calls"
exit $1
STUB
    chmod +x "$FB_DIR/src/scripts/pogo-self-deploy"
}
fb_run() {  # fb_run STREAK_LINE COUNT WINDOW NOW -> echoes FALLBACK_STATUS
    : > "$FB_DIR/calls"
    printf '%s\n' "$1" > "$FB_DIR/streak"
    POGO_DEPLOY_SRC="$FB_DIR/src" POGO_DEPLOY_BOOTSTRAP_REPO="$FB_DIR/src" \
    POGO_DEPLOY_TRANSPORT_STREAK="$FB_DIR/streak" POGO_DEPLOY_WINDOW="$3" POGO_DEPLOY_NOW="$4" \
    HOME="$FB_DIR" FB_MAIL="$FB_MAIL" COUNT="$2" \
    bash -c 'source "'"$RUNNER"'"
             parse_window "$POGO_DEPLOY_WINDOW"
             POGO_CLI=""; MG=""; SYNC_CLASS=network; SYNC_VIGIL_SPENT=9000
             alert() { printf "SUBJECT: %s\n" "$1" >> "$FB_MAIL"; printf "%s\n" "$2" >> "$FB_MAIL"; return 0; }
             fallback_bounce 2026-08-19 "$COUNT" >/dev/null 2>&1
             printf "%s\n" "$FALLBACK_STATUS"'
}

# not-due: below the threshold, nothing is invoked and nothing is mailed.
fb_bouncer 0
ST="$(fb_run '2026-08-19 1 -' 1 2-6 03)"
{ [ "$ST" = "not-due" ] && [ ! -s "$FB_DIR/calls" ]; } \
    && pass "fallback_bounce below the threshold reports not-due and invokes NOTHING — one lost night must cost nothing at all" \
    || fail "below-threshold fallback: status=$ST calls=[$(cat "$FB_DIR/calls" 2>/dev/null)]"

# bounced: the bouncer runs, the streak resets, the fleet event is announced.
: > "$FB_MAIL"; fb_bouncer 0
ST="$(fb_run '2026-08-18 1 -' 2 2-6 03)"
[ "$ST" = "bounced" ] && pass "fallback_bounce at the threshold runs the bouncer and reports bounced" || fail "at-threshold fallback status=$ST"
grep -q '^bounce --yes --drain-timeout [0-9][0-9]*$' "$FB_DIR/calls" \
    && pass "it calls 'bounce --yes --drain-timeout N' — the window-derived budget, and --yes because there is no tty at 03:00" \
    || fail "the bouncer was called as [$(cat "$FB_DIR/calls")]"
grep -q -- '--force' "$FB_DIR/calls" \
    && fail "the fallback passed --force — it must never orphan commits that exist only in a worktree" \
    || pass "and NEVER --force: a drain refusal is reported, not overridden"
{ [ "$(cut -d' ' -f2 "$FB_DIR/streak")" = "0" ] && [ "$(cut -d' ' -f3 "$FB_DIR/streak")" = "2026-08-19" ]; } \
    && pass "a completed bounce RESETS the streak and records the date — a week-long outage bounces once every N nights, not every night" \
    || fail "streak after a bounce: [$(cat "$FB_DIR/streak")]"
grep -q 'SUBJECT: .*FLEET BOUNCED' "$FB_MAIL" \
    && pass "and it announces the bounce out of band, on the local channel" || fail "no bounce announcement mailed: [$(cat "$FB_MAIL")]"

# failed: exit 7 is the drain refusing. The streak is NOT reset — nothing was
# restarted, so the count still measures what it measured.
: > "$FB_MAIL"; fb_bouncer 7
ST="$(fb_run '2026-08-18 1 -' 2 2-6 03)"
[ "$ST" = "failed" ] && pass "a bouncer that exits 7 (the drain stalled) reports failed — the refusal is respected, not retried past" || fail "failed-bounce status=$ST"
[ "$(cut -d' ' -f2 "$FB_DIR/streak")" = "1" ] \
    && pass "and a refused bounce does NOT reset the streak — the count still measures how long the fleet has gone without a restart" \
    || fail "a refused bounce reset the streak: [$(cat "$FB_DIR/streak")]"
grep -q 'SUBJECT: .*COULD NOT bounce' "$FB_MAIL" \
    && pass "and the refusal is mailed too — a fallback that fired and did nothing is exactly what must not be silent" || fail "no refusal announcement mailed"

# refused-window: a fire too late to finish a drain must not start one.
: > "$FB_MAIL"; fb_bouncer 0
ST="$(fb_run '2026-08-18 1 -' 2 2-5 05)"
{ [ "$ST" = "refused-window" ] && [ ! -s "$FB_DIR/calls" ]; } \
    && pass "with no usable window left the fallback refuses and never invokes the bouncer — a drain that cannot finish freezes dispatch and delivers nothing" \
    || fail "window-refusal: status=$ST calls=[$(cat "$FB_DIR/calls" 2>/dev/null)]"
grep -q 'SUBJECT: .*DECLINED' "$FB_MAIL" \
    && pass "and the DECLINED outcome is mailed — the fleet is still owed a restart and somebody has to know" || fail "a declined fallback went unannounced"

# refused-noscript: the checkout the sync could not advance may not exist at all,
# which is the `clone` arm of the very failure being handled.
: > "$FB_MAIL"; rm -f "$FB_DIR/src/scripts/pogo-self-deploy"
ST="$(fb_run '2026-08-18 1 -' 2 2-6 03)"
[ "$ST" = "refused-noscript" ] \
    && pass "a box with no local bouncer at all reports refused-noscript rather than dying inside the fallback" \
    || fail "no-script refusal: status=$ST"
unset -f fb_bouncer fb_run
rm -rf "$FB_DIR"
# The stubs above lived in child processes; this is the witness for that claim,
# because every assertion after this point would otherwise be reading a fixture.
declare -f alert | grep -q 'mail send' \
    && pass "the real alert() is intact after the fallback fixtures — the stubs were confined to child shells" \
    || fail "alert() is still stubbed; later assertions in this file would be measuring the fixture"


# --- bounce_reason_line: the runner reads the bounce's own sentence -------
# Same channel, same rule as the deploy's (mg-0155): the caller does not
# re-derive a story from an integer. The fallback's mail is the only
# announcement on a night the network is down, so it is the last place that can
# afford a guess about why the bounce did not happen.
BRF="$WORK/bounce-reason"
cat > "$BRF" <<'REC'
exit=7
stage=drain
installed=no
reason=3 polecat(s) still hold commits that exist only in their worktree after 900s
--- verbatim ---
3 polecat(s) still hold commits that exist only in their worktree after 900s
refusing to orphan unpushed work without --force; restoring dispatch
REC
OUT="$(bounce_reason_line "$BRF" 7)"
case "$OUT" in
    *"stage drain"*"3 polecat(s) still hold commits"*) pass "bounce_reason_line reports the stage and the bounce's OWN headline, not a story derived from the code" ;;
    *) fail "bounce_reason_line: $OUT" ;;
esac
OUT="$(bounce_reason_line "$WORK/no-such-record" 5)"
case "$OUT" in
    *"left no reason record"*"$WORK/no-such-record"*) pass "and with no record it SAYS there is none, naming the path — an absent record is reported, never filled in" ;;
    *) fail "bounce_reason_line with no record: $OUT" ;;
esac
case "$(bounce_reason_line "$WORK/no-such-record" 5)" in
    *"$(describe_exit 5)"*) pass "falling back to describe_exit for the code alone, which is all an integer can honestly carry" ;;
    *) fail "the no-record fallback does not describe the exit code" ;;
esac

# --- WIRING: the trigger is reached, and only where it should be ----------
# A fallback nothing calls is the mg-b9e7 shape — a detector that existed and
# only fired when a person ran it. And a fallback called from the WRONG place is
# worse than none: before retry_will_follow it would bounce the fleet at 03:00 on
# a night the 04:00 fire could still have deployed properly.
RWF_LINE="$(grep -n 'if retry_will_follow "\$sync_rc"; then' "$RUNNER" | cut -d: -f1)"
FB_LINE="$(grep -n '^                    fallback_bounce "\$today" "\$streak" ;;$' "$RUNNER" | cut -d: -f1)"
SYNC_ALERT_LINE="$(grep -n 'ABORTED: could not sync' "$RUNNER" | head -1 | cut -d: -f1)"
{ [ -n "$RWF_LINE" ] && [ -n "$FB_LINE" ] && [ "$FB_LINE" -gt "$RWF_LINE" ] && [ "$FB_LINE" -lt "$SYNC_ALERT_LINE" ]; } \
    && pass "fallback_bounce is called AFTER retry_will_follow and BEFORE the sync alert (lines $RWF_LINE < $FB_LINE < $SYNC_ALERT_LINE) — no bounce while a later fire could still deploy, and the alert can report what the fallback did" \
    || fail "fallback_bounce is wired at line ${FB_LINE:-?}, outside (${RWF_LINE:-?}, ${SYNC_ALERT_LINE:-?})"
# AND IT IS INSIDE THE RUN DEADLINE'S COVERAGE. The bounce is a long-running
# orchestration call and is deliberately not wrapped in run_bounded — its
# legitimate duration IS the drain budget, and a second bound would either
# duplicate that number or contradict it. What bounds it is arm_run_deadline, a
# watchdog in a separate process that covers whichever stage the run is wedged in
# (mg-56ac). That only holds if the fallback runs after the arming.
ARM_LINE="$(grep -n 'arm_run_deadline "\$DEADLINE_S" "\$\$"' "$RUNNER" | cut -d: -f1)"
{ [ -n "$ARM_LINE" ] && [ "$FB_LINE" -gt "$ARM_LINE" ]; } \
    && pass "the fallback runs downstream of arm_run_deadline (line $ARM_LINE), so a bounce that wedges is killed by the same watchdog as every other stage" \
    || fail "fallback_bounce (${FB_LINE:-?}) is not covered by the run deadline (${ARM_LINE:-?})"

# A DRY RUN MUST NOT BOUNCE A FLEET. ATTEMPT_ARMED is false for --dry-run and for
# every fire that skipped, and it is the guard on both the bump and the reset.
sed -n "${RWF_LINE},${SYNC_ALERT_LINE}p" "$RUNNER" | grep -q 'if \$ATTEMPT_ARMED; then' \
    && pass "the whole streak-and-bounce block is guarded by \$ATTEMPT_ARMED — a dry run decides nothing and bounces nothing" \
    || fail "the fallback block is not guarded by ATTEMPT_ARMED"
# The reset lives on the OBSERVATION, not on an exit code: this line is the one
# place a run has proved the transport works.
SR_LINE="$(grep -n 'sync_recovery_notice "\$SYNC_TRIES"' "$RUNNER" | cut -d: -f1)"
CLR_LINE="$(grep -n 'transport streak: CLEARED (was' "$RUNNER" | cut -d: -f1)"
{ [ -n "$SR_LINE" ] && [ -n "$CLR_LINE" ] && [ "$CLR_LINE" -gt "$SR_LINE" ] && [ "$CLR_LINE" -lt "$RWF_LINE" ]; } \
    && fail "the streak reset sits before the sync-failure branch — check the line numbers, it must follow a SUCCESSFUL sync" \
    || pass "the streak is cleared on the successful-sync path, keyed on the fetch having returned rather than on the run's final exit code"
grep -q '  fallback: \$FALLBACK_STATUS' "$RUNNER" \
    && pass "and the sync-abort alert cross-references what the fallback did, so one mail is not read without the other" \
    || fail "the sync alert does not report the fallback's outcome"

# ---------------------------------------------------------------------------
echo
echo "--- Results ---"
grep -c '^PASS' "$RESULTS_FILE" | sed 's/^/passed: /'
if grep -q '^FAIL' "$RESULTS_FILE"; then
    grep '^FAIL' "$RESULTS_FILE"
    exit 1
fi
echo "all pogo-deploy trigger tests passed"
