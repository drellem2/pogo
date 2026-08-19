#!/usr/bin/env bash
# Controls on scripts/fleet-liveness-probe.sh — the EXTERNAL witness that the
# FLEET is still completing turns (mg-f867).
#
# TWO CASES ARE LOAD-BEARING and the rest are here to make their verdicts
# interpretable.
#
# SECTION 6 — THE THIRD CELL. The spec this probe was built from had a design
# defect that pm-riemann found by implementing it: the per-agent form of the
# liveness test read uptime from the wrong awk field, the parse yielded zero, a
# zero uptime puts process_start at NOW, and every one of seven agents read as
# dead — including ones that were plainly fine. A launchd observer mailing on
# that would page the fleet over a format change in `pogo agent list`. The rule
# it established is A FAILED MEASUREMENT MUST NOT FAIL TOWARD ALARM, and the
# remedy is a third cell: UNMEASURABLE, reported per row, never folded into
# either verdict. Section 6 stages four different unmeasurable states — an
# absent directory, an empty population, an unstattable row, and a FUTURE mtime
# — and asserts none of them produces FLEET-STOP and none of them produces OK.
#
# The future-mtime case is the one that matters most and is easiest to skip: it
# is the only way this predicate could fail toward GREEN (a negative age is
# younger than every threshold), which is the exact silence the whole ticket is
# about, reproduced by its own remedy.
#
# SECTION 8 — THE DELIVERY HALF, AND THE REASON IT IS TESTED THE WAY IT IS.
# revision-probe's --mail path delivered three correct alerts to `human` on 08-16
# and 08-17 and then went silent mid-incident with NO code change (mg-7ce7). Its
# capability probe is `"$cand" --help | grep -q macguffin` under `set -uo
# pipefail`: grep exits on the first match, the producer takes SIGPIPE and exits
# 141, pipefail surfaces it, and a working binary is reported ABSENT.
#
# THAT IS A RACE, and a race cannot be tested by running it once. The same idiom
# against `git --version` and `curl --version` passes 10/10 on this box because
# those producers finish writing ~25 bytes before grep closes the pipe; `mg`
# writes 2404 bytes from a 7.7MB Go binary and loses 10/10. Two of the three call
# sites are the identical bug, currently winning.
#
# So section 8 FORCES THE LOSING SIDE: a deliberately chatty stub mg, and its
# FIRST assertion is that the OLD idiom really does get 141 against that fixture.
# Without that assertion the section would pass against a stub that never
# SIGPIPEd and would be certifying timing luck as correctness. Only then does it
# assert this probe finds the same binary.
#
# SECTION 9 AND 10 are the other half of the same requirement: the ledger must
# record the send RESULT and not the send ATTEMPT, so a stub that exits 0 and
# delivers nothing must be recorded as `attempted-unconfirmed` and must NOT start
# the re-notify throttle. Collapsing computed / attempted / delivered is what let
# a count of refusal lines be read as "this path never worked" — an inference
# that had to be withdrawn once the mail record was consulted.
#
# There is no real `mg` anywhere in this file. Every mail case runs against a
# stub maildir, because a control that writes to ~/.macguffin is one nobody can
# run twice.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
pogo_sandbox_create fleetprobe
SANDBOX="$POGO_SANDBOX_DIR"

cleanup() {
    pogo_sandbox_down
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

pogo_sandbox_isolate

PROBE="$HERE/fleet-liveness-probe.sh"
[ -x "$PROBE" ] || pogo_sandbox_fail "scripts/fleet-liveness-probe.sh is not executable — the thing under test cannot be run"

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------
# NOW is a fixed synthetic epoch. Every mtime below is set relative to it with
# `touch -t`, so the whole suite is time-independent — a control whose verdict
# depends on when it runs is one that goes red at 3am for nobody.

NOW=1786000000        # 2026-08-06T06:46:40Z, an arbitrary fixed point

# set_mtime FILE EPOCH — portable, because `touch -d @epoch` is GNU-only.
set_mtime() {
    perl -e 'utime $ARGV[1], $ARGV[1], $ARGV[0] or die "utime: $!"' "$1" "$2" \
        || pogo_sandbox_fail "could not set an mtime on $1 — the whole suite is built on them"
}

# mkfleet DIR spec... — each spec is NAME:AGE_SECONDS
mkfleet() {
    local dir="$1"; shift
    rm -rf "$dir"; mkdir -p "$dir"
    local spec name age
    for spec in "$@"; do
        name="${spec%%:*}"; age="${spec##*:}"
        echo "$name completed a turn" > "$dir/$name.log"
        set_mtime "$dir/$name.log" $(( NOW - age ))
    done
}

LEDGER="$SANDBOX/ledger"
STAMP="$SANDBOX/stamp"

# run_probe wraps the invocation every case shares. --no-self-test by default:
# the delivery control has its own sections and must not run inside the
# measurement cases, or a mail failure would be read as a measurement failure.
run_probe() {
    "$PROBE" --now "$NOW" --stamp "$STAMP" --log "$LEDGER" --no-self-test "$@"
}

ledger_last() { tail -1 "$LEDGER" 2>/dev/null; }
ledger_lines() { [ -f "$LEDGER" ] && wc -l < "$LEDGER" | tr -d ' ' || echo 0; }

# ---------------------------------------------------------------------------
# 1. OK — some agent completed a turn inside the threshold
# ---------------------------------------------------------------------------

FLEET="$SANDBOX/turnlog"
mkfleet "$FLEET" mayor:600 doctor:7200 pa:100000

out="$(run_probe --turnlog-dir "$FLEET" --stale-after 2h)"; rc=$?
if [ "$rc" -eq 0 ] && case "$out" in *"OK — mayor.log"*) true ;; *) false ;; esac; then
    pass "a fleet whose newest turn is 10m old reads OK and names the agent"
else
    fail "a fresh fleet exited $rc: $out"
fi

# ---------------------------------------------------------------------------
# 2. NEWEST-ACROSS-ALL, never per-agent — the property the whole design rests on
# ---------------------------------------------------------------------------
# An idle PM legitimately goes hours between turns, and this box really carries a
# turnlog (`a270.log`) untouched since 2026-08-11 by design. A per-agent
# threshold pages on both. The fleet form must read GREEN over a population in
# which six of seven rows are ancient, provided ONE is fresh.

mkfleet "$FLEET" mayor:600 a270:604800 doctor:200000 pa:300000 \
        pm-pogo:250000 pm-riemann:260000 architect:270000
out="$(run_probe --turnlog-dir "$FLEET" --stale-after 2h)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "one fresh agent among six ancient ones reads OK — the predicate is the fleet, not the row"
else
    fail "six stale rows and one fresh one exited $rc — this is the per-agent false alarm the design refuses (exit want 0): $out"
fi

# And the converse: make the ONE fresh row stale and the same population alerts.
set_mtime "$FLEET/mayor.log" $(( NOW - 500000 ))
out="$(run_probe --turnlog-dir "$FLEET" --stale-after 2h --quiet)"; rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *"FLEET STOP"*) true ;; *) false ;; esac; then
    pass "the same population with NO fresh row alerts — the all-of-them condition is what fires"
else
    fail "an entirely stale fleet exited $rc, want 1 with a FLEET STOP headline: $out"
fi

# ---------------------------------------------------------------------------
# 3. The threshold is the boundary it says it is
# ---------------------------------------------------------------------------

mkfleet "$FLEET" mayor:7199
run_probe --turnlog-dir "$FLEET" --stale-after 2h >/dev/null 2>&1
rc_under=$?
mkfleet "$FLEET" mayor:7201
run_probe --turnlog-dir "$FLEET" --stale-after 2h >/dev/null 2>&1
rc_over=$?
if [ "$rc_under" -eq 0 ] && [ "$rc_over" -eq 1 ]; then
    pass "one second under the threshold is OK and one second over it alerts"
else
    fail "threshold boundary: 7199s exited $rc_under (want 0), 7201s exited $rc_over (want 1)"
fi

# ---------------------------------------------------------------------------
# 4. IT REPORTS EITHER WAY — the ledger is the witness's own heartbeat
# ---------------------------------------------------------------------------
# A witness that writes only when it is unhappy cannot be distinguished from a
# witness that is not running, and a witness that was not running is what this
# whole ticket is about.

rm -f "$LEDGER"
mkfleet "$FLEET" mayor:600
run_probe --turnlog-dir "$FLEET" --stale-after 2h >/dev/null 2>&1
mkfleet "$FLEET" mayor:500000
run_probe --turnlog-dir "$FLEET" --stale-after 2h >/dev/null 2>&1
run_probe --turnlog-dir "$SANDBOX/absent" --stale-after 2h >/dev/null 2>&1
run_probe --turnlog-dir "$FLEET" --stale-after nonsense >/dev/null 2>&1
n="$(ledger_lines)"
if [ "$n" = "4" ]; then
    pass "the ledger has one line per run — green, red, unmeasurable AND a setup failure"
else
    fail "the ledger has $n line(s) after four runs, want 4 — a green run or a setup failure that writes nothing makes 'no alert' and 'no probe' the same observation"
fi
if grep -q 'exit=0 OK' "$LEDGER" && grep -q 'exit=1 FLEET-STOP' "$LEDGER" \
    && grep -q 'exit=2 UNMEASURABLE' "$LEDGER" && grep -q 'SETUP-FAILED' "$LEDGER"; then
    pass "each of the four run outcomes is a DISTINCT ledger word"
else
    fail "the ledger does not distinguish the four outcomes:
$(cat "$LEDGER")"
fi

# ---------------------------------------------------------------------------
# 5. INDEPENDENCE — it touches nothing an agent turn or a deploy provides
# ---------------------------------------------------------------------------
# This probe must run on a box where the fleet has been dead for five days. If it
# reached for `pogo`, `pogod`, `go`, `jq`, `git` or `curl` it would be armed only
# under conditions it exists to report. An exit-code-only assertion would pass
# against a probe that shelled out to a `pogo` which happened to agree, so each
# poisoned binary writes a marker file and the markers are checked.

POISON="$SANDBOX/poison"
mkdir -p "$POISON"
for tool in go pogo pogod jq git curl; do
    cat > "$POISON/$tool" <<EOF
#!/bin/sh
echo "\$0 \$*" >> "$SANDBOX/poisoned.calls"
exit 66
EOF
    chmod +x "$POISON/$tool"
done
rm -f "$SANDBOX/poisoned.calls"

mkfleet "$FLEET" mayor:600
out="$(PATH="$POISON:$PATH" run_probe --turnlog-dir "$FLEET" --stale-after 2h)"; rc=$?
invoked=""
[ -f "$SANDBOX/poisoned.calls" ] && invoked=" $(awk '{print $1}' "$SANDBOX/poisoned.calls" | sort -u | tr '\n' ' ')"
if [ "$rc" -eq 0 ] && [ -z "$invoked" ]; then
    pass "with go/pogo/pogod/jq/git/curl POISONED first on PATH the probe still reaches OK, and invoked none of them"
else
    fail "the probe exited $rc and invoked$invoked — a witness that reaches for what the fleet or the deploy provides is armed only when they are working, which is mg-f867 verbatim"
fi

# ---------------------------------------------------------------------------
# 6. THE THIRD CELL — a failed measurement must not fail toward ALARM
# ---------------------------------------------------------------------------
# Four unmeasurable states. NONE may produce FLEET-STOP (exit 1) and none may
# produce OK (exit 0). This is the defect pm-riemann found in the spec by
# implementing it: a two-valued instrument has nowhere to put "I could not
# measure this", so the failure lands in whichever cell the arithmetic happens
# to produce — and for him that cell was `dead`, for all seven agents at once.

check_unmeasurable() {
    local label="$1"; shift
    local o r
    o="$("$@" 2>&1)"; r=$?
    if [ "$r" -eq 2 ] && case "$o" in *UNMEASURABLE*) true ;; *) false ;; esac; then
        pass "UNMEASURABLE, not a verdict: $label"
    else
        fail "$label exited $r (want 2 UNMEASURABLE). Landing in either verdict is the defect this cell exists for: $o"
    fi
}

check_unmeasurable "an absent turnlog directory" \
    run_probe --turnlog-dir "$SANDBOX/absent" --stale-after 2h

mkdir -p "$SANDBOX/empty"
check_unmeasurable "a turnlog directory with no *.log in it (an EMPTY population is not a stopped one)" \
    run_probe --turnlog-dir "$SANDBOX/empty" --stale-after 2h

# A row that cannot be stat'd, two ways, both real.
#
# (a) A DANGLING symlink — a turnlog whose target was rotated away. Note that
#     this one is NOT caught by stat: on darwin `stat -L` on a dangling link does
#     not fail, it falls back to the LINK's own mtime, so a link made a moment
#     ago would read as a completed turn that never happened. The probe's `[ -e ]`
#     check is what catches it, and this case is what would go green without it.
mkdir -p "$SANDBOX/broken"
ln -sf "$SANDBOX/does-not-exist" "$SANDBOX/broken/mayor.log"
check_unmeasurable "a population whose only row is a DANGLING symlink (which stat -L reports as fresh)" \
    run_probe --turnlog-dir "$SANDBOX/broken" --stale-after 2h

# (b) A row whose target cannot be reached — stat really does fail here.
mkdir -p "$SANDBOX/locked" "$SANDBOX/denied"
echo "unreachable" > "$SANDBOX/locked/x.log"
chmod 000 "$SANDBOX/locked"
ln -sf "$SANDBOX/locked/x.log" "$SANDBOX/denied/mayor.log"
check_unmeasurable "a population whose only row cannot be stat'd at all" \
    run_probe --turnlog-dir "$SANDBOX/denied" --stale-after 2h

# THE ONE THAT MATTERS: a future mtime makes the age NEGATIVE, which is younger
# than every threshold. A two-cell instrument reports OK and goes quiet.
mkfleet "$FLEET" mayor:-7200
check_unmeasurable "a FUTURE mtime — the one way this predicate could read GREEN over a broken measurement" \
    run_probe --turnlog-dir "$FLEET" --stale-after 2h

# A row that CAN be read alongside one that cannot: the fleet question is still
# answerable, and the unreadable row must be named rather than silently dropped.
mkfleet "$FLEET" mayor:600
ln -sf "$SANDBOX/locked/x.log" "$FLEET/ghost.log"
out="$(run_probe --turnlog-dir "$FLEET" --stale-after 2h 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && case "$out" in *"UNREADABLE"*ghost.log*) true ;; *) false ;; esac; then
    pass "a partly-unreadable population still answers the fleet question AND names the row it could not cover"
else
    fail "a population with one unreadable row exited $rc and did not name it — a glob that quietly skips the file it could not stat is a smaller version of this ticket: $out"
fi
rm -f "$FLEET/ghost.log"
chmod 755 "$SANDBOX/locked"      # or the sandbox teardown cannot remove it

# ---------------------------------------------------------------------------
# 7. POGO_HOME=$HOME — the LEGACY value that is live on this box
# ---------------------------------------------------------------------------
# An old shell integration exported POGO_HOME=$HOME meaning "where the dotfiles
# live", and it survives in existing zshrc copies and launchd plists.
# internal/config.PogoHome normalizes it to $HOME/.pogo. A probe that did not
# would look in $HOME/agents/turnlog, find nothing, and report UNMEASURABLE
# forever on the one box it was written for.

FAKEHOME="$SANDBOX/fakehome"
mkfleet "$FAKEHOME/.pogo/agents/turnlog" mayor:600
out="$(HOME="$FAKEHOME" POGO_HOME="$FAKEHOME" "$PROBE" --now "$NOW" --stamp "$SANDBOX/s2" --no-self-test --stale-after 2h 2>&1)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "POGO_HOME=\$HOME is normalized to \$HOME/.pogo, as internal/config.PogoHome does"
else
    fail "with the legacy POGO_HOME=\$HOME the probe exited $rc — it is looking in \$HOME/agents/turnlog, which exists on no box: $out"
fi

# ---------------------------------------------------------------------------
# 8. THE CAPABILITY PROBE MUST NOT DIE ON SIGPIPE (mg-7ce7)
# ---------------------------------------------------------------------------
# `set -o pipefail` + `CMD | grep -q PATTERN`: grep exits on the first match, the
# producer takes SIGPIPE and exits 141, pipefail makes 141 the pipeline's status,
# and A CAPABILITY PROBE THEN REPORTS A WORKING TOOL AS ABSENT. That is what
# silenced revision-probe.sh's --mail path mid-incident, three delivered alerts
# in and with no code change.
#
# The stub below is CHATTY on purpose: it prints the match first and then far
# more than a pipe buffer holds, so grep -q's early exit really does kill it.
# The first assertion checks THE FIXTURE REPRODUCES THE DEFECT. Without it this
# section would pass against a stub that never SIGPIPEd and would be certifying
# timing luck — which is how revision-probe's own suite stayed green over the
# idiom for months: its stub mg echoes one short line, so it never lost the race
# the real binary loses every time.

MAILBIN="$SANDBOX/mailbin"
MAILDIR="$SANDBOX/fakemail"
mkdir -p "$MAILBIN" "$MAILDIR"

# make_stub_mg BIN [chatty|quiet] [send-rc] [deliver|swallow]
make_stub_mg() {
    local bin="$1" chatty="${2:-quiet}" sendrc="${3:-0}" deliver="${4:-deliver}"
    mkdir -p "$(dirname "$bin")"
    cat > "$bin" <<EOF
#!/usr/bin/env bash
# A stub macguffin. Its maildir is a directory of NDJSON files, one per box.
MAILDIR="$MAILDIR"
CHATTY="$chatty"
SENDRC="$sendrc"
DELIVER="$deliver"
EOF
    cat >> "$bin" <<'EOF'
case "$1" in
    --help|-h)
        echo "macguffin — the mg CLI (stub)"
        if [ "$CHATTY" = chatty ]; then
            # More than any pipe buffer. A consumer that exits early kills this.
            for i in $(seq 1 20000); do
                echo "filler line $i ------------------------------------------------"
            done
        fi
        exit 0
        ;;
esac
sub="${1:-}"; shift
[ "$sub" = mail ] || exit 64
act="${1:-}"; shift
box=""
case "$act" in
    list|send|register) box="${1:-}"; shift ;;
    archive) box="${1%%/*}"; mid="${1#*/}"; shift ;;
esac
f="$MAILDIR/$box.ndjson"
case "$act" in
    register) : > "$f"; exit 0 ;;
    list)
        if [ -s "$f" ]; then cat "$f"; exit 0; fi
        if [ -f "$f" ]; then
            echo "{\"mailbox\":\"$box\",\"unread\":0,\"exists\":true}" >&2
        else
            echo "{\"mailbox\":\"$box\",\"unread\":0,\"exists\":false}" >&2
        fi
        exit 0
        ;;
    send)
        subject=""
        for a in "$@"; do case "$a" in --subject=*) subject="${a#--subject=}" ;; esac; done
        [ "$SENDRC" = 0 ] || { echo "stub refused" >&2; exit "$SENDRC"; }
        if [ "$DELIVER" = deliver ]; then
            printf '{"id":"%s.%s","from":"fleet-liveness-probe","subject":"%s","read":false}\n' \
                "$(date +%s)" "$RANDOM$RANDOM" "$subject" >> "$f"
        fi
        exit 0
        ;;
    archive)
        [ -f "$f" ] && grep -v "\"id\":\"$mid\"" "$f" > "$f.tmp" 2>/dev/null && mv "$f.tmp" "$f"
        exit 0
        ;;
esac
exit 65
EOF
    chmod +x "$bin"
}

make_stub_mg "$MAILBIN/mg" chatty 0 deliver

# 8a. THE FIXTURE MUST REPRODUCE THE DEFECT, or this section guards nothing.
old_idiom_rc=0
( set -uo pipefail; "$MAILBIN/mg" --help 2>/dev/null | grep -q macguffin ) || old_idiom_rc=$?
if [ "$old_idiom_rc" -eq 141 ]; then
    pass "the fixture reproduces mg-7ce7: the OLD idiom (pipefail + grep -q) rejects a working mg with SIGPIPE 141"
else
    fail "the chatty stub did not SIGPIPE the old idiom (exit $old_idiom_rc, want 141) — this section would then be certifying timing luck as correctness, which is exactly how revision-probe's own suite stayed green over the idiom: its stub mg echoes one short line and never loses the race the real binary loses every time"
fi

# 8b. And this probe must accept it.
rm -f "$MAILDIR"/*.ndjson
: > "$MAILDIR/human.ndjson"
out="$(PATH="$MAILBIN:$PATH" "$PROBE" --now "$NOW" --stamp "$SANDBOX/s3" --log "$LEDGER" \
        --self-test-only --self-test-stamp "$SANDBOX/s3.selftest" \
        --self-test-to selfbox 2>&1)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "the probe FINDS the same mg the old idiom rejected — no pipe, so nothing can die in one"
else
    fail "the probe rejected a working mg (exit $rc). If this says no-mg, the pipefail+grep -q idiom is back: $out"
fi

# ---------------------------------------------------------------------------
# 9. THE DELIVERY POSITIVE CONTROL, AND ITS THREE FAILURE MODES
# ---------------------------------------------------------------------------
# "An alert path that has never been observed succeeding is not known to work."
# A control that only ever passes is a presence check, so each failure mode is
# staged and required to FAIL.

run_selftest_only() {
    local bindir="$1"; shift
    PATH="$bindir:$PATH" "$PROBE" --now "$NOW" --stamp "$SANDBOX/s4" \
        --self-test-only --self-test-stamp "$SANDBOX/s4.selftest" \
        --self-test-to selfbox "$@" 2>&1
}

# 9a. send exits nonzero.
FAILBIN="$SANDBOX/failbin"; make_stub_mg "$FAILBIN/mg" quiet 3 deliver
rm -f "$MAILDIR"/*.ndjson; : > "$MAILDIR/human.ndjson"
out="$(run_selftest_only "$FAILBIN")"; rc=$?
if [ "$rc" -eq 3 ] && case "$out" in *"send-rc-3"*) true ;; *) false ;; esac; then
    pass "the control FAILS when the send exits nonzero, and says so"
else
    fail "a refusing mg gave exit $rc: $out"
fi

# 9b. THE ONE THAT MATTERS: send exits 0 and nothing arrives. `mg mail send`
# exiting 0 is an ATTEMPT, and a record that cannot tell an attempt from a
# delivery cannot answer the question this ticket turned on.
SWALLOWBIN="$SANDBOX/swallowbin"; make_stub_mg "$SWALLOWBIN/mg" quiet 0 swallow
rm -f "$MAILDIR"/*.ndjson; : > "$MAILDIR/human.ndjson"
out="$(run_selftest_only "$SWALLOWBIN")"; rc=$?
if [ "$rc" -eq 3 ] && case "$out" in *"sent-not-arrived"*) true ;; *) false ;; esac; then
    pass "the control FAILS when the send exits 0 and the message never arrives — ATTEMPTED is not DELIVERED"
else
    fail "an mg that exits 0 and delivers nothing was scored as a working path (exit $rc): $out"
fi

# 9c. The real recipient is not addressable. `mg mail send` refuses a recipient
# it has never seen, and a refusal at alert time is a lost alert.
rm -f "$MAILDIR"/*.ndjson       # no human.ndjson: the box does not exist
out="$(run_selftest_only "$MAILBIN")"; rc=$?
if [ "$rc" -eq 3 ] && case "$out" in *"recipient-human-unknown"*) true ;; *) false ;; esac; then
    pass "the control FAILS when the ALERT recipient is not addressable, even though the send path itself works"
else
    fail "an unaddressable 'human' was scored as a working alert path (exit $rc): $out"
fi

# 9d. And it PASSES against a path that works, or it is a control that only says no.
rm -f "$MAILDIR"/*.ndjson; : > "$MAILDIR/human.ndjson"
out="$(run_selftest_only "$MAILBIN")"; rc=$?
if [ "$rc" -eq 0 ] && case "$out" in *"PASSED"*) true ;; *) false ;; esac; then
    pass "the control PASSES against a working path — a control that reddens everything is not a control"
else
    fail "the control failed against a working stub (exit $rc): $out"
fi

# 9e. It never registers the ALERT recipient. Talking past mg's refusal is how a
# typo'd name becomes a dead drop that reports "Delivered"; the ONLY box this
# probe may create is its own.
if [ ! -f "$MAILDIR/human.ndjson" ] || [ -s "$MAILDIR/human.ndjson" ]; then
    : # human.ndjson was pre-created by the case above; the check below is the real one
fi
rm -f "$MAILDIR"/*.ndjson
out="$(run_selftest_only "$MAILBIN")" || true
if [ ! -f "$MAILDIR/human.ndjson" ] && [ -f "$MAILDIR/selfbox.ndjson" ]; then
    pass "it registers ONLY its own self-test box, never the alert recipient"
else
    fail "the probe created a mailbox for the alert recipient — talking past mg's no_such_mailbox refusal re-creates the phantom-mailbox class the refusal exists to remove"
fi

# ---------------------------------------------------------------------------
# 10. THE LEDGER RECORDS THE SEND RESULT, NOT THE SEND ATTEMPT
# ---------------------------------------------------------------------------
# "The record has to distinguish computed / attempted / delivered, or the next
# reader draws my conclusion again." — mg-f867. It did: refusal lines in a report
# log were counted as computed alerts and read as "this path never worked", and
# the mail record then showed three real deliveries. The ledger has to make that
# mistake impossible to repeat.

mkfleet "$FLEET" mayor:500000
alert_run() {
    local bindir="$1"; shift
    PATH="$bindir:$PATH" "$PROBE" --now "$NOW" --turnlog-dir "$FLEET" --stale-after 2h \
        --stamp "$SANDBOX/s5" --log "$LEDGER" --no-self-test --quiet "$@" >/dev/null 2>&1
}

rm -f "$LEDGER" "$SANDBOX/s5"; rm -f "$MAILDIR"/*.ndjson; : > "$MAILDIR/human.ndjson"
alert_run "$MAILBIN"                       # no --mail at all
if case "$(ledger_last)" in *"mail=computed"*) true ;; *) false ;; esac; then
    pass "an alert with no --mail is recorded as COMPUTED"
else
    fail "want mail=computed, got: $(ledger_last)"
fi

rm -f "$LEDGER" "$SANDBOX/s5"
alert_run "$SWALLOWBIN" --mail             # exits 0, delivers nothing
if case "$(ledger_last)" in *"mail=attempted-unconfirmed"*) true ;; *) false ;; esac; then
    pass "a send that exits 0 and does not arrive is recorded as ATTEMPTED, never DELIVERED"
else
    fail "want mail=attempted-unconfirmed, got: $(ledger_last)"
fi
# ...and it must NOT have started the throttle, or the next run goes quiet about
# an alert that reached nobody.
if [ ! -f "$SANDBOX/s5" ] || ! grep -qE ' [0-9]+$' "$SANDBOX/s5"; then
    pass "an unconfirmed send does NOT start the re-notify throttle — the next run tries again"
else
    fail "the stamp recorded a notification for a send that never arrived: $(cat "$SANDBOX/s5")"
fi

rm -f "$LEDGER" "$SANDBOX/s5"; rm -f "$MAILDIR"/*.ndjson; : > "$MAILDIR/human.ndjson"
alert_run "$MAILBIN" --mail                # arrives
if case "$(ledger_last)" in *"mail=delivered"*) true ;; *) false ;; esac; then
    pass "a send that is READ BACK OUT of the recipient's mailbox is recorded as DELIVERED"
else
    fail "want mail=delivered, got: $(ledger_last)"
fi

# 10d. The throttle holds for the same unresolved stop, and only then.
rm -f "$LEDGER"
alert_run "$MAILBIN" --mail --renotify 12h
if case "$(ledger_last)" in *"mail=throttled"*) true ;; *) false ;; esac; then
    pass "the same unresolved stop is not re-mailed inside the re-notify window"
else
    fail "want mail=throttled on the second run, got: $(ledger_last)"
fi
n_before="$(grep -c . "$MAILDIR/human.ndjson")"
rm -f "$LEDGER"
set_mtime "$FLEET/mayor.log" $(( NOW - 400000 ))   # a NEW stop: different mtime
alert_run "$MAILBIN" --mail --renotify 12h
n_after="$(grep -c . "$MAILDIR/human.ndjson")"
if [ "$n_after" -gt "$n_before" ]; then
    pass "a DIFFERENT stop mails again — the throttle is keyed on the situation, not on the clock alone"
else
    fail "the throttle outlived the situation it was throttling ($n_before -> $n_after mails)"
fi

# 10e. No mg at all: the alert stands, and the ledger says nothing reached anyone.
rm -f "$LEDGER" "$SANDBOX/s5"
# A minimal PATH that still has date/mktemp/stat but no macguffin. /usr/bin/mg
# may well be ON it — that is the Micro-Emacs editor, and being rejected because
# it does not self-identify as macguffin is the behaviour under test.
PATH="/usr/bin:/bin" GOBIN="" GOPATH="" HOME="$SANDBOX/nohome" "$PROBE" \
    --now "$NOW" --turnlog-dir "$FLEET" --stale-after 2h --stamp "$SANDBOX/s5" \
    --log "$LEDGER" --no-self-test --quiet --mail >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 1 ] && case "$(ledger_last)" in *"mail=no-mg"*) true ;; *) false ;; esac; then
    pass "with no mg on the box the alert still exits 1 and the ledger records that nothing was sent"
else
    fail "no-mg run exited $rc with ledger: $(ledger_last)"
fi

# ---------------------------------------------------------------------------
# 11. --help still reaches the end of EXIT STATUS
# ---------------------------------------------------------------------------
# The header comment IS the help text, addressed by a hard-coded line range,
# which truncates silently the moment the header grows.

out="$("$PROBE" --help 2>&1)"
if case "$out" in *"the mg-7ce7 state exactly"*) true ;; *) false ;; esac; then
    pass "--help still reaches the last line of EXIT STATUS"
else
    fail "--help is truncated — the line range in usage() has drifted from the header"
fi

# ---------------------------------------------------------------------------

echo
echo "=== scripts/fleet-liveness-probe.sh controls ==="
grep -c '^PASS' "$RESULTS_FILE" | sed 's/^/  passed: /'
if grep -q '^FAIL' "$RESULTS_FILE"; then
    grep '^FAIL' "$RESULTS_FILE"
    exit 1
fi
echo "  all controls green"
