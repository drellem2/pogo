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

# A healthy readout of N, past the deadline: the pre-existing timeout path, which
# must survive this change untouched. It is what exit 7 has always hung off.
drain_probe() { printf '%s\n200' '{"draining":true,"count":3,"polecats":[]}'; }
[ "$(DW_TIMEOUT=0 dw)" = "3|1" ] \
    && pass "drain_wait: N active at the deadline -> rc 1 and the COUNT (the exit-7 timeout path is unchanged)" \
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

echo ""
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
