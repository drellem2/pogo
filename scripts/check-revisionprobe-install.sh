#!/usr/bin/env bash
# AUDIT com.pogo.revisionprobe — the fourth launchd job, which the Go registry
# deliberately does not carry (mg-e2e6).
#
# WHY THIS IS A SHELL SCRIPT AND NOT A ROW IN internal/service/launchagentaudit.go
#
# managedLaunchAgents() says in its own comment that a fourth launchd job means a
# row there, and then explains at length why com.pogo.revisionprobe is not one:
#
#   - a row needs a Go copy of the plist to render against, which is exactly the
#     mirror mg-b201 was filed for (the shipped plist and the installed one
#     drifted, and only a dedicated test noticed);
#   - and it would put the auditor for the DEPLOY WITNESS inside the binary the
#     deploy installs — so the audit for "the witness is not armed" would arrive
#     only by a working deploy, which is the shape the witness exists to escape.
#
# Both objections are about WHERE the auditor lives, not about whether the audit
# is worth doing. The omission was commented in place and honest, and it still
# left an audit that does not happen. This file is that audit on the
# merge-activated path: a tracked script, rendering from the tracked template
# through the tracked installer, invoking no `go`, no `pogo` and no `pogod`.
#
# WHAT IT CHECKS — FOUR SEPARATE QUESTIONS, REPORTED SEPARATELY
#
#   PLIST    does the installed plist match what this checkout's installer would
#            write? Byte equality, the same predicate the Go registry uses.
#   LOADED   does launchd actually know the label? A byte-perfect plist that was
#            never bootstrapped is a job that exists as a file and fires never —
#            "present by existence, absent by effect", which is the phrase this
#            whole lineage turns on.
#   BODY     is the tracked probe at --src current with origin/main? The plist
#            points at deploy-src on purpose, and deploy-src is advanced by the
#            deploy runner's sync_src. A deploy that stops firing does not stop
#            this job firing — it freezes the probe's TEXT. That is a real and
#            invisible way for a witness to age, and it is the one mg-30f8 found
#            on three other jobs (a runner three weeks and 1191 lines stale under
#            a plist every check called `ok`).
#   LEDGER   how old is the newest line in the probe's ledger? The probe writes
#            one line per subject per run whatever the verdict, precisely so that
#            "no alert" and "no probe" can be told apart — and nothing on this
#            box performs that read. A witness that stopped is silent in exactly
#            the way a healthy one is.
#
# They are four rows and not one score for the mg-30f8 reason: "3 of 4 clean" over
# different subjects is a sentence nobody can act on.
#
# WHAT IT DOES NOT DO, said plainly because an un-run check is this ticket's own
# defect class: NOTHING SCHEDULES THIS. It is not wired to launchd, and it must
# not be wired to com.pogo.revisionprobe — a detector for "this job is not armed"
# activated by that job is the anti-pattern scripts/revision-probe.sh is the
# implementation of. It is a control for an operator, an agent, or a future
# scheduled caller that is independent of both the deploy and the probe. Its
# fixture controls live in scripts/install-revision-probe_test.sh and DO run on
# every merge; those prove the checker works, not that this box is clean.
#
# USAGE
#
#   scripts/check-revisionprobe-install.sh
#   scripts/check-revisionprobe-install.sh --src ~/.pogo/deploy-src
#   scripts/check-revisionprobe-install.sh --launch-agents-dir DIR --launchctl BIN
#
# EXIT STATUS
#
#   0  every row clean
#   1  at least one FINDING — a row is stale, absent, unloaded or silent
#   2  the audit could not be performed, which is not a pass

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LABEL="com.pogo.revisionprobe"
INSTALLER="$HERE/install-revision-probe.sh"

SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
LA_DIR="${POGO_LAUNCHAGENTS_DIR:-$HOME/Library/LaunchAgents}"
LAUNCHCTL="${POGO_LAUNCHCTL:-}"
LEDGER="${POGO_REVISION_PROBE_LOG:-$HOME/Library/Logs/pogo/revision-probe.log}"
# The job fires hourly. Two missed fires is the point at which "quiet" stops
# being indistinguishable from "the host slept" — launchd defers a sleep-missed
# fire and delivers one run on wake, so a single gap is expected and two is not.
LEDGER_STALE_AFTER=7200
NOW_RAW=""
URL="${POGO_SERVER_URL:-http://127.0.0.1:10000}"
STALE_AFTER="24h"

die() { echo "check-revisionprobe-install: $*" >&2; exit 2; }
usage() { awk 'NR>1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "${BASH_SOURCE[0]}"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --src) SRC="${2:-}"; shift 2 ;;
        --url) URL="${2:-}"; shift 2 ;;
        --stale-after) STALE_AFTER="${2:-}"; shift 2 ;;
        --launch-agents-dir) LA_DIR="${2:-}"; shift 2 ;;
        --launchctl) LAUNCHCTL="${2:-}"; shift 2 ;;
        --ledger) LEDGER="${2:-}"; shift 2 ;;
        --ledger-stale-after) LEDGER_STALE_AFTER="${2:-}"; shift 2 ;;
        --now) NOW_RAW="${2:-}"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option '$1' (try --help)" ;;
    esac
done

PLIST="$LA_DIR/$LABEL.plist"
FINDINGS=0
UNCHECKED=0

row() {   # state text
    printf '  %-9s %s\n' "$1" "$2"
    case "$1" in
        FINDING)   FINDINGS=$(( FINDINGS + 1 )) ;;
        UNCHECKED) UNCHECKED=$(( UNCHECKED + 1 )) ;;
    esac
}

if [ -n "$NOW_RAW" ]; then NOW="$NOW_RAW"; else NOW="$(date +%s)"; fi

echo "check-revisionprobe-install: auditing $LABEL"
echo "  installer  $INSTALLER"
echo "  plist      $PLIST"
echo "  src        $SRC"
echo

# ---------------------------------------------------------------------------
# PLIST — installed vs what this checkout's installer would write
# ---------------------------------------------------------------------------
# Rendered by ASKING THE INSTALLER, not by re-implementing its substitutions
# here. A checker with its own copy of the render is a second mirror, and the
# mirror is the defect class this file's own header cites as the reason the Go
# registry has no row.
#
# --dry-run prints three `# would ...` lines before the XML; the plist starts at
# the first `<?xml`. Cutting on that marker rather than on a line count keeps
# this working when the preamble changes.

RENDER_ARGS=(--dry-run --src "$SRC" --url "$URL" --stale-after "$STALE_AFTER"
             --launch-agents-dir "$LA_DIR")
# Built as an array, not interpolated: `${LAUNCHCTL:+--launchctl $LAUNCHCTL}`
# word-splits on any path with a space in it, and the failure would be a
# mis-rendered command line rather than an error anybody sees.
[ -z "$LAUNCHCTL" ] || RENDER_ARGS+=(--launchctl "$LAUNCHCTL")

if [ ! -x "$INSTALLER" ]; then
    row UNCHECKED "PLIST — $INSTALLER is missing or not executable, so nothing rendered the expected bytes"
elif ! DRY="$("$INSTALLER" "${RENDER_ARGS[@]}" 2>&1)"; then
    row UNCHECKED "PLIST — the installer refused to render (exit non-zero). Its refusal is the message:"
    printf '%s\n' "$DRY" | sed 's/^/               /'
else
    EXPECTED="$(printf '%s\n' "$DRY" | sed -n '/<?xml/,$p')"
    if [ -z "$EXPECTED" ]; then
        row UNCHECKED "PLIST — the installer's dry run printed no XML, so there is nothing to compare against"
    elif [ ! -f "$PLIST" ]; then
        row FINDING "PLIST — NOT INSTALLED: no plist at $PLIST. The witness is not armed. Run: $INSTALLER"
    elif [ "$EXPECTED" = "$(cat "$PLIST")" ]; then
        row OK "PLIST — the installed plist is byte-identical to what this checkout would write"
    else
        row FINDING "PLIST — the installed plist DIFFERS from what this checkout would write. Diff (installed -> expected):"
        diff <(cat "$PLIST") <(printf '%s\n' "$EXPECTED") 2>/dev/null | head -30 | sed 's/^/               /'
        printf '               %s\n' "re-run: $INSTALLER"
    fi
fi

# ---------------------------------------------------------------------------
# LOADED — a plist on disk is a file, not a job
# ---------------------------------------------------------------------------

resolve_launchctl() {
    local cand
    for cand in "$LAUNCHCTL" /bin/launchctl /usr/bin/launchctl "$(command -v launchctl 2>/dev/null)"; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        LAUNCHCTL="$cand"; return 0
    done
    return 1
}

if ! resolve_launchctl; then
    row UNCHECKED "LOADED — no launchctl on this host; whether the job is in the domain is unknown"
elif "$LAUNCHCTL" print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
    row OK "LOADED — launchd knows $LABEL in gui/$(id -u)"
else
    row FINDING "LOADED — launchd does NOT know $LABEL. The plist may be perfect; the job fires never. Run: $INSTALLER"
fi

# ---------------------------------------------------------------------------
# BODY — the probe the job actually executes
# ---------------------------------------------------------------------------
# The job's program is the TRACKED script at $SRC, never a copy, which removes
# the mg-30f8 payload-copy drift class outright. What it does not remove is
# $SRC itself falling behind: sync_src is the deploy runner's, so a deploy that
# stops firing freezes the probe's text at the last sync while the job keeps
# firing. The reference is read with ls-remote for the reason the probe reads it
# that way — a remote-tracking ref is only as fresh as the last fetch, and the
# thing that fetches here is the deploy being audited.

PROBE="$SRC/scripts/revision-probe.sh"
if [ ! -f "$PROBE" ]; then
    row FINDING "BODY — $PROBE does not exist. The job names a program that is not there; it fails every hour and reports nothing."
elif [ ! -d "$SRC/.git" ] && [ ! -f "$SRC/.git" ]; then
    row UNCHECKED "BODY — $SRC is not a git checkout, so the probe's text cannot be dated"
else
    REMOTE_TIP="$(GIT_TERMINAL_PROMPT=0 git -C "$SRC" ls-remote origin refs/heads/main 2>/dev/null | awk 'NR==1{print $1}')"
    LOCAL_TIP="$(git -C "$SRC" rev-parse HEAD 2>/dev/null)"
    if [ -z "$REMOTE_TIP" ] || [ -z "$LOCAL_TIP" ]; then
        row UNCHECKED "BODY — could not read both revisions (local='$LOCAL_TIP' remote='$REMOTE_TIP')"
    elif [ "$REMOTE_TIP" = "$LOCAL_TIP" ]; then
        row OK "BODY — $SRC is at origin/main ($(printf '%.8s' "$LOCAL_TIP")); the job runs current probe text"
    else
        # A NEGATIVE COUNT NEEDS ITS OWN SENTENCE. rev-list cannot count
        # LOCAL..REMOTE when $SRC has never fetched the remote tip — the object
        # is simply not there — and it fails by printing nothing, at which point
        # "an unknown number of commits behind" reads as an instrument fault
        # rather than as what it is: the checkout is behind by at least a fetch.
        # Measured on this box 2026-09-08, which is how the two got told apart.
        if git -C "$SRC" cat-file -e "$REMOTE_TIP^{commit}" 2>/dev/null; then
            BEHIND="$(git -C "$SRC" rev-list --count "$LOCAL_TIP..$REMOTE_TIP" 2>/dev/null)"
            case "${BEHIND:-x}" in ''|*[!0-9]*) BEHIND="an uncountable number of" ;; esac
            BEHIND_NOTE="$BEHIND commit(s) behind origin/main"
        else
            BEHIND_NOTE="behind origin/main by at least a fetch — the remote tip $(printf '%.8s' "$REMOTE_TIP") is not even an object in this checkout yet, so the gap cannot be counted from here"
        fi
        row FINDING "BODY — $SRC is $BEHIND_NOTE. The job still FIRES; the probe text it fires is stale, so any fix merged into the probe since is not running. Run: git -C $SRC fetch origin && git -C $SRC merge --ff-only origin/main"
    fi
fi

# ---------------------------------------------------------------------------
# LEDGER — is the witness still firing?
# ---------------------------------------------------------------------------
# This is the read the probe's own header says only a reader can perform, and
# which no scheduled thing on this box performs. Age of the NEWEST line, against
# an hourly schedule.

if [ ! -r "$LEDGER" ]; then
    row FINDING "LEDGER — $LEDGER is absent or unreadable. The job may be loaded and firing; nothing records that it is, so a stopped witness and a healthy one look the same."
else
    LAST_TS="$(tail -1 "$LEDGER" 2>/dev/null | awk '{print $1}')"
    LAST_EPOCH=""
    case "$LAST_TS" in
        [0-9][0-9][0-9][0-9]-*)
            LAST_EPOCH="$(date -u -d "$LAST_TS" +%s 2>/dev/null)" \
                || LAST_EPOCH="$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$LAST_TS" +%s 2>/dev/null)"
            ;;
    esac
    if [ -z "$LAST_EPOCH" ]; then
        row UNCHECKED "LEDGER — the newest line does not start with a timestamp this can parse: '$LAST_TS'"
    else
        AGE=$(( NOW - LAST_EPOCH ))
        [ "$AGE" -ge 0 ] || AGE=0
        if [ "$AGE" -le "$LEDGER_STALE_AFTER" ]; then
            row OK "LEDGER — newest line is $(( AGE / 60 ))m old ($LAST_TS); the witness is firing"
        else
            row FINDING "LEDGER — newest line is $(( AGE / 3600 ))h old ($LAST_TS), past the $(( LEDGER_STALE_AFTER / 3600 ))h allowance for an hourly job. The witness has stopped, and a stopped witness raises no alarm about itself. Check: $LAUNCHCTL print gui/$(id -u)/$LABEL"
        fi
    fi
fi

echo
if [ "$FINDINGS" -gt 0 ]; then
    echo "check-revisionprobe-install: $FINDINGS FINDING(S), $UNCHECKED row(s) not checked"
    exit 1
fi
if [ "$UNCHECKED" -gt 0 ]; then
    # NOT a pass. A row nothing could evaluate has not reported its subject
    # healthy, and collapsing it into 0 is the absence-of-evidence-as-evidence
    # defect this repo has now paid for several times over.
    echo "check-revisionprobe-install: no findings, but $UNCHECKED row(s) could NOT BE CHECKED — this is not a clean audit"
    exit 2
fi
echo "check-revisionprobe-install: all rows clean — $LABEL is installed, loaded, current and firing"
exit 0
