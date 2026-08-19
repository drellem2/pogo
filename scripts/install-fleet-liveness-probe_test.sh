#!/usr/bin/env bash
# Controls on scripts/install-fleet-liveness-probe.sh — the ARMING of the fleet
# liveness witness (mg-f867).
#
# WHY THE ARMING NEEDS ITS OWN CONTROLS. mg-ce10 landed an external witness and
# wired it to nothing: 501 lines, referenced by a changelog fragment, a docs
# section and test.sh, and by no schedule, no plist and no runner. A detector
# armed by NOTHING is present by existence and absent by effect, which is the
# limiting case of this ticket's own rule.
#
# THE TWO LOAD-BEARING CASES:
#
# SECTION 4 — THE ARGUMENT VECTOR IS EXECUTED, not parsed and hoped over. A plist
# can parse, install and appear in `launchctl list` while naming a command line
# nobody has ever run. install-revision-probe.sh's first live install did exactly
# that: loaded, listed, correct-looking in `launchctl print`, and writing `unknown
# option '--log'` once an hour forever, because the plist and the probe are two
# tracked files that reach a box at DIFFERENT times. Section 4 reads
# ProgramArguments back out of the rendered plist and runs that exact vector
# against a probe from a stale checkout.
#
# SECTION 5 — THE DELIVERY POSITIVE CONTROL GATES THE INSTALL. revision-probe's
# --mail path delivered three correct alerts and then went silent mid-incident
# with no code change, because its capability probe is a race (mg-7ce7). Arming a
# witness that can see and cannot speak is this ticket's defect reproduced by its
# own remedy, so a failing control must REFUSE and leave nothing behind.
#
# Nothing here touches the developer's real launchd domain or real mail: the
# launchctl is a recording stub and the probe's mail path runs against a stub mg.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
pogo_sandbox_create fleetinstall
SANDBOX="$POGO_SANDBOX_DIR"

cleanup() {
    pogo_sandbox_down
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

pogo_sandbox_isolate

INSTALLER="$HERE/install-fleet-liveness-probe.sh"
[ -x "$INSTALLER" ] || pogo_sandbox_fail "scripts/install-fleet-liveness-probe.sh is not executable"

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------
# SRC is a checkout-shaped directory holding the REAL probe, because section 4
# runs it. Copied rather than symlinked: the installer's precondition checks ask
# about a file at a path, and a symlink would let a wrong path pass.

SRC="$SANDBOX/src"
mkdir -p "$SRC/scripts/launchd"
cp "$HERE/fleet-liveness-probe.sh" "$SRC/scripts/fleet-liveness-probe.sh"
chmod +x "$SRC/scripts/fleet-liveness-probe.sh"

LA_DIR="$SANDBOX/LaunchAgents"
PLIST="$LA_DIR/com.pogo.fleetliveness.plist"
LC_LOG="$SANDBOX/launchctl.calls"

make_launchctl() {
    local path="$1" print_rc="$2"
    cat > "$path" <<EOF
#!/usr/bin/env bash
echo "\$*" >> "$LC_LOG"
case "\$1" in
    print) exit $print_rc ;;
    *) exit 0 ;;
esac
EOF
    chmod +x "$path"
}
LC_OK="$SANDBOX/launchctl-ok"
LC_NOPRINT="$SANDBOX/launchctl-noprint"
make_launchctl "$LC_OK" 0
make_launchctl "$LC_NOPRINT" 1

# A stub macguffin, so the installer's delivery control runs against a fake
# maildir. There is no real `mg` on the PATH of any case below.
MAILDIR="$SANDBOX/fakemail"
mkdir -p "$MAILDIR"
make_stub_mg() {
    local bin="$1" sendrc="${2:-0}"
    mkdir -p "$(dirname "$bin")"
    cat > "$bin" <<EOF
#!/usr/bin/env bash
MAILDIR="$MAILDIR"
SENDRC="$sendrc"
EOF
    cat >> "$bin" <<'EOF'
case "$1" in --help|-h) echo "macguffin — the mg CLI (stub)"; exit 0 ;; esac
sub="${1:-}"; shift; [ "$sub" = mail ] || exit 64
act="${1:-}"; shift
case "$act" in
    list|send|register) box="${1:-}"; shift ;;
    archive) box="${1%%/*}"; mid="${1#*/}"; shift ;;
esac
f="$MAILDIR/$box.ndjson"
case "$act" in
    register) : > "$f"; exit 0 ;;
    list)
        if [ -s "$f" ]; then cat "$f"; exit 0; fi
        if [ -f "$f" ]; then echo "{\"mailbox\":\"$box\",\"exists\":true}" >&2
        else echo "{\"mailbox\":\"$box\",\"exists\":false}" >&2; fi
        exit 0 ;;
    send)
        [ "$SENDRC" = 0 ] || exit "$SENDRC"
        printf '{"id":"%s.%s","from":"fleet-liveness-probe","subject":"x"}\n' \
            "$(date +%s)" "$RANDOM$RANDOM" >> "$f"
        exit 0 ;;
    archive)
        [ -f "$f" ] && grep -v "\"id\":\"$mid\"" "$f" > "$f.tmp" 2>/dev/null && mv "$f.tmp" "$f"
        exit 0 ;;
esac
exit 65
EOF
    chmod +x "$bin"
}
GOODBIN="$SANDBOX/goodbin"; make_stub_mg "$GOODBIN/mg" 0
BADBIN="$SANDBOX/badbin";  make_stub_mg "$BADBIN/mg" 3

reset_mail() { rm -f "$MAILDIR"/*.ndjson; : > "$MAILDIR/human.ndjson"; }

run_install() {
    local bindir="$1"; shift
    rm -f "$LC_LOG"
    PATH="$bindir:$PATH" "$INSTALLER" --src "$SRC" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" "$@" 2>&1
}

# ---------------------------------------------------------------------------
# 1. --dry-run renders and touches nothing
# ---------------------------------------------------------------------------

rm -rf "$LA_DIR"; reset_mail
out="$(run_install "$GOODBIN" --dry-run)"; rc=$?
if [ "$rc" -eq 0 ] && [ ! -e "$PLIST" ] && [ ! -s "$LC_LOG" ]; then
    pass "--dry-run renders without writing a plist or calling launchctl"
else
    fail "--dry-run exited $rc, plist exists: $([ -e "$PLIST" ] && echo yes || echo no), launchctl calls: $(cat "$LC_LOG" 2>/dev/null)"
fi
# Two conditions, and the second is the one that matters: a placeholder left in
# a <string> VALUE is a job that installs, lists, and cannot run. A placeholder
# in a COMMENT is documentation for a hand-installer and is meant to survive.
if case "$out" in *"$SRC/scripts/fleet-liveness-probe.sh"*) true ;; *) false ;; esac \
    && [ -z "$(printf '%s\n' "$out" | grep '<string>' | grep 'YOUR_USERNAME')" ]; then
    pass "the render substitutes the checkout path and leaves no placeholder in a value"
else
    fail "the rendered plist is wrong:
$out"
fi

# ---------------------------------------------------------------------------
# 2. A missing probe is REFUSED, and nothing is written
# ---------------------------------------------------------------------------
# Installing a job that points at an absent script produces a LaunchAgent that
# fails every fifteen minutes and reports nothing.

rm -rf "$LA_DIR"; reset_mail
EMPTY_SRC="$SANDBOX/emptysrc"; mkdir -p "$EMPTY_SRC/scripts"
out="$(PATH="$GOODBIN:$PATH" "$INSTALLER" --src "$EMPTY_SRC" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [ ! -e "$PLIST" ]; then
    pass "a src with no probe in it is REFUSED and leaves the box exactly as it was"
else
    fail "a src with no probe exited $rc and left plist=$([ -e "$PLIST" ] && echo yes || echo no): $out"
fi

# ---------------------------------------------------------------------------
# 3. Install, then verify BY ASKING LAUNCHD
# ---------------------------------------------------------------------------
# `bootstrap` returning 0 is a claim about parsing, not about the job existing in
# the domain. This whole lineage exists because a component reported success for
# a thing that had not happened.

rm -rf "$LA_DIR"; reset_mail
out="$(run_install "$GOODBIN")"; rc=$?
if [ "$rc" -eq 0 ] && [ -f "$PLIST" ]; then
    pass "a good install writes the plist and exits 0"
else
    fail "install exited $rc: $out"
fi
if grep -q '^bootout ' "$LC_LOG" && grep -q '^bootstrap ' "$LC_LOG" && grep -q '^print ' "$LC_LOG"; then
    pass "it boots out first, bootstraps, and then ASKS launchd whether the job is there"
else
    fail "launchctl was asked for: $(cat "$LC_LOG")"
fi
if command -v plutil >/dev/null 2>&1 && plutil -lint "$PLIST" >/dev/null 2>&1; then
    pass "the installed plist parses"
elif ! command -v plutil >/dev/null 2>&1; then
    pass "the installed plist parses (skipped: no plutil on this host)"
else
    fail "the installed plist does not parse"
fi

rm -rf "$LA_DIR"; reset_mail
out="$(PATH="$GOODBIN:$PATH" "$INSTALLER" --src "$SRC" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_NOPRINT" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *"does not know the job"*) true ;; *) false ;; esac; then
    pass "a bootstrap that exits 0 while launchd does not know the job is a FAILURE, not an install"
else
    fail "a launchd that cannot print the job gave exit $rc: $out"
fi

# ---------------------------------------------------------------------------
# 4. THE ARGUMENT VECTOR IS EXECUTED against the probe at $SRC
# ---------------------------------------------------------------------------
# Staged as an ARRIVAL-ORDER problem, which is what it really is: the installer
# runs from one checkout and the job points at another that the deploy runner
# advances. A stale probe there does not know the flags this plist names.

rm -rf "$LA_DIR"; reset_mail
STALE_SRC="$SANDBOX/stalesrc"; mkdir -p "$STALE_SRC/scripts"
cat > "$STALE_SRC/scripts/fleet-liveness-probe.sh" <<'OLD'
#!/usr/bin/env bash
# An older probe that has never heard of --stamp.
while [ $# -gt 0 ]; do
    case "$1" in
        --turnlog-dir|--stale-after|--log) shift 2 ;;
        --quiet|--mail) shift ;;
        -h|--help) echo "old probe"; exit 0 ;;
        *) echo "unknown option '$1'" >&2; exit 2 ;;
    esac
done
exit 0
OLD
chmod +x "$STALE_SRC/scripts/fleet-liveness-probe.sh"
out="$(PATH="$GOODBIN:$PATH" "$INSTALLER" --src "$STALE_SRC" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *"cannot parse the command line"*) true ;; *) false ;; esac \
    && [ ! -e "$PLIST" ]; then
    pass "a probe that cannot parse this job's vector REFUSES the install — no LaunchAgent that writes 'unknown option' forever"
else
    fail "a stale probe exited $rc with plist=$([ -e "$PLIST" ] && echo yes || echo no): $out"
fi

# ---------------------------------------------------------------------------
# 5. THE DELIVERY POSITIVE CONTROL GATES THE INSTALL
# ---------------------------------------------------------------------------

rm -rf "$LA_DIR"; reset_mail
out="$(run_install "$BADBIN")"; rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *"delivery positive control FAILED"*) true ;; *) false ;; esac \
    && [ ! -e "$PLIST" ]; then
    pass "a delivery path that cannot send REFUSES the install and arms nothing"
else
    fail "a broken delivery path gave exit $rc with plist=$([ -e "$PLIST" ] && echo yes || echo no): $out"
fi

# The recipient not being addressable is the same refusal for a different reason:
# `mg mail send human` would be refused at alert time, which is a lost alert.
rm -rf "$LA_DIR"; rm -f "$MAILDIR"/*.ndjson
out="$(run_install "$GOODBIN")"; rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *"recipient-human-unknown"*) true ;; *) false ;; esac; then
    pass "an unaddressable alert recipient REFUSES the install, even though the send path itself works"
else
    fail "an unaddressable recipient gave exit $rc: $out"
fi

# And it can be overridden knowingly, saying so in the output rather than quietly.
rm -rf "$LA_DIR"
out="$(run_install "$BADBIN" --skip-delivery-check)"; rc=$?
if [ "$rc" -eq 0 ] && case "$out" in *"has NOT been observed working"*) true ;; *) false ;; esac; then
    pass "--skip-delivery-check arms anyway AND states in the output that the alarm is unproven"
else
    fail "--skip-delivery-check exited $rc: $out"
fi

# 5d. The control must not be able to send a FLEET STOP as a side effect of
# arming. It runs --self-test-only, which never measures the fleet.
rm -rf "$LA_DIR"; reset_mail
n_before="$(grep -c . "$MAILDIR/human.ndjson" 2>/dev/null || echo 0)"
run_install "$GOODBIN" >/dev/null 2>&1
n_after="$(grep -c . "$MAILDIR/human.ndjson" 2>/dev/null || echo 0)"
if [ "$n_after" = "$n_before" ]; then
    pass "arming the job sends NOTHING to the alert recipient — the control is self-test-only"
else
    fail "installing mailed the alert recipient ($n_before -> $n_after) — arming a witness must not be an event"
fi

# ---------------------------------------------------------------------------
# 5e. THE INSTALLER'S OWN PLACEHOLDER GUARD, WITH THE LOSING SIDE FORCED
# ---------------------------------------------------------------------------
# A remedy is an artifact of the same kind as the defect, so it is subject to
# that defect. The obvious spelling of the installer's placeholder scan is
#
#     printf '%s' "$RENDERED" | grep '<string>' | grep -q 'YOUR_USERNAME'
#
# which is the mg-7ce7 shape verbatim, on the install path of the job that exists
# because of mg-7ce7. Measured: against the real template it WINS — the middle
# grep emits ~30 lines, well under a pipe buffer — which is what "latent" means,
# and is why git and curl pass in revision-probe while mg loses every time.
#
# So this case forces the losing side the same way section 8 of the probe suite
# does: a template padded past any pipe buffer, with an unsubstitutable
# placeholder in a <string> VALUE near the top. The guard must still refuse. The
# installer runs from a COPY here, because it reads its template relative to its
# own directory.

FAKE_INST_DIR="$SANDBOX/fakeinst"
mkdir -p "$FAKE_INST_DIR/launchd"
cp "$INSTALLER" "$FAKE_INST_DIR/install-fleet-liveness-probe.sh"
chmod +x "$FAKE_INST_DIR/install-fleet-liveness-probe.sh"

# The placeholder is NOT under /Users/, so no substitution rule can reach it —
# an unsubstituted /Users/YOUR_USERNAME_X would be rewritten by the $HOME rule
# and would test nothing.
{
    sed 's#<string>/Users/YOUR_USERNAME/go/bin:#<string>/Elsewhere/YOUR_USERNAME/go/bin:#' \
        "$HERE/launchd/com.pogo.fleetliveness.plist" | sed '$d'
    echo "    <key>Padding</key>"
    echo "    <array>"
    # ~200KB of <string> lines: more than any pipe buffer, so a `grep -q`
    # consumer closing early really does SIGPIPE the producer.
    for i in $(seq 1 3000); do
        echo "        <string>padding $i ------------------------------------------------</string>"
    done
    echo "    </array>"
    echo "</dict>"
    echo "</plist>"
} > "$FAKE_INST_DIR/launchd/com.pogo.fleetliveness.plist"

# First: prove the fixture really would lose the race, or this case guards
# nothing. The producer is the middle grep over a 200KB render.
pad_render="$(cat "$FAKE_INST_DIR/launchd/com.pogo.fleetliveness.plist")"
old_idiom_rc=0
( set -uo pipefail; printf '%s\n' "$pad_render" | grep '<string>' | grep -q 'YOUR_USERNAME' ) || old_idiom_rc=$?
if [ "$old_idiom_rc" -eq 141 ]; then
    pass "the padded template reproduces the race on the INSTALLER's own guard: the old idiom returns 141, i.e. 'no placeholder found'"
else
    fail "the padded template did not SIGPIPE the old idiom (exit $old_idiom_rc, want 141) — this case would then be guarding nothing"
fi

rm -rf "$LA_DIR"; reset_mail
out="$(PATH="$GOODBIN:$PATH" "$FAKE_INST_DIR/install-fleet-liveness-probe.sh" --src "$SRC" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" --dry-run 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *"still has YOUR_USERNAME in a value"*) true ;; *) false ;; esac; then
    pass "the installer REFUSES a surviving placeholder even at a size that defeats the pipefail spelling"
else
    fail "a 200KB render with a live placeholder was accepted (exit $rc) — the guard is the mg-7ce7 shape and lost its own race: $(printf '%s' "$out" | head -3)"
fi

# ---------------------------------------------------------------------------
# 6. --uninstall boots out and removes, and KEEPS the evidence
# ---------------------------------------------------------------------------

rm -rf "$LA_DIR"; reset_mail
run_install "$GOODBIN" >/dev/null 2>&1     # something to uninstall
rm -f "$LC_LOG"
out="$("$INSTALLER" --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" --uninstall 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && [ ! -e "$PLIST" ] && grep -q '^bootout ' "$LC_LOG"; then
    pass "--uninstall boots the job out and removes the plist"
else
    fail "--uninstall exited $rc, plist=$([ -e "$PLIST" ] && echo yes || echo no), calls: $(cat "$LC_LOG" 2>/dev/null)"
fi
if case "$out" in *"LEFT IN PLACE"*) true ;; *) false ;; esac; then
    pass "it says the ledger and stamp are kept — deleting them would erase the evidence along with the instrument"
else
    fail "--uninstall says nothing about the ledger and stamp: $out"
fi

# ---------------------------------------------------------------------------
# 7. The tracked template is the ONE source
# ---------------------------------------------------------------------------
# The house pattern elsewhere keeps a Go string that mirrors the in-repo plist,
# and mg-b201 is what that costs: the shipped plist and the installed one
# drifted, and it took a dedicated test to notice.

if ! grep -q '<key>Label</key>' "$INSTALLER"; then
    pass "the installer holds no copy of the plist — it renders the tracked template"
else
    fail "the installer contains plist markup of its own, which is the drift class mg-b201 measured"
fi

rm -rf "$LA_DIR"
out="$(POGO_LAUNCHAGENTS_DIR="$LA_DIR" "$INSTALLER" --src "$SRC" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" --dry-run 2>&1)"
if case "$out" in *"StartCalendarInterval"*) true ;; *) false ;; esac \
    && case "$out" in *"<integer>7</integer>"*) true ;; *) false ;; esac; then
    pass "the rendered job carries the schedule the tracked template declares"
else
    fail "the rendered job lost its schedule"
fi

# ---------------------------------------------------------------------------

echo
echo "=== scripts/install-fleet-liveness-probe.sh controls ==="
grep -c '^PASS' "$RESULTS_FILE" | sed 's/^/  passed: /'
if grep -q '^FAIL' "$RESULTS_FILE"; then
    grep '^FAIL' "$RESULTS_FILE"
    exit 1
fi
echo "  all controls green"
