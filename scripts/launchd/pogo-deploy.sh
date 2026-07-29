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
# HYGIENE: ABSOLUTE PATHS, NEVER BARE NAMES (mg-dd5f / mg-015f)
# -------------------------------------------------------------
# launchd hands a job a minimal PATH, and on macOS /usr/bin/mg is the Micro-Emacs
# EDITOR — bare `mg` binds to it, panics headless ("standard input and output
# must be a terminal") and delivers no alert at all. So `mg` is resolved to an
# absolute path through an IDENTITY check before use, and `pogo` /
# `pogo-self-deploy` are addressed by absolute path too. A wrapper that alerts
# via a binary it did not verify is a wrapper with no alert path.
#
# ...BUT AN ABSOLUTE PATH IS NOT A WORKING BINARY (mg-36e3)
# ---------------------------------------------------------
# The same discipline applied to `git` produced the opposite bug. git was pinned
# to /usr/bin/git on the reasoning that git ships in /usr/bin on every macOS. It
# does — but /usr/bin/git is the Command Line Tools SHIM, and a damaged or
# half-installed Xcode makes it fail EVERY invocation, `git --version` included,
# with "Error loading required libraries ... unable to locate xcodebuild" and
# exit 71. On such a box this job cannot clone, cannot fetch and cannot read a
# rev, so sync_src aborts and the nightly alerts and deploys nothing — night
# after night, which is precisely the silent-nightly failure this file exists to
# end. Observed on Daniel's box, where /usr/local/bin/git 2.40.0 was fine and
# only the shim was broken.
#
# So git is resolved like mg: candidates in order, each required to prove itself
# by actually RUNNING. Existence is not the test — the broken shim is executable,
# on PATH, and satisfies every check short of execution.
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
#   POGO_DEPLOY_WINDOW       "START-END" local hours, half-open (default "2-5")
#   POGO_DEPLOY_ZSHENV       file to read GH_TOKEN from ($HOME/.zshenv; the
#                            installed plist binds whichever init file actually
#                            defines the export)
#   GIT                      pin a specific git (still health-checked)
#   POGO_DEPLOY_GRACE        seconds to wait before the post-bounce check (120)
#   POGO_DEPLOY_LOCK_DIR     mutual-exclusion dir
#   POGO_DEPLOY_ALERT_TO     first alert recipient (pm-pogo)
#   POGO_DEPLOY_SKIP_WINDOW  set to 1 to bypass the window guard (controls only)
#   POGO_DEPLOY_NOW          "HH" override for the window guard (tests only)

set -u

HOME="${HOME:-$(cd ~ && pwd)}"
SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
BOOTSTRAP_REPO="${POGO_DEPLOY_BOOTSTRAP_REPO:-$HOME/dev/pogo}"
DEPLOY_REMOTE="${POGO_DEPLOY_REMOTE:-}"
WINDOW="${POGO_DEPLOY_WINDOW:-2-5}"
ZSHENV="${POGO_DEPLOY_ZSHENV:-$HOME/.zshenv}"
GRACE="${POGO_DEPLOY_GRACE:-120}"
LOCK_DIR="${POGO_DEPLOY_LOCK_DIR:-$HOME/.pogo/deploy.lock.d}"
ALERT_TO="${POGO_DEPLOY_ALERT_TO:-pm-pogo}"
DEPLOY_REF="${POGO_DEPLOY_REF:-main}"
STALE_LOCK_MIN="${POGO_DEPLOY_STALE_LOCK_MIN:-180}"
DRY_RUN=false

# GIT, MG and POGO_CLI are all resolved (and checked by execution) at run time.
# GIT is seeded from the environment when an operator pins one, and that value is
# still health-checked rather than trusted — a pinned path that cannot run is the
# same outage as an unpinned one.
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
# never installed. 02:00–05:00 is wide enough to catch a nearby wake and narrow
# enough that nobody is working.
#
# Half-open [START, END): hour 5 is out, so a 05:00 wake does not deploy into
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

# `git`, resolved by EXECUTION rather than by existence. See the header note: a
# broken Command Line Tools shim at /usr/bin/git is executable, is on PATH, and
# fails every call with exit 71 — so -x and `command -v` both say yes about a
# binary that cannot clone. Requiring "git version" on stdout is the cheapest
# check that the thing actually works.
#
# A real Homebrew/local git is preferred over the shim because the shim is the
# fragile one, but the shim stays in the list so a box with only CLT installed
# still deploys. An operator-pinned $GIT is tried first and health-checked too.
resolve_git() {
    local cand
    local -a cands=()
    [ -n "${GIT:-}" ] && cands+=("$GIT")
    cands+=("/opt/homebrew/bin/git" "/usr/local/bin/git" "/usr/bin/git")
    cand="$(command -v git 2>/dev/null)"
    [ -n "$cand" ] && cands+=("$cand")

    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        "$cand" --version 2>/dev/null | grep -q '^git version' || continue
        GIT="$cand"
        log "git: resolved working git at $GIT ($("$cand" --version 2>/dev/null))"
        return 0
    done
    err "git: no WORKING git among ${cands[*]} — note a present-but-broken /usr/bin/git (damaged Xcode CLT) fails every call with exit 71, so existence proves nothing"
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

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --dry-run) DRY_RUN=true ;;
            # Bounded by the `set -u` sentinel rather than a line number: the
            # header is long and grows, and a hardcoded range silently starts
            # truncating --help mid-sentence the first time somebody documents
            # something (it had already drifted past the ENV list).
            -h|--help) sed -n '2,/^set -u/p' "${BASH_SOURCE[0]}" | sed '/^set -u/d' | sed 's/^# \{0,1\}//'; exit 0 ;;
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
    trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

    # --- tools + credentials ------------------------------------------------
    # Resolve the alert path BEFORE anything that can fail, so a failure has
    # somewhere to be reported to. A wrapper whose first failure is "I cannot
    # tell you about failures" is the silent nightly all over again.
    resolve_mg   || { err "no alert path — refusing to run unattended"; exit 1; }
    resolve_pogo || log "pogo CLI unresolved — the post-bounce schedule check will be skipped"
    # After resolve_mg, so a git failure can actually be reported; before
    # sync_src, which is the first thing that needs git.
    resolve_git || {
        alert "[pogo-deploy] ABORTED: no working git" \
"The nightly redeploy could not find a git that RUNS, so it could not sync its
checkout. Nothing was deployed and the running pogod is untouched.

The usual cause is a damaged Xcode Command Line Tools install: /usr/bin/git is
the CLT shim, and when Xcode is broken it fails every call — 'git --version'
included — with 'unable to locate xcodebuild' and exit 71. Existence checks pass;
the binary just cannot work.

  log: $HOME/Library/Logs/pogo/pogo-deploy.log
  fix: install or repair a real git (xcode-select --install, or brew install git)
       and confirm 'git --version' prints a version. Pin one for this job with
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

    # --- gate 3: safe sync --------------------------------------------------
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

    # --- gate 4: drift ------------------------------------------------------
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
        log "dry-run: would run '$DEPLOY redeploy --yes' (never --force). Stopping here."
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
    log "redeploy: $DEPLOY redeploy --yes (repo $SRC)"
    local rc=0
    POGO_REPO="$SRC" "$DEPLOY" redeploy --yes || rc=$?
    log "redeploy: exit $rc — $(describe_exit "$rc")"

    if [ "$rc" -ne 0 ]; then
        alert "[pogo-deploy] RED: nightly redeploy exited $rc" \
"The unattended nightly redeploy FAILED and did not retry.

  exit $rc: $(describe_exit "$rc")
  checkout: $SRC ($("$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null))
  log:      $HOME/Library/Logs/pogo/pogo-deploy.log

DO NOT re-run with --force. On exit 9 the control suite went RED before the
kickstart, which means the running pogod was never replaced — the box is in a
known-good state and the artifact is the problem. Read the log, fix the cause,
and let the next nightly carry it."
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
