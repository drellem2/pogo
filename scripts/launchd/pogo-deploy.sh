#!/bin/bash
# pogo-deploy.sh — the unattended nightly redeploy TRIGGER (mg-42ac / mg-b7d0).
#
# WHAT THIS IS NOT
# ----------------
# It is not a deployer. Every gate that matters — the ancestry guard, the drain,
# the build, do_prove, the kickstart, verify_running, the mail-check post-check —
# already lives in scripts/pogo-self-deploy, which is deliberately thin and
# rarely touched. This file adds the ONE thing that script cannot supply for
# itself: a caller outside pogod's process tree that fires on a clock. Anything
# here that starts to look like deploy logic belongs in pogo-self-deploy instead.
#
# It is also NOT com.pogo.recovery. Recovery is the tier-3 safety net that
# bounces a wedged pogod; folding a deploy into it would mean every recovery
# restart also became a rebuild from main, which is the opposite of a safety net
# (mg-cf48 declined exactly that conflation). Two jobs, two labels, two logs.
#
# WHY IT IS A LAUNCHD JOB AND NOT A pogod SCHEDULE
# ------------------------------------------------
# pogo-self-deploy's first line is assert_out_of_band (mg-1bbf): it refuses any
# caller inside pogod's tree, because the kickstart -k it ends with kills that
# tree — including the caller, mid-deploy, with nothing left running to report
# what happened. Every crew agent and every polecat is such a descendant. A
# LaunchAgent is parented by launchd, so it passes the guard by construction.
#
# THE THREE FAILURE MODES THIS FILE IS SHAPED AROUND
# --------------------------------------------------
#   (a) it silently no-ops forever — the window is too narrow, or the mac slept
#       through 03:00 and the catch-up fire lands at 14:00 mid-workday;
#   (b) it clobbers Daniel's dev tree — a fetch/checkout/merge in a working
#       directory he may be mid-edit in;
#   (c) it forces a bad deploy — a do_prove RED papered over with --force.
#
# Against (a): the window guard is a RANGE, not an instant, and every skip is
# logged with its reason so a job that never deploys is distinguishable from a
# job that never ran. Against (b): the deploy builds from a DEDICATED checkout
# ($POGO_DEPLOY_SRC, default ~/.pogo/deploy-src) that nothing else writes to,
# and even there a dirty tree aborts rather than resets. Against (c): --force is
# not passed, not plumbed, and not overridable by env — a do_prove RED (exit 9)
# is a loud alert and a preserved running pogod, which is the correct outcome.
#
# THE DRAIN BUDGET IS DERIVED FROM THE WINDOW, NOT A CONSTANT (mg-8f7e)
# -----------------------------------------------------------------------
# pogo-self-deploy's own default is 1800s, and on 2026-07-31 that budget was
# shorter than a routine polecat lifetime: the drain gave up with three still
# working, whose uptimes were 1h33m, 1h19m and 38m. Two of the three had
# individually been running longer than the whole budget before the drain
# started. The nightly exited 7 and the next attempt was 24 hours away.
#
# The fix is not a bigger constant — a constant calibrated against today's queue
# expires the moment the queue shifts toward longer items, which is exactly what
# had happened. It is to stop having a constant. The number that actually
# constrains the drain is when the window closes: the redeploy must not bounce
# the fleet into the working day. So the budget is
#
#     (seconds until WINDOW_END) - RESERVE, capped at MAX_DRAIN
#
# where RESERVE is the time the build, do_prove, kickstart and verification need
# after the drain finishes. A 03:00 fire under the production window gets 2h
# rather than 30m, and a fire that lands too late to finish gets no attempt at
# all rather than a doomed one. Nothing here needs recalibrating when polecats
# get longer — the window is the thing an operator already reasons about.
#
# WHY A LONG SINGLE DRAIN, AND ONLY THEN A RETRY
# -----------------------------------------------
# Retries are the weaker half of this and must not be mistaken for the fix.
# `draining=true` refuses new polecat dispatch (verified live in the running
# daemon: internal/agent/api.go handleSpawnPolecat, shipped in 1b1f12d), so a
# drain is MONOTONE — the count only falls. The moment an attempt gives up,
# pogo-self-deploy's exit trap restores dispatch and the fleet refills. Three
# 30-minute attempts are therefore strictly worse than one 90-minute attempt:
# each retry starts against a partly-fresh fleet, and the long blockers it was
# waiting out have been joined by new ones.
#
# So the budget comes first and the retry is the backstop for an attempt that
# ended EARLY — one that stalled with window left over, or a night where the
# 03:00 fire never happened because the mac was asleep. It is scoped to exit 7
# for the same reason: that is the only exit whose cause is "the fleet was
# busy", which is transient, and the only one that built nothing and bounced
# nothing. A build failure or a control-suite RED will fail identically on the
# retry and mail a duplicate alert, so the night is settled on the first attempt.
#
# HYGIENE: ABSOLUTE PATHS, NEVER BARE NAMES (mg-dd5f / mg-015f)
# -------------------------------------------------------------
# launchd hands a job a minimal PATH, and on macOS /usr/bin/mg is the Micro-Emacs
# EDITOR — bare `mg` binds to it, panics headless ("standard input and output
# must be a terminal") and delivers no alert at all. So `mg` is resolved to an
# absolute path through an IDENTITY check before use, and `pogo` /
# `pogo-self-deploy` are addressed by absolute path too. A wrapper that alerts
# via a binary it did not verify is a wrapper with no alert path.
#
# ...AND AN ABSOLUTE PATH IS NOT A WORKING BINARY (mg-b72a)
# ----------------------------------------------------------
# The identity check above is really an EXECUTION check: `mg` is accepted only
# after it runs and prints something only macguffin prints. `git` was the one
# primitive that did not get the same treatment — it was pinned to /usr/bin/git
# on the reasoning that git ships in /usr/bin on every macOS. It does; but
# /usr/bin/git is the Xcode Command Line Tools SHIM, and a shim is not a git.
# When the install behind it is damaged the shim fails EVERY invocation,
# `git --version` included, with "unable to locate xcodebuild" and exit 71 —
# while staying executable, staying on PATH, and remaining indistinguishable
# from a healthy git to `-x` and to `command -v`.
#
# So `git` is now resolved the way `mg` already was: candidates in order, each
# required to prove itself by actually RUNNING. This is a CONSISTENCY change and
# is justified as one — `git` was the lone primitive trusted on sight. No such
# breakage has been reproduced on this host, and the change is deliberately
# modest here: where the only git present is a healthy /usr/bin/git the
# candidate list collapses to that one path, and the sole difference is that a
# broken shim would abort once, loudly, with an alert, instead of failing
# separately inside every clone/fetch/rev-parse in sync_src. It changes which
# git runs only on a host that has more than one — a Homebrew box — and there it
# deploys instead of aborting.
#
# GH_TOKEN IS SOURCED AT RUNTIME, NEVER FROM THE PLIST
# ----------------------------------------------------
# ~/Library/LaunchAgents is world-readable; a token in the plist is a token on
# disk for every process on the box. It is read out of ~/.zshenv at run time
# instead (one grep, one eval of one export line — not a wholesale source of a
# zsh file by bash). The value is never logged, echoed, or mailed.
#
# USAGE
#   pogo-deploy.sh              # the nightly run (what launchd invokes)
#   pogo-deploy.sh --dry-run    # every gate, no redeploy — prints what it would do
#
# ENV overrides (all optional; defaults are the production values):
#   POGO_DEPLOY_SRC          dedicated checkout to build from (~/.pogo/deploy-src)
#   POGO_DEPLOY_REMOTE       clone URL used to create it if absent
#   POGO_DEPLOY_BOOTSTRAP_REPO  repo to read the origin URL from ($HOME/dev/pogo)
#   POGO_DEPLOY_WINDOW       "START-END" local hours, half-open (default "2-6")
#   POGO_DEPLOY_ZSHENV       file to read GH_TOKEN from ($HOME/.zshenv)
#   POGO_DEPLOY_GRACE        seconds to wait before the post-bounce check (120)
#   POGO_DEPLOY_LOCK_DIR     mutual-exclusion dir
#   POGO_DEPLOY_ALERT_TO     first alert recipient (pm-pogo)
#   POGO_DEPLOY_SKIP_WINDOW  set to 1 to bypass the window guard (controls only)
#   POGO_DEPLOY_NOW          "HH" override for the window guard (tests only)
#   POGO_DEPLOY_RESERVE      seconds of the window kept back for build+bounce (1200)
#   POGO_DEPLOY_MAX_DRAIN    ceiling on one drain, seconds (7200)
#   POGO_DEPLOY_MIN_DRAIN    floor below which a fire does not attempt at all (600)
#   POGO_DEPLOY_FIRE_HOURS   the plist's fire hours, for "will a retry follow?" ("3 4 5")
#   POGO_DEPLOY_STAMP        the night's attempt record ($POGO_HOME/deploy-attempt.stamp)
#   GIT                      pin a specific git (still checked by execution)

set -u

HOME="${HOME:-$(cd ~ && pwd)}"
SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
BOOTSTRAP_REPO="${POGO_DEPLOY_BOOTSTRAP_REPO:-$HOME/dev/pogo}"
DEPLOY_REMOTE="${POGO_DEPLOY_REMOTE:-}"
WINDOW="${POGO_DEPLOY_WINDOW:-2-6}"
ZSHENV="${POGO_DEPLOY_ZSHENV:-$HOME/.zshenv}"
GRACE="${POGO_DEPLOY_GRACE:-120}"
LOCK_DIR="${POGO_DEPLOY_LOCK_DIR:-$HOME/.pogo/deploy.lock.d}"
ALERT_TO="${POGO_DEPLOY_ALERT_TO:-pm-pogo}"
DEPLOY_REF="${POGO_DEPLOY_REF:-main}"
STALE_LOCK_MIN="${POGO_DEPLOY_STALE_LOCK_MIN:-180}"
DRY_RUN=false

# The drain budget's three parameters (mg-8f7e — see the header).
#
# RESERVE is what the run still owes after the drain returns: go install, the
# post-install revision check, do_prove, the kickstart, verify_running, the
# mail-check post-check, and this file's own GRACE sleep. 20 minutes is generous
# for a build that normally takes two, and being generous is the right side to
# err on — overrunning the window means bouncing the fleet into the working day,
# while under-using it costs only drain patience.
#
# MAX_DRAIN caps one attempt at 2h. The drain holds `draining=true` for its whole
# duration and nothing dispatches while it does, so an unbounded budget would
# trade a missed deploy for a night of no dispatch at all. 2h clears the longest
# polecat lifetime observed on 07-31 (1h33m) with margin.
#
# MIN_DRAIN is the floor. A fire landing at 05:50 has minutes, not hours; it must
# skip rather than start a drain it cannot finish, because a drain that times out
# has still stopped dispatch for its whole length and delivered nothing.
RESERVE="${POGO_DEPLOY_RESERVE:-1200}"
MAX_DRAIN="${POGO_DEPLOY_MAX_DRAIN:-7200}"
MIN_DRAIN="${POGO_DEPLOY_MIN_DRAIN:-600}"

# The plist's fire hours. Duplicated from com.pogo.deploy.plist rather than read
# from it, and that duplication buys exactly one thing: the RED alert can say
# whether a retry is coming instead of asserting "did not retry" and being wrong.
# A drifted list makes one sentence of one alert optimistic; reading the plist at
# 03:00 to avoid that would add a parse and a failure mode to the path that has
# to work when everything else is broken.
FIRE_HOURS="${POGO_DEPLOY_FIRE_HOURS:-3 4 5}"

# Where the night's outcome is recorded, so a 04:00 fire knows what the 03:00
# fire did. Under POGO_HOME, which the plist binds, so the job and an operator
# running it by hand agree on the path.
STAMP="${POGO_DEPLOY_STAMP:-${POGO_HOME:-$HOME/.pogo}/deploy-attempt.stamp}"

# Set once a fire is past the window/lock/stamp gates and is really attempting
# tonight's deploy. The EXIT trap records an attempt only when it is true, so a
# fire that skipped (late, locked out, already settled) leaves no trace and
# cannot be mistaken for one that ran.
ATTEMPT_ARMED=false
ATTEMPT_N=0

# GIT, MG and POGO_CLI are all resolved at run time and all checked by RUNNING
# the candidate, never by trusting its path. GIT is seeded from the environment
# when an operator pins one, and that pin is health-checked like any other
# candidate: a pinned path that cannot run is the same outage as no pin at all.
GIT="${GIT:-}"
MG=""
POGO_CLI=""

ts()  { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "[$(ts)] $*"; }
err() { echo "[$(ts)] ERROR: $*" >&2; }

# ---------------------------------------------------------------------------
# 1. Window guard
# ---------------------------------------------------------------------------
# StartCalendarInterval is not a promise that the job runs at 03:00 — it is a
# promise that the job runs. If the mac is asleep at 03:00 the fire is deferred
# and launchd delivers it on the next wake, which is whenever Daniel opens the
# lid: 09:00, 14:00, mid-demo. A redeploy bounces the entire fleet, so a
# deferred fire must be DROPPED, not honoured late.
#
# The window is a RANGE and not an instant for the opposite failure: a mac that
# wakes at 04:15 for a Time Machine run should still get its deploy. Too narrow
# and the job silently never deploys — which reads identically to a job that was
# never installed. 02:00–06:00 is wide enough to catch a nearby wake and narrow
# enough that nobody is working.
#
# It widened from 2-5 to 2-6 with mg-8f7e, and the extra hour is not slack: the
# drain budget is now derived from the distance to WINDOW_END, so the window's
# width IS the deploy's patience. 06:00 is still comfortably before anybody is
# at the machine, and it buys the 03:00 fire a two-hour drain where 05:00 would
# have capped it at 100 minutes.
#
# Half-open [START, END): hour 6 is out, so a 06:00 wake does not deploy into
# the start of the day.
parse_window() {
    local spec="$1"
    case "$spec" in
        [0-9]*-[0-9]*) : ;;
        *) return 1 ;;
    esac
    WINDOW_START=$(( 10#${spec%%-*} ))
    WINDOW_END=$(( 10#${spec##*-} ))
    { [ "$WINDOW_START" -ge 0 ] && [ "$WINDOW_END" -le 24 ] && [ "$WINDOW_START" -lt "$WINDOW_END" ]; }
}

# in_window HOUR START END — half-open [START,END). Base-10 forced: `date +%H`
# emits "08"/"09", which bash reads as invalid OCTAL and errors under set -u,
# turning a routine 08:00 catch-up fire into a crash instead of a skip.
in_window() {
    local h=$(( 10#$1 )) start=$(( 10#$2 )) end=$(( 10#$3 ))
    [ "$h" -ge "$start" ] && [ "$h" -lt "$end" ]
}

current_hour() {
    if [ -n "${POGO_DEPLOY_NOW:-}" ]; then printf '%s' "$POGO_DEPLOY_NOW"; else date +%H; fi
}

# now_hms — the local clock as "H M S", honouring POGO_DEPLOY_NOW so a control
# can place a run anywhere in (or out of) the window. Under the override minutes
# and seconds are zero: a test that says "it is 05:00" means the top of the hour,
# not five o'clock plus whatever the real wall clock happens to read.
now_hms() {
    if [ -n "${POGO_DEPLOY_NOW:-}" ]; then printf '%s 0 0' "$POGO_DEPLOY_NOW"; else date '+%H %M %S'; fi
}

# deploy_date — the night this fire belongs to. The window never crosses
# midnight (parse_window rejects an inverted range), so the calendar date is the
# night's identity and no rollover arithmetic is needed.
deploy_date() {
    if [ -n "${POGO_DEPLOY_DATE:-}" ]; then printf '%s' "$POGO_DEPLOY_DATE"; else date +%F; fi
}

# ---------------------------------------------------------------------------
# 1b. Drain budget (mg-8f7e)
# ---------------------------------------------------------------------------
# See the header for why this is derived rather than a constant. The arithmetic
# is deliberately plain integer maths on H/M/S rather than `date -j` epoch
# parsing: this runs at 03:00 as the only thing standing between a merge and
# activation, and a date-format dependency is one more way for it to fail on a
# host where nobody is watching.
#
# Returns 0 — not a negative number — when the remaining window is below the
# floor. "Zero seconds" is the caller's cue to skip; a negative budget handed on
# to --drain-timeout would be read as an immediate timeout and produce a fake
# exit 7 rather than an honest skip.
drain_budget() {
    local end=$(( 10#$1 )) reserve=$(( 10#$2 )) max=$(( 10#$3 )) min=$(( 10#$4 ))
    local h m s left
    if [ $# -ge 7 ]; then h="$5"; m="$6"; s="$7"; else read -r h m s <<<"$(now_hms)"; fi
    left=$(( end * 3600 - (10#$h * 3600 + 10#$m * 60 + 10#$s) - reserve ))
    [ "$left" -gt "$max" ] && left="$max"
    [ "$left" -lt "$min" ] && left=0
    printf '%s' "$left"
}

# next_fire_hour NOW_H [HOURS] — the next scheduled fire strictly after NOW_H;
# non-zero when tonight has none left. FIRE_HOURS must stay sorted ascending.
next_fire_hour() {
    local now=$(( 10#$1 )) hours="${2:-$FIRE_HOURS}" h
    for h in $hours; do
        if [ $(( 10#$h )) -gt "$now" ]; then printf '%s' "$(( 10#$h ))"; return 0; fi
    done
    return 1
}

# retry_will_follow RC — will a later fire tonight actually try again? Three
# things all have to hold, and asserting it without checking them is how the RED
# alert came to claim "did not retry" as an unconditional fact.
retry_will_follow() {
    local rc="$1" nxt
    [ "$rc" -eq 7 ] || return 1
    nxt="$(next_fire_hour "$(current_hour)")" || return 1
    [ "$(drain_budget "$WINDOW_END" "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN" "$nxt" 0 0)" -gt 0 ]
}

# ---------------------------------------------------------------------------
# 1c. The night's attempt record (mg-8f7e)
# ---------------------------------------------------------------------------
# One line: "<date> <attempts> <last_rc>". It exists so a 04:00 fire can tell
# the two nights apart that otherwise look identical from inside it — one where
# 03:00 stalled on a busy fleet (retry: the fleet may have thinned) and one
# where 03:00 failed on the build (do not retry: it will fail the same way and
# mail a second identical alert).
#
# An unreadable or absent record reads as "first attempt", which is the same
# behaviour this job had before the record existed. Failing that way round
# means a corrupt stamp costs at most one extra deploy attempt, where the
# opposite default would silently disable the nightly.
stamp_write() {
    mkdir -p "$(dirname "$1")" 2>/dev/null
    printf '%s %s %s\n' "$2" "$3" "$4" > "$1"
}

stamp_read() { cat "$1" 2>/dev/null || true; }

# attempt_disposition TODAY LINE -> first | retry | settled
attempt_disposition() {
    local today="$1" line="$2" d n rc
    [ -n "$line" ] || { echo first; return 0; }
    read -r d n rc <<<"$line"
    : "$n"
    case "${rc:-}" in
        ''|*[!0-9]*) echo first; return 0 ;;
    esac
    [ "$d" = "$today" ] || { echo first; return 0; }
    if [ "$rc" -eq 7 ]; then echo retry; else echo settled; fi
}

# stamp_attempts LINE TODAY — attempts already made tonight (0 for another night).
stamp_attempts() {
    local line="$1" today="$2" d n
    [ -n "$line" ] || { echo 0; return 0; }
    read -r d n _ <<<"$line"
    if [ "$d" = "$today" ] && [ -n "$n" ] && [ -z "${n//[0-9]/}" ]; then echo "$n"; else echo 0; fi
}

# ---------------------------------------------------------------------------
# 2. Tool resolution — identity, not existence (mg-015f / mg-dd5f)
# ---------------------------------------------------------------------------
# /usr/bin/mg satisfies -x and `command -v mg`; it is the Micro-Emacs editor.
# Locating a candidate is never the same as trusting it, so each must
# self-identify as macguffin before it is accepted. GOPATH candidates are tried
# FIRST so the production run — launchd PATH, /usr/bin ahead of ~/go/bin —
# resolves the real binary without consulting PATH at all.
resolve_mg() {
    local cand gobin gopath pathmg
    local -a cands=()
    gobin="$(go env GOBIN 2>/dev/null)"
    [ -n "$gobin" ] && cands+=("$gobin/mg")
    gopath="$(go env GOPATH 2>/dev/null)"
    [ -n "$gopath" ] && cands+=("$gopath/bin/mg")
    [ -n "${GOPATH:-}" ] && cands+=("$GOPATH/bin/mg")
    cands+=("$HOME/go/bin/mg")
    pathmg="$(command -v mg 2>/dev/null)"
    [ -n "$pathmg" ] && cands+=("$pathmg")

    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        "$cand" --help 2>/dev/null | grep -q 'macguffin' || continue
        MG="$cand"
        log "mg: resolved macguffin at $MG"
        return 0
    done
    err "mg: no macguffin 'mg' among ${cands[*]} — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR)"
    return 1
}

# `git`, resolved by EXECUTION rather than by existence, for the reason in the
# header: /usr/bin/git is the Command Line Tools shim, and a damaged CLT leaves
# it executable, on PATH, and unable to complete a single call. `-x` and
# `command -v` both say yes about it. Requiring "git version" on stdout is the
# cheapest question that only a working git can answer.
#
# A real Homebrew/local git is preferred over the shim because the shim is the
# fragile one, but the shim stays in the list so a CLT-only box still deploys.
# On a host where it is the only git present, this list has exactly one entry
# and the preference never comes up.
#
# Factored out from resolve_git so the tests can substitute a list of fakes. On
# a host with a healthy git the real list can never produce a rejection, and an
# execution check that has only ever been exercised against a working binary has
# not been tested at all.
git_candidates() {
    local -a cands=()
    [ -n "${GIT:-}" ] && cands+=("$GIT")
    cands+=("/opt/homebrew/bin/git" "/usr/local/bin/git" "/usr/bin/git")
    local onpath; onpath="$(command -v git 2>/dev/null)"
    [ -n "$onpath" ] && cands+=("$onpath")
    printf '%s\n' "${cands[@]}"
}

resolve_git() {
    local cand
    local -a cands=()
    while IFS= read -r cand; do
        [ -n "$cand" ] && cands+=("$cand")
    done < <(git_candidates)

    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        "$cand" --version 2>/dev/null | grep -q '^git version' || continue
        GIT="$cand"
        log "git: resolved working git at $GIT ($("$cand" --version 2>/dev/null))"
        return 0
    done
    err "git: no WORKING git among ${cands[*]:-<none>} — a present-but-broken /usr/bin/git (damaged Xcode CLT) fails every call with exit 71, so existence proves nothing"
    return 1
}

# The `pogo` CLI, wanted for the post-bounce schedule read and for events emit.
# Same absolute-path discipline; the identity check is cheaper (there is no
# same-named impostor in /usr/bin) but PATH still may not contain it.
resolve_pogo() {
    local cand
    local -a cands=()
    [ -n "${POGO_BIN:-}" ] && cands+=("$POGO_BIN")
    cands+=("$HOME/go/bin/pogo" "$HOME/.local/bin/pogo")
    cand="$(command -v pogo 2>/dev/null)"
    [ -n "$cand" ] && cands+=("$cand")
    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        POGO_CLI="$cand"
        log "pogo: resolved CLI at $POGO_CLI"
        return 0
    done
    err "pogo: no 'pogo' CLI found among ${cands[*]}"
    return 1
}

# ---------------------------------------------------------------------------
# 3. GH_TOKEN at run time
# ---------------------------------------------------------------------------
# Bash cannot `source` a zsh file safely, and we do not want to run arbitrary
# init anyway — we want ONE variable. So: match the export line, eval that line
# alone. If GH_TOKEN is already in the environment (an operator running this by
# hand from a shell) we keep it and read nothing.
#
# The value is never logged. Callers get "present"/"absent", which is the only
# fact any diagnostic needs; a prefix echo is just as leakable as the whole
# token and the transcript outlives the run.
load_gh_token() {
    local f="${1:-$ZSHENV}" line
    if [ -n "${GH_TOKEN:-}" ]; then
        log "GH_TOKEN: already present in the environment"
        return 0
    fi
    if [ ! -r "$f" ]; then
        err "GH_TOKEN: cannot read $f"
        return 1
    fi
    line="$(grep -E '^[[:space:]]*export[[:space:]]+GH_TOKEN=' "$f" 2>/dev/null | tail -1)"
    if [ -z "$line" ]; then
        err "GH_TOKEN: no 'export GH_TOKEN=' line in $f"
        return 1
    fi
    eval "$line" || { err "GH_TOKEN: could not evaluate the export line in $f"; return 1; }
    if [ -z "${GH_TOKEN:-}" ]; then
        err "GH_TOKEN: the export line in $f yielded an empty value"
        return 1
    fi
    export GH_TOKEN
    log "GH_TOKEN: sourced from $f (present, ${#GH_TOKEN} chars)"
    return 0
}

# ---------------------------------------------------------------------------
# 4. Alerting
# ---------------------------------------------------------------------------
# Two channels, on purpose. The event is for the digest and is best-effort; the
# mail is the loud half. `human` is always copied on a RED because a deploy that
# refused to deploy is a thing Daniel has to know about by morning, and pm-pogo
# reading it is not the same as Daniel seeing it.
alert() {
    local subject="$1" body="$2" bf rc=0
    err "ALERT: $subject"
    printf '%s\n' "$body" >&2

    if [ -n "$POGO_CLI" ]; then
        "$POGO_CLI" events emit --type=deploy_nightly_failed --agent=pogo-deploy \
            --details="{\"subject\":\"$subject\"}" >/dev/null 2>&1 || true
    fi
    [ -n "$MG" ] || { err "alert: no macguffin resolved — NOTHING WAS MAILED"; return 1; }

    bf="$(mktemp)" || { err "alert: cannot create a body file"; return 1; }
    printf '%s\n' "$body" > "$bf"
    # --body-file, not --body: the shell expands $VAR / $(cmd) inside a --body
    # string before mg ever sees it, and mg reports Delivered on the mangled
    # text (mg-8380). These bodies carry log paths and remedies verbatim.
    "$MG" mail send "$ALERT_TO" --from=pogo-deploy --subject="$subject" --body-file "$bf" >/dev/null 2>&1 \
        || { err "alert: mail to '$ALERT_TO' failed"; rc=1; }
    "$MG" mail send human --from=pogo-deploy --subject="$subject" --body-file "$bf" >/dev/null 2>&1 \
        || { err "alert: mail to 'human' failed"; rc=1; }
    rm -f "$bf"
    [ "$rc" -eq 0 ] && log "alert: mailed '$ALERT_TO' and 'human'"
    return $rc
}

# ---------------------------------------------------------------------------
# 5. Safe sync of the dedicated checkout
# ---------------------------------------------------------------------------
# A DEDICATED checkout, not /Users/daniel/dev/pogo. The dev tree is a place a
# human works: at 03:00 it may hold a half-finished edit, a rebase in progress,
# or a branch that is not main. Nothing this job does should be able to touch
# it, and the strongest way to guarantee that is to never name it. The only read
# of the dev tree here is `git remote get-url origin` during the one-time clone.
#
# Even in the dedicated tree, DIRTY ABORTS. It should never be dirty; if it is,
# something is going on that nobody has explained, and a reset would destroy the
# evidence of it. Fail loudly, deploy nothing.
sync_src() {
    if [ ! -d "$SRC/.git" ]; then
        local remote="$DEPLOY_REMOTE"
        if [ -z "$remote" ]; then
            remote="$("$GIT" -C "$BOOTSTRAP_REPO" remote get-url origin 2>/dev/null)"
        fi
        [ -n "$remote" ] || { err "sync: no clone URL (set POGO_DEPLOY_REMOTE) and could not read origin from $BOOTSTRAP_REPO"; return 1; }
        log "sync: bootstrapping the dedicated checkout at $SRC from $remote"
        "$GIT" clone --quiet "$remote" "$SRC" || { err "sync: clone failed"; return 1; }
    fi

    "$GIT" -C "$SRC" fetch --quiet origin || { err "sync: git fetch origin failed in $SRC"; return 1; }

    if [ -n "$("$GIT" -C "$SRC" status --porcelain 2>/dev/null)" ]; then
        err "sync: $SRC is DIRTY — refusing to touch it"
        "$GIT" -C "$SRC" status --short >&2
        return 1
    fi

    "$GIT" -C "$SRC" checkout --quiet "$DEPLOY_REF" || { err "sync: cannot checkout $DEPLOY_REF in $SRC"; return 1; }
    # --ff-only: a deploy tree that has diverged from origin has commits nobody
    # meant to build. Merging them would deploy them; resetting would erase
    # them. Refuse, and let a human look.
    if ! "$GIT" -C "$SRC" merge --ff-only --quiet "origin/$DEPLOY_REF"; then
        err "sync: $SRC has DIVERGED from origin/$DEPLOY_REF — refusing a non-fast-forward"
        return 1
    fi
    log "sync: $SRC at $DEPLOY_REF $("$GIT" -C "$SRC" rev-parse --short HEAD)"
    return 0
}

# ---------------------------------------------------------------------------
# 6. Drift gate
# ---------------------------------------------------------------------------
# `pogo-self-deploy check` is read-only and never acts, and its classifier is
# the one already trusted to decide what a redeploy owes — including CLI drift,
# which it covers deliberately (mg-ddf1). Reusing its verdict rather than
# recomputing "running rev == origin/main" here is the difference between a
# trigger and a second deployer with its own opinion.
#
# no drift -> exit 0 WITHOUT bouncing. A fleet-wide bounce costs every agent its
# session; doing it for a no-op is strictly worse than not running at all.
is_clean_verdict() {
    printf '%s' "$1" | grep -qE '^action[[:space:]]*:[[:space:]]*clean'
}

# ---------------------------------------------------------------------------
# 7. Outcome classification
# ---------------------------------------------------------------------------
# The exit codes are pogo-self-deploy's, and each names a DIFFERENT operator
# response. Collapsing them into "the deploy failed" is what makes a nightly job
# unactionable at 08:00.
#
# exit 9 (do_prove RED) is the important one and the best one: do_prove runs
# AFTER the build and BEFORE the kickstart, so a 9 means the running pogod was
# never touched. It is a clean negative control, not an outage — and the correct
# response is emphatically not to retry with --force.
describe_exit() {
    case "$1" in
        0) echo "OK" ;;
        1) echo "refused before deploying (ancestry guard, unresolvable mg, or unclassifiable drift)" ;;
        2) echo "bad invocation or unusable repo path" ;;
        3) echo "refused: non-interactive without --yes (a bug in this wrapper — it must pass --yes)" ;;
        4) echo "BUILD FAILED (dirty tree, go install, or the post-install revision check)" ;;
        5) echo "launchctl kickstart failed — pogod may be DOWN" ;;
        6) echo "post-restart verification failed" ;;
        7) echo "drain stalled" ;;
        8) echo "verify_running failed — the new pogod did not come up" ;;
        9) echo "do_prove RED — the control suite refused the artifact BEFORE the restart; the running pogod is UNTOUCHED" ;;
        *) echo "unclassified failure" ;;
    esac
}

# remedy_for_exit CODE — what to DO about this exit, and why.
#
# The RED alert used to carry ONE paragraph, about exit 9, under every code
# (mg-8f7e). Under the 07-31 exit 7 it told the reader that "the control suite
# went RED before the kickstart ... the artifact is the problem" — a description
# of a different failure. Exit 7 never reaches the build, so there is no artifact
# to suspect and nothing in the build log to find. The advice that followed it
# ("read the log, fix the cause, let the next nightly carry it") was right by
# accident, and being right for a stated wrong reason is worse than being wrong:
# a reader who trusts the reasoning goes and looks at the build.
#
# So each code gets the paragraph that is true of it. describe_exit says what
# happened; this says what it means and what to do.
# dispatch_freeze_note RC ELAPSED — what the attempt cost the fleet, in seconds.
#
# `draining=true` refuses ALL new polecat dispatch, so a drain is not just a late
# bounce: it is an interval in which no new work starts for anyone. That cost was
# never reported, which made "raise the budget" look free — the architect's
# reading of mg-8f7e, and correct as far as it goes.
#
# Two things bound it, and neither is visible without a number in the log. The
# freeze ends when the fleet quiesces, NOT when the budget runs out: the 07-30
# deploy drained in 3m50s under the same 1800s budget, so a larger budget costs
# nothing on a night that would have succeeded anyway. It is only the nights that
# STALL that pay the full budget and get nothing for it — and that is exactly the
# case this prints, so the trade-off accumulates as measurements instead of
# staying an argument.
#
# Reported for exit 7 only. On every other outcome the drain reached zero and the
# elapsed time includes the build and the bounce, so quoting it as a freeze would
# overstate it.
dispatch_freeze_note() {
    local rc="$1" elapsed="$2"
    [ "$rc" -eq 7 ] || return 0
    cat <<EOF
COST: while the drain waited, pogod refused all new polecat dispatch. This
attempt froze dispatch for about ${elapsed}s and deployed nothing — when a run
ends in exit 7 the run IS essentially the drain, since every phase before it
takes seconds. A stalled night pays that in full for no activation, which is why
POGO_DEPLOY_MAX_DRAIN caps one attempt at ${MAX_DRAIN}s instead of letting it
run to the end of the window. A night that quiesces pays only until it does.
EOF
}

remedy_for_exit() {
    case "$1" in
        4)
            cat <<'EOF'
The BUILD failed, so nothing was installed and nothing was bounced — the running
pogod is untouched and the fleet is intact. The artifact is the problem and the
log holds the compiler or `go install` error verbatim. A retry cannot help: the
same commit will fail the same way. Fix main, and the next nightly carries it.
EOF
            ;;
        5)
            cat <<'EOF'
The launchctl kickstart failed AFTER a successful install, which is the one exit
here that can leave the box worse than it found it: pogod may be DOWN. Check it
first and restore service before diagnosing anything else:

  launchctl print gui/$(id -u)/com.pogo.daemon | head -20
  launchctl kickstart -k gui/$(id -u)/com.pogo.daemon
  curl -s http://127.0.0.1:10000/version
EOF
            ;;
        6|8)
            cat <<'EOF'
The new pogod was installed and started but did not verify. The binary on disk is
the NEW one while the process may be missing or unhealthy, so this is the state
to resolve by hand rather than leave for the next nightly. Confirm what is
actually running, then decide whether to kickstart again or roll the install
back:

  curl -s http://127.0.0.1:10000/version
  tail -50 ~/Library/Logs/pogo/pogod.log
EOF
            ;;
        7)
            cat <<'EOF'
The DRAIN timed out: polecats were still working when the budget ran out. Note
what this exit does NOT mean — the build never ran, so there is no artifact to
suspect and nothing in the build log to read. pogod was never replaced, and
pogo-self-deploy's exit trap has already restored dispatch (draining=false).

Nor was the fleet racing the drain: `draining=true` makes pogod refuse new
polecat dispatch, so the count only falls. Whatever was still running had been
running before the drain started.

This is therefore a statement about how long the fleet's work takes, not about
the deploy. The drain budget is derived from what remains of the deploy window
(POGO_DEPLOY_RESERVE / POGO_DEPLOY_MAX_DRAIN), so if it is being exhausted the
work genuinely outlasts it. Look at what was still running:

  curl -s http://127.0.0.1:10000/agents/drain | python3 -m json.tool
  pogo agent list

If the same long-lived polecats block it night after night, the question is
their lifetime, not the budget.
EOF
            ;;
        9)
            cat <<'EOF'
The control suite went RED BEFORE the kickstart, which means the running pogod
was never replaced — the box is in a known-good state and the artifact is the
problem. This is the best failure in the list: a clean negative control, not an
outage. Read the log, fix the cause, and let the next nightly carry it.
EOF
            ;;
        *)
            cat <<'EOF'
Read the log before acting. Where in the sequence it stopped decides whether the
running pogod was replaced, and that is the fact everything else depends on:

  curl -s http://127.0.0.1:10000/version
EOF
            ;;
    esac
}

# ---------------------------------------------------------------------------
# 8. Post-bounce mail-check verification
# ---------------------------------------------------------------------------
# pogo-self-deploy already runs this check within ~30s of the kickstart. This
# one runs after a longer grace, and that is not redundancy: on 07-17 the
# schedules were present immediately after the bounce and were reaped minutes
# later as agent_gone, leaving crew without their 10-minute mail loop while
# every health signal stayed green. A second look, later, is the only thing that
# sees that.
mail_check_ids() {
    [ -n "$POGO_CLI" ] || { echo "?"; return 1; }
    local out
    out="$("$POGO_CLI" schedule list --json 2>/dev/null)" || { echo "?"; return 1; }
    printf '%s' "$out" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\(mail-check-[^"]*\)".*/\1/p' | sort -u
}

# missing_ids PRE POST — ids present before the bounce and absent after.
missing_ids() {
    local pre="$1" post="$2"
    [ "$pre" = "?" ] || [ "$post" = "?" ] && { echo "?"; return 0; }
    comm -23 <(printf '%s\n' "$pre" | sed '/^$/d' | sort -u) \
             <(printf '%s\n' "$post" | sed '/^$/d' | sort -u)
}

# ---------------------------------------------------------------------------
# 9. Lock
# ---------------------------------------------------------------------------
# A redeploy can legitimately take an hour (the drain waits for polecats). If a
# fire lands while one is running, the second must NOT start a competing drain —
# it exits 0 and lets the first finish.
acquire_lock() {
    mkdir -p "$(dirname "$LOCK_DIR")" 2>/dev/null
    if mkdir "$LOCK_DIR" 2>/dev/null; then return 0; fi
    if find "$LOCK_DIR" -maxdepth 0 -type d -mmin +"$STALE_LOCK_MIN" 2>/dev/null | grep -q .; then
        log "lock: reclaiming a stale lock (>${STALE_LOCK_MIN}min)"
        rm -rf "$LOCK_DIR"
        mkdir "$LOCK_DIR" 2>/dev/null && return 0
    fi
    return 1
}

# on_exit RC — the single EXIT trap: record the night's outcome, then drop the
# lock. One trap rather than a record call at each of the seven exit points,
# because the exit point that gets forgotten is exactly the one that leaves the
# night looking unattempted and lets the next fire repeat a failure.
#
# The recording is conditional on ATTEMPT_ARMED, which is set only after every
# skip gate has passed. A fire that was late, locked out or already settled must
# leave the record untouched: it did not attempt anything, and writing "attempt
# 2, rc 0" for it would settle a night whose real attempt is still running.
on_exit() {
    local rc="$1"
    if $ATTEMPT_ARMED; then
        ATTEMPT_N=$(( ATTEMPT_N + 1 ))
        if stamp_write "$STAMP" "$(deploy_date)" "$ATTEMPT_N" "$rc"; then
            log "attempt recorded: $(deploy_date) attempt=$ATTEMPT_N rc=$rc ($STAMP)"
        else
            err "could not write the attempt record at $STAMP — a later fire tonight may repeat this attempt"
        fi
    fi
    rmdir "$LOCK_DIR" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --dry-run) DRY_RUN=true ;;
            # Bounded by the `set -u` sentinel, not by a line number. The header
            # is long and grows, and a hardcoded range starts truncating --help
            # mid-thought the first time anybody documents anything — the old
            # '2,80p' had already drifted and was cutting the ENV list off in
            # the middle of itself. The `s///p` prints only lines that were
            # comments, so the sentinel and the blank line before it drop out.
            -h|--help) sed -n '2,/^set -u/p' "${BASH_SOURCE[0]}" | sed -n 's/^# \{0,1\}//p'; exit 0 ;;
            *) err "unknown flag: $1"; exit 2 ;;
        esac
        shift
    done

    log "pogo-deploy: start (src=$SRC window=$WINDOW dry_run=$DRY_RUN)"

    # --- gate 1: the window -------------------------------------------------
    parse_window "$WINDOW" || { err "bad POGO_DEPLOY_WINDOW '$WINDOW' (want START-END)"; exit 2; }
    local hour; hour="$(current_hour)"
    if [ "${POGO_DEPLOY_SKIP_WINDOW:-}" = "1" ]; then
        log "window: guard BYPASSED (POGO_DEPLOY_SKIP_WINDOW=1) at hour $hour"
    elif ! in_window "$hour" "$WINDOW_START" "$WINDOW_END"; then
        log "window: local hour $hour is outside [${WINDOW_START},${WINDOW_END}) — this is a deferred/catch-up fire, NOT deploying. Exit 0."
        exit 0
    else
        log "window: local hour $hour is inside [${WINDOW_START},${WINDOW_END})"
    fi

    # --- gate 2: one at a time ----------------------------------------------
    if ! acquire_lock; then
        log "lock: another pogo-deploy run holds $LOCK_DIR — exiting 0"
        exit 0
    fi
    trap 'on_exit $?' EXIT

    # --- gate 3: has tonight already been settled? (mg-8f7e) ----------------
    # The 04:00 and 05:00 fires are retries for ONE failure — a stalled drain.
    # Every other outcome is settled by the first attempt, because re-running it
    # reproduces the failure and mails a duplicate alert.
    local today stamp_line disp
    today="$(deploy_date)"
    stamp_line="$(stamp_read "$STAMP")"
    disp="$(attempt_disposition "$today" "$stamp_line")"
    ATTEMPT_N="$(stamp_attempts "$stamp_line" "$today")"
    case "$disp" in
        first) log "attempt: first of the night ($today)" ;;
        retry) log "attempt: RETRY $(( ATTEMPT_N + 1 )) — tonight's earlier attempt exited 7 (drain stalled), the one failure a later fire can fix" ;;
        settled)
            log "attempt: tonight ($today) is already settled by attempt ${ATTEMPT_N} — its outcome was not a drain stall, so a retry would repeat the same failure and the same alert. Exit 0."
            exit 0 ;;
    esac

    # --- gate 4: is there enough window left to drain AND deploy? -----------
    local budget
    budget="$(drain_budget "$WINDOW_END" "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
    if [ "$budget" -le 0 ]; then
        log "budget: under ${MIN_DRAIN}s of usable window is left before ${WINDOW_END}:00 (reserve ${RESERVE}s) — a drain started now could not finish, and one that times out has stopped dispatch for its whole length and delivered nothing. NOT deploying. Exit 0."
        exit 0
    fi
    log "budget: drain gets up to ${budget}s (window ends ${WINDOW_END}:00, reserve ${RESERVE}s, cap ${MAX_DRAIN}s)"

    # Past every skip: this fire is really attempting tonight's deploy, so its
    # outcome is the night's outcome. A dry run never arms — it decides nothing
    # and must not settle a night it did not attempt.
    $DRY_RUN || ATTEMPT_ARMED=true

    # --- tools + credentials ------------------------------------------------
    # Resolve the alert path BEFORE anything that can fail, so a failure has
    # somewhere to be reported to. A wrapper whose first failure is "I cannot
    # tell you about failures" is the silent nightly all over again.
    resolve_mg   || { err "no alert path — refusing to run unattended"; exit 1; }
    resolve_pogo || log "pogo CLI unresolved — the post-bounce schedule check will be skipped"
    # After resolve_mg, so a git failure has somewhere to be reported; before
    # sync_src, which is the first thing that needs git.
    resolve_git || {
        alert "[pogo-deploy] ABORTED: no working git" \
"The nightly redeploy could not find a git that RUNS, so it could not sync its
checkout. Nothing was deployed and the running pogod is untouched.

One cause that presents this way is a damaged Xcode Command Line Tools install:
/usr/bin/git is the CLT shim, and when the install behind it is broken it fails
every call — 'git --version' included — with 'unable to locate xcodebuild' and
exit 71. Existence checks pass; the binary just cannot work.

  log: $HOME/Library/Logs/pogo/pogo-deploy.log
  fix: install or repair a git and confirm 'git --version' prints a version
       (xcode-select --install, or brew install git). Pin one for this job with
       GIT=/path/to/git if several are installed."
        exit 1
    }
    load_gh_token || {
        alert "[pogo-deploy] ABORTED: no GH_TOKEN" \
"The nightly redeploy could not obtain GH_TOKEN from $ZSHENV, so any gh call in
the deploy would fail unauthenticated (the mg-03ea/mg-25fb class). Nothing was
deployed and the running pogod is untouched.

  log: $HOME/Library/Logs/pogo/pogo-deploy.log
  fix: confirm 'export GH_TOKEN=' is present and readable in $ZSHENV"
        exit 1
    }

    # --- gate 5: safe sync --------------------------------------------------
    if ! sync_src; then
        alert "[pogo-deploy] ABORTED: could not sync $SRC" \
"The nightly redeploy refused to advance its dedicated checkout. Nothing was
deployed and the running pogod is untouched. Daniel's dev tree was NOT touched.

  checkout: $SRC
  log:      $HOME/Library/Logs/pogo/pogo-deploy.log
  fix:      inspect 'git -C $SRC status' — dirty or diverged aborts by design."
        exit 1
    fi

    local DEPLOY="$SRC/scripts/pogo-self-deploy"
    [ -x "$DEPLOY" ] || { err "no executable pogo-self-deploy at $DEPLOY"; exit 1; }

    # --- gate 6: drift ------------------------------------------------------
    local check_out check_rc
    check_out="$(POGO_REPO="$SRC" "$DEPLOY" check 2>&1)"; check_rc=$?
    printf '%s\n' "$check_out"
    if [ "$check_rc" -ne 0 ]; then
        alert "[pogo-deploy] ABORTED: drift check could not classify" \
"'pogo-self-deploy check' exited $check_rc, so the job cannot tell what is owed.
Nothing was deployed.

$check_out"
        exit 1
    fi
    if is_clean_verdict "$check_out"; then
        log "drift: none — running pogod and installed binaries are at $DEPLOY_REF. NOT bouncing the fleet. Exit 0."
        exit 0
    fi
    log "drift: work owed — proceeding to redeploy"

    if $DRY_RUN; then
        log "dry-run: would run '$DEPLOY redeploy --yes --drain-timeout $budget' (never --force). Stopping here."
        exit 0
    fi

    # --- snapshot before the bounce ----------------------------------------
    local pre; pre="$(mail_check_ids)"
    log "pre-bounce mail-check schedules: $(printf '%s' "$pre" | tr '\n' ' ')"

    # --- the redeploy -------------------------------------------------------
    # --yes because confirm() exits 3 without a tty. NEVER --force: the two
    # things --force overrides are killing live polecats and bouncing a fleet
    # whose idleness could not be established, and neither is a decision an
    # unattended 03:00 job gets to make.
    #
    # --drain-timeout is the window-derived budget (mg-8f7e), not the script's
    # own 1800s default. That default is right for a human running the script by
    # hand — thirty minutes is about as long as anybody waits at a terminal — and
    # wrong for the unattended run, which has the whole night and needs it.
    log "redeploy: $DEPLOY redeploy --yes --drain-timeout $budget (repo $SRC)"
    local rc=0 t0 elapsed
    t0="$(date +%s)"
    POGO_REPO="$SRC" "$DEPLOY" redeploy --yes --drain-timeout "$budget" || rc=$?
    elapsed=$(( $(date +%s) - t0 ))
    log "redeploy: exit $rc after ${elapsed}s — $(describe_exit "$rc")"

    if [ "$rc" -ne 0 ]; then
        # A stall with another fire still to come is not yet a RED. The retry is
        # the point of the extra fires, and mailing an alert per attempt would
        # make three of them the cost of having them (mg-8f7e). The event is
        # still emitted, so the digest sees every attempt; the mail waits until
        # the night has actually run out of chances.
        if retry_will_follow "$rc"; then
            local nxt; nxt="$(next_fire_hour "$(current_hour)")"
            log "redeploy: exit 7 (drain stalled) after attempt $(( ATTEMPT_N + 1 )) — the $(printf '%02d' "$nxt"):00 fire will retry. Not alerting yet."
            dispatch_freeze_note "$rc" "$elapsed"
            if [ -n "$POGO_CLI" ]; then
                "$POGO_CLI" events emit --type=deploy_nightly_retry_pending --agent=pogo-deploy \
                    --details="{\"exit\":$rc,\"attempt\":$(( ATTEMPT_N + 1 )),\"retry_hour\":$nxt,\"drain_timeout\":$budget,\"dispatch_frozen_s\":$elapsed}" >/dev/null 2>&1 || true
            fi
            exit "$rc"
        fi
        alert "[pogo-deploy] RED: nightly redeploy exited $rc" \
"The unattended nightly redeploy FAILED, and no fire is left tonight to retry it.

  exit $rc:  $(describe_exit "$rc")
  attempt:   $(( ATTEMPT_N + 1 )) tonight ($today)
  drain:     up to ${budget}s were allowed
  elapsed:   ${elapsed}s
  checkout:  $SRC ($("$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null))
  log:       $HOME/Library/Logs/pogo/pogo-deploy.log

DO NOT re-run with --force.

$(remedy_for_exit "$rc")
$(dispatch_freeze_note "$rc" "$elapsed")"
        exit "$rc"
    fi

    # --- post-bounce verification ------------------------------------------
    log "grace: waiting ${GRACE}s before re-reading schedules"
    sleep "$GRACE"
    local post lost
    post="$(mail_check_ids)"
    lost="$(missing_ids "$pre" "$post")"
    if [ "$lost" = "?" ]; then
        log "mail-check re-check: UNKNOWN (could not read schedules before or after)"
    elif [ -n "$lost" ]; then
        alert "[pogo-deploy] mail-check schedules LOST across the nightly bounce" \
"The redeploy succeeded, but ${GRACE}s later these mail-check schedules that
existed before the bounce are gone:

$lost

The fleet's mail loop is degraded and WILL LOOK HEALTHY — pogod is up, the port
answers, agents are alive. Diagnose:

  pogo schedule list | grep mail-check
  pogo agent diagnose <name>

Restore by nudging the affected agents to re-register."
    else
        log "mail-check re-check: OK — every pre-bounce schedule is back ($(printf '%s' "$post" | grep -c . ) present)"
    fi

    log "pogo-deploy: done — pogod redeployed to $("$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null)"
    exit 0
}

# Run main only when executed directly, so the test harness can source this file
# and exercise the pure helpers without firing a deploy.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
