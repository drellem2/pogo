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
# THE SUBJECT IS NOT ONLY pogod (mg-e2e6)
#
# mg-a03d scoped this file to pogod, said so, and filed the general case rather
# than implying it. This is that case. Apply architect's test to what mg-a03d
# shipped — "what would this instrument report if the thing it names stopped
# entirely?" — and the narrow probe answers GREEN for a bridget reader that has
# been inert for two days, because it is not watching it.
#
# Measured twice, by hand, both times by somebody chasing something else:
#
#   mg-c2f5 / mg-8158   the running bridget started 2026-08-06 15:13; the
#                       mg-65d2 commit that changes its behaviour is dated
#                       2026-08-07 19:14. 69 lines plus a 133-line test file,
#                       merged, green, and never executed.
#   2026-08-14 06:16Z   pid 1736 (bridget) had been up 2d08h and predated two
#                       merged fixes, one of them functional (mg-18bf).
#
# Nothing reported either. `~/.pogo/bin/bridget` is a SYMLINK into ~/dev/bridget,
# so the binary on disk is always current and any check that stats the file
# reports healthy: only the running PROCESS is stale.
#
# TWO AXES, AND THE SECOND IS WEAKER. IT IS REPORTED AS THE WEAKER THING.
#
#   axis      reading                                     what it dates
#   -------   -----------------------------------------   ----------------------
#   http      GET /version -> revision, vs origin/main     the CODE the process
#                                                          loaded. Strong.
#   process   `ps` elapsed time -> start instant, vs the   the PROCESS. Weak,
#             commit dates in the checkout it runs from     and ONE-SIDED.
#
# One-sided, and this is the whole honesty of the process axis:
#
#   commits landed AFTER the process started
#       -> it cannot be executing them; its image was fixed at exec. A FINDING.
#   no commits since it started
#       -> proves NOTHING. The binary it exec'd may itself have been built from
#          an older tree, and for a subject that runs a DEPLOYED COPY the repo
#          and the copy can differ without any commit at all. The verdict word
#          for that state is NOT-DISPROVEN, never OK, and the ledger carries the
#          distinction so a reader cannot mistake one for the other.
#
# Dressing the weak reading up as a revision comparison would be this file's own
# defect one layer along: a green that means less than it looks.
#
# WHERE THE SUBJECTS COME FROM — A TRACKED REGISTRY, NOT THE PLIST
#
# scripts/revision-subjects.conf, inside --repo. It is tracked, so a new subject
# is armed by a MERGE plus the deploy runner's sync_src: no plist edit, no
# re-install, no `pogo`, no build. That is the same activation path this script
# is on, and it is why the subject list is not a set of flags in
# com.pogo.revisionprobe.plist — a job's argument vector changes only when
# somebody re-runs the installer, which is the arming gap one layer down.
#
# The existing hourly job therefore needs NO change to gain subjects, which is
# the point: a second launchd job would be a second thing to notice has stopped.
#
# AN ABSENT REGISTRY IS A FINDING, NOT A QUIET FALLBACK. Reverting to pogod-only
# is precisely the narrow state this ticket exists to end, so it is not reachable
# by a file going missing without anybody hearing about it: exit 2, with its own
# ledger line. `--subjects none` is the way to ask for the narrow scope on
# purpose, and it says so out loud when it does.
#
# USAGE
#
#   scripts/revision-probe.sh
#   scripts/revision-probe.sh --stale-after 12h --mail
#   scripts/revision-probe.sh --url http://127.0.0.1:10000 --repo ~/.pogo/deploy-src
#   scripts/revision-probe.sh --log ~/Library/Logs/pogo/revision-probe.log --mail
#   scripts/revision-probe.sh --subjects ~/.pogo/deploy-src/scripts/revision-subjects.conf
#   scripts/revision-probe.sh --subjects none     # pogod only, the mg-a03d scope
#
# EXIT STATUS
#
#   The WORST status across every subject checked, because one subject's exit 0
#   must not soften another's exit 2. A run that reached a verdict for pogod and
#   could not find bridget at all has NOT found this box healthy.
#
#   0  clean — every subject is on its reference, or is within threshold, or (on
#      the process axis) has no commit newer than its start to be behind
#   1  ALERT — a subject has been behind its code for longer than N
#   2  the probe could not run for at least one subject: no curl/git, unreadable
#      repo, daemon silent, a named process not running at all, or the subject
#      registry missing. A check that could not run has NOT found its subject
#      healthy, so this is a finding and not a shrug.
#
#   A setup failure that defeats BOTH axes (an unparseable duration, no working
#   git) still exits 2 immediately and checks nothing: that is a broken host, not
#   a subject state, and continuing would produce verdicts nothing measured.

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
# The subject registry (mg-e2e6). Empty means "the tracked one in --repo",
# resolved after the command line is parsed because --repo may still change.
# `none` asks for the mg-a03d pogod-only scope on purpose, and says so.
SUBJECTS_RAW="${POGO_REVISION_PROBE_SUBJECTS:-}"
SUBJECTS_FILE=""
# Retries for the loopback read. pogod is restarted BY the deploy, so a single
# refused connection during the restart window is not evidence of anything; a
# refusal that survives three tries seconds apart is.
PROBE_TRIES=3
PROBE_GAP=2

usage() {
    # The header comment IS the help text, and the range is now DERIVED rather
    # than written down: every line from 2 up to the first line that is not a
    # comment. The old form said `sed -n '2,91p'` and warned in place that a
    # hard-coded range "silently truncates the moment the header grows and
    # nothing else would notice" — which is exactly what mg-e2e6 did to it. A
    # bound that a later edit invalidates is not a bound; awk finds the end of
    # the block by reading it.
    awk 'NR>1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "${BASH_SOURCE[0]}"
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
        --subjects) SUBJECTS_RAW="${2:-}"; shift 2 ;;
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

# ONE LINE PER SUBJECT PER RUN, not one line per run (mg-e2e6). The pogod line
# is composed from the globals above, exactly as it always was; every other
# subject appends its own preformatted line here and the trap writes them all.
#
# Accumulating rather than appending as we go is deliberate: the trap stays the
# single writer, so the "either way" property is still STRUCTURAL and not a call
# somebody can add an exit path without. It also keeps pogod's line first, which
# is what a reader tailing the ledger is looking for.
SUBJECT_LINES=()

# POGOD_RC is pogod's OWN status, which stops being the process exit status the
# moment a second subject can raise it. The ledger must record what each subject
# found, not what the run as a whole ended up returning.
POGOD_RC=""

log_verdict() {
    local rc="$1" dir line
    [ -n "$LOG" ] || return 0
    rc="${POGOD_RC:-$rc}"
    dir="$(dirname "$LOG")"
    [ -d "$dir" ] || mkdir -p "$dir" 2>/dev/null
    {
        printf '%s exit=%s subject=%-18s %-14s running=%.8s reference=%.8s age=%s threshold=%s' \
            "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$rc" "pogod" "$VERDICT" \
            "${RUNNING:-<unread>}" "${REFERENCE:-<unread>}" \
            "$AGE_LABEL" "$STALE_AFTER_RAW"
        [ -z "$VERDICT_NOTE" ] || printf ' -- %s' "$VERDICT_NOTE"
        printf '\n'
        for line in ${SUBJECT_LINES[@]+"${SUBJECT_LINES[@]}"}; do
            printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$line"
        done
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


# ---------------------------------------------------------------------------
# THE PROCESS AXIS — the general subject (mg-e2e6)
# ---------------------------------------------------------------------------
# Everything from here to run_process_subjects implements the weaker of the two
# readings described in the header. Nothing in it is reachable unless a subject
# registry exists, so a box that has not synced one behaves exactly as mg-a03d
# shipped — except that it SAYS SO, at exit 2, rather than quietly narrowing.

SUBJECTS_WORST=0            # the worst status any non-pogod subject reached

# subject_record adds one ledger line and raises the aggregate status. The line
# shares its leading fields with pogod's (timestamp, exit, subject, verdict) so
# that a reader can grep one ledger for one verdict word; the evidence fields
# after that differ because the two axes measure different things and pretending
# otherwise is the "green that means less than it looks" this file rejects.
subject_record() {
    local rc="$1" name="$2" verdict="$3" detail="$4" note="${5:-}" line
    line="$(printf 'exit=%s subject=%-18s %-14s %s' "$rc" "$name" "$verdict" "$detail")"
    [ -z "$note" ] || line="$line -- $note"
    SUBJECT_LINES+=("$line")
    [ "$rc" -le "$SUBJECTS_WORST" ] || SUBJECTS_WORST="$rc"
}

# --- mail, factored out so both axes deliver through one code path ----------
# It was inline in the pogod alert before this ticket. Two copies of a delivery
# path is two places for the mg-7ce7 defect (a `grep -q` capability probe losing
# a SIGPIPE race and reporting a working tool as absent) to come back into,
# and only one of them would have a control on it.

MG=""
resolve_mg() {
    local cand mg_out
    [ -z "$MG" ] || return 0
    # /usr/bin/mg satisfies -x and `command -v mg`; it is the Micro-Emacs
    # editor. Every candidate must self-identify as macguffin before it is
    # trusted (mg-015f / mg-dd5f). `go env` is deliberately NOT consulted for
    # GOBIN/GOPATH here — this probe must run without a toolchain.
    #
    # THE IDENTITY CHECK HAS NO PIPE IN IT, and this is the call site that made
    # that mandatory (mg-7ce7): `mg --help | grep -q macguffin` under pipefail
    # rejected a working mg 10 times out of 10, so this branch always fell
    # through to the refusal below and 55 correctly-computed alerts reached
    # nobody. See the long note above resolve_git.
    for cand in "${GOBIN:-}/mg" "${GOPATH:-}/bin/mg" "$HOME/go/bin/mg" "$(command -v mg 2>/dev/null)"; do
        case "$cand" in ""|"/mg"|"/bin/mg") continue ;; esac
        [ -x "$cand" ] || continue
        mg_out="$("$cand" --help 2>/dev/null)"
        case "$mg_out" in *macguffin*) MG="$cand"; return 0 ;; esac
    done
    return 1
}

# send_mail SUBJECT BODY -> 0 delivered, 1 not delivered. A refusal and a failed
# send are both "not delivered", because the caller's only correct reaction to
# either is to leave the throttle unset and try again next hour.
send_mail() {
    local bf rc
    if ! resolve_mg; then
        echo "revision-probe: --mail was asked for but no macguffin 'mg' was found — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR). The alert above is still the exit status." >&2
        return 1
    fi
    bf="$(mktemp)"
    printf '%s\n' "$2" > "$bf"
    "$MG" mail send "$MAIL_TO" --from=revision-probe --subject="$1" --body-file "$bf" >/dev/null 2>&1
    rc=$?
    rm -f "$bf"
    if [ "$rc" -ne 0 ]; then
        echo "revision-probe: could not mail $MAIL_TO — the alert stands, it just did not reach anyone" >&2
        return 1
    fi
    return 0
}

# --- reading the process table ---------------------------------------------
# ps, NEVER pgrep. `pgrep` excludes the calling process AND EVERY ONE OF ITS
# ANCESTORS unless passed -a — that is man pgrep, not a quirk of this box — so a
# probe run under a supervisor it is watching would report that supervisor
# ABSENT, which this file then reads as a finding. Measured on this host
# 2026-08-20: `pgrep -x pogod` returns empty at exit 1 from a worker shell while
# pogod is serving. An instrument that answers "not running" for a running
# process is worse here than no instrument, because the whole subject is
# staleness and ABSENT is one of the verdicts.
#
# etime, not lstart. `lstart` renders a LOCALE-DEPENDENT date that then has to be
# re-parsed by a `date` whose dialect differs between darwin and GNU — two
# conversions, both of which can fail quietly and neither of which this script
# can control. `etime` is `[[dd-]hh:]mm:ss` of integers everywhere, and the start
# instant is one subtraction from a clock we already have.
#
# The table is read ONCE per run and shared by every subject, so two subjects
# cannot disagree about which processes existed.
PS_BIN=""
PS_TABLE=""
resolve_ps() {
    local cand out
    for cand in "${POGO_REVISION_PROBE_PS:-}" "$(command -v ps 2>/dev/null)" /bin/ps /usr/bin/ps; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        out="$("$cand" -Ao pid=,etime=,command= 2>/dev/null)"
        # Identity by EXECUTION, like resolve_git and resolve_curl: a ps that
        # cannot enumerate a single process on a box running this script is not
        # a ps, whatever `-x` says about it.
        [ -n "$out" ] || continue
        PS_BIN="$cand"; PS_TABLE="$out"; return 0
    done
    return 1
}

# etime_seconds turns [[dd-]hh:]mm:ss into seconds, refusing anything else. It
# returns non-zero rather than echoing 0 on a parse failure: a zero elapsed time
# puts the process start at NOW, which makes every commit older than it and
# every subject read clean. That is the exact arithmetic that made pm-riemann's
# first per-agent liveness check report UNPROVEN for seven healthy agents
# (see scripts/fleet-liveness-probe.sh); here it would fail the other way, into
# silence, which is worse.
etime_seconds() {
    local e="$1" days=0 rest a b c f
    case "$e" in
        *-*) days="${e%%-*}"; rest="${e#*-}" ;;
        *)   rest="$e" ;;
    esac
    IFS=: read -r a b c <<EOF
$rest
EOF
    if [ -z "${c:-}" ]; then c="${b:-}"; b="${a:-}"; a=0; fi
    for f in "$days" "$a" "$b" "$c"; do
        [ -n "$f" ] || return 1
        [ -z "${f//[0-9]/}" ] || return 1
    done
    echo $(( 10#$days * 86400 + 10#$a * 3600 + 10#$b * 60 + 10#$c ))
}

# match_subject_processes fills MATCH_PIDS / MATCH_STARTS for one pattern.
#
# The match is a FIXED SUBSTRING of the full command line, not a regex, so a
# pattern in the tracked registry can never become a portability question about
# whose regex dialect ran it. A pattern ending in `$` must match at the END of
# the command line: `/dev/bridget/bridget` is a prefix of
# `/dev/bridget/bridget-supervise`, and two subjects silently collapsing into
# one is a witness that reports the wrong process's age under the right name.
MATCH_PIDS=""
MATCH_STARTS=""
match_subject_processes() {
    local pattern="$1" now_real="$2" pid etime cmd el start anchored=0
    MATCH_PIDS=""; MATCH_STARTS=""
    case "$pattern" in *'$') anchored=1; pattern="${pattern%'$'}" ;; esac
    while read -r pid etime cmd; do
        [ -n "${cmd:-}" ] || continue
        [ "$pid" != "$$" ] || continue
        if [ "$anchored" -eq 1 ]; then
            case "$cmd" in *"$pattern") ;; *) continue ;; esac
        else
            case "$cmd" in *"$pattern"*) ;; *) continue ;; esac
        fi
        el="$(etime_seconds "$etime")" || continue
        start=$(( now_real - el ))
        MATCH_PIDS="$MATCH_PIDS $pid"
        MATCH_STARTS="$MATCH_STARTS $start"
    done <<EOF
$PS_TABLE
EOF
    [ -n "$MATCH_PIDS" ]
}

# same_process compares two process identities of the form p<pid>@<start>.
#
# NOT string equality, and the difference is a defect the merge gate caught that
# a standalone suite run could not. The start instant is DERIVED — `date +%s`
# minus the whole seconds `ps` reports as elapsed — so it is a rounded quantity
# and consecutive samples of the SAME unrestarted process legitimately differ by
# a second as the two truncations fall either side of a boundary. Exact equality
# on it means the throttle randomly decides the subject restarted, and the alert
# is then re-mailed on every hourly fire: the "alarm nobody reads" the throttle
# exists to prevent, arriving through the mechanism meant to prevent it.
#
# The pid carries the identity; the start is a tie-breaker with slop. A restart
# gets a new pid, so the common case is decided by the first comparison. A pid
# RECYCLED onto the same number inside the slop window would be read as the same
# process and could suppress one notification — that is the deliberate trade,
# and it is bounded at one notification, against an unbounded re-mail loop. (The
# box does recycle the whole pid space quickly, mg-cbc3, which is why the start
# is compared at all rather than the pid trusted alone.)
IDENTITY_SLOP=120

same_process() {
    local a="$1" b="$2" a_pid b_pid a_start b_start d
    [ -n "$a" ] && [ -n "$b" ] || return 1
    # Each side checked on its own: concatenating them and looking for two '@'
    # also accepts one well-formed id next to a malformed one carrying both.
    case "$a" in *@*) ;; *) return 1 ;; esac
    case "$b" in *@*) ;; *) return 1 ;; esac
    a_pid="${a%@*}"; a_pid="${a_pid#p}"; a_start="${a##*@}"
    b_pid="${b%@*}"; b_pid="${b_pid#p}"; b_start="${b##*@}"
    [ "$a_pid" = "$b_pid" ] || return 1
    [ -n "$a_start" ] && [ -z "${a_start//[0-9]/}" ] || return 1
    [ -n "$b_start" ] && [ -z "${b_start//[0-9]/}" ] || return 1
    d=$(( a_start - b_start ))
    [ "$d" -ge 0 ] || d=$(( -d ))
    [ "$d" -le "$IDENTITY_SLOP" ]
}

# --- one subject ------------------------------------------------------------

check_process_subject() {
    local name="$1" repo="$2" ref="$3" pattern="$4"
    local now_real refsha oldest_pid oldest_start nmatched st
    local newer count oldest_ct age age_label stamp
    local st_since st_id st_ref st_mailed mailed_at identity
    local subject body detail

    now_real="$(date +%s)"

    if [ ! -d "$repo/.git" ] && [ ! -f "$repo/.git" ]; then
        subject_record 2 "$name" NO-CHECKOUT "repo=$repo" \
            "'$repo' is not a git checkout, so there is no code to date this process against. A subject that cannot be measured has not been found healthy."
        return 0
    fi
    refsha="$("$GIT" -C "$repo" rev-parse --verify "$ref^{commit}" 2>/dev/null)"
    if [ -z "$refsha" ]; then
        subject_record 2 "$name" NO-REF "repo=$repo ref=$ref" \
            "'$ref' does not name a commit in $repo"
        return 0
    fi

    if ! match_subject_processes "$pattern" "$now_real"; then
        # ARCHITECT'S TEST, ANSWERED IN THE INSTRUMENT: "what would this report
        # if the thing it names stopped entirely?" A staleness check whose only
        # verdicts are {current, stale} has no cell for {gone}, and the
        # arithmetic then puts a dead subject in whichever cell it falls into.
        # Here it is its own verdict, at exit 2.
        subject_record 2 "$name" ABSENT "pattern=$pattern" \
            "no process matches — this subject is not running at all, which is not the same as running current code"
        echo "revision-probe: $name — NO PROCESS matches '$pattern'. Not stale: ABSENT." >&2
        return 0
    fi

    # The OLDEST match is the subject, not the first and not the newest. A
    # supervised program is commonly several processes — four `poll-mail.sh`
    # bash processes were live on this box when this was written — and the stale
    # one is the finding. Picking any other would let one restarted child vouch
    # for its siblings, which is how a population-level check reports the health
    # of its healthiest member.
    oldest_pid=""; oldest_start=""; nmatched=0
    # MATCH_PIDS and MATCH_STARTS are positionally paired; walk them together.
    # nmatched counts PROCESSES and count counts COMMITS — two different numbers
    # that both wanted to be called "count", which is how the report came to say
    # "2 process(es) matched" about a two-commit gap while drafting this.
    set -- $MATCH_STARTS
    for st in "$@"; do
        nmatched=$(( nmatched + 1 ))
        if [ -z "$oldest_start" ] || [ "$st" -lt "$oldest_start" ]; then
            oldest_start="$st"
            oldest_pid="$(printf '%s\n' $MATCH_PIDS | sed -n "${nmatched}p")"
        fi
    done

    newer="$("$GIT" -C "$repo" log --format='%ct %h %cs %s' --since="@$oldest_start" "$refsha" 2>/dev/null)"
    if [ -z "$newer" ]; then
        # NOT-DISPROVEN, never OK. No commit has landed since this process
        # started, so nothing here shows it stale — and nothing here shows it
        # current either: the image it exec'd may have been built from an older
        # tree, and a subject that runs a deployed COPY can differ from its repo
        # with no commit involved. The ledger word has to carry that, because a
        # word that reads as health is the thing a reader acts on.
        rm -f "${STAMP}.${name}" 2>/dev/null
        subject_record 0 "$name" NOT-DISPROVEN \
            "pid=$oldest_pid started=$(fmt_epoch "$oldest_start") ref=$(printf '%.8s' "$refsha") newer=0" \
            "no commit in $repo is newer than this process — that does NOT establish it is current"
        say "revision-probe: $name — NOT-DISPROVEN. pid $oldest_pid started $(fmt_epoch "$oldest_start"); no commit in $repo since. This does not prove it is current; the process axis can only disprove."
        return 0
    fi

    count="$(printf '%s\n' "$newer" | grep -c '^')"
    oldest_ct="$(printf '%s\n' "$newer" | tail -1 | awk '{print $1}')"
    # The clock is DERIVED, not stamped. For the http axis "first seen diverged"
    # has to be recorded because nothing else dates it; here the moment the
    # process became provably behind is a durable fact — the commit date of the
    # OLDEST commit it missed — so it is read from git every run. A stamped
    # first-seen would also silently restart whenever the stamp was lost, which
    # is the one direction a staleness clock must not fail in.
    age=$(( NOW - oldest_ct ))
    [ "$age" -ge 0 ] || age=0
    age_label="$(format_age "$age")"
    identity="p${oldest_pid}@${oldest_start}"
    stamp="${STAMP}.${name}"

    detail="pid=$oldest_pid started=$(fmt_epoch "$oldest_start") ref=$(printf '%.8s' "$refsha") newer=$count age=$age_label threshold=$STALE_AFTER_RAW"

    build_process_report() {
        cat <<EOF
MEASURED — PROCESS AXIS, WHICH IS THE WEAKER OF THE TWO
  subject            $name
  process            pid $oldest_pid, started $(fmt_epoch "$oldest_start")
                     $nmatched process(es) matched '$pattern'; the OLDEST is the subject
  checkout           $repo at $ref ($refsha)
  commits since it   $count, the oldest dated $(fmt_epoch "$oldest_ct")
$(printf '%s\n' "$newer" | head -5 | sed 's/^[0-9]* /                     /')
  behind for         $age_label  (threshold $(format_age "$STALE_AFTER"))

WHAT THIS PROVES, AND WHAT IT DOES NOT
  PROVES     the process cannot be executing those commits. Its image was fixed
             at exec and they did not exist yet.
  DOES NOT   say what revision it IS running. It dates the PROCESS, not the code
             the process loaded — a start time later than every commit would not
             have established currency either. There is no /version to ask here;
             that reading is available for pogod and for nothing else on this box.

WHAT TO DO
  ps -o pid,lstart,command -p $oldest_pid
  git -C $repo log --oneline --since=@$oldest_start $ref
  # then restart the subject — and check whether it runs the repo directly (a
  # symlink, as ~/.pogo/bin/bridget is) or a DEPLOYED COPY. For a copy, a
  # restart alone re-execs the same stale files and the finding survives it.
EOF
    }

    if [ "$age" -le "$STALE_AFTER" ]; then
        subject_record 0 "$name" BEHIND "$detail" \
            "behind $count commit(s) but only for $age_label, inside the $STALE_AFTER_RAW threshold"
        say "revision-probe: $name — BEHIND $count commit(s), within threshold ($age_label of $STALE_AFTER_RAW)."
        [ "$QUIET" -eq 1 ] || { say ""; build_process_report; }
        return 0
    fi

    mailed_at=""
    if [ -r "$stamp" ]; then
        read -r st_since st_id st_ref st_mailed _ < "$stamp" 2>/dev/null
        if same_process "${st_id:-}" "$identity" \
            && [ -n "${st_mailed:-}" ] && [ -z "${st_mailed//[0-9]/}" ]; then
            mailed_at="$st_mailed"
        fi
    fi

    subject="$name has been running $(format_age $(( NOW - oldest_start ))) and predates $count commit(s) in $repo"
    body="$(printf 'revision-probe: ALERT — %s.\n\nThis is the PROCESS axis (mg-e2e6): a `ps` start time against commit dates. It\nis weaker than the revision comparison used for pogod and is reported as the\nweaker thing — see WHAT THIS PROVES below.\n\n%s\n' \
        "$subject" "$(build_process_report)")"

    echo "revision-probe: ALERT — $subject"
    say ""
    say "$body"

    local suppressed=""
    if [ "$DO_MAIL" -eq 1 ] && [ -n "$mailed_at" ] && [ "$RENOTIFY" -gt 0 ] \
        && [ $(( NOW - mailed_at )) -lt "$RENOTIFY" ]; then
        suppressed="already mailed $(format_age $(( NOW - mailed_at ))) ago; next notification after $(format_age "$RENOTIFY") (--renotify)"
        echo "revision-probe: mail SUPPRESSED for $name — $suppressed. The alert above and the exit status stand." >&2
    elif [ "$DO_MAIL" -eq 1 ]; then
        # A FAILED send is not a notification: leave mailed_at as it was so the
        # next run tries again rather than buying twelve hours of silence for an
        # alert that reached nobody.
        if send_mail "$subject" "$body"; then mailed_at="$NOW"; fi
    fi

    [ -d "$(dirname "$stamp")" ] || mkdir -p "$(dirname "$stamp")" 2>/dev/null
    printf '%s %s %s %s\n' "$oldest_ct" "$identity" "$refsha" "${mailed_at:--}" > "$stamp" 2>/dev/null \
        || echo "revision-probe: WARNING — could not write $stamp, so $name's re-notify throttle cannot be recorded and the same alert will be mailed every run" >&2

    if [ -n "$suppressed" ]; then
        subject_record 1 "$name" STALE-PROCESS "$detail" "mail suppressed: $suppressed"
    else
        subject_record 1 "$name" STALE-PROCESS "$detail" \
            "predates $count commit(s); the oldest one it missed landed $(fmt_epoch "$oldest_ct")"
    fi
    return 0
}

# --- the registry -----------------------------------------------------------

# expand_home turns a leading ~/ or a literal $HOME into this user's home, so the
# tracked registry names paths without naming a username. The substitution is
# deliberately not `eval`: a registry line is data, and a file that arrives by
# merge into an unattended hourly job is not a place to start executing strings.
expand_home() {
    local v="$1"
    case "$v" in
        '~/'*)     v="$HOME/${v#\~/}" ;;
        '$HOME/'*) v="$HOME/${v#\$HOME/}" ;;
        '$HOME')   v="$HOME" ;;
    esac
    echo "$v"
}

run_process_subjects() {
    local file name repo ref pattern rest line lineno=0 seen=0

    case "$SUBJECTS_RAW" in
        none|off|disabled)
            say "revision-probe: process subjects DISABLED (--subjects $SUBJECTS_RAW) — this run is the mg-a03d pogod-only scope, asked for on purpose."
            return 0
            ;;
        "") file="$REPO/scripts/revision-subjects.conf" ;;
        *)  file="$SUBJECTS_RAW" ;;
    esac
    SUBJECTS_FILE="$file"

    if [ ! -r "$file" ]; then
        subject_record 2 "(registry)" NO-REGISTRY "path=$file" \
            "the subject registry is absent or unreadable, so this run watched pogod ONLY — the narrow scope mg-e2e6 exists to end. Use --subjects none to ask for it on purpose."
        echo "revision-probe: the subject registry '$file' is absent or unreadable, so this run watched pogod ONLY." >&2
        echo "  That is a FINDING, not a default. A general witness that quietly narrows itself is the failure this ticket is about." >&2
        echo "  Pass --subjects none if the narrow scope is what you want; otherwise bring $REPO forward." >&2
        return 0
    fi

    if ! resolve_ps; then
        subject_record 2 "(registry)" NO-PS "path=$file" \
            "no working 'ps' could enumerate this host, so no subject in the registry was measured"
        return 0
    fi

    while IFS= read -r line || [ -n "$line" ]; do
        lineno=$(( lineno + 1 ))
        case "$line" in ''|'#'*) continue ;; esac
        # shellcheck disable=SC2086
        read -r name repo ref pattern rest <<EOF
$line
EOF
        if [ -z "${pattern:-}" ]; then
            subject_record 2 "(registry)" BAD-LINE "line=$lineno" \
                "expected 'name repo ref pattern', got: $line"
            continue
        fi
        repo="$(expand_home "$repo")"
        pattern="$(expand_home "$pattern")"
        seen=$(( seen + 1 ))
        check_process_subject "$name" "$repo" "$ref" "$pattern"
    done < "$file"

    if [ "$seen" -eq 0 ]; then
        subject_record 2 "(registry)" EMPTY-REGISTRY "path=$file" \
            "the registry exists but names no subject, which watches nothing while looking configured"
    else
        say "revision-probe: $seen process subject(s) checked from $file"
    fi
    return 0
}

# finish is the single exit for the pogod axis. Every `exit` in the pogod path
# below goes through it, so the process subjects cannot be skipped by a branch
# somebody adds later — the same argument the ledger's EXIT trap is built on.
#
# The status returned is the WORST across all subjects. It is not pogod's,
# because a run that found pogod current and bridget absent has not found this
# box healthy, and a caller that reads only the exit code must not be told it
# has.
finish() {
    local rc="$1" worst
    POGOD_RC="$rc"
    run_process_subjects
    worst="$rc"
    [ "$SUBJECTS_WORST" -le "$worst" ] || worst="$SUBJECTS_WORST"
    exit "$worst"
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
#
# AND NOTE HOW EVERY IDENTITY CHECK BELOW IS SPELLED (mg-7ce7). Output is
# captured into a variable and matched with `case`. It is NOT
# `"$cand" --version | grep -q ...`, and the difference is not a style
# preference either — it is the defect that made this file's `--mail` path
# undeliverable for 55 consecutive runs, mid-incident, with no code change:
#
#     `grep -q` exits on the FIRST match. The producer, still writing, takes
#     SIGPIPE and exits 141. `set -uo pipefail` (line 93) makes 141 the
#     PIPELINE's status, `|| continue` fires, and A CAPABILITY PROBE REPORTS A
#     WORKING TOOL AS ABSENT — silently, and in the safe-looking direction.
#
# Whether it loses is a RACE between how much the producer still has to write
# and how soon the consumer closes the pipe. Measured on this host, 10 runs each
# under `set -o pipefail`, at the three call sites this file used to have:
#
#     git  --version | grep -q 'git version'    0/10 fail   ~25 bytes, small binary
#     curl --version | grep -q '^curl '         0/10 fail   ~25 bytes, small binary
#     mg   --help    | grep -q 'macguffin'     10/10 fail   2404 bytes, 7.7MB Go binary
#
# So only ONE of the three was failing, and the other two were the identical
# defect winning on timing luck — one grown binary, one loaded box or one slower
# disk from reporting "no working git found" on the probe whose whole job is to
# read a git revision. All three are fixed here, not just the one that was red.
# Section 13 of scripts/revision-probe_test.sh guards all three the way a race
# has to be guarded: it FORCES THE LOSING SIDE with a deliberately chatty
# producer and asserts the old idiom really does get 141 against that fixture
# BEFORE asserting these resolvers survive it. One passing run of a race is a
# coin landing the right way, not a verification — and that is precisely how
# this file's own suite stayed green over the idiom, with a stub mg that echoes
# one short line and never loses the race the real binary loses every time.
#
# AND THE TRIAGE RULE THIS TICKET SHIPPED IS WRONG WHERE IT MATTERS MOST — it
# said a shell BUILTIN producer is safe at any size, so `printf | grep -q` sites
# need not be looked at. Measured here, bash 3.2 and zsh alike, match at byte 0,
# 20 runs each: builtin `printf` into `grep -q` is 0/20 at 8KB and 20/20 SIGPIPE
# 141 at 64KB and at 256KB. The predicate is the PIPE BUFFER, not the producer's
# class: whoever is writing dies if bytes remain when the consumer exits. See
# CONTRIBUTING.md, "`cmd | grep -q` under `set -o pipefail` is a race".

resolve_git() {
    local cand out
    for cand in "${GIT:-}" /opt/homebrew/bin/git /usr/local/bin/git /usr/bin/git \
                "$(command -v git 2>/dev/null)"; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        out="$("$cand" --version 2>/dev/null)"
        case "$out" in *'git version'*) GIT="$cand"; return 0 ;; esac
    done
    return 1
}

resolve_curl() {
    local cand out
    for cand in "${CURL:-}" /usr/bin/curl /opt/homebrew/bin/curl /usr/local/bin/curl \
                "$(command -v curl 2>/dev/null)"; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        out="$("$cand" --version 2>/dev/null)"
        # `^curl ` was an anchored grep; the equivalent without a pipeline is a
        # prefix pattern on the captured output.
        case "$out" in 'curl '*) CURL="$cand"; return 0 ;; esac
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
    finish 2
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
    finish 2
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
    finish 0
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
    finish 0
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

# resolve_mg and send_mail live with the process axis above, so that both axes
# deliver through ONE path. Two copies of a delivery path is two places for the
# mg-7ce7 defect to come back into, and only one of them would have a control.
if [ "$DO_MAIL" -eq 1 ]; then
    if send_mail "$SUBJECT" "$BODY"; then
        MAILED_AT="$NOW"
        write_stamp
    fi
fi

finish 1
