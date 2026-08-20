#!/usr/bin/env bash
# =============================================================================
# mergedopen-runtime_test.sh — the merged-not-closed ALERT and the merged-work
# GATE, observed against a REAL pogod (mg-161a, successor to mg-9d4e/mg-f17c)
# =============================================================================
#
# WHY THIS IS NOT A GO TEST, AND CANNOT BE ONE.
#
# cmd/pogod's production mail sink refuses to send from a test binary:
#
#     func defaultMergedOpenAlertMail(a mergedOpenAlert) {
#         if testing.Testing() { ...log and return... }
#
# That guard is correct — without it a `go test ./cmd/pogod/...` run manufactures
# a real fleet alarm in the coordinator's real inbox — and it is exactly why the
# alert's delivery had gone unobserved since mg-9d4e landed. Every unit test of
# this path asserts against a STUB installed in its place (mergedopen_test.go,
# testmain_test.go). A green cmd/pogod suite is therefore evidence about the
# stub, not about the sink; mg-f17c said so and could not close the gap, and
# mg-161a is the successor that carries it.
#
# So the observation has to be made by a pogod that is NOT a test binary. This
# driver builds one, gives it a private everything (scripts/pogo-sandbox), and
# drives a real branch through a real refinery into a real merge, then reads
# what the daemon actually did.
#
# WHAT IS OBSERVED, AND THE TRAP IT HAS TO AVOID
#
# mg-9d4e's own file (cmd/pogod/mergedopen.go) warns whoever verifies this that
# a DIFFERENT merged-not-closed mail already arrives today, from internal/
# filernotify, and that reading the old path as evidence for the new one would
# score the observation as done when nothing new had run. The two are separable
# by inspection and this driver separates them explicitly:
#
#   THIS path   `[merged-not-closed] <id> is MERGED but still open …`
#               -> agent.CoordinatorName(), preceded by a
#                  `work_item_merged_not_closed` event on the spine.
#   OLD path    `MERGED BUT NOT CLOSED: <id> — <title>`
#               -> the item's CREATOR, no event.
#
# Every assertion below names which path it is reading, and OBS-6/OBS-6b assert
# the discriminator directly rather than leaving it to the reader. It is not a
# theoretical confusion: in this driver's own runs the OLD path delivers into the
# SAME sandbox mailbox, on the SAME merge ("filernotify: told mayor that work item
# ... MERGED BUT IS STILL OPEN"), so the two really are side by side in one box.
#
# BOTH ARMS. The delivery has a failure direction — a coordinator mailbox that
# does not exist — and a control with no red arm is a control that has not been
# shown to bite (mg-712e). Phase 1 merges with NO registered coordinator mailbox
# and requires the undelivered event; phase 2 registers it and requires the mail.
# The two phases differ in exactly one staged fact.
#
# Usage:  bash scripts/mergedopen-runtime_test.sh
#         MERGEDOPEN_KEEP=1 bash scripts/mergedopen-runtime_test.sh   # keep the root
#
# THE GATE HALF CAN ALSO BE PROBED ON A LIVE DAEMON, SAFELY, and the technique is
# worth keeping because probing a REFUSAL is only safe if the failure direction
# is harmless. Aim a spawn at an already-merged item and pass a --template that
# does not exist: in internal/agent/api.go every gate runs first and
# ResolveTemplate runs after all of them and before the worktree, agent dir and
# expanded prompt file (mg-ef80). So there are exactly two outcomes — the gate
# refuses (409, the observation), or the gate fails open and the request dies at
# template resolution (404) having created nothing. There is no path on which a
# worker is dispatched. Done against the running pogod on 2026-08-20; see
# docs/investigations/mergedopen-runtime-observation-2026-08-20.md.
#
# Exit: 0 all observations made; 1 an observation failed; 99 the sandbox could
# not be established (scripts/pogo-sandbox's setup-failure code — the run makes
# NO claim about the tree).
# =============================================================================

set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/pogo-sandbox"

RUN=0
PASSED=0
step()  { echo; echo "===== $* ====="; }
note()  { echo "  - $*"; }
ok()    { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo "  PASS  $*"; }
bad()   { RUN=$((RUN+1)); echo "  FAIL  $*" >&2; }
die()   { echo "FATAL: $*" >&2; exit 1; }

dump_log() {
    [ -n "${POGO_SANDBOX_LOG:-}" ] && [ -f "$POGO_SANDBOX_LOG" ] || return 0
    echo "----- sandbox pogod.log (tail) -----" >&2
    tail -n 60 "$POGO_SANDBOX_LOG" >&2
    echo "------------------------------------" >&2
}

# ---------------------------------------------------------------------------
# 1. a private everything, and a pogod built from THIS checkout
# ---------------------------------------------------------------------------
step "1. build pogo + pogod, then isolate"

pogo_sandbox_create mopen

# ARMED BEFORE ANYTHING IS STARTED, and it names the signals. Phase 3 dispatches
# a polecat over the gate (the override arm), and that worker outlives the daemon
# that spawned it — `pogo agent stop` does not reach a process launchd has
# reparented, and EXIT with no signal list does not fire on SIGTERM. So the probe
# agents are stopped FIRST, by name, before the daemon that knows about them is
# killed (mg-c675).
PROBE_AGENTS=()
mergedopen_cleanup() {
    local a
    [ -n "${BIN_DIR:-}" ] && [ -x "${BIN_DIR}/pogo" ] && for a in "${PROBE_AGENTS[@]:-}"; do
        [ -n "$a" ] || continue
        "$BIN_DIR/pogo" agent stop "$a" >/dev/null 2>&1
    done
    if [ -z "${MERGEDOPEN_KEEP:-}" ]; then
        pogo_sandbox_down
    else
        pogo_sandbox_daemon_kill
        sandbox_port_release
        echo "KEPT: $POGO_SANDBOX_DIR"
    fi
}
trap mergedopen_cleanup EXIT INT TERM HUP

BIN_DIR="$POGO_SANDBOX_DIR/bin"
mkdir -p "$BIN_DIR"

# Built BEFORE pogo_sandbox_isolate, under the real HOME, so the toolchain and
# module cache are the developer's warm ones. -p is the CORE BUDGET: go decides
# its own build parallelism from the MACHINE otherwise, and this host's share is
# three of ten.
(
    cd "$REPO_ROOT"
    GOBIN="$BIN_DIR" go install -p "${POGO_WORKER_CORES:-2}" ./cmd/pogo ./cmd/pogod
) || die "go install failed"
[ -x "$BIN_DIR/pogod" ] || die "pogod was not built"
[ -x "$BIN_DIR/pogo" ]  || die "pogo was not built"
command -v mg >/dev/null || die "mg is not on PATH"

pogo_sandbox_isolate

# TMPDIR is NOT part of pogo_sandbox_isolate's envelope and has to be here: the
# daemon lockfile and the agent socket dir are both anchored to os.TempDir(), so
# a sandbox pogod sharing the developer's TMPDIR collides with the live one.
export TMPDIR="$POGO_SANDBOX_DIR/tmp"
mkdir -p "$TMPDIR"
export PATH="$BIN_DIR:$PATH"

note "sandbox root: $POGO_SANDBOX_DIR"
note "HOME=$HOME  POGO_HOME=$POGO_HOME  MG_ROOT=$MG_ROOT"

# ---------------------------------------------------------------------------
# 2. sandbox profile: a coordinator NAME, and no crew
# ---------------------------------------------------------------------------
step "2. stage the sandbox profile"

mkdir -p "$XDG_CONFIG_HOME/pogo"
cat > "$XDG_CONFIG_HOME/pogo/config.toml" <<'EOF'
[agents]
coordinator = "mayor"
autostart = false
command = "sleep 600"
EOF

mg init >/dev/null 2>&1 || die "mg init failed under MG_ROOT=$MG_ROOT"

# THE MAILBOX IS DELIBERATELY NOT REGISTERED YET. Phase 1 is the red arm.
if mg mail list mayor >/dev/null 2>&1 && ! mg mail list mayor 2>&1 | grep -q 'No such mailbox'; then
    note "note: a mayor mailbox already answers in this fresh MG_ROOT"
fi

# ---------------------------------------------------------------------------
# 3. a repo the refinery will accept, with a gate that passes
# ---------------------------------------------------------------------------
step "3. set up the test repo"

ORIGIN="$POGO_SANDBOX_DIR/origin.git"
WORK="$POGO_SANDBOX_DIR/work"
git init --bare -b main "$ORIGIN" >/dev/null 2>&1 || die "bare init failed"
git init -b main "$WORK" >/dev/null 2>&1 || die "work init failed"
(
    cd "$WORK"
    git config user.email mergedopen@pogo.test
    git config user.name  'Merged Open Probe'
    echo "# merged-not-closed runtime probe" > README.md
    printf '#!/bin/bash\nexit 0\n' > build.sh
    chmod +x build.sh
    git add README.md build.sh
    git commit -q -m "initial"
    git remote add origin "$ORIGIN"
    git push -q -u origin main
) || die "test repo setup failed"
note "origin: $ORIGIN"

# ---------------------------------------------------------------------------
# 4. the daemon
# ---------------------------------------------------------------------------
step "4. start the sandbox pogod"

pogo_sandbox_daemon "$BIN_DIR/pogod" /health
export POGO_PORT="$POGO_SANDBOX_PORT"
note "pogod pid=$POGO_SANDBOX_PID port=$POGO_SANDBOX_PORT log=$POGO_SANDBOX_LOG"

# The observation depends on this daemon NOT being a test binary. Say so from
# the daemon's own report rather than from the fact that we ran `go install`.
if "$BIN_DIR/pogod" --version >/dev/null 2>&1; then
    note "pogod build: $("$BIN_DIR/pogod" --version 2>&1 | head -1)"
fi

EVENTS="$POGO_HOME/events.log"
# Set here rather than only inside phase 2: under `set -u` a failed green merge
# would otherwise take out phase 3 with an unbound-variable error instead of
# letting it report what it found.
MERGED_SHA=""
MERGED_MR=""

# ---------------------------------------------------------------------------
# helpers for the two phases
# ---------------------------------------------------------------------------

# file_declaring_item TITLE -> prints the work item id.
#
# --type=design because `mg new` writes `declares-remainder` by default for the
# types where it holds, and design is one of them (checked, not assumed: OBS-0
# asserts the tag is actually on the item).
#
# AND IT IS CLAIMED. `mg done` on an UNCLAIMED item is refused for a different
# reason ("not claimed, so it cannot be completed"), which would still raise the
# alert but would not be the scenario mg-9d4e is about. In the live shape the
# polecat holds the claim at the moment the branch merges, so the claim is
# staged here for the same reason.
file_declaring_item() {
    local title="$1" out id
    out="$(mg new --type=design --priority=medium --repo="$WORK" "$title" 2>&1)"
    id="$(printf '%s' "$out" | grep -oE 'mg-[a-f0-9]+' | head -1)"
    [ -n "$id" ] || { echo "could not file work item: $out" >&2; return 1; }
    mg claim "$id" >/dev/null 2>&1 || { echo "could not claim $id" >&2; return 1; }
    printf '%s' "$id"
}

# merge_a_branch_for ID LABEL — push a trivial branch and drive it through the
# refinery with ID as the author. Returns 0 once the MR reads merged.
merge_a_branch_for() {
    local id="$1" label="$2"
    local branch="probe-$label"
    local out mr status i
    (
        cd "$WORK"
        git fetch -q origin main
        git checkout -q main
        git reset --hard -q origin/main
        git checkout -q -b "$branch"
        printf 'probe %s\n' "$label" >> README.md
        git add README.md
        git commit -q -m "probe: $label ($id)"
        git push -q origin "$branch"
        git checkout -q main
    ) || { echo "could not push $branch" >&2; return 1; }

    out="$(pogo refinery submit "$branch" --repo="$WORK" --author="$id" --target=main --json 2>&1)"
    mr="$(printf '%s' "$out" | grep -oE '"id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed -E 's/.*"([^"]*)"$/\1/')"
    [ -n "$mr" ] || { echo "submit returned no MR id: $out" >&2; return 1; }
    note "MR $mr for $id on branch $branch"

    for i in $(seq 1 90); do
        status="$(curl -sf "http://127.0.0.1:$POGO_SANDBOX_PORT/refinery/mr/$mr" 2>/dev/null \
            | grep -oE '"status"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed -E 's/.*"([^"]*)"$/\1/')"
        case "$status" in
            merged) MERGED_MR="$mr"; return 0 ;;
            failed) echo "MR $mr FAILED its gate" >&2; return 1 ;;
        esac
        sleep 2
    done
    echo "MR $mr never finalized (last status=${status:-unknown})" >&2
    return 1
}

# wait_for_event TYPE ID — the reap runs after the merge is recorded, so the
# event trails the status flip by a moment.
wait_for_event() {
    local etype="$1" id="$2" i
    for i in $(seq 1 30); do
        if [ -f "$EVENTS" ] && grep "\"event_type\":\"$etype\"" "$EVENTS" 2>/dev/null | grep -q "$id"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# -F, NOT a plain grep. The subject this searches for opens with
# `[merged-not-closed]`, which as a basic regular expression is a BRACKET
# EXPRESSION matching ONE character — so the unescaped pattern cannot match the
# subject it was written to find. It was written that way first, and the arm it
# broke was the RED one: OBS-3 asserts that no such mail exists, and a pattern
# that can never match passes that assertion no matter what is on disk. The
# absence arm is the one a search bug is invisible in, which is why both arms of
# this control read the store through this one function.
mail_body_for_subject() {
    grep -rlF "$1" "$MG_ROOT/mail" 2>/dev/null | head -1
}

# ===========================================================================
# PHASE 1 — THE RED ARM: no coordinator mailbox, so the mail cannot land
# ===========================================================================
step "5. PHASE 1 (red arm) — merge with NO coordinator mailbox registered"

WI_RED="$(file_declaring_item 'runtime probe: red arm')" || die "could not file the red-arm item"
note "work item: $WI_RED"

mg show "$WI_RED" 2>&1 | grep -E '^Tags:' | grep -q 'declares-remainder' \
    && ok "OBS-0  the item carries declares-remainder (mg done will refuse it)" \
    || bad "OBS-0  the item does NOT carry declares-remainder — the scenario below is not the one this asserts"

MERGED_MR=""
if merge_a_branch_for "$WI_RED" red; then
    ok "the refinery merged the red-arm branch (MR $MERGED_MR)"
else
    bad "the red-arm branch did not merge — nothing below observed the alert"
    dump_log
fi

if wait_for_event work_item_merged_not_closed "$WI_RED"; then
    ok "OBS-1  work_item_merged_not_closed is ON THE EVENT SPINE for $WI_RED"
    note "$(grep "\"event_type\":\"work_item_merged_not_closed\"" "$EVENTS" | grep "$WI_RED" | head -1 | cut -c1-400)"
else
    bad "OBS-1  no work_item_merged_not_closed event for $WI_RED"
    dump_log
fi

if wait_for_event work_item_merged_not_closed_undelivered "$WI_RED"; then
    ok "OBS-2  RED ARM BITES: the send failed and said so — work_item_merged_not_closed_undelivered"
    note "$(grep "work_item_merged_not_closed_undelivered" "$EVENTS" | grep "$WI_RED" | head -1 | cut -c1-400)"
else
    bad "OBS-2  no undelivered event — with no mayor mailbox the send should have failed and been recorded"
fi

# A POSITIVE CONTROL FOR THE SEARCH, AT THE INSTANT THE ABSENCE IS CLAIMED.
# OBS-3 asserts that something is NOT on disk, and an absence assertion passes
# for free if the searcher is broken or the tree is not where it thinks — which
# is not hypothetical here: the first version of mail_body_for_subject used a
# plain grep, so `[merged-not-closed]` was a bracket expression that could not
# match the subject it was written to find, and OBS-3 passed on a store it had
# never really looked in. So a decoy carrying the same subject SHAPE under a
# different id is planted and found first. If OBS-3a fails, OBS-3 says nothing.
mg mail register mergedopen-decoy >/dev/null 2>&1
mg mail send mergedopen-decoy --from=probe \
    --subject="[merged-not-closed] SEARCHPROBE is MERGED but still open" \
    --body="positive control for the absence assertion below" >/dev/null 2>&1
if [ -n "$(mail_body_for_subject "[merged-not-closed] SEARCHPROBE")" ]; then
    ok "OBS-3a the searcher FINDS a decoy with this subject shape, so the absence below is a reading"
else
    bad "OBS-3a the searcher cannot find a mail it was just shown — OBS-3 below would pass vacuously"
fi

if [ -z "$(mail_body_for_subject "[merged-not-closed] $WI_RED")" ]; then
    ok "OBS-3  and no [merged-not-closed] mail exists anywhere under MG_ROOT for $WI_RED"
else
    bad "OBS-3  a [merged-not-closed] mail for $WI_RED exists although no mailbox was registered"
fi

# ===========================================================================
# PHASE 2 — THE GREEN ARM: register the coordinator, merge again
# ===========================================================================
step "6. PHASE 2 (green arm) — register mayor, then merge a second item"

mg mail register mayor >/dev/null 2>&1 || die "could not register the sandbox mayor mailbox"
note "registered the sandbox coordinator mailbox: mayor"

WI="$(file_declaring_item 'runtime probe: green arm')" || die "could not file the green-arm item"
note "work item: $WI"

MERGED_MR=""
if merge_a_branch_for "$WI" green; then
    ok "the refinery merged the green-arm branch (MR $MERGED_MR)"
    MERGED_SHA="$(cd "$WORK" && git fetch -q origin main && git rev-parse origin/main)"
    note "merged sha on origin/main: $MERGED_SHA"
else
    bad "the green-arm branch did not merge — nothing below observed the alert"
    dump_log
fi

wait_for_event work_item_merged_not_closed "$WI" \
    && ok "work_item_merged_not_closed is on the spine for $WI" \
    || bad "no work_item_merged_not_closed event for $WI"

# The mail write trails the event: the event is emitted FIRST by design.
MAILFILE=""
for _ in $(seq 1 30); do
    MAILFILE="$(mail_body_for_subject "[merged-not-closed] $WI")"
    [ -n "$MAILFILE" ] && break
    sleep 1
done

if [ -n "$MAILFILE" ]; then
    ok "OBS-4  THE ALERT WAS DELIVERED: a [merged-not-closed] mail for $WI exists on disk"
    note "maildir file: ${MAILFILE#$MG_ROOT/}"
else
    bad "OBS-4  no [merged-not-closed] mail for $WI landed in the sandbox mail store"
    dump_log
fi

# Delivered TO THE COORDINATOR, read back through mg rather than off the disk:
# a file under mail/mayor/ is where the sender put it, and `mg mail list mayor`
# is what a coordinator would actually see.
if mg mail list mayor 2>&1 | grep -q "merged-not-closed"; then
    ok "OBS-5  the coordinator's own inbox (mg mail list mayor) shows it"
else
    bad "OBS-5  mg mail list mayor does not show the alert"
    mg mail list mayor 2>&1 | head -20 >&2
fi

# THE DISCRIMINATOR mg-9d4e's file warns about, asserted rather than assumed.
if [ -n "$MAILFILE" ]; then
    if grep -q "is MERGED but still open" "$MAILFILE" && grep -qi "^From:.*pogod" "$MAILFILE"; then
        ok "OBS-6  it is THIS path: bracketed subject, from pogod, to the coordinator"
    else
        bad "OBS-6  the mail does not carry this path's markers (bracketed subject + From: pogod)"
        head -20 "$MAILFILE" >&2
    fi
    if grep -q "MERGED BUT NOT CLOSED:" "$MAILFILE"; then
        bad "OBS-6b the mail carries the OLD filernotify subject — the two paths are not separated"
    else
        ok "OBS-6b it does NOT carry the old filernotify subject (MERGED BUT NOT CLOSED:)"
    fi
    grep -q "mg done $WI --successor=" "$MAILFILE" \
        && ok "OBS-7  the remedy travels with it (mg done $WI --successor=<id>)" \
        || bad "OBS-7  the mail does not carry the remedy line"
fi

# The state the alert is ABOUT actually obtains.
if mg show "$WI" 2>/dev/null | grep -qE '^Status:[[:space:]]+(done|archived)'; then
    bad "OBS-8  the item closed after all — the alert would have been about nothing"
else
    ok "OBS-8  the item is STILL OPEN with its work on main — the state the alert reports"
fi

# ===========================================================================
# PHASE 3 — THE MERGED-WORK GATE, at the dispatch chokepoint
# ===========================================================================
step "7. PHASE 3 — the merged-work gate refuses a dispatch onto $WI"

# RELEASE THE CLAIM FIRST, because that is the live shape and it is the whole
# hazard: pogod stops the merged polecat about half a second after the merge and
# its claim goes with it, so the item lands back in available/ — unclaimed, open,
# and finished — which is exactly what priority-wake then advertises as ready.
# The claim staged in file_declaring_item stood in for the polecat's; here the
# polecat would be gone.
mg unclaim "$WI" >/dev/null 2>&1 || note "could not unclaim $WI (the gate is asserted below regardless)"
mg show "$WI" 2>/dev/null | grep -qE '^Status:[[:space:]]+available' \
    && ok "OBS-8b the item is back in available/ — the exact state priority-wake advertises as ready" \
    || bad "OBS-8b the item did not return to available/ after the claim was released"

SPAWN_OUT="$(pogo agent spawn-polecat "probe$$" \
    --task="re-derive already-merged work" \
    --body="this dispatch must be refused" \
    --id="$WI" \
    --repo="$WORK" \
    --branch=main 2>&1)"
SPAWN_RC=$?

if [ "$SPAWN_RC" != "0" ]; then
    ok "OBS-9  the spawn was REFUSED (exit $SPAWN_RC)"
else
    bad "OBS-9  the spawn SUCCEEDED — a worker was dispatched onto merged work"
fi
note "refusal: $(printf '%s' "$SPAWN_OUT" | tr '\n' ' ' | cut -c1-320)"

# WHICH gate refused. "HAS ALREADY MERGED" and "--merged-override" occur in
# exactly one refusal string in the tree (internal/agent/mergedgate.go); the
# four gates ahead of it at this chokepoint say something else entirely.
if printf '%s' "$SPAWN_OUT" | grep -q "HAS ALREADY MERGED" \
   && printf '%s' "$SPAWN_OUT" | grep -q "merged-override"; then
    ok "OBS-10 it was THE MERGED-WORK GATE that refused (its refusal text, not another gate's)"
else
    bad "OBS-10 the refusal is not the merged-work gate's — some other gate answered first"
fi

# The refusal abbreviates the sha (shortMergedSHA), so the ASSERTION is on the
# abbreviation of the sha that is actually on origin/main — which is still the
# property that matters: a reader can check the claim with one `git log`.
if printf '%s' "$SPAWN_OUT" | grep -q "${MERGED_SHA:0:12}"; then
    ok "OBS-11 the refusal quotes ${MERGED_SHA:0:12}, the commit that is on origin/main — the claim is checkable"
else
    bad "OBS-11 the refusal does not quote the sha that is on origin/main ($MERGED_SHA)"
fi

# A REFUSED DISPATCH LEAVES NOTHING BEHIND (mg-ef80).
if [ -d "$POGO_HOME/polecats/probe$$" ] || [ -d "$POGO_HOME/agents/probe$$" ]; then
    bad "OBS-12 the refused dispatch left a worktree or agent dir behind"
else
    ok "OBS-12 the refused dispatch left no worktree and no agent dir"
fi

# The override is the proof that OBS-9 came from THIS gate: --merged-override
# bypasses this refusal and no other.
PROBE_AGENTS+=("probeov$$")
OVERRIDE_OUT="$(pogo agent spawn-polecat "probeov$$" \
    --task="re-derive already-merged work, deliberately" \
    --body="override probe" \
    --id="$WI" \
    --repo="$WORK" \
    --branch=main \
    --merged-override="runtime observation of the override arm (mg-161a)" 2>&1)"
OVERRIDE_RC=$?
if [ "$OVERRIDE_RC" = "0" ]; then
    ok "OBS-13 --merged-override dispatches over the same refusal, so OBS-9 was this gate and nothing else"
    if wait_for_event dispatch_merged_work_overridden "$WI"; then
        ok "OBS-14 the override is RECORDED: dispatch_merged_work_overridden names the reason"
        note "$(grep "dispatch_merged_work_overridden" "$EVENTS" | grep "$WI" | head -1 | cut -c1-400)"
    else
        bad "OBS-14 no dispatch_merged_work_overridden event — the override was silent"
    fi
else
    bad "OBS-13 --merged-override did NOT dispatch (exit $OVERRIDE_RC) — something other than the merged gate is refusing"
    note "override said: $(printf '%s' "$OVERRIDE_OUT" | tr '\n' ' ' | cut -c1-320)"
fi

# ---------------------------------------------------------------------------
step "observation complete"
echo "  ran $RUN observations; $PASSED passed"
if [ "$PASSED" -eq "$RUN" ]; then
    echo "PASS: $PASSED/$RUN"
    exit 0
fi
echo "FAIL: $PASSED/$RUN" >&2
exit 1
