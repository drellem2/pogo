#!/usr/bin/env bash
# =============================================================================
# pogo-condition-controls.sh — POSITIVE CONTROLS FOR THE pogod CONDITION
# ANNUNCIATOR (mg-342d, carrying mg-c3f0's enumeration)
# =============================================================================
#
#   scripts/pogo-condition-controls.sh            # every control (~3 min, 15 boots)
#   scripts/pogo-condition-controls.sh NEG A2     # the merge-time subset (~1 min)
#   scripts/pogo-condition-controls.sh A11        # one row
#
# WHY THIS FILE EXISTS AND WHY IT IS NOT A UNIT TEST
# -------------------------------------------------
# mg-342d wires fourteen enumerated pogod conditions to mail. Its acceptance bar
# is explicit and it is the right bar:
#
#     "A positive control per condition you wire: force the condition and show
#      the mail arrives at the named actor. A notification path verified only by
#      reading the code is not verified."
#
# That sentence is not pedantry. The condition this whole line of work descends
# from (A1, mg-c3f0) was a CORRECT, loud, well-worded log line that no one
# received for seven days. Every unit test in cmd/pogod passes a recorder into
# the mail seam — necessarily, because a test that shells out to the real `mg`
# mails a live crew agent and manufactures a fleet alarm — and a recorder proves
# the annunciator decided to send. It cannot prove a mail EXISTS in a maildir,
# which is the only claim that matters.
#
# So this runs a real pogod, forces each condition for real, and reads the mail
# back out of a real (sandboxed) macguffin store with `mg mail list`.
#
# THREE DIRECTIONS PER CONDITION, because one is not a control
# -----------------------------------------------------------
#   1. POSITIVE — force the condition, assert the mail arrives at the named
#      actor. Also asserts the underlying pogod log line fired, separately: if
#      the forcing did not reproduce the condition, that is an INSTRUMENT
#      failure, and reporting it as a missing mail would be a wrong diagnosis.
#      A probe that cannot tell "the alarm is broken" from "I failed to break
#      anything" is not a probe.
#   2. NEGATIVE — a clean sandbox must raise NOTHING and mail NOTHING. Without
#      this, a bug that annunciates unconditionally passes every positive
#      control in the file. The architect's note on this ticket makes the point
#      in general terms: an errored or empty instrument reads the same as a
#      healthy one unless something proves it can go the other way.
#   3. SUPPRESSION — reboot with the condition still live and assert the mail
#      count does NOT change. This is the one property that decides whether the
#      alarm survives being real: seven identical mails is how a genuine alarm
#      gets filtered out, which is mg-c3f0's binding constraint.
#
# WHAT IT DOES NOT CLAIM
# ----------------------
# Five of the wired conditions are not forceable from outside the process and
# are NOT controlled here. They are listed in the summary rather than omitted —
# an enumeration that shrinks silently is the failure mg-342d exists to end —
# and each says what stands in for the live control:
#
#   A2b  scheduler_disabled_no_home   requires os.UserHomeDir() to fail.
#   A5b  autostart_failed (non-coord) needs a second auto_start crew prompt that
#                                     survives InstallPrompts; differs from the
#                                     controlled arm by one bool, pinned by
#                                     TestA5NamesTheCaseThatCannotBeDeliveredLive.
#   A6   restart_failed               needs a spawn that succeeds once and then
#                                     fails on respawn, with no seam to force it.
#   A13  ghteardown_not_armed         pogod's own pathenv.Ensure REPAIRS PATH
#                                     before the LookPath, so `gh` cannot be
#                                     hidden from it by setting PATH.
#   A15  worktree_notice_undelivered  emits an event, not a mail, by design.
#
# All five go through the identical Raise path the nine controlled rows exercise;
# what is unproven for them is the FORCING, not the delivery.
#
# THIS FILE HAS BEEN OBSERVED GOING RED (2026-07-30)
# --------------------------------------------------
# A control observed only passing has not been observed working. Sabotaged
# `conditionAnnunciator.Raise` into an unconditional early return, built that,
# and ran `NEG A2` against it: **7 failures**, naming the missing delivery, the
# missing wake, the missing suppression record, and the missing clear.
#
# The instructive part is which assertions did NOT fail. **All three NEGATIVE
# controls still passed** — a completely dead annunciator does report "no live
# conditions" and does mail nobody, so a clean boot looks identical whether the
# mechanism is healthy or absent. That is the same shape as this ticket's whole
# subject (silence is not evidence), and it is why the negative control cannot
# stand alone: each direction catches exactly what the other cannot.
# =============================================================================

set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/pogo-sandbox"

# By default this file BUILDS ITS OWN pogod from $REPO_ROOT, so a merge-time run is
# a control on the commit rather than on whatever happens to be in ./bin. Same
# seam as scripts/pogo-self-deploy_live_test.sh (mg-bfe5): POGO_CONTROL_POGOD
# points it at a prebuilt artifact when the question is "do these bytes work"
# rather than "does this commit work".
POGOD_BIN="${POGO_CONTROL_POGOD:-${POGOD_BIN:-}}"
BOOT_WAIT="${POGO_CONTROL_BOOT_WAIT:-9}"

PASS=0
FAIL=0
SKIP=0
declare -a FAILURES=()

# ---------------------------------------------------------------------------
# reporting
# ---------------------------------------------------------------------------
ok()   { PASS=$((PASS + 1)); printf '  \033[32mPASS\033[0m  %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); FAILURES+=("$1"); printf '  \033[31mFAIL\033[0m  %s\n' "$1"; }
note() { printf '        %s\n' "$1"; }
hdr()  { printf '\n=== %s ===\n' "$1"; }

# An instrument failure is reported as its own category. A control that says
# "no mail arrived" when in truth it failed to break anything is worse than no
# control: it produces a confident wrong diagnosis.
instrument_fail() {
    FAIL=$((FAIL + 1))
    FAILURES+=("INSTRUMENT: $1")
    printf '  \033[33mINSTRUMENT\033[0m  %s\n' "$1"
    note "The forcing did not reproduce the condition, so this run makes NO claim"
    note "about whether the notice would have been delivered."
}

# ---------------------------------------------------------------------------
# sandbox lifecycle
# ---------------------------------------------------------------------------

# fresh_sandbox LABEL — a private, proven-isolated root with HOME, XDG_CONFIG_HOME,
# POGO_HOME and MG_ROOT all under it. Delegated to scripts/pogo-sandbox because
# isolation done by hand is isolation done by remembering, and that has failed on
# this repo four times (mg-6092, mg-e8e7, mg-5336, mg-3412).
fresh_sandbox() {
    POGO_SANDBOX_DIR=""
    POGO_SANDBOX_ISOLATED=""
    pogo_sandbox_create "cond-$1"
    pogo_sandbox_isolate
    # pogo_sandbox_isolate pins POGO_AGENT_AUTOSTART=false so a stray config
    # cannot spawn the machine's fleet. Two controls (A2's wake, A5) need a crew
    # agent to exist, and they re-enable it deliberately with a harmless
    # `command`. Unset here so each control decides for itself.
    unset POGO_AGENT_AUTOSTART
    mkdir -p "$POGO_HOME"
    # REGISTER THE COORDINATOR'S MAILBOX IN THE SANDBOX (mg-d639). Every control
    # below asserts that pogod's annunciator DELIVERED a notice to `mayor`, and
    # the sandbox's MG_ROOT is a fresh empty store, so `mayor` is a name mg has
    # never seen there. mg used to file mail for an unknown name; it now refuses
    # (no_such_mailbox, exit 3), and six controls went red reporting "pogod did
    # not report mailing mayor" — true, and for a reason that says nothing about
    # pogod.
    #
    # `mg mail register`, NOT `--create` on pogod's own send: the annunciator is
    # production code and must keep refusing a coordinator name that names no
    # box, because that refusal is a real misconfiguration on a real install
    # (mg-7dc1 is the systemic repair, and pm-pogo's ruling there is explicitly
    # that --create must not spread to callsites). What was missing is the
    # sandbox's own setup, so the fixture is what gains the registration.
    # Idempotent by contract, and it creates the Maildir without sending, so
    # mail_count starts at 0 exactly as the controls assume.
    #
    # Reported as an INSTRUMENT failure, not swallowed, and not as a plain FAIL.
    # A seed that fails quietly is how this cost an evening: six controls read
    # "pogod did not report mailing mayor" — a confident statement about pogod,
    # produced by the harness never having given it a mailbox to mail. That is
    # precisely the category instrument_fail exists to keep separate.
    if ! mg mail register mayor >/dev/null 2>&1; then
        instrument_fail "could not register the coordinator mailbox 'mayor' under MG_ROOT=${MG_ROOT:-<unset>} — every 'notice delivered' assertion below would report on the seeding, not on pogod"
    fi
}

# boot [LOGFILE] — start the sandbox pogod and wait for it to finish its startup
# annunciation. Not sandbox_daemon_start, for one reason: that helper truncates
# its log with `>`, and the A14 control needs to APPEND to a pre-seeded oversized
# pogod.log at the launchd path. The port reservation and privacy proofs still
# come from the harness.
#
# boot SETS $POGOD_LOG, so a caller must never derive its next log path from
# $POGOD_LOG — that compounds ("pogod.log.reboot.healed.healed") and every
# assertion afterwards greps a file that does not exist. Use boot_log NAME.
POGOD_PID=""
POGOD_LOG=""

# boot_log NAME — a fresh, non-compounding log path inside this sandbox.
boot_log() { printf '%s/pogod-%s.log' "$POGO_SANDBOX_DIR" "$1"; }

boot() {
    POGOD_LOG="${1:-$POGO_SANDBOX_DIR/pogod.log}"
    if [ -z "${POGO_SANDBOX_PORT:-}" ]; then
        sandbox_port_reserve
        POGO_SANDBOX_PORT="$SANDBOX_PORT"
        POGO_SANDBOX_URL="http://127.0.0.1:$POGO_SANDBOX_PORT"
    fi
    "$POGOD_BIN" -port "$POGO_SANDBOX_PORT" >> "$POGOD_LOG" 2>&1 &
    POGOD_PID=$!
    sleep "$BOOT_WAIT"
    if ! kill -0 "$POGOD_PID" 2>/dev/null; then
        wait "$POGOD_PID" 2>/dev/null
        instrument_fail "the sandbox pogod exited during boot; see $POGOD_LOG"
        return 1
    fi
    return 0
}

halt() {
    [ -n "$POGOD_PID" ] || return 0
    kill "$POGOD_PID" 2>/dev/null
    wait "$POGOD_PID" 2>/dev/null
    POGOD_PID=""
    sleep 1
}

# ---------------------------------------------------------------------------
# assertions
# ---------------------------------------------------------------------------

# assert_log_line PATTERN DESC — the underlying pogod condition actually fired.
# Checked BEFORE the mail so an unreproduced condition is diagnosed as an
# instrument failure rather than as a missing notice.
assert_log_line() {
    if grep -qE "$1" "$POGOD_LOG" 2>/dev/null; then
        return 0
    fi
    instrument_fail "$2 (no log line matching /$1/ in $POGOD_LOG)"
    return 1
}

# assert_mailed ID ROW TO — the annunciator says it mailed, AND the mail is in the
# addressee's maildir. Both halves: the first is the decision, the second is the
# only thing that means a reader can ever see it.
assert_mailed() {
    local id="$1" row="$2" to="$3" n
    if ! grep -qF "condition $id ($row) mailed to $to" "$POGOD_LOG" 2>/dev/null; then
        bad "$row/$id: pogod did not report mailing $to"
        note "$(grep -E 'condition |annunciator' "$POGOD_LOG" | tail -5)"
        return 1
    fi
    n="$(mail_count "$to")"
    if [ "$n" -lt 1 ]; then
        bad "$row/$id: pogod SAID it mailed $to and $to's maildir is EMPTY — this is the A1 defect exactly (a correct report nobody received)"
        return 1
    fi
    ok "$row/$id: notice delivered to $to's maildir ($n unread)"
    return 0
}

# mail_count TO — unread in the sandboxed maildir. `mg` roots at $MG_ROOT, which
# pogo_sandbox_isolate pinned inside the sandbox, so this can never read the
# machine's real ~/.macguffin/mail.
mail_count() {
    local out
    out="$(mg mail list "$1" 2>/dev/null)"
    case "$out" in
        *"No mailbox for"*) printf '0'; return 0 ;;
    esac
    printf '%s' "$(printf '%s\n' "$out" | grep -c '●')"
}

# assert_subject_mentions TO NEEDLE DESC — the notice a recipient actually opens
# has to name the fault. A delivered mail with an unreadable subject is a
# delivered mail nobody triages.
assert_subject_mentions() {
    if mg mail list "$1" 2>/dev/null | grep -qF "$2"; then
        ok "$3"
    else
        bad "$3 — no delivered subject contains \"$2\""
    fi
}

# assert_suppressed_on_reboot ID ROW TO — the condition is still live on the next
# boot and must NOT mail again.
assert_suppressed_on_reboot() {
    local id="$1" row="$2" to="$3" before after rlog
    before="$(mail_count "$to")"
    rlog="$(boot_log reboot)"
    halt
    : > "$rlog"
    boot "$rlog" || return 1
    after="$(mail_count "$to")"
    if [ "$after" != "$before" ]; then
        bad "$row/$id: rebooting with the condition STILL LIVE mailed again ($before → $after). Repetition is how a real alarm gets filtered out — mg-c3f0's binding constraint"
        return 1
    fi
    if ! grep -qF "suppressed" "$rlog" 2>/dev/null; then
        bad "$row/$id: the second boot neither mailed nor recorded a suppression, so a persisting condition is indistinguishable from a resolved one on the spine"
        return 1
    fi
    ok "$row/$id: suppressed on the next boot, and the suppression is on the record"
    return 0
}

# ---------------------------------------------------------------------------
# the controls
# ---------------------------------------------------------------------------

# A2 — scheduler load failed. The enumeration's own highest severity, and the
# only row that also asks to WAKE its addressee: every agent's mail-check loop is
# a scheduler entry, so on this boot mail is deliverable but nothing will prompt
# anyone to read it.
#
# This control is the evidence for that claim rather than an assertion of it. It
# runs with a crew agent auto-starting, and asserts the PTY nudge lands while the
# scheduler is confirmed down — so the wake channel is proven not to depend on the
# subsystem whose failure it reports.
control_A2() {
    hdr "A2 — scheduler load failed (mail AND wake, with the scheduler confirmed down)"
    fresh_sandbox a2
    cat > "$POGO_HOME/config.toml" <<'EOF'
[agents]
autostart = true
command = "sleep 600"
coordinator = "mayor"

[ack_watch]
enabled = true

[heartbeat]
interval = "3s"
EOF
    printf '{ this is not valid json' > "$POGO_HOME/schedules.json"

    boot || { halt; return; }
    assert_log_line 'scheduler load failed' "the corrupt schedules.json did not make scheduler.New fail" || { halt; return; }

    assert_mailed "scheduler_load_failed" "A2" "mayor"
    assert_subject_mentions mayor "SCHEDULER DID NOT LOAD" \
        "A2: the subject states the fleet-wide consequence, not just the error"

    # The wake. This is the half mg-c3f0 §6 stopped at: "mailing the coordinator
    # works, but the coordinator will not be WOKEN to read it, only mailed."
    if grep -qF "WOKEN to read the notice" "$POGOD_LOG"; then
        ok "A2: the coordinator was actively NUDGED with the scheduler down — mail is not the only channel"
        note "This is why an in-pogod detector for A2 is not dead by its own fault:"
        note "the nudge rides the heartbeat, and the heartbeat DRIVES the scheduler"
        note "rather than depending on it (main.go's OnTick: 'if sched != nil')."
    else
        bad "A2: no PTY wake landed. Mail alone reaches a maildir nothing will prompt the coordinator to open, which is exactly where mg-c3f0 left this row"
    fi

    # A3 rides the same boot: ack-watch cannot arm without the scheduler.
    hdr "A3 — ack-watch NOT armed (disabled by the failure it would have caught)"
    assert_log_line 'ack-watch NOT armed' "ack_watch was enabled but the not-armed branch never ran" \
        && assert_mailed "ackwatch_not_armed" "A3" "mayor"

    assert_suppressed_on_reboot "scheduler_load_failed" "A2" "mayor"

    # The CLEAR direction, and the recurrence after it. Without the clear, a
    # condition that broke, was fixed, and broke again would be silenced by its
    # own resolved history — a failure that produces no symptom at all.
    local healed again before after
    healed="$(boot_log healed)"
    again="$(boot_log again)"
    halt
    rm -f "$POGO_HOME/schedules.json"
    : > "$healed"
    boot "$healed" || { halt; return; }
    if grep -qF "condition scheduler_load_failed CLEARED" "$healed"; then
        ok "A2: a resolved condition is CLEARED and recorded as resolved"
    else
        bad "A2: the healthy boot did not clear the condition; the store would grow and a recurrence would be suppressed by resolved history"
    fi

    before="$(mail_count mayor)"
    halt
    printf '{ broken again' > "$POGO_HOME/schedules.json"
    : > "$again"
    boot "$again" || { halt; return; }
    after="$(mail_count mayor)"
    if [ "$after" -gt "$before" ]; then
        ok "A2: a recurrence after a clear mails IMMEDIATELY rather than inheriting the old quiet window"
    else
        bad "A2: a recurrence after a clear was suppressed ($before → $after) — the alarm is silent for the second incident"
    fi
    halt
}

# A4 — prompt refresh failed. Strictly worse than the A1 that started this: A1 is
# one prompt declined for a reason, A4 is every prompt stale for none.
# A7 rides the same boot (unknown provider), and A11 arrives on the first
# heartbeat tick.
control_A4_A7_A11() {
    hdr "A4 / A7 / A11 — prompt refresh, unknown provider, own-heartbeat write"
    fresh_sandbox a4
    mkdir -p "$POGO_HOME/agents"
    cat > "$POGO_HOME/config.toml" <<'EOF'
[agents]
autostart = false
command = "sleep 600"
coordinator = "mayor"
provider = "not-a-real-provider"

[heartbeat]
interval = "3s"
EOF
    # A11: pogod writes $POGO_HOME/health/pogod.heartbeat. A regular file where the
    # directory must be makes every write fail, for as long as it is there.
    : > "$POGO_HOME/health"
    # A4: InstallPrompts writes under agents/. Read-execute only, so it can look
    # and cannot write — the shape a permissions or full-disk fault takes.
    chmod 0500 "$POGO_HOME/agents"

    boot || { chmod 0755 "$POGO_HOME/agents"; halt; return; }

    assert_log_line 'prompt refresh failed' "the unwritable agents dir did not make InstallPrompts fail" \
        && assert_mailed "prompt_refresh_failed" "A4" "mayor"
    assert_log_line 'unknown agent provider' "the bogus provider id was resolved successfully" \
        && assert_mailed "unknown_provider:not-a-real-provider" "A7" "mayor"
    assert_log_line 'failed to write own heartbeat' "the health-dir collision did not break the heartbeat write" \
        && assert_mailed "pogod_heartbeat_write_failed" "A11" "mayor"

    # A11 fires every heartbeat tick (3s here). The per-condition floor is the
    # only thing standing between this row and ~2900 mails a day, so it is worth
    # measuring rather than trusting.
    local n0 n1
    n0="$(mail_count mayor)"
    sleep 12
    n1="$(mail_count mayor)"
    if [ "$n1" = "$n0" ]; then
        ok "A11: four more heartbeat ticks with the write still failing produced no further mail (the hard floor holds)"
    else
        bad "A11: mail count moved $n0 → $n1 across four ticks. A condition that recurs every 3s must be floored, or the channel is a firehose"
    fi

    chmod 0755 "$POGO_HOME/agents"
    halt
}

# A5 — auto-start failed, and specifically the case the enumeration called
# genuinely hard: the agent that failed to start IS the coordinator, so the actor
# and the casualty are the same process.
control_A5() {
    hdr "A5 — the COORDINATOR failed to auto-start (no in-fleet reader by construction)"
    fresh_sandbox a5
    cat > "$POGO_HOME/config.toml" <<'EOF'
[agents]
autostart = true
command = "/nonexistent/binary/xyz"
coordinator = "mayor"
EOF
    boot || { halt; return; }
    assert_log_line 'auto-start of mayor failed' "the missing binary did not make the spawn fail" || { halt; return; }
    assert_mailed "autostart_failed:mayor" "A5" "mayor"
    assert_subject_mentions mayor "THE COORDINATOR (mayor) FAILED TO AUTO-START" \
        "A5: the notice does not disguise the coordinator case as an ordinary crew failure"

    # The honest half. The mail is in the maildir and will be read on the first
    # mail-check after the coordinator next starts — but nothing read it at the
    # time, and a notice implying otherwise would be a false reassurance in the
    # one case that matters.
    # Read the body back out of the maildir — the whole point of a live control is
    # that the delivered ARTIFACT is inspected, not the string the code would have
    # sent. `mg mail read` takes the id `mg mail list` printed after the slash.
    local msgid
    msgid="$(mg mail list mayor 2>/dev/null | grep -oE 'mayor/[0-9.]+' | head -1 | cut -d/ -f2)"
    if [ -n "$msgid" ] && mg mail read mayor "$msgid" --force 2>/dev/null | grep -qF "no in-fleet reader"; then
        ok "A5: the DELIVERED body states plainly that nothing read it when the fault occurred"
    else
        bad "A5: could not read back a delivered body containing 'no in-fleet reader' (msgid=${msgid:-none}). A notice that overstates its own delivery is a false reassurance in the one case that matters"
    fi
    assert_suppressed_on_reboot "autostart_failed:mayor" "A5" "mayor"
    halt
}

# A9 — git GC degraded. Unbounded branch and worktree growth with no symptom
# until a disk fills.
control_A9() {
    hdr "A9 — git GC skipped (no work-item index, so NO sweep runs on any repo)"
    fresh_sandbox a9
    cat > "$POGO_HOME/config.toml" <<'EOF'
[agents]
autostart = false
command = "sleep 600"
coordinator = "mayor"

[gitgc]
enabled = true
repos = ["/nonexistent/repo/path"]
EOF
    # No macguffin store exists in a fresh sandbox, so the ticket-index load fails
    # for real rather than by injection — the same guard that refuses to sweep
    # without knowing which work items exist (mg-0130).
    boot || { halt; return; }
    assert_log_line 'git GC skipped' "the sweep ran despite there being no work-item index" \
        && assert_mailed "gitgc_no_ticket_index" "A9" "mayor"
    halt
}

# A10 — role-default pin failed. An unpinned role name can be renamed by a later
# upgrade, and an agent name is also a mailbox name and a schedule id.
control_A10() {
    hdr "A10 — role-default pin failed (role names left unpinned)"
    fresh_sandbox a10
    # A v0.3.0-era config: [agents] present, role keys absent, so a pin is needed.
    # Read-only, so the write fails where a real permissions fault would.
    printf '[agents]\nautostart = false\ncommand = "sleep 600"\n' > "$POGO_HOME/config.toml"
    chmod 0444 "$POGO_HOME/config.toml"
    boot || { chmod 0644 "$POGO_HOME/config.toml"; halt; return; }
    assert_log_line 'role-default pin failed' "the read-only config.toml was pinned anyway" \
        && assert_mailed "role_pin_failed" "A10" "mayor"
    chmod 0644 "$POGO_HOME/config.toml"
    halt
}

# A14 — log rotation failed. The row whose consequence is the other thirteen: the
# post-mortem log ~90 conditions fall back to may be lost or grow unbounded.
control_A14() {
    hdr "A14 — log rotation failed (the fallback channel for the other thirteen)"
    fresh_sandbox a14
    printf '[agents]\nautostart = false\ncommand = "sleep 600"\ncoordinator = "mayor"\n' > "$POGO_HOME/config.toml"

    # Rotation is a no-op unless stderr IS the launchd log path, so the log has to
    # be that exact file, over the 10MiB threshold, in a directory that cannot be
    # renamed into.
    local logdir="$HOME/Library/Logs/pogo" log
    mkdir -p "$logdir"
    log="$logdir/pogod.log"
    dd if=/dev/zero bs=1048576 count=11 2>/dev/null | tr '\0' 'x' > "$log"
    chmod 0500 "$logdir"

    boot "$log"
    chmod 0755 "$logdir"
    assert_log_line 'log rotation failed' "the 11MiB log in a read-only dir rotated successfully" \
        && assert_mailed "log_rotation_failed" "A14" "mayor"
    halt
}

# THE NEGATIVE CONTROL, and it is not optional. Every positive control above
# passes just as well against an annunciator that mails unconditionally. This is
# the only assertion in the file that can tell the difference.
control_negative() {
    hdr "NEGATIVE CONTROL — a healthy pogod raises nothing and mails nobody"
    fresh_sandbox clean
    printf '[agents]\nautostart = false\ncommand = "sleep 600"\ncoordinator = "mayor"\n' > "$POGO_HOME/config.toml"
    boot || { halt; return; }

    if grep -qF "condition annunciator: no live conditions" "$POGOD_LOG"; then
        ok "a clean boot reports no live conditions"
    else
        bad "a clean boot did not report 'no live conditions'; every positive control above is uninterpretable if the annunciator fires on a healthy daemon"
        note "$(grep -E 'condition |annunciator' "$POGOD_LOG" | tail -8)"
    fi
    local n
    n="$(mail_count mayor)"
    if [ "$n" = "0" ]; then
        ok "a clean boot delivered no mail at all"
    else
        bad "a clean boot delivered $n mail(s) to mayor — the annunciator is a firehose, and every PASS above is meaningless"
    fi
    # And the summary must fire even here: a boot that emits no summary is a boot
    # where the annunciator is not running, which must not look like a clean one.
    if grep -qF "condition annunciator:" "$POGOD_LOG"; then
        ok "the per-boot summary fires on a CLEAN boot too, so its absence means 'not running' rather than 'nothing wrong'"
    else
        bad "no per-boot summary on a clean boot — a stopped annunciator would be indistinguishable from a healthy fleet, which is this ticket's whole defect one level up"
    fi
    halt
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
if [ -z "$POGOD_BIN" ]; then
    POGOD_BIN="$(mktemp -d)/pogod"
    printf 'building pogod from %s ...\n' "$REPO_ROOT"
    (cd "$REPO_ROOT" && go build -o "$POGOD_BIN" ./cmd/pogod) \
        || pogo_sandbox_fail "could not build pogod from $REPO_ROOT — the controls below would have measured nothing"
fi
[ -x "$POGOD_BIN" ] || pogo_sandbox_fail "pogod binary not found at $POGOD_BIN"
command -v mg >/dev/null 2>&1 || pogo_sandbox_fail "\`mg\` is not on PATH, so no control in this file could read a maildir back"

trap 'halt; pogo_sandbox_down 2>/dev/null || true' EXIT

WANT="${*:-ALL}"
want() { case " $WANT " in *" ALL "*|*" $1 "*) return 0 ;; esac; return 1; }

printf 'pogod condition annunciator — live positive controls (mg-342d)\n'
printf 'binary: %s\n' "$POGOD_BIN"

want NEG && control_negative
want A2  && control_A2
want A4  && control_A4_A7_A11
want A5  && control_A5
want A9  && control_A9
want A10 && control_A10
want A14 && control_A14

hdr "RESULTS"
printf '  %d passed, %d failed, %d indeterminate\n' "$PASS" "$FAIL" "$SKIP"
printf '\n  NOT CONTROLLED HERE (forcing not reachable from outside the process):\n'
printf '    A2b scheduler_disabled_no_home  — needs os.UserHomeDir() to fail\n'
printf '    A5b autostart_failed, non-coordinator arm — differs by one bool, unit-pinned\n'
printf '    A6  restart_failed — needs a spawn that succeeds once then fails\n'
printf '    A13 ghteardown_not_armed — pogod repairs PATH before the LookPath\n'
printf '    A15 worktree_notice_undelivered — an event by design, not a mail\n'
printf '  Declined outright, with reasons: A8, A12 (see cmd/pogod/conditions_test.go)\n'

if [ "$FAIL" -gt 0 ]; then
    printf '\n\033[31mFAILURES:\033[0m\n'
    for f in "${FAILURES[@]}"; do printf '  - %s\n' "$f"; done
    exit 1
fi
printf '\n\033[32mall controls green\033[0m\n'
exit 0
