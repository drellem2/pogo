#!/usr/bin/env bash
# Tests for scripts/pogo-self-deploy — the out-of-band pogod redeployer.
# Exercises the pure logic (JSON extraction, three-way drift classification)
# by sourcing the script (its main() is guarded by a BASH_SOURCE check) so no
# daemon, go install, or launchctl is touched.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RESULTS_FILE=$(mktemp)
trap 'rm -f "$RESULTS_FILE"' EXIT
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE"; }

# Source the driver — main() will NOT run because BASH_SOURCE != $0.
# shellcheck source=/dev/null
source "$HERE/pogo-self-deploy"

# --- json_str / json_num against a representative /agents/drain payload ---
DRAIN='{"draining":true,"count":2,"polecats":[{"name":"cat-a","pid":11,"work_item_id":"mg-aaaa","worktree_dir":"/wt/a","source_repo":"/repo"},{"name":"cat-b","pid":12,"work_item_id":"mg-bbbb","worktree_dir":"/wt/b","source_repo":"/repo"}]}'
[ "$(printf '%s' "$DRAIN" | json_num count)" = "2" ] \
    && pass "json_num extracts count" || fail "json_num count"
[ "$(printf '%s' "$DRAIN" | json_str draining)" = "" ] \
    && pass "json_str skips non-string (draining is bool)" || fail "json_str draining"
VER='{"revision":"abc123def456","time":"2026-07-14T00:45:56Z","modified":false}'
[ "$(printf '%s' "$VER" | json_str revision)" = "abc123def456" ] \
    && pass "json_str extracts revision" || fail "json_str revision"

# --- json_bool: the drain flag the restore trap reads back (mg-8b48) --------
# json_str CANNOT read `"draining":true` (asserted above), so the restore needs
# its own extractor. It must read BOTH values, and must yield EMPTY rather than
# guess when the field is absent or is not a bool — restore_drain turns an empty
# read into the "?" sentinel, and "?" is what stops it from ASSERTING a state it
# never observed.
[ "$(printf '%s' "$DRAIN" | json_bool draining)" = "true" ] \
    && pass "json_bool reads draining=true off a real drain payload" || fail "json_bool true ($(printf '%s' "$DRAIN" | json_bool draining))"
DRAIN_OFF='{"draining":false,"count":0,"polecats":[]}'
[ "$(printf '%s' "$DRAIN_OFF" | json_bool draining)" = "false" ] \
    && pass "json_bool reads draining=false (and does not collapse it to empty/true)" || fail "json_bool false"
# false must NOT read as absent: "off" and "unreadable" drive different branches
# in restore_drain — one restores, the other admits it is assuming.
[ -n "$(printf '%s' "$DRAIN_OFF" | json_bool draining)" ] \
    && pass "json_bool: draining=false is a VALUE, not an absence" || fail "json_bool false read as absent"
[ -z "$(printf '%s' '{"count":0,"polecats":[]}' | json_bool draining)" ] \
    && pass "json_bool yields empty when the field is absent (-> '?', never a guess)" || fail "json_bool absent field"
[ -z "$(printf '%s' '{"draining":"yes"}' | json_bool draining)" ] \
    && pass "json_bool yields empty for a non-bool value (-> '?', never a guess)" || fail "json_bool non-bool value"

# --- classify_drift: the four cases from the mg-6afa ruling ---
# The 4th arg is the installed CLI revision (mg-ddf1); it defaults to the
# installed pogod revision, which is the "both binaries moved together" state
# the original four cases were written against — so they still assert exactly
# what they always did.
# ${4-$2}, NOT ${4:-$2}: an explicitly-empty 4th arg means "no CLI on disk", and
# that is precisely the value that must not be read as "matches pogod".
#
# rev_in_repo is stubbed KNOWN here (mg-8f09): these cases exercise the
# STALENESS axis with symbolic revisions ("old"/"new"), which no real repo
# contains. Stubbing provenance-is-fine is what keeps them testing the one thing
# they were written to test. The provenance axis gets its own cases below, where
# the stub is what varies.
classify() {
    RUNNING="$1"; INSTALLED="$2"; MAIN="$3"; INSTALLED_CLI="${4-$2}"
    rev_in_repo() { return 0; }
    classify_drift
}

classify aaa aaa aaa
{ [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = false ] && [[ "$ACTION" == clean* ]]; } \
    && pass "clean: running==installed==main" || fail "clean case ($ACTION)"

# installed==main but running stale -> restart only, NO build
classify old new new
{ [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = true ] && [[ "$ACTION" == RESTART* ]]; } \
    && pass "restart-only: installed==main, running stale" || fail "restart-only case ($ACTION)"

# running==installed, both behind main -> build+restart
classify old old new
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = true ] && [[ "$ACTION" == BUILD* ]]; } \
    && pass "build+restart: running==installed behind main" || fail "build+restart case ($ACTION)"

# all three differ -> build+restart
classify a b c
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = true ]; } \
    && pass "build+restart: all three differ" || fail "all-differ case ($ACTION)"

# main unknown -> cannot classify (non-zero)
if classify aaa aaa "" ; then fail "empty main should fail classify"; else pass "empty main fails classify"; fi

# --- classify_drift sees CLI drift, not just pogod drift (mg-ddf1) ---------
# THE BUG, pinned. This is the exact state of the box on 2026-07-17: pogod built
# and running main, the CLI three days behind. The old classifier read only
# INSTALLED, so it called this "clean — nothing owed" and the redeploy's own
# drift detection could not see the drift. A build IS owed here, and no restart
# is: the daemon is already current.
classify new new new old
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = false ] && [[ "$ACTION" == BUILD\ owed* ]]; } \
    && pass "CLI drift alone owes a BUILD (the mg-ddf1 bug: pogod current, pogo stale)" \
    || fail "CLI-stale-only case: expected BUILD owed, no restart (NEEDS_BUILD=$NEEDS_BUILD NEEDS_RESTART=$NEEDS_RESTART ACTION=$ACTION)"

# ...and it must NAME the CLI, so the operator reading the report learns WHICH
# binary is behind. "a build is owed" without the name sends them to the daemon.
[[ "$ACTION" == *pogo* ]] && [[ "$ACTION" != *pogod\ behind* ]] \
    && pass "CLI-stale action names pogo (and does not blame pogod)" || fail "action must name the stale binary ($ACTION)"

# A MISSING CLI binary is drift, not a pass — and unlike an unstamped one, a
# build really is the fix, so it stays on the BUILD path (mg-8f09).
classify new new new "$REV_MISSING"
{ [ "$NEEDS_BUILD" = true ] && [[ "$ACTION" == BUILD\ owed* ]]; } \
    && pass "missing CLI owes a BUILD (a build is what fixes a missing binary)" || fail "missing CLI case ($ACTION)"

# The bare-empty spelling must behave the same: nothing may read as "matches".
classify new new new ""
{ [ "$NEEDS_BUILD" = true ] && [[ "$ACTION" == BUILD\ owed* ]]; } \
    && pass "empty CLI revision owes a BUILD (empty never reads as matches main)" || fail "empty CLI case ($ACTION)"

# The converse guard: a current CLI must not mask a stale pogod.
classify old old new new
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = true ]; } \
    && pass "stale pogod still owes BUILD+RESTART when the CLI is current" || fail "stale-pogod-current-CLI case ($ACTION)"

# --- THE THREE PROVENANCE STATES (mg-8f09) --------------------------------
# The stamp is EVIDENCE, not truth. These three cases are the whole ticket, and
# they are asserted in all three states — a check demonstrated only in its
# passing state is not evidence that it can fail.

# STATE 1 — stamp present and ours: comparable to main, and clean.
classify aaa aaa aaa
{ [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = false ] && [[ "$ACTION" == clean* ]]; } \
    && pass "state 1: a stamp from THIS repo that matches main is clean" || fail "state 1 ($ACTION)"

# STATE 2 — stamp ABSENT (the post-mg-2ce4 world, and the regression that would
# otherwise ship invisibly). Must NOT be clean, and must NOT be "behind" either:
# both are claims about a binary that has told us nothing.
unstamped() {
    RUNNING="$1"; INSTALLED="$2"; MAIN="$3"; INSTALLED_CLI="${4-$2}"
    rev_in_repo() { return 0; }
    classify_drift
}
unstamped "$REV_UNSTAMPED" "$REV_UNSTAMPED" aaa "$REV_UNSTAMPED"
if [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = false ] && [[ "$ACTION" == clean* ]]; then
    fail "state 2: an UNSTAMPED binary read as CLEAN — the mg-de08 defect (absence read as evidence)"
else
    pass "state 2: an unstamped binary is NOT clean"
fi
{ ! classify_drift; } \
    && pass "state 2: an unstamped binary REFUSES to classify (non-zero), so check exits 1" \
    || fail "state 2 must return non-zero"
[[ "$ACTION" == UNKNOWN\ PROVENANCE* ]] \
    && pass "state 2: verdict says provenance UNKNOWN" || fail "state 2 must say UNKNOWN ($ACTION)"
# Not "does the word 'behind' appear" — the verdict is allowed to say it is NOT
# behind. What it must never do is ASSERT behind-ness (the mg-49bc phrasing) for
# something whose ancestry it could not measure.
{ [[ "$ACTION" != *"behind main"* ]] && [[ "$ACTION" != BUILD* ]] && [[ "$ACTION" != RESTART* ]]; } \
    && pass "state 2: verdict does NOT assert 'behind main' (an unmeasured claim about ancestry)" \
    || fail "state 2 must not claim behind-ness it never measured ($ACTION)"
# It must name what it compared: the ref and the expected HEAD.
{ [[ "$ACTION" == *main* ]] && [[ "$ACTION" == *aaa* ]]; } \
    && pass "state 2: verdict names the ref and the expected HEAD it wanted" \
    || fail "state 2 must name what it compared ($ACTION)"
# A build does not clear an unstamped binary, so it must not be prescribed.
[ "$NEEDS_BUILD" = false ] \
    && pass "state 2: does NOT owe a build (a rebuild is unstamped too — that is a reconcile loop)" \
    || fail "state 2 must not prescribe a build"

# STATE 3 — stamp PRESENT but from a FOREIGN repo. This is today's live
# behavior: a binary built in a polecat worktree carries ~/.pogo's HEAD.
# rev_in_repo is the only thing that varies from state 1.
foreign() {
    RUNNING="$1"; INSTALLED="$2"; MAIN="$3"; INSTALLED_CLI="${4-$2}"
    rev_in_repo() { [ "$1" != "ffffdeadbeef" ]; }
    classify_drift
}
foreign ffffdeadbeef ffffdeadbeef aaaabbbbcccc
if [ "$NEEDS_BUILD" = false ] && [ "$NEEDS_RESTART" = false ] && [[ "$ACTION" == clean* ]]; then
    fail "state 3: a FOREIGN stamp read as clean"
else
    pass "state 3: a foreign stamp is NOT clean"
fi
{ ! classify_drift; } \
    && pass "state 3: a foreign stamp REFUSES to classify (loud, non-zero)" \
    || fail "state 3 must return non-zero"
[[ "$ACTION" == FOREIGN* ]] \
    && pass "state 3: verdict leads with FOREIGN STAMP" || fail "state 3 must say FOREIGN ($ACTION)"
# Name BOTH sides — the claimed revision and the repo/ref it is absent from.
# This is the mg-49bc lesson: "drift" alone sent the reader to look for a stale
# dirty build that did not exist.
[[ "$ACTION" == *ffffdeadbeef* ]] \
    && pass "state 3: verdict names the CLAIMED revision" || fail "state 3 must name the claimed rev ($ACTION)"
{ [[ "$ACTION" == *aaaabbbbcccc* ]] && [[ "$ACTION" == *main* ]]; } \
    && pass "state 3: verdict names the EXPECTED repo HEAD and ref" \
    || fail "state 3 must name what it expected ($ACTION)"
[[ "$ACTION" != *behind* ]] \
    && pass "state 3: verdict does NOT call a foreign commit 'behind'" \
    || fail "state 3 must not describe a foreign commit as behind ($ACTION)"

# The regression that made mg-49bc misread the box: a foreign stamp's
# vcs.modified=true describes the FOREIGN repo's tree, so a foreign stamp must
# never be explained as a stale-or-dirty local build.
[[ "$ACTION" != *dirty* ]] && [[ "$ACTION" != *stale* ]] \
    && pass "state 3: verdict does not blame a stale/dirty local tree (the mg-49bc misreading)" \
    || fail "state 3 must not reach for stale/dirty ($ACTION)"

# --- the two absences must stay apart (mg-de08's rule, mechanized) ---------
# MISSING owes a build; UNSTAMPED refuses. If these ever collapse back into one
# empty string, this is the test that says so.
classify new new new "$REV_MISSING"; missing_build="$NEEDS_BUILD"
unstamped new new new "$REV_UNSTAMPED"; unstamped_build="$NEEDS_BUILD"
{ [ "$missing_build" = true ] && [ "$unstamped_build" = false ]; } \
    && pass "missing != unstamped: one owes a build, the other refuses" \
    || fail "the two absences collapsed (missing_build=$missing_build unstamped_build=$unstamped_build)"

# Clean means BOTH match — the restart-only branch must not fire while the CLI
# is stale, because that branch SKIPS go install and would strand the CLI dark
# for another cycle. This is the regression that would silently re-open the bug.
classify old new new old
{ [ "$NEEDS_BUILD" = true ] && [ "$NEEDS_RESTART" = true ] && [[ "$ACTION" != RESTART* ]]; } \
    && pass "stale CLI blocks the restart-only branch (which skips go install)" \
    || fail "restart-only must not fire with a stale CLI ($ACTION)"

# --- stale_bins names exactly what is behind ------------------------------
INSTALLED=new INSTALLED_CLI=old MAIN=new
[ "$(stale_bins)" = "pogo" ] && pass "stale_bins: CLI only" || fail "stale_bins CLI only ($(stale_bins))"
INSTALLED=old INSTALLED_CLI=new MAIN=new
[ "$(stale_bins)" = "pogod" ] && pass "stale_bins: daemon only" || fail "stale_bins daemon only ($(stale_bins))"
INSTALLED=old INSTALLED_CLI=old MAIN=new
[ "$(stale_bins)" = "pogod, pogo" ] && pass "stale_bins: both" || fail "stale_bins both ($(stale_bins))"
INSTALLED=new INSTALLED_CLI=new MAIN=new
[ -z "$(stale_bins)" ] && pass "stale_bins: empty when nothing is behind" || fail "stale_bins clean ($(stale_bins))"

# --- DEPLOYED_CMDS is the coupling that stops this recurring --------------
# The drift check and the build BOTH iterate this list. If pogo ever falls out
# of it, the CLI goes dark again — and silently, which is the whole ticket.
case " ${DEPLOYED_CMDS[*]} " in
    *" pogod "*) pass "DEPLOYED_CMDS includes pogod" ;;
    *) fail "DEPLOYED_CMDS must include pogod (${DEPLOYED_CMDS[*]})" ;;
esac
case " ${DEPLOYED_CMDS[*]} " in
    *" pogo "*) pass "DEPLOYED_CMDS includes pogo — the CLI ships with the daemon" ;;
    *) fail "DEPLOYED_CMDS must include pogo (${DEPLOYED_CMDS[*]})" ;;
esac

# --- installed_bin resolves per-binary paths, honouring POGO_GOBIN ---------
( POGO_GOBIN=/tmp/gobin
  [ "$(installed_bin pogod)" = "/tmp/gobin/pogod" ] && [ "$(installed_bin pogo)" = "/tmp/gobin/pogo" ] ) \
    && pass "installed_bin resolves each binary under POGO_GOBIN" || fail "installed_bin per-name resolution"

# --- classify_drain_precondition: the mg-065e bootstrap disambiguation ---
# 2xx -> proceed with drain
[ "$(classify_drain_precondition 200)" = "drain" ] \
    && pass "drain-precond: 200 -> drain" || fail "drain-precond 200 ($(classify_drain_precondition 200))"
[ "$(classify_drain_precondition 204)" = "drain" ] \
    && pass "drain-precond: 204 -> drain" || fail "drain-precond 204"
# 404 -> bootstrap (server up, endpoint predates mg-cae1) — NOT "pogod down"
[ "$(classify_drain_precondition 404)" = "bootstrap" ] \
    && pass "drain-precond: 404 -> bootstrap" || fail "drain-precond 404 ($(classify_drain_precondition 404))"
# 000 / empty -> pogod genuinely unreachable (connection refused / timeout)
[ "$(classify_drain_precondition 000)" = "down" ] \
    && pass "drain-precond: 000 -> down" || fail "drain-precond 000"
[ "$(classify_drain_precondition "")" = "down" ] \
    && pass "drain-precond: empty -> down" || fail "drain-precond empty"
# any other status -> error:<code>, refuse rather than guess
[ "$(classify_drain_precondition 500)" = "error:500" ] \
    && pass "drain-precond: 500 -> error:500" || fail "drain-precond 500 ($(classify_drain_precondition 500))"
[ "$(classify_drain_precondition 401)" = "error:401" ] \
    && pass "drain-precond: 401 -> error:401" || fail "drain-precond 401"
# 503 -> stopped, its OWN disposition (mg-0155). It is the one non-2xx status
# that names a state rather than raising a question: RequireOrchestration is the
# only non-test source of a 503 on /agents/drain (handleDrain answers 200/400/405
# and nothing else), so it means "pogod is not in full mode" and nothing else.
# It used to fall into error:*, whose whole contract is "we do not know".
[ "$(classify_drain_precondition 503)" = "stopped" ] \
    && pass "drain-precond: 503 -> stopped (orchestration is stopped — a NAMED state, not a guess)" \
    || fail "drain-precond 503 ($(classify_drain_precondition 503))"
[ "$(classify_drain_precondition 503)" != "error:503" ] \
    && pass "drain-precond: 503 is no longer swept into the 'we do not know' branch" \
    || fail "drain-precond 503 still classifies as error:503"

# --- the reason channel: what crosses the process boundary (mg-0155) -------
# THE DEFECT, for the reader who finds this in a year. Each of these refusals
# already printed its own correct sentence and then exited 6 — and the unattended
# runner is a SEPARATE PROCESS that only received the 6. It re-derived a story
# from that integer and re-derived the wrong one: on 2026-08-07 the RED mail said
# "post-restart verification failed / the binary on disk is the NEW one" about a
# run that died at the first step with elapsed 0s and installed nothing.
#
# So these drive the REAL refusals — not a stub of them — and assert on the
# record the runner actually reads. Three things per disposition:
#   1. the exit code says where the run stopped (6: before the build; 12 when a
#      CONFIRMED `stopped` also means the fleet is down right now — mg-6d2f)
#   2. `reason` is this refusal's own headline, not another's
#   3. `installed=no`, which is what stops a remedy asserting an install
#
# Both arms throughout. A record that named the right failure while ALSO leaving
# the wrong ones in place would page just as badly.
precond_run() (
    # Run a real refusal the way cmd_redeploy does: reason channel armed, drain
    # trap armed, exit trap installed. Echoes the record file's path is not
    # possible from a subshell, so the caller passes one in.
    export POGO_DEPLOY_REASON_FILE="$2"
    REASON_FILE="$2"
    ERR_LOG="$(mktemp)"
    DEPLOY_STAGE="drain"
    DEPLOY_INSTALLED="no"
    DRAIN_PRIOR="?"
    DRAIN_ARMED=true
    # A restore, if one is attempted, fails — which is what happened on
    # 2026-08-07 and is what the disarm assertions below are about.
    drain_post() { echo 000; }
    # The `stopped` arm CONFIRMS its reading against /server/mode before it says
    # anything out loud (mg-6d2f), so the mode has to be driven from here. Stub
    # it, or this file's verdict depends on whatever daemon happens to be
    # listening on :10000 on the machine running the suite — and on a box whose
    # pogod is healthy, the confirmed-outage branch would never be reached at
    # all. $3 defaults to the 08-07 state, which is what every other assertion
    # in this block is about.
    PRECOND_STUB_MODE="${3:-index-only}"
    server_mode() { echo "$PRECOND_STUB_MODE"; }
    trap on_deploy_exit EXIT
    refuse_drain_precondition "$1"
)
rec_field() { sed -n "/^--- verbatim ---$/q; s/^$2=//p" "$1" | head -n 1; }

# want_rc per disposition. Three refusals say only WHERE the run stopped and
# share 6. `stopped`, once confirmed, also says what the world is like right now
# — the fleet is not dispatching — and that is what 12 carries to the runner,
# which picks the FLEET DOWN subject off it before it has read the record.
precond_want_rc() { case "$1" in stopped) echo 12 ;; *) echo 6 ;; esac; }

PRECOND_TMP="$(mktemp -d)"
for disp in bootstrap down stopped error:500; do
    f="$PRECOND_TMP/rec.${disp//:/_}"
    want="$(precond_want_rc "$disp")"
    out="$(precond_run "$disp" "$f" 2>&1)"; rc=$?
    printf '%s\n' "$out" > "$PRECOND_TMP/out.${disp//:/_}"
    [ "$rc" -eq "$want" ] \
        && pass "precond $disp: exits $want — the most an integer can honestly say about this refusal" \
        || fail "precond $disp exited $rc, want $want"
    [ -f "$f" ] \
        && pass "precond $disp: wrote a reason record for the runner to read" \
        || fail "precond $disp wrote no reason record"
    [ "$(rec_field "$f" installed)" = "no" ] \
        && pass "precond $disp: installed=no — the field that stops the alert asserting an install" \
        || fail "precond $disp installed=$(rec_field "$f" installed), want no"
    [ "$(rec_field "$f" stage)" = "drain" ] \
        && pass "precond $disp: stage=drain" || fail "precond $disp stage=$(rec_field "$f" stage)"
done

# The reason lines: each names ITS OWN failure. The four are asserted together
# because "the right story appears" and "the wrong ones do not" are two claims,
# and a channel that carried all four sentences under every disposition would
# satisfy the first alone.
case "$(rec_field "$PRECOND_TMP/rec.bootstrap" reason)" in
    *"predates the /agents/drain endpoint"*) pass "precond bootstrap: reason names the chicken-and-egg, by name" ;;
    *) fail "precond bootstrap reason: $(rec_field "$PRECOND_TMP/rec.bootstrap" reason)" ;;
esac
case "$(rec_field "$PRECOND_TMP/rec.down" reason)" in
    *"not answering"*) pass "precond down: reason says pogod did not answer" ;;
    *) fail "precond down reason: $(rec_field "$PRECOND_TMP/rec.down" reason)" ;;
esac
case "$(rec_field "$PRECOND_TMP/rec.stopped" reason)" in
    *"orchestration is STOPPED"*503*) pass "precond stopped: reason names orchestration AND the 503 that proved it" ;;
    *) fail "precond stopped reason: $(rec_field "$PRECOND_TMP/rec.stopped" reason)" ;;
esac
case "$(rec_field "$PRECOND_TMP/rec.error_500" reason)" in
    *"unexpected HTTP 500"*) pass "precond error:500: reason carries the STATUS, which is the whole of what is known" ;;
    *) fail "precond error:500 reason: $(rec_field "$PRECOND_TMP/rec.error_500" reason)" ;;
esac
# The wrong-story arm, stated as a matrix: no disposition's reason may name
# another's cause.
wrong=0
for pair in "bootstrap:not answering" "bootstrap:orchestration is STOPPED" \
            "down:predates" "down:orchestration is STOPPED" \
            "stopped:predates" "stopped:not answering" \
            "error_500:predates" "error_500:orchestration is STOPPED"; do
    r="$(rec_field "$PRECOND_TMP/rec.${pair%%:*}" reason)"
    case "$r" in *"${pair#*:}"*) fail "precond ${pair%%:*} reason also claims '${pair#*:}': $r"; wrong=1 ;; esac
done
[ "$wrong" -eq 0 ] && pass "precond: no disposition's reason line tells another disposition's story"

# reason is the HEADLINE, not the last line. The 503 refusal ends with "confirm
# with: curl -s .../server/mode"; a channel that took the last err line would
# describe the outage as a confirmation command.
case "$(rec_field "$PRECOND_TMP/rec.stopped" reason)" in
    *"confirm with"*) fail "precond stopped: reason took the LAST err line (the remedy), not the headline" ;;
    *) pass "precond stopped: reason is the first line of the burst, not the trailing remedy" ;;
esac
# ...and the remedy is not lost — it rides in the verbatim block.
grep -q "pogo server start" "$PRECOND_TMP/rec.stopped" \
    && pass "precond stopped: the verbatim block carries the one-command remedy" \
    || fail "precond stopped: verbatim block lost the remedy"
grep -q "start-orchestration" "$PRECOND_TMP/rec.stopped" \
    && pass "precond stopped: and the raw endpoint, for a box with no working CLI" \
    || fail "precond stopped: verbatim block lost the endpoint"

# --- the false-outage line the 503 refusal used to emit (mg-0155) ----------
# The restore trap is armed BEFORE the enabling POST, deliberately: a POST that
# times out AFTER pogod set the flag comes back 000, so the ambiguous case must
# still restore. That reasoning does not reach a status that is NOT ambiguous.
# On 2026-08-07 the 503 refusal ran the restore anyway, the restore POST hit the
# same 503, and the run emitted "drain restore FAILED (HTTP 503) — pogod may
# STILL be draining and dispatching NO polecats. The fleet will look healthy and
# do nothing." The fleet was never draining, and the command offered as the fix
# points at the endpoint that is refusing.
for disp in bootstrap stopped; do
    o="$(cat "$PRECOND_TMP/out.${disp}")"
    case "$o" in
        *"drain restore FAILED"*) fail "precond $disp: still reports a restore failure over a flag it never set" ;;
        *) pass "precond $disp: no false 'drain restore FAILED / fleet dispatching NO polecats' line" ;;
    esac
    case "$o" in
        *"restore disarmed"*) pass "precond $disp: says WHY it skipped the restore, rather than silently skipping" ;;
        *) fail "precond $disp: disarmed the restore without saying so" ;;
    esac
done
# The OTHER arm, and it is the one that matters more: `down` and `error:*` must
# STILL restore. A disarm that fired on every refusal would trade a false alarm
# for a real fleet-wide outage — dispatch left off with nothing to turn it back
# on. `down` is precisely the ambiguous case the arming order exists for.
for disp in down error_500; do
    case "$(cat "$PRECOND_TMP/out.${disp}")" in
        *"restoring dispatch"*) pass "precond ${disp}: STILL attempts the restore — the mutation cannot be ruled out here" ;;
        *) fail "precond ${disp}: skipped the restore on a path where drain may really be on" ;;
    esac
done

# --- the confirmed refusal says the fleet is down, in those words (mg-6d2f) -
# Not a rewording of "orchestration is stopped". The 08-07 alert was accurate
# and cost 10h39m anyway, because nothing in it distinguished a night when the
# deploy did not land from a night when nothing was running. The words a reader
# has to meet are the words that name the second.
case "$(cat "$PRECOND_TMP/out.stopped")" in
    *"THE FLEET IS DOWN"*"did not restart it"*)
        pass "precond stopped: says THE FLEET IS DOWN and that this run did not restart it — the 08-07 sentence that was missing" ;;
    *) fail "precond stopped: the confirmed outage does not say the fleet is down: $(cat "$PRECOND_TMP/out.stopped")" ;;
esac
grep -q "^reason=.*THE FLEET IS DOWN" "$PRECOND_TMP/rec.stopped" \
    && pass "precond stopped: and it is the HEADLINE, so it survives the process boundary into the RED mail" \
    || fail "precond stopped: 'THE FLEET IS DOWN' is not the reason line — the runner will not see it"

# --- and it is CONFIRMED, not inferred from three digits (mg-6d2f) ---------
# THE ARM THAT MATTERS. `stopped` is a hypothesis drawn from a status code, and
# this whole file's vocabulary exists because "I could not read it" kept getting
# rendered as a fact. A 503 whose mode endpoint does NOT agree is exactly that
# case — a middleware above the mux, a proxy in front of the port — and the
# right answer there is to report both readings and refuse, not to announce an
# outage that was just not confirmed. Driven by pointing the stub somewhere else.
for stubbed in full "$MODE_UNREACHABLE"; do
    f="$PRECOND_TMP/rec.disagree"
    o="$(precond_run stopped "$f" "$stubbed" 2>&1)"; rc=$?
    [ "$rc" -eq 6 ] \
        && pass "precond stopped/mode=$stubbed: exits 6, NOT the 12 that means 'there is an outage right now'" \
        || fail "precond stopped/mode=$stubbed exited $rc, want 6"
    case "$o" in
        *"THE FLEET IS DOWN"*) fail "precond stopped/mode=$stubbed: announced an outage it did not confirm" ;;
        *) pass "precond stopped/mode=$stubbed: does not narrate an outage the mode endpoint denies" ;;
    esac
    case "$o" in
        *"/server/mode reports '$stubbed'"*)
            pass "precond stopped/mode=$stubbed: reports BOTH readings — the 503 and what the mode endpoint actually said" ;;
        *) fail "precond stopped/mode=$stubbed: does not say what /server/mode reported: $o" ;;
    esac
done
rm -rf "$PRECOND_TMP"

# ...and it stays a NARROW claim. The neighbours must not be swept in: a 502 or a
# 504 says nothing about the run mode, and widening this to 5xx would put a
# confident outage story on top of a transport failure.
[ "$(classify_drain_precondition 502)" = "error:502" ] && [ "$(classify_drain_precondition 504)" = "error:504" ] \
    && pass "drain-precond: 502/504 stay error:<code> — only 503 carries the orchestration meaning" \
    || fail "drain-precond 5xx neighbours leaked into 'stopped'"

# --- server_mode: the reading verify_running cannot make (mg-6d2f) --------
# THE DEFECT, for the reader who finds this in a year. verify_running polled
# /version and nothing else. /version is deliberately NOT behind
# RequireOrchestration, so a pogod that restarted into index-only mode answers
# it, reports main's revision, and passes verification — while every /agents,
# /refinery and /scheduler call returns 503 and the fleet dispatches nothing.
# That is Daniel's report of the 08-07 night in one sentence: the deploy said it
# was done and the server was not running.
#
# These are the PURE half — base_url is pointed at a stub so the parse and the
# sentinels can be driven without a daemon. They prove server_mode DISTINGUISHES
# correctly. They deliberately do NOT prove the wiring; the live control owns
# that direction, against a real pogod whose mode it really changes.
# The stub's variable is deliberately NOT named `body`. bash scopes dynamically,
# so server_mode's own `local body` shadows the caller's for the whole call —
# including inside a stub the caller defined. A `body` here reads back empty (and
# under `set -u`, errors), which the driver then classifies as UNREACHABLE: the
# control would report the sentinel it was written to disprove, for a reason that
# has nothing to do with the code under test.
mode_stub() {  # $1: body to serve, or "" to simulate a curl failure
    local MODE_STUB_BODY="$1"
    if [ -z "$MODE_STUB_BODY" ]; then
        curl() { return 22; }
    else
        curl() { printf '%s' "$MODE_STUB_BODY"; }
    fi
    server_mode
}

[ "$(mode_stub '{"mode":"full"}')" = "full" ] \
    && pass "server_mode: full mode parses to 'full'" || fail "server_mode full ($(mode_stub '{"mode":"full"}'))"
[ "$(mode_stub '{"mode":"index-only"}')" = "index-only" ] \
    && pass "server_mode: index-only parses to 'index-only' — the state the fleet was in on 08-07" \
    || fail "server_mode index-only ($(mode_stub '{"mode":"index-only"}'))"
# THE DISTINCTION THAT MATTERS, and the one this file's whole vocabulary exists
# for: a daemon that will not talk and a daemon that talks but names no mode are
# different facts, and neither may render as a mode. If either collapsed to ""
# the caller's `[ "$mode" = "full" ]` would be false for all three and the
# operator would be told "index-only" about a box that never answered.
[ "$(mode_stub '')" = "$MODE_UNREACHABLE" ] \
    && pass "server_mode: an unreachable daemon yields the UNREACHABLE sentinel, never a mode" \
    || fail "server_mode unreachable ($(mode_stub ''))"
[ "$(mode_stub '{"revision":"abc"}')" = "$MODE_UNSTAMPED" ] \
    && pass "server_mode: a body with no mode field yields UNREPORTED, not an empty string" \
    || fail "server_mode unstamped ($(mode_stub '{"revision":"abc"}'))"
# The sentinels can never collide with a real mode — same rule REV_* obey.
case "$MODE_UNREACHABLE$MODE_UNSTAMPED" in
    *full*|*index-only*) fail "a MODE_* sentinel contains a real mode name — it could be mistaken for one" ;;
    *) pass "MODE_* sentinels cannot collide with a real mode string" ;;
esac

# --- verify_orchestration: RED on index-only, GREEN on full ---------------
# Both directions, because a check that only ever refuses is a brick and one that
# only ever passes is decoration. Driven through the real function with
# server_mode stubbed; ORCHESTRATION_VERIFY_TIMEOUT=0 so the RED does not pay the
# 60s a real deploy allows for startup.
vo_run() {  # $1: mode server_mode should report -> echoes "<rc>|<stderr>"
    (
        # Captured into a named variable, not read as $1 inside the stub: a `$1`
        # there is server_mode's own first argument, which is unset.
        VO_STUB_MODE="$1"
        server_mode() { echo "$VO_STUB_MODE"; }
        ORCHESTRATION_VERIFY_TIMEOUT=0
        local out rc=0
        out="$(verify_orchestration 2>&1)" || rc=$?
        printf '%s|%s' "$rc" "$out"
    )
}

VO_FULL="$(vo_run full)"
[ "${VO_FULL%%|*}" = "0" ] \
    && pass "verify_orchestration: mode=full passes — the check is conditional, not a brick" \
    || fail "verify_orchestration refused a full-mode daemon ($VO_FULL)"

VO_IDX="$(vo_run index-only)"
case "$VO_IDX" in
    1\|*THE\ FLEET\ IS\ DOWN*)
        pass "verify_orchestration: mode=index-only FAILS and says THE FLEET IS DOWN — the 08-07 state now stops the deploy" ;;
    0\|*)
        fail "FAIL-OPEN: verify_orchestration passed an index-only daemon — a deploy would report success over a dead fleet" ;;
    *)
        fail "verify_orchestration on index-only returned '$VO_IDX'" ;;
esac
# An unreachable daemon must also fail. It is a DIFFERENT fact from index-only
# and the message names it, but the verdict is the same: not proven up.
VO_UNK="$(vo_run "$MODE_UNREACHABLE")"
[ "${VO_UNK%%|*}" = "1" ] \
    && pass "verify_orchestration: an unreachable daemon fails too — 'could not read it' is not 'it is up'" \
    || fail "verify_orchestration passed an unreachable daemon ($VO_UNK)"

# --- cmd_redeploy WIRES verify_orchestration in, after verify_running -----
# The assertions above prove the function can refuse. This one proves the
# refusal is reachable from the real deploy path — the seam mg-c02d's lesson is
# about, checked here by source because the live control cannot run a real
# launchctl kickstart. Order matters and is asserted: verify_running first, so
# "no pogod at all" and "a pogod that is not serving the fleet" keep their own
# exit codes instead of collapsing into one.
SELF_DEPLOY_SRC="$HERE/pogo-self-deploy"
if grep -q 'verify_running || exit 8' "$SELF_DEPLOY_SRC" \
   && grep -q 'verify_orchestration || exit 11' "$SELF_DEPLOY_SRC" \
   && [ "$(grep -n 'verify_running || exit 8' "$SELF_DEPLOY_SRC" | cut -d: -f1)" \
        -lt "$(grep -n 'verify_orchestration || exit 11' "$SELF_DEPLOY_SRC" | cut -d: -f1)" ]; then
    pass "cmd_redeploy runs verify_orchestration (exit 11) AFTER verify_running (exit 8) — both checks, in order"
else
    fail "cmd_redeploy does not wire verify_running -> verify_orchestration; the post-restart check is back to reading /version alone"
fi

# --- drain_wait: the gate that used to fail OPEN (mg-65b2) -----------------
# THE DEFECT, for the reader who finds this in a year. drain_wait used to end
# with `count="${count:-0}"`, and `curl -sf` yields an EMPTY body on ANY failure
# — refused connection, timeout, 5xx, non-JSON. So "I could not read it" became
# "there are zero polecats", on the FIRST poll, and the drain reported quiesced
# without waiting. The redeploy then kickstart -k'd a LIVE fleet, minting
# survivors that setsid out of the process group and go invisible forever — with
# no --force anywhere. Measured against a dead port, not inferred.
#
# These are the PURE half: drain_probe and witness_alive_count are stubbed so the
# decision table can be driven without a daemon. They prove drain_wait DECIDES
# correctly. They deliberately do NOT prove the wiring — a stubbed curl is not a
# curl, and this whole ticket exists because a real curl's empty body meant
# something nobody checked. The live control (pogo-self-deploy_live_test.sh)
# owns that direction, against a real pogod it really kills.
#
# The contract under test:
#   rc 0 + count  — quiesced, safe to bounce
#   rc 1 + count  — deadline passed, polecats still active
#   rc 2 + "?"    — CANNOT TELL; refuse (--force overrides, in cmd_redeploy)
dw() (
    # Run drain_wait in a subshell with stubs, echoing "<stdout>|<rc>".
    DRAIN_TIMEOUT="${DW_TIMEOUT:-5}"
    DRAIN_UNREADABLE_SLEEP=0   # the retry POLICY is under test, not the clock
    local out rc=0
    out="$(drain_wait 2>/dev/null)" || rc=$?
    echo "$out|$rc"
)

# A healthy readout of zero: quiesced. The NEGATIVE direction — without this the
# assertions below are satisfied by a drain_wait that refuses unconditionally,
# which would be a gate that never opens rather than a gate that works.
drain_probe() { printf '%s\n200' '{"draining":true,"count":0,"polecats":[]}'; }
[ "$(dw)" = "0|0" ] \
    && pass "drain_wait: a healthy readout of 0 still quiesces (the refusal is CONDITIONAL, not hard-wired)" \
    || fail "drain_wait: healthy zero did not quiesce ($(dw)) — the gate never opens"

# A healthy readout of N HOLDERS, past the deadline: the timeout path, which must
# survive mg-853a's narrowing. It is what exit 7 has always hung off.
#
# The body now has to carry real polecat records, because since mg-853a `count`
# alone no longer decides anything — three polecats with no worktree_dir at all
# would clear. These three name worktrees that do not exist, so each is `unknown`
# and each HOLDS: the rc-1 shape is reached through the new predicate rather than
# around it. (The equivalent through `owes` needs real git and is asserted in the
# mg-853a section further down, against a branch really pushed to a real origin.)
DW_HOLD_BODY='{"draining":true,"count":3,"polecats":[{"name":"a","pid":1,"work_item_id":"mg-a","worktree_dir":"/nonexistent/dw/a"},{"name":"b","pid":2,"work_item_id":"mg-b","worktree_dir":"/nonexistent/dw/b"},{"name":"c","pid":3,"work_item_id":"mg-c","worktree_dir":"/nonexistent/dw/c"}]}'
drain_probe() { printf '%s\n200' "$DW_HOLD_BODY"; }
[ "$(DW_TIMEOUT=0 dw)" = "3|1" ] \
    && pass "drain_wait: N holding the drain at the deadline -> rc 1 and the COUNT (the exit-7 timeout path survives the narrowing)" \
    || fail "drain_wait: timeout path regressed ($(DW_TIMEOUT=0 dw))"

# (1) A MISSING SAMPLE IS NOT A MEASUREMENT. The cheapest part of the fix and
# probably most real occurrences: one transient failure must cost a re-poll, not
# end the drain. The stub fails once and then answers honestly.
#
# THE CALL COUNTER LIVES IN A FILE, NOT A VARIABLE, AND THAT IS NOT FUSSINESS.
# drain_wait probes via `raw="$(drain_probe)"`, so every stub call runs in its
# own command-substitution SUBSHELL and a `DW_CALLS=$((DW_CALLS+1))` increments a
# copy that dies with it — the counter reads 0 forever, the stub returns its
# first-call answer every time, and the assertion passes without ever exercising
# the recovery it names. The first draft of this test did exactly that and went
# green against code that could not have worked. A control that cannot fail is
# not a control (mg-c02d), and a shell test whose state evaporates is one.
DW_STATE="$(mktemp)"
trap 'rm -f "$RESULTS_FILE" "$DW_STATE"' EXIT
dw_calls() { cat "$DW_STATE" 2>/dev/null || echo 0; }
dw_bump()  { echo $(( $(dw_calls) + 1 )) > "$DW_STATE"; }

echo 0 > "$DW_STATE"
drain_probe() {
    dw_bump
    if [ "$(dw_calls)" -eq 1 ]; then printf '\n000'    # curl: connection refused
    else printf '%s\n200' '{"draining":true,"count":0,"polecats":[]}'; fi
}
DW_RES="$(dw)"
{ [ "$DW_RES" = "0|0" ] && [ "$(dw_calls)" -ge 2 ]; } \
    && pass "drain_wait: ONE unreadable sample -> polls again ($(dw_calls) probes) and then MEASURES zero (does not conclude from it)" \
    || fail "drain_wait: a transient blip was not re-polled ($DW_RES after $(dw_calls) probe(s))"

# Now the RED's exact shape: a readout that NEVER answers. The old code took
# `${count:-0}` -> 0 -> quiesced off sample 1 and never probed twice. Asserting
# the probe COUNT is what pins the difference — a fix that still decided on the
# first sample would satisfy every verdict assertion above while reproducing the
# bug, because "refuse" and "refuse immediately" have the same stdout.
echo 0 > "$DW_STATE"
drain_probe() { dw_bump; printf '\n000'; }
witness_alive_count() { echo 0; return 0; }
DW_RES="$(dw)"
[ "$(dw_calls)" -ge "$DRAIN_UNREADABLE_LIMIT" ] \
    && pass "drain_wait: an unreadable readout is probed $(dw_calls)x (>= $DRAIN_UNREADABLE_LIMIT) before it means anything — the old code decided on sample 1" \
    || fail "drain_wait: decided after $(dw_calls) sample(s) — a single missing sample must never be a verdict"

# (2)+(3) SUSTAINED SILENCE -> classify with the classifier we ALREADY have, then
# consult the SECOND witness rather than guess. pogod down + witness says idle:
# PROCEED, by right. The bounce is the repair and strands nothing that is not
# already stranded (mg-61a0) — this is the case a blanket refuse-on-unreachable
# would have broken, blocking the repair at the moment it is needed.
drain_probe() { printf '\n000'; }
witness_alive_count() { echo 0; return 0; }
[ "$(dw)" = "0|0" ] \
    && pass "drain_wait: pogod down + witness reports NO live polecat -> PROCEED (the wedged-pogod repair is not blocked)" \
    || fail "drain_wait: down+idle did not proceed ($(dw)) — the repair path is blocked"

# pogod down + witness says a polecat IS alive: REFUSE. Bouncing here mints
# PERMANENT survivors — they outlive kickstart -k and go dark forever.
witness_alive_count() { echo 2; return 0; }
[ "$(dw)" = "?|2" ] \
    && pass "drain_wait: pogod down + witness reports LIVE polecats -> REFUSE with '?' (never a fabricated 0)" \
    || fail "drain_wait: down+live did not refuse ($(dw)) — this is the fail-open that mints survivors"

# DOUBLE ABSENCE: pogod silent AND the witness cannot answer. Genuinely unknown
# — nothing left to consult — so fail closed. This is mg-13a3's thesis one layer
# up: never conclude "drained" from a single absence, let alone two.
witness_alive_count() { echo "?"; return 1; }
[ "$(dw)" = "?|2" ] \
    && pass "drain_wait: DOUBLE ABSENCE (pogod down + witness unreadable) -> fails CLOSED with '?'" \
    || fail "drain_wait: double absence did not fail closed ($(dw))"

# REACHABLE but unreadable — a LIVE pogod whose count we cannot see. The witness
# must NOT be consulted here: it knows nothing about polecats this pogod holds in
# a registry we just failed to read, so a 0 from it would be a fresh fail-open.
# Refuse instead; --force already means "I know it's wedged, bounce it anyway".
drain_probe() { printf '%s\n503' '{"error":"overloaded"}'; }
witness_alive_count() { echo 0; return 0; }   # would say "idle" — must be ignored
[ "$(dw)" = "?|2" ] \
    && pass "drain_wait: a LIVE pogod with an unreadable count -> REFUSE (the witness cannot speak for a registry we could not read)" \
    || fail "drain_wait: reachable-but-unreadable did not refuse ($(dw)) — a live pogod would be bounced blind"

# A 2xx whose BODY does not parse is the same fact as a 5xx: reachable, and we
# still cannot count. It must not fall through to `-eq 0` on an empty string.
drain_probe() { printf '%s\n200' '{"draining":true,"polecats":[]}'; }   # no count field
[ "$(dw)" = "?|2" ] \
    && pass "drain_wait: 2xx with an UNPARSEABLE body -> REFUSE (an absent count is not a zero one)" \
    || fail "drain_wait: an unparseable 2xx body was treated as a measurement ($(dw))"

# Put the REAL functions back. `unset -f` would not do it: bash keeps no stack of
# shadowed definitions, so unsetting a stub deletes the function outright and
# every assertion below would measure a "command not found" instead of the code.
# shellcheck source=/dev/null
source "$HERE/pogo-self-deploy"

# --- witness_alive_count: EMPTY-never-0, at the CLI seam (mg-65b2) ----------
# The drain's second witness is reached by shelling to `pogo agent witness`, and
# every way that hop can fail must land on "?" — never on a confident 0. The
# hazard is concrete: the `pogo` on PATH during a drain is the one from the LAST
# deploy, so an old CLI that has never heard of this subcommand is the EXPECTED
# case on the first night this ships, not an exotic one.
POGO_CLI_STUB="$(mktemp)"; chmod +x "$POGO_CLI_STUB"
trap 'rm -f "$RESULTS_FILE" "$DW_STATE" "$POGO_CLI_STUB"' EXIT
wac() { POGO_CLI="$POGO_CLI_STUB" witness_alive_count 2>/dev/null; echo "|$?"; }

printf '#!/bin/bash\necho %s\n' "'{\"witness_present\":true,\"alive_count\":0,\"alive\":[]}'" > "$POGO_CLI_STUB"
[ "$(wac)" = "0
|0" ] && pass "witness_alive_count: a readable witness reporting 0 is a MEASUREMENT (rc 0)" \
      || fail "witness_alive_count: readable zero not reported ($(wac))"

printf '#!/bin/bash\necho %s\n' "'{\"witness_present\":true,\"alive_count\":2,\"alive\":[{\"name\":\"a\",\"pid\":1}]}'" > "$POGO_CLI_STUB"
[ "$(wac)" = "2
|0" ] && pass "witness_alive_count: reads a live count off the CLI's compact JSON" \
      || fail "witness_alive_count: live count not read ($(wac))"

# rc 2 = no witness file. An ABSENCE, not a zero — the whole reason the CLI
# spends an exit code on it.
printf '#!/bin/bash\necho %s\nexit 2\n' "'{\"error\":\"no polecat witness at /x\"}'" > "$POGO_CLI_STUB"
[ "$(wac)" = "?
|1" ] && pass "witness_alive_count: an ABSENT witness yields '?' (never 0 — an unwritten witness is not an idle fleet)" \
      || fail "witness_alive_count: absent witness did not yield '?' ($(wac))"

# rc 1 = a witness exists and could not be read.
printf '#!/bin/bash\necho %s\nexit 1\n' "'{\"error\":\"parse error\"}'" > "$POGO_CLI_STUB"
[ "$(wac)" = "?
|1" ] && pass "witness_alive_count: an UNREADABLE witness yields '?'" \
      || fail "witness_alive_count: unreadable witness did not yield '?' ($(wac))"

# The old-CLI case, exactly as cobra fails it: a usage dump on stderr, non-zero,
# no JSON at all. Must not parse as anything.
printf '#!/bin/bash\necho "Error: unknown command \\"witness\\" for \\"pogo agent\\"" >&2\nexit 1\n' > "$POGO_CLI_STUB"
[ "$(wac)" = "?
|1" ] && pass "witness_alive_count: an OLD pogo that has never heard of 'agent witness' yields '?' (fails CLOSED, the expected first-night case)" \
      || fail "witness_alive_count: an old CLI did not fail closed ($(wac))"

# Absent binary entirely — launchd hands jobs a minimal PATH (the sink already
# learned this the hard way).
POGO_CLI_SAVE="$POGO_CLI_STUB"; POGO_CLI_STUB="/nonexistent/pogo-$$"
[ "$(wac)" = "?
|1" ] && pass "witness_alive_count: a MISSING pogo binary yields '?' (minimal-PATH launchd case)" \
      || fail "witness_alive_count: missing binary did not yield '?' ($(wac))"
POGO_CLI_STUB="$POGO_CLI_SAVE"

# A 0 that is NOT accompanied by rc 0 must never be believed: this is the
# EMPTY-never-0 rule (mg-76e5) at this seam. A CLI that fails while printing a
# zero-shaped body is exactly how a fail-open sneaks back in.
printf '#!/bin/bash\necho %s\nexit 1\n' "'{\"witness_present\":true,\"alive_count\":0,\"alive\":[]}'" > "$POGO_CLI_STUB"
[ "$(wac)" = "?
|1" ] && pass "witness_alive_count: a FAILING CLI that prints alive_count:0 is still '?' (the exit code decides, not the body)" \
      || fail "witness_alive_count: believed a zero from a failed CLI ($(wac)) — the fail-open, rebuilt at the new seam"

# --skip-drain flag defaults false and is settable (bootstrap remedy)
[ "$SKIP_DRAIN" = false ] && pass "skip-drain defaults false" || fail "skip-drain default"

# --- mail-check post-check: the mg-de08 positive control -------------------
# A check that cannot fail is not a check. The FIRST assertion below is the one
# that earns the rest: it drives the exact live incident — five crew mail-checks
# present before a bounce, reaped as agent_gone during it, zero after — and
# proves the check reports RED. Only then is a green from it worth anything.

# Sets, one id per line — the shape the driver really passes (mg-ea3e).
FIVE=$'mail-check-architect\nmail-check-mayor\nmail-check-pa\nmail-check-pm-dealdesk\nmail-check-pm-pogo'
FOUR=$'mail-check-architect\nmail-check-mayor\nmail-check-pa\nmail-check-pm-dealdesk'

# The 2026-07-17 outage: 5 mail-checks in, 0 out, no polecats killed.
[ "$(classify_mail_check_restore "$FIVE" "" "")" = "missing:mail-check-architect mail-check-mayor mail-check-pa mail-check-pm-dealdesk mail-check-pm-pogo" ] \
    && pass "post-check FAILS on the live mg-de08 incident (5 reaped -> all 5 NAMED)" \
    || fail "post-check did not fire on the mg-de08 incident ($(classify_mail_check_restore "$FIVE" "" ""))"
# A partial loss must fire too — the outage was only caught because ONE agent's
# heartbeat went stale; four surviving loops must not mask the fifth. And the
# verdict must say WHICH agent lost its loop: that is the name you go nudge.
[ "$(classify_mail_check_restore "$FIVE" "$FOUR" "")" = "missing:mail-check-pm-pogo" ] \
    && pass "post-check FAILS on a partial loss and NAMES the one that went (5 -> 4)" \
    || fail "post-check missed a partial loss ($(classify_mail_check_restore "$FIVE" "$FOUR" ""))"

# ...and the other direction: an intact fleet is OK, or the check cries wolf on
# every deploy and gets ignored — which is how mg-de08 stayed quiet.
[ "$(classify_mail_check_restore "$FIVE" "$FIVE" "")" = "ok" ] \
    && pass "post-check passes when every mail-check survives" || fail "post-check false alarm (5 -> 5)"
[ "$(classify_mail_check_restore "$FIVE" "$FIVE"$'\nmail-check-mg-9999' "")" = "ok" ] \
    && pass "post-check passes when a mail-check is ADDED (5 -> 6)" || fail "post-check false alarm on an added schedule"
[ "$(classify_mail_check_restore "" "" "")" = "ok" ] \
    && pass "post-check passes on an empty fleet (0 -> 0)" || fail "post-check false alarm on empty fleet"
# Same count in, same count out, but they are NOT THE SAME SCHEDULES. The old
# count arithmetic called this OK — 5 >= 5 — while a crew agent sat mute and a
# fresh polecat's loop stood in for it. Identity is the only thing that sees it.
[ "$(classify_mail_check_restore "$FIVE" "$FOUR"$'\nmail-check-mg-9999' "")" = "missing:mail-check-pm-pogo" ] \
    && pass "post-check FAILS on a swap: 5 in, 5 out, but pm-pogo's loop is gone" \
    || fail "a swap slipped through — the check is still counting ($(classify_mail_check_restore "$FIVE" "$FOUR"$'\nmail-check-mg-9999' ""))"

# A --force bounce kills polecats; their mail-checks are reaped ON PURPOSE.
# Counting that as damage would cry wolf on every forced deploy. Slack is the
# SET of schedules those polecats held — granted by name, spendable only on the
# name it was granted for.
SLACK2=$'mail-check-mg-aaaa\nmail-check-mg-bbbb'
PRE7="$FIVE"$'\nmail-check-mg-aaaa\nmail-check-mg-bbbb'
[ "$(classify_mail_check_restore "$PRE7" "$FIVE" "$SLACK2")" = "ok" ] \
    && pass "post-check tolerates the loss of 2 force-killed polecats' own loops" \
    || fail "post-check flagged an expected polecat loss ($(classify_mail_check_restore "$PRE7" "$FIVE" "$SLACK2"))"
# But slack must not swallow a real loss beyond the polecats that died.
[ "$(classify_mail_check_restore "$PRE7" "$FOUR" "$SLACK2")" = "missing:mail-check-pm-pogo" ] \
    && pass "post-check still fires on a loss beyond the force-killed polecats" \
    || fail "slack swallowed a real loss ($(classify_mail_check_restore "$PRE7" "$FOUR" "$SLACK2"))"

# THE mg-ea3e CASE, at the classifier. Two polecats die on --force; only ONE of
# them had a mail-check. Slack is therefore ONE schedule, not two — and the crew
# loss it would otherwise have paid for is reported, by name.
#
# Under the old count this was: pre 6, post 4, slack 2 (two dead polecats) ->
# 4 >= 6-2 -> OK. A real crew loss, absorbed in silence by an allowance the
# no-mail-check polecat never earned. That is the bug, and this is the assertion
# that would have caught it.
PRE6="$FIVE"$'\nmail-check-mg-aaaa'   # 5 crew + ONE polecat with a loop; mg-bbbb has none
SLACK1=$'mail-check-mg-aaaa'          # only mg-aaaa had a schedule to lose
[ "$(classify_mail_check_restore "$PRE6" "$FOUR" "$SLACK1")" = "missing:mail-check-pm-pogo" ] \
    && pass "mg-ea3e: a no-mail-check polecat's death buys NO slack — the crew loss is NAMED" \
    || fail "mg-ea3e: crew loss hid behind a dead polecat's allowance ($(classify_mail_check_restore "$PRE6" "$FOUR" "$SLACK1"))"
# Slack for a schedule that never existed is not slack for some other schedule.
# Reasoning about identity means an unearned allowance cannot be SPENT anywhere,
# not merely that it is smaller.
[ "$(classify_mail_check_restore "$PRE6" "$FOUR" $'mail-check-mg-aaaa\nmail-check-mg-bbbb')" = "missing:mail-check-pm-pogo" ] \
    && pass "mg-ea3e: slack naming an absent schedule cannot be spent on a present one" \
    || fail "mg-ea3e: phantom slack was redeemed against a real loss"

# Unreachable pogod -> unknown, never "everything is gone". mail_check_ids
# echoes the "?" sentinel (not an empty set) when curl fails, because with sets
# an empty string is a legitimate answer — "reachable, holds nothing" — and it
# must not read as a fleet-wide wipe.
[ "$(classify_mail_check_restore "$FIVE" "?" "")" = "unknown" ] \
    && pass "post-check reports unknown (not FAILED) when the post-set is unreadable" \
    || fail "unreadable post-set misclassified ($(classify_mail_check_restore "$FIVE" "?" ""))"
[ "$(classify_mail_check_restore "?" "$FIVE" "")" = "unknown" ] \
    && pass "post-check reports unknown when the pre-set is unreadable" || fail "unreadable pre-set misclassified"
# ...and the distinction the sentinel exists to protect: a REACHABLE daemon
# holding nothing is a fact, not an outage, and must not be laundered into one.
[ "$(classify_mail_check_restore "" "" "")" = "ok" ] \
    && pass "an empty set is a fact (0 -> 0 = ok), a '?' is an absence of one (-> unknown)" \
    || fail "empty set did not read as a real, reachable, empty fleet"

# --- what the FAILED branch tells the reader to DO (mg-6d7b) ----------------
# This check reads schedules and never the agent registry, so it can establish
# that a mail-check is gone and cannot establish WHY. Those are two causes with
# opposite remedies: an agent that survived can re-register on a nudge, and an
# agent that is GONE took its schedule with it and has nothing to nudge. For
# eight months this branch printed the first one unconditionally, and on
# 2026-08-10 it fired on the second — doctor was absent from `pogo agent list`
# entirely. A nudge into the void reports nothing worth noticing, so following
# it ends with the fleet recorded as restored and the mail loop still dead.
#
# Exercised, not grepped: the remedy has to be in the OUTPUT of the branch that
# fires, and a string that moved to a comment is not in the output.
MC_OUT="$(
    MAIL_CHECK_TIMEOUT=0
    mail_check_ids() { printf 'mail-check-mayor'; }   # the doctor loop is gone
    verify_mail_checks_restored "$(printf 'mail-check-mayor\nmail-check-doctor')" "" 2>&1
)"
case "$MC_OUT" in
    *"mail-check-doctor"*) pass "self-deploy: the FAILED branch still names the lost schedule" ;;
    *) fail "self-deploy: the lost schedule is not named: $MC_OUT" ;;
esac
case "$MC_OUT" in
    *"pogo agent start <agent>"*) pass "self-deploy: the branch offers the remedy for an agent that is GONE" ;;
    *) fail "self-deploy: the only remedy offered is still the one that cannot work when the agent is gone: $MC_OUT" ;;
esac
case "$MC_OUT" in
    *"pogo nudge <agent>"*) pass "self-deploy: it KEEPS the nudge for the case that one is right for" ;;
    *) fail "self-deploy: the nudge remedy was dropped rather than conditioned: $MC_OUT" ;;
esac
case "$MC_OUT" in
    *"depends on"*|*"DEPENDS ON"*) pass "self-deploy: the two remedies are printed as CONDITIONAL, not as a list to try" ;;
    *) fail "self-deploy: two opposite remedies are printed with nothing saying which applies: $MC_OUT" ;;
esac
case "$MC_OUT" in
    *"pogo agent list"*) pass "self-deploy: it names the command that resolves the condition" ;;
    *) fail "self-deploy: the reader is given a condition and no way to evaluate it: $MC_OUT" ;;
esac
# The exact sentence the 2026-08-10 alert was wrong with, in its unconditional form.
case "$MC_OUT" in
    *"restore by nudging the affected agents"*) fail "self-deploy: the unconditional nudge remedy is still printed: $MC_OUT" ;;
    *) pass "self-deploy: the unconditional 'restore by nudging' sentence is gone" ;;
esac
# And the healthy path must not have picked any of this up.
MC_OK="$(
    MAIL_CHECK_TIMEOUT=0
    mail_check_ids() { printf 'mail-check-mayor'; }
    verify_mail_checks_restored "mail-check-mayor" "" 2>&1
)"
case "$MC_OK" in
    *"pogo agent start"*|*"pogo nudge"*) fail "self-deploy: a healthy post-check prints a remedy: $MC_OK" ;;
    *) pass "self-deploy: a healthy post-check prescribes nothing" ;;
esac

# --- extract_mail_check_ids against a representative /scheduler/schedules body ---
SCHEDS='[{"id":"mail-check-mayor","agent":"mayor","cron":"*/10 * * * *"},{"id":"sweep-morning","agent":"crew-pm-pogo","cron":"0 9 * * *"},{"id":"mail-check-pm-pogo","agent":"pm-pogo","cron":"*/10 * * * *"},{"id":"mail-check-mg-de08","agent":"de08","cron":"*/10 * * * *"}]'
[ "$(printf '%s' "$SCHEDS" | extract_mail_check_ids)" = $'mail-check-mayor\nmail-check-mg-de08\nmail-check-pm-pogo' ] \
    && pass "extract_mail_check_ids returns only mail-check-* ids, by name" \
    || fail "extract_mail_check_ids ($(printf '%s' "$SCHEDS" | extract_mail_check_ids | tr '\n' ' '))"
# The sweeps are exactly what made the outage invisible — agents kept LOOKING
# scheduled. The parser must not be fooled by them.
[ -z "$(printf '%s' '[{"id":"sweep-morning","agent":"crew-pm-pogo"},{"id":"sweep-evening","agent":"crew-pm-pogo"}]' | extract_mail_check_ids)" ] \
    && pass "extract_mail_check_ids does not count surviving sweeps as mail-checks" || fail "extract_mail_check_ids picked up a sweep"
[ -z "$(printf '%s' '[]' | extract_mail_check_ids)" ] \
    && pass "extract_mail_check_ids handles an empty schedule list" || fail "extract_mail_check_ids on empty list"

# --- the slack lookup: names in, names out (mg-ea3e) ------------------------
# The pre-bounce world: 2 crew loops, polecat de08 with a loop, polecat f00d
# WITHOUT one (it never registered — mg-6fe0's nil-registrar drop is a live way
# to get here), plus a decoy sweep.
#
# de08's schedule is LAST in the body ON PURPOSE. The body's final object has no
# trailing newline, and a bare `while read` sets the line and then reports EOF —
# silently dropping whatever sorts last. This fixture caught exactly that during
# mg-ea3e; keep the schedule under test in the last position and it stays caught.
PRE_BODY='[{"id":"mail-check-mayor","agent":"mayor","cron":"*/10 * * * *"},{"id":"mail-check-pm-pogo","agent":"pm-pogo","cron":"*/10 * * * *"},{"id":"sweep-morning","agent":"pm-pogo","cron":"0 9 * * *"},{"id":"mail-check-mg-de08","agent":"de08","cron":"*/10 * * * *"}]'
# The bounce killed BOTH polecats. The snapshot is the only record of who they were.
SNAP='{"draining":true,"count":2,"polecats":[{"name":"de08","pid":111,"work_item_id":"mg-de08","worktree_dir":"/w/de08","source_repo":"/r"},{"name":"f00d","pid":222,"work_item_id":"mg-f00d","worktree_dir":"/w/f00d","source_repo":"/r"}]}'

[ "$(printf '%s' "$SNAP" | snapshot_polecat_names)" = $'de08\nf00d' ] \
    && pass "snapshot_polecat_names reads both dead polecats' names off the drain snapshot" \
    || fail "snapshot_polecat_names ($(printf '%s' "$SNAP" | snapshot_polecat_names | tr '\n' ' '))"

# THE ASSERTION THE TICKET IS ABOUT. Two polecats died. Slack is ONE schedule —
# de08's — because f00d had none to lose. The old code said "2".
[ "$(expected_lost_mail_checks "$PRE_BODY" "$SNAP")" = "mail-check-mg-de08" ] \
    && pass "mg-ea3e: 2 dead polecats, 1 mail-check between them -> slack names exactly that one" \
    || fail "mg-ea3e: slack is not the set of schedules that vanished ($(expected_lost_mail_checks "$PRE_BODY" "$SNAP" | tr '\n' ' '))"
# Kill only the polecat that had no loop: NOTHING is excused. A count would have
# granted 1 and eaten the next real loss whole.
SNAP_NOLOOP='{"draining":true,"count":1,"polecats":[{"name":"f00d","pid":222,"work_item_id":"mg-f00d","worktree_dir":"/w/f00d","source_repo":"/r"}]}'
[ -z "$(expected_lost_mail_checks "$PRE_BODY" "$SNAP_NOLOOP")" ] \
    && pass "mg-ea3e: killing a polecat that had NO mail-check grants ZERO slack" \
    || fail "mg-ea3e: a polecat with no mail-check was granted slack ($(expected_lost_mail_checks "$PRE_BODY" "$SNAP_NOLOOP" | tr '\n' ' '))"
# Slack is scoped to the dead. A crew agent's loop is never excused, and the
# decoy sweep is not a mail-check no matter whose it is.
SNAP_CREWNAME='{"draining":true,"count":1,"polecats":[{"name":"pm-pogo","pid":333,"work_item_id":"mg-xxxx","worktree_dir":"/w/x","source_repo":"/r"}]}'
[ "$(expected_lost_mail_checks "$PRE_BODY" "$SNAP_CREWNAME")" = "mail-check-pm-pogo" ] \
    && pass "expected_lost_mail_checks matches on the agent a schedule is ADDRESSED to, and takes the sweep with nothing" \
    || fail "expected_lost_mail_checks agent match ($(expected_lost_mail_checks "$PRE_BODY" "$SNAP_CREWNAME" | tr '\n' ' '))"
# An empty snapshot (nothing died) excuses nothing — the un-forced deploy path.
[ -z "$(expected_lost_mail_checks "$PRE_BODY" '')" ] \
    && pass "no dead polecats -> no slack at all" || fail "slack granted with an empty snapshot"

# --- unreachable_list: the survivors the drain CANNOT drain (mg-0b77) -------
# `count` is a fact about pogod's in-memory REGISTRY, not about the machine. A
# polecat that outlived an earlier pogod restart is permanently absent from that
# registry while still alive, so it reads as 0 — and the driver used to print
# "drain complete — 0 polecats active" over it: no snapshot, no cleanup, no
# mention. These parse the `unreachable` array that now carries them.
#
# THE CASE THAT MATTERS is count:0 WITH a survivor — the exact shape the old
# message lied about. If unreachable_list cannot read that payload, the fix is
# decorative.
SURVIVOR='{"draining":true,"count":0,"polecats":[],"unreachable":[{"name":"cat-9f21","pid":41207,"start_time":"2026-07-17T02:14:00Z","work_item_id":"mg-9f21"}]}'
[ "$(unreachable_list "$SURVIVOR")" = "cat-9f21 (pid=41207, work_item=mg-9f21)" ] \
    && pass "mg-0b77: unreachable_list reads a survivor out of a count:0 drain payload" \
    || fail "mg-0b77: unreachable_list count:0 survivor ($(unreachable_list "$SURVIVOR"))"

# The registry count must NOT be disturbed by the new field — drain_wait polls
# it to zero, and a survivor is deliberately not counted (it is not drainable;
# counting it would block every future redeploy forever).
[ "$(printf '%s' "$SURVIVOR" | json_num count)" = "0" ] \
    && pass "mg-0b77: the unreachable array does not corrupt json_num count" \
    || fail "mg-0b77: json_num count with unreachable present ($(printf '%s' "$SURVIVOR" | json_num count))"

# Two survivors, one line each.
SURVIVORS2='{"draining":true,"count":0,"polecats":[],"unreachable":[{"name":"cat-a","pid":11,"start_time":"2026-07-17T02:14:00Z","work_item_id":"mg-aaaa"},{"name":"cat-b","pid":12,"start_time":"2026-07-17T02:15:00Z","work_item_id":"mg-bbbb"}]}'
[ "$(unreachable_list "$SURVIVORS2" | wc -l | tr -d ' ')" = "2" ] \
    && pass "mg-0b77: unreachable_list splits multiple survivors" \
    || fail "mg-0b77: unreachable_list multiple ($(unreachable_list "$SURVIVORS2" | tr '\n' ' '))"

# A clean drain has no `unreachable` key at all (omitempty). It must yield
# NOTHING rather than a spurious line — a false alarm on every redeploy would
# train its reader to ignore the real one.
[ -z "$(unreachable_list "$DRAIN_OFF")" ] \
    && pass "mg-0b77: a clean drain payload yields no survivors" \
    || fail "mg-0b77: spurious survivor from a clean payload ($(unreachable_list "$DRAIN_OFF"))"

# A live polecat in `polecats` is NOT a survivor: it is registered, drainable,
# and drain_wait is already waiting for it. Reading it out of the wrong array
# would report a healthy fleet as leaked.
[ -z "$(unreachable_list "$DRAIN")" ] \
    && pass "mg-0b77: registered polecats are not read as unreachable" \
    || fail "mg-0b77: healthy polecat reported as a survivor ($(unreachable_list "$DRAIN"))"

# An unreadable witness is "cannot see", NOT "none" (mg-76e5). The field must be
# readable so report_drain_complete can refuse to print a clean drain.
ERRBODY='{"draining":true,"count":0,"polecats":[],"unreachable_err":"witness: cannot read /p/w.json: unexpected end of JSON input"}'
[ -n "$(printf '%s' "$ERRBODY" | json_str unreachable_err)" ] \
    && pass "mg-0b77: unreachable_err is readable, so 'cannot see' never prints as a clean drain" \
    || fail "mg-0b77: unreachable_err unreadable"

# report_drain_complete must never turn "I could not look" into "none
# unreachable". Its fetch is a SECOND, independent call — drain_wait's success
# proves the daemon answered 15s ago, not that it answers now. Drive the failure
# by pointing base_url at a closed port, and assert on what it SAYS: the
# distinction is the whole ticket, so a silent 0-exit is not good enough.
(
    base_url() { echo "http://127.0.0.1:1"; }   # nothing listens here
    OUT="$(report_drain_complete 2>&1)"
    case "$OUT" in
        *"could not look"*)
            pass "mg-0b77: an unreachable daemon reports 'could not look', not 'none unreachable'" ;;
        *"none unreachable"*)
            fail "mg-0b77: a FAILED fetch printed 'none unreachable' — absence of evidence rendered as a claim about the world" ;;
        *)  fail "mg-0b77: report_drain_complete said nothing useful on a failed fetch ($OUT)" ;;
    esac
) 2>/dev/null

# ===========================================================================
# THE DRAIN PREDICATE: "WOULD STOPPING THIS POLECAT LOSE WORK?" (mg-3a96)
# ===========================================================================
# WHAT CHANGED HERE, AND WHY THE OLD ASSERTIONS ARE INVERTED RATHER THAN EXTENDED.
# mg-853a's predicate asked whether a polecat OWED THE REFINERY A MERGE, and
# these tests asserted that a pushed-and-unmerged branch HOLDS the drain. That is
# now asserted the other way round, because pm-pogo ruled the proxy inverted: a
# pushed branch is DURABLE — the commits are on origin, the restart cannot touch
# them, the refinery lands them afterwards — so its polecat is the safest thing
# in the fleet to stop. On 2026-08-06 exactly one such branch held the whole
# deploy for 7,200s (cat-z37ad; origin/polecat-z37ad unlandable for 12h30m), and
# THE AUG 6 REPLAY below is the assertion that that specific body now clears.
#
# WHY THESE USE REAL GIT AND NOT STUBS. The predicate IS a git question. A stub
# returns whatever its author already believed about `merge-base --is-ancestor`,
# `git cherry`, `rev-parse --abbrev-ref` in a worktree, and when a push writes
# refs/remotes/origin/<branch> — and those beliefs are exactly what could be
# wrong. The same lesson mg-c02d and mg-65b2 each paid for once: the failure mode
# a control exists to catch lives in the WIRING. So this builds a real bare
# origin, a real clone, and real worktrees in a temp dir, and asks the shipping
# function about them.
#
# BOTH DIRECTIONS ARE OBSERVED, and that is the acceptance condition rather than
# a nicety. A predicate that has only ever returned "durable" has not been shown
# to work — it is indistinguishable from `return durable`, and a drain that
# always clears orphans every unpushed commit on the box. The `unpushed` cases
# below are real local-only commits in real worktrees, asserted to HOLD.
MD_TMP="$(mktemp -d)"
trap 'rm -f "$RESULTS_FILE" "$DW_STATE"; rm -rf "$MD_TMP"' EXIT
# -c on every call: this must not depend on the running user's git identity, and
# a repo-local `git config` would be one more thing to get wrong per worktree.
mdgit() { git -c user.email=t@example.invalid -c user.name=t -c commit.gpgsign=false "$@"; }

mdgit init -q --bare "$MD_TMP/origin.git"
mdgit clone -q "$MD_TMP/origin.git" "$MD_TMP/repo" 2>/dev/null
printf 'base\n' > "$MD_TMP/repo/f"
mdgit -C "$MD_TMP/repo" add f
mdgit -C "$MD_TMP/repo" commit -qm base
mdgit -C "$MD_TMP/repo" branch -M main
mdgit -C "$MD_TMP/repo" push -q origin main

# (a) A polecat that has COMMITTED AND NOT PUSHED. This is the one state a bounce
#     really costs something: the commits exist in no other place, and the only
#     process that was ever going to push them is the one about to be killed.
#     Under mg-853a this read `clear`; it now HOLDS, and that inversion is the
#     legitimate core of the old behaviour finally pointed at the right event.
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-local "$MD_TMP/wt-local" main
printf 'local\n' > "$MD_TMP/wt-local/g"
mdgit -C "$MD_TMP/wt-local" add g
mdgit -C "$MD_TMP/wt-local" commit -qm local

# (b) A polecat that PUSHED and is waiting on the merge queue — cat-z37ad's shape
#     on 2026-08-06. `git push` writes refs/remotes/origin/wt-pushed, which is the
#     evidence the predicate reads. THE WHOLE TICKET is that this does not hold.
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-pushed "$MD_TMP/wt-pushed" main
printf 'pushed\n' > "$MD_TMP/wt-pushed/h"
mdgit -C "$MD_TMP/wt-pushed" add h
mdgit -C "$MD_TMP/wt-pushed" commit -qm pushed
mdgit -C "$MD_TMP/wt-pushed" push -q origin wt-pushed

# (c) A polecat that pushed AND THEN COMMITTED AGAIN. origin/<branch> exists, so
#     a predicate that tested only for the ref's EXISTENCE would call this
#     durable and orphan the second commit. It is the positive control for the
#     `--is-ancestor` test in branch (1), separate from "was anything pushed".
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-ahead "$MD_TMP/wt-ahead" main
printf 'first\n' > "$MD_TMP/wt-ahead/k"
mdgit -C "$MD_TMP/wt-ahead" add k
mdgit -C "$MD_TMP/wt-ahead" commit -qm first
mdgit -C "$MD_TMP/wt-ahead" push -q origin wt-ahead
printf 'second\n' > "$MD_TMP/wt-ahead/k2"
mdgit -C "$MD_TMP/wt-ahead" add k2
mdgit -C "$MD_TMP/wt-ahead" commit -qm second

# (d) A polecat whose branch already LANDED as a fast-forward-able merge, with
#     the remote branch not yet reaped. Durable twice over.
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-merged "$MD_TMP/wt-merged" main
printf 'landed\n' > "$MD_TMP/wt-merged/i"
mdgit -C "$MD_TMP/wt-merged" add i
mdgit -C "$MD_TMP/wt-merged" commit -qm landed
mdgit -C "$MD_TMP/wt-merged" push -q origin wt-merged
mdgit -C "$MD_TMP/repo" merge -q --no-edit wt-merged
mdgit -C "$MD_TMP/repo" push -q origin main

# (e) A polecat that has committed NOTHING — freshly spawned, sitting at main.
#     Its branch is not on origin, so the unpushed test must not fire on it.
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-fresh "$MD_TMP/wt-fresh" main

# (f) THE REBASE CASE, staged end to end, because it is the one that would
#     otherwise hold a deploy FOREVER on a branch nobody will ever push again.
#     internal/refinery/merge.go rebases before merging and then runs
#     `push origin --delete <branch>` (step=branch-reap), so a just-landed polecat
#     has: no origin/<branch>, and a HEAD whose SHAs are not in main. Both
#     containment tests fail on it and only `git cherry`'s patch ids can see that
#     the work is in fact landed.
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-rebased "$MD_TMP/wt-rebased" main
printf 'rebased\n' > "$MD_TMP/wt-rebased/j"
mdgit -C "$MD_TMP/wt-rebased" add j
mdgit -C "$MD_TMP/wt-rebased" commit -qm rebased
mdgit -C "$MD_TMP/wt-rebased" push -q origin wt-rebased
MD_REBASED_SHA="$(mdgit -C "$MD_TMP/wt-rebased" rev-parse HEAD)"
# main advances first — the single-lane queue serving another branch, which is
# what forces the rebase to produce a different SHA rather than the same commit.
printf 'other\n' >> "$MD_TMP/repo/f"
mdgit -C "$MD_TMP/repo" add f
mdgit -C "$MD_TMP/repo" commit -qm other
# NO `-q` HERE, AND THAT IS NOT A STYLE POINT: `git cherry-pick` HAS NO -q FLAG.
# It was written with one, the flag made it exit non-zero, stderr was discarded,
# and the cherry-pick silently never happened — after which BOTH fixture
# preconditions below still passed (the ref was reaped; HEAD was trivially absent
# from main) while staging nothing. The real assertion caught it. The third
# precondition exists so the next reader does not need it to.
mdgit -C "$MD_TMP/repo" cherry-pick "$MD_REBASED_SHA" >/dev/null 2>&1
mdgit -C "$MD_TMP/repo" push -q origin main
mdgit -C "$MD_TMP/repo" push -q origin --delete wt-rebased 2>/dev/null

# THE FIXTURE IS ASSERTED, NOT ASSUMED. If the reap did not remove the
# remote-tracking ref, or the cherry-pick reproduced the same SHA, then (f) below
# would pass through branch (1) or (2) and the `git cherry` path would never run
# while the test still went green — a control that proves nothing, which is the
# family of defect this whole file exists to remove.
{ ! mdgit -C "$MD_TMP/wt-rebased" rev-parse --verify --quiet refs/remotes/origin/wt-rebased >/dev/null 2>&1; } \
    && pass "mg-3a96 fixture: the reap really removed refs/remotes/origin/wt-rebased, so the rebase case cannot be answered by branch (1)" \
    || fail "mg-3a96 fixture: origin/wt-rebased still resolves after 'push origin --delete' — the rebase case below is NOT staged and its assertion proves nothing"
{ ! mdgit -C "$MD_TMP/wt-rebased" merge-base --is-ancestor HEAD refs/remotes/origin/main 2>/dev/null; } \
    && pass "mg-3a96 fixture: the rebased branch's SHAs really are absent from origin/main, so the rebase case cannot be answered by branch (2)" \
    || fail "mg-3a96 fixture: HEAD is still an ancestor of origin/main — the cherry-pick did not change the SHA and the git-cherry path is untested"
# THE PRECONDITION THE FIRST TWO CANNOT GIVE. Both of those pass just as happily
# when the landing never happened at all, which is not "rebase-landed" — it is
# "genuinely unpushed", the opposite case, and the assertion below it would then
# be demanding the WRONG answer. So check that the work is really on main, by
# content, under a SHA that is not this branch's.
mdgit -C "$MD_TMP/wt-rebased" cat-file -e refs/remotes/origin/main:j 2>/dev/null \
    && pass "mg-3a96 fixture: the rebased branch's content really did land on origin/main under a different SHA — the case is 'landed', not 'never pushed'" \
    || fail "mg-3a96 fixture: origin/main does not contain the rebased branch's file — the cherry-pick did not happen and the assertion below is testing the wrong case"

# (g) THE REVIEW POLECAT — gh#134, the case that held the drain forever. A
#     reviewer is spawned on its own branch and must end up holding the BUILDER's
#     commits. It cannot `git checkout` the PR branch: git refuses a branch
#     already checked out in another worktree, and the builder's worktree is
#     still live while its PR is in review. So polecat-review.md:124-127 puts it
#     on `reset --hard origin/<pr-branch>` against its OWN branch, which it never
#     pushes. Result: HEAD is on origin/wt-pushed and on NO ref named wt-review.
#     Built with the same `reset --hard` the prompt instructs, not by branching
#     from the head — the shape under test is the one the prompt produces.
mdgit -C "$MD_TMP/repo" worktree add -q -b wt-review "$MD_TMP/wt-review" main
mdgit -C "$MD_TMP/wt-review" reset -q --hard refs/remotes/origin/wt-pushed

# THE FIXTURE IS ASSERTED, NOT ASSUMED — same reason as (f) below. Each of these
# rules out one OTHER branch of durability_of answering (g), which would leave
# the new containment test unexercised while its assertion still went green.
{ ! mdgit -C "$MD_TMP/wt-review" rev-parse --verify --quiet refs/remotes/origin/wt-review >/dev/null 2>&1; } \
    && pass "gh#134 fixture: the reviewer's OWN branch is not on origin, so the reviewer case cannot be answered by branch (1)" \
    || fail "gh#134 fixture: origin/wt-review resolves — the reviewer fixture pushed its own branch and branch (2b) below is NOT staged"
{ ! mdgit -C "$MD_TMP/wt-review" merge-base --is-ancestor HEAD refs/remotes/origin/main 2>/dev/null; } \
    && pass "gh#134 fixture: the reviewed commits are absent from origin/main, so the reviewer case cannot be answered by branch (2)" \
    || fail "gh#134 fixture: the PR under review is already on origin/main — the reviewer fixture is a merged branch and proves nothing"
[ "$(mdgit -C "$MD_TMP/wt-review" rev-parse HEAD)" = "$(mdgit -C "$MD_TMP/wt-review" rev-parse refs/remotes/origin/wt-pushed)" ] \
    && pass "gh#134 fixture: the reviewer really is sitting at the builder's pushed head — the commits it holds exist under SOMEBODY's origin ref" \
    || fail "gh#134 fixture: wt-review's HEAD is not origin/wt-pushed — the reset did not happen and the case below is 'genuinely unpushed', the opposite case"

# (h) refs/remotes/origin/HEAD, WHICH THIS FIXTURE OTHERWISE LACKS. A real clone
#     of a non-empty repository carries it; this one was cloned from a bare repo
#     that was still EMPTY, so git had no default branch to record and never
#     wrote the symref. That absence is why gh#134's `grep -v` exclusion was
#     invisible to the suite — deleting it left the run at 250/0, not because the
#     exclusion is inert but because nothing here had the ref to exclude
#     (measured in review of PR 140 round 1, whose advisory reached the right
#     conclusion from the wrong reason). Setting it makes the fixture MORE like a
#     real clone, and it is what test (2b)'s exclusion is finally asserted
#     against, in the no-integration-ref block below.
mdgit -C "$MD_TMP/repo" remote set-head origin main
mdgit -C "$MD_TMP/wt-fresh" rev-parse --verify --quiet refs/remotes/origin/HEAD >/dev/null 2>&1 \
    && pass "gh#134 fixture: refs/remotes/origin/HEAD now resolves, so the exclusion below has something to exclude" \
    || fail "gh#134 fixture: refs/remotes/origin/HEAD still does not resolve — the origin/HEAD assertions below are vacuous and would pass with the filter deleted"

md() { durability_of "$1"; }
mdw() { durability_of "$1" | cut -d' ' -f1; }

# --- THE INVERSION: what holds, and what no longer does ---------------------
[ "$(mdw "$MD_TMP/wt-local")" = "unpushed" ] \
    && pass "mg-3a96: THE POSITIVE CONTROL — real local-only commits in a real worktree HOLD the drain ($(md "$MD_TMP/wt-local"))" \
    || fail "mg-3a96: committed-but-unpushed read as '$(md "$MD_TMP/wt-local")' — the predicate never returns 'unpushed', so it protects nothing and every deploy orphans local commits"

[ "$(mdw "$MD_TMP/wt-pushed")" = "durable" ] \
    && pass "mg-3a96: THE AUG 6 CASE — a branch pushed to origin and NOT merged does NOT hold the drain ($(md "$MD_TMP/wt-pushed"))" \
    || fail "mg-3a96: pushed-but-unmerged read as '$(md "$MD_TMP/wt-pushed")' — this is mg-853a's inverted predicate still in place, and it is what held the 2026-08-06 deploy for the full 7200s"

# The report half of rule 3: not waited on, but NAMED. A deploy that walks past
# pending merges without saying so is indistinguishable from one that had none.
printf '%s' "$(md "$MD_TMP/wt-pushed")" | grep -q 'awaiting the refinery' \
    && pass "mg-3a96: a durable-but-unlanded branch is MARKED 'awaiting the refinery', so the run log records what the deploy went past" \
    || fail "mg-3a96: the pushed-unmerged line carries no 'awaiting the refinery' marker ($(md "$MD_TMP/wt-pushed")) — rule 3's decoupling would be silent"

[ "$(mdw "$MD_TMP/wt-ahead")" = "unpushed" ] \
    && pass "mg-3a96: a branch that pushed and then COMMITTED AGAIN holds — existence of origin/<branch> is not the test, containment of HEAD is ($(md "$MD_TMP/wt-ahead"))" \
    || fail "mg-3a96: a branch ahead of its own remote read as '$(md "$MD_TMP/wt-ahead")' — the second commit would be orphaned by the bounce"

[ "$(mdw "$MD_TMP/wt-merged")" = "durable" ] \
    && pass "mg-3a96: a branch pushed AND already contained in origin/main does not hold the drain ($(md "$MD_TMP/wt-merged"))" \
    || fail "mg-3a96: a merged branch read as '$(md "$MD_TMP/wt-merged")'"
printf '%s' "$(md "$MD_TMP/wt-merged")" | grep -qv 'awaiting the refinery' \
    && pass "mg-3a96: an already-landed branch is NOT marked as awaiting the refinery — the marker means something" \
    || fail "mg-3a96: a landed branch was marked 'awaiting the refinery' ($(md "$MD_TMP/wt-merged")) — the count in the run log would be noise"

[ "$(mdw "$MD_TMP/wt-fresh")" = "durable" ] \
    && pass "mg-3a96: a freshly spawned polecat with nothing committed does not hold the drain ($(md "$MD_TMP/wt-fresh"))" \
    || fail "mg-3a96: an empty worktree read as '$(md "$MD_TMP/wt-fresh")' — every spawn would hold the deploy"

[ "$(mdw "$MD_TMP/wt-rebased")" = "durable" ] \
    && pass "mg-3a96: a REBASE-LANDED branch whose remote ref the refinery reaped is durable — patch ids see what SHAs cannot ($(md "$MD_TMP/wt-rebased"))" \
    || fail "mg-3a96: a rebase-landed branch read as '$(md "$MD_TMP/wt-rebased")' — every merged polecat would hold the deploy on a branch nobody will ever push again"

# --- gh#134: THE REVIEWER, WHOSE COMMITS LIVE UNDER SOMEBODY ELSE'S REF ------
# The reported stall. Before the fix this read
#   `unpushed 1 commit(s) on wt-review exist only in <wt> — nothing on origin
#    holds them`
# whose trailing clause is provably false: origin/wt-pushed holds them. The
# wait it produced was unsatisfiable — the reviewer never pushes this branch —
# so the drain burned its whole budget and the deploy exited 7.
[ "$(mdw "$MD_TMP/wt-review")" = "durable" ] \
    && pass "gh#134: a REVIEW POLECAT holding the builder's pushed commits on its own never-pushed branch does NOT hold the drain ($(md "$MD_TMP/wt-review"))" \
    || fail "gh#134: the reviewer read as '$(md "$MD_TMP/wt-review")' — durability_of still asks only about origin/<this branch>, and every reviewed PR stalls the nightly redeploy until the deadline"

# The verdict word alone would also be produced by a predicate that had gone
# permissive. NAME THE CARRYING REF: the line has to say which origin ref makes
# this safe, or the deploy log records a clearance nobody can audit afterwards.
printf '%s' "$(md "$MD_TMP/wt-review")" | grep -q 'origin/wt-pushed' \
    && pass "gh#134: the reviewer's durable line NAMES the ref that carries its commits, so the clearance can be checked rather than trusted" \
    || fail "gh#134: the reviewer cleared without naming a holder ($(md "$MD_TMP/wt-review")) — indistinguishable from a predicate that stopped testing"

# THE TRUE POSITIVES THAT MATTER, RESTATED AGAINST THE WIDER TEST. Widening the
# ref set is a step toward permissiveness, and the cost of overshooting is
# mg-9a19 (1026 lines orphaned). wt-local never pushed at all; wt-ahead pushed
# and then committed again. Neither has any origin ref holding HEAD, so (2b)
# must not fire on them — asserted here as well as above, because above they
# were passing before this change existed.
[ "$(mdw "$MD_TMP/wt-local")" = "unpushed" ] && [ "$(mdw "$MD_TMP/wt-ahead")" = "unpushed" ] \
    && pass "gh#134: widening the ref set did NOT release the two genuine holders — never-pushed and pushed-then-ahead both still hold" \
    || fail "gh#134: a genuine holder was released by the wider containment test (local='$(md "$MD_TMP/wt-local")' ahead='$(md "$MD_TMP/wt-ahead")') — this is the mg-9a19 orphan-1026-lines direction"

# The five `durable`s above must be durable for DIFFERENT reasons. Identical
# detail would mean branches of durability_of are dead and the assertions are
# agreeing by accident — the same check mg-853a's suite made for its two clears.
#
# COMPARED AS SHAPES, NOT AS LINES, AND THAT IS WHAT MAKES THIS ABLE TO FAIL AT
# ALL. Every verdict interpolates the branch name and the worktree path, so two
# lines produced by the SAME branch of durability_of are still textually distinct
# — and `sort -u` over them is invariant under precisely the collapse this
# assertion exists to detect. The un-elided version of this check PASSED under
# the early-seat mutation while being cited, in this file and in the shipping
# script, as the thing that caught it (found in review of gh#134, PR 140 round 1;
# the triage packet's measurement had been taken against a candidate wording that
# did not interpolate the branch). Eliding $MD_TMP and the wt-* names first
# compares WHICH BRANCH ANSWERED rather than what it said about a given fixture.
#
# FILTERED TO `^durable` FOR THE SAME REASON THE NAME SAYS `durable`: without it
# the count is satisfied by a case that is not durable at all — under the
# fix-removed mutation wt-review's line is the `unpushed` one, still distinct,
# still counted — so the assertion would have claimed more than it checked.
#
# THIS IS THE GUARD ON WHERE gh#134's TEST (2b) SITS, now measured rather than
# asserted on principle: seated ABOVE (1) and (2) it answers for wt-pushed,
# wt-merged, wt-fresh AND wt-review, and the count below drops from 5 to 3.
# THREE, not two, and the difference is worth stating because it is the elision's
# own limit: those four cases produce TWO shapes, not one, since wt-merged and
# wt-fresh are held by origin/main while the other two are held by an origin/wt-*
# ref, and `wt-* -> <B>` does not touch the word "main". The count still fails the
# assertion, which is what the guard needs; it simply does not collapse as far as
# a first reading suggests. Measured at 9b5f171 (PR 140 round 2 advisory).
md_shape() { md "$1" | sed -e "s#$MD_TMP#<T>#g" -e 's#wt-[a-z]*#<B>#g'; }
MD_DETAILS="$(printf '%s\n%s\n%s\n%s\n%s\n' "$(md_shape "$MD_TMP/wt-pushed")" "$(md_shape "$MD_TMP/wt-merged")" "$(md_shape "$MD_TMP/wt-fresh")" "$(md_shape "$MD_TMP/wt-rebased")" "$(md_shape "$MD_TMP/wt-review")" | grep '^durable' | sort -u | wc -l | tr -d ' ')"
[ "$MD_DETAILS" = "5" ] \
    && pass "mg-3a96/gh#134: the five durable paths are five distinct measurements, not one fallback wearing five hats" \
    || fail "mg-3a96/gh#134: only $MD_DETAILS distinct durable SHAPES across five cases — a path is dead, or a case is not durable at all, or gh#134's containment test is seated too early and is preempting the specific ones"

# --- the unknowns: a question we failed to ask is not an answer of 'durable' ---
[ "$(mdw "")" = "unknown" ] \
    && pass "mg-3a96: an absent worktree_dir is 'unknown', never 'durable'" \
    || fail "mg-3a96: empty worktree_dir read as '$(md "")'"
[ "$(mdw "$MD_TMP/does-not-exist")" = "unknown" ] \
    && pass "mg-3a96: a worktree_dir that does not exist is 'unknown'" \
    || fail "mg-3a96: missing dir read as '$(md "$MD_TMP/does-not-exist")'"
mkdir -p "$MD_TMP/not-git"
[ "$(mdw "$MD_TMP/not-git")" = "unknown" ] \
    && pass "mg-3a96: a non-git directory is 'unknown'" \
    || fail "mg-3a96: a non-git dir read as '$(md "$MD_TMP/not-git")'"
mdgit -C "$MD_TMP/wt-local" checkout -q --detach
[ "$(mdw "$MD_TMP/wt-local")" = "unknown" ] \
    && pass "mg-3a96: a DETACHED HEAD is 'unknown' — 'HEAD' is not a branch name to look up on origin" \
    || fail "mg-3a96: detached HEAD read as '$(md "$MD_TMP/wt-local")' — a bogus origin/HEAD lookup would decide this"
# STILL 'unknown' AFTER gh#134, AND ON PURPOSE. A detached reviewer sitting on a
# pushed head would be answered by (2b) if the test were seated above the branch
# naming — but that seat also preempts (1) and (2) and collapses the distinctness
# assertion above, and pm-pogo ruled the detached case out of gh#134's scope
# because both live measurements are the named-branch shape. So it holds, which
# is the SAFE direction. mg-f0bf carries the constraint from the other side:
# polecat-review.md:125 instructs an impossible checkout, and repairing that with
# a detached HEAD would re-create on every reviewer the exact deadlock gh#134
# removes. If this assertion ever has to change, mg-f0bf is why.
mdgit -C "$MD_TMP/repo" worktree add -q --detach "$MD_TMP/wt-detached" refs/remotes/origin/wt-pushed
[ "$(mdw "$MD_TMP/wt-detached")" = "unknown" ] \
    && pass "gh#134/mg-f0bf: a DETACHED worktree sitting on a pushed head is still 'unknown' and still HOLDS — deliberately out of scope, and the safe direction ($(md "$MD_TMP/wt-detached"))" \
    || fail "gh#134/mg-f0bf: a detached HEAD read as '$(md "$MD_TMP/wt-detached")' — the containment test is seated above the branch naming, which is the placement that collapses the distinct durable paths"
mdgit -C "$MD_TMP/wt-local" checkout -q wt-local
(
    # NOTE THE WORKTREE CHOSE HERE. Under mg-853a this case used the PUSHED
    # branch, because the old predicate could not answer without an integration
    # ref. It can now: a pushed branch is durable whatever it targets (bound 4),
    # so the unresolvable-target case has to be staged on an UNPUSHED branch,
    # which is the one that still needs something to compare against.
    DRAIN_MERGE_TARGETS="refs/remotes/origin/no-such-integration-branch"
    [ "$(durability_of "$MD_TMP/wt-local" | cut -d' ' -f1)" = "unknown" ] \
        && pass "mg-3a96: unpushed, and no integration ref resolves -> 'unknown', never 'durable'" \
        || fail "mg-3a96: an unresolvable integration target read as '$(durability_of "$MD_TMP/wt-local")' — 'I could not find main' printed as 'safe to bounce'"
    [ "$(durability_of "$MD_TMP/wt-pushed" | cut -d' ' -f1)" = "durable" ] \
        && pass "mg-3a96: a PUSHED branch is durable even when no integration ref resolves — mg-853a's bound 1 (non-main targets held the deploy) dissolves rather than being widened" \
        || fail "mg-3a96: a pushed branch needed an integration ref to be called durable ($(durability_of "$MD_TMP/wt-pushed")) — the refinery is still coupled into the predicate"
    # gh#134's OTHER seat requirement, and the reason (2b) is above (3) rather
    # than below it. gh#134's own worktree showed upstream origin/develop, i.e. a
    # repo where neither main nor master resolves — there the reviewer fell
    # through to (3) and read `unknown`, which holds the drain exactly as
    # `unpushed` does (drain_unpushed_holders counts them together). A test
    # seated after (3) would never run on the very deployment that reported this.
    [ "$(durability_of "$MD_TMP/wt-review" | cut -d' ' -f1)" = "durable" ] \
        && pass "gh#134: the reviewer is durable even when NO integration ref resolves — the reported repo's base was origin/develop, and 'unknown' holds the drain just as 'unpushed' does" \
        || fail "gh#134: with no integration ref the reviewer read as '$(durability_of "$MD_TMP/wt-review")' — the containment test is seated below (3) and does not answer on the deployment that reported the bug"

    # THE origin/HEAD EXCLUSION, EXERCISED — and this block is the ONLY place it
    # can be. With an integration ref resolving, a fresh worktree is answered by
    # test (2) and never reaches (2b) at all; only here does (2b) get to name a
    # holder for it, and refs/remotes/origin/HEAD sorts before
    # refs/remotes/origin/main, so without the filter it is the ref that gets
    # named. THE VERDICT WORD IS `durable` EITHER WAY — asserted immediately
    # below — so this is naming quality, not correctness: the cost of dropping
    # the filter is a deploy log that credits the default-branch symref, which
    # says nothing about who pushed. Asserted so the filter cannot be deleted as
    # dead code (PR 140 round 1 advisory).
    [ "$(durability_of "$MD_TMP/wt-fresh" | cut -d' ' -f1)" = "durable" ] \
        && pass "gh#134: with no integration ref a fresh worktree is still durable — the exclusion below changes the NAME, never the verdict" \
        || fail "gh#134: a fresh worktree read as '$(durability_of "$MD_TMP/wt-fresh")' with no integration ref — either the containment test (2b) is not answering at all, or the origin/HEAD exclusion changed a verdict, which it must never do"
    { ! durability_of "$MD_TMP/wt-fresh" | grep -q 'origin/HEAD'; } \
        && pass "gh#134: the holder named is a real branch ref, not the origin/HEAD symref ($(durability_of "$MD_TMP/wt-fresh"))" \
        || fail "gh#134: the verdict credits origin/HEAD ($(durability_of "$MD_TMP/wt-fresh")) — the default-branch symref says nothing about who pushed, and the run log would name it instead of the branch that holds the work"
)

# --- polecat_objects: the unreachable tail must not leak into the predicate ---
# An OrphanedPolecat carries "name" and "work_item_id" just like a polecat, so a
# greedy capture that ran past the polecats array would look entirely plausible
# while asking git about processes this drain does not count (mg-0b77).
MIXED='{"draining":true,"count":1,"polecats":[{"name":"cat-live","pid":1,"work_item_id":"mg-live","worktree_dir":"/wt/live","source_repo":"/repo"}],"unreachable":[{"name":"cat-ghost","pid":2,"start_time":"2026-07-17T02:14:00Z","work_item_id":"mg-ghost"}]}'
MIXED_OUT="$(printf '%s' "$MIXED" | polecat_objects)"
{ printf '%s' "$MIXED_OUT" | grep -q 'cat-live' && ! printf '%s' "$MIXED_OUT" | grep -q 'cat-ghost'; } \
    && pass "mg-853a: polecat_objects reads the polecats array only — an unreachable survivor does not leak into the durability predicate" \
    || fail "mg-853a: polecat_objects swallowed the unreachable array ($MIXED_OUT) — the greedy capture ran past the polecats ']'"
# Counted with an anchored grep, NOT `wc -l`. The body arrives via command
# substitution with no trailing newline and BSD sed preserves that, so the last
# object has no terminator and `wc -l` reports one fewer than there are — which
# is the same off-by-one the `|| [ -n "$line" ]` guards in drain_durability exist
# for, and it would make this assertion agree with a splitter that had really
# dropped a polecat.
[ "$(printf '%s' "$DRAIN" | polecat_objects | grep -c '^{')" = "2" ] \
    && pass "mg-853a: polecat_objects splits a two-polecat body into two objects" \
    || fail "mg-853a: polecat_objects split ($(printf '%s' "$DRAIN" | polecat_objects | grep -c '^{') objects)"

# --- drain_durability / drain_unpushed_holders over a real snapshot ---------
# The bodies below name the REAL worktrees built above, so this is the whole
# path: JSON -> worktree_dir -> git -> verdict -> count.
md_body() {
    local out="" wt name
    for wt in "$@"; do
        name="$(basename "$wt")"
        [ -n "$out" ] && out="$out,"
        out="$out{\"name\":\"$name\",\"pid\":1,\"work_item_id\":\"mg-$name\",\"worktree_dir\":\"$wt\",\"source_repo\":\"$MD_TMP/repo\"}"
    done
    printf '{"draining":true,"count":%d,"polecats":[%s]}' "$#" "$out"
}

CLEAR_BODY="$(md_body "$MD_TMP/wt-pushed" "$MD_TMP/wt-merged" "$MD_TMP/wt-fresh")"
CLEAR_REPORT="$(drain_durability "$CLEAR_BODY")"
[ "$(drain_unpushed_holders "$CLEAR_REPORT")" = "0" ] \
    && pass "mg-3a96: three RUNNING polecats, one of them pushed-and-unlanded -> 0 hold the drain" \
    || fail "mg-3a96: durable polecats held the drain ($CLEAR_REPORT)"
[ "$(drain_awaiting_refinery "$CLEAR_REPORT")" = "1" ] \
    && pass "mg-3a96: the pending-merge count is REPORTED separately from the hold count — one branch awaiting the refinery, zero holding the deploy" \
    || fail "mg-3a96: expected 1 branch awaiting the refinery, got $(drain_awaiting_refinery "$CLEAR_REPORT") ($CLEAR_REPORT)"

HELD_BODY="$(md_body "$MD_TMP/wt-local" "$MD_TMP/wt-pushed" "$MD_TMP/wt-merged")"
HELD_REPORT="$(drain_durability "$HELD_BODY")"
[ "$(drain_unpushed_holders "$HELD_REPORT")" = "1" ] \
    && pass "mg-3a96: the SAME three polecats with one local-only branch -> exactly 1 holds the drain" \
    || fail "mg-3a96: expected exactly 1 holder, got $(drain_unpushed_holders "$HELD_REPORT") ($HELD_REPORT)"
printf '%s\n' "$HELD_REPORT" | grep -q '^unpushed wt-local (mg-wt-local):' \
    && pass "mg-3a96: the report line NAMES the polecat and its work item, so a count can always be expanded into the evidence behind it" \
    || fail "mg-3a96: report line does not name the holder ($HELD_REPORT)"

# A ONE-polecat body is where the missing-trailing-newline trap bites hardest:
# dropping the last record drops all of them, and the drain sails past the exact
# case it exists to catch. Same trap as mg-a558, one function over.
SOLO_REPORT="$(drain_durability "$(md_body "$MD_TMP/wt-local")")"
[ "$(drain_unpushed_holders "$SOLO_REPORT")" = "1" ] \
    && pass "mg-3a96: a ONE-polecat snapshot is not silently dropped — the sole holder is still counted" \
    || fail "mg-3a96: a single holding polecat counted $(drain_unpushed_holders "$SOLO_REPORT") ($SOLO_REPORT) — the last-record read trap is live and the drain would proceed over it"

# gh#134 END TO END, in the shape the issue actually reports: a builder whose PR
# is open and pushed, and the reviewer reviewing it. This pair is what a nightly
# redeploy meets whenever any PR is in review, and before the fix the reviewer
# alone held the drain to the deadline. Asserted through the whole path —
# JSON -> worktree_dir -> git -> verdict -> count — because the per-worktree
# assertion above cannot show that the count agrees with it.
REVIEW_REPORT="$(drain_durability "$(md_body "$MD_TMP/wt-pushed" "$MD_TMP/wt-review")")"
[ "$(drain_unpushed_holders "$REVIEW_REPORT")" = "0" ] \
    && pass "gh#134: a builder-with-open-PR plus its REVIEWER -> 0 hold the drain; the nightly redeploy is no longer blocked by any PR being in review ($REVIEW_REPORT)" \
    || fail "gh#134: the builder/reviewer pair held the drain ($(drain_unpushed_holders "$REVIEW_REPORT") holder(s): $REVIEW_REPORT) — the wait is unsatisfiable and the deploy exits 7 at the deadline"
# And the discrimination the clear above cannot supply on its own: swap the
# reviewer for a polecat with genuinely local-only commits and the count moves.
REVIEW_HELD="$(drain_durability "$(md_body "$MD_TMP/wt-pushed" "$MD_TMP/wt-local")")"
[ "$(drain_unpushed_holders "$REVIEW_HELD")" = "1" ] \
    && pass "gh#134: the same snapshot with a genuinely unpushed polecat in the reviewer's place still counts 1 holder — the clear above is a measurement, not 'return 0'" \
    || fail "gh#134: expected 1 holder with a local-only polecat, got $(drain_unpushed_holders "$REVIEW_HELD") ($REVIEW_HELD)"

# An unreadable worktree is counted WITH the holders: they differ in what we
# know, not in what is safe to do about it.
GHOST_REPORT="$(drain_durability "$(md_body "$MD_TMP/does-not-exist")")"
[ "$(drain_unpushed_holders "$GHOST_REPORT")" = "1" ] \
    && pass "mg-3a96: an 'unknown' verdict HOLDS the drain — a question we failed to ask is not an answer of 'durable'" \
    || fail "mg-3a96: an unreadable worktree did not hold the drain ($GHOST_REPORT)"

# --- drain_wait, wired to the re-aimed predicate ----------------------------
# In a subshell so the drain_probe stub cannot leak into the sections below.
(
    dwn() (
        DRAIN_TIMEOUT="${DWN_TIMEOUT:-5}"
        DRAIN_UNREADABLE_SLEEP=0
        local out rc=0
        out="$(drain_wait 2>/dev/null)" || rc=$?
        echo "$out|$rc"
    )

    # THE AUG 6 REPLAY, AND THE ACCEPTANCE CONDITION FOR THIS TICKET. One polecat,
    # branch pushed to origin, not landed — cat-z37ad exactly. Under mg-853a's
    # predicate this body returned "1|1" after burning the entire 7200s window and
    # the deploy exited 7. It must now clear on the first poll.
    AUG6_BODY="$(md_body "$MD_TMP/wt-pushed")"
    drain_probe() { printf '%s\n200' "$AUG6_BODY"; }
    [ "$(dwn)" = "0|0" ] \
        && pass "mg-3a96: THE AUG 6 REPLAY — a lone pushed-and-unlandable branch CLEARS the drain instead of holding it for the full budget" \
        || fail "mg-3a96: drain_wait still holds for a pushed-unmerged branch ($(dwn)) — the 2026-08-06 stall reproduces verbatim and mg-853a's narrowing is all that shipped"

    # A busy but durable fleet still clears.
    drain_probe() { printf '%s\n200' "$CLEAR_BODY"; }
    [ "$(dwn)" = "0|0" ] \
        && pass "mg-3a96: drain_wait CLEARS with 3 polecats still running, because none of them holds a commit that exists only locally" \
        || fail "mg-3a96: drain_wait did not clear over running-but-durable polecats ($(dwn))"

    # And the discrimination: one polecat with local-only commits holds, to the
    # deadline. Without this the clear above is indistinguishable from `return 0`.
    drain_probe() { printf '%s\n200' "$HELD_BODY"; }
    [ "$(DWN_TIMEOUT=0 dwn)" = "1|1" ] \
        && pass "mg-3a96: drain_wait HOLDS for the one polecat with local-only commits, and reports that count (not the polecat count) at the deadline" \
        || fail "mg-3a96: drain_wait did not hold for unpushed work ($(DWN_TIMEOUT=0 dwn)) — a bounce here kills the only process that was going to push those commits"

    # The report must state WHAT IT CHECKED. An alert naming a cause it has not
    # established is the defect that started this line of work; "drained" over a
    # fleet of three running polecats would be a fresh instance of it.
    drain_probe() { printf '%s\n200' "$CLEAR_BODY"; }
    DWN_OUT="$(DRAIN_TIMEOUT=5 DRAIN_UNREADABLE_SLEEP=0 drain_wait 2>&1 >/dev/null)"
    case "$DWN_OUT" in
        *"holds unpushed work"*)
            pass "mg-3a96: the drain's clearing report names the predicate it checked, not 'drained'" ;;
        *)  fail "mg-3a96: the clearing report does not say what was checked ($DWN_OUT)" ;;
    esac
    case "$DWN_OUT" in
        *"durable wt-pushed"*|*"durable wt-merged"*)
            pass "mg-3a96: the clearing report lists the per-polecat evidence, so the claim can be checked rather than taken" ;;
        *)  fail "mg-3a96: the clearing report gives no per-polecat evidence ($DWN_OUT)" ;;
    esac
    # RULE 3, SAID OUT LOUD. The deploy proceeds past unlanded branches on
    # purpose; a run that did so without naming them would be indistinguishable
    # from one that had none to name, which is the futility-detector complaint
    # (ruling rule 4) arriving at the success path instead of the failure path.
    case "$DWN_OUT" in
        *"proceeding past 1 pushed branch"*)
            pass "mg-3a96: the drain NAMES the unlanded branch it bounced past — the decoupling from refinery health is auditable, not silent" ;;
        *)  fail "mg-3a96: the drain cleared over a pending merge without saying so ($DWN_OUT)" ;;
    esac
    # The word it must NOT print: the old "0 polecats active" phrasing over a live
    # fleet is exactly the over-claim mg-0b77 removed one layer up.
    case "$DWN_OUT" in
        *"polecats active"*)
            fail "mg-3a96: the drain still reports 'polecats active' while clearing over running polecats — the report claims more than it checked" ;;
        *)  pass "mg-3a96: the drain does not describe a durability clearance as an idle fleet" ;;
    esac
) 2>/dev/null

# --- do_prove: the deploy-time gate on the detector (mg-bfe5) ---------------
# do_prove decides whether a redeploy is allowed to proceed. These drive its REAL
# body against a stub control whose output and exit code the test dictates, so
# every verdict below is the driver's own logic.
#
# The stub is the point, not a shortcut. The question here is NOT "does the live
# control work" — the live control answers that itself, at length, against a real
# daemon. It is "does the GATE refuse?", and the only way to ask that is to hand
# it a control that fails, that half-passes, or that lies by exiting 0 while
# demonstrating nothing. None of those can be staged with the real control.
#
# Without these, do_prove would be a guard whose refusal path had never once been
# executed — a check shipped without a demonstrated RED, inside the mechanism
# that exists because checks get shipped without demonstrated REDs.
PROVE_DIR=$(mktemp -d)
trap 'rm -f "$RESULTS_FILE"; rm -rf "$PROVE_DIR"' EXIT
mkdir -p "$PROVE_DIR/repo/scripts" "$PROVE_DIR/gobin"
# Stand-ins for the installed artifacts. do_prove only needs them to exist and be
# executable; the stub control below is what "reports" on them.
#
# BOTH binaries, not just pogod (mg-65b2): the drain gate shells to `pogo agent
# witness` when pogod stops answering, so the CLI is now part of the deploy's
# DECISION path and do_prove hands it to the control as POGO_LIVE_CONTROL_POGO.
# It refuses if either artifact is missing — which is why the fixture stages
# both. If you are here because these tests started exiting 9, that is the check
# working: do_prove's preconditions grew, and the fixture has to grow with them.
printf '#!/bin/sh\nexit 0\n' > "$PROVE_DIR/gobin/pogod"; chmod +x "$PROVE_DIR/gobin/pogod"
printf '#!/bin/sh\nexit 0\n' > "$PROVE_DIR/gobin/pogo"; chmod +x "$PROVE_DIR/gobin/pogo"

# Write a stub live control that emits $1 and exits $2.
stub_control() {
    { printf '#!/bin/bash\ncat <<'"'"'STUBEOF'"'"'\n%s\nSTUBEOF\nexit %s\n' "$1" "$2"; } \
        > "$PROVE_DIR/repo/scripts/pogo-self-deploy_live_test.sh"
    chmod +x "$PROVE_DIR/repo/scripts/pogo-self-deploy_live_test.sh"
}

# Run the real do_prove against the fixture. Echoes rc; stdout/stderr to $1.
prove_run() {
    local outfile="$1"
    (
        REPO="$PROVE_DIR/repo"
        POGO_GOBIN="$PROVE_DIR/gobin"
        MAIN=deadbeefdeadbeef
        installed_rev() { echo deadbeefdeadbeef; }
        unset POGO_DEPLOY_PROVING
        do_prove
    ) > "$outfile" 2>&1
    echo $?
}
PROVE_OUT="$PROVE_DIR/out"

# (a) THE GREEN. Both directions demonstrated -> the deploy proceeds. Without
#     this the refusals below could all be "do_prove always refuses".
stub_control 'PASS: something
PROVED: GREEN
PROVED: RED
=== Results: 19 passed, 0 failed ===' 0
[ "$(prove_run "$PROVE_OUT")" = "0" ] \
    && pass "do_prove: a control that demonstrates BOTH directions lets the deploy proceed" \
    || fail "do_prove refused a control that proved both directions (rc=$(prove_run "$PROVE_OUT")): $(cat "$PROVE_OUT")"

# (b) THE ASK, half 1: RED demonstrated but never GREEN. A detector only ever
#     shown going RED can be hard-wired to RED and is worth nothing.
stub_control 'PROVED: RED
=== Results: 19 passed, 0 failed ===' 0
PR_RC="$(prove_run "$PROVE_OUT")"
{ [ "$PR_RC" = "9" ] && grep -q "both directions" "$PROVE_OUT"; } \
    && pass "do_prove: REFUSES a control that demonstrated RED but never GREEN (a hard-wired RED proves nothing)" \
    || fail "do_prove ALLOWED a RED-only control (rc=$PR_RC) — a detector hard-wired to RED would deploy"

# (c) THE ASK, half 2: GREEN demonstrated but never RED. This is the loophole the
#     whole family lives in — the control that has never been shown able to fail.
stub_control 'PROVED: GREEN
=== Results: 19 passed, 0 failed ===' 0
PR_RC="$(prove_run "$PROVE_OUT")"
{ [ "$PR_RC" = "9" ] && grep -q "both directions" "$PROVE_OUT"; } \
    && pass "do_prove: REFUSES a control that demonstrated GREEN but never RED (an undemonstrated RED is decoration)" \
    || fail "do_prove ALLOWED a GREEN-only control (rc=$PR_RC) — the exact defect this ticket exists to close"

# (d) THE EXIT CODE IS NOT THE SIGNAL. A control that exits 0 having demonstrated
#     nothing — every assertion deleted, or an early exit before the controls —
#     must not be read as proof. This is why do_prove asserts on the tokens.
stub_control 'PASS: driver resolves base_url to the sandbox daemon
=== Results: 1 passed, 0 failed ===' 0
PR_RC="$(prove_run "$PROVE_OUT")"
[ "$PR_RC" = "9" ] \
    && pass "do_prove: REFUSES a control that exits 0 while demonstrating NEITHER direction (exit 0 != proven)" \
    || fail "do_prove trusted a clean exit 0 that proved nothing (rc=$PR_RC) — the gate reads the exit code, not the evidence"

# (e) a control that actually fails must stop the deploy, and say so.
stub_control 'PROVED: GREEN
FAIL: positive control FAILED: assembled path did NOT report RED
=== Results: 1 passed, 1 failed ===' 1
PR_RC="$(prove_run "$PROVE_OUT")"
{ [ "$PR_RC" = "9" ] && grep -q "FAILED against the built artifact" "$PROVE_OUT"; } \
    && pass "do_prove: a FAILING control refuses the deploy (and the failure is echoed, not swallowed)" \
    || fail "do_prove did not refuse on a failing control (rc=$PR_RC)"

# (f2) a MISSING pogo CLI refuses too (mg-65b2). do_build installs pogo in
#      lockstep with pogod, so an absent CLI here means the build did not do what
#      it said — and the drain gate calls `pogo agent witness` to decide whether
#      a silent pogod's fleet is live. Proving a gate whose CLI we never checked,
#      and then deploying it, is the shape of fail-open this whole file refuses.
#      Restored immediately: everything after it needs the fixture intact.
stub_control 'PROVED: RED
PROVED: GREEN
=== Results: 2 passed, 0 failed ===' 0
mv "$PROVE_DIR/gobin/pogo" "$PROVE_DIR/gobin/pogo.hidden"
PR_RC="$(prove_run "$PROVE_OUT")"
{ [ "$PR_RC" = "9" ] && grep -q "no installed pogo CLI" "$PROVE_OUT"; } \
    && pass "do_prove: a MISSING pogo CLI refuses the deploy — the drain gate's own dependency is checked, not assumed" \
    || fail "do_prove deployed with no pogo CLI installed (rc=$PR_RC) — the gate that reads the witness would be unproven"
mv "$PROVE_DIR/gobin/pogo.hidden" "$PROVE_DIR/gobin/pogo"

# (g) the gate fails CLOSED on its own absence. A missing control is the
#     detector's detector gone — not "nothing to prove".
rm -f "$PROVE_DIR/repo/scripts/pogo-self-deploy_live_test.sh"
PR_RC="$(prove_run "$PROVE_OUT")"
[ "$PR_RC" = "9" ] \
    && pass "do_prove: a MISSING live control refuses the deploy (fails closed, not open)" \
    || fail "do_prove proceeded with no live control present (rc=$PR_RC) — the gate fails open on its own absence"

# (g) re-entrancy fails LOUD rather than skipping. The live control drives real
#     cmd_redeploy runs; today they all die in do_build and never reach do_prove,
#     but a control that ever got past the build would otherwise recurse forever.
#     Refusing (not skipping) also means a stray env var cannot silently
#     downgrade a deploy back to the unproven behaviour.
stub_control 'PROVED: GREEN
PROVED: RED' 0
PR_RC=$(
    (
        REPO="$PROVE_DIR/repo"; POGO_GOBIN="$PROVE_DIR/gobin"; MAIN=deadbeefdeadbeef
        installed_rev() { echo deadbeefdeadbeef; }
        POGO_DEPLOY_PROVING=1
        do_prove
    ) > "$PROVE_OUT" 2>&1
    echo $?
)
{ [ "$PR_RC" = "9" ] && grep -q "refusing to recurse" "$PROVE_OUT"; } \
    && pass "do_prove: re-entry refuses LOUD (never silently skips the proof)" \
    || fail "do_prove re-entry did not refuse (rc=$PR_RC) — either it recurses or it skips silently"

# --- resolve_mg: the macguffin-NOT-editor resolver (mg-015f) -----------------
# The deploy's real alert path (mail_alert) and the live controls invoke `mg` by
# this RESOLVED ABSOLUTE path, never the bare name — because /usr/bin/mg on macOS
# is the Micro-Emacs EDITOR. Bare `mg` binds to it under launchd's minimal PATH
# (the nightly's exact context: /usr/bin ahead of ~/go/bin, no go) and panics
# headless, delivering NO alert. These controls stage fake binaries so the two
# acceptance directions run deterministically on any box.
MG_T="$(mktemp -d)"
mkdir -p "$MG_T/editoronly"
# A stand-in for /usr/bin/mg: its --help does NOT self-identify as macguffin (the
# real editor errors "illegal option -- -"), so the resolver must REJECT it even
# though it satisfies -x and `command -v mg`.
cat > "$MG_T/editoronly/mg" <<'EOF'
#!/bin/bash
echo "usage: mg [-nR] [-b file] [file ...]" >&2
exit 1
EOF
chmod +x "$MG_T/editoronly/mg"
mk_macguffin() {
    cat > "$1" <<'EOF'
#!/bin/bash
echo "macguffin work-item tracker"
EOF
    chmod +x "$1"
}

# (A) POSITIVE CONTROL — the nightly's exact context. Only the editor is on PATH,
# ~/go is not on PATH, and `go` itself is absent. The real macguffin still sits at
# $HOME/go/bin/mg ON DISK, and the resolver must find it WITHOUT consulting PATH.
MG_HOME_A="$MG_T/home-a"; mkdir -p "$MG_HOME_A/go/bin"; mk_macguffin "$MG_HOME_A/go/bin/mg"
MG_RESOLVED_A="$(
    export HOME="$MG_HOME_A" PATH="$MG_T/editoronly:/usr/bin:/bin"
    unset GOPATH GOBIN POGO_GOBIN
    MG=""; resolve_mg >/dev/null 2>&1 && printf '%s' "$MG"
)"
[ "$MG_RESOLVED_A" = "$MG_HOME_A/go/bin/mg" ] \
    && pass "resolve_mg (mg-015f): PATH-INDEPENDENT — with only the editor on PATH and no go, it still resolves the go-installed macguffin ($MG_RESOLVED_A). This is the nightly's context." \
    || fail "resolve_mg did not resolve macguffin under the nightly's bad PATH (got '$MG_RESOLVED_A') — the deploy would bind bare mg to the editor and fail closed and silent"

# (B) LOUD FAILURE — when the ONLY mg reachable ANYWHERE (GOPATH, $HOME/go, PATH)
# is the editor, the resolver must FAIL and say so, never silently accept it.
# Existence is not identity — a mere `command -v mg` guard would pass it through.
MG_ERR_B="$MG_T/b.err"
MG_RC_B="$(
    export HOME="$MG_T/emptyhome-b" GOPATH="$MG_T/gp-editor-b" PATH="$MG_T/editoronly:/usr/bin:/bin"
    unset GOBIN POGO_GOBIN
    mkdir -p "$HOME" "$GOPATH/bin"
    cp "$MG_T/editoronly/mg" "$GOPATH/bin/mg"
    MG=""; resolve_mg 2>"$MG_ERR_B"; printf '%s|%s' "$?" "$MG"
)"
if [ "${MG_RC_B%%|*}" != "0" ] && [ -z "${MG_RC_B#*|}" ] && grep -q "refusing bare 'mg'" "$MG_ERR_B"; then
    pass "resolve_mg (mg-015f): FAILS LOUDLY when only the editor is reachable — it rejects /usr/bin/mg by identity, leaves MG unset, and names why"
else
    fail "resolve_mg accepted the editor or failed quietly (rc|MG='$MG_RC_B') — an existence-only guard would pass the editor straight through and the sink would panic headless"
fi
rm -rf "$MG_T"

# --- mail_alert: the wrong-recipient refusal, across BOTH mg generations -----
# The property this function owns: a recipient name that names no mailbox is a
# CONFIG defect, and the operator must be told that in words. It is not "the
# send failed" — every send failure says that, and the one thing an operator
# needs at 03:00 is which of the two things is wrong, the name or the mail.
#
# There are now two mechanisms for the same situation and mail_alert must give
# the same diagnosis on both, because the operator's situation is identical:
#
#   pre-mg-d639  — mg auto-creates the box and reports Delivered. The send
#                  succeeds; `mailbox_created:true` is the only tell.
#   post-mg-d639 — mg REFUSES (no_such_mailbox, exit 3) and returns a JSON
#                  error blob. The send fails; the tell is in the blob.
#
# Both arms are exercised because this script runs against whatever mg is
# installed on the box it deploys, which is not the mg in this checkout. An
# assertion written against only the current one would go quiet on the other,
# which is how the live control stopped measuring its property in the first
# place: it asserted the MECHANISM's words rather than the DIAGNOSIS.
MA_T="$(mktemp -d)"
MA_BODY="$MA_T/body.txt"; echo "alert body" > "$MA_BODY"

# A stub mg whose behaviour is selected by $MA_MODE, so both generations are
# reachable from one fixture. `mail list` echoes the msg_id back, so the
# readback succeeds on the happy path and does not confound the refusals.
cat > "$MA_T/mg" <<'EOF'
#!/bin/bash
case "$2" in
  send)
    case "$MA_MODE" in
      refuses)  echo '{"error":{"code":"no_such_mailbox","category":"not_found","exit":3,"message":"no mailbox named \"ghost\""}}'; exit 3 ;;
      creates)  echo '{"msg_id":"1.2.3","mailbox_created":true}'; exit 0 ;;
      other)    echo '{"error":{"code":"io_error","exit":1,"message":"disk on fire"}}'; exit 1 ;;
      ok)       echo '{"msg_id":"1.2.3","mailbox_created":false}'; exit 0 ;;
    esac ;;
  list) echo '{"messages":[{"msg_id":"1.2.3"}]}'; exit 0 ;;
esac
EOF
chmod +x "$MA_T/mg"
MG="$MA_T/mg"

ma_run() { MA_MODE="$1" mail_alert ghost "subj" "$MA_BODY" 2>&1; }

# (A) post-mg-d639: the refusal. Necessary but NOT sufficient that it returns
#     non-zero — mail_alert returns 1 from four paths, and a control that
#     accepted any of them would pass against "disk on fire" too (arm C).
MA_OUT_A="$(ma_run refuses)"; MA_RC_A=$?
if [ "$MA_RC_A" -ne 0 ] \
   && printf '%s' "$MA_OUT_A" | grep -q "had NO mailbox" \
   && printf '%s' "$MA_OUT_A" | grep -q "the recipient name is wrong"; then
    pass "mail_alert (mg-d639): mg's no_such_mailbox REFUSAL is reported as a wrong recipient NAME, not as a generic send failure — the diagnosis survived the mechanism moving into mg"
else
    fail "mail_alert did not name the recipient on mg's no_such_mailbox refusal (rc=$MA_RC_A): $MA_OUT_A"
fi

# (B) pre-mg-d639: the auto-create. Same diagnosis, and still a REFUSAL —
#     an alert sitting in a box nobody reads is not a delivery.
MA_OUT_B="$(ma_run creates)"; MA_RC_B=$?
if [ "$MA_RC_B" -ne 0 ] \
   && printf '%s' "$MA_OUT_B" | grep -q "had NO mailbox" \
   && printf '%s' "$MA_OUT_B" | grep -q "the recipient name is wrong"; then
    pass "mail_alert: an older mg that auto-creates the box is STILL undelivered, with the same diagnosis — the mailbox_created check is retained, not replaced"
else
    fail "mail_alert accepted a send into a mailbox it had just created, or dropped the diagnosis (rc=$MA_RC_B): $MA_OUT_B"
fi

# (C) THE DISCRIMINATOR — the arm that stops A and B being satisfied by any
#     failure at all. A send that failed for an unrelated reason must NOT be
#     reported as a wrong recipient name: sending the operator to check the
#     coordinator name over a disk error is the same misdirection this ticket
#     exists to remove, one layer down.
MA_OUT_C="$(ma_run other)"; MA_RC_C=$?
if [ "$MA_RC_C" -ne 0 ] \
   && ! printf '%s' "$MA_OUT_C" | grep -q "the recipient name is wrong" \
   && printf '%s' "$MA_OUT_C" | grep -q "disk on fire"; then
    pass "mail_alert: a send that failed for an UNRELATED reason is not blamed on the recipient name, and the real error is echoed — the name diagnosis is scoped, not a catch-all"
else
    fail "mail_alert blamed the recipient name for an unrelated send failure, or swallowed it (rc=$MA_RC_C): $MA_OUT_C"
fi

# (D) The happy path still returns 0, so none of the above turned the function
#     into one that can only refuse.
ma_run ok >/dev/null 2>&1 \
    && pass "mail_alert: a send into an EXISTING mailbox that reads back still succeeds" \
    || fail "mail_alert now refuses a good delivery — the refusals above are unfalsifiable"

unset MG
rm -rf "$MA_T"

# ---------------------------------------------------------------------------
# Out-of-band guard: pogod_ancestor / assert_out_of_band (mg-1bbf)
# ---------------------------------------------------------------------------
# NOTHING HERE INVOKES THE REAL DEPLOY, and that is not fastidiousness. The
# specimen this guard exists to refuse is a pogod descendant, and the test
# runner IS one (test.sh runs under an agent, which pogod spawned). So the one
# arm best placed to be tested by invocation is the arm that, if the guard is
# broken, proceeds into `kickstart -k` and kills pogod, the fleet, and the test
# process itself — a test whose failure mode erases the evidence and the tester.
# Testing by invocation would only be safe if the guard already worked, which is
# the thing not yet known. So ps_parent — the single seam onto the process
# table — is replaced with a synthetic tree, and both arms are proved as
# function calls that never enter cmd_redeploy.
#
# The synthetic tree is what makes the NEGATIVE arm real, too: a genuinely
# detached process cannot be constructed from inside the fleet (anything this
# test spawns is a descendant of it), so "proceeds past the check" is proved
# against a chain that terminates at launchd with no pogod in it.

# Two fixture trees. Format: "pid -> ppid comm", fed to a stubbed ps_parent.
#   descendant:   900 (bash) <- 800 (claude) <- 700 (pogod) <- 1
#   out-of-band:  900 (bash) <- 500 (login)  <- 400 (Terminal) <- 1
OOB_TREE=""
ps_parent() {
    printf '%s\n' "$OOB_TREE" | while IFS= read -r row; do
        case "$row" in "$1 "*) printf '%s' "${row#* }"; return 0 ;; esac
    done
}

OOB_TREE='900 800 /bin/bash
800 700 claude
700 1 /Users/daniel/go/bin/pogod'
ANC="$(pogod_ancestor 900)"
if [ "$ANC" = "700 /Users/daniel/go/bin/pogod" ]; then
    pass "pogod_ancestor (mg-1bbf): FINDS pogod two hops up and reports its pid and path ($ANC) — the guard can fire"
else
    fail "pogod_ancestor missed pogod in the descendant tree (got '$ANC') — a guard that cannot fire is the prose it replaced"
fi

# The absolute path is why the match is on the BASENAME: ps reports the exec
# path, which differs per machine and per sandbox.
OOB_TREE='900 800 /bin/bash
800 700 claude
700 1 /opt/pogo/sbin/pogod'
pogod_ancestor 900 >/dev/null \
    && pass "pogod_ancestor: matches on basename, so a pogod installed at any prefix is still recognised" \
    || fail "pogod_ancestor missed a pogod at a non-default prefix — the match is path-dependent"

# NEGATIVE ARM. A guard that refuses everything passes the arm above and breaks
# the only path that works, so this one is not optional.
OOB_TREE='900 500 /bin/bash
500 400 login
400 1 /System/.../Terminal'
if pogod_ancestor 900 >/dev/null; then
    fail "pogod_ancestor reported a pogod ancestor in a terminal-rooted chain — the guard refuses the ONLY caller that is allowed to run"
else
    pass "pogod_ancestor (mg-1bbf): a terminal-rooted chain (bash <- login <- Terminal <- launchd) walks to the root and finds NO pogod — Daniel's invocation proceeds"
fi

# A chain that dead-ends on an unreadable pid must terminate, and must terminate
# as "no pogod found" — not hang, and not guess.
OOB_TREE='900 800 /bin/bash'
pogod_ancestor 900 >/dev/null \
    && fail "pogod_ancestor claimed a pogod ancestor from a chain it could not read" \
    || pass "pogod_ancestor: an unreadable parent ends the walk without claiming a pogod"

# A cycle must not hang the deploy path.
OOB_TREE='900 800 /bin/bash
800 900 /bin/bash'
pogod_ancestor 900 >/dev/null \
    && fail "pogod_ancestor found a pogod in a cyclic chain" \
    || pass "pogod_ancestor: a cyclic parent chain terminates (depth cap) instead of hanging the deploy"

# --- assert_out_of_band: the refusal itself, in a subshell (it exits 1) ------
# These trees are rooted at the REAL $$, because assert_out_of_band walks from
# the live process rather than from a pid a caller hands it — that defaulting is
# part of what is under test, so it is not stubbed away. $$ is stable inside the
# subshells below (bash keeps the parent's value).
# POGO_AGENT_NAME is unset explicitly wherever the ancestry arm is the one being
# proved: this suite RUNS inside a pogod-spawned agent, so the marker is set for
# real, and leaving it would let the env arm mask an ancestry walk that never
# fired.
DESC_TREE="$$ 800 /bin/bash
800 700 claude
700 1 /Users/daniel/go/bin/pogod"
OOB_TREE="$DESC_TREE"
OOB_ERR="$(mktemp)"
( unset POGO_AGENT_NAME; assert_out_of_band 2>"$OOB_ERR" >/dev/null ); OOB_RC=$?
if [ "$OOB_RC" = "1" ]; then
    pass "assert_out_of_band (mg-1bbf): a pogod descendant is REFUSED with exit 1 — the seventeenth refusal path"
else
    fail "assert_out_of_band returned $OOB_RC for a pogod descendant — it did not refuse"
fi
grep -qi "descendant of pogod" "$OOB_ERR" \
    && pass "assert_out_of_band: the refusal NAMES pogod as the reason" \
    || fail "refusal does not name pogod: $(cat "$OOB_ERR")"
grep -qi "OUT OF BAND" "$OOB_ERR" \
    && pass "assert_out_of_band: the refusal states the out-of-band remedy" \
    || fail "refusal does not state the remedy: $(cat "$OOB_ERR")"
# An agent that reads only "refused" concludes the DEPLOY is broken and either
# escalates wrongly or hunts for a way around the guard. The text has to say the
# deploy is fine and the caller is not, and hand it somewhere.
grep -q "THE REDEPLOY IS LEGITIMATE" "$OOB_ERR" \
    && pass "assert_out_of_band: says the redeploy is legitimate and the CALLER is wrong — not a bare denial" \
    || fail "refusal reads as 'the deploy is broken'"
grep -q "mail 'human'" "$OOB_ERR" \
    && pass "assert_out_of_band: names the handoff an agent should perform instead" \
    || fail "refusal gives an agent no next action"
rm -f "$OOB_ERR"

# The reparenting hole the ancestry walk cannot see: a detached agent whose
# intermediate parent exited is adopted by launchd, so the chain shows no pogod
# while the caller is still inside the fleet. The env marker pogod stamps is
# what still catches it.
OOB_TREE="$$ 1 /bin/bash"
OOB_ERR2="$(mktemp)"
( export POGO_AGENT_NAME=polecat-dead-parent; assert_out_of_band 2>"$OOB_ERR2" >/dev/null ); OOB_RC2=$?
if [ "$OOB_RC2" = "1" ] && grep -q "polecat-dead-parent" "$OOB_ERR2"; then
    pass "assert_out_of_band (mg-1bbf): a REPARENTED agent (chain shows launchd, POGO_AGENT_NAME set) is still refused, and the refusal names the marker it fired on"
else
    fail "a reparented pogod-spawned agent walked past the guard (rc=$OOB_RC2) — detaching would be enough to evade it"
fi
rm -f "$OOB_ERR2"

# NEGATIVE ARM, assembled: terminal-rooted chain AND no agent marker -> the
# guard returns 0 and cmd_redeploy carries on to its real preconditions.
OOB_TREE="$$ 500 /bin/bash
500 400 login
400 1 /System/.../Terminal"
OOB_ERR3="$(mktemp)"
( unset POGO_AGENT_NAME; assert_out_of_band 2>"$OOB_ERR3" >/dev/null ); OOB_RC3=$?
if [ "$OOB_RC3" = "0" ] && [ ! -s "$OOB_ERR3" ]; then
    pass "assert_out_of_band (mg-1bbf): Daniel's terminal invocation PROCEEDS past the check, silently — the one working path is not broken by the guard"
else
    fail "assert_out_of_band refused a legitimate out-of-band caller (rc=$OOB_RC3): $(cat "$OOB_ERR3")"
fi
rm -f "$OOB_ERR3"

# The guard must sit on the path every redeploy takes — a check the real caller
# routes around is the same non-mechanism as a comment. Asserted against the
# source rather than by invocation, for the reason at the top of this section.
grep -A6 '^cmd_redeploy() {' "$HERE/pogo-self-deploy" | grep -q 'assert_out_of_band' \
    && pass "assert_out_of_band is called at the TOP of cmd_redeploy — on the entry path, ahead of drain/build/kickstart" \
    || fail "assert_out_of_band is not wired into cmd_redeploy's entry — the guard exists but nothing reaches it"
# ...and `check`, which never acts, must NOT be guarded: it is how the fleet
# notices its own drift.
sed -n '/^cmd_check() {/,/^}/p' "$HERE/pogo-self-deploy" | grep -q 'assert_out_of_band' \
    && fail "the guard blinds 'check', which never acts — the fleet can no longer observe its own drift" \
    || pass "'check' is deliberately unguarded: read-only, never acts, and is the fleet's only way to see it is stale"

unset -f ps_parent

# ---------------------------------------------------------------------------
# What the daemon ANSWERED (mg-08e9) — the fact the 08-07 deploy threw away
# ---------------------------------------------------------------------------
# THE DEFECT. drain_post ran `curl -s -o /dev/null -w '%{http_code}'`. On
# 2026-08-07 the POST answered 503, the run exited 6 after 0 seconds, and the
# reason pogod gave went to /dev/null. Eight nights, no new information.
#
# WHAT THIS ADDS ON TOP OF mg-0155, WHICH IS NOT THE SAME THING. That ticket
# classified 503 as `stopped` and wrote the operator's sentence at the refusal
# site. Those sentences are an INFERENCE FROM THE SOURCE TREE — what a status
# must have meant, given how pogod is wired today. These assertions are about
# the daemon's own account of the same moment travelling beside it. Agreement
# confirms the diagnosis; disagreement is the only way a stale inference ever
# surfaces instead of being confidently mis-narrated.
#
# THE PROPERTY THAT MATTERS MOST IS THE ONE ABOUT THE STATUS. This is an
# observability change on an error path, so the failure to guard is the fix
# breaking the thing it annotates: the status is the PRIMARY fact and must
# survive a body that is empty, multi-line, enormous, or full of control bytes.

# --- http_status / http_body: the split, against real curl output shapes ----
R_503="$(printf '%s\n503' 'orchestration is stopped')"
[ "$(http_status "$R_503")" = "503" ] \
    && pass "http_status: reads the code off a body+status response" || fail "http_status 503 ($(http_status "$R_503"))"
[ "$(http_body "$R_503")" = "orchestration is stopped" ] \
    && pass "http_body: recovers the 08-07 body that used to go to /dev/null" || fail "http_body 503 ($(http_body "$R_503"))"

# AN EMPTY BODY MUST NOT EAT THE STATUS. This is the `down` case (curl writes
# "000" and no body). If the split got it wrong the status would read as empty
# and classify_drain_precondition would call a real answer `down` — turning an
# observability fix into a misdiagnosis.
R_EMPTY="$(printf '\n000')"
[ "$(http_status "$R_EMPTY")" = "000" ] \
    && pass "http_status: an EMPTY body still yields the status (the primary fact survives)" || fail "http_status empty-body ($(http_status "$R_EMPTY"))"
[ -z "$(http_body "$R_EMPTY")" ] \
    && pass "http_body: an empty body reads as empty, not as the status code" || fail "http_body empty-body ($(http_body "$R_EMPTY"))"

# A MULTI-LINE BODY — an HTML error page, a pretty-printed JSON blob. The status
# is the LAST line, so only a greedy match gets this right.
R_MULTI="$(printf '%s\n503' $'{\n  "error": "orchestration is stopped"\n}')"
[ "$(http_status "$R_MULTI")" = "503" ] \
    && pass "http_status: survives a multi-line body (matches the LAST newline, not the first)" || fail "http_status multi-line ($(http_status "$R_MULTI"))"
[ "$(http_body "$R_MULTI")" = "$(printf '{\n  "error": "orchestration is stopped"\n}')" ] \
    && pass "http_body: keeps every line of a multi-line body" || fail "http_body multi-line"

[ "$(http_status "999")" = "999" ] && [ -z "$(http_body "999")" ] \
    && pass "http_status/http_body: a newline-less response is all status, no invented body" || fail "http_status/body newline-less"

# --- fmt_http_body: bounded, one line, never silently truncated -------------
[ "$(fmt_http_body 400 'orchestration is stopped')" = "orchestration is stopped" ] \
    && pass "fmt_http_body: a short body passes through verbatim" || fail "fmt_http_body verbatim"
case "$(fmt_http_body 400 '')" in
    *"empty body"*) pass "fmt_http_body: an empty body is STATED, not rendered as blank" ;;
    *) fail "fmt_http_body empty ($(fmt_http_body 400 ''))" ;;
esac

# ONE LINE, ALWAYS. Every err line is copied verbatim into the reason record,
# whose format is line-oriented — a body carrying "\nreason=..." would forge a
# field in the record the RED alert is built from.
MB="$(fmt_http_body 400 "$(printf 'line one\nline two\ttabbed')")"
[ "$(printf '%s' "$MB" | wc -l | tr -d ' ')" = "0" ] \
    && pass "fmt_http_body: newlines and tabs collapse — a body cannot forge a field in the reason record" || fail "fmt_http_body one-line ($MB)"
[ "$MB" = "line one line two tabbed" ] \
    && pass "fmt_http_body: control bytes become spaces (tokens stay separate) and runs are squeezed" || fail "fmt_http_body squeeze ($MB)"

# TRUNCATION IS ANNOUNCED. A body cut to 400 chars and presented as the whole
# answer is a smaller copy of the defect being fixed.
LONG="$(printf 'x%.0s' $(seq 1 900))"
TB="$(fmt_http_body 400 "$LONG")"
case "$TB" in
    *"truncated; 900 chars in full"*) pass "fmt_http_body: truncation is ANNOUNCED with the full length" ;;
    *) fail "fmt_http_body truncation notice ($TB)" ;;
esac
[ "${#TB}" -lt 500 ] \
    && pass "fmt_http_body: the rendering is actually BOUNDED (a 900-char body does not reach the alert whole)" || fail "fmt_http_body bound (${#TB})"

# --- END TO END: the body reaches the reason record, and so the RED alert ---
# The whole point. mg-0155 built the channel (every err line is copied into the
# record's verbatim block, which the runner renders into the mail); this ticket
# puts the daemon's answer into it. Driven through the REAL refusal path, with
# the real reason-record writer, exactly as cmd_redeploy calls it.
BODY_TMP="$(mktemp -d)"
precond_run_body() (
    export POGO_DEPLOY_REASON_FILE="$2"
    REASON_FILE="$2"
    ERR_LOG="$(mktemp)"
    DEPLOY_STAGE="drain"
    DEPLOY_INSTALLED="no"
    DRAIN_PRIOR="?"
    DRAIN_ARMED=true
    drain_post() { printf '\n000'; }
    # The confirmed-outage branch is the one these assertions are about, so pin
    # the mode rather than inheriting whatever is listening on :10000 (mg-6d2f).
    server_mode() { echo "index-only"; }
    trap on_deploy_exit EXIT
    refuse_drain_precondition "$1" "$3"
)
REAL_503='{"error":"orchestration is stopped","mode":"index-only"}'
precond_run_body stopped "$BODY_TMP/rec.stopped" "$REAL_503" >/dev/null 2>&1
grep -q 'orchestration is stopped' "$BODY_TMP/rec.stopped" \
    && pass "END TO END: the 503 body reaches the reason record — and therefore the RED alert, not just the log" \
    || fail "END TO END: the reason record lost the body ($(cat "$BODY_TMP/rec.stopped"))"
# The MODE is in that body too, and it is the field that would settle which
# index-only path was taken — the question mg-293c could not answer from
# pogod.log, whose mode-transition lines were absent across the whole window.
grep -q 'index-only' "$BODY_TMP/rec.stopped" \
    && pass "END TO END: the body's MODE field survives too — the fact pogod.log did not record on 08-07" \
    || fail "END TO END: the mode field was dropped"
# The headline must still be the deploy's own sentence, NOT the body. mg-0155's
# channel takes the FIRST err line as `reason`; inserting the answer above it
# would have quietly replaced the operator's headline with a JSON blob.
case "$(rec_field "$BODY_TMP/rec.stopped" reason)" in
    *"orchestration is STOPPED"*) pass "END TO END: the headline is still the deploy's own sentence — the body did not displace it" ;;
    *) fail "the body displaced the reason headline: $(rec_field "$BODY_TMP/rec.stopped" reason)" ;;
esac

# `down` must NOT claim an answer. 000 is curl reporting that nothing responded;
# rendering the absent body as "(empty body — the server answered...)" would
# assert exactly what this disposition denies.
precond_run_body down "$BODY_TMP/rec.down" "" >/dev/null 2>&1
grep -q 'it answered' "$BODY_TMP/rec.down" \
    && fail "precond down: claims the server 'answered' when 000 means nothing responded" \
    || pass "precond down: does NOT claim an answer — unreachable and silent stay different facts"

# THAT ASSERTION EARNED ITS KEEP IMMEDIATELY, and the story is worth keeping.
# restore_drain also POSTs /agents/drain and also now prints what came back. The
# `down` refusal reaches the restore trap with a dead port, so the first version
# of that line put "it answered: (empty body — the server answered, but said
# nothing)" into the record of a run whose entire finding was that nothing
# answered. The guard below is the positive half: when something DOES answer
# non-2xx, the restore must still quote it, or this would be a fix by deletion.
restore_said() (
    ERR_LOG=""; DRAIN_ARMED=true; DRAIN_PRIOR="false"
    drain_post() { printf '%s\n503' "$1"; }   # $1 is the want; body is arbitrary
    restore_drain 6 2>&1
)
case "$(restore_said)" in
    *"it answered"*"false"*) pass "restore_drain: a failing restore that WAS answered quotes the answer (the guard is conditional, not a deletion)" ;;
    *) fail "restore_drain dropped the body on an answered non-2xx: $(restore_said)" ;;
esac
restore_silent() (
    ERR_LOG=""; DRAIN_ARMED=true; DRAIN_PRIOR="false"
    drain_post() { printf '\n000'; }
    restore_drain 6 2>&1
)
case "$(restore_silent)" in
    *"it answered"*) fail "restore_drain claims an answer on 000: $(restore_silent)" ;;
    *"nothing answered the restore POST"*) pass "restore_drain: a restore nothing answered says so, instead of quoting an empty body as an answer" ;;
    *) fail "restore_drain said neither: $(restore_silent)" ;;
esac

# ...and the other two answering dispositions do carry it, so the line above is
# a property of `down`, not of a function that never prints the body.
precond_run_body bootstrap "$BODY_TMP/rec.bootstrap" 'no route for POST /agents/drain' >/dev/null 2>&1
grep -q 'no route for POST' "$BODY_TMP/rec.bootstrap" \
    && pass "precond bootstrap: carries what the 404 said (the answer is printed whenever there IS one)" \
    || fail "precond bootstrap dropped the body"
precond_run_body error:500 "$BODY_TMP/rec.e500" 'panic: nil map read' >/dev/null 2>&1
grep -q 'panic: nil map read' "$BODY_TMP/rec.e500" \
    && pass "precond error:500: carries the body — the branch that refuses to GUESS still reports what it was told" \
    || fail "precond error:500 dropped the body"
# That branch's own sentence had to change with it: it used to say the status
# was "the whole of what is known", which stops being true the moment the body
# is printed beside it.
grep -q 'the status above is the whole of what is known' "$BODY_TMP/rec.e500" \
    && fail "precond error:500 still claims the status is all that is known, while printing the body above it" \
    || pass "precond error:500: no longer claims the status is all that is known — the sentence tracks the new fact"

# A no-body call must still produce every sentence (older harnesses, and any
# caller that has no body to give).
precond_run_body stopped "$BODY_TMP/rec.nobody" "" >/dev/null 2>&1
grep -q 'orchestration is STOPPED' "$BODY_TMP/rec.nobody" \
    && pass "precond: a call with no body still emits the full refusal (the argument is additive, not required)" \
    || fail "precond: an empty body suppressed the refusal text"

# --- A REAL CURL AGAINST A REAL 503 ----------------------------------------
# A STUBBED CURL IS NOT A CURL, and this file carries the scar: mg-65b2 exists
# because `curl -sf`'s empty-body-on-any-failure was something nobody had run.
# Everything above would pass against a drain_post whose curl invocation is
# subtly wrong — a missing `-w`, an `-o` left behind, a `-f` that suppresses the
# 5xx body — because none of it runs curl. This does, against a socket
# answering exactly what pogod answers.
if command -v python3 >/dev/null 2>&1; then
    STUB_PY="$(mktemp)"; STUB_PORT_F="$(mktemp)"
    cat > "$STUB_PY" <<'PY'
import http.server, socketserver, sys
BODY = b'{"error":"orchestration is stopped","mode":"index-only"}'
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length') or 0))
        self.send_response(503)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(BODY)))
        self.end_headers()
        self.wfile.write(BODY)
    def log_message(self, *a): pass
srv = socketserver.TCPServer(('127.0.0.1', 0), H)
open(sys.argv[1], 'w').write(str(srv.server_address[1]))
srv.serve_forever()
PY
    python3 "$STUB_PY" "$STUB_PORT_F" & STUB_PID=$!
    for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$STUB_PORT_F" ] && break; sleep 0.2; done
    STUB_PORT="$(cat "$STUB_PORT_F" 2>/dev/null)"
    if [ -n "$STUB_PORT" ]; then
        # A subshell so PORT points at the stub for these cases only. pass/fail
        # append to $RESULTS_FILE, a FILE, so the verdicts survive the subshell
        # even though a variable would not.
        ( PORT="$STUB_PORT"
          RAW="$(drain_post true)"
          [ "$(http_status "$RAW")" = "503" ] \
              && pass "drain_post over a REAL socket: the 503 status arrives (curl's -w wiring is right, not just the split)" \
              || fail "drain_post real socket: status was '$(http_status "$RAW")', not 503"
          case "$(http_body "$RAW")" in
            *"orchestration is stopped"*)
              pass "drain_post over a REAL socket: the 503 BODY arrives — the exact fact 2026-08-07 discarded" ;;
            *) fail "drain_post real socket: the body was lost ('$(http_body "$RAW")')" ;;
          esac
          # And that classify still rules `stopped` off the real status, so the
          # body is an addition to mg-0155's diagnosis and not a change to it.
          [ "$(classify_drain_precondition "$(http_status "$RAW")")" = "stopped" ] \
              && pass "a REAL 503 still classifies as 'stopped' — mg-0155's ruling is unchanged, it is now checkable" \
              || fail "the real 503 no longer classifies as stopped"
        )
    else
        fail "real-curl control: the stub server never reported a port"
    fi
    kill "$STUB_PID" 2>/dev/null || true
    wait "$STUB_PID" 2>/dev/null || true
    rm -f "$STUB_PY" "$STUB_PORT_F"
else
    echo "SKIP: real-curl control needs python3 (the pure assertions above still ran)"
fi

# --- the wiring, asserted against the source -------------------------------
# Everything above could pass while drain_post still wrote the body to
# /dev/null. This is the assertion that would have failed on 2026-08-06 — the
# one that names the actual defect.
sed -n '/^drain_post() {/,/^}/p' "$HERE/pogo-self-deploy" | grep -q -- '-o /dev/null' \
    && fail "drain_post still discards the response body to /dev/null — this is the mg-08e9 defect, restored" \
    || pass "drain_post does NOT discard its response body (the mg-08e9 defect cannot come back unnoticed)"
sed -n '/^drain_post() {/,/^}/p' "$HERE/pogo-self-deploy" | grep -q "w '..%{http_code}'" \
    && pass "drain_post asks curl for the status on its own final line — body and status from ONE hop" \
    || fail "drain_post no longer emits a trailing status line; every caller's split is broken"
# And the caller must actually PASS it on. A drain_post that captures the body
# into a variable nobody forwards is the same silence with more code.
grep -q 'refuse_drain_precondition "\$disp" "\$dp_body"' "$HERE/pogo-self-deploy" \
    && pass "cmd_redeploy forwards the captured body to the refusal — it is not captured and dropped" \
    || fail "cmd_redeploy captures the body but does not hand it to refuse_drain_precondition"

echo ""
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
