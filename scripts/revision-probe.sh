#!/usr/bin/env bash
# The EXTERNAL witness that the redeploy actually reached the daemon (mg-ce10).
#
# THE RULE THIS FILE IS THE IMPLEMENTATION OF
#
#     A detector for "X did not happen" must not be ACTIVATED BY X.
#
# mg-5bd2 added a positive staleness detector — the right fix for the right
# defect — but every line of it landed inside pogod (`cmd/pogod`,
# `internal/driftwatch`, `internal/config`). pogod is installed by the redeploy.
# So the alarm for "the redeploy did not work" is armed only by a redeploy that
# worked, and on a night the deploy fails the new alarm is dark for exactly the
# reason the old exit-code proxy was dark on 2026-08-01..08-04: the detector
# lived inside the thing whose absence it was supposed to report.
#
# This is the second instance of that shape, which is why pm-pogo ruled it a
# rule rather than a bug. mg-853a hit it and routed around it deliberately —
# "it ships in pogod, and the only thing that installs pogod is the redeploy it
# would unblock" — and went into `scripts/pogo-self-deploy` for that reason.
#
# WHY SCRIPT-SIDE ACTUALLY FIXES IT — THE ACTIVATION PATHS DIFFER
#
#   artifact                       activates on
#   ---------------------------    ------------------------------------------
#   tracked files in deploy-src    `git fetch` + `--ff-only` merge (sync_src)
#   pogod / pogo binaries          build + install — only a SUCCESSFUL deploy
#   launchd plists                 install + load
#
# A guard against deploy failure must live on the merge-activated path, never
# the build-activated one. This file is tracked, so it is present in every
# checkout at the merge commit; a `git pull` arms it, with no `go install`, no
# build and no redeploy.
#
# THIS DOES NOT REPLACE driftwatch, AND MUST NOT. Inside pogod, driftwatch
# answers "what am I running?", which is the daemon's own business and useful
# once live. This is the *external* witness, because a component cannot be the
# sole reporter of its own absence.
#
# WHAT IT DOES — TWO READS, NO BUILD
#
#     running   = curl -s localhost:10000/version | jq -r .revision
#     reference = the tip of origin/main
#     if running != reference for longer than N -> alert, naming the commit gap
#
# That is the whole point: the check needs nothing the deploy provides, so it
# does not depend on the deploy providing it. It never builds, never installs,
# never restarts anything, and never invokes `pogo` or `pogod` — see the
# controls in scripts/revision-probe_test.sh, which poison all three on PATH.
#
# THE REFERENCE READ PREFERS THE REMOTE, ON PURPOSE. The obvious `git rev-parse
# origin/main` reads a remote-tracking ref, which only a fetch refreshes — and
# in deploy-src the thing that fetches is the deploy runner. On a night the
# deploy never fires, that ref does not advance either, so a probe keyed to it
# would compare two stale numbers, find them equal, and report health. That is
# the same defect one layer down. So the reference is read with `git ls-remote`
# (read-only, no local mutation, no fetch) and falls back to the local
# remote-tracking ref only when the network read fails — saying so in the
# report, because a silent fallback to a stale ref is precisely the failure this
# file exists to remove.
#
# IT REPORTS EITHER WAY — THE LEDGER (mg-a03d)
#
# `--log FILE` appends ONE line per run, whatever the verdict, from an EXIT
# trap rather than from each terminal branch. That is not tidiness either:
#
#   - a witness that writes only when it is unhappy cannot be distinguished
#     from a witness that is not running. Silence has two causes and the
#     operator needs to tell them apart — which is this ticket's whole lineage.
#   - the trap is what makes "either way" structural. A `log_verdict` call
#     added to each `exit` is one an exit path can be added without, and the
#     paths that get forgotten are the rare ones, which are the interesting
#     ones.
#
# The ledger is therefore also a HEARTBEAT for the probe itself: its newest
# line's age answers "is the witness still firing?", which no amount of alert
# mail can.
#
# USAGE
#
#   scripts/revision-probe.sh
#   scripts/revision-probe.sh --stale-after 12h --mail
#   scripts/revision-probe.sh --url http://127.0.0.1:10000 --repo ~/.pogo/deploy-src
#   scripts/revision-probe.sh --log ~/Library/Logs/pogo/revision-probe.log --mail
#
# EXIT STATUS
#
#   0  clean — running == reference, or the divergence is younger than N
#   1  ALERT — the running revision has differed from the reference for > N
#   2  the probe could not run (no curl/git, unreadable repo, daemon silent).
#      A check that could not run has NOT found its subject healthy, so this is
#      a finding and not a shrug.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_REPO="$(cd "$HERE/.." && pwd)"

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------

URL="${POGO_SERVER_URL:-http://127.0.0.1:10000}"
REPO="$DEFAULT_REPO"
REF="main"
REMOTE="origin"
REF_SOURCE="auto"          # auto | remote | local
STALE_AFTER_RAW="${POGO_REVISION_PROBE_STALE_AFTER:-24h}"
STAMP="${POGO_REVISION_PROBE_STAMP:-$HOME/.pogo/revision-probe.stamp}"
LOG="${POGO_REVISION_PROBE_LOG:-}"
NOW_RAW=""
DO_MAIL=0
MAIL_TO="human"
# How long before the SAME unresolved alert is mailed again. The arming schedule
# is hourly (see scripts/launchd/com.pogo.revisionprobe.plist) because the
# divergence clock can only mature at the sampling rate — a daily probe first
# SEES a divergence a day after it starts, so a 24h threshold would need three
# nights of failure to fire. Hourly sampling with unthrottled mail is 24
# identical notifications a day, which is the "alarm nobody reads" this file's
# own threshold exists to prevent. So the two are set together: sample often,
# notify rarely. `--renotify 0` mails on every alerting run.
RENOTIFY_RAW="${POGO_REVISION_PROBE_RENOTIFY:-12h}"
QUIET=0
# Retries for the loopback read. pogod is restarted BY the deploy, so a single
# refused connection during the restart window is not evidence of anything; a
# refusal that survives three tries seconds apart is.
PROBE_TRIES=3
PROBE_GAP=2

usage() {
    # The header comment IS the help text. The range ends at the last line of
    # EXIT STATUS; scripts/revision-probe_test.sh asserts that `--help` still
    # reaches it, because a hard-coded line range silently truncates the moment
    # the header grows and nothing else would notice.
    sed -n '2,91p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

die_setup() {
    VERDICT="SETUP-FAILED"
    VERDICT_NOTE="$*"
    echo "revision-probe: $*" >&2
    exit 2
}

say() { [ "$QUIET" -eq 1 ] || echo "$@"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --url) URL="${2:-}"; shift 2 ;;
        --repo) REPO="${2:-}"; shift 2 ;;
        --ref) REF="${2:-}"; shift 2 ;;
        --remote) REMOTE="${2:-}"; shift 2 ;;
        --ref-source) REF_SOURCE="${2:-}"; shift 2 ;;
        --stale-after) STALE_AFTER_RAW="${2:-}"; shift 2 ;;
        --stamp) STAMP="${2:-}"; shift 2 ;;
        --log) LOG="${2:-}"; shift 2 ;;
        --renotify) RENOTIFY_RAW="${2:-}"; shift 2 ;;
        --now) NOW_RAW="${2:-}"; shift 2 ;;
        --mail) DO_MAIL=1; shift ;;
        --mail-to) MAIL_TO="${2:-}"; DO_MAIL=1; shift 2 ;;
        --tries) PROBE_TRIES="${2:-}"; shift 2 ;;
        --retry-gap) PROBE_GAP="${2:-}"; shift 2 ;;
        --quiet) QUIET=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die_setup "unknown option '$1' (try --help)" ;;
    esac
done

# ---------------------------------------------------------------------------
# The ledger — ONE line per run, whatever happened (mg-a03d)
# ---------------------------------------------------------------------------
# Installed as an EXIT trap, and installed HERE — before the first thing that
# can call die_setup — so that a probe which dies on its own setup still leaves
# a line saying it tried. The only uncovered path is an unparseable command
# line above, which cannot be logged because the log's own path comes from it.
#
# The trap must not disturb the exit status: it calls no `exit`, so bash
# preserves the status the script was leaving with.
#
# The timestamp is real wall-clock even when `--now` injects a synthetic clock
# for the age arithmetic. The two answer different questions — "when did this
# run" versus "how long has the divergence stood" — and a ledger whose
# timestamps could be back-dated by a flag is no longer a heartbeat.

VERDICT="INCOMPLETE"        # every terminal path below overwrites this
VERDICT_NOTE=""
AGE_LABEL="-"

log_verdict() {
    local rc="$1" dir
    [ -n "$LOG" ] || return 0
    dir="$(dirname "$LOG")"
    [ -d "$dir" ] || mkdir -p "$dir" 2>/dev/null
    {
        printf '%s exit=%s %-12s running=%.8s reference=%.8s age=%s threshold=%s' \
            "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$rc" "$VERDICT" \
            "${RUNNING:-<unread>}" "${REFERENCE:-<unread>}" \
            "$AGE_LABEL" "$STALE_AFTER_RAW"
        [ -z "$VERDICT_NOTE" ] || printf ' -- %s' "$VERDICT_NOTE"
        printf '\n'
    } >> "$LOG" 2>/dev/null || {
        echo "revision-probe: WARNING — could not append to the ledger $LOG. The run below still happened; nothing recorded that it did, so a reader cannot tell this probe from one that never fired." >&2
        return 0
    }
}

trap 'log_verdict "$?"' EXIT

case "$REF_SOURCE" in
    auto|remote|local) ;;
    *) die_setup "--ref-source must be auto, remote or local (got '$REF_SOURCE')" ;;
esac

# ---------------------------------------------------------------------------
# Duration and clock helpers
# ---------------------------------------------------------------------------

# parse_duration turns 90m / 24h / 2d / 3600 into seconds. A threshold the
# operator cannot express in the unit they think in gets set wrong once and then
# trusted forever.
parse_duration() {
    local raw="$1" num unit
    num="${raw%[smhd]}"
    unit="${raw#"$num"}"
    [ -n "$num" ] || return 1
    [ -z "${num//[0-9]/}" ] || return 1
    case "$unit" in
        ""|s) echo "$num" ;;
        m) echo $(( num * 60 )) ;;
        h) echo $(( num * 3600 )) ;;
        d) echo $(( num * 86400 )) ;;
        *) return 1 ;;
    esac
}

# to_epoch accepts epoch seconds or RFC3339, so --now can be written the way a
# log line writes it. Both date dialects are tried because this file must run on
# the darwin box it was written for AND under a GNU coreutils CI image.
to_epoch() {
    local ts="$1" out
    if [ -z "${ts//[0-9]/}" ]; then echo "$ts"; return 0; fi
    if out="$(date -d "$ts" +%s 2>/dev/null)" && [ -n "$out" ]; then echo "$out"; return 0; fi
    local norm="$ts"
    norm="${norm%%.*}"                       # drop fractional seconds
    if [ "${norm: -1}" = "Z" ]; then
        out="$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$norm" +%s 2>/dev/null)"
    else
        # +01:00 -> +0100, which is what %z wants.
        norm="$(echo "$norm" | sed -E 's/([+-][0-9]{2}):([0-9]{2})$/\1\2/')"
        out="$(date -j -f '%Y-%m-%dT%H:%M:%S%z' "$norm" +%s 2>/dev/null)"
    fi
    [ -n "$out" ] || return 1
    echo "$out"
}

# fmt_epoch renders an epoch as RFC3339. Both date dialects again: BSD spells it
# `-r <epoch>`, GNU spells that `-d @<epoch>` and reads `-r` as a FILE.
fmt_epoch() {
    date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
        || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
        || echo "epoch $1"
}

# format_age renders seconds as the days/hours a human reads a deploy gap in.
format_age() {
    local s="$1" d h m
    [ "$s" -ge 0 ] 2>/dev/null || s=0
    d=$(( s / 86400 )); h=$(( (s % 86400) / 3600 )); m=$(( (s % 3600) / 60 ))
    if [ "$d" -gt 0 ]; then echo "${d}d${h}h"
    elif [ "$h" -gt 0 ]; then echo "${h}h${m}m"
    else echo "${m}m"; fi
}

STALE_AFTER="$(parse_duration "$STALE_AFTER_RAW")" \
    || die_setup "--stale-after '$STALE_AFTER_RAW' is not a duration (e.g. 90m, 24h, 2d)"

RENOTIFY="$(parse_duration "$RENOTIFY_RAW")" \
    || die_setup "--renotify '$RENOTIFY_RAW' is not a duration (e.g. 90m, 12h, 0)"

if [ -n "$NOW_RAW" ]; then
    NOW="$(to_epoch "$NOW_RAW")" || die_setup "--now '$NOW_RAW' is not an epoch or RFC3339 timestamp"
else
    NOW="$(date +%s)"
fi

# ---------------------------------------------------------------------------
# Tool resolution — by EXECUTION, never by existence
# ---------------------------------------------------------------------------
# The same reasoning as pogo-deploy.sh's resolve_git: /usr/bin/git is the
# Command Line Tools shim, and a damaged CLT leaves it executable, on PATH, and
# unable to complete a single call. `-x` and `command -v` both say yes about it.
#
# NOTE WHAT IS NOT RESOLVED HERE: `go`, `pogo` and `pogod`. This probe must be
# able to run on a box where the deploy has been failing for a week, so it may
# not depend on anything the deploy installs. That is not a stylistic
# preference — it is the entire reason the file exists, and it is asserted in
# scripts/revision-probe_test.sh with all three poisoned on PATH.

resolve_git() {
    local cand
    for cand in "${GIT:-}" /opt/homebrew/bin/git /usr/local/bin/git /usr/bin/git \
                "$(command -v git 2>/dev/null)"; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        "$cand" --version 2>/dev/null | grep -q 'git version' || continue
        GIT="$cand"
        return 0
    done
    return 1
}

resolve_curl() {
    local cand
    for cand in "${CURL:-}" /usr/bin/curl /opt/homebrew/bin/curl /usr/local/bin/curl \
                "$(command -v curl 2>/dev/null)"; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        "$cand" --version 2>/dev/null | grep -q '^curl ' || continue
        CURL="$cand"
        return 0
    done
    return 1
}

resolve_git || die_setup "no working 'git' found — the reference revision cannot be read, and a probe that cannot read its reference has not found the daemon current"
resolve_curl || die_setup "no working 'curl' found — the running revision cannot be read, and a probe that cannot read the daemon has not found it current"

[ -d "$REPO/.git" ] || [ -f "$REPO/.git" ] \
    || die_setup "'$REPO' is not a git checkout — pass --repo, or keep this script inside the checkout it reads"

# ---------------------------------------------------------------------------
# Read 1 — what is RUNNING
# ---------------------------------------------------------------------------
# The revision is pulled out of the JSON with sed rather than jq. jq is not
# installed by the deploy either, but it is also not universal, and a probe that
# exits 2 on a box without jq is a probe that is dark for a reason unrelated to
# the fault it watches. The field is a flat hex string in a flat object, so the
# extraction is exact.

VERSION_URL="${URL%/}/version"
VERSION_BODY=""
CURL_RC=0
attempt=1
while :; do
    VERSION_BODY="$("$CURL" -s --max-time 5 --fail "$VERSION_URL" 2>/dev/null)"
    CURL_RC=$?
    [ "$CURL_RC" -ne 0 ] || break
    [ "$attempt" -lt "$PROBE_TRIES" ] || break
    attempt=$(( attempt + 1 ))
    sleep "$PROBE_GAP"
done

if [ "$CURL_RC" -ne 0 ]; then
    # The ledger keeps these two exit-2 states apart for the same reason the
    # prose below does: one owes a restart, the other owes an investigation.
    # Collapsing them into a single "could not run" would put the distinction
    # this file argues for in the narrative and not in the record.
    VERDICT="UNREACHABLE"
    VERDICT_NOTE="pogod did not answer $VERSION_URL after $PROBE_TRIES tries (curl exit $CURL_RC)"
    cat >&2 <<EOF
revision-probe: pogod did not answer $VERSION_URL after $PROBE_TRIES tries (curl exit $CURL_RC).

This is a FINDING, not an inconclusive result. The probe's whole subject is
whether the running daemon is current; a daemon that does not answer is not
current, it is absent. It is reported at exit 2 rather than exit 1 only because
the alarm below is about a revision, and no revision was read.

  launchctl print gui/\$(id -u)/com.pogo.daemon | head -40
  tail -50 ~/Library/Logs/pogo/pogod.log
EOF
    exit 2
fi

RUNNING="$(printf '%s' "$VERSION_BODY" \
    | sed -n 's/.*"revision"[[:space:]]*:[[:space:]]*"\([0-9a-fA-F]*\)".*/\1/p')"

if [ -z "$RUNNING" ]; then
    VERDICT="NO-REVISION"
    VERDICT_NOTE="$VERSION_URL answered but named no revision"
    cat >&2 <<EOF
revision-probe: $VERSION_URL answered but named no revision.

An unreachable daemon and a daemon that cannot say what it is are DIFFERENT
states and are kept apart here: the first owes a restart, the second owes an
investigation (a binary built with no vcs stamp reports an empty revision).

body: $VERSION_BODY
EOF
    exit 2
fi

# ---------------------------------------------------------------------------
# Read 2 — the REFERENCE
# ---------------------------------------------------------------------------

REFERENCE=""
REF_ORIGIN=""
REF_NOTE=""

read_remote_ref() {
    local out
    # GIT_TERMINAL_PROMPT=0: an unattended probe that stops to ask for a
    # password does not fail, it HANGS, and a hung probe is a silent one.
    out="$(GIT_TERMINAL_PROMPT=0 "$GIT" -C "$REPO" ls-remote "$REMOTE" "refs/heads/$REF" 2>/dev/null)" || return 1
    out="$(printf '%s' "$out" | awk 'NR==1{print $1}')"
    [ -n "$out" ] || return 1
    REFERENCE="$out"
    REF_ORIGIN="git ls-remote $REMOTE refs/heads/$REF (authoritative — read over the network, nothing fetched)"
    return 0
}

read_local_ref() {
    local out
    out="$("$GIT" -C "$REPO" rev-parse "refs/remotes/$REMOTE/$REF" 2>/dev/null)" || return 1
    [ -n "$out" ] || return 1
    REFERENCE="$out"
    REF_ORIGIN="git rev-parse $REMOTE/$REF in $REPO (LOCAL remote-tracking ref — only as fresh as the last fetch)"
    return 0
}

case "$REF_SOURCE" in
    remote)
        read_remote_ref || die_setup "could not read $REMOTE/$REF with ls-remote and --ref-source=remote forbids the local fallback"
        ;;
    local)
        read_local_ref || die_setup "no local remote-tracking ref $REMOTE/$REF in $REPO"
        ;;
    auto)
        if ! read_remote_ref; then
            read_local_ref || die_setup "could not read $REMOTE/$REF from the network OR from $REPO — the reference is unknown, so nothing here can be compared"
            REF_NOTE="ls-remote FAILED, so this is the local ref. The deploy runner is what fetches deploy-src, so on a night the deploy never fires this number does not advance either — treat an equal comparison below as unproven, not as health."
        fi
        ;;
esac

# ---------------------------------------------------------------------------
# The commit gap — context for the report, never a gate
# ---------------------------------------------------------------------------

GAP_LINE=""
have_commit() { "$GIT" -C "$REPO" cat-file -e "$1^{commit}" 2>/dev/null; }

if ! have_commit "$RUNNING"; then
    GAP_LINE="unavailable — the running revision $RUNNING is NOT AN OBJECT IN $REPO.
                     Either that checkout has not fetched it, or the daemon was built somewhere
                     else entirely. A known cause on this box: ~/.pogo is itself a git repo, so a
                     \`go build\` inside a polecat worktree stamps ~/.pogo's HEAD (mg-5bd2)."
elif ! have_commit "$REFERENCE"; then
    GAP_LINE="unavailable — the reference $REFERENCE is not in $REPO yet, which is itself a
                     finding: this checkout is also behind the remote it was compared against."
else
    n="$("$GIT" -C "$REPO" rev-list --count "$RUNNING..$REFERENCE" 2>/dev/null)"
    if [ -n "$n" ] && [ -z "${n//[0-9]/}" ]; then
        GAP_LINE="$n commit(s) between the running revision and the reference"
    else
        GAP_LINE="unavailable — rev-list could not count $RUNNING..$REFERENCE"
    fi
fi

# ---------------------------------------------------------------------------
# The clock — how LONG has it diverged?
# ---------------------------------------------------------------------------
# The stamp records when divergence was first SEEN, keyed on the running
# revision. Keying it on the running revision (and not on the reference) is what
# makes the measurement mean what it says:
#
#   - main advancing must NOT restart the clock. It advances all day; if it
#     reset the timer, a busy repo would keep the alarm permanently disarmed.
#   - the running revision CHANGING must restart it. A new binary is live, so a
#     deploy did happen — the thing this probe watches for. It may still be
#     behind a main that moved since, and that is not the failure being watched.

SINCE="$NOW"
# MAILED_AT carries the fourth stamp field: when this same unresolved alert was
# last put in front of a human. It is keyed on the running revision for exactly
# the reasons SINCE is — a new binary is a new situation and deserves a fresh
# notification, a moving reference is not.
MAILED_AT=""
stamp_dir="$(dirname "$STAMP")"
if [ -r "$STAMP" ]; then
    read -r st_since st_rev st_ref st_mailed _ < "$STAMP" 2>/dev/null
    if [ -n "${st_rev:-}" ] && [ "$st_rev" = "$RUNNING" ] \
        && [ -n "${st_since:-}" ] && [ -z "${st_since//[0-9]/}" ]; then
        SINCE="$st_since"
        if [ -n "${st_mailed:-}" ] && [ -z "${st_mailed//[0-9]/}" ]; then
            MAILED_AT="$st_mailed"
        fi
    fi
fi

write_stamp() {
    [ -d "$stamp_dir" ] || mkdir -p "$stamp_dir" 2>/dev/null
    printf '%s %s %s %s\n' "$SINCE" "$RUNNING" "$REFERENCE" "${MAILED_AT:--}" > "$STAMP" 2>/dev/null \
        || echo "revision-probe: WARNING — could not write the stamp $STAMP, so the divergence clock restarts every run and the alarm can never mature" >&2
}

AGE=$(( NOW - SINCE ))
[ "$AGE" -ge 0 ] || AGE=0
AGE_LABEL="$(format_age "$AGE")"

# ---------------------------------------------------------------------------
# Verdict
# ---------------------------------------------------------------------------

if [ "$RUNNING" = "$REFERENCE" ]; then
    # Clear the stamp: the next divergence must be timed from ITS own start, not
    # from a stale record of a divergence that has since closed. Clearing it also
    # resets the re-notify throttle, which is right — a convergence that later
    # breaks is a new alert, not a continuation of the old one.
    rm -f "$STAMP" 2>/dev/null
    VERDICT="OK"
    AGE_LABEL="-"
    say "revision-probe: OK — pogod is running $REFERENCE, which is $REMOTE/$REF."
    say "  running   $RUNNING   ($VERSION_URL)"
    say "  reference $REFERENCE   ($REF_ORIGIN)"
    [ -z "$REF_NOTE" ] || say "  NOTE      $REF_NOTE"
    exit 0
fi

write_stamp

build_report() {
    cat <<EOF
MEASURED
  running revision   $RUNNING
                     read from $VERSION_URL
  reference revision $REFERENCE
                     read from $REF_ORIGIN
  first diverged     $(fmt_epoch "$SINCE") (recorded in $STAMP)
  divergence age     $(format_age "$AGE")  (threshold $(format_age "$STALE_AFTER"))
  commit gap         $GAP_LINE
EOF
    [ -z "$REF_NOTE" ] || printf '  REFERENCE NOTE     %s\n' "$REF_NOTE"
    cat <<'EOF'

WHY THIS PROBE AND NOT pogod's OWN CHECK
pogod carries driftwatch, which reports its own revision age — and that is the
right home for the daemon's own reporting. It cannot be the whole answer,
because it is installed BY the redeploy: the alarm for "the redeploy did not
work" would be armed only by a redeploy that worked. This probe is a tracked
file, so it goes live at MERGE and is armed on a box where the deploy has been
failing for a week. That is why the two exist together.

WHAT TO DO
  tail -80 ~/Library/Logs/pogo/pogo-deploy.log   # did the job even fire?
  launchctl print gui/$(id -u)/com.pogo.deploy | head -40
  # then redeploy AND RESTART pogod — a restart alone re-launches the same binary,
  # and a `go install` alone rewrites the file under a process that keeps serving.
EOF
}

if [ "$AGE" -le "$STALE_AFTER" ]; then
    VERDICT="DIVERGED"
    say "revision-probe: DIVERGED, within threshold — pogod is not on $REMOTE/$REF, first seen $(format_age "$AGE") ago (threshold $(format_age "$STALE_AFTER"))."
    say ""
    [ "$QUIET" -eq 1 ] || build_report
    exit 0
fi

VERDICT="ALERT"
SUBJECT="pogod has not been redeployed for $(format_age "$AGE") — running $(printf '%.8s' "$RUNNING"), $REMOTE/$REF is $(printf '%.8s' "$REFERENCE")"
BODY="$(printf 'revision-probe: ALERT — the running pogod revision has differed from %s/%s for %s, which is longer than the %s threshold.\n\nThis probe is a tracked file in the checkout, so it is armed by a MERGE and not by a deploy. It reports on the deploy without depending on the deploy having worked.\n\n%s\n' \
    "$REMOTE" "$REF" "$(format_age "$AGE")" "$(format_age "$STALE_AFTER")" "$(build_report)")"

echo "revision-probe: ALERT — $SUBJECT"
say ""
say "$BODY"

# ---------------------------------------------------------------------------
# Optional: mail the alert itself
# ---------------------------------------------------------------------------
# Off by default — the exit status is the witness, and the arming schedule may
# prefer to route the output itself. When it IS asked for, the probe mails
# directly rather than leaving "and then mail human" as an instruction in a
# scheduler message, because that instruction only runs if an agent turn runs,
# and turns that never run are half of this ticket's lineage.
#
# THROTTLED, and the throttle is in the probe rather than in the schedule
# (mg-a03d). The sampling rate and the notification rate answer different
# questions and must be settable apart: the clock can only mature as fast as the
# probe samples, so the schedule wants to be frequent, while the same unchanged
# fact put in front of a human 24 times a day is an alarm that gets filtered.
# Putting the throttle in the scheduler would have forced one rate to serve both.
#
# A FAILED send does not count as a notification. The alert did not reach anyone,
# so the next run must try again rather than record the attempt and go quiet.

MAIL_SUPPRESSED=""
if [ "$DO_MAIL" -eq 1 ] && [ -n "$MAILED_AT" ] && [ "$RENOTIFY" -gt 0 ] \
    && [ $(( NOW - MAILED_AT )) -lt "$RENOTIFY" ]; then
    MAIL_SUPPRESSED="already mailed $(format_age $(( NOW - MAILED_AT ))) ago; next notification after $(format_age "$RENOTIFY") (--renotify)"
    VERDICT_NOTE="mail suppressed: $MAIL_SUPPRESSED"
    echo "revision-probe: mail SUPPRESSED — $MAIL_SUPPRESSED. The alert above and the exit status stand." >&2
    DO_MAIL=0
fi

if [ "$DO_MAIL" -eq 1 ]; then
    # /usr/bin/mg satisfies -x and `command -v mg`; it is the Micro-Emacs
    # editor. Every candidate must self-identify as macguffin before it is
    # trusted (mg-015f / mg-dd5f). `go env` is deliberately NOT consulted for
    # GOBIN/GOPATH here — this probe must run without a toolchain.
    MG=""
    for cand in "${GOBIN:-}/mg" "${GOPATH:-}/bin/mg" "$HOME/go/bin/mg" "$(command -v mg 2>/dev/null)"; do
        case "$cand" in ""|"/mg"|"/bin/mg") continue ;; esac
        [ -x "$cand" ] || continue
        "$cand" --help 2>/dev/null | grep -q 'macguffin' || continue
        MG="$cand"
        break
    done
    if [ -z "$MG" ]; then
        echo "revision-probe: --mail was asked for but no macguffin 'mg' was found — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR). The alert above is still the exit status." >&2
    else
        bf="$(mktemp)"
        printf '%s\n' "$BODY" > "$bf"
        if ! "$MG" mail send "$MAIL_TO" --from=revision-probe \
            --subject="$SUBJECT" --body-file "$bf" >/dev/null 2>&1; then
            echo "revision-probe: could not mail $MAIL_TO — the alert stands, it just did not reach anyone" >&2
        else
            MAILED_AT="$NOW"
            write_stamp
        fi
        rm -f "$bf"
    fi
fi

exit 1
