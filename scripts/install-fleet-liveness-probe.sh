#!/usr/bin/env bash
# ARM scripts/fleet-liveness-probe.sh as a LaunchAgent (mg-f867).
#
# WHY AN INSTALLER AND NOT "COPY THE PLIST"
#
# mg-ce10 landed the external redeploy witness and wired it to NOTHING — 501
# lines, referenced by a changelog fragment, a docs section and test.sh, and by
# no schedule, no plist and no runner. That is the limiting case of the rule
# these probes implement: a detector armed by nothing is present by existence
# and absent by effect. mg-a03d fixed it with an installer, and this file is the
# same shape for the fleet-liveness witness.
#
# WHAT IT WILL NOT DO
#
# It renders the TRACKED plist (scripts/launchd/com.pogo.fleetliveness.plist),
# never a copy held in this file. The house pattern elsewhere keeps a Go string
# that "mirrors" the in-repo plist, and mg-b201 is what that costs: the shipped
# plist and the installed one drifted, and it took a dedicated test to notice.
# One source, no mirror, no drift class.
#
# It refuses before it writes. A half-installed LaunchAgent is worse than an
# absent one: it is a job that exists, appears in `launchctl list`, and cannot
# run.
#
# It EXECUTES the argument vector it is about to install, read back out of the
# rendered plist. That check exists because install-revision-probe.sh's first
# live install produced a LaunchAgent that was loaded, listed, correct-looking in
# `launchctl print`, and wrote `unknown option` once an hour forever — the plist
# and the probe are two tracked files that reach a box at DIFFERENT times, and
# $SRC is advanced by the deploy runner rather than by whoever runs this.
#
# THE DELIVERY POSITIVE CONTROL IS PART OF ARMING, NOT AN EXTRA
#
# revision-probe delivered three correct stale-pogod alerts to `human` on 08-16
# and 08-17 and then went silent mid-incident, with NO code change: its
# capability probe is `cmd | grep -q` under `set -o pipefail`, which is a RACE
# that a large enough producer loses on SIGPIPE 141 (mg-7ce7). So the lesson is
# not "that pattern does not work" — it does, and this job is modelled on it —
# it is that a delivery path can pass and then stop with nothing having changed.
#
# This installer therefore runs the probe's own delivery control before arming
# and REFUSES the install if it fails. That check is necessary and NOT
# sufficient, and the difference is the whole point: one passing run of a race is
# a coin landing the right way, which is why the armed job re-runs the same
# control on a cadence (default every 12h) rather than trusting this one.
#
# Arming a witness that cannot speak would be the defect this ticket is about,
# reproduced by its own remedy.
#
# USAGE
#
#   scripts/install-fleet-liveness-probe.sh                 # install / re-install
#   scripts/install-fleet-liveness-probe.sh --dry-run       # render and print, touch nothing
#   scripts/install-fleet-liveness-probe.sh --uninstall     # bootout and remove
#   scripts/install-fleet-liveness-probe.sh --src ~/dev/pogo --stale-after 2h
#   scripts/install-fleet-liveness-probe.sh --skip-delivery-check   # arm anyway, and say so
#
# EXIT STATUS
#
#   0  installed (or removed, or rendered under --dry-run) and verified loaded
#   2  refused — a precondition is not met, and nothing was changed

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LABEL="com.pogo.fleetliveness"
TEMPLATE="$HERE/launchd/$LABEL.plist"

SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
STALE_AFTER="2h"
LA_DIR="${POGO_LAUNCHAGENTS_DIR:-$HOME/Library/LaunchAgents}"
LAUNCHCTL="${POGO_LAUNCHCTL:-}"
DRY_RUN=0
UNINSTALL=0
SKIP_DELIVERY=0

die() {
    echo "install-fleet-liveness-probe: $*" >&2
    exit 2
}

usage() { sed -n '2,61p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --src) SRC="${2:-}"; shift 2 ;;
        --stale-after) STALE_AFTER="${2:-}"; shift 2 ;;
        # --launch-agents-dir and --launchctl exist so the controls can install
        # into a fixture directory against a recording stub. An installer whose
        # only test is "run it on the real box and look" is one nobody runs.
        --launch-agents-dir) LA_DIR="${2:-}"; shift 2 ;;
        --launchctl) LAUNCHCTL="${2:-}"; shift 2 ;;
        --skip-delivery-check) SKIP_DELIVERY=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option '$1' (try --help)" ;;
    esac
done

PLIST="$LA_DIR/$LABEL.plist"

resolve_launchctl() {
    local cand
    for cand in "$LAUNCHCTL" /bin/launchctl /usr/bin/launchctl "$(command -v launchctl 2>/dev/null)"; do
        [ -n "$cand" ] || continue
        [ -x "$cand" ] || continue
        LAUNCHCTL="$cand"
        return 0
    done
    return 1
}

resolve_launchctl \
    || die "no launchctl on this host. This installer arms a macOS LaunchAgent; on any other platform the witness needs a different substrate and this script refuses rather than pretending."

UID_NUM="$(id -u)"
DOMAIN="gui/$UID_NUM"

if [ "$UNINSTALL" -eq 1 ]; then
    if [ -f "$PLIST" ]; then
        "$LAUNCHCTL" bootout "$DOMAIN/$LABEL" >/dev/null 2>&1
        rm -f "$PLIST" || die "could not remove $PLIST"
        echo "install-fleet-liveness-probe: removed $LABEL and $PLIST"
        echo "  The stamp and the ledger are LEFT IN PLACE — they are the record of what the"
        echo "  witness saw, and deleting them on uninstall would erase the evidence along with"
        echo "  the instrument."
    else
        "$LAUNCHCTL" bootout "$DOMAIN/$LABEL" >/dev/null 2>&1
        echo "install-fleet-liveness-probe: no $PLIST to remove; booted out $LABEL in case it was loaded"
    fi
    exit 0
fi

[ -f "$TEMPLATE" ] || die "the plist template $TEMPLATE is missing — this script renders the tracked template rather than generating a copy, so that a change to the job cannot be made in one place and forgotten in the other"

PROBE="$SRC/scripts/fleet-liveness-probe.sh"
[ -d "$SRC" ] || die "--src '$SRC' does not exist. That is the checkout the job runs the probe FROM; pass --src, or let the deploy create ~/.pogo/deploy-src first."
[ -f "$PROBE" ] || die "'$PROBE' is missing. Installing a job that points at an absent script produces a LaunchAgent that fails every fifteen minutes and reports nothing — an alarm that is louder in the log than in anyone's inbox."
[ -x "$PROBE" ] || die "'$PROBE' is not executable"

case "$STALE_AFTER" in
    *[!0-9smhd]*|"") die "--stale-after '$STALE_AFTER' is not a duration (e.g. 90m, 2h, 1d)" ;;
esac

LOG_DIR="$HOME/Library/Logs/pogo"
LEDGER="$LOG_DIR/fleet-liveness.log"
REPORT="$LOG_DIR/fleet-liveness.report.log"
STAMP="$HOME/.pogo/fleet-liveness.stamp"
TURNLOG_DIR="$HOME/.pogo/agents/turnlog"

# `/Users/YOUR_USERNAME` in the template stands for $HOME, not for a literal
# /Users path. On this box they are the same string; keeping them separate is
# what lets the controls render into a fixture home instead of the developer's.
for v in "$HOME" "$SRC" "$STALE_AFTER"; do
    case "$v" in
        *'#'*) die "'$v' contains '#', which is the substitution delimiter used to render the plist. Refusing rather than emitting a mangled job." ;;
    esac
done

render() {
    sed -e "s#/Users/YOUR_USERNAME/\.pogo/deploy-src#__POGO_SRC__#g" \
        -e "s#/Users/YOUR_USERNAME#$HOME#g" \
        -e "s#__POGO_SRC__#$SRC#g" \
        -e "s#<string>2h</string>#<string>$STALE_AFTER</string>#" \
        "$TEMPLATE"
}

RENDERED="$(render)" || die "could not render $TEMPLATE"

# THE CHECK THAT MATTERS: a surviving placeholder is a job that is installed,
# listed, and permanently broken. Scoped to <string> VALUES — the template's
# comments name the placeholder on purpose, to tell a hand-installer what to
# substitute, so a placeholder in a comment is documentation and one in a value
# is a broken job.
# NO `| grep -q` HERE, AND THAT IS THIS TICKET'S OWN RULE APPLIED TO ITS OWN
# REMEDY. The obvious spelling of this check is
#
#     printf '%s' "$RENDERED" | grep '<string>' | grep -q 'YOUR_USERNAME'
#
# and it is the mg-7ce7 shape verbatim: a real binary producing into an
# early-exiting `grep -q` under `set -o pipefail`. It happens to WIN today — the
# middle grep emits ~30 lines, far under a pipe buffer, so it finishes writing
# before the consumer closes — which is exactly what "latent" means and exactly
# how git and curl pass in revision-probe while mg loses every time. A guard on
# the install path that goes quiet when the plist grows past 64KB is not a guard.
#
# So the placeholder scan captures its output instead (a plain `grep` reads all
# of its input and cannot early-exit), and the mention scan is pure shell with no
# pipe at all. Both are structurally immune rather than currently lucky.
PLACEHOLDER_HITS="$(printf '%s\n' "$RENDERED" | grep '<string>' | grep 'YOUR_USERNAME')"
if [ -n "$PLACEHOLDER_HITS" ]; then
    die "the rendered plist still has YOUR_USERNAME in a value — refusing to install a job that cannot run:
$PLACEHOLDER_HITS"
fi
for required in "$PROBE" "$TURNLOG_DIR" "$LEDGER" "$STAMP"; do
    case "$RENDERED" in
        *"$required"*) ;;
        *) die "the rendered plist does not mention '$required' — the template and this installer disagree about the job's shape, and installing the result would arm something nobody described" ;;
    esac
done

# ---------------------------------------------------------------------------
# THE ARGUMENT VECTOR MUST BE ONE THE PROBE AT $SRC CAN PARSE
# ---------------------------------------------------------------------------
# Checked by EXECUTION, never by grepping --help text or comparing versions:
# appending --help to the real vector makes the probe parse every flag ahead of
# it and then exit 0 before doing anything. An unknown flag dies at the parse.
# The vector is read back out of the RENDERED plist rather than rebuilt here, so
# the thing checked is the thing installed.

PROBE_ARGS=""
PROBE_ARGV=()
extract_program_arguments() {
    # The ProgramArguments array, one <string> per line. sed rather than a plist
    # reader: this script may not assume python3 any more than it may assume go.
    printf '%s\n' "$RENDERED" \
        | sed -n '/<key>ProgramArguments<\/key>/,/<\/array>/p' \
        | sed -n 's/.*<string>\(.*\)<\/string>.*/\1/p'
}

CAPCHECK_OUT=""
CAPCHECK_RC=0
check_argument_vector() {
    local line
    PROBE_ARGV=()
    while IFS= read -r line; do PROBE_ARGV+=("$line"); done <<EOF
$(extract_program_arguments)
EOF
    [ "${#PROBE_ARGV[@]}" -gt 1 ] \
        || die "could not read ProgramArguments back out of the rendered plist — refusing to install a job whose command line nothing has checked"
    PROBE_ARGS="${PROBE_ARGV[*]}"
    CAPCHECK_OUT="$("${PROBE_ARGV[@]}" --help 2>&1 >/dev/null)"
    CAPCHECK_RC=$?
    return "$CAPCHECK_RC"
}

if ! check_argument_vector; then
    die "the probe at $PROBE cannot parse the command line this job would give it (exit $CAPCHECK_RC):

    $PROBE_ARGS

  $CAPCHECK_OUT

This is an ARRIVAL-ORDER problem, not a bug in either file. The plist and the
probe are both tracked, and they reach this box at different times: you are
running this installer from one checkout, and the job points at
  $SRC
which the deploy runner's sync_src advances. Installing anyway would arm a
LaunchAgent that is loaded, listed, and writes 'unknown option' every fifteen
minutes — which is the exact defect this whole job exists to remove.

Bring the checkout forward and re-run:

    git -C $SRC fetch origin && git -C $SRC merge --ff-only origin/main
    $0"
fi

if [ "$DRY_RUN" -eq 1 ]; then
    echo "# would write $PLIST"
    echo "# would run:  $LAUNCHCTL bootout $DOMAIN/$LABEL   (ignoring failure — it may not be loaded)"
    echo "# would run:  $LAUNCHCTL bootstrap $DOMAIN $PLIST"
    echo "# would run the probe's delivery positive control before arming"
    echo
    printf '%s\n' "$RENDERED"
    exit 0
fi

# ---------------------------------------------------------------------------
# THE DELIVERY POSITIVE CONTROL, BEFORE ARMING
# ---------------------------------------------------------------------------
# Detection and delivery are separate halves and only the first is exercised by
# ordinary operation. Arming a detector whose notification path has never been
# observed working is how a witness ends up silent during the incident it exists
# for — and a path observed working ONCE is not a path known to work, because the
# idiom that silenced revision-probe is a race that flipped without a code change.
#
# It runs the probe's own --self-test rather than a copy of it. A control that
# reimplemented the send would vouch for the copy.

DELIVERY_NOTE=""
if [ "$SKIP_DELIVERY" -eq 1 ]; then
    DELIVERY_NOTE="SKIPPED (--skip-delivery-check). This job's alert path has NOT been observed working."
else
    # --self-test-only, not --self-test: arming a job must not be able to send a
    # FLEET STOP alert as a side effect of arming it. And it is ONE invocation,
    # never retried — a control that retries on failure converts a real defect
    # into a pass, which is the one thing a positive control may not do.
    ST_OUT="$("$PROBE" --self-test-only --self-test-stamp "$STAMP.selftest" 2>&1)"
    ST_RC=$?
    if [ "$ST_RC" -ne 0 ]; then
        die "the delivery positive control FAILED (exit $ST_RC), so nothing was armed:

$ST_OUT

An alert path that has never been observed succeeding is not known to work, and
arming a witness that can see and cannot speak is exactly the state mg-7ce7
measured: a delivery half that worked, was relied on because it had worked, and
then stopped silently — caught only because somebody went looking.

Fix the path and re-run, or arm it anyway with --skip-delivery-check and know
that the alarm is unproven."
    fi
    DELIVERY_NOTE="PASSED — a self-test message was sent and read back out of the mailbox it was sent to, and 'human' was confirmed addressable"
fi

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------

mkdir -p "$LA_DIR" || die "could not create $LA_DIR"
mkdir -p "$LOG_DIR" 2>/dev/null
mkdir -p "$(dirname "$STAMP")" 2>/dev/null

printf '%s\n' "$RENDERED" > "$PLIST" || die "could not write $PLIST"

if command -v plutil >/dev/null 2>&1; then
    plutil -lint "$PLIST" >/dev/null 2>&1 \
        || die "the plist just written to $PLIST does not parse — refusing to leave it there; run with --dry-run to see the render"
fi

# bootout first, unconditionally: bootstrap over an already-loaded label fails
# with `Load failed: 5: Input/output error`, and a re-install that silently
# no-ops would leave the OLD job definition running under the NEW plist's name.
"$LAUNCHCTL" bootout "$DOMAIN/$LABEL" >/dev/null 2>&1
BOOTSTRAP_OUT="$("$LAUNCHCTL" bootstrap "$DOMAIN" "$PLIST" 2>&1)"
BOOTSTRAP_RC=$?
if [ "$BOOTSTRAP_RC" -ne 0 ]; then
    die "launchctl bootstrap failed (exit $BOOTSTRAP_RC): $BOOTSTRAP_OUT
The plist is at $PLIST. Nothing is loaded, so the witness is NOT armed."
fi

# `bootstrap` returning 0 means the plist was accepted, which is a claim about
# parsing and not about the job existing in the domain. This whole lineage exists
# because a component reported success for a thing that had not happened.
if ! "$LAUNCHCTL" print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
    die "launchctl bootstrap exited 0 but '$LAUNCHCTL print $DOMAIN/$LABEL' does not know the job. The witness is NOT armed."
fi

cat <<EOF
install-fleet-liveness-probe: ARMED — $LABEL is loaded in $DOMAIN.

  plist     $PLIST
  program   $PROBE
  turnlogs  $TURNLOG_DIR
  ledger    $LEDGER      (one line per run: OK, FLEET-STOP or UNMEASURABLE)
  reports   $REPORT
  stamp     $STAMP

  fires     every 15 minutes at :07 :22 :37 :52, plus once now (RunAtLoad)
  replay    DEFERRED-ONCE across sleep — launchd delivers one run on wake for
            any number of fires missed while asleep. A fire missed because the
            host was POWERED OFF is lost outright; nothing replays it.
  threshold $STALE_AFTER on the NEWEST turnlog mtime across ALL agents. Not
            per-agent: an idle PM legitimately goes hours between turns, and the
            all-of-them condition is what distinguishes a fleet stop from
            ordinary idleness.
  alerts    mailed to 'human' by the probe itself, at most once per threshold
            window for the same unresolved stop. MAIL, not nudge — measured
            during the 2026-08-14 outage, mail is the only wake channel that
            survived both the idle gate and wake-silence suppression.
  delivery  $DELIVERY_NOTE

  It reports EITHER WAY. Check that the witness is alive, not just quiet:

      tail -5 $LEDGER
      $LAUNCHCTL print $DOMAIN/$LABEL | head -20

  A ledger whose newest line is hours old means the witness stopped, which looks
  identical to health if you only watch for alerts — and a witness that was not
  firing is the whole reason this job exists.
EOF
