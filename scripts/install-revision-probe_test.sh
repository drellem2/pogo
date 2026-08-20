#!/usr/bin/env bash
# Controls on scripts/install-revision-probe.sh — the ARMING of the external
# redeploy witness (mg-a03d).
#
# WHAT THIS FILE IS GUARDING AGAINST, and it is not a hypothetical: mg-ce10
# landed a 501-line probe referenced by a changelog fragment, a docs section and
# test.sh, and by no schedule, no plist and no caller. The rule the probe
# implements — a detector for "X did not happen" must not be ACTIVATED BY X —
# has a limiting case, which is a detector activated by NOTHING. The witness was
# present by existence and absent by effect, and every artifact around it read
# as done.
#
# So the load-bearing case here is SECTION 4, and it is deliberately not "the
# plist parses". A plist can parse, install, appear in `launchctl list` and name
# a command line that has never been run once. Section 4 extracts
# ProgramArguments from the plist this installer actually wrote, EXECUTES that
# exact argument vector, and asserts a ledger line came out of it — against a
# live stub daemon for the green arm and a dead port for the red one. A check
# that has only ever produced one of the two is a check of unknown polarity.
#
# Section 5 is the same independence argument the probe's own controls make, one
# level up: `pogo service install-recovery` and `pogo service install-deploy`
# are the house pattern for installing a LaunchAgent and both live in the `pogo`
# BINARY, which the redeploy installs. An arming step that needs a current
# `pogo` cannot be run on the box that needs arming.
#
# SECTION 2b turns that unknown-polarity complaint on the INSTALLER'S OWN RENDER
# GUARDS, which had only ever been shown to accept (mg-712e). It runs a copy of
# the installer beside a doctored template and asserts both refusals: a surviving
# placeholder at a render size that defeats the `| grep -q` spelling — measured
# 5/5 SIGPIPE 141, and that spelling ACCEPTED such a render at exit 0 — and a
# template that stops naming the stamp.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
pogo_sandbox_create revprobeinstall
SANDBOX="$POGO_SANDBOX_DIR"

STUB_PID=""
cleanup() {
    if [ -n "$STUB_PID" ]; then
        kill "$STUB_PID" 2>/dev/null
        wait "$STUB_PID" 2>/dev/null
    fi
    pogo_sandbox_down
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

pogo_sandbox_isolate

INSTALLER="$HERE/install-revision-probe.sh"
[ -x "$INSTALLER" ] || pogo_sandbox_fail "scripts/install-revision-probe.sh is not executable — the thing under test cannot be run"

command -v python3 >/dev/null 2>&1 \
    || pogo_sandbox_fail "python3 is absent — neither the stub daemon nor the plist reader can be stood up, and no assertion below would mean anything without them"

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------
# SRC is a checkout-shaped directory holding the REAL probe, because section 4
# runs it. Copying rather than symlinking: the installer's precondition checks
# ask about a file at a path, and a symlink would let a wrong path pass.

SRC="$SANDBOX/src"
mkdir -p "$SRC/scripts"
cp "$HERE/revision-probe.sh" "$SRC/scripts/revision-probe.sh"
chmod +x "$SRC/scripts/revision-probe.sh"

LA_DIR="$SANDBOX/LaunchAgents"
PLIST="$LA_DIR/com.pogo.revisionprobe.plist"
LC_LOG="$SANDBOX/launchctl.calls"

# A recording launchctl. Every control below asserts on what the installer ASKED
# launchd to do; none of them touch the developer's real launchd domain, which a
# suite that bootstraps a job named com.pogo.* absolutely would (mg-3412).
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

# The stub daemon, serving a revision the test controls. A real pogod could only
# ever report the one revision its own binary was built from.
VERSION_FILE="$SANDBOX/version.json"
PORT_FILE="$SANDBOX/stub.port"
cat > "$SANDBOX/stub.py" <<'PY'
import http.server, socketserver, sys
path = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != '/version':
            self.send_error(404); return
        try:
            body = open(path, 'rb').read()
        except OSError:
            self.send_error(503); return
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a):
        pass
srv = socketserver.TCPServer(('127.0.0.1', 0), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
PY

FAKEREV="1111111111111111111111111111111111111111"
printf '{"revision":"%s","go_version":"go1.25.0"}\n' "$FAKEREV" > "$VERSION_FILE"
python3 "$SANDBOX/stub.py" "$VERSION_FILE" > "$PORT_FILE" 2>/dev/null &
STUB_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    PORT="$(head -1 "$PORT_FILE" 2>/dev/null)"
    [ -z "$PORT" ] || break
    sleep 0.25
done
[ -n "${PORT:-}" ] || pogo_sandbox_fail "the stub daemon never reported a port"
URL="http://127.0.0.1:$PORT"

# A git checkout for the probe's reference read, so section 4 exercises the real
# two-read path rather than a probe that dies on its repo argument.
export GIT_AUTHOR_NAME=probe GIT_AUTHOR_EMAIL=probe@example.com
export GIT_COMMITTER_NAME=probe GIT_COMMITTER_EMAIL=probe@example.com
UPSTREAM="$SANDBOX/upstream.git"
git init --bare --quiet "$UPSTREAM" || pogo_sandbox_fail "could not create the bare upstream fixture"
git -C "$SRC" init --quiet 2>/dev/null || pogo_sandbox_fail "could not init the src fixture as a checkout"
git -C "$SRC" symbolic-ref HEAD refs/heads/main
git -C "$SRC" remote add origin "$UPSTREAM"
echo one > "$SRC/file"
git -C "$SRC" add -A && git -C "$SRC" commit --quiet -m one
git -C "$SRC" push --quiet -u origin main 2>/dev/null || pogo_sandbox_fail "could not push the src fixture"

install_probe() {
    bash "$INSTALLER" --src "$SRC" --url "$URL" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" "$@" 2>&1
}

# ---------------------------------------------------------------------------
# 1. It installs, and it says the job is armed only after asking launchd
# ---------------------------------------------------------------------------
rm -f "$LC_LOG"
out="$(install_probe)"; rc=$?
if [ "$rc" -eq 0 ] && [ -f "$PLIST" ]; then
    pass "the installer writes the plist and exits 0"
else
    fail "install exited $rc and left $([ -f "$PLIST" ] && echo "a plist" || echo "no plist") — output: $out"
fi
if grep -q "bootout gui/$(id -u)/com.pogo.revisionprobe" "$LC_LOG" \
    && grep -q "bootstrap gui/$(id -u) $PLIST" "$LC_LOG"; then
    pass "it boots the label out before bootstrapping it — a re-install over a loaded job otherwise no-ops with I/O error 5"
else
    fail "the installer did not bootout-then-bootstrap — calls were: $(cat "$LC_LOG" 2>&1)"
fi
if grep -q "print gui/$(id -u)/com.pogo.revisionprobe" "$LC_LOG"; then
    pass "it VERIFIES by asking launchd, rather than trusting bootstrap's exit code"
else
    fail "nothing verified the job exists in the domain — bootstrap exiting 0 is a claim about parsing, not about the job — calls: $(cat "$LC_LOG" 2>&1)"
fi

# The whole ticket is about a component reporting success for something that did
# not happen. The installer must not do it either.
rm -f "$LC_LOG" "$PLIST"
out="$(bash "$INSTALLER" --src "$SRC" --url "$URL" --launch-agents-dir "$LA_DIR" \
        --launchctl "$LC_NOPRINT" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && printf '%s' "$out" | grep -qi 'NOT armed'; then
    pass "a bootstrap launchd does not confirm is reported as NOT ARMED, not as an install"
else
    fail "the installer exited $rc when launchctl print did not know the job — it reported success for something that had not happened — output: $out"
fi

# ---------------------------------------------------------------------------
# 2. The rendered job is complete — no placeholder reaches disk
# ---------------------------------------------------------------------------
# A LaunchAgent installed with a literal YOUR_USERNAME is loaded, listed, and
# permanently broken: this ticket's own defect, shipped by its own fix.
install_probe >/dev/null 2>&1
# CAPTURED, not `| grep -q`. The producer here is an EXTERNAL grep and the
# consumer leaves on first match, which under this file's `pipefail` is the
# mg-7ce7 shape: 141, read as "no placeholder", from a plist that HAS one. It
# wins against today's 10KB plist and loses 5/5 at 257KB (measured, mg-712e), so
# the assertion would go quiet exactly as the job grew — and it would go quiet in
# the direction that reports health.
placeholder_hits="$(grep '<string>' "$PLIST" | grep 'YOUR_USERNAME')"
if [ -n "$placeholder_hits" ]; then
    fail "a YOUR_USERNAME placeholder survived into an installed plist value: $placeholder_hits"
else
    pass "no placeholder survives into an installed plist value"
fi
# plutil is LENIENT and the strict reader is section 4's plistlib load. Measured
# while writing this file: a template comment containing a double hyphen (the
# name of a flag) is invalid XML, expat rejects it, and `plutil -lint` says OK.
# So this assertion is a floor, not the guarantee — the guarantee is that
# something below actually reads the file back.
if command -v plutil >/dev/null 2>&1; then
    if plutil -lint "$PLIST" >/dev/null 2>&1; then
        pass "the installed plist parses"
    else
        fail "the installed plist does not parse: $(plutil -lint "$PLIST" 2>&1)"
    fi
fi
if grep -q "$SRC/scripts/revision-probe.sh" "$PLIST"; then
    pass "the job runs the TRACKED script in the checkout, not a copy taken at install time"
else
    fail "the plist does not point at $SRC/scripts/revision-probe.sh — $(grep -A2 ProgramArguments "$PLIST" | head -5)"
fi

# ---------------------------------------------------------------------------
# 2b. THE TWO RENDER GUARDS, WITH THE LOSING SIDE FORCED AND THE RED ARM RUN
# ---------------------------------------------------------------------------
# Everything above asks whether a GOOD render is accepted. Neither of the
# installer's two render guards had ever been shown to REFUSE anything, which is
# this file's own header complaint — a check that has only ever produced one of
# the two is a check of unknown polarity — applied to the guards rather than to
# the job.
#
# It is not academic. mg-712e is a gate run that failed here reporting "the
# rendered plist does not mention <a sandbox path>", which is the mention
# guard's refusal text, on a render that cannot have been wrong: the installer's
# renderer and its checker read the same $HOME and $SRC in the same process, so
# every path that loop can name is one the render just substituted in. The alarm
# was reachable in a state it cannot detect.
#
# THAT RUN WAS NOT REPRODUCED — 22/22 standalone, 22/22 inside a green
# full-suite gate, 0 failures in 1,150 runs of the failing section's shape — and
# the SIGPIPE route to that particular message is separately measured DEAD: its
# producer was bash's BUILTIN printf, which lost 0 of 1,200 races at payloads up
# to 256KB with the match at byte 0. So what the conversion closes is the only
# route by which that sentence could be FALSE; it is not a diagnosis of that run.
# Both guards are now pure of a pipeline's exit status, and both are asserted
# below in the direction they exist for, which is the part nothing had ever run.
#
# The installer reads its template relative to its own directory, so these cases
# run a COPY of it beside a doctored template.

FAKE_INST_DIR="$SANDBOX/fakeinst"
mkdir -p "$FAKE_INST_DIR/launchd"
cp "$INSTALLER" "$FAKE_INST_DIR/install-revision-probe.sh"
chmod +x "$FAKE_INST_DIR/install-revision-probe.sh"
FAKE_TEMPLATE="$FAKE_INST_DIR/launchd/com.pogo.revisionprobe.plist"
fake_install() { bash "$FAKE_INST_DIR/install-revision-probe.sh" --src "$SRC" --url "$URL" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" --dry-run 2>&1; }

# The control on the control: run the copy against the REAL template first. If
# this refused, the two refusals below would prove nothing about the doctoring.
cp "$HERE/launchd/com.pogo.revisionprobe.plist" "$FAKE_TEMPLATE"
rm -f "$PLIST"
out="$(fake_install)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "the installer run from a copy, against the tracked template, still renders clean — so a refusal below is the doctoring and not the fixture"
else
    fail "the copied installer refused the REAL template (exit $rc) — the two cases below would be measuring the harness: $out"
fi

# CASE A — the placeholder guard, at a size that defeats the `| grep -q`
# spelling. The placeholder is NOT under /Users/, because an unsubstituted
# /Users/YOUR_USERNAME would be rewritten by the $HOME rule and would test
# nothing.
{
    sed 's#<string>/Users/YOUR_USERNAME/go/bin:#<string>/Elsewhere/YOUR_USERNAME/go/bin:#' \
        "$HERE/launchd/com.pogo.revisionprobe.plist" | sed '$d' | sed '$d'
    echo "    <key>Padding</key>"
    echo "    <array>"
    # ~250KB of <string> lines: more than any pipe buffer, so a `grep -q`
    # consumer closing early really does SIGPIPE the producer ahead of it.
    for i in $(seq 1 3000); do
        echo "        <string>padding $i ------------------------------------------------</string>"
    done
    echo "    </array>"
    echo "</dict>"
    echo "</plist>"
} > "$FAKE_TEMPLATE"

# First: prove the fixture really would lose the race, or this case certifies
# timing luck rather than the fix (mg-7ce7's own requirement).
pad_render="$(cat "$FAKE_TEMPLATE")"
old_idiom_rc=0
( set -uo pipefail; printf '%s\n' "$pad_render" | grep '<string>' | grep -q 'YOUR_USERNAME' ) || old_idiom_rc=$?
if [ "$old_idiom_rc" -eq 141 ]; then
    pass "the padded template reproduces mg-7ce7 on the INSTALLER's own guard: the old idiom returns 141, which reads as 'no placeholder found'"
else
    fail "the padded template did not SIGPIPE the old idiom (exit $old_idiom_rc, want 141) — this case would then be guarding nothing"
fi

rm -f "$PLIST"
out="$(fake_install)"; rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *"still has YOUR_USERNAME in a value"*) true ;; *) false ;; esac; then
    pass "the installer REFUSES a surviving placeholder even at a render size that defeats the pipefail spelling"
else
    fail "a 250KB render with a live placeholder was accepted (exit $rc) — the guard is the mg-7ce7 shape and lost its own race: $(printf '%s' "$out" | head -3)"
fi

# CASE B — the mention guard's RED ARM, which is the assertion mg-712e's report
# named and which had never been run. Drop the stamp from the template and the
# render genuinely stops mentioning it; the guard must say so, and must name the
# path it could not find.
grep -v -e '<string>--stamp</string>' \
        -e '<string>/Users/YOUR_USERNAME/\.pogo/revision-probe\.stamp</string>' \
        "$HERE/launchd/com.pogo.revisionprobe.plist" > "$FAKE_TEMPLATE"
rm -f "$PLIST"
out="$(fake_install)"; rc=$?
if [ "$rc" -eq 2 ] \
    && case "$out" in *"does not mention"*) true ;; *) false ;; esac \
    && case "$out" in *"$HOME/.pogo/revision-probe.stamp"*) true ;; *) false ;; esac; then
    pass "a template that stops naming the stamp is REFUSED, and the refusal names the missing path — the mention guard has a red arm at last"
else
    fail "a template with no stamp path rendered and was accepted (exit $rc) — the guard whose refusal text mg-712e reported has never been shown to fire: $(printf '%s' "$out" | head -3)"
fi

# And the guard must not be reachable by anything but a real omission. This is
# the mg-712e shape stated as an assertion: every path the loop can name is one
# the render just substituted in, so a clean render must never produce that
# sentence — including at a size where a pipeline-status spelling would.
cp "$HERE/launchd/com.pogo.revisionprobe.plist" "$FAKE_TEMPLATE"
rm -f "$PLIST"
mention_false_alarms=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
    out="$(fake_install)"
    case "$out" in *"does not mention"*) mention_false_alarms=$((mention_false_alarms + 1)) ;; esac
done
if [ "$mention_false_alarms" -eq 0 ]; then
    pass "10 clean renders produced 0 'does not mention' refusals — the alarm mg-712e saw is not reachable from a good render"
else
    fail "$mention_false_alarms of 10 clean renders reported a render mismatch — the guard is alarming on something other than the comparison, which is mg-712e verbatim"
fi

# ---------------------------------------------------------------------------
# 3. It refuses rather than half-installing
# ---------------------------------------------------------------------------
# A job that exists and cannot run is worse than an absent one: it appears in
# every inventory and reports nothing.
rm -f "$PLIST"
out="$(bash "$INSTALLER" --src "$SANDBOX/nope" --launch-agents-dir "$LA_DIR" \
        --launchctl "$LC_OK" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [ ! -f "$PLIST" ]; then
    pass "a --src that does not exist is refused, and nothing is written"
else
    fail "install with a missing --src exited $rc and $([ -f "$PLIST" ] && echo "WROTE a plist" || echo "wrote nothing") — output: $out"
fi

EMPTY="$SANDBOX/empty-src"
mkdir -p "$EMPTY/scripts"
out="$(bash "$INSTALLER" --src "$EMPTY" --launch-agents-dir "$LA_DIR" \
        --launchctl "$LC_OK" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [ ! -f "$PLIST" ]; then
    pass "a checkout with no revision-probe.sh is refused — arming a job that points at an absent script arms nothing"
else
    fail "install against a probe-less checkout exited $rc — output: $out"
fi

# THE ARRIVAL-ORDER REFUSAL, and it is not hypothetical — this is the defect the
# first live install actually produced (2026-08-09). The plist and the probe are
# both tracked files that reach a box at DIFFERENT times: the installer runs from
# the checkout you invoked it in, while the job points at $SRC, which the deploy
# runner's sync_src advances. Install before that sync and the job names flags
# the probe there has never heard of. The result was a LaunchAgent that was
# loaded, listed, correct-looking under `launchctl print`, and wrote
# "unknown option '--log'" once an hour — this ticket's own defect, present by
# existence and absent by effect, reproduced by its own remedy.
OLDSRC="$SANDBOX/old-src"
mkdir -p "$OLDSRC/scripts"
cat > "$OLDSRC/scripts/revision-probe.sh" <<'EOF'
#!/usr/bin/env bash
# A probe from before the ledger flags existed. It rejects what it does not know,
# which is the behaviour that makes this detectable at all.
while [ $# -gt 0 ]; do
    case "$1" in
        --repo|--url|--stale-after|--stamp) shift 2 ;;
        --quiet|--mail) shift ;;
        -h|--help) exit 0 ;;
        *) echo "revision-probe: unknown option '$1' (try --help)" >&2; exit 2 ;;
    esac
done
exit 0
EOF
chmod +x "$OLDSRC/scripts/revision-probe.sh"
rm -f "$PLIST"
out="$(bash "$INSTALLER" --src "$OLDSRC" --url "$URL" --launch-agents-dir "$LA_DIR" \
        --launchctl "$LC_OK" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [ ! -f "$PLIST" ]; then
    pass "a probe that cannot parse the job's own command line is refused, and nothing is armed"
else
    fail "the installer exited $rc against a probe that rejects the vector, and $([ -f "$PLIST" ] && echo "ARMED it anyway" || echo "wrote nothing") — an hourly 'unknown option' is a witness that is loaded, listed and dead — output: $out"
fi
if printf '%s' "$out" | grep -q 'merge --ff-only origin/main'; then
    pass "the refusal names the sync that fixes it, rather than leaving the reader to work out the arrival order"
else
    fail "the refusal does not say how to resolve it — output: $out"
fi
# The check must be by EXECUTION. A version string or a --help grep would pass
# against a probe that prints the flag in its usage text and rejects it in its
# parser, which is a real shape: revision-probe.sh's usage IS its header comment.
LIAR="$SANDBOX/liar-src"
mkdir -p "$LIAR/scripts"
cat > "$LIAR/scripts/revision-probe.sh" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in -h|--help) echo "usage: --repo --url --log --renotify --quiet --mail"; exit 0 ;; esac
echo "revision-probe: unknown option '$1'" >&2
exit 2
EOF
chmod +x "$LIAR/scripts/revision-probe.sh"
rm -f "$PLIST"
out="$(bash "$INSTALLER" --src "$LIAR" --url "$URL" --launch-agents-dir "$LA_DIR" \
        --launchctl "$LC_OK" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [ ! -f "$PLIST" ]; then
    pass "a probe whose HELP TEXT lists the flags but whose parser rejects them is still refused — the check is by execution"
else
    fail "the installer was satisfied by help text (exit $rc) — this probe's usage is its header comment, so a grep would have passed here — output: $out"
fi

# --dry-run must be exactly that: the render, and no side effect.
out="$(install_probe --dry-run)"; rc=$?
if [ "$rc" -eq 0 ] && [ ! -f "$PLIST" ] && printf '%s' "$out" | grep -q '<key>Label</key>'; then
    pass "--dry-run renders the plist and writes nothing"
else
    fail "--dry-run exited $rc and $([ -f "$PLIST" ] && echo "WROTE $PLIST" || echo "wrote nothing")"
fi

# ---------------------------------------------------------------------------
# 4. THE LOAD-BEARING CASE — the argument vector launchd will run, RUN
# ---------------------------------------------------------------------------
# Everything above proves the installer produced a well-formed job. None of it
# proves the job DOES anything, and that distinction is the entire ticket. So:
# read ProgramArguments back out of the installed plist and execute that exact
# vector. Both arms, because a check that has never produced a red is a check of
# unknown polarity, and this one's red arm is the state it exists to report.

install_probe >/dev/null 2>&1
read_args() {
    python3 - "$PLIST" <<'PY'
import plistlib, sys
with open(sys.argv[1], 'rb') as fh:
    print('\n'.join(plistlib.load(fh)['ProgramArguments']))
PY
}
# A read loop rather than `mapfile`: /bin/bash on this box is 3.2, where
# mapfile does not exist. A control that only runs under a homebrew bash is a
# control that does not run in the gate.
ARGV=()
while IFS= read -r argv_line; do ARGV+=("$argv_line"); done < <(read_args)
[ "${#ARGV[@]}" -gt 1 ] || pogo_sandbox_fail "ProgramArguments came back with ${#ARGV[@]} element(s) — nothing below would be testing the installed job"

LEDGER="$HOME/Library/Logs/pogo/revision-probe.log"
rm -f "$LEDGER"

# GREEN ARM: the stub daemon is up and answering. The job's own command line
# must run to a verdict and leave a line saying so.
out="$("${ARGV[@]}" 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] || [ "$rc" -eq 1 ]; then
    pass "the installed ProgramArguments RUN and reach a verdict (exit $rc) — the job is wired, not merely well-formed"
else
    fail "the installed argument vector exited $rc against a live daemon; launchd would run exactly this every hour — output: $out"
fi
if [ -f "$LEDGER" ] && grep -q "$(printf '%.8s' "$FAKEREV")" "$LEDGER"; then
    pass "the wired job writes a ledger line naming the revision it read from the daemon"
else
    fail "no ledger line at $LEDGER after running the installed vector — the job would report nothing on a good night, and 'no alert' would mean both healthy and dark — got: $(cat "$LEDGER" 2>&1)"
fi
green_lines="$(wc -l < "$LEDGER" | tr -d ' ')"

# RED ARM: same installed job, daemon gone. This is the failure the probe exists
# to report, and it must produce a DIFFERENT, non-zero, recorded outcome.
kill "$STUB_PID" 2>/dev/null; wait "$STUB_PID" 2>/dev/null; STUB_PID=""
out="$("${ARGV[@]}" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then
    pass "with the daemon gone the same installed job exits 2 — a check that could not run has not found its subject healthy"
else
    fail "the installed job exited $rc with nothing listening on $URL, want 2 — output: $out"
fi
red_lines="$(wc -l < "$LEDGER" | tr -d ' ')"
if [ "$red_lines" -gt "$green_lines" ] && grep -q 'UNREACHABLE' "$LEDGER"; then
    pass "the red arm appends its own ledger line, and it is distinguishable from the green one"
else
    fail "the red run did not record a distinct line — green had $green_lines, now $red_lines — ledger: $(cat "$LEDGER" 2>&1)"
fi

# ---------------------------------------------------------------------------
# 5. THE ARMING MUST NOT NEED WHAT THE DEPLOY INSTALLS
# ---------------------------------------------------------------------------
# The same independence argument as the probe's own section 5, one level up. An
# installer that shells out to `pogo` cannot arm the box whose `pogo` is ten days
# stale — which is the box that needs arming.
POISON="$SANDBOX/poison"
mkdir -p "$POISON"
for tool in go pogo pogod; do
    cat > "$POISON/$tool" <<EOF
#!/usr/bin/env bash
echo "$tool was invoked" > "$POISON/$tool.marker"
exit 66
EOF
    chmod +x "$POISON/$tool"
done
rm -f "$POISON"/*.marker "$PLIST"
out="$(PATH="$POISON:$PATH" bash "$INSTALLER" --src "$SRC" --url "$URL" \
        --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" 2>&1)"; rc=$?
invoked=""
for tool in go pogo pogod; do
    [ -f "$POISON/$tool.marker" ] && invoked="$invoked $tool"
done
if [ -n "$invoked" ]; then
    fail "the installer invoked$invoked — an arming step that needs what the deploy installs cannot arm the box whose deploy is broken"
else
    pass "the installer invoked none of go/pogo/pogod"
fi
if [ "$rc" -eq 0 ] && [ -f "$PLIST" ]; then
    pass "and it still completed the install with all three poisoned on PATH"
else
    fail "the installer exited $rc with go/pogo/pogod poisoned — output: $out"
fi

# ---------------------------------------------------------------------------
# 6. Idempotent, and --uninstall leaves the EVIDENCE behind
# ---------------------------------------------------------------------------
install_probe >/dev/null 2>&1
install_probe >/dev/null 2>&1
count="$(find "$LA_DIR" -name 'com.pogo.revisionprobe*' | wc -l | tr -d ' ')"
if [ "$count" = "1" ]; then
    pass "re-installing replaces the job rather than accumulating plists"
else
    fail "$count com.pogo.revisionprobe* files in $LA_DIR after two installs, want 1"
fi

printf 'x\n' > "$LEDGER"
STAMPFILE="$HOME/.pogo/revision-probe.stamp"
mkdir -p "$(dirname "$STAMPFILE")"; printf '1 2 3 4\n' > "$STAMPFILE"
rm -f "$LC_LOG"
out="$(bash "$INSTALLER" --launch-agents-dir "$LA_DIR" --launchctl "$LC_OK" --uninstall 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && [ ! -f "$PLIST" ] && grep -q "bootout gui/$(id -u)/com.pogo.revisionprobe" "$LC_LOG"; then
    pass "--uninstall boots the job out and removes the plist"
else
    fail "--uninstall exited $rc, plist $([ -f "$PLIST" ] && echo remains || echo gone), calls: $(cat "$LC_LOG" 2>&1)"
fi
if [ -f "$LEDGER" ] && [ -f "$STAMPFILE" ]; then
    pass "--uninstall leaves the ledger and the stamp — removing the instrument must not erase what it saw"
else
    fail "--uninstall deleted the record along with the job"
fi

# --- tally -------------------------------------------------------------------
echo
echo "=== scripts/install-revision-probe.sh controls ==="
cat "$RESULTS_FILE"
fails=$(grep -c '^FAIL:' "$RESULTS_FILE" || true)
passes=$(grep -c '^PASS:' "$RESULTS_FILE" || true)
echo "$passes passed, $fails failed"
[ "$fails" -eq 0 ] || exit 1
[ "$passes" -gt 0 ] || { echo "no assertions ran"; exit 1; }
