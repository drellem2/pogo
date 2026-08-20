#!/usr/bin/env bash
# ARM the external redeploy witness (mg-a03d).
#
# WHAT THIS FIXES
#
# mg-ce10 landed scripts/revision-probe.sh — the external witness that the
# redeploy actually reached the daemon — and wired it to nothing. Measured on
# the merge commit: 501 lines, referenced by a changelog fragment, a docs
# section and test.sh, and by zero schedules, zero plists and zero callers. The
# rule the probe implements is that a detector for "X did not happen" must not
# be ACTIVATED BY X; a detector activated by NOTHING is the limiting case, and
# the witness was present by existence and absent by effect.
#
# This script is the arming, and it is a tracked shell script for the same
# reason the probe is: it must work on a box where the deploy has been failing
# for a week. `pogo service install-recovery` and `pogo service install-deploy`
# are the house pattern for installing a LaunchAgent, and both live in the `pogo`
# BINARY — which the redeploy installs. An arming step that needs a current
# `pogo` cannot be run on the box that needs arming. So: no `go`, no `pogo`, no
# `pogod`, asserted by scripts/install-revision-probe_test.sh with all three
# poisoned first on PATH.
#
# THE SUBSTRATE, AND WHAT THE WITNESS IS INDEPENDENT OF
#
#   INDEPENDENT OF                     because
#   --------------------------------   -----------------------------------------
#   pogod being alive                  launchd owns the trigger. A pogod that
#                                      does not answer is REPORTED (exit 2), not
#                                      a reason the probe fails to run.
#   the pogo / pogod binaries          never invoked; poisoned-PATH controls.
#   the deploy runner                  never invoked. A probe called by the
#                                      deploy cannot witness the deploy that
#                                      never fired — four of eight failing
#                                      nights (mg-2def) — which is driftwatch's
#                                      defect (mg-5bd2), not a fix for it.
#   an agent turn                      the probe mails `human` itself.
#   the refinery                       nothing in the path touches it.
#
#   DEPENDS ON                         and the consequence
#   --------------------------------   -----------------------------------------
#   launchd keeping the job loaded     boot it out and the witness is silent
#                                      with no alarm. The LEDGER's newest-line
#                                      age is how that becomes visible.
#   the host being powered ON at :20   launchd defers a fire across SLEEP, not
#                                      across shutdown. The 2026-08-07 no-fire
#                                      nights were a power-off; this witness
#                                      would have been dark for them too.
#   $SRC holding the tracked script    deploy-src is advanced by the deploy
#                                      runner's sync_src. A deploy that stops
#                                      firing freezes the probe's BODY at the
#                                      last sync — it does not stop the job
#                                      FIRING.
#
# FAILURE MODES IT WITNESSES
#
#   1. the nightly deploy never fired            running rev never changes while
#   2. the deploy fired and failed               main advances -> the clock
#                                                matures -> ALERT
#   3. the deploy fired, EXITED 0, and left the fleet on the old binary. This is
#      the case doctor reported on 2026-08-09: the 09:39 run reported success
#      with the daemon eight hours older than the merge it was supposed to carry.
#      Nothing else on this box asserts the RUNNING revision after a deploy.
#   4. pogod not running / not answering                  exit 2
#   5. pogod answering but unable to name its revision    exit 2, distinct state
#   6. the probe itself having stopped — visible in the ledger, by a reader.
#
# FAILURE MODES IT DOES NOT WITNESS — named, because an instrument's silence is
# only informative if its blind spots are written down
#
#   - a host powered off at every fire time (above).
#   - this job being booted out or its plist deleted. Nothing re-arms it.
#   - pogod running the CORRECT revision in the wrong run mode (index-only
#     serves /version happily). That is /server/mode and mg-6d2f's subject.
#   - ANY LONG-LIVED PROCESS THAT IS NOT pogod. The bridget reader, the notifier
#     pollers and the bridget supervisor have the identical defect — a merged
#     change sat inert in the bridget reader for two days (mg-c2f5 / mg-8158)
#     and nothing reported it. This ticket is scoped NARROW to pogod on purpose:
#     a pogod-only witness that runs beats a general one that does not, and the
#     general case is filed rather than implied.
#
# THE CIRCULARITY, so it is not rediscovered as a bug
#
# This job reaches a box through a merge and an install, and the install is part
# of the deploy it watches. So it can never witness the deploy that installed
# it — only the FIRST deploy after that, and every one thereafter. "It confirmed
# tonight's deploy" is not available as an acceptance criterion for the install
# itself, and asking for it would be asking the witness to predate its own
# arrival.
#
# USAGE
#
#   scripts/install-revision-probe.sh                 # install / re-install
#   scripts/install-revision-probe.sh --dry-run       # render and print, touch nothing
#   scripts/install-revision-probe.sh --uninstall     # bootout and remove
#   scripts/install-revision-probe.sh --src ~/dev/pogo --stale-after 12h
#
# EXIT STATUS
#
#   0  installed (or removed, or rendered under --dry-run) and verified loaded
#   2  refused — a precondition is not met, and nothing was changed

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LABEL="com.pogo.revisionprobe"
TEMPLATE="$HERE/launchd/$LABEL.plist"

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------

SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
URL="${POGO_SERVER_URL:-http://127.0.0.1:10000}"
STALE_AFTER="24h"
LA_DIR="${POGO_LAUNCHAGENTS_DIR:-$HOME/Library/LaunchAgents}"
LAUNCHCTL="${POGO_LAUNCHCTL:-}"
DRY_RUN=0
UNINSTALL=0

die() {
    echo "install-revision-probe: $*" >&2
    exit 2
}

usage() { sed -n '2,110p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --src) SRC="${2:-}"; shift 2 ;;
        --url) URL="${2:-}"; shift 2 ;;
        --stale-after) STALE_AFTER="${2:-}"; shift 2 ;;
        # --launch-agents-dir and --launchctl exist so the controls can install
        # into a fixture directory against a recording stub. An installer whose
        # only test is "run it on the real box and look" is one nobody runs.
        --launch-agents-dir) LA_DIR="${2:-}"; shift 2 ;;
        --launchctl) LAUNCHCTL="${2:-}"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option '$1' (try --help)" ;;
    esac
done

PLIST="$LA_DIR/$LABEL.plist"

# ---------------------------------------------------------------------------
# Preconditions — all of them checked BEFORE anything is written
# ---------------------------------------------------------------------------
# A half-installed LaunchAgent is worse than an absent one: it is a job that
# exists, appears in `launchctl list`, and cannot run. Every refusal below
# leaves the box exactly as it was.

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
        echo "install-revision-probe: removed $LABEL and $PLIST"
        echo "  The stamp and the ledger are LEFT IN PLACE — they are the record of what the"
        echo "  witness saw, and deleting them on uninstall would erase the evidence along with"
        echo "  the instrument."
    else
        "$LAUNCHCTL" bootout "$DOMAIN/$LABEL" >/dev/null 2>&1
        echo "install-revision-probe: no $PLIST to remove; booted out $LABEL in case it was loaded"
    fi
    exit 0
fi

[ -f "$TEMPLATE" ] || die "the plist template $TEMPLATE is missing — this script renders the tracked template rather than generating a copy, so that a change to the job cannot be made in one place and forgotten in the other"

PROBE="$SRC/scripts/revision-probe.sh"
[ -d "$SRC" ] || die "--src '$SRC' does not exist. That is the checkout the job runs the probe FROM; pass --src, or let the deploy create ~/.pogo/deploy-src first."
[ -f "$PROBE" ] || die "'$PROBE' is missing. Installing a job that points at an absent script produces a LaunchAgent that fails every hour and reports nothing — an alarm that is louder in the log than in anyone's inbox."
[ -x "$PROBE" ] || die "'$PROBE' is not executable"

case "$STALE_AFTER" in
    *[!0-9smhd]*|"") die "--stale-after '$STALE_AFTER' is not a duration (e.g. 90m, 24h, 2d)" ;;
esac

LOG_DIR="$HOME/Library/Logs/pogo"
LEDGER="$LOG_DIR/revision-probe.log"
REPORT="$LOG_DIR/revision-probe.report.log"
STAMP="$HOME/.pogo/revision-probe.stamp"

# ---------------------------------------------------------------------------
# Render
# ---------------------------------------------------------------------------
# Rendered FROM the tracked template, not from a copy of it held here. The house
# pattern (internal/service/recovery.go, internal/service/deploy.go) keeps a Go
# string that "mirrors" the in-repo plist, and mg-b201 is what that costs: the
# shipped plist and the installed one drifted, and it took a dedicated test
# (deploy_test.go) to notice. One source, no mirror, no drift class.

# `/Users/YOUR_USERNAME` in the template stands for $HOME, not for a literal
# /Users path. On this box they are the same string; keeping them separate is
# what lets the controls render into a fixture home instead of asserting against
# the developer's own (mg-78a5's whole subject).
for v in "$HOME" "$SRC" "$URL" "$STALE_AFTER"; do
    case "$v" in
        *'#'*) die "'$v' contains '#', which is the substitution delimiter used to render the plist. Refusing rather than emitting a mangled job." ;;
    esac
done

render() {
    sed -e "s#/Users/YOUR_USERNAME/\.pogo/deploy-src#__POGO_SRC__#g" \
        -e "s#/Users/YOUR_USERNAME#$HOME#g" \
        -e "s#__POGO_SRC__#$SRC#g" \
        -e "s#<string>http://127\.0\.0\.1:10000</string>#<string>$URL</string>#" \
        -e "s#<string>24h</string>#<string>$STALE_AFTER</string>#" \
        "$TEMPLATE"
}

RENDERED="$(render)" || die "could not render $TEMPLATE"

# THE CHECK THAT MATTERS: a surviving placeholder is a job that is installed,
# listed, and permanently broken — the exact "present by existence, absent by
# effect" shape this whole ticket is about, reproduced by its own fix.
# Scoped to <string> VALUES: the template's comments name the placeholder on
# purpose, to tell a hand-installer what to substitute. A placeholder surviving
# in a comment is documentation; one surviving in a value is a broken job.
#
# NEITHER SCAN BELOW MAY TEST A PIPELINE'S EXIT STATUS (mg-7ce7, mg-712e), and
# the two scans need that for DIFFERENT reasons.
#
# The placeholder scan's obvious spelling is
#
#     printf '%s' "$RENDERED" | grep '<string>' | grep -q 'YOUR_USERNAME'
#
# and it is the mg-7ce7 shape verbatim — the middle `grep` is an EXTERNAL
# producer feeding a consumer that leaves on first match, under `pipefail`.
# Measured on this box 2026-08-19: 0/5 against today's 10KB render, 5/5 SIGPIPE
# 141 against a 257KB one. That is what "latent" means, and 141 is not zero, so
# the `if` reads FALSE and the guard fails OPEN — a live placeholder installs.
# install-fleet-liveness-probe.sh already carries the safe spelling; this file
# was the remaining instance of the shape.
#
# The mention scan is pure shell for a second reason, and it is why mg-712e
# arrived here. Its message asserts "the template and this installer disagree",
# but the renderer and the checker read the SAME $HOME and $SRC in the same
# process, so a content disagreement about those is impossible by construction:
# every path this loop can name is one the render just substituted in. A
# pipeline's exit status was therefore the only way that sentence could be
# FALSE, and an alarm that can be false about a state it cannot detect is worse
# than no alarm — it is read as a real render defect and sends the reader to the
# template. mg-712e reproduced nothing (0 failures in 1,150 installs, a green
# full-suite gate, and 0/1,200 for a BUILTIN producer at up to 256KB with the
# match at byte 0). This closes the route; it does not diagnose that run.
PLACEHOLDER_HITS="$(printf '%s\n' "$RENDERED" | grep '<string>' | grep 'YOUR_USERNAME')"
if [ -n "$PLACEHOLDER_HITS" ]; then
    die "the rendered plist still has YOUR_USERNAME in a value — refusing to install a job that cannot run:
$PLACEHOLDER_HITS"
fi
for required in "$PROBE" "$SRC" "$LEDGER" "$STAMP"; do
    case "$RENDERED" in
        *"$required"*) ;;
        *) die "the rendered plist does not mention '$required' — the template and this installer disagree about the job's shape, and installing the result would arm something nobody described" ;;
    esac
done

# ---------------------------------------------------------------------------
# THE ARGUMENT VECTOR MUST BE ONE THE PROBE AT $SRC CAN PARSE
# ---------------------------------------------------------------------------
# Found by this script installing itself wrongly, which is worth recording. The
# plist and the probe are two tracked files that reach a box at DIFFERENT times:
# this installer runs from whatever checkout you invoked it from, while the job
# it writes points at $SRC, and $SRC is advanced by the deploy runner's sync_src.
# Install before that sync and the job names flags the probe there has never
# heard of. Measured on 2026-08-09: the first live install produced a LaunchAgent
# that was loaded, listed, correct-looking in `launchctl print`, and wrote
# `unknown option '--log'` once an hour forever.
#
# That is this ticket's own defect — present by existence, absent by effect —
# reproduced by its own remedy, which is the failure mode a fix is most exposed
# to and least likely to be checked for.
#
# Checked by EXECUTION, never by grepping --help text or comparing versions:
# appending --help to the real vector makes the probe parse every flag ahead of
# it and then exit 0 before doing anything. An unknown flag dies at the parse.
# The vector is read back out of the RENDERED plist rather than rebuilt here, so
# the thing checked is the thing installed.

PROBE_ARGS=""
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
    local args=() line
    while IFS= read -r line; do args+=("$line"); done <<EOF
$(extract_program_arguments)
EOF
    [ "${#args[@]}" -gt 1 ] \
        || die "could not read ProgramArguments back out of the rendered plist — refusing to install a job whose command line nothing has checked"
    PROBE_ARGS="${args[*]}"
    CAPCHECK_OUT="$("${args[@]}" --help 2>&1 >/dev/null)"
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
LaunchAgent that is loaded, listed, and writes 'unknown option' once an hour —
which is the exact defect this whole job exists to remove.

Bring the checkout forward and re-run:

    git -C $SRC fetch origin && git -C $SRC merge --ff-only origin/main
    $0

or install against a checkout that already has the matching probe:

    $0 --src /path/to/checkout"
fi

if [ "$DRY_RUN" -eq 1 ]; then
    echo "# would write $PLIST"
    echo "# would run:  $LAUNCHCTL bootout $DOMAIN/$LABEL   (ignoring failure — it may not be loaded)"
    echo "# would run:  $LAUNCHCTL bootstrap $DOMAIN $PLIST"
    echo
    printf '%s\n' "$RENDERED"
    exit 0
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
# with `Load failed: 5: Input/output error` and a re-install that silently
# no-ops would leave the OLD job definition running under the NEW plist's name,
# which is a difference nobody can see from `launchctl list`.
"$LAUNCHCTL" bootout "$DOMAIN/$LABEL" >/dev/null 2>&1
BOOTSTRAP_OUT="$("$LAUNCHCTL" bootstrap "$DOMAIN" "$PLIST" 2>&1)"
BOOTSTRAP_RC=$?
if [ "$BOOTSTRAP_RC" -ne 0 ]; then
    die "launchctl bootstrap failed (exit $BOOTSTRAP_RC): $BOOTSTRAP_OUT
The plist is at $PLIST. Nothing is loaded, so the witness is NOT armed."
fi

# ---------------------------------------------------------------------------
# Verify — by asking launchd, not by assuming the exit code
# ---------------------------------------------------------------------------
# `bootstrap` returning 0 means the plist was accepted, which is a claim about
# parsing and not about the job existing in the domain. This whole ticket exists
# because a component reported success for a thing that had not happened.

if ! "$LAUNCHCTL" print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
    die "launchctl bootstrap exited 0 but '$LAUNCHCTL print $DOMAIN/$LABEL' does not know the job. The witness is NOT armed."
fi

cat <<EOF
install-revision-probe: ARMED — $LABEL is loaded in $DOMAIN.

  plist    $PLIST
  program  $PROBE
  repo     $SRC
  daemon   $URL
  ledger   $LEDGER      (one line per run, green or red)
  reports  $REPORT
  stamp    $STAMP

  fires    hourly at :20, plus once now (RunAtLoad)
  replay   DEFERRED-ONCE across sleep — launchd delivers one run on wake for
           any number of fires missed while asleep. A fire missed because the
           host was POWERED OFF is lost outright; nothing replays it.
  alerts   mailed to 'human' by the probe itself, at most once per 12h for the
           same unresolved divergence (revision-probe.sh --renotify)

  It reports EITHER WAY. Check that the witness is alive, not just quiet:

      tail -5 $LEDGER
      $LAUNCHCTL print $DOMAIN/$LABEL | head -20

  A ledger whose newest line is hours old means the witness stopped, which
  looks identical to health if you only watch for alerts.
EOF
