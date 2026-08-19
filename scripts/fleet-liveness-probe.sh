#!/usr/bin/env bash
# The EXTERNAL witness that the FLEET is still completing turns (mg-f867).
#
# THE RULE THIS FILE IS THE IMPLEMENTATION OF
#
#     A detector hosted INSIDE the population it watches cannot report that
#     population failing.
#
# It is the population-level sibling of scripts/revision-probe.sh's rule ("a
# detector for X did not happen must not be ACTIVATED BY X"), and mg-f867
# recorded three independent instances of it from ONE incident:
#
#   1. deploy-verify §0 asks exactly the right question — "would this read green
#      over a fleet that is present and doing nothing?" — and its completed-turn
#      test would have caught the 2026-08-14 outage on the morning of 08-15. It
#      never ran, because it is `deploy-verify-architect`, one of architect's own
#      schedules, and architect was one of the seven agents that stopped.
#   2. ack-watch's escalation names `mayor` as a recipient while `mayor` is in
#      its own `blackout_agents` set. During the event the escalation exists for,
#      the non-human recipient is void by construction.
#   3. Every fleet-wide scheduled check on this box routes through the
#      coordinator, so every one of them inherits the same void.
#
# Three instances in three independently-built components is what makes this
# structural rather than a bug in one detector. A worker who fixed only
# deploy-verify §0 would have left 2 and 3 live and believed the class closed.
#
# WHAT IT DOES — ONE stat AND A SUBTRACTION, AND THAT IS THE CORRECTNESS ARGUMENT
#
#     newest = the most recent mtime across $POGO_HOME/agents/turnlog/*.log
#     if now - newest > threshold  ->  ALERT: no agent ANYWHERE completed a turn
#
# NEWEST-ACROSS-ALL, never per-agent. Per-agent staleness is noisy and has
# already produced false alarms — an idle PM legitimately goes hours between
# turns (heartbeat-stall-false-positive-idle-pms), and this box carries a
# turnlog (`a270.log`) that has been untouched since 2026-08-11 by design. But
# if the MOST RECENT turn by ANY agent is old, every agent is down at once,
# which has now happened twice (2026-08-11, 22h; 2026-08-14, ~118h) and is never
# benign.
#
# The fleet form was also chosen over the per-agent form on a CORRECTNESS
# argument, not a simplicity one, and it was architect's own spec that got this
# wrong first. pm-riemann implemented the per-agent form (turnlog line vs that
# agent's process start) and its first version reported UNPROVEN for all seven
# agents including ones that were plainly fine: it read uptime from the wrong
# awk field, the parse yielded zero, a zero uptime puts process_start at NOW, so
# every turnlog line predated it and everything read as dead. A launchd observer
# mailing on that would page the fleet over a format change in `pogo agent list`.
#
# This form PARSES NOTHING. No uptime, no pid, no field extraction, no
# dependency on any CLI's output format — so it is structurally incapable of
# that failure. Keep the per-agent form as a diagnostic a human runs AFTER this
# alarm fires (`pogo check-turns`), not as the thing that decides whether to page.
#
# THREE CELLS, NOT TWO — AND A FAILED MEASUREMENT MUST NOT FAIL TOWARD ALARM
#
# A two-valued instrument — {alive, dead} — has nowhere to put "I could not
# measure this", so the failure lands in whichever cell the arithmetic happens to
# produce. That is the mirror of a-binary-detector-has-no-cell-for-hung, where
# {ran, did-not-run} scored a hung run as healthy. Opposite polarity, same defect;
# the polarity is not the point, THE MISSING THIRD CELL IS.
#
#     OK             the newest turn is younger than the threshold
#     FLEET-STOP     it is older, and that is a fleet-wide stop
#     UNMEASURABLE   this probe could not measure it, and says which row and why
#
# UNMEASURABLE is never folded into either verdict. It has its own exit status,
# its own ledger word and its own mail subject, because "the fleet is down" and
# "I cannot see the fleet" owe different responses.
#
# A future-dated mtime is UNMEASURABLE, not OK, and that case is here on purpose:
# it is the one way this predicate could fail toward GREEN (a negative age reads
# as fresh), which is the exact silence this whole ticket is about.
#
# IT MUST PROVE IT CAN DELIVER, NOT MERELY THAT IT CAN DETECT (mg-7ce7)
#
# This file's design was specced as "modelled on com.pogo.revisionprobe, which
# works as an external witness because it notifies without depending on an agent
# turn running". THAT ENDORSEMENT IS KEPT — and the reason it is worth keeping is
# not the reason the spec gave.
#
# The first correction said revision-probe's --mail path was unreachable and had
# never delivered. Architect WITHDREW that by name on 2026-08-19 at 07:55Z, and
# the mail record is the authority:
#
#     2026-08-16T17:20Z   "not been redeployed for 2d12h"   DELIVERED to human
#     2026-08-17T06:20Z   "... 3d1h"                        DELIVERED to human
#     2026-08-17T23:20Z   "... 3d18h"                       DELIVERED to human
#     <nothing after>
#
# So the pattern is NOT refuted. It delivered exactly as designed, three times,
# and then STOPPED SILENTLY, mid-incident, with no code change. Keep the pattern;
# fix the idiom.
#
# THE IDIOM IS A RACE, AND THAT IS THE WHOLE LESSON. `set -uo pipefail` plus
# `"$cand" --help | grep -q macguffin`: grep exits on the first match, the
# producer takes SIGPIPE and exits 141, pipefail surfaces the 141, and the
# capability probe reports a working tool as ABSENT. Whether it loses depends on
# whether the producer finishes writing before the consumer closes the pipe.
# Measured across revision-probe's three call sites:
#
#     git  --version | grep -q 'git version'    0/10 fail   ~25 bytes, small binary
#     curl --version | grep -q '^curl '         0/10 fail   ~25 bytes, small binary
#     mg   --help    | grep -q 'macguffin'     10/10 fail   2404 bytes, 7.7MB Go binary
#
# Two of those three are the identical bug currently WINNING the race, and they
# flip the day a binary grows. Nothing changed in the code between 08-17T23:20Z
# and the silence that followed.
#
# The corrected story is MORE alarming than the withdrawn one, not less. A
# component that never worked is caught the first time anyone looks at it. A
# component that worked, was cited as precedent on the strength of having worked,
# and then stopped, is caught by nobody: the endorsement was TRUE when it was
# made and decayed silently during the incident it existed to witness. That is
# an-endorsement-is-a-claim compounded by world-state-claims-decay.
#
# So three requirements land on this file, and they are load-bearing:
#
#   1. NO `set -o pipefail` AROUND `CMD | grep -q`, AND EVERY CALL SITE AUDITED,
#      not just the one that was failing. This file has exactly one capability
#      probe (resolve_mg) and it contains no pipe at all — output captured into a
#      variable, matched with `case` — so there is no producer to die and no
#      second site to be latent. Section 8 of
#      scripts/fleet-liveness-probe_test.sh guards it, and guards it the way a
#      RACE has to be guarded: it FORCES THE LOSING SIDE with a deliberately
#      chatty producer, and asserts the old idiom really does get 141 against
#      that fixture BEFORE asserting this resolution survives it. One passing run
#      of a race is not a verification, it is a coin landing the right way.
#   2. A POSITIVE CONTROL ON THE DELIVERY PATH, on a cadence — send a known
#      self-test message and confirm it ARRIVES. Detection and delivery are
#      separate halves and only the first is exercised by ordinary operation: a
#      detector that never fires never tests its own notification path, so the
#      path rots unobserved and is discovered during the incident it exists for.
#      The cadence is the point, and it is the corrected story that demands it: a
#      path can pass today and stop tomorrow with nothing having changed, so a
#      one-off check at install time proves only that it worked once.
#   3. LOG THE SEND RESULT, NOT THE SEND ATTEMPT. The ledger's mail= field
#      distinguishes computed / attempted / delivered, where "delivered" means
#      the message was READ BACK OUT of the recipient's mailbox. Refusal lines in
#      a report log are computed alerts, and a record that cannot separate those
#      from delivered ones is what let "computed" be read as "never worked" —
#      the inference that had to be withdrawn.
#
# MAIL, NEVER NUDGE, AND THAT WAS MEASURED DURING THE OUTAGE
#
#   - mayor never goes idle, so nudges are dropped on the idle gate ("still
#     producing output after 30s — busy or stuck redrawing"); nudge_suppressed
#     _consecutive reached 7. Continuous PTY output makes an agent look busiest
#     exactly when it is least reachable.
#   - pa was woken once ~106h ago, emitted zero PTY output, and wake_silence_once
#     then stopped trying — 143 suppressed nudges over 106h with the age climbing
#     and never resetting (mg-3a8a). The rule that avoids spamming a silent agent
#     is what guarantees a silent agent is never woken again.
#
# Architect recovered because its mail-check fell back from nudge to MAIL, and
# mail reached it. Mail is the only wake channel that survived both suppression
# rules. This probe therefore mails, and it mails a HUMAN, because during a
# fleet-wide stop there is by definition no in-fleet actor left to act.
#
# WHAT THIS DOES NOT CLOSE — stated so nobody inherits a wrong premise
#
#   - A host that is POWERED OFF misses the fire outright; launchd defers across
#     sleep, not across shutdown. The ledger is how that silence is visible after
#     the fact.
#   - Nothing here watches THIS probe. Its ledger is its heartbeat and the
#     self-test mail is a second one, but both need a reader. That is the same
#     class one level out and it is honestly open, not quietly closed.
#   - It is REPORT-ONLY. It never nudges, restarts, stops or spawns anything. A
#     stale turnlog has two causes that look identical and take opposite
#     responses — a wedged session (restart is right) and an agent failing every
#     turn in ~10ms on an expired credential (restart destroys the transcript that
#     diagnoses it, and the replacement inherits the credential).
#   - internal/turnwatch is pogod-resident and reads the same artifact. It is NOT
#     redundant with this and neither replaces the other: turnwatch covers FLEET
#     DOWN, POGOD UP with minutes of latency; it wedges with its host, and launchd
#     restarts on exit only. This one covers POGOD WEDGED, which is the cell
#     turnwatch's own header says it does not close.
#
# USAGE
#
#   scripts/fleet-liveness-probe.sh
#   scripts/fleet-liveness-probe.sh --stale-after 2h --mail
#   scripts/fleet-liveness-probe.sh --turnlog-dir ~/.pogo/agents/turnlog --quiet
#   scripts/fleet-liveness-probe.sh --self-test          # force the delivery control now
#   scripts/fleet-liveness-probe.sh --self-test-only     # ONLY the delivery control, no fleet verdict
#
# EXIT STATUS
#
#   0  OK — some agent completed a turn within the threshold
#   1  ALERT — no agent completed a turn in the threshold window (fleet stop)
#   2  UNMEASURABLE — this probe could not measure the fleet, or could not run.
#      A check that could not run has NOT found its subject healthy, so this is
#      a finding and not a shrug.
#   3  the fleet reads OK but the DELIVERY POSITIVE CONTROL FAILED — detection
#      is fine and the alarm could not reach anyone. Reported separately because
#      a probe that can see and cannot speak is the mg-7ce7 state exactly.

set -uo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------

# POGO_HOME=$HOME is a LEGACY VALUE that is live on this box: an old shell
# integration exported "where the dotfiles live" and it survives in existing
# zshrc copies and launchd plists. internal/config.PogoHome normalizes it to
# $HOME/.pogo, and this script must normalize it the same way or it would look
# in $HOME/agents/turnlog, find nothing, and report UNMEASURABLE forever on the
# one box it was written for.
resolve_pogo_home() {
    local h="${POGO_HOME:-}"
    if [ -z "$h" ]; then echo "$HOME/.pogo"; return 0; fi
    # Compare without trailing slashes rather than with realpath: this must work
    # on a box where the directory does not exist yet.
    local a="${h%/}" b="${HOME%/}"
    if [ "$a" = "$b" ]; then echo "$a/.pogo"; else echo "$h"; fi
}

TURNLOG_DIR="${POGO_FLEET_PROBE_TURNLOG_DIR:-$(resolve_pogo_home)/agents/turnlog}"
STALE_AFTER_RAW="${POGO_FLEET_PROBE_STALE_AFTER:-2h}"
STAMP="${POGO_FLEET_PROBE_STAMP:-$(resolve_pogo_home)/fleet-liveness.stamp}"
LOG="${POGO_FLEET_PROBE_LOG:-}"
NOW_RAW=""
DO_MAIL=0
MAIL_TO="human"
MAIL_FROM="fleet-liveness-probe"
# How long before the SAME unresolved fleet stop is mailed again. The sampling
# rate and the notification rate answer different questions and must be settable
# apart — see the note in com.pogo.fleetliveness.plist.
RENOTIFY_RAW="${POGO_FLEET_PROBE_RENOTIFY:-1h}"
# The delivery positive control. Cadence, not every run: it puts a real message
# through the real path, and doing that every ten minutes would be its own noise.
SELFTEST_EVERY_RAW="${POGO_FLEET_PROBE_SELFTEST_EVERY:-12h}"
SELFTEST_TO="${POGO_FLEET_PROBE_SELFTEST_TO:-fleet-liveness-selftest}"
SELFTEST_STAMP=""
FORCE_SELFTEST=0
NO_SELFTEST=0
SELFTEST_ONLY=0
QUIET=0
# Clock skew tolerance for a FUTURE mtime. A few seconds of skew between the
# writer and this reader is ordinary; minutes is not, and a future mtime is the
# one way this predicate could read green over a broken measurement.
FUTURE_SLACK=120

usage() {
    # The header comment IS the help text. scripts/fleet-liveness-probe_test.sh
    # asserts --help still reaches the last line of EXIT STATUS, because a
    # hard-coded range silently truncates the moment the header grows.
    sed -n '2,196p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

VERDICT="INCOMPLETE"        # every terminal path below overwrites this
VERDICT_NOTE=""
AGE_LABEL="-"
NEWEST_NAME="-"
AGENT_COUNT=0
# MAIL_RESULT is the whole point of the ledger: computed / attempted / delivered
# are three different facts, and collapsing them is what let a count of refusal
# lines in a report log be read as "this path never worked" — an inference that
# had to be withdrawn once the mail record was consulted.
MAIL_RESULT="n/a"
SELFTEST_RESULT="-"

say() { [ "$QUIET" -eq 1 ] || echo "$@"; }

die_setup() {
    VERDICT="SETUP-FAILED"
    VERDICT_NOTE="$*"
    echo "fleet-liveness-probe: $*" >&2
    exit 2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --turnlog-dir) TURNLOG_DIR="${2:-}"; shift 2 ;;
        --stale-after) STALE_AFTER_RAW="${2:-}"; shift 2 ;;
        --stamp) STAMP="${2:-}"; shift 2 ;;
        --log) LOG="${2:-}"; shift 2 ;;
        --renotify) RENOTIFY_RAW="${2:-}"; shift 2 ;;
        --now) NOW_RAW="${2:-}"; shift 2 ;;
        --mail) DO_MAIL=1; shift ;;
        --mail-to) MAIL_TO="${2:-}"; DO_MAIL=1; shift 2 ;;
        --mail-from) MAIL_FROM="${2:-}"; shift 2 ;;
        --self-test) FORCE_SELFTEST=1; shift ;;
        --self-test-only) FORCE_SELFTEST=1; SELFTEST_ONLY=1; shift ;;
        --no-self-test) NO_SELFTEST=1; shift ;;
        --self-test-every) SELFTEST_EVERY_RAW="${2:-}"; shift 2 ;;
        --self-test-to) SELFTEST_TO="${2:-}"; shift 2 ;;
        --self-test-stamp) SELFTEST_STAMP="${2:-}"; shift 2 ;;
        --future-slack) FUTURE_SLACK="${2:-}"; shift 2 ;;
        --quiet) QUIET=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die_setup "unknown option '$1' (try --help)" ;;
    esac
done

[ -n "$SELFTEST_STAMP" ] || SELFTEST_STAMP="$STAMP.selftest"

# ---------------------------------------------------------------------------
# The ledger — ONE line per run, whatever happened
# ---------------------------------------------------------------------------
# Installed as an EXIT trap, and installed HERE, before the first thing that can
# call die_setup, so a probe that dies on its own setup still leaves a line
# saying it tried.
#
# A witness that writes only when it is unhappy cannot be distinguished from a
# witness that is not running, and this file's entire subject is a detector that
# was silent for five days because it never fired. The newest ledger line's age
# is the only thing on this box that answers "is the witness itself firing?".
#
# The trap calls no `exit`, so bash preserves the status the script was leaving
# with. The timestamp is real wall clock even when --now injects a synthetic one
# for the age arithmetic: a ledger whose timestamps can be back-dated by a flag
# is no longer a heartbeat.

log_verdict() {
    local rc="$1" dir
    [ -n "$LOG" ] || return 0
    dir="$(dirname "$LOG")"
    [ -d "$dir" ] || mkdir -p "$dir" 2>/dev/null
    {
        printf '%s exit=%s %-12s newest=%s age=%s threshold=%s agents=%s mail=%s selftest=%s' \
            "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$rc" "$VERDICT" \
            "$NEWEST_NAME" "$AGE_LABEL" "$STALE_AFTER_RAW" "$AGENT_COUNT" \
            "$MAIL_RESULT" "$SELFTEST_RESULT"
        [ -z "$VERDICT_NOTE" ] || printf ' -- %s' "$VERDICT_NOTE"
        printf '\n'
    } >> "$LOG" 2>/dev/null || {
        echo "fleet-liveness-probe: WARNING — could not append to the ledger $LOG. The run below still happened; nothing recorded that it did, so a reader cannot tell this probe from one that never fired." >&2
        return 0
    }
}

trap 'log_verdict "$?"' EXIT

# ---------------------------------------------------------------------------
# Duration and clock helpers
# ---------------------------------------------------------------------------

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

# to_epoch accepts epoch seconds or RFC3339. Both date dialects are tried
# because this file must run on the darwin box it was written for AND under a
# GNU coreutils CI image.
to_epoch() {
    local ts="$1" out norm
    if [ -z "${ts//[0-9]/}" ]; then echo "$ts"; return 0; fi
    if out="$(date -d "$ts" +%s 2>/dev/null)" && [ -n "$out" ]; then echo "$out"; return 0; fi
    norm="${ts%%.*}"
    if [ "${norm: -1}" = "Z" ]; then
        out="$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$norm" +%s 2>/dev/null)"
    else
        norm="$(echo "$norm" | sed -E 's/([+-][0-9]{2}):([0-9]{2})$/\1\2/')"
        out="$(date -j -f '%Y-%m-%dT%H:%M:%S%z' "$norm" +%s 2>/dev/null)"
    fi
    [ -n "$out" ] || return 1
    echo "$out"
}

# fmt_epoch renders an epoch as RFC3339. BSD spells it `-r <epoch>`, GNU spells
# that `-d @<epoch>` and reads `-r` as a FILE.
fmt_epoch() {
    date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
        || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
        || echo "epoch $1"
}

format_age() {
    local s="$1" d h m
    [ "$s" -ge 0 ] 2>/dev/null || s=0
    d=$(( s / 86400 )); h=$(( (s % 86400) / 3600 )); m=$(( (s % 3600) / 60 ))
    if [ "$d" -gt 0 ]; then echo "${d}d${h}h"
    elif [ "$h" -gt 0 ]; then echo "${h}h${m}m"
    else echo "${m}m"; fi
}

STALE_AFTER="$(parse_duration "$STALE_AFTER_RAW")" \
    || die_setup "--stale-after '$STALE_AFTER_RAW' is not a duration (e.g. 90m, 2h, 1d)"
RENOTIFY="$(parse_duration "$RENOTIFY_RAW")" \
    || die_setup "--renotify '$RENOTIFY_RAW' is not a duration (e.g. 30m, 1h, 0)"
SELFTEST_EVERY="$(parse_duration "$SELFTEST_EVERY_RAW")" \
    || die_setup "--self-test-every '$SELFTEST_EVERY_RAW' is not a duration (e.g. 6h, 12h, 1d)"
[ -z "${FUTURE_SLACK//[0-9]/}" ] && [ -n "$FUTURE_SLACK" ] \
    || die_setup "--future-slack '$FUTURE_SLACK' is not a number of seconds"

if [ -n "$NOW_RAW" ]; then
    NOW="$(to_epoch "$NOW_RAW")" || die_setup "--now '$NOW_RAW' is not an epoch or RFC3339 timestamp"
else
    NOW="$(date +%s)"
fi

# ---------------------------------------------------------------------------
# CAPABILITY PROBES — BY EXECUTION, AND NEVER THROUGH A PIPE (mg-7ce7)
# ---------------------------------------------------------------------------
# `set -o pipefail` plus `CMD | grep -q PATTERN` is the idiom that silenced
# revision-probe's --mail path mid-incident. grep exits on the first match, the
# producer takes SIGPIPE and exits 141, pipefail surfaces the 141 as the
# pipeline's status, and A CAPABILITY PROBE THEN REPORTS A WORKING TOOL AS
# ABSENT.
#
# It is not a hypothetical and it is not fixed at the time of writing —
# re-derived on this box rather than repeated from the ticket:
#
#     $ set -uo pipefail; mg --help 2>/dev/null | grep -q macguffin; echo $?
#     141
#
# It is a RACE, which is why it is worth removing structurally rather than
# patching where it is failing today: the same idiom against `git --version` and
# `curl --version` passes 10/10 on this box purely because those producers finish
# writing ~25 bytes before grep closes the pipe. They are the same bug, currently
# winning, and they flip when a binary grows.
#
# So the fix here is not a `|| true` in the right place: capture into a variable,
# match with `case`. There is no pipe, so there is nothing for a producer to die
# in, and the idiom cannot be reintroduced by someone copying a neighbouring
# line. Written up in the shared corpus as pipefail-plus-grep-q-reports-absent.
#
# `go`, `pogo` and `pogod` are deliberately NOT resolved anywhere in this file.
# This probe must run on a box where the fleet has been dead for five days, so it
# may not depend on anything an agent turn or a deploy provides. Asserted in
# scripts/fleet-liveness-probe_test.sh with all of them poisoned on PATH.

MG=""
resolve_mg() {
    # /usr/bin/mg satisfies -x and `command -v mg`; it is the Micro-Emacs
    # editor. Every candidate must self-identify as macguffin before it is
    # trusted (mg-015f / mg-dd5f). `go env` is deliberately not consulted for
    # GOBIN/GOPATH — this probe must run without a toolchain.
    local cand out
    for cand in "${GOBIN:-}/mg" "${GOPATH:-}/bin/mg" "$HOME/go/bin/mg" "$(command -v mg 2>/dev/null)"; do
        case "$cand" in ""|"/mg"|"/bin/mg") continue ;; esac
        [ -x "$cand" ] || continue
        out="$("$cand" --help 2>/dev/null)"
        case "$out" in *macguffin*) MG="$cand"; return 0 ;; esac
    done
    return 1
}

# ---------------------------------------------------------------------------
# THE DELIVERY POSITIVE CONTROL (mg-7ce7 is why this is not optional)
# ---------------------------------------------------------------------------
# It sends a real message through the real path and then READS IT BACK OUT of
# the recipient's mailbox. Confirming arrival is the whole point: `mg mail send`
# exiting 0 is an ATTEMPT, not a delivery, and a record that cannot tell those
# apart cannot answer the question this ticket turned on — did the alert reach
# anyone, or was it merely computed?
#
# THE TRADE, STATED RATHER THAN HIDDEN. The control sends to a dedicated box
# ($SELFTEST_TO), not to $MAIL_TO. It therefore exercises mg resolution, the send
# invocation, the store write and arrival — which is every part that has actually
# been observed failing — and it does NOT exercise the human box being
# addressable. That last one is covered separately and without a send, by asking
# mg whether $MAIL_TO exists, because `mg mail send` refuses a recipient it has
# never seen and a refusal at alert time is a lost alert.
#
# It is a cadence, not every run: a real message every ten minutes is its own
# noise, and noise is how an alarm stops being read.

selftest_due() {
    [ "$NO_SELFTEST" -eq 1 ] && return 1
    [ "$FORCE_SELFTEST" -eq 1 ] && return 0
    local last=""
    if [ -r "$SELFTEST_STAMP" ]; then
        read -r last _ < "$SELFTEST_STAMP" 2>/dev/null
    fi
    [ -n "${last:-}" ] && [ -z "${last//[0-9]/}" ] || return 0
    [ $(( NOW - last )) -ge "$SELFTEST_EVERY" ]
}

# mailbox_exists asks mg whether a name is addressable, because `mg mail send`
# REFUSES a recipient mg has never seen and a refusal at alert time is a lost
# alert.
#
# Reading it needs both streams, and the reason is worth recording because it
# cost this file a wrong verdict on its first live run. `mg mail list <box>
# --json` splits its output three ways:
#
#   empty box that EXISTS      stdout empty, stderr {"exists":true,...}
#   box that does NOT exist    stdout empty, stderr {"exists":false,...}
#   box WITH messages          stdout has message objects, stderr EMPTY
#
# So the existence object is not always emitted, and a check that only looked for
# `"exists":true` reported the busiest mailbox on the box — `human`, with 1927
# messages — as unknown. A message object is itself proof the box exists.
#
# No pipe anywhere, for the reason in the header.
mailbox_exists() {
    local out
    out="$("$MG" mail list "$1" --all --json 2>&1)"
    case "$out" in
        *'"exists":false'*) return 1 ;;
        *'"exists":true'*)  return 0 ;;
        *'"id":"'*)         return 0 ;;
    esac
    # No output at all is not "the box is absent" — it is "mg said nothing",
    # which this control refuses to read as either answer.
    return 1
}

# mail_ids prints the message ids currently in a box from $MAIL_FROM, one per
# line. Arrival is confirmed by DIFFING this before and after the send, never by
# matching the subject: two alerts about one unresolved stop carry the same
# subject, so a subject match would confirm the OLD message and report a send
# that never landed as delivered. That is the failure this control exists to
# catch, reproduced inside the control.
mail_ids() {
    local out
    out="$("$MG" mail list "$1" --all --json --from="$MAIL_FROM" 2>/dev/null)"
    printf '%s\n' "$out" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'
}

# first_new_id prints an id present in $2 (after) and absent from $1 (before).
first_new_id() {
    local before="$1" after="$2" id
    while IFS= read -r id; do
        [ -n "$id" ] || continue
        case "
$before
" in
            *"
$id
"*) continue ;;
        esac
        printf '%s\n' "$id"
        return 0
    done <<EOF
$after
EOF
    return 1
}

run_selftest() {
    local token subject bf out rc before after mid
    if ! resolve_mg; then
        SELFTEST_RESULT="FAILED:no-mg"
        return 1
    fi
    token="fleet-liveness-selftest-$NOW-$$"
    subject="POSITIVE CONTROL $token"

    if ! mailbox_exists "$SELFTEST_TO"; then
        # Registering is correct here and only here: this box is this probe's
        # own, and being its first writer is the one case --create-style
        # behaviour is for. It is NEVER done for $MAIL_TO — talking past that
        # refusal is how a typo'd recipient becomes a dead drop that reports
        # "Delivered".
        "$MG" mail register "$SELFTEST_TO" >/dev/null 2>&1
    fi

    bf="$(mktemp)" || { SELFTEST_RESULT="FAILED:no-tmpfile"; return 1; }
    cat > "$bf" <<EOF
$subject

This message is a POSITIVE CONTROL on the delivery path of
scripts/fleet-liveness-probe.sh. It carries no finding about the fleet.

It exists because detection and delivery are separate halves and only the first
is exercised by ordinary operation. A detector that never fires never tests its
own notification path, so the path rots unobserved and is discovered during the
incident it exists for.

Measured, on this box: revision-probe.sh delivered three correct stale-pogod
alerts to human on 08-16 and 08-17 and then went silent mid-incident, with NO
code change. Its capability probe is written as \`cmd | grep -q\` under
\`set -o pipefail\`, which is a RACE — a large enough producer takes SIGPIPE, the
141 surfaces, and a working binary is reported absent (mg-7ce7). A path that
passed yesterday is not a path that works today, which is why this control runs
on a cadence and not once at install time.

If you are reading this in a mailbox, the path works.
EOF
    before="$(mail_ids "$SELFTEST_TO")"
    out="$("$MG" mail send "$SELFTEST_TO" --from="$MAIL_FROM" --subject="$subject" --body-file "$bf" 2>&1)"
    rc=$?
    rm -f "$bf"
    if [ "$rc" -ne 0 ]; then
        SELFTEST_RESULT="FAILED:send-rc-$rc"
        SELFTEST_DETAIL="$out"
        return 1
    fi

    # ARRIVAL, not the exit status. This is the assertion the whole control is for.
    after="$(mail_ids "$SELFTEST_TO")"
    mid="$(first_new_id "$before" "$after")" || {
        SELFTEST_RESULT="FAILED:sent-not-arrived"
        SELFTEST_DETAIL="mg mail send exited 0 and no new message from $MAIL_FROM appeared in $SELFTEST_TO. The send was ATTEMPTED, not DELIVERED, and those are the two states this whole file exists to keep apart."
        return 1
    }

    # Archive it so the control does not become a mailbox that fills up. A
    # failure to archive is not a failure of the control: the message arrived,
    # which is the thing being proven.
    "$MG" mail archive "$SELFTEST_TO/$mid" >/dev/null 2>&1

    # The real recipient must be ADDRESSABLE, checked without sending to it.
    if ! mailbox_exists "$MAIL_TO"; then
        SELFTEST_RESULT="FAILED:recipient-$MAIL_TO-unknown"
        SELFTEST_DETAIL="the delivery path works, but 'mg mail send $MAIL_TO' would be REFUSED — mg has never seen that name. At alert time that refusal is a lost alert."
        return 1
    fi

    SELFTEST_RESULT="ok"
    [ -d "$(dirname "$SELFTEST_STAMP")" ] || mkdir -p "$(dirname "$SELFTEST_STAMP")" 2>/dev/null
    printf '%s ok\n' "$NOW" > "$SELFTEST_STAMP" 2>/dev/null
    return 0
}


# --self-test-only: run the delivery control and stop. It is what the installer
# calls, and it deliberately does not measure the fleet — arming a job must not
# be able to send a FLEET STOP alert as a side effect of arming it.
if [ "$SELFTEST_ONLY" -eq 1 ]; then
    SELFTEST_DETAIL=""
    if run_selftest; then
        say "fleet-liveness-probe: delivery positive control PASSED — a message was sent to $SELFTEST_TO and read back out of it, and '$MAIL_TO' is addressable."
        VERDICT="SELFTEST-OK"
        exit 0
    fi
    cat >&2 <<EOF
fleet-liveness-probe: DELIVERY POSITIVE CONTROL FAILED — $SELFTEST_RESULT
$SELFTEST_DETAIL

An alert path that has never been observed succeeding is not known to work. This
run makes NO claim about the fleet; it says only that this probe could not be
shown to reach anyone.
EOF
    VERDICT="SELFTEST-FAILED"
    VERDICT_NOTE="$SELFTEST_RESULT"
    exit 3
fi

# ---------------------------------------------------------------------------
# THE MEASUREMENT — a stat over a glob, and a subtraction
# ---------------------------------------------------------------------------
# Every failure below lands in UNMEASURABLE and names the row and the reason.
# None of them lands in FLEET-STOP: a probe that pages the fleet because it
# could not read a directory is the mg-f867 spec's own first defect, found by
# pm-riemann implementing it.

unmeasurable() {
    VERDICT="UNMEASURABLE"
    VERDICT_NOTE="$*"
    cat >&2 <<EOF
fleet-liveness-probe: UNMEASURABLE — $*

This is NOT "the fleet is down" and it is NOT "the fleet is fine". A two-valued
instrument has nowhere to put "I could not measure this", so the failure lands in
whichever cell the arithmetic happens to produce; this probe refuses to judge
instead. The two states owe different responses:

  FLEET-STOP    every agent has stopped completing turns -> wake them, by MAIL
  UNMEASURABLE  this witness cannot see the artifact     -> fix the witness

  ls -la $TURNLOG_DIR
  tail -3 $TURNLOG_DIR/*.log
EOF
    exit 2
}

# stat_mtime prints a file's mtime in epoch seconds. BSD spells the format `-f
# %m`, GNU spells it `-c %Y`; both are tried so the probe runs on the darwin box
# and under a GNU coreutils CI image.
#
# `-L` on both, and it is not cosmetic. Without it BSD stat reports the SYMLINK's
# own mtime, so a turnlog that is a link to a rotated-away file would answer with
# the moment the link was made and read as a completed turn that never happened —
# a green verdict from an artifact that is not there. With -L a dangling link
# fails to stat, which lands in UNMEASURABLE where it belongs. The question this
# probe asks is about the DATA's age, not the pointer's.
stat_mtime() {
    local out
    out="$(stat -L -f %m "$1" 2>/dev/null)" && [ -n "$out" ] && { echo "$out"; return 0; }
    out="$(stat -L -c %Y "$1" 2>/dev/null)" && [ -n "$out" ] && { echo "$out"; return 0; }
    return 1
}

[ -d "$TURNLOG_DIR" ] \
    || unmeasurable "the turnlog directory '$TURNLOG_DIR' does not exist or is not a directory. That is a finding: the artifact this probe reads is the only one on this box that a COMPLETED turn is needed to produce, and \`pogo agent list\` showed last-activity='just now' for agents whose turnlogs were five days stale."
[ -r "$TURNLOG_DIR" ] \
    || unmeasurable "the turnlog directory '$TURNLOG_DIR' is not readable by this process"

NEWEST_MTIME=""
UNREADABLE=""
shopt -s nullglob
for f in "$TURNLOG_DIR"/*.log; do
    AGENT_COUNT=$(( AGENT_COUNT + 1 ))
    # `[ -e ]` FOLLOWS symlinks, and it is here because of a measured BSD quirk:
    # `stat -L` on a DANGLING symlink does not fail on darwin, it silently falls
    # back to the link's own mtime. So a turnlog symlinked to a file that has
    # been rotated away would report the moment the LINK was made as a completed
    # turn — a green verdict from an artifact that is not there, which is this
    # ticket's whole failure mode in miniature. Measured:
    #
    #     ln -s /nonexistent x.log && stat -L -f %m x.log   ->  exit 0, link mtime
    #
    # Digits-only is then the acceptance test, and it is spelled the long way on
    # purpose: `[ -z "${m//[0-9]/}" ]` is TRUE for an EMPTY m as well as for a
    # numeric one, so the short spelling accepts the failure it is checking for.
    if [ ! -e "$f" ] || ! m="$(stat_mtime "$f")" || [ -z "$m" ] || [ -n "${m//[0-9]/}" ]; then
        # Per-ROW refusal, reported by name. An unmeasurable row is never folded
        # into either verdict, and never silently dropped from the population
        # either — a glob that quietly skips the one file it could not stat is a
        # smaller version of exactly this ticket.
        UNREADABLE="$UNREADABLE $(basename "$f")"
        continue
    fi
    if [ -z "$NEWEST_MTIME" ] || [ "$m" -gt "$NEWEST_MTIME" ]; then
        NEWEST_MTIME="$m"
        NEWEST_NAME="$(basename "$f")"
    fi
done
shopt -u nullglob

if [ -z "$NEWEST_MTIME" ]; then
    if [ "$AGENT_COUNT" -eq 0 ]; then
        unmeasurable "no *.log files under '$TURNLOG_DIR' — the population is EMPTY, which is not the same as a population that has stopped. A fleet that was never spawned and a fleet that died look identical to a file-count, and they owe different responses."
    fi
    unmeasurable "none of the $AGENT_COUNT turnlog file(s) could be stat'd:$UNREADABLE"
fi

AGE=$(( NOW - NEWEST_MTIME ))

if [ "$AGE" -lt "-$FUTURE_SLACK" ]; then
    # THE ONE WAY THIS PREDICATE COULD READ GREEN OVER A BROKEN MEASUREMENT.
    # A future mtime makes AGE negative, which is younger than any threshold, so
    # a two-cell instrument would report OK and go quiet — the exact silence this
    # ticket is about, reproduced by its own remedy. It is a finding instead.
    AGE_LABEL="future"
    unmeasurable "the newest turnlog '$NEWEST_NAME' has an mtime $(format_age $(( -AGE ))) IN THE FUTURE ($(fmt_epoch "$NEWEST_MTIME")). A negative age is younger than every threshold, so treating this as OK would report health from a broken clock. Check for NTP steps, a restored backup, or a file touched by something other than a completed turn."
fi

[ "$AGE" -ge 0 ] || AGE=0
AGE_LABEL="$(format_age "$AGE")"

if [ -n "$UNREADABLE" ]; then
    # Partial coverage is stated in every verdict below rather than dropped: the
    # newest readable row still answers the fleet question, but the reader is
    # owed the fact that the population was not fully covered.
    VERDICT_NOTE="unreadable row(s):$UNREADABLE"
fi

# ---------------------------------------------------------------------------
# The stamp — when this stop was first SEEN, and when it was last MAILED
# ---------------------------------------------------------------------------
# Keyed on the newest mtime, so a fleet that resumes and stops again is a NEW
# alert rather than a continuation of the old one.

FIRST_SEEN="$NOW"
MAILED_AT=""
stamp_dir="$(dirname "$STAMP")"
if [ -r "$STAMP" ]; then
    read -r st_seen st_mtime st_mailed _ < "$STAMP" 2>/dev/null
    if [ -n "${st_mtime:-}" ] && [ "$st_mtime" = "$NEWEST_MTIME" ] \
        && [ -n "${st_seen:-}" ] && [ -z "${st_seen//[0-9]/}" ]; then
        FIRST_SEEN="$st_seen"
        if [ -n "${st_mailed:-}" ] && [ -z "${st_mailed//[0-9]/}" ]; then
            MAILED_AT="$st_mailed"
        fi
    fi
fi

write_stamp() {
    [ -d "$stamp_dir" ] || mkdir -p "$stamp_dir" 2>/dev/null
    printf '%s %s %s\n' "$FIRST_SEEN" "$NEWEST_MTIME" "${MAILED_AT:--}" > "$STAMP" 2>/dev/null \
        || echo "fleet-liveness-probe: WARNING — could not write the stamp $STAMP, so the re-notify throttle cannot hold and the same stop will be mailed every run" >&2
}

SELFTEST_DETAIL=""
SELFTEST_FAILED=0
if selftest_due; then
    if ! run_selftest; then
        SELFTEST_FAILED=1
        cat >&2 <<EOF
fleet-liveness-probe: DELIVERY POSITIVE CONTROL FAILED — $SELFTEST_RESULT
$SELFTEST_DETAIL

Detection and delivery are separate halves. Whatever this run says about the
fleet below, THIS probe's alarm cannot be assumed to reach anyone. That is the
mg-7ce7 state exactly: a witness whose delivery half worked, was relied on
because it had worked, and then stopped silently with no code change — caught
only because somebody went looking.

  tail -5 ${LOG:-<no --log configured>}
  $MG mail list $SELFTEST_TO --all
EOF
    fi
else
    if [ "$NO_SELFTEST" -eq 1 ]; then
        SELFTEST_RESULT="off"
    else
        st_last=""
        [ -r "$SELFTEST_STAMP" ] && read -r st_last _ < "$SELFTEST_STAMP" 2>/dev/null
        if [ -n "${st_last:-}" ] && [ -z "${st_last//[0-9]/}" ]; then
            SELFTEST_RESULT="fresh($(format_age $(( NOW - st_last ))))"
        else
            SELFTEST_RESULT="fresh"
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Verdict
# ---------------------------------------------------------------------------

if [ "$AGE" -le "$STALE_AFTER" ]; then
    rm -f "$STAMP" 2>/dev/null
    VERDICT="OK"
    say "fleet-liveness-probe: OK — $NEWEST_NAME completed a turn $AGE_LABEL ago (threshold $(format_age "$STALE_AFTER"))."
    say "  turnlogs   $AGENT_COUNT under $TURNLOG_DIR"
    say "  newest     $NEWEST_NAME at $(fmt_epoch "$NEWEST_MTIME")"
    say "  self-test  $SELFTEST_RESULT"
    [ -z "$UNREADABLE" ] || say "  UNREADABLE$UNREADABLE — not covered by this verdict"
    if [ "$SELFTEST_FAILED" -eq 1 ]; then
        # The fleet is fine and this witness cannot speak. Reported as its own
        # exit status rather than folded into OK, because an alarm that has never
        # been observed succeeding is not known to work.
        VERDICT="OK-DELIVERY-BROKEN"
        exit 3
    fi
    exit 0
fi

write_stamp

VERDICT="FLEET-STOP"

build_report() {
    cat <<EOF
MEASURED
  newest turn        $NEWEST_NAME at $(fmt_epoch "$NEWEST_MTIME")
  age                $AGE_LABEL   (threshold $(format_age "$STALE_AFTER"))
  population         $AGENT_COUNT turnlog file(s) under $TURNLOG_DIR
  first seen by this probe
                     $(fmt_epoch "$FIRST_SEEN") (recorded in $STAMP)
  delivery control   $SELFTEST_RESULT
EOF
    [ -z "$UNREADABLE" ] || printf '  UNREADABLE ROWS    %s — outside this verdict\n' "$UNREADABLE"
    cat <<'EOF'

WHAT THIS MEANS
The predicate is NEWEST-ACROSS-ALL, not per-agent. An idle PM legitimately goes
hours between turns; this says the MOST RECENT turn by ANY agent is older than
the threshold, which means every agent is down simultaneously. That has happened
twice (2026-08-11, 22h; 2026-08-14, ~118h) and has never been benign.

WHY THIS ARRIVED BY MAIL AND NOT AS A NUDGE
Measured during the 2026-08-14 outage: nudges recovered the three agents that
were merely unreachable and did nothing for the three that were wedged. mayor
never goes idle, so the idle gate drops its nudges (a spinner makes an agent look
busiest exactly when it is least reachable); pa answered a wake with silence, and
wake_silence_once then stopped trying for 106h. Mail is the only wake channel
that survived both rules.

WHAT TO DO
  ls -la ~/.pogo/agents/turnlog/                 # per-agent, for the shape
  pogo agent list                                # DO NOT TRUST last-activity here:
                                                 # it read 'just now' for agents
                                                 # whose turnlogs were 5 days stale
  pogo check-turns                               # the per-agent diagnostic
  pogo nudge <agent> --immediate                 # recovers the merely unreachable
  pogo agent stop <agent> && pogo agent start <agent>   # the only exit for a wedge
EOF
}

SUBJECT="FLEET STOP — no agent has completed a turn for $AGE_LABEL (newest: $NEWEST_NAME)"
BODY="$(printf 'fleet-liveness-probe: ALERT — no agent on this box has completed a turn for %s, which is longer than the %s threshold.\n\nThis probe is a launchd job reading a file. It is not a crew schedule and it is not hosted inside the fleet, because a detector hosted INSIDE the population it watches cannot report that population failing — which is why the check that was built for this exact outage (deploy-verify §0) never ran: it was architect'"'"'s own schedule, and architect was one of the agents that stopped.\n\n%s\n' \
    "$AGE_LABEL" "$(format_age "$STALE_AFTER")" "$(build_report)")"

echo "fleet-liveness-probe: ALERT — $SUBJECT"
say ""
say "$BODY"

# ---------------------------------------------------------------------------
# Mail — and the ledger records the RESULT, not the attempt
# ---------------------------------------------------------------------------

if [ "$DO_MAIL" -eq 0 ]; then
    MAIL_RESULT="computed"
elif [ -n "$MAILED_AT" ] && [ "$RENOTIFY" -gt 0 ] && [ $(( NOW - MAILED_AT )) -lt "$RENOTIFY" ]; then
    MAIL_RESULT="throttled"
    VERDICT_NOTE="${VERDICT_NOTE:+$VERDICT_NOTE; }mail throttled: already mailed $(format_age $(( NOW - MAILED_AT ))) ago, next after $(format_age "$RENOTIFY")"
    echo "fleet-liveness-probe: mail THROTTLED — already mailed $(format_age $(( NOW - MAILED_AT ))) ago. The alert above and the exit status stand." >&2
elif ! resolve_mg; then
    MAIL_RESULT="no-mg"
    echo "fleet-liveness-probe: --mail was asked for but no macguffin 'mg' was found — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR). The alert above is still the exit status, and nothing has reached a human." >&2
else
    bf="$(mktemp)"
    printf '%s\n' "$BODY" > "$bf"
    ids_before="$(mail_ids "$MAIL_TO")"
    send_out="$("$MG" mail send "$MAIL_TO" --from="$MAIL_FROM" --subject="$SUBJECT" --body-file "$bf" 2>&1)"
    send_rc=$?
    rm -f "$bf"
    if [ "$send_rc" -ne 0 ]; then
        MAIL_RESULT="send-failed-rc-$send_rc"
        echo "fleet-liveness-probe: could not mail $MAIL_TO (exit $send_rc) — the alert stands, it just did not reach anyone: $send_out" >&2
    else
        # ATTEMPTED is not DELIVERED. Read it back, by NEW id.
        ids_after="$(mail_ids "$MAIL_TO")"
        case "$(first_new_id "$ids_before" "$ids_after" && echo yes)" in
            *yes*)
                MAIL_RESULT="delivered"
                MAILED_AT="$NOW"
                write_stamp
                ;;
            *)
                # The send exited 0 and the message is not in the box. Do NOT
                # stamp: an unconfirmed send must be retried next run rather than
                # recorded as a notification and then gone quiet.
                MAIL_RESULT="attempted-unconfirmed"
                echo "fleet-liveness-probe: mg mail send exited 0 but the alert is NOT in $MAIL_TO. Recording ATTEMPTED, not DELIVERED, and NOT starting the re-notify throttle — the next run will try again." >&2
                ;;
        esac
    fi
fi

exit 1
